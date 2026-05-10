# Sprint 41 — Auth + Safety Hardening (Design Doc)

**Status:** DESIGN — pending Richard sign-off before any code lands.
**Sprint:** 41 (`docs/SPRINT-PLAN.md` lines 1274–1416).
**Owner:** Richard.
**Items covered:** `RBAC-ROLES` (1w), `PLUGIN-PRIORITY` (0.5d), `DANGEROUS-DECORATOR` (0.5d), `PLUGIN-BACKUPS` (1d).
**Items deferred to a sibling doc / followup:** `JOURNAL-ENDPOINT` — orthogonal concern, no shared abstraction with the rest of the sprint.

---

## 1. Background & motivation

Sprint 36 shipped the reactive config model: plugins declare watched config keys, the observer matches a dirty-set against those keys, and the diff engine pushes only the deltas through `config_push` WS frames (`internal/clientd/messages.go:123`). Four plugins are converted today — `hostname`, `hosts`, `sssd`, `limits` — and they all live inside the same world: pure `Render(state) → []InstallInstruction`, side-effect-free, idempotent, order-free.

The observer is **deliberately ignorant of plugin semantics**. It treats every plugin as a black box: a name, a set of watched keys, and a pure render function. That ignorance is the right baseline. It is also a fiction the moment we start adding plugins that interact with each other or with the host:

- **Order matters in places.** The reactive contract says "plugins are independent and order-free." That is true for the four plugins we have. It is *not* true for the plugin set we are about to add: SELinux must be configured before `sshd` is restarted; the hostname must be set before `/etc/hosts` is written (or `getent hosts $(hostname)` returns garbage during the transition window); PAM must be configured before SSSD restarts.
- **Some plugins are dangerous.** A misrendered firewall rule, a bad PAM stack, a typo in `/etc/sudoers` — these can lock the operator out of the cluster. We need a tripwire that refuses to apply a flagged change without a separate confirmation.
- **Some plugins should be undoable.** Today, if a plugin renders bad content and `clientd` writes it, the on-disk previous version is gone. ANCHORS preserve the file's *non-managed* regions, but the managed block itself has no history. We need a per-plugin snapshot before write, retained for a configurable number of generations.
- **The auth model is too coarse.** Today's `requireRole(minimum)` middleware (`internal/server/middleware.go:304`) maps three rigid roles — admin/operator/readonly — onto every endpoint via a single rank order. Posix group membership (via LDAP `memberOf`) is not consulted. A user who joins `cluster-ops` in LDAP cannot become an admin in clustr without a separate user row mutation. We need role assignments to flow from group membership.

Sprint 41 addresses these four concerns. The **engineering decision worth flagging** is that three of them — priority, dangerous, backups — are best treated as **one unified extension to the plugin interface**, not three independent bolts. That is the central design call this doc makes. RBAC is a separate but related axis: it intersects the others at exactly one point (dangerous endpoints must check role), so it is designed in parallel here.

---

## 2. Decision: unified plugin metadata, not three separate hooks

The wrong shape would be to add three new optional methods to `config.Plugin`:

```go
// WRONG — three orthogonal sniff-checks, three places to forget.
type Plugin interface {
    Name() string
    WatchedKeys() []string
    Render(state ClusterState) ([]api.InstallInstruction, error)

    Priority() int        // optional, default 100
    IsDangerous() bool    // optional, default false
    BackupPaths() []string // optional, default nil
}
```

Why this is wrong: every call site that consults one of these methods must remember the others exist. The observer's batch dispatcher needs Priority; the push handler needs IsDangerous; the clientd apply path needs BackupPaths. Three separate sites, three separate type-assertion-or-default dances. Each new plugin author has three different "did you remember to override this?" questions instead of one.

**The right shape is a single `Metadata()` method returning a value type** that carries all three concerns plus future ones:

```go
// internal/config/plugin.go (Sprint 41 extension)
type Plugin interface {
    Name() string
    WatchedKeys() []string
    Render(state ClusterState) ([]api.InstallInstruction, error)

    // Metadata returns plugin invariants the observer + push pipeline +
    // clientd apply path consult. Must be deterministic for a given plugin
    // version (no time, no random) — the observer caches the result.
    Metadata() PluginMetadata
}

// PluginMetadata bundles the cross-cutting invariants a plugin declares.
// Zero value is "default, safe, low-priority, no backup, not dangerous" —
// adding a field is a non-breaking change because every plugin gets the
// zero default for the new field until it overrides Metadata().
type PluginMetadata struct {
    // Priority orders apply within a single observer batch. Lower runs
    // earlier. Default 100. Stable sort: equal priorities preserve plugin
    // registration order.
    Priority int

    // Dangerous, when true, instructs the server to require an operator
    // confirmation token before delivering the config_push WS frame. The
    // clientd apply path is unchanged — the gate is server-side. See §5.
    Dangerous bool

    // DangerReason is the human-readable string surfaced in the confirmation
    // UI / CLI prompt. Empty when Dangerous is false. Required when true.
    DangerReason string

    // Backup, when non-nil, instructs clientd to snapshot the listed paths
    // before applying this plugin's push. See §6.
    Backup *BackupSpec
}

type BackupSpec struct {
    // Paths is the list of file paths to snapshot. Each path is resolved
    // verbatim on the node — clientd does not expand globs. The plugin is
    // responsible for knowing exactly what it writes.
    Paths []string

    // RetainN is the number of snapshots to keep, oldest-first GC.
    // Default 3 when zero. Hard-capped at 16 by clientd.
    RetainN int

    // StoredAt is the directory template under which clientd writes the
    // snapshot. Tokens: <plugin>, <timestamp>. Default:
    //   /var/lib/clustr/backups/<plugin>/<timestamp>/
    // The plugin may pin a different prefix if a file-shape reason
    // exists (e.g. a plugin that writes inside /boot wants snapshots
    // co-located for atomic recovery via an initramfs hook).
    StoredAt string
}
```

