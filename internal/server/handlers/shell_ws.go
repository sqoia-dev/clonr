package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/creack/pty"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// shellDepError is the structured JSON payload sent to the browser when a
// required server-side dependency (e.g. systemd-nspawn) is missing.
// The browser xterm component matches on Code == "shell_dependency_missing"
// and renders an inline error panel instead of the raw terminal.
type shellDepError struct {
	Code        string   `json:"code"`
	Missing     []string `json:"missing"`
	Remediation string   `json:"remediation"`
}

// checkShellDeps verifies that all binaries required to run an image shell
// session are present on PATH. Returns a non-nil shellDepError if any are
// missing, ready to be serialised and sent to the client.
func checkShellDeps() *shellDepError {
	missing := []string{}
	if _, err := exec.LookPath("systemd-nspawn"); err != nil {
		missing = append(missing, "systemd-nspawn")
	}
	if len(missing) == 0 {
		return nil
	}
	return &shellDepError{
		Code:        "shell_dependency_missing",
		Missing:     missing,
		Remediation: "On the server: `sudo dnf install systemd-container` (RHEL/Rocky/AlmaLinux)",
	}
}

// systemdRunAvailable returns true if systemd-run(1) is present on PATH.
// Used to decide whether to wrap nspawn in a systemd-run --scope.
var systemdRunAvailable = func() bool {
	_, err := exec.LookPath("systemd-run")
	return err == nil
}()

// procStatusPath is the path to /proc/self/status. It is a variable so tests
// can point it at a temp file to simulate different kernel environments without
// requiring root or real /proc manipulation.
var procStatusPath = "/proc/self/status"

// isNoNewPrivilegesActive reads /proc/self/status (or procStatusPath in tests)
// and returns true when the kernel has set NoNewPrivs:	1 on the current process.
// This means the process — and any children — cannot gain new privileges via
// setuid, capabilities, or similar mechanisms, and systemd-run --scope cannot
// escape the parent cgroup's restrictions.
func isNoNewPrivilegesActive() bool {
	f, err := os.Open(procStatusPath)
	if err != nil {
		// Cannot determine state; conservatively report false so the caller can
		// decide whether to proceed or fail.
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "NoNewPrivs:") {
			// Format: "NoNewPrivs:\t1" or "NoNewPrivs:\t0"
			fields := strings.Fields(line)
			return len(fields) >= 2 && fields[1] == "1"
		}
	}
	return false
}

// wrapNspawnInScope wraps a systemd-nspawn invocation in a systemd-run
// --scope --slice=clustr-shells.slice so the nspawn process runs outside the
// clustr-serverd cgroup and is not subject to its NoNewPrivileges=true or
// CapabilityBoundingSet restrictions.  Without this wrapping, nspawn fails
// with "Failed to move root directory: Operation not permitted" because it
// cannot call pivot_root(2) without CAP_SYS_ADMIN.
//
// Falls back to a direct systemd-nspawn invocation when systemd-run is not
// available (e.g. inside a Docker container or minimal install).
//
// Fallback guard: when NoNewPrivileges=true is already active on the parent
// process, the direct-nspawn fallback will silently fail — the kernel will not
// allow pivot_root(2) or mount-namespace creation regardless of binary
// capabilities. Rather than produce a misleading runtime error, refuse early
// and return a nil Cmd with an explicit error the caller can surface to the
// operator. The operator must either install systemd-run (so the scope wrapper
// can escape the cgroup) or remove NoNewPrivileges=true from the service unit.
func wrapNspawnInScope(sessionID string, nspawnArgs []string) (*exec.Cmd, error) {
	if !systemdRunAvailable {
		// Fallback path: direct nspawn without scope wrapper.
		// Guard: refuse if the kernel has already set NoNewPrivileges on this process.
		if isNoNewPrivilegesActive() {
			return nil, fmt.Errorf(
				"shell: cannot start nspawn — NoNewPrivileges=true is set on the " +
					"clustr-serverd process and systemd-run is not available; " +
					"install systemd-run (dnf install systemd) so nspawn can be " +
					"wrapped in a scope that escapes the NoNewPrivileges restriction, " +
					"or remove NoNewPrivileges=true from the clustr-serverd service unit",
			)
		}
		return exec.Command("systemd-nspawn", nspawnArgs...), nil
	}
	scopeName := "clustr-shell-" + sessionID + ".scope"
	args := []string{
		"--scope",
		"--slice=clustr-shells.slice",
		"--unit=" + scopeName,
		"--quiet",
		"--",
		"systemd-nspawn",
	}
	args = append(args, nspawnArgs...)
	return exec.Command("systemd-run", args...), nil
}

