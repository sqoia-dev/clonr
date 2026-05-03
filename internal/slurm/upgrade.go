// upgrade.go — Slurm rolling upgrade orchestration.
//
// StartUpgrade kicks off an async rolling upgrade and returns the operation ID
// immediately. The actual execution runs in a background goroutine via executeUpgrade.
//
// Phase ordering (Slurm compatibility requirement — MUST NOT be changed):
//   1. DBD nodes     — slurmdbd must be upgraded first
//   2. Controller    — slurmctld upgraded after DBD, before compute
//   3. Compute nodes — batched, drained, upgraded, resumed
//   4. Login nodes   — no service restart, push binaries last
//
// Drain/resume commands are sent to the controller node via slurm_admin_cmd.
// Binary push uses the existing slurm_binary_push + ack infrastructure.
package slurm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/db"
)

// ─── Public types ─────────────────────────────────────────────────────────────

// UpgradeRequest is the body for POST /slurm/upgrades.
type UpgradeRequest struct {
	ToBuildID         string `json:"to_build_id"`       // target Slurm build UUID
	BatchSize         int    `json:"batch_size"`        // compute nodes per batch (default 10)
	DrainTimeoutMin   int    `json:"drain_timeout_min"` // max minutes to wait for drain (default 30)
	ConfirmedDBBackup bool   `json:"confirmed_db_backup"`
}

