package handlers

// exec_test.go — integration tests for POST /api/v1/exec (#126)
//
// These tests use a fake ExecHubIface and ExecDBIface to verify:
//   - All 5 output formats produce expected SSE output
//   - Selector validation (empty selector → 400)
//   - Node-not-connected path produces error event
//   - Summary event is always the last SSE event
//   - consolidate format groups nodes with identical output
//   - max_exit_code in summary reflects per-node codes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/selector"
)

// ─── fakes ───────────────────────────────────────────────────────────────────

// execTestDB satisfies ExecDBIface with a fixed node list.
type execTestDB struct {
	nodes []selector.SelectorNode
}

func (f *execTestDB) ListAllNodes(_ context.Context) ([]selector.SelectorNode, error) {
	return f.nodes, nil
}

func (f *execTestDB) ListGroupMemberIDs(_ context.Context, groupName string) ([]selector.NodeID, error) {
	return nil, fmt.Errorf("group %q not found", groupName)
}

func (f *execTestDB) ListNodeIDsByRackNames(_ context.Context, _ []string) ([]selector.NodeID, error) {
	return nil, nil
}

// execTestHub satisfies ExecHubIface.
// Send() immediately delivers the pre-configured result to the registered channel.
type execTestHub struct {
	// nodeResults maps nodeID → result payload. Missing = disconnected.
	nodeResults map[string]clientd.OperatorExecResultPayload
	// disconnected lists node IDs that are offline.
	disconnected map[string]bool

	// mu guards pending against concurrent access from parallel node goroutines.
	mu sync.Mutex
	// pending channels registered by RegisterOperatorExec, keyed by msgID.
	pending map[string]chan clientd.OperatorExecResultPayload
}

func newExecTestHub(results map[string]clientd.OperatorExecResultPayload, disconnected ...string) *execTestHub {
	dc := make(map[string]bool, len(disconnected))
	for _, id := range disconnected {
		dc[id] = true
	}
	if results == nil {
		results = map[string]clientd.OperatorExecResultPayload{}
	}
	return &execTestHub{
		nodeResults:  results,
		disconnected: dc,
		pending:      make(map[string]chan clientd.OperatorExecResultPayload),
	}
}

func (h *execTestHub) IsConnected(nodeID string) bool {
	return !h.disconnected[nodeID]
}

// Send is called by HandleExec after RegisterOperatorExec.
// We extract the nodeID to find the registered channel, then deliver the result.
func (h *execTestHub) Send(nodeID string, msg clientd.ServerMessage) error {
	// Unmarshal the payload to get the RefMsgID which equals the msgID used
	// to register the channel.
	var pl clientd.OperatorExecRequestPayload
	_ = json.Unmarshal(msg.Payload, &pl)
	msgID := pl.RefMsgID
	if msgID == "" {
		msgID = msg.MsgID
	}

	// Deliver the result to the registered channel (if any).
	h.mu.Lock()
	ch, ok := h.pending[msgID]
	h.mu.Unlock()
	if ok {
		result := h.nodeResults[nodeID]
		result.RefMsgID = msgID
		ch <- result
	}
	return nil
}

func (h *execTestHub) RegisterOperatorExec(msgID string) <-chan clientd.OperatorExecResultPayload {
	ch := make(chan clientd.OperatorExecResultPayload, 1)
	h.mu.Lock()
	h.pending[msgID] = ch
	h.mu.Unlock()
	return ch
}

func (h *execTestHub) UnregisterOperatorExec(msgID string) {
	h.mu.Lock()
	delete(h.pending, msgID)
	h.mu.Unlock()
}

// Compile-time interface check.
var _ ExecHubIface = (*execTestHub)(nil)

// ─── helpers ─────────────────────────────────────────────────────────────────

