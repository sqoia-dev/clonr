# clustr LDAP Module — Sprint Plan (v1)

**Owner:** Dinesh (implementation), Gilfoyle (ops review), Richard (arch review)
**Status:** Ready to build
**Scope:** One sprint, single merge to `main`

---

## 1. Goal

Add an optional "LDAP" module to clustr. When an operator enables it in the webui, clustr:

1. Stands up a self-hosted OpenLDAP (slapd) instance on the clustr-serverd host, configured end-to-end following best practices.
2. Generates a long-lived self-signed CA and a server certificate signed by it.
3. Exposes a new webui section (behind a nav separator, visible only when the module is enabled) for managing users and groups.
4. Injects sssd + CA trust + ldap.conf into every node reimaged after enable, so nodes authenticate against the same directory.

The feature is opt-in. A clustr install with the module disabled must behave identically to today.

---

## 2. Architectural Decisions (locked)

| Area | Decision | Rationale |
|---|---|---|
| **LDAP server** | OpenLDAP (`slapd`) | HPC default; what OpenHPC / xCAT / Warewulf / Bright assume. |
| **Config backend** | `cn=config` (OLC), **seeded once** via `slapadd -n 0 -F` from a rendered LDIF; never edited by clustr at runtime | Rocky 9 ships cn=config by default; avoids fighting the packaging. cn=config stays a deployment artifact, not a runtime control plane. |
| **Schema** | Classic NIS: `nis` + `cosine` + `inetorgperson` (posixAccount, posixGroup, memberUid) | Every HPC doc, every pam_ldap example, every sssd.conf in the wild assumes memberUid. sssd handles rfc2307bis too, but classic matches operator muscle memory. |
| **Password hashing** | `{CRYPT}` with glibc `$6$` (SHA-512-crypt, 100k rounds, built into stock slapd) | No module load required — `pw-sha2` is not packaged in EPEL's openldap-servers-2.6.8. Go-side hashing via `github.com/GehirnInc/crypt/sha512_crypt` (pure Go, no CGO). |
| **systemd unit** | Ship `clustr-slapd.service`; distro `slapd.service` masked on install | Full control over paths, no collision with a pre-existing slapd. |
| **Paths** | Data `/var/lib/clustr/ldap/` (mdb), config `/etc/clustr/ldap/slapd.d/`, TLS `/etc/clustr/ldap/tls/`, PKI `/etc/clustr/pki/` (CA) | Everything clustr-owned lives under `/*/clustr/`. |
| **Runtime user** | clustr-serverd runs as a dedicated `clustr` system user. Polkit rule grants `clustr` exactly `start`, `stop`, `restart` on `clustr-slapd.service`. Initial `slapadd` seed runs as root during module enable. | Principle of least privilege. |
| **Listening surface** | `ldaps://:636` only. Port 389 bound to `127.0.0.1` for maintenance only, or disabled. | TLS-only prevents the classic "StartTLS misconfigured and nobody noticed" failure. |
| **CA cert** | RSA 4096, **30-year validity**, stored at `/etc/clustr/pki/ca.crt`, key at `/etc/clustr/pki/ca.key` (mode 0600) | Rotating a root of trust across every compute node is painful; avoid doing it often. LDAP clients don't enforce the 825-day browser rule. |
| **Server cert** | RSA 4096, **5-year validity**, auto-renewed at year 4 by a background goroutine. SAN must include hostname, IP, and `clustr.local`. | Long enough that a neglected cluster doesn't break; short enough that rotation is survivable. Missing IP SAN is the #1 sssd TLS failure. |
| **Key type** | RSA 4096 (not ed25519) | Older HPC node OSes still have OpenSSL/libldap stacks that choke on ed25519 in TLS handshakes. |
| **Base DN** | Operator-provided at enable time. Default pre-fill: `dc=cluster,dc=local`. **Locked** once the first node is provisioned. | Once a node is imaged with a base DN there is no clean migration path. UI enforces the lock. |
| **Bind accounts** | Two: `cn=admin,<baseDN>` (Directory Manager, never leaves the server) and `cn=node-reader,ou=services,<baseDN>` (read-only on `ou=people` and `ou=groups`, enforced by ACL in the seed LDIF). **Only `node-reader` credentials are injected into nodes.** | Biggest footgun in any LDAP-backed fleet is nodes holding the admin bind. |
| **Indices** | `uid eq`, `uidNumber eq`, `gidNumber eq`, `memberUid eq`, `cn eq,sub`, `entryCSN eq`, `entryUUID eq` | Covers sssd's lookup patterns without over-indexing. |
| **mdb tuning** | `maxsize 1073741824` (1 GB), `checkpoint 512 5` | Fits <10K users comfortably. Resizing mdb is one of the few safe cn=config touches later. |
| **Node auth stack** | sssd with offline cache enabled, `pam_mkhomedir`, `TLS_REQCERT demand` enforced | nslcd has no meaningful offline cache; sssd must serve cached creds when clustr is unreachable. |
| **Home dirs** | Local `/home/<user>` created on first login via `pam_mkhomedir` | NFS/automount is a future module, not this one. |
| **sudoers** | Opt-in checkbox, **off by default**, with warning | sudoRole schema works but is too easy to foot-gun via a UI click. |
| **Password policy** | ppolicy overlay enabled by default; default policy at `cn=default,ou=policies,<baseDN>`: min length 8, lockout after 5 failures (permanent, admin unlocks), no expiry, no history, no quality checks. Force-change on reset via per-reset checkbox in the UI. Last-login tracking via `pwdLastSuccess`. | Gilfoyle spec 2026-04-18. |
| **Module framework** | **None.** Ship as `internal/ldap/` with explicit wiring at two call sites | clustr has no plugin surface today; inventing one for one module is waste. Document the extension seams so a second module (NFS, Slurm) can follow the same shape. |

