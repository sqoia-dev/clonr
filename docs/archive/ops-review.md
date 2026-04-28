# clustr Ops Review
**Date:** 2026-04-25
**Reviewer:** Gilfoyle (Infra / Platform / Security)
**Scope:** Build, deployment pipeline, security posture, observability, HA/failure modes, operator ergonomics, compliance, production-readiness gaps
**Feed:** 90-day sprint plan input

---

## 1. Current State (what works today, ops-wise)

The dev deployment on cloner (192.168.1.151, Rocky Linux 9) is running and serving a live single-node lab cluster. The operational surface that is functional today:

**Deployment pipeline**: `clustr-autodeploy.timer` pulls `origin/main` every 2 minutes, builds all three binaries (`clustr-serverd`, `clustr` static, `clustr-clientd`), rebuilds the initramfs, performs an atomic binary replace (stage to `.autodeploy-new`, then `mv`), hot-restarts `clustr-serverd`, and runs a 30-second health check against `/api/v1/nodes`. Build-in-progress guard queries `/api/v1/images` before restarting so active ISO builds are not killed mid-run. The entire loop is journal-logged under `clustr-autodeploy.service`.

**Backup**: `clustr-backup.timer` fires at 02:00 daily. Uses `sqlite3 .backup` (WAL-safe hot backup), 14-day retention on DB snapshots, 30-day ISO cache rsync mirror. A restore script (`clustr-restore.sh`) stops the service, validates the backup integrity, preserves the live DB, restores, and verifies post-restart health. This is production-grade for a single-host backup story.

**Authentication**: API key auth (SHA-256 hash of bare entropy in `api_keys` table, never stored plaintext), node-scoped keys bound to specific node IDs with `requireNodeOwnership` middleware, session cookie auth (HMAC-SHA256 stateless tokens, 12-hour TTL with 30-minute sliding window). The middleware stack correctly enforces scope hierarchy: `admin` > `operator` > `readonly` > `node`. `requireImageAccess` middleware locks each node to only its assigned image during blob downloads.

**PXE chain**: iPXE EFI binary is committed with SHA-256 checksum. Boot scripts are dynamically generated per MAC address at `/api/v1/boot/ipxe`. Node-scoped tokens are embedded in the kernel cmdline at PXE-serve time. The initramfs refuses to run without a `clustr.token` parameter and halts with an explicit error log rather than silently deploying unauthenticated.

**Initramfs build**: Reproducible on the server itself (local-mode SSH shim), binary coverage validation checks every command the init script calls before packaging, mknod device nodes pre-created, cpio sorted for deterministic archive ordering.

**Service hardening**: systemd unit defines `CapabilityBoundingSet` restricting to exactly the 11 capabilities needed (documented per-capability), `DeviceAllow` restricts to `/dev/kvm`, loop devices, and `/dev/net/tun`. `OOMScoreAdjust=-500` protects the control plane under memory pressure. `MemoryMax=8G` bounds total memory for QEMU-co-located builds.

**DB**: SQLite with WAL journal, foreign key enforcement, `_busy_timeout=5000` to queue concurrent writers. Background `lastUsedFlusher` batches API key `last_used_at` writes in 30-second windows to avoid per-request write amplification. Startup reconciliation marks stuck builds as interrupted/resumable on process restart.

**Log purger**: `runLogPurger` background goroutine, configurable retention via `CLUSTR_LOG_RETENTION` (default 14 days), purges on a 60-minute tick, logs purged row count.

**SSH hardening config**: `deploy/ssh/40-clustr-hardening.conf` is deployed to `/etc/ssh/sshd_config.d/`, enforces key-only auth, no root password login, `MaxAuthTries=3`, `LoginGraceTime=20`.

**CI**: `ci.yml` runs `go vet` + `go test` + static link verification on every push and PR. `release.yml` gates on test pass before any cross-compile. `docker.yml` produces multi-arch (`linux/amd64`, `linux/arm64`) container image on version tags. GHA layer cache is configured. Initramfs is built and attached to releases via `initramfs.yml`.

---

## 2. Security Posture Summary

### P0 — Exploit path or immediate data exposure

**SEC-P0-1: Three credential classes stored plaintext in SQLite DB with no encryption at rest**

Affected columns and locations:
- `ldap_module_config.service_bind_password` (migration `027_ldap_module.sql:30`) — the LDAP service bind account password is stored verbatim in the DB row. Explicitly noted in the migration comment as a "V2 hardening item."
- `ldap_module_config.admin_passwd` (migration `028_ldap_admin_passwd.sql:11`) — LDAP Directory Manager password, added to persist across restarts. Migration comment reads: "identical to service_bind_password (plaintext, file-permission protected)."
- `node_configs.bmc_config` (migration `005_bmc_ib_config.sql`) — BMC/IPMI and Proxmox power provider credentials are serialised as JSON into this TEXT column. For Proxmox nodes this includes the API password or token secret. For IPMI nodes it includes the BMC password. These are readable to any process that can open the SQLite file.

The DB file (`/var/lib/clustr/db/clustr.db`) is owned by root but has mode 644 per the restore script (`chmod 644` at line 109 of `clustr-restore.sh`). Any local process running as a non-root user with read access to `/var/lib/clustr/` can extract all BMC passwords, LDAP credentials, and Proxmox API tokens.

