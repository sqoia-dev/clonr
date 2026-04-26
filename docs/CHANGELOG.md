# clustr Changelog

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

*Next sprint: Sprint 3 — RBAC + Audit Trail (2026-05-25)*
