package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sqoia-dev/clustr/internal/secrets"
)

// LDAPModuleConfig is the persisted state of the LDAP module singleton.
// ServiceBindPassword and AdminPasswd are stored encrypted at rest (S1-15, D4).
// The DB layer transparently encrypts on write and decrypts on read.
type LDAPModuleConfig struct {
	Enabled             bool
	Status              string // disabled|provisioning|ready|error
	StatusDetail        string
	BaseDN              string
	CACertPEM           string
	CAKeyPEM            string
	CACertFingerprint   string
	ServerCertPEM       string
	ServerKeyPEM        string
	ServerCertNotAfter  time.Time
	AdminPasswordHash   string
	ServiceBindDN       string
	// ServiceBindPassword is decrypted at read time (migration 038+).
	ServiceBindPassword string
	// AdminPasswd is the Directory Manager password, decrypted at read time (migration 038+).
	AdminPasswd string
	BaseDNLocked        bool
	LastProvisionedAt   time.Time
	LastCheckedAt       time.Time
	LastCheckError      string
	// SudoersEnabled indicates whether the clustr-admins LDAP group sudoers feature is active.
	SudoersEnabled bool
	// SudoersGroupCN is the CN of the LDAP group written into /etc/sudoers.d on deployed nodes.
	SudoersGroupCN string

	// Sprint 8 — write-bind credentials (migration 079).
	// WriteBindDN / WriteBindPassword: optional elevated bind used for directory writes.
	// If unset, falls back to AdminPasswd (the DM bind). Encrypted at rest.
	WriteBindDN       string
	WriteBindPassword string
	// WriteCapable is the cached probe result: nil = not yet probed, false = probe failed, true = OK.
	WriteCapable       *bool
	WriteCapableDetail string
}