// UpgradeNodeResult is the per-node result stored in slurm_upgrade_operations.node_results.
type UpgradeNodeResult struct {
	OK               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`
	InstalledVersion string `json:"installed_version,omitempty"`
	Phase            string `json:"phase"` // "dbd", "controller", "compute", "login"
}

// UpgradeValidation is returned by ValidateUpgrade.
type UpgradeValidation struct {
	Valid       bool              `json:"valid"`
	Warnings    []string          `json:"warnings,omitempty"`
	Errors      []string          `json:"errors,omitempty"`
	UpgradePlan *UpgradePlan      `json:"upgrade_plan,omitempty"`
	FromVersion string            `json:"from_version,omitempty"`
	ToVersion   string            `json:"to_version,omitempty"`
	JobCount    int               `json:"job_count"`
}

// UpgradePlan describes the planned upgrade phases (returned by ValidateUpgrade).
type UpgradePlan struct {
	DBDNodes        []string   `json:"dbd_nodes"`
	ControllerNodes []string   `json:"controller_nodes"`
	ComputeBatches  [][]string `json:"compute_batches"`
	LoginNodes      []string   `json:"login_nodes"`
}

// UpgradeOperation is the API representation of an upgrade op.
type UpgradeOperation struct {
	ID                string                       `json:"id"`
	FromBuildID       string                       `json:"from_build_id"`
	ToBuildID         string                       `json:"to_build_id"`
	Status            string                       `json:"status"`
	Phase             string                       `json:"phase,omitempty"`
	CurrentBatch      int                          `json:"current_batch"`
	TotalBatches      int                          `json:"total_batches"`
	BatchSize         int                          `json:"batch_size"`
	DrainTimeoutMin   int                          `json:"drain_timeout_min"`
	ConfirmedDBBackup bool                         `json:"confirmed_db_backup"`
	InitiatedBy       string                       `json:"initiated_by"`
	StartedAt         int64                        `json:"started_at"`
	CompletedAt       *int64                       `json:"completed_at,omitempty"`
	NodeResults       map[string]UpgradeNodeResult `json:"node_results,omitempty"`
}

// ─── In-progress upgrade state ────────────────────────────────────────────────

// upgradeState holds mutable state for one in-progress upgrade.
// Only one upgrade runs at a time; guarded by Manager.upgradeMu.
type upgradeState struct {
	opID   string
	paused bool
	cancel context.CancelFunc
}

// ─── Public API ───────────────────────────────────────────────────────────────

// ValidateUpgrade performs pre-upgrade validation and returns a plan + warnings.
// This is a read-only operation; it does not start an upgrade.
func (m *Manager) ValidateUpgrade(ctx context.Context, req UpgradeRequest) (*UpgradeValidation, error) {
	v := &UpgradeValidation{}

	// 1. Resolve target build.
	toBuild, err := m.db.SlurmGetBuild(ctx, req.ToBuildID)
	if err != nil {
		v.Errors = append(v.Errors, fmt.Sprintf("target build %s not found", req.ToBuildID))
		return v, nil
	}
	if toBuild.Status != "completed" {
		v.Errors = append(v.Errors, fmt.Sprintf("target build %s is not completed (status: %s)", req.ToBuildID, toBuild.Status))
		return v, nil
	}
	v.ToVersion = toBuild.Version

	// 2. Check per-node deployed state: if ALL managed slurm nodes are already at
	// the target version, this is a true no-op. If SOME are already at target,
	// report that so the operator knows the upgrade is partial.
	nodeVersions, _ := m.db.SlurmListNodeVersions(ctx)
	nodesAtTarget := 0
	for _, nv := range nodeVersions {
		if nv.DeployedVersion == toBuild.Version {
			nodesAtTarget++
		}
	}
	totalManagedNodes := len(nodeVersions)
	if totalManagedNodes > 0 && nodesAtTarget == totalManagedNodes {
		v.Errors = append(v.Errors, fmt.Sprintf("all %d nodes are already at version %s — no upgrade needed", totalManagedNodes, toBuild.Version))
		return v, nil
	}
	if nodesAtTarget > 0 && nodesAtTarget < totalManagedNodes {
		v.Warnings = append(v.Warnings, fmt.Sprintf("%d of %d nodes already at version %s; will upgrade the remaining %d", nodesAtTarget, totalManagedNodes, toBuild.Version, totalManagedNodes-nodesAtTarget))
	}

	// Resolve from_version from per-node state (use majority version or active build).
	activeBuildID, _ := m.db.SlurmGetActiveBuildID(ctx)
	if activeBuildID != "" {
		if fromBuild, err2 := m.db.SlurmGetBuild(ctx, activeBuildID); err2 == nil {
			v.FromVersion = fromBuild.Version
		}
	}

	// 3. DB backup required if DBD nodes exist.
	dbdNodes, _ := m.db.SlurmListNodesByRole(ctx, RoleDBD)
	if len(dbdNodes) > 0 && !req.ConfirmedDBBackup {
		v.Warnings = append(v.Warnings, "DB backup confirmation required — include confirmed_db_backup: true in request")
	}

	// 4. Check for in-progress upgrade.
	if m.hasInProgressUpgrade(ctx) {
		v.Errors = append(v.Errors, "another upgrade is already in progress")
		return v, nil
	}

	// 5. Build upgrade plan.
	plan, warnings := m.buildUpgradePlan(ctx, req)
	v.UpgradePlan = plan
	v.Warnings = append(v.Warnings, warnings...)

	// 6. Check connected nodes.
	if m.hub != nil {
		allSlurmNodes := collectAllPlanNodes(plan)
		for _, nodeID := range allSlurmNodes {
			if !m.hub.IsConnected(nodeID) {
				v.Warnings = append(v.Warnings, fmt.Sprintf("node %s is offline (clustr-clientd not connected)", nodeID))
			}
		}
	}

	// 7. Job count check (best-effort via controller node).
	v.JobCount = m.queryJobCount(ctx, plan)

	v.Valid = len(v.Errors) == 0
	return v, nil
}

// StartUpgrade kicks off an async rolling upgrade. Returns the upgrade op ID.
func (m *Manager) StartUpgrade(ctx context.Context, req UpgradeRequest, initiatedBy string) (string, error) {
	// Apply defaults.
	if req.BatchSize <= 0 {
		req.BatchSize = 10
	}
	if req.DrainTimeoutMin <= 0 {
		req.DrainTimeoutMin = 30
	}

	// Pre-flight: validate build exists and is completed.
	toBuild, err := m.db.SlurmGetBuild(ctx, req.ToBuildID)
	if err != nil {
		return "", fmt.Errorf("slurm: upgrade: target build not found: %w", err)
	}
	if toBuild.Status != "completed" {
		return "", fmt.Errorf("slurm: upgrade: target build %s is not completed (status: %s)", req.ToBuildID, toBuild.Status)
	}

	// DB backup confirmation required.
	dbdNodes, _ := m.db.SlurmListNodesByRole(ctx, RoleDBD)
	if len(dbdNodes) > 0 && !req.ConfirmedDBBackup {
		return "", fmt.Errorf("slurm: upgrade: db backup confirmation required (confirmed_db_backup must be true)")
	}

	// Reject if another upgrade is already running.
	if m.hasInProgressUpgrade(ctx) {
		return "", fmt.Errorf("slurm: upgrade: another upgrade is already in progress")
	}

	// Resolve from_build_id.
	fromBuildID, _ := m.db.SlurmGetActiveBuildID(ctx)

	opID := uuid.New().String()
	now := time.Now().Unix()

	opRow := db.SlurmUpgradeOpRow{
		ID:                opID,
		FromBuildID:       fromBuildID,
		ToBuildID:         req.ToBuildID,
		Status:            "queued",
		BatchSize:         req.BatchSize,
		DrainTimeoutMin:   req.DrainTimeoutMin,
		ConfirmedDBBackup: req.ConfirmedDBBackup,
		InitiatedBy:       initiatedBy,
		Phase:             "",
		CurrentBatch:      0,
		TotalBatches:      0,
		StartedAt:         now,
	}
	if err := m.db.SlurmCreateUpgradeOp(ctx, opRow); err != nil {
		return "", fmt.Errorf("slurm: upgrade: create op record: %w", err)
	}

	log.Info().
		Str("op_id", opID).
		Str("to_build_id", req.ToBuildID).
		Str("to_version", toBuild.Version).
		Str("initiated_by", initiatedBy).
		Msg("slurm: upgrade: starting rolling upgrade")

	// Run in background so HTTP handler can return immediately.
	go func() {
		bgCtx := context.Background()
		if err := m.executeUpgrade(bgCtx, opID, req); err != nil {
			log.Error().Err(err).Str("op_id", opID).Msg("slurm: upgrade: execution failed")
		}
	}()

	return opID, nil
}

// PauseUpgrade signals the upgrade to pause after the current batch completes.
func (m *Manager) PauseUpgrade(ctx context.Context, opID string) error {
	op, err := m.db.SlurmGetUpgradeOp(ctx, opID)
	if err != nil {
		return fmt.Errorf("slurm: pause: op not found: %w", err)
	}
	if op.Status != "in_progress" {
		return fmt.Errorf("slurm: pause: upgrade is not in_progress (status: %s)", op.Status)
	}

	m.upgradeMu.Lock()
	if m.activeUpgrade != nil && m.activeUpgrade.opID == opID {
		m.activeUpgrade.paused = true
	}
	m.upgradeMu.Unlock()

	// Mark paused in DB immediately so polling clients see it.
	return m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
		Status:       "paused",
		Phase:        op.Phase,
		CurrentBatch: op.CurrentBatch,
		TotalBatches: op.TotalBatches,
	})
}

// ResumeUpgrade resumes a paused upgrade.
func (m *Manager) ResumeUpgrade(ctx context.Context, opID string) error {
	op, err := m.db.SlurmGetUpgradeOp(ctx, opID)
	if err != nil {
		return fmt.Errorf("slurm: resume: op not found: %w", err)
	}
	if op.Status != "paused" {
		return fmt.Errorf("slurm: resume: upgrade is not paused (status: %s)", op.Status)
	}

	req := UpgradeRequest{
		ToBuildID:         op.ToBuildID,
		BatchSize:         op.BatchSize,
		DrainTimeoutMin:   op.DrainTimeoutMin,
		ConfirmedDBBackup: op.ConfirmedDBBackup,
	}

	// Re-launch execution from current batch.
	go func() {
		bgCtx := context.Background()
		if err := m.executeUpgrade(bgCtx, opID, req); err != nil {
			log.Error().Err(err).Str("op_id", opID).Msg("slurm: resume: execution failed")
		}
	}()

	return nil
}

// RollbackUpgrade initiates a rollback to the previous build (from_build_id).
func (m *Manager) RollbackUpgrade(ctx context.Context, opID string) error {
	op, err := m.db.SlurmGetUpgradeOp(ctx, opID)
	if err != nil {
		return fmt.Errorf("slurm: rollback: op not found: %w", err)
	}

	allowedStatuses := map[string]bool{
		"in_progress": true, "paused": true, "failed": true, "completed": true,
	}
	if !allowedStatuses[op.Status] {
		return fmt.Errorf("slurm: rollback: cannot rollback from status %s", op.Status)
	}

	if op.FromBuildID == "" {
		return fmt.Errorf("slurm: rollback: no previous build recorded for this operation")
	}

	// Verify the previous build still exists and is completed.
	fromBuild, err := m.db.SlurmGetBuild(ctx, op.FromBuildID)
	if err != nil {
		return fmt.Errorf("slurm: rollback: previous build %s not found (may have been deleted): %w", op.FromBuildID, err)
	}
	if fromBuild.Status != "completed" {
		return fmt.Errorf("slurm: rollback: previous build %s is not completed", op.FromBuildID)
	}

	// Cancel any running upgrade execution for this op.
	m.upgradeMu.Lock()
	if m.activeUpgrade != nil && m.activeUpgrade.opID == opID {
		m.activeUpgrade.cancel()
		m.activeUpgrade = nil
	}
	m.upgradeMu.Unlock()

	// Mark the original op as rolled_back.
	now := time.Now().Unix()
	_ = m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
		Status:      "rolled_back",
		Phase:       op.Phase,
		CompletedAt: &now,
	})

	// Create a new upgrade op for the rollback (from current → previous).
	rollbackReq := UpgradeRequest{
		ToBuildID:         op.FromBuildID,
		BatchSize:         op.BatchSize,
		DrainTimeoutMin:   op.DrainTimeoutMin,
		ConfirmedDBBackup: op.ConfirmedDBBackup,
	}

	rollbackOpID, err := m.StartUpgrade(ctx, rollbackReq, "rollback:"+opID)
	if err != nil {
		return fmt.Errorf("slurm: rollback: failed to start rollback upgrade: %w", err)
	}

	log.Info().
		Str("original_op_id", opID).
		Str("rollback_op_id", rollbackOpID).
		Str("to_build_id", op.FromBuildID).
		Msg("slurm: rollback initiated")

	return nil
}

// GetUpgradeOp retrieves an upgrade operation for the route handler.
func (m *Manager) GetUpgradeOp(ctx context.Context, opID string) (*UpgradeOperation, error) {
	row, err := m.db.SlurmGetUpgradeOp(ctx, opID)
	if err != nil {
		return nil, err
	}
	return upgradeOpRowToAPI(row), nil
}

// ListUpgradeOps returns all upgrade operations for the route handler.
func (m *Manager) ListUpgradeOps(ctx context.Context) ([]UpgradeOperation, error) {
	rows, err := m.db.SlurmListUpgradeOps(ctx)
	if err != nil {
		return nil, err
	}
	ops := make([]UpgradeOperation, 0, len(rows))
	for _, row := range rows {
		ops = append(ops, *upgradeOpRowToAPI(&row))
	}
	return ops, nil
}

// ─── Orchestration core ───────────────────────────────────────────────────────

// executeUpgrade runs the rolling upgrade. Called from a background goroutine.
// It updates the DB op record at each phase transition.
func (m *Manager) executeUpgrade(ctx context.Context, opID string, req UpgradeRequest) error {
	// Register this as the active upgrade so pause/cancel can find it.
	execCtx, cancel := context.WithCancel(ctx)
	state := &upgradeState{opID: opID, cancel: cancel}
	m.upgradeMu.Lock()
	m.activeUpgrade = state
	m.upgradeMu.Unlock()
	defer func() {
		m.upgradeMu.Lock()
		if m.activeUpgrade != nil && m.activeUpgrade.opID == opID {
			m.activeUpgrade = nil
		}
		m.upgradeMu.Unlock()
		cancel()
	}()

	// Mark in_progress.
	if err := m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
		Status: "in_progress",
		Phase:  "starting",
	}); err != nil {
		return fmt.Errorf("upgrade: mark in_progress: %w", err)
	}

	toBuild, err := m.db.SlurmGetBuild(ctx, req.ToBuildID)
	if err != nil {
		return m.failUpgrade(ctx, opID, fmt.Errorf("fetch target build: %w", err))
	}

	// Collect per-node results (accumulated across all phases).
	nodeResults := make(map[string]UpgradeNodeResult)
	var nodeResultsMu sync.Mutex

	recordResult := func(nodeID string, result UpgradeNodeResult) {
		// Build a snapshot under lock, then release before the DB write.
		nodeResultsMu.Lock()
		nodeResults[nodeID] = result
		nr := make(map[string]UpgradeNodeResult, len(nodeResults))
		for k, v := range nodeResults {
			nr[k] = v
		}
		nodeResultsMu.Unlock()
		// Persist snapshot so the UI sees live progress.
		m.persistNodeResults(ctx, opID, nr)

		// Track per-node deployed version for accurate upgrade validation.
		// If the upgrade succeeded and we have an installed version, record it.
		if result.OK && result.InstalledVersion != "" {
			if verErr := m.db.SlurmUpsertNodeVersion(ctx, db.SlurmNodeVersionRow{
				NodeID:          nodeID,
				DeployedVersion: result.InstalledVersion,
				BuildID:         req.ToBuildID,
				InstallMethod:   "dnf",
				InstalledAt:     time.Now().Unix(),
				InstalledBy:     "clustr-server",
			}); verErr != nil {
				log.Warn().Err(verErr).Str("node_id", nodeID).Msg("slurm: upgrade: failed to record node version (non-fatal)")
			}
		}
	}

	// ── Collect plan ──────────────────────────────────────────────────────
	plan, _ := m.buildUpgradePlan(ctx, req)

	totalBatches := len(plan.ComputeBatches)
	if err := m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
		Status:       "in_progress",
		Phase:        "dbd",
		TotalBatches: totalBatches,
	}); err != nil {
		log.Warn().Err(err).Str("op_id", opID).Msg("slurm: upgrade: failed to set total_batches")
	}

	// ── Phase 1: DBD ──────────────────────────────────────────────────────
	if len(plan.DBDNodes) > 0 {
		log.Info().Str("op_id", opID).Strs("nodes", plan.DBDNodes).Msg("slurm: upgrade: phase DBD")
		for _, nodeID := range plan.DBDNodes {
			if err := m.checkPauseCancel(execCtx, state); err != nil {
				return m.pauseOrCancelUpgrade(ctx, opID, nodeResults)
			}
			result := m.dnfUpgradeNode(execCtx, opID, nodeID, toBuild)
			result.Phase = "dbd"
			recordResult(nodeID, result)
			if !result.OK {
				return m.failUpgrade(ctx, opID, fmt.Errorf("DBD node %s failed: %s", nodeID, result.Error))
			}
		}
	}

	// ── Phase 2: Controller ───────────────────────────────────────────────
	if len(plan.ControllerNodes) > 0 {
		log.Info().Str("op_id", opID).Strs("nodes", plan.ControllerNodes).Msg("slurm: upgrade: phase controller")
		if err := m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
			Status:       "in_progress",
			Phase:        "controller",
			TotalBatches: totalBatches,
		}); err != nil {
			log.Warn().Err(err).Msg("slurm: upgrade: failed to update phase to controller")
		}
		for _, nodeID := range plan.ControllerNodes {
			if err := m.checkPauseCancel(execCtx, state); err != nil {
				return m.pauseOrCancelUpgrade(ctx, opID, nodeResults)
			}
			result := m.dnfUpgradeNode(execCtx, opID, nodeID, toBuild)
			result.Phase = "controller"
			recordResult(nodeID, result)
			if !result.OK {
				return m.failUpgrade(ctx, opID, fmt.Errorf("controller node %s failed: %s", nodeID, result.Error))
			}
		}
	}

	// ── Phase 3: Compute (batched) ────────────────────────────────────────
	// Find the controller node ID to use for drain/resume commands.
	controllerID := ""
	if len(plan.ControllerNodes) > 0 {
		controllerID = plan.ControllerNodes[0]
	}

	if len(plan.ComputeBatches) > 0 {
		if err := m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
			Status:       "in_progress",
			Phase:        "compute",
			TotalBatches: totalBatches,
		}); err != nil {
			log.Warn().Err(err).Msg("slurm: upgrade: failed to update phase to compute")
		}
	}

	for batchIdx, batch := range plan.ComputeBatches {
		if err := m.checkPauseCancel(execCtx, state); err != nil {
			return m.pauseOrCancelUpgrade(ctx, opID, nodeResults)
		}

		log.Info().
			Str("op_id", opID).
			Int("batch", batchIdx+1).
			Int("total", len(plan.ComputeBatches)).
			Strs("nodes", batch).
			Msg("slurm: upgrade: compute batch")

		// Update current_batch in DB.
		_ = m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
			Status:       "in_progress",
			Phase:        "compute",
			CurrentBatch: batchIdx + 1,
			TotalBatches: totalBatches,
		})

		// Step 1: Drain batch nodes via controller.
		if controllerID != "" {
			slurmNodeNames := m.clustrIDsToSlurmNames(ctx, batch)
			if err := m.sendAdminCmd(execCtx, controllerID, clientd.SlurmAdminCmdPayload{
				Command: "drain",
				Nodes:   slurmNodeNames,
				Reason:  "clustr-upgrade",
			}); err != nil {
				log.Warn().Err(err).Int("batch", batchIdx+1).Msg("slurm: upgrade: drain failed for batch")
				// Don't abort — drain failure is non-fatal; nodes may already be idle.
			}

			// Step 2: Wait for jobs to drain.
			drainTimeout := time.Duration(req.DrainTimeoutMin) * time.Minute
			drained := m.waitForDrain(execCtx, controllerID, slurmNodeNames, drainTimeout)
			if !drained {
				log.Warn().Int("batch", batchIdx+1).
					Msg("slurm: upgrade: drain timeout reached — proceeding anyway (some jobs may still be running)")
			}
		}

		// Step 3: Push binary to all compute nodes in batch concurrently.
		batchOK := true
		var bwg sync.WaitGroup
		type batchResult struct {
			nodeID string
			result UpgradeNodeResult
		}
		batchCh := make(chan batchResult, len(batch))

		for _, nodeID := range batch {
			bwg.Add(1)
			go func(nid string) {
				defer bwg.Done()
				r := m.dnfUpgradeNode(execCtx, opID, nid, toBuild)
				r.Phase = "compute"
				batchCh <- batchResult{nodeID: nid, result: r}
			}(nodeID)
		}
		bwg.Wait()
		close(batchCh)

		for br := range batchCh {
			recordResult(br.nodeID, br.result)
			if !br.result.OK {
				batchOK = false
				log.Warn().
					Str("node_id", br.nodeID).
					Str("error", br.result.Error).
					Msg("slurm: upgrade: compute node failed in batch")
			}
		}

		// Step 4: Resume batch nodes (even if some failed, to avoid permanent drain).
		if controllerID != "" {
			slurmNodeNames := m.clustrIDsToSlurmNames(ctx, batch)
			if err := m.sendAdminCmd(execCtx, controllerID, clientd.SlurmAdminCmdPayload{
				Command: "resume",
				Nodes:   slurmNodeNames,
			}); err != nil {
				log.Warn().Err(err).Int("batch", batchIdx+1).Msg("slurm: upgrade: resume failed for batch")
			}
		}

		if !batchOK {
			return m.failUpgrade(ctx, opID, fmt.Errorf("batch %d had node failures — upgrade stopped", batchIdx+1))
		}
	}

	// ── Phase 4: Login nodes ──────────────────────────────────────────────
	if len(plan.LoginNodes) > 0 {
		log.Info().Str("op_id", opID).Strs("nodes", plan.LoginNodes).Msg("slurm: upgrade: phase login")
		if err := m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
			Status:       "in_progress",
			Phase:        "login",
			TotalBatches: totalBatches,
		}); err != nil {
			log.Warn().Err(err).Msg("slurm: upgrade: failed to update phase to login")
		}
		for _, nodeID := range plan.LoginNodes {
			if err := m.checkPauseCancel(execCtx, state); err != nil {
				return m.pauseOrCancelUpgrade(ctx, opID, nodeResults)
			}
			result := m.dnfUpgradeNode(execCtx, opID, nodeID, toBuild)
			result.Phase = "login"
			recordResult(nodeID, result)
			// Login node failures are non-fatal (no daemon to restart).
			if !result.OK {
				log.Warn().Str("node_id", nodeID).Str("error", result.Error).
					Msg("slurm: upgrade: login node failed (non-fatal)")
			}
		}
	}

	// ── Complete: set active build ────────────────────────────────────────
	if err := m.db.SlurmSetActiveBuild(ctx, req.ToBuildID); err != nil {
		log.Error().Err(err).Str("op_id", opID).Msg("slurm: upgrade: failed to set active build after completion")
	}

	now := time.Now().Unix()
	nodeResultsMu.Lock()
	finalResults := make(map[string]UpgradeNodeResult, len(nodeResults))
	for k, v := range nodeResults {
		finalResults[k] = v
	}
	nodeResultsMu.Unlock()

	nrJSON, _ := json.Marshal(finalResults)
	if err := m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
		Status:       "completed",
		Phase:        "done",
		TotalBatches: totalBatches,
		CompletedAt:  &now,
		NodeResults:  json.RawMessage(nrJSON),
	}); err != nil {
		log.Error().Err(err).Str("op_id", opID).Msg("slurm: upgrade: failed to mark completed")
	}

	log.Info().Str("op_id", opID).Str("version", toBuild.Version).
		Msg("slurm: upgrade: rolling upgrade completed successfully")
	return nil
}

// ─── Phase helpers ────────────────────────────────────────────────────────────

// pushBinaryToNode sends a slurm_binary_push to one node and waits for the ack.
func (m *Manager) pushBinaryToNode(ctx context.Context, opID, nodeID string, build *db.SlurmBuildRow) UpgradeNodeResult {
	if m.hub == nil {
		return UpgradeNodeResult{OK: false, Error: "hub not available"}
	}
	if !m.hub.IsConnected(nodeID) {
		return UpgradeNodeResult{OK: false, Error: "node offline (clustr-clientd not connected)"}
	}

	// Generate a signed artifact URL for the node to download.
	artifactURL, err := m.GenerateArtifactURL(build.ID)
	if err != nil {
		return UpgradeNodeResult{OK: false, Error: "generate artifact URL: " + err.Error()}
	}

	msgID := uuid.New().String()
	payload, err := json.Marshal(clientd.SlurmBinaryPushPayload{
		BuildID:     build.ID,
		Version:     build.Version,
		ArtifactURL: artifactURL,
		Checksum:    build.ArtifactChecksum,
	})
	if err != nil {
		return UpgradeNodeResult{OK: false, Error: "marshal payload: " + err.Error()}
	}

	serverMsg := clientd.ServerMessage{
		Type:    "slurm_binary_push",
		MsgID:   msgID,
		Payload: json.RawMessage(payload),
	}

	ackCh := m.hub.RegisterAck(msgID)
	defer m.hub.UnregisterAck(msgID)

	if err := m.hub.Send(nodeID, serverMsg); err != nil {
		return UpgradeNodeResult{OK: false, Error: "send failed: " + err.Error()}
	}

	log.Info().
		Str("node_id", nodeID).
		Str("msg_id", msgID).
		Str("version", build.Version).
		Msg("slurm: upgrade: slurm_binary_push sent, waiting for ack")

	// Download can take a while (30 min configured in slurminstall.go).
	const binaryPushAckTimeout = 35 * time.Minute

	select {
	case ack := <-ackCh:
		// Try to parse SlurmBinaryAckPayload from AckPayload.Error.
		var binAck clientd.SlurmBinaryAckPayload
		if err := json.Unmarshal([]byte(ack.Error), &binAck); err == nil {
			return UpgradeNodeResult{
				OK:               binAck.OK,
				Error:            binAck.Error,
				InstalledVersion: binAck.InstalledVersion,
			}
		}
		// Fallback: use generic ack fields.
		return UpgradeNodeResult{OK: ack.OK, Error: ack.Error}

	case <-time.After(binaryPushAckTimeout):
		return UpgradeNodeResult{
			OK:    false,
			Error: fmt.Sprintf("ack timeout after %s", binaryPushAckTimeout),
		}

	case <-ctx.Done():
		return UpgradeNodeResult{OK: false, Error: "context cancelled"}
	}
}

// dnfUpgradeNode sends a slurm_dnf_upgrade to one node and waits for the ack.
// This is the primary upgrade path (Sprint 17+). The node installs from
// clustr-internal-repo via dnf, then reports the installed version back.
func (m *Manager) dnfUpgradeNode(ctx context.Context, opID, nodeID string, build *db.SlurmBuildRow) UpgradeNodeResult {
	if m.hub == nil {
		return UpgradeNodeResult{OK: false, Error: "hub not available"}
	}
	if !m.hub.IsConnected(nodeID) {
		return UpgradeNodeResult{OK: false, Error: "node offline (clustr-clientd not connected)"}
	}

	// Build the package spec list for this build.
	// e.g. "slurm-25.11.5-clustr1.el9", "slurmd-25.11.5-clustr1.el9", ...
	pkgSpecs := buildPkgSpecs(build.Version)

	msgID := uuid.New().String()
	payload, err := json.Marshal(clientd.SlurmDnfUpgradePayload{
		BuildID:  build.ID,
		Version:  build.Version,
		PkgSpecs: pkgSpecs,
	})
	if err != nil {
		return UpgradeNodeResult{OK: false, Error: "marshal payload: " + err.Error()}
	}

	serverMsg := clientd.ServerMessage{
		Type:    "slurm_dnf_upgrade",
		MsgID:   msgID,
		Payload: json.RawMessage(payload),
	}

	ackCh := m.hub.RegisterAck(msgID)
	defer m.hub.UnregisterAck(msgID)

	if err := m.hub.Send(nodeID, serverMsg); err != nil {
		return UpgradeNodeResult{OK: false, Error: "send failed: " + err.Error()}
	}

	log.Info().
		Str("node_id", nodeID).
		Str("msg_id", msgID).
		Str("version", build.Version).
		Strs("pkg_specs", pkgSpecs).
		Msg("slurm: upgrade: slurm_dnf_upgrade sent, waiting for ack")

	// dnf installs can take a while (30 min is conservative but safe).
	const dnfUpgradeAckTimeout = 35 * time.Minute

	select {
	case ack := <-ackCh:
		// Try to parse SlurmDnfUpgradeAckPayload from AckPayload.Error.
		var dnfAck clientd.SlurmDnfUpgradeAckPayload
		if err := json.Unmarshal([]byte(ack.Error), &dnfAck); err == nil {
			return UpgradeNodeResult{
				OK:               dnfAck.OK,
				Error:            dnfAck.Error,
				InstalledVersion: dnfAck.InstalledVersion,
			}
		}
		// Fallback: use generic ack fields.
		return UpgradeNodeResult{OK: ack.OK, Error: ack.Error}

	case <-time.After(dnfUpgradeAckTimeout):
		return UpgradeNodeResult{
			OK:    false,
			Error: fmt.Sprintf("ack timeout after %s", dnfUpgradeAckTimeout),
		}

	case <-ctx.Done():
		return UpgradeNodeResult{OK: false, Error: "context cancelled"}
	}
}

// buildPkgSpecs returns the dnf package spec list for a given Slurm version.
// The specs match the RPMs built by buildSlurmRPMs (repo.go).
func buildPkgSpecs(slurmVersion string) []string {
	subPkgs := []string{"slurm", "slurmd", "slurmctld", "slurmdbd", "slurm-libs", "slurm-pam_slurm"}
	specs := make([]string, 0, len(subPkgs))
	for _, pkg := range subPkgs {
		specs = append(specs, fmt.Sprintf("%s-%s-clustr1", pkg, slurmVersion))
	}
	return specs
}

// recordNodeVersion persists the per-node deployed version after a successful upgrade.
// Extracted here for use by one-off recovery installs outside the rolling upgrade flow.
func (m *Manager) recordNodeVersion(ctx context.Context, nodeID string, build *db.SlurmBuildRow, result UpgradeNodeResult, method string) {
	ver := result.InstalledVersion
	if ver == "" {
		ver = build.Version // best-effort if sinfo --version wasn't available
	}
	if err := m.db.SlurmUpsertNodeVersion(ctx, db.SlurmNodeVersionRow{
		NodeID:          nodeID,
		DeployedVersion: ver,
		BuildID:         build.ID,
		InstallMethod:   method,
		InstalledAt:     time.Now().Unix(),
		InstalledBy:     "clustr-server",
	}); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("slurm: failed to record node version (non-fatal)")
	}
}

// sendAdminCmd sends a slurm_admin_cmd to the controller and waits for the result.
func (m *Manager) sendAdminCmd(ctx context.Context, controllerNodeID string, cmd clientd.SlurmAdminCmdPayload) error {
	if m.hub == nil || !m.hub.IsConnected(controllerNodeID) {
		return fmt.Errorf("controller node %s is offline", controllerNodeID)
	}

	msgID := uuid.New().String()
	payload, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("marshal admin cmd: %w", err)
	}

	serverMsg := clientd.ServerMessage{
		Type:    "slurm_admin_cmd",
		MsgID:   msgID,
		Payload: json.RawMessage(payload),
	}

	ackCh := m.hub.RegisterAck(msgID)
	defer m.hub.UnregisterAck(msgID)

	if err := m.hub.Send(controllerNodeID, serverMsg); err != nil {
		return fmt.Errorf("send admin cmd: %w", err)
	}

	select {
	case ack := <-ackCh:
		var result clientd.SlurmAdminCmdResult
		if err := json.Unmarshal([]byte(ack.Error), &result); err == nil {
			if !result.OK {
				return fmt.Errorf("admin cmd %s failed: %s", cmd.Command, result.Error)
			}
			return nil
		}
		if !ack.OK {
			return fmt.Errorf("admin cmd %s ack failed: %s", cmd.Command, ack.Error)
		}
		return nil

	case <-time.After(nodeAckTimeout):
		return fmt.Errorf("admin cmd %s timed out after %s", cmd.Command, nodeAckTimeout)

	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendAdminCmdWithResult sends a slurm_admin_cmd and returns the full result.
func (m *Manager) sendAdminCmdWithResult(ctx context.Context, controllerNodeID string, cmd clientd.SlurmAdminCmdPayload) (*clientd.SlurmAdminCmdResult, error) {
	if m.hub == nil || !m.hub.IsConnected(controllerNodeID) {
		return nil, fmt.Errorf("controller node %s is offline", controllerNodeID)
	}

	msgID := uuid.New().String()
	payload, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshal admin cmd: %w", err)
	}

	serverMsg := clientd.ServerMessage{
		Type:    "slurm_admin_cmd",
		MsgID:   msgID,
		Payload: json.RawMessage(payload),
	}

	ackCh := m.hub.RegisterAck(msgID)
	defer m.hub.UnregisterAck(msgID)

	if err := m.hub.Send(controllerNodeID, serverMsg); err != nil {
		return nil, fmt.Errorf("send admin cmd: %w", err)
	}

	select {
	case ack := <-ackCh:
		var result clientd.SlurmAdminCmdResult
		if err := json.Unmarshal([]byte(ack.Error), &result); err == nil {
			return &result, nil
		}
		// Fallback: construct from generic ack.
		return &clientd.SlurmAdminCmdResult{OK: ack.OK, Error: ack.Error}, nil

	case <-time.After(nodeAckTimeout):
		return nil, fmt.Errorf("admin cmd %s timed out", cmd.Command)

	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// waitForDrain polls squeue on the controller until the given node list shows zero
// running/pending jobs, or the timeout elapses. Returns true if fully drained.
func (m *Manager) waitForDrain(ctx context.Context, controllerID string, slurmNodeNames []string, timeout time.Duration) bool {
	if len(slurmNodeNames) == 0 || controllerID == "" {
		return true
	}

	deadline := time.Now().Add(timeout)
	pollInterval := 30 * time.Second

	for time.Now().Before(deadline) {
		result, err := m.sendAdminCmdWithResult(ctx, controllerID, clientd.SlurmAdminCmdPayload{
			Command: "check_queue",
			Nodes:   slurmNodeNames,
		})
		if err != nil {
			log.Warn().Err(err).Msg("slurm: upgrade: check_queue failed during drain wait")
		} else if result.OK && result.JobCount == 0 {
			log.Info().Strs("nodes", slurmNodeNames).Msg("slurm: upgrade: all nodes drained")
			return true
		} else if result != nil {
			log.Debug().Int("job_count", result.JobCount).Msg("slurm: upgrade: waiting for drain")
		}

		// Check for context cancellation or pause.
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pollInterval):
		}
	}

	return false
}

// ─── Plan + state helpers ─────────────────────────────────────────────────────

// buildUpgradePlan builds the upgrade execution plan from node role assignments.
func (m *Manager) buildUpgradePlan(ctx context.Context, req UpgradeRequest) (*UpgradePlan, []string) {
	batchSize := req.BatchSize
	if batchSize <= 0 {
		batchSize = 10
	}

	plan := &UpgradePlan{}
	var warnings []string

	dbdNodes, _ := m.db.SlurmListNodesByRole(ctx, RoleDBD)
	plan.DBDNodes = dbdNodes

	ctrlNodes, _ := m.db.SlurmListNodesByRole(ctx, RoleController)
	plan.ControllerNodes = ctrlNodes

	computeNodes, _ := m.db.SlurmListNodesByRole(ctx, RoleCompute)
	plan.ComputeBatches = batchSlice(computeNodes, batchSize)

	loginNodes, _ := m.db.SlurmListNodesByRole(ctx, RoleLogin)
	plan.LoginNodes = loginNodes

	if len(dbdNodes) == 0 && len(ctrlNodes) == 0 && len(computeNodes) == 0 {
		warnings = append(warnings, "no nodes with Slurm roles assigned — upgrade will have no effect")
	}

	return plan, warnings
}

// batchSlice splits a slice into batches of size n.
func batchSlice(items []string, n int) [][]string {
	if len(items) == 0 || n <= 0 {
		return nil
	}
	var batches [][]string
	for i := 0; i < len(items); i += n {
		end := i + n
		if end > len(items) {
			end = len(items)
		}
		batches = append(batches, items[i:end])
	}
	return batches
}

// collectAllPlanNodes returns all node IDs in the plan (for connectivity checks).
func collectAllPlanNodes(plan *UpgradePlan) []string {
	if plan == nil {
		return nil
	}
	var all []string
	all = append(all, plan.DBDNodes...)
	all = append(all, plan.ControllerNodes...)
	for _, batch := range plan.ComputeBatches {
		all = append(all, batch...)
	}
	all = append(all, plan.LoginNodes...)
	return all
}

// clustrIDsToSlurmNames maps clustr node UUIDs to Slurm node names.
// For now, we use the node hostname (from the nodes table via heartbeat) or
// fall back to the UUID if the hostname is not available.
// This assumes Slurm NodeName matches the hostname recorded at deploy time.
func (m *Manager) clustrIDsToSlurmNames(ctx context.Context, nodeIDs []string) []string {
	names := make([]string, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		hostname, err := m.db.NodeGetHostname(ctx, id)
		if err != nil || hostname == "" {
			names = append(names, id) // fall back to UUID
		} else {
			names = append(names, hostname)
		}
	}
	return names
}

// queryJobCount asks the controller for the current job count (best-effort).
func (m *Manager) queryJobCount(ctx context.Context, plan *UpgradePlan) int {
	if plan == nil || len(plan.ControllerNodes) == 0 || m.hub == nil {
		return -1
	}
	controllerID := plan.ControllerNodes[0]
	result, err := m.sendAdminCmdWithResult(ctx, controllerID, clientd.SlurmAdminCmdPayload{
		Command: "check_queue",
	})
	if err != nil || result == nil || !result.OK {
		return -1
	}
	return result.JobCount
}

// hasInProgressUpgrade checks the DB for any upgrade operation with status in_progress.
func (m *Manager) hasInProgressUpgrade(ctx context.Context) bool {
	ops, err := m.db.SlurmListUpgradeOps(ctx)
	if err != nil {
		return false
	}
	for _, op := range ops {
		if op.Status == "in_progress" {
			return true
		}
	}
	return false
}

// checkPauseCancel checks if the upgrade should be paused or cancelled.
// Returns a non-nil error if execution should stop.
func (m *Manager) checkPauseCancel(ctx context.Context, state *upgradeState) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	m.upgradeMu.RLock()
	paused := state.paused
	m.upgradeMu.RUnlock()
	if paused {
		return fmt.Errorf("upgrade paused")
	}
	return nil
}

// pauseOrCancelUpgrade marks the upgrade as paused (if paused) or cancelled.
func (m *Manager) pauseOrCancelUpgrade(ctx context.Context, opID string, nodeResults map[string]UpgradeNodeResult) error {
	m.upgradeMu.RLock()
	var paused bool
	if m.activeUpgrade != nil && m.activeUpgrade.opID == opID {
		paused = m.activeUpgrade.paused
	}
	m.upgradeMu.RUnlock()

	status := "failed"
	if paused {
		status = "paused"
	}

	nrJSON, _ := json.Marshal(nodeResults)
	_ = m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
		Status:      status,
		NodeResults: json.RawMessage(nrJSON),
	})
	return fmt.Errorf("upgrade %s", status)
}

// failUpgrade marks the upgrade as failed and returns the error.
func (m *Manager) failUpgrade(ctx context.Context, opID string, cause error) error {
	log.Error().Err(cause).Str("op_id", opID).Msg("slurm: upgrade: failed")
	now := time.Now().Unix()
	_ = m.db.SlurmUpdateUpgradeOp(ctx, opID, db.SlurmUpgradeOpUpdate{
		Status:      "failed",
		CompletedAt: &now,
	})
	return cause
}

// persistNodeResults writes the accumulated per-node results to the DB without
// disturbing phase/current_batch/total_batches fields.
func (m *Manager) persistNodeResults(ctx context.Context, opID string, results map[string]UpgradeNodeResult) {
	nrJSON, err := json.Marshal(results)
	if err != nil {
		log.Warn().Err(err).Str("op_id", opID).Msg("slurm: upgrade: failed to marshal node results")
		return
	}
	if err := m.db.SlurmUpdateUpgradeOpResults(ctx, opID, json.RawMessage(nrJSON)); err != nil {
		log.Warn().Err(err).Str("op_id", opID).Msg("slurm: upgrade: failed to persist node results")
	}
}

// ─── API conversion ───────────────────────────────────────────────────────────

func upgradeOpRowToAPI(row *db.SlurmUpgradeOpRow) *UpgradeOperation {
	op := &UpgradeOperation{
		ID:                row.ID,
		FromBuildID:       row.FromBuildID,
		ToBuildID:         row.ToBuildID,
		Status:            row.Status,
		Phase:             row.Phase,
		CurrentBatch:      row.CurrentBatch,
		TotalBatches:      row.TotalBatches,
		BatchSize:         row.BatchSize,
		DrainTimeoutMin:   row.DrainTimeoutMin,
		ConfirmedDBBackup: row.ConfirmedDBBackup,
		InitiatedBy:       row.InitiatedBy,
		StartedAt:         row.StartedAt,
		CompletedAt:       row.CompletedAt,
	}
	if len(row.NodeResults) > 0 {
		var results map[string]UpgradeNodeResult
		if err := json.Unmarshal(row.NodeResults, &results); err == nil {
			op.NodeResults = results
		}
	}
	return op
}

