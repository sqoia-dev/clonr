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

*Next sprint: Sprint 2 — Image Factory + Tags Model (2026-05-11)*
