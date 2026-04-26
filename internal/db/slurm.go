// slurm.go — DB methods for the Slurm module.
// All methods follow the pattern established by internal/db/ldap.go.
package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ─── Row structs ──────────────────────────────────────────────────────────────

// SlurmModuleConfigRow is the persisted state of the Slurm module singleton.
type SlurmModuleConfigRow struct {
	Enabled      bool
	Status       string   // not_configured|ready|disabled|error
	ClusterName  string
	ManagedFiles []string // decoded from JSON
	SlurmRepoURL string   // optional dnf repo URL for auto-install at deploy time
	CreatedAt    int64
	UpdatedAt    int64
}

// SlurmConfigFileRow represents one version of a managed config file.
type SlurmConfigFileRow struct {
	ID         string
	Filename   string
	Version    int
	Content    string
	IsTemplate bool
	Checksum   string
	AuthoredBy string
	Message    string
	CreatedAt  int64
}

// SlurmNodeOverrideRow is one per-node override key/value pair.
type SlurmNodeOverrideRow struct {
	ID          string
	NodeID      string
	OverrideKey string
	Value       string
	UpdatedAt   int64
}

// SlurmNodeConfigStateRow tracks which config version is live on a node.
type SlurmNodeConfigStateRow struct {
	NodeID          string
	Filename        string
	DeployedVersion int
	ContentHash     string
	DeployedAt      int64
	PushOpID        string
	SlurmVersion    string
}

// SlurmDriftRow represents a node/file pair that is out of sync.
type SlurmDriftRow struct {
	NodeID          string
	Filename        string
	CurrentVersion  int    // latest version in slurm_config_files
	DeployedVersion int    // version on node (0 if never deployed)
	InSync          bool
}

// SlurmPushOperationRow is the persisted state of one push operation.
type SlurmPushOperationRow struct {
	ID           string
	Filenames    []string       // decoded from JSON
	FileVersions map[string]int // decoded from JSON
	InitiatedBy  string
	ApplyAction  string
	Status       string
	NodeCount    int
	SuccessCount int
	FailureCount int
	StartedAt    int64
	CompletedAt  *int64
	NodeResults  json.RawMessage
}

// SlurmPushOpUpdate carries the mutable fields for SlurmUpdatePushOp.
type SlurmPushOpUpdate struct {
	Status       string
	SuccessCount int
	FailureCount int
	CompletedAt  *int64
	NodeResults  json.RawMessage
}

// SlurmScriptRow is one version of a managed Slurm hook script.
type SlurmScriptRow struct {
	ID         string
	ScriptType string
	Version    int
	Content    string
	DestPath   string
	Checksum   string
	AuthoredBy string
	Message    string
	CreatedAt  int64
}

// SlurmScriptConfigRow holds the enable/path config for one script type.
type SlurmScriptConfigRow struct {
	ScriptType string
	DestPath   string
	Enabled    bool
	UpdatedAt  int64
}

// SlurmScriptStateRow tracks which script version is live on a node.
type SlurmScriptStateRow struct {
	NodeID          string
	ScriptType      string
	DeployedVersion int
	ContentHash     string
	DeployedAt      int64
	PushOpID        string
}

// SlurmBuildRow is one Slurm build attempt.
type SlurmBuildRow struct {
	ID                 string
	Version            string
	Arch               string
	Status             string
	ConfigureFlags     []string // decoded from JSON
	ArtifactPath       string
	ArtifactChecksum   string
	ArtifactSizeBytes  int64
	InitiatedBy        string
	LogKey             string
	StartedAt          int64
	CompletedAt        *int64
	ErrorMessage       string
}

// SlurmBuildUpdate carries the mutable fields for SlurmUpdateBuild.
type SlurmBuildUpdate struct {
	Status            string
	ArtifactPath      string
	ArtifactChecksum  string
	ArtifactSizeBytes int64
	CompletedAt       *int64
	ErrorMessage      string
}

// SlurmBuildDepRow is one dependency artifact used in a build.
type SlurmBuildDepRow struct {
	ID               string
	BuildID          string
	DepName          string
	DepVersion       string
	ArtifactPath     string
	ArtifactChecksum string
}

// SlurmDepMatrixRow is one row in the dependency compatibility matrix.
type SlurmDepMatrixRow struct {
	ID               string
	SlurmVersionMin  string
	SlurmVersionMax  string
	DepName          string
	DepVersionMin    string
	DepVersionMax    string
	Source           string
	CreatedAt        int64
}

// SlurmDepRange is the resolved version range for one dependency.
type SlurmDepRange struct {
	DepName       string
	DepVersionMin string
	DepVersionMax string
}

// SlurmUpgradeOpRow is one rolling upgrade attempt.
type SlurmUpgradeOpRow struct {
	ID                 string
	FromBuildID        string
	ToBuildID          string
	Status             string
	BatchSize          int
	DrainTimeoutMin    int
	ConfirmedDBBackup  bool
	InitiatedBy        string
	Phase              string
	CurrentBatch       int
	TotalBatches       int
	StartedAt          int64
	CompletedAt        *int64
	NodeResults        json.RawMessage
}

// SlurmUpgradeOpUpdate carries the mutable fields for SlurmUpdateUpgradeOp.
type SlurmUpgradeOpUpdate struct {
	Status        string
	Phase         string
	CurrentBatch  int
	TotalBatches  int
	CompletedAt   *int64
	NodeResults   json.RawMessage
}

// ─── Module config (singleton) ────────────────────────────────────────────────