**Why one method, not three:**

1. **One source of truth.** A plugin author writes `func (P) Metadata() PluginMetadata { return PluginMetadata{...} }` once. Every site that consults plugin invariants reads the same struct.
2. **Zero-value is safe and explicit.** A plugin that does not override `Metadata()` gets Priority=0 (effectively earliest by default, see §2.1 below), Dangerous=false, Backup=nil. The zero value is *exactly* the current behavior of every converted plugin — i.e. the migration is a no-op for plugins that don't care.
3. **Adding a future field never breaks the interface.** When v0.3.x adds, say, `RequiresReboot bool` to `PluginMetadata`, every plugin keeps compiling and gets the zero default. With three separate methods, adding a new dimension means a new method on the interface and a new "did you forget to override?" everywhere.
4. **The observer can cache the whole struct once per plugin at registration time** — see §4.

### 2.1 Priority numbering: int with default 100, not named tiers

Two options were considered for the priority dimension:

**Option A — Named tiers.** `Priority Tier` where `Tier` is an enum: `TierPreBoot, TierEarly, TierNormal, TierLate, TierPostApply`. Authoritative, readable, finite.

**Option B — Integer with anchored default.** `Priority int` where lower runs earlier, default 100. Numeric, infinitely composable.

**Recommendation: Option B (integer, default 100).** Rationale:

- **Insertion between existing plugins is free.** With named tiers, a new plugin that must run between `TierEarly` and `TierNormal` requires either adding a new tier name (re-compiling every plugin) or reusing one of the existing tiers (mixing semantics). With integers, inserting at 75 between 50 and 100 is trivial and local.
- **The author is forced to think about the number.** "Priority 50" is meaningless until you compare it to other plugins; that comparison is exactly the analysis the author should do. Named tiers let the author pick `TierNormal` without thinking, which is the wrong default.
- **Default 100 (not 0) leaves headroom on both sides.** Plugins that "should run early" pick 50, "should run late" pick 150. The default behavior is "I have no opinion, run me alongside the other no-opinion plugins."
- **clustervisor uses integer priorities** (decorator `@priority(N)`); the contract has been validated in production for years. We are not inventing a new ordering primitive.

The doc explicitly **does not** support negative priorities. Lowest legal value is 0 (reserved for "must run first ever, e.g. baseline filesystem layout"). Highest is 1000 (reserved for "post-apply hooks, e.g. service restarts that must happen after every other plugin has settled"). Out-of-range priorities cause registration to fail at server startup — caught at boot, not in production.

### 2.2 Per-plugin metadata for the four converted plugins

This section is the concrete contract: for each of the four Sprint 36 plugins, what does `Metadata()` return, and why?

| Plugin | Priority | Dangerous | Backup paths | Rationale |
|---|---|---|---|---|
| `hostname` | 20 | false | `/etc/hostname` | Hostname must be set **before** anything that resolves it. `/etc/hosts`, sssd, slurmctld config — all read the hostname. Priority 20 puts it well before the default tier (100). It is single-file, fully owned, recoverable by re-render — backup is cheap and bounds the blast radius of a typo. Not dangerous: a bad hostname is awful but not lockout-class. |
| `hosts` | 30 | false | `/etc/hosts` | Must run after hostname (so the local-host entry is correct) but before any service that resolves cluster peers (slurm, sssd). ANCHORS preserve the non-managed regions; backup covers the managed block. Not dangerous (the managed block is bounded, and the file is human-readable). |
| `sssd` | 80 | **true** | `/etc/sssd/sssd.conf` | A bad SSSD config breaks login for every LDAP-resolved user. On a 200-node lab where every operator is LDAP-backed, this is lockout-class. **Dangerous=true** with reason `"misrendered sssd.conf breaks login for all LDAP users; recovery requires console access"`. Priority 80: must run after hostname/hosts but before any slurm config that references LDAP-resolved users. Backup covers the file. |
| `limits` | 110 | false | `/etc/security/limits.conf` | Limits.conf is consulted by PAM at session-establishment time, but a malformed entry is silently ignored — the worst case is "limits don't apply on next login," not "no one can log in." Priority 110: runs after the default tier; nothing else depends on limits being set first. ANCHORS + backup. Not dangerous. |

