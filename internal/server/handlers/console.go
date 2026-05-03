package handlers

// console.go — WS /api/v1/console/{node_id} (#128)
//
// Accepts a WebSocket from the operator, opens the upstream console session
// (IPMI SOL via ipmitool or SSH PTY), and pipes bidirectionally.
//
// Upstream mode selection:
//  1. If the node has BMC credentials configured, use IPMI SOL (--ipmi-sol).
//  2. Otherwise fall back to SSH PTY (--ssh) using the server's node management
//     key ($CLUSTR_NODE_MGMT_KEY, default /etc/clustr/node_mgmt_key).
//
// The operator connects with a plain WebSocket. Bytes received from the operator
// are written to the upstream pty; bytes read from the upstream pty are forwarded
// to the operator. The session is 1:1 — exactly one node per WebSocket connection.
//
// Escape character (~.) is handled client-side by the CLI; the server does not
// intercept or interpret it. This keeps the server broker transport-agnostic.
//
// Wire protocol:
//   client → server: raw bytes as a WebSocket text frame.
//   server → client: raw bytes as a WebSocket text frame.
//   server → client: JSON control frame when session ends:
//     {"type":"exit","code":<int>,"error":"<msg>"}

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	gossh "golang.org/x/crypto/ssh"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

const (
	// consoleWriteDeadline is the per-write deadline to the upstream process.
	consoleWriteDeadline = 5 * time.Second

	// defaultNodeMgmtKey is the default path to the server's node management SSH key.
	// Override with CLUSTR_NODE_MGMT_KEY.
	defaultNodeMgmtKey = "/etc/clustr/node_mgmt_key"

	// defaultConsoleSSHUser is the default SSH user for console sessions.
	// Override with CLUSTR_CONSOLE_SSH_USER.
	defaultConsoleSSHUser = "root"

	// consoleSSHDialTimeout caps the initial SSH dial.
	consoleSSHDialTimeout = 15 * time.Second

	// consoleUpstreamReadBuf is the read buffer size for the upstream pty/SSH stream.
	consoleUpstreamReadBuf = 4096
)

// ConsoleDB is the minimal database interface needed by ConsoleHandler.
type ConsoleDB interface {
	GetNodeConfig(ctx context.Context, nodeID string) (api.NodeConfig, error)
}

// ConsoleHandler handles WS /api/v1/console/{node_id}.
type ConsoleHandler struct {
	DB ConsoleDB
}

// consoleExitMsg is sent as a final WebSocket text frame when the session ends.
type consoleExitMsg struct {
	Type  string `json:"type"`  // always "exit"
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
}

// HandleConsole upgrades the HTTP connection to a WebSocket console session.
//
// Query parameters (both optional):
//
//	mode=ipmi-sol   force IPMI SOL (error if no BMC configured)
//	mode=ssh        force SSH PTY (error if no node IP configured)
//
// When mode is absent the handler auto-selects: IPMI SOL if BMC is configured,
// SSH otherwise.
func (h *ConsoleHandler) HandleConsole(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	if nodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}

	modeParam := r.URL.Query().Get("mode")

	// AUTH: bearer token may be supplied via Authorization header OR ?token=
	// query param. The query-param fallback exists ONLY because browsers cannot
	// set custom headers on WebSocket upgrade requests; wsTokenLift middleware
	// hoists ?token= into the Authorization header before apiKeyAuth runs, so
	// by the time execution reaches here the request is already authenticated.
	// This is one of the TWO places in the API where ?token= is accepted (the
	// other is ShellWS). HTTP endpoints reject query-param tokens — see
	// wsTokenLift / extractBearerToken in middleware.go.

	// Upgrade to WebSocket before any blocking work so the client gets a fast
	// error response if the upgrade fails.
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("console ws: upgrade failed")
		return
	}
	defer conn.Close()

	cfg, err := h.DB.GetNodeConfig(r.Context(), nodeID)
	if err != nil {
		sendConsoleError(conn, fmt.Sprintf("node not found: %v", err))
		return
	}

	// Resolve upstream mode.
	mode, err := resolveConsoleMode(modeParam, cfg)
	if err != nil {
		sendConsoleError(conn, err.Error())
		return
	}

	log.Info().
		Str("node_id", nodeID).
		Str("hostname", cfg.Hostname).
		Str("mode", mode).
		Msg("console ws: session starting")

	switch mode {
	case "ipmi-sol":
		h.runSOLSession(r.Context(), conn, nodeID, cfg)
	case "ssh":
		h.runSSHSession(r.Context(), conn, nodeID, cfg)
	default:
		sendConsoleError(conn, fmt.Sprintf("unknown console mode %q", mode))
	}
}

