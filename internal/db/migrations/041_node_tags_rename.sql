-- 041_node_tags_rename.sql (S2-4)
-- Renames node_configs.groups column to node_configs.tags.
-- Dual JSON emission is handled in Go code for one release (backward compat).
-- SQLite does not support RENAME COLUMN in older versions, but modernc.org/sqlite
-- targets SQLite 3.35+ which added ALTER TABLE RENAME COLUMN support.

ALTER TABLE node_configs RENAME COLUMN groups TO tags;
