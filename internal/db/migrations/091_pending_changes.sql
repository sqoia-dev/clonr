-- #154: two-stage commit — pending_changes table and module-stage-mode flags.

CREATE TABLE pending_changes (
    id          TEXT PRIMARY KEY,         -- UUID v4
    kind        TEXT NOT NULL,            -- e.g. "ldap_user", "sudoers_rule", "node_network"
    target      TEXT NOT NULL,            -- entity being changed (user_id, rule_id, node_id, etc.)
    payload     TEXT NOT NULL,            -- JSON-encoded change set
    created_by  TEXT,                     -- API key id or session user
    created_at  INTEGER NOT NULL
);

CREATE INDEX idx_pending_changes_kind ON pending_changes(kind);

-- stage_mode_flags stores per-surface opt-in flags for two-stage commit.
-- key is the surface name (e.g. "ldap_user", "sudoers_rule", "node_network").
-- value is "1" for enabled, "0" for disabled.
CREATE TABLE stage_mode_flags (
    surface    TEXT PRIMARY KEY,
    enabled    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);