// SlurmGetConfig reads the singleton Slurm module config row.
// Returns sql.ErrNoRows if the row has never been inserted.
func (db *DB) SlurmGetConfig(ctx context.Context) (*SlurmModuleConfigRow, error) {
	row := db.sql.QueryRowContext(ctx,
		`SELECT enabled, status, cluster_name, managed_files, slurm_repo_url, created_at, updated_at
		 FROM slurm_module_config WHERE id = 1`)

	var cfg SlurmModuleConfigRow
	var clusterName sql.NullString
	var managedFilesJSON string
	var repoURL sql.NullString

	if err := row.Scan(&cfg.Enabled, &cfg.Status, &clusterName, &managedFilesJSON,
		&repoURL, &cfg.CreatedAt, &cfg.UpdatedAt); err != nil {
		return nil, err
	}

	cfg.ClusterName = clusterName.String
	cfg.SlurmRepoURL = repoURL.String
	if managedFilesJSON != "" {
		_ = json.Unmarshal([]byte(managedFilesJSON), &cfg.ManagedFiles)
	}
	if cfg.ManagedFiles == nil {
		cfg.ManagedFiles = []string{}
	}
	return &cfg, nil
}

// SlurmSaveConfig writes the singleton config row (INSERT OR REPLACE).
func (db *DB) SlurmSaveConfig(ctx context.Context, cfg SlurmModuleConfigRow) error {
	filesJSON, err := json.Marshal(cfg.ManagedFiles)
	if err != nil {
		return fmt.Errorf("db: SlurmSaveConfig: marshal managed_files: %w", err)
	}
	now := time.Now().Unix()
	var repoURL interface{}
	if cfg.SlurmRepoURL != "" {
		repoURL = cfg.SlurmRepoURL
	}
	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO slurm_module_config (id, enabled, status, cluster_name, managed_files, slurm_repo_url, created_at, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled        = excluded.enabled,
			status         = excluded.status,
			cluster_name   = excluded.cluster_name,
			managed_files  = excluded.managed_files,
			slurm_repo_url = excluded.slurm_repo_url,
			updated_at     = excluded.updated_at
	`, cfg.Enabled, cfg.Status, cfg.ClusterName, string(filesJSON), repoURL, now, now)
	if err != nil {
		return fmt.Errorf("db: SlurmSaveConfig: %w", err)
	}
	return nil
}

// SlurmSetStatus updates only the status column on the singleton row.
func (db *DB) SlurmSetStatus(ctx context.Context, status string) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE slurm_module_config SET status = ?, updated_at = ? WHERE id = 1`,
		status, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("db: SlurmSetStatus: %w", err)
	}
	return nil
}

// ─── Config file CRUD ─────────────────────────────────────────────────────────

// SlurmGetCurrentConfig returns the highest-version row for the given filename.
func (db *DB) SlurmGetCurrentConfig(ctx context.Context, filename string) (*SlurmConfigFileRow, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, filename, version, content, is_template, checksum, authored_by, message, created_at
		FROM slurm_config_files
		WHERE filename = ?
		ORDER BY version DESC
		LIMIT 1
	`, filename)
	return scanSlurmConfigFile(row)
}

// SlurmGetConfigVersion returns a specific version of a config file.
func (db *DB) SlurmGetConfigVersion(ctx context.Context, filename string, version int) (*SlurmConfigFileRow, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, filename, version, content, is_template, checksum, authored_by, message, created_at
		FROM slurm_config_files
		WHERE filename = ? AND version = ?
	`, filename, version)
	return scanSlurmConfigFile(row)
}

// SlurmListConfigHistory returns all versions of a file, newest first.
func (db *DB) SlurmListConfigHistory(ctx context.Context, filename string) ([]SlurmConfigFileRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, filename, version, content, is_template, checksum, authored_by, message, created_at
		FROM slurm_config_files
		WHERE filename = ?
		ORDER BY version DESC
	`, filename)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmListConfigHistory: %w", err)
	}
	defer rows.Close()
	return scanSlurmConfigFileRows(rows)
}

// SlurmListCurrentConfigs returns the current (max) version of every managed file.
func (db *DB) SlurmListCurrentConfigs(ctx context.Context) ([]SlurmConfigFileRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, filename, version, content, is_template, checksum, authored_by, message, created_at
		FROM slurm_config_files
		WHERE (filename, version) IN (
			SELECT filename, MAX(version) FROM slurm_config_files GROUP BY filename
		)
		ORDER BY filename
	`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmListCurrentConfigs: %w", err)
	}
	defer rows.Close()
	return scanSlurmConfigFileRows(rows)
}

// SlurmSaveConfigVersion inserts a new version row for the given filename.
// The version number is MAX(version)+1 for that filename (or 1 if no rows exist).
// Returns the new version number.
func (db *DB) SlurmSaveConfigVersion(ctx context.Context, filename, content, authoredBy, message string) (int, error) {
	checksum := computeSHA256(content)
	id := uuid.New().String()
	now := time.Now().Unix()

	// Determine next version atomically via MAX+1 within the INSERT.
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_config_files (id, filename, version, content, is_template, checksum, authored_by, message, created_at)
		SELECT ?, ?, COALESCE(MAX(version), 0) + 1, ?, 0, ?, ?, ?, ?
		FROM slurm_config_files
		WHERE filename = ?
	`, id, filename, content, checksum, authoredBy, message, now, filename)
	if err != nil {
		return 0, fmt.Errorf("db: SlurmSaveConfigVersion: %w", err)
	}

	// Read back the version we just inserted.
	var ver int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT version FROM slurm_config_files WHERE id = ?`, id,
	).Scan(&ver); err != nil {
		return 0, fmt.Errorf("db: SlurmSaveConfigVersion: read version: %w", err)
	}
	return ver, nil
}

