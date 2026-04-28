# Changelog

All notable changes to clustr are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [v1.9.0] — 2026-04-28 (Sprint J — Show HN Final Polish)

**Sprint J — Show HN Final Polish (J1–J5)**

All changes are additive and non-breaking per D28. No schema changes. No API
contract changes.

### Changed (J1 — Node.js 20 → 24 GHA action sweep)

- Bumped `actions/checkout@v4` → `actions/checkout@v5` across all seven
  workflow files (`ci.yml`, `docker.yml`, `release.yml`, `initramfs.yml`,
  `ipxe-build.yml`, `lab-validate.yml`, `slurm-build.yml`). Node.js 20 actions
  are deprecated; forced migration to Node.js 24 takes effect 2026-06-02 on
  GitHub-hosted runners.
- Bumped `actions/setup-node@v4` → `actions/setup-node@v5` in `ci.yml`
  (five jobs: test, a11y, lighthouse, link-check; all Node.js 20 consumer jobs).
- `actions/setup-go@v5`, `actions/upload-artifact@v4`,
  `actions/download-artifact@v4`, `softprops/action-gh-release@v2`, and all
  `docker/*` actions are already at current versions — no change.

### Added (J2 — Smoke flake-threshold tracker)

- **`.github/smoke-streak.json`** — tracks consecutive green smoke runs on
  main. Increment after each green smoke push; reset to `0` on failure.
- **`Check smoke flake-threshold` CI step** in smoke job — reads the JSON and
  fails CI with a human-action message when the streak reaches 3: "Remove
  `continue-on-error: true` from the smoke job in ci.yml." Initialised at
  streak=2 (two consecutive green runs on 2026-04-28).
- **`docs/testing.md`** — documented the full threshold workflow, reset
  procedure, and flake policy for the streak tracker.

### Fixed (J3 — Initramfs workflow integrity)

- **`scripts/build-initramfs.sh`** — `CLUSTR_SERVER_USER` and
  `CLUSTR_SERVER_PASS` are now required only in remote mode. Setting
  `CLUSTR_CI_MODE=1` (or pointing `CLUSTR_SERVER_HOST` to localhost) activates
  local mode and skips SSH entirely. This unblocks the `initramfs.yml` workflow
  which has been failing on every tag since v1.5.0 (error: `CLUSTR_SERVER_USER
  must be set`).
- **`initramfs.yml`** — added `CLUSTR_CI_MODE: "1"` env var to the
  "Build initramfs" step. The CI-built initramfs sources kernel modules from
  Ubuntu runner packages; the production-quality build (with Rocky 9 modules
  from the lab server) is still run by the autodeploy script on `cloner`.
- **v1.8.0 release notes** updated via `gh release edit` to redirect users to
  v1.8.1 (which has the iPXE UEFI binary). v1.8.1 is the recommended release.

### Added (J4 — Demo asset)

- **`docs/assets/demo.tape`** — VHS tape script for the animated terminal demo.
  Run `vhs docs/assets/demo.tape` to generate `docs/assets/clustr-demo.gif`.
  Shows: `clustr-serverd doctor` → `version` → health check → node registration
  → API verification. Target: ~30 seconds, ≤5 MB.
- **`docs/assets/clustr-demo-static.svg`** — static SVG diagram showing the
  same four-step flow (pre-flight, server start, node registration, API output).
  Renders inline on GitHub without any tooling.
- **`README.md`** — replaced the `<!-- GIF placeholder -->` comment block with
  the static SVG (`<img>` tag, 900px wide) and a one-line note linking to the
  tape script.

### Fixed (J5 — Dogfood pass Round 2)

- **`internal/server/ui/static/set-password.html`** — password hint now shows
  the full rule: "at least one uppercase letter, one lowercase letter, and one
  digit." Previously showed only "Minimum 8 characters" (IP-14 from Jared's
  audit — the rule existed server-side but was not communicated to users at the
  change-password form).
- **`docs/getting-started-audit-2026-04.md`** — added Round 2 status section:
  all 28 paper cuts re-assessed against v1.8.1 + Sprint J. 10/10 top items
  resolved. Revised bounce-% map: cumulative first-attempt success estimate
  improved from ~15-20% to ~30-35%. 10 Sprint K candidates documented.

