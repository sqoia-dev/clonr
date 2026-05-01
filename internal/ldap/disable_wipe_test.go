// disable_wipe_test.go — Go test asserting that POST /ldap/internal/disable
// wipes the data directory by default, and preserves it when preserve_data=true.
//
// Uses a real temp-dir so the os.RemoveAll behavior is exercised against the
// filesystem rather than mocked. No live slapd or systemd required.
//
// openTestDB is defined in write_test.go (same package).
package ldap

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sqoia-dev/clustr/internal/config"
)

// makeDisableManager builds a minimal Manager wired to a temp data dir.
// The DB singleton row is seeded by migration 027 so LDAPDisable succeeds.
func makeDisableManager(t *testing.T, dataDir, configDir string) *Manager {
	t.Helper()
	database := openTestDB(t)
	cfg := config.ServerConfig{
		LDAPDataDir:   dataDir,
		LDAPConfigDir: configDir,
	}
	return New(cfg, database)
}

// TestDisableWipesDataDirByDefault verifies that calling POST /ldap/internal/disable
// without a body removes the slapd data directory.
func TestDisableWipesDataDirByDefault(t *testing.T) {
	// Create a temporary data dir that simulates an existing slapd installation.
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "ldap")
	slapdData := filepath.Join(dataDir, "data")
	slapdConfig := filepath.Join(tmp, "etc", "ldap", "slapd.d")

	for _, d := range []string{slapdData, slapdConfig} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		// Write a sentinel file so we can verify removal.
		sentinelPath := filepath.Join(d, "sentinel.txt")
		if err := os.WriteFile(sentinelPath, []byte("data"), 0644); err != nil {
			t.Fatalf("write sentinel: %v", err)
		}
	}

	m := makeDisableManager(t, dataDir, filepath.Join(tmp, "etc", "ldap"))

	// POST with empty body — default is wipe.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ldap/internal/disable", nil)
	req.ContentLength = 0
	w := httptest.NewRecorder()
	m.handleInternalDisable(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// data dir must be gone.
	if _, err := os.Stat(slapdData); !os.IsNotExist(err) {
		t.Errorf("expected slapd data dir %s to be removed, but it still exists", slapdData)
	}
}

// TestDisablePreservesDataDirWhenFlagSet verifies that {preserve_data: true}
// keeps the slapd data directory intact.
func TestDisablePreservesDataDirWhenFlagSet(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "ldap")
	slapdData := filepath.Join(dataDir, "data")
	slapdConfig := filepath.Join(tmp, "etc", "ldap", "slapd.d")

	for _, d := range []string{slapdData, slapdConfig} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	m := makeDisableManager(t, dataDir, filepath.Join(tmp, "etc", "ldap"))

	// POST with {preserve_data: true}.
	body, _ := json.Marshal(map[string]bool{"preserve_data": true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ldap/internal/disable", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	w := httptest.NewRecorder()
	m.handleInternalDisable(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// data dir must still exist.
	if _, err := os.Stat(slapdData); os.IsNotExist(err) {
		t.Errorf("expected slapd data dir %s to be preserved, but it was removed", slapdData)
	}
}
