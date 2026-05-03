-- 039_bmc_credential_encryption.sql — BMC/power credential encryption at rest (S1-16, D4)
--
-- Adds boolean flag columns to track whether node_configs.bmc_config and
-- node_configs.power_provider JSON blobs are stored as AES-256-GCM ciphertext.
-- The encrypted columns wrap the entire JSON so passwords inside are never
-- visible in the SQLite file. Encryption is transparent: read decrypts,
-- write encrypts. UI shows plaintext fields normally.
--
-- Migration: on first start after upgrade, all existing plaintext rows with
-- non-empty bmc_config or power_provider are re-encrypted by MigrateBMCCredentials().
ALTER TABLE node_configs ADD COLUMN bmc_config_encrypted INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_configs ADD COLUMN power_provider_encrypted INTEGER NOT NULL DEFAULT 0;
