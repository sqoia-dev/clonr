# clustr — Forward Sprint Plan

**Last updated:** 2026-05-08

This document captures the working sprint plan for clustr after the 2026-05-07/08 release train (v0.1.10 → v0.1.22) and the 2026-05-08 competitor (clustervisor) reviews. Sprints are sized for parallel execution across Richard, Dinesh, and Gilfoyle.

This is a planning artifact, not a contract. Reorder, drop, or merge sprints as customer reality dictates. Update statuses inline as work lands.

> **Operational hardening pass — 2026-05-08:** every sprint item below was grounded against the current tree (commit `93cc39f`+) and expanded with concrete file paths, function/SQL/UI signatures, API contracts, acceptance criteria, owner split, and test plan. Items already shipped in the current code are flagged inline. Items whose paths/structure need to differ from the original wording are corrected in place. This is now an executable plan — Dinesh or Gilfoyle should be able to pick up any item and start coding without re-asking the architect.

---

## How this plan was built

Two inputs:

1. **Real bugs found shipping today** — over 12 patch releases (v0.1.10 → v0.1.21) the deploy pipeline went through layered failures: orphaned FK rows, blob path resolution, BIOS bootloader stale signatures, the served-initramfs file-name bug, UI staleness, chroot DNS injection, sssd config gaps. Several of those bugs were invisible until the *next* one was uncovered. Sprint 33 specifically targets the observability + portability gaps that would have surfaced those bugs in one pass instead of twelve.

2. **Architectural reviews of clustervisor** (a Python+Perl competitor in the same problem space). Four Richard dispatches:
   - Broad clustervisor architecture review (`/home/ubuntu/sqoia-dev/clustervisor/`)
   - Imaging-pipeline deep-dive (cloner toolchain, on-node deploy phases)
   - Cockpit UI plugin review (`/home/ubuntu/sqoia-dev/clustervisor-cockpit/` — minified-only)
   - Multi-daemon architecture review (`/home/ubuntu/sqoia-dev/clustervisor-more/` — full server runtime)

   The most adoptable patterns are surfaced below; anti-patterns we explicitly reject are at the bottom.

---

## Convergent findings across all four reviews

Two gaps surfaced **independently in every review.** Treat as the highest-priority adoptions:

