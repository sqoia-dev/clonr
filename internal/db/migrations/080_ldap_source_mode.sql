-- migration 080: LDAP source mode toggle (Sprint 9)
--
-- source_mode: "internal" (default) or "external".
--   "internal" = clustr provisions its own slapd (the Enable() flow).
--   "external" = clustr points at an operator-supplied directory server.
--
-- The default is "internal" on all installs.

ALTER TABLE ldap_module_config ADD COLUMN source_mode TEXT NOT NULL DEFAULT 'internal';
