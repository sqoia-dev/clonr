// slurm_builds.go — DB methods for Slurm build pipeline: secrets, deps, active build.
// Sprint 8 additions. Follows the same patterns as internal/db/slurm.go.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ─── Secrets ─────────────────────────────────────────────────────────────────

// SlurmSecretRow is one row in the slurm_secrets table.
type SlurmSecretRow struct {
	KeyType        string // e.g. "munge.key"
	EncryptedValue string // hex-encoded encrypted ciphertext
	RotatedAt      int64  // unix timestamp
	RotatedBy      string // "system" or username
}

// SlurmUpsertSecret inserts or replaces a secret row.
func (db *DB) SlurmUpsertSecret(ctx context.Context, row SlurmSecretRow) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_secrets (key_type, encrypted_value, rotated_at, rotated_by)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key_type) DO UPDATE SET
			encrypted_value = excluded.encrypted_value,
			rotated_at      = excluded.rotated_at,
			rotated_by      = excluded.rotated_by
	`, row.KeyType, row.EncryptedValue, row.RotatedAt, row.RotatedBy)
	if err != nil {
		return fmt.Errorf("db: SlurmUpsertSecret: %w", err)
	}
	return nil
}

// SlurmGetSecret retrieves a secret row by key_type.
// Returns sql.ErrNoRows if the key does not exist.
func (db *DB) SlurmGetSecret(ctx context.Context, keyType string) (*SlurmSecretRow, error) {
	row := db.sql.QueryRowContext(ctx,
		`SELECT key_type, encrypted_value, rotated_at, rotated_by FROM slurm_secrets WHERE key_type = ?`,
		keyType)
	var r SlurmSecretRow
	var rotatedBy sql.NullString
	if err := row.Scan(&r.KeyType, &r.EncryptedValue, &r.RotatedAt, &rotatedBy); err != nil {
		return nil, err
	}
	r.RotatedBy = rotatedBy.String
	return &r, nil
}

// SlurmListSecrets returns all secret rows (without the encrypted_value for safety).
// Callers that need the value should use SlurmGetSecret.
func (db *DB) SlurmListSecrets(ctx context.Context) ([]SlurmSecretRow, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT key_type, rotated_at, rotated_by FROM slurm_secrets ORDER BY key_type`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmListSecrets: %w", err)
	}
	defer rows.Close()

	var result []SlurmSecretRow
	for rows.Next() {
		var r SlurmSecretRow
		var rotatedBy sql.NullString
		if err := rows.Scan(&r.KeyType, &r.RotatedAt, &rotatedBy); err != nil {
			return nil, err
		}
		r.RotatedBy = rotatedBy.String
		result = append(result, r)
	}
	return result, rows.Err()
}

// ─── Build dependencies ───────────────────────────────────────────────────────

// SlurmInsertBuildDep inserts one dependency artifact record for a build.
func (db *DB) SlurmInsertBuildDep(ctx context.Context, dep SlurmBuildDepRow) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO slurm_build_deps (id, build_id, dep_name, dep_version, artifact_path, artifact_checksum)
		VALUES (?, ?, ?, ?, ?, ?)
	`, dep.ID, dep.BuildID, dep.DepName, dep.DepVersion, dep.ArtifactPath, dep.ArtifactChecksum)
	if err != nil {
		return fmt.Errorf("db: SlurmInsertBuildDep: %w", err)
	}
	return nil
}

// SlurmGetBuildDeps returns all dependency artifacts for a given build.
func (db *DB) SlurmGetBuildDeps(ctx context.Context, buildID string) ([]SlurmBuildDepRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, build_id, dep_name, dep_version, artifact_path, artifact_checksum
		FROM slurm_build_deps WHERE build_id = ?
		ORDER BY dep_name
	`, buildID)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmGetBuildDeps: %w", err)
	}
	defer rows.Close()

	var result []SlurmBuildDepRow
	for rows.Next() {
		var r SlurmBuildDepRow
		if err := rows.Scan(&r.ID, &r.BuildID, &r.DepName, &r.DepVersion,
			&r.ArtifactPath, &r.ArtifactChecksum); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ─── Active build ─────────────────────────────────────────────────────────────

// SlurmSetActiveBuild sets the active build ID on the singleton module config.
// The build must exist and have status "completed".
func (db *DB) SlurmSetActiveBuild(ctx context.Context, buildID string) error {
	// Verify the build exists and is completed.
	var status string
	if err := db.sql.QueryRowContext(ctx,
		`SELECT status FROM slurm_builds WHERE id = ?`, buildID,
	).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("db: SlurmSetActiveBuild: build %s not found", buildID)
		}
		return fmt.Errorf("db: SlurmSetActiveBuild: lookup: %w", err)
	}
	if status != "completed" {
		return fmt.Errorf("db: SlurmSetActiveBuild: build %s is not completed (status: %s)", buildID, status)
	}

	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx,
		`UPDATE slurm_module_config SET active_build_id = ?, updated_at = ? WHERE id = 1`,
		buildID, now)
	if err != nil {
		return fmt.Errorf("db: SlurmSetActiveBuild: %w", err)
	}
	return nil
}

// SlurmGetActiveBuildID returns the active build ID from the module config,
// or empty string if none is set.
func (db *DB) SlurmGetActiveBuildID(ctx context.Context) (string, error) {
	var activeBuildID sql.NullString
	err := db.sql.QueryRowContext(ctx,
		`SELECT active_build_id FROM slurm_module_config WHERE id = 1`,
	).Scan(&activeBuildID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db: SlurmGetActiveBuildID: %w", err)
	}
	return activeBuildID.String, nil
}

// SlurmListDepMatrix returns all rows in the dependency compatibility matrix.
func (db *DB) SlurmListDepMatrix(ctx context.Context) ([]SlurmDepMatrixRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, slurm_version_min, slurm_version_max, dep_name, dep_version_min, dep_version_max, source, created_at
		FROM slurm_dep_matrix ORDER BY slurm_version_min, dep_name
	`)
	if err != nil {
		return nil, fmt.Errorf("db: SlurmListDepMatrix: %w", err)
	}
	defer rows.Close()

	var result []SlurmDepMatrixRow
	for rows.Next() {
		var r SlurmDepMatrixRow
		if err := rows.Scan(&r.ID, &r.SlurmVersionMin, &r.SlurmVersionMax,
			&r.DepName, &r.DepVersionMin, &r.DepVersionMax, &r.Source, &r.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// SlurmDeleteBuild deletes a build record and its artifact metadata.
// Does NOT delete the on-disk artifact file — caller is responsible.
// Returns an error if the build is the currently active build.
func (db *DB) SlurmDeleteBuild(ctx context.Context, buildID string) error {
	// Prevent deleting the active build.
	activeID, err := db.SlurmGetActiveBuildID(ctx)
	if err != nil {
		return err
	}
	if activeID == buildID {
		return fmt.Errorf("db: SlurmDeleteBuild: cannot delete the active build; set a different build active first")
	}

	_, err = db.sql.ExecContext(ctx, `DELETE FROM slurm_builds WHERE id = ?`, buildID)
	if err != nil {
		return fmt.Errorf("db: SlurmDeleteBuild: %w", err)
	}
	return nil
}
