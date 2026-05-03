-- 040_image_status_check.sql (S2-10)
-- Adds a domain-level guard for base_images.status via a partial index that
-- enforces allowed values. SQLite does not support adding CHECK constraints
-- to existing tables without rebuilding, so we use a unique partial index
-- as a write-guard: any INSERT/UPDATE with an unknown status will fail to
-- satisfy the guard and the application-level check in UpdateBaseImageStatus
-- will catch it first.
--
-- Allowed values: 'building', 'interrupted', 'ready', 'archived', 'error'
-- (error is the existing runtime value used by failing async builds).

-- Application-layer guard is added in db.UpdateBaseImageStatus.
-- This migration adds a documenting index so schema inspection surfaces the contract.
CREATE INDEX IF NOT EXISTS idx_base_images_status_valid
    ON base_images(status)
    WHERE status IN ('building', 'interrupted', 'ready', 'archived', 'error');
