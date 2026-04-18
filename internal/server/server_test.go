package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/internal/config"
	"github.com/sqoia-dev/clonr/internal/db"
	"github.com/sqoia-dev/clonr/internal/server"
)

func newTestServer(t *testing.T) (*server.Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := config.ServerConfig{
		ListenAddr:  ":0",
		ImageDir:    filepath.Join(dir, "images"),
		DBPath:      filepath.Join(dir, "test.db"),
		AuthDevMode: true, // existing tests use a mock token; dev mode bypasses DB lookup
		LogLevel:    "error",
	}

	srv := server.New(cfg, database, server.BuildInfo{})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func authHeader() string {
	return "Bearer test-token"
}

func doJSON(t *testing.T, ts *httptest.Server, method, path string, body any, out any) *http.Response {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, ts.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return resp
}

func TestHealth(t *testing.T) {
	_, ts := newTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/health", nil)
	req.Header.Set("Authorization", authHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	var h api.HealthResponse
	_ = json.NewDecoder(resp.Body).Decode(&h)
	if h.Status != "ok" {
		t.Errorf("status field: got %s", h.Status)
	}
}

func TestAuth_RequiresToken(t *testing.T) {
	// Create a server without dev mode to verify auth enforcement.
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	cfg := config.ServerConfig{
		ListenAddr: ":0",
		ImageDir:   filepath.Join(dir, "images"),
		DBPath:     filepath.Join(dir, "test.db"),
		LogLevel:   "error",
		// AuthDevMode intentionally false — auth should be enforced.
	}
	srv := server.New(cfg, database, server.BuildInfo{})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/images", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", resp.StatusCode)
	}
}

func TestImages_CreateAndList(t *testing.T) {
	_, ts := newTestServer(t)

	createReq := api.CreateImageRequest{
		Name:    "rocky9-test",
		Version: "1.0.0",
		OS:      "Rocky Linux 9.3",
		Arch:    "x86_64",
		Format:  api.ImageFormatFilesystem,
		Tags:    []string{"test"},
	}

	var created api.BaseImage
	resp := doJSON(t, ts, http.MethodPost, "/api/v1/images", createReq, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status: got %d want 201", resp.StatusCode)
	}
	if created.ID == "" {
		t.Error("created image should have an ID")
	}
	if created.Name != "rocky9-test" {
		t.Errorf("name: got %s", created.Name)
	}
	if created.Status != api.ImageStatusBuilding {
		t.Errorf("status: got %s want building", created.Status)
	}

	// List should contain our image.
	var list api.ListImagesResponse
	doJSON(t, ts, http.MethodGet, "/api/v1/images", nil, &list)
	if list.Total != 1 {
		t.Errorf("total: got %d want 1", list.Total)
	}

	// Get by ID.
	var got api.BaseImage
	resp2 := doJSON(t, ts, http.MethodGet, "/api/v1/images/"+created.ID, nil, &got)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("get status: got %d want 200", resp2.StatusCode)
	}
	if got.ID != created.ID {
		t.Errorf("id mismatch: got %s want %s", got.ID, created.ID)
	}
}

func TestImages_NotFound(t *testing.T) {
	_, ts := newTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/images/does-not-exist", nil)
	req.Header.Set("Authorization", authHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d want 404", resp.StatusCode)
	}
}

func TestImages_Delete(t *testing.T) {
	_, ts := newTestServer(t)

	createReq := api.CreateImageRequest{Name: "to-delete", Format: api.ImageFormatBlock}
	var created api.BaseImage
	doJSON(t, ts, http.MethodPost, "/api/v1/images", createReq, &created)

	resp := doJSON(t, ts, http.MethodDelete, "/api/v1/images/"+created.ID, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete status: got %d want 204", resp.StatusCode)
	}

	// Image should be gone — GET must return 404.
	getResp, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/images/"+created.ID, nil)
	getResp.Header.Set("Authorization", authHeader())
	httpResp, _ := http.DefaultClient.Do(getResp)
	if httpResp.StatusCode != http.StatusNotFound {
		t.Errorf("get after delete: got %d want 404", httpResp.StatusCode)
	}
}

func TestImages_ValidationError(t *testing.T) {
	_, ts := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/images",
		strings.NewReader(`{"version":"1.0"}`)) // missing name
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
}

