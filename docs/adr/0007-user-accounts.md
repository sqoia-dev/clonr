# ADR-0007: User Accounts and First-Run Bootstrap

**Date:** 2026-04-13
**Status:** Accepted
**Amends:** ADR-0001 (additive — no regression to API key primitive), ADR-0006 (session payload extended, login endpoint extended)
**Last Verified:** 2026-04-14 — applies to clonr main @ 722c8f3

---

## Context

The current first-run experience is unshippable for a self-hosted product. An operator must SSH into the server and grep `journalctl` for a bootstrap admin key that is printed exactly once at startup. If missed, there is no recovery path short of rotating keys manually. There is also no concept of user identity: every admin API key is equivalent, audit logs have no human attribution, and there is no way to give an operator read-only or scoped access without handing them a full admin key.

ADR-0001 established two-scope API keys (`admin`, `node`). ADR-0006 established a stateless HMAC-signed browser session cookie. Both are correct primitives for their stated purposes and are not regressed here. This ADR adds a user account layer on top of them: a `users` table, a role model, a predictable first-run default credential, and user-scoped personal API keys. The result is a complete, auditable auth surface appropriate for a v1.0 self-hosted product.

---

## Decision

### 1. Users Table Schema

```sql
CREATE TABLE users (
    id                   TEXT PRIMARY KEY,
    username             TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash        TEXT NOT NULL,
    role                 TEXT NOT NULL CHECK(role IN ('admin', 'operator', 'readonly')),
    must_change_password INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL,
    last_login_at        INTEGER,
    disabled_at          INTEGER
);
```

`username` uses SQLite's `COLLATE NOCASE` to enforce case-insensitive uniqueness at the database level. Application code must normalize to lowercase on input before any comparison.

`password_hash` stores a bcrypt-encoded string (standard `$2b$` prefix). Bcrypt cost factor: **12**.

**Bcrypt over argon2:** argon2 is the stronger algorithm for new systems, but it requires either CGO (for the reference C library) or a pure-Go port with its own maintenance surface. clonr targets `CGO_ENABLED=0` static binaries (ADR-0004). The `golang.org/x/crypto/bcrypt` package is pure Go, already in the dependency graph via `golang.org/x/crypto`, and is entirely adequate for a self-hosted control plane that is not expected to sustain password-cracking attacks from the open internet. The 72-byte bcrypt input limit is a known caveat; passwords beyond 72 bytes are silently truncated. This must be documented in the password change UI.

`disabled_at`: soft-delete mechanism. A non-null value means the account is disabled; the user cannot log in but the record is preserved for audit history.

### 2. Role and Permission Mapping

Three roles map onto the existing two-scope enforcement in `pkg/server/middleware.go`:

| Role | Effective scope | What is permitted |
|---|---|---|
| `admin` | Equivalent to `scope=admin` | Everything: nodes, images, deploys, groups, API key management, user management, server settings |
| `operator` | Subset of `scope=admin` | Nodes (CRUD), reimages, deploys, images, logs, groups. NOT user management, API key management for others, or server settings. |
| `readonly` | Read-only subset of `scope=admin` | GET-only on nodes, deploys, images, logs. Cannot trigger any state-changing action. |

The existing `requireScope(adminOnly bool)` middleware in `middleware.go` is a two-value gate. To express three roles, this function is replaced by `requireRole(minimum Role)` which accepts `RoleAdmin`, `RoleOperator`, or `RoleReadonly` and enforces a hierarchy: admin > operator > readonly.

The `AuthContext` struct (currently `{Scope, KeyPrefix}`) gains a `UserID string` and `Role Role` field. When auth resolves via session cookie (user login path), `UserID` and `Role` are populated; `KeyPrefix` is empty. When auth resolves via Bearer token (API key path), `KeyPrefix` is populated; `UserID` and `Role` depend on whether the key is user-owned (see personal API keys below). Both paths produce a single `AuthContext` — all downstream handlers remain unaware of which source resolved it.

The `ctxKeyScope` context key remains for backward compatibility during the transition. It is set to `admin` for `admin` and `operator` session users (so existing `requireScope` calls still pass) and to a new internal value `readonly` for `readonly` users. Route groups that need finer operator/admin distinction use the new `requireRole` middleware.

Node-scoped keys (`scope=node`) are orthogonal to the user role system and are not affected.

### 3. First-Run Bootstrap

