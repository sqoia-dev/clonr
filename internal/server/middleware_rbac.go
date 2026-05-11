package server

// middleware_rbac.go — Sprint 41 Day 1 (P2 fix: shim folded into requireRoleRBAC)
//
// RBAC helpers for the dual-decision parity logging path.
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
// P2 fix (Sprint 41 review): the original rbacDecisionMiddleware was designed to
// be stacked AFTER requireRole, which meant denied requests never reached the shim
// and rbac_decision=deny was never logged for legacy-denied paths. This undermined
// the parity goal. The fix integrates RBAC decision logging directly into
// requireRoleRBAC (see middleware.go), which runs the new-path evaluation BEFORE
// the legacy gate so both allow and deny outcomes are captured.
//
// rbacDecisionMiddleware has been deleted. Use requireRoleRBAC on routes that need
// parity logging. requirePermission (also defined here) is the replacement for
// routes that graduate to per-verb RBAC on Day 3.

import (
	"net/http"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/auth"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

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
//lint:ignore U1000 wired to POST /api/v1/nodes/{id}/reimage on Day 3 (Sprint 41)
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

//lint:ignore U1000 used by requirePermission which is wired on Day 3 (Sprint 41)
func writeInternalError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"error":"` + msg + `","code":"internal_error"}`))
}
