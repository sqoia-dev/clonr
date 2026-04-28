-- Migration 067: Per-user notification preferences (Sprint E, E4, CF-11/CF-15 enhancements).
--
-- Extends Sprint D's basic SMTP infrastructure with per-user, per-event-type preferences.
-- Default behavior: immediate delivery for critical events, daily digest for routine ones.
-- This realizes D19 (ship the recommendation) and scaffolds for i18n (language field).
--
-- Digest modes:
--   immediate  — send immediately (critical events)
--   hourly     — batch into hourly digest
--   daily      — batch into daily digest (default for routine events)
--   weekly     — batch into weekly digest
--   disabled   — never send this category to this user

CREATE TABLE IF NOT EXISTS user_notification_prefs (
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_type       TEXT NOT NULL,   -- matches notification event name (e.g. 'nodegroup_membership_added')
    delivery_mode    TEXT NOT NULL DEFAULT 'daily'
                     CHECK(delivery_mode IN ('immediate','hourly','daily','weekly','disabled')),
    language         TEXT NOT NULL DEFAULT 'en',   -- scaffold for future i18n; only 'en' in v1.4
    updated_at       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, event_type)
);

CREATE INDEX IF NOT EXISTS idx_unp_user  ON user_notification_prefs(user_id);
CREATE INDEX IF NOT EXISTS idx_unp_event ON user_notification_prefs(event_type);

-- Notification digest queue: events awaiting batched delivery.
-- Events with delivery_mode=immediate bypass this table (sent inline).
CREATE TABLE IF NOT EXISTS notification_digest_queue (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_type      TEXT NOT NULL,
    recipient_email TEXT NOT NULL,
    subject         TEXT NOT NULL DEFAULT '',
    body_text       TEXT NOT NULL DEFAULT '',
    body_html       TEXT NOT NULL DEFAULT '',
    scheduled_for   INTEGER NOT NULL,   -- Unix timestamp of planned digest send
    created_at      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ndq_user      ON notification_digest_queue(user_id);
CREATE INDEX IF NOT EXISTS idx_ndq_scheduled ON notification_digest_queue(scheduled_for);
CREATE INDEX IF NOT EXISTS idx_ndq_event     ON notification_digest_queue(event_type);

-- Global notification event defaults (delivery mode per event type, used when
-- a user has no explicit preference row).
-- D19 principle: ship the recommendation (immediate for critical, daily for routine).
CREATE TABLE IF NOT EXISTS notification_event_defaults (
    event_type    TEXT PRIMARY KEY,
    delivery_mode TEXT NOT NULL DEFAULT 'daily'
                  CHECK(delivery_mode IN ('immediate','hourly','daily','weekly','disabled')),
    description   TEXT NOT NULL DEFAULT ''
);

INSERT OR IGNORE INTO notification_event_defaults (event_type, delivery_mode, description) VALUES
('ldap_account_created',        'immediate', 'Account created — critical, user needs credentials immediately'),
('nodegroup_membership_added',  'immediate', 'Added to a group — critical, user needs cluster access info'),
('nodegroup_membership_removed','immediate', 'Removed from a group — critical, user should know immediately'),
('pi_request_approved',         'immediate', 'PI request approved — PI needs to act on approval'),
('pi_request_denied',           'immediate', 'PI request denied — PI needs to know decision'),
('allocation_change_approved',  'immediate', 'Allocation change approved — PI needs to act on approval'),
('allocation_change_denied',    'immediate', 'Allocation change denied — PI needs to know decision'),
('annual_review',               'daily',     'Annual review due — routine reminder, daily digest is fine'),
('annual_review_submitted',     'daily',     'Annual review submitted — routine admin notification'),
('broadcast',                   'immediate', 'Admin broadcast — always immediate (maintenance windows etc)'),
('digest_summary',              'daily',     'Digest summary — always daily (this is the digest itself)');
