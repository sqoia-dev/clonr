# clustr Changelog

---

## Turnkey verification Round 4 — kpartx extraction fix + DeviceAllow dm fix (2026-04-26)

### Problem 1: losetup --partscan partition devices unOpenable from Go subprocess

On Rocky Linux 9 kernel `5.14.0-611.47.1.el9_7.x86_64` with an XFS data volume mounted,
`losetup --partscan` creates `/dev/loopNpM` device nodes in `/dev` but they cannot be opened
from Go child processes spawned by `clustr-serverd`. The kernel creates the nodes and
`dmesg` shows `loop0: p1 p2 p3`, but any attempt to `open(2)` them from a subprocess of the
Go server returns `EPERM` / "Can't open blockdev".

Root cause: the combination of the XFS volume and the specific kernel's loop partition
device handling creates a situation where the partition devices exist in `/dev` but are not
accessible from within Go subprocess namespaces.

### Fix 1: switch from losetup --partscan to kpartx

`extract.go` `ExtractRootfs()` now:
1. Attaches the raw disk with `losetup --find --show` (no `--partscan`)
2. Creates partition device-mapper aliases via `kpartx -av loopDev`
   → produces `/dev/mapper/loop0p1`, `/dev/mapper/loop0p2`, etc.
3. Uses `/dev/mapper/loopNpM` paths for all subsequent mount operations
4. Cleans up with `kpartx -d loopDev` before `losetup -d loopDev` in defer

Device-mapper aliases work reliably regardless of kernel loop device configuration.
Committed as `f6b200d`.

### Problem 2: /dev/mapper/control not in DeviceAllow

`clustr-serverd.service` uses `DevicePolicy=auto` which enforces a cgroup BPF device filter.
The `DeviceAllow` list included loop devices and `/dev/kvm` but not device-mapper. When the
extract subprocess called `kpartx`, libdevmapper's first action is to open `/dev/mapper/control`
(char device, major 10, minor 236). The cgroup device filter denied the `open(2)` with EPERM:
`/dev/mapper/control: open failed: Operation not permitted`.

### Fix 2: add /dev/mapper/control and block-253 to DeviceAllow

Added two new `DeviceAllow` entries to `deploy/systemd/clustr-serverd.service`:
- `DeviceAllow=/dev/mapper/control rw` — allows libdevmapper to open the control node
- `DeviceAllow=block-253 rwm` — allows read/write/mknod on device-mapper block devices
  (major 253), which are the `/dev/mapper/loopNpM` partition aliases created by kpartx

Updated the comment block explaining why both loop and device-mapper device allow entries
are required for the extraction pipeline.

### Round 4 verification result

After both fixes:
- Rocky Linux 9.5, BIOS firmware, OpenHPC EL9 repo
- Image `bc6d3923` built to `ready` status in ~4 minutes (ISO cached)
- Image size: 1.7 GiB (vs. 1.8 GiB for Rocky 10 — reasonable for a minimal install)
- Firmware: `bios`, disk layout: biosboot(1M) + /boot(1G) + /(root, XFS)
- Build extraction succeeded: no "Can't open blockdev", no "Operation not permitted"

Node reimage and `srun -N2 hostname` verification: in progress (continuing).

### Gaps found

- **NEW-GAP-13** (P2): `deploy/systemd/clustr-serverd.service` in the repo lacked
  `DeviceAllow` entries for device-mapper. Any fresh install of clustr on a Rocky Linux 9
  host would hit this bug on the first BIOS image build. Fixed in this commit.

- **NEW-GAP-14** (P2): `extract.go` used `losetup --partscan` which fails on Rocky Linux 9
  kernels with XFS data volumes when called from a Go subprocess. Fixed by switching to kpartx
  in `f6b200d`.

---

## Management LAN access via Caddy bridge — tls-provisioning.md + install.md (2026-04-26)

### Problem

After the SEC-P0-2 fix bound `clustr-serverd` to `10.99.0.1:8080` (provisioning interface only),
the operator web UI became unreachable from the management LAN (`192.168.1.151`). The security
posture was correct but the management access path was broken.

### Solution: Caddy reverse-proxy bridge (Option B)

Installed Caddy 2.11.2 on the provisioning host. Caddy listens on `192.168.1.151:80` (management
interface, bound via `bind` directive) and reverse-proxies to `10.99.0.1:8080`. Because both IPs
belong to the same host, Caddy reaches `clustr-serverd` as an interface-local connection with no
firewall involvement. `clustr-serverd` was not restarted and `CLUSTR_LISTEN_ADDR` was not changed.

Operational impact: none. clientd nodes continue reaching `10.99.0.1:8080` directly. No deploy
traffic flows through Caddy.

Firewall change: opened `http/tcp` on the `external` zone (eth0 / management LAN); removed the
previously open `8080/tcp` on the external zone (it was harmless but misleading).

### Doc updates

- `docs/tls-provisioning.md` — new §3 "Management interface access (dual-NIC setup)" with the
  exact Caddyfile, installation steps, firewall rules, and verification commands. §3–§5 (old)
  renumbered to §4–§6. Added dual-NIC Caddyfile variant to §2.2.

- `docs/install.md` — firewall section now includes a "Dual-NIC operators" note pointing to
  `tls-provisioning.md §3` and explicitly warning against rebinding to `0.0.0.0`.

### Live state on cloner (192.168.1.151)

- Caddy 2.11.2 installed, `systemctl enable --now caddy` — active and enabled
- Listening: `192.168.1.151:80` (management) + `127.0.0.1:2019` (admin API, localhost-only)
- `http://192.168.1.151/api/v1/healthz/ready` returns HTTP 200 with all checks green
- `http://192.168.1.151/` returns clustr web UI (HTTP 405 on HEAD is expected — GET works)
- `http://10.99.0.1:8080/api/v1/healthz/ready` still returns HTTP 200 (direct path intact)
- clustr-serverd PID 1593353, no restart, continuous since 09:50:57

---

## Turnkey verification Round 3 — slurm-module.md API corrections + README Quick Start (2026-04-26)

### Slurm API path and body format corrections

Three API errors found during live verification (all silent or returning misleading errors):

- **`/api/v1/modules/slurm/{enable,status}` paths do not exist.** Correct paths are
  `/api/v1/slurm/{enable,status}`. The old `/modules/slurm/` prefix was never registered.
  `slurm-module.md §3` and `§9` updated with correct paths.

- **Role assignment body format was wrong.** `PUT /api/v1/nodes/{id}/slurm/role` requires
  `{"roles": ["controller"]}` (plural array). The doc said `{"role": "controller"}` (singular
  string). Sending the wrong format returned HTTP 200 with `{"status":"ok"}` but silently set
  an empty roles array — a quiet no-op with a success response. `slurm-module.md §4` corrected.
  Role assignment path was also wrong: `/api/v1/slurm/roles/{id}` → 404; correct path is
  `/api/v1/nodes/{id}/slurm/role`.

- **Config save endpoint expects JSON, not text/plain.** `PUT /api/v1/slurm/configs/{name}`
  requires JSON body `{"content":"<full-conf-text>","message":"<optional>"}`. The doc said
  `text/plain`. Updated `slurm-module.md §6` and `§9`.

### slurm.conf default value issue (operator action required)

The default `slurm.conf` rendered on first module enable sets `SlurmctldHost` to the clustr
server's own hostname and `AccountingStorageType=accounting_storage/slurmdbd`. Both values are
wrong for a basic 2-node cluster with no slurmdbd. Operators must update `slurm.conf` via
`PUT /api/v1/slurm/configs/slurm.conf` after module enable. Added to `slurm-module.md §6` and
the new README Quick Start section.

### OpenHPC EL10 availability confirmed absent

Live test confirmed `https://repos.openhpc.community/OpenHPC/3/EL_10/repodata/repomd.xml` returns
HTTP 404. The recommended path for Slurm clusters is Rocky Linux 9 + OpenHPC EL9 repo until
OpenHPC publishes EL10 packages. `slurm-module.md §2.1` updated with explicit warning.

### README Quick Start: 2-Node Slurm Cluster