1. **End-to-end IPMI / BMC / consoles** — backend wrapper (cloner review #6), deploy-time provisioning (imaging deep-dive `BMC-IN-DEPLOY`), web UI with serial + VNC consoles (cockpit review #1). HPC table-stakes — the operator-experience gap most likely to lose deals on demo day.
2. **Out-of-band / agent-less node monitoring** — clustr can only collect stats when clustr-clientd is running. Every review independently identified "monitor a node before it's enrolled or after it's broken" as a gap. The cv-side machinery is `cv_external_statsd` + the `access` plugin (ping / ssh-banner / `ipmi mc-info` reachability probes).

If you only adopt one thing per quarter, alternate these two.

---

## Status at top of plan

**Just shipped today (2026-05-07/08):** v0.1.10–v0.1.21
- v0.1.10 — DB migration 102b orphan FK cleanup
- v0.1.11 — BLOB-RESOLVE format-aware default blob path
- v0.1.12 — BIOS bootloader fix (wipefs + grub flags) + Images tab nav fix
- v0.1.13 — Real BIOS fix (the served-file bug) — `initramfs-clustr.img` path + partition wipefs + version logging
- v0.1.14 — UI-STALE — orphan reimage row closer + defensive UI gate
- v0.1.15 — LDAP infra: URI gen + DIT repair endpoint + verify-boot gating + cert SANs + UEFI BootOrder repair + RPM-update active-jobs guard
- v0.1.16 — Chroot DNS injection for `install_instructions`
- v0.1.17–v0.1.21 — initramfs screen output capture + sssd `services = nss, pam, ssh` + `ldap_tls_reqcert = allow`

**End-to-end LDAP works on vm201 + vm202:** sssd active, `id rromero` returns uid=10001 with `clustr-admins` membership, SSH login succeeds.

**In flight:** v0.1.22 — periodic LDAP health heartbeat + admin re-verify endpoint + UI Re-verify button + Nodes table image-name display. Two PRs awaiting cherry-pick.

---

## Conventions used in the per-item blocks

Every sprint item below uses this structure:

- **Status** — `TODO` (default), `PARTIAL — see notes`, `DONE — strike from plan`, `BLOCKED on …`.
- **Owner** — Richard (architect/backend), Dinesh (web/UI), Gilfoyle (ops/infra/security). Two names = both required.
- **Files to touch** — verified absolute paths under `staging/clustr/`. New files marked `(new)`.
- **Pre-conditions** — sprint items in this plan that must merge first.
- **Order within sprint** — only set when items have a code dependency between them.
- **Signatures** — Go function signatures, SQL migration shape, or React component prop types.
- **API contract** — request/response JSON when adding endpoints.
- **Acceptance** — the concrete passing test or live behavior that proves the item is done.
- **Test plan** — minimum-viable test (Go unit, Go integration with sqlite, vitest component, or E2E on cloner).

Effort estimates are corrected inline where the verified code complexity differs from the original guess. Format: `was 1d → 2d (notes)`.

---

## Sprint 33 — Deploy observability + portability

**Sprint goal:** when a deploy fails, the operator sees the failing line of the on-node install log live in the web UI within 2 seconds, and the same captured-on-virtio-deployed-to-PERC image boots on both with no manual intervention.

**Sprint exit criteria:**
- [ ] Deploy a node on cloner; tail the install log from the web UI in real time (SSE).
- [ ] Capture an image inside a virtio VM, deploy it to a node with a different storage controller (e.g. SATA/AHCI), confirm it boots without dracut hostonly tailoring leaving it stuck.
- [ ] Confirm the v0.1.13 `wipefs` + new `dd ... count=10` PRE-ZERO sequence leaves a freshly imaged disk free of stale GRUB stage 1 bytes (re-imaging a node that previously booted should not chain to the old bootloader for any reason).
- [ ] Schedule a 256-node multi-deploy on the lab grid and confirm `/deploy-complete` peak QPS at the server is below 30/s (jittered).

**Theme:** kill the bug class that ate today.
**Owner:** Richard (deploy + serverd), Dinesh (UI for log streaming).

**Order:** STREAM-LOG-PHASE → STREAM-LOG-UI (UI depends on phase metadata). DRACUT-REGEN, MULTICAST-JITTER, PRE-ZERO are independent.

| ID | Item | Effort | Status |
|---|---|---|---|
| `STREAM-LOG-PHASE` | Phase-tagged install log streaming (the *missing half* of `STREAM-LOG`) | 1d | TODO |
| `STREAM-LOG-UI` | Web UI log viewer on Node detail tab | 1d | TODO |
| `DRACUT-REGEN` | Add `--lvmconf --force-add=mdraid --force-add=lvm` to existing dracut call | 0.5d | PARTIAL |
| `MULTICAST-JITTER` | Random 0-60s pre-`/deploy-complete` jitter | 5min | TODO |
| `PRE-ZERO` | `dd if=/dev/zero ... count=10` before `wipefs` | 5min | TODO |

**Source:** clustervisor `ClonerInstall.pm` runs `status_print_log("INFO", msg)` that double-writes to local console AND to `client->log()`; their cloner regenerates initrd in chroot for every kernel via `_create_system_files_el` and uses `dd ... count=10` before partitioning.

**Suggested cut:** v0.1.23 (or split STREAM-LOG into v0.1.23 / v0.1.24).

### `STREAM-LOG-PHASE` — phase-tagged install log streaming

**Status:** TODO (the streaming pipe already exists; the missing piece is **per-phase tagging on every entry** so the UI can group/colour by phase)

**Correction to original plan:** the plan claimed we need a new `internal/client/loghook.go` and a new `POST /api/v1/nodes/{id}/install-log` endpoint. **Both already exist in different form:**
- `pkg/client/logger.go` (`RemoteLogWriter`) buffers zerolog JSON lines and POSTs them to `POST /api/v1/logs` (`internal/server/handlers/logs.go:IngestLogs`).
- `pkg/client/progress.go` (`ProgressReporter.StartPhase`) already tracks `currentPhase` for progress (preflight/partitioning/formatting/downloading/extracting/finalizing).
- `cmd/clustr/main.go:919` already wires `RemoteLogWriter` into the in-deploy zerolog logger.

The ACTUAL gap is that `RemoteLogWriter` does not include the deploy phase as a structured field on each `LogEntry`. The UI cannot filter the existing log stream by phase because the field doesn't exist on the wire.

**Owner:** Richard (backend stream wiring), Dinesh (UI consumer).

**Files to touch:**
- `pkg/api/types.go` — `LogEntry` (line 1267) — add `Phase string \`json:"phase,omitempty"\``.
- `pkg/client/logger.go:269` — `parseZerologLine` — read phase from a struct field on `RemoteLogWriter` and stamp it on every entry.
- `pkg/client/logger.go:32-46` — `RemoteLogWriter` struct — add `phase string` field guarded by `mu`.
- `pkg/client/logger.go` — add `SetPhase(phase string)` method, mirroring `SetComponent`.
- `cmd/clustr/main.go:919, 1188+` — call `remoteWriter.SetPhase(<phase>)` at every existing `printPhase` site so the next batch of log lines is tagged.
- `internal/db/migrations/105_log_entries_phase.sql` (new) — `ALTER TABLE node_logs ADD COLUMN phase TEXT;` — or, if log entries are not durable, no migration needed and the field lives only on the in-memory `logbroker`.

**Decision required (Richard):** durable phase column or in-memory only? Recommend in-memory only for v0.1.23 — the `logbroker` already broadcasts to SSE subscribers; durable logs can stay phase-blind.

**Signatures:**
```go
// pkg/api/types.go
type LogEntry struct {
    // ... existing fields ...
    Phase string `json:"phase,omitempty"` // e.g. "partitioning", "downloading", "finalize"
}

// pkg/client/logger.go
func (w *RemoteLogWriter) SetPhase(phase string) {
    w.mu.Lock()
    defer w.mu.Unlock()
    w.phase = phase
}
```

**Acceptance:**
- A new unit test in `pkg/client/logger_test.go` that drives `SetPhase("partitioning")` then writes a zerolog JSON line and asserts the resulting `LogEntry.Phase == "partitioning"`.
- Live: tail `/api/v1/logs/stream?component=deploy&node_mac=…` while a deploy runs on cloner; every event after the first `printPhase` carries `phase="<name>"`.

**Test plan:**
```go
// pkg/client/logger_test.go
func TestRemoteLogWriter_StampsPhase(t *testing.T) {
    var captured []api.LogEntry
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewDecoder(r.Body).Decode(&captured)
        w.WriteHeader(204)
    }))
    defer srv.Close()
    w := NewRemoteLogWriter(New(srv.URL, ""), "00:11", "h", WithComponent("deploy"))
    w.SetPhase("partitioning")
    w.Write([]byte(`{"level":"info","message":"sgdisk --zap-all"}` + "\n"))
    w.flushNow() // expose helper for tests
    if got := captured[0].Phase; got != "partitioning" {
        t.Fatalf("Phase = %q; want %q", got, "partitioning")
    }
}
```

---

### `STREAM-LOG-UI` — Node-detail Install Log tab

**Status:** TODO

**Correction:** the plan said "Web UI Nodes detail tab streams the log via SSE". The endpoint to consume is the existing `GET /api/v1/logs/stream` (see `internal/server/handlers/logs.go:210`), filtered to `component=deploy` and `node_mac=<node primary MAC>`. No new server endpoint required.

**Owner:** Dinesh.

**Pre-conditions:** `STREAM-LOG-PHASE` (so colour-by-phase works) — but the tab can ship without phase first and add colour later.

**Files to touch:**
- `web/src/routes/node-detail-tabs.tsx` — add a third tab `<DeployLogTab nodeId={…} primaryMac={…} />` next to the existing Sensors and EventLog tabs. Existing pattern is in `EventLogTab` at line 319.
- `web/src/components/ui/log-viewer.tsx` (new) — generic SSE-backed log viewer, also used by Sprint 41 `JOURNAL-ENDPOINT` and Sprint 46 `LOG-VIEWER-COMPONENT`. Build the minimum viable here; refactor to a shared component during Sprint 46.
- `web/src/lib/sse-backoff.ts` — already exists (`sseReconnectDelay`); reuse.

**Component shape:**
```tsx
type DeployLogTabProps = { nodeId: string; primaryMac: string }
function DeployLogTab({ nodeId, primaryMac }: DeployLogTabProps) {
  const [entries, setEntries] = React.useState<LogEntry[]>([])
  const [connected, setConnected] = React.useState(false)
  const [phaseFilter, setPhaseFilter] = React.useState<string | null>(null)
  // EventSource on `/api/v1/logs/stream?component=deploy&node_mac=<mac>`
  // Auto-reconnect via sseReconnectDelay on close.
  // Render row-virtualized list (max 5000 rows in memory; older rows fall off).
}
```

**Acceptance:**
- Open Nodes → click a node → Install Log tab. Trigger a reimage. Lines appear within 2s of being emitted by the embedded clustr binary.
- Tab indicator dot turns red on a WARN/ERROR entry.

**Test plan:**
- vitest snapshot of `<DeployLogTab>` rendered with a mocked `EventSource` that emits 3 known entries.
- Smoke test on cloner: trigger reimage of vm201, observe the live log.

---

### `DRACUT-REGEN` — strengthen the existing in-chroot dracut call

**Status:** PARTIAL — the chroot dracut call already exists.

**Correction to original plan:** the plan implied this was a new file `internal/deploy/regen_initramfs.go`. **It already lives at `internal/deploy/finalize.go:1480`:**
```go
dracutCmd := exec.CommandContext(ctx, "chroot", mountRoot,
    "dracut", "--force", "--no-hostonly", "--regenerate-all")
```

The actual gap is the missing flags `--lvmconf --force-add=mdraid --force-add=lvm`. Without `--force-add=mdraid`, a node captured on virtio (no md modules required) and deployed to a host with hardware RAID won't pull mdraid into the regenerated initramfs. Without `--lvmconf`, LVM root setups break similarly.

**Owner:** Richard.

**Files to touch:**
- `internal/deploy/finalize.go:1480` — extend the dracut argv. Also add `-N` (alias for `--no-hostonly`, redundant but matches cv) and `-fv` (force + verbose).
- `internal/deploy/finalize_test.go` — add a TestDracutArgvIncludesPortabilityFlags test by capturing the invoked argv via the existing test pattern.

**Signature change (argv only):**
```go
dracutArgv := []string{
    "--force", "--no-hostonly", "-v",
    "--lvmconf",
    "--force-add", "mdraid",
    "--force-add", "lvm",
    "--regenerate-all",
}
dracutCmd := exec.CommandContext(ctx, "chroot", mountRoot, append([]string{"dracut"}, dracutArgv...)...)
```

**Acceptance:**
- A new finalize test asserting the dracut argv contains all four flags.
- Cross-controller deploy test on cloner: capture from vm201 (virtio), deploy to a host with a different controller, boot succeeds.

**Test plan:**
- Add `TestFinalize_DracutArgvIncludesPortabilityFlags` to `internal/deploy/finalize_test.go` mirroring the `prepareDracutForRAID` test pattern. Use a fake `runAndLog` (or expose argv via a recorder) — there is no current spy; add one if needed (this is the first test that needs it).

---

### `MULTICAST-JITTER` — random 0-60s sleep before `/deploy-complete`

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `cmd/clustr/main.go:1623` — immediately before `reporter.StartPhase("deploy-complete", 0)`, when the deploy used multicast (the `usedMulticast` boolean derived from the `attemptMulticastReceive` call site near line 1052), `time.Sleep(time.Duration(rand.Intn(60)) * time.Second)`. Use a per-node-deterministic seed so retries don't all jitter to the same value (`rand.New(rand.NewSource(hash(primaryMAC)))` keyed off `primaryMAC`).

**Signature (helper):**
```go
// jitterSleepIfMulticast sleeps a deterministic 0-60s interval seeded by the
// node MAC when usedMulticast is true. Suppresses the thundering herd when
// 256 nodes finish multicast within the same second and all POST /deploy-complete.
func jitterSleepIfMulticast(usedMulticast bool, primaryMAC string) {
    if !usedMulticast { return }
    h := fnv.New32a(); _, _ = h.Write([]byte(primaryMAC))
    secs := h.Sum32() % 60
    time.Sleep(time.Duration(secs) * time.Second)
}
```

**Acceptance:**
- Lab test: 32-node multicast → server log shows `/deploy-complete` requests spread across a 60s window, peak rate < 5/s.
- Unit test on `jitterSleepIfMulticast` with stubbed sleep that just records the duration.

**Test plan:**
- Refactor `jitterSleepIfMulticast` to take a `sleep func(time.Duration)` param so the test can inject. Unit test: same MAC → same duration; different MACs → different durations within [0, 60s).

---

### `PRE-ZERO` — `dd zero count=10` before `wipefs`

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/deploy/rsync.go:1198` — extend `diskWipeSequence` to prepend a `dd if=/dev/zero of=<target> bs=1M count=10` step. Mark it as best-effort (log on failure, don't propagate — the existing wipefs+sgdisk are the authoritative gates).

**Signature change:**
```go
func diskWipeSequence(target string) []diskWipeCmd {
    return []diskWipeCmd{
        {Name: "dd", Args: []string{"if=/dev/zero", "of=" + target, "bs=1M", "count=10", "conv=fsync"}},
        {Name: "wipefs", Args: []string{"-a", target}},
        {Name: "sgdisk", Args: []string{"--zap-all", target}},
    }
}
```

**Acceptance:**
- `internal/deploy/disk_wipe_test.go` (already exists) — add `TestDiskWipeSequence_DDFirst` asserting the first command is `dd ... count=10`.

**Test plan:**
```go
func TestDiskWipeSequence_DDFirst(t *testing.T) {
    seq := diskWipeSequence("/dev/sda")
    if len(seq) < 3 || seq[0].Name != "dd" {
        t.Fatalf("first command = %v; want dd", seq[0])
    }
    if got := seq[0].Args[0]; got != "if=/dev/zero" { t.Errorf("dd[0]=%q", got) }
}
```

---

## Sprint 34 — BootOrder + IPMI + consoles

**Sprint goal:** an operator without ssh into a node can reach its serial console + iKVM, view the SEL, drain a fault, and pin its boot to PXE-or-disk — entirely from the web UI. Hostlist syntax (`compute[01-12]`) works in CLI and UI.

**Sprint exit criteria:**
- [ ] Click a row in Nodes → Console tab opens xterm.js to the BMC SOL stream within 5s.
- [ ] Click "Open iKVM" on a Dell iDRAC node and see the framebuffer in noVNC.
- [ ] Click "Boot setting → PXE (next boot)" on a node, power-cycle, observe the node PXE-boots once and reverts to disk.
- [ ] `clustr ipmi sensors compute01` returns the same data the web shows.
- [ ] `clustr group list compute[01-12]` resolves 12 nodes; same syntax in the bulk-add UI.
- [ ] Recommissioning a node automatically re-applies its BMC IP/user from `disk_layout` schema (operator never opens vendor BMC UI).

**Theme:** HPC table stakes — converged from cloner #6, imaging deep-dive `BMC-IN-DEPLOY`, and cockpit review #1.
**Owner:** Richard (deploy + ipmi), Gilfoyle (BMC ops), Dinesh (console UI).

**Order:** HOSTLIST is independent and unblocks BULK-* in Sprint 44 — land it first. BOOT-POLICY before SERIAL-CONSOLE-UI (the latter can hard-pin boot via the former). VNC-CONSOLE-UI is independent. BMC-IN-DEPLOY independent of UI.

| ID | Item | Effort | Status |
|---|---|---|---|
| `HOSTLIST` | pyhostlist-style range syntax across CLI + API + UI | 1d | TODO |
| `BOOT-POLICY` | Explicit `BootOrderPolicy` field replacing reactive repair | was 0.5d → 1d | TODO |
| `IPMI-MIN` | `clustr ipmi {power,sel,sensors}` via freeipmi | was 2d → 1d (most exists) | PARTIAL |
| `BMC-IN-DEPLOY` | Idempotent BMC IP/user/channel reset in initramfs | 2d | TODO |
| `SERIAL-CONSOLE-UI` | xterm.js + WebSocket bridge to `ipmitool sol` | 3d | TODO |
| `VNC-CONSOLE-UI` | noVNC proxy to BMC iKVM | 4d | TODO |
| `BOOT-SETTINGS-MODAL` | Per-node boot settings persisted + iPXE menu integration | 2d | TODO |

**Source:** clustervisor `cv_ipmi.py` (1240 lines) + `EFIBootmgr.pm:214 set_order`, `pyhostlist` everywhere in cluster config, cockpit `SerialConsole` / `VncConsole` PatternFly components.

### `HOSTLIST` — pyhostlist range syntax

**Status:** TODO.

**Owner:** Richard (parser), Dinesh (UI consumption — pairs with Sprint 44 `HOSTLIST-BULK-ADD`).

**Files to touch:**
- `internal/selector/` (already exists) — add `hostlist.go` (new) implementing `Expand(pattern string) ([]string, error)` for `node[01-12,20-25,30]` syntax. Include `Compress(names []string) string` for the inverse so the UI can show "12 nodes (compute[01-12])" hints. Pure Go, no vendor — write it; the original plan suggested `github.com/segmentio/go-hostlist` but that doesn't exist and `compute[01-12,20-25,30,a-c]` is ~150 LOC of stdlib.
- `cmd/clustr/group.go` — group commands accept hostlists.
- `cmd/clustr/users.go` — same.
- `internal/server/handlers/nodes.go` — `ListNodes` accepts `?hostnames=compute[01-12]` and expands server-side before lookup.
- `web/src/lib/hostlist.ts` (new) — TS port of the same parser/formatter for client-side preview in `BulkAddNodes` and node selectors. ~80 LOC.

**Signatures:**
```go
// internal/selector/hostlist.go
func Expand(pattern string) ([]string, error)
func Compress(names []string) string
```

```ts
// web/src/lib/hostlist.ts
export function expandHostlist(pattern: string): string[]
export function compressHostnames(names: string[]): string
```

**API contract:**
```
GET /api/v1/nodes?hostnames=compute[01-12,20]
→ 200 { "items": [...12 nodes...], "matched": 12, "unmatched": ["compute20"] }
```

**Acceptance:**
- `internal/selector/hostlist_test.go` covers: contiguous ranges, comma-list, mixed (`compute[01-04,10]`), padding preservation (`01` not `1`), out-of-order (`[03,01,02]`), single-item bracket (`compute[01]`), error cases (`[a-]`, `[]`, unmatched bracket).
- CLI: `clustr group list compute[01-12]` shows 12 rows.
- UI bulk add: paste `gpu[001-128]\n` → preview table shows 128 rows with auto-numbered hostnames.

**Test plan:** Go table-driven test in `internal/selector/hostlist_test.go`. Vitest in `web/src/lib/hostlist.test.ts` mirroring the same table.

---

### `BOOT-POLICY` — explicit per-node BootOrderPolicy field

**Status:** TODO. Effort revised `0.5d → 1d` because schema + CLI + UI hookup all need touching, not just the deploy path.

**Owner:** Richard.

**Pre-conditions:** none (replaces existing reactive `RepairBootOrderForReimage`).

**Files to touch:**
- `internal/db/migrations/105_node_boot_order_policy.sql` (new) — `ALTER TABLE nodes ADD COLUMN boot_order_policy TEXT NOT NULL DEFAULT 'auto' CHECK (boot_order_policy IN ('auto','network','os'));`. `auto` = current behaviour (PXE first via `RepairBootOrderForReimage`).
- `pkg/api/types.go:NodeConfig` — add `BootOrderPolicy string \`json:"boot_order_policy"\``.
- `internal/db/db.go` — read/write boot_order_policy on `GetNode` / `UpdateNode`.
- `internal/deploy/efiboot.go:223` — `RepairBootOrderForReimage(ctx, policy string)` now takes a policy. Behaviour:
  - `auto` or `network` → existing PXE-first behaviour.
  - `os` → reorder OS entry first, leave PXE second.
- `internal/deploy/finalize.go:1240` — pass `cfg.BootOrderPolicy` into the call.
- `internal/server/handlers/nodes.go` — accept `boot_order_policy` on PATCH.
- `web/src/routes/node-detail-tabs.tsx` — surface the policy in the node detail panel; allow editing (admin-only).

**Migration SQL:**
```sql
-- 105_node_boot_order_policy.sql
ALTER TABLE nodes ADD COLUMN boot_order_policy TEXT NOT NULL DEFAULT 'auto'
    CHECK (boot_order_policy IN ('auto','network','os'));
```

**Signature change:**
```go
// internal/deploy/efiboot.go
func RepairBootOrderForReimage(ctx context.Context, policy string) error
```

**API contract:**
```
PATCH /api/v1/nodes/{id}
{ "boot_order_policy": "os" }
→ 200 NodeConfig
```

**Acceptance:**
- Set policy to `os` on a UEFI node, run a deploy, observe `efibootmgr -v` after reboot lists OS entry first.
- Default-`auto` nodes behave identically to today.

**Test plan:** extend `internal/deploy/bootloader_test.go:TestRepairBootOrderForReimage_BIOSNoOp` with `TestRepairBootOrderForReimage_OSPolicy_KeepsOSFirst` using the existing efibootmgr fake.

---

### `IPMI-MIN` — `clustr ipmi {power,sel,sensors}` via freeipmi

**Status:** PARTIAL — most of this exists.

**Correction to original plan:** the plan claims this is a 2d new build. Reality:
- `clustr ipmi power {on,off,cycle,reset,status}` — **already exists** (`cmd/clustr/main.go:1869` `newIPMIPowerCmd`).
- `clustr ipmi sel {list,clear,head,tail,filter}` — **already exists** (`cmd/clustr/ipmi_sel.go`).
- `clustr ipmi sensors` — **NOT YET A CLI SUBCOMMAND** but the backend endpoint and `internal/ipmi/ipmi.go:GetSensorData` already exist.

The actual remaining work: add `clustr ipmi sensors` CLI subcommand, ~80 LOC mirroring `ipmi_sel.go`. Effort revised `2d → 1d`.

**Owner:** Richard.

**Files to touch:**
- `cmd/clustr/ipmi_sensors.go` (new) — mirrors `cmd/clustr/ipmi_sel.go` shape; calls `GET /api/v1/nodes/{id}/sensors`.
- `cmd/clustr/main.go:130-143` — wire into `ipmiCmd.AddCommand(newIPMISensorsCmd(...))`.

**Signature:**
```go
// cmd/clustr/ipmi_sensors.go
func newIPMISensorsCmd(httpClient *http.Client, server *string) *cobra.Command
```

**Acceptance:**
- `clustr ipmi sensors -n compute01` returns a table of sensor name / value / unit / status mirroring the web Sensors tab.
- Test with the existing `fake-ipmi-sensors` testdata in `internal/ipmi/`.

**Test plan:** mirror `cmd/clustr/console_cmd_test.go` style — argv parse + URL build.

---

### `BMC-IN-DEPLOY` — idempotent BMC reset from `disk_layout` schema

**Status:** TODO.

**Correction:** the plan said `internal/deploy/bmc.go` (new). Confirm this is a new file — checked: no `internal/deploy/bmc*.go` exists. Existing `applyBMCConfig` in `internal/deploy/finalize.go:380` writes the BMC config but only into the deployed root filesystem (it pokes `/etc/clustr/bmc.json`), not via `ipmitool` against the live BMC. The new file should call `ipmitool lan set` to actually write the BMC NIC config during the in-initramfs phase.

**Owner:** Richard (impl), Gilfoyle (test on real hardware).

**Files to touch:**
- `internal/deploy/bmc.go` (new) — `func ApplyBMCConfigToHardware(ctx context.Context, cfg *api.BMCNodeConfig) error` — runs `ipmitool lan set 1 ipsrc static`, `ipmitool lan set 1 ipaddr X`, `ipmitool user set name 2 admin`, etc., from `pkg/api/types.go:BMCNodeConfig`. Idempotent — re-running a deploy with the same config must be a no-op (compare existing values via `ipmitool lan print 1` first).
- `internal/deploy/finalize.go:380` — call `ApplyBMCConfigToHardware` before `applyBMCConfig` (which writes the config-file half).
- `internal/deploy/bmc_test.go` (new) — table tests with a fake ipmitool runner.

**Signatures:**
```go
// internal/deploy/bmc.go
type bmcRunner func(ctx context.Context, args ...string) (string, error)

func ApplyBMCConfigToHardware(ctx context.Context, cfg *api.BMCNodeConfig) error {
    return applyBMCConfigWithRunner(ctx, cfg, defaultIpmitoolRunner)
}
func applyBMCConfigWithRunner(ctx context.Context, cfg *api.BMCNodeConfig, run bmcRunner) error
```

**Acceptance:**
- Deploy a node that previously had a wrong BMC IP. `ipmitool lan print 1` after deploy shows the configured IP.
- Re-deploy with the same config — no `ipmitool lan set` calls actually fire (idempotency check passes).

**Test plan:** unit test the runner-injected variant against fake outputs of `ipmitool lan print 1`. Lab test on cloner with a real BMC.

---

### `SERIAL-CONSOLE-UI` — xterm.js + WebSocket bridge to `ipmitool sol`

**Status:** TODO.

**Correction:** the plan said "today clustr only has CLI `clustr console --ipmi-sol`". Confirmed: `cmd/clustr/console_cmd.go` exists; the WS endpoint that *would* serve the UI is not yet present. There is `internal/server/handlers/console.go` for ssh-based shells, but no SOL bridge.

**Owner:** Richard (backend bridge), Dinesh (xterm.js component).

**Pre-conditions:** none (BMC-IN-DEPLOY is independent — operators may want SOL on nodes that haven't been deploy-touched yet).

**Files to touch:**
- `internal/server/handlers/console.go` (extend) — new handler `ServeSOLWebSocket` (websocket upgrade, spawn `ipmitool sol activate`, pipe both directions). Reuse the existing `shell_ws.go` keepalive pattern.
- `internal/ipmi/ipmi.go:590` — `SOLActivate` already exists; wrap it to expose stdin/stdout io for the WS bridge.
- `web/src/components/SerialConsole.tsx` (new) — xterm.js + the existing `useWebSocket` hook (check `web/src/hooks/`).
- `web/src/routes/node-detail-tabs.tsx` — add a Console tab.
- `web/package.json` — add `xterm` and `xterm-addon-fit` dependencies.

**Signature:**
```go
// internal/server/handlers/console.go
func (h *ConsoleHandler) ServeSOLWebSocket(w http.ResponseWriter, r *http.Request)
```

**API contract:**
```
GET /api/v1/nodes/{id}/console/sol  (Upgrade: websocket)
→ binary frames bidirectional (terminal IO)
```

**Acceptance:**
- Open a node's Console tab, see BIOS POST output during boot, type at the BIOS menu and observe response.
- Two browser tabs to the same node = both observe traffic (server multiplexes).

**Test plan:**
- Backend: `internal/server/handlers/console_test.go` — extend with a SOL-bridge test that uses an in-memory ipmitool stub.
- vitest snapshot of `<SerialConsole>` with mocked WS.

---

### `VNC-CONSOLE-UI` — noVNC proxy to BMC iKVM

**Status:** TODO. Effort retained at 4d but flag the per-vendor risk.

**Risk surface:** Dell iDRAC, Supermicro IPMIView, AMI MegaRAC each use different iKVM transports. A single noVNC proxy works ONLY for vendors that expose plain VNC (rare in modern BMCs). Most use Java/HTML5-jKVM consoles that are NOT VNC-protocol — the bridge needs vendor adapters.

**Recommendation:** ship Supermicro first (cleanest VNC), iDRAC second (uses HTML5 console — bridge via launching the vendor URL with credentials, NOT a true noVNC proxy). Document Dell/AMI as "open vendor portal" until Sprint 49.

**Owner:** Gilfoyle (BMC vendor heuristics) + Dinesh (UI).

**Files to touch:**
- `internal/ipmi/vnc.go` (new) — vendor-detection + proxy dispatch.
- `internal/server/handlers/console.go` — add `ServeVNCWebSocket` for Supermicro path; redirect to vendor-URL for Dell/AMI.
- `web/src/components/VncConsole.tsx` (new) — `react-novnc` wrapper.
- `web/package.json` — add `@novnc/novnc` dep.

**Acceptance:**
- Supermicro node: click Open iKVM → see framebuffer in browser within 8s.
- iDRAC node: click Open iKVM → opens iDRAC HTML5 console in new tab with creds.
- Known-unsupported BMC: shows a "VNC console not supported on this BMC vendor (yet) — open vendor portal: <link>" message instead of a broken canvas.

**Test plan:**
- Unit test the vendor detection (parse `ipmitool mc info` output → vendor enum) with the existing `internal/ipmi/ipmi.go:DetectVendor` infrastructure.
- E2E on cloner: only Supermicro variant (no Dell hardware in lab today).

---

### `BOOT-SETTINGS-MODAL` — per-node persistent boot settings

**Status:** TODO.

**Correction:** the plan said "Clustr today only has one-shot `/power/pxe` `/power/disk`". Confirmed (`internal/server/handlers/ipmi.go:267,278`). The new persistence sits on `nodes` (or a new `node_boot_settings` table) and feeds into `internal/server/handlers/boot.go:106:ServeIPXEScript` so the iPXE menu picks the persisted entry when no one-shot override is in flight.

**Owner:** Richard (backend + iPXE wiring), Dinesh (modal).

**Files to touch:**
- `internal/db/migrations/106_node_boot_settings.sql` (new) — adds columns or a side table:
  ```sql
  ALTER TABLE nodes ADD COLUMN persistent_ipxe_entry TEXT;       -- e.g. "memtest", "rescue"
  ALTER TABLE nodes ADD COLUMN persistent_kernel_cmdline TEXT;   -- appended verbatim
  ```
- `internal/server/handlers/boot.go:106-360` — `ServeIPXEScript` consumes the persisted entry when present and the node is not in the middle of an active reimage.
- `internal/server/handlers/boot_entries.go` — extend the boot-entry catalog so the UI can pick from a known set.
- `web/src/components/BootSettingsModal.tsx` (new) — modal with two fields (entry picker + cmdline textarea) and a typed-confirmation guard (type the hostname).
- `web/src/routes/node-detail-tabs.tsx` — wire the "Change boot settings" action.

**API contract:**
```
PATCH /api/v1/nodes/{id}/boot-settings
{ "persistent_ipxe_entry": "rescue", "persistent_kernel_cmdline": "console=ttyS0,115200n8" }
→ 200 NodeConfig
```

**Acceptance:**
- Set persistent_ipxe_entry=rescue on a node → next PXE boot lands on rescue.
- Cancel: PATCH with `null` reverts.

**Test plan:** integration test in `internal/server/handlers/boot_test.go` (already exists) — add `TestServeIPXEScript_HonorsPersistentEntry`.

---

## Sprint 35 — Disk / storage breadth (closes #255)

**Sprint goal:** an operator can pick, override, edit, and duplicate disk layouts in the UI; UEFI-detected nodes get the correct ESP-bearing layout; IMSM hardware RAID and LVM root work in the deploy path.

**Sprint exit criteria:**
- [ ] Deploy a UEFI-detected node — it gets a layout that includes `esp` flag automatically (no operator intervention).
- [ ] Layouts tab in UI: list, create, edit, duplicate work end-to-end.
- [ ] Per-node Disk Layout picker on the node detail panel: change override → next deploy uses the picked layout.
- [ ] IMSM lab node: deploy onto Intel RST container array, boots.
- [ ] LVM lab node: deploy with LVM root, boots.

**Theme:** firmware-aware layout + hardware RAID variants + per-node disk UX.
**Owner:** Richard (backend), Dinesh (UI).

**Order:** UEFI-LAYOUT (backend correctness, fixes data quality) → DISK-LAYOUT-PICKER (read path UI) → DISK-LAYOUT-EDITOR (write path UI) → DISK-LAYOUT-DUPLICATE (small UX improvement on top). UEFI-WEBAPP folds into DISK-LAYOUT-PICKER. IMSM and LVM are independent backend tracks.

| ID | Item | Effort | Status |
|---|---|---|---|
| `UEFI-LAYOUT` (#255) | Firmware-aware default layout selection | 1d | TODO |
| `UEFI-WEBAPP` (#255) | (folded into DISK-LAYOUT-PICKER) | — | MERGED |
| `DISK-LAYOUT-PICKER` | Per-node picker + override on node detail | 1w | TODO |
| `DISK-LAYOUT-EDITOR` | Visual edit/create with partition tree | 2w | TODO |
| `DISK-LAYOUT-DUPLICATE` | "Duplicate layout" action | 0.5d | TODO |
| `IMSM` | Intel IMSM hardware RAID containers | 3d | TODO |
| `LVM-DEPLOY` | LVM root in disk_layout | 4d | TODO |

**Source:** clustervisor `disk_raid_imsm` (`ClonerInstall.pm:589`), `disk_lvm` (line 808), cockpit "Disk Layouts" tab + capture/edit/duplicate actions.

### `UEFI-LAYOUT` — firmware-aware default layout selection (#255)

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `pkg/api/types.go` — `DiskLayout` already has `EFI` flag info on partitions; verify by inspection (line ~near the existing `EffectiveLayout` method on `NodeConfig`).
- `internal/db/disk_layouts.go` — extend `DefaultLayoutForNode(node)` to branch on `node.DetectedFirmware` (existing column from migration 026): UEFI → ensure layout includes a 512MB ESP partition with `esp` flag and `mountpoint=/boot/efi`. BIOS → existing layout.
- `internal/server/handlers/layout.go:GetEffectiveLayout` — defensive: if a node has `detected_firmware=uefi` but the resolved layout has no ESP, return the response with a `warning` field rather than silently corrupt the deploy.
- `internal/db/migrations/107_seed_uefi_default_layout.sql` (new) — seed a `clustr-default-uefi` layout row alongside the existing default.

**Migration SQL (sketch):**
```sql
-- 107_seed_uefi_default_layout.sql
INSERT OR IGNORE INTO disk_layouts (id, name, body_json) VALUES (
  'clustr-default-uefi',
  'clustr default (UEFI)',
  json('{"partitions":[
    {"size":"512MiB","fs":"vfat","flags":["esp"],"mountpoint":"/boot/efi"},
    {"size":"1GiB","fs":"ext4","mountpoint":"/boot"},
    {"size":"-","fs":"xfs","mountpoint":"/"}
  ]}')
);
```

**Acceptance:**
- vm202 (UEFI), no override, no group layout → `GET /api/v1/nodes/{id}/effective-layout` returns the UEFI layout.
- BIOS-detected node → returns BIOS layout.

**Test plan:** `internal/db/disk_layouts_test.go` — `TestDefaultLayoutForNode_UEFI` and `_BIOS`. Live: redeploy vm202.

---

### `DISK-LAYOUT-PICKER` — per-node UI picker + override

**Status:** TODO.

**Correction:** the plan said the server endpoints `/effective-layout` and `/layout-override` already exist. Confirmed — `internal/server/handlers/layout.go:183, 310`. The work here is purely UI.

**Owner:** Dinesh.

**Files to touch:**
- `web/src/routes/node-detail-tabs.tsx` — add Disk Layout tab.
- `web/src/components/DiskLayoutPicker.tsx` (new) — dropdown of available `disk_layouts.id` values + a "Custom override" toggle that opens DISK-LAYOUT-EDITOR.
- `web/src/lib/api.ts` — add `getEffectiveLayout(nodeId)` and `setLayoutOverride(nodeId, layoutId | null)` helpers.

**Component shape:**
```tsx
type DiskLayoutPickerProps = { nodeId: string }
// Reads /effective-layout for "currently selected"
// Lists /api/v1/disk-layouts for picker options
// On select → PUT /api/v1/nodes/{id}/layout-override
```

**Acceptance:**
- Node detail → Disk Layout tab shows current effective layout + source (image / group / override).
- Pick a different layout → next reimage uses it.

**Test plan:** vitest with mocked `apiFetch`.

---

### `DISK-LAYOUT-EDITOR` — visual edit/create

**Status:** TODO.

**Owner:** Dinesh.

**Pre-conditions:** `DISK-LAYOUT-PICKER` (the editor opens from the picker).

**Files to touch:**
- `web/src/routes/disk-layouts.tsx` (new) — top-level Disk Layouts route in `web/src/router.tsx` (mirror the `images.tsx` pattern).
- `web/src/components/DiskLayoutEditor.tsx` (new) — partition rows table, drag-resize column, validation (sum-of-sizes ≤ smallest member disk if RAID, ESP required when target is UEFI, etc.).
- Extend `web/src/lib/api.ts` with CRUD helpers.

**Component shape:**
```tsx
type Partition = { id: string; size: string; fs: 'ext4'|'xfs'|'vfat'|'swap'; flags: string[]; mountpoint: string }
type DiskLayoutEditorProps = {
  initial?: DiskLayout
  onSave: (layout: DiskLayout) => Promise<void>
  onCancel: () => void
}
```

**Acceptance:**
- Create a new layout via UI → save → appears in picker.
- Edit existing layout → save → existing nodes using it get the change on next reimage (no in-flight changes).

**Test plan:** vitest snapshot + integration test that POSTs to a stub server.

---

### `DISK-LAYOUT-DUPLICATE` — "Duplicate" action

**Status:** TODO.

**Owner:** Dinesh.

**Files to touch:**
- `web/src/routes/disk-layouts.tsx` — Duplicate button on each row → opens editor pre-filled with `<existing>.copy`.
- Backend: no new endpoint — POST /api/v1/disk-layouts already exists.

**Acceptance:** Duplicate a layout → see `<original> (copy)` in the list.

**Test plan:** vitest interaction test.

---

### `IMSM` — Intel IMSM hardware RAID containers

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/deploy/raid.go` — extend the layout schema validation + assembly path. Two-pass mdadm:
  ```go
  // Pass 1: container creation
  exec.CommandContext(ctx, "mdadm", "--create", "/dev/md/imsm0",
      "--metadata=imsm", "--raid-devices=N", member1, member2, ...)
  // Pass 2: array creation inside the container
  exec.CommandContext(ctx, "mdadm", "--create", "/dev/md/vol0",
      "--metadata=imsm", "--raid-devices=N", "--level=1", "/dev/md/imsm0")
  ```
- `internal/deploy/raid_imsm_test.go` (already exists) — extend to cover the two-pass call sequence.
- `pkg/api/types.go:DiskLayout` — add `IMSMContainer string` flag on `RAIDArraySpec`.

**Acceptance:**
- Lab IMSM node deploys + boots.
- `mdadm --detail /dev/md/vol0` post-boot shows imsm metadata.

**Test plan:** unit-level mdadm command sequencing assertion (the existing pattern in `raid_imsm_test.go`); lab test on real Intel RST hardware (Gilfoyle to provision).

---

### `LVM-DEPLOY` — LVM root from layout

**Status:** TODO. Defer unless customer asks (mark in plan).

**Owner:** Richard.

**Files to touch:**
- `pkg/api/types.go` — extend `DiskLayout` schema with `VGs []VolumeGroupSpec` and `LVs []LogicalVolumeSpec`.
- `internal/deploy/finalize.go` — add `prepareDracutForLVM` (parallel to `prepareDracutForRAID`) — install lvm2 in chroot, regenerate initramfs with lvm dracut module.
- `internal/deploy/lvm.go` (new) — pvcreate / vgcreate / lvcreate sequence.

**Pre-conditions:** None, but DRACUT-REGEN's `--force-add lvm` in Sprint 33 is a prerequisite for boot.

**Acceptance:** lab LVM node boots from /dev/mapper/vg0-root.

**Test plan:** internal/deploy/lvm_test.go with command-recorder pattern.

---

## Sprint 36 — Reactive config model

**Sprint goal:** an operator edits one node's IP. Only the network-related plugins re-render and re-push to the affected nodes — no full `clustr deploy --all` required.

**Sprint exit criteria:**
- [ ] Design doc landed (this is required before code).
- [ ] Edit a hostname → only hostname/hosts/sssd plugins re-push.
- [ ] Two plugins each own a region of `/etc/security/limits.conf` via begin/end markers; editing one doesn't blow away the other.

**Theme:** the single biggest competitor steal. Changes the deploy mental model.
**Owner:** Richard. Needs design doc first.

| ID | Item | Effort | Status |
|---|---|---|---|
| `CONFIG-OBSERVE` | Plugin config-key observer pattern | was 5d → 10d | DESIGN-DOC-FIRST |
| `ANCHORS` | Anchors-based partial-file edits | 1d | TODO |

**Effort revised:** `CONFIG-OBSERVE` was estimated 5d. Reality: `internal/config/` exists but the observer/diff/render plumbing is not wired through the existing `clientd/configapply.go` apply path. Wiring observer pattern into existing apply requires changes to clientd, server, and DB schema for tracking last-rendered hash. **10d minimum**, plus a 2d design doc preceding code.

**Source:** clustervisor `@settings(global_observe=...)` decorator + `cv_reconfigure.py` instruction processor; their `anchors` field on file-write instructions.

**Why it matters:** today we deploy imperatively (`clustr deploy <node>`) — operators run "deploy --all" after every edit just in case. The reactive model converges automatically. Long-term correct answer.

### `CONFIG-OBSERVE` — plugin config-key observer

**Status:** DESIGN-DOC-FIRST. **Do not start coding without Richard signing off the design doc** at `docs/design/reactive-config.md`.

**Owner:** Richard.

**Files to touch (after design):**
- `internal/config/observer.go` (new) — observer registration, diff engine, render scheduler.
- `internal/config/plugin.go` (new) — plugin interface with `WatchedKeys() []string`, `Render(state) ([]InstallInstruction, error)`.
- `internal/db/migrations/108_config_render_state.sql` (new) — table tracking per-(node, plugin) last-rendered hash + last-pushed timestamp.
- `internal/clientd/configapply.go` — extend to apply selectively-pushed plugin output without a full reapply.

**Design doc must answer:**
1. Exact-path subscriptions vs regex (start with exact-path, per the original plan).
2. Idempotency contract on plugin Render (forbid side effects).
3. Push protocol: same `config_push` WS message as today, with a per-plugin tag?
4. Race: two operator edits arrive simultaneously. Single linear render queue, or coalesce?
5. Failure semantics: one plugin fails to render — the others still push?

**Test plan:** Specified in design doc.

---

### `ANCHORS` — partial-file edits via begin/end markers

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `pkg/api/types.go` — extend `InstallInstruction` with `Anchors *AnchorPair` (begin/end strings).
- `internal/deploy/inchroot.go:applyOverwrite` — when `instr.Anchors != nil`, replace only the region between begin/end markers, preserving anything outside. Insert markers if absent.

**Signature change:**
```go
type InstallInstruction struct {
    // ... existing ...
    Anchors *AnchorPair `json:"anchors,omitempty"`
}
type AnchorPair struct {
    Begin string `json:"begin"` // e.g. "# BEGIN clustr/limits"
    End   string `json:"end"`   // e.g. "# END clustr/limits"
}
```

**Acceptance:**
- Two install_instructions both target `/etc/security/limits.conf` with distinct anchors. Apply both → file contains both regions, neither overwrites the other.

**Test plan:** `internal/deploy/inchroot_test.go` — add `TestInstallInstruction_Overwrite_Anchors_TwoPluginsCoexist`.

---

## Sprint 37 — Stateless / diskless boot mode

**Sprint goal:** a compute node PXE-boots, mounts a NFS or RAM-loaded rootfs, runs slurmd, never installs to disk. Cluster-wide image swap is one symlink update.

**Sprint exit criteria:**
- [ ] Lab node boots stateless from cloner → reachable on cluster network → slurmd registers.
- [ ] Roll image: change cluster-wide pointer → next reboot loads new initrd.
- [ ] Existing block-clone path unchanged for nodes flagged stateful.

**Theme:** product axis, v0.2 candidate.
**Owner:** Richard.

| ID | Item | Effort | Status |
|---|---|---|---|
| `DISKLESS` | PXE-loaded rootfs, no disk install | 7d | TODO |

**Source:** clustervisor `cv_diskless.py` + `client/tasks/download-stateless-image.py`.

**Commercial gap:** Warewulf / OpenHPC default-stateless shops will not switch to clustr without this. Concrete competitive miss.

### `DISKLESS` — stateless boot mode

**Status:** TODO.

**Owner:** Richard. Do not parallelize across owners — single design.

**Files to touch:**
- `internal/db/migrations/109_node_operating_mode.sql` (new) — `ALTER TABLE nodes ADD COLUMN operating_mode TEXT NOT NULL DEFAULT 'block_install' CHECK (operating_mode IN ('block_install','filesystem_install','stateless_nfs','stateless_ram'));`.
- `internal/server/handlers/boot.go:106:ServeIPXEScript` — branch on `node.OperatingMode`. Stateless variants chain to a different iPXE path (kernel + initrd-with-rootfs, no `boot=clustr-deploy`).
- `internal/initramfs/` — new variant builder that includes the captured image as initrd contents, OR a netroot kernel arg pointing at the cluster NFS export.
- `internal/network/` — ensure the cluster network plugin exposes an NFS export of `/var/lib/clustr/images/<id>/rootfs/`.

**Pre-conditions:** Sprint 33 STREAM-LOG-PHASE (so the operator can see boot progress without ssh into a stateless node that has no SSH yet).

**API contract:**
```
PATCH /api/v1/nodes/{id}
{ "operating_mode": "stateless_nfs" }
→ 200 NodeConfig
```

**Acceptance:** lab node operating_mode=stateless_nfs → PXE → boots → slurmd online.

**Test plan:** integration test in `internal/server/handlers/boot_test.go`; lab E2E.

---

## Sprint 38 — Stats / telemetry breadth + agent-less monitoring

**Sprint goal:** monitor a node before it's enrolled, during deploy, and after it's broken — without depending on clustr-clientd. Existing on-node stats expand with InfiniBand, MegaRAID, and Intel SSD telemetry.

**Sprint exit criteria:**
- [ ] PROBE-3 reachability shown on Nodes table for any node with a known IP, regardless of clientd state.
- [ ] EXTERNAL-STATS goroutine pool ingests BMC sensors for nodes with `bmc_config_encrypted` set, every 60s.
- [ ] `expires_at` set on stats writes; auto-stale stats vanish from "current" views.
- [ ] Alert lifecycle: push → set → unset → expire works through `/alerts/...`.
- [ ] Stat registry: declared metrics show with chart-grouping hint in UI.
- [ ] Intel SSD plugin returns SMART data for one lab node.

**Theme:** metric ergonomics + missing collectors + monitor-without-agent.
**Owner:** Richard (stats registry), individual collector authors.

**Order:** PROBE-3 (smallest, biggest visibility win, no deps) → STAT-EXPIRES (schema) → STAT-REGISTRY (typed metrics — affects EXTERNAL-STATS shape) → SYSTEM-ALERT-FRAMEWORK → EXTERNAL-STATS → INTELSSD-PLUGIN. IB-PLUGIN and MEGARAID-PLUGIN are **already shipped** (see status line below) — strike from plan.

| ID | Item | Effort | Status |
|---|---|---|---|
| `PROBE-3` | Three reachability probes | <1d | TODO |
| `EXTERNAL-STATS` | Agent-less BMC/IPMI/SNMP probes for unenrolled-or-broken nodes | 2 sprints (~10d) | TODO |
| `STAT-EXPIRES` | `expires_at` column on stats writes | was 0.5d → 1d | TODO |
| `SYSTEM-ALERT-FRAMEWORK` | Push/set/unset/expire lifecycle on alerts | 1d | TODO |
| `STAT-REGISTRY` | Typed metric registry with chart-grouping hint | 2d | TODO |
| ~~`IB-PLUGIN`~~ | InfiniBand stats collector | — | DONE — `internal/clientd/stats/infiniband.go` (181 lines), wired in `client.go:98` |
| ~~`MEGARAID-PLUGIN`~~ | LSI MegaRAID stats | — | DONE — `internal/clientd/stats/megaraid.go` (216 lines), wired in `client.go:101` |
| `INTELSSD-PLUGIN` | Intel enterprise SSD SMART | 0.5d | TODO |

**Source:** clustervisor `common/stat_plugins/` + `register("float", ..., ddcg=...)` pattern; `stats/workers/external_{parent,child}.py` + `stats/workers/plugins/{access,external-bmc,snmp}.py`; `alert/push|set|unset` endpoints in `cv_serverd/api.py`.

**Note:** `EXTERNAL-STATS` should be built as a **goroutine pool inside clustr-serverd** — clustervisor's separate-daemon architecture is a Python GIL constraint we don't import.

### `PROBE-3` — three reachability probes (no clientd required)

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/server/probes/` (new package).
  - `ping.go` — ICMP probe via `golang.org/x/net/icmp`. Already in go.sum? Check `go.sum` — if not, add. This requires a privileged socket on Linux; either run setcap or use a `clustr-privhelper` round-trip. **Decision required:** for v0.1.x, run the probe inside clustr-serverd with `CAP_NET_RAW`; document in deploy docs.
  - `ssh.go` — TCP connect to port 22, read banner string, timeout 3s. No deps.
  - `ipmi_mc.go` — `ipmitool -I lanplus -H <bmc_ip> -U <user> -P <pass> mc info`, parse exit code only.
- `internal/server/probes/probes.go` — `RunAllOnce(ctx, node) (ProbeResult, error)`.
- `internal/server/handlers/node_health.go` — extend `/health` response with `probes: { ping: bool, ssh: bool, bmc: bool, last_checked: ts }`.
- `internal/server/server.go` — register a goroutine pool (50 workers, 60s tick) that probes every node.
- `web/src/routes/nodes.tsx` — three small dots in the row showing the latest result.

**Signature:**
```go
// internal/server/probes/probes.go
type ProbeResult struct {
    Ping       bool      `json:"ping"`
    SSH        bool      `json:"ssh"`
    BMC        bool      `json:"bmc"`
    CheckedAt  time.Time `json:"checked_at"`
}
func RunAllOnce(ctx context.Context, node *api.NodeConfig) ProbeResult
```

**API contract:**
```
GET /api/v1/nodes/{id}/probes
→ 200 { "ping": true, "ssh": false, "bmc": true, "checked_at": "..." }
```

**Acceptance:**
- Power off vm201 → within 60s the Nodes table shows ping=red, ssh=red, bmc=green (BMC stays up).
- Pull network cable on a lab node → all three turn red.

**Test plan:** unit tests with stubbed sockets / fake `ipmitool`. Integration test on cloner.

---

### `EXTERNAL-STATS` — agent-less BMC/IPMI/SNMP probes

**Status:** TODO. Sized as 2 sprints (~10d).

**Owner:** Richard. **Build inside clustr-serverd as a goroutine pool, NOT a separate daemon.**

**Pre-conditions:** STAT-EXPIRES (writes need to set `expires_at`), STAT-REGISTRY (declares metric semantics).

**Files to touch:**
- `internal/server/stats/external/` (new package — note placement under `internal/server/`, not `internal/clientd/`, since this runs server-side).
  - `pool.go` — goroutine pool (configurable size, default 20 workers).
  - `bmc.go` — collect `ipmi-sensors` per-node. Reuse `internal/ipmi/ipmi.go:GetSensorData` and the existing `bmc_config_encrypted` decrypt path.
  - `snmp.go` — gosnmp-based collector for switch/PDU.
- `internal/server/handlers/stats.go` — extend with `/api/v1/nodes/{id}/external_stats` endpoint that returns the most recent BMC/SNMP sample.

**API contract:**
```
GET /api/v1/nodes/{id}/external_stats
→ 200 [
  { "plugin":"bmc", "sensor":"cpu_temp", "value":58.0, "unit":"celsius", "ts":"..." },
  ...
]
```

**Acceptance:**
- Configure BMC creds on a node. Within one collection cycle, BMC sensor values appear in the response.
- Stop clustr-clientd on the same node — external_stats keeps flowing.

**Test plan:** Stub IPMI runner, table-test the worker pool.

---

### `STAT-EXPIRES` — `expires_at` on stats

**Status:** TODO. Effort revised `0.5d → 1d` (the schema change is small but the "current view" filter must be threaded through stats query handlers + Prometheus exporter).

**Owner:** Richard.

**Files to touch:**
- `internal/db/migrations/110_node_stats_expires_at.sql` (new) — `ALTER TABLE node_stats ADD COLUMN expires_at INTEGER;` (Unix seconds, nullable for stats with no TTL).
- `internal/db/node_stats.go` — extend `InsertSamples` to accept and write `expires_at`.
- `internal/server/handlers/stats.go` — "current values" queries filter `WHERE expires_at IS NULL OR expires_at > strftime('%s','now')`.
- `internal/server/metrics_collector.go` (Prometheus exporter) — same filter.

**Migration SQL:**
```sql
ALTER TABLE node_stats ADD COLUMN expires_at INTEGER;
CREATE INDEX IF NOT EXISTS idx_node_stats_expires ON node_stats(expires_at) WHERE expires_at IS NOT NULL;
```

**Acceptance:**
- Insert a sample with `expires_at = now() + 30s`. After 30s, /stats current view no longer returns it.

**Test plan:** `internal/db/node_stats_test.go` — `TestNodeStats_RespectsExpiresAt`.

---

### `SYSTEM-ALERT-FRAMEWORK` — alert lifecycle

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/alerts/lifecycle.go` (new) — `Push(key, device)`, `Set(key, device, value)`, `Unset(key, device)`, `Expire(key, device, ttl)`.
- `internal/db/migrations/111_alert_lifecycle.sql` (new) — extend existing alerts table with a state column.
- `internal/server/handlers/alerts.go` — endpoints `/alerts/push/{key}/{device}` etc.

**API contract:**
```
POST /api/v1/alerts/push/{key}/{device}
{ "value": 87.5, "severity": "warn" }
→ 204
```

**Acceptance:**
- Trigger the same alert twice → only one alert row in DB (push is idempotent on key+device).
- Unset → state transitions to closed.

**Test plan:** unit + integration in `internal/alerts/`.

---

### `STAT-REGISTRY` — typed metric registry

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/clientd/stats/stats.go` — extend the `Plugin` interface with a `Schema() []MetricDecl` method declaring metric semantics (type, unit, upper-bound, dashboard-chart-grouping hint).
- `pkg/api/types.go` — `MetricDecl` struct.
- `internal/clientd/heartbeat.go` — first heartbeat ships `Schema()` result; server caches per-node.
- Server uses the schema to drive the UI's per-node Sensors tab (currently free-form).

**Signature:**
```go
type MetricDecl struct {
    Name         string  `json:"name"`     // e.g. "used_memory"
    Type         string  `json:"type"`     // "float", "int", "bool"
    Unit         string  `json:"unit"`
    Upper        float64 `json:"upper,omitempty"`
    Title        string  `json:"title"`    // human-readable
    ChartGroup   string  `json:"chart_group,omitempty"` // e.g. "GPU Memory Usage"
}

type Plugin interface {
    Name() string
    Collect(ctx context.Context) []Sample
    Schema() []MetricDecl  // NEW
}
```

**Acceptance:**
- Sensors tab shows known metrics grouped by `ChartGroup` (e.g. all "GPU Memory Usage" metrics in one chart).

**Test plan:** unit tests on each plugin's Schema() output.

---

### `INTELSSD-PLUGIN` — Intel enterprise SSD SMART

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/clientd/stats/intelssd.go` (new) — wraps `isdct show -smart` (Intel Datacenter Tool). If `isdct` not present, plugin returns no samples (graceful degrade).
- `internal/clientd/client.go:98` — register the new plugin.

**Acceptance:** lab node with Intel DC SSD reports SMART data.

**Test plan:** stub `isdct` output, table-test parser.

---

## Sprint 39 — Slurm day-2 integration

**Sprint goal:** clustr understands live slurm state — pending jobs, drained nodes, running upgrades — and operators can drain/undrain a node from the UI without ssh.

**Sprint exit criteria:**
- [ ] `/api/v1/slurm/state` returns live `squeue` + `sinfo` output via slurmrestd.
- [ ] LDAP user creation auto-syncs to `slurmdbd` association.
- [ ] Bundles tab: live state (which nodes have which bundle, drift, in-progress upgrades).
- [ ] Per-node drain/undrain action visible + working from node detail panel.

**Theme:** clustr currently pushes slurm config but doesn't talk to slurmctld for live state.
**Owner:** Richard.

| ID | Item | Effort | Status |
|---|---|---|---|
| `SLURM-REST` | slurmrestd JWT integration for live state | was 2d → 3d | TODO |
| `SLURM-ACCOUNTS` | LDAP user → slurmdbd association sync | 3d | TODO |
| `SLURM-CHANNEL` | Channel concept for slurm RPM bundles | 2d | TODO |
| `BUNDLES-DAYTWO` | Bundles tab shows live state | 1d | TODO |
| `SLURM-DRAIN-UI` | Per-node drain/undrain action | 1d | TODO |

**Source:** clustervisor `slurm_server_legacy.py` (JWT-signed REST) + `cv_slurm_accounts.py` + `/slurm/avail` channel query.

**Order:** SLURM-REST first (everything else depends on read-side). SLURM-DRAIN-UI is the smallest second item that exercises write-side. SLURM-CHANNEL is independent.

### `SLURM-REST` — slurmrestd integration

**Status:** TODO. Effort revised `2d → 3d` (JWT setup + cert deployment is non-trivial).

**Owner:** Richard.

**Files to touch:**
- `internal/slurm/restclient.go` (new) — JWT-signed HTTP client for slurmrestd.
- `internal/slurm/manager.go` — extend with live-state methods using the rest client.
- `internal/server/handlers/dashboard.go` — surface live state in dashboard.
- `internal/db/migrations/112_slurm_jwt_secret.sql` (new) — store the slurmrestd JWT secret encrypted (use the existing `secrets` package).

**Signature:**
```go
type RESTClient struct {
    BaseURL string
    JWT     string
    Client  *http.Client
}
func (c *RESTClient) ListJobs(ctx context.Context) ([]SlurmJob, error)
func (c *RESTClient) ListNodes(ctx context.Context) ([]SlurmNode, error)
func (c *RESTClient) DrainNode(ctx context.Context, name, reason string) error
```

**API contract:**
```
GET /api/v1/slurm/state
→ 200 { "jobs": [...], "nodes": [...], "partitions": [...] }
```

**Acceptance:** dashboard widget shows running job count.

**Test plan:** integration test against slurmrestd in lab.

---

### `SLURM-ACCOUNTS` — LDAP → slurmdbd sync

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/slurm/accounts.go` (new) — `EnsureAssociation(user, account)`.
- `internal/ldap/` — wire user-create event to call `EnsureAssociation`.

**Acceptance:** create LDAP user → `sacctmgr show user` lists them.

---

### `SLURM-CHANNEL` — slurm RPM channels

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/slurm/repo.go` — extend bundle metadata with channel field.
- `internal/db/migrations/113_slurm_bundle_channel.sql` (new) — `ALTER TABLE slurm_bundles ADD COLUMN channel TEXT NOT NULL DEFAULT 'release';`.
- `internal/server/handlers/bundles.go` — filter by channel.

**Acceptance:** subscribe a node group to channel `testing` → only testing-channel bundles appear.

---

### `BUNDLES-DAYTWO` — live bundle state

**Status:** TODO.

**Owner:** Richard + Dinesh.

**Files to touch:**
- `web/src/routes/slurm.tsx#bundles` — extend the bundles tab with a per-node deployment-state column.
- `internal/server/handlers/bundles.go` — extend `/api/v1/slurm/bundles` response with deployment state.

**Acceptance:** Bundles tab shows "12/16 nodes on slurm-v24.11.4-clustr5".

---

### `SLURM-DRAIN-UI` — per-node drain/undrain

**Status:** TODO.

**Owner:** Dinesh.

**Files to touch:**
- `web/src/routes/node-detail-tabs.tsx` — add Drain/Undrain action.
- `internal/server/handlers/nodes.go` — call SLURM-REST.

**API contract:**
```
POST /api/v1/nodes/{id}/slurm/drain { "reason": "maintenance" }
POST /api/v1/nodes/{id}/slurm/undrain
```

**Acceptance:** click Drain → `sinfo` shows node drained.

---

## Sprint 40 — Network plane

**Sprint goal:** clustr can run the cluster's DHCP+DNS without external dnsmasq, and SNMP-collect switch/PDU/UPS state.

**Sprint exit criteria:**
- [ ] Optional dnsmasq plugin: enable in cluster brain → existing dnsmasq config replaced with clustr-managed.
- [ ] SNMP collector reaches a lab switch and reports interface state.

**Theme:** own the cluster's network identity, not just compute.
**Owner:** Gilfoyle (operational), Richard (collector contracts).

| ID | Item | Effort | Status |
|---|---|---|---|
| `DHCP-DNS` | Optional dnsmasq plugin | 2d | TODO |
| `SNMP-COMPOSER` | Switch / PDU / UPS YAML schemas + SNMP collector | 3d | TODO |

**Source:** clustervisor `dhcp_dns_server.py` + `cv_snmp.py`.

### `DHCP-DNS` — optional dnsmasq plugin

**Status:** TODO. Optional — only enable when operator opts in.

**Owner:** Gilfoyle.

**Files to touch:**
- `internal/network/dnsmasq.go` (new) — render dnsmasq config from cluster brain.
- `internal/server/handlers/network.go` — toggle endpoint.

---

### `SNMP-COMPOSER` — SNMP collector

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/server/stats/external/snmp.go` (overlap with EXTERNAL-STATS) — gosnmp v2c collector.
- Per-vendor schema files in `internal/server/stats/external/snmp_schemas/`.

---

## Sprint 41 — Auth + safety hardening

**Sprint goal:** RBAC roles map to posix groups, plugins can declare priority/danger/backup invariants, and operators get journalctl from the web without ssh.

**Sprint exit criteria:**
- [ ] Member of posix group `cluster-ops` auto-resolves to admin in clustr UI.
- [ ] An install_instruction marked `is_dangerous` rejects without `--force`.
- [ ] Plugin priority enforced: selinux configures before sshd restarts.
- [ ] `GET /api/v1/nodes/{id}/journal?unit=sssd` returns a streamed journal.

**Theme:** opt-in security primitives + plugin guardrails + RBAC.
**Owner:** Richard.

| ID | Item | Effort | Status |
|---|---|---|---|
| `RBAC-ROLES` | Generalize users → roles + role_assignments + group-aware resolution | 1w | TODO |
| `JOURNAL-ENDPOINT` | `journalctl --unit X --since Y` via clientd | 0.5d | TODO |
| `DANGEROUS-DECORATOR` | `IsDangerous(reason)` on plugins / install_instruction steps | 0.5d | TODO |
| `PLUGIN-PRIORITY` | `Priority(N)` on plugins / install_instructions | 0.5d | TODO |
| `PLUGIN-BACKUPS` | Per-plugin file backups with retention `plugin_backups=N` | 1d | TODO |
| ~~`MUNGE-OPT`~~ | REJECTED — see anti-patterns. | n/a | REJECTED |

**Source:** clustervisor `resolve_roles()` (`server/cv_config.py:6065`), plugin decorators (`@is_dangerous`, `set_priority`, `plugin_backups`), `CVSystemdUnit.getlog` (`common/cv_dbus.py:166`).

**Order:** JOURNAL-ENDPOINT first (smallest, biggest demo win). RBAC-ROLES is the largest; design-doc first. Plugin decorators are independent.

### `RBAC-ROLES` — generalize roles + group-aware resolution

**Status:** TODO. **Design-doc-first.**

**Correction to original plan:** the plan said `internal/server/auth/`. **That directory does not exist.** Auth lives in `internal/server/middleware.go` (`requireRole` at line 304) and `internal/server/handlers/auth.go`. The new RBAC layer should live in a new `internal/auth/` package (note: `internal/auth/`, not `internal/server/auth/` — this is a cross-cutting concern used by both server middleware and handlers).

**Owner:** Richard.

**Files to touch:**
- `internal/auth/rbac.go` (new) — `ResolveRoles(uid string) (isAdmin bool, roles []string, perms map[string]bool, error)`.
- `internal/db/migrations/114_roles_and_assignments.sql` (new) — new `roles` and `role_assignments` tables, populated from existing `users.role` column.
- `internal/server/middleware.go:requireRole` — delegate to `auth.ResolveRoles`.
- `internal/db/users.go` — extend `User` with Groups field, populated from LDAP `memberOf`.

**Migration SQL (sketch):**
```sql
CREATE TABLE roles (
  id TEXT PRIMARY KEY,
  name TEXT UNIQUE NOT NULL,
  permissions_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE role_assignments (
  id TEXT PRIMARY KEY,
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('user','posix_group')),
  subject_id TEXT NOT NULL,
  UNIQUE(role_id, subject_kind, subject_id)
);

-- Backfill from users.role:
INSERT INTO roles(id,name,permissions_json) VALUES
  ('admin','admin','{"*":true}'),
  ('operator','operator','{"node.read":true,"node.write":true}'),
  ('viewer','viewer','{"node.read":true}');
INSERT INTO role_assignments(id, role_id, subject_kind, subject_id)
  SELECT lower(hex(randomblob(16))), role, 'user', id FROM users;
```

**Signature:**
```go
type Resolution struct {
    IsAdmin     bool
    Roles       []string
    Permissions map[string]bool
}
func ResolveRoles(ctx context.Context, db *db.DB, userID string) (*Resolution, error)
```

**Acceptance:**
- A user in posix group `cluster-ops` with no per-user role row resolves to admin if `cluster-ops` is mapped to the admin role.
- Adding a user to `cluster-ops` in LDAP → next session their role updates.

**Test plan:** unit + integration in `internal/auth/`. Add `internal/server/middleware_test.go` extension for the group-aware path.

---

### `JOURNAL-ENDPOINT` — journalctl proxy via clientd

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/clientd/journal.go` — already exists. Extend to support a streaming sub-mode (`--follow`).
- `internal/server/handlers/clientd.go` — add `GET /api/v1/nodes/{id}/journal?unit=X&since=Y`.

**API contract:**
```
GET /api/v1/nodes/{id}/journal?unit=sssd&since=10m
→ 200 { "lines": [...] } (or SSE for follow=true)
```

**Acceptance:** click a node → Journal tab → enter unit name → see lines.

**Test plan:** integration test against an existing clustr-clientd in lab.

---

### `DANGEROUS-DECORATOR` — `is_dangerous` flag

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `pkg/api/types.go` — add `IsDangerous bool` and `DangerousReason string` to `InstallInstruction`.
- `internal/deploy/inchroot.go:applyInstallInstructions` — refuse to apply when `instr.IsDangerous && !ctx.Force`. Force comes from a deploy flag.

**Acceptance:** install_instruction with `is_dangerous=true` is rejected on a normal deploy.

---

### `PLUGIN-PRIORITY` — `Priority(N)` ordering

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `pkg/api/types.go` — add `Priority int` to `InstallInstruction` (lower = earlier).
- `internal/deploy/inchroot.go:applyInstallInstructions` — sort by Priority before applying.

**Acceptance:** unit test with three instructions, priorities 30, 10, 20 → applied in 10, 20, 30 order.

---

### `PLUGIN-BACKUPS` — file backups with retention

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/deploy/inchroot.go:applyOverwrite` — before write, copy existing file to `<file>.clustr-backup.<N>`. Rotate at retention limit.

---

## Sprint 42 — Migration + schema discipline

**Sprint goal:** schema changes can skip versions safely, the audit log has a JSONL replay path, and migration drift is loud.

**Sprint exit criteria:**
- [ ] Stats DB lives in a separate file with its own migrations.
- [ ] `from_version → to_version` lookup table allows skipping intermediate migrations.
- [ ] JSON Schema validation rejects an invalid combo at write time.
- [ ] Two-phase validate+commit endpoint pair works for at least one resource.
- [ ] Schema drift between binary and DB shows as a banner.

**Theme:** make schema changes safer + audit log debug-friendlier.
**Owner:** Richard.

| ID | Item | Effort | Status |
|---|---|---|---|
| `STATS-DB-SPLIT` | Move stats time-series to `stats.db` | 1d | TODO |
| `MIGRATE-CHAIN` | `from→to` lookup map for skip-version upgrades | 0.5d | TODO |
| `JSON-SCHEMA` | Per-resource JSON Schema validation at API write | 2d | TODO |
| `VALIDATE-COMMIT` | Two-phase write: validate → commit | 1d | TODO |
| `MULTI-ERROR-ROLLUP` | Aggregate ALL validation failures, not first | 0.5d | TODO |
| `SCHEMA-DRIFT-BANNER` | Surface bin↔DB drift as system alert + UI banner | 0.5d | TODO |
| `NOTICE-PATCH` | Supervised DB migration escape hatch | 1d | TODO |
| `EVENT-LOG-JSONL` | JSONL sidecar to SQL audit_log | 0.5d | TODO |

**Source:** clustervisor `migrate/migrations/lookup.yml` chain pattern, Cerberus schema validator (`cv_schema.py`), `cv_event_store.py`, `upgrade/__init__.py` `CVNoticeCheck/CVNoticePatch`, two-phase `validate_config` + `queue/reconfigure` pattern.

### `STATS-DB-SPLIT`

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/db/db.go` — open a second `sql.DB` handle for `/var/lib/clustr/stats.db`.
- `internal/db/migrations/stats/` (new dir) — separate migration files for stats.
- `internal/db/node_stats.go` — switch to the stats handle.

**Acceptance:** stats schema changes don't bump main `clustr.db` version.

---

### `MIGRATE-CHAIN`

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/db/migrations/lookup.yml` (new) — declares which migrations can be skipped from versions N→M.
- `internal/db/db.go` — extend migration runner to consult lookup.

---

### `JSON-SCHEMA`

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `pkg/api/schema/` (already exists). Verify and extend with cross-resource FK rules.
- `internal/server/middleware.go` — add validating middleware on PUT/POST.

---

### `VALIDATE-COMMIT`

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/server/handlers/changes.go` (already exists — it has `pending_changes` infrastructure). Extend with explicit `/validate` endpoint.

---

### `MULTI-ERROR-ROLLUP`

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/server/handlers/helpers.go` — extend `writeValidationError` to accept `[]ValidationError`.

---

### `SCHEMA-DRIFT-BANNER`

**Status:** TODO.

**Owner:** Richard + Dinesh.

**Files to touch:**
- Server: emit a system alert on startup if the binary's expected schema version > DB version.
- `web/src/components/AppShell.tsx` — banner consumer.

---

### `NOTICE-PATCH`

**Status:** TODO. Default upgrade path remains RPM. This is the supervised escape hatch only.

**Files to touch:**
- `internal/db/notice.go` (new) — `Check()` checks current DB shape, returns "patch needed" status.
- `cmd/clustr-serverd/notice.go` (new) — `clustr-serverd notice check|patch` subcommands.

---

### `EVENT-LOG-JSONL`

**Status:** TODO.

**Owner:** Richard.

**Files to touch:**
- `internal/db/audit.go` — extend audit-log writes with a JSONL sidecar to `/var/lib/clustr/audit.jsonl`.

---

## Sprint 43 — v0.2.0 cleanup (non-competitor)

**Sprint goal:** clear the v0.2.0 backlog — remove dead PI workflow code, finish lab matrix, miscellaneous polish.

**Sprint exit criteria:**
- [ ] PI workflow Go code + DB tables + web routes removed.
- [ ] All listed numbered issues either closed or moved to a future sprint.

**Theme:** carry-over from existing backlog.

| ID | Item | Effort | Status |
|---|---|---|---|
| `PI-CODE-WIPE` (#251) | Remove dead PI workflow Go code + drop now-orphaned tables | 1d | TODO |
| `LAB-BLK-1` (#195) | Clone VMID 303, enroll as compute, finish matrix Rows 2/3/6 | 0.5d | TODO |
| `LAB-BLK-2` (#196) | Bake eth0 NetworkManager config + sshd_config persistence into VMID 299 template | 0.5d | TODO |
| `OPS-7` (#214) | Clean up orphaned initramfs build temp dirs | 0.5d | TODO |
| `TEST-2` (#193) | Web↔CLI auth/error semantics parity contract tests | 1d | TODO |
| `DOC-3` (#194) | Justify panic usage in initramfs handler + schema init | 0.5d | TODO |
| `UX-13` (#198) | api.ts polish | 0.5d | TODO |
| `TEST-3` (#199) | Silence jsdom canvas warning in vitest setup | 0.25d | TODO |
| `OPS-4` (#202) | Decide intent of VMID 301/302; move to vmbr10 if needed | 0.25d | TODO |
| `UX-16` (#220) | `clustr group reimage --wait` / SSE progress option | 1d | TODO |
| `UX-17` (#221) | `clustr stats` node resolution uses health endpoint indirectly | 0.5d | TODO |
| `P3` (#226) | Shell WS read deadline + pong handler + max session TTL | 1d | TODO |
| `CI-1` (#227) | Add staticcheck to CI | 0.5d | TODO |
| `BONUS-1` (#228) | Schema responses missing Cache-Control headers | 0.25d | TODO |
| `BONUS-3` (#230) | `wrapNspawnInScope` fallback trap under `NoNewPrivileges=true` | 0.5d | TODO |

### `PI-CODE-WIPE` (#251)

**Status:** TODO.

**Owner:** Richard.

**Files to remove:**
- `internal/db/pi.go`
- `internal/db/user_group_memberships.go`
- `internal/server/handlers/portal/pi.go`
- `internal/server/handlers/portal/managers.go` (verify if PI-only)
- t.Skip()'d tests across the repo (search: `t.Skip(.*pi`)
- Any `/portal/pi` web routes (verify in `web/src/router.tsx`)

**Files to touch (drop tables):**
- `internal/db/migrations/115_drop_pi_tables.sql` (new) — drop now-orphaned tables: `pi_expansion_requests`, `node_group_pi`, …. Run grep on `internal/db/migrations/` for references before dropping.

**Acceptance:** `go build ./...` clean, `go test ./...` passes, web build passes, no PI references in `web/src/`.

---

## Sprint 44 — Cockpit-parity node UX (demo-day gaps)

**Sprint goal:** the inputs an HPC operator reaches for in the first 30 seconds — multi-NIC, hostlist, bulk power, bulk reimage — all work in the UI.

**Sprint exit criteria:**
- [ ] Add a node with 1 IB + 1 mgmt + 1 compute + 1 IPMI interface in a single form.
- [ ] Bulk-add `compute[001-128]` → 128 nodes register in one submit.
- [ ] Multi-select 32 nodes on the Nodes table → click "Power Cycle" → confirm → all 32 are cycled.
- [ ] Multi-select 32 nodes → click "Reimage" → group reimage SSE plays out.
- [ ] Variants: GPU role overrides kernel cmdline without affecting non-GPU nodes.

**Theme:** the inputs an HPC operator reaches for in the first 30 seconds.
**Owner:** Dinesh (UI), Richard (backend schema extensions).

These are the top 5 missing inputs/actions Richard's node-input-field inventory identified — what cockpit exposes on the Nodes surface that clustr does not. **Each is concretely demo-day-visible.**

**Order:** HOSTLIST-BULK-ADD depends on Sprint 34 `HOSTLIST` (parser must land first). MULTI-NIC-EDITOR is independent. BULK-MULTISELECT-POWER → BULK-MULTISELECT-ACTIONS (the second extends the first). VARIANTS-SYSTEM is independent and large; gate behind real customer ask.

| ID | Item | Effort | Status |
|---|---|---|---|
| `MULTI-NIC-EDITOR` | Multi-NIC / fabric (IB) editor on node form | 1 sprint (~5d) | TODO |
| `HOSTLIST-BULK-ADD` | Hostname range expansion in bulk-add | 2-3d | TODO |
| `BULK-MULTISELECT-POWER` | Multi-select on nodes table + bulk power | 3-5d | TODO |
| `BULK-MULTISELECT-ACTIONS` | Generalize multi-select to reimage/drain/run-command | 2d on top | TODO |
| `VARIANTS-SYSTEM` | Per-attribute group/role variants | 1w | TODO — gated on customer ask |

**Source:** cockpit node-input-field inventory (Richard dispatch `af2d382c3b88fa761`).

### `MULTI-NIC-EDITOR`

**Status:** TODO.

**Correction:** the plan said "Schema extension on `node_configs.interfaces` (already exists as `[]json`)". Confirmed: `pkg/api/types.go:475` `Interfaces []InterfaceConfig`. The schema is already plural. **The work is purely UI** — the existing AddNodeSheet (`web/src/routes/nodes.tsx:57`) hard-codes one interface. Backend already supports many.

**Owner:** Dinesh.

**Files to touch:**
- `web/src/routes/nodes.tsx:57:AddNodeSheet` — replace single-MAC/IP block with `<InterfaceList>` component.
- `web/src/components/InterfaceList.tsx` (new) — list of interface rows: `{ kind: 'ethernet'|'fabric'|'ipmi', name: 'eth0', mac: '...', ip: '...' }`. Shape mirrors `pkg/api/types.go:InterfaceConfig` and `IBInterfaceConfig`.
- `web/src/components/IBInterfaceForm.tsx` (new) — IB-specific fields (port/key/etc).

**Component shape:**
```tsx
type InterfaceRow =
  | { kind: 'ethernet'; name: string; mac: string; ip?: string }
  | { kind: 'fabric'; device: string; ip?: string; port?: number }
  | { kind: 'ipmi'; channel: number; ip: string; user: string; pass: string }

type InterfaceListProps = {
  value: InterfaceRow[]
  onChange: (next: InterfaceRow[]) => void
}
```

**Acceptance:**
- AddNodeSheet → click "Add interface" 4 times → fill IB + 3 ethernet → submit → server stores all 4 in `interfaces[]`.

**Test plan:** vitest interaction test that adds 3 interfaces and verifies the POST body shape.

---

### `HOSTLIST-BULK-ADD`

**Status:** TODO.

**Pre-conditions:** Sprint 34 `HOSTLIST` (parser must exist).

**Owner:** Dinesh.

**Files to touch:**
- `web/src/routes/nodes.tsx:344:BulkAddNodes` — extend the CSV/YAML input with a third "Hostlist" tab.
- Use `expandHostlist` from `web/src/lib/hostlist.ts` (Sprint 34 product).

**Acceptance:** paste `compute[001-128]\ngpu[001-008]` → preview shows 136 rows.

---

### `BULK-MULTISELECT-POWER`

**Status:** TODO.

**Owner:** Dinesh + Richard (backend bulk endpoint).

**Files to touch:**
- `web/src/routes/nodes.tsx` — add row checkboxes + a sticky bulk-action bar at the bottom (matches existing `groups.tsx` pattern).
- `internal/server/handlers/ipmi.go` — new `POST /api/v1/nodes/bulk/power/{action}` accepting `{ "node_ids": [...] }`. Server-side fanout with a goroutine pool (limit 32 concurrent).

**API contract:**
```
POST /api/v1/nodes/bulk/power/cycle
{ "node_ids": ["...", "..."] }
→ 200 { "results": [{ "node_id": "...", "ok": true }, ...] }
```

**Acceptance:** select 4 nodes, click Cycle, all cycle.

---

### `BULK-MULTISELECT-ACTIONS`

**Status:** TODO.

**Owner:** Dinesh.

**Files to touch:** extend the action bar from BULK-MULTISELECT-POWER with reimage / drain / run-command / set-boot.

---

### `VARIANTS-SYSTEM`

**Status:** TODO. Gated on real customer ask. Don't speculate.

**Owner:** Richard + Dinesh. Design-doc-first.

---

## Sprint 45 — Cockpit-parity ops surfaces (operator power-tools)

**Sprint goal:** operators can run a command across all nodes in a group, build custom dashboards, and write monitor rules — all from the UI.

**Sprint exit criteria:**
- [ ] `clustr exec --group compute "uname -r"` AND its UI equivalent run a fan-out and stream output.
- [ ] Custom dashboard with at least 2 widgets configurable by a non-engineer.
- [ ] Monitor rule with threshold + history view + alert action mapping.

**Theme:** cluster-wide actions + dashboards + observability surfaces.
**Owner:** Dinesh (UI), Richard (backend).

| ID | Item | Effort | Status |
|---|---|---|---|
| `PARALLEL-EXEC-UI` | pdsh-equivalent UI bound to groups + selectors | 1-2w | TODO |
| `CUSTOM-DASHBOARDS` | Operator-built monitoring views | 4-6w | TODO |
| `MONITOR-RULES-UI` | Extend alerts UI with rule history, action mapping | 1w | TODO |
| `SYNCED-FILES` | Server-managed config-file push | 3w | TODO |
| `ROLE-PACKAGES` | Declared package sets per-role | 2w | TODO |
| `EXPRESSION-BUILDER` | UI for the named-compute-expression rule language | 1w | TODO |

**Source:** cockpit UI review (Richard dispatch `a90d7f63bf7ca7f22`) ranked items 2-5; cockpit node inventory's "non-node flag" (Computed/Group stat builder, Synced File Settings, Expression Builder).

**Order:** PARALLEL-EXEC-UI first (smallest, exercises existing single-node WS exec at fan-out). MONITOR-RULES-UI next (extends existing alerts UI). CUSTOM-DASHBOARDS depends on STAT-REGISTRY (Sprint 38). EXPRESSION-BUILDER depends on either MONITOR-RULES-UI or CUSTOM-DASHBOARDS landing first.

### `PARALLEL-EXEC-UI`

**Status:** TODO.

**Owner:** Dinesh.

**Files to touch:**
- `web/src/routes/exec.tsx` (new) — top-level Exec route. Mirror `web/src/routes/slurm.tsx` shape.
- `web/src/router.tsx` — register the route.
- `internal/server/handlers/exec.go` (already exists for single-node) — extend with bulk endpoint.

**API contract:**
```
POST /api/v1/exec/bulk
{ "selector": "group:compute", "command": "uname -r" }
→ SSE stream of { node_id, line, stream: "stdout"|"stderr"|"exit" }
```

**Acceptance:** Exec page → enter command → see live output from all selected nodes interleaved with row prefixes.

---

### `CUSTOM-DASHBOARDS`

**Status:** TODO.

**Pre-conditions:** Sprint 38 `STAT-REGISTRY` (typed metrics).

**Owner:** Dinesh + Richard.

**Files to touch:**
- `web/src/routes/dashboards.tsx` (new).
- `internal/db/migrations/116_dashboards.sql` (new) — store dashboard definitions.
- `internal/server/handlers/dashboards.go` (new).

**Defer until at least one customer asks** — dashboards are a long-lived investment.

---

### `MONITOR-RULES-UI`

**Status:** TODO.

**Owner:** Dinesh.

**Files to touch:**
- Existing `web/src/routes/alerts.tsx` — extend with rule history + action mapping panels.
- `internal/server/handlers/alert_rules.go` — already exists; extend response shape.

---

### `SYNCED-FILES`

**Status:** TODO.

**Owner:** Richard. Adjacent to bundles model.

---

### `ROLE-PACKAGES`

**Status:** TODO.

**Owner:** Richard.

---

### `EXPRESSION-BUILDER`

**Status:** TODO. Defer until MONITOR-RULES-UI or CUSTOM-DASHBOARDS lands.

---

## Sprint 46 — UI pattern lifts (PatternFly → shadcn)

**Sprint goal:** four cockpit UI patterns (wizard, log viewer, bulk-edit context, description list) ported as shadcn primitives — usable across the app for node-add, log streaming, bulk operations, and detail panels.

**Sprint exit criteria:**
- [ ] Wizard component used in at least one flow (e.g. multi-NIC node-add from Sprint 44).
- [ ] LogViewer used in at least two places (Sprint 33 STREAM-LOG-UI + Sprint 41 JOURNAL-ENDPOINT).
- [ ] BulkEditContext powers the bulk-edit table action (Sprint 44).
- [ ] DescriptionList replaces ad-hoc detail panels in node detail tabs.

**Theme:** steal cockpit's most useful component patterns; build them in shadcn, don't adopt PatternFly.
**Owner:** Dinesh.

| ID | Item | Effort | Status |
|---|---|---|---|
| `WIZARD-COMPONENT` | Multi-step wizard with side-rail nav | 3d | TODO |
| `LOG-VIEWER-COMPONENT` | Inline-search + row-virtualized log viewer | 2d | TODO |
| `BULK-EDIT-CONTEXT` | Multi-row table edit context | 2d | TODO |
| `DESCRIPTION-LIST` | Horizontal/vertical detail-panel layout | 1d | TODO |

**Source:** cockpit's PatternFly v4 components — distinctive patterns worth reproducing in our shadcn setup, not adopting PatternFly itself.

### `WIZARD-COMPONENT`

**Status:** TODO.

**Owner:** Dinesh.

**Files to touch:**
- `web/src/components/ui/wizard.tsx` (new) — provider + step components.
- Test usage: refactor `BulkAddNodes` to use it, OR use it in multi-NIC node-add.

**Component shape:**
```tsx
type WizardStep = { id: string; title: string; component: React.ReactNode; valid: boolean }
type WizardProps = { steps: WizardStep[]; onFinish: () => void; onCancel: () => void }
```

---

### `LOG-VIEWER-COMPONENT`

**Status:** TODO.

**Owner:** Dinesh.

**Pre-conditions:** Sprint 33 STREAM-LOG-UI's first cut might inline-build a viewer; refactor that into the shared component here.

**Files to touch:**
- `web/src/components/ui/log-viewer.tsx` — promote the Sprint 33 viewer to a generic component.
- Use react-virtuoso (already in the dep tree? check `web/package.json`).

---

### `BULK-EDIT-CONTEXT`

**Status:** TODO.

**Owner:** Dinesh.

**Files to touch:**
- `web/src/components/ui/bulk-edit.tsx` (new) — context provider for selected rows + edit actions.

---

### `DESCRIPTION-LIST`

**Status:** TODO.

**Owner:** Dinesh.

**Files to touch:**
- `web/src/components/ui/description-list.tsx` (new) — `<DList><DTerm>...<DDesc>...</DList>` primitive.
- Refactor at least one node-detail tab to use it.

---

## Anti-patterns (explicitly reject)

From the four clustervisor reviews — features they have that we deliberately do **not** copy:

- **Two-language toolchain (Python + Perl).** Stay Go-only. Their boundary tax is real.
- **MongoDB primary store + SQLite-per-device for stats.** Two engines, two operational stories, two backup paths, two failure modes. clustr's single-SQLite-with-WAL is strictly better at our scale; a 1k-node cluster's config fits in <100MB.
- **Decorator-everywhere DSL** (`@settings(...)` with 12 keyword args). Powerful only because of Python dynamism. Use Go interfaces + structs.
- **Bottle as web framework.** Single-threaded, gevent-monkey-patched. Gin (clustr's choice) is strictly better.
- **`distutils.version.LooseVersion`** and similar dead Python stdlib usage. Their codebase shows real bit-rot.
- **IPMI invocation in core server hot path.** clustr-privhelper is the right factoring — keep BMC ops out of the serverd process.
- **Rsync with no Range resume on disconnect.** clustr already does HTTP Range resume (#122) — keep it.
- **Pre-built `initrd-src` checked in as black box.** clustr builds initramfs from scratch deterministically (#162). Keep it.
- **Munge as web/API auth substrate.** Bakes in `uid==0` root-only model that fights OIDC / SSO / multi-tenant. HPC-internal-only at best. If we ever need an inter-daemon transport, use mTLS or HMAC.
- **Pyarmor-obfuscated license module imported across the codebase.** `LicenseError` caught at every mutation. Security-audit hostile (you can't verify the binary you ship), ops-dangerous (license corruption = total brain freeze), incompatible with clustr being open-source.
- **`useradm_action`-style script execution from server.** `spawn(shell_execute(f"{script} {segment}"))` where `segment` comes from a config write = anyone with config write has RCE. Our privhelper boundary is the right model; do not regress.
- **Notice-driven manual upgrade scripts as the *default* upgrade path.** Their `Notice/Patch` is fine as a *supervised escape hatch* (we adopted it as `NOTICE-PATCH` in Sprint 42), but the default remains "RPM lands, daemon restarts, done."
- **Fork-based parent/child worker model in stats** (`stats/fork.py`, 660 lines of `fork()`+SIGUSR1+SIGCHLD+pipe IPC). This is what Go gives us free with goroutines + channels.
- **Cockpit plugin packaging.** Requires `cockpit-ws` on every managed host (security + ops cost), inherits PAM auth from `/etc/passwd` (no SSO/OIDC), desktop-browser-only. Standalone-SPA-embedded-in-server wins for HPC operators.
- **Generic OK/Cancel modals for destructive ops.** Clustr's typed-confirmation pattern (type the node-id, hostname, group name) is materially better — don't lose it in any "simplify the UI" refactor.

---

## Things clustr already does meaningfully better

Don't lose these in any future refactor:

### Imaging / deploy
- Static Go binary in initrd vs Perl interpreter + ~100 .pm modules
- HTTP Range-resume blob fetch (#122) vs plain rsync
- Block-image deploy (bit-perfect golden images) vs filesystem-only
- Reproducible cpio builds (#162, `SOURCE_DATE_EPOCH`) vs non-deterministic
- HTTP token auth (per-node-scoped) vs munge-key-baked-into-initrd
- Verified `BOOTX64.EFI` post-install vs trusting `grub2-install` exit code
- `clustr-verify-boot.service` post-reboot — clustervisor has no phone-home, just trusts deploy worked
- Concurrent partition + blob download — overlaps disk and network I/O

### UX safety
- Typed-confirmation pattern for destructive ops (type node-id for BMC edit, hostname for capture, group name for rolling reimage) vs cockpit's generic OK/Cancel
- SSE-streamed group-reimage progress with per-row state (`/api/v1/node-groups/{id}/reimage/events`) vs cockpit polling
- Encrypted-at-rest BMC credentials (migration `039_bmc_credential_encryption.sql`) vs cockpit's secure_fields mask
- Cancel active reimage as a first-class operation (`DELETE /reimage/active`) — cockpit has no equivalent

### Per-node features
- Per-node BIOS profile assign/detach/read/apply
- Per-node sudoers staging + sync (`node_sudoers.go`)
- Per-node config-history audit log
- Auto-policy state on node groups (`auto-policy-state` API + undo)

### Engineering hygiene
- ~3000 lines of `_test.go` in `internal/deploy/` vs near-zero unit tests in cv-cloner
- Single Go binary deploy (RPM EL8/9/10) — no Mongo, no Cockpit-on-every-host
- clustr-privhelper as the single privilege boundary
- 3-component architecture (clustr-serverd + clustr-clientd + clustr-privhelper) vs their ~5-daemon sprawl

### Operating-mode reach
- Mobile/tablet operator views feasible (Tailwind, responsive primitives) — Cockpit is desktop-only
- Headless / API-first usage — same REST/WS surface for web and CLI
- Embed in third-party portals / iframe — cockpit's CSP makes this a fight

---

## Multi-daemon decomposition — verdict

**Stay 3-component.** clustr-serverd + clustr-clientd + clustr-privhelper. Clustervisor's separate-daemon split is a Python GIL escape hatch + per-feature licensing artifact + appliance-restart artifact — none apply to us. Build new subsystems (external stats, image management, auth watchdog) **inside clustr-serverd as goroutine pools**, not as separate processes.

**The one possible 2027-Q1 exception:** extract `clustr-statsd` only when stat collection load demonstrably contends with API request handling under real customer load. Even then, prefer scaling clustr-serverd horizontally before extracting a daemon.

## Total raw effort

Originally estimated ~95 engineering days across 14 sprints. Hardening pass corrections:

- Sprint 33: STREAM-LOG split into PHASE+UI (matches reality) — net +0d.
- Sprint 34: IPMI-MIN was 2d → 1d (most exists). BOOT-POLICY 0.5d → 1d. Net -0.5d.
- Sprint 36: CONFIG-OBSERVE was 5d → 10d. Net +5d.
- Sprint 38: IB-PLUGIN, MEGARAID-PLUGIN already done (-1.5d). STAT-EXPIRES 0.5d → 1d (+0.5d). Net -1d.
- Sprint 39: SLURM-REST 2d → 3d. Net +1d.

New total: **~99.5 engineering days across 14 sprints.** With Richard + Dinesh + Gilfoyle in parallel, **~5-7 weeks calendar**, with Sprint 36 (reactive config) being the biggest single risk.

## Suggested execution order

1. **Now:** finish v0.1.22 (in flight)
2. **Next 1-2 days:** Sprint 33 (observability/portability — highest ROI, smallest)
3. **Then by customer pull:**
   - Sprint 34 (BootOrder + IPMI + consoles — the #1 convergent gap, demo-day blocker)
   - Sprint 38 (stats breadth + agent-less probes — the #2 convergent gap)
   - Sprint 44 (cockpit-parity node UX — the demo-day input gaps: multi-NIC, hostlist, bulk power)
   - Sprint 35 (disk breadth)
   - Sprint 37 (diskless, the commercial gap)
4. **In parallel:** Sprint 36 (reactive config — needs design doc) staged behind smaller wins
5. **Mid-priority:** Sprint 45 (cockpit ops surfaces: parallel exec, dashboards, monitor rules) — high lock-in but large effort
6. **Quality-of-life:** Sprints 39 / 40 / 41 / 42 / 46
7. **Cleanup pass:** Sprint 43 before any v0.2.0 cut

Reorder freely as customer reality dictates. The two **convergent findings** (IPMI/consoles, agent-less monitoring) should not slip below the demo-day-gap UX work in Sprint 44 — they're prerequisites for the deals that fund everything else.
