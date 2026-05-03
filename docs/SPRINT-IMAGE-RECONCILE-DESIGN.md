# Image Blob Reconcile — Design

**Status:** Design — go/no-go decision pending founder review
**Author:** Richard
**Date:** 2026-05-03
**Source:** rocky10 image checksum-drift incident (Gilfoyle diagnostic, 2026-05-03); founder mandate "users are not going to deep dive for stale entries every time"
**Task IDs:** continue from current head → start at #245
**Verdict (TL;DR):** Q1 = **A** (one-shot SQL UPDATE on cloner). Q2 = startup + periodic + lazy-on-failure reconciler keyed off `metadata.json` cross-check, new statuses `corrupt` and `blob_missing`, generic `POST /reconcile` endpoint, pre-deploy guard. Q3 = ship in v0.10.3 hotfix. Sprint estimated 6–8 engineering days.

---

## Q1 — Unblock verdict: **OPTION A. UPDATE the row, ship today.**

**Do this now on cloner**, single statement, inside a transaction, with a pre-image dump for audit trail:

```sql
BEGIN;
SELECT id, name, version, checksum, size_bytes, finalized_at
  FROM base_images WHERE id = '9a9af513-...';   -- capture pre-image
UPDATE base_images
   SET checksum = 'ee1a42f8a4b7153cbc85a313ce15b0f3f02ad4b2c338e70a045c52c89c1a3aba',
       size_bytes = 1857474560
 WHERE id = '9a9af513-...'
   AND checksum = '7bc6f92a...'           -- optimistic guard against double-fix
   AND status = 'ready';
-- verify rowcount=1, then COMMIT
COMMIT;
```

Then `curl -I https://cloner:8080/api/v1/images/9a9af513-.../blob` to confirm the new `X-Clustr-Blob-SHA256` header, retry the reimage on vm201, watch it succeed, file the incident note.

### Why A and not B or C

The `ee1a42f8` value has **three independent corroborating sources**:

1. `metadata.json:tar_sha256` — written by a separate code path (image factory finalization) that the SSH fix went through correctly
2. The deploy agent's actual download hash — computed by an entirely different binary on a different host from the actual bytes on the wire
3. The `rootfs.tar.bak-pre-sshfix-20260426073641` backup file proving the swap happened on 2026-04-26 and gives us the bytes we'd recover to if we wanted to undo it

Three independent witnesses agreeing on the same SHA256 is a strictly stronger guarantee than the original `7bc6f92a` had. The DB column is the only thing that disagrees, and it disagrees because the SSH-fix script forgot to UPDATE it. There is no scenario where the bytes are corrupt and all three witnesses lie identically. The DB row is the artifact-of-record's stale label, not the artifact itself.

**B (re-import from ISO)** is wrong because:
- It loses the SSH fix embedded in the current tar
- 45–90 minutes vs. 2 minutes for an outage that is blocking the lab right now
- The architectural argument "we don't trust hand-applied fixes in production images" is correct as a *forward* policy (we should bake fixes into kickstart, not hot-patch tars), but applying it retroactively to recover here means re-doing the SSH fix on a fresh tar before vm201/vm202 can boot. Net result: same hand-applied fix, more wall time, same trust posture.

**C (re-upload through `POST /:id/blob`)** is wrong because:
- It requires a status reset to `building` first, which is itself an unauthenticated DB mutation — strictly more state-changing operations than A
- The blob endpoint will write the same bytes that are already on disk, then recompute a checksum that already matches `metadata.json`, then update the DB row to that value. That is exactly what option A does directly, with extra steps and more code paths exercised.
- Going "through the legitimate code path" is good policy when the legitimate path exists. The legitimate path here would be a `recheck-blob` endpoint that we are **about to build** (Q2). Until that ships, A is the legitimate path.

**Operator-friction cost is the tiebreaker.** A is 2 minutes and 1 SQL statement; the founder is waiting; the lab is down. Ship A.

### Audit trail requirements when applying A

