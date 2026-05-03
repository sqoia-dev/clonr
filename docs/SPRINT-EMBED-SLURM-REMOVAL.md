# Sprint — Remove the "built-in" slurm bundle row

**Status:** Design — go decision
**Author:** Richard
**Date:** 2026-05-03
**Source:** Founder hit `DELETE /api/v1/bundles/builtin` → 409, asked "why does clustr-serverd need slurm binaries built in?"
**Verdict (TL;DR):** **Option 1 — rip it out.** Ship in the **v0.10.x line as a follow-up to Dinesh-2's bundles UI work** (likely v0.10.2). No migration needed.

---

## TL;DR for the founder

The framing in the original question (and in Jared's hand-off) overstates what "embedded" means. **There is no slurm tarball compiled into clustr-serverd.** The "built-in bundle" is three short metadata strings — `builtinSlurmVersion`, `builtinSlurmBundleVersion`, `builtinSlurmBundleSHA256` — injected via `-ldflags` at build time (`Makefile:18-24`). The handler at `internal/server/handlers/bundles.go:138-150` synthesizes a fake `Bundle{ID:"builtin", Kind:"builtin", Source:"embedded"}` row in `ListBundles` *only when no DB build matches the ldflag version*. The 140 MB number doesn't exist anywhere in the binary; the actual ~33 MB `clustr-serverd` is mostly the embedded SPA (`web/dist`) and the migrations.

The bundle tarball itself (`clustr-slurm-bundle-v24.11.4-clustr5-el9-x86_64.tar.gz`, the 575ead… SHA256 the founder saw) is a **GitHub Release artifact** fetched on demand by `clustr-serverd bundle install --from-release` (`cmd/clustr-serverd/bundle.go:148-196`). It was downloaded once on the cloner during v1.0 ship, extracted to `/var/lib/clustr/repo/el9-x86_64/`, and the `.installed-version` marker confirms it (`installed_at: 2026-04-27T23:24:48Z`). The cluster has been running off the on-disk repo, not the binary, the entire time.

So the question is much smaller than it looked. We are removing **a presentation-only synthetic row** plus **the three ldflags + Makefile lines** that feed it. There is no migration, no operator-visible behavior change beyond the one row disappearing, and zero risk to running clusters. Option 1 is the right call by a wide margin; option 2 is the wrong answer to the right question; option 3 entrenches the conceptual mess that triggered the founder's gripe.

---

## 1. Verdict — Option 1 (rip it out)

The synthetic "builtin" row exists only to answer *"what slurm version did this binary ship with?"* That question has two correct answers in the architecture we already have:

1. **`clustr-serverd version`** — the CLI subcommand that already prints `slurm bundle: v24.11.4-clustr5` and `slurm version: 24.11.4` from the same ldflags (`cmd/clustr-serverd/version_cmd.go:41-42`). Operator-facing, exact, no UI surface needed.
2. **`/var/lib/clustr/repo/el9-x86_64/.installed-version`** — the actual JSON marker for what's installed in the cluster's serving repo, updated by `bundle install` (`cmd/clustr-serverd/bundle.go:331-342`). This is what nodes consume via dnf.

The Bundles tab is supposed to be the canonical view of the slurm RPM catalog the cluster has built and is serving (`internal/server/handlers/bundles.go:19-28`). Sprint 17's architecture call ("clustr replaces OpenHPC, builds → signed RPMs → internal yum repo → nodes consume via dnf") makes `slurm_builds` the source of truth. A synthetic row that doesn't correspond to anything in that table or on disk is a category error — it's a *binary metadata fact* dressed up as a *catalog entry*.

The Sprint 30/31 "wiped scope stays wiped" rule applies cleanly: we wipe this synthetic concept, we don't replace it with an "optional baseline RPM" that re-creates the same conceptual fork in a different shape (option 2's failure mode).

### Why not option 2 (separate `clustr-server-slurm-baseline` RPM)

Option 2 sounds like a clean separation but it's solving a problem we don't have. The current "embedded" thing **does not actually carry a tarball** — there is nothing to extract into a sidecar package. What option 2 actually proposes is: "ship a 140 MB tarball alongside the server RPM so air-gapped clusters can install slurm without a build." That's a *new* feature, not a refactor of the existing one. We have **zero evidence of demand** for offline-first quickstart (no operator has asked, the founder has not asked, the project memory shows no such customer pull). Building a packaging surface and an `import-baseline` import path for a hypothetical user violates "wiped scope stays wiped" and "no headcount/revenue gating" — the right time to build it is when an operator says they need it, not pre-emptively.

If that demand ever materializes, the right response is `bundle install --from-file /path/to/tarball.tar.gz` (which already works today, `cmd/clustr-serverd/bundle.go:200-225`) plus a docs page explaining how to mirror a release tarball for air-gapped install. Zero new RPM, zero new code path.

