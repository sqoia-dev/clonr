# clustr WebUI — Sprint Plan (v1.0.1 → v1.1 → v1.1.1 → v1.2 → v1.2.5 → v1.3 → v1.4 → v1.5 → v1.6 → v1.7 → v2.0+)

**Date:** 2026-04-27 (original) — **Updated 2026-04-27 (ColdFront integration pass: Sprint C scope expanded; Sprints C.5, D, E added; D24 + D25 ruled)** — **Re-sequenced 2026-04-27 (Sprint Z dissolved; Sprints F/G/H committed at v1.5/v1.6/v1.7; D27 supersedes D25; D28 versioning policy ruled)**
**Decision-maker:** Richard (Technical Co-founder) — full delegated authority from founder
**Status:** LOCKED. Sprints A, B, B.5, C, C.5, D, E, F, G, H are RELEASED (v1.0.1 → v1.7.0). Items in Buckets 2/3/4 of the dissolved Sprint Z are unscheduled; trigger conditions documented per item.
**Source reviews (deleted post-synthesis — recoverable via git log):**
- `docs/webui-review-engineering.md` (Dinesh, commit `9a12772`) — 8 P1 / 14 P2 / 11 P3
- `docs/webui-review-ops.md` (Jared, commit `8221e91`) — 1 Blocker / 9 High / 8 Medium / 3 Low
- `docs/webui-review-personas.md` (Monica, commit `20d92dc`) — 6 personas, 1 served today
**Active source inputs (preserved):**
- `docs/coldfront-feature-mapping.md` (Monica, commit `2a25fd0`) — 40 ColdFront features inventoried; powerhouse thesis; persona model expanded (PI promoted to first-class). Ruled into D24 (positioning) and D25 (customer-pull gate). Drives v1.2 scope expansion and Sprints C.5, D, E.

This plan supersedes any informal webui v1.1 backlog. Every finding from the three source reviews is addressed (mapped to a sprint or explicitly deferred with rationale) in the traceability table at the end. Every ColdFront feature (CF-01 through CF-40) from Monica's mapping is also addressed in the ColdFront traceability table. New cross-cutting principles in Phase 3 are also written into `docs/decisions.md` as D19–D25.

## Standing principles (founder directives, baked in)

These apply to ALL sprints in this plan and any future sprint plan. Do not re-litigate.

1. **Sprints do not stop.** Sprint dispatch is automatic. When a sprint closes (tag shipped, CI green, autodeploy verified), the next sprint begins on schedule without operator approval. Founder explicitly directed: "they should not stop."
2. **No headcount or revenue gating.** Open-source product, no SaaS, no billing. There is no "we'd need to hire" framing. We staff from the agent fleet (Dinesh leads frontend; Gilfoyle owns infra; Richard rules architecture; Monica owns positioning; Jared owns ops). All technical work is in-house and proceeds on technical merits alone. Founder directive: "if we can do it in house with the team we have we do it."
3. **No revenue loss to optimize against.** Cost of churn is engineering time only. This raises our risk tolerance for refactors and framework adoption — we are NOT protecting paying customers from change.
4. **Re-rule routes through Richard, but does not block sprint dispatch.** Founder can override at any time with a written directive (as happened 2026-04-27 for D21).

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

**Rationale:** Adding a JS test framework + CSP refactor is a multi-sprint slog that delivers zero new operator-visible value. The 5 helper functions can be tested with `node:test` (zero deps) inside Sprint B. Full migration sequenced with framework adoption (see D21 re-rule + D23).

---

### C10. Tech debt — module-split `app.js` (E-1) in v1.1, v1.1.1, or v1.2? (RE-RULED 2026-04-27)

**Original ruling (2026-04-27 AM):** Deferred to v1.2 (Sprint C C2 workstream). Module-split between feature waves to avoid merge collision with role-aware nav.

**RE-RULED (2026-04-27 PM, after founder directive on D21):** **Module-split moves to a dedicated Sprint B.5 (v1.1.1) between Sprint B and Sprint C.** Sprint C uses the clean module boundaries to introduce the framework (D23: Alpine + HTMX) on the Researcher portal greenfield surface.

**Rationale (re-rule):** Founder lifted the framework deferral. Two prerequisites now must happen for Sprint C: module-split AND framework introduction. Doing both inside Sprint C blows the v1.2 timeline AND violates the principle of "one big change at a time per surface" (refactoring code AND swapping in a framework on the same files is a recipe for unreviewable PRs). Splitting into B.5 (refactor only, no framework) → C (framework on new modules + greenfield Researcher portal) gives clean reviewable seams. Cost: adds 2 weeks of calendar time before v1.2. Benefit: drastically lower risk on the framework introduction. Net win.

**See:** D21 (re-rule) and D23 (framework choice) in `docs/decisions.md`.

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

### Sprint B — v1.1.0 "Trustworthy Admin UI" **[COMPLETED — 2026-04-27]**

**Tag target:** `v1.1.0` 4-6 weeks after Sprint A ships (target window: 2026-06-01 to 2026-06-15).
**Actual ship:** 2026-04-27 (accelerated — all 30 deliverables landed in a single sprint session).
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

**Explicitly out of scope for v1.1 (rolls to v1.1.1, v1.2, or later):**
- New `viewer` role (v1.2)
- `/portal/` researcher route (v1.2)
- Full module-split of `app.js` (**v1.1.1 — Sprint B.5**)
- Alpine.js / HTMX adoption (**v1.2 — Sprint C, see D23**)
- CSP headers / inline-handler migration (deferred — Alpine 3 CSP build mitigates when needed)
- Vitest harness (we get `node:test` coverage of helpers only)
- LDAP self-service password change (v1.2)
- Per-node verify_timeout override (v1.2)
- DHCP pool config in UI (v1.3+)
- Reporting dashboards for PIs (v2.0+, gated on customer-defined metrics)

**Sprint B framework note (D21 re-rule baked in):** Sprint B explicitly stays vanilla. Audit log UI (B3-3) and Webhooks UI (B3-1/B3-2) ship in vanilla. They will be rewritten to HTMX in Sprint C as the second wave of framework adoption (Researcher portal is the first). Do NOT introduce Alpine or HTMX in Sprint B — that work is Sprint C's responsibility on top of Sprint B.5's clean module boundaries.

---

### Sprint B.5 — v1.1.1 "Framework Adoption Pilot + Module Split" (NEW — added 2026-04-27 per D21 re-rule) **[FRAMEWORK PILOT COMPLETE — 2026-04-27; module split pending]**

**Tag target:** `v1.1.1` 2 weeks after v1.1.0 ships (target window: 2026-06-15 to 2026-06-29).
**Goal:** Two parts: (A) Vendor Alpine.js + HTMX, establish the pattern on a pilot page (DHCP Leases), document conventions for Sprint C. (B) Decompose monolithic `app.js` (9,388 LOC) and `slurm.js` (2,033 LOC) into ES6 module-per-page structure. Pure mechanical refactor. Part A is complete. Part B is pending.

**Part A — Alpine+HTMX adoption pilot — COMPLETED 2026-04-27:**
- Alpine.js 3.15.11 + HTMX 2.0.9 vendored under `internal/server/ui/static/vendor/`
- SHA256 verified against two CDNs; manifest at `VENDOR-CHECKSUMS.txt`
- DHCP Leases page (`#/network/allocations`) migrated to Alpine: `dhcpLeasesComponent()` factory, `x-data`/`x-init`/`x-show`/`x-for`/`x-text`/`:class`/`@click` demonstrated
- Migration target rationale: small read-only list, no mutation surface, low blast radius, representative of the most common pattern
- Pattern playbook: `docs/frontend-patterns.md`
- D23 updated with vendored versions + checksums

**Goal (original):** Decompose monolithic `app.js` (9,388 LOC) and `slurm.js` (2,033 LOC) into ES6 module-per-page structure. Pure mechanical refactor. ZERO new features. This sprint exists solely to give Sprint C clean seams to layer Alpine + HTMX onto.
**Personas served:** None directly. This is a foundation sprint. Persona benefit lands in Sprint C+.
**Owner:** Dinesh (engineering lead, sole author), Gilfoyle (CI verification, autodeploy), Richard (PR review on module-boundary decisions).
**Estimate:** 2 weeks engineering. No external dependencies. No design questions to resolve.
**Dependencies:** Sprint B complete and v1.1.0 tagged.

**Why a dedicated micro-sprint instead of bundling into Sprint C:** D21 re-rule (2026-04-27) opened framework adoption. D23 chose Alpine + HTMX. Doing module-split AND framework adoption in the same sprint produces unreviewable PRs and conflates two unrelated changes (refactor vs. framework introduction). Splitting them into B.5 (refactor) → C (framework on clean modules) costs 2 weeks of calendar time and buys clean review surface + clear rollback boundaries.

**Deliverables:**

| ID | Source | Description |
|---|---|---|
| B5R-1 | Dinesh E-1 | Extract `Pages.deploys`, `Pages.images`, `Pages.nodes`, `Pages.dhcp`, `Pages.audit`, `Pages.webhooks`, `Pages.settings` into `internal/server/ui/static/js/pages/*.js`. Each module exports a single `render(container, route)` function + a `cleanup()` hook. ES6 modules via `<script type="module">`. No build step. |
| B5R-2 | Dinesh E-1 | `app.js` retains: Router, App state, global helpers (`fmtBytes`, `escHtml`, `App.toast`), Auth bootstrap, page-cleanup dispatch. Target: 9,388 → ≤3,500 LOC. |
| B5R-3 | Dinesh A-13, E-4 | Extract `escHtml`, `fmtBytes`, `fmtRelative`, `fmtDate` into `utils.js`. Single source of truth. Add `'` escaping (E-4 fix). |
| B5R-4 | Dinesh F-1 | Expand `node:test` coverage on `utils.js` to ≥80% line coverage. Wire to CI. |
| B5R-5 | new | Each extracted page module gets a `cleanup()` hook that closes any SSE streams, clears any timers. Router calls `cleanup()` on route change. Resolves D-4 (ISO SSE leak) and D-13 (page cleanup hook) from Dinesh's review preemptively. |
| B5R-6 | new | Document module conventions in `internal/server/ui/static/js/README.md`: file naming, export shape, lifecycle hooks, dependency rules (page modules import from `utils.js` and `api.js` only — never from each other; never from `app.js`). |

**Acceptance criteria:**
- `app.js` LOC ≤3,500 (verified by `wc -l`).
- All routes that worked in v1.1.0 still work in v1.1.1 — verified by full manual smoke (Dinesh) on cloner dev host across all 3 RBAC roles.
- No new features. No new endpoints. No new behavior. Bytes-on-the-wire identical for any given API call.
- `node:test` suite covers `utils.js` at ≥80% line coverage.
- CI green on `main` for the v1.1.1 tag commit.
- Cloner autodeploy picks up the new bundle without operator action.
- `internal/server/ui/static/js/README.md` exists and documents the module conventions.

