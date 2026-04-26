package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// newNodesHandler returns a NodesHandler wired to the given DB.
func newNodesHandler(d *db.DB) *NodesHandler {
	return &NodesHandler{DB: d}
}

// makeTestNodeWithGroup creates a NodeConfig with a pre-assigned group and
// inserts it into d.
func makeTestNodeWithGroup(t *testing.T, d *db.DB, mac, hostname, groupID string) api.NodeConfig {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	cfg := api.NodeConfig{
		ID:         "node-" + mac,
		Hostname:   hostname,
		PrimaryMAC: mac,
		GroupID:    groupID,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(t.Context(), cfg); err != nil {
		t.Fatalf("makeTestNodeWithGroup CreateNodeConfig: %v", err)
	}
	return cfg
}

// putNodeRequest fires UpdateNode against the handler with the given body,
// injecting the node ID into the chi URL params.
func putNodeRequest(t *testing.T, h *NodesHandler, nodeID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("putNodeRequest json.Marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/nodes/"+nodeID, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")

	// Inject chi URL param so chi.URLParam(r, "id") resolves correctly.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", nodeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.UpdateNode(w, req)
	return w
}

// TestUpdateNode_PreservesGroupID_WhenOmittedFromRequest is the regression test
// for BUG-1: a PUT request that omits group_id must not silently clear the
// node's existing group assignment.
func TestUpdateNode_PreservesGroupID_WhenOmittedFromRequest(t *testing.T) {
	d := openTestDB(t)
	const (
		mac      = "aa:bb:cc:11:22:33"
		hostname = "group-node01"
		groupID  = "group-hpc-rack1"
	)

	node := makeTestNodeWithGroup(t, d, mac, hostname, groupID)

	// PUT with no group_id field — simulates the webui node-list modal which
	// does not include group_id in its payload.
	w := putNodeRequest(t, newNodesHandler(d), node.ID, map[string]any{
		"hostname":    hostname,
		"primary_mac": mac,
		// group_id intentionally absent
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	// Reload from DB and confirm group is still assigned.
	got, err := d.GetNodeConfig(t.Context(), node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig after PUT: %v", err)
	}
	if got.GroupID != groupID {
		t.Errorf("UpdateNode: GroupID = %q after omitted group_id PUT; want %q (group must be preserved)", got.GroupID, groupID)
	}
}

// TestUpdateNode_UpdatesGroupID_WhenProvided verifies that supplying a non-empty
// group_id in the PUT body correctly updates the node's group assignment.
func TestUpdateNode_UpdatesGroupID_WhenProvided(t *testing.T) {
	d := openTestDB(t)
	const (
		mac      = "aa:bb:cc:44:55:66"
		hostname = "group-node02"
		oldGroup = "group-old"
		newGroup = "group-new"
	)

	node := makeTestNodeWithGroup(t, d, mac, hostname, oldGroup)

	w := putNodeRequest(t, newNodesHandler(d), node.ID, map[string]any{
		"hostname":    hostname,
		"primary_mac": mac,
		"group_id":    newGroup,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	got, err := d.GetNodeConfig(t.Context(), node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig after PUT: %v", err)
	}
	if got.GroupID != newGroup {
		t.Errorf("UpdateNode: GroupID = %q; want %q", got.GroupID, newGroup)
	}
}

// TestUpdateNode_PreservesGroupID_WhenExplicitlyEmpty verifies that sending
// group_id="" preserves the existing group (empty string cannot unassign;
// the dedicated group-membership endpoint must be used for that).
func TestUpdateNode_PreservesGroupID_WhenExplicitlyEmpty(t *testing.T) {
	d := openTestDB(t)
	const (
		mac      = "aa:bb:cc:77:88:99"
		hostname = "group-node03"
		groupID  = "group-production"
	)

	node := makeTestNodeWithGroup(t, d, mac, hostname, groupID)

	w := putNodeRequest(t, newNodesHandler(d), node.ID, map[string]any{
		"hostname":    hostname,
		"primary_mac": mac,
		"group_id":    "", // explicit empty string
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	got, err := d.GetNodeConfig(t.Context(), node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig after PUT: %v", err)
	}
	if got.GroupID != groupID {
		t.Errorf("UpdateNode: GroupID = %q after empty group_id PUT; want %q (group must be preserved)", got.GroupID, groupID)
	}
}