### Why not option 3 (keep + UI hide)

Option 3 ships a label change ("Embedded — cannot be removed") and leaves the synthetic row, the conditional prepend logic, and the three ldflags in place. The founder's complaint wasn't "the tooltip is missing" — it was "why is this concept here at all?" UI-only fixes that don't address conceptual confusion are exactly the technical-debt patches that compound into rewrite-class refactors three sprints later. We caught this one before it spread; remove the cause, not the symptom.

---

## 2. Migration — no DB migration needed

### Audit findings (cloner DB, 2026-05-03)

- **`slurm_builds` table:** 1 completed build — `a0b3755c-8e38-487f-9a7e-3890184d4e89`, version `24.11.5`, artifact at `/var/lib/clustr/slurm-builds/slurm-24.11.5-x86_64.tar.gz`, 128 MB. **This is the operator-built one, not the v1.0 embed version.** The v1.0 embed (`24.11.4-clustr5`) has no `slurm_builds` row — it was installed via `bundle install` to `/var/lib/clustr/repo/el9-x86_64/` directly, bypassing the build pipeline.
- **`slurm_node_version` table:** **empty.** Zero nodes report a deployed slurm version against any build_id. The cloner's two slurm nodes (`slurm-controller`, `slurm-compute`) have no row.
- **`slurm_node_config_state` table:** both nodes have config-file rows from `slurm.conf`/`gres.conf`/etc. push ops, but **the `slurm_version` column is NULL** for every row. No node is consuming a versioned bundle from the catalog in a way the catalog tracks.

**Conclusion:** No node anywhere is recorded as "consuming the builtin." The on-disk repo (`/var/lib/clustr/repo/el9-x86_64/slurm-24.11.4-1.el9.x86_64.rpm` and 14 sibling RPMs) is what nodes actually `dnf install` from, but the catalog model treats that as out-of-band. So removing the synthetic builtin row removes **only a UI element**, with no DB row to migrate, no nodes to reassign, and no on-disk repo content to touch.

### What changes for existing v0.10.1 deployments

1. **Bundles tab in the web UI:** the "built-in" row vanishes from the list when no `slurm_builds` row exists. Operators who had not yet built their own slurm see an empty Bundles tab with an empty-state message. The intro card from v0.10.1 ("clustr-build-pipeline → signed RPMs → internal yum repo → dnf install on nodes") plus a "Build Slurm" CTA pointing at the Slurm tab is the right empty state.
2. **`GET /api/v1/bundles`:** returns `{bundles: [], total: 0}` instead of one synthetic row when no builds exist. Clients that hard-coded a check for `bundle.id == "builtin"` would break — there should be none (it was added in v0.10.1, three days ago, in the same release that fixed the divergence bug). No external API consumers.
3. **`DELETE /api/v1/bundles/builtin`:** no longer special-cased; returns `404 not_found` like any other unknown ID. Cleaner.
4. **`clustr-serverd version` CLI:** unchanged. Still prints the slurm version stamped into the binary, which is the legitimate use of those ldflags.
5. **`clustr-serverd bundle install` (no flags):** unchanged. Still uses the ldflags-injected default to fetch the matching GitHub Release tarball. **This is the only legitimate consumer of the three `builtinSlurm*` variables and we keep it.**

**No release-note migration step required.** The v0.10.2 changelog entry is one line: "Removed the synthetic `builtin` bundle row from `GET /api/v1/bundles`. The Bundles tab now shows only entries from the slurm_builds table; binary slurm metadata is available via `clustr-serverd version` and `bundle install` (unchanged)."

---

## 3. Ship slot — fold into v0.10.x as v0.10.2

**Recommendation: Dinesh-2 follow-up, ship as v0.10.2.**

Reasoning:

- **Dinesh-2 is in the bundles UI surface this week** (intro card, kind badge, delete UI all landed in v0.10.1). The empty-state design + the synthetic-row removal are the same surface; folding them prevents two web rebuilds and two changelog entries for the same UX area.
- **Sprint 31 (chassis/v0.11.0)** is the wrong slot — totally different surface (datacenter rack model), different files (`internal/db/racks.go`, `web/src/routes/datacenter.tsx`), no merge benefit.
- **Sprint 32 (image-reconcile/v0.10.3)** is also the wrong slot — image staging/reconciliation, no overlap with bundles.
- **Its own micro-sprint** is overkill — the entire change is ~40 LOC backend (delete the conditional prepend block + handler-struct fields), ~30 LOC web (empty-state component), ~8 LOC Makefile (drop the three slurm ldflags from the server build, keep them only on the binary that needs them — which is `clustr-serverd` itself, since `bundle install` lives there). Plus tests.

**Concrete task list (~half-day for Dinesh):**

