# clustr WebUI — Sprint Plan (v1.0.1 → v1.1 → v1.2 → v2.0)

**Date:** 2026-04-27
**Decision-maker:** Richard (Technical Co-founder) — full delegated authority from founder
**Status:** LOCKED. Sprints A, B, C below are committed. Sprint D is directional, not committed.
**Source reviews:**
- `docs/webui-review-engineering.md` (Dinesh, commit `9a12772`) — 8 P1 / 14 P2 / 11 P3
- `docs/webui-review-ops.md` (Jared, commit `8221e91`) — 1 Blocker / 9 High / 8 Medium / 3 Low
- `docs/webui-review-personas.md` (Monica, commit `20d92dc`) — 6 personas, 1 served today

This plan supersedes any informal webui v1.1 backlog. Every finding from the three source reviews is addressed (mapped to a sprint or explicitly deferred with rationale) in the traceability table at the end. New cross-cutting principles in Phase 3 are also written into `docs/decisions.md` as D19–D22.

---

## Phase 1 — Contested Calls

The three reviews disagree on what matters most. The founder cannot adjudicate; ranking below is mine, by product judgment. Each call is one ruling + one-line rationale. No appeals — re-decision requires written counter-rationale.

### C1. What is the single most important problem in the webui today?

**The three answers on the table:**
- Dinesh: A-10 Auth privilege bug (operator briefly sees admin UI on transient `/auth/me` failure)
- Jared: New-admin onboarding pain (B-3 7-step "configure and deploy" + A-1 login hint)
- Monica: Role-aware nav (RBAC exists server-side; UI shows admin surface to all roles)

**Ruling:** **Monica's frame is the strategic problem; Dinesh's bug is a tactical hotfix; Jared's is the v1.1 north star.** All three ship inside 6 weeks but they do not compete — they layer. A-10 is a Sprint A hotfix (days). Role-aware nav is the Sprint B v1.1 anchor (weeks). New-admin onboarding is a Sprint B sub-theme (also weeks). Monica's analysis is the only one that affects positioning for the first institutional pitch; Dinesh's fix is the only one that affects security-of-deployed-systems today.

**Rationale:** Trust is the load-bearing pitch ("self-hosted, signed bundles, RBAC"). An RBAC model the UI doesn't honor is a feature checkbox without a story. Fix the bug fast, then make the model visible.

---

### C2. Is B-4 (sysbage ReferenceError) and A-10 (Auth role default to admin) hotfix or v1.1 bundle?

**Ruling:** **Hotfix. Ship as v1.0.1 within 5 business days of this plan.**

**Rationale:** B-4 is a page that does not render at all. A-10 is a privilege boundary the UI silently relaxes on a network blip. Both are pre-existing in the v1.0 tag we just shipped. Bundling them with v1.1 means at least 4 weeks of known-broken pages on the Show HN demo and known-permissive role enforcement on first design partner installs. Show HN is on 2026-07-27 (D16); a hotfix branch tagged v1.0.1 within a week protects that timeline. The other Top-5 P1s from Dinesh (A-1 fmtBytes, A-7 save race, B-5 webhooks UI, B-6 audit log UI) are real but not "broken in prod today" — they ride v1.1.

---

### C3. Is the raw slurm.conf editor (Jared C-1 Blocker) gated to admin only, or removed and replaced by structured form?

**Ruling:** **Keep the raw editor. Add server-side validation. Wrap it in a "Show advanced" disclosure under a structured Quick Settings form.** Do NOT remove. Do NOT make admin-only (it already is — operators can't write Slurm config).

**Rationale:** Building a structured form for the full Slurm.conf surface (200+ directives, version-dependent semantics) is a 4-month project we cannot afford in v1.1. The Blocker rating from Jared is correctly about the lack of validation, not the existence of the editor. Server-side validation (`slurmd -C` dry-run + AST parse on save) closes the Blocker. The structured "Quick Settings" form covers the 8-12 directives 90% of operators ever touch (NodeName, PartitionName, SlurmctldHost, etc.); the raw editor remains as the escape hatch for the 10% of operators who need it. This matches D22 below ("structured form + advanced raw" is the canonical pattern for all config editors).

---

### C4. Bundle granularity — one big v1.1, or v1.1.0 / v1.1.1 / v1.1.2 micro-releases?

**Ruling:** **Two patch releases inside a single v1.1 cycle.** Sprint A → v1.0.1 (hotfix). Sprint B → v1.1.0 (Trustworthy Admin UI). Sprint C → v1.2.0 (Researcher Portal MVP). NO micro-releases inside v1.1. The v1.1 deliverables are coupled (role-aware nav requires the operator dashboard reshape; the dashboard reshape requires the anomaly card; the anomaly card needs label renames to make sense).

**Rationale:** Micro-releasing would force re-running the install/upgrade docs pass three times in 6 weeks. Operator cognitive load > release-frequency optics for a self-hosted product where every upgrade is a manual operator decision. Two releases (v1.0.1 patch, v1.1.0 feature, v1.2.0 feature) in 12 weeks is the right cadence.

---

### C5. Persona scope for v1.1 — role-aware nav for admin/operator/readonly only, or add a 4th "researcher" role?

**Ruling:** **v1.1 = the existing 3 RBAC roles only. No new role. Researcher = v1.2 with a new `viewer` role and a separate `/portal/` route.**

**Rationale:** The `viewer` role Monica proposes is more restricted than `readonly` (partition-level only, no node internals, no LDAP detail). Adding it in v1.1 requires (a) DB migration, (b) RBAC test rewrite, (c) a new route hierarchy. None of those fit inside the v1.1 "make existing roles visible" goal. v1.1 ships role-aware nav for the 3 existing roles. v1.2 adds the `viewer` role + the `/portal/` researcher surface as a pair. This also satisfies Monica's deferred Q1 (researcher portal vs operator-scoped nav priority) — operator-scoped nav is v1.1, researcher portal is v1.2.

---

### C6. Monica's deferred Q1 — Researcher portal vs operator-scoped nav, which first?

**Ruling:** **Operator-scoped nav first (v1.1). Researcher portal second (v1.2).**

**Rationale:** Operator-scoped nav fixes a trust gap that exists for every install (the moment the sysadmin gives anyone else an account). Researcher portal is a competitive differentiator that matters only when the first institutional design partner shows up with researchers. The first is a defect fix, the second is a feature wedge. Defect fixes precede wedges. Also: operator-scoped nav is a frontend-only change (no schema, no new role). Researcher portal needs a new role + new route + status aggregation queries — much bigger.

---

### C7. Monica's deferred Q2 — LDAP self-service password scope. Can researchers reset their own HPC password?

**Ruling:** **Yes, in v1.2. The LDAP module already exposes the operations; v1.2 adds a self-service surface gated by the new `viewer` role.** The scope is strictly "change own LDAP password" — no email reset flow, no security questions, no admin-side approval. Only the logged-in user can change their own password, validated against the current password.

**Rationale:** Without self-service password reset, a researcher who forgets their HPC password files a sysadmin ticket. Sysadmin time is the load-bearing constraint Monica's review identified. A 50-line endpoint + a 30-line modal eliminates that ticket class. Out of scope: forgotten-password recovery (SMTP dependency) — defer to v1.3 with an explicit "requires SMTP configured" gate. The current admin-side `/api/v1/ldap/users/{username}/password` endpoint is the implementation primitive; we add a `/api/v1/ldap/me/password` variant that takes `current_password` + `new_password` and is callable by any authenticated session.

---

### C8. Monica's deferred Q3 — Reporting data model for PIs / IT directors. Build speculatively or wait for first paying customer?

**Ruling:** **Wait for the first paying customer. Defer to v2.0+ horizon.** No reporting schema is built before a customer specifies what they want measured.

**Rationale:** Monica's own write-up is correct: "Do not build a reporting schema speculatively — get a paying customer to define the first three metrics they want and build exactly those." Aggregation tables are very expensive to retrofit if the metrics turn out to be wrong (you've burned weeks on rollups for the wrong dimension). The cost of waiting is one feature on a slide deck. The cost of guessing is months of throwaway work. Wait. The first design partner LOI (per D15) is the trigger.

