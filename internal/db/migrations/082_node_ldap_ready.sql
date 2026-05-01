-- Migration 082: LDAP readiness state per node.
-- ldap_ready (nullable): NULL = not yet checked, 1 = sssd connected, 0 = not ready.
-- ldap_ready_detail: human-readable detail from the verify-boot probe.
-- Set by the server when a node phones home via POST /api/v1/nodes/:id/verify-boot
-- and includes sssd status in the payload (Sprint 15 #99).
ALTER TABLE node_configs ADD COLUMN ldap_ready INTEGER;
ALTER TABLE node_configs ADD COLUMN ldap_ready_detail TEXT NOT NULL DEFAULT '';
