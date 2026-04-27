package server_test

// repo_test.go tests the /repo/* HTTP route and /repo/health endpoint.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/server"
)

// newRepoTestServer creates a test server with a populated RepoDir.
func newRepoTestServer(t *testing.T, repoDir string) *httptest.Server {
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
		AuthDevMode: true,
		LogLevel:    "error",
		RepoDir:     repoDir,
	}

	srv := server.New(cfg, database, server.BuildInfo{})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestRepoRoute_ServesFiles verifies /repo/* serves files from cfg.RepoDir.
func TestRepoRoute_ServesFiles(t *testing.T) {
	repoDir := t.TempDir()

	// Create a fake repodata/repomd.xml.
	subDir := filepath.Join(repoDir, "el9-x86_64", "repodata")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const xmlContent = "<repomd>test</repomd>"
	if err := os.WriteFile(filepath.Join(subDir, "repomd.xml"), []byte(xmlContent), 0o644); err != nil {
		t.Fatalf("write repomd.xml: %v", err)
	}

	ts := newRepoTestServer(t, repoDir)

	resp, err := http.Get(ts.URL + "/repo/el9-x86_64/repodata/repomd.xml")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != xmlContent {
		t.Errorf("body = %q, want %q", string(body), xmlContent)
	}
}

// TestRepoRoute_Returns404ForMissing verifies missing files return 404.
func TestRepoRoute_Returns404ForMissing(t *testing.T) {
	repoDir := t.TempDir()
	ts := newRepoTestServer(t, repoDir)

	resp, err := http.Get(ts.URL + "/repo/el9-x86_64/repodata/does-not-exist.xml")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestRepoRoute_CacheHeadersRepodata verifies repodata files get max-age=300.
func TestRepoRoute_CacheHeadersRepodata(t *testing.T) {
	repoDir := t.TempDir()
	subDir := filepath.Join(repoDir, "el9-x86_64", "repodata")
	_ = os.MkdirAll(subDir, 0o755)
	_ = os.WriteFile(filepath.Join(subDir, "repomd.xml"), []byte("<repomd/>"), 0o644)

	ts := newRepoTestServer(t, repoDir)

	resp, err := http.Get(ts.URL + "/repo/el9-x86_64/repodata/repomd.xml")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(cc, "max-age=300") {
		t.Errorf("Cache-Control = %q, want to contain max-age=300", cc)
	}
}

// TestRepoRoute_CacheHeadersRPM verifies RPM files get max-age=86400, immutable.
func TestRepoRoute_CacheHeadersRPM(t *testing.T) {
	repoDir := t.TempDir()
	subDir := filepath.Join(repoDir, "el9-x86_64")
	_ = os.MkdirAll(subDir, 0o755)
	_ = os.WriteFile(filepath.Join(subDir, "slurm-24.11.4-1.el9.x86_64.rpm"), []byte("fake-rpm"), 0o644)

	ts := newRepoTestServer(t, repoDir)

	resp, err := http.Get(ts.URL + "/repo/el9-x86_64/slurm-24.11.4-1.el9.x86_64.rpm")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(cc, "max-age=86400") {
		t.Errorf("Cache-Control = %q, want to contain max-age=86400", cc)
	}
	if !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want to contain immutable", cc)
	}
}

// TestRepoRoute_ByteRange verifies Range request returns 206 with correct slice.
func TestRepoRoute_ByteRange(t *testing.T) {
	repoDir := t.TempDir()
	subDir := filepath.Join(repoDir, "el9-x86_64")
	_ = os.MkdirAll(subDir, 0o755)
	const content = "0123456789abcdef"
	_ = os.WriteFile(filepath.Join(subDir, "test.rpm"), []byte(content), 0o644)

	ts := newRepoTestServer(t, repoDir)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/repo/el9-x86_64/test.rpm", nil)
	req.Header.Set("Range", "bytes=4-7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	want := content[4:8]
	if string(body) != want {
		t.Errorf("range body = %q, want %q", string(body), want)
	}
}

// TestRepoRoute_NoAuthRequired verifies /repo/* is accessible without auth credentials.
func TestRepoRoute_NoAuthRequired(t *testing.T) {
	repoDir := t.TempDir()
	subDir := filepath.Join(repoDir, "el9-x86_64", "repodata")
	_ = os.MkdirAll(subDir, 0o755)
	_ = os.WriteFile(filepath.Join(subDir, "repomd.xml"), []byte("<repomd/>"), 0o644)

	// Use a non-dev-mode server to ensure auth is enforced for /api/v1
	// but /repo/* is still accessible without a token.
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
		AuthDevMode: false, // real auth mode
		LogLevel:    "error",
		RepoDir:     repoDir,
	}
	srv := server.New(cfg, database, server.BuildInfo{})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// No Authorization header — /repo/* should still return 200.
	resp, err := http.Get(ts.URL + "/repo/el9-x86_64/repodata/repomd.xml")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (repo route should be unauthenticated)", resp.StatusCode)
	}
}

// TestRepoHealth_Empty verifies /repo/health returns empty installed list when
// no bundles are installed.
func TestRepoHealth_Empty(t *testing.T) {
	repoDir := t.TempDir()
	ts := newRepoTestServer(t, repoDir)

	resp, err := http.Get(ts.URL + "/repo/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result struct {
		Installed []json.RawMessage `json:"installed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Installed) != 0 {
		t.Errorf("installed = %d, want 0", len(result.Installed))
	}
}

// TestRepoHealth_WithBundle verifies /repo/health lists installed bundles.
func TestRepoHealth_WithBundle(t *testing.T) {
	repoDir := t.TempDir()

	// Simulate an installed bundle.
	subDir := filepath.Join(repoDir, "el9-x86_64")
	_ = os.MkdirAll(subDir, 0o755)

	iv := map[string]string{
		"distro":         "el9",
		"arch":           "x86_64",
		"slurm_version":  "24.11.4",
		"clustr_release": "1",
		"installed_at":   time.Now().UTC().Format(time.RFC3339),
		"bundle_sha256":  "abc123def456",
	}
	ivData, _ := json.Marshal(iv)
	_ = os.WriteFile(filepath.Join(subDir, ".installed-version"), ivData, 0o644)

	ts := newRepoTestServer(t, repoDir)

	resp, err := http.Get(ts.URL + "/repo/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result struct {
		Installed []struct {
			Distro       string `json:"distro"`
			Arch         string `json:"arch"`
			SlurmVersion string `json:"slurm_version"`
		} `json:"installed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Installed) != 1 {
		t.Fatalf("installed count = %d, want 1", len(result.Installed))
	}
	if result.Installed[0].Distro != "el9" {
		t.Errorf("distro = %q, want el9", result.Installed[0].Distro)
	}
	if result.Installed[0].SlurmVersion != "24.11.4" {
		t.Errorf("slurm_version = %q, want 24.11.4", result.Installed[0].SlurmVersion)
	}
}

// TestRepoHealth_NoAuthRequired verifies /repo/health is accessible without auth.
func TestRepoHealth_NoAuthRequired(t *testing.T) {
	repoDir := t.TempDir()

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
		AuthDevMode: false,
		LogLevel:    "error",
		RepoDir:     repoDir,
	}
	srv := server.New(cfg, database, server.BuildInfo{})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// No credentials.
	resp, err := http.Get(ts.URL + "/repo/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (should be unauthenticated)", resp.StatusCode)
	}
}

