-- Migration 062: Annual review cycles (Sprint D, D4-1/D4-2, CF-11 lite).
--
-- Admin creates a review cycle with a deadline. All PI-owned NodeGroups
-- receive a banner and email. PIs respond: affirm (group is active) or archive.
-- Admin views aggregate results.

CREATE TABLE IF NOT EXISTS review_cycles (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    deadline    INTEGER NOT NULL,   -- Unix timestamp
    created_by  TEXT NOT NULL REFERENCES users(id),
    created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS review_responses (
    id            TEXT PRIMARY KEY,
    cycle_id      TEXT NOT NULL REFERENCES review_cycles(id) ON DELETE CASCADE,
    node_group_id TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    pi_user_id    TEXT REFERENCES users(id),
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK(status IN ('pending','affirmed','archive_requested','no_response')),
    notes         TEXT NOT NULL DEFAULT '',
    responded_at  INTEGER,
    UNIQUE(cycle_id, node_group_id)
);

CREATE INDEX IF NOT EXISTS idx_review_responses_cycle ON review_responses(cycle_id);
CREATE INDEX IF NOT EXISTS idx_review_responses_status ON review_responses(status);
