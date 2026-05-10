// Package handlers — node_health.go implements GET /api/v1/health (node aggregate)
// and GET /api/v1/nodes/{id}/health for per-node reachability + heartbeat summary.
//
// "Reachable" means the clustr-clientd WebSocket is currently connected.
// "last_heartbeat" is the last_seen_at timestamp from the node_configs table,
// updated whenever a heartbeat message arrives over the WebSocket.
package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/selector"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// NodeHealthDBIface defines the DB operations needed by NodeHealthHandler.
type NodeHealthDBIface interface {
	ListNodeConfigs(ctx context.Context, baseImageID string) ([]api.NodeConfig, error)
	GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error)
	// ListAllNodes returns all node configs in the lightweight selector shape.
	// Satisfied by selector.DBAdapter.ListAllNodes; wired in server.go.
	ListAllNodes(ctx context.Context) ([]selector.SelectorNode, error)
	// ListGroupMemberIDs returns the node IDs of all members of the named group.
	ListGroupMemberIDs(ctx context.Context, groupName string) ([]selector.NodeID, error)
	// ListNodeIDsByRackNames returns node IDs for all nodes in the named racks.
	ListNodeIDsByRackNames(ctx context.Context, rackNames []string) ([]selector.NodeID, error)
	// ListNodeIDsByEnclosureLabels returns node IDs for all nodes in the named enclosures.
	ListNodeIDsByEnclosureLabels(ctx context.Context, labels []string) ([]selector.NodeID, error)
}

// NodeHealthHubIface is the subset of ClientdHub used by the health handler.
type NodeHealthHubIface interface {
	IsConnected(nodeID string) bool
	ConnectedNodes() []string
}

// NodeHealthHandler serves the per-node health summary endpoint.
type NodeHealthHandler struct {
	DB  NodeHealthDBIface
	Hub NodeHealthHubIface
}

// NodeHealthEntry is one row in the health response.
type NodeHealthEntry struct {
	NodeID        string     `json:"node_id"`
	Name          string     `json:"name"`
	Reachable     bool       `json:"reachable"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"` // nil when never seen
	// HeartbeatAge is seconds since last_heartbeat, -1 when never seen.
	HeartbeatAge float64 `json:"heartbeat_age_seconds"`
	Status       string  `json:"status"` // derived from api.NodeConfig.State()
}

// NodeHealthResponse is returned by GET /api/v1/health and GET /api/v1/nodes/{id}/health.
type NodeHealthResponse struct {
	Nodes       []NodeHealthEntry `json:"nodes"`
	TotalNodes  int               `json:"total_nodes"`
	Reachable   int               `json:"reachable"`
	Unreachable int               `json:"unreachable"`
	AsOf        time.Time         `json:"as_of"`
}

// GetClusterHealth handles GET /api/v1/cluster/health.
// Returns reachability + heartbeat summary for all nodes (or a subset via selector query params).
//
// Selector query parameters (mirror the CLI selector grammar):
//
//	nodes=HOSTLIST   — hostlist expression (node01, node[01-32], …)
//	group=NAME       — node group name
//	all=true         — all registered nodes (default when no selector given)
//	active=true      — active nodes only (deployed_verified state)
//	racks=LIST       — rack names (comma-separated; resolved after #138)
//	chassis=LIST     — chassis names (comma-separated; resolved after #138)
//	ignore_status=true — bypass active-state filter
//
// Legacy compat: ?selector=NODE_ID still accepted (maps to nodes=NODE_ID).
func (h *NodeHealthHandler) GetClusterHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	q := r.URL.Query()

	// Build a SelectorSet from query parameters.
	set := selector.SelectorSet{
		Nodes:        q.Get("nodes"),
		Group:        q.Get("group"),
		All:          q.Get("all") == "true",
		Active:       q.Get("active") == "true",
		Racks:        q.Get("racks"),
		Chassis:      q.Get("chassis"),
		IgnoreStatus: q.Get("ignore_status") == "true",
	}

	// Legacy compat: ?selector=NODE_ID (single node lookup, pre-#125 CLI behaviour).
	if legacySel := q.Get("selector"); legacySel != "" && set.Nodes == "" {
		set.Nodes = legacySel
	}

	// Load all nodes (full config for health assembly).
	allNodes, err := h.DB.ListNodeConfigs(ctx, "")
	if err != nil {
		writeError(w, err)
		return
	}

	// If no selector is specified, return all nodes (backward-compatible default).
	if set.IsEmpty() {
		entries := buildHealthEntries(allNodes, h.Hub)
		resp := summariseHealth(entries)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Resolve the selector to a set of node IDs, then filter the full node list.
	nodeIDs, err := selector.Resolve(ctx, h.DB, set)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.ErrorResponse{
			Error: err.Error(),
			Code:  "selector_error",
		})
		return
	}

	idSet := make(map[string]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		idSet[id] = struct{}{}
	}

	var filtered []api.NodeConfig
	for _, n := range allNodes {
		if _, ok := idSet[n.ID]; ok {
			filtered = append(filtered, n)
		}
	}

	entries := buildHealthEntries(filtered, h.Hub)
	resp := summariseHealth(entries)
	writeJSON(w, http.StatusOK, resp)
}

