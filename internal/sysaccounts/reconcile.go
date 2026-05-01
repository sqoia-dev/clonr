// reconcile.go — startup reconciliation for system_accounts rows whose UID/GID
// drifted into LDAP user space during the Sprint 13 #96 single-range bug.
//
// ReconcileFromNode reads `getent passwd <name>` from the controller node via
// clientd exec_request and updates the DB row with the on-node UID.  It only
// patches rows where UID ≥ 1000, which are the mis-allocated ones.  Rows that
// already have a correct system UID (< 1000) are skipped.
//
// The reconciliation is gated behind CLUSTR_RECONCILE_SYSACCOUNTS=1 so it
// does not fire on every restart.
package sysaccounts

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/clientd"
)

// NodeExecer is a minimal interface for sending exec_request messages to a
// connected node and collecting the result.  *server.ClientdHub satisfies this.
type NodeExecer interface {
	IsConnected(nodeID string) bool
	RegisterExec(msgID string) <-chan clientd.ExecResultPayload
	UnregisterExec(msgID string)
	Send(nodeID string, msg clientd.ServerMessage) error
}

// ReconcileFromNode inspects every system_accounts row with UID ≥ 1000, asks
// the controller node for the on-node UID via `getent passwd <name>`, and
// updates the DB row to match.  Rows with UID < 1000 are skipped (already correct).
//
// controllerNodeID is the registered node UUID for the cluster controller that
// has clientd connected.  If the node is not connected the function logs a warning
// and exits cleanly — it does not return an error, so startup is not blocked.
//
// ReconcileFromNode is not safe to call concurrently with itself.
func (m *Manager) ReconcileFromNode(ctx context.Context, hub NodeExecer, controllerNodeID string) error {
	if !hub.IsConnected(controllerNodeID) {
		log.Warn().
			Str("controller_node_id", controllerNodeID).
			Msg("sysaccounts reconcile: controller node not connected, skipping (will retry on next startup with CLUSTR_RECONCILE_SYSACCOUNTS=1)")
		return nil
	}

	accounts, err := m.db.SysAccountsListAccounts(ctx)
	if err != nil {
		return fmt.Errorf("sysaccounts reconcile: list accounts: %w", err)
	}

	fixed := 0
	for _, acct := range accounts {
		if acct.UID < 1000 {
			continue // already in system range, nothing to do
		}

		// Ask the controller node for the on-node UID.
		onNodeUID, err := getentUID(ctx, hub, controllerNodeID, acct.Username)
		if err != nil {
			log.Warn().
				Err(err).
				Str("username", acct.Username).
				Int("db_uid", acct.UID).
				Msg("sysaccounts reconcile: getent failed, skipping account")
			continue
		}

		if onNodeUID == acct.UID {
			// No drift — already consistent (shouldn't happen for UID>=1000 accounts
			// on a node provisioned by DNF, but be defensive).
			continue
		}

		log.Info().
			Str("username", acct.Username).
			Int("old_uid", acct.UID).
			Int("new_uid", onNodeUID).
			Msg("sysaccounts reconcile: updating UID to match on-node truth")

		if err := m.db.SysAccountsUpdateUID(ctx, acct.ID, onNodeUID); err != nil {
			log.Error().
				Err(err).
				Str("username", acct.Username).
				Int("old_uid", acct.UID).
				Int("new_uid", onNodeUID).
				Msg("sysaccounts reconcile: failed to update UID in DB")
			continue
		}
		fixed++
	}

	log.Info().
		Int("fixed", fixed).
		Int("total", len(accounts)).
		Msg("sysaccounts reconcile: complete")
	return nil
}

// getentUID sends `getent passwd <username>` to the node via clientd exec_request
// and returns the UID (field 3 of the colon-delimited passwd output).
func getentUID(ctx context.Context, hub NodeExecer, nodeID, username string) (int, error) {
	msgID := uuid.New().String()

	payload, err := json.Marshal(clientd.ExecRequestPayload{
		RefMsgID: msgID,
		Command:  "getent",
		Args:     []string{"passwd", username},
	})
	if err != nil {
		return 0, fmt.Errorf("marshal exec_request: %w", err)
	}

	serverMsg := clientd.ServerMessage{
		Type:    "exec_request",
		MsgID:   msgID,
		Payload: json.RawMessage(payload),
	}

	execCh := hub.RegisterExec(msgID)
	defer hub.UnregisterExec(msgID)

	if err := hub.Send(nodeID, serverMsg); err != nil {
		return 0, fmt.Errorf("send exec_request to node %s: %w", nodeID, err)
	}

	// Wait up to 15 seconds for the result.
	timeout := 15 * time.Second
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	select {
	case result := <-execCh:
		if result.ExitCode != 0 {
			return 0, fmt.Errorf("getent passwd %s exited %d: %s", username, result.ExitCode, result.Stderr)
		}
		return parseGetentUID(result.Stdout)
	case <-deadline.C:
		return 0, fmt.Errorf("timeout waiting for getent passwd %s (15s)", username)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// parseGetentUID parses the UID from a `getent passwd` output line.
// Format: name:password:uid:gid:gecos:home:shell
func parseGetentUID(line string) (int, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, fmt.Errorf("empty getent passwd output")
	}
	parts := strings.Split(line, ":")
	if len(parts) < 4 {
		return 0, fmt.Errorf("unexpected getent passwd format: %q", line)
	}
	uid, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, fmt.Errorf("parse UID field %q: %w", parts[2], err)
	}
	return uid, nil
}