### Extension seams (documented convention for future modules)

1. `internal/<module>/` package, `Manager` struct owning all state.
2. Numbered DB migration under `internal/db/migrations/`.
3. Wiring in `server.New()`: construct Manager, store on `Server`, call `Manager.StartBackgroundWorkers(ctx)` from `StartBackgroundWorkers()`.
4. Route registration: `<module>.RegisterRoutes(r, mgr)` in `buildRouter()`.
5. A `NodeConfigProvider`-shaped method on the Manager that `applyNodeConfig()` queries.

---

## 3. Data Model

### 3.1 SQL migration — `internal/db/migrations/027_ldap_module.sql`

```sql
CREATE TABLE ldap_module_config (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    enabled             INTEGER NOT NULL DEFAULT 0,
    status              TEXT    NOT NULL DEFAULT 'disabled',
    status_detail       TEXT    NOT NULL DEFAULT '',
    base_dn             TEXT    NOT NULL DEFAULT '',
    admin_dn            TEXT    NOT NULL DEFAULT '',
    service_bind_dn     TEXT    NOT NULL DEFAULT '',
    service_bind_passwd TEXT    NOT NULL DEFAULT '',   -- plaintext, used by nodes; file-backed storage OK for v1
    ca_cert_pem         TEXT    NOT NULL DEFAULT '',
    ca_key_pem          TEXT    NOT NULL DEFAULT '',
    ca_fingerprint      TEXT    NOT NULL DEFAULT '',
    server_cert_not_after TIMESTAMP,
    base_dn_locked      INTEGER NOT NULL DEFAULT 0,    -- set to 1 when first node is provisioned
    last_seeded_at      TIMESTAMP,
    last_checked_at     TIMESTAMP
);

INSERT INTO ldap_module_config (id) VALUES (1);

CREATE TABLE ldap_node_state (
    node_id             TEXT PRIMARY KEY,
    ldap_configured_at  TIMESTAMP NOT NULL,
    last_config_hash    TEXT NOT NULL,
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);
```

> Confirm the `nodes` table name by reading `internal/db/` before finalizing the FK.

### 3.2 Go types

