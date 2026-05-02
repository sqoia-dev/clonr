package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ClientdDBIface defines the database operations used by the clientd handler.
// Declared as an interface so the handler package does not import the concrete db type.
type ClientdDBIface interface {
	UpsertHeartbeat(ctx context.Context, nodeID string, hb *db.HeartbeatRow) error
	GetHeartbeat(ctx context.Context, nodeID string) (*db.HeartbeatRow, error)
	UpdateLastSeen(ctx context.Context, nodeID string) error
	InsertLogBatch(ctx context.Context, entries []api.LogEntry) error
	GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error)
	InsertStatsBatch(ctx context.Context, rows []db.NodeStatRow) error
}

// ClientdHubIface is the hub operations needed by the handler.
// The concrete *server.ClientdHub implements this; declared here to avoid
// an import cycle between handlers and server.
type ClientdHubIface interface {
	RegisterConn(nodeID string, conn *websocket.Conn, send chan []byte, cancel context.CancelFunc)
	Unregister(nodeID string)
	ConnectedNodes() []string
	IsConnected(nodeID string) bool
	Send(nodeID string, msg clientd.ServerMessage) error
	AppendJournalEntries(nodeID string, entries []api.LogEntry)
	// Ack registry — used for config_push round-trips.
	RegisterAck(msgID string) <-chan clientd.AckPayload
	UnregisterAck(msgID string)
	DeliverAck(msgID string, payload clientd.AckPayload) bool
	// Exec registry — used for exec_request round-trips.
	RegisterExec(msgID string) <-chan clientd.ExecResultPayload
	UnregisterExec(msgID string)
	DeliverExecResult(msgID string, payload clientd.ExecResultPayload) bool
	// OperatorExec registry — used for operator_exec_request batch fan-out.
	RegisterOperatorExec(msgID string) <-chan clientd.OperatorExecResultPayload
	UnregisterOperatorExec(msgID string)
	DeliverOperatorExecResult(msgID string, payload clientd.OperatorExecResultPayload) bool
	// DiskCapture registry — used for disk_capture_request round-trips (#146).
	RegisterDiskCapture(msgID string) <-chan clientd.DiskCaptureResultPayload
	UnregisterDiskCapture(msgID string)
	DeliverDiskCaptureResult(msgID string, payload clientd.DiskCaptureResultPayload) bool
}

// ClientdHandler handles the clustr-clientd WebSocket endpoint and related REST queries.
type ClientdHandler struct {
	DB     ClientdDBIface
	Hub    ClientdHubIface
	Broker LogBroker // publishes log entries to SSE subscribers; nil = no fan-out
	// ServerCtx is used for DB writes so a node disconnect does not abort in-flight transactions.
	ServerCtx context.Context
	// SudoersNodeConfig, when non-nil, is called by HandleSudoersPush to fetch the
	// current sudoers drop-in content for broadcast to connected nodes.
	SudoersNodeConfig func(ctx context.Context) (*api.SudoersNodeConfig, error)
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

	case "ack":
		h.handleAck(nodeID, msg)

	case "exec_result":
		h.handleExecResult(nodeID, msg)

	case "operator_exec_result":
		h.handleOperatorExecResult(nodeID, msg)

	case "disk_capture_result":
		h.handleDiskCaptureResult(nodeID, msg)

	case "stats_batch":
		h.handleStatsBatch(ctx, nodeID, msg)

	default:
		log.Debug().Str("node_id", nodeID).Str("type", msg.Type).
			Msg("clientd ws: unknown message type (ignored)")
	}
}

// handleAck processes an "ack" message from the node, routing it to the waiting
// HTTP handler via the hub's ack registry. The RefMsgID identifies the outbound
// server message that triggered this ack (e.g. a config_push msg_id).
func (h *ClientdHandler) handleAck(nodeID string, msg clientd.ClientMessage) {
	var payload clientd.AckPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("clientd ws: malformed ack payload")
		return
	}
	delivered := h.Hub.DeliverAck(payload.RefMsgID, payload)
	log.Debug().
		Str("node_id", nodeID).
		Str("ref_msg_id", payload.RefMsgID).
		Bool("ok", payload.OK).
		Bool("delivered", delivered).
		Msg("clientd ws: ack received from node")
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

// configPushRequest is the JSON body for PUT /api/v1/nodes/{id}/config-push.
type configPushRequest struct {
	Target  string `json:"target"`
	Content string `json:"content"`
}