// scanSlurmConfigFile scans one row from slurm_config_files.
func scanSlurmConfigFile(row *sql.Row) (*SlurmConfigFileRow, error) {
	var r SlurmConfigFileRow
	var authoredBy, message sql.NullString
	var isTemplate int
	if err := row.Scan(&r.ID, &r.Filename, &r.Version, &r.Content,
		&isTemplate, &r.Checksum, &authoredBy, &message, &r.CreatedAt); err != nil {
		return nil, err
	}
	r.IsTemplate = isTemplate != 0
	r.AuthoredBy = authoredBy.String
	r.Message = message.String
	return &r, nil
}

// scanSlurmConfigFileRows scans multiple rows from slurm_config_files.
func scanSlurmConfigFileRows(rows *sql.Rows) ([]SlurmConfigFileRow, error) {
	var result []SlurmConfigFileRow
	for rows.Next() {
		var r SlurmConfigFileRow
		var authoredBy, message sql.NullString
		var isTemplate int
		if err := rows.Scan(&r.ID, &r.Filename, &r.Version, &r.Content,
			&isTemplate, &r.Checksum, &authoredBy, &message, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.IsTemplate = isTemplate != 0
		r.AuthoredBy = authoredBy.String
		r.Message = message.String
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// ─── Node overrides ───────────────────────────────────────────────────────────

// SlurmGetNodeOverrides returns all override key/value pairs for a node.
func (db *DB) SlurmGetNodeOverrides(ctx context.Context, nodeID string) (map[string]string, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT override_key, value FROM slurm_node_overrides WHERE node_id = ?`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmGetNodeOverrides: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		result[k] = v
	}
	return result, rows.Err()
}

// SlurmSaveNodeOverrides upserts override key/value pairs for a node.
func (db *DB) SlurmSaveNodeOverrides(ctx context.Context, nodeID string, overrides map[string]string) error {
	now := time.Now().Unix()
	for k, v := range overrides {
		id := uuid.New().String()
		_, err := db.sql.ExecContext(ctx, `
			INSERT INTO slurm_node_overrides (id, node_id, override_key, value, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(node_id, override_key) DO UPDATE SET
				value      = excluded.value,
				updated_at = excluded.updated_at
		`, id, nodeID, k, v, now)
		if err != nil {
			return fmt.Errorf("db: SlurmSaveNodeOverrides: %w", err)
		}
	}
	return nil
}

// ─── Node config state (drift) ────────────────────────────────────────────────

// SlurmGetNodeConfigState returns all file sync state rows for one node.
func (db *DB) SlurmGetNodeConfigState(ctx context.Context, nodeID string) ([]SlurmNodeConfigStateRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT node_id, filename, deployed_version, content_hash, deployed_at, push_op_id, slurm_version
		FROM slurm_node_config_state
		WHERE node_id = ?
	`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmGetNodeConfigState: %w", err)
	}
	defer rows.Close()
	return scanNodeConfigStateRows(rows)
}

// SlurmUpsertNodeConfigState inserts or updates the sync record for (node, file).
func (db *DB) SlurmUpsertNodeConfigState(ctx context.Context, nodeID, filename string, version int, contentHash, pushOpID string) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_node_config_state (node_id, filename, deployed_version, content_hash, deployed_at, push_op_id)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id, filename) DO UPDATE SET
			deployed_version = excluded.deployed_version,
			content_hash     = excluded.content_hash,
			deployed_at      = excluded.deployed_at,
			push_op_id       = excluded.push_op_id
	`, nodeID, filename, version, contentHash, now, pushOpID)
	if err != nil {
		return fmt.Errorf("db: SlurmUpsertNodeConfigState: %w", err)
	}
	return nil
}

// SlurmDriftQuery returns drift status per (node, file) by comparing
// the current max version in slurm_config_files against slurm_node_config_state.
func (db *DB) SlurmDriftQuery(ctx context.Context) ([]SlurmDriftRow, error) {
	// Get all current (max) versions per file.
	curRows, err := db.sql.QueryContext(ctx,
		`SELECT filename, MAX(version) FROM slurm_config_files GROUP BY filename`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmDriftQuery: current versions: %w", err)
	}
	defer curRows.Close()

	current := make(map[string]int)
	for curRows.Next() {
		var fn string
		var ver int
		if err := curRows.Scan(&fn, &ver); err != nil {
			return nil, err
		}
		current[fn] = ver
	}
	if err := curRows.Err(); err != nil {
		return nil, err
	}

	// Get all node state rows.
	stateRows, err := db.sql.QueryContext(ctx,
		`SELECT node_id, filename, deployed_version, content_hash, deployed_at, push_op_id, slurm_version
		 FROM slurm_node_config_state`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmDriftQuery: state rows: %w", err)
	}
	defer stateRows.Close()

	states, err := scanNodeConfigStateRows(stateRows)
	if err != nil {
		return nil, err
	}

	var result []SlurmDriftRow
	for _, s := range states {
		curVer := current[s.Filename]
		result = append(result, SlurmDriftRow{
			NodeID:          s.NodeID,
			Filename:        s.Filename,
			CurrentVersion:  curVer,
			DeployedVersion: s.DeployedVersion,
			InSync:          curVer == s.DeployedVersion,
		})
	}
	return result, nil
}

func scanNodeConfigStateRows(rows *sql.Rows) ([]SlurmNodeConfigStateRow, error) {
	var result []SlurmNodeConfigStateRow
	for rows.Next() {
		var r SlurmNodeConfigStateRow
		var pushOpID, slurmVersion sql.NullString
		if err := rows.Scan(&r.NodeID, &r.Filename, &r.DeployedVersion, &r.ContentHash,
			&r.DeployedAt, &pushOpID, &slurmVersion); err != nil {
			return nil, err
		}
		r.PushOpID = pushOpID.String
		r.SlurmVersion = slurmVersion.String
		result = append(result, r)
	}
	return result, rows.Err()
}

// ─── Push operations ──────────────────────────────────────────────────────────

// SlurmCreatePushOp inserts a new push operation row.
func (db *DB) SlurmCreatePushOp(ctx context.Context, op SlurmPushOperationRow) error {
	filenamesJSON, _ := json.Marshal(op.Filenames)
	versionsJSON, _ := json.Marshal(op.FileVersions)

	var completedAt *int64
	if op.CompletedAt != nil {
		completedAt = op.CompletedAt
	}
	var nodeResultsStr sql.NullString
	if len(op.NodeResults) > 0 {
		nodeResultsStr = sql.NullString{String: string(op.NodeResults), Valid: true}
	}

	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_push_operations
			(id, filenames, file_versions, initiated_by, apply_action, status,
			 node_count, success_count, failure_count, started_at, completed_at, node_results)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, op.ID, string(filenamesJSON), string(versionsJSON), op.InitiatedBy, op.ApplyAction,
		op.Status, op.NodeCount, op.SuccessCount, op.FailureCount,
		op.StartedAt, completedAt, nodeResultsStr)
	if err != nil {
		return fmt.Errorf("db: SlurmCreatePushOp: %w", err)
	}
	return nil
}

// SlurmUpdatePushOp updates mutable fields of a push operation.
func (db *DB) SlurmUpdatePushOp(ctx context.Context, opID string, u SlurmPushOpUpdate) error {
	var nodeResultsStr sql.NullString
	if len(u.NodeResults) > 0 {
		nodeResultsStr = sql.NullString{String: string(u.NodeResults), Valid: true}
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE slurm_push_operations SET
			status        = ?,
			success_count = ?,
			failure_count = ?,
			completed_at  = ?,
			node_results  = ?
		WHERE id = ?
	`, u.Status, u.SuccessCount, u.FailureCount, u.CompletedAt, nodeResultsStr, opID)
	if err != nil {
		return fmt.Errorf("db: SlurmUpdatePushOp: %w", err)
	}
	return nil
}

