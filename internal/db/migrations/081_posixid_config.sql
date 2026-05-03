-- migration 081: POSIX ID allocator config (Sprint 13 #96)
--
-- posixid_config holds a single row (id=1) with the UID/GID allocation ranges
-- and reserved ranges. The allocator reads this row to find the next available
-- UID or GID, and validates that manually specified IDs fall within policy.
--
-- reserved_uid_ranges and reserved_gid_ranges are JSON arrays of [min, max] pairs.
-- Default reserved ranges:
--   [0, 999]   — traditional UNIX system accounts
--   [1000, 9999] — distro-typical service account range (sssd, munge, slurm, etc.)
-- Operator-configurable IDs therefore start at 10000.

CREATE TABLE IF NOT EXISTS posixid_config (
    id                   INTEGER PRIMARY KEY CHECK (id = 1),
    uid_min              INTEGER NOT NULL DEFAULT 10000,
    uid_max              INTEGER NOT NULL DEFAULT 60000,
    gid_min              INTEGER NOT NULL DEFAULT 10000,
    gid_max              INTEGER NOT NULL DEFAULT 60000,
    reserved_uid_ranges  TEXT    NOT NULL DEFAULT '[[0,999],[1000,9999]]',
    reserved_gid_ranges  TEXT    NOT NULL DEFAULT '[[0,999],[1000,9999]]',
    updated_at           INTEGER NOT NULL DEFAULT 0
);

-- Seed default row.
INSERT OR IGNORE INTO posixid_config (id, uid_min, uid_max, gid_min, gid_max,
    reserved_uid_ranges, reserved_gid_ranges, updated_at)
VALUES (1, 10000, 60000, 10000, 60000,
    '[[0,999],[1000,9999]]', '[[0,999],[1000,9999]]', 0);
