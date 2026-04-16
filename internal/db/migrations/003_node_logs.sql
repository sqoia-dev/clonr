CREATE TABLE IF NOT EXISTS node_logs (
    id         TEXT PRIMARY KEY,
    node_mac   TEXT NOT NULL,
    hostname   TEXT NOT NULL DEFAULT '',
    level      TEXT NOT NULL,
    component  TEXT NOT NULL,
    message    TEXT NOT NULL,
    fields     TEXT NOT NULL DEFAULT '{}',
    timestamp  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_node_logs_mac ON node_logs(node_mac, timestamp);
CREATE INDEX IF NOT EXISTS idx_node_logs_level ON node_logs(level);
CREATE INDEX IF NOT EXISTS idx_node_logs_component ON node_logs(component);
