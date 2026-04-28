# Changelog

All notable changes to clustr are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [v1.3.0] — 2026-04-27

**Sprint D — IT Director Portal + Notifications + Grants/Publications**

Adds institutional oversight (IT Director role), email notification
infrastructure, per-group grant and publication tracking, and lightweight
annual review cycles. No breaking changes; all new features are additive.

### Added

- **Director role (D-1 / CF-17)** — Sixth RBAC role: `director`. Read-only
  institutional view. Login routes to `/portal/director/`. `requireDirector()`
  middleware allows director and admin only. Scope sentinel `"director"` added
  to `apiKeyAuth`. DB migration 059 extends the `users.role` CHECK constraint.
  `director` role now valid in user create/update API.

- **Director Portal (`/portal/director/`) (D-1)** — Alpine.js SPA with three
  tabs: Summary (cluster-wide KPI cards), Groups (searchable table with PI,
  node/member/grant/pub counts), and Annual Review (status summary). CSV export
  of group summaries and full grants/publications CSV. Read-only; no mutations.

- **Director API (D-1)** — New endpoints under `requireDirector()`:
  `GET /api/v1/portal/director/summary`,
  `GET /api/v1/portal/director/groups`,
  `GET /api/v1/portal/director/groups/{id}`,
  `GET /api/v1/portal/director/export.csv`,
  `GET /api/v1/portal/director/export-full.csv`,
  `GET /api/v1/portal/director/review-cycles`,
  `GET /api/v1/portal/director/review-cycles/{id}`.

- **SMTP notifications (D-2 / CF-15)** — `internal/notifications` package with
  `Mailer` interface (SMTP + test stub). `Notifier` dispatcher with best-effort
  send (never blocks primary workflow). Events: LDAP account created, member
  added/removed, PI request approved/denied, annual review due/submitted,
  broadcast. Templates embedded via `//go:embed`. SMTP config stored encrypted
  (AES-256-GCM) in new `smtp_config` table (migration 063); env vars override
  DB values at send time. Rate-limited broadcast via `broadcast_log` table.

- **SMTP admin UI (D-2)** — New "Notifications" tab in Settings (admin-only).
  SMTP host/port/user/pass/from/TLS/SSL form. "Send test" button sends to the
  configured From address. Broadcast panel with NodeGroup selector.

- **SMTP API (D-2)** — `GET/PUT /api/v1/admin/smtp`, `POST /api/v1/admin/smtp/test`,
  `POST /api/v1/node-groups/{id}/broadcast`. Broadcast rate-limited to 1/hour/group.
  Password never returned in GET response.

- **PI notifications wired (D-2)** — `NotifyMemberAdded` fires on auto-approved
  add; `NotifyMemberRemoved` fires on PI-initiated removal; `NotifyPIRequestApproved/Denied`
  fires on admin resolution of pending member requests. All fire-and-forget in goroutines.

- **Grant tracking (D-3 / CF-12)** — `grants` table (migration 060). PI can
  CRUD grants on their NodeGroups. Admin can manage all. Fields: title, agency,
  grant number, amount, dates, status (active/no_cost_extension/expired/pending),
  notes. Director sees counts per group; full list via CSV export.
  Routes: `GET/POST /api/v1/portal/pi/groups/{id}/grants`,
  `PUT/DELETE /api/v1/portal/pi/groups/{id}/grants/{grantID}`.

- **Publication tracking (D-3 / CF-13)** — `publications` table (migration 061).
  PI can CRUD publications. Fields: DOI, title, authors, journal, year.
  Optional DOI metadata lookup via CrossRef API (opt-in: `CLUSTR_DOI_LOOKUP_ENABLED=true`).
  Air-gap deployments are not affected (disabled by default).
  Route: `GET /api/v1/portal/pi/publications/lookup?doi=<doi>`.

- **Annual review cycles (D-4 / CF-14)** — `review_cycles` + `review_responses`
  tables (migration 062). Admin creates a cycle; pending response rows created for
  all PI-owned groups. PIs respond: affirmed or archive_requested. Director and
  admin view aggregate status. Routes: admin CRUD under `/api/v1/admin/review-cycles/`,
  PI response at `/api/v1/portal/pi/review-cycles/{cycleID}/groups/{groupID}/respond`.

- **PI Portal enhancements (D-3/D-4)** — New Grants, Publications, and Annual
  Review tabs in the PI portal. Grant and publication modals with inline DOI
  lookup (when enabled). Review cards with affirm/archive buttons.