Added a new end-to-end Quick Start section to `README.md` with:
- Step-by-step numbered instructions from "clustr installed" to `srun -N2 hostname`
- Exact curl commands with placeholder variables
- Call-out that OpenHPC EL10 does not exist yet (use Rocky 9)
- Warning about correct `roles` body format (plural array)
- slurm.conf operator requirements (SlurmctldHost + AccountingStorageType)
- Troubleshooting table for common smoke test failures
- Link to `docs/slurm-module.md` for advanced topics

### Onboarding walkthrough: Round 3 section added

`docs/onboarding-walkthrough.md` updated with "Final Turnkey Verification — 2026-04-26 (Round 3)"
documenting: EL10 repo 404, Rocky 9 image build kickoff, all API bugs found, slurm.conf correction,
and new gaps NEW-GAP-7 through NEW-GAP-10 (all doc/surface fixes, no architectural blockers).

---

## Turnkey gap-fill — DNS injection, build pipeline, audit visibility (2026-04-25)

### NEW-GAP-5 — Inject DNS into NetworkManager profiles

- **Root cause:** Deployed nodes received NM profiles without a `dns=` line,
  causing systemd-resolved to fall back to the QEMU NAT default `10.0.2.3`
  which is unreachable from the provisioning network (10.99.0.0/24). Every
  outbound network call (`dnf`, `curl`, systemd-resolved) on the node silently
  timed out.
- **Fix:** Added `clusterDNSServers()` in `internal/deploy/finalize.go` that
  reads `CLUSTR_DNS_SERVERS` env var (comma-separated IPs). Default is
  `1.1.1.1,8.8.8.8`. Operators running a local forwarder should set
  `CLUSTR_DNS_SERVERS=10.99.0.1`.
- **Applied to all three NM profile writers:**
  - `writeNMKeyfile()` — static/DHCP per-interface profiles (per-interface DNS
    takes precedence; falls back to cluster defaults when `iface.DNS` is empty)
  - `writeClustrDHCPProfile()` — the clustr-dhcp DHCP fallback profile
  - `writeNetworkConfig()` fallback — the wired-dhcp wildcard profile

### NEW-GAP-4 partial — dnf install failure visibility + repo URL validation

- **Audit action `slurm.install.failed`:** When `dnf install` inside the chroot
  fails, the full combined stdout+stderr (last 2 KB) is now captured and emitted
  as a structured zerolog WARN event with `dnf_output`, `node_id`, and `repo_url`
  fields. Operators can grep or stream server logs to see the exact dnf error.
- **`SlurmInstallAuditFn` callback type** exposed from `internal/deploy` package
  so server-level code can wire the `db.AuditService` to write
  `slurm.install.failed` audit records — query with
  `GET /api/v1/audit?action=slurm.install.failed`.
- **EL9/EL10 mismatch detection** in `installSlurmInChroot()`: reads
  `VERSION_ID` from `/etc/os-release` in the chroot and compares it against the
  EL version encoded in the repo URL (`EL_9` / `EL_10` / `EL9` substrings).
  Logs a WARN with the correct URL template if a mismatch is found — runs before
  `dnf install` so the operator sees it immediately.
- **URL reachability check at enable time:** `POST /api/v1/modules/slurm/enable`
  now issues a HEAD request (5s timeout) to `slurm_repo_url` and logs INFO on
  success or WARN on failure/unexpected status. Non-fatal — enable always
  succeeds regardless of URL reachability.
- **`db.AuditActionSlurmInstallFailed = "slurm.install.failed"`** added to
  `internal/db/audit.go` audit action constant set.

### NEW-GAP-6 — Build-from-source pipeline configure flags

- **Root cause:** `buildOneDep()` only ever passed `--prefix` to `./configure`.
  PMIx requires `--with-hwloc=<hwloc-install-path>` to locate hwloc headers;
  without it, PMIx builds without hwloc support, and the resulting Slurm binary
  fails to detect topology.
- **Fix:** Added `depConfigureFlags(name, installPaths)` in `deps.go` that
  returns per-dep cross-dep configure flags. PMIx receives
  `--with-hwloc=<hwloc-install>`. Other deps (hwloc, ucx, munge, libjwt) use
  only `--prefix`.
- **`buildOneDep()` signature changed:** now accepts `installPaths map[string]string`
  (populated progressively as each dep finishes). The caller (`buildDependencies`)
  passes the accumulating map so each dep sees the previous deps' install paths.
- **`buildSlurmConfigureArgs()` in `builder.go` improved:** all dep paths now use
  explicit `--with-<dep>=<path>` when available (previously `--with-munge` had no
  path). Added `--with-munge=<path>`, `--with-jwt=<path>`, fall-back to flag-only
  form when a dep was skipped.

---

## Turnkey gap-fill round 3 — rocky10 image SSH fix + Slurm EL-version docs (2026-04-26)

### NEW-GAP-3 (image) — rocky10 image: add PermitRootLogin yes to sshd drop-in

- Root cause verified: the rocky10 base image (UUID `9a9af513`) had `PermitRootLogin`
  commented out in all sshd_config layers, relying solely on the deploy-time injected
  `70-permit-root.conf` drop-in. The image-level default is now explicit so any deploy
  path that skips the drop-in injection step still permits root SSH key auth.
- Applied Path B fix directly to the live rootfs at
  `/var/lib/clustr/images/9a9af513-5d98-4514-918a-c446df122dcf/rootfs`:
  - Added `etc/ssh/sshd_config.d/60-clustr-lab.conf` with `PermitRootLogin yes`
    and `PasswordAuthentication no` (key auth only; password auth removed from image
    default — deploy injects `PasswordAuthentication yes` separately if needed for lab).
  - Removed stale `etc/ssh/sshd_config.d/60-clonr-password-auth.conf` (old name,
    only had `PasswordAuthentication yes`, no `PermitRootLogin`).
- Re-tarred rootfs deterministically using the identical `tar` flags as
  `Factory.bakeDeterministicTar` (sort=name, mtime=@0, same exclusion list).
- Updated `tar-sha256` sidecar: `ee1a42f8a4b7153cbc85a313ce15b0f3f02ad4b2c338e70a045c52c89c1a3aba`
- Recomputed `checksumDir` (Go-equivalent Python walk): `7bc6f92af1be24551b3d534dc3efaf9ea3dc53b7c3a81fba5d6ea9b7e61563f8`
- Updated `base_images` DB row: `checksum` and `size_bytes` columns updated.
- Updated `metadata.json` sidecar with new checksums and `patch_note`.
- Additional diagnosis: confirmed PAM stack on deployed nodes is already correct —
  finalize code strips `pam_sss.so` from `password-auth` even when LDAP module is
  enabled (GAP-14 fix), so account-phase PAM rejection from the walkthrough was a
  pre-GAP-14 deploy artifact. The authselect profile in the image is `local` (not
  ldap), shadow exists with a valid root hash, nsswitch resolves `passwd: files systemd`.
  The deploy code's `70-permit-root.conf` injection is the primary PermitRootLogin
  mechanism; this image-level fix is belt-and-suspenders.

### NEW-GAP-4 (docs) — slurm-module.md: EL version guidance for slurm_repo_url

- Added §2.1 "Choosing `slurm_repo_url`" to §2 (Image prerequisites):
  - OpenHPC URL table: EL9 (`OpenHPC/3/EL_9`) vs EL10 (check upstream availability).
  - SchedMD URL pattern for direct downloads.
  - Source comparison table (OpenHPC vs SchedMD vs EPEL vs gold image) with
    when-to-use guidance.
  - Critical: URL must match image EL major version. Mismatch causes silent `dnf`
    failure (no matching packages, no error from dnf, Slurm absent after deploy).
  - Reachability test: `curl -I <slurm_repo_url>` from the clustr-serverd host.
  - Coordination note: once server-side EL version validation lands (Dinesh's
    NEW-GAP-4 code fix), the section will update to reflect that clustr enforces
    the match at module-enable time.
- Added troubleshooting row "Slurm not installed on deployed node" to §10:
  - Points to §2.1 for URL selection.
  - Distinguishes EL-mismatch silent failure from unreachable-repo WARN failure.
  - Notes that `slurm.install.failed` audit event will appear after Dinesh's fix lands.

---

## Turnkey gap-fill round 2 — Slurm auto-install (2026-04-25)

### NEW-GAP-2 (code) — `munge_key_present` in Slurm status

