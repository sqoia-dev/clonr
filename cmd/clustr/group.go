package main

// group.go — clustr group subcommand: node group CRUD, membership management,
// and group-targeted rolling reimage.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/sqoia-dev/clustr/pkg/api"
)

func init() {
	groupCmd := &cobra.Command{
		Use:   "group",
		Short: "Manage node groups (bulk targeting for rolling reimage)",
	}
	groupCmd.AddCommand(
		newGroupListCmd(),
		newGroupCreateCmd(),
		newGroupShowCmd(),
		newGroupAddMemberCmd(),
		newGroupRemoveMemberCmd(),
		newGroupReimageCmd(),
		newGroupJobCmd(),
		newGroupDeleteCmd(),
	)
	rootCmd.AddCommand(groupCmd)
}

// ─── group list ──────────────────────────────────────────────────────────────

func newGroupListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all node groups with member counts",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			var resp api.ListNodeGroupsResponse
			if err := c.GetJSON(ctx, "/api/v1/node-groups", &resp); err != nil {
				return fmt.Errorf("list groups: %w", err)
			}

			groups := resp.Groups
			if len(groups) == 0 {
				fmt.Println("No node groups defined.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tROLE\tMEMBERS\tDESCRIPTION\tUPDATED")
			for _, g := range groups {
				role := g.Role
				if role == "" {
					role = "—"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
					shortID(g.ID),
					g.Name,
					role,
					g.MemberCount,
					truncate(g.Description, 40),
					g.UpdatedAt.Format("2006-01-02 15:04"),
				)
			}
			return w.Flush()
		},
	}
}

// ─── group create ─────────────────────────────────────────────────────────────

func newGroupCreateCmd() *cobra.Command {
	var (
		flagRole        string
		flagDescription string
	)
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a node group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			body := api.CreateNodeGroupRequest{
				Name:        args[0],
				Description: flagDescription,
				Role:        flagRole,
			}
			var g api.NodeGroup
			if err := c.PostJSON(ctx, "/api/v1/node-groups", body, &g); err != nil {
				return fmt.Errorf("create group: %w", err)
			}
			fmt.Printf("Group created: %s (%s)\n", g.Name, shortID(g.ID))
			return nil
		},
	}
	cmd.Flags().StringVar(&flagRole, "role", "", "HPC role: compute, login, storage, gpu, admin")
	cmd.Flags().StringVar(&flagDescription, "description", "", "Human-readable description")
	return cmd
}

// ─── group show ──────────────────────────────────────────────────────────────

func newGroupShowCmd() *cobra.Command {
	var flagJSON bool
	cmd := &cobra.Command{
		Use:   "show <id|name>",
		Short: "Show group detail including members",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			id, err := resolveGroupID(ctx, c, args[0])
			if err != nil {
				return err
			}

			var resp api.GroupMembersResponse
			if err := c.GetJSON(ctx, "/api/v1/node-groups/"+id, &resp); err != nil {
				return fmt.Errorf("get group: %w", err)
			}

			if flagJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}

			g := resp.Group
			fmt.Printf("Group:       %s\n", g.Name)
			fmt.Printf("ID:          %s\n", g.ID)
			if g.Role != "" {
				fmt.Printf("Role:        %s\n", g.Role)
			}
			if g.Description != "" {
				fmt.Printf("Description: %s\n", g.Description)
			}
			fmt.Printf("Created:     %s\n", g.CreatedAt.Format(time.RFC3339))
			fmt.Printf("Updated:     %s\n", g.UpdatedAt.Format(time.RFC3339))
			fmt.Printf("Members:     %d\n", len(resp.Members))

			if len(resp.Members) > 0 {
				fmt.Println()
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tHOSTNAME\tMAC\tSTATUS")
				for _, n := range resp.Members {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
						shortID(n.ID),
						n.Hostname,
						n.PrimaryMAC,
						string(n.State()),
					)
				}
				w.Flush()
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&flagJSON, "json", false, "Output as JSON")
	return cmd
}

// ─── group add-member ────────────────────────────────────────────────────────

func newGroupAddMemberCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add-member <group-id|name> <node-id|hostname>",
		Short: "Add a node to a group (idempotent)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			groupID, err := resolveGroupID(ctx, c, args[0])
			if err != nil {
				return err
			}
			nodeID, err := resolveNodeID(ctx, c, args[1])
			if err != nil {
				return err
			}

			body := api.AddGroupMembersRequest{NodeIDs: []string{nodeID}}
			var resp api.GroupMembersResponse
			if err := c.PostJSON(ctx, "/api/v1/node-groups/"+groupID+"/members", body, &resp); err != nil {
				return fmt.Errorf("add member: %w", err)
			}
			fmt.Printf("Node %s added to group %s (%d members total)\n",
				args[1], resp.Group.Name, len(resp.Members))
			return nil
		},
	}
}

// ─── group remove-member ──────────────────────────────────────────────────────

func newGroupRemoveMemberCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove-member <group-id|name> <node-id|hostname>",
		Short: "Remove a node from a group",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			groupID, err := resolveGroupID(ctx, c, args[0])
			if err != nil {
				return err
			}
			nodeID, err := resolveNodeID(ctx, c, args[1])
			if err != nil {
				return err
			}

			if err := c.DeleteJSON(ctx, "/api/v1/node-groups/"+groupID+"/members/"+nodeID); err != nil {
				return fmt.Errorf("remove member: %w", err)
			}
			fmt.Printf("Node %s removed from group.\n", args[1])
			return nil
		},
	}
}

// ─── group reimage ────────────────────────────────────────────────────────────

func newGroupReimageCmd() *cobra.Command {
	var (
		flagImage           string
		flagConcurrency     int
		flagPauseOnFailPct  int
	)
	cmd := &cobra.Command{
		Use:   "reimage <group-id|name>",
		Short: "Rolling reimage all nodes in a group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagImage == "" {
				return fmt.Errorf("--image is required")
			}

			ctx := context.Background()
			c := clientFromFlags()

			groupID, err := resolveGroupID(ctx, c, args[0])
			if err != nil {
				return err
			}

			body := api.GroupReimageRequest{
				ImageID:           flagImage,
				Concurrency:       flagConcurrency,
				PauseOnFailurePct: flagPauseOnFailPct,
			}
			var status api.GroupReimageJobStatus
			if err := c.PostJSON(ctx, "/api/v1/node-groups/"+groupID+"/reimage", body, &status); err != nil {
				return fmt.Errorf("reimage group: %w", err)
			}

			fmt.Printf("Group reimage started.\n")
			fmt.Printf("Job ID:       %s\n", status.JobID)
			fmt.Printf("Group:        %s\n", status.GroupID)
			fmt.Printf("Image:        %s\n", shortID(status.ImageID))
			fmt.Printf("Total nodes:  %d\n", status.TotalNodes)
			fmt.Printf("Concurrency:  %d\n", status.Concurrency)
			fmt.Printf("Pause %%fail:  %d%%\n", status.PauseOnFailurePct)
			fmt.Printf("Status:       %s\n", status.Status)
			fmt.Printf("\nPoll status:  clustr group job %s\n", status.JobID)
			return nil
		},
	}
	cmd.Flags().StringVar(&flagImage, "image", "", "Image ID to deploy (required)")
	cmd.Flags().IntVar(&flagConcurrency, "concurrency", 5, "Max nodes reimaged simultaneously")
	cmd.Flags().IntVar(&flagPauseOnFailPct, "pause-on-failure-pct", 20, "Pause rollout if this %% of a wave fails")
	return cmd
}

// ─── group job ────────────────────────────────────────────────────────────────

