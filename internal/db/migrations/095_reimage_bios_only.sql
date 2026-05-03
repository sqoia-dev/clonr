-- Migration 095: add bios_only flag to reimage_requests (#159)
--
-- bios_only=1 means the reimage skips image fetch and only applies BIOS
-- settings via the vendor binary in initramfs.
ALTER TABLE reimage_requests ADD COLUMN bios_only INTEGER NOT NULL DEFAULT 0;
