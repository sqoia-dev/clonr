-- 104_fix_sudoers_group_cn.sql — v0.1.15
--
-- Migration 036 added sudoers_group_cn with DEFAULT 'clustr-admins', but
-- installs that ran the LDAP enable flow before the clonr→clustr rename
-- (2026-04-25) had the row populated with 'clonr-admins' from the prior
-- sudoersDefaultGroupCN constant. The seedDIT path renames the LDAP entry
-- on next Enable() via migrateClonrAdminsGroup, but the DB column is
-- never touched by that path — so the deploy pipeline keeps writing
-- /etc/sudoers.d/clonr-admins with `%clonr-admins ALL=(ALL) NOPASSWD:ALL`
-- because cfg.SudoersGroupCN comes from this column.
--
-- Symptom on the cloner host (v0.1.13 deploy of vm201/vm202):
--   ls /etc/sudoers.d/  →  clonr-admins
--   sqlite3 ... "SELECT sudoers_group_cn FROM ldap_module_config;"
--                       →  clonr-admins
--
-- This migration is a one-shot: any singleton row still holding the
-- legacy value is updated to the canonical clustr-admins. Idempotent —
-- safe to re-run because the WHERE narrows to legacy rows only.
UPDATE ldap_module_config
   SET sudoers_group_cn = 'clustr-admins'
 WHERE id = 1
   AND sudoers_group_cn = 'clonr-admins';