The Slurm munge key has its own AES-256-GCM encryption via `slurm/deps.go` keyed on `CLUSTR_SECRET_KEY`. However, `CLUSTR_SECRET_KEY` defaults to `sha256("clustr-slurm-secrets-v1")` when the env var is not set, meaning the munge key is encrypted with a publicly known key unless the operator explicitly sets `CLUSTR_SECRET_KEY`. The systemd unit does not set this variable.

**Immediate action required**: Set DB file permissions to 600 (root-only read). Add `CLUSTR_SECRET_KEY` to the `secrets.env` pattern already used for `CLUSTR_SESSION_SECRET`. Ticket the LDAP and BMC credential encryption work.

**SEC-P0-2: clustr-serverd binds to `0.0.0.0:8080` in the production systemd unit**

`clustr-serverd.service` sets `CLUSTR_LISTEN_ADDR=0.0.0.0:8080`. The comment on line 19 says `# Bind to provisioning interface only in production: CLUSTR_LISTEN_ADDR=10.99.0.1:8080` but the live value is all interfaces. In a typical rack deployment the management/provisioning interface and the campus or data-center network are on the same host (different interfaces or VLANs). Binding to `0.0.0.0` exposes the clustr UI, API, DHCP (port 67/udp), and TFTP (port 69/udp) to all reachable networks unless an external firewall blocks them.

On cloner today the firewall posture is unknown from this review. For a production HPC controller this is a P0 network exposure: DHCP on all interfaces will respond to DHCP Discovers from any network the host is connected to and can hijack addresses on non-provisioning networks.

**Immediate action**: Change to `CLUSTR_LISTEN_ADDR=<provisioning-interface-IP>:8080` and bind DHCP/TFTP to the provisioning interface via `CLUSTR_PXE_INTERFACE`. Document this as the required production configuration.

**SEC-P0-3: iPXE EFI binary in `deploy/pxe/` and `internal/bootassets/` is not reproducibly built from source and is not code-signed**

The committed `ipxe.efi` binary (SHA-256: `868aa34057ff416ebf2fdfb5781de035e2c540477c04039198a9f8a9c6130034`) is a prebuilt binary from an unverified build environment. `deploy/pxe/BUILD.md` documents that this binary does NOT have `COLOUR_CMD` compiled in. There is no CI step that rebuilds it from source and verifies the checksum. There is no Secure Boot signing.

For HPC clusters that have Secure Boot enabled (increasingly common at federal labs under NIST SP 800-147), an unsigned iPXE binary will fail to boot without adding a custom MOK. For clusters without Secure Boot, the unsigned binary is boot-chain-complete but the binary provenance is unverifiable.

There is no workflow that rebuilds `ipxe.efi` from the pinned source tag and attaches it to a release. The binary sitting in `internal/bootassets/` is embedded in the server binary at compile time — its contents are baked into every `clustr-serverd` build without re-verification.

### P1 — Hardening gaps, not immediately exploitable in a locked-down environment

**SEC-P1-1: `StrictHostKeyChecking=no` in the embedded initramfs build script**

`internal/server/handlers/scripts/build-initramfs.sh` (the server-side version) uses `sshpass -p ... ssh -o StrictHostKeyChecking=no` on all three SSH/SCP invocations (lines 77, 87, 97). This means the initramfs build that runs in response to an operator clicking "Build Initramfs" in the UI does not verify the SSH host key of the source server. In the current single-host topology the target is `127.0.0.1`, so MITM is not a practical concern. But the pattern will survive copy/paste into a multi-host deployment.

The outer `scripts/build-initramfs.sh` (the operator-facing script) uses `StrictHostKeyChecking=accept-new`, which trusts on first connect but then verifies. This is marginally better. Both should be `StrictHostKeyChecking=yes` with the host key pre-populated in a known_hosts file.

**SEC-P1-2: `InsecureSkipVerify: true` in the Proxmox power provider**

`internal/power/proxmox/provider.go:100` sets `TLSClientConfig: &tls.Config{InsecureSkipVerify: true}` on the Proxmox API HTTP client. Annotated with `//nolint:gosec`. This means BMC power operations (on, off, cycle, reset) against Proxmox do not verify the PVE host's TLS certificate. An attacker with network access to the provisioning network who can MITM the Proxmox API could reroute power-cycle commands to arbitrary VMs.

For homelabs with self-signed PVE certificates this is a pragmatic trade-off. For production, the Proxmox provider should accept a configurable CA certificate path or an option to skip only in explicitly-dev mode.

**SEC-P1-3: `CLUSTR_SECRET_KEY` is not in the systemd unit or documented in the operator install path**

Slurm munge key encryption uses `CLUSTR_SECRET_KEY`. If not set, the key falls back to a static SHA-256 of a known string. The systemd unit file references `CLUSTR_SECRET_MASTER_KEY_PATH=/etc/clustr/secret-master.key` (for a different, currently unused master key feature) but does not reference `CLUSTR_SECRET_KEY`. The operator install documentation does not mention this variable. Any production deployment that sets up Slurm without explicitly configuring `CLUSTR_SECRET_KEY` has a munge key encrypted with a publicly known key.

**SEC-P1-4: Dropbear SSH per-boot password logged at INFO level in initramfs**