- `GET /api/v1/modules/slurm/status` now returns `"munge_key_present": <bool>`.
  `true` iff `slurm_secrets` has a row for `munge.key`. Lets operators confirm
  the munge key was generated without needing a separate DB query.

### Item 2 — Auto-install Slurm at deploy time (Option B: operator-provided repo URL)

- **Design choice: Option B** — the operator provides `slurm_repo_url` at
  module-enable time. This avoids tying clustr to a specific Slurm distribution
  and works equally well with OpenHPC, SchedMD RPMs, or any custom dnf repo.
- `POST /api/v1/modules/slurm/enable` now accepts `slurm_repo_url` (optional).
  Stored in `slurm_module_config.slurm_repo_url` (migration 050).
- At deploy finalize time, if `slurm_repo_url` is set, `finalize.go` writes
  `/etc/yum.repos.d/clustr-slurm.repo` into the chroot and runs
  `dnf install -y slurm munge` (+ `slurm-slurmctld` for controllers,
  `slurm-slurmd` for compute nodes) inside the chroot before config injection.
- The existing GAP-14 binary-existence guard remains in place as the safety net
  for nodes without a Slurm role (they correctly skip service enable).
- Non-fatal: if `dnf` fails (unreachable repo, network error), the error is
  logged as WARN and the deploy continues — operator can diagnose and re-image.

---

## Docs — Slurm turnkey path (2026-04-25)

### Documentation

- **[NEW-GAP-1] `docs/slurm-module.md` §3 — enable curl example corrected**
  Route fixed from `/api/v1/slurm/enable` to `/api/v1/modules/slurm/enable`.
  Request body added: `cluster_name` (required, sets `ClusterName` in
  `slurm.conf`) and `slurm_repo_url` (repo used for auto-install at deploy
  time). Inline explanation added for both fields. Status verify example
  updated to the correct `/modules/slurm/status` route with a full expected
  response shape including `munge_key_present`, `cluster_name`, and
  `slurm_repo_url`. `munge_key_present` field semantics documented (true
  after first enable, stays true permanently under normal operation).
  §9 API reference table routes corrected to `/modules/slurm/*` for all
  three module-status entries; descriptions updated to reflect required
  request body and new response fields.

- **[NEW-GAP-2 docs] `docs/slurm-module.md` §3 — `munge_key_present` field documented**
  The `GET /api/v1/modules/slurm/status` expected response now explicitly
  shows `munge_key_present: true` and explains its lifecycle. Closes the
  docs side of NEW-GAP-2 (Dinesh's code side: add `munge_key_present` field
  to the status response struct).

- **[Item 2] `docs/slurm-module.md` — auto-install turnkey path documented**
  §1 Overview updated: module now described as auto-installing Slurm at deploy
  time. The previous statement "the module does not install Slurm binaries" is
  removed. §2 restructured into two paths: Recommended (any base image,
  auto-install from `slurm_repo_url` at finalize) and Advanced (gold image with
  Slurm pre-baked, for air-gapped or speed-critical deployments). Auto-install
  finalize sequence documented (repo add → package install → munge key inject →
  slurm.conf write → systemd enable). Non-fatal behavior documented (WARN + continue
  if repo unreachable). §3 "What happens automatically on enable" updated with
  step 3 (`cluster_name` and `slurm_repo_url` stored) and step 4 (auto-install
  gated on Slurm role). §7 smoke test intro updated: fresh Rocky 9/10 image now
  works end-to-end without pre-baking. Prerequisites checklist updated to include
  `slurm_repo_url` reachability check. §8 "Upgrading Slurm" now documents the
  auto-install upgrade path (update `slurm_repo_url`, reimage — no image rebuild
  required) alongside the gold image upgrade path. §10 troubleshooting: replaced
  "install slurm in the image" row with "check slurm_repo_url reachability" row,
  updated to reflect non-fatal WARN behavior. Old degraded-state row retained for
  the gold-image advanced path.

---

## Gap-fill Sprint — Slurm + UX hardening (2026-04-25)

