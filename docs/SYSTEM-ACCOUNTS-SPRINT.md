# clonr System Accounts Module — Sprint Plan (v1)

**Owner:** Dinesh (implementation), Richard (arch review), Gilfoyle (ops review)
**Status:** Ready to build
**Scope:** One sprint, single merge to `main`

---

## 1. Goal

Add a "System Accounts" feature to clonr. When an operator defines system accounts
and groups in the webui, clonr:

1. Persists the account and group definitions in the local SQLite DB.
2. Injects them into every node's `/etc/passwd`, `/etc/group`, and `/etc/shadow`
   during the finalize step — using `useradd`/`groupadd` in chroot — before the
   node reboots.
3. Provides a new webui section (under a "SYSTEM" nav separator) for managing those
   accounts and groups.

This is the correct mechanism for service accounts that:
- Must exist before the network is up (slurm, munge, nfs, sssd itself).
- Need a fixed, consistent UID/GID across every node in the cluster.
- Cannot wait for LDAP or any other network-based directory service.

The feature is always-on once accounts are defined. A clonr install with no system
accounts defined must behave identically to today.

---

## 2. Architectural Decisions (locked)

| Area | Decision | Rationale |
|---|---|---|
| **Storage** | SQLite tables under the existing `internal/db/` layer, migration 029 | Consistent with all other clonr config; no new storage dependency. |
| **Injection mechanism** | `useradd`/`groupadd` inside `chroot <mountRoot>` | Same pattern as the base OS package manager calls already in finalize.go. Avoids parsing `/etc/passwd` line-by-line, which is brittle and error-prone. |
| **Groups before users** | Groups are created first; `useradd --gid <gid>` references them | Mirrors how a real sysadmin would do it; avoids `useradd` failure when the primary group does not yet exist. |
| **Idempotency** | Check for existing UID/GID via `getent passwd <uid>` / `getent group <gid>` in chroot before invoking add commands | Re-running finalize on the same rootfs (e.g. reimage) must not produce errors when the base image already baked the account. |
| **Conflict model** | Fatal error if a UID/GID is already occupied by a *different* name; skip silently if name + UID/GID already match | Prevents silent UID aliasing, which causes security bugs in HPC environments. |
| **Shadow** | `useradd --no-create-home --password '!'` locks the account (disabled login, valid for service auth) | Service accounts never need interactive login; `!` is the conventional disabled-password sentinel on Linux. |
| **Home dir** | Created by `useradd --home-dir <path> --no-create-home` if operator sets a dir; dir creation is opt-in via `create_home` flag | Most service accounts reference `/var/run/munge` or similar paths that are managed by their own packages, not manually created here. |
| **Ordering** | Injected at step 8 in `applyNodeConfig()`, after LDAP config and before the function returns | LDAP config (step 7) is already the last step; system accounts extend the chain. Never fatal — injection failure is logged as a warning and deployment continues. |
| **No framework** | Ship as `internal/sysaccounts/` with two call sites: `server.go` wiring and `finalize.go` injection | Same extension seam convention established by the LDAP module. |
| **LDAP independence** | Feature works regardless of whether the LDAP module is enabled | Service accounts are local to `/etc/passwd`; LDAP is orthogonal. |
| **No runtime side effects** | No background workers, no health checks, no goroutines | Account definitions are static config; no ongoing process is needed. |
| **No UID/GID auto-assignment** | Operator always specifies UID and GID explicitly | Implicit ID allocation across a fleet is the root cause of most HPC account-drift bugs. Require explicit intent. |

### Extension seam (follows LDAP module convention)

1. `internal/sysaccounts/` package, `Manager` struct owning DB access.
2. Numbered DB migration `internal/db/migrations/029_system_accounts.sql`.
3. Wiring in `server.New()`: construct Manager, store on `Server`.
4. Route registration: `sysaccounts.RegisterRoutes(r, mgr)` in `buildRouter()`.
5. A `NodeConfigProvider`-shaped method `Manager.Accounts(ctx)` / `Manager.Groups(ctx)`
   that `applyNodeConfig()` calls during finalize.

---

## 3. Data Model

### 3.1 SQL migration — `internal/db/migrations/029_system_accounts.sql`

