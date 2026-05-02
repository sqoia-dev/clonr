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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// NodeHealthDBIface defines the DB operations needed by NodeHealthHandler.
type NodeHealthDBIface interface {
	ListNodeConfigs(ctx context.Context, baseImageID string) ([]api.NodeConfig, error)
	GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error)
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

// GetClusterHealth handles GET /api/v1/health.
// Returns reachability + heartbeat summary for all nodes (or a subset via ?selector).
//
// Query params:
//
//	selector=NODE  — single node ID (same as GET /nodes/{id}/health but wrapped in array)
//	-n, selector handled by the thin flag layer in the CLI; server just takes a bare param.
func (h *NodeHealthHandler) GetClusterHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Load all nodes.
	nodes, err := h.DB.ListNodeConfigs(ctx, "")
	if err != nil {
		writeError(w, err)
		return
	}

	// Optional single-node filter from ?selector= query param.
	if sel := r.URL.Query().Get("selector"); sel != "" {
		var filtered []api.NodeConfig
		for _, n := range nodes {
			if n.ID == sel || n.Hostname == sel {
				filtered = append(filtered, n)
				break
			}
		}
		nodes = filtered
	}

	entries := buildHealthEntries(nodes, h.Hub)
	resp := summariseHealth(entries)
	writeJSON(w, http.StatusOK, resp)
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
