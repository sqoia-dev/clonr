package server

// pprof.go wires net/http/pprof into the chi router.
// The pprof endpoints are only accessible to admin users (gated by requireRole("admin"))
// and only enabled when CLUSTR_PPROF_ENABLED=true.
//
// Enable:
//   CLUSTR_PPROF_ENABLED=true clustr-serverd
//
// Then profile (from a host with admin API access):
//   go tool pprof http://clustr-admin-token@host:8080/debug/pprof/profile?seconds=30
//
// The pprof middleware is inserted into the route tree at /debug/pprof.
// All pprof endpoints require an admin session or admin API key.

import (
	"net/http"
	"net/http/pprof"

	"github.com/go-chi/chi/v5"
)

// pprofIndex serves the pprof index page (list of available profiles).
func pprofIndex(w http.ResponseWriter, r *http.Request) {
	pprof.Index(w, r)
}

// pprofCmdline serves the /debug/pprof/cmdline endpoint (process command line).
func pprofCmdline(w http.ResponseWriter, r *http.Request) {
	pprof.Cmdline(w, r)
}

// pprofProfile serves the /debug/pprof/profile endpoint (30s CPU profile by default).
func pprofProfile(w http.ResponseWriter, r *http.Request) {
	pprof.Profile(w, r)
}

// pprofSymbol serves the /debug/pprof/symbol endpoint (symbol lookup).
func pprofSymbol(w http.ResponseWriter, r *http.Request) {
	pprof.Symbol(w, r)
}

// pprofTrace serves the /debug/pprof/trace endpoint (execution trace).
func pprofTrace(w http.ResponseWriter, r *http.Request) {
	pprof.Trace(w, r)
}

// pprofHandler serves named profiles (heap, goroutine, allocs, etc.) via the
// pprof.Handler factory which is keyed by chi URL parameter {name}.
func pprofHandler(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		pprof.Index(w, r)
		return
	}
	pprof.Handler(name).ServeHTTP(w, r)
}