- **DB migrations 059–063** — Director role, grants, publications, review cycles,
  SMTP config + broadcast log tables.

### Changed

- `validRole()` in users handler now accepts all six roles:
  `admin, operator, readonly, viewer, pi, director`.
- Login redirect now routes `director` role to `/portal/director/`.

### Dependencies
No new external dependencies. The CrossRef DOI lookup uses Go's `net/http`
standard library with a polite User-Agent header.

---

## [v1.2.5] — 2026-04-28

**Sprint C.5 — PI Governance Layer**

The PI persona is now a first-class RBAC role. PIs can log in, view their
assigned NodeGroups, manage member requests, view read-only utilization stats,
and submit expansion requests — all without admin involvement for day-to-day
tasks.

### Added

- **PI role (C.5-1 / CF-09)** — Fifth RBAC role: `admin > operator > pi > readonly > viewer`.
  PIs authenticate with username+password; login dispatches to `/portal/pi/`.
  API scope `pi` added to the API key auth middleware. `requirePI()` middleware
  allows admin/operator/pi; blocks readonly and viewer.

- **PI NodeGroup ownership (C.5-1)** — New `pi_user_id` column on `node_groups`
  (migration 056). Admin assigns PI via `PUT /api/v1/node-groups/{id}/pi`.
  One PI can own many groups; one group has at most one PI.

- **PI Portal (`/portal/pi/`) (C.5-4)** — Dedicated SPA served at `/portal/pi/`.
  Alpine.js component with two tabs: **My Groups** (cards with expandable member
  list, Add Member modal, Expansion Request modal) and **Utilization** (read-only
  stats with HTMX auto-refresh every 60s). Partial responses returned when
  `HX-Request: true`.

- **PI self-service member management (C.5-2 / CF-08)** — PIs can request
  LDAP group members be added to their NodeGroup. Requests land in
  `pi_member_requests` (migration 056) as `pending`; admins approve/deny via
  the Admin panel or the API. When `pi_auto_approve = 1` in `portal_config`
  (or `CLUSTR_PI_AUTO_APPROVE=true` env), requests are auto-approved and the
  LDAP add fires immediately.

- **PI expansion requests (C.5-2)** — PIs can submit node-count expansion
  requests with a justification. Stored in `pi_expansion_requests`
  (migration 057). Admins acknowledge or dismiss. Read-only list on the PI
  portal under each group card.

- **PI utilization view (C.5-3 / CF-02 partial)** — `GET /api/v1/portal/pi/groups/{id}/utilization`
  returns pure-SQL aggregation: total/deployed/undeployed node counts,
  last-deploy timestamp, failed deploys in last 30 days, active member count.
  No rollup tables. Gaps (no Slurm job data) labeled as unavailable.

- **Admin PI management routes** — `GET /api/v1/admin/pi/member-requests`,
  `POST /api/v1/admin/pi/member-requests/{id}/resolve`,
  `GET /api/v1/admin/pi/expansion-requests`,
  `POST /api/v1/admin/pi/expansion-requests/{id}/resolve`.

- **`auth/me` returns username** — `GET /api/v1/auth/me` now includes `username`
  for PI portal display ("Signed in as <username>").

- **LDAP manager: AddUserToGroup / RemoveUserFromGroup** — Two new public methods
  on `ldap.Manager` delegate to the DIT client for PI-triggered membership changes.

- **Migrations 055–058** — `pi_auto_approve` in `portal_config` (055),
  `pi_user_id` FK + `pi_member_requests` table (056), `pi_expansion_requests`
  table (057), `users.role` CHECK constraint expanded to include `pi` and
  `viewer` (058, recreates table via rename+copy).

- **PI RBAC tests** — `internal/server/pi_rbac_test.go`: 6 tests covering scope
  gating, admin route blocking, DB ownership, member request lifecycle,
  expansion request creation, and utilization query on empty group.

### Try it

```bash
# Create a PI user (admin session required)
curl -s -X POST http://localhost:7001/api/v1/admin/users \
  -H "Content-Type: application/json" \
  -H "X-API-Key: <admin-key>" \
  -d '{"username":"jdoe","password":"changeme","role":"pi"}'

# Assign the PI to a NodeGroup
curl -s -X PUT http://localhost:7001/api/v1/node-groups/<group-id>/pi \
  -H "Content-Type: application/json" \
  -H "X-API-Key: <admin-key>" \
  -d '{"pi_user_id":"<user-id>"}'

# Log in as PI — browser is redirected to /portal/pi/
# Username: jdoe / Password: changeme
```

