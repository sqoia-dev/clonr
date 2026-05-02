package main

// cp.go — `clustr cp` command (#127)
//
// Usage:
//
//	clustr cp [selector flags] [--recursive] [--preserve] [--parallel N] SRC DST
//
// Pushes a file or directory from the server's local filesystem to the DST path
// on every node matching the selector. The server tars the source, base64-encodes
// the tarball, and delivers it via operator_exec_request (same WebSocket transport
// as `clustr exec`).
//
// v1 constraints:
//   - SRC is an absolute path readable by clustr-serverd (server-side path).
//   - Max uncompressed source: 32 MB.
//   - No rsync delta; full push each time.
//
// Exit code: max of all per-node exit codes.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sqoia-dev/clustr/internal/selector"
)

func newCpCmd() *cobra.Command {
	var (
		sel         selector.SelectorSet
		recursive   bool
		preserve    bool
		includeSelf bool
		parallel    int
	)

	cmd := &cobra.Command{
		Use:   "cp SRC DST",
		Short: "Push a file or directory from the server to selected nodes",
		Long: `cp pushes a file or directory from the server's local filesystem to a destination
path on every node matching the selector.

SRC must be an absolute path readable by clustr-serverd.
DST is the destination directory on the target nodes; it is created if absent.

The server builds an in-memory tar archive of SRC, encodes it as base64, and
delivers it to each target node via the operator_exec_request clientd message
(same WebSocket transport as 'clustr exec'). The node extracts the archive into
DST using /bin/sh + tar.

v1 limits:
  - SRC must reside on the server's local filesystem.
  - Max uncompressed source size: 32 MB.

Examples:
  clustr cp -A /etc/munge/munge.key /etc/munge/
  clustr cp -n node[01-04] -r /etc/slurm /etc/slurm
  clustr cp -g compute --preserve /etc/hosts /etc/hosts`,

		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sel.IsEmpty() {
				return fmt.Errorf("at least one selector required (-n, -g, -A, -a)")
			}
			srcPath := args[0]
			dstPath := args[1]

			if !strings.HasPrefix(srcPath, "/") {
				return fmt.Errorf("SRC must be an absolute path (got %q)", srcPath)
			}

			return runCp(sel, srcPath, dstPath, recursive, preserve, includeSelf, parallel)
		},
	}

	selector.RegisterSelectorFlags(cmd, &sel)
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Copy directory trees recursively")
	cmd.Flags().BoolVar(&preserve, "preserve", false, "Preserve file permissions, ownership, and timestamps")
	cmd.Flags().BoolVar(&includeSelf, "include-self", false, "Also push to the server node itself (reserved; no-op in v1)")
	cmd.Flags().IntVar(&parallel, "parallel", 8, "Max concurrent target nodes (1–64)")

	return cmd
}

// cpProgressEvent is the per-node progress SSE event from POST /api/v1/cp.
type cpProgressEvent struct {
	Node     string `json:"node"`
	NodeID   string `json:"node_id"`
	Status   string `json:"status"` // "ok" | "failed"
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// cpSummary mirrors cpSummaryEvent from the server.
type cpSummary struct {
	Type    string          `json:"type"`
	Results []cpNodeResult2 `json:"results"`
	MaxExit int             `json:"max_exit_code"`
}

type cpNodeResult2 struct {
	NodeID   string `json:"node_id"`
	Hostname string `json:"hostname"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// runCp sends POST /api/v1/cp and streams SSE progress events to stdout.
func runCp(sel selector.SelectorSet, srcPath, dstPath string, recursive, preserve, includeSelf bool, parallel int) error {
	c := clientFromFlags()

	body := map[string]interface{}{
		"nodes":         sel.Nodes,
		"group":         sel.Group,
		"all":           sel.All,
		"active":        sel.Active,
		"racks":         sel.Racks,
		"chassis":       sel.Chassis,
		"ignore_status": sel.IgnoreStatus,
		"src_path":      srcPath,
		"dst_path":      dstPath,
		"recursive":     recursive,
		"preserve":      preserve,
		"include_self":  includeSelf,
		"parallel":      parallel,
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("cp: marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/cp", bytes.NewReader(bodyJSON))
	if err != nil {
		return fmt.Errorf("cp: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}

	sseClient := &http.Client{}
	resp, err := sseClient.Do(req)
	if err != nil {
		return fmt.Errorf("cp: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cp: server returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var summary *cpSummary
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

		// Detect summary event.
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(data), &probe)
		if probe.Type == "summary" {
			var s cpSummary
			if err := json.Unmarshal([]byte(data), &s); err == nil {
				summary = &s
				maxExit = s.MaxExit
			}
			continue
		}

		// Per-node progress event.
		var prog cpProgressEvent
		if err := json.Unmarshal([]byte(data), &prog); err == nil && prog.Node != "" {
			if prog.Status == "ok" {
				fmt.Printf("  %-30s  ok\n", prog.Node)
			} else {
				errMsg := prog.Error
				if errMsg == "" {
					errMsg = fmt.Sprintf("exit=%d", prog.ExitCode)
				}
				fmt.Printf("  %-30s  FAILED  %s\n", prog.Node, errMsg)
			}
		} else {
			// Unknown event — print raw.
			fmt.Println(data)
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("cp: read stream: %w", err)
	}

	// Print summary.
	if summary != nil {
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
		return &execExitError{code: maxExit}
	}
	return nil
}