```sql
-- 029_system_accounts.sql: system service account definitions for node injection.
--
-- Accounts and groups defined here are injected into /etc/passwd, /etc/group,
-- and /etc/shadow via useradd/groupadd in chroot during the finalize step.
-- They are local accounts — entirely independent of the LDAP module.

CREATE TABLE system_groups (
    id          TEXT    PRIMARY KEY,    -- UUID
    name        TEXT    NOT NULL UNIQUE,
    gid         INTEGER NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE system_accounts (
    id           TEXT    PRIMARY KEY,   -- UUID
    username     TEXT    NOT NULL UNIQUE,
    uid          INTEGER NOT NULL UNIQUE,
    -- primary_gid references system_groups.gid (not .id) so the value is
    -- self-contained when serialised into NodeConfig for the deploy agent.
    -- Application code enforces referential integrity; SQLite FK not used here
    -- because GID may optionally reference a group defined outside this table
    -- (e.g. a group baked into the base image at a well-known GID).
    primary_gid  INTEGER NOT NULL,
    shell        TEXT    NOT NULL DEFAULT '/sbin/nologin',
    home_dir     TEXT    NOT NULL DEFAULT '/dev/null',
    -- create_home: when true, useradd will create the home directory.
    -- Default false — most service accounts reference package-managed dirs.
    create_home  INTEGER NOT NULL DEFAULT 0,
    -- system_account: when true, passes --system to useradd.
    -- Cosmetic on most distros (Rocky/RHEL UID < 1000 is system by convention)
    -- but explicit is better than implicit.
    system_account INTEGER NOT NULL DEFAULT 1,
    comment      TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- Indexes for the two hot paths: list-all (finalize reads all rows) and
-- conflict-check-by-uid/gid (validate before save).
CREATE INDEX idx_system_accounts_uid ON system_accounts(uid);
CREATE INDEX idx_system_groups_gid   ON system_groups(gid);
```

### 3.2 Go types

```go
// pkg/api/types.go — add

// SystemGroup is a local POSIX group to be injected into every deployed node.
type SystemGroup struct {
    ID          string    `json:"id"`
    Name        string    `json:"name"`
    GID         int       `json:"gid"`
    Description string    `json:"description"`
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
}

// SystemAccount is a local POSIX user account to be injected into every deployed node.
type SystemAccount struct {
    ID            string    `json:"id"`
    Username      string    `json:"username"`
    UID           int       `json:"uid"`
    PrimaryGID    int       `json:"primary_gid"`
    Shell         string    `json:"shell"`
    HomeDir       string    `json:"home_dir"`
    CreateHome    bool      `json:"create_home"`
    SystemAccount bool      `json:"system_account"`
    Comment       string    `json:"comment"`
    CreatedAt     time.Time `json:"created_at"`
    UpdatedAt     time.Time `json:"updated_at"`
}

// SystemAccountsNodeConfig carries the full set of accounts and groups to inject
// during finalization. Populated at reimage-request time; always reflects the
// current DB state at the moment the deploy starts.
type SystemAccountsNodeConfig struct {
    Groups   []SystemGroup   `json:"groups"`
    Accounts []SystemAccount `json:"accounts"`
}
```

Add to `NodeConfig`:

```go
// SystemAccounts, when non-nil and non-empty, causes finalization to inject
// local POSIX accounts and groups into /etc/passwd, /etc/group, and /etc/shadow.
SystemAccounts *SystemAccountsNodeConfig `json:"system_accounts,omitempty"`
```

