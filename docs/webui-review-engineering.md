# WebUI Engineering Review

**Scope:** Engineering lens — bugs, dead code, inconsistent patterns, coverage gaps, tech debt.  
**Reviewer:** Dinesh (Product Engineer)  
**Date:** 2026-04-27  
**Branch:** main  
**Files reviewed:** `internal/server/ui/static/js/app.js` (~9,350 lines), `api.js` (444 lines),
`logs.js`, `ldap.js`, `network.js`, `slurm.js`, `sysaccounts.js`, `index.html`,
`internal/server/server.go` (routing), `handlers/webhooks.go`, `handlers/audit.go`,
`handlers/nodes.go` (header).

---

## Category A — Bugs

### A-1 `fmtBytes` variable shadowed inside heartbeat render, producing inconsistent units
**Severity:** P1  
**Where:** `app.js` ~line 3921 (inside the heartbeat/health card render function)  
**What's wrong:** A local `const fmtBytes` is declared inside the render closure using SI divisors
(`1e9` for GB, `1e6` for MB). The outer global `fmtBytes` uses binary divisors (`1024**3` for
GiB, `1024**2` for MiB). Any byte values shown in the heartbeat panel (disk, memory) report in
decimal gigabytes while every other panel reports binary gibibytes. On a 16 GiB machine the
heartbeat reads "17.2 GB" while the image detail page reads "16.0 GiB" — same data, different
numbers.  
**Suggested fix:** Remove the inner `fmtBytes` declaration and let the closure use the global.
If SI units are intentional for that panel, rename the local to `fmtBytesSI` and leave a comment.

---

### A-2 `_removeImageTag` uses `console.error` on failure — no user feedback
**Severity:** P1  
**Where:** `app.js` ~line 2264 (tag chip remove handler in image detail page)  
**What's wrong:** When the DELETE tag API call fails, the handler calls `console.error(e)` and
returns silently. The chip is still removed from the DOM optimistically before the call, so the
UI shows the tag as deleted while the server still has it. On next page load the tag reappears
with no explanation.  
**Suggested fix:** Move the DOM removal to the success path (after `await`), and on failure call
`App.toast('Failed to remove tag: ' + e.message, 'error')`.

---

### A-3 `showDeleteImageModal` calls raw `API.get('/nodes', …)` instead of `API.nodes.list()`
**Severity:** P2  
**Where:** `app.js` ~line 1871  
**What's wrong:** One call site bypasses the typed `API.nodes.list()` wrapper and calls the
internal `API.get` helper directly. If the nodes endpoint path or response envelope changes,
this call site will be missed in refactors. It also skips any request options that `API.nodes.list`
applies.  
**Suggested fix:** Replace with `API.nodes.list()` for consistency with every other node fetch
in the codebase.

---

### A-4 Config history `changed_at` treated as Unix integer — will produce wrong dates if server returns ISO string
**Severity:** P1  
**Where:** `app.js` ~line 6282 (node config history table row builder)  
**What's wrong:** The code uses `new Date(r.changed_at * 1000)` which treats the timestamp
as a Unix epoch in seconds. The audit handler (`handlers/audit.go`) and all other handlers in
this repo return RFC3339 strings. If `changed_at` is also RFC3339, multiplying by 1000 produces
a date in the year ~52,000. Needs verification against the actual handler response, but the
pattern is inconsistent with every other timestamp in the codebase.  
**Suggested fix:** Use `fmtDate(r.changed_at)` which already handles ISO strings, consistent
with the rest of the codebase. If the field really is Unix seconds, document it explicitly in the
handler.

---

### A-5 `_deployProgressTable` silently hard-caps at 20 entries with no overflow indicator
**Severity:** P2  
**Where:** `app.js` ~line 931  
**What's wrong:** The live deploy progress table renders at most 20 rows and drops the rest
without any indication to the user that entries are hidden. In a large cluster running parallel
reimages, entries beyond the cap are invisible.  
**Suggested fix:** After the cap, append a row like `+ N more…` or raise the cap and add
virtual scrolling. At minimum, show a count above the table.

