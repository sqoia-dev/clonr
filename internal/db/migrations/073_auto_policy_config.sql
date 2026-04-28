-- Migration 073: Admin-configurable auto-compute allocation policy (H1 policy table).
--
-- One row, singleton pattern (id = 'default'). Operator configures via
-- Settings → Governance → Auto-allocation tab (H2 webui) or API.
--
-- Fields:
--   enabled                 — master toggle (default FALSE per D19 / spec)
--   default_node_count      — how many nodes to pre-assign (default 0 = no auto-assign)
--   default_hardware_profile — JSON string of hardware profile attributes to require
--   default_partition_template — Go template string for Slurm partition name
--                                (default "{{.ProjectSlug}}-compute")
--   default_role            — NodeGroup role to set (default "compute")
--   notify_admins_on_create — email all admins when auto-policy creates a group
--   created_at, updated_at  — bookkeeping

CREATE TABLE IF NOT EXISTS auto_policy_config (
    id                         TEXT    PRIMARY KEY DEFAULT 'default',
    enabled                    INTEGER NOT NULL DEFAULT 0,
    default_node_count         INTEGER NOT NULL DEFAULT 0,
    default_hardware_profile   TEXT    NOT NULL DEFAULT '{}',
    default_partition_template TEXT    NOT NULL DEFAULT '{{.ProjectSlug}}-compute',
    default_role               TEXT    NOT NULL DEFAULT 'compute',
    notify_admins_on_create    INTEGER NOT NULL DEFAULT 0,
    created_at                 INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_at                 INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

-- Seed a disabled default row so reads always succeed without an INSERT.
INSERT OR IGNORE INTO auto_policy_config (id) VALUES ('default');
