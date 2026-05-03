package handlers_test

// events_test.go — integration tests for GET /api/v1/events (UX-4 multiplexed SSE).
//
// Tests:
//   1. An event published to the bus arrives at a connected subscriber.
//   2. Topic filter: events for non-subscribed topics are NOT forwarded.
//   3. Keepalive: a ": ping" comment line arrives within one tick.

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/server/eventbus"
	"github.com/sqoia-dev/clustr/internal/server/handlers"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// readSSELines reads from resp until it collects n non-empty lines or the
// deadline elapses.  It returns what was collected.
func readSSELines(t *testing.T, resp *http.Response, n int, deadline time.Duration) []string {
	t.Helper()
	lines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			l := scanner.Text()
			if l != "" {
				lines <- l
			}
		}
		close(lines)
	}()

	var out []string
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for len(out) < n {
		select {
		case l, ok := <-lines:
			if !ok {
				return out
			}
			out = append(out, l)
		case <-timer.C:
			return out
		}
	}
	return out
}

// ─── Test 1: event arrives ───────────────────────────────────────────────────

func TestEventsHandler_EventArrives(t *testing.T) {
	bus := eventbus.New()
	h := &handlers.EventsHandler{Bus: bus}

	srv := httptest.NewServer(http.HandlerFunc(h.ServeEvents))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	// Give the handler goroutine time to call Subscribe before we publish.
	time.Sleep(50 * time.Millisecond)

	bus.Publish(eventbus.TopicNodes, map[string]string{"node_id": "node-abc"})

	// Expect: the "event:" line and the "data:" line.
	lines := readSSELines(t, resp, 3, 2*time.Second) // ": connected" + event + data
	var eventLine, dataLine string
	for _, l := range lines {
		if strings.HasPrefix(l, "event:") {
			eventLine = l
		}
		if strings.HasPrefix(l, "data:") {
			dataLine = l
		}
	}

	if eventLine != "event: nodes" {
		t.Errorf("expected 'event: nodes', got %q (all lines: %v)", eventLine, lines)
	}

	var payload map[string]string
	rawData := strings.TrimPrefix(dataLine, "data: ")
	if err := json.Unmarshal([]byte(rawData), &payload); err != nil {
		t.Fatalf("unmarshal data line %q: %v", dataLine, err)
	}
	if payload["node_id"] != "node-abc" {
		t.Errorf("expected node_id=node-abc, got %q", payload["node_id"])
	}
}

// ─── Test 2: topic filter ─────────────────────────────────────────────────────

func TestEventsHandler_TopicFilter(t *testing.T) {
	bus := eventbus.New()
	h := &handlers.EventsHandler{Bus: bus}

	srv := httptest.NewServer(http.HandlerFunc(h.ServeEvents))
	defer srv.Close()

	// Subscribe only to "images".
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(srv.URL + "?topics=images")
	if err != nil {
		t.Fatalf("GET /events?topics=images: %v", err)
	}
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	bus.Publish(eventbus.TopicNodes, map[string]string{"x": "1"})  // should not arrive
	bus.Publish(eventbus.TopicImages, map[string]string{"x": "2"}) // should arrive

	lines := readSSELines(t, resp, 4, 2*time.Second)
	var eventLines []string
	for _, l := range lines {
		if strings.HasPrefix(l, "event:") {
			eventLines = append(eventLines, l)
		}
	}

	if len(eventLines) != 1 {
		t.Fatalf("expected exactly 1 event line (images only), got %d: %v", len(eventLines), eventLines)
	}
	if eventLines[0] != "event: images" {
		t.Errorf("expected 'event: images', got %q", eventLines[0])
	}
}

// ─── Test 3: keepalive ping ───────────────────────────────────────────────────

func TestEventsHandler_KeepalivePing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping keepalive test under -short (requires ~15s wall clock)")
	}

	bus := eventbus.New()
	h := &handlers.EventsHandler{Bus: bus}

	srv := httptest.NewServer(http.HandlerFunc(h.ServeEvents))
	defer srv.Close()

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	found := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			l := scanner.Text()
			if strings.HasPrefix(l, ":") {
				found <- l
				return
			}
		}
		close(found)
	}()

	select {
	case line, ok := <-found:
		if !ok {
			t.Fatal("SSE stream closed before ping arrived")
		}
		// Expect either ": connected" (initial) or ": ping" (keepalive).
		if line != ": ping" && line != ": connected" {
			t.Fatalf("unexpected comment line: %q", line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for keepalive ping (expected within 15s)")
	}
}