---

### A-6 `_diffTable` auto-refresh tick silently no-ops when empty-state was rendered
**Severity:** P2  
**Where:** `app.js` ~lines 900-906  
**What's wrong:** `_diffTable` locates the `<tbody>` with `container.querySelector('tbody')`.
If the container previously rendered an empty-state `<div>` (no `<tbody>`), the query returns
`null` and the refresh tick does nothing — the table never populates even when data arrives.
**Suggested fix:** Have the auto-refresh callback re-render the full table structure (not just
rows), or ensure the empty-state path always renders an empty `<tbody>` inside the table.

---

### A-7 Node detail tab saves race — concurrent tab saves overwrite each other
**Severity:** P1  
**Where:** `app.js` ~lines 4804-4806 (save handler for node detail tabs)  
**What's wrong:** Each tab save does `GET /nodes/{id}` then merges its fields and issues
`PUT /nodes/{id}`. If two tabs are saved within the same round-trip window (possible with a
fast admin pressing "Save" on two tabs in quick succession), the second GET reads stale state
and the PUT overwrites the first save's changes.  
**Suggested fix:** Use per-field PATCH endpoints (if available) or implement optimistic
locking using `updated_at` ETag/If-Match semantics. Short-term: disable the save button on all
tabs while any save is in flight.

---

### A-8 `_pollGroupReimageJob` never stops if modal is closed during polling
**Severity:** P2  
**Where:** `app.js` ~lines 7418-7430  
**What's wrong:** `_pollGroupReimageJob` schedules itself with `setTimeout` and recurses until
the job reaches a terminal state. If the operator closes the modal mid-poll, the timeout fires
anyway, calling DOM update functions on elements that no longer exist. The poll also holds
a reference to the removed modal, preventing GC.  
**Suggested fix:** Store the timeout handle in a module-level variable. Clear it when the modal
is removed (add a cleanup call to the close button and backdrop click handler).

---

### A-9 SSE `snapshot` handler registered twice on ISO build page
**Severity:** P2  
**Where:** `app.js` ~lines 7978 and 8010 (inside `_startIsoBuildSSE`)  
**What's wrong:** `es.addEventListener('snapshot', ...)` is registered at line ~7978 to apply
the initial state. A second listener is registered at line ~8010 solely to reset the error
counter. Both fire on every `snapshot` event. The first listener sets `_snapshotReceived = true`
but does not reset `_sseErrorCount`; the second does the reset. This ordering is fragile — if
the event fires before the second listener is attached (unlikely with synchronous registration,
but possible in future refactors) the error counter never resets.  
**Suggested fix:** Merge the two `snapshot` listeners into one.

---

### A-10 `Auth.boot` reads role from `/auth/me` but defaults to `'admin'` on any network error
**Severity:** P1  
**Where:** `app.js` ~line 9308 (`Auth._role = me.role || 'admin'`)  
**What's wrong:** If `/auth/me` fails (network error or unexpected shape), `Auth._role` stays at
its initialization value of `'admin'` (line 9341). An operator-role session whose `/auth/me`
call fails briefly will get full admin UI access until the next successful check. The comment
"still try to start the app; api.js will redirect on 401" is correct for auth failure, but a
transient network error will silently grant elevated UI permissions.  
**Suggested fix:** Default `Auth._role` to `'readonly'` at initialization. Promote to the actual
role only after a successful `/auth/me` response.

---

### A-11 `_settingsResetUserPassword` uses browser `prompt()` to collect a password
**Severity:** P2  
**Where:** `app.js` ~line 8843  
**What's wrong:** `prompt()` sends the entered value in plaintext in the dialog's DOM and is
not a password field — the input is visible as the user types. It also lacks any client-side
length validation until after the dialog is dismissed, giving a poor UX.  
**Suggested fix:** Replace with a modal that uses `<input type="password">` consistent with the
rest of the settings modals (`_settingsCreateUserModal` already does this correctly).

