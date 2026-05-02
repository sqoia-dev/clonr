package main

// alerts.go — `clustr alerts` command (#134)
//
// Usage:
//
//	clustr alerts [-L | --list]     list active alerts (default)
//	clustr alerts [-S | --state]    alert state machine summary (counts)
//	clustr alerts [-R | --recent]   resolved alerts in the last 24h
//
// Filter flags (applies to -L and -R):
//
//	--severity SLIST   comma-separated severity values (default: all)
//	--node NODE_ID     filter by node
//	--rule RULE_NAME   filter by rule name
//
// Output flags:
//
//	--json             emit raw API JSON instead of the human-readable table

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// cliAlert mirrors the alerts.Alert JSON shape.  Defined locally so the CLI
// does not import internal packages.
type cliAlert struct {
	ID           int64             `json:"id"`
	RuleName     string            `json:"rule_name"`
	NodeID       string            `json:"node_id"`
	Sensor       string            `json:"sensor"`
	Labels       map[string]string `json:"labels,omitempty"`
	Severity     string            `json:"severity"`
	State        string            `json:"state"`
	FiredAt      time.Time         `json:"fired_at"`
	ResolvedAt   *time.Time        `json:"resolved_at,omitempty"`
	LastValue    float64           `json:"last_value"`
	ThresholdOp  string            `json:"threshold_op"`
	ThresholdVal float64           `json:"threshold_val"`
}

// cliAlertsResponse mirrors the alertsResponse JSON envelope from the handler.
type cliAlertsResponse struct {
	Active []cliAlert `json:"active"`
	Recent []cliAlert `json:"recent"`
}

