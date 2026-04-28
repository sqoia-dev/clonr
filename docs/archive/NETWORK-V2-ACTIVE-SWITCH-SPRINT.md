# clustr Network Module — v2 Sprint Plan: Active Switch Configuration

**Owner:** Dinesh (implementation), Richard (arch review), Gilfoyle (security/ops review)
**Status:** Ready to build — pending v1 enhancement completion
**Prerequisite:** v1 Network module fully shipped (migration 030, network manager, webui)
**Scope:** Two implementation phases — Phase 1 (Arista eAPI) and Phase 2 (Juniper NETCONF)

---

## 1. Goal

v1 gave clustr a network inventory: it knows what switches exist and what VLAN/bond
profiles should be on nodes. v2 closes the loop by actually configuring the switches.

When an operator defines a bond profile with VLAN 100, MTU 9000, and 802.3ad on
port `Ethernet1/1`, clustr v2 can:

1. Generate the exact switch config diff (dry-run preview).
2. Save the switch's running config before touching it.
3. Push the config with a confirmation gate.
4. Record every change in an immutable audit log.
5. Roll back automatically if the push fails mid-way.

The blast radius of a bad switch config is the entire cluster going offline. Every
design decision in v2 is weighted against that fact.

---

## 2. Scope Boundaries (v2)

### In scope

- Switch credential storage (encrypted at rest, AES-256-GCM).
- Go `SwitchDriver` interface — the vendor-agnostic contract all clients implement.
- **Arista EOS eAPI client** — full implementation (Phase 1).
- **Juniper Junos NETCONF client** — full implementation (Phase 2).
- Dry-run / diff / apply workflow with confirmation gate for destructive operations.
- Config rollback: save running config before changes, restore on failure.
- Immutable change audit log in SQLite.
- API endpoints for all switch config operations.
- UI additions: credential management, config diff viewer, apply button, audit log viewer.
- DB migrations 031 and 032.

### Explicitly out of scope (v3)

- Cisco NX-OS (NX-API).
- Dell OS10 (REST API).
- SSH + CLI fallback for unclassified vendors.
- IB switch configuration (Mellanox/NVIDIA UFM is a separate domain).
- Automated LLDP-based port discovery (planned for v1 enhancement, feeds v2 targeting).
- Switch firmware upgrades.
- BGP / routing configuration.
- zTP (Zero-Touch Provisioning) workflows.

---

## 3. Architectural Decisions (locked)

| Area | Decision | Rationale |
|---|---|---|
| **Driver model** | Each vendor is a separate Go struct implementing `SwitchDriver` interface. No unified abstraction layer beyond the interface. | The interface is thin: each vendor's API model is different enough that a deep abstraction leaks vendor concepts anyway. The interface defines behavior contracts; clients own their implementation. |
| **Credential encryption** | AES-256-GCM, key derived separately from the DB. Key stored in a file path configurable via `--switch-key-file` flag (default: `$data_dir/switch.key`). | Credentials at rest must survive DB backup/restore without the encryption key. Key rotation requires re-encrypting all credential rows — acceptable frequency. |
| **Dry-run mandatory** | Every config operation first runs in dry-run mode, returning a structured diff. A separate API call applies the diff. UI always shows the diff before offering Apply. | The cluster is the blast radius. Requiring explicit review before apply is not optional UX friction — it is the safety model. |
| **Confirmation gate** | Destructive operations (VLAN removal, trunk modification, port-channel deletion) require a `force: true` flag on the apply call. | Removing a VLAN from a trunk can silently drop traffic on running nodes. |
| **Rollback** | Before any apply, the server fetches and stores the switch's full running config in the `switch_config_snapshots` table. On apply failure, the server pushes the snapshot back. | Running config is the ground truth. Snapshots are versioned; old ones are retained for the audit trail. |
| **Audit log** | Every diff, apply, and rollback is written to `switch_audit_log` before the operation completes. Log rows are immutable (no UPDATE/DELETE). | Forensics after a cluster incident require knowing exactly who pushed what, when, with what diff. |
| **No background config polling** | The server does not continuously poll switch state. Config is fetched on demand (dry-run, snapshot). | Polling at cluster scale generates constant API load on switches. State drift is surfaced by the operator running a dry-run; the diff shows what has changed. |
| **Least-privilege switch user** | clustr requires a dedicated switch user with permission scoped to: VLAN create/modify/delete, interface config (mode, trunk, MTU, PFC, portfast, BPDU guard, LLDP), LAG/port-channel config, MAC table reads, running-config reads. NOT admin, NOT config-replace, NOT ZTP, NOT firmware. | Credential theft scope is bounded. Documents the required privilege set per vendor (see §6 and §7). |
| **clustr-generated keypair** | For Arista (which supports SSH key auth on eAPI), clustr generates a per-switch ED25519 keypair and stores the private key encrypted. The public key is displayed to the operator for manual installation. For Juniper, SSH key auth is also used for NETCONF. | SSH keys over passwords: no replay attacks, no password rotation required, revocation is clean. |
| **Storage** | SQLite tables, migrations 031 (credentials + audit) and 032 (Juniper NETCONF). Consistent with all other clustr config. | |

---

## 4. The `SwitchDriver` Interface

Location: `internal/network/driver/driver.go`

This is a Type-1 irreversible decision. The interface must be stable before either
vendor client is written.

```go
package driver

import (
    "context"
    "time"
)

// SwitchDriver is the vendor-agnostic contract for programmatic switch configuration.
// Each vendor implements this interface independently. No shared base struct.
//
// All methods that modify switch state must be preceded by a dry-run call that returns
// a Diff. The caller is responsible for presenting the diff and obtaining confirmation
// before calling Apply.
type SwitchDriver interface {
    // Ping verifies connectivity and credential validity. Returns nil on success.
    // Used to test credentials when they are first saved.
    Ping(ctx context.Context) error

    // Info returns switch identity: hostname, model, software version, uptime.
    Info(ctx context.Context) (*SwitchInfo, error)

    // RunningConfig fetches the full running configuration as a raw string
    // (EOS: show running-config; Junos: show configuration | display set).
    // Used for snapshots before any apply. The format is vendor-specific and
    // is stored verbatim — clustr does not parse it.
    RunningConfig(ctx context.Context) (string, error)

    // DiffVLAN computes what changes are required to bring the switch's VLAN
    // table into alignment with the desired state. Returns a Diff (list of
    // add/modify/remove operations) and a bool indicating whether any operation
    // is destructive (removes an existing VLAN or removes a VLAN from a trunk).
    DiffVLAN(ctx context.Context, desired []VLANSpec) (*Diff, bool, error)

    // DiffPortChannel computes required changes for LAG/port-channel config.
    DiffPortChannel(ctx context.Context, desired []PortChannelSpec) (*Diff, bool, error)

    // DiffInterface computes required changes for interface-level config:
    // mode (access/trunk), VLAN assignment, MTU, PFC, portfast, BPDU guard, LLDP.
    DiffInterface(ctx context.Context, desired []InterfaceSpec) (*Diff, bool, error)

    // Apply executes a previously computed Diff on the switch.
    // If force is false and the Diff contains destructive operations, Apply returns
    // ErrDestructiveNotForced without making any changes.
    // On any error after the first command has been sent, Apply calls Rollback
    // internally and returns a wrapped error that includes the rollback result.
    // The snapshotID identifies the RunningConfig snapshot to use for rollback.
    Apply(ctx context.Context, diff *Diff, snapshotID string, force bool) (*ApplyResult, error)

    // Rollback pushes back the running config identified by snapshotID.
    // This is called automatically by Apply on failure; it is also exposed
    // so the operator can manually trigger a rollback from the UI.
    Rollback(ctx context.Context, snapshotID string) error

    // MACTable returns the current MAC address table (port → MACs).
    // Used by the topology discovery and port-targeting features.
    MACTable(ctx context.Context) ([]MACEntry, error)

    // LLDPNeighbors returns LLDP neighbor information for all ports.
    LLDPNeighbors(ctx context.Context) ([]LLDPNeighbor, error)
}

// SwitchInfo identifies a switch.
type SwitchInfo struct {
    Hostname        string
    Model           string
    SoftwareVersion string
    SerialNumber    string
    Uptime          time.Duration
}

// VLANSpec describes the desired state of a VLAN.
type VLANSpec struct {
    ID     int    // 1-4094
    Name   string // optional label, e.g. "compute-data"
    Active bool   // false = VLAN defined but suspended (Arista/Junos both support this)
}

// PortChannelSpec describes a LAG/port-channel (switch-side bond equivalent).
type PortChannelSpec struct {
    ID          int      // port-channel ID, e.g. 1 for Port-Channel1 (Arista) or ae1 (Junos)
    Mode        string   // "active" (LACP) or "on" (static)
    MemberPorts []string // e.g. ["Ethernet1", "Ethernet2"] or ["ge-0/0/0", "ge-0/0/1"]
    Description string
}

// InterfaceSpec describes the desired state of a switch port.
type InterfaceSpec struct {
    Port        string // vendor-native port name, e.g. "Ethernet1" or "ge-0/0/1"
    Mode        string // "access" or "trunk"
    AccessVLAN  int    // only when Mode == "access"
    TrunkVLANs  []int  // only when Mode == "trunk"; empty means "all"
    NativeVLAN  int    // 802.1q native VLAN on trunk ports; 0 = no explicit native
    MTU         int    // 0 = do not configure (leave at switch default)
    PFCEnabled  bool   // Priority Flow Control for RoCEv2 lossless RDMA
    PFCPriority int    // PFC priority class (typically 3 for RoCEv2)
    Portfast    bool   // edge/server-facing ports: enable spanning-tree portfast
    BPDUGuard   bool   // enable BPDU guard alongside portfast
    LLDPEnabled bool   // ensure LLDP transmit+receive is on
    Description string
}

// Diff is a structured list of changes to apply to a switch.
// The slice is ordered — operations must be applied in sequence.
type Diff struct {
    Operations  []DiffOp
    // HumanReadable is a text representation suitable for display in the UI.
    // Each vendor client generates this in its native CLI syntax (EOS config
    // commands for Arista, set commands for Junos) so operators can read it
    // without translating from an internal format.
    HumanReadable string
}

// DiffOp is a single atomic change within a Diff.
type DiffOp struct {
    Kind        DiffOpKind // Add, Modify, Remove
    Target      string     // human description: "VLAN 100", "Port-Channel1", "Ethernet3"
    Destructive bool       // true if this op removes or reduces existing config
    // VendorCommands is the ordered list of CLI/API commands that implement this op.
    // For Arista: EOS CLI lines. For Junos: set/delete statements.
    // Informational only — the driver's Apply() executes these via the vendor API,
    // not by shelling out.
    VendorCommands []string
}

// DiffOpKind categorizes a DiffOp.
type DiffOpKind string

const (
    DiffOpAdd    DiffOpKind = "add"
    DiffOpModify DiffOpKind = "modify"
    DiffOpRemove DiffOpKind = "remove"
)

// ApplyResult summarizes the outcome of an Apply call.
type ApplyResult struct {
    OperationsApplied int
    SnapshotID        string // snapshot used for this apply (for rollback reference)
    DurationMs        int64
}

// MACEntry maps a MAC address to a switch port and VLAN.
type MACEntry struct {
    MAC     string // "aa:bb:cc:dd:ee:ff"
    Port    string // vendor-native port name
    VLANID  int
    Dynamic bool // false = static/sticky
}

// LLDPNeighbor is one LLDP neighbor record from a switch port.
type LLDPNeighbor struct {
    LocalPort       string
    NeighborSysName string
    NeighborPort    string
    NeighborMgmtIP  string
    ChassisID       string
}

// Sentinel errors.
var (
    // ErrDestructiveNotForced is returned by Apply when the diff contains
    // destructive operations but force=false.
    ErrDestructiveNotForced = errors.New("diff contains destructive operations; set force=true to proceed")

    // ErrRollbackFailed wraps the original apply error and the rollback error.
    // The switch may be in a partially-configured state. Operator must intervene.
    ErrRollbackFailed = errors.New("apply failed AND rollback failed; manual intervention required")
)
```

