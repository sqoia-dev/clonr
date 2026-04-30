-- migration 077: per-node sudoers assignments (Sprint 7, NODE-SUDO-1)
--
-- node_sudoers stores explicit per-node sudoer assignments made by operators
-- through the web UI. This is additive to any LDAP-derived global sudoers push
-- (which remains intact). The deploy pipeline merges both sources when writing
-- /etc/sudoers.d/clustr on the node.
--
-- Fields:
--   node_id          — FK to node_configs.id
--   user_identifier  — LDAP uid or local clustr username
--   source           — "ldap" | "local"
--   commands         — sudoers commands string (default ALL)
--   assigned_at      — unix timestamp
--   assigned_by      — clustr user id who made the assignment

CREATE TABLE IF NOT EXISTS node_sudoers (
    node_id         TEXT NOT NULL,
    user_identifier TEXT NOT NULL,
    source          TEXT NOT NULL DEFAULT 'local',
    commands        TEXT NOT NULL DEFAULT 'ALL',
    assigned_at     INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    assigned_by     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (node_id, user_identifier)
);

CREATE INDEX IF NOT EXISTS idx_node_sudoers_node_id ON node_sudoers(node_id);
