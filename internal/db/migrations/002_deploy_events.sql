CREATE TABLE IF NOT EXISTS deploy_events (
    id              TEXT PRIMARY KEY,
    node_config_id  TEXT NOT NULL REFERENCES node_configs(id),
    image_id        TEXT NOT NULL REFERENCES base_images(id),
    triggered_by    TEXT NOT NULL DEFAULT 'cli',
    status          TEXT NOT NULL,
    error_message   TEXT NOT NULL DEFAULT '',
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    deployed_at     INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_deploy_events_node ON deploy_events(node_config_id);
CREATE INDEX IF NOT EXISTS idx_deploy_events_image ON deploy_events(image_id);
