package server

// pi_rbac_test.go — RBAC tests for the PI role (Sprint C.5 — C5-1-5)
//
// Covers:
//   - PI login scope maps to api.KeyScope("pi")
//   - requirePI() middleware: pi scope passes, viewer/readonly blocked
//   - PI cannot reach admin routes (requireScope(true) blocks "pi" scope)
//
// Dead-workflow tests (TestPINodeGroupOwnership, TestPIMemberRequestCreate,
// TestPIExpansionRequestCreate, TestPIUtilizationQuery) were removed in
// Sprint 43-prime Day 2 (PI-CODE-WIPE) when the backing tables were dropped.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestPIScope verifies the requirePI middleware correctly gates by scope.
func TestPIScope(t *testing.T) {
	// A handler that returns 200 if reached.
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	piMW := requirePI()

	tests := []struct {
		name     string
		scope    api.KeyScope
		wantCode int
	}{
		{"admin", api.KeyScopeAdmin, http.StatusOK},
		{"operator", api.KeyScopeOperator, http.StatusOK},
		{"pi", api.KeyScope("pi"), http.StatusOK},
		{"readonly", api.KeyScope("readonly"), http.StatusForbidden},
		{"viewer", api.KeyScope("viewer"), http.StatusForbidden},
		{"no scope", api.KeyScope(""), http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/portal/pi/groups", nil)
			if tt.scope != "" {
				ctx := context.WithValue(req.Context(), ctxKeyScope{}, tt.scope)
				req = req.WithContext(ctx)
			}
			rr := httptest.NewRecorder()
			piMW(ok).ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("scope=%q: got %d, want %d", tt.scope, rr.Code, tt.wantCode)
			}
		})
	}
}

// TestPICannotReachAdmin verifies that a pi-scoped session cannot reach admin routes
// (requireScope(adminOnly=true) blocks "pi" scope).
func TestPICannotReachAdmin(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := requireScope(true) // adminOnly=true

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil)
	ctx := context.WithValue(req.Context(), ctxKeyScope{}, api.KeyScope("pi"))
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	mw(ok).ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("pi scope should not reach admin route: got %d", rr.Code)
	}
}
