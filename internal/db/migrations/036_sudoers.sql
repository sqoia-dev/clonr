-- 036_sudoers.sql — LDAP sudoers group feature
ALTER TABLE ldap_module_config ADD COLUMN sudoers_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ldap_module_config ADD COLUMN sudoers_group_cn TEXT NOT NULL DEFAULT 'clustr-admins';
