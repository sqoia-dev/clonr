package clientd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

const (
	heartbeatInterval = 60 * time.Second
	writeTimeout      = 10 * time.Second
	readTimeout       = 90 * time.Second // must be > heartbeatInterval

	// Reconnect backoff: 5s, 10s, 20s, 40s, cap 5min.
	backoffInitial = 5 * time.Second
	backoffMax     = 5 * time.Minute
)

// Client is the clonr-clientd WebSocket client. It maintains a persistent
// connection to clonr-serverd, sending heartbeats and dispatching server messages.
type Client struct {
	serverURL string
	tokenPath string
	nodeID    string
	version   string

	// send is the outbound message channel. The writeLoop drains it.
	send chan []byte

	// journalMu guards the active JournalStreamer.
	journalMu      sync.Mutex
	journalStreamer *JournalStreamer

	// nodeMAC is read from the token file path context; populated lazily.
	// For journal entries we need a MAC address to stamp on each LogEntry.
	// We derive it from /etc/clonr/node-mac if present, falling back to empty string.
	nodeMAC string
}

// New creates a Client. serverURL is the full ws:// or wss:// URL for the
// clientd endpoint (e.g. ws://192.168.1.10:8080/api/v1/nodes/abc123/clientd/ws).
// tokenPath is the path to the node-token file (default /etc/clonr/node-token).
// version is the binary version string injected at build time.
func New(serverURL, tokenPath, version string) (*Client, error) {
	if serverURL == "" {
		return nil, fmt.Errorf("clientd: serverURL is required")
	}
	if tokenPath == "" {
		tokenPath = "/etc/clonr/node-token"
	}

	// Extract node ID from the URL path: .../nodes/{id}/clientd/ws
	nodeID, err := extractNodeID(serverURL)
	if err != nil {
		return nil, fmt.Errorf("clientd: could not extract node ID from URL %q: %w", serverURL, err)
	}

	// Read node MAC from /etc/clonr/node-mac if present.
	// Not fatal — missing MAC means journal entries omit node_mac until server fills it in.
	var nodeMAC string
	if data, err := os.ReadFile("/etc/clonr/node-mac"); err == nil {
		nodeMAC = strings.TrimSpace(string(data))
	}

	return &Client{
		serverURL: serverURL,
		tokenPath: tokenPath,
		nodeID:    nodeID,
		version:   version,
		send:      make(chan []byte, 64),
		nodeMAC:   nodeMAC,
	}, nil
}

// Run is the main loop. It connects to the server and reconnects with exponential
// backoff on any connection failure. It returns only when ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	backoff := backoffInitial

	for {
		err := c.connect(ctx)
		if err == nil {
			// connect returned nil only when ctx was cancelled.
			return ctx.Err()
		}

		// Check if context is done before sleeping.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		log.Warn().Err(err).Dur("backoff", backoff).
			Msg("clientd: connection failed, reconnecting after backoff")

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

// connect attempts a single WebSocket connection. Returns when the connection
// closes (cleanly or with an error). On 401 it re-reads the token file before
// returning so the next reconnect attempt uses the fresh token.
func (c *Client) connect(ctx context.Context) error {
	token, err := c.readToken()
	if err != nil {
		return fmt.Errorf("clientd: read token: %w", err)
	}

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+token)

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}

	log.Info().Str("url", c.serverURL).Msg("clientd: connecting to server")

	conn, resp, err := dialer.DialContext(ctx, c.serverURL, hdr)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			log.Warn().Msg("clientd: 401 Unauthorized — token may have been rotated; re-reading token file on next attempt")
			return fmt.Errorf("clientd: 401 Unauthorized")
		}
		return fmt.Errorf("clientd: dial: %w", err)
	}
	defer conn.Close()

	log.Info().Str("node_id", c.nodeID).Msg("clientd: connected to server")

	// Reset backoff on successful connection is handled by the caller resetting
	// the backoff variable before calling connect — it is reset implicitly when
	// a new connection succeeds because we return nil only on ctx cancellation.

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	// readDone signals that the read loop has exited.
	readDone := make(chan error, 1)

	go func() {
		readDone <- c.readLoop(connCtx, conn)
	}()

	// Send hello immediately.
	if err := c.sendHello(conn); err != nil {
		return fmt.Errorf("clientd: send hello: %w", err)
	}

	// Write loop runs in the foreground, draining c.send and the heartbeat ticker.
	writeErr := c.writeLoop(connCtx, conn)

	// Wait for the read loop to stop.
	connCancel()
	<-readDone

	if ctx.Err() != nil {
		// Parent context cancelled — send goodbye and close cleanly.
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "clonr-clientd shutting down"),
			time.Now().Add(writeTimeout),
		)
		return nil
	}

	return writeErr
}

