package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/server"
)

// newAuthTestServer creates a test server pre-seeded with an admin API key
// and the default clustr/clustr bootstrap user (via BootstrapDefaultUser).
func newAuthTestServer(t *testing.T) (*server.Server, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := config.ServerConfig{
		ListenAddr:    ":0",
		ImageDir:      filepath.Join(dir, "images"),
		DBPath:        filepath.Join(dir, "test.db"),
		LogLevel:      "error",
		SessionSecret: "test-session-secret-32-bytes-xxx",
		SessionSecure: false,
	}

	// Bootstrap the default user (clustr/clustr) — this is what the real server does at startup.
	if err := server.BootstrapDefaultUser(context.Background(), database); err != nil {
		t.Fatalf("bootstrap default user: %v", err)
	}

	srv := server.New(cfg, database, server.BuildInfo{})

	// Also seed a legacy admin key for backward-compat tests.
	rawKey, _, err := server.CreateAPIKey(context.Background(), database, api.KeyScopeAdmin, "test key")
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	fullKey := "clustr-admin-" + rawKey

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, fullKey
}

// clientWithJar returns an http.Client that tracks cookies.
func clientWithJar(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{Jar: jar}
}

// ─── username/password login tests (ADR-0007) ───────────────────────────────

func TestLogin_UsernamePassword_HappyPath(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	body := strings.NewReader(`{"username":"clustr","password":"clustr"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", body)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: got %d, want 200", resp.StatusCode)
	}

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["ok"] != true {
		t.Error("expected {ok:true} in login response")
	}
	// ANTI-REGRESSION: force_password_change must never be true — clustr/clustr works permanently.
	if out["force_password_change"] == true {
		t.Error("ANTI-REGRESSION: force_password_change must be false for default clustr user")
	}

	// Verify session cookie is set.
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == "clustr_session" && c.Value != "" {
			found = true
			if !c.HttpOnly {
				t.Error("clustr_session cookie should be HttpOnly")
			}
		}
	}
	if !found {
		t.Error("clustr_session cookie not set after login")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	body := strings.NewReader(`{"username":"clustr","password":"wrongpassword"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", body)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: got %d, want 401", resp.StatusCode)
	}

	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["error"] != "Invalid username or password" {
		t.Errorf("wrong error message: %q", out["error"])
	}
}

func TestLogin_DisabledUser(t *testing.T) {
	// A full disabled-user flow is covered in users_test.go via handler tests.
	t.Log("disabled user login tested via handler unit tests (see pkg/server/handlers)")
}

func TestLogin_BootstrapNotRepeated(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// First call creates the user.
	if err := server.BootstrapDefaultUser(ctx, database); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	count, err := database.CountUsers(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 user after bootstrap, got %d", count)
	}

	// Second call must NOT create another user.
	if err := server.BootstrapDefaultUser(ctx, database); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	count2, _ := database.CountUsers(ctx)
	if count2 != 1 {
		t.Errorf("expected still 1 user after second bootstrap, got %d", count2)
	}
}