```go
// internal/sysaccounts/manager.go

type Manager struct {
    db *db.DB
}

func New(database *db.DB) *Manager

// Groups returns all defined system groups, ordered by GID ascending.
func (m *Manager) Groups(ctx context.Context) ([]api.SystemGroup, error)

// Accounts returns all defined system accounts, ordered by UID ascending.
func (m *Manager) Accounts(ctx context.Context) ([]api.SystemAccount, error)

// NodeConfig builds and returns a SystemAccountsNodeConfig for embed in NodeConfig.
// Returns nil (not an error) when no accounts or groups are defined, so finalize
// skips injection cleanly.
func (m *Manager) NodeConfig(ctx context.Context) (*api.SystemAccountsNodeConfig, error)

// CreateGroup validates and inserts a new system group.
// Returns api.ErrConflict if the name or GID is already in use.
func (m *Manager) CreateGroup(ctx context.Context, g api.SystemGroup) (api.SystemGroup, error)

// UpdateGroup replaces a group definition.
// GID changes are validated for conflicts before writing.
func (m *Manager) UpdateGroup(ctx context.Context, id string, g api.SystemGroup) (api.SystemGroup, error)

// DeleteGroup removes a group by ID.
// Returns api.ErrConflict if any system_account references this group's GID as its primary_gid.
func (m *Manager) DeleteGroup(ctx context.Context, id string) error

// CreateAccount validates and inserts a new system account.
// Returns api.ErrConflict if the username or UID is already in use.
func (m *Manager) CreateAccount(ctx context.Context, a api.SystemAccount) (api.SystemAccount, error)

// UpdateAccount replaces an account definition.
// UID changes are validated for conflicts before writing.
func (m *Manager) UpdateAccount(ctx context.Context, id string, a api.SystemAccount) (api.SystemAccount, error)

// DeleteAccount removes an account by ID.
func (m *Manager) DeleteAccount(ctx context.Context, id string) error
```

---

## 4. API Surface (v1)

All under `/api/v1/system/`, admin role required (same gate as LDAP routes).

### Groups

| Method | Path | Body / Notes |
|---|---|---|
| GET | `/api/v1/system/groups` | Returns `{groups: [...], total: N}` |
| POST | `/api/v1/system/groups` | `{name, gid, description?}` → 201 Created with full object. 409 if name or GID conflicts. |
| PUT | `/api/v1/system/groups/{id}` | Full replace: `{name, gid, description}`. 409 on GID conflict with a different group. 409 if any account's `primary_gid` would be orphaned and the new GID differs — client must migrate accounts first. |
| DELETE | `/api/v1/system/groups/{id}` | 409 if any account's `primary_gid` references this group's GID. |

### Accounts

| Method | Path | Body / Notes |
|---|---|---|
| GET | `/api/v1/system/accounts` | Returns `{accounts: [...], total: N}` |
| POST | `/api/v1/system/accounts` | `{username, uid, primary_gid, shell?, home_dir?, create_home?, system_account?, comment?}` → 201 Created. 409 if username or UID conflicts. |
| PUT | `/api/v1/system/accounts/{id}` | Full replace. 409 on UID conflict with a different account. |
| DELETE | `/api/v1/system/accounts/{id}` | Hard delete. No cascade required. |

**Request/response bodies** follow the same shape as existing clonr handlers:
errors use `api.ErrorResponse{Error, Code}`, success uses the typed object directly
or a list wrapper. Validation errors return 400 with `code: "validation_error"`.

**Validation rules (enforced server-side):**
- `name` / `username`: non-empty, max 32 chars, matches `^[a-z_][a-z0-9_-]*$`
- `uid`: 1–65534 (exclude 0 = root, 65535 = nobody/overflow). Warn in UI (not enforce)
  if < 1000 (conventionally system-reserved on most distros).
- `gid`: same range as UID.
- `shell`: must be an absolute path (`/sbin/nologin`, `/bin/bash`, etc.). Default `/sbin/nologin`.
- `home_dir`: must be an absolute path. Default `/dev/null`.

---

## 5. Finalize Integration

### 5.1 `applyNodeConfig()` extension

Add step 8 to the existing ordered sequence (after LDAP config at step 7):

```go
// Step 8: System accounts — inject local POSIX accounts and groups into the
// deployed filesystem before first boot. Services (slurm, munge, nfs) start
// before sssd; accounts must exist locally.
if cfg.SystemAccounts != nil &&
    (len(cfg.SystemAccounts.Groups) > 0 || len(cfg.SystemAccounts.Accounts) > 0) {
    log.Info().
        Int("groups", len(cfg.SystemAccounts.Groups)).
        Int("accounts", len(cfg.SystemAccounts.Accounts)).
        Msg("finalize: injecting system accounts")
    if err := injectSystemAccounts(ctx, mountRoot, cfg.SystemAccounts); err != nil {
        // Non-fatal: log prominently so operator can remediate without aborting
        // the deploy. Node boots without the accounts; re-image to fix.
        log.Warn().Err(err).Msg("WARNING: finalize: system accounts injection failed (non-fatal)")
    } else {
        log.Info().Msg("finalize: system accounts injected")
    }
}
```

