package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/internal/config"
	"github.com/sqoia-dev/clonr/internal/db"
	"github.com/sqoia-dev/clonr/internal/server"
)

// newAPIKeyTestServer creates a test server pre-seeded with a known admin key.
// Returns the server handler, httptest.Server, the full bearer token, and the db.
func newAPIKeyTestServer(t *testing.T) (*httptest.Server, *http.Client, string, *db.DB) {
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

	rawKey, _, err := server.CreateAPIKey(context.Background(), database, api.KeyScopeAdmin, "test admin key")
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	fullKey := "clonr-admin-" + rawKey

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	client := clientWithJar(t)
	return ts, client, fullKey, database
}

// adminDo performs an authenticated request with Bearer token.
func adminDo(t *testing.T, client *http.Client, ts *httptest.Server, method, path, body, token string) *http.Response {
	t.Helper()
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, ts.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// TestAPIKeys_List returns an array of non-revoked keys.
func TestAPIKeys_List(t *testing.T) {
	ts, client, token, _ := newAPIKeyTestServer(t)

	resp := adminDo(t, client, ts, http.MethodGet, "/api/v1/admin/api-keys", "", token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: got %d, want 200", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	keys, ok := out["api_keys"].([]interface{})
	if !ok {
		t.Fatal("expected api_keys array in response")
	}
	// Seeded with 1 admin key.
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
}

// TestAPIKeys_Create_Admin creates a new admin key and verifies the raw key is returned once.
func TestAPIKeys_Create_Admin(t *testing.T) {
	ts, client, token, _ := newAPIKeyTestServer(t)

	body := `{"scope":"admin","label":"ci-runner"}`
	resp := adminDo(t, client, ts, http.MethodPost, "/api/v1/admin/api-keys", body, token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d, want 201", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)

	if _, ok := out["key"]; !ok {
		t.Fatal("expected raw 'key' in create response")
	}
	rawKey, _ := out["key"].(string)
	if !strings.HasPrefix(rawKey, "clonr-admin-") {
		t.Errorf("expected clonr-admin- prefix, got %q", rawKey)
	}
	apiKey, _ := out["api_key"].(map[string]any)
	if apiKey["label"] != "ci-runner" {
		t.Errorf("label: got %v, want ci-runner", apiKey["label"])
	}
	if apiKey["scope"] != "admin" {
		t.Errorf("scope: got %v, want admin", apiKey["scope"])
	}
}

// TestAPIKeys_Create_Node creates a node-scoped key.
func TestAPIKeys_Create_Node(t *testing.T) {
	ts, client, token, _ := newAPIKeyTestServer(t)

	body := `{"scope":"node","label":"node-01-deploy","node_id":"test-node-123"}`
	resp := adminDo(t, client, ts, http.MethodPost, "/api/v1/admin/api-keys", body, token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create node key: got %d, want 201", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)

	rawKey, _ := out["key"].(string)
	if !strings.HasPrefix(rawKey, "clonr-node-") {
		t.Errorf("expected clonr-node- prefix, got %q", rawKey)
	}
	apiKey, _ := out["api_key"].(map[string]any)
	if apiKey["node_id"] != "test-node-123" {
		t.Errorf("node_id: got %v, want test-node-123", apiKey["node_id"])
	}
}

// TestAPIKeys_Create_Node_MissingNodeID requires node_id for node scope.
func TestAPIKeys_Create_Node_MissingNodeID(t *testing.T) {
	ts, client, token, _ := newAPIKeyTestServer(t)

	body := `{"scope":"node","label":"oops"}`
	resp := adminDo(t, client, ts, http.MethodPost, "/api/v1/admin/api-keys", body, token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing node_id: got %d, want 400", resp.StatusCode)
	}
}

// TestAPIKeys_Revoke revokes a key and verifies it disappears from the list.
func TestAPIKeys_Revoke(t *testing.T) {
	ts, client, token, _ := newAPIKeyTestServer(t)

	// Create a second admin key (so we can revoke one without hitting the last-key guard).
	createBody := `{"scope":"admin","label":"disposable"}`
	createResp := adminDo(t, client, ts, http.MethodPost, "/api/v1/admin/api-keys", createBody, token)
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d", createResp.StatusCode)
	}
	var created map[string]any
	_ = json.NewDecoder(createResp.Body).Decode(&created)
	apiKey, _ := created["api_key"].(map[string]any)
	id, _ := apiKey["id"].(string)

	// Revoke it.
	revokeResp := adminDo(t, client, ts, http.MethodDelete, "/api/v1/admin/api-keys/"+id, "", token)
	defer revokeResp.Body.Close()
	if revokeResp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: got %d, want 200", revokeResp.StatusCode)
	}

	// List should now show only 1 key (the original seeded key).
	listResp := adminDo(t, client, ts, http.MethodGet, "/api/v1/admin/api-keys", "", token)
	defer listResp.Body.Close()
	var listOut map[string]any
	_ = json.NewDecoder(listResp.Body).Decode(&listOut)
	keys, _ := listOut["api_keys"].([]interface{})
	if len(keys) != 1 {
		t.Fatalf("after revoke: expected 1 key, got %d", len(keys))
	}
}

// TestAPIKeys_Revoke_LastAdminKey returns 409 when revoking the only admin key.
func TestAPIKeys_Revoke_LastAdminKey(t *testing.T) {
	ts, client, token, database := newAPIKeyTestServer(t)

	// Get the seeded key's ID.
	keys, err := database.ListAPIKeys(context.Background())
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 seeded key, got %d", len(keys))
	}

	id := keys[0].ID
	resp := adminDo(t, client, ts, http.MethodDelete, "/api/v1/admin/api-keys/"+id, "", token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("last key revoke: got %d, want 409", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	code, _ := out["code"].(string)
	if code != "last_admin_key" {
		t.Errorf("code: got %q, want last_admin_key", code)
	}
}

// TestAPIKeys_Rotate mints a new key with the same label, revokes the old one.
func TestAPIKeys_Rotate(t *testing.T) {
	ts, client, token, database := newAPIKeyTestServer(t)

	// Get the seeded key's ID.
	keys, err := database.ListAPIKeys(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	oldID := keys[0].ID

	// Rotate.
	rotateResp := adminDo(t, client, ts, http.MethodPost, "/api/v1/admin/api-keys/"+oldID+"/rotate", "", token)
	defer rotateResp.Body.Close()
	if rotateResp.StatusCode != http.StatusOK {
		t.Fatalf("rotate: got %d, want 200", rotateResp.StatusCode)
	}
	var rotated map[string]any
	_ = json.NewDecoder(rotateResp.Body).Decode(&rotated)

	newRawKey, _ := rotated["key"].(string)
	if !strings.HasPrefix(newRawKey, "clonr-admin-") {
		t.Errorf("new key prefix: got %q", newRawKey)
	}
	newAPIKey, _ := rotated["api_key"].(map[string]any)
	newID, _ := newAPIKey["id"].(string)
	if newID == oldID {
		t.Error("rotated key should have a different ID")
	}

	// Old key should now be rejected (revoked).
	listResp := adminDo(t, client, ts, http.MethodGet, "/api/v1/admin/api-keys", "", token)
	defer listResp.Body.Close()
	// The old token (token) has been revoked — it should get 401.
	if listResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("old key after rotate: got %d, want 401", listResp.StatusCode)
	}
	var errOut map[string]any
	_ = json.NewDecoder(listResp.Body).Decode(&errOut)
	errCode, _ := errOut["code"].(string)
	if errCode != "key_revoked" {
		t.Errorf("error code: got %q, want key_revoked", errCode)
	}

	// New key must work.
	listResp2 := adminDo(t, client, ts, http.MethodGet, "/api/v1/admin/api-keys", "", newRawKey)
	defer listResp2.Body.Close()
	if listResp2.StatusCode != http.StatusOK {
		t.Errorf("new key after rotate: got %d, want 200", listResp2.StatusCode)
	}
}

// TestAPIKeys_ExpiredKey is rejected with key_expired error code.
func TestAPIKeys_ExpiredKey(t *testing.T) {
	ts, client, _, database := newAPIKeyTestServer(t)

	// Create a key that's already expired.
	raw, id, err := server.CreateAPIKey(context.Background(), database, api.KeyScopeAdmin, "expired")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = id

	// Manually set expires_at to the past.
	past := time.Now().Add(-time.Hour)
	_ = database.UpdateAPIKeyExpiry(context.Background(), id, &past)

	fullKey := "clonr-admin-" + raw
	resp := adminDo(t, client, ts, http.MethodGet, "/api/v1/admin/api-keys", "", fullKey)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired key: got %d, want 401", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	code, _ := out["code"].(string)
	if code != "key_expired" {
		t.Errorf("error code: got %q, want key_expired", code)
	}
}

// TestAPIKeys_RevokedKey is rejected with key_revoked error code.
func TestAPIKeys_RevokedKey(t *testing.T) {
	ts, client, token, database := newAPIKeyTestServer(t)

	// Create a second key and immediately revoke it.
	raw, _, err := server.CreateAPIKey(context.Background(), database, api.KeyScopeAdmin, "to-revoke")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get its ID.
	keys, _ := database.ListAPIKeys(context.Background())
	var targetID string
	for _, k := range keys {
		if k.Description == "to-revoke" {
			targetID = k.ID
		}
	}
	if targetID == "" {
		t.Fatal("did not find the to-revoke key")
	}

	// Revoke via the API (use the original token which still works).
	revokeResp := adminDo(t, client, ts, http.MethodDelete, "/api/v1/admin/api-keys/"+targetID, "", token)
	defer revokeResp.Body.Close()
	if revokeResp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: got %d", revokeResp.StatusCode)
	}

	// Now the revoked key should get 401 with key_revoked.
	fullKey := "clonr-admin-" + raw
	resp := adminDo(t, client, ts, http.MethodGet, "/api/v1/admin/api-keys", "", fullKey)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key: got %d, want 401", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	code, _ := out["code"].(string)
	if code != "key_revoked" {
		t.Errorf("error code: got %q, want key_revoked", code)
	}
}

// TestAPIKeys_LastUsedAt verifies last_used_at is updated asynchronously (eventually).
func TestAPIKeys_LastUsedAt(t *testing.T) {
	ts, client, token, database := newAPIKeyTestServer(t)

	// Initial request — last_used_at may be nil.
	_ = adminDo(t, client, ts, http.MethodGet, "/api/v1/admin/api-keys", "", token)

	// The async goroutine should update last_used_at. Give it a short window.
	time.Sleep(50 * time.Millisecond)

	keys, err := database.ListAPIKeys(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("no keys")
	}
	if keys[0].LastUsedAt == nil {
		t.Error("last_used_at should be set after a successful request")
	}
}