**Why this interface shape:**

- `DiffVLAN`, `DiffPortChannel`, `DiffInterface` are separate methods, not a single
  `Diff(desiredState)`. This allows the UI to offer granular operations (just push
  the VLAN config, just fix the MTU) without requiring a full declarative state model
  that clustr doesn't have yet.

- `HumanReadable` on `Diff` uses vendor CLI syntax intentionally. HPC network admins
  know Arista EOS CLI fluently. Showing them `vlan 100\n name compute-data` is
  immediately legible. An abstract DSL would require translation and introduce trust
  deficit.

- `VendorCommands` on `DiffOp` is informational. The driver's `Apply()` does not
  shell out — it uses the vendor API. `VendorCommands` is there so the audit log
  records what was logically executed.

---

## 5. Data Model — DB Migrations

### 5.1 Migration 031 — `internal/db/migrations/031_switch_credentials.sql`

```sql
-- 031_switch_credentials.sql: encrypted switch credentials, config snapshots,
-- and the immutable change audit log for active switch configuration (Network v2).

-- ── Switch credentials ────────────────────────────────────────────────────────
-- One credential row per switch (network_switches.id FK).
-- All sensitive fields are AES-256-GCM encrypted; see internal/network/crypto.go.
-- The encryption key is stored separately from the DB (--switch-key-file flag).
CREATE TABLE switch_credentials (
    switch_id           TEXT    PRIMARY KEY
                                REFERENCES network_switches(id) ON DELETE CASCADE,

    -- auth_method: "password" or "ssh_key"
    auth_method         TEXT    NOT NULL DEFAULT 'ssh_key',

    -- username: plaintext. Not sensitive on its own without the credential.
    username            TEXT    NOT NULL DEFAULT 'clustr',

    -- password_enc: AES-256-GCM encrypted password. NULL when auth_method = 'ssh_key'.
    -- Format: base64(nonce || ciphertext || tag), 12-byte nonce.
    password_enc        TEXT,

    -- private_key_enc: AES-256-GCM encrypted PEM private key (ED25519).
    -- NULL when auth_method = 'password'.
    private_key_enc     TEXT,

    -- public_key: plaintext public key in authorized_keys format.
    -- Displayed to the operator for installation on the switch.
    -- Not sensitive; stored plaintext for display without decrypting the private key.
    public_key          TEXT    NOT NULL DEFAULT '',

    -- eapi_port: Arista eAPI HTTPS port. Default 443. Ignored for non-Arista vendors.
    eapi_port           INTEGER NOT NULL DEFAULT 443,

    -- eapi_tls_verify: 1 = verify TLS cert against system CA bundle.
    -- 0 = skip verify (self-signed certs common on lab switches).
    eapi_tls_verify     INTEGER NOT NULL DEFAULT 0,

    -- netconf_port: Juniper NETCONF SSH port. Default 830.
    netconf_port        INTEGER NOT NULL DEFAULT 830,

    -- last_ping_ok: 1 = last Ping() succeeded, 0 = failed, -1 = never tested.
    last_ping_ok        INTEGER NOT NULL DEFAULT -1,
    last_ping_at        INTEGER,    -- unix timestamp

    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

-- ── Running config snapshots ──────────────────────────────────────────────────
-- Snapshots are taken before every apply. Retained indefinitely (disk is cheap;
-- cluster forensics are not). Pruning is a manual/admin operation.
CREATE TABLE switch_config_snapshots (
    id          TEXT    PRIMARY KEY,    -- UUID
    switch_id   TEXT    NOT NULL REFERENCES network_switches(id) ON DELETE CASCADE,

    -- taken_at: when the snapshot was captured, unix timestamp.
    taken_at    INTEGER NOT NULL,

    -- trigger: "pre_apply" (automatic before apply), "manual" (operator-triggered).
    trigger     TEXT    NOT NULL DEFAULT 'pre_apply',

    -- config_text: verbatim output of RunningConfig(). May be large (50-200 KB
    -- for a fully-configured spine switch). Stored as TEXT (SQLite handles large TEXT fine).
    config_text TEXT    NOT NULL,

    -- config_hash: SHA-256 of config_text, hex-encoded.
    -- Used to detect whether the switch config changed between snapshots.
    config_hash TEXT    NOT NULL
);

CREATE INDEX idx_snapshots_switch_taken ON switch_config_snapshots(switch_id, taken_at DESC);

-- ── Change audit log ──────────────────────────────────────────────────────────
-- Immutable. No UPDATE or DELETE ever issued against this table.
-- Every dry-run, apply, rollback, and credential test is recorded here.
CREATE TABLE switch_audit_log (
    id              TEXT    PRIMARY KEY,    -- UUID
    switch_id       TEXT    NOT NULL REFERENCES network_switches(id) ON DELETE CASCADE,

    -- event_type: "dry_run", "apply", "rollback", "credential_test", "snapshot"
    event_type      TEXT    NOT NULL,

    -- operator: the clustr username (from the API key / session) who triggered the event.
    operator        TEXT    NOT NULL DEFAULT '',

    -- snapshot_id: the switch_config_snapshots row captured before this apply.
    -- NULL for dry_run and credential_test events.
    snapshot_id     TEXT    REFERENCES switch_config_snapshots(id),

    -- diff_human: the HumanReadable field from the Diff struct. Stored verbatim.
    -- NULL for snapshot and credential_test events.
    diff_human      TEXT,

    -- ops_count: number of DiffOps in the diff. 0 for no-op diffs.
    ops_count       INTEGER NOT NULL DEFAULT 0,

    -- had_destructive: 1 if any DiffOp was destructive.
    had_destructive INTEGER NOT NULL DEFAULT 0,

    -- forced: 1 if the apply was called with force=true (bypassed destructive gate).
    forced          INTEGER NOT NULL DEFAULT 0,

    -- outcome: "success", "failure", "rollback_success", "rollback_failure", "no_changes"
    outcome         TEXT    NOT NULL DEFAULT 'success',

    -- error_msg: populated when outcome != "success". Truncated to 2048 chars.
    error_msg       TEXT    NOT NULL DEFAULT '',

    -- duration_ms: how long the operation took.
    duration_ms     INTEGER NOT NULL DEFAULT 0,

    created_at      INTEGER NOT NULL
);

CREATE INDEX idx_audit_switch_time ON switch_audit_log(switch_id, created_at DESC);
CREATE INDEX idx_audit_time        ON switch_audit_log(created_at DESC);
```

