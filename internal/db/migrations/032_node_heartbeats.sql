-- Sprint 1: clustr-clientd heartbeat persistence.
--
-- node_heartbeats stores the most recent heartbeat received from each node's
-- clustr-clientd daemon. One row per node (upserted on every heartbeat).
-- disk_usage and services are JSON arrays serialized as TEXT.

CREATE TABLE IF NOT EXISTS node_heartbeats (
    node_id      TEXT PRIMARY KEY REFERENCES node_configs(id) ON DELETE CASCADE,
    received_at  INTEGER NOT NULL,
    uptime_sec   REAL,
    load_1       REAL,
    load_5       REAL,
    load_15      REAL,
    mem_total_kb INTEGER,
    mem_avail_kb INTEGER,
    disk_usage   TEXT,  -- JSON: []clientd.DiskUsage
    services     TEXT,  -- JSON: []clientd.ServiceStatus
    kernel       TEXT,
    clientd_ver  TEXT
);
