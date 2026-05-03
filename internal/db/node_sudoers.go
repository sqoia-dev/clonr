package db

import (
	"context"
	"time"
)

// NodeSudoer is a single per-node sudoer assignment.
type NodeSudoer struct {
	NodeID         string    `json:"node_id"`
	UserIdentifier string    `json:"user_identifier"`
	Source         string    `json:"source"`   // "ldap" | "local"
	Commands       string    `json:"commands"` // e.g. "ALL"
	AssignedAt     time.Time `json:"assigned_at"`
	AssignedBy     string    `json:"assigned_by"`
}

// NodeSudoersListByNode returns all sudoer assignments for a node, ordered by assigned_at.
func (db *DB) NodeSudoersListByNode(ctx context.Context, nodeID string) ([]NodeSudoer, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT node_id, user_identifier, source, commands, assigned_at, assigned_by
		FROM node_sudoers
		WHERE node_id = ?
		ORDER BY assigned_at ASC
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NodeSudoer
	for rows.Next() {
		var s NodeSudoer
		var assignedAtUnix int64
		if err := rows.Scan(&s.NodeID, &s.UserIdentifier, &s.Source, &s.Commands, &assignedAtUnix, &s.AssignedBy); err != nil {
			return nil, err
		}
		s.AssignedAt = time.Unix(assignedAtUnix, 0).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// NodeSudoersAdd inserts or replaces a sudoer assignment for a node.
func (db *DB) NodeSudoersAdd(ctx context.Context, s NodeSudoer) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO node_sudoers (node_id, user_identifier, source, commands, assigned_at, assigned_by)
		VALUES (?, ?, ?, ?, strftime('%s','now'), ?)
		ON CONFLICT(node_id, user_identifier) DO UPDATE SET
			source      = excluded.source,
			commands    = excluded.commands,
			assigned_at = excluded.assigned_at,
			assigned_by = excluded.assigned_by
	`, s.NodeID, s.UserIdentifier, s.Source, s.Commands, s.AssignedBy)
	return err
}

// NodeSudoersRemove deletes a sudoer assignment for a node.
func (db *DB) NodeSudoersRemove(ctx context.Context, nodeID, userIdentifier string) error {
	_, err := db.sql.ExecContext(ctx, `
		DELETE FROM node_sudoers WHERE node_id = ? AND user_identifier = ?
	`, nodeID, userIdentifier)
	return err
}
