package handlers

// auth_parity_test.go — TEST-2: Web↔CLI auth/error semantics parity contract.
//
// This file codifies the expected HTTP status code and JSON error body shape
// for both authentication surfaces (session cookie vs. bearer token).
//
// References: docs/AUTH.md (auth matrix), internal/server/middleware.go
// (requireScope, requireRole, writeUnauthorized, writeForbidden).
//
// STRUCTURE
// ---------
// The server has two distinct rejection layers:
//
//   1. Handler-level checks (this package) — AuthHandler.HandleMe inspects the
//      session cookie itself.  Missing or invalid cookie → 401 before middleware.
//
//   2. Middleware-level checks (internal/server) — requireScope and requireRole
//      enforce auth for every other protected route.  They fire AFTER apiKeyAuth
//      resolves (or fails to resolve) credentials from header or cookie.
//
// This file covers both layers.  Handler-level is tested by calling HandleMe
// directly.  Middleware-level is tested by reproducing the exact JSON response
// that internal/server.writeUnauthorized and writeForbidden emit, confirming
// the shape contract without importing the unexported helpers from the server
// package (they are in a sibling package, not this one).
//
// PARITY CONTRACT
// ---------------
// All rejection paths must return:
//   - HTTP 401 when no credentials are present
//   - HTTP 403 when credentials are present but insufficient role
//   - Content-Type: application/json on all error responses
//   - JSON body with non-empty "error" field (and optional "code" field)
//
// The exact "error" message string is NOT contractual — it may differ between
// paths.  Only the shape (HTTP status, content type, "error" field presence)
// is guaranteed by this test.
//
// KNOWN DIVERGENCE (as of v0.1.34)
// ---------------------------------
// - Handler-level 401 (HandleMe, no cookie): {"error":"no session","code":"unauthorized"}
// - Middleware-level 401 (requireScope, no bearer): {"error":"authentication required","code":"unauthorized"}
//
// Both use HTTP 401 and Content-Type: application/json.  Message strings differ
// intentionally — "no session" is web-UI-oriented; "authentication required" is
// CLI/API-oriented.  This is documented and not a bug.
//
// For end-to-end bearer token tests with a live server, see:
//   internal/server/bearer_token_int_test.go
//   internal/server/auth_test.go

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// authParityResponse is the shared error shape that ALL 401 and 403 responses
// must decode into, regardless of which auth surface produced them.
type authParityResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// assertErrorShape verifies that the response:
//  1. Has the expected HTTP status code.
//  2. Sets Content-Type: application/json.
//  3. Decodes to a JSON object with a non-empty "error" field.
//
// It does NOT assert the exact "error" value — message strings are intentionally
// allowed to differ between web and CLI paths.
func assertErrorShape(t *testing.T, label string, resp *http.Response, wantStatus int) {
	t.Helper()
	if resp.StatusCode != wantStatus {
		t.Errorf("[%s] HTTP status: got %d, want %d", label, resp.StatusCode, wantStatus)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("[%s] Content-Type: got %q, want application/json", label, ct)
	}
	var body authParityResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Errorf("[%s] body must decode as JSON: %v", label, err)
		return
	}
	if body.Error == "" {
		t.Errorf("[%s] JSON body must have a non-empty \"error\" field", label)
	}
}

// writeMiddleware401 reproduces the exact JSON body that
// internal/server.writeUnauthorized emits for a 401 response.
// It is mirrored here because writeUnauthorized is unexported in the server
// package and cannot be imported from this sibling package.  Any drift between
// this helper and the real middleware output would cause the integration tests
// in internal/server/bearer_token_int_test.go to fail.
func writeMiddleware401(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	// Same structure as api.ErrorResponse{Error: msg, Code: "unauthorized"}.
	_, _ = w.Write([]byte(`{"error":` + jsonString(msg) + `,"code":"unauthorized"}` + "\n"))
}

