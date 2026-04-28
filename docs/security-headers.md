# Security Headers

Added in v1.5.0 (Sprint F). Served by `securityHeadersMiddleware` in
`internal/server/middleware.go`, applied globally via `r.Use()` in the router.

## Headers

| Header | Value | Purpose |
|---|---|---|
| `Content-Security-Policy` | See below | Prevents XSS, clickjacking, data injection |
| `X-Content-Type-Options` | `nosniff` | Prevents MIME-type sniffing |
| `X-Frame-Options` | `DENY` | Legacy clickjacking protection (redundant with CSP `frame-ancestors`) |
| `Referrer-Policy` | `same-origin` | No referrer sent on cross-origin requests |

## Content-Security-Policy

```
default-src 'self';
script-src 'self';
style-src 'self' 'unsafe-inline';
img-src 'self' data:;
connect-src 'self';
frame-ancestors 'none';
form-action 'self';
base-uri 'self'
```

**`script-src 'self'`** — All JavaScript must be loaded from the same origin as an
external file. No inline `<script>` bodies and no inline event handler attributes
(`onclick=`, `onsubmit=`, etc.) are allowed.

**`style-src 'self' 'unsafe-inline'`** — Inline styles are permitted (no nonces
implemented). External style sheets must be same-origin.

**`img-src 'self' data:`** — Images may be same-origin or `data:` URI (used for
favicon and inline SVGs).

**`frame-ancestors 'none'`** — The UI cannot be embedded in any `<frame>`,
`<iframe>`, `<object>`, or `<embed>`. Clickjacking is prevented at the policy level.

**`connect-src 'self'`** — XHR, `fetch`, WebSocket, and EventSource connections
must go to the same origin.

## Alpine.js CSP Build

The standard Alpine.js build uses `eval()` internally and requires
`script-src 'unsafe-eval'`. Sprint F switched to the `@alpinejs/csp` build
(`alpine-csp-3.15.11.min.js`) which evaluates `x-data` expressions in a shadow
realm without `eval`. This allows `script-src 'self'` to be enforced.

The CSP build is vendored at:
`internal/server/ui/static/vendor/alpinejs/alpine-csp-3.15.11.min.js`

SHA256: `24560d2a22fa5ec57384894527f4e0ed7c40aa33332030ef5934107f8e1c1e45`

See `internal/server/ui/static/vendor/VENDOR-CHECKSUMS.txt` for the full checksum
manifest.

## Inline Event Handler Removal

The admin SPA (`app.js`) had 300+ inline event handler attributes in template
literals (`onclick="Pages.foo()"`, etc.). These were renamed to `data-on-click=`,
`data-on-submit=`, `data-on-change=`, `data-on-input=` and dispatched by a central
`Delegate` object using a RegExp-based dispatch table. No `eval` is used.

Portal pages (`portal.html`, `portal_director.html`, `portal_pi.html`) had inline
`<script>` blocks. These were extracted to external `.js` files under
`internal/server/ui/static/js/`.

## CSP Regression Tests

Automated regression tests live at `test/js/csp-policy.test.mjs`. They verify:

- No inline `<script>` bodies in any served HTML file.
- No inline event handler attributes in any served HTML file.
- `securityHeadersMiddleware` declares `script-src 'self'` and `frame-ancestors 'none'`.
- `securityHeadersMiddleware` is wired into `server.go`.
- `index.html` loads the Alpine CSP build.
- The `/audit/export` route is registered.

Run locally: `node --test test/js/csp-policy.test.mjs`

CI: The `test` job in `.github/workflows/ci.yml` runs these tests on every push.
