-- 106_node_stats_expires_at.sql (Sprint 38 Bundle A — STAT-EXPIRES)
--
-- Adds an optional expires_at column to node_stats so that callers can
-- mark samples as TTL-bounded ("agent-less probe values stop being
-- 'current' 60 minutes after the last poll"). The semantics:
--
--   expires_at IS NULL              — sample never expires; treated like
--                                     it is today (clientd-pushed metrics
--                                     keep their existing behaviour).
--   expires_at <= strftime('%s','now') — sample is stale; "current views"
--                                     filter it out. Historical queries
--                                     (since/until time-window) are
--                                     unaffected.
--
-- A daily sweeper deletes rows where expires_at < now()-N (we ride on top
-- of the existing 7-day retention sweeper, which still wins for the
-- common case where retention < expiry).
--
-- The partial index keeps the column zero-cost for clients that never
-- set it (clientd's stats_batch handler currently leaves it NULL).

ALTER TABLE node_stats ADD COLUMN expires_at INTEGER;

CREATE INDEX IF NOT EXISTS idx_node_stats_expires_at
    ON node_stats(expires_at)
    WHERE expires_at IS NOT NULL;