**Out of scope for B.5:**
- Any framework introduction (Alpine, HTMX, Lit, etc.) — Sprint C only.
- Any TypeScript / build step / bundler — explicitly forbidden per D10.
- Any new feature, even small — feature creep here defeats the entire point.
- Any CSP work — sequenced with framework adoption.

**Risk:** Pure refactors are deceptively easy to ship broken. Mitigation: ship in small PRs (one page module per PR), run full smoke after each merge, never bundle two page extractions into one PR.

---

### Sprint C — v1.2.0 "Researcher Portal MVP + Framework Introduction"

**Tag target:** `v1.2.0` 6-8 weeks after v1.1.1 ships (target window: 2026-08-10 to 2026-09-07). Note: 2-week shift later than original plan because Sprint B.5 sits between B and C.
**Goal:** Deliver the researcher portal that creates the institutional-buyer wedge against Bright Computing, AND introduce Alpine.js + HTMX (per D23) on the researcher portal greenfield surface and on selected v1.1 pages (audit log, anomaly card). **Scope expanded 2026-04-27 per ColdFront integration: adds OnDemand portal link (CF-26) and storage quota display (CF-28) — both trivially small adds that ride the researcher portal greenfield work.**
**Personas served:**
- Persona 3 (Researcher) — first time they have any UI surface
- Persona 1 (Sysadmin) — better long-term maintainability via clean modules (B.5) + reactive UI on audit/anomaly surfaces (C)
- Persona 2 (Junior Ops) — quality-of-life fixes from v1.1 P2 backlog
**Owner:** Dinesh (engineering lead), Monica (researcher-portal copy + competitive positioning validation), Jared (LDAP self-service ops doc), Richard (architecture review on framework integration patterns).
**Estimate:** 6-8 weeks across the three workstreams (OnDemand link + storage quota fold-in adds ~1-2 days, absorbed in C1 budget).
**Dependencies:** Sprint B.5 complete and v1.1.1 tagged. Module-split MUST be done before this sprint starts — framework introduction onto a 9,388-LOC monolith is not feasible.

**Workstream C1 — Researcher portal MVP (greenfield Alpine.js proof case per D23)**

The Researcher portal is the FIRST production surface using Alpine. Greenfield = no vanilla code to migrate, no risk of regression, contained blast radius. If Alpine works here, we have proof to backfill into existing pages. If Alpine fails here, we revert to vanilla and revisit D23 — the risk is bounded to one new page.

| ID | Source | Description |
|---|---|---|
| C1-1 | Monica Persona 3 / new | Add `viewer` role to RBAC. Migration `053_add_viewer_role.sql`. Documented in `docs/rbac.md`. |
| C1-2 | Monica Persona 3 / D23 | New route `/portal/` (separate from `/admin/`). Single page built with Alpine.js: cluster status (partition health from Slurm), available images, "My HPC Account" panel. Uses `x-data` for top-level state, `x-show`/`x-if` for conditional panels, `x-on:click` for modal triggers. |
| C1-3 | Monica C7 ruling / new | LDAP self-service password change. New endpoint `POST /api/v1/ldap/me/password` accepting current+new password, callable by `viewer` or above. Alpine modal in /portal/ "My Account" panel — `x-data` tracks form state (current password, new password, confirm, validation errors). |
| C1-4 | Monica Persona 3 / Section D / D23 | Slurm partition status surface: `GET /api/v1/slurm/partitions/status` returns array of `{partition, state, total_nodes, available_nodes}`. Rendered as cards on /portal/ via HTMX `hx-trigger="every 60s"` for live refresh (content negotiation returns HTML partial for HTMX, JSON for API). |
| C1-5 | Monica positioning | Hide /admin/ entirely from `viewer` role logins — they only see /portal/. Hide /portal/ from admin/operator role logins (they see /admin/ only). Login redirect logic dispatches by role. |
| C1-6 | new | Tests: viewer login lands on /portal/, cannot navigate to /admin/, cannot mutate any data, can change own LDAP password. Alpine `x-data` state hydration is testable via DOM inspection in headless browser. |
| C1-7 | CF-26 (Monica) | OnDemand portal link. If `CLUSTR_ONDEMAND_URL` env var is set, render an "Open OnDemand" link/card in the /portal/ "My Account" panel. Single env var read at server start, single Alpine `x-show` on a static link. Zero backend logic. High institutional value for university deployments running OnDemand. |
| C1-8 | CF-28 (Monica) | Storage quota display. New `GET /api/v1/ldap/me/quota` endpoint reads configurable LDAP attributes (`CLUSTR_LDAP_QUOTA_USED_ATTR`, `CLUSTR_LDAP_QUOTA_LIMIT_ATTR`) for the logged-in user. Renders as a quota card on /portal/ "My Account" panel via Alpine. If attributes unmapped, card hidden. No new storage-system integration — purely surfaces what LDAP already exposes. |

**Workstream C2 — Framework introduction (Alpine.js + HTMX, per D23)**

The C2 module-split workstream from the original plan moved to Sprint B.5 (v1.1.1). This C2 workstream is now framework introduction. Module-split is a prerequisite (must be done first in B.5) and is NOT a deliverable here.

| ID | Source | Description |
|---|---|---|
| C2-1 | D23 | Vendor Alpine.js 3 (latest stable) and HTMX 2 (latest stable) into `internal/server/ui/static/vendor/`. Pin exact version in `internal/server/ui/static/vendor/VENDOR.md` (record version, source URL, SHA256, license text). Served via existing `embed.FS`. |
| C2-2 | D23 | Add `<script src="/static/vendor/alpine.min.js" defer></script>` and `<script src="/static/vendor/htmx.min.js" defer></script>` to the layout HTML. Verify pages load with no console errors and existing vanilla code keeps working. |
| C2-3 | D23 | Document framework usage conventions in `internal/server/ui/static/js/README.md` (extending B5R-6): when to reach for Alpine vs HTMX vs vanilla; `x-data` scope rules; `hx-target`/`hx-swap` patterns; how to mix on the same page. |
| C2-4 | D23, B3-3 | Rewrite Audit log page (`pages/audit.js`, originally vanilla in v1.1) using HTMX for filter/paginate. Server adds an HTML-partial response variant to `GET /api/v1/audit` (content negotiation: `Accept: text/html` returns `<tr>...</tr>` rows; `Accept: application/json` unchanged for API consumers). |
| C2-5 | D23, B2-4 | Rewrite Dashboard Anomalies card (originally vanilla in v1.1) using HTMX `hx-trigger="every 30s"` for periodic refresh. Same content-negotiation pattern. |
| C2-6 | D23 | Add a `node:test` smoke test that asserts Alpine and HTMX are loaded on the layout page (catches accidental removal in future PRs). |

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
- OnDemand portal link visible in /portal/ when `CLUSTR_ONDEMAND_URL` is set; hidden otherwise (CF-26).
- Storage quota card visible in /portal/ when LDAP quota attributes mapped; hidden otherwise (CF-28).
- Alpine.js + HTMX vendored under `internal/server/ui/static/vendor/` with pinned versions documented in `VENDOR.md`.
- Audit log page (rewritten in HTMX) loads + filters + paginates without full-page reload.
- Dashboard Anomalies card refreshes every 30s via HTMX without reloading the dashboard.
- Researcher portal `/portal/` is built with Alpine and demonstrates `x-data` reactive state on at least 3 distinct interactive components (account panel, partition status, image catalog).
- All other v1.0/v1.1 pages still work in vanilla — framework adoption is purely additive.
- All 14 P2 findings from engineering review FIXED.
- All 8 Medium findings from ops review FIXED (or explicitly deferred with rationale).
- CI green on the v1.2.0 tag.
- `docs/researcher-portal.md` authored by Monica + Dinesh (positioning + how-to).
- `internal/server/ui/static/js/README.md` updated with Alpine + HTMX usage conventions.

---

### Sprint C.5 — v1.2.5 "PI Governance Scaffolding" **[COMPLETED — 2026-04-28]**

Cross-references: `internal/db/migrations/055–058`, `internal/db/pi.go`, `internal/server/handlers/portal/pi.go`, `internal/server/ui/static/portal_pi.html`, `internal/server/pi_rbac_test.go`. Tag: `v1.2.5` (commit `991a267`).

**Tag target:** `v1.2.5` — shipped 2026-04-28 (1 day after v1.2.0).
**Goal:** Promote the PI persona to first-class. Add the PI role + the PI self-service surface + the NodeGroup utilization view. This sprint operationalizes Monica's Persona 4A (PI/Research Group Lead) and absorbs the structural primitives from ColdFront's CF-08 / CF-09 / CF-02 / CF-14 governance model — but at clustr's physical-resource abstraction level, NOT ColdFront's abstract-quota model (per coldfront-feature-mapping.md Risk 4).
**Why a v1.2.5 micro-sprint instead of folding into v1.2 or v1.3:** v1.2 (Sprint C) is already large (researcher portal + framework adoption). PI governance is a new persona — it deserves a clean release boundary so the ColdFront-aware audience knows when to look. v1.3 should focus on IT Director + notifications (different persona); bundling PI work into v1.3 conflates two persona expansions.
**Personas served:**
- Persona 4A (PI / Research Group Lead) — first-class for the first time, gets a PI portal at `/portal/pi/`
- Persona 1 (Sysadmin) — fewer "add a student to my group" tickets thanks to PI self-service
- Persona 3 (Researcher) — improved "you're in this group" surface in /portal/ as a side-effect
**Owner:** Dinesh (engineering lead), Monica (PI portal copy + member-management UX validation), Jared (LDAP integration ops doc), Richard (RBAC schema review + NodeGroup-as-allocation framing review).
**Estimate:** 3-4 weeks engineering across the three workstreams.
**Dependencies:** Sprint C complete and v1.2.0 tagged. Viewer role + /portal/ scaffolding from C1 is the foundation we extend.

**Workstream C5-1 — PI role + RBAC primitives (CF-09)**

