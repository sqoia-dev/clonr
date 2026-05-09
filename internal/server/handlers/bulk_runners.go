// Package handlers — bulk_runners.go wires the Sprint 44 BulkHandler
// to the existing reimage Orchestrator and clientd Hub via thin adapter
// types. Keeping the wiring here (not in bulk.go) means bulk.go remains a
// pure HTTP+fan-out implementation that is testable without dragging in
// the Orchestrator or Hub concrete types.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/reimage"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── Reimage runner adapter ───────────────────────────────────────────────────

// reimageOrchestratorRunner is a thin adapter around *reimage.Orchestrator
// that satisfies BulkReimageRunner.
type reimageOrchestratorRunner struct {
	DB           *db.DB
	Orchestrator *reimage.Orchestrator
}

// NewReimageRunner returns a BulkReimageRunner backed by the production
// orchestrator + DB.
func NewReimageRunner(d *db.DB, o *reimage.Orchestrator) BulkReimageRunner {
	return &reimageOrchestratorRunner{DB: d, Orchestrator: o}
}

// StartReimage creates a reimage request and immediately triggers it.  Mirrors
// the per-node body of ReimageHandler.Create but without the per-request
// pre-checks (force/SSH-key/active-conflict) that don't apply at bulk scope.
// Bulk callers pass force=true to bypass the pre-checks; force=false applies
// the standard "image must be ready" guard.
func (r *reimageOrchestratorRunner) StartReimage(ctx context.Context, nodeID, imageID string, force bool, requestedBy string) (string, error) {
	if r.DB == nil || r.Orchestrator == nil {
		return "", fmt.Errorf("reimage runner not fully wired")
	}

	node, err := r.DB.GetNodeConfig(ctx, nodeID)
	if err != nil {
		return "", fmt.Errorf("load node: %w", err)
	}
	if imageID == "" {
		imageID = node.BaseImageID
	}
	if imageID == "" {
		return "", fmt.Errorf("no image_id and node has no base_image_id")
	}

	if !force {
		img, err := r.DB.GetBaseImage(ctx, imageID)
		if err != nil {
			return "", fmt.Errorf("load image: %w", err)
		}
		if img.Status != api.ImageStatusReady {
			return "", fmt.Errorf("image %q is not ready (status: %s)", imageID, img.Status)
		}
	}

	if requestedBy == "" {
		requestedBy = "bulk"
	}
	req := api.ReimageRequest{
		ID:          uuid.New().String(),
		NodeID:      nodeID,
		ImageID:     imageID,
		Status:      api.ReimageStatusPending,
		RequestedBy: requestedBy,
		CreatedAt:   time.Now().UTC(),
	}
	if err := r.DB.CreateReimageRequest(ctx, req); err != nil {
		return "", fmt.Errorf("create reimage request: %w", err)
	}
	if err := r.Orchestrator.Trigger(ctx, req.ID); err != nil {
		// Best-effort surface — request row stays as failed once orchestrator
		// updates it. Caller sees the trigger error here.
		return req.ID, fmt.Errorf("trigger: %w", err)
	}
	return req.ID, nil
}

// ─── Exec runner adapter ──────────────────────────────────────────────────────

// clientdHubExecRunner satisfies BulkExecRunner. It dispatches a single
// operator_exec_request on the clientd hub and waits for the matching
// operator_exec_result.  Used by bulk drain (scontrol on a Slurm head) and
// bulk exec (run a command on each selected node).
type clientdHubExecRunner struct {
	Hub clientdExecHub
}

// clientdExecHub is the subset of *clientd.Hub used here.  Defined as an
// interface so this adapter can be unit-tested without spinning up a real
// websocket hub.
type clientdExecHub interface {
	IsConnected(nodeID string) bool
	Send(nodeID string, msg clientd.ServerMessage) error
	RegisterOperatorExec(msgID string) <-chan clientd.OperatorExecResultPayload
	UnregisterOperatorExec(msgID string)
}

// NewExecRunner returns a BulkExecRunner backed by the clientd hub.
func NewExecRunner(hub clientdExecHub) BulkExecRunner {
	return &clientdHubExecRunner{Hub: hub}
}

// ExecOne runs `command args...` on one node and returns (exit_code, output, err).
// Output is the concatenation of stdout (preferred) or stderr if no stdout.
func (e *clientdHubExecRunner) ExecOne(ctx context.Context, nodeID, command string, args []string, timeoutSec int) (int, string, error) {
	if e.Hub == nil {
		return -1, "", fmt.Errorf("hub not configured")
	}
	if !e.Hub.IsConnected(nodeID) {
		return -1, "", fmt.Errorf("node %s not connected", nodeID)
	}
	if timeoutSec <= 0 {
		timeoutSec = 60
	}

	msgID := uuid.New().String()
	payload, err := json.Marshal(clientd.OperatorExecRequestPayload{
		RefMsgID:   msgID,
		Command:    command,
		Args:       args,
		TimeoutSec: timeoutSec,
	})
	if err != nil {
		return -1, "", fmt.Errorf("marshal: %w", err)
	}

	ch := e.Hub.RegisterOperatorExec(msgID)
	defer e.Hub.UnregisterOperatorExec(msgID)

	if err := e.Hub.Send(nodeID, clientd.ServerMessage{
		Type:    "operator_exec_request",
		MsgID:   msgID,
		Payload: json.RawMessage(payload),
	}); err != nil {
		return -1, "", fmt.Errorf("send: %w", err)
	}

	deadline := time.Duration(timeoutSec+5) * time.Second
	select {
	case res := <-ch:
		// Prefer stdout, fall back to stderr for the human-readable detail.
		out := res.Stdout
		if out == "" {
			out = res.Stderr
		}
		return res.ExitCode, out, nil
	case <-time.After(deadline):
		return -1, "", fmt.Errorf("timed out after %ds waiting for result", timeoutSec+5)
	case <-ctx.Done():
		return -1, "", ctx.Err()
	}
}