// readLoop reads messages from the server and dispatches them by type.
func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		conn.SetReadDeadline(time.Now().Add(readTimeout))

		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("clientd: read: %w", err)
		}

		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Warn().Err(err).Msg("clientd: received malformed server message")
			continue
		}

		c.dispatchServerMessage(msg)
	}
}

// dispatchServerMessage handles a single message from the server.
func (c *Client) dispatchServerMessage(msg ServerMessage) {
	switch msg.Type {
	case "ack":
		// Server acknowledged a client message — no action needed.
		log.Debug().Str("ref_msg_id", msg.MsgID).Msg("clientd: received ack from server")

	case "log_pull_start":
		c.handleLogPullStart(msg)

	case "log_pull_stop":
		c.stopJournalStreamer()
		log.Info().Msg("clientd: journal streaming stopped by server request")

	case "config_push":
		c.handleConfigPush(msg)

	case "slurm_config_push":
		c.handleSlurmConfigPush(msg)

	default:
		log.Debug().Str("type", msg.Type).Str("msg_id", msg.MsgID).
			Msg("clientd: received unknown server message type (ignored)")
	}
}

// handleConfigPush parses a config_push payload, applies it, and sends an ack.
func (c *Client) handleConfigPush(msg ServerMessage) {
	var payload ConfigPushPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		log.Warn().Err(err).Str("msg_id", msg.MsgID).Msg("clientd: malformed config_push payload")
		c.sendAck(msg.MsgID, false, "malformed payload: "+err.Error())
		return
	}

	log.Info().
		Str("msg_id", msg.MsgID).
		Str("target", payload.Target).
		Msg("clientd: applying config push")

	if err := applyConfig(payload); err != nil {
		log.Error().Err(err).Str("msg_id", msg.MsgID).Str("target", payload.Target).
			Msg("clientd: config push apply failed")
		c.sendAck(msg.MsgID, false, err.Error())
		return
	}

	log.Info().Str("target", payload.Target).Str("msg_id", msg.MsgID).
		Msg("clientd: config push applied successfully")
	c.sendAck(msg.MsgID, true, "")
}

// handleSlurmConfigPush parses a slurm_config_push payload, applies all config
// files atomically, runs the apply action, and sends a structured ack back.
func (c *Client) handleSlurmConfigPush(msg ServerMessage) {
	var payload SlurmConfigPushPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		log.Warn().Err(err).Str("msg_id", msg.MsgID).Msg("clientd: malformed slurm_config_push payload")
		c.sendAck(msg.MsgID, false, "malformed slurm_config_push payload: "+err.Error())
		return
	}

	log.Info().
		Str("msg_id", msg.MsgID).
		Str("push_op_id", payload.PushOpID).
		Int("files", len(payload.Files)).
		Str("apply_action", payload.ApplyAction).
		Msg("clientd: applying slurm config push")

	result := applySlurmConfig(payload)

	// Re-use the generic ack channel so the server's ack registry can deliver
	// the ack to the waiting push orchestrator goroutine.
	c.sendSlurmAck(msg.MsgID, result)

	if result.OK {
		log.Info().Str("push_op_id", payload.PushOpID).Msg("clientd: slurm config push applied successfully")
	} else {
		log.Error().Str("push_op_id", payload.PushOpID).Str("error", result.Error).
			Msg("clientd: slurm config push apply failed")
	}
}

