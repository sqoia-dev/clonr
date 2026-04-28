# clustr Architecture Review

Review date: 2026-04-25
Reviewer: Richard (Technical Co-founder / Architecture)
Inputs: full read of `cmd/`, `internal/server/`, `internal/deploy/`, `internal/image/`,
`internal/power/`, `internal/pxe/`, `internal/db/`, `internal/reimage/`,
`pkg/api/`, `pkg/client/`, all 36 migrations, `docs/boot-architecture.md` §8 and
§10, `docs/webui-review.md`.
Scope: architecture only. Implementation review owned by Dinesh; infra/ops owned
by Gilfoyle. This doc feeds the 90-day sprint plan.

---

## 1. Architecture map (one-page mental model)

```
                        ┌────────────────────────────────────────────┐
                        │  cmd/clustr-serverd  (single Go process)   │
                        │                                            │
   PXE/iPXE  ─────►  internal/pxe        (DHCP + TFTP + iPXE script) │
                          │                                          │
                          ▼                                          │
   /api/v1/* ─────►  internal/server     (chi router, sessions,      │
                        ├─ handlers/      auth, SSE/WS hubs)         │
                        │   ├─ boot.go       — serves vmlinuz/initramfs
                        │   ├─ nodes.go      — register, deploy callbacks
                        │   ├─ images.go     — CRUD + blob serve
                        │   ├─ factory.go    — pull/import/build/capture
                        │   ├─ reimage.go    — REST shim → orchestrator
                        │   ├─ node_groups.go— group CRUD + group-reimage
                        │   ├─ ipmi.go,      — power surface
                        │   │   power.go
                        │   ├─ logs.go       — SSE + REST + ingest
                        │   ├─ progress.go   — deploy SSE
                        │   ├─ initramfs.go  — initramfs build mgmt
                        │   ├─ layout.go     — disk layout resolve
                        │   ├─ clientd.go    — clientd WS hub + exec
                        │   └─ shell_ws.go   — image shell WS
                        │                                            │
                        ├─ internal/reimage   — Orchestrator (Trigger,
                        │                       Scheduler, group runner)
                        │                                            │
                        ├─ internal/image     — Factory (pull, import,
                        │   factory.go (2.3k) build, capture, finalize)
                        │   shell.go          — image shell sessions
                        │   metadata.go       — sidecar
                        │                                            │
                        ├─ internal/deploy    — runs INSIDE the deploy
                        │   rsync.go (2.4k)     initramfs on the node
                        │   finalize.go (2.4k)  (compiled into clustr-static)
                        │   block.go, raid.go, zfs.go, network.go,
                        │   efiboot.go, phonehome.go, sysaccounts.go
                        │                                            │
                        ├─ internal/power     — Provider interface +
                        │   power/ipmi          Registry + 2 backends
                        │   power/proxmox                            │
                        │                                            │
                        ├─ internal/{ldap,    — feature modules with
                        │   slurm, network,     own DB tables, server
                        │   sysaccounts}        background workers
                        │                                            │
                        └─ internal/db        — single SQLite file,
                            db.go (2.5k)        36 migrations, all
                            slurm.go (1.2k)     persistence in one pkg
                            network.go (660),
                            ldap.go, …
                        │                                            │
                        ▼                                            │
                   SQLite (single file, WAL, FK on)                  │
                        │                                            │
                        ▼                                            │
                  internal/server/ui  — vanilla JS SPA               │
                  (app.js 8 kLOC + slurm.js 2 kLOC + …)              │
                        │                                            │
   Browser   ─────►  /index.html                                     │
                        ▲                                            │
                        │   (SSE: logs, progress, build-progress)    │
                        │   (WS:  shell, clientd, log streaming)     │
                        └────────────────────────────────────────────┘

  Deployed node ──► clustr-clientd (long-lived WS to /clientd/ws)
                    clustr-static  (one-shot, runs in deploy initramfs)
```

Key architectural facts:

1. **Single process, single binary, single SQLite file.** No external services
   (PXE/TFTP/HTTP all in-process). This is *correct* for current stage and is
   load-bearing for the open-source pitch (one container, no Postgres dep).
2. **`internal/deploy` runs on the node, not the server.** Compiled into
   `clustr-static` and executed inside the deploy initramfs. Server and deploy
   share no runtime — only the wire protocol via `pkg/api`. This is a clean
   boundary.
3. **`pkg/api` is the wire contract** between server, `clustr-static`,
   `clustr-clientd`, and `pkg/client`. It is the only package that crosses
   process boundaries. It is currently used as both the wire contract AND the
   internal domain model; see §3.2.
4. **The Orchestrator is the only thing that mutates power state.** Handlers
   call into the orchestrator; the orchestrator owns the power provider lookup
   and the `reimage_pending` lifecycle. This is good.
5. **`internal/image/factory.go` is the only multi-thousand-line file that is
   monolithic by accident**, not by design. See §3.1.

---

## 2. What is structurally sound — DO NOT REWRITE

These are the architectural calls that are right and should stay through the
next year of growth.

### 2.1 The Power `Provider` interface