// LDAPGetConfig reads the singleton LDAP module config row.
// Decrypts service_bind_password and admin_passwd at read time (migration 038+).
// Returns sql.ErrNoRows if the row has never been inserted (migration not applied).
func (db *DB) LDAPGetConfig(ctx context.Context) (LDAPModuleConfig, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT
			enabled, status, status_detail, base_dn,
			ca_cert_pem, ca_key_pem, ca_cert_fingerprint,
			server_cert_pem, server_key_pem, server_cert_not_after,
			admin_password_hash, service_bind_dn, service_bind_password,
			base_dn_locked, last_provisioned_at, last_checked_at, last_check_error,
			admin_passwd,
			sudoers_enabled, sudoers_group_cn,
			service_bind_password_encrypted, admin_passwd_encrypted,
			write_bind_dn, write_bind_password, write_bind_password_encrypted,
			write_capable, write_capable_detail
		FROM ldap_module_config WHERE id = 1
	`)

	var cfg LDAPModuleConfig
	var serverCertNotAfter sql.NullString
	var lastProvisionedAt sql.NullString
	var lastCheckedAt sql.NullString
	var sbpEncrypted, apEncrypted, wbpEncrypted bool
	var writeCapable sql.NullBool

	err := row.Scan(
		&cfg.Enabled, &cfg.Status, &cfg.StatusDetail, &cfg.BaseDN,
		&cfg.CACertPEM, &cfg.CAKeyPEM, &cfg.CACertFingerprint,
		&cfg.ServerCertPEM, &cfg.ServerKeyPEM, &serverCertNotAfter,
		&cfg.AdminPasswordHash, &cfg.ServiceBindDN, &cfg.ServiceBindPassword,
		&cfg.BaseDNLocked, &lastProvisionedAt, &lastCheckedAt, &cfg.LastCheckError,
		&cfg.AdminPasswd,
		&cfg.SudoersEnabled, &cfg.SudoersGroupCN,
		&sbpEncrypted, &apEncrypted,
		&cfg.WriteBindDN, &cfg.WriteBindPassword, &wbpEncrypted,
		&writeCapable, &cfg.WriteCapableDetail,
	)
	if err != nil {
		return LDAPModuleConfig{}, err
	}

	// Decrypt credentials if marked as encrypted.
	if sbpEncrypted && cfg.ServiceBindPassword != "" {
		if plain, derr := secrets.Decrypt(cfg.ServiceBindPassword); derr == nil {
			cfg.ServiceBindPassword = string(plain)
		}
		// If decryption fails (wrong key), leave the ciphertext — caller will see garbage
		// and the module will error on use. This is intentional: fail-closed.
	}
	if apEncrypted && cfg.AdminPasswd != "" {
		if plain, derr := secrets.Decrypt(cfg.AdminPasswd); derr == nil {
			cfg.AdminPasswd = string(plain)
		}
	}
	if wbpEncrypted && cfg.WriteBindPassword != "" {
		if plain, derr := secrets.Decrypt(cfg.WriteBindPassword); derr == nil {
			cfg.WriteBindPassword = string(plain)
		}
	}

	if serverCertNotAfter.Valid && serverCertNotAfter.String != "" {
		if t, err := time.Parse(time.RFC3339, serverCertNotAfter.String); err == nil {
			cfg.ServerCertNotAfter = t
		}
	}
	if lastProvisionedAt.Valid && lastProvisionedAt.String != "" {
		if t, err := time.Parse(time.RFC3339, lastProvisionedAt.String); err == nil {
			cfg.LastProvisionedAt = t
		}
	}
	if lastCheckedAt.Valid && lastCheckedAt.String != "" {
		if t, err := time.Parse(time.RFC3339, lastCheckedAt.String); err == nil {
			cfg.LastCheckedAt = t
		}
	}
	if writeCapable.Valid {
		b := writeCapable.Bool
		cfg.WriteCapable = &b
	}

	return cfg, nil
}

// LDAPSaveConfig saves the full LDAP module config to the singleton row.
// Encrypts service_bind_password and admin_passwd at write time (S1-15, D4).
// If CLUSTR_SECRET_KEY is not set, the write is rejected to prevent storing
// credentials in plaintext after the encryption migration has been applied.
func (db *DB) LDAPSaveConfig(ctx context.Context, cfg LDAPModuleConfig) error {
	var notAfterStr *string
	if !cfg.ServerCertNotAfter.IsZero() {
		s := cfg.ServerCertNotAfter.UTC().Format(time.RFC3339)
		notAfterStr = &s
	}
	var lastProvStr *string
	if !cfg.LastProvisionedAt.IsZero() {
		s := cfg.LastProvisionedAt.UTC().Format(time.RFC3339)
		lastProvStr = &s
	}

	// Encrypt credentials. Both fields are encrypted independently.
	sbpCiphertext := cfg.ServiceBindPassword
	sbpEncrypted := false
	if cfg.ServiceBindPassword != "" {
		enc, err := secrets.Encrypt([]byte(cfg.ServiceBindPassword))
		if err != nil {
			return fmt.Errorf("db: LDAPSaveConfig: encrypt service_bind_password: %w", err)
		}
		sbpCiphertext = enc
		sbpEncrypted = true
	}

	apCiphertext := cfg.AdminPasswd
	apEncrypted := false
	if cfg.AdminPasswd != "" {
		enc, err := secrets.Encrypt([]byte(cfg.AdminPasswd))
		if err != nil {
			return fmt.Errorf("db: LDAPSaveConfig: encrypt admin_passwd: %w", err)
		}
		apCiphertext = enc
		apEncrypted = true
	}

	_, err := db.sql.ExecContext(ctx, `
		UPDATE ldap_module_config SET
			enabled = ?,
			status = ?,
			status_detail = ?,
			base_dn = ?,
			ca_cert_pem = ?,
			ca_key_pem = ?,
			ca_cert_fingerprint = ?,
			server_cert_pem = ?,
			server_key_pem = ?,
			server_cert_not_after = ?,
			admin_password_hash = ?,
			service_bind_dn = ?,
			service_bind_password = ?,
			service_bind_password_encrypted = ?,
			last_provisioned_at = ?,
			admin_passwd = ?,
			admin_passwd_encrypted = ?
		WHERE id = 1
	`,
		cfg.Enabled, cfg.Status, cfg.StatusDetail, cfg.BaseDN,
		cfg.CACertPEM, cfg.CAKeyPEM, cfg.CACertFingerprint,
		cfg.ServerCertPEM, cfg.ServerKeyPEM, notAfterStr,
		cfg.AdminPasswordHash, cfg.ServiceBindDN,
		sbpCiphertext, boolToInt(sbpEncrypted),
		lastProvStr,
		apCiphertext, boolToInt(apEncrypted),
	)
	if err != nil {
		return fmt.Errorf("db: LDAPSaveConfig: %w", err)
	}
	return nil
}

// LDAPSetStatus updates just the status and status_detail columns.
func (db *DB) LDAPSetStatus(ctx context.Context, status, detail string) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE ldap_module_config SET status = ?, status_detail = ? WHERE id = 1`,
		status, detail,
	)
	if err != nil {
		return fmt.Errorf("db: LDAPSetStatus: %w", err)
	}
	return nil
}

