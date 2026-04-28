// manager_test.go — unit tests for URL resolution, sentinel handling, and D18
// reseed-defaults endpoint behaviour.
package slurm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
)

// TestResolveRepoURL covers the three resolution cases for resolveRepoURL:
//   - empty string → clustr-builtin URL
//   - "clustr-builtin" sentinel → clustr-builtin URL
//   - custom URL → returned unchanged
func TestResolveRepoURL(t *testing.T) {
	const serverURL = "http://10.99.0.1:8080"

	tests := []struct {
		name      string
		serverURL string
		stored    string
		wantURL   string
	}{
		{
			name:      "empty resolves to builtin",
			serverURL: serverURL,
			stored:    "",
			wantURL:   "http://10.99.0.1:8080/repo/el9-x86_64/",
		},
		{
			name:      "sentinel resolves to builtin",
			serverURL: serverURL,
			stored:    RepoSentinelBuiltin,
			wantURL:   "http://10.99.0.1:8080/repo/el9-x86_64/",
		},
		{
			name:      "custom OpenHPC URL unchanged",
			serverURL: serverURL,
			stored:    "https://repos.openhpc.community/OpenHPC/3/EL_9",
			wantURL:   "https://repos.openhpc.community/OpenHPC/3/EL_9",
		},
		{
			name:      "custom schedmd URL unchanged",
			serverURL: serverURL,
			stored:    "https://download.schedmd.com/slurm/rhel9/",
			wantURL:   "https://download.schedmd.com/slurm/rhel9/",
		},
		{
			name:      "trailing slash on ServerURL is stripped",
			serverURL: serverURL + "/",
			stored:    RepoSentinelBuiltin,
			wantURL:   "http://10.99.0.1:8080/repo/el9-x86_64/",
		},
		{
			name:      "empty ServerURL falls back to localhost",
			serverURL: "",
			stored:    RepoSentinelBuiltin,
			wantURL:   "http://localhost:8080/repo/el9-x86_64/",
		},
		{
			name:      "alternate port in ServerURL",
			serverURL: "http://10.99.0.1:9090",
			stored:    "",
			wantURL:   "http://10.99.0.1:9090/repo/el9-x86_64/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{ServerURL: tc.serverURL}
			got := m.resolveRepoURL(tc.stored)
			if got != tc.wantURL {
				t.Errorf("resolveRepoURL(%q) with ServerURL=%q\n  got  %q\n  want %q",
					tc.stored, tc.serverURL, got, tc.wantURL)
			}
		})
	}
}

// TestRepoSentinelBuiltinValue guards that the sentinel string is not
// accidentally changed — it is irreversible once written to DB rows.
func TestRepoSentinelBuiltinValue(t *testing.T) {
	const want = "clustr-builtin"
	if RepoSentinelBuiltin != want {
		t.Errorf("RepoSentinelBuiltin changed: got %q, want %q — this is irreversible, do not rename", RepoSentinelBuiltin, want)
	}
}

// ─── D18: reseed-defaults endpoint tests ─────────────────────────────────────

// openTestManager returns a Manager backed by a fresh in-memory SQLite DB
// (all migrations applied) and a chi router with the Slurm routes registered.
// The Manager's in-memory cfg is populated so the handler can read ManagedFiles.
func openTestManager(t *testing.T) (*Manager, chi.Router) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	m := &Manager{db: d}
	// Populate the in-memory cfg so handlers have a cluster name + file list.
	m.cfg = &db.SlurmModuleConfigRow{
		Enabled:      true,
		Status:       "ready",
		ClusterName:  "test-cluster",
		ManagedFiles: []string{"slurm.conf", "cgroup.conf", "gres.conf"},
	}

	r := chi.NewRouter()
	RegisterRoutes(r, m)
	return m, r
}

// reseedResult mirrors the JSON body returned by the reseed endpoint.
type reseedResult struct {
	Reseeded []string `json:"reseeded"`
	Skipped  []struct {
		Filename string `json:"filename"`
		Reason   string `json:"reason"`
	} `json:"skipped"`
	Missing []string `json:"missing"`
}