### Upgrade

Apply migrations 055–058. They are additive — no data loss. The `users` table is
recreated in migration 058 (role CHECK constraint expansion); all existing rows
are preserved. Restart `clustr-serverd` after migration.

---

## [v1.2.0] — 2026-04-27

**Sprint C — Researcher Portal MVP + ColdFront wedge**

### Added

- **Researcher Portal (C1)** — Viewer role with read-only dashboard. PI-scoped
  view shows group membership, node status, and active deployments.
  Routes: `#/portal`. Auth: viewer session cookie, separate `/portal.html` SPA.

- **HTMX anomaly polling (C2-5)** — Dashboard anomaly card replaced with an
  HTMX-driven widget that polls `/api/v1/dashboard/anomalies` every 30 seconds
  using `hx-swap="outerHTML"`. Eliminates full-page refresh for anomaly counts.

- **Dashboard System Health card (C3-22)** — Reads `/api/v1/healthz/ready` and
  renders OK / Degraded / Error with per-check tooltip. No more hardcoded "Online".

- **Per-node verify-boot timeout override (C3-18)** — New `verify_timeout_override`
  field on `NodeConfig` (migration 054). Admins set a per-node timeout in seconds
  on the Configuration tab; `0` disables the timeout for that node. The verify-boot
  scanner applies the override before flagging a node as timed-out.

- **Last failure summary banner (C3-20)** — When a node's last deploy failed and
  has not been superseded by a successful deploy, a red banner appears above the
  tabs with timestamp, "View Logs", and "Re-deploy" CTA.

- **Bulk-select status shortcuts (C3-21)** — The bulk action bar now includes
  "+Failed", "+Timed Out", and "+Never Deployed" quick-select buttons.
  Node checkboxes carry a `data-status` attribute keyed to lifecycle state.

- **Per-build cancel (C3-5)** — `POST /api/v1/images/{id}/cancel` sets a building
  image's status to `error` without deleting the record.

- **Image download button (C3-6)** — "Download Image" button on image detail
  (status `ready` only) streams the blob via the existing authenticated endpoint.

- **Config history pagination (C3-7)** — Config history tab loads 50 rows,
  appends on "Load more" with remaining count displayed.

- **Initramfs card relocated (C3-23)** — Moved from the Images page to Settings
  → System tab (formerly "Server Info", now "System").

- **Slurm sync: untracked node surfacing (C3-24)** — Sync Status page shows a
  "Deployed Nodes Not in Slurm" card for deployed nodes with no config push
  history. Each row has an "Add to cluster" button that triggers an immediate push.

- **Slurm Settings: preview config panel (C3-25)** — New inline preview section
  in Slurm Settings lets admins select a config file + Node ID and see the
  rendered output before pushing.

- **Bundle info in Settings → System (C3-26)** — Installed Slurm RPM bundles are
  displayed in a table fetched from `/repo/health`. Includes a collapsed
  "Re-install bundle" CLI hint.

- **Layout smoke tests (C2-6)** — `internal/server/layout_smoke_test.go` asserts
  that `index.html` and `portal.html` both include the pinned Alpine 3.15.11 and
  HTMX 2.0.9 vendor scripts.

### Changed

- **Disk layout tab (C3-17)** — "Customize Layout" is collapsed by default when
  no node-level override exists; only open when source is `node`. Group detail
  page now shows a colour-coded visual partition bar alongside the table.

- **Reimage modal (C3-19)** — Concurrency input has a tooltip explaining the
  `CLUSTR_REIMAGE_MAX_CONCURRENT` server cap (default 20). Post-submit status
  shows effective vs requested concurrency when they differ.

- **Network tab validation (C3-16)** — IP Address fields validate CIDR notation
  (`a.b.c.d/prefix`); Gateway validates bare IP. Invalid entries block save with
  an inline error.

- **CIDR + nav guard (C3-16 / C3-11)** — Navigation guard prevents leaving a
  dirty node detail page without confirmation.

- **Settings "Server Info" → "System"** — Tab renamed to reflect the broader
  scope: initramfs, bundle info, and server diagnostics in one place.

- **`API.health.ready()`** — New method in `api.js` for `GET /api/v1/healthz/ready`.

### Fixed

- **Deploy progress overflow (C3-1)** — Dashboard deploy table caps at 5 visible
  rows; overflow is linked to the full deploys page.
