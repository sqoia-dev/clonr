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

- [x] **PAL-2-2** "Reimage node…" in the palette opens an inline node picker (search, recent, paginated). Selecting a node opens that node's detail Sheet with the reimage panel already expanded. No more redirect to `/nodes`.

#### v1.0 known-limitations cleanup

- [x] **KL-1** Auto-assign the dual `["controller","worker"]` role on the controller after slurm bundle deploy completes. Eliminates the post-provision API call. Add a unit test.
- [x] **KL-2** Replace D18 reseed endpoint's generic slurm.conf stub with a cluster-specific config generator that reads the deployed node inventory + roles. Existing cluster topology should round-trip through reseed without operator intervention.
- [x] **KL-3** Remove `CgroupAutomount=` from generated slurm.conf (deprecated parameter; warning visible in slurmd logs). Confirm no behavior regression with the existing e2e tests.

#### Failure-mode polish

- [x] **POL-4** Global error boundary at the route level — catches render errors, shows a recovery card with "Reload" + the last 5 user actions (no PII / no payload data). Don't show stack traces unless `?debug=1`.
- [x] **POL-5** Optimistic update rollback on every mutation that does one. Reimage already does this; audit Settings → API key create/revoke and GPG add/delete and add the same pattern.
- [x] **POL-6** Network failure UX — when SSE disconnects and can't reconnect for >30s, the topbar connection indicator turns red and a one-line banner appears: "Live updates paused. Click to retry." (No spinner-on-failure; failure is its own state.)
- [x] **POL-7** Empty / loading / error states are explicitly rendered for every list. No accidental "blank screen looks like empty list" cases.

#### Accessibility — minimum bar

- [x] **A11Y-1** All interactive elements keyboard-reachable; no focus traps; skip-to-main link in shell.
- [x] **A11Y-2** Color contrast on dark + light themes meets WCAG AA. Audit the OKLCH palette; bump where needed.
- [x] **A11Y-3** Status indicators have text or aria-label, not color-only (already a UI/UX principle, audit & fix any gaps).
- [x] **A11Y-4** Tables have proper `<th scope>` + caption.

#### Tests

- [x] **TEST-3** Vitest: tests for the new fetch wrapper 401 handling + the SSE reconnect logic.
- [x] **TEST-4** Vitest: tests for the reimage flow's confirm-gate (typed ID match) and rollback on POST failure.
- [x] **TEST-5** Server: Go tests for the new GPG endpoints + the SSE image channel.

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

---

## Versioning reset (2026-04-29)

Founder directive: restart versioning. Existing `v1.0.0..v1.12.2` tags are legacy. **No release until Sprint 4 ships.** The next tag will be `v0.1.0` — explicitly pre-stable. Gilfoyle's RPM pipeline + `pkg.sqoia.dev` infra are wired and idle, waiting for that tag.

Existing tags remain in place as historical record. Slurm bundle tags (`slurm-v24.11.4-clustr*`) are unrelated and stay.

---

## Sprint 4 — Creation Flows (the missing half of the app)

**Started:** 2026-04-29 (immediately after Sprint 3)
**Target:** 10–14 days. Escalate at day 16.

### Goal

The app currently lets operators **observe and act on existing things** but not **create new ones**. You cannot add a node, create an image (from URL or ISO), or build an initramfs from the web UI. Until those creation flows exist, this is an observability dashboard, not a complete management application. Sprint 4 closes the gap.

After Sprint 4 ships green, we tag **v0.1.0** — the first release of clustr v2.

### Re-use the proven patterns

- TanStack Query mutations with optimistic updates + rollback on error (already audited for current mutations in Sprint 3 POL-5).
- SSE for any operation that takes longer than a click (long downloads, builds, registrations).
- Inline destructive confirmation when relevant (e.g. cancel a running build).
- Form validation via Zod schemas.
- Empty states still teach with paste-ready CLI snippets — but now there's also a button right next to the snippet to do the same thing in the UI.

### In scope

#### Add Node (web UI)

- [x] **NODE-CREATE-1** Server: confirm the registration endpoint used by `clustr-cli register` (read CLI source). Document its shape; if it returns 200/201 with the new node, web reuses as-is. If it requires CLI-only quirks (e.g. mTLS), expose a parallel `POST /api/v1/nodes` that's session-cookie authenticated.
- [x] **NODE-CREATE-2** Web: "Add Node" button on the Nodes empty state AND in the topbar (next to filters). Opens a `<Sheet>` with a form: hostname, MAC address, IP/network (preferably auto-detected if cluster has DHCP), role (controller/worker/both), optional notes.
- [x] **NODE-CREATE-3** Form validation (Zod): hostname matches `^[a-z0-9-]{1,63}$`, MAC normalized to lowercase colon-form, IP is valid IPv4 (or empty for DHCP).
- [x] **NODE-CREATE-4** On submit: optimistic insert into the Nodes list with status "registering"; rollback on error and show field-specific server validation errors verbatim.
- [x] **NODE-CREATE-5** Cmd-K action "Add node…" opens the same Sheet inline.
- [x] **NODE-CREATE-6** Empty state on `/nodes` shows the button alongside the existing CLI snippet.

#### Create Image — from URL

- [x] **IMG-URL-1** Server: `POST /api/v1/images/from-url` accepts `{url, name?, expected_sha256?}`, kicks off async download into the image store. Returns `{image_id, status: "downloading"}`. Emits image SSE events (`image.downloading`, `image.created`, `image.failed`) with progress percent + bytes.
- [x] **IMG-URL-2** Server: validate URL scheme (`http`/`https` only), HEAD-check Content-Length before committing if reachable. Reject URLs to internal IPs unless an allowlist flag is set (SSRF guard).
- [x] **IMG-URL-3** Server: if `expected_sha256` is provided, verify after download; on mismatch, delete the partial and emit `image.failed` with reason.
- [x] **IMG-URL-4** Web: "Add Image" button on `/images` opens a Sheet with tabs: "From URL" / "Upload ISO". URL tab has fields: URL, Name (auto-suggested from URL filename), expected SHA256 (optional, with a "Why?" tooltip).
- [x] **IMG-URL-5** On submit: optimistic insert with status "downloading"; SSE drives the progress bar. Sheet stays open with the progress card; close button cancels the download (server endpoint `DELETE /api/v1/images/{id}` while status=downloading aborts).
- [x] **IMG-URL-6** Cmd-K action "Add image from URL…" opens the same flow.

#### Create Image — ISO Upload (resumable, TUS protocol)

Sprint 4 ships resumable uploads via the TUS protocol — interrupted uploads of multi-GB ISOs auto-resume from the last byte rather than restarting at 0.

- [x] **IMG-ISO-1** Server: implement TUS 1.0 protocol endpoints under `/api/v1/uploads/`. `POST` creates an upload (returns URL + Upload-Length), `HEAD` returns current offset, `PATCH` accepts byte ranges at the current offset, `DELETE` aborts and cleans up. Spec: https://tus.io/protocols/resumable-upload (link only — read the spec, don't trust memory of it).
- [x] **IMG-ISO-2** Server: `POST /api/v1/images/from-upload` accepts `{upload_id, name?, expected_sha256?}` after the TUS upload completes. Server moves the assembled file into the image store, computes SHA256, registers the image. Emits standard image SSE events.
- [x] **IMG-ISO-3** Server: streaming PATCH writes chunks directly to disk (never buffer whole ISO). Default cap 32 GB, configurable. Stale uploads (no PATCH for >24h) garbage-collected.
- [x] **IMG-ISO-4** Web: `tus-js-client` library (small, standard). Drag-and-drop area + file picker. After completion, the client calls `POST /api/v1/images/from-upload` to register.
- [x] **IMG-ISO-5** Real upload progress driven by tus-js-client's `onProgress` callback. Pause/Resume buttons (TUS supports this natively). Reconnect attempts on network blip.
- [x] **IMG-ISO-6** SHA256 in browser via SubtleCrypto for <2 GB files; ≥2 GB skip client hash and rely on server's. Show computed/expected match status when complete.
- [x] **IMG-ISO-7** Soft warning if file >10 GB: "Large ISO — consider hosting it internally and using From URL." Don't block; just nudge.

#### Image deletion (web UI)

- [x] **IMG-DEL-1** Server: ensure `DELETE /api/v1/images/{id}` works for completed images, not just downloading-state cancel. Refuse with 409 + clear error if any node currently uses the image (operator must reimage them first). Refuse with 409 if any initramfs build references it as a base.
- [x] **IMG-DEL-2** Web: Delete button in image detail Sheet with inline destructive confirmation (typed image name to confirm, per UI/UX principle 4). Show "blocked: in use by N nodes" state with a list of those nodes (clickable to navigate) when refusal applies.
- [x] **IMG-DEL-3** Cmd-K action: "Delete image…" opens a search picker, then the same inline confirmation flow.
- [x] **IMG-DEL-4** Optimistic remove from list with rollback on 409.

#### Edit Node

- [x] **EDIT-NODE-1** Server: `PATCH /api/v1/nodes/{id}` accepts partial fields — `hostname`, `role` (controller/worker/both), `network` (IP/CIDR overrides), `notes`. Validate same rules as create. Emits `node.updated` SSE event.
- [x] **EDIT-NODE-2** Web: Edit button in node detail Sheet flips the read-only fields into editable form (inline, not modal). Save / Cancel buttons. Optimistic update with rollback on validation error.
- [x] **EDIT-NODE-3** Role changes that affect cluster topology (e.g. demoting a controller) require typed-hostname confirm before submitting — they're destructive in the cluster sense.
- [x] **EDIT-NODE-4** Cmd-K: "Edit node…" opens picker → opens detail Sheet in edit mode.

#### Node groups / tags / labels

Use Kubernetes-style key:value tags. "Groups" emerge from filtering by tag — no separate group concept.