---

### A-12 `_settingsChangeUserRole` uses browser `prompt()` for role selection
**Severity:** P2  
**Where:** `app.js` ~line 8858  
**What's wrong:** Role selection via `prompt()` requires the admin to type a role name exactly.
Typos are caught only after the API call (which returns an error). There is no preview of what
the selected role can do.  
**Suggested fix:** Replace with a modal containing a `<select>` element, consistent with `_settingsCreateUserModal`.

---

### A-13 `escHtml` defined twice — once in `logs.js` (line 160) and once in `app.js`
**Severity:** P2  
**Where:** `logs.js` line 160; `app.js` (global scope, defined earlier in the file)  
**What's wrong:** Both definitions are identical. `logs.js` is loaded before `app.js`
(per `index.html` script order). The second definition in `app.js` overwrites the first.
This is harmless now but creates a maintenance hazard: if the implementations ever diverge,
the winner is determined solely by script load order.  
**Suggested fix:** Remove the duplicate in `logs.js`; import or rely on the global defined in
`app.js`. Or extract both to a shared `utils.js` module.

---

### A-14 ISO build cancel calls `API.images.delete` — no dedicated cancel endpoint
**Severity:** P2  
**Where:** `app.js` ~line 8091 (`_cancelIsoBuild`)  
**What's wrong:** Cancelling a build deletes the image record entirely. If the server has a
dedicated cancel/abort endpoint (e.g., `POST /images/{id}/cancel`), using delete bypasses any
graceful teardown (stopping the QEMU VM, cleaning up temp disk). If delete is truly the only
path, the UI button label "Cancel Build" is misleading — it should say "Abort and Delete".  
**Suggested fix:** Add a `POST /images/{id}/cancel` endpoint server-side. Update the UI to
call it. If that endpoint doesn't exist yet, rename the button "Cancel & Delete".

---

## Category B — Dead Code / Inconsistent Patterns

### B-1 `showShellHint` function is dead code — marked "Legacy fallback" and never called distinctly
**Severity:** P3  
**Where:** `app.js` ~line 2307  
**What's wrong:** The function is commented as "Legacy fallback — kept in case xterm.js fails
to load" but its entire body is just `openShellTerminal(imageId)` — it does not fall back to
anything different. No call site passes it a different argument or checks for xterm unavailability.  
**Suggested fix:** Delete `showShellHint`. If a real xterm fallback is needed later, add it
then with a meaningful implementation.

---

### B-2 `_updateIsoBuildProgress` is an explicit no-op kept as a placeholder
**Severity:** P3  
**Where:** `app.js` line 8078  
**What's wrong:** `_updateIsoBuildProgress() {}` with a comment "ISO builds now use SSE". The
function is not called from anywhere meaningful. It occupies space and confuses readers about
whether it was intentionally left blank or accidentally emptied.  
**Suggested fix:** Delete the function. If it was previously called from outside `Pages`, grep
for call sites first, then remove.

---

### B-3 Inline `confirm()` in `slurm.js` disable path instead of `showConfirmModal`
**Severity:** P3  
**Where:** `slurm.js` line 194  
**What's wrong:** `SlurmPages._bindSettingsEvents` uses `if (!confirm('Disable the Slurm module?...'))` 
— a blocking browser dialog. Every other destructive action in the codebase uses the custom
`Pages.showConfirmModal(...)` which is styled, non-blocking, and accessible.  
**Suggested fix:** Replace `confirm()` with `Pages.showConfirmModal(...)` consistent with the
pattern used for node delete, reimage cancel, etc.

---

