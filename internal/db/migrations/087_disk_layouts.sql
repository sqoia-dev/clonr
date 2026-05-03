-- migration 087: disk layout as a first-class object (#146)
--
-- disk_layouts stores named, reusable partition/FS/RAID layouts that can be
-- assigned to a node group (default for the group) or to an individual node
-- (per-node override).  The layout_json column carries the full api.DiskLayout
-- JSON: partitions, bootloader, optional RAID arrays, optional ZFS pools.
--
-- Precedence during deploy (highest to lowest):
--   1. node.disk_layout_id   (per-node override)
--   2. node_groups.disk_layout_id  (group default)
--   3. existing recommendation path (DiskLayoutOverride JSON / image default)
--
-- source_node_id is nullable — set when the layout was captured from a live
-- node; NULL for hand-authored layouts.

CREATE TABLE IF NOT EXISTS disk_layouts (
    id              TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL UNIQUE,
    source_node_id  TEXT,               -- nullable; node it was captured from
    captured_at     INTEGER NOT NULL,   -- unix seconds
    layout_json     TEXT    NOT NULL,   -- JSON-serialised api.DiskLayout
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_disk_layouts_source ON disk_layouts(source_node_id);

-- FK columns on existing tables (nullable in both cases).
ALTER TABLE node_groups ADD COLUMN disk_layout_id TEXT REFERENCES disk_layouts(id);
ALTER TABLE node_configs ADD COLUMN disk_layout_id TEXT REFERENCES disk_layouts(id);
