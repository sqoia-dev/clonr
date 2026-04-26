-- 049_drop_last_deploy_succeeded_at.sql (S6-8)
-- Removes the back-compat dual-write column node_configs.last_deploy_succeeded_at.
-- The canonical field is deploy_completed_preboot_at (ADR-0008, migration 022).
-- The dual-write was introduced to ease the ADR-0008 transition and is no longer
-- needed at v1.0. All readers now use deploy_completed_preboot_at exclusively.

ALTER TABLE node_configs DROP COLUMN last_deploy_succeeded_at;
