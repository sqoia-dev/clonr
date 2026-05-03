package main

// health.go — `clustr health` command (#130, refactored in #125 cleanup)
//
// Usage:
//
//	clustr health [selector flags] [--ping | --wait [--timeout DUR]]
//	clustr health --ping [--ping-timeout DUR]
//
// Displays per-node reachability, heartbeat age, and status summary.
// Accepts the full selector grammar shared by exec, cp, and console.
//
// --ping performs a server round-trip check against GET /api/v1/health and
// prints: OK clustr-server <host>:<port> rt=<N>ms
// Default ping timeout: 5s. Override via --ping-timeout or CLUSTR_PING_TIMEOUT.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/sqoia-dev/clustr/internal/selector"
)

// nodeHealthEntry and nodeHealthResponse mirror the server-side types in
// internal/server/handlers/node_health.go. Defined locally; the CLI does not
// import the handlers package.
type cliNodeHealthEntry struct {
	NodeID        string     `json:"node_id"`
	Name          string     `json:"name"`
	Reachable     bool       `json:"reachable"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
	HeartbeatAge  float64    `json:"heartbeat_age_seconds"`
	Status        string     `json:"status"`
}

type cliNodeHealthResponse struct {
	Nodes       []cliNodeHealthEntry `json:"nodes"`
	TotalNodes  int                  `json:"total_nodes"`
	Reachable   int                  `json:"reachable"`
	Unreachable int                  `json:"unreachable"`
	AsOf        time.Time            `json:"as_of"`
}

func newHealthCmd() *cobra.Command {
	var (
		sel             selector.SelectorSet
		flagPing        bool
		flagWait        bool
		flagTimeout     string
		flagPingTimeout string
	)

	cmd := &cobra.Command{
		Use:   "health",
		Short: "Aggregate per-node reachability and status summary",
		Long: `health shows the reachability and heartbeat state for nodes managed by this cluster.

By default it prints a table: NODE / STATUS / REACHABLE / HEARTBEAT AGE.

--ping performs a server round-trip check against GET /api/v1/health and prints:

  OK clustr-server <host>:<port> rt=<N>ms

Default timeout is 5s; override with --ping-timeout or CLUSTR_PING_TIMEOUT env var.
Exits non-zero on timeout, connection refused, or any non-200 response.

--wait polls every 2s until all targeted nodes show reachable=true, or until
--timeout (default 5m) expires.

Selector flags (full grammar from #125):
  -n HOSTLIST  node01, node[01-32], node[01-04,08,12-15]
  -g NAME      node group
  -A           all registered nodes (default when no selector given)
  -a           active nodes only (deployed_verified state)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagPing {
				return runServerPing(flagPingTimeout)
			}

			// Resolve timeout for --wait.
			timeout := 5 * time.Minute
			if flagTimeout != "" {
				d, err := time.ParseDuration(flagTimeout)
				if err != nil {
					return fmt.Errorf("--timeout: expected a duration like 5m, 30s: %w", err)
				}
				timeout = d
			}

			if flagWait {
				return runHealthWait(sel, timeout)
			}

			resp, err := fetchHealthSel(sel)
			if err != nil {
				return err
			}

			printHealthTable(resp)
			return nil
		},
	}

	selector.RegisterSelectorFlags(cmd, &sel)
	cmd.Flags().BoolVar(&flagPing, "ping", false, "Round-trip latency check against clustr-serverd (exits non-zero on failure)")
	cmd.Flags().BoolVar(&flagWait, "wait", false, "Poll until all targets are reachable")
	cmd.Flags().StringVar(&flagTimeout, "timeout", "5m", "Timeout for --wait (e.g. 5m, 30s)")
	cmd.Flags().StringVar(&flagPingTimeout, "ping-timeout", "", "Timeout for --ping (default 5s; env: CLUSTR_PING_TIMEOUT)")

	return cmd
}

// runServerPing hits GET /api/v1/health, measures round-trip latency, and
// prints: OK clustr-server <host>:<port> rt=<N>ms
//
// Timeout resolution order: --ping-timeout flag → CLUSTR_PING_TIMEOUT env → 5s default.
// Returns a non-nil error (and exits non-zero) on timeout, conn refused, or non-200.
func runServerPing(flagPingTimeout string) error {
	timeout := resolvePingTimeout(flagPingTimeout)

	c := clientFromFlags()
	serverURL := c.BaseURL

	// Parse host:port for the output line.
	u, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Errorf("ping: invalid server URL %q: %w", serverURL, err)
	}
	hostPort := u.Host

	pingURL := serverURL + "/api/v1/health"
	req, err := http.NewRequest(http.MethodGet, pingURL, nil)
	if err != nil {
		return fmt.Errorf("ping: build request: %w", err)
	}
	// Auth header so we measure a real authenticated round-trip.
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}
	req.Header.Set("Accept", "application/json")

	hc := &http.Client{Timeout: timeout}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := hc.Do(req)
	rt := time.Since(start)

	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("ping: timed out after %s connecting to %s", timeout, hostPort)
		}
		return fmt.Errorf("ping: %s: %w", hostPort, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping: %s returned HTTP %d", hostPort, resp.StatusCode)
	}

	rtMs := rt.Milliseconds()
	fmt.Printf("OK clustr-server %s rt=%dms\n", hostPort, rtMs)
	return nil
}

