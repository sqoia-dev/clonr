package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// nodeRateLimiter enforces a per-node request rate limit using a token-bucket
// approximation: track the last request time and reject if the interval is
// shorter than minInterval.
type nodeRateLimiter struct {
	mu          sync.Mutex
	last        map[string]time.Time // key: node MAC/ID → time of last accepted request
	minInterval time.Duration        // minimum gap between accepted requests per node
}

func newNodeRateLimiter(maxPerSecond int) *nodeRateLimiter {
	interval := time.Second
	if maxPerSecond > 0 {
		interval = time.Second / time.Duration(maxPerSecond)
	}
	return &nodeRateLimiter{
		last:        make(map[string]time.Time),
		minInterval: interval,
	}
}

// Allow returns true when the node identified by key is within the rate limit.
// Uses a sliding-window approximation: one token per minInterval.
func (r *nodeRateLimiter) Allow(key string) bool {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, ok := r.last[key]; ok && now.Sub(last) < r.minInterval {
		return false
	}
	r.last[key] = now
	return true
}

// LogBroker is the interface the handler needs from the broker — keeps the
// handler package free of a concrete import cycle.
type LogBroker interface {
	Subscribe(filter api.LogFilter) (id string, ch <-chan api.LogEntry, cancel func())
	Publish(entries []api.LogEntry)
}

// LogsHubIface exposes the hub methods the logs handler needs for node-journal
// subscriber tracking. Declared here to avoid an import cycle.
type LogsHubIface interface {
	IncrementLogSubscribers(nodeID string) int
	DecrementLogSubscribers(nodeID string) int
	JournalSnapshot(nodeID string) []api.LogEntry
}

// LogsHandler handles all /api/v1/logs routes.
type LogsHandler struct {
	DB     *db.DB
	Broker LogBroker
	// Hub is optional. When set, StreamLogs tracks node-journal SSE subscribers
	// and drives log_pull_start / log_pull_stop on the connected node.
	Hub LogsHubIface
	// ServerCtx is a server-lifetime context used for DB writes so that a
	// client disconnect (r.Context() cancellation) does not abort an in-flight
	// SQLite transaction and silently drop a log batch.
	ServerCtx context.Context

	// ingestLimiter enforces a per-node rate limit on POST /api/v1/logs.
	// Lazily initialized on first use (100 req/s default).
	ingestLimiter     *nodeRateLimiter
	ingestLimiterOnce sync.Once
}

// getIngestLimiter returns the singleton rate limiter, initializing it once.
func (h *LogsHandler) getIngestLimiter() *nodeRateLimiter {
	h.ingestLimiterOnce.Do(func() {
		h.ingestLimiter = newNodeRateLimiter(100) // 100 req/s per node
	})
	return h.ingestLimiter
}

// IngestLogs handles POST /api/v1/logs
// Accepts a JSON array of LogEntry objects and persists them.
func (h *LogsHandler) IngestLogs(w http.ResponseWriter, r *http.Request) {
	const maxLogsBodyBytes = 5 << 20 // 5 MiB
	r.Body = http.MaxBytesReader(w, r.Body, maxLogsBodyBytes)

	var entries []api.LogEntry
	if err := json.NewDecoder(r.Body).Decode(&entries); err != nil {
		if err.Error() == "http: request body too large" {
			http.Error(w, "request body too large (max 5MB)", http.StatusRequestEntityTooLarge)
			return
		}
		writeValidationError(w, "invalid JSON body: expected array of log entries")
		return
	}
	if len(entries) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(entries) > 500 {
		writeValidationError(w, "batch too large: max 500 entries per request")
		return
	}

	// Validate required fields.
	for i, e := range entries {
		if e.ID == "" {
			writeValidationError(w, fmt.Sprintf("entry[%d]: id is required", i))
			return
		}
		if e.NodeMAC == "" {
			writeValidationError(w, fmt.Sprintf("entry[%d]: node_mac is required", i))
			return
		}
		if e.Level == "" {
			writeValidationError(w, fmt.Sprintf("entry[%d]: level is required", i))
			return
		}
		if e.Message == "" {
			writeValidationError(w, fmt.Sprintf("entry[%d]: message is required", i))
			return
		}
		if e.Timestamp.IsZero() {
			entries[i].Timestamp = time.Now().UTC()
		}
	}

	// Per-node rate limiting: reject if more than 100 req/s from the same node.
	// Keyed on the first entry's NodeMAC (validated above, always non-empty).
	limiter := h.getIngestLimiter()
	if !limiter.Allow(entries[0].NodeMAC) {
		http.Error(w, "rate limit exceeded: max 100 requests/second per node", http.StatusTooManyRequests)
		return
	}

	// Use the server-lifetime context (not r.Context()) for the DB write so
	// that a client disconnect mid-request does not cancel the SQLite
	// transaction and silently drop the batch. Bound it to 30s so a real
	// deadlock still surfaces.
	writeCtx := h.ServerCtx
	if writeCtx == nil {
		writeCtx = r.Context()
	}
	writeCtx, cancel := context.WithTimeout(writeCtx, 30*time.Second)
	defer cancel()

	if err := h.DB.InsertLogBatch(writeCtx, entries); err != nil {
		log.Error().Err(err).Msg("ingest logs")
		writeError(w, err)
		return
	}

	// Publish to SSE subscribers after persisting — best-effort.
	h.Broker.Publish(entries)

	w.WriteHeader(http.StatusCreated)
}

