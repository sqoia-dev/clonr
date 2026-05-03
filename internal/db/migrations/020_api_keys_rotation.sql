-- 020_api_keys_rotation: add rotation, labelling, and soft-delete support to api_keys.
--
-- label      : human-readable operator label (e.g. "robert-laptop", "ci-runner").
--              Displayed in the UI key table instead of the raw description field.
-- created_by : optional attribution — label of the key/session that created this key.
--              Used for audit attribution (Sanjay rank 4.8: requested_by identity).
-- revoked_at : soft-delete timestamp (unix). Non-null → key is rejected by middleware.
--              Using soft delete so the UI can show "revoked" state and the audit log
--              retains a record of the key's existence.

ALTER TABLE api_keys ADD COLUMN label TEXT;
ALTER TABLE api_keys ADD COLUMN created_by TEXT;
ALTER TABLE api_keys ADD COLUMN revoked_at INTEGER;

CREATE INDEX IF NOT EXISTS idx_api_keys_revoked_at ON api_keys(revoked_at);
