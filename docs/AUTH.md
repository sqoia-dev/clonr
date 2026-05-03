# Authentication

clustr supports three authentication surfaces, each with different semantics
because of the underlying transport constraints.

## Auth matrix

| Surface | Mechanism | 401 behavior | Revoke effect |
|---------|-----------|--------------|---------------|
| Web UI | HTTP-only `clustr_session` cookie (stateless HMAC-SHA256 token) | `SESSION_EXPIRED_EVENT` dispatched in client; user redirected to login | No server-side session store — revoke takes effect at next request only if the session secret has been rotated. Disable the user account to block re-login. |
| CLI | `Authorization: Bearer <token>` header | Command exits non-zero with the API's error JSON (`code: "key_revoked"` or `"key_expired"`) | API key disabled in DB; subsequent commands fail at next call |
| WebSocket (browser) | `?token=<bearer-token>` query param | WS upgrade fails with 401; client reconnects via the existing backoff helper | Same as the bearer token that was lifted into `?token=` |

## Why each is the way it is

### Web UI: cookie + stateless session token

The web UI never sees an API key directly. Operators log in with username and
password (against the local `users` table — never LDAP). The server issues an
HTTP-only `clustr_session` cookie containing a stateless, HMAC-SHA256-signed
token (ADR-0006/0007). Subsequent requests carry the cookie automatically.

**Session lifetime:** 12-hour absolute TTL with a sliding window. If a session
has not been touched for more than 30 minutes, the server re-signs and
re-issues the cookie, resetting the absolute TTL from the current time. Active
sessions therefore never expire mid-use.

**Session secret:** Set `CLUSTR_SESSION_SECRET` (hex-encoded) in
`/etc/clustr/secrets.env` for sessions to survive server restarts. If this
variable is not set, a random ephemeral secret is generated on startup and a
warning is logged — all existing sessions are invalidated on every restart.

Rationale: keeps the bearer secret out of JavaScript memory, makes browser
back/forward and tab duplication just work, and requires no session store.

### CLI: bearer header

The CLI is invoked from terminals, scripts, and CI systems. Cookies aren't
practical there. Bearer token in `Authorization` header is the standard
non-browser auth scheme. Configure via `--token <key>`, `--server <url>`, or
`CLUSTR_TOKEN` / `CLUSTR_SERVER` env vars.

Token format: `clustr-admin-<hex>` (admin scope) or `clustr-node-<hex>` (node
scope). The server strips the typed prefix and verifies a SHA-256 hash against
the `api_keys` table.

### WebSocket: ?token= query (browser only)

Browser WebSocket clients cannot set custom HTTP headers on the upgrade
request. The `wsTokenLift` middleware hoists a `?token=` query parameter into
`Authorization: Bearer <token>` *before* `apiKeyAuth` runs, so the bearer-auth
path treats it identically to a normal API call.

**This fallback is enabled only on WS routes** (`ShellWS` at
`/images/{id}/shell-session/{sid}/ws` and `ConsoleWS` at
`/console/{node_id}`). Plain HTTP endpoints use `extractBearerToken`, which
reads only the `Authorization` header and never accepts `?token=` — tokens
leak via access logs, browser history, proxy logs, and referrer headers.

## Session roles

Web UI sessions carry a role that maps to an auth scope:

| Role | Scope | Access |
|------|-------|--------|
| `admin` | `KeyScopeAdmin` | Full access |
| `operator` | `KeyScopeOperator` | Node/group operations; gated by group membership |
| `readonly` | `"readonly"` sentinel | Read-only; blocked on all state-changing routes |
| `pi` | `"pi"` sentinel | PI portal routes only |
| `director` | `"director"` sentinel | Director portal routes only |
| `viewer` | `"viewer"` sentinel | Most restricted; portal read access only |

## Operator troubleshooting

### "I'm logged out unexpectedly in the web UI"

A `SESSION_EXPIRED_EVENT` was dispatched by `apiFetch` in `web/src/lib/api.ts`.
Causes:

- Session cookie expired (12-hour TTL; sliding, but requires active use within
  each 30-minute window to keep resetting)
- Server restarted without a persistent `CLUSTR_SESSION_SECRET` set —
  the ephemeral secret changed, invalidating all outstanding cookies. Fix:
  set a stable secret in `/etc/clustr/secrets.env` and restart once.
- The session cookie was cleared by the browser (privacy mode, manual clear,
  or the server explicitly cleared it after HMAC verification failure)

### "My CLI commands suddenly fail with 401"

The bearer token is invalid or revoked. Possible causes:

- Token was disabled in the web UI's API Keys panel (`code: "key_revoked"`)
- Token has passed its expiry date (`code: "key_expired"`)
- `CLUSTR_TOKEN` env var is wrong, unset, or points at the wrong key format
- `CLUSTR_SERVER` points at a different server instance

### "WebSocket sessions disconnect after some seconds"

Not auth-related unless the disconnect coincides with a 401 log entry. If the
upgrade itself is accepted (101 Switching Protocols), subsequent disconnects
are typically caused by network timeouts or the server-side shell/console
session being closed. Check `clustr-serverd` logs for the specific WS handler.

### "All users are being logged out after a server restart"

`CLUSTR_SESSION_SECRET` is not set. Generate a persistent secret:

```bash
openssl rand -hex 64 | sed 's/^/CLUSTR_SESSION_SECRET=/' \
  | sudo tee -a /etc/clustr/secrets.env > /dev/null
sudo systemctl restart clustr-serverd
```

Users will need to log in once after this restart, then sessions will survive
future restarts.

## See also

- `internal/server/middleware.go` — `extractBearerToken` (strict header-only)
  and `wsTokenLift` (query-param hoist for WS routes)
- `internal/server/session.go` — `signSessionToken`, `validateSessionToken`,
  session TTL constants, and ADR-0006/0007 notes
- `web/src/lib/api.ts` — `SESSION_EXPIRED_EVENT` dispatch on 401
- `cmd/clustr/main.go` — `--token` / `--server` flag and `CLUSTR_TOKEN` /
  `CLUSTR_SERVER` env var resolution
