// routes.go — HTTP route handlers for the Slurm module API.
// All routes are registered under /api/v1/slurm/ and require admin role.
// Follows the same pattern as internal/ldap/routes.go.
package slurm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// RegisterRoutes wires all Slurm API endpoints into the given chi router group.
// All routes require admin role — the caller is responsible for applying the
// requireRole("admin") middleware before calling this function.
func RegisterRoutes(r chi.Router, m *Manager) {
	// Module lifecycle.
	r.Get("/slurm/status", m.handleStatus)
	r.Post("/slurm/enable", m.handleEnable)
	r.Post("/slurm/disable", m.handleDisable)

	// Config file management.
	r.Get("/slurm/configs", m.handleListConfigs)
	r.Get("/slurm/configs/{filename}", m.handleGetConfig)
	r.Put("/slurm/configs/{filename}", m.handleSaveConfig)
	r.Get("/slurm/configs/{filename}/history", m.handleConfigHistory)
	r.Get("/slurm/configs/{filename}/render/{node_id}", m.handleRenderPreview)

	// Sync / drift status.
	r.Get("/slurm/sync-status", m.handleSyncStatus)

	// Push operations.
	r.Post("/slurm/push", m.handlePush)
	r.Get("/slurm/push-ops/{op_id}", m.handlePushOpStatus)

	// Per-node overrides.
	r.Get("/nodes/{node_id}/slurm/overrides", m.handleGetOverrides)
	r.Put("/nodes/{node_id}/slurm/overrides", m.handleSaveOverrides)

	// Per-node roles.
	r.Get("/nodes/{node_id}/slurm/role", m.handleGetRole)
	r.Put("/nodes/{node_id}/slurm/role", m.handleSetRole)
	r.Get("/nodes/{node_id}/slurm/sync-status", m.handleNodeSyncStatus)

	// GAP-17: flat node/role/sync endpoints expected by the walkthrough nav.
	// /slurm/nodes — list all clustr-managed nodes with their Slurm roles.
	r.Get("/slurm/nodes", m.handleSlurmNodes)
	// /slurm/roles — list the available Slurm role strings.
	r.Get("/slurm/roles", m.handleSlurmRoles)
	// /slurm/sync — trigger a push of all managed configs to all worker nodes.
	r.Post("/slurm/sync", m.handleSlurmSync)

	// Role summary and lookup.
	r.Get("/slurm/nodes/by-role/{role}", m.handleNodesByRole)
	r.Get("/slurm/roles/summary", m.handleRoleSummary)

	// Script management.
	r.Get("/slurm/scripts", m.handleListScripts)
	r.Get("/slurm/scripts/configs", m.handleListScriptConfigs)
	r.Get("/slurm/scripts/{script_type}", m.handleGetScript)
	r.Put("/slurm/scripts/{script_type}", m.handleSaveScript)
	r.Get("/slurm/scripts/{script_type}/history", m.handleScriptHistory)
	r.Put("/slurm/scripts/{script_type}/config", m.handleUpsertScriptConfig)

	// Build management (Sprint 8 full pipeline).
	r.Get("/slurm/builds", m.handleListBuilds)
	r.Post("/slurm/builds", m.handleStartBuild)
	r.Get("/slurm/builds/{build_id}", m.handleGetBuild)
	r.Delete("/slurm/builds/{build_id}", m.handleDeleteBuild)
	r.Get("/slurm/builds/{build_id}/artifact", m.handleDownloadArtifact)
	r.Get("/slurm/builds/{build_id}/logs", m.handleBuildLogs)
	r.Post("/slurm/builds/{build_id}/set-active", m.handleSetActiveBuild)

	// Dependency matrix.
	r.Get("/slurm/deps/matrix", m.handleListDepMatrix)

	// Munge key management.
	r.Post("/slurm/munge-key/generate", m.handleGenerateMungeKey)
	r.Post("/slurm/munge-key/rotate", m.handleRotateMungeKey)

	// Rolling upgrade operations (Sprint 9).
	r.Post("/slurm/upgrades/validate", m.handleValidateUpgrade)
	r.Post("/slurm/upgrades", m.handleStartUpgrade)
	r.Get("/slurm/upgrades", m.handleListUpgrades)
	r.Get("/slurm/upgrades/{op_id}", m.handleGetUpgrade)
	r.Post("/slurm/upgrades/{op_id}/pause", m.handlePauseUpgrade)
	r.Post("/slurm/upgrades/{op_id}/resume", m.handleResumeUpgrade)
	r.Post("/slurm/upgrades/{op_id}/rollback", m.handleRollbackUpgrade)
}

// ─── Status ───────────────────────────────────────────────────────────────────

