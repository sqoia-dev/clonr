# clustr — 90-Day Sprint Plan

**Window:** 2026-04-26 through 2026-07-25 (13 weeks, 6 x 2-week sprints + Sprint 0)
**Team:** Founder + AI agent team (Richard, Dinesh, Gilfoyle, Jared, Monica). No new hires in window.
**Prepared by:** Jared (Chief of Staff / Ops)
**Primary inputs:** `webui-review.md`, `architecture-review.md`, `implementation-review.md`, `ops-review.md`, `boot-architecture.md`
**Date prepared:** 2026-04-26
**Decisions reference:** `docs/decisions.md` (Richard, 2026-04-26) — all founder-escalation questions resolved; sprint placement amended in this doc to match.

---

## 1. Executive Summary

**Headline:** clustr enters this window as a functional but rough-edged single-server provisioning tool. It exits as a production-ready, secure-by-default HPC node imaging platform, ready for a first external operator install.

**The 90-day arc:**

Today clustr can clone and deploy nodes, manage images, and integrate with Slurm — but it has critical security gaps (world-readable SQLite containing BMC credentials and LDAP passwords), a silent data-corruption bug (NodeGroup cleared on every PUT), no RBAC, no pagination that would survive a 50-node cluster, and a scattered web of UI inconsistencies that would frustrate every operator persona. It is not safe to hand to an external operator.

By day 90, clustr will have:

- All P0 security gaps closed — credentials protected at rest, interface binding hardened, build artifacts verified
- RBAC with group-scoped operator roles, suitable for multi-team HPC environments
- A consolidated image factory (single finalize path), image tags surfaced in the UI, image metadata visible
- `groups[]` renamed to `tags[]` and the NodeGroup model clarified as the sole structured grouping primitive
- Observability: Prometheus metrics, readiness endpoint, structured audit log
- Production-grade log retention with per-node row cap
- Test coverage for the four zero-coverage feature modules (ldap, network, slurm, sysaccounts)
- Pagination on all list endpoints
- A persona-specific UX pass: bulk ops for HPC sysadmin, GPU inventory for ML lab, webhook callbacks for CI
- A v1.0 release artifact: packaged, installable, documented, with green CI including the lab gate

**What does NOT change:** no framework migration for the SPA, no Postgres migration, no HA/multi-server, no pkg/api wire/domain split. These are deliberately deferred.

---

## 2. Sprint 0 — In-Flight P0 Fixes (now / 0-1 day)

**Status: IN FLIGHT. These are already being shipped. Listed for completeness and traceability.**

| Item | Finding | Owner | Acceptance Criteria |
|---|---|---|---|
| Fix GroupID cleared on PUT | BUG-1 (`nodes.go:276`) | Dinesh | `PUT /api/v1/nodes/{id}` with no `group_id` in body preserves existing group assignment. Test added covering this path. |
| Fix Makefile binary names | BUILD-1 (`Makefile`: `clonr` → `clustr`) | Dinesh | `make all` completes without error. Both `bin/clustr-serverd` and `bin/clustr-static` are produced. |
| Fix Dockerfile binary names | BUILD-2 | Dinesh | `docker build` succeeds. Container binary is `/usr/local/bin/clustr-serverd`. `VOLUME` path updated. |
| DB chmod 600 | SEC-P0-1 / P0-OPS-1 | Gilfoyle | `/var/lib/clustr/db/clustr.db` is 600. `clustr-restore.sh` changed from `chmod 644` to `chmod 600`. |
| Fix `CLUSTR_LISTEN_ADDR` in systemd unit | SEC-P0-2 / P0-OPS-2 | Gilfoyle | `clustr-serverd.service` binds to provisioning interface IP, not `0.0.0.0`. |
| Fix Slurm `CLUSTR_SECRET_KEY` default exposure | SEC-P1-3 / P0-OPS-3 | Gilfoyle | `CLUSTR_SECRET_KEY` added to `secrets.env` generation docs. Startup logs explicit WARN if env var unset. |

---

## 3. Sprint 1 (Weeks 1–2: 2026-04-27 to 2026-05-10) — Foundation

**Theme:** Close the remaining pre-external-user P0/P1 gaps. Establish test infrastructure for untested modules. Implement log retention model.

**Sprint goal:** clustr can be handed to an early adopter without creating security incidents or silent data loss.

### Backlog

