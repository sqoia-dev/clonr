package db

import (
	"context"
	"time"

	"github.com/sqoia-dev/clonr/pkg/api"
)

// ─── Groups ───────────────────────────────────────────────────────────────────

// SysAccountsListGroups returns all system groups ordered by GID ascending.
func (db *DB) SysAccountsListGroups(ctx context.Context) ([]api.SystemGroup, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, gid, description, created_at, updated_at
		FROM system_groups
		ORDER BY gid ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []api.SystemGroup
	for rows.Next() {
		var g api.SystemGroup
		var createdAt, updatedAt int64
		if err := rows.Scan(&g.ID, &g.Name, &g.GID, &g.Description, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		g.CreatedAt = time.Unix(createdAt, 0).UTC()
		g.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// SysAccountsCreateGroup inserts a new system group.
func (db *DB) SysAccountsCreateGroup(ctx context.Context, g api.SystemGroup) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO system_groups (id, name, gid, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, g.ID, g.Name, g.GID, g.Description, g.CreatedAt.Unix(), g.UpdatedAt.Unix())
	return err
}

// SysAccountsUpdateGroup replaces a group row identified by ID.
func (db *DB) SysAccountsUpdateGroup(ctx context.Context, g api.SystemGroup) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE system_groups
		SET name=?, gid=?, description=?, updated_at=?
		WHERE id=?
	`, g.Name, g.GID, g.Description, g.UpdatedAt.Unix(), g.ID)
	return err
}

// SysAccountsDeleteGroup removes a group by ID.
func (db *DB) SysAccountsDeleteGroup(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM system_groups WHERE id=?`, id)
	return err
}

// ─── Accounts ─────────────────────────────────────────────────────────────────

// SysAccountsListAccounts returns all system accounts ordered by UID ascending.
func (db *DB) SysAccountsListAccounts(ctx context.Context) ([]api.SystemAccount, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, username, uid, primary_gid, shell, home_dir,
		       create_home, system_account, comment, created_at, updated_at
		FROM system_accounts
		ORDER BY uid ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []api.SystemAccount
	for rows.Next() {
		var a api.SystemAccount
		var createHome, systemAccount int
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&a.ID, &a.Username, &a.UID, &a.PrimaryGID,
			&a.Shell, &a.HomeDir,
			&createHome, &systemAccount,
			&a.Comment, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		a.CreateHome = createHome != 0
		a.SystemAccount = systemAccount != 0
		a.CreatedAt = time.Unix(createdAt, 0).UTC()
		a.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// SysAccountsCreateAccount inserts a new system account.
func (db *DB) SysAccountsCreateAccount(ctx context.Context, a api.SystemAccount) error {
	createHome := 0
	if a.CreateHome {
		createHome = 1
	}
	sysAcct := 1
	if !a.SystemAccount {
		sysAcct = 0
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO system_accounts
		    (id, username, uid, primary_gid, shell, home_dir,
		     create_home, system_account, comment, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, a.ID, a.Username, a.UID, a.PrimaryGID, a.Shell, a.HomeDir,
		createHome, sysAcct, a.Comment, a.CreatedAt.Unix(), a.UpdatedAt.Unix())
	return err
}

// SysAccountsUpdateAccount replaces an account row identified by ID.
func (db *DB) SysAccountsUpdateAccount(ctx context.Context, a api.SystemAccount) error {
	createHome := 0
	if a.CreateHome {
		createHome = 1
	}
	sysAcct := 1
	if !a.SystemAccount {
		sysAcct = 0
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE system_accounts
		SET username=?, uid=?, primary_gid=?, shell=?, home_dir=?,
		    create_home=?, system_account=?, comment=?, updated_at=?
		WHERE id=?
	`, a.Username, a.UID, a.PrimaryGID, a.Shell, a.HomeDir,
		createHome, sysAcct, a.Comment, a.UpdatedAt.Unix(), a.ID)
	return err
}

// SysAccountsDeleteAccount removes an account by ID.
func (db *DB) SysAccountsDeleteAccount(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM system_accounts WHERE id=?`, id)
	return err
}