### 5.2 Migration 032 — `internal/db/migrations/032_juniper_netconf.sql`

```sql
-- 032_juniper_netconf.sql: Juniper-specific NETCONF session configuration.
-- Arista eAPI needs no additional tables (all config in switch_credentials).
-- Juniper NETCONF has additional knobs worth persisting.

CREATE TABLE switch_juniper_config (
    switch_id           TEXT    PRIMARY KEY
                                REFERENCES network_switches(id) ON DELETE CASCADE,

    -- commit_confirmed_timeout: seconds for "commit confirmed N". If clustr loses
    -- connectivity after a commit, Junos auto-rolls back after this timeout.
    -- 0 = use standard commit (no confirmed). Default: 120 seconds.
    -- This is Juniper's native rollback safety net; use it.
    commit_confirmed_timeout INTEGER NOT NULL DEFAULT 120,

    -- routing_instance: for switches where the management plane is in a non-default
    -- routing instance. Empty = use default.
    routing_instance    TEXT    NOT NULL DEFAULT '',

    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);
```

### 5.3 `network_switches` table addition

The existing `network_switches` table (migration 030) needs one new column to
identify which vendor driver to use. Add via a `ALTER TABLE` in migration 031:

```sql
-- Add vendor_driver to network_switches. Must match a registered driver key.
-- Values: "arista_eos", "juniper_junos", "unmanaged" (no active config).
-- Default "unmanaged" is backward-compatible with v1 rows.
ALTER TABLE network_switches ADD COLUMN vendor_driver TEXT NOT NULL DEFAULT 'unmanaged';
```

---

## 6. Arista EOS eAPI Client (Phase 1 — Full Implementation)

### 6.1 Overview

Arista's eAPI is JSON-RPC over HTTPS. A request body is a JSON object with a
`cmds` array of EOS CLI commands. The response returns structured JSON output for
each command. This is the cleanest switch API in the industry.

- Endpoint: `https://<mgmt_ip>:<port>/command-api`
- Auth: HTTP Basic (username:password) or client certificate. We use Basic.
- All commands run in a single JSON-RPC call (batched). Arista executes them
  transactionally within the eAPI session.
- eAPI supports `"format": "json"` (structured output) and `"format": "text"`
  (raw CLI output). We use JSON for machine-readable state, text for RunningConfig.

Location: `internal/network/driver/arista/client.go`

### 6.2 Client struct

```go
package arista

import (
    "bytes"
    "context"
    "crypto/tls"
    "encoding/json"
    "fmt"
    "net/http"
    "time"

    "github.com/sqoia-dev/clustr/internal/network/driver"
)

// Client implements driver.SwitchDriver for Arista EOS via eAPI.
type Client struct {
    baseURL    string       // "https://192.168.1.10:443/command-api"
    username   string
    password   string       // decrypted at construction time; not stored in DB
    httpClient *http.Client
}

// New constructs an Arista eAPI client.
// tlsVerify=false accepts self-signed certs (common in lab/HPC environments).
func New(mgmtIP string, port int, username, password string, tlsVerify bool) *Client {
    transport := &http.Transport{
        TLSClientConfig: &tls.Config{InsecureSkipVerify: !tlsVerify},
    }
    return &Client{
        baseURL:  fmt.Sprintf("https://%s:%d/command-api", mgmtIP, port),
        username: username,
        password: password,
        httpClient: &http.Client{
            Transport: transport,
            Timeout:   30 * time.Second,
        },
    }
}

// eapiRequest is the JSON-RPC request body.
type eapiRequest struct {
    Jsonrpc string   `json:"jsonrpc"`
    Method  string   `json:"method"`
    Params  eapiParams `json:"params"`
    ID      int      `json:"id"`
}

type eapiParams struct {
    Version int      `json:"version"`
    Cmds    []string `json:"cmds"`
    Format  string   `json:"format"` // "json" or "text"
}

// eapiResponse is the JSON-RPC response envelope.
type eapiResponse struct {
    Jsonrpc string            `json:"jsonrpc"`
    ID      int               `json:"id"`
    Result  []json.RawMessage `json:"result,omitempty"`
    Error   *eapiError        `json:"error,omitempty"`
}

type eapiError struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
    Data    any    `json:"data,omitempty"`
}

// call sends a batch of EOS CLI commands and returns the raw JSON result array.
// format should be "json" for structured output or "text" for raw CLI output.
func (c *Client) call(ctx context.Context, format string, cmds ...string) ([]json.RawMessage, error) {
    reqBody := eapiRequest{
        Jsonrpc: "2.0",
        Method:  "runCmds",
        Params: eapiParams{
            Version: 1,
            Cmds:    cmds,
            Format:  format,
        },
        ID: 1,
    }
    body, err := json.Marshal(reqBody)
    if err != nil {
        return nil, fmt.Errorf("arista: marshal request: %w", err)
    }

    req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("arista: build request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")
    req.SetBasicAuth(c.username, c.password)

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("arista: http: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusUnauthorized {
        return nil, fmt.Errorf("arista: authentication failed (check credentials)")
    }
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("arista: unexpected HTTP status %d", resp.StatusCode)
    }

    var eapiResp eapiResponse
    if err := json.NewDecoder(resp.Body).Decode(&eapiResp); err != nil {
        return nil, fmt.Errorf("arista: decode response: %w", err)
    }
    if eapiResp.Error != nil {
        return nil, fmt.Errorf("arista: eAPI error %d: %s", eapiResp.Error.Code, eapiResp.Error.Message)
    }
    return eapiResp.Result, nil
}
```

### 6.3 Driver interface implementations

**`Ping`:**
```go
func (c *Client) Ping(ctx context.Context) error {
    _, err := c.call(ctx, "json", "show version")
    return err
}
```

**`Info`:**
```go
// showVersionResult is the JSON structure returned by "show version" on EOS.
type showVersionResult struct {
    ModelName       string  `json:"modelName"`
    SystemMacAddress string `json:"systemMacAddress"`
    SoftwareImageVersion string `json:"softwareImageVersion"`
    Uptime          float64 `json:"uptime"` // seconds
    Hostname        string  `json:"hostname"`
    SerialNumber    string  `json:"serialNumber"`
}

func (c *Client) Info(ctx context.Context) (*driver.SwitchInfo, error) {
    results, err := c.call(ctx, "json", "show version")
    if err != nil {
        return nil, err
    }
    var sv showVersionResult
    if err := json.Unmarshal(results[0], &sv); err != nil {
        return nil, fmt.Errorf("arista: parse show version: %w", err)
    }
    return &driver.SwitchInfo{
        Hostname:        sv.Hostname,
        Model:           sv.ModelName,
        SoftwareVersion: sv.SoftwareImageVersion,
        SerialNumber:    sv.SerialNumber,
        Uptime:          time.Duration(sv.Uptime) * time.Second,
    }, nil
}
```

**`RunningConfig`:**
```go
func (c *Client) RunningConfig(ctx context.Context) (string, error) {
    results, err := c.call(ctx, "text", "show running-config")
    if err != nil {
        return "", err
    }
    // Text format returns {"output": "..."} 
    var textResult struct {
        Output string `json:"output"`
    }
    if err := json.Unmarshal(results[0], &textResult); err != nil {
        return "", fmt.Errorf("arista: parse running-config: %w", err)
    }
    return textResult.Output, nil
}
```

**`DiffVLAN`:**

The approach: fetch `show vlan` (JSON), compute what's missing vs. what's extra,
generate EOS config commands.

