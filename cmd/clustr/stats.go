package main

// stats.go — `clustr stats` command (#134)
//
// Usage:
//
//	clustr stats -n NODE [-s SENSOR_REGEX] [--since DUR] [--until DUR] [--json]
//
// Fetches per-plugin metric samples for a single node and prints a table:
//
//	TIME  PLUGIN  SENSOR  VALUE  UNIT
//
// The -n selector uses the standard selector grammar (#125) but enforces
// single-node: if the resolved set has more than one node the command errors.
//
// Out of scope (Sprint 23+): real-time follow (-f), graphical sparklines,
// multi-node aggregation.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/sqoia-dev/clustr/internal/selector"
)

// cliStatSample mirrors the stats API response element.
type cliStatSample struct {
	Plugin string            `json:"plugin"`
	Sensor string            `json:"sensor"`
	Value  float64           `json:"value"`
	Unit   string            `json:"unit,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	TS     int64             `json:"ts"` // Unix seconds
}

func newStatsCmd() *cobra.Command {
	var (
		sel        selector.SelectorSet
		flagSensor string
		flagSince  string
		flagUntil  string
		flagJSON   bool
	)

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show per-plugin metric samples for a node",
		Long: `stats fetches metric samples for a single cluster node and prints a
TIME / PLUGIN / SENSOR / VALUE / UNIT table.

The node is identified via the standard selector grammar (#125).  Only one
node may be selected; use a specific hostname with -n to avoid ambiguity.

Time range flags accept Go duration strings (1h, 15m, 30s).  Both are
relative to now: --since 1h means "start 1 hour ago", --until 0s means "now".

Sensor filter (-s) accepts a Go regular expression matched against the
sensor name.  The filter is applied client-side after the server returns
results.

Out of scope for this release: real-time follow (-f), sparkline graphs,
multi-node aggregation.  These land in Sprint 23.

Examples:
  clustr stats -n node01
  clustr stats -n node01 -s "cpu.*" --since 15m
  clustr stats -n node01 --since 30m --until 5m --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sel.IsEmpty() {
				return fmt.Errorf("at least one selector required (-n, -g, -A, -a)")
			}

			// Resolve the selector to node IDs via the health endpoint.
			// Single-node enforcement is done client-side after resolution.
			nodeID, err := resolveStatsNode(sel)
			if err != nil {
				return err
			}

			// Parse time range durations.
			now := time.Now()
			since := now.Add(-time.Hour) // default: 1h ago
			until := now                 // default: now

			if flagSince != "" {
				d, err := time.ParseDuration(flagSince)
				if err != nil {
					return fmt.Errorf("--since: expected a duration like 1h, 15m, 30s: %w", err)
				}
				since = now.Add(-d)
			}
			if flagUntil != "" {
				d, err := time.ParseDuration(flagUntil)
				if err != nil {
					return fmt.Errorf("--until: expected a duration like 0s, 5m: %w", err)
				}
				until = now.Add(-d)
			}

			if since.After(until) {
				return fmt.Errorf("--since window is after --until; nothing to query")
			}

			// Compile sensor regex.
			sensorRE := (*regexp.Regexp)(nil)
			if flagSensor != "" && flagSensor != ".*" {
				re, err := regexp.Compile(flagSensor)
				if err != nil {
					return fmt.Errorf("-s / --sensor: invalid regex: %w", err)
				}
				sensorRE = re
			}

			return runStats(nodeID, sensorRE, since, until, flagJSON)
		},
	}

	selector.RegisterSelectorFlags(cmd, &sel)
	cmd.Flags().StringVarP(&flagSensor, "sensor", "s", ".*", "Sensor name regex filter")
	cmd.Flags().StringVar(&flagSince, "since", "1h", "Start of time range relative to now (e.g. 1h, 15m)")
	cmd.Flags().StringVar(&flagUntil, "until", "0s", "End of time range relative to now (e.g. 0s, 5m)")
	cmd.Flags().BoolVar(&flagJSON, "json", false, "Output raw API JSON")

	return cmd
}

// resolveStatsNode uses the health endpoint to resolve a SelectorSet to a
// single node ID.  Returns an error if the selector resolves to zero or more
// than one node.
func resolveStatsNode(sel selector.SelectorSet) (string, error) {
	ctx := context.Background()
	c := clientFromFlags()

	// Build query string from selector fields — mirrors fetchHealthSel in health.go.
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
		return "", fmt.Errorf("stats: resolve node: %w", err)
	}

	if len(resp.Nodes) == 0 {
		return "", fmt.Errorf("stats: selector matched no nodes")
	}
	if len(resp.Nodes) > 1 {
		return "", fmt.Errorf("clustr stats v1: single-node only; got %d nodes", len(resp.Nodes))
	}

	return resp.Nodes[0].NodeID, nil
}

// runStats fetches stats for nodeID and renders the output table.
func runStats(nodeID string, sensorRE *regexp.Regexp, since, until time.Time, jsonOut bool) error {
	ctx := context.Background()
	c := clientFromFlags()

	q := url.Values{}
	q.Set("since", strconv.FormatInt(since.Unix(), 10))
	q.Set("until", strconv.FormatInt(until.Unix(), 10))

	path := "/api/v1/nodes/" + nodeID + "/stats?" + q.Encode()

	var rows []cliStatSample
	if err := c.GetJSON(ctx, path, &rows); err != nil {
		return fmt.Errorf("stats: %w", err)
	}

	// Apply client-side sensor filter.
	if sensorRE != nil {
		filtered := rows[:0]
		for _, r := range rows {
			if sensorRE.MatchString(r.Sensor) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	if jsonOut {
		return encodeJSON(rows)
	}

	if len(rows) == 0 {
		fmt.Println("No stats found for the given time range and sensor filter.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tPLUGIN\tSENSOR\tVALUE\tUNIT")
	for _, r := range rows {
		t := time.Unix(r.TS, 0).UTC().Format(time.RFC3339)
		fmt.Fprintf(w, "%s\t%s\t%s\t%.6g\t%s\n",
			t,
			r.Plugin,
			sensorWithLabels(r.Sensor, r.Labels),
			r.Value,
			r.Unit,
		)
	}
	return w.Flush()
}