| ID | Source | Description |
|---|---|---|
| C5-1-1 | CF-09 (Monica) | Add `pi` role to RBAC. Migration `054_add_pi_role.sql`. Per D1, role enum extended; `pi` is more privileged than `viewer` but more restricted than `operator`. Documented in `docs/rbac.md`. |
| C5-1-2 | CF-09 (Monica) | NodeGroup ownership: add `pi_user_id` nullable FK on `node_groups` table. Migration `055_node_group_pi.sql`. A NodeGroup can have one PI; one PI can own multiple NodeGroups. Admin assigns PI; PI cannot transfer ownership. |
| C5-1-3 | CF-09 (Monica) | Auth middleware: `pi` role gates `/portal/pi/` and PI-scoped API endpoints. PI can read/write only their owned NodeGroups; cannot touch nodes/images/Slurm/LDAP-admin. |
| C5-1-4 | CF-09 (Monica) | Login redirect dispatch updated: `pi` role lands on `/portal/pi/` (default), can navigate to `/portal/` (researcher view) but NOT `/admin/`. |
| C5-1-5 | new | Tests: PI login can list owned NodeGroups, cannot list non-owned NodeGroups, cannot reach `/admin/`, cannot mutate node config. RBAC audit-log entries written for all PI actions. |

**Workstream C5-2 — PI self-service member management (CF-08)**

| ID | Source | Description |
|---|---|---|
| C5-2-1 | CF-08 (Monica) | New endpoint `GET /api/v1/portal/pi/groups` returns owned NodeGroups with member list (LDAP usernames + display names from LDAP attributes). |
| C5-2-2 | CF-08 (Monica) | New endpoint `POST /api/v1/portal/pi/groups/{id}/members` accepts a username; if `CLUSTR_PI_AUTO_APPROVE=true` (config), creates LDAP account + adds to group immediately; else creates a pending request that admin approves from `/admin/` "Pending PI Requests" panel. |
| C5-2-3 | CF-08 (Monica) | New endpoint `DELETE /api/v1/portal/pi/groups/{id}/members/{username}` removes user from NodeGroup; LDAP account deactivated (not deleted) if no other group memberships remain. |
| C5-2-4 | CF-08 (Monica) | `/portal/pi/` "My Groups" panel built with Alpine: list owned groups, expandable member list per group, "Add member" modal (Alpine `x-data` form state), "Remove member" confirmation modal. Pending-request status displayed if auto-approve disabled. |
| C5-2-5 | CF-08 (Monica) | `/admin/` gets new "Pending PI Requests" surface (admin only) — list of pending member-add requests with PI name, target group, requested user, timestamps, "Approve" / "Deny" actions. Audit-logged. |
| C5-2-6 | new | Tests: PI can add members to owned group, cannot add members to non-owned group, removal triggers expected LDAP state transitions. Auto-approve mode tested separately from admin-approval mode. |

**Workstream C5-3 — NodeGroup utilization view for PIs (CF-02 + CF-14, structural primitive only)**

Per D25, this is the "low cost, low risk" structural primitive — read-only aggregation of existing data, no new metrics schema. Custom metrics defer to v1.3+/v1.4 once a customer specifies them.

| ID | Source | Description |
|---|---|---|
| C5-3-1 | CF-02 (Monica) | New endpoint `GET /api/v1/portal/pi/groups/{id}/utilization` returns aggregated stats from existing tables: `node_count`, `deployed_count`, `undeployed_count`, `last_deploy_at`, `failed_deploys_30d`, `partition_state` (from existing Slurm partition status), `member_count`. NO new schema; pure SQL over `nodes`, `reimages`, `node_groups`, `audit_log`, `slurm_partitions`. |
| C5-3-2 | CF-02 / CF-14 (Monica) | `/portal/pi/groups/{id}` detail view: utilization summary card (Alpine for tab state), member list, partition health card (HTMX `hx-trigger="every 60s"` for live refresh — reuses C1-4 partition status pattern). Read-only, non-technical language ("Your group has 8 nodes; 7 deployed; 1 awaiting reimage"). |
| C5-3-3 | CF-02 (Monica) | "Request more nodes" CTA on group detail view — opens a textarea modal (Alpine), POST to `/api/v1/portal/pi/groups/{id}/expansion-requests`. Stored in new `pi_expansion_requests` table (migration `056`). Admin sees pending requests in `/admin/`. NO automatic node assignment — admin reviews and acts manually. (Lightweight CF-20 allocation-change-request precursor.) |
| C5-3-4 | new | Tests: utilization endpoint returns correct counts for fixtured data, expansion request creates row, admin can list/dismiss requests. |

**Acceptance criteria for Sprint C.5:**
- `pi` role exists in RBAC, login dispatches to `/portal/pi/`, cannot reach `/admin/`.
- PI can view all owned NodeGroups, see member list, add/remove members (per `CLUSTR_PI_AUTO_APPROVE` mode).
- PI cannot interact with non-owned NodeGroups (403 enforced server-side, UI hides).
- Admin sees pending PI member-add requests and PI expansion requests in `/admin/`.
- NodeGroup utilization view loads in <500ms for fixtures with 100 nodes (no rollup tables; pure SQL aggregation).
- All actions audit-logged with PI user_id, action, target group_id, target username (where applicable).
- Researcher view (Persona 3) gets a "Your group is owned by Dr. <PI name>; ask them about access" string when applicable — graceful surfacing of PI role to researchers.
- CI green on v1.2.5 tag. Cloner autodeploy picks up the new bundle.
- `docs/pi-portal.md` authored by Monica + Dinesh (PI persona + how-to + auto-approve vs manual-approve mode).
- `docs/rbac.md` updated to document the 5-role model (admin / operator / pi / viewer / readonly).

**Out of scope for v1.2.5:**
- Grant/publication tracking (v1.3 — CF-12, CF-13)
- Annual project review workflow (v1.4 — CF-11)
- IT Director read-only view (v1.3 — CF-17)
- Email notifications on PI member-add events (v1.3 — bundled with general SMTP work)
- Custom utilization metrics defined by customer (v2.0+ per D25, gated on customer pull)
- Allocation change requests with full approval-history workflow (v1.4 — CF-20 full version)
- Manager delegation (PI promotes member to manager) — defer to v1.3 only if a customer asks; CF-09 mentions it but Monica's v1.2 list does not require it for the wedge

---

### Sprint D — v1.3.0 "IT Director View + Notifications + Grants/Publications" **[COMPLETED — 2026-04-27]**

**Tag target:** `v1.3.0` 5-7 weeks after v1.2.5 ships (target window: 2026-10-12 to 2026-11-23).
**Goal:** Promote the IT Director persona (Persona 5) to first-class with a read-only summary view. Add the SMTP scaffolding that unblocks email notifications. Add structural primitives for grant tracking and publication tracking. This sprint absorbs the bulk of Monica's v1.3-v1.4 follow-on bucket (CF-11, CF-12, CF-13, CF-15, CF-17) — sequenced as the natural next layer after the PI is first-class.
**Personas served:**
- Persona 5 (IT Director) — first surface ever; read-only summary view at `/portal/director/`
- Persona 4A (PI) — gains grant + publication entry surface; gets email when their group changes
- Persona 3 (Researcher) — gets email when their LDAP account is created or NodeGroup membership changes
- Persona 1 (Sysadmin) — gains "broadcast to NodeGroup" admin tool (CF-18 precursor)
**Owner:** Dinesh (engineering lead), Gilfoyle (SMTP infra + deliverability validation), Monica (Director-portal copy + grant/publication form UX), Jared (notification template review + ops doc), Richard (schema review for grant/publication tables).
**Estimate:** 5-7 weeks engineering across the four workstreams.
**Dependencies:** Sprint C.5 complete and v1.2.5 tagged. PI role and `/portal/pi/` scaffolding extended for grants/publications input.

**Workstream D1 — IT Director read-only view (CF-17 + CF-14 reporting primitive)**

| ID | Source | Description |
|---|---|---|
| D1-1 | CF-17 (Monica) | Add `director` role to RBAC. Migration `057_add_director_role.sql`. Read-only across all NodeGroups, all members, all utilization data. Cannot mutate anything. Cannot view node internals (BMC, Slurm config) — only aggregated summaries. |
| D1-2 | CF-17 (Monica) | New `/portal/director/` route. Alpine-built dashboard: total nodes, total deployed, total NodeGroups, total active researchers, deploy success rate (last 30d), per-NodeGroup utilization summary table (paginated). All read from existing tables — NO new metric rollup tables (per D25). |
| D1-3 | CF-17 / CF-14 (Monica) | Per-NodeGroup detail view: PI name, member count, node count, deploy stats, grants list (D3-1), publications list (D3-2). Linked from D1-2 summary table. |
| D1-4 | CF-14 (Monica) | CSV export endpoint `GET /api/v1/portal/director/export.csv?range=quarter` returns flat row-per-NodeGroup with all summary columns. Director can download for institutional reporting. |
| D1-5 | new | Tests: director login lands on `/portal/director/`, cannot mutate, cannot reach `/admin/` or `/portal/pi/`, can view all NodeGroups regardless of PI ownership. CSV export generates valid CSV against fixtures. |

**Workstream D2 — Email notifications (CF-15, SMTP scaffolding)**

| ID | Source | Description |
|---|---|---|
| D2-1 | CF-15 (Monica) | New `internal/notifications/smtp.go` with `Send(to, subject, body)` interface. Config via env: `CLUSTR_SMTP_HOST`, `CLUSTR_SMTP_PORT`, `CLUSTR_SMTP_USER`, `CLUSTR_SMTP_PASS` (encrypted at rest per D4 — extend the encryption migration), `CLUSTR_SMTP_FROM`. Graceful degradation: if SMTP unset, all notification calls are no-ops with INFO-level log. |
| D2-2 | CF-15 (Monica) | Notification templates in `internal/notifications/templates/`: `ldap_account_created.txt`, `nodegroup_membership_added.txt`, `nodegroup_membership_removed.txt`, `pi_request_approved.txt`, `pi_request_denied.txt`. Plain text only (no HTML in v1.3 — keep it simple). |
| D2-3 | CF-15 (Monica) | Notification triggers wired into existing event paths: LDAP module `CreateUser` → send `ldap_account_created`. PI member-add (auto-approve or admin-approve) → send `nodegroup_membership_added`. PI member-remove → send `nodegroup_membership_removed`. |
| D2-4 | CF-15 (Monica) | `/admin/` Settings → Notifications tab: shows SMTP config status (configured/not), template preview, "Send test email" button (admin only). Audit-logged. |
| D2-5 | new | Tests: notification dispatch with mocked SMTP server (testcontainers MailHog or equivalent); template rendering with fixture data; no-op behavior when SMTP unset. |

**Workstream D3 — Grant + Publication tracking (CF-12 + CF-13)**

