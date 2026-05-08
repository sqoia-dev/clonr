# clustr — Forward Sprint Plan

**Last updated:** 2026-05-08

This document captures the working sprint plan for clustr after the 2026-05-07/08 release train (v0.1.10 → v0.1.22) and the 2026-05-08 competitor (clustervisor) review. Sprints are sized for parallel execution across Richard, Dinesh, and Gilfoyle.

This is a planning artifact, not a contract. Reorder, drop, or merge sprints as customer reality dictates. Update statuses inline as work lands.

---

## How this plan was built

Two inputs:

1. **Real bugs found shipping today** — over 12 patch releases (v0.1.10 → v0.1.21) the deploy pipeline went through layered failures: orphaned FK rows, blob path resolution, BIOS bootloader stale signatures, the served-initramfs file-name bug, UI staleness, chroot DNS injection, sssd config gaps. Several of those bugs were invisible until the *next* one was uncovered. Sprint 33 specifically targets the observability + portability gaps that would have surfaced those bugs in one pass instead of twelve.

2. **Architectural review of clustervisor** (a Python+Perl competitor in the same problem space). Two Richard dispatches: a broad architecture review and a focused imaging-pipeline deep-dive. The most adoptable patterns are surfaced below; anti-patterns we explicitly reject are at the bottom.

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

## Sprint 33 — Deploy observability + portability

**Theme:** kill the bug class that ate today.
**Owner:** Richard (deploy + serverd), Dinesh (UI for log streaming).

| ID | Item | Effort | Touches |
|---|---|---|---|
| `STREAM-LOG` | Streaming server-side install log. zerolog hook in embedded `clustr` binary POSTs every line to `POST /api/v1/nodes/{id}/install-log` with current phase. Web UI Nodes detail tab streams the log via SSE. | 1.5d | new `internal/client/loghook.go`, `internal/server/handlers/install_log.go`, wire into `cmd/clustr/main.go` |
| `DRACUT-REGEN` | In-chroot `dracut --regenerate-all -fv -N --lvmconf --force-add=mdraid --force-add=lvm` per-kver in `/boot` after `InstallBootloader`. Kills "captured on virtio, deploys to Dell PERC, won't boot". | 1d | `internal/deploy/finalize.go`, new `internal/deploy/regen_initramfs.go` |
| `MULTICAST-JITTER` | Random 0-60s sleep in agent before posting `/deploy-complete` after multicast finishes. Prevents 256-node thundering herd on serverd. | 5min | `cmd/clustr/main.go:runAutoDeployMode` |
| `PRE-ZERO` | `dd if=/dev/zero of=$disk bs=1M count=10` before `wipefs` in `diskWipeSequence`. Belt-and-braces for any boot-sector / GRUB stage 1 stowaways `wipefs` misses. | 5min | `internal/deploy/rsync.go:1199` |

**Source:** clustervisor `ClonerInstall.pm` runs `status_print_log("INFO", msg)` that double-writes to local console AND to `client->log()`; their cloner regenerates initrd in chroot for every kernel via `_create_system_files_el` and uses `dd ... count=10` before partitioning.

**Suggested cut:** v0.1.23 (or split into v0.1.23 / v0.1.24).

---

## Sprint 34 — BootOrder + IPMI minimal

**Theme:** HPC table stakes.
**Owner:** Richard (deploy + ipmi), Gilfoyle (BMC ops).

