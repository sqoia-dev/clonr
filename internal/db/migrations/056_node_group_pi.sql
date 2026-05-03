-- Migration 056: NodeGroup PI ownership + PI member requests.
--
-- Adds pi_user_id nullable FK on node_groups: one PI owns a NodeGroup;
-- one PI can own multiple NodeGroups. Admin assigns PI; PI cannot transfer.
--
-- Also creates pi_member_requests table for the pending-approval workflow
-- when CLUSTR_PI_AUTO_APPROVE=false (the default).

-- PI ownership on node groups.
ALTER TABLE node_groups ADD COLUMN pi_user_id TEXT REFERENCES users(id);

-- PI member-add requests (pending or resolved).
CREATE TABLE IF NOT EXISTS pi_member_requests (
    id           TEXT PRIMARY KEY,
    group_id     TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    pi_user_id   TEXT NOT NULL REFERENCES users(id),
    -- ldap_username is the HPC account username being added to the group.
    ldap_username TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK(status IN ('pending','approved','denied')),
    requested_at INTEGER NOT NULL,
    resolved_at  INTEGER,
    resolved_by  TEXT,   -- user_id of admin who approved/denied
    note         TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_pi_member_requests_group ON pi_member_requests(group_id);
CREATE INDEX IF NOT EXISTS idx_pi_member_requests_status ON pi_member_requests(status);