- `BEGIN;` + `SELECT` of the pre-image into the operator's terminal (capture the output in the incident note)
- Optimistic-guard `WHERE checksum = '7bc6f92a...'` so a concurrent fix doesn't double-update
- `audit_log` insert recording: actor=`gilfoyle@cloner`, action=`image.checksum.repair`, object_id=`9a9af513-...`, before/after SHAs, justification="post-finalization tar swap on 2026-04-26 (SSH fix); checksum drift confirmed by metadata.json + deploy agent download + .bak file"
- File a follow-up issue referencing this design doc as the structural fix

This is operator-grade hand-repair, audited, time-boxed, and reversible (re-UPDATE back to `7bc6f92a` if the deploy still fails for any reason). The shape this should never take is "log into prod, type SQL, walk away." It must be `BEGIN/UPDATE/verify/COMMIT/audit-row`.

---

## Q2 — Auto-reconcile architecture

The class of bug is **post-finalization artifact mutation** without DB awareness. Five flavors exist on disk today; we should design the reconciler to handle all five.

### Failure matrix (refined from the prompt)

| Failure mode | DB state | On-disk state | Detection signal | Reconcile action |
|---|---|---|---|---|
| **F1. Checksum drift, metadata corroborates** (the rocky10 case) | `status=ready`, `checksum=A` | `tar=B`, `metadata.json:tar_sha256=B` | computed disk SHA ≠ DB checksum AND computed disk SHA == metadata.json | **Self-heal:** UPDATE checksum + size_bytes from metadata; `audit_log` action=`image.reconcile.healed`; emit alert (info severity) for operator visibility |
| **F2. Checksum drift, metadata agrees with DB** (tampering after finalization) | `status=ready`, `checksum=A`, `metadata.json:tar_sha256=A` | `tar=B` | computed disk SHA ≠ DB checksum AND computed disk SHA ≠ metadata.json AND DB == metadata | **Quarantine:** `status=corrupt`, `error_message="blob mutated post-finalization (sha disagrees with both DB and metadata)"`; do NOT mutate the on-disk tar; alert (warning) |
| **F3. Checksum drift, no metadata** (legacy or import) | `status=ready`, `checksum=A` | `tar=B`, no `metadata.json` or `tar_sha256` empty | computed disk SHA ≠ DB checksum AND no corroborating source | **Quarantine:** `status=corrupt`, `error_message="checksum mismatch with no corroborating metadata; manual recheck required"`; alert (warning) |
| **F4. Blob missing** | `status=ready`, `blob_path=/x/y/rootfs.tar` | file does not exist | `os.Stat(blob_path)` returns ENOENT | **Quarantine:** `status=blob_missing`, `error_message="blob file absent at <path>"`; alert (error) |
| **F5. Size mismatch** (truncated download / partial write) | `status=ready`, `size_bytes=N` | file exists, size=M, M≠N | `os.Stat(blob_path).Size() != size_bytes` AND M < N (or M > N+headroom) | **Quarantine:** `status=corrupt`, `error_message="blob size N expected, M on disk (truncation suspected)"`; alert (warning) |
| **F6. Blob path absolute drift** | `status=ready`, `blob_path=/old/abs/path` | file moved to `/new/path` | `os.Stat(blob_path)` ENOENT, but `<imageDir>/<id>/rootfs.tar` exists with valid SHA | **Self-heal:** UPDATE blob_path; audit; alert (info) |

The **load-bearing decision** is that F1 and F6 self-heal silently+loudly while F2/F3/F4/F5 quarantine. Self-heal applies only when there is independent corroborating evidence the on-disk state is the correct one. Quarantine never deletes data.

#### Why F1 self-heals safely

The discriminator is "does `metadata.json:tar_sha256` agree with the disk SHA we just computed?" Both come from physically separate writes (the metadata sidecar is written by `image.WriteMetadata` during finalization, the tar is written by the build pipeline), and an attacker or a bug that wanted to fake F1 would have to coordinate updates to two on-disk files in lockstep. In practice this happens only when an operator hand-applies a fix and re-runs the metadata writer — exactly the pattern we want to recover from automatically. Any other corruption shows up as F2 (which we **do not** auto-heal).

