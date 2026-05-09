-- migration 110: firmware-aware disk layout selection (Sprint 35 / #255)
--
-- Adds a `firmware_kind` column to disk_layouts so the catalog selector can
-- prefer a layout matching the node's detected firmware when neither a
-- node-level nor a group-level FK has pinned a specific layout.
--
-- Values:
--   'bios' — layout is BIOS-only (e.g. has a biosboot partition, no ESP).
--   'uefi' — layout is UEFI-only (has an ESP partition with the esp flag).
--   'any'  — layout is firmware-agnostic (the autocorrect path will reshape
--            it at deploy time).  This is the safe default for legacy rows.
--
-- The selector also runs a "structurally compatible with target firmware"
-- predicate (e.g. has-ESP for UEFI, has-biosboot or no-ESP for BIOS) so an
-- operator who imports a layout without setting firmware_kind still gets
-- the right routing.  The column is the explicit, fast-path signal.

ALTER TABLE disk_layouts
  ADD COLUMN firmware_kind TEXT NOT NULL DEFAULT 'any'
  CHECK (firmware_kind IN ('bios', 'uefi', 'any'));

CREATE INDEX IF NOT EXISTS idx_disk_layouts_firmware_kind
  ON disk_layouts(firmware_kind);

-- Seed two clustr-default layouts so a fresh install has something to fall
-- back to when neither node nor group has pinned a layout and the image
-- default is unsuitable for the node's firmware.  Both seeds are idempotent
-- via INSERT OR IGNORE on the unique `name` column.

INSERT OR IGNORE INTO disk_layouts (
  id, name, source_node_id, captured_at, layout_json,
  created_at, updated_at, firmware_kind
) VALUES (
  'clustr-default-uefi',
  'clustr default (UEFI)',
  NULL,
  strftime('%s', 'now'),
  json('{"partitions":['
    || '{"label":"esp","size_bytes":536870912,"filesystem":"vfat","mountpoint":"/boot/efi","flags":["esp","boot"],"min_bytes":0},'
    || '{"label":"boot","size_bytes":1073741824,"filesystem":"ext4","mountpoint":"/boot","flags":[],"min_bytes":0},'
    || '{"label":"root","size_bytes":0,"filesystem":"xfs","mountpoint":"/","flags":[],"min_bytes":0}'
    || '],"bootloader":{"type":"grub2","target":"x86_64-efi"}}'),
  strftime('%s', 'now'),
  strftime('%s', 'now'),
  'uefi'
);

INSERT OR IGNORE INTO disk_layouts (
  id, name, source_node_id, captured_at, layout_json,
  created_at, updated_at, firmware_kind
) VALUES (
  'clustr-default-bios',
  'clustr default (BIOS)',
  NULL,
  strftime('%s', 'now'),
  json('{"partitions":['
    || '{"label":"biosboot","size_bytes":1048576,"filesystem":"biosboot","mountpoint":"","flags":["bios_grub"],"min_bytes":0},'
    || '{"label":"boot","size_bytes":1073741824,"filesystem":"ext4","mountpoint":"/boot","flags":[],"min_bytes":0},'
    || '{"label":"root","size_bytes":0,"filesystem":"xfs","mountpoint":"/","flags":[],"min_bytes":0}'
    || '],"bootloader":{"type":"grub2","target":"i386-pc"}}'),
  strftime('%s', 'now'),
  strftime('%s', 'now'),
  'bios'
);
