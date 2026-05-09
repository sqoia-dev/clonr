# Changelog

## Unreleased — Sprint 38 Bundle A (PROBE-3 + EXTERNAL-STATS + STAT-EXPIRES)

Three coordinated additions that let clustr-serverd monitor a node before
it is enrolled, during deploy, and after it is broken — without
depending on clustr-clientd.

### `PROBE-3` — three reachability probes per node, no clientd required

New `internal/server/stats/external/probes.go`. Each cycle the goroutine
pool runs three independent probes per node:

- **ping** — shells out to `/usr/bin/ping -c1 -W2 -n` against the
  primary interface IP. We deliberately avoid the raw-socket
  `golang.org/x/net/icmp` path so clustr-serverd does not need
  `CAP_NET_RAW`. Hostname/IP arguments pass through a strict
  validator before reaching argv (no shell metacharacters, no
  whitespace, no newlines).
- **ssh** — pure `net.DialTimeout` to TCP/22 + `bufio.ReadString('\n')`,
  matching the `SSH-2.0-` (and transitional `SSH-1.99-`) banner
  prefix. No clients, no key exchange, no auth.
- **ipmi_mc** — `ipmi-sensors --no-output --session-timeout=2000` over
  LAN+ to the BMC IP from `bmc_config_encrypted`. We only inspect
  the exit code; success means "the BMC's mc info handshake worked".

Result is three booleans + a `checked_at` timestamp written to the
new `node_external_stats` table with `source='probe'`. Each probe is
independent: a ping failure never short-circuits the SSH or IPMI
probe.

### `EXTERNAL-STATS` — agent-less BMC + SNMP collectors

Same package, new files:

- `bmc.go` — wraps `internal/ipmi.FreeIPMIClient.Sensors()` so a full
  `ipmi-sensors` sweep is captured per cycle. Decrypted creds come
  from `bmc_config_encrypted` via the existing path. Failure is
  recorded as `payload.error` instead of a missing row.
- `snmp.go` — gosnmp v1 wrapper. v2c GET against a configurable OID
  list, two-second per-target timeout. v3 USM and traps are out of
  scope for this sprint.
- `pool.go` — the goroutine pool (default 20 workers, 60-second
  cadence). Buffered job channel sized to `Workers` so a slow node
  holds at most one slot at a time.

Exposed via `GET /api/v1/nodes/{id}/external_stats`:

```json
{
  "probes":  { "ping": true, "ssh": false, "ipmi_mc": true,
               "checked_at": "2026-05-09T11:59:50Z" },
  "samples": {
    "bmc":  { "sensors": { "CPU Temp": { "value": "42", "unit": "C" } } },
    "snmp": { "samples": { "1.3.6.1.2.1.1.3.0": { "value": "12345", "type": "ticks" } } },
    "ipmi": null
  },
  "last_seen":  "2026-05-09T11:59:55Z",
  "expires_at": "2026-05-09T13:00:00Z"
}
```

A never-polled node returns 200 with all top-level fields nil so the UI
can render "not yet polled" instead of error-handling a 404.

Tunables (env, read once at startup):
`CLUSTR_EXTERNAL_POOL_WORKERS`, `CLUSTR_EXTERNAL_POOL_CADENCE_SECONDS`,
`CLUSTR_EXTERNAL_PROBE_TTL_MINUTES`, `CLUSTR_EXTERNAL_BMC_TTL_MINUTES`,
`CLUSTR_EXTERNAL_SNMP_TTL_MINUTES`, plus
`CLUSTR_EXTERNAL_POOL_DISABLE` / `CLUSTR_EXTERNAL_SKIP_BMC` /
`CLUSTR_EXTERNAL_SKIP_SNMP` / `CLUSTR_EXTERNAL_SKIP_PING`.

### `STAT-EXPIRES` — `expires_at` on stats writes

Migration `106_node_stats_expires_at.sql` adds a nullable
`expires_at INTEGER` (Unix seconds) column to `node_stats` plus a
partial index on `expires_at IS NOT NULL`. Migration
`107_node_external_stats.sql` creates the new `node_external_stats`
table.

Semantics:
- `expires_at IS NULL` — the long-standing behaviour for
  clientd-pushed streaming metrics. Sample is "current" forever, the
  existing 7-day retention sweeper still wins.
- `expires_at <= now()` — sample is stale. New `IncludeExpired bool`
  flag on `QueryNodeStatsParams` defaults to false, so "current"
  reads (alert engine, per-node UI, Prometheus exposition cache)
  silently drop the row. Historical reads opt back in.

A daily sweeper (`runExternalStatsSweeper`) deletes both expired
`node_external_stats` rows and TTL-bounded `node_stats` rows whose
`expires_at` has elapsed.

### Tests added

- `internal/server/stats/external/probes_test.go` — argv tables for
  ping + ipmi_mc, banner regex against good/bad/edge SSH banners,
  partial-failure independence, empty-targets-no-runner-call.
- `internal/server/stats/external/bmc_test.go` — argv shape, error
  propagation, freeipmi CSV sensor parsing.
- `internal/server/handlers/external_stats_test.go` — full-payload
  round-trip with chi router, empty-state, DB-error 500, expired-row
  filtering, unknown-source forward-compatibility drop.
- `internal/db/node_external_stats_test.go` — UPSERT round-trip,
  invalid-JSON rejection, ListExternalStatsForNode expires-at
  filter, sweep idempotency, sweep leaves NULL-expires_at node_stats
  rows alone, `QueryNodeStats` honours the `IncludeExpired` toggle.

### New deps

`github.com/gosnmp/gosnmp v1.42.0`.

---

## Unreleased — Sprint 38 Bundle B (STAT-REGISTRY + IB/MegaRAID/IntelSSD plugins + SYSTEM-ALERT-FRAMEWORK)

Backend-only landing for the second half of Sprint 38: typed metric registry,
three new ergonomic stats plugins, and the operator-visible system_alerts
lifecycle (push/set/unset/expire).

### `STAT-REGISTRY` — typed metric registry

New `internal/clientd/stats/metric_registry.go` exposing
`Register(typ, name string, opts ...Option)` and a `*MetricRegistry` type.

Public API:

```go
type MetricType string  // "float" | "int" | "bool"
type MetricDecl struct {
    Type, Name, Device, Unit, Title, ChartGroup string
    Upper float64
}
type Option func(*MetricDecl)
func Device(s string) Option
func Unit(s string) Option
func Upper(v float64) Option
func Title(s string) Option
func ChartGroup(s string) Option

func NewMetricRegistry() *MetricRegistry
func (r *MetricRegistry) Register(typ MetricType, name string, opts ...Option) (MetricDecl, error)
func (r *MetricRegistry) MustRegister(typ MetricType, name string, opts ...Option) MetricDecl
func (r *MetricRegistry) Get(name, device string) (MetricDecl, bool)
func (r *MetricRegistry) All() []MetricDecl
func (r *MetricRegistry) Sample(name, device string, value float64) Sample
```

Key validation rules:
- `typ` must be one of `float`, `int`, `bool` -> `ErrMetricInvalidType`
- `name` required -> `ErrMetricMissingName`
- `Title(...)` required -> `ErrMetricMissingTitle`
- `(name, device)` is the unique key; same name + different device is allowed.

`stats.Sample` gains an optional `MetricName` field (`json:"metric_name,omitempty"`)
and `clientd.StatsSample` mirrors it on the wire.  Plugins that pre-date the
registry leave `MetricName` empty; the existing emit-by-name path keeps working.
The server uses `MetricName` to resolve unit/title/chart-group hints from the
registered `MetricDecl` without a separate dashboard config.

### `IB-PLUGIN` (sysfs variant) — `internal/clientd/stats/plugins/infiniband.go`

Reads `/sys/class/infiniband/<dev>/ports/<n>/{state,rate,link_layer,counters/*}`.
Six metrics registered under `ChartGroup("InfiniBand")`:
`ib_state`, `ib_rate_gbps`, `ib_link_layer`, `ib_port_rcv_data_bytes`,
`ib_port_xmit_data_bytes`, `ib_symbol_errors`.

Co-exists with the legacy ibstat-shelling plugin in `infiniband.go`.  Returns
nil silently on hosts without IB hardware.

### `MEGARAID-PLUGIN` — `internal/clientd/stats/plugins/megaraid.go`

LSI/Broadcom MegaRAID controller summary via `storcli` or `MegaCli`,
whichever is on PATH (preference: `storcli` -> `storcli64` -> `MegaCli`
-> `MegaCli64`).  Seven metrics registered under `ChartGroup("MegaRAID")`.
Graceful no-op on hosts with neither binary installed.

### `INTELSSD-PLUGIN` — `internal/clientd/stats/plugins/intelssd.go`

Intel enterprise SSD SMART via `isdct` (Intel Datacenter Tool) or `intelmas`
(rebrand).  Six metrics registered under `ChartGroup("Intel SSD")` including
the inverted `intel_ssd_media_wear_pct` (0 = unworn, 100 = end-of-life).

### `SYSTEM-ALERT-FRAMEWORK` — operator-visible alerts with TTL

New package `internal/server/alerts/` with `Store` + `Handler`:

```
POST /api/v1/system_alerts/push                    -- push transient alert
POST /api/v1/system_alerts/set/{key}/{device}      -- set/upsert durable alert
POST /api/v1/system_alerts/unset/{key}/{device}    -- clear active alert
GET  /api/v1/system_alerts                         -- list current
```