// collectSSEEvents sends a POST to the handler and returns the full payload
// of each SSE event (everything after "data: " in the first line, plus any
// continuation lines before the blank event-separator).
//
// The server writes multi-line event data as:
//
//	data: <first line of payload>\n
//	<continuation lines>\n
//	\n   ← event separator
//
// This function reassembles the full payload per event (without the "data: " prefix).
func collectSSELines(t *testing.T, h *ExecHandler, reqBody map[string]interface{}) []string {
	t.Helper()
	bodyJSON, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", bytes.NewReader(bodyJSON))
	w := httptest.NewRecorder()
	h.HandleExec(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	return parseSSEEvents(w.Body.String())
}

// parseSSEEvents splits a raw SSE body into event payloads.
// Each event is delimited by a blank line. The first line of a data event
// begins with "data: "; continuation lines are appended with "\n".
func parseSSEEvents(body string) []string {
	var events []string
	var current strings.Builder
	inEvent := false

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "data: ") {
			if inEvent {
				current.WriteByte('\n')
			}
			current.WriteString(line[len("data: "):])
			inEvent = true
		} else if line == "" {
			if inEvent {
				events = append(events, current.String())
				current.Reset()
				inEvent = false
			}
		} else if inEvent {
			// Continuation line (part of same event payload).
			current.WriteByte('\n')
			current.WriteString(line)
		}
	}
	if inEvent && current.Len() > 0 {
		events = append(events, current.String())
	}
	return events
}

// parseSummary parses the last SSE event payload as an execSummaryEvent.
func parseSummary(t *testing.T, lines []string) execSummaryEvent {
	t.Helper()
	if len(lines) == 0 {
		t.Fatal("no SSE events received")
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	var s execSummaryEvent
	if err := json.Unmarshal([]byte(last), &s); err != nil {
		t.Fatalf("last SSE event is not valid JSON: %v — event: %s", err, last)
	}
	if s.Type != "summary" {
		t.Fatalf("expected type=summary, got %q", s.Type)
	}
	return s
}

// makeExecNodes creates SelectorNode slice for the DB fake.
func makeExecNodes(ids []string) []selector.SelectorNode {
	var out []selector.SelectorNode
	for _, id := range ids {
		out = append(out, selector.SelectorNode{
			ID:       id,
			Hostname: "host-" + id,
			Active:   true,
		})
	}
	return out
}

func okExecResult(stdout string) clientd.OperatorExecResultPayload {
	return clientd.OperatorExecResultPayload{ExitCode: 0, Stdout: stdout}
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestExec_EmptySelector verifies that an empty selector returns HTTP 400.
func TestExec_EmptySelector(t *testing.T) {
	h := &ExecHandler{
		DB:  &execTestDB{},
		Hub: newExecTestHub(nil),
	}
	body := map[string]interface{}{"command": "hostname"}
	bodyJSON, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", bytes.NewReader(bodyJSON))
	w := httptest.NewRecorder()
	h.HandleExec(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty selector, got %d", w.Code)
	}
}

// TestExec_MissingCommand verifies that a missing command returns HTTP 400.
func TestExec_MissingCommand(t *testing.T) {
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes([]string{"n1"})},
		Hub: newExecTestHub(nil),
	}
	body := map[string]interface{}{"all": true}
	bodyJSON, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", bytes.NewReader(bodyJSON))
	w := httptest.NewRecorder()
	h.HandleExec(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing command, got %d", w.Code)
	}
}

// TestExec_InvalidOutputFormat verifies unknown output_format returns 400.
func TestExec_InvalidOutputFormat(t *testing.T) {
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes([]string{"n1"})},
		Hub: newExecTestHub(nil),
	}
	body := map[string]interface{}{"all": true, "command": "hostname", "output_format": "bogus"}
	bodyJSON, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/exec", bytes.NewReader(bodyJSON))
	w := httptest.NewRecorder()
	h.HandleExec(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid format, got %d", w.Code)
	}
}

// TestExec_HeaderFormat verifies "header" format produces === hostname === blocks.
func TestExec_HeaderFormat(t *testing.T) {
	const nodeID = "node-aaa"
	hub := newExecTestHub(map[string]clientd.OperatorExecResultPayload{
		nodeID: okExecResult("node-aaa\n"),
	})
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes([]string{nodeID})},
		Hub: hub,
	}
	lines := collectSSELines(t, h, map[string]interface{}{
		"all":           true,
		"command":       "hostname",
		"output_format": "header",
	})
	if len(lines) < 2 {
		t.Fatalf("expected ≥2 SSE lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "=== host-"+nodeID+" ===") {
		t.Errorf("header: expected '=== host-node-aaa ===' in first line, got: %q", lines[0])
	}
	s := parseSummary(t, lines)
	if s.MaxExit != 0 {
		t.Errorf("expected max_exit_code=0, got %d", s.MaxExit)
	}
	if len(s.Results) != 1 {
		t.Errorf("expected 1 result, got %d", len(s.Results))
	}
}