// resolveConsoleMode determines the upstream mode from the query parameter and
// the node's configuration. Auto-selects when mode is empty.
func resolveConsoleMode(modeParam string, cfg api.NodeConfig) (string, error) {
	hasBMC := (cfg.BMC != nil && cfg.BMC.IPAddress != "") ||
		(cfg.PowerProvider != nil && cfg.PowerProvider.Fields["host"] != "")

	switch modeParam {
	case "ipmi-sol":
		if !hasBMC {
			return "", fmt.Errorf("ipmi-sol mode requires BMC configuration; node has none")
		}
		return "ipmi-sol", nil
	case "ssh":
		if nodeIP(cfg) == "" {
			return "", fmt.Errorf("ssh mode requires a node IP address; node has none")
		}
		return "ssh", nil
	case "":
		// Auto-select.
		if hasBMC {
			return "ipmi-sol", nil
		}
		if nodeIP(cfg) != "" {
			return "ssh", nil
		}
		return "", fmt.Errorf("console requires either a BMC or a node IP address; node has neither")
	default:
		return "", fmt.Errorf("unknown mode %q; valid: ipmi-sol, ssh", modeParam)
	}
}

// nodeIP extracts the primary node IP address (without CIDR suffix) from cfg.
// Returns empty string when no usable IP is found.
func nodeIP(cfg api.NodeConfig) string {
	for _, iface := range cfg.Interfaces {
		if iface.IPAddress != "" {
			// Strip CIDR suffix if present.
			ip := iface.IPAddress
			for i, c := range ip {
				if c == '/' {
					return ip[:i]
				}
			}
			return ip
		}
	}
	return ""
}

// ─── IPMI SOL ────────────────────────────────────────────────────────────────

// runSOLSession runs an ipmitool sol activate process and pipes it to the WS conn.
func (h *ConsoleHandler) runSOLSession(ctx context.Context, conn *websocket.Conn, nodeID string, cfg api.NodeConfig) {
	// Resolve BMC credentials.
	var bmcHost, bmcUser, bmcPass string
	if cfg.PowerProvider != nil && cfg.PowerProvider.Fields["host"] != "" {
		bmcHost = cfg.PowerProvider.Fields["host"]
		bmcUser = cfg.PowerProvider.Fields["username"]
		bmcPass = cfg.PowerProvider.Fields["password"]
	} else if cfg.BMC != nil {
		bmcHost = cfg.BMC.IPAddress
		bmcUser = cfg.BMC.Username
		bmcPass = cfg.BMC.Password
	}
	if bmcHost == "" {
		sendConsoleError(conn, "no BMC host configured for this node")
		return
	}

	// Write password to a temp file (same pattern as ipmi.Client).
	passFile := ""
	if bmcPass != "" {
		tf, err := os.CreateTemp("", "ipmi-pass-*")
		if err != nil {
			sendConsoleError(conn, fmt.Sprintf("create temp password file: %v", err))
			return
		}
		passFile = tf.Name()
		_ = tf.Chmod(0600)
		_, _ = tf.WriteString(bmcPass)
		_ = tf.Close()
		defer os.Remove(passFile)
	}

	// Build ipmitool arguments.
	var args []string
	if bmcHost != "" {
		passArg := []string{"-I", "lanplus", "-H", bmcHost, "-U", bmcUser}
		if passFile != "" {
			passArg = append(passArg, "-f", passFile)
		}
		args = append(passArg, "sol", "activate")
	} else {
		args = []string{"sol", "activate"}
	}

	runPTYSession(ctx, conn, "ipmitool", args, nodeID, "ipmi-sol")
}