- [x] **TAG-1** Server: `tags` field on the node model, persisted as a JSON object (or separate tags table — your call, document in commit). `POST /api/v1/nodes/{id}/tags` adds, `DELETE /api/v1/nodes/{id}/tags/{key}` removes. Tag keys match `^[a-z0-9._/-]{1,63}$`, values up to 255 chars.
- [ ] **TAG-2** Server: `GET /api/v1/nodes?tag=key:value` filters by tag. Multiple `tag` params = AND. (Sprint 5: tag filter URL param)
- [x] **TAG-3** Web: tag chips visible in Nodes list (compact form: `env=prod`). Inline + button on each row to add a tag (popover with key + value inputs).
- [ ] **TAG-4** Web: filter bar gains tag selector (autocomplete from observed keys). URL-driven (existing pattern). (Sprint 5)
- [x] **TAG-5** Web: tag detail in node Sheet with full management (add, remove with × on chip).

#### Bulk node creation (CSV / YAML paste)

- [x] **BULK-1** Server: `POST /api/v1/nodes/batch` accepts an array of node specs. Validates each, returns per-row results: `{index, status: "created" | "skipped" | "failed", id?, error?}`. Atomicity: NOT all-or-nothing — partial success is OK; the response tells the operator exactly what landed.
- [x] **BULK-2** Web: Add Node Sheet gains a "Bulk" tab beside "Single". Textarea accepts CSV (with header `hostname,mac,ip,role,notes`) or YAML (a list of objects with the same keys). Auto-detect format on paste based on first non-blank line.
- [x] **BULK-3** Web: "Preview" button parses the input client-side and shows a table of what will be created (rows with red highlighting for parse errors). Operator confirms before submit.
- [x] **BULK-4** Web: on submit, show row-by-row results in the same table (status column populated as the server response comes back).
- [ ] **BULK-5** Empty state: include a sample CSV snippet alongside the existing CLI snippet. (deferred to Sprint 5)

#### Activity deletion

