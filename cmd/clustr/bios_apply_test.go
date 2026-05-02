// bios_apply_test.go — CLI smoke tests for 'clustr bios apply' (Sprint 26).
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"encoding/json"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestBiosApplyCmd_RequiresNode verifies that 'clustr bios apply' without
// --node returns a non-zero exit (cobra required flag enforcement).
func TestBiosApplyCmd_RequiresNode(t *testing.T) {
	cmd := newBiosApplyCmd()
	cmd.SetArgs([]string{}) // no --node flag
	// Silence cobra's usage output so test output is clean.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	// Execute should fail because --node is marked required.
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error when --node flag is missing")
	}
}

// TestBiosApplyCmd_HitsApplyEndpoint verifies that providing --node sends
// a POST to /api/v1/nodes/<id>/bios/apply and prints the result.
func TestBiosApplyCmd_HitsApplyEndpoint(t *testing.T) {
	const nodeID = "apply-test-node"
	hit := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/nodes/"+nodeID+"/bios/apply" {
			hit = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(api.BiosApplyResponse{
				Applied: 2,
				Message: "settings staged to NVRAM; reboot required for changes to take effect",
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	cmd := newBiosApplyCmd()
	// Persistent flags like --server are on the parent (rootCmd); we set flagServer
	// directly on the package-level var so clientFromFlags() resolves correctly.
	origServer := flagServer
	defer func() { flagServer = origServer }()
	flagServer = srv.URL

	cmd.SetArgs([]string{"-n", nodeID})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hit {
		t.Error("expected POST /api/v1/nodes/<id>/bios/apply to be called")
	}
}

// TestBiosApplyCmd_NoChanges verifies the CLI handles applied=0 without error.
func TestBiosApplyCmd_NoChanges(t *testing.T) {
	const nodeID = "no-changes-node"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/nodes/"+nodeID+"/bios/apply" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(api.BiosApplyResponse{
				Applied: 0,
				Message: "no changes — node is already at desired state",
			})
			return
		}
		// Profile assign endpoint is not called when --profile is omitted.
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	origServer := flagServer
	defer func() { flagServer = origServer }()
	flagServer = srv.URL

	cmd := newBiosApplyCmd()
	cmd.SetArgs([]string{"-n", nodeID})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error for no-changes response: %v", err)
	}
}
