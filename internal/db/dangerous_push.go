package db

// dangerous_push.go — Sprint 41 Day 3
//
// DB helpers for the pending_dangerous_pushes staging table (migration 114).
// All methods are called by the dangerous-push handlers in
// internal/server/handlers/dangerous_push.go.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PendingDangerousPush is one row from the pending_dangerous_pushes table.
type PendingDangerousPush struct {
	ID           string
	NodeID       string
	PluginName   string
	RenderedHash string
	PayloadJSON  string
	Reason       string
	Challenge    string
	ExpiresAt    time.Time
	CreatedBy    string
	CreatedAt    time.Time
	// AttemptCount is maintained in-memory by the handler on read; the DB only
	// stores it via IncrementDangerousPushAttempts. Not persisted in the
	// initial migration — tracked as a separate column added in migration 115.
	AttemptCount int
	Consumed     bool
}

// InsertPendingDangerousPush stages a dangerous push for operator confirmation.
// The row is valid until expires_at. A new row is always inserted; if the operator
// re-triggers the same plugin push (e.g. by re-saving config), a new pending_id
// is issued and the old one may remain until it expires or is GC'd.
func (db *DB) InsertPendingDangerousPush(ctx context.Context, p PendingDangerousPush) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO pending_dangerous_pushes
			(id, node_id, plugin_name, rendered_hash, payload_json,
			 reason, challenge, expires_at, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		p.ID,
		p.NodeID,
		p.PluginName,
		p.RenderedHash,
		p.PayloadJSON,
		p.Reason,
		p.Challenge,
		p.ExpiresAt.Unix(),
		p.CreatedBy,
		p.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: insert pending dangerous push: %w", err)
	}
	return nil
}

// GetPendingDangerousPush retrieves a staged push by ID.
// Returns sql.ErrNoRows when the row does not exist.
func (db *DB) GetPendingDangerousPush(ctx context.Context, id string) (*PendingDangerousPush, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, node_id, plugin_name, rendered_hash, payload_json,
		       reason, challenge, expires_at, created_by, created_at,
		       COALESCE(attempt_count, 0), COALESCE(consumed, 0)
		FROM pending_dangerous_pushes
		WHERE id = ?
	`, id)

	var p PendingDangerousPush
	var expiresUnix, createdUnix int64
	var consumed int
	err := row.Scan(
		&p.ID,
		&p.NodeID,
		&p.PluginName,
		&p.RenderedHash,
		&p.PayloadJSON,
		&p.Reason,
		&p.Challenge,
		&expiresUnix,
		&p.CreatedBy,
		&createdUnix,
		&p.AttemptCount,
		&consumed,
	)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("db: get pending dangerous push %s: %w", id, err)
	}
	p.ExpiresAt = time.Unix(expiresUnix, 0).UTC()
	p.CreatedAt = time.Unix(createdUnix, 0).UTC()
	p.Consumed = consumed != 0
	return &p, nil
}

// IncrementDangerousPushAttempts increments the attempt counter for a pending push
// and returns the new count. Used to enforce the 3-strike lockout rule.
// If the new count reaches maxAttempts, the row is also marked consumed.
func (db *DB) IncrementDangerousPushAttempts(ctx context.Context, id string, maxAttempts int) (int, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("db: begin tx for increment attempts: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx,
		`UPDATE pending_dangerous_pushes SET attempt_count = COALESCE(attempt_count, 0) + 1 WHERE id = ?`,
		id,
	)
	if err != nil {
		return 0, fmt.Errorf("db: increment attempt count for %s: %w", id, err)
	}

	var newCount int
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(attempt_count, 0) FROM pending_dangerous_pushes WHERE id = ?`,
		id,
	).Scan(&newCount)
	if err != nil {
		return 0, fmt.Errorf("db: read attempt count for %s: %w", id, err)
	}

	if newCount >= maxAttempts {
		_, err = tx.ExecContext(ctx,
			`UPDATE pending_dangerous_pushes SET consumed = 1 WHERE id = ?`,
			id,
		)
		if err != nil {
			return 0, fmt.Errorf("db: consume after max attempts for %s: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("db: commit attempt increment for %s: %w", id, err)
	}
	return newCount, nil
}

// ConsumePendingDangerousPush marks a pending push as consumed so it cannot be
// confirmed again. Called on successful confirmation or after 3-strike lockout.
func (db *DB) ConsumePendingDangerousPush(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE pending_dangerous_pushes SET consumed = 1 WHERE id = ?`,
		id,
	)
	if err != nil {
		return fmt.Errorf("db: consume pending dangerous push %s: %w", id, err)
	}
	return nil
}

// PurgeDangerousPushes deletes consumed or expired rows older than the cutoff.
// Called by the audit-log purger to prevent unbounded table growth.
func (db *DB) PurgeDangerousPushes(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx, `
		DELETE FROM pending_dangerous_pushes
		WHERE consumed = 1
		   OR expires_at < ?
	`, olderThan.Unix())
	if err != nil {
		return 0, fmt.Errorf("db: purge dangerous pushes: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
