# Changelog

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
