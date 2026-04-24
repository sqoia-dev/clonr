package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clonr/internal/clientd"
	"github.com/sqoia-dev/clonr/pkg/api"
)

const (
	// journalRingSize is the maximum number of recent journal log entries buffered
	// per node. When a new SSE subscriber connects, the buffer is replayed so the
	// Logs tab shows recent entries immediately rather than waiting for the next batch.
	journalRingSize = 500
)

// journalRingBuffer is a fixed-capacity ring buffer for api.LogEntry values.
// Not safe for concurrent use — callers must hold the hub mutex.
type journalRingBuffer struct {
	buf  []api.LogEntry
	head int // index of the next write position
	full bool
}

func newJournalRingBuffer() *journalRingBuffer {
	return &journalRingBuffer{buf: make([]api.LogEntry, journalRingSize)}
}

// push appends an entry to the ring, overwriting the oldest when full.
func (r *journalRingBuffer) push(e api.LogEntry) {
	r.buf[r.head] = e
	r.head = (r.head + 1) % journalRingSize
	if r.head == 0 {
		r.full = true
	}
}

// snapshot returns all stored entries in chronological order.
func (r *journalRingBuffer) snapshot() []api.LogEntry {
	if !r.full {
		out := make([]api.LogEntry, r.head)
		copy(out, r.buf[:r.head])
		return out
	}
	out := make([]api.LogEntry, journalRingSize)
	copy(out, r.buf[r.head:])
	copy(out[journalRingSize-r.head:], r.buf[:r.head])
	return out
}

// nodeJournalState holds per-node journal streaming state in the hub.
type nodeJournalState struct {
	subCount int                // number of active SSE subscribers for node-journal
	ring     *journalRingBuffer // recent log entries for fast replay on new subscriber
}

// ClientdHub tracks all active clonr-clientd WebSocket connections, keyed by node ID.
// It is safe for concurrent use.
//
// It implements handlers.ClientdHubIface (RegisterConn, Unregister, ConnectedNodes,
// IsConnected) — confirmed at compile time by the assignment in server.go.
type ClientdHub struct {
	mu    sync.RWMutex
	conns map[string]*clientdConn

	// journalMu guards journalState (separate from conns lock to avoid deadlocks
	// when Publish is called from the log handler while holding journalMu but not mu).
	journalMu    sync.Mutex
	journalState map[string]*nodeJournalState // nodeID → state
}

// clientdConn holds one live WebSocket connection for a single node.
type clientdConn struct {
	nodeID string
	conn   *websocket.Conn
	send   chan []byte
	cancel context.CancelFunc
}

// NewClientdHub returns an initialised hub with no connections.
func NewClientdHub() *ClientdHub {
	return &ClientdHub{
		conns:        make(map[string]*clientdConn),
		journalState: make(map[string]*nodeJournalState),
	}
}

// RegisterConn adds a connection to the hub. If a prior connection for the same
// nodeID already exists it is closed first (one active connection per node).
// This method satisfies handlers.ClientdHubIface.
func (h *ClientdHub) RegisterConn(nodeID string, conn *websocket.Conn, send chan []byte, cancel context.CancelFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if old, ok := h.conns[nodeID]; ok {
		log.Warn().Str("node_id", nodeID).
			Msg("clientd hub: displacing existing connection (same node reconnected)")
		old.cancel()
		old.conn.Close()
	}
	h.conns[nodeID] = &clientdConn{
		nodeID: nodeID,
		conn:   conn,
		send:   send,
		cancel: cancel,
	}
	log.Info().Str("node_id", nodeID).Msg("clientd hub: node registered")
}

// Unregister removes a connection from the hub. Safe to call multiple times.
func (h *ClientdHub) Unregister(nodeID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, nodeID)
	log.Info().Str("node_id", nodeID).Msg("clientd hub: node unregistered")
}

// Send enqueues a server message for delivery to the named node.
// Returns an error if the node is not connected or the send buffer is full.
func (h *ClientdHub) Send(nodeID string, msg clientd.ServerMessage) error {
	h.mu.RLock()
	c, ok := h.conns[nodeID]
	h.mu.RUnlock()
	if !ok {
		return fmt.Errorf("clientd hub: node %s is not connected", nodeID)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("clientd hub: marshal message: %w", err)
	}

	select {
	case c.send <- data:
		return nil
	default:
		return fmt.Errorf("clientd hub: send buffer full for node %s", nodeID)
	}
}

