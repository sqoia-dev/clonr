package db

// pending_changes.go — persistence layer for the two-stage commit queue (#154).

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PendingChange is a row in the pending_changes table.
type PendingChange struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Payload   string `json:"payload"` // raw JSON
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt int64  `json:"created_at"` // unix seconds
}

// PendingChangesInsert inserts a single pending change row.
func (db *DB) PendingChangesInsert(ctx context.Context, c PendingChange) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO pending_changes (id, kind, target, payload, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.Kind, c.Target, c.Payload, c.CreatedBy, c.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("pending_changes: insert: %w", err)
	}
	return nil
}

// PendingChangesList returns all pending changes, optionally filtered by kind.
// Pass kind="" to list all.
func (db *DB) PendingChangesList(ctx context.Context, kind string) ([]PendingChange, error) {
	var rows *sql.Rows
	var err error
	if kind != "" {
		rows, err = db.sql.QueryContext(ctx,
			`SELECT id, kind, target, payload, COALESCE(created_by,''), created_at FROM pending_changes WHERE kind = ? ORDER BY created_at ASC`,
			kind,
		)
	} else {
		rows, err = db.sql.QueryContext(ctx,
			`SELECT id, kind, target, payload, COALESCE(created_by,''), created_at FROM pending_changes ORDER BY created_at ASC`,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("pending_changes: list: %w", err)
	}
	defer rows.Close()

	var out []PendingChange
	for rows.Next() {
		var c PendingChange
		if err := rows.Scan(&c.ID, &c.Kind, &c.Target, &c.Payload, &c.CreatedBy, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("pending_changes: scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PendingChangesGet returns a single pending change by ID.
func (db *DB) PendingChangesGet(ctx context.Context, id string) (PendingChange, error) {
	var c PendingChange
	err := db.sql.QueryRowContext(ctx,
		`SELECT id, kind, target, payload, COALESCE(created_by,''), created_at FROM pending_changes WHERE id = ?`,
		id,
	).Scan(&c.ID, &c.Kind, &c.Target, &c.Payload, &c.CreatedBy, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return c, fmt.Errorf("pending_changes: id %q not found", id)
	}
	if err != nil {
		return c, fmt.Errorf("pending_changes: get %q: %w", id, err)
	}
	return c, nil
}

// PendingChangesDelete deletes one or more pending changes by ID.
// An empty ids slice is a no-op.
func (db *DB) PendingChangesDelete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if _, err := db.sql.ExecContext(ctx, `DELETE FROM pending_changes WHERE id = ?`, id); err != nil {
			return fmt.Errorf("pending_changes: delete %q: %w", id, err)
		}
	}
	return nil
}

// PendingChangesDeleteAll deletes all rows from pending_changes.
func (db *DB) PendingChangesDeleteAll(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM pending_changes`); err != nil {
		return fmt.Errorf("pending_changes: delete all: %w", err)
	}
	return nil
}

// PendingChangesCount returns the total number of pending changes.
func (db *DB) PendingChangesCount(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_changes`).Scan(&n)
	return n, err
}

// ─── Stage mode flags ─────────────────────────────────────────────────────────

// StageModeGet returns whether two-stage commit is enabled for the named surface.
// Returns false (disabled) when no row exists.
func (db *DB) StageModeGet(ctx context.Context, surface string) (bool, error) {
	var enabled int
	err := db.sql.QueryRowContext(ctx,
		`SELECT enabled FROM stage_mode_flags WHERE surface = ?`, surface,
	).Scan(&enabled)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stage_mode: get %q: %w", surface, err)
	}
	return enabled != 0, nil
}

// StageModeSet upserts the stage mode flag for the named surface.
func (db *DB) StageModeSet(ctx context.Context, surface string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO stage_mode_flags (surface, enabled, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(surface) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		surface, val, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("stage_mode: set %q: %w", surface, err)
	}
	return nil
}

// StageModeGetAll returns all surface stage-mode settings as a map.
func (db *DB) StageModeGetAll(ctx context.Context) (map[string]bool, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT surface, enabled FROM stage_mode_flags`)
	if err != nil {
		return nil, fmt.Errorf("stage_mode: get all: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var surface string
		var enabled int
		if err := rows.Scan(&surface, &enabled); err != nil {
			return nil, fmt.Errorf("stage_mode: scan: %w", err)
		}
		out[surface] = enabled != 0
	}
	return out, rows.Err()
}
