package main

// ipmi_sel.go — `clustr ipmi sel` subcommands (#129)
//
// Subcommands:
//   clustr ipmi sel list   -n NODE
//   clustr ipmi sel clear  -n NODE [-y]
//   clustr ipmi sel head N -n NODE
//   clustr ipmi sel tail N -n NODE
//   clustr ipmi sel filter -n NODE [--level info|warn|critical]
//
// These commands talk to clustr-serverd via the REST API, which in turn
// reaches the node's BMC via its configured IPMI client.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// SEL API response types — mirrors internal/server/handlers/ipmi.go.
// Defined locally to avoid importing the handlers package from the CLI.
type selEntry struct {
	ID       string    `json:"id"`
	Date     string    `json:"date"`
	Time     string    `json:"time"`
	Sensor   string    `json:"sensor"`
	Event    string    `json:"event"`
	Severity string    `json:"severity"`
	Raw      string    `json:"raw"`
	Parsed   time.Time `json:"timestamp"`
}

type selResponse struct {
	NodeID      string     `json:"node_id"`
	Entries     []selEntry `json:"entries"`
	LastChecked time.Time  `json:"last_checked"`
}

// newIPMISELCmd returns the `clustr ipmi sel` group.
func newIPMISELCmd() *cobra.Command {
	selCmd := &cobra.Command{
		Use:   "sel",
		Short: "System Event Log (SEL) operations",
		Long: `Read, filter, and clear the IPMI System Event Log (SEL) for a node.

The node must have a BMC/IPMI provider configured in clustr-serverd.
Pass the node ID via -n.`,
	}
	selCmd.AddCommand(
		newIPMISELListCmd(),
		newIPMISELClearCmd(),
		newIPMISELHeadCmd(),
		newIPMISELTailCmd(),
		newIPMISELFilterCmd(),
	)
	return selCmd
}

// ─── sel list ─────────────────────────────────────────────────────────────────

func newIPMISELListCmd() *cobra.Command {
	var flagNode string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all SEL entries for a node",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagNode == "" {
				return fmt.Errorf("-n / --node is required")
			}
			resp, err := fetchSEL(flagNode, "", 0, 0)
			if err != nil {
				return err
			}
			printSEL(resp.Entries)
			return nil
		},
	}
	cmd.Flags().StringVarP(&flagNode, "node", "n", "", "Node ID (required)")
	return cmd
}

// ─── sel clear ────────────────────────────────────────────────────────────────

func newIPMISELClearCmd() *cobra.Command {
	var flagNode string
	var flagYes bool

	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear the SEL on a node",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagNode == "" {
				return fmt.Errorf("-n / --node is required")
			}

			if !flagYes {
				fmt.Fprintf(os.Stderr, "Clear SEL on node %s? [y/N]: ", flagNode)
				s := bufio.NewScanner(os.Stdin)
				s.Scan()
				answer := strings.TrimSpace(strings.ToLower(s.Text()))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(os.Stderr, "Aborted.")
					return nil
				}
			}

			ctx := context.Background()
			c := clientFromFlags()
			var result map[string]interface{}
			if err := c.PostJSON(ctx, "/api/v1/nodes/"+flagNode+"/sel/clear", nil, &result); err != nil {
				return fmt.Errorf("sel clear: %w", err)
			}
			fmt.Printf("SEL cleared on node %s.\n", flagNode)
			return nil
		},
	}
	cmd.Flags().StringVarP(&flagNode, "node", "n", "", "Node ID (required)")
	cmd.Flags().BoolVarP(&flagYes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

// ─── sel head ─────────────────────────────────────────────────────────────────

func newIPMISELHeadCmd() *cobra.Command {
	var flagNode string

	cmd := &cobra.Command{
		Use:   "head [N]",
		Short: "Show the first N SEL entries (default 10)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagNode == "" {
				return fmt.Errorf("-n / --node is required")
			}
			n := 10
			if len(args) == 1 {
				var err error
				n, err = strconv.Atoi(args[0])
				if err != nil || n <= 0 {
					return fmt.Errorf("count must be a positive integer, got %q", args[0])
				}
			}
			resp, err := fetchSEL(flagNode, "", n, 0)
			if err != nil {
				return err
			}
			printSEL(resp.Entries)
			return nil
		},
	}
	cmd.Flags().StringVarP(&flagNode, "node", "n", "", "Node ID (required)")
	return cmd
}

// ─── sel tail ─────────────────────────────────────────────────────────────────

func newIPMISELTailCmd() *cobra.Command {
	var flagNode string

	cmd := &cobra.Command{
		Use:   "tail [N]",
		Short: "Show the last N SEL entries (default 10)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagNode == "" {
				return fmt.Errorf("-n / --node is required")
			}
			n := 10
			if len(args) == 1 {
				var err error
				n, err = strconv.Atoi(args[0])
				if err != nil || n <= 0 {
					return fmt.Errorf("count must be a positive integer, got %q", args[0])
				}
			}
			resp, err := fetchSEL(flagNode, "", 0, n)
			if err != nil {
				return err
			}
			printSEL(resp.Entries)
			return nil
		},
	}
	cmd.Flags().StringVarP(&flagNode, "node", "n", "", "Node ID (required)")
	return cmd
}

// ─── sel filter ───────────────────────────────────────────────────────────────

func newIPMISELFilterCmd() *cobra.Command {
	var flagNode string
	var flagLevel string

	cmd := &cobra.Command{
		Use:   "filter",
		Short: "Filter SEL entries by severity level",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagNode == "" {
				return fmt.Errorf("-n / --node is required")
			}
			resp, err := fetchSEL(flagNode, flagLevel, 0, 0)
			if err != nil {
				return err
			}
			if len(resp.Entries) == 0 {
				fmt.Printf("No SEL entries at level %q on node %s.\n", flagLevel, flagNode)
				return nil
			}
			printSEL(resp.Entries)
			return nil
		},
	}
	cmd.Flags().StringVarP(&flagNode, "node", "n", "", "Node ID (required)")
	cmd.Flags().StringVar(&flagLevel, "level", "critical", "Minimum severity: info, warn, critical")
	return cmd
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// fetchSEL calls GET /api/v1/nodes/{id}/sel with optional filter params.
func fetchSEL(nodeID, level string, head, tail int) (*selResponse, error) {
	ctx := context.Background()
	c := clientFromFlags()

	path := "/api/v1/nodes/" + nodeID + "/sel"
	sep := "?"
	addParam := func(k, v string) {
		path += sep + k + "=" + v
		sep = "&"
	}
	if level != "" {
		addParam("level", level)
	}
	if head > 0 {
		addParam("head", strconv.Itoa(head))
	} else if tail > 0 {
		addParam("tail", strconv.Itoa(tail))
	}

	var resp selResponse
	if err := c.GetJSON(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("ipmi sel: %w", err)
	}
	return &resp, nil
}

// printSEL writes a tabular view of SEL entries to stdout.
// Format: ID  DATE        TIME      SENSOR                     EVENT                             SEVERITY
func printSEL(entries []selEntry) {
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
