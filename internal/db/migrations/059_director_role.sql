-- Migration 059: Add director role to RBAC (Sprint D, D1-1).
--
-- The director role is a read-only institutional view role — above viewer but
-- below readonly in the privilege hierarchy. Directors see all NodeGroups,
-- members, grants, and publications without any mutation capability.
--
-- SQLite has no ALTER COLUMN — recreate the users table with the extended CHECK.

PRAGMA foreign_keys = OFF;

ALTER TABLE users RENAME TO _users_059_old;

CREATE TABLE users (
    id                   TEXT PRIMARY KEY,
    username             TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash        TEXT NOT NULL,
    role                 TEXT NOT NULL CHECK(role IN ('admin','operator','readonly','viewer','pi','director')),
    must_change_password INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL,
    last_login_at        INTEGER,
    disabled_at          INTEGER
);

INSERT INTO users SELECT * FROM _users_059_old;
DROP TABLE _users_059_old;

PRAGMA foreign_keys = ON;
