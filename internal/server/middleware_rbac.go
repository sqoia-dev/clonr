package server

// middleware_rbac.go — Sprint 41 Day 1
//
// RBAC shim and dual-decision logging middleware.
//
// Day 1 purpose: wire the new auth.ResolveRoles path alongside the existing
// requireRole middleware, log both decisions, assert parity. The legacy
// requireRole result remains authoritative — Day 1 makes NO behavior change.
//
// The logged lines have the form:
//
//	rbac_decision=allow rbac_reason=wildcard user_id=<id> route=POST /api/v1/nodes/{id}/reimage
//	rbac_decision=deny  rbac_reason=no_permission user_id=<id> route=...
//
// These logs are the dataset Day 3 uses to prove the RBAC switch is safe:
// every legacy Allow must pair with an rbac_decision=allow log line, and every
// legacy Deny must pair with an rbac_decision=deny log line, before the switch.
//
// requirePermission (also defined here) is the replacement middleware for routes
// that graduate to per-verb RBAC on Day 3. On Day 1 it is defined but not yet
// wired to any route.

import (
	"net/http"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/auth"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// rbacDecisionMiddleware wraps an inner handler with a dual-decision log:
//  1. It calls auth.ResolveRoles for the session user (if any) and evaluates
//     auth.Allow(resolution, verb) — the new RBAC path.
//  2. It logs the RBAC decision as WARN (not authoritative on Day 1).
//  3. It always invokes next — the legacy requireRole check is upstream and
//     remains the only gate that rejects requests.
//
// Usage: stack this AFTER apiKeyAuth and requireRole in the middleware chain:
//
//	router.With(apiKeyAuth(...), requireRole("operator"), rbacDecisionMiddleware(db, "node.reimage")).Get(...)
//
// On Day 3, requireRole is removed from the chain and rbacDecisionMiddleware
// is promoted to enforce (or replaced by requirePermission which enforces).
func rbacDecisionMiddleware(database *db.DB, verb string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := userIDFromContext(r.Context())
			if userID == "" {
				// Bearer-token auth — no user ID in context; skip RBAC logging for now.
				// Bearer keys are mapped to a scope; Day 3 adds token→role resolution.
				next.ServeHTTP(w, r)
				return
			}

			resolution, err := auth.ResolveRoles(r.Context(), database, userID)
			if err != nil {
				// Resolution failure on Day 1 is non-fatal: log and continue.
				// On Day 3 this becomes a hard fail.
				log.Warn().
					Err(err).
					Str("user_id", userID).
					Str("verb", verb).
					Msg("rbac shim: ResolveRoles failed (parity mode — legacy auth still authoritative)")
				next.ServeHTTP(w, r)
				return
			}

			decision, reason := rbacDecision(resolution, verb)
			log.Warn().
				Str("rbac_decision", decision).
				Str("rbac_reason", reason).
				Str("user_id", userID).
				Str("verb", verb).
				Str("route", r.Method+" "+r.URL.Path).
				Bool("parity_mode", true).
				Msg("rbac shim: dual-decision log (Day 1 — legacy auth still authoritative)")

			next.ServeHTTP(w, r)
		})
	}
}

// requirePermission gates a handler on the calling user holding the named
// permission verb. This is the replacement for requireRole on routes that
// graduate to per-verb RBAC.
//
// On Day 1, this function is defined but not wired to any route. Day 3 wires
// POST /api/v1/nodes/{id}/reimage as the first route to use it.
//
// Bearer API keys with KeyScopeAdmin always pass. Session users are resolved
// via auth.ResolveRoles; posix group assignments are honoured.
//
//nolint:unused // wired on Day 3
func requirePermission(database *db.DB, verb string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope := scopeFromContext(r.Context())
			if scope == "" {
				writeUnauthorized(w, "authentication required")
				return
			}
			// Bearer admin key always passes.
			if scope == api.KeyScopeAdmin {
				next.ServeHTTP(w, r)
				return
			}

			userID := userIDFromContext(r.Context())
			if userID == "" {
				// Non-admin bearer token with no user ID. Check scope-based
				// permission mapping (Sprint 42 will populate this table).
				// For now, deny non-admin scoped tokens on permission-gated routes.
				writeForbidden(w, "bearer token scope does not grant "+verb)
				return
			}

			resolution, err := auth.ResolveRoles(r.Context(), database, userID)
			if err != nil {
				log.Error().
					Err(err).
					Str("user_id", userID).
					Str("verb", verb).
					Msg("requirePermission: ResolveRoles failed")
				writeInternalError(w, "role resolution failed")
				return
			}

			if !auth.Allow(resolution, verb) {
				decision, reason := rbacDecision(resolution, verb)
				log.Info().
					Str("rbac_decision", decision).
					Str("rbac_reason", reason).
					Str("user_id", userID).
					Str("verb", verb).
					Str("route", r.Method+" "+r.URL.Path).
					Msg("requirePermission: access denied")
				writeForbidden(w, "permission denied: "+verb)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// rbacDecision returns a (decision, reason) pair for logging.
func rbacDecision(r *auth.Resolution, verb string) (string, string) {
	if r == nil {
		return "deny", "no_resolution"
	}
	if r.IsAdmin {
		return "allow", "wildcard"
	}
	if r.Permissions[verb] {
		return "allow", "exact_match"
	}
	// Check namespace wildcard (e.g. "node.*" grants "node.read").
	if idx := lastDot(verb); idx >= 0 {
		ns := verb[:idx] + ".*"
		if r.Permissions[ns] {
			return "allow", "namespace_wildcard"
		}
	}
	if len(r.Roles) == 0 {
		return "deny", "no_roles"
	}
	return "deny", "no_permission"
}

// lastDot returns the index of the last '.' in s, or -1 if none.
func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// writeInternalError writes a 500 JSON response.
func writeInternalError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"error":"` + msg + `","code":"internal_error"}`))
}
