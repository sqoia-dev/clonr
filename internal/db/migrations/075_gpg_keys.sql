-- 075_gpg_keys.sql — user-managed GPG public keys (Sprint 3, GPG-1/2/3).
--
-- Stores ASCII-armored public keys that operators import via the Settings UI
-- or CLI. These are in addition to the three embedded keys (clustr, rocky,
-- EPEL) which are managed as static files and not stored here.
--
-- fingerprint: 40-char hex fingerprint (full, lower-case, no spaces).
-- owner:       human-readable label (e.g. "My Org Signing Key").
-- armored_key: the full ASCII-armored public key block.
-- created_at:  when the key was imported.

CREATE TABLE IF NOT EXISTS gpg_keys (
    fingerprint TEXT PRIMARY KEY,
    owner       TEXT NOT NULL DEFAULT '',
    armored_key TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
