-- stats migration 002: node_external_stats table
--
-- Mirrors the schema originally created in clustr.db by migration 107.
-- This is the authoritative copy for the stats DB (stats.db).
--
-- node_external_stats stores results of agent-less collectors that run
-- inside clustr-serverd (no clientd required on the target node):
--
--   PROBE-3:        ping / ssh-banner / ipmi-mc reachability probes.
--   EXTERNAL-STATS: BMC sensor samples (ipmi-sensors), SNMP samples
--                   (gosnmp), per-poll cycle.
--
-- Schema: one latest row per (node_id, source). Polls overwrite via UPSERT.

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