- **Diff table empty state (C3-2)** — Config push diff no longer breaks when the
  container has no `tbody` on first data arrival.
- **Reimage modal stale polling (C3-3)** — Poll guard checks modal liveness before
  and after each async fetch; stops immediately when modal is closed.
- **Snapshot SSE dedup (C3-4)** — Merged duplicate SSE listeners into one;
  tracking vars declared once, no double-apply on reconnect.
- **SSE stream cleanup on navigation (C3-13)** — `Router._navigate()` disconnects
  `App._nodeLogStream` so stale streams don't accumulate.
- **Settings log stream preservation (C3-14)** — `_settingsRender()` disconnects
  `App._logStream` before re-rendering to prevent double-stream on tab switch.
- **Custom variable key validation (C3-15)** — Keys validated with
  `/^[A-Za-z0-9_-]+$/`; invalid keys show a warning icon and orange border.

---

## [Unreleased — v1.1.1] — Sprint B.5 Alpine+HTMX adoption

**Sprint B.5 — Framework adoption pilot**

### Added

- **Alpine.js 3.15.11 + HTMX 2.0.9 vendored** — both libraries are embedded
  in the server binary via Go `embed.FS`. Served from `/ui/vendor/`. No CDN,
  no build step, no npm. Integrity manifest at
  `internal/server/ui/static/vendor/VENDOR-CHECKSUMS.txt` (SHA256 verified
  against two independent CDNs). Decision reference: D23.

- **DHCP Leases page migrated to Alpine.js** — `#/network/allocations` is the
  pilot surface for the Alpine+HTMX framework adoption (D23). The vanilla
  string-building render is replaced with a declarative `x-data` component
  (`dhcpLeasesComponent()`). Functional parity: same data, same columns, same
  auto-refresh cadence. Demonstrates: `x-data` / `x-init`, `x-show`, `x-for`
  with `:key`, `x-text`, `:href`, `:class`, `@click`, factory function pattern.

- **Frontend patterns playbook** — `docs/frontend-patterns.md` documents the
  Alpine+HTMX conventions, when to use each tool, coexistence with vanilla,
  common gotchas (CSP, Alpine init order, `x-for` in `<template>`), and two
  annotated examples. Sprint C uses this as its migration ramp.

### Changed

- `internal/server/ui/static/index.html` — two `<script>` tags added before
  `app.js` for Alpine and HTMX. Vanilla pages are unaffected.

---

## [v1.1.0] — 2026-04-27

**Sprint B — Trustworthy Admin UI**

### Added

- **Role-aware navigation (B1):** Sidebar nav now hides Slurm, LDAP, System
  Accounts, Network, and Audit sections for `operator` and `readonly` roles.
  Operator dashboard shows "Your Groups" and "Your Recent Deploys" panels
  instead of "Recent Images" and "Live Log Stream". `GET /api/v1/auth/me` now
  includes `assigned_groups: []` for operator scope tracking.

- **Configure & Deploy CTA (B2-1):** Registered nodes (hardware profile
  discovered, no image assigned) now show a "Configure & Deploy" primary
  button in the nodes list. Clicking opens a 3-step guided modal: image
  select → SSH key confirm → deploy trigger. Assigns the image and queues
  a reimage in a single action.

- **First-login hint (B2-2):** Login page shows a "First time?" hint with
  default credentials when the cluster has not yet been set up.
  A new unauthenticated endpoint `GET /api/v1/auth/bootstrap-status` is
  used to determine whether to show the hint.

- **Restore Slurm defaults (B2-3):** Slurm Module Setup page now includes
  a "Restore Default Config Files" button that re-seeds clustr's built-in
  templates for any config files where `is_clustr_default=true`.

- **Anomaly card (B2-4):** Dashboard shows an "Anomalies" card with
  clickable filter links for failed deploys, never-deployed nodes, and
  nodes stale >90 days.

- **Webhooks UI (B3-1/B3-2):** Full webhook CRUD in Settings → Webhooks tab.
  Create, edit (URL / secret / events / enabled), delete, and view delivery
  history per webhook.

- **Audit log page (B3-3):** New `#/audit` route renders a paginated audit
  table with actor / action / date-range filters. Link added to sidebar under
  System section (admin only).

- **About tab (B3-4/B3-5):** Settings → About tab shows server version,
  build SHA, Slurm bundle version, and security trust signals.

- **Slurm config validation endpoint (B5-1):** New
  `POST /api/v1/slurm/configs/{filename}/validate` endpoint performs
  structural validation without saving. Returns `{"valid":bool,"issues":[...]}`.
  For `slurm.conf` this checks required keys and detects duplicates.

