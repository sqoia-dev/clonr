// manager.go — Slurm module Manager: lifecycle (Enable/Disable), status,
// NodeConfig projection, and background health checks.
// Follows the same pattern as internal/ldap/manager.go.
package slurm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// Status values mirroring the DB status column.
const (
	statusNotConfigured = "not_configured"
	statusReady         = "ready"
	statusDisabled      = "disabled"
	statusError         = "error"
)

// defaultManagedFiles is the list of files the module manages by default.
var defaultManagedFiles = []string{
	"slurm.conf",
	"gres.conf",
	"cgroup.conf",
	"topology.conf",
	"plugstack.conf",
}

// ClientdHubIface is the subset of ClientdHub used by the Slurm manager.
// Defined here so we don't import the server package (circular import).
// The concrete *server.ClientdHub satisfies this interface.
type ClientdHubIface interface {
	Send(nodeID string, msg clientd.ServerMessage) error
	ConnectedNodes() []string
	IsConnected(nodeID string) bool
	// Ack registry — same mechanism used by the generic config_push handler.
	RegisterAck(msgID string) <-chan clientd.AckPayload
	UnregisterAck(msgID string)
	DeliverAck(msgID string, payload clientd.AckPayload) bool
}

// Manager owns the Slurm module lifecycle and provides the API surface for
// config management and status. It is safe for concurrent use.
type Manager struct {
	db  *db.DB
	hub ClientdHubIface

	mu  sync.RWMutex
	cfg *db.SlurmModuleConfigRow // in-memory cache, loaded from DB on New()

	// upgradeMu guards activeUpgrade state (separate from cfg lock to avoid contention).
	upgradeMu     sync.RWMutex
	activeUpgrade *upgradeState
}

// New creates a Manager and restores in-memory state from the DB.
// If no config row exists (fresh install), the module starts in not_configured state.
// Seeds the dep matrix from the embedded JSON on every startup (INSERT OR IGNORE).
func New(database *db.DB, hub ClientdHubIface) *Manager {
	m := &Manager{
		db:  database,
		hub: hub,
	}
	m.restoreFromDB(context.Background())
	if err := m.seedDepMatrix(context.Background()); err != nil {
		log.Warn().Err(err).Msg("slurm: dep matrix seed failed (non-fatal)")
	}
	return m
}

// restoreFromDB loads the singleton config row into memory on startup.
func (m *Manager) restoreFromDB(ctx context.Context) {
	row, err := m.db.SlurmGetConfig(ctx)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Warn().Err(err).Msg("slurm: failed to restore config from DB on startup")
		}
		return
	}
	m.mu.Lock()
	m.cfg = row
	m.mu.Unlock()
}

// ─── Enable ───────────────────────────────────────────────────────────────────

// EnableRequest is the body for POST /api/v1/slurm/enable.
type EnableRequest struct {
	ClusterName  string   `json:"cluster_name"`
	ManagedFiles []string `json:"managed_files,omitempty"`
}

