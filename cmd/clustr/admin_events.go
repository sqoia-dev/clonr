package main

// admin_events.go — Sprint 42 Day 4 EVENT-LOG-JSONL
//
// Implements `clustr admin events tail [--follow] [--filter <glob>]`
//
// Local mode (no --server flag and /var/lib/clustr/log/events.jsonl exists):
//   Reads directly from the file. --follow polls the file for new lines.
//   --filter <glob> restricts output to lines whose action matches the glob.
//
// Remote mode (--server is set, or local file does not exist):
//   Calls GET /api/v1/admin/events (SSE) on the clustr-serverd instance.
//   Streams SSE data: lines to stdout.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/pkg/client"
)

// defaultLocalEventLogPath is the path checked before falling back to remote.
const defaultLocalEventLogPath = "/var/lib/clustr/log/events.jsonl"

func newAdminEventsTailCmd() *cobra.Command {
	var (
		flagFollow bool
		flagFilter string
	)

	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Stream the JSONL structured event log",
		Long: `Stream the clustr structured event log (Sprint 42 EVENT-LOG-JSONL).

Local mode: when run on the clustr-serverd host (and --server is not set),
reads /var/lib/clustr/log/events.jsonl directly.

Remote mode: streams events via GET /api/v1/admin/events on the configured
clustr-serverd instance using Server-Sent Events.

Filter examples:
  --filter 'dangerous_push.*'   show only dangerous push events
  --filter 'notice.*'           show only notice events
  --filter 'node.*'             show only node lifecycle events`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			// Determine local vs remote mode.
			isRemote := flagServer != ""
			localPath := defaultLocalEventLogPath
			if !isRemote {
				if _, err := os.Stat(localPath); err != nil {
					isRemote = true
				}
			}

			if isRemote {
				return runEventsTailRemote(ctx, flagFollow, flagFilter)
			}
			return runEventsTailLocal(ctx, localPath, flagFollow, flagFilter)
		},
	}

	cmd.Flags().BoolVarP(&flagFollow, "follow", "f", false, "Keep streaming new events as they arrive")
	cmd.Flags().StringVar(&flagFilter, "filter", "", "Only show events whose action matches this glob (e.g. dangerous_push.*)")
	return cmd
}

// ─── Local (file) mode ───────────────────────────────────────────────────────

func runEventsTailLocal(ctx context.Context, path string, follow bool, filterGlob string) error {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("open event log %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if filterGlob != "" && !matchActionGlobCLI(filterGlob, line) {
			continue
		}
		fmt.Println(line)
	}
	if !follow {
		return scanner.Err()
	}

	// Follow mode: poll every 500 ms for new lines.
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if filterGlob != "" && !matchActionGlobCLI(filterGlob, line) {
				continue
			}
			fmt.Println(line)
		}
		if err := scanner.Err(); err != nil && err != io.EOF {
			return err
		}
	}
}

// ─── Remote (SSE) mode ───────────────────────────────────────────────────────

func runEventsTailRemote(ctx context.Context, follow bool, filterGlob string) error {
	cfg := config.LoadClientConfig()
	if flagServer != "" {
		cfg.ServerURL = flagServer
	}
	if flagToken != "" {
		cfg.AuthToken = flagToken
	}
	_ = client.New(cfg.ServerURL, cfg.AuthToken) // validate config resolved

	endpoint := cfg.ServerURL + "/api/v1/admin/events"
	params := url.Values{}
	if follow {
		params.Set("follow", "1")
	}
	if filterGlob != "" {
		params.Set("filter", filterGlob)
	}
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("events request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		// SSE format: "data: <payload>\n\n"
		if strings.HasPrefix(line, "data: ") {
			fmt.Println(strings.TrimPrefix(line, "data: "))
		}
		// Skip comment lines (": ...") and blank lines.
	}
	return scanner.Err()
}

// ─── Action glob helper ───────────────────────────────────────────────────────

// matchActionGlobCLI extracts the action field from a JSONL line and tests it
// against the glob using filepath.Match semantics.
func matchActionGlobCLI(glob, line string) bool {
	const needle = `"action":"`
	idx := strings.Index(line, needle)
	if idx >= 0 {
		rest := line[idx+len(needle):]
		end := strings.IndexByte(rest, '"')
		if end >= 0 {
			action := rest[:end]
			// filepath.Match uses '/' as separator. Replace '.' with '/' for matching,
			// then restore so event namespaces (node.create) work with 'node.*' globs.
			matched, _ := matchDotGlob(glob, action)
			return matched
		}
	}
	// Fallback: match against full line.
	matched, _ := matchDotGlob(glob, line)
	return matched
}

// matchDotGlob applies filepath.Match with '.' treated as the path separator.
// This lets "dangerous_push.*" match "dangerous_push.staged" etc.
func matchDotGlob(pattern, name string) (bool, error) {
	// Replace '.' with '/' in both pattern and name for filepath.Match semantics.
	p := strings.ReplaceAll(pattern, ".", "/")
	n := strings.ReplaceAll(name, ".", "/")
	return filepath.Match(p, n)
}
