// push.go — Cluster-wide Slurm config push orchestration.
// Manager.executePush fans out slurm_config_push messages to all target nodes
// concurrently via the ClientdHub, collects acks, updates per-node config state,
// and finalises the push operation record in the DB.
package slurm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

const (
	// nodeAckTimeout is how long to wait for a single node to ack a push.
	nodeAckTimeout = 60 * time.Second
)

// PushRequest is the input for a cluster-wide Slurm config push.
type PushRequest struct {
	// Filenames is the list of managed config files to push.
	// Empty means all files relevant to each node's roles.
	Filenames []string `json:"filenames"`
	// ScriptTypes is an optional list of script types to include in the push
	// (e.g. ["Prolog", "Epilog"]). Empty means include all enabled scripts
	// relevant to each node's roles. Scripts are written to their configured
	// dest_path with mode 0755 but do NOT trigger scontrol reconfigure.
	ScriptTypes []string `json:"script_types,omitempty"`
	// ApplyAction is "reconfigure" (scontrol reconfigure) or "restart" (systemctl restart).
	ApplyAction string `json:"apply_action"`
	// TargetNodes is an optional subset of node IDs to push to.
	// Empty means all nodes that have Slurm roles assigned.
	TargetNodes []string `json:"target_nodes,omitempty"`
}

// nodeWork holds the pre-computed files and scripts to send to a specific node.
type nodeWork struct {
	nodeID       string
	files        []clientd.SlurmFilePush
	scripts      []clientd.SlurmScriptPush
	isController bool
}

// nodeOutcome holds the result of a push attempt to one node.
type nodeOutcome struct {
	nodeID string
	result api.SlurmNodeResult
}

// executePushWithID runs push orchestration using a pre-created push op record.
// The op must already exist in the DB with status "pending" or "in_progress".
// It is called from StartPush's background goroutine.
func (m *Manager) executePushWithID(ctx context.Context, opID string, req PushRequest, initiatedBy string) (*api.SlurmPushOperation, error) {
	return m.executePushCore(ctx, opID, req, initiatedBy)
}

// executePush creates a new push op record and runs the orchestration.
// Called by Manager.Push() for synchronous use (tests, internal callers).
func (m *Manager) executePush(ctx context.Context, req PushRequest, initiatedBy string) (*api.SlurmPushOperation, error) {
	return m.executePushCore(ctx, "", req, initiatedBy)
}