### B-4 `sysaccounts.js` calls undefined `sysbage(a)` — likely a typo for a badge helper
**Severity:** P1  
**Where:** `sysaccounts.js` line 55  
**What's wrong:** `sysbage(a)` is called in the accounts table row template. No function named
`sysbage` exists anywhere in the codebase. This will throw `ReferenceError: sysbage is not
defined` at runtime whenever the System Accounts page is loaded, preventing the page from
rendering at all.  
**Suggested fix:** Determine the intended function name (likely a status badge for system
accounts — check git history for `sysbage` or `sysBadge`). Correct the typo.

---

### B-5 Settings page has no Webhooks tab despite full backend CRUD existing
**Severity:** P1  
**Where:** `app.js` ~lines 8343-8354 (`_settingsRender` tab list); `server.go` ~lines 877-883  
**What's wrong:** `GET /api/v1/admin/webhooks` (list), `POST` (create), `PUT/{id}` (update),
`DELETE/{id}` (delete), and `GET/{id}/deliveries` are all implemented and registered on the
server. `API.network.* ` and `API.ldap.*` have full module UIs. Webhooks have zero UI — the
entire feature is inaccessible to anyone not using curl.  
**Suggested fix:** Add a "Webhooks" tab to `_settingsRender`. Minimum viable: list subscriptions,
create/delete, and show last 5 delivery attempts per subscription.

---

### B-6 Audit log endpoint exists but has no UI page
**Severity:** P1  
**Where:** `app.js` `_initRoutes` (no `/audit` route); `handlers/audit.go`; `api.js`
`API.audit.query()`  
**What's wrong:** `GET /api/v1/audit` returns paginated audit records (`records`, `total`,
`limit`, `offset`). `API.audit.query()` is already wired in `api.js`. No route in the SPA
router leads to an audit page, and there is no nav link. The entire audit trail is inaccessible
from the browser.  
**Suggested fix:** Add `'/audit': Pages.audit` to `_initRoutes`. Add an "Audit" link under
SYSTEM in the sidebar. Render a paginated table of `created_at`, `actor`, `action`, `target`,
`detail`.

---

### B-7 `GET /nodes/connected` endpoint has no UI surface
**Severity:** P3  
**Where:** `server.go` ~line 963; `app.js` (no caller)  
**What's wrong:** The server exposes `/nodes/connected` returning which nodes currently have
an active agent connection. This would be valuable for a "live" indicator on the node list.
Currently unused from the UI.  
**Suggested fix:** Add a "connected" icon or badge to the node list table. Optionally poll
`/nodes/connected` on the node list page every 30s and annotate live nodes.

---

### B-8 `GET /repo/health` endpoint unreachable from UI
**Severity:** P3  
**Where:** `server.go` ~line 773; no UI caller  
**What's wrong:** A repository health endpoint exists but is never queried from the UI. The
server-info Settings tab only calls `/health`. Repository health errors would not surface to
admins.  
**Suggested fix:** Include repo health in the Server Info tab, alongside the existing health
data.

---

### B-9 `_nodeActionsRediscover` label misleads — actually queues a reimage, not a hardware rediscovery
**Severity:** P2  
**Where:** `app.js` ~line (node actions section)  
**What's wrong:** The function is named "rediscover" but calls `POST /nodes/{id}/reimage`,
which sets `reimage_pending=true`. This is a destructive operation (node will be wiped on next
PXE boot), not a read-only hardware probe. The button label misleads operators into triggering
an unintended reimage.  
**Suggested fix:** Rename the action to "Queue Reimage" or "Schedule Wipe" and add a warning in
the confirm modal explaining that the node disk will be erased on next boot.

---

## Category C — Coverage Gaps

### C-1 No UI for webhook delivery history inspection
**Severity:** P1  
**Where:** `handlers/webhooks.go` `HandleListDeliveries`; no UI  
**What's wrong:** `GET /admin/webhooks/{id}/deliveries` returns recent delivery attempts with
status codes and timestamps. Even if a Webhooks tab is added (B-5), the delivery log per
subscription is needed to debug why webhooks are not firing.  
**Suggested fix:** Add a "Deliveries" expandable section per webhook row showing last 10
attempts (status code, timestamp, error if any).