| ID | Source | Description |
|---|---|---|
| D3-1 | CF-12 (Monica) | Schema: `grants` table (migration `058`) with columns `id, node_group_id (FK), title, funding_agency, grant_number, amount, start_date, end_date, status, created_by_user_id, created_at`. PI can CRUD grants on owned NodeGroups via `/portal/pi/` "Grants" panel. Director and admin can read. |
| D3-2 | CF-13 (Monica) | Schema: `publications` table (migration `059`) with columns `id, node_group_id (FK), doi, title, authors, journal, year, created_by_user_id, created_at`. PI can CRUD publications on owned NodeGroups via `/portal/pi/` "Publications" panel. Director and admin can read. |
| D3-3 | CF-13 (Monica) | DOI auto-fill: new endpoint `GET /api/v1/portal/pi/publications/lookup?doi=<doi>` calls Crossref API (`api.crossref.org/works/<doi>`) to fetch metadata. Server-side outbound (one of the few clustr outbound calls — explicitly opt-in via `CLUSTR_DOI_LOOKUP_ENABLED=true`; off by default to preserve air-gap). Returns title, authors, journal, year for PI to confirm before save. |
| D3-4 | CF-12 / CF-13 (Monica) | `/portal/pi/groups/{id}` detail view extended: "Grants" tab (CRUD via Alpine modals) and "Publications" tab (CRUD + DOI lookup via HTMX). |
| D3-5 | CF-14 (Monica) | Director view (D1-3) renders grants list and publications list per NodeGroup, included in CSV export (D1-4). |
| D3-6 | new | Tests: PI can CRUD grants on owned group, cannot touch non-owned group's grants. DOI lookup works (mocked Crossref response) and gracefully fails when feature disabled or network unavailable. |

**Workstream D4 — Annual review (lightweight CF-11) + admin broadcast (CF-18)**

| ID | Source | Description |
|---|---|---|
| D4-1 | CF-11 (Monica) | Lightweight annual review: admin can trigger a "review cycle" via `POST /api/v1/admin/review-cycles` with a deadline date. All PIs receive an email + see a `/portal/pi/` banner: "Annual review due by <date>; click to affirm your group is active." PI clicks → "Affirm Active" or "Request Archive". Admin sees results in a `/admin/review-cycles/{id}` page. |
| D4-2 | CF-11 (Monica) | Schema: `review_cycles` (id, deadline, created_at) + `review_responses` (cycle_id, node_group_id, status [affirmed/archived/no-response], pi_user_id, responded_at). Migration `060`. |
| D4-3 | CF-18 (Monica) | Admin broadcast: `/admin/nodegroups/{id}` gets a "Send message" button. Modal with subject + body. Sends email to all NodeGroup members via D2-1 SMTP. Audit-logged with subject + recipient count (NOT body — privacy). |
| D4-4 | new | Tests: review cycle creation triggers expected email count; PI affirmation updates response row; admin broadcast hits expected member count. |

**Acceptance criteria for Sprint D:**
- `director` role exists; `/portal/director/` summary view loads with per-NodeGroup utilization for all groups.
- CSV export downloads valid CSV with grants/publications/utilization columns per NodeGroup.
- SMTP configured via env vars; "Send test email" works from `/admin/` Settings → Notifications.
- LDAP account creation, NodeGroup member-add, and PI request approval all trigger expected emails (verified with MailHog in CI).
- PI can create/edit/delete grants and publications on owned NodeGroups; cannot on non-owned.
- DOI lookup works when enabled, gracefully fails (form remains usable) when disabled or offline.
- Admin can trigger an annual review cycle; all PIs receive notification email + banner; affirm/archive responses tracked.
- Admin can broadcast a message to a NodeGroup's members; audit log records the broadcast event.
- CI green on v1.3.0 tag.
- `docs/director-portal.md` authored by Monica + Dinesh.
- `docs/notifications.md` authored by Jared (SMTP setup + template customization).

**Out of scope for v1.3:**
- Custom utilization metrics defined per-customer (v2.0+ per D25)
- Full CF-20 allocation change request workflow with multi-step approval (v1.4)
- XDMoD integration (deferred per D25 — high cost, customer-pull gated)
- HTML email templates (v1.4 polish)
- Email notification preferences per user (v1.4 — opt-in/opt-out)
- Real institutional review workflow with multiple reviewers (v2.0+ — CF-11 full)
- Publication impact metrics / citation counts (v2.0+ — third-party API integration)

---

### Sprint E — v1.4.0 "Allocation Workflow Maturity + Field of Science + Visibility" **[COMPLETED — 2026-04-27]**

**Tag target:** `v1.4.0` 5-6 weeks after v1.3.0 ships (target window: 2026-11-30 to 2027-01-11).
**Goal:** Round out the governance surface with the remaining v1.3-v1.4 follow-ons from Monica's mapping (CF-16 Field of Science, CF-20 allocation change requests, CF-39 attribute visibility, plus polish). This sprint completes the "structural primitives" portion of the ColdFront-inspired roadmap; everything beyond v1.4 either requires customer-defined metrics (D25 gates) or external system integration (XDMoD, FreeIPA per D25 hard defer).
**Personas served:** All five personas; primarily depth on Persona 4A (PI) and Persona 5 (Director).
**Owner:** Dinesh (engineering lead), Monica (FOS taxonomy + visibility flag UX), Jared (allocation request workflow ops doc), Richard (schema + RBAC review).
**Estimate:** 5-6 weeks across four workstreams.
**Dependencies:** Sprint D complete and v1.3.0 tagged.

**Workstream E1 — Allocation change request workflow (CF-20 full)**

| ID | Source | Description |
|---|---|---|
| E1-1 | CF-20 (Monica) | Promote `pi_expansion_requests` (from C5-3-3) to a richer `allocation_requests` table (migration `061`): adds `request_type` enum (expand_nodes / change_partition / change_attributes / decommission), `justification` text, `status` enum (pending / approved / denied / withdrawn), `decided_by_user_id`, `decided_at`, `decision_note`. Backfill existing expansion requests. |
| E1-2 | CF-20 (Monica) | PI flow: `/portal/pi/groups/{id}/requests` shows request history; "New request" modal with type selector + justification field. PI can withdraw their own pending requests. |
| E1-3 | CF-20 (Monica) | Admin flow: `/admin/allocation-requests` paginated list (filter by status, PI, group). Approve/Deny modals capture decision_note. Email to PI on decision (D2 SMTP). Audit-logged. |
| E1-4 | CF-20 (Monica) | Director view (D1) shows pending request count per group. |

**Workstream E2 — Field of Science classification (CF-16)**

| ID | Source | Description |
|---|---|---|
| E2-1 | CF-16 (Monica) | Schema: `fields_of_science` table (migration `062`) seeded with NSF FOS list (admin can extend via `/admin/`). `node_groups.field_of_science_id` nullable FK. |
| E2-2 | CF-16 (Monica) | PI can set FOS on owned NodeGroup via `/portal/pi/groups/{id}` settings. Optional field. |
| E2-3 | CF-16 (Monica) | Director view aggregates utilization by FOS (additional CSV export column + summary card on `/portal/director/`). |
| E2-4 | CF-16 (Monica) | Admin can manage FOS list (add/edit/disable entries) at `/admin/fields-of-science`. |

**Workstream E3 — Per-attribute visibility controls (CF-39)**

| ID | Source | Description |
|---|---|---|
| E3-1 | CF-39 (Monica) | Generalize the existing implicit visibility (BMC creds private, node count public): introduce a per-attribute `visibility` enum (`admin_only`, `pi_visible`, `member_visible`, `public_within_director`). Apply to `node_configs` columns and to grant/publication fields. |
| E3-2 | CF-39 (Monica) | Server-side filter at API layer: queries return only attributes the requesting role + relationship (PI of group, member of group, etc.) is allowed to see. |
| E3-3 | CF-39 (Monica) | Admin UI: `/admin/visibility-policy` lets admin override defaults for any attribute (e.g., "expose grant amount to all members" vs "PI-only"). |

**Workstream E4 — Polish + deferred items pulled forward**

| ID | Source | Description |
|---|---|---|
| E4-1 | CF-15 (deferred from D2) | Per-user notification preferences: each user can opt out of categories (membership changes, account events, review notifications). Stored in `user_notification_prefs` (migration `063`). |
| E4-2 | CF-11 (deferred from D4) | Multi-reviewer annual review workflow: admin can designate review approvers; review cycle results aggregated across approver responses. |
| E4-3 | new | HTML email templates (alongside text) for D2 templates. Plain-text remains the fallback. |
| E4-4 | new | `/admin/audit-log` filter by NodeGroup (cross-cuts the existing actor/action/date filters from B3-3). |
| E4-5 | Jared G-3 (deferred from prior plan) | Hidden SYSTEM section discoverability fix: pin SYSTEM as a top-level expanded section in nav for admin role. |
| E4-6 | Jared D-1 (deferred) | DHCP pool config in UI (read-only display first; mutation requires runtime reconfig + restart story — keep mutation CLI-only for v1.4). |

**Acceptance criteria for Sprint E:**
- PI can submit allocation change requests of all 4 types; admin can approve/deny with notes; PI receives email on decision.
- FOS can be set on NodeGroups; director CSV export includes FOS column; admin can manage the FOS list.
- Per-attribute visibility enforced at API layer; admin can override defaults.
- Per-user notification preferences honored; opt-out users do not receive opted-out categories.
- DHCP pool config visible (read-only) in admin UI.
- CI green on v1.4.0 tag.
- `docs/allocation-requests.md` authored by Jared.