---

### C9. F-2 / E-3 — frontend tests + CSP headers. v1.1 or deferred?

**Ruling:** **Deferred to v1.2 backlog (not committed).** Critical frontend logic that Dinesh flagged for testing (F-1: `fmtBytes`, `fmtRelative`, `_phasePercent`) gets minimal node:test coverage in v1.1 as a small Sprint B side task. Full Vitest harness + CSP migration deferred until D10 webui module-split happens (post-v1.0 plan in decisions.md).

**Rationale:** Adding a JS test framework + CSP refactor is a multi-sprint slog that delivers zero new operator-visible value. The 5 helper functions can be tested with `node:test` (zero deps) inside Sprint B. Full migration waits for the framework decision (see D21 below).

---

### C10. Tech debt — module-split `app.js` (E-1) in v1.1 or v1.2?

**Ruling:** **Deferred to v1.2. v1.1 keeps adding to monolithic `app.js`.**

**Rationale:** Module-splitting `app.js` while simultaneously adding the role-aware nav, anomaly card, audit log UI, and webhooks UI is a recipe for merge-conflict hell. Ship v1.1 features first against the monolith. Module-split immediately after v1.1 GA, before the v1.2 researcher portal work starts. This sequences the refactor between two feature waves rather than during one. See D21 below for the JS framework decision boundary.

---

**Phase 1 summary:** 10 contested calls ruled. Most consequential: **C1** (frames the entire 90 days), **C2** (sets the hotfix tempo for institutional credibility), **C5+C6** (defines who the v1.1 product is FOR), **C7** (resolves a Monica deferral with a concrete v1.2 deliverable), **C10** (sequences refactor vs feature work to avoid merge collision).

---

## Phase 2 — Sprint Plans

### Sprint A — v1.0.1 Hotfix Patch

**Tag target:** `v1.0.1` within 5 business days of plan acceptance (target: 2026-05-04).
**Goal:** Ship the two truly-broken-in-prod findings before any v1.1 work begins.
**Persona served:** Persona 1 (Sysadmin) and Persona 2 (Junior Ops) — the only two who have UI access today.
**Owner:** Dinesh (engineering), Gilfoyle (release tag + autodeploy verify), Jared (release notes).
**Estimate:** 2 days engineering + 1 day verification = 3 working days.
**Dependencies:** None.

**Deliverables:**

