package db

import (
	"context"
	"fmt"
	"time"
)

// UserGroupMembership is a single user→group operator assignment.
type UserGroupMembership struct {
	UserID  string
	GroupID string
	Role    string // always "operator" in v1.0
}

// SetUserGroupMemberships replaces all group memberships for userID with the
// supplied set in a single transaction. Pass an empty slice to clear all.
func (db *DB) SetUserGroupMemberships(ctx context.Context, userID string, groupIDs []string) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: set user group memberships: begin tx: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM user_group_memberships WHERE user_id = ?`, userID,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: set user group memberships: delete old: %w", err)
	}

	for _, groupID := range groupIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO user_group_memberships (user_id, group_id, role) VALUES (?, ?, 'operator')`,
			userID, groupID,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("db: set user group memberships: insert group %s: %w", groupID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: set user group memberships: commit: %w", err)
	}
	return nil
}

// GetUserGroupMemberships returns all group IDs the user has operator access to.
func (db *DB) GetUserGroupMemberships(ctx context.Context, userID string) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT group_id FROM user_group_memberships WHERE user_id = ? ORDER BY group_id`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: get user group memberships: %w", err)
	}
	defer rows.Close()

	var groupIDs []string
	for rows.Next() {
		var gid string
		if err := rows.Scan(&gid); err != nil {
			return nil, fmt.Errorf("db: get user group memberships: scan: %w", err)
		}
		groupIDs = append(groupIDs, gid)
	}
	return groupIDs, rows.Err()
}

// UserHasGroupAccess returns true if userID is an operator member of groupID.
func (db *DB) UserHasGroupAccess(ctx context.Context, userID, groupID string) (bool, error) {
	var count int
	err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_group_memberships WHERE user_id = ? AND group_id = ?`,
		userID, groupID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("db: user has group access: %w", err)
	}
	return count > 0, nil
}

// ListAllUserGroupMemberships returns all memberships (all users, all groups).
// Used by admin Settings page to show the full assignment matrix.
func (db *DB) ListAllUserGroupMemberships(ctx context.Context) ([]UserGroupMembership, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT user_id, group_id, role FROM user_group_memberships ORDER BY user_id, group_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: list all user group memberships: %w", err)
	}
	defer rows.Close()

	var out []UserGroupMembership
	for rows.Next() {
		var m UserGroupMembership
		if err := rows.Scan(&m.UserID, &m.GroupID, &m.Role); err != nil {
			return nil, fmt.Errorf("db: list all user group memberships: scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetGroupIDForNode returns the primary group_id for a node (fast-path from node_configs).
// Returns "" if the node has no group assigned.
func (db *DB) GetGroupIDForNode(ctx context.Context, nodeID string) (string, error) {
	var groupID string
	err := db.sql.QueryRowContext(ctx,
		`SELECT COALESCE(group_id, '') FROM node_configs WHERE id = ?`, nodeID,
	).Scan(&groupID)
	if err != nil {
		return "", fmt.Errorf("db: get group id for node: %w", err)
	}
	return groupID, nil
}

// PurgeAuditLog deletes audit_log rows older than olderThan.
// Returns the number of rows deleted.
func (db *DB) PurgeAuditLog(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx,
		`DELETE FROM audit_log WHERE created_at < ?`, olderThan.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("db: purge audit log: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
