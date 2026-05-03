-- Migration 063: SMTP notification configuration (Sprint D, D2-1, CF-15).
--
-- Stores SMTP settings in the DB (encrypted password). Settings can also be
-- overridden by env vars (CLUSTR_SMTP_*) which take precedence at runtime.
-- Only one row exists (upsert pattern on id='smtp').

CREATE TABLE IF NOT EXISTS smtp_config (
    id          TEXT PRIMARY KEY DEFAULT 'smtp',
    host        TEXT NOT NULL DEFAULT '',
    port        INTEGER NOT NULL DEFAULT 587,
    username    TEXT NOT NULL DEFAULT '',
    password_enc TEXT NOT NULL DEFAULT '',   -- AES-256-GCM encrypted, same scheme as LDAP creds
    from_addr   TEXT NOT NULL DEFAULT '',
    use_tls     INTEGER NOT NULL DEFAULT 1,  -- 1=STARTTLS, 0=plain
    use_ssl     INTEGER NOT NULL DEFAULT 0,  -- 1=implicit TLS (port 465), 0=STARTTLS or plain
    updated_at  INTEGER NOT NULL DEFAULT 0
);

-- Insert the default (unconfigured) row.
INSERT OR IGNORE INTO smtp_config (id, updated_at) VALUES ('smtp', 0);

-- Broadcast rate-limiting: track last broadcast time per NodeGroup.
CREATE TABLE IF NOT EXISTS broadcast_log (
    group_id    TEXT PRIMARY KEY REFERENCES node_groups(id) ON DELETE CASCADE,
    last_sent_at INTEGER NOT NULL DEFAULT 0
);
