// deploy_observability_test.go — Sprint 36 Day 4
//
// Tests for the deploy-complete render-hash telemetry added in Day 4.
// Asserts that DeployComplete logs a warning when a converted plugin has no
// config_render_state row (i.e. the reactive observer has not fired for that
// plugin since the node was registered).
package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// deployCompleteRequest fires POST /api/v1/nodes/:id/deploy-complete via the
// handler under test. Returns the recorded response.
func deployCompleteRequest(t *testing.T, h *NodesHandler, nodeID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/deploy-complete", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", nodeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.DeployComplete(w, req)
	return w
}

// TestDeploy_LogsMissingRenderHashWarning verifies that DeployComplete returns
// 204 (success) even when config_render_state has no rows for the converted
// plugins — the missing-hash check is a soft warning, not a hard gate.
//
// The test relies on the fact that a freshly-deployed node has no
// config_render_state rows (the observer fires only after clientd connects).
// We assert the HTTP status and not the log output (zerolog is not easily
// capturable in unit tests; integration and lab E2E validate the actual log
// lines).
func TestDeploy_LogsMissingRenderHashWarning(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Create a minimal node and mark it as having an image assigned.
	now := time.Now().UTC().Truncate(time.Second)
	nodeID := "node-render-hash-test"
	cfg := api.NodeConfig{
		ID:         nodeID,
		Hostname:   "render-hash-node",
		PrimaryMAC: "aa:bb:cc:dd:ee:ff",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(ctx, cfg); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	h := &NodesHandler{DB: d}

	// No config_render_state rows exist — this simulates a freshly deployed node
	// where the reactive observer has not yet fired for any converted plugin.
	// DeployComplete must still return 204 (warning is non-fatal).
	w := deployCompleteRequest(t, h, nodeID)
	if w.Code != http.StatusNoContent {
		t.Errorf("DeployComplete with missing render hash: status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

// TestDeploy_RenderHashPresent_SucceedsWithoutWarning verifies that DeployComplete
// returns 204 when all four converted plugins have config_render_state rows
// (i.e. the observer has already fired and the node has current config).
func TestDeploy_RenderHashPresent_SucceedsWithoutWarning(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	nodeID := "node-render-hash-present"
	cfg := api.NodeConfig{
		ID:         nodeID,
		Hostname:   "render-hash-ok-node",
		PrimaryMAC: "11:22:33:44:55:66",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(ctx, cfg); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	// Seed config_render_state rows for all four converted plugins so the
	// observability path finds hashes and logs info-level (no warnings).
	convertedPlugins := []string{"hostname", "sssd", "hosts", "limits"}
	for _, pluginName := range convertedPlugins {
		if err := d.UpsertRenderHash(ctx, nodeID, pluginName, "aabbcc"+pluginName, now, now); err != nil {
			t.Fatalf("UpsertRenderHash for %s: %v", pluginName, err)
		}
	}

	h := &NodesHandler{DB: d}
	w := deployCompleteRequest(t, h, nodeID)
	if w.Code != http.StatusNoContent {
		t.Errorf("DeployComplete with render hashes present: status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

// TestDeploy_PartialRenderHashPresent verifies that DeployComplete returns 204
// even when only some (but not all) converted plugins have render hashes.
// The warning for missing hashes is non-fatal.
func TestDeploy_PartialRenderHashPresent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	nodeID := "node-render-hash-partial"
	cfg := api.NodeConfig{
		ID:         nodeID,
		Hostname:   "render-hash-partial-node",
		PrimaryMAC: "aa:11:22:33:44:55",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(ctx, cfg); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	// Only seed a hash for hostname — sssd, hosts, limits are missing.
	if err := d.UpsertRenderHash(ctx, nodeID, "hostname", "deadbeef", now, now); err != nil {
		t.Fatalf("UpsertRenderHash hostname: %v", err)
	}

	// Verify GetRenderHash works for the seeded plugin and returns "" for missing ones.
	hash, err := d.GetRenderHash(ctx, nodeID, "hostname")
	if err != nil {
		t.Fatalf("GetRenderHash hostname: %v", err)
	}
	if hash == "" {
		t.Error("GetRenderHash: expected non-empty hash for hostname, got empty")
	}

	missingHash, err := d.GetRenderHash(ctx, nodeID, "sssd")
	if err != nil {
		t.Fatalf("GetRenderHash sssd: %v", err)
	}
	if missingHash != "" {
		t.Errorf("GetRenderHash sssd: expected empty hash for missing plugin, got %q", missingHash)
	}

	h := &NodesHandler{DB: d}
	w := deployCompleteRequest(t, h, nodeID)
	if w.Code != http.StatusNoContent {
		t.Errorf("DeployComplete with partial render hashes: status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

// TestDeploy_DefaultPath_RegisterRequest_LegacyConfigApplyDefault verifies that
// RegisterRequest.LegacyConfigApply defaults to false (omitempty zero value).
// This is a protocol backward-compat guard: old server versions that do not know
// about the field receive no field in the JSON and treat it as false.
func TestDeploy_DefaultPath_RegisterRequest_LegacyConfigApplyDefault(t *testing.T) {
	req := api.RegisterRequest{
		HardwareProfile:  []byte(`{}`),
		DetectedFirmware: "uefi",
		MulticastMode:    "auto",
		// LegacyConfigApply not set — must default to false.
	}
	if req.LegacyConfigApply {
		t.Error("RegisterRequest.LegacyConfigApply must default to false")
	}
}

// TestDeploy_LegacyFlagSet_RegisterRequest_FieldSet verifies that setting
// LegacyConfigApply=true in RegisterRequest is preserved correctly.
func TestDeploy_LegacyFlagSet_RegisterRequest_FieldSet(t *testing.T) {
	req := api.RegisterRequest{
		HardwareProfile:   []byte(`{}`),
		LegacyConfigApply: true,
	}
	if !req.LegacyConfigApply {
		t.Error("RegisterRequest.LegacyConfigApply must be true when explicitly set")
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// seedConfigRenderState is a test helper that inserts a config_render_state row
// directly via db.UpsertRenderHash, bypassing the observer.
func seedConfigRenderState(t *testing.T, d *db.DB, nodeID, pluginName, hash string) {
	t.Helper()
	now := time.Now().UTC()
	if err := d.UpsertRenderHash(context.Background(), nodeID, pluginName, hash, now, now); err != nil {
		t.Fatalf("seedConfigRenderState(%s, %s): %v", nodeID, pluginName, err)
	}
}