On server startup, before accepting traffic:

```
if count(users) == 0:
    insert user: username='clonr', password=bcrypt('clonr', cost=12), role='admin', must_change_password=1
    log.Warn "SECURITY: default credentials clonr/clonr are active — change password on first login"
```

The default user is created **only if the users table is completely empty**. If the table has any user (including a disabled one), no default is created. This prevents accidental re-creation after deliberate removal.

The existing `BootstrapAdminKey` function in `apikeys.go` is retained unchanged. It runs on the same startup pass. Sites that pre-configure API keys via environment variables (e.g., CI/CD bootstrap scripts) are unaffected.

The journalctl-grep bootstrap flow is deprecated but not removed in v1.0. It continues to work as a fallback for CLI-only deployments.

### 4. Login Flow Upgrade

#### Extended login endpoint

`POST /api/v1/auth/login` accepts two mutually exclusive forms:

**Form A — user credentials (primary, v1.0+):**
```json
{ "username": "alice", "password": "hunter2" }
```

**Form B — raw API key (deprecated, one-release transition window):**
```json
{ "key": "clonr-admin-<hex>" }
```

Form B logs a deprecation warning: `auth/login: key-based login is deprecated; use username+password`. Form B is removed in v1.1.

On successful Form A login:

1. Server looks up user by `LOWER(username)`, checks `disabled_at IS NULL`.
2. `bcrypt.CompareHashAndPassword(stored_hash, provided_password)`.
3. Updates `last_login_at`.
4. Builds session payload:

```json
{
  "sub":   "<user_id>",
  "role":  "admin",
  "iat":   <unix>,
  "exp":   <unix + 43200>,
  "slide": <unix>
}
```

The `kid` / `scope` fields from ADR-0006 are replaced by `sub` (user_id) and `role`. The HMAC signing and sliding expiry logic in `session.go` are unchanged; only the payload struct changes.

5. If `must_change_password=1`, response additionally sets:

```
Set-Cookie: clonr_force_password_change=1; HttpOnly; Secure; SameSite=Strict; Path=/; Max-Age=43200
```

The UI checks for this cookie on every page render and redirects to `/set-password` before rendering any other view. The session cookie is fully valid (the user is authenticated) but the UI enforces the password change gate client-side. Server-side, the `must_change_password` flag does not block API calls — this is intentional to allow the set-password endpoint to function without a special unauthenticated path.

#### Password change endpoint

```
POST /api/v1/auth/set-password
Authorization: session cookie (authenticated)

{ "current_password": "...", "new_password": "..." }
```

Server re-verifies `current_password` against the stored hash (prevents session hijack from changing password silently), bcrypt-hashes `new_password` at cost 12, updates `password_hash`, sets `must_change_password=0`, and issues a fresh session cookie with a new `iat`/`exp`. Clears `clonr_force_password_change` cookie.

### 5. Backward Compatibility with Raw-Key Login

- `POST /api/v1/auth/login` with `{"key": "..."}` continues to work for one release with a deprecation log line.
- The bootstrap admin API key continues to work as `Authorization: Bearer` on all API paths. ADR-0001 and ADR-0006 are not regressed.
- Personal API keys: users can generate their own API keys from the Settings page. This requires extending `api_keys` with an optional `user_id` foreign key:

```sql
ALTER TABLE api_keys ADD COLUMN user_id TEXT REFERENCES users(id);
```

When a user generates a personal key, its scope is capped to their role:
- `admin` user → `scope=admin` key
- `operator` user → operator-scoped key (a new `scope=operator` value, or admin scope with a role annotation — see open questions)
- `readonly` user → cannot generate API keys (keys are for automation; readonly sessions cover their use case)

- Audit attribution (`requested_by` field across the codebase): `user:<username>` when action came via session cookie, `key:<label>` when via Bearer token. This closes the audit gap identified in Sanjay rank-4.8.

### 6. Password Policy

- Minimum length: 8 characters.
- No maximum length enforced by the application. bcrypt silently truncates input beyond 72 bytes; this caveat is documented in the UI.
- No character class requirements (no symbol/digit gymnastics). Current NIST SP 800-63B guidance favors length over composition rules.
- No password history in v1.0. Single-operator and small-team deployments do not require it. Password history enforcement is deferred to v1.1.
- Passwords are not checked against breach databases in v1.0 (requires HTTPS egress to HaveIBeenPwned API, which conflicts with air-gapped HPC deployment targets).

