-- migration 106: Sprint 38 SYSTEM-ALERT-FRAMEWORK
--
-- system_alerts is operator-visible alert state with TTL — distinct from the
-- existing `alerts` table (#133), which holds rule-engine evaluations
-- (firing/resolved with a rule_name foreign-key).  system_alerts generalises
-- the rule engine's lifecycle into push/set/unset/expire semantics so other
-- subsystems (probes, deploy, slurm, …) can surface operator-visible alerts
-- without going through the rule engine.
--
-- Lifecycle:
--   push  — transient alert that auto-expires after expires_at.
--   set   — durable alert (no expiry) keyed by (key, device); upsert.
--   unset — clears the (key, device) row by stamping cleared_at.
--   sweep — periodic GC removes rows with expires_at < now.
--
-- (key, device) is the natural addressing tuple — a single key may have
-- multiple alerts active for different devices (e.g. key="raid_degraded",
-- device="ctrl0/vd1").  An UNIQUE index on (key, device) WHERE cleared_at
-- IS NULL enforces "one active alert per (key, device)" while allowing
-- historical cleared rows to remain.

CREATE TABLE IF NOT EXISTS system_alerts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT    NOT NULL,
    device      TEXT    NOT NULL,         -- "" when not device-scoped
    level       TEXT    NOT NULL,         -- info | warn | critical
    message     TEXT    NOT NULL,
    fields_json TEXT,                     -- nullable; JSON-encoded extra context
    created_at  INTEGER NOT NULL,         -- unix seconds
    expires_at  INTEGER,                  -- nullable; unix seconds; NULL = no expiry (set)
    cleared_at  INTEGER                   -- nullable; unix seconds; set on unset/expire
);

-- One *active* alert per (key, device).  Cleared/expired rows stay for
-- audit but don't collide with new pushes.
CREATE UNIQUE INDEX IF NOT EXISTS idx_system_alerts_active_keydev
    ON system_alerts (key, device)
    WHERE cleared_at IS NULL;

-- Sweep index: find rows whose expires_at has passed and cleared_at is NULL.
CREATE INDEX IF NOT EXISTS idx_system_alerts_expires_pending
    ON system_alerts (expires_at)
    WHERE expires_at IS NOT NULL AND cleared_at IS NULL;

-- Listing index for GET /api/v1/system_alerts (active only).
CREATE INDEX IF NOT EXISTS idx_system_alerts_active
    ON system_alerts (created_at DESC)
    WHERE cleared_at IS NULL;