| ID | Item | Finding | Owner | Effort | Acceptance Criteria |
|---|---|---|---|---|---|
| S1-1 | Remove committed root-level binaries from git | BUILD-3 / P0-OPS-5 | Gilfoyle | S | `git rm clustr clustr-serverd clustr-static` at repo root. `.gitignore` updated to cover root-level binary names. No binary files tracked by git. |
| S1-2 | Remove Dropbear SSH password from initramfs log | SEC-P1-4 / P1-OPS-8 | Dinesh | S | `log " password : $SSH_PASS"` removed from `initramfs-init.sh`. Default SSH password randomized per-boot using `/dev/urandom` 8-char hex. Log only states SSH is enabled and port number. |
| S1-3 | HTTP server timeouts | SEC-P1-7 / P1-OPS-1 | Dinesh | S | `http.Server` in `server.go` has `ReadHeaderTimeout=10s`, `ReadTimeout=60s`, `WriteTimeout=300s`, `IdleTimeout=120s`. Test that long-running idle connections are closed. |
| S1-4 | Log retention: per-node row cap + summary table | Arch §4.3 / Richard recommendation | Dinesh | M | New `CLUSTR_LOG_MAX_ROWS_PER_NODE` env var (default 50,000). `runLogPurger` does two-pass eviction: TTL pass then per-node cap pass. New `node_logs_summary` table records purge events with row counts. Migration 040+. `CLUSTR_LOG_RETENTION` default changed from 14d to 7d in docs and systemd unit template. Founder decision checkpoint: approve defaults (see §13). |
| S1-5 | Test infrastructure for ldap, network, slurm, sysaccounts | TD-1 | Dinesh | L | Mock LDAP server test harness covering DN lookup, bind, group enumeration. Network profile golden-file tests (at least 5 fixture pairs: input config → expected NM keyfile). Slurm config rendering golden-file tests. `sysaccounts` injection test against a temp rootfs. No external services required for `go test`. |
| S1-6 | Pagination on `/api/v1/nodes` and `/api/v1/images` | TD-5 / API-4 | Dinesh | M | Both endpoints accept `?page=` and `?per_page=` (default 50). Response includes `total`, `page`, `per_page`, `next_cursor`. DB queries use `LIMIT/OFFSET`. UI node list and image grid fetch page 1 on load; "Load more" / infinite scroll for subsequent pages. |
| S1-7 | Autodeploy rollback guard: reimage in-progress check | P1-OPS-3 | Dinesh | S | Before `clustr-serverd` restart, autodeploy script queries `/api/v1/reimage` for records in non-terminal states. If any found, defers restart (same pattern as ISO build check). |
| S1-8 | Autodeploy rollback on health failure | P1-OPS-2 | Gilfoyle | M | Previous binary kept as `clustr-serverd.prev`. On health-check failure after binary swap, `clustr-serverd.prev` is restored and service restarted. Outcome logged to journal. |
| S1-9 | `clustr-serverd` creates required directories on first run | P1-OPS-6 / OPS-3 | Dinesh | S | At startup, server creates `CLUSTR_DB_PATH` dir, `CLUSTR_IMAGE_DIR`, `CLUSTR_BOOT_DIR`, `CLUSTR_TFTP_DIR`, `CLUSTR_LOG_ARCHIVE_DIR` if they do not exist. No panic or crash on fresh install. |
| S1-10 | Readiness endpoint | OBS-2 / P1-OPS-7 | Dinesh | S | `GET /api/v1/healthz/ready` pings DB (sample query), verifies boot dir exists, verifies initramfs is present. Returns 200 with structured JSON on healthy; 503 with specific failure reason if any check fails. Existing `/health` liveness endpoint unchanged. |
| S1-11 | Fix `actorLabel` in reimage `requested_by` | BUG-5 / P1-9 / P1-OPS-5 | Dinesh | S | `reimage.go:107` changed from `RequestedBy: "api"` to `RequestedBy: actorLabel(r.Context())`. All new reimage records carry the authenticated user ID or key label. |
| S1-12 | UpsertNodeByMAC atomicity fix | BUG-3 / TD-4 | Dinesh | S | `db.go:510–575` refactored to `INSERT OR IGNORE` + `UPDATE` pattern or wrapped in `BEGIN EXCLUSIVE` transaction. Concurrent PXE boot test added. |
| S1-13 | Document auth scope split for `/deploy/progress` | API-1 | Dinesh | S | Comment added adjacent to `server.go:509–512` explaining that `POST /deploy/progress` is outside admin group (node-scoped keys) while `GET` paths are inside. README API reference updated. |
| S1-14 | Wire `lab-validate.yml` on tag push | BUILD-5 / P1-OPS-9 | Gilfoyle | S | `release.yml` has `needs: [lab-validate]`. UEFI (vm202) and BIOS (vm201) both pass lab validation before release artifact is published. |
| S1-15 | LDAP credential encryption at rest (moved from S2-12 per `decisions.md` D4) | SEC-P0-1 / P1-OPS-10 | Dinesh | L | `ldap_module_config.service_bind_password` and `admin_passwd` encrypted using same AES-256-GCM pattern as Slurm munge key. Encrypted at write time, decrypted at read time. `CLUSTR_SECRET_KEY` required (server fails closed if unset in non-dev mode). Migration: existing plaintext values re-encrypted on first server start after upgrade. New `*_encrypted` boolean column tracks per-row state for idempotent migration. |
| S1-16 | BMC credential encryption at rest (new scope per `decisions.md` D4) | SEC-P0-1 | Dinesh | M | `node_configs.bmc_config` JSON column: BMC password and Proxmox API password fields encrypted using same AES-256-GCM pattern. Migration encrypts existing plaintext values at first start after upgrade. UI continues to display credential fields normally; encryption is at-rest only. |

**Founder decision point (end of Sprint 1):** Log retention defaults are LOCKED in `decisions.md` D2 (no review needed). Confirm Sprint 1 acceptance (all P0/P1 security gaps closed, test infrastructure delivered, LDAP+BMC encryption verified on the lab) and unblock Sprint 2.

---

## 4. Sprint 2 (Weeks 3–4: 2026-05-11 to 2026-05-24) — Image Factory + Grouping Model

**Theme:** Consolidate the image factory's five finalize paths into one. Clarify the `tags[]` vs `NodeGroup` model. Surface image metadata and tags in the UI.

**Sprint goal:** Image lifecycle is coherent end-to-end. Tags are visible and usable. The `groups[]`/`NodeGroup` confusion is resolved in the API and UI.

### Backlog