Ten code-side findings from the end-to-end new-user walkthrough (Task #62)
resolved before external-user release.

### P1 fixes (must-fix before external users)

- **GAP-2** `GET /api/v1/healthz/ready` is now unauthenticated. Docker Compose
  healthchecks, `install.md` smoke tests, and the README Quick Start all expect
  200 without credentials. The `/health` liveness probe is unchanged.

- **GAP-14** Slurm/munge service enable now guarded by binary existence check.
  `writeSlurmConfig` no longer calls `systemctl enable slurmd` (or `munge`,
  `slurmctld`, `slurmdbd`) when the corresponding binary is absent from the
  deployed rootfs. The previous behaviour created broken systemd symlinks that
  put the unit in degraded state on first boot, causing PAM to terminate SSH
  sessions immediately after key acceptance — making every deployed node
  unreachable via SSH.

- **GAP-17** Three flat Slurm API routes added:
  `GET /api/v1/slurm/nodes`, `GET /api/v1/slurm/roles`,
  `POST /api/v1/slurm/sync` (triggers push, returns op ID for polling).

- **GAP-19** Munge key auto-generated on first `POST /slurm/enable`. Idempotent —
  if a key already exists in `slurm_secrets` it is not overwritten.

- **GAP-20** Audit log wired into previously-missing handlers:
  API key create/revoke/rotate (`api_key.*` actions) and
  Slurm module enable + config file save (`slurm_config.update`).

- **GAP-21** `/api/v1/users` CRUD alias routes added (list, create, GET {id},
  update, delete) — admin-only, same handlers as `/admin/users`. Sprint 3 docs
  and the walkthrough expected these paths.

### P2 fixes (important for usable experience)

- **GAP-15** Reimage preflight blocks reimaging a node with no `ssh_keys`
  configured (HTTP 400, code `no_ssh_keys`). Pass `force=true` to override.
  Prevents deploying a permanently SSH-inaccessible node.

- **GAP-11** `GET /api/v1/nodes/{id}/reimage/active` added. Previously only
  DELETE existed; GET returned an empty body, breaking JSON parsers. Now returns
  the active `ReimageRequest` when one exists, or
  `{"status":"no_active_reimage"}` otherwise.

- **GAP-23** On startup, if `/var/lib/clustr/clonr.db` exists alongside the
  current `clustr.db`, log WARN so operators know it is safe to delete.
  No auto-deletion.

---

## v1.0.0 — Release Notes (target: 2026-07-25)

### What is clustr?

clustr is an open-source, self-hosted HPC node imaging and provisioning platform.
It ships as a single Go binary (`clustr-serverd`) backed by SQLite, requires no cloud
services, and targets bare-metal and Proxmox-managed clusters of any size.

### 90-Day Arc: v0.x → v1.0

clustr enters v1.0 as a production-ready, secure-by-default platform. Over the 90-day
sprint window (Sprint 0 through Sprint 6) the following transformations were made:

**Security hardened:** All P0 credential-at-rest gaps are closed — BMC passwords and
LDAP bind credentials are AES-256-GCM encrypted (CLUSTR_SECRET_KEY required). The DB
is chmod 600. The provisioning listener is bound to the provisioning interface, not
0.0.0.0. SSH passwords in the PXE initramfs are now per-boot random (from /dev/urandom).
gosec, trivy, and govulncheck run in CI on every push.

**Multi-user RBAC:** Three-tier role model (admin / operator / readonly) with
group-scoped operator access. Operators can manage their assigned NodeGroups without
touching other groups. Full audit log on all state-changing actions.

**Coherent image factory:** Five separate finalize paths collapsed into one
`finalizeImageFromRootfs` helper. Image tags, metadata, and stale-initramfs warnings
are surfaced in the UI.

**Clean data model:** `groups[]` renamed to `tags[]` (unstructured node labels).
`NodeGroup` is the sole structured grouping primitive. The `node_configs.group_id`
denormalized column is replaced by `node_group_memberships.is_primary=1` as the
authoritative source. The `last_deploy_succeeded_at` back-compat alias is removed;
`deploy_completed_preboot_at` (ADR-0008) is now the sole canonical field.

**Production observability:** Prometheus `/metrics` endpoint with 8 metrics, readiness
endpoint (`/api/v1/healthz/ready`), outbound webhooks with HMAC signing, structured
audit log, node config change history.

**Operator tooling:** Docker Compose install package, Ansible bare-metal role,
install/upgrade docs, backup integrity verification, rollback guard in autodeploy.

**UI polish:** Per-persona UX improvements — bulk reimage, GPU inventory, power state
column, server-side sort/search, scheduled reimage, getting-started wizard, first-deploy
wizard, CI key preset, dry-run checkbox for group reimage.

### API Deprecation Notice

The `groups` field in `NodeConfig` JSON responses is deprecated. It mirrors `tags`
and will be removed in **v1.1**. All endpoints returning `NodeConfig` now emit a
`Sunset: Sat, 25 Oct 2026 00:00:00 GMT` header. Update clients to read `tags` instead.

### Migration Notes

- Migrations 001–049 run automatically at startup. All are backward-compatible.
- `CLUSTR_SECRET_KEY` is now required in non-dev mode. Server refuses to start without
  it. See `docs/install.md` for key generation instructions.
- `node_configs.group_id` column dropped (migration 048). Group assignment now lives
  exclusively in `node_group_memberships`. No action required — migration handles it.
- `node_configs.last_deploy_succeeded_at` column dropped (migration 049). Use
  `deploy_completed_preboot_at` for all deploy timestamp reads.

---

## Gap-Fill Sprint — Turnkey Readiness (2026-04-26)

### Documentation

- **[GAP-9] `docs/install.md` §7 Step 5 — boot order corrected**
  Changed boot order guidance from `net0;scsi0` (PXE-first) to `scsi0;net0`
  (disk-first, net as fallback). Added explanation: persistent default is
  disk-first; clustr temporarily flips to PXE-first via Proxmox API /
  `SetNextBoot` for each reimage trigger, then flips back after verify-boot.

- **[GAP-16] New `docs/slurm-module.md`** — complete Slurm module operator guide
  Covers: prerequisites (Slurm must be in the image before deploy), enabling
  the module, controller vs worker role assignment, munge key generation and
  distribution, `slurm.conf` rendering from node inventory, `srun hostname`
  smoke test, day-2 ops (add node, remove node, upgrade Slurm), full API
  reference, and troubleshooting table. Linked from `README.md` and
  `docs/install.md` See Also section.

- **[GAP-2] `docs/install.md` §3.6 and §7 Step 1 — healthz unauthenticated documented**
  Both healthz/ready examples updated to reflect the code fix (GAP-2 above):
  the endpoint is unauthenticated, no Bearer token required. Examples restored
  to the original no-auth form with a clarifying comment.

- **[GAP-1] `docs/install.md` §5 — `CLUSTR_SECRET_MASTER_KEY_PATH` documented**
  Added entry to the Security env var table explaining this optional path-based
  key override and its fallback behaviour.

- **[GAP-3] `docs/install.md` §4.4 and `README.md` Quick Start Step 2 — bare-metal env var note**
  Added explicit note that `clustr.env` is a Docker Compose convention; bare-
  metal operators must set env vars in the systemd unit's `Environment=` lines.

- **[GAP-4] `docs/install.md` §6 — bootstrap key recovery callout**
  Added prominent "Recovery: if you missed the bootstrap API key" block with
  the exact `clustr-serverd apikey create` command for both bare-metal and
  Docker Compose paths.

- **[GAP-5] `docs/install.md` §3.2 and §5 — `CLUSTR_SESSION_SECRET` warnings**
  Added WARNING comment to the secrets.env generation snippet and strengthened
  the env var table entry to explicitly state that omitting this causes every
  web UI session to be invalidated on every server restart.

- **[GAP-6] `docs/install.md` §7 Step 2 — image creation path reordered**
  `factory/pull` (cloud image URL) is now the primary recommended first-image
  path — no extra host packages, works out of the box. ISO build moved to
  "Alternative path" with an explicit prerequisite list (`qemu-kvm`, `qemu-img`,
  `genisoimage`, `xorriso`).

- **[GAP-8] `docs/install.md` — new §8 "Registering Nodes"**
  New section covering: MAC address discovery methods (Proxmox, DHCP log, BIOS),
  `POST /api/v1/nodes` API call, `ssh_keys` importance warning, Proxmox power
  provider config, IPMI power provider config, and manual power-cycle workflow
  for nodes without BMC.

- **[GAP-12] `docs/install.md` — new §9 "Reimaging Multiple Nodes"**
  New section covering: bulk reimage via web UI checkbox + floating action bar,
  group reimage via `POST /api/v1/node-groups/{id}/reimage`, use case for
  redeploying an entire cluster.

- **[GAP-10] `docs/install.md` §7 troubleshooting — `autoexec.ipxe` row**
  Added row explaining that `autoexec.ipxe not found` in TFTP logs is normal
  UEFI iPXE probe behaviour, not an error.

- **[GAP-13] `docs/install.md` §7 troubleshooting — initramfs warning row**
  Added row explaining that `initramfs not found` during finalize is expected
  on ISO-built images; dracut rebuilds it automatically.

- **[systemctl degraded] `docs/install.md` §7 troubleshooting — degraded row**
  Added row: `systemctl is-system-running` shows `degraded` after first boot
  → caused by `slurmd.service` enabled but slurm not installed → fix by
  pre-installing slurm in image or disabling the Slurm module.

---

## Sprint 6 — Release Readiness (2026-07-06 → 2026-07-19)

### CI / Build Gates

- **[S6-9] `initramfs.yml` gates on `ci.yml` success**
  `initramfs.yml` now calls `ci.yml` via `workflow_call` and declares
  `needs: [ci]` on the build job. The initramfs artifact is built and attached
  to a release only after lint, tests, build, gosec, govulncheck, and trivy all
  pass. Combined with `release.yml`'s existing `needs: [lab-validate]`, the
  complete gate chain is: `go test` passes → initramfs built → lab validation
  green → release artifacts published. No initramfs binary ships without green
  CI and green lab validation.

- **[S6-1] Reproducible iPXE build from source in CI** (SEC-P0-3)
  New `.github/workflows/ipxe-build.yml` workflow pins iPXE at `v1.21.1`,
  builds `ipxe.efi` from source with `EXTRA_CFLAGS="-DCOLOUR_CMD -DIMAGE_PNG
  -DCONSOLE_CMD"`, computes SHA-256, and compares it to the committed value in
  `deploy/pxe/ipxe.efi.sha256`. A mismatch fails the build and blocks any
  release. On tag push, the CI-built binary and its SHA-256 sidecar are attached
  to the GitHub Release so operators can verify the binary they downloaded.
  `deploy/pxe/README.md` updated with full provenance record: version tag,
  build flags, SHA-256, and CI verification reference.
  `internal/bootassets/assets.go` updated with v1.21.1 tag and CI gate note.
  **Closes SEC-P0-3 from ops-review.**

### Packaging

- **[S6-2] Docker Compose install package** (primary packaging — Decision D7)
  `deploy/docker-compose/docker-compose.yml` — production-ready Compose file
  with `network_mode: host` (required for DHCP/TFTP broadcast binding),
  volume mount for `/var/lib/clustr`, `/dev/kvm` pass-through, capability grants
  (`NET_BIND_SERVICE`, `SYS_ADMIN`, `SYS_CHROOT`, `MKNOD`), cgroup device rule
  for loop block devices (`b 7:* rwm`), healthcheck via the S1-10 readiness
  endpoint, and an 8 GB memory ceiling.
  `deploy/docker-compose/.env.example` — fully documented template for all
  `CLUSTR_*` environment variables with inline comments and generation commands.
  README "Quick Start" section rewritten to use Docker Compose as the primary
  install path (creates dirs, generates secrets, fetches the Compose file, runs
  `docker compose up -d`) replacing the obsolete bare `docker run` one-liner.

- **[S6-3] Ansible role for bare-metal install** (secondary packaging — Decision D7)
  `deploy/ansible/` role covering:
  - Version resolution (`latest` queries GitHub Releases API; specific tag pins for reproducibility)
  - SHA-256 verified binary download and atomic swap
  - Data directory creation (idempotent `file:` tasks)
  - `secrets.env` written with `no_log: true` (prevents secrets in Ansible output)
  - `clustr.env` from `roles/clustr/templates/clustr.env.j2` (all variables templated)
  - systemd unit from `roles/clustr/templates/clustr-serverd.service.j2`
  - Backup + restore-verify script download and timer enablement
  - Firewall rules via `firewalld` (Rocky/RHEL) or `ufw` (Ubuntu/Debian)
  - Restart guard: checks for active reimages before triggering a handler-driven restart
  - Post-install readiness endpoint smoke test
  - Tags: `install`, `config`, `systemd`, `firewall`
  `deploy/ansible/site.yml` — example top-level playbook.
  `deploy/ansible/README.md` — usage, variable reference, idempotency guarantees.

### Documentation

- **[S6-4] `docs/upgrade.md`** — operator upgrade guide
  Covers: how migrations work (automatic at startup, transactional, fail-closed),
  Docker Compose and bare-metal upgrade procedures with checksum verification,
  which env vars invalidate sessions on rotation (`CLUSTR_SESSION_SECRET` logs
  out all users; `CLUSTR_SECRET_KEY` transparently re-encrypts credentials —
  no session impact), how to confirm a successful upgrade (version endpoint,
  readiness checks, migration log output), and full rollback procedure (stop,
  restore DB from S4-8 verified backup, restore old binary, restart, verify).
  Linked from README and `docs/install.md`.

- **[S6-5] `docs/tls-provisioning.md`** — TLS provisioning guide (HA-5)
  Covers: when TLS is required (threat model decision table), Caddy as the
  recommended terminator (install, Caddyfile with header hardening, Secure
  cookie activation, firewall lockdown of port 8080, internal PKI options via
  DNS-01 challenge or manual cert), configuring `CLUSTR_SERVER` in the iPXE
  initramfs boot script for HTTPS, injecting a private CA cert into initramfs,
  physically-isolated network exception (conditions under which HTTP is
  acceptable: L2 isolation, no routing, restricted physical access), and
  nginx/Traefik/HAProxy alternatives with example configs.
  `docs/install.md` §2 now links to this document. Linked from README.

### Schema Cleanup

- **[S6-6] Drop `node_configs.group_id` column** (migration 048)
  The denormalized fast-path `group_id` column on `node_configs` is removed.
  The authoritative source is now `node_group_memberships WHERE is_primary = 1`
  (established in S2-5, migration 042). All DB reads and writes updated to use
  the membership table exclusively. The `EffectiveLayout()` and
  `EffectiveExtraMounts()` call chain continues to receive the primary group via
  the JOIN. BUG-1 (GroupID cleared on PUT) fix remains intact — the handler now
  routes group changes through `AssignNodeToGroup` → `SetPrimaryGroupMember`.

- **[S6-8] Drop `node_configs.last_deploy_succeeded_at` column** (migration 049)
  The back-compat dual-write column introduced during the ADR-0008 two-phase
  deploy verification transition is removed. `deploy_completed_preboot_at` is
  now the sole canonical "deploy complete" timestamp. `RecordDeploySucceeded`
  no longer dual-writes. `State()` no longer falls back to `LastDeploySucceededAt`.
  The `last_deploy` sort column in the nodes list now sorts by
  `DeployCompletedPrebootAt`. The `TestMigration022_DualWrite_BackCompat` test
  is replaced by `TestMigration022_DeploySucceeded_StateTransitions` which
  tests the canonical ADR-0008 path without any legacy fallback.

### API Deprecation (S6-7)

- **[S6-7] `NodeConfig.groups` field Sunset header**
  Per `decisions.md` D3: the `groups` JSON field stays in v1.0 responses but
  is removed in v1.1. All node-returning endpoints (`GET/POST/PUT /api/v1/nodes`,
  `GET /api/v1/nodes/:id`, `GET /api/v1/nodes/by-mac/:mac`,
  `GET /api/v1/node-groups/:id`) now emit:
  ```
  Sunset: Sat, 25 Oct 2026 00:00:00 GMT
  Deprecation: true; rel="deprecation"; field="groups"
  ```
  Clients should migrate to reading the `tags` field. The `groups` field will be
  removed in v1.1 (estimated 2026-10-25, 90 days after v1.0).

### Deploy Log Cleanup (S6-10)

- **[S6-10] Remove debug ESP log blocks from `rsync.go`**
  Two debug log blocks (lines 526–553 in the pre-patch file) that logged EFI
  System Partition directory contents after extraction are removed. These were
  added during UEFI boot debugging and are no longer needed now that UEFI boot
  is confirmed stable. Production deploy logs are uncluttered. The blocks used
  `Debug()` level but were verbose enough to obscure real deploy events in busy
  clusters.

---

## Sprint 5 — Persona Polish (2026-06-22 → 2026-07-06)

### Hardware Discovery

- **[S5-2] GPU detection via PCI sysfs**
  `DiscoverGPUs()` in `internal/hardware/gpu.go` enumerates `/sys/bus/pci/devices`
  for PCI class `0x03xx` (VGA, XGA, 3D Controller, Display Controller) without
  requiring `lspci`. Vendor/device IDs are resolved to readable names; VRAM is
  read from AMD's `mem_info_vram_total` or NVIDIA's BAR0 `resource` file.
  `SystemInfo.GPUs []GPU` is populated in `Discover()`. Returns `nil, nil` when
  sysfs is absent (CI-safe). Tests in `gpu_test.go`.

### UI / UX

- **[S5-1] Power state column in nodes list**
  The nodes list now shows a "Power" column. After the table renders, a batch
  of concurrent IPMI/provider power-status calls (capped at 10 in-flight) fills
  in the state for each node that has a BMC or power provider configured.
  Nodes without power management show `—`.

- **[S5-3] Server-side sort for nodes list**
  `GET /api/v1/nodes` accepts `?sort=hostname|status|last_deploy|group` and
  `?dir=asc|desc`. The nodes list table headers for Host, Status, Updated, and
  Group are now clickable; the current sort column shows an arrow indicator.
  Sort state persists across auto-refresh cycles.

- **[S5-4] Bulk reimage from nodes list**
  A checkbox column appears for admin/operator roles. Selecting one or more nodes
  shows a floating action bar with a "Reimage Selected" button. The modal lets
  the operator choose a target image (or use each node's assigned image) and
  toggle dry-run. Reimage requests are submitted individually with a concurrency
  loop (no rate limit at the UI layer).

- **[S5-5] Retry and Re-deploy from nodes list row actions**
  Nodes in a failed deploy state show a "Retry" button that finds the most recent
  failed reimage and posts to `/reimage/:id/retry`. Nodes with an assigned image
  and no active failure show a "Re-deploy" button that opens the reimage modal
  pre-populated with the node's current image.

- **[S5-6] CI API key preset in key-creation modal**
  The "Create API Key" modal now includes a "CI / Pipeline key" quick-preset
  that fills scope=node, label="ci-key", and TTL=30 days with one click.
  After a node-scoped key is created, the confirmation dialog shows a ready-to-use
  `curl` snippet for triggering a reimage from a CI pipeline.

- **[S5-7] Dry-run checkbox in group reimage modal**
  The group reimage modal now includes the same "Dry run" checkbox as the
  single-node reimage modal. When checked, `dry_run: true` is passed to
  `POST /node-groups/:id/reimage`.

- **[S5-8] Images page: Build from ISO first + onboarding callout**
  "Build from ISO" is now the primary (blue) button on the Images page.
  Pull, Capture, and Import are secondary. When no images exist and no filter
  is active, an info callout card explains each build method to new operators.
  The empty-state action button also defaults to "Build from ISO".

- **[S5-9] First-deploy wizard on dashboard**
  When no images and no nodes exist, the Dashboard shows a 3-step getting-started
  wizard: (1) Build an Image, (2) Register a Node, (3) Deploy. The wizard
  disappears automatically once images or nodes are present.

- **[S5-10] Deployments direct nav link + full-page view**
  A "Deployments" link appears in the sidebar (activity-waveform icon).
  `#/deploys` renders a dedicated page with a live-progress table (SSE-backed)
  and a full reimage history table with retry/cancel actions.

- **[S5-11] GET /api/v1/progress 404 routing — documented**
  The routing asymmetry is intentional and already documented in `server.go`:
  `POST /deploy/progress` is outside the admin auth group (node-scoped key from
  initramfs); `GET /deploy/progress`, `/stream`, and `/:mac` are inside the
  admin group (operator read). No routing gap exists.

- **[S5-12] Node config change history**
  Migration 047 creates `node_config_history` — an append-only, field-level
  audit trail written on every `UpdateNode` call. `DiffNodeConfigFields` diffs
  the before/after `NodeConfig` structs and records only changed non-sensitive
  fields. `GET /api/v1/nodes/:id/config-history` (paginated) exposes the log.
  A "Config History" tab in the node detail view shows the change log with
  actor label and timestamp.

### GPU Display

- **[S5-2 UI] GPU inventory in node Hardware tab**
  The Hardware tab now renders a "GPUs (N)" card when the node's hardware profile
  includes GPU entries. The table shows model, vendor ID, device ID, PCI address,
  and VRAM size.

---

All notable changes are grouped by sprint. Items marked [P0] or [P1] are
priority security or reliability fixes.

---

## Sprint 1 — Foundation + Security Hardening (2026-04-25 → 2026-05-10)

### Security

- **[S1-2] Per-boot random SSH password in initramfs** [P0]
  Password is now generated from /dev/urandom on every initramfs boot
  (`dd if=/dev/urandom … | od -An -tx1`). The fixed password and the
  `log " password : …"` line that leaked it to the serial console are
  both removed.

- **[S1-15] LDAP credential encryption at rest** [P0]
  `ldap_module_config.service_bind_password` and `admin_passwd` are now
  encrypted with AES-256-GCM (CLUSTR_SECRET_KEY) at write time and
  decrypted at read time. Migration 038 adds `*_encrypted` flag columns.
  `MigrateLDAPCredentials()` re-encrypts legacy plaintext rows on first
  start after upgrade.

- **[S1-16] BMC / power credential encryption at rest** [P0]
  `node_configs.bmc_config` and `power_provider` JSON blobs are now
  encrypted with AES-256-GCM. Migration 039 adds `*_encrypted` flag
  columns. `MigrateBMCCredentials()` re-encrypts legacy rows on startup.
  `CLUSTR_SECRET_KEY` is required in non-dev mode; server hard-fails if
  unset or set to the insecure default.

- **[shared] `internal/secrets` package**
  New shared AES-256-GCM helpers (`Encrypt`, `Decrypt`, `EncryptWithKey`,
  `DecryptWithKey`, `DeriveKey`, `ValidateKey`) used by LDAP, BMC, and
  future modules. Eliminates duplicated crypto code across modules.

### Reliability / Correctness

- **[S1-3] HTTP server timeouts**
  `clustr-serverd` now sets `ReadHeaderTimeout=10s`, `ReadTimeout=60s`,
  `WriteTimeout=300s`, `IdleTimeout=120s`, preventing connection floods
  from blocking the server.

- **[S1-4] Two-pass log purge: TTL eviction + per-node row cap** [P1]
  The log purger now runs two passes: TTL-based eviction (default 7 days
  via `CLUSTR_LOG_RETENTION`) followed by a per-node row cap (default
  50 000 rows via `CLUSTR_LOG_MAX_ROWS_PER_NODE`). Purge results are
  recorded in the new `node_logs_summary` table (migration 037).

- **[S1-7] Autodeploy reimage-in-progress guard**
  Before restarting `clustr-serverd`, the autodeploy script queries
  `/api/v1/reimages` for non-terminal jobs (pending/triggered/in\_progress/
  running). If any are active the restart is deferred. Same fail-open
  pattern as the existing ISO build guard.

- **[S1-10] Readiness endpoint**
  `GET /api/v1/healthz/ready` returns 200 with structured JSON when the DB
  responds, boot dir exists, and initramfs is present; returns 503 with a
  per-check failure map otherwise. The existing `/health` liveness endpoint
  is unchanged.

- **[S1-11] `actorLabel` wired to reimage `requested_by`** [P1-BUG]
  `ReimageHandler.Create` now records `actorLabel(r.Context())` instead of
  the hardcoded string `"api"`. All new reimage records carry the
  authenticated key label or user ID. Retry records use `label + " (retry)"`.

- **[S1-12] `UpsertNodeByMAC` atomicity fix** [P1-BUG]
  The upsert now runs inside a serializable SQLite transaction
  (`BEGIN IMMEDIATE`), eliminating a TOCTOU race condition that could cause
  duplicate node registrations during concurrent PXE boot bursts.

### Operations / Developer Experience

- **[S1-6] Pagination on `/api/v1/nodes` and `/api/v1/images`**
  Both endpoints accept `?page=` and `?per_page=` (default 50, max 500).
  Responses include `page`, `per_page`, and `next_cursor` when pagination
  params are supplied. Calls without params return the full list as before
  (backward compatible).

- **[S1-9] Required directories created on first run**
  `clustr-serverd` now calls `os.MkdirAll` on DB dir, `CLUSTR_IMAGE_DIR`,
  `CLUSTR_BOOT_DIR`, `CLUSTR_TFTP_DIR`, and `CLUSTR_LOG_ARCHIVE_DIR` at
  startup. Fresh installs no longer panic with "no such file or directory".

- **[S1-13] Auth scope comment for `POST /deploy/progress`**
  Inline comment added to `server.go` explaining that `POST /deploy/progress`
  is deliberately outside the admin group (node-scoped key required) while
  the `GET` paths remain inside the admin group.

### Test Infrastructure

- **[S1-5] Test coverage for ldap, network, slurm, sysaccounts**
  - `internal/ldap/dit_test.go` — `entryToUser`, `serverNameFromURI`,
    DN helpers, `HashPasswordCrypt` (uniqueness, format, min length)
  - `internal/network/configgen_test.go` — 7 golden-file pairs: Arista
    (access ports, PFC, LAG), Juniper basic, generic fallback (unknown +
    empty vendor), default MTU injection, trunk port mode
  - `internal/network/configgen.go` — extracted `RenderSwitchTemplate`
    (pure function, no DB), fixed Arista template LAG member port loop
    (`$lag` variable, was `$.ID` which referenced wrong struct)
  - `internal/slurm/render_test.go` — `RenderConfig` golden tests for
    slurm.conf, gres.conf, plugstack passthrough, `overrideOrDefault`,
    map missingkey=zero behavior
  - `internal/sysaccounts/manager_test.go` — group/account CRUD, conflict
    detection, `EnsureDefaults`, `NodeConfig` nil/non-nil
  - `internal/deploy/sysaccounts_test.go` — `parseGetentName`, error types
  - `internal/secrets/secrets_test.go` — full encryption round-trip, nonce
    randomness, tamper detection, wrong key, `DeriveKey` determinism

---

## Sprint 2 — Image Factory + Tags Model (2026-05-11 → 2026-05-24)

### Image Factory

- **[S2-1] Unified `finalizeImageFromRootfs` helper**
  All five async build paths (`pullAsync`, `importISOAsync`, `captureAsync`,
  `buildFromISOAsync`, `resumeFinalize`) now delegate to a single
  `finalizeImageFromRootfs` method via a `finalizeSourceMetadata` struct.
  Eliminates duplicated disk-layout detection, scrub, tar, blob-path,
  FinalizeBaseImage, metadata-sidecar, and build-manifest logic.

### Data Model

- **[S2-4] `groups[]` → `tags[]` rename (API + DB)**
  Migration 041: `node_configs.groups` column renamed to `node_configs.tags`
  via `ALTER TABLE RENAME COLUMN` (SQLite 3.35+).
  `pkg/api.NodeConfig` gains a `Tags []string` field; `Groups` is retained as
  a deprecated alias. Both fields emitted in API responses for one release.
  DB layer writes/reads `tags` column; scanners back-fill `Groups = Tags`.

- **[S2-5] `node_group_memberships.is_primary` flag**
  Migration 042: adds `is_primary INTEGER NOT NULL DEFAULT 0` to
  `node_group_memberships` with a partial unique index ensuring at most one
  primary row per node. Back-fill promotes single-membership nodes and nodes
  whose `group_id` matches the historic fast-path column.
  `AddGroupMember` promotes the first membership to primary automatically.
  New `SetPrimaryGroupMember` function promotes a membership within a
  transaction.

- **[S2-10] Image status domain guard**
  Migration 043: documenting partial-index comment for valid `base_images.status`
  values. `db.UpdateBaseImageStatus` validates against `validImageStatuses` map
  before executing, returning a clear error on invalid transitions.

### API

- **[S2-3] Image tag filter: `GET /api/v1/images?tag=`**
  `ListBaseImages` accepts an optional `tag` parameter; uses SQLite JSON1
  `json_each()` to filter images whose `tags` array contains the value.
  
- **[S2-3] `PUT /api/v1/images/:id/tags` endpoint**
  New handler replaces the entire tags array atomically. Returns the updated
  image record. New `UpdateImageTags` DB method.

### Security / Validation

- **[S2-8] `sshpass` pre-flight check in capture handler**
  `POST /api/v1/factory/capture` checks `exec.LookPath("sshpass")` when
  `ssh_password` is non-empty. Returns 400 with a clear message before
  accepting the request if the binary is not installed on the server host.

- **[S2-9] `base_image_id` validation in `CreateNode`**
  `POST /api/v1/nodes` validates the `base_image_id` exists in the DB before
  INSERT. Returns 400 "image not found" instead of letting SQLite FK constraints
  produce a 500.

### UI

- **[S2-2] Image metadata displayed on detail page**
  Image detail page fetches `GET /api/v1/images/:id/metadata` in parallel with
  the image record. Kernel version, CUDA version (if present), build method,
  build timestamp, distro, and installed packages summary are displayed in a
  "Build Metadata" KV grid section.

- **[S2-3] Image tag editor on detail page + tag filter dropdown on list**
  Image detail page shows an interactive tag editor: existing tags as removable
  chips, inline "Add tag" input. Images grid gains a tag filter dropdown (all
  unique tags from loaded images); selecting a tag re-fetches with `?tag=`.

- **[S2-6] Node modal: Node Group dropdown**
  Add/Edit Node modal includes a "Node Group" `<select>` dropdown populated
  from `GET /api/v1/node-groups`. Optional; unset is valid.

- **[S2-7] Tags vs NodeGroup label fix**
  "Groups (comma-separated)" → "Tags" with tooltip: "Unstructured labels used
  for filtering and Slurm role assignment."
  "Node Group" with tooltip: "Primary operational group — controls disk layout
  inheritance, network profile, and group reimages."
  `submitNode` and `_tabSaveOverview` now send `tags` (with `groups` as
  backward-compat alias).

- **[S2-11] Stale initramfs dashboard indicator**
  Dashboard fetches initramfs info at load time. When the newest `ready` image
  was created after the initramfs `build_time`, a warning banner is shown:
  "Initramfs may be stale — rebuild before next PXE boot." Links to Images
  page.

- **[S2-13] Nav: `<details>` collapsibles replaced with flat section headers**
  `<details><summary>Slurm</summary>` and `<details><summary>LDAP</summary>`
  removed from `index.html`. Replaced with `<div class="nav-section-header">`
  elements using the same uppercased section label style. Nav items shown/
  hidden by JS at init time via existing `nav-slurm-managed` /
  `nav-ldap-managed` display toggles. No more flash of expanded collapsible on
  page transitions.

- **[S2-14] Replace `confirm()`/`alert()` with modal pattern**
  All `confirm()` and `alert()` calls in `app.js` replaced with
  `Pages.showConfirmModal()` and `Pages.showAlertModal()`. New shared modal
  utilities added to the `Pages` object. Destructive actions show a modal with
  a red "danger" confirm button. Automated Playwright/Puppeteer tests can now
  interact with confirmation dialogs without special browser flags.

---

## Sprint 3 — RBAC + Audit Trail (2026-05-25 → 2026-06-07)

### Access Control

- **[S3-1] 3-tier RBAC: admin / operator / readonly roles**
  Migration 043: `user_group_memberships(user_id, group_id, role)` table with
  FK cascade, PRIMARY KEY `(user_id, group_id)`, and indexes on both FKs.
  New DB helpers: `SetUserGroupMemberships`, `GetUserGroupMemberships`,
  `UserHasGroupAccess`, `ListAllUserGroupMemberships`, `GetGroupIDForNode`.
  `requireGroupAccess` and `requireGroupAccessByGroupID` middleware enforce
  group-scoped operator access on all mutation routes. Admin scope passes
  unconditionally; readonly and node-scoped keys always get 403.
  7 table-driven tests in `internal/server/rbac_test.go`.

- **[S3-2] UI permission gating for destructive actions**
  Reimage button, power controls, and Delete node are hidden in the nodes list
  and node detail when `Auth._role === 'readonly'`. Delete node and node
  management actions additionally hidden for `operator` role. Delete Image
  is admin-only. "Add Node" button hidden for non-admin. Power actions
  extracted to shared `_nodeRowActions` helper used by both initial render
  and auto-refresh.

- **[S3-3] Settings > Users: group membership assignment UI**
  Users table gains a "Group Memberships" column showing NodeGroup chips for
  operator accounts. An "Edit" button opens a modal with checkboxes for all
  defined NodeGroups. Save calls `PUT /api/v1/users/{id}/group-memberships`.
  API endpoints: `GET /api/v1/users/{id}/group-memberships` and
  `PUT /api/v1/users/{id}/group-memberships` (admin only).
  Users list response from `GET /admin/users` now includes `group_ids` array
  per user (back-filled via `ListAllUserGroupMemberships`).

### Audit Trail

- **[S3-4] `audit_log` table**
  Migration 044: `audit_log(id, actor_id, actor_label, action, resource_type,
  resource_id, old_value TEXT, new_value TEXT, ip_addr, created_at INTEGER)`.
  Indexes on `created_at DESC`, `actor_id`, `(resource_type, resource_id)`,
  `action`. 24 action constants defined (`AuditActionNode*`, `AuditActionImage*`,
  `AuditActionGroup*`, `AuditActionUser*`, `AuditActionAPIKey*`).
  `AuditService.Record()` is best-effort (errors logged, never propagated).

- **[S3-5] Audit calls wired to all mutation handlers**
  Nodes (create/update/delete), reimage (create), node groups (create/update/
  delete/reimage), images (create/delete), users (create/update/delete/
  reset-password, group-memberships) all record to the audit log.
  `GET /api/v1/audit` (admin only) supports filters: `since`, `until`,
  `actor`, `action`, `resource_type`, `limit`, `offset`.

### Operations / Reliability

- **[S3-7] Nodes list: hostname/MAC/status search**
  Search input in the nodes page header. `GET /api/v1/nodes?search=` performs
  a LIKE query on `hostname`, `primary_mac`, and `status`. Client-side debounce
  (300ms) updates sections in place without a full-page reload.

- **[S3-8] Scheduled reimage datetime picker**
  Reimage modal now includes an optional `datetime-local` input for
  `scheduled_at`. Empty = immediate. Non-empty schedules via the existing
  `scheduled_at` field in `POST /api/v1/nodes/{id}/reimage`. Toast confirms
  whether the reimage was queued or scheduled.

- **[S3-9] Disk space pre-flight at startup + periodic monitor**
  `clustr-serverd` checks available disk on `CLUSTR_IMAGE_DIR` at startup and
  every 15 minutes. Logs WARN at 80%, ERROR at 90%. At 95% the server logs
  FATAL and exits to prevent partial image writes corrupting the store.
  `diskUsagePct` uses `syscall.Statfs` (Linux-native, no cgo).

- **[S3-10] `CLUSTR_AUTH_DEV_MODE` loopback guard**
  If `CLUSTR_AUTH_DEV_MODE=1` and the listen address is not loopback
  (`127.x.x.x`, `::1`, `localhost`), the server refuses to start with a clear
  error message. Prevents accidentally running insecure dev mode on a
  network-accessible address.

### Session UX

- **[S3-6] Session expiry: 401 → `/login?next=` redirect**
  `api.js`'s `_redirectToLogin` now appends `?next=<encoded-hash>` so the
  user returns to the right page after re-authenticating. `login.js` reads
  `?next=` on successful login and restores the hash.

### Documentation

- **[S3-11] `docs/rbac.md`**
  Covers: 3-tier role model, permission matrix, group-scoped operator semantics,
  session vs API key auth, bootstrap admin flow, and migration story from
  single-tenant to multi-user. Implements decision D12 from `decisions.md`.

---

---

## Sprint 4 — Production Readiness (2026-06-08 → 2026-06-21)

### Ops / Backup

- **[S4-7] Backup: warn on captured images not backed up** (HA-4)
  `clustr-backup.sh` now queries the freshly-written DB backup for all
  `base_images` rows with `build_method = 'capture'` and status in
  `(ready, building, interrupted)`. For each captured image found, it
  emits an explicit WARNING to the journal:
  - If `CLUSTR_BACKUP_REMOTE` is unset: "Captured image [...] is not
    rebuildable from ISO cache and is NOT backed up off-site."
  - If the blob is missing from disk: "Captured image [...] blob is NOT
    found on disk — this image data may already be lost."
  - If a remote is configured: acknowledgement that the blob is present
    and being synced.
  Captured images are the only image type that cannot be rebuilt from ISO
  cache. Operators now learn of this risk at backup time, before data loss.

- **[S4-8] Autodeploy restore test — `clustr-backup-verify.timer`** (HA-3)
  New weekly systemd timer + service (`deploy/systemd/clustr-backup-verify.*`)
  that verifies backup integrity by performing an actual restore test:
  1. Finds the most recent `clustr-*.db` in `CLUSTR_BACKUP_DIR`.
  2. Copies it to a temp directory.
  3. Starts an ephemeral `clustr-serverd` instance (`CLUSTR_AUTH_DEV_MODE=1`,
     port `CLUSTR_BACKUP_VERIFY_PORT`, default 18080) against the temp DB.
  4. Hits `GET /api/v1/healthz/ready` (S1-10 readiness endpoint) and waits
     up to `CLUSTR_BACKUP_VERIFY_WAIT` seconds (default 30).
  5. Shuts down the instance and removes the temp dir.
  Pass/fail logged to journal via `logger -t clustr-backup-verify`.
  On failure: WARNING-priority journal entry emitted via both `logger -p
  daemon.warning` and `systemd-cat -p warning` — surfaces in
  `journalctl -p warning` monitoring queries.
  Timer fires every Sunday at 03:00 (1 hour after the daily backup at 02:00).
  Script lives at `scripts/ops/clustr-backup-verify.sh`; installed to
  `/usr/local/sbin/` by operators following `docs/install.md`.

### Documentation

- **[S4-14] `docs/install.md`** — full installation guide for first external operators.
  Covers 7 sections: prerequisites, network setup, Docker Compose path (primary),
  bare-metal/Ansible path (secondary), complete env var reference (all `CLUSTR_*`
  variables with defaults and override guidance), bootstrap admin account
  (default `clustr/clustr` credential + one-time API key capture), and a
  first-deploy smoke test (build image → register node → trigger reimage →
  PXE boot → verify `verified_booted` status). Includes troubleshooting table
  for the 5 most common first-deploy failure modes. Linked from `README.md`
  Installation section.

### Observability

- **[S4-1] Prometheus metrics endpoint: `GET /metrics`**
  New `internal/metrics` package registers 8 Prometheus metrics via `promauto`:
  `clustr_active_deploys` (gauge), `clustr_deploy_total{status}` (counter vec),
  `clustr_api_requests_total{endpoint,status,method}` (counter vec),
  `clustr_db_size_bytes` (gauge), `clustr_image_disk_bytes` (gauge),
  `clustr_nodes{state}` (gauge vec), `clustr_flip_back_failures` (counter),
  `clustr_webhook_deliveries_total{event,status}` (counter vec).
  `GET /metrics` served by `promhttp.Handler()` without authentication.
  `runMetricsCollector` goroutine ticks every 30 s to update gauges (node
  counts by state, active deploys, DB file size, image dir total bytes).
  `endpointLabel()` in middleware coarsens path labels to prevent cardinality
  explosion from node/image IDs. All node states pre-seeded to 0 at startup.
  `go.mod` adds `github.com/prometheus/client_golang v1.23.2`.

- **[S4-9] Flip-back failure counter in health endpoint**
  `GET /api/v1/health` now includes `flip_back_failures` (omitted when 0).
  Incremented atomically each time `flipNodeToDiskFirst` returns an error in
  the verify-boot scanner goroutine. Wired to `metrics.FlipBackFailures` (S4-1)
  and to a local `int64` counter on the server struct for the health response.

### Integrations

- **[S4-2] Outbound webhooks: delivery pipeline**
  New `internal/webhook` package: `Dispatcher.Dispatch(ctx, event, payload)`
  fans out to all active subscriptions for the event in separate goroutines.
  `deliver()` retries up to 3 times with exponential backoff (1 s → 2 s → 4 s).
  Each delivery attempt recorded in `webhook_deliveries` table.
  `post()` signs request bodies with HMAC-SHA256 (`X-Clustr-Signature: sha256=…`).
  Events: `deploy.complete`, `deploy.failed`, `verify_boot.timeout`, `image.ready`.
  Migration 045: `webhook_subscriptions` + `webhook_deliveries` tables.
  Admin CRUD: `GET/POST /admin/webhooks`, `GET/PUT/DELETE /admin/webhooks/{id}`,
  `GET /admin/webhooks/{id}/deliveries`.

### Background Workers / Reliability

- **[S4-3] Orphan reimage_pending reaper**
  `runReimagePendingReaper` goroutine runs at startup then hourly. Finds all
  nodes with `reimage_pending = 1` that have no active (non-terminal) reimage
  request. Clears `reimage_pending` to prevent nodes getting stuck in a PXE
  boot loop after a server crash between `SetNextBoot(PXE)` and the DB write.

- **[S4-4] Crash-recovery: resume running group reimage jobs on startup**
  `resumeRunningGroupReimageJobs` runs at startup. Finds all
  `group_reimage_jobs` with `status = 'running'` from before the last process
  exit. Re-dispatches remaining (not-yet-triggered) nodes via the orchestrator.
  New `Orchestrator.ResumeGroupReimageJob(ctx, jobID)` method.

- **[S4-6] Bounded-concurrency verify-boot scanner**
  `runVerifyTimeoutScanner` now fans out `flipNodeToDiskFirst` calls via a
  bounded semaphore (buffered channel). Default concurrency: 5, configurable
  via `CLUSTR_FLIP_CONCURRENCY`. Prevents a large batch of simultaneous
  verify-boot timeouts from creating O(n) concurrent Proxmox API calls.
  Fires `verify_boot.timeout` webhook on each timeout detection (S4-2).

- **[S4-10] `DELETE /api/v1/nodes/{id}/reimage/active`**
  Cancels the active (non-terminal) reimage for a node by node ID without
  requiring the reimage request ID. Returns the cancelled record or 404 if
  no active reimage exists.

### Deploy UX

- **[S4-5] Proxmox custom CA cert support**
  `power_provider.proxmox` config accepts new optional field `tls_ca_cert_path`.
  When set, the Proxmox HTTP client builds a cert pool from the PEM file and
  uses it as the TLS root of trust. When `insecure = true` (and no CA path),
  the client logs a WARN and uses `InsecureSkipVerify`. Default: system pool.
  Eliminates the `insecure = true` footgun for orgs with private PKI.
  UI: `tls_ca_cert_path` input added to both the node create/edit modal and
  the inline BMC tab Proxmox fields section.

- **[S4-11] Per-deployment `inject_vars` custom_vars override**
  `POST /api/v1/nodes/{id}/reimage` accepts optional `inject_vars: {k: v}`.
  Migration 046: `reimage_requests.inject_vars TEXT NOT NULL DEFAULT '{}'`.
  At `POST /api/v1/nodes/register`, when `action = deploy` and
  `reimage_pending = true`, `GetInjectVarsForActiveReimage` merges the stored
  inject_vars into `NodeConfig.CustomVars` before returning to the deploy agent.
  inject_vars keys win on collision. Not persisted to `node_configs` — ephemeral
  for this deployment only. Allows per-job parameter injection without editing
  the node's persistent config.

### UI

- **[S4-12] Remove duplicate Delete button from node detail header**
  The standalone `<button class="btn btn-danger btn-sm">Delete</button>` in the
  node detail page header has been removed. Delete node is available exclusively
  from the Actions dropdown ("Delete node" in the danger zone), eliminating
  the redundant and potentially confusing second button.

- **[S4-13] Server Info tab: live data replaces placeholder**
  Settings → Server Info now shows: version, commit SHA, build time, total
  registered node count, flip-back failure count, and the `/metrics` endpoint
  URL. Data fetched from `GET /api/v1/health` + `GET /api/v1/nodes` in
  parallel. A warning banner appears if `flip_back_failures > 0`.
