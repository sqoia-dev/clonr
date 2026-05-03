-- Migration 061: Publication tracking (Sprint D, D3-2, CF-13).
--
-- PIs track publications produced by their research group.
-- DOI is optional; metadata can be auto-filled via CrossRef when DOI is provided.
-- Publications are linked to a NodeGroup; admin and director can read all.

CREATE TABLE IF NOT EXISTS publications (
    id                TEXT PRIMARY KEY,
    node_group_id     TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    doi               TEXT NOT NULL DEFAULT '',   -- empty for manual entries
    title             TEXT NOT NULL DEFAULT '',
    authors           TEXT NOT NULL DEFAULT '',   -- comma-separated or free text
    journal           TEXT NOT NULL DEFAULT '',
    year              INTEGER NOT NULL DEFAULT 0,
    created_by_user_id TEXT NOT NULL REFERENCES users(id),
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_publications_group ON publications(node_group_id);
CREATE INDEX IF NOT EXISTS idx_publications_doi ON publications(doi) WHERE doi != '';