// SlurmGetPushOp retrieves a push operation by ID.
func (db *DB) SlurmGetPushOp(ctx context.Context, opID string) (*SlurmPushOperationRow, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, filenames, file_versions, initiated_by, apply_action, status,
		       node_count, success_count, failure_count, started_at, completed_at, node_results
		FROM slurm_push_operations WHERE id = ?
	`, opID)
	return scanSlurmPushOp(row)
}

func scanSlurmPushOp(row *sql.Row) (*SlurmPushOperationRow, error) {
	var r SlurmPushOperationRow
	var initiatedBy, nodeResults sql.NullString
	var filenamesJSON, versionsJSON string
	if err := row.Scan(&r.ID, &filenamesJSON, &versionsJSON, &initiatedBy, &r.ApplyAction,
		&r.Status, &r.NodeCount, &r.SuccessCount, &r.FailureCount,
		&r.StartedAt, &r.CompletedAt, &nodeResults); err != nil {
		return nil, err
	}
	r.InitiatedBy = initiatedBy.String
	_ = json.Unmarshal([]byte(filenamesJSON), &r.Filenames)
	_ = json.Unmarshal([]byte(versionsJSON), &r.FileVersions)
	if nodeResults.Valid {
		r.NodeResults = json.RawMessage(nodeResults.String)
	}
	return &r, nil
}

// ─── Node roles ───────────────────────────────────────────────────────────────

// SlurmGetNodeRoles returns the roles assigned to a node (parsed from JSON).
func (db *DB) SlurmGetNodeRoles(ctx context.Context, nodeID string) ([]string, error) {
	var rolesJSON string
	err := db.sql.QueryRowContext(ctx,
		`SELECT roles FROM slurm_node_roles WHERE node_id = ?`, nodeID,
	).Scan(&rolesJSON)
	if err == sql.ErrNoRows {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: SlurmGetNodeRoles: %w", err)
	}
	var roles []string
	_ = json.Unmarshal([]byte(rolesJSON), &roles)
	if roles == nil {
		roles = []string{}
	}
	return roles, nil
}

// SlurmSetNodeRoles upserts the roles for a node.
func (db *DB) SlurmSetNodeRoles(ctx context.Context, nodeID string, roles []string, autoDetect bool) error {
	if roles == nil {
		roles = []string{}
	}
	rolesJSON, err := json.Marshal(roles)
	if err != nil {
		return fmt.Errorf("db: SlurmSetNodeRoles: marshal roles: %w", err)
	}
	autoDetectInt := 0
	if autoDetect {
		autoDetectInt = 1
	}
	now := time.Now().Unix()
	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO slurm_node_roles (node_id, roles, auto_detect, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			roles       = excluded.roles,
			auto_detect = excluded.auto_detect,
			updated_at  = excluded.updated_at
	`, nodeID, string(rolesJSON), autoDetectInt, now)
	if err != nil {
		return fmt.Errorf("db: SlurmSetNodeRoles: %w", err)
	}
	return nil
}

