package main

// console_cmd.go — `clustr console` command (#128)
//
// Usage:
//
//	clustr console -n NODE [--ipmi-sol | --ssh] [-e ESCAPE]
//
// Opens a brokered terminal console to a single node via the server's
// WS /api/v1/console/{node_id} endpoint.
//
// The server opens the upstream session (IPMI SOL via ipmitool, or SSH PTY)
// and pipes it bidirectionally. The CLI puts the local terminal in raw mode and
// relays bytes transparently.
//
// Escape character handling (--escape / -e, default ~.):
//   - The CLI reads the escape sequence locally in raw mode.
//   - When the operator types the escape sequence (newline then ~.) the CLI
//     closes the WebSocket connection cleanly without forwarding the bytes.
//   - The server never sees the escape sequence.
//
// Exit: 0 on clean disconnect, non-zero on connection error.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// consoleEscapeState tracks the raw-mode escape-sequence detector.
// The default escape sequence is "~." typed after a newline.
// State machine:
//
//	idle         → newline  → seenNewline
//	seenNewline  → '~'      → seenTilde
//	seenTilde    → '.'      → disconnect
//	(any other byte resets to idle unless it is a newline)
type consoleEscapeState int

const (
	escIdle       consoleEscapeState = iota
	escSeenNewline                   // last char was \r or \n
	escSeenTilde                     // last two chars were newline then ~
)

// consoleExitMsg mirrors the server's consoleExitMsg for JSON parsing.
type consoleExitMsg struct {
	Type  string `json:"type"`
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
}

func newConsoleCmd() *cobra.Command {
	var (
		nodeFlag   string
		modeIPMI   bool
		modeSSH    bool
		escapeChar string
	)

	cmd := &cobra.Command{
		Use:   "console",
		Short: "Open a terminal console to a node (IPMI SOL or SSH PTY)",
		Long: `console opens a brokered terminal session to a single node.

The server manages the upstream connection (IPMI SOL via ipmitool, or SSH PTY)
and proxies the terminal bidirectionally to the operator's terminal.

Upstream mode:
  --ipmi-sol   IPMI Serial Over LAN (requires BMC configuration on the node)
  --ssh        SSH PTY (requires a node IP and the server's node management key)

When neither flag is specified, the server auto-selects: IPMI SOL if the node
has BMC credentials configured, SSH otherwise.

Escape character (-e / --escape, default "~."):
  Type <Enter>~. to disconnect cleanly. The escape sequence is consumed locally
  and never forwarded to the upstream session.

Examples:
  clustr console -n node01
  clustr console -n node01 --ssh
  clustr console -n node01 --ipmi-sol -e "~."`,

		RunE: func(cmd *cobra.Command, args []string) error {
			if nodeFlag == "" {
				return fmt.Errorf("console requires exactly one target: use -n NODE")
			}
			if modeIPMI && modeSSH {
				return fmt.Errorf("--ipmi-sol and --ssh are mutually exclusive")
			}
			if escapeChar == "" {
				escapeChar = "~."
			}

			mode := ""
			switch {
			case modeIPMI:
				mode = "ipmi-sol"
			case modeSSH:
				mode = "ssh"
			}

			return runConsole(nodeFlag, mode, escapeChar)
		},
	}

	cmd.Flags().StringVarP(&nodeFlag, "node", "n", "", "Target node by ID or hostname (required; exactly one node)")
	cmd.Flags().BoolVar(&modeIPMI, "ipmi-sol", false, "Use IPMI Serial Over LAN (requires BMC config)")
	cmd.Flags().BoolVar(&modeSSH, "ssh", false, "Use SSH PTY (requires node IP and server management key)")
	cmd.Flags().StringVarP(&escapeChar, "escape", "e", "~.", `Disconnect escape sequence (default "~.", typed after a newline)`)

	_ = cmd.MarkFlagRequired("node")

	return cmd
}