// LDAPUpdateHealthCheck records the last-checked timestamp and any error string.
func (db *DB) LDAPUpdateHealthCheck(ctx context.Context, checkedAt time.Time, checkError string) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE ldap_module_config SET last_checked_at = ?, last_check_error = ? WHERE id = 1`,
		checkedAt.UTC().Format(time.RFC3339), checkError,
	)
	if err != nil {
		return fmt.Errorf("db: LDAPUpdateHealthCheck: %w", err)
	}
	return nil
}

// LDAPDisable resets the module to the disabled state, clearing all config data.
func (db *DB) LDAPDisable(ctx context.Context) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE ldap_module_config SET
			enabled = 0,
			status = 'disabled',
			status_detail = '',
			base_dn = '',
			ca_cert_pem = '',
			ca_key_pem = '',
			ca_cert_fingerprint = '',
			server_cert_pem = '',
			server_key_pem = '',
			server_cert_not_after = NULL,
			admin_password_hash = '',
			service_bind_dn = '',
			service_bind_password = '',
			service_bind_password_encrypted = 0,
			admin_passwd = '',
			admin_passwd_encrypted = 0,
			base_dn_locked = 0,
			last_provisioned_at = NULL,
			last_checked_at = NULL,
			last_check_error = ''
		WHERE id = 1
	`)
	if err != nil {
		return fmt.Errorf("db: LDAPDisable: %w", err)
	}
	return nil
}

// LDAPSetAdminPasswd writes only the admin_passwd column atomically, encrypting it.
// Called by Enable() after provisioning so the encrypted password survives restarts.
func (db *DB) LDAPSetAdminPasswd(ctx context.Context, passwd string) error {
	ciphertext := passwd
	encrypted := false
	if passwd != "" {
		enc, err := secrets.Encrypt([]byte(passwd))
		if err != nil {
			return fmt.Errorf("db: LDAPSetAdminPasswd: encrypt: %w", err)
		}
		ciphertext = enc
		encrypted = true
	}
	_, err := db.sql.ExecContext(ctx,
		`UPDATE ldap_module_config SET admin_passwd = ?, admin_passwd_encrypted = ? WHERE id = 1`,
		ciphertext, boolToInt(encrypted),
	)
	if err != nil {
		return fmt.Errorf("db: LDAPSetAdminPasswd: %w", err)
	}
	return nil
}