When `clustr.ssh=1` is passed on the kernel cmdline, `initramfs-init.sh` logs the plaintext SSH password at INFO level: `log " password : $SSH_PASS"` (line 297). This log line goes to `/tmp/init.log` and is served over port 9999 (busybox httpd, no auth). Any host on the provisioning network can retrieve the password via `curl http://<node-ip>:9999/init.log`.

The per-boot password defaults to `clustrdev` if `clustr.ssh.pass=` is not provided on the cmdline. The log server on port 9999 is always started regardless of `clustr.ssh=` setting, so the log (containing the password when SSH is enabled) is always accessible. This is a provisioning-network-internal exposure — anyone on the provisioning subnet can SSH into any node mid-deploy using the default password.

For the majority of deployments this is acceptable (operators use this feature to debug stuck deploys), but it should be documented as a security consideration and the default password should be per-deploy randomized rather than static `clustrdev`.

**SEC-P1-5: DB file permissions set to 644 in `clustr-restore.sh`**

`scripts/ops/clustr-restore.sh:109` runs `chmod 644 "${DB_PATH}"` after restoring. This makes the DB world-readable. Combined with SEC-P0-1, any unprivileged local user can read all BMC credentials, LDAP passwords, and API key hashes. The correct permissions for this file are 600 (root read/write only).

**SEC-P1-6: No rate limiting on the API outside of the log ingest endpoint**

`handlers/logs.go` has a 100 req/s per-node rate limiter on `POST /api/v1/logs`. No other endpoint has rate limiting. The blob download endpoint (`GET /api/v1/images/{id}/blob`) streams large files and has no per-IP or per-key rate limit. A misconfigured or hostile node-scoped key could saturate the server's disk read bandwidth or the provisioning network. The reimage trigger endpoint has no burst protection — nothing prevents submitting 200 simultaneous reimage requests.

**SEC-P1-7: No HTTP server timeouts set on `http.Server`**

`server.go:152` creates `&http.Server{Addr: cfg.ListenAddr, Handler: s.router}` with no `ReadTimeout`, `WriteTimeout`, `ReadHeaderTimeout`, or `IdleTimeout` fields. This means slow clients can hold connections open indefinitely. In a provisioning environment where nodes phone home during deploy, a large number of stale connections from failed deploys accumulates without bound. The effective limit is the OS `ulimit -n` for the process.

### P2 — Security polish

**SEC-P2-1: `CLUSTR_AUTH_DEV_MODE=1` env var bypasses all authentication**

This is correctly documented as a dev-only escape hatch and logs a WARNING at startup. However, there is no check that prevents it from being set in the production systemd unit by accident. A deploy mistake that adds this variable to the environment file silently disables all authentication with no runtime indicator beyond a log line. A startup assertion that refuses to serve if `CLUSTR_AUTH_DEV_MODE=1` AND the server is listening on a non-loopback address would catch this class of misconfiguration.

**SEC-P2-2: Bearer token in WebSocket `?token=` query parameter**

`extractBearerToken` falls back to `r.URL.Query().Get("token")` for WebSocket compatibility. Query parameters are logged by most HTTP reverse proxies and access log aggregators in plaintext. Any operator who puts a reverse proxy (nginx, Caddy) in front of clustr will log API keys to the access log unless they specifically mask that query parameter. Shell session WebSocket upgrades go through this path.

**SEC-P2-3: Compiled binaries committed to root of git repo**

`clustr`, `clustr-serverd`, and `clustr-static` are tracked by git at the repo root (confirmed by `git ls-files`). `.gitignore` excludes `/bin/` but not the root-level binaries or their renamed counterparts. These binaries are committed at specific versions (Apr 18 timestamps based on `ls -la`) and diverge from the current source tree after any subsequent commits. This creates ambiguity about which binary is "current" and means the repo contains large binary blobs in git history that cannot be undone without a history rewrite. It also means `git diff` and `git status` are noisy with binary changes.

**SEC-P2-4: lab-validate workflow stores SSH keys as GitHub secrets but uses `StrictHostKeyChecking=no`**

`scripts/ci/lab-validate.sh` uses `ssh -o StrictHostKeyChecking=no` for all lab SSH connections. If the `LAB_PROXMOX_SSH_KEY` or `LAB_SERVER_SSH_KEY` secrets were leaked, they would be usable against any SSH server without host verification. The workflow should pre-populate known_hosts via `ssh-keyscan` at setup time and then use `StrictHostKeyChecking=yes`.

---

## 3. HA / Failure-Mode Analysis

### Single Points of Failure

| Component | SPOF? | Impact when failed | Current mitigation |
|---|---|---|---|
| clustr-serverd process | Yes | No new deploys, PXE nodes boot loop | `Restart=on-failure`, `RestartSec=5s` |
| SQLite DB | Yes | Complete data loss; no HA path | Daily backup via `clustr-backup.timer`, restore script |
| `/var/lib/clustr/` XFS volume | Yes | All images, DB, boot files lost | ISO cache rsync backup (local-only unless `CLUSTR_BACKUP_REMOTE` set); image blobs not backed up |
| cloner host (192.168.1.151) | Yes | Total service outage | None — single physical VM |
| Git origin (`github.com/sqoia-dev/clustr`) | Yes for autodeploy | Autodeploy stalls; binary in place continues serving | Script exits cleanly on `git fetch` failure, leaves prior binary |
| Network path to github | Yes for autodeploy | Same as above | Same |

