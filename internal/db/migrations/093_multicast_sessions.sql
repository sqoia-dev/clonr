-- Migration 093: multicast_sessions and multicast_session_members tables.
-- Supports the UDPCast fleet-reimage scheduler (#157).
--
-- multicast_sessions tracks each batching window keyed on (image_id, layout_id).
-- States: staging → transmitting → complete | failed | partial
--
-- multicast_session_members tracks per-node join/outcome within a session.

CREATE TABLE IF NOT EXISTS multicast_sessions (
    id                   TEXT    PRIMARY KEY,            -- UUIDv4
    image_id             TEXT    NOT NULL,
    layout_id            TEXT,                           -- nullable; matches disk_layouts.id
    state                TEXT    NOT NULL DEFAULT 'staging', -- staging|transmitting|complete|failed|partial
    multicast_group      TEXT    NOT NULL,               -- e.g. 239.255.42.7
    sender_port          INTEGER NOT NULL,
    rate_bps             INTEGER NOT NULL,               -- snapshot of multicast_rate_bps at session start
    started_at           INTEGER NOT NULL,
    fire_at              INTEGER NOT NULL,               -- started_at + window_seconds (or override)
    transmit_started_at  INTEGER,
    completed_at         INTEGER,
    error                TEXT,                           -- non-empty when state in (failed, partial)
    member_count         INTEGER NOT NULL DEFAULT 0,
    success_count        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_multicast_sessions_state
    ON multicast_sessions(state);

CREATE INDEX IF NOT EXISTS idx_multicast_sessions_match
    ON multicast_sessions(image_id, layout_id, state);

CREATE TABLE IF NOT EXISTS multicast_session_members (
    session_id   TEXT    NOT NULL REFERENCES multicast_sessions(id),
    node_id      TEXT    NOT NULL,
    joined_at    INTEGER NOT NULL,
    notified_at  INTEGER,                    -- when session descriptor was returned to the node
    finished_at  INTEGER,
    outcome      TEXT,                       -- success|failed|fellback_unicast
    PRIMARY KEY (session_id, node_id)
);

-- Global multicast configuration (key/value). Inserted once with defaults;
-- operator updates via PUT /api/v1/multicast/config.
CREATE TABLE IF NOT EXISTS multicast_config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT OR IGNORE INTO multicast_config (key, value) VALUES
    ('enabled',          'true'),
    ('window_seconds',   '60'),
    ('threshold',        '2'),
    ('rate_bps',         '100000000'),
    ('group_base',       '239.255.42.0');
