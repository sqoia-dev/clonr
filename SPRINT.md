# clustr Webapp v2 — Sprint Plan

**Single source of truth.** This is the only sprint doc. Update task checkboxes inline as work lands. Do not create additional planning docs.

**Sprint owner:** Dinesh
**Technical authority:** Richard (answers questions on demand — never a blocker)
**PM:** founder + chief-of-staff
**Started:** 2026-04-29
**Target:** walking skeleton merged in 7 days; escalate at day 10 if slipping

---

## Why we're doing this

The current webapp is confusing and overwhelming. We are deleting it entirely — code and docs — and rebuilding from a clear mind on modern 2026 UI/UX foundations. Audience: HPC operators. Goal: they open the app and immediately see what their cluster is doing.

---

## Stack (decided — do not relitigate)

| Concern | Choice |
|---|---|
| Build | Vite 6 |
| Framework | React 19 (with compiler) |
| Language | TypeScript |
| Router | TanStack Router |
| Data | TanStack Query + native EventSource/WebSocket |
| Styling | Tailwind v4 |
| Components | shadcn/ui (Radix primitives, copy-in) |
| Forms | TanStack Form + Zod |
| Icons | Lucide |
| Serving | Go `embed.FS` from `clustr-serverd`, single binary |
| Dev | Vite at :5173 with CORS to server :8080 |

No SSR. No Next.js. No Redux/Zustand. No GraphQL/tRPC. No auth provider. No telemetry. No charts in v1.

---

## Information architecture (4 surfaces — that's it)

1. **Nodes** — registered nodes, status, role, last heartbeat, reimage entrypoint.
2. **Images** — base images + bundles, version, SHA256, "what nodes use this."
3. **Activity** — unified live event stream (provisioning, API calls, errors).
4. **Settings** — API keys, server config, GPG keys.

`/` redirects to `/nodes`. No standalone Dashboard. Bundles live as a tab inside Images.

---

## UI/UX principles (apply to every surface)

1. Cmd-K command palette is primary nav. Sidebar is secondary.
2. Real-time over polling. SSE for state and activity.
3. Selection / filters / sort live in the URL. No modal-trapped state.
4. Inline destructive confirmation with diff + typed entity ID. No "are you sure" modals.
5. Progressive disclosure. Default = 5 columns. Advanced toggle = SHA256, GPG, repo stanzas.
6. Status = color + shape + text. Never color alone.
7. Empty states teach (paste-ready CLI snippet).
8. Latency budget visible (top progress bar).

---

## Design system foundations

- Typography: Inter Variable (UI), JetBrains Mono (IDs/SHAs/configs). Two sizes in v1: 14px body, 12px meta.
- Color: dark mode default, light supported, OKLCH. Neutral grays + one accent (cyan) + semantic green/amber/red.
- Spacing: Tailwind default 4px scale. Don't customize.
- Layout: collapsible sidebar, top bar with Cmd-K trigger + connection indicator, content area. No nested layouts.

---

## Sprint 1 — Walking Skeleton

### In scope

