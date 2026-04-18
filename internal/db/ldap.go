package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// LDAPModuleConfig is the persisted state of the LDAP module singleton.
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
	// ServiceBindPassword is stored plaintext in v1.
	// V2 hardening: encrypt at rest. See migration comment in 027_ldap_module.sql.
	ServiceBindPassword string
	BaseDNLocked        bool
	LastProvisionedAt   time.Time
	LastCheckedAt       time.Time
	LastCheckError      string
}

// LDAPGetConfig reads the singleton LDAP module config row.
// Returns sql.ErrNoRows if the row has never been inserted (migration not applied).
func (db *DB) LDAPGetConfig(ctx context.Context) (LDAPModuleConfig, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT
			enabled, status, status_detail, base_dn,
			ca_cert_pem, ca_key_pem, ca_cert_fingerprint,
			server_cert_pem, server_key_pem, server_cert_not_after,
			admin_password_hash, service_bind_dn, service_bind_password,
			base_dn_locked, last_provisioned_at, last_checked_at, last_check_error
		FROM ldap_module_config WHERE id = 1
	`)

	var cfg LDAPModuleConfig
	var serverCertNotAfter sql.NullString
	var lastProvisionedAt sql.NullString
	var lastCheckedAt sql.NullString

	err := row.Scan(
		&cfg.Enabled, &cfg.Status, &cfg.StatusDetail, &cfg.BaseDN,
		&cfg.CACertPEM, &cfg.CAKeyPEM, &cfg.CACertFingerprint,
		&cfg.ServerCertPEM, &cfg.ServerKeyPEM, &serverCertNotAfter,
		&cfg.AdminPasswordHash, &cfg.ServiceBindDN, &cfg.ServiceBindPassword,
		&cfg.BaseDNLocked, &lastProvisionedAt, &lastCheckedAt, &cfg.LastCheckError,
	)
	if err != nil {
		return LDAPModuleConfig{}, err
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

	return cfg, nil
}

// LDAPSaveConfig saves the full LDAP module config to the singleton row.
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
			last_provisioned_at = ?
		WHERE id = 1
	`,
		cfg.Enabled, cfg.Status, cfg.StatusDetail, cfg.BaseDN,
		cfg.CACertPEM, cfg.CAKeyPEM, cfg.CACertFingerprint,
		cfg.ServerCertPEM, cfg.ServerKeyPEM, notAfterStr,
		cfg.AdminPasswordHash, cfg.ServiceBindDN, cfg.ServiceBindPassword,
		lastProvStr,
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
