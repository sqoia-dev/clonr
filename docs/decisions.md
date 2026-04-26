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

*End of decisions. Sprint 1 has unambiguous targets. Re-decisions require a written counter-rationale routed through Richard.*
