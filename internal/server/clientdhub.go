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
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/pkg/api"
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

// pendingAck holds a channel to receive an AckPayload for an in-flight config_push.
type pendingAck struct {
	ch chan clientd.AckPayload
}

// pendingOperatorExecResult holds a channel to receive an OperatorExecResultPayload
// for an in-flight operator_exec_request.
type pendingOperatorExecResult struct {
	ch chan clientd.OperatorExecResultPayload
}

// pendingExecResult holds a channel to receive an ExecResultPayload for an
// in-flight exec_request.
type pendingExecResult struct {
	ch chan clientd.ExecResultPayload
}

// ClientdHub tracks all active clustr-clientd WebSocket connections, keyed by node ID.
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

	// ackRegistry maps outbound msg_id → pendingAck so the HTTP handler can
	// block until the node sends back the "ack" message. sync.Map is used for
	// lock-free fast-path reads; entries are stored by msg_id (string).
	ackRegistry sync.Map

	// execRegistry maps outbound msg_id → pendingExecResult so the HTTP handler
	// can block until the node sends back the "exec_result" message.
	execRegistry sync.Map

	// operatorExecRegistry maps outbound msg_id → pendingOperatorExecResult so the
	// batch exec HTTP handler can collect operator_exec_result messages per node.
	operatorExecRegistry sync.Map

	// diskCaptureRegistry maps outbound msg_id → pendingDiskCaptureResult so the
	// capture HTTP handler can block until the node replies with disk_capture_result.
	diskCaptureRegistry sync.Map

	// biosReadRegistry maps outbound msg_id → pendingBiosReadResult so the
	// ReadBios HTTP handler can block until the node replies with bios_read_result.
	biosReadRegistry sync.Map

	// biosApplyRegistry maps outbound msg_id → pendingBiosApplyResult so the
	// ApplyBios HTTP handler can block until the node replies with bios_apply_result.
	biosApplyRegistry sync.Map

	// ldapHealthRegistry maps outbound msg_id → pendingLDAPHealthResult so the
	// admin "force re-verify" HTTP handler can block until the node replies
	// with ldap_health_result. fix/v0.1.22-ldap-reverify.
	ldapHealthRegistry sync.Map
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

// RegisterAck creates a pending ack entry for msgID and returns the channel to
// read the ack from. The caller must call UnregisterAck(msgID) when done.
func (h *ClientdHub) RegisterAck(msgID string) <-chan clientd.AckPayload {
	ch := make(chan clientd.AckPayload, 1)
	h.ackRegistry.Store(msgID, pendingAck{ch: ch})
	return ch
}

// UnregisterAck removes a pending ack entry. Safe to call after timeout or receipt.
func (h *ClientdHub) UnregisterAck(msgID string) {
	h.ackRegistry.Delete(msgID)
}

// DeliverAck looks up msgID in the ack registry and, if found, sends payload on
// its channel (non-blocking). Called by the WebSocket handler when it receives an
// "ack" message from the node.
func (h *ClientdHub) DeliverAck(msgID string, payload clientd.AckPayload) bool {
	v, ok := h.ackRegistry.Load(msgID)
	if !ok {
		return false
	}
	pa := v.(pendingAck)
	select {
	case pa.ch <- payload:
		return true
	default:
		// Channel already has a value or is closed — ignore.
		return false
	}
}

// RegisterExec creates a pending exec result entry for msgID and returns the
// channel to read the ExecResultPayload from. The caller must call
// UnregisterExec(msgID) when done.
func (h *ClientdHub) RegisterExec(msgID string) <-chan clientd.ExecResultPayload {
	ch := make(chan clientd.ExecResultPayload, 1)
	h.execRegistry.Store(msgID, pendingExecResult{ch: ch})
	return ch
}

// UnregisterExec removes a pending exec result entry.
func (h *ClientdHub) UnregisterExec(msgID string) {
	h.execRegistry.Delete(msgID)
}

