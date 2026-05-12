// project_managers.go — DB operations for G3 PI manager delegation (Sprint G / CF-09).
//
// A PI can deputize co-managers for their NodeGroups. Managers have the same
// per-project rights as the PI but are NOT the project owner.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ProjectManager is one row in the project_managers join table.
type ProjectManager struct {
	ID              string
	NodeGroupID     string
	NodeGroupName   string // populated by joins
	UserID          string
	Username        string // populated by joins
	GrantedByUserID string
	GrantedByName   string // populated by joins
	GrantedAt       time.Time
}

// ErrManagerNotFound is returned when a delegation row does not exist.
var ErrManagerNotFound = fmt.Errorf("db: manager delegation not found")

// AddProjectManager creates a manager delegation. Returns ErrManagerNotFound
// replacement — actually returns duplicate if already delegated (ignored by handler).
func (db *DB) AddProjectManager(ctx context.Context, nodeGroupID, userID, grantedByUserID string) (*ProjectManager, error) {
	id := uuid.New().String()
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO project_managers (id, node_group_id, user_id, granted_by_user_id, granted_at)
		VALUES (?, ?, ?, ?, ?)`,
		id, nodeGroupID, userID, grantedByUserID, now,
	)
	if err != nil {
		// SQLite unique constraint fires when already delegated.
		return nil, fmt.Errorf("db: add project manager: %w", err)
	}
	return db.GetProjectManager(ctx, id)
}

// RemoveProjectManager removes a manager delegation by nodeGroupID + userID.
func (db *DB) RemoveProjectManager(ctx context.Context, nodeGroupID, userID string) error {
	res, err := db.sql.ExecContext(ctx, `
		DELETE FROM project_managers WHERE node_group_id = ? AND user_id = ?`,
		nodeGroupID, userID,
	)
	if err != nil {
		return fmt.Errorf("db: remove project manager: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrManagerNotFound
	}
	return nil
}

// GetProjectManager returns one delegation row by its primary key ID.
func (db *DB) GetProjectManager(ctx context.Context, id string) (*ProjectManager, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT pm.id, pm.node_group_id, ng.name,
		       pm.user_id, u.username,
		       pm.granted_by_user_id, gb.username,
		       pm.granted_at
		FROM project_managers pm
		JOIN node_groups ng ON ng.id = pm.node_group_id
		JOIN users u        ON u.id  = pm.user_id
		JOIN users gb       ON gb.id = pm.granted_by_user_id
		WHERE pm.id = ?`, id)
	return scanProjectManager(row)
}

// ListProjectManagersForGroup returns all managers for a given NodeGroup.
func (db *DB) ListProjectManagersForGroup(ctx context.Context, nodeGroupID string) ([]ProjectManager, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT pm.id, pm.node_group_id, ng.name,
		       pm.user_id, u.username,
		       pm.granted_by_user_id, gb.username,
		       pm.granted_at
		FROM project_managers pm
		JOIN node_groups ng ON ng.id = pm.node_group_id
		JOIN users u        ON u.id  = pm.user_id
		JOIN users gb       ON gb.id = pm.granted_by_user_id
		WHERE pm.node_group_id = ?
		ORDER BY pm.granted_at ASC`, nodeGroupID)
	if err != nil {
		return nil, fmt.Errorf("db: list project managers for group: %w", err)
	}
	defer rows.Close()
	return scanProjectManagers(rows)
}

// ListManagedGroupsForUser returns all NodeGroups where userID has been delegated
// as manager. Used by the PI portal middleware to augment the user's group list.
// Does not join on the dropped pi_user_id column.
func (db *DB) ListManagedGroupsForUser(ctx context.Context, userID string) ([]NodeGroupSummary, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT ng.id, ng.name, ng.description, ng.role,
		       (SELECT COUNT(*) FROM node_group_memberships m WHERE m.group_id = ng.id) AS node_count,
		       (SELECT COUNT(*) FROM node_configs nc
		         LEFT JOIN node_group_memberships m2 ON m2.node_id = nc.id AND m2.is_primary = 1
		         WHERE m2.group_id = ng.id AND nc.deploy_completed_preboot_at IS NOT NULL) AS deployed_count,
		       ng.created_at, ng.updated_at
		FROM node_groups ng
		JOIN project_managers pm ON pm.node_group_id = ng.id
		WHERE pm.user_id = ?
		ORDER BY ng.name ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: list managed groups for user: %w", err)
	}
	defer rows.Close()
	var out []NodeGroupSummary
	for rows.Next() {
		var s NodeGroupSummary
		var role sql.NullString
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Description, &role,
			&s.NodeCount, &s.DeployedCount,
			&createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("db: scan managed groups for user: %w", err)
		}
		s.Role = role.String
		s.CreatedAt = time.Unix(createdAt, 0)
		s.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}

// IsProjectManager returns true if userID is a delegated manager for nodeGroupID.
func (db *DB) IsProjectManager(ctx context.Context, nodeGroupID, userID string) (bool, error) {
	var count int
	err := db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM project_managers
		WHERE node_group_id = ? AND user_id = ?`, nodeGroupID, userID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("db: is project manager check: %w", err)
	}
	return count > 0, nil
}

// IsProjectManagerOrPI returns true if the user is a delegated manager for the group.
// The pi_user_id column was dropped in migration 103; ownership is now expressed
// solely via project_managers. This function is retained for call-site compat.
func (db *DB) IsProjectManagerOrPI(ctx context.Context, nodeGroupID, userID string) (bool, error) {
	return db.IsProjectManager(ctx, nodeGroupID, userID)
}

// ─── Scan helpers ──────────────────────────────────────────────────────────────

func scanProjectManager(row *sql.Row) (*ProjectManager, error) {
	var pm ProjectManager
	var grantedAtUnix int64
	err := row.Scan(
		&pm.ID, &pm.NodeGroupID, &pm.NodeGroupName,
		&pm.UserID, &pm.Username,
		&pm.GrantedByUserID, &pm.GrantedByName,
		&grantedAtUnix,
	)
	if err == sql.ErrNoRows {
		return nil, ErrManagerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: scan project manager: %w", err)
	}
	pm.GrantedAt = time.Unix(grantedAtUnix, 0)
	return &pm, nil
}

func scanProjectManagers(rows *sql.Rows) ([]ProjectManager, error) {
	var out []ProjectManager
	for rows.Next() {
		var pm ProjectManager
		var grantedAtUnix int64
		if err := rows.Scan(
			&pm.ID, &pm.NodeGroupID, &pm.NodeGroupName,
			&pm.UserID, &pm.Username,
			&pm.GrantedByUserID, &pm.GrantedByName,
			&grantedAtUnix,
		); err != nil {
			return nil, fmt.Errorf("db: scan project managers: %w", err)
		}
		pm.GrantedAt = time.Unix(grantedAtUnix, 0)
		out = append(out, pm)
	}
	return out, rows.Err()
}