// ConfigPush pushes a config file to a connected node and waits for the ack.
// Route: PUT /api/v1/nodes/{id}/config-push (admin-only)
func (h *ClientdHandler) ConfigPush(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if nodeID == "" {
		writeValidationError(w, "missing node id")
		return
	}

	if !h.Hub.IsConnected(nodeID) {
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: "node is not connected (clustr-clientd offline)",
			Code:  "node_offline",
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20)) // 2 MB read limit
	if err != nil {
		writeValidationError(w, "failed to read request body")
		return
	}

	var req configPushRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Target == "" {
		writeValidationError(w, "target is required")
		return
	}
	if len(req.Content) > 1<<20 {
		writeValidationError(w, "content exceeds 1 MB limit")
		return
	}

	// Compute sha256 checksum of content so the node can verify integrity.
	sum := sha256.Sum256([]byte(req.Content))
	checksum := fmt.Sprintf("sha256:%x", sum)

	msgID := uuid.New().String()
	payload, err := json.Marshal(clientd.ConfigPushPayload{
		Target:   req.Target,
		Content:  req.Content,
		Checksum: checksum,
	})
	if err != nil {
		writeError(w, fmt.Errorf("marshal config_push payload: %w", err))
		return
	}

	serverMsg := clientd.ServerMessage{
		Type:    "config_push",
		MsgID:   msgID,
		Payload: json.RawMessage(payload),
	}

	// Register ack channel before sending to avoid a race where the node
	// replies faster than we register.
	ackCh := h.Hub.RegisterAck(msgID)
	defer h.Hub.UnregisterAck(msgID)

	if err := h.Hub.Send(nodeID, serverMsg); err != nil {
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: "failed to send config_push to node: " + err.Error(),
			Code:  "send_failed",
		})
		return
	}

	log.Info().Str("node_id", nodeID).Str("target", req.Target).Str("msg_id", msgID).
		Msg("clientd: config_push sent to node, waiting for ack")

	select {
	case ack := <-ackCh:
		if ack.OK {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"ok":     true,
				"target": req.Target,
			})
		} else {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
				"ok":    false,
				"error": ack.Error,
			})
		}
	case <-time.After(30 * time.Second):
		writeJSON(w, http.StatusGatewayTimeout, api.ErrorResponse{
			Error: "timed out waiting for ack from node (30s)",
			Code:  "ack_timeout",
		})
	case <-r.Context().Done():
		// Client disconnected before ack arrived — silently drop.
		return
	}
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

// handleExecResult processes an "exec_result" message from the node, delivering
// it to the ExecOnNode HTTP handler that is waiting on the exec registry.
func (h *ClientdHandler) handleExecResult(nodeID string, msg clientd.ClientMessage) {
	var payload clientd.ExecResultPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("clientd ws: malformed exec_result payload")
		return
	}
	delivered := h.Hub.DeliverExecResult(payload.RefMsgID, payload)
	log.Debug().
		Str("node_id", nodeID).
		Str("ref_msg_id", payload.RefMsgID).
		Int("exit_code", payload.ExitCode).
		Bool("truncated", payload.Truncated).
		Bool("delivered", delivered).
		Msg("clientd ws: exec_result received from node")
}

// handleOperatorExecResult processes an "operator_exec_result" message from the node,
// delivering it to the batch ExecHandler that is waiting on the operator exec registry.
func (h *ClientdHandler) handleOperatorExecResult(nodeID string, msg clientd.ClientMessage) {
	var payload clientd.OperatorExecResultPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).
			Msg("clientd ws: malformed operator_exec_result payload")
		return
	}
	delivered := h.Hub.DeliverOperatorExecResult(payload.RefMsgID, payload)
	log.Debug().
		Str("node_id", nodeID).
		Str("ref_msg_id", payload.RefMsgID).
		Int("exit_code", payload.ExitCode).
		Bool("truncated", payload.Truncated).
		Bool("delivered", delivered).
		Msg("clientd ws: operator_exec_result received from node")
}

// handleDiskCaptureResult processes a "disk_capture_result" message from the
// node and delivers it to the waiting CaptureDiskLayout HTTP handler.
func (h *ClientdHandler) handleDiskCaptureResult(nodeID string, msg clientd.ClientMessage) {
	var payload clientd.DiskCaptureResultPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("clientd ws: malformed disk_capture_result payload")
		return
	}
	delivered := h.Hub.DeliverDiskCaptureResult(payload.RefMsgID, payload)
	log.Debug().
		Str("node_id", nodeID).
		Str("ref_msg_id", payload.RefMsgID).
		Bool("delivered", delivered).
		Str("error", payload.Error).
		Msg("clientd ws: disk_capture_result received from node")
}