// sendSlurmAck sends an "ack" message carrying a SlurmConfigAckPayload in the Error field
// and a JSON-encoded structured payload. The server ack registry receives an AckPayload;
// the Slurm manager reads the raw payload from the registered ack channel.
//
// Protocol: we embed the SlurmConfigAckPayload as JSON inside AckPayload.Error.
// The server-side push orchestrator reads the AckPayload.Error as a JSON string
// and unmarshals it to get the full SlurmConfigAckPayload. This re-uses the
// existing ack delivery infrastructure without protocol changes.
func (c *Client) sendSlurmAck(refMsgID string, result SlurmConfigAckPayload) {
	// Encode the full result as JSON for the server to parse.
	resultJSON, err := json.Marshal(result)
	if err != nil {
		log.Warn().Err(err).Msg("clientd: failed to marshal slurm ack result")
		c.sendAck(refMsgID, false, "failed to marshal slurm ack result")
		return
	}

	ackPayload, err := json.Marshal(AckPayload{
		RefMsgID: refMsgID,
		OK:       result.OK,
		Error:    string(resultJSON), // server decodes this as SlurmConfigAckPayload JSON
	})
	if err != nil {
		log.Warn().Err(err).Msg("clientd: failed to marshal slurm ack payload")
		return
	}

	msg := ClientMessage{
		Type:    "ack",
		MsgID:   uuid.New().String(),
		Payload: json.RawMessage(ackPayload),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		log.Warn().Err(err).Msg("clientd: failed to marshal slurm ack message")
		return
	}

	select {
	case c.send <- data:
	default:
		log.Warn().Str("ref_msg_id", refMsgID).
			Msg("clientd: slurm ack dropped — send buffer full")
	}
}

// sendAck enqueues an ack message referencing the given server message ID.
func (c *Client) sendAck(refMsgID string, ok bool, errMsg string) {
	ackPayload, err := json.Marshal(AckPayload{
		RefMsgID: refMsgID,
		OK:       ok,
		Error:    errMsg,
	})
	if err != nil {
		log.Warn().Err(err).Msg("clientd: failed to marshal ack payload")
		return
	}

	msg := ClientMessage{
		Type:    "ack",
		MsgID:   uuid.New().String(),
		Payload: json.RawMessage(ackPayload),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		log.Warn().Err(err).Msg("clientd: failed to marshal ack message")
		return
	}

	select {
	case c.send <- data:
	default:
		log.Warn().Str("ref_msg_id", refMsgID).
			Msg("clientd: ack dropped — send buffer full")
	}
}

// handleLogPullStart parses the log_pull_start payload and starts a JournalStreamer.
func (c *Client) handleLogPullStart(msg ServerMessage) {
	var payload LogPullStartPayload
	if len(msg.Payload) > 0 {
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			log.Warn().Err(err).Msg("clientd: malformed log_pull_start payload")
			return
		}
	}

	// Stop any existing streamer before starting a new one.
	c.stopJournalStreamer()

	log.Info().
		Strs("units", payload.Units).
		Int("priority", payload.Priority).
		Str("since", payload.Since).
		Msg("clientd: starting journal streaming")

	streamer := NewJournalStreamer(payload.Units, payload.Priority, payload.Since, c.nodeMAC)
	if err := streamer.Start(context.Background(), payload.Units, payload.Priority, payload.Since); err != nil {
		log.Error().Err(err).Msg("clientd: failed to start journalctl — is journalctl available?")
		return
	}

	c.journalMu.Lock()
	c.journalStreamer = streamer
	c.journalMu.Unlock()

	// Forward batches as log_batch messages to the server.
	go c.forwardJournalBatches(streamer)
}

// forwardJournalBatches reads batches from the streamer and sends them as log_batch messages.
func (c *Client) forwardJournalBatches(streamer *JournalStreamer) {
	for batch := range streamer.Batches() {
		if len(batch) == 0 {
			continue
		}

		payload, err := json.Marshal(batch)
		if err != nil {
			log.Warn().Err(err).Msg("clientd: failed to marshal log batch")
			continue
		}

		msg := ClientMessage{
			Type:    "log_batch",
			MsgID:   uuid.New().String(),
			Payload: json.RawMessage(payload),
		}
		data, err := json.Marshal(msg)
		if err != nil {
			log.Warn().Err(err).Msg("clientd: failed to marshal log_batch message")
			continue
		}

		select {
		case c.send <- data:
			log.Debug().Int("count", len(batch)).Msg("clientd: sent log_batch to server")
		default:
			log.Warn().Int("count", len(batch)).
				Msg("clientd: log_batch dropped — send buffer full")
		}
	}
}