**Delivery notes (2026-04-27):**
- E1 delivered: 5 request types (add_member/remove_member/increase_resources/extend_duration/archive_project), admin Governance tab with pending queue + history. Email on decision. Data migration from pi_expansion_requests.
- E2 delivered: ~130 NSF FOS entries in two-level hierarchy. PI portal FOS dropdown. Director FOS utilization tab with percentage bars. Admin FOS CRUD in Governance tab.
- E3 delivered: project_attribute_visibility + attribute_visibility_defaults tables. D26 defaults seeded (see docs/decisions.md D26). CanSee() helper. PI Visibility tab. Admin global defaults in Governance tab.
- E4 delivered: user_notification_prefs + notification_digest_queue + notification_event_defaults tables. GetMyPrefs/SetMyPref/ResetMyPrefs API (session-auth scoped). Digest queue processor background worker (hourly flush). HTML templates for all 8 events. RawMailer interface for multipart MIME.
- E4-2 (multi-reviewer annual review): deferred to v2.0+ — single-reviewer model from D4 is sufficient for v1.4.
- E4-4 (audit log NodeGroup filter): deferred — not in scope; existing date/actor/action filters sufficient.
- E4-5 (SYSTEM nav pin): deferred — nav structure stable; no operator complaints.
- E4-6 (DHCP pool config read-only UI): deferred to v1.1 backlog (#85) — unchanged.
- docs/allocation-requests.md: authoring delegated to Jared post-sprint.

**Out of scope for v1.4 (rolls to v2.0+ horizon):**
- All items in the Sprint Z (v2.0+) horizon list below.

---

### Sprint Z — v2.0+ Horizon (RE-SEQUENCED 2026-04-27 per D27 — see `docs/decisions.md`)

**What changed (2026-04-27):** Sprint Z was originally framed as one undifferentiated "directional, NOT committed" horizon gated on customer signal. That framing predates the founder's standing rules (continuous sprints, no headcount/revenue gating, default-to-BUILD-on-the-fence). D27 supersedes D25 to make crisp: customer-pull gating now applies ONLY to features where the customer must literally define the contract (custom metrics, third-party integrations into their stack, IdP shape). Everything else gets re-bucketed into "build now" (committed Sprints F/G/H below) or "build on a concrete technical signal." Sprint Z, as a single bucket, is dissolved.

**The 14 themes + 13 CF-Z items are re-bucketed below into four crisp categories.**

#### Bucket 1 — Build now (committed in Sprints F/G/H, v1.5/v1.6/v1.7)

These are cheap structural primitives or obvious wins. They don't need customer specification. They're security/identity/automation primitives that every clustr operator gets value from. Default to BUILD per founder standing rule.

| Item | Origin | Sprint | Tag | Rationale |
|---|---|---|---|---|
| CSP headers + inline-handler removal | Z#11, E-3 | F | v1.5.0 | D23 chose Alpine 3 (CSP-safe build, vendored in B.5). Mechanical migration; security primitive; doesn't need customer pull. |
| SIEM JSONL export endpoint | Z#12, D13-trigger | F | v1.5.0 | Cheap structural — JSONL stream over existing audit log table. Doesn't need a regulated customer to design correctly; the schema is just `audit_events.*` already in DB. |
| Optional allocation expiration field | CF-03 (optional) | F | v1.5.0 | Nullable `expires_at` on NodeGroup + UI surface. Low cost, low risk; institutions that want renewal cycles can use it, those that don't (default) ignore it. |
| OpenLDAP project plugin | Z#3, CF-24 | G | v1.6.0 | We already own LDAP module + per-NodeGroup membership. posixGroup auto-creation is mechanical extension. Useful in non-FreeIPA environments (the common case). |
| Resource access restriction by group | Z#6, CF-40 | G | v1.6.0 | LDAP groups already exist (from CF-24 / existing LDAP plugin). Restricting which groups can request a NodeGroup allocation is contained policy code; doesn't require multi-tenancy to be valuable. |
| Manager delegation (PI-to-manager) | CF-09 (manager) | G | v1.6.0 | PI role exists from C.5. One additional `manager` sub-role + delegated permissions; cheap, well-understood. |
| Auto-compute allocation | Z#4, CF-29 | H | v1.7.0 | PI onboarding workflow shipped in C.5. Auto-NodeGroup creation + partition auto-assignment is structural automation. Single-theme sprint because it touches NodeGroup auto-creation + Slurm partition wiring + PI onboarding integration. |

**Build-now total: 7 items across 3 sprints (F/G/H).**

#### Bucket 2 — Build after technical-pull trigger (gated on concrete technical signal, NOT customer revenue)

These items have a real cost and a real risk profile. We don't need a customer to specify them — but we do need a concrete technical signal that the cost is justified. Each item has an explicit, monitorable trigger and a named decision-maker.

| Item | Origin | Trigger (concrete) | Monitor where | Decision-maker | Tag when triggered |
|---|---|---|---|---|---|
| PostgreSQL migration | Z#7, CF-38 | SQLite write contention >50 ops/sec sustained for 1 hour OR a single deployment exceeds 500 nodes OR multi-tenant requirement triggers (Bucket 2 multi-tenant item) | Existing `clustr_db_busy_total` counter (already emitted by `internal/server/db/`); add Grafana panel to `monitoring/grafana/`. Alert at >50 ops/sec sustained 1h. | Richard (technical) | v2.0.0 (BREAKING — schema migration, see D28) |
| Multi-tenant data isolation | Z#5 | Either: hosted-clustr-as-service decision is taken by founder, OR a single operator runs ≥3 logically-separate node fleets needing strict cross-fleet isolation | Inbound: founder directive; technical: NodeGroup count per server >100 with cross-group access boundaries requested in issues | Founder (product) | v2.0.0 (BREAKING — schema-wide tenant_id) |
| Heavier framework migration (Preact/Vue/Svelte + build step) | Z#10 | Per D21 active trigger #3: a specific feature requires complex form state machines beyond Alpine's reactive ergonomics — concretely, a single page exceeds 800 LOC of Alpine `x-data` state OR triggers >3 architectural workarounds (manual reactivity, custom directive hacks) | Code review during Sprint F+ feature work; Richard reviews all `internal/server/ui/static/pages/*.js` quarterly | Richard (technical) | v2.0.0 (BREAKING — D10 broken: requires npm/build step) |
| Two-tier hot/cold log archive | Z#13, D2-trigger | Operator reports an evicted-log incident (issue or support thread) OR retention env var has been raised >30 days by ≥2 operators (signals demand for longer retention than TTL+row-cap can serve cheaply) | GitHub issues label `audit-log-retention`; Operator survey at v1.5 ship | Richard (technical) | v1.8.0 or later (additive, non-breaking — no major bump) |

**Tech-trigger total: 4 items.** None are scheduled into Sprint F/G/H. When a trigger fires, an unscheduled sprint is dispatched and the v-tag is assigned per D28 (major if breaking, minor if additive).

#### Bucket 3 — Build after customer specification (genuinely needs customer to define the contract)

These items can't be built right without a named customer telling us what their stack/metrics/IdP look like. Building them speculatively is wasted code. Per D27, this is the ONLY bucket where customer-pull gating still applies.

| Item | Origin | What we're waiting for | Cost of waiting |
|---|---|---|---|
| OIDC / SAML federation | Z#1, CF-25 | First institutional operator naming their IdP (Keycloak / Okta / Azure AD / Mokey / Shibboleth). The `userinfo` claim mapping, group-to-role binding, and session-vs-token semantics differ enough across IdPs that speculative build = throwaway code. | Low — local sessions + API keys cover all current operators. The contract surface (auth interface) is already abstracted. |
| FreeIPA HBAC bridge | Z#2, CF-22 | First operator running FreeIPA who wants allocation→HBAC mapping. FreeIPA HBAC rule shape varies wildly by deployment (host groups, sudo rules, kerberos service classes); no universal mapping exists. | Low — operators without FreeIPA aren't blocked; CF-24 (Bucket 1) covers the OpenLDAP path. |
| XDMoD integration | Z#8, CF-27 | First operator with running XDMoD. XDMoD's data model (sub-resource accounting, service-units conversion) is institution-specific. | Low — XDMoD adoption is roughly half the target market; the half without XDMoD don't care. |
| Customer-defined utilization metrics + reporting model | Z#9 | First operator specifying what they want measured (cost-per-job? GPU-hours-by-PI? throughput-by-FOS?). Without this, we'd build the wrong rollups. | Low — read-only aggregation views of existing data already shipped in D + E. The custom-metrics layer is the upgrade. |
| Custom allocation attributes (CF-04) | CF-04 | First operator naming the attribute set they need (license counts, storage quota, special hardware tags). | Low — `field_of_science`, `grant_*`, `publication_*`, `node_count` already cover the institutional defaults. |
| Custom resource attributes (CF-06) | CF-06 | Same as CF-04, scoped to Resource (Node / NodeGroup) not Allocation. | Low — hardware profile already tracks the standard set. |
| Custom attribute types (CF-37) | CF-37 | Type system for custom attributes (string / int / enum / date / bool). Goes hand-in-hand with CF-04 / CF-06. | Low — only matters when CF-04/CF-06 are in scope. |

**Customer-spec total: 7 items.** Not scheduled. When pull arrives, a feature spike is dispatched. None of these are blockers for v2.0.

#### Bucket 4 — Skip / explicit defer to v3.0+

| Item | Origin | Why skipped |
|---|---|---|
| Cloud resource allocation | Z#14, CF-30 | Out of clustr's positioning. clustr is bare-metal-first per D24 powerhouse thesis. If clustr ever expands to hybrid HPC+cloud, this is a v3.0+ scope expansion that earns its own sprint plan. Explicit non-goal for v2.x. |

**Skip total: 1 item.**

---

### Sprint F — v1.5.0 "Security & Audit Hardening" **[COMPLETED — 2026-04-27]**

**Goal:** Lock down the webui's security posture (CSP) and operationalize the audit log for downstream consumers (SIEM JSONL, optional allocation expiration). All deliverables additive; no breaking changes; v1.5.0 minor bump per D28.

**Persona served:** Personas 1 (Sysadmin) and 5 (IT Director) primary; Persona 6 (regulated/institutional) pre-positioned without being gated on it.

**Cadence:** 4-6 weeks per standing cadence.

**Deliverables:**

1. **F1 — CSP headers + inline-handler removal**
   - Set `Content-Security-Policy` headers on every webui response; default policy `default-src 'self'; script-src 'self' 'unsafe-inline-disabled'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'`.
   - Migrate any remaining inline `onclick=` to Alpine `@click=` (per D23) or vanilla `addEventListener` in a page's `init()`.
   - Verify Alpine 3 CSP-safe build (vendored in Sprint B.5) is the loaded variant; swap if not.
   - Add a CSP regression test: `curl -I /` must include the header.

2. **F2 — SIEM JSONL audit-log export endpoint**
   - `GET /api/v1/audit/export?since=<rfc3339>&limit=N` streaming JSONL of audit events (one event per line).
   - Cursor pagination via `next_since` header.
   - Admin-only (existing RBAC). Document in `docs/api.md`.
   - Reverses D13's "no SIEM export in v1.0" by promoting the JSONL contract to a stable surface; D13 re-decision trigger has fired (it's in scope without waiting for a regulated customer per D27).

3. **F3 — Optional allocation expiration**
   - Schema migration: nullable `expires_at TIMESTAMP` on `node_groups`.
   - PI portal: optional "Expires on" field on NodeGroup edit.
   - Director portal: filter "Expiring within 90 days" view.
   - Notifications (uses Sprint D SMTP scaffolding): warn PI at 30/14/7 days before expiry. NO automatic deactivation — display only. (Strict expiration is still SKIP per CF-03; this is the optional field that closes the optional CF-03 case.)

4. **F4 — CSP migration regression test suite**
   - Frontend test that loads each top-level page in a headless browser and asserts no CSP violations in console.
   - Run in CI on every PR.

