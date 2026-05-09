// Package handlers — console_sol.go (Sprint 34 SERIAL-CONSOLE)
//
// WebSocket bridge over `ipmitool sol activate` for the per-node Serial-over-
// LAN endpoint at GET /api/v1/nodes/{id}/console/sol.
//
// # Wire protocol (xterm.js side)
//
//   - Both directions are RAW BYTES wrapped in WebSocket BINARY frames
//     (websocket.BinaryMessage). The client sends the JS
//     Buffer.toString('binary') payload of every keystroke/paste; the
//     server forwards them verbatim to ipmitool's stdin. The server reads
//     ipmitool's stdout and pipes each chunk back as a single
//     BinaryMessage.
//
//   - The server does NOT interpret escape sequences. xterm.js handles the
//     ANSI/VT100 stream end-to-end. The SOL stream from the BMC is already
//     a faithful serial console.
//
//   - Stdin is line-mode at the operator's xterm — xterm.js queues until
//     the operator presses Enter and then sends one frame containing the
//     accumulated bytes. This matches the BMC's KCS+SOL console which
//     expects line-buffered input.
//
//   - Stdout is raw bytes — the server reads from the PTY in 4KB chunks
//     and writes each chunk as one BinaryMessage. xterm.js renders
//     byte-by-byte.
//
//   - When the SOL session ends (BMC disconnect, ipmitool exits, operator
//     types ~. ), the server sends a final TextMessage:
//     {"type":"exit","code":<int>,"error":"<msg>"} then closes the WS.
//
//   - Single-active-session per node: a second connect for the same node
//     closes the first session cleanly. The server kills ipmitool and
//     cancels the bridge context for the loser, then upgrades the winner.
//
// # Auth
//
// Admin scope. The route is wired with requireRole("admin") in server.go.
// The wsTokenLift middleware hoists ?token= into the Authorization header
// so browser WS clients (which can't set custom headers on upgrade) work.
//
// # Privilege boundary
//
// The actual `ipmitool sol activate` invocation goes through
// clustr-privhelper (verb: ipmi-sol-activate) so the BMC password never
// appears in clustr-serverd's argv — the password is written to a 0600
// temp file by the helper as root. clustr-serverd runs as the unprivileged
// "clustr" user.

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/pkg/api"
)

const (
	// solReadBufSize is the read buffer size for the upstream SOL pty
	// stream.
	solReadBufSize = 4096

	// solWSPingInterval is how often the server sends a WS ping when
	// stdout is idle, so middleboxes (NAT, load balancers) don't time out
	// the connection during long BIOS POST waits.
	solWSPingInterval = 30 * time.Second

	// solPrivhelperBinary is the path to the setuid helper that executes
	// the actual ipmitool invocation. The helper validates argv and writes
	// the BMC password to a 0600 temp file before exec'ing ipmitool.
	solPrivhelperBinary = "/usr/sbin/clustr-privhelper"
)

// SOLBridge is the per-node bridge state. One bridge per node — a second
// connect supersedes the first.
type SOLBridge struct {
	NodeID  string
	Started time.Time
	// cancel terminates the bridge: signals the upstream goroutine to
	// stop, kills ipmitool, and unblocks the WS reader.
	cancel context.CancelFunc
}

// SOLConsoleHandler exposes GET /api/v1/nodes/{id}/console/sol with
// single-active-session per node semantics.
//
// Concurrency invariant: the active map is guarded by mu. Every read/write
// of `active` must hold the mutex. Goroutines spawned per-bridge clean up
// their own entry under the mutex when they exit.
type SOLConsoleHandler struct {
	DB ConsoleDB

	mu     sync.Mutex
	active map[string]*SOLBridge

	// Spawn is the function used to start the privhelper subprocess with a
	// PTY. Tests inject a fake to avoid touching the real binary;
	// production uses defaultSOLSpawn which exec's clustr-privhelper.
	Spawn SOLSpawnFunc
}

// SOLSpawnFunc is the abstraction over the privhelper subprocess. It returns
// the PTY (which is also the read/write side of stdin+stdout for ipmitool),
// the underlying cmd (so the caller can Wait() for exit), and any error.
type SOLSpawnFunc func(ctx context.Context, creds solCreds) (io.ReadWriteCloser, *exec.Cmd, error)

// solCreds is the minimal credential bundle for a single SOL session.
type solCreds struct {
	Host     string
	Username string
	Password string
}

// NewSOLConsoleHandler returns a handler with the production spawn function.
func NewSOLConsoleHandler(db ConsoleDB) *SOLConsoleHandler {
	return &SOLConsoleHandler{
		DB:     db,
		active: make(map[string]*SOLBridge),
		Spawn:  defaultSOLSpawn,
	}
}