- **Validate-before-save in config editor (B5-2):** The Slurm config editor
  now calls the validate endpoint before saving and displays inline errors,
  blocking save if issues are found.

- **Custom kickstart default template (B5-3):** The "Advanced: Custom
  Kickstart" section in the Build Image modal now has a "View default
  kickstart template" toggle that reveals the clustr-generated template
  inline as a reference.

- **JS utility test suite (B4-8):** 34 unit tests for `fmtBytes`,
  `fmtRelative`, `_isoDetectDistro`, `_phasePercent`, and `_phaseLabel`
  using Node.js built-in `node:test`. Run with `make test-js`.

### Changed

- **Label renames (B2-5):** "Allocations" → "DHCP Leases" in the Network
  nav; Slurm and LDAP nav entries renamed from "Settings" to "Module Setup".

- **Wizard gate (B2-6):** The "Getting Started" wizard on the dashboard now
  stays visible until at least one node has `deploy_verified_booted_at` set,
  rather than hiding as soon as any image or node exists.

- **Deploy progress empty state (B2-7):** The Active Deployments card now
  shows a helpful subtext and CTA link when no deployments are in progress.

- **Password change preserves redirect (B2-8):** After setting a new
  password, the user is redirected to the page they were originally trying
  to reach (via `?next=` query param) rather than always going to `/`.

- **"Queue Reimage" rename (B4-7):** The "Rediscover" / "Re-discover
  hardware" button on the node detail page is now labelled "Queue Reimage"
  with an explicit disk-wipe warning in the confirmation dialog.

### Fixed

- **Binary fmtBytes (B4-1):** Removed an inner `fmtBytes` shadow in the
  heartbeat render path that was using decimal SI (1e9 for GB) instead of
  the canonical binary fmtBytes (1024³). Memory figures now consistently
  display as GiB.

- **Save button race condition (B4-2):** All `[id^="tab-save-"]` buttons
  are disabled while any node-detail-tab save is in flight, preventing
  concurrent saves that could overwrite each other.

- **Image tag remove error handling (B4-3):** `_removeImageTag()` now only
  updates the DOM on API success and shows a toast on failure, instead of
  optimistically removing the tag regardless of the API response.

- **prompt() / confirm() replacements (B4-5):** Password reset and role
  change in Settings now use proper modal dialogs instead of browser
  `prompt()` / `confirm()` calls.

- **Delete-image node check (B4-6):** `showDeleteImageModal()` now uses
  `API.nodes.list({ base_image_id: id })` instead of the generic
  `API.get('/nodes')` call that didn't filter by image.

### Upgrade

No database migration required. Pull the latest server binary and restart
`clustr-serverd`.

---

## [v1.0.1] — 2026-04-27

**Hotfix release — UI privilege escalation + System Accounts crash**

### Fixed

- **System Accounts page crash (B-4):** The badge helper in `sysaccounts.js`
  was named `sysbage` (an accidental shortening); renamed to `sysBadge` to
  follow the camelCase convention used throughout the codebase
  (`dhcpStateBadge`, etc.). The System Accounts page now renders correctly for
  all users. Added three regression tests that guard the response shape of
  `GET /system/accounts` and confirm `system_account` is present in the list
  response (the field sysBadge reads to decide whether to show the "sys"
  indicator).
  Commits: `bb19c05`

- **UI privilege escalation on network error (A-10):** `Auth._role` was
  initialised to `'admin'` and the `/auth/me` fallback also used `'admin'`, so
  any transient network blip during page boot silently granted the full admin UI
  to operator and readonly sessions for the duration of the session. Real
  backend authz was always enforced (operators received 403 on admin endpoints),
  but the UI showed admin affordances it should not have shown.
  Fix: `Auth._role` now defaults to `'readonly'` (lowest privilege). A
  successful `/auth/me` promotes to the real role. Boot retries up to 3 times
  with exponential backoff (500ms, 1s, 2s) before giving up; a 401/403
  redirects to login immediately. On final failure an error banner is shown and
  all role-gated UI stays hidden — no silent privilege grant.
  Commits: `dcfc61f`

### Upgrade

No database migration required. Pull the latest server binary and restart
`clustr-serverd`. The fix takes effect on next page load.

---

## [v1.0.0] — 2026-04-27

Initial release. Self-hosted webhook dev platform for bare-metal HPC clusters.
