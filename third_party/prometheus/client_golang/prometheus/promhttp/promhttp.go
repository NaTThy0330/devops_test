package promhttp

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
)

type HandlerOpts struct{}

func HandlerFor(registry *prometheus.Registry, _ HandlerOpts) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(registry.Render()))
	})
}