---

## [v1.8.0] — 2026-04-27 (Sprint I — Show HN Hardening, partial)

**Sprint I — Show HN Hardening (I1, I3, I4, I9 — engineering polish batch)**

### Added (I3 — WCAG 2.1 AA + Lighthouse perf budget)

- **axe-core CI gate** — new `a11y` job in `.github/workflows/ci.yml` runs
  `axe-core` via `jsdom` against all 6 static HTML pages on every push and PR.
  Fails on any WCAG 2.1 AA critical or serious violation. Run locally with
  `make a11y` after `npm install --prefix test/js axe-core jsdom`.

- **Lighthouse perf budget** — new `lighthouse` job in CI runs `@lhci/cli`
  against `index.html`, `portal.html`, and `portal_pi.html` served from the
  static dist dir. Budget: FCP ≤ 2s, TTI ≤ 4s, TBT ≤ 300ms (CI headroom above
  Richard's 1.5s/3s/200ms targets). Accessibility score hard gate ≥ 0.90.

- **`docs/accessibility.md`** — documents audited pages, CI gate, how to extend,
  and all waived items with rationale.

- **`lighthouse-budget.json`** + **`.lighthouserc.json`** — budget thresholds
  and Lighthouse CI configuration.

### Changed (I3 — WCAG accessibility fixes)

- `portal.html` — password change inputs now have `id`/`for` label associations;
  loading div has `aria-live="polite"`; error div has `aria-live="assertive"` +
  `role="alert"`; `<main id="main-content">` added.

- `portal_director.html` — outer content `<div>` promoted to `<main id="main-content">`;
  tab widget gets full ARIA tab/tablist/tabpanel pattern with `aria-selected`,
  `aria-controls`, `id` attributes; loading/error divs get `aria-live`; modal
  overlay gets `role="dialog" aria-modal="true" aria-labelledby`; close button
  gets `aria-label="Close dialog"`.

- `portal_pi.html` — full ARIA tab/tabpanel pattern on 7-tab widget;
  `id`/`for` associations on ~25 label/input pairs across all modals (Add Member,
  Expansion, Change Request, Grant, Publication) and the first-project wizard;
  all 4 modal overlays get `role="dialog" aria-modal="true" aria-labelledby`;
  close buttons get `aria-label`; visibility group select gets `id`/`for`;
  per-row visibility selects get `:aria-label` binding; `sr-only` utility class
  added for required-field screen-reader text.

---

## [v1.7.0] — 2026-04-27

**Sprint H — Allocation Automation (CF-01, CF-26, CF-33)**

Adds an auto-compute allocation policy engine, a PI onboarding wizard, and a
24-hour undo window so the first-login experience for PIs is hands-off and
reversible without admin intervention.

### Added

- **Auto-compute allocation engine (H1 / CF-01)** — New `internal/allocation`
  package with `Engine.Run()` that executes a 7-step pipeline: create NodeGroup,
  assign PI ownership, sync LDAP project group (non-fatal), apply access
  restriction (non-fatal), add Slurm partition, persist `auto_policy_state` JSON,
  audit event. Fatal failures roll back the NodeGroup. `Engine.Undo()` reverses
  all actions within the 24-hour window. `ParseStateView()` computes the
  remaining undo window and is used by both the API and background finalizer.

- **DB migrations 072–073** — Migration 072 adds `auto_compute`, `auto_policy_state`,
  and `auto_policy_finalized_at` to `node_groups`, adds `onboarding_completed` to
  `users`, and creates `idx_node_groups_auto_policy_pending` for the pending-group
  scanner. Migration 073 creates the `auto_policy_config` singleton table (disabled
  by default) with knobs: `enabled`, `default_node_count`, `default_hardware_profile`,
  `default_partition_template`, `default_role`, `notify_admins_on_create`.

- **PI onboarding wizard (H2 / CF-33)** — Single-screen overlay shown to PIs
  who have no projects on first login. Fields: project name, partition name template,
  initial members (comma-separated), LDAP sync toggle, auto-compute toggle. Submits
  to `POST /api/v1/projects`, which invokes the allocation engine when
  `auto_compute=true`. Wizard is dismissed permanently after first project creation
  or by explicit skip. `GET /api/v1/portal/pi/onboarding-status` drives the
  show/hide logic; `POST /api/v1/portal/pi/onboarding-complete` records completion.

- **24-hour undo window (H3 / CF-26)** — `POST /api/v1/node-groups/{id}/undo-auto-policy`
  reverses all engine actions and sends the PI a notification email. Returns 409 when
  the window is closed. `GET /api/v1/node-groups/{id}/auto-policy-state` returns
  `undo_available`, `hours_remaining`, and metadata for the PI portal banner. A
  background worker (`runAutoPolicyFinalizer`) ticks hourly and finalizes all groups
  whose 24-hour window has elapsed, closing the undo opportunity and auditing each
  finalization.

- **Slurm auto-partition helper** — `SlurmManager.AddAutoPartition()` appends a
  `PartitionName=<name> Nodes=ALL State=UP Default=NO` stanza to `slurm.conf` via
  the existing versioned config pipeline. Idempotent (no-op if partition already
  present). Validates new config via `validateSlurmConf` (logs warnings, does not
  block).

- **Admin config CRUD** — `GET /api/v1/admin/auto-policy-config` and
  `PUT /api/v1/admin/auto-policy-config` expose the singleton config for enabling
  auto-allocation and tuning defaults without a server restart.

- **Notification templates** — `auto_allocation_created.txt` (admin summary on
  NodeGroup creation) and `auto_allocation_undone.txt` (PI notification on undo).

- **PI portal undo banner** — PI portal card for each auto-compute group shows a
  dismissible banner with hours remaining and an Undo button while the window is
  open.

### Changed

- `POST /api/v1/projects` now accepts `auto_compute`, `partition_template`,
  `initial_members`, and `ldap_sync_enabled` fields. When `auto_compute=true` the
  allocation engine runs automatically after group creation.

---

## [v1.6.0] — 2026-04-27

**Sprint G — Identity & Access Primitives (CF-24, CF-40, CF-09)**

Completes clustr's IAM story for non-FreeIPA environments: OpenLDAP project
plugin auto-creates posixGroups per NodeGroup, per-NodeGroup LDAP group
restrictions gate Slurm partition access, and PI manager delegation lets PIs
deputize co-managers for their groups. No breaking changes; all new features
are additive.

### Added

- **OpenLDAP project plugin (G1 / CF-24)** — When the LDAP module is enabled
  and a NodeGroup is created, clustr auto-creates a `posixGroup` in LDAP
  (`cn=clustr-project-<slug>,ou=clustr-projects,<base_dn>`). Member add/remove
  operations are mirrored to `memberUid`. LDAP failures never block the primary
  workflow: they are enqueued in `ldap_sync_queue` and retried with exponential
  backoff (1 m → 2 m → 4 m → … capped at 60 m) by a background worker (ticks
  every 2 minutes). Additive-only sync: manually-added LDAP members are never
  removed by clustr re-sync. GID numbers are stable (derived from NodeGroup UUID
  in the 10 000–29 999 range). DB migrations 069 adds `ldap_group_dn`,
  `ldap_sync_state`, `ldap_sync_last_at`, `ldap_sync_error`,
  `ldap_sync_enabled` columns to `node_groups` and creates the
  `ldap_sync_queue` retry table.

- **Per-NodeGroup LDAP group access restriction (G2 / CF-40)** — New
  `allowed_ldap_groups` JSON array column on `node_groups` (migration 070).
  When non-empty, the Slurm config render emits `AllowGroups=` on the
  corresponding `PartitionName` line. Default `[]` = open access (no change
  to existing behavior). Admin-only API:
  `GET /api/v1/node-groups/{id}/ldap-restrictions`,
  `PUT /api/v1/node-groups/{id}/ldap-restrictions`. Pass `[]` to clear
  (restore open access). `NodeGroupRestrictions map[string][]string` added to
  `RenderContext` so custom `slurm.conf.tmpl` templates can reference it.

- **PI manager delegation (G3 / CF-09)** — New `project_managers` join table
  (migration 071) with `UNIQUE(node_group_id, user_id)`. A PI can delegate
  management rights to other users for their NodeGroup. Delegated managers have
  the same per-project rights as the PI (view utilization, manage members,
  submit allocation requests, set expiration) but are NOT the owner (cannot
  delete the group, change PI ownership, or change visibility defaults). Admin
  can list/revoke any delegation. New endpoints under PI portal middleware:
  `GET /api/v1/portal/pi/groups/{id}/managers`,
  `POST /api/v1/portal/pi/groups/{id}/managers` (`{"user_id": "<uuid>"}`),
  `DELETE /api/v1/portal/pi/groups/{id}/managers/{userID}`,
  `GET /api/v1/portal/pi/managed-groups`. Notification sent to newly delegated
  manager on grant; PI notified when admin revokes a delegation on their group.
  Audit events: `pi.manager.grant`, `pi.manager.revoke`.

- **Notification templates for manager delegation** — `manager_granted`
  (text + HTML) explains delegated manager permissions; `manager_revoked`
  (text + HTML) notifies the PI when admin removes a delegation.
  `NotifyManagerGranted` and `NotifyManagerRevoked` methods added to `Notifier`.

- **DB migrations 069–071** — See G1/G2/G3 above. All migrations are
  `ALTER TABLE` / `CREATE TABLE IF NOT EXISTS` (safe on upgrade; existing rows
  get default values).

---

## [v1.5.0] — 2026-04-27

**Sprint F — Security & Audit Hardening (F1–F5)**

Hardens the security posture with a strict Content Security Policy, removes all
inline scripts, adds a SIEM-compatible audit log export endpoint, and implements
allocation expiration warnings. No breaking API changes.

### Added

- **Content Security Policy (F1)** — `securityHeadersMiddleware` emits CSP,
  `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and
  `Referrer-Policy: same-origin` on every response. Alpine.js switched to the
  CSP-safe `@alpinejs/csp` build. All inline `<script>` blocks extracted to
  external `.js` files; inline event handler attributes replaced with
  `data-on-*` attributes dispatched by a central `Delegate` object.

- **SIEM audit log export (F2)** — `GET /api/v1/audit/export` streams the
  audit log as JSONL/NDJSON. Admin-only; rate-limited to 1 export per minute.
  Supports `since`, `until`, `actor`, `action`, `resource_type` query params.
  `StreamAuditLog` added to the DB layer for memory-efficient streaming.
  "Export JSONL" button added to the admin Audit Log page.

- **Allocation expiration (F3)** — Optional `expires_at` field on `node_groups`
  (migration 068). `PUT /api/v1/node-groups/{id}/expiration` (pi+),
  `DELETE /api/v1/node-groups/{id}/expiration`. Background scanner runs daily;
  sends warning emails at 30, 14, and 7 days. `NotifyExpirationWarning` added to
  the notifications package (text + HTML templates).

- **CSP regression tests (F4)** — `test/js/csp-policy.test.mjs` (18 tests)
  asserts no inline scripts, no inline handlers, CSP header configured, Alpine
  CSP build used, SIEM export route registered. Added to CI.

- **Documentation (F5)** — `docs/security-headers.md` (CSP policy reference),
  `docs/audit.md` (SIEM export guide, JSONL schema, action reference).

---

## [v1.4.0] — 2026-04-27

**Sprint E — Governance Polish (CF-11, CF-15, CF-16, CF-20, CF-39)**

Completes the governance layer with a unified allocation change request workflow,
NSF Field of Science taxonomy, per-attribute visibility controls, per-user
notification preferences, and HTML email templates for all notification events.
No breaking changes; all new features are additive.

### Added

- **Allocation change request workflow (E-1 / CF-20)** — Replaces the
  point-solution `pi_expansion_requests` with a unified `allocation_change_requests`
  table (migration 064) covering five request types: `add_member`, `remove_member`,
  `increase_resources`, `extend_duration`, `archive_project`. Each request carries
  a `payload` JSON blob, status (`pending/approved/denied/expired/withdrawn`),
  reviewer ID, and review notes. Existing expansion requests are migrated on
  upgrade. PI portal gains a "Change Requests" tab with submit/withdraw UI and
  status badges. Admin settings gain a "Governance" tab showing a pending-queue
  table with one-click approve/deny (deny prompts for a review note), plus a
  recent history table. On decision, a notification email is sent to the PI.

- **Fields of Science taxonomy (E-2 / CF-16)** — `fields_of_science` table
  (migration 065) with two-level NSF hierarchy (18 top-level fields, ~130 leaf
  entries, `nsf_code` classification). `field_of_science_id` nullable FK added to
  `node_groups`. PIs set their group's FOS via a dropdown on the PI portal group
  card. Director portal gains a "Field of Science" tab with utilization breakdown
  (group counts per FOS, percentage bar). Admin Governance tab surfaces FOS CRUD
  (add/edit entries). Routes: `GET /api/v1/fields-of-science` (public),
  `PATCH /api/v1/portal/pi/groups/{id}/field-of-science` (PI),
  `GET|POST /api/v1/admin/fields-of-science`,
  `PUT /api/v1/admin/fields-of-science/{id}` (admin),
  `GET /api/v1/portal/director/fos-utilization` (director).

- **Per-attribute visibility policy (E-3 / CF-39)** — `project_attribute_visibility`
  (per-project overrides) and `attribute_visibility_defaults` (global defaults)
  tables (migration 066). Four visibility levels: `public > member > pi > admin_only`.
  `CanSee(role, level, isPI, isMember)` helper in the DB layer. D26 defaults seeded
  at migration: grant amounts/numbers are `pi`, hardware details are `admin_only`,
  standard group fields are `public`. PIs can override defaults per group via the
  PI portal "Visibility" tab. Admins manage global defaults in the Governance tab.
  Routes: `GET|PATCH /api/v1/portal/pi/groups/{id}/attribute-visibility`,
  `DELETE /api/v1/portal/pi/groups/{id}/attribute-visibility/{attr}` (PI),
  `GET|PUT /api/v1/admin/attribute-visibility-defaults/{attr}` (admin).

- **Per-user notification preferences (E-4 / CF-11/CF-15 enhancements)** —
  `user_notification_prefs`, `notification_digest_queue`, and
  `notification_event_defaults` tables (migration 067). Delivery modes:
  `immediate | hourly | daily | weekly | disabled`. D19 defaults seeded:
  immediate for account created, membership changes, PI decisions, and broadcast;
  daily for annual review reminders. Any authenticated user can manage their own
  prefs via `GET /api/v1/me/notification-prefs`,
  `PUT /api/v1/me/notification-prefs/{event}`,
  `POST /api/v1/me/notification-prefs/reset`. Admin can inspect any user's prefs
  at `GET /api/v1/admin/users/{id}/notification-prefs`. Digest queue processor
  background worker flushes due entries hourly, batching by recipient.

- **HTML email templates (E-4 / CF-15)** — All eight notification events now have
  HTML counterparts alongside plain-text templates, rendered as
  `multipart/alternative` MIME messages. `RawMailer` interface added so
  `SMTPMailer` can send pre-built MIME messages; non-HTML mailers fall back to
  plain text transparently. New `AllocationChangeDecisionData` struct and
  `NotifyAllocationChangeDecision()` Notifier method for the new E-1 workflow.

- **Admin Governance settings tab** — New "Governance" tab in the admin Settings
  panel consolidates: allocation change request queue (pending + history), FOS
  taxonomy management (add/edit entries), and attribute visibility defaults
  (global level picker per attribute).

- **Director FOS utilization breakdown** — New "Field of Science" tab in the
  Director portal shows each FOS field with group count and percentage bar.
  Data from `GET /api/v1/portal/director/fos-utilization`.

- **DB migrations 064–067** — Allocation change requests (with data migration from
  `pi_expansion_requests`), fields of science, attribute visibility policy tables,
  and notification preferences with seeded defaults.

### Changed

- `AllocationChangeRequestHandler` and `NotificationPrefsHandler` now accept an
  injected `GetActorInfo` closure for correct admin attribution in audit logs
  (avoids import cycle; previously fell back to literal `"admin"`).
- `/me/notification-prefs` routes now require `requireScope(true)` (session auth)
  to ensure user ID is available in context.
- Notifier instance stored on `Server` struct for use by the digest processor
  background worker.

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
