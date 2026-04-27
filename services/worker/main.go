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
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Event     string `json:"event,omitempty"`
	Status    string `json:"status,omitempty"`
	Updated   int    `json:"updated_records,omitempty"`
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
		entry.Service = "worker"
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

func (l *jsonLogger) Tick(status string, updated int) {
	l.emit(logEntry{
		Level:   "info",
		Message: "worker_tick",
		Event:   "worker_tick",
		Status:  status,
		Updated: updated,
	})
}

func (l *jsonLogger) Request(method, path string, status int, latency time.Duration) {
	l.emit(logEntry{
		Level:     "info",
		Message:   "request_completed",
		Method:    method,
		Path:      path,
		LatencyMS: latency.Milliseconds(),
		Status:    strconv.Itoa(status),
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

type dailyRecord struct {
	Date      string
	UpdatedAt time.Time
}

type recordStore struct {
	mu      sync.Mutex
	records []dailyRecord
}

func newRecordStore() *recordStore {
	now := time.Now().UTC()
	yesterday := now.Add(-24 * time.Hour)
	return &recordStore{
		records: []dailyRecord{
			{Date: yesterday.Format("2006-01-02"), UpdatedAt: yesterday},
			{Date: now.Format("2006-01-02"), UpdatedAt: now.Add(-30 * time.Minute)},
		},
	}
}

func (s *recordStore) updateTodayRecords(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	today := now.UTC().Format("2006-01-02")
	updated := 0
	for i := range s.records {
		if s.records[i].Date == today {
			s.records[i].UpdatedAt = now.UTC()
			updated++
		}
	}

	if updated == 0 {
		s.records = append(s.records, dailyRecord{Date: today, UpdatedAt: now.UTC()})
		updated = 1
	}

	return updated
}

var workerJobsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "worker_jobs_total",
		Help: "Total worker jobs processed by result.",
	},
	[]string{"result"},
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(0)
	}

	port := envOrDefault("PORT", "9090")
	intervalSeconds := envInt("WORKER_INTERVAL_SECONDS", 60)
	logger := newLogger(parseLogLevel(envOrDefault("LOG_LEVEL", "info")))
	store := newRecordStore()

	registry := prometheus.NewRegistry()
	registry.MustRegister(workerJobsTotal)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/", notFoundHandler)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           requestMiddleware(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
	defer ticker.Stop()

	jobDone := make(chan struct{})

	go func() {
		defer close(jobDone)
		logger.Info("worker server starting on port " + port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("worker server failed", err)
			stop()
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case tickTime := <-ticker.C:
				updated := store.updateTodayRecords(tickTime)
				workerJobsTotal.WithLabelValues("success").Inc()
				logger.Tick("success", updated)
			}
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("worker shutdown failed", err)
	}

	<-jobDone
	logger.Info("worker stopped")
}

func requestMiddleware(logger *jsonLogger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if r.URL.Path == "/metrics" {
			logger.Debug("metrics scraped")
		}
		logger.Request(r.Method, r.URL.Path, recorder.status, time.Since(start))
	})
}

func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "not found",
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

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