func TestNodes_CreateAndGet(t *testing.T) {
	_, ts := newTestServer(t)

	// First create an image to reference.
	var img api.BaseImage
	doJSON(t, ts, http.MethodPost, "/api/v1/images", api.CreateImageRequest{
		Name: "base", Format: api.ImageFormatFilesystem,
	}, &img)

	nodeReq := api.CreateNodeConfigRequest{
		Hostname:    "compute-01",
		FQDN:        "compute-01.hpc.local",
		PrimaryMAC:  "aa:bb:cc:dd:ee:01",
		BaseImageID: img.ID,
		Groups:      []string{"compute"},
		CustomVars:  map[string]string{"role": "worker"},
	}
	var node api.NodeConfig
	resp := doJSON(t, ts, http.MethodPost, "/api/v1/nodes", nodeReq, &node)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create node status: got %d want 201", resp.StatusCode)
	}
	if node.ID == "" {
		t.Error("node should have ID")
	}

	// Get by ID.
	var got api.NodeConfig
	doJSON(t, ts, http.MethodGet, "/api/v1/nodes/"+node.ID, nil, &got)
	if got.Hostname != "compute-01" {
		t.Errorf("hostname: got %s", got.Hostname)
	}

	// Get by MAC.
	var byMAC api.NodeConfig
	resp2 := doJSON(t, ts, http.MethodGet, "/api/v1/nodes/by-mac/aa:bb:cc:dd:ee:01", nil, &byMAC)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("by-mac status: got %d want 200", resp2.StatusCode)
	}
	if byMAC.ID != node.ID {
		t.Errorf("by-mac id: got %s want %s", byMAC.ID, node.ID)
	}
}

func TestNodes_Update(t *testing.T) {
	_, ts := newTestServer(t)

	var img api.BaseImage
	doJSON(t, ts, http.MethodPost, "/api/v1/images", api.CreateImageRequest{
		Name: "base", Format: api.ImageFormatFilesystem,
	}, &img)

	var node api.NodeConfig
	doJSON(t, ts, http.MethodPost, "/api/v1/nodes", api.CreateNodeConfigRequest{
		Hostname: "old-name", PrimaryMAC: "aa:bb:cc:dd:ee:ff", BaseImageID: img.ID,
	}, &node)

	updateReq := api.UpdateNodeConfigRequest{
		Hostname:    "new-name",
		PrimaryMAC:  "aa:bb:cc:dd:ee:ff",
		BaseImageID: img.ID,
	}
	var updated api.NodeConfig
	resp := doJSON(t, ts, http.MethodPut, "/api/v1/nodes/"+node.ID, updateReq, &updated)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status: got %d want 200", resp.StatusCode)
	}
	if updated.Hostname != "new-name" {
		t.Errorf("hostname: got %s", updated.Hostname)
	}
}

func TestNodes_Delete(t *testing.T) {
	_, ts := newTestServer(t)

	var img api.BaseImage
	doJSON(t, ts, http.MethodPost, "/api/v1/images", api.CreateImageRequest{
		Name: "base", Format: api.ImageFormatFilesystem,
	}, &img)

	var node api.NodeConfig
	doJSON(t, ts, http.MethodPost, "/api/v1/nodes", api.CreateNodeConfigRequest{
		Hostname: "to-delete", PrimaryMAC: "de:ad:be:ef:00:01", BaseImageID: img.ID,
	}, &node)

	resp := doJSON(t, ts, http.MethodDelete, "/api/v1/nodes/"+node.ID, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status: got %d want 204", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/nodes/"+node.ID, nil)
	req.Header.Set("Authorization", authHeader())
	resp2, err2 := http.DefaultClient.Do(req)
	if err2 != nil {
		t.Fatalf("after delete request: %v", err2)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("after delete: got %d want 404", resp2.StatusCode)
	}
}

func TestImages_Status(t *testing.T) {
	_, ts := newTestServer(t)

	var img api.BaseImage
	doJSON(t, ts, http.MethodPost, "/api/v1/images", api.CreateImageRequest{
		Name: "status-test", Format: api.ImageFormatFilesystem,
	}, &img)

	var status map[string]any
	resp := doJSON(t, ts, http.MethodGet, "/api/v1/images/"+img.ID+"/status", nil, &status)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if status["status"] != string(api.ImageStatusBuilding) {
		t.Errorf("status field: got %v", status["status"])
	}
}