// TestExec_InlineFormat verifies "inline" format prefixes each line with "hostname: ".
func TestExec_InlineFormat(t *testing.T) {
	const nodeID = "node-bbb"
	hub := newExecTestHub(map[string]clientd.OperatorExecResultPayload{
		nodeID: okExecResult("line1\nline2\n"),
	})
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes([]string{nodeID})},
		Hub: hub,
	}
	lines := collectSSELines(t, h, map[string]interface{}{
		"all":           true,
		"command":       "echo",
		"output_format": "inline",
	})
	if len(lines) < 3 {
		t.Fatalf("expected ≥3 lines (2 inline + summary), got %d", len(lines))
	}
	want0 := "host-" + nodeID + ": line1"
	want1 := "host-" + nodeID + ": line2"
	if lines[0] != want0 {
		t.Errorf("inline[0]: got %q, want %q", lines[0], want0)
	}
	if lines[1] != want1 {
		t.Errorf("inline[1]: got %q, want %q", lines[1], want1)
	}
}

// TestExec_RealtimeFormat is functionally identical to inline in a sync test.
func TestExec_RealtimeFormat(t *testing.T) {
	const nodeID = "node-ccc"
	hub := newExecTestHub(map[string]clientd.OperatorExecResultPayload{
		nodeID: okExecResult("hello\n"),
	})
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes([]string{nodeID})},
		Hub: hub,
	}
	lines := collectSSELines(t, h, map[string]interface{}{
		"all":           true,
		"command":       "echo",
		"output_format": "realtime",
	})
	if len(lines) < 2 {
		t.Fatalf("expected ≥2 lines, got %d", len(lines))
	}
	want := "host-" + nodeID + ": hello"
	if lines[0] != want {
		t.Errorf("realtime[0]: got %q, want %q", lines[0], want)
	}
}

// TestExec_JSONFormat verifies "json" format emits parseable JSON per output line.
func TestExec_JSONFormat(t *testing.T) {
	const nodeID = "node-ddd"
	hub := newExecTestHub(map[string]clientd.OperatorExecResultPayload{
		nodeID: okExecResult("json-line\n"),
	})
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes([]string{nodeID})},
		Hub: hub,
	}
	lines := collectSSELines(t, h, map[string]interface{}{
		"all":           true,
		"command":       "echo",
		"output_format": "json",
	})
	if len(lines) < 2 {
		t.Fatalf("expected ≥2 lines, got %d", len(lines))
	}
	var ev execSSEEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("json[0] not valid JSON: %v — %q", err, lines[0])
	}
	if ev.Hostname != "host-"+nodeID {
		t.Errorf("json: hostname %q, want %q", ev.Hostname, "host-"+nodeID)
	}
	if ev.Stream != "stdout" {
		t.Errorf("json: stream %q, want stdout", ev.Stream)
	}
	if ev.Line != "json-line" {
		t.Errorf("json: line %q, want json-line", ev.Line)
	}
}

// TestExec_ConsolidateIdentical verifies nodes with identical output are grouped.
func TestExec_ConsolidateIdentical(t *testing.T) {
	nodes := []string{"node-e1", "node-e2", "node-e3"}
	hub := newExecTestHub(map[string]clientd.OperatorExecResultPayload{
		"node-e1": okExecResult("same output\n"),
		"node-e2": okExecResult("same output\n"),
		"node-e3": okExecResult("same output\n"),
	})
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes(nodes)},
		Hub: hub,
	}
	lines := collectSSELines(t, h, map[string]interface{}{
		"all":           true,
		"command":       "uname",
		"output_format": "consolidate",
	})
	// 3 identical nodes → 1 group + 1 summary = 2 lines.
	if len(lines) < 2 {
		t.Fatalf("consolidate identical: expected ≥2 lines, got %d", len(lines))
	}
	groupLine := lines[0]
	for _, id := range nodes {
		if !strings.Contains(groupLine, "host-"+id) {
			t.Errorf("consolidate: group header missing hostname %q; line: %q", "host-"+id, groupLine)
		}
	}
	if !strings.Contains(groupLine, "same output") {
		t.Errorf("consolidate: group line should contain 'same output'; got: %q", groupLine)
	}
	s := parseSummary(t, lines)
	if s.MaxExit != 0 {
		t.Errorf("consolidate: expected max_exit_code=0, got %d", s.MaxExit)
	}
}

