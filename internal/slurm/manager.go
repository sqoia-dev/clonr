// manager.go — Slurm module Manager: lifecycle (Enable/Disable), status,
// NodeConfig projection, and background health checks.
// Follows the same pattern as internal/ldap/manager.go.
package slurm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

// RepoSentinelBuiltin is the slurm_repo_url value that means "use this
// clustr-server's own bundled package repository at /repo/<distro>-<arch>/".
// It is the default for new installs. An empty string is treated equivalently
// for backward-compatibility. This value is irreversible once shipped as a DB
// value — do not rename without a migration.
const RepoSentinelBuiltin = "clustr-builtin"

// defaultManagedFiles is the list of files the module manages by default.
// slurmdbd.conf is included so that controller nodes receive it and
// installSlurmInChroot can detect the controller role (hasSlurmdbd flag).
var defaultManagedFiles = []string{
	"slurm.conf",
	"slurmdbd.conf",
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

	// Audit records state-changing events. Optional — nil disables auditing.
	// GAP-20: wired from server.go after construction.
	Audit *db.AuditService

	// GetActorInfo returns (actorID, actorLabel) from a request context.
	// actorID is the api_keys.id or users.id; actorLabel is "key:<label>" or
	// "user:<id>". Wired from server.go after getActorInfo is defined.
	// Falls back to ("", "unknown") when nil.
	GetActorInfo func(r *http.Request) (id, label string)

	// ServerURL is the base URL of this clustr-server instance as reachable by
	// deployed nodes (e.g. "http://10.99.0.1:8080"). Used to resolve the
	// RepoSentinelBuiltin sentinel to a concrete /repo/<distro>-<arch>/ URL.
	// Wired from server.go after the serverURL is computed from PXE config.
	ServerURL string

	// GPGKeyBytes is the ASCII-armored clustr release GPG public key, embedded
	// from build/slurm/keys/clustr-release.asc.pub. Wired from server.go via
	// server.GPGKeyBytes(). When set, it is carried through NodeConfig() into
	// api.SlurmNodeConfig.GPGKey so deploy/finalize.go can write it into node
	// chroots at /etc/pki/rpm-gpg/RPM-GPG-KEY-clustr and enable gpgcheck=1.
	GPGKeyBytes []byte

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
// If the module is already enabled, it also seeds any managed files that have
// no existing template yet (e.g. slurmdbd.conf added to defaultManagedFiles
// after the module was first enabled).
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

	// Seed any managed files that have no template yet.  This handles the case
	// where a file is added to defaultManagedFiles after the module was first
	// enabled (e.g. slurmdbd.conf).  seedDefaultTemplates is idempotent.
	if row.Enabled && len(row.ManagedFiles) > 0 {
		if err := m.seedDefaultTemplates(ctx, row.ClusterName, row.ManagedFiles); err != nil {
			log.Warn().Err(err).Msg("slurm: restoreFromDB: seed default templates failed (non-fatal)")
		}
	}
}

// ─── Enable ───────────────────────────────────────────────────────────────────

// EnableRequest is the body for POST /api/v1/slurm/enable.
type EnableRequest struct {
	ClusterName  string   `json:"cluster_name"`
	ManagedFiles []string `json:"managed_files,omitempty"`
	// SlurmRepoURL is the dnf repo URL used for auto-install at deploy time.
	// When set, finalize.go adds this repo to the node's dnf config inside the
	// chroot and runs `dnf install -y slurm slurm-slurmctld slurm-slurmd munge`
	// before writing Slurm config files.
	//
	// Special values:
	//   ""                 → same as "clustr-builtin" (back-compat, default)
	//   "clustr-builtin"   → use this clustr-server's bundled /repo/el9-x86_64/
	//                        (gpgcheck=1 with the embedded clustr key)
	//   any other string   → operator-provided URL, used verbatim (gpgcheck=0)
	//
	// Leave empty (or set to "clustr-builtin") for the turnkey bundled-repo path.
	SlurmRepoURL string `json:"slurm_repo_url,omitempty"`
}