func newGroupJobCmd() *cobra.Command {
	var flagJSON bool
	cmd := &cobra.Command{
		Use:   "job <job-id>",
		Short: "Poll the status of a rolling group reimage job",
		Long: `job fetches the current status of a group reimage job by ID.

The job ID is printed by 'clustr group reimage' when the rolling reimage is
initiated. Use this command to monitor progress.

Example:
  clustr group reimage compute --image abc123
  clustr group job <job-id>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			var status api.GroupReimageJobStatus
			if err := c.GetJSON(ctx, "/api/v1/reimages/jobs/"+args[0], &status); err != nil {
				return fmt.Errorf("group job: %w", err)
			}

			if flagJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(status)
			}

			fmt.Printf("Job ID:        %s\n", status.JobID)
			fmt.Printf("Status:        %s\n", status.Status)
			fmt.Printf("Group:         %s\n", shortID(status.GroupID))
			fmt.Printf("Image:         %s\n", shortID(status.ImageID))
			fmt.Printf("Total nodes:   %d\n", status.TotalNodes)
			fmt.Printf("Triggered:     %d\n", status.TriggeredNodes)
			fmt.Printf("Succeeded:     %d\n", status.SucceededNodes)
			fmt.Printf("Failed:        %d\n", status.FailedNodes)
			fmt.Printf("Concurrency:   %d\n", status.Concurrency)
			if status.ErrorMessage != "" {
				fmt.Printf("Error:         %s\n", status.ErrorMessage)
			}
			fmt.Printf("Updated:       %s\n", status.UpdatedAt.UTC().Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().BoolVar(&flagJSON, "json", false, "Output as JSON")
	return cmd
}

// ─── group delete ─────────────────────────────────────────────────────────────

func newGroupDeleteCmd() *cobra.Command {
	var flagForce bool
	cmd := &cobra.Command{
		Use:   "delete <id|name>",
		Short: "Delete a node group (removes membership; does not delete nodes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			groupID, err := resolveGroupID(ctx, c, args[0])
			if err != nil {
				return err
			}
			if !flagForce {
				// Show member count as a safety prompt.
				var resp api.GroupMembersResponse
				if getErr := c.GetJSON(ctx, "/api/v1/node-groups/"+groupID, &resp); getErr == nil && len(resp.Members) > 0 {
					fmt.Printf("Group %q has %d member(s). Re-run with --force to confirm deletion.\n",
						resp.Group.Name, len(resp.Members))
					return nil
				}
			}
			if err := c.DeleteJSON(ctx, "/api/v1/node-groups/"+groupID); err != nil {
				return fmt.Errorf("delete group: %w", err)
			}
			fmt.Printf("Group %s deleted.\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&flagForce, "force", false, "Delete even if the group has members")
	return cmd
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// resolveGroupID maps a name or ID to a canonical UUID. It tries the value as-is
// (it may already be a full UUID) and falls back to searching by name.
func resolveGroupID(ctx context.Context, c interface{ GetJSON(context.Context, string, interface{}) error }, nameOrID string) (string, error) {
	// If it looks like a UUID (36 chars with dashes), use it directly.
	if len(nameOrID) == 36 && strings.Count(nameOrID, "-") == 4 {
		return nameOrID, nil
	}
	// Otherwise list all groups and match by name or short ID prefix.
	var resp api.ListNodeGroupsResponse
	if err := c.GetJSON(ctx, "/api/v1/node-groups", &resp); err != nil {
		return "", fmt.Errorf("resolve group: %w", err)
	}
	for _, g := range resp.Groups {
		if g.Name == nameOrID || strings.HasPrefix(g.ID, nameOrID) {
			return g.ID, nil
		}
	}
	return "", fmt.Errorf("group %q not found", nameOrID)
}

// resolveNodeID maps a hostname or node ID to a canonical UUID.
func resolveNodeID(ctx context.Context, c interface{ GetJSON(context.Context, string, interface{}) error }, nameOrID string) (string, error) {
	if len(nameOrID) == 36 && strings.Count(nameOrID, "-") == 4 {
		return nameOrID, nil
	}
	var resp api.ListNodesResponse
	if err := c.GetJSON(ctx, "/api/v1/nodes", &resp); err != nil {
		return "", fmt.Errorf("resolve node: %w", err)
	}
	for _, n := range resp.Nodes {
		if n.Hostname == nameOrID || strings.HasPrefix(n.ID, nameOrID) {
			return n.ID, nil
		}
	}
	return "", fmt.Errorf("node %q not found", nameOrID)
}

// truncate shortens a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}