// executePushCore is the main push orchestration function.
// If opID is empty, it creates a new push operation record. If opID is provided,
// it updates an existing record (already created by StartPush).
func (m *Manager) executePushCore(ctx context.Context, existingOpID string, req PushRequest, initiatedBy string) (*api.SlurmPushOperation, error) {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()

	if cfg == nil || !cfg.Enabled {
		return nil, fmt.Errorf("slurm: module is not enabled")
	}

	// ── 1. Determine target nodes ──────────────────────────────────────────

	var targetNodeIDs []string
	if len(req.TargetNodes) > 0 {
		targetNodeIDs = req.TargetNodes
	} else {
		// All nodes that have any Slurm role assigned.
		roleEntries, err := m.db.SlurmListAllNodeRoles(ctx)
		if err != nil {
			return nil, fmt.Errorf("slurm: list node roles: %w", err)
		}
		for _, entry := range roleEntries {
			if len(entry.Roles) > 0 {
				targetNodeIDs = append(targetNodeIDs, entry.NodeID)
			}
		}
	}

	if len(targetNodeIDs) == 0 {
		return nil, fmt.Errorf("slurm: no target nodes — assign roles first")
	}

	// ── 2. Determine files to push and build file versions map ────────────

	managedFiles := req.Filenames
	if len(managedFiles) == 0 {
		managedFiles = cfg.ManagedFiles
	}

	fileVersions := make(map[string]int)
	for _, fn := range managedFiles {
		row, err := m.db.SlurmGetCurrentConfig(ctx, fn)
		if err == nil && row != nil {
			fileVersions[fn] = row.Version
		}
	}

	// ── 3. Create or reuse push operation record ──────────────────────────

	opID := existingOpID
	now := time.Now().Unix()

	if opID == "" {
		// Synchronous path (Manager.Push): create a new record.
		opID = uuid.New().String()
		opRow := db.SlurmPushOperationRow{
			ID:           opID,
			Filenames:    managedFiles,
			FileVersions: fileVersions,
			InitiatedBy:  initiatedBy,
			ApplyAction:  req.ApplyAction,
			Status:       "in_progress",
			NodeCount:    len(targetNodeIDs),
			StartedAt:    now,
		}
		if err := m.db.SlurmCreatePushOp(ctx, opRow); err != nil {
			return nil, fmt.Errorf("slurm: create push op: %w", err)
		}
	} else {
		// Async path (StartPush): record already exists; update to in_progress.
		if err := m.db.SlurmUpdatePushOp(ctx, opID, db.SlurmPushOpUpdate{
			Status: "in_progress",
		}); err != nil {
			log.Warn().Err(err).Str("op_id", opID).Msg("slurm: push: could not update op to in_progress")
		}
	}

	log.Info().
		Str("op_id", opID).
		Strs("files", managedFiles).
		Str("apply_action", req.ApplyAction).
		Int("node_count", len(targetNodeIDs)).
		Msg("slurm: push operation started")

	// ── 4. Build per-node work items (render files, filter by role) ────────

	var (
		works       []nodeWork
		controllerIDs []string
	)

	for _, nodeID := range targetNodeIDs {
		roles, err := m.db.SlurmGetNodeRoles(ctx, nodeID)
		if err != nil {
			roles = []string{}
		}

		// Determine which files this node should receive.
		nodeFiles := FilesForRoles(roles)
		if len(nodeFiles) == 0 {
			nodeFiles = managedFiles // fallback: send all if no role-specific files
		}

		// Intersect with the requested filenames.
		nodeFiles = intersect(managedFiles, nodeFiles)

		if len(nodeFiles) == 0 {
			log.Warn().Str("node_id", nodeID).Msg("slurm: push: no files applicable for node after role filter")
			continue
		}

		// Render all files for this node.
		rendered, err := m.RenderAllForNode(ctx, nodeID)
		if err != nil {
			log.Warn().Err(err).Str("node_id", nodeID).Msg("slurm: push: render failed for node")
			rendered = make(map[string]string)
		}

		var filePushes []clientd.SlurmFilePush
		for _, fn := range nodeFiles {
			content, ok := rendered[fn]
			if !ok {
				// Try fetching raw content as fallback.
				row, dbErr := m.db.SlurmGetCurrentConfig(ctx, fn)
				if dbErr != nil {
					log.Warn().Err(dbErr).Str("node_id", nodeID).Str("file", fn).
						Msg("slurm: push: file not in DB, skipping")
					continue
				}
				content = row.Content
			}

			sum := sha256.Sum256([]byte(content))
			filePushes = append(filePushes, clientd.SlurmFilePush{
				Filename: fn,
				Content:  content,
				Checksum: fmt.Sprintf("sha256:%x", sum),
				DestPath: "/etc/slurm/" + fn,
			})
		}

		if len(filePushes) == 0 {
			continue
		}

		// ── Build script payloads for this node ──────────────────────────
		//
		// Determine which script types apply to this node's roles.
		nodeScriptTypes := ScriptTypesForRoles(roles)
		// If the caller specified an explicit list, intersect with role-relevant types.
		if len(req.ScriptTypes) > 0 {
			nodeScriptTypes = intersect(req.ScriptTypes, nodeScriptTypes)
		}

		var scriptPushes []clientd.SlurmScriptPush
		if len(nodeScriptTypes) > 0 {
			// Fetch enabled script configs.
			scriptCfgs, _ := m.db.SlurmListScriptConfigs(ctx)
			enabledMap := make(map[string]string) // scriptType → destPath
			for _, sc := range scriptCfgs {
				if sc.Enabled {
					enabledMap[sc.ScriptType] = sc.DestPath
				}
			}

			for _, st := range nodeScriptTypes {
				destPath, ok := enabledMap[st]
				if !ok {
					continue // script not enabled
				}
				row, err := m.db.SlurmGetCurrentScript(ctx, st)
				if err != nil || row == nil {
					log.Warn().Str("node_id", nodeID).Str("script_type", st).
						Msg("slurm: push: script has no saved version, skipping")
					continue
				}
				sum := sha256.Sum256([]byte(row.Content))
				scriptPushes = append(scriptPushes, clientd.SlurmScriptPush{
					ScriptType: st,
					Content:    row.Content,
					Checksum:   fmt.Sprintf("sha256:%x", sum),
					DestPath:   destPath,
					Version:    row.Version,
				})
			}
		}

		isCtrl := hasRole(roles, RoleController)
		if isCtrl {
			controllerIDs = append(controllerIDs, nodeID)
		}

		works = append(works, nodeWork{
			nodeID:       nodeID,
			files:        filePushes,
			scripts:      scriptPushes,
			isController: isCtrl,
		})
	}

	// ── 5. Fan-out pushes concurrently; controller last for "restart" ──────
	//
	// For restart: dispatch compute/login/dbd nodes first, then controller.
	// For reconfigure: order doesn't matter — all concurrent.

	outcomeCh := make(chan nodeOutcome, len(works))
	var wg sync.WaitGroup

	dispatchNode := func(w nodeWork) {
		defer wg.Done()
		outcome := m.pushToNode(ctx, opID, req.ApplyAction, w)
		outcomeCh <- outcome
	}

	if req.ApplyAction == "restart" && len(controllerIDs) > 0 {
		// Dispatch non-controller nodes first.
		var controllerWorks []nodeWork
		for _, w := range works {
			if w.isController {
				controllerWorks = append(controllerWorks, w)
				continue
			}
			wg.Add(1)
			go dispatchNode(w)
		}
		// Wait for all non-controller nodes to complete before firing controller.
		wg.Wait()

		// Now dispatch controller nodes.
		var ctrlWg sync.WaitGroup
		for _, w := range controllerWorks {
			ctrlWg.Add(1)
			go func(w nodeWork) {
				defer ctrlWg.Done()
				outcome := m.pushToNode(ctx, opID, req.ApplyAction, w)
				outcomeCh <- outcome
			}(w)
		}
		ctrlWg.Wait()
	} else {
		for _, w := range works {
			wg.Add(1)
			go dispatchNode(w)
		}
		wg.Wait()
	}

	close(outcomeCh)

	// ── 6. Collect results and update state ────────────────────────────────

	nodeResults := make(map[string]api.SlurmNodeResult)
	successCount := 0
	failureCount := 0

	// Build a map of nodeID → scripts sent, for state update after ack.
	nodeScriptsSent := make(map[string][]clientd.SlurmScriptPush, len(works))
	for _, w := range works {
		nodeScriptsSent[w.nodeID] = w.scripts
	}

	for outcome := range outcomeCh {
		nodeResults[outcome.nodeID] = outcome.result
		if outcome.result.OK {
			successCount++
			// Update per-file config state for this node.
			m.updateNodeConfigState(ctx, opID, outcome.nodeID, outcome.result.FileResults, managedFiles, fileVersions)
			// Update per-script state for this node.
			m.updateNodeScriptState(ctx, opID, outcome.nodeID, outcome.result.ScriptResults, nodeScriptsSent[outcome.nodeID])
		} else {
			failureCount++
		}
	}

	// Also count nodes that were in targetNodeIDs but had no work items
	// (e.g. no applicable files after role filter).
	worked := make(map[string]bool)
	for _, w := range works {
		worked[w.nodeID] = true
	}
	for _, nodeID := range targetNodeIDs {
		if !worked[nodeID] {
			// No applicable files — not a failure, but log it.
			log.Info().Str("node_id", nodeID).Msg("slurm: push: node had no applicable files, skipped")
		}
	}

	// ── 7. Finalise push operation ─────────────────────────────────────────

	completedAt := time.Now().Unix()
	status := "completed"
	switch {
	case failureCount > 0 && successCount > 0:
		status = "partial"
	case failureCount > 0 && successCount == 0:
		status = "failed"
	}

	nodeResultsJSON, _ := json.Marshal(nodeResults)
	update := db.SlurmPushOpUpdate{
		Status:       status,
		SuccessCount: successCount,
		FailureCount: failureCount,
		CompletedAt:  &completedAt,
		NodeResults:  json.RawMessage(nodeResultsJSON),
	}
	if err := m.db.SlurmUpdatePushOp(ctx, opID, update); err != nil {
		log.Error().Err(err).Str("op_id", opID).Msg("slurm: push: failed to update push op status")
	}

	log.Info().
		Str("op_id", opID).
		Str("status", status).
		Int("success", successCount).
		Int("failure", failureCount).
		Msg("slurm: push operation completed")

	// Return the full push op record.
	finalRow, err := m.db.SlurmGetPushOp(ctx, opID)
	if err != nil {
		// Construct from what we know if DB read fails.
		op := &api.SlurmPushOperation{
			ID:           opID,
			Filenames:    managedFiles,
			FileVersions: fileVersions,
			ApplyAction:  req.ApplyAction,
			Status:       status,
			NodeCount:    len(targetNodeIDs),
			SuccessCount: successCount,
			FailureCount: failureCount,
			StartedAt:    now,
			CompletedAt:  &completedAt,
			NodeResults:  nodeResults,
		}
		return op, nil
	}
	return pushOpRowToAPI(finalRow), nil
}