---

### C-2 No error displayed when image blob download fails mid-stream
**Severity:** P2  
**Where:** `app.js` (image detail page download action); `api.js` `API.images.downloadBlob`  
**What's wrong:** Image blob download uses a simple `window.location.href` or anchor click
approach. If the download fails (server error, network drop, storage corruption), the browser
shows a generic download failure with no error message from the server.  
**Suggested fix:** Use `fetch()` with streaming response; on non-2xx status, read the response
body and surface the error via `App.toast()`.

---

### C-3 Node config history page has no pagination — loads all records
**Severity:** P2  
**Where:** `app.js` ~line 6250 (config history page); server handler  
**What's wrong:** Config history is fetched with no `limit` parameter. For nodes that have been
reconfigured many times, this could return hundreds of records and cause a slow render.  
**Suggested fix:** Add `limit=50` and "Load more" pagination, consistent with the audit query
handler which already supports `limit`/`offset`.

---

### C-4 Group reimage modal has no node count preview before confirming
**Severity:** P2  
**Where:** `app.js` ~line 7350 (group reimage flow)  
**What's wrong:** When an operator selects a node group and triggers a group reimage, the modal
does not show how many nodes will be affected before the confirm button is pressed. On a
group with 40 nodes this is a significant blind spot.  
**Suggested fix:** After group selection, call `API.nodes.list({ group_id })` and display the
count (e.g., "This will reimage 40 nodes") in the modal before the confirm step.

---

### C-5 DHCP leases page has no auto-refresh countdown indicator
**Severity:** P3  
**Where:** `app.js` ~line 9188 (`dhcpLeases`, `App.setAutoRefresh(30000)`)  
**What's wrong:** The page auto-refreshes every 30s but gives no visual indication of when the
next refresh will occur. Operators may think stale data is current.  
**Suggested fix:** Add a "Last updated X seconds ago" timestamp near the refresh button,
updated each second via `setInterval`.

---

### C-6 No keyboard shortcut or global search to navigate nodes by hostname/MAC
**Severity:** P3  
**Where:** `app.js` `_initRoutes`; sidebar  
**What's wrong:** Finding a specific node in a cluster of 200+ requires scrolling a table.
There is no command palette, global search, or even a `Ctrl+K` style shortcut. For large
deployments this is a significant workflow gap.  
**Suggested fix:** Add a client-side fuzzy search over the cached node list. A minimal
implementation fits in ~30 lines using the existing `App._nodeCache`.

---

### C-7 Slurm upgrade page has no rollback UI
**Severity:** P2  
**Where:** `slurm.js` (upgrade flow); server routes  
**What's wrong:** If a Slurm rolling upgrade fails mid-way (some nodes upgraded, some not),
there is no UI path to trigger a rollback or see which nodes are on which version.  
**Suggested fix:** If the backend supports rollback, expose it. At minimum, add a "node Slurm
version" column to the Slurm node table to show divergence.

---

## Category D — UX/Interaction Issues (Technical)

### D-1 Session expiry banner requires a manual button click — no auto-redirect countdown
**Severity:** P2  
**Where:** `index.html` `#session-expiry-banner`; `Auth.extendSession()`  
**What's wrong:** The banner appears some time before expiry (exact threshold unclear from
client code) but just shows "Session expiring soon" with an "Extend" button. There is no countdown
showing how many minutes remain, and if the admin ignores it they lose their session mid-operation
with no warning.  
**Suggested fix:** Show a countdown timer in the banner (e.g., "Session expires in 4:32").
Auto-redirect to `/login` when the timer hits zero rather than waiting for the next API call to 401.

---