| ID | Item | Finding | Owner | Effort | Acceptance Criteria |
|---|---|---|---|---|---|
| S2-1 | Extract `finalizeImageFromRootfs` helper | Arch §3.1 / R-1 / QW-5 | Dinesh | M | Single `finalizeImageFromRootfs(ctx, imageID, rootfsPath, sourceMetadata)` function. All five async paths (`pullAsync`, `importISOAsync`, `captureAsync`, `buildFromISOAsync`, `resumeFinalize`) call it. Any future post-finalization step (initramfs rebuild signal, webhook) is added once. `factory.go` target: < 1,400 LOC from current 2,265. |
| S2-2 | Surface image metadata in UI | Webui P1-4 / Arch §3.1 followthrough | Dinesh | S | Image detail page calls `GET /api/v1/images/{id}/metadata`. Kernel version, CUDA version (if present), installed packages summary, build timestamp displayed in "Image Details" KV grid. Metadata consistently populated across all five build paths (enforced by S2-1). |
| S2-3 | Image tags: UI add/remove/filter | Webui P1-3 / Arch §4.2 | Dinesh | M | Image detail page has a tag editor (add/remove individual tags). Images grid has a tag filter dropdown. `GET /api/v1/images` accepts `?tag=` query param. Tags field is no longer dead surface area in the UI. |
| S2-4 | `groups[]` → `tags[]` rename — first half (API + DB) | Arch §4.2 recommendation | Dinesh | M | Migration 041: rename `node_configs.groups` column to `node_configs.tags`. `pkg/api.NodeConfig.Groups` field gets new `Tags` field; both serialized in JSON response for one release (backward compat). `NodeConfig.Groups` marked deprecated in code comment. Slurm module updated to use `Tags` internally. |
| S2-5 | `node_group_memberships`: add `is_primary` flag | Arch §4.2 recommendation | Dinesh | M | Migration 042: `is_primary BOOLEAN NOT NULL DEFAULT FALSE` added to `node_group_memberships`. Partial unique index: `CREATE UNIQUE INDEX IF NOT EXISTS idx_primary_group_membership ON node_group_memberships(node_id) WHERE is_primary=TRUE`. Existing `group_id` values migrated to `is_primary=TRUE` rows. `EffectiveLayout()` reads primary membership instead of `group_id`. `node_configs.group_id` dual-read maintained; column marked for removal at v1.0. |
| S2-6 | UI: fix node list modal to include `group_id` field | Webui P0-2 | Dinesh | S | Node list Add/Edit modal includes a "Node Group" dropdown (populated from `GET /api/v1/node-groups`). Nodes created or edited from the modal can be assigned to a NodeGroup. Field is optional; unset is valid. |
| S2-7 | UI: `tags[]` vs NodeGroup — help text and label fix | Webui P1-10 / Arch §4.2 | Dinesh | S | Node Overview tab labels "Tags" (freeform, previously "Groups/Tags") and "Node Group" (structured, FK). Tooltip on Tags field: "Unstructured labels used for filtering and Slurm role assignment." Tooltip on Node Group: "Primary operational group — controls disk layout inheritance, network profile, and group reimages." |
| S2-8 | `sshpass` pre-flight check in capture | BUG-4 / QW-3 | Dinesh | S | `CaptureNode` handler checks `exec.LookPath("sshpass")` when `req.SSHPassword != ""`. Returns 400 with message "sshpass is not installed on the server host" before accepting the request. |
| S2-9 | Fix `base_image_id` validation in `CreateNode` | API-6 / QW-7 | Dinesh | S | `CreateNode` validates `BaseImageID` exists in DB before INSERT. Returns 400 "image not found" rather than letting SQLite FK constraint produce a 500. |
| S2-10 | image status state machine: add `CHECK` constraint | Arch §5.3 item 4 | Dinesh | S | Migration 043: `CHECK (status IN ('building', 'interrupted', 'ready', 'archived'))` on `base_images.status`. `db.UpdateBaseImageStatus` adds a domain-level guard. |
| S2-11 | Stale initramfs dashboard indicator | Webui P1-6 | Dinesh | S | Dashboard stat card or persistent sidebar indicator shows "Initramfs may be stale" when newest image `created_at` > initramfs `built_at`. Warning links to Images page. PXE serve path logs WARN when initramfs kernel version predates the latest image's kernel version (using kernel version recorded at initramfs build time). |
| S2-13 | Nav: replace `<details>` collapsibles with conditional sections | Webui P0-3 | Dinesh | S | `<details><summary>Slurm</summary>` and `<details><summary>LDAP</summary>` removed from `index.html`. Replaced with flat section headers. Module nav items shown/hidden based on server-returned module status at init time, not post-load JS hide/show. Nav state no longer flashes on page transition. |
| S2-14 | Replace `confirm()`/`alert()` with modal pattern | Webui P0-4 | Dinesh | M | All `confirm()` at `app.js:1130`, `app.js:1639`, `app.js:3022` replaced with modal-based confirmations. `alert()` similarly replaced. Destructive actions require modal confirmation using the existing modal system. Automated Playwright/Puppeteer tests can now interact with confirmation dialogs. |

**Founder decision point (end of Sprint 2):** Review the `tags[]`/NodeGroup model (S2-4, S2-5, S2-7). Approve dual-read deprecation timeline for `groups` → deprecated in API docs now, removed at v1.0. Sign off on `node_configs.group_id` removal schedule.

---

## 5. Sprint 3 (Weeks 5–6: 2026-05-25 to 2026-06-07) — RBAC + Audit Trail

**Theme:** Implement three-tier role + group-scoped operator RBAC. Wire audit identity through to all state-changing actions.

**Sprint goal:** Multiple operators with different permission scopes can safely share a clustr instance. Every state-changing action is attributed.

### Backlog