Update the doc comment at the top of `applyNodeConfig()`:

```
//  8. System accounts (groups then users, idempotent via getent check in chroot)
```

### 5.2 `injectSystemAccounts()` implementation

Location: `internal/deploy/finalize.go` (or a new `internal/deploy/sysaccounts.go`
if the function grows beyond ~80 lines — prefer the separate file to keep
finalize.go readable).

Injection order:
1. For each group in `cfg.SystemAccounts.Groups` (sorted by GID ascending):
   - Run `chroot <mountRoot> getent group <gid>` (or `<name>`). Parse output.
   - If GID is already present with the same name: skip (idempotent).
   - If GID is already present with a different name: return a named error
     (`ErrGIDConflict{GID, existing, requested}`) — finalize logs it as a
     non-fatal warning.
   - If absent: run `chroot <mountRoot> groupadd --gid <gid> <name>`.

2. For each account in `cfg.SystemAccounts.Accounts` (sorted by UID ascending):
   - Run `chroot <mountRoot> getent passwd <uid>`.
   - If UID is already present with the same username: skip.
   - If UID is already present with a different username: return a named error.
   - If absent: build `useradd` args:
     ```
     useradd
       --uid <uid>
       --gid <primary_gid>
       --shell <shell>
       --home-dir <home_dir>
       [--no-create-home | --create-home]   // per create_home flag
       [--system]                            // per system_account flag
       --comment <comment>
       --password '!'                        // locked password — no interactive login
       <username>
     ```

All commands run through the existing `runAndLog(ctx, label, cmd)` helper already
used in `finalize.go` for chroot-dnf and grub2-install calls. Error from any
individual account is non-fatal to the overall inject call — log and continue,
so a single bad entry does not block 50 valid accounts.

### 5.3 NodeConfig population at reimage time

The `RegisterNode` handler in `handlers/nodes.go` already has the `LDAPNodeConfig`
injection pattern to follow. Wire `SystemAccounts` the same way on `NodesHandler`:

```go
// internal/server/handlers/nodes.go

type NodesHandler struct {
    DB *db.DB
    LDAPNodeConfig           func(ctx context.Context) (*api.LDAPNodeConfig, error)
    RecordNodeLDAPConfigured func(ctx context.Context, nodeID, configHash string) error
    // SystemAccountsConfig returns the current set of system accounts/groups for
    // embed in the NodeConfig returned to the deploy agent. Returns nil if none defined.
    SystemAccountsConfig     func(ctx context.Context) (*api.SystemAccountsNodeConfig, error)
}
```

In `RegisterNode`, after LDAP config population:

```go
if h.SystemAccountsConfig != nil {
    saCfg, err := h.SystemAccountsConfig(r.Context())
    if err != nil {
        log.Warn().Err(err).Msg("register: could not load system accounts config (non-fatal)")
    } else {
        cfg.SystemAccounts = saCfg
    }
}
```

Wire in `server.go` `buildRouter()` alongside the existing LDAP closure:

```go
nodes := &handlers.NodesHandler{
    DB: s.db,
    LDAPNodeConfig: func(ctx context.Context) (*api.LDAPNodeConfig, error) {
        return s.ldapMgr.NodeConfig(ctx)
    },
    RecordNodeLDAPConfigured: func(ctx context.Context, nodeID, configHash string) error {
        return s.ldapMgr.RecordNodeConfigured(ctx, nodeID, configHash)
    },
    SystemAccountsConfig: func(ctx context.Context) (*api.SystemAccountsNodeConfig, error) {
        return s.sysAccountsMgr.NodeConfig(ctx)
    },
}
```

---

## 6. Webui Placement

### 6.1 Nav structure

The LDAP module established the `<hr>` separator + uppercase section label pattern.
Add a "SYSTEM" section below the existing LDAP section in `index.html`:

```html
<!-- System Accounts module nav — always visible for admins -->
<div id="nav-system-section">
    <hr style="border:none;border-top:1px solid var(--border);margin:8px 0;">
    <div style="font-size:11px;font-weight:600;text-transform:uppercase;
                letter-spacing:0.08em;color:var(--text-sidebar);opacity:0.5;
                padding:4px 12px 2px;">SYSTEM</div>
    <a href="#/system/accounts" class="nav-item" id="nav-system-accounts">
        <!-- user icon -->
        <svg .../>
        <span>Accounts</span>
    </a>
    <a href="#/system/groups" class="nav-item" id="nav-system-groups">
        <!-- users/group icon -->
        <svg .../>
        <span>Groups</span>
    </a>
</div>
```

This section is always visible (no enable/disable toggle). Hide from non-admin users
via the same 403 response pattern used by the LDAP nav bootstrap: if
`GET /api/v1/system/accounts` returns 403, hide `#nav-system-section`.

### 6.2 Pages

**`#/system/accounts`** — Accounts table + modals

Columns: Username | UID | Primary GID | Shell | Home Dir | System? | Comment | Actions

Create/Edit modal fields: username, uid (number input), primary_gid (number input or
dropdown of defined groups), shell (text, default `/sbin/nologin`), home_dir (text,
default `/dev/null`), create_home (checkbox, default off), system_account (checkbox,
default on), comment (optional text).

**`#/system/groups`** — Groups table + modals

Columns: Name | GID | Description | Actions

Create/Edit modal fields: name, gid (number input), description (optional text).

**JS conventions:** Same as `ldap.js` — plain JS in `sysaccounts.js`, loaded after
`ldap.js` and before `app.js`. `API.sysaccounts.{listGroups, createGroup, updateGroup,
deleteGroup, listAccounts, createAccount, updateAccount, deleteAccount}` wrappers in
`api.js`.

**Router registration** in `app.js`:

```js
Router.register('/system/accounts', SysAccountsPages.accounts);
Router.register('/system/groups',   SysAccountsPages.groups);
```

Nav bootstrap for the system section can be a simple try/catch on
`GET /api/v1/system/accounts` — hide on 403, show on any 2xx.

---

## 7. Files

### New

```
internal/sysaccounts/manager.go         # Manager: CRUD, NodeConfig()
internal/sysaccounts/routes.go          # RegisterRoutes, handler methods

internal/db/migrations/029_system_accounts.sql

internal/server/ui/static/js/sysaccounts.js
```

### Modified

```
pkg/api/types.go                        # add SystemGroup, SystemAccount, SystemAccountsNodeConfig;
                                        # add SystemAccounts *SystemAccountsNodeConfig on NodeConfig

internal/deploy/finalize.go             # add step 8: injectSystemAccounts() call in applyNodeConfig();
                                        # add injectSystemAccounts() function (or split to sysaccounts.go)

internal/server/server.go               # add *sysaccounts.Manager field; wire in New();
                                        # wire SystemAccountsConfig func on NodesHandler;
                                        # register routes in buildRouter() admin group

internal/server/handlers/nodes.go       # add SystemAccountsConfig func field on NodesHandler;
                                        # populate cfg.SystemAccounts in RegisterNode

internal/server/ui/static/index.html    # add SYSTEM nav section
internal/server/ui/static/js/api.js     # add API.sysaccounts.* wrappers
internal/server/ui/static/js/app.js     # register /system/* routes
```

---

## 8. Edge Cases

### 8.1 UID/GID conflict at injection time

**Scenario:** The base image already has `munge` at UID 1002, but the operator
defines `munge` at UID 1003. Or the image has an unrelated account at UID 1003.

**Behavior:**

| Existing account | Requested | Result |
|---|---|---|
| Same name, same UID | Same name, same UID | Skip (idempotent) |
| Different name, same UID | Any | `ErrUIDConflict` — logged as warning, deployment continues |
| Same name, different UID | Any | `ErrUIDConflict` — same |
| Not present | Any | `useradd` runs normally |

The non-fatal approach is deliberate: a UID conflict on one account should not block
the 10 other accounts from being injected. Each entry is processed independently.

### 8.2 Updating an existing account

Updating an account changes only the DB row. **Nodes already deployed are not
retroactively updated** — they reflect the account definitions that were active at
reimage time. To propagate changes, the operator must reimage affected nodes. This
is the same model as every other clonr config change (SSH keys, network config, etc.).