// SlurmListNodesByRole returns node IDs that have the given role in their JSON array.
// Uses SQLite JSON_EACH for accurate membership test.
func (db *DB) SlurmListNodesByRole(ctx context.Context, role string) ([]string, error) {
	// Use JSON_EACH via a subquery — works on SQLite 3.38+ (ship with go-sqlite3).
	// Falls back gracefully: if JSON_EACH is unavailable, LIKE provides approximate results.
	rows, err := db.sql.QueryContext(ctx, `
		SELECT DISTINCT node_id
		FROM slurm_node_roles
		WHERE EXISTS (
			SELECT 1 FROM JSON_EACH(slurm_node_roles.roles) WHERE value = ?
		)
	`, role)
	if err != nil {
		// Fallback: LIKE-based search (slightly broader but acceptable for v1).
		rows, err = db.sql.QueryContext(ctx,
			`SELECT node_id FROM slurm_node_roles WHERE roles LIKE ?`,
			"%\""+role+"\"%")
		if err != nil {
			return nil, fmt.Errorf("db: SlurmListNodesByRole: %w", err)
		}
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SlurmNodeRoleEntry is a single (node_id, roles) pair returned by SlurmListAllNodeRoles.
type SlurmNodeRoleEntry struct {
	NodeID string
	Roles  []string
}

// SlurmListAllNodeRoles returns all (node_id, roles) pairs in slurm_node_roles.
// Used by the renderer to build the full node list for slurm.conf generation.
func (db *DB) SlurmListAllNodeRoles(ctx context.Context) ([]SlurmNodeRoleEntry, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT node_id, roles FROM slurm_node_roles ORDER BY node_id`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmListAllNodeRoles: %w", err)
	}
	defer rows.Close()

	var entries []SlurmNodeRoleEntry
	for rows.Next() {
		var nodeID, rolesJSON string
		if err := rows.Scan(&nodeID, &rolesJSON); err != nil {
			return nil, err
		}
		var roles []string
		_ = json.Unmarshal([]byte(rolesJSON), &roles)
		if roles == nil {
			roles = []string{}
		}
		entries = append(entries, SlurmNodeRoleEntry{NodeID: nodeID, Roles: roles})
	}
	return entries, rows.Err()
}

// SlurmRoleSummary returns a map of role → node count across all nodes.
func (db *DB) SlurmRoleSummary(ctx context.Context) (map[string]int, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT roles FROM slurm_node_roles`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmRoleSummary: %w", err)
	}
	defer rows.Close()

	summary := make(map[string]int)
	for rows.Next() {
		var rolesJSON string
		if err := rows.Scan(&rolesJSON); err != nil {
			return nil, err
		}
		var roles []string
		if err := json.Unmarshal([]byte(rolesJSON), &roles); err != nil {
			continue
		}
		for _, r := range roles {
			summary[r]++
		}
	}
	return summary, rows.Err()
}

// ─── Scripts ──────────────────────────────────────────────────────────────────

// SlurmGetCurrentScript returns the highest-version script for the given type.
func (db *DB) SlurmGetCurrentScript(ctx context.Context, scriptType string) (*SlurmScriptRow, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, script_type, version, content, dest_path, checksum, authored_by, message, created_at
		FROM slurm_scripts
		WHERE script_type = ?
		ORDER BY version DESC
		LIMIT 1
	`, scriptType)
	return scanSlurmScript(row)
}

// SlurmGetScriptVersion returns a specific version of a script.
func (db *DB) SlurmGetScriptVersion(ctx context.Context, scriptType string, version int) (*SlurmScriptRow, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, script_type, version, content, dest_path, checksum, authored_by, message, created_at
		FROM slurm_scripts
		WHERE script_type = ? AND version = ?
	`, scriptType, version)
	return scanSlurmScript(row)
}

// SlurmListScriptHistory returns all versions of a script, newest first.
func (db *DB) SlurmListScriptHistory(ctx context.Context, scriptType string) ([]SlurmScriptRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, script_type, version, content, dest_path, checksum, authored_by, message, created_at
		FROM slurm_scripts
		WHERE script_type = ?
		ORDER BY version DESC
	`, scriptType)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmListScriptHistory: %w", err)
	}
	defer rows.Close()
	return scanSlurmScriptRows(rows)
}

// SlurmSaveScriptVersion inserts a new script version.
// Returns the new version number.
func (db *DB) SlurmSaveScriptVersion(ctx context.Context, scriptType, destPath, content, authoredBy, message string) (int, error) {
	checksum := computeSHA256(content)
	id := uuid.New().String()
	now := time.Now().Unix()

	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_scripts (id, script_type, version, content, dest_path, checksum, authored_by, message, created_at)
		SELECT ?, ?, COALESCE(MAX(version), 0) + 1, ?, ?, ?, ?, ?, ?
		FROM slurm_scripts WHERE script_type = ?
	`, id, scriptType, content, destPath, checksum, authoredBy, message, now, scriptType)
	if err != nil {
		return 0, fmt.Errorf("db: SlurmSaveScriptVersion: %w", err)
	}

	var ver int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT version FROM slurm_scripts WHERE id = ?`, id,
	).Scan(&ver); err != nil {
		return 0, fmt.Errorf("db: SlurmSaveScriptVersion: read version: %w", err)
	}
	return ver, nil
}

// SlurmListScriptConfigs returns all script configuration rows.
func (db *DB) SlurmListScriptConfigs(ctx context.Context) ([]SlurmScriptConfigRow, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT script_type, dest_path, enabled, updated_at FROM slurm_script_config ORDER BY script_type`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmListScriptConfigs: %w", err)
	}
	defer rows.Close()

	var result []SlurmScriptConfigRow
	for rows.Next() {
		var r SlurmScriptConfigRow
		var enabled int
		if err := rows.Scan(&r.ScriptType, &r.DestPath, &enabled, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		result = append(result, r)
	}
	return result, rows.Err()
}

