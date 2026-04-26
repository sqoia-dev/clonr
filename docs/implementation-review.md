# clustr Implementation Review

**Review date:** 2026-04-25  
**Reviewer:** Dinesh (Product Engineer)  
**Scope:** Implementation quality only — not architecture (Richard's lane), not infra (Gilfoyle's lane)  
**Feeds:** 90-day sprint plan  
**Codebase snapshot:** `staging/clustr/` on main, post boot-architecture work (ADR-0008 / §10)

---

## Methodology

Full read of all 184 Go source files, targeted read of hot zones identified in the brief, live API route tracing, cross-referencing the webui-review P0 findings against server routing, and inspection of all 39 test files. Line citations are exact offsets from the files as they exist on main today.

---

## 1. Code Quality Assessment

### `internal/deploy/rsync.go` — **SOLID**

The 2,443-line file is the largest in the codebase and arguably the most complex. Quality is good:

- Phase structure is clear: preflight → RAID → partition → format → mount → download (concurrent) → extract → unmount → finalize. Each phase is bookended with `reportStep()` + `opts.Reporter.StartPhase/EndPhase` calls.
- The concurrent blob prefetch (start HTTP while partitioning) is correct — the goroutine writes to a buffered channel and the main goroutine blocks on receive after the disk ops complete.
- Rollback on failure is present and correct: `backupPartitionTable` saves the partition table, `doRollback` restores it on any early-exit path, and the backup is only removed on clean success.
- Retry loop for stream failures (up to `maxDownloadAttempts`) is correct and includes exponential backoff.
- `http.DefaultClient` is used for blob downloads (lines 216, 489, 1995). This carries a zero read timeout by default — a stalled server can hang the deploy agent indefinitely. Not a crash risk today but will become a support issue on flaky networks.

Issues:
- **`rsync.go:526–553`**: Debug log blocks for ESP EFI directory contents are `Info`-level log calls wrapped in `Debug()` calls (actually using `logger().Debug()`) — this is fine, but there are two of them left in the hot path after the boot architecture work. They should be removed or downgraded once the UEFI boot issue is resolved, otherwise they clutter deploy logs in production.

### `internal/power/proxmox/provider.go` — **SOLID**

Clean, well-scoped file. The stop+start dance for Proxmox config commit is correct and the `waitForStatus` poll loop handles context cancellation properly via `select`. Thread safety is correct: `mu sync.Mutex` protects the ticket/csrf cache; `ensureTicket` holds the lock for the entire re-auth window.

One minor issue: `setBootOrder` hardcodes `"order=net0;scsi0"` and `"order=scsi0;net0"` as string literals (lines 297–299). If a VM has its disk on `virtio0` or `ide0` instead of `scsi0`, the boot order write will silently succeed but not have the intended effect. The field name is a Proxmox construct, not a standardized one. This is architectural scope for Richard but the hardcoded string is an implementation smell — it should at minimum be documented as a known constraint.

Test coverage: The `provider_test.go` file is excellent — uses `httptest.Server`, asserts call ordering, covers stop/start on running VM, config-only on stopped VM, boot order string values, and stop-failure error propagation. This is the strongest-tested module in the codebase.

### `internal/server/handlers/nodes.go` — **NEEDS WORK**

Well-structured overall but contains one correctness bug (documented in §3 below) and one missing validation:

- **`nodes.go:276` — groupID preservation logic is wrong.** The condition is:
  ```go
  if req.GroupID != "" || req.GroupID == "" && existing.GroupID != "" {
      groupID = req.GroupID
  }
  ```
  Due to operator precedence this evaluates as:
  ```go
  if req.GroupID != "" || (req.GroupID == "" && existing.GroupID != "") {
  ```
  When `req.GroupID == ""` (omitted from PUT body) and `existing.GroupID = "some-group-id"`, the condition is `false || (true && true) = true`, so `groupID = req.GroupID = ""`. The existing group assignment is silently cleared on every PUT that doesn't explicitly include `group_id`. The developer intent (preserve the existing group) is inverted.

- `CreateNode` handler (`nodes.go:139–195`) does not validate that `BaseImageID` refers to an existing image before inserting. A node can be created with a dangling `base_image_id` that points to nothing.

- `UpdateNode` handler (`nodes.go:318`) returns the `cfg` struct built in the handler, not the post-update DB read. This means the response `updated_at` is computed in the handler (`time.Now().UTC()`) which is correct, but if the DB write triggers a trigger or computed column, the response would not reflect it. Minor, but inconsistent with `GetNode` which always returns the DB-authoritative state.