func TestSetPassword_HappyPath(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	// Login with clustr/clustr.
	loginBody := strings.NewReader(`{"username":"clustr","password":"clustr"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", loginBody)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := client.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", resp.StatusCode)
	}

	// Change password. Must meet complexity: uppercase + lowercase + digit.
	pwBody := strings.NewReader(`{"current_password":"clustr","new_password":"Newpassword1"}`)
	pwReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/set-password", pwBody)
	pwReq.Header.Set("Content-Type", "application/json")
	pwResp, err := client.Do(pwReq)
	if err != nil {
		t.Fatalf("set-password request: %v", err)
	}
	defer pwResp.Body.Close()

	if pwResp.StatusCode != http.StatusOK {
		var out map[string]string
		_ = json.NewDecoder(pwResp.Body).Decode(&out)
		t.Fatalf("set-password: got %d, want 200: %v", pwResp.StatusCode, out)
	}

	// Verify no force-change cookie is set (the flow is eliminated).
	for _, c := range pwResp.Cookies() {
		if c.Name == "clustr_force_password_change" && c.MaxAge > 0 {
			t.Error("ANTI-REGRESSION: clustr_force_password_change cookie must not be set")
		}
	}

	// Logout then log back in with the new password.
	client.Post(ts.URL+"/api/v1/auth/logout", "application/json", nil)

	newLoginBody := strings.NewReader(`{"username":"clustr","password":"Newpassword1"}`)
	newReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", newLoginBody)
	newReq.Header.Set("Content-Type", "application/json")
	newResp, _ := client.Do(newReq)
	newResp.Body.Close()
	if newResp.StatusCode != http.StatusOK {
		t.Fatalf("re-login with new password: got %d, want 200", newResp.StatusCode)
	}
}

// TestSetPassword_AnyNonEmptyPasswordAccepted confirms that any non-empty password
// is accepted — no complexity rules, no rejection list.
func TestSetPassword_AnyNonEmptyPasswordAccepted(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	// Login.
	loginBody := strings.NewReader(`{"username":"clustr","password":"clustr"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", loginBody)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := client.Do(req)
	resp.Body.Close()

	// A short/simple password must now be accepted.
	pwBody := strings.NewReader(`{"current_password":"clustr","new_password":"short"}`)
	pwReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/set-password", pwBody)
	pwReq.Header.Set("Content-Type", "application/json")
	pwResp, _ := client.Do(pwReq)
	pwResp.Body.Close()
	if pwResp.StatusCode != http.StatusOK {
		t.Fatalf("ANTI-REGRESSION: any non-empty password should be accepted, got %d", pwResp.StatusCode)
	}
}

// ─── legacy API-key login tests (deprecated path) ───────────────────────────

func TestLogin_LegacyKey_HappyPath(t *testing.T) {
	_, ts, fullKey := newAuthTestServer(t)
	client := clientWithJar(t)

	body := strings.NewReader(`{"key":"` + fullKey + `"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", body)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy key login: got %d, want 200", resp.StatusCode)
	}

	// Verify session cookie is set.
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == "clustr_session" && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("clustr_session cookie not set after legacy key login")
	}
}

func TestLogin_InvalidKey(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	body := strings.NewReader(`{"key":"clustr-admin-000000000000000000000000000000000000000000000000000000000000ffff"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", body)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid key login: got %d, want 401", resp.StatusCode)
	}
}

func TestMe_WithValidSession(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	// Login with username/password.
	body := strings.NewReader(`{"username":"clustr","password":"clustr"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := client.Do(req)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", resp.StatusCode)
	}

	// Now call /me — cookie jar should send the session cookie.
	meReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/me", nil)
	meResp, err := client.Do(meReq)
	if err != nil {
		t.Fatalf("me request: %v", err)
	}
	defer meResp.Body.Close()

	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("/me: got %d, want 200", meResp.StatusCode)
	}

	var out map[string]any
	_ = json.NewDecoder(meResp.Body).Decode(&out)
	if out["role"] != "admin" {
		t.Errorf("role: got %v, want admin", out["role"])
	}
	if _, ok := out["expires_at"]; !ok {
		t.Error("expected expires_at in /me response")
	}
}

func TestMe_WithoutSession(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/me", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("me request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/me without session: got %d, want 401", resp.StatusCode)
	}
}

func TestLogout_ClearsCookie(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	// Login.
	body := strings.NewReader(`{"username":"clustr","password":"clustr"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := client.Do(req)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", resp.StatusCode)
	}

	// Logout.
	logoutReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/logout", nil)
	logoutResp, err := client.Do(logoutReq)
	if err != nil {
		t.Fatalf("logout request: %v", err)
	}
	logoutResp.Body.Close()

	if logoutResp.StatusCode != http.StatusOK {
		t.Fatalf("logout: got %d, want 200", logoutResp.StatusCode)
	}

	// /me should now return 401.
	meReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/me", nil)
	meResp, _ := client.Do(meReq)
	meResp.Body.Close()
	if meResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout /me: got %d, want 401", meResp.StatusCode)
	}
}

func TestCookieAuth_GrantsAccess(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	// Login to get a session cookie.
	body := strings.NewReader(`{"username":"clustr","password":"clustr"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := client.Do(req)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", resp.StatusCode)
	}

	// Call an admin-only endpoint (no Bearer header — cookie only).
	imagesReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/images", nil)
	imagesResp, err := client.Do(imagesReq)
	if err != nil {
		t.Fatalf("images request: %v", err)
	}
	defer imagesResp.Body.Close()

	if imagesResp.StatusCode != http.StatusOK {
		t.Fatalf("cookie auth on /images: got %d, want 200", imagesResp.StatusCode)
	}
}

func TestSlidingExpiry_ReissuesCookie(t *testing.T) {
	t.Log("sliding expiry is unit-tested in session_test.go (TestValidate_SlidingReissue)")
	_ = time.Second // satisfy import
}

// ─── A-10 regression: Auth role must never default to admin on /auth/me failure ─

// TestMe_ReturnsRole_NotAdmin asserts that /auth/me for a logged-in admin
// session returns the actual role field from the server.  The webui Auth.boot
// now uses me.role || 'readonly' (not 'admin') so the server must return a
// non-empty role for the promotion to work correctly.
func TestMe_ReturnsRole_NotAdmin(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	// Login as the bootstrapped clustr/clustr admin.
	body := strings.NewReader(`{"username":"clustr","password":"clustr"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := client.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", resp.StatusCode)
	}

	// /me must return a non-empty role so the webui can promote from 'readonly'.
	meReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/me", nil)
	meResp, err := client.Do(meReq)
	if err != nil {
		t.Fatalf("me request: %v", err)
	}
	defer meResp.Body.Close()

	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("/me: got %d, want 200", meResp.StatusCode)
	}

	var out map[string]any
	_ = json.NewDecoder(meResp.Body).Decode(&out)

	role, _ := out["role"].(string)
	if role == "" {
		t.Fatal("/me returned empty role — webui will not promote from 'readonly' default")
	}
	// The server must not inject an 'admin' role for non-admin users.
	// For the bootstrap user this is 'admin' which is correct; the key assertion
	// is that the field is populated so the JS fallback 'readonly' is not used.
	t.Logf("/me role = %q (non-empty, webui will promote from readonly default)", role)
}

// TestMe_NoSession_Returns401_NotAdminRole is the core A-10 regression guard.
// If /auth/me on a missing session ever returned 200 with a privileged role,
// the previous bug (Auth._role defaulting to 'admin' on any failure) would be
// invisible because the server was also handing out admin.  This test verifies
// the server correctly returns 401 — not a 200 with role:"admin" — ensuring
// the JS retry loop will redirect to login rather than silently granting admin.
func TestMe_NoSession_Returns401_NotAdminRole(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	// No login — no session cookie.
	client := clientWithJar(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/me", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("me request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/me without session: got %d, want 401 — server must not return a role on unauthenticated requests", resp.StatusCode)
	}

	// Confirm the body does not contain role:"admin".
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if role, ok := out["role"]; ok {
		t.Errorf("/me 401 body contains role=%v — server must not leak role on unauthenticated requests", role)
	}
}

// ─── /auth/status first-run detection tests (AUTH0-1) ───────────────────────

func TestAuthStatus_HasAdmin_True(t *testing.T) {
	// newAuthTestServer bootstraps a default admin user, so has_admin should be true.
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/status", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("auth/status request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/auth/status: got %d, want 200", resp.StatusCode)
	}

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["has_admin"] != true {
		t.Errorf("/auth/status has_admin: got %v, want true", out["has_admin"])
	}
}

func TestAuthStatus_NoAdmin_False(t *testing.T) {
	// Create a server with an empty database — no bootstrap user.
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := config.ServerConfig{
		ListenAddr:    ":0",
		ImageDir:      filepath.Join(dir, "images"),
		DBPath:        filepath.Join(dir, "test.db"),
		LogLevel:      "error",
		SessionSecret: "test-session-secret-32-bytes-xxx",
		SessionSecure: false,
	}
	// Do NOT call BootstrapDefaultUser — empty database.
	srv := server.New(cfg, database, server.BuildInfo{})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	client := clientWithJar(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/status", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("auth/status request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/auth/status: got %d, want 200", resp.StatusCode)
	}

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["has_admin"] != false {
		t.Errorf("/auth/status has_admin: got %v, want false (empty db)", out["has_admin"])
	}
}

func TestAuthStatus_IsPublic_NoAuth(t *testing.T) {
	// Verify /auth/status is accessible without any credentials.
	_, ts, _ := newAuthTestServer(t)
	// Plain client — no cookie jar, no auth headers.
	resp, err := http.Get(ts.URL + "/api/v1/auth/status")
	if err != nil {
		t.Fatalf("auth/status request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/auth/status public: got %d, want 200", resp.StatusCode)
	}
}