// SlurmUpsertScriptConfig inserts or updates a script config row.
func (db *DB) SlurmUpsertScriptConfig(ctx context.Context, cfg SlurmScriptConfigRow) error {
	enabledInt := 0
	if cfg.Enabled {
		enabledInt = 1
	}
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_script_config (script_type, dest_path, enabled, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(script_type) DO UPDATE SET
			dest_path  = excluded.dest_path,
			enabled    = excluded.enabled,
			updated_at = excluded.updated_at
	`, cfg.ScriptType, cfg.DestPath, enabledInt, now)
	if err != nil {
		return fmt.Errorf("db: SlurmUpsertScriptConfig: %w", err)
	}
	return nil
}

// SlurmGetScriptState returns all script sync state rows for one node.
func (db *DB) SlurmGetScriptState(ctx context.Context, nodeID string) ([]SlurmScriptStateRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT node_id, script_type, deployed_version, content_hash, deployed_at, push_op_id
		FROM slurm_script_state WHERE node_id = ?
	`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmGetScriptState: %w", err)
	}
	defer rows.Close()

	var result []SlurmScriptStateRow
	for rows.Next() {
		var r SlurmScriptStateRow
		var pushOpID sql.NullString
		if err := rows.Scan(&r.NodeID, &r.ScriptType, &r.DeployedVersion,
			&r.ContentHash, &r.DeployedAt, &pushOpID); err != nil {
			return nil, err
		}
		r.PushOpID = pushOpID.String
		result = append(result, r)
	}
	return result, rows.Err()
}

// SlurmUpsertScriptState inserts or updates a script sync state row.
func (db *DB) SlurmUpsertScriptState(ctx context.Context, nodeID, scriptType string, version int, hash, pushOpID string) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_script_state (node_id, script_type, deployed_version, content_hash, deployed_at, push_op_id)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id, script_type) DO UPDATE SET
			deployed_version = excluded.deployed_version,
			content_hash     = excluded.content_hash,
			deployed_at      = excluded.deployed_at,
			push_op_id       = excluded.push_op_id
	`, nodeID, scriptType, version, hash, now, pushOpID)
	if err != nil {
		return fmt.Errorf("db: SlurmUpsertScriptState: %w", err)
	}
	return nil
}

func scanSlurmScript(row *sql.Row) (*SlurmScriptRow, error) {
	var r SlurmScriptRow
	var authoredBy, message sql.NullString
	if err := row.Scan(&r.ID, &r.ScriptType, &r.Version, &r.Content, &r.DestPath,
		&r.Checksum, &authoredBy, &message, &r.CreatedAt); err != nil {
		return nil, err
	}
	r.AuthoredBy = authoredBy.String
	r.Message = message.String
	return &r, nil
}

func scanSlurmScriptRows(rows *sql.Rows) ([]SlurmScriptRow, error) {
	var result []SlurmScriptRow
	for rows.Next() {
		var r SlurmScriptRow
		var authoredBy, message sql.NullString
		if err := rows.Scan(&r.ID, &r.ScriptType, &r.Version, &r.Content, &r.DestPath,
			&r.Checksum, &authoredBy, &message, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.AuthoredBy = authoredBy.String
		r.Message = message.String
		result = append(result, r)
	}
	return result, rows.Err()
}

// ─── Builds ───────────────────────────────────────────────────────────────────

// SlurmCreateBuild inserts a new build row.
func (db *DB) SlurmCreateBuild(ctx context.Context, b SlurmBuildRow) error {
	flagsJSON, _ := json.Marshal(b.ConfigureFlags)
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_builds
			(id, version, arch, status, configure_flags, artifact_path, artifact_checksum,
			 artifact_size_bytes, initiated_by, log_key, started_at, completed_at, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, b.ID, b.Version, b.Arch, b.Status, string(flagsJSON),
		b.ArtifactPath, b.ArtifactChecksum, b.ArtifactSizeBytes,
		b.InitiatedBy, b.LogKey, b.StartedAt, b.CompletedAt, b.ErrorMessage)
	if err != nil {
		return fmt.Errorf("db: SlurmCreateBuild: %w", err)
	}
	return nil
}

// SlurmUpdateBuild updates mutable fields of a build row.
func (db *DB) SlurmUpdateBuild(ctx context.Context, id string, u SlurmBuildUpdate) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE slurm_builds SET
			status               = ?,
			artifact_path        = ?,
			artifact_checksum    = ?,
			artifact_size_bytes  = ?,
			completed_at         = ?,
			error_message        = ?
		WHERE id = ?
	`, u.Status, u.ArtifactPath, u.ArtifactChecksum, u.ArtifactSizeBytes,
		u.CompletedAt, u.ErrorMessage, id)
	if err != nil {
		return fmt.Errorf("db: SlurmUpdateBuild: %w", err)
	}
	return nil
}