// pushToNode sends a slurm_config_push to one node and waits for the ack.
// Returns a nodeOutcome describing success, failure, or offline/timeout.
func (m *Manager) pushToNode(ctx context.Context, opID, applyAction string, w nodeWork) nodeOutcome {
	if m.hub == nil {
		return nodeOutcome{
			nodeID: w.nodeID,
			result: api.SlurmNodeResult{
				OK:    false,
				Error: "hub not available",
			},
		}
	}

	// If the node is not connected, record it as an offline failure immediately.
	if !m.hub.IsConnected(w.nodeID) {
		log.Warn().Str("node_id", w.nodeID).Msg("slurm: push: node offline, skipping")
		return nodeOutcome{
			nodeID: w.nodeID,
			result: api.SlurmNodeResult{
				OK:    false,
				Error: "node offline (clustr-clientd not connected)",
			},
		}
	}

	msgID := uuid.New().String()

	payload, err := json.Marshal(clientd.SlurmConfigPushPayload{
		PushOpID:    opID,
		Files:       w.files,
		Scripts:     w.scripts,
		ApplyAction: applyAction,
	})
	if err != nil {
		return nodeOutcome{
			nodeID: w.nodeID,
			result: api.SlurmNodeResult{OK: false, Error: "marshal payload: " + err.Error()},
		}
	}

	serverMsg := clientd.ServerMessage{
		Type:    "slurm_config_push",
		MsgID:   msgID,
		Payload: json.RawMessage(payload),
	}

	// Register ack channel before sending to avoid a race.
	ackCh := m.hub.RegisterAck(msgID)
	defer m.hub.UnregisterAck(msgID)

	if err := m.hub.Send(w.nodeID, serverMsg); err != nil {
		return nodeOutcome{
			nodeID: w.nodeID,
			result: api.SlurmNodeResult{OK: false, Error: "send failed: " + err.Error()},
		}
	}

	log.Info().
		Str("node_id", w.nodeID).
		Str("msg_id", msgID).
		Int("files", len(w.files)).
		Int("scripts", len(w.scripts)).
		Msg("slurm: push: slurm_config_push sent, waiting for ack")

	// Wait for ack, timeout, or context cancellation.
	select {
	case ack := <-ackCh:
		return m.interpretSlurmAck(w.nodeID, ack)

	case <-time.After(nodeAckTimeout):
		log.Warn().Str("node_id", w.nodeID).Str("msg_id", msgID).
			Msg("slurm: push: ack timeout for node")
		return nodeOutcome{
			nodeID: w.nodeID,
			result: api.SlurmNodeResult{
				OK:    false,
				Error: fmt.Sprintf("ack timeout after %s", nodeAckTimeout),
			},
		}

	case <-ctx.Done():
		return nodeOutcome{
			nodeID: w.nodeID,
			result: api.SlurmNodeResult{OK: false, Error: "context cancelled"},
		}
	}
}

