package handlers

// events_log.go — Sprint 42 Day 4 EVENT-LOG-JSONL
//
// GET /api/v1/admin/events
//   Streams the JSONL event log via Server-Sent Events.
//   Query parameters:
//     ?follow=1        — keep the connection open and stream new lines as they arrive
//     ?filter=<glob>   — only emit lines whose action field matches the glob
//                        e.g. ?filter=dangerous_push.*
//
// The handler reads the log file directly (on-host) from EventLogPath.
// When the log file does not exist (e.g. fresh install without any events),
// it returns 200 with an empty SSE stream (or 204 when not following).
//
// Glob matching uses filepath.Match semantics: '*' matches within a segment,
// '?' matches one character. The separator is '.' for action strings.

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EventsLogHandler implements GET /api/v1/admin/events.
type EventsLogHandler struct {
	// EventLogPath is the absolute path to the active JSONL event log.
	EventLogPath string
}

// HandleTail serves the JSONL event log as SSE.
func (h *EventsLogHandler) HandleTail(w http.ResponseWriter, r *http.Request) {
	follow := r.URL.Query().Get("follow") == "1"
	filterGlob := r.URL.Query().Get("filter")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, canFlush := w.(http.Flusher)

	f, err := os.Open(h.EventLogPath) //#nosec G304 -- path is configured by the server operator
	if err != nil {
		if os.IsNotExist(err) {
			// No events yet — write an info comment and return (or stream if follow).
			fmt.Fprintf(w, ": no event log at %s\n\n", h.EventLogPath)
			if canFlush {
				flusher.Flush()
			}
			if !follow {
				return
			}
			// In follow mode: poll until the file appears.
			for {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(2 * time.Second):
					f2, err2 := os.Open(h.EventLogPath) //#nosec G304
					if err2 == nil {
						f = f2
						goto streaming
					}
				}
			}
		}
		http.Error(w, `{"error":"cannot open event log"}`, http.StatusInternalServerError)
		return
	}
	defer f.Close()

streaming:
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if filterGlob != "" && !matchActionGlob(filterGlob, line) {
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", line)
		if canFlush {
			flusher.Flush()
		}
	}

	if !follow {
		return
	}

	// Follow mode: poll for new data every 500 ms.
	for {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if filterGlob != "" && !matchActionGlob(filterGlob, line) {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			if canFlush {
				flusher.Flush()
			}
		}
		if err := scanner.Err(); err != nil && err != io.EOF {
			return
		}
	}
}

// matchActionGlob extracts the "action" field value from a JSONL line and
// tests it against the glob. Uses a cheap string search rather than full JSON
// decode for performance. Falls back to full-line glob when extraction fails.
func matchActionGlob(glob, line string) bool {
	// Fast path: extract action value from "action":"<value>"
	const needle = `"action":"`
	idx := strings.Index(line, needle)
	if idx >= 0 {
		rest := line[idx+len(needle):]
		end := strings.IndexByte(rest, '"')
		if end >= 0 {
			action := rest[:end]
			matched, _ := filepath.Match(glob, action)
			return matched
		}
	}
	// Fallback: glob against the whole line.
	matched, _ := filepath.Match(glob, line)
	return matched
}