// invalidateImageSidecar deletes the tar-sha256 sidecar file for imageID so
// that the next blob fetch recomputes the tarball hash from scratch.
//
// This is a temporary hotfix for the shell-session mutation problem: when a
// shell session writes files into the image rootfs (e.g. /root/.bash_history),
// the stored tar-sha256 no longer matches the new tarball content. Deploy agents
// verify the hash and fail with ExitDownload(5) when there is a mismatch.
//
// Proper fix: use an overlayfs-backed shell session so writes never touch the
// base rootfs (ADR-0009 overlayfs model). Until that lands, invalidating the
// sidecar forces a fresh hash computation on next blob fetch, which avoids the
// mismatch at the cost of one extra full-tar pass.
//
// Returns true if the sidecar existed (i.e. the image was marked as mutated).
func invalidateImageSidecar(imageDir, imageID string) bool {
	sidecarPath := filepath.Join(imageDir, imageID, "tar-sha256")
	err := os.Remove(sidecarPath)
	if err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).
			Str("image_id", imageID).
			Str("path", sidecarPath).
			Msg("shell session close: failed to remove tar-sha256 sidecar")
		return false
	}
	existed := !os.IsNotExist(err)
	if existed {
		log.Info().
			Str("image_id", imageID).
			Str("path", sidecarPath).
			Msg("shell session closed — invalidated tar-sha256 sidecar; next blob fetch will recompute")
	}
	return existed
}

// wsUpgrader validates that browser WebSocket connections originate from the
// same host as the server. Non-browser clients (no Origin header) are allowed
// through unconditionally so CLI tools and deploy agents are unaffected.
var wsUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser client (CLI, curl, agent)
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// wsMsg is the JSON envelope used by the browser xterm WebSocket protocol.
// Types:
//
//	"data"   — terminal I/O bytes (base64 encoded for reliable JSON transport)
//	"resize" — terminal resize, carries Cols and Rows
//	"ping"   — keepalive from client (server ignores)
//	"error"  — structured error from server; Data is JSON-encoded error payload
type wsMsg struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"` // raw bytes as string (browser sends UTF-8)
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// marshalShellDepError serialises a shellDepError as a JSON string suitable
// for embedding in a wsMsg.Data field so the browser can decode it.
func marshalShellDepError(e *shellDepError) string {
	b, _ := json.Marshal(e)
	return string(b)
}

