package db

// fields_of_science DB layer — Sprint E (E2, CF-16).
//
// NSF Field of Science taxonomy. Two-level hierarchy: top-level fields and subfields.
// node_groups has a nullable field_of_science_id FK (migration 065).

import (
	"context"
	"database/sql"
	"fmt"
)

// FieldOfScience represents an NSF FOS taxonomy entry.
type FieldOfScience struct {
	ID        string
	Name      string
	ParentID  string // empty for top-level
	NSFCode   string // e.g. "11.01"
	Enabled   bool
	SortOrder int
}

// ListFieldsOfScience returns all enabled FOS entries, optionally filtered to
// top-level only (parentID="") or children of a given parent.
func (db *DB) ListFieldsOfScience(ctx context.Context) ([]FieldOfScience, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, COALESCE(parent_id,''), COALESCE(nsf_code,''), enabled, sort_order
		FROM fields_of_science
		WHERE enabled = 1
		ORDER BY sort_order, name
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list fields of science: %w", err)
	}
	defer rows.Close()
	return scanFOS(rows)
}

// ListAllFieldsOfScience returns all FOS entries including disabled (for admin management).
func (db *DB) ListAllFieldsOfScience(ctx context.Context) ([]FieldOfScience, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, COALESCE(parent_id,''), COALESCE(nsf_code,''), enabled, sort_order
		FROM fields_of_science
		ORDER BY sort_order, name
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list all fields of science: %w", err)
	}
	defer rows.Close()
	return scanFOS(rows)
}

func scanFOS(rows *sql.Rows) ([]FieldOfScience, error) {
	var out []FieldOfScience
	for rows.Next() {
		var f FieldOfScience
		var enabled int
		if err := rows.Scan(&f.ID, &f.Name, &f.ParentID, &f.NSFCode, &enabled, &f.SortOrder); err != nil {
			return nil, fmt.Errorf("db: scan field of science: %w", err)
		}
		f.Enabled = enabled == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFieldOfScience returns one FOS entry by ID.
func (db *DB) GetFieldOfScience(ctx context.Context, id string) (*FieldOfScience, error) {
	var f FieldOfScience
	var enabled int
	err := db.sql.QueryRowContext(ctx, `
		SELECT id, name, COALESCE(parent_id,''), COALESCE(nsf_code,''), enabled, sort_order
		FROM fields_of_science WHERE id = ?
	`, id).Scan(&f.ID, &f.Name, &f.ParentID, &f.NSFCode, &enabled, &f.SortOrder)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("db: field of science %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db: get field of science: %w", err)
	}
	f.Enabled = enabled == 1
	return &f, nil
}

// CreateFieldOfScience inserts a new FOS entry (admin can extend the list).
func (db *DB) CreateFieldOfScience(ctx context.Context, f *FieldOfScience) error {
	var parentID sql.NullString
	if f.ParentID != "" {
		parentID = sql.NullString{String: f.ParentID, Valid: true}
	}
	var nsfCode sql.NullString
	if f.NSFCode != "" {
		nsfCode = sql.NullString{String: f.NSFCode, Valid: true}
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO fields_of_science (id, name, parent_id, nsf_code, enabled, sort_order)
		VALUES (?, ?, ?, ?, 1, ?)
	`, f.ID, f.Name, parentID, nsfCode, f.SortOrder)
	if err != nil {
		return fmt.Errorf("db: create field of science: %w", err)
	}
	return nil
}

// UpdateFieldOfScience updates name/nsf_code/enabled/sort_order on an existing FOS entry.
func (db *DB) UpdateFieldOfScience(ctx context.Context, f *FieldOfScience) error {
	var nsfCode sql.NullString
	if f.NSFCode != "" {
		nsfCode = sql.NullString{String: f.NSFCode, Valid: true}
	}
	enabled := 0
	if f.Enabled {
		enabled = 1
	}
	res, err := db.sql.ExecContext(ctx, `
		UPDATE fields_of_science
		SET name = ?, nsf_code = ?, enabled = ?, sort_order = ?
		WHERE id = ?
	`, f.Name, nsfCode, enabled, f.SortOrder, f.ID)
	if err != nil {
		return fmt.Errorf("db: update field of science: %w", err)
	}
	return requireOneRow(res, "fields_of_science", f.ID)
}

// SetNodeGroupFOS sets the field_of_science_id on a NodeGroup. Pass empty string to clear.
func (db *DB) SetNodeGroupFOS(ctx context.Context, groupID, fosID string) error {
	var fosNull sql.NullString
	if fosID != "" {
		fosNull = sql.NullString{String: fosID, Valid: true}
	}
	res, err := db.sql.ExecContext(ctx, `
		UPDATE node_groups SET field_of_science_id = ?, updated_at = strftime('%s','now')
		WHERE id = ?
	`, fosNull, groupID)
	if err != nil {
		return fmt.Errorf("db: set node group FOS: %w", err)
	}
	return requireOneRow(res, "node_groups", groupID)
}

// NodeGroupFOSSummary is used by the director view to aggregate by field of science.
type NodeGroupFOSSummary struct {
	FOSID       string
	FOSName     string
	ParentName  string
	GroupCount  int
	NodeCount   int
	MemberCount int
}

// GetFOSUtilizationSummary returns utilization aggregated by FOS for the director view.
func (db *DB) GetFOSUtilizationSummary(ctx context.Context) ([]NodeGroupFOSSummary, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT
			COALESCE(fos.id, 'unclassified')              AS fos_id,
			COALESCE(fos.name, 'Unclassified')            AS fos_name,
			COALESCE(pfos.name, '')                       AS parent_name,
			COUNT(DISTINCT ng.id)                         AS group_count,
			COUNT(DISTINCT n.id)                          AS node_count,
			COUNT(DISTINCT ngm.user_id)                   AS member_count
		FROM node_groups ng
		LEFT JOIN fields_of_science fos  ON fos.id  = ng.field_of_science_id
		LEFT JOIN fields_of_science pfos ON pfos.id = fos.parent_id
		LEFT JOIN nodes n                ON n.group_id = ng.id
		LEFT JOIN node_group_memberships ngm ON ngm.group_id = ng.id AND ngm.status = 'approved'
		GROUP BY fos_id, fos_name, parent_name
		ORDER BY group_count DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: get FOS utilization summary: %w", err)
	}
	defer rows.Close()

	var out []NodeGroupFOSSummary
	for rows.Next() {
		var s NodeGroupFOSSummary
		if err := rows.Scan(&s.FOSID, &s.FOSName, &s.ParentName, &s.GroupCount, &s.NodeCount, &s.MemberCount); err != nil {
			return nil, fmt.Errorf("db: scan FOS summary: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
