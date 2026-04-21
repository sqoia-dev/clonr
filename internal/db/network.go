package db

import (
	"context"
	"time"

	"github.com/sqoia-dev/clonr/pkg/api"
)

// ─── Switches ─────────────────────────────────────────────────────────────────

// NetworkListSwitches returns all switches ordered by name.
func (db *DB) NetworkListSwitches(ctx context.Context) ([]api.NetworkSwitch, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, role, vendor, model, mgmt_ip, notes, is_managed, created_at, updated_at
		FROM network_switches
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var switches []api.NetworkSwitch
	for rows.Next() {
		var s api.NetworkSwitch
		var isManaged int
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Role, &s.Vendor, &s.Model,
			&s.MgmtIP, &s.Notes, &isManaged, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		s.IsManaged = isManaged != 0
		s.CreatedAt = time.Unix(createdAt, 0).UTC()
		s.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		switches = append(switches, s)
	}
	return switches, rows.Err()
}

// NetworkCreateSwitch inserts a new switch record.
func (db *DB) NetworkCreateSwitch(ctx context.Context, s api.NetworkSwitch) error {
	isManaged := 1
	if !s.IsManaged {
		isManaged = 0
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO network_switches
		    (id, name, role, vendor, model, mgmt_ip, notes, is_managed, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, s.ID, s.Name, string(s.Role), s.Vendor, s.Model, s.MgmtIP, s.Notes,
		isManaged, s.CreatedAt.Unix(), s.UpdatedAt.Unix())
	return err
}

// NetworkUpdateSwitch replaces a switch row identified by ID.
func (db *DB) NetworkUpdateSwitch(ctx context.Context, s api.NetworkSwitch) error {
	isManaged := 1
	if !s.IsManaged {
		isManaged = 0
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE network_switches
		SET name=?, role=?, vendor=?, model=?, mgmt_ip=?, notes=?, is_managed=?, updated_at=?
		WHERE id=?
	`, s.Name, string(s.Role), s.Vendor, s.Model, s.MgmtIP, s.Notes,
		isManaged, s.UpdatedAt.Unix(), s.ID)
	return err
}

// NetworkDeleteSwitch removes a switch by ID.
func (db *DB) NetworkDeleteSwitch(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM network_switches WHERE id=?`, id)
	return err
}
