// cmd/clustr/ipmi_admin.go — Sprint 34 IPMI-MIN node-targeted CLI.
//
// New shape: `clustr ipmi node <id> {power,sel,sensors}` calls the
// admin-scoped /api/v1/nodes/{id}/ipmi/* endpoints federated through
// clustr-privhelper. This is the CLI surface the spec calls for and is
// meant to be the path operators reach for during incident triage.
//
// The existing top-level `clustr ipmi power|sel|sensors` shape (raw BMC
// host/user/pass flags) remains untouched — those bypass the server and
// talk directly to a BMC, useful for one-off pre-enrollment checks.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// nodeIPMIPowerActions is the set of valid first-positional power actions.
var nodeIPMIPowerActions = map[string]bool{
	"status": true, "on": true, "off": true, "cycle": true, "reset": true,
}

// newIPMINodeCmd returns the `clustr ipmi node <id> ...` parent command.
//
// The command tree is:
//
//	clustr ipmi node <id> power {status,on,off,cycle,reset}
//	clustr ipmi node <id> sel {list,clear}
//	clustr ipmi node <id> sensors
func newIPMINodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node <node-id> {power,sel,sensors} ...",
		Short: "IPMI operations on a registered clustr node (admin scope)",
		Long: `Run IPMI power/SEL/sensor operations on a node registered in clustr-serverd.

The credentials and target host come from the node's stored
bmc_config_encrypted record; you do NOT supply --host/--user/--pass.  All
operations route through clustr-privhelper on the server so the BMC
password never appears in argv.

Examples:
  clustr ipmi node compute01 power status
  clustr ipmi node compute01 power cycle
  clustr ipmi node compute01 sel list
  clustr ipmi node compute01 sel clear -y
  clustr ipmi node compute01 sensors
`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeID := args[0]
			verb := args[1]
			rest := args[2:]
			switch verb {
			case "power":
				if len(rest) != 1 {
					return fmt.Errorf("usage: clustr ipmi node <id> power {status,on,off,cycle,reset}")
				}
				return runIPMINodePower(nodeID, rest[0])
			case "sel":
				return runIPMINodeSEL(nodeID, rest)
			case "sensors":
				if len(rest) != 0 {
					return fmt.Errorf("usage: clustr ipmi node <id> sensors")
				}
				return runIPMINodeSensors(nodeID)
			default:
				return fmt.Errorf("unknown ipmi verb %q (allowed: power, sel, sensors)", verb)
			}
		},
	}
	return cmd
}

// runIPMINodePower POSTs /api/v1/nodes/{id}/ipmi/power/{action}.
func runIPMINodePower(nodeID, action string) error {
	if !nodeIPMIPowerActions[action] {
		return fmt.Errorf("invalid power action %q (allowed: status, on, off, cycle, reset)", action)
	}
	c := clientFromFlags()
	var resp ipmiPowerActionResponse
	path := fmt.Sprintf("/api/v1/nodes/%s/ipmi/power/%s", nodeID, action)
	if err := c.PostJSON(context.Background(), path, nil, &resp); err != nil {
		return fmt.Errorf("ipmi power %s: %w", action, err)
	}
	if action == "status" {
		fmt.Printf("Power: %s\n", resp.Output)
	} else {
		fmt.Printf("ipmi-power %s: %s\n", action, resp.Output)
	}
	return nil
}

// runIPMINodeSEL handles the sel list / sel clear forms.
func runIPMINodeSEL(nodeID string, rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("usage: clustr ipmi node <id> sel {list,clear}")
	}
	op := rest[0]
	c := clientFromFlags()
	path := fmt.Sprintf("/api/v1/nodes/%s/ipmi/sel", nodeID)
	switch op {
	case "list":
		var resp ipmiSELResponse
		if err := c.GetJSON(context.Background(), path, &resp); err != nil {
			return fmt.Errorf("ipmi sel list: %w", err)
		}
		printIPMISELEntries(resp.Entries)
		return nil
	case "clear":
		yes := false
		for _, a := range rest[1:] {
			if a == "-y" || a == "--yes" {
				yes = true
				break
			}
		}
		if !yes {
			fmt.Fprintf(os.Stderr, "Clear SEL on node %s? [y/N]: ", nodeID)
			var ans string
			_, _ = fmt.Scanln(&ans)
			ans = strings.ToLower(strings.TrimSpace(ans))
			if ans != "y" && ans != "yes" {
				fmt.Fprintln(os.Stderr, "Aborted.")
				return nil
			}
		}
		if err := c.DeleteJSON(context.Background(), path); err != nil {
			return fmt.Errorf("ipmi sel clear: %w", err)
		}
		fmt.Printf("SEL cleared on node %s.\n", nodeID)
		return nil
	}
	return fmt.Errorf("unknown sel op %q (allowed: list, clear)", op)
}

// runIPMINodeSensors GETs /api/v1/nodes/{id}/ipmi/sensors and prints a
// table.
func runIPMINodeSensors(nodeID string) error {
	c := clientFromFlags()
	var resp ipmiSensorsResponse
	path := fmt.Sprintf("/api/v1/nodes/%s/ipmi/sensors", nodeID)
	if err := c.GetJSON(context.Background(), path, &resp); err != nil {
		return fmt.Errorf("ipmi sensors: %w", err)
	}
	if len(resp.Sensors) == 0 {
		fmt.Println("No sensor data available.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SENSOR\tVALUE\tUNITS\tSTATUS")
	for _, s := range resp.Sensors {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Value, s.Units, s.Status)
	}
	return w.Flush()
}

// printIPMISELEntries renders the SEL entries as a table mirroring the
// existing `clustr ipmi sel` output shape.
func printIPMISELEntries(entries []ipmiSELEntry) {
	if len(entries) == 0 {
		fmt.Println("SEL has no entries.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDATE\tTIME\tSENSOR\tEVENT\tSEVERITY")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.ID, e.Date, e.Time, e.Sensor, e.Event, e.Severity)
	}
	_ = w.Flush()
}

// ─── Wire types (mirror handler responses) ────────────────────────────────────

// ipmiPowerActionResponse mirrors handlers.IPMIPowerActionResponse.
type ipmiPowerActionResponse struct {
	NodeID string `json:"node_id"`
	Action string `json:"action"`
	Output string `json:"output"`
	OK     bool   `json:"ok"`
}

// ipmiSensorEntry mirrors ipmi.Sensor.
type ipmiSensorEntry struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Units  string `json:"units"`
	Status string `json:"status"`
}

// ipmiSensorsResponse mirrors handlers.IPMISensorsResponse.
type ipmiSensorsResponse struct {
	NodeID      string            `json:"node_id"`
	Sensors     []ipmiSensorEntry `json:"sensors"`
	LastChecked time.Time         `json:"last_checked"`
}

// ipmiSELEntry mirrors ipmi.SELEntry.
type ipmiSELEntry struct {
	ID       string `json:"id"`
	Date     string `json:"date"`
	Time     string `json:"time"`
	Sensor   string `json:"sensor"`
	Event    string `json:"event"`
	Severity string `json:"severity"`
}

// ipmiSELResponse mirrors handlers.IPMISELResponse.
type ipmiSELResponse struct {
	NodeID      string         `json:"node_id"`
	Entries     []ipmiSELEntry `json:"entries"`
	LastChecked time.Time      `json:"last_checked"`
}