### Failure Modes: Mid-Deploy Server Restart

`clustr-serverd` is restarted during an active node deploy. The autodeploy script checks `/api/v1/images` for `status=building` before restarting but does NOT check for in-flight reimage operations. A node that is mid-rsync or mid-partitioning when `SIGTERM` is sent to `clustr-serverd` will:
1. Lose the active HTTP connection to `clustr-serverd` from the initramfs.
2. The `clustr deploy` command in the initramfs will retry the API connection but after the server restarts all in-flight deploy state is gone.
3. The node will be stuck in a half-written disk state. An operator must manually trigger a reimage.

The autodeploy script only guards against ISO builds (QEMU processes). The gap is: no check for active PXE-deploy sessions (`clustr-clientd` heartbeats or in-progress reimage records).

**Mitigation needed**: Before restarting, also check for any reimage records in non-terminal states (i.e., `status != 'completed' AND status != 'failed'`). If any are active, defer restart the same way ISO builds are deferred.

### Failure Mode: Disk Full on clustr Host

`/var/lib/clustr/` holds the DB, image blobs, ISO cache, boot files, and TMPDIR for QEMU builds. There is no disk full detection or pre-emptive quota enforcement. When the volume fills:
- SQLite WAL writes fail → `clustr-serverd` crashes or returns 500 on all write paths
- QEMU ISO build writes to TMPDIR fail mid-build → image marked `interrupted`
- New initramfs builds fail silently or partially
- Backup script fails silently (no space for backup file)

The systemd unit sets `MemoryMax=8G` but there is no `LimitFSIZE` or disk quota. No dashboard metric or alert exists for disk utilization.

A 100GB XFS volume at 192.168.1.151 is in use (`/dev/sda1`). At 200 nodes with 5GB images each, the image directory alone consumes 1TB. The current volume is undersized for that scale.

### Failure Mode: clustr-serverd Cannot Reach Nodes (Network Partition)

If the provisioning network becomes unreachable:
- `GET /api/v1/nodes/{id}/power` will return stale cached power status (the `PowerCache` in `server/powercache.go` has a TTL but the staleness indicator is not surfaced to the operator)
- In-flight deploys on nodes lose their HTTP connection to the server and enter retry loops
- The verify-boot scanner continues to tick and will mark nodes as `verify_timeout` if they were mid-deploy when the partition started, even if the node completed successfully but could not report back
- IPMI/BMC operations (power cycle, SOL) are direct node-to-BMC calls, so they are unaffected

### Failure Mode: DHCP Conflict

If another DHCP server exists on the provisioning network, nodes may receive IPs from the wrong server and fail to reach `clustr-serverd` for PXE. The clustr DHCP server does not log or detect competing DHCP responses. There is no DHCP server isolation check at startup.

### Autodeploy Failure Modes

The 2-minute polling autodeploy has several documented gaps:

1. **Lost mid-restart deploys** (described above).
2. **No rollback on health check failure**: If the health check fails after the binary is replaced, the new (broken) binary is already in place. The script exits non-zero and the timer retries in 2 minutes, but the broken binary is serving (or crashing). There is no automatic rollback to the previous binary. A second binary should be kept as `.prev` and restored on health failure.
3. **Initramfs build failure does not block server restart**: If the initramfs build fails (e.g., SSH to localhost fails, a kernel module is missing), the script logs a WARNING, keeps the old initramfs, and proceeds to restart the server. This is intentional (fail-open) but means a new binary may serve different API semantics while the initramfs still has an old binary. This creates a version skew window.
4. **`REMOTE_SHA` vs `LOCAL_SHA` on failed restart**: After `git reset --hard`, `LOCAL_SHA == REMOTE_SHA`. If the restart fails and the next cycle runs, the SHA check passes immediately and the cycle exits early — the broken binary is never retried. This is a logic gap in the health-failure path.

### SQLite Single-Writer Constraint

