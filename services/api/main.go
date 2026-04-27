package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type logLevel int

const (
	levelDebug logLevel = iota
	levelInfo
	levelWarn
	levelError
)

type jsonLogger struct {
	mu    sync.Mutex
	level logLevel
	enc   *json.Encoder
}

type logEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Service   string `json:"service"`
	Method    string `json:"method,omitempty"`
	Path      string `json:"path,omitempty"`
	Status    int    `json:"status,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

func newLogger(level logLevel) *jsonLogger {
	return &jsonLogger{
		level: level,
		enc:   json.NewEncoder(os.Stdout),
	}
}

func (l *jsonLogger) emit(entry logEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if entry.Service == "" {
		entry.Service = "api"
	}
	if err := l.enc.Encode(entry); err != nil {
		log.Printf("failed to write json log: %v", err)
	}
}

func (l *jsonLogger) Debug(message string) {
	if l.level > levelDebug {
		return
	}
	l.emit(logEntry{Level: "debug", Message: message})
}

func (l *jsonLogger) Info(message string) {
	if l.level > levelInfo {
		return
	}
	l.emit(logEntry{Level: "info", Message: message})
}

func (l *jsonLogger) Warn(message string) {
	if l.level > levelWarn {
		return
	}
	l.emit(logEntry{Level: "warn", Message: message})
}

func (l *jsonLogger) Error(message string, err error) {
	l.emit(logEntry{Level: "error", Message: message, Error: err.Error()})
}

func (l *jsonLogger) Request(method, path string, status int, latency time.Duration) {
	l.emit(logEntry{
		Level:     "info",
		Message:   "request_completed",
		Method:    method,
		Path:      path,
		Status:    status,
		LatencyMS: latency.Milliseconds(),
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

var (
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests processed by the API.",
		},
		[]string{"method", "path", "status"},
	)
	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path", "status"},
	)
)

func main() {
	port := envOrDefault("PORT", "8080")
	logger := newLogger(parseLogLevel(envOrDefault("LOG_LEVEL", "info")))

	registry := prometheus.NewRegistry()
	registry.MustRegister(httpRequestsTotal, httpRequestDuration)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/", notFoundHandler)

	handler := requestMiddleware(logger, mux)
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("api server starting on port " + port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("api server failed", err)
			stop()
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("api shutdown failed", err)
		return
	}

	logger.Info("api server stopped")
}

func requestMiddleware(logger *jsonLogger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		statusCode := recorder.status
		path := r.URL.Path
		latency := time.Since(start)

		httpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(statusCode)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path, strconv.Itoa(statusCode)).Observe(latency.Seconds())
		logger.Request(r.Method, path, statusCode, latency)
	})
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "not found",
	})
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "method not allowed",
	})
}

func parseLogLevel(value string) logLevel {
	switch value {
	case "debug":
		return levelDebug
	case "warn":
		return levelWarn
	case "error":
		return levelError
	default:
		return levelInfo
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
