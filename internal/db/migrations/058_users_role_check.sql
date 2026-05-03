-- 058: Expand users.role CHECK constraint to include 'viewer' and 'pi'.
--
-- Migration 025 created users with CHECK(role IN ('admin', 'operator', 'readonly')).
-- Migrations 053 and 055 added the viewer and pi roles at the application layer
-- but did not update the schema constraint (noting SQLite prohibits CHECK changes).
--
-- SQLite has no ALTER COLUMN or DROP CONSTRAINT — we must recreate the table.
-- FOREIGN KEY enforcement is off by default in SQLite; we use a rename + recreate
-- pattern and then copy data across.

PRAGMA foreign_keys = OFF;

-- 1. Rename the old table.
ALTER TABLE users RENAME TO _users_old;

-- 2. Create the new table with the expanded CHECK.
CREATE TABLE users (
    id                   TEXT PRIMARY KEY,
    username             TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash        TEXT NOT NULL,
    role                 TEXT NOT NULL CHECK(role IN ('admin', 'operator', 'readonly', 'viewer', 'pi')),
    must_change_password INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL,
    last_login_at        INTEGER,
    disabled_at          INTEGER
);

-- 3. Copy all existing rows.
INSERT INTO users SELECT * FROM _users_old;

-- 4. Drop the old table.
DROP TABLE _users_old;

PRAGMA foreign_keys = ON;
