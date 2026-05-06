-- Migration 102b — clean orphan child rows accumulated under the pre-v0.1.7
-- FK-enforcement-disabled era.
--
-- Sequencing
-- ----------
-- The migration runner sorts files by lexical filename order
-- (internal/db/db.go, sort.Slice). 102b sorts after 102 and before 103, so
-- this cleanup runs as the first step of the v0.1.10 upgrade chain:
--
--     102_fix_ldap_node_state_fk.sql
--     102b_clean_pre_v019_fk_orphans.sql   ← THIS FILE
--     103_repair_users_old_fk_dangling.sql
--
-- Background
-- ----------
-- Until v0.1.7 (commit ae7d573), modernc.org/sqlite silently ignored the
-- `_foreign_keys=on` DSN parameter. Every ON DELETE CASCADE clause shipped
-- since 001_initial.sql has been advisory-only. Over the lifetime of cloner's
-- production database, parent rows in `node_configs`, `base_images`, and
-- `slurm_builds` were deleted while child rows in dependent tables remained,
-- accumulating orphans that no live workflow can reach.
--
-- v0.1.7 turned FK enforcement on at the connection layer. v0.1.9 added a
-- post-migration `PRAGMA foreign_key_check` guard inside the migration runner
-- (db.go:assertNoFKViolations). On cloner, that guard correctly refused to
-- commit migration 103 because 197 pre-existing orphans surfaced for the first
-- time:
--
--     reimage_requests          → base_images       92
--     reimage_requests          → node_configs      43
--     slurm_build_deps          → slurm_builds      34
--     slurm_upgrade_operations  → slurm_builds      12
--     node_config_history       → node_configs      11
--     ldap_node_state           → node_configs       2
--     node_heartbeats           → node_configs       2
--     slurm_node_roles          → node_configs       1
--                                                 ----
--                                                  197
--
-- (Counts measured against /var/lib/clustr/db/clustr.db on cloner via
--  sqlite3 -readonly + pragma_foreign_key_check, 2026-05-03.)
--
-- Disposition
-- -----------
-- DELETE the orphans. They reference parents that no longer exist; the data is
-- unreachable from any live workflow, and there is no parent row to reparent
-- to. NULL is not an option for these columns — every FK on every affected
-- table is NOT NULL (or PRIMARY KEY in the heartbeat / role / state cases).
--
-- A WHERE clause that filters orphans only (rather than `DELETE FROM <table>`)
-- protects fresh databases and CI databases where some of these tables hold
-- legitimate live rows whose parents still exist. On a clean DB every DELETE
-- below is a no-op.
--
-- Runner context
-- --------------
-- db.go disables `foreign_keys` for the duration of the migration. CASCADE on
-- the affected tables would not have helped here anyway because the parents
-- are already gone — there is nothing left to cascade from. The DELETEs are
-- pure leaf-row removal, no tree walks, no cycles.

-- node_configs-rooted orphans -------------------------------------------------

DELETE FROM ldap_node_state
 WHERE node_id NOT IN (SELECT id FROM node_configs);

DELETE FROM node_config_history
 WHERE node_id NOT IN (SELECT id FROM node_configs);

DELETE FROM slurm_node_roles
 WHERE node_id NOT IN (SELECT id FROM node_configs);

DELETE FROM node_heartbeats
 WHERE node_id NOT IN (SELECT id FROM node_configs);

-- reimage_requests has TWO FKs (node_id → node_configs, image_id → base_images);
-- a row is orphan iff EITHER FK is dangling. Single DELETE covers both rules.
-- Column name confirmed against cloner schema: image_id (not base_image_id).
DELETE FROM reimage_requests
 WHERE node_id  NOT IN (SELECT id FROM node_configs)
    OR image_id NOT IN (SELECT id FROM base_images);

-- slurm_builds-rooted orphans -------------------------------------------------

DELETE FROM slurm_build_deps
 WHERE build_id NOT IN (SELECT id FROM slurm_builds);

-- slurm_upgrade_operations has TWO FKs (from_build_id, to_build_id), both
-- → slurm_builds. Either dangling makes the row an orphan.
DELETE FROM slurm_upgrade_operations
 WHERE from_build_id NOT IN (SELECT id FROM slurm_builds)
    OR to_build_id   NOT IN (SELECT id FROM slurm_builds);

-- The migration runner verifies PRAGMA foreign_key_check returns zero rows
-- before committing this transaction. See db.go assertNoFKViolations.
