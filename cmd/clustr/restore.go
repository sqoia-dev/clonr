package main

// restore.go — Sprint 41 Day 4
//
// clustr restore — plugin backup list and replay.
//
//   clustr restore list --node <node-id> --plugin <name>
//     Calls GET /api/v1/backups and prints available snapshots.
//
//   clustr restore replay <backup-id>
//     Calls POST /api/v1/backups/{id}/restore, polls status, prints result.
//
//   clustr restore replay --pending-id <X>
//     Shortcut: resolves the backup tied to a confirmed dangerous-push ID.
//
// See docs/design/sprint-41-auth-safety.md §5.3.

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// ─── wire types ──────────────────────────────────────────────────────────────

type restoreBackupItem struct {
	ID                     string  `json:"id"`
	NodeID                 string  `json:"node_id"`
	PluginName             string  `json:"plugin_name"`
	BlobPath               string  `json:"blob_path"`
	TakenAt                string  `json:"taken_at"`
	PendingDangerousPushID *string `json:"pending_dangerous_push_id,omitempty"`
}

type restoreBackupListResponse struct {
	Backups []restoreBackupItem `json:"backups"`
	Total   int                 `json:"total"`
}

type restoreInitiateResponse struct {
	JobID    string `json:"job_id"`
	BackupID string `json:"backup_id"`
	NodeID   string `json:"node_id"`
	Plugin   string `json:"plugin"`
	Status   string `json:"status"`
}

type restoreStatusResponse struct {
	JobID     string  `json:"job_id"`
	BackupID  string  `json:"backup_id"`
	NodeID    string  `json:"node_id"`
	Plugin    string  `json:"plugin"`
	Status    string  `json:"status"`
	Error     *string `json:"error,omitempty"`
	StartedAt string  `json:"started_at"`
	DoneAt    *string `json:"done_at,omitempty"`
}

// resolvePendingIDResponse is the wire type for the backup looked up by
// pending dangerous push ID.
type resolvedBackupForPending struct {
	ID                     string  `json:"id"`
	NodeID                 string  `json:"node_id"`
	PluginName             string  `json:"plugin_name"`
	BlobPath               string  `json:"blob_path"`
	TakenAt                string  `json:"taken_at"`
	PendingDangerousPushID *string `json:"pending_dangerous_push_id,omitempty"`
}

// ─── command wiring ──────────────────────────────────────────────────────────

func init() {
	restoreCmd := &cobra.Command{
		Use:   "restore",
		Short: "List and replay plugin config backups",
		Long: `Manage pre-render plugin backup snapshots.

Backups are taken automatically by clustr-serverd before applying any plugin
that declares a BackupSpec (e.g. the sssd plugin). Use 'restore list' to view
available snapshots and 'restore replay' to roll back to a previous state.`,
	}

	restoreCmd.AddCommand(newRestoreListCmd())
	restoreCmd.AddCommand(newRestoreReplayCmd())

	rootCmd.AddCommand(restoreCmd)
}

// ─── restore list ─────────────────────────────────────────────────────────────

