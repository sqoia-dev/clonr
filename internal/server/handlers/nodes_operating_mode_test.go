// nodes_operating_mode_test.go — Sprint 37 DISKLESS Bundle A: PATCH
// /api/v1/nodes/{id} support for operating_mode.
//
// Coverage:
//   * happy path — each accepted enum value round-trips through the handler
//     and lands in the DB.
//   * 400 on bogus value — the API-layer enum validator rejects unknown
//     values before they reach SQLite.
//   * 400 on non-string JSON value — the body decoder catches type mismatches.
//   * absence is a no-op — omitting the key preserves the existing value.
//
// Auth-middleware coverage is intentionally out of scope here; the route is
// chained behind requireGroupAccess in server.go, exercised by the broader
// router integration tests. These tests target the handler in isolation.
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// patchNodeRequest is defined in initramfs_filter_test.go — reused here.

// TestPatchNode_OperatingMode_HappyPath_AllEnumValues confirms every accepted
// enum value can be set via PATCH and round-trips through the DB.
func TestPatchNode_OperatingMode_HappyPath_AllEnumValues(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:f0", "patch-mode-happy")
	h := newNodesHandler(d)

	for _, mode := range api.OperatingModeValues {
		w := patchNodeRequest(t, h, node.ID, map[string]any{
			"operating_mode": mode,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH %q: expected 200, got %d; body: %s", mode, w.Code, w.Body.String())
		}

		// Confirm response body carries the new value.
		var resp api.NodeConfig
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response (%q): %v", mode, err)
		}
		if resp.OperatingMode != mode {
			t.Errorf("response.OperatingMode = %q, want %q", resp.OperatingMode, mode)
		}

		// Confirm DB persisted the new value.
		got, err := d.GetNodeConfig(t.Context(), node.ID)
		if err != nil {
			t.Fatalf("GetNodeConfig (%q): %v", mode, err)
		}
		if got.OperatingMode != mode {
			t.Errorf("DB.OperatingMode = %q, want %q", got.OperatingMode, mode)
		}
	}
}

// TestPatchNode_OperatingMode_RejectsBogusValue confirms the API-layer enum
// validator returns 400 with a useful message before the request hits the DB.
func TestPatchNode_OperatingMode_RejectsBogusValue(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:f1", "patch-mode-bogus")
	h := newNodesHandler(d)

	w := patchNodeRequest(t, h, node.ID, map[string]any{
		"operating_mode": "not-a-real-mode",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bogus operating_mode, got %d; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "operating_mode") {
		t.Errorf("400 response should mention operating_mode; got: %s", body)
	}

	// Confirm DB unchanged — still the default.
	got, err := d.GetNodeConfig(t.Context(), node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig: %v", err)
	}
	if got.OperatingMode != api.OperatingModeBlockInstall {
		t.Errorf("OperatingMode after rejected PATCH = %q, want %q (unchanged)", got.OperatingMode, api.OperatingModeBlockInstall)
	}
}

// TestPatchNode_OperatingMode_RejectsEmptyString confirms an explicit empty
// string is not silently accepted as "default" — callers must send the
// canonical value (block_install) or omit the key entirely.
func TestPatchNode_OperatingMode_RejectsEmptyString(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:f2", "patch-mode-empty")
	h := newNodesHandler(d)

	// First set it to a non-default value so we can confirm the "rejected"
	// branch leaves the existing value intact rather than silently resetting
	// to the default.
	w := patchNodeRequest(t, h, node.ID, map[string]any{
		"operating_mode": api.OperatingModeStatelessNFS,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("setup PATCH: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	w = patchNodeRequest(t, h, node.ID, map[string]any{
		"operating_mode": "",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty operating_mode, got %d; body: %s", w.Code, w.Body.String())
	}

	got, err := d.GetNodeConfig(t.Context(), node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig: %v", err)
	}
	if got.OperatingMode != api.OperatingModeStatelessNFS {
		t.Errorf("OperatingMode after rejected empty-string PATCH = %q, want %q (unchanged)", got.OperatingMode, api.OperatingModeStatelessNFS)
	}
}

// TestPatchNode_OperatingMode_OmittedPreservesExisting confirms that a PATCH
// that does not include the operating_mode key leaves the existing value
// intact — the partial-update contract.
func TestPatchNode_OperatingMode_OmittedPreservesExisting(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:f3", "patch-mode-omit")
	h := newNodesHandler(d)

	// Set it to a non-default value first.
	w := patchNodeRequest(t, h, node.ID, map[string]any{
		"operating_mode": api.OperatingModeStatelessRAM,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("setup PATCH: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	// Now PATCH with an unrelated field; operating_mode must be preserved.
	w = patchNodeRequest(t, h, node.ID, map[string]any{
		"hostname": "patch-mode-omit-renamed",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("hostname PATCH: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	got, err := d.GetNodeConfig(t.Context(), node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig: %v", err)
	}
	if got.OperatingMode != api.OperatingModeStatelessRAM {
		t.Errorf("OperatingMode after omitted-key PATCH = %q, want %q (preserved)", got.OperatingMode, api.OperatingModeStatelessRAM)
	}
	if got.Hostname != "patch-mode-omit-renamed" {
		t.Errorf("Hostname = %q, want %q", got.Hostname, "patch-mode-omit-renamed")
	}
}

// TestPatchNode_OperatingMode_RejectsNonStringJSON confirms a JSON value of
// the wrong type (e.g. number) is rejected with 400 rather than panicking or
// silently coercing.
func TestPatchNode_OperatingMode_RejectsNonStringJSON(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:f4", "patch-mode-type")
	h := newNodesHandler(d)

	w := patchNodeRequest(t, h, node.ID, map[string]any{
		"operating_mode": 42,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-string operating_mode, got %d; body: %s", w.Code, w.Body.String())
	}
}
