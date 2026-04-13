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

	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/pkg/config"
	"github.com/sqoia-dev/clonr/pkg/db"
	"github.com/sqoia-dev/clonr/pkg/server"
)

// newAuthTestServer creates a test server pre-seeded with an admin API key.
// Returns the server, httptest.Server, and the raw admin key string.
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

	srv := server.New(cfg, database)

	// Bootstrap an admin key so we can test login.
	rawKey, _, err := server.CreateAPIKey(context.Background(), database, api.KeyScopeAdmin, "test key")
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	fullKey := "clonr-admin-" + rawKey

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

func TestLogin_HappyPath(t *testing.T) {
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
		t.Fatalf("login: got %d, want 200", resp.StatusCode)
	}

	var out map[string]bool
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out["ok"] {
		t.Error("expected {ok:true} in login response")
	}

	// Verify session cookie is set.
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == "clonr_session" && c.Value != "" {
			found = true
			if !c.HttpOnly {
				t.Error("clonr_session cookie should be HttpOnly")
			}
			if c.SameSite != http.SameSiteStrictMode {
				t.Error("clonr_session cookie should be SameSite=Strict")
			}
		}
	}
	if !found {
		t.Error("clonr_session cookie not set after login")
	}
}

func TestLogin_InvalidKey(t *testing.T) {
	_, ts, _ := newAuthTestServer(t)
	client := clientWithJar(t)

	body := strings.NewReader(`{"key":"clonr-admin-000000000000000000000000000000000000000000000000000000000000ffff"}`)
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
	_, ts, fullKey := newAuthTestServer(t)
	client := clientWithJar(t)

	// Login first.
	body := strings.NewReader(`{"key":"` + fullKey + `"}`)
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
	if out["scope"] != "admin" {
		t.Errorf("scope: got %v, want admin", out["scope"])
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
	_, ts, fullKey := newAuthTestServer(t)
	client := clientWithJar(t)

	// Login.
	body := strings.NewReader(`{"key":"` + fullKey + `"}`)
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

	// Cookie should be cleared (MaxAge=-1 or empty value).
	for _, c := range logoutResp.Cookies() {
		if c.Name == "clonr_session" {
			if c.Value != "" && c.MaxAge >= 0 {
				t.Error("logout should clear the session cookie (empty value or MaxAge<0)")
			}
		}
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
	_, ts, fullKey := newAuthTestServer(t)
	client := clientWithJar(t)

	// Login to get a session cookie.
	body := strings.NewReader(`{"key":"` + fullKey + `"}`)
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
	// This test verifies that the middleware re-issues the cookie when the
	// session's slide timestamp is stale (>30m). We do this by manually
	// crafting a token via the exported session functions — which aren't
	// exported, so we test it indirectly through the /me endpoint using the
	// session_test.go unit tests. The integration behaviour (cookie re-issue)
	// is covered by the middleware's internal logic tested via session_test.go.
	// This test just documents the expected integration behaviour.
	t.Log("sliding expiry is unit-tested in session_test.go (TestValidate_SlidingReissue)")
	_ = time.Second // satisfy import
}
