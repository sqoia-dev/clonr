package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── boot_entries CRUD (#160) ─────────────────────────────────────────────────

// CreateBootEntry inserts a new BootEntry row.
func (db *DB) CreateBootEntry(ctx context.Context, e api.BootEntry) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO boot_entries (id, name, kind, kernel_url, initrd_url, cmdline, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, e.ID, e.Name, e.Kind, e.KernelURL,
		nullString(e.InitrdURL), nullString(e.Cmdline),
		boolToInt(e.Enabled),
		e.CreatedAt.Unix(), e.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: create boot entry: %w", err)
	}
	return nil
}

// GetBootEntry returns a BootEntry by ID. Returns api.ErrNotFound when absent.
func (db *DB) GetBootEntry(ctx context.Context, id string) (api.BootEntry, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, kind, kernel_url, initrd_url, cmdline, enabled, created_at, updated_at
		FROM boot_entries WHERE id = ?
	`, id)
	return scanBootEntry(row)
}

// ListBootEntries returns all BootEntry rows ordered by name.
// When enabledOnly is true, only enabled=1 rows are returned.
func (db *DB) ListBootEntries(ctx context.Context, enabledOnly bool) ([]api.BootEntry, error) {
	query := `SELECT id, name, kind, kernel_url, initrd_url, cmdline, enabled, created_at, updated_at
		FROM boot_entries`
	if enabledOnly {
		query += " WHERE enabled = 1"
	}
	query += " ORDER BY name ASC"

	rows, err := db.sql.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("db: list boot entries: %w", err)
	}
	defer rows.Close()

	var entries []api.BootEntry
	for rows.Next() {
		e, err := scanBootEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// UpdateBootEntry replaces all mutable fields of an existing BootEntry.
func (db *DB) UpdateBootEntry(ctx context.Context, e api.BootEntry) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE boot_entries
		SET name = ?, kind = ?, kernel_url = ?, initrd_url = ?, cmdline = ?, enabled = ?, updated_at = ?
		WHERE id = ?
	`, e.Name, e.Kind, e.KernelURL,
		nullString(e.InitrdURL), nullString(e.Cmdline),
		boolToInt(e.Enabled),
		time.Now().Unix(),
		e.ID,
	)
	if err != nil {
		return fmt.Errorf("db: update boot entry: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// DeleteBootEntry removes a BootEntry by ID.
func (db *DB) DeleteBootEntry(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM boot_entries WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete boot entry: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// ─── scan helper ─────────────────────────────────────────────────────────────

type bootEntryScanner interface {
	Scan(dest ...any) error
}

func scanBootEntry(s bootEntryScanner) (api.BootEntry, error) {
	var (
		e             api.BootEntry
		initrdNull    sql.NullString
		cmdlineNull   sql.NullString
		enabledInt    int
		createdAtUnix int64
		updatedAtUnix int64
	)
	err := s.Scan(
		&e.ID, &e.Name, &e.Kind, &e.KernelURL,
		&initrdNull, &cmdlineNull,
		&enabledInt, &createdAtUnix, &updatedAtUnix,
	)
	if err == sql.ErrNoRows {
		return api.BootEntry{}, api.ErrNotFound
	}
	if err != nil {
		return api.BootEntry{}, fmt.Errorf("db: scan boot entry: %w", err)
	}
	if initrdNull.Valid {
		e.InitrdURL = initrdNull.String
	}
	if cmdlineNull.Valid {
		e.Cmdline = cmdlineNull.String
	}
	e.Enabled = enabledInt != 0
	e.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	e.UpdatedAt = time.Unix(updatedAtUnix, 0).UTC()
	return e, nil
}
