-- Migration 028: persist the LDAP admin (Directory Manager) password in the DB.
--
-- The admin password was previously held in process memory only, set during Enable()
-- and lost on every restart. This caused "admin password not in memory" errors on the
-- Users / Groups pages after autodeploy pushed a new binary.
--
-- Threat model: identical to service_bind_password (plaintext, file-permission
-- protected). A future coordinated pass should encrypt both at rest. Not in this
-- migration — see fix(ldap) commit message.

ALTER TABLE ldap_module_config ADD COLUMN admin_passwd TEXT NOT NULL DEFAULT '';
