# clustr Network Module — Sprint Plan (v1)

**Owner:** Dinesh (implementation), Richard (arch review), Gilfoyle (ops review)
**Status:** Ready to build
**Scope:** Two sprints — Phase 1 (data model + Ethernet + switch registry) and Phase 2 (IB + OpenSM + finalize injection)

---

## 1. Goal

Add a "Network" module to clustr. When an operator configures network profiles and
switch definitions in the webui, clustr:

1. Persists switch inventory, Ethernet bond/VLAN/MTU profiles, and InfiniBand
   configuration in the local SQLite DB.
2. Injects per-node Ethernet config (NetworkManager keyfiles) into the deployed
   filesystem during the finalize step, using the node's assigned network profile.
3. Injects IPoIB configuration (NetworkManager IPoIB keyfiles) and, when no
   external Subnet Manager is present, installs and enables OpenSM on the head
   node image at build time and injects an opensm.conf on deploy.
4. Adds IB-related package groups (rdma-core, opensm, infiniband-diags) to the
   kickstart role packages for roles that need them (compute, head-node, storage).
5. Provides a new "Network" section in the webui nav for managing all of the above.

The feature is opt-in at the network-profile level. A clustr install with no network
profiles defined must behave identically to today. Existing node deployments that
use `api.NodeConfig.Interfaces` for static IP assignment continue to work unchanged.

---

## 2. Scope Boundaries (v1)

What this sprint covers:

- Switch registry: store metadata about management/data/IB switches (name, IP, role,
  model — NOT programmatic SNMP control in v1).
- Ethernet bond configuration: define bond mode, member interfaces, VLAN, MTU.
- IPoIB interface configuration: associate an IPoIB IP and mode with a node.
- OpenSM auto-configuration: if the cluster has no external SM, generate and inject
  opensm.conf onto the head node image; enable opensm.service.
- Image-build integration: add rdma-core / opensm / infiniband-diags to relevant
  role package lists.
- Finalize injection: write NetworkManager keyfiles for bonds, VLANs, and IPoIB
  into the deployed rootfs. Does not replace the existing `InterfaceConfig` path —
  operates alongside it, adding bond/VLAN/IPoIB that `InterfaceConfig` cannot
  express.

What this sprint explicitly does NOT cover:

- SNMP-based switch port configuration (flipping VLANs on switch ports via SNMP).
  That is SDN territory; v1 is documentation and inventory only.
- IB partition (pkey) management via the SA (subnet admin). v1 documents pkeys
  but does not push them to the fabric.
- Automated IB fabric topology discovery (ibnetdiscover). v1 reads SM LID from
  sysfs on node registration and surfaces it — no active sweep.
- Dynamic SM failover / high-availability OpenSM.
- Replacing the existing `api.NodeConfig.Interfaces` + `InterfaceConfig` path.
  Bond/VLAN/IPoIB is additive on top of it.

---

## 3. Architectural Decisions (locked)

| Area | Decision | Rationale |
|---|---|---|
| **Storage** | SQLite tables, migration 030 | Consistent with all other clustr config. |
| **Switch registry** | Inventory-only in v1 (no SNMP write) | An HPC admin configures switches manually; clustr tracks what exists and surfaces it in the UI. Programmatic switch control is a Type-1 irreversible API design decision that needs more validation before committing. |
| **Ethernet injection** | NetworkManager keyfiles written into `/etc/NetworkManager/system-connections/` in chroot | NM keyfiles are the canonical format on Rocky/RHEL 8+. They avoid the deprecated `ifcfg` format and work with both RHEL 8 and RHEL 9/10. Written via chroot `nmcli connection load` or direct file write — no running NetworkManager required. |
| **IPoIB injection** | NM keyfile with `type=infiniband`, `transport-mode=connected|datagram` | Same mechanism as Ethernet. No separate tool required. |
| **OpenSM detection** | Read `sm_lid` from `/sys/class/infiniband/<dev>/ports/1/sm_lid` on node registration | Already collected by `hardware.DiscoverIBDevices()`. `sm_lid=0` means no active SM. |
| **OpenSM config** | Single opensm.conf stored in the DB, injected into `/etc/opensm/opensm.conf` in chroot during finalize for the designated head node | Simple, auditable, no templating complexity for v1. |
| **OpenSM package** | Added to the `head-node` role package list in `roles.go` when image has IB role flag; injected via `%post systemctl enable opensm` | Follows the existing roles.go pattern exactly. |
| **Network profiles** | A `NetworkProfile` record owns bond+VLAN+MTU config and is assigned to a `NodeGroup` (not individual nodes) | Consistent with the group-level config pattern already established for disk layouts and extra mounts. Node-level overrides are out of scope for v1. |
| **IB profile** | Stored on the NetworkProfile; covers IPoIB mode, IP assignment method (static, dhcp, none), pkey list | Single struct, not a separate table. |
| **No framework** | Ship as `internal/network/` with the same extension seam as `internal/sysaccounts/` | Follows established convention. |
| **No background workers** | No SNMP polling, no fabric sweeps | Static config. Nothing to watch at runtime. |
| **IB SM presence flag** | A boolean `sm_detected` stored on each node's hardware profile summary | Already computed from `sm_lid != 0`; just needs to be surfaced on the node detail page and factored into the opensm.conf decision. |

### Extension seam (follows sysaccounts pattern)