// interpretSlurmAck parses the ack returned from a node.
// The node encodes SlurmConfigAckPayload as JSON in AckPayload.Error (see
// client.go sendSlurmAck). We unmarshal it to get per-file results and apply output.
func (m *Manager) interpretSlurmAck(nodeID string, ack clientd.AckPayload) nodeOutcome {
	// The Error field carries the JSON-encoded SlurmConfigAckPayload.
	var slurmAck clientd.SlurmConfigAckPayload
	if err := json.Unmarshal([]byte(ack.Error), &slurmAck); err != nil {
		// Fallback: treat the raw AckPayload.OK as the result.
		log.Warn().Err(err).Str("node_id", nodeID).
			Msg("slurm: push: could not parse slurm ack detail; using generic ok field")
		return nodeOutcome{
			nodeID: nodeID,
			result: api.SlurmNodeResult{
				OK:    ack.OK,
				Error: ack.Error,
			},
		}
	}

	// Convert SlurmFileApplyResult → api.SlurmFileResult.
	fileResults := make([]api.SlurmFileResult, 0, len(slurmAck.FileResults))
	for _, fr := range slurmAck.FileResults {
		fileResults = append(fileResults, api.SlurmFileResult{
			Filename: fr.Filename,
			OK:       fr.OK,
			Error:    fr.Error,
		})
	}

	// Convert SlurmScriptApplyResult → api.SlurmScriptResult.
	scriptResults := make([]api.SlurmScriptResult, 0, len(slurmAck.ScriptResults))
	for _, sr := range slurmAck.ScriptResults {
		scriptResults = append(scriptResults, api.SlurmScriptResult{
			ScriptType: sr.ScriptType,
			OK:         sr.OK,
			Error:      sr.Error,
		})
	}

	result := api.SlurmNodeResult{
		OK:            slurmAck.OK,
		Error:         slurmAck.Error,
		FileResults:   fileResults,
		ScriptResults: scriptResults,
		ApplyResult: api.SlurmApplyResult{
			Action:   "", // will be set by caller context
			OK:       slurmAck.OK,
			ExitCode: slurmAck.ApplyExitCode,
			Output:   slurmAck.ApplyOutput,
		},
	}

	return nodeOutcome{nodeID: nodeID, result: result}
}