// ShellWSHandler handles GET /api/v1/images/:id/shell-session/:sid/ws
//
// Upgrades the HTTP connection to WebSocket, forks a shell inside the image
// using systemd-nspawn (providing UTS, PID, and mount namespace isolation)
// with a PTY attached, then bidirectionally pipes:
//
//	client keystrokes → PTY stdin
//	PTY stdout        → client
//
// The session identified by :sid must already exist (created via
// POST /api/v1/images/:id/shell-session). On WebSocket close, the nspawn
// process is killed and the PTY is released.
func (h *FactoryHandler) ShellWS(w http.ResponseWriter, r *http.Request) {
	imageID := chi.URLParam(r, "id")
	sessionID := chi.URLParam(r, "sid")

	// Resolve the session — must already exist.
	sessions := h.Shells.ListSessions()
	var rootDir string
	for _, s := range sessions {
		if s.ID == sessionID && s.ImageID == imageID {
			rootDir = s.RootDir
			break
		}
	}
	if rootDir == "" {
		http.Error(w, "shell session not found or expired", http.StatusNotFound)
		return
	}

	// AUTH: bearer token may be supplied via Authorization header OR ?token=
	// query param. The query-param fallback exists ONLY because browsers cannot
	// set custom headers on WebSocket upgrade requests; wsTokenLift middleware
	// hoists ?token= into the Authorization header before apiKeyAuth runs, so
	// by the time execution reaches here the request is already authenticated.
	// This is the ONE place in the API where ?token= is accepted. HTTP endpoints
	// reject query-param tokens (see wsTokenLift / extractBearerToken in
	// middleware.go — query fallback is absent from the shared HTTP auth path).
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote the HTTP error response.
		log.Warn().Err(err).Str("session_id", sessionID).Msg("shell ws: upgrade failed")
		return
	}
	defer conn.Close()

	// Extract actor identity now that we have the request in scope.
	var actorID, actorLabel string
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}

	// --- Dependency pre-check (after WebSocket upgrade) ---
	// Upgrading first ensures the structured error JSON reaches the browser
	// over the established WebSocket connection rather than being dropped as a
	// failed HTTP handshake. systemd-nspawn presence is checked at session open
	// time (not at server start) so a dnf install fixes it without a restart.
	if depErr := checkShellDeps(); depErr != nil {
		log.Error().
			Str("session_id", sessionID).
			Str("image_id", imageID).
			Strs("missing", depErr.Missing).
			Msg("shell ws: dependency pre-check failed — closing with structured error")
		// Audit the failure so Activity shows the event.
		if h.Audit != nil {
			h.Audit.Record(r.Context(), actorID, actorLabel,
				db.AuditActionImageShellDepMissing, "image", imageID,
				r.RemoteAddr, nil,
				map[string]any{"missing": depErr.Missing, "session_id": sessionID},
			)
		}
		// Send the structured error as a JSON message so the browser xterm
		// component can intercept it and render an inline error panel.
		_ = conn.WriteJSON(wsMsg{Type: "error", Data: marshalShellDepError(depErr)})
		_ = conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shell_dependency_missing"),
		)
		return
	}

	// RISK-1(a): send mutation warning as the first WebSocket frame so any
	// non-browser client (CLI tools, scripts) also receives the advisory before
	// any user data flows.
	_ = conn.WriteJSON(wsMsg{
		Type: "warning",
		Data: api.ShellMutationWarning,
	})

	log.Info().Str("session_id", sessionID).Str("image_id", imageID).Str("rootdir", rootDir).
		Msg("shell ws: terminal session started")

	// Audit: shell session WebSocket connected.
	if h.Audit != nil {
		h.Audit.Record(r.Context(), actorID, actorLabel,
			db.AuditActionImageShellStart, "image", imageID,
			r.RemoteAddr, nil,
			map[string]any{"session_id": sessionID},
		)
	}

	// Determine which shell binary is available inside the image.
	shell := "/bin/bash"
	if _, statErr := os.Stat(rootDir + shell); statErr != nil {
		shell = "/bin/sh"
	}

	// Use systemd-nspawn for proper namespace isolation: UTS (hostname),
	// PID, and mount namespaces are all handled automatically. This prevents
	// the shell from inheriting the management server's hostname and avoids
	// the need to manually bind-mount /proc, /sys, /dev, etc.
	//
	// Wrap in systemd-run --scope --slice=clustr-shells.slice so the nspawn
	// process runs outside the clustr-serverd cgroup and is not subject to its
	// NoNewPrivileges=true restriction. Without this, pivot_root(2) fails with
	// "Operation not permitted" because CAP_SYS_ADMIN cannot be used through
	// a NoNewPrivileges boundary.
	nspawnArgs := []string{
		"--quiet",
		"-D", rootDir,
		"--",
		shell, "--login",
	}
	cmd, err := wrapNspawnInScope(sessionID, nspawnArgs)
	if err != nil {
		writeWSError(conn, err.Error())
		log.Error().Err(err).Str("session_id", sessionID).Msg("shell ws: nspawn scope setup failed")
		return
	}
	cmd.Env = []string{
		"TERM=xterm-256color",
		"HOME=/root",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"SHELL=" + shell,
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		writeWSError(conn, fmt.Sprintf("failed to start shell: %v", err))
		log.Error().Err(err).Str("session_id", sessionID).Msg("shell ws: pty start failed")
		return
	}
	defer func() {
		ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		log.Info().Str("session_id", sessionID).Str("image_id", imageID).
			Msg("shell ws: terminal session ended")
		// Invalidate the tar-sha256 sidecar so the next blob fetch recomputes
		// the tarball hash. The shell session may have written files into the
		// rootfs (e.g. /root/.bash_history) that would cause a hash mismatch
		// and fail the deploy agent's integrity check (ExitDownload 5).
		//
		mutated := invalidateImageSidecar(h.ImageDir, imageID)
		// Audit: shell session closed. RISK-1(a) — record whether mutation occurred.
		if h.Audit != nil {
			h.Audit.Record(r.Context(), actorID, actorLabel,
				db.AuditActionImageShellClose, "image", imageID,
				r.RemoteAddr, nil,
				map[string]any{"session_id": sessionID, "mutated": mutated},
			)
		}
	}()

	// PTY → WebSocket: stream shell output to browser.
	ptyClosed := make(chan struct{})
	go func() {
		defer close(ptyClosed)
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				msg := wsMsg{Type: "data", Data: string(buf[:n])}
				if writeErr := conn.WriteJSON(msg); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				// PTY closed — shell exited.
				return
			}
		}
	}()

	// WebSocket → PTY: relay client keystrokes and resize events.
	for {
		var msg wsMsg
		if err := conn.ReadJSON(&msg); err != nil {
			// Client disconnected.
			break
		}

		switch msg.Type {
		case "data":
			if _, writeErr := io.WriteString(ptmx, msg.Data); writeErr != nil {
				log.Debug().Err(writeErr).Str("session_id", sessionID).Msg("shell ws: pty write error")
				return
			}
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: msg.Cols, Rows: msg.Rows})
			}
		}

		// If the PTY closed while we were reading, stop.
		select {
		case <-ptyClosed:
			writeWSError(conn, "shell exited")
			return
		default:
		}
	}
}

