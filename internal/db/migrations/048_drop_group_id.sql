-- 048_drop_group_id.sql (S6-6)
-- Removes the denormalised fast-path node_configs.group_id column.
-- The authoritative source of a node's primary group is now exclusively
-- node_group_memberships WHERE is_primary = 1 (established in migration 042).
--
-- BUG-1 fix (Sprint 0) remains intact — the NodeGroup cleared-on-PUT bug was
-- fixed in the handler layer, which now writes through node_group_memberships
-- and never touches the column being dropped here.
--
-- SQLite does not support DROP COLUMN in versions < 3.35.0. The server
-- requires SQLite 3.35+ (enforced at startup since migration 041 which uses
-- ALTER TABLE RENAME COLUMN). Using the same approach here is safe.

ALTER TABLE node_configs DROP COLUMN group_id;
