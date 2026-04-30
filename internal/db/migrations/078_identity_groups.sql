-- migration 078: identity surface group data models (Sprint 7)
--
-- clustr_group_overlays: supplementary memberships on LDAP groups (GRP-OVERLAY-1).
--   Operator adds clustr-managed users to LDAP groups without writing the directory.
--   At deploy time, /etc/group on the node merges LDAP-native + overlay members.
--
-- clustr_specialty_groups: clustr-only groups with no LDAP backing (GRP-SPECIALTY-1).
--   Full CRUD. Deployed alongside system accounts via the cloning pipeline.

CREATE TABLE IF NOT EXISTS clustr_group_overlays (
    group_dn        TEXT NOT NULL,        -- LDAP group DN (e.g. cn=users,ou=groups,dc=cluster,dc=local)
    user_identifier TEXT NOT NULL,        -- uid (LDAP) or clustr username (local)
    source          TEXT NOT NULL DEFAULT 'local', -- "ldap" | "local"
    added_at        INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    added_by        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (group_dn, user_identifier)
);

CREATE INDEX IF NOT EXISTS idx_group_overlays_group_dn ON clustr_group_overlays(group_dn);

CREATE TABLE IF NOT EXISTS clustr_specialty_groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    gid_number  INTEGER NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE TABLE IF NOT EXISTS clustr_specialty_group_members (
    group_id        TEXT NOT NULL REFERENCES clustr_specialty_groups(id) ON DELETE CASCADE,
    user_identifier TEXT NOT NULL,
    source          TEXT NOT NULL DEFAULT 'local',
    PRIMARY KEY (group_id, user_identifier)
);

CREATE INDEX IF NOT EXISTS idx_specialty_group_members_group ON clustr_specialty_group_members(group_id);
