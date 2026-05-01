package db

import (
	"context"
	"time"
)

// PosixIDConfig is the single-row config for the POSIX ID allocator.
type PosixIDConfig struct {
	UIDMin             int
	UIDMax             int
	GIDMin             int
	GIDMax             int
	ReservedUIDRanges  string // raw JSON, e.g. [[0,999],[1000,9999]]
	ReservedGIDRanges  string // raw JSON
	UpdatedAt          time.Time
}

// PosixIDGetConfig reads the single posixid_config row (id=1).
// Returns a row with defaults if the table exists but the row is absent.
func (db *DB) PosixIDGetConfig(ctx context.Context) (PosixIDConfig, error) {
	var cfg PosixIDConfig
	var updatedAt int64
	row := db.sql.QueryRowContext(ctx, `
		SELECT uid_min, uid_max, gid_min, gid_max,
		       reserved_uid_ranges, reserved_gid_ranges, updated_at
		FROM posixid_config WHERE id = 1
	`)
	err := row.Scan(
		&cfg.UIDMin, &cfg.UIDMax,
		&cfg.GIDMin, &cfg.GIDMax,
		&cfg.ReservedUIDRanges, &cfg.ReservedGIDRanges,
		&updatedAt,
	)
	if err != nil {
		return cfg, err
	}
	cfg.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return cfg, nil
}

// PosixIDSaveConfig upserts the posixid_config row.
func (db *DB) PosixIDSaveConfig(ctx context.Context, cfg PosixIDConfig) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO posixid_config
		    (id, uid_min, uid_max, gid_min, gid_max,
		     reserved_uid_ranges, reserved_gid_ranges, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		    uid_min             = excluded.uid_min,
		    uid_max             = excluded.uid_max,
		    gid_min             = excluded.gid_min,
		    gid_max             = excluded.gid_max,
		    reserved_uid_ranges = excluded.reserved_uid_ranges,
		    reserved_gid_ranges = excluded.reserved_gid_ranges,
		    updated_at          = excluded.updated_at
	`,
		cfg.UIDMin, cfg.UIDMax,
		cfg.GIDMin, cfg.GIDMax,
		cfg.ReservedUIDRanges, cfg.ReservedGIDRanges,
		time.Now().Unix(),
	)
	return err
}

// PosixIDRoleRange holds allocation config for a single role from posixid_role_ranges.
type PosixIDRoleRange struct {
	Role               string
	UIDMin             int
	UIDMax             int
	GIDMin             int
	GIDMax             int
	ReservedUIDRanges  string // raw JSON
	ReservedGIDRanges  string // raw JSON
}

// PosixIDGetRoleRange reads a single row from posixid_role_ranges by role.
func (db *DB) PosixIDGetRoleRange(ctx context.Context, role string) (PosixIDRoleRange, error) {
	var r PosixIDRoleRange
	row := db.sql.QueryRowContext(ctx, `
		SELECT role, uid_min, uid_max, gid_min, gid_max,
		       reserved_uid_ranges, reserved_gid_ranges
		FROM posixid_role_ranges WHERE role = ?
	`, role)
	err := row.Scan(
		&r.Role,
		&r.UIDMin, &r.UIDMax,
		&r.GIDMin, &r.GIDMax,
		&r.ReservedUIDRanges, &r.ReservedGIDRanges,
	)
	if err != nil {
		return r, err
	}
	return r, nil
}

// SysAccountsUpdateUID updates the uid field of a single system_accounts row.
// Used by the posixid reconciliation pass to correct mis-allocated UIDs.
func (db *DB) SysAccountsUpdateUID(ctx context.Context, accountID string, newUID int) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE system_accounts SET uid = ?, updated_at = ? WHERE id = ?`,
		newUID, time.Now().Unix(), accountID,
	)
	return err
}

// SysAccountsListUIDs returns all UIDs from the system_accounts table.
func (db *DB) SysAccountsListUIDs(ctx context.Context) ([]int, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT uid FROM system_accounts ORDER BY uid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var uids []int
	for rows.Next() {
		var uid int
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, rows.Err()
}

// SysAccountsListGIDs returns all GIDs from the system_groups table.
func (db *DB) SysAccountsListGIDs(ctx context.Context) ([]int, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT gid FROM system_groups ORDER BY gid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gids []int
	for rows.Next() {
		var gid int
		if err := rows.Scan(&gid); err != nil {
			return nil, err
		}
		gids = append(gids, gid)
	}
	return gids, rows.Err()
}