| ID | Item | Finding | Owner | Effort | Acceptance Criteria |
|---|---|---|---|---|---|
| S3-1 | RBAC: `user_group_memberships` table + middleware | Arch §4.1 recommendation | Dinesh | L | Migration 044: `user_group_memberships(user_id, group_id, role TEXT CHECK(role IN ('operator')))`. `requireGroupAccess(nodeIDParam)` middleware: admins bypass, operators check membership, readonly reject. Handlers updated: `POST reimage`, `power ops`, `PUT node`, `DELETE node`, `group reimage` all route through `requireGroupAccess`. Tests covering all three role levels + non-member rejection. |
| S3-2 | RBAC: UI permission gating | Arch §4.1 / Webui P2-1 | Dinesh | M | Reimage button, power controls, delete actions hidden or disabled in UI when current user lacks permission for the node's group. User management section in Settings only visible to admin. Admin-only actions (create NodeGroup, manage modules) gated on role. |
| S3-3 | RBAC: operator group assignment in Settings UI | Arch §4.1 | Dinesh | M | Settings > Users page shows each user's role and group memberships. Admin can assign an operator to one or more NodeGroups. Operator can see which groups they manage. API: `PUT /api/v1/users/{id}/group-memberships`. |
| S3-4 | Structured audit log table | Arch §5.3 / OBS-3 / P2-OPS-3 | Dinesh | M | Migration 045: `audit_log(id, actor_id, actor_label, action TEXT, resource_type, resource_id, old_value JSONB, new_value JSONB, created_at, ip_addr)`. Written on: reimage trigger, node config mutation, image create/archive/delete, NodeGroup create/delete, user create/modify/delete, LDAP config change, Slurm config change. `GET /api/v1/audit?since=&until=&actor=&action=&resource_type=` endpoint returning paginated results. |
| S3-5 | Plumb audit log writes through all state-changing handlers | OBS-3 / AUDIT-2 | Dinesh | M | Every handler that mutates state calls `h.Audit.Record(ctx, action, resourceType, resourceID, oldVal, newVal)`. Node config changes capture before/after snapshot. Reimage records include actor from `actorLabel`. Image status changes recorded. |
| S3-6 | Session expiry: 401 → `/login` redirect | Webui P0-5 | Dinesh | S | Global API response interceptor in `app.js` catches 401 responses and redirects to `/login` with `?next=` param preserving current route. No more unhandled JSON error blobs on session expiry. |
| S3-7 | Nodes list: hostname/MAC/status search | Webui P1-1 | Dinesh | M | Search input above nodes list. `GET /api/v1/nodes?search=` accepted by server (DB LIKE query on hostname, primary_mac, status). Client-side debounce. Results update in place without full-page reload. |
| S3-8 | Scheduled reimage: surface `scheduled_at` in UI | Webui P1-2 / Mismatch 1 | Dinesh | S | Reimage modal includes a datetime picker for `scheduled_at` (optional). Empty = immediate. Non-empty schedules the reimage via the existing `scheduled_at` field in `POST /api/v1/nodes/{id}/reimage`. |
| S3-9 | Disk space pre-flight at startup + periodic check | HA-2 / P2-OPS-6 | Dinesh | S | At startup, `clustr-serverd` checks available disk on `CLUSTR_IMAGE_DIR`. Logs WARN at 80%, ERROR at 90%, FATAL with exit at 95%. Background goroutine checks every 15 minutes and logs at same thresholds. |
| S3-10 | `CLUSTR_AUTH_DEV_MODE` loopback guard | SEC-P2-1 | Dinesh | S | Startup check: if `CLUSTR_AUTH_DEV_MODE=1` AND listen address is not loopback, server refuses to start with a clear error message. |
| S3-11 | Write `docs/rbac.md` (new per `decisions.md` D12) | OPS-1 | Dinesh | S | `docs/rbac.md` covers: 3-tier role model (admin/operator/readonly), group-scoped operator semantics, bootstrap admin via `clustr-serverd apikey bootstrap`, session vs API key auth, and the migration story from single-tenant to multi-user. Linked from README. |
| S3-12 | `gosec` + `trivy` + `govulncheck` in CI (new per `decisions.md` D8) | SEC baseline | Gilfoyle | S | `ci.yml` runs `gosec ./...`, `govulncheck ./...`, and `trivy image` on the built container. Build fails on HIGH findings. Documented allow-list for any false positives. |

**Founder decision point (end of Sprint 3):** RBAC model final sign-off. Review the `user_group_memberships` implementation against real usage on the dev cluster. Confirm the permission matrix in Arch §4.1 is correct. Multi-user readiness review: is the system ready for a second human operator?

---

## 6. Sprint 4 (Weeks 7–8: 2026-06-08 to 2026-06-21) — Production Readiness

**Theme:** Close Gilfoyle's production-readiness gaps. Observability. Backup automation. Failure-mode hardening.

**Sprint goal:** An operator running clustr in a real production environment has the monitoring, backup, and failure-recovery tooling they need.

### Backlog

| ID | Item | Finding | Owner | Effort | Acceptance Criteria |
|---|---|---|---|---|---|
| S4-1 | Prometheus `/metrics` endpoint | OBS-1 / P2-OPS-1 | Dinesh | M | `GET /metrics` exports (at minimum): `clustr_active_deploys`, `clustr_deploy_total{status}`, `clustr_api_requests_total{endpoint,status,method}`, `clustr_db_size_bytes`, `clustr_image_disk_bytes`, `clustr_node_count{state}`. Uses `prometheus/client_golang`. README includes Prometheus scrape config example. |
| S4-2 | Outbound webhook notifications | P2-OPS-2 / Webui Persona C P2-6 | Dinesh | M | Settings > Webhooks page: add webhook URL + events to subscribe to (`deploy.complete`, `deploy.failed`, `verify_boot.timeout`, `image.ready`). POST to configured URL with JSON payload including event type, node ID, image ID, timestamp, actor. Retry 3x with exponential backoff. `webhook_deliveries` table records delivery attempts and responses. |
| S4-3 | Reimage-pending reaper | Arch §5.3 item 1 | Dinesh | S | Background goroutine runs hourly: finds `node_configs` with `reimage_pending=TRUE` AND no `reimage_requests` row in a non-terminal state. Clears `reimage_pending` and logs the recovery. Prevents nodes from looping PXE-deploy forever on orphaned state. |
| S4-4 | Group reimage resume on startup | Arch §5.4 item 5 | Dinesh | S | Server startup hook calls `ResumeGroupReimageJob` for any `group_reimage_jobs` row with `status='running'` from a prior process. Job resumes from last completed node. Prevents lost group reimage jobs on server restart. |
| S4-5 | Proxmox TLS: configurable CA cert path | SEC-P1-2 / P2-OPS-4 | Dinesh | S | Proxmox power provider config gains `tls_ca_cert_path` field (optional). When provided, TLS verification uses the specified CA. When empty, `InsecureSkipVerify` still applies but is logged as WARN in production mode. UI: node edit Proxmox section shows TLS CA cert path field. |
| S4-6 | Scanner fan-out for `flipNodeToDiskFirst` | QW-11 / `server.go:228` | Dinesh | S | `runVerifyTimeoutScanner` fans out `flipNodeToDiskFirst` calls into goroutines with a bounded semaphore (configurable, default 5 concurrent). Prevents scanner from blocking for 10+ minutes on simultaneous timeouts at a 200-node cluster. |
| S4-7 | Backup: detect and warn on captured images not backed up | HA-4 | Gilfoyle | S | `clustr-backup.sh` detects images with `source_type=capture` (or equivalent) and includes them in backup OR emits explicit WARNING in backup log: "Captured image X is not rebuildable from ISO cache and is not backed up." Operators learn about this risk before data loss. |
| S4-8 | Autodeploy restore test (verify backup integrity) | HA-3 | Gilfoyle | M | Weekly `clustr-backup-verify.timer`: restores most recent DB backup to a temp path, starts `clustr-serverd --db-path <temp>` on a different port, hits `/api/v1/healthz/ready`, shuts it down. Pass/fail logged. Operator alerted via journal WARN on failure. |
| S4-9 | `verify_boot` flip-back failure: escalate from WARN to `/health` counter | Arch §5.3 item 2 | Dinesh | S | When server-side `flipNodeToDiskFirst` fails after `deploy_verified_booted`, increment `clustr_flipback_failures_total` counter (surfaces in Prometheus via S4-1). `/health` response includes `flip_back_failures` count. Operator can detect silent persistent-PXE-boot risk from the metrics endpoint. |
| S4-10 | `DEL /api/v1/nodes/{id}/reimage/active` endpoint | Webui Persona C P2-9 | Dinesh | S | New endpoint cancels the currently in-flight reimage for a node by node ID (not reimage ID). CI automation can cancel a running deploy without needing to know the reimage request UUID. Returns 404 if no active reimage. Returns 200 with cancelled reimage record on success. |
| S4-11 | Image build: pre-inject `custom_vars` at reimage time | Webui Persona C gap | Dinesh | M | `POST /api/v1/nodes/{id}/reimage` accepts `inject_vars: {key: value}` in request body. These are merged with the node's `custom_vars` for this deployment only (not persisted). Deploy agent receives them in the initramfs cmdline. CI automation can inject `TEST_SUITE=foo BUILD_ID=123` per run without a separate PUT to modify node config. |
| S4-12 | Remove duplicate Delete button on node detail | Webui things-no-persona-needs #4 | Dinesh | S | One of the two "Delete node" entry points on the node detail header removed (per `app.js:3153–3156`). Single path to destructive action remains. |
| S4-13 | Remove "Server info" placeholder text | Webui things-no-persona-needs #2 | Dinesh | S | `app.js:7336` "Server info will appear here in a future update" removed. Either implement a basic server info section (version, uptime, node count) or remove the section entirely. |
| S4-14 | Write `docs/install.md` (moved from S6-4 partial scope per `decisions.md` D12) | OPS-1 | Gilfoyle | M | `docs/install.md` covers: Docker Compose path (primary) and Ansible/bare-metal path (secondary). Includes prerequisites, env var reference, bootstrap admin step, first-deploy smoke test. Required before Sprint 4 exit since Sprint 4 ships to first external operator. |

