# clustr — Architectural & Sprint-Plan Decisions (Locked)

**Date:** 2026-04-26
**Author:** Richard (Technical Co-founder)
**Audience:** Sprint execution agents (Dinesh, Gilfoyle, Jared, Monica) + future reviewers
**Status:** LOCKED. These supersede prior `?` markers in `architecture-review.md` and the founder decision points in `90-day-sprint-plan.md`. Re-decisions require a written counter-rationale and a founder OK.

This is the operating reference. Every sprint task is bound to one or more of these calls. If a sprint task can't be executed against a decision below, escalate before deviating.

Reversibility legend:
- **cheap** — re-decide in a sprint, no migration pain
- **costly** — re-decide in a quarter, requires data migration or API rework
- **one-way** — bakes into wire contracts / external integrations / persisted user trust; re-decide only with major-version break

---

## Decision Index

| # | Topic | Call | Reversibility |
|---|---|---|---|
| D1 | RBAC model + auth mechanism | 3-tier + group-scoped operator; sessions for humans, API keys for machines, bootstrap admin via `clustr-serverd apikey create` first-run script; OIDC/LDAP-bind deferred to v1.1 | costly |
| D2 | Log retention model | TTL + per-node row cap (Richard's option). 7-day TTL, 50K rows/node default. Env-var configured. No cold archive in v1.0. | cheap |
| D3 | `groups[]` → `tags[]` migration | Dual-emit at JSON layer in Sprint 2 (S2-4). Field stays through v1.0. Removal at v1.1, NOT v1.0. | costly |
| D4 | LDAP credential encryption | Sprint 1 placement (moved up from S2-12). AES-256-GCM, sealed by `CLUSTR_SECRET_KEY`. Plaintext re-encrypted at first-start migration. | cheap |
| D5 | Image factory consolidation | `Finalize(ctx, imageID, rootfsPath, sourceMetadata)` signature. 5 entry points become thin wrappers. Status state machine via DB CHECK. | cheap |
| D6 | HA / scaling | Single-server through v1.0 and beyond. Postgres trigger: sustained SQLite write contention OR a customer signs that needs >50 concurrent active deploys. SSE/WS scale: target 100 concurrent clients before optimizing. | costly |
| D7 | Packaging strategy for v1.0 | Docker Compose (primary) + Ansible role (secondary). No Helm, no operator, no apt/rpm. | cheap |
| D8 | External security review | Yes, conditional. `gosec` + `trivy` in CI from Sprint 3. Human pen-test scoped to PXE chain + RBAC, $5K budget cap, engaged at end of Sprint 5 only IF first design partner has been signed. Otherwise defer to v1.1. | cheap |
| D9 | GPU detection | Keep in Sprint 5. NVIDIA-only via `lspci` PCIe vendor ID `0x10de`. CUDA version surfaced from image metadata sidecar (already collected in build). No driver matching enforcement in v1.0. | cheap |
| D10 | Webui framework | Stay vanilla JS through v1.0. Kill `<details>` / `confirm()` / `alert()` per existing sprint backlog. Module-split `app.js` deferred to v1.1 candidate-list. | costly |
| D11 | Test infrastructure | Real OpenLDAP container in CI (testcontainers-go). Golden file fixtures for network profiles + Slurm. In-memory SQLite per test. | cheap |
| D12 | Documentation strategy | In-repo `docs/` (Markdown). Single `README.md` quickstart by end of Sprint 4. `docs/install.md` + `docs/upgrade.md` by Sprint 6. No Docusaurus / mkdocs site for v1.0. | cheap |
| D13 | Audit log scope | Internal-product retention (default 90 days, configurable). No SIEM export in v1.0. JSONL export endpoint in v1.1 if a regulated customer signs. | cheap |
| D14 | Initramfs auto-rebuild | NOT automated in v1.0. Stale-initramfs warning surfaced in dashboard + WARN at PXE serve. Manual rebuild remains the trigger. | cheap |
| D15 | Hiring trigger | First paying design partner with >50 nodes signs an LOI. Until then, no hires. | cheap |
| D16 | Show HN timing | Independent from tunnl. Post-v1.0 ship (Sprint 6 + 1 week buffer). No pre-launch beta announcement. | cheap |
| D17 | Slurm controller dual-role default | Controller runs slurmd by default (dual-role `["controller","compute"]`). Operator opts out by editing role list. Documented as small-cluster-friendly default. | cheap |
| D18 | Template -> DB sync mechanism | `is_clustr_default` boolean on `slurm_config_files` (default true for seed-time rows, false for API writes). Admin endpoint `POST /api/v1/slurm/configs/reseed-defaults` re-seeds only rows where current version is still flagged default. Operator-edited rows never touched. | cheap |
| D19 | Customizability default (webui) | Lock the knob, ship the recommendation, expose under "Advanced" disclosure. NOT "expose every knob with a sensible default." | cheap |
| D20 | CLI/UI parity policy | Routine ops (more than once per cluster lifetime) MUST have webui surface. CLI-only acceptable for Day-0 bootstrap and post-disaster recovery only. | costly |
| D21 | JS framework threshold | Vanilla JS + ES6 modules through v1.2. Re-evaluate framework adoption ONLY when (a) total LOC >5000 across all `pages/*.js` + `app.js`, (b) frontend hire with framework expertise lands, (c) feature requires complex state machines vanilla can't handle, OR (d) CSP enforcement becomes priority. Evaluate Alpine+HTMX first; React/Vue/Svelte only if Alpine/HTMX insufficient. | costly |
| D22 | Raw config editor pattern | Structured form for the 80% case + "Advanced: edit raw" disclosure + server-side validation on every save (regardless of which surface produced the input). Applies to all current and future config surfaces (slurm.conf, kickstart, network profiles, BMC). | cheap (per-surface) |
| D23 | JS framework choice | Alpine.js 3 + HTMX 2, vendored under `internal/server/ui/static/vendor/`, no build step. Adopted Sprint C (v1.2.0) on greenfield Researcher portal. Mixing with vanilla supported indefinitely. | costly |
| D24 | Powerhouse positioning thesis | clustr is the only open-source platform that unifies bare-metal HPC provisioning with allocation governance in a single Go binary. Closes the decade-old gap between node provisioners (xCAT/Warewulf) and allocation managers (ColdFront). Self-hosted, air-gap native, cryptographically verified trust chain from allocation decision to running job. Standing positioning statement; all sprint plans align to this. | one-way (positioning, not code) |
| D25 | Customer-pull gate (governance features) | NO blanket customer-pull gate on governance features (founder directive: no revenue/headcount gating, sprints don't stop). Hybrid prioritization: build structural primitives (roles, surfaces, conceptual model) speculatively in v1.2-v1.4; defer customer-specific metric definitions, schema-heavy additions, and integrations gated on external systems (XDMoD, FreeIPA) until validated demand. Richard owns the speculative-vs-pulled call per sprint. **SUPERSEDED 2026-04-27 by D27** — the rule was correct but the bucketing label "customer-pull" was overbroad and let cheap structural items get parked in Sprint Z. D27 narrows it. | cheap |
| D26 | Attribute visibility defaults (Sprint E) | Default to least-sensitive reasonable level per attribute class. Financial → `pi`. Operational → `member`. Public research identity → `public`. Security credentials → `admin_only`. PI/Admin override available. | cheap |
| D27 | Sprint Z re-sequencing (supersedes D25) | Dissolve undifferentiated "Sprint Z." Re-bucket into 4 buckets: (1) BUILD NOW = cheap structural primitives, scheduled into Sprints F/G/H at v1.5.0 → v1.7.0; (2) TECH-TRIG = unscheduled, gated on concrete technical signal (LOC, scale event, contention threshold) with explicit monitor + decision-maker; (3) CUST-SPEC = unscheduled, genuinely needs customer to define contract (custom metrics, third-party integrations, IdP shape); (4) SKIP = explicit non-goal (cloud allocation). Customer-pull gating now applies ONLY to Bucket 3. | cheap |
| D28 | Versioning policy (v1.x vs v2.x boundary) | Minor bump (v1.x.0) for additive changes: new endpoints, new optional fields, new RBAC roles, new pages, plugin additions. Major bump (v2.0.0) reserved for: (a) schema migration that requires data conversion (PostgreSQL, multi-tenant tenant_id, attribute type system); (b) auth contract change (OIDC/SAML replacing or modifying API key/session semantics); (c) breaking API contract (removed endpoints, renamed fields in stable surfaces); (d) D10 violation (build step required). Patch bump (v1.x.y) reserved for hotfix. | one-way (versioning is contract with operators) |
| D29 | Sprint I selection (post-BUILD-NOW exhaustion) | Sprint I = "Show HN Hardening" at v1.8.0, hybrid of Option A (launch artifacts) and Option D (engineering polish), unified by Persona 0 (the HN reader stranger). 9 deliverables (I1–I9), Dinesh + Jared + Monica + Erlich owners, conditional Gilfoyle spike defaulted off. Target ship 2026-06-08, ~7 weeks of buffer to D16's 2026-07-27 launch. Rejects Options B/C (TECH-TRIG/CUST-SPEC speculation) as direct violations of D27 Bucket 2/3 gates. Sprint dispatches under D27 standing meta-rule (default to BUILD-NOW when on the fence). | cheap |
| D30 | UI refactor affordance audit | When a sprint task moves or removes a UI surface (button, card, page section), verify the action remains discoverable from every entry point that previously reached it. Lesson from Sprint C (C3-23): the initramfs rebuild card was moved from Images → Settings/System but the stale-warning on Dashboard/Images still pointed to Images — leaving no actionable path. Any "move" that drops discoverability is equivalent to a deletion and must be caught in the same PR. | cheap |

---

## D1 — RBAC Model + Auth Mechanism (full spec)

**Question:** Confirm 3-tier + group-scoped operator. Lock auth mechanism, token storage, login flow, bootstrap, and migration.

**Decision:**

**Roles (locked, matches Arch §4.1):**
- `admin` — full access, all groups, all resources, all module config
- `operator` — read-everything, mutate within assigned NodeGroup(s), cannot manage users/modules/NodeGroups
- `readonly` — GET-only across everything they can see (no row-level scoping in v1.0)

**Auth mechanisms (multiple, parallel):**
1. **Session cookies for humans (UI)** — already in place. HMAC-SHA256, 12h TTL, 30min sliding window. Keep `CLUSTR_SESSION_SECRET` requirement (warn if unset; do NOT auto-generate per restart in production — fail closed if missing in non-dev mode).
2. **Admin-scoped API keys** — already in place. SHA-256 hash storage. Per-key label, `last_used_at`, optional 30-day TTL. Rotation = revoke + create new.
3. **Node-scoped API keys** — already in place. Auto-minted at PXE-serve time, scoped to one node ID. Do not change.
4. **No OIDC, no LDAP-bind for operator auth in v1.0.** LDAP is for cluster HPC accounts only (Gilfoyle was right on this — circular dependency risk if RBAC depends on LDAP). OIDC/SAML deferred to v1.1+ when a customer asks.

**Token storage:**
- Sessions: HMAC, server-side stateless (no session table needed). Already implemented.
- API keys: bcrypt → already SHA-256, leave it; bcrypt is overkill since tokens are high-entropy random.
- Rotation: documented operator action (revoke + recreate). No forced rotation in v1.0.

**Webui auth flow:**
- Login page (`/login`) — already exists as separate HTML, keep it. Username + password. POST sets session cookie. 401 anywhere → redirect to `/login?next=<route>` (Sprint 3 task S3-6).
- No SSO redirect, no OAuth callback, no MFA in v1.0.

**First-run bootstrap:**
- `clustr-serverd apikey bootstrap` CLI command. Run once at install. Creates the initial admin user + admin API key, prints both to stdout, refuses to run if any user already exists. Operator copies the key to their kubeconfig-equivalent or uses the printed username/password to log into the UI.
- Document this in the Sprint 6 install guide (D12).
- Do NOT ship a default `admin/admin` account. No.

**Migration path for existing single-tenant deployments (the cloner dev host):**
- The current single-admin user/key on cloner becomes the admin. No data migration.
- Sprint 3 migration 044 (`user_group_memberships`) is additive. Existing admin keeps full access. Operators created post-migration default to no group memberships → effectively read-only until an admin grants them a group.
- The webui must NOT break for an admin-only deployment. RBAC UI only shows the User Management section to admins; non-admins see nothing changed.

**Rationale:** Pure stateless sessions + API keys is the minimum viable shape for a self-hosted product. OIDC adds operational and dependency surface that does not match Persona D (homelabber). Group-scoped operator is the only model that matches Persona A's real-world team structure.

**Reversibility:** **costly**. Adding OIDC later is additive (new auth path). Switching role model after v1.0 ships requires a permission-grandfather migration — not impossible but politically painful with multiple operators in production.

**Re-decision triggers:**
- First design partner explicitly requires SSO (most common: Okta/AzureAD)
- A customer in a regulated environment requires MFA (HIPAA, SOC 2)
- Group-scoped operator turns out to be wrong abstraction in practice (e.g., operators want resource-level instead of group-level scoping)

---

## D2 — Log Retention Model

**Question:** Richard's TTL + per-node row cap vs Gilfoyle's two-tier hot/cold archive.

**Decision:** **Richard's model.** TTL + per-node row cap. No cold archive in v1.0.

**Defaults:**
- `CLUSTR_LOG_RETENTION=7d` (down from 14d)
- `CLUSTR_LOG_MAX_ROWS_PER_NODE=50000` (new)
- Both env-vars only. No settings UI in v1.0.
- Purger runs hourly (existing): TTL pass first, then per-node cap eviction pass.
- New `node_logs_summary` table records eviction events for telemetry.

**Why not the two-tier model (Gilfoyle's option):** Adds an archive directory to manage (rotation, format stability, cleanup, restore tooling). The customer who needs >7d high-fidelity logs is the customer who already has an external log infra and will scrape `/api/v1/logs/stream`. Self-hosted-single-binary positioning is load-bearing on our pitch.

**Ops surface:** env vars only (D7 packaging is Docker Compose / Ansible — both let the operator set env). Settings UI for retention is v1.1 polish.

**Rationale:** TTL+cap bounds disk usage by node count (predictable for capacity planning). Two-tier is a future feature gated on real customer demand, not a v1.0 must-have.

**Reversibility:** **cheap**. Both layers (TTL purger and cap eviction) are independent code paths. Adding two-tier in v1.1 = new env vars + new archive writer + new retrieval endpoint, no schema break.

**Re-decision triggers:**
- A customer reports "I needed log line X from 30 days ago and it was evicted by row cap"
- SQLite DB size becomes a startup-time issue (>5GB)

---

## D3 — `groups[]` → `tags[]` Rename — Migration Sequence

**Question:** Migration plan, dual-emit timing, breaking change schedule.

**Decision:** Dual-emit through v1.0. Removal pushed to v1.1 (NOT v1.0 as Jared had it).

**Sequence:**
- **Sprint 2 (S2-4):** Migration 041 renames the **DB column** from `groups` to `tags`. The `pkg/api.NodeConfig` struct gains a `Tags` field. JSON serialization emits BOTH `tags` and `groups` (alias) on response. Input accepts EITHER. `NodeConfig.Groups` field marked `// Deprecated: use Tags`. Slurm module switches to read from `Tags` internally.
- **Sprint 2 (S2-5):** Migration 042 adds `is_primary` bool to `node_group_memberships` with partial unique index. Backfill from existing `group_id` values. `EffectiveLayout()` switches to read primary membership.
- **Sprint 6 (S6-6, S6-7):** Migration 047 drops `node_configs.group_id` column. `NodeConfig.Groups` JSON field removed. **This is a v1.0 breaking change** — acceptable because v1.0 is when we earn the right to define stable contracts.

**Wait — re-reading Jared's plan:** S6-6 and S6-7 ARE in Sprint 6 (v1.0 release). Jared put removal at v1.0. I'm overruling: **keep dual-emit through v1.0, remove in v1.1.**

**Why:** The dual-emit cost (one extra JSON field) is trivial. The cost of removing it at v1.0 is that any pre-v1.0 external integrator (the early-adopter design partners we're trying to court) gets a breaking change as their first taste of "stable" v1.0. That's bad first-customer experience.

**Updated plan:**
- Sprint 2: dual-emit lands.
- Sprint 6 (v1.0): `group_id` column drop happens (S6-6 stays — internal data model consolidation, no API impact). The `Groups` JSON field stays in responses with a deprecation header (`Sunset: <v1.1 date>`).
- v1.1: `Groups` field removed from JSON. API changelog documents the rename clearly.

**Sprint plan changes required:**
- S6-7 ("`pkg/api.NodeConfig.Groups` deprecation complete") **defers to v1.1**. Replace with: "Add `Sunset` HTTP header to `Groups` field; document in `CHANGELOG.md`."

**Rationale:** Dual-emit is cheap insurance for the small population of integrators who will exist between Sprint 2 and v1.0. Removing too aggressively destroys the goodwill of the first 5 design partners who are also our most likely word-of-mouth amplifiers.

**Reversibility:** **costly**. Removing a JSON field is a wire-contract break. Re-adding it later is awkward.

**Re-decision triggers:**
- Zero design partners by Sprint 5 (then aggressive removal is fine — nobody using it)
- A design partner explicitly asks for the rename to land sooner (then accelerate)

---

## D4 — LDAP Credential Encryption (sprint placement + scheme)

**Question:** Sprint 1 vs Sprint 2 placement. Encryption scheme. Migration path.

**Decision:** **Move to Sprint 1.** AES-256-GCM, sealed by existing `CLUSTR_SECRET_KEY`. Plaintext values re-encrypted on first server start after upgrade.

**Why Sprint 1 (not Sprint 2):**
- Listed as **SEC-P0-1** in ops-review (LDAP credentials in plaintext). P0 means "do before any external operator installs." Sprint 1 theme is literally "Close the remaining pre-external-user P0/P1 gaps."
- Jared put it Sprint 2 (S2-12). I'm overruling. P0-labeled security work belongs in Sprint 1.

**Encryption scheme:**
- Reuse `internal/slurm/deps.go` AES-256-GCM helper. Already implemented, already tested for the Slurm munge key.
- Key derivation: `CLUSTR_SECRET_KEY` (already required by D1's stance — fail closed if unset in non-dev mode).
- Affected columns: `ldap_module_config.service_bind_password`, `ldap_module_config.admin_passwd`, `node_configs.bmc_config` (this last one wasn't in S2-12 — adding it; the Proxmox API password lives here in cleartext JSON).

**Migration path (must NOT break the lab today):**
- Migration 040+ adds `*_encrypted` boolean column to each affected table.
- On first start after upgrade: server iterates rows where `*_encrypted=false`, encrypts in-place, sets the flag. Idempotent.
- If `CLUSTR_SECRET_KEY` is unset at upgrade: server logs FATAL and refuses to start (operator must set the key first; documented in upgrade guide).
- Rollback: documented manual SQL to decrypt — only useful within 24h of bad upgrade. Operator backup → restore → fix env → re-upgrade is the recommended path.

**Sprint plan changes required:**
- Move S2-12 to Sprint 1 as S1-15. Update Sprint 2 backlog to remove it.
- Add S1-16: also encrypt `node_configs.bmc_config` plaintext credentials.
- Add to Sprint 0 (already in flight): require `CLUSTR_SECRET_KEY` in `secrets.env` template and document in install path. (Already covered by Sprint 0 SEC-P1-3 / P0-OPS-3 — confirm scope.)

**Rationale:** Sprint 1 is where "no external operator without security incident" lives. Plaintext LDAP/BMC credentials in a chmod-644 SQLite file is the headline risk; defer to Sprint 2 leaves a 4-week window where the dev cluster is materially unsafe to demo.

**Reversibility:** **cheap**. The encryption is at-rest only; the in-memory shape is identical. Backing out = decrypt all rows, drop the `*_encrypted` columns. One-night job.

**Re-decision triggers:** none likely. This is the right scheme, and it's already proven in the Slurm path.

---

## D5 — Image Factory Consolidation — Exact Target Shape

**Question:** Lock the `Finalize(...)` signature. List entry points. State machine. Backwards compat.

**Decision:**

**Signature (locked):**
```go
// Finalize takes a fully-extracted rootfs at rootfsPath and runs the
// shared post-extraction sequence: arch detect, deterministic tar bake,
// SHA-256 hash, blob path persist, metadata sidecar write, status flip
// to ready, progress completion event. Returns the final BaseImage.
//
// rootfsPath: filesystem directory containing extracted root content.
//             Caller owns lifecycle; Finalize does NOT delete it.
// sourceMetadata: structured info captured at extraction time
//                 (source URL, ISO label, capture-from hostname,
//                 build distro, etc.) — written to the metadata sidecar
//                 verbatim so all paths produce comparable metadata.
//
// Errors: returns an error if any post-extraction step fails. Caller
// is responsible for setting the image to 'interrupted' status on err
// (so resumeFinalize can pick it up).
func (f *Factory) Finalize(
    ctx context.Context,
    imageID string,
    rootfsPath string,
    sourceMetadata SourceMetadata,
) (*api.BaseImage, error)
```

**Five entry points → thin wrappers:**

| Entry point | Current line | New shape |
|---|---|---|
| `pullAsync(imageID, url)` | factory.go:226 | extract via `pullAndExtract` → call `Finalize(ctx, imageID, rootfsPath, SourceMetadata{Source: "pull", URL: url})` |
| `importISOAsync(imageID, isoPath)` | factory.go:434 | extract via `extractISO` → `Finalize(...)` with `SourceMetadata{Source: "import_iso", ISOPath: isoPath}` |
| `captureAsync(imageID, req, sshUser, sshPort)` | factory.go:690 | rsync from host → `Finalize(...)` with `SourceMetadata{Source: "capture", FromHost: req.Host}` |
| `buildISOAsync(imageID, req, distro)` | factory.go:1532 | QEMU build → `extractLiveOS` → `Finalize(...)` with `SourceMetadata{Source: "build_iso", Distro: distro}` |
| `resumeFinalize(imageID, img, phase)` | factory.go:2004 | reload state from DB → call `Finalize(...)` with `SourceMetadata` reconstructed from `img.Metadata` |

**State machine (DB-enforced):**
- New CHECK constraint via Sprint 2 task S2-10: `CHECK (status IN ('building', 'interrupted', 'ready', 'archived'))`.
- Domain-level guard in `db.UpdateBaseImageStatus`:
  - `building → ready` (via Finalize success)
  - `building → interrupted` (process restart / error)
  - `interrupted → building` (via resumeFinalize)
  - `ready → archived` (via operator action)
  - All other transitions: rejected with explicit error.

**Backwards compat for in-flight builds during migration:**
- The migration is code-only (no schema change beyond S2-10 CHECK). Existing in-flight builds at upgrade time:
  - If `building`: process restart already marks them `interrupted` via existing reconciliation path.
  - On startup, `resumeFinalize` picks them up as before — but now uses the consolidated `Finalize`.
- Risk: an in-flight build during the upgrade might have a partial rootfs. The existing rsync + tar bake idempotency handles this (re-extracts cleanly). Tested via the Sprint 1 test infrastructure (S1-5).

**Target file size:** factory.go drops from 2,265 LOC to <1,400 LOC (Jared's target — accept).

**Rationale:** Single Finalize is the highest-leverage refactor on the table (Richard's review §3.1). Making the entry point shape `Finalize(ctx, imageID, rootfsPath, sourceMetadata)` keeps the interface narrow enough to test in isolation but wide enough to absorb future cross-cutting changes (initramfs auto-rebuild signal, webhook emission, deploy-target-affinity tagging).

**Reversibility:** **cheap**. The refactor is purely internal. Public API and DB shape are unchanged (apart from the additive CHECK constraint). Reverting = git revert, no migration.

**Re-decision triggers:**
- A new image source type (e.g., OCI registry pull) needs metadata that doesn't fit `SourceMetadata` struct → expand the struct, no contract change
- Finalize grows beyond what's reasonable in one function → extract sub-helpers, keep the entry point

---

## D6 — HA / Scaling Story for Sprint 4 and Beyond

**Question:** Single-server through 90-day window? Postgres trigger conditions? WebSocket/SSE scale target?

**Decision:**

**Single-server through v1.0 and beyond.** No HA work in the 90-day window. No HA work in v1.1 either, unless triggered.

**SQLite → Postgres triggers (any one):**
1. Sustained SQLite write contention: median write latency >100ms over a 1h window in production (need Sprint 4 Prometheus metrics S4-1 to even measure this)
2. A signed customer with >50 concurrent active deploys (concrete LOI, not "interest")
3. DB file size >50GB at steady state (purger maxed out, log growth not the cause)

Until then, SQLite WAL + `MaxOpenConns(1)` is correct for stage. Code discipline (D5's stateless orchestrator, DB-as-truth) preserves the option.

**WebSocket/SSE scaling target:**
- Target: 100 concurrent SSE/WS subscribers per server before optimization.
- Current model: per-subscriber goroutine, in-memory pub/sub. At 100 subscribers × ~10KB stack ≈ 1MB. Trivial.
- Failure mode at scale: at 1000+ subscribers, log fan-out becomes the bottleneck, not the WS plumbing. We'd address by (a) batching log writes per second per subscriber rather than per-line, or (b) introducing a leader/follower split. Not v1.0 scope.
- Sprint 4 Prometheus exposes `clustr_active_sse_subscribers` gauge to monitor.

**HA architecture for "later":** Documented in Arch §3.4 — orchestrator is stateless, DB is truth. When we go HA we add: (a) leader-election for the group reimage runner (etcd or Postgres advisory locks), (b) shared pub/sub for SSE/WS hubs (Redis or Postgres LISTEN/NOTIFY), (c) Postgres for the DB layer. None of this is v1.0.

**Rationale:** Stage discipline. Single-server is correct for our customer profile (sub-200-node clusters, single ops team). HA work has zero customer-facing value until we lose a server. Write the architecture doc, monitor the metrics, defer the build.

**Reversibility:** **costly**. Once HA is built, we own the operational complexity (leader election, network partitions, split-brain). Going BACK to single-server from HA is technically easy but the operator habits are baked in.

**Re-decision triggers:** see triggers above. Also: first incident where `clustr-serverd` goes down for >30 min causes a customer-visible outage they complain about → that's the "you should have built HA" moment.

---

## D7 — Packaging Strategy for v1.0

**Question:** Native binary vs Docker vs Helm vs Operator vs apt/rpm. Pick 1-2.

**Decision:**

**Two packaging modes for v1.0:**
1. **Docker Compose (PRIMARY)** — `deploy/docker-compose/docker-compose.yml` + `.env.example`. README quickstart points here first. Sprint 6 task S6-2.
2. **Ansible role (SECONDARY)** — `deploy/ansible/clustr-server/`. For operators who can't run containers (most HPC environments — they need root on the metal for DHCP/TFTP). Sprint 6 task S6-3.

**Skip for v1.0:**
- **Helm chart:** Our customers run HPC bare-metal, not Kubernetes. Building Helm = optimizing for a population we don't have.
- **Operator:** Same reason, plus operators are a deeper k8s-native commitment.
- **apt/rpm:** Distro-specific packaging is high effort, low payoff. Operators who want this can write their own spec from the binary release. Defer to v1.1+ if a customer asks.
- **Snap/Flatpak:** No.

**Native binary release stays:** GitHub Releases continues to ship cross-compiled `clustr` (CLI) and `clustr-serverd` (server) binaries via existing `release.yml`. The Docker Compose and Ansible package both consume the binary — they're packaging, not replacement.

**Note on Docker for HPC:** Docker Compose works for the homelabber (Persona D) and small lab (Persona B). Persona A (200-node HPC) will use Ansible — they need DHCP/TFTP on the host network namespace, root for IPMI, and existing config-management discipline.

**Rationale:** Two packaging paths cover the four personas. Helm/operator/apt/rpm are vanity packaging — each adds a CI matrix, a release artifact, and a documentation surface for marginal customer reach.

**Reversibility:** **cheap**. Adding Helm or apt later is purely additive. Removing Docker Compose later would be painful — but we won't.

**Re-decision triggers:**
- A customer signs that runs k8s-native infrastructure (low probability for HPC)
- A distro maintainer offers to package us (then: yes, accept the help, give them what they need)

---

## D8 — External Security Review

**Question:** Yes / no / conditional for v1.0. Scope. Budget.

**Decision:** **Conditional yes.** Tied to design partner signing.

**Mandatory baseline (regardless of customer status):**
- `gosec` in CI from Sprint 3 forward. Fail build on HIGH findings.
- `trivy` container scan in CI from Sprint 3 forward. Fail build on HIGH CVEs in base image.
- `govulncheck` in CI from Sprint 3 forward. Fail on actively exploited Go stdlib CVEs.
- These are zero-marginal-cost. Just wire them.

**Human pen-test (conditional):**
- IF a design partner has signed an LOI by end of Sprint 5 → engage a human pen-tester for $5K, scoped to:
  1. PXE chain (iPXE binary provenance, initramfs boot, kernel cmdline injection, node-token security)
  2. RBAC (privilege escalation, group-scope bypass, session hijacking)
  - 2-week engagement, findings incorporated before v1.0 tag.
- IF NO design partner by end of Sprint 5 → defer human review to v1.1. Document the rationale: "open-source = community review; internal CI tooling baseline applied; no customer attack surface in production yet."

**Why not unconditional yes:** We're a pre-revenue OSS project. $5K against zero customers is bad capital allocation. Once a customer is on the line, the security review IS the cost-of-doing-business and gets approved.

**Why not unconditional no:** PXE chain is high-stakes (compromising it owns the cluster). If we ship v1.0 to a real cluster without a human ever auditing the boot chain, we're operating on hope.

**Vendor selection:** Defer until trigger fires. Likely candidates: Trail of Bits (overkill), Doyensec (right-sized), an independent Go security consultant via Hacker One Bounty Bridge ($2-5K cheaper).

**Rationale:** Stage-appropriate. Automated tooling first (free, immediate). Human review when it matters (when we have a customer who cares).

**Reversibility:** **cheap**. We can always engage a reviewer post-v1.0.

**Re-decision triggers:**
- A serious vulnerability is discovered in the wild against a similar tool (xCAT, Cobbler, MAAS)
- A design partner specifically asks for a SOC 2 or third-party security report

---

## D9 — GPU Detection (Sprint 5)

**Question:** Keep or cut. If keep, scope.

**Decision:** **Keep.** Minimum viable: NVIDIA-only via PCIe vendor ID detection.

**Scope (locked):**
- Hardware survey in initramfs runs `lspci -mm -nn` and filters for vendor ID `0x10de` (NVIDIA). Records:
  - PCI BDF (bus:device.function)
  - PCI device ID (model identifier)
  - Lookup against an embedded NVIDIA device-ID → marketing-name table (small JSON file, ~200 entries covering Tesla/Quadro/RTX/Ampere/Hopper)
- Stored in `hardware_profile.gpus[]` as `[{bdf, device_id, model_name}]`.
- Node detail Hardware tab displays GPU count + model names.
- Image detail surfaces CUDA version from existing metadata sidecar (D5's Finalize already populates this for ISO builds that ran the CUDA install steps).
- Role-mismatch warning gets one upgrade: assigning a CPU-only image to a node with GPUs surfaces a confirmation modal (not a hard block).

**Explicit non-scope for v1.0:**
- AMD ROCm GPU detection (vendor ID `0x1002`) — defer to v1.1
- Intel GPU detection — defer to v1.1
- VRAM size detection (requires `nvidia-smi` in initramfs, which requires NVIDIA driver in initramfs — too much complexity)
- Driver version matching (image CUDA version vs node driver version) — defer to v1.1
- GPU partitioning / MIG awareness — never (that's a runtime concern, not provisioning)

**Why not cut:** Persona B (AI/ML lab) is a high-conviction segment for clustr. GPU visibility is the most asked-for missing feature in their workflow per webui-review. Cutting it leaves Persona B with no story. The minimum scope is small enough (PCIe detect + lookup table) to fit Sprint 5's L-effort budget.

**Rationale:** NVIDIA-only is 90% of the AI/ML market. Skip the long tail in v1.0. Driver-matching is the temptation we resist — "is your driver version compatible with this image's CUDA?" is a complete project on its own; ship the visibility, defer the enforcement.

**Reversibility:** **cheap**. Adding AMD/Intel detection in v1.1 is just expanding the lspci filter. Driver matching is purely additive.

**Re-decision triggers:**
- A Persona B design partner is on AMD-only infra (rare but possible — newer AI labs flirt with ROCm) → expand to AMD
- Persona B reports "GPU shows up but I can't tell what driver matches" → driver matching becomes v1.1 priority

---

## D10 — Webui Framework Decision

**Question:** Keep vanilla JS, or migrate (React/Vue/Svelte/HTMX)? When?

**Decision:** **Stay vanilla JS through v1.0.** Module-split deferred to v1.1 evaluation.

**What we DO ship in v1.0:**
- Per Sprint 2 backlog: kill `<details>` collapsibles, replace `confirm()`/`alert()` with modal pattern, fix node modal `group_id` field. These are quality fixes, not framework migration.
- Per Sprint 3: 401 → /login redirect, search input, modal-based forms.
- Per Sprint 5: GPU UI, persona polish.

**What we do NOT do:**
- No React/Vue/Svelte migration in 90 days.
- No HTMX adoption.
- No build step (`webpack`, `vite`, `esbuild`, `rollup`).
- No TypeScript.
- No `app.js` module split (deferring R-2 from Richard's review — Jared correctly listed this in non-goals).

**Rationale:** A framework migration is a 4-6 week project that touches every page. The benefit (smaller files, type safety, component reuse) does not show up in any persona's day-1 workflow. The cost (build complexity, deploy artifact complexity, contradicts "one binary one container" pitch) shows up immediately. The 8K-LOC `app.js` is debt, but it's KNOWN debt — every velocity drag is in the existing pages, not in adding new ones.

**Post-v1.0 plan (NOT a v1.0 commitment, just direction):**
- Module-split `app.js` into per-page modules (`Pages/nodes.js`, `Pages/images.js`, etc.) using ES6 modules + `<script type="module">`. No build step. This buys 80% of the framework benefit at 10% of the cost.
- Re-evaluate framework adoption only if: (a) a new contributor wants to add a major feature and the existing JS organization blocks them, or (b) we hire a frontend engineer who has strong opinions and a track record.

**Sprint plan changes required:** None. Webui-related sprint tasks already align with this — they're targeted quality fixes, not migration.

**Rationale:** "One binary, one container, no build step" is a load-bearing positioning pillar. We give that up only when forced.

**Reversibility:** **costly**. Once we adopt a framework, we don't rip it out. Stay in vanilla JS until we have a strong reason to leave.

**Re-decision triggers:**
- Frontend engineer hire with framework expertise (D15 hiring trigger may unlock this)
- A specific feature (rich data viz, complex form state machines) that vanilla JS makes painful
- A contributor PR that adds a framework — we'd reject it but the request is a signal

---

## D11 — Test Infrastructure (Sprint 1, S1-5)

**Question:** Mock LDAP server approach. Network profile fixtures. Slurm config goldens. Test database.

**Decision:**

**Mock LDAP server:** **Real OpenLDAP container via testcontainers-go.**
- Add `github.com/testcontainers/testcontainers-go` dependency.
- Test fixture spins up `osixia/openldap:1.5.0` (well-maintained image), bootstraps with a known DN tree, exposes on a random port, tears down on test cleanup.
- Tests cover: DN lookup, bind success/failure, group enumeration, password change flow.
- CI runs Docker (already set up — `docker.yml` workflow uses Docker daemon).

**Why real OpenLDAP, not a Go fake:**
- LDAP protocol quirks (referrals, paged results, attribute case-sensitivity) are exactly what bites you in production. A Go fake won't reproduce these.
- testcontainers handles lifecycle; tests stay hermetic.
- Cost: ~10s test startup. Acceptable.

**Network profile fixtures:** **Golden file approach.**
- Layout: `internal/network/testdata/profiles/<scenario>/input.json` + `expected.nmconnection` pairs.
- Initial scenarios (5 minimum): static IPv4, static IPv4 + bond, DHCP IPv4, static IPv4 + VLAN, IPv6 SLAAC.
- Test runs the renderer, diffs against expected. `-update` flag regenerates goldens (gated on `UPDATE_GOLDEN=1` env to prevent CI accidentally rewriting).

**Slurm config goldens:** **Same pattern.**
- `internal/slurm/testdata/configs/<scenario>/input.json` + `expected.conf`.
- Initial scenarios: single partition, multi-partition, GPU partition with GRES, federation member.

**Test database:** **In-memory SQLite per test.**
- Use `:memory:` DSN with `?cache=shared` for tests that need multiple connections.
- Run all migrations on test setup. Each test gets a fresh DB.
- Why not shared fixture: parallel test execution requires isolation. Per-test cost is ~50ms migration apply. Acceptable.

**Why not testcontainers Postgres/MySQL for DB tests:**
- We're SQLite-first (D6). Testing against Postgres in CI would test something we don't ship.
- When we eventually move to Postgres (D6 trigger), we add a Postgres test suite at that point.

**Sprint plan changes required:** None. S1-5 acceptance criteria already line up with this. Add explicit dependency note in S1-5: "Adds `testcontainers-go` to go.mod."

**Rationale:** Real LDAP via testcontainers gives us protocol fidelity for the most-likely-to-bite module. Golden files give us cheap regression tests for the deterministic renderers. In-memory SQLite gives us parallel test execution without contention.

**Reversibility:** **cheap**. Each test infra choice is independent of every other.

**Re-decision triggers:**
- testcontainers-go startup cost grows (e.g., CI moves to a runner where Docker is slow) → switch to a real LDAP fake
- Golden file maintenance becomes a chore (lots of churn) → move to assertion-based tests for the volatile cases

---

## D12 — Documentation Strategy

**Question:** Where docs live. When install guide exists. Tutorial timing.

**Decision:**

**Where:** **In-repo `docs/` (Markdown).** No Docusaurus, no mkdocs site for v1.0.

**Structure (locked):**
```
docs/
  README.md              -> "Start here" map (already exists if we count existing docs)
  install.md             -> Sprint 4 deliverable (was Sprint 6 — moving up)
  upgrade.md             -> Sprint 6 deliverable
  configuration.md       -> Env var reference; updated each sprint as new vars land
  rbac.md                -> Sprint 3 deliverable (alongside RBAC implementation)
  tls-provisioning.md    -> Sprint 6 deliverable (already in S6-5)
  troubleshooting.md     -> Sprint 5 deliverable
  api-reference.md       -> Continuous; auto-generated from chi route registration in v1.1
```

**When the install guide exists:** **Sprint 4** (moving up from Sprint 6).

**Why move install guide to Sprint 4:** Sprint 4's exit criterion is "ready for first external operator" (Jared's plan). An external operator without an install guide can't install. Sprint 6 is too late for the doc that gates external trial.

**Tutorial / first-time setup walkthrough:** **Sprint 5.**
- Per Sprint 5 task S5-9 (first-deploy wizard) — that's the in-app version. The Markdown version goes in `docs/getting-started.md` alongside.

**Why not a docs site (Docusaurus, mkdocs):**
- Adds a build step and a deploy target. Contradicts "self-contained repo" posture.
- GitHub renders Markdown well enough. Direct links to `docs/install.md` work.
- v1.1 candidate: if community contribution velocity warrants it, set up a docs site then.

**Sprint plan changes required:**
- Move S6-4 (install/upgrade guide) split: install.md → Sprint 4 as new task S4-14. Upgrade.md stays in Sprint 6 as part of S6-4.
- Add docs/rbac.md to Sprint 3 backlog as new task S3-11.

**Rationale:** Markdown in-repo is the lowest-friction choice. External operators read GitHub. Documentation effort goes into content, not infrastructure.

**Reversibility:** **cheap**. Moving to a docs site later = pointing Docusaurus at the existing `docs/` directory. Markdown survives the transition.

**Re-decision triggers:**
- Community contributors start writing docs PRs and the lack of versioning becomes painful
- A search-across-docs request from a customer
- Multiple supported versions (v1.0, v1.1, v2.0) need parallel docs

---

## D13 — Audit Log Scope (related to Open Q5)

**Question:** Audit log retention. SIEM export.

**Decision:** **Internal product retention only in v1.0. No SIEM export.**

**Scope:**
- `audit_log` table per Sprint 3 task S3-4. Schema as Jared specified.
- Retention: configurable via `CLUSTR_AUDIT_RETENTION` env var, default **90 days**.
- Purger: same hourly tick as log purger (S1-4), separate sweep.
- API: `GET /api/v1/audit?since=&until=&actor=&action=&resource_type=` paginated query (in S3-4 already).

**Explicitly NOT in v1.0:**
- SIEM export endpoint (e.g., `/api/v1/audit/export?format=cef`)
- Syslog forwarding
- Real-time stream (SSE/WS subscription to audit events)
- Cryptographic chaining (tamper-evident log)

**Rationale:** 90-day retention satisfies SOC 2 Type 1 minimum and most internal audit needs. SIEM export is a v1.1 feature gated on a regulated customer. Cryptographic chaining is a FedRAMP/STIG concern — defer to when a federal customer is in the pipeline.

**Reversibility:** **cheap**. Adding export or stream endpoints later is purely additive.

**Re-decision triggers:**
- HIPAA/SOC 2/FedRAMP customer in pipeline → SIEM export priority bumps
- Customer reports audit log gaps after an incident

---

## D14 — Initramfs Auto-Rebuild

**Question:** Should initramfs auto-rebuild when an image is finalized (per Persona A's "should be automated" comment in webui-review)?

**Decision:** **NOT automated in v1.0.** Manual button stays. Stale-warning surfaced loudly.

**What we do:**
- Sprint 2 task S2-11 (already in plan): stale-initramfs indicator on dashboard + WARN at PXE serve when initramfs kernel version predates the latest image.
- Manual "Rebuild Initramfs" button stays.

**Why not auto-rebuild:**
- Initramfs build is 30-60s and CPU-bound. Auto-triggering on every image finalize creates a stampede if multiple images finalize concurrently.
- The stale-warning surface is more important than auto-rebuild — it tells the operator "hey, something needs your attention" without surprising them by changing system state.
- Auto-rebuild is a v1.1 candidate once we have telemetry on how often it's actually needed.

**Reversibility:** **cheap**. Auto-rebuild is a single goroutine queue. Add it later when warranted.

**Re-decision triggers:**
- Operator reports "I forgot to rebuild and got a stale-kernel deploy failure" three times in a quarter

---

## D15 — Hiring Trigger (Open Q6)

**Question:** When does the founder hire the first engineer?

**Decision:** **First paying design partner with >50 nodes signs an LOI.**

**Why this trigger:**
- Pre-customer hire is funded by burn, post-customer hire is funded by revenue trajectory
- 50-node threshold filters out tire-kickers — a 50-node cluster is a real procurement decision
- LOI (not just verbal interest) demonstrates concrete commitment

**Until then:** Founder + AI agent team is the operating model. Sprint plan is sized for this.

**Re-decision triggers:**
- A strategic hire becomes available (someone we'd regret not catching)
- A specific skill gap blocks v1.0 ship (e.g., we discover we need a frontend specialist)

**Reversibility:** **cheap.** A hiring decision is reversible (don't hire) but a hire-then-let-go is expensive emotionally and legally.

---

## D16 — Show HN Timing (Open Q2)

**Question:** Show HN timing — coordinate with tunnl, or independent? Pre-launch beta?

**Decision:** **Independent from tunnl. Post-v1.0 ship + 1 week buffer. No pre-launch beta announcement.**

**Sequence:**
- Sprint 6 ends 2026-07-19 with v1.0 tag.
- 1-week buffer: shake out any first-72-hour bugs from real installs.
- Show HN target: **2026-07-27** (Sunday — best HN traffic).

**Why no pre-launch beta announcement:**
- We can have design partners (private outreach) before v1.0 — that's expected.
- A public beta announcement before v1.0 fragments attention and burns the "new launch" novelty.
- Show HN works once. Save the shot for v1.0.

**Why independent from tunnl:**
- Different audience (HPC sysadmins vs. developers), different value prop.
- Bundling them dilutes the message.

**Sprint plan changes required:** None. Sprint 5 founder decision point already includes "Show HN timing." Confirmed: 2026-07-27 target.

**Reversibility:** **cheap**. Announce when ready.

---

## D17 — Slurm Controller Dual-Role (Default Topology)

**Date:** 2026-04-27
**Author:** Richard
**Context:** Gilfoyle's REG-2 forensics (`docs/lab-validation-pr5.md` §"REGRESSION FORENSICS", commit `369afeb`) showed that after Dinesh's GAP-NEW-3 fix (`b614091`), slurmctld is correctly scoped to controllers only. But this means the canonical 2-VM lab (1 controller + 1 compute) cannot satisfy `srun -N2` because the controller no longer runs slurmd. Round 4's apparent pass was an artifact of pre-fix manual state.

**Question:** For v1.0, what is the default Slurm role assignment for a node tagged "controller"?

**Decision:** **Controller runs slurmd by default.** Default role assignment for a controller node is `["controller", "compute"]` (dual-role). Operator opts out (production hygiene) by removing `compute`/`worker` from the role list via the API or webui.

**Why Option B (default dual-role with opt-out) over A and C:**
- **Option A (controller-only)**: forces 3-VM minimum for a working `srun -N2` demo. Worst possible Show HN first-impression; the `examples/2-vm-lab` that ships with v1.0 stops being end-to-end demo-able.
- **Option C (auto-detect by cluster size)**: clever but unpredictable. Adding the second compute node would silently flip the controller from compute-on to compute-off, breaking jobs already scheduled on the controller. Surprise behavior changes are the worst class of bug for sysadmins.
- **Option B (default dual-role)**: explicit, predictable, opt-out-able. The 200-node operator who cares about controller hygiene already knows to drop `compute` from the controller role; the homelabber gets a working `srun -N2` out of the box. Same default works for both audiences with one knob to turn.

**What "default" means concretely:**
- The webui "Add Slurm Role" workflow for a controller node defaults the role multi-select to `["controller", "compute"]` (both checkboxes pre-checked).
- The API `PUT /api/v1/slurm/nodes/{id}/roles` continues to accept any role list — no server-side override of operator intent. The default lives in the UI and in the bootstrap path only.
- Existing role assignments are NOT auto-migrated. The current cloner dev cluster (controller has `["controller"]`, compute has `["worker"]`) gets fixed by the operator running the API call once. Documented in `docs/upgrade.md` for v1.0.

**Renderer fix (orthogonal but required for either option):**
- `internal/slurm/render.go:173` must accept `"worker"` as an alias for `RoleCompute`. Currently the renderer silently drops worker-tagged nodes from the NodeName block. This is a bug regardless of D17 — fix it.
- See spec block below for the exact change.

**Rationale:** Small-cluster ergonomics is the load-bearing first-impression for clustr. The 2-VM lab is the demo we ship in `examples/`, the demo we screenshot for Show HN, the demo Persona D (homelabber) tries first. Forcing a 3-VM minimum for `srun -N2` to work is an unforced own-goal. Production hygiene is a real concern but easily handled by the operator who cares.

**Reversibility:** **cheap**. The default lives in the webui form's pre-checked state and in the bootstrap example. Changing the default later is a single-line change in the webui plus a docs note in CHANGELOG. No data migration.

**Re-decision triggers:**
- A design partner reports controller resource contention from running slurmd (then: change the default for new clusters, leave existing clusters alone)
- Multiple design partners ask for a CLI flag to disable the default (then: add `--controller-only` to whatever bootstrap CLI we ship in v1.1)

**Implementation spec for Dinesh (D17):**
```
1. internal/slurm/render.go:173 — add "worker" alias to the role check:
     if !hasRole(entry.Roles, RoleController) &&
        !hasRole(entry.Roles, RoleCompute) &&
        !hasRole(entry.Roles, "worker") { continue }
   (Or add `RoleWorker = "worker"` constant in roles.go and use it.)
2. webui/static/app.js — Slurm node-role assignment modal: when the
   selected node is being assigned the "controller" role, pre-check the
   "worker" checkbox as well. Operator can uncheck. No server-side override.
3. examples/2-vm-lab/README.md (create if missing) — document that
   slurm-controller has roles ["controller", "worker"] for this lab topology.
4. docs/slurm-module.md — add a "Role assignment" section describing the
   dual-role default and the production opt-out.
5. docs/upgrade.md — add a one-liner: "If your existing cluster has a
   controller-only Slurm controller and you want srun -N2 to work in a
   1+1 topology, add the 'worker' role: PUT /api/v1/slurm/nodes/{controller_id}/roles
   with body {\"roles\": [\"controller\", \"worker\"]}".
6. internal/slurm/render_test.go — add a test case asserting that a node
   with roles ["worker"] appears in the NodeName block. (Currently it
   would be dropped.)
```

Estimated effort: ~1.5 hours (render.go + test ~30min, webui pre-check ~30min, three docs touch-ups ~30min).

---

## D18 — Template → DB Sync Mechanism

**Date:** 2026-04-27
**Author:** Richard
**Context:** Gilfoyle's REG-1 forensics surfaced a structural gap. `seedDefaultTemplates()` only runs once — after that, every commit to `internal/slurm/templates/*.tmpl` is dead weight. The template fix in `5995c75` (MpiDefault pmix→none) never reached the deployed cluster because the DB rows were authored before the fix and `seedDefaultTemplates()` is gated on "no row exists." This shape will recur for every future template fix.

Compounding issue: `SlurmSaveConfigVersion` hardcodes `is_template=0` on all writes (`internal/db/slurm.go:347`). `seedDefaultTemplates` also stores rendered output with `is_template=0` rather than storing the template source with `is_template=1`. Result: the renderer's template-rendering path is effectively dead for cluster-default config. This was a latent design bug; D18 doesn't fix the latent bug (that's a separate v1.1 cleanup), but it does fix the immediate problem.

**Question:** How do template fixes in code reach already-seeded clusters without auto-pushing to live nodes?

**Decision:** **Option D-lite** — `is_clustr_default` boolean column + admin re-seed endpoint. NOT auto-reseed-on-startup. Operator-controlled, surgical, reversible.

**Concrete shape:**
- Migration `052_slurm_config_files_clustr_default.sql`:
  ```sql
  ALTER TABLE slurm_config_files ADD COLUMN is_clustr_default INTEGER NOT NULL DEFAULT 0;
  ```
- Backfill rule for existing rows: leave `is_clustr_default=0` for ALL existing rows. Existing clusters opt into the new flow by hitting the re-seed endpoint explicitly. (Safer than trying to retro-detect "was this seeded by clustr or hand-authored.")
- `seedDefaultTemplates` flow: when seeding version 1, set `is_clustr_default=1`. Subsequent operator API writes (via `SlurmSaveConfigVersion`) set `is_clustr_default=0` — that row is now sacred.
- New endpoint `POST /api/v1/slurm/configs/reseed-defaults` (admin-only):
  - Iterates `cfg.ManagedFiles`. For each filename:
    - Read current version. If `is_clustr_default=1` (i.e., never operator-edited since seed), render the embedded template and call `SlurmSaveConfigVersion` to bump to a new version with `is_clustr_default=1`.
    - If `is_clustr_default=0` (operator-edited), skip with a per-file note in the response: `{"filename":"slurm.conf","action":"skipped","reason":"operator-customized"}`.
  - Returns a JSON summary: `{"reseeded":["cgroup.conf"],"skipped":["slurm.conf"],"missing_template":[...]}`.
  - Does NOT push to nodes. Operator triggers a separate `POST /api/v1/slurm/sync` to deploy the new versions. Two-step is intentional — no surprises.
- `SlurmSaveConfigVersion` signature gets a new bool param `isClustrDefault bool`; operator-API write path passes `false` (safe default), `seedDefaultTemplates` passes `true`.

**Why Option D-lite (manual + flagged) over A/C (auto-reseed) or pure B (manual without flag):**
- **A and C (auto-reseed)**: silently overwriting deployed config on operator clusters mid-upgrade is a class-of-bug we never want to ship. Per-template SHA versioning (C) is just A with more rigorous detection — same blast radius.
- **Pure B (admin endpoint, no flag)**: re-seeds ALL files including operator-customized ones. Wipes operator work. Wrong default.
- **D-lite (flag + endpoint)**: operator chooses the moment, only un-customized rows get touched, two-step deploy keeps the operator in the driver's seat. Reversible — every reseed creates a new version, operator can roll back to any prior version via existing version-history API.

**Migration story for the cloner dev cluster (and any existing v5 cluster):**
- After 052 lands, all existing rows have `is_clustr_default=0` (sacred).
- For the cloner dev cluster specifically, REG-1 fix is a one-shot: operator runs `POST /api/v1/slurm/configs/{filename}/reseed-from-template?force=true` (or hand-authors version 6 via the existing API, which is what Gilfoyle's "Alternate 1-line mitigation" suggested).
- Document in `docs/upgrade.md` v1.0 release notes: "REG-1 one-time fix: PUT a corrected slurm.conf via the API after upgrade. Future template-shipped fixes will be applied via the new /reseed-defaults endpoint."
- Greenfield clusters post-v1.0 get `is_clustr_default=1` from the start; future template commits propagate by operator running `/reseed-defaults` once per release.

**Why not fix the latent `is_template=0` bug now:** That requires changing `SlurmSaveConfigVersion` to accept `is_template`, changing all callers, changing the seed path to store template source rather than rendered output, and validating that the renderer correctly handles every existing template under all override permutations. ~1 day of work and a real regression risk on an already-shaky module. Defer to v1.1 as "templates: store source, render at deploy" cleanup. D18 ships the operator-facing fix without rewriting the rendering layer.

**Rationale:** Operator-controlled re-seed is the smallest blast-radius shape that solves the structural problem. Auto-anything is too dangerous given the current "DB row content is verbatim deployed" pattern. The `is_clustr_default` flag is the minimum DB schema change needed to safely distinguish "clustr seeded this" from "operator wrote this."

**Reversibility:** **cheap**. The column is additive. The endpoint can be removed in v1.1 if a better mechanism (e.g., the proper template-source-stored-in-DB rewrite) lands. No wire-contract impact.

**Re-decision triggers:**
- Operators report the two-step (reseed → sync) is too cumbersome and want a one-step "reseed and deploy" → add a `?deploy=true` query param to the reseed endpoint
- The latent `is_template=0` bug bites a customer — bumps the v1.1 "store template source" cleanup priority

**Implementation spec for Dinesh (D18):**
```
1. internal/db/migrations/052_slurm_config_files_clustr_default.sql:
     ALTER TABLE slurm_config_files
       ADD COLUMN is_clustr_default INTEGER NOT NULL DEFAULT 0;
2. internal/db/slurm.go:
   - Add field IsClustrDefault bool to SlurmConfigFileRow.
   - Update all SELECT queries (lines 285, 297, 307, 322) to include the column.
   - Update scanSlurmConfigFile (line 365) to scan it.
   - Change SlurmSaveConfigVersion signature: add `isClustrDefault bool` param;
     update the INSERT to set the column from the param (replacing hardcoded 0).
3. internal/slurm/manager.go seedDefaultTemplates (line 276):
     pass `true` for the new isClustrDefault arg.
4. internal/slurm/routes.go (and any other call site of SlurmSaveConfigVersion):
     pass `false` for operator API writes.
5. internal/slurm/routes.go: add new handler handleSlurmReseedDefaults:
   - Path: POST /api/v1/slurm/configs/reseed-defaults
   - Admin-only (use existing admin auth middleware)
   - Iterate cfg.ManagedFiles. For each: read current version via
     SlurmGetCurrentConfig. If row.IsClustrDefault is true, re-render
     the embedded template and call SlurmSaveConfigVersion(..., true).
     Else, append to "skipped" list with reason="operator-customized".
   - Return JSON summary: {"reseeded":[...],"skipped":[...],"missing":[...]}.
   - No node push — explicit operator follow-up via /slurm/sync.
6. internal/slurm/manager_test.go: add tests for reseed flow:
   - reseed when row.IsClustrDefault=true bumps version
   - reseed when row.IsClustrDefault=false leaves row untouched
7. docs/slurm-module.md: add "Re-seeding default templates" section
   documenting the endpoint, the flag semantics, and the two-step
   reseed→sync pattern.
8. docs/upgrade.md: add v1.0 release note about REG-1 one-time fix
   and the new endpoint for future template propagation.
```

Estimated effort: ~2 hours (migration + DB changes ~30min, route handler ~45min, tests ~30min, docs ~15min).

---

## v1.0 Ship Implications (D17 + D18)

Both calls are **non-blocking for v1.0 once specs above are executed**. They are additive (no breaking changes to existing API contracts, no destructive migrations). Both can land in a single Dinesh sprint window before tagging v1.0.

**Sequencing:**
1. D17 render.go alias fix lands first (single-file change, ~30min, unblocks Round 5 srun -N2 validation).
2. D18 migration + endpoint lands second (~2h, larger change but fully isolated).
3. Round 5 e2e validation (Task #82) runs against a fresh reimage with both fixes present. If srun -N2 PASSES on a 1+1 cluster after the operator assigns dual-role to the controller, both REGs are closed.
4. v1.0 tag.

**One latent debt acknowledged:** The `is_template=0` rendering shortcut is a known v1.1 cleanup. Documented here so future-Richard doesn't rediscover it. Tracking implicitly via the D18 "Re-decision triggers" above.

---

## Other Items Reviewed (No New Decisions Needed)

These came up in the reviews but are already correctly handled in the sprint plan or are non-issues:

- **`pkg/api` wire/domain split (Arch §3.2):** Confirmed deferred. Non-goal in 90-day window. Trigger: `/api/v2/` or row-level RBAC redaction.
- **`internal/db/db.go` package split (Arch §3.3):** Confirmed deferred. File-level split is fine; package split is post-v1.0.
- **Handler-Server cyclic-import workarounds (Arch §3.7):** Cosmetic. Defer.
- **Image build progress 4-layer indirection (Arch §3.8):** Defer.
- **`pricing / OSS positioning` (Open Q4):** Marketing/strategy concern, not technical. Belongs in Monica's lane. Not blocking sprint execution.
- **IPv6 provisioning (HA-1):** Confirmed non-goal for v1.0. Federal customer trigger.
- **PBS/Torque integration:** Confirmed non-goal. Slurm only in v1.0.

---

## Sprint Plan Amendments Required

Concrete edits to `docs/90-day-sprint-plan.md` as a result of these decisions:

1. **Sprint 1:** Add S1-15 (LDAP credential encryption) — moved from S2-12.
2. **Sprint 1:** Add S1-16 (BMC credential encryption in `node_configs.bmc_config`) — new scope from D4.
3. **Sprint 1 founder decision point:** Remove "log retention defaults" (now locked in D2). Replace with "Confirm Sprint 1 acceptance and unblock Sprint 2."
4. **Sprint 2:** Remove S2-12 (now S1-15).
5. **Sprint 3:** Add S3-11 (write `docs/rbac.md` alongside RBAC implementation).
6. **Sprint 4:** Add S4-14 (write `docs/install.md`) — moved from S6-4 partial scope.
7. **Sprint 6:** S6-4 reduced scope — only `docs/upgrade.md` remains; `docs/install.md` moved to Sprint 4.
8. **Sprint 6:** S6-7 reduced scope — `Groups` JSON field stays with `Sunset` header. Field removal moves to v1.1.
9. **Open Questions Q1-Q6:** All answered in this doc; remove from open list.
10. **Risk Register:** Add new row — "Real OpenLDAP via testcontainers in CI fails on slow CI runners" (low/medium, mitigation: budget 60s startup or fall back to a Go fake).

These edits land in the sprint plan in the next pass.

---

## D19 — Customizability Default (WebUI)

**Date:** 2026-04-27
**Author:** Richard
**Context:** Founder directive on v1.1+ webui sprint planning explicitly called out "items that are too open for customizability" alongside "lack of automation where manual processes are too customizable." Three reviews surfaced this from different angles: Jared C-2/C-3/C-4/C-5 (too-open custom kickstart, custom variables, network freeform fields, freeform disk layout), Monica's new-admin friendliness theme, Dinesh's note that the v1.0 form surfaces are dense.

**Question:** When in doubt about whether to expose a configuration knob in the webui, do we lean "lock the knob, ship the recommendation" or "expose the knob with a sensible default"?

**Decision:** **Lock the knob, ship the recommendation, expose under an "Advanced" disclosure.** The default surface for any config is the recommended value, not the editable field.

**Concrete v1.x application:**
- Slurm Module Setup form: 4 visible fields (controller node, cluster name, version, default partition). Everything else under "Advanced".
- Node Group disk layout: defaults to "Inherit from image". "Customize layout" is collapsed.
- Reimage modal: defaults to "Use image default kickstart". Custom kickstart under "Advanced".
- ISO build modal: defaults to recommended distro/version. Custom kernel/cmdline under "Advanced".

**Why this default (not the opposite):**
- New-admin friendliness is the v1.x guiding theme per founder directive.
- Every visible knob is a decision the new admin must make or research before clicking save.
- The 5-knob form scares new admins; the "click Deploy with sensible defaults" form gets them to first success.
- Power users who need the knob can find it under Advanced — one extra click is much cheaper than every new admin staring at 12 fields.

**Tradeoff acknowledged:** Power users will occasionally feel patronized. The cost (one extra click for them) is much lower than the cost of every new admin facing a wall of optional fields.

**Reversibility:** **cheap**. Promoting a field out of Advanced is a 1-line UI change. Demoting after exposure is also cheap.

**Re-decision triggers:**
- Power-user customer reports that "Advanced" is consistently the first thing they click — at that point, expose top-N most-clicked-Advanced fields by default.
- A specific persona (e.g., AI/ML lab) needs different defaults than the general HPC operator — then do persona-specific defaults, not "expose everything."

---

## D20 — CLI/UI Parity Policy

**Date:** 2026-04-27
**Author:** Richard
**Context:** Jared's ops review flagged multiple CLI-only operations that operators routinely need (B-1 reseed-defaults, B-6 bundle install, E-5 password recovery). Monica's persona review noted that the LDAP module exposes operations only via admin curl. Founder directive emphasized "lack of automation where manual processes are too customizable" — operationally this maps to "if the operator has to drop to SSH for a routine action, that's a positioning failure."

**Question:** Do all CLI ops get a webui equivalent in v1.1, or do some stay CLI-first?

**Decision:** **Routine operator actions (anything done more than once per cluster lifetime) MUST have a webui surface. Initial bootstrap and disaster-recovery operations MAY remain CLI-only with a documented path.**

**Categorization (locked):**

**Must have webui surface:**
- Reseed Slurm defaults — v1.1 (B2-3 in webui sprint plan)
- Bundle install / version display — v1.2 (C3-26)
- Change own LDAP password — v1.2 (C1-3)
- Audit log query — v1.1 (B3-3)
- Webhook subscriptions — v1.1 (B3-1)
- Any future "I do this every cluster" action

**CLI-only acceptable indefinitely (with docs):**
- `clustr-serverd apikey bootstrap` (Day-0 only)
- Force-decrypt rollback after D4 encryption error (post-disaster, once)
- DB compaction / vacuum (operational maintenance, not UI-routine)
- Autodeploy circuit breaker reset (post-failure, once)
- Direct SQL queries for support escalation (never UI-exposed)

**Why this policy:** Self-hosted product positioning requires that operators don't need to SSH for routine work. SSH for first-install or post-incident is acceptable; SSH for "I want to see my audit log" is a positioning failure that undermines the institutional pitch.

**Reversibility:** **costly**. Once we ship a webui surface for an action, removing it later is a regression. Adding webui surface for a CLI-only action later is cheap.

**Re-decision triggers:**
- A CLI operation we marked "Day-0 only" turns out to be a regular operation (e.g., bootstrap is run weekly because operators are doing fresh installs constantly) — promote to webui.
- A webui operation we shipped turns out to be ineffective in the UI form (too dangerous, too many footguns) — demote back to CLI with appropriate operator warnings.

---

## D21 — JS Framework Threshold (RE-RULED 2026-04-27)

**Date:** 2026-04-27 (original) / **Re-ruled:** 2026-04-27 (same day, founder directive)
**Author:** Richard
**Context (original):** D10 locked vanilla JS through v1.0. Dinesh E-1 raised the question for v1.1+ given `app.js` is 9,350 LOC. Original ruling deferred adoption pending 4 triggers, one of which was a frontend engineer hire.
**Context (re-rule):** Founder explicit directive: "do not worry about hiring someone or number of locations, expect that it's worth looking at framework re-eval, if we can do it in house with the team we have we do it. this is opensource so right now there is no revenue loss." This invalidates Trigger 2 (hire) and lowers the bar for the in-house re-eval. Verified state: `app.js` = 9,388 LOC; total static JS = 15,248 LOC across 8 files. **Trigger 1 (>5,000 LOC) has fired and was already firing at original rule time.** The original ruling was effectively gated on Trigger 2 in spirit; with Trigger 2 invalid, Trigger 1's firing demands action.

**Question:** Adopt a framework now (Sprint B/C window) or keep deferring?

**New decision:** **Adopt a framework, but on a deliberate sequencing — module-split first, framework second, on the clean module boundaries. Framework choice ruled in D23.**

**Revised triggers (drop #2; keep the rest as ongoing health checks):**
1. ~~Total LOC across all `pages/*.js` modules + `app.js` exceeds **5,000**~~ — **FIRED 2026-04-27** (15,248 LOC actual). Re-eval done; outcome below.
2. ~~Frontend engineer hire — INVALID per founder directive 2026-04-27.~~ Removed. We staff from the agent fleet (Dinesh leads frontend) and proceed in-house.
3. A specific feature requires complex form state machines (e.g., the v2.0 PI dashboard with cross-filtering charts) that vanilla makes painful. **Still active** as a future re-eval trigger for escalating beyond Alpine.
4. CSP enforcement (E-3) becomes priority — CSP migration is the natural moment to revisit framework choice. **Still active** but largely subsumed by D23's Alpine adoption (Alpine is CSP-friendly with `unsafe-eval` workaround documented).

**Re-eval outcome (this is the new ruling):**
1. **Module-split first (Sprint B.5, v1.1.1).** Refactor `app.js` (9,388 LOC) and `slurm.js` (2,033 LOC) into `pages/*.js` ES6 modules. Pure mechanical refactor. No framework yet. Goal: clean module boundaries we can layer onto.
2. **Framework adoption second (Sprint C, v1.2.0).** Researcher portal is the greenfield proof case for Alpine — small surface, contained blast radius, zero risk of regressing v1.1 admin/operator UI. Backfill Alpine into existing pages opportunistically as we touch them (audit log table is the obvious second candidate).
3. **Choice locked in D23 below: Alpine.js (vendored, no build step) + minimal HTMX for table-driven pages.** Not Preact/Vue/Svelte — those need a build step and violate D10's "one binary, one container, no build step" positioning.

**Standing principle (founder directive baked in):** Sprints do not stop. Sprint dispatch is automatic — when one sprint closes, the next begins on schedule. No headcount/revenue/commercial gating. We are open-source; cost of churn is engineering time only.

**Reversibility:** **costly**. Once Alpine is in `vendor/`, it stays. The mitigation: Alpine can be removed from any single page by reverting that page to vanilla — incremental adoption is incrementally reversible. Full removal across the codebase would be a multi-week project; we accept that cost.

**Re-decision triggers (going forward):**
- Trigger 3 (form state-machine feature) fires → re-eval whether Alpine is sufficient or we need a real reactive framework. Lit and Preact remain the escalation candidates (in that order; Lit if web-components ergonomics fit, Preact if we need React ecosystem).
- Trigger 4 (CSP) fires → confirm Alpine-CSP plan is still viable (Alpine 3 supports CSP-safe build).
- New trigger 5: if Alpine adoption stalls or causes operator-visible regressions in 3 consecutive releases, fall back to vanilla-only and accept the LOC ceiling.

**Supersedes:** Original D21 (same date, earlier in day). Original triggers retained above with strikethrough for audit trail.

---

## D22 — Raw Config Editor Pattern

**Date:** 2026-04-27
**Author:** Richard
**Context:** Jared C-1 flagged the raw `slurm.conf` editor as a Blocker (no validation). Jared C-2 raised the same shape for custom kickstart. C-4 for network freeform fields. Multiple config surfaces in v1.0 expose raw textareas. The pattern decision needs to be made once and applied everywhere.

**Question:** For all config surfaces that today expose raw textareas (slurm.conf, kickstart, network profiles, BMC config): do we use structured forms with "advanced: edit raw" escape hatch, OR keep raw with validation?

**Decision:** **Both, in this exact pattern: structured form for the 80% case, raw editor as an "Advanced: edit raw" escape hatch, server-side validation on every save (regardless of which surface produced the input).**

**The pattern (locked):**
1. **Structured form** for the 8-15 most common directives — covers ~80% of operator use cases. New admins never leave this.
2. **"Advanced: edit raw" disclosure** that reveals a textarea pre-populated with what the structured form would render. Operator can edit freely.
3. **Server-side validation on save** — for Slurm: `slurmd -C` or AST parse. For kickstart: `pykickstart` parse. For network: `nm-online` config dry-run or equivalent. Validation runs regardless of which surface (form or raw) the operator used.
4. **Inline error display** with line numbers and structured messages. Hard-block on parse errors. "Save anyway" button only for high-confidence-but-non-fatal warnings (e.g., deprecated directive still works).
5. **Default templates always re-renderable** via D18 reseed mechanism (operator never permanently loses the recommended starting point).

**Concrete sequencing:**
- v1.1: Slurm.conf gets validation (B5-1, B5-2 in webui sprint plan). Custom Kickstart gets "View default template" link (B5-3). Raw editor stays as-is for both.
- v1.2 / v1.3: Quick Settings structured form layered above the raw editor for slurm.conf. Same pattern applied to kickstart and network profiles.
- v2.0+: BMC/IPMI config gets the same treatment.

**Why this principle (not "remove the raw editor" or "keep raw without form"):**
- New-admin onboarding (Jared's #1 theme, founder priority) requires that the default path is "fill in the form, click Deploy".
- Power-user flexibility (Persona 1 sysadmin) requires that the raw editor is never taken away — there are too many edge cases that don't fit any structured form.
- Structured form + raw escape + validation is the only pattern that satisfies both audiences without feature loss.

**Tradeoff acknowledged:** Building structured forms is real work. We do it incrementally — slurm.conf in v1.1 (validation only) and v1.3 (full structured form). Other config surfaces follow as time permits.

**Reversibility:** **cheap** at the per-surface level. Adding a structured form on top of an existing raw editor is purely additive. Removing the raw editor is the call we never make.

**Re-decision triggers:**
- A specific config surface turns out to be 100% covered by the structured form (operators never use the raw escape) — at that point we can consider removing the raw escape for that surface only. Unlikely.
- A new config surface comes online that has no sensible structured-form shape (e.g., custom Lua hooks) — use raw-only with validation as the pattern, document why structured form was skipped.

---

## D23 — Framework Choice: Alpine.js + minimal HTMX, vendored, no build step

**Date:** 2026-04-27
**Author:** Richard
**Context:** D21 re-rule fired the framework re-evaluation. This decision picks the actual framework. Constraints (per D10 + founder directive): no build step if avoidable, must work in standard Go embed flow, must allow incremental adoption (one page at a time, old vanilla pages keep working), must align with self-hosted simplicity.

**Question:** Which framework — Alpine, HTMX, Lit, Preact, Vue, or stay vanilla and just modularize?

**Options considered:**

| Option | Size | Build step? | DX for our shape of work | Verdict |
|---|---|---|---|---|
| **Alpine.js 3** | ~15 KB min+gzip | No | Excellent for declarative reactivity on top of server-rendered HTML — `x-data`, `x-show`, `x-on:click`, `x-for` cover 90% of what we hand-roll today | **CHOSEN** |
| **HTMX** | ~14 KB | No | Excellent for table-driven pages (audit log, deploy progress) — `hx-get`, `hx-trigger="every 2s"`, `hx-swap` replaces our hand-rolled SSE/poll boilerplate | **CHOSEN** for specific surfaces (audit, deploy progress, anomaly card) |
| Lit (web components) | ~6 KB | No (but needs lit-html template literals) | Web components ergonomics are heavy for our needs; shadow-DOM scoping is a foot-gun for our existing CSS; no clear win over Alpine | Rejected |
| Preact + htm | ~5 KB | No (with htm) but realistically yes (JSX preferred) | React mental model is overkill for our CRUD pages; htm is awkward; ecosystem pull will eventually drag a build step | Rejected |
| Vue 3 (no-build, ESM) | ~50 KB | Optional but real-world yes | Heavier than Alpine for the same job; SFC ecosystem drags in build tooling | Rejected |
| Svelte | ~5 KB runtime | **Required** | Compiler-driven; violates D10 explicitly | Rejected |
| React + Vite | ~45 KB + build | **Required** | Violates D10; massive ecosystem pull; we'd be the only Go-embedded webui shipping a Vite build | Rejected |
| Vanilla + ES6 modules only (no framework) | 0 KB | No | Module-split helps maintainability but does nothing for reactive state, role-aware rendering, or form state machines — we'd keep hand-rolling these badly | Insufficient — module-split is necessary but not sufficient |

**Decision:** **Alpine.js 3 + HTMX 2 (latest stable at adoption time). Both vendored under `internal/server/ui/static/vendor/`. Both served via Go's `embed.FS`. No CDN. No build step. No bundler. No npm/package.json.**

**Adoption rules:**
1. **Vendored only.** Drop `alpine.min.js` and `htmx.min.js` into `internal/server/ui/static/vendor/`. Pin exact version in a `VENDOR.md` alongside (record version, source URL, SHA256, license). Re-vendor only via deliberate PR.
2. **Loaded via standard `<script>` tags** in the layout HTML — no module imports for the framework itself (Alpine attaches globally; HTMX same).
3. **Alpine for in-page reactive state** — role-aware rendering, modal show/hide, dirty-flag tracking, form validation, conditional disabling of mutation buttons.
4. **HTMX for server-driven swaps** — audit log filter/paginate, deploy progress polling, anomaly card refresh, webhook delivery history. Anywhere we'd otherwise write `setInterval(fetch(...).then(render(...)))` boilerplate.
5. **Vanilla retained** for: app router (#/route hash routing), Auth bootstrap, SSE log streams (HTMX SSE extension is optional but not adopted v1.2 — re-eval v1.3), API wrapper module (`api.js`).
6. **No framework on existing v1.0/v1.1 pages until they're touched for other reasons.** Researcher portal is the greenfield introduction. Audit log (Sprint B B3-3) is the SECOND adoption — but for the v1.2 cycle, not retroactively in Sprint B (Sprint B keeps vanilla; B3-3 ships in vanilla; the Alpine/HTMX rewrite of audit log is a Sprint C deliverable bundled with Researcher portal work).
7. **Mixing vanilla and Alpine on the same page is supported and expected during the transition.** Alpine `x-data` attaches scoped state to a DOM subtree; surrounding vanilla code continues to work.

**Why this combo (rationale on the choice):**
- **Alpine + HTMX is the only no-build option that gives us BOTH client reactivity AND server-driven swaps without a heavyweight runtime.** Most frameworks pick one of those two; we need both for our shape of work.
- **Both are mature, MIT, single-vendor-file, and have stable APIs.** Alpine has been at v3 since 2021; HTMX at v1+ since 2020.
- **Both sources are tiny** (~30 KB combined min+gzip). Pages stay snappy on the operator's laptop in a server room over a flaky LAN.
- **No build step** preserves D10's "one binary, one container, no build step" positioning intact. CI stays simple. Deploy stays simple. Operator running `clustr-serverd` on a Rocky 9 box never needs Node installed.
- **Alpine is CSP-friendly via Alpine 3's CSP build** (`alpinejs-csp`) — when E-3 (CSP enforcement) trigger fires, we swap the vendored file for the CSP build. Mitigates D21 trigger 4 risk.
- **HTMX has explicit support for server-side rendering of partials** — we already render JSON, switching specific endpoints to also render HTML partials is additive (new endpoint or content-negotiation, not a rewrite).

**Sprint B.5 update (2026-04-27 — pilot complete):**

Status: **in-progress — pilot page migrated, pattern documented**.

Vendored versions:
- Alpine.js **3.15.11** — `internal/server/ui/static/vendor/alpinejs/alpine-3.15.11.min.js`
  - SHA256: `beeba63d08956f64fa060f6b6a29c87a341bf069fb96c9459c222c6fd42e58ae`
  - Verified against jsdelivr + unpkg on 2026-04-27
- HTMX **2.0.9** — `internal/server/ui/static/vendor/htmx/htmx-2.0.9.min.js`
  - SHA256: `57d9191515339922bd1356d7b2d80b1ee3b29f1b3a2c65a078bb8b2e8fd9ae5f`
  - Verified against jsdelivr + unpkg on 2026-04-27

Integrity manifest: `internal/server/ui/static/vendor/VENDOR-CHECKSUMS.txt`

Pilot migration: **DHCP Leases page** (`#/network/allocations`) — chosen because it is
a small, clean, read-only list with no mutation buttons (low blast radius) and representative
of the most common pattern (fetch JSON → render table). `Pages.dhcpLeases()` now renders an
Alpine `x-data` component; the vanilla string-building pattern is fully replaced.

Pattern documentation: `docs/frontend-patterns.md` — the Sprint C playbook.

**Concrete v1.2 deliverables (reflected in webui-sprint-plan.md updates):**
- [DONE in B.5] Vendor Alpine 3.15.11 + HTMX 2.0.9.
- [DONE in B.5] Pilot migration: DHCP Leases page to Alpine.
- [DONE in B.5] Pattern documentation in `docs/frontend-patterns.md`.
- Researcher portal `/portal/` (C1 workstream) built with Alpine for reactive state.
- Audit log page (already shipped vanilla in v1.1) rewritten to HTMX for filter/paginate UX.
- Anomaly card (already shipped vanilla in v1.1) rewritten to HTMX for periodic refresh.
- All other v1.0/v1.1 pages remain vanilla until touched.

**Reversibility:** **per-page reversible, codebase-wide costly.** A single page can be reverted to vanilla in an afternoon (rip out `x-data`/`x-on`/`hx-get`, write the equivalent JS). Removing Alpine + HTMX everywhere would be a multi-week project after v1.2 adoption. Mitigation: every Alpine/HTMX adoption PR must demonstrate clear value over the vanilla equivalent in the PR description.

**Re-decision triggers:**
- A v2.0 feature (PI cross-filtering charts, multi-pane dashboards) blows past Alpine's complexity ceiling — escalate to Preact or Vue with a build step, accepting the D10 hit.
- Alpine or HTMX hits an EOL / abandonment signal (no commits in 18 months, security CVE unpatched > 30 days) — fall back to vanilla on affected surfaces, evaluate replacements.
- Operator complaints about page weight / load time — measure before re-deciding; expect this trigger to never fire.

**Out of scope for this decision:** TypeScript (still no), JSX (still no), bundlers (still no), Tailwind/PostCSS (still no — vanilla CSS in the vendored stylesheet stays). If any of those become desirable, that's a new D-number and a new fight.

---

## D24 — Powerhouse Positioning Thesis (standing statement)

**Date:** 2026-04-27
**Author:** Richard (synthesizing Monica's `coldfront-feature-mapping.md` Phase 3)
**Context:** Monica's ColdFront feature mapping (commit `2a25fd0`) articulated a one-paragraph positioning statement after inventorying 40 ColdFront features and assessing clustr's complementary surface. The statement frames clustr as the unified bare-metal-to-governance platform that does not currently exist in open-source HPC. Founder directive on this work was "powerhouse of an application" — Monica's framing operationalizes that directive into a defensible market position.

**Question:** Adopt Monica's positioning statement as a standing reference for all subsequent sprint plans, marketing copy, and architecture decisions?

**Decision:** **Adopted as-is, lightly edited for compactness. This is the standing positioning statement going forward.**

**The positioning statement (locked):**

> The HPC platform market has a decade-old gap: node provisioning tools (xCAT, Warewulf) handle bare metal but ignore governance, while allocation management tools (ColdFront) handle governance but ignore bare metal — leaving every HPC center to maintain a fragile custom bridge between them. clustr closes that gap in a single open-source Go binary that provisions nodes, installs and configures Slurm with a GPG-signed bundle, manages LDAP accounts, gives researchers a self-service status portal, gives PIs a utilization dashboard, and gives IT directors the impact reporting they need to justify compute spend — all self-hosted, all air-gap compatible, all with zero egress and a cryptographically verified trust chain from allocation decision to running job. The window is open because the major incumbents (Bright Computing) are proprietary and expensive, the open-source alternatives (xCAT, Warewulf, ColdFront) are siloed, and no single team has shipped a unified platform.

**The three load-bearing differentiators (locked):**
1. **Single binary, single data model.** Cluster state and allocation state are the same SQLite database. No separate Django, no PostgreSQL, no worker queue, no separate allocation DB. NodeGroup is the unified primitive (Project + Allocation + Resource collapsed into one entity).
2. **Signed-bundle trust chain.** Provisioning-to-governance is cryptographically bound end-to-end. PI's allocation maps to a NodeGroup; that group drives Slurm partition config; that config is GPG-signed. ColdFront + xCAT cannot tell this story.
3. **Air-gap native.** Embedded Slurm repo, static binary distribution, zero outbound, no Python ecosystem. Works in classified environments and research networks without internet routing.

**Three competitive positions (this is how we frame against each in pitches):**
- **vs. xCAT / Warewulf:** "We do everything they do plus governance — turn an operational tool into a platform department heads can justify to their CFO."
- **vs. ColdFront:** "We absorb their governance surface; you don't run two systems anymore." Genuine displacement play, not a "we do it better" argument.
- **vs. Bright Computing:** "80% of Bright's feature surface at 0% of the license cost; open-source, truly self-hosted, no vendor lock-in." The remaining 20% (HA failover, enterprise GPU scheduling) is a future paid-tier upsell — not v1.x scope.

**Why adopted as-is (not edited or deferred):**
- Monica's analysis is grounded in 40-feature inventory, not slideware.
- The market gap is real and decade-old — competing claim by anyone else would be checkable and false.
- The three differentiators are genuinely defensible (single binary, crypto trust chain, air-gap) — none of these are easy to retrofit into an existing competitor's stack.
- Founder directive ("powerhouse of an application") aligns with the positioning. Deferring would be hedge-for-hedging's-sake.

**What this commits us to (operationally):**
- All subsequent sprint plans are framed against this positioning. v1.2 researcher portal, v1.2.5 PI governance, v1.3 IT director reporting, v1.4 impact reporting, v2.0 multi-tenant — every sprint is a step toward the unified platform described above.
- All marketing copy (Show HN, README, docs landing) leads with the gap-closing frame, not "another HPC tool."
- All competitive positioning conversations (institutional pitches, conference talks) lean on the three differentiators.

**What this does NOT commit us to (be honest about scope):**
- We do not commit to building all 40 ColdFront features. D25 governs that prioritization.
- We do not commit to displacing ColdFront in their existing customer base. We commit to being the natural choice for new institutional procurements where the choice is "ColdFront + xCAT vs. clustr."
- We do not commit to the paid tier roadmap (the "remaining 20% upsell" line) — that's a v3.0+ business model conversation, not a v1.x technical commitment.

**Reversibility:** **one-way for positioning purposes** (you cannot un-say a market position once you've launched on it), **cheap at the code level** (positioning shifts don't force code changes; the code already supports the positioning). The asymmetry is the point: making this call is high-stakes for messaging, low-stakes for engineering.

**Re-decision triggers:**
- A second open-source project ships a unified provisioning + governance platform before our v1.2 (kills the "no single team has shipped" claim) — re-frame against the new entrant.
- A paying customer materially redefines what "governance" means (e.g., they want allocation-as-budget rather than allocation-as-physical-resource) — re-frame Risk 4 from coldfront-feature-mapping.md.
- Bright Computing or another commercial incumbent open-sources their governance layer — major re-eval.

**See:** `docs/coldfront-feature-mapping.md` Phase 3 for the full thesis writeup, competitive positioning details, and the "Why clustr Now" pitch.

---

## D25 — Customer-Pull Gate for Governance Features

**Date:** 2026-04-27
**Author:** Richard (resolving Monica's Risk 1 from `coldfront-feature-mapping.md`)
**Context:** Monica flagged in her Phase 5 risk analysis: "every ColdFront-inspired feature in the backlog must have a named customer persona waiting for it before development starts. No speculative governance features." Her concern is that ColdFront's 40-feature surface represents 12-18 months of engineering for a 2-person team if built to ColdFront's depth, and the founder's "powerhouse" directive could absorb all available velocity into governance work that nobody asked for. Founder's standing rule (memory: `feedback_no_headcount_gating.md` + `feedback_continuous_sprints.md`): no revenue or headcount gating; sprints do not stop. These two framings are in genuine tension.

**Question:** Do we adopt Monica's customer-pull gate as a hard constraint on governance feature work, or does the founder's "sprints don't stop" directive override it, or is there a synthesis?

**Decision:** **Hybrid. NO blanket customer-pull gate. Build structural primitives speculatively (roles, surfaces, conceptual model) where the marginal cost is low and the optionality value is high. DEFER specific implementations that have high implementation cost AND require external validation to define correctly. Richard owns the speculative-vs-pulled call per sprint, applying the prioritization rule below.**

**The prioritization rule (locked):**

For every ColdFront-inspired feature considered for any sprint, classify by two axes:

| Axis 1: Implementation Cost | Axis 2: Customer-Definition Risk | Verdict |
|---|---|---|
| Low (≤1 sprint) | Low (we know what to build) | **BUILD SPECULATIVELY** in next available sprint |
| Low (≤1 sprint) | High (customer must define metrics/shape) | **BUILD THE PRIMITIVE SPECULATIVELY**; defer customer-specific shape |
| High (≥2 sprints) | Low (we know what to build) | **BUILD WHEN SEQUENCED**; sprint plan governs |
| High (≥2 sprints) | High (customer must define) | **DEFER until first named customer pulls** |

**Concrete v1.2-v1.4 application:**
- **PI role + RBAC primitive** → low cost, low risk → **build speculatively in v1.2.5**
- **PI self-service member management** → low cost, low risk (UX is well-understood) → **build speculatively in v1.2.5**
- **NodeGroup utilization view (read-only aggregation of existing data)** → low cost, low risk → **build speculatively in v1.2.5**
- **IT Director read-only summary view (existing data, no new metrics)** → low cost, low risk → **build speculatively in v1.3**
- **Email notifications for LDAP events** → medium cost (SMTP infra), low risk → **build in v1.3 once SMTP scaffolding lands**
- **Grant tracking (CF-12)** → medium cost (new schema), low risk (well-defined model from ColdFront) → **build speculatively in v1.3**
- **Publication tracking + DOI search (CF-13)** → medium cost, low risk → **build speculatively in v1.3**
- **Annual project review workflow (CF-11)** → medium cost, MEDIUM risk (workflow shape varies by institution) → **build minimal version v1.4; full workflow gated on customer pull**
- **XDMoD integration (CF-27)** → high cost (external system dependency), HIGH risk (only ~half the target market runs XDMoD) → **DEFER until first named customer with XDMoD pulls**
- **FreeIPA HBAC bridge (CF-22)** → high cost, high risk (FreeIPA shape varies wildly by deployment) → **DEFER until first named customer with FreeIPA pulls**
- **Custom impact reporting metrics** → low code cost but HIGH definition risk (wrong metrics = throwaway work) → **DEFER until first paying customer specifies first 3 metrics**

**Why hybrid (rejection of both pure positions):**

**Why we reject pure customer-pull gate (Monica's stated framing):**
- Open-source product with no revenue gate means there are zero paying customers to pull. Customer-pull-only would freeze governance work indefinitely.
- The structural primitives (roles, surfaces, conceptual NodeGroup-as-allocation framing) have high optionality value and low cost. Building them speculatively pre-positions us for the first institutional conversation; not building them means the first institutional pitch hits a "we'd need to build that" wall.
- Founder directive is explicit: "if we can do it in house with the team we have we do it. this is opensource so right now there is no revenue loss." Customer-pull gating contradicts this directly.

**Why we reject pure speculative build (founder's literal directive):**
- Monica's risk is correct: the 40-feature surface IS too large to build to ColdFront's depth speculatively. The two-person agent team plus operator review bandwidth is bounded even though hiring and revenue aren't.
- Some features (custom metrics, customer-specific workflow shapes, external system integrations) are 100% wasted work if built wrong, and they CANNOT be built right without customer input. These are the genuine "wait for pull" cases.
- Building everything speculatively risks a v1.5 codebase carrying 30 features of governance code where 5 features get used. The maintenance burden compounds.

**The synthesis:** Speculative build is the default for cheap structural work. Customer-pull is the gate for expensive, customer-shaped work. Richard makes the per-sprint call applying the rule above.

**Standing meta-rule (founder directive baked in):** If Richard is genuinely on the fence, default to BUILD over DEFER. Cost of speculative build is engineering time only (no revenue at risk). Cost of deferral is missing the institutional pitch window. Asymmetry favors building.

**What this means for the sprint plan:**
- v1.2 (Sprint C): Researcher portal MVP + Alpine/HTMX framework adoption — already locked, unchanged.
- v1.2.5 (Sprint C.5, NEW): PI role + PI self-service + NodeGroup utilization view — speculative but cheap and high-optionality.
- v1.3 (Sprint D, NEW): IT Director read-only + email notifications + grants + publications — speculative; structural primitives that ColdFront proves users want.
- v1.4 (Sprint E, NEW): Impact reporting + annual review (minimal) + allocation change requests + admin messaging — speculative but at the boundary; revisit if v1.3 governance features see no use after 6 months.
- v2.0+: OIDC, FreeIPA, multi-tenant, PostgreSQL, OpenLDAP project plugin — gated by D1, D6, D15-style external triggers (which are about technical scale, NOT revenue gating).

**Reversibility:** **cheap**. This is a prioritization principle, not a code commitment. Re-deciding the rule re-prioritizes the next sprint, no migration needed.

**Re-decision triggers:**
- v1.3 ships and 6 months in, no operator (free or paid) is using the governance features → tighten the gate; pause v1.4 scope, divert to provisioning depth or framework polish.
- A named institutional customer LOIs with a governance feature request that doesn't fit our prioritization → re-shuffle the sprint queue to pull their request forward.
- Monica revisits the risk and judges the speculative bets are accumulating without payoff → tighten the gate.

**Supersedes:** Implicit "wait for first paying customer" gates from C8 in `webui-sprint-plan.md` and from `coldfront-feature-mapping.md` Phase 5 Risk 1. Both narrowed: the wait-for-customer gate now applies only to customer-definition-risk features (per the table above), not to governance work as a whole.

**See:** `docs/coldfront-feature-mapping.md` Phase 5 for Monica's original risk framing; `docs/webui-sprint-plan.md` Sprints C.5, D, E for the concrete sprint application of this rule.

---

## D26 — Attribute Visibility Defaults (Sprint E)

**Date:** 2026-04-27
**Author:** Dinesh (implementation decision during Sprint E, E-3)
**Context:** Sprint E introduces per-attribute visibility controls with four levels
(`public > member > pi > admin_only`). Global defaults must be seeded in migration 066.
The question: what should the default visibility be for each NodeGroup attribute, and
should the bias be toward openness or restriction?

**Question:** For each tracked group attribute, what visibility level is the
appropriate default, and on what principle?

**Decision:** **Default to the least-sensitive reasonable level per attribute class.
Administrative and financial data defaults to `pi` or `admin_only`. Operational data
visible to members defaults to `member`. Publicly legible research identity data
defaults to `public`. Security credentials are always `admin_only`.**

**Defaults locked in migration 066:**

| Attribute | Default | Rationale |
|---|---|---|
| `grant_amount` | `pi` | Financial sensitivity — members don't need dollar amounts |
| `grant_number` | `pi` | Award numbers are often embargoed by sponsor |
| `funding_agency` | `public` | Widely published in papers; no sensitivity |
| `field_of_science` | `public` | Research classification; openly visible |
| `node_count` | `member` | Members need to understand group scale; external: not needed |
| `pi_name` | `public` | PI identity is public research record |
| `description` | `public` | Group purpose/abstract; standard institutional transparency |
| `bmc_credentials` | `admin_only` | Hardware access credentials; never expose below admin |
| `publication_doi` | `public` | Published work; DOI is citable in public record |
| `publication_title` | `public` | Published work title |
| `publication_authors` | `public` | Author list |
| `utilization_stats` | `member` | Node utilization data; internal operational metric |
| `slurm_partition` | `member` | Partition names are HPC-internal |
| `node_hardware` | `pi` | Hardware specifications; PI-level operational detail |

**The principle (locked):** "Least-sensitive reasonable" — not maximum openness, not
maximum restriction. Each attribute's default follows the most common institutional
expectation for that attribute class. Admins and PIs can override per-project when
their policy differs.

**Why not admin_only for everything (maximum restriction):** Makes the system useless
for its intended purpose — researchers can't see their own group's relevant operational
context. Defeats the ColdFront-inspired self-service model.

**Why not public for everything (maximum openness):** Financial and hardware details
have genuine sensitivity. Grant amounts in particular are often pre-publication
embargoed. BMC credentials exposed to members is a security failure.

**PI override:** PIs can override any attribute's visibility for their own group via
the PI portal "Visibility" tab, scoped to their group only. This respects institutional
variation without requiring admin intervention.

**Admin override:** Admins can change the global default for any attribute via the
"Governance" settings tab. This allows system-wide policy re-alignment when an
institution has blanket rules (e.g., "all financial data is admin_only").

**Reversibility:** **cheap**. Visibility defaults are seeded by migration and can be
changed at runtime by admins. No data migrations required for default changes.

**Re-decision triggers:**
- An institutional operator reports that a default creates friction or a compliance issue.
- A new attribute is added; this decision should be extended to cover it with an explicit
  rationale following the same principle.

---

## D27 — Sprint Z Re-Sequencing (supersedes D25)

**Date:** 2026-04-27
**Author:** Richard (Technical Co-founder)
**Context:** D25 ruled a hybrid customer-pull gate for governance features. The rule was correct, but in practice the bucketing label "customer-pull" was overbroad: cheap structural primitives (CSP headers, SIEM JSONL export, OpenLDAP plugin, manager delegation, optional allocation expiration) ended up parked in the undifferentiated "Sprint Z (v2.0+ horizon, NOT committed)" bucket alongside genuinely customer-shaped work (custom metrics, OIDC, FreeIPA, XDMoD). The founder's standing rules — continuous sprints (`feedback_continuous_sprints.md`), no headcount/revenue gating (`feedback_no_headcount_gating.md`), and D25's own closing line ("when on the fence, default to BUILD over DEFER") — all argue that the cheap structural items should be committed sprints, not horizon items.

**Question:** Does customer-pull gating apply to all of Sprint Z, or only to the items that genuinely need customer specification?

**Decision:** **Dissolve the undifferentiated Sprint Z. Re-bucket every Z item into one of four explicit buckets. Customer-pull gating applies ONLY to Bucket 3 (CUST-SPEC).**

**The four buckets (locked):**

| Bucket | Definition | Sprint placement |
|---|---|---|
| **1. BUILD NOW** | Cheap structural primitives + obvious wins. Don't need customer specification to build correctly. | Committed sprints F/G/H at v1.5.0 → v1.6.0 → v1.7.0 |
| **2. TECH-TRIG (technical-pull trigger)** | Real cost / real risk profile. Don't need customer to specify, but DO need a concrete technical signal that the cost is justified. | Unscheduled. Each item has explicit trigger + monitor location + decision-maker. Dispatched when trigger fires. |
| **3. CUST-SPEC (customer specification)** | Genuinely needs a named customer to define the contract (custom metrics, third-party integrations into their stack, IdP shape). Speculative build = throwaway code. | Unscheduled. Dispatched when first customer pulls. |
| **4. SKIP** | Out of clustr's positioning (e.g., cloud allocation per D24). Explicit non-goal. | Not built in v2.x. Revisit at v3.0+ if positioning changes. |

**Application to old Sprint Z items (14 themes + 13 CF-Z items, deduplicated):**

- **Build now (7 items)** → Sprints F/G/H (see `webui-sprint-plan.md` for deliverables):
  - F (v1.5.0): CSP headers, SIEM JSONL export, optional allocation expiration (CF-03 optional)
  - G (v1.6.0): OpenLDAP project plugin (CF-24), resource access restriction by group (CF-40), manager delegation (CF-09 manager scope)
  - H (v1.7.0): Auto-compute allocation (CF-29)
- **Tech-trigger (4 items)** — explicit triggers documented in `webui-sprint-plan.md` Bucket 2 table:
  - PostgreSQL migration (CF-38) — trigger: SQLite write contention >50 ops/sec sustained 1h OR single deployment >500 nodes OR multi-tenant requirement triggers
  - Multi-tenant data isolation — trigger: hosted-clustr-as-service decision OR operator runs ≥3 logically-separate fleets needing isolation
  - Heavier framework migration — trigger: single page exceeds 800 LOC of Alpine x-data state OR triggers >3 architectural workarounds
  - Two-tier hot/cold log archive — trigger: operator-reported eviction incident OR retention env raised >30 days by ≥2 operators
- **Customer-spec (7 items)** — wait for first named customer:
  - OIDC/SAML federation (CF-25) — needs IdP named
  - FreeIPA HBAC bridge (CF-22) — needs FreeIPA-running operator
  - XDMoD integration (CF-27) — needs XDMoD-running operator
  - Customer-defined utilization metrics — needs metric set specified
  - Custom allocation attributes (CF-04), custom resource attributes (CF-06), custom attribute types (CF-37) — need attribute set specified
- **Skip (1 item)** — explicit defer to v3.0+:
  - Cloud resource allocation (CF-30) — out of clustr's bare-metal-first positioning per D24

**Why supersede D25 rather than amend:**

D25's text was internally consistent and the prioritization table was correct. The failure mode was that "Sprint Z" as a single bucket label encouraged future agents to sweep cheap structural work into a deferral pile. D27 makes the bucketing crisp and the labels self-explanatory ("BUILD NOW" can't be misread as "defer"). The substance of D25's prioritization rule (low-cost low-risk → build speculatively; high-cost high-customer-risk → defer) survives intact within Buckets 1 and 3.

**Standing meta-rule (carried forward from D25, made primary in D27):** When Richard is on the fence between BUILD-NOW and one of the gated buckets, default to BUILD-NOW. Cost of speculative build is engineering time only (no revenue at risk per `feedback_no_headcount_gating.md`). Cost of deferral is missing the institutional pitch window. Asymmetry favors building.

**Decision authority for trigger evaluation (Bucket 2):**
- **Technical triggers** (PostgreSQL contention, framework LOC ceiling, log eviction) → Richard decides when triggered.
- **Product triggers** (multi-tenant requirement from a hosted-service decision, customer-pull arrivals routed to Bucket 3) → Founder decides.

**Reversibility:** **cheap**. Re-bucketing items between buckets is a doc edit + sprint plan update; no code commitment. If a Bucket 1 item turns out to need customer input (we discover during F/G/H build), drop it back to Bucket 3 and dispatch a customer conversation.

**Re-decision triggers:**
- A Bucket 1 sprint (F/G/H) ships and the feature sees zero adoption after 6 months → reconsider Bucket 1 criteria; tighten toward speculative-build skepticism.
- A Bucket 3 customer-pull arrives that contradicts our IdP/integration assumptions → re-shuffle the relevant CUST-SPEC item and possibly revise Bucket 1 priorities.
- Founder directive that changes the BUILD-NOW default → re-rule entirely.

**Supersedes:** D25 (Customer-Pull Gate for Governance Features). D25 remains in this document for traceability; its decision text is marked SUPERSEDED in the index.

**See:** `docs/webui-sprint-plan.md` Sprint Z section (re-sequenced) for the per-item bucket assignments and Sprint F/G/H deliverable lists.

**TECH-TRIG monitoring (Sprint M, v1.11.0):** All four Bucket 2 triggers are now instrumented. The background evaluator runs every 10 minutes and writes results to the `tech_trig_state` table. Admin UI surface: Settings > Tech Triggers. API: `GET /api/v1/admin/tech-triggers`. Prometheus: `clustr_tech_trigger{name}` and `clustr_tech_trigger_value{name}`. Operator runbook: `docs/tech-triggers.md`.

---

## D28 — Versioning Policy (v1.x vs v2.x boundary)

**Date:** 2026-04-27
**Author:** Richard (Technical Co-founder)
**Context:** With Sprint Z re-sequenced (D27) into committed Sprints F/G/H at v1.5.0 → v1.7.0, the v1.x → v2.x boundary needs an explicit policy. Historically the boundary has implicitly meant "breaking changes," but absent a written rule future agents may drift the version number arbitrarily — bumping to v2.0 just because the feature feels big, or staying at v1.x through a schema migration because the change set isn't dramatic. We need a crisp, mechanical rule.

**Question:** What classes of change warrant a major-version bump (v2.0.0), versus a minor bump (v1.x.0), versus a patch (v1.x.y)?

**Decision:** **Strict semver-flavored policy:**

**Patch bump (v1.x.y):** Hotfix only. Bug fixes that don't change any contract. Example: Sprint A v1.0.1.

**Minor bump (v1.x.0):** All additive changes. New endpoints, new optional fields, new RBAC roles (additive — existing roles unchanged), new pages, new plugins, new audit events, new optional database columns. The defining test: an operator on the previous version can upgrade with no config changes, no data migration steps beyond automatic schema migrations, no client/integration changes. Examples: Sprint B (v1.1.0) added webhooks + audit UI; Sprint D (v1.3.0) added director portal + SMTP; Sprint E (v1.4.0) added attribute visibility + FOS classification; Sprint F (v1.5.0) adds CSP + SIEM export + optional expiration.

**Major bump (v2.0.0):** Reserved for any of the following:

(a) **Schema migration requiring data conversion or schema-wide changes to existing tables.** Examples: PostgreSQL migration (data export/import, driver swap), multi-tenant `tenant_id` added to every table (backfill required), attribute type system that re-shapes the existing `attributes` table.

(b) **Auth contract change.** Examples: OIDC/SAML replacing or modifying API key/session semantics in ways that require operator action (re-issuing keys, re-binding sessions). Adding OIDC alongside existing API keys, with no removal, would NOT trigger major — that's additive (minor).

(c) **Breaking API contract on stable surfaces.** Examples: removing an endpoint from `/api/v1/`, renaming a field in a documented response schema, changing a status code semantically. Internal endpoints (`/api/internal/`) are not in scope.

(d) **D10 violation.** Heavier framework migration that requires npm/build step. Operators currently can `git clone && go build` — introducing Node.js as a build dependency is an operator-facing contract change.

(e) **Wire protocol break.** Webhook payload schema change that breaks existing receivers; SSE event schema change that breaks existing dashboards.

**Mechanical rule:** If an operator on v1.x can upgrade to v1.(x+1) by `git pull && systemctl restart clustr-serverd` with no other action, it's minor. If they have to do anything else (run a migration, change config, re-issue keys, install Node, update integrations), it's major.

**Concrete near-term application:**
- Sprint F (v1.5.0) — additive (CSP headers, JSONL endpoint, optional nullable column). MINOR.
- Sprint G (v1.6.0) — additive (new role `manager`, new optional `allowed_request_groups[]`, OpenLDAP plugin). MINOR.
- Sprint H (v1.7.0) — additive (auto-allocation policy default OFF, new endpoints). MINOR.
- PostgreSQL migration (TECH-TRIG) — data migration required. MAJOR (v2.0.0).
- Multi-tenant tenant_id (TECH-TRIG) — schema-wide backfill. MAJOR (v2.0.0).
- OIDC federation (CUST-SPEC) — depends on whether it's additive (minor) or replaces sessions (major). Likely MAJOR.
- Heavier framework (TECH-TRIG) — D10 violation. MAJOR (v2.0.0).
- Additive Bucket 3 plugins (FreeIPA, XDMoD when pulled) — MINOR (v1.x.0).

**The first BREAKING change among the unscheduled items is what cuts v2.0.0.** Until then, we stay in v1.x and continue minor bumps.

**Why this rule (vs. looser "v2.0 when it feels like a big release"):**
- Operators trust semver. Mis-versioning is a credibility hit.
- Forces honest accounting: if Sprint F/G/H feel small individually but together represent a "v2.0 worth of value," that's still v1.7.0 by this rule. Emotional bigness is not a versioning criterion.
- Prevents pre-emptive v2.0 marketing pressure from skipping over v1.5/v1.6/v1.7. Every minor release is a discrete operator value drop.
- Makes the v2.0 announcement meaningful: when v2.0 ships, operators know there's a migration/action required.

**Why not pure semver (where breaking ANY API field is major):**
- We're pre-customer; internal evolution churn would generate too many v2.x.y.z bumps to be useful.
- Operators don't have third-party integrations into clustr's API yet at scale; the contract surface that matters most is the operator-upgrade experience (the mechanical rule above).
- We can tighten to strict semver later (post first paying integrator) without breaking the policy now.

**Reversibility:** **one-way** for any version already shipped. The policy itself is cheap to amend going forward.

**Re-decision triggers:**
- First paying integrator with a code-level dependency on clustr's API → tighten to strict semver (every breaking field change = major).
- A change lands that genuinely doesn't fit any category cleanly → amend the policy with the new category, don't shoehorn.

**Supersedes:** Implicit "v2.0 = horizon items" framing in old Sprint Z and pre-D27 sprint plan headers.

**See:** `docs/webui-sprint-plan.md` Sprint F/G/H sections for application; release tags in git history for examples of past minor bumps.

---

## D29 — Sprint I Selection: Show HN Hardening (BUILD-NOW bucket exhaustion)

**Date:** 2026-04-28
**Author:** Richard (Technical Co-founder)
**Context:** Sprints F/G/H shipped v1.5.0/v1.6.0/v1.7.0 between 2026-04-27 and 2026-04-28. The D27 BUILD-NOW bucket is fully drained (7/7 items). Per founder standing rules — sprints don't stop, no headcount/revenue gating, default to BUILD on the fence — a Sprint I must dispatch immediately. The remaining D27 buckets (TECH-TRIG, CUST-SPEC) are explicitly gated behind triggers that have not fired. D16 sets a Show HN target of 2026-07-27, ~13 weeks out from today.

**Question:** What is the next sprint when the BUILD-NOW bucket is exhausted, no TECH-TRIG signal has fired, and no CUST-SPEC customer has pulled?

**Options considered:**

| Option | Verdict | Reason |
|---|---|---|
| A — Show HN prep only | Reject (incomplete) | Pure marketing/docs sprint isn't engineering output; standing rule is sprints don't stop. |
| B — Speculative TECH-TRIG (framework migration) | Reject | Direct violation of D27 Bucket 2 gates and D21/D23 framework rulings. Alpine isn't at ceiling; pre-building the abstraction means building the wrong abstraction. |
| C — Speculative CUST-SPEC (OIDC) | Reject | Direct violation of D25/D27 Bucket 3 gates. No named IdP = wrong claim shape = throwaway code. The whole point of D27 was to stop sweeping speculative customer-shaped work into committed sprints. |
| D — Cross-cutting polish | Partial-accept | Engineering output is real (perf, a11y, smoke tests, install hardening) but "polish sprint" without focus risks yak-shaving. |
| E — Pivot to other repos (tunnl/resolvr/hpc-ai-tools) | Reject | Loses 10-sprint clustr team rhythm. Cofounder isn't dispatching tunnl this turn. |
| F — Hybrid | **Accept** | A + D combined, scoped tight, with the HN-reader stranger as the unifying persona. |

**Decision:** **Sprint I = "Show HN Hardening" at v1.8.0 — hybrid of Option A (launch artifacts) and Option D (engineering polish).** The unifying persona is Persona 0: the HN reader who has never heard of clustr. Every deliverable is justified against "does this materially affect a stranger's first 30 minutes with clustr, or our ability to defend the launch in the comment thread?" The sprint is COMMITTABLE (9 deliverables I1–I9 with acceptance tests; see `webui-sprint-plan.md`), not directional.

**Why hybrid, not pure Option A:** Standing rule says sprints don't stop and the team is engineering-led. A pure docs/copy sprint underuses Dinesh and breaks rhythm. Engineering deliverables (I1 install hardening, I2 smoke fixtures, I3 a11y/perf, I4 backend perf baseline, I9 docs sweep) keep the engineering org loaded; non-engineering deliverables (I5 Jared audit, I6 Monica copy, I7 Erlich launch plan) parallelize on ICs whose work is on the critical path for D16 anyway.

**Why hybrid, not pure Option D:** "Polish sprint" without an external forcing function is the classic yak-shave. The 2026-07-27 Show HN date provides the forcing function: any polish work that doesn't measurably improve a stranger's first-touch is out of scope. I4 (perf baseline) is bounded by "document p50/p95 at 50- and 500-node simulated load, fix only what profiling justifies" — not "make it fast." This is the discipline that distinguishes a forcing-function-anchored polish sprint from yak-shaving.

**Why this doesn't violate D27:** Sprint I touches zero items in TECH-TRIG (Bucket 2) or CUST-SPEC (Bucket 3). All deliverables are either (a) D27 Bucket 1-style cheap structural improvements to what we've already shipped, or (b) launch artifacts that are not code commitments to any external contract. The conditional I8 (hosted demo spike) defaults OFF specifically because hosting an internet-exposed PXE/DHCP service during HN traffic is a security-blast-radius risk that cannot be undone in real time.

**Why I rejected the speculative-build options (B and C) directly:** D27's standing meta-rule says "default to BUILD-NOW when on the fence between BUILD-NOW and a gated bucket." Sprint I IS that BUILD-NOW default — for the work that was hiding in plain sight (institutional readiness for Show HN) and that we'd been treating as "not real engineering." Calling it Sprint I and giving it a v1.8.0 tag forces it to compete on the same plane as F/G/H did. That's the discipline. Inventing a TECH-TRIG or CUST-SPEC dispatch when neither trigger has fired is not "default to BUILD" — it's "violate the gate because we can't think of anything else to build," which D27 explicitly forecloses.

**Why this doesn't violate D16:** D16 set Show HN at 2026-07-27 with a 1-week post-v1.0 buffer. We're well past v1.0 (we're at v1.7.0). Sprint I targets ship at 2026-06-08, leaving 7 weeks of buffer instead of 1. That's slack for a re-sequence if launch artifacts surface install-time gaps that need follow-up engineering.

**Reversibility:** **cheap.** If a TECH-TRIG signal fires mid-sprint (e.g., a 500-node operator surfaces, PostgreSQL contention shows up), pause Sprint I and dispatch the trigger sprint. Sprint I deliverables are atomic per workstream; nothing is half-built waste.

**Re-decision triggers:**
- A TECH-TRIG signal fires before Sprint I ships → pause Sprint I, dispatch trigger sprint, resume Sprint I when trigger sprint ships.
- A CUST-SPEC customer pulls during Sprint I → evaluate whether their pull is more valuable than launch readiness; founder call.
- The 2026-06-08 ship target slips by more than 2 weeks → re-evaluate D16 launch date with founder; do not silently push.
- Show HN ships and produces zero adoption signal after 30 days → revisit "default to BUILD" assumption for the next bucket-exhaustion case (D27 meta-rule re-decision trigger).

**Concrete dispatch:**
- **Owners:** Dinesh (I1, I2, I3, I4, I9 engineering); Jared (I5 audit); Monica (I6 copy); Erlich (I7 launch plan); Gilfoyle (I8 only if I8 is approved mid-sprint, default skip).
- **Target tag:** v1.8.0 (additive per D28 — new CLI subcommands, smoke fixtures, doc updates, perf baseline doc; no schema change, no API contract change).
- **Cadence:** 4-6 weeks. Target ship: 2026-06-08.
- **Parallel:** Gilfoyle's task #104 (iPXE build + Lab Validation CI noise) continues in parallel; it is unblocking for I2 (smoke fixtures) but does not gate I1/I3/I4/I5/I6/I7/I9.

**Standing meta-rule preserved:** When the next bucket-exhaustion moment arrives (after Sprint I ships and BUILD-NOW is again empty), the same D27 meta-rule applies — default to BUILD over wait, find the next high-leverage Bucket 1-style work that doesn't violate Bucket 2/3 gates, and dispatch.

**Supersedes:** Nothing. Extends D27's sprint-dispatch logic to the case where the originally-enumerated BUILD-NOW bucket is empty.

**See:** `docs/webui-sprint-plan.md` Sprint I section for the 9 deliverables, owner assignments, and acceptance criteria.

---

*End of decisions. Sprint 1 has unambiguous targets. Re-decisions require a written counter-rationale routed through Richard.*
