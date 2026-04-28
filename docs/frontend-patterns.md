# clustr Frontend Patterns — Alpine.js + HTMX

**Added:** Sprint B.5 (2026-04-27)  
**Owner:** Dinesh  
**Decisions:** D21 (re-rule), D23 (framework choice)  
**Status:** Pilot complete. DHCP Leases page migrated. Sprint C expands adoption.

---

## Overview

clustr's webui uses a **layered approach**: vanilla JS pages keep working unchanged; Alpine.js and HTMX are adopted incrementally, one surface at a time. No big-bang rewrite. No build step. No npm.

Both libraries are vendored under `internal/server/ui/static/vendor/` and embedded in the server binary via Go's `embed.FS`. They are loaded as regular `<script>` tags in `index.html` before `app.js`.

**When to reach for each tool:**

| Situation | Tool |
|---|---|
| Client-side reactive state (loading/error/data, form state, modal show/hide) | **Alpine.js** |
| Server-driven partial HTML swaps (filter/paginate, polling, live updates) | **HTMX** |
| Hash routing, Auth bootstrap, SSE log streams, API wrapper | **Vanilla JS** (keep as-is) |
| New greenfield page with complex state | **Alpine** first; add HTMX for any polling/swap pieces |

---

## Vendored Libraries

| Library | Version | File | SHA256 |
|---|---|---|---|
| Alpine.js | 3.15.11 | `vendor/alpinejs/alpine-3.15.11.min.js` | `beeba63d08956f64fa060f6b6a29c87a341bf069fb96c9459c222c6fd42e58ae` |
| HTMX | 2.0.9 | `vendor/htmx/htmx-2.0.9.min.js` | `57d9191515339922bd1356d7b2d80b1ee3b29f1b3a2c65a078bb8b2e8fd9ae5f` |

Integrity manifest: `internal/server/ui/static/vendor/VENDOR-CHECKSUMS.txt`

Served at: `/ui/vendor/alpinejs/alpine-3.15.11.min.js` and `/ui/vendor/htmx/htmx-2.0.9.min.js`

**To update a library:** download new minified file, cross-verify SHA256 against two CDNs (jsdelivr + unpkg), update `VENDOR-CHECKSUMS.txt` and this file and `docs/decisions.md` D23. Commit as a dedicated PR.

---

## Alpine.js Conventions

### Component factory pattern

Alpine components in clustr are **factory functions** registered on `window`:

```js
// Global factory function — Alpine finds this via x-data="myComponent()"
function myComponent() {
    return {
        // State
        loading: true,
        error:   null,
        items:   [],

        // Lifecycle — called automatically by x-init
        async init() {
            await this.refresh();
        },

        // Methods
        async refresh() {
            this.loading = true;
            this.error   = null;
            try {
                const data  = await API.someEndpoint();
                this.items  = data.items || [];
            } catch (err) {
                this.error  = 'Failed to load: ' + err.message;
            } finally {
                this.loading = false;
            }
        },
    };
}
```

In the template (rendered by `App.render()` as an HTML string):

```html
<div x-data="myComponent()" x-init="init()">
    <div x-show="loading" class="loading"><div class="spinner"></div>Loading…</div>
    <div x-show="!loading && error" class="alert alert-error" x-text="error"></div>
    <div x-show="!loading && !error">
        <!-- page content here -->
    </div>
</div>
```

**Why factory functions (not inline `x-data="{}"`):** factory functions live in `app.js`, are testable in isolation, keep the HTML string readable, and allow sharing logic between the factory and vanilla callers.

### x-data scope rules

- **One top-level `x-data` per page.** The root div owns all page state. Do not nest `x-data` components unless there is a clear sub-component reason.
- **Global helpers are accessible inside Alpine expressions** as long as they are defined before Alpine initialises (i.e., defined in a script that runs before Alpine runs). `fmtRelative`, `fmtBytes`, `escHtml` are all defined in `app.js`/`logs.js` and are available. **However** — expose them explicitly as component methods to make the dependency readable:

