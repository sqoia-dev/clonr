-- Fix node_rack_position FK: 089_racks.sql referenced nodes(id) but the
-- actual table is node_configs. Recreate with the correct FK so rack placements
-- (INSERT into node_rack_position) succeed at runtime with _foreign_keys=on.
PRAGMA foreign_keys = OFF;

CREATE TABLE node_rack_position_new (
    node_id  TEXT PRIMARY KEY REFERENCES node_configs(id) ON DELETE CASCADE,
    rack_id  TEXT NOT NULL REFERENCES racks(id) ON DELETE CASCADE,
    slot_u   INTEGER NOT NULL,
    height_u INTEGER NOT NULL DEFAULT 1
);

INSERT INTO node_rack_position_new SELECT * FROM node_rack_position;

DROP TABLE node_rack_position;
ALTER TABLE node_rack_position_new RENAME TO node_rack_position;

CREATE INDEX IF NOT EXISTS idx_node_rack_position_rack ON node_rack_position(rack_id);

PRAGMA foreign_keys = ON;