// updateNodeConfigState updates slurm_node_config_state for each successfully
// written file in the ack result.
func (m *Manager) updateNodeConfigState(
	ctx context.Context,
	opID, nodeID string,
	fileResults []api.SlurmFileResult,
	managedFiles []string,
	fileVersions map[string]int,
) {
	// Build a set of successfully written files.
	okFiles := make(map[string]bool)
	for _, fr := range fileResults {
		if fr.OK {
			okFiles[fr.Filename] = true
		}
	}

	// If fileResults is empty but the node result was OK, assume all files succeeded.
	if len(fileResults) == 0 {
		for _, fn := range managedFiles {
			okFiles[fn] = true
		}
	}

	for fn, ok := range okFiles {
		if !ok {
			continue
		}
		ver := fileVersions[fn]

		// Compute content hash by re-fetching current content.
		// This is the rendered content that was pushed; we approximate with the DB hash.
		row, err := m.db.SlurmGetCurrentConfig(ctx, fn)
		var contentHash string
		if err == nil && row != nil {
			contentHash = row.Checksum
		}

		if err := m.db.SlurmUpsertNodeConfigState(ctx, nodeID, fn, ver, contentHash, opID); err != nil {
			log.Error().Err(err).
				Str("node_id", nodeID).Str("filename", fn).
				Msg("slurm: push: failed to update node config state")
		}
	}
}

// updateNodeScriptState updates slurm_script_state for each successfully
// written script in the ack result.
func (m *Manager) updateNodeScriptState(
	ctx context.Context,
	opID, nodeID string,
	scriptResults []api.SlurmScriptResult,
	sentScripts []clientd.SlurmScriptPush,
) {
	if len(sentScripts) == 0 {
		return
	}

	// Build a set of successfully written script types.
	okScripts := make(map[string]bool)
	for _, sr := range scriptResults {
		if sr.OK {
			okScripts[sr.ScriptType] = true
		}
	}
	// If scriptResults is empty but the push succeeded, assume all scripts were written.
	if len(scriptResults) == 0 {
		for _, s := range sentScripts {
			okScripts[s.ScriptType] = true
		}
	}

	// Build a quick lookup of sent scripts for version + checksum.
	sentMap := make(map[string]clientd.SlurmScriptPush, len(sentScripts))
	for _, s := range sentScripts {
		sentMap[s.ScriptType] = s
	}

	for st, ok := range okScripts {
		if !ok {
			continue
		}
		s, found := sentMap[st]
		if !found {
			continue
		}
		// Derive content hash from the checksum field (strip "sha256:" prefix).
		hash := s.Checksum
		if len(hash) > 7 && hash[:7] == "sha256:" {
			hash = hash[7:]
		}
		if err := m.db.SlurmUpsertScriptState(ctx, nodeID, st, s.Version, hash, opID); err != nil {
			log.Error().Err(err).
				Str("node_id", nodeID).Str("script_type", st).
				Msg("slurm: push: failed to update node script state")
		}
	}
}

// intersect returns the elements of a that are also in b (preserving a's order).
func intersect(a, b []string) []string {
	bSet := make(map[string]struct{}, len(b))
	for _, v := range b {
		bSet[v] = struct{}{}
	}
	var result []string
	for _, v := range a {
		if _, ok := bSet[v]; ok {
			result = append(result, v)
		}
	}
	return result
}
