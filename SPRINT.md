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
- [ ] **DEPLOY-1** Verify autodeploy on `cloner` (192.168.1.151) builds and serves the new app
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
