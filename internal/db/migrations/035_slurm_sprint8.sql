-- Sprint 8: Slurm build pipeline additions.
-- Adds active_build_id to the module config singleton and fills out the
-- slurm_module_config table with the new column if it doesn't exist.

-- Track which Slurm build is currently active (deployed to the cluster).
ALTER TABLE slurm_module_config ADD COLUMN active_build_id TEXT REFERENCES slurm_builds(id);
