-- Migration 064: Full allocation change request workflow (Sprint E, E1, CF-20).
--
-- Promotes pi_expansion_requests to a richer, general-purpose change request table.
-- PIs can request changes of multiple types; admin reviews and decides.
-- Auto-approve of member additions (C.5 behavior) is preserved for request_type='add_member'.
--
-- Existing pi_expansion_requests rows are migrated into allocation_change_requests
-- with request_type='expand_nodes'.

CREATE TABLE IF NOT EXISTS allocation_change_requests (
    id               TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    requester_user_id TEXT NOT NULL REFERENCES users(id),
    request_type     TEXT NOT NULL
                     CHECK(request_type IN (
                         'add_member',
                         'remove_member',
                         'increase_resources',
                         'extend_duration',
                         'archive_project'
                     )),
    -- payload is type-specific JSON:
    --   add_member:         {"username": "jsmith"}
    --   remove_member:      {"username": "jsmith"}
    --   increase_resources: {"resource": "cpu_hours|storage_quota|gpu_count", "current": "...", "requested": "...", "justification": "..."}
    --   extend_duration:    {"current_end": "2026-12-31", "requested_end": "2027-12-31", "justification": "..."}
    --   archive_project:    {"justification": "..."}
    payload          TEXT NOT NULL DEFAULT '{}',
    justification    TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK(status IN ('pending','approved','denied','expired','withdrawn')),
    reviewed_by      TEXT REFERENCES users(id),
    reviewed_at      INTEGER,
    review_notes     TEXT NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER   -- optional expiry; NULL = no auto-expiry
);

CREATE INDEX IF NOT EXISTS idx_acr_project  ON allocation_change_requests(project_id);
CREATE INDEX IF NOT EXISTS idx_acr_status   ON allocation_change_requests(status);
CREATE INDEX IF NOT EXISTS idx_acr_type     ON allocation_change_requests(request_type);
CREATE INDEX IF NOT EXISTS idx_acr_requester ON allocation_change_requests(requester_user_id);

-- Migrate existing pi_expansion_requests into allocation_change_requests.
-- Uses json_object() available in SQLite >= 3.38 (Rocky Linux 9 ships 3.39.x).
INSERT OR IGNORE INTO allocation_change_requests
    (id, project_id, requester_user_id, request_type, payload, justification, status,
     reviewed_by, reviewed_at, review_notes, created_at)
SELECT
    id,
    group_id,
    pi_user_id,
    'increase_resources',
    json_object(
        'resource', 'expand_nodes',
        'justification', justification
    ),
    justification,
    CASE status
        WHEN 'pending'        THEN 'pending'
        WHEN 'acknowledged'   THEN 'approved'
        WHEN 'dismissed'      THEN 'denied'
        ELSE 'pending'
    END,
    resolved_by,
    resolved_at,
    '',
    requested_at
FROM pi_expansion_requests;