- [x] **ACT-DEL-1** Server: `DELETE /api/v1/audit/{id}` removes a single activity entry. `DELETE /api/v1/audit?before=<rfc3339>&kind=<k>` bulk-removes entries matching the filter (returns count deleted).
- [x] **ACT-DEL-2** Server: any deletion is itself logged as an `audit.purged` event (with the count + filter that was used) so the meta-trail is preserved. The audit-purged events themselves cannot be deleted (or only deletable by some explicit override that's not in scope here).
- [x] **ACT-DEL-3** Web: row-level checkbox column on the Activity table. "Delete selected" button appears when ≥1 row is selected; opens inline confirmation requiring the operator to type "delete N entries".
- [x] **ACT-DEL-4** Web: header-bar "Clear filtered…" button visible when a filter is active. Inline confirmation: shows the filter being used + the count that will be deleted. Operator types "clear" to confirm.
- [x] **ACT-DEL-5** After deletion, optimistic remove from the list; SSE confirms (the new `audit.purged` event also lands in the stream — visible immediately).

#### Build Initramfs

- [x] **INITRD-1** Server: identify the existing initramfs build path (probably `dracut`-based or similar — read `internal/server/` for any existing build code; CLI may already have `clustr-cli initramfs build`). Wrap as `POST /api/v1/initramfs/build` accepting `{base_image_id, modules?, kernel_args?}`. Returns `{build_id, status: "queued"}`. Emits SSE events for queued/running/log-line/completed/failed with timestamped log lines.
- [x] **INITRD-2** Server: built artifact is registered as an image in the same image store, with kind=initramfs. Operators can then deploy it to nodes via the existing reimage flow.
- [x] **INITRD-3** Web: "Build Initramfs" button in the Images surface (Bundles tab, since initramfs is a bundle-like artifact). Opens a Sheet form: base image (dropdown of compatible base images), additional modules (multi-select or comma-separated string), kernel args (textarea, monospace).
- [x] **INITRD-4** On submit: optimistic insert into the Images list with status "building"; SSE drives a live log panel inside the detail Sheet (auto-scroll lock per existing Activity pattern).
- [x] **INITRD-5** On completion: toast with "View resulting image" action that opens the new initramfs's detail Sheet.
- [x] **INITRD-6** Cancel button cancels a running build (server endpoint `DELETE /api/v1/initramfs/builds/{id}`).
- [x] **INITRD-7** Cmd-K action "Build initramfs…" opens the same flow.

#### Cross-cutting

- [x] **X-1** Activity stream (`/activity`) gets new event kinds: `node.registered`, `image.downloaded`, `image.uploaded`, `initramfs.built` (and the corresponding `.failed` variants). Each shows up in the unified feed. (audit events fire via existing audit.Record calls)
- [x] **X-2** Toast notifications fire on every successful or failed creation, with a "View" action linking to the entity.
- [x] **X-3** Empty states updated — every list (Nodes, Images, Bundles inside Images) shows the new "Create…" button next to the existing CLI snippet so operators see both paths.
- [ ] **X-4** Vitest: critical paths covered — node-create form validation, image-from-URL mutation flow, ISO upload progress events, initramfs build SSE consumption. (Sprint 5)
- [ ] **X-5** Go tests: server endpoints for from-url, upload, initramfs-build (httptest style, mirror existing auth_test.go pattern). (Sprint 5)
- [x] **X-6** README Quick Start updated: after `dnf install` and `bootstrap-admin`, "Open the web UI, add your first node, upload an ISO, build initramfs" — show the operator the happy path is fully UI-driven.

### Out of scope (Sprint 5+)

- Resumable / chunked / TUS-protocol uploads (Sprint 4 = single-shot upload only).
- Image deletion through the UI (CLI keeps doing it for now — surface the snippet in the detail Sheet).
- Editing existing nodes (rename, change role, etc) — separate sprint, more careful.
- Node groups / tags / labels.
- Bulk creation (add many nodes at once via CSV/YAML).
- Multi-tenancy / orgs / team management.
- User self-service password reset (still admin-driven).

### Definition of done

1. All Sprint 4 checkboxes ticked in this doc.
2. CI green on the merge commit (lint, vitest, go test, build, smoke, gosec, govulncheck, trivy).
3. Autodeploy on cloner ships the latest SHA.
4. Operator end-to-end on a fresh deploy: log in → click "Add node" → register a node → click "Add image" / "From URL" → image downloads with live progress → click "Build initramfs" against that image → watch live log → resulting initramfs available → reimage a node onto it. All from the web UI; no CLI required for these flows.
5. **Tag `v0.1.0` after Sprint 4 ships green.** Gilfoyle's release pipeline fires; RPMs land at `pkg.sqoia.dev/clustr/{el8,el9,el10}/x86_64/`. Verify on a fresh Rocky 9 VM in the Proxmox lab.

---

## Sprint 4 SHIPPED — v0.1.4 RELEASED (2026-04-29)

- Sprint 4 green SHA: `e69e76a`. v0.1.4 release SHA: `8b4e9b0` (post-pipeline-fixup).
- All 6 RPM URLs serving from `pkg.sqoia.dev/clustr/{el8,el9,el10}/{x86_64,aarch64}/`. Repo metadata GPG-signed.
- v0.1.0–v0.1.3 are pipeline-iteration tags (broken signing); v0.1.4 is the first working release.
- POST-SPRINT-4 GAP CLOSED: node deletion UI — Delete button in node detail Sheet with inline typed-hostname confirm, DELETE mutation with optimistic remove + 409 rollback, and Cmd-K "Delete node…" picker.

---

## Sprint 5 — Catch-up + Carry-overs

**Started:** 2026-04-29 (immediately after v0.1.4 ship)
**Target:** 4–6 days. Small sprint.

### Goal

Sprint 4 shipped functionally complete but Dinesh deferred test coverage on the new endpoints, plus a few small UX gaps. Sprint 5 closes those plus tidies the v0.1.x tag visibility.

### In scope

#### Test catch-up (Sprint 4 X-4 / X-5)

- [x] **TEST-S5-1** Vitest: node-create form validation (valid + each invalid field path), Edit-Node optimistic update + rollback on 409, Bulk add CSV/YAML parser preview, image-from-URL mutation flow, TUS upload progress event handling, initramfs build SSE consumption (queued → running → log → completed).
- [x] **TEST-S5-2** Go httptest: `from-url` (success / scheme reject / SSRF reject / SHA256 mismatch / cancel mid-download), TUS endpoints (POST create + HEAD offset + PATCH chunk + DELETE abort + GC stale), `from-upload` (valid + unknown upload_id), `nodes/batch` (all-success + partial-fail + 0-row), `initramfs/build` (success + cancel mid-run), `audit DELETE` (single + filtered bulk + audit.purged meta-event present + meta-event itself undeletable).
- [x] **TEST-S5-3** Add `pnpm exec vitest run` and `go test ./...` enforcement to CI lint+test jobs (already enforced post-Sprint 3 — verify the new tests are picked up; expand if not).

#### UX carry-overs

- [x] **TAG-2** `?tag=key:value` URL param filter on `/nodes`. Multiple `tag` params = AND. Server endpoint already supports filter; web reads URL state and passes to the query.
- [x] **TAG-4** Filter bar tag selector with autocomplete from observed keys (de-dup the keys from the current node list; suggest values per key). URL-driven (existing pattern).
- [x] **BULK-5** Nodes empty state shows a paste-able CSV sample alongside the existing CLI register snippet. The CSV matches the bulk-add format from BULK-2.

#### Release hygiene

- [x] **REL-1** Mark v0.1.0, v0.1.1, v0.1.2, v0.1.3 GitHub Releases as prerelease (so v0.1.4 is the default download path on the releases page). Tags stay as git history. Use `gh release edit <tag> --prerelease`.
- [x] **REL-2** Add a one-line note to the v0.1.4 GH Release body: "First working release; v0.1.0–v0.1.3 were pipeline iterations." Don't make it a full changelog or launch announcement.

### Out of scope (Sprint 6+)

- Anything not in this list. If quality issues surface from real operator use of v0.1.4, that becomes Sprint 6.
- LDAP / sysaccounts / slurm management / portals (still wiped, still gone).

### Definition of done

1. All Sprint 5 checkboxes ticked.
2. CI green on the merge SHA, with the new tests passing.
3. Tag `v0.1.5` after merge — RPM pipeline auto-fires, packages land at pkg.sqoia.dev (no manual intervention this time, the pipeline is solid).
4. Tag URL filter works end-to-end: paste `/nodes?tag=env:prod&tag=role:worker`, see filtered list.
5. v0.1.0–v0.1.3 are no longer the default visible release on the GH releases page.

---

## Sprint 6 — Operational depth (the CLI gaps that matter)

**Started:** 2026-04-30
**Target:** 12–16 days. Bigger sprint than 5 — seven workstreams.

### Goal

Sprint 6 closes the operational depth gaps that force operators into the CLI today: power control, hardware/IPMI config, group operations, and image lifecycle (shell, capture, reuse-files).

### In scope

#### Power buttons on the Nodes list

- [ ] **PWR-LIST-1** Per-row action cluster on `/nodes` (compact, hover-revealed or always-on column): on/off/cycle/reset/boot-PXE/boot-disk. Keyboard accessible, ARIA labels.
- [ ] **PWR-LIST-2** Each click → existing `POST /api/v1/nodes/{id}/power/<action>`. Optimistic state update; toast on success/failure with BMC error verbatim.
- [ ] **PWR-LIST-3** Multi-select rows + bulk action bar: "Power off N nodes…" with typed-confirm. Same for power-cycle.
- [ ] **PWR-LIST-4** Power-state column shows live state (on/off/unknown) via SSE; per-row hover-poll fallback.

#### IPMI / Proxmox config under node detail

- [ ] **CFG-1** New "Hardware" section in node detail Sheet — read-mode default. Fields: BMC (address, username, interface, vendor, last sensor snapshot); Proxmox fields (node-name, vmid, host-IP, credentials reference) only for VM-backed nodes.
- [ ] **CFG-2** Sensors panel (read-only IPMI temp/fan/voltage). Refresh button + auto-refresh while sheet open.
- [ ] **CFG-3** Edit BMC config: typed-confirm on save (BMC IP changes can lock the operator out). Server: `PATCH /api/v1/nodes/{id}/bmc` (read source first; add if missing).
- [ ] **CFG-4** Test connection button: `POST /api/v1/nodes/{id}/bmc/test` returns OK or BMC error verbatim.

#### Node groups — display + UI

- [ ] **GRP-1** Server endpoints (some exist per `server.go:1562-1565`; extend as needed):
  - `GET /api/v1/node-groups` (list with member counts)
  - `GET /api/v1/node-groups/{id}` (detail with members)
  - `POST/DELETE /api/v1/node-groups/{id}/members[/{node_id}]`
  - `POST /api/v1/node-groups/{id}/reimage` (rolling — see REIMG-BULK)
- [ ] **GRP-2** Web: Groups as a **tab on `/nodes`**, not a top-level surface (keeps IA at 4 surfaces). Toggles between "All nodes" and "Groups" view.
- [ ] **GRP-3** Group detail Sheet: name, member list (+/- buttons or drag-drop), tags, last reimage timestamp. Inline destructive delete with typed group name.
- [ ] **GRP-4** Create group: toolbar button → Sheet form (name, optional description, optional initial members from a node picker).
- [ ] **GRP-5** Cmd-K: "Create node group…", "Edit group…", "Delete group…".

#### Bulk reimage (rolling, group-scoped)

- [ ] **REIMG-BULK-1** Server: `POST /api/v1/node-groups/{id}/reimage` accepts `{target_image_id, kernel_args?, parallelism?}`. Returns `{job_id}`. SSE per-node progress events. Default parallelism = 1.
- [ ] **REIMG-BULK-2** Web: "Reimage group" button on group detail Sheet. Inline confirm panel: target image dropdown (compatible only), parallelism slider (1..N), typed group name to confirm.
- [ ] **REIMG-BULK-3** Live progress panel inside sheet via SSE — per-node status (queued/imaging/verifying/done/failed). Cancel button cancels remaining queued; in-flight nodes finish or fail naturally.
- [ ] **REIMG-BULK-4** On completion: toast "Reimaged N/M nodes in <group> (X failed)" with link to filtered Activity.

#### ISO import — reuse files already on server filesystem

- [ ] **ISO-FS-1** Server: `GET /api/v1/images/local-files` lists ISO/image files in import dir (e.g. `/var/lib/clustr/imports/`). Returns `[{path, name, size, mtime, sha256?}]`. Path configurable via `CLUSTR_IMPORT_DIR`.
- [ ] **ISO-FS-2** Server: `POST /api/v1/images/from-local-file` accepts `{path, name?, expected_sha256?}`. Validates path within import dir (no traversal). Registers file as base image in-place (hardlink/symlink — document choice). Standard image SSE events.
- [ ] **ISO-FS-3** Web: Add Image Sheet gains a third tab **"From server filesystem"** alongside "From URL" and "Upload ISO". Lists files with size, mtime, SHA256 (lazy on click). Operator picks + names + submits.
- [ ] **ISO-FS-4** Empty state: explains where to drop files (the import dir path) so they appear.

#### Image shell (interactive chroot via web terminal)

- [ ] **SHELL-1** Server: `WebSocket /api/v1/images/{id}/shell` opens PTY-backed shell inside chroot of the image. Bidirectional binary stream. Idle timeout 15min, hard timeout 60min.
- [ ] **SHELL-2** Server: admin role only. Audit-log every shell session (operator + image + duration).
- [ ] **SHELL-3** Reuse existing `clustr image shell` code path if present (`cmd/clustr/`). Wrap chroot mount/unmount in clean session lifecycle.
- [ ] **SHELL-4** Web: image detail Sheet "Shell" button → full-screen drawer (or new tab) with xterm.js + xterm-addon-fit + WebSocket. Disconnect on tab close.
- [ ] **SHELL-5** Add `@xterm/xterm` + `@xterm/addon-fit` via pnpm if not already in deps.
- [ ] **SHELL-6** Cmd-K: "Shell into image…" picker.

#### Image capture from a live node

- [ ] **CAP-1** Server: `POST /api/v1/images/capture` accepts `{source_node_id, name, version?, exclude_paths?}`. Returns `{image_id, capture_id, status:"queued"}`. SSE: `image.capture.started/progress/completed/failed`.
- [ ] **CAP-2** Server-side: SSH from clustr-server to source node, rsync live filesystem, materialize as base image. Reuse existing `clustr image capture` logic if present.
- [ ] **CAP-3** Auth: capture requires admin. Source node must already have a registered key.
- [ ] **CAP-4** Web: node detail Sheet "Capture as base image" button below reimage panel. Inline form: image name, version, exclude paths textarea. Typed source hostname to confirm.
- [ ] **CAP-5** Live progress in Sheet via SSE — bytes transferred + rsync output streaming.
- [ ] **CAP-6** Toast on completion linking to new image.
- [ ] **CAP-7** Cmd-K: "Capture image from node…" picker.

#### Cross-cutting

- [ ] **X6-1** New Activity event kinds: `node.power.on/off/cycled`, `node.bmc.updated`, `node-group.*` (created/updated/deleted/reimaged), `image.captured`, `image.shell.started/ended`.
- [ ] **X6-2** Vitest: power button mutation, group create flow, capture progress SSE, xterm.js mount + WebSocket reconnect.
- [ ] **X6-3** Go tests: BMC patch + test, group reimage SSE, local-files listing with traversal-rejection cases, capture happy path.
- [ ] **X6-4** README — add a sentence under Quick Start showing the new operator workflows are now web-first.

### Out of scope (Sprint 7+)

- LDAP / sysaccounts / portals (still wiped, still gone).
- Multi-tenancy / orgs.
- BMC firmware updates / cluster-wide IPMI policy push.
- Image diff (compare two images).
- Multi-node parallel shell (broadcast tmux-style).
- Live disk capture without quiesce.

### Definition of done

1. All Sprint 6 checkboxes ticked.
2. CI green on the merge SHA. New Vitest + Go test counts visible in CI logs.
3. Autodeploy on cloner ships the latest. Hard-refresh required (cache headers ensure that's enough).
4. Operator end-to-end on cloner, **no CLI required**:
   - Power-cycle a node from the `/nodes` list (single click).
   - Open node detail → Hardware section shows BMC + Proxmox config + sensors → edit BMC IP.
   - Create a node group with 2 members.
   - Trigger rolling reimage on the group, watch per-node progress.
   - Open image shell into a base image, run a command, close.
   - Capture a live node as a new base image.
   - Add an image from a file already on the server filesystem.
5. Tag `v0.2.0` after Sprint 6 ships green — substantive feature release. RPM pipeline auto-fires.

---

## Sprint 7 — Identity (LDAP / Sudoers / System Accounts)

**Started:** 2026-04-30
**Target:** 8–12 days. Medium sprint.

### Goal

Bring back the operator-facing UI for LDAP and identity management. The server-side LDAP integration is fully intact (`internal/ldap/Manager`, migrations 028/029/036/069/070, endpoints already mounted) — only the UI was wiped with the legacy webapp. This sprint surfaces the core identity workflows again so operators can configure LDAP, preview sudoers, and manage local system accounts from the web.

### IA change — fifth top-level surface

Adding **Identity** as a top-level sidebar entry. Founder explicitly authorized breaking the "4 surfaces only" rule for this — identity ops are core HPC operator work. New IA:

```
Nodes / Images / Activity / Settings / Identity
```

The order in the sidebar puts Identity after Settings (admin-y category cluster).

### In scope

#### Identity surface — anchored sections in this exact order

```
/identity
  ├── #users           ← Local clustr users CRUD + LDAP user browse
  ├── #groups          ← LDAP groups browse (read-only)
  ├── #system-accounts ← Linux accounts the cloning process provisions on nodes
  └── #ldap-config     ← LDAP server config + test
```

Sudoers is **not** on this surface — it moves to per-node (see "Per-node sudoers" section below).

#### Users section (CRUD for local clustr users + LDAP browse)

- [ ] **USERS-1** Server: confirm/extend endpoints under `/api/v1/admin/users` (the CLI hits these — read `cmd/clustr/users.go` for the exact paths). Operations: list, create (admin sets initial password), reset-password (admin sets temp password, marks `must_change_password`), edit (role, username), disable (soft delete preserving audit history), enable.
- [ ] **USERS-2** Web Users section, top sub-card "Local users": table with username, role, last login, status. Add/Edit/Reset-password/Disable inline flows.
  - **Add**: inline form with username, initial role dropdown (admin/operator/readonly/viewer), initial password (auto-generated suggestion + show toggle).
  - **Edit**: same fields editable except username (immutable for audit integrity), inline-save.
  - **Reset password**: click button → server generates a temp password → modal-free panel shows it ONCE with a copy button + a one-time hint "Send this to the user; they'll be prompted to change on next login."
  - **Disable**: inline destructive confirm with typed username. Soft delete only (preserves audit). Re-enable is its own action.
- [ ] **USERS-3** Web Users section, second sub-card "LDAP users": search box + result list (read-only). Hits `GET /api/v1/ldap/users?q=<query>` (server endpoint to add — wraps an LDAP search via the existing `Manager`). Fields shown: uid, full name, email, primary group, member-of (collapsed). NO edit/delete — directory is read-only from clustr.
- [ ] **USERS-4** Cmd-K: "Add user…", "Reset user password…", "Search LDAP users…".

#### Groups section (LDAP browse + supplementary overlay + specialty groups)

Three behaviors in one section. **The existing auto-creation / auto-membership flow stays intact** — clustr keeps deriving group state from LDAP at deploy time. This UI adds operator overlay on top.

- [ ] **GRP-LDAP-1** Server: confirm or add `GET /api/v1/ldap/groups?q=<query>` returning LDAP group entries via `Manager`. Fields: cn, dn, gidNumber, ldap_members (collapsed list), clustr_supplementary_members (clustr overlay).
- [ ] **GRP-OVERLAY-1** Server: data model for clustr-supplementary memberships (membership rows that overlay LDAP groups without writing to the directory). Read source first — there may already be a table for this. If not, add `clustr_group_overlays` (group_dn, user_identifier, source). Endpoints: `POST /api/v1/groups/{group_dn}/supplementary-members` (`{user_identifier, source}`), `DELETE /api/v1/groups/{group_dn}/supplementary-members/{user_identifier}`.
- [ ] **GRP-OVERLAY-2** Server: at deploy/reimage time, the rendered `/etc/group` on each node merges LDAP-native members + clustr supplementary overlay. Verify the existing `Manager.NodeConfig` path, extend if it doesn't already do this merge.
- [ ] **GRP-SPECIALTY-1** Server: data model for clustr-only specialty groups (groups that exist entirely in clustr, no LDAP backing). If a table doesn't exist, add `clustr_specialty_groups` (id, name, gid, description, members). Endpoints: `GET/POST/PATCH/DELETE /api/v1/groups/specialty[/{id}]`.
- [ ] **GRP-SPECIALTY-2** Server: specialty groups deploy alongside system accounts via the existing cloning pipeline. Verify deploy logic handles them; extend if not.
- [ ] **GRP-WEB-1** Web Groups section: unified table with columns: name, source (LDAP / Specialty), gidNumber, member count. Filter by source. Search across both.
- [ ] **GRP-WEB-2** Click an LDAP group row → expand to show: LDAP-native members (read-only, from directory) + clustr-supplementary members (manage via add/remove buttons with the same user-picker dropdown the per-node sudoer flow uses).
- [ ] **GRP-WEB-3** Click a specialty group row → expand to show: full member list, edit name/gid/description, manage members via the user picker. Inline destructive delete (typed group name).
- [ ] **GRP-WEB-4** "Create specialty group" button at top of Groups section → inline form: name, gid (auto-suggest next available), description, optional initial members.
- [ ] **GRP-WEB-5** Cmd-K: "Add user to group…", "Create specialty group…", "Search groups…".
- [ ] **GRP-AUTO-1** **Important — preserve existing auto-creation behavior.** Whatever clustr currently does to auto-create or auto-populate groups during deploy MUST keep working. The new UI is additive overlay; it does not replace the auto-flow. Read `internal/ldap/Manager` to confirm what auto-creation/auto-membership logic exists, document it briefly in PR description so it's clear the new code preserves it.

#### System accounts section

- [ ] **SYSACCT-1** Server: confirm or add CRUD for `system_accounts` (migration 029). Endpoints: `GET /api/v1/system-accounts`, `POST`, `PATCH /{id}`, `DELETE /{id}`.
- [ ] **SYSACCT-2** Fields per existing schema: username, uid, gid, primary group, supplementary groups, home dir, shell, comment, target node-groups.
- [ ] **SYSACCT-3** Web System accounts section: table with username, uid, gid, target groups, status. Add/Edit/Delete via inline-confirm (typed username for delete).
- [ ] **SYSACCT-4** Validation: username `^[a-z][a-z0-9_-]{0,30}$`, uid/gid integer; warn-don't-block on collision with system reserved (0..999).
- [ ] **SYSACCT-5** Existing cloning pipeline already consumes the schema — UI is read/write only. Verify by reading `internal/ldap/Manager.NodeConfig`.
- [ ] **SYSACCT-6** Cmd-K: "Add system account…".

#### LDAP config section

- [ ] **LDAP-1** Server: confirm/add `GET /api/v1/ldap/config` and `PUT /api/v1/ldap/config`. Read `internal/ldap/Manager` for the current config struct shape and reuse it. Fields: server URL, base DN, bind DN, bind password (write-only — never returned), user search filter, group search filter, TLS mode (none/starttls/tls), CA cert (optional, paste PEM).
- [ ] **LDAP-2** Server: `POST /api/v1/ldap/test` — bind + sample search using either submitted-but-not-yet-saved config OR saved config. Returns success or structured error.
- [ ] **LDAP-3** Web LDAP config section: read-mode shows current config (password masked). Edit-form on click. Test button visible in both modes. Save persists; test-result banner persists until next test or save.
- [ ] **LDAP-4** Bind password input never echoes saved value (placeholder "leave blank to keep"). Audit-log every config change as `ldap.config.updated` with field names (NOT values).
- [ ] **LDAP-5** Cmd-K: "LDAP config…", "Test LDAP connection…".

#### Per-node sudoers (in /nodes detail Sheet — NOT on Identity surface)

Founder direction: sudoers should be **per-node and explicit**, not a global LDAP-derived push. Operators add/edit/delete sudoers on a specific node via a dropdown user picker (LDAP users + local users). The existing global LDAP-derived sudoers stays as a background mechanism but isn't surfaced in the operator UI — explicit per-node assignments are the operator-facing model.

- [ ] **NODE-SUDO-1** Server: new model — `node_sudoers` table (or extension of existing). Fields: node_id, user_identifier (LDAP DN or local username), source (ldap/local), commands (default `ALL`), assigned_at, assigned_by. Add migration if needed.
- [ ] **NODE-SUDO-2** Server endpoints: `GET /api/v1/nodes/{id}/sudoers` (list), `POST /api/v1/nodes/{id}/sudoers` (`{user_identifier, source, commands?}`), `DELETE /api/v1/nodes/{id}/sudoers/{user_identifier}`.
- [ ] **NODE-SUDO-3** Server: each per-node sudoer assignment must end up in the actual `/etc/sudoers.d/clustr` on that node. Hook into the existing deploy / reimage path so the file lands on next deploy. For already-deployed nodes, expose `POST /api/v1/nodes/{id}/sudoers/sync` to push the current set without reimaging.
- [ ] **NODE-SUDO-4** Web: in the node detail Sheet (read-mode, below the Hardware section), add a "Sudoers" subsection.
  - List of current sudoers on this node: each row shows username, source badge (LDAP / Local), commands, remove button.
  - "Add sudoer" inline form: dropdown picker that searches **both LDAP and local users** by typed query; selecting a result populates the entry. Optional commands field (default `ALL`).
  - Inline destructive remove — typed username confirm.
  - "Sync to node now" button below the list when there are unsynced changes.
- [ ] **NODE-SUDO-5** The user-picker dropdown component is reusable — same pattern as the Cmd-K node picker but typed for users. Users come from a unified `GET /api/v1/users?q=&source=ldap|local|all` endpoint (combine LDAP search + local DB).
- [ ] **NODE-SUDO-6** Cmd-K: "Add sudoer to node…" picker.
- [ ] **NODE-SUDO-7** Activity event: `node.sudoer.added/removed/synced`.

#### IA + sidebar

- [ ] **IA-1** Add `/identity` route in TanStack Router. Required role: admin.
- [ ] **IA-2** Sidebar: add "Identity" entry below "Settings" with a Lucide icon (shield-check or fingerprint).
- [ ] **IA-3** Update memory `project_clustr_webapp_v2.md` IA section: 5 surfaces, not 4. Note Sudoers is per-node, not Identity-level.

#### Cross-cutting

- [ ] **X7-1** Activity event kinds added: `ldap.config.updated`, `ldap.test.run`, `ldap.sudoers.pushed`, `system-account.created/updated/deleted`.
- [ ] **X7-2** Vitest: LDAP config form validation, sudoers SSE consumer, system-account CRUD flows.
- [ ] **X7-3** Go tests: LDAP config GET/PUT (with bind-password masking), test endpoint with mock LDAP server, system-accounts CRUD, sudoers preview produces deterministic output.
- [ ] **X7-4** README — short sentence under the operator workflows section: "Configure LDAP and manage system accounts from the Identity tab."

### Out of scope (deferred — not legacy-wipe, just held)

- **LDAP project plugin** (migration 069) — founder paused; revisit when needed.
- **Per-node-group LDAP restrictions** (migration 070) — same.
- PI / Director portals — still wiped per `feedback_no_legacy_restore.md`.
- Multi-tenant identity / orgs.
- LDAP-backed login *to clustr-serverd itself* — login still uses local password, LDAP-on-clustr-itself is a separate scope.

### Definition of done

1. All Sprint 7 checkboxes ticked in this doc.
2. CI green on the merge SHA. Vitest + Go test counts visible.
3. Autodeploy on cloner ships the latest. Hard-refresh; new Identity sidebar entry visible.
4. Operator end-to-end on cloner, no CLI:
   - Configure LDAP (URL, base DN, bind credentials).
   - Test connection — success path returns user count.
   - View sudoers preview.
   - Push sudoers to a node group, watch per-node progress.
   - Create a system account, target a node group, verify it gets provisioned on next deploy.
5. Tag `v0.3.0` after Sprint 7 ships green — Identity is a major surface addition. Pipeline auto-fires.

---

## Sprint 8 — LDAP write-back

**Founder authorization (2026-04-30):** clustr writes to the LDAP directory, not just reads. Operators can create/edit/delete LDAP users and groups, reset LDAP passwords, edit attributes — all from the web UI.

**Sprint 8 starts after Sprint 7 ships v0.3.0.** Sprint 7 builds the read paths and the user-picker component; Sprint 8 promotes Identity → Users + Identity → Groups to read-write. **Don't start until Sprint 7 is merged.**

### Goal

Promote LDAP integration from read-only to read-write. Operators no longer need a separate directory client for routine identity management. clustr becomes authoritative for the LDAP directory (or at least co-authoritative with the existing directory tooling).

### Architectural calls (decided up front)

- **Backend support:** start with whatever backend `internal/ldap/Manager` already speaks. Read its source. If it's OpenLDAP-flavored, focus there for v0.4.0. If FreeIPA or AD, that. Generalize later.
- **Bind privileges:** writes require a privileged bind. Existing config has `bind DN` + `bind password` for reads. Add an optional **second bind** for writes (`write_bind_dn`, `write_bind_password`) so operators can keep reads on a low-privilege bind and only elevate for writes. If unset, fall back to the read bind.
- **Bind privilege check:** at config-save time, attempt a no-op write probe and warn the operator if the bind is read-only. Don't block — they may not need writes for now.
- **Group membership model:** when an operator edits LDAP group members in the web UI, the directory write is authoritative for that group. The Sprint 7 supplementary-overlay model still exists for groups the operator chooses NOT to touch directly. Per-group toggle: "manage in directory" vs "use overlay." Default = overlay (Sprint 7 behavior).

### In scope

#### LDAP write — config

- [x] **WRITE-CFG-1** Server: extend `LDAPConfig` with optional `write_bind_dn` and `write_bind_password` (both write-only — never returned). PUT `/api/v1/ldap/config` accepts them.
- [x] **WRITE-CFG-2** Server: at config-save time, perform a probe operation (e.g. read+write a tombstone OU, then delete it) with the write bind. Return result as `write_capable: bool` + reason. UI surfaces a yellow banner when not write-capable.
- [x] **WRITE-CFG-3** Web LDAP config section: add the second bind credentials below the read bind. Tooltip: "Optional. Required only if you want to create/edit/delete users and groups in LDAP from clustr." Status indicator: write-capable / read-only / untested.

#### LDAP user write

- [x] **WRITE-USER-1** Server: `POST /api/v1/ldap/users` accepts `{username, full_name?, email?, gid?, ssh_keys?, initial_password?}`. Builds the LDIF entry per backend dialect, binds with write creds, adds the entry. Returns the new DN.
- [x] **WRITE-USER-2** Server: `PATCH /api/v1/ldap/users/{dn}` accepts a partial set of attributes. Per-backend modify operations.
- [x] **WRITE-USER-3** Server: `DELETE /api/v1/ldap/users/{dn}` removes the entry. Refuse with 409 if removing this entry would orphan group memberships beyond a configurable threshold (safety net).
- [x] **WRITE-USER-4** Server: `POST /api/v1/ldap/users/{dn}/reset-password` — server generates a temp password, applies it to the directory entry, returns the temp value once. Per-backend password change op (OpenLDAP password modify extended op vs AD `unicodePwd` vs FreeIPA `pwd_policy`).
- [x] **WRITE-USER-5** Web Identity → Users → LDAP sub-card: add "Add LDAP user" button. On each search result row: Edit / Delete / Reset Password buttons. Edit opens an inline form with attribute fields. Reset shows the temp pwd once with a copy button (mirrors local-user reset UX).
- [x] **WRITE-USER-6** Inline destructive confirm on delete (typed LDAP username).
- [x] **WRITE-USER-7** Cmd-K: "Add LDAP user…", "Edit LDAP user…", "Reset LDAP password…", "Delete LDAP user…".

#### LDAP group write

- [x] **WRITE-GRP-1** Server: `POST /api/v1/ldap/groups` accepts `{name, gid_number?, description?, initial_members?}`. Creates the group entry per backend dialect.
- [x] **WRITE-GRP-2** Server: `PATCH /api/v1/ldap/groups/{dn}` accepts attribute changes including member list edits. Per-backend group-membership representation (`member` DN list vs `memberUid` username list).
- [x] **WRITE-GRP-3** Server: `DELETE /api/v1/ldap/groups/{dn}` removes the group entry. 409 if any system reference still uses it (sudoers, restrictions if Sprint 8.1+).
- [x] **WRITE-GRP-4** Web Identity → Groups: per LDAP group row, gain "Manage in directory" toggle (default off = Sprint 7 overlay behavior). When on, the row's Edit panel writes to the directory directly instead of clustr's overlay. Member changes go through `PATCH`.
- [x] **WRITE-GRP-5** "Add LDAP group" button at the top of the Groups section (alongside the existing "Create specialty group"). Inline form: name, gid (auto-suggest from directory), description, optional initial members from the user picker.
- [x] **WRITE-GRP-6** Cmd-K: "Add LDAP group…", "Edit LDAP group…", "Delete LDAP group…".

#### Audit + safety

- [x] **WRITE-AUDIT-1** Every write op is audit-logged in clustr's audit table with a `directory_write: true` tag, including the operator, the DN, the operation, and a hash of the changed attributes (NOT the values for sensitive ones like passwords).
- [x] **WRITE-SAFETY-1** Per-write inline-confirm with typed entity name on Delete and on Reset Password. Edit flows save inline without typed-confirm.
- [x] **WRITE-SAFETY-2** Read banner at the top of Identity → Users + Identity → Groups when write mode is active: "Writes go directly to your LDAP directory." Yellow when write bind is configured but unverified; green when probed-OK; absent when no write bind set.

#### Backend dialect

- [x] **WRITE-DIALECT-1** Server: detect (or operator-configures) backend type. Use `internal/ldap/Manager` to centralize per-dialect ops. Start with the dialect cloner currently uses; document the assumption clearly.
- [x] **WRITE-DIALECT-2** Provide a stub for each major backend (OpenLDAP, FreeIPA, AD, generic) that surfaces a clear "not implemented for this backend" error rather than silent no-op or corrupting writes.

#### Tests

- [x] **WRITE-TEST-1** Go: in-process LDAP server fixture (e.g. `github.com/go-ldap/ldap` with a fake backend, OR spin up `glauth` in CI). Tests for create/edit/delete user, create/edit/delete group, password reset against the fixture.
- [x] **WRITE-TEST-2** Vitest: write-form validation, optimistic update + rollback on directory error, dialect-specific error message rendering.

### Out of scope (Sprint 9+)

- LDAP project plugin (migration 069) — still deferred.
- Per-node-group LDAP restrictions (migration 070) — still deferred.
- Schema discovery / dynamic attribute editor (auto-detect what attributes the directory schema supports). Sprint 8 hardcodes the common attribute set; schema discovery is a separate sprint if needed.
- LDAP replication awareness (writing to a primary then waiting for read replicas to catch up).
- Bulk import (CSV → LDAP entries).
- Self-service password change (LDAP user changing their own pwd via clustr web UI). Admin-driven only.

### Definition of done

1. All Sprint 8 checkboxes ticked.
2. CI green on the merge SHA, including the new in-process LDAP fixture tests.
3. Autodeploy on cloner ships the latest. Hard-refresh; write-mode banner appears in Identity → Users when a write bind is configured.
4. Operator end-to-end on cloner against a test LDAP server (glauth or spun-up OpenLDAP container in the lab, NOT production directory):
   - Configure LDAP with both read and write binds → see "write-capable" green status
   - Add an LDAP user, set initial password
   - Edit an attribute
   - Reset that user's password, see the temp pwd once
   - Add an LDAP group, add the new user as a member
   - Delete the user → confirmation flow works → 409 if last admin
   - Delete the group → confirmation works
   - Switch a group from overlay-mode to direct-write-mode and back; verify state lands correctly each time
5. Tag `v0.4.0` after Sprint 8 ships — LDAP write capability is a substantive product expansion. Pipeline auto-fires.

---

## Sprint 9 — Internal LDAP auto-deploy UI

**Founder direction (2026-04-30):** clustr already has internal slapd auto-deploy capability (`internal/ldap/slapd.go`, `install.go`, `embed.go`, `assets/clustr-slapd.service`, `templates/slapd-seed.ldif.tmpl`, `Manager.Enable()` per migration 028). Sprints 7+8 LDAP UI was external-only. Sprint 9 surfaces the internal path.

**Richard's call (Option A — mode toggle, default Internal):** failure modes attach to operator-initiated actions; multi-cluster orgs need a non-silent escape hatch; migration 028 already shows internal state is fragile, surfacing the toggle keeps it legible.

### In scope

#### LDAP config — mode toggle

- [x] **MODE-1** Segmented control at top of Identity → LDAP config: `[Internal — clustr-managed]` / `[External — existing directory]`. Default = Internal on first-run. Persisted in `ldap_module_config`.
- [x] **MODE-2** Internal mode form: single base DN field defaulting to `dc=cluster,dc=local`, optional admin-password override (blank = clustr generates + stores), one big "Enable" button. No bind-DN/URL fields.
- [x] **MODE-3** External mode form: the existing Sprint 7+8 form (URL, base DN, read bind, write bind, TLS, write-capable banner).
- [x] **MODE-4** Mode switch requires typed-confirm.

#### Internal — Enable flow

- [x] **ENABLE-1** Server: `POST /api/v1/ldap/internal/enable` accepts `{base_dn, admin_password?}`. Wraps `Manager.Enable()`. Returns success + slapd status OR structured error with remediation hint.
- [x] **ENABLE-2** Structured error codes: `port_in_use`, `slapd_not_installed`, `selinux_denied`, `unit_failed_to_start`. Each with one-line remediation.
- [x] **ENABLE-3** Server: `GET /api/v1/ldap/internal/status` returns `{enabled, running, port, uptime_sec, admin_password_set}`. Polled when Internal mode is active.
- [x] **ENABLE-4** Web: Enable button → "Provisioning slapd…" spinner → green status panel OR inline structured error with copy-button on diagnostic command (e.g. `systemctl status clustr-slapd`).
- [x] **ENABLE-5** Web Internal mode: bind fields hidden. Read+write binds auto-configure to localhost.
- [x] **ENABLE-6** Web: status panel below mode toggle when Internal enabled — slapd state, port, uptime, "Show admin password (one-time)" recovery affordance with copy button.

#### Disable / re-enable / mode switch

- [x] **DISABLE-1** Server: `POST /api/v1/ldap/internal/disable` stops slapd, preserves data dir. `POST /api/v1/ldap/internal/destroy` wipes data — typed-confirm with literal `destroy` required.
- [x] **DISABLE-2** Web: "Disable" link in status panel → inline panel with Stop only / Stop + Wipe options. Stop only is reversible. Stop+Wipe requires typed-confirm.
- [x] **DISABLE-3** Mode-switch UX: switching Internal → External while Internal is running prompts: "Internal LDAP is running. Stop it before switching, or leave it running on this host?" Default = leave running, flip config to External.

#### Cross-cutting

- [x] **X9-1** Activity event kinds: `ldap.internal.enabled`, `ldap.internal.disabled`, `ldap.internal.destroyed`, `ldap.mode.switched`.
- [x] **X9-2** Vitest: mode-toggle UI, Enable mutation each error variant, mode-switch typed-confirm.
- [x] **X9-3** Go tests: `Enable()` happy path + each error variant, status, disable/destroy paths.
- [x] **X9-4** README — one sentence: "By default, clustr provisions its own LDAP server on first config. Switch the LDAP mode to External to connect to an existing directory."

### Out of scope (Sprint 10+)

- Internal↔External data migration (export/import).
- Multi-master / replicated internal LDAP.
- Internal LDAP schema customization beyond seed LDIF.
- Internal LDAP backup/restore from web UI (operator rsync `/var/lib/clustr/slapd/` for now).

### Definition of done

1. All Sprint 9 checkboxes ticked.
2. CI green on the merge SHA.
3. Autodeploy on cloner ships latest. Hard-refresh; mode toggle visible at top of LDAP config, default Internal.
4. Operator end-to-end on cloner, no CLI:
   - Fresh-state instance: open LDAP config, see Internal selected, default base DN populated
   - Click Enable → spinner → green status panel
   - LDAP users / groups / read+write all work against the new internal slapd
   - Switch to External, typed-confirm, see external form
   - Switch back to Internal, typed-confirm, slapd state preserved
   - Disable via status panel; re-enable; data persists
5. Tag `v0.5.0` after Sprint 9 ships — Internal LDAP auto-deploy is a substantive operator-flow expansion. Pipeline auto-fires.

---

## Sprint 10 — Slurm surfaced (manage / build / upgrade)

**Founder direction (2026-04-30):** "no way to manage/build/upgrade slurm bundles/packages, it would be nice to have slurm surfaced."

Server-side capability is far deeper than v2 webapp surfaces: `internal/slurm/{builder.go,upgrade.go,routes.go,manager.go,scripts}` already implement enable/disable, full config CRUD with validation/history/per-node-render, role assignment, scripts editor, async build pipeline (download → deps → configure → make → package → checksum → store), and rolling upgrade orchestration (DBD → controller → compute → login phase ordering). v2 webapp only surfaces the read-only Bundles tab on /images.

### IA change — sixth top-level surface

`Nodes / Images / Activity / Identity / Settings / Slurm`. Slurm operations are deep enough to deserve their own surface. The "4 surfaces only" original IA constraint was retired with Identity (Sprint 7); Slurm is the natural sixth.

### In scope

#### Slurm surface — anchored sections

- [x] **STAT-1..3** Status section: enabled/disabled badge, role counts, controller hostname, last-sync; Enable/Disable buttons (typed-confirm); "Sync now" button.
- [x] **CFG-1..5** Configs section: list editable configs, click → editor Sheet (mono `<textarea>` — see editor note below) with Validate/Save/History tabs; Reseed defaults with typed-confirm.
- [x] **ROLE-1..4** Roles section: node table with current slurm roles, click → inline role editor, multi-select for bulk role change, role-count summary cards.
- [x] **SCR-1..3** Scripts section: list scripts (prolog/epilog/etc), editor Sheet with history, per-script config (dest_path).
- [x] **BUILD-1..6** Builds section: table of builds (version/arch/status/SHA), "Build new" Sheet form (BuildConfig fields), SSE-driven live log panel during build (new `GET /slurm/builds/{id}/log-stream` SSE endpoint added), delete build with typed-confirm, click row → detail with set-active action.
- [x] **UPG-1..5** Upgrades section: list ops, "Start upgrade" Sheet (target build dropdown of completed builds, batch size, drain timeout) with pre-validation; phase stepper (DBD → controller → compute → login); per-node state in detail sheet; pause/resume/rollback controls; completion toast.

#### Per-node integration

- [x] **NODE-SLURM-1..3** Slurm subsection in `/nodes` detail Sheet: current role + sync status + override count display; inline overrides editor (JSON textarea); inline "Set role" picker.

#### IA + sidebar + cleanup

- [x] **IA-S10-1..4** Add `/slurm` route, sidebar entry below Settings with Lucide `Cpu` icon, Cmd-K "Slurm management…" action + `g l` keyboard shortcut, Identity added to Cmd-K nav routes.
- [x] **IMG-BUNDLE-1** Add a "Manage custom Slurm builds in the Slurm tab →" link on the `/images` Bundles tab pointing to `/slurm#builds`.

#### Cross-cutting

- [x] **X10-1..4** Server: new SSE log-stream endpoint for build progress (`GET /slurm/builds/{id}/log-stream`). Client: Slurm types added to `lib/types.ts`. Editor decision: `<textarea>` with JetBrains Mono — Slurm configs are ≤100 lines; codemirror/monaco adds ~300 kB to the bundle without compelling need at this scale. Note: Activity event-kind enums (`slurm.build.*`, `slurm.upgrade.*`) are already recorded via `db.AuditActionSlurmConfigChange` from the server — no new server instrumentation needed.

### Out of scope (Sprint 11+)

- Job queue / scheduler views (operators have `squeue`/`sinfo`).
- Accounting reports (sacct) UI.
- Partition definition UI beyond `slurm.conf` editing.
- QoS editor.
- Federation / multi-cluster slurm.
- Spank plugin management.

### Definition of done

1. All Sprint 10 SPRINT.md checkboxes ticked.
2. CI green on the merge SHA.
3. Autodeploy on cloner ships latest. Hard-refresh; new "Slurm" sidebar entry visible. `/slurm` loads with all sections rendered.
4. Operator end-to-end on cloner, no CLI:
   - View Slurm status, see role counts
   - Edit slurm.conf, validate, save, push to nodes
   - Set a node's slurm role, see it propagate
   - Build a new slurm bundle (e.g. against an older version like 23.11.10 for fast iteration) and watch the SSE log
   - Start a rolling upgrade to the new bundle, watch DBD → controller → compute → login progression
   - Check Activity for the full audit trail

---

## Sprint 12 — systemd lifecycle hardening + Slurm tail

**Shipped:** 2026-04-29

### Audit findings (#88)

Systemd callsites scanned across `internal/`, `cmd/`, `pkg/`. Services with a UI Enable/Disable/Status surface: **1** (`clustr-slapd`). Kill criterion (>3 affected) was **NOT triggered**.

| Service | UI surface | Was Disable missing systemctl disable? | Status reads cached? | Enable port-check? |
|---|---|---|---|---|
| `clustr-slapd` | YES (ldap/internal) | YES — #87 bug class confirmed | YES — `isSlapdRunning()` only ran `is-active` | NO — pre-flight used `ss` but not wired into enable path |
| `slurmctld/slurmd/munge` | NO — node-side only via clientd | n/a | n/a | n/a |
| `slurmdbd` | NO — node-side only | n/a | n/a | n/a |
| `clustr-serverd` | NO — not self-managed | n/a | n/a | n/a |

### Shipped (#89)

- **`internal/sysd/sysd.go`** — shared helpers: `Disable` (stop+disable+reset-failed, idempotent), `QueryStatus` (live, no cache; returns `Active/Enabled/LoadState/ActiveState/UnitFileState`), `Enable` (port-check → daemon-reload → enable → start).
- **`internal/sysd/sysd_test.go`** — anti-regression tests: ButtonState all 4 states, PortInUseError, idempotency gating, FormatPort.
- **`internal/ldap/slapd.go`** — `StopSlapd` now calls `sysd.Disable` (stop+disable+reset-failed); `StartSlapd` calls `sysd.Enable` with port 636 check; `EnableSlapdService` is a shim.
- **`internal/ldap/slapd.go`** — `SlapdStatus()` exported; returns `sysd.Status` from live systemd query.
- **`internal/ldap/internal_routes.go`** — `InternalStatusResponse` extended: `systemd_active`, `systemd_enabled`, `ui_buttons` (derived from `sysd.ButtonState`). Both `isSlapdRunning()` calls removed; all status queries live.
- **`internal/ldap/internal_routes.go`** — `mapEnableError` now handles `*sysd.PortInUseError` → structured `port_in_use` error code.
- **`web/src/lib/types.ts`** — `LDAPInternalStatusResponse` extended with `systemd_active`, `systemd_enabled`, `ui_buttons`. Sprint 12 Slurm TAIL types added: `SlurmRenderPreviewResponse`, `SlurmDepMatrixResponse`, `SlurmPushOperation`, `SlurmMungeKeyResponse`.

### Slurm tail shipped (#91)

- [x] **TAIL-1** Config editor sheet gains "Preview" tab — enter node ID → renders config via `GET /slurm/configs/{filename}/render/{node_id}` → read-only textarea.
- [x] **TAIL-2** Munge key "generate" / "rotate" panel on Status section, toggled by "Munge key" button (visible when module enabled).
- [x] **TAIL-3** Dep matrix section added to `/slurm` page — table of all version constraints from `GET /slurm/deps/matrix`.
- [x] **TAIL-4** Sync now returns push-op from `POST /slurm/sync`; polling drawer opens and polls `GET /slurm/push-ops/{op_id}` every 2s until completed/failed. Per-node results with file-level breakdown.

All four TAIL endpoints verified 401 (not 404) on cloner before scoping.

### KL verification (#90)

- **KL-VERIFY-1 PASS**: Controller node `cbf2c958` has roles `["controller","worker"]` — dual-role auto-assignment works. Sprint 11 carry-over note was stale. No re-implementation needed.
- **KL-VERIFY-2 PASS**: `POST /slurm/configs/reseed-defaults` correctly skips operator-customized files (`is_clustr_default=0`). slurm.conf has `ClusterName=test-cluster`, cluster-specific hostnames. Operator topology round-trips without intervention.
- **Stale notes**: Both Sprint 11 KL carry-over notes are stale and deleted (see above checkboxes).

### Tests

- Go: `internal/sysd/sysd_test.go` — ButtonState×4 states, PortInUseError, idempotency, FormatPort.
- Vitest: `sprint12-sysd-tail.test.ts` — 22 new tests covering ButtonState derivation, ui_buttons field, TAIL-1..4 response shapes, push-op poll stop condition. All 216 tests green.

### Definition of done

1. All Sprint 12 checkboxes ticked. CI green. Autodeploy on cloner picks up.
2. LDAP internal disable/enable cycle works without `port_in_use` (sysd.Disable now also disables unit, eliminating survival-across-reboot bug).
3. Slurm: Preview tab renders per-node config. Munge key generate/rotate works. Dep matrix loads. Sync now opens push-op drawer.

### Carry-overs to Sprint 13

- UI: Wire `ui_buttons` from `LDAPInternalStatusResponse` into identity.tsx button visibility (currently buttons are hardcoded based on `enabled` flag; with Sprint 12 server changes the 4-state rendering can now be driven by `ui_buttons`).
- LDAP: `slapdUptimeSec()` in `internal_routes.go` still queries `systemctl show` directly — migrate to `sysd.QueryStatus` or a dedicated uptime helper in Sprint 13.
- Any tangential items found during audit (LDAP project plugin per-group restrictions, etc.) remain deferred.
5. Tag `v0.6.0` after Sprint 10 ships green — Slurm surface is a substantive new top-level capability. Pipeline auto-fires.

---

## Sprint 17 — Fresh-VM Verification + LDAP/SSSD Matrix

**Carried from Sprint 16 #104 / #104a.** Requires a reproducible fresh Rocky 9 VM baseline in the lab.

### Prerequisite (infra)

- [ ] **INFRA-VM-1** Build a reusable Rocky 9 VM template (VMID 9001) from the minimal ISO with:
  - Single NIC (vmbr0, DHCP)
  - Root password `clustrtest123`
  - SSH root login enabled
  - `dnf-plugins-core`, `curl`, `wget` pre-installed
  - Procedure: use Proxmox console during Anaconda install (5 min interactive), then `qm template 9001`
  - **Rationale**: The kickstart-from-ISO automation fails on Rocky 9.7 minimal in this lab — Anaconda gets stuck in text mode with no disk writes even after cdrom-embedded ks.cfg. Root cause is Anaconda cdrom stage2 + inst.ks=cdrom:/ not advancing when virtio-net has no DHCP response available at kernel-cmdline phase. Needs interactive console to unblock. Once template exists, all future VM tests clone it in <30s.

### Task #104 — Fresh RPM install verification (Sprint 17)

**Depends on INFRA-VM-1.**

- [ ] **VM-104-1** Clone template → VMID 204 (`clustr-104-fresh`), boot, verify Rocky 9 reachable via SSH.
- [ ] **VM-104-2** Walk install sequence verbatim:
  ```
  sudo rpm --import https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr
  sudo dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/el9/clustr.repo
  sudo dnf install -y clustr-serverd
  sudo systemctl enable --now clustr-serverd
  sudo /usr/sbin/clustr-serverd bootstrap-admin
  ```
- [ ] **VM-104-3** Verify 6 surfaces render: Nodes, Images, Activity, Settings + Slurm + Identity (LDAP) tabs.
- [ ] **VM-104-4** Create LDAP user with empty `initial_password` → verify temp password panel appears in web UI.
- [ ] **VM-104-5** Build slurm bundle via web UI → verify `deps_matrix` auto-install runs via privhelper, no `unit_not_allowed` errors in server log.
- [ ] **VM-104-6** Node CRUD: register node via API, verify it appears in Nodes surface, delete it.
- [ ] **VM-104-7** File one Sprint 17/18 task per gap found. Use gap IDs: `GAP-104-N`.

**Known ahead of time (from pre-verification on cloner):**
- `pkg.sqoia.dev` GPG key and clustr.repo both reachable. Repo infrastructure is OK.
- `clustr-privhelper` setuid bit (4755) applied by post-install scriptlet — no manual fixup needed.

### Task #104a — LDAP/SSSD/cert chain verification matrix (Sprint 17)

**Depends on VM-104-2 completing successfully (LDAP enabled on fresh VM).**

For each row: mark PASS / FAIL / GAP-DOCUMENTED. File Sprint 17/18 task per FAIL or GAP.

| # | Check | How to verify | Status |
|---|---|---|---|
| 1 | Clean slapd bring-up | Fresh VM, click Enable → slapd starts first try, no manual fixup | pending |
| 2 | CA rotation mid-flight | Disable+wipe → Enable → CA regenerates → nodes' `ldap_tls_cacert` auto-updates AND sssd reconnects (or document gap) | pending |
| 3 | Service-bind credential rotation | Same flow with `service_bind_password` regen → nodes' sssd.conf auto-updates or surfaces drift | pending |
| 4 | Node enrollment cold | Fresh node, never seen LDAP → join cluster → sssd config baked correctly → `getent passwd <ldap-user>` works first try | pending |
| 5 | Node enrollment warm | Existing node, slapd state changed since last deploy → re-deploy → sssd updated atomically | pending |
| 6 | SSH login via pubkey | Set `sshPublicKey` on LDAP user → SSH with private key → login works | pending |
| 7 | SSH login via password | SSH with temp password from create flow → login works | pending |
| 8 | Sudo via LDAP group | Add user to LDAP "wheel" or sudoers group → `sudo -i` on node succeeds | pending |
| 9 | sssd offline cache | Cut node→slapd network → cached creds work for N minutes per pwd_policy | pending |

**Known gaps from Sprint 15 code review (pre-verification):**

- **GAP-104a-1 (CA rotation, row 2):** No automatic CA cert push to enrolled nodes on re-Enable. The `NodeConfig()` projection returns the new CA cert PEM, but it is only pushed to nodes on the next deploy/reimage cycle, not proactively. Nodes sssd.conf holds a stale `ldap_tls_cacert` until re-deployed. Severity: HIGH — breaks LDAP auth on all nodes silently after any CA rotation.
- **GAP-104a-2 (service-bind drift, row 3):** Same path as CA rotation. `service_bind_password` is pushed to nodes only on deploy. No push-on-change mechanism. Severity: MEDIUM — auth works until slapd's ppolicy locks the stale entry (if lockout configured); detectable via health check failure.
- **GAP-104a-3 (node enrollment cold, row 4):** sssd.conf template uses `ldap_uri = ldaps://<hostname>:636`. On a fresh node before DNS resolves the clustr-serverd hostname, sssd fails to start. Severity: MEDIUM — depends on DNS setup; may work if IP used instead of hostname.
- **GAP-104a-4 (SSH pubkey, row 6):** `sshPublicKey` attribute support requires `openssh-lpk` schema and `AuthorizedKeysCommand` configured in sshd_config on each node. The schema embed lands in Sprint 15 (#97), but sshd_config injection is not implemented in the deploy finalize step. Severity: LOW for MVP; documented in KL.

**Sprint 17/18 task IDs (to be formalized when sprint opens):**

| Gap ID | Sprint | Priority | Title |
|---|---|---|---|
| GAP-104a-1 | 17 | HIGH | Push updated CA cert to enrolled nodes on LDAP re-Enable/CA-rotate |
| GAP-104a-2 | 17 | MEDIUM | Detect and push service-bind password drift to enrolled nodes |
| GAP-104a-3 | 18 | MEDIUM | Use IP in sssd ldap_uri if hostname DNS resolution is uncertain at deploy time |
| GAP-104a-4 | 18 | LOW | Inject AuthorizedKeysCommand in sshd_config during node deploy for SSH pubkey auth |
| INFRA-VM-1 | 17 | BLOCKER | Build reusable Rocky 9 VM template for lab verification runs |

---

## Sprint 19 backlog — surfaced 2026-04-29

### #113 — Split posix_id_allocator into LDAP-user range and system-account range (HIGH)

**Bug evidence (founder-surfaced 2026-04-29):**
On cloner, `system_accounts` row for `munge` shows UID **10003** with GID **996**. UID 10003 is in the LDAP user range (10000–60000). GID 996 is correctly in the FHS system range (<1000) — that came from DNF's `groupadd -r munge`, not clustr.

The asymmetry exposes a Sprint 13 (#96) design oversight: `internal/posixid/allocator.go` has a single `AllocateUID()` reading one range from `posixid_config`. Both `internal/ldap` user creation and `internal/db/sysaccounts.CreateAccount` call it, so daemon accounts end up in user-id space.

**Why this matters:**
- FHS/Linux convention reserves UID/GID <1000 for system accounts; SSSD's default `min_id=1000` excludes them from login enumeration.
- UID drift risk: if a node's local `dnf install munge` picks 996 but clustr's record says 10003, manifest push creates same-name/different-UID divergence and file-ownership chaos.
- Conceptually wrong: human users and daemon accounts share an identity allocator that has no notion of the distinction.

**Fix:**
1. Migration adds `kind` column to `posixid_config` with two seeded rows: `kind=ldap_user` (10000–60000), `kind=system_account` (200–999).
2. `posixid.AllocateUID(kind)` and `AllocateGID(kind)` take the kind explicitly. No default.
3. `internal/ldap` user-create calls with `kind=ldap_user`; `sysaccounts.CreateAccount` calls with `kind=system_account`.
4. **Reconciliation migration for existing rows:** any `system_accounts` row with UID ≥1000 gets re-synced to its on-node UID (read via clientd `getent passwd <name>` push-back), not blindly re-allocated. Munge specifically lands at whatever DNF put it on the node (likely 996).
5. Anti-regression test: assert `system_accounts.uid < 1000` invariant on every CreateAccount path.
6. Documented in feedback memory: system accounts never share id-space with LDAP users.

**Owner:** Richard scopes (15 min) → Dinesh implements. Queued behind Sprint 18 landing green.

---

## Sprint 18 verification matrix results (2026-05-01)

**Branch:** `main` at `ff09c17` (v0.9.0 / Sprint 16+17 bundle)
**Server:** clustr-serverd v0.2.0-501-ga4af5df on cloner (192.168.1.151)
**Test nodes:** slurm-controller (10.99.0.100, VMID 201), slurm-compute (10.99.0.101, VMID 202)
**Template build:** VMID 299 (`rocky9-minimal-template`), install in progress at time of matrix run

### Matrix results

| # | Check | Result | Evidence |
|---|---|---|---|
| 1 | Clean slapd bring-up | BLOCKED | Template VM (299) install still running; existing cloner has LDAP already enabled (no clean reset available without data loss). Re-test in Sprint 19 once template is usable. See GAP-S18-4. |
| 2 | CA rotation mid-flight | BLOCKED | Depends on #109/#110 (Dinesh). Not retested. |
| 3 | Service-bind credential drift | BLOCKED | Depends on #109/#110 (Dinesh). Not retested. |
| 4 | Node enrollment cold | BLOCKED | Depends on #112 (Dinesh). Not retested. |
| 5 | Node enrollment warm | PASS | `getent passwd sprint13test` → `sprint13test:*:10000:10000:sprint13test:/home/sprint13test:/bin/bash` on slurm-controller (reimaged 2026-04-30T22:44, sssd active). No manual fixup. |
| 6 | SSH login via pubkey | BLOCKED | Depends on #109 (Dinesh). Not retested. |
| 7 | SSH login via password | PASS | `sshpass -p 'TestPass123!' ssh sprint13test@10.99.0.100` → `uid=10000(sprint13test)` + `SSH_SUCCESS`. No sshd_config fixup needed. Minor: no homedir creation (see GAP-S18-5). |
| 8 | Sudo via LDAP group | FAIL | `POST /api/v1/ldap/sudoers/members {uid: sprint13test}` → LDAP Error 32 "No Such Object". Group `clonr-admins` referenced by `sudoers_group_cn` does not exist in LDAP DIT (only user private groups present). See GAP-S18-1. |
| 9 | sssd offline cache | PASS | iptables blocked port 636 on node; `getent passwd sprint13test` returned cached entry; `sshpass` SSH returned "Authenticated with cached credentials, expires Fri May 8 2026"; `iptables -D` unblocked cleanly. `offline_credentials_expiration = 7` (7 days) confirmed in sssd.conf. |

### New gaps surfaced

**GAP-S18-1 — sudoers group `clonr-admins` missing from LDAP DIT (HIGH)**
- `POST /api/v1/ldap/sudoers` returns LDAP 32 "No Such Object" because the `clonr-admins` group was never seeded in `ou=groups,dc=cluster,dc=local`.
- Root cause: the LDAP DIT seed (`Manager.Enable()` / `slapd-seed.ldif.tmpl`) does not create the sudoers group. The group name was `clonr-admins` pre-rename; the server config still references it.
- The node has `/etc/sudoers.d/clonr-admins` file (deployed by the provisioning pipeline with `%clonr-admins ALL=(ALL) NOPASSWD:ALL`) but the LDAP group it references does not exist, so sudo silently does nothing.
- Fix: `Manager.SeedDIT()` must create the `cn=<sudoers_group_cn>` entry in `ou=groups` during enable/re-enable. Also consider renaming `clonr-admins` → `clustr-admins` at the same time.
- **Owner:** Dinesh. Sprint 19.

**GAP-S18-2 — sudoers group name still `clonr-admins` (LOW)**
- `GET /api/v1/ldap/sudoers/status` returns `group_cn: "clonr-admins"`. Project was renamed `clonr` → `clustr` in 2026-04-25.
- The default sudoers group CN should be `clustr-admins`. This is a cosmetic rename but consistent with the product name.
- Fix: default config value + DIT seed. Migration for existing installs: check if old group exists, rename if so.
- **Owner:** Dinesh. Sprint 19 (bundle with GAP-S18-1).

**GAP-S18-3 — pam_mkhomedir not configured; LDAP user homedirs not created on first login (LOW)**
- SSH login succeeds but logs: `Could not chdir to home directory /home/sprint13test: No such file or directory`.
- sssd is configured but the node deploy does not add `pam_mkhomedir.so` to the PAM session stack or set `homedir_substring` handling.
- Fix: add `pam_mkhomedir.so` to `/etc/pam.d/sshd` (or a common file) in the node deploy finalize step.
- **Owner:** Dinesh. Sprint 19.

**GAP-S18-4 — Row 1 (clean slapd bring-up) not verifiable without fresh server state (MEDIUM)**
- Testing "first Enable ever, no prior LDAP state" requires a clustr-serverd instance that has never run `Enable()`. The live cloner has had LDAP enabled for weeks; wiping it would destroy production LDAP data.
- The Rocky 9 template (VMID 299) is being built for this. Once the template is ready, clone it, install clustr-serverd from pkg.sqoia.dev, run `bootstrap-admin`, open LDAP config, click Enable — that is the correct Row 1 test.
- Action for Sprint 19: after VMID 299 template is confirmed (agent up, SSH accessible), run the Row 1 matrix item as the first clustr action on a clean install.
- **Owner:** Gilfoyle. Sprint 19.

**GAP-S18-5 — Template VM 299 build status (IN PROGRESS)**
- Rocky 9.7 minimal + oemdrv kickstart install started at 06:43 local time on Proxmox host. Disk partitions not yet written as of matrix run (installer countdown to first package install).
- oemdrv kickstart (`ks-rocky9-template.iso`, VMID 299 ide3) delivers kickstart without network access — avoids the virtio-net stall that blocked Sprint 16.
- Once install completes + agent responds: run `qm stop 299; qm set 299 --boot order=scsi0 --ide2 none,media=cdrom --ide3 none,media=cdrom; qm template 299`.
- Template documented in `reference_proxmox.md`. VMID 299, storage `local-lvm`, 2 vCPU / 2 GB RAM / 32 GB.
- **Action for Sprint 19:** after template confirmed, run INFRA-VM-1 re-verification and Row 1 matrix item. Mark INFRA-VM-1 done.

### Sprint 19 task IDs from this run

| Gap ID | Priority | Title |
|---|---|---|
| GAP-S18-1 | HIGH | Seed `clonr-admins` (or `clustr-admins`) LDAP group during `Manager.Enable()` |
| GAP-S18-2 | LOW | Rename default sudoers group CN `clonr-admins` → `clustr-admins` |
| GAP-S18-3 | LOW | Add `pam_mkhomedir.so` to node PAM deploy step |
| GAP-S18-4 | MEDIUM | Re-run Row 1 (clean slapd bring-up) on fresh clustr install from VMID 299 template |
| GAP-S18-5 | MEDIUM | Complete VMID 299 Rocky 9 template build + convert to template + update reference_proxmox.md |