```go
// pkg/api/types.go — add
type LDAPNodeConfig struct {
    ServerURI         string // "ldaps://clustr.local:636"
    BaseDN            string
    ServiceBindDN     string // read-only service account, NOT admin DN
    ServiceBindPasswd string // plaintext, dropped into sssd.conf on the node
    CACertPEM         string // written to /etc/pki/ca-trust/source/anchors/clustr-ca.crt and /etc/openldap/certs/
}

// internal/ldap/manager.go — not wire-exposed
type Manager struct {
    db      *sql.DB
    cfgDir  string  // /etc/clustr/ldap
    dataDir string  // /var/lib/clustr/ldap
    pkiDir  string  // /etc/clustr/pki
    // ...
}

func (m *Manager) Enable(ctx context.Context, req EnableRequest) error
func (m *Manager) Disable(ctx context.Context, req DisableRequest) error
func (m *Manager) Status(ctx context.Context) (Status, error)
func (m *Manager) NodeConfig(ctx context.Context) (*api.LDAPNodeConfig, error) // nil if disabled
func (m *Manager) MarkNodeConfigured(ctx context.Context, nodeID, configHash string) error
func (m *Manager) LockBaseDN(ctx context.Context) error
func (m *Manager) StartBackgroundWorkers(ctx context.Context) // cert expiry watch, slapd health probe
```

`LDAPNodeConfig` intentionally omits any admin credential. If a future caller needs to populate it from the admin DN, the compiler won't let them — the field does not exist.

---

## 4. API Surface (v1)

All under `/api/v1/ldap/`, gated by the existing admin role check:

| Method | Path | Body / Notes |
|---|---|---|
| GET | `/status` | `{enabled, status, status_detail, base_dn, ca_fingerprint, server_cert_not_after, cert_expiry_warning}` |
| POST | `/enable` | `{base_dn, admin_password}` → 202 Accepted, poll `/status`. Runs cert gen + slapd seed + start in a goroutine. |
| POST | `/disable` | `{confirm: "detach"\|"destroy", nodes_acknowledged: bool}`. 409 if nodes still configured and `nodes_acknowledged` is false. |
| GET | `/users` | List all posixAccount entries |
| POST | `/users` | `{uid, uid_number, gid_number, home, shell, full_name, ssh_pubkey?, initial_password}` |
| PUT | `/users/{uid}` | Update subset: shell, full_name, ssh_pubkey, disabled |
| DELETE | `/users/{uid}` | Hard delete |
| POST | `/users/{uid}/password` | `{new_password}` — convenience over generic PUT |
| GET | `/groups` | List all posixGroup entries |
| POST | `/groups` | `{cn, gid_number, description?}` |
| DELETE | `/groups/{cn}` | Hard delete |
| POST | `/groups/{cn}/members` | `{uid}` — adds memberUid |
| DELETE | `/groups/{cn}/members/{uid}` | Removes memberUid |
| POST | `/backup` | Run `slapcat -n 1` to a timestamped LDIF under `/var/lib/clustr/ldap/backups/`, return filename |

Webui pages (static + JS under `clustr-static/`):

- `/ldap` — settings, status, enable/disable, backup button
- `/ldap/users` — user table + create/edit modals
- `/ldap/groups` — group table + member management

Nav injection: nav HTML always contains the LDAP section, gated by `if (status.enabled)` in the bootstrap JS. Bootstrap fetches `/api/v1/ldap/status` alongside `/api/v1/auth/me` on page load. A `<hr>` separator precedes the LDAP nav group.

---

## 5. Files

### New
```
internal/ldap/manager.go          # Manager + Enable/Disable/Status
internal/ldap/cert.go             # CA gen, server cert gen, renewal
internal/ldap/slapd.go            # systemctl wrappers, seed LDIF rendering, slapadd -n 0 exec
internal/ldap/dit.go              # go-ldap client: user/group CRUD
internal/ldap/routes.go           # RegisterRoutes, handler methods
internal/ldap/templates/slapd-seed.ldif.tmpl
internal/ldap/templates/sssd.conf.tmpl
internal/ldap/templates/ldap.conf.tmpl
internal/ldap/health.go           # background health + cert expiry watcher

internal/db/migrations/027_ldap_module.sql

clustr-static/pages/ldap.html      # (or equivalent per existing static layout)
clustr-static/pages/ldap-users.html
clustr-static/pages/ldap-groups.html
clustr-static/js/ldap.js

deploy/systemd/clustr-slapd.service
deploy/polkit/50-clustr-slapd.rules
deploy/install/ldap-bootstrap.sh  # masks distro slapd.service, installs our unit, creates /var/lib/clustr/ldap, polkit file
```