`internal/power/power.go` defines a 6-method interface (Status, PowerOn,
PowerOff, PowerCycle, Reset, SetNextBoot, SetPersistentBootOrder) plus a
Registry indirection. After §10's contract sharpening, this is correct shape:

- One-shot `SetNextBoot` + durable `SetPersistentBootOrder` + `ErrNotSupported`
  cleanly absorbs the IPMI/Proxmox semantic mismatch without leaking it into
  callers.
- The Registry pattern keeps backend `init()` registration uncoupled from the
  orchestrator. Adding Redfish, vSphere, AWS EC2, libvirt is a new package +
  `Register(registry)` call — no orchestrator changes.
- The "two methods, not three" decision (no separate `SetNextBootOnce` vs
  `SetNextBootPersistent`) is the right call per §10.5: the abstraction
  guarantees one-shot observability; backends compensate internally.

This abstraction is earning its keep. Leave it.

### 2.2 Two-phase deploy verification (ADR-0008)

The `deploy_completed_preboot_at` / `deploy_verified_booted_at` /
`deploy_verify_timeout_at` triplet on `node_configs`, plus the verify-boot
scanner in `internal/server/server.go:207`, is the right model. It is the
distinguishing feature vs. xCAT/Cobbler ("did the rootfs write succeed" is not
the same as "the OS actually boots"), and the migration was done correctly with
backfill.

