-- 030_network_module.sql: switch inventory, Ethernet bond/VLAN profiles,
-- and InfiniBand/OpenSM configuration for network-aware node deployment.

-- ── Switch registry ───────────────────────────────────────────────────────────
-- Records managed and unmanaged switches in the fabric. v1 is inventory-only:
-- clustr does not program switches via SNMP. Fields are documentation for admins.
CREATE TABLE network_switches (
    id          TEXT    PRIMARY KEY,        -- UUID
    name        TEXT    NOT NULL UNIQUE,    -- "mgmt-sw-01", "data-sw-01", "ib-sw-01"
    role        TEXT    NOT NULL,           -- "management", "data", "infiniband"
    -- vendor/model is free text; not validated.
    vendor      TEXT    NOT NULL DEFAULT '',
    model       TEXT    NOT NULL DEFAULT '',
    -- mgmt_ip: management IP or hostname. Empty for unmanaged switches.
    mgmt_ip     TEXT    NOT NULL DEFAULT '',
    -- notes: free-text admin notes. Good place to record VLAN ranges, port mappings.
    notes       TEXT    NOT NULL DEFAULT '',
    -- For IB switches: is_managed=0 means the switch has no built-in SM.
    -- When any IB switch has is_managed=0, the Network module will flag that
    -- OpenSM must run on a host.
    is_managed  INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- ── Ethernet bond / VLAN profiles ─────────────────────────────────────────────
-- A NetworkProfile defines how bond interfaces are constructed on nodes
-- assigned to a NodeGroup with this profile. One profile per group; the
-- effective profile is always the group's profile (no node-level override in v1).
CREATE TABLE network_profiles (
    id          TEXT    PRIMARY KEY,        -- UUID
    name        TEXT    NOT NULL UNIQUE,    -- "compute-profile", "login-profile"
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- bond_configs belongs to a network_profile. A profile may have zero or more
-- bond interfaces. Each bond owns one or more member NICs (stored in bond_members).
CREATE TABLE bond_configs (
    id              TEXT    PRIMARY KEY,    -- UUID
    profile_id      TEXT    NOT NULL REFERENCES network_profiles(id) ON DELETE CASCADE,
    bond_name       TEXT    NOT NULL,       -- "bond0", "bond1"
    -- mode: "active-backup", "balance-rr", "802.3ad" (LACP), "balance-xor", etc.
    -- Stored as the NM string form. UI presents human-readable labels.
    mode            TEXT    NOT NULL DEFAULT '802.3ad',
    mtu             INTEGER NOT NULL DEFAULT 1500,
    -- vlan_id: when non-zero, a VLAN sub-interface is created on top of the bond.
    -- The bond itself carries untagged traffic; the VLAN iface carries tagged.
    -- Set to 0 for no VLAN.
    vlan_id         INTEGER NOT NULL DEFAULT 0,
    -- ip_method: "static", "dhcp", "none"
    ip_method       TEXT    NOT NULL DEFAULT 'static',
    -- ip_cidr: e.g. "10.0.1.0/24". Ignored when ip_method != "static".
    -- Node-specific addresses are derived from this base + node index OR pulled
    -- from the existing InterfaceConfig on the NodeConfig.
    -- In v1, ip_cidr is documentation only: actual per-node IPs come from
    -- NodeConfig.Interfaces. The bond profile controls structure, not addresses.
    ip_cidr         TEXT    NOT NULL DEFAULT '',
    -- lacp_rate: "slow" or "fast". Relevant only when mode = "802.3ad".
    lacp_rate       TEXT    NOT NULL DEFAULT 'fast',
    -- xmit_hash_policy: for 802.3ad / balance-xor.
    xmit_hash_policy TEXT   NOT NULL DEFAULT 'layer3+4',
    sort_order      INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- bond_members lists the NIC interfaces that are enslaved to a bond.
-- Interface identification is by kernel name pattern (e.g. "ens2f0") or
-- MAC address. In v1, MAC is preferred because kernel names are not stable.
CREATE TABLE bond_members (
    id          TEXT    PRIMARY KEY,        -- UUID
    bond_id     TEXT    NOT NULL REFERENCES bond_configs(id) ON DELETE CASCADE,
    -- match_mac: preferred matching method. Exact MAC.
    match_mac   TEXT    NOT NULL DEFAULT '',
    -- match_name: kernel interface name. Used when MAC is unknown.
    match_name  TEXT    NOT NULL DEFAULT '',
    sort_order  INTEGER NOT NULL DEFAULT 0
);

-- ── InfiniBand / IPoIB configuration ─────────────────────────────────────────
-- One ib_profile per network_profile. A profile with no ib_profile has no IB config.
CREATE TABLE ib_profiles (
    id              TEXT    PRIMARY KEY,    -- UUID
    profile_id      TEXT    NOT NULL UNIQUE REFERENCES network_profiles(id) ON DELETE CASCADE,
    -- ipoib_mode: "connected" (higher throughput, higher CPU) or "datagram"
    -- (more compatible, lower overhead). "connected" requires kernel module support.
    ipoib_mode      TEXT    NOT NULL DEFAULT 'connected',
    -- ipoib_mtu: 65520 for connected mode, 2044 for datagram. 0 = leave at kernel default.
    ipoib_mtu       INTEGER NOT NULL DEFAULT 65520,
    -- ip_method: "static", "dhcp", "none"
    ip_method       TEXT    NOT NULL DEFAULT 'dhcp',
    -- pkeys: space-separated partition key list, e.g. "0x7fff 0x8001".
    -- Informational in v1 — written to a comment in the NM keyfile.
    pkeys           TEXT    NOT NULL DEFAULT '',
    -- device_match: kernel IB device name prefix to match, e.g. "mlx5_" or "hfi1_".
    -- Empty = match first IB device found.
    device_match    TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- ── OpenSM configuration ──────────────────────────────────────────────────────
-- Single-row table (enforced by CHECK). When the cluster uses managed IB switches
-- with a built-in SM, this table stays empty. When the admin elects to run OpenSM
-- on the head node, a row is created here.
CREATE TABLE opensm_config (
    id              TEXT    PRIMARY KEY,    -- UUID; only one row expected
    -- enabled: when 1, the network module injects opensm.conf and enables
    -- opensm.service on nodes whose NodeGroup has head_node_opensm=1.
    enabled         INTEGER NOT NULL DEFAULT 0,
    -- head_node_profile_id: the network profile assigned to the head/login node group.
    -- The NetworkProfile for that group gets the opensm package injected.
    head_node_profile_id TEXT NOT NULL DEFAULT '',
    -- conf_content: full opensm.conf file content. Populated with a sensible
    -- default when created; admin can edit in the webui.
    conf_content    TEXT    NOT NULL DEFAULT '',
    -- log_prefix: directory for opensm logs. Default /var/log/opensm.
    log_prefix      TEXT    NOT NULL DEFAULT '/var/log/opensm',
    -- sm_priority: 0-15. Higher wins mastership. Default 0.
    sm_priority     INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- ── Group → Network profile assignment ────────────────────────────────────────
-- Maps a NodeGroup to a NetworkProfile. Each group may have at most one profile.
-- When a group has no assignment, no network profile config is injected (existing
-- NodeConfig.Interfaces path is used unchanged).
CREATE TABLE group_network_profiles (
    group_id    TEXT    NOT NULL PRIMARY KEY,    -- references node_groups.id
    profile_id  TEXT    NOT NULL REFERENCES network_profiles(id)
);

-- Indexes for the hot paths.
CREATE INDEX idx_bond_configs_profile     ON bond_configs(profile_id);
CREATE INDEX idx_bond_members_bond        ON bond_members(bond_id);
CREATE INDEX idx_ib_profiles_profile      ON ib_profiles(profile_id);
CREATE INDEX idx_group_network_profiles   ON group_network_profiles(profile_id);