// TestExec_ConsolidateDistinct verifies nodes with different output form separate groups.
func TestExec_ConsolidateDistinct(t *testing.T) {
	hub := newExecTestHub(map[string]clientd.OperatorExecResultPayload{
		"n1": okExecResult("output-A\n"),
		"n2": okExecResult("output-B\n"),
	})
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes([]string{"n1", "n2"})},
		Hub: hub,
	}
	lines := collectSSELines(t, h, map[string]interface{}{
		"all":           true,
		"command":       "uname",
		"output_format": "consolidate",
	})
	// 2 distinct outputs → 2 group lines + 1 summary = 3 lines minimum.
	if len(lines) < 3 {
		t.Fatalf("consolidate distinct: expected ≥3 lines, got %d: %v", len(lines), lines)
	}
}

// TestExec_NodeDisconnected verifies disconnected nodes produce error events.
func TestExec_NodeDisconnected(t *testing.T) {
	const nodeID = "node-offline"
	hub := newExecTestHub(
		map[string]clientd.OperatorExecResultPayload{},
		nodeID, // mark as disconnected
	)
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes([]string{nodeID})},
		Hub: hub,
	}
	lines := collectSSELines(t, h, map[string]interface{}{
		"all":           true,
		"command":       "hostname",
		"output_format": "header",
	})
	if len(lines) < 2 {
		t.Fatalf("expected ≥2 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "[error]") {
		t.Errorf("disconnected: expected [error] in output; got: %q", lines[0])
	}
	if !strings.Contains(lines[0], "not connected") {
		t.Errorf("disconnected: expected 'not connected'; got: %q", lines[0])
	}
	s := parseSummary(t, lines)
	if s.MaxExit == 0 {
		t.Errorf("disconnected: expected nonzero max_exit_code")
	}
}

// TestExec_SummaryIsLastLine verifies the summary event is always the final SSE event.
func TestExec_SummaryIsLastLine(t *testing.T) {
	nodes := []string{"n-a", "n-b", "n-c"}
	hub := newExecTestHub(map[string]clientd.OperatorExecResultPayload{
		"n-a": okExecResult("a-out\n"),
		"n-b": {ExitCode: 1, Stdout: "b-out\n"},
		"n-c": okExecResult("c-out\n"),
	})
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes(nodes)},
		Hub: hub,
	}
	lines := collectSSELines(t, h, map[string]interface{}{
		"all":           true,
		"command":       "hostname",
		"output_format": "header",
	})
	s := parseSummary(t, lines) // asserts last line is summary
	if s.MaxExit != 1 {
		t.Errorf("expected max_exit_code=1, got %d", s.MaxExit)
	}
	if len(s.Results) != 3 {
		t.Errorf("expected 3 results, got %d", len(s.Results))
	}
}

// TestExec_MaxExitCode verifies max_exit_code is the maximum per-node exit code.
func TestExec_MaxExitCode(t *testing.T) {
	hub := newExecTestHub(map[string]clientd.OperatorExecResultPayload{
		"n1": {ExitCode: 0},
		"n2": {ExitCode: 2},
		"n3": {ExitCode: 1},
	})
	h := &ExecHandler{
		DB:  &execTestDB{makeExecNodes([]string{"n1", "n2", "n3"})},
		Hub: hub,
	}
	lines := collectSSELines(t, h, map[string]interface{}{
		"all":     true,
		"command": "exit",
	})
	s := parseSummary(t, lines)
	if s.MaxExit != 2 {
		t.Errorf("expected max_exit_code=2, got %d", s.MaxExit)
	}
}

// TestSplitLines verifies the splitLines helper handles edge cases.
func TestSplitLines(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"hello\n", []string{"hello"}},
		{"a\nb\n", []string{"a", "b"}},
		{"a\r\nb\r\n", []string{"a", "b"}},
		{"no newline", []string{"no newline"}},
	}
	for _, tc := range tests {
		got := splitLines(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("splitLines(%q): got %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitLines(%q)[%d]: got %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}
