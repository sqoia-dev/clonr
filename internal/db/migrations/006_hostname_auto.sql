-- 006_hostname_auto.sql
-- Tracks whether a node's hostname was auto-generated (1) or admin-set (0).
-- Default 0 so existing rows are treated as admin-set.
ALTER TABLE node_configs ADD COLUMN hostname_auto INTEGER NOT NULL DEFAULT 0;
