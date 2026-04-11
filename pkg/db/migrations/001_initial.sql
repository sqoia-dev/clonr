CREATE TABLE IF NOT EXISTS base_images (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    version         TEXT NOT NULL,
    os              TEXT NOT NULL,
    arch            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'building',
    format          TEXT NOT NULL,
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    checksum        TEXT NOT NULL DEFAULT '',
    blob_path       TEXT NOT NULL DEFAULT '',
    disk_layout     TEXT NOT NULL DEFAULT '{}',
    tags            TEXT NOT NULL DEFAULT '[]',
    source_url      TEXT NOT NULL DEFAULT '',
    notes           TEXT NOT NULL DEFAULT '',
    error_message   TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    finalized_at    INTEGER
);

CREATE INDEX IF NOT EXISTS idx_base_images_status ON base_images(status);
CREATE INDEX IF NOT EXISTS idx_base_images_name ON base_images(name, version);

CREATE TABLE IF NOT EXISTS node_configs (
    id              TEXT PRIMARY KEY,
    hostname        TEXT NOT NULL,
    fqdn            TEXT NOT NULL,
    primary_mac     TEXT NOT NULL,
    interfaces      TEXT NOT NULL DEFAULT '[]',
    ssh_keys        TEXT NOT NULL DEFAULT '[]',
    kernel_args     TEXT NOT NULL DEFAULT '',
    groups          TEXT NOT NULL DEFAULT '[]',
    custom_vars     TEXT NOT NULL DEFAULT '{}',
    base_image_id   TEXT NOT NULL REFERENCES base_images(id),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_node_configs_mac ON node_configs(primary_mac);
CREATE INDEX IF NOT EXISTS idx_node_configs_base_image ON node_configs(base_image_id);
CREATE INDEX IF NOT EXISTS idx_node_configs_hostname ON node_configs(hostname);