// TestNodes_UpdatePreservesPowerProvider is a regression test for the bug where
// a PUT /api/v1/nodes/:id that omits power_provider would silently wipe stored
// Proxmox credentials, causing subsequent reimages to fail with:
//
//	"proxmox provider: must supply either (username+password) or (token_id+token_secret)"
//
// The handler must preserve existing PowerProvider credentials when the PUT body
// does not include power_provider (omitempty field absent / nil pointer).
func TestNodes_UpdatePreservesPowerProvider(t *testing.T) {
	_, ts := newTestServer(t)

	var img api.BaseImage
	doJSON(t, ts, http.MethodPost, "/api/v1/images", api.CreateImageRequest{
		Name: "base", Format: api.ImageFormatFilesystem,
	}, &img)

	// Create a node with Proxmox power provider credentials.
	var node api.NodeConfig
	doJSON(t, ts, http.MethodPost, "/api/v1/nodes", api.CreateNodeConfigRequest{
		Hostname:    "vm206",
		PrimaryMAC:  "aa:bb:cc:dd:ee:06",
		BaseImageID: img.ID,
	}, &node)

	// Set the power provider via an initial PUT (simulates operator configuring creds).
	initialUpdate := api.UpdateNodeConfigRequest{
		Hostname:    "vm206",
		PrimaryMAC:  "aa:bb:cc:dd:ee:06",
		BaseImageID: img.ID,
		PowerProvider: &api.PowerProviderConfig{
			Type: "proxmox",
			Fields: map[string]string{
				"api_url":  "https://192.168.1.223:8006",
				"node":     "pve",
				"vmid":     "206",
				"username": "root@pam",
				"password": "secret-password",
				"insecure": "true",
			},
		},
	}
	var afterFirstPUT api.NodeConfig
	resp := doJSON(t, ts, http.MethodPut, "/api/v1/nodes/"+node.ID, initialUpdate, &afterFirstPUT)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initial update: got %d want 200", resp.StatusCode)
	}

	// A second PUT that only changes the hostname and omits power_provider entirely.
	// This simulates the "innocent rename" that previously wiped credentials.
	renameUpdate := api.UpdateNodeConfigRequest{
		Hostname:    "vm206-renamed",
		PrimaryMAC:  "aa:bb:cc:dd:ee:06",
		BaseImageID: img.ID,
		// PowerProvider intentionally omitted — must be preserved from existing record.
	}
	var afterSecondPUT api.NodeConfig
	resp2 := doJSON(t, ts, http.MethodPut, "/api/v1/nodes/"+node.ID, renameUpdate, &afterSecondPUT)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("rename update: got %d want 200", resp2.StatusCode)
	}

	// Re-fetch the node from the DB to confirm stored state.
	var fetched api.NodeConfig
	doJSON(t, ts, http.MethodGet, "/api/v1/nodes/"+node.ID, nil, &fetched)

	if fetched.PowerProvider == nil {
		t.Fatal("power provider was wiped by rename PUT — regression: credentials must be preserved when power_provider is omitted from request")
	}
	if fetched.PowerProvider.Type != "proxmox" {
		t.Errorf("power provider type: got %q want \"proxmox\"", fetched.PowerProvider.Type)
	}
	// The GET response sanitizes credentials (password → "****"), so we check
	// the type and non-secret fields are intact, not the raw password.
	if fetched.PowerProvider.Fields["api_url"] != "https://192.168.1.223:8006" {
		t.Errorf("api_url field lost after rename: got %q", fetched.PowerProvider.Fields["api_url"])
	}
	if fetched.PowerProvider.Fields["username"] != "root@pam" {
		t.Errorf("username field lost after rename: got %q", fetched.PowerProvider.Fields["username"])
	}

	// Confirm ClearPowerProvider=true actually removes the provider.
	clearUpdate := api.UpdateNodeConfigRequest{
		Hostname:           "vm206-renamed",
		PrimaryMAC:         "aa:bb:cc:dd:ee:06",
		BaseImageID:        img.ID,
		ClearPowerProvider: true,
	}
	var afterClear api.NodeConfig
	resp3 := doJSON(t, ts, http.MethodPut, "/api/v1/nodes/"+node.ID, clearUpdate, &afterClear)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("clear update: got %d want 200", resp3.StatusCode)
	}
	var fetchedAfterClear api.NodeConfig
	doJSON(t, ts, http.MethodGet, "/api/v1/nodes/"+node.ID, nil, &fetchedAfterClear)
	if fetchedAfterClear.PowerProvider != nil {
		t.Errorf("ClearPowerProvider=true should have removed the provider, got: %+v", fetchedAfterClear.PowerProvider)
	}
}