#### Why F2 does NOT self-heal even if both DB and metadata agree

If DB == metadata but the tar disagrees with both, the on-disk tar is the rogue. We cannot tell whether someone deliberately replaced it (legitimate hand-fix that forgot to update metadata too) or whether something corrupted it (truncated copy, bitrot, attacker). Quarantining preserves all three pieces of state and lets the operator decide whether to (a) re-finalize from disk (if the tar is the new truth) or (b) restore from a backup (if the tar is the rogue).

### Trigger surface (recommended combination)

| Trigger | Cost | Coverage | Recommendation |
|---|---|---|---|
| **Startup pass** | One-shot at boot. SHA256 over a 2GB tar is ~5s on a Nanode-class CPU. For a worst-case real cluster (50 images × 4GB avg) ≈ 50 × 10s = 8 minutes serialized, much less if parallelized 4-wide. Acceptable on cold boot; start it in a goroutine so the HTTP listener comes up immediately. | All 6 failure modes for every image | **YES, always** |
| **Periodic timer** | Background goroutine, every 6h. Same hash work amortized. | All 6 failure modes; catches drift introduced after boot (tampering, fs-level corruption, manual edits) | **YES, configurable cadence (default 6h, env `CLUSTR_RECONCILE_INTERVAL`, set to `0` to disable)** |
| **Lazy on-failure** | Triggered when a deploy agent reports `X-Clustr-Reimage-Failure: checksum-mismatch` (new client-side hint header) or when the server's blob handler detects size-on-disk vs. size_bytes drift | F1, F2, F4, F5 — the modes that surface as deploy failures | **YES, narrowly scoped to deploy-failure hint; never on plain GET (would amplify a 1-byte serve into a 5s hash)** |
| **On every blob serve** | Hash on every download = 5s × every reimage | Detects F1/F5 fastest but at unacceptable cost | **NO. Veto.** |

The combination "startup + periodic + lazy-on-deploy-failure" gives:
- Bounded worst-case detection latency = `max(boot_interval, 6h)` for silently-introduced drift
- Zero added latency on the happy-path serve hot loop
- Immediate detection on the path that matters most (a deploy is failing right now)

### Status taxonomy

Today: `building`, `interrupted`, `ready`, `archived`, `error` (per `validImageStatuses` in `internal/db/db.go:393`).

**Add two new statuses, do not shoehorn into `error`:**

| Status | Meaning | Operator action |
|---|---|---|
| `corrupt` | On-disk artifact disagrees with DB and we cannot safely auto-heal (F2, F3, F5) | Investigate; either rebuild, re-finalize, or `force-recheck` after manual repair |
| `blob_missing` | DB row exists, blob file does not (F4) | Restore from backup, re-import, or delete |

Why new statuses, not `error`+reason:

- `error` today means "build pipeline failed" — operators expect a Resume button. Conflating with "the bytes on disk are wrong" sends the operator down the wrong recovery path.
- The reimage handler can `WHERE status = 'ready'` cleanly. Adding `error_message LIKE '%checksum%'` filtering everywhere is the kind of grep-for-strings logic that breaks silently when a wording changes.
- Migration risk is low: one new SQL migration extending `validImageStatuses` and `idx_base_images_status_valid`; no data backfill needed because the new statuses are forward-only.
- The UI gets two distinct visual states (red-X "corrupt", grey-Q "blob missing") that map to different operator workflows.

`archived` and `interrupted` continue to mean what they mean today.

### Operator UX