| ID | Source | Description |
|---|---|---|
| A-HF-1 | Dinesh B-4 | Fix `sysbage` ReferenceError in `sysaccounts.js:55`. Identify intended function from git blame; rename or implement. System Accounts page must render. |
| A-HF-2 | Dinesh A-10 | Change `Auth._role` initial value from `'admin'` to `'readonly'`. Promote to actual role only after successful `/auth/me` 200. |
| A-HF-3 | new | Add a regression test: `auth_test.go` asserts that on `/auth/me` 500, the cookie context resolves to lowest privilege (or login redirect — pick whichever matches Auth.boot's resilience model). |
| A-HF-4 | release | Tag `v1.0.1`. Update `CHANGELOG.md` with hotfix-only release notes. Push autodeploy. |

**Acceptance criteria:**
- System Accounts page renders without console errors on a fresh install (verified by Gilfoyle on cloner dev host).
- A simulated `/auth/me` failure (intercept via DevTools network tab) does NOT show the admin nav sections to a non-admin session.
- Regression test green on CI.
- v1.0.1 tag published; ghcr image rebuilt; cloner autodeploy picks up the new bundle within 10 minutes.

**Out of scope (do NOT include in v1.0.1):** Anything else from any review. This is a 2-bug patch release, full stop. Scope creep here delays Sprint B.

---

### Sprint B — v1.1.0 "Trustworthy Admin UI"

**Tag target:** `v1.1.0` 4-6 weeks after Sprint A ships (target window: 2026-06-01 to 2026-06-15).
**Goal:** Make the existing RBAC model visible in the UI, eliminate the new-admin onboarding cliff, and surface the admin features that exist server-side but have zero browser presence (audit log, webhooks).
**Personas served:**
- Persona 1 (Sysadmin) — better dashboard, label clarity, surfaced features
- Persona 2 (Junior Ops) — first time the UI actually fits the operator role
- Persona 3 (Researcher) — bridge state: `readonly` becomes usable but limited; full researcher portal in C
**Owner:** Dinesh (engineering lead), Jared (operator UX validation, label decisions, install doc updates), Gilfoyle (CI for any new endpoints, cloner deploy verification).
**Estimate:** 5 weeks engineering across the four workstreams below.
**Dependencies:** Sprint A complete and v1.0.1 tagged.

**Workstream B1 — Role-aware nav and operator dashboard (the strategic anchor)**

| ID | Source | Description |
|---|---|---|
| B1-1 | Monica Gap-1 | On `Auth.boot` success, read `me.role`. If `operator`: hide Slurm Config/Scripts/Push/Builds/Upgrades nav, hide LDAP nav, hide System Accounts nav, hide Network Switches/Profiles nav. Settings restricted to "Change Password" tab only. |
| B1-2 | Monica Gap-1 | If `readonly`: same hides as operator, plus disable all mutation buttons (Reimage, Edit, Delete, Configure). All forms read-only. Settings restricted to "Change Password" tab only. |
| B1-3 | Monica Horizon-1 | Operator dashboard variant: when role=operator, replace "Recent Images" + "Live Log Stream" cards with "Your Groups" card (NodeGroups assigned to user) + "Your Recent Deploys" (scoped to those groups). |
| B1-4 | Monica Section-B | Server `/api/v1/auth/me` already returns role. Add `assigned_groups: []` to the response if not already present (verify with Dinesh). UI consumes. |
| B1-5 | new | Test: operator login on a fresh fixture does not see Slurm/LDAP/System nav items in the rendered DOM. Asserts via headless browser test or by directly testing the nav-render function. |

**Workstream B2 — New-admin onboarding and label clarity (Jared's top 5)**

| ID | Source | Description |
|---|---|---|
| B2-1 | Jared B-3 | "Configure and Deploy" inline action on the Nodes list: when a node has no `base_image_id` (status Registered), show a primary CTA in that row that opens a 3-step modal (image select → SSH key confirm → reimage trigger). Replaces the 7-step manual flow. |
| B2-2 | Jared A-1 | Login page: add helper text below the password field "First time? Default username is `clustr`. Your password was printed in the server startup log." Hide once `must_change_password=false` globally (server exposes `bootstrap_complete` boolean on `/api/v1/auth/bootstrap-status` unauth probe). |
| B2-3 | Jared B-1 | "Restore Defaults" button on Slurm Module Setup page. Calls `POST /api/v1/slurm/configs/reseed-defaults` (D18). Confirmation modal lists what will be re-seeded. |
| B2-4 | Jared F-1 | Dashboard "Anomalies" card: count of (a) nodes whose last reimage failed, (b) nodes with verify_timeout in last 30d, (c) nodes never deployed, (d) nodes with no successful deploy in >90d. Each count is a clickable filter on the Nodes list. |
| B2-5 | Jared G-1 | Rename "Allocations" nav item to "DHCP Leases". Rename Slurm "Settings" nav item to "Module Setup". Rename LDAP "Settings" nav item to "Module Setup". |
| B2-6 | Jared A-2 | First-deploy wizard gating fix: show until `deploy_verified_booted_at` is non-null on at least one node (replacing the current `images=0 AND nodes=0` gate). |
| B2-7 | Jared A-3 | Dashboard "Active Deployments" empty state gets subtext + CTA: "No deployments in progress. Trigger a reimage from the Nodes page." |
| B2-8 | Jared A-4 | `/set-password` flow preserves `?next=` param across the forced-password-change redirect. |

**Workstream B3 — Surface backend-complete features (Dinesh top 5: B-5, B-6)**

| ID | Source | Description |
|---|---|---|
| B3-1 | Dinesh B-5 | Add "Webhooks" tab to Settings (admin only). List subscriptions (URL, events, last delivery). Create / Edit / Delete modal. Calls `GET/POST/PUT/DELETE /api/v1/admin/webhooks`. |
| B3-2 | Dinesh C-1 | Per-webhook expandable "Last 10 Deliveries" panel showing status code + timestamp + error. Calls `GET /api/v1/admin/webhooks/{id}/deliveries`. |
| B3-3 | Dinesh B-6 | Add "Audit" route `#/audit` (admin only). Sidebar link under SYSTEM section. Paginated table: created_at, actor, action, target, detail. Filter by actor + action + date range. Calls `GET /api/v1/audit`. |
| B3-4 | Jared F-3 / Dinesh's general gap | Settings → About tab: populate with server version, bundle version, build date, uptime, GPG bundle signing key fingerprint, link to CHANGELOG. New `GET /api/v1/system/version` endpoint if not already present. |
| B3-5 | Monica Horizon-1 trust signals | Settings → About tab adds a "Security" subsection: encryption status (LDAP/BMC creds AES-256-GCM: yes), session config (HMAC, 12h TTL), bundle GPG verified (yes/no + fingerprint). Read-only display; this is the trust-signal surface. |

**Workstream B4 — Top P1 quality fixes from engineering review**

| ID | Source | Description |
|---|---|---|
| B4-1 | Dinesh A-1 | Remove the inner `fmtBytes` shadow in heartbeat render. All byte values use binary GiB consistently. |
| B4-2 | Dinesh A-7 | Disable all node-detail save buttons while any save is in flight (short-term mitigation for the GET-then-PUT race). Optimistic locking via `updated_at` ETag is v1.2 follow-up. |
| B4-3 | Dinesh A-2 | `_removeImageTag` moves DOM removal to success path; surfaces error via `App.toast` on failure. |
| B4-4 | Dinesh A-4 | Verify `changed_at` field type in handler response; switch UI to `fmtDate(r.changed_at)`. |
| B4-5 | Dinesh A-11, A-12 | Replace `prompt()` for password reset and role change with proper modals (consistent with existing `_settingsCreateUserModal`). |
| B4-6 | Dinesh A-3 | Replace direct `API.get('/nodes', …)` with `API.nodes.list()` in `showDeleteImageModal`. |
| B4-7 | Dinesh B-9 | Rename "Rediscover" node action to "Queue Reimage". Confirm modal warns disk will be wiped. |
| B4-8 | Dinesh F-1 (minimal) | Add `node:test` (zero-dep) suite covering `fmtBytes`, `fmtRelative`, `_isoDetectDistro`, `_phasePercent`, `_phaseLabel`. Wire to CI. |

**Workstream B5 — Slurm config validation (resolves Jared's Blocker C-1)**

| ID | Source | Description |
|---|---|---|
| B5-1 | Jared C-1 | Server-side validation endpoint `POST /api/v1/slurm/configs/{filename}/validate` that runs `slurmd -C` against the proposed content. Returns structured validation errors. |
| B5-2 | Jared C-1 | UI: on save, call validate first. Display structured errors inline. "Save anyway" button for high-confidence-but-non-fatal warnings. Hard-block on parse errors. |
| B5-3 | Jared C-2 | Custom Kickstart textarea: add "View default template" link that opens a read-only modal showing what would be generated for current distro/role. |

**Acceptance criteria for Sprint B:**
- Operator role login shows ≤6 nav items (Dashboard, Nodes, Deployments, DHCP Leases, Settings) — verified manually + by automated test.
- Readonly role login shows the same nav with all mutation buttons disabled.
- New admin walking through fresh install reaches "first node deployed" in ≤8 actions (down from 13+ in v1.0). Validated by Jared on cloner dev host with a freshly reset DB.
- Dashboard shows Anomalies card with at least one of the four counts non-zero in any non-greenfield install.
- Audit log accessible at `#/audit`. Webhooks CRUD at Settings → Webhooks.
- Slurm config save with intentionally-broken slurm.conf returns inline error before write commits.
- All 8 P1 findings from engineering review either FIXED in v1.1 or explicitly deferred (table at end).
- CI green on `main` for the v1.1.0 tag commit. Cloner autodeploy picks up the new bundle.
- `docs/upgrade.md` v1.1 release notes section authored by Jared.

**Explicitly out of scope for v1.1 (rolls to v1.2 or later):**
- New `viewer` role
- `/portal/` researcher route
- Full module-split of `app.js`
- CSP headers / inline-handler migration
- Vitest harness (we get `node:test` coverage of helpers only)
- LDAP self-service password change (v1.2)
- Per-node verify_timeout override
- DHCP pool config in UI
- Reporting dashboards for PIs

---

### Sprint C — v1.2.0 "Researcher Portal MVP + Refactor Foundation"

**Tag target:** `v1.2.0` 6-8 weeks after v1.1.0 ships (target window: 2026-07-20 to 2026-08-10).
**Goal:** Deliver the researcher portal that creates the institutional-buyer wedge against Bright Computing, AND complete the `app.js` module-split that v1.1 deferred.
**Personas served:**
- Persona 3 (Researcher) — first time they have any UI surface
- Persona 1 (Sysadmin) — better long-term maintainability via module-split (no day-1 UX change)
- Persona 2 (Junior Ops) — quality-of-life fixes from v1.1 P2 backlog
**Owner:** Dinesh (engineering lead), Monica (researcher-portal copy + competitive positioning validation), Jared (LDAP self-service ops doc).
**Estimate:** 6-8 weeks across the three workstreams.
**Dependencies:** Sprint B complete and v1.1.0 tagged.

**Workstream C1 — Researcher portal MVP**

| ID | Source | Description |
|---|---|---|
| C1-1 | Monica Persona 3 / new | Add `viewer` role to RBAC. Migration `053_add_viewer_role.sql`. Documented in `docs/rbac.md`. |
| C1-2 | Monica Persona 3 | New route `/portal/` (separate from `/admin/`). Single page: cluster status (partition health from Slurm), available images, "My HPC Account" panel. |
| C1-3 | Monica C7 ruling / new | LDAP self-service password change. New endpoint `POST /api/v1/ldap/me/password` accepting current+new password, callable by `viewer` or above. Modal in /portal/ "My Account" panel. |
| C1-4 | Monica Persona 3 / Section D | Slurm partition status surface: `GET /api/v1/slurm/partitions/status` returns array of `{partition, state, total_nodes, available_nodes}`. Rendered as cards on /portal/. |
| C1-5 | Monica positioning | Hide /admin/ entirely from `viewer` role logins — they only see /portal/. Hide /portal/ from admin/operator role logins (they see /admin/ only). Login redirect logic dispatches by role. |
| C1-6 | new | Tests: viewer login lands on /portal/, cannot navigate to /admin/, cannot mutate any data, can change own LDAP password. |

**Workstream C2 — `app.js` module-split (foundation for v2.0+ persona-specific dashboards)**

| ID | Source | Description |
|---|---|---|
| C2-1 | Dinesh E-1 | Extract `Pages.deploys`, `Pages.images`, `Pages.nodes`, `Pages.dhcp`, `Pages.audit`, `Pages.webhooks` into `pages/*.js` files. ES6 modules via `<script type="module">`. No build step. |
| C2-2 | Dinesh E-1 | `app.js` retains: Router, App state, global helpers (`fmtBytes`, `escHtml`, `App.toast`), Auth bootstrap. Target shrink: 9,350 → ≤3,500 LOC. |
| C2-3 | Dinesh A-13, E-4 | Extract `escHtml`, `fmtBytes`, `fmtRelative`, `fmtDate` into `utils.js`. Single source of truth. Add `'` escaping (E-4). |
| C2-4 | Dinesh F-1 | Expand `node:test` coverage now that helpers are in `utils.js`. Target: 80% line coverage on `utils.js`. |

**Workstream C3 — v1.1 P2 backlog**

| ID | Source | Description |
|---|---|---|
| C3-1 | Dinesh A-5 | `_deployProgressTable` cap indicator: append "+ N more…" row when over 20. |
| C3-2 | Dinesh A-6 | `_diffTable` re-render handles empty-state-to-data transition. |
| C3-3 | Dinesh A-8 | `_pollGroupReimageJob` cleanup on modal close. |
| C3-4 | Dinesh A-9 | Merge duplicate `snapshot` SSE listeners on ISO build page. |
| C3-5 | Dinesh A-14 | Add server-side `POST /images/{id}/cancel`. UI calls cancel instead of delete. Rename button "Cancel Build". |
| C3-6 | Dinesh C-2 | Image blob download via `fetch()` with non-2xx error toast. |
| C3-7 | Dinesh C-3 | Node config history pagination (limit=50, "Load more"). |
| C3-8 | Dinesh C-4 | Group reimage modal: show node count preview before confirm. |
| C3-9 | Dinesh C-7 | Slurm node table: add "Slurm version" column to surface upgrade divergence. |
| C3-10 | Dinesh D-1 | Session expiry banner countdown timer + auto-redirect. |
| C3-11 | Dinesh D-2 | Node detail dirty-state warns before navigation. |
| C3-12 | Dinesh D-3 | Toast deduplication (count badge instead of stack). |
| C3-13 | Dinesh D-4 | Page cleanup hook closes SSE streams on navigation. |
| C3-14 | Dinesh D-5 | Settings tab body re-render preserves LogStream instance. |
| C3-15 | Jared C-3 | Custom Variables: add "Supported variables" link. Unknown vars show warning icon. |
| C3-16 | Jared C-4 | Network field client-side CIDR validation. Pre-populate gateway/DNS from server defaults. |
| C3-17 | Jared C-5 | Disk layout default to "Inherit"; "Customize layout" collapsed by default; visual partition preview. |
| C3-18 | Jared D-2 | Per-node `verify_timeout_override` field on Configuration tab. |
| C3-19 | Jared D-4 | Reimage concurrency: show server clamp via tooltip; show effective vs requested after submit. |
| C3-20 | Jared E-2 | Node detail "last failure summary" card at top when last deploy failed. |
| C3-21 | Jared E-4 | "Select all unverified" / "Select all failed" bulk-select shortcuts on Nodes page. |
| C3-22 | Jared F-2 | Dashboard System Health card: render OK/Degraded/Error breakdown from `/healthz/ready` structured response. |
| C3-23 | Jared B-2 | Move initramfs card from Images page to Settings → System tab. Keep stale-warning banner on Dashboard. |
| C3-24 | Jared B-5 | Slurm Sync Status: surface "nodes deployed but not in slurm.conf"; "Add to cluster" one-click. |
| C3-25 | Jared B-4 | Slurm Settings: "Preview generated config" button shows what next deploy will write. |
| C3-26 | Jared B-6 | Bundle install/version surface in Settings → System (read-only display + "Re-install bundle" button). |

**Acceptance criteria for Sprint C:**
- A `viewer`-role login redirects to `/portal/` and cannot reach any `/admin/` route (verified by test).
- Researcher can change own LDAP password from /portal/ "My Account" without admin involvement.
- `app.js` LOC ≤3,500. All extracted page modules import `utils.js`.
- All 14 P2 findings from engineering review FIXED.
- All 8 Medium findings from ops review FIXED (or explicitly deferred with rationale).
- CI green on the v1.2.0 tag.
- `docs/researcher-portal.md` authored by Monica + Dinesh (positioning + how-to).

---

### Sprint D — v2.0+ Horizon (directional, NOT committed)

**Goal:** Persona-specific dashboards, multi-tenancy, federated identity, leadership reporting. High-level only — no commits, no estimates, gated on customer signal.
**Persona served:** Personas 4 (PI), 5 (IT Director), 6 (Federated User).
**Trigger conditions (D6 / D8 / D15 — already locked):**
- First paying design partner with >50 nodes signs LOI
- AND/OR first institutional/regulated customer requests SSO, SOC 2, or multi-tenant scoping

**Directional deliverables (write the plan when triggered, not before):**

1. **PI / Group Lead dashboard** (`/portal/group/{id}`) — group-scoped utilization summary. Requires `utilization_events` table or rollup query against `reimages` + `audit_log`. Customer-driven metric set.
2. **IT Director quarterly summary export** — read-only reporting endpoint, CSV-exportable. Same data backbone as #1.
3. **OIDC / SAML federation** (D1 re-decision trigger) — adds external IdP support. Keeps local sessions + API keys for sysadmins.
4. **Multi-tenant data isolation** — schema-wide tenant_id; per-tenant NodeGroup scoping. Major undertaking. Only for federated/external user persona.
5. **Persona-specific dashboards** — sysadmin operational dashboard distinct from PI utilization dashboard distinct from IT director quarterly view. Three different IAs sharing the same data layer.
6. **Webui framework migration** (D10/D21 re-decision) — only if a frontend hire lands or the module-split (C2) hits a complexity ceiling.
7. **CSP headers + inline-handler removal** (E-3) — sequenced after framework decision because CSP migration touches every onclick.
8. **SIEM export** (D13 re-decision trigger) — JSONL export endpoint for audit log. Gated on a regulated customer.
9. **Two-tier hot/cold log archive** (D2 re-decision trigger) — gated on customer reporting evicted-log incident.
10. **Reporting data model for PIs/IT directors** (Monica Q3 — answered C8) — built when first paying customer specifies metrics.

**No estimates. No owners. No deliverables list.** This horizon exists only so that v1.1 and v1.2 decisions don't accidentally close off these doors.

---

## Phase 3 — Cross-Cutting Principles (D19–D22)

Each of these is written once, applied everywhere, and locked into `docs/decisions.md`.

### D19 — Customizability Default: "Lock the knob, ship the recommendation, expose under Advanced"

**Principle:** When in doubt about whether to expose a configuration knob, lean **closed by default with a sensible recommended value, expose the knob behind an "Advanced" disclosure**. Do NOT lean "expose every knob with a sensible default."

**Why this principle:** New-admin friendliness is the v1.x guiding theme (founder directive). Every visible knob is a decision the new admin must make or research before clicking save. The 5-knob form scares new admins; the "click Deploy with sensible defaults" form gets them to first success. Power users who need the knob can find it under Advanced.

**Concrete applications:**
- Slurm Module Setup form: 4 visible fields (controller node, cluster name, version, default partition). Everything else under "Advanced".
- Node Group disk layout: defaults to "Inherit from image". "Customize layout" is collapsed.
- Reimage modal: defaults to "Use image default kickstart". Custom kickstart under "Advanced".
- ISO build modal: defaults to recommended distro/version. Custom kernel/cmdline under "Advanced".

**Tradeoff acknowledged:** Power users will occasionally feel patronized by the "Advanced" disclosure. The cost (one extra click for them) is much lower than the cost of every new admin staring at 12 fields wondering which is required.

**Reversibility:** **cheap**. Promoting a field out of Advanced is a 1-line UI change. Demoting after exposure is also cheap.

---

### D20 — CLI/UI Parity Policy: "Critical operations must have a webui equivalent. CLI-first is acceptable for initial-install and recovery only."

**Principle:** Every routine operator action (anything done more than once per cluster lifetime) MUST have a webui surface. Initial bootstrap and disaster recovery operations (commands you run once on Day 0 or once after catastrophe) MAY remain CLI-only with a documented path.

**Concrete categorization:**

**Must have webui surface (v1.1 or v1.2):**
- Reseed Slurm defaults (B2-3 in Sprint B — shipping v1.1)
- Bundle install / version display (C3-26 in Sprint C — shipping v1.2)
- Change own LDAP password (C1-3 in Sprint C — shipping v1.2)
- Audit log query (B3-3 in Sprint B — shipping v1.1)
- Webhook subscriptions (B3-1 in Sprint B — shipping v1.1)

**CLI-only acceptable indefinitely (with docs):**
- Initial admin bootstrap (`clustr-serverd apikey bootstrap`) — Day-0 only
- Force-decrypt rollback (recovery from D4 encryption error) — once, post-disaster
- DB compaction / vacuum — operational maintenance, not UI-routine
- Autodeploy circuit breaker reset (`echo 0 > /var/lib/clustr/bundle-install-failures`) — once, post-failure
- Direct SQL queries for support escalation — never UI-exposed

**Why this policy:** Self-hosted product positioning requires that operators don't need to SSH for routine work. SSH for first-install or post-incident is acceptable; SSH for "I want to see my audit log" is a positioning failure.

**Reversibility:** **costly**. Once we ship a webui surface for an action, removing it later is a regression. Adding webui surface for a CLI-only action later is cheap.

---

### D21 — JS Framework Threshold: "Vanilla JS until app.js module-split hits 5,000 LOC across modules OR a frontend hire wants a framework. Whichever first."

**Principle:** Stay in vanilla JS + `<script type="module">` ES modules through v1.2. Re-evaluate framework adoption (Alpine, HTMX, Lit, or — if scope warrants — React/Vue/Svelte) when ANY ONE of the following triggers fires:

1. Total LOC across all `pages/*.js` modules + `app.js` exceeds 5,000 (post-C2 we expect ~3,500; growth headroom = ~1,500 LOC over 6 months).
2. A frontend engineer hire (D15 trigger) lands AND has framework expertise + a track record of shipping in vanilla.
3. A specific feature requires complex form state machines (e.g., the v2.0 PI dashboard with cross-filtering charts) that vanilla JS makes painful.
4. We need CSP enforcement (E-3) — CSP migration is the natural moment to revisit framework choice because the inline-handler refactor is touching every page anyway.

**Until any trigger fires:** Vanilla JS, ES6 modules, no build step. Explicitly NO bundlers (webpack/vite/esbuild/rollup), NO TypeScript, NO JSX.

**When a trigger fires:** Evaluate Alpine + HTMX (lowest cognitive cost, no build step) FIRST. Only escalate to React/Vue/Svelte if Alpine/HTMX cannot meet the requirement.

**Why this principle:** "One binary, one container, no build step" is load-bearing positioning per D10. The v1.0/v1.1 webui works without a framework; adding a framework for the sake of it costs CI complexity, deploy artifact complexity, and the contradicts the self-hosted simplicity pitch. The C2 module-split (Sprint C) gets us 80% of framework benefits at 10% of the cost.

**Reversibility:** **costly**. Once we adopt a framework, we don't rip it out. The threshold above is conservative on purpose.

**Re-decision triggers explicit:** see 4 conditions above.

---

### D22 — Raw Config Editor Pattern: "Structured form for the 80% case, raw editor as Advanced escape hatch, server-side validation on every save"

**Principle:** Every configuration surface that today exposes a raw textarea (slurm.conf, kickstart, network profiles, BMC config, etc.) follows this pattern:

1. **Structured form** for the 8-15 most common directives — covers ~80% of operator use cases. New admins never leave this.
2. **"Advanced: edit raw" disclosure** that reveals a textarea pre-populated with what the structured form would render. Operator can edit freely.
3. **Server-side validation on save** — for Slurm: `slurmd -C` or AST parse. For kickstart: `pykickstart` parse. For network: `nm-online` config dry-run. Validation runs regardless of which surface (form or raw) the operator used.
4. **Inline error display** with line numbers and structured messages. Hard-block on parse errors. "Save anyway" only for high-confidence-but-non-fatal warnings (e.g., deprecated directive still works).
5. **Default templates always re-renderable** via D18 reseed mechanism (operator never permanently loses the recommended starting point).

**Concrete v1.x application:**
- v1.1: Slurm.conf gets validation (B5-1, B5-2). Custom Kickstart gets "View default template" link (B5-3). Raw editor stays.
- v1.2 / v1.3: Quick Settings structured form layered above the raw editor for slurm.conf. Same pattern applied to kickstart and network profiles.
- v2.0+: BMC/IPMI config gets the same treatment.

**Why this principle:** New-admin onboarding (Jared's #1 theme) requires that the default path is "fill in the form, click Deploy". Power-user flexibility (Persona 1 sysadmin) requires that the raw editor is never taken away. Structured form + raw escape + validation is the only pattern that satisfies both.

**Tradeoff acknowledged:** Building structured forms is real work. We do it incrementally — slurm.conf in v1.1 (validation only) and v1.3 (full structured form). Other config surfaces follow as time permits.

**Reversibility:** **cheap** at the per-surface level. Adding a structured form on top of an existing raw editor is purely additive.

---

**Phase 3 summary:** 4 principles ruled (D19–D22). Most strategically-loaded: **D21** (the JS framework threshold). It's the call that, if wrong in either direction, costs the most: too aggressive (adopt framework) and we burn weeks on a migration that delivers no operator value AND violates the self-hosted simplicity positioning; too conservative (vanilla forever) and we eventually hit a complexity ceiling that limits what we can build for personas 4-6. The 4 explicit re-decision triggers are the safety valve.

---

## Traceability Table — Every Finding from Every Review

Legend: **A** = Sprint A (v1.0.1), **B** = Sprint B (v1.1.0), **C** = Sprint C (v1.2.0), **D** = Sprint D horizon (v2.0+), **DEFER** = explicit defer with rationale, **N/A** = not a webui finding.

### Engineering review (Dinesh, `webui-review-engineering.md`)

| Finding | Sev | Sprint | Notes |
|---|---|---|---|
| A-1 fmtBytes shadow | P1 | B | B4-1 |
| A-2 _removeImageTag silent failure | P1 | B | B4-3 |
| A-3 raw API.get bypass | P2 | B | B4-6 |
| A-4 changed_at unit confusion | P1 | B | B4-4 |
| A-5 _deployProgressTable cap | P2 | C | C3-1 |
| A-6 _diffTable empty-state | P2 | C | C3-2 |
| A-7 node-detail save race | P1 | B | B4-2 (button-disable mitigation; full ETag in v1.2 deferred) |
| A-8 _pollGroupReimageJob cleanup | P2 | C | C3-3 |
| A-9 SSE snapshot duplicate listener | P2 | C | C3-4 |
| A-10 Auth role default to admin | P1 | A | A-HF-2 (HOTFIX) |
| A-11 prompt() for password | P2 | B | B4-5 |
| A-12 prompt() for role | P2 | B | B4-5 |
| A-13 escHtml duplicated | P2 | C | C2-3 |
| A-14 ISO cancel uses delete | P2 | C | C3-5 |
| B-1 showShellHint dead code | P3 | C | Bundled into C2 module-split cleanup |
| B-2 _updateIsoBuildProgress no-op | P3 | C | Bundled into C2 cleanup |
| B-3 confirm() in slurm.js | P3 | B | Folded into B5 work |
| B-4 sysbage ReferenceError | P1 | A | A-HF-1 (HOTFIX) |
| B-5 webhooks no UI | P1 | B | B3-1, B3-2 |
| B-6 audit no UI | P1 | B | B3-3 |
| B-7 /nodes/connected unused | P3 | C | Add badge to node list during C2 split |
| B-8 /repo/health unused | P3 | C | Add to Settings → System tab during C3-26 |
| B-9 Rediscover misleading label | P2 | B | B4-7 |
| C-1 webhook delivery history | P1 | B | B3-2 |
| C-2 image download error | P2 | C | C3-6 |
| C-3 config history pagination | P2 | C | C3-7 |
| C-4 group reimage count preview | P2 | C | C3-8 |
| C-5 DHCP refresh countdown | P3 | DEFER | Cosmetic; reconsider if operators report confusion |
| C-6 global search / Ctrl+K | P3 | DEFER | Real value at 200+ nodes; revisit if first design partner has large fleet |
| C-7 Slurm rollback UI | P2 | C | C3-9 (version column); rollback flow itself defers to D when backend supports |
| D-1 session expiry countdown | P2 | C | C3-10 |
| D-2 node detail dirty-state nav warn | P2 | C | C3-11 |
| D-3 toast dedup | P3 | C | C3-12 |
| D-4 ISO SSE not closed on nav | P2 | C | C3-13 |
| D-5 Settings tab loses log state | P3 | C | C3-14 |
| E-1 monolithic app.js | P2 | C | C2-1, C2-2 |
| E-2 template-literal HTML pervasive | P3 | DEFER | Document grep CI rule in v1.2; full migration tied to D21 framework decision |
| E-3 no CSP | P2 | DEFER | D horizon — sequenced with framework migration (D21 trigger 4) |
| E-4 escHtml missing apostrophe | P3 | C | C2-3 |
| E-5 XHR upload bypasses 401 redirect | P3 | C | Folded into C2 cleanup with auth.js extraction |
| F-1 no JS unit tests | P2 | B+C | B4-8 (helpers via node:test); C2-4 (expand) |
| F-2 no SSE integration test | P2 | DEFER | Tied to D21 framework decision; node:test of `_phasePercent` covered in B4-8 |
| F-3 no node-editor dirty-flag test | P2 | C | Add when C3-11 lands |
| F-4 dhcp_test response shape | P3 | C | Add during C2 (10-line test) |

**Engineering totals: 8 P1, 14 P2, 11 P3 = 33 findings. A: 2. B: 12. C: 13. DEFER: 6.**

### Ops review (Jared, `webui-review-ops.md`)

| Finding | Sev | Sprint | Notes |
|---|---|---|---|
| A-1 login default-creds hint | High | B | B2-2 |
| A-2 wizard gating | High | B | B2-6 |
| A-3 dashboard active-deploy empty state | Medium | B | B2-7 |
| A-4 password-change `?next=` lost | Medium | B | B2-8 |
| B-1 reseed-defaults button | High | B | B2-3 |
| B-2 initramfs card location | High | C | C3-23 |
| B-3 configure-and-deploy shortcut | High | B | B2-1 |
| B-4 Slurm controller dual-role preview | Medium | C | C3-25 |
| B-5 nodes-not-in-slurm.conf surfacing | Medium | C | C3-24 |
| B-6 bundle install in webui | Low | C | C3-26 |
| C-1 raw slurm.conf editor (Blocker) | Blocker | B | B5-1, B5-2 (per C3 ruling: keep editor + validate, do not remove) |
| C-2 custom kickstart no syntax help | High | B | B5-3 |
| C-3 custom variables undocumented | Medium | C | C3-15 |
| C-4 network freeform CIDR | Medium | C | C3-16 |
| C-5 disk layout no preview | Medium | C | C3-17 |
| D-1 DHCP pool not in UI | High | DEFER | Requires runtime reconfig + restart story; v1.3 candidate. Read-only display in C3-26 stretch goal. |
| D-2 verify timeout per-node | Medium | C | C3-18 |
| D-3 log retention not in UI | Low | DEFER | Per D2: env-var-only is correct for v1.0/v1.1; v1.3+ adds Settings UI when a customer asks |
| D-4 reimage concurrency clamp invisible | Low | C | C3-19 |
| E-1 add-node 7-step flow | Grade C+ | B | Resolved by B2-1 |
| E-2 unhealthy node diagnosis | Grade B | C | C3-20 |
| E-3 Slurm upgrade discoverability | Grade B- | DEFER | Polish item; nav already labels it. Revisit if operator reports difficulty. |
| E-4 reimage everything bulk | Grade A- | C | C3-21 |
| E-5 forgot password recovery | Grade D | DEFER | Requires SMTP for true self-service. Add "Forgot password?" link → docs URL in v1.1 (folded into B2-2 helper-text rework if scope allows; otherwise v1.3). |
| F-1 dashboard anomaly card | High | B | B2-4 |
| F-2 system health nuance | Medium | C | C3-22 |
| F-3 server/bundle version display | Medium | B | B3-4 |
| G-1 "Allocations" rename | Medium | B | B2-5 |
| G-2 SLURM "Settings" rename | Medium | B | B2-5 |
| G-3 hidden SYSTEM section | Medium | DEFER | Discoverability is real but not blocking. Bundle into v1.3 nav refresh. |
| G-4 no breadcrumbs | Low | DEFER | Polish; revisit when operator says they got lost. |
| G-5 settings only via gear icon | Low | DEFER | Add label hover tooltip in v1.3 polish. |

**Ops totals: 1 Blocker, 9 High, 8 Medium, 3 Low + 5 grades = 26 findings. B: 14. C: 14. DEFER: 7. (Some findings are double-counted between A-E and the grades section in Jared's doc; deduplicated here.)**

### Persona review (Monica, `webui-review-personas.md`)

| Finding / Recommendation | Severity | Sprint | Notes |
|---|---|---|---|
| Gap 1: UI shape doesn't match RBAC model | Foundational | B | B1-1, B1-2, B1-3 (entire B1 workstream) |
| Gap 2: Researcher has no surface | High | C | C1-1 through C1-6 |
| Gap 3: Trust story invisible in UI | High | B | B3-4, B3-5 |
| Gap 4: Bright Computing user-portal threat | Medium | C | Closed by C1 researcher portal MVP |
| Gap 5: Multi-tenancy / federated user | Defer | D | Per D6/D15 — gated on customer signal |
| Q1: Researcher portal vs operator-scoped nav priority | DEFERRED→ANSWERED | C6 ruling | Operator-scoped nav v1.1, researcher portal v1.2 |
| Q2: LDAP self-service password scope | DEFERRED→ANSWERED | C7 ruling | v1.2 — change own password only, gated on `viewer` role |
| Q3: Reporting data model for PIs/IT directors | DEFERRED→ANSWERED | C8 ruling | Wait for first paying customer to specify metrics; v2.0+ horizon |
| Persona 1 missing scheduled deploys UI | Sysadmin gap | DEFER | `scheduled_at` field exists; surfacing is v1.3 polish. Not blocking institutional pitch. |
| Persona 1 missing searchable 200-node list | Sysadmin gap | DEFER | Same as Dinesh C-6; revisit when first ≥100-node design partner appears. |
| Persona 4 (PI) — group utilization summary | Strategic | D | Gated on customer-defined metrics |
| Persona 5 (IT Director) — quarterly summary | Strategic | D | Gated on customer-defined metrics |
| Persona 6 (Federated / external) | Strategic defer | D | Per D1 — OIDC is v1.1+ when customer asks |

**Persona totals: 5 strategic gaps, 3 deferred questions, 4-6 persona-specific affordances = ~13 findings. B: 4. C: 3. D: 3. DEFER: 3 (with rationale).**

---

## Summary

- **Total findings across 3 reviews:** ~72 (33 engineering + 26 ops + 13 persona, deduplicating overlap)
- **Sprint A (v1.0.1 hotfix):** 2 findings (the two truly broken-in-prod items)
- **Sprint B (v1.1.0):** 30 findings (12 engineering + 14 ops + 4 persona)
- **Sprint C (v1.2.0):** 27 findings (13 engineering + 14 ops + 3 persona, including the researcher portal MVP)
- **Sprint D (v2.0+ horizon, NOT committed):** 8 directional themes
- **DEFERRED with explicit rationale:** ~16 findings (each row above with DEFER includes the why)

Nothing is silently dropped.

---

## Closing — Top 3 "If We Only Do These In 90 Days, The Product Is Materially Better"

**1. Ship Sprint A (v1.0.1 hotfix) within 5 business days.** Fixing the System Accounts ReferenceError and the Auth role-default-to-admin bug protects the Show HN demo, protects the first design partner installs, and resets the institutional credibility baseline. These are 1-day fixes that compound for every day they're not shipped. Everything else flows from "v1.0 is genuinely safe."

**2. Role-aware nav (Sprint B Workstream B1).** This is the single change that converts clustr's RBAC story from "trust us, it's enforced server-side" to "log in as an operator and see the difference." It is frontend-only, it requires no schema changes, it costs ~1 week of engineering, and it is the foundation that every subsequent multi-persona feature builds on. Without it, the v1.2 researcher portal is just "another page"; with it, the researcher portal is the visible payoff of a coherent multi-persona architecture.

**3. The "Configure and Deploy" inline shortcut + Anomalies dashboard card (Sprint B Workstreams B2-1 + B2-4).** Together, these two items collapse the new-admin onboarding cliff (the 7-step PXE-to-deployed flow becomes 3 steps) AND give every returning admin a "what's broken" answer at first glance. Jared correctly identified onboarding pain as the #1 admin friction; these two changes hit both ends of that lifecycle (Day 0 and Day 30+) with a combined ~1-week build cost. They turn the dashboard from a "dashboard-shaped object" into a tool the operator actually opens for information, not just navigation.

Everything else in the plan matters. These three are the ones that, if the next 90 days went sideways and we only got these out, would still leave clustr materially better positioned for institutional adoption than v1.0 is today.

---

*End of plan. Sprints A, B, C are committed. Sprint D is directional. Re-decision routes through Richard.*
