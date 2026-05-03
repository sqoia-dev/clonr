-- 043_user_group_memberships.sql (S3-1)
-- RBAC: user→group operator assignments.
-- Operators may be granted access to one or more NodeGroups; this table tracks
-- those assignments. Admins bypass this table (full access via requireScope).
-- Readonly users are never granted group access.
--
-- role is always 'operator' in v1.0 (no per-group-role variation yet).
-- The (user_id, group_id) pair is the natural PK; a user can only have one
-- role per group. Foreign keys cascade on delete so removing a user or group
-- automatically cleans up membership rows.

CREATE TABLE IF NOT EXISTS user_group_memberships (
    user_id  TEXT NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    role     TEXT NOT NULL CHECK(role IN ('operator')),
    PRIMARY KEY (user_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_ugm_user  ON user_group_memberships(user_id);
CREATE INDEX IF NOT EXISTS idx_ugm_group ON user_group_memberships(group_id);
