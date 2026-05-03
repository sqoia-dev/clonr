// slurm_repo.go — DB methods for clustr-internal-repo: per-node Slurm version
// tracking and per-cluster GPG key management.
// Sprint 17 additions. Follows the pattern established by internal/db/slurm_builds.go.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SlurmNodeVersionRow is one row in slurm_node_version.
// It tracks the Slurm version that clustr last successfully installed on a node.
type SlurmNodeVersionRow struct {
	NodeID          string
	DeployedVersion string // e.g. "25.11.5"
	BuildID         string // clustr build UUID; empty for artifact-install fallback
	InstallMethod   string // "dnf" | "artifact"
	InstalledAt     int64
	InstalledBy     string
}

// SlurmUpsertNodeVersion records (or updates) the Slurm version on a node.
// Called after a successful dnf upgrade or artifact install ack.
func (db *DB) SlurmUpsertNodeVersion(ctx context.Context, row SlurmNodeVersionRow) error {
	if row.InstalledAt == 0 {
		row.InstalledAt = time.Now().Unix()
	}
	if row.InstalledBy == "" {
		row.InstalledBy = "clustr-server"
	}
	if row.InstallMethod == "" {
		row.InstallMethod = "dnf"
	}

	var buildIDArg interface{}
	if row.BuildID != "" {
		buildIDArg = row.BuildID
	}

	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_node_version
			(node_id, deployed_version, build_id, install_method, installed_at, installed_by)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			deployed_version = excluded.deployed_version,
			build_id         = excluded.build_id,
			install_method   = excluded.install_method,
			installed_at     = excluded.installed_at,
			installed_by     = excluded.installed_by
	`, row.NodeID, row.DeployedVersion, buildIDArg, row.InstallMethod, row.InstalledAt, row.InstalledBy)
	if err != nil {
		return fmt.Errorf("db: SlurmUpsertNodeVersion: %w", err)
	}
	return nil
}

// SlurmGetNodeVersion returns the deployed Slurm version for a node.
// Returns sql.ErrNoRows if the node has never had Slurm deployed by clustr.
func (db *DB) SlurmGetNodeVersion(ctx context.Context, nodeID string) (*SlurmNodeVersionRow, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT node_id, deployed_version, COALESCE(build_id,''), install_method, installed_at, installed_by
		FROM slurm_node_version WHERE node_id = ?
	`, nodeID)
	var r SlurmNodeVersionRow
	if err := row.Scan(&r.NodeID, &r.DeployedVersion, &r.BuildID,
		&r.InstallMethod, &r.InstalledAt, &r.InstalledBy); err != nil {
		return nil, err
	}
	return &r, nil
}

// SlurmListNodeVersions returns all per-node version rows for all nodes.
// Returns empty slice (not nil) if none exist yet.
func (db *DB) SlurmListNodeVersions(ctx context.Context) ([]SlurmNodeVersionRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT node_id, deployed_version, COALESCE(build_id,''), install_method, installed_at, installed_by
		FROM slurm_node_version ORDER BY installed_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmListNodeVersions: %w", err)
	}
	defer rows.Close()

	var result []SlurmNodeVersionRow
	for rows.Next() {
		var r SlurmNodeVersionRow
		if err := rows.Scan(&r.NodeID, &r.DeployedVersion, &r.BuildID,
			&r.InstallMethod, &r.InstalledAt, &r.InstalledBy); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	if result == nil {
		result = []SlurmNodeVersionRow{}
	}
	return result, rows.Err()
}

// SlurmNodesAtVersion returns node IDs where deployed_version matches the given version.
// Used by ValidateUpgrade to determine how many nodes are already at the target.
func (db *DB) SlurmNodesAtVersion(ctx context.Context, version string) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT node_id FROM slurm_node_version WHERE deployed_version = ?`, version)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmNodesAtVersion: %w", err)
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

// ─── Per-cluster GPG key (for clustr-internal-repo) ──────────────────────────

// SlurmRepoGPGConfig holds the per-cluster GPG public key stored on the module
// config row. The private key is stored encrypted in slurm_secrets under key_type
// "repo.gpg.private".
type SlurmRepoGPGConfig struct {
	PublicKeyArmored string // ASCII-armored GPG public key
	KeyID            string // short key ID, e.g. "41E51A6653BBA540"
}

// SlurmGetRepoGPGConfig returns the per-cluster GPG key config from the module config row.
// Returns nil, sql.ErrNoRows if no GPG key has been generated yet.
func (db *DB) SlurmGetRepoGPGConfig(ctx context.Context) (*SlurmRepoGPGConfig, error) {
	var pub, keyID sql.NullString
	err := db.sql.QueryRowContext(ctx,
		`SELECT repo_gpg_public_key, repo_gpg_key_id FROM slurm_module_config WHERE id = 1`,
	).Scan(&pub, &keyID)
	if err != nil {
		return nil, err
	}
	if !pub.Valid || pub.String == "" {
		return nil, sql.ErrNoRows
	}
	return &SlurmRepoGPGConfig{
		PublicKeyArmored: pub.String,
		KeyID:            keyID.String,
	}, nil
}

// SlurmSetRepoGPGConfig stores the per-cluster GPG public key on the module config row.
func (db *DB) SlurmSetRepoGPGConfig(ctx context.Context, cfg SlurmRepoGPGConfig) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		UPDATE slurm_module_config
		SET repo_gpg_public_key = ?, repo_gpg_key_id = ?, updated_at = ?
		WHERE id = 1
	`, cfg.PublicKeyArmored, cfg.KeyID, now)
	if err != nil {
		return fmt.Errorf("db: SlurmSetRepoGPGConfig: %w", err)
	}
	return nil
}