// solExitMsg matches the existing consoleExitMsg shape so the xterm.js side
// can use one decoder for both endpoints.
type solExitMsg struct {
	Type  string `json:"type"` // "exit" | "superseded"
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// HandleSOL upgrades the HTTP connection to a WebSocket and bridges it to
// a fresh `ipmitool sol activate` subprocess (via privhelper).
func (h *SOLConsoleHandler) HandleSOL(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if nodeID == "" {
		http.Error(w, "node id is required", http.StatusBadRequest)
		return
	}

	cfg, err := h.DB.GetNodeConfig(r.Context(), nodeID)
	if err != nil {
		http.Error(w, fmt.Sprintf("node not found: %v", err), http.StatusNotFound)
		return
	}
	creds, ok := resolveSOLCreds(cfg)
	if !ok {
		http.Error(w, "node has no BMC configured", http.StatusBadRequest)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("sol ws: upgrade failed")
		return
	}
	defer conn.Close()

	// Single-active-session enforcement: if another bridge is active for
	// this node, supersede it cleanly before starting ours.
	h.superseded(nodeID)

	bridgeCtx, cancel := context.WithCancel(r.Context())
	defer cancel()

	pipe, cmd, err := h.Spawn(bridgeCtx, creds)
	if err != nil {
		sendSOLExit(conn, -1, fmt.Sprintf("start sol session: %v", err))
		return
	}
	defer pipe.Close()
	defer func() {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		if cmd != nil {
			_ = cmd.Wait()
		}
	}()

	bridge := &SOLBridge{
		NodeID:  nodeID,
		Started: time.Now().UTC(),
		cancel:  cancel,
	}
	h.mu.Lock()
	h.active[nodeID] = bridge
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if cur := h.active[nodeID]; cur == bridge {
			delete(h.active, nodeID)
		}
		h.mu.Unlock()
	}()

	log.Info().Str("node_id", nodeID).Str("bmc_host", creds.Host).Msg("sol ws: session started")

	exitCode := pumpSOLBridge(bridgeCtx, conn, pipe)

	sendSOLExit(conn, exitCode, "")
	log.Info().Str("node_id", nodeID).Int("exit_code", exitCode).Msg("sol ws: session ended")
}

// superseded closes any existing bridge for nodeID and removes it from the
// active map. Called from HandleSOL before starting a new bridge.
func (h *SOLConsoleHandler) superseded(nodeID string) {
	h.mu.Lock()
	prev, ok := h.active[nodeID]
	if ok {
		delete(h.active, nodeID)
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	log.Info().Str("node_id", nodeID).Msg("sol ws: superseding previous session")
	prev.cancel()
}

// resolveSOLCreds extracts BMC credentials from the node config for an SOL
// session. Returns ok==false when no usable host is present.
func resolveSOLCreds(cfg api.NodeConfig) (solCreds, bool) {
	var c solCreds
	if cfg.PowerProvider != nil && cfg.PowerProvider.Fields["host"] != "" {
		c.Host = cfg.PowerProvider.Fields["host"]
		c.Username = cfg.PowerProvider.Fields["username"]
		c.Password = cfg.PowerProvider.Fields["password"]
	} else if cfg.BMC != nil {
		c.Host = cfg.BMC.IPAddress
		c.Username = cfg.BMC.Username
		c.Password = cfg.BMC.Password
	}
	if c.Host == "" {
		return c, false
	}
	return c, true
}

// pumpSOLBridge runs the bidirectional bridge until either side closes.
// Returns the upstream exit code (0 for clean close, -1 if the WS dropped
// first).
func pumpSOLBridge(ctx context.Context, conn *websocket.Conn, pipe io.ReadWriteCloser) int {
	upstreamDone := make(chan int, 1)
	go func() {
		buf := make([]byte, solReadBufSize)
		for {
			n, err := pipe.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					upstreamDone <- -1
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					upstreamDone <- 0
				} else {
					upstreamDone <- 1
				}
				return
			}
		}
	}()

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if len(data) == 0 {
				continue
			}
			if _, writeErr := pipe.Write(data); writeErr != nil {
				return
			}
		}
	}()

	pingTicker := time.NewTicker(solWSPingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case code := <-upstreamDone:
			return code
		case <-clientDone:
			return -1
		case <-ctx.Done():
			return -2
		case <-pingTicker.C:
			deadline := time.Now().Add(5 * time.Second)
			if err := conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				return -1
			}
		}
	}
}

// sendSOLExit emits the final exit JSON frame.
func sendSOLExit(conn *websocket.Conn, code int, msg string) {
	b, _ := json.Marshal(solExitMsg{Type: "exit", Code: code, Error: msg})
	_ = conn.WriteMessage(websocket.TextMessage, b)
}

// defaultSOLSpawn shells out to clustr-privhelper ipmi-sol-activate,
// attaches a PTY so ipmitool's tty checks succeed, and writes the
// credentials JSON envelope to the helper's stdin.
//
// The PTY is the io.ReadWriteCloser the bridge pumps to/from.
func defaultSOLSpawn(ctx context.Context, creds solCreds) (io.ReadWriteCloser, *exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, solPrivhelperBinary, "ipmi-sol-activate") //#nosec G204 -- helperBinary is a fixed literal; verb is hardcoded

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("pty start: %w", err)
	}

	envelope, err := json.Marshal(map[string]string{
		"host":     creds.Host,
		"username": creds.Username,
		"password": creds.Password,
	})
	if err != nil {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, fmt.Errorf("encode creds: %w", err)
	}
	envelope = append(envelope, '\n')
	if _, err := ptmx.Write(envelope); err != nil {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, fmt.Errorf("write creds: %w", err)
	}

	return ptmx, cmd, nil
}
