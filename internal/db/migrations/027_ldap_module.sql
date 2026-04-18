-- 027_ldap_module.sql: LDAP module state tables.
--
-- ldap_module_config is a singleton (id=1 enforced by CHECK constraint).
-- ldap_node_state tracks which nodes have been configured with LDAP client config.

CREATE TABLE ldap_module_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled BOOLEAN NOT NULL DEFAULT 0,
    -- status: disabled | provisioning | ready | error
    status TEXT NOT NULL DEFAULT 'disabled',
    status_detail TEXT NOT NULL DEFAULT '',
    base_dn TEXT NOT NULL DEFAULT '',
    ca_cert_pem TEXT NOT NULL DEFAULT '',
    -- ca_key_pem stores the CA private key in PEM form.
    -- V2 hardening item: encrypt this at rest using a secrets facility.
    -- For v1, stored plaintext with the understanding that access to the DB
    -- file requires filesystem-level access to the clonr server host.
    ca_key_pem TEXT NOT NULL DEFAULT '',
    ca_cert_fingerprint TEXT NOT NULL DEFAULT '',
    server_cert_pem TEXT NOT NULL DEFAULT '',
    server_key_pem TEXT NOT NULL DEFAULT '',
    server_cert_not_after DATETIME,
    -- admin_password_hash is bcrypt of the Directory Manager password.
    -- The plaintext DM password is held in memory only (never persisted).
    admin_password_hash TEXT NOT NULL DEFAULT '',
    service_bind_dn TEXT NOT NULL DEFAULT '',
    -- service_bind_password stores the node-reader service account password in plaintext.
    -- This is required because it must be templated into each node's sssd.conf at
    -- reimage time. V2 hardening item: encrypt using a secrets facility.
    service_bind_password TEXT NOT NULL DEFAULT '',
    -- base_dn_locked is set to 1 once the first node is provisioned with LDAP
    -- client config. After that the base DN cannot be changed.
    base_dn_locked INTEGER NOT NULL DEFAULT 0,
    last_provisioned_at DATETIME,
    last_checked_at DATETIME,
    last_check_error TEXT NOT NULL DEFAULT ''
);

INSERT INTO ldap_module_config (id) VALUES (1);

CREATE TABLE ldap_node_state (
    node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    configured_at DATETIME NOT NULL,
    -- last_config_hash is sha256 of the rendered sssd.conf, used for drift detection.
    last_config_hash TEXT NOT NULL
);
