-- Add hardware_profile column to node_configs for PXE auto-registration.
-- Stored as a JSON blob; NULL when not yet discovered.
ALTER TABLE node_configs ADD COLUMN hardware_profile TEXT NOT NULL DEFAULT '';

-- Recreate node_configs to make base_image_id nullable.
-- Auto-registered nodes have no image assigned yet; the admin assigns one later.
-- SQLite requires a table recreation to change column constraints.
PRAGMA foreign_keys = OFF;

CREATE TABLE node_configs_new (
    id              TEXT PRIMARY KEY,
    hostname        TEXT NOT NULL,
    fqdn            TEXT NOT NULL DEFAULT '',
    primary_mac     TEXT NOT NULL,
    interfaces      TEXT NOT NULL DEFAULT '[]',
    ssh_keys        TEXT NOT NULL DEFAULT '[]',
    kernel_args     TEXT NOT NULL DEFAULT '',
    groups          TEXT NOT NULL DEFAULT '[]',
    custom_vars     TEXT NOT NULL DEFAULT '{}',
    base_image_id   TEXT REFERENCES base_images(id),
    hardware_profile TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

INSERT INTO node_configs_new
    (id, hostname, fqdn, primary_mac, interfaces, ssh_keys, kernel_args,
     groups, custom_vars, base_image_id, hardware_profile, created_at, updated_at)
SELECT
    id, hostname, fqdn, primary_mac, interfaces, ssh_keys, kernel_args,
    groups, custom_vars, base_image_id, hardware_profile, created_at, updated_at
FROM node_configs;

DROP TABLE node_configs;
ALTER TABLE node_configs_new RENAME TO node_configs;

CREATE UNIQUE INDEX IF NOT EXISTS idx_node_configs_mac ON node_configs(primary_mac);
CREATE INDEX IF NOT EXISTS idx_node_configs_base_image ON node_configs(base_image_id);
CREATE INDEX IF NOT EXISTS idx_node_configs_hostname ON node_configs(hostname);

PRAGMA foreign_keys = ON;