// TestReseedDefaults_DefaultRowGetsNewVersion verifies that a file with
// is_clustr_default=1 gets a new version after hitting the reseed endpoint.
func TestReseedDefaults_DefaultRowGetsNewVersion(t *testing.T) {
	m, r := openTestManager(t)
	ctx := context.Background()

	// Seed slurm.conf as a clustr-default (version 1).
	v1, err := m.db.SlurmSaveConfigVersion(ctx, "slurm.conf",
		"ClusterName=old\nMpiDefault=pmix\n",
		"clustr-system", "Initial default template", true)
	if err != nil || v1 != 1 {
		t.Fatalf("seed v1: err=%v ver=%d", err, v1)
	}

	// Routes are registered without the /api/v1 prefix — hit them directly.
	req := httptest.NewRequest(http.MethodPost, "/slurm/configs/reseed-defaults", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var result reseedResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// slurm.conf should have been reseeded.
	found := false
	for _, f := range result.Reseeded {
		if f == "slurm.conf" {
			found = true
		}
	}
	if !found {
		t.Errorf("slurm.conf not in reseeded list; got: %+v", result)
	}

	// A new version (v2) must now be the current config.
	row, err := m.db.SlurmGetCurrentConfig(ctx, "slurm.conf")
	if err != nil {
		t.Fatalf("SlurmGetCurrentConfig: %v", err)
	}
	if row.Version != 2 {
		t.Errorf("version after reseed: got %d, want 2", row.Version)
	}
	if !row.IsClustrDefault {
		t.Errorf("IsClustrDefault after reseed: got false, want true")
	}
}

// TestReseedDefaults_OperatorRowIsSkipped verifies that a file with
// is_clustr_default=0 (operator-edited) is NOT reseeded.
func TestReseedDefaults_OperatorRowIsSkipped(t *testing.T) {
	m, r := openTestManager(t)
	ctx := context.Background()

	// Seed version 1 as clustr-default, then operator-edit to version 2.
	if _, err := m.db.SlurmSaveConfigVersion(ctx, "slurm.conf",
		"ClusterName=old\n", "clustr-system", "seed", true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	v2, err := m.db.SlurmSaveConfigVersion(ctx, "slurm.conf",
		"ClusterName=custom\n", "operator-key", "operator edit", false)
	if err != nil {
		t.Fatalf("operator edit: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/slurm/configs/reseed-defaults", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d; body: %s", rec.Code, rec.Body.String())
	}

	var result reseedResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// slurm.conf must be in skipped, not reseeded.
	for _, f := range result.Reseeded {
		if f == "slurm.conf" {
			t.Errorf("slurm.conf should be skipped (operator-edited) but appears in reseeded")
		}
	}
	found := false
	for _, s := range result.Skipped {
		if s.Filename == "slurm.conf" {
			found = true
			if s.Reason != "operator-customized" {
				t.Errorf("skip reason: got %q, want operator-customized", s.Reason)
			}
		}
	}
	if !found {
		t.Errorf("slurm.conf not in skipped list; got: %+v", result)
	}

	// Current version must still be v2 (operator's version).
	row, err := m.db.SlurmGetCurrentConfig(ctx, "slurm.conf")
	if err != nil {
		t.Fatalf("SlurmGetCurrentConfig: %v", err)
	}
	if row.Version != v2 {
		t.Errorf("version after skip: got %d, want %d (operator version unchanged)", row.Version, v2)
	}
}

// TestReseedDefaults_Idempotent verifies that calling the endpoint twice does
// not double-bump the version (i.e. each call creates exactly one new version).
func TestReseedDefaults_Idempotent(t *testing.T) {
	m, r := openTestManager(t)
	ctx := context.Background()

	// Seed slurm.conf as clustr-default.
	if _, err := m.db.SlurmSaveConfigVersion(ctx, "slurm.conf",
		"ClusterName=test\n", "clustr-system", "seed", true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First call.
	req1 := httptest.NewRequest(http.MethodPost, "/slurm/configs/reseed-defaults", nil)
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first call: status %d", rec1.Code)
	}

	// Second call.
	req2 := httptest.NewRequest(http.MethodPost, "/slurm/configs/reseed-defaults", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second call: status %d", rec2.Code)
	}

	// After two calls starting from version 1, we should be at version 3
	// (each reseed creates one new version from the current IsClustrDefault row).
	row, err := m.db.SlurmGetCurrentConfig(ctx, "slurm.conf")
	if err != nil {
		t.Fatalf("SlurmGetCurrentConfig: %v", err)
	}
	if row.Version != 3 {
		t.Errorf("version after two reseeds: got %d, want 3", row.Version)
	}
	if !row.IsClustrDefault {
		t.Errorf("IsClustrDefault: got false, want true")
	}
}
