-- 053: Add viewer role support.
--
-- The viewer role is the 4th RBAC role (admin / operator / readonly / viewer).
-- It is more restricted than readonly: viewers only access /portal/ and cannot
-- reach /admin/ or any admin API. LDAP self-service password change is the only
-- mutation available to viewers (their own account only).
--
-- This migration is additive — the users table already uses a TEXT role column
-- with no CHECK constraint, so no schema change is needed. The role value 'viewer'
-- is now a valid value at the application layer.
--
-- Also stores the OnDemand URL and LDAP quota attribute configuration used by
-- the researcher portal (/portal/).

CREATE TABLE IF NOT EXISTS portal_config (
    id          INTEGER PRIMARY KEY CHECK (id = 1),  -- singleton row
    ondemand_url         TEXT NOT NULL DEFAULT '',
    ldap_quota_used_attr TEXT NOT NULL DEFAULT '',
    ldap_quota_limit_attr TEXT NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL DEFAULT 0
);

-- Seed the singleton row so reads never need INSERT-or-NOTHING gymnastics.
INSERT OR IGNORE INTO portal_config (id, ondemand_url, ldap_quota_used_attr, ldap_quota_limit_attr, updated_at)
VALUES (1, '', '', '', 0);