func (m *Manager) handleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := m.Status(r.Context())
	if err != nil {
		jsonError(w, "failed to read Slurm status", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, status, http.StatusOK)
}

// ─── Enable / Disable ─────────────────────────────────────────────────────────

func (m *Manager) handleEnable(w http.ResponseWriter, r *http.Request) {
	var req EnableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := m.Enable(r.Context(), req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]string{"status": "ready"}, http.StatusOK)
}

func (m *Manager) handleDisable(w http.ResponseWriter, r *http.Request) {
	if err := m.Disable(r.Context()); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "disabled"}, http.StatusOK)
}

// ─── Config file management ───────────────────────────────────────────────────

func (m *Manager) handleListConfigs(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.SlurmListCurrentConfigs(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("slurm: list configs failed")
		jsonError(w, "failed to list configs", http.StatusInternalServerError)
		return
	}
	files := make([]api.SlurmConfigFile, 0, len(rows))
	for _, row := range rows {
		files = append(files, configRowToAPI(row))
	}
	jsonResponse(w, map[string]interface{}{"configs": files, "total": len(files)}, http.StatusOK)
}

func (m *Manager) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")

	// Optional ?version= query param for fetching a specific version.
	if vStr := r.URL.Query().Get("version"); vStr != "" {
		ver, err := strconv.Atoi(vStr)
		if err != nil {
			jsonError(w, "invalid version parameter", http.StatusBadRequest)
			return
		}
		row, err := m.db.SlurmGetConfigVersion(r.Context(), filename, ver)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				jsonError(w, "config version not found", http.StatusNotFound)
				return
			}
			jsonError(w, "failed to fetch config version", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, configRowToAPI(*row), http.StatusOK)
		return
	}

	row, err := m.db.SlurmGetCurrentConfig(r.Context(), filename)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "config file not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to fetch config", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, configRowToAPI(*row), http.StatusOK)
}

