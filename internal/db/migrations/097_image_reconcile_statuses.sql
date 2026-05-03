-- 097_image_reconcile_statuses.sql (#245)
-- Adds 'corrupt' and 'blob_missing' to the allowed base_images.status values.
--
-- 'corrupt'      — on-disk artifact disagrees with DB and cannot be auto-healed
--                  (F2/F3/F5 from the reconcile design). Operator must investigate.
-- 'blob_missing' — DB row exists but the blob file is absent (F4).
--                  Operator must restore or delete.
--
-- The application-layer guard (validImageStatuses in db.go) is updated alongside
-- this migration to accept the two new values. The partial index below extends the
-- documenting constraint so schema inspection surfaces the full allowed set.

DROP INDEX IF EXISTS idx_base_images_status_valid;

CREATE INDEX IF NOT EXISTS idx_base_images_status_valid
    ON base_images(status)
    WHERE status IN (
        'building', 'interrupted', 'ready', 'archived', 'error',
        'corrupt', 'blob_missing'
    );
