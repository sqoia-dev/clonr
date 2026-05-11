-- 116_plugin_backups.sql — Sprint 41 Day 4
--
-- Records every pre-render snapshot taken by clustr-privhelper backup-write
-- on the node. Each row represents one tarball written to the node's local
-- filesystem before the corresponding plugin's config push was applied.
--
-- The pending_dangerous_push_id FK links a snapshot to the dangerous-push
-- confirmation that triggered it, enabling:
--
--   clustr restore replay --pending-id <X>
--
-- to find the backup taken just before that dangerous push was applied.
--
-- Retention: the server prunes rows (and instructs the node to delete the
-- tarball) when the per-(node,plugin) count exceeds BackupSpec.MaxBackups.
-- The node-side GC runs in clustr-privhelper after each successful apply.

CREATE TABLE plugin_backups (
    id                       TEXT    PRIMARY KEY,   -- server-issued UUID, pb-<nano>
    node_id                  TEXT    NOT NULL,
    plugin_name              TEXT    NOT NULL,
    blob_path                TEXT    NOT NULL,      -- absolute path to the tarball on the NODE
    taken_at                 INTEGER NOT NULL,      -- unix timestamp; when the snapshot was taken
    pending_dangerous_push_id TEXT   NULL           -- FK to pending_dangerous_pushes.id (nullable)
);

-- List backups for a (node, plugin) pair, newest first.
CREATE INDEX idx_plugin_backups_node_plugin_time
    ON plugin_backups(node_id, plugin_name, taken_at DESC);

-- Look up the backup tied to a specific dangerous-push confirmation.
CREATE INDEX idx_plugin_backups_pending_push
    ON plugin_backups(pending_dangerous_push_id)
    WHERE pending_dangerous_push_id IS NOT NULL;
