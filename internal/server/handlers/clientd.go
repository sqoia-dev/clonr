package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clonr/internal/clientd"
	"github.com/sqoia-dev/clonr/internal/db"
	"github.com/sqoia-dev/clonr/pkg/api"
)

// ClientdDBIface defines the database operations used by the clientd handler.
// Declared as an interface so the handler package does not import the concrete db type.
type ClientdDBIface interface {
	UpsertHeartbeat(ctx context.Context, nodeID string, hb *db.HeartbeatRow) error
	GetHeartbeat(ctx context.Context, nodeID string) (*db.HeartbeatRow, error)
	UpdateLastSeen(ctx context.Context, nodeID string) error
	InsertLogBatch(ctx context.Context, entries []api.LogEntry) error
	GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error)
}

// ClientdHubIface is the hub operations needed by the handler.
// The concrete *server.ClientdHub implements this; declared here to avoid
// an import cycle between handlers and server.
type ClientdHubIface interface {
	RegisterConn(nodeID string, conn *websocket.Conn, send chan []byte, cancel context.CancelFunc)
	Unregister(nodeID string)
	ConnectedNodes() []string
	IsConnected(nodeID string) bool
	AppendJournalEntries(nodeID string, entries []api.LogEntry)
}

// ClientdHandler handles the clonr-clientd WebSocket endpoint and related REST queries.
type ClientdHandler struct {
	DB     ClientdDBIface
	Hub    ClientdHubIface
	Broker LogBroker // publishes log entries to SSE subscribers; nil = no fan-out
	// ServerCtx is used for DB writes so a node disconnect does not abort in-flight transactions.
	ServerCtx context.Context
}

// HandleClientdWS upgrades the connection to WebSocket and runs the clientd protocol.
// Route: GET /api/v1/nodes/{id}/clientd/ws (node-scoped, requireNodeOwnership)
func (h *ClientdHandler) HandleClientdWS(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if nodeID == "" {
		http.Error(w, "missing node id", http.StatusBadRequest)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("clientd ws: upgrade failed")
		return
	}
	defer conn.Close()

	log.Info().Str("node_id", nodeID).Str("remote", r.RemoteAddr).
		Msg("clientd ws: node connected")

	connCtx, connCancel := context.WithCancel(r.Context())
	defer connCancel()

	send := make(chan []byte, 64)
	h.Hub.RegisterConn(nodeID, conn, send, connCancel)
	defer h.Hub.Unregister(nodeID)

	// Send outbound messages in the background.
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for {
			select {
			case <-connCtx.Done():
				return
			case data, ok := <-send:
				if !ok {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					log.Warn().Err(err).Str("node_id", nodeID).
						Msg("clientd ws: send error")
					connCancel()
					return
				}
			}
		}
	}()

	// Read loop — blocks until connection closes or context is cancelled.
	for {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			if connCtx.Err() != nil {
				break
			}
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Warn().Err(err).Str("node_id", nodeID).Msg("clientd ws: read error")
			}
			break
		}

		var msg clientd.ClientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Warn().Err(err).Str("node_id", nodeID).
				Msg("clientd ws: malformed message from node")
			continue
		}

		h.dispatchClientMessage(r.Context(), nodeID, msg)
	}

	connCancel()
	<-sendDone
	log.Info().Str("node_id", nodeID).Msg("clientd ws: node disconnected")
}

// dispatchClientMessage routes an incoming node message to the appropriate handler.
func (h *ClientdHandler) dispatchClientMessage(ctx context.Context, nodeID string, msg clientd.ClientMessage) {
	switch msg.Type {
	case "hello":
		h.handleHello(ctx, nodeID, msg)

	case "heartbeat":
		h.handleHeartbeat(ctx, nodeID, msg)

	case "log_batch":
		h.handleLogBatch(ctx, nodeID, msg)

	default:
		log.Debug().Str("node_id", nodeID).Str("type", msg.Type).
			Msg("clientd ws: unknown message type (ignored)")
	}
}

