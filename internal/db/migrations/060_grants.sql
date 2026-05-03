-- Migration 060: Grant tracking (Sprint D, D3-1, CF-12).
--
-- PIs track the grants that fund their research group.
-- Grants are linked to a NodeGroup; admin and director can read all grants.
-- PI can CRUD grants on owned NodeGroups only.

CREATE TABLE IF NOT EXISTS grants (
    id                TEXT PRIMARY KEY,
    node_group_id     TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    title             TEXT NOT NULL DEFAULT '',
    funding_agency    TEXT NOT NULL DEFAULT '',
    grant_number      TEXT NOT NULL DEFAULT '',
    amount            TEXT NOT NULL DEFAULT '',  -- stored as text; currency/formatting is UI concern
    start_date        TEXT NOT NULL DEFAULT '',  -- ISO-8601 date string
    end_date          TEXT NOT NULL DEFAULT '',  -- ISO-8601 date string
    status            TEXT NOT NULL DEFAULT 'active'
                      CHECK(status IN ('active','no_cost_extension','expired','pending')),
    notes             TEXT NOT NULL DEFAULT '',
    created_by_user_id TEXT NOT NULL REFERENCES users(id),
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_grants_group ON grants(node_group_id);
CREATE INDEX IF NOT EXISTS idx_grants_status ON grants(status);
