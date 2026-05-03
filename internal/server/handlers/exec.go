package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/selector"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ExecDBIface is the database interface required by ExecHandler.
type ExecDBIface interface {
	ListAllNodes(ctx context.Context) ([]selector.SelectorNode, error)
	ListGroupMemberIDs(ctx context.Context, groupName string) ([]selector.NodeID, error)
	ListNodeIDsByRackNames(ctx context.Context, rackNames []string) ([]selector.NodeID, error)
	ListNodeIDsByEnclosureLabels(ctx context.Context, labels []string) ([]selector.NodeID, error)
}

// ExecHubIface is the hub interface required by ExecHandler.
type ExecHubIface interface {
	IsConnected(nodeID string) bool
	Send(nodeID string, msg clientd.ServerMessage) error
	RegisterOperatorExec(msgID string) <-chan clientd.OperatorExecResultPayload
	UnregisterOperatorExec(msgID string)
}

// ExecHandler implements POST /api/v1/exec — batch parallel exec across a node selector.
type ExecHandler struct {
	DB  ExecDBIface
	Hub ExecHubIface
}

// execAPIRequest is the JSON body for POST /api/v1/exec.
type execAPIRequest struct {
	// Selector fields — mirrors SelectorSet.
	Nodes        string `json:"nodes,omitempty"`
	Group        string `json:"group,omitempty"`
	All          bool   `json:"all,omitempty"`
	Active       bool   `json:"active,omitempty"`
	Racks        string `json:"racks,omitempty"`
	Chassis      string `json:"chassis,omitempty"`
	IgnoreStatus bool   `json:"ignore_status,omitempty"`

	// Command is the binary to execute. Required.
	Command string `json:"command"`
	// Args is the argument list (no shell expansion).
	Args []string `json:"args,omitempty"`
	// OutputFormat controls how results are rendered. Defaults to "header".
	// Valid values: "inline", "header", "consolidate", "realtime", "json".
	OutputFormat string `json:"output_format,omitempty"`
	// TimeoutSec is the per-node execution timeout. Defaults to 60, hard cap 3600.
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// execSSEEvent is one SSE-streamed event for a single node's result.
type execSSEEvent struct {
	NodeID   string `json:"node"`
	Hostname string `json:"hostname"`
	Stream   string `json:"stream"` // "stdout", "stderr", "error"
	Line     string `json:"line"`
	ExitCode int    `json:"exit_code,omitempty"`
	// Ts is an RFC3339 timestamp of when the result was received on the server.
	Ts string `json:"ts"`
}

// execSummaryEvent is the final SSE event reporting per-node exit codes.
type execSummaryEvent struct {
	Type    string           `json:"type"` // "summary"
	Results []execNodeResult `json:"results"`
	MaxExit int              `json:"max_exit_code"`
}

type execNodeResult struct {
	NodeID   string `json:"node_id"`
	Hostname string `json:"hostname"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// execFanResult is the per-node result collected by HandleExec's goroutine fan-out.
// It is a package-level type so that streamResults and streamConsolidate can
// reference it as a channel element type.
type execFanResult struct {
	nodeID   string
	hostname string
	payload  clientd.OperatorExecResultPayload
	err      error // node not connected or send failed
}

// HandleExec handles POST /api/v1/exec.
// It resolves the selector, fans out operator_exec_request to all connected target nodes,
// collects results, and streams them as SSE in the requested output format.
func (h *ExecHandler) HandleExec(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeValidationError(w, "failed to read request body")
		return
	}

	var req execAPIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Command == "" {
		writeValidationError(w, "command is required")
		return
	}

	outputFormat := req.OutputFormat
	if outputFormat == "" {
		outputFormat = "header"
	}
	switch outputFormat {
	case "inline", "header", "consolidate", "realtime", "json":
		// valid
	default:
		writeValidationError(w, fmt.Sprintf("invalid output_format %q; valid: inline, header, consolidate, realtime, json", outputFormat))
		return
	}

	timeoutSec := req.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if timeoutSec > 3600 {
		timeoutSec = 3600
	}

	set := selector.SelectorSet{
		Nodes:        req.Nodes,
		Group:        req.Group,
		All:          req.All,
		Active:       req.Active,
		Racks:        req.Racks,
		Chassis:      req.Chassis,
		IgnoreStatus: req.IgnoreStatus,
	}
	if set.IsEmpty() {
		writeValidationError(w, "at least one selector required")
		return
	}

	nodeIDs, err := selector.Resolve(r.Context(), h.DB, set)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.ErrorResponse{
			Error: err.Error(),
			Code:  "selector_error",
		})
		return
	}
	if len(nodeIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"message": "selector matched zero nodes",
			"results": []interface{}{},
		})
		return
	}

	// Resolve hostnames for display.
	// We build a map from the nodes we fetched during selector resolution.
	// A second DB call is wasteful; instead look up hostnames via a helper on ExecDBIface.
	hostnameByID := make(map[string]string, len(nodeIDs))
	allNodes, _ := h.DB.ListAllNodes(r.Context())
	for _, n := range allNodes {
		hostnameByID[n.ID] = n.Hostname
	}

	// Set up SSE.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
			Error: "streaming not supported by this transport",
			Code:  "no_flusher",
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Fan out to connected nodes.
	args := req.Args
	if args == nil {
		args = []string{}
	}

	results := make(chan execFanResult, len(nodeIDs))

	var wg sync.WaitGroup
	for _, nid := range nodeIDs {
		nid := nid
		hostname := hostnameByID[nid]
		if hostname == "" {
			hostname = nid
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			if !h.Hub.IsConnected(nid) {
				results <- execFanResult{
					nodeID:   nid,
					hostname: hostname,
					err:      fmt.Errorf("node not connected (clustr-clientd offline)"),
				}
				return
			}

			msgID := uuid.New().String()
			execPayload, _ := json.Marshal(clientd.OperatorExecRequestPayload{
				RefMsgID:   msgID,
				Command:    req.Command,
				Args:       args,
				TimeoutSec: timeoutSec,
			})

			serverMsg := clientd.ServerMessage{
				Type:    "operator_exec_request",
				MsgID:   msgID,
				Payload: json.RawMessage(execPayload),
			}

			ch := h.Hub.RegisterOperatorExec(msgID)
			defer h.Hub.UnregisterOperatorExec(msgID)

			if err := h.Hub.Send(nid, serverMsg); err != nil {
				results <- execFanResult{
					nodeID:   nid,
					hostname: hostname,
					err:      fmt.Errorf("send failed: %w", err),
				}
				return
			}

			// Wait for result with a server-side deadline slightly longer than
			// the node's per-command timeout to ensure the node's own timeout
			// fires first (cleaner error messages).
			deadline := time.Duration(timeoutSec+5) * time.Second
			select {
			case res := <-ch:
				results <- execFanResult{
					nodeID:   nid,
					hostname: hostname,
					payload:  res,
				}
			case <-time.After(deadline):
				results <- execFanResult{
					nodeID:   nid,
					hostname: hostname,
					err:      fmt.Errorf("timed out waiting for result (%ds)", timeoutSec+5),
				}
			case <-r.Context().Done():
				results <- execFanResult{
					nodeID:   nid,
					hostname: hostname,
					err:      fmt.Errorf("client disconnected"),
				}
			}
		}()
	}

	// Collect results in background and close channel when all goroutines finish.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Stream results to client.
	// For "consolidate" we need to collect all results before rendering.
	// For all other formats we stream as results arrive.
	switch outputFormat {
	case "consolidate":
		h.streamConsolidate(w, flusher, results, len(nodeIDs))
	default:
		h.streamResults(w, flusher, results, outputFormat)
	}
}

// sseWrite writes a single SSE data line to the response writer.
func sseWrite(w io.Writer, flusher http.Flusher, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// streamResults streams results as they arrive for inline / header / realtime / json formats.
func (h *ExecHandler) streamResults(w http.ResponseWriter, flusher http.Flusher,
	results <-chan execFanResult, format string) {

	var summary execSummaryEvent
	summary.Type = "summary"
	maxExit := 0

	for res := range results {
		nr := execNodeResult{
			NodeID:   res.nodeID,
			Hostname: res.hostname,
		}

		if res.err != nil {
			nr.ExitCode = -1
			nr.Error = res.err.Error()
			summary.Results = append(summary.Results, nr)
			if maxExit < 1 {
				maxExit = 1
			}

			switch format {
			case "json":
				ev, _ := json.Marshal(execSSEEvent{
					NodeID:   res.nodeID,
					Hostname: res.hostname,
					Stream:   "error",
					Line:     res.err.Error(),
					ExitCode: -1,
					Ts:       time.Now().UTC().Format(time.RFC3339),
				})
				sseWrite(w, flusher, string(ev))
			default:
				sseWrite(w, flusher, formatNodeError(res.hostname, res.err.Error(), format))
			}
			continue
		}

		p := res.payload
		nr.ExitCode = p.ExitCode
		if p.Error != "" {
			nr.Error = p.Error
		}
		summary.Results = append(summary.Results, nr)
		if p.ExitCode > maxExit {
			maxExit = p.ExitCode
		}

		switch format {
		case "json":
			ts := time.Now().UTC().Format(time.RFC3339)
			for _, line := range splitLines(p.Stdout) {
				ev, _ := json.Marshal(execSSEEvent{
					NodeID:   res.nodeID,
					Hostname: res.hostname,
					Stream:   "stdout",
					Line:     line,
					ExitCode: p.ExitCode,
					Ts:       ts,
				})
				sseWrite(w, flusher, string(ev))
			}
			for _, line := range splitLines(p.Stderr) {
				ev, _ := json.Marshal(execSSEEvent{
					NodeID:   res.nodeID,
					Hostname: res.hostname,
					Stream:   "stderr",
					Line:     line,
					ExitCode: p.ExitCode,
					Ts:       ts,
				})
				sseWrite(w, flusher, string(ev))
			}
		case "inline":
			for _, line := range splitLines(p.Stdout) {
				sseWrite(w, flusher, res.hostname+": "+line)
			}
			for _, line := range splitLines(p.Stderr) {
				sseWrite(w, flusher, res.hostname+": [stderr] "+line)
			}
		case "header":
			var sb strings.Builder
			sb.WriteString("=== " + res.hostname + " ===\n")
			sb.WriteString(p.Stdout)
			if p.Stderr != "" {
				sb.WriteString("[stderr]\n")
				sb.WriteString(p.Stderr)
			}
			sseWrite(w, flusher, sb.String())
		case "realtime":
			for _, line := range splitLines(p.Stdout) {
				sseWrite(w, flusher, res.hostname+": "+line)
			}
			for _, line := range splitLines(p.Stderr) {
				sseWrite(w, flusher, res.hostname+": [stderr] "+line)
			}
		}
	}

	summary.MaxExit = maxExit
	sortSummaryResults(&summary)
	summaryJSON, _ := json.Marshal(summary)
	sseWrite(w, flusher, string(summaryJSON))
}

// streamConsolidate collects all results then groups nodes with identical output,
// printing each group's output once with the node list before it.
func (h *ExecHandler) streamConsolidate(w http.ResponseWriter, flusher http.Flusher,
	results <-chan execFanResult, total int) {

	type groupKey struct {
		stdout    string
		stderr    string
		exitCode  int
		errString string
	}
	type outputGroup struct {
		key   groupKey
		nodes []string // hostnames
	}

	var collected []execFanResult
	for res := range results {
		collected = append(collected, res)
	}

	// Group by (stdout hash, stderr hash, exit_code).
	groups := make(map[string]*outputGroup)
	var groupOrder []string

	for _, res := range collected {
		var key groupKey
		if res.err != nil {
			key = groupKey{errString: res.err.Error(), exitCode: -1}
		} else {
			key = groupKey{
				stdout:   res.payload.Stdout,
				stderr:   res.payload.Stderr,
				exitCode: res.payload.ExitCode,
			}
		}

		// Hash the key for map lookup.
		h256 := sha256.Sum256([]byte(fmt.Sprintf("%v", key)))
		k := fmt.Sprintf("%x", h256)

		if _, ok := groups[k]; !ok {
			groups[k] = &outputGroup{key: key}
			groupOrder = append(groupOrder, k)
		}
		hostname := res.hostname
		if hostname == "" {
			hostname = res.nodeID
		}
		groups[k].nodes = append(groups[k].nodes, hostname)
	}

	maxExit := 0
	var summaryResults []execNodeResult

	for _, k := range groupOrder {
		g := groups[k]
		sort.Strings(g.nodes)

		nodeList := strings.Join(g.nodes, ",")
		var sb strings.Builder
		sb.WriteString("=== " + nodeList + " ===\n")

		if g.key.errString != "" {
			sb.WriteString("[error] " + g.key.errString + "\n")
			if maxExit < 1 {
				maxExit = 1
			}
		} else {
			sb.WriteString(g.key.stdout)
			if g.key.stderr != "" {
				sb.WriteString("[stderr]\n")
				sb.WriteString(g.key.stderr)
			}
			if g.key.exitCode > maxExit {
				maxExit = g.key.exitCode
			}
		}
		sseWrite(w, flusher, sb.String())

		for _, res := range collected {
			hostname := res.hostname
			if hostname == "" {
				hostname = res.nodeID
			}
			if containsString(g.nodes, hostname) {
				nr := execNodeResult{NodeID: res.nodeID, Hostname: hostname, ExitCode: g.key.exitCode}
				if g.key.errString != "" {
					nr.Error = g.key.errString
					nr.ExitCode = -1
				}
				summaryResults = append(summaryResults, nr)
			}
		}
	}

	// Final summary event.
	summary := execSummaryEvent{
		Type:    "summary",
		Results: summaryResults,
		MaxExit: maxExit,
	}
	sortSummaryResults(&summary)
	summaryJSON, _ := json.Marshal(summary)
	sseWrite(w, flusher, string(summaryJSON))
}

// formatNodeError renders an error line for non-JSON formats.
func formatNodeError(hostname, errMsg, format string) string {
	switch format {
	case "header":
		return "=== " + hostname + " ===\n[error] " + errMsg + "\n"
	default:
		return hostname + ": [error] " + errMsg
	}
}

// splitLines splits a string into non-empty lines.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	raw := strings.Split(s, "\n")
	var out []string
	for _, l := range raw {
		// Trim trailing CR for Windows-style line endings.
		l = strings.TrimRight(l, "\r")
		out = append(out, l)
	}
	// Trim trailing empty line from a final newline.
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// sortSummaryResults sorts summary results by hostname for deterministic output.
func sortSummaryResults(s *execSummaryEvent) {
	sort.Slice(s.Results, func(i, j int) bool {
		return s.Results[i].Hostname < s.Results[j].Hostname
	})
}

// containsString reports whether s contains the target string.
func containsString(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}

// ─── ExecDBIface adapter for *db.DB ──────────────────────────────────────────

// ExecDBAdapter wraps a *selector.DBAdapter to satisfy ExecDBIface.
// In production, use selector.NewDBAdapter(db) and pass the result here.
type ExecDBAdapter struct {
	inner *selector.DBAdapter
}

// NewExecDBAdapter creates a new ExecDBAdapter.
func NewExecDBAdapter(inner *selector.DBAdapter) *ExecDBAdapter {
	return &ExecDBAdapter{inner: inner}
}

func (a *ExecDBAdapter) ListAllNodes(ctx context.Context) ([]selector.SelectorNode, error) {
	return a.inner.ListAllNodes(ctx)
}

func (a *ExecDBAdapter) ListGroupMemberIDs(ctx context.Context, groupName string) ([]selector.NodeID, error) {
	return a.inner.ListGroupMemberIDs(ctx, groupName)
}

func (a *ExecDBAdapter) ListNodeIDsByRackNames(ctx context.Context, rackNames []string) ([]selector.NodeID, error) {
	return a.inner.ListNodeIDsByRackNames(ctx, rackNames)
}

func (a *ExecDBAdapter) ListNodeIDsByEnclosureLabels(ctx context.Context, labels []string) ([]selector.NodeID, error) {
	return a.inner.ListNodeIDsByEnclosureLabels(ctx, labels)
}