```js
// In the component factory, re-expose the global:
fmtRelative(ts) { return fmtRelative(ts); },
```

This makes it clear inside Alpine template expressions that the method comes from the component scope.

### Safe interpolation — no manual escaping

Alpine's `x-text`, `:href`, `:title`, `:class`, `x-bind:*` never call `innerHTML`. They are safe by default:

```html
<!-- Safe: Alpine auto-escapes -->
<span x-text="lease.hostname"></span>
<a :href="'#/nodes/' + lease.node_id" x-text="lease.hostname"></a>

<!-- NEVER do this — innerHTML is unsafe -->
<span x-html="lease.hostname"></span>  <!-- x-html is innerHTML — only for trusted server HTML -->
```

`x-html` is available but should only be used for HTML returned from the server (which is sanitised server-side). Never use `x-html` for user-supplied string fields.

### Conditional rendering: x-show vs x-if

| Directive | Behaviour | When to use |
|---|---|---|
| `x-show` | `display:none` — element stays in DOM | Loading/error/empty states; things that toggle frequently |
| `x-if` | Adds/removes from DOM | Content that is never shown to a role; one-time conditional |

Prefer `x-show` for toggling states on the same page. It avoids Alpine re-mounting the subtree on each toggle, which means no re-fetch side-effects.

### List rendering with x-for

Always use `<template x-for>`, not `x-for` on a visible element:

```html
<template x-for="item in items" :key="item.id">
    <tr>
        <td x-text="item.name"></td>
        <td x-text="item.role"></td>
    </tr>
</template>
```

`:key` should be a stable unique identifier (MAC address, ID, etc.). This gives Alpine's differ a hint for efficient DOM updates on refresh.

### Dynamic CSS classes with :class

For state-dependent badge classes:

```html
<span :class="stateBadgeClass(item.state)" x-text="stateBadgeLabel(item.state)"></span>
```

Where `stateBadgeClass` and `stateBadgeLabel` are methods on the Alpine component:

```js
stateBadgeClass(state) {
    const map = { 'active': 'badge badge-deployed', 'failed': 'badge badge-error' };
    return map[state] || 'badge badge-neutral';
},
stateBadgeLabel(state) {
    const map = { 'active': 'Active', 'failed': 'Failed' };
    return map[state] || (state || 'Unknown');
},
```

---

## HTMX Conventions

HTMX is used for **server-driven partial HTML swaps** — cases where the server returns an HTML fragment that replaces a target element. This is the pattern for:

- Audit log filter/paginate (Sprint C)
- Dashboard Anomaly card periodic refresh (Sprint C)
- Any table that benefits from server-side filtering without a full page re-render

### Content negotiation pattern

The server endpoint must detect whether the request is from HTMX and return HTML vs JSON accordingly:

```go
func (h *MyHandler) List(w http.ResponseWriter, r *http.Request) {
    items, _ := h.DB.List(r.Context())
    if r.Header.Get("HX-Request") == "true" {
        // Return HTML fragment for HTMX swap
        w.Header().Set("Content-Type", "text/html")
        renderPartial(w, items)
        return
    }
    // Return JSON for API consumers
    writeJSON(w, http.StatusOK, api.ListResponse{Items: items})
}
```

The `HX-Request: true` header is set automatically by HTMX on all its requests.

### Basic HTMX swap

```html
<!-- Button triggers a GET and swaps the result into #target-table -->
<button hx-get="/api/v1/audit?actor=root"
        hx-target="#audit-table-body"
        hx-swap="innerHTML">
    Filter
</button>

<tbody id="audit-table-body">
    <!-- HTMX swaps <tr>…</tr> rows here -->
</tbody>
```

### Polling

```html
<!-- HTMX polls every 30 seconds and swaps the returned HTML fragment -->
<div id="anomaly-card"
     hx-get="/api/v1/dashboard/anomalies/partial"
     hx-trigger="every 30s"
     hx-swap="outerHTML">
    <!-- initial content rendered server-side or by vanilla JS on first load -->
</div>
```