The UI should display a notice: "Changes take effect on next reimage."

### 8.3 Deleting an account

Deletion removes the DB row only. Already-deployed nodes retain the account in their
local `/etc/passwd`. The UI should display this warning in the delete confirmation
dialog.

For groups: the API returns 409 if any account in the DB references the group's GID
as `primary_gid`. The operator must update or delete those accounts first. The error
body should list the conflicting account usernames so the operator knows what to fix.

### 8.4 Account removal from a live node

Out of scope for v1. Removing an account from a running node requires either a
reimage (the clonr way) or manual `userdel` via SSH. Document this in the UI
delete confirmation dialog.

### 8.5 GID referenced by `primary_gid` but not in `system_groups`

Allowed. An operator may set `primary_gid = 1001` where `1001` is already baked
into the base image (e.g. the distro ships a `slurm` group). In that case the group
injection step skips the GID (not in `system_groups`) and `useradd` uses the
existing group from the image. No error.

### 8.6 UID/GID range overlap with LDAP posixAccount entries

No conflict at the OS level — local `/etc/passwd` entries take precedence over LDAP
for the same UID (NSS search order is `files ldap` by default in sssd). The operator
is responsible for not assigning the same UID to a local system account and an LDAP
posixAccount. The UI should display a static warning: "Ensure UIDs defined here do
not overlap with UIDs in your LDAP directory."

### 8.7 `useradd` not present in the initramfs

`useradd` and `groupadd` are called via `chroot <mountRoot>` — they run from the
*deployed image's* binaries, not the initramfs. As long as the base image includes
`shadow-utils` (present in every Rocky/RHEL minimal install), this works. If the
base image is stripped to the point of not having `shadow-utils`, `useradd` will
return a non-zero exit code; `runAndLog` will log it, and injection is skipped
(non-fatal). The operator must ensure their base image includes `shadow-utils`.

### 8.8 Password field in `/etc/shadow`

`useradd --password '!'` sets the locked password token. The account exists in the
system but cannot authenticate via PAM password login. Service accounts (slurm,
munge) authenticate via their own mechanisms (munge key, Slurm auth plugin), not
PAM passwords. This is the correct and conventional configuration.

---

## 9. Phased Implementation

Each phase commits cleanly and pushes. CI runs the build; `clonr-autodeploy.timer`
on the clonr VM rebuilds within 2 minutes.

### Phase 1 — DB migration + Manager skeleton + API stub

- Migration `029_system_accounts.sql`.
- `Manager` with `ListGroups`, `ListAccounts`, `NodeConfig` returning empty slices.
- `RegisterRoutes` wired in `server.go` with all endpoints returning 501 Not Implemented.
- `NodesHandler.SystemAccountsConfig` wired (returns `nil` until Phase 2 is complete).
- **Commit:** `feat(sysaccounts): scaffold module with migration and route skeleton`

### Phase 2 — Full CRUD + validation

- `CreateGroup`, `UpdateGroup`, `DeleteGroup`, `CreateAccount`, `UpdateAccount`,
  `DeleteAccount` in `Manager` with all validation rules from Section 4.
- All API endpoints return correct responses.
- `NodeConfig()` returns populated `SystemAccountsNodeConfig`.
- **Commit:** `feat(sysaccounts): implement group and account CRUD`

### Phase 3 — Finalize injection

- `injectSystemAccounts()` function in `internal/deploy/` (or `finalize.go`).
- `applyNodeConfig()` calls it as step 8.
- `NodeConfig` population in `RegisterNode`.
- `SystemAccounts` field added to `api.NodeConfig` and `api.types.go`.
- **Commit:** `feat(sysaccounts): inject accounts into deployed nodes during finalize`

### Phase 4 — Webui

- `sysaccounts.js` with `SysAccountsPages.accounts` and `SysAccountsPages.groups`.
- `API.sysaccounts.*` wrappers in `api.js`.
- Router registration in `app.js`.
- SYSTEM nav section in `index.html`.
- **Commit:** `feat(sysaccounts): add accounts and groups webui`

---

## 10. Build & Deploy Policy (hard rule)

