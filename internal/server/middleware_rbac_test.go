package server

// middleware_rbac_test.go — Sprint 41 Day 1 (P2 fix: shim integrated into requireRoleRBAC)
//
// Parity mode tests for the RBAC dual-decision logging path.
//
// Contract being tested (post P2 fix):
//   1. requireRoleRBAC logs rbac_decision=allow for requests the legacy gate allows.
//   2. requireRoleRBAC logs rbac_decision=deny for requests the legacy gate denies.
//      This is the bug Codex caught: the old rbacDecisionMiddleware placed AFTER
//      requireRole never saw denied requests, so deny decisions were never logged.
//   3. Bearer-token requests (no userID in context) skip the RBAC log silently.
//
// Day 1 parity rule: requireRoleRBAC NEVER changes the HTTP response.
// Only the log output changes relative to requireRole.
//
// The test verifies decisions by inspecting the zerolog output captured via
// a bytes.Buffer writer injected into the logger for the duration of the test.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// captureLog replaces the global zerolog logger with one writing to buf for
// the duration of the test, restoring the original on cleanup.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	orig := log.Logger
	log.Logger = zerolog.New(buf)
	t.Cleanup(func() { log.Logger = orig })
	return buf
}

// injectSessionContext injects the minimal context values that apiKeyAuth would
// set for a session-cookie-authenticated user: scope, userID, userRole.
func injectSessionContext(r *http.Request, scope api.KeyScope, userID, role string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxKeyScope{}, scope)
	ctx = context.WithValue(ctx, ctxKeyUserID{}, userID)
	ctx = context.WithValue(ctx, ctxKeyUserRole{}, role)
	return r.WithContext(ctx)
}

// TestRequireRole_LogsNewRBACAllowMatchingLegacyAllow verifies that when
// requireRoleRBAC allows a request via the legacy gate, it also emits an
// rbac_decision=allow log from the new RBAC path.
//
// Happy path: admin user, admin role_assignment, admin-minimum route.
// Both legacy and RBAC paths should allow the request.
func TestRequireRole_LogsNewRBACAllowMatchingLegacyAllow(t *testing.T) {
	database := newTestDB(t)
	buf := captureLog(t)

	// Seed an admin user with a direct role_assignment.
	userID := "u-rbac-allow-test"
	_, err := database.SQL().Exec(
		`INSERT INTO users (id, username, password_hash, role, must_change_password, created_at)
		 VALUES (?, 'rbac-allow', '$2a$10$testhashtesthashhhhhhhhhhh', 'admin', 0, strftime('%s','now'))`,
		userID,
	)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	_, err = database.SQL().Exec(
		`INSERT INTO role_assignments (id, role_id, subject_kind, subject_id, created_at)
		 VALUES (lower(hex(randomblob(16))), 'role-admin', 'user', ?, strftime('%s','now'))`,
		userID,
	)
	if err != nil {
		t.Fatalf("seed role_assignment: %v", err)
	}

	// Build middleware chain: requireRoleRBAC("admin", "node.reimage", db) → ok handler.
	// The RBAC log fires before the legacy gate. Both should produce allow.
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chain := requireRoleRBAC("admin", "node.reimage", database)(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/x/reimage", nil)
	req = injectSessionContext(req, api.KeyScopeAdmin, userID, "admin")
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	// Legacy path: 200 OK.
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body=%s", rr.Code, rr.Body.String())
	}

	// RBAC shim: log must contain rbac_decision=allow.
	logOutput := buf.String()
	if !strings.Contains(logOutput, "rbac_decision") {
		t.Errorf("no rbac_decision in log output; got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "allow") {
		t.Errorf("expected rbac_decision=allow in log; got: %s", logOutput)
	}
	if strings.Contains(logOutput, `"rbac_decision":"deny"`) {
		t.Errorf("unexpected rbac_decision=deny in log; got: %s", logOutput)
	}
}

// TestRequireRole_LogsNewRBACDenyEvenWhenLegacyDenies is the regression test for
// the Codex P2 bug: a request that the legacy gate denies must STILL produce an
// rbac_decision=deny log line from the new RBAC path.
//
// The old rbacDecisionMiddleware was stacked AFTER requireRole. Any request that
// legacy denied never reached rbacDecisionMiddleware, so deny decisions were
// invisible in the parity logs. requireRoleRBAC fixes this by evaluating the new
// RBAC path BEFORE the legacy gate, so both outcomes are always captured.
func TestRequireRole_LogsNewRBACDenyEvenWhenLegacyDenies(t *testing.T) {
	database := newTestDB(t)
	buf := captureLog(t)

	// Seed a viewer user — insufficient for an admin-minimum route.
	userID := "u-rbac-deny-test"
	_, err := database.SQL().Exec(
		`INSERT INTO users (id, username, password_hash, role, must_change_password, created_at)
		 VALUES (?, 'rbac-deny', '$2a$10$testhashtesthashhhhhhhhhhh', 'viewer', 0, strftime('%s','now'))`,
		userID,
	)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Assign the viewer role directly.
	_, err = database.SQL().Exec(
		`INSERT INTO role_assignments (id, role_id, subject_kind, subject_id, created_at)
		 VALUES (lower(hex(randomblob(16))), 'role-viewer', 'user', ?, strftime('%s','now'))`,
		userID,
	)
	if err != nil {
		t.Fatalf("seed role_assignment: %v", err)
	}

	// Build middleware chain: requireRoleRBAC("admin", "node.reimage", db) → ok handler.
	// The RBAC log fires BEFORE the legacy gate, so the deny decision is captured
	// even though the legacy gate is about to reject the request.
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chain := requireRoleRBAC("admin", "node.reimage", database)(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/x/reimage", nil)
	req = injectSessionContext(req, api.KeyScope("viewer"), userID, "viewer")
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	// Legacy path: 403 Forbidden (viewer cannot reach admin-minimum route).
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d; body=%s", rr.Code, rr.Body.String())
	}

	// RBAC shim: log must contain rbac_decision=deny — this is the regression check.
	// Before the P2 fix, this assertion would fail because the shim never saw the request.
	logOutput := buf.String()
	if !strings.Contains(logOutput, "rbac_decision") {
		t.Errorf("no rbac_decision in log output; got: %s — deny decisions must be captured before the legacy gate rejects", logOutput)
	}
	if !strings.Contains(logOutput, "deny") {
		t.Errorf("expected deny in log; got: %s", logOutput)
	}
}

// TestMiddlewareRBAC_BearerTokenSkipsLog verifies that Bearer-token requests
// (no userID in context) skip the RBAC log silently and the legacy gate handles
// them normally (admin Bearer keys pass, no RBAC log emitted).
func TestMiddlewareRBAC_BearerTokenSkipsLog(t *testing.T) {
	database := newTestDB(t)
	buf := captureLog(t)

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chain := requireRoleRBAC("admin", "node.reimage", database)(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/x/reimage", nil)
	// Bearer token: scope set to admin, but NO userID in context.
	// The legacy gate passes admin Bearer keys. No RBAC log should be emitted.
	ctx := context.WithValue(req.Context(), ctxKeyScope{}, api.KeyScopeAdmin)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	// Legacy gate passes admin Bearer key: 200 OK.
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	// No RBAC log should have been emitted (no userID → skip RBAC path).
	if strings.Contains(buf.String(), "rbac_decision") {
		t.Errorf("unexpected rbac_decision log for bearer token request: %s", buf.String())
	}
}