**Founder decision point (end of Sprint 4):** Production-readiness checkpoint. Review Prometheus metrics dashboard and backup/restore verification status. Confirm the system is ready for a first external operator on their own hardware. Any blockers before Persona polish in Sprint 5?

---

## 7. Sprint 5 (Weeks 9–10: 2026-06-22 to 2026-07-05) — Persona Polish

**Theme:** Targeted UX improvements for each operator persona. The goal is that a first-time external user of each type can accomplish their primary workflow without friction.

**Sprint goal:** Every persona (A/B/C/D) has their highest-value missing workflow addressed.

### Backlog

| ID | Item | Persona | Finding | Owner | Effort | Acceptance Criteria |
|---|---|---|---|---|---|---|
| S5-1 | Power state column in nodes list | A/B | Webui P1-5 | Dinesh | M | Nodes list table includes a "Power" column showing cached power state (on/off/unknown) for each node. State fetched in batch at list load time (one call per node with concurrency limit 10), not per-dropdown-open. Thundering-herd against BMC network eliminated. |
| S5-2 | GPU hardware detection and display | B | Webui P2-3 | Dinesh | L | Hardware survey in initramfs collects PCIe device list (`lspci -vmm` output), filtered to GPU devices. GPU model, count, and VRAM size stored in `hardware_profile.gpus[]`. Node detail Hardware tab displays GPU inventory. Image detail shows CUDA version from metadata (via S2-2). Role-mismatch warning upgraded: GPU node assigned CPU-only image requires explicit confirmation. |
| S5-3 | Nodes list: column header sorting | A/B | Webui P2-5 | Dinesh | M | Clicking column headers in the nodes list sorts by that column (hostname, status, last deploy, group). Sort state persists within session. Server-side sorting on `GET /api/v1/nodes?sort=hostname&dir=asc`. |
| S5-4 | Bulk reimage from nodes list | A | Webui §top5 universal gap + Persona A | Dinesh | M | Checkbox column on nodes list. "Reimage selected" action bar appears when 1+ nodes are checked. Reimage modal accepts image selection and concurrency setting. Submits individual reimage requests for each selected node (not a group reimage — no group membership required). |
| S5-5 | "Retry" and "Re-deploy last image" from nodes list row | B | Webui Persona B pain | Dinesh | S | Nodes list row action dropdown includes "Re-deploy last image" (pre-populates reimage modal with last image) and "Retry" for nodes in Failed state. Eliminates the 3-click flow for routine re-deploys. |
| S5-6 | CI API key preset in key creation flow | C | Webui Persona C "cookie-cutter" | Dinesh | S | API key creation modal has a "CI integration" template: node-scoped key, 30-day TTL, label "ci-key", with example curl snippet for triggering a reimage displayed after creation. |
| S5-7 | Group reimage: add `dry_run` option | C | Webui P1-7 / Mismatch 7 | Dinesh | S | Group reimage modal includes `dry_run` checkbox, consistent with single-node reimage modal. API path for group reimage passes `dry_run` through to individual reimage requests. |
| S5-8 | Image creation: recommended path guidance | D | Webui Persona D "cookie-cutter" | Dinesh | S | Images page shows a callout card: "New here? Build from ISO is the recommended starting point." The four entry points are restructured: "Build from ISO" is first and highlighted; "Pull", "Capture from Host", "Import ISO" are secondary. Tooltip on each explains when to use it. |
| S5-9 | Getting-started: first-deploy wizard | D | Webui Persona D missing | Dinesh | L | If DB has zero images AND zero nodes, dashboard shows a 3-step wizard card: (1) Create an image → (2) Boot a node → (3) Deploy. Each step links to the relevant action. Wizard dismisses after first successful deploy. In-app link to external documentation (placeholder URL in Settings About tab populated). |
| S5-10 | Active deployments: promote to direct nav link | A/B/C | Webui things-to-promote #3 | Dinesh | S | Sidebar has a direct "Deployments" link leading to the active deployments table. Or dedicate `/deploys` route. Table reachable in one click from anywhere in the UI. |
| S5-11 | `GET /api/v1/progress` path confirmation + documentation | API-1 / Webui P0-1 | Dinesh | S | Targeted repro of the P0-1 404 with a fresh admin session and curl. Root cause confirmed (stale client cache vs real routing gap). If real gap: routing fix. Documentation comment added at server.go explaining the ingest (node-scoped) vs read (admin-scoped) asymmetry for `/deploy/progress`. |
| S5-12 | Node config change history table | AUDIT-2 / P2-OPS-7 | Dinesh | M | Migration 046: `node_config_history(id, node_id, actor_label, changed_at, field_name, old_value, new_value)`. Written on every `UpdateNodeConfig` call. Node detail page shows a "Config History" tab with paginated change log. Auditor can see who changed what and when. |