### Modified
```
internal/server/server.go         # add *ldap.Manager field, wire in New(), call RegisterRoutes() + StartBackgroundWorkers()
internal/deploy/finalize.go       # add writeLDAPConfig() call at end of applyNodeConfig(); populate cfg.LDAPConfig at reimage-request time in the reimage scheduling path
pkg/api/types.go                  # add LDAPConfig *LDAPNodeConfig on NodeConfig; define LDAPNodeConfig
internal/config/config.go         # add LDAPDataDir, LDAPConfigDir, LDAPPKIDir to ServerConfig (with defaults)
<nav template file>               # add LDAP nav section with <hr> separator, gated by JS
```

> Read `internal/deploy/finalize.go` to confirm the exact line and the `NodeConfig` population path before wiring. Richard's spec pins the injection at `applyNodeConfig()` — validate and adjust.

---

## 6. Phased Implementation

Each phase commits cleanly and pushes. CI runs the build; `clustr-autodeploy.timer` on the clustr VM rebuilds and hot-restarts within 2 minutes.

### Phase 1 — Skeleton + migration + feature-off
- Migration 027, `Manager` with stub methods, `/api/v1/ldap/status` returning `{enabled: false}`, nav separator hidden.
- Wiring in `server.go` so the manager exists but does nothing.
- **Commit:** `feat(ldap): scaffold module with status endpoint and migration`

### Phase 2 — Enable flow, certs, slapd up
- `cert.go`: CA + server cert generation, persisted to disk and DB.
- `slapd.go`: render seed LDIF from template, run `slapadd -n 0 -F <config>`, `systemctl start clustr-slapd`.
- `Enable()` runs synchronously in a goroutine with status updates; `/status` polls.
- Health goroutine: 30s bind to `localhost:636`, write status.
- systemd unit + polkit file + install script.
- **Commit:** `feat(ldap): add enable flow with slapd provisioning and TLS`

### Phase 3 — User + group CRUD + webui
- `dit.go` user/group CRUD via go-ldap.
- Webui pages + JS for settings, users, groups.
- Nav separator appears when enabled.
- **Commit:** `feat(ldap): add user and group management`

### Phase 4 — Node injection
- `LDAPNodeConfig` in `pkg/api/types.go`.
- Populate at reimage-request time by calling `ldapMgr.NodeConfig()`.
- `writeLDAPConfig()` inside `applyNodeConfig()`: render sssd.conf, drop CA, enable sssd in chroot, write ldap.conf.
- `MarkNodeConfigured()` called on successful phone-home; base DN locks on first success.
- **Commit:** `feat(ldap): inject sssd config into reimaged nodes`

### Phase 5 — Disable flow + backup + polish
- Two-mode disable with 409 gate.
- `/backup` runs slapcat.
- Cert-expiry warning banner.
- Config-drift warning (hash of `/etc/clustr/ldap/slapd.d/` compared to last-seeded hash).
- **Commit:** `feat(ldap): add safe disable, backup, and drift detection`

---

## 7. Build & Deploy Policy (hard rule)

**Do NOT run `go build`, `go test`, `make`, or any compile-heavy command on the sqoia-dev workstation.** That host is resource-constrained; local builds have OOM'd it.

Instead:

1. Write code, commit, push to `origin/main` on `sqoia-dev/clustr`.
2. GitHub Actions CI (`ci.yml`) builds and tests on push — monitor with `gh run watch` per the standing CI-watch rule. If CI fails, land a corrective commit before marking the phase done.
3. `clustr-autodeploy.timer` on `192.168.1.151` polls `origin/main` every 2 minutes, rebuilds binaries and initramfs, hot-restarts `clustr-serverd`. Watch with:
   ```
   ssh -i ~/.ssh/id_ed25519 root@192.168.1.151 "journalctl -u clustr-autodeploy.service -f"
   ```
4. To test uncommitted changes on the VM: `systemctl stop clustr-autodeploy.timer` on the VM, rsync your tree, build there. Resume with `systemctl start clustr-autodeploy.timer`.