Keep. Do not consolidate into a single `last_deploy_state` enum — the three
timestamps independently encode information that an enum loses (specifically:
"OS wrote OK but never came up" is recoverable; "OS wrote OK and booted but
then died" is a different incident).

### 2.3 The `reimage_requests` table as the system of record for reimage history

The `reimage_requests` table (migration 008 + 022) with `dry_run`, `phase`,
`exit_code`, `exit_name`, `requested_by`, `scheduled_at` is the correct shape:
one row per intent, terminal-state details captured for forensics, reusable as
audit log. The webui review correctly notes `requested_by` is hardcoded to
`"api"` — that's a fix, not a redesign. The table itself is right.

### 2.4 The `ReimagePending` flag as the PXE handler gate

`node_configs.reimage_pending` as a single-bit flag that the iPXE-script
generator reads (`internal/pxe/boot.go`) is the correct level of indirection
between "operator wants this node redeployed" and "next PXE boot routes to
deploy initramfs." It collapses what would otherwise be a state machine in
firmware-land into one column read on every PXE request. Keep.

### 2.5 The deploy/server split

`internal/deploy/*` runs on the node inside the deploy initramfs and shares no
runtime with the server. The only things that cross are HTTP requests
(`POST /deploy/progress`, `POST /nodes/{id}/deploy-complete`, etc.) and the
node-scoped API key. This means the server can be rebuilt and restarted
without affecting an in-flight deploy; an in-flight deploy only fails if its
HTTP calls back to the server fail. That's a strong property. Keep.

### 2.6 Node-scoped API keys with auto-mint at PXE-serve time

`internal/server/apikeys.go:CreateNodeScopedKey` mints a fresh node-scoped key
each PXE serve, atomically revoking old keys for the node. The
`requireNodeOwnership("id")` middleware ensures the scoped key can only act on
its own node. This is correct security design at the right level — no shared
deploy secret, no per-node static configuration, no human in the loop. Keep.

### 2.7 The Orchestrator as the sole power-state mutator

`internal/reimage/Orchestrator.Trigger` owns: provider resolution → image
assignment → `reimage_pending=true` → SetNextBoot → PowerCycle → status
update. There are no other code paths that PowerCycle a node as part of
reimage flow. This concentration is the right call — it makes the failure
modes enumerable and the recovery logic local.

### 2.8 `pkg/client` as a separate package

A typed Go client in `pkg/client/` (522 LOC) with `pkg/client/logger.go` and
`pkg/client/progress.go` keeps `clustr-clientd`, `clustr-static`, and the
forthcoming MCP server from re-implementing HTTP scaffolding. The current
shape is right — keep it.

---

## 3. What is wrong-shape and needs rework

Listed in priority order. "Wrong-shape" means the abstraction is at the wrong
level, the boundary is at the wrong place, or there is duplicated logic that
will diverge.

### 3.1 The Image Factory has 5 finalize paths that duplicate post-extraction logic — P1

`internal/image/factory.go` (2,265 LOC) exposes 5 async entry points:

- `pullAsync(imageID, url)` — line 226
- `importISOAsync(imageID, isoPath)` — line 434
- `captureAsync(imageID, req, sshUser, sshPort)` — line 690
- `buildISOAsync(imageID, req, distro)` — line 1532
- `ResumeFromPhase(imageID, img, phase)` → `resumeFinalize(...)` — line 2004

Each ends with the same conceptual sequence: detect arch, write deterministic
tar, sha256, set image metadata, mark image `ready`, write metadata sidecar,
emit progress completion. Today these are inlined, not factored. Symptom:
ADR-0008 verify-boot logic, the metadata sidecar (mig 023), and the resume
phase plumbing (mig 018) each had to be added in 5 places. Some were missed
(per the webui review, the metadata endpoint exists but is not always populated
consistently across paths).

**Architectural call**: converge to a single `Finalize(ctx, imageID, rootfsPath,
buildHandle, sourceMetadata) error` called by 5 thin entry points
(`pullAsync`, `importISOAsync`, `buildISOAsync`, `captureAsync`, `resumeFinalize`).
Each entry point's job becomes "produce a rootfs directory + source metadata,
hand to Finalize." Reduces the 2,265 LOC factory to ~1,200, and makes ADR-0008
or similar future additions a one-place change.

Defer the rewrite of `pullAndExtract` / `extractISO` / `extractLiveOS` /
`buildISOFromISO` themselves — those are different by necessity (different
sources). Only the post-rootfs sequence converges.

### 3.2 `pkg/api` mixes wire types and domain types — P2

`pkg/api/types.go` is consumed both as:

1. The HTTP wire contract (JSON tags, omitempty, REST response shapes).
2. The internal domain model (passed through `internal/db`, `internal/reimage`,
   `internal/deploy`, methods like `NodeConfig.State()`).

This is fine *today* (single deployment, no clients beyond ours, no schema
versioning needs). It will hurt within 12 months because:

- API versioning (`/api/v1/` → `/api/v2/`) requires distinct response types,
  but domain code uses the same struct.
- `NodeConfig` carries 30+ fields including transient fields (`ClusterHosts`),
  derived fields (`HostnameAuto`), and persisted fields. The persistence
  layer cannot distinguish.
- Adding RBAC / per-field redaction (e.g. don't return `BMC.Password` to
  read-only role) requires duplicating the struct anyway.

**Architectural call**: defer for the 90-day window but plan for a split:
`internal/domain/` holds the pure domain types; `pkg/api/v1/` is generated or
hand-mapped wire types. The split happens when *either* (a) we add `/api/v2/`,
or (b) we add RBAC field-level redaction, whichever comes first. Until then,
add a comment on `NodeConfig` documenting "this is both wire and domain — do
not add transient fields without `,omitempty` and a comment."

### 3.3 `internal/db/db.go` is 2,459 LOC, monolithic — P2

The `db` package has been split across files (db.go, slurm.go, network.go,
ldap.go, sysaccounts.go, apikeys.go, heartbeats.go, slurm_builds.go,
users.go) but `db.go` itself is still 2.5 kLOC and contains: base_images,
node_configs, reimage_requests, deploy_events, node_logs, node_groups,
group_memberships, group_reimage_jobs, image resumable state, initramfs
builds. Migrations are 36 files; new feature modules each add a migration
plus DB methods.

**Architectural call**: the file-level split is fine. The package-level
shape is wrong-ish but not catastrophic. Recommend: as new features land,
keep the discipline of one file per concept (already the pattern); when
we do the wire/domain split (§3.2), introduce an interface
`internal/db.Store` and let each subsystem declare the subset of methods
it needs. This makes testing easier (smaller mocks) and surfaces unintended
coupling. **Do not do this in the next 90 days** — it is a deferred
quality move, not a velocity blocker.

### 3.4 In-process scheduler + SQLite + single server is fine *today* but the path to HA is unspecified — P2 (architectural debt, not bug)

Current model:

- Reimage Orchestrator runs in-process, ticks every 30s, holds no state
  beyond `o.DB`. Stateless tick + DB-as-truth is the right model.
- Group reimage runs in a goroutine per job, no leader election.
- SQLite WAL mode, single writer.
- WebSocket clientd hub + SSE log broker are in-memory pub/sub keyed by
  process.

When this breaks:

| Trigger | What breaks | Lead time |
|---|---|---|
| `clustr-serverd` restart mid-deploy | Deploy continues (deploy is on the node), but logs in-flight to the server are dropped between the pre-restart batch and reconnect; SSE subscribers reconnect on their own; **clientd WS connections from deployed nodes drop and reconnect (client-side handles this)**. Net: no data loss in DB, transient log gap. | Working today. |
| 2x clustr-serverd instances behind a load balancer | SSE/WS hubs do not share state — a clientd WS landing on instance A is invisible to instance B. Group reimage workers would double-fire on tick collision. SQLite cannot be shared. | First HA sprint (out of 90-day window). |
| 500+ concurrent log SSE subscribers | LogBroker is a fan-out per subscriber; goroutine count and SQLite contention rise linearly. Not a cliff, but degrades. | At ~200-node deploy with multiple operators streaming. |
| 1000+ nodes, 100/s log ingest each | SQLite write contention dominates; current `idx_node_logs_mac` index helps reads but writes are per-row insert via batch. Pruning is hourly TTL only. | At ~50-100 nodes for an active deploy. |

**Architectural call**: the *code shape* is HA-ready in the right places
(orchestrator is stateless, DB is truth, deploy is autonomous on the node).
The *infrastructure* is single-server and that's correct for stage. To
preserve the option to scale:

- **Do not** introduce in-memory state in handlers that isn't recoverable
  from DB. (Currently respected; keep enforcing in code review.)
- **Do not** make the group reimage runner write progress only to memory
  — it already writes to `group_reimage_jobs` row, good.
- When we hit the SQLite ceiling (likely 6-12 months out at observed
  growth), we will swap to PostgreSQL behind a tiny `internal/db.Store`
  interface. The migrations will need a Postgres dialect (currently
  SQLite-specific in places — `INTEGER`-as-timestamp, no schemas). Plan
  for it; do not do it now.
- HA / multi-server is post-Series-A territory. Do not architect for it
  in the next 90 days.

### 3.5 The `groups` (freeform) vs `group_id` + `node_group_memberships` — P0 (founder escalation, see §4.2)

Three coexisting grouping mechanisms:

1. `node_configs.groups TEXT[]` — freeform comma-separated, used by Slurm
   role assignment.
2. `node_configs.group_id` — single FK to `node_groups`, used for
   layout/mounts inheritance.
3. `node_group_memberships(node_id, group_id)` — many-to-many, used for
   group-reimage and the new role column.

These were added incrementally (mig 001 → 011 → 021). Architectural call in §4.2.

### 3.6 The monolithic 8,005 LOC `app.js` SPA — P1 (frontend architecture)

Out of scope for backend review but called out for the sprint plan:
vanilla JS, no build step, ~40% inline `style=` strings, parallel editing
surfaces (modal vs detail page) per the webui review. This is shipped
debt that compounds with every new feature. **Architectural call**: do
not adopt React/Vue (build complexity contradicts the "one binary, one
container" pitch); do consolidate to module-per-page, kill `confirm()`/
`alert()`, route everything through one editing surface per resource.
Implementation specifics belong to Dinesh, not this doc.

### 3.7 Handler-Server cyclic-import workarounds — P2

`internal/server/handlers/clientd.go` declares `ClientdDBIface` and
`ClientdHubIface` to avoid importing the server package. Same pattern in
`logs.go` (`LogBroker`, `LogsHubIface`). This is a circular-import
workaround dressed as DI.

**Architectural call**: it's not wrong, it just shouldn't be necessary.
The cleaner shape is a `types/` or `services/` package that holds the
interfaces, with both `server` and `handlers` depending on it. **Defer**.
Cosmetic. Will be cleaned up naturally when we do the §3.2 wire/domain
split.

### 3.8 The image build progress reporter has 4 layers of indirection — P3

`Server` → `buildProgressAdapter` → `BuildProgressStore` →
`BuildHandle`/`buildHandleAdapter` → SSE subscribers. The adapter pattern
is correct (decouples `image` package from `server`-specific SSE), but
there are 4 types where 2 would do. Not blocking. Defer.

---

## 4. Founder escalation responses

### 4.1 RBAC model — RECOMMENDATION: three-tier role + group-scoped operator role

**Current state**: `users.role` is `admin | operator | readonly` (mig 025
added the column with a CHECK constraint). API key scope is `admin | node`.
`requireRole("admin")` middleware exists. There is no permission
enforcement for `operator` or `readonly` — they are just labels.

**Options considered**:

1. **Pure role-based** (admin / operator / readonly, no scoping). Simple,
   maps to user expectations. Admin gets everything, operator gets
   non-destructive cluster ops, readonly gets GET only.
2. **Group-scoped operator** (admin / operator-of-group(s) / readonly).
   Operator is constrained to specific NodeGroups — can only reimage,
   power-cycle, ssh-key-update, etc. nodes in groups they own. This is
   what HPC sysadmins ask for (Persona A in webui review).
3. **LDAP-group-passthrough** (any LDAP group becomes a clustr role).
   Defers role definition to LDAP. Maximum flexibility but ties RBAC to
   LDAP being deployed.
4. **ACL-per-resource** (per-node, per-image permission lists). Most
   flexible, most complex. xCAT-class.

**Recommendation: Option 2 — three-tier role + group-scoped operator
role.** The permission matrix is:

| Action | admin | operator | readonly |
|---|---|---|---|
| GET (any) | yes | yes | yes |
| POST/PUT/DELETE on images | yes | yes | no |
| POST reimage on a node | yes | only if node in operator's group(s) | no |
| Power on/off/cycle a node | yes | only if node in operator's group(s) | no |
| Modify NodeConfig | yes | only if node in operator's group(s) | no |
| Create/delete NodeGroup | yes | no | no |
| Manage api-keys, users | yes | no | no |
| Manage Slurm/LDAP/Network module config | yes | no | no |
| Trigger group reimage | yes | only if group is in operator's groups | no |

The "operator-of-groups" mapping lives in a new join table
`user_group_memberships(user_id, group_id, role)` where role is `operator`
(extensible to `viewer-of-group` later). Admin role bypasses the table;
readonly bypasses too (read-only for everything they can see).

**Why this and not Option 1**: Persona A (200-node university) is the
highest-value HPC use case and it requires per-team scoping
("postdoc Alice can reimage gpu-team-a nodes, not gpu-team-b"). Pure
role gives 1 admin + N operator-everywheres which doesn't match real
org structures.

**Why not Option 3**: ties RBAC to LDAP being deployed. We have customers
running clustr without LDAP (Persona B, C, D). RBAC must work standalone.
LDAP-group-passthrough can be added on top of Option 2 later as a way
of populating the group-membership table.

**Why not Option 4**: cost too high for stage. xCAT's RBAC is a known
operator pain point — let's not copy it.

**Confidence**: high (80%). Kill criterion: if first design partner asks
for "row-level read scoping" (operator can't even SEE nodes outside
their group), we revisit. Today none have asked.

**Implementation note for Dinesh** (out of architectural scope, but for
sprint sequencing): the API surface change is small — a single
`requireGroupAccess(nodeIDParam)` middleware that admins bypass,
operators check membership for, readonly rejects. The DB change is one
new table + one CHECK constraint. The webui change is the larger lift
(per-role nav gating, per-action buttons gated on operator's groups).

### 4.2 `groups[]` vs `NodeGroup` conflict — RECOMMENDATION: keep both, rename, document

**Current state**:

- `node_configs.groups TEXT[]` — freeform string labels. Used by Slurm
  module to decide which `NodeName=` lines to emit per role. Used by the
  UI nodes-page filter.
- `node_configs.group_id` — single FK to `node_groups`. Used by
  `EffectiveLayout()` and `EffectiveExtraMounts()` for inheritance.
- `node_group_memberships` — many-to-many, used by group-reimage and
  appears to be the strategic future direction (it backfilled from
  `group_id` in mig 021).

**Options**:

1. **Deprecate `groups[]`, fold into `NodeGroup` membership.** Slurm role
   matching becomes "node belongs to a NodeGroup with role=compute,
   role=login, etc." Removes one concept.
2. **Deprecate `group_id`, use only `node_group_memberships`.** Cleaner
   data model (one many-to-many table). Existing `group_id` consumers
   (layout resolution) switch to "first membership" or "primary
   membership."
3. **Keep both, rename for clarity.** `groups[]` becomes `tags[]`
   (matches the unused `tags` field on images — which we'd fold in too);
   `NodeGroup` stays as the structured grouping. Tags are unstructured
   labels for filtering and Slurm role hints; Groups are structured
   primary ownership with inheritance.
4. **Three-way: `tags[]` (string filter), `NodeGroup` (primary, single,
   inheritance), `Membership` (multiple groups for ops).** Most
   accurate to current usage.

**Recommendation: Option 3 — keep both, rename `groups[]` to `tags[]`,
treat `node_groups` as the sole "structured group" concept, and
deprecate `node_configs.group_id` in favor of the
`node_group_memberships` table with a "primary group" flag.**

Specifically:

- Rename `node_configs.groups` column → `node_configs.tags` (mig 037).
  Update `pkg/api.NodeConfig.Groups` → `Tags`. Wire migration path for
  one release: emit both fields on JSON for v1, accept either on input.
- Tags are unstructured strings (existing semantics). Used for: Slurm
  role matching, ad-hoc filtering, label-based image affinity (future).
- `node_group_memberships` becomes the sole structured grouping. Add
  `is_primary BOOLEAN` to the table; constrain to one primary per
  node via partial unique index. Migrate existing `group_id` values
  to `is_primary=true` membership rows (already backfilled in mig 021;
  just need the flag).
- `node_configs.group_id` column is deprecated, dual-read for one
  release, dropped in v1.0. `EffectiveLayout()` reads the primary
  membership instead.
- Image `tags[]` (already in DB, never surfaced — webui review §"API/UI
  Mismatches" #3) becomes operationally meaningful: "deploy images
  with tag=production to nodes with tag=production" is the future
  affinity rule. Until then, tags are pure metadata.

**Why not Option 1**: Slurm role matching by tag is more flexible than
group membership. A node may belong to one primary NodeGroup
(`gpu-team-a`) but be tagged `compute, gpu, slurm-partition-debug`.
Forcing all of those into NodeGroups makes group count explode and the
inheritance model meaningless.

**Why not Option 2**: kills the layout-resolution clean three-tier
fallback (node → group → image). The "first membership" or "primary"
patch is what Option 3 already does; might as well keep `group_id`
semantics under a rename and a new column.

**Why not Option 4**: too many concepts. Three layers when two would do.

**Confidence**: high (85%). Kill criterion: if a design partner tells us
they want multiple "primary" groups per node (i.e., true many-to-many
ownership, not "one primary + N secondary"), the model collapses to
Option 2 — but that contradicts how Persona A actually thinks about
HPC node organization.

### 4.3 Log retention model — RECOMMENDATION: hourly TTL purge with per-node cap, configurable retention, no archive

**Current state**:

- `node_logs` table: append-only, indexed by `(node_mac, timestamp)`,
  level, component.
- `runLogPurger` background worker (server.go:317) ticks hourly,
  deletes rows older than `CLUSTR_LOG_RETENTION` (default 14 days).
- No per-node row cap. No archival path. No log rotation to file.
- Ingest is rate-limited per-node at 100 req/s, batches of up to 500
  entries (`logs.go`).

**The math**: 200 nodes × 10-min deploy × 100 entries/sec × 500-byte
avg row ≈ 6 GB per deploy event. Multiplied across an active fleet,
this is the disk-pressure issue Gilfoyle raised in webui-review Q3.
14-day retention means a 200-node cluster doing daily reimages
holds ~84 GB of logs in SQLite. SQLite handles that, but FTS-less
queries get slow and the DB file gets large.

**Options**:

1. **Status quo** — hourly TTL only. Simple. Bombs at scale.
2. **TTL + per-node row cap** — keep last N rows per node regardless
   of age. Bounds disk by node count, not by deploy frequency.
3. **TTL + size cap on the log table** — cap total table size, evict
   oldest. Bounds disk absolutely.
4. **TTL + archive to compressed file on disk before purge.**
   Operators can grep historical logs from disk if needed. Adds an
   archive directory to manage.
5. **Move logs to a separate SQLite file**, attached as needed, so the
   primary DB stays small.
6. **External log sink** (Loki, OpenSearch, S3). Adds runtime dep.
   Contradicts open-source self-hosted single-binary pitch.

**Recommendation: Option 2 — TTL + per-node row cap, configurable,
no archive in v1.**

Concretely:

- Default retention: **7 days** (down from current 14 — empirical).
- New: per-node hard cap of **50,000 rows**, evicting oldest first.
  At ~500 bytes/row this is ~25 MB per node = ~5 GB at 200 nodes,
  bounded.
- Both configurable via env: `CLUSTR_LOG_RETENTION` (already exists),
  `CLUSTR_LOG_MAX_ROWS_PER_NODE` (new).
- Purger runs hourly, evicts in two passes per cycle: TTL evict, then
  per-node cap evict.
- A new `node_logs_summary` table records counts at purge time so
  operators can see "you lost 12,000 lines of node X logs at
  2026-04-25T14:00 due to row cap" — telemetry for tuning.
- No archive file in v1. Operators who want long-term log retention
  point an external scraper at `/api/v1/logs/stream` (the hook is
  already there). v2 *might* add Option 4 if a customer asks.

**Why not Option 5 (separate DB file)**: it's a refactor for a problem
we don't have yet. Also complicates the "one container, one volume"
deployment. Defer until size of the primary DB blocks startup.

**Why not Option 6 (external sink)**: violates the "no external deps"
positioning. Even if optional, the documentation surface and support
matrix grows. Operators who need this can run their own scraper.

**Why not Option 4 (archive files)**: managing a directory of compressed
log files adds ops complexity (rotation, cleanup, format stability,
search tooling). Customers with serious log retention needs will run
their own log infra anyway.

**Confidence**: high (90%). The math is clean; the eviction policy is
standard. Kill criterion: operator complaint that "I needed log line X
from 30 days ago and it was evicted by the row cap" — at which point
we either (a) bump defaults, (b) add Option 4, or (c) document
"point a scraper at `/api/v1/logs/stream` if you need long retention."
Today none of those is requested.

---

## 5. Risk inventory (observation, not opinion)

### 5.1 Single points of failure

| SPOF | Blast radius | Mitigation today |
|---|---|---|
| `clustr-serverd` process | All UI, all REST, PXE, TFTP, log ingest. Deploys in flight on nodes continue (they don't depend on server runtime), but their callbacks fail. | None — restart resumes from DB. |
| SQLite file | All persisted state. Corruption = full rebuild. | WAL mode + FK on. No automated backup. |
| `cloner` host (192.168.1.151) for dev | Dev iteration loop. | Autodeploy script lives there too. |
| One initramfs per server | All deploys. Stale initramfs = all deploys boot the wrong kernel. | Manual "Rebuild Initramfs" button in UI. |
| Node-scoped key | Single deploy. Compromised key = single-node attack surface. | 30-day TTL + per-PXE-serve rotation. |
| Session HMAC secret | All UI sessions. If `CLUSTR_SESSION_SECRET` is unset, secret is generated per process start — sessions don't survive restart (logged as a Warn). | Documented as warn. |

The first two are the structural ones. SQLite corruption resilience and
DB backup are not addressed in code; this is a Gilfoyle-owned operations
question (recommend nightly snapshot to disk + an `/api/v1/admin/backup`
endpoint that triggers `VACUUM INTO`).

### 5.2 Untested critical paths (gaps in test coverage that matter)

| Path | Tested? | Risk |
|---|---|---|
| Proxmox provider stop+start sequence (§10) | Tests planned (§10.10) but not yet written | Medium. Hand-validated on vm202; first regression silently breaks dev. |
| Bare-metal IPMI provider against real hardware | No (no hardware in dev) | Known. Will surface on first bare-metal deploy. |
| Multi-node concurrent reimage (>10 nodes) at the orchestrator | `TestGroupReimage_DispatchesConcurrent` exercises 5 | Low. Cap is 5 by default; logical scaling. |
| Image factory `resumeFinalize` after server restart mid-build | No explicit test | Medium. The DB has the state; the code path may have bugs. |
| Verify-boot timeout scanner under DB load | No | Low. Single SQL query; non-critical timing. |
| LDAP / SSSD config rendering against a real LDAP server | No integration test | Medium. Module surface is large; rendering bugs surface only at deploy. |
| Slurm config rendering against a real Slurm cluster | No integration test | Medium. Same. |
| Power provider failure mid-orchestration (PowerCycle returns error after SetNextBoot succeeded) | Unit-tested via `failProvider` | Low. |

### 5.3 Implicit contracts not enforced

1. **`node_configs.reimage_pending` must be cleared on deploy-complete OR
   on cancel.** The PXE handler reads this; if neither call fires, the
   node loops PXE-deploy forever. There is no enforcement that a
   `triggered` reimage request always reaches a terminal state.
   Recommend: server-side reaper that scans for nodes with
   `reimage_pending=true` AND no `reimage_requests` row in non-terminal
   state and clears the flag (defensive).

2. **`SetPersistentBootOrder([disk, pxe])` must be called after a
   successful Proxmox deploy.** This is the §10 invariant. It is
   enforced in code (server-side flip on `deployed_verified` + flip on
   timeout + client-side `FlipToDisk`), but the implicit contract is
   "all three must succeed at least once." If all three fail silently,
   the Proxmox VM is stuck PXE-first. Recommend: a low-noise alert when
   the server-side flip-back fails (currently logged as Warn — escalate
   to a counter that surfaces in `/health`).

3. **Node-scoped API key TTL.** A 30-day TTL is set; nothing enforces
   that an expired key is revoked. The auth check rejects expired keys,
   but they accumulate as rows. Recommend: weekly purge of expired
   revoked keys.

4. **`base_images.status` transitions.** `building → ready → archived`.
   No state-machine enforcement; DB allows arbitrary transitions. Code
   discipline only. Add a CHECK constraint or a domain-level guard in
   `db.UpdateBaseImageStatus`.

5. **Initramfs SHA must match the kernel/modules of the latest base
   image.** Today this is operator-managed (manual rebuild). A stale
   initramfs deploys nodes that fail to find their root device. There
   is no enforcement at deploy time that the initramfs is fresh.
   Recommend: store the image's kernel version on initramfs build,
   compare on PXE serve, refuse to serve if mismatched (or warn loudly
   on the dashboard, per webui review P1-6).

### 5.4 State that lives in 2+ places and can drift

1. **`node_configs.groups[]` (text array) vs `node_group_memberships`
   table** — see §4.2.
2. **`node_configs.group_id` vs `node_group_memberships`** — see §4.2.
3. **Proxmox VM "running" config vs "pending" config** — Proxmox-side
   state, not clustr's. The §10 fix moves drift mitigation into the
   Proxmox provider itself.
4. **`node_configs.last_deploy_succeeded_at` vs
   `node_configs.deploy_completed_preboot_at`** — dual-write for
   back-compat (mig 024). Removable in v1.0.
5. **Server-side `reimage_orchestrator` in-process state vs DB** — the
   orchestrator is stateless by design, but the group reimage runner
   holds in-memory progress that is *also* written to
   `group_reimage_jobs`. On restart, `ResumeGroupReimageJob` exists
   (db.go:2091) but is not wired into a server-startup recovery path.
   Recommend: add a startup hook that calls `ResumeGroupReimageJob` for
   any `running` job whose owning process is gone.
6. **Power provider `Status()` cache vs reality** — `powerCache.go`
   caches power status for 15s. Operations that change state
   (PowerCycle) don't invalidate the cache. UI may show stale state for
   up to 15s. Acceptable today; document.
7. **`session_secret` ephemeral vs persistent** — if
   `CLUSTR_SESSION_SECRET` env var isn't set, it's generated per
   process start. Sessions don't survive restarts. Documented; not a
   bug.

---

## 6. Sequencing — what changes first, second, third (and what doesn't)

### Sprint 1 (weeks 1-3) — load-bearing fixes

These are P0 from the webui review plus the architectural fixes that
unblock everything downstream.

1. **§10 boot-architecture changes land in code** (Dinesh) — Proxmox
   provider stop+start, server-side flip-back, orchestrator timeout
   flip. Tests per §10.10. **This is the only thing blocking E2E
   stability on the dev VMs.**
2. **Webui P0-1: `GET /api/v1/progress` 404** — routing fix or alias.
   Dinesh.
3. **Webui P0-3, P0-4, P0-5**: kill `<details>` nav collapsibles,
   replace `confirm()`/`alert()` with modal pattern, add 401 →
   `/login` redirect on session expiry. Dinesh.
4. **Risk 5.3.5: stale-initramfs gate** — store kernel version on
   initramfs build, compare on PXE serve, surface warning. Dinesh.
5. **Founder escalation §4.3 (log retention)**: implement
   per-node row cap + new env var + dashboard counter. Dinesh.

### Sprint 2 (weeks 4-6) — architectural rework #1: Image Factory finalize convergence

1. **§3.1**: refactor `internal/image/factory.go` to a single
   `Finalize(ctx, imageID, rootfsPath, sourceMetadata) error` called
   by 5 thin entry points. Pull metadata sidecar writing into Finalize
   so it is consistent across pull/import/build/capture/resume. Dinesh.
2. **Webui P1-4 (image metadata endpoint never called)** + P1-3 (image
   tags). Surface them in the UI now that the backend is consistent.
3. **Founder escalation §4.2 (groups vs NodeGroup) — first half**:
   add `is_primary` to `node_group_memberships`, dual-read with
   `group_id`, document in code comments. Do **not** rename `groups[]`
   yet — that's an API break. Dinesh.

### Sprint 3 (weeks 7-9) — RBAC

1. **Founder escalation §4.1 (RBAC)**: implement Option 2
   (group-scoped operator role). New `user_group_memberships` table,
   `requireGroupAccess` middleware, per-action gating in handlers.
   Dinesh.
2. **Webui P1-9 (audit trail: requested_by carries user identity)**:
   trivial once RBAC is in place — handlers grab the authenticated
   user/key from context. Dinesh.
3. **Webui P1-1 (nodes-list search)**, P1-2 (`scheduled_at` in UI),
   P1-5 (power state column), P1-6 (initramfs staleness on dashboard).

### Sprint 4 (weeks 10-12) — quality and observability

1. **Risk 5.3.1 (reimage-pending reaper)** — defensive sweep.
2. **Risk 5.3.4 (image status state machine)** — add CHECK + domain
   guard.
3. **Risk 5.4.5 (group-reimage resume on startup)** — wire
   `ResumeGroupReimageJob` into server start.
4. **Webhook/callback API for deploy completion** (webui Persona C
   gap; P2-6).
5. **Test coverage**: integration test for the full Proxmox reimage
   cycle (§10.10.1 codified as a test); LDAP module integration test
   harness; Slurm module integration test harness.

### What explicitly does NOT change in the next 90 days

These are deliberate non-goals. Pre-empt the temptation:

1. **Do not split `pkg/api` into wire and domain types.** Defer until
   we add `/api/v2/` or field-level RBAC redaction.
2. **Do not migrate to Postgres.** SQLite is fine until we hit ~50-100
   active nodes per server. It is reversible (`internal/db.Store`
   interface lift when we get there).
3. **Do not introduce a build step for the SPA.** The `app.js`
   refactor is consolidation, not framework adoption.
4. **Do not add HA / multi-server.** Out of stage. The orchestrator
   shape is HA-friendly; that's all we need to preserve.
5. **Do not add an external log sink integration.** Keep the
   "self-hosted, one binary" pitch.
6. **Do not deprecate the `groups[]` field in this window.** Rename
   path is a v2 API concern.
7. **Do not add Redfish or vSphere providers.** The interface is
   ready; demand isn't.
8. **Do not split `internal/db/db.go` into smaller packages** — file
   split is fine, package split is a deferred quality move.
9. **Do not refactor `internal/deploy/finalize.go` (2,363 LOC)**
   despite its size. It is a cohesive single concern (write a
   bootable rootfs into `mountRoot`). Splitting it makes the cross-
   step state harder to reason about. The size is justified.
10. **Do not add a generic event-bus/pub-sub abstraction.** SSE for
    UI clients + WS for clientd is the right shape. Resist the urge
    to "unify" them via a NATS/Kafka layer.

---

## 7. Closing observations

1. The architecture is, on the whole, **structurally sound for stage**.
   The boot architecture (§8 + §10 of `boot-architecture.md`) is a
   high-quality piece of architectural discipline — diagnosing the
   actual failure mode, surveying the reference systems, and choosing
   the hybrid model rather than a universal abstraction is exactly the
   work that prevents 6 months of follow-on bugs.
2. The single biggest velocity drag in the next 90 days is **the
   image factory's 5 finalize paths** (§3.1). Every cross-cutting
   feature (verify-boot, metadata sidecar, resumable builds) has paid
   the 5x duplication tax. Convergence is the highest-leverage
   refactor on the table.
3. The single biggest **future** risk is the **wire/domain coupling in
   `pkg/api`** (§3.2). It will not bite in the 90-day window. It will
   bite hard the day we add a second API version or row-level RBAC. We
   should be ready to do the split when triggered, not before.
4. The webui is the weakest part of the codebase by code quality, but
   that is a knowable, bounded debt — 8 kLOC of vanilla JS — not an
   architectural mistake. The SPA-vs-no-build choice is correct for
   stage; the implementation discipline inside it is the issue.
5. The **groups vs NodeGroup mess** (§4.2) is the kind of thing that
   gets harder every week we defer it. Rename to `tags[]` and pick
   `node_group_memberships` as the structured-grouping winner is a
   clean call we can land in Sprint 2 without breaking the public API.
6. The RBAC choice (§4.1) is the most consequential **product**
   decision in the review. Get it wrong and we will rebuild it in 12
   months under pressure. Group-scoped operator is the right answer
   for HPC; single-tenant homelabbers don't notice; CI bots use
   node-scoped keys (already correct).
