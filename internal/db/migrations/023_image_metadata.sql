-- 021_image_metadata.sql
-- Adds a nullable metadata_json TEXT column to base_images that mirrors the
-- content-only image metadata sidecar file
-- (/var/lib/clustr/images/<id>/metadata.json) for fast DB queries without disk
-- I/O (ADR-0009 Sprint 3 prep).
--
-- The column is populated by the image build pipeline when it writes the sidecar
-- and by the GET /api/v1/images/:id/metadata handler on first access. Existing
-- rows start with NULL and are back-filled lazily on next access.
--
-- Schema version 1 matches ImageMetadata.SchemaVersion = 1 in pkg/image/metadata.go.

ALTER TABLE base_images ADD COLUMN metadata_json TEXT;