// ConnectedNodes returns a snapshot list of currently connected node IDs.
func (h *ClientdHub) ConnectedNodes() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.conns))
	for id := range h.conns {
		ids = append(ids, id)
	}
	return ids
}

// IsConnected returns true if the node currently has an active connection.
func (h *ClientdHub) IsConnected(nodeID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[nodeID]
	return ok
}

// runSendLoop drains the conn's send channel, writing messages to the WebSocket.
// Runs in its own goroutine; returns when the channel is closed or a write fails.
// Retained here for potential use in a future server-push flow.
func runSendLoop(conn *websocket.Conn, ch <-chan []byte) {
	for data := range ch {
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Warn().Err(err).Msg("clientd hub: send loop write error")
			return
		}
	}
}

// IncrementLogSubscribers records that a new SSE subscriber is watching node-journal
// logs for nodeID. When the count goes from 0→1 it sends log_pull_start to the node.
// Returns the new subscriber count.
func (h *ClientdHub) IncrementLogSubscribers(nodeID string) int {
	h.journalMu.Lock()
	state := h.journalStateFor(nodeID)
	state.subCount++
	count := state.subCount
	h.journalMu.Unlock()

	if count == 1 {
		// First subscriber — instruct the node to begin streaming.
		h.sendLogPullStart(nodeID)
	}
	return count
}

// DecrementLogSubscribers records that an SSE subscriber has disconnected.
// When the count reaches 0 it sends log_pull_stop to the node.
// Returns the new subscriber count (never goes below 0).
func (h *ClientdHub) DecrementLogSubscribers(nodeID string) int {
	h.journalMu.Lock()
	state := h.journalStateFor(nodeID)
	if state.subCount > 0 {
		state.subCount--
	}
	count := state.subCount
	h.journalMu.Unlock()

	if count == 0 {
		h.sendLogPullStop(nodeID)
	}
	return count
}

// JournalSnapshot returns a point-in-time copy of the recent journal entries
// buffered for nodeID. Used to replay recent entries to a new SSE subscriber.
func (h *ClientdHub) JournalSnapshot(nodeID string) []api.LogEntry {
	h.journalMu.Lock()
	defer h.journalMu.Unlock()
	state, ok := h.journalState[nodeID]
	if !ok || state.ring == nil {
		return nil
	}
	return state.ring.snapshot()
}

// AppendJournalEntries adds entries to the per-node ring buffer.
// Called by the clientd handler when a log_batch arrives from the node.
func (h *ClientdHub) AppendJournalEntries(nodeID string, entries []api.LogEntry) {
	h.journalMu.Lock()
	defer h.journalMu.Unlock()
	state := h.journalStateFor(nodeID)
	for _, e := range entries {
		state.ring.push(e)
	}
}

// journalStateFor returns (and creates if needed) the journalState for nodeID.
// Must be called with journalMu held.
func (h *ClientdHub) journalStateFor(nodeID string) *nodeJournalState {
	if s, ok := h.journalState[nodeID]; ok {
		return s
	}
	s := &nodeJournalState{ring: newJournalRingBuffer()}
	h.journalState[nodeID] = s
	return s
}

// sendLogPullStart sends a log_pull_start message to the node. Best-effort: if
// the node is not connected the error is logged and ignored.
func (h *ClientdHub) sendLogPullStart(nodeID string) {
	payload, _ := json.Marshal(clientd.LogPullStartPayload{
		Priority: -1, // no priority filter — include everything
	})
	msg := clientd.ServerMessage{
		Type:    "log_pull_start",
		MsgID:   uuid.New().String(),
		Payload: json.RawMessage(payload),
	}
	if err := h.Send(nodeID, msg); err != nil {
		log.Debug().Err(err).Str("node_id", nodeID).
			Msg("clientd hub: could not send log_pull_start (node may not be connected)")
	} else {
		log.Info().Str("node_id", nodeID).Msg("clientd hub: sent log_pull_start to node")
	}
}

// sendLogPullStop sends a log_pull_stop message to the node. Best-effort.
func (h *ClientdHub) sendLogPullStop(nodeID string) {
	msg := clientd.ServerMessage{
		Type:  "log_pull_stop",
		MsgID: uuid.New().String(),
	}
	if err := h.Send(nodeID, msg); err != nil {
		log.Debug().Err(err).Str("node_id", nodeID).
			Msg("clientd hub: could not send log_pull_stop (node may not be connected)")
	} else {
		log.Info().Str("node_id", nodeID).Msg("clientd hub: sent log_pull_stop to node")
	}
}
