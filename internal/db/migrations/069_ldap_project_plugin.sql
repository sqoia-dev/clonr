-- Migration 069: OpenLDAP project plugin (Sprint G, v1.6.0 / CF-24).
--
-- Adds LDAP group sync state to node_groups:
--   ldap_group_dn       — LDAP DN of the posixGroup created for this group
--                         (null = no posixGroup created yet or LDAP not enabled)
--   ldap_sync_state     — 'pending' | 'synced' | 'failed' | 'disabled'
--   ldap_sync_last_at   — Unix timestamp of last sync attempt
--   ldap_sync_error     — last error message (empty on success)
--   ldap_sync_enabled   — whether this group participates in LDAP project sync
--                         (default true; admin can disable per group)

ALTER TABLE node_groups ADD COLUMN ldap_group_dn        TEXT;
ALTER TABLE node_groups ADD COLUMN ldap_sync_state      TEXT NOT NULL DEFAULT 'disabled'
    CHECK(ldap_sync_state IN ('pending','synced','failed','disabled'));
ALTER TABLE node_groups ADD COLUMN ldap_sync_last_at    INTEGER;
ALTER TABLE node_groups ADD COLUMN ldap_sync_error      TEXT NOT NULL DEFAULT '';
ALTER TABLE node_groups ADD COLUMN ldap_sync_enabled    INTEGER NOT NULL DEFAULT 1;

-- Retry queue for failed LDAP sync operations (never block the primary workflow).
-- When LDAP is down, changes are queued here and retried by the background worker.
CREATE TABLE IF NOT EXISTS ldap_sync_queue (
    id           TEXT PRIMARY KEY,
    group_id     TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    operation    TEXT NOT NULL CHECK(operation IN ('create_group','delete_group','add_member','remove_member','resync')),
    payload      TEXT NOT NULL DEFAULT '{}',  -- JSON: {member_uid, ...}
    attempt      INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    next_retry_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ldap_sync_queue_next_retry ON ldap_sync_queue(next_retry_at);
CREATE INDEX IF NOT EXISTS idx_ldap_sync_queue_group ON ldap_sync_queue(group_id);