func (m *Manager) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")

	var body struct {
		Content string `json:"content"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Content == "" {
		jsonError(w, "content is required", http.StatusBadRequest)
		return
	}

	authoredBy := keyLabelFromContext(r)
	ver, err := m.db.SlurmSaveConfigVersion(r.Context(), filename, body.Content, authoredBy, body.Message)
	if err != nil {
		log.Error().Err(err).Str("filename", filename).Msg("slurm: save config version failed")
		jsonError(w, "failed to save config", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]interface{}{"filename": filename, "version": ver}, http.StatusOK)
}

func (m *Manager) handleConfigHistory(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	rows, err := m.db.SlurmListConfigHistory(r.Context(), filename)
	if err != nil {
		jsonError(w, "failed to fetch history", http.StatusInternalServerError)
		return
	}
	files := make([]api.SlurmConfigFile, 0, len(rows))
	for _, row := range rows {
		files = append(files, configRowToAPI(row))
	}
	jsonResponse(w, map[string]interface{}{"filename": filename, "history": files, "total": len(files)}, http.StatusOK)
}

// handleRenderPreview is GET /api/v1/slurm/configs/{filename}/render/{node_id}.
// Renders the specified config file template for the given node and returns the
// result. Pure dry-run: no state is written, no files are deployed.
func (m *Manager) handleRenderPreview(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	nodeID := chi.URLParam(r, "node_id")

	rendered, err := m.RenderAllForNode(r.Context(), nodeID)
	if err != nil {
		log.Error().Err(err).Str("filename", filename).Str("node_id", nodeID).
			Msg("slurm: render preview failed")
		jsonError(w, "render failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	content, ok := rendered[filename]
	if !ok {
		jsonError(w, "config file not found or not applicable for this node", http.StatusNotFound)
		return
	}

	jsonResponse(w, map[string]string{
		"filename":         filename,
		"node_id":          nodeID,
		"rendered_content": content,
		"checksum":         checksumString(content),
	}, http.StatusOK)
}

// ─── Sync / drift status ──────────────────────────────────────────────────────

func (m *Manager) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	driftRows, err := m.db.SlurmDriftQuery(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("slurm: drift query failed")
		jsonError(w, "failed to compute sync status", http.StatusInternalServerError)
		return
	}

	type driftEntry struct {
		NodeID          string `json:"node_id"`
		Filename        string `json:"filename"`
		CurrentVersion  int    `json:"current_version"`
		DeployedVersion int    `json:"deployed_version"`
		InSync          bool   `json:"in_sync"`
	}

	entries := make([]driftEntry, 0, len(driftRows))
	for _, d := range driftRows {
		entries = append(entries, driftEntry{
			NodeID:          d.NodeID,
			Filename:        d.Filename,
			CurrentVersion:  d.CurrentVersion,
			DeployedVersion: d.DeployedVersion,
			InSync:          d.InSync,
		})
	}

	// Compute script drift: compare slurm_script_state deployed_version against
	// current max version in slurm_scripts for each (node, script_type) pair.
	type scriptDriftEntry struct {
		NodeID          string `json:"node_id"`
		ScriptType      string `json:"script_type"`
		CurrentVersion  int    `json:"current_version"`
		DeployedVersion int    `json:"deployed_version"`
		InSync          bool   `json:"in_sync"`
	}

	scriptDrift := m.computeScriptDrift(r.Context())

	jsonResponse(w, map[string]interface{}{
		"drift":        entries,
		"total":        len(entries),
		"script_drift": scriptDrift,
		"script_total": len(scriptDrift),
	}, http.StatusOK)
}

func (m *Manager) handleNodeSyncStatus(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	stateRows, err := m.db.SlurmGetNodeConfigState(r.Context(), nodeID)
	if err != nil {
		jsonError(w, "failed to fetch node sync status", http.StatusInternalServerError)
		return
	}

	type stateEntry struct {
		Filename        string `json:"filename"`
		DeployedVersion int    `json:"deployed_version"`
		ContentHash     string `json:"content_hash"`
		DeployedAt      int64  `json:"deployed_at"`
		PushOpID        string `json:"push_op_id,omitempty"`
	}

	entries := make([]stateEntry, 0, len(stateRows))
	for _, s := range stateRows {
		entries = append(entries, stateEntry{
			Filename:        s.Filename,
			DeployedVersion: s.DeployedVersion,
			ContentHash:     s.ContentHash,
			DeployedAt:      s.DeployedAt,
			PushOpID:        s.PushOpID,
		})
	}
	jsonResponse(w, map[string]interface{}{"node_id": nodeID, "state": entries}, http.StatusOK)
}

// ─── Push operations ──────────────────────────────────────────────────────────

func (m *Manager) handlePush(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Filenames   []string `json:"filenames"`
		ScriptTypes []string `json:"script_types,omitempty"`
		ApplyAction string   `json:"apply_action"` // "reconfigure" or "restart"
		NodeIDs     []string `json:"node_ids,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	// filenames may be empty — executePush will default to all managed files.
	if body.ApplyAction == "" {
		body.ApplyAction = "reconfigure"
	}
	if body.ApplyAction != "reconfigure" && body.ApplyAction != "restart" {
		jsonError(w, "apply_action must be 'reconfigure' or 'restart'", http.StatusBadRequest)
		return
	}

	// Validate any explicitly-requested script types.
	for _, st := range body.ScriptTypes {
		if !IsKnownScriptType(st) {
			jsonError(w, "unknown script type: "+st, http.StatusBadRequest)
			return
		}
	}

	initiatedBy := keyLabelFromContext(r)

	req := PushRequest{
		Filenames:   body.Filenames,
		ScriptTypes: body.ScriptTypes,
		ApplyAction: body.ApplyAction,
		TargetNodes: body.NodeIDs,
	}

	// Create the push op record immediately so we can return an op ID to the caller.
	// The actual fan-out runs in a background goroutine; the caller polls GET /slurm/push-ops/{op_id}.
	op, err := m.StartPush(r.Context(), req, initiatedBy)
	if err != nil {
		log.Error().Err(err).Msg("slurm: push failed to start")
		jsonError(w, "push failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, op, http.StatusAccepted)
}

func (m *Manager) handlePushOpStatus(w http.ResponseWriter, r *http.Request) {
	opID := chi.URLParam(r, "op_id")
	op, err := m.GetPushOp(r.Context(), opID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "push operation not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to fetch push operation", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, op, http.StatusOK)
}

// ─── Node overrides ───────────────────────────────────────────────────────────

func (m *Manager) handleGetOverrides(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	overrides, err := m.db.SlurmGetNodeOverrides(r.Context(), nodeID)
	if err != nil {
		jsonError(w, "failed to fetch overrides", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, api.SlurmNodeOverride{
		NodeID: nodeID,
		Params: overrides,
	}, http.StatusOK)
}

func (m *Manager) handleSaveOverrides(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")

	var body struct {
		Params map[string]string `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Params == nil {
		jsonError(w, "params is required", http.StatusBadRequest)
		return
	}

	if err := m.db.SlurmSaveNodeOverrides(r.Context(), nodeID, body.Params); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("slurm: save overrides failed")
		jsonError(w, "failed to save overrides", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
}

// ─── Node roles ───────────────────────────────────────────────────────────────

func (m *Manager) handleGetRole(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	roles, err := m.db.SlurmGetNodeRoles(r.Context(), nodeID)
	if err != nil {
		jsonError(w, "failed to fetch roles", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]interface{}{"node_id": nodeID, "roles": roles}, http.StatusOK)
}

func (m *Manager) handleSetRole(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")

	var body struct {
		Roles      []string `json:"roles"`
		AutoDetect bool     `json:"auto_detect"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := m.db.SlurmSetNodeRoles(r.Context(), nodeID, body.Roles, body.AutoDetect); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("slurm: set roles failed")
		jsonError(w, "failed to set roles", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
}

func (m *Manager) handleNodesByRole(w http.ResponseWriter, r *http.Request) {
	role := chi.URLParam(r, "role")
	nodeIDs, err := m.db.SlurmListNodesByRole(r.Context(), role)
	if err != nil {
		jsonError(w, "failed to list nodes by role", http.StatusInternalServerError)
		return
	}
	if nodeIDs == nil {
		nodeIDs = []string{}
	}
	jsonResponse(w, map[string]interface{}{"role": role, "node_ids": nodeIDs, "total": len(nodeIDs)}, http.StatusOK)
}

func (m *Manager) handleRoleSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := m.db.SlurmRoleSummary(r.Context())
	if err != nil {
		jsonError(w, "failed to fetch role summary", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]interface{}{"summary": summary}, http.StatusOK)
}

// ─── Scripts ──────────────────────────────────────────────────────────────────

// handleListScripts returns all known script types with their current version,
// enabled status, and dest_path. Script types that have no saved version yet
// are still listed (with version=0 and no content).
func (m *Manager) handleListScripts(w http.ResponseWriter, r *http.Request) {
	// Canonical list of all supported script types (stable order).
	allTypes := []string{
		"Prolog", "Epilog", "TaskProlog", "TaskEpilog",
		"PrologSlurmctld", "EpilogSlurmctld",
		"HealthCheckProgram", "RebootProgram",
		"SrunProlog", "SrunEpilog",
	}

	// Fetch all script configs (enabled/dest_path per type).
	cfgRows, err := m.db.SlurmListScriptConfigs(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("slurm: list scripts: failed to fetch configs")
		jsonError(w, "failed to fetch script configs", http.StatusInternalServerError)
		return
	}
	cfgMap := make(map[string]db.SlurmScriptConfigRow, len(cfgRows))
	for _, c := range cfgRows {
		cfgMap[c.ScriptType] = c
	}

	type scriptSummary struct {
		ScriptType string `json:"script_type"`
		Version    int    `json:"version"`
		Checksum   string `json:"checksum,omitempty"`
		DestPath   string `json:"dest_path,omitempty"`
		Enabled    bool   `json:"enabled"`
		HasContent bool   `json:"has_content"`
	}

	out := make([]scriptSummary, 0, len(allTypes))
	for _, st := range allTypes {
		cfg := cfgMap[st]
		summary := scriptSummary{
			ScriptType: st,
			DestPath:   cfg.DestPath,
			Enabled:    cfg.Enabled,
		}
		row, err := m.db.SlurmGetCurrentScript(r.Context(), st)
		if err == nil && row != nil {
			summary.Version = row.Version
			summary.Checksum = row.Checksum
			summary.HasContent = true
		}
		out = append(out, summary)
	}

	jsonResponse(w, map[string]interface{}{"scripts": out, "total": len(out)}, http.StatusOK)
}

func (m *Manager) handleGetScript(w http.ResponseWriter, r *http.Request) {
	scriptType := chi.URLParam(r, "script_type")
	row, err := m.db.SlurmGetCurrentScript(r.Context(), scriptType)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "script not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to fetch script", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, scriptRowToAPI(*row), http.StatusOK)
}

func (m *Manager) handleSaveScript(w http.ResponseWriter, r *http.Request) {
	scriptType := chi.URLParam(r, "script_type")

	var body struct {
		Content  string `json:"content"`
		DestPath string `json:"dest_path"`
		Message  string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Content == "" || body.DestPath == "" {
		jsonError(w, "content and dest_path are required", http.StatusBadRequest)
		return
	}

	if err := ValidateScript(scriptType, body.Content); err != nil {
		jsonError(w, "script validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	authoredBy := keyLabelFromContext(r)
	ver, err := m.db.SlurmSaveScriptVersion(r.Context(), scriptType, body.DestPath, body.Content, authoredBy, body.Message)
	if err != nil {
		log.Error().Err(err).Str("script_type", scriptType).Msg("slurm: save script version failed")
		jsonError(w, "failed to save script", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]interface{}{"script_type": scriptType, "version": ver}, http.StatusOK)
}

func (m *Manager) handleScriptHistory(w http.ResponseWriter, r *http.Request) {
	scriptType := chi.URLParam(r, "script_type")
	rows, err := m.db.SlurmListScriptHistory(r.Context(), scriptType)
	if err != nil {
		jsonError(w, "failed to fetch script history", http.StatusInternalServerError)
		return
	}
	scripts := make([]api.SlurmScriptFile, 0, len(rows))
	for _, row := range rows {
		scripts = append(scripts, scriptRowToAPI(row))
	}
	jsonResponse(w, map[string]interface{}{"script_type": scriptType, "history": scripts, "total": len(scripts)}, http.StatusOK)
}

func (m *Manager) handleListScriptConfigs(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.SlurmListScriptConfigs(r.Context())
	if err != nil {
		jsonError(w, "failed to list script configs", http.StatusInternalServerError)
		return
	}

	type scriptConfigResp struct {
		ScriptType string `json:"script_type"`
		DestPath   string `json:"dest_path"`
		Enabled    bool   `json:"enabled"`
		UpdatedAt  int64  `json:"updated_at"`
	}

	out := make([]scriptConfigResp, 0, len(rows))
	for _, row := range rows {
		out = append(out, scriptConfigResp{
			ScriptType: row.ScriptType,
			DestPath:   row.DestPath,
			Enabled:    row.Enabled,
			UpdatedAt:  row.UpdatedAt,
		})
	}
	jsonResponse(w, map[string]interface{}{"configs": out, "total": len(out)}, http.StatusOK)
}

func (m *Manager) handleUpsertScriptConfig(w http.ResponseWriter, r *http.Request) {
	scriptType := chi.URLParam(r, "script_type")

	var body struct {
		DestPath string `json:"dest_path"`
		Enabled  bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.DestPath == "" {
		jsonError(w, "dest_path is required", http.StatusBadRequest)
		return
	}

	if err := m.db.SlurmUpsertScriptConfig(r.Context(), db.SlurmScriptConfigRow{
		ScriptType: scriptType,
		DestPath:   body.DestPath,
		Enabled:    body.Enabled,
	}); err != nil {
		jsonError(w, "failed to save script config", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
}

// ─── Builds ───────────────────────────────────────────────────────────────────

func (m *Manager) handleListBuilds(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.SlurmListBuilds(r.Context())
	if err != nil {
		jsonError(w, "failed to list builds", http.StatusInternalServerError)
		return
	}

	activeBuildID, _ := m.db.SlurmGetActiveBuildID(r.Context())

	out := make([]api.SlurmBuild, 0, len(rows))
	for _, row := range rows {
		out = append(out, buildRowToAPI(row, activeBuildID))
	}
	jsonResponse(w, map[string]interface{}{"builds": out, "total": len(out), "active_build_id": activeBuildID}, http.StatusOK)
}

func (m *Manager) handleGetBuild(w http.ResponseWriter, r *http.Request) {
	buildID := chi.URLParam(r, "build_id")
	row, err := m.db.SlurmGetBuild(r.Context(), buildID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "build not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to fetch build", http.StatusInternalServerError)
		return
	}
	activeBuildID, _ := m.db.SlurmGetActiveBuildID(r.Context())
	jsonResponse(w, buildRowToAPI(*row, activeBuildID), http.StatusOK)
}

func (m *Manager) handleStartBuild(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SlurmVersion   string   `json:"slurm_version"`
		Arch           string   `json:"arch"`
		ConfigureFlags []string `json:"configure_flags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.SlurmVersion == "" {
		jsonError(w, "slurm_version is required", http.StatusBadRequest)
		return
	}

	initiatedBy := keyLabelFromContext(r)
	cfg := BuildConfig{
		SlurmVersion:   body.SlurmVersion,
		Arch:           body.Arch,
		ConfigureFlags: body.ConfigureFlags,
	}
	buildID, err := m.StartBuild(r.Context(), cfg, initiatedBy)
	if err != nil {
		log.Error().Err(err).Msg("slurm: start build failed")
		jsonError(w, "failed to start build: "+err.Error(), http.StatusBadRequest)
		return
	}
	jsonResponse(w, map[string]string{"build_id": buildID, "status": "building"}, http.StatusAccepted)
}

func (m *Manager) handleDeleteBuild(w http.ResponseWriter, r *http.Request) {
	buildID := chi.URLParam(r, "build_id")

	// Fetch the build to get the artifact path for cleanup.
	row, err := m.db.SlurmGetBuild(r.Context(), buildID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "build not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to fetch build", http.StatusInternalServerError)
		return
	}

	if err := m.db.SlurmDeleteBuild(r.Context(), buildID); err != nil {
		log.Error().Err(err).Str("build_id", buildID).Msg("slurm: delete build failed")
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}

	// Best-effort artifact cleanup.
	if row.ArtifactPath != "" {
		if err := os.Remove(row.ArtifactPath); err != nil && !os.IsNotExist(err) {
			log.Warn().Err(err).Str("path", row.ArtifactPath).Msg("slurm: delete build: artifact cleanup failed")
		}
	}

	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}

func (m *Manager) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	buildID := chi.URLParam(r, "build_id")
	token := r.URL.Query().Get("token")
	expires := r.URL.Query().Get("expires")

	// Validate signed URL.
	if token == "" || expires == "" {
		jsonError(w, "missing token or expires parameter", http.StatusBadRequest)
		return
	}
	if !m.ValidateArtifactToken(buildID, token, expires) {
		jsonError(w, "invalid or expired token", http.StatusForbidden)
		return
	}

	row, err := m.db.SlurmGetBuild(r.Context(), buildID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "build not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to fetch build", http.StatusInternalServerError)
		return
	}
	if row.Status != "completed" || row.ArtifactPath == "" {
		jsonError(w, "build artifact not available", http.StatusNotFound)
		return
	}

	// Sanitize path before serving.
	cleanPath := filepath.Clean(row.ArtifactPath)
	if !strings.HasPrefix(cleanPath, slurmBuildsDir) {
		jsonError(w, "invalid artifact path", http.StatusInternalServerError)
		return
	}

	f, err := os.Open(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			jsonError(w, "artifact file not found on disk", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to open artifact", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(cleanPath))
	if row.ArtifactChecksum != "" {
		w.Header().Set("X-Checksum-SHA256", row.ArtifactChecksum)
	}
	if row.ArtifactSizeBytes > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", row.ArtifactSizeBytes))
	}
	http.ServeContent(w, r, filepath.Base(cleanPath), time.Time{}, f)
}

func (m *Manager) handleBuildLogs(w http.ResponseWriter, r *http.Request) {
	buildID := chi.URLParam(r, "build_id")

	// Verify the build exists.
	if _, err := m.db.SlurmGetBuild(r.Context(), buildID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "build not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to fetch build", http.StatusInternalServerError)
		return
	}

	// Build logs are emitted via zerolog with build_id field. For v1, return a
	// stub that indicates where to find the logs (server log stream).
	jsonResponse(w, map[string]interface{}{
		"build_id": buildID,
		"message":  "Build logs are available via GET /api/v1/logs?component=slurm-build or the SSE log stream",
		"log_key":  buildID,
	}, http.StatusOK)
}

func (m *Manager) handleSetActiveBuild(w http.ResponseWriter, r *http.Request) {
	buildID := chi.URLParam(r, "build_id")

	if err := m.db.SlurmSetActiveBuild(r.Context(), buildID); err != nil {
		log.Error().Err(err).Str("build_id", buildID).Msg("slurm: set active build failed")
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]string{"status": "ok", "active_build_id": buildID}, http.StatusOK)
}

// ─── Dependency matrix ────────────────────────────────────────────────────────

func (m *Manager) handleListDepMatrix(w http.ResponseWriter, r *http.Request) {
	type matrixRow struct {
		ID              string `json:"id"`
		SlurmVersionMin string `json:"slurm_version_min"`
		SlurmVersionMax string `json:"slurm_version_max"`
		DepName         string `json:"dep_name"`
		DepVersionMin   string `json:"dep_version_min"`
		DepVersionMax   string `json:"dep_version_max"`
		Source          string `json:"source"`
	}

	// Query the dep matrix for all known Slurm versions by doing a wildcard resolve.
	// We fetch raw rows via the DB since SlurmResolveDepVersions is version-scoped.
	rows, err := m.db.SlurmListDepMatrix(r.Context())
	if err != nil {
		jsonError(w, "failed to fetch dep matrix", http.StatusInternalServerError)
		return
	}

	out := make([]matrixRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, matrixRow{
			ID:              row.ID,
			SlurmVersionMin: row.SlurmVersionMin,
			SlurmVersionMax: row.SlurmVersionMax,
			DepName:         row.DepName,
			DepVersionMin:   row.DepVersionMin,
			DepVersionMax:   row.DepVersionMax,
			Source:          row.Source,
		})
	}
	jsonResponse(w, map[string]interface{}{"matrix": out, "total": len(out)}, http.StatusOK)
}

// ─── Rolling upgrade operations (Sprint 9) ────────────────────────────────────

func (m *Manager) handleValidateUpgrade(w http.ResponseWriter, r *http.Request) {
	var req UpgradeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ToBuildID == "" {
		jsonError(w, "to_build_id is required", http.StatusBadRequest)
		return
	}

	result, err := m.ValidateUpgrade(r.Context(), req)
	if err != nil {
		log.Error().Err(err).Msg("slurm: validate upgrade failed")
		jsonError(w, "validation error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, result, http.StatusOK)
}

func (m *Manager) handleStartUpgrade(w http.ResponseWriter, r *http.Request) {
	var req UpgradeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ToBuildID == "" {
		jsonError(w, "to_build_id is required", http.StatusBadRequest)
		return
	}

	initiatedBy := keyLabelFromContext(r)
	opID, err := m.StartUpgrade(r.Context(), req, initiatedBy)
	if err != nil {
		log.Error().Err(err).Msg("slurm: start upgrade failed")
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]string{"op_id": opID, "status": "queued"}, http.StatusAccepted)
}

func (m *Manager) handleListUpgrades(w http.ResponseWriter, r *http.Request) {
	ops, err := m.ListUpgradeOps(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("slurm: list upgrades failed")
		jsonError(w, "failed to list upgrades", http.StatusInternalServerError)
		return
	}
	if ops == nil {
		ops = []UpgradeOperation{}
	}
	jsonResponse(w, map[string]interface{}{"operations": ops, "total": len(ops)}, http.StatusOK)
}

func (m *Manager) handleGetUpgrade(w http.ResponseWriter, r *http.Request) {
	opID := chi.URLParam(r, "op_id")
	op, err := m.GetUpgradeOp(r.Context(), opID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "upgrade operation not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to fetch upgrade operation", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, op, http.StatusOK)
}

func (m *Manager) handlePauseUpgrade(w http.ResponseWriter, r *http.Request) {
	opID := chi.URLParam(r, "op_id")
	if err := m.PauseUpgrade(r.Context(), opID); err != nil {
		log.Error().Err(err).Str("op_id", opID).Msg("slurm: pause upgrade failed")
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResponse(w, map[string]string{"status": "paused"}, http.StatusOK)
}

func (m *Manager) handleResumeUpgrade(w http.ResponseWriter, r *http.Request) {
	opID := chi.URLParam(r, "op_id")
	if err := m.ResumeUpgrade(r.Context(), opID); err != nil {
		log.Error().Err(err).Str("op_id", opID).Msg("slurm: resume upgrade failed")
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResponse(w, map[string]string{"status": "in_progress"}, http.StatusOK)
}

func (m *Manager) handleRollbackUpgrade(w http.ResponseWriter, r *http.Request) {
	opID := chi.URLParam(r, "op_id")
	if err := m.RollbackUpgrade(r.Context(), opID); err != nil {
		log.Error().Err(err).Str("op_id", opID).Msg("slurm: rollback upgrade failed")
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResponse(w, map[string]string{"status": "rollback_initiated"}, http.StatusAccepted)
}

// ─── Munge key ────────────────────────────────────────────────────────────────

func (m *Manager) handleGenerateMungeKey(w http.ResponseWriter, r *http.Request) {
	if err := m.GenerateMungeKey(r.Context()); err != nil {
		log.Error().Err(err).Msg("slurm: generate munge key failed")
		jsonError(w, "failed to generate munge key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "ok", "message": "munge key generated and stored"}, http.StatusOK)
}

func (m *Manager) handleRotateMungeKey(w http.ResponseWriter, r *http.Request) {
	if err := m.RotateMungeKey(r.Context()); err != nil {
		log.Error().Err(err).Msg("slurm: rotate munge key failed")
		jsonError(w, "failed to rotate munge key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "ok", "message": "munge key rotated"}, http.StatusOK)
}

// ─── GAP-17 flat endpoints ────────────────────────────────────────────────────

// handleSlurmNodes is GET /api/v1/slurm/nodes.
// Returns all clustr-managed nodes that have a Slurm role assignment,
// along with their assigned roles and whether they are currently connected.
func (m *Manager) handleSlurmNodes(w http.ResponseWriter, r *http.Request) {
	allRoles, err := m.db.SlurmListAllNodeRoles(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("slurm: list nodes failed")
		jsonError(w, "failed to list Slurm nodes", http.StatusInternalServerError)
		return
	}

	type slurmNodeEntry struct {
		NodeID    string   `json:"node_id"`
		Roles     []string `json:"roles"`
		Connected bool     `json:"connected"`
	}

	entries := make([]slurmNodeEntry, 0, len(allRoles))
	for _, entry := range allRoles {
		connected := m.hub != nil && m.hub.IsConnected(entry.NodeID)
		entries = append(entries, slurmNodeEntry{
			NodeID:    entry.NodeID,
			Roles:     entry.Roles,
			Connected: connected,
		})
	}
	jsonResponse(w, map[string]interface{}{"nodes": entries, "total": len(entries)}, http.StatusOK)
}

// handleSlurmRoles is GET /api/v1/slurm/roles.
// Returns the canonical list of Slurm node roles supported by clustr.
func (m *Manager) handleSlurmRoles(w http.ResponseWriter, r *http.Request) {
	roles := []string{"controller", "worker", "dbd", "login"}
	jsonResponse(w, map[string]interface{}{"roles": roles, "total": len(roles)}, http.StatusOK)
}

// handleSlurmSync is POST /api/v1/slurm/sync.
// Triggers a push of all managed Slurm config files to all connected worker
// nodes using reconfigure as the apply action. Returns a push operation ID
// that callers can poll via GET /slurm/push-ops/{op_id}.
func (m *Manager) handleSlurmSync(w http.ResponseWriter, r *http.Request) {
	initiatedBy := keyLabelFromContext(r)

	req := PushRequest{
		ApplyAction: "reconfigure",
	}

	op, err := m.StartPush(r.Context(), req, initiatedBy)
	if err != nil {
		log.Error().Err(err).Msg("slurm: sync (push) failed to start")
		jsonError(w, "sync failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, op, http.StatusAccepted)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// computeScriptDrift returns drift entries for scripts by comparing slurm_script_state
// deployed_version against the current max version in slurm_scripts.
func (m *Manager) computeScriptDrift(ctx context.Context) []map[string]interface{} {
	// Get all node script state rows.
	allNodeRoles, err := m.db.SlurmListAllNodeRoles(ctx)
	if err != nil {
		return nil
	}

	// Build current script version map: scriptType → currentVersion
	currentVersions := make(map[string]int)
	scriptCfgs, _ := m.db.SlurmListScriptConfigs(ctx)
	for _, sc := range scriptCfgs {
		if !sc.Enabled {
			continue
		}
		row, err := m.db.SlurmGetCurrentScript(ctx, sc.ScriptType)
		if err == nil && row != nil {
			currentVersions[sc.ScriptType] = row.Version
		}
	}

	var result []map[string]interface{}
	for _, entry := range allNodeRoles {
		stateRows, err := m.db.SlurmGetScriptState(ctx, entry.NodeID)
		if err != nil {
			continue
		}
		for _, s := range stateRows {
			curVer := currentVersions[s.ScriptType]
			result = append(result, map[string]interface{}{
				"node_id":          s.NodeID,
				"script_type":      s.ScriptType,
				"current_version":  curVer,
				"deployed_version": s.DeployedVersion,
				"in_sync":          curVer == s.DeployedVersion && curVer > 0,
			})
		}
	}
	return result
}

// buildRowToAPI converts a DB build row to the API type.
func buildRowToAPI(row db.SlurmBuildRow, activeBuildID string) api.SlurmBuild {
	return api.SlurmBuild{
		ID:               row.ID,
		Version:          row.Version,
		Arch:             row.Arch,
		Status:           row.Status,
		ConfigureFlags:   row.ConfigureFlags,
		ArtifactPath:     row.ArtifactPath,
		ArtifactChecksum: row.ArtifactChecksum,
		ArtifactSize:     row.ArtifactSizeBytes,
		StartedAt:        row.StartedAt,
		CompletedAt:      row.CompletedAt,
		ErrorMessage:     row.ErrorMessage,
		IsActive:         row.ID == activeBuildID && activeBuildID != "",
	}
}

// configRowToAPI converts a DB config file row to the API type.
func configRowToAPI(row db.SlurmConfigFileRow) api.SlurmConfigFile {
	return api.SlurmConfigFile{
		Filename: row.Filename,
		Path:     "/etc/slurm/" + row.Filename,
		Content:  row.Content,
		Checksum: row.Checksum,
		FileMode: "0644",
		Owner:    "slurm:slurm",
		Version:  row.Version,
	}
}

// scriptRowToAPI converts a DB script row to the API type.
func scriptRowToAPI(row db.SlurmScriptRow) api.SlurmScriptFile {
	return api.SlurmScriptFile{
		ScriptType: row.ScriptType,
		DestPath:   row.DestPath,
		Content:    row.Content,
		Checksum:   row.Checksum,
		Version:    row.Version,
	}
}

// keyLabelFromContext extracts an API key label from the request context.
// Falls back to "unknown" when no label is set.
func keyLabelFromContext(r *http.Request) string {
	type keyLabelKey struct{}
	if v := r.Context().Value(keyLabelKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return "unknown"
}

func jsonResponse(w http.ResponseWriter, body interface{}, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Error().Err(err).Msg("slurm routes: encode response failed")
	}
}

func jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
