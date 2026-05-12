package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Notice is one row in the notices table.
type Notice struct {
	ID          int64
	Body        string
	Severity    string // "info" | "warning" | "critical"
	CreatedBy   string
	CreatedAt   time.Time
	ExpiresAt   *time.Time // nil = never expires
	DismissedAt *time.Time // nil = not dismissed
}

// CreateNoticeParams holds the fields for inserting a new notice.
type CreateNoticeParams struct {
	Body      string
	Severity  string
	CreatedBy string
	ExpiresAt *time.Time
}

// InsertNotice creates a new notice row and returns the fully-populated Notice.
func (db *DB) InsertNotice(ctx context.Context, p CreateNoticeParams) (Notice, error) {
	now := time.Now().UTC()
	var expiresUnix sql.NullInt64
	if p.ExpiresAt != nil {
		expiresUnix = sql.NullInt64{Int64: p.ExpiresAt.Unix(), Valid: true}
	}

	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO notices (body, severity, created_by, created_at, expires_at, dismissed_at)
		VALUES (?, ?, ?, ?, ?, NULL)
	`, p.Body, p.Severity, p.CreatedBy, now.Unix(), expiresUnix)
	if err != nil {
		return Notice{}, fmt.Errorf("db: insert notice: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Notice{}, fmt.Errorf("db: insert notice last id: %w", err)
	}

	return Notice{
		ID:        id,
		Body:      p.Body,
		Severity:  p.Severity,
		CreatedBy: p.CreatedBy,
		CreatedAt: now,
		ExpiresAt: p.ExpiresAt,
	}, nil
}

// GetActiveNotice returns the single most-visible active notice: the highest
// severity, then most recently created. Returns nil, nil when no active notice
// exists. A notice is inactive when dismissed_at IS NOT NULL or when
// expires_at IS NOT NULL AND expires_at <= now.
func (db *DB) GetActiveNotice(ctx context.Context) (*Notice, error) {
	now := time.Now().Unix()

	row := db.sql.QueryRowContext(ctx, `
		SELECT id, body, severity, created_by, created_at, expires_at, dismissed_at
		FROM notices
		WHERE dismissed_at IS NULL
		  AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY
		  CASE severity
		    WHEN 'critical' THEN 0
		    WHEN 'warning'  THEN 1
		    WHEN 'info'     THEN 2
		    ELSE 3
		  END ASC,
		  created_at DESC
		LIMIT 1
	`, now)

	return scanNotice(row)
}

// DismissNotice sets dismissed_at to now for the given notice ID.
// Returns sql.ErrNoRows when the notice does not exist.
func (db *DB) DismissNotice(ctx context.Context, id int64) error {
	now := time.Now().UTC().Unix()
	res, err := db.sql.ExecContext(ctx, `
		UPDATE notices SET dismissed_at = ? WHERE id = ?
	`, now, id)
	if err != nil {
		return fmt.Errorf("db: dismiss notice: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: dismiss notice rows: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

type noticeRowScanner interface {
	Scan(dest ...any) error
}

func scanNotice(row noticeRowScanner) (*Notice, error) {
	var n Notice
	var createdAt int64
	var expiresAt sql.NullInt64
	var dismissedAt sql.NullInt64

	err := row.Scan(
		&n.ID, &n.Body, &n.Severity, &n.CreatedBy,
		&createdAt, &expiresAt, &dismissedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: scan notice: %w", err)
	}

	n.CreatedAt = time.Unix(createdAt, 0).UTC()
	if expiresAt.Valid {
		t := time.Unix(expiresAt.Int64, 0).UTC()
		n.ExpiresAt = &t
	}
	if dismissedAt.Valid {
		t := time.Unix(dismissedAt.Int64, 0).UTC()
		n.DismissedAt = &t
	}
	return &n, nil
}
