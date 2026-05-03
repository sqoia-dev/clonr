-- migration 085: on-node stats collection (#131)
--
-- node_stats stores time-series samples pushed by clustr-clientd over the
-- clientd WebSocket as stats_batch messages. Each row is one sensor reading
-- from one plugin on one node at one point in time.
--
-- Retention: a background goroutine in clustr-serverd runs a sweeper every
-- 5 minutes and deletes rows older than CLUSTR_STATS_RETENTION_DAYS (default 7).
-- Hard limits: min 1 day, max 90 days.
--
-- Primary key: (node_id, plugin, sensor, ts) — guarantees idempotent inserts
-- when the node re-sends an unacknowledged batch (same batch_id, same ts).
-- SQLite's INSERT OR IGNORE on this PK makes repeated inserts safe.
--
-- Index strategy (two targeted indexes, not a covering index):
--   idx_node_stats_ts           — sweeper DELETE and time-range queries
--   idx_node_stats_node_plugin  — per-node/plugin reads (API + Prometheus cache)

CREATE TABLE IF NOT EXISTS node_stats (
    node_id      TEXT    NOT NULL,
    plugin       TEXT    NOT NULL,
    sensor       TEXT    NOT NULL,
    value        REAL    NOT NULL,
    unit         TEXT,
    labels_json  TEXT,               -- nullable; JSON-encoded map[string]string
    ts           INTEGER NOT NULL    -- Unix seconds; NOT NULL; indexed
);

-- PRIMARY KEY as a separate constraint so INSERT OR IGNORE works cleanly.
CREATE UNIQUE INDEX IF NOT EXISTS pk_node_stats
    ON node_stats (node_id, plugin, sensor, ts);

-- Sweeper uses this to find old rows efficiently.
CREATE INDEX IF NOT EXISTS idx_node_stats_ts
    ON node_stats (ts);

-- API query and Prometheus latest-sample cache use this.
CREATE INDEX IF NOT EXISTS idx_node_stats_node_plugin
    ON node_stats (node_id, plugin);
