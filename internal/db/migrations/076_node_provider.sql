-- migration 076: add provider column to node_configs
-- Stores the node's provisioning/power backend type: "ipmi", "proxmox", or "" (unset).
-- TEXT with empty-string default so existing rows are valid without backfill.
ALTER TABLE node_configs ADD COLUMN provider TEXT NOT NULL DEFAULT '';
