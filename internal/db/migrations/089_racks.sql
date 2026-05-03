-- #149: rack model — two-table schema for physical rack inventory and node placement.
-- 088 is reserved for #147.

CREATE TABLE racks (
    id         TEXT    PRIMARY KEY,                -- UUID v4
    name       TEXT    NOT NULL UNIQUE,
    height_u   INTEGER NOT NULL DEFAULT 42,        -- rack U height
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE node_rack_position (
    node_id  TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    rack_id  TEXT NOT NULL REFERENCES racks(id) ON DELETE CASCADE,
    slot_u   INTEGER NOT NULL,                     -- bottom-most U occupied (1 = bottom-most slot)
    height_u INTEGER NOT NULL DEFAULT 1            -- # of U the node occupies
);

CREATE INDEX idx_node_rack_position_rack ON node_rack_position(rack_id);
