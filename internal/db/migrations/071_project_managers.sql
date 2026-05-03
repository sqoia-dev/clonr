-- Migration 071: PI manager delegation (Sprint G, v1.6.0 / CF-09 manager scope).
--
-- project_managers is a join table: a PI can delegate management rights on their
-- NodeGroup to one or more users. Delegated managers have the same per-project
-- rights as the PI (member mgmt, utilization, allocation requests, expiration set)
-- but are NOT the project owner.
--
-- granted_by_user_id must be the PI who owns the group or an admin.
-- A manager does NOT need the 'pi' RBAC role; the portal middleware checks this table.

CREATE TABLE IF NOT EXISTS project_managers (
    id                  TEXT PRIMARY KEY,
    node_group_id       TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    user_id             TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    granted_by_user_id  TEXT NOT NULL REFERENCES users(id),
    granted_at          INTEGER NOT NULL,
    UNIQUE(node_group_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_project_managers_group  ON project_managers(node_group_id);
CREATE INDEX IF NOT EXISTS idx_project_managers_user   ON project_managers(user_id);
