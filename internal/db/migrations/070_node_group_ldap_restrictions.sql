-- Migration 070: Per-NodeGroup LDAP group access restrictions (Sprint G, v1.6.0 / CF-40).
--
-- allowed_ldap_groups is a JSON array of LDAP group DNs (or group CNs) that are
-- permitted to submit Slurm jobs on this NodeGroup's partition.
-- Empty array (default) = open access (current behavior — no restriction).
--
-- The Slurm config renderer reads this field and emits AllowGroups= on the
-- PartitionName line when the list is non-empty.

ALTER TABLE node_groups ADD COLUMN allowed_ldap_groups TEXT NOT NULL DEFAULT '[]';