//lint:ignore U1000 legacy ?selector= filter used by external monitors; retained for backward compat when the param is re-enabled
// nodeHealthMatchesSelector returns true when n.ID or n.Hostname matches sel.
// Used for the legacy ?selector= parameter.
func nodeHealthMatchesSelector(n api.NodeConfig, sel string) bool {
	return n.ID == sel || strings.EqualFold(n.Hostname, sel)
}

// ─── NodeHealthDBAdapter ──────────────────────────────────────────────────────

// nodeHealthDBInner is the set of methods used from *db.DB.
type nodeHealthDBInner interface {
	ListNodeConfigs(ctx context.Context, baseImageID string) ([]api.NodeConfig, error)
	GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error)
}

// NodeHealthDBAdapter satisfies NodeHealthDBIface by composing the raw DB with
// the selector.DBAdapter (which adds ListAllNodes / ListGroupMemberIDs).
type NodeHealthDBAdapter struct {
	inner    nodeHealthDBInner
	selInner *selector.DBAdapter
}

// NewNodeHealthDBAdapter creates a NodeHealthDBAdapter.
// inner is *db.DB; selInner is selector.NewDBAdapter(db).
func NewNodeHealthDBAdapter(inner nodeHealthDBInner, selInner *selector.DBAdapter) *NodeHealthDBAdapter {
	return &NodeHealthDBAdapter{inner: inner, selInner: selInner}
}

// ListNodeConfigs delegates to the underlying DB.
func (a *NodeHealthDBAdapter) ListNodeConfigs(ctx context.Context, baseImageID string) ([]api.NodeConfig, error) {
	return a.inner.ListNodeConfigs(ctx, baseImageID)
}

// GetNodeConfig delegates to the underlying DB.
func (a *NodeHealthDBAdapter) GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error) {
	return a.inner.GetNodeConfig(ctx, id)
}

// ListAllNodes delegates to the selector adapter.
func (a *NodeHealthDBAdapter) ListAllNodes(ctx context.Context) ([]selector.SelectorNode, error) {
	return a.selInner.ListAllNodes(ctx)
}

// ListGroupMemberIDs delegates to the selector adapter.
func (a *NodeHealthDBAdapter) ListGroupMemberIDs(ctx context.Context, groupName string) ([]selector.NodeID, error) {
	return a.selInner.ListGroupMemberIDs(ctx, groupName)
}

// ListNodeIDsByRackNames delegates to the selector adapter.
func (a *NodeHealthDBAdapter) ListNodeIDsByRackNames(ctx context.Context, rackNames []string) ([]selector.NodeID, error) {
	return a.selInner.ListNodeIDsByRackNames(ctx, rackNames)
}

// ListNodeIDsByEnclosureLabels delegates to the selector adapter.
func (a *NodeHealthDBAdapter) ListNodeIDsByEnclosureLabels(ctx context.Context, labels []string) ([]selector.NodeID, error) {
	return a.selInner.ListNodeIDsByEnclosureLabels(ctx, labels)
}

// GetNodeHealth handles GET /api/v1/nodes/{id}/health.
// Returns single-node health entry.
func (h *NodeHealthHandler) GetNodeHealth(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	cfg, err := h.DB.GetNodeConfig(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}

	entry := nodeHealthEntry(cfg, h.Hub)
	entries := []NodeHealthEntry{entry}
	resp := summariseHealth(entries)
	writeJSON(w, http.StatusOK, resp)
}

// buildHealthEntries converts a slice of NodeConfigs into health entries by
// cross-referencing the live connection state from the hub.
func buildHealthEntries(nodes []api.NodeConfig, hub NodeHealthHubIface) []NodeHealthEntry {
	now := time.Now().UTC()
	entries := make([]NodeHealthEntry, 0, len(nodes))
	for _, n := range nodes {
		e := nodeHealthEntry(n, hub)
		_ = now // used indirectly via HeartbeatAge
		entries = append(entries, e)
	}
	return entries
}

// nodeHealthEntry constructs a single NodeHealthEntry from a NodeConfig and the hub.
func nodeHealthEntry(n api.NodeConfig, hub NodeHealthHubIface) NodeHealthEntry {
	reachable := hub.IsConnected(n.ID)

	var heartbeatAge float64 = -1
	if n.LastSeenAt != nil {
		heartbeatAge = time.Since(*n.LastSeenAt).Seconds()
	}

	return NodeHealthEntry{
		NodeID:        n.ID,
		Name:          n.Hostname,
		Reachable:     reachable,
		LastHeartbeat: n.LastSeenAt,
		HeartbeatAge:  heartbeatAge,
		Status:        string(n.State()),
	}
}

// summariseHealth wraps entries in a NodeHealthResponse with aggregate counts.
func summariseHealth(entries []NodeHealthEntry) NodeHealthResponse {
	var reachable, unreachable int
	for _, e := range entries {
		if e.Reachable {
			reachable++
		} else {
			unreachable++
		}
	}
	if entries == nil {
		entries = []NodeHealthEntry{}
	}
	return NodeHealthResponse{
		Nodes:       entries,
		TotalNodes:  len(entries),
		Reachable:   reachable,
		Unreachable: unreachable,
		AsOf:        time.Now().UTC(),
	}
}
