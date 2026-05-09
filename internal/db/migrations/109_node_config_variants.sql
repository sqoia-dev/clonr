-- migration 109: Sprint 44 VARIANTS-SYSTEM
--
-- Per-attribute overlays for NodeConfig. A variant is a (scope, attribute_path,
-- value_json) triple that overrides the "base" NodeConfig field at resolve
-- time.  Resolver applies variants in priority order:
--
--     role        (lowest)
--     group
--     node-direct (highest)
--
-- Higher-priority variants overwrite lower-priority ones for the same
-- attribute_path.  Direct fields on the node row remain the floor — variants
-- only take effect when they have something to say.
--
-- scope_kind   what scope_id refers to:
--     "global"  — applied to every node (scope_id = "")
--     "group"   — applied to nodes whose group_id matches scope_id
--     "role"    — applied to nodes whose role tag matches scope_id (string match)
--
-- attribute_path is a dotted JSON-pointer-style path: "kernel_args",
-- "interfaces[0].ip", "bmc.username".  The resolver implementation in
-- internal/db/node_variants.go interprets the path against api.NodeConfig.
--
-- value_json is the raw JSON value to splice in at attribute_path. Stored as
-- TEXT so we can carry arbitrary types without a separate column-per-type
-- table.

CREATE TABLE IF NOT EXISTS node_config_variants (
    id              TEXT    PRIMARY KEY,        -- UUID
    node_id         TEXT,                       -- nullable; when NULL the variant is scope-keyed only
    attribute_path  TEXT    NOT NULL,
    value_json      TEXT    NOT NULL,
    scope_kind      TEXT    NOT NULL,           -- 'global' | 'group' | 'role'
    scope_id        TEXT    NOT NULL,           -- "" for global, group_id for group, role label for role
    created_at      INTEGER NOT NULL,           -- unix seconds
    CHECK (scope_kind IN ('global', 'group', 'role'))
);

-- Resolver lookup: enumerate all variants in (scope_kind, scope_id) order.
CREATE INDEX IF NOT EXISTS idx_node_config_variants_scope
    ON node_config_variants (scope_kind, scope_id, attribute_path);

-- Per-node override lookup (scope_kind='global' AND node_id=?).
CREATE INDEX IF NOT EXISTS idx_node_config_variants_node
    ON node_config_variants (node_id);