1. `internal/network/` package, `Manager` struct.
2. DB migration `internal/db/migrations/030_network_module.sql`.
3. Wiring in `server.New()`: construct Manager, store on `Server`.
4. Route registration: `network.RegisterRoutes(r, mgr)` in `buildRouter()`.
5. `Manager.NodeNetworkConfig(ctx, nodeID)` called from the deploy pipeline during
   `RegisterNode` and finalize.

---

## 4. Data Model

### 4.1 SQL Migration — `internal/db/migrations/030_network_module.sql`

```sql
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
```

### 4.2 Go Types — `pkg/api/types.go` additions

```go
// ── Network module types ──────────────────────────────────────────────────────

// NetworkSwitchRole enumerates the valid roles for a switch in the fabric.
type NetworkSwitchRole string

const (
    NetworkSwitchRoleManagement  NetworkSwitchRole = "management"  // IPMI/BMC access
    NetworkSwitchRoleData        NetworkSwitchRole = "data"        // compute traffic
    NetworkSwitchRoleInfiniBand  NetworkSwitchRole = "infiniband"  // IB fabric
)

// NetworkSwitch is an inventory record for a physical switch in the cluster fabric.
// clustr does not program switches in v1; this is documentation + SM-detection input.
type NetworkSwitch struct {
    ID        string            `json:"id"`
    Name      string            `json:"name"`
    Role      NetworkSwitchRole `json:"role"`
    Vendor    string            `json:"vendor,omitempty"`
    Model     string            `json:"model,omitempty"`
    MgmtIP    string            `json:"mgmt_ip,omitempty"`
    Notes     string            `json:"notes,omitempty"`
    IsManaged bool              `json:"is_managed"` // for IB: false = no built-in SM
    CreatedAt time.Time         `json:"created_at"`
    UpdatedAt time.Time         `json:"updated_at"`
}

// BondMember identifies a NIC to be enslaved to a bond.
type BondMember struct {
    ID        string `json:"id"`
    BondID    string `json:"bond_id"`
    MatchMAC  string `json:"match_mac,omitempty"`
    MatchName string `json:"match_name,omitempty"`
    SortOrder int    `json:"sort_order"`
}

// BondConfig describes one bond interface within a NetworkProfile.
type BondConfig struct {
    ID              string       `json:"id"`
    ProfileID       string       `json:"profile_id"`
    BondName        string       `json:"bond_name"`       // "bond0"
    Mode            string       `json:"mode"`            // "802.3ad", "active-backup", etc.
    MTU             int          `json:"mtu"`
    VLANID          int          `json:"vlan_id"`         // 0 = no VLAN
    IPMethod        string       `json:"ip_method"`       // "static", "dhcp", "none"
    IPCIDR          string       `json:"ip_cidr,omitempty"`
    LACPRate        string       `json:"lacp_rate,omitempty"`
    XmitHashPolicy  string       `json:"xmit_hash_policy,omitempty"`
    SortOrder       int          `json:"sort_order"`
    Members         []BondMember `json:"members"`
    CreatedAt       time.Time    `json:"created_at"`
    UpdatedAt       time.Time    `json:"updated_at"`
}

// IBProfile holds InfiniBand / IPoIB configuration for a NetworkProfile.
type IBProfile struct {
    ID          string    `json:"id"`
    ProfileID   string    `json:"profile_id"`
    IPoIBMode   string    `json:"ipoib_mode"`   // "connected" or "datagram"
    IPoIBMTU    int       `json:"ipoib_mtu"`    // 65520 for connected, 2044 for datagram
    IPMethod    string    `json:"ip_method"`    // "static", "dhcp", "none"
    PKeys       []string  `json:"pkeys"`        // ["0x7fff", "0x8001"]
    DeviceMatch string    `json:"device_match,omitempty"` // "mlx5_" or "hfi1_"
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
}

// NetworkProfile is the top-level network configuration entity assigned to a NodeGroup.
type NetworkProfile struct {
    ID          string       `json:"id"`
    Name        string       `json:"name"`
    Description string       `json:"description,omitempty"`
    Bonds       []BondConfig `json:"bonds,omitempty"`
    IB          *IBProfile   `json:"ib,omitempty"`
    CreatedAt   time.Time    `json:"created_at"`
    UpdatedAt   time.Time    `json:"updated_at"`
}

// OpenSMConfig holds the cluster-wide OpenSM configuration.
// Only one instance exists per clustr install. When Enabled=false, no OpenSM
// config is injected anywhere.
type OpenSMConfig struct {
    ID                string    `json:"id"`
    Enabled           bool      `json:"enabled"`
    HeadNodeProfileID string    `json:"head_node_profile_id"`
    ConfContent       string    `json:"conf_content"`       // full opensm.conf text
    LogPrefix         string    `json:"log_prefix"`
    SMPriority        int       `json:"sm_priority"`
    CreatedAt         time.Time `json:"created_at"`
    UpdatedAt         time.Time `json:"updated_at"`
}

// NetworkNodeConfig carries the resolved per-node network configuration
// injected into NodeConfig during the deploy pipeline.
type NetworkNodeConfig struct {
    // Bonds is the list of bond interfaces to create. Each entry produces
    // a set of NM keyfiles in the deployed rootfs.
    Bonds []BondConfig `json:"bonds,omitempty"`
    // IB, when non-nil, produces an IPoIB NM keyfile in the deployed rootfs.
    IB *IBProfile `json:"ib,omitempty"`
    // OpenSMConf, when non-empty, is written to /etc/opensm/opensm.conf
    // and opensm.service is enabled. Only set on the designated head node group.
    OpenSMConf string `json:"opensm_conf,omitempty"`
}
```

