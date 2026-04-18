-- Migration 026: store the firmware type detected by the deploy agent at
-- registration time. "uefi" or "bios"; empty string for legacy nodes that
-- registered before this field existed.
ALTER TABLE node_configs ADD COLUMN detected_firmware TEXT NOT NULL DEFAULT '';