### Mixing Alpine and HTMX on the same element

This is supported and expected. Alpine manages local state; HTMX manages server swaps:

```html
<div x-data="{ open: false }"
     hx-get="/api/v1/items"
     hx-trigger="load"
     hx-target="this"
     hx-swap="innerHTML">
    <!-- HTMX populates this div on load; Alpine manages the open state -->
    <button @click="open = !open">Toggle</button>
    <div x-show="open">…</div>
</div>
```

**Gotcha:** When HTMX swaps content into a div that contains Alpine components, Alpine needs to process the new DOM. HTMX fires an `htmx:afterSwap` event you can listen to if re-initialisation is needed. In most cases Alpine's MutationObserver handles this automatically — test after each swap.

---

## Coexisting with Vanilla Pages

All existing vanilla JS pages continue working exactly as before. No changes are required to vanilla pages just because Alpine/HTMX are loaded. The libraries attach themselves globally (`window.Alpine`, `window.htmx`) but do not modify existing DOM unless they find `x-data` or `hx-*` attributes.

**Rule:** Alpine/HTMX attributes are only added to NEW pages or when an existing page is explicitly migrated. A page migration PR must:
1. Remove the vanilla `async Page.foo()` render logic
2. Replace with a factory function + Alpine template
3. Demonstrate functional parity (same data, same affordances)
4. Add or update a smoke test

---

## File naming and organisation

Until the Sprint B.5 module split lands (targeting `pages/*.js`), new Alpine component factories live in `app.js` near the `Pages.xxx()` function that renders them.

**Current convention (pre-module-split):**

```
app.js
  ├── Pages.dhcpLeases()           ← renders the Alpine root div
  └── dhcpLeasesComponent()        ← Alpine factory, defined near the renderer
```

**Post-module-split convention (Sprint C+):**

```
pages/dhcp.js
  ├── export function render(container) { ... }   ← renders into container
  └── function dhcpLeasesComponent() { ... }       ← Alpine factory, local to module
```

---

## Common gotchas

**1. CSP and inline event handlers**

The existing vanilla pages use `onclick="..."` inline handlers which require `unsafe-inline` in CSP. Alpine's `@click` directives also require `unsafe-inline` in the standard build. When CSP enforcement becomes a priority (D21 trigger 4), swap `vendor/alpinejs/alpine-3.x.x.min.js` for the CSP build (`alpinejs-csp`). The CSP build moves all expression evaluation to a shadow realm that does not need `unsafe-inline`. This is a one-file swap — no code changes needed.

**2. Alpine initialisation order**

Alpine must be loaded BEFORE `app.js`. The current `index.html` loads Alpine as a regular (non-defer) script immediately before `app.js`. If you move Alpine to `<head defer>`, Alpine's DOMContentLoaded fires after all body scripts — which may be too late if `app.js` renders an Alpine root during its own DOMContentLoaded handler. Keep Alpine non-defer, in the body, before `app.js`.

**3. x-show vs display:none in server-rendered HTML**

Alpine's `x-show` works by toggling `display:none`. If the initial HTML from `App.render()` has no explicit display setting, Alpine evaluates the expression immediately and hides/shows correctly. Do not pre-hide elements with `style="display:none"` when using `x-show` — Alpine manages this.

**4. `x-for` and `<template>` elements**

Always wrap `x-for` in a `<template>` tag, not on a `<tr>` directly. Some browsers do not support `x-for` on non-template elements inside `<tbody>`. The safe pattern:

```html
<tbody>
    <template x-for="item in items" :key="item.id">
        <tr>...</tr>
    </template>
</tbody>
```

**5. Attribute conflicts between Alpine and HTMX**

Alpine's `x-bind:*` and HTMX's `hx-*` don't conflict — they use different attribute namespaces. However, if HTMX swaps content that contains `x-data`, Alpine re-processes it automatically via its MutationObserver. If you see stale state after a swap, check that `x-data` is on the root of the swapped fragment, not on the swap target itself.