// MigrateLDAPCredentials re-encrypts any plaintext LDAP credentials on first run
// after the encryption migration (038). Safe to call multiple times — rows already
// marked as encrypted are skipped. Returns (changed, error) where changed indicates
// at least one row was encrypted.
func (db *DB) MigrateLDAPCredentials(ctx context.Context) (bool, error) {
	// Read current state directly (not via LDAPGetConfig, which decrypts).
	var sbp, ap string
	var sbpEncrypted, apEncrypted bool
	err := db.sql.QueryRowContext(ctx, `
		SELECT service_bind_password, service_bind_password_encrypted,
		       admin_passwd, admin_passwd_encrypted
		FROM ldap_module_config WHERE id = 1
	`).Scan(&sbp, &sbpEncrypted, &ap, &apEncrypted)
	if err == sql.ErrNoRows {
		return false, nil // no row yet — nothing to migrate
	}
	if err != nil {
		return false, fmt.Errorf("db: MigrateLDAPCredentials: read: %w", err)
	}

	changed := false

	// Encrypt service_bind_password if plaintext and non-empty.
	if !sbpEncrypted && sbp != "" {
		enc, err := secrets.Encrypt([]byte(sbp))
		if err != nil {
			return false, fmt.Errorf("db: MigrateLDAPCredentials: encrypt service_bind_password: %w", err)
		}
		_, err = db.sql.ExecContext(ctx,
			`UPDATE ldap_module_config SET service_bind_password = ?, service_bind_password_encrypted = 1 WHERE id = 1`,
			enc,
		)
		if err != nil {
			return false, fmt.Errorf("db: MigrateLDAPCredentials: write service_bind_password: %w", err)
		}
		changed = true
	}

	// Encrypt admin_passwd if plaintext and non-empty.
	if !apEncrypted && ap != "" {
		enc, err := secrets.Encrypt([]byte(ap))
		if err != nil {
			return false, fmt.Errorf("db: MigrateLDAPCredentials: encrypt admin_passwd: %w", err)
		}
		_, err = db.sql.ExecContext(ctx,
			`UPDATE ldap_module_config SET admin_passwd = ?, admin_passwd_encrypted = 1 WHERE id = 1`,
			enc,
		)
		if err != nil {
			return false, fmt.Errorf("db: MigrateLDAPCredentials: write admin_passwd: %w", err)
		}
		changed = true
	}

	return changed, nil
}

