-- 029_system_accounts.sql: system service account definitions for node injection.
--
-- Accounts and groups defined here are injected into /etc/passwd, /etc/group,
-- and /etc/shadow via useradd/groupadd in chroot during the finalize step.
-- They are local accounts — entirely independent of the LDAP module.

CREATE TABLE system_groups (
    id          TEXT    PRIMARY KEY,    -- UUID
    name        TEXT    NOT NULL UNIQUE,
    gid         INTEGER NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE system_accounts (
    id           TEXT    PRIMARY KEY,   -- UUID
    username     TEXT    NOT NULL UNIQUE,
    uid          INTEGER NOT NULL UNIQUE,
    -- primary_gid references system_groups.gid (not .id) so the value is
    -- self-contained when serialised into NodeConfig for the deploy agent.
    -- Application code enforces referential integrity; SQLite FK not used here
    -- because GID may optionally reference a group defined outside this table
    -- (e.g. a group baked into the base image at a well-known GID).
    primary_gid  INTEGER NOT NULL,
    shell        TEXT    NOT NULL DEFAULT '/sbin/nologin',
    home_dir     TEXT    NOT NULL DEFAULT '/dev/null',
    -- create_home: when true, useradd will create the home directory.
    -- Default false — most service accounts reference package-managed dirs.
    create_home  INTEGER NOT NULL DEFAULT 0,
    -- system_account: when true, passes --system to useradd.
    -- Cosmetic on most distros (Rocky/RHEL UID < 1000 is system by convention)
    -- but explicit is better than implicit.
    system_account INTEGER NOT NULL DEFAULT 1,
    comment      TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- Indexes for the two hot paths: list-all (finalize reads all rows) and
-- conflict-check-by-uid/gid (validate before save).
CREATE INDEX idx_system_accounts_uid ON system_accounts(uid);
CREATE INDEX idx_system_groups_gid   ON system_groups(gid);
