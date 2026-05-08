# clustr — Forward Sprint Plan

**Last updated:** 2026-05-08

This document captures the working sprint plan for clustr after the 2026-05-07/08 release train (v0.1.10 → v0.1.22) and the 2026-05-08 competitor (clustervisor) reviews. Sprints are sized for parallel execution across Richard, Dinesh, and Gilfoyle.

This is a planning artifact, not a contract. Reorder, drop, or merge sprints as customer reality dictates. Update statuses inline as work lands.

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

## Sprint 34 — BootOrder + IPMI + consoles

**Theme:** HPC table stakes — converged from cloner #6, imaging deep-dive `BMC-IN-DEPLOY`, and cockpit review #1.
**Owner:** Richard (deploy + ipmi), Gilfoyle (BMC ops), Dinesh (console UI).

| ID | Item | Effort |
|---|---|---|
| `BOOT-POLICY` | Explicit `BootOrderPolicy` field on `NodeConfig` (`network`/`os`). Set at deploy time via `efibootmgr --bootorder`, replacing the reactive `RepairBootOrderForReimage` (#225). | 0.5d |
| `IPMI-MIN` | `clustr ipmi <node> {power,sel,sensors}` via freeipmi, federated through serverd. New `internal/ipmi/` + admin endpoint per BMC. | 2d |
| `BMC-IN-DEPLOY` | Idempotent BMC IP/user/channel reset from `disk_layout` schema, runs in initramfs each deploy. New `internal/deploy/bmc.go`. | 2d |
| `SERIAL-CONSOLE-UI` | Web-served IPMI Serial-over-LAN console (terminal in browser). Backend wraps `ipmitool sol`, frontend uses xterm.js + WebSocket bridge. Today clustr only has CLI `clustr console --ipmi-sol`. | 3d |
| `VNC-CONSOLE-UI` | Web-served VNC/iKVM console — cockpit has `VncConsole`, we have nothing. Backend tunnels BMC iKVM (vendor-specific: Dell iDRAC, Supermicro IPMIView, AMI MegaRAC) through a noVNC proxy. | 4d |
| `BOOT-SETTINGS-MODAL` | Per-node "Change boot settings" modal — pin a netboot menu entry per node + kernel cmdline. Clustr today only has one-shot `/power/pxe` `/power/disk`. New persisted columns + iPXE menu integration in `internal/server/handlers/boot.go`. | 2d |
| `HOSTLIST` | pyhostlist-style range syntax (`node[01-12,20-25]`) across CLI parser, API list endpoints, web UI selectors. Use `github.com/segmentio/go-hostlist` or vendored equivalent. | 1d |

**Source:** clustervisor `cv_ipmi.py` (1240 lines) + `EFIBootmgr.pm:214 set_order`, `pyhostlist` everywhere in cluster config, cockpit `SerialConsole` / `VncConsole` PatternFly components.

---

## Sprint 35 — Disk / storage breadth (closes #255)

**Theme:** firmware-aware layout + hardware RAID variants + per-node disk UX.
**Owner:** Richard (backend), Dinesh (UI).

| ID | Item | Effort |
|---|---|---|
| `UEFI-LAYOUT` (#255) | Firmware-aware `disk_layout` selection. vm202 currently gets BIOS layout under UEFI — needs ESP partition + `esp` flag when `detected_firmware=uefi`. | 1d |
| `UEFI-WEBAPP` (#255) | Disk layout selection / editing UI. Currently no operator path to pick or customize layouts before deploy. | 2d |
| `DISK-LAYOUT-PICKER` | Per-node disk layout picker + override surfaced in node detail. Server has `/effective-layout` and `/layout-override` already; UI exposes nothing. Operators can't tell what disk plan a node uses without API. | 1w |
| `DISK-LAYOUT-EDITOR` | Visual disk-layout editor (cockpit has full "Edit disk layout" + "Create from scratch"). Render partition tree, allow drag-resize, validate. Builds on `DISK-LAYOUT-PICKER`. | 2w |
| `DISK-LAYOUT-DUPLICATE` | "Duplicate disk layout" action — clone an existing layout as a starting point for a variant. | 0.5d |
| `IMSM` | Intel IMSM hardware RAID containers + sub-arrays. Two-pass mdadm (`imsm-container` then `imsm-dev`). | 3d |
| `LVM-DEPLOY` | LVM in `disk_layout`. pvcreate / vgcreate / lvcreate from layout. Defer unless customer asks. | 4d |

**Source:** clustervisor `disk_raid_imsm` (`ClonerInstall.pm:589`), `disk_lvm` (line 808), cockpit "Disk Layouts" tab + capture/edit/duplicate actions.

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

## Sprint 38 — Stats / telemetry breadth + agent-less monitoring

**Theme:** metric ergonomics + missing collectors + monitor-without-agent.
**Owner:** Richard (stats registry), individual collector authors.

| ID | Item | Effort |
|---|---|---|
| `PROBE-3` | Three reachability probes — `icmplib.ping`, raw-socket SSH banner read, `ipmi-sensors --no-output ... mc info`. Three booleans per node, no clientd required. ~200 LOC Go. **Smallest first step toward agent-less monitoring** — pure win. | <1d |
| `EXTERNAL-STATS` | Agent-less BMC/IPMI/SNMP probes for unenrolled-or-broken nodes. New `internal/server/stats/external/` goroutine pool; reuse existing `bmc_config_encrypted` for creds; expose as `/api/v1/nodes/{id}/external_stats`. **Closes the "blackout = blind" gap** before, during, and after deploy. | 2 sprints |
| `STAT-EXPIRES` | `expires_at` column on stats writes — auto-stale stats vanish from "current" views. Today clustr treats stats as monotonic; staleness is implicit. | 0.5d |
| `SYSTEM-ALERT-FRAMEWORK` | First-class operator-visible alert with key+device addressing + lifecycle (push/set/unset/expire). clustr today has events, not lifecycled alerts. Adopt their `alert/push|set|unset/<key>/<device>` pattern. | 1d |
| `STAT-REGISTRY` | Typed metric registry: `register("float", "used_memory", device=, unit=, upper=, title=, ddcg="GPU Memory Usage")`. Chart-grouping hint baked into the metric — UI consumes it directly, no separate dashboard config. | 2d |
| `IB-PLUGIN` | InfiniBand stats collector reading `/sys/class/infiniband` (port state, counters, link rate). | 0.5d |
| `MEGARAID-PLUGIN` | LSI MegaRAID controller stats. | 1d |
| `INTELSSD-PLUGIN` | Intel enterprise SSD SMART (different from generic SMART). | 0.5d |

**Source:** clustervisor `common/stat_plugins/` + `register("float", ..., ddcg=...)` pattern; `stats/workers/external_{parent,child}.py` + `stats/workers/plugins/{access,external-bmc,snmp}.py`; `alert/push|set|unset` endpoints in `cv_serverd/api.py`.

**Note:** `EXTERNAL-STATS` should be built as a **goroutine pool inside clustr-serverd** — clustervisor's separate-daemon architecture is a Python GIL constraint we don't import.

---

## Sprint 39 — Slurm day-2 integration

**Theme:** clustr currently pushes slurm config but doesn't talk to slurmctld for live state.
**Owner:** Richard.

| ID | Item | Effort |
|---|---|---|
| `SLURM-REST` | Talk to `slurmctld` via REST API + JWT for live state (jobs, partitions, node down/drain). Read-side first; write-side later. | 2d |
| `SLURM-ACCOUNTS` | LDAP user → `slurmdbd` association sync. Currently a manual operator step. | 3d |
| `SLURM-CHANNEL` | Slurm RPM channel concept — `release` / `testing` / etc. channels for the Bundles model. clustr today has per-tag bundles; channel subscription ("subscribe to slurm-stable") is a natural extension. Mirrors clustervisor's `/slurm/avail` query against `repo.advancedclustering.com/slurm/{channel}/...`. | 2d |
| `BUNDLES-DAYTWO` | Bundles tab shows live state of deployed slurm bundles (which nodes have which bundle, drift, in-progress upgrades). | 1d |
| `SLURM-DRAIN-UI` | Per-node drain/undrain inline action wired to `slurmctld` — visible in node detail panel, bulk-selectable on nodes table. | 1d |

**Source:** clustervisor `slurm_server_legacy.py` (JWT-signed REST) + `cv_slurm_accounts.py` + `/slurm/avail` channel query.

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

**Theme:** opt-in security primitives + plugin guardrails + RBAC.
**Owner:** Richard.

| ID | Item | Effort |
|---|---|---|
| `RBAC-ROLES` | Generalize clustr's local users table to a `roles` table + `role_assignments`. `resolve_roles(uid)` returns `(is_admin, roles, merged_permissions)`. **Group-aware** — match against posix group membership so "anyone in `cluster-ops` group is admin" works without per-user grants. Touches `internal/server/auth/`, login flow, every admin-gated handler. | 1w |
| `JOURNAL-ENDPOINT` | `GET /api/v1/nodes/{id}/journal?unit=X&since=Y` — wraps `journalctl --unit X --since Y` via clustr-clientd. Cockpit users expect this; clustr-serverd has nothing equivalent. Cheap. | 0.5d |
| `DANGEROUS-DECORATOR` | `IsDangerous(reason)` on plugins / install_instruction steps — requires `--force` to apply. Safety rail for destructive ops (e.g., wipes `/etc/passwd`). | 0.5d |
| `PLUGIN-PRIORITY` | `Priority(N)` on plugins / install_instructions. Solves "selinux must be configured before sshd restart". | 0.5d |
| `PLUGIN-BACKUPS` | Per-plugin file backups with retention `plugin_backups=N`. Every modified file gets a snapshot, last N kept. Cheap rollback. | 1d |
| ~~`MUNGE-OPT`~~ | **REJECTED** after the multi-daemon review. Munge bakes in `uid==0` root-only model that fights OIDC/SSO/multi-tenant and is HPC-internal-only. If we ever need an inter-daemon transport, use mTLS on a unix socket or HMAC header — not munge. | n/a |

**Source:** clustervisor `resolve_roles()` (`server/cv_config.py:6065`), plugin decorators (`@is_dangerous`, `set_priority`, `plugin_backups`), `CVSystemdUnit.getlog` (`common/cv_dbus.py:166`).

---

## Sprint 42 — Migration + schema discipline

**Theme:** make schema changes safer + audit log debug-friendlier.
**Owner:** Richard.

| ID | Item | Effort |
|---|---|---|
| `STATS-DB-SPLIT` | Move stats time-series into a separate SQLite file (`/var/lib/clustr/stats.db`) with WAL + own migrations dir. Lets us iterate stats schema without touching `clustr.db`. | 1d |
| `MIGRATE-CHAIN` | `from_version → to_version` lookup map for skip-version upgrades. Currently linear numbered SQL files; add a `lookup.yml` so we can prune intermediate steps. | 0.5d |
| `JSON-SCHEMA` | Per-resource JSON Schema validation at API write boundary, with cross-resource FK rules. Catches "I configured an invalid combo" at write time, not at deploy time on the node. | 2d |
| `VALIDATE-COMMIT` | Two-phase write: `POST /validate-config` → preview validation → `POST /commit-config`. Cockpit pattern. Worth adopting for multi-node config edits where cluster-wide impact is non-trivial. | 1d |
| `MULTI-ERROR-ROLLUP` | Form/API error response surfaces ALL validation failures, not first-error-per-field. Cockpit aggregates ("Errors with variants" multi-error). Today clustr returns first error. | 0.5d |
| `SCHEMA-DRIFT-BANNER` | When running binary expects schema vN but DB is at vN-1, surface as a system alert + UI banner. Drift telemetry. | 0.5d |
| `NOTICE-PATCH` | Notice/Patch upgrade pattern for **supervised** DB migrations only. RPM cadence remains the default for binary refresh; this is the escape hatch for migrations that need process pause. Server periodically runs `Check` (sets system alert), operator runs `Patch` script. | 1d |
| `EVENT-LOG-JSONL` | JSON-lines sidecar to SQL `audit_log`. `tail -f`-able, replayable for support bundles (`replay(*handlers, after=ts)`). Same data, two consumers. | 0.5d |

**Source:** clustervisor `migrate/migrations/lookup.yml` chain pattern, Cerberus schema validator (`cv_schema.py`), `cv_event_store.py`, `upgrade/__init__.py` `CVNoticeCheck/CVNoticePatch`, two-phase `validate_config` + `queue/reconfigure` pattern.

---

## Sprint 44 — Cockpit-parity node UX (demo-day gaps)

**Theme:** the inputs an HPC operator reaches for in the first 30 seconds.
**Owner:** Dinesh (UI), Richard (backend schema extensions).

These are the top 5 missing inputs/actions Richard's node-input-field inventory identified — what cockpit exposes on the Nodes surface that clustr does not. **Each is concretely demo-day-visible.**

| ID | Item | Effort |
|---|---|---|
| `MULTI-NIC-EDITOR` | Multi-NIC / fabric (IB) editor on node form. HPC nodes have IB + 1GbE mgmt + 10GbE compute + IPMI minimum. Today clustr's `NodeAddSheet` takes one MAC + one IP. Schema extension on `node_configs.interfaces` (already exists as `[]json`) + form rework. Cockpit puts `ethernet`, `fabric`, `ipmi` interface blocks per-node with per-channel settings. | 1 sprint |
| `HOSTLIST-BULK-ADD` | Hostname range expansion in bulk-add (`compute[001-128,200-250]`). Pure parser + UI preview, no server change. Pairs with `HOSTLIST` from Sprint 34 (CLI/API parser). | 2-3d |
| `BULK-MULTISELECT-POWER` | Multi-select on nodes table + bulk power on/off/cycle. Today operators reboot 32 nodes by clicking 32 power-cycle icons. New `POST /api/v1/nodes/bulk/power/{action}` or client-side fanout. | 3-5d |
| `BULK-MULTISELECT-ACTIONS` | Generalize multi-select beyond power: bulk reimage trigger, bulk drain, bulk run-command, bulk netboot change. | 2d on top of `BULK-MULTISELECT-POWER` |
| `VARIANTS-SYSTEM` | Per-attribute overlays — node attributes can have group-scoped or role-scoped variants. Cockpit's "Add new variant" / "Save variants" / "Errors with variants" UX. Nice-to-have but powerful for "all GPU nodes use override X for kernel args" type config. | 1w |

**Source:** cockpit node-input-field inventory (Richard dispatch `af2d382c3b88fa761`).

---

## Sprint 45 — Cockpit-parity ops surfaces (operator power-tools)

**Theme:** cluster-wide actions + dashboards + observability surfaces.
**Owner:** Dinesh (UI), Richard (backend).

| ID | Item | Effort |
|---|---|---|
| `PARALLEL-EXEC-UI` | "Run commands across all nodes in this group" — pdsh-equivalent UI bound to groups + selectors. clustr already has single-node WS exec; generalize to fan-out. New `web/src/routes/exec.tsx`. | 1-2w |
| `CUSTOM-DASHBOARDS` | Operator-built monitoring views with widgets, group-stats, computed-stats. Real lock-in feature. Requires stat-definition schema (covered by `STAT-REGISTRY` in Sprint 38) + a query/aggregation API + web UI for layout. | 4-6w |
| `MONITOR-RULES-UI` | Extending existing alerts UI (#155) with: rule history, thresholds, alert history view, action mapping, named-compute-expression rules. | 1w |
| `SYNCED-FILES` | Push config files to nodes from server-managed manifest. Adjacent to bundles model; in-product Ansible-lite. | 3w |
| `ROLE-PACKAGES` | Declared package sets per-role (e.g., "compute" role gets nvidia-driver + cuda + slurm-client). Lives alongside `SYNCED-FILES`. | 2w |
| `EXPRESSION-BUILDER` | UI for the named-compute-expression rule language used by `MONITOR-RULES-UI` and `CUSTOM-DASHBOARDS`. Defer until either lands. | 1w |

**Source:** cockpit UI review (Richard dispatch `a90d7f63bf7ca7f22`) ranked items 2-5; cockpit node inventory's "non-node flag" (Computed/Group stat builder, Synced File Settings, Expression Builder).

---

## Sprint 46 — UI pattern lifts (PatternFly → shadcn)

**Theme:** steal cockpit's most useful component patterns; build them in shadcn, don't adopt PatternFly.
**Owner:** Dinesh.

| ID | Item | Effort |
|---|---|---|
| `WIZARD-COMPONENT` | Multi-step wizard with side-rail nav + persistent step indicator. Useful for setup, imaging, and the multi-NIC node-add flow from Sprint 44. Build from shadcn primitives. | 3d |
| `LOG-VIEWER-COMPONENT` | Inline-search + row-virtualized log viewer. Needed for activity logs, install-log streaming (Sprint 33 `STREAM-LOG`), and `JOURNAL-ENDPOINT` (Sprint 41). | 2d |
| `BULK-EDIT-CONTEXT` | Multi-row table edit context. Pairs with `BULK-MULTISELECT-*` (Sprint 44) — let operators edit tags / BMC / group on N rows at once. | 2d |
| `DESCRIPTION-LIST` | Horizontal/vertical detail-panel layout component. Used in node detail tabs, image detail, bundle detail. | 1d |

**Source:** cockpit's PatternFly v4 components — distinctive patterns worth reproducing in our shadcn setup, not adopting PatternFly itself.

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

~95 engineering days across 14 sprints. With Richard + Dinesh + Gilfoyle in parallel, ~5-6 weeks calendar.

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
