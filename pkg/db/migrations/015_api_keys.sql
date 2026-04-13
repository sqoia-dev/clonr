-- api_keys: SHA-256 hashed API keys with per-scope rotation support.
-- scope is either 'admin' or 'node'.
CREATE TABLE IF NOT EXISTS api_keys (
    id          TEXT PRIMARY KEY,
    scope       TEXT NOT NULL CHECK (scope IN ('admin', 'node')),
    key_hash    TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    last_used_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys(key_hash);
CREATE INDEX IF NOT EXISTS idx_api_keys_scope     ON api_keys(scope);