---

## Annotated example: DHCP Leases page

This is the Sprint B.5 pilot migration. Source: `Pages.dhcpLeases()` and `dhcpLeasesComponent()` in `app.js`.

### The factory function

```js
function dhcpLeasesComponent() {
    return {
        loading: true,   // spinner visible on mount
        error:   null,   // set on API failure
        leases:  [],     // populated from API.dhcp.leases().leases
        count:   0,

        async init() {
            await this.refresh();  // called by x-init on mount
        },

        async refresh() {
            this.loading = true;
            this.error   = null;
            try {
                const data  = await API.dhcp.leases();
                this.leases = (data && Array.isArray(data.leases)) ? data.leases : [];
                this.count  = (data && data.count != null) ? data.count : this.leases.length;
            } catch (err) {
                this.error = 'Failed to load DHCP allocations: ' + err.message;
            } finally {
                this.loading = false;
            }
        },

        stateBadgeClass(state) { /* maps state → CSS class */ },
        stateBadgeLabel(state) { /* maps state → label string */ },
        fmtRelative(ts) { return fmtRelative(ts); },  // expose global helper
    };
}
```

### Key template patterns

```html
<!-- Root: x-data names the factory, x-init calls init() -->
<div x-data="dhcpLeasesComponent()" x-init="init()">

    <!-- x-show for three mutually-exclusive states -->
    <div x-show="loading" class="loading">…</div>
    <div x-show="!loading && error" class="alert alert-error" x-text="error"></div>
    <div x-show="!loading && !error">

        <!-- Reactive subtitle: no string concatenation, just x-text expression -->
        <span x-text="count + ' node' + (count === 1 ? '' : 's')"></span>

        <!-- @click shorthand for x-on:click -->
        <button @click="refresh()">Refresh</button>

        <!-- x-for inside <template> for table rows -->
        <template x-for="lease in leases" :key="lease.mac">
            <tr>
                <!-- Conditional deep-link via x-show on sibling elements -->
                <td>
                    <a x-show="lease.node_id"
                       :href="'#/nodes/' + lease.node_id"
                       x-text="lease.hostname"></a>
                    <span x-show="!lease.node_id" x-text="lease.hostname"></span>
                </td>
                <!-- Dynamic class from component method -->
                <td>
                    <span :class="stateBadgeClass(lease.deploy_state)"
                          x-text="stateBadgeLabel(lease.deploy_state)"></span>
                </td>
                <!-- Safe attribute binding -->
                <td>
                    <span x-show="lease.last_seen_at"
                          :title="lease.last_seen_at"
                          x-text="fmtRelative(lease.last_seen_at)"></span>
                </td>
            </tr>
        </template>

    </div>
</div>
```

### What this replaces (vanilla pattern)

The vanilla version built the entire table as a template-literal string, called `App.render(htmlString)`, and called `App.setAutoRefresh()` to re-call the whole function every 30 seconds (which re-rendered the entire DOM).

The Alpine version:
- Renders once into the DOM
- Updates reactively when `this.leases` changes (only diffed rows re-render via x-for)
- The `App.setAutoRefresh()` call is retained to integrate with the router's cleanup

---

## Sprint C playbook

Sprint C (v1.2.0) expands Alpine+HTMX to:

1. **Researcher portal (`/portal/`)** — greenfield, full Alpine component, HTMX for Slurm partition status polling
2. **Audit log page rewrite** — HTMX filter/paginate (content negotiation on `GET /api/v1/audit`)
3. **Dashboard Anomaly card** — HTMX `hx-trigger="every 30s"` polling

For each migration:
- Write the component factory first, test it in isolation
- Replace `Pages.xxx()` with the Alpine render
- Add the server-side HTML partial endpoint (if HTMX)
- Verify all RBAC roles see correct data/controls
- Add a smoke test
