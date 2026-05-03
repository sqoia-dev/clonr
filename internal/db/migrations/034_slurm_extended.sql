-- Sprint 4: Extended Slurm module tables (builds, scripts, roles, secrets, upgrades).
--
-- These tables are created in a separate migration to keep 033 minimal and reviewable.
-- Rows appear only when the corresponding features are used.

-- slurm_builds: one row per Slurm version build attempt.
CREATE TABLE IF NOT EXISTS slurm_builds (
    id                   TEXT PRIMARY KEY,
    version              TEXT NOT NULL,
    arch                 TEXT NOT NULL,
    status               TEXT NOT NULL,         -- queued|building|completed|failed
    configure_flags      TEXT NOT NULL,         -- JSON array of extra ./configure flags
    artifact_path        TEXT,
    artifact_checksum    TEXT,
    artifact_size_bytes  INTEGER,
    initiated_by         TEXT,
    log_key              TEXT,
    started_at           INTEGER NOT NULL,
    completed_at         INTEGER,
    error_message        TEXT,
    UNIQUE(version, arch)
);

-- slurm_build_deps: dependency artifacts used in a given Slurm build.
CREATE TABLE IF NOT EXISTS slurm_build_deps (
    id                TEXT PRIMARY KEY,
    build_id          TEXT NOT NULL REFERENCES slurm_builds(id) ON DELETE CASCADE,
    dep_name          TEXT NOT NULL,
    dep_version       TEXT NOT NULL,
    artifact_path     TEXT NOT NULL,
    artifact_checksum TEXT NOT NULL
);

-- slurm_dep_matrix: compatibility matrix between Slurm versions and dependency versions.
-- Seeded from internal/slurm/deps_matrix.json at server startup (INSERT OR IGNORE).
CREATE TABLE IF NOT EXISTS slurm_dep_matrix (
    id                 TEXT PRIMARY KEY,
    slurm_version_min  TEXT NOT NULL,
    slurm_version_max  TEXT NOT NULL,
    dep_name           TEXT NOT NULL,
    dep_version_min    TEXT NOT NULL,
    dep_version_max    TEXT NOT NULL,
    source             TEXT NOT NULL DEFAULT 'bundled',
    created_at         INTEGER NOT NULL
);

-- slurm_secrets: encrypted cluster-level secrets (munge.key, etc.).
CREATE TABLE IF NOT EXISTS slurm_secrets (
    key_type          TEXT PRIMARY KEY,
    encrypted_value   TEXT NOT NULL,
    rotated_at        INTEGER NOT NULL,
    rotated_by        TEXT
);

-- slurm_upgrade_operations: one row per rolling upgrade attempt.
CREATE TABLE IF NOT EXISTS slurm_upgrade_operations (
    id                  TEXT PRIMARY KEY,
    from_build_id       TEXT NOT NULL REFERENCES slurm_builds(id),
    to_build_id         TEXT NOT NULL REFERENCES slurm_builds(id),
    status              TEXT NOT NULL,
    batch_size          INTEGER NOT NULL,
    drain_timeout_min   INTEGER NOT NULL,
    confirmed_db_backup INTEGER NOT NULL DEFAULT 0,
    initiated_by        TEXT,
    phase               TEXT,
    current_batch       INTEGER,
    total_batches       INTEGER,
    started_at          INTEGER NOT NULL,
    completed_at        INTEGER,
    node_results        TEXT
);

-- slurm_scripts: versioned Slurm hook script storage.
CREATE TABLE IF NOT EXISTS slurm_scripts (
    id           TEXT PRIMARY KEY,
    script_type  TEXT NOT NULL,
    version      INTEGER NOT NULL,
    content      TEXT NOT NULL,
    dest_path    TEXT NOT NULL,
    checksum     TEXT NOT NULL,
    authored_by  TEXT,
    message      TEXT,
    created_at   INTEGER NOT NULL,
    UNIQUE(script_type, version)
);

-- slurm_script_state: per-node per-script deployment tracking.
CREATE TABLE IF NOT EXISTS slurm_script_state (
    node_id          TEXT NOT NULL REFERENCES node_configs(id) ON DELETE CASCADE,
    script_type      TEXT NOT NULL,
    deployed_version INTEGER NOT NULL,
    content_hash     TEXT NOT NULL,
    deployed_at      INTEGER NOT NULL,
    push_op_id       TEXT,
    PRIMARY KEY (node_id, script_type)
);

-- slurm_script_config: which scripts are enabled and their dest_path.
CREATE TABLE IF NOT EXISTS slurm_script_config (
    script_type  TEXT PRIMARY KEY,
    dest_path    TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    updated_at   INTEGER NOT NULL
);

-- slurm_node_roles: per-node Slurm role assignment.
CREATE TABLE IF NOT EXISTS slurm_node_roles (
    node_id     TEXT PRIMARY KEY REFERENCES node_configs(id) ON DELETE CASCADE,
    roles       TEXT NOT NULL DEFAULT '[]',
    auto_detect INTEGER NOT NULL DEFAULT 0,
    updated_at  INTEGER NOT NULL
);

-- slurm_node_config_state gains slurm_version column.
-- Tracks installed Slurm binary version per node, set on slurm_binary_ack receipt.
ALTER TABLE slurm_node_config_state ADD COLUMN slurm_version TEXT;