// SlurmGetBuild retrieves a build by ID.
func (db *DB) SlurmGetBuild(ctx context.Context, id string) (*SlurmBuildRow, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, version, arch, status, configure_flags, artifact_path, artifact_checksum,
		       artifact_size_bytes, initiated_by, log_key, started_at, completed_at, error_message
		FROM slurm_builds WHERE id = ?
	`, id)
	return scanSlurmBuild(row)
}

// SlurmListBuilds returns all builds ordered by start time descending.
func (db *DB) SlurmListBuilds(ctx context.Context) ([]SlurmBuildRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, version, arch, status, configure_flags, artifact_path, artifact_checksum,
		       artifact_size_bytes, initiated_by, log_key, started_at, completed_at, error_message
		FROM slurm_builds ORDER BY started_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmListBuilds: %w", err)
	}
	defer rows.Close()

	var result []SlurmBuildRow
	for rows.Next() {
		var r SlurmBuildRow
		var flagsJSON string
		var artifactPath, artifactChecksum, initiatedBy, logKey, errorMessage sql.NullString
		var artifactSizeBytes sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Version, &r.Arch, &r.Status, &flagsJSON,
			&artifactPath, &artifactChecksum, &artifactSizeBytes,
			&initiatedBy, &logKey, &r.StartedAt, &r.CompletedAt, &errorMessage); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(flagsJSON), &r.ConfigureFlags)
		r.ArtifactPath = artifactPath.String
		r.ArtifactChecksum = artifactChecksum.String
		r.ArtifactSizeBytes = artifactSizeBytes.Int64
		r.InitiatedBy = initiatedBy.String
		r.LogKey = logKey.String
		r.ErrorMessage = errorMessage.String
		result = append(result, r)
	}
	return result, rows.Err()
}

func scanSlurmBuild(row *sql.Row) (*SlurmBuildRow, error) {
	var r SlurmBuildRow
	var flagsJSON string
	var artifactPath, artifactChecksum, initiatedBy, logKey, errorMessage sql.NullString
	var artifactSizeBytes sql.NullInt64
	if err := row.Scan(&r.ID, &r.Version, &r.Arch, &r.Status, &flagsJSON,
		&artifactPath, &artifactChecksum, &artifactSizeBytes,
		&initiatedBy, &logKey, &r.StartedAt, &r.CompletedAt, &errorMessage); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(flagsJSON), &r.ConfigureFlags)
	r.ArtifactPath = artifactPath.String
	r.ArtifactChecksum = artifactChecksum.String
	r.ArtifactSizeBytes = artifactSizeBytes.Int64
	r.InitiatedBy = initiatedBy.String
	r.LogKey = logKey.String
	r.ErrorMessage = errorMessage.String
	return &r, nil
}

// ─── Upgrade operations ───────────────────────────────────────────────────────

// SlurmCreateUpgradeOp inserts a new upgrade operation.
func (db *DB) SlurmCreateUpgradeOp(ctx context.Context, op SlurmUpgradeOpRow) error {
	confirmedInt := 0
	if op.ConfirmedDBBackup {
		confirmedInt = 1
	}
	var nodeResultsStr sql.NullString
	if len(op.NodeResults) > 0 {
		nodeResultsStr = sql.NullString{String: string(op.NodeResults), Valid: true}
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_upgrade_operations
			(id, from_build_id, to_build_id, status, batch_size, drain_timeout_min,
			 confirmed_db_backup, initiated_by, phase, current_batch, total_batches,
			 started_at, completed_at, node_results)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, op.ID, op.FromBuildID, op.ToBuildID, op.Status, op.BatchSize, op.DrainTimeoutMin,
		confirmedInt, op.InitiatedBy, op.Phase, op.CurrentBatch, op.TotalBatches,
		op.StartedAt, op.CompletedAt, nodeResultsStr)
	if err != nil {
		return fmt.Errorf("db: SlurmCreateUpgradeOp: %w", err)
	}
	return nil
}

// SlurmUpdateUpgradeOp updates mutable fields of an upgrade operation.
func (db *DB) SlurmUpdateUpgradeOp(ctx context.Context, id string, u SlurmUpgradeOpUpdate) error {
	var nodeResultsStr sql.NullString
	if len(u.NodeResults) > 0 {
		nodeResultsStr = sql.NullString{String: string(u.NodeResults), Valid: true}
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE slurm_upgrade_operations SET
			status        = ?,
			phase         = ?,
			current_batch = ?,
			total_batches = ?,
			completed_at  = ?,
			node_results  = ?
		WHERE id = ?
	`, u.Status, u.Phase, u.CurrentBatch, u.TotalBatches, u.CompletedAt, nodeResultsStr, id)
	if err != nil {
		return fmt.Errorf("db: SlurmUpdateUpgradeOp: %w", err)
	}
	return nil
}