// DeliverExecResult delivers an ExecResultPayload to the waiting HTTP handler.
// Called by the WebSocket handler when it receives an "exec_result" message from
// the node. Returns true if the result was delivered to a waiting caller.
func (h *ClientdHub) DeliverExecResult(msgID string, payload clientd.ExecResultPayload) bool {
	v, ok := h.execRegistry.Load(msgID)
	if !ok {
		return false
	}
	pe := v.(pendingExecResult)
	select {
	case pe.ch <- payload:
		return true
	default:
		return false
	}
}

// RegisterOperatorExec creates a pending operator exec result entry for msgID and
// returns the channel to read the OperatorExecResultPayload from. The caller must
// call UnregisterOperatorExec(msgID) when done.
func (h *ClientdHub) RegisterOperatorExec(msgID string) <-chan clientd.OperatorExecResultPayload {
	ch := make(chan clientd.OperatorExecResultPayload, 1)
	h.operatorExecRegistry.Store(msgID, pendingOperatorExecResult{ch: ch})
	return ch
}

// UnregisterOperatorExec removes a pending operator exec result entry.
func (h *ClientdHub) UnregisterOperatorExec(msgID string) {
	h.operatorExecRegistry.Delete(msgID)
}

// DeliverOperatorExecResult delivers an OperatorExecResultPayload to the waiting
// HTTP handler. Called by the WebSocket handler when it receives an
// "operator_exec_result" message from the node. Returns true if delivered.
func (h *ClientdHub) DeliverOperatorExecResult(msgID string, payload clientd.OperatorExecResultPayload) bool {
	v, ok := h.operatorExecRegistry.Load(msgID)
	if !ok {
		return false
	}
	pe := v.(pendingOperatorExecResult)
	select {
	case pe.ch <- payload:
		return true
	default:
		return false
	}
}

// pendingDiskCaptureResult holds a channel to receive a DiskCaptureResultPayload
// for an in-flight disk_capture_request.
type pendingDiskCaptureResult struct {
	ch chan clientd.DiskCaptureResultPayload
}

// RegisterDiskCapture creates a pending disk capture result entry for msgID and
// returns the channel to read the DiskCaptureResultPayload from.  The caller
// must call UnregisterDiskCapture(msgID) when done.
func (h *ClientdHub) RegisterDiskCapture(msgID string) <-chan clientd.DiskCaptureResultPayload {
	ch := make(chan clientd.DiskCaptureResultPayload, 1)
	h.diskCaptureRegistry.Store(msgID, pendingDiskCaptureResult{ch: ch})
	return ch
}

// UnregisterDiskCapture removes a pending disk capture result entry.
func (h *ClientdHub) UnregisterDiskCapture(msgID string) {
	h.diskCaptureRegistry.Delete(msgID)
}

// DeliverDiskCaptureResult delivers a DiskCaptureResultPayload to the waiting
// HTTP handler.  Called by the WebSocket handler when it receives a
// "disk_capture_result" message from the node.  Returns true if delivered.
func (h *ClientdHub) DeliverDiskCaptureResult(msgID string, payload clientd.DiskCaptureResultPayload) bool {
	v, ok := h.diskCaptureRegistry.Load(msgID)
	if !ok {
		return false
	}
	pe := v.(pendingDiskCaptureResult)
	select {
	case pe.ch <- payload:
		return true
	default:
		return false
	}
}

// ─── BiosRead registry (#159) ─────────────────────────────────────────────────

// pendingBiosReadResult is the in-flight state for a bios_read_request.
type pendingBiosReadResult struct {
	ch chan clientd.BiosReadResultPayload
}

// RegisterBiosRead creates a pending BIOS read entry for msgID and returns the
// channel to read the BiosReadResultPayload from.  The caller must call
// UnregisterBiosRead(msgID) when done.
func (h *ClientdHub) RegisterBiosRead(msgID string) <-chan clientd.BiosReadResultPayload {
	ch := make(chan clientd.BiosReadResultPayload, 1)
	h.biosReadRegistry.Store(msgID, pendingBiosReadResult{ch: ch})
	return ch
}

// UnregisterBiosRead removes a pending BIOS read entry.
func (h *ClientdHub) UnregisterBiosRead(msgID string) {
	h.biosReadRegistry.Delete(msgID)
}

