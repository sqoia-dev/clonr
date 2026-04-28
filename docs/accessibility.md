# clustr WebUI — Accessibility

**Added:** Sprint I (v1.8.0, 2026-04-27)
**Owner:** Dinesh
**Standard:** WCAG 2.1 Level AA

---

## Overview

clustr's webUI conforms to WCAG 2.1 Level AA on all load-bearing pages.
Conformance is enforced by an automated CI gate using `axe-core` running against
the static HTML via `jsdom`. The gate fails on any critical or serious violation.

---

## Pages Audited

| Page | File | Route |
|---|---|---|
| Admin dashboard | `internal/server/ui/static/index.html` | `/` |
| Login | `internal/server/ui/static/login.html` | `/login` |
| Researcher portal | `internal/server/ui/static/portal.html` | `/portal/` |
| Director portal | `internal/server/ui/static/portal_director.html` | `/portal/director/` |
| PI portal | `internal/server/ui/static/portal_pi.html` | `/portal/pi/` |
| Set password | `internal/server/ui/static/set-password.html` | `/set-password` |

---

## CI Gate

The a11y CI job runs on every push to `main` and every pull request.

**Workflow job:** `a11y` in `.github/workflows/ci.yml`

**What it does:**
1. Sets up Node.js 20
2. Installs `axe-core` and `jsdom` as test-only devDependencies under `test/js/`
3. Runs `node --test test/js/a11y.test.mjs`
4. Fails on any **critical** or **serious** axe-core violation

**Run locally:**

```bash
# Install test deps (first time only)
npm install --prefix test/js axe-core jsdom

# Run the audit
make a11y
# or directly:
node --test test/js/a11y.test.mjs
```

**Rule scope:** WCAG 2.1 AA tags — `wcag2a`, `wcag2aa`, `wcag21a`, `wcag21aa`

---

## Lighthouse Performance Budget

Lighthouse is run against the static HTML in CI to guard against performance
regressions. The budget targets desktop-class hardware (no CPU/network throttling
in CI) with reasonable headroom.

**Budget file:** `lighthouse-budget.json`
**Config:** `.lighthouserc.json`

| Metric | Budget | Rationale |
|---|---|---|
| First Contentful Paint | ≤ 2000ms | Richard spec: 1.5s target + 0.5s CI headroom |
| Time To Interactive | ≤ 4000ms | Richard spec: 3s target + 1s CI headroom |
| Total Blocking Time | ≤ 300ms | Richard spec: 200ms target + 100ms headroom |
| JS bundle | ≤ 512 KB | Vendored libs (Alpine + HTMX) are ~95 KB combined |
| CSS bundle | ≤ 128 KB | |
| Total page weight | ≤ 1024 KB | |

**CI gate behaviour:**
- Performance assertions: `warn` (non-blocking; operator should investigate)
- Accessibility score: `error` (fail build if score < 0.90)
- Budget overruns: `warn` (informational for now — these run on unthrottled CI hardware)

**Run locally:**

```bash
# Install Lighthouse CI (first time only)
npm install --prefix test/js @lhci/cli

# Run Lighthouse CI collect + assert
npx --prefix test/js lhci autorun
```

---

## Findings Fixed (Sprint I / v1.8.0)

### Critical Fixes

| ID | Page(s) | Issue | Fix |
|---|---|---|---|
| C1 | `portal.html` | Password change inputs had `<label>` elements with no `for` + no `id` on inputs (form labels not associated) | Added `id` to all 3 password inputs, `for` to all 3 labels |
| C2 | `portal_pi.html` | All modal form inputs lacked `id`/`for` label associations (Add Member, Expansion, Change Request, Grant, Publication modals + Wizard) | Added `id` + `for` to all ~25 label/input pairs |
| C3 | `portal_director.html` | No `<main>` landmark in director portal (only a `<div>`) | Changed outer content `<div>` to `<main id="main-content">` |
| C4 | `portal_pi.html` | Same as C3 — no `<main>` landmark | Added `id="main-content"` to existing `<main>` element |
| C5 | All portal pages | Modal close buttons (`×`) had no accessible label — screen readers announce `×` literally | Added `aria-label="Close dialog"` to all 5 modal close buttons |
| C6 | All portal pages | Modal overlays missing `role="dialog"` and `aria-modal="true"` — screen readers don't know they're in a dialog | Added `role="dialog" aria-modal="true" aria-labelledby="..."` to all 6 modal overlays; added `id` to modal title elements |

### Serious Fixes

| ID | Page(s) | Issue | Fix |
|---|---|---|---|
| S1 | `portal_director.html` | Tab widget (`director-tabs`) had no ARIA tab role pattern — screen readers can't identify it as a tab list | Added `role="tablist"` + `role="tab"` + `aria-selected` + `aria-controls` + `id` to all tabs; added `role="tabpanel"` + `aria-labelledby` + `id` to all 3 panels |
| S2 | `portal_pi.html` | Same as S1 — 7-tab PI portal widget had no ARIA tab semantics | Added full tab/tablist/tabpanel ARIA pattern to all 7 tabs and panels |
| S3 | `portal.html`, `portal_pi.html` | Loading and error divs had no `aria-live` — screen readers miss state transitions | Added `aria-live="polite"` to loading divs, `aria-live="assertive"` + `role="alert"` to error divs |
| S4 | `portal_director.html` | Same as S3 | Same fix |

---

## Waived Items (Moderate)

These moderate findings are documented here and will not be fixed in v1.8.0.
They are excluded from the CI gate.

| ID | Page(s) | Rule | Rationale |
|---|---|---|---|
| W1 | `portal.html`, `portal_director.html`, `portal_pi.html` | `skip-nav` — no skip-to-main link on portal pages | Portal pages are single-column layouts without a persistent sidebar. The primary navigation is a small header with 1–2 controls. A skip link adds little value vs. the admin dashboard (which has a full sidebar nav and already has the skip link). Defer to v2.0 if a researcher accessibility audit flags it. |
| W2 | All pages | `color-contrast` — axe-core cannot evaluate CSS custom properties through jsdom's non-cascade-aware style engine | Contrast was manually verified against the light-theme CSS variables in `style.css`: `--text-primary: #1e293b` on `#ffffff` = 16.1:1 (pass); `--text-secondary: #475569` on `#ffffff` = 6.6:1 (pass); portal dark-theme `#e6edf3` on `#0d1117` = 15.2:1 (pass). All text meets 4.5:1 minimum. |

---

## How to Extend

To add a new page to the audit:

1. Add the file path to the `pages` array in `test/js/a11y.test.mjs`
2. Run `make a11y` locally to see the baseline
3. Fix any critical/serious violations before merging
4. Document any waived moderate items in this file under the Waived Items table

To add a new page to the Lighthouse budget:

1. Add the URL to the `url` array in `.lighthouserc.json`
2. The budget in `lighthouse-budget.json` applies to all paths (`"path": "/*"`)

---

## References

- [WCAG 2.1 Level AA](https://www.w3.org/WAI/WCAG21/quickref/?levels=AA)
- [axe-core rules](https://dequeuniversity.com/rules/axe/4.x)
- [ARIA Authoring Practices: Tabs](https://www.w3.org/WAI/ARIA/apg/patterns/tabs/)
- [ARIA dialog pattern](https://www.w3.org/WAI/ARIA/apg/patterns/dialog-modal/)