```go
// showVLANResult is partial — only the fields clustr needs.
type showVLANResult struct {
    VLANs map[string]struct {
        Name   string `json:"name"`
        Status string `json:"status"` // "active" or "suspend"
    } `json:"vlans"`
}

func (c *Client) DiffVLAN(ctx context.Context, desired []driver.VLANSpec) (*driver.Diff, bool, error) {
    results, err := c.call(ctx, "json", "show vlan")
    if err != nil {
        return nil, false, err
    }
    var sv showVLANResult
    if err := json.Unmarshal(results[0], &sv); err != nil {
        return nil, false, fmt.Errorf("arista: parse show vlan: %w", err)
    }

    var ops []driver.DiffOp
    hasDestructive := false

    // Build index of current VLANs.
    current := make(map[int]struct{ name, status string })
    for idStr, v := range sv.VLANs {
        var id int
        fmt.Sscanf(idStr, "%d", &id)
        current[id] = struct{ name, status string }{v.Name, v.Status}
    }

    // Compute adds and modifies.
    desiredSet := make(map[int]driver.VLANSpec)
    for _, d := range desired {
        desiredSet[d.ID] = d
        cur, exists := current[d.ID]
        if !exists {
            // VLAN does not exist — add it.
            cmds := []string{
                fmt.Sprintf("vlan %d", d.ID),
            }
            if d.Name != "" {
                cmds = append(cmds, fmt.Sprintf("   name %s", d.Name))
            }
            if !d.Active {
                cmds = append(cmds, "   state suspend")
            }
            cmds = append(cmds, "exit")
            ops = append(ops, driver.DiffOp{
                Kind:           driver.DiffOpAdd,
                Target:         fmt.Sprintf("VLAN %d", d.ID),
                Destructive:    false,
                VendorCommands: cmds,
            })
        } else {
            // VLAN exists — check if name or state needs updating.
            var modCmds []string
            needsMod := false
            if d.Name != "" && cur.name != d.Name {
                modCmds = append(modCmds, fmt.Sprintf("vlan %d", d.ID), fmt.Sprintf("   name %s", d.Name), "exit")
                needsMod = true
            }
            wantStatus := "active"
            if !d.Active {
                wantStatus = "suspend"
            }
            if cur.status != wantStatus {
                modCmds = append(modCmds, fmt.Sprintf("vlan %d", d.ID), fmt.Sprintf("   state %s", wantStatus), "exit")
                needsMod = true
            }
            if needsMod {
                ops = append(ops, driver.DiffOp{
                    Kind:           driver.DiffOpModify,
                    Target:         fmt.Sprintf("VLAN %d", d.ID),
                    Destructive:    false,
                    VendorCommands: modCmds,
                })
            }
        }
    }

    // Compute removes: VLANs on switch not in desired set.
    // Note: skip VLANs 1 (untagged default) and 1002-1005 (legacy).
    for id := range current {
        if id == 1 || (id >= 1002 && id <= 1005) {
            continue
        }
        if _, wanted := desiredSet[id]; !wanted {
            ops = append(ops, driver.DiffOp{
                Kind:           driver.DiffOpRemove,
                Target:         fmt.Sprintf("VLAN %d", d.ID),
                Destructive:    true, // removing a VLAN is always destructive
                VendorCommands: []string{fmt.Sprintf("no vlan %d", id)},
            })
            hasDestructive = true
        }
    }

    diff := buildDiff(ops)
    return diff, hasDestructive, nil
}
```

**`DiffPortChannel`** follows the same pattern using `show port-channel summary` (JSON).

**`DiffInterface`:**

Fetch `show interfaces` and `show spanning-tree interface <port>` (JSON) for portfast
state. The interface diff is the most complex — it must handle access vs. trunk mode,
allowed VLANs on trunk, MTU, and PFC. Implement as a private `diffOneInterface()`
function called per port.

PFC config on Arista:
```
! On the interface:
priority-flow-control on
priority-flow-control priority 3 no-drop

! On the class-map / policy-map (QoS):
! This requires touching the global QoS config, not just the interface.
! The DiffInterface implementation must check whether the QoS policy is
! already applied and generate the full policy-map commands if not.
```

For v2 Phase 1, PFC diff is implemented but limited to detecting whether PFC is
enabled on the interface. Full QoS policy-map management is deferred to v2.1.

**`Apply`:**

```go
func (c *Client) Apply(ctx context.Context, diff *driver.Diff, snapshotID string, force bool) (*driver.ApplyResult, error) {
    // Check destructive gate.
    for _, op := range diff.Operations {
        if op.Destructive && !force {
            return nil, driver.ErrDestructiveNotForced
        }
    }

    start := time.Now()

    // Collect all commands from all ops.
    // Arista eAPI executes a batch as a single transaction in the config session.
    var cmds []string
    cmds = append(cmds, "configure session clustr-apply")
    for _, op := range diff.Operations {
        cmds = append(cmds, op.VendorCommands...)
    }
    cmds = append(cmds, "commit")

    _, err := c.call(ctx, "json", cmds...)
    if err != nil {
        // Apply failed — roll back.
        rbErr := c.Rollback(ctx, snapshotID)
        if rbErr != nil {
            return nil, fmt.Errorf("%w: apply: %v; rollback: %v", driver.ErrRollbackFailed, err, rbErr)
        }
        return nil, fmt.Errorf("arista: apply failed (rolled back): %w", err)
    }

    return &driver.ApplyResult{
        OperationsApplied: len(diff.Operations),
        SnapshotID:        snapshotID,
        DurationMs:        time.Since(start).Milliseconds(),
    }, nil
}
```

**Note on `configure session`:** Arista EOS supports configuration sessions
(`configure session <name>`), which are transactional config contexts. Commands
staged in a session are not active until `commit` is issued. If the session is
abandoned (connection drop), the session is discarded. This is the correct mechanism
to use — not `configure terminal` — because it provides transactional semantics
natively on the switch side.

**`Rollback`:**

```go
func (c *Client) Rollback(ctx context.Context, snapshotID string) error {
    // Fetch the snapshot text from the DB.
    // The Manager passes the snapshot content into the client via a closure
    // at construction time rather than having the client reach into the DB directly.
    // See §8.2 for the Manager wiring.
    snapshot, err := c.snapshotFetcher(ctx, snapshotID)
    if err != nil {
        return fmt.Errorf("arista: rollback: fetch snapshot: %w", err)
    }
    // Use "configure replace" — Arista's native full-config replacement command.
    // This is safer than replaying the original commands because it handles
    // deletions correctly (removed config lines are actually removed).
    cmds := []string{
        "configure replace terminal:",
        snapshot, // the full running-config text
        "EOF",
    }
    // configure replace on Arista via eAPI uses a special multi-line approach.
    // In practice, use the rollback via file method:
    //   copy terminal: flash:clustr-rollback.conf
    //   configure replace flash:clustr-rollback.conf
    // Implemented as two separate calls: one to stage the file, one to apply.
    // See rollbackViaFile() private method.
    return c.rollbackViaFile(ctx, snapshot)
}
```

**MACTable and LLDPNeighbors** use `show mac address-table` and `show lldp neighbors detail`
(JSON format) respectively. Straightforward JSON unmarshalling.

### 6.4 Required switch user (Arista)

Document in the UI when credentials are saved. The operator must create:

```
! On the Arista switch (EOS CLI):
username clustr privilege 2 secret <generated-password>

! Role definition — restrict to what clustr needs:
role network-admin
   10 permit mode exec command show.*
   20 permit mode config command vlan.*
   30 permit mode config command interface.*
   40 permit mode config command port-channel.*
   50 permit mode config command priority-flow-control.*
   60 deny mode exec command .*

username clustr role network-admin
```

For eAPI access, ensure eAPI is enabled:
```
management api http-commands
   protocol https
   no shutdown
```

### 6.5 Arista files

```
internal/network/driver/driver.go          # SwitchDriver interface and types (all vendors)
internal/network/driver/arista/client.go   # Client struct, call(), all interface methods
internal/network/driver/arista/diff.go     # DiffVLAN, DiffPortChannel, DiffInterface
internal/network/driver/arista/snapshot.go # RunningConfig, rollbackViaFile
internal/network/driver/arista/mac.go      # MACTable, LLDPNeighbors
```

---

## 7. Juniper Junos NETCONF Client (Phase 2)

### 7.1 Overview

Juniper's canonical programmatic interface is NETCONF (RFC 6241) over SSH. The
`github.com/Juniper/go-netconf` library handles the NETCONF session, capability
negotiation, and RPC framing.

Key differences from Arista eAPI:

- State is fetched via `<get-configuration>` RPC (returns Junos XML config).
- Changes are staged via `<load-configuration>` (with `action="merge"` or `action="replace"`).
- Applied via `<commit>` RPC, which optionally takes a `<confirmed/>` tag for
  auto-rollback if not confirmed within N seconds (Juniper's native safety net).
- Rollback is via `<rollback>` RPC (uses Junos's built-in rollback history, up to 50
  commits). We also maintain our own snapshot in the DB as a belt-and-suspenders approach.
- Junos config is XML; the `HumanReadable` diff output uses `set` format (the CLI
  equivalent) because HPC admins read `set` format fluently.

Location: `internal/network/driver/juniper/client.go`

### 7.2 Dependency

Add to `go.mod`:
```
github.com/Juniper/go-netconf v0.3.0
```

The library provides `netconf.DialSSH()` which returns a `*netconf.Session`. All
NETCONF RPCs are sent via `session.Exec(netconf.RawMethod(...))`.

### 7.3 Client struct (design — less detail than Arista)

```go
package juniper

import (
    "context"
    "github.com/Juniper/go-netconf/netconf"
    "github.com/sqoia-dev/clustr/internal/network/driver"
    "golang.org/x/crypto/ssh"
)

type Client struct {
    host            string       // "192.168.1.20:830"
    sshConfig       *ssh.ClientConfig
    commitTimeout   int          // seconds for commit confirmed; 0 = standard commit
    snapshotFetcher func(ctx context.Context, snapshotID string) (string, error)
}

func New(host string, port int, sshConfig *ssh.ClientConfig, commitTimeout int,
    snapshotFetcher func(context.Context, string) (string, error)) *Client
```

The client does NOT maintain a persistent NETCONF session. Each operation opens a
new SSH/NETCONF session, performs the RPC(s), and closes. This is safe for
infrequent config operations and avoids stale session handling.

### 7.4 NETCONF RPC strategy

**Ping:** Open a session, check capabilities, close. If `urn:ietf:params:netconf:base:1.0`
is in the advertised capabilities, the switch is reachable and responding.

**Info:** `<get-system-information/>` RPC (Junos native RPC). Returns hostname, model,
OS version, serial number. Uptime is in `<get-system-uptime-information/>`.

**RunningConfig:** `<get-configuration>` with `<format>set</format>` inside `<configuration-information>`.
This returns the entire config in `set` format — one `set interfaces ge-0/0/1 ...`
line per config statement. Store verbatim as the snapshot.

**DiffVLAN / DiffPortChannel / DiffInterface:**
1. Fetch current config via `<get-configuration>`.
2. Parse relevant XML subtrees (bridge-domains, interfaces, aggregated-ethernet).
3. Compute delta.
4. Generate `set` / `delete` statements for `HumanReadable` and `VendorCommands`.
5. Stage changes using `<load-configuration format="set" action="merge">` with the
   set statements as the payload.
6. Fetch `<compare-configuration>` to get the actual unified diff — use this as the
   definitive `HumanReadable` output (Junos computes this itself, more reliable than
   our generated output).

Note: Juniper EX-series switches use bridge-domains for VLANs (rather than the
`vlan` database model used by some other platforms). The client handles both the
`enhanced-layer2-software` (ELS) model (EX4300, EX4600) and the legacy model, detected
from the switch's YANG capabilities or by probing for the bridge-domains hierarchy.

**Apply:**
```
<commit>
  <confirmed/>
  <confirm-timeout>120</confirm-timeout>
  <log>clustr apply: {audit log ID}</log>
</commit>
```
The `commit confirmed 120` means: if clustr doesn't send a confirming `<commit>` within
120 seconds, Junos auto-rolls back. After `<commit confirmed>` succeeds, clustr sends
a plain `<commit>` to confirm. If clustr crashes or loses connectivity in that window,
the switch self-heals. This is `commitTimeout` in `switch_juniper_config`.

**Rollback (Junos native):** `<rollback>0</rollback>` rolls to the last committed config.
Since we just committed the bad config, rollback-0 is the previous state. However,
this assumes only one commit happened since the snapshot — which is guaranteed by
the per-operation snapshot model. Belt-and-suspenders: also have the DB snapshot if
Junos rollback history is unclear.

### 7.5 Required switch user (Juniper)

```
set system login class clustr-class permissions [ interface network routing view view-configuration ]
set system login user clustr class clustr-class
set system login user clustr authentication ssh-ed25519 "<clustr public key>"
set system services netconf ssh port 830
```

The `clustr-class` permissions intentionally exclude `maintenance`, `admin`, `configure`,
and `rollback`. Junos NETCONF with `interface` + `network` + `routing` permissions covers
VLAN, interface, and LAG config.

### 7.6 Juniper files

```
internal/network/driver/juniper/client.go    # Client struct, session management
internal/network/driver/juniper/diff.go      # DiffVLAN, DiffPortChannel, DiffInterface (ELS + legacy)
internal/network/driver/juniper/rpc.go       # RPC helpers, XML types for Junos responses
internal/network/driver/juniper/snapshot.go  # RunningConfig, Rollback
internal/network/driver/juniper/mac.go       # MACTable (get-ethernet-switching-table), LLDPNeighbors
```

---

## 8. Credential Management (`internal/network/crypto.go`)

### 8.1 Encryption

```go
package network

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "encoding/base64"
    "fmt"
    "io"
)

// EncryptField encrypts plaintext with AES-256-GCM.
// Returns base64(12-byte-nonce || ciphertext || 16-byte-tag).
func EncryptField(key []byte, plaintext string) (string, error) {
    block, err := aes.NewCipher(key)
    if err != nil {
        return "", fmt.Errorf("encrypt: %w", err)
    }
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return "", fmt.Errorf("encrypt: gcm: %w", err)
    }
    nonce := make([]byte, gcm.NonceSize()) // 12 bytes
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
        return "", fmt.Errorf("encrypt: nonce: %w", err)
    }
    ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
    return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptField decrypts a value produced by EncryptField.
func DecryptField(key []byte, encoded string) (string, error) {
    data, err := base64.StdEncoding.DecodeString(encoded)
    if err != nil {
        return "", fmt.Errorf("decrypt: base64: %w", err)
    }
    block, err := aes.NewCipher(key)
    if err != nil {
        return "", fmt.Errorf("decrypt: %w", err)
    }
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return "", fmt.Errorf("decrypt: gcm: %w", err)
    }
    nonceSize := gcm.NonceSize()
    if len(data) < nonceSize {
        return "", fmt.Errorf("decrypt: ciphertext too short")
    }
    nonce, ciphertext := data[:nonceSize], data[nonceSize:]
    plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
    if err != nil {
        return "", fmt.Errorf("decrypt: authentication failed")
    }
    return string(plaintext), nil
}
```

### 8.2 Key management

The encryption key is a 32-byte random value. On first startup with v2 enabled,
the server generates a key and writes it to `--switch-key-file` (default:
`$data_dir/switch.key`) with mode 0600. If the file already exists, it is read.

The key is held in memory on the `Manager` and never written to the DB or logged.

Key rotation: a separate CLI subcommand `clustr re-encrypt-switch-keys` reads
all credential rows, decrypts with the old key, re-encrypts with a new key, and
writes them back in a single SQLite transaction. Old key file is renamed with a
timestamp suffix. Document this process; do not automate it in v2.

### 8.3 Keypair generation

When the operator chooses SSH key auth, the UI calls:

```
POST /api/v1/network/switches/{id}/credentials/generate-keypair
```

The server:
1. Generates an ED25519 keypair via `crypto/ed25519`.
2. Serializes the private key as PEM (PKCS#8).
3. Encrypts the PEM with `EncryptField`.
4. Stores `private_key_enc` and `public_key` (SSH authorized_keys format) in
   `switch_credentials`.
5. Returns `{public_key: "ssh-ed25519 AAAA... clustr@<hostname>"}` to the UI.
6. The UI displays the public key with a copy button and instructions:
   "Add this key to the clustr user's authorized_keys on the switch."

---

## 9. Manager Additions (`internal/network/manager.go`)

Add these methods to the existing `Manager` struct. The Manager owns credential
decryption and driver instantiation — no handler or client code touches raw keys.

```go
// Credentials
func (m *Manager) GetCredentials(ctx context.Context, switchID string) (*api.SwitchCredentials, error)
func (m *Manager) SetCredentials(ctx context.Context, switchID string, creds api.SetCredentialsRequest) error
func (m *Manager) GenerateKeypair(ctx context.Context, switchID string) (publicKey string, err error)
func (m *Manager) TestCredentials(ctx context.Context, switchID string) error  // calls Ping(), writes audit log

// Config snapshot
func (m *Manager) TakeSnapshot(ctx context.Context, switchID string, trigger string) (*api.ConfigSnapshot, error)
func (m *Manager) ListSnapshots(ctx context.Context, switchID string) ([]api.ConfigSnapshot, error)
func (m *Manager) GetSnapshot(ctx context.Context, snapshotID string) (*api.ConfigSnapshot, error)

// Diff / apply / rollback
func (m *Manager) DiffVLAN(ctx context.Context, switchID string, desired []api.VLANSpec) (*api.SwitchDiff, error)
func (m *Manager) DiffPortChannel(ctx context.Context, switchID string, desired []api.PortChannelSpec) (*api.SwitchDiff, error)
func (m *Manager) DiffInterface(ctx context.Context, switchID string, desired []api.InterfaceSpec) (*api.SwitchDiff, error)
func (m *Manager) ApplyDiff(ctx context.Context, switchID string, diffID string, force bool) (*api.ApplyResult, error)
func (m *Manager) Rollback(ctx context.Context, switchID string, snapshotID string) error

// Audit log
func (m *Manager) ListAuditLog(ctx context.Context, switchID string, limit, offset int) ([]api.AuditLogEntry, error)

// Read operations (no config changes)
func (m *Manager) GetSwitchInfo(ctx context.Context, switchID string) (*api.SwitchInfo, error)
func (m *Manager) GetMACTable(ctx context.Context, switchID string) ([]api.MACEntry, error)
func (m *Manager) GetLLDPNeighbors(ctx context.Context, switchID string) ([]api.LLDPNeighbor, error)

// Internal: instantiate the correct driver for a switch
func (m *Manager) driverFor(ctx context.Context, switchID string) (driver.SwitchDriver, error)
```

`driverFor` looks up the switch's `vendor_driver` column and credential row,
decrypts the credential, constructs the appropriate client (`arista.New(...)` or
`juniper.New(...)`), and returns it. The driver is instantiated per-call — no
connection pooling (switch API connections are infrequent and lightweight).

**Diff caching:** After computing a diff, store it in a short-lived in-memory map
(keyed by `diffID = UUID`). The apply endpoint references a `diffID` to ensure it
applies the exact diff the operator reviewed. TTL: 10 minutes. If the diff expires,
the operator must re-run the diff. This prevents stale diff application.

---

## 10. API Surface (v2 additions)

All under `/api/v1/network/switches/{id}/`, admin role required.

### 10.1 Credential management

| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/network/switches/{id}/credentials` | Returns credential metadata (auth_method, username, public_key, last_ping_ok, last_ping_at). Never returns password_enc or private_key_enc. |
| PUT | `/api/v1/network/switches/{id}/credentials` | `{auth_method, username, password?, private_key_pem?}`. Encrypts and stores. 400 if auth_method=ssh_key but no key provided. |
| POST | `/api/v1/network/switches/{id}/credentials/generate-keypair` | Generates ED25519 keypair, stores encrypted private key, returns `{public_key}`. Idempotent — overwrites existing keypair. |
| POST | `/api/v1/network/switches/{id}/credentials/test` | Calls Ping(). Returns `{ok: bool, latency_ms: int, error?: string}`. Writes audit log entry. |

### 10.2 Config snapshots

| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/network/switches/{id}/snapshots` | Returns `{snapshots: [{id, taken_at, trigger, config_hash}], total: N}`. Does not return config_text (too large for list view). |
| GET | `/api/v1/network/switches/{id}/snapshots/{snapshot_id}` | Returns full snapshot including config_text. |
| POST | `/api/v1/network/switches/{id}/snapshots` | Manual snapshot trigger. `{trigger: "manual"}`. Returns the created snapshot (with config_text). |

### 10.3 Diff operations

| Method | Path | Notes |
|---|---|---|
| POST | `/api/v1/network/switches/{id}/diff/vlan` | Body: `{vlans: [VLANSpec]}`. Returns `{diff_id, diff: SwitchDiff}`. Diff is cached server-side for 10 minutes. |
| POST | `/api/v1/network/switches/{id}/diff/port-channel` | Body: `{port_channels: [PortChannelSpec]}`. Returns `{diff_id, diff: SwitchDiff}`. |
| POST | `/api/v1/network/switches/{id}/diff/interface` | Body: `{interfaces: [InterfaceSpec]}`. Returns `{diff_id, diff: SwitchDiff}`. |

The `SwitchDiff` response type:

```go
type SwitchDiff struct {
    DiffID        string      `json:"diff_id"`           // cache key for apply
    Operations    []DiffOpAPI `json:"operations"`        // structured ops for UI rendering
    HumanReadable string      `json:"human_readable"`    // CLI syntax for display
    HasDestructive bool       `json:"has_destructive"`   // true if any op is destructive
    ExpiresAt     time.Time   `json:"expires_at"`        // when the cached diff expires
}

type DiffOpAPI struct {
    Kind        string   `json:"kind"`           // "add", "modify", "remove"
    Target      string   `json:"target"`         // "VLAN 100", "Ethernet3"
    Destructive bool     `json:"destructive"`
    Commands    []string `json:"commands"`       // vendor CLI commands (informational)
}
```

### 10.4 Apply and rollback

| Method | Path | Notes |
|---|---|---|
| POST | `/api/v1/network/switches/{id}/apply` | Body: `{diff_id, force?: bool}`. Takes a pre-apply snapshot automatically. Returns `{result: ApplyResult, snapshot_id, audit_id}`. 400 if diff_id not found or expired. 409 if diff has destructive ops and force is not true. |
| POST | `/api/v1/network/switches/{id}/rollback` | Body: `{snapshot_id}`. Rolls back to the specified snapshot. Returns `{audit_id}`. |

### 10.5 Read operations

| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/network/switches/{id}/info` | Returns `SwitchInfo`. Live fetch from switch. |
| GET | `/api/v1/network/switches/{id}/mac-table` | Returns `{entries: [MACEntry], fetched_at}`. Live fetch. |
| GET | `/api/v1/network/switches/{id}/lldp-neighbors` | Returns `{neighbors: [LLDPNeighbor], fetched_at}`. Live fetch. |

### 10.6 Audit log

| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/network/switches/{id}/audit` | Returns `{entries: [AuditLogEntry], total: N}`. Query params: `?limit=50&offset=0`. Default limit 50, max 200. |
| GET | `/api/v1/network/audit` | Cluster-wide audit log (all switches). Same pagination. |

---

## 11. Go Types (additions to `pkg/api/types.go`)

```go
// SwitchCredentials is the metadata returned by GET credentials.
// Sensitive fields (password, private key) are never returned via API.
type SwitchCredentials struct {
    SwitchID    string     `json:"switch_id"`
    AuthMethod  string     `json:"auth_method"` // "password" or "ssh_key"
    Username    string     `json:"username"`
    PublicKey   string     `json:"public_key,omitempty"` // for display
    EAPIPort    int        `json:"eapi_port"`
    EAPITLSVerify bool     `json:"eapi_tls_verify"`
    NetconfPort int        `json:"netconf_port"`
    LastPingOK  int        `json:"last_ping_ok"`  // -1=untested, 0=fail, 1=ok
    LastPingAt  *time.Time `json:"last_ping_at,omitempty"`
    CreatedAt   time.Time  `json:"created_at"`
    UpdatedAt   time.Time  `json:"updated_at"`
}

// SetCredentialsRequest is the body for PUT credentials.
type SetCredentialsRequest struct {
    AuthMethod     string `json:"auth_method"`
    Username       string `json:"username"`
    Password       string `json:"password,omitempty"`       // plaintext; encrypted server-side
    PrivateKeyPEM  string `json:"private_key_pem,omitempty"` // PEM; encrypted server-side
    EAPIPort       int    `json:"eapi_port,omitempty"`
    EAPITLSVerify  bool   `json:"eapi_tls_verify"`
    NetconfPort    int    `json:"netconf_port,omitempty"`
}

// ConfigSnapshot is a stored running-config snapshot.
type ConfigSnapshot struct {
    ID         string    `json:"id"`
    SwitchID   string    `json:"switch_id"`
    TakenAt    time.Time `json:"taken_at"`
    Trigger    string    `json:"trigger"` // "pre_apply", "manual"
    ConfigHash string    `json:"config_hash"`
    ConfigText string    `json:"config_text,omitempty"` // omitted in list view
}

// AuditLogEntry is one row from switch_audit_log.
type AuditLogEntry struct {
    ID             string    `json:"id"`
    SwitchID       string    `json:"switch_id"`
    EventType      string    `json:"event_type"`
    Operator       string    `json:"operator"`
    SnapshotID     string    `json:"snapshot_id,omitempty"`
    DiffHuman      string    `json:"diff_human,omitempty"`
    OpsCount       int       `json:"ops_count"`
    HadDestructive bool      `json:"had_destructive"`
    Forced         bool      `json:"forced"`
    Outcome        string    `json:"outcome"`
    ErrorMsg       string    `json:"error_msg,omitempty"`
    DurationMs     int64     `json:"duration_ms"`
    CreatedAt      time.Time `json:"created_at"`
}

// VLANSpec, PortChannelSpec, InterfaceSpec, SwitchDiff, DiffOpAPI, ApplyResult,
// SwitchInfo, MACEntry, LLDPNeighbor — mirror the driver package types
// but as API-layer types (no driver package dependency in pkg/api).
```

---

## 12. UI Additions

### 12.1 Switch detail page — new tab: "Configure"

The existing switch list page (v1: `#/network/switches`) shows switch metadata.
In v2, clicking a switch opens a detail view with tabs:

**Tab: "Overview"** — existing metadata (name, role, vendor, model, mgmt_ip, notes).
Add: credential status badge (green "Connected", red "Unreachable", gray "Not configured").

**Tab: "Credentials"** — credential form.
- Auth method toggle: "SSH Key" / "Password".
- Username field.
- SSH key mode: show `public_key` in a read-only textarea with a "Copy" button and
  instructions panel. "Generate New Keypair" button calls the keypair endpoint.
  "Upload Existing Private Key" option (paste PEM).
- Password mode: password input (never pre-filled, never shown after save).
- Port configuration: eAPI port (default 443), TLS verify toggle, NETCONF port (default 830).
- "Test Connection" button — calls the test endpoint, shows result inline.
- "Save" button.

**Tab: "VLAN Config"** — VLAN diff/apply.
- Input: a multi-value tag input for VLAN IDs and optional names, e.g.
  `100 (compute-data)`, `200 (storage)`, `300 (ipmi)`.
- Pre-populated from the VLAN IDs referenced in the cluster's network profiles (pulled
  from the existing NetworkProfile + BondConfig data — these are the VLANs clustr
  knows about).
- "Preview Changes" button: calls `POST /diff/vlan`, shows the diff viewer.
- Diff viewer: two-pane — left: structured ops list (add/modify/remove with color coding),
  right: `human_readable` text in a monospace code block.
- Destructive ops highlighted in orange with a warning icon.
- "Apply" button: disabled if no diff or diff expired. If diff has destructive ops,
  shows a confirmation modal: "This will remove VLAN(s) from the switch. Traffic on
  affected ports will be dropped. Type 'confirm' to proceed."
- Apply result: shows ops count, duration, and a link to the audit log entry.

**Tab: "Interface Config"** — port configuration.
- Table of ports (fetched from `show interfaces` or a manually-entered port list).
- Per-port: mode (access/trunk), VLAN assignment, MTU, portfast, BPDU guard, LLDP, PFC.
- "Preview Changes" / "Apply" flow identical to VLAN tab.

**Tab: "Snapshots"** — snapshot list.
- Table: ID (truncated), taken_at, trigger, config_hash, "View" button.
- "Take Snapshot Now" button.
- View opens a modal with the full config_text in a scrollable, monospace textarea.
- "Rollback to this snapshot" button — confirmation modal, then POST /rollback.

**Tab: "Audit Log"** — audit event list.
- Table: timestamp, event_type, operator, outcome, ops_count, had_destructive,
  duration_ms, "View Diff" button (shows diff_human in a modal).
- Paginated, 50 per page. Newest first.

### 12.2 Cluster-wide audit log

New nav item under NETWORK: "Audit Log" (`#/network/audit`).

Shows all events across all switches, newest first. Columns: timestamp, switch name,
event_type, operator, outcome, ops_count, "View" button.

### 12.3 New JS files

```
internal/server/ui/static/js/network-switch-detail.js   # tabs: overview, credentials, vlan, interface, snapshots, audit
internal/server/ui/static/js/network-audit.js           # cluster audit page
internal/server/ui/static/js/network-diff-viewer.js     # reusable diff viewer component (used by switch-detail tabs)
```

Extend `api.js`:
```js
API.network.credentials = { get, set, generateKeypair, test }
API.network.snapshots    = { list, get, create }
API.network.diff         = { vlan, portChannel, interface }
API.network.apply        = { apply, rollback }
API.network.audit        = { list, listAll }
API.network.live         = { info, macTable, lldpNeighbors }
```

---

## 13. Phased Implementation

Each phase is an independent set of commits. Phase 1 ships a fully working
Arista client. Phase 2 adds Juniper. Both phases share the same interface,
credential store, and audit infrastructure built in Phase 1.

### Phase 1 — Infrastructure + Arista eAPI

**Step 1.1 — Driver interface + DB migrations**
- `internal/network/driver/driver.go` — full `SwitchDriver` interface and all types.
- Migration 031 (`switch_credentials`, `switch_config_snapshots`, `switch_audit_log`,
  `ALTER TABLE network_switches ADD COLUMN vendor_driver`).
- Commit: `feat(network-v2): add switch driver interface and credential migrations`

**Step 1.2 — Credential management**
- `internal/network/crypto.go` — `EncryptField`, `DecryptField`, key loading.
- `Manager` credential methods: `GetCredentials`, `SetCredentials`, `GenerateKeypair`,
  `TestCredentials`.
- API endpoints: credential GET, PUT, generate-keypair, test.
- Commit: `feat(network-v2): encrypted credential storage and keypair generation`

**Step 1.3 — Arista eAPI client (core)**
- `internal/network/driver/arista/client.go` — `Client`, `call()`, `Ping`, `Info`,
  `RunningConfig`, `MACTable`, `LLDPNeighbors`.
- `Manager.driverFor()` — instantiates Arista client when `vendor_driver = 'arista_eos'`.
- API endpoints: `/info`, `/mac-table`, `/lldp-neighbors`.
- Commit: `feat(network-v2): Arista eAPI client core (ping, info, running-config, MAC, LLDP)`

**Step 1.4 — Snapshot management**
- `Manager.TakeSnapshot`, `ListSnapshots`, `GetSnapshot`.
- API endpoints: snapshot list, get, create.
- Commit: `feat(network-v2): running-config snapshot capture and retrieval`

**Step 1.5 — Arista diff engine**
- `internal/network/driver/arista/diff.go` — `DiffVLAN`, `DiffPortChannel`,
  `DiffInterface`.
- In-memory diff cache in Manager (10-minute TTL).
- API endpoints: `/diff/vlan`, `/diff/port-channel`, `/diff/interface`.
- Commit: `feat(network-v2): Arista VLAN, port-channel, and interface diff engine`

**Step 1.6 — Arista apply + rollback + audit log**
- `internal/network/driver/arista/snapshot.go` — `Apply`, `Rollback` via `configure replace`.
- `Manager.ApplyDiff`, `Manager.Rollback`.
- Audit log writes in Manager (pre-operation log, post-operation outcome update).
- API endpoints: `/apply`, `/rollback`, `/audit`.
- Commit: `feat(network-v2): Arista apply, rollback, and audit log`

**Step 1.7 — Phase 1 UI**
- Switch detail tabs: Overview (credential badge), Credentials, VLAN Config,
  Snapshots, Audit Log.
- Diff viewer component.
- Cluster-wide audit log page.
- Commit: `feat(network-v2): switch detail UI with credential, diff, snapshot, and audit views`

### Phase 2 — Juniper NETCONF

**Step 2.1 — NETCONF dependency + migration 032**
- `go get github.com/Juniper/go-netconf`.
- Migration 032 (`switch_juniper_config`).
- Commit: `feat(network-v2): add Juniper NETCONF dependency and migration`

**Step 2.2 — Juniper client core**
- `internal/network/driver/juniper/client.go` — `Client`, session management,
  `Ping`, `Info`, `RunningConfig`, `MACTable`, `LLDPNeighbors`.
- `Manager.driverFor()` updated to handle `vendor_driver = 'juniper_junos'`.
- Commit: `feat(network-v2): Juniper NETCONF client core`

**Step 2.3 — Juniper diff + apply + rollback**
- `internal/network/driver/juniper/diff.go` — `DiffVLAN`, `DiffPortChannel`,
  `DiffInterface` (ELS model; detect and handle legacy model).
- `internal/network/driver/juniper/snapshot.go` — `Apply` with `commit confirmed`,
  `Rollback` via Junos native + DB snapshot fallback.
- Commit: `feat(network-v2): Juniper NETCONF diff, apply, and rollback`

**Step 2.4 — Juniper UI additions**
- Add NETCONF port and commit timeout fields to credential tab.
- Interface tab: Junos-style port name hints (ge-0/0/0, xe-0/0/0, ae1).
- Commit: `feat(network-v2): Juniper credential and interface UI`

---

## 14. New Files

```
internal/network/driver/driver.go                      # SwitchDriver interface, all shared types
internal/network/driver/arista/client.go               # Arista client struct, call(), Ping, Info
internal/network/driver/arista/diff.go                 # DiffVLAN, DiffPortChannel, DiffInterface
internal/network/driver/arista/snapshot.go             # RunningConfig, Apply, Rollback
internal/network/driver/arista/mac.go                  # MACTable, LLDPNeighbors
internal/network/driver/juniper/client.go              # Juniper client struct, session, Ping, Info
internal/network/driver/juniper/diff.go                # Diff methods (ELS + legacy)
internal/network/driver/juniper/rpc.go                 # RPC helpers, XML types
internal/network/driver/juniper/snapshot.go            # RunningConfig, Apply (commit confirmed), Rollback
internal/network/driver/juniper/mac.go                 # MACTable, LLDPNeighbors
internal/network/crypto.go                             # EncryptField, DecryptField, key loading

internal/db/migrations/031_switch_credentials.sql
internal/db/migrations/032_juniper_netconf.sql

internal/server/ui/static/js/network-switch-detail.js
internal/server/ui/static/js/network-audit.js
internal/server/ui/static/js/network-diff-viewer.js
```

## Modified Files

```
pkg/api/types.go
    + SwitchCredentials, SetCredentialsRequest, ConfigSnapshot, AuditLogEntry
    + VLANSpec, PortChannelSpec, InterfaceSpec, SwitchDiff, DiffOpAPI, ApplyResult
    + SwitchInfo, MACEntry, LLDPNeighbor

internal/network/manager.go
    + encKey []byte field
    + driverFor() private method
    + all credential, snapshot, diff, apply, rollback, audit log methods

internal/network/routes.go
    + all v2 route registrations

internal/server/server.go
    + --switch-key-file flag, key loading at startup, pass key to Manager

internal/server/ui/static/js/api.js
    + API.network.credentials, snapshots, diff, apply, audit, live namespaces

internal/server/ui/static/js/network-switches.js
    + switch row click → navigate to #/network/switches/{id}

internal/server/ui/static/index.html
    + "Audit Log" nav item under NETWORK
    + route handler registrations

internal/server/ui/static/js/app.js
    + Router.register for /network/switches/:id and /network/audit

go.mod / go.sum
    + github.com/Juniper/go-netconf (Phase 2 only)
```

---

## 15. Security Considerations

### 15.1 The clustr server is now a high-value target

With switch credentials stored on the clustr server, a compromise of the clustr
server gives an attacker the ability to misconfigure every managed switch in the
cluster. Mitigations:

- Encryption key stored separately from DB (different file, different permissions).
- Least-privilege switch users (cannot access routing config, firmware, or admin
  plane — only the interfaces and VLANs clustr needs).
- TLS on eAPI enforced in production (TLS verify = true for production switches;
  skip-verify only for lab use, with a UI warning).
- Audit log is append-only in SQLite — no API endpoint allows deletion.
- All switch config operations require admin API key — no user-scoped key can
  trigger an apply.

Document in the UI: "clustr switch credentials are encrypted at rest. Treat the clustr
server with the same security posture as your switch management VLAN."

### 15.2 Blast radius of failed apply

Even with dry-run + confirmation, a correctly-reviewed diff can still break things
(e.g., VLAN 100 removed from trunk on a port that a running node's bond uses — the
admin simply didn't know the node was using it). Mitigations:

- Snapshot is always taken before apply. The rollback path is always available.
- `commit confirmed` on Juniper provides a 120-second automatic rollback if clustr
  crashes during apply.
- Arista's `configure session` is transactional within eAPI — if the session drops
  mid-commit, the session is discarded and the switch is unchanged.
- The UI warns: "This operation affects live switch configuration. Verify that no
  nodes depend on the VLANs being removed."

### 15.3 API key scope

Switch config operations (diff, apply, rollback, credential management) must be
restricted to the admin role. The existing role check in the chi router middleware
already enforces this for all `/api/v1/network/` routes. No change needed — the
existing middleware is sufficient.

---

## 16. Edge Cases

### 16.1 Switch unreachable during apply

The `Apply` call has a 30-second HTTP timeout (Arista) or SSH timeout (Juniper).
If the switch is unreachable, `Apply` returns an error before any commands are sent.
The snapshot is still taken (before the apply call). The audit log records `outcome=failure`.
The diff remains cached; the operator can retry after resolving connectivity.

### 16.2 clustr crashes during apply (Arista)

Arista `configure session clustr-apply` is abandoned. The switch discards the staged
config. No partial config is applied. This is the correct behavior — `configure session`
is designed for exactly this failure mode.

### 16.3 clustr crashes during apply (Juniper)

`commit confirmed 120` is sent. The switch waits 120 seconds for a confirming `commit`.
If none arrives, Junos rolls back automatically. The cluster is protected. clustr
records the snapshot but cannot update the audit log outcome. On restart, clustr has
no visibility into whether the commit confirmed completed or was rolled back — the
operator must check `show system commit` on the switch and compare against the snapshot.
Document this as a known operational gap.

### 16.4 Diff cache expiry before apply

If the operator takes 11 minutes to review a diff (cache TTL is 10 minutes), the
apply returns `{error: "diff expired", code: "diff_expired"}`. The operator re-runs
the diff. The re-diff may return different results if someone else changed the switch
in the interim — which is the correct behavior. A stale diff applied to a changed
switch is more dangerous than a re-run.

### 16.5 Two operators applying concurrently

No distributed locking in v2. If two operators simultaneously apply different diffs
to the same switch, the second apply wins (last write wins at the switch level).
Both snapshot+audit log entries exist. The second operator's apply may fail if the
switch rejects conflicting config. This is an acceptable operational limitation for
v2 — HPC clusters have one network admin at a time. Document it. Lock-per-switch
is a v3 enhancement if needed.

### 16.6 Vendor not recognized

If `vendor_driver` is `"unmanaged"` or any unrecognized string, `driverFor()` returns
an error: `"switch <id> has vendor_driver='unmanaged'; configure credentials and set
vendor_driver first"`. All diff/apply/rollback endpoints return 422 Unprocessable Entity.
Read-only endpoints (info, mac-table, lldp) also return 422. The credentials tab in the
UI shows a "Select vendor" prompt if `vendor_driver` is unmanaged.

### 16.7 Arista running EOS version without configure session support

`configure session` was introduced in EOS 4.14. Any EOS version newer than that
(the current minimum in HPC environments is EOS 4.20+) supports it. Do not add a
fallback to `configure terminal` — the session model is load-bearing for safety.
If a very old switch is encountered, fail with a clear error: "EOS version too old;
configure session requires EOS 4.14 or later."

---

## 17. Build and CI Rules (unchanged from v1)

**Do NOT run `go build`, `go test`, `make` on the sqoia-dev workstation.** OOMs the host.

1. Write code, commit, push to `origin/main`.
2. GitHub Actions CI builds and tests. Watch with `gh run watch`. Fix failures before
   marking a step done.
3. `clustr-autodeploy.timer` on `192.168.1.151` picks up changes within 2 minutes.

For the Juniper client: the NETCONF library requires an actual Juniper device to
test against. Write unit tests against a mock `netconf.Session` (use the library's
test helpers). Integration tests are manual against a lab switch — document the
required test procedure in a comment at the top of `juniper/client_test.go`.

---

## 18. Acceptance Criteria

### Phase 1 (Arista)

- [ ] An admin can register an Arista switch with `vendor_driver = 'arista_eos'`.
- [ ] An admin can save SSH key credentials; the public key is displayed for installation.
- [ ] "Test Connection" succeeds against a reachable Arista switch and fails clearly
      against an unreachable one.
- [ ] Dry-run VLAN diff returns a structured diff with `human_readable` in EOS CLI syntax.
- [ ] A diff with destructive ops is blocked by the apply endpoint unless `force: true`.
- [ ] Apply takes a pre-apply snapshot, pushes the config via `configure session`, and
      records the outcome in the audit log.
- [ ] Rollback restores the switch to the snapshot state via `configure replace`.
- [ ] MAC table and LLDP neighbor reads return live data from the switch.
- [ ] Audit log is append-only; GET returns events newest-first.
- [ ] CI is green on every pushed commit.

### Phase 2 (Juniper)

- [ ] An admin can register a Juniper switch with `vendor_driver = 'juniper_junos'`.
- [ ] NETCONF session opens, capabilities are negotiated, Ping returns ok.
- [ ] Dry-run VLAN diff returns a diff with `human_readable` in `set` format.
- [ ] Apply uses `commit confirmed 120`; a subsequent confirming commit is sent on success.
- [ ] Rollback uses Junos native rollback (RPC) with DB snapshot as fallback.
- [ ] `commit_confirmed_timeout` from `switch_juniper_config` is honored.
- [ ] ELS and legacy VLAN models are both handled (detected automatically).
- [ ] CI is green on every pushed commit.

---

## 19. Out of Scope (v3)

- Cisco NX-OS (NX-API) and Dell OS10 (REST) clients.
- SSH + CLI fallback for unrecognized vendors.
- Per-switch config locking to prevent concurrent apply conflicts.
- Full QoS / PFC policy-map management (v2 detects PFC state; v3 manages it end-to-end).
- IB switch programming (NVIDIA UFM API — separate driver, separate scope).
- BGP / routing table configuration.
- Switch firmware upgrade orchestration.
- Automated fabric remediation (detect drift from desired state and auto-apply).
- Key rotation tooling beyond the manual `clustr re-encrypt-switch-keys` subcommand.