// handleHello processes the initial hello message from a newly connected node.
func (h *ClientdHandler) handleHello(ctx context.Context, nodeID string, msg clientd.ClientMessage) {
	var payload clientd.HelloPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("clientd ws: malformed hello payload")
		return
	}

	log.Info().
		Str("node_id", nodeID).
		Str("hostname", payload.Hostname).
		Str("kernel", payload.KernelVersion).
		Str("clientd_ver", payload.ClientdVersion).
		Float64("uptime_sec", payload.UptimeSeconds).
		Msg("clientd ws: hello received")

	if err := h.DB.UpdateLastSeen(ctx, nodeID); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("clientd ws: UpdateLastSeen failed on hello")
	}
}

// handleHeartbeat persists the heartbeat payload and updates last_seen_at.
func (h *ClientdHandler) handleHeartbeat(ctx context.Context, nodeID string, msg clientd.ClientMessage) {
	var payload clientd.HeartbeatPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("clientd ws: malformed heartbeat payload")
		return
	}

	log.Debug().
		Str("node_id", nodeID).
		Float64("load_1", payload.Load1).
		Float64("uptime_sec", payload.UptimeSeconds).
		Msg("clientd ws: heartbeat received")

	row := &db.HeartbeatRow{
		NodeID:     nodeID,
		ReceivedAt: time.Now().UTC(),
		UptimeSec:  payload.UptimeSeconds,
		Load1:      payload.Load1,
		Load5:      payload.Load5,
		Load15:     payload.Load15,
		MemTotalKB: payload.MemTotalKB,
		MemAvailKB: payload.MemAvailKB,
		DiskUsage:  payload.DiskUsage,
		Services:   payload.Services,
		Kernel:     payload.KernelVersion,
		ClientdVer: payload.ClientdVersion,
	}
	if err := h.DB.UpsertHeartbeat(ctx, nodeID, row); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("clientd ws: UpsertHeartbeat failed")
	}
	if err := h.DB.UpdateLastSeen(ctx, nodeID); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("clientd ws: UpdateLastSeen failed on heartbeat")
	}
}

// handleLogBatch persists and fans out a batch of journal log entries from a node.
func (h *ClientdHandler) handleLogBatch(ctx context.Context, nodeID string, msg clientd.ClientMessage) {
	var entries []api.LogEntry
	if err := json.Unmarshal(msg.Payload, &entries); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("clientd ws: malformed log_batch payload")
		return
	}
	if len(entries) == 0 {
		return
	}

	// Fetch the node's primary MAC to stamp on entries that are missing it.
	// We cache a lookup failure as empty string to avoid hammering the DB.
	var nodeMACForFill string
	if node, err := h.DB.GetNodeConfig(ctx, nodeID); err == nil {
		nodeMACForFill = node.PrimaryMAC
	}

	for i := range entries {
		if entries[i].NodeMAC == "" && nodeMACForFill != "" {
			entries[i].NodeMAC = nodeMACForFill
		}
		if entries[i].ID == "" {
			entries[i].ID = uuid.New().String()
		}
		if entries[i].Timestamp.IsZero() {
			entries[i].Timestamp = time.Now().UTC()
		}
	}

	// Use server-lifetime context for the DB write so node disconnect does not abort the transaction.
	writeCtx := h.ServerCtx
	if writeCtx == nil {
		writeCtx = ctx
	}

	if err := h.DB.InsertLogBatch(writeCtx, entries); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Int("count", len(entries)).
			Msg("clientd ws: InsertLogBatch failed for log_batch")
		return
	}

	// Buffer in ring for replay and fan-out to SSE subscribers.
	h.Hub.AppendJournalEntries(nodeID, entries)
	if h.Broker != nil {
		h.Broker.Publish(entries)
	}

	log.Debug().Str("node_id", nodeID).Int("count", len(entries)).
		Msg("clientd ws: log_batch persisted and published")
}

// GetHeartbeat returns the most recent heartbeat for a node as JSON.
// Route: GET /api/v1/nodes/{id}/heartbeat (admin-only)
func (h *ClientdHandler) GetHeartbeat(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	hb, err := h.DB.GetHeartbeat(r.Context(), nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "no heartbeat received yet for this node",
			})
			return
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hb)
}

// GetConnectedNodes returns a list of currently connected node IDs.
// Route: GET /api/v1/nodes/connected (admin-only)
func (h *ClientdHandler) GetConnectedNodes(w http.ResponseWriter, r *http.Request) {
	ids := h.Hub.ConnectedNodes()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"connected_nodes": ids,
		"count":           len(ids),
	})
}
