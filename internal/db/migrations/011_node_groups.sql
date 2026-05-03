-- 011_node_groups.sql
-- Adds node_groups table and layout override columns to node_configs.
--
-- Layout resolution hierarchy (highest → lowest priority):
--   1. node_configs.disk_layout_override  (node-level)
--   2. node_groups.disk_layout            (group-level)
--   3. base_images.disk_layout            (image default)

CREATE TABLE IF NOT EXISTS node_groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    -- disk_layout stores the group-level DiskLayout override as JSON.
    -- Empty object '{}' means "use image default" (no override).
    disk_layout TEXT NOT NULL DEFAULT '{}',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_node_groups_name ON node_groups(name);

-- Add group membership to node_configs.
-- group_id is a soft FK (SQLite does not enforce FK on ALTER ADD COLUMN easily,
-- but _foreign_keys=on is set on the connection so we use a proper REFERENCES).
ALTER TABLE node_configs ADD COLUMN group_id TEXT REFERENCES node_groups(id);

-- Add node-level disk layout override.
-- Empty object '{}' means "no override" — image/group layout applies.
ALTER TABLE node_configs ADD COLUMN disk_layout_override TEXT NOT NULL DEFAULT '{}';

-- Note: extra_mounts columns are added in migration 012_extra_mounts.sql.
