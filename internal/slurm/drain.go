// internal/slurm/drain.go — entry points used by the multi-select bulk
// drain HTTP handler (Sprint 44 BULK-MULTISELECT-ACTIONS).
//
// The pre-existing slurm.Manager already knows how to send a
// slurm_admin_cmd to the controller node — that is the command path
// used by the rolling upgrade orchestrator.  This file exposes the
// same primitive without dragging an upgrade plan along, so the
// bulk drain handler can dispatch one drain RPC per request.
//
// Why we don't use the per-target ExecRunner: ExecOne rejects calls to
// a node that isn't connected over the clientd websocket (clientd
// drains its own connection when the node goes offline).  Operators
// drain offline nodes routinely — that's the whole point of marking a
// node down to slurmctld so jobs route around it.  The slurmctld
// admin path (via the controller's clientd connection) reaches
// scontrol regardless of whether the target node is up.

package slurm

import (
	"context"
	"errors"
	"fmt"

	"github.com/sqoia-dev/clustr/internal/clientd"
)

// ErrNoController is returned when no node in the cluster is tagged as
// a slurm controller (RoleController).
var ErrNoController = errors.New("slurm: no controller node configured")

// ErrControllerOffline is returned when the elected controller is not
// connected to clustr-serverd's clientd hub at request time.  Drain
// requires the controller to be reachable; offline controllers cannot
// service scontrol RPCs.
var ErrControllerOffline = errors.New("slurm: controller node is offline")

// DrainNodes runs `scontrol update nodename=<name> state=DRAIN
// reason=<reason>` on the cluster's controller node for every clustr
// node ID in nodeIDs.  Codex post-ship review issue #7: previously the
// bulk drain handler dispatched scontrol via ExecOne to each TARGET
// node, but the exec runner refuses disconnected targets and operators
// want to drain offline nodes specifically.  Drain is a slurmctld
// action, so the right place to issue it is the controller.
//
// Returns ErrNoController when no controller is tagged in the DB and
// ErrControllerOffline when the controller is not connected.  All
// other errors come straight from the slurm_admin_cmd ack pipeline.
func (m *Manager) DrainNodes(ctx context.Context, nodeIDs []string, reason string) error {
	if reason == "" {
		reason = "clustr-bulk-drain"
	}
	controllerID, err := m.electController(ctx)
	if err != nil {
		return err
	}
	if m.hub == nil || !m.hub.IsConnected(controllerID) {
		return fmt.Errorf("%w (controller %s)", ErrControllerOffline, controllerID)
	}

	slurmNames := m.clustrIDsToSlurmNames(ctx, nodeIDs)
	if len(slurmNames) == 0 {
		return nil
	}
	return m.sendAdminCmd(ctx, controllerID, clientd.SlurmAdminCmdPayload{
		Command: "drain",
		Nodes:   slurmNames,
		Reason:  reason,
	})
}

// electController returns the first clustr node tagged with the
// "controller" role.  Mirrors the controller-resolution shape used by
// the upgrade orchestrator (plan.ControllerNodes[0]).
func (m *Manager) electController(ctx context.Context) (string, error) {
	if m.db == nil {
		return "", ErrNoController
	}
	ctrlNodes, err := m.db.SlurmListNodesByRole(ctx, RoleController)
	if err != nil {
		return "", fmt.Errorf("slurm: list controller nodes: %w", err)
	}
	if len(ctrlNodes) == 0 {
		return "", ErrNoController
	}
	return ctrlNodes[0], nil
}
