-- 047_node_config_history.sql — S5-12: node config change history for audit trail.
--
-- node_config_history records every field-level change made to a node's
-- configuration via UpdateNodeConfig. The actor_label mirrors the format
-- used in audit_log ("user:<id>" or "key:<label>"). Paginated via the
-- /api/v1/nodes/{id}/config-history endpoint (admin-only).
--
-- This is append-only: rows are never updated or deleted here (the 90-day
-- audit purger handles audit_log; config history is kept for the lifetime
-- of the node unless manually purged).

CREATE TABLE IF NOT EXISTS node_config_history (
    id          TEXT NOT NULL PRIMARY KEY,
    node_id     TEXT NOT NULL REFERENCES node_configs(id) ON DELETE CASCADE,
    actor_label TEXT NOT NULL DEFAULT '',
    changed_at  INTEGER NOT NULL,          -- Unix timestamp (seconds UTC)
    field_name  TEXT NOT NULL,             -- e.g. "hostname", "base_image_id", "tags"
    old_value   TEXT NOT NULL DEFAULT '',  -- JSON-encoded or plain string
    new_value   TEXT NOT NULL DEFAULT ''   -- JSON-encoded or plain string
);

CREATE INDEX IF NOT EXISTS idx_node_config_history_node_id ON node_config_history(node_id, changed_at DESC);
