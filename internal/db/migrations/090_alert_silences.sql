-- 090_alert_silences.sql
-- Alert silence table for Sprint 24 #155 UI.
-- Silences suppress alert dispatch for a (rule_name, optional node_id) pair
-- until expires_at (unix seconds).  expires_at = -1 means "forever".

CREATE TABLE IF NOT EXISTS alert_silences (
    id          TEXT    PRIMARY KEY,
    rule_name   TEXT    NOT NULL,
    node_id     TEXT,                  -- NULL = silence the rule on every node
    expires_at  INTEGER NOT NULL,      -- unix seconds; -1 for forever
    created_at  INTEGER NOT NULL,
    created_by  TEXT
);

CREATE INDEX IF NOT EXISTS idx_alert_silences_rule ON alert_silences(rule_name);