Add to `NodeConfig`:

```go
// NetworkConfig, when non-nil, causes finalization to write NetworkManager
// keyfiles for bond interfaces and IPoIB, and optionally inject opensm.conf.
// This is additive to Interfaces: both are written; Interfaces handles simple
// static IPs, NetworkConfig handles bonds, VLANs, and IPoIB.
NetworkConfig *NetworkNodeConfig `json:"network_config,omitempty"`
```

### 4.3 Go Manager — `internal/network/manager.go`

```go
package network

type Manager struct {
    db *db.DB
}

func New(database *db.DB) *Manager

// Switches
func (m *Manager) ListSwitches(ctx context.Context) ([]api.NetworkSwitch, error)
func (m *Manager) CreateSwitch(ctx context.Context, s api.NetworkSwitch) (api.NetworkSwitch, error)
func (m *Manager) UpdateSwitch(ctx context.Context, id string, s api.NetworkSwitch) (api.NetworkSwitch, error)
func (m *Manager) DeleteSwitch(ctx context.Context, id string) error

// Profiles
func (m *Manager) ListProfiles(ctx context.Context) ([]api.NetworkProfile, error)
func (m *Manager) GetProfile(ctx context.Context, id string) (api.NetworkProfile, error)
func (m *Manager) CreateProfile(ctx context.Context, p api.NetworkProfile) (api.NetworkProfile, error)
func (m *Manager) UpdateProfile(ctx context.Context, id string, p api.NetworkProfile) (api.NetworkProfile, error)
func (m *Manager) DeleteProfile(ctx context.Context, id string) error

// Group assignments
func (m *Manager) AssignProfileToGroup(ctx context.Context, groupID, profileID string) error
func (m *Manager) UnassignProfileFromGroup(ctx context.Context, groupID string) error
func (m *Manager) GetGroupProfile(ctx context.Context, groupID string) (*api.NetworkProfile, error)

// OpenSM
func (m *Manager) GetOpenSMConfig(ctx context.Context) (*api.OpenSMConfig, error)
func (m *Manager) SetOpenSMConfig(ctx context.Context, cfg api.OpenSMConfig) (api.OpenSMConfig, error)

// Deploy pipeline
// NodeNetworkConfig resolves the effective network config for a node, given
// its GroupID. Returns nil if no profile is assigned to the group or the
// node has no group.
func (m *Manager) NodeNetworkConfig(ctx context.Context, groupID string) (*api.NetworkNodeConfig, error)

// HasUnmanagedIBSwitch returns true if any registered IB switch has is_managed=false.
// Used by the UI to warn that OpenSM is required.
func (m *Manager) HasUnmanagedIBSwitch(ctx context.Context) (bool, error)
```

---

## 5. API Surface (v1)

All under `/api/v1/network/`, admin role required.

### 5.1 Switches

| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/network/switches` | Returns `{switches: [...], total: N}` |
| POST | `/api/v1/network/switches` | `{name, role, vendor?, model?, mgmt_ip?, notes?, is_managed?}`. 409 on duplicate name. |
| PUT | `/api/v1/network/switches/{id}` | Full replace. |
| DELETE | `/api/v1/network/switches/{id}` | No cascades. Always succeeds. |

### 5.2 Profiles

| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/network/profiles` | Returns `{profiles: [...], total: N}`. Each profile includes its bonds and IB profile. |
| GET | `/api/v1/network/profiles/{id}` | Full profile with bonds + members + IB profile. |
| POST | `/api/v1/network/profiles` | `{name, description?, bonds: [...], ib?: {...}}`. Bonds + members + IB created transactionally. 409 on duplicate name. |
| PUT | `/api/v1/network/profiles/{id}` | Full replace. Existing bonds/members/ib are deleted and recreated. |
| DELETE | `/api/v1/network/profiles/{id}` | 409 if any group_network_profiles row references this profile. |

### 5.3 Group assignments

| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/node-groups/{id}/network-profile` | Returns the assigned `NetworkProfile` or 404 if none. |
| PUT | `/api/v1/node-groups/{id}/network-profile` | `{profile_id}`. Replaces any existing assignment. |
| DELETE | `/api/v1/node-groups/{id}/network-profile` | Removes the assignment. |

### 5.4 OpenSM

| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/network/opensm` | Returns `OpenSMConfig` or a stub with `enabled: false` if no row. |
| PUT | `/api/v1/network/opensm` | Upsert. `{enabled, head_node_profile_id, conf_content, log_prefix?, sm_priority?}`. |

### 5.5 IB status (read-only)

| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/network/ib-status` | Returns `{has_unmanaged_ib_switch: bool, opensm_required: bool, opensm_configured: bool}`. Dashboard summary. |

**Validation rules:**

- `name` on switches and profiles: non-empty, max 64 chars, `^[a-zA-Z0-9._-]+$`.
- `role` on switches: one of `"management"`, `"data"`, `"infiniband"`.
- `mode` on bond: one of `"802.3ad"`, `"active-backup"`, `"balance-rr"`, `"balance-xor"`, `"broadcast"`, `"balance-alb"`, `"balance-tlb"`. These are the NM strings.
- `mtu` on bond: 576–65535. Typical values: 1500, 9000.
- `vlan_id`: 0 (none) or 1–4094.
- `ipoib_mode`: `"connected"` or `"datagram"`.
- `ip_method`: `"static"`, `"dhcp"`, or `"none"`.
- Bond must have at least one member (match_mac or match_name must be non-empty).
- `conf_content` on opensm_config: max 64 KiB.

---

## 6. Finalize Integration

### 6.1 `applyNodeConfig()` extension

Add step 9 after system accounts injection (step 8):

```go
// Step 9: Network config — write NM keyfiles for bonds, VLANs, and IPoIB;
// optionally inject opensm.conf and enable opensm.service.
if cfg.NetworkConfig != nil {
    log.Info().
        Int("bonds", len(cfg.NetworkConfig.Bonds)).
        Bool("ib", cfg.NetworkConfig.IB != nil).
        Bool("opensm", cfg.NetworkConfig.OpenSMConf != "").
        Msg("finalize: injecting network config")
    if err := injectNetworkConfig(ctx, mountRoot, cfg.NetworkConfig); err != nil {
        // Non-fatal: log and continue. Node boots with whatever NM keyfiles
        // the image already has. Admin can reimage to fix.
        log.Warn().Err(err).Msg("WARNING: finalize: network config injection failed (non-fatal)")
    } else {
        log.Info().Msg("finalize: network config injected")
    }
}
```

Update the `applyNodeConfig()` doc comment:

```
//  9. Network config (bond NM keyfiles, IPoIB keyfile, opensm.conf if head node)
```

### 6.2 `injectNetworkConfig()` — `internal/deploy/network.go`

Location: `internal/deploy/network.go` (new file, keeps `finalize.go` readable).

**Injection sequence:**

1. Create `/etc/NetworkManager/system-connections/` in chroot if absent (mode 0700).

2. For each bond in `cfg.NetworkConfig.Bonds`:
   a. Write a NM keyfile for the bond master connection.
   b. For each member NIC in the bond: write a NM keyfile for the slave connection.
   c. If `VLANID > 0`: write a NM keyfile for the VLAN sub-interface.

   NM keyfile for bond master (example for bond0 in 802.3ad mode with LACP):
   ```ini
   [connection]
   id=bond0
   type=bond
   interface-name=bond0
   autoconnect=yes

   [bond]
   mode=802.3ad
   lacp-rate=fast
   xmit-hash-policy=layer3+4
   mtu=9000

   [ipv4]
   method=auto
   ```

   NM keyfile for bond slave (example for ens2f0 enslaved to bond0):
   ```ini
   [connection]
   id=bond0-slave-ens2f0
   type=ethernet
   interface-name=ens2f0
   master=bond0
   slave-type=bond
   autoconnect=yes

   [ethernet]
   ```

   NM keyfile for VLAN sub-interface (example for bond0.100):
   ```ini
   [connection]
   id=bond0.100
   type=vlan
   interface-name=bond0.100
   autoconnect=yes

   [vlan]
   id=100
   parent=bond0

   [ipv4]
   method=auto
   ```

   When `match_mac` is set on a member, use NM's `[ethernet]\nmac-address=<MAC>` to
   match the physical NIC regardless of kernel name.

3. If `cfg.NetworkConfig.IB != nil`:
   Write an NM keyfile for `ib0` (or the matched device, defaulting to `ib0`):
   ```ini
   [connection]
   id=ib0
   type=infiniband
   interface-name=ib0
   autoconnect=yes

   [infiniband]
   transport-mode=connected
   mtu=65520

   [ipv4]
   method=auto
   ```

4. If `cfg.NetworkConfig.OpenSMConf != ""`:
   a. `mkdir -p <mountRoot>/etc/opensm/`
   b. Write `opensm.conf` content to `<mountRoot>/etc/opensm/opensm.conf` (mode 0644).
   c. Run `chroot <mountRoot> systemctl enable opensm` via the existing `runAndLog` helper.

All file writes use `os.WriteFile` directly — no chroot subprocess needed for keyfile
writes. Keyfiles are chmod 0600 root:root (NM requires this).

The `systemctl enable opensm` call requires opensm to be installed in the image.
When opensm is absent (chroot path not found), `runAndLog` returns a non-zero exit
code that is logged as a warning. Injection of the other keyfiles is not affected.

**Idempotency:** Keyfiles are written with `os.WriteFile` which truncates and replaces.
Re-running finalize on the same rootfs overwrites with the current config. This is
the correct behavior — the DB is the source of truth.

**No conflict with existing InterfaceConfig path:** The existing code writes NM
keyfiles for simple static IPs in the `Interfaces` slice. Bond keyfiles use distinct
interface names (`bond0`, `bond0.100`, `ib0`) that cannot conflict with the simple
interface names already handled.

### 6.3 NodeConfig population at reimage time

Follow the `SystemAccountsConfig` pattern established in `handlers/nodes.go`:

```go
type NodesHandler struct {
    // ... existing fields ...
    NetworkConfig func(ctx context.Context, groupID string) (*api.NetworkNodeConfig, error)
}
```

In `RegisterNode`, after system accounts population:

```go
if h.NetworkConfig != nil && node.GroupID != "" {
    netCfg, err := h.NetworkConfig(r.Context(), node.GroupID)
    if err != nil {
        log.Warn().Err(err).Msg("register: could not load network config (non-fatal)")
    } else {
        cfg.NetworkConfig = netCfg
    }
}
```

Wire in `server.go` alongside the existing closures:

```go
NetworkConfig: func(ctx context.Context, groupID string) (*api.NetworkNodeConfig, error) {
    return s.networkMgr.NodeNetworkConfig(ctx, groupID)
},
```

---

## 7. Image Build Integration (roles.go)

### 7.1 IB package additions

Modify `internal/image/isoinstaller/roles.go` to add IB packages to the relevant roles.

**`head-node` role** — add to Rocky/AlmaLinux package list:
```
"rdma-core", "libibverbs-utils", "infiniband-diags",
"opensm", "opensm-libs",
"perftest",
```

**`compute` role** — add to Rocky/AlmaLinux package list:
```
"rdma-core", "libibverbs-utils", "infiniband-diags",
"perftest",
```

**`storage` role** — add to Rocky/AlmaLinux package list:
```
"rdma-core", "libibverbs-utils", "infiniband-diags",
```

**`gpu-compute` role** — add to Rocky/AlmaLinux package list (same as compute):
```
"rdma-core", "libibverbs-utils", "infiniband-diags",
"perftest",
```

Ubuntu equivalents for the same roles:
```
"rdma-core", "ibverbs-utils", "infiniband-diags",
"opensm"           (head-node only),
"perftest",
```

### 7.2 OpenSM service enablement in kickstart

Modify `kickstartTemplate` in `kickstart.go` to add a conditional `systemctl enable`
for opensm when the `NeedsOpenSM` flag is true. This flag is set in `ksTemplateData`
when `hasRole(opts.RoleIDs, "head-node")` is true AND the caller indicates IB is
present (pass-through via `BuildOptions`).

In practice: `NeedsOpenSM` is set if the `head-node` role is selected. The operator
can disable opensm.service manually on head nodes that don't have IB hardware.

Add to `ksTemplateData`:
```go
NeedsOpenSM bool // head-node role + IB selected
```

Add to kickstart `%post` section (after `systemctl enable sshd`):
```
{{- if .NeedsOpenSM}}
# ── OpenSM (InfiniBand Subnet Manager, head node only) ────────────────────────
# opensm.conf is injected by clustr finalize for clusters with unmanaged IB switches.
# Enable the service here so it starts on first boot if opensm.conf is present.
systemctl enable opensm || true
{{end -}}
```

The `|| true` prevents kickstart failure if opensm package isn't present due to a
future role change or package naming difference.

### 7.3 `BuildOptions` changes

Add `HasInfiniBand bool` to the `BuildOptions` struct in `isoinstaller/` (or infer it
from `hasRole(opts.RoleIDs, "head-node")`). Because the head-node role now ships
with the opensm package, the enablement is unconditional when that role is selected.
The flag in `BuildOptions` is only needed if we want finer control (head node but no IB).
For v1, unconditional is simpler and correct: if opensm isn't running on a head node
without IB hardware, it simply exits at startup with no harm.

---

## 8. OpenSM Auto-Configuration Logic

### 8.1 When OpenSM is needed

The operator needs OpenSM on the head node when:

1. The cluster has InfiniBand hardware (any node's hardware profile shows IB devices), AND
2. Either: the IB switch(es) registered in the switch registry have `is_managed=false`,
   OR: the cluster has no dedicated IB switch registered at all (bare-cable or
   direct-attach topology).

The UI surfaces this as a warning on the Network dashboard: "IB detected on nodes
but no managed SM found — OpenSM must run on the head node."

### 8.2 Default opensm.conf

When the operator clicks "Configure OpenSM" in the UI, the server generates a
sensible default `opensm.conf` and populates the text field. The default:

```
# opensm.conf generated by clustr Network module
# Edit as needed. Injected into /etc/opensm/opensm.conf on head node deploy.

