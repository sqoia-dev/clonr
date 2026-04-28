// auto_policy.go — DB operations for the auto-compute allocation policy engine
// (Sprint H, v1.7.0 / CF-29).
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AutoPolicyConfig is the admin-configurable singleton that governs the
// auto-compute policy engine. Stored in auto_policy_config (migration 073).
type AutoPolicyConfig struct {
	Enabled                 bool
	DefaultNodeCount        int
	DefaultHardwareProfile  string // JSON
	DefaultPartitionTemplate string
	DefaultRole             string
	NotifyAdminsOnCreate    bool
	UpdatedAt               time.Time
}

// GetAutoPolicyConfig reads the singleton auto-policy configuration row.
// Always returns a non-nil config (seeded by migration 073).
func (db *DB) GetAutoPolicyConfig(ctx context.Context) (*AutoPolicyConfig, error) {
	var cfg AutoPolicyConfig
	var updatedAt int64
	err := db.sql.QueryRowContext(ctx, `
		SELECT enabled, default_node_count, default_hardware_profile,
		       default_partition_template, default_role,
		       notify_admins_on_create, updated_at
		FROM auto_policy_config WHERE id = 'default'
	`).Scan(
		&cfg.Enabled, &cfg.DefaultNodeCount, &cfg.DefaultHardwareProfile,
		&cfg.DefaultPartitionTemplate, &cfg.DefaultRole,
		&cfg.NotifyAdminsOnCreate, &updatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("db: get auto policy config: %w", err)
	}
	cfg.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &cfg, nil
}

// UpdateAutoPolicyConfig writes all editable fields of the auto-policy config.
func (db *DB) UpdateAutoPolicyConfig(ctx context.Context, cfg AutoPolicyConfig) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE auto_policy_config
		SET enabled = ?, default_node_count = ?, default_hardware_profile = ?,
		    default_partition_template = ?, default_role = ?,
		    notify_admins_on_create = ?, updated_at = ?
		WHERE id = 'default'
	`,
		boolToInt(cfg.Enabled), cfg.DefaultNodeCount, cfg.DefaultHardwareProfile,
		cfg.DefaultPartitionTemplate, cfg.DefaultRole,
		boolToInt(cfg.NotifyAdminsOnCreate), time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: update auto policy config: %w", err)
	}
	return nil
}

// SetAutoComputeState writes the auto_policy_state JSON blob and marks the
// NodeGroup as auto_compute=1. Called by the policy engine after it has
// successfully completed all creation steps (H1).
func (db *DB) SetAutoComputeState(ctx context.Context, groupID string, stateJSON string) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE node_groups
		SET auto_compute = 1, auto_policy_state = ?, updated_at = ?
		WHERE id = ?
	`, stateJSON, time.Now().Unix(), groupID)
	if err != nil {
		return fmt.Errorf("db: set auto compute state: %w", err)
	}
	return nil
}

// GetAutoComputeState reads the auto_policy_state for a NodeGroup.
// Returns ("", nil) when the group exists but has no policy state.
func (db *DB) GetAutoComputeState(ctx context.Context, groupID string) (string, *time.Time, error) {
	var stateJSON sql.NullString
	var finalizedAt sql.NullInt64
	err := db.sql.QueryRowContext(ctx, `
		SELECT auto_policy_state, auto_policy_finalized_at
		FROM node_groups WHERE id = ?
	`, groupID).Scan(&stateJSON, &finalizedAt)
	if err == sql.ErrNoRows {
		return "", nil, fmt.Errorf("db: group %s not found", groupID)
	}
	if err != nil {
		return "", nil, fmt.Errorf("db: get auto compute state: %w", err)
	}
	var fin *time.Time
	if finalizedAt.Valid {
		t := time.Unix(finalizedAt.Int64, 0).UTC()
		fin = &t
	}
	return stateJSON.String, fin, nil
}

// FinalizeAutoComputeState marks the undo window as closed by setting
// auto_policy_finalized_at on the NodeGroup.
func (db *DB) FinalizeAutoComputeState(ctx context.Context, groupID string) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE node_groups
		SET auto_policy_finalized_at = ?, updated_at = ?
		WHERE id = ? AND auto_compute = 1 AND auto_policy_finalized_at IS NULL
	`, time.Now().Unix(), time.Now().Unix(), groupID)
	if err != nil {
		return fmt.Errorf("db: finalize auto compute state: %w", err)
	}
	return nil
}

// ListPendingAutoComputeGroups returns all NodeGroups that have auto_compute=1
// but have not yet been finalized. Used by the background finalizer worker.
func (db *DB) ListPendingAutoComputeGroups(ctx context.Context) ([]PendingAutoGroup, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, auto_policy_state, created_at
		FROM node_groups
		WHERE auto_compute = 1 AND auto_policy_finalized_at IS NULL
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list pending auto compute groups: %w", err)
	}
	defer rows.Close()

	var out []PendingAutoGroup
	for rows.Next() {
		var g PendingAutoGroup
		var stateJSON sql.NullString
		var createdAt int64
		if err := rows.Scan(&g.GroupID, &stateJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("db: scan pending auto compute: %w", err)
		}
		g.StateJSON = stateJSON.String
		g.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, g)
	}
	return out, rows.Err()
}

// PendingAutoGroup is a row from ListPendingAutoComputeGroups.
type PendingAutoGroup struct {
	GroupID   string
	StateJSON string
	CreatedAt time.Time
}

// ClearAutoComputeState clears the auto_compute flag and state JSON from a
// NodeGroup after a successful undo operation (H3).
func (db *DB) ClearAutoComputeState(ctx context.Context, groupID string) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE node_groups
		SET auto_compute = 0, auto_policy_state = NULL, auto_policy_finalized_at = NULL, updated_at = ?
		WHERE id = ?
	`, time.Now().Unix(), groupID)
	if err != nil {
		return fmt.Errorf("db: clear auto compute state: %w", err)
	}
	return nil
}

// ─── PI onboarding wizard ────────────────────────────────────────────────────

// MarkOnboardingCompleted sets onboarding_completed=1 on the given user.
// Idempotent: safe to call multiple times.
func (db *DB) MarkOnboardingCompleted(ctx context.Context, userID string) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE users SET onboarding_completed = 1 WHERE id = ?
	`, userID)
	if err != nil {
		return fmt.Errorf("db: mark onboarding completed: %w", err)
	}
	return nil
}

// IsOnboardingCompleted returns true if the user has completed the wizard.
func (db *DB) IsOnboardingCompleted(ctx context.Context, userID string) (bool, error) {
	var v int
	err := db.sql.QueryRowContext(ctx, `
		SELECT onboarding_completed FROM users WHERE id = ?
	`, userID).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db: check onboarding: %w", err)
	}
	return v == 1, nil
}

// GetAdminEmails returns the clustr username (used as email address) for all
// admin-role users. Used by notify_admins_on_create.
func (db *DB) GetAdminEmails(ctx context.Context) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT username FROM users WHERE role = 'admin' ORDER BY username ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: get admin emails: %w", err)
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		emails = append(emails, u)
	}
	return emails, rows.Err()
}
