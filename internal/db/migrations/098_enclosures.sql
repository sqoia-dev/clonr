-- 098_enclosures.sql (#231 Sprint 31 — chassis enclosures, Path A)
--
-- Adds multi-node enclosure support to the datacenter model.
-- Existing node_rack_position rows are untouched — NULL enclosure_id / slot_index
-- is valid (rack-direct placement) and passes the XOR trigger.
--
-- XOR invariant (exactly one parent):
--   rack_id IS NOT NULL AND enclosure_id IS NULL     → rack-direct node
--   rack_id IS NULL     AND enclosure_id IS NOT NULL → enclosure-resident node
--   both NULL or both NOT NULL → RAISE(ABORT, …)

-- ─── enclosures table ─────────────────────────────────────────────────────────

CREATE TABLE enclosures (
    id          TEXT    PRIMARY KEY,                          -- UUID v4
    rack_id     TEXT    NOT NULL REFERENCES racks(id) ON DELETE CASCADE,
    rack_slot_u INTEGER NOT NULL,                             -- bottom-most U the chassis occupies
    height_u    INTEGER NOT NULL,                             -- how many U the chassis occupies
    type_id     TEXT    NOT NULL,                             -- key into the Go canned catalog
    label       TEXT,                                         -- operator-supplied chassis name (optional)
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE INDEX idx_enclosures_rack ON enclosures(rack_id);

-- ─── node_rack_position: add enclosure columns ────────────────────────────────
--
-- Existing rows have rack_id IS NOT NULL, so the two new columns default to NULL
-- and satisfy the trigger's "rack-direct" branch automatically.

ALTER TABLE node_rack_position ADD COLUMN enclosure_id TEXT
    REFERENCES enclosures(id) ON DELETE CASCADE;
ALTER TABLE node_rack_position ADD COLUMN slot_index INTEGER;

CREATE INDEX idx_node_rack_position_enclosure ON node_rack_position(enclosure_id)
    WHERE enclosure_id IS NOT NULL;

-- ─── XOR trigger: INSERT ─────────────────────────────────────────────────────

CREATE TRIGGER node_rack_position_xor_parent_insert
BEFORE INSERT ON node_rack_position
BEGIN
    SELECT CASE
        WHEN (NEW.rack_id IS NOT NULL AND NEW.enclosure_id IS NOT NULL)
          OR (NEW.rack_id IS NULL     AND NEW.enclosure_id IS NULL)
        THEN RAISE(ABORT, 'node_rack_position: exactly one of rack_id/enclosure_id required')
    END;
    SELECT CASE
        WHEN NEW.enclosure_id IS NOT NULL AND NEW.slot_index IS NULL
        THEN RAISE(ABORT, 'node_rack_position: slot_index required when enclosure_id is set')
    END;
    SELECT CASE
        WHEN NEW.rack_id IS NOT NULL AND NEW.slot_u IS NULL
        THEN RAISE(ABORT, 'node_rack_position: slot_u required when rack_id is set')
    END;
END;

-- ─── XOR trigger: UPDATE ─────────────────────────────────────────────────────

CREATE TRIGGER node_rack_position_xor_parent_update
BEFORE UPDATE ON node_rack_position
BEGIN
    SELECT CASE
        WHEN (NEW.rack_id IS NOT NULL AND NEW.enclosure_id IS NOT NULL)
          OR (NEW.rack_id IS NULL     AND NEW.enclosure_id IS NULL)
        THEN RAISE(ABORT, 'node_rack_position: exactly one of rack_id/enclosure_id required')
    END;
    SELECT CASE
        WHEN NEW.enclosure_id IS NOT NULL AND NEW.slot_index IS NULL
        THEN RAISE(ABORT, 'node_rack_position: slot_index required when enclosure_id is set')
    END;
    SELECT CASE
        WHEN NEW.rack_id IS NOT NULL AND NEW.slot_u IS NULL
        THEN RAISE(ABORT, 'node_rack_position: slot_u required when rack_id is set')
    END;
END;