guid            0x0000000000000000
sm_priority     0
lmc             0
max_wire_smps   4
transaction_timeout 200
max_op_vls      5
log_flags       0x83
force_log_flush 0
log_file        /var/log/opensm/opensm.log
partition_config /etc/opensm/partitions.conf

# Routing engine: minhop is correct for most flat-tree IB fabrics.
routing_engine  minhop
```

The `guid` field is left as all-zeros, which causes OpenSM to use the port GUID
of the first active port it finds — the correct default for a single-SM setup.

The operator can paste in a custom `opensm.conf` for more complex topologies
(e.g. fat-tree routing, multiple SMs with priority arbitration, pkey configuration).

### 8.3 Injection at finalize time

`Manager.NodeNetworkConfig()` checks whether the queried group's profile ID matches
`opensm_config.head_node_profile_id` AND `opensm_config.enabled = 1`. If so, it
populates `NetworkNodeConfig.OpenSMConf` with the `conf_content`.

The finalize code then writes the file and enables the service, as described in §6.2.

### 8.4 SM detection from hardware profile

Extend the node hardware summary to surface whether each IB port has an active SM:

- `IBPort.SMLID` is already collected in `hardware.DiscoverIBDevices()`.
- A non-zero `sm_lid` means an SM is active on that subnet.
- Add a helper `IBHasActiveSM(devices []IBDevice) bool` to `internal/hardware/infiniband.go`.

The server can call this during `RegisterNode` to automatically populate a
`sm_detected` field in the node's summary (shown in the node detail panel and
on the Network IB status page).

This is read-only observation — the Network module does not act on SM detection
automatically. The operator makes the call whether to run OpenSM.

---

## 9. Webui Placement

### 9.1 Nav structure

Add a "NETWORK" section below the existing SYSTEM section in `index.html`:

```html
<div id="nav-network-section">
    <hr style="border:none;border-top:1px solid var(--border);margin:8px 0;">
    <div style="font-size:11px;font-weight:600;text-transform:uppercase;
                letter-spacing:0.08em;color:var(--text-sidebar);opacity:0.5;
                padding:4px 12px 2px;">NETWORK</div>
    <a href="#/network/switches" class="nav-item" id="nav-network-switches">
        <!-- switch icon or server-network icon -->
        <svg .../><span>Switches</span>
    </a>
    <a href="#/network/profiles" class="nav-item" id="nav-network-profiles">
        <!-- network icon -->
        <svg .../><span>Profiles</span>
    </a>
    <a href="#/network/infiniband" class="nav-item" id="nav-network-ib">
        <!-- ib/fabric icon -->
        <svg .../><span>InfiniBand</span>
    </a>