5. **F5 — Documentation**
   - `docs/security-headers.md` — CSP policy explanation, how to extend for custom integrations.
   - Update `docs/audit.md` (or add if absent) with JSONL schema + example SIEM ingestion config (Splunk + Elastic).
   - `CHANGELOG.md` entry for v1.5.0.

**Target tag:** **v1.5.0** (additive, no schema break, no API break — D28 says minor bump).

**Risk profile:** Medium. CSP migration is mechanical but wide blast radius (touches every page). F4 (regression test) is the safety net.

**Success criteria:**
- Every page passes CSP enforcement with no console violations.
- JSONL endpoint streams 10K events without OOM.
- Optional expiration field round-trips through PI portal without breaking existing NodeGroup CRUD.

---

### Sprint G — v1.6.0 "Identity & Access Primitives" (RELEASED)

**Goal:** Round out identity primitives to make clustr a complete IAM story for non-FreeIPA environments. OpenLDAP project plugin + group-based access restriction + manager delegation. Non-breaking; v1.6.0 minor bump.

**Persona served:** Personas 1 (Sysadmin), 4 (PI), 5 (IT Director).

**Cadence:** 4-6 weeks.

**Deliverables:**

1. **G1 — OpenLDAP project plugin (CF-24)**
   - Auto-create posixGroup in LDAP for each NodeGroup (when LDAP module enabled).
   - Sync NodeGroup membership → posixGroup memberUid on member add/remove.
   - Configurable OU per cluster (default `ou=clustr-projects,$base_dn`).
   - Idempotent on re-sync; never deletes manually-added LDAP members (additive only).

2. **G2 — Resource access restriction by group (CF-40)**
   - Per-NodeGroup field: `allowed_request_groups[]` (LDAP group DNs allowed to request membership).
   - Default `[]` = open (current behavior).
   - PI portal Visibility tab gets a "Who can request access?" picker.

3. **G3 — Manager delegation (CF-09 manager scope)**
   - New role: `manager` (sits between `pi` and `member` in RBAC).
   - PI portal: "Delegate management" tab; PI can grant `manager` role to a member of their NodeGroup.
   - Manager can: add/remove members, view utilization, request expansion. Cannot: delete NodeGroup, change PI ownership, change visibility defaults.
   - Migration to `users.role` CHECK constraint (precedent: 2026-04-28 PI role expansion in `991a267`).

4. **G4 — Documentation**
   - Update `docs/rbac.md` to 6-role model (admin / operator / pi / manager / member / viewer).
   - Update `docs/user-management.md` with OpenLDAP plugin enablement and group-restriction examples.
   - `CHANGELOG.md` entry for v1.6.0.

**Target tag:** **v1.6.0** (additive role + nullable fields; no breaking change to existing RBAC).

**Risk profile:** Low-medium. Role additions have well-trodden migration pattern. OpenLDAP plugin uses existing LDAP module abstractions.

**Success criteria:**
- New NodeGroup creates posixGroup automatically when LDAP enabled; verified in `cloner` lab integration test.
- Manager role can perform delegated actions; cannot escalate beyond delegated scope (RBAC test).
- Group-restriction filter blocks unauthorized request attempts with audit log entry.

---

### Sprint H — v1.7.0 "Allocation Automation" **[COMPLETED — 2026-04-27]**

**Goal:** Auto-compute allocation — NodeGroup auto-creation + Slurm partition auto-assignment when a new PI is onboarded. The structural payoff of the C.5 PI onboarding work. Single-theme sprint because it touches NodeGroup creation + Slurm partition wiring + PI onboarding integration; bundling with G's identity work would mix risk profiles.

**Persona served:** Personas 1 (Sysadmin), 4 (PI), 5 (IT Director). Reduces operator-toil for PI onboarding from 5 manual steps to 1.

**Cadence:** 4-6 weeks.

**Deliverables:**

1. **H1 — Auto-compute allocation policy engine**
   - Configurable policy: when a new PI is onboarded, optionally auto-create a NodeGroup with N nodes from a named hardware profile.
   - Policy fields: `enabled` (default false), `default_node_count`, `default_hardware_profile`, `default_partition_template`, `notify_admins_on_create` (bool).
   - Admin-configurable via Settings → Governance tab.

2. **H2 — Slurm partition auto-assignment**
   - When auto-NodeGroup is created, auto-add a Slurm partition entry to slurm.conf (subject to D22 raw editor pattern; structured form path).
   - Validate via `slurmd -C` per D22 step 3 before commit.
   - Reload Slurm controller (existing `slurmctl reload` path).

3. **H3 — PI onboarding workflow integration**
   - PI onboarding wizard (from C.5) gets a new step: "Auto-allocate compute? [Y/N]" with preview of what would be created.
   - Audit event: `pi_onboarded.auto_allocation` with full policy snapshot.
   - Rollback path: single-button "Undo auto-allocation" available for 24h post-creation.

4. **H4 — Documentation**
   - Update `docs/pi-portal.md` with auto-allocation workflow.
   - Add `docs/auto-allocation.md` covering policy configuration + rollback semantics.
   - `CHANGELOG.md` entry for v1.7.0.

**Target tag:** **v1.7.0** (additive feature; default off; no breaking change).

**Risk profile:** Medium-high. Touches Slurm config write path (regression risk on D22 validation). Single-theme isolation (not bundling with G) is intentional.

**Success criteria:**
- Auto-allocation creates NodeGroup + Slurm partition + sends notifications in single transaction (or rolls back atomically).
- Disabled-by-default verified; existing PI onboarding flow unchanged when policy off.
- 24h undo restores prior state cleanly (NodeGroup deleted, partition removed, audit trail intact).

---

### Sprints I+ — Unscheduled (dispatched on tech-trigger or customer-pull signal)

Per D27, we don't pre-schedule sprints for items that need a trigger. When PostgreSQL contention, multi-tenant demand, framework ceiling, or a customer-pull arrives, an unscheduled sprint is dispatched and the v-tag is assigned per D28:

- **PostgreSQL migration** → **v2.0.0** (BREAKING per D28 — schema migration)
- **Multi-tenant data isolation** → **v2.0.0** (BREAKING per D28 — schema-wide tenant_id)
- **Heavier framework migration** → **v2.0.0** (BREAKING per D28 — D10 violated, build step required)
- **Two-tier hot/cold log archive** → **v1.8.0+** (additive, minor bump)
- **OIDC / SAML federation** → likely **v2.0.0** (BREAKING per D28 — auth contract change) when first IdP is named
- **FreeIPA HBAC bridge** → **v1.x** minor bump (additive plugin) when pulled
- **XDMoD integration** → **v1.x** minor bump (additive plugin) when pulled
- **Custom metrics / custom attributes (CF-04/06/37)** → **v1.x** minor bump (additive) when pulled

**v2.0.0 will be cut when the first BREAKING change in this list lands.** Until then, the sequence is v1.5 → v1.6 → v1.7 → v1.8 → ... per Sprint F/G/H/I/... cadence.

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

### D21 — JS Framework Threshold (RE-RULED 2026-04-27 — see `docs/decisions.md` D21 for full audit trail)

**Principle (re-rule):** Trigger 1 (>5,000 LOC) has fired (actual: 15,248 LOC). Trigger 2 (frontend hire) is invalid per founder directive — we staff in-house from the agent fleet. The ruling is now: **module-split first (Sprint B.5, v1.1.1), framework adoption second (Sprint C, v1.2.0).** Framework choice is locked in D23 below.

**Active triggers going forward (3 + 4 from original; 1 already fired and resolved; 2 invalid):**
1. ~~LOC threshold — FIRED 2026-04-27, resolved by Sprint B.5 + Sprint C.~~
2. ~~Frontend hire — INVALID per founder.~~
3. A specific feature requires complex form state machines beyond Alpine's reactive ergonomics → escalate to Preact or Vue (with build step, accepting D10 hit).
4. CSP enforcement priority → swap vendored Alpine for Alpine 3 CSP-safe build.

**Standing constraints (unchanged from original):** no bundlers, no TypeScript, no JSX, no npm/package.json. D10 stays load-bearing.

**Why the re-rule:** Founder explicitly opened the door ("if we can do it in house with the team we have we do it. this is opensource so right now there is no revenue loss"). Trigger 2 was the implicit gate; with it gone and Trigger 1 long since fired, deferring further was hedging for hedging's sake. The risk-bounded path: module-split first (mechanical, no behavior change), framework on greenfield Researcher portal second (contained blast radius, demonstrably reversible if Alpine doesn't fit).

**Reversibility:** Per-page reversible (rip Alpine out of any single page in an afternoon); codebase-wide costly. Mitigation: incremental adoption, never bulk migrations.

**See `docs/decisions.md` D21 (re-rule) and D23 (framework choice) for full rationale, options considered, and adoption rules.**

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

**Phase 3 summary:** 5 principles ruled (D19–D23). D21 re-ruled same day after founder directive (see audit trail in `docs/decisions.md` D21 and D23). D23 locks Alpine.js + HTMX as the chosen framework, vendored, no build step. D21's original "vanilla until 4 triggers fire" stance was correct as of the morning of 2026-04-27; founder's directive that afternoon ("if we can do it in house with the team we have we do it. this is opensource so right now there is no revenue loss") removed the implicit hire-gate and made deferral hedge-for-hedging's-sake. Re-ruled accordingly. The framework decision is now bound, sequenced, and reversible at the per-page level.

---

## Traceability Table — Every Finding from Every Review

