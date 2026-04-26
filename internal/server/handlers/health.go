package handlers

import (
	"context"
	"net/http"
	"os"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// DBPinger is satisfied by *db.DB — avoids importing the db package here.
type DBPinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler returns a simple liveness check and a readiness check.
type HealthHandler struct {
	Version    string
	CommitSHA  string
	BuildTime  string
	DB         DBPinger
	BootDir    string
	InitramfsPath string
}

// ServeHTTP handles GET /api/v1/health — liveness probe.
// Always returns 200 if the process is alive.
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.HealthResponse{
		Status:    "ok",
		Version:   h.Version,
		CommitSHA: h.CommitSHA,
		BuildTime: h.BuildTime,
	})
}

// ReadyResponse is returned by GET /api/v1/healthz/ready.
type ReadyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// ServeReady handles GET /api/v1/healthz/ready — readiness probe.
// Returns 200 if the server is ready to serve traffic, 503 otherwise.
// Checks: DB ping, boot dir exists, initramfs present.
func (h *HealthHandler) ServeReady(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]string)
	allOK := true

	// Check 1: database connectivity.
	if h.DB != nil {
		if err := h.DB.Ping(r.Context()); err != nil {
			checks["db"] = "error: " + err.Error()
			allOK = false
		} else {
			checks["db"] = "ok"
		}
	} else {
		checks["db"] = "not configured"
	}

	// Check 2: boot directory exists.
	if h.BootDir != "" {
		if _, err := os.Stat(h.BootDir); err != nil {
			checks["boot_dir"] = "missing: " + h.BootDir
			allOK = false
		} else {
			checks["boot_dir"] = "ok"
		}
	}

	// Check 3: initramfs present.
	if h.InitramfsPath != "" {
		if _, err := os.Stat(h.InitramfsPath); err != nil {
			checks["initramfs"] = "missing: " + h.InitramfsPath
			allOK = false
		} else {
			checks["initramfs"] = "ok"
		}
	}

	status := "ready"
	code := http.StatusOK
	if !allOK {
		status = "not_ready"
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, ReadyResponse{Status: status, Checks: checks})
}