**The pattern this exposes:**

- Most plugins are not dangerous. Mark dangerous narrowly — overuse turns the gate into a yes/yes/yes click-through.
- Most plugins want a backup. Single-file plugins should default to backing up the file they write. Multi-file plugins (none today, but coming) should list every path they touch.
- Priorities cluster into bands. 0–50 = "foundation" (hostname, /etc/hosts, kernel sysctls). 51–100 = "middleware" (sssd, pam, chrony). 101–150 = "applications" (slurm, ssh keys, limits). 151–200 = "post-apply hooks" (service restarts, validation probes). Document the bands in code comments on `PluginMetadata` so authors pick deliberately.

---

## 3. RBAC-ROLES — separate but related

### 3.1 Schema

Two new tables, one new resolution function. Migration **113** (the next free after `112_config_render_state.sql`).

```sql
-- internal/db/migrations/113_roles_and_assignments.sql

CREATE TABLE roles (
    id               TEXT    PRIMARY KEY,
    name             TEXT    UNIQUE NOT NULL,
    permissions_json TEXT    NOT NULL DEFAULT '{}',
    is_builtin       INTEGER NOT NULL DEFAULT 0,   -- 1 = system role, cannot be deleted
    created_at       INTEGER NOT NULL
);

CREATE TABLE role_assignments (
    id           TEXT    PRIMARY KEY,
    role_id      TEXT    NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    subject_kind TEXT    NOT NULL CHECK (subject_kind IN ('user', 'posix_group')),
    subject_id   TEXT    NOT NULL,                 -- users.id when kind=user; posix CN when kind=posix_group
    created_at   INTEGER NOT NULL,
    UNIQUE(role_id, subject_kind, subject_id)
);

CREATE INDEX idx_role_assignments_subject ON role_assignments(subject_kind, subject_id);

-- Seed built-in roles. permissions_json is the canonical truth; the
-- legacy users.role column is preserved for one release as a backstop.
INSERT INTO roles (id, name, permissions_json, is_builtin, created_at) VALUES
    ('role-admin',    'admin',    '{"*":true}',                                                1, strftime('%s','now')),
    ('role-operator', 'operator', '{"node.read":true,"node.write":true,"node.reimage":true}',  1, strftime('%s','now')),
    ('role-viewer',   'viewer',   '{"node.read":true}',                                        1, strftime('%s','now'));

-- Backfill: every existing user gets a role_assignment row matching their
-- current users.role value. After Sprint 41 the users.role column is
-- read-only legacy; Sprint 43 deletes it.
INSERT INTO role_assignments (id, role_id, subject_kind, subject_id, created_at)
    SELECT
        lower(hex(randomblob(16))),
        'role-' || COALESCE(NULLIF(role,''), 'viewer'),
        'user',
        id,
        strftime('%s','now')
    FROM users
    WHERE role IN ('admin','operator','viewer','readonly','pi');
```

### 3.2 Resolution function

New package `internal/auth/` (not `internal/server/auth/` — the same logic is consumed by `internal/server/middleware.go` and by the future `JOURNAL-ENDPOINT` handler; sub-packaging it under `server/` re-introduces the coupling we are trying to break).

```go
// internal/auth/rbac.go

package auth

// Resolution is the cached result of role resolution for one user request.
// It is computed once per request (in middleware) and stored in the request
// context for downstream handlers to consult cheaply.
type Resolution struct {
    UserID      string
    IsAdmin     bool
    Roles       []string         // role names, sorted for determinism
    Permissions map[string]bool  // union of all role permissions, wildcard "*" → IsAdmin=true
    Groups      []string         // posix CNs from users.groups (LDAP memberOf cache)
}

// ResolveRoles computes the role/permission union for a user, considering:
//   1. Direct user assignments in role_assignments (subject_kind='user').
//   2. Posix group assignments in role_assignments (subject_kind='posix_group')
//      where the user's cached LDAP groups include the assigned subject_id.
//   3. The legacy users.role column, as a fallback if no role_assignments
//      row exists (one-release deprecation path).
//
// The function is read-only; it MUST NOT mutate any table.
func ResolveRoles(ctx context.Context, db *db.DB, userID string) (*Resolution, error)
```

### 3.3 Permission strings

Permissions are dot-delimited verbs scoped by resource type: `node.read`, `node.write`, `node.reimage`, `image.create`, `user.write`, etc. The `*` wildcard at the top level grants everything (admin).

A handler asks: `auth.Allow(resolution, "node.reimage")`. A wildcard match (`*` in permissions, OR a `node.*` match against `node.reimage`) returns true. Otherwise the exact verb must be present.

Wildcards are **only** supported in the permission grant, not in the query. A handler must ask for an exact verb. This forces the handler author to be explicit about what they need — the inverse pattern (handlers asking `node.*`) would let a permission set that grants only `node.read` accidentally pass a write check by a typo on the handler side.

