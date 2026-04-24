-- Sprint 4: Slurm module core tables.
--
-- slurm_module_config: singleton (id=1). Stores module enable state and
-- cluster-level settings. Mirrors the pattern of ldap_module_config.
CREATE TABLE IF NOT EXISTS slurm_module_config (
    id              INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled         INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'not_configured',  -- not_configured|ready|disabled|error
    cluster_name    TEXT,
    managed_files   TEXT NOT NULL DEFAULT '["slurm.conf","gres.conf","cgroup.conf","topology.conf","plugstack.conf"]',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- slurm_push_operations must be created before slurm_node_config_state
-- because slurm_node_config_state has a FK into it.
CREATE TABLE IF NOT EXISTS slurm_push_operations (
    id               TEXT PRIMARY KEY,
    filenames        TEXT NOT NULL,          -- JSON array
    file_versions    TEXT NOT NULL,          -- JSON map {filename: version_int}
    initiated_by     TEXT,
    apply_action     TEXT NOT NULL,          -- "reconfigure" or "restart"
    status           TEXT NOT NULL,          -- pending|in_progress|completed|partial|failed
    node_count       INTEGER NOT NULL,
    success_count    INTEGER NOT NULL DEFAULT 0,
    failure_count    INTEGER NOT NULL DEFAULT 0,
    started_at       INTEGER NOT NULL,
    completed_at     INTEGER,
    node_results     TEXT                    -- JSON map {node_id: {ok, error, file_results}}
);

-- slurm_config_files: versioned config file storage.
-- One row per file per version. The "current" version is MAX(version) for a given filename.
CREATE TABLE IF NOT EXISTS slurm_config_files (
    id           TEXT PRIMARY KEY,
    filename     TEXT NOT NULL,
    version      INTEGER NOT NULL,
    content      TEXT NOT NULL,
    is_template  INTEGER NOT NULL DEFAULT 0,
    checksum     TEXT NOT NULL,
    authored_by  TEXT,
    message      TEXT,
    created_at   INTEGER NOT NULL,
    UNIQUE(filename, version)
);

-- slurm_node_overrides: per-node hardware parameters and GRES data.
CREATE TABLE IF NOT EXISTS slurm_node_overrides (
    id           TEXT PRIMARY KEY,
    node_id      TEXT NOT NULL REFERENCES node_configs(id) ON DELETE CASCADE,
    override_key TEXT NOT NULL,
    value        TEXT NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE(node_id, override_key)
);

-- slurm_node_config_state: per-node per-file sync tracking.
-- Updated on successful push ack. Source of truth for drift detection.
CREATE TABLE IF NOT EXISTS slurm_node_config_state (
    node_id          TEXT NOT NULL REFERENCES node_configs(id) ON DELETE CASCADE,
    filename         TEXT NOT NULL,
    deployed_version INTEGER NOT NULL,
    content_hash     TEXT NOT NULL,
    deployed_at      INTEGER NOT NULL,
    push_op_id       TEXT REFERENCES slurm_push_operations(id),
    PRIMARY KEY (node_id, filename)
);