// ─── SSH PTY ──────────────────────────────────────────────────────────────────

// runSSHSession opens an SSH PTY to the node and pipes it to the WS conn.
func (h *ConsoleHandler) runSSHSession(ctx context.Context, conn *websocket.Conn, nodeID string, cfg api.NodeConfig) {
	ip := nodeIP(cfg)
	if ip == "" {
		sendConsoleError(conn, "no node IP address available for SSH console")
		return
	}

	// Resolve SSH key path.
	keyPath := os.Getenv("CLUSTR_NODE_MGMT_KEY")
	if keyPath == "" {
		keyPath = defaultNodeMgmtKey
	}

	sshUser := os.Getenv("CLUSTR_CONSOLE_SSH_USER")
	if sshUser == "" {
		sshUser = defaultConsoleSSHUser
	}

	// Load the private key.
	keyBytes, err := os.ReadFile(keyPath) //#nosec G304 -- operator-configured path
	if err != nil {
		sendConsoleError(conn, fmt.Sprintf("read node management key %s: %v", keyPath, err))
		return
	}

	signer, err := gossh.ParsePrivateKey(keyBytes)
	if err != nil {
		sendConsoleError(conn, fmt.Sprintf("parse node management key: %v", err))
		return
	}

	sshCfg := &gossh.ClientConfig{
		User:            sshUser,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //#nosec G106 -- internal management network; nodes do not have stable host keys pre-enrollment
		Timeout:         consoleSSHDialTimeout,
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, consoleSSHDialTimeout)
	defer dialCancel()

	addr := ip + ":22"
	// ssh.Dial does not accept a context directly; use a goroutine to honour it.
	type dialResult struct {
		client *gossh.Client
		err    error
	}
	dialCh := make(chan dialResult, 1)
	go func() {
		c, e := gossh.Dial("tcp", addr, sshCfg)
		dialCh <- dialResult{c, e}
	}()

	var sshClient *gossh.Client
	select {
	case res := <-dialCh:
		if res.err != nil {
			sendConsoleError(conn, fmt.Sprintf("SSH dial %s: %v", addr, res.err))
			return
		}
		sshClient = res.client
	case <-dialCtx.Done():
		sendConsoleError(conn, fmt.Sprintf("SSH dial %s: timed out after %s", addr, consoleSSHDialTimeout))
		return
	}
	defer sshClient.Close()

	sess, err := sshClient.NewSession()
	if err != nil {
		sendConsoleError(conn, fmt.Sprintf("SSH new session: %v", err))
		return
	}
	defer sess.Close()

	// Request a PTY.
	if err := sess.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{}); err != nil {
		sendConsoleError(conn, fmt.Sprintf("SSH request pty: %v", err))
		return
	}

	// Attach session stdin/stdout/stderr pipes.
	stdin, err := sess.StdinPipe()
	if err != nil {
		sendConsoleError(conn, fmt.Sprintf("SSH stdin pipe: %v", err))
		return
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sendConsoleError(conn, fmt.Sprintf("SSH stdout pipe: %v", err))
		return
	}

	if err := sess.Shell(); err != nil {
		sendConsoleError(conn, fmt.Sprintf("SSH shell: %v", err))
		return
	}

	log.Info().
		Str("node_id", nodeID).
		Str("addr", addr).
		Str("user", sshUser).
		Msg("console ws: SSH session opened")

	var wg sync.WaitGroup
	done := make(chan struct{})

	// SSH stdout → WebSocket.
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, consoleUpstreamReadBuf)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.TextMessage, buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()

	// WebSocket → SSH stdin.
	go func() {
		defer close(done)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if _, writeErr := stdin.Write(data); writeErr != nil {
				return
			}
		}
	}()

	// Wait for shell to exit or WS to close.
	exitCode := 0
	if err := sess.Wait(); err != nil {
		if exitErr, ok := err.(*gossh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		}
	}

	close(done) // signal output goroutine to stop
	wg.Wait()

	// Send exit control frame.
	exitMsg, _ := json.Marshal(consoleExitMsg{Type: "exit", Code: exitCode})
	_ = conn.WriteMessage(websocket.TextMessage, exitMsg)

	log.Info().
		Str("node_id", nodeID).
		Int("exit_code", exitCode).
		Msg("console ws: SSH session ended")
}

