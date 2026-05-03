// node_group_restrictions.go — DB operations for G2 per-NodeGroup LDAP group
// access restrictions (Sprint G / CF-40).
//
// allowed_ldap_groups is a JSON array of LDAP group CNs (or DNs) stored as a
// TEXT column on node_groups. Default '[]' = open access (no restriction).
package db

import (
	"context"
	"encoding/json"
	"fmt"
)

// GetNodeGroupAllowedLDAPGroups returns the allowed LDAP group list for a NodeGroup.
// Returns an empty slice when the list is unset or empty (= no restriction).
func (db *DB) GetNodeGroupAllowedLDAPGroups(ctx context.Context, groupID string) ([]string, error) {
	var raw string
	err := db.sql.QueryRowContext(ctx,
		`SELECT allowed_ldap_groups FROM node_groups WHERE id = ?`, groupID,
	).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("db: get allowed ldap groups for %s: %w", groupID, err)
	}
	var groups []string
	if err := json.Unmarshal([]byte(raw), &groups); err != nil {
		return []string{}, nil
	}
	if groups == nil {
		groups = []string{}
	}
	return groups, nil
}

// SetNodeGroupAllowedLDAPGroups sets the allowed LDAP group list for a NodeGroup.
// Pass an empty slice to clear the restriction (open access).
func (db *DB) SetNodeGroupAllowedLDAPGroups(ctx context.Context, groupID string, groups []string) error {
	if groups == nil {
		groups = []string{}
	}
	raw, err := json.Marshal(groups)
	if err != nil {
		return fmt.Errorf("db: marshal allowed ldap groups: %w", err)
	}
	res, err := db.sql.ExecContext(ctx, `
		UPDATE node_groups SET allowed_ldap_groups = ? WHERE id = ?`,
		string(raw), groupID,
	)
	if err != nil {
		return fmt.Errorf("db: set allowed ldap groups: %w", err)
	}
	return requireOneRow(res, "node_groups", groupID)
}

// NodeGroupNameRow is a minimal row used by the Slurm render path to resolve IDs → names.
type NodeGroupNameRow struct {
	ID   string
	Name string
}

// ListNodeGroupsForRender returns all NodeGroup IDs and names.
// Used by the Slurm render path to build the restriction map.
func (db *DB) ListNodeGroupsForRender(ctx context.Context) ([]NodeGroupNameRow, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id, name FROM node_groups ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("db: list node groups for render: %w", err)
	}
	defer rows.Close()
	var out []NodeGroupNameRow
	for rows.Next() {
		var r NodeGroupNameRow
		if err := rows.Scan(&r.ID, &r.Name); err != nil {
			return nil, fmt.Errorf("db: scan node group for render: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListNodeGroupsWithRestrictions returns all node groups that have non-empty
// allowed_ldap_groups restrictions. Used by the Slurm renderer to know which
// partitions need AllowGroups= emitted.
func (db *DB) ListNodeGroupsWithRestrictions(ctx context.Context) (map[string][]string, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, allowed_ldap_groups FROM node_groups
		WHERE allowed_ldap_groups != '[]' AND allowed_ldap_groups != ''`)
	if err != nil {
		return nil, fmt.Errorf("db: list node groups with restrictions: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]string)
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("db: scan node group restrictions: %w", err)
		}
		var groups []string
		if err := json.Unmarshal([]byte(raw), &groups); err != nil {
			continue
		}
		if len(groups) > 0 {
			result[id] = groups
		}
	}
	return result, rows.Err()
}
