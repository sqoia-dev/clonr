-- 113_roles_and_assignments.sql — Sprint 41 Day 1
--
-- RBAC foundation: roles table, role_assignments table, and the users.groups
-- column for caching LDAP posix group memberships at session-creation time.
--
-- The legacy users.role column is preserved for one release as a backstop.
-- Sprint 43 drops it after role_assignments is fully authoritative.

-- ── roles ──────────────────────────────────────────────────────────────────

CREATE TABLE roles (
    id               TEXT    PRIMARY KEY,
    name             TEXT    UNIQUE NOT NULL,
    permissions_json TEXT    NOT NULL DEFAULT '{}',
    is_builtin       INTEGER NOT NULL DEFAULT 0,   -- 1 = system role, cannot be deleted via API
    created_at       INTEGER NOT NULL
);

-- ── role_assignments ────────────────────────────────────────────────────────

CREATE TABLE role_assignments (
    id           TEXT    PRIMARY KEY,
    role_id      TEXT    NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    subject_kind TEXT    NOT NULL CHECK (subject_kind IN ('user', 'posix_group')),
    subject_id   TEXT    NOT NULL,                 -- users.id when kind=user; posix CN when kind=posix_group
    created_at   INTEGER NOT NULL,
    UNIQUE(role_id, subject_kind, subject_id)
);

CREATE INDEX idx_role_assignments_subject ON role_assignments(subject_kind, subject_id);

-- ── users.groups ────────────────────────────────────────────────────────────
-- Caches the posix CNs from LDAP memberOf at session-creation time.
-- Stored as a JSON array of strings, e.g. '["cluster-ops","hpc-admins"]'.
-- Re-read on each new session; no live refresh during an active session.
-- NULL means the cache is empty / the user has no LDAP group memberships.

ALTER TABLE users ADD COLUMN groups_json TEXT;

-- ── Seed built-in roles ──────────────────────────────────────────────────────
--
-- permissions_json is the canonical truth for the new RBAC path.
-- The legacy users.role column remains authoritative for one release.
--
-- Permission verbs (dot-delimited resource.action):
--   *           — wildcard: grants everything (admin only)
--   node.read   — list/get nodes
--   node.write  — create/update nodes
--   node.reimage — trigger reimage
--   image.create — create base images
--   user.write  — create/update/delete users

INSERT INTO roles (id, name, permissions_json, is_builtin, created_at) VALUES
    ('role-admin',    'admin',    '{"*":true}',
     1, strftime('%s','now')),
    ('role-operator', 'operator', '{"node.read":true,"node.write":true,"node.reimage":true}',
     1, strftime('%s','now')),
    ('role-viewer',   'viewer',   '{"node.read":true}',
     1, strftime('%s','now'));

-- ── Backfill: assign every existing user to the matching built-in role ────────
--
-- The users.role column uses: 'admin', 'operator', 'readonly', 'viewer', 'pi',
-- 'director'. Map to the three new roles:
--   admin     → role-admin
--   operator  → role-operator
--   readonly / viewer / pi / director → role-viewer
--
-- users with an unrecognised or NULL role are skipped (no assignment created);
-- the legacy fallback in auth.ResolveRoles handles them for one release.

INSERT INTO role_assignments (id, role_id, subject_kind, subject_id, created_at)
    SELECT
        lower(hex(randomblob(16))),
        CASE role
            WHEN 'admin'    THEN 'role-admin'
            WHEN 'operator' THEN 'role-operator'
            ELSE                 'role-viewer'
        END,
        'user',
        id,
        strftime('%s','now')
    FROM users
    WHERE role IN ('admin', 'operator', 'readonly', 'viewer', 'pi', 'director');