// Enable activates the Slurm module with the given cluster configuration.
// Creates default config file templates from embedded defaults if no config
// files exist yet. Idempotent — safe to call while already enabled.
//
// Hard-fails if CLUSTR_SECRET_KEY is unset or is the insecure default: the
// Slurm module uses AES-256-GCM to encrypt munge keys and other secrets at
// rest. Without a deployment-specific key, every installation would share the
// same publicly-known encryption key.
func (m *Manager) Enable(ctx context.Context, req EnableRequest) error {
	if err := validateSecretKey(); err != nil {
		return err
	}

	if req.ClusterName == "" {
		return fmt.Errorf("slurm: cluster_name is required")
	}

	// Validate slurm_repo_url at enable time: HEAD check + EL version mismatch
	// detection so operators get immediate feedback rather than a silent deploy failure.
	// The clustr-builtin sentinel and empty string do not need validation —
	// the URL is resolved at deploy time from cfg.ServerURL, not stored.
	if req.SlurmRepoURL != "" && req.SlurmRepoURL != RepoSentinelBuiltin {
		validateSlurmRepoURL(ctx, req.SlurmRepoURL)
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
		SlurmRepoURL: req.SlurmRepoURL,
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

	// GAP-19: Auto-generate a munge key on first enable if one does not already
	// exist. This is idempotent — if a key already exists we leave it alone so
	// existing deployments are not invalidated. GenerateMungeKey uses upsert,
	// so calling it when a key already exists would rotate it; we guard against
	// that by checking first.
	if _, err := m.db.SlurmGetSecret(ctx, "munge.key"); err != nil {
		// ErrNoRows (or any error) → no key yet → generate one.
		if genErr := m.GenerateMungeKey(ctx); genErr != nil {
			// Non-fatal: the operator can generate the key manually via
			// POST /api/v1/slurm/munge-key/generate. Log clearly.
			log.Warn().Err(genErr).Msg("slurm: auto-generate munge key on enable failed (non-fatal) — run POST /slurm/munge-key/generate manually")
		} else {
			log.Info().Msg("slurm: munge key auto-generated on first enable")
		}
	} else {
		log.Debug().Msg("slurm: munge key already exists — skipping auto-generation")
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

		_, err = m.db.SlurmSaveConfigVersion(ctx, filename, buf.String(), "clustr-system", "Initial default template", true)
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
	Enabled         bool          `json:"enabled"`
	Status          string        `json:"status"`
	ClusterName     string        `json:"cluster_name"`
	SlurmRepoURL    string        `json:"slurm_repo_url,omitempty"`
	ManagedFiles    []string      `json:"managed_files"`
	ConnectedNodes  []string      `json:"connected_nodes"`
	MungeKeyPresent bool          `json:"munge_key_present"`
	DriftSummary    *DriftSummary `json:"drift_summary,omitempty"`
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

	// munge_key_present: true iff slurm_secrets has a row for "munge.key".
	_, mungeErr := m.db.SlurmGetSecret(ctx, "munge.key")
	mungeKeyPresent := mungeErr == nil

	resp := &SlurmModuleStatus{
		Enabled:         row.Enabled,
		Status:          row.Status,
		ClusterName:     row.ClusterName,
		SlurmRepoURL:    row.SlurmRepoURL,
		ManagedFiles:    row.ManagedFiles,
		ConnectedNodes:  connectedNodes,
		MungeKeyPresent: mungeKeyPresent,
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

	// Fetch and encode the munge key so finalize.go can write it to
	// /etc/munge/munge.key on the node.  A missing key is non-fatal here —
	// NodeConfig() still returns valid slurm config; the node will log a warning
	// and munge will fail to start.  The correct fix is POST /slurm/munge-key/generate.
	var mungeKeyB64 string
	if rawKey, mkErr := m.GetMungeKey(ctx); mkErr != nil {
		log.Warn().Err(mkErr).Str("node_id", nodeID).
			Msg("slurm: NodeConfig: munge key unavailable — node will boot without /etc/munge/munge.key (run POST /slurm/munge-key/generate)")
	} else {
		mungeKeyB64 = base64.StdEncoding.EncodeToString(rawKey)
	}

	// Resolve the SlurmRepoURL: empty string and the "clustr-builtin" sentinel
	// both map to the clustr-server's own bundled repo.  An arbitrary URL is
	// passed through unchanged so operators can override to a custom mirror.
	isBuiltin := cfg.SlurmRepoURL == "" || cfg.SlurmRepoURL == RepoSentinelBuiltin
	resolvedRepoURL := m.resolveRepoURL(cfg.SlurmRepoURL)

	// Carry the GPG key through to finalize.go only for the builtin path.
	// For operator-provided custom URLs, the operator owns GPG trust; we leave
	// gpgKeyB64 empty so deploy/finalize.go uses gpgcheck=0 as before.
	var gpgKeyB64 string
	if isBuiltin && len(m.GPGKeyBytes) > 0 {
		gpgKeyB64 = base64.StdEncoding.EncodeToString(m.GPGKeyBytes)
	}

	return &api.SlurmNodeConfig{
		ClusterName:  cfg.ClusterName,
		Roles:        roles,
		Configs:      configs,
		Scripts:      scripts,
		SlurmRepoURL: resolvedRepoURL,
		MungeKey:     mungeKeyB64,
		GPGKey:       gpgKeyB64,
	}, nil
}

// resolveRepoURL resolves the stored slurm_repo_url to the URL that will be
// written into the node's .repo file.
//
// Resolution rules:
//   - "" (empty) → clustr-builtin (default for new installs, back-compat)
//   - RepoSentinelBuiltin ("clustr-builtin") → ServerURL + "/repo/el9-x86_64/"
//   - any other string → returned unchanged (operator override)
//
// The "/repo/el9-x86_64/" path is the PR3 URL structure served by the
// bundled-repo HTTP handler. EL10 and other arches extend this naturally.
// MVP is EL9 x86_64 only; extending requires a new distro/arch parameter
// here once multi-target bundles land (see docs/slurm-build-pipeline.md §3).
func (m *Manager) resolveRepoURL(stored string) string {
	if stored == "" || stored == RepoSentinelBuiltin {
		serverURL := strings.TrimRight(m.ServerURL, "/")
		if serverURL == "" {
			// ServerURL not wired yet (e.g. tests that don't set it).
			// Fall back to a relative path that will at least be recognisable in logs.
			serverURL = "http://localhost:8080"
		}
		resolved := serverURL + "/repo/el9-x86_64/"
		log.Info().
			Str("stored_value", stored).
			Str("resolved_url", resolved).
			Msg("slurm: NodeConfig: resolved clustr-builtin sentinel to bundled repo URL")
		return resolved
	}
	return stored
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

// validateSlurmRepoURL performs a best-effort HEAD request to the repo URL and
// logs the result. Called at module-enable time to give the operator immediate
// feedback about URL reachability.
//
// This is purely advisory (non-fatal): if the URL is unreachable at enable time
// it may still become reachable later, or the operator may be setting it before
// the repo server is running. We never block enable on URL reachability.
func validateSlurmRepoURL(ctx context.Context, repoURL string) {
	// 5-second timeout for the HEAD request — enough to detect a dead host
	// without hanging the enable flow.
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, repoURL, nil)
	if err != nil {
		log.Warn().Str("slurm_repo_url", repoURL).Err(err).
			Msg("slurm: enable: could not build HEAD request for slurm_repo_url (URL may be invalid)")
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Warn().Str("slurm_repo_url", repoURL).Err(err).
			Msg("slurm: enable: slurm_repo_url is not reachable — dnf install will fail unless this is fixed before deploy")
		return
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusMovedPermanently, http.StatusFound:
		log.Info().Str("slurm_repo_url", repoURL).Int("status", resp.StatusCode).
			Msg("slurm: enable: slurm_repo_url reachability check passed")
	default:
		log.Warn().Str("slurm_repo_url", repoURL).Int("status", resp.StatusCode).
			Msg("slurm: enable: slurm_repo_url returned unexpected status — verify the URL is correct")
	}
}