- `RegisterNode` (`nodes.go:345`) calls `h.DB.ListNodeConfigs(r.Context(), "")` to build the cluster hosts injection for every registration. On a 200-node cluster this is a full table scan on every PXE boot. This is a performance concern at scale, not a correctness issue.

### `internal/db/db.go` — **SOLID**

The DB layer is well-disciplined:
- All queries use parameterized `ExecContext`/`QueryContext` — no string interpolation, no SQL injection surface.
- `requireOneRow` is used consistently after every `UPDATE`/`DELETE` to detect not-found vs. success.
- `scanBaseImage`, `scanNodeConfig`, and `scanReimageRequest` are centralized scan functions — column lists don't drift.
- `RecordVerifyBooted` two-step pattern is correct: SELECT to check existing state, then conditional UPDATE. The comment explaining why CASE-expression RowsAffected is unreliable on SQLite is accurate and saves the next developer from re-learning it.
- `nodeConfigCols` / `nodeConfigColsJoined` constants keep SELECT lists in sync with schema. Good.

Minor issues:
- **`db.go:510–576` — `UpsertNodeByMAC` is not atomic.** It does a SELECT followed by INSERT or UPDATE in two separate statements without a transaction. Two concurrent PXE boots from the same MAC (possible during a rapid double-reboot) could both see "not found" and both try to INSERT, with one succeeding and the other returning a UNIQUE constraint error on `primary_mac`. SQLite's `MaxOpenConns(1)` reduces this window but does not eliminate it. The correct fix is `INSERT OR REPLACE` or wrapping the check+insert in a `BEGIN EXCLUSIVE` transaction.
- **`db.go:117–130` — `flushLastUsed` silently swallows all errors.** `tx.Rollback()` returns are discarded. `stmt.Exec` errors are discarded. If the WAL is locked or the DB is read-only, last_used_at updates silently drop. This is acceptable for a best-effort flush but should at minimum log on error.
- **`db.go:172–181` — Migration rename map** uses `db.sql.Exec` (line 180) without error checking. If a rename fails (e.g., the old name doesn't exist), the failure is silently swallowed. This is benign for new installs but could leave an existing DB with a wrong migration name tracked, causing the renamed migration to be re-applied.

### `internal/image/factory.go` — **NEEDS WORK**

The five async finalize paths (`pullAsync`, `importISOAsync`, `captureAsync`, `buildFromISOAsync`, and the inline path via `buildFromISOFile`) all follow the same pattern: extract rootfs → `bakeDeterministicTar` → `SetBlobPath` → `FinalizeBaseImage`. The pattern is correct and each path handles errors consistently. However:

- **The pattern is copy-pasted across five async functions.** If a new step needs to be added to the finalize sequence (e.g., triggering an initramfs rebuild, as Gilfoyle's review suggests), it must be added five times. There is no `finalizeImage(imageID string, rootfs string)` helper. This is the primary refactor candidate for this file.
- `cap_` naming at `factory.go:113` is an antipattern (trailing underscore to avoid shadowing the builtin `cap`). The variable should be renamed to `semCapacity` or `maxBuilds`.
- `captureAsync` (`factory.go:690`) uses `sshpass` by branching on `req.SSHPassword != ""`. If `sshpass` is not installed, the error message from the deploy agent is "exec: sshpass not found" — not actionable for operators. There should be a pre-flight check for `sshpass` availability before accepting a capture request with a password.
- `rejectSelfCapture` (`factory.go:643`) performs a DNS lookup to detect self-capture. If the DNS lookup fails (`net.LookupHost` returns error), the function returns `nil` — i.e., it allows the capture to proceed. The comment says "let rsync surface the error," which is reasonable, but it means the self-capture guard is silently bypassed on DNS failure.

### `internal/server/handlers/reimage.go` — **SOLID**

Clean implementation. The pre-checks (image status, active reimage detection) are correctly ordered before the DB write. The `Force` flag bypasses both checks deliberately and the code makes this visible. `RequestedBy` is hardcoded to `"api"` (`reimage.go:107`) regardless of the authenticated user — this is the P1-9 audit trail gap from Gilfoyle's review. The fix is simple: populate from `actorLabel(r.Context())` defined in `middleware.go`.

### `internal/server/server.go` — **SOLID**

Router construction is well-organized. Auth scope enforcement is applied correctly — node-scoped callbacks are outside the `requireScope(true)` group, admin-only endpoints are inside. The `flipNodeToDiskFirst` function (`server.go:270`) is cleanly factored. Background workers are started after router construction and before traffic (correct startup order).

One issue: `runVerifyTimeoutScanner` (`server.go:207`) ticks every 60 seconds. For each timed-out node it calls `flipNodeToDiskFirst` which for Proxmox triggers a full `stop → waitForStatus → start` cycle (up to 60 seconds each). If 10 nodes time out simultaneously the scanner goroutine will block for up to 10 minutes per tick processing them sequentially. For a production 200-node cluster this could cause cascading delays. The fix is to fan out `flipNodeToDiskFirst` into goroutines inside the scanner loop — this is a medium-effort improvement.

### `internal/server/progress.go` — **SOLID**

`ProgressStore` is correctly implemented: `sync.RWMutex` for state, `sync.RWMutex` for subscribers, non-blocking publish (slow consumers are dropped rather than blocking the caller). The cleanup goroutine runs every 5 minutes with a 30-minute retention window. Clean.

### `internal/server/handlers/progress.go` — **SOLID**

Handler implementation is correct. The SSE stream implementation at `StreamProgress` sends an initial snapshot on connect so the UI doesn't miss state that was set before the subscription was established. Good pattern.

### `internal/server/middleware.go` — **SOLID**

Auth middleware is well-structured. `imageAccessCache` uses `sync.Map` keyed by `"nodeID:imageID"` with a 60-second TTL to avoid a DB round-trip on every blob chunk download. Correct.

### `internal/server/ui/static/js/app.js` — **HOT MESS**

8,005 lines of vanilla JS with no build step, no module system, no type checking. The webui-review document already covered the anti-patterns exhaustively. From an implementation standpoint:

- **No error boundaries.** An unhandled exception in any `Pages.*` function can leave the entire SPA in an uncaught-exception state with no recovery path.
- **Global state everywhere.** `App._cache`, `Pages._reimagePollTimer`, `Pages._progressSource` are stored directly on the module objects. This works for a single-page SPA but is fragile as the feature set grows.
- **Event listener cleanup is route-coupled.** The router at `app.js:33–39` explicitly cleans up named page-level listeners by function reference. Any new page that registers a listener must add cleanup here or leak it. There is no lifecycle interface.
- **`escHtml()` used as event handler argument injection** (noted in webui-review). No XSS today due to same-origin authenticated context, but will cause breakage when hostnames contain apostrophes or backslashes.
- The framework debt is real: any feature work that touches the nodes list or images grid requires reading and modifying template literal strings with inline style attributes. The investment to move to a lightweight framework (Alpine.js, Svelte, or even a module-based vanilla approach) pays off immediately when the first two or three feature sprints land.

---

## 2. Specific Bugs Found

### BUG-1 — `internal/server/handlers/nodes.go:276` — GroupID silently cleared on PUT (SEVERITY: HIGH)

**File:line:** `internal/server/handlers/nodes.go:276`

```go
groupID := existing.GroupID
if req.GroupID != "" || req.GroupID == "" && existing.GroupID != "" {
    groupID = req.GroupID
}
```

**What happens:** When any PUT to `/api/v1/nodes/{id}` omits `group_id` (empty string, the zero value), the condition evaluates to `true` (because `req.GroupID == ""` AND `existing.GroupID != ""`), setting `groupID = req.GroupID = ""`. The existing group assignment is cleared.

**Impact:** Every admin PUT to update a node's hostname, image, or power provider silently removes its NodeGroup assignment. This breaks group-level reimages, network profiles, and disk layout inheritance for nodes managed via the node-list edit modal (which does not include `group_id`).

**Fix:** The condition should be:
```go
groupID := existing.GroupID
if req.GroupID != "" {
    groupID = req.GroupID
}
// To explicitly clear: add a ClearGroupID bool field analogous to ClearPowerProvider
```

**Test coverage:** Zero. There is no test for `UpdateNode` with `GroupID` preservation. The `server_test.go` file covers `PowerProvider` preservation (which has a `ClearPowerProvider` bool guard and works correctly) but the analogous `GroupID` path has no test.

---

### BUG-2 — `internal/server/ui/static/js/api.js:330` vs `internal/server/server.go:694` — Progress endpoint path mismatch (SEVERITY: HIGH)

**Root cause (confirmed):** The frontend calls `API.progress.list()` which maps to `GET /api/v1/deploy/progress` (per `api.js:330`). The server routes this correctly. However, `app.js:463` calls `API.progress.list()` in the dashboard init — and the webui-review reported 404 in live testing. Investigation shows the route IS registered at `server.go:694` under the admin-only `r.Group(func(r chi.Router) { r.Use(requireScope(true)) ... })`. The `POST /deploy/progress` (progress ingest from deploy agent) is outside this group at `server.go:511`, but the `GET /deploy/progress` endpoints are inside the admin-only group.

**Impact:** This is the P0-1 finding from Gilfoyle's webui review confirmed at source. The active deploy polling on the dashboard returns 404 if the auth middleware's `requireScope(true)` applies before the session cookie is validated, or if the session is in a partial state. More specifically: the live test was done with a valid session, which means the scope check passed but something in the route resolution produced 404. The ingest endpoint (POST) is registered outside the admin group at `server.go:511`. The list/stream/get endpoints are registered inside the admin group at `server.go:692–694`. This is the correct registration — a 404 from a valid admin session would indicate either a routing ambiguity between the inner and outer POST registration (the `POST /deploy/progress` inside `r.Post("/deploy/progress", progress.IngestProgress)` at line 511 is outside the admin group, while the GET routes are inside) or the UI was testing without valid admin auth. The actual P0-1 issue needs a targeted repro with curl to confirm whether the 404 is a stale client or a real route gap.

**Action:** Requires explicit curl test with a valid admin session key. File this as a bug requiring reproduction before sprint-1.

---

### BUG-3 — `internal/db/db.go:510–575` — `UpsertNodeByMAC` is a non-atomic read-then-write (SEVERITY: MEDIUM)

**File:line:** `internal/db/db.go:521` (SELECT) and `db.go:557` (INSERT)

Two separate statements without a transaction. Concurrent PXE boots from the same node — possible during rapid cold cycles or dual-boot testing — can both hit the SELECT with `ErrNotFound` and both attempt INSERT. One will succeed; the other will return a SQLite UNIQUE constraint violation on `primary_mac`.

**Impact:** The error surfaces to the deploy agent as a 500, which retries — so it's recoverable. But it causes a spurious deploy failure log and a confused operator. On the dev Proxmox cluster with automated VM resets this will occur.

**Fix:** Replace with `INSERT OR IGNORE` followed by `UPDATE`, or wrap in `BEGIN EXCLUSIVE` transaction.

---

### BUG-4 — `internal/image/factory.go:690` — `captureAsync` uses `sshpass` without pre-flight check (SEVERITY: MEDIUM)

**File:line:** `internal/image/factory.go:750–756`

When `req.SSHPassword != ""`, the capture goroutine constructs `exec.CommandContext(ctx, "sshpass", ...)`. If `sshpass` is not installed on the server host, the goroutine fails after the API has already returned 200 Accepted, and the image record is left in `building` state with an error message like `"rsync failed: exec: sshpass: executable file not found in $PATH"`.

**Impact:** Operator submits a capture, the API says "started," the image never transitions to ready. The operator has no indication of what went wrong without reading logs.

**Fix:** Add a pre-flight `exec.LookPath("sshpass")` check in `CaptureNode` before returning the "building" response, and return a synchronous 400 error if `sshpass` is absent and a password was provided.

---

### BUG-5 — `internal/server/handlers/reimage.go:107` — `requested_by` hardcoded to `"api"` (SEVERITY: LOW/MEDIUM)

**File:line:** `internal/server/handlers/reimage.go:107`

```go
RequestedBy: "api",
```

Every reimage request is attributed to `"api"` regardless of which user or API key initiated it. The `actorLabel(r.Context())` function in `middleware.go` returns `"user:<id>"` or `"key:<label>"` depending on how the request was authenticated. This is already available in the handler context.

**Impact:** No audit trail. On a shared cluster, operators cannot determine who triggered a reimage. This is the P1-9 finding from the webui-review.

**Fix:** One line change:
```go
RequestedBy: actorLabel(r.Context()),
```

---

## 3. Test Gap Analysis

**Total test functions: 268 across 39 test files.**

### Critical paths with zero coverage

| Package | Coverage gap | Risk |
|---|---|---|
| `internal/ldap` | Zero tests | LDAP auth, DN lock, sssd.conf generation — entire feature untested |
| `internal/network` | Zero tests | Network profile resolution, NM keyfile generation |
| `internal/slurm` | Zero tests | Slurm config rendering, upgrade orchestration |
| `internal/sysaccounts` | Zero tests | System account injection into deployed FS |
| `internal/power/ipmi` | Zero tests | IPMI power control — the non-Proxmox path |
| `internal/clientd` | Zero tests | WebSocket client daemon, config apply, heartbeat |
| `handlers/nodes.go UpdateNode` | No GroupID preservation test | BUG-1 above went undetected |
| `handlers/reimage.go` | No end-to-end reimage API test | Orchestrator trigger path untested via HTTP |
| `internal/deploy/finalize.go` | No unit tests | `applyNodeConfig` — hostname, fstab, grub, SSH key injection |
| `internal/deploy/efiboot.go` | No unit tests | NVRAM entry parsing and manipulation |

### Well-covered areas

- `internal/server/verify_boot_test.go`: ADR-0008 state machine has excellent coverage — 6 scenarios including heartbeat idempotency, wrong-node-key rejection, and timeout scanner.
- `internal/power/proxmox/provider_test.go`: Stop+start sequencing fully covered with mock Proxmox API.
- `internal/db/db_test.go`: Migration, node CRUD, API key hash/lookup covered.
- `internal/reimage/orchestrator_test.go`: Trigger and scheduler logic covered.
- `internal/deploy/rsync_test.go`: Partition device naming, disk selection covered.

### Test infrastructure quality

Tests use `t.TempDir()` correctly for isolation, `httptest.NewServer` for HTTP handler tests, and custom mock servers for external services (Proxmox). The `internal/deploy/testutil/` package provides loop device and rootfs helpers for integration-style deploy tests — this is good infrastructure that is underutilized.

---

## 4. API Issues

### API-1 — `/api/v1/progress` vs `/api/v1/deploy/progress` — historical naming confusion

The webui-review P0-1 finding references `app.js:462` calling `API.progress.list()`. The `api.js` defines this as `GET /api/v1/deploy/progress` (correct, matches server routing at `server.go:694`). The webui-review's live 404 was not reproduced in code review — the routes appear correctly registered. The issue may have been a stale browser cache or auth state issue during live testing. However, the confusion stems from the fact that the ingest endpoint (`POST /deploy/progress`) is registered *outside* the admin group (at `server.go:511`) while the read endpoints are *inside*. This is correct behavior — deploy agents use node-scoped keys, which lack `admin` scope. The asymmetry is intentional but undocumented, which will cause future confusion.

**Action:** Document the auth scope split for `/deploy/progress` in a comment adjacent to the route registrations.

### API-2 — `PUT /api/v1/nodes/{id}` silently clears GroupID (confirmed bug — same as BUG-1)

The node list modal omits `group_id` entirely from the PUT body. Combined with BUG-1, this means every edit from the node list modal silently clears the group assignment. The UI/API contract is: if `group_id` is absent from the request, preserve the existing value. The current implementation does the opposite.

### API-3 — `PUT /api/v1/nodes/{id}` response is from the handler, not the DB

`UpdateNode` (`nodes.go:312`) calls `h.DB.UpdateNodeConfig`, then returns the locally-constructed `cfg` struct rather than re-fetching from the DB. If the DB or any middleware modifies the record (e.g., a trigger, a future constraint), the response would be stale. Consistent with how `CreateNode` works, but inconsistent with `GetNode`. Low risk today.

### API-4 — No pagination on `/api/v1/nodes` or `/api/v1/images`

Both endpoints return all records in a single response. Noted in webui-review as P2-17. At 200 nodes with full `hardware_profile` JSON blobs (which can be 2–5 KB each), the payload can reach 1MB+ per request. The `ListNodes` DB query has no LIMIT or cursor. This is a pre-launch blocker for large clusters.

### API-5 — `scheduled_at` accepted by API, ignored in UI (confirmed UI gap)

`reimage.go:105` correctly handles `scheduled_at` from the request body. The UI never sends it. This is the P1-2 finding from webui-review — confirmed as a UI gap, not an API bug.

### API-6 — `CreateNode` does not validate `BaseImageID` existence

`CreateNode` (`nodes.go:188`) inserts the node with the provided `base_image_id` without checking whether the image exists. A foreign key constraint should prevent this if `PRAGMA foreign_keys=on` is in effect. The DSN at `db.go:46` includes `_foreign_keys=on`, so the DB will reject an invalid `base_image_id`. However, the error will bubble up as a generic 500 (SQLite FOREIGN KEY constraint violation) rather than a 400 with a meaningful message. The handler should validate explicitly and return a 400.

---

## 5. Quick Wins Backlog

| ID | Finding | File:Line | Effort | Severity |
|---|---|---|---|---|
| QW-1 | Fix GroupID preservation bug in UpdateNode | `nodes.go:276` | 15 min | HIGH |
| QW-2 | Populate `requested_by` from auth context | `reimage.go:107` | 5 min | MEDIUM |
| QW-3 | Pre-flight `sshpass` check before capture-with-password | `factory.go:750` | 30 min | MEDIUM |
| QW-4 | `UpsertNodeByMAC` — use INSERT OR IGNORE + UPDATE | `db.go:557` | 1 hour | MEDIUM |
| QW-5 | Wrap `finalizeImage` helper across 5 async paths | `factory.go:226–815` | 2–3 hours | MEDIUM |
| QW-6 | Add GroupID preservation test for UpdateNode | `server_test.go` | 1 hour | HIGH (prevents regression) |
| QW-7 | Validate `BaseImageID` exists in `CreateNode` | `nodes.go:188` | 30 min | LOW |
| QW-8 | Fix migration rename error handling | `db.go:180` | 10 min | LOW |
| QW-9 | Replace `cap_` with `semCapacity` in factory | `factory.go:113` | 5 min | LOW |
| QW-10 | Remove debug ESP log blocks post-boot-arch | `rsync.go:526–553` | 10 min | LOW |
| QW-11 | Add scanner fan-out for `flipNodeToDiskFirst` | `server.go:228–252` | 2 hours | MEDIUM |
| QW-12 | Document auth scope split for `/deploy/progress` | `server.go:509–512` | 10 min | LOW |
| QW-13 | Log errors in `flushLastUsed` | `db.go:117–130` | 15 min | LOW |
| QW-14 | `http.DefaultClient` in deploy — add read timeout | `rsync.go:216,489` | 30 min | MEDIUM |

---

## 6. Refactor Candidates

### R-1 — `internal/image/factory.go` — Extract `finalizeImageFromRootfs` helper

**Current state:** Five async functions (`pullAsync`, `importISOAsync`, `captureAsync`, `buildFromISOAsync`, inline via `buildFromISOFile`) all end with:
1. `bakeDeterministicTar`
2. `Store.SetBlobPath`
3. `Store.FinalizeBaseImage`

Any new post-finalization step (initramfs rebuild signal, webhook, metadata flush) must be added in five places.

**Proposed refactor:**
```go
func (f *Factory) finalizeImageFromRootfs(ctx context.Context, imageID, imageRoot, rootfs string) error {
    tarPath, tarChecksum, tarSize, err := f.bakeDeterministicTar(ctx, imageID, imageRoot, rootfs)
    if err != nil { return fmt.Errorf("bake tar: %w", err) }
    if err := f.Store.SetBlobPath(ctx, imageID, tarPath); err != nil { return err }
    return f.Store.FinalizeBaseImage(ctx, imageID, tarSize, tarChecksum)
}
```
Each async function's tail becomes a single call. Estimated reduction: ~60 lines of duplicated error handling.

### R-2 — `internal/server/ui/static/js/app.js` — Module split and lifecycle interface

**Current state:** 8,005 lines, no module system, no lifecycle interface. Event listener cleanup is centralized in the router and requires the router to know every page-level listener by function reference.

**Proposed refactor:** Not a "move to React" exercise — the current approach can be improved significantly without a framework:
1. Split by feature area into ES module files (`nodes.js`, `images.js`, `reimages.js`, `settings.js`, `dashboard.js`) matching the existing module objects.
2. Define a `Page` interface: `{ init(params), cleanup() }` that each page implements.
3. Router calls `currentPage.cleanup()` before `nextPage.init(params)` — no more per-listener cleanup list in the router.
4. Move inline `style=` attributes into CSS classes progressively as files are touched.

This is a sprint-2+ effort but prevents the file from reaching 15K lines by the end of the 90-day plan.

### R-3 — `internal/db/db.go` — Replace string-constant `nodeConfigCols` with structured scan

**Current state:** `nodeConfigCols` and `nodeConfigColsJoined` are string constants (lines 592–612). Every `SELECT` that uses them has the same column count assumption. Adding a column requires updating the constant AND the scan function AND verifying the column indices match.

**Note:** The current approach is explicit and readable — this is a "nice to have" not a blocker. The risk is that a future migration adds a column, someone updates the scan function but forgets to update `nodeConfigCols`, and queries start silently misaligning columns. The fix is to add a compile-time test that SELECTs and scans a node, verifying the column count matches, but this is low-urgency.

---

## 7. Tech Debt Actively Costing Velocity

### TD-1 — Zero test coverage for `ldap`, `network`, `slurm`, `sysaccounts`

These are four of the five most-used feature modules for the HPC persona. Any change to them requires manual validation on a live cluster because there are no unit tests. Feature development in these modules is slow and risky. One sprint of test infrastructure (mock LDAP server, network profile fixtures, slurm config golden file tests) unlocks confident iteration.

### TD-2 — `app.js` 8K-line monolith blocks parallel frontend work

Two developers cannot work on different sections of `app.js` without constant merge conflicts. Until the file is split, frontend feature velocity is single-threaded. This is actively costing sprint throughput.

### TD-3 — `UpdateNodeConfig` response inconsistency

`UpdateNode` returns the handler-constructed struct, not the DB-read struct. As the node config grows in complexity (more derived fields, computed state), this inconsistency will cause subtle bugs where the UI shows stale data after a PUT. This will surface as a confusing bug in a sprint that adds a new computed field.

### TD-4 — `UpsertNodeByMAC` non-atomic read-then-write

This will eventually cause a silent 500 in a production environment with automated node management or rapid node cycling. The fix is small (< 1 hour) but the symptom is confusing and will be hard to diagnose after the fact.

### TD-5 — No pagination on node/image list endpoints

At 50+ nodes the payload reaches a size that causes noticeable UI lag. At 200 nodes it will timeout or OOM the browser tab. This needs to land before the first external large-cluster user.

---

## Summary

**Package-level assessment:**

| Package | Assessment |
|---|---|
| `internal/deploy/rsync.go` | Solid — minor: DefaultClient timeout, debug log cleanup |
| `internal/power/proxmox/provider.go` | Solid — best-tested module in the codebase |
| `internal/server/handlers/nodes.go` | Needs work — BUG-1 (GroupID clear) is a correctness bug that silently corrupts state |
| `internal/db/db.go` | Solid — non-atomic UpsertNodeByMAC and flushLastUsed error swallow need fixes |
| `internal/image/factory.go` | Needs work — duplicate finalize pattern, sshpass pre-flight missing |
| `internal/server/server.go` | Solid — scanner fan-out needed at scale |
| `internal/server/handlers/reimage.go` | Solid — one-liner audit fix needed |
| `internal/server/ui/static/js/app.js` | Hot mess — functional but unscalable; framework migration needed in sprint 2–3 |
| `internal/ldap/`, `internal/network/`, `internal/slurm/` | Cannot assess — zero tests make quality unknown |

**Top 5 critical findings:**

1. **BUG-1** (`nodes.go:276`): GroupID cleared on every PUT that omits `group_id`. Silent data corruption. Fix is trivial; risk of leaving it is fleet-wide group assignment loss.
2. **BUG-3** (`db.go:521`): `UpsertNodeByMAC` TOCTOU race. Will cause 500 errors on rapid node cycling. Fix is small.
3. **BUG-4** (`factory.go:750`): Capture with SSH password accepts the request and silently fails if `sshpass` is not installed. Bad operator UX.
4. **TD-1**: Four major feature modules (`ldap`, `network`, `slurm`, `sysaccounts`) have zero tests. Every PR to those packages ships untested code to production.
5. **TD-5**: No pagination on `/api/v1/nodes` or `/api/v1/images`. Pre-launch blocker for any cluster > 50 nodes.

**Blocker before sprint plan is built:**

BUG-1 (`nodes.go:276`) is already in production on the dev cluster. Any operator that has assigned nodes to NodeGroups and then edits those nodes via the UI (which uses the PUT endpoint without `group_id`) will silently lose the group assignment. This should be fixed before the 90-day plan is finalized because it affects the reliability of the group-reimage workflows that the sprint plan is likely to build on.
