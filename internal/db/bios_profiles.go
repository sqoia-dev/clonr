package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── bios_profiles CRUD (#159) ───────────────────────────────────────────────

// CreateBiosProfile inserts a new bios_profiles row.
func (db *DB) CreateBiosProfile(ctx context.Context, p api.BiosProfile) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO bios_profiles (id, name, vendor, settings_json, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, p.ID, p.Name, p.Vendor, p.SettingsJSON, p.Description,
		p.CreatedAt.Unix(), p.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("db: create bios profile: %w", err)
	}
	return nil
}

// GetBiosProfile returns a bios_profiles row by ID.
// Returns api.ErrNotFound when absent.
func (db *DB) GetBiosProfile(ctx context.Context, id string) (api.BiosProfile, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, vendor, settings_json, description, created_at, updated_at
		FROM bios_profiles WHERE id = ?
	`, id)
	return scanBiosProfile(row)
}

// GetBiosProfileByName returns a bios_profiles row by name.
// Returns api.ErrNotFound when absent.
func (db *DB) GetBiosProfileByName(ctx context.Context, name string) (api.BiosProfile, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, vendor, settings_json, description, created_at, updated_at
		FROM bios_profiles WHERE name = ?
	`, name)
	return scanBiosProfile(row)
}

// ListBiosProfiles returns all bios_profiles rows ordered by name.
func (db *DB) ListBiosProfiles(ctx context.Context) ([]api.BiosProfile, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, vendor, settings_json, description, created_at, updated_at
		FROM bios_profiles ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list bios profiles: %w", err)
	}
	defer rows.Close()

	var profiles []api.BiosProfile
	for rows.Next() {
		p, err := scanBiosProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// UpdateBiosProfile updates the mutable fields (name, settings_json, description)
// of an existing bios_profiles row and bumps updated_at.
func (db *DB) UpdateBiosProfile(ctx context.Context, id, name, settingsJSON, description string) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE bios_profiles
		SET name = ?, settings_json = ?, description = ?, updated_at = ?
		WHERE id = ?
	`, name, settingsJSON, description, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("db: update bios profile: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// DeleteBiosProfile removes a bios_profiles row by ID.
// Returns api.ErrNotFound when absent.
// The caller must detach all node_bios_profile bindings before calling this
// to honour the FK constraint.
func (db *DB) DeleteBiosProfile(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM bios_profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete bios profile: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// BiosProfileRefCount returns the number of node_bios_profile rows referencing
// the given profile ID.  Used to enforce the 409 guard on DELETE.
func (db *DB) BiosProfileRefCount(ctx context.Context, id string) (int, error) {
	var count int
	err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_bios_profile WHERE profile_id = ?`, id,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db: count bios profile refs: %w", err)
	}
	return count, nil
}

// ─── node_bios_profile CRUD (#159) ───────────────────────────────────────────

// AssignBiosProfile upserts a node_bios_profile row, binding nodeID to
// profileID.  Resets last_applied_at and applied_settings_hash on the
// assumption that the new profile may differ from the last applied one.
func (db *DB) AssignBiosProfile(ctx context.Context, nodeID, profileID string) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO node_bios_profile (node_id, profile_id)
		VALUES (?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			profile_id = excluded.profile_id,
			last_applied_at = NULL,
			applied_settings_hash = NULL,
			last_apply_error = NULL
	`, nodeID, profileID)
	if err != nil {
		return fmt.Errorf("db: assign bios profile: %w", err)
	}
	return nil
}

// DetachBiosProfile removes the node_bios_profile row for nodeID.
// Returns nil (not ErrNotFound) when no row exists — detach is idempotent.
func (db *DB) DetachBiosProfile(ctx context.Context, nodeID string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM node_bios_profile WHERE node_id = ?`, nodeID)
	if err != nil {
		return fmt.Errorf("db: detach bios profile: %w", err)
	}
	return nil
}

// GetNodeBiosProfile returns the node_bios_profile row for nodeID.
// Returns api.ErrNotFound when no profile is assigned.
func (db *DB) GetNodeBiosProfile(ctx context.Context, nodeID string) (api.NodeBiosProfile, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT node_id, profile_id, last_applied_at, applied_settings_hash, last_apply_error
		FROM node_bios_profile WHERE node_id = ?
	`, nodeID)
	return scanNodeBiosProfile(row)
}

// RecordBiosApply updates node_bios_profile after a successful apply with the
// current time as last_applied_at, the profile settings hash, and clears any
// previous error.
func (db *DB) RecordBiosApply(ctx context.Context, nodeID, settingsHash string) error {
	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		UPDATE node_bios_profile
		SET last_applied_at = ?, applied_settings_hash = ?, last_apply_error = NULL
		WHERE node_id = ?
	`, now, settingsHash, nodeID)
	if err != nil {
		return fmt.Errorf("db: record bios apply: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// RecordBiosApplyError records a failure in node_bios_profile.last_apply_error.
// last_applied_at and applied_settings_hash are left unchanged (they reflect
// the last successful apply).
func (db *DB) RecordBiosApplyError(ctx context.Context, nodeID, errMsg string) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE node_bios_profile SET last_apply_error = ? WHERE node_id = ?
	`, errMsg, nodeID)
	if err != nil {
		return fmt.Errorf("db: record bios apply error: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// ─── scan helpers ─────────────────────────────────────────────────────────────

type biosProfileScanner interface {
	Scan(dest ...any) error
}

func scanBiosProfile(s biosProfileScanner) (api.BiosProfile, error) {
	var (
		p             api.BiosProfile
		createdAtUnix int64
		updatedAtUnix int64
	)
	err := s.Scan(&p.ID, &p.Name, &p.Vendor, &p.SettingsJSON, &p.Description,
		&createdAtUnix, &updatedAtUnix)
	if err == sql.ErrNoRows {
		return api.BiosProfile{}, api.ErrNotFound
	}
	if err != nil {
		return api.BiosProfile{}, fmt.Errorf("db: scan bios profile: %w", err)
	}
	p.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	p.UpdatedAt = time.Unix(updatedAtUnix, 0).UTC()
	return p, nil
}

func scanNodeBiosProfile(s biosProfileScanner) (api.NodeBiosProfile, error) {
	var (
		nbp              api.NodeBiosProfile
		lastAppliedUnix  sql.NullInt64
		settingsHash     sql.NullString
		lastApplyErr     sql.NullString
	)
	err := s.Scan(&nbp.NodeID, &nbp.ProfileID, &lastAppliedUnix, &settingsHash, &lastApplyErr)
	if err == sql.ErrNoRows {
		return api.NodeBiosProfile{}, api.ErrNotFound
	}
	if err != nil {
		return api.NodeBiosProfile{}, fmt.Errorf("db: scan node bios profile: %w", err)
	}
	if lastAppliedUnix.Valid {
		t := time.Unix(lastAppliedUnix.Int64, 0).UTC()
		nbp.LastAppliedAt = &t
	}
	if settingsHash.Valid {
		nbp.AppliedSettingsHash = settingsHash.String
	}
	if lastApplyErr.Valid {
		nbp.LastApplyError = lastApplyErr.String
	}
	return nbp, nil
}
