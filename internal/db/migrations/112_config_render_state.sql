-- migration 112: Sprint 36 Bundle A — reactive config render-state tracking.
--
-- config_render_state tracks, per-(node, plugin), the hash of the last
-- successfully-rendered install-instruction set and when it was pushed.
-- The reactive-config diff engine uses this to short-circuit re-pushes
-- when a plugin's render output is byte-identical to the last push.
--
-- Granularity is per-(node, plugin) so two plugins that contribute to the
-- same target file (via ANCHORS) can be re-pushed independently.
--
-- rendered_hash  — SHA-256 of canonical-JSON(instructions); computed by
--                  config.HashInstructions.
-- rendered_at    — wall-clock timestamp when Render was last called for
--                  this (node, plugin) pair; NULL until first render.
-- pushed_at      — NULL until the push is acked by clientd; non-NULL means
--                  the node received and applied the instructions.
-- push_attempts  — monotonically incrementing counter; used for the
--                  >10 failures → severity=critical escalation (§7.3).
-- last_error     — last Render or push error; NULL on success. Lets the
--                  operator see "this plugin has been broken for 6 hours"
--                  via a simple SQL query without joining alert history.
--
-- FK to nodes (not node_configs) via ON DELETE CASCADE so rows are pruned
-- automatically when a node is decommissioned.

CREATE TABLE IF NOT EXISTS config_render_state (
    node_id        TEXT    NOT NULL,
    plugin_name    TEXT    NOT NULL,
    rendered_hash  TEXT    NOT NULL DEFAULT '',
    rendered_at    INTEGER,                    -- unix seconds; NULL before first render
    pushed_at      INTEGER,                    -- unix seconds; NULL until clientd acks
    push_attempts  INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT,                       -- NULL on success
    PRIMARY KEY (node_id, plugin_name),
    FOREIGN KEY (node_id) REFERENCES node_configs(id) ON DELETE CASCADE
);

-- Index for plugin-wide queries (e.g. "which nodes have this plugin broken?").
CREATE INDEX IF NOT EXISTS idx_config_render_state_plugin
    ON config_render_state (plugin_name);

-- Index for pushed_at to find stale (never-pushed) rows efficiently.
CREATE INDEX IF NOT EXISTS idx_config_render_state_pushed_at
    ON config_render_state (pushed_at);