DB migration `108_system_alerts.sql` adds the table (sequenced after Bundle A's
`106_node_stats_expires_at.sql` and `107_node_external_stats.sql`):

```sql
CREATE TABLE system_alerts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT    NOT NULL,
    device      TEXT    NOT NULL,
    level       TEXT    NOT NULL,    -- info | warn | critical
    message     TEXT    NOT NULL,
    fields_json TEXT,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER,             -- NULL = no expiry (set, not push)
    cleared_at  INTEGER              -- set on unset/sweep
);
CREATE UNIQUE INDEX idx_system_alerts_active_keydev
    ON system_alerts (key, device) WHERE cleared_at IS NULL;
```

The unique partial index enforces "one active alert per (key, device)" while
allowing historical cleared rows for audit.

Wire shape (matches Dinesh's `web/src/lib/types.ts SystemAlert`):

```ts
{
  id: number,
  key: string,
  device: string,
  level: "info" | "warn" | "critical",
  message: string,
  fields?: Record<string, unknown>,
  set_at: string,        // RFC3339
  expires_at?: string    // RFC3339 (only on push)
}
```

Lifecycle:
- `Push` is for fire-and-forget alerts that auto-clear after TTL (default 5m).
  Repeated Push calls on the same `(key, device)` upsert; the row count does
  not grow under retry.
- `Set` is for durable alerts with no expiry (e.g. host_unreachable).
- `Unset` stamps `cleared_at`.
- A 30s sweeper goroutine started in `StartBackgroundWorkers` stamps
  `cleared_at` on rows whose `expires_at` has passed.

Generalises the rule-engine alerts table (#133): `system_alerts` is
operator-visible state with TTL; the existing `alerts` table stays for
rule-engine evaluations.

### Tests

- `metric_registry_test.go` — register+lookup roundtrip, duplicate-name-different-device,
  duplicate-(name,device) collision, missing Title validation, invalid type,
  empty name, All() ordering, Sample() ergonomics, MustRegister panic paths.
- `plugins/infiniband_test.go` — fixture-tree parser tests against a mock
  /sys/class/infiniband, including 2-dev x 2-port cross-product.
- `plugins/megaraid_test.go` — graceful no-op when no CLI present, storcli
  JSON parser fixture, MegaCli line-counter fixture, all-metric registration.
- `plugins/intelssd_test.go` — graceful no-op, JSON-array + JSON-object
  envelope parsing, unit-suffix stripping, inversion of MediaWearIndicator.
- `system_alerts_test.go` — push expires after TTL, set->unset roundtrip,
  push idempotency, different devices stay separate, invalid level / empty
  key validation, Push->Set->Unset state transition, fields JSON roundtrip.

### Out of scope

- Wiring the new ergonomic plugins into `internal/clientd/client.go` —
  intentional.  The legacy `infiniband.go` (ibstat) and `megaraid.go`
  (storcli) plugins continue to drive on-node collection; the new
  registry-aware plugins are available for plugin authors to opt into.
- Web UI consumption of `chart_group` and `metric_name` foreign-key — that
  belongs to Dinesh's parallel UI track.
- PROBE-3 / EXTERNAL-STATS / STAT-EXPIRES (Bundle A — separate branch).

## Unreleased — Sprint 34 Bundle A (BOOT-POLICY + BOOT-SETTINGS-MODAL backend + HOSTLIST)

Three cohesive backend additions for Sprint 34:

### `pkg/hostlist` — pdsh-style range parser

New `pkg/hostlist/` package with `Expand(string) ([]string, error)` and
`Compress([]string) string`. Handles single brackets (`node[01-12]`),
zero-padded ranges (`node[001-128]`), comma-joined ranges
(`node[01-04,08,12-15]`), out-of-order specifications
(`node[03,01,02]`), top-level commas, and multi-bracket cross-products
(`rack[1-3]-node[01-12]` = 36 names).

Wired into:

- `internal/server/handlers/nodes.go:ListNodes` — `GET /api/v1/nodes?names=<pattern>`
  filters by hostname against the expanded set. Unknown names surface in the
  `X-Clustr-Unmatched-Hostnames` response header so the UI can display
  "12 of 14 matched (compute13, compute14 unknown)".
- `cmd/clustr/group.go` — `add-member` and `remove-member` accept a hostlist
  pattern as the second positional arg. Strict expansion: every name must
  resolve, otherwise the command errors before any membership change.

### `BOOT-POLICY` — explicit per-node BootOrderPolicy field

`NodeConfig.BootOrderPolicy` (one of `auto` / `network` / `os`) replaces the
v0.1.22 reactive `RepairBootOrderForReimage`. Threaded from cfg →
`bootloaderCtx.BootOrderPolicy` → `runGrub2InstallEFIInChroot` → new
`ApplyBootOrderPolicy(ctx, policy)`.

- `auto` / `network` / `""` preserve the v0.1.22 PXE-first repair semantics
  (back-compat for legacy nodes; `auto` is the migration default).
- `os` reorders BootOrder so an OS entry leads, with the first PXE entry
  moved to second position. Used for login / storage / service nodes that
  cold-boot from disk by default and only PXE on demand.

`RepairBootOrderForReimage` is preserved as a thin shim over
`ApplyBootOrderPolicy("auto")` so existing callers and tests compile.

Schema: migration `105_node_boot_policy_settings.sql` adds three columns to
`node_configs`: `boot_order_policy` (CHECK constrained, default `'auto'`),
`netboot_menu_entry` (nullable TEXT), `kernel_cmdline` (nullable TEXT).

### `BOOT-SETTINGS-MODAL` backend

New `NodeConfig.NetbootMenuEntry` and `NodeConfig.KernelCmdline` fields wired
into `internal/server/handlers/boot.go:generateDiskBootScript` so when a
deployed node's PXE-boot script is served:

- `NetbootMenuEntry` (a row id from `boot_entries`, validated server-side)
  becomes the auto-selected default item in the iPXE menu. A dangling /
  disabled entry is degraded to "fall back to default disk-boot menu" with
  a logged warning — never strands the node.
- `KernelCmdline` is appended verbatim to the kernel cmdline of any chained
  entry. Use cases: serial console pinning (`console=ttyS0,115200n8`),
  one-shot debug flags (`nomodeset`).

`internal/pxe/boot.go` exports a new `GenerateDiskBootScriptWithSettings`
function (the existing `GenerateDiskBootScript` becomes a thin shim with
`persistedEntry=nil, persistedKernelCmdline=""` for back-compat). Both BIOS
and UEFI iPXE templates were extended with a `:persisted` label that fires
when `PersistedEntry` is set.

### Wire shape — `PUT /api/v1/nodes/{id}/boot-settings`

```jsonc
PUT /api/v1/nodes/{id}/boot-settings
Content-Type: application/json

{
  "boot_order_policy":  "network",             // optional; "auto"|"network"|"os" or "" (clear→auto)
  "netboot_menu_entry": "rescue-entry-uuid",   // optional; must reference boot_entries.id (or "" to clear)
  "kernel_cmdline":     "console=ttyS0,115200n8" // optional; ≤4096 bytes, no NUL (or "" to clear)
}

→ 200 OK { ...full sanitized NodeConfig... }
→ 400 Bad Request — invalid policy / oversize cmdline / NUL byte / dangling entry id
→ 404 Not Found — node id unknown
```

All three fields are pointer-typed in the request struct
(`api.UpdateNodeBootSettingsRequest`). Pointer-nil = "leave alone".
Empty string non-nil = "clear" (`boot_order_policy` "" normalises to "auto"
because the column is NOT NULL). Non-empty non-nil = "set".

Audit: every successful PUT records before/after via the existing
`AuditActionNodeUpdate` so operators have a write-trail for boot policy
changes (these decisions silently affect every future reimage).

Auth: admin-only. Group-scoped operators can edit a node's hostname / image
via `PUT /api/v1/nodes/{id}` but not its boot routing — boot settings
have cluster-wide blast radius.

### Tests

- `pkg/hostlist/hostlist_test.go` (new) — 22 cases covering plain names,
  contiguous + gapped + out-of-order ranges, multi-bracket cross-products,
  zero-pad preservation, large ranges (128-element), error paths
  (unmatched brackets, empty groups, reversed ranges, non-numeric, leading /
  trailing dashes, double commas), and round-trip Compress(Expand(x))
  invariants.
- `internal/deploy/bootloader_test.go`:
  - `TestApplyBootOrderPolicy_BIOSNoOp` — every policy value (including
    "garbage") is a no-op on BIOS hosts (CI default).
  - `TestBootOrderArgs_PolicyArgvTranslation` — pins the exact
    `efibootmgr -o <BOOT0001>,<BOOT0002>,...` argv contract via the
    `bootOrderArgs` helper without spawning a real efibootmgr.
  - `TestApplyBootOrderPolicy_UnknownOnUEFI` — gates the unknown-policy
    error on UEFI hosts only (skipped on BIOS CI).
- `internal/server/handlers/nodes_boot_settings_test.go` (new) — 8 cases
  covering happy-path policy set, empty-string normalisation to auto,
  invalid policy rejection, kernel_cmdline length cap and NUL rejection,
  netboot_menu_entry dangling-reference rejection, netboot_menu_entry
  happy path, pointer-semantics preservation, and 404 for unknown node id.

### Out of scope (Bundle B / UI)

- IPMI / BMC / SOL — separate Richard dispatch on `feat/sprint-34-bundle-b`.
- Web UI for the Boot Settings modal + Hostlist filter input — separate
  Dinesh dispatch on `feat/sprint-34-ui-a`.

---
## Unreleased — Sprint 34 Bundle B

IPMI/BMC core (IPMI-MIN), idempotent BMC reset in deploy (BMC-IN-DEPLOY),
and the WebSocket Serial-over-LAN backend bridge (SERIAL-CONSOLE backend).
Bundle A (BOOT-POLICY / BOOT-SETTINGS / HOSTLIST) is dispatched separately;
VNC-CONSOLE is deferred until real BMC iKVM lab hardware is available.

### Added

- **FreeIPMI wrapper (`internal/ipmi/freeipmi.go`)**: typed
  `FreeIPMIClient` wrapping `ipmi-power`, `ipmi-sel`, `ipmi-sensors` with
  a runner abstraction for unit-testable argv composition. Parsers for
  the comma-separated output produced by `--no-header-output
  --comma-separated-output --output-event-state`.
- **Privhelper IPMI verbs (`cmd/clustr-privhelper/ipmi.go`)**: six new
  verbs — `ipmi-power`, `ipmi-sel`, `ipmi-sensors`, `ipmi-lan-set`,
  `ipmi-lan-get`, `ipmi-sol-activate`. BMC credentials are passed via
  stdin as a one-line JSON envelope so the password never appears in
  `/proc/<pid>/cmdline`. Argv is rebuilt internally per-verb from a
  static allowlist of fields/actions; raw flags from callers are never
  honoured.
- **Privhelper Go client (`internal/privhelper/ipmi.go`)**: typed
  `IPMIPower`, `IPMISEL`, `IPMISensors`, `IPMILANGet`, `IPMILANSet`,
  `IPMILANSetPassword` functions wrapping the new verbs.
- **Admin REST endpoints (`internal/server/handlers/ipmi_admin.go`)**:
  - `POST /api/v1/nodes/{id}/ipmi/power/{action}` —
    `{status,on,off,cycle,reset}`
  - `GET /api/v1/nodes/{id}/ipmi/sel`
  - `DELETE /api/v1/nodes/{id}/ipmi/sel`
  - `GET /api/v1/nodes/{id}/ipmi/sensors`

  All admin-scoped; all federated through `clustr-privhelper` so BMC
  creds stay off /proc cmdline. The legacy `/power /sel /sensors` routes
  (in-tree ipmitool wrapper) are unchanged.
- **CLI: `clustr ipmi node <id> {power,sel,sensors}`**
  (`cmd/clustr/ipmi_admin.go`): hits the admin /ipmi/* endpoints. The
  existing `clustr ipmi power|sel|sensors` raw-host shape (no node
  lookup, direct BMC) is preserved.
- **BMC-IN-DEPLOY (`internal/deploy/bmc.go`)**: idempotent BMC LAN reset
  that reads `ipmitool lan print 1`, diffs against the desired
  `api.BMCNodeConfig`, and writes only the fields that differ.
  Re-deploying a node with the same config is a no-op against the BMC
  (verified by unit test
  `TestApplyBMCConfigToHardware_SecondApply_NoWrites`). Wired into
  `internal/deploy/finalize.go:applyNodeConfig` as the first step of the
  BMC block; the existing `applyBMCConfig` (user-record write on slot 2)
  runs after.
- **SERIAL-CONSOLE backend bridge
  (`internal/server/handlers/console_sol.go`)**: `GET
  /api/v1/nodes/{id}/console/sol` (WebSocket, admin scope). Spawns
  `ipmitool sol activate` via `clustr-privhelper` with a PTY;
  multiplexes stdin/stdout to the WS connection. Single-active-session
  per node — a second connect closes the first cleanly via `cancel()`
  on the bridge context. Wire shape: line-mode stdin, raw-byte stdout,
  both as `websocket.BinaryMessage` (xterm.js handles the ANSI/VT
  stream end-to-end). 30s ping interval matches the existing UX-12
  keepalive pattern.

### Tests

- `internal/ipmi/freeipmi_test.go` — argv composition for
  power/sel/sensors, CSV parser correctness, runner mock (no real
  binary).
- `cmd/clustr-privhelper/ipmi_test.go` — verb arg validation, allowlist
  parity locks, `isSafeBMCField` injection rejection, channel range
  check.
- `internal/deploy/bmc_test.go` — `parseLanPrint`, `planBMCDiff`
  (all-differ / no-diff / partial-diff), `applyBMCConfigWithRunner`
  first-apply-writes / second-apply-no-writes idempotency, stable argv
  sequence.
- `internal/server/handlers/console_sol_test.go` — WS bidirectional
  byte forwarding (stdout → WS binary frame, WS → subprocess),
  single-active-session supersedure, no-BMC rejection, `resolveSOLCreds`
  priority.
- `internal/server/handlers/ipmi_admin_test.go` — handler dispatch
  shape, privhelper failure → 502, no-BMC rejection.
- `cmd/clustr/ipmi_admin_test.go` — URL composition + argv parsing for
  `clustr ipmi node ...`.

### Privilege boundary notes

- Every live-IPMI invocation from `clustr-serverd` (the unprivileged
  "clustr" user) routes through `clustr-privhelper`, per the standing
  memory rule. No new polkit rules, no sudoers entries.
- `BMC-IN-DEPLOY` runs in the initramfs as root and exec's `ipmitool`
  directly — the privhelper's privilege boundary is moot pre-OS.

## Unreleased — Sprint 33 STREAM-LOG-PHASE

Phase-tagged install-log streaming so the web UI's live tail can colour-group
/ filter by deploy phase. The hardened sprint plan (`docs/SPRINT-PLAN.md`
Sprint 33) confirmed both the on-node POST pipe (`pkg/client/logger.go` →
`POST /api/v1/logs`) and the SSE broadcaster
(`internal/server/handlers/logs.go` → `GET /api/v1/logs/stream`) already
exist. The actual gap was per-line phase metadata: every entry shipped
phase-blind, so the UI had no signal to group on. This change adds that
missing field, threads it through every existing phase-transition site in
the deploy CLI, and keeps the wire 100% backward-compat (`omitempty`).

### Added

- **`api.LogEntry.Phase` field** (`pkg/api/types.go`): new `Phase string` on
  the wire, `omitempty`. Older servers / clients that don't know about it
  ignore it; newer consumers (Dinesh's Install Log tab) colour-group the
  live stream on it.
- **`RemoteLogWriter.SetPhase()` / `Phase()`** (`pkg/client/logger.go`):
  mirrors the existing `SetComponent` / `SetHostname` / `SetNodeMAC` pattern.
  Stamps every subsequent zerolog line with the supplied phase. Buffer is
  not retroactively rewritten — only lines emitted *after* the call get the
  new phase, matching the UX expectation that the phase reflects "what was
  happening when this log was emitted".
- **Inline `phase` field override** (`pkg/client/logger.go:parseZerologLine`):
  a `phase` field present on the zerolog JSON line takes precedence over the
  writer-level phase. Mirrors the existing `component` precedence so callers
  can override per-line via `deployLog.Info().Str("phase","X").Msg(…)`
  without touching `SetPhase()` (the deploy `progressFn` already emits this).
- **Phase wiring in `cmd/clustr/main.go`**: `runAutoDeployMode` and
  `runAutoDeployImage` now call `remoteWriter.SetPhase(...)` at every phase
  boundary — `hardware`, `register`, `bios`, `wait-for-assign`, `image-fetch`,
  `preflight`, `multicast`, the deployer-emitted `partitioning` /
  `formatting` / `downloading` / `extracting`, plus `finalizing` and
  `deploy-complete`. The non-auto interactive deploy path uses the existing
  inline `Str("phase", phase)` pattern in `progressFn`, which the new
  override-precedence rule above picks up automatically.

### Tests

- `pkg/client/logger_test.go` (new): unit-level coverage of the phase-tagging
  contract via `httptest.NewServer` standing in for the real `POST
  /api/v1/logs` endpoint. Covers (a) the hardened-plan acceptance test
  (`SetPhase("partitioning") → Phase=="partitioning"`), (b) phase
  transitions across multiple Writes, (c) inline-phase override, (d) empty
  phase by default for non-deploy contexts, (e) concurrent SetPhase / Write
  with no race, (f) phase survives the WARN/ERROR urgent-flush path.

### Wire shape (for Dinesh's UI consumer)

The SSE event payload at
`GET /api/v1/logs/stream?component=deploy&mac=<primary-mac>` is now:

```jsonc
data: {
  "id": "<uuid>",
  "node_mac": "aa:bb:cc:dd:ee:ff",
  "hostname": "compute-01",
  "level": "info",
  "component": "deploy",
  "phase": "partitioning",       // NEW — empty for pre-Sprint-33 streams
  "message": "sgdisk --zap-all",
  "fields": { ... },
  "timestamp": "2026-05-09T..."
}
```

`phase` is `omitempty` so the UI must treat empty/missing as "unknown / no
group". Phase strings the UI should expect (today): `hardware`, `register`,
`bios`, `wait-for-assign`, `image-fetch`, `preflight`, `multicast`,
`partitioning`, `formatting`, `downloading`, `extracting`, `finalizing`,
`deploy-complete`. The set is fixed-but-extensible — UI should treat unknown
phases as a default colour rather than discard them.

### Out of scope

- DRACUT-REGEN, MULTICAST-JITTER, PRE-ZERO — separate Sprint 33 dispatch
  (Richard's `feat/sprint-33-deploy-trio` branch).
- The web-side log viewer (Dinesh's parallel branch).
- Durable per-line phase persistence in `node_logs` — phase rides on the
  in-memory broker only; durable rows stay phase-blind for now (the UI
  consumes the live SSE stream, not the historical query). Cheap to add a
  column later if we need it; deferring keeps the migration surface small.
## Unreleased — Sprint 33 deploy-pipeline trio

Three small deploy-pipeline hardenings that share a CI run and ship together.
Each one targets a discrete failure class observed across the v0.1.10 →
v0.1.21 patch storm (see `docs/SPRINT-PLAN.md` Sprint 33 source notes).

### Added

- **DRACUT-REGEN: per-kver portable initramfs regeneration**
  (`internal/deploy/regen_initramfs.go`, `internal/deploy/finalize.go`):
  the in-chroot dracut call in `applyBootConfig` is replaced with a
  per-kernel-version loop driven by `runDracutInChroot`. For every
  `vmlinuz-<kver>` in the deployed `/boot`, it runs
  `chroot <root> dracut -fv -N --lvmconf --force-add mdraid --force-add lvm
  /boot/initramfs-<kver>.img <kver>`. The new portability flags
  (`--lvmconf`, `--force-add mdraid`, `--force-add lvm`) ensure an image
  captured on virtio (no md, no lvm in the running kernel modules) boots
  correctly on a target with a different storage controller (PERC RAID,
  SATA AHCI, mdraid root, LVM root). Per-kver iteration also threads
  progress through the existing `runAndLog` so the v0.1.22 install_log
  heartbeat surfaces each kver's progress instead of one monolithic 30-90s
  silent pause. Failures on any single kver are logged-non-fatal; the
  next kver is still attempted. New file `internal/deploy/regen_initramfs.go`
  with unit tests at `internal/deploy/regen_initramfs_test.go` covering the
  argv shape, kernel discovery (including rescue-kernel filtering), empty
  /boot, and missing /boot.

- **MULTICAST-JITTER: deterministic 0-60s pre-`/deploy-complete` sleep**
  (`cmd/clustr/multicast_jitter.go`, `cmd/clustr/main.go`): when 256 nodes
  finish a multicast image transfer in the same one-second window, every
  node POSTs `/deploy-complete` simultaneously and the server's request
  rate spikes to 256/s, spilling into request latency and (on the small
  pkg.sqoia.dev tier) timing out some POSTs. Each multicast-completed
  node now sleeps a deterministic 0-60s offset (FNV-1a-hash of primary
  MAC seeds a `math/rand` source; offset = `Intn(60)`) before posting.
  Determinism is critical for retries: a node that failed at offset=17
  retries at offset=17, not at a fresh random offset that would re-bunch
  the herd at the retry attempt. Unicast deploys are not affected
  (already serialized by the per-blob HTTP byte-rate limit). Unit tests at
  `cmd/clustr/multicast_jitter_test.go` cover the no-op-on-unicast path,
  the [0, 60s) bound, per-MAC determinism, cross-MAC distribution, and the
  `multicastJitterMaxSeconds` const contract.

- **PRE-ZERO: `dd if=/dev/zero of=$disk bs=1M count=10 conv=fsync` before
  `wipefs -a`** (`internal/deploy/rsync.go`): `diskWipeSequence` now
  prepends a 10 MiB dd zero pass before the existing `wipefs -a` →
  `sgdisk --zap-all` chain. `wipefs` only erases recognised
  filesystem/RAID superblocks; raw stage-1 GRUB bytes / MBR partition
  tables / boot-sector code are invisible to it. Without dd-first, a
  previously imaged disk can chain-load a stale GRUB stage 1 on the next
  boot before the freshly written bootloader runs. Idempotent (zeroing
  the same 10 MiB twice is fine), best-effort (logged-non-fatal on
  failure — wipefs and sgdisk remain the authoritative gates).
  `internal/deploy/disk_wipe_test.go` extended with `TestDiskWipeSequence_DDFirst`
  and `TestDiskWipeSequence_DDBeforeWipefs` plus updated existing tests
  to the new 3-command sequence.

### Source

`clustervisor` was the cross-reference: `ClonerInstall.pm` runs `dd ...
count=10` before partitioning, regenerates the initrd in-chroot per kernel
via `_create_system_files_el`, and uses jittered post-multicast callbacks.
Each behaviour was a known production-deploy hardening that mainline
clustr was missing.

### Out of scope

- `STREAM-LOG-PHASE` and `STREAM-LOG-UI` (Sprint 33 observability half) ship
  in a separate dispatch.

## v0.1.22 — 2026-05-08

LDAP readiness staleness fix. v0.1.21 made LDAP work end-to-end on
freshly-deployed nodes (sssd active, `id rromero` returns uid=10001 with
clustr-admins membership) but the UI kept showing both nodes as "LDAP Failed"
because `node_configs.ldap_ready=0` was stamped during the verify-boot
phone-home — a one-shot probe that fired exactly once at first boot. If sssd
happened to be slow or broken at that single moment, the row was wrong
forever; nothing else ever wrote it. Today's manual `UPDATE node_configs SET
ldap_ready=1` on cloner exposed the class of bug. v0.1.22 makes
`ldap_ready` a continuously-refreshed value driven by clientd's existing
60 s heartbeat, plus an admin force-reverify endpoint for instant feedback.

### Fixed

- **Heartbeat-driven LDAP readiness rewrite** (`internal/clientd/ldap_health.go`, `internal/clientd/heartbeat.go`, `internal/clientd/messages.go`, `internal/server/handlers/clientd.go`): clientd now runs a cheap local LDAP probe on every heartbeat (5 s timeout, single `systemctl is-active sssd` + `sssctl domain-list` + `sssctl domain-status <domain>`), packages the result as `LDAPHealthStatus{Configured, Active, Connected, Domain, Detail}`, and piggybacks it on the existing `HeartbeatPayload`. Server's `handleHeartbeat` decodes the field and rewrites `node_configs.ldap_ready` + `ldap_ready_detail` when the node is LDAP-configured (`LDAPNodeIsConfigured` true). Nodes never deployed with LDAP keep `ldap_ready=NULL` so `pkg/api.NodeConfig.State()` continues to treat them as "no LDAP expected" rather than "LDAP failed". Result: any future node where sssd recovers post-first-boot self-heals on the next 60 s tick — no manual SQL, no reimage required.
- **Admin force re-verify endpoint** (`internal/server/handlers/clientd.go`, `internal/server/clientdhub.go`, `internal/server/server.go`): new `POST /api/v1/nodes/{id}/verify-ldap` (admin scope) sends a `ldap_health_request` server→node WebSocket message; clientd runs the probe immediately and replies with `ldap_health_result`. Server applies the snapshot to `node_configs` AND returns `VerifyLDAPResponse{ready, configured, active, connected, domain, detail, applied}` synchronously (10 s timeout). Round-trip plumbed through a new `ldapHealthRegistry` on the hub matching the existing disk-capture / bios-read pattern. Use case: operator clicks "Re-verify LDAP" in the UI after fixing sssd config and gets instant feedback instead of waiting up to 60 s for the next heartbeat tick.

### Tests

- `internal/clientd/ldap_health_test.go`: pure-function coverage of `parseDomainList` (single/multi/header-line/whitespace), `parseDomainStatus` (online/offline/case-insensitive/no-marker), and `firstNonEmptyLine`. The full probe runs `systemctl`/`sssctl` and is exercised end-to-end on cloner; the unit test covers the parsing surfaces that produce the operator-facing `Detail` string.

### Operational

- The probe shells out to `sssctl` (single dnf-managed binary, present on every clustr-deployed image with LDAP). No new dependencies. Bounded 5 s total — a hung sssd cannot delay heartbeats beyond that.
- The fix is forward-compat: `LDAPHealth` is `omitempty` on the wire, so a v0.1.22 server still accepts heartbeats from older clientd binaries (it just never refreshes `ldap_ready` for those nodes — same as today). When clientd is upgraded, the next heartbeat catches up.

## 0.1.17 — 2026-05-08

Fix initramfs deploy hang: PTY backpressure blocks first clustr write in
screen-based logging path, causing a 18+ minute delay before any HTTP
request reaches the server. Root cause confirmed via server access logs
(zero node requests for 18min, then a 401 on register) and init.log
analysis (zero bytes from clustr binary despite screen session running).

### Critical

- **Initramfs screen session blocks on PTY backpressure** (`internal/server/handlers/scripts/initramfs-init.sh`): when `clustr.ssh=1`, the init script ran clustr inside a detached screen session with `clustr 2>&1 | tee -a $LOG`. In detached mode, screen's internal scrollback buffer fills up (no attached client draining the PTY master), which stalls the PTY slave write, which blocks `tee`'s `read()` from the pipe, which fills the pipe buffer, which blocks clustr's first `fmt.Fprintln(os.Stderr)` write before a single byte reaches `init.log`. The result: zero log output, zero server calls, for as long as screen's buffer takes to be drained (never, in detached mode). Fix: replace `2>&1 | tee -a "$LOG"` with `>> "$LOG" 2>&1` inside the screen `sh -c` payload. Clustr now appends directly to `init.log` (bypassing the PTY entirely for I/O), while screen's PTY remains available for live operator attachment via `screen -r clustr-deploy`. Backpressure path is eliminated; log writes are always non-blocking.

### Operational

- `screen -r clustr-deploy` still works for live session attachment; the difference is that clustr's output goes to `init.log` directly rather than through the PTY/tee chain. Both the log file and the attached screen session show output correctly.

## 0.1.16 — 2026-05-08

Live-deploy DNS fix folded into the LDAP-functional path. v0.1.15 shipped the
LDAP infrastructure (DIT repair, verify-boot gating, deployed_ldap_failed PXE
routing) but Gilfoyle's end-to-end validation showed deploys still hung 35+
minutes mid-finalize. Root cause: chroot DNS injection was happening from
inside the chroot itself — a self-bind no-op against a baked-in unreachable
nameserver — so every `dnf install` blocked on package-fetch DNS resolution.

### Critical

- **`install_instructions` script opcode hangs on chroot DNS** (`internal/deploy/chroot_mounts.go`, `internal/deploy/inchroot.go`): Rocky9.7's `install_instructions` script payload tried to set up its own chroot mounts with `mount --bind /etc/resolv.conf /etc/resolv.conf` — but the executor runs the payload via `chroot <target_root> /bin/sh <script>`, so that bind ran INSIDE the chroot. It was a self-bind against the chroot's own broken `/etc/resolv.conf` (image had `nameserver 10.0.2.3`, the QEMU NAT DNS, unreachable from the deploy network). Every `dnf -y install sssd ...` blocked on DNS resolution for tens of seconds per package, accumulating into a 35+ minute deploy stall before the chroot exec eventually died on context cancellation. Fix: new `setupChrootMounts(targetRoot)` helper in `chroot_mounts.go` does the bind from the HOST side BEFORE chrooting — proc, sysfs, recursive-bind /dev (so devpts/shm come along), and crucially `mount --bind /etc/resolv.conf <targetRoot>/etc/resolv.conf` so the host's working resolver is visible inside the chroot. `applyScript` calls `setupChrootMounts` at the top with deferred cleanup; script payloads no longer need any mount lines (the existing self-bind ones in Rocky9.7's payload become harmless no-ops). Helper handles the symlink-resolv.conf case (replaces with regular file before bind so systemd-resolved-stub images work) and the missing-resolv.conf case (creates placeholder before binding). Cleanup uses `MNT_DETACH` so a leaked fd in the chroot doesn't strand the deploy host's mount table.
- **Script output now streams to deploy log** (`internal/deploy/inchroot.go`): pre-fix `applyScript` used `cmd.CombinedOutput()` — output buffered in memory until the process exited, so a hanging `dnf install` showed nothing in the log for 35min, then dumped the full transcript on timeout. Replaced with `StdoutPipe`/`StderrPipe` + `bufio.Scanner` per-line logging tagged with `step=N stream=stdout|stderr`, plus a 50-line tail buffer that gets embedded in the error message on non-zero exit. Operators now see "still on dnf install package N of 47" in real time. Belt-and-suspenders for #258 SCREEN-CAPTURE — full operator-visible terminal capture is still a separate task, but per-step stdout streaming closes the worst-case "deploy hung 35min, no signal" gap.

### Tests

- `internal/deploy/chroot_mounts_test.go::TestSetupChrootMounts_ResolvConfBind`: plants a broken `nameserver 10.0.2.3` in the target rootfs, runs `setupChrootMounts`, verifies the file now reads as the host's `/etc/resolv.conf` content (bind succeeded), runs cleanup, verifies the broken planted content reappears (bind torn down). Skipped when not running as root (mount(2) requires CAP_SYS_ADMIN).
- `internal/deploy/chroot_mounts_test.go::TestSetupChrootMounts_NoExistingResolvConf`: pins the placeholder-creation path for minimal images that ship without `/etc/resolv.conf`.
- `internal/deploy/chroot_mounts_test.go::TestSetupChrootMounts_SymlinkResolvConf`: pins the symlink-replacement path for images where `/etc/resolv.conf` is a systemd-resolved stub link.
- `internal/deploy/chroot_mounts_test.go::TestSetupChrootMounts_PartialFailureCleansUp`: pins the cleanup-on-partial-failure path so a failed mid-setup mount doesn't leak earlier mounts on the deploy host.

### Operational

- The `mount --bind /etc/resolv.conf /etc/resolv.conf` lines in existing `install_instructions` payloads (e.g. Rocky9.7) become harmless no-ops with this fix in place — no DB migration required. Future seeded `install_instructions` defaults can drop those lines (the executor now owns that lifecycle).
- This fix unblocks the LDAP path that v0.1.15's three-layer repair targeted: SSSD packages + authselect now actually install in the in-chroot pass rather than timing out, so post-deploy nodes converge to `deployed_verified` instead of `deployed_ldap_failed`.

## 0.1.15 — 2026-05-07

LDAP-functional cut + two live-deploy blockers folded in (FIX-EFI #225 +
RPM-UPDATE-1 #225).  v0.1.14 shipped a UI fix only (stale reimage badge);
this release is the LDAP-broken-at-three-layers repair pass uncovered by
Gilfoyle on freshly-imaged vm201/vm202, plus the boot-order regression and
update-timer mid-flight kill that surfaced during the same deploy session.

### Critical

- **`sssd.conf` `ldap_uri` pointed at the cloner's public IPv6, unreachable from nodes** (`internal/ldap/manager.go`): `detectPrimaryIP()` resolved `os.Hostname()` via `net.LookupHost` and returned `addrs[0]`. On the dual-stack cloner that races to the global IPv6 (`2600:1700:a4b0:1540:e426:4d06:ccee:53e9`); deployed nodes on the PXE provisioning subnet have no IPv6 connectivity, and slapd binds IPv4-only on `0.0.0.0:636`, so the ldaps URI was unreachable from every node. Fix: new `internalLDAPHost(cfg)` helper prefers `cfg.PXE.ServerIP` (`CLUSTR_PXE_SERVER_IP=10.99.0.1` in the standard install) and only falls back to `detectPrimaryIP`/`detectHostname` when no PXE server IP is configured. Used by `Manager.NodeConfig` (deploy-time `sssd.conf` generation) and `Manager.FanoutLDAPConfig` (post-Enable CA push). `detectPrimaryIP` is left intact for cert SAN generation, with a caution comment so future call sites pick the right helper.
- **`status=ready` with empty DIT — no recovery path** (`internal/ldap/manager.go`): cloner's `ldap_module_config.status=ready` since 2026-05-01 but `ldapsearch -b dc=cluster,dc=local` returns `err=32 No such object`. data.mdb (159744 bytes) holds only LMDB metadata; the data DIT was never seeded, or was seeded into an mdb file that was later overwritten/lost. `Manager.Enable` short-circuits when status is already `ready`, so the existing seed path was unreachable. Fix: new idempotent `Manager.RepairDIT` operation re-runs `seedDIT` against the live slapd instance (HealthBind first, then add-or-self-heal each canonical entry — base DN, ou=people/groups/services/policies, cn=node-reader, cn=clustr-admins). Wired up at `POST /api/v1/ldap/internal/repair-dit` (admin scope, audit event `ldap.internal.dit_repaired`). Refuses to run when module is disabled, base_dn is empty, or admin/service passwords are unavailable in both memory and DB.
- **verify-boot transitioned to `deployed_verified` even when sssd was broken** (`pkg/api/types.go`, `internal/server/handlers/nodes.go`, `internal/db/racks.go`, `web/`): pre-v0.1.15 `NodeConfig.State()` returned `NodeStateDeployedVerified` the moment `deploy_verified_booted_at` was set, regardless of the verify-boot payload. vm201/vm202 phoned home with `sssd_status=not_installed`, `pam_sss_present=false` and were stamped `deployed_verified` anyway. New state `NodeStateDeployedLDAPFailed` ("deployed_ldap_failed") plus a priority-3 gate in `State()`: when `LDAPReady != nil && !*LDAPReady`, return the new state instead of `deployed_verified`. `LDAPReady=nil` (older clients, LDAP-not-configured nodes) preserves legacy semantics. Mirrors the gate in racks.go SQL CASE so server-side state derivation matches the typed Go path. Web UI renders the new state as red "LDAP Failed" via `StatusDot.tsx`. Verify-boot log line upgraded from WARN to ERROR for visibility.
- **Sudoers drop-in still using `clonr-admins` filename + group reference** (`internal/db/migrations/104_fix_sudoers_group_cn.sql`, `internal/deploy/finalize.go`): GAP-S18-2 (#115) renamed the LDAP group CN to `clustr-admins` and added a runtime DIT migration in `seedDIT`, but `ldap_module_config.sudoers_group_cn` was never updated. `cfg.SudoersGroupCN` flows from that column into `writeSudoersDropin` as the filename AND the group reference in the rule body, so every reimage of an LDAP-enabled node kept writing `/etc/sudoers.d/clonr-admins` with `%clonr-admins ALL=(ALL) NOPASSWD:ALL` — pointing at a group that no longer exists in the DIT, granting nothing. Migration 104 is a narrow `UPDATE … WHERE sudoers_group_cn = 'clonr-admins'` (idempotent, preserves operator-set custom group names). `writeSudoersDropin` defensively unlinks any stray `/etc/sudoers.d/clonr-admins` on every deploy when the configured GroupCN is anything else, so already-deployed nodes converge on next reimage.
- **rpm-update timer restarted clustr-serverd mid-deploy, killing UDPCast blob streams** (`scripts/ops/clustr-rpm-update.sh`, `deploy/systemd/clustr-rpm-update.{service,timer}`, `nfpm.yaml`, `scripts/pkg-postinstall.sh`, `scripts/pkg-preremove.sh`) — RPM-UPDATE-1 #225: today's live deploy of vm201/vm202 was kneecapped when the host's 15-minute `clustr-rpm-update.timer` fired during finalize and `dnf upgrade -y clustr clustr-serverd` restarted the daemon mid blob-stream. Pre-fix the script went straight to `dnf upgrade` with no awareness of in-flight work — by design (the timer was a hand-rolled add-on, not packaged). Fix folds the script into the RPM (`/usr/local/sbin/clustr-rpm-update.sh`, ships `.service` + `.timer` to `/usr/lib/systemd/system/`, mirrors the autodeploy script's defer pattern). Before `dnf upgrade`, the script GETs `/api/v1/system/active-jobs` (unauthenticated, fail-open) and exits 0 if ANY of `initramfs_builds`, `image_builds`, `reimages`, `deploys`, `operator_sessions`, `pxe_in_flight` is non-empty — logging a one-line summary so operators can see in `journalctl -u clustr-rpm-update.service` why a cycle was skipped. Defer cap: 24 consecutive deferrals (~6 hours, env-overridable via `CLUSTR_RPM_UPDATE_DEFER_CAP`) before the script proceeds anyway, on the assumption that an in-flight job has stuck. Cap counter at `/var/lib/clustr/rpm-update-defer-count`, cleared on every successful update. Postinstall does NOT auto-enable the timer (preserves the existing cloner's operator-paused state); the install message tells operators to `systemctl enable --now clustr-rpm-update.timer` if they want auto-update.
- **NVRAM BootOrder repair after every UEFI deploy** (`internal/deploy/efiboot.go`, `internal/deploy/finalize.go`, `internal/deploy/bootloader_test.go`) — FIX-EFI #225: bare-metal UEFI hosts can carry stale OS NVRAM entries from a prior life of the disk (Anaconda kickstart, an older clustr release, a manual rescue session), or — on Proxmox — from pflash that survives reimage. Those entries land ahead of PXE in BootOrder and silently break future reimages: the node UEFI-iPXEs from the stale entry instead of the clustr PXE script, and operators see an "iPXE-only loop" with no obvious cause. Fix: new `RepairBootOrderForReimage` in `internal/deploy/efiboot.go` runs at the end of `runGrub2InstallEFIInChroot` (so every distro path — EL, SLES, Debian, Ubuntu — picks it up via the shared chroot helper). Function is a no-op on BIOS hosts (`/sys/firmware/efi` absent) and best-effort on UEFI: re-orders BootOrder so a PXE / IPv4 / IPv6 / Network entry leads, leaving OS entries in place (non-destructive — destructive cleanup is reserved for explicit operator action on shared-firmware hosts). PXE label heuristic centralised in `pxeEntryLabelMatch` and now matches the "Network Boot" / "UEFI: Network Card" labels emitted by HP iLO / Dell BMCs in addition to the OVMF-style "PXEv4" / "IPv4". Failure is logged and swallowed — a successful deploy is never failed because of an NVRAM tweak; the deployed node still boots via removable-media auto-discovery (`\EFI\BOOT\BOOTX64.EFI`) regardless of order. Pure-function PXE matcher unit-tested (`TestPXEEntryLabelMatch`) plus a BIOS no-op smoke test (`TestRepairBootOrderForReimage_BIOSNoOp`). Note: this is a defence-in-depth change — clustr already passes `--no-nvram --removable` to grub2-install on every distro path, so we don't add NVRAM entries ourselves; this fix targets entries inherited from the disk's prior history.
- **LDAP server cert SAN list missed `CLUSTR_PXE_SERVER_IP`** (`internal/ldap/cert.go`, `internal/ldap/manager.go`) — Codex P1 on PR #2: the v0.1.15 `ldap_uri` fix routed `NodeConfig.ldap_uri` through `internalLDAPHost(cfg)` (honors `CLUSTR_PXE_SERVER_IP=10.99.0.1`), but `Enable()` was still feeding cert SAN generation only `detectPrimaryIP()` — which on the dual-stack cloner returns the public IPv6. When the two diverge — exactly the scenario the patch targeted — nodes connect to `ldaps://10.99.0.1:636` with `ldap_tls_reqcert=demand` and reject the server cert (PXE IP not in SAN list). Net: LDAP auth would still fail post-deploy. Fix: `generateServerCert` now takes an `extraIPs []string`; `Enable()` passes `internalLDAPHost(m.cfg)` when it differs from `primaryIP`. SAN list dedups against loopback + primaryIP. Existing post-Enable `FanoutLDAPConfig()` already pushes the regenerated CA to enrolled nodes, so cert rotation reaches the fleet without additional plumbing.
- **`deployed_ldap_failed` nodes auto-reimaged on next PXE cycle** (`internal/server/handlers/boot.go`, `internal/server/handlers/boot_test.go`) — Codex P1 on PR #2: the new `deployed_ldap_failed` state was added to `NodeConfig.State()` and the verify-boot path, but the PXE boot decision in `boot.go` only disk-booted `deployed`/`deployed_verified`/`deployed_preboot`/`deploy_verify_timeout`. A node in `deployed_ldap_failed` (OS bootable, sssd broken) that ever PXE-booted again — manual reboot, persistent netboot, IPMI bootdev pxe set during operator triage — fell through into the initramfs deploy path and got silently reimaged. That discards a potentially trivially-fixable LDAP issue (transient slapd, sssd cache flush) and reverses the entire point of the new state, which is operator-triage gating. Fix: add `NodeStateDeployedLDAPFailed` to the disk-boot allowlist with a WARN log line. Operator decides whether to repair LDAP or trigger an explicit reimage.

### Tests

- `internal/ldap/internal_ldap_host_test.go`: pins `internalLDAPHost(cfg)` returns `cfg.PXE.ServerIP` regardless of host network state, falls back gracefully when unset.
- `internal/ldap/repair_dit_test.go`: pins each precondition refusal in `RepairDIT` (module disabled, base_dn empty, admin password unavailable, service password unavailable). The seed-against-running-slapd path is exercised by manual cherry-pick validation.
- `pkg/api/state_ldap_gate_test.go`: pins `State()` priority — `LDAPReady=true` → `deployed_verified`, `LDAPReady=nil` → `deployed_verified` (legacy preserved), `LDAPReady=false` → `deployed_ldap_failed` (regression guard), gate doesn't apply pre-verify-boot, ReimagePending dominates.
- `internal/db/migration_104_test.go`: pins migration 104 normalizes legacy values and preserves operator-set custom group names.
- `internal/deploy/sudoers_dropin_test.go`: pins drop-in is named after `GroupCN`, body uses the configured CN, legacy `clonr-admins` file is removed during write, but writer never deletes its own output.
- `internal/ldap/cert_test.go`: pins `generateServerCert` SAN coverage — primaryIP appears, `extraIPs` IPs appear (PXE-IP regression guard), duplicates dedup, non-IP entries (hostnames returned by `internalLDAPHost` fallback) are dropped.
- `internal/server/handlers/boot_test.go::TestServeIPXEScript_DeployedLDAPFailed_DiskBoots`: pins that a node in `deployed_ldap_failed` state (DeployVerified+LDAPReady=false) receives a disk-boot script on PXE — operator triages, no auto-reimage.

### Operational

- After upgrading the cloner to v0.1.15, run `POST /api/v1/ldap/internal/repair-dit` once to populate the empty DIT. Already-deployed nodes need to be reimaged to pick up the corrected `sssd.conf` (`ldap_uri=ldaps://10.99.0.1:636`) and the renamed sudoers file. Migration 104 runs automatically on first start.

## 0.1.14 — 2026-05-07

### Fixes

- **Stale "Reimage in progress" badge after successful redeploy** (`internal/db/db.go`, `internal/server/handlers/nodes.go`, `web/src/routes/nodes.tsx`): when an operator double-fired reimage on the same node — first request stuck in `triggered`, second request kicked off, completed, and self-closed — the older row was never transitioned. The `deploy-complete` handler used `GetActiveReimageForNode` (single-row, `ORDER BY created_at DESC LIMIT 1`) and operated on whichever row that returned. The `/api/v1/nodes/{id}/reimage/active` polling endpoint used the same single-row query, so the UI kept reading the stuck `triggered` row and rendering the "Reimage in progress" badge on a node already in `deployed_verified`. Live evidence on cloner: vm202 (`ac7fb8e3-…`) had three rows — newest `complete`, middle `triggered` (orphaned), oldest `failed`. Fix is two-front. Server: new `DB.CloseActiveReimagesForNode` bulk-transitions every non-terminal row for a node to a terminal status idempotently; `DeployComplete`, `DeployFailed`, and the first `VerifyBoot` call all invoke it so any prior cycle's leaked rows close at the next definitive "node finished provisioning" signal. Webapp: the `Reimage in progress` block now gates on `node.reimage_pending` (the canonical "is a reimage actually happening RIGHT NOW" flag the server clears in `RecordDeploySucceeded`) instead of just the reimage row's status — defence-in-depth so a future server bug never manifests as a stuck badge.

### Tests

- `internal/db/reimage_terminal_test.go::TestCloseActiveReimagesForNode`: asserts bulk-close transitions all non-terminal rows, leaves terminal rows alone, is idempotent on repeat calls, and rejects non-terminal status arguments.

### Operational

- vm202's stuck `triggered` row (`2b5fa813-…`) was cleared directly in the cloner DB so the live UI clears immediately; future occurrences of this class are auto-handled by the bulk-close path.

## 0.1.13 — 2026-05-07

### Critical

- **PXE-served initramfs ignored every rebuild since May 3** (`internal/server/handlers/boot.go`): `BootHandler.ServeInitramfs` was reading `initramfs.img` while the build pipeline writes the live image to `initramfs-clustr.img` (matches `InitramfsPath` in `internal/server/server.go` and the auto-reconcile target in `internal/server/reconcile.go`). On cloner, `initramfs.img` had been frozen at a May-3 dev build (`clustr version dev`, with the v0.1.11 `wiping existing partition table` codepath) for four days. Every initramfs rebuild after that — including v0.1.12 with the round-2 wipefs+grub2-install fix — landed on disk but never reached a PXE-booting node. ServeInitramfs now prefers `initramfs-clustr.img` and falls back to `initramfs.img` only when the live file is missing (preserves the brand-new install bootstrap path), with a WARN log on the fallback so a repeat is loud, not silent. **This is the actual root cause of the vm201/vm202 BIOS bootloader failures three earlier rounds were targeting** — the fix-code was correct, the served initramfs was stale.

### Fixes

- **Defence-in-depth — wipe partition interiors before mkfs** (`internal/deploy/rsync.go::partitionDisk`): after `sgdisk` creates the new partition table and the partition device nodes appear, run `wipefs -a /dev/sdaN` on each partition. `wipefs -a <whole-disk>` only erases magic strings libblkid sees at disk-device scope (PMBR, GPT primary, GPT backup) — it does not recurse into nested partition byte ranges. On a redeploy of a previously imaged disk the new GPT lands on top of partition byte ranges still holding XFS/ext4 superblocks at well-known offsets from the prior install. `grub_fs_probe()` walks the whole disk and can detect those residual signatures, hitting the `(ctx.dest_partmap && fs)` branch in grub-2.06 `util/setup.c` which emits "multiple partition labels" + "Embedding is not possible" + "will not proceed with blocklists" and aborts. mkfs writes new signatures over the same byte ranges in the next phase, so this wipe is best-effort: a non-zero exit is logged but mkfs is the authoritative gate.
- **Initramfs build — log resolved binary version** (`internal/server/handlers/initramfs.go`): the build handler now logs the absolute path, file stat (`size`, `mode`, `mtime`), and `clustr --version` output of the binary it is about to embed in the initramfs, before invoking `build-initramfs.sh`. Catches the next stale-binary class (wrong `CLUSTR_BIN_PATH`, picked up an unexpected fallback) at the build step instead of after a full deploy round-trip.
- **Default `CLUSTR_BIN_PATH` matches RPM layout** (`internal/config/config.go`): default flipped from `/usr/local/bin/clustr` (legacy `make install` path) to `/usr/bin/clustr` (RPM-installed location). The systemd unit shipped by the RPM still sets `CLUSTR_BIN_PATH` explicitly, so this only affects non-RPM installs (developer hosts, CI). The previous default pointed at a non-existent file on RPM hosts and silently fell through to the `os.Executable()`-relative `clustr-static` lookup in the build handler.

### Tests

- `internal/server/handlers/serve_initramfs_test.go`: asserts `ServeInitramfs` prefers `initramfs-clustr.img` over `initramfs.img` when both exist, and falls back to the legacy filename when the live build is absent.

## 0.1.12 — 2026-05-07

### Fixes

- **BIOS bootloader install — non-RAID grub2-install flags:** the EL BIOS/GPT path now passes `--skip-fs-probe` and `--modules=part_gpt biosdisk` to `grub2-install` on single-disk (non-RAID) deploys, mirroring what the RAID-on-whole-disk branch already did. Without these flags, grub-probe could observe stale FS signatures from a previous deploy and abort with "multiple partition labels", failing the bootloader phase on a redeploy. Logic extracted into a pure `elGRUBBIOSArgs` helper so the argv contract is unit-testable.
- **Disk wipe — wipefs before sgdisk:** `partitionDisk` now runs `wipefs -a <disk>` *before* `sgdisk --zap-all <disk>`, instead of only as a fallback after sgdisk failure. sgdisk's `--zap-all` clears the GPT/MBR header but leaves filesystem and RAID superblocks intact; that residue is exactly what trips grub-probe on redeploy. wipefs is idempotent (exit 0 on a clean disk), so the proactive call is logged-only on non-zero — sgdisk remains the authoritative gate. The legacy "sgdisk failed → retry wipefs → fatal" path is preserved. RAID-on-whole-disk topology is unaffected (the md device path partitions the array, raw members are skipped via `isMdDevice`). Wipe sequence factored into `diskWipeSequence` for unit-testable order contract.
- **Exit code routing — bootloader vs. finalize:** `cmd/clustr/main.go` now inspects the error chain returned from `deployer.Finalize` and returns `ExitBootloader` (10) when the cause is a `*deploy.BootloaderError`, instead of always returning `ExitFinalize` (9). Cosmetic but stops bootloader regressions from being mislabeled as finalize failures in the deploy-failed callback (`exit_code` and `phase` fields).

### Tests

- `internal/deploy/disk_wipe_test.go`: asserts the wipe-order contract — `wipefs -a` strictly precedes `sgdisk --zap-all`, target disk propagates to both commands as the final positional argument.
- `internal/deploy/distro_el_grub_args_test.go`: asserts the grub2-install argv shape on all three BIOS paths (non-RAID, RAID-on-whole-disk, md-on-partitions). Non-RAID path now requires `--skip-fs-probe` and `--modules=part_gpt biosdisk`.

### Deferred to 0.1.13

- UEFI routing on firmware-blind layouts (vm202).
- Webapp layout-selection UI.

## 0.1.11 — 2026-05-07

### Fixes

- **Image reconciler — block-format default path:** `resolveBlobPath` now picks the F6 default-layout filename based on `base_images.format`. `ImageFormatBlock` resolves to `<imageDir>/<id>/image.img`; `ImageFormatFilesystem` keeps `<imageDir>/<id>/rootfs.tar`. Pre-fix, every block-format image with an empty `blob_path` (initramfs builds, partclone/dd captures that finalized without a `SetBlobPath` call) was falsely flipped to `blob_missing` on each reconcile tick because the resolver only knew about `rootfs.tar`. Blobs were intact on disk; only the DB status was wrong. No migration needed — existing rows self-heal on the next reconcile pass via the F6 write-back path, which now populates `blob_path` with the correct filename.

### Tests

- New `internal/server/reconcile_image_test.go`: format-aware default-path table test, plus end-to-end `resolveBlobPath` cases for block-format empty-DBPath (the regression), filesystem-format empty-DBPath (anti-regression), and block-format truly-missing (resolver must not falsely heal dead rows).

## 0.1.10 — 2026-05-03

### Critical

- **Schema cleanup (migration 102b):** Deletes 197 orphan child rows accumulated under the pre-v0.1.7 era when modernc.org/sqlite silently ignored FK enforcement. Affects `ldap_node_state`, `node_config_history`, `slurm_node_roles`, `node_heartbeats`, `reimage_requests`, `slurm_build_deps`, and `slurm_upgrade_operations` — all rows referencing parents in `node_configs`, `base_images`, or `slurm_builds` that were deleted while the FK was advisory-only. Filename uses the `102b` suffix so the cleanup runs **before** migration 103 (lexical filename order, db.go `sort.Slice`); without this, 103's runtime guard correctly refused to commit on cloner because 197 pre-existing violations surfaced post-rebuild.
- **Unblocks v0.1.9 upgrade:** clustr-serverd was crash-looping on cloner. After 102b lands, both 102b and 103 apply cleanly with `PRAGMA foreign_key_check` returning zero rows after each step.

## 0.1.9 — 2026-05-06

### Critical

- **Schema repair (migration 103):** Rebuilds `api_keys`, `node_groups`, and three PI/membership tables to remove dangling FK references to the long-dropped `_users_old` table — an artifact of migration 058's rename-and-drop pattern from before SQLite 3.26 `legacy_alter_table` was understood. All FKs now point at `users(id)`. Unblocks deploys, which were failing with `no such table: main._users_old` during PXE boot token mint.
- **`api_keys.user_id` is now NOT NULL:** every API key has a user owner. Pre-existing NULL rows are backfilled to the `clustr` bootstrap admin during the rebuild. Node-scope keys default to a 24h TTL on mint; admin-scope keys keep `expires_at = NULL` until rotation UX lands.
- **Token sweeper:** new goroutine in clustr-serverd, 5-minute cadence, deletes `api_keys` rows past `expires_at`. Bounded growth on the keys table.

### Anti-regression

- **Runtime guard:** the migration runner now runs `PRAGMA foreign_key_check` after every migration applies, and aborts the transaction on any violation. A dangling-FK migration can no longer ship undetected.
- **CI linter:** new `scripts/migrations-lint.sh` (wired into the CI workflow) flags any new migration that drops or renames a referenced table without rebuilding its dependents. Allowlist for legitimate exceptions documented inline.

## 0.1.8 — 2026-05-04

### Critical

- **selfmon watchdog:** clustr-serverd now correctly notifies systemd via `sd_notify(WATCHDOG=1)` from inside the metrics collector goroutine. Prior to v0.1.8, `WatchdogSec=90` was declared in the systemd unit but never received a keepalive, so systemd was killing the service every 90 seconds with SIGABRT. Long-running operations (image builds, ISO downloads) could not complete on cloner.
- **Restart rule delta:** `cp.serverd.restart.crit` now uses a 10-minute windowed delta of the systemd NRestarts counter rather than the absolute lifetime value. The rule no longer fires permanently after the first 3 restarts post-boot.

### Fixes

- **Web — alerts:** Silence dropdown now renders correctly. The Tailwind `overflow-hidden` on the table wrapper was clipping the absolute-positioned popover regardless of z-index.
- **Web — image builds:** clicking an in-progress image to re-attach to the build progress panel now correctly replays current state (phase, bytes, serial log) on reconnect rather than displaying empty "Downloading… 0 B."

## 0.1.7 — 2026-05-03

### Critical

- **DB integrity:** SQLite foreign-key enforcement is now actually active at runtime. `modernc.org/sqlite`'s DSN silently ignored the `_foreign_keys=on` parameter, meaning all `ON DELETE CASCADE` constraints have been advisory rather than enforced since clustr-serverd first shipped. FK enforcement is now set explicitly via a `PRAGMA foreign_keys = ON` statement on each connection after migrations complete. Migrations also set `legacy_alter_table=ON` to prevent SQLite 3.26+ from rewriting FK references during table renames.
- **DB cleanup:** Migration 101 deletes orphan `node_rack_position` rows pointing at deleted enclosures/racks (such rows could not exist if FK cascades had been working; this cleans up data drift accumulated under the broken state). Migration 102 fixes a long-latent FK bug where `ldap_node_state.node_id` referenced a non-existent `nodes` table; corrected to `node_configs`.

### Fixes

- **Datacenter chassis:** `DELETE /api/v1/enclosures/:id` now defensively clears any `node_rack_position` rows for the chassis before deleting the enclosure (belt-and-suspenders on top of FK cascade). The handler never touches `node_configs`. Chassis tile rendering got node name truncation and correct eject button sizing.
- **Datacenter drag preview:** dragging a node now shows the hostname (with U-count as secondary text) instead of just the U-count.

### Features

- **Image builds:** in-progress imports in `/images` are now clickable to re-open the BuildProgressPanel that was dismissed. Download and install continue in the background; clicking re-attaches to the live event stream.

## 0.1.6 — 2026-05-03

### Fixes

- **Datacenter chassis (Sprint 31 followups):** `ListEnclosuresByRack` no longer panics on rack refresh after a chassis is added (was caused by nested SQLite queries while the outer cursor was still open). Migration 100 relaxes `node_rack_position` columns (`rack_id`, `slot_u`, `height_u`) to nullable so nodes can actually be placed inside chassis — Sprint 31's NOT NULL constraint was incompatible with its own XOR trigger, making nodes unplaceable in any enclosure since Sprint 31 shipped.
- **SELF-MON:** `collectSystemd` now logs an ERR when its 5s timeout fires (was silently returning nil, invisible in logs). Heartbeat write moved to the start of each collect cycle so slow collectors no longer trip `WatchdogSec=90`.

## 0.1.5 — 2026-05-03

### Features

- **Server:** ISO downloads via the `from-url` import are now cached at `/var/lib/clustr/iso-cache/` and resume on interruption. Re-importing the same URL hits cache instantly. Operators can `rm -rf` the cache dir to reclaim space (no eviction policy yet — v0.1.6 follow-up).
- **Server:** Build serial-log ring bumped 100 → 1000 lines server-side to match UI capacity. Anaconda output no longer truncated.

### Refactor

- **Server:** ISO build phase emission deduplicated. Each phase fires exactly once now.

## 0.1.4 — 2026-05-03

### Features

- **Web UI:** The Add Image dialog now shows live download progress (bytes / total / %) with ETA estimated from a rolling 10-sample rate average, replacing the static "Downloading…" placeholder. When Content-Length is absent, an indeterminate spinner is shown instead.
- **Web UI:** Install phases (generating_config through finalizing) show a scrollable monospace serial console panel streaming the last 500 lines of anaconda qemu output in real time. Auto-scrolls to bottom; stops auto-scroll when the user scrolls up (sticky-scroll).
- **Server:** ISO download phase now emits `BuildHandle.SetProgress` events per-chunk during HTTP read. The `pullAsync` → `pullAndExtract` → `buildFromISOFile` chain wires `OnPhase`, `OnSerialLine`, and `OnStderrLine` callbacks through to the existing SSE event store.

## 0.1.3 — 2026-05-03

### Features

- **Self-monitoring (SELF-MON):** clustr-serverd now monitors its own control-plane host — root disk, data disk, scratch space, memory, PSI, systemd unit state, time drift, cert expiry, and image-store orphans. Persistent status strip in the web UI; new `/control-plane` detail route. 17 default alert rules baked in.
- **Schema:** new `hosts` table with `role` column distinguishing `control_plane` from `cluster_node`. Migration 099. Cluster `nodes` carry a nullable `host_id` FK back to `hosts`.
- **Anti-regression:** `WatchdogSec=90` on the `clustr-serverd` systemd unit; new `clustr-selfmon-watchdog.timer` fires every 5 minutes, checks `/run/clustr/selfmon.heartbeat` staleness, and posts `crit` to syslog plus an optional fallback webhook (`/etc/clustr/fallback-alert-url`) if the metrics goroutine has hung.
- **Packaging:** `chrony` declared as a `Requires:` dep for `chronyc tracking` (NTP drift metric).

## 0.1.2 — 2026-05-03

### Fixes

- **isoinstaller:** qemu VMs now get a `virtio-rng` device and 4 GB default memory — fixes early-boot hang on Rocky 10 (entropy starvation + memory pressure).
- **isoinstaller:** Default build timeout bumped 30 m → 60 m to fit real-world Rocky 10 anaconda + dnf-update wall time.

## 0.1.1 — 2026-05-03

### Fixes

- **Image import (ISO URLs):** `from-url` requests now route through `Factory.PullImage` so ISO inputs hit the qemu+kickstart auto-install pipeline. Previously the web UI's "Add Image" form bypassed the pipeline entirely, silently producing an unusable raw ISO blob. Founder-reported regression; fixed in #237.
- **RPM packaging:** `clustr-serverd` now declares its full isoinstaller runtime deps as `Requires:` — `qemu-kvm`, `qemu-img`, `genisoimage`, `p7zip`, `p7zip-plugins`, `kpartx`, `rsync`, `edk2-ovmf` — so a fresh `dnf install clustr-serverd` pulls in everything the ISO build pipeline needs without manual intervention. A CI assertion step was added that fails the build if any declared dep goes missing (#238).

## 0.1.0 — 2026-05-03

Initial public release. Open-source HPC node cloning and image management suite.
Server (`clustr-serverd`) + privilege helper boundary (`clustr-privhelper`) + static CLI + web UI.
Distributed as signed RPMs for EL8/EL9/EL10 (x86_64/aarch64).

### Highlights

- **Chassis enclosures** — enclosure entity with unified node placement endpoint; datacenter rack diagram supports enclosure-scoped node assignment
- **Image auto-reconcile** — background reconciler self-heals orphaned staging artifacts; blob auto-reconcile on startup with resume on partial downloads
- **GPG-signed RPM repo** — auto-generates repo GPG key on first startup; `rpmsign` pipeline for per-EL signed packages published to `pkg.sqoia.dev`
- **Web build smoke CI** — `ci.yml` runs a full Vite build + route smoke check on every push to main
- **NAT keepalive on exec** — WebSocket ping/pong keepalive on `clustr exec` sessions prevents NAT idle-timeout disconnects
- **Health ping** — `clustr health --ping` reports round-trip latency to the server
- **UDPCast multicast** — `udp-sender`/`udp-receiver` vendored from source (GPL-2.0) for fleet-reimage multicast; attached source tarball on every release for §3a compliance
- **BIOS push** — Intel `syscfg`, Dell `racadm`, Supermicro `sum` providers; BIOS profile CRUD and diff+apply pipeline
- **Distro drivers** — `DistroDriver` interface covering EL8/EL9/EL10, Ubuntu 20/22/24, Debian 12, SLES 15
- **Slurm RPM pipeline** — clustr builds and signs Slurm RPMs into a per-cluster internal yum repo; nodes consume via `dnf` (no external network required). Bundles tab is the cluster's Slurm catalog.
- **clustr-privhelper** — single setuid privilege boundary for all host-root operations; replaces polkit/sudoers entries
- **Rack diagram** — drag-and-drop node placement, unassigned sidebar, height-U selector, multi-rack tile layout
- **Alert engine** — YAML-defined alert rules, async SMTP delivery worker pool, silence support
- **SEC-1/SEC-2 hardening** — Bearer token restricted to `Authorization:` header only; lsblk echo redacted from initramfs build logs
- **UID range split** — `ldap_user` (10000–60000) and `system_account` (200–999) allocated from separate ranges to prevent UID drift with DNF-managed daemons