### 3.4 Middleware

`internal/server/middleware.go:requireRole(minimum string)` is preserved as a *legacy* wrapper that internally calls `auth.ResolveRoles` and then matches against `minimum` using the old rank table. Existing routes do not change.

The **new** middleware is `requirePermission("verb")`:

```go
// requirePermission gates a handler on the calling user holding the named
// permission verb. Replaces requireRole for routes that need per-verb gating.
// Bearer API keys with KeyScopeAdmin always pass; scoped keys pass iff
// their scope grants the verb (mapped via api.ScopePermissions).
func requirePermission(verb string) func(http.Handler) http.Handler
```

For Sprint 41, **the only route that switches to `requirePermission` is the canonical RBAC test bed**: `POST /api/v1/nodes/{id}/reimage`. Every other route stays on `requireRole`. This is deliberate: we land the new primitive, prove it on one endpoint, and let Sprint 42 / 43 migrate the rest at a comfortable cadence. We do not big-bang the auth layer in the middle of a safety sprint.

### 3.5 LDAP group caching

`users.groups` (new column, also in migration 113) caches the user's posix CNs from LDAP `memberOf` at session-creation time. Refresh policy: re-read on session creation; no live refresh during an active session (an admin demotion takes effect on next login). This is the same policy clustervisor uses; the alternative (per-request LDAP roundtrip) is a 5–20ms tax on every API call.

### 3.6 Intersection with DANGEROUS-DECORATOR

A "dangerous" endpoint (one whose underlying plugin or instruction has `Dangerous=true`) requires the caller to hold a permission that explicitly grants the dangerous verb. By convention this is `<resource>.dangerous` — e.g. `node.dangerous` grants the right to execute confirmed-dangerous mutations on a node. The built-in `admin` role has `*` and so always passes. `operator` does **not** get `node.dangerous` by default; an explicit role grant is required.

This is the single tie-point between RBAC and the plugin metadata extensions. Everything else in §2 is orthogonal to auth.

---

## 4. Push protocol changes

The observer's existing pipeline (Sprint 36) is:

```
config write → dirty-set → match plugins → render → diff → push (one config_push per plugin per node)
```

Sprint 41 inserts two new steps and adds one field to the push payload:

```
config write → dirty-set → match plugins → render → diff
            → SORT batch by Metadata().Priority           ← NEW
            → for each push:
                if Metadata().Dangerous:
                    stage push, send config_dangerous_confirm_required ← NEW
                    wait for config_dangerous_confirmed (or timeout)
                send config_push (now carries Priority field)
                clientd: snapshot Metadata().Backup.Paths      ← NEW (clientd-side)
                clientd: apply (existing path)
```

### 4.1 `ConfigPushPayload` extension

```go
// internal/clientd/messages.go (Sprint 41 extension to existing struct)
type ConfigPushPayload struct {
    Target       string `json:"target"`
    Content      string `json:"content"`
    Checksum     string `json:"checksum"`
    Plugin       string `json:"plugin,omitempty"`
    RenderedHash string `json:"rendered_hash,omitempty"`

    // NEW (Sprint 41):

    // Priority is the plugin's declared priority (PluginMetadata.Priority).
    // The server includes it for observability; clientd applies pushes in
    // the order they arrive on the wire. Server-side sorting is the
    // authoritative ordering.
    Priority int `json:"priority,omitempty"`

    // Backup, when non-nil, instructs clientd to snapshot the listed paths
    // before applying this push. Empty paths slice is treated as nil.
    Backup *BackupDirective `json:"backup,omitempty"`
}

type BackupDirective struct {
    Paths    []string `json:"paths"`
    RetainN  int      `json:"retain_n"`
    StoredAt string   `json:"stored_at"`           // server expands template tokens
    Manifest string   `json:"manifest"`            // server-supplied manifest filename, e.g. "manifest.json"
}
```

The `Dangerous` flag is **not** in `ConfigPushPayload`. Dangerous pushes are gated *before* the `config_push` frame is delivered — see §4.2. By the time the node sees `config_push`, the confirmation handshake has already passed; clientd does not need to know the push was once dangerous.

Backward compat: clients that don't know `Priority` ignore it (`omitempty`, primitive int). Clients that don't know `Backup` ignore it and skip the snapshot — this is acceptable because old clients are by definition not running on hosts the operator has chosen to back up. The server records the backup expectation in `config_render_state` regardless, so an operator can detect "this node is on an old clientd and your backup directive is being silently dropped" via a `config_render_state.backup_status` field (TODO row addition).

### 4.2 Dangerous-confirmation handshake

Two new WS message types in the existing clientd/serverd channel:

```
server → operator-UI:   config_dangerous_confirm_required {
    push_id:      <uuid>,
    plugin:       "sssd",
    node_id:      <node-uuid>,
    reason:       "misrendered sssd.conf breaks login for all LDAP users",
    rendered_hash: <sha256>,
    expires_at:   <unix-ts, server-issued, 5min from now>,
}

operator-UI → server:  POST /api/v1/config/dangerous/confirm {
    push_id:           <uuid>,
    confirmation_text: "I understand this can break login for all LDAP users",
    rendered_hash:     <sha256>,  // must match the staged push
}
```