**Founder decision point (end of Sprint 5):** Launch-readiness review. Do the four personas each have a plausible first-use path? Is there anything from the webui-review backlog that must land before a public announcement? Set Show HN timing expectations.

---

## 8. Sprint 6 (Weeks 11–12: 2026-07-06 to 2026-07-19) — Release Readiness

**Theme:** Reproducible build artifacts, packaging, documentation, and v1.0 ship decision.

**Sprint goal:** An external operator can find clustr, install it from official docs, and have a working deployment in under 2 hours. A v1.0 tag produces a complete, verifiable release.

### Backlog

| ID | Item | Finding | Owner | Effort | Acceptance Criteria |
|---|---|---|---|---|---|
| S6-1 | Reproducible iPXE build from source in CI | SEC-P0-3 / P2-OPS-8 | Gilfoyle | L | `ipxe-build.yml` workflow: pins to a specific iPXE upstream git tag, builds `ipxe.efi` in CI, computes SHA-256, compares to committed value. On tag push: rebuilt binary attached to GitHub Release with SHA-256 in release notes. Committed binary in `internal/bootassets/` replaced with CI-verified artifact or removed in favor of a download-at-build-time pattern. |
| S6-2 | Docker Compose install package | BUILD-6 / P2-OPS-5 | Gilfoyle | M | `deploy/docker-compose/docker-compose.yml` with `clustr-serverd` container, volume mounts for `CLUSTR_IMAGE_DIR` and `CLUSTR_DB_PATH`, `.env.example` with all required variables documented. `docker compose up` on a fresh Linux host produces a working clustr instance. README "Quick Start" section uses this as the primary path. |
| S6-3 | Ansible role for bare-metal install | BUILD-6 / P2-OPS-5 | Gilfoyle | M | `deploy/ansible/` role covers: download binaries, create dirs, create systemd unit from template, populate secrets.env, configure firewall rules for ports 8080/67/69. `README.md` install section references this role. Idempotent: re-running does not disrupt a running instance. |
| S6-4 | Operator upgrade guide | OPS-1 / Webui Persona D | Gilfoyle | S | `docs/upgrade.md`: explains that migrations run automatically at startup, which env vars invalidate sessions on rotation, how to confirm a successful upgrade, rollback procedure (restore from backup). Linked from README. (Note: install.md moved to S4-14 per `decisions.md` D12.) |
| S6-5 | TLS provisioning guide | HA-5 / P2-OPS-10 | Gilfoyle | S | `docs/tls-provisioning.md` created (referenced but missing per ops-review). Covers Caddy TLS termination as recommended production front-end. Instructions for configuring `CLUSTR_SERVER` in the initramfs for HTTPS. Note on provisioning-network HTTP being acceptable in physically-isolated environments. |
| S6-6 | `node_configs.group_id` column removal | Arch §4.2 / S2-5 | Dinesh | S | Migration 047: `node_configs.group_id` column dropped. All reads migrated to `node_group_memberships` with `is_primary=TRUE`. `EffectiveLayout()` and `EffectiveExtraMounts()` use primary membership. BUG-1 fix from Sprint 0 remains in place; `group_id` is no longer a data loss vector. |
| S6-7 | `pkg/api.NodeConfig.Groups` Sunset header (scope reduced per `decisions.md` D3) | Arch §4.2 / S2-4 | Dinesh | S | `Groups` JSON field stays in v1.0 responses but is annotated with `Sunset: <v1.1 release date>` HTTP header on endpoints that return it. `CHANGELOG.md` documents the rename and the v1.1 removal commitment. **Field removal moves to v1.1, NOT v1.0**, to avoid breaking the small population of pre-v1.0 design partners. |
| S6-8 | `last_deploy_succeeded_at` dual-write removal | Arch §5.4 item 4 | Dinesh | S | Migration 048: `node_configs.last_deploy_succeeded_at` column dropped. All references switched to `deploy_completed_preboot_at`. Removes the back-compat dual-write. |
| S6-9 | `initramfs.yml` gates on `ci.yml` success | BUILD-4 | Gilfoyle | S | `initramfs.yml` has `needs: [ci]` dependency. Initramfs artifact is only published after `go test` passes. Combined with S1-14 (lab gate), no release artifact ships without passing tests AND lab validation. |
| S6-10 | Remove debug ESP log blocks from `rsync.go` | QW-10 | Dinesh | S | Debug log blocks at `rsync.go:526–553` removed or downgraded to `Debug()` level now that UEFI boot is confirmed stable. Production deploy logs are uncluttered. |
| S6-11 | `v1.0` release checklist execution | — | Founder + All | M | CI green on main. `lab-validate` green on vm201 (BIOS) and vm202 (UEFI). All P0 security findings closed. All Sprint 0–6 acceptance criteria met. `CHANGELOG.md` written covering v0.x → v1.0 changes. Tag `v1.0.0` pushed. GitHub Release published with Docker Compose file, Ansible role, initramfs artifact, SHA-256 checksums. |