// writeWSError sends an error message to the WebSocket client as terminal output.
func writeWSError(conn *websocket.Conn, msg string) {
	_ = conn.WriteJSON(wsMsg{Type: "data", Data: "\r\n\033[31m[clustr] " + msg + "\033[0m\r\n"})
}

// ActiveDeploys handles GET /api/v1/images/:id/active-deploys
// Scans recent deploy log entries (last 30 minutes, component=deploy) for any
// that reference this image ID. Returns a count and isActive flag so the
// browser shell modal can show a warning when opening a shell on a live image.
func (h *FactoryHandler) ActiveDeploys(w http.ResponseWriter, r *http.Request) {
	imageID := chi.URLParam(r, "id")

	since := time.Now().Add(-30 * time.Minute)
	entries, err := h.DB.QueryLogs(r.Context(), api.LogFilter{
		Component: "deploy",
		Since:     &since,
		Limit:     500,
	})
	if err != nil {
		writeError(w, err)
		return
	}

	// Count entries that mention this image ID in any field.
	activeCount := 0
	for _, e := range entries {
		// Check if any log field contains the image ID.
		found := false
		for _, v := range e.Fields {
			if s, ok := v.(string); ok && s == imageID {
				found = true
				break
			}
		}
		if found {
			activeCount++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"image_id":     imageID,
		"active_count": activeCount,
		"is_active":    activeCount > 0,
	})
}
