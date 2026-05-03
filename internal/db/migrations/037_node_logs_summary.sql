-- 037_node_logs_summary.sql — log purge event summary table (S1-4, D2)
--
-- Records each log purge cycle with row counts, enabling operators to audit
-- retention behaviour without querying the live node_logs table.
-- The runLogPurger appends one row per purge cycle (TTL pass + per-node cap pass).
CREATE TABLE IF NOT EXISTS node_logs_summary (
    id             TEXT    NOT NULL PRIMARY KEY,  -- UUID
    purged_at      INTEGER NOT NULL,              -- unix timestamp of this purge run
    ttl_rows       INTEGER NOT NULL DEFAULT 0,    -- rows removed by TTL pass
    cap_rows       INTEGER NOT NULL DEFAULT 0,    -- rows removed by per-node cap pass
    total_rows     INTEGER NOT NULL DEFAULT 0,    -- ttl_rows + cap_rows
    retention_secs INTEGER NOT NULL DEFAULT 0,    -- TTL window that was applied
    max_rows_cap   INTEGER NOT NULL DEFAULT 0,    -- per-node cap that was applied
    node_count     INTEGER NOT NULL DEFAULT 0     -- number of nodes that had cap applied
);