// ─── PTY process broker (IPMI SOL) ───────────────────────────────────────────

// runPTYSession starts a process with a PTY attached and bridges it to the
// WebSocket connection. Used for IPMI SOL (ipmitool sol activate).
func runPTYSession(ctx context.Context, conn *websocket.Conn, binary string, args []string, nodeID, modeLabel string) {
	cmd := exec.CommandContext(ctx, binary, args...) //#nosec G204 -- arguments are server-controlled (ipmitool with operator-resolved BMC creds)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		sendConsoleError(conn, fmt.Sprintf("start %s: %v", binary, err))
		return
	}
	defer func() {
		ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		log.Info().
			Str("node_id", nodeID).
			Str("mode", modeLabel).
			Msg("console ws: PTY session ended")
	}()

	log.Info().
		Str("node_id", nodeID).
		Str("mode", modeLabel).
		Str("binary", binary).
		Msg("console ws: PTY session started")

	var wg sync.WaitGroup
	ptyClosed := make(chan struct{})

	// PTY stdout → WebSocket.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ptyClosed)
		buf := make([]byte, consoleUpstreamReadBuf)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.TextMessage, buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WebSocket → PTY stdin.
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			// WS disconnected — kill the upstream.
			break
		}
		if len(data) == 0 {
			continue
		}
		if _, writeErr := ptmx.Write(data); writeErr != nil {
			break
		}

		// Check if the upstream already exited.
		select {
		case <-ptyClosed:
			sendConsoleExit(conn, 0, "")
			return
		default:
		}
	}

	// Wait for PTY goroutine to finish.
	wg.Wait()
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// sendConsoleError sends a structured error message to the WS client and closes.
func sendConsoleError(conn *websocket.Conn, msg string) {
	log.Warn().Str("error", msg).Msg("console ws: error")
	b, _ := json.Marshal(consoleExitMsg{Type: "exit", Code: -1, Error: msg})
	_ = conn.WriteMessage(websocket.TextMessage, b)
	_ = conn.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, msg),
	)
}

// sendConsoleExit sends a clean exit message to the WS client.
func sendConsoleExit(conn *websocket.Conn, code int, msg string) {
	b, _ := json.Marshal(consoleExitMsg{Type: "exit", Code: code, Error: msg})
	_ = conn.WriteMessage(websocket.TextMessage, b)
}

// ─── DB adapter ──────────────────────────────────────────────────────────────

// ConsoleDBAdapter adapts *db.DB to satisfy ConsoleDB.
type ConsoleDBAdapter struct {
	inner *db.DB
}

// NewConsoleDBAdapter creates a new ConsoleDBAdapter.
func NewConsoleDBAdapter(inner *db.DB) *ConsoleDBAdapter {
	return &ConsoleDBAdapter{inner: inner}
}

// GetNodeConfig delegates to the underlying DB.
func (a *ConsoleDBAdapter) GetNodeConfig(ctx context.Context, nodeID string) (api.NodeConfig, error) {
	return a.inner.GetNodeConfig(ctx, nodeID)
}