func newRestoreListCmd() *cobra.Command {
	var (
		flagNodeID     string
		flagPlugin     string
		flagOutputJSON bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available backup snapshots",
		Long: `List plugin backup snapshots for a node/plugin pair.

Snapshots are sorted newest first. The "pending_push" column shows the
dangerous-push ID the snapshot was tied to (if any) — use that ID with
'clustr restore replay --pending-id <X>' to roll back a specific push.`,
		Example: `  clustr restore list --node <node-id> --plugin sssd
  clustr restore list --node <node-id>
  clustr restore list --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestoreList(flagNodeID, flagPlugin, flagOutputJSON)
		},
	}
	cmd.Flags().StringVar(&flagNodeID, "node", "", "Node ID to filter by")
	cmd.Flags().StringVar(&flagPlugin, "plugin", "", "Plugin name to filter by (e.g. sssd)")
	cmd.Flags().BoolVar(&flagOutputJSON, "output-json", false, "Output as JSON")
	return cmd
}

func runRestoreList(nodeID, pluginName string, outputJSON bool) error {
	ctx := context.Background()
	c := clientFromFlags()

	url := "/api/v1/backups"
	sep := "?"
	if nodeID != "" {
		url += sep + "node_id=" + nodeID
		sep = "&"
	}
	if pluginName != "" {
		url += sep + "plugin=" + pluginName
		sep = "&"
	}
	_ = sep

	var resp restoreBackupListResponse
	if err := c.GetJSON(ctx, url, &resp); err != nil {
		return fmt.Errorf("restore list: %w", err)
	}

	if outputJSON {
		enc := json.NewEncoder(rootCmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	if len(resp.Backups) == 0 {
		fmt.Fprintln(rootCmd.OutOrStdout(), "No backups found.")
		return nil
	}

	tw := tabwriter.NewWriter(rootCmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPLUGIN\tNODE\tTAKEN AT\tPENDING PUSH")
	for _, b := range resp.Backups {
		pendingPush := "-"
		if b.PendingDangerousPushID != nil {
			pendingPush = *b.PendingDangerousPushID
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			b.ID,
			b.PluginName,
			b.NodeID,
			b.TakenAt,
			pendingPush,
		)
	}
	return tw.Flush()
}

// ─── restore replay ──────────────────────────────────────────────────────────

func newRestoreReplayCmd() *cobra.Command {
	var (
		flagPendingID  string
		flagOutputJSON bool
		flagTimeout    int
	)
	cmd := &cobra.Command{
		Use:   "replay [backup-id]",
		Short: "Restore a node to a previous plugin config snapshot",
		Long: `Roll back a node's plugin config to a captured snapshot.

Specify a backup by its ID directly, or use --pending-id to look up the backup
tied to a specific dangerous-push confirmation (e.g. after a bad SSSD push).

The command polls until the restore completes or the timeout expires.`,
		Example: `  clustr restore replay pb-1234567890
  clustr restore replay --pending-id <dangerous-push-uuid>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Must supply exactly one of: positional backup-id OR --pending-id.
			if flagPendingID != "" && len(args) > 0 {
				return fmt.Errorf("specify either a backup-id argument or --pending-id, not both")
			}
			if flagPendingID == "" && len(args) == 0 {
				return fmt.Errorf("a backup-id argument or --pending-id is required")
			}
			backupID := ""
			if len(args) > 0 {
				backupID = args[0]
			}
			return runRestoreReplay(backupID, flagPendingID, flagOutputJSON, flagTimeout)
		},
	}
	cmd.Flags().StringVar(&flagPendingID, "pending-id", "", "Dangerous-push ID to look up the associated backup")
	cmd.Flags().BoolVar(&flagOutputJSON, "output-json", false, "Output as JSON")
	cmd.Flags().IntVar(&flagTimeout, "timeout", 180, "Seconds to wait for the restore to complete")
	return cmd
}

func runRestoreReplay(backupID, pendingID string, outputJSON bool, timeoutSec int) error {
	ctx := context.Background()
	c := clientFromFlags()

	// Resolve --pending-id to a backup-id via the list endpoint.
	if pendingID != "" {
		url := "/api/v1/backups?pending_id=" + pendingID
		var resp restoreBackupListResponse
		if err := c.GetJSON(ctx, url, &resp); err != nil {
			return fmt.Errorf("resolve pending-id %s: %w", pendingID, err)
		}
		if len(resp.Backups) == 0 {
			return fmt.Errorf("no backup found for pending-push-id %s", pendingID)
		}
		backupID = resp.Backups[0].ID
		fmt.Fprintf(rootCmd.ErrOrStderr(), "resolved pending-id %s → backup %s\n", pendingID, backupID)
	}

	// Initiate the restore.
	var initResp restoreInitiateResponse
	if err := c.PostJSON(ctx, "/api/v1/backups/"+backupID+"/restore", nil, &initResp); err != nil {
		return fmt.Errorf("initiate restore: %w", err)
	}

	jobID := initResp.JobID
	fmt.Fprintf(rootCmd.ErrOrStderr(), "restore job %s started (node=%s plugin=%s)\n",
		jobID, initResp.NodeID, initResp.Plugin)

	// Poll for completion.
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	pollInterval := 2 * time.Second

	for {
		var statusResp restoreStatusResponse
		statusURL := "/api/v1/backups/" + backupID + "/restore-status?job_id=" + jobID
		if err := c.GetJSON(ctx, statusURL, &statusResp); err != nil {
			return fmt.Errorf("poll restore status: %w", err)
		}

		switch statusResp.Status {
		case "done":
			if outputJSON {
				enc := json.NewEncoder(rootCmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(statusResp)
			}
			fmt.Fprintf(rootCmd.OutOrStdout(), "restore completed: job=%s backup=%s node=%s plugin=%s\n",
				jobID, backupID, statusResp.NodeID, statusResp.Plugin)
			return nil

		case "failed":
			errMsg := "(unknown)"
			if statusResp.Error != nil {
				errMsg = *statusResp.Error
			}
			return fmt.Errorf("restore failed: %s", errMsg)
		}

		// Still pending/running — check timeout.
		if time.Now().After(deadline) {
			return fmt.Errorf("restore timed out after %ds (job=%s status=%s)",
				timeoutSec, jobID, statusResp.Status)
		}

		time.Sleep(pollInterval)
	}
}
