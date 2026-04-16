-- 012_extra_mounts.sql
-- Adds extra_mounts JSON columns to node_configs and node_groups.
--
-- extra_mounts stores a JSON array of FstabEntry objects. The server merges
-- group and node entries at deploy time (group mounts form the base; node
-- mounts override by mount_point or append).

ALTER TABLE node_configs ADD COLUMN extra_mounts TEXT NOT NULL DEFAULT '[]';
ALTER TABLE node_groups  ADD COLUMN extra_mounts TEXT NOT NULL DEFAULT '[]';