**Founder decision point (end of Sprint 6):** v1.0 ship/no-ship decision. The binary criterion: all P0 security findings from ops-review are closed, all items from Sprint 0 through Sprint 6 are at acceptance, CI and lab validation are green. If any Sprint 5/6 items are incomplete, triage into a v1.0.1 hotfix scope vs. hold.

---

## 9. Cross-Cutting Tracks

These run across multiple sprints and do not fit cleanly into a single backlog.

### Documentation (continuous, Sprint 1–6)

- Each sprint's new features include a short `docs/` update or inline code comment
- `docs/api-reference.md` expanded as new endpoints land (webhooks, audit log, pagination, `/healthz/ready`)
- In-app documentation link (`app.js:_settingsAboutTab`) populated with a real URL no later than Sprint 5
- `CHANGELOG.md` maintained from Sprint 1 forward so v1.0 release notes are not written in a panic

### Performance and Load Testing (Sprint 4–5)

- Target: 200-node concurrent reimage burst against the dev host
- Tooling: a synthetic load script (`scripts/load-test/`) that registers N fake nodes, triggers group reimage, and measures: API response latency, SQLite write queue depth (via Prometheus after S4-1), memory growth, and log ingest throughput
- Success criteria: no OOM, no 500s, `group_reimage_jobs` table correct at completion
- Owner: Dinesh (implementation), Gilfoyle (infra observation)

### External Security Review (Sprint 6 preparation)

- Scope: the five credential classes at rest (BMC, LDAP, session HMAC), the PXE chain (iPXE binary provenance, initramfs boot), the API surface (RBAC bypass attempts, rate limiting, token leakage via URL params)
- Timing: engaged at end of Sprint 5, findings incorporated before v1.0 tag
- Budget / approach: at the founder's discretion (see Open Questions §14). At minimum, run `gosec` and `trivy` in CI and address all HIGH findings before release. A human security reviewer is the ideal but may be post-launch.

---

## 10. Explicit Non-Goals (90-Day Window)

These are deliberate decisions, not omissions. Raising any of these in sprint planning is a scope-creep signal.

| Non-Goal | Reason | Revisit Trigger |
|---|---|---|
| `pkg/api` wire/domain split | Premature. Only matters when we add `/api/v2/` or row-level RBAC redaction. | Adding second API version or per-field redaction. |
| PostgreSQL migration | SQLite is fine at current scale. The `internal/db.Store` interface lift is deferred. | 50–100 concurrent active nodes on a single server. |
| HA / multi-server architecture | Out of stage. Orchestrator shape is HA-friendly; that is sufficient. | Post-Series-A territory. |
| SPA framework migration (React/Vue) | Contradicts the "one binary, one container" pitch. Module split (R-2) is the right scope. | Never add a build step in this window. |
| External log sink integration (Loki/OpenSearch) | Violates self-hosted single-binary pitch. | Customer explicitly requests it. |
| PBS/Torque integration | Slurm is the differentiator. PBS is a future commercial add-on. | Paying customer requires PBS. |
| Redfish / vSphere power providers | Interface is ready; demand is not demonstrated. | First customer on Redfish hardware. |
| IPv6 provisioning network | Federal/DoD path; not the first target market. | Federal procurement opportunity emerges. |
| `internal/db/db.go` package-level split | File-level split is fine. Package split is deferred quality move. | Same as Postgres trigger — post-100 nodes. |
| `internal/deploy/finalize.go` refactor | 2,363 LOC is a cohesive single concern. Splitting makes cross-step state harder to reason about. | Only if a new deploy phase is added that cannot fit the existing structure. |
| BMC credential rotation workflow | Not MVP. Operators do this manually via IPMI tools. | CIS/STIG compliance requirement from a customer. |
| Helm chart or Kubernetes operator | Target is bare-metal clusters, not Kubernetes-native environments. | Customer on k8s management plane (unlikely for HPC). |

---

## 11. Risk Register

### What could blow up the plan

| Risk | Probability | Impact | Mitigation |
|---|---|---|---|
| Boot architecture regression on bare-metal (vs Proxmox VMs) | Medium | High — blocks v1.0 | `lab-validate` added to release gate (S1-14). First bare-metal test should happen Sprint 2 or earlier. |
| `app.js` 8K LOC becomes a merge-conflict bottleneck | High | Medium — slows UI sprints | Sprint 2 module split (R-2 approach) reduces this. Single-threaded frontend is a known constraint. |
| SQLite write contention at 50+ concurrent nodes during load testing | Medium | Medium — surfaces in Sprint 4-5 | Prometheus metrics (S4-1) will surface this. Mitigation: `_busy_timeout` tuning, batch write paths. Postgres is the nuclear option but deferred. |
| LDAP+BMC credential encryption (S1-15/S1-16) breaks existing deployments silently | Low | High — data integrity | Migration path explicitly tested: `*_encrypted` flag enables idempotent re-run; existing plaintext values re-encrypted on first start; rollback instructions in upgrade doc. Server fails closed if `CLUSTR_SECRET_KEY` unset (no silent fallback). |
| Real OpenLDAP via testcontainers in CI fails on slow runners (per `decisions.md` D11) | Low/Medium | Low — slows Sprint 1 only | Budget 60s container startup. Fall back to a Go-based LDAP fake if CI runner can't run Docker reliably. |
| Founder bandwidth: 6 sprints × 2 weeks = 90 days is tight with AI-agent team | Medium | High — delays v1.0 | Explicit scope gates at each sprint end. Non-goals list prevents feature creep. Cut plan (below) defines the minimum. |
| External security review finds a P0 gap in Sprint 6 | Low-Medium | High — blocks v1.0 | `gosec` + `trivy` run in CI from Sprint 3 forward. `CLUSTR_AUTH_DEV_MODE` guard (S3-10) and RBAC middleware (S3-1) close the most likely surface areas early. |

### Signals that would cause a re-plan

