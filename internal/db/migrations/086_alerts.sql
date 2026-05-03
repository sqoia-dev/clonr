-- migration 086: alert rule engine (#133)
--
-- alerts persists the state of every alert that has fired or resolved.
-- The engine evaluates YAML rules from /etc/clustr/rules.d/ on a 60s tick.
-- A row is inserted when an alert fires and updated when it resolves.
--
-- State machine: firing | resolved
--
-- UNIQUE constraint on (rule_name, node_id, sensor, labels_json, fired_at) so
-- that a rule which fires, resolves, and fires again creates a new row rather
-- than colliding.  fired_at (unix seconds) discriminates the repetitions.
--
-- label_json is the JSON-encoded label-tuple used for grouping — matches the
-- labels_json column from node_stats for the same sensor, or NULL for rules
-- with no label filter.

CREATE TABLE IF NOT EXISTS alerts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_name     TEXT    NOT NULL,
    node_id       TEXT    NOT NULL,
    sensor        TEXT    NOT NULL,
    labels_json   TEXT,                 -- nullable; JSON-encoded map[string]string
    severity      TEXT    NOT NULL,     -- info | warn | critical
    state         TEXT    NOT NULL,     -- firing | resolved
    fired_at      INTEGER NOT NULL,     -- unix seconds
    resolved_at   INTEGER,              -- nullable; unix seconds
    last_value    REAL    NOT NULL,
    threshold_op  TEXT    NOT NULL,
    threshold_val REAL    NOT NULL,
    UNIQUE (rule_name, node_id, sensor, labels_json, fired_at)
);

CREATE INDEX IF NOT EXISTS idx_alerts_state ON alerts (state);
CREATE INDEX IF NOT EXISTS idx_alerts_node  ON alerts (node_id);
