-- 107_node_external_stats.sql (Sprint 38 Bundle A — PROBE-3 + EXTERNAL-STATS)
--
-- node_external_stats stores results of agent-less collectors that run
-- inside clustr-serverd (no clientd required on the target node):
--
--   PROBE-3:        ping / ssh-banner / ipmi-mc reachability probes.
--   EXTERNAL-STATS: BMC sensor samples (ipmi-sensors), SNMP samples
--                   (gosnmp), per-poll cycle.
--
-- Why a separate table from node_stats:
--   - node_stats holds streaming time-series from clientd; primary
--     key (node_id, plugin, sensor, ts) and write paths are tuned for
--     bulk inserts of ~50 rows/second per node.
--   - external_stats has a different shape: at most one "latest" row
--     per (node_id, source, key) at any time, and the API returns the
--     latest sample only — there is no time-series view. Storing as a
--     replace-on-write key/value/JSON-blob row keeps queries trivial.
--
-- Schema rationale:
--   - source: 'probe' | 'bmc' | 'snmp' | 'ipmi'.  The probe row
--     stores the three booleans as a single JSON blob; the bmc/snmp
--     rows store the parsed sensor maps as JSON. Single column keeps
--     migrations forward-compatible when collectors gain new fields.
--   - PRIMARY KEY (node_id, source) — there is exactly one latest
--     sample per (node, source). Polls overwrite via UPSERT.
--   - last_seen_at: when the sample was actually collected.
--   - expires_at:   when the sample becomes stale (default 60 min
--                   after collection); "current" reads filter on it.

CREATE TABLE IF NOT EXISTS node_external_stats (
    node_id      TEXT    NOT NULL,
    source       TEXT    NOT NULL,            -- 'probe'|'bmc'|'snmp'|'ipmi'
    payload_json TEXT    NOT NULL,            -- collector-specific JSON
    last_seen_at INTEGER NOT NULL,            -- Unix seconds, collection ts
    expires_at   INTEGER NOT NULL,            -- Unix seconds, ttl boundary
    PRIMARY KEY (node_id, source)
);

CREATE INDEX IF NOT EXISTS idx_node_external_stats_expires
    ON node_external_stats(expires_at);
