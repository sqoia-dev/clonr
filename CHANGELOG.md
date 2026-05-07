# Changelog

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