- [x] **WIPE-1** Delete `internal/server/ui/` entirely
- [x] **WIPE-2** Update `internal/server/server.go` — remove UI imports/routes, leave a placeholder `/` 200 until new app is wired
- [x] **WIPE-3** Update `internal/server/tech_trig_worker.go` — remove JS LOC scanning
- [x] **WIPE-4** Delete `test/js/`, `lighthouse-budget.json`, `.lighthouserc.json` (if present)
- [x] **WIPE-5** Update `.github/workflows/ci.yml` — remove Node test/a11y/Lighthouse jobs
- [x] **WIPE-6** Update `Makefile` — remove `test-js`, `a11y`, `smoke` targets
- [x] **WIPE-7** Delete `docs/` entirely and root `CHANGELOG.md`. Delete `README.md`. (SPRINT.md stays.)
- [x] **WIPE-8** Confirm `go build ./...` is green and `clustr-serverd` starts
- [x] **SCAFFOLD-1** Create `web/` directory with Vite + React 19 + TS scaffold
- [x] **SCAFFOLD-2** Tailwind v4 wired, dark mode default, light mode toggle
- [x] **SCAFFOLD-3** TanStack Router + Query installed, root route + `/nodes` route
- [x] **SCAFFOLD-4** shadcn/ui initialized, copy in: Button, Input, Table, Sheet, Command, Toast, Tabs
- [x] **SCAFFOLD-5** Inter Variable + JetBrains Mono loaded
- [x] **EMBED-1** Go `embed.FS` serving built `web/dist/` with SPA fallback to `index.html`
- [x] **EMBED-2** Build pipeline: `make web` builds Vite, `go build` embeds it
- [x] **AUTH-1** Login screen (paste API key, validate, store in `localStorage`)
- [x] **AUTH-2** Auth context + protected routes (redirect unauthenticated to `/login`)
- [x] **SHELL-1** App shell: collapsible sidebar (Nodes, Images, Activity, Settings — last three disabled)
- [x] **SHELL-2** Top bar with Cmd-K trigger (palette opens, lists routes, no actions yet)
- [x] **SHELL-3** Top bar connection indicator (SSE connected/disconnected/reconnecting)
- [x] **NODES-1** Nodes list — fetch from real API, 5-column default view, advanced toggle reveals SHA256/role-detail
- [x] **NODES-2** SSE subscription for live node state updates (no polling)
- [x] **NODES-3** Row click opens detail Sheet — full node info, "reimage" button stub (logs intent, no action yet)
- [x] **NODES-4** Empty state with paste-ready `clustr-cli register` snippet
- [x] **NODES-5** URL-driven filters + sort (search params, copy-paste-able)
- [x] **DEPLOY-1** Verify autodeploy on `cloner` (192.168.1.151) builds and serves the new app
- [x] **DEPLOY-2** Minimal new `README.md` — what clustr is, link to live UI, build instructions
- [x] **CI-1** New CI: lint TS, build web, build Go, smoke-run binary. Green on main.

### Out of scope (Sprint 2+)

