-- 114_pending_dangerous_pushes.sql — Sprint 41 Day 1
--
-- Staging table for dangerous config pushes awaiting operator confirmation.
-- Rows are inserted when the observer generates a push whose plugin has
-- Dangerous=true. The push is held here until the operator submits the
-- typed confirmation string via POST /api/v1/config/dangerous/confirm.
--
-- Populated and consumed by the dangerous-confirmation flow (Sprint 41 Day 3).
-- Created in Day 1 so the schema is ready before the flow is wired.

CREATE TABLE pending_dangerous_pushes (
    id            TEXT    PRIMARY KEY,         -- push_id (server-issued UUID)
    node_id       TEXT    NOT NULL,
    plugin_name   TEXT    NOT NULL,
    rendered_hash TEXT    NOT NULL,            -- SHA-256 hex of the staged instruction set
    payload_json  TEXT    NOT NULL,            -- full ConfigPushPayload JSON, ready to send
    reason        TEXT    NOT NULL,            -- DangerReason from PluginMetadata
    challenge     TEXT    NOT NULL,            -- exact string the operator must type verbatim
    expires_at    INTEGER NOT NULL,            -- unix timestamp; 5 minutes from creation
    created_by    TEXT    NOT NULL,            -- actor_id of the operator who triggered the dirty-set
    created_at    INTEGER NOT NULL
);

-- GC index: the audit-log purger scans for rows older than expires_at+1h.
CREATE INDEX idx_pending_dangerous_pushes_expires ON pending_dangerous_pushes(expires_at);

-- Node lookup: cancel all staged pushes for a node when it goes offline.
CREATE INDEX idx_pending_dangerous_pushes_node ON pending_dangerous_pushes(node_id);
