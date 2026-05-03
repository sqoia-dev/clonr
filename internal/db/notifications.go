package db

import (
	"context"
	"fmt"
	"time"

	"github.com/sqoia-dev/clustr/internal/secrets"
)

// SMTPConfig is the SMTP settings stored in the DB.
// Password is decrypted on read; encrypted on write.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string // plaintext on read, encrypted in DB
	From     string
	UseTLS   bool
	UseSSL   bool
}

// IsConfigured reports whether the SMTP config has enough info to send mail.
func (c SMTPConfig) IsConfigured() bool {
	return c.Host != "" && c.From != ""
}

// GetSMTPConfig returns the SMTP configuration from the DB.
// The password is decrypted before returning.
func (db *DB) GetSMTPConfig(ctx context.Context) (SMTPConfig, error) {
	var cfg SMTPConfig
	var passEnc string
	var useTLS, useSSL int
	err := db.sql.QueryRowContext(ctx, `
		SELECT host, port, username, password_enc, from_addr, use_tls, use_ssl
		FROM smtp_config WHERE id = 'smtp'
	`).Scan(&cfg.Host, &cfg.Port, &cfg.Username, &passEnc, &cfg.From, &useTLS, &useSSL)
	if err != nil {
		return cfg, fmt.Errorf("db: get smtp config: %w", err)
	}
	cfg.UseTLS = useTLS == 1
	cfg.UseSSL = useSSL == 1
	if passEnc != "" {
		plain, err := secrets.Decrypt(passEnc)
		if err == nil {
			cfg.Password = string(plain)
		}
		// Silently ignore decryption errors; password is just empty.
	}
	return cfg, nil
}

// SetSMTPConfig upserts the SMTP configuration.
// The password is encrypted before storing.
func (db *DB) SetSMTPConfig(ctx context.Context, cfg SMTPConfig) error {
	passEnc := ""
	if cfg.Password != "" {
		enc, err := secrets.Encrypt([]byte(cfg.Password))
		if err != nil {
			return fmt.Errorf("db: encrypt smtp password: %w", err)
		}
		passEnc = enc
	}
	useTLS := 0
	if cfg.UseTLS {
		useTLS = 1
	}
	useSSL := 0
	if cfg.UseSSL {
		useSSL = 1
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO smtp_config (id, host, port, username, password_enc, from_addr, use_tls, use_ssl, updated_at)
		VALUES ('smtp', ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  host=excluded.host, port=excluded.port, username=excluded.username,
		  password_enc=excluded.password_enc, from_addr=excluded.from_addr,
		  use_tls=excluded.use_tls, use_ssl=excluded.use_ssl, updated_at=excluded.updated_at
	`, cfg.Host, cfg.Port, cfg.Username, passEnc, cfg.From, useTLS, useSSL, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("db: set smtp config: %w", err)
	}
	return nil
}

// ListApprovedMemberEmails returns the LDAP usernames (used as email addresses)
// of all approved members of a NodeGroup. These come from pi_member_requests
// with status='approved'. If users have email-format usernames (e.g. jdoe@example.com)
// they work directly as SMTP To: addresses. Otherwise the operator should configure
// an LDAP attribute for email lookup (future enhancement).
func (db *DB) ListApprovedMemberEmails(ctx context.Context, groupID string) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT DISTINCT ldap_username
		FROM pi_member_requests
		WHERE group_id = ? AND status = 'approved' AND ldap_username != ''
	`, groupID)
	if err != nil {
		return nil, fmt.Errorf("db: list approved member emails: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ─── Broadcast rate-limiting ──────────────────────────────────────────────────

// GetBroadcastLastSent returns the Unix timestamp of the last broadcast for a group.
// Returns 0 if no broadcast has been sent for this group.
func (db *DB) GetBroadcastLastSent(ctx context.Context, groupID string) (int64, error) {
	var ts int64
	err := db.sql.QueryRowContext(ctx,
		`SELECT last_sent_at FROM broadcast_log WHERE group_id = ?`, groupID,
	).Scan(&ts)
	if err != nil {
		return 0, nil // no row = never sent
	}
	return ts, nil
}

// SetBroadcastLastSent upserts the broadcast timestamp for a group.
func (db *DB) SetBroadcastLastSent(ctx context.Context, groupID string, ts time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO broadcast_log (group_id, last_sent_at) VALUES (?, ?)
		ON CONFLICT(group_id) DO UPDATE SET last_sent_at = excluded.last_sent_at
	`, groupID, ts.Unix())
	if err != nil {
		return fmt.Errorf("db: set broadcast last sent: %w", err)
	}
	return nil
}