Static exploration (Read, Grep, Glob) is fine locally. Anything that invokes the Go toolchain is not.

---

## 8. Commit & Authorship

Per repo standing rule: all commits authored as `NessieCanCode <robert.romero@sqoia.dev>`. No Co-Authored-By lines. No Claude attribution. Set git config on first work in this repo:

```bash
git config user.name "NessieCanCode"
git config user.email "robert.romero@sqoia.dev"
```

Commit prefixes: `feat:` for new module code, `fix:` for CI corrections, `chore:` for scaffolding and systemd assets.

Push via the standard ssh-agent pattern in `CLAUDE.md`.

---

## 7a. V1 scope additions (2026-04-18)

- **Force-change-at-next-login:** `SetPassword(uid, password, forceChange bool)` sets `pwdReset=TRUE` atomically when the operator checks the per-reset checkbox. This is a per-action decision, not a global setting — every admin password reset should be an explicit choice about whether to force rotation.
- **Last-login tracking:** `pwdLastSuccess` operational attribute requested explicitly in `ListUsers` LDAP search and surfaced as a "Last Login" column in the user table with relative-time display (Never / Just now / Nm ago / Nh ago / Nd ago / 30d+). Computed client-side from the ISO timestamp.

## 9. Out of Scope (v2)

- Replication / multi-master / HA. Single authoritative server with a 5-minute restore procedure is the correct HA story for a single cluster.
- Retroactive push-config to nodes imaged before enable. V1 requires reimage.
- SSH AuthorizedKeysCommand → LDAP integration (the `sshPublicKey` attribute can be stored but sssd-side `AuthorizedKeysCommand` wiring is v2).
- NFS home directories / autofs — separate future module.
- sudoRole / LDAP-backed sudo — opt-in in v1 but no dedicated UI.
- Audit log of LDAP write operations.
- LDAP restore from backup via UI. CLI-only with a documented procedure.
- Argon2 password hashing. SSHA512 until the RHEL packaging catches up.
- Cert auto-renewal notification email. Status banner only.
- ~~ppolicy overlay~~ — done in v1 (2026-04-18).
- ~~Force-change-at-next-login~~ — done in v1 (2026-04-18, per-reset checkbox).
- ~~Last-login column~~ — done in v1 (2026-04-18, pwdLastSuccess relative time).

---

## 10. Known Risks

| Risk | Mitigation |
|---|---|
| Operator disables module while nodes still use it | Two-mode disable; 409 with affected node list unless `nodes_acknowledged: true`; docs point to de-provision path. |
| Base DN change after first node | Locked in DB once first node reports configured; UI hides the field. |
| Node TLS fails due to missing IP SAN | SAN generator in `cert.go` pulls hostname, primary IP, and `clustr.local` by default. Log and surface TLS errors from reimage clearly. |
| argon2 upgrade in v2 | Currently SHA-512-crypt is weaker than argon2id; v2 path is LTB Project's pw-argon2.so or building from contrib. |
| clustr user can't control clustr-slapd.service | Install script places polkit rule; enable flow errors out clearly if polkit denies. |
| Operator manually edits cn=config | Health watcher hashes `/etc/clustr/ldap/slapd.d/`; drift surfaces as a warning, not an error. Module keeps operating. |
| Secrets (admin DN password, service bind password) leak via logs | Never log passwords. Redact request bodies on the enable/disable handlers. |

---

## 11. Acceptance Criteria

- [ ] Fresh clustr install with module disabled behaves identically to today.
- [ ] Enabling the module from the webui brings up a working `ldaps://:636` endpoint with a valid self-signed cert chain in under 30s.
- [ ] Creating a user + group in the webui, reimaging a node, and SSH'ing into that node with the LDAP user succeeds — including `pam_mkhomedir` creating the home directory on first login.
- [ ] Disabling in `detach` mode stops slapd without touching data; re-enabling brings it back.
- [ ] Disabling in `destroy` mode requires `nodes_acknowledged` if any node is marked `ldap_configured`.
- [ ] CI is green on every pushed commit.
- [ ] No `go build` / `go test` / `make` invoked locally during development.