**Do NOT run `go build`, `go test`, `make`, or any compile-heavy command on the
sqoia-dev workstation.** That host is resource-constrained; local builds OOM it.

1. Write code, commit, push to `origin/main` on `sqoia-dev/clonr`.
2. GitHub Actions CI (`ci.yml`) builds and tests on push. Monitor with `gh run watch`
   per the standing CI-watch rule. Fix failures before marking a phase done.
3. `clonr-autodeploy.timer` on `192.168.1.151` polls `origin/main` every 2 minutes,
   rebuilds binaries and initramfs, hot-restarts `clonr-serverd`. Watch with:
   ```
   ssh -i ~/.ssh/id_ed25519 root@192.168.1.151 "journalctl -u clonr-autodeploy.service -f"
   ```

---

## 11. Commit & Authorship

Per repo standing rule: all commits authored as `NessieCanCode <robert.romero@sqoia.dev>`.
No Co-Authored-By lines. No Claude attribution.

```bash
git config user.name "NessieCanCode"
git config user.email "robert.romero@sqoia.dev"
```

Commit prefixes: `feat(sysaccounts):` for feature work, `fix:` for CI corrections,
`chore:` for scaffolding.

Push via the standard ssh-agent pattern in `CLAUDE.md`.

---

## 12. Known Risks

| Risk | Mitigation |
|---|---|
| Base image lacks `shadow-utils` | `useradd` call fails; `runAndLog` logs the error non-fatally; injection is skipped. Node boots without the account. Operator must ensure `shadow-utils` is present in the image. |
| UID/GID overlap between system accounts and LDAP posixAccount entries | Local `/etc/passwd` takes precedence in `files ldap` NSS order. Still a security footgun — display a static warning in the UI. |
| Operator changes a UID/GID after many nodes are deployed | Old nodes retain the old account. New reimages get the new UID. Mixed-UID cluster breaks file ownership. Document prominently: UID/GID values should be treated as immutable once any node is deployed with them. |
| `getent` not available in the deployed image's chroot | Extremely unlikely (`getent` is in `glibc-common`, present in every RHEL derivative). If absent, `useradd` will return its own error for a duplicate UID; `runAndLog` captures and logs it non-fatally. |
| Very large account lists slow finalize | Each `getent` + `useradd` call forks a child process. At 50 accounts this is ~100 forks, well under 1 second. Not a practical concern for HPC cluster service accounts (typically < 20). |
| Injection failure leaves partial accounts on node | Each account is independent; a failure on account N does not roll back accounts 1..N-1. This is acceptable: partial injection is better than no injection. The failing account name is in the log. |

---

## 13. Acceptance Criteria

- [ ] Fresh clonr install with no system accounts defined behaves identically to today.
- [ ] An admin can define a `munge` group (GID 1002) and a `munge` account (UID 1002)
      in the webui.
- [ ] Reimaging a node causes `munge:x:1002:` to appear in the node's `/etc/group`
      and `munge:x:1002:1002::/var/run/munge:/sbin/nologin` in `/etc/passwd`.
- [ ] Running finalize twice on the same rootfs (re-reimage) does not produce an
      error when the accounts are already present with matching UID/GID.
- [ ] Finalize logs a warning (non-fatal) and continues when a UID conflict is
      detected; the remaining accounts in the list are still processed.
- [ ] The API returns 409 with a clear message when attempting to create a group
      whose GID is already in use by a different group.
- [ ] Deleting a group that is referenced as `primary_gid` by any account returns
      409 with the list of conflicting account usernames.
- [ ] CI is green on every pushed commit.
- [ ] No `go build` / `go test` / `make` invoked locally during development.

---

## 14. Out of Scope (v2)

- Retroactive account injection onto already-deployed nodes (requires reimage in v1).
- UID/GID conflict detection against LDAP directory entries (would require a live
  LDAP query at save time; too coupled for v1).
- Account expiry, password aging, or any PAM policy fields.
- `supplementary_groups` field on accounts (secondary group membership beyond
  `primary_gid`). The `usermod -aG` pattern is v2.
- UI-enforced UID/GID reservation ranges (e.g. "warn if UID < 500"). Display a
  static advisory note instead.
- Audit log of account changes.
