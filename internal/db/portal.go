package db

import (
	"context"
	"fmt"
	"time"
)

// PortalConfig holds operator-configurable settings surfaced on the researcher
// portal (/portal/). Stored as a singleton row in the portal_config table.
type PortalConfig struct {
	OnDemandURL       string // CLUSTR_ONDEMAND_URL or DB value
	LDAPQuotaUsedAttr string // LDAP attribute for used quota bytes (e.g. quotaUsed)
	LDAPQuotaLimitAttr string // LDAP attribute for quota limit bytes (e.g. quotaLimit)
	UpdatedAt         time.Time
}

// GetPortalConfig reads the singleton portal_config row.
func (db *DB) GetPortalConfig(ctx context.Context) (PortalConfig, error) {
	var cfg PortalConfig
	var updatedAt int64
	err := db.sql.QueryRowContext(ctx,
		`SELECT ondemand_url, ldap_quota_used_attr, ldap_quota_limit_attr, updated_at
		 FROM portal_config WHERE id = 1`,
	).Scan(&cfg.OnDemandURL, &cfg.LDAPQuotaUsedAttr, &cfg.LDAPQuotaLimitAttr, &updatedAt)
	if err != nil {
		return PortalConfig{}, fmt.Errorf("db: get portal config: %w", err)
	}
	cfg.UpdatedAt = time.Unix(updatedAt, 0)
	return cfg, nil
}

// UpdatePortalConfig writes the singleton portal_config row.
func (db *DB) UpdatePortalConfig(ctx context.Context, cfg PortalConfig) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE portal_config
		 SET ondemand_url = ?, ldap_quota_used_attr = ?, ldap_quota_limit_attr = ?, updated_at = ?
		 WHERE id = 1`,
		cfg.OnDemandURL, cfg.LDAPQuotaUsedAttr, cfg.LDAPQuotaLimitAttr, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: update portal config: %w", err)
	}
	return nil
}
