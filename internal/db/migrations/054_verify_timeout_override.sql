-- Migration 054: per-node verify_timeout_override
-- Adds an optional per-node verify-boot timeout in seconds.
-- NULL means use the global CLUSTR_VERIFY_TIMEOUT setting.
ALTER TABLE node_configs ADD COLUMN verify_timeout_override INTEGER;
