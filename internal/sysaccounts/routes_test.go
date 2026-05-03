// routes_test.go — regression tests for sysaccounts HTTP routes.
//
// B-4 regression: GET /system/accounts must return 200 with an "accounts" array
// in the JSON body so that the webui sysBadge render path can iterate accounts
// without throwing a runtime error.  This test guards against any rename or
// envelope change that would break the frontend.
package sysaccounts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/sysaccounts"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// newTestRouter wires sysaccounts routes directly without the server's auth
// middleware.  These tests exercise the handler logic; auth enforcement is
// tested at the server integration level in internal/server/auth_test.go.
func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	d := openTestDB(t)
	mgr := sysaccounts.New(d, nil) // nil allocator: test specifies UIDs explicitly
	r := chi.NewRouter()
	sysaccounts.RegisterRoutes(r, mgr)
	return r
}

// TestListAccounts_ResponseShape is the B-4 regression test.
// It asserts that GET /system/accounts returns 200 with a JSON body that
// contains an "accounts" array — the field the webui iterates to render
// account rows (including the sysBadge call on each account object).
func TestListAccounts_ResponseShape(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/system/accounts", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /system/accounts: got %d, want 200", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// "accounts" key must be present and must be a JSON array.
	// The webui iterates this array and calls sysBadge on each element;
	// a missing or wrong-typed field causes a runtime error in the browser.
	raw, ok := body["accounts"]
	if !ok {
		t.Fatal(`response missing "accounts" key — webui render will throw ReferenceError`)
	}
	if _, isSlice := raw.([]any); !isSlice {
		t.Errorf(`"accounts" is %T, want []any (JSON array)`, raw)
	}
}

// TestListGroups_ResponseShape asserts GET /system/groups returns 200 with
// a "groups" array, guarding the parallel render path in the groups table.
func TestListGroups_ResponseShape(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/system/groups", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /system/groups: got %d, want 200", w.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if _, ok := body["groups"]; !ok {
		t.Fatal(`response missing "groups" key`)
	}
}

// TestListAccounts_SystemAccountField verifies that a created system account
// carries system_account=true in the response — the field sysBadge reads to
// decide whether to render the "sys" badge.
func TestListAccounts_SystemAccountField(t *testing.T) {
	d := openTestDB(t)
	mgr := sysaccounts.New(d, nil) // nil allocator: test specifies UIDs explicitly
	r := chi.NewRouter()
	sysaccounts.RegisterRoutes(r, mgr)

	// Create a system account via the manager so it appears in the list response.
	ctx := context.Background()
	if _, err := mgr.CreateAccount(ctx, api.SystemAccount{
		Username:      "munge",
		UID:           996,
		PrimaryGID:    996,
		Shell:         "/sbin/nologin",
		HomeDir:       "/var/run/munge",
		SystemAccount: true,
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/system/accounts", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /system/accounts after create: got %d, want 200", w.Code)
	}

	var body struct {
		Accounts []struct {
			Username      string `json:"username"`
			SystemAccount bool   `json:"system_account"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Accounts) != 1 {
		t.Fatalf("expected 1 account in list, got %d", len(body.Accounts))
	}
	a := body.Accounts[0]
	if a.Username != "munge" {
		t.Errorf("account username = %q, want munge", a.Username)
	}
	// sysBadge reads a.system_account to decide whether to show the "sys" badge.
	// If this field is absent or false for a system account, the badge won't render.
	if !a.SystemAccount {
		t.Error("system_account should be true in list response — sysBadge reads this field")
	}
}
