// pi.go — Minimal node group summary helpers retained after PI-CODE-WIPE (Sprint 43-prime Day 2).
//
// The PI workflow (pi_member_requests, pi_expansion_requests, node_groups.pi_user_id,
// portal_config.pi_auto_approve) was wiped in Sprint 36 and the tables dropped in
// migration 119. This file retains only the NodeGroupSummary type and its associated
// query helpers that are still consumed by live portal handlers (grants, publications,
// managers, allocation change requests, FOS, attribute visibility, auto-policy,
// review cycles).
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrRequestNotFound is returned when a queried row does not exist.
var ErrRequestNotFound = fmt.Errorf("db: request not found")

// NodeGroupSummary is a lightweight view of a NodeGroup for portal use.
// The PIUserID and PIUsername fields are always empty after migration 103
// dropped node_groups.pi_user_id — retained for struct compat with callers.
type NodeGroupSummary struct {
	ID            string
	Name          string
	Description   string
	Role          string
	NodeCount     int
	DeployedCount int
	PIUserID      string // always "" post-migration-103
	PIUsername    string // always "" post-migration-103
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// GetNodeGroupSummary returns a NodeGroupSummary for a single group.
// Does not query the dropped pi_user_id column.
func (db *DB) GetNodeGroupSummary(ctx context.Context, groupID string) (NodeGroupSummary, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT ng.id, ng.name, ng.description, ng.role,
		       (SELECT COUNT(*) FROM node_group_memberships m WHERE m.group_id = ng.id) AS node_count,
		       (SELECT COUNT(*) FROM node_configs nc
		         LEFT JOIN node_group_memberships m2 ON m2.node_id = nc.id AND m2.is_primary = 1
		         WHERE m2.group_id = ng.id AND nc.deploy_completed_preboot_at IS NOT NULL) AS deployed_count,
		       ng.created_at, ng.updated_at
		FROM node_groups ng
		WHERE ng.id = ?`,
		groupID,
	)
	var s NodeGroupSummary
	var role sql.NullString
	var createdAt, updatedAt int64
	err := row.Scan(
		&s.ID, &s.Name, &s.Description, &role,
		&s.NodeCount, &s.DeployedCount,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeGroupSummary{}, ErrRequestNotFound
	}
	if err != nil {
		return NodeGroupSummary{}, fmt.Errorf("db: get node group summary: %w", err)
	}
	s.Role = role.String
	s.CreatedAt = time.Unix(createdAt, 0)
	s.UpdatedAt = time.Unix(updatedAt, 0)
	return s, nil
}

// ListAllNodeGroupSummaries returns all NodeGroups with utilization summary columns.
// Used by admin users viewing the portal group list. Does not query pi_user_id.
func (db *DB) ListAllNodeGroupSummaries(ctx context.Context) ([]NodeGroupSummary, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT ng.id, ng.name, ng.description, ng.role,
		       (SELECT COUNT(*) FROM node_group_memberships m WHERE m.group_id = ng.id) AS node_count,
		       (SELECT COUNT(*) FROM node_configs nc
		         LEFT JOIN node_group_memberships m2 ON m2.node_id = nc.id AND m2.is_primary = 1
		         WHERE m2.group_id = ng.id AND nc.deploy_completed_preboot_at IS NOT NULL) AS deployed_count,
		       ng.created_at, ng.updated_at
		FROM node_groups ng
		ORDER BY ng.name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: list all node group summaries: %w", err)
	}
	defer rows.Close()
	return scanNodeGroupSummaries(rows)
}

// ListNodeGroupsByPI returns all NodeGroups where userID has a project_manager row.
// The old pi_user_id column is gone; ownership is now expressed via project_managers.
func (db *DB) ListNodeGroupsByPI(ctx context.Context, piUserID string) ([]NodeGroupSummary, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT ng.id, ng.name, ng.description, ng.role,
		       (SELECT COUNT(*) FROM node_group_memberships m WHERE m.group_id = ng.id) AS node_count,
		       (SELECT COUNT(*) FROM node_configs nc
		         LEFT JOIN node_group_memberships m2 ON m2.node_id = nc.id AND m2.is_primary = 1
		         WHERE m2.group_id = ng.id AND nc.deploy_completed_preboot_at IS NOT NULL) AS deployed_count,
		       ng.created_at, ng.updated_at
		FROM node_groups ng
		JOIN project_managers pm ON pm.node_group_id = ng.id AND pm.user_id = ?
		ORDER BY ng.name ASC`,
		piUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: list node groups by pi: %w", err)
	}
	defer rows.Close()
	return scanNodeGroupSummaries(rows)
}

// IsNodeGroupOwnedByPI returns true if userID has a project_manager row for the group.
// The pi_user_id column was dropped in migration 103; ownership now lives in project_managers.
func (db *DB) IsNodeGroupOwnedByPI(ctx context.Context, groupID, piUserID string) (bool, error) {
	var count int
	err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_managers WHERE node_group_id = ? AND user_id = ?`,
		groupID, piUserID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("db: check pi group ownership: %w", err)
	}
	return count > 0, nil
}

func scanNodeGroupSummaries(rows *sql.Rows) ([]NodeGroupSummary, error) {
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
			return nil, fmt.Errorf("db: scan node group summary: %w", err)
		}
		s.Role = role.String
		s.CreatedAt = time.Unix(createdAt, 0)
		s.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}