| ID | Item | Effort |
|---|---|---|
| `BOOT-POLICY` | Explicit `BootOrderPolicy` field on `NodeConfig` (`network`/`os`). Set at deploy time via `efibootmgr --bootorder`, replacing the reactive `RepairBootOrderForReimage` (#225). | 0.5d |
| `IPMI-MIN` | `clustr ipmi <node> {power,sel,sensors}` via freeipmi, federated through serverd. New `internal/ipmi/` + admin endpoint per BMC. | 2d |
| `BMC-IN-DEPLOY` | Idempotent BMC IP/user/channel reset from `disk_layout` schema, runs in initramfs each deploy. New `internal/deploy/bmc.go`. | 2d |
| `HOSTLIST` | pyhostlist-style range syntax (`node[01-12,20-25]`) across CLI parser, API list endpoints, web UI selectors. Use `github.com/segmentio/go-hostlist` or vendored equivalent. | 1d |

**Source:** clustervisor `cv_ipmi.py` (1240 lines) + `EFIBootmgr.pm:214 set_order`, `pyhostlist` everywhere in cluster config.

---

## Sprint 35 — Disk / storage breadth (closes #255)

**Theme:** firmware-aware layout + hardware RAID variants.
**Owner:** Richard (backend), Dinesh (UI).

| ID | Item | Effort |
|---|---|---|
| `UEFI-LAYOUT` (#255) | Firmware-aware `disk_layout` selection. vm202 currently gets BIOS layout under UEFI — needs ESP partition + `esp` flag when `detected_firmware=uefi`. | 1d |
| `UEFI-WEBAPP` (#255) | Disk layout selection / editing UI. Currently no operator path to pick or customize layouts before deploy. | 2d |
| `IMSM` | Intel IMSM hardware RAID containers + sub-arrays. Two-pass mdadm (`imsm-container` then `imsm-dev`). | 3d |
| `LVM-DEPLOY` | LVM in `disk_layout`. pvcreate / vgcreate / lvcreate from layout. Defer unless customer asks. | 4d |

**Source:** clustervisor `disk_raid_imsm` (`ClonerInstall.pm:589`), `disk_lvm` (line 808).

---

## Sprint 36 — Reactive config model

**Theme:** the single biggest competitor steal. Changes the deploy mental model.
**Owner:** Richard. Needs design doc first.

| ID | Item | Effort |
|---|---|---|
| `CONFIG-OBSERVE` | Plugin-trigger config-key observer pattern. Each config plugin declares which keys it cares about; reconfigure engine diffs config, matches changed keys against plugin triggers, re-renders + pushes only affected plugins. Goal: operator edits one IP, only those plugins push. Start with exact-path subscriptions, not regex (Go-ergonomic). | 5d |
| `ANCHORS` | Anchors-based partial-file edits. Plugin owns a region of `/etc/security/limits.conf` (begin/end markers), not the whole file. Solves multi-plugin file collisions. | 1d |

**Source:** clustervisor `@settings(global_observe=...)` decorator + `cv_reconfigure.py` instruction processor; their `anchors` field on file-write instructions.

**Why it matters:** today we deploy imperatively (`clustr deploy <node>`) — operators run "deploy --all" after every edit just in case. The reactive model converges automatically. Long-term correct answer.

---

## Sprint 37 — Stateless / diskless boot mode

**Theme:** product axis, v0.2 candidate.
**Owner:** Richard.

| ID | Item | Effort |
|---|---|---|
| `DISKLESS` | Boot kernel + initrd from PXE, NFS or RAM-load rootfs, no disk install. Image updates roll cluster-wide via initrd swap. Doesn't replace block-clone — operating mode flag on node. | 7d |

**Source:** clustervisor `cv_diskless.py` + `client/tasks/download-stateless-image.py`.

**Commercial gap:** Warewulf / OpenHPC default-stateless shops will not switch to clustr without this. Concrete competitive miss.

---

## Sprint 38 — Stats / telemetry breadth

**Theme:** metric ergonomics + missing collectors.
**Owner:** Richard (stats registry), individual collector authors.

| ID | Item | Effort |
|---|---|---|
| `STAT-REGISTRY` | Typed metric registry: `register("float", "used_memory", device=, unit=, upper=, title=, ddcg="GPU Memory Usage")`. Chart-grouping hint baked into the metric — UI consumes it directly, no separate dashboard config. | 2d |
| `IB-PLUGIN` | InfiniBand stats collector reading `/sys/class/infiniband` (port state, counters, link rate). | 0.5d |
| `MEGARAID-PLUGIN` | LSI MegaRAID controller stats. | 1d |
| `INTELSSD-PLUGIN` | Intel enterprise SSD SMART (different from generic SMART). | 0.5d |

**Source:** clustervisor `common/stat_plugins/` + `register("float", ..., ddcg=...)` pattern.

---

## Sprint 39 — Slurm day-2 integration

**Theme:** clustr currently pushes slurm config but doesn't talk to slurmctld for live state.
**Owner:** Richard.

| ID | Item | Effort |
|---|---|---|
| `SLURM-REST` | Talk to `slurmctld` via REST API + JWT for live state (jobs, partitions, node down/drain). Read-side first; write-side later. | 2d |
| `SLURM-ACCOUNTS` | LDAP user → `slurmdbd` association sync. Currently a manual operator step. | 3d |
| `BUNDLES-DAYTWO` | Bundles tab shows live state of deployed slurm bundles (which nodes have which bundle, drift, in-progress upgrades). | 1d |

**Source:** clustervisor `slurm_server_legacy.py` (JWT-signed REST) + `cv_slurm_accounts.py`.

---

## Sprint 40 — Network plane

**Theme:** own the cluster's network identity, not just compute.
**Owner:** Gilfoyle (operational), Richard (collector contracts).

| ID | Item | Effort |
|---|---|---|
| `DHCP-DNS` | Own dnsmasq config from cluster brain. Today clustr expects external DHCP/DNS. Becomes optional plugin. | 2d |
| `SNMP-COMPOSER` | Switch / PDU / UPS YAML schemas + SNMP collector (`cv_snmp.py` equivalent). Per-vendor schema files. | 3d |

**Source:** clustervisor `dhcp_dns_server.py` + `cv_snmp.py`.

---

## Sprint 41 — Auth + safety hardening

**Theme:** opt-in security primitives + plugin guardrails.
**Owner:** Richard.

| ID | Item | Effort |
|---|---|---|
| `MUNGE-OPT` | Opt-in munge transport when slurm is detected. Reuses slurm's munge key for server↔node auth — no new clustr-specific keypair to manage. Standalone clustr (no slurm) keeps API key transport. | 2d |
| `DANGEROUS-DECORATOR` | `IsDangerous(reason)` on plugins / install_instruction steps — requires `--force` to apply. Safety rail for destructive ops (e.g., wipes `/etc/passwd`). | 0.5d |
| `PLUGIN-PRIORITY` | `Priority(N)` on plugins / install_instructions. Solves "selinux must be configured before sshd restart". | 0.5d |
| `PLUGIN-BACKUPS` | Per-plugin file backups with retention `plugin_backups=N`. Every modified file gets a snapshot, last N kept. Cheap rollback. | 1d |

**Source:** clustervisor `pymunge` + plugin decorators (`@is_dangerous`, `set_priority`, `plugin_backups`).

---

## Sprint 42 — Migration + schema discipline

**Theme:** make schema changes safer + audit log debug-friendlier.
**Owner:** Richard.

| ID | Item | Effort |
|---|---|---|
| `MIGRATE-CHAIN` | `from_version → to_version` lookup map for skip-version upgrades. Currently linear numbered SQL files; add a `lookup.yml` so we can prune intermediate steps. | 0.5d |
| `JSON-SCHEMA` | Per-resource JSON Schema validation at API write boundary, with cross-resource FK rules. Catches "I configured an invalid combo" at write time, not at deploy time on the node. | 2d |
| `EVENT-LOG-JSONL` | JSON-lines sidecar to SQL `audit_log`. `tail -f`-able, replayable for support bundles (`replay(*handlers, after=ts)`). Same data, two consumers. | 0.5d |

**Source:** clustervisor `migrate/migrations/lookup.yml` chain pattern, Cerberus schema validator (`cv_schema.py`), `cv_event_store.py`.

---

## Sprint 43 — v0.2.0 cleanup (non-competitor)

**Theme:** carry-over from existing backlog.

| ID | Item | Effort |
|---|---|---|
| `PI-CODE-WIPE` (#251) | Remove dead PI workflow Go code (`internal/db/pi.go`, `user_group_memberships.go`, `internal/server/handlers/portal/pi.go`, t.Skip()'d tests, `/pi` web routes) + drop now-orphaned tables. Target v0.2.0. | 1d |
| `LAB-BLK-1` (#195) | Clone VMID 303, enroll as compute, finish matrix Rows 2/3/6. | 0.5d |
| `LAB-BLK-2` (#196) | Bake eth0 NetworkManager config + sshd_config persistence into VMID 299 template. | 0.5d |
| `OPS-7` (#214) | Clean up orphaned initramfs build temp dirs + stale `.build-*` artifacts. | 0.5d |
| `TEST-2` (#193) | Web↔CLI auth/error semantics parity contract tests. | 1d |
| `DOC-3` (#194) | Justify panic usage in initramfs handler + schema init (or remove panics). | 0.5d |
| `UX-13` (#198) | api.ts polish (status in non-JSON error + headers typing). | 0.5d |
| `TEST-3` (#199) | Silence jsdom canvas warning in vitest setup. | 0.25d |
| `OPS-4` (#202) | Decide intent of VMID 301/302; move to vmbr10 if needed. | 0.25d |
| `UX-16` (#220) | `clustr group reimage --wait` / SSE progress option. | 1d |
| `UX-17` (#221) | `clustr stats` node resolution uses health endpoint indirectly — fix lookup. | 0.5d |
| `P3` (#226) | Shell WS read deadline + pong handler + max session TTL. | 1d |
| `CI-1` (#227) | Add staticcheck to CI. | 0.5d |
| `BONUS-1` (#228) | Schema responses missing Cache-Control headers. | 0.25d |
| `BONUS-3` (#230) | `wrapNspawnInScope` fallback trap under `NoNewPrivileges=true`. | 0.5d |

---

## Anti-patterns (explicitly reject)

From the clustervisor review — features they have that we deliberately do **not** copy:

- **Two-language toolchain (Python + Perl).** Stay Go-only. Their boundary tax is real.
- **MongoDB primary store.** SQLite is correct for clustr's scale and audit story. Their `tiny_config.py` MockCollection is the cost of Mongo's hostility to schema changes.
- **Decorator-everywhere DSL** (`@settings(...)` with 12 keyword args). Powerful only because of Python dynamism. Use Go interfaces + structs.
- **Bottle as web framework.** Single-threaded, gevent-monkey-patched. Gin (clustr's choice) is strictly better.
- **`distutils.version.LooseVersion`** and similar dead Python stdlib usage. Their codebase shows real bit-rot.
- **IPMI invocation in core server hot path.** clustr-privhelper is the right factoring — keep BMC ops out of the serverd process.
- **Rsync with no Range resume on disconnect.** clustr already does HTTP Range resume (#122) — keep it.
- **Pre-built `initrd-src` checked in as black box.** clustr builds initramfs from scratch deterministically (#162). Keep it.

---

## Things clustr already does meaningfully better

Don't lose these in any future refactor:

- Static Go binary in initrd vs Perl interpreter + ~100 .pm modules
- HTTP Range-resume blob fetch vs plain rsync
- Block-image deploy (bit-perfect golden images) vs filesystem-only
- Reproducible cpio builds (`SOURCE_DATE_EPOCH`) vs non-deterministic
- HTTP token auth (per-node-scoped) vs munge-key-baked-into-initrd
- Verified `BOOTX64.EFI` post-install vs trusting `grub2-install` exit code
- `clustr-verify-boot.service` post-reboot — clustervisor has no phone-home, just trusts deploy worked
- Concurrent partition + blob download — overlaps disk and network I/O
- ~3000 lines of `_test.go` in `internal/deploy/` vs near-zero unit tests

---

## Total raw effort
~52 days of engineering across 11 sprints. With Richard + Dinesh + Gilfoyle in parallel, ~3-4 weeks calendar.

## Suggested execution order

1. **Now:** finish v0.1.22 (in flight)
2. **Next 1-2 days:** Sprint 33 (observability/portability — highest ROI, smallest)
3. **Then by customer pull:** Sprint 34 (HPC table stakes) → Sprint 35 (disk breadth) → Sprint 37 (diskless, the commercial gap)
4. **In parallel:** Sprint 36 (reactive config — needs design doc) staged behind smaller wins
5. **Lower:** Sprints 38–42 are quality-of-life
6. **Cleanup pass:** Sprint 43 before any v0.2.0 cut

Reorder freely as customer reality dictates.