Legend: **A** = Sprint A (v1.0.1), **B** = Sprint B (v1.1.0), **B.5** = Sprint B.5 (v1.1.1, module-split), **C** = Sprint C (v1.2.0, framework + researcher portal), **C.5** = Sprint C.5 (v1.2.5, PI governance), **D** = Sprint D (v1.3.0, director + notifications + grants/pubs), **E** = Sprint E (v1.4.0, allocation workflow + visibility), **F** = Sprint F (v1.5.0, security & audit hardening), **G** = Sprint G (v1.6.0, identity & access primitives), **H** = Sprint H (v1.7.0, allocation automation), **TECH-TRIG** = unscheduled, gated on technical signal (per D27 Bucket 2), **CUST-SPEC** = unscheduled, gated on customer specification (per D27 Bucket 3), **SKIP** = explicit non-goal (per D27 Bucket 4), **DEFER** = old marker, treated as TECH-TRIG or CUST-SPEC per re-bucket below.

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
| A-13 escHtml duplicated | P2 | B.5 | B5R-3 (moved from C2-3 per D21 re-rule) |
| A-14 ISO cancel uses delete | P2 | C | C3-5 |
| B-1 showShellHint dead code | P3 | B.5 | Bundled into B.5 module-split cleanup (was C2) |
| B-2 _updateIsoBuildProgress no-op | P3 | B.5 | Bundled into B.5 cleanup (was C2) |
| B-3 confirm() in slurm.js | P3 | B | Folded into B5 work |
| B-4 sysbage ReferenceError | P1 | A | A-HF-1 (HOTFIX) |
| B-5 webhooks no UI | P1 | B | B3-1, B3-2 |
| B-6 audit no UI | P1 | B | B3-3 |
| B-7 /nodes/connected unused | P3 | B.5 | Add badge to node list during B.5 module-split (was C2) |
| B-8 /repo/health unused | P3 | C | Add to Settings → System tab during C3-26 |
| B-9 Rediscover misleading label | P2 | B | B4-7 |
| C-1 webhook delivery history | P1 | B | B3-2 |
| C-2 image download error | P2 | C | C3-6 |
| C-3 config history pagination | P2 | C | C3-7 |
| C-4 group reimage count preview | P2 | C | C3-8 |
| C-5 DHCP refresh countdown | P3 | DEFER | Cosmetic; reconsider if operators report confusion |
| C-6 global search / Ctrl+K | P3 | DEFER | Real value at 200+ nodes; revisit if first design partner has large fleet |
| C-7 Slurm rollback UI | P2 | C | C3-9 (version column); rollback flow itself defers to Z when backend supports |
| D-1 session expiry countdown | P2 | C | C3-10 |
| D-2 node detail dirty-state nav warn | P2 | C | C3-11 |
| D-3 toast dedup | P3 | C | C3-12 |
| D-4 ISO SSE not closed on nav | P2 | B.5 | B5R-5 (page cleanup hooks resolve this preemptively; was C3-13) |
| D-5 Settings tab loses log state | P3 | C | C3-14 |
| E-1 monolithic app.js | P2 | B.5 | B5R-1, B5R-2 (moved to B.5 per D21 re-rule; was C2-1, C2-2) |
| E-2 template-literal HTML pervasive | P3 | C | Largely resolved by Alpine adoption (D23) on touched pages; vanilla pages stay until they're touched |
| E-3 no CSP | P2 | F | F1 (Sprint F, v1.5.0). Re-bucketed 2026-04-27 per D27: CSP is a build-now security primitive, no longer customer-pull-gated. Alpine 3 CSP-safe build vendored in B.5 makes the migration mechanical. |
| E-4 escHtml missing apostrophe | P3 | B.5 | B5R-3 (was C2-3) |
| E-5 XHR upload bypasses 401 redirect | P3 | B.5 | Folded into B.5 module-split with auth.js extraction (was C2 cleanup) |
| F-1 no JS unit tests | P2 | B+B.5 | B4-8 (helpers via node:test in Sprint B); B5R-4 (expand to ≥80% on utils.js in B.5) |
| F-2 no SSE integration test | P2 | DEFER | Page cleanup hooks (B5R-5) close the leak class; full SSE integration test deferred — revisit if SSE-related operator bug reports surface |
| F-3 no node-editor dirty-flag test | P2 | C | Add when C3-11 lands |
| F-4 dhcp_test response shape | P3 | B.5 | Add during B.5 module-split (was C2; 10-line test) |

**Engineering totals: 8 P1, 14 P2, 11 P3 = 33 findings. A: 2. B: 12. B.5: 8. C: 5. DEFER: 6.** (Reflects D21 re-rule reshuffling code-quality + module-split work into B.5.)

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
| Gap 5: Multi-tenancy / federated user | Defer | Z | Per D6/D15 — gated on customer signal; in Sprint Z horizon |
| Q1: Researcher portal vs operator-scoped nav priority | DEFERRED→ANSWERED | C6 ruling | Operator-scoped nav v1.1, researcher portal v1.2 |
| Q2: LDAP self-service password scope | DEFERRED→ANSWERED | C7 ruling | v1.2 — change own password only, gated on `viewer` role |
| Q3: Reporting data model for PIs/IT directors | DEFERRED→ANSWERED (REVISED 2026-04-27) | C8 ruling + D25 | Structural primitives (PI utilization view, director summary view) ship speculatively in C.5/D per D25. Customer-defined custom metrics still gated to v2.0+. |
| Persona 1 missing scheduled deploys UI | Sysadmin gap | DEFER | `scheduled_at` field exists; surfacing is v1.3 polish. Not blocking institutional pitch. Revisit in E if room. |
| Persona 1 missing searchable 200-node list | Sysadmin gap | DEFER | Same as Dinesh C-6; revisit when first ≥100-node design partner appears. |
| Persona 4 (PI) — group utilization summary | Strategic | C.5 | Promoted to first-class via ColdFront integration (D25 hybrid rule); ships speculatively in C.5 (C5-3 workstream) |
| Persona 5 (IT Director) — quarterly summary | Strategic | D | Read-only summary view + CSV export ships in D (D1 workstream); ColdFront integration promoted ahead of customer-pull gate per D25 |
| Persona 6 (Federated / external) | Strategic defer | Z | Per D1 — OIDC is v2.0+ horizon item, gated on customer signal |

**Persona totals: 5 strategic gaps, 3 deferred questions, 4-6 persona-specific affordances = ~13 findings. B: 4. C: 3. C.5: 1. D: 2. Z: 2. DEFER: 3 (with rationale).** (Updated 2026-04-27 per ColdFront integration: Persona 4 moved C.5; Persona 5 moved D; Q3 partially answered — primitives now build speculatively per D25.)

---

## Summary

- **Total findings across 3 reviews:** ~72 (33 engineering + 26 ops + 13 persona, deduplicating overlap)
- **Total ColdFront features inventoried (Monica `coldfront-feature-mapping.md`):** 40 (32 actionable + 5 not applicable + 3 conditional)
- **Sprint A (v1.0.1 hotfix):** 2 findings (the two truly broken-in-prod items)
- **Sprint B (v1.1.0):** 30 findings (12 engineering + 14 ops + 4 persona) + ColdFront CF-10 prerequisite (role-aware nav)
- **Sprint B.5 (v1.1.1 module-split):** 8 findings (engineering refactor only — pure foundation work for Sprint C framework adoption)
- **Sprint C (v1.2.0 researcher portal + framework):** 19 review findings + 4 ColdFront features (CF-08 partial, CF-10, CF-26, CF-28)
- **Sprint C.5 (v1.2.5 PI governance, NEW per ColdFront integration):** 3 ColdFront features (CF-02 partial, CF-08 full, CF-09)
- **Sprint D (v1.3.0 director view + notifications + grants/pubs, NEW):** 5 ColdFront features (CF-11 lite, CF-12, CF-13, CF-14 partial, CF-15, CF-17, CF-18)
- **Sprint E (v1.4.0 allocation workflow + visibility, NEW):** 4 ColdFront features (CF-16, CF-20, CF-39, plus CF-11 + CF-15 enhancements)
- **Sprint F (v1.5.0, COMMITTED — security & audit hardening):** 5 deliverables (CSP, SIEM JSONL export, optional expiration, regression suite, docs). Re-bucketed from old Sprint Z items 11, 12, and CF-03 optional per D27.
- **Sprint G (v1.6.0, COMMITTED — identity & access primitives):** 4 deliverables (OpenLDAP plugin CF-24, group restriction CF-40, manager delegation CF-09 scope, docs). Re-bucketed from old Sprint Z items 3, 6, and CF-09 manager per D27.
- **Sprint H (v1.7.0, COMMITTED — allocation automation):** 4 deliverables (auto-compute policy CF-29, Slurm partition auto-assignment, PI onboarding integration, docs). Re-bucketed from old Sprint Z item 4 / CF-29 per D27.
- **Unscheduled — TECH-TRIG (D27 Bucket 2, gated on concrete technical signal):** 4 items — PostgreSQL migration, multi-tenant isolation, heavier framework migration, two-tier hot/cold log archive. Each has explicit trigger + monitor + decision-maker documented above.
- **Unscheduled — CUST-SPEC (D27 Bucket 3, gated on customer specification):** 7 items — OIDC/SAML, FreeIPA HBAC, XDMoD, custom utilization metrics, custom allocation attributes (CF-04), custom resource attributes (CF-06), custom attribute types (CF-37).
- **SKIP (D27 Bucket 4, explicit non-goal):** 1 item — Cloud resource allocation (CF-30); plus 5 ColdFront items marked Skip from earlier (CF-31 Keycloak search, CF-32 Starfish, CF-34/CF-35 Django-specific, CF-03 strict mandatory expiration).
- **DEFERRED review findings with explicit rationale:** ~16 review findings (unchanged from prior pass).

Nothing is silently dropped. Sprint Z (the single undifferentiated horizon) is dissolved per D27; every prior Z item is re-bucketed into F/G/H, TECH-TRIG, CUST-SPEC, or SKIP.

---

## ColdFront Traceability Table — every CF-XX from `coldfront-feature-mapping.md`

Source: `docs/coldfront-feature-mapping.md` (Monica, commit `2a25fd0`).

Legend: **A** = v1.0.1, **B** = v1.1.0, **B.5** = v1.1.1, **C** = v1.2.0, **C.5** = v1.2.5, **D** = v1.3.0, **E** = v1.4.0, **F** = v1.5.0, **G** = v1.6.0, **H** = v1.7.0, **TECH-TRIG** = unscheduled per D27 Bucket 2, **CUST-SPEC** = unscheduled per D27 Bucket 3, **SKIP** = explicit non-goal per D27 Bucket 4, **PARTIAL-EXISTING** = clustr already covers this in current code.

### Core Platform (CF-01 through CF-20)