// SlurmGetUpgradeOp retrieves an upgrade operation by ID.
func (db *DB) SlurmGetUpgradeOp(ctx context.Context, id string) (*SlurmUpgradeOpRow, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, from_build_id, to_build_id, status, batch_size, drain_timeout_min,
		       confirmed_db_backup, initiated_by, phase, current_batch, total_batches,
		       started_at, completed_at, node_results
		FROM slurm_upgrade_operations WHERE id = ?
	`, id)
	var r SlurmUpgradeOpRow
	var initiatedBy, phase, nodeResults sql.NullString
	var currentBatch, totalBatches sql.NullInt64
	var confirmedInt int
	if err := row.Scan(&r.ID, &r.FromBuildID, &r.ToBuildID, &r.Status, &r.BatchSize,
		&r.DrainTimeoutMin, &confirmedInt, &initiatedBy, &phase,
		&currentBatch, &totalBatches, &r.StartedAt, &r.CompletedAt, &nodeResults); err != nil {
		return nil, err
	}
	r.ConfirmedDBBackup = confirmedInt != 0
	r.InitiatedBy = initiatedBy.String
	r.Phase = phase.String
	r.CurrentBatch = int(currentBatch.Int64)
	r.TotalBatches = int(totalBatches.Int64)
	if nodeResults.Valid {
		r.NodeResults = json.RawMessage(nodeResults.String)
	}
	return &r, nil
}

// SlurmUpdateUpgradeOpResults updates only the node_results and status fields,
// leaving phase/current_batch/total_batches/completed_at unchanged.
// Used by the upgrade orchestrator to write per-node results frequently without
// risking reset of progress counters.
func (db *DB) SlurmUpdateUpgradeOpResults(ctx context.Context, id string, nodeResults json.RawMessage) error {
	var nodeResultsStr sql.NullString
	if len(nodeResults) > 0 {
		nodeResultsStr = sql.NullString{String: string(nodeResults), Valid: true}
	}
	_, err := db.sql.ExecContext(ctx,
		`UPDATE slurm_upgrade_operations SET node_results = ? WHERE id = ?`,
		nodeResultsStr, id)
	if err != nil {
		return fmt.Errorf("db: SlurmUpdateUpgradeOpResults: %w", err)
	}
	return nil
}

// SlurmListUpgradeOps returns all upgrade operations ordered by start time descending.
func (db *DB) SlurmListUpgradeOps(ctx context.Context) ([]SlurmUpgradeOpRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, from_build_id, to_build_id, status, batch_size, drain_timeout_min,
		       confirmed_db_backup, initiated_by, phase, current_batch, total_batches,
		       started_at, completed_at, node_results
		FROM slurm_upgrade_operations ORDER BY started_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmListUpgradeOps: %w", err)
	}
	defer rows.Close()

	var result []SlurmUpgradeOpRow
	for rows.Next() {
		var r SlurmUpgradeOpRow
		var initiatedBy, phase, nodeResults sql.NullString
		var currentBatch, totalBatches sql.NullInt64
		var confirmedInt int
		if err := rows.Scan(&r.ID, &r.FromBuildID, &r.ToBuildID, &r.Status, &r.BatchSize,
			&r.DrainTimeoutMin, &confirmedInt, &initiatedBy, &phase,
			&currentBatch, &totalBatches, &r.StartedAt, &r.CompletedAt, &nodeResults); err != nil {
			return nil, err
		}
		r.ConfirmedDBBackup = confirmedInt != 0
		r.InitiatedBy = initiatedBy.String
		r.Phase = phase.String
		r.CurrentBatch = int(currentBatch.Int64)
		r.TotalBatches = int(totalBatches.Int64)
		if nodeResults.Valid {
			r.NodeResults = json.RawMessage(nodeResults.String)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ─── Dep matrix ───────────────────────────────────────────────────────────────

// SlurmSeedDepMatrix inserts dep matrix rows using INSERT OR IGNORE.
func (db *DB) SlurmSeedDepMatrix(ctx context.Context, entries []SlurmDepMatrixRow) error {
	for _, e := range entries {
		_, err := db.sql.ExecContext(ctx, `
			INSERT OR IGNORE INTO slurm_dep_matrix
				(id, slurm_version_min, slurm_version_max, dep_name, dep_version_min, dep_version_max, source, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, e.ID, e.SlurmVersionMin, e.SlurmVersionMax, e.DepName, e.DepVersionMin, e.DepVersionMax, e.Source, e.CreatedAt)
		if err != nil {
			return fmt.Errorf("db: SlurmSeedDepMatrix: %w", err)
		}
	}
	return nil
}

// SlurmResolveDepVersions returns the compatible dependency version ranges for a given Slurm version.
// When multiple rows exist for the same dep_name (e.g. after re-seeding with a newer bundled version),
// the most recently inserted row wins (MAX(created_at)), so that updated defaults take effect.
func (db *DB) SlurmResolveDepVersions(ctx context.Context, slurmVersion string) (map[string]SlurmDepRange, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT dep_name, dep_version_min, dep_version_max
		FROM slurm_dep_matrix
		WHERE slurm_version_min <= ? AND slurm_version_max > ?
		  AND created_at = (
		    SELECT MAX(m2.created_at)
		    FROM slurm_dep_matrix m2
		    WHERE m2.dep_name = slurm_dep_matrix.dep_name
		      AND m2.slurm_version_min <= ?
		      AND m2.slurm_version_max > ?
		  )
		GROUP BY dep_name
	`, slurmVersion, slurmVersion, slurmVersion, slurmVersion)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmResolveDepVersions: %w", err)
	}
	defer rows.Close()

	result := make(map[string]SlurmDepRange)
	for rows.Next() {
		var r SlurmDepRange
		if err := rows.Scan(&r.DepName, &r.DepVersionMin, &r.DepVersionMax); err != nil {
			return nil, err
		}
		result[r.DepName] = r
	}
	return result, rows.Err()
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// computeSHA256 returns the hex-encoded SHA-256 of s.
func computeSHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}
