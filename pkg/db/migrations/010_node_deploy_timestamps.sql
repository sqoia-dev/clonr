-- Lifecycle timestamps for tracking per-node deploy outcomes.
-- Used by NodeConfig.State() to compute the current state of a node
-- without storing a redundant state column.
-- last_deploy_succeeded_at: Unix timestamp of most recent successful finalize.
-- last_deploy_failed_at:    Unix timestamp of most recent failed deploy.
ALTER TABLE node_configs ADD COLUMN last_deploy_succeeded_at INTEGER;
ALTER TABLE node_configs ADD COLUMN last_deploy_failed_at    INTEGER;