- Images surface, Activity surface, Settings surface (the routes exist, the screens don't)
- Cmd-K *actions* (palette opens but only navigates routes)
- Reimage flow execution (button only)
- Forms beyond login
- Toast system beyond basic notifications
- Tests beyond CI smoke
- Storybook, Figma library, design tokens system
- i18n
- Charts / dashboards / metrics views
- Sentry / analytics / telemetry

### Definition of done

1. All checkboxes above are checked.
2. CI is green on the merge commit.
3. Autodeploy on `cloner` ships the new app and `http://10.99.0.1:8080/` loads it.
4. An operator can log in with an API key, see real nodes from the live server, watch a node status update in real time, and click a node to see its details.
5. No leftover references to the old webapp anywhere in the repo.

---

## Working notes

- Richard owns architecture decisions. Ping him for any ambiguity.
- Dinesh executes. Commit early and often per CI/CD watch rules.
- All commits authored as `NessieCanCode <robert.romero@sqoia.dev>`. No Co-Authored-By.
- After every push, watch the CI run; fix red before reporting done.
- Local Go builds are forbidden on this host (OOM). Push to main, let autodeploy + CI build.

---

## Sprint 2 — Complete the App

**Started:** 2026-04-29 (immediately after Sprint 1)
**Target:** 7–10 days. Escalate at day 12.

### Goal

Sprint 1 shipped the shell + Nodes. Sprint 2 makes the app **useful and complete**: the other three surfaces become real, the operator can act on nodes (not just observe), and Cmd-K stops being decorative.

### Re-use the Nodes pattern

Every new surface follows the same structure already proven on `/nodes`:
- TanStack Query + SSE for data
- 5-column default view + advanced toggle
- Detail `<Sheet>` on row click
- Empty state with paste-ready CLI snippet
- URL-driven filters + sort
- Status = color + shape + text

Don't re-litigate the pattern. If something doesn't fit, ping Richard.

### In scope

#### Phase 0 — Username + password login (do this first)

Sprint 1 shipped with API-key-paste login. That's wrong UX for an end-user web app. Web defaults to username/password; API keys are CLI-only. The server already has the full machinery — sessions, login/logout/me/set-password handlers, middleware that accepts both cookie and API key, `bootstrap-admin` CLI. Phase 0 is one tiny server endpoint + a web re-skin.

**Server (one addition):**

- [x] **AUTH0-1** Add `GET /api/v1/auth/status` → `{has_admin: bool}`, public (no auth). ~20 LOC + test. Used by web first-run gate.

**Web (re-skin Sprint 1 auth):**

- [x] **AUTH0-2** Delete the API-key-paste login screen + `localStorage` token store from Sprint 1
- [x] **AUTH0-3** New `<Login/>` view: username + password form, `POST /api/v1/auth/login` with `credentials: "include"`. Show server error messages verbatim (don't reinvent password rules client-side).
- [x] **AUTH0-4** Global fetch wrapper: every request includes `credentials: "include"`; on 401 → flip session state to `unauthed` and re-mount `<Login/>`. No `X-Api-Key`, no `Authorization` headers from the web UI.
- [x] **AUTH0-5** `useSession()` hook: calls `GET /api/v1/auth/me` on mount; states = `loading | authed(user) | unauthed | setup_required`. App shell consults this before rendering protected content.
- [x] **AUTH0-6** First-run gate: before `<Login/>`, fetch `/api/v1/auth/status`. If `!has_admin`, render a "Setup required" page with paste-ready `clustr-serverd bootstrap-admin` snippet (host-local CLI only — never expose bootstrap over the web).
- [x] **AUTH0-7** `<SetPassword/>` view, shown when login response has `force_password_change: true` (or the `clustr_force_password_change` cookie is set). Posts to `POST /api/v1/auth/set-password`.
- [x] **AUTH0-8** Logout: `POST /api/v1/auth/logout` → flip session state to `unauthed`. Logout button lives in Settings (per SET-5) AND in the topbar user menu.
- [x] **AUTH0-9** Vite dev proxy to clustr-serverd so cookies work cross-origin in dev (don't loosen `SameSite=Strict`).

**Auth anti-patterns (do not do any of these):**

- No password / username / session in `localStorage` or `sessionStorage`.
- No JWTs, no refresh-token rotation. The server's HMAC session with sliding expiry is correct as-is — don't touch.
- No client-side password complexity rules. Mirror server errors only.
- No web-callable admin bootstrap. CLI-only is the security boundary.
- No mixed auth from the web UI. Cookie only.
- Don't weaken `SameSite=Strict` to `Lax` in dev. Fix dev with a Vite proxy.

**API keys remain valid for CLI/programmatic access.** The server middleware already accepts both. Settings → API Keys (SET-2) is for the logged-in user to manage their keys, not for web login.

**Auth migration is the foundation for every other Sprint 2 phase.** Do not start Images/Activity/Settings/Reimage/Cmd-K until Phase 0 is merged and CI is green. Otherwise every TanStack Query call in those phases gets refactored when auth changes.

#### Images surface

- [x] **IMG-1** `/images` route — list base images + bundles. Default cols: name, version, size, SHA256-short, "in use by" count
- [x] **IMG-2** Tabs inside Images: "Base Images" and "Bundles" (per IA — bundles live here, not top-level)
- [x] **IMG-3** SSE updates when an image is uploaded / deleted / referenced. No polling.
- [x] **IMG-4** Detail Sheet on row click — full SHA256, GPG fingerprint, size, repo stanzas, list of nodes currently using it
- [x] **IMG-5** Empty state with `clustr-cli image upload` snippet (read real CLI syntax from source)
- [x] **IMG-6** URL-driven search + sort

#### Activity surface

- [x] **ACT-1** `/activity` route — unified live event stream. Replaces the legacy 3-separate-log views.
- [x] **ACT-2** Source events from server's existing audit/event endpoint (read server source; if no unified endpoint exists, ping Richard — adding one is in-scope if needed)
- [x] **ACT-3** Default cols: timestamp (relative), kind (provisioning / api / error), subject (node id / image id / api key id), summary
- [x] **ACT-4** SSE live append. Auto-scroll lock when user scrolls up (don't fight the user).
- [x] **ACT-5** Filter bar: kind + subject. URL-driven.
- [x] **ACT-6** Click row → detail Sheet with full payload (JSON in mono font, expandable)
- [x] **ACT-7** Empty state: "No activity yet. Trigger a node provisioning or upload an image."

#### Settings surface

- [x] **SET-1** `/settings` route, sectioned (not tabs — single-page sections per IA principle: "Settings: One page, sectioned")
- [x] **SET-2** Section: API Keys — list, create (modal-free; inline form), revoke (inline destructive confirm with typed key label)
- [x] **SET-3** Section: Server Config — read-only view of current config (server hostname, network, ports, version)
- [x] **SET-4** Section: GPG Keys — list installed keys, fingerprints, add new (paste public key block)
- [x] **SET-5** Logout button at bottom of Settings (clears `localStorage`, redirects to `/login`)

#### Reimage flow (the killer action)

- [x] **REIMG-1** Replace the Sprint 1 stub button on Node detail Sheet with the real flow
- [x] **REIMG-2** Inline destructive confirmation per UI/UX principle 4: expands inline below the button, shows current image → target image diff (visually distinct), requires typing the node ID
- [x] **REIMG-3** Target image selector: dropdown of available base images, pre-filtered to compatible ones
- [x] **REIMG-4** On confirm: POST to existing reimage endpoint, optimistic update node status to "provisioning", subscribe to SSE for that node's progress
- [x] **REIMG-5** Inline progress bar in the detail sheet (uses the same SSE stream as Nodes-2)
- [x] **REIMG-6** Toast on completion / failure with link to Activity entry

#### Cmd-K actions

- [x] **PAL-1** Palette now lists actions in addition to routes. Sections: Navigation, Nodes, Images, API Keys
- [x] **PAL-2** Action: "Reimage node…" — opens a node picker, then triggers the same inline reimage flow on the Node detail page
- [x] **PAL-3** Action: "Create API key…" — inline form same as Settings → API Keys
- [x] **PAL-4** Action: "Upload image…" — links out to CLI doc (no UI upload in v2)
- [x] **PAL-5** Recent items: last 5 entities visited (nodes, images), persisted in localStorage

#### Tests + polish

- [x] **TEST-1** Vitest configured. Critical hooks tested: `useAuth`, `useSSE`, query key factories
- [x] **TEST-2** Add `pnpm test` to CI before `make web`
- [x] **POL-1** Loading skeletons on every list (no spinner-on-empty)
- [x] **POL-2** Keyboard shortcuts: `g n` → Nodes, `g i` → Images, `g a` → Activity, `g s` → Settings (vim-style leader, no conflict with Cmd-K)
- [x] **POL-3** Update SPRINT.md checkboxes inline as items complete

### Out of scope (Sprint 3+)

- Image upload through the UI (CLI only — link to docs)
- GPG key generation in-app (paste only)
- Multi-tenancy / orgs / users (clustr is single-tenant API-key-only — won't change)
- Reimage scheduling / batch reimage across many nodes
- Charts / metrics / cluster utilization views
- Dark/light system-preference auto-detection (manual toggle is fine for now)
- Mobile layouts (operator audience is desktop-first; not a priority)

### Definition of done

1. All Sprint 2 checkboxes ticked.
2. CI green on the merge commit (lint, vitest, build web, build go, smoke).
3. Autodeploy on `cloner` ships and `http://10.99.0.1:8080/` shows the new screens live.
4. An operator can: log in, see all 4 surfaces with real data, trigger a reimage from either the Node detail OR Cmd-K, watch it progress in real time, see the resulting Activity entry, create + revoke an API key from Settings, and find any entity by Cmd-K.
5. No regressions on Sprint 1 functionality (Nodes list, SSE, login still work).

---

## Sprint 3 — Harden + Close Carry-overs

**Started:** 2026-04-29 (immediately after Sprint 2)
**Target:** 7–10 days. Escalate at day 12.

### Goal

Sprint 1 made the app load. Sprint 2 made it useful. Sprint 3 makes it **trustworthy**: every gap closes, every poll becomes real-time, every known v1.0 limitation gets resolved, and the failure modes stop surprising operators.

No new top-level surfaces. No new big features. This is finishing work + the v1.0 KL items.

### In scope

#### Phase 0 — Default admin credentials (do this first)

The first-run UX should be: run one command, open the web UI, type a memorable default, change the password. Match Grafana/Jenkins/Portainer/MinIO conventions instead of forcing the operator to invent + remember credentials before they've even seen the app.

- [x] **DEF-1** `clustr-serverd bootstrap-admin` with no flags creates user `clustr` with password `clustr` and `force_password_change=true`. Existing `--username` / `--password` flags continue to override — operator control intact.
- [x] **DEF-2** `bootstrap-admin` reject conditions: if the literal default password equals the username (`clustr`/`clustr`), `force_password_change` MUST be set. Add a unit test that fails if a future change ever lets the default through unflagged.
- [x] **DEF-3** Server `/api/v1/auth/set-password` rejects setting the password back to `clustr` when transitioning out of force-change. Test it.
- [x] **DEF-4** Web Setup page (AUTH0-6 from Sprint 2) surfaces a copyable code block: `clustr-serverd bootstrap-admin` and a one-line hint immediately below: *"Default credentials: `clustr` / `clustr` — you'll be prompted to change on first login."*
- [x] **DEF-5** Web Login page shows the default-creds hint as small muted text below the form ONLY when `/auth/status` returned `has_admin: true` AND the operator has not yet successfully logged in *and* the URL has `?firstrun` (set by the redirect from Setup → Login after bootstrap). Don't permanently advertise default creds in the live UI — they should disappear after first login.
- [x] **DEF-6** README Quick Start: document the default creds in a fenced block, with the force-password-change note.

- [x] **SSE-1** Add server SSE channel for image lifecycle events (upload, delete, ref-count changes). Mirror the shape of the existing node SSE channel. Replaces the 15s polling in IMG-3.
- [x] **SSE-2** Wire `/images` to consume the new channel; remove the `refetchInterval`. No regressions.

#### GPG keys — real surface

- [x] **GPG-1** Server: `GET /api/v1/gpg-keys` (list installed keyring entries with fingerprints, owner, trust, created). Read from the same source the bundle deploy uses.
- [x] **GPG-2** Server: `POST /api/v1/gpg-keys` accepts ASCII-armored public key block, validates, imports, returns the new entry. `DELETE /api/v1/gpg-keys/{fingerprint}` removes.
- [x] **GPG-3** Web: replace the Settings → GPG CLI note with a real list + paste-to-add inline form + inline destructive remove (typed fingerprint to confirm, per UI/UX principle 4).

#### Cmd-K reimage picker

- [ ] **PAL-2-2** "Reimage node…" in the palette opens an inline node picker (search, recent, paginated). Selecting a node opens that node's detail Sheet with the reimage panel already expanded. No more redirect to `/nodes`.

#### v1.0 known-limitations cleanup

- [ ] **KL-1** Auto-assign the dual `["controller","worker"]` role on the controller after slurm bundle deploy completes. Eliminates the post-provision API call. Add a unit test.
- [ ] **KL-2** Replace D18 reseed endpoint's generic slurm.conf stub with a cluster-specific config generator that reads the deployed node inventory + roles. Existing cluster topology should round-trip through reseed without operator intervention.
- [ ] **KL-3** Remove `CgroupAutomount=` from generated slurm.conf (deprecated parameter; warning visible in slurmd logs). Confirm no behavior regression with the existing e2e tests.

#### Failure-mode polish

- [ ] **POL-4** Global error boundary at the route level — catches render errors, shows a recovery card with "Reload" + the last 5 user actions (no PII / no payload data). Don't show stack traces unless `?debug=1`.
- [ ] **POL-5** Optimistic update rollback on every mutation that does one. Reimage already does this; audit Settings → API key create/revoke and GPG add/delete and add the same pattern.
- [ ] **POL-6** Network failure UX — when SSE disconnects and can't reconnect for >30s, the topbar connection indicator turns red and a one-line banner appears: "Live updates paused. Click to retry." (No spinner-on-failure; failure is its own state.)
- [ ] **POL-7** Empty / loading / error states are explicitly rendered for every list. No accidental "blank screen looks like empty list" cases.

#### Accessibility — minimum bar

- [ ] **A11Y-1** All interactive elements keyboard-reachable; no focus traps; skip-to-main link in shell.
- [ ] **A11Y-2** Color contrast on dark + light themes meets WCAG AA. Audit the OKLCH palette; bump where needed.
- [ ] **A11Y-3** Status indicators have text or aria-label, not color-only (already a UI/UX principle, audit & fix any gaps).
- [ ] **A11Y-4** Tables have proper `<th scope>` + caption.

#### Tests

- [ ] **TEST-3** Vitest: tests for the new fetch wrapper 401 handling + the SSE reconnect logic.
- [ ] **TEST-4** Vitest: tests for the reimage flow's confirm-gate (typed ID match) and rollback on POST failure.
- [ ] **TEST-5** Server: Go tests for the new GPG endpoints + the SSE image channel.

### Out of scope (Sprint 4+)

- Mobile layouts.
- Image upload through the UI (CLI remains the path).
- Multi-tenancy / orgs / users beyond local users.
- Charts / metrics / cluster utilization views.
- Batch reimage across many nodes (note for Sprint 4 if it surfaces).
- SSO / OAuth / MFA / password reset flows.

### Definition of done

1. All Sprint 3 checkboxes ticked.
2. CI green on the merge commit (lint, vitest, go test, build, smoke, gosec, govulncheck, trivy).
3. Autodeploy on cloner ships the latest SHA; no SSE polling fallback in `/images`.
4. Operator can: log in, manage GPG keys from Settings, trigger reimage entirely from Cmd-K (no /nodes detour), see Activity update in real time across nodes + images, get a useful error UI when SSE drops.
5. v1.0 KL-1 / KL-2 / KL-3 closed and documented in a one-line note per fix in the commit message.
