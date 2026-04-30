-- migration 079: LDAP write-back — write bind credentials + group direct-write mode
--
-- write_bind_dn / write_bind_password: optional elevated bind used for all
--   directory writes. If unset, falls back to the existing read (admin) bind.
--   Credentials are stored encrypted (same pattern as service_bind_password, migration 038).
--
-- write_capable: cached probe result. NULL = not yet probed, 1 = probed OK, 0 = probe failed.
--   Re-set every time the operator saves the config via PUT /api/v1/ldap/config.
--
-- write_capable_detail: human-readable reason when write_capable=0 or NULL.
--
-- clustr_ldap_group_mode: per-LDAP-group write mode toggle.
--   cn = LDAP group CN (unique in the DIT). mode = "overlay" (default) or "direct".
--   "overlay" = Sprint 7 supplementary member model (no directory writes).
--   "direct"  = LDAP group edits in clustr go directly to the directory.

ALTER TABLE ldap_module_config ADD COLUMN write_bind_dn                 TEXT NOT NULL DEFAULT '';
ALTER TABLE ldap_module_config ADD COLUMN write_bind_password            TEXT NOT NULL DEFAULT '';
ALTER TABLE ldap_module_config ADD COLUMN write_bind_password_encrypted  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ldap_module_config ADD COLUMN write_capable                  INTEGER;       -- NULL|0|1
ALTER TABLE ldap_module_config ADD COLUMN write_capable_detail           TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS clustr_ldap_group_mode (
    cn          TEXT PRIMARY KEY,               -- LDAP group CN
    mode        TEXT NOT NULL DEFAULT 'overlay', -- "overlay" | "direct"
    updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_by  TEXT NOT NULL DEFAULT ''
);
