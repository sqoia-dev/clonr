-- 115_dangerous_push_attempts.sql — Sprint 41 Day 3
--
-- Adds attempt_count and consumed columns to pending_dangerous_pushes.
-- These were not included in migration 114 (Day 1) to keep the schema minimal
-- until the flow was implemented. Added now that Day 3 wires the actual gate.
--
-- attempt_count: number of failed confirmation attempts. After 3 the row is
--               locked out (consumed=1) to prevent brute-force.
-- consumed:      1 when the push has been confirmed or locked out; 0 otherwise.
--               A consumed row is never re-used. The GC purger targets consumed=1
--               rows for deletion.

ALTER TABLE pending_dangerous_pushes ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pending_dangerous_pushes ADD COLUMN consumed      INTEGER NOT NULL DEFAULT 0;

-- Index for the GC query in PurgeDangerousPushes (consumed OR expired).
CREATE INDEX idx_pending_dangerous_pushes_consumed ON pending_dangerous_pushes(consumed);
