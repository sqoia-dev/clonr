-- 119_drop_pi_tables.sql — Sprint 43-prime Day 2 PI-CODE-WIPE
--
-- The PI workflow (pi_member_requests, pi_expansion_requests,
-- portal_config.pi_auto_approve) was wiped in Sprint 36 and the associated
-- Go code removed in Sprint 43-prime Day 2. Migration 103 already dropped
-- node_groups.pi_user_id; this migration drops the remaining dead tables and
-- the dead portal_config column.

DROP TABLE IF EXISTS pi_member_requests;
DROP TABLE IF EXISTS pi_expansion_requests;
ALTER TABLE portal_config DROP COLUMN pi_auto_approve;
