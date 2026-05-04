-- 102_fix_ldap_node_state_fk.sql (v0.1.7, #247)
--
-- Migration 027 created ldap_node_state with:
--   node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE
--
-- The table was named "nodes" in the original schema draft but was shipped as
-- "node_configs" from the very first migration (001_initial.sql). Because
-- PRAGMA foreign_keys was never enabled at runtime (the DSN _foreign_keys=on
-- parameter is silently ignored by modernc.org/sqlite, and no explicit PRAGMA
-- was set), the broken FK reference never caused a runtime failure.
--
-- As of v0.1.7, db.Open() explicitly enables foreign_keys after all migrations
-- have run (see db.go). With FK enforcement on, INSERT INTO ldap_node_state
-- fails with "no such table: main.nodes" because the FK target never existed.
--
-- This migration recreates ldap_node_state with the correct FK target:
-- REFERENCES node_configs(id) ON DELETE CASCADE.

-- legacy_alter_table is already on during migrations (set in migrate() in db.go),
-- so the RENAME below does not update FK references in other tables that may
-- reference ldap_node_state (there are none — ldap_node_state is a leaf table).

CREATE TABLE ldap_node_state_new (
    node_id           TEXT PRIMARY KEY REFERENCES node_configs(id) ON DELETE CASCADE,
    configured_at     DATETIME NOT NULL,
    last_config_hash  TEXT NOT NULL
);

INSERT INTO ldap_node_state_new SELECT * FROM ldap_node_state;

DROP TABLE ldap_node_state;
ALTER TABLE ldap_node_state_new RENAME TO ldap_node_state;
