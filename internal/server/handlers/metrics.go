package handlers

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsHandler serves GET /metrics using the default Prometheus registry.
// It is mounted without authentication so that Prometheus scrapers can reach
// it without managing API keys. Operators who want to restrict access should
// front clustr-serverd with a reverse proxy (e.g. Caddy with basicauth).
type MetricsHandler struct{}

// ServeHTTP delegates to the standard prometheus HTTP handler.
func (h *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	promhttp.Handler().ServeHTTP(w, r)
}
