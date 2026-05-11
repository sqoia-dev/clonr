package db

// plugin_backups.go — Sprint 41 Day 4
//
// DB helpers for the plugin_backups table (migration 116).
// Rows are created by the server when a node acknowledges that
// clustr-privhelper backup-write succeeded, and pruned to MaxBackups
// per (node, plugin) after each successful render.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PluginBackup is one row from the plugin_backups table.
type PluginBackup struct {
	ID                     string
	NodeID                 string
	PluginName             string
	BlobPath               string    // absolute path to the tarball on the node
	TakenAt                time.Time
	PendingDangerousPushID string // empty when not tied to a dangerous push
}

// InsertPluginBackup records a new plugin snapshot.
func (db *DB) InsertPluginBackup(ctx context.Context, b PluginBackup) error {
	var pendingID interface{} // NULL when empty
	if b.PendingDangerousPushID != "" {
		pendingID = b.PendingDangerousPushID
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO plugin_backups
			(id, node_id, plugin_name, blob_path, taken_at, pending_dangerous_push_id)
		VALUES (?, ?, ?, ?, ?, ?)
	`,
		b.ID,
		b.NodeID,
		b.PluginName,
		b.BlobPath,
		b.TakenAt.Unix(),
		pendingID,
	)
	if err != nil {
		return fmt.Errorf("db: insert plugin backup: %w", err)
	}
	return nil
}

// GetPluginBackup retrieves a single backup by ID.
// Returns sql.ErrNoRows when the row does not exist.
func (db *DB) GetPluginBackup(ctx context.Context, id string) (*PluginBackup, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, node_id, plugin_name, blob_path, taken_at,
		       COALESCE(pending_dangerous_push_id, '')
		FROM plugin_backups
		WHERE id = ?
	`, id)

	var b PluginBackup
	var takenUnix int64
	err := row.Scan(
		&b.ID,
		&b.NodeID,
		&b.PluginName,
		&b.BlobPath,
		&takenUnix,
		&b.PendingDangerousPushID,
	)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("db: get plugin backup %s: %w", id, err)
	}
	b.TakenAt = time.Unix(takenUnix, 0).UTC()
	return &b, nil
}

// GetPluginBackupByPendingPush returns the backup tied to a confirmed dangerous
// push ID, or sql.ErrNoRows if none exists. Used by
//
//	clustr restore replay --pending-id <X>
func (db *DB) GetPluginBackupByPendingPush(ctx context.Context, pendingPushID string) (*PluginBackup, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, node_id, plugin_name, blob_path, taken_at,
		       COALESCE(pending_dangerous_push_id, '')
		FROM plugin_backups
		WHERE pending_dangerous_push_id = ?
		ORDER BY taken_at DESC
		LIMIT 1
	`, pendingPushID)

	var b PluginBackup
	var takenUnix int64
	err := row.Scan(
		&b.ID,
		&b.NodeID,
		&b.PluginName,
		&b.BlobPath,
		&takenUnix,
		&b.PendingDangerousPushID,
	)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("db: get plugin backup by pending push %s: %w", pendingPushID, err)
	}
	b.TakenAt = time.Unix(takenUnix, 0).UTC()
	return &b, nil
}

// ListPluginBackups returns backups for a (node, plugin) pair sorted newest first.
// Pass empty strings to omit filtering by node_id or plugin_name.
func (db *DB) ListPluginBackups(ctx context.Context, nodeID, pluginName string) ([]PluginBackup, error) {
	query := `
		SELECT id, node_id, plugin_name, blob_path, taken_at,
		       COALESCE(pending_dangerous_push_id, '')
		FROM plugin_backups
		WHERE 1=1`
	args := []interface{}{}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}
	if pluginName != "" {
		query += " AND plugin_name = ?"
		args = append(args, pluginName)
	}
	query += " ORDER BY taken_at DESC"

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: list plugin backups: %w", err)
	}
	defer rows.Close()

	var out []PluginBackup
	for rows.Next() {
		var b PluginBackup
		var takenUnix int64
		if err := rows.Scan(
			&b.ID,
			&b.NodeID,
			&b.PluginName,
			&b.BlobPath,
			&takenUnix,
			&b.PendingDangerousPushID,
		); err != nil {
			return nil, fmt.Errorf("db: list plugin backups scan: %w", err)
		}
		b.TakenAt = time.Unix(takenUnix, 0).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// PrunePluginBackups deletes the oldest backups for a (node, plugin) pair
// beyond maxBackups, returning the IDs (and blob paths) of deleted rows so
// the caller can remove the corresponding tarballs.
//
// The server calls this after each successful render to enforce BackupSpec.MaxBackups.
func (db *DB) PrunePluginBackups(ctx context.Context, nodeID, pluginName string, maxBackups int) ([]PluginBackup, error) {
	if maxBackups <= 0 {
		maxBackups = 3
	}

	// Find the IDs and paths of rows beyond maxBackups (oldest first).
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, node_id, plugin_name, blob_path, taken_at,
		       COALESCE(pending_dangerous_push_id, '')
		FROM plugin_backups
		WHERE node_id = ? AND plugin_name = ?
		ORDER BY taken_at DESC
		LIMIT -1 OFFSET ?
	`, nodeID, pluginName, maxBackups)
	if err != nil {
		return nil, fmt.Errorf("db: prune plugin backups query: %w", err)
	}
	defer rows.Close()

	var pruned []PluginBackup
	for rows.Next() {
		var b PluginBackup
		var takenUnix int64
		if err := rows.Scan(
			&b.ID,
			&b.NodeID,
			&b.PluginName,
			&b.BlobPath,
			&takenUnix,
			&b.PendingDangerousPushID,
		); err != nil {
			return nil, fmt.Errorf("db: prune plugin backups scan: %w", err)
		}
		b.TakenAt = time.Unix(takenUnix, 0).UTC()
		pruned = append(pruned, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: prune plugin backups rows: %w", err)
	}
	rows.Close()

	if len(pruned) == 0 {
		return nil, nil
	}

	// Delete them by ID.
	ids := make([]interface{}, len(pruned))
	placeholders := make([]string, len(pruned))
	for i, b := range pruned {
		ids[i] = b.ID
		placeholders[i] = "?"
	}
	delSQL := "DELETE FROM plugin_backups WHERE id IN (" + joinStrings(placeholders, ",") + ")"
	if _, err := db.sql.ExecContext(ctx, delSQL, ids...); err != nil {
		return nil, fmt.Errorf("db: prune plugin backups delete: %w", err)
	}

	return pruned, nil
}

// DeletePluginBackup removes a single backup row by ID.
// Returns sql.ErrNoRows if the row does not exist.
func (db *DB) DeletePluginBackup(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM plugin_backups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete plugin backup %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: delete plugin backup rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// joinStrings joins elements with sep. Used to build SQL placeholders.
func joinStrings(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}
