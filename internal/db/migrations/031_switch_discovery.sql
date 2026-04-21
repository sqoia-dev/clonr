-- 031_switch_discovery.sql: adds DHCP auto-discovery fields to network_switches,
-- and port/uplink capacity fields for cabling plan generation.

-- Add auto-discovery tracking columns to network_switches.
-- mac_address: the MAC seen in the DHCP discover that triggered discovery.
-- status: "confirmed" (admin-created or admin-confirmed) vs "discovered" (auto).
-- discovered_at: Unix timestamp when auto-discovery first fired (NULL for confirmed).
-- port_count: total switchport count used by the cabling plan generator.
-- uplink_ports: comma-separated uplink port numbers excluded from node assignment.
ALTER TABLE network_switches ADD COLUMN mac_address   TEXT    NOT NULL DEFAULT '';
ALTER TABLE network_switches ADD COLUMN status        TEXT    NOT NULL DEFAULT 'confirmed';
ALTER TABLE network_switches ADD COLUMN discovered_at INTEGER;
ALTER TABLE network_switches ADD COLUMN port_count    INTEGER NOT NULL DEFAULT 48;
ALTER TABLE network_switches ADD COLUMN uplink_ports  TEXT    NOT NULL DEFAULT '';