- First external operator finds a deploy-breaking bug not in our review docs
- Bare-metal IPMI testing in Sprint 2 reveals a failure mode in the boot architecture
- RBAC implementation in Sprint 3 reveals that the `user_group_memberships` model does not match how a real HPC sysadmin manages permissions
- SQLite write contention hits before Sprint 4 load testing (i.e., during normal dev iteration at 10+ nodes)

### 50% Descope Cut — Minimum Viable v1.0

If we must cut the plan by 50%, this is the minimum that constitutes a shippable v1.0:

**Keep:**
- All Sprint 0 items (in flight)
- All Sprint 1 items (security and stability foundation — non-negotiable)
- S2-1 (factory finalize), S2-6 (node modal group_id), S2-13 (nav flash), S2-14 (modal replace confirm())
- S3-1 and S3-2 (RBAC implementation and UI gating — product differentiation)
- S3-6 (session expiry), S3-7 (node search)
- S4-1 (Prometheus), S4-2 (webhooks), S4-3 (reimage reaper)
- S6-1 (reproducible iPXE), S6-2 (Docker Compose), S6-4 (install/upgrade docs), S6-11 (v1.0 release)

**Defer:**
- S2-2 (image metadata UI), S2-3 (image tags UI) — Sprint 1.1
- (Note: LDAP+BMC encryption S1-15/S1-16 is NOT cuttable per `decisions.md` D4 — P0 security)
- S3-4 through S3-5 (full audit log table) — replace with S1-11 minimal actor attribution
- S4-6 through S4-13 (production hardening polish) — post-launch
- All Sprint 5 persona polish items — v1.1
- S6-3 (Ansible role), S6-5 (TLS guide) — v1.1

---

## 12. Resourcing Assumption

This plan is sized for **one founder + AI agent team, no new hires in the window**.

- Effort estimates: S = 0.5–1 day, M = 2–3 days, L = 4–5 days
- Each sprint contains approximately 10–15 days of work across all items, assuming the AI agent team operates at roughly 2–3x individual contributor throughput on well-scoped tasks
- The L-sized items (S1-5 test infrastructure, S2-1 factory refactor, S3-1 RBAC, S5-2 GPU detection, S6-1 iPXE build) are the plan's pacing constraints. If any L item spills, the sprint end review should decide whether to cut something else or carry it
- Founder time is the binding constraint on decision points and on any work that requires judgment calls the agents cannot make (Q1–Q6 in §14)
- No external contractors or security reviewers are budgeted. Sprint 6 security review assumes `gosec`/`trivy` only unless founder decides otherwise

---

## 13. Founder Decision Points by Sprint

| Sprint End | Date | Decision Required |
|---|---|---|
| Sprint 0 | 2026-04-27 | Confirm Sprint 0 items are shipped. Unblock Sprint 1. |
| Sprint 1 | 2026-05-10 | Confirm Sprint 1 acceptance: all P0/P1 security gaps closed (incl. LDAP+BMC encryption per S1-15/S1-16), test infrastructure delivered. Unblock Sprint 2. (Log retention defaults locked in `decisions.md` D2.) |
| Sprint 2 | 2026-05-24 | Confirm `tags[]`/NodeGroup migration landed cleanly per `decisions.md` D3 (dual-emit through v1.0, field removal in v1.1). Image factory consolidation per D5 verified against test suite. |
| Sprint 3 | 2026-06-07 | RBAC end-to-end check on the dev cluster: bootstrap admin, create operator, scope to NodeGroup, verify operator CANNOT touch out-of-scope nodes. Audit log surfaces actor on every state change. (RBAC model locked in D1.) |
| Sprint 4 | 2026-06-21 | Production-readiness checkpoint. Review Prometheus metrics, backup/restore results, disk space pre-flight. Verify `docs/install.md` (S4-14) is complete. Go/no-go on first external operator install. (Per D8: if a design partner has signed an LOI by now, engage human pen-test for Sprint 5-6 window.) |
| Sprint 5 | 2026-07-05 | Launch-readiness review. Persona walkthrough: can each persona type complete their primary workflow? Show HN scheduled per D16 for 2026-07-27. |
| Sprint 6 | 2026-07-19 | v1.0 ship/no-ship decision. Binary criterion: all P0 security items closed, CI green (incl. `gosec`/`trivy`/`govulncheck` per S3-12), lab gate green, release checklist complete, install + upgrade docs complete. |

---

## 14. Open Questions — RESOLVED

All Q1–Q6 questions previously listed here have been resolved by Richard in `docs/decisions.md` (2026-04-26) under founder delegation. Summary:

- **Q1 (RBAC / LDAP-passthrough):** Resolved in D1 — clustr owns its own users table, operator RBAC is independent of LDAP. LDAP is for cluster HPC accounts only. OIDC/SAML deferred to v1.1+.
- **Q2 (Show HN timing):** Resolved in D16 — independent from tunnl, target 2026-07-27 (1 week after v1.0 ship). No pre-launch beta.
- **Q3 (Security review):** Resolved in D8 — `gosec`/`trivy`/`govulncheck` baseline mandatory in Sprint 3. Human pen-test conditional on signed design partner by end of Sprint 5.
- **Q4 (Pricing/positioning):** Routed to Monica's lane. Not blocking sprint execution. Initial answer: MIT OSS, no SaaS, no paid tier at v1.0; commercial support/consulting opens post-v1.0 once demand is real.
- **Q5 (Audit log retention/compliance):** Resolved in D13 — 90-day retention default, no SIEM export in v1.0, JSONL export endpoint in v1.1 if a regulated customer signs.
- **Q6 (Hiring trigger):** Resolved in D15 — first design partner with >50 nodes signs an LOI.

New escalations or scope changes during sprint execution: route to Richard via the standard agent loop. Do not re-open these without a written counter-rationale.

---

*End of plan. Total estimated engineering scope: approximately 20–24 weeks of individual-contributor effort, compressed to 13 weeks via the AI agent team and the founder's direction layer. The critical path runs through: Sprint 1 security foundation → Sprint 3 RBAC → Sprint 4 observability → Sprint 6 release artifacts. Slipping the RBAC sprint is the highest-risk delay because it gates the multi-user story that makes clustr viable for Persona A (the highest-value market segment).*