The recommended confirmation mechanism is a **typed confirm-string**, not a token-handshake:

- **Token-handshake** (operator clicks "confirm" → UI POSTs a server-issued token back): one click. Easy to misclick. Easy to automate around. The whole point of the gate is to force the operator to *read the reason*.
- **Typed confirm-string** (operator must type the reason text, or a server-issued challenge phrase, verbatim): two-step. Hard to misclick. Forces the operator to read the reason character by character.

clustervisor uses the typed-confirm-string pattern for cluster-destructive operations. The empirical record is that operators who type the string have *never* misfired; operators who clicked a button have. This is the headline architectural decision worth founder review (§9).

The server validates:
1. `push_id` exists and is still staged (not expired).
2. `rendered_hash` matches the staged push exactly. (If a re-render fires between confirm-required and confirm, the push_id is invalidated and a new confirm-required is issued — the operator cannot confirm a stale render.)
3. `confirmation_text` matches the expected challenge phrase exactly (case-sensitive, whitespace-sensitive).
4. The caller's `auth.Resolution` includes the `<resource>.dangerous` permission for the affected resource (see §3.6).

If all four pass, the server emits the staged `config_push` to the node and writes an audit entry (§7).

### 4.3 Where the staging lives

Dangerous pushes are staged in a new table:

```sql
CREATE TABLE pending_dangerous_pushes (
    id            TEXT    PRIMARY KEY,         -- push_id
    node_id       TEXT    NOT NULL,
    plugin_name   TEXT    NOT NULL,
    rendered_hash TEXT    NOT NULL,
    payload_json  TEXT    NOT NULL,            -- the full ConfigPushPayload, ready to send
    reason        TEXT    NOT NULL,
    challenge     TEXT    NOT NULL,            -- exact string the operator must type
    expires_at    INTEGER NOT NULL,
    created_by    TEXT    NOT NULL,            -- actor_id of the operator who triggered the dirty-set
    created_at    INTEGER NOT NULL
);
```

Cleanup: rows older than `expires_at + 1h` are GC'd by the existing audit-log purger (extended to handle this table). Expired pushes are not auto-retried; the operator re-triggers by re-saving the config (which re-fires the observer).

---

## 5. PLUGIN-BACKUPS — clientd snapshot pipeline

Backups are taken **on the node**, by `clientd`, **before** the apply. The server never sees the snapshot content; the server only knows "a snapshot was requested, with these paths and this retention."

### 5.1 On-node layout

```
/var/lib/clustr/backups/<plugin>/<unix-ts>/
    manifest.json     ← {"plugin":"sssd","paths":["/etc/sssd/sssd.conf"],"sha256":{"/etc/sssd/sssd.conf":"<hex>"},"created_at":<ts>,"rendered_hash":"<hex>"}
    etc/sssd/sssd.conf     ← path-preserving copy of the original file
    etc/...                ← (for multi-file plugins)
```

The `<plugin>/<timestamp>/` directory is the unit of rollback. Path-preserving copy means a restore is `cp -a backups/sssd/<ts>/etc/sssd/sssd.conf /etc/sssd/sssd.conf` — no manifest interpretation required to recover by hand if clientd is broken.

### 5.2 Retention

`clientd` runs the GC sweep as the final step of every successful apply: for each plugin under `/var/lib/clustr/backups/`, list timestamped subdirs sorted descending, keep the first `RetainN`, delete the rest. RetainN defaults to 3 (the last three configurations of every plugin). Hard-capped at 16 to bound disk usage on small nodes.

A failed apply does **not** GC. If the apply fails, the snapshot from this push is the most-recent, and the operator may want to restore to the previous one — which the GC must not have removed.

### 5.3 `clustr restore` CLI

```
clustr restore <plugin> --node <node-id> [--from <timestamp>] [--list] [--dry-run]

  --list       list available backups for this plugin on this node, oldest→newest
  --from       restore from the named timestamp (default: previous-to-current,
               i.e. one step back)
  --dry-run    print what would be copied without writing
  --node       target node (required; restores are per-node)
```

The CLI is a thin shim over a new server endpoint `POST /api/v1/nodes/{id}/restore` that pushes a `config_restore` WS frame to clientd. Restore is itself permission-gated (`<resource>.dangerous`, see §3.6) because a restore can re-introduce a previously broken config.

Restore is **not** auto-triggered by an apply failure. The reasoning is in §6.

---

## 6. Failure / rollback semantics

The decision tree for a failed apply:

1. **Plugin Render() fails on the server.** Already handled (Sprint 36): the observer logs, marks the plugin's `config_render_state` row dirty, and the next dirty-set tick retries. No push is sent. Nothing to roll back.