// handleStatsBatch persists a stats_batch message from a node and sends a
// stats_ack back to indicate acceptance. Uses the server-lifetime context so
// a node disconnect doesn't abort the in-flight SQLite transaction.
func (h *ClientdHandler) handleStatsBatch(ctx context.Context, nodeID string, msg clientd.ClientMessage) {
	var payload clientd.StatsBatchPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("clientd ws: malformed stats_batch payload")
		h.sendStatsAck(nodeID, msg.MsgID, payload.BatchID, false, "malformed payload: "+err.Error())
		return
	}

	if len(payload.Samples) == 0 {
		h.sendStatsAck(nodeID, msg.MsgID, payload.BatchID, true, "")
		return
	}

	// Convert to DB rows.
	rows := make([]db.NodeStatRow, 0, len(payload.Samples))
	for _, s := range payload.Samples {
		rows = append(rows, db.NodeStatRow{
			NodeID: nodeID,
			Plugin: payload.Plugin,
			Sensor: s.Sensor,
			Value:  s.Value,
			Unit:   s.Unit,
			Labels: s.Labels,
			TS:     s.TS,
		})
	}

	// Use server-lifetime context so disconnect doesn't abort the transaction.
	writeCtx := h.ServerCtx
	if writeCtx == nil {
		writeCtx = ctx
	}

	if err := h.DB.InsertStatsBatch(writeCtx, rows); err != nil {
		log.Error().Err(err).
			Str("node_id", nodeID).
			Str("plugin", payload.Plugin).
			Str("batch_id", payload.BatchID).
			Int("samples", len(payload.Samples)).
			Msg("clientd ws: InsertStatsBatch failed")
		h.sendStatsAck(nodeID, msg.MsgID, payload.BatchID, false, "db insert failed")
		return
	}

	log.Debug().
		Str("node_id", nodeID).
		Str("plugin", payload.Plugin).
		Str("batch_id", payload.BatchID).
		Int("samples", len(payload.Samples)).
		Msg("clientd ws: stats_batch persisted")

	h.sendStatsAck(nodeID, msg.MsgID, payload.BatchID, true, "")
}

// sendStatsAck sends a "stats_ack" ServerMessage back to the node.
func (h *ClientdHandler) sendStatsAck(nodeID, refMsgID, batchID string, accepted bool, errMsg string) {
	payload, err := json.Marshal(clientd.StatsAckPayload{
		BatchID:  batchID,
		Accepted: accepted,
		Error:    errMsg,
	})
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("clientd ws: failed to marshal stats_ack")
		return
	}

	serverMsg := clientd.ServerMessage{
		Type:    "stats_ack",
		MsgID:   refMsgID,
		Payload: json.RawMessage(payload),
	}
	if err := h.Hub.Send(nodeID, serverMsg); err != nil {
		log.Debug().Err(err).Str("node_id", nodeID).Msg("clientd ws: failed to send stats_ack (node may have disconnected)")
	}
}