1. `internal/server/handlers/bundles.go` — delete the `BuiltinSlurmVersion`/`BuiltinBundleVersion`/`BuiltinBundleSHA256` fields from `BundlesHandler`, delete the `if !builtinVersionSeen` block (lines 138-151), delete the `if id == "builtin"` block in `DeleteBundle` (lines 173-179).
2. `internal/server/server.go:978-982` — drop the three `Builtin*` field assignments from the handler construction. (Leave `s.buildInfo.SlurmVersion` etc. on `buildInfo` — `version` CLI still uses them.)
3. `web/src/routes/bundles.tsx` (or wherever the table renders) — drop the kind=`builtin` badge case from the kind column, replace the "no bundles yet" path with a CTA that links to `/slurm` and explains the build pipeline.
4. `internal/server/handlers/bundles_test.go` — drop the builtin-prepend test, add an empty-state test (no rows → empty array), add a 404 test for `DELETE /api/v1/bundles/builtin`.
5. `pkg/api/types.go` — remove the `Kind: "builtin"` enum value if it's documented; keep `"build"` only.
6. `CHANGELOG.md` — single-line v0.10.2 entry as drafted above.

**Tag `v0.10.2`** when CI is green. RPM pipeline auto-fires.

### What we explicitly do NOT touch

- **`cmd/clustr-serverd/bundle.go`** — `builtinSlurmBundleVersion` and `builtinSlurmBundleSHA256` stay. They are the legitimate default for `clustr-serverd bundle install` with no flags. The `bundle` subcommand is a fetcher/installer, not a catalog entry, and the GitHub Release URL it builds (`slurm-v24.11.4-clustr5`) is the source of truth for what the operator gets.
- **`cmd/clustr-serverd/version_cmd.go`** — keeps printing the embedded version. This is the right answer to "what did this binary ship with."
- **The Makefile ldflags** — keep all three. They feed the two consumers above.

The change is purely: stop synthesizing a fake catalog row from binary metadata. The binary metadata itself stays.

---

## 4. Anti-regression guard

The failure mode we want to prevent is "a future sprint adds a new presentation-layer concept that bypasses `slurm_builds` to surface binary or filesystem state as a Bundle." Two cheap guards:

1. **Architecture-doc note** in `docs/SPRINT-EMBED-SLURM-REMOVAL.md` (this file) and a one-paragraph add to the `BundlesHandler` doc comment in `internal/server/handlers/bundles.go`: *"The Bundles tab is the view of the `slurm_builds` table only. Binary slurm metadata is exposed via `clustr-serverd version`; on-disk repo state is exposed via `clustr-serverd bundle list`. Do not synthesize Bundle entries from any other source."*
2. **CI lint (lightweight):** add a grep-based check to the existing `check-initramfs-script-sync` Makefile pattern — fail CI if `internal/server/handlers/bundles.go` contains the string `Kind:` followed by anything other than `"build"`. One line of make, prevents the obvious regression. Format:
   ```
   check-bundles-kind-purity:
       @! grep -nE 'Kind:\s+"[^"]+"' internal/server/handlers/bundles.go | grep -v '"build"' \
         || (echo "ERROR: BundlesHandler must only emit Kind=build entries; see docs/SPRINT-EMBED-SLURM-REMOVAL.md" && exit 1)
   ```
   Wired into the `test` target. Adds ~5ms to CI. Catches the regression at PR time.

The 140-MB-binary-size check the original prompt suggested is unnecessary — **the binary never had a tarball to begin with**, so there is no failure mode where a future engineer accidentally embeds one. If someone tries, `go:embed *.tar.gz` is itself a glaring code-review signal.

---

## 5. Decision summary

| Field | Value |
|---|---|
| Verdict | Option 1 — remove the synthetic builtin bundle row |
| Why | "Embedded" is three ldflag strings, not a tarball. Synthetic catalog row violates the `slurm_builds`-as-source-of-truth model from Sprint 17. Audit shows zero nodes depend on it. |
| Ship slot | Dinesh-2 follow-up, v0.10.2 |
| Effort | ~half-day, ~80 LOC + tests |
| Migration | None. No DB row exists. No on-disk repo content touched. |
| External API break | None. Clients hard-coding `bundle.id == "builtin"` would break, but the row was only added 3 days ago in v0.10.1 — no external consumers. |
| Anti-regression | Doc note in `BundlesHandler` + a 1-line CI lint forbidding `Kind` values other than `"build"`. |
| What stays | `clustr-serverd version` (prints embedded slurm metadata), `clustr-serverd bundle install` (uses ldflag default to fetch GH release), the Makefile ldflags. |

**Confidence:** High. The change is bounded, the audit is unambiguous, the architectural justification is the same one we made in Sprint 17. Ship it.