### D-2 Node detail dirty-state tracking does not warn before navigation
**Severity:** P2  
**Where:** `app.js` `Pages._nodeEditorState`; `Router` (hash-change handler)  
**What's wrong:** If an operator edits a node detail tab and then clicks a sidebar link or
browser back, the Router navigates immediately without checking `_nodeEditorState.dirty`. Unsaved
changes are silently discarded.  
**Suggested fix:** In the `Router` `hashchange` handler, check if `Pages._nodeEditorState.dirty`
is set. If so, show a `showConfirmModal` before navigating away.

---

### D-3 Toast notifications have no deduplication — rapid errors produce a stack of identical toasts
**Severity:** P3  
**Where:** `app.js` `App.toast()`  
**What's wrong:** Calling `App.toast('message', 'error')` multiple times in quick succession
(e.g., from a polling loop that fails repeatedly) stacks identical toast elements in the DOM.
On a slow network, three or four identical error toasts appear simultaneously.  
**Suggested fix:** Before appending a new toast, check if an identical message already exists
in the container. If so, either skip the duplicate or update a count badge on the existing toast.

---

### D-4 Image detail page does not close the ISO build SSE stream on navigation away
**Severity:** P2  
**Where:** `app.js` `_startIsoBuildSSE` stores `Pages._isoBuildSSE`; `App.render()`  
**What's wrong:** `Pages._isoBuildSSE` is assigned when the image detail page opens. If the
user navigates to another page before the build completes, `App.render()` replaces the DOM but
does not call `Pages._isoBuildSSE.close()`. The SSE connection stays open, consuming server
resources and firing DOM update callbacks on detached nodes.  
**Suggested fix:** Add a `Pages._cleanup` hook that `App.render()` calls before replacing the
DOM. Register the SSE close in that hook for any page that opens a stream.

---

### D-5 Log viewer on Settings page loses state on tab switch
**Severity:** P3  
**Where:** `app.js` `_settingsRender`; `App._logStream`  
**What's wrong:** When the admin switches from "Server Info" to "API Keys" and back, the entire
settings page is re-rendered via `_settingsRender(tab)`. This re-renders the log viewer from
scratch, discards the live stream, and re-issues `_loadAppLogs()` which fetches the last 500
log lines again. Any entries that arrived since the initial load are lost.  
**Suggested fix:** Only replace the tab body content, not the entire settings card. Persist the
`LogStream` instance across tab switches.

---

## Category E — Frontend Tech Debt

### E-1 Monolithic `app.js` (~9,350 lines) with no module boundaries
**Severity:** P2  
**Where:** `app.js` entire file  
**What's wrong:** All core SPA logic — router, app state, all 30+ page renderers, all modals,
all helper functions — lives in a single 9,350-line file. There are no ES module `import/export`
boundaries. Loading the full file is unavoidable even for pages that use only a tiny fraction
of the code. Changes to unrelated sections require reading through thousands of lines to
understand context. The existing module split (`ldap.js`, `slurm.js`, etc.) is inconsistent —
why do LDAP and Slurm get their own files but Deployments, Settings, and DHCP do not?  
**Suggested fix:** Progressively extract page namespaces to their own files following the
established pattern (`DeployPages`, `SettingsPages`, `DHCPPages`). Keep `app.js` as the
orchestrator (router + App state + global helpers). No bundler required — just additional
`<script>` tags in `index.html`.

---

### E-2 All page-level HTML is generated via template literal string concatenation
**Severity:** P3  
**Where:** `app.js`, `ldap.js`, `network.js`, `slurm.js`, `sysaccounts.js` — pervasive  
**What's wrong:** Every page renders by assembling HTML strings with template literals, then
calling `App.render(html)` which sets `innerHTML`. This makes it impossible to statically
analyze for XSS vectors, prevents any diffing/patching (full DOM replacement on every refresh),
and makes it easy to accidentally inject raw data. While `escHtml()` is used consistently for
user-provided values, it is not enforced structurally.  
**Suggested fix:** For v1 this is acceptable given the no-framework constraint. Mitigation: add
an ESLint rule (or a grep CI check) that flags any `innerHTML` assignment where the value is not
wrapped in a function call to `escHtml`, `cardWrap`, `alertBox`, or another known-safe helper.