| CF-# | Feature | Sprint | Notes |
|---|---|---|---|
| CF-01 | Project management | C.5 | Implemented as NodeGroup-as-Project (single primitive per coldfront-feature-mapping.md Risk 2) — PI ownership added in C.5 |
| CF-02 | Allocation management | C.5 + E | NodeGroup-as-Allocation in C.5; expansion-request workflow in C.5 (lightweight) → E (full CF-20) |
| CF-03 | Allocation expiration + renewal | SKIP (strict) / F (optional) | Strict mandatory expiration is wrong for bare-metal HPC (per Monica Bucket 5); optional `expires_at` field ships in F (v1.5.0) per F3 — re-bucketed 2026-04-27 per D27 (cheap structural primitive, not customer-spec). |
| CF-04 | Allocation attributes (custom) | CUST-SPEC | Custom attributes literally require customer to define which attributes. Per D27 Bucket 3. |
| CF-05 | Resource management | PARTIAL-EXISTING + Z | clustr Nodes + Images + Hardware Profiles already cover compute resource; storage/license/cloud resource types defer to Z |
| CF-06 | Resource attributes (custom + inherited) | CUST-SPEC | Same as CF-04 — customer must define attribute set. Per D27 Bucket 3. |
| CF-07 | Linked resources (parent-child) | PARTIAL-EXISTING | Nodes link to NodeGroups already; partition resources implicit via Slurm config |
| CF-08 | User management | C + C.5 | Researcher role added in C; PI self-service member management in C.5 |
| CF-09 | PI / Manager delegation | C.5 (PI) / G (manager) | PI role shipped in C.5. Manager-delegation re-bucketed 2026-04-27 per D27 to G (v1.6.0) — cheap structural primitive (G3), no longer customer-pull-gated. |
| CF-10 | Self-service user portal | C | Researcher portal MVP at /portal/ |
| CF-11 | Annual project review workflow | D (lite) + E (multi-reviewer) | Lightweight version in D; multi-reviewer enhancement in E |
| CF-12 | Grant tracking | D | Grants table + PI CRUD + director read |
| CF-13 | Publication tracking + DOI search | D | Publications table + Crossref DOI lookup (opt-in for air-gap deployments) |
| CF-14 | Research output / impact reporting | D | Director view + CSV export — read-only aggregation, no new metric rollups (per D25) |
| CF-15 | Email notifications | D + E | SMTP scaffolding + LDAP/membership/PI templates in D; per-user prefs + HTML templates in E |
| CF-16 | Field of Science classification | E | NSF FOS table + per-NodeGroup tag + director aggregation |
| CF-17 | Read-only center director view | D | `director` role + /portal/director/ |
| CF-18 | System admin messaging to project users | D | Admin broadcast to NodeGroup members (uses D2 SMTP) |
| CF-19 | User status tracking | PARTIAL-EXISTING + E | Active/inactive exists in user table; visibility filtering aligned with E3 visibility-policy work |
| CF-20 | Allocation change requests | C.5 (lite) + E (full) | Lightweight expansion-request in C.5; full multi-type workflow in E |

### Plugin Ecosystem (CF-21 through CF-32)

| CF-# | Plugin | Sprint | Notes |
|---|---|---|---|
| CF-21 | Slurm plugin (sacctmgr) | PARTIAL-EXISTING | clustr's Slurm module installs/configures Slurm; sacctmgr-driven account governance is the ColdFront layer — clustr's NodeGroup-driven model is the equivalent abstraction |
| CF-22 | FreeIPA plugin | CUST-SPEC | FreeIPA HBAC rule shape varies wildly by deployment; needs first FreeIPA-running operator to define mapping. Per D27 Bucket 3. |
| CF-23 | LDAP user search | PARTIAL-EXISTING + C.5 | clustr's LDAP module already has user lookup; C.5 PI member-add uses it for the autocomplete UX |
| CF-24 | OpenLDAP project plugin | G | Re-bucketed 2026-04-27 per D27 to G (v1.6.0) — we own LDAP module; posixGroup auto-creation is mechanical extension; useful in non-FreeIPA environments (the common case). |
| CF-25 | Mokey/OIDC plugin | CUST-SPEC | OIDC claim mapping varies across IdPs (Mokey/Keycloak/Okta/Azure AD); needs first institutional operator naming their IdP. Per D27 Bucket 3. v2.0.0 candidate per D28 (auth contract change). |
| CF-26 | OnDemand plugin | C | OnDemand portal link via env var (C1-7) |
| CF-27 | XDMoD plugin | CUST-SPEC | XDMoD data model is institution-specific; needs first XDMoD-running operator. Per D27 Bucket 3. |
| CF-28 | iQuota plugin | C | Storage quota display via LDAP attribute mapping (C1-8) |
| CF-29 | Auto-compute allocation | H | Re-bucketed 2026-04-27 per D27 to H (v1.7.0). PI onboarding workflow shipped in C.5; auto-NodeGroup + Slurm partition wiring is structural automation, single-theme sprint. |
| CF-30 | OpenStack plugin | SKIP | Out of scope; clustr is bare-metal-first per D24 powerhouse thesis. v3.0+ candidate if clustr ever expands to hybrid HPC+cloud. |
| CF-31 | Keycloak user search | SKIP | Per Monica Bucket 5 — Keycloak not common in clustr target market; OIDC (Z) covers the use case generically |
| CF-32 | Starfish plugin | SKIP | Per Monica Bucket 5 — CCR-specific niche tooling |

### Technical / Architectural (CF-33 through CF-40)

| CF-# | Feature | Sprint | Notes |
|---|---|---|---|
| CF-33 | REST API | PARTIAL-EXISTING | clustr has full REST API at `/api/v1/`; new endpoints land per sprint as needed (PI/director endpoints in C.5 + D) |
| CF-34 | Django admin interface | SKIP | Not applicable — clustr is Go, not Django; webui already serves this function |
| CF-35 | Django signals / event hooks | SKIP | Not applicable — Go module-plugin pattern already implements this |
| CF-36 | Multiple auth backends | PARTIAL-EXISTING + Z | Sessions + API keys today; OIDC/LDAP-bind defer to Z (per D1) |
| CF-37 | Custom attribute types | CUST-SPEC | Type system for customer-defined attributes (string/int/enum/date/bool) — only matters when CF-04/CF-06 are pulled. Per D27 Bucket 3. |
| CF-38 | PostgreSQL data store | TECH-TRIG | Per D6 + D27 — gated on concrete technical signal: SQLite write contention >50 ops/sec sustained 1h OR single deployment >500 nodes OR multi-tenant requirement. v2.0.0 when triggered (BREAKING per D28). |
| CF-39 | Allocation visibility controls | E | Per-attribute visibility policy (E3 workstream) |
| CF-40 | Resource access restriction by group | G | Re-bucketed 2026-04-27 per D27 to G (v1.6.0). LDAP groups exist; restriction policy is contained code; doesn't require multi-tenancy to be valuable. |

### ColdFront Traceability Summary (RE-BUCKETED 2026-04-27 per D27)

| Bucket | Count | Verifiable in sprint |
|---|---|---|
| Already covered by clustr (PARTIAL-EXISTING) | 7 | CF-05, CF-07, CF-19 partial, CF-21, CF-23, CF-33, CF-36 |
| Sprint C (v1.2.0) — ColdFront integration adds | 4 | CF-08 partial, CF-10, CF-26, CF-28 |
| Sprint C.5 (v1.2.5) — PI governance | 4 | CF-01 (via NodeGroup), CF-02 partial, CF-08 full, CF-09 (PI scope) |
| Sprint D (v1.3.0) — director + notifications + grants/pubs | 7 | CF-11 lite, CF-12, CF-13, CF-14, CF-15, CF-17, CF-18 |
| Sprint E (v1.4.0) — allocation workflow + visibility | 4 | CF-16, CF-20, CF-39, plus CF-11/CF-15 enhancements |
| **Sprint F (v1.5.0) — security & audit hardening** | 1 (+CSP/SIEM) | CF-03 optional |
| **Sprint G (v1.6.0) — identity & access primitives** | 3 | CF-09 (manager scope), CF-24, CF-40 |
| **Sprint H (v1.7.0) — allocation automation** | 1 | CF-29 |
| **TECH-TRIG (unscheduled, technical signal — D27 Bucket 2)** | 1 | CF-38 |
| **CUST-SPEC (unscheduled, customer specification — D27 Bucket 3)** | 5 | CF-04, CF-06, CF-22, CF-25, CF-27, CF-37 |
| Skip with rationale (D27 Bucket 4) | 5 | CF-30 (cloud, v3.0+), CF-31, CF-32, CF-34, CF-35 |
| **Total** | **40** | (Some CF-#s span multiple buckets; counts above may sum >40) |

---

## Closing — Top 3 "If We Only Do These In 90 Days, The Product Is Materially Better"

**1. Ship Sprint A (v1.0.1 hotfix) within 5 business days.** Fixing the System Accounts ReferenceError and the Auth role-default-to-admin bug protects the Show HN demo, protects the first design partner installs, and resets the institutional credibility baseline. These are 1-day fixes that compound for every day they're not shipped. Everything else flows from "v1.0 is genuinely safe."

**2. Role-aware nav (Sprint B Workstream B1).** This is the single change that converts clustr's RBAC story from "trust us, it's enforced server-side" to "log in as an operator and see the difference." It is frontend-only, it requires no schema changes, it costs ~1 week of engineering, and it is the foundation that every subsequent multi-persona feature builds on. Without it, the v1.2 researcher portal is just "another page"; with it, the researcher portal is the visible payoff of a coherent multi-persona architecture.

**3. The "Configure and Deploy" inline shortcut + Anomalies dashboard card (Sprint B Workstreams B2-1 + B2-4).** Together, these two items collapse the new-admin onboarding cliff (the 7-step PXE-to-deployed flow becomes 3 steps) AND give every returning admin a "what's broken" answer at first glance. Jared correctly identified onboarding pain as the #1 admin friction; these two changes hit both ends of that lifecycle (Day 0 and Day 30+) with a combined ~1-week build cost. They turn the dashboard from a "dashboard-shaped object" into a tool the operator actually opens for information, not just navigation.

Everything else in the plan matters. These three are the ones that, if the next 90 days went sideways and we only got these out, would still leave clustr materially better positioned for institutional adoption than v1.0 is today.

**The longer arc (updated 2026-04-27 per Sprint Z re-sequencing, D27, D28):** Sprints A → B → B.5 → C → C.5 → D → E are RELEASED (v1.0.1 → v1.4.0). The next three sprints are COMMITTED: F (v1.5.0 security & audit hardening) → G (v1.6.0 identity & access primitives) → H (v1.7.0 allocation automation). All three are additive; no breaking changes; no schema migrations; no major-version bump. Per D27, the old "Sprint Z" undifferentiated horizon is dissolved — its items are re-bucketed into committed F/G/H, technical-trigger-gated (Bucket 2), customer-spec-gated (Bucket 3), or skip (Bucket 4). Per D28, the v2.0.0 boundary is reserved for the first BREAKING change (most likely PostgreSQL migration, multi-tenant schema, OIDC contract change, or framework migration with build step). Until a breaking change is triggered, the sequence stays in v1.x. Sprints do not stop; dispatch is automatic per founder directive.

---

*End of plan. Sprints A, B, B.5, C, C.5, D, E, F, G, H are RELEASED (v1.0.1 → v1.7.0). Items in D27 Buckets 2/3 are unscheduled with explicit triggers. Bucket 4 is explicit skip. Sprints do not stop — dispatch is automatic per founder directive. Re-decision routes through Richard but does not block dispatch.*