// Enable activates the Slurm module with the given cluster configuration.
// Creates default config file templates from embedded defaults if no config
// files exist yet. Idempotent — safe to call while already enabled.
func (m *Manager) Enable(ctx context.Context, req EnableRequest) error {
	if req.ClusterName == "" {
		return fmt.Errorf("slurm: cluster_name is required")
	}

	managedFiles := req.ManagedFiles
	if len(managedFiles) == 0 {
		managedFiles = defaultManagedFiles
	}

	// Save config row.
	now := time.Now().Unix()
	row := db.SlurmModuleConfigRow{
		Enabled:      true,
		Status:       statusReady,
		ClusterName:  req.ClusterName,
		ManagedFiles: managedFiles,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := m.db.SlurmSaveConfig(ctx, row); err != nil {
		return fmt.Errorf("slurm: save config: %w", err)
	}

	// Seed default templates for any files that have no versions yet.
	if err := m.seedDefaultTemplates(ctx, req.ClusterName, managedFiles); err != nil {
		log.Warn().Err(err).Msg("slurm: seed default templates failed (non-fatal)")
	}

	// Update in-memory cache.
	m.mu.Lock()
	m.cfg = &row
	m.mu.Unlock()

	log.Info().Str("cluster_name", req.ClusterName).Msg("slurm: module enabled")
	return nil
}

// seedDefaultTemplates creates version 1 of each managed file from the embedded
// templates, but only if no version exists for that file yet.
func (m *Manager) seedDefaultTemplates(ctx context.Context, clusterName string, files []string) error {
	for _, filename := range files {
		// Check if any version already exists.
		existing, err := m.db.SlurmGetCurrentConfig(ctx, filename)
		if err == nil && existing != nil {
			continue // already seeded
		}

		tmplName := "templates/" + filename + ".tmpl"
		tmpl, err := template.ParseFS(templateFS, tmplName)
		if err != nil {
			log.Debug().Str("file", filename).Msg("slurm: no embedded template for file, skipping seed")
			continue
		}

		data := map[string]interface{}{
			"ClusterName":        clusterName,
			"ControllerHostname": "clustr-server",
			"Timestamp":          time.Now().UTC().Format(time.RFC3339),
			"Nodes":              []interface{}{},
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			log.Warn().Err(err).Str("file", filename).Msg("slurm: render default template failed")
			continue
		}

		_, err = m.db.SlurmSaveConfigVersion(ctx, filename, buf.String(), "clustr-system", "Initial default template")
		if err != nil {
			log.Warn().Err(err).Str("file", filename).Msg("slurm: save default template version failed")
		} else {
			log.Info().Str("file", filename).Msg("slurm: seeded default template")
		}
	}
	return nil
}

// ─── Disable ──────────────────────────────────────────────────────────────────

// Disable marks the module disabled. Does NOT remove configs from deployed nodes.
func (m *Manager) Disable(ctx context.Context) error {
	if err := m.db.SlurmSetStatus(ctx, statusDisabled); err != nil {
		return fmt.Errorf("slurm: disable: %w", err)
	}

	// Update enabled flag and status in the singleton row.
	row, err := m.db.SlurmGetConfig(ctx)
	if err == nil {
		row.Enabled = false
		row.Status = statusDisabled
		_ = m.db.SlurmSaveConfig(ctx, *row)
	}

	m.mu.Lock()
	if m.cfg != nil {
		m.cfg.Enabled = false
		m.cfg.Status = statusDisabled
	}
	m.mu.Unlock()

	log.Info().Msg("slurm: module disabled")
	return nil
}

// ─── Status ───────────────────────────────────────────────────────────────────

// SlurmModuleStatus is the response for GET /api/v1/slurm/status.
type SlurmModuleStatus struct {
	Enabled        bool             `json:"enabled"`
	Status         string           `json:"status"`
	ClusterName    string           `json:"cluster_name"`
	ManagedFiles   []string         `json:"managed_files"`
	ConnectedNodes []string         `json:"connected_nodes"`
	DriftSummary   *DriftSummary    `json:"drift_summary,omitempty"`
}

// DriftSummary is a compact per-file sync summary included in the status response.
type DriftSummary struct {
	TotalNodes   int `json:"total_nodes"`
	InSyncNodes  int `json:"in_sync_nodes"`
	OutOfSync    int `json:"out_of_sync"`
}

// Status reads the current module state and returns a status response.
func (m *Manager) Status(ctx context.Context) (*SlurmModuleStatus, error) {
	row, err := m.db.SlurmGetConfig(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &SlurmModuleStatus{Status: statusNotConfigured, ManagedFiles: []string{}}, nil
		}
		return nil, fmt.Errorf("slurm: status: %w", err)
	}

	var connectedNodes []string
	if m.hub != nil {
		connectedNodes = m.hub.ConnectedNodes()
	}

	resp := &SlurmModuleStatus{
		Enabled:        row.Enabled,
		Status:         row.Status,
		ClusterName:    row.ClusterName,
		ManagedFiles:   row.ManagedFiles,
		ConnectedNodes: connectedNodes,
	}

	// Compute drift summary.
	driftRows, err := m.db.SlurmDriftQuery(ctx)
	if err == nil && len(driftRows) > 0 {
		var inSync, total int
		seenNodes := make(map[string]bool)
		for _, d := range driftRows {
			if !seenNodes[d.NodeID] {
				seenNodes[d.NodeID] = true
				total++
			}
			if d.InSync {
				inSync++
			}
		}
		resp.DriftSummary = &DriftSummary{
			TotalNodes:  total,
			InSyncNodes: inSync,
			OutOfSync:   total - inSync,
		}
	}

	return resp, nil
}

// ─── NodeConfig projection ────────────────────────────────────────────────────