### 7. User Management Endpoints (Admin-Only)

All routes are under `/api/v1/admin/users` and require `role=admin`.

| Method | Path | Action |
|---|---|---|
| `GET` | `/api/v1/admin/users` | List all users (id, username, role, must_change_password, created_at, last_login_at, disabled) |
| `POST` | `/api/v1/admin/users` | Create user: `{username, password, role}` |
| `PUT` | `/api/v1/admin/users/{id}` | Update role or disable: `{role?, disabled?}` |
| `POST` | `/api/v1/admin/users/{id}/reset-password` | Admin sets temp password: `{password}`, sets `must_change_password=1` |
| `DELETE` | `/api/v1/admin/users/{id}` | Soft delete: sets `disabled_at=now()` |

**Last-admin guard:** `PUT` and `DELETE` must check that the operation would not leave zero enabled admin users. The pattern mirrors the existing last-admin-key guard in the API key management handlers. The check is: `count(users WHERE role='admin' AND disabled_at IS NULL) > 1` before proceeding with a disable or role-change on an admin user.

Password hashes are never returned in any response body.

### 8. OIDC v1.1 Composition

When OIDC is enabled (per ADR-0001 v1.1 intent), the `users` table gains a nullable `oidc_subject TEXT` column. On OIDC assertion, the middleware maps the `sub` claim to a user record via `oidc_subject`. If no match exists and JIT provisioning is enabled, a new user row is inserted with a random `password_hash` (making local login impossible for that account by construction). Local password login continues to work for break-glass access — the `clonr` default admin or any user without an `oidc_subject` set can always log in with a password. Role assignment from OIDC comes from a configurable claim mapping (e.g., OIDC group claim → clonr role). This design keeps local accounts and OIDC accounts in the same table, avoiding a dual-store lookup on every request.

### 9. Out of Scope for ADR-0007

The following are explicit non-goals for this ADR and v1.0:

- **Password reset via email** — requires SMTP configuration; not present in the target deployment model.
- **2FA / TOTP** — deferred to v1.1.
- **SSH key authentication** — different problem domain; handled by the node provisioning layer, not the control plane.
- **SCIM provisioning** — deferred to v2.x.

---

## Implementation Notes for Dinesh

The changes split cleanly into three packages:

1. **`pkg/db`**: add `users` table migration, `CreateUser`, `GetUserByUsername`, `UpdateUser`, `ListUsers`, `SoftDeleteUser`, `SetLastLogin` DB methods. Extend `api_keys` migration to add nullable `user_id` column.

2. **`pkg/server/session.go`**: change `sessionPayload` fields from `{Kid, Scope}` to `{Sub, Role}`. `newSessionPayload` takes a `userID string, role Role` instead of `keyPrefix`. The HMAC sign/verify logic is unchanged.

3. **`pkg/server/middleware.go`**: `AuthContext` gains `UserID` and `Role`. `apiKeyAuth` populates both paths. Add `requireRole(minimum Role)` middleware. Keep `requireScope` as a shim during the transition.

4. **`pkg/server/server.go`**: `BootstrapDefaultUser` runs after DB migrations, before `BootstrapAdminKey`.

5. **`pkg/server/handlers/`**: new `auth.go` (login, logout, set-password), new `users.go` (admin CRUD). `requested_by` audit field plumbed through `AuthContext`.

---

## Consequences

- The SSH-into-journalctl bootstrap UX is eliminated. First-run is `open https://clonr-host/` → login `clonr`/`clonr` → forced password change.
- ADR-0001 (API key scopes) and ADR-0006 (session cookie signing) are not regressed. All existing CLI and service-account usage continues without change.
- `session.go` payload struct is a breaking change for any outstanding session cookies across the upgrade. Operators re-login once after upgrading; `CLONR_SESSION_SECRET` rotation is not required.
- The `users` table is a new SQLite migration. The migration runner must handle it atomically with the `api_keys.user_id` column addition.
- bcrypt at cost 12: ~250ms per hash on a 2020-era server CPU. Login is a low-frequency operation on a control plane; this is acceptable. The 72-byte truncation limit is a documentation item, not a code fix.
- Personal API keys scoped by user role introduce a new `scope=operator` value (or a role annotation column) in `api_keys`. This is an additive schema change and does not invalidate existing keys.