</div>
```

Hide `#nav-network-section` when `GET /api/v1/network/switches` returns 403,
using the same nav-bootstrap pattern as the SYSTEM section.

### 9.2 Pages

**`#/network/switches`** — Switch registry

Columns: Name | Role | Vendor | Model | Mgmt IP | Managed? | Notes | Actions

Managed column shows a green checkmark for managed switches and an orange warning
icon for unmanaged IB switches (those that need OpenSM).

Create/Edit modal fields: name, role (dropdown), vendor, model, mgmt_ip, notes,
is_managed (checkbox, default on; shown only when role=infiniband).

Banner at top of page when any unmanaged IB switch exists:
"One or more InfiniBand switches are unmanaged (no built-in SM). Configure OpenSM
on the InfiniBand page to ensure fabric routing works."

**`#/network/profiles`** — Network profiles

List view: profile name | description | bond count | IB? | Assigned groups | Actions

Create/Edit modal: multi-section form.
- Profile name + description.
- Bonds section: list of bonds (add/remove). Each bond expands to show:
  - Bond name, mode (dropdown), MTU, VLAN ID, LACP rate (if 802.3ad),
    xmit-hash-policy (if 802.3ad).
  - Members: list of MAC/name entries (add/remove).
- InfiniBand section: optional toggle. When enabled, shows:
  - IPoIB mode (connected/datagram), IPoIB MTU, IP method, PKeys (tag input),
    device match (text input, placeholder "leave empty to use first device").

Profile assignment to a node group happens from the Node Groups page
(add a "Network Profile" dropdown to the group edit modal) — not from the
profiles page. This follows the same pattern as disk layout overrides.

**`#/network/infiniband`** — InfiniBand & OpenSM dashboard

Top section: status cards.
- "IB Hardware Detected": yes/no (aggregated from node hardware profiles).
- "Active SM Detected": yes/no (aggregated from sm_lid != 0 across all nodes).
- "Unmanaged IB Switches": count of switches with is_managed=false.
- "OpenSM Configured": yes/no + enabled/disabled.

Bottom section: OpenSM configuration panel.
- Toggle to enable/disable OpenSM injection.
- Dropdown to select the head node network profile (determines which nodes get
  opensm.conf injected).
- SM priority input (0-15).
- Large text area for opensm.conf content, with a "Load Default" button that
  fetches the default conf from `GET /api/v1/network/opensm/default-conf`.
- Log prefix input.
- Save button.

Warning banner: "Changes take effect on next reimage of nodes in the head node profile."

**Node groups integration** — add to the existing Group edit modal:

- "Network Profile" dropdown: list of profiles + "(none)". When set, a PUT to
  `/api/v1/node-groups/{id}/network-profile` is issued on save.

**Node detail page** — add to the existing node detail view:

- "IB Status" row: shows detected IB devices (from hardware profile) and whether
  SM was active at registration time (`sm_lid` value).

**JS conventions:** Same as `ldap.js` and `sysaccounts.js`. Three new files:
- `network-switches.js` — switch CRUD pages.
- `network-profiles.js` — profile CRUD pages.
- `network-ib.js` — IB/OpenSM dashboard page.

`API.network.*` wrappers in `api.js`.

Router registration in `app.js`:
```js
Router.register('/network/switches',   NetworkSwitchesPage.index);
Router.register('/network/profiles',   NetworkProfilesPage.index);
Router.register('/network/infiniband', NetworkIBPage.index);
```

---

## 10. Files

### New

```
internal/network/manager.go             # Manager: switch/profile/opensm CRUD, NodeNetworkConfig()
internal/network/routes.go              # RegisterRoutes, all handler methods
internal/network/defaults.go            # defaultOpenSMConf() generator

internal/deploy/network.go              # injectNetworkConfig(), NM keyfile generators

internal/db/migrations/030_network_module.sql

internal/server/ui/static/js/network-switches.js
internal/server/ui/static/js/network-profiles.js
internal/server/ui/static/js/network-ib.js
```

### Modified

```
pkg/api/types.go
    + NetworkSwitch, NetworkSwitchRole, BondMember, BondConfig,
      IBProfile, NetworkProfile, OpenSMConfig, NetworkNodeConfig types
    + NetworkConfig *NetworkNodeConfig field on NodeConfig

internal/deploy/finalize.go
    + Step 9: injectNetworkConfig() call in applyNodeConfig()
    + Update step list doc comment

internal/server/server.go
    + *network.Manager field
    + wire in New(), wire NetworkConfig func on NodesHandler
    + register routes in buildRouter() admin group

internal/server/handlers/nodes.go
    + NetworkConfig func field on NodesHandler
    + populate cfg.NetworkConfig in RegisterNode

internal/image/isoinstaller/roles.go
    + rdma-core / infiniband-diags / opensm packages to relevant roles

internal/image/isoinstaller/kickstart.go
    + NeedsOpenSM flag in ksTemplateData
    + conditional systemctl enable opensm in %post template

internal/hardware/infiniband.go
    + IBHasActiveSM(devices []IBDevice) bool helper

internal/server/ui/static/index.html   # add NETWORK nav section
internal/server/ui/static/js/api.js    # add API.network.* wrappers
internal/server/ui/static/js/app.js    # register /network/* routes
```