// resolvePingTimeout returns the ping timeout from flag → env → default (5s).
func resolvePingTimeout(flagVal string) time.Duration {
	if flagVal != "" {
		if d, err := time.ParseDuration(flagVal); err == nil {
			return d
		}
	}
	if env := os.Getenv("CLUSTR_PING_TIMEOUT"); env != "" {
		if d, err := time.ParseDuration(env); err == nil {
			return d
		}
	}
	return 5 * time.Second
}

// fetchHealthSel calls GET /api/v1/cluster/health with selector query params.
func fetchHealthSel(sel selector.SelectorSet) (*cliNodeHealthResponse, error) {
	ctx := context.Background()
	c := clientFromFlags()

	// Build query string from selector fields.
	q := url.Values{}
	if sel.Nodes != "" {
		q.Set("nodes", sel.Nodes)
	}
	if sel.Group != "" {
		q.Set("group", sel.Group)
	}
	if sel.All {
		q.Set("all", "true")
	}
	if sel.Active {
		q.Set("active", "true")
	}
	if sel.Racks != "" {
		q.Set("racks", sel.Racks)
	}
	if sel.Chassis != "" {
		q.Set("chassis", sel.Chassis)
	}
	if sel.IgnoreStatus {
		q.Set("ignore_status", "true")
	}

	path := "/api/v1/cluster/health"
	if len(q) > 0 {
		path = path + "?" + q.Encode()
	}

	var resp cliNodeHealthResponse
	if err := c.GetJSON(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("health: %w", err)
	}
	return &resp, nil
}

// printHealthTable renders the health summary as a tab-aligned table.
// Columns: NODE / STATUS / REACHABLE / HEARTBEAT
func printHealthTable(resp *cliNodeHealthResponse) {
	if len(resp.Nodes) == 0 {
		fmt.Println("No nodes registered.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NODE\tSTATUS\tREACHABLE\tHEARTBEAT")
	for _, n := range resp.Nodes {
		name := n.Name
		if name == "" {
			name = shortID(n.NodeID)
		}

		reachStr := "no"
		if n.Reachable {
			reachStr = "yes"
		}

		hbStr := "never"
		if n.HeartbeatAge >= 0 {
			hbStr = formatAge(n.HeartbeatAge)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, n.Status, reachStr, hbStr)
	}
	_ = w.Flush()

	fmt.Printf("\n%d nodes  |  %d reachable  |  %d unreachable\n",
		resp.TotalNodes, resp.Reachable, resp.Unreachable)
}

// formatAge formats a seconds-since-heartbeat value into a human-readable string.
func formatAge(secs float64) string {
	if secs < 0 {
		return "never"
	}
	d := time.Duration(math.Round(secs)) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

// runHealthWait polls the health endpoint every 2s until all targeted nodes
// show reachable=true, or until timeout expires.
func runHealthWait(sel selector.SelectorSet, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	tick := 2 * time.Second

	fmt.Fprintf(os.Stderr, "Waiting for nodes to become reachable (timeout: %s)...\n", timeout)

	for {
		resp, err := fetchHealthSel(sel)
		if err != nil {
			return fmt.Errorf("health wait: %w", err)
		}

		if resp.Unreachable == 0 && resp.TotalNodes > 0 {
			fmt.Fprintf(os.Stderr, "All %d node(s) reachable.\n", resp.TotalNodes)
			printHealthTable(resp)
			return nil
		}

		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "Timeout: %d/%d node(s) still unreachable.\n",
				resp.Unreachable, resp.TotalNodes)
			printHealthTable(resp)
			return fmt.Errorf("health wait: timed out after %s", timeout)
		}

		unreachableNames := make([]string, 0, resp.Unreachable)
		for _, n := range resp.Nodes {
			if !n.Reachable {
				name := n.Name
				if name == "" {
					name = shortID(n.NodeID)
				}
				unreachableNames = append(unreachableNames, name)
			}
		}
		fmt.Fprintf(os.Stderr, "  %d/%d unreachable: %s — retrying in %s\n",
			resp.Unreachable, resp.TotalNodes,
			joinNames(unreachableNames, 5),
			tick,
		)

		time.Sleep(tick)
	}
}

// joinNames returns the first n names joined by commas, with a "+N more" suffix.
func joinNames(names []string, max int) string {
	if len(names) == 0 {
		return ""
	}
	shown := names
	extra := 0
	if len(names) > max {
		shown = names[:max]
		extra = len(names) - max
	}
	s := ""
	for i, n := range shown {
		if i > 0 {
			s += ", "
		}
		s += n
	}
	if extra > 0 {
		s += fmt.Sprintf(" +%d more", extra)
	}
	return s
}
