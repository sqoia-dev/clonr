package db

// attribute_visibility DB layer — Sprint E (E3, CF-39).
//
// Per-attribute visibility policy for NodeGroup (project) attributes.
// The API layer uses GetEffectiveVisibility to determine what to expose per role.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AttributeVisibilityLevel represents the minimum role required to see an attribute.
type AttributeVisibilityLevel string

const (
	VisibilityAdminOnly AttributeVisibilityLevel = "admin_only"
	VisibilityPI        AttributeVisibilityLevel = "pi"
	VisibilityMember    AttributeVisibilityLevel = "member"
	VisibilityPublic    AttributeVisibilityLevel = "public"
)

// CanSee reports whether a given role+relationship can see an attribute at the given visibility level.
// role is the clustr role (admin, pi, operator, viewer, readonly, director).
// isMember indicates whether the user is an approved member of the project.
// isPI indicates whether the user is the PI of the project.
func CanSee(visibility AttributeVisibilityLevel, role string, isPI, isMember bool) bool {
	switch visibility {
	case VisibilityAdminOnly:
		return role == "admin"
	case VisibilityPI:
		return role == "admin" || isPI
	case VisibilityMember:
		return role == "admin" || isPI || isMember
	case VisibilityPublic:
		// All authenticated users including director, operator, viewer.
		return true
	default:
		return role == "admin"
	}
}

// AttributeVisibilityDefault is one row from the global defaults table.
type AttributeVisibilityDefault struct {
	AttributeName string
	Visibility    AttributeVisibilityLevel
	Description   string
}

// ListAttributeVisibilityDefaults returns all global defaults.
func (db *DB) ListAttributeVisibilityDefaults(ctx context.Context) ([]AttributeVisibilityDefault, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT attribute_name, visibility, description
		FROM attribute_visibility_defaults
		ORDER BY attribute_name
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list attribute visibility defaults: %w", err)
	}
	defer rows.Close()
	var out []AttributeVisibilityDefault
	for rows.Next() {
		var d AttributeVisibilityDefault
		if err := rows.Scan(&d.AttributeName, &d.Visibility, &d.Description); err != nil {
			return nil, fmt.Errorf("db: scan visibility default: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetEffectiveVisibility returns the effective visibility for an attribute on a project.
// It checks the project-specific override first; falls back to the global default;
// falls back to 'admin_only' if no entry exists.
func (db *DB) GetEffectiveVisibility(ctx context.Context, projectID, attributeName string) (AttributeVisibilityLevel, error) {
	// Project-specific override.
	var vis string
	err := db.sql.QueryRowContext(ctx, `
		SELECT visibility FROM project_attribute_visibility
		WHERE project_id = ? AND attribute_name = ?
	`, projectID, attributeName).Scan(&vis)
	if err == nil {
		return AttributeVisibilityLevel(vis), nil
	}
	if err != sql.ErrNoRows {
		return VisibilityAdminOnly, fmt.Errorf("db: get project visibility: %w", err)
	}

	// Global default.
	err = db.sql.QueryRowContext(ctx, `
		SELECT visibility FROM attribute_visibility_defaults WHERE attribute_name = ?
	`, attributeName).Scan(&vis)
	if err == nil {
		return AttributeVisibilityLevel(vis), nil
	}
	if err != sql.ErrNoRows {
		return VisibilityAdminOnly, fmt.Errorf("db: get global visibility default: %w", err)
	}

	// No entry found — default to admin_only (secure default).
	return VisibilityAdminOnly, nil
}

// ProjectVisibilityOverride is one row from project_attribute_visibility.
type ProjectVisibilityOverride struct {
	ProjectID     string
	AttributeName string
	Visibility    AttributeVisibilityLevel
	UpdatedAt     time.Time
	UpdatedBy     string
}

// ListProjectVisibilityOverrides returns all per-project overrides for a given NodeGroup.
func (db *DB) ListProjectVisibilityOverrides(ctx context.Context, projectID string) ([]ProjectVisibilityOverride, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT project_id, attribute_name, visibility, updated_at, COALESCE(updated_by,'')
		FROM project_attribute_visibility
		WHERE project_id = ?
		ORDER BY attribute_name
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("db: list project visibility overrides: %w", err)
	}
	defer rows.Close()
	var out []ProjectVisibilityOverride
	for rows.Next() {
		var o ProjectVisibilityOverride
		var updatedAt int64
		if err := rows.Scan(&o.ProjectID, &o.AttributeName, &o.Visibility, &updatedAt, &o.UpdatedBy); err != nil {
			return nil, fmt.Errorf("db: scan visibility override: %w", err)
		}
		o.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, o)
	}
	return out, rows.Err()
}

// SetProjectVisibilityOverride upserts a per-project attribute visibility override.
func (db *DB) SetProjectVisibilityOverride(ctx context.Context, projectID, attributeName string, visibility AttributeVisibilityLevel, updatedBy string) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO project_attribute_visibility (project_id, attribute_name, visibility, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, attribute_name) DO UPDATE SET
		    visibility = excluded.visibility,
		    updated_at = excluded.updated_at,
		    updated_by = excluded.updated_by
	`, projectID, attributeName, visibility, time.Now().Unix(), updatedBy)
	if err != nil {
		return fmt.Errorf("db: set project visibility override: %w", err)
	}
	return nil
}

// DeleteProjectVisibilityOverride removes a per-project override (reverts to global default).
func (db *DB) DeleteProjectVisibilityOverride(ctx context.Context, projectID, attributeName string) error {
	_, err := db.sql.ExecContext(ctx, `
		DELETE FROM project_attribute_visibility
		WHERE project_id = ? AND attribute_name = ?
	`, projectID, attributeName)
	if err != nil {
		return fmt.Errorf("db: delete project visibility override: %w", err)
	}
	return nil
}

// SetAttributeVisibilityDefault upserts a global default visibility for an attribute.
func (db *DB) SetAttributeVisibilityDefault(ctx context.Context, attributeName string, visibility AttributeVisibilityLevel, description string) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO attribute_visibility_defaults (attribute_name, visibility, description, updated_at)
		VALUES (?, ?, ?, strftime('%s','now'))
		ON CONFLICT(attribute_name) DO UPDATE SET
		    visibility   = excluded.visibility,
		    description  = excluded.description,
		    updated_at   = excluded.updated_at
	`, attributeName, visibility, description)
	if err != nil {
		return fmt.Errorf("db: set attribute visibility default: %w", err)
	}
	return nil
}