// NodeConfig returns the SlurmNodeConfig struct for injection into api.NodeConfig
// during the deploy pipeline. Returns nil if the module is not enabled or not ready.
// It renders each config file for the specific node (resolving template variables)
// and filters to only the files relevant to the node's assigned roles.
func (m *Manager) NodeConfig(ctx context.Context, nodeID string) (*api.SlurmNodeConfig, error) {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()

	if cfg == nil || !cfg.Enabled || cfg.Status != statusReady {
		return nil, nil
	}

	// Determine which files this node should receive based on its roles.
	roles, err := m.db.SlurmGetNodeRoles(ctx, nodeID)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("slurm: NodeConfig: failed to fetch roles, using all managed files")
		roles = []string{}
	}

	// FilesForRoles returns the role-filtered set. If the node has no roles
	// assigned yet, fall back to all managed files so the deploy still works.
	relevantFiles := FilesForRoles(roles)
	if len(relevantFiles) == 0 {
		relevantFiles = cfg.ManagedFiles
	}

	// Render all managed files for this node.
	rendered, err := m.RenderAllForNode(ctx, nodeID)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("slurm: NodeConfig: render failed, falling back to raw content")
		rendered = nil
	}

	// Build the Configs list, restricted to role-relevant files.
	relevantSet := make(map[string]struct{}, len(relevantFiles))
	for _, f := range relevantFiles {
		relevantSet[f] = struct{}{}
	}

	var configs []api.SlurmConfigFile
	for _, filename := range cfg.ManagedFiles {
		if _, ok := relevantSet[filename]; !ok {
			continue
		}

		row, err := m.db.SlurmGetCurrentConfig(ctx, filename)
		if err != nil {
			log.Warn().Err(err).Str("filename", filename).Msg("slurm: NodeConfig: skip missing file")
			continue
		}

		content := row.Content
		if rendered != nil {
			if rc, ok := rendered[filename]; ok {
				content = rc
			}
		}

		mode := "0644"
		if filename == "slurmdbd.conf" {
			mode = "0600"
		}

		configs = append(configs, api.SlurmConfigFile{
			Filename: row.Filename,
			Path:     "/etc/slurm/" + row.Filename,
			Content:  content,
			Checksum: checksumString(content),
			FileMode: mode,
			Owner:    "slurm:slurm",
			Version:  row.Version,
		})
	}

	// Collect scripts for role-relevant script types.
	var scripts []api.SlurmScriptFile
	scriptTypes := ScriptTypesForRoles(roles)
	scriptConfigs, err := m.db.SlurmListScriptConfigs(ctx)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("slurm: NodeConfig: failed to list script configs")
		scriptConfigs = nil
	}

	// Build a set of enabled script types for quick lookup.
	enabledScripts := make(map[string]string) // scriptType → destPath
	for _, sc := range scriptConfigs {
		if sc.Enabled {
			enabledScripts[sc.ScriptType] = sc.DestPath
		}
	}

	for _, st := range scriptTypes {
		if _, ok := enabledScripts[st]; !ok {
			continue
		}
		row, err := m.db.SlurmGetCurrentScript(ctx, st)
		if err != nil {
			log.Warn().Err(err).Str("script_type", st).Msg("slurm: NodeConfig: skip missing script")
			continue
		}
		scripts = append(scripts, api.SlurmScriptFile{
			ScriptType: row.ScriptType,
			DestPath:   row.DestPath,
			Content:    row.Content,
			Checksum:   row.Checksum,
			Version:    row.Version,
		})
	}

	return &api.SlurmNodeConfig{
		ClusterName: cfg.ClusterName,
		Configs:     configs,
		Scripts:     scripts,
	}, nil
}

// checksumString returns the hex-encoded SHA-256 of s.
// Used to recompute the checksum after template rendering produces new content.
func checksumString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ─── Push ops helpers ─────────────────────────────────────────────────────────

// GetPushOp retrieves a push operation for the route handler.
func (m *Manager) GetPushOp(ctx context.Context, opID string) (*api.SlurmPushOperation, error) {
	row, err := m.db.SlurmGetPushOp(ctx, opID)
	if err != nil {
		return nil, err
	}
	return pushOpRowToAPI(row), nil
}

// pushOpRowToAPI converts a DB row to the API type.
func pushOpRowToAPI(row *db.SlurmPushOperationRow) *api.SlurmPushOperation {
	op := &api.SlurmPushOperation{
		ID:           row.ID,
		Filenames:    row.Filenames,
		FileVersions: row.FileVersions,
		ApplyAction:  row.ApplyAction,
		Status:       row.Status,
		NodeCount:    row.NodeCount,
		SuccessCount: row.SuccessCount,
		FailureCount: row.FailureCount,
		StartedAt:    row.StartedAt,
		CompletedAt:  row.CompletedAt,
	}
	if len(row.NodeResults) > 0 {
		var results map[string]api.SlurmNodeResult
		if err := json.Unmarshal(row.NodeResults, &results); err == nil {
			op.NodeResults = results
		}
	}
	return op
}