// QueryLogs handles GET /api/v1/logs
// Query params: mac, hostname, level, component, since, limit
func (h *LogsHandler) QueryLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := api.LogFilter{
		NodeMAC:   q.Get("mac"),
		Hostname:  q.Get("hostname"),
		Level:     q.Get("level"),
		Component: q.Get("component"),
	}

	if sinceStr := q.Get("since"); sinceStr != "" {
		t, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			writeValidationError(w, "invalid 'since' param: must be RFC3339 (e.g. 2024-01-01T00:00:00Z)")
			return
		}
		filter.Since = &t
	}

	if limitStr := q.Get("limit"); limitStr != "" {
		n, err := strconv.Atoi(limitStr)
		if err != nil || n <= 0 {
			writeValidationError(w, "invalid 'limit' param: must be a positive integer")
			return
		}
		filter.Limit = n
	}

	entries, err := h.DB.QueryLogs(r.Context(), filter)
	if err != nil {
		log.Error().Err(err).Msg("query logs")
		writeError(w, err)
		return
	}
	if entries == nil {
		entries = []api.LogEntry{}
	}
	writeJSON(w, http.StatusOK, api.ListLogsResponse{Logs: entries, Total: len(entries)})
}

// StreamLogs handles GET /api/v1/logs/stream
// Streams new log entries as Server-Sent Events.
// Optional query params: mac, hostname, level, component
//
// When component=node-journal and mac= are both set, this handler tracks the
// SSE subscriber count in the hub so the node is told to start/stop journal streaming.
func (h *LogsHandler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	// Verify the client supports SSE.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by this server", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()
	filter := api.LogFilter{
		NodeMAC:   q.Get("mac"),
		Hostname:  q.Get("hostname"),
		Level:     q.Get("level"),
		Component: q.Get("component"),
	}

	// If this is a node-journal stream with a MAC filter, track the subscription
	// in the hub so the node knows to start/stop journal streaming.
	var nodeID string
	var replayEntries []api.LogEntry
	if filter.Component == "node-journal" && filter.NodeMAC != "" && h.Hub != nil {
		// Resolve MAC → nodeID via the DB.
		if node, err := h.DB.GetNodeConfigByMAC(r.Context(), filter.NodeMAC); err == nil {
			nodeID = node.ID
			// Collect buffered entries for immediate replay before live stream begins.
			replayEntries = h.Hub.JournalSnapshot(nodeID)
			h.Hub.IncrementLogSubscribers(nodeID)
			defer h.Hub.DecrementLogSubscribers(nodeID)
		} else {
			log.Debug().Err(err).Str("mac", filter.NodeMAC).
				Msg("stream logs: could not resolve MAC to node ID — hub tracking skipped")
		}
	}

	_, ch, cancel := h.Broker.Subscribe(filter)
	defer cancel()

	// SSE headers — must be set before first write.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering if present
	w.WriteHeader(http.StatusOK)

	// Send a comment to establish the stream before any data arrives.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Replay buffered journal entries so the Logs tab shows recent output immediately.
	for _, entry := range replayEntries {
		data, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	if len(replayEntries) > 0 {
		flusher.Flush()
	}

	ctx := r.Context()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Client disconnected — cancel() in defer handles cleanup.
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case entry, open := <-ch:
			if !open {
				return
			}
			data, err := json.Marshal(entry)
			if err != nil {
				continue // skip unserializable entries
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
