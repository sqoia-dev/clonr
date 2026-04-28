# Changelog

All notable changes to clustr are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

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