---

## 11. Edge Cases

### 11.1 Node has no group — no network config injected

`NodeNetworkConfig()` requires a `groupID`. Nodes not in a group get `nil` back;
finalize skips network injection entirely. This is the correct behavior: ungrouped
nodes don't have a profile to derive config from.

### 11.2 Group has no network profile assigned

`GetGroupProfile()` returns nil. `NodeNetworkConfig()` returns nil. Finalize skips.
Existing `Interfaces` path is unaffected.

### 11.3 Bond member MAC not found on node

The NM keyfile is written with the MAC anyway. NM will fail to activate that
connection on first boot because no interface matches the MAC. This is logged by NM
on the deployed node; it does not break other bonds or the node's primary interface.
The operator needs to correct the member MAC and reimage.

The UI should warn when a bond member uses `match_name` instead of `match_mac`:
"Interface name matching is not stable across reboots — use MAC address matching."

### 11.4 OpenSM injected on a node without opensm package

The `systemctl enable opensm` call in `injectNetworkConfig()` will fail if opensm
is not installed in the image. This is logged as a warning (non-fatal). The
`opensm.conf` file is still written to `/etc/opensm/opensm.conf`; it will be used
when opensm is later installed manually. The correct fix is to rebuild the image
with the `head-node` role, which now includes the opensm package.

### 11.5 Multiple IB devices on head node

When a node has mlx5_0 and mlx5_1 (e.g., a dual-port HCA), OpenSM by default
manages all ports it can reach. The `guid=0x0000...` default in opensm.conf causes
it to use the first active port's GUID. The operator can pin a specific GUID in
opensm.conf by looking it up in the node's hardware profile (node_guid field from
`IBDevice`) and pasting it into the conf text area. The UI shows the hardware
profile's IB device list in the node detail panel, making this lookup trivial.

### 11.6 VLANs on top of bonds

The NM keyfile for the VLAN sub-interface references the bond as its parent. If
the bond profile defines both MTU=9000 on the bond and VLAN ID=100, the VLAN
iface inherits the MTU from the parent unless explicitly overridden. For v1,
the VLAN inherits the bond MTU — this is the right behavior for HPC data networks
(jumbo frames on the data VLAN).

### 11.7 Existing NM keyfiles in the image

The image may already have NM keyfiles (e.g., from the kickstart `network --bootproto=dhcp`
directive). Clustr's injected bond keyfiles use distinct `interface-name` values
(`bond0`, `ib0`) that won't conflict with the image's `eth0` or `ens3` keyfiles.
The image's DHCP keyfile for the provisioning NIC is left untouched.

### 11.8 Updating a profile after nodes are deployed

Same model as system accounts: updating a profile changes only the DB row. Already-
deployed nodes retain their injected NM keyfiles. To propagate changes, the operator
reimages. The UI displays: "Changes take effect on next reimage of nodes in groups
assigned to this profile."

### 11.9 Deleting a profile assigned to a group

Returns 409 with a list of group names that reference the profile. The operator
must unassign the profile from those groups first. The error body:
`{"error": "profile is assigned to 2 groups", "code": "profile_in_use", "groups": ["compute-group", "gpu-group"]}`.

---

## 12. Phased Implementation

Each phase commits cleanly and pushes. CI runs the build; `clustr-autodeploy.timer`
rebuilds on the clustr VM within 2 minutes.

### Phase 1 — DB migration + Manager skeleton + switch CRUD

- Migration `030_network_module.sql`.
- `Manager` skeleton with `ListSwitches`, `CreateSwitch`, `UpdateSwitch`, `DeleteSwitch`.
- `RegisterRoutes` wired in `server.go` with switch endpoints fully implemented;
  all other endpoints returning 501 Not Implemented.
- **Commit:** `feat(network): scaffold module with migration and switch CRUD`

### Phase 2 — Profile CRUD + group assignment

- `CreateProfile`, `GetProfile`, `UpdateProfile`, `DeleteProfile` (transactional
  bond + member + IB upsert).
- `AssignProfileToGroup`, `UnassignProfileFromGroup`, `GetGroupProfile`.
- All profile + group-assignment endpoints return correct responses.
- **Commit:** `feat(network): implement profile CRUD and group assignment`

### Phase 3 — OpenSM config + IB status endpoint

- `GetOpenSMConfig`, `SetOpenSMConfig`, `HasUnmanagedIBSwitch`.
- `GET /api/v1/network/opensm`, `PUT /api/v1/network/opensm`.
- `GET /api/v1/network/ib-status`.
- `defaultOpenSMConf()` generator in `defaults.go`.
- **Commit:** `feat(network): add OpenSM config and IB status endpoints`

### Phase 4 — Finalize injection + NodeConfig wiring

- `injectNetworkConfig()` in `internal/deploy/network.go`: NM keyfile writers
  for bond master, bond slave, VLAN, IPoIB, opensm.conf.
- `applyNodeConfig()` step 9 in `finalize.go`.
- `Manager.NodeNetworkConfig()` implemented.
- `NetworkConfig` func field on `NodesHandler`; `RegisterNode` population.
- `NetworkConfig *NetworkNodeConfig` field on `api.NodeConfig`.
- `IBHasActiveSM()` helper in `internal/hardware/infiniband.go`.
- **Commit:** `feat(network): inject NM keyfiles and opensm.conf during finalize`