---

### E-3 No Content Security Policy header — inline scripts are unrestricted
**Severity:** P2  
**Where:** `server.go` middleware stack; `index.html`  
**What's wrong:** The server does not set a `Content-Security-Policy` header. `index.html` uses
numerous `onclick=` inline event attributes (e.g., `onclick="Pages.deploys()"`,
`onclick="Pages._cancelIsoBuild('${img.id}')"`) which would be blocked by even a basic CSP.
The heavy use of inline handlers throughout means adding CSP later is a large refactor.  
**Suggested fix:** Short-term: document the inline-handlers constraint in a code comment so
future contributors do not assume CSP is compatible. Medium-term: migrate inline handlers to
`addEventListener` calls wired in the page-init functions. Then add a `script-src 'self'` CSP
header.

---

### E-4 `escHtml` in `logs.js` is a duplicate of the global (see A-13) — also missing `'` escaping
**Severity:** P3  
**Where:** `logs.js` lines 160-166  
**What's wrong:** Both `escHtml` implementations escape `&`, `<`, `>`, `"` but not `'` (single
quote). In contexts where the escaped value is placed inside a single-quoted HTML attribute
(e.g., `onclick='...'`), this creates a potential injection vector. The global `app.js`
version has the same gap.  
**Suggested fix:** Add `.replace(/'/g, '&#039;')` to both. Then remove the duplicate (see A-13).

---

### E-5 XHR-based ISO upload in `API.factory.uploadISO` is inconsistent with all other API calls (fetch-based)
**Severity:** P3  
**Where:** `api.js` `API.factory.uploadISO`  
**What's wrong:** All other API functions use `fetch()`. `uploadISO` uses `XMLHttpRequest`
specifically to get upload progress events. This is the only XHR in the codebase. It is
otherwise correct, but it bypasses the central error handling in `api.js` (`_handleResponse`,
auth redirect on 401). A 401 during upload will silently fail rather than redirecting to
the login page.  
**Suggested fix:** Keep XHR for progress events, but add an explicit `xhr.status === 401`
check in `onerror`/`onload` to redirect to `/login`, matching `api.js` behavior.

---

## Category F — Test Coverage Gaps

### F-1 No frontend unit tests for any JavaScript logic
**Severity:** P2  
**Where:** `internal/server/ui/static/js/` — entire directory  
**What's wrong:** The Go handler tests (`boot_test.go`, `dhcp_test.go`, `nodes_test.go`,
`initramfs_test.go`, `shell_ws_test.go`) provide backend coverage. There are zero tests for
the frontend. Critical logic with real bug potential — `_isoDetectDistro`, `_phasePercent`,
`sortNodeConfigs` (client-side sort), `fmtBytes`, `fmtRelative`, `_deploysHistoryRows` status
badge mapping — is completely untested.  
**Suggested fix:** Add a minimal Vitest (or vanilla `node:test`) test suite covering the pure
helper functions. These can be extracted from `app.js` without touching the DOM. Start with
`fmtBytes`, `fmtRelative`, `_isoDetectDistro`, `_phasePercent`, `_phaseLabel`.

---

### F-2 No integration test for the SSE-based build progress flow
**Severity:** P2  
**Where:** `handlers/buildprogress.go`; `_startIsoBuildSSE` in `app.js`  
**What's wrong:** The ISO build progress feature is the most complex streaming flow in the
product. The server sends `snapshot` + incremental SSE events; the client applies phase
transitions and drives a progress bar. There are no tests verifying the SSE event protocol,
the phase-to-progress-percent mapping, or the interrupted-build detection logic (3 errors
before showing the banner).  
**Suggested fix:** Add a Go integration test that runs an HTTP test server, opens an SSE
connection, sends a synthetic event sequence, and asserts the phase transitions. For the JS
side, unit-test `_phasePercent` and `_phaseLabel`.