// DeliverBiosReadResult delivers a BiosReadResultPayload to the waiting HTTP
// handler.  Called by the WebSocket handler when it receives a
// "bios_read_result" message from the node.  Returns true if delivered.
func (h *ClientdHub) DeliverBiosReadResult(msgID string, payload clientd.BiosReadResultPayload) bool {
	v, ok := h.biosReadRegistry.Load(msgID)
	if !ok {
		return false
	}
	pe := v.(pendingBiosReadResult)
	select {
	case pe.ch <- payload:
		return true
	default:
		return false
	}
}

// ─── BiosApply registry (Sprint 26) ──────────────────────────────────────────

// pendingBiosApplyResult is the in-flight state for a bios_apply_request.
type pendingBiosApplyResult struct {
	ch chan clientd.BiosApplyResultPayload
}

// RegisterBiosApply creates a pending BIOS apply entry for msgID and returns the
// channel to read the BiosApplyResultPayload from.  The caller must call
// UnregisterBiosApply(msgID) when done.
func (h *ClientdHub) RegisterBiosApply(msgID string) <-chan clientd.BiosApplyResultPayload {
	ch := make(chan clientd.BiosApplyResultPayload, 1)
	h.biosApplyRegistry.Store(msgID, pendingBiosApplyResult{ch: ch})
	return ch
}

// UnregisterBiosApply removes a pending BIOS apply entry.
func (h *ClientdHub) UnregisterBiosApply(msgID string) {
	h.biosApplyRegistry.Delete(msgID)
}

// DeliverBiosApplyResult delivers a BiosApplyResultPayload to the waiting HTTP
// handler.  Called by the WebSocket handler when it receives a
// "bios_apply_result" message from the node.  Returns true if delivered.
func (h *ClientdHub) DeliverBiosApplyResult(msgID string, payload clientd.BiosApplyResultPayload) bool {
	v, ok := h.biosApplyRegistry.Load(msgID)
	if !ok {
		return false
	}
	pe := v.(pendingBiosApplyResult)
	select {
	case pe.ch <- payload:
		return true
	default:
		return false
	}
}

// ─── LDAPHealth registry (fix/v0.1.22-ldap-reverify) ─────────────────────────

// pendingLDAPHealthResult is the in-flight state for an ldap_health_request.
type pendingLDAPHealthResult struct {
	ch chan clientd.LDAPHealthResultPayload
}

// RegisterLDAPHealth creates a pending LDAP health entry for msgID and returns
// the channel to read the LDAPHealthResultPayload from. The caller must call
// UnregisterLDAPHealth(msgID) when done.
func (h *ClientdHub) RegisterLDAPHealth(msgID string) <-chan clientd.LDAPHealthResultPayload {
	ch := make(chan clientd.LDAPHealthResultPayload, 1)
	h.ldapHealthRegistry.Store(msgID, pendingLDAPHealthResult{ch: ch})
	return ch
}

// UnregisterLDAPHealth removes a pending LDAP health entry.
func (h *ClientdHub) UnregisterLDAPHealth(msgID string) {
	h.ldapHealthRegistry.Delete(msgID)
}

// DeliverLDAPHealthResult delivers an LDAPHealthResultPayload to the waiting
// HTTP handler. Called by the WebSocket handler when it receives an
// "ldap_health_result" message from the node. Returns true if delivered.
func (h *ClientdHub) DeliverLDAPHealthResult(msgID string, payload clientd.LDAPHealthResultPayload) bool {
	v, ok := h.ldapHealthRegistry.Load(msgID)
	if !ok {
		return false
	}
	pe := v.(pendingLDAPHealthResult)
	select {
	case pe.ch <- payload:
		return true
	default:
		return false
	}
}

//lint:ignore U1000 scaffolding for Sprint 34 WS server-push flow (CLIENTD-PUSH); not yet wired into a route
// runSendLoop drains the conn's send channel, writing messages to the WebSocket.
// Runs in its own goroutine; returns when the channel is closed or a write fails.
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