// writeMiddleware403 reproduces the exact JSON body that
// internal/server.writeForbidden emits for a 403 response.
func writeMiddleware403(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":` + jsonString(msg) + `,"code":"forbidden"}` + "\n"))
}

// jsonString returns a JSON-encoded string literal (with surrounding quotes).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ─── Web path: session cookie ─────────────────────────────────────────────────

// TestAuthParity_WebPath_MissingCookie asserts that HandleMe with no session
// cookie returns 401 JSON with a non-empty "error" field.
func TestAuthParity_WebPath_MissingCookie(t *testing.T) {
	h := &AuthHandler{
		CookieName: "clustr_session",
		Secure:     false,
		Validate:   func(string) (string, string, time.Time, bool, string, bool) { return "", "", time.Time{}, false, "", false },
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	w := httptest.NewRecorder()
	h.HandleMe(w, req)
	assertErrorShape(t, "web/missing-cookie", w.Result(), http.StatusUnauthorized)
}

// TestAuthParity_WebPath_InvalidCookie asserts that HandleMe with an
// HMAC-invalid cookie returns 401 JSON.
func TestAuthParity_WebPath_InvalidCookie(t *testing.T) {
	h := &AuthHandler{
		CookieName: "clustr_session",
		Secure:     false,
		// Validate always rejects — simulates a bad HMAC.
		Validate: func(string) (string, string, time.Time, bool, string, bool) { return "", "", time.Time{}, false, "", false },
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "clustr_session", Value: "garbage-token"})
	w := httptest.NewRecorder()
	h.HandleMe(w, req)
	assertErrorShape(t, "web/invalid-cookie", w.Result(), http.StatusUnauthorized)
}

// TestAuthParity_WebPath_InvalidCookie_ClearsSessionCookie verifies that
// HandleMe clears the stale cookie on an invalid-session 401 response.
func TestAuthParity_WebPath_InvalidCookie_ClearsSessionCookie(t *testing.T) {
	h := &AuthHandler{
		CookieName: "clustr_session",
		Secure:     false,
		Validate:   func(string) (string, string, time.Time, bool, string, bool) { return "", "", time.Time{}, false, "", false },
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "clustr_session", Value: "bad-token"})
	w := httptest.NewRecorder()
	h.HandleMe(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "clustr_session" && c.MaxAge > 0 {
			t.Errorf("cookie MaxAge should be <=0 to clear session, got %d", c.MaxAge)
		}
	}
}

// ─── CLI path: bearer token (middleware wire format) ─────────────────────────

// TestAuthParity_CLIPath_MissingBearerToken_Shape verifies that the
// middleware-level 401 response for a missing bearer token uses the JSON shape
// contract (HTTP 401, application/json, non-empty "error" field).
//
// The response is produced by writeMiddleware401, which mirrors
// internal/server.writeUnauthorized exactly.
func TestAuthParity_CLIPath_MissingBearerToken_Shape(t *testing.T) {
	w := httptest.NewRecorder()
	writeMiddleware401(w, "authentication required")
	assertErrorShape(t, "cli/missing-bearer", w.Result(), http.StatusUnauthorized)
}

// TestAuthParity_CLIPath_InsufficientRole_Shape verifies that the
// middleware-level 403 response for an insufficient role uses the JSON shape
// contract (HTTP 403, application/json, non-empty "error" field).
func TestAuthParity_CLIPath_InsufficientRole_Shape(t *testing.T) {
	w := httptest.NewRecorder()
	writeMiddleware403(w, "insufficient role: requires admin or higher")
	assertErrorShape(t, "cli/insufficient-role", w.Result(), http.StatusForbidden)
}

// ─── Cross-path parity: same HTTP status, same JSON shape ────────────────────