---

### F-3 No test covering the `_nodeEditorState` dirty-flag and multi-tab save logic
**Severity:** P2  
**Where:** `app.js` node detail page tab save handlers  
**What's wrong:** The tab-level dirty tracking and the GET-then-PUT save pattern (see A-7 for
the race condition) have no automated coverage. A test would have caught the race condition
before it shipped.  
**Suggested fix:** Add a Go test for `PUT /nodes/{id}` that issues two concurrent PUTs and
asserts the final state is the last writer's version (documenting the known race), OR that
the server rejects the second with a 409 if optimistic locking is implemented.

---

### F-4 DHCP handler tests (`dhcp_test.go`) do not cover the `/dhcp/leases` endpoint response shape
**Severity:** P3  
**Where:** `handlers/dhcp_test.go`  
**What's wrong:** The DHCP leases UI (`dhcpLeases` in `app.js`) depends on the response having
a `leases` array and a `count` field. A server-side change that renames `count` to `total` or
changes the envelope would silently break the UI with no test catching it.  
**Suggested fix:** Add a test in `dhcp_test.go` that asserts the exact response JSON shape
including the `leases`/`count` keys.

---

## Top 5 Things to Fix Before v1.1 Ships

1. **B-4 — `sysbage` ReferenceError in `sysaccounts.js` (P1):** The System Accounts page
   throws a `ReferenceError` at runtime and will not render for any user. This is a hard
   page-breaking regression that must be fixed before any v1.1 release.

2. **A-10 — `Auth._role` defaults to `'admin'` on network error (P1):** A transient network
   hiccup during boot silently grants admin-level UI to operator and readonly sessions. This is
   a privilege escalation in the UI layer that should be fixed before a multi-user deployment.

3. **B-5 + B-6 — Webhooks and Audit log are backend-complete but have zero UI (P1):** Both
   features are fully implemented on the server and wired in `api.js`. Shipping v1.1 without
   any way for operators to see the audit trail or manage webhook subscriptions from the browser
   makes these features effectively invisible.

4. **A-1 — `fmtBytes` shadow in heartbeat panel produces wrong byte units (P1):** The
   heartbeat panel reports disk/memory in decimal gigabytes while every other panel uses binary
   gibibytes. On a 16 GiB server this shows as "17.2 GB" vs "16.0 GiB" elsewhere — a confusing
   inconsistency that undermines operator trust in the health display.

5. **A-7 — Node detail tab save race condition (P1):** Two concurrent tab saves silently
   overwrite each other with no error shown to the operator. In a multi-admin deployment this
   leads to silent configuration loss. Short-term mitigation (disable save button while any save
   is in flight) is a single-day fix.

---

## Flows Not Fully Evaluated

- **IPMI/power actions** — `handlers/power.go` and `handlers/ipmi.go` were not read. The UI
  power action flow (boot, shutdown, reset from node detail) was observed in `app.js` but the
  full request/response contract was not verified against the handler.
- **Live SSH terminal (`xterm.js`)** — the WebSocket protocol for shell sessions
  (`API.images.shellWsUrl`, `shell_ws.go`) was not tested against a live server session. The
  UI wiring appears correct but connection error handling was not traced end-to-end.
- **Slurm rolling upgrade** — `slurm.js` upgrade page was not fully read past the settings/configs
  sections. The upgrade trigger and status polling flow was not evaluated.
- **LDAP user/group provisioning flow** — `ldap.js` was read through the settings and enable form.
  The user creation, group sync, and sudo-rules flows were not evaluated against the server
  handler implementation.
- **Node group operator RBAC enforcement** — the server-side enforcement of operator scope
  (operators may only reimage nodes in their assigned groups) was not verified against the UI
  gating in `_deploysHistoryRows` and group reimage modal.
