package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clonr/internal/clientd"
)

// ClientdHub tracks all active clonr-clientd WebSocket connections, keyed by node ID.
// It is safe for concurrent use.
//
// It implements handlers.ClientdHubIface (RegisterConn, Unregister, ConnectedNodes,
// IsConnected) — confirmed at compile time by the assignment in server.go.
type ClientdHub struct {
	mu    sync.RWMutex
	conns map[string]*clientdConn
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
		conns: make(map[string]*clientdConn),
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