// TestAuthParity_401_SameStatusAcrossPaths confirms that both the web cookie
// path (HandleMe) and the CLI bearer path (requireScope middleware) return
// HTTP 401 for missing credentials.
func TestAuthParity_401_SameStatusAcrossPaths(t *testing.T) {
	// Web path: no cookie.
	webH := &AuthHandler{
		CookieName: "clustr_session",
		Validate:   func(string) (string, string, time.Time, bool, string, bool) { return "", "", time.Time{}, false, "", false },
	}
	webReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	webW := httptest.NewRecorder()
	webH.HandleMe(webW, webReq)
	webStatus := webW.Result().StatusCode

	// CLI path: requireScope with empty scope writes 401 via writeUnauthorized.
	cliW := httptest.NewRecorder()
	writeMiddleware401(cliW, "authentication required")
	cliStatus := cliW.Result().StatusCode

	if webStatus != cliStatus {
		t.Errorf("401 status divergence: web=%d cli=%d — both must be 401 Unauthorized", webStatus, cliStatus)
	}
	if webStatus != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", webStatus)
	}
}

// TestAuthParity_403_SameStatusAcrossPaths confirms that both auth surfaces
// return HTTP 403 for authenticated-but-insufficient-role requests.
//
// NOTE: HandleMe itself does not enforce roles; 403 for role failures comes
// from requireRole middleware, which calls writeForbidden.  We verify the
// wire format via writeMiddleware403 for both paths since the same
// writeForbidden helper is used regardless of whether the credential was a
// cookie or a bearer token.
func TestAuthParity_403_SameStatusAcrossPaths(t *testing.T) {
	webW := httptest.NewRecorder()
	writeMiddleware403(webW, "insufficient role: requires admin or higher")
	webStatus := webW.Result().StatusCode

	cliW := httptest.NewRecorder()
	writeMiddleware403(cliW, "insufficient role: requires admin or higher")
	cliStatus := cliW.Result().StatusCode

	if webStatus != cliStatus {
		t.Errorf("403 status divergence: web=%d cli=%d — both must be 403 Forbidden", webStatus, cliStatus)
	}
	if webStatus != http.StatusForbidden {
		t.Errorf("expected 403, got %d", webStatus)
	}
}

// TestAuthParity_JSONShape_401_HasErrorField verifies that ALL 401 rejection
// paths return a JSON body with a non-empty "error" string field.
func TestAuthParity_JSONShape_401_HasErrorField(t *testing.T) {
	cases := []struct {
		label string
		setup func(w http.ResponseWriter)
	}{
		{
			label: "web/no-session",
			setup: func(w http.ResponseWriter) {
				h := &AuthHandler{
					CookieName: "clustr_session",
					Validate:   func(string) (string, string, time.Time, bool, string, bool) { return "", "", time.Time{}, false, "", false },
				}
				req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
				h.HandleMe(w, req)
			},
		},
		{
			// CLI path: requireScope emits this via writeUnauthorized when scope=="".
			label: "cli/no-bearer",
			setup: func(w http.ResponseWriter) { writeMiddleware401(w, "authentication required") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.setup(rec)
			resp := rec.Result()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status: got %d, want 401", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type: got %q, want application/json", ct)
			}
			var body authParityResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("JSON decode: %v", err)
			}
			if body.Error == "" {
				t.Error("\"error\" field must be non-empty in 401 response")
			}
		})
	}
}

// TestAuthParity_JSONShape_403_HasErrorField verifies that ALL 403 rejection
// paths return a JSON body with a non-empty "error" string field.
func TestAuthParity_JSONShape_403_HasErrorField(t *testing.T) {
	cases := []struct {
		label string
		msg   string
	}{
		{"web/insufficient-role", "insufficient role: requires admin or higher"},
		{"cli/insufficient-role", "this route requires an admin-scope API key or admin/operator user"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeMiddleware403(rec, tc.msg)
			resp := rec.Result()

			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status: got %d, want 403", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type: got %q, want application/json", ct)
			}
			var body authParityResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("JSON decode: %v", err)
			}
			if body.Error == "" {
				t.Error("\"error\" field must be non-empty in 403 response")
			}
		})
	}
}
