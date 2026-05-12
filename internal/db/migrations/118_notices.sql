-- 118: notices table for the NOTICE-PATCH operator escape hatch.
--
-- A notice is a sticky global banner written by an admin and visible to all
-- webapp users until it expires or is dismissed.  Only the most recent
-- non-dismissed, non-expired notice is surfaced by GET /api/v1/notices/active.
-- If multiple are active, the one with the highest severity (critical > warning
-- > info) and then most recent created_at wins.

CREATE TABLE notices (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    body         TEXT    NOT NULL,
    severity     TEXT    NOT NULL CHECK(severity IN ('info', 'warning', 'critical')),
    created_by   TEXT,
    created_at   INTEGER NOT NULL,  -- Unix timestamp
    expires_at   INTEGER,           -- Unix timestamp; NULL = never expires
    dismissed_at INTEGER            -- Unix timestamp; NULL = not dismissed
);

CREATE INDEX idx_notices_active
    ON notices (dismissed_at, expires_at, severity DESC, created_at DESC);