// runConsole connects to the server's console WebSocket endpoint and relays
// the terminal bidirectionally until the session ends or the operator disconnects.
func runConsole(nodeID, mode, escSeq string) error {
	c := clientFromFlags()

	// Build WebSocket URL from the server base URL.
	wsURL, err := buildConsoleWSURL(c.BaseURL, nodeID, mode)
	if err != nil {
		return fmt.Errorf("console: build URL: %w", err)
	}

	// Build WebSocket header (auth token).
	headers := http.Header{}
	if c.AuthToken != "" {
		headers.Set("Authorization", "Bearer "+c.AuthToken)
	}

	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second

	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("console: connect to %s: HTTP %d", wsURL, resp.StatusCode)
		}
		return fmt.Errorf("console: connect to %s: %w", wsURL, err)
	}
	defer conn.Close()

	fmt.Fprintf(os.Stderr, "[clustr] console connected  (escape: %s)\r\n", escSeq)

	// Put the local terminal in raw mode so all keystrokes go through unmodified.
	var oldState *term.State
	if term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return fmt.Errorf("console: set raw terminal: %w", err)
		}
		defer func() {
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
		}()
	}

	// Handle OS signals (SIGINT, SIGTERM) to restore terminal before exit.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		if oldState != nil {
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
		}
		os.Exit(1)
	}()

	// exitCh carries the server's exit message (or a local error).
	type sessionResult struct {
		exitCode int
		errMsg   string
	}
	resultCh := make(chan sessionResult, 1)

	// Server → stdout relay.
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				resultCh <- sessionResult{exitCode: -1, errMsg: err.Error()}
				return
			}
			// Try to decode a JSON exit message.
			if len(data) > 0 && data[0] == '{' {
				var exit consoleExitMsg
				if json.Unmarshal(data, &exit) == nil && exit.Type == "exit" {
					resultCh <- sessionResult{exitCode: exit.Code, errMsg: exit.Error}
					return
				}
			}
			// Raw terminal output — write directly to stdout.
			_, _ = os.Stdout.Write(data)
		}
	}()

	// Stdin → WebSocket relay with escape-sequence detection.
	go func() {
		buf := make([]byte, 256)
		state := escSeenNewline // treat start-of-session as after a newline

		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				resultCh <- sessionResult{exitCode: -1, errMsg: err.Error()}
				return
			}
			if n == 0 {
				continue
			}

			// Check for escape sequence byte-by-byte.
			// We send bytes up to (but not including) the escape sequence.
			// On detection we close and return.
			sendStart := 0
			for i := 0; i < n; i++ {
				b := buf[i]
				newState, disconnect := advanceEscapeState(state, b, escSeq)
				state = newState
				if disconnect {
					// Send any bytes before the escape sequence.
					if i > 0 {
						if sendStart < i {
							_ = conn.WriteMessage(websocket.TextMessage, buf[sendStart:i])
						}
					}
					fmt.Fprintf(os.Stderr, "\r\n[clustr] disconnected by escape sequence\r\n")
					resultCh <- sessionResult{exitCode: 0}
					return
				}
				_ = sendStart // suppress lint warning; used below
			}

			// No escape sequence triggered — send the whole chunk.
			if err := conn.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
				resultCh <- sessionResult{exitCode: -1, errMsg: err.Error()}
				return
			}
		}
	}()

	res := <-resultCh

	// Restore terminal before printing anything.
	if oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
		oldState = nil
	}
	signal.Stop(sigCh)

	if res.errMsg != "" && !isNormalWSClose(res.errMsg) {
		fmt.Fprintf(os.Stderr, "[clustr] console error: %s\n", res.errMsg)
		if res.exitCode != 0 {
			return fmt.Errorf("console: exit code %d", res.exitCode)
		}
	}

	if res.exitCode != 0 {
		return fmt.Errorf("console: remote exit code %d", res.exitCode)
	}
	return nil
}

// advanceEscapeState advances the escape-sequence state machine for one byte.
// Returns the new state and whether the disconnect sequence was completed.
//
// The default escape sequence "~." is detected after a newline:
//
//	<newline> ~ .  →  disconnect
//
// Any byte that does not continue the sequence resets to idle (or seenNewline
// if the byte itself is a newline).
func advanceEscapeState(state consoleEscapeState, b byte, escSeq string) (consoleEscapeState, bool) {
	if len(escSeq) < 2 {
		// No valid escape sequence configured.
		return escIdle, false
	}
	escTrigger := escSeq[0] // e.g. '~'
	escEnd := escSeq[1]     // e.g. '.'

	switch state {
	case escIdle:
		if b == '\r' || b == '\n' {
			return escSeenNewline, false
		}
		return escIdle, false

	case escSeenNewline:
		if b == byte(escTrigger) {
			return escSeenTilde, false
		}
		if b == '\r' || b == '\n' {
			return escSeenNewline, false
		}
		return escIdle, false

	case escSeenTilde:
		if b == byte(escEnd) {
			return escIdle, true // DISCONNECT
		}
		if b == '\r' || b == '\n' {
			return escSeenNewline, false
		}
		return escIdle, false
	}
	return escIdle, false
}

// buildConsoleWSURL converts an HTTP/HTTPS base URL to a ws/wss URL for the
// console endpoint.
func buildConsoleWSURL(baseURL, nodeID, mode string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}

	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}

	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/console/" + nodeID
	if mode != "" {
		q := u.Query()
		q.Set("mode", mode)
		u.RawQuery = q.Encode()
	}

	return u.String(), nil
}

// isNormalWSClose returns true when errMsg is a gorilla/websocket close-frame
// error that indicates a clean server-initiated close.
func isNormalWSClose(errMsg string) bool {
	return strings.Contains(errMsg, "websocket: close") &&
		strings.Contains(errMsg, "1000")
}
