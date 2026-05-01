-- migration 084: split POSIX ID allocator into per-role ranges (#113)
--
-- posixid_role_ranges replaces the single-row posixid_config as the authoritative
-- allocation config. Each role gets its own UID/GID range and reserved sets.
--
-- Two roles are seeded:
--   ldap_user      — human LDAP accounts: 10000-60000 (reserved 0-9999)
--   system_account — daemon/service accounts: 200-999   (reserved 0-199, matching
--                    distro RPM/DNF-managed UIDs so clustr never collides with them)
--
-- Migration 081's posixid_config table is left untouched for legacy reads.
-- New allocations go through posixid_role_ranges only.

CREATE TABLE IF NOT EXISTS posixid_role_ranges (
    role                 TEXT    PRIMARY KEY,
    uid_min              INTEGER NOT NULL,
    uid_max              INTEGER NOT NULL,
    gid_min              INTEGER NOT NULL,
    gid_max              INTEGER NOT NULL,
    reserved_uid_ranges  TEXT    NOT NULL DEFAULT '[]',
    reserved_gid_ranges  TEXT    NOT NULL DEFAULT '[]'
);

INSERT OR IGNORE INTO posixid_role_ranges
    (role, uid_min, uid_max, gid_min, gid_max, reserved_uid_ranges, reserved_gid_ranges)
VALUES
    ('ldap_user',      10000, 60000, 10000, 60000, '[[0,9999]]',  '[[0,9999]]'),
    ('system_account', 200,   999,   200,   999,   '[[0,199]]',   '[[0,199]]');
