package handlers

// nodes_boot_settings_test.go — Sprint 34 BOOT-SETTINGS-MODAL handler tests.
//
// Validates the PUT /api/v1/nodes/{id}/boot-settings contract:
//
//   - Valid policies (auto/network/os) are persisted and round-trip through
//     the API.
//   - Invalid policy values are rejected with HTTP 400.
//   - Empty-string policy normalises to 'auto' (the column is NOT NULL).
//   - kernel_cmdline length cap (4096 bytes) is enforced.
//   - kernel_cmdline NUL-byte rejection.
//   - netboot_menu_entry must reference an existing boot_entries row;
//     dangling references are rejected with HTTP 400.
//   - Pointer semantics in the request: nil = "leave alone", "" = "clear",
//     non-empty = "set".

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// putBootSettingsRequest fires UpdateBootSettings against h with the given
// JSON body, injecting nodeID into the chi URL params so chi.URLParam(r,"id")
// resolves correctly.
func putBootSettingsRequest(t *testing.T, h *NodesHandler, nodeID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/nodes/"+nodeID+"/boot-settings",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", nodeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.UpdateBootSettings(w, req)
	return w
}

// makeBootSettingsTestNode creates a minimal NodeConfig and inserts it.
func makeBootSettingsTestNode(t *testing.T, d *db.DB, id, mac, hostname string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	cfg := api.NodeConfig{
		ID:         id,
		Hostname:   hostname,
		PrimaryMAC: mac,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(t.Context(), cfg); err != nil {
		t.Fatalf("create node: %v", err)
	}
}

// TestUpdateBootSettings_HappyPath_Network exercises the canonical "set
// network policy" path: the policy is persisted and the response NodeConfig
// reflects the new value.
func TestUpdateBootSettings_HappyPath_Network(t *testing.T) {
	d := openTestDB(t)
	makeBootSettingsTestNode(t, d, "node-1", "aa:bb:cc:dd:ee:01", "n01")

	h := newNodesHandler(d)
	policy := "network"
	w := putBootSettingsRequest(t, h, "node-1", api.UpdateNodeBootSettingsRequest{
		BootOrderPolicy: &policy,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	got, err := d.GetNodeConfig(t.Context(), "node-1")
	if err != nil {
		t.Fatalf("GetNodeConfig: %v", err)
	}
	if got.BootOrderPolicy != "network" {
		t.Errorf("BootOrderPolicy = %q, want network", got.BootOrderPolicy)
	}
}

// TestUpdateBootSettings_EmptyPolicy_NormalisedToAuto verifies the column's
// NOT-NULL constraint is satisfied by the empty-string clearance path.
func TestUpdateBootSettings_EmptyPolicy_NormalisedToAuto(t *testing.T) {
	d := openTestDB(t)
	makeBootSettingsTestNode(t, d, "node-1", "aa:bb:cc:dd:ee:02", "n02")

	// Pre-set to "os" so we can observe the clear.
	pol := "os"
	if err := d.UpdateNodeBootSettings(t.Context(), "node-1", &pol, nil, nil); err != nil {
		t.Fatalf("preset: %v", err)
	}

	h := newNodesHandler(d)
	empty := ""
	w := putBootSettingsRequest(t, h, "node-1", api.UpdateNodeBootSettingsRequest{
		BootOrderPolicy: &empty,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	got, _ := d.GetNodeConfig(t.Context(), "node-1")
	if got.BootOrderPolicy != "auto" {
		t.Errorf("BootOrderPolicy = %q, want auto (cleared/normalised)", got.BootOrderPolicy)
	}
}

// TestUpdateBootSettings_InvalidPolicy_Rejected covers the validation switch.
func TestUpdateBootSettings_InvalidPolicy_Rejected(t *testing.T) {
	d := openTestDB(t)
	makeBootSettingsTestNode(t, d, "node-1", "aa:bb:cc:dd:ee:03", "n03")

	h := newNodesHandler(d)
	bogus := "first-thing-that-comes-to-mind"
	w := putBootSettingsRequest(t, h, "node-1", api.UpdateNodeBootSettingsRequest{
		BootOrderPolicy: &bogus,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "boot_order_policy must be one of") {
		t.Errorf("body = %s; want substring 'boot_order_policy must be one of'", w.Body.String())
	}
}

// TestUpdateBootSettings_KernelCmdlineLengthCap rejects oversized cmdlines.
func TestUpdateBootSettings_KernelCmdlineLengthCap(t *testing.T) {
	d := openTestDB(t)
	makeBootSettingsTestNode(t, d, "node-1", "aa:bb:cc:dd:ee:04", "n04")

	h := newNodesHandler(d)
	cmdline := strings.Repeat("a", 4097) // one byte over cap
	w := putBootSettingsRequest(t, h, "node-1", api.UpdateNodeBootSettingsRequest{
		KernelCmdline: &cmdline,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "exceeds 4096-byte cap") {
		t.Errorf("body = %s; want length-cap error", w.Body.String())
	}
}

// TestUpdateBootSettings_KernelCmdlineNULRejected verifies the NUL-byte guard.
func TestUpdateBootSettings_KernelCmdlineNULRejected(t *testing.T) {
	d := openTestDB(t)
	makeBootSettingsTestNode(t, d, "node-1", "aa:bb:cc:dd:ee:05", "n05")

	h := newNodesHandler(d)
	bad := "console=ttyS0\x00ohno"
	w := putBootSettingsRequest(t, h, "node-1", api.UpdateNodeBootSettingsRequest{
		KernelCmdline: &bad,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "must not contain NUL") {
		t.Errorf("body = %s; want NUL-rejection error", w.Body.String())
	}
}

// TestUpdateBootSettings_NetbootEntry_DanglingRejected exercises the
// boot-entries lookup; a non-existent entry id is a 400.
func TestUpdateBootSettings_NetbootEntry_DanglingRejected(t *testing.T) {
	d := openTestDB(t)
	makeBootSettingsTestNode(t, d, "node-1", "aa:bb:cc:dd:ee:06", "n06")

	h := newNodesHandler(d)
	dangling := "no-such-entry-id"
	w := putBootSettingsRequest(t, h, "node-1", api.UpdateNodeBootSettingsRequest{
		NetbootMenuEntry: &dangling,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "does not reference an existing boot entry") {
		t.Errorf("body = %s; want dangling-reference error", w.Body.String())
	}
}

// TestUpdateBootSettings_NetbootEntry_HappyPath inserts a real boot entry,
// references it from the modal payload, and verifies the persisted node row
// reflects the new pointer.
func TestUpdateBootSettings_NetbootEntry_HappyPath(t *testing.T) {
	d := openTestDB(t)
	makeBootSettingsTestNode(t, d, "node-1", "aa:bb:cc:dd:ee:07", "n07")

	now := time.Now().UTC().Truncate(time.Second)
	entry := api.BootEntry{
		ID:        "rescue-entry",
		Name:      "Rescue",
		Kind:      string(api.BootEntryKindRescue),
		KernelURL: "http://example/rescue/vmlinuz",
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := d.CreateBootEntry(t.Context(), entry); err != nil {
		t.Fatalf("CreateBootEntry: %v", err)
	}

	h := newNodesHandler(d)
	id := "rescue-entry"
	w := putBootSettingsRequest(t, h, "node-1", api.UpdateNodeBootSettingsRequest{
		NetbootMenuEntry: &id,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	got, _ := d.GetNodeConfig(t.Context(), "node-1")
	if got.NetbootMenuEntry != "rescue-entry" {
		t.Errorf("NetbootMenuEntry = %q, want rescue-entry", got.NetbootMenuEntry)
	}
}

// TestUpdateBootSettings_PointerSemantics — nil-pointer fields are NOT
// touched.  Pre-set all three fields, send a request that only updates
// boot_order_policy, and verify the other two are preserved.
func TestUpdateBootSettings_PointerSemantics_OnlyTouchedFieldsChange(t *testing.T) {
	d := openTestDB(t)
	makeBootSettingsTestNode(t, d, "node-1", "aa:bb:cc:dd:ee:08", "n08")

	now := time.Now().UTC().Truncate(time.Second)
	if err := d.CreateBootEntry(t.Context(), api.BootEntry{
		ID:        "memtest",
		Name:      "Memtest",
		Kind:      string(api.BootEntryKindMemtest),
		KernelURL: "http://example/memtest",
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateBootEntry: %v", err)
	}

	// Pre-set all three fields.
	pol := "os"
	entry := "memtest"
	cmdline := "console=ttyS0,115200n8"
	if err := d.UpdateNodeBootSettings(t.Context(), "node-1", &pol, &entry, &cmdline); err != nil {
		t.Fatalf("preset: %v", err)
	}

	// Send a request that only updates boot_order_policy.
	h := newNodesHandler(d)
	newPol := "network"
	w := putBootSettingsRequest(t, h, "node-1", api.UpdateNodeBootSettingsRequest{
		BootOrderPolicy: &newPol,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	got, _ := d.GetNodeConfig(t.Context(), "node-1")
	if got.BootOrderPolicy != "network" {
		t.Errorf("BootOrderPolicy = %q, want network", got.BootOrderPolicy)
	}
	if got.NetbootMenuEntry != "memtest" {
		t.Errorf("NetbootMenuEntry = %q, want memtest (preserved)", got.NetbootMenuEntry)
	}
	if got.KernelCmdline != "console=ttyS0,115200n8" {
		t.Errorf("KernelCmdline = %q, want preserved", got.KernelCmdline)
	}
}

// TestUpdateBootSettings_NotFound_Returns404 verifies the lookup-of-existing
// node path: a request against an unknown id returns 404, not 200/500.
func TestUpdateBootSettings_NotFound_Returns404(t *testing.T) {
	d := openTestDB(t)
	h := newNodesHandler(d)
	pol := "auto"
	w := putBootSettingsRequest(t, h, "no-such-node", api.UpdateNodeBootSettingsRequest{
		BootOrderPolicy: &pol,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}