// stopJournalStreamer stops the active JournalStreamer if one is running.
func (c *Client) stopJournalStreamer() {
	c.journalMu.Lock()
	s := c.journalStreamer
	c.journalStreamer = nil
	c.journalMu.Unlock()

	if s != nil {
		s.Stop()
	}
}


// writeLoop sends messages from the c.send channel and fires the heartbeat ticker.
// Returns when ctx is cancelled or a write fails.
func (c *Client) writeLoop(ctx context.Context, conn *websocket.Conn) error {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case data := <-c.send:
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return fmt.Errorf("clientd: write: %w", err)
			}

		case <-ticker.C:
			if err := c.sendHeartbeat(conn); err != nil {
				return fmt.Errorf("clientd: send heartbeat: %w", err)
			}
		}
	}
}

// sendHello constructs and sends the "hello" message.
func (c *Client) sendHello(conn *websocket.Conn) error {
	hostname, _ := os.Hostname()

	var kernelVersion string
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		kernelVersion = strings.TrimSpace(string(data))
	}

	var uptimeSeconds float64
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		if fields := strings.Fields(string(data)); len(fields) >= 1 {
			_, _ = fmt.Sscanf(fields[0], "%f", &uptimeSeconds)
		}
	}

	payload, err := json.Marshal(HelloPayload{
		NodeID:         c.nodeID,
		Hostname:       hostname,
		KernelVersion:  kernelVersion,
		UptimeSeconds:  uptimeSeconds,
		ClientdVersion: c.version,
	})
	if err != nil {
		return err
	}

	msg := ClientMessage{
		Type:    "hello",
		MsgID:   uuid.New().String(),
		Payload: json.RawMessage(payload),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.WriteMessage(websocket.TextMessage, data)
}

// sendHeartbeat collects metrics and sends a "heartbeat" message.
func (c *Client) sendHeartbeat(conn *websocket.Conn) error {
	hb := collectHeartbeat(c.version)

	payload, err := json.Marshal(hb)
	if err != nil {
		return err
	}

	msg := ClientMessage{
		Type:    "heartbeat",
		MsgID:   uuid.New().String(),
		Payload: json.RawMessage(payload),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err = conn.WriteMessage(websocket.TextMessage, data)
	if err == nil {
		log.Debug().Msg("clientd: heartbeat sent")
	}
	return err
}

// readToken reads the node API token from the token file.
func (c *Client) readToken() (string, error) {
	data, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// extractNodeID parses the node ID from the WebSocket URL path.
// Expected format: .../nodes/{id}/clientd/ws
func extractNodeID(rawURL string) (string, error) {
	// Strip ws:// or wss:// scheme and host to get the path.
	// Simple string parsing is sufficient — no need for url.Parse overhead.
	path := rawURL
	if idx := strings.Index(rawURL, "://"); idx != -1 {
		rest := rawURL[idx+3:]
		if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
			path = rest[slashIdx:]
		}
	}

	// Find /nodes/<id>/clientd/ws pattern.
	const nodesPrefix = "/nodes/"
	const clientdSuffix = "/clientd/ws"

	nodesIdx := strings.Index(path, nodesPrefix)
	if nodesIdx < 0 {
		return "", fmt.Errorf("URL path does not contain /nodes/")
	}
	afterNodes := path[nodesIdx+len(nodesPrefix):]

	clientdIdx := strings.Index(afterNodes, clientdSuffix)
	if clientdIdx < 0 {
		return "", fmt.Errorf("URL path does not contain /clientd/ws suffix")
	}
	nodeID := afterNodes[:clientdIdx]
	if nodeID == "" {
		return "", fmt.Errorf("node ID is empty in URL path")
	}
	return nodeID, nil
}
