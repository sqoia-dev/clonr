# ClusterVisor gap sprint plan — Richard's review

**Status:** Planning only. Not approved. Sits in `docs/` for the founder to review.
**Author:** Richard (technical authority)
**Date:** 2026-05-01
**Source:** `docs/IMPROVEMENTS` (1375 lines, AI-generated comparison vs. ClusterVisor 1.25.04-4844) cross-referenced against `docs/OVERVIEW.md` and `SPRINT.md` head (#119).
**Task IDs:** continue from #119 → start at #120.

---

## TL;DR

The gap analysis is high quality. It correctly identifies the three real product holes:

1. **Day-2 ops surface** — `clustr exec`, `clustr cp`, `clustr console`. Without these, every operator who tries clustr keeps `pdsh` and `ipmitool sol activate` in their muscle memory and concludes clustr is "just provisioning."
2. **On-node stats + alerts** — clustr ships Prometheus `/metrics` server-side only. We cannot answer "which nodes have a degraded RAID array right now?" from inside the product. ClusterVisor can.
3. **Hardware-realistic initramfs + multicast deploy** — our current initramfs is virtio-only. On real Mellanox / Intel / Broadcom / megaraid_sas hardware it fails at "no usable NIC found." And every reimage fans out one HTTP stream per node, which doesn't scale past ~20 simultaneous reimages on a 1 GbE server uplink.

Everything else in IMPROVEMENTS either lands behind those three, is already at parity or better, or violates a standing constraint and gets rejected.

I am proposing **Sprints 20 through 25** below. Sprint 20 is the smallest, highest-leverage real-hardware fix and ships first because the alternative is a real customer trying clustr on a real cluster and watching it fail PXE on day one. Sprints 21 and 22 are the day-2 ops surface (the largest perceived ceiling). Sprint 23 is the stats agent. Sprints 24 and 25 are the imaging/UI follow-ups that compound on top.

---

## How I read IMPROVEMENTS

The author of IMPROVEMENTS is approximating ClusterVisor's surface and telling us where we don't match. That's useful, but ClusterVisor is a 25-year-old plugin-based Python+Perl product with a customer base that drives its surface area. Mirroring it 1:1 is wrong; we will end up with a smaller, opinionated subset.

I'm filtering on three axes:

- **Architectural fit.** Does this respect clustr's standing constraints? (Single privhelper boundary, no containers, signed-RPM-first, wipe-by-default lifecycle, POSIX UID range split, web auth = local users only, clustr replaces OpenHPC for slurm distribution.)
- **Operator daily-driver impact.** How often does this come up in the first 30 days of running a real cluster? "Every day" beats "once a quarter."
- **Reversibility.** Schema additions and new top-level objects (rack model, disk_layouts, boot_entries, exec/cp surface) are Type 1 — get them right. Plugin internals and CLI ergonomics are Type 2 — ship and iterate.

---

## Accepted with high confidence

These are the gaps that are real misses, fit our architecture, and we should build.

### From operator-CLI parity (P0/P5 in IMPROVEMENTS)

- `clustr exec` (parallel exec over the existing clientd WebSocket).
- `clustr cp` (parallel file copy, recursive, preserve).
- `clustr console` (IPMI SOL streamed to the operator terminal; SSH PTY as a fallback).
- `clustr ipmi sel` (batch SEL view / clear / filter).
- Richer `clustr ipmi sensors` (filter, threshold, regex).
- `clustr health` (per-node summary + ping + wait).
- `clustr scheduler` (jobs / partitions read-only) — pulls from `slurmrestd`.
- Selector grammar standardised across all batch commands: `-n NODE`, `-g GROUP`, `--racks`, `--chassis`, `-A` all, `-a` active, `--ignore-status`. This selector grammar is the actual feature; the individual commands inherit it.

### From stats / alerting (P1)

- On-node collector, folded into `clustr-clientd` (not a new daemon — one less systemd unit, one less RPM, one less attack surface). Plugin set v1: cpu, memory, disks, MD, net, infiniband (link state + port counters), nvme, system (loadavg/uptime), firmware versions.
- Plugin set v2 (same sprint, second half): nvidia (GPU temp/ECC/util), megaraid (RAID degraded), zfs (pool state), ntp drift.
- Server table `node_stats(node_id, plugin, sensor, value, ts)` plus retention sweeper.
- Alert rule engine evaluating YAML rules from `/etc/clustr/rules.d/*.yml` on a 60s tick. Routes through the existing webhook dispatcher and SMTP notifier — no new delivery infrastructure.

### From imaging hardening (CG2/CG3/CG10/CG14 — all small fixes)

- **Real-hardware kernel modules in initramfs** (CG2). Today's hardcoded virtio-only list is the single biggest reason a real cluster cannot adopt clustr. This is the earliest sprint, before anything else.
- **xattr / ACL preservation through deploy** (CG3). Busybox tar silently drops POSIX ACLs. SELinux relabel masks part of this; per-file ACLs are gone. Untestable today; needs a deploy round-trip integration test.
- **HTTP `Range:` resume on blob download** (CG10). Half-week change. The retry loop currently restarts at byte 0.
- **Bandwidth + concurrency caps on `/blob`** (CG14). Stops one reimage from saturating the management plane.

### From imaging features (CG1, CG7, CG8, CG12, CG13)

- **Disk layout as a first-class object** (CG7). Today the layout is implicit (auto-recommended at deploy time) or attached to one image. Operators want "use layout `compute-h100-v3` everywhere in this group." Schema addition: `disk_layouts` table + `node_groups.disk_layout_id` + per-node override. Worth doing alongside CG8 because both touch the deploy plan.
- **Per-image install instructions DSL** (CG8). Three-opcode DSL (modify/overwrite/script) executed in the chroot before bootloader. Saves operators from rebuilding an entire image to change `/etc/motd` on the storage role. Small surface area.
- **In-chroot reconfigure pass** (CG12). Today the very first boot of a freshly imaged node has stale `/etc/hosts`, no chronyd peers, no LDAP joined — a 30s-3m "online but useless" window. Fix: factor `internal/clientd/configapply.go` so it can target an arbitrary root, run it from the deploy agent against `/mnt/target` before unmount.
- **UDPCast multicast for fleet reimage** (CG1). The single highest-leverage feature for any site that reimages >20 nodes at once. Required to claim "fleet management" credibly.
- **Per-image stateless / netboot / rescue menu entries** (CG13). Today the iPXE menu is fixed at 3 entries. A `boot_entries` table lets us add Memtest, a rescue boot, and named netboot installers without code changes.

### From web UI (UP1, UP3, UP6, UP9)

- **Per-node Sensors + Event Log + Console tabs in the node Sheet** (UP1). Lands on top of the stats agent. Use `recharts` for charts (MIT, ~120 KB) — not ApexCharts (the ClusterVisor 6 MB bundle is the wrong tradeoff).
- **In-browser IPMI SOL console** (UP3). Pop-out route, `xterm.js` over a WebSocket the server brokers via `ipmitool sol activate`. Generalise the existing `internal/image/shell.ShellManager` pattern. Do not ship noVNC/SPICE/RDP — KVM viewer is niche, SOL is the 95% case in HPC.
- **Slurm Jobs / Partitions UI** (UP6). Pull from `slurmrestd`, lands on the existing `/slurm` route as a new tab. Tables only in v1, no charts.
- **Two-stage commit UI** (UP9). Lightweight pending-changes drawer. Backend P4 first (next item).

### From config workflow (P4)

- **Two-stage commit / pending changes** for LDAP, sudoers, and network mutations. New `pending_changes` table, three new endpoints, one drawer in the AppShell. Default off — preserves current immediate-apply behaviour for everything else. Operators opt in per surface in Settings.

### From hardware coverage (CG5, CG9, CG11)

- **BIOS settings push, vendor-pluggable** (CG5). Start with Intel `intel-syscfg`. Dell `racadm` next. Supermicro `sum` last. Behind a clean `internal/bios/Provider` interface so vendor binaries are swappable. Bundled in the initramfs; sized at <40 MB additional.
- **IMSM / hardware RAID passthrough** (CG9). Common on Xeon servers. `mdadm --imsm-platform-test` detection in `internal/deploy/raid.go`, branch the assembly path.
- **Ubuntu distro driver in the installer** (CG11). Refactor `finalize.go` around a `DistroDriver` interface and add an `ubuntu24` driver. Many HPC sites with mixed Rocky+Ubuntu compute pools want this. Defer SLES.

### From schema / API hygiene (P6)

- **Versioned API + JSON schemas + OpenAPI 3.1**. Add `Api-Version: v1` header on every response, generate JSON schema from `pkg/api` types, ship the OpenAPI 3.1 spec at `/api/v1/openapi.json`. Cheap to do once `pkg/api` is audited; pays back forever in third-party tooling.

### From rack/datacenter model (UP4)

- **Rack model + datacenter view**. Two-table schema (`racks`, `node_rack_position`), single SVG component, rack-scoped bulk power. Doubles as the hook for `--racks` / `--chassis` selectors.

---

## Accepted with reservations (smaller scope or later)

These are real, but smaller, more niche, or worth doing reduced-scope.

- **USB / offline installer** (CG4). Real but niche — air-gapped HPC sites are a small minority. Build it as a one-shot CLI (`clustr-serverd installer build`) producing a hybrid ISO. Defer past Sprint 25.
- **Reproducible initramfs builder** (CG6). The bash builder works in CI and on cloner. Option A (Make + builder Docker image): we don't ship Docker, but a builder image used only at build time is fine. Option B (Buildroot) is correctly the right long-term answer — it's also a quarter of work I am not going to greenlight on the first pass. Path A in Sprint 25; revisit Path B if Path A turns out to be insufficient.
- **NFS server templating, DNS authoritative, switch discovery UI** — backlog. Each is small; bundle them when an operator asks.
- **`clustr user` / `clustr group` / `clustr sshkey` CLI commands** (P5 round 1). Mostly thin wrappers over endpoints that already exist. Worth doing but not until after the exec/console surface lands; we don't want to cement a CLI ergonomics pattern for these before we've shipped `clustr exec` and learned what selector grammar feels right.
- **Schema versioning of every config artifact** (ClusterVisor's 1.0 / 1.1 split). Don't mirror this. Our migrations + `pkg/api` OpenAPI spec do the same job with less ceremony. The piece worth taking is the OpenAPI export.
- **Save-as-view / persistent filters** (UP10 partial). Real productivity win for ops. One-week ticket. Not a Sprint 25 blocker.

---

## Rejected — and why

I am being explicit here because IMPROVEMENTS proposes things that violate our standing commitments.

### Hard rejects (architectural conflict)

- **Web auth via LDAP / external directory.** IMPROVEMENTS doesn't propose this directly, but the "LDAP browser" UP feature in Appendix C edges toward it. Web auth stays on the local users table, full stop. The LDAP browser is fine as a read-only tool that talks to the cluster's LDAP — but it never authenticates the web admin.
- **Cockpit module / Cockpit integration.** ClusterVisor ships a Cockpit module. We have a self-contained React SPA with a single auth model. Cockpit adds another auth layer (PAM), another package dependency, and another upgrade path. Reject.
- **Docker-based reproducible initramfs builder, if it implies shipping Docker as a runtime dependency.** A *build-time* container image (used only by CI to produce reproducible bytes) is fine. A runtime Docker dependency is not. clustr is RPM + source, full stop.
- **License enforcement / pyarmor-style protection / "send logs to ACT" support funnel.** clustr is open-source; we have no license to enforce. Skip.
- **LogVisor AI integration.** Proprietary upstream. Skip.
- **Cron-installed `restart-slurmrestd` daily.** If `slurmrestd` accumulates state, fix `slurmrestd`'s memory hygiene or the upstream Slurm config — don't paper over it with a daily cron.
- **Munge bundling / pymunge.** Slurm's RPMs install munge as a dep already. We don't need our own munge surface.

### Soft rejects (scope / value mismatch)

- **VNC / SPICE / RDP browser console.** noVNC drops a ~1 MB JS bundle just for the protocol. KVM-over-IP is rarely used in HPC ops once SOL is available; operators reach for it for OS-install troubleshooting and clustr handles the install path itself. SOL is the 95% case. Skip VNC/SPICE/RDP entirely.
- **Monaco editor in the browser.** Adds 5 MB of JS for the use case "edit a config file in the UI." We do not edit cluster config files in the browser as the primary workflow; operators use `clustr exec ... vi /etc/...` once the exec surface lands. Tiny syntax-highlighted YAML editor (`react-simple-code-editor` + `prismjs`) is enough for the alert rule editor.
- **Web file manager** (`cv-cockpit-filemanager`). Once `clustr exec` ships, an operator with shell access has every file management workflow already. Ship `clustr exec` first; if the file-manager request comes back from a real customer, revisit.
- **Cluster appliance installer** (ClusterVisor's `cv-installer`). Our install is `dnf install clustr-server && clustr-serverd bootstrap-admin`. That is simpler by design. The "appliance template" pattern is a relic of ClusterVisor's plugin sprawl.
- **Plugin discovery UI** (UP11). We're a Go monolith. We ship the plugins we ship. A Plugins page that lists "the plugins you cannot install" is operator-confusing and adds no value. Defer indefinitely.
- **Multiple dashboards / per-role dashboards / dashboard widgets / dashboard CRUD** (UP2, large parts of Cockpit). Dashboards are the single most-requested feature that almost nobody actually uses past the first week. Ship the per-node Sensors panels (UP1) and the operator's daily questions are answered without a top-level dashboard. Add a single fixed "Cluster Overview" page only if a customer asks for one.
- **Color picker / per-widget theming.** No.
- **In-app software upgrade** (Cockpit's "ClusterVisor Software Upgrade"). Operators run `dnf upgrade clustr-server`. Doing this from the running daemon is a footgun (the binary serving you the upgrade UI is the binary being replaced). Reject.
- **"Send Logs" diagnostic bundler.** Useful pattern; we don't run a support funnel. If we ever need it, it's a 200-line CLI command. Defer.
- **Computed stats / expression-builder UI / heatmap widgets / SVG export.** Late-game polish. Reject for now; revisit only after Sprint 25 ships.
- **Schema versioning per artifact (ClusterVisor's 1.0 / 1.1).** Our forward-only SQL migrations + `pkg/api` types do the same thing with less ceremony. Take the OpenAPI export from P6; skip the artifact schema versioning.

### Tabled (real but low priority)

- **Synced-file management UI** (Cockpit's "Synced File Settings"). Manages cluster-wide synced files like `/etc/hosts`. clustr already does the equivalent through `internal/sysaccounts/` reconcile + the in-chroot reconfigure pass (CG12). Adding a UI is a follow-up after CG12 lands.
- **Email template editor in UI.** SMTP config exists. Templates are at `internal/notifications/templates/`. Edit-in-UI is nice; not urgent.
- **In-DB notices for upgrades.** ClusterVisor's `notices.yml`. Useful when we have multi-version migration paths people might miss. Add when we have a customer with an upgrade horror story to motivate the format.
- **Per-image multicast scheduler interface** (operator UI for batched reimage windows). Lands when CG1 multicast lands; defer the UI to Sprint 25's UP track.

---

## Constraint-conflict checklist

I scanned IMPROVEMENTS for any proposal that conflicts with the standing architectural commitments, and confirmed each below.

| Standing constraint | IMPROVEMENTS proposal | Conflict? | Disposition |
|---|---|---|---|
| No containers (RPM + source only) | Reproducible initramfs builder via Docker | Build-time only is fine; runtime Docker dependency not. | Use Path A as build-time only. |
| `clustr-privhelper` is the single privilege boundary | Stats agent runs on nodes | No conflict — `clustr-clientd` already does. Fold collector in. | Accept. |
| `clustr-privhelper` is the single privilege boundary | BIOS settings push needs root on node | Use clientd → privhelper for the vendor binary invocation. | Accept; route through privhelper. |
| Web auth = local users table only | "LDAP Settings" / "LDAP browser" Cockpit-style | Browser is fine as read-only DIT explorer. Auth must not bind to LDAP. | Accept browser, reject auth-via-LDAP. |
| Wipe-by-default lifecycle | Two-stage commit pending changes | Orthogonal — pending changes don't change the wipe semantics of Disable/Stop/Reset. | Accept. |
| POSIX UID range split (system 200-999, user 10000-60000) | New posixid users for stats agent | Not implied — collector runs as root via existing clientd. | No new accounts. |
| clustr replaces OpenHPC for slurm distribution | `clustr scheduler` jobs view | Read-only against `slurmrestd`; doesn't change slurm distribution path. | Accept. |
| No HN/launch/outreach framing | IMPROVEMENTS itself frames things as "displace clustervisor in the field" | Engineering-only framing in this doc. | Reframed throughout. |
| Wiped scope stays wiped | (No proposal restoring deleted scope) | None found. | OK. |
| No headcount/revenue gating | IMPROVEMENTS's 6-month roadmap is paced on team-size assumptions | Repaced on architectural readiness, not staffing. | Repaced below. |
| Build artifacts never committed to git | Stats agent ships per-plugin metric definitions | Generated metric registry — don't commit. | Note in scope. |

No proposed work in IMPROVEMENTS structurally violates the constraints in a way that survives reframing. The risky proposals (LDAP auth, Cockpit, Docker runtime, license enforcement) are rejected outright above.

---

## Sprints — proposed sequencing

Each sprint has a single coherent theme and a small enough surface to ship in a 2-week window with one engineer driving (Dinesh) and Richard answering questions on demand. Task IDs continue from #119.

---

### Sprint 20 — Real-hardware initramfs (HIGH) — "stop failing PXE"

**Theme:** clustr currently cannot PXE-boot real Mellanox / Intel / Broadcom / megaraid hardware. Fix that before anything else.

**Why this sprint exists:** Today's `scripts/initramfs-init.sh` insmod's a virtio-only module list. The first time a real customer plugs a Mellanox CX-5 NIC into a clustr-managed cluster the deploy fails at "no usable NIC found." We have no other gap that prevents adoption this hard. Sprint 20 fixes that and a small cluster of related deploy-correctness bugs.

**Scope:**

- **#120 — Real-hardware kernel module set in initramfs (HIGH, M)**
  Owner: Dinesh; arch by Richard.
  In: expand the `scripts/build-initramfs.sh` allowlist to cover `mlx5_core/mlx5_ib/mlx4_*`, `i40e/ice/ixgbe/igb/e1000e`, `bnxt_en/bnx2x/tg3`, `nvme/nvme_core`, `megaraid_sas/mpt3sas/aacraid`, `dm_*`, `btrfs`, `nvme`, `crc32c_generic`. Build-time enumerate from `/lib/modules/$KVER/kernel/{net,drivers/net,drivers/scsi,drivers/block,drivers/md,drivers/nvme,fs}` and write a manifest of included modules into the build artifact for debug. Out: alternative driver runtime auto-load via lspci aliases (deferred as a follow-up if the explicit list proves fragile).
  Depends on: nothing.

- **#121 — xattr / ACL preservation in deploy (HIGH, S)**
  Owner: Dinesh; arch by Richard.
  In: replace busybox `tar` with GNU `tar` in initramfs binary list. Add `--xattrs --xattrs-include='*' --acls` to the streamExtract invocation in `internal/deploy/rsync.go`. Add a deploy-round-trip test that captures an image with SELinux contexts + POSIX ACLs and asserts they survive capture → deploy → re-capture. Out: ACL editor UI.
  Depends on: nothing.

- **#122 — HTTP `Range:` resume on blob retries (MEDIUM, S)**
  Owner: Dinesh.
  In: track bytes-received per attempt in the rsync.go retry loop; on retry send `Range: bytes=N-` and resume the stream. Verify Go's `net/http.ServeContent` end-to-end. Cap total retry duration at `CLUSTR_DEPLOY_TIMEOUT`. Out: parallel-range streams (no need yet).
  Depends on: nothing.

- **#123 — Bandwidth and concurrency caps on `/blob` (MEDIUM, S)**
  Owner: Dinesh.
  In: `CLUSTR_BLOB_MAX_BPS` (per-stream), `CLUSTR_BLOB_MAX_CONCURRENCY` (global) env vars. Token-bucket middleware in `internal/server/handlers/images.go`. Out: per-image / per-group quota policies.
  Depends on: nothing.

- **#124 — Lab-validate Sprint 20 on real Mellanox + megaraid hardware (HIGH, S)**
  Owner: Gilfoyle (or Jared if Gilfoyle is unavailable).
  In: confirm a node with a Mellanox CX-5 + an LSI 9361-8i actually PXE-boots, partitions, deploys an image, and rejoins the cluster end-to-end. Document exact module load order observed. Out: full lab-validate matrix (separate sprint task).
  Depends on: #120 #121 #122.

**Estimated complexity:** S–M tasks; sprint total **M**. Sprint 20 is small on purpose — it's the gating dependency for Sprints 22 and 25.

---

### Sprint 21 — Operator parallel ops, part 1 (HIGH) — "exec and console"

**Theme:** Bring the day-2 ops surface that defines whether clustr feels like a cluster manager or a provisioner.

**Why this sprint exists:** Without `clustr exec` and `clustr console`, every operator who tries clustr concludes it's "just provisioning." This is the single biggest perceived-ceiling gap.

**Scope:**

- **#125 — Selector grammar and routing (HIGH, M)**
  Owner: Richard scopes (1 day) → Dinesh implements.
  In: `internal/selector/` package with one `Resolve(*SelectorSet) []NodeID`. Selector flags: `-n NODE` (hostlist syntax — `node[01-32]`), `-g GROUP`, `--racks RACK`, `--chassis CHASSIS`, `-A` (all), `-a` (active), `--ignore-status`. Cobra-side flag wiring shared by every batch command. Out: per-rack / per-chassis resolution against the rack model — racks/chassis return empty until #137 lands; falls back gracefully.
  Depends on: nothing.

- **#126 — `clustr exec` over clientd WebSocket (HIGH, L)**
  Owner: Dinesh; arch by Richard.
  In: new server endpoint `POST /api/v1/exec` (SSE-streamed, one stream per target). Reuse the existing `clustr-clientd` WebSocket as the transport — no new SSH/port management on the operator workstation. Output formats: `inline`, `header`, `consolidate`, `realtime`, `json` (match cv-exec exactly). CLI wires through the selector grammar. Out: file-streaming (#127), pty/tty exec (#128).
  Depends on: #125.

- **#127 — `clustr cp` (HIGH, M)**
  Owner: Dinesh.
  In: `POST /api/v1/cp` server-side. Recursive, `--preserve` (mode/owner/timestamps), `--include-self`, parallel. Reuse the same clientd WebSocket transport. Out: rsync-style delta (one-shot push only in v1).
  Depends on: #125 #126 (transport pattern).

- **#128 — `clustr console --ipmi-sol` and `--ssh` (HIGH, M)**
  Owner: Dinesh; arch by Richard for the SOL broker.
  In: `WS /api/v1/console/{node_id}` brokered server-side. IPMI SOL via `ipmitool sol activate`; SSH PTY as fallback. Escape character `~.` (default), configurable via `-e`. Generalise the pattern from `internal/image/shell.go`. Out: in-browser console (UP3 lands later in Sprint 24).
  Depends on: #125. Independent of #126/#127.

- **#129 — `clustr ipmi sel {list,clear,head,tail,filter --level critical}` (MEDIUM, S)**
  Owner: Dinesh.
  In: CLI wires to existing IPMI surface; add filter / head / tail / level on top.
  Depends on: nothing (the underlying IPMI client exists).

- **#130 — `clustr health [--summary|--ping|--wait]` (MEDIUM, S)**
  Owner: Dinesh.
  In: aggregate per-node health summary endpoint + CLI. `--wait` polls until all targets are reachable or timeout.
  Depends on: nothing.

**Estimated complexity:** Sprint 21 total **L**. The selector grammar and `exec` are the irreversibles; consoles and `cp` are layered on top.

---

### Sprint 22 — Stats + alerts (HIGH) — "answer 'is anything broken right now?'"

**Theme:** On-node collectors + a small alert engine. From this sprint forward we can answer "which nodes have a degraded RAID array" without external tooling.

**Why this sprint exists:** ClusterVisor's `cv-stats` + `cv-alerts` is a complete self-contained answer to the operator's daily questions. clustr currently exposes Prometheus `/metrics` server-side only, with no on-node collectors and no alerting. This is P1 in IMPROVEMENTS and the second-largest functional gap.

**Scope:**

- **#131 — Stats collector folded into `clustr-clientd` (HIGH, L)**
  Owner: Dinesh; arch by Richard.
  In: new `internal/clientd/stats/` subpackage with plugin pattern (`Plugin` interface: `Name()`, `Collect(ctx) []Sample`). Plugin set v1: `cpu`, `memory`, `disks`, `md`, `net`, `system`, `nvme`, `infiniband` (link state + counters via `ibstat`), `firmware`. Push samples over the existing clientd WebSocket. New server table `node_stats(node_id, plugin, sensor, value, ts)` + retention sweeper (default 7d). New endpoint `GET /api/v1/nodes/{id}/stats?plugin=&since=`. Prometheus exposition extended to include per-node series. Out: GPU plugins (#132).
  Depends on: #120 (real-hardware initramfs — same kernel module assumptions on running nodes).

- **#132 — GPU + RAID + ZFS + NTP plugins (HIGH, M)**
  Owner: Dinesh.
  In: `nvidia` (via `nvidia-smi -q -x`), `megaraid` (via `storcli`), `zfs` (`zpool status -p`), `ntp` (`chronyc tracking`). Plugins are optional — if the binary isn't present, the plugin reports "not configured" cleanly and stays out of the metric series. Out: rocm (defer until a customer with AMD GPUs).
  Depends on: #131.

- **#133 — Alert rule engine + YAML rules (HIGH, M)**
  Owner: Dinesh; arch by Richard.
  In: `internal/alerts/` package. YAML rules under `/etc/clustr/rules.d/*.yml` (mode 0640 root:clustr). Default rule set shipped: `disk-percent`, `infiniband-down`, `hw-raid-degraded`, `sw-raid-degraded`, `zpool-degraded`, `cluster-mces-errors`, `cluster-nodes-offline`, `appliance-diskspacelow`. Rules evaluate on a 60s tick. Routes through the existing webhook dispatcher and SMTP notifier — no new delivery infrastructure. New endpoint `GET /api/v1/alerts` (active + history). Out: silence-with-expiry (#143 in Sprint 24 UI).
  Depends on: #131.

- **#134 — `clustr alerts` + `clustr stats` CLI (MEDIUM, S)**
  Owner: Dinesh.
  In: `clustr alerts -L | -S | -R` and `clustr stats -n NODE -s REGEX` shapes match cv-alerts / cv-stats. Out: silence/ack flow (waits on UI in Sprint 24).
  Depends on: #131 #133.

**Estimated complexity:** Sprint 22 total **L**. The hardest piece is the per-plugin metric registry — once it's right, adding plugins is mechanical.

---

### Sprint 23 — Imaging maturity (MEDIUM) — "disk layouts, instructions, in-chroot reconfigure"

**Theme:** Image-system features that operators expect once they've stopped fighting PXE.

**Why this sprint exists:** With Sprint 20 the deploy works on real hardware. Sprint 23 is the "now what" follow-up: separate disk layouts as a first-class object, give operators a small DSL to customise images without rebuilding them, and close the "first boot is useless for 30s-3m" window via in-chroot reconfigure.

**Scope:**

- **#135 — Disk layout as a first-class object (MEDIUM, M)**
  Owner: Richard scopes → Dinesh implements.
  In: new migration `disk_layouts(id, name, source_node_id, captured_at, layout_json)`, endpoints `POST /api/v1/disk-layouts/capture/{node_id}`, `GET /api/v1/disk-layouts`, `PUT /api/v1/disk-layouts/{id}`. New foreign keys `node_groups.disk_layout_id` (default for the group) and per-node override on `nodes.disk_layout_id`. Deploy precedence: explicit layout > group default > recommendation. Out: layout DSL editor (operators paste layout JSON in v1).
  Depends on: nothing.

- **#136 — Per-image install instructions DSL (MEDIUM, S)**
  Owner: Dinesh.
  In: extend `BaseImage` with `install_instructions []InstallInstruction`. `InstallInstruction = { opcode: "modify"|"overwrite"|"script", target: string, payload: string }`. Deploy agent runs them in order inside the chroot after extract, before bootloader install. UI: "Install Instructions" tab on the image edit drawer. Out: opcode beyond the three.
  Depends on: #135 (deploy plan touches both).

- **#137 — In-chroot reconfigure pass (MEDIUM, M)**
  Owner: Dinesh.
  In: factor `internal/clientd/configapply.go` so the file-writing logic targets an arbitrary root. New deploy phase `inChrootReconfigure` against `/mnt/target` before unmount. Closes the "online but useless" first-boot window. Out: full plugin parity with cv-reconfigure (we apply only what clientd already applies).
  Depends on: nothing.

- **#138 — Rack model + `--racks` selector wiring (LOW, S)**
  Owner: Dinesh.
  In: migrations `racks(id, name, height_u)` and `node_rack_position(node_id, rack_id, slot_u, height_u)`. Selector grammar (#125) starts resolving `--racks` against the model. Out: rack diagram UI (lands in Sprint 24).
  Depends on: nothing (UI lands later but the data model is needed earlier).

- **#139 — IMSM / hardware RAID passthrough (LOW, S)**
  Owner: Dinesh.
  In: `mdadm --imsm-platform-test` detection in `internal/deploy/raid.go`; branch the assembly path. Add a qemu-IMSM emulated test. Out: full vendor RAID coverage (megaraid CLI is bundled but not the deploy path in v1).
  Depends on: #120 (initramfs has mdadm with IMSM support after the module work).

**Estimated complexity:** Sprint 23 total **M**. Bigger than Sprint 20, smaller than Sprint 22.

---

### Sprint 24 — Web UI catch-up (MEDIUM) — "expose what we've built"

**Theme:** Surface the Sprint 21–23 backends in the React SPA. Almost entirely frontend work.

**Why this sprint exists:** Sprints 21–23 add a lot of new server-side capability. Without UI surfacing, operators learn it through CLI only and the discovery curve gets worse, not better.

**Scope:**

- **#140 — Per-node Sensors + Event Log + Console tabs (MEDIUM, M)**
  Owner: Dinesh.
  In: three new tabs on the node Sheet. Sensors uses `recharts` (~120 KB) for live values + thresholds. Event Log is a virtualized list with level filter + regex + head/tail (lands on the #129 backend). Console embeds `xterm.js` connected to the #128 SOL broker.
  Depends on: #128 #129 #131.

- **#141 — Slurm Jobs / Partitions UI (MEDIUM, M)**
  Owner: Dinesh.
  In: new tab on the existing `/slurm` route. Pull from `slurmrestd`. Tables only in v1. Reuse TanStack Query + the existing SSE hookup. Out: charts (no charts in v1).
  Depends on: nothing (slurmrestd is already a dependency).

- **#142 — Two-stage commit backend + drawer UI (MEDIUM, M)**
  Owner: Dinesh; arch by Richard.
  In: `pending_changes(id, kind, target, payload, created_by, created_at)` table. Endpoints `POST /api/v1/changes`, `GET /api/v1/changes`, `POST /api/v1/changes/commit`, `POST /api/v1/changes/clear`. Wire LDAP / sudoers / network handlers to *also* offer "stage" mode in addition to immediate-apply. UI: Pending Changes badge + drawer in AppShell with diff per change. Default off — preserves current behaviour. Operators opt in per surface in Settings.
  Depends on: nothing.

- **#143 — Alerts UI (MEDIUM, M)**
  Owner: Dinesh.
  In: new top-level `/alerts` route. Active / Silenced / History tabs. Per-rule drill-down. In-line "Silence for 1h / 4h / 24h / forever". YAML rule editor uses `react-simple-code-editor` + `prismjs` (small) — not Monaco. Out: rule diff/preview on save (defer).
  Depends on: #133.

- **#144 — Rack diagram (LOW, M)**
  Owner: Dinesh.
  In: new `/datacenter` route. Single SVG rack component, drag-and-drop U-positioning, bulk power per rack. Reads from #138's `racks` + `node_rack_position` tables.
  Depends on: #138.

**Estimated complexity:** Sprint 24 total **L**. Mostly mechanical frontend; the backends already exist.

---

### Sprint 25 — Imaging breadth (MEDIUM) — "multicast, distros, BIOS, API spec"

**Theme:** Larger-scope imaging features and the API hygiene work that's been accumulating.

**Why this sprint exists:** The remaining accepted IMPROVEMENTS items are bigger surface-area changes — UDPCast, Ubuntu distro driver, BIOS settings push, OpenAPI spec — that earn their place in a single sprint where all four can be designed, scoped, and shipped together. Each is independently 1–2 weeks; bundling reduces the total integration cost.

**Scope:**

- **#145 — UDPCast multicast for fleet reimage (HIGH, L)**
  Owner: Richard scopes (2 days) → Dinesh implements.
  In: ship `udpcast` (sender + receiver) in initramfs. Server-side `internal/multicast/` scheduler with `multicast_sessions` table and a 60s batching window. New CLI flag `clustr deploy --multicast=auto|off|require`. Add an iPXE menu item for "Reimage (wait for fleet)" vs "Reimage (now)". Out: per-image bandwidth shaping (the multicast rate is a single global setting in v1).
  Depends on: #120 (initramfs builder must accommodate larger binaries).

- **#146 — `DistroDriver` interface + Ubuntu driver (MEDIUM, M)**
  Owner: Dinesh.
  In: refactor `internal/deploy/finalize.go` around `DistroDriver { WriteSystemFiles(...), InstallBootloader(...) }`. Implement drivers `el8`, `el9`, `el10`, `ubuntu24`. Plumb through the image factory so it knows which driver applies. Out: Debian, SLES.
  Depends on: nothing.

- **#147 — BIOS settings push (Intel first) (MEDIUM, L)**
  Owner: Richard scopes → Dinesh implements.
  In: `internal/bios/` with `Provider` interface and `intel` provider wrapping `intel-syscfg`. New tables `bios_profiles(id, name, vendor, settings_json)` and `node_bios_profile(node_id, profile_id, last_applied_at)`. Deploy phase reads desired profile, diffs against current, applies via clientd → privhelper-brokered `intel-syscfg` call. Bundle Intel binary in initramfs. Out: Dell `racadm` and Supermicro `sum` providers (separate task once Intel is shipped).
  Depends on: #120 (initramfs binary footprint).

- **#148 — Per-image stateless / netboot menu entries (LOW, S)**
  Owner: Dinesh.
  In: `boot_entries(id, name, kind, kernel_url, initrd_url, cmdline, enabled)` table. Render extra menu items into the iPXE menu at PXE-serve time. UI: "Boot Menu" tab on Settings. Stock entries: Memtest, a Rescue boot (busybox + dropbear with operator-supplied password). Out: full stateless image lifecycle (the image kind itself is deferred to a later sprint — see "wiped scope stays wiped", we don't restore Sprint-2-era diskless work without explicit founder go-ahead).
  Depends on: #120.

- **#149 — `Api-Version: v1` + JSON schema + OpenAPI 3.1 export (LOW, M)**
  Owner: Dinesh.
  In: `Api-Version: v1` response header on every API request. Generate JSON schemas from `pkg/api` types via `github.com/invopop/jsonschema`. Ship them under `/usr/share/clustr/schema/v1/` and serve at `/api/v1/schemas/`. OpenAPI 3.1 spec at `/api/v1/openapi.json`. Out: `/api/v2/...` parallel routes (no v2 yet — this is the work that *makes* a future v2 cheap).
  Depends on: nothing.

- **#150 — Reproducible initramfs builder, Path A (LOW, M)**
  Owner: Gilfoyle.
  In: replace SSH-pull from 192.168.1.151 with a Make-driven local kernel-module extraction step. Ship a build-time builder image (CI uses it; not a runtime dep). Bit-identical output across builds. Out: Buildroot migration (Path B) — defer.
  Depends on: #120 (the module set settles first).

**Estimated complexity:** Sprint 25 total **XL**. The largest sprint by scope. Acceptable because every task is well-bounded; if any one slips, the others ship independently.

---

## Backlog (post-Sprint 25)

These are accepted-with-reservation items that don't fit cleanly into Sprints 20-25 but should be tracked.

- **#151** — `clustr user` / `clustr group` / `clustr sshkey` CLI commands (Sprint 26 candidate). Owner Dinesh. S each, total M. Wait until `clustr exec` is stable so the selector grammar is settled.
- **#152** — Save-as-view / persistent filters (Sprint 26 candidate). Owner Dinesh. M.
- **#153** — Distro catalog + image library polish in UI. M.
- **#154** — Per-rack / per-chassis bulk power UI (relies on #138/#144 landing). S.
- **#155** — USB / offline installer (CG4). L. Gate on a real customer asking.
- **#156** — Buildroot-based reproducible initramfs (CG6 Path B). XL. Gate on Path A turning out insufficient.
- **#157** — Dell `racadm` BIOS provider. M. Follows #147.
- **#158** — Supermicro `sum` BIOS provider. M. Follows #147.
- **#159** — NFS server templating, DNS authoritative, switch discovery UI. S each. Bundle into one sprint when an operator asks.
- **#160** — Notification drawer + saved views (UP10). M.
- **#161** — Synced-file management UI. S. Lands on top of #137.

---

## Risks and irreversibility flags

The Type-1 (irreversible) decisions in this plan, called out so we get them right the first time:

1. **Selector grammar (#125).** Once shipped, every batch CLI command and every server endpoint that takes a target uses this grammar. Wrong here is expensive. Mitigation: Richard scopes for a full day before Dinesh starts, and we explicitly mirror cv-exec's flag set so operator muscle memory transfers.
2. **`node_stats` table schema (#131).** Time-series data — we don't want to migrate this once it has a year of history in it. Mitigation: pick the schema before any plugin lands; `(node_id, plugin, sensor, value, ts)` with a separate retention sweeper is the bet. Don't index more than `(node_id, ts)` and `(plugin, sensor, ts)` in v1.
3. **`disk_layouts` as first-class object (#135).** Operator workflows will start to depend on layout names. Renaming or restructuring later costs a migration. Mitigation: ship with `name` as the stable identifier from day one and never expose `id` in CLI output.
4. **Two-stage commit semantics (#142).** "Stage and apply" is a contract operators will rely on for change-control. Subsequently changing what "commit" means atomically is hard. Mitigation: scope to LDAP / sudoers / network only in v1; a future fourth surface joins the same commit barrier rather than getting its own.
5. **API versioning (#149).** Once we publish v1 OpenAPI, breaking changes need a v2. Mitigation: audit `pkg/api` for embedded types before the export goes out; ship the v1 spec deliberately, not as an automated dump of whatever is currently in the tree.

The Type-2 decisions (everything else) we treat as reversible: ship, learn, iterate.

---

## What this plan deliberately doesn't do

- It doesn't propose a top-level Dashboard. Per-node Sensors panels (#140) answer the operator's daily questions; a multi-widget dashboard is a feature that almost nobody uses past the first week. Defer until a customer asks.
- It doesn't propose KVM-over-IP / noVNC / SPICE / RDP. SOL is the 95% case in HPC.
- It doesn't propose Cockpit integration, in-app upgrades, or a Send-Logs support funnel. We are not running ClusterVisor's support model.
- It doesn't propose restoring stateless / diskless image support. That scope was previously deferred and stays deferred until a customer asks for it explicitly. (#148 ships the iPXE *menu entries* for stateless images, not the image lifecycle itself.)

---

## Recommendation

Approve Sprints 20 and 21 in full. Approve Sprint 22 contingent on the stats schema in #131 being reviewed by Richard before any plugin ships. Hold Sprints 23–25 for re-review after Sprint 22 lands so we can adjust scope based on what we learned.

If the founder wants only one sprint approved before reviewing the rest, it's Sprint 20. Real hardware adoption is gated on it, and every subsequent sprint inherits its initramfs work.
