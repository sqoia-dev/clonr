-- 038_ldap_credential_encryption.sql — LDAP credential encryption at rest (S1-15, D4)
--
-- Adds boolean flag columns to track whether service_bind_password and
-- admin_passwd are stored as AES-256-GCM ciphertext. The server uses these
-- flags to drive the idempotent first-start plaintext→ciphertext migration.
--
-- Encryption semantics:
--   *_encrypted = 0 → the value column holds plaintext (legacy or empty)
--   *_encrypted = 1 → the value column holds hex(nonce||ciphertext||tag)
--
-- The server reads both columns on startup and re-encrypts any plaintext
-- values on first write after upgrade, setting the flag to 1.
ALTER TABLE ldap_module_config ADD COLUMN service_bind_password_encrypted INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ldap_module_config ADD COLUMN admin_passwd_encrypted INTEGER NOT NULL DEFAULT 0;
