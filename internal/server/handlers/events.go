package handlers

// events.go — GET /api/v1/events
//
// Multiplexed SSE endpoint that fans out all internal event-bus topics over a
// single persistent connection per browser tab.  Each client opens exactly one
// connection; the server fans matching events from the Bus to that connection.
//
// Query param:
//
//	?topics=nodes,images   — comma-separated list of topics to subscribe to.
//	                         Omit (or empty) to receive all topics.
//
// SSE event format:
//
//	event: nodes.changed
//	data: {"node_id":"...","...":"..."}
//
//	event: ping
//	data: {}
//
// Keepalive: a ": ping\n\n" SSE comment is sent every 15 s so proxies do not
// close idle connections (mirrors the existing per-page SSE endpoints).
//
// Deprecated legacy endpoints (/images/events, /logs/stream, etc.) continue to
// work and also publish to this bus; they are candidates for removal in Sprint 27+.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sqoia-dev/clustr/internal/server/eventbus"
)

// EventsBusIface is the subset of *eventbus.Bus required by EventsHandler.
// Declared as an interface so tests can inject a fake.
type EventsBusIface interface {
	Subscribe(topics []eventbus.Topic) (<-chan eventbus.Event, func())
}

// EventsHandler serves GET /api/v1/events — the multiplexed SSE stream.
type EventsHandler struct {
	Bus EventsBusIface
}

// ServeEvents handles GET /api/v1/events.
func (h *EventsHandler) ServeEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by this server", http.StatusInternalServerError)
		return
	}

	// Parse optional ?topics= filter.
	var wantTopics []eventbus.Topic
	if raw := strings.TrimSpace(r.URL.Query().Get("topics")); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				wantTopics = append(wantTopics, eventbus.Topic(t))
			}
		}
	}

	ch, cancel := h.Bus.Subscribe(wantTopics)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Establish the stream immediately so the browser knows it's connected.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			// SSE event name is the topic with "." replaced by the conventional
			// "<resource>.<verb>" form. The bus already uses this convention so the
			// topic IS the event name.
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Topic, ev.Data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
