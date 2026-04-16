-- 020_group_memberships.sql
-- Adds explicit many-to-many node_group_memberships table for group-targeted
-- bulk operations (rolling reimage) and a role column on node_groups.
-- Also adds group_reimage_jobs for tracking rolling reimage job state.
--
-- The existing group_id column on node_configs is retained for single-group
-- fast-path lookups. Memberships table enables multi-group membership and
-- explicit add/remove operations with concurrent tracking.

-- Add role column to node_groups (NULL = unclassified).
-- Valid values: 'compute', 'login', 'storage', 'gpu', 'admin', or NULL.
ALTER TABLE node_groups ADD COLUMN role TEXT;

-- Explicit many-to-many membership table.
CREATE TABLE IF NOT EXISTS node_group_memberships (
    node_id  TEXT NOT NULL REFERENCES node_configs(id) ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES node_groups(id)  ON DELETE CASCADE,
    PRIMARY KEY (node_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_node_group_memberships_group ON node_group_memberships(group_id);
CREATE INDEX IF NOT EXISTS idx_node_group_memberships_node  ON node_group_memberships(node_id);

-- Back-fill memberships from the existing group_id FK on node_configs.
-- Any node that already has group_id set gets a corresponding membership row.
INSERT OR IGNORE INTO node_group_memberships (node_id, group_id)
SELECT id, group_id FROM node_configs WHERE group_id IS NOT NULL AND group_id != '';

-- Rolling reimage jobs — one row per POST /node-groups/:id/reimage invocation.
CREATE TABLE IF NOT EXISTS group_reimage_jobs (
    id                   TEXT PRIMARY KEY,
    group_id             TEXT NOT NULL REFERENCES node_groups(id),
    image_id             TEXT NOT NULL,
    concurrency          INTEGER NOT NULL DEFAULT 5,
    pause_on_failure_pct INTEGER NOT NULL DEFAULT 20,
    -- status: 'running', 'paused', 'complete', 'failed'
    status               TEXT NOT NULL DEFAULT 'running',
    total_nodes          INTEGER NOT NULL DEFAULT 0,
    triggered_nodes      INTEGER NOT NULL DEFAULT 0,
    succeeded_nodes      INTEGER NOT NULL DEFAULT 0,
    failed_nodes         INTEGER NOT NULL DEFAULT 0,
    error_message        TEXT NOT NULL DEFAULT '',
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_group_reimage_jobs_group ON group_reimage_jobs(group_id);