// LDAPCountConfiguredNodes returns the number of nodes with ldap_node_state rows.
func (db *DB) LDAPCountConfiguredNodes(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM ldap_node_state`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("db: LDAPCountConfiguredNodes: %w", err)
	}
	return n, nil
}

// LDAPListConfiguredNodeIDs returns the node IDs that have ldap_node_state rows.
func (db *DB) LDAPListConfiguredNodeIDs(ctx context.Context) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT node_id FROM ldap_node_state`)
	if err != nil {
		return nil, fmt.Errorf("db: LDAPListConfiguredNodeIDs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// LDAPRecordNodeConfigured upserts a ldap_node_state row for the given node.
func (db *DB) LDAPRecordNodeConfigured(ctx context.Context, nodeID, configHash string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO ldap_node_state (node_id, configured_at, last_config_hash)
		VALUES (?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			configured_at = excluded.configured_at,
			last_config_hash = excluded.last_config_hash
	`, nodeID, now, configHash)
	if err != nil {
		return fmt.Errorf("db: LDAPRecordNodeConfigured: %w", err)
	}
	return nil
}

// LDAPLockBaseDN sets base_dn_locked = 1 on the singleton config row.
// Idempotent — safe to call multiple times.
func (db *DB) LDAPLockBaseDN(ctx context.Context) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE ldap_module_config SET base_dn_locked = 1 WHERE id = 1`)
	if err != nil {
		return fmt.Errorf("db: LDAPLockBaseDN: %w", err)
	}
	return nil
}

// LDAPSetSudoersEnabled updates the sudoers_enabled and sudoers_group_cn columns.
// Pass enabled=true and the desired group CN to activate; enabled=false to deactivate.
// When disabling, groupCN is ignored (the existing value is preserved for re-enable).
func (db *DB) LDAPSetSudoersEnabled(ctx context.Context, enabled bool, groupCN string) error {
	if enabled {
		_, err := db.sql.ExecContext(ctx,
			`UPDATE ldap_module_config SET sudoers_enabled = 1, sudoers_group_cn = ? WHERE id = 1`,
			groupCN,
		)
		if err != nil {
			return fmt.Errorf("db: LDAPSetSudoersEnabled(true): %w", err)
		}
	} else {
		_, err := db.sql.ExecContext(ctx,
			`UPDATE ldap_module_config SET sudoers_enabled = 0 WHERE id = 1`,
		)
		if err != nil {
			return fmt.Errorf("db: LDAPSetSudoersEnabled(false): %w", err)
		}
	}
	return nil
}

// ─── Sprint 8: write-bind credentials (migration 079) ────────────────────────

// LDAPSetWriteBind persists the optional elevated write-bind credentials.
// Both are encrypted at rest. Pass empty strings to clear the write bind.
func (db *DB) LDAPSetWriteBind(ctx context.Context, bindDN, bindPassword string) error {
	wbpCiphertext := bindPassword
	wbpEncrypted := false
	if bindPassword != "" {
		enc, err := secrets.Encrypt([]byte(bindPassword))
		if err != nil {
			return fmt.Errorf("db: LDAPSetWriteBind: encrypt write_bind_password: %w", err)
		}
		wbpCiphertext = enc
		wbpEncrypted = true
	}
	_, err := db.sql.ExecContext(ctx,
		`UPDATE ldap_module_config SET write_bind_dn = ?, write_bind_password = ?, write_bind_password_encrypted = ? WHERE id = 1`,
		bindDN, wbpCiphertext, boolToInt(wbpEncrypted),
	)
	if err != nil {
		return fmt.Errorf("db: LDAPSetWriteBind: %w", err)
	}
	return nil
}

// LDAPSetWriteCapable records the result of the probe-write operation.
// capable=nil clears the cached result (unknown). capable=true/false sets it.
func (db *DB) LDAPSetWriteCapable(ctx context.Context, capable *bool, detail string) error {
	if capable == nil {
		_, err := db.sql.ExecContext(ctx,
			`UPDATE ldap_module_config SET write_capable = NULL, write_capable_detail = ? WHERE id = 1`,
			detail,
		)
		return err
	}
	_, err := db.sql.ExecContext(ctx,
		`UPDATE ldap_module_config SET write_capable = ?, write_capable_detail = ? WHERE id = 1`,
		boolToInt(*capable), detail,
	)
	return err
}

// ─── Sprint 8: LDAP group mode (migration 079) ───────────────────────────────

// LDAPGroupMode represents the write-mode for a single LDAP group.
type LDAPGroupMode struct {
	CN        string
	Mode      string // "overlay" | "direct"
	UpdatedAt time.Time
	UpdatedBy string
}

// LDAPGetGroupModes returns all rows from clustr_ldap_group_mode.
func (db *DB) LDAPGetGroupModes(ctx context.Context) ([]LDAPGroupMode, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT cn, mode, updated_at, updated_by FROM clustr_ldap_group_mode`)
	if err != nil {
		return nil, fmt.Errorf("db: LDAPGetGroupModes: %w", err)
	}
	defer rows.Close()
	var out []LDAPGroupMode
	for rows.Next() {
		var gm LDAPGroupMode
		var ts int64
		if err := rows.Scan(&gm.CN, &gm.Mode, &ts, &gm.UpdatedBy); err != nil {
			return nil, err
		}
		gm.UpdatedAt = time.Unix(ts, 0).UTC()
		out = append(out, gm)
	}
	return out, rows.Err()
}

// LDAPGetGroupMode returns the mode for a single group CN.
// Returns "overlay" and nil error when no row exists (default).
func (db *DB) LDAPGetGroupMode(ctx context.Context, cn string) (string, error) {
	var mode string
	err := db.sql.QueryRowContext(ctx,
		`SELECT mode FROM clustr_ldap_group_mode WHERE cn = ?`, cn,
	).Scan(&mode)
	if err == sql.ErrNoRows {
		return "overlay", nil
	}
	if err != nil {
		return "", fmt.Errorf("db: LDAPGetGroupMode: %w", err)
	}
	return mode, nil
}

// LDAPSetGroupMode upserts the mode for a single LDAP group CN.
// updatedBy is the actor's identifier (user ID or "system").
func (db *DB) LDAPSetGroupMode(ctx context.Context, cn, mode, updatedBy string) error {
	if mode != "overlay" && mode != "direct" {
		return fmt.Errorf("db: LDAPSetGroupMode: invalid mode %q (must be overlay or direct)", mode)
	}
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO clustr_ldap_group_mode (cn, mode, updated_at, updated_by)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(cn) DO UPDATE SET
			mode       = excluded.mode,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by
	`, cn, mode, now, updatedBy)
	if err != nil {
		return fmt.Errorf("db: LDAPSetGroupMode: %w", err)
	}
	return nil
}
