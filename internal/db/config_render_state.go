// Package db — config_render_state.go implements the persistence layer for
// the reactive config diff engine (Sprint 36 Bundle A).
//
// THREAD-SAFETY: All methods are safe for concurrent use. The underlying
// sql.DB (WAL mode, MaxOpenConns=1) serialises writers.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ConfigRenderState is a row from the config_render_state table.
// It records the last successfully-rendered hash for a (node, plugin) pair
// so the diff engine can skip re-pushes when Render output is unchanged.
type ConfigRenderState struct {
	// NodeID is the cluster node UUID.
	NodeID string
	// PluginName is the stable plugin identifier (Plugin.Name()).
	PluginName string
	// RenderedHash is the SHA-256 hex digest of the last canonical render output.
	RenderedHash string
	// RenderedAt is when Render was last called for this row.
	RenderedAt time.Time
	// PushedAt is when the last push was acked by clientd. Zero if never pushed.
	PushedAt time.Time
	// PushAttempts is the number of push attempts made for the current hash.
	PushAttempts int
	// LastError is the last Render or push error. Empty string means success.
	LastError string
}

// GetRenderHash returns the stored rendered_hash for (nodeID, pluginName).
// Returns ("", nil) when no row exists — the diff engine treats a missing row
// as a guaranteed hash mismatch (first-push-wins semantics).
func (db *DB) GetRenderHash(ctx context.Context, nodeID, pluginName string) (string, error) {
	var hash string
	err := db.sql.QueryRowContext(ctx,
		`SELECT rendered_hash FROM config_render_state WHERE node_id = ? AND plugin_name = ?`,
		nodeID, pluginName,
	).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db: GetRenderHash: %w", err)
	}
	return hash, nil
}

// GetRenderState returns the full ConfigRenderState row for (nodeID, pluginName).
// Returns (nil, nil) when no row exists.
func (db *DB) GetRenderState(ctx context.Context, nodeID, pluginName string) (*ConfigRenderState, error) {
	var (
		hash         string
		renderedAt   sql.NullInt64
		pushedAt     sql.NullInt64
		pushAttempts int
		lastError    sql.NullString
	)
	err := db.sql.QueryRowContext(ctx, `
		SELECT rendered_hash, rendered_at, pushed_at, push_attempts, last_error
		FROM config_render_state
		WHERE node_id = ? AND plugin_name = ?
	`, nodeID, pluginName).Scan(&hash, &renderedAt, &pushedAt, &pushAttempts, &lastError)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: GetRenderState: %w", err)
	}
	row := &ConfigRenderState{
		NodeID:       nodeID,
		PluginName:   pluginName,
		RenderedHash: hash,
		PushAttempts: pushAttempts,
	}
	if renderedAt.Valid {
		row.RenderedAt = time.Unix(renderedAt.Int64, 0).UTC()
	}
	if pushedAt.Valid {
		row.PushedAt = time.Unix(pushedAt.Int64, 0).UTC()
	}
	if lastError.Valid {
		row.LastError = lastError.String
	}
	return row, nil
}

// UpsertRenderHash upserts the rendered_hash for (nodeID, pluginName) and
// records renderedAt as the render timestamp. pushed_at is set to pushedAt
// when non-zero, otherwise it is left NULL (not-yet-pushed).
// push_attempts is incremented on every call; last_error is cleared on success.
//
// The upsert is idempotent: calling it twice with the same hash produces a
// single row, not two.
func (db *DB) UpsertRenderHash(ctx context.Context, nodeID, pluginName, hash string, renderedAt time.Time, pushedAt time.Time) error {
	var pushedAtVal sql.NullInt64
	if !pushedAt.IsZero() {
		pushedAtVal = sql.NullInt64{Int64: pushedAt.Unix(), Valid: true}
	}

	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO config_render_state
			(node_id, plugin_name, rendered_hash, rendered_at, pushed_at, push_attempts, last_error)
		VALUES (?, ?, ?, ?, ?, 1, NULL)
		ON CONFLICT (node_id, plugin_name) DO UPDATE SET
			rendered_hash  = excluded.rendered_hash,
			rendered_at    = excluded.rendered_at,
			pushed_at      = CASE WHEN excluded.pushed_at IS NOT NULL THEN excluded.pushed_at ELSE pushed_at END,
			push_attempts  = push_attempts + 1,
			last_error     = NULL
	`, nodeID, pluginName, hash, renderedAt.Unix(), pushedAtVal)
	if err != nil {
		return fmt.Errorf("db: UpsertRenderHash: %w", err)
	}
	return nil
}

// SetRenderError records a render or push failure for (nodeID, pluginName)
// and increments push_attempts. The rendered_hash is preserved from the
// last successful render (or empty string if the row is new).
func (db *DB) SetRenderError(ctx context.Context, nodeID, pluginName, errMsg string) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO config_render_state
			(node_id, plugin_name, rendered_hash, rendered_at, push_attempts, last_error)
		VALUES (?, ?, '', ?, 1, ?)
		ON CONFLICT (node_id, plugin_name) DO UPDATE SET
			rendered_at   = ?,
			push_attempts = push_attempts + 1,
			last_error    = excluded.last_error
	`, nodeID, pluginName, now, errMsg, now)
	if err != nil {
		return fmt.Errorf("db: SetRenderError: %w", err)
	}
	return nil
}

// DeleteForNode removes all config_render_state rows for nodeID. This is a
// cascade-safety net — the FK ON DELETE CASCADE handles it automatically
// when a node is deleted, but callers may invoke this explicitly when they
// need to force a full re-render for a node without deleting the node itself.
func (db *DB) DeleteForNode(ctx context.Context, nodeID string) error {
	_, err := db.sql.ExecContext(ctx,
		`DELETE FROM config_render_state WHERE node_id = ?`, nodeID)
	if err != nil {
		return fmt.Errorf("db: DeleteForNode (config_render_state): %w", err)
	}
	return nil
}