`sqlDB.SetMaxOpenConns(1)` serialises all DB writes. Under load (200 nodes, concurrent deploys, log ingest at 100 req/s per node) this becomes a write bottleneck. Reads are unaffected by WAL mode (readers don't block writers), but the single writer constraint means bursts of simultaneous deploy-complete callbacks, heartbeats, and log ingest compete for the single write slot and queue behind `_busy_timeout=5000`.

---

## 4. Observability Gaps

**OBS-1: No Prometheus metrics endpoint**

`clustr-serverd` emits structured zerolog logs but exposes no `/metrics` endpoint. There is no way to integrate clustr into an existing Prometheus/Grafana monitoring stack. For a 200-node production cluster, operators expect to graph: active deploy count, deploy success/failure rate, image build duration, DHCP lease pool utilization, API request latency by endpoint, DB write queue depth.

**OBS-2: `/health` is liveness-only — no readiness distinction**

`GET /api/v1/health` returns `{"status":"ok","version":...}` regardless of whether the DB is healthy, the DHCP server is bound, or the TFTP server is running. There is no distinction between "the process is up" (liveness) and "the process is ready to serve deploys" (readiness). A load balancer or uptime monitor that checks `/health` will report the service as up even when the DB connection is broken or the PXE subsystem failed to bind.

**OBS-3: No structured audit log separate from application log**

All log output (INFO requests, WARN, ERROR, and audit events) goes to a single zerolog stream to the journal. There is no dedicated audit trail (who triggered what action and when) that can be shipped to a SIEM or retained independently of application debug logs. The `actorLabel` function in `middleware.go` builds a user attribution string but it's embedded in the same log stream as `"message": "request"` lines.

**OBS-4: Alerting is entirely absent**

No built-in alerting exists. No webhook notification on deploy failure, verify-boot timeout, disk pressure, or service health. The Uptime Kuma instance at `:3001` on the Linode monitors the tunnl.sh and sqoia.dev static sites; it does not monitor clustr on the cloner host.

**OBS-5: No external uptime monitoring for the clustr endpoint**

The cloner dev instance at `192.168.1.151:8080` has no external uptime monitoring. A developer pushing a bad binary that crashes `clustr-serverd` will not know until a node deploy fails or someone notices manually.

**OBS-6: Log retention is configurable but the default is silent**

`CLUSTR_LOG_RETENTION=0` (the default when env var is unset) causes `parseLogRetention()` to return `0`. In `server.go`, a `0` duration is treated as "use server default" (14 days). This is correct but non-obvious — an operator reading the config expects `0` to mean "no retention limit" or "immediate purge". The default should be explicit in the systemd unit.

---

## 5. Build, Packaging, and Distribution Gaps

**BUILD-1: Makefile has stale binary names (`clonr` instead of `clustr`)**

`Makefile` targets reference `./cmd/clonr` and `./cmd/clonr-serverd` with output paths `bin/clonr` and `bin/clonr-serverd`. The project was renamed from `clonr` to `clustr`. The `Makefile` was not updated. `make all` will fail because `cmd/clonr/` does not exist. The correct paths are `./cmd/clustr` and `./cmd/clustr-serverd`. The CI workflows use the correct paths; only the local development `Makefile` is broken.

**BUILD-2: Dockerfile references stale binary name and hardcodes Go 1.25 without a stable base tag**

`Dockerfile` uses `FROM golang:1.25-alpine AS builder` and builds `./cmd/clonr-serverd`. Go 1.25 is the same version referenced in the CI workflows (currently valid as of April 2026). The build command `./cmd/clonr-serverd` will fail if the directory no longer exists. The `VOLUME ["/var/lib/clonr"]` instruction uses the old name. The container image is tagged as `ghcr.io/sqoia-dev/clustr-server` (correct) but the binary inside will be at `/usr/local/bin/clonr-serverd` (incorrect after rename).

**BUILD-3: Committed binaries at repo root diverge from source**

`clustr`, `clustr-serverd`, and `clustr-static` at the repo root are tracked by git. They are from April 18 builds and have diverged from current source. They consume ~45MB of git history and create confusion about which binary is "current." They should be removed from tracking with `git rm` and added to `.gitignore` (the `.gitignore` already excludes `/bin/` but not root-level names that are not prefixed with `clonr`).

**BUILD-4: `initramfs.yml` does not gate on `ci.yml` success**

The `initramfs.yml` release workflow runs in parallel with `release.yml` on tag push. Neither workflow has a `needs:` dependency on the other. There is no requirement that `go test` passes before the initramfs is attached to a release. If a tag is pushed with a test failure, the initramfs can be published containing a broken binary.

**BUILD-5: `lab-validate.yml` is `workflow_dispatch` only and not gating releases**

The lab validation workflow is intentionally not wired to release tags pending UEFI path stabilization. This is documented in the workflow file. Until it is wired, every tagged release ships without a lab-gate. The `docs/boot-architecture.md` confirms the UEFI boot path is now fixed and verified on vm202. This is the appropriate time to wire the lab gate.

**BUILD-6: No Helm chart, operator manifest, or installable package**

The release artifacts are: a static `clustr` client binary, a `clustr-serverd` server binary, and an initramfs image. There is no Docker Compose file, no Helm chart, no RPM/DEB, no Ansible role for initial provisioning. An operator installing clustr today must: download 3 artifacts, manually create systemd units, create `/var/lib/clustr/` directory structure, generate a session secret, run a kickstart or manual SSH install. The `scripts/setup/install-dev-vm.sh` and `scripts/kickstart-clustr-server.cfg` exist but are not linked from the README install path.

---

## 6. High Availability and Production-Readiness Gaps

**HA-1: No migration path for IPv6 or dual-stack provisioning networks**

PXE config assumes IPv4. `CLUSTR_PXE_RANGE`, `CLUSTR_PXE_SERVER_IP`, and the iPXE script generation are IPv4-only. Federal HPC environments increasingly mandate IPv6 (DoD STIG). This is a pre-sales blocker for federal customers.

**HA-2: No volume health check or pre-flight disk space assertion**

At startup, `clustr-serverd` does not check the available disk space on `CLUSTR_IMAGE_DIR` or `TMPDIR`. An operator can start the service on a nearly-full volume and will only discover the problem when an ISO build or image write fails mid-operation.

**HA-3: No automated restore test**

The backup script creates daily snapshots but there is no automated restore test. The backup integrity check (`sqlite3 .backup` + `.tables` verification) validates that the backup is a valid SQLite file but does not verify that the restored DB produces a healthy clustr instance. A periodic `clustr-restore.sh` test against a temp path with service-health verification would close this gap.

**HA-4: Image blobs are not included in off-site backup by default**

`clustr-backup.sh` backs up the DB, the ISO cache, and an image inventory (names + sizes). The image blobs themselves (the actual rootfs tarballs under `CLUSTR_IMAGE_DIR`) are NOT backed up — the script documents this as intentional ("blobs rebuild from ISO cache"). However, a custom-captured image (built via "Capture from Host") is not rebuildable from an ISO. If the host disk fails and a captured production image is lost, it cannot be recovered. The backup script should detect captured images and either include them in backup or warn explicitly.

**HA-5: No inter-service TLS on the provisioning network**

Communication between nodes (during deploy) and `clustr-serverd` uses plain HTTP (`http://10.99.0.1:8080`). The initramfs `CLUSTR_SERVER` variable defaults to `http://` and the build script comments note: "For environments where provisioning network is not fully trusted, configure TLS." There is no documentation on how to configure this, no `docs/tls-provisioning.md` file (referenced in `build-initramfs.sh` but missing), and no certificate provisioning step in the install path.

For an air-gapped HPC cluster where the provisioning network is physically isolated, this is acceptable. For any environment where the provisioning network is shared VLAN, an eavesdropper can capture API keys and node tokens from HTTP traffic.

---

## 7. Operator Ergonomics Gaps

**OPS-1: No operator upgrade path documented**

There is no `UPGRADING.md` or upgrade section in the README. The autodeploy script handles upgrades for the dev deployment. But an operator running clustr from a tarball or Docker image has no guidance on: how to apply migrations (migrations run automatically at startup — good, but undocumented), whether a restart is required, whether existing sessions are invalidated (yes, on session secret rotation), or what changed between versions.

**OPS-2: No migration rollback path**

SQLite migrations are apply-only (forward-only). There is no migration rollback tooling. If a bad migration is pushed, the only recovery path is to restore from the pre-migration DB backup. This is adequate if daily backups are running and the migration was caught within 24 hours.

**OPS-3: Directory structure not created by the binary**

On first run with a fresh install, `clustr-serverd` will panic or fail if `/var/lib/clustr/db/`, `/var/lib/clustr/images/`, `/var/lib/clustr/boot/`, `/var/lib/clustr/tftpboot/`, and `/var/lib/clustr/tmp/` do not exist. The service unit does not create them. The install documentation (or a `--setup` flag) should create this structure.

**OPS-4: No capacity planning signal**

An operator cannot answer "how close am I to hitting node limits?" or "how much disk does each image consume?" from the UI or API. There is no `/api/v1/stats` endpoint returning node count, image count, active deploy count, DB size, disk utilization, or DHCP pool exhaustion.

**OPS-5: No debug tooling for failed deploys beyond "check logs"**

When a deploy fails, the operator's workflow is: go to node detail → Logs tab → scroll. There is no `clustr debug <node-id>` command that summarizes the last deploy attempt (phase, exit code, log tail, hardware profile mismatch warnings). The `clustr-static` binary embedded in the initramfs could serve this role since it already implements the deploy client, but no diagnostic subcommand exists.

---

## 8. Compliance and Audit

**AUDIT-1: Reimage `requested_by` is hardcoded to `"api"` for all reimage requests**

`internal/server/handlers/reimage.go:108` sets `RequestedBy: "api"` regardless of which user or API key triggered the reimage. The `actorLabel` function in middleware correctly attributes actions to session users and key labels, but this information is not plumbed through to the reimage record. An audit trail showing "sysadmin-alice triggered reimage of node-047 at 14:32" is not possible with the current code.

This is independently noted as P1-9 in the webui review.

**AUDIT-2: Node config changes have no event log**

When a node's configuration changes (image assignment, kernel args, disk layout, BMC credentials), only `updated_at` is updated. There is no `node_config_history` table or change log. An auditor cannot determine: who changed the boot image from `rocky-9-base` to `rocky-9-cuda` and when.

**AUDIT-3: No image deployment provenance record**

When a node is deployed with an image, the reimage record captures the image ID and timestamp. But there is no verification of image integrity at deploy time in the audit record. The `X-Clustr-Blob-SHA256` header is set when serving the blob and the client verifies it, but the result of that verification is not written back to the server audit trail. An auditor cannot confirm: "when node-047 was deployed on 2026-03-15, the image SHA-256 was verified as X."

**AUDIT-4: No LDAP audit trail for user/group operations**

The LDAP module allows adding users, modifying groups, and changing sudoers configuration. These operations are sent to slapd and logged there, but clustr itself does not emit an audit event to the application log or DB when these operations succeed. HIPAA and FedRAMP both require audit trails for privileged account management.

**AUDIT-5: Per-node BMC credentials not rotated**

BMC/IPMI passwords are set once (manually by the operator in the UI) and stored indefinitely. There is no rotation workflow, no expiry concept, and no mechanism to bulk-rotate BMC passwords across a fleet. Federal environments under CIS L2 or DISA STIG require privileged credential rotation on a schedule.

---

## 9. Founder Escalation Responses (operational angle)

### Q1: RBAC model decision

From an ops perspective: the `users` table already has `admin/operator/readonly` roles and the middleware correctly enforces them via `requireRole`. The missing piece is **per-group scope**: an `operator` with `group_id=gpu-team-a` should only be able to trigger reimages on nodes in that group, not the entire cluster. This requires a `user_group_permissions` join table and a middleware that checks group membership on reimage and node-mutation routes.

The critical ops constraint: whatever RBAC model is chosen, it must produce an auditable permission decision that can be logged (user X was denied action Y on resource Z because they lack permission for group W). Without this, the audit trail is incomplete regardless of how fine-grained the permissions are.

Do not lean into LDAP-group-based delegation for internal clustr RBAC. LDAP should provision node users (cluster HPC accounts), not operator permissions. Mixing the two creates a circular dependency: an LDAP-dependent RBAC system cannot manage LDAP if the RBAC system is down.

Recommended model: a `user_permissions` table with `(user_id, resource_type, resource_id, permission_set)` rows, evaluated at request time, with a cached result in the session token. Scopes: `nodes:read`, `nodes:deploy`, `nodes:admin`, `images:read`, `images:admin`, `cluster:admin`. Combine with `group_id` FK to restrict `nodes:deploy` to a specific group.

### Q2: `groups[]` freeform vs NodeGroup

From an ops perspective, both should coexist but serve different purposes:
- `group_id` (NodeGroup FK): the operational grouping. This is what you reimage, what you assign network profiles to, what you scope RBAC to.
- `groups[]` (freeform labels): metadata for Slurm partition assignment and operator-defined tagging. These are NOT operational primitives.

The current confusion is that both are presented as equivalent grouping mechanisms in the UI. The fix is naming: rename `groups[]` to `labels[]` or `tags[]` in the API and UI. This is a non-breaking rename if done at the API response serialization layer. The DB column does not need to change.

Do not deprecate `groups[]` yet — Slurm partition role matching depends on them. But stop using the word "group" for them.

### Q3: Log retention model

The `node_logs` table is the highest-volume table in the DB. At 100 ingest req/s per node, a 10-minute deploy on 50 concurrent nodes generates ~3 million rows. The current 14-day default retains those rows for two weeks.

SQLite at 100 rows/row × 50 nodes × 600 seconds = 3M rows per full-fleet deploy. Each row is approximately 400 bytes (ID, node_mac, timestamp, level, component, message). That is 1.2GB per full-fleet deploy cycle, or ~24GB over 14 days of daily full-fleet deploys. On the current 100GB volume, logs alone can consume 24% of capacity within 14 days.

**Recommended model**:
- Keep 48-72 hours of full-fidelity logs in SQLite for live queries.
- Export older logs to compressed flat files in `CLUSTR_LOG_ARCHIVE_DIR` (one file per node per day).
- Expose `GET /api/v1/logs/archive/{date}/{node_mac}` to retrieve historical logs.
- Add `CLUSTR_LOG_HOT_RETENTION=72h` and `CLUSTR_LOG_ARCHIVE_RETENTION=90d` as the two-tier config.

This is a two-engineer sprint: DB purger update (Dinesh), log archive writer (Dinesh), archive retrieval endpoint (Dinesh), storage budget docs (ops).

---

## 10. Recommended Ops Investments by Priority

### P0 — Do before any external operator installs this in production

| ID | Action | Effort | Owner |
|---|---|---|---|
| P0-OPS-1 | Fix DB file permissions: `chmod 600 /var/lib/clustr/db/clustr.db`. Update `clustr-restore.sh` to use 600. Add to install docs. | 30 min | Gilfoyle |
| P0-OPS-2 | Fix `CLUSTR_LISTEN_ADDR` in systemd unit to provisioning interface IP, not `0.0.0.0`. Add hardening note to install guide. | 1 hour | Gilfoyle |
| P0-OPS-3 | Add `CLUSTR_SECRET_KEY` to `secrets.env` generation docs. Add startup assertion: if `CLUSTR_SECRET_KEY` not set, log WARN with key fallback value clearly stated. | 2 hours | Dinesh |
| P0-OPS-4 | Fix `Makefile` binary names (`clonr` → `clustr`). Fix `Dockerfile` binary names and `VOLUME` path. | 1 hour | Dinesh |
| P0-OPS-5 | Remove committed root-level binaries from git tracking. Update `.gitignore` to cover `clustr`, `clustr-serverd`, `clustr-static` at root. | 30 min | Gilfoyle |

### P1 — Fix before first customer / 200-node deployment

| ID | Action | Effort | Owner |
|---|---|---|---|
| P1-OPS-1 | HTTP server timeouts: add `ReadHeaderTimeout=10s`, `ReadTimeout=60s`, `WriteTimeout=300s`, `IdleTimeout=120s` to `http.Server` instantiation. | 2 hours | Dinesh |
| P1-OPS-2 | Autodeploy rollback: keep previous binary as `.prev`, restore on health-check failure. | 4 hours | Gilfoyle |
| P1-OPS-3 | Autodeploy reimage guard: query for in-progress reimage records before restarting, same pattern as ISO build check. | 3 hours | Dinesh |
| P1-OPS-4 | Log retention two-tier model: 72-hour hot window in SQLite, archive to compressed flat files. | 1 sprint (Dinesh) |
| P1-OPS-5 | Audit `requested_by` in reimage records: plumb `actorLabel(ctx)` through `CreateReimageRequest`. | 3 hours | Dinesh |
| P1-OPS-6 | Directory creation on first run: `clustr-serverd` should create all required dirs under `CLUSTR_IMAGE_DIR`, `CLUSTR_DB_PATH`, `CLUSTR_BOOT_DIR`, `CLUSTR_TFTP_DIR`, `CLUSTR_LOG_ARCHIVE_DIR` at startup rather than crashing on missing paths. | 4 hours | Dinesh |
| P1-OPS-7 | Healthz readiness endpoint: `GET /api/v1/healthz/ready` that pings the DB, verifies boot dir exists, verifies initramfs is present. Separate from `/health` liveness. | 3 hours | Dinesh |
| P1-OPS-8 | Dropbear SSH password not logged at INFO. Remove the `log " password : $SSH_PASS"` line from `initramfs-init.sh`. Log only that SSH is enabled and which IP/port. Rotate default password per-boot (use `/dev/urandom` 8-char hex at init time). | 2 hours | Dinesh |
| P1-OPS-9 | Wire `lab-validate.yml` on tag push now that UEFI boot path is confirmed green. Add `needs: [lab-validate]` to `release.yml`. | 2 hours | Gilfoyle |
| P1-OPS-10 | Encrypt LDAP `service_bind_password` and `admin_passwd` at rest using the same AES-256-GCM pattern already implemented for Slurm secrets. | 1 sprint (Dinesh) |

### P2 — Sprint 2 and beyond

| ID | Action | Effort |
|---|---|---|
| P2-OPS-1 | Prometheus `/metrics` endpoint: at minimum, export `clustr_active_deploys`, `clustr_deploy_total{status}`, `clustr_api_requests_total{endpoint,status}`, `clustr_db_size_bytes`, `clustr_image_disk_bytes`. | 1 sprint |
| P2-OPS-2 | Outbound webhook notifications: configurable `POST` on deploy-complete, deploy-failed, verify-boot-timeout. Needed for CI integration (webui review Persona C). | 1 sprint |
| P2-OPS-3 | Per-user audit log export: `GET /api/v1/audit?since=&until=&user=&action=` returning structured audit events from a dedicated `audit_log` table. | 1 sprint |
| P2-OPS-4 | Proxmox TLS: add `tls_ca_cert_path` field to Proxmox power provider config. Default to `InsecureSkipVerify=false` with explicit opt-in for dev. | 1 week |
| P2-OPS-5 | Install package: Docker Compose file for containerized deployment. Ansible role for bare-metal install. Kickstart template already exists; link it from README. | 1 sprint |
| P2-OPS-6 | Disk space pre-flight at startup and periodic check via background goroutine, with alert log at 80% / 90% / 95% fill levels. | 3 days |
| P2-OPS-7 | Node config change history: `node_config_history` table capturing old/new values and actor on every node config mutation. | 1 sprint |
| P2-OPS-8 | Rebuild iPXE from source in CI on tag push. Pin to a tagged iPXE release. Emit SHA-256 in the release notes. Remove the committed binary from the repo or replace with a CI-verified artifact. | 1 week |
| P2-OPS-9 | `CLUSTR_BACKUP_REMOTE` — make off-site backup the default path, not optional. Ship default config that uses local Object Storage or a second volume. | 1 day |
| P2-OPS-10 | TLS provisioning guide: write the missing `docs/tls-provisioning.md`. Add Caddy or nginx TLS termination as the recommended production front-end. | 2 days |

---

## 11. Quick Wins (small fixes, high ops payoff)

Listed in order of impact-to-effort ratio:

1. **Fix DB permissions to 600** — one `chmod`, closes SEC-P0-1 partially. 30 minutes.
2. **Fix `CLUSTR_LISTEN_ADDR` in systemd unit** — change `0.0.0.0:8080` to `10.99.0.1:8080`. 10 minutes.
3. **Fix `Makefile` binary names** — `make all` currently fails due to stale `clonr` paths. 30 minutes.
4. **Remove root-level binaries from git** — `git rm clustr clustr-serverd clustr-static` + update `.gitignore`. 20 minutes.
5. **Add `CLUSTR_SECRET_KEY` to systemd unit secrets.env template** — prevents munge key from being encrypted with the static fallback key. 30 minutes.
6. **Remove `log " password : $SSH_PASS"` from initramfs-init.sh** — eliminates SSH password exposure in the unauthenticated log server. 15 minutes.
7. **Fix `clustr-restore.sh` chmod to 600** — two-character change. 10 minutes.
8. **Add `CLUSTR_LOG_RETENTION` to systemd unit** — make the 14-day default explicit rather than implicit. 15 minutes.
9. **Wire `lab-validate.yml` on tag push** — boot arch confirmed green; unblock release gating. 2 hours.
10. **Add HTTP server timeouts** — four lines added to the `http.Server` struct instantiation. 1 hour including test.

---

*End of ops review. Total estimated P0+P1 effort: approximately 4 engineering-weeks split across Dinesh (implementation) and Gilfoyle (infra/config). The quick wins list (items 1-10 above) closes the most critical gaps in under one working day of focused work.*
