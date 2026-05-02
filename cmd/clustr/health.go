package main

// health.go — `clustr health` command (#130)
//
// Usage:
//   clustr health [-n NODE | -A] [--summary | --ping | --wait [--timeout DUR]]
//
// Displays per-node reachability, heartbeat age, and status summary.
//
// TODO(#125): replace the -n / -A local flag parsing with selector.RegisterSelectorFlags
// once the selector grammar package lands.

import (
	"context"
	"fmt"
	"math"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
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
		// TODO(#125): replace -n / -A with selector.RegisterSelectorFlags(cmd)
		// once the selector grammar package (internal/selector/) lands.
		flagNode    string
		flagAll     bool
		flagSummary bool
		flagPing    bool
		flagWait    bool
		flagTimeout string
	)

	cmd := &cobra.Command{
		Use:   "health",
		Short: "Aggregate per-node reachability and status summary",
		Long: `health shows the reachability and heartbeat state for nodes managed by this cluster.

By default (--summary) it prints a table: NODE / STATUS / REACHABLE / HEARTBEAT AGE.

--ping actively requests a heartbeat from each target node's clientd daemon and
reports the round-trip latency. (Not yet implemented in Sprint 21; shown as N/A.)

--wait polls every 2s until all targeted nodes show reachable=true, or until
--timeout (default 5m) expires.

Selector flags (minimal, pre-#125):
  -n  single node by ID or hostname
  -A  all registered nodes (default when neither -n nor -A is provided)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve timeout.
			timeout := 5 * time.Minute
			if flagTimeout != "" {
				d, err := time.ParseDuration(flagTimeout)
				if err != nil {
					return fmt.Errorf("--timeout: expected a duration like 5m, 30s: %w", err)
				}
				timeout = d
			}

			if flagWait {
				return runHealthWait(flagNode, timeout)
			}

			resp, err := fetchHealth(flagNode, flagAll)
			if err != nil {
				return err
			}

			if flagPing {
				// Ping round-trip not yet available in Sprint 21; show data with a note.
				fmt.Fprintln(os.Stderr, "note: --ping shows last-known heartbeat age (active ping not yet available)")
			}

			printHealthTable(resp)
			return nil
		},
	}

	// TODO(#125): replace with selector.RegisterSelectorFlags(cmd)
	cmd.Flags().StringVarP(&flagNode, "node", "n", "", "Target a single node by ID or hostname")
	cmd.Flags().BoolVarP(&flagAll, "all", "A", false, "Target all registered nodes (default)")
	cmd.Flags().BoolVar(&flagSummary, "summary", true, "Show summary table (default)")
	cmd.Flags().BoolVar(&flagPing, "ping", false, "Request active heartbeat ping from each node's clientd")
	cmd.Flags().BoolVar(&flagWait, "wait", false, "Poll until all targets are reachable")
	cmd.Flags().StringVar(&flagTimeout, "timeout", "5m", "Timeout for --wait (e.g. 5m, 30s)")

	return cmd
}

// fetchHealth calls GET /api/v1/cluster/health or GET /api/v1/nodes/{id}/health.
func fetchHealth(nodeID string, all bool) (*cliNodeHealthResponse, error) {
	ctx := context.Background()
	c := clientFromFlags()

	var path string
	if nodeID != "" {
		// Single-node path.
		path = "/api/v1/nodes/" + nodeID + "/health"
	} else {
		// Cluster-wide path (all nodes).
		path = "/api/v1/cluster/health"
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
func runHealthWait(nodeID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	tick := 2 * time.Second

	fmt.Fprintf(os.Stderr, "Waiting for nodes to become reachable (timeout: %s)...\n", timeout)

	for {
		resp, err := fetchHealth(nodeID, nodeID == "")
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