func newAlertsCmd() *cobra.Command {
	var (
		flagList     bool
		flagState    bool
		flagRecent   bool
		flagSeverity string
		flagNode     string
		flagRule     string
		flagJSON     bool
		// stub flags — not yet implemented, Sprint 24
		flagAck     bool
		flagSilence bool
	)

	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "List and inspect cluster alerts",
		Long: `alerts shows active and recently resolved alerts for the cluster.

Modes:
  -L / --list    List active alerts (default).
  -S / --state   Print a count summary by severity and rule.
  -R / --recent  List resolved alerts in the last 24 hours.

Filter flags (applies to -L and -R):
  --severity SLIST   Comma-separated severity values, e.g. warn,critical
  --node NODE_ID     Filter by node ID
  --rule RULE_NAME   Filter by rule name

Silence / acknowledge flow is not yet implemented; it ships in Sprint 24.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub flags — inform the user and exit cleanly.
			if flagAck {
				fmt.Fprintln(os.Stderr, "not yet implemented; coming in Sprint 24")
				return nil
			}
			if flagSilence {
				fmt.Fprintln(os.Stderr, "not yet implemented; coming in Sprint 24")
				return nil
			}

			// Determine mode.  Default is -L when nothing is set.
			modeCount := 0
			if flagList {
				modeCount++
			}
			if flagState {
				modeCount++
			}
			if flagRecent {
				modeCount++
			}
			if modeCount > 1 {
				return fmt.Errorf("only one of -L/--list, -S/--state, -R/--recent may be set at a time")
			}
			if modeCount == 0 {
				flagList = true // default mode
			}

			switch {
			case flagState:
				return runAlertsSummary(flagJSON)
			case flagRecent:
				return runAlertsRecent(flagSeverity, flagNode, flagRule, flagJSON)
			default: // flagList
				return runAlertsList(flagSeverity, flagNode, flagRule, flagJSON)
			}
		},
	}

	cmd.Flags().BoolVarP(&flagList, "list", "L", false, "List active alerts (default mode)")
	cmd.Flags().BoolVarP(&flagState, "state", "S", false, "Show alert state summary (counts by severity and rule)")
	cmd.Flags().BoolVarP(&flagRecent, "recent", "R", false, "Show resolved alerts in the last 24 hours")
	cmd.Flags().StringVar(&flagSeverity, "severity", "", "Comma-separated severity filter, e.g. warn,critical")
	cmd.Flags().StringVar(&flagNode, "node", "", "Filter by node ID")
	cmd.Flags().StringVar(&flagRule, "rule", "", "Filter by rule name")
	cmd.Flags().BoolVar(&flagJSON, "json", false, "Output raw API JSON")
	// Stub flags for Sprint 24.
	cmd.Flags().BoolVar(&flagAck, "ack", false, "Acknowledge an alert (not yet implemented; Sprint 24)")
	cmd.Flags().BoolVar(&flagSilence, "silence", false, "Silence an alert (not yet implemented; Sprint 24)")

	return cmd
}

// fetchAlerts calls GET /api/v1/alerts with the provided filter params.
// When stateFilter is set, only that state bucket is populated in the response.
func fetchAlerts(severityCSV, nodeID, ruleName, stateFilter string) (*cliAlertsResponse, error) {
	ctx := context.Background()
	c := clientFromFlags()

	q := url.Values{}
	if severityCSV != "" {
		q.Set("severity", severityCSV)
	}
	if nodeID != "" {
		q.Set("node", nodeID)
	}
	if ruleName != "" {
		q.Set("rule", ruleName)
	}
	if stateFilter != "" {
		q.Set("state", stateFilter)
	}

	path := "/api/v1/alerts"
	if len(q) > 0 {
		path = path + "?" + q.Encode()
	}

	var resp cliAlertsResponse
	if err := c.GetJSON(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("alerts: %w", err)
	}
	return &resp, nil
}

// runAlertsList fetches and prints active alerts.
func runAlertsList(severityCSV, nodeID, ruleName string, jsonOut bool) error {
	resp, err := fetchAlerts(severityCSV, nodeID, ruleName, "firing")
	if err != nil {
		return err
	}

	if jsonOut {
		return encodeJSON(resp)
	}

	alerts := resp.Active
	if len(alerts) == 0 {
		fmt.Println("No active alerts.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SEVERITY\tRULE\tNODE\tSENSOR\tVALUE\tTHRESHOLD\tFIRED")
	for _, a := range alerts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.4g\t%s%.4g\t%s (%s)\n",
			a.Severity,
			a.RuleName,
			shortID(a.NodeID),
			sensorWithLabels(a.Sensor, a.Labels),
			a.LastValue,
			a.ThresholdOp,
			a.ThresholdVal,
			a.FiredAt.UTC().Format(time.RFC3339),
			alertAge(a.FiredAt),
		)
	}
	return w.Flush()
}

// runAlertsRecent fetches and prints resolved alerts from the last 24h.
func runAlertsRecent(severityCSV, nodeID, ruleName string, jsonOut bool) error {
	resp, err := fetchAlerts(severityCSV, nodeID, ruleName, "resolved")
	if err != nil {
		return err
	}

	if jsonOut {
		return encodeJSON(resp)
	}

	alerts := resp.Recent
	if len(alerts) == 0 {
		fmt.Println("No resolved alerts in the last 24 hours.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SEVERITY\tRULE\tNODE\tSENSOR\tVALUE\tTHRESHOLD\tRESOLVED")
	for _, a := range alerts {
		resolvedStr := "—"
		if a.ResolvedAt != nil {
			resolvedStr = fmt.Sprintf("%s (%s)",
				a.ResolvedAt.UTC().Format(time.RFC3339),
				alertAge(*a.ResolvedAt),
			)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.4g\t%s%.4g\t%s\n",
			a.Severity,
			a.RuleName,
			shortID(a.NodeID),
			sensorWithLabels(a.Sensor, a.Labels),
			a.LastValue,
			a.ThresholdOp,
			a.ThresholdVal,
			resolvedStr,
		)
	}
	return w.Flush()
}

// runAlertsSummary fetches all active alerts and prints counts by severity / rule.
func runAlertsSummary(jsonOut bool) error {
	resp, err := fetchAlerts("", "", "", "firing")
	if err != nil {
		return err
	}

	if jsonOut {
		return encodeJSON(resp)
	}

	alerts := resp.Active
	total := len(alerts)

	bySeverity := make(map[string]int)
	byRule := make(map[string]int)
	for _, a := range alerts {
		bySeverity[a.Severity]++
		byRule[a.RuleName]++
	}

	// Build the severity summary string.
	var parts []string
	for _, sev := range []string{"critical", "warn", "info"} {
		if n := bySeverity[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, sev))
		}
	}
	// Include any unknown severities not in the canonical list.
	for sev, n := range bySeverity {
		if sev != "critical" && sev != "warn" && sev != "info" {
			parts = append(parts, fmt.Sprintf("%d %s", n, sev))
		}
	}

	if total == 0 {
		fmt.Println("Active alerts: 0")
		return nil
	}

	fmt.Printf("Active alerts: %d", total)
	if len(parts) > 0 {
		fmt.Printf(" (%s)", strings.Join(parts, ", "))
	}
	fmt.Println()

	if len(byRule) > 0 {
		fmt.Println("By rule:")
		for rule, n := range byRule {
			fmt.Printf("  %s: %d\n", rule, n)
		}
	}

	return nil
}

// sensorWithLabels formats a sensor name with its label set, e.g. "used_pct{disk=/dev/sda}".
func sensorWithLabels(sensor string, labels map[string]string) string {
	if len(labels) == 0 {
		return sensor
	}
	pairs := make([]string, 0, len(labels))
	for k, v := range labels {
		pairs = append(pairs, k+"="+v)
	}
	return sensor + "{" + strings.Join(pairs, ",") + "}"
}

// alertAge returns a human-readable relative age string for t, e.g. "3h ago".
func alertAge(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

// encodeJSON writes v as indented JSON to stdout.
func encodeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
