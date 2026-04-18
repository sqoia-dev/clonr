// logs.go — SSE handler that streams the clonr-slapd.service systemd journal.
// Transport: Server-Sent Events, mirroring the existing /api/v1/logs/stream handler.
package ldap

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// defaultJournalLines is how many historical lines to send for a non-follow request.
	defaultJournalLines = 200
	// slapdUnit is the systemd unit whose journal is streamed.
	slapdUnit = "clonr-slapd.service"
)

// handleLogs handles GET /api/v1/ldap/logs
// Returns the last N lines of the clonr-slapd.service journal as newline-delimited
// JSON objects. Query params: lines (default 200).
func (m *Manager) handleLogs(w http.ResponseWriter, r *http.Request) {
	if err := m.requireEnabled(r.Context()); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}

	n := defaultJournalLines
	if s := r.URL.Query().Get("lines"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			n = v
		}
	}

	// journalctl -u clonr-slapd.service -o short-iso --no-pager -n N
	cmd := exec.CommandContext(r.Context(),
		"journalctl",
		"-u", slapdUnit,
		"-o", "short-iso",
		"--no-pager",
		"-n", strconv.Itoa(n),
	)
	out, err := cmd.Output()
	if err != nil {
		// journalctl exits non-zero when the unit has no journal entries yet.
		// Still return an empty-lines response rather than an error.
		log.Warn().Err(err).Msg("ldap logs: journalctl exited non-zero (unit may have no entries)")
		out = []byte{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// Emit as a JSON array of line strings for easy client consumption.
	fmt.Fprint(w, "[")
	first := true
	now := time.Now().UTC()
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if !first {
			fmt.Fprint(w, ",")
		}
		first = false
		fmt.Fprintf(w, `{"line":%s,"timestamp":%s}`,
			jsonStringEscape(line),
			jsonStringEscape(now.Format(time.RFC3339)),
		)
	}
	fmt.Fprint(w, "]")
}

// handleLogsStream handles GET /api/v1/ldap/logs/stream
// Streams new journal lines as SSE events using:
//
//	journalctl -u clonr-slapd.service -f -o short-iso --no-pager
//
// Each SSE event carries the raw journal line as {"line":"...","timestamp":"..."}.
func (m *Manager) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	if err := m.requireEnabled(r.Context()); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by this server", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Send a comment to establish the stream before any data arrives.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()

	// journalctl -f follows the journal and blocks until the context is cancelled.
	cmd := exec.CommandContext(ctx,
		"journalctl",
		"-u", slapdUnit,
		"-f",
		"-o", "short-iso",
		"--no-pager",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Error().Err(err).Msg("ldap logs stream: stdout pipe")
		fmt.Fprintf(w, "data: {\"error\":\"failed to start journalctl\"}\n\n")
		flusher.Flush()
		return
	}

	if err := cmd.Start(); err != nil {
		log.Error().Err(err).Msg("ldap logs stream: journalctl start")
		fmt.Fprintf(w, "data: {\"error\":\"failed to start journalctl\"}\n\n")
		flusher.Flush()
		return
	}

	// Ensure the child process is cleaned up when the client disconnects.
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := sc.Text()
		if line == "" {
			continue
		}
		now := time.Now().UTC().Format(time.RFC3339)
		payload := fmt.Sprintf(`{"line":%s,"timestamp":%s}`,
			jsonStringEscape(line),
			jsonStringEscape(now),
		)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
}

// requireEnabled returns an error (suitable for a 409 response) if the LDAP
// module is not enabled. Mirrors the "module is not ready" convention used by
// the DIT-backed endpoints.
func (m *Manager) requireEnabled(ctx context.Context) error {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("ldap module is not enabled")
		}
		return fmt.Errorf("ldap: read module config: %w", err)
	}
	if !row.Enabled {
		return fmt.Errorf("ldap module is not enabled")
	}
	return nil
}

// jsonStringEscape returns a JSON-encoded string (with surrounding quotes).
// Handles the common ASCII printable range and escapes control characters.
func jsonStringEscape(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if c < 0x20 {
				out = append(out, []byte(fmt.Sprintf(`\u%04x`, c))...)
			} else {
				out = append(out, c)
			}
		}
	}
	out = append(out, '"')
	return string(out)
}

