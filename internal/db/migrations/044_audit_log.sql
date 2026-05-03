-- 044_audit_log.sql (S3-4)
-- Structured audit log for all state-changing actions in clustr.
--
-- actor_id:      users.id for session auth, api_keys.id for Bearer auth,
--                '' for unauthenticated (legacy PXE callbacks), 'system' for internal.
-- actor_label:   human-readable "user:<username>" or "key:<label>" or "system".
-- action:        verb string — see constants in internal/db/audit.go.
-- resource_type: "node" | "image" | "node_group" | "user" | "api_key"
--                | "reimage" | "ldap_config" | "slurm_config" | "system".
-- resource_id:   PK of the affected resource, or '' for system events.
-- old_value:     JSON snapshot before the mutation (NULL for creates).
-- new_value:     JSON snapshot after the mutation (NULL for deletes).
-- ip_addr:       remote IP of the originating request, or ''.
-- created_at:    unix timestamp (integer) for fast range queries.
--
-- Retention: default 90 days, configurable via CLUSTR_AUDIT_RETENTION.
-- Purger runs hourly alongside the log purger.

CREATE TABLE IF NOT EXISTS audit_log (
    id            TEXT    PRIMARY KEY,
    actor_id      TEXT    NOT NULL DEFAULT '',
    actor_label   TEXT    NOT NULL DEFAULT '',
    action        TEXT    NOT NULL,
    resource_type TEXT    NOT NULL DEFAULT '',
    resource_id   TEXT    NOT NULL DEFAULT '',
    old_value     TEXT,   -- JSON or NULL
    new_value     TEXT,   -- JSON or NULL
    ip_addr       TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_log_created_at    ON audit_log(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_actor_id      ON audit_log(actor_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_resource      ON audit_log(resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_action        ON audit_log(action);