// execRequest is the JSON body for POST /api/v1/nodes/{id}/exec.
type execRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// HandleSudoersPush renders the sudoers drop-in content and broadcasts it as a
// config_push to every connected node. Returns per-node results with 30s timeout.
// Route: POST /api/v1/ldap/sudoers/push (admin-only)
func (h *ClientdHandler) HandleSudoersPush(w http.ResponseWriter, r *http.Request) {
	if h.SudoersNodeConfig == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.ErrorResponse{
			Error: "sudoers config function not wired",
			Code:  "not_configured",
		})
		return
	}

	sudoersCfg, err := h.SudoersNodeConfig(r.Context())
	if err != nil {
		writeError(w, fmt.Errorf("fetch sudoers config: %w", err))
		return
	}
	if sudoersCfg == nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{
			Error: "sudoers is not enabled or LDAP module is not ready",
			Code:  "sudoers_disabled",
		})
		return
	}

	var content string
	if sudoersCfg.NoPasswd {
		content = fmt.Sprintf("%%%s ALL=(ALL) NOPASSWD:ALL\n", sudoersCfg.GroupCN)
	} else {
		content = fmt.Sprintf("%%%s ALL=(ALL) ALL\n", sudoersCfg.GroupCN)
	}

	sum := sha256.Sum256([]byte(content))
	checksum := fmt.Sprintf("sha256:%x", sum)

	nodeIDs := h.Hub.ConnectedNodes()
	if len(nodeIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":      true,
			"message": "no nodes currently connected",
			"results": map[string]interface{}{},
		})
		return
	}

	type nodeResult struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	results := make(map[string]nodeResult, len(nodeIDs))

	for _, nodeID := range nodeIDs {
		msgID := uuid.New().String()
		payload, err := json.Marshal(clientd.ConfigPushPayload{
			Target:   "sudoers",
			Content:  content,
			Checksum: checksum,
		})
		if err != nil {
			results[nodeID] = nodeResult{OK: false, Error: "marshal payload: " + err.Error()}
			continue
		}

		serverMsg := clientd.ServerMessage{
			Type:    "config_push",
			MsgID:   msgID,
			Payload: json.RawMessage(payload),
		}

		ackCh := h.Hub.RegisterAck(msgID)
		if err := h.Hub.Send(nodeID, serverMsg); err != nil {
			h.Hub.UnregisterAck(msgID)
			results[nodeID] = nodeResult{OK: false, Error: "send failed: " + err.Error()}
			continue
		}

		select {
		case ack := <-ackCh:
			h.Hub.UnregisterAck(msgID)
			if ack.OK {
				results[nodeID] = nodeResult{OK: true}
			} else {
				results[nodeID] = nodeResult{OK: false, Error: ack.Error}
			}
		case <-time.After(30 * time.Second):
			h.Hub.UnregisterAck(msgID)
			results[nodeID] = nodeResult{OK: false, Error: "ack timeout (30s)"}
		}
	}

	okCount := 0
	for _, r := range results {
		if r.OK {
			okCount++
		}
	}

	log.Info().
		Int("nodes", len(nodeIDs)).
		Int("ok", okCount).
		Msg("clientd: sudoers push complete")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":            okCount == len(nodeIDs),
		"node_count":    len(nodeIDs),
		"success_count": okCount,
		"failure_count": len(nodeIDs) - okCount,
		"results":       results,
	})
}

// ExecOnNode sends an exec_request to a connected node and waits for the result.
// Route: POST /api/v1/nodes/{id}/exec (admin-only)
func (h *ClientdHandler) ExecOnNode(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if nodeID == "" {
		writeValidationError(w, "missing node id")
		return
	}

	if !h.Hub.IsConnected(nodeID) {
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: "node is not connected (clustr-clientd offline)",
			Code:  "node_offline",
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeValidationError(w, "failed to read request body")
		return
	}

	var req execRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Command == "" {
		writeValidationError(w, "command is required")
		return
	}
	if req.Args == nil {
		req.Args = []string{}
	}

	msgID := uuid.New().String()
	payload, err := json.Marshal(clientd.ExecRequestPayload{
		RefMsgID: msgID,
		Command:  req.Command,
		Args:     req.Args,
	})
	if err != nil {
		writeError(w, fmt.Errorf("marshal exec_request payload: %w", err))
		return
	}

	serverMsg := clientd.ServerMessage{
		Type:    "exec_request",
		MsgID:   msgID,
		Payload: json.RawMessage(payload),
	}

	// Register before sending to avoid a race where the node replies before we register.
	execCh := h.Hub.RegisterExec(msgID)
	defer h.Hub.UnregisterExec(msgID)

	if err := h.Hub.Send(nodeID, serverMsg); err != nil {
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: "failed to send exec_request to node: " + err.Error(),
			Code:  "send_failed",
		})
		return
	}

	log.Info().
		Str("node_id", nodeID).
		Str("command", req.Command).
		Strs("args", req.Args).
		Str("msg_id", msgID).
		Msg("clientd: exec_request sent to node, waiting for result")

	select {
	case result := <-execCh:
		writeJSON(w, http.StatusOK, result)
	case <-time.After(30 * time.Second):
		writeJSON(w, http.StatusGatewayTimeout, api.ErrorResponse{
			Error: "timed out waiting for exec_result from node (30s)",
			Code:  "exec_timeout",
		})
	case <-r.Context().Done():
		// Client disconnected before result arrived — silently drop.
		return
	}
}
