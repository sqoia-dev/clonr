package main

// stats_resolve_test.go — UX-17: verify that resolveStatsNode delegates to
// fetchHealthSel (the same helper used by `clustr exec` and `clustr cp`)
// rather than hand-rolling its own health query.
//
// The test spins up a local httptest server that emulates the cluster health
// endpoint and asserts that resolveStatsNode reaches the same route with the
// same query parameters that fetchHealthSel would produce.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/selector"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// makeHealthServer builds a minimal httptest.Server that serves
// GET /api/v1/cluster/health and returns one node with the given ID.
// It records every request URL so tests can inspect query parameters.
func makeHealthServer(t *testing.T, nodeID, hostname string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/cluster/health") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		paths = append(paths, r.URL.RequestURI())
		resp := cliNodeHealthResponse{
			Nodes: []cliNodeHealthEntry{
				{NodeID: nodeID, Name: hostname, Reachable: true, Status: "deployed_verified"},
			},
			TotalNodes:  1,
			Reachable:   1,
			Unreachable: 0,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return srv, &paths
}

// TestResolveStatsNode_UsesHealthEndpoint verifies that resolveStatsNode routes
// through the shared health endpoint (same as fetchHealthSel) rather than a
// bespoke implementation.  Regression guard for UX-17.
func TestResolveStatsNode_UsesHealthEndpoint(t *testing.T) {
	const wantNodeID = "aaaabbbb-cccc-dddd-eeee-ffffffffffff"
	srv, paths := makeHealthServer(t, wantNodeID, "node-01")
	defer srv.Close()

	// Wire the CLI to point at the test server.
	origServer := flagServer
	defer func() { flagServer = origServer }()
	flagServer = srv.URL

	sel := selector.SelectorSet{Nodes: "node-01"}
	got, err := resolveStatsNode(sel)
	if err != nil {
		t.Fatalf("resolveStatsNode: unexpected error: %v", err)
	}
	if got != wantNodeID {
		t.Errorf("resolveStatsNode returned %q, want %q", got, wantNodeID)
	}

	// The health endpoint must have been called exactly once.
	if len(*paths) != 1 {
		t.Fatalf("expected 1 health endpoint call, got %d: %v", len(*paths), *paths)
	}
	if !strings.Contains((*paths)[0], "/api/v1/cluster/health") {
		t.Errorf("expected call to /api/v1/cluster/health, got %q", (*paths)[0])
	}
}

// TestResolveStatsNode_SameRouteAsFetchHealthSel verifies that resolveStatsNode
// and fetchHealthSel hit the same URL path for an identical selector.
// This is the core UX-17 contract: stats node resolution must be consistent
// with exec/cp node resolution.
func TestResolveStatsNode_SameRouteAsFetchHealthSel(t *testing.T) {
	const wantNodeID = "11112222-3333-4444-5555-666677778888"
	srv, paths := makeHealthServer(t, wantNodeID, "node-02")
	defer srv.Close()

	origServer := flagServer
	defer func() { flagServer = origServer }()
	flagServer = srv.URL

	sel := selector.SelectorSet{Nodes: "node-02"}

	// Call resolveStatsNode — captures path[0].
	if _, err := resolveStatsNode(sel); err != nil {
		t.Fatalf("resolveStatsNode: %v", err)
	}
	statsPath := (*paths)[0]

	// Reset paths and call fetchHealthSel directly — captures path[1].
	*paths = nil
	if _, err := fetchHealthSel(sel); err != nil {
		t.Fatalf("fetchHealthSel: %v", err)
	}
	healthPath := (*paths)[0]

	if statsPath != healthPath {
		t.Errorf("resolveStatsNode used path %q but fetchHealthSel used %q — they must match (UX-17)", statsPath, healthPath)
	}
}

// TestResolveStatsNode_ZeroNodes asserts that zero results from the health
// endpoint produce a clear "no nodes" error.
func TestResolveStatsNode_ZeroNodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := cliNodeHealthResponse{Nodes: nil, TotalNodes: 0}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	origServer := flagServer
	defer func() { flagServer = origServer }()
	flagServer = srv.URL

	sel := selector.SelectorSet{Nodes: "ghost-node"}
	_, err := resolveStatsNode(sel)
	if err == nil {
		t.Fatal("expected error for zero nodes, got nil")
	}
	if !strings.Contains(err.Error(), "no nodes") {
		t.Errorf("expected 'no nodes' in error, got: %v", err)
	}
}

// TestResolveStatsNode_MultipleNodes asserts that more than one result returns
// a "single-node only" error.
func TestResolveStatsNode_MultipleNodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := cliNodeHealthResponse{
			Nodes: []cliNodeHealthEntry{
				{NodeID: "aaa", Name: "node-01"},
				{NodeID: "bbb", Name: "node-02"},
			},
			TotalNodes: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	origServer := flagServer
	defer func() { flagServer = origServer }()
	flagServer = srv.URL

	sel := selector.SelectorSet{All: true}
	_, err := resolveStatsNode(sel)
	if err == nil {
		t.Fatal("expected error for multi-node result, got nil")
	}
	if !strings.Contains(err.Error(), "single-node") {
		t.Errorf("expected 'single-node' in error, got: %v", err)
	}
}

// Verify the api package is used (compiler keeps the import).
var _ = api.HealthResponse{}