**Images tab (Bundles surface):**
- Status pill colors: `ready`=green, `building`=blue, `interrupted`=amber, `error`=amber-with-Resume, `corrupt`=red-X, `blob_missing`=grey-Q, `archived`=neutral
- Hover tooltip on the pill: full `error_message` + last-reconcile timestamp
- Row-action menu adds three actions for non-ready images:
  - **Recheck blob** — runs the reconciler immediately for this image (the new endpoint, see below)
  - **Force re-finalize** (for `corrupt` only) — accepts the on-disk SHA as the new truth, rewrites metadata, returns to `ready`. Requires typed confirmation `re-finalize <image-id>` to acknowledge "I have inspected the on-disk tar and it is the correct content."
  - **Delete** — same as today

**Image detail page:**
- Add a "Reconcile" panel showing: last-checked timestamp, last-checked SHA (vs. DB), metadata SHA, on-disk size vs. DB size, blob_path resolution (exists/missing/path), a "Recheck now" button
- Audit log section auto-filtered to `image.reconcile.*` actions for this image

**Reimage handler:**
- Refuse to deploy a non-`ready` image with HTTP 409 and structured body `{"error":"image_not_ready","image_status":"corrupt","detail":"..."}`. Better than today's "checksum mismatch" failing mid-deploy after the agent has already downloaded 2GB.

### Endpoint shape

**Replace BUG-19's narrow `recheck-blob` with a general `reconcile` endpoint.** The narrow shape would be one endpoint per failure class (recheck-blob, recheck-size, recheck-path), which scales badly. A single endpoint that runs the full reconcile pass and returns a structured outcome scales naturally as we add F-cases.

```
POST /api/v1/images/:id/reconcile
  Body (optional): {"force_re_finalize": false}
  Auth: admin scope (mutates DB and on-disk metadata in the re-finalize case)

  Response 200 OK:
  {
    "image_id": "9a9af513-...",
    "outcome": "healed" | "ok" | "quarantined" | "blob_missing" | "no_change",
    "previous_status": "ready",
    "new_status": "ready" | "corrupt" | "blob_missing",
    "checks": {
      "blob_exists": true,
      "size_on_disk": 1857474560,
      "size_in_db": 1857474560,
      "sha_on_disk": "ee1a42f8...",
      "sha_in_db": "ee1a42f8...",
      "sha_in_metadata": "ee1a42f8...",
      "blob_path_resolution": "found_at_db_path" | "found_at_default_layout" | "not_found"
    },
    "actions_taken": ["updated_checksum", "updated_size_bytes"],
    "audit_id": "ulid..."
  }

  Response 409 (force_re_finalize requested but image is in healthy state):
  Response 404 (image not found):
  Response 500 (I/O failure during hash):
```

Same handler is invoked by:
- The startup pass (in-process call, no HTTP)
- The periodic timer (in-process)
- The UI "Recheck now" button (HTTP)
- The reimage handler's pre-deploy guard (in-process)

One implementation, four call sites. The HTTP shape is the operator-facing surface; the underlying `func (s *Server) ReconcileImage(ctx, imageID, opts) (*ReconcileResult, error)` is what the rest of the daemon calls.

### Pre-deploy guard

**Yes, the reimage handler should call `ReconcileImage` before accepting a request.** Argument:

- Cost: hashing a 2GB tar is ~5s. A reimage is itself a 60–120s operation. A 5s pre-check that prevents a failed 60s deploy + a 30s rollback + an operator investigation is cheap.
- Skipping the hash on the hot path: the reconciler can be cached. After a successful reconcile within `CLUSTR_RECONCILE_TTL` (default 1h), skip the hash and use the cached result. So a cluster doing back-to-back reimages eats one hash per hour per image, not one per deploy.
- Failure mode if we don't guard: the rocky10 incident, every time the DB drifts. The deploy agent downloads 2GB, computes the SHA, fails, retries (often), the operator gets paged.

Implementation: reimage handler calls `s.ReconcileImage(ctx, imageID, ReconcileOpts{CacheTTL: cfg.ReconcileTTL, FailOnQuarantine: true})`. If outcome is `ok` or `healed`, proceed. If `quarantined` or `blob_missing`, return 409 to the deploy agent with the structured body above. The deploy agent surfaces this to the operator immediately, not after a 2GB download.

