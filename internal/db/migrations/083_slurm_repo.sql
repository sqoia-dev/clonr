-- Migration 083: clustr-internal-repo support.
--
-- Adds per-node deployed Slurm version tracking (replaces the global active_build_id
-- flag for upgrade validation — truth is per-node, not cluster-global).
-- The slurm_secrets table already exists; the new GPG key rows use it unchanged.
-- Adds repo_gpg_public_key column to slurm_module_config for the per-cluster
-- GPG key used to sign and verify clustr-internal-repo RPMs.

-- Per-node deployed Slurm version, set when a node acks a successful dnf upgrade
-- or artifact install. NULL means no Slurm has been deployed by clustr yet.
-- Column lives on slurm_node_config_state because that table already tracks
-- per-node Slurm state (slurm_version was added in 034_slurm_extended.sql).
-- We use a separate table to avoid coupling to config-push operations.
CREATE TABLE IF NOT EXISTS slurm_node_version (
    node_id           TEXT PRIMARY KEY REFERENCES node_configs(id) ON DELETE CASCADE,
    deployed_version  TEXT NOT NULL,           -- e.g. "25.11.5"
    build_id          TEXT,                    -- clustr build UUID, NULL for fallback installs
    install_method    TEXT NOT NULL DEFAULT 'dnf', -- 'dnf' | 'artifact'
    installed_at      INTEGER NOT NULL,
    installed_by      TEXT NOT NULL DEFAULT 'clustr-server'
);

-- GPG key columns on slurm_module_config.
-- repo_gpg_public_key: ASCII-armored GPG public key for clustr-internal-repo signing.
-- Generated once at slurm-init time; rotated by operator action only.
-- NULL on existing rows (no key generated yet).
ALTER TABLE slurm_module_config ADD COLUMN repo_gpg_public_key TEXT;
ALTER TABLE slurm_module_config ADD COLUMN repo_gpg_key_id TEXT;
