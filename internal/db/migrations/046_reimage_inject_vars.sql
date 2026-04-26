-- 046_reimage_inject_vars.sql — S4-11: per-deployment custom_vars overrides.
--
-- inject_vars: JSON object merged with node custom_vars at deploy time.
-- Not persisted to node_configs — ephemeral for this reimage only.
ALTER TABLE reimage_requests ADD COLUMN inject_vars TEXT NOT NULL DEFAULT '{}';