// StartPush creates a push operation record immediately (so the caller gets an
// op ID to poll), then runs the actual fan-out in a background goroutine.
// Use GetPushOp to poll the status.
func (m *Manager) StartPush(ctx context.Context, req PushRequest, initiatedBy string) (*api.SlurmPushOperation, error) {
	// Validate module state before spawning goroutine.
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	if cfg == nil || !cfg.Enabled {
		return nil, fmt.Errorf("slurm: module is not enabled")
	}

	// Create the push op record with "pending" status so the caller has an ID.
	op, err := m.createPendingPushOp(ctx, req, initiatedBy)
	if err != nil {
		return nil, err
	}

	// Run the orchestration in a background goroutine with a server-lifetime context
	// so the HTTP request context cancellation does not abort in-flight node pushes.
	go func() {
		bgCtx := context.Background()
		if err := m.runPushBackground(bgCtx, op.ID, req, initiatedBy); err != nil {
			log.Error().Err(err).Str("op_id", op.ID).Msg("slurm: background push failed")
		}
	}()

	return op, nil
}

// createPendingPushOp creates a push operation DB record with status "pending"
// and returns its API representation.
func (m *Manager) createPendingPushOp(ctx context.Context, req PushRequest, initiatedBy string) (*api.SlurmPushOperation, error) {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()

	managedFiles := req.Filenames
	if len(managedFiles) == 0 && cfg != nil {
		managedFiles = cfg.ManagedFiles
	}

	fileVersions := make(map[string]int)
	for _, fn := range managedFiles {
		row, err := m.db.SlurmGetCurrentConfig(ctx, fn)
		if err == nil && row != nil {
			fileVersions[fn] = row.Version
		}
	}

	// Estimate node count from target nodes or connected nodes.
	nodeCount := 0
	if len(req.TargetNodes) > 0 {
		nodeCount = len(req.TargetNodes)
	} else if m.hub != nil {
		nodeCount = len(m.hub.ConnectedNodes())
	}

	id := newUUID()
	now := time.Now().Unix()
	opRow := db.SlurmPushOperationRow{
		ID:           id,
		Filenames:    managedFiles,
		FileVersions: fileVersions,
		InitiatedBy:  initiatedBy,
		ApplyAction:  req.ApplyAction,
		Status:       "pending",
		NodeCount:    nodeCount,
		StartedAt:    now,
	}
	if err := m.db.SlurmCreatePushOp(ctx, opRow); err != nil {
		return nil, fmt.Errorf("slurm: create push op: %w", err)
	}
	return pushOpRowToAPI(&opRow), nil
}

// runPushBackground runs the push orchestration in the background.
// The push op record already exists with status "pending"; executePushWithID
// transitions it to "in_progress" and then to the final status.
func (m *Manager) runPushBackground(ctx context.Context, opID string, req PushRequest, initiatedBy string) error {
	_, err := m.executePushWithID(ctx, opID, req, initiatedBy)
	return err
}

// Push is a synchronous push for internal use and tests. Prefer StartPush for HTTP handlers.
func (m *Manager) Push(ctx context.Context, req PushRequest, initiatedBy string) (*api.SlurmPushOperation, error) {
	return m.executePush(ctx, req, initiatedBy)
}

// newUUID returns a new random UUID string.
func newUUID() string {
	return uuid.New().String()
}

// ─── Background workers ───────────────────────────────────────────────────────

// StartBackgroundWorkers launches the health-check goroutine.
// Called by server.go after creating the Manager.
func (m *Manager) StartBackgroundWorkers(ctx context.Context) {
	go m.runHealthChecker(ctx)
}

func (m *Manager) runHealthChecker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Info().Msg("slurm: health checker started")
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("slurm: health checker stopping")
			return
		case <-ticker.C:
			m.healthCheck(ctx)
		}
	}
}

// healthCheck verifies module config integrity.
// In v1 this is lightweight — just ensures the DB row is consistent.
func (m *Manager) healthCheck(ctx context.Context) {
	row, err := m.db.SlurmGetConfig(ctx)
	if err != nil || !row.Enabled {
		return
	}

	// Verify that managed files have at least one version.
	anyMissing := false
	for _, filename := range row.ManagedFiles {
		existing, err := m.db.SlurmGetCurrentConfig(ctx, filename)
		if err != nil || existing == nil {
			anyMissing = true
			log.Warn().Str("filename", filename).Msg("slurm: health check: managed file has no versions")
		}
	}

	if anyMissing && row.Status == statusReady {
		_ = m.db.SlurmSetStatus(ctx, statusError)
		m.mu.Lock()
		if m.cfg != nil {
			m.cfg.Status = statusError
		}
		m.mu.Unlock()
	} else if !anyMissing && row.Status == statusError {
		_ = m.db.SlurmSetStatus(ctx, statusReady)
		m.mu.Lock()
		if m.cfg != nil {
			m.cfg.Status = statusReady
		}
		m.mu.Unlock()
	}
}

// ─── Accessors for use by route handlers ─────────────────────────────────────

// IsEnabled reports whether the module is currently enabled and ready.
func (m *Manager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg != nil && m.cfg.Enabled && m.cfg.Status == statusReady
}
