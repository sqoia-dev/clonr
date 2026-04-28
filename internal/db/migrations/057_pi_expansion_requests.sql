-- Migration 057: PI expansion requests (C5-3-3).
--
-- PIs can request additional nodes for their NodeGroup via the portal.
-- Admin reviews and acts manually — no automatic node assignment.

CREATE TABLE IF NOT EXISTS pi_expansion_requests (
    id           TEXT PRIMARY KEY,
    group_id     TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    pi_user_id   TEXT NOT NULL REFERENCES users(id),
    justification TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK(status IN ('pending','acknowledged','dismissed')),
    requested_at INTEGER NOT NULL,
    resolved_at  INTEGER,
    resolved_by  TEXT   -- user_id of admin who acknowledged/dismissed
);

CREATE INDEX IF NOT EXISTS idx_pi_expansion_requests_group ON pi_expansion_requests(group_id);
CREATE INDEX IF NOT EXISTS idx_pi_expansion_requests_status ON pi_expansion_requests(status);
