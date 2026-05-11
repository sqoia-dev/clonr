-- stats migration 001: node_stats time-series table
--
-- Mirrors the schema originally created in clustr.db by migrations 085 and 106.
-- This is the authoritative copy for the stats DB (stats.db).
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
-- expires_at (from clustr.db migration 106): optional TTL boundary.
--   expires_at IS NULL           — sample never expires (clientd streaming metrics).
--   expires_at <= strftime('%s') — sample is stale; filtered from current views.

CREATE TABLE IF NOT EXISTS node_stats (
    node_id      TEXT    NOT NULL,
    plugin       TEXT    NOT NULL,
    sensor       TEXT    NOT NULL,
    value        REAL    NOT NULL,
    unit         TEXT,
    labels_json  TEXT,               -- nullable; JSON-encoded map[string]string
    ts           INTEGER NOT NULL,   -- Unix seconds
    expires_at   INTEGER             -- nullable; Unix seconds; NULL = never expires
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

-- Partial index for the expires_at TTL filter; zero-cost for rows without expiry.
CREATE INDEX IF NOT EXISTS idx_node_stats_expires_at
    ON node_stats(expires_at)
    WHERE expires_at IS NOT NULL;