2. **Render succeeds, push sent, clientd snapshot succeeds, clientd apply fails.** Clientd reports failure via `config_push_ack {ok: false, error: ...}`. The server marks `config_render_state.last_pushed_at = NULL` for `(node, plugin)` — Sprint 36 already does this on negative acks; we extend it to record `last_failure_reason` in a new column. **The snapshot stays in place** (the most-recent backup is now the broken render's input; the second-most-recent is the last-known-good).

3. **Apply succeeds, but downstream verification fails** (e.g. `sssd` apply returns OK, but the next `getent passwd` against LDAP times out). Clientd has no way to know this — the apply was "byte-on-disk." The operator notices, runs `clustr restore sssd --node <id> --from <previous-timestamp>`, and the previous config is back in place within seconds.

4. **No automatic rollback.** This is a deliberate choice. Auto-rollback requires a health-probe model per plugin ("after sssd apply, do `getent passwd test@`"). That model is fragile (LDAP server down ≠ sssd config broken) and easy to misfire (a transient network blip rolls back a good apply). v1 is operator-driven rollback with a fast manual path. v2 may add opt-in post-apply probes per plugin — that is a Sprint 50+ concern, explicitly out of scope here.

5. **Restore can itself fail.** Idempotent: re-running the restore command is safe. Errors surface as ack failures and are audited (§7).

---

## 7. Audit logging integration

The existing `audit_log` table (migration `044_audit_log.sql`, schema in §0 of `internal/db/audit.go`) gains four new action constants:

```go
// internal/db/audit.go (additions)
const (
    AuditActionConfigDangerousConfirmRequired = "config.dangerous.confirm_required"
    AuditActionConfigDangerousConfirmed       = "config.dangerous.confirmed"
    AuditActionConfigBackupCreated            = "config.backup.created"   // optional; high-volume, default off
    AuditActionConfigRestore                  = "config.restore"
    AuditActionRoleAssign                     = "role.assign"
    AuditActionRoleRevoke                     = "role.revoke"
)
```

For each action:

| Action | actor_id | resource_type | resource_id | old_value | new_value |
|---|---|---|---|---|---|
| `config.dangerous.confirm_required` | operator who triggered the dirty-set | `config_push` | `push_id` | NULL | `{"plugin":"sssd","reason":"...","challenge":"..."}` |
| `config.dangerous.confirmed` | operator who typed the confirm string | `config_push` | `push_id` | (the staged push as it existed) | `{"plugin":"sssd","applied":true}` |
| `config.restore` | operator | `node` | `<node-id>` | `{"plugin":"sssd","from_timestamp":...,"to_timestamp":...}` | NULL |
| `role.assign` | admin who made the change | `user` or `posix_group` | subject_id | NULL | `{"role":"admin"}` |
| `role.revoke` | admin who made the change | `user` or `posix_group` | subject_id | `{"role":"admin"}` | NULL |

`config.backup.created` is intentionally **not** written by default — every plugin push would emit one, and the audit log volume balloons. A future flag (`CLUSTR_AUDIT_BACKUPS=1`) opts in for debugging.

---

## 8. Rollout plan — four-day execution

Mirrors the Sprint 36 four-day rollout pattern.

### Day 1 — Bundle A — interface + storage scaffolding

Land the foundation without behavior change.

- **`internal/config/plugin.go`** — extend `Plugin` interface with `Metadata() PluginMetadata`. Define `PluginMetadata` and `BackupSpec` structs. Zero-value is "default, safe, no-op."
- **`internal/config/plugin_metadata.go`** (new) — godoc-heavy file with the priority band convention (0–50 foundation, 51–100 middleware, 101–150 applications, 151–200 hooks), validation helpers, and registration-time checks (priority must be 0–1000; if Dangerous then DangerReason must be non-empty; etc.).
- **Convert the four existing plugins to declare `Metadata()`** — but with values chosen so apply behavior is **identical** to Sprint 36. Specifically: every plugin returns its §2.2 priority, but Dangerous is left false for all four on Day 1 (we test the SSSD dangerous flow on Day 3 separately, behind a feature flag, before flipping the default).
- **`internal/db/migrations/113_roles_and_assignments.sql`** — schema + backfill from §3.1.
- **`internal/auth/`** (new package) — `Resolution`, `ResolveRoles`, `Allow` functions. Read-only; no callers yet.
- **`internal/db/migrations/114_pending_dangerous_pushes.sql`** — staging table from §4.3.
- **CI gates:** new package staticcheck-clean; one unit test per data structure proving zero-value defaults.

**Exit:** server compiles, all Sprint 36 tests still green, no runtime behavior change.

### Day 2 — PLUGIN-PRIORITY — observer sort

The smallest, lowest-risk piece. Land it first to flush out priority-related bugs before stacking on the dangerous gate.

- **`internal/config/observer.go`** — extend the batch dispatcher: after collecting the `(node, plugin)` pairs for a dirty-set tick, sort by `plugin.Metadata().Priority` (stable sort), then dispatch in order.
- **`internal/server/reactive_push.go`** — populate `ConfigPushPayload.Priority` from the plugin metadata. (Server-side authoritative; clientd is informed but not authoritative.)
- **Test** — `internal/config/observer_priority_test.go`: register three plugins with priorities 150/100/50, fire a batch, assert dispatch order is 50→100→150. Register two plugins with priority 100 each, fire a batch, assert dispatch order matches registration order (stable sort).
- **Integration test** — bring up the hostname (P=20) and hosts (P=30) plugins together, dirty both, assert hostname push ack lands before hosts push ack.

**Exit:** priority enforced. Hostname-before-hosts proven on lab.

### Day 3 — DANGEROUS-DECORATOR + RBAC enforcement

Two pieces. They land together because the RBAC enforcement *is* the gate on the dangerous flow.

- **`internal/auth/`** — add `Allow(resolution, verb)` consumer code. Wire `internal/server/middleware.go:requirePermission` (new) using `ResolveRoles`. Existing `requireRole` continues to work via a small shim that calls `ResolveRoles` and reduces to the legacy rank table.
- **`POST /api/v1/nodes/{id}/reimage`** — switch this **one** route from `requireRole("admin")` to `requirePermission("node.reimage")`. Canonical RBAC test bed.
- **Dangerous flow** — implement the staging table writer, the `config_dangerous_confirm_required` WS frame, the `POST /api/v1/config/dangerous/confirm` endpoint, the typed-confirm-string validation. Behind a server flag (`CLUSTR_DANGEROUS_GATE_ENABLED=1`, default off in v0.1.x → flipped on in v0.2.0).
- **SSSD plugin** — flip `Metadata().Dangerous = true` once the flow is live. This is the moment the gate becomes observable.
- **Tests** — `internal/server/middleware_permission_test.go` (read-only role gets 403 on `node.reimage`, admin gets 200); `internal/server/dangerous_gate_test.go` (confirm-required emitted, confirm with wrong string rejected, confirm with right string applies, confirm with stale rendered_hash rejected).

**Exit:** SSSD changes require typed confirmation. Reimage requires `node.reimage`. Posix-group → role resolution works end-to-end in lab.

### Day 4 — PLUGIN-BACKUPS — clientd snapshot + restore CLI

Closes the sprint.

- **`internal/clientd/configapply.go`** — before the existing atomic-write step, check `ConfigPushPayload.Backup`. If non-nil, copy each listed path to `<StoredAt>/<plugin>/<timestamp>/...`, write `manifest.json`, only then proceed with apply. On apply success, run GC (`RetainN`).
- **`pkg/api/types.go`** — add `RestorePayload` and the `config_restore` WS frame schema.
- **`internal/server/handlers/clientd.go`** — `POST /api/v1/nodes/{id}/restore` handler, gated on `requirePermission("<resource>.dangerous")`.
- **`cmd/clustr/restore.go`** (new) — CLI command per §5.3.
- **Per-plugin Metadata** — flip `Backup: &BackupSpec{...}` on for all four converted plugins per §2.2.
- **Tests** — `internal/clientd/backup_test.go` (snapshot pre-apply, manifest content, RetainN GC). `internal/clientd/restore_test.go` (backup → corrupt apply → restore → assert byte-identical to pre-apply original). End-to-end lab test: deliberately misrender sssd.conf (by hand-editing on a non-prod node), run `clustr restore sssd --node X`, assert login restored.

**Exit:** Sprint 41 acceptance criteria met. v0.2.0 unblocks on completion.

---

## 9. Open questions / explicit non-goals

### 9.1 Non-goals (will not ship in v1)

- **Backup encryption.** Snapshots live in `/var/lib/clustr/backups/` with root:root, mode 0700. Encryption-at-rest and offsite shipping are v0.3.x concerns; the use case is "restore on the same host within hours of the bad apply," not "compliance archival."
- **Cross-plugin transactions.** If `hostname` then `hosts` is a logical unit, is the right primitive a transactional batch where either both apply or both roll back? **Recommendation: no.** Priority gets you ordered apply, which covers ~95% of the need. Cross-plugin atomicity requires a two-phase-commit protocol on clientd and a real-time coordination layer in the observer — both add significant complexity for a use case the converted plugins do not have. Defer until a real plugin pair requires it (almost certainly never).
- **Multi-role tokens.** A single API token is bound to exactly one role. Multi-role granting comes from being a member of multiple posix groups, each assigned a role; the union is computed by `ResolveRoles`. Per-token role unions add a `token_roles` join table that is unjustifiable for v1.
- **Auto-rollback on apply failure.** See §6 item 4. v1 is operator-driven; auto-rollback awaits per-plugin health probes (Sprint 50+).
- **Plugin-defined custom permissions.** A plugin cannot register a new permission verb at runtime. The permission set is a closed enum defined in `internal/auth/permissions.go` and reviewed in PR. Open enums are a configuration-by-convention trap.

### 9.2 Decisions worth flagging for founder review

The two most consequential calls in this doc:

1. **Priority as `int` with default 100, not named tiers.** §2.1. This is irreversible-ish — once plugins ship with integer priorities, switching to named tiers requires a coordinated rewrite of every plugin. I am 85% confident the integer approach is correct (clustervisor's production track record + the insertion-between-existing-plugins argument). The 15% risk is that integer priorities become a magic-number culture where authors copy each other's numbers without thinking. The mitigation is the documented band convention in code (`internal/config/plugin_metadata.go` godoc) plus PR review.

2. **Typed confirm-string, not token-handshake, for dangerous operations.** §4.2. This is a UX call, not a security call (both designs are equally secure against attackers — the operator is authenticated). The empirical case for typed-confirm is clustervisor's record of zero misfires; the case against is the friction of typing a 60-character string when you are sure you want to do the thing. **I am recommending typed-confirm.** The friction is the point.

### 9.3 Decisions explicitly deferred to follow-up

- **Migration order if Day 1 fails CI on a slow path.** The four-day plan assumes Day 1 lands cleanly and the interface extension is reverse-compatible (it is — every plugin gets zero defaults). If a downstream consumer of `config.Plugin` breaks (e.g. an external integration via plugin registration), we revisit the interface shape — but no such consumer exists today.
- **Per-role API token scopes.** §9.1 forecloses for v1; revisit in Sprint 45+ if a real operator workflow needs multi-role.
- **Surfacing dangerous-confirm in the CLI vs. web UI only.** This doc assumes web UI for v1 (the typed-confirm string fits a modal cleanly). CLI surface (`clustr deploy --confirm "...phrase..."`) is a follow-up.

---

## 10. Test plan (cumulative across all four days)

Per-day test files, plus an end-to-end sprint exit check.

| Area | File | What it asserts |
|---|---|---|
| Metadata defaults | `internal/config/plugin_metadata_test.go` | Zero-value `PluginMetadata` has Priority=0 (but registration rejects 0-as-explicit-default, see below), Dangerous=false, Backup=nil. Default-via-constructor priority is 100. |
| Priority bands | `internal/config/plugin_metadata_test.go` | Priority < 0 or > 1000 at registration → error. Dangerous=true with empty DangerReason → registration error. |
| Observer sort | `internal/config/observer_priority_test.go` | Three plugins P=50/100/150 → apply order is 50→100→150. Two plugins both P=100 → registration-order preserved. |
| RBAC resolve | `internal/auth/rbac_test.go` | User with no role_assignment falls back to users.role legacy column. User with role_assignment subject_kind=user resolves to that role. User with no direct assignment but with posix_group assignment resolves to the group's role. Union of multiple roles unions permissions. |
| Permission allow | `internal/auth/allow_test.go` | `Allow(r, "node.read")` true for viewer. False for an unscoped Resolution. Wildcard `*` matches everything. `node.*` permission matches `node.read` query. Query for `node.*` is rejected (queries must be exact verbs). |
| Permission middleware | `internal/server/middleware_permission_test.go` | viewer hits 403 on `POST /api/v1/nodes/{id}/reimage`. admin hits 200. operator with explicit `node.reimage` grant hits 200. |
| Dangerous gate | `internal/server/dangerous_gate_test.go` | Triggering a dangerous plugin emits `config_dangerous_confirm_required`. Confirm with wrong text → 400 and push not delivered. Confirm with right text → push delivered. Confirm with stale rendered_hash (re-render happened in between) → 409 Conflict, push re-staged with new id. Confirm after `expires_at` → 410 Gone. |
| Backup snapshot | `internal/clientd/backup_test.go` | Before apply, listed paths exist under `/var/lib/clustr/backups/<plugin>/<ts>/`. manifest.json contains sha256 per path. RetainN=3 keeps the most recent 3 timestamps. |
| Restore | `internal/clientd/restore_test.go` | Snapshot → corrupt-apply → restore → file byte-identical to pre-snapshot original. Restore is idempotent on repeat invocation. |
| Audit | `internal/db/audit_dangerous_test.go` | Every confirm-required, confirmed, and restore action writes the expected `audit_log` row with the schema in §7. |
| Sprint exit | `internal/integration/sprint41_test.go` | End-to-end: dirty hostname + hosts + sssd in one config write. Observer sorts (hostname first), sssd triggers confirm-required, operator confirms via API, all three apply in order, backups exist, restore of sssd rolls back to pre-apply state. |

---

## 11. Summary

Sprint 41 extends the Sprint 36 reactive config model with three plugin-level invariants (priority, dangerous, backup) unified under one `PluginMetadata` value type, and lands a posix-group-aware RBAC layer that intersects the dangerous-plugin axis at one well-defined point. The four-day rollout mirrors Sprint 36: interface scaffolding Day 1, ordering Day 2, gating Day 3, durability Day 4.

The central architectural call is the **unified `Metadata()` method**: one source of truth, zero-value-safe, forward-compatible with future invariants. The secondary call is **integer priority with default 100 and documented bands**, over named tiers. The third is **typed-confirm-string** for dangerous operations, over token-handshake.

Sign-off needed from Richard on §9.2 before Day 1 lands.