### Phase 5 — roles.go + kickstart IB packages

- Add rdma-core / infiniband-diags / opensm to relevant roles.
- `NeedsOpenSM` flag + conditional `systemctl enable opensm` in kickstart `%post`.
- **Commit:** `feat(network): add IB packages to compute and head-node roles`

### Phase 6 — Webui

- `network-switches.js`, `network-profiles.js`, `network-ib.js`.
- `API.network.*` wrappers in `api.js`.
- Router registration in `app.js`.
- NETWORK nav section in `index.html`.
- Network Profile dropdown added to node group edit modal.
- IB status rows added to node detail page.
- **Commit:** `feat(network): add switch, profile, and InfiniBand webui`

---

## 13. Build & Deploy Policy (hard rule)

**Do NOT run `go build`, `go test`, `make`, or any compile-heavy command on the
sqoia-dev workstation.** That host is resource-constrained; local builds OOM it.

1. Write code, commit, push to `origin/main` on `sqoia-dev/clustr`.
2. GitHub Actions CI (`ci.yml`) builds and tests on push. Monitor with `gh run watch`
   per the standing CI-watch rule. Fix failures before marking a phase done.
3. `clustr-autodeploy.timer` on `192.168.1.151` polls `origin/main` every 2 minutes,
   rebuilds binaries and initramfs, hot-restarts `clustr-serverd`.

---

## 14. Commit & Authorship

All commits authored as `NessieCanCode <robert.romero@sqoia.dev>`.
No Co-Authored-By lines. No Claude attribution.

```bash
git config user.name "NessieCanCode"
git config user.email "robert.romero@sqoia.dev"
```

Commit prefixes: `feat(network):` for feature work, `fix:` for CI corrections,
`chore:` for scaffolding.

Push via the standard ssh-agent pattern in `CLAUDE.md`.

---

## 15. Known Risks

| Risk | Mitigation |
|---|---|
| NM keyfile conflicts with image's existing connections | Injected keyfiles use `bond0`, `ib0` etc. — names that won't match the image's provisioning NIC keyfiles (which use dhcp on a random iface name). No conflict expected. |
| Bond member MAC not present on node | NM silently ignores the connection on boot. Node still comes up on its provisioning iface. Admin must correct MAC and reimage. Log the mismatch in NM journal. |
| opensm.service enabled but opensm not installed | `systemctl enable` fails non-fatally. opensm.conf still written. Head node boots without SM. Admin must rebuild image with head-node role. |
| Two head nodes both run OpenSM with same priority | NM election resolves this; lower priority SM defers. Document: set different `sm_priority` values if running redundant SMs. Not a correctness bug — IB SM election handles it. |
| Large opensm.conf (complex fat-tree fabric) | Max 64 KiB enforced on PUT. Fat-tree configs with hundreds of switches can be larger — raise limit to 256 KiB in v2 if needed. |
| Profile update with nodes currently deployed | Old NM keyfiles stay on deployed nodes. Reimages pull new config. Same model as all other clustr config. Document prominently. |
| VLAN keyfile written but switch port not configured for the VLAN | NM activates the VLAN interface but traffic is tagged and the switch drops it. This is an operator error — clustr cannot configure the switch in v1. Surface this clearly in the UI: "Ensure the switch port carrying this bond is configured for VLAN {id}." |
| Bond with 802.3ad requires switch LACP configuration | Document in the bond edit modal: "LACP (802.3ad) requires the connected switch ports to have LACP enabled." Out of scope for v1 switch programming. |

---

## 16. Acceptance Criteria

- [ ] Fresh clustr install with no network profiles defined behaves identically to today.
- [ ] An admin can register a management switch, a data switch, and an unmanaged IB
      switch in the switch registry.
- [ ] The IB status endpoint returns `has_unmanaged_ib_switch: true` when an
      unmanaged IB switch is registered.
- [ ] An admin can create a network profile with a `bond0` (802.3ad, 2 members,
      MTU 9000) and an IPoIB config (connected, DHCP).
- [ ] Assigning the profile to a node group and reimaging a node causes bond keyfiles
      and an ib0 keyfile to appear in `/etc/NetworkManager/system-connections/`
      on the deployed rootfs.
- [ ] An admin can configure OpenSM (enable, pick head node profile, paste conf).
- [ ] Reimaging a node in the head node group causes `/etc/opensm/opensm.conf` to
      be present and `opensm.service` to be enabled in the deployed rootfs.
- [ ] Deleting a profile assigned to a group returns 409 with the group names listed.
- [ ] CI is green on every pushed commit.
- [ ] No `go build` / `go test` / `make` invoked locally during development.

---

## 17. Out of Scope (v2)

- SNMP-based switch port VLAN assignment (programmatic switch configuration).
- IB partition (pkey) management via the Subnet Administrator.
- Automated `ibnetdiscover` / fabric topology sweep and visualization.
- HA OpenSM with automated failover between head nodes.
- Node-level network profile overrides (v1: profile is group-scoped only).
- Dynamic NM profile reload on running nodes without reimage.
- MTU probing / PMTU discovery automation.
- RoCE (RDMA over Converged Ethernet) configuration — RoCE nodes appear in IB
  device discovery but IPoIB keyfiles assume native IB transport.
- IPMI/BMC network assignment from the management switch (v1: BMC config stays
  on the existing `NodeConfig.BMC` struct and is applied as before).
