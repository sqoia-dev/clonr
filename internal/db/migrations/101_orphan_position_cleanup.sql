-- 101_orphan_position_cleanup.sql (v0.1.7, #247)
--
-- Migration 100 (100_fix_node_rack_position_nullable.sql) included a
-- "PRAGMA foreign_keys = OFF" statement. In the modernc.org/sqlite pure-Go
-- driver, this pragma affects the connection-level setting even when executed
-- inside a transaction. Because the migration runner uses a single connection
-- (MaxOpenConns=1), the "PRAGMA foreign_keys = OFF" in migration 100 silently
-- disabled FK enforcement for the remainder of that server process lifetime
-- (the DSN _foreign_keys=on sets the pragma at connection-open time, but the
-- in-session PRAGMA statement overrides it on some driver versions).
--
-- Consequence on production: after migration 100 was applied, subsequent
-- DELETE FROM enclosures statements did not cascade to node_rack_position,
-- leaving orphan position rows that point at deleted enclosures. The
-- node_configs rows themselves were untouched by the enclosure-delete path —
-- the data-loss (node_configs rows missing) was a separate pre-existing issue
-- unrelated to the enclosure delete cascade.
--
-- This migration cleans up the orphan position rows. The deleted node_configs
-- rows cannot be restored without a backup; this migration only removes
-- references to deleted parent rows to bring the DB into a consistent state.
--
-- The FK enforcement regression is fixed in db.go: after migrate() completes,
-- Open() now executes "PRAGMA foreign_keys = on" directly on the connection
-- outside any transaction to ensure the session-level setting is correct
-- regardless of what any migration did.

-- Clean up orphan node_rack_position rows whose enclosure was already deleted.
DELETE FROM node_rack_position
  WHERE enclosure_id IS NOT NULL
    AND enclosure_id NOT IN (SELECT id FROM enclosures);

-- Clean up orphan node_rack_position rows whose rack was already deleted
-- (defense in depth — the racks ON DELETE CASCADE should have handled these,
-- but apply the same cleanup pattern here).
DELETE FROM node_rack_position
  WHERE rack_id IS NOT NULL
    AND rack_id NOT IN (SELECT id FROM racks);
