package main

// exec.go — `clustr exec` command (#126)
//
// Usage:
//
//	clustr exec [selector flags] --output FORMAT --timeout DUR -- COMMAND [ARG...]
//
// Fans out an arbitrary shell command to all nodes matching the selector via the
// operator_exec_request clientd WebSocket message type. Results are streamed as
// SSE from the server and printed in the requested output format.
//
// Output formats:
//
//	header      (default) per-node block: === hostname ===\nstdout
//	inline      per-line prefix: hostname: line
//	realtime    same as inline, interleaved as received
//	consolidate group nodes with identical output: === n01,n02 ===\nshared output
//	json        one JSON object per line: {"node":"...","stream":"...","line":"..."}
//
// Exit code: max of all per-node exit codes (0 = all succeeded).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sqoia-dev/clustr/internal/selector"
)

func newExecCmd() *cobra.Command {
	var (
		sel          selector.SelectorSet
		outputFormat string
		timeout      string
	)

	cmd := &cobra.Command{
		Use:   "exec -- COMMAND [ARG...]",
		Short: "Run a command on selected nodes via clientd",
		Long: `exec fans out an arbitrary command to all nodes matching the selector.

Results are streamed from the server as SSE events and printed in the requested
output format. The process exits with the maximum exit code returned by any node
(0 if all succeeded, 1 if any node was unreachable/errored).

The -- separator is required to separate selector/output flags from the command
and its arguments.

Output formats (--output):
  header      (default) per-node block: === hostname ===\nstdout
  inline      per-line prefix: hostname: line
  realtime    same as inline, printed as results arrive
  consolidate group nodes with identical output
  json        one JSON object per line per output line

Examples:
  clustr exec -A -- hostname
  clustr exec -n node[01-04] --output inline -- uptime
  clustr exec -g compute --output json -- cat /etc/os-release
  clustr exec -a --timeout 30s -- systemctl status slurmd`,

		// DisableFlagParsing is NOT set — we rely on the cobra convention that
		// everything after -- is treated as positional args, not flags.
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sel.IsEmpty() {
				return fmt.Errorf("at least one selector required (-n, -g, -A, -a)")
			}

			// Validate output format.
			switch outputFormat {
			case "header", "inline", "realtime", "consolidate", "json":
				// valid
			default:
				return fmt.Errorf("invalid --output %q; valid: header, inline, realtime, consolidate, json", outputFormat)
			}

			// Parse timeout.
			dur, err := time.ParseDuration(timeout)
			if err != nil {
				return fmt.Errorf("invalid --timeout %q: %w", timeout, err)
			}
			if dur < 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			if dur > time.Hour {
				return fmt.Errorf("--timeout exceeds 1h hard cap")
			}
			timeoutSec := int(dur.Seconds())
			if timeoutSec == 0 {
				timeoutSec = 60
			}

			command := args[0]
			cmdArgs := args[1:]

			return runExec(sel, command, cmdArgs, outputFormat, timeoutSec)
		},
	}

	selector.RegisterSelectorFlags(cmd, &sel)
	cmd.Flags().StringVarP(&outputFormat, "output", "o", "header",
		"Output format: header, inline, realtime, consolidate, json")
	cmd.Flags().StringVar(&timeout, "timeout", "60s",
		"Per-node execution timeout (e.g. 30s, 5m); hard cap 1h")

	return cmd
}

// execSSEPayload mirrors the server-side execSSEEvent for JSON format.
type execSSEPayload struct {
	NodeID   string `json:"node,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Stream   string `json:"stream,omitempty"`
	Line     string `json:"line,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Ts       string `json:"ts,omitempty"`
}

// execSummary mirrors execSummaryEvent from the server.
type execSummary struct {
	Type    string           `json:"type"`
	Results []execNodeResult `json:"results"`
	MaxExit int              `json:"max_exit_code"`
}

type execNodeResult struct {
	NodeID   string `json:"node_id"`
	Hostname string `json:"hostname"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// runExec sends POST /api/v1/exec and streams SSE results to stdout.
// Returns an error wrapping the max_exit_code from the summary when nonzero.
func runExec(sel selector.SelectorSet, command string, cmdArgs []string, outputFormat string, timeoutSec int) error {
	c := clientFromFlags()

	// Build request body.
	body := map[string]interface{}{
		"nodes":         sel.Nodes,
		"group":         sel.Group,
		"all":           sel.All,
		"active":        sel.Active,
		"racks":         sel.Racks,
		"chassis":       sel.Chassis,
		"ignore_status": sel.IgnoreStatus,
		"command":       command,
		"args":          cmdArgs,
		"output_format": outputFormat,
		"timeout_sec":   timeoutSec,
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("exec: marshal request: %w", err)
	}

	// Use a no-timeout HTTP client for SSE streaming; the server keeps
	// the connection open until all nodes have responded.
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/exec", bytes.NewReader(bodyJSON))
	if err != nil {
		return fmt.Errorf("exec: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}

	sseClient := &http.Client{}
	resp, err := sseClient.Do(req)
	if err != nil {
		return fmt.Errorf("exec: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("exec: server returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Parse SSE stream.
	// Each SSE event is: "data: <payload>\n\n"
	// The final event is the summary JSON.
	var summary *execSummary
	maxExit := 0

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[len("data: "):]
		if data == "" {
			continue
		}

		// Try to detect the summary event.
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(data), &probe)
		if probe.Type == "summary" {
			var s execSummary
			if err := json.Unmarshal([]byte(data), &s); err == nil {
				summary = &s
				maxExit = s.MaxExit
			}
			continue
		}

		// Print the data line according to format.
		// For json format the server already emits valid JSON per line.
		// For all other formats the server emits pre-rendered text.
		fmt.Println(data)
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("exec: read stream: %w", err)
	}

	// Print summary to stderr (not stdout) for human formats.
	if summary != nil && outputFormat != "json" {
		fmt.Fprintf(os.Stderr, "\n%d node(s)  |  max exit code: %d\n",
			len(summary.Results), maxExit)
		if maxExit != 0 {
			for _, r := range summary.Results {
				if r.ExitCode != 0 || r.Error != "" {
					name := r.Hostname
					if name == "" {
						name = shortID(r.NodeID)
					}
					if r.Error != "" {
						fmt.Fprintf(os.Stderr, "  %-30s  exit=%d  error=%s\n", name, r.ExitCode, r.Error)
					} else {
						fmt.Fprintf(os.Stderr, "  %-30s  exit=%d\n", name, r.ExitCode)
					}
				}
			}
		}
	}

	if maxExit != 0 {
		// Return a typed exit-code error so the main RunE machinery converts it.
		return &execExitError{code: maxExit}
	}
	return nil
}

// execExitError carries a numeric exit code so the top-level command runner
// can call os.Exit with it instead of printing an error message.
type execExitError struct{ code int }

func (e *execExitError) Error() string { return fmt.Sprintf("exit code %d", e.code) }
func (e *execExitError) ExitCode() int  { return e.code }
