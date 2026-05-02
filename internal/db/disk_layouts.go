package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── disk_layouts CRUD (#146) ─────────────────────────────────────────────────

// CreateDiskLayout inserts a new StoredDiskLayout row.
func (db *DB) CreateDiskLayout(ctx context.Context, dl api.StoredDiskLayout) error {
	layoutJSON, err := json.Marshal(dl.Layout)
	if err != nil {
		return fmt.Errorf("db: marshal disk layout: %w", err)
	}
	sourceNodeID := sql.NullString{String: dl.SourceNodeID, Valid: dl.SourceNodeID != ""}
	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO disk_layouts (id, name, source_node_id, captured_at, layout_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, dl.ID, dl.Name, sourceNodeID, dl.CapturedAt.Unix(), string(layoutJSON),
		dl.CreatedAt.Unix(), dl.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("db: create disk layout: %w", err)
	}
	return nil
}

// GetDiskLayout returns a StoredDiskLayout by ID. Returns api.ErrNotFound when absent.
func (db *DB) GetDiskLayout(ctx context.Context, id string) (api.StoredDiskLayout, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, source_node_id, captured_at, layout_json, created_at, updated_at
		FROM disk_layouts WHERE id = ?
	`, id)
	return scanDiskLayout(row)
}

// GetDiskLayoutByName returns a StoredDiskLayout by name. Returns api.ErrNotFound when absent.
func (db *DB) GetDiskLayoutByName(ctx context.Context, name string) (api.StoredDiskLayout, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, source_node_id, captured_at, layout_json, created_at, updated_at
		FROM disk_layouts WHERE name = ?
	`, name)
	return scanDiskLayout(row)
}

// ListDiskLayouts returns all StoredDiskLayout rows, ordered by name.
func (db *DB) ListDiskLayouts(ctx context.Context) ([]api.StoredDiskLayout, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, source_node_id, captured_at, layout_json, created_at, updated_at
		FROM disk_layouts ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list disk layouts: %w", err)
	}
	defer rows.Close()

	var layouts []api.StoredDiskLayout
	for rows.Next() {
		dl, err := scanDiskLayout(rows)
		if err != nil {
			return nil, err
		}
		layouts = append(layouts, dl)
	}
	return layouts, rows.Err()
}

// UpdateDiskLayoutFields updates the name and/or layout_json of an existing record.
// Both fields are replaced unconditionally; updated_at is refreshed.
func (db *DB) UpdateDiskLayoutFields(ctx context.Context, id, name string, layout api.DiskLayout) error {
	layoutJSON, err := json.Marshal(layout)
	if err != nil {
		return fmt.Errorf("db: marshal disk layout: %w", err)
	}
	res, err := db.sql.ExecContext(ctx, `
		UPDATE disk_layouts SET name = ?, layout_json = ?, updated_at = ? WHERE id = ?
	`, name, string(layoutJSON), time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("db: update disk layout: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// DeleteDiskLayout removes a disk layout by ID.
// The caller is responsible for checking FK references before calling this.
func (db *DB) DeleteDiskLayout(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM disk_layouts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete disk layout: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// DiskLayoutRefCount returns the number of node_groups and node_configs rows
// that reference the given disk_layout_id.  Used to enforce the 409 guard on
// DELETE /api/v1/disk-layouts/{id}.
func (db *DB) DiskLayoutRefCount(ctx context.Context, id string) (int, error) {
	var groupCount, nodeCount int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_groups WHERE disk_layout_id = ?`, id,
	).Scan(&groupCount); err != nil {
		return 0, fmt.Errorf("db: count group refs for disk layout: %w", err)
	}
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_configs WHERE disk_layout_id = ?`, id,
	).Scan(&nodeCount); err != nil {
		return 0, fmt.Errorf("db: count node refs for disk layout: %w", err)
	}
	return groupCount + nodeCount, nil
}

// GetNodeDiskLayoutID returns the disk_layout_id FK value for a node_config row.
// Returns ("", nil) when the column is NULL (no explicit layout assigned).
func (db *DB) GetNodeDiskLayoutID(ctx context.Context, nodeID string) (string, error) {
	var v sql.NullString
	err := db.sql.QueryRowContext(ctx,
		`SELECT disk_layout_id FROM node_configs WHERE id = ?`, nodeID,
	).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db: get node disk_layout_id: %w", err)
	}
	return v.String, nil
}

// GetGroupDiskLayoutID returns the disk_layout_id FK value for a node_groups row.
// Returns ("", nil) when the column is NULL.
func (db *DB) GetGroupDiskLayoutID(ctx context.Context, groupID string) (string, error) {
	var v sql.NullString
	err := db.sql.QueryRowContext(ctx,
		`SELECT disk_layout_id FROM node_groups WHERE id = ?`, groupID,
	).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db: get group disk_layout_id: %w", err)
	}
	return v.String, nil
}

// SetNodeDiskLayoutID sets (or clears when layoutID=="") the disk_layout_id FK
// on a node_configs row.
func (db *DB) SetNodeDiskLayoutID(ctx context.Context, nodeID, layoutID string) error {
	var v interface{}
	if layoutID != "" {
		v = layoutID
	}
	_, err := db.sql.ExecContext(ctx,
		`UPDATE node_configs SET disk_layout_id = ?, updated_at = ? WHERE id = ?`,
		v, time.Now().Unix(), nodeID,
	)
	if err != nil {
		return fmt.Errorf("db: set node disk_layout_id: %w", err)
	}
	return nil
}

// SetGroupDiskLayoutID sets (or clears when layoutID=="") the disk_layout_id FK
// on a node_groups row.
func (db *DB) SetGroupDiskLayoutID(ctx context.Context, groupID, layoutID string) error {
	var v interface{}
	if layoutID != "" {
		v = layoutID
	}
	_, err := db.sql.ExecContext(ctx,
		`UPDATE node_groups SET disk_layout_id = ?, updated_at = ? WHERE id = ?`,
		v, time.Now().Unix(), groupID,
	)
	if err != nil {
		return fmt.Errorf("db: set group disk_layout_id: %w", err)
	}
	return nil
}

// ─── scan helper ─────────────────────────────────────────────────────────────

type diskLayoutScanner interface {
	Scan(dest ...any) error
}

func scanDiskLayout(s diskLayoutScanner) (api.StoredDiskLayout, error) {
	var (
		dl             api.StoredDiskLayout
		sourceNodeNull sql.NullString
		capturedAtUnix int64
		layoutJSONStr  string
		createdAtUnix  int64
		updatedAtUnix  int64
	)
	err := s.Scan(&dl.ID, &dl.Name, &sourceNodeNull, &capturedAtUnix,
		&layoutJSONStr, &createdAtUnix, &updatedAtUnix)
	if err == sql.ErrNoRows {
		return api.StoredDiskLayout{}, api.ErrNotFound
	}
	if err != nil {
		return api.StoredDiskLayout{}, fmt.Errorf("db: scan disk layout: %w", err)
	}
	if sourceNodeNull.Valid {
		dl.SourceNodeID = sourceNodeNull.String
	}
	dl.CapturedAt = time.Unix(capturedAtUnix, 0).UTC()
	dl.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	dl.UpdatedAt = time.Unix(updatedAtUnix, 0).UTC()
	if err := json.Unmarshal([]byte(layoutJSONStr), &dl.Layout); err != nil {
		return api.StoredDiskLayout{}, fmt.Errorf("db: unmarshal disk layout json: %w", err)
	}
	return dl, nil
}
