package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─── Group overlays ───────────────────────────────────────────────────────────

// GroupOverlay is a supplementary membership on an LDAP group.
type GroupOverlay struct {
	GroupDN        string    `json:"group_dn"`
	UserIdentifier string    `json:"user_identifier"`
	Source         string    `json:"source"` // "ldap" | "local"
	AddedAt        time.Time `json:"added_at"`
	AddedBy        string    `json:"added_by"`
}

// GroupOverlayListByGroup returns all supplementary members for an LDAP group DN.
func (db *DB) GroupOverlayListByGroup(ctx context.Context, groupDN string) ([]GroupOverlay, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT group_dn, user_identifier, source, added_at, added_by
		FROM clustr_group_overlays
		WHERE group_dn = ?
		ORDER BY added_at ASC
	`, groupDN)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GroupOverlay
	for rows.Next() {
		var o GroupOverlay
		var addedAtUnix int64
		if err := rows.Scan(&o.GroupDN, &o.UserIdentifier, &o.Source, &addedAtUnix, &o.AddedBy); err != nil {
			return nil, err
		}
		o.AddedAt = time.Unix(addedAtUnix, 0).UTC()
		out = append(out, o)
	}
	return out, rows.Err()
}

// GroupOverlayAdd inserts a supplementary membership. Idempotent via ON CONFLICT.
func (db *DB) GroupOverlayAdd(ctx context.Context, o GroupOverlay) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO clustr_group_overlays (group_dn, user_identifier, source, added_at, added_by)
		VALUES (?, ?, ?, strftime('%s','now'), ?)
		ON CONFLICT(group_dn, user_identifier) DO UPDATE SET
			source   = excluded.source,
			added_at = excluded.added_at,
			added_by = excluded.added_by
	`, o.GroupDN, o.UserIdentifier, o.Source, o.AddedBy)
	return err
}

// GroupOverlayRemove removes a supplementary membership.
func (db *DB) GroupOverlayRemove(ctx context.Context, groupDN, userIdentifier string) error {
	_, err := db.sql.ExecContext(ctx, `
		DELETE FROM clustr_group_overlays WHERE group_dn = ? AND user_identifier = ?
	`, groupDN, userIdentifier)
	return err
}

// ─── Specialty groups ─────────────────────────────────────────────────────────

// SpecialtyGroup is a clustr-only group with no LDAP backing.
type SpecialtyGroup struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	GIDNumber   int       `json:"gid_number"`
	Description string    `json:"description"`
	Members     []string  `json:"members,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SpecialtyGroupListAll returns all specialty groups ordered by name.
func (db *DB) SpecialtyGroupListAll(ctx context.Context) ([]SpecialtyGroup, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, gid_number, description, created_at, updated_at
		FROM clustr_specialty_groups
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SpecialtyGroup
	for rows.Next() {
		var g SpecialtyGroup
		var createdAt, updatedAt int64
		if err := rows.Scan(&g.ID, &g.Name, &g.GIDNumber, &g.Description, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		g.CreatedAt = time.Unix(createdAt, 0).UTC()
		g.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Populate members for each group.
	for i := range out {
		members, err := db.SpecialtyGroupListMembers(ctx, out[i].ID)
		if err != nil {
			continue
		}
		out[i].Members = members
	}
	return out, nil
}

// SpecialtyGroupGet returns a single specialty group by ID.
func (db *DB) SpecialtyGroupGet(ctx context.Context, id string) (SpecialtyGroup, error) {
	var g SpecialtyGroup
	var createdAt, updatedAt int64
	err := db.sql.QueryRowContext(ctx, `
		SELECT id, name, gid_number, description, created_at, updated_at
		FROM clustr_specialty_groups WHERE id = ?
	`, id).Scan(&g.ID, &g.Name, &g.GIDNumber, &g.Description, &createdAt, &updatedAt)
	if err != nil {
		return SpecialtyGroup{}, err
	}
	g.CreatedAt = time.Unix(createdAt, 0).UTC()
	g.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	members, _ := db.SpecialtyGroupListMembers(ctx, id)
	g.Members = members
	return g, nil
}

// SpecialtyGroupCreate creates a new specialty group. Returns the created group.
func (db *DB) SpecialtyGroupCreate(ctx context.Context, g SpecialtyGroup) (SpecialtyGroup, error) {
	if g.ID == "" {
		g.ID = uuid.New().String()
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO clustr_specialty_groups (id, name, gid_number, description)
		VALUES (?, ?, ?, ?)
	`, g.ID, g.Name, g.GIDNumber, g.Description)
	if err != nil {
		return SpecialtyGroup{}, err
	}
	return db.SpecialtyGroupGet(ctx, g.ID)
}

// SpecialtyGroupUpdate updates name, gid_number, and description.
func (db *DB) SpecialtyGroupUpdate(ctx context.Context, id string, g SpecialtyGroup) (SpecialtyGroup, error) {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE clustr_specialty_groups
		SET name = ?, gid_number = ?, description = ?, updated_at = strftime('%s','now')
		WHERE id = ?
	`, g.Name, g.GIDNumber, g.Description, id)
	if err != nil {
		return SpecialtyGroup{}, err
	}
	return db.SpecialtyGroupGet(ctx, id)
}

// SpecialtyGroupDelete deletes a specialty group and its members (cascade).
func (db *DB) SpecialtyGroupDelete(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM clustr_specialty_groups WHERE id = ?`, id)
	return err
}

// SpecialtyGroupListMembers returns user identifiers for a group.
func (db *DB) SpecialtyGroupListMembers(ctx context.Context, groupID string) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT user_identifier FROM clustr_specialty_group_members
		WHERE group_id = ? ORDER BY user_identifier ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// SpecialtyGroupAddMember adds a member. Idempotent.
func (db *DB) SpecialtyGroupAddMember(ctx context.Context, groupID, userIdentifier, source string) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO clustr_specialty_group_members (group_id, user_identifier, source)
		VALUES (?, ?, ?)
		ON CONFLICT(group_id, user_identifier) DO UPDATE SET source = excluded.source
	`, groupID, userIdentifier, source)
	return err
}

// SpecialtyGroupRemoveMember removes a member.
func (db *DB) SpecialtyGroupRemoveMember(ctx context.Context, groupID, userIdentifier string) error {
	_, err := db.sql.ExecContext(ctx, `
		DELETE FROM clustr_specialty_group_members WHERE group_id = ? AND user_identifier = ?
	`, groupID, userIdentifier)
	return err
}