### Generalization: per-type code, shared scaffolding

Three artifact-bearing types in clustr today: base images, initramfs builds, bundle RPMs (yum repo). A fourth (#146 disk layouts) is on the roadmap. The temptation is to define a `Reconcilable` interface; my opinion is **don't do that yet**.

Reasons:
- The three types' on-disk layouts and corroborating-evidence sources are different enough that a generic interface degenerates into `func(ctx) error` with all the actual logic in per-type implementations. The interface adds a layer of indirection without saving lines.
- BUG-15's `ReconcileStuckInitramfsBuilds` is already the right shape for initramfs and lives in `internal/server/reconcile.go`. Add `ReconcileImageBlobs(ctx)` there, alongside it. Two functions, parallel structure, easy to read top-to-bottom.
- The bundle-RPM yum-repo case (artifacts under `/var/lib/clustr/repos/`) has a *different* failure profile: dnf's `repodata/repomd.xml` is the manifest, signed by clustr's GPG key. The reconciler there is "verify the signed manifest matches reality, regenerate it if not." That's structurally a different operation from "hash the tar and compare to the DB column."

What we should standardize:
- A shared `pkg/reconcile/result.go` with the `ReconcileResult` and `Outcome` types so the audit log, the API response, and the UI all use the same vocabulary
- A shared `pkg/reconcile/audit.go` helper to insert a uniform `image.reconcile.*` / `initramfs.reconcile.*` / `bundle.reconcile.*` audit row
- A shared periodic-timer dispatcher in `internal/server/server.go` that calls each typed reconciler on its own cadence

If by sprint 33 we have four typed reconcilers and they really do share more than the result type, factor the interface then. Premature unification today buys nothing.

---

## Q3 — Where this ships: **v0.10.3 hotfix.**

Three sequencing options were on the table:

- **v0.10.3 hotfix** — ships as a focused release within the v0.10.x train. Founder mandate is explicit and time-sensitive ("users are not going to deep dive every time"). The implementation is bounded (one new endpoint, one new package, one migration, one UI panel). It does not block on anything in flight. **RECOMMENDED.**
- **v0.11.0** — folding into a larger sprint dilutes urgency and pushes the user-visible fix behind chassis-support work. Founder mandate disagrees.
- **Sprint 30 amendment** — ARCH-1 split-daemon work is its own large refactor with its own risk envelope. Adding reconciler hardening as a "related-but-not-blocking" workstream sounds clean but in practice means the reconciler ships on the split-daemon timeline, not its own. Wrong tradeoff.

The reconciler is also independent of the split-daemon work: it lives in the control plane in either architecture (DB writes go there), and migrating it to the post-split shape is a 1-day refactor in Sprint 30. We don't owe Sprint 30 anything by shipping it first.

**Cadence implication:** v0.10.3 ships before v0.11.0. Dinesh-1 is finishing v0.10.0; v0.10.1 and v0.10.2 are in flight. Add v0.10.3 to the train. The release cadence is "fast" (5 min autodeploy on cloner), not "slow per-version" — landing this in v0.10.3 doesn't slow anything down structurally, it just means one more version tag.

---

## Sprint 31 — Image Blob Reconcile

**Theme:** auto-detection and self-heal of post-finalization artifact drift; quarantine of unrecoverable cases; pre-deploy guard. Founder mandate: operator never has to deep-dive for stale entries again.
**Estimated duration:** 6–8 engineering days
**Owner mix:** Dinesh (code + UI), Gilfoyle (cloner soak + audit verification), Richard (review)
**Numbering:** continues from #244 → starts at #245

### Tasks

- [ ] **#245 — Migration: add `corrupt` and `blob_missing` statuses (HIGH, S, ~0.5d)**
  Owner: Dinesh.
  In: new `internal/db/migrations/NNN_image_reconcile_statuses.sql` extending the partial-index allowlist. Update `validImageStatuses` map in `internal/db/db.go:393`. Add `ImageStatusCorrupt` and `ImageStatusBlobMissing` constants in `pkg/api/types.go:24`. No data backfill (forward-only).
  Depends on: nothing.
  DoD: migration applies cleanly on cloner's existing DB; `UpdateBaseImageStatus` accepts and rejects the right values; unit test for both.

- [ ] **#246 — `ReconcileResult` types in `pkg/reconcile/` (HIGH, S, ~0.5d)**
  Owner: Dinesh + Richard review.
  In: new package `pkg/reconcile/` with `Outcome`, `ReconcileResult`, `Checks`, `ReconcileOpts` types. Public, used by both the HTTP layer and internal callers. Copy the JSON schema from §"Endpoint shape" above.
  Depends on: nothing.
  DoD: types defined; godoc on every field; reused in #247 and #251.

- [ ] **#247 — `ReconcileImage` core function (HIGH, M, ~1.5d)**
  Owner: Dinesh.
  In: new file `internal/server/reconcile_image.go` with `func (s *Server) ReconcileImage(ctx, imageID, opts) (*pkg/reconcile.Result, error)` implementing the full F1–F6 matrix. Uses `image.ReadMetadata` for the corroboration source. Hashes via `io.Copy(sha256.New(), f)` like the existing initramfs reconciler. Cache-by-imageID for `opts.CacheTTL`. Writes audit log via `internal/db/audit` for any state change.
  Depends on: #245, #246.
  DoD: unit tests for each row in the F1–F6 matrix using a tmpdir + sqlite + synthetic tar; -race clean; no flakes over 100 runs.

- [ ] **#248 — Reimage handler pre-deploy guard (HIGH, S, ~0.5d)**
  Owner: Dinesh.
  In: in `internal/server/handlers/reimage.go` (or wherever the reimage entry lives), call `ReconcileImage` before accepting the request. On `quarantined` or `blob_missing`, return HTTP 409 with the structured error body. Use `cfg.ReconcileTTL` (new env `CLUSTR_RECONCILE_TTL`, default 1h) for the cache.
  Depends on: #247.
  DoD: integration test: drift the DB checksum, request a reimage, assert 409 with the right error body and the reconciler runs once.

- [ ] **#249 — Startup reconcile pass (HIGH, S, ~0.5d)**
  Owner: Dinesh.
  In: extend `cmd/clustr-serverd/main.go:441` block (where ReconcileStuckBuilds is called) to also call `ReconcileAllImages(ctx)`. Run in a background goroutine — do NOT block the listener. Log a single info line per image processed; aggregate summary at end ("reconciled N images, healed M, quarantined Q, blob-missing B").
  Depends on: #247.
  DoD: cloner restart logs the reconcile summary; HTTP listener comes up in <1s regardless of image count.

- [ ] **#250 — Periodic reconcile timer (HIGH, S, ~0.5d)**
  Owner: Dinesh.
  In: ticker goroutine in `internal/server/server.go` Start path, runs `ReconcileAllImages` every `CLUSTR_RECONCILE_INTERVAL` (default 6h, `0` = disabled). Skip if a previous pass is still running (no overlapping). Emit alert (info severity) on first quarantine of any image.
  Depends on: #247.
  DoD: integration test with `CLUSTR_RECONCILE_INTERVAL=2s`: timer fires, drift introduced between ticks is detected within one interval, no overlap.

- [ ] **#251 — `POST /api/v1/images/:id/reconcile` endpoint (HIGH, M, ~1d)**
  Owner: Dinesh.
  In: new handler in `internal/server/handlers/images.go`, admin-scope only. Calls `ReconcileImage` with `ReconcileOpts{CacheTTL: 0, ForceReFinalize: body.force_re_finalize}`. Returns the JSON shape from §"Endpoint shape." Force-re-finalize path: accepts the on-disk SHA as truth, calls `image.WriteMetadata` to rewrite the sidecar with the disk SHA, sets `status=ready`, audit-logs `image.re_finalize.forced`.
  Depends on: #247.
  DoD: handler test for all 5 outcome variants + force-re-finalize success + force-re-finalize-on-healthy-image-rejected; admin-scope auth verified.

- [ ] **#252 — UI: status pills + Reconcile panel + Recheck button (MEDIUM, M, ~1.5d)**
  Owner: Dinesh.
  In: extend `web/src/routes/images.tsx` with the two new status colors. Add a "Reconcile" expandable panel on the image detail row showing the `Checks` block. Wire a "Recheck now" button to `POST /reconcile`. Wire a "Force re-finalize" button (only visible when `status=corrupt`) with typed-confirmation modal. Empty-state copy explains what each status means.
  Depends on: #251.
  DoD: every state visually distinct; tested with `web/src/test/...` against the API mock; keyboard-accessible.

- [ ] **#253 — `clustr image reconcile <id>` CLI command (MEDIUM, S, ~0.5d)**
  Owner: Dinesh.
  In: extend `cmd/clustr/` with `image reconcile` subcommand. Output the `ReconcileResult` as a human table by default, JSON with `-o json`. Support `--force-re-finalize`. Wire to `POST /reconcile`.
  Depends on: #251.
  DoD: `clustr image reconcile 9a9af513` works end-to-end against cloner; help text matches existing CLI style.

- [ ] **#254 — Audit log helper + grep recipes (MEDIUM, S, ~0.25d)**
  Owner: Dinesh.
  In: ensure all reconciler state changes write `audit_log` rows with action prefix `image.reconcile.` so `clustr audit search --action image.reconcile` works. Document the audit grep recipes in `docs/RUNBOOK-IMAGE-RECONCILE.md`.
  Depends on: #247.
  DoD: cloner soak shows audit rows for every healed/quarantined image; runbook reviewed by Gilfoyle.

- [ ] **#255 — End-to-end soak on cloner (HIGH, M, ~1d wall, mostly waiting)**
  Owner: Gilfoyle + Dinesh.
  In: deploy v0.10.3 to cloner. Synthesize each F1–F6 case on a throwaway test image: F1 by re-running the rocky10 fix on a different image, F2 by `dd if=/dev/urandom of=rootfs.tar` after finalization, F3 by deleting metadata.json, F4 by `rm rootfs.tar`, F5 by truncating, F6 by moving to a sibling dir. For each: confirm reconciler detects within one interval, applies the right action, and the UI shows the right state. Confirm the rocky10 case auto-heals on the next startup pass (dropping the manual SQL UPDATE applied for Q1).
  Depends on: #245–#254.
  DoD: 6/6 scenarios pass; soak log filed in `docs/VALIDATION-RECONCILE-2026-05-XX.md`.

- [ ] **#256 — v0.10.3 release (HIGH, S, ~0.5d)**
  Owner: Dinesh + Richard sign-off.
  In: CHANGELOG entry, version bump, tag, RPM build via existing release workflow. Release notes call out the founder mandate, the rocky10 incident as the catalyst, and the operator-facing recovery path for legacy `corrupt` images.
  Depends on: #255.
  DoD: tag pushed, GitHub release published, RPM in the EL9/EL10 repos, CI green on release SHA.

### Sprint 31 ship gate

- All 12 tasks landed. CI green on `main`.
- Cloner running v0.10.3 for ≥48 hours with the 6h periodic reconciler firing at least 8 times.
- Zero false-positive quarantines (no ready image incorrectly marked corrupt) across the soak.
- The rocky10 image (`9a9af513`) survives a deliberate revert of the Q1 SQL UPDATE → reconciler heals it on next startup → reimage of vm201 succeeds end-to-end without operator intervention.
- Operator runbook for "image quarantined" tested by someone who is not Dinesh or Richard (Jared or Gilfoyle).

### Out of scope for Sprint 31

- Initramfs build reconciler hardening (already covered by BUG-15)
- Bundle RPM yum-repo reconciler (different signal, different recovery; defer to Sprint 33)
- Disk-layout reconciler (#146 is its own design)
- Generic `Reconcilable` interface (premature; revisit at Sprint 33 if 4 typed reconcilers exist)
- Multi-host blob reconciliation (only relevant post-split, post-remote-builder)

---

## Risks and what could go wrong

| Risk | Likelihood | Severity | Mitigation |
|---|---|---|---|
| Periodic reconciler hashes 50×4GB images and saturates disk I/O during business hours | MEDIUM | LOW | Default 6h cadence; sequential not parallel; `nice` + `ionice` if measured impact; configurable via env |
| Startup pass blocks listener for minutes on a cluster with many large images | LOW | MEDIUM | Run in goroutine, never block listener; log progress; doctor surfaces "reconcile in progress" |
| False-positive quarantine corrupts a healthy image's record | LOW | HIGH | Quarantine is non-destructive (no on-disk delete); operator can `force-re-finalize` to clear; soak in #255 specifically tests false-positive rate |
| Cache TTL too long: stale healthy result hides a real corruption | MEDIUM | LOW | Default 1h is short enough that the periodic timer (6h) catches it within one cycle; lazy-on-failure trigger catches deploy failures immediately |
| Force-re-finalize accepts a corrupted tar as new truth | MEDIUM | HIGH | Typed confirmation `re-finalize <image-id>` required; audit log captures actor + timestamp; runbook says "inspect the tar with `tar tzf | head` first"; future: cryptographic signature requirement |
| Concurrent reconciler runs on same image (startup race with periodic) | MEDIUM | LOW | Per-image mutex in `ReconcileImage`; second caller waits then returns cached result |
| metadata.json itself drifts after a hand-edit, both sources lie consistently | LOW | MEDIUM | This is the F2 case where the disk tar is the rogue and metadata+DB agree; reconciler quarantines correctly. The inverse (DB drifts to match a corrupted metadata) cannot happen because the DB is only ever written by `UpdateBaseImageStatusReady` (finalization) or by the reconciler (which uses the disk hash, not metadata directly) |
| Adding two new statuses breaks downstream code that exhaustively switches on `ImageStatus` | MEDIUM | LOW | Grep for `case api.ImageStatusReady:` patterns in #245 and either add cases or use `default:` semantics; CI lint catches missed switches if `exhaustive` linter is enabled |
| Pre-deploy guard adds 5s to every reimage when cache cold | HIGH | LOW | Cache TTL 1h amortizes across back-to-back deploys; first deploy after a config change pays the cost once; operator-facing latency is unchanged in steady state |
| BUG-19 was filed expecting a narrow `recheck-blob` endpoint; we ship `reconcile` instead | HIGH | LOW | Update BUG-19 with the design decision; the new endpoint is a strict superset; CLI alias `clustr image recheck-blob` for muscle memory if needed |

---

## Recommendation

**Q1: Apply Option A on cloner today, with the audit-row guard rails above.** Lab unblocks in 2 minutes. The corroborating evidence is overwhelming.

**Q2: Build the reconciler as designed.** The F1–F6 matrix covers the rocky10 case and its 5 sibling failure modes. Two new statuses (`corrupt`, `blob_missing`) cleanly separate "the bytes are wrong" from "the build pipeline failed." A single `POST /reconcile` endpoint serves UI, CLI, and internal callers; pre-deploy guard prevents the failed-mid-deploy incident class. Per-type reconcilers, no premature interface.

**Q3: Ship in v0.10.3.** Hotfix-cadence release. 6–8 engineering days. The founder mandate is time-sensitive enough that folding this into v0.11.0 or Sprint 30 dilutes the signal.

The rocky10 incident is the cheap version of this bug — a single image, a single operator who knows where to look. The expensive version is a customer with 30 images, a quarterly OS-update cycle that hand-patches three of them, and a 200-node multicast reimage that fails at node 47 because a checksum drifted six weeks ago and nobody noticed. The reconciler costs 6–8 days and prevents that entire class.

— Richard
