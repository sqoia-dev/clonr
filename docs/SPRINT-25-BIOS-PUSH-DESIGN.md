# Sprint 25 #159 — BIOS settings push (Intel first)

**Author:** Richard
**Date:** 2026-05-01
**Status:** Design — implementation owner Dinesh
**Source plan:** `docs/CLUSTERVISOR-GAP-SPRINT.md` Sprint 25 (renumbered #159 from #147)

---

## Goal

Push desired BIOS settings (boot order, hyperthreading, power policy, virtualization, secure boot, etc.) onto Intel Xeon servers so that operators stop chasing per-node BIOS drift by hand. The first provider wraps Intel's `intel-syscfg` (a.k.a. SYSCFG) utility. The interface is vendor-agnostic so Dell `racadm` and Supermicro `sum` can drop in later (already in the post-Sprint-25 backlog as #157/#158-original).

**Out of scope (explicit):** Dell, Supermicro, AMI Megarac, OpenBMC settings, BIOS firmware *flashing* (that's a separate vendor-specific story). No BIOS settings are preserved across `clustr reset` (wiped scope stays wiped).

---

## Architectural decisions

### D1. Apply phase — pre-boot inside initramfs, NOT post-boot via clientd

**Decision: BIOS apply runs in initramfs, before partitioning, on every deploy where the node has an assigned profile and current settings differ.**

Rationale, decisive factor first:

- Many BIOS settings (HT, IOMMU, NUMA topology, persistent memory mode, virtualization extensions, boot order) **only take effect after the next firmware POST**. Applying post-boot via clientd means "the change you made today silently doesn't apply until the operator reboots the node a second time." That's a footgun and exactly the class of confusion clustr exists to eliminate.
- The deploy already requires a reboot afterward (we're imaging the disk). Applying BIOS in initramfs means one reboot covers both the BIOS change and the OS install — no extra cycle.
- Initramfs is already root, no extra privilege story. Post-boot would need a privhelper hop.
- Drift detection (the post-boot use case) can still be served: clientd can read current settings every 24h with `intel-syscfg /s` and compare against the assigned profile, raising an alert via the #133 alert engine. Reading is cheap; only **apply** is restricted to initramfs.

The cost of this choice is that an operator who only wants to change BIOS settings (without reimaging) has to trigger a "BIOS-only deploy" — a reboot into initramfs, apply, reboot to disk, no image touched. We add this as a CLI affordance in commit 3:

```
clustr bios apply <node-selector>      # reboots target nodes through initramfs to apply
```

This routes through the existing reimage path with a `bios_only=true` flag in `reimage_requests`. Initramfs detects the flag, applies BIOS, **skips** the disk write, reboots. Adds a new `NodeStateBiosApplying` state.

### D2. `Provider` interface

Located at `internal/bios/provider.go`. Minimal but extensible:

```go
package bios

type Setting struct {
    Name  string `json:"name"`   // e.g. "Intel(R) Hyper-Threading Technology"
    Value string `json:"value"`  // opaque to clustr; vendor-defined
}

type Change struct {
    Setting
    From string `json:"from"`    // current value
    To   string `json:"to"`      // desired value
}

type Provider interface {
    // Vendor returns the vendor identifier ("intel", "dell", "supermicro").
    Vendor() string

    // ReadCurrent returns all currently-set BIOS values readable by the
    // vendor binary on this node. Called from initramfs and from clientd.
    ReadCurrent(ctx context.Context) ([]Setting, error)

    // Diff computes the change set needed to bring current → desired.
    // Settings present in current but not in desired are left alone (not
    // reset) — desired is treated as a partial override, not a full image.
    Diff(desired, current []Setting) ([]Change, error)

    // Apply writes the change set. Caller has already authenticated via
    // privhelper if running outside initramfs. Returns the changes that
    // were actually applied (vendor binary may report some as already-set,
    // some as deferred-until-reboot, etc.).
    Apply(ctx context.Context, changes []Change) ([]Change, error)

    // SupportedSettings returns vendor-known setting names. Used by the
    // server to validate profile JSON at create-time. Optional — empty
    // slice means "accept anything; let Apply fail at deploy if invalid."
    SupportedSettings(ctx context.Context) ([]string, error)
}
```

Registry pattern: `internal/bios/registry.go` exposes `Register(Provider)` and `Lookup(vendor) Provider`. The Intel provider self-registers from `init()`. Adding Dell is one new file under `internal/bios/dell/` plus its `Register` call — no changes to the interface, no changes to the registry, no changes to deploy logic. That is the extensibility test (D7 below).

### D3. Profile vs override — auto-apply on next deploy

`bios_profiles` is the named, reusable setting set. `node_bios_profile` binds a node to a profile.

**Lifecycle:**
- Profile changes (profile JSON edited) → bumps `bios_profiles.updated_at`, no nodes touched yet.
- On next deploy of a node whose `node_bios_profile.profile_id` is non-NULL: initramfs reads the profile, diffs against `intel-syscfg /s`, applies if non-empty diff, records `last_applied_at = now` and `applied_settings_hash = sha256(profile_json)`.
- Operator-triggered re-apply without reimage: `clustr bios apply <selector>` (D1).
- Operator detaches node from profile (sets `profile_id = NULL`): no settings are reverted; node simply stops being managed. Wiped scope stays wiped.

This is the ClusterVisor pattern (their config-template-attach-to-node model) but constrained: clustr does not maintain an "expected baseline" beyond the explicitly-assigned profile. We do not silently revert settings the profile doesn't mention.

### D4. Schema (migration NNN — see "migration number" note below)

**Migration number:** highest committed at this design's HEAD is `091_pending_changes.sql`. Parallel streams (#157 UDPCast, #160 boot_entries) are also adding migrations this sprint. Pick the next free integer at commit time; `git pull --rebase` immediately before pushing commit 1.

```sql
-- migration NNN: BIOS profiles and per-node binding (#159)

CREATE TABLE bios_profiles (
    id              TEXT    PRIMARY KEY,            -- UUIDv4
    name            TEXT    NOT NULL UNIQUE,
    vendor          TEXT    NOT NULL,               -- "intel" in v1
    settings_json   TEXT    NOT NULL,               -- JSON object {name: value, ...}
    description     TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX idx_bios_profiles_vendor ON bios_profiles(vendor);

CREATE TABLE node_bios_profile (
    node_id                 TEXT    PRIMARY KEY,    -- one profile per node, for now
    profile_id              TEXT    NOT NULL REFERENCES bios_profiles(id),
    last_applied_at         INTEGER,                -- unix seconds, NULL until first apply
    applied_settings_hash   TEXT,                   -- sha256(profile.settings_json) at last apply
    last_apply_error        TEXT                    -- non-empty on most recent failure
);
```

`pkg/api/types.go` gets `BiosProfile` and `NodeBiosProfile` types matching the SQL shape.

### D5. `settings_json` shape and validation

**Decision: opaque JSON object, vendor-syntax keys, no clustr-side schema.**

```json
{
  "Intel(R) Hyper-Threading Technology": "Disable",
  "Power Performance Tuning":             "OS Controls EPB",
  "Energy Efficient Turbo":               "Disable",
  "PCIe ASPM Support":                    "Disable"
}
```

Reasons:
- Intel SYSCFG has hundreds of settings, varies by motherboard SKU and firmware version. Modelling them in clustr is a forever-job we'd never finish.
- The operator's mental model already lives in vendor docs and existing setting-export files. We let them paste those values directly.
- Validation: at profile-create time, if `Provider.SupportedSettings()` returns a non-empty list (Intel's does, by parsing `intel-syscfg /d` output), reject keys not in that list with a clear error. If the binary isn't available on the server (operator hasn't dropped it yet), the validation step is skipped with a warning; failures surface at deploy time.

The price we pay is "garbage profile JSON deploys to a node and fails at apply time." Acceptable: the failure is per-node, recorded in `last_apply_error`, doesn't brick the node (BIOS settings the binary rejects are simply not applied — POST still succeeds).

### D6. Intel binary licensing — operator-supplied, NOT redistributed

Intel SYSCFG is distributed under the **Intel End User License Agreement For Developer Tools**, which prohibits redistribution as part of a third-party product. Clustr is open source (MIT) and we cannot bundle the Intel binary in our RPM or in initramfs without violating Intel's EULA.

**The build does not redistribute `intel-syscfg`. The operator supplies it.**

Operator workflow:
1. Operator downloads `Save_and_Restore_System_Configuration_Utility_(SYSCFG)_for_Linux_x.x.x.zip` from Intel's site (free with Intel registration).
2. Drops `syscfg` (the binary) at `/var/lib/clustr/vendor-bios/intel/syscfg` on the clustr server.
3. `clustr bios provider verify intel` runs `syscfg /h` to confirm the drop, then registers the path.
4. On the next initramfs build (`scripts/build-initramfs.sh`), the build script checks for the file and bundles it if present. Builds without the file produce an initramfs that fails BIOS apply for Intel nodes with a clear error pointing to the operator workflow above.

Documented in a new file `docs/BIOS-INTEL-SETUP.md` (one page, written in commit 1). The build script log line is the single source of truth: `bios: intel binary present, bundling /var/lib/clustr/vendor-bios/intel/syscfg → initramfs:/usr/local/bin/intel-syscfg` or `bios: intel binary absent, intel BIOS apply will fail on nodes with Intel profiles`.

The same pattern generalizes to Dell `racadm` and Supermicro `sum` when those providers land.

### D7. privhelper — new verb `bios-apply` (used only by the drift-detect path)

Inside initramfs we are already root and don't need privhelper. The new verb exists for the **post-boot drift detection** read path on running nodes (clientd → privhelper) and as the call shape if we ever add an in-OS apply mode.

Verb: `bios-apply <vendor> <profile-blob-path>`

Validation in `cmd/clustr-privhelper/main.go`:
- `vendor` must match `^[a-z]+$` and be in the embedded allowlist `{intel, dell, supermicro}` (additions require a clustr-privhelper rebuild — intentional; vendors are infrequent).
- `profile-blob-path` must be a regular file under `/var/lib/clustr/bios-staging/` and end in `.json` (single-purpose staging dir, mode 0700 root). Caller (clientd) writes the profile JSON there with `os.WriteFile(path, blob, 0600)` first.
- helper rebuilds argv: `/usr/local/bin/intel-syscfg /r <staging-path>` (for the Intel provider), captures stdout/stderr, audits to `audit_log` per existing pattern.

A second verb `bios-read <vendor>` returns current settings as JSON on stdout (`intel-syscfg /s -` → stdout). Used by both apply (to compute diff) and the future periodic drift checker.

This is the standing-rule pattern: verbs validated, argv rebuilt server-side, audit_log row per call. No new sudoers entries.

### D8. Provider extensibility — Dell drops in without refactoring

The Dell provider (#157-backlog) is purely additive:

1. New file `internal/bios/dell/dell.go` implementing `Provider`. Wraps `racadm bios get`/`bios set` instead of `intel-syscfg`.
2. `init()` calls `bios.Register(&dellProvider{})`.
3. New entry in `cmd/clustr-privhelper/main.go` allowlist: `dell`.
4. New entry in `scripts/build-initramfs.sh` operator-supplied-binary path: `/var/lib/clustr/vendor-bios/dell/racadm`.
5. UI affordance: profile-create dropdown gains a `dell` option once the operator's registered the binary.

No interface changes. No deploy-flow changes. No DB schema changes (vendor is already a column).

That extensibility is what makes the #147 doc say "vendor-pluggable" rather than "Intel-only with a future refactor." Verifying it now is the point of the registry pattern in D2 — the Intel provider IS the test case.

---

## Tests

| Layer | Test type | Notes |
|---|---|---|
| `Provider.Diff()` | Unit, table-driven | Known input/output pairs. Settings missing from desired → no change. Identical values → empty change set. Case-insensitive setting names per Intel convention. |
| `Provider.Apply()` Intel binary fork | Unit with `exec.Cmd` mocked | The existing `internal/deploy/testutil` mock-cmd pattern. Stub `intel-syscfg /r` returns canned stdout, helper captures and parses. |
| privhelper `bios-apply` verb | Unit | Argv validation, allowlist, audit row written. Mock the inner exec. |
| Profile CRUD endpoints | `httptest` | Standard server handler tests. |
| Apply-during-deploy initramfs | E2E | Defer until founder has a real Intel server. v1 ships with the unit + httptest layer; the E2E gate is documented in SPRINT.md as a known follow-up tied to #124 lab validation. |
| qemu fake-binary integration | Integration | Ship a small Go binary `test/bios/fake-syscfg/main.go` that mimics `intel-syscfg /s` and `/r` over canned settings. Initramfs build picks it up when `CLUSTR_BIOS_FAKE=1` is set. Lets us cover the deploy → initramfs → apply path without an Intel server. Behind `-tags bios_fake`. |

The fake-syscfg binary is the meaningful gap-filler. It runs in the existing qemu deploy test harness and exercises the diff → apply → record-result path end to end. Real Intel hardware testing is gated on real hardware.

---

## Phasing for Dinesh

Three commits.

### Commit 1 — Provider interface + Intel stub + DB schema + docs

- Migration `NNN_bios_profiles.sql` (D4). Pick the next free integer at commit time.
- `internal/bios/provider.go` interface (D2).
- `internal/bios/registry.go`.
- `internal/bios/intel/intel.go` — full `Provider` implementation, but with `Apply` and `ReadCurrent` calling out to a binary path resolved at runtime; if binary absent returns a typed `ErrBinaryMissing` error.
- `pkg/api/types.go` adds `BiosProfile`, `NodeBiosProfile`.
- `internal/db/bios_profiles.go` — CRUD on the new tables.
- `internal/server/handlers/bios.go` — `POST/GET/PATCH/DELETE /api/v1/bios-profiles`, `POST /api/v1/nodes/{id}/bios-profile` (assign), `DELETE /api/v1/nodes/{id}/bios-profile` (detach), `GET /api/v1/nodes/{id}/bios/current` (live read via clientd → privhelper).
- `docs/BIOS-INTEL-SETUP.md` — operator's binary-drop instructions.
- Unit tests for `Diff`, registry, CRUD.

### Commit 2 — privhelper plumbing + clientd integration

- `cmd/clustr-privhelper/main.go` adds verbs `bios-apply` and `bios-read` (D7) with allowlist + audit.
- `internal/privhelper/privhelper.go` Go API: `BiosApply(ctx, vendor, profilePath) error`, `BiosRead(ctx, vendor) ([]Setting, error)`.
- `internal/clientd/operator_bios.go` — node-side handlers for new server messages `bios_read_request` and `bios_apply_request` (the latter only used in the post-boot apply path which we explicitly aren't enabling in v1, but the message handler exists so D1 can be revisited if needed).
- `internal/clientd/messages.go` — new payload types.
- Periodic drift check: every 24h, clientd calls `BiosRead`, compares hash against `applied_settings_hash`, posts `bios_drift` event if mismatch. Drift is reported, not auto-corrected — operator must trigger reapply.

### Commit 3 — Initramfs apply phase + CLI + UI + tests

- `scripts/build-initramfs.sh` — operator-supplied-binary detection (D6) and bundle.
- `cmd/clustr/main.go runAutoDeployMode` — new phase between hardware discovery and partitioning: read `node_bios_profile`, load provider, diff, apply if non-empty diff. On `ErrBinaryMissing`, fail-loud with the operator-runbook URL in the deploy log.
- `cmd/clustr/bios.go` — new subcommand tree: `clustr bios profiles {list,create,show,delete}`, `clustr bios assign <node-selector> <profile-name>`, `clustr bios detach <node-selector>`, `clustr bios apply <node-selector>` (D1), `clustr bios provider verify <vendor>`.
- `bios_only=true` reimage flag wired through `internal/server/reimage_workers.go` and the deploy phase skip.
- New `NodeStateBiosApplying` state.
- Web UI: BIOS Profiles tab on Settings; per-node BIOS Profile section in the node Sheet (assign / detach / view current). Lands behind a feature flag `bios.ui_enabled` in `server_config` so UI ships dark if any deploy-side regression sneaks through.
- Fake-syscfg binary + `-tags bios_fake` integration test (D — Tests).

---

## Privilege boundary check

| Operation | Where it runs | Privilege path |
|---|---|---|
| `intel-syscfg /s` (read settings) inside initramfs | initramfs (root) | Direct exec, no privhelper |
| `intel-syscfg /r profile.json` (apply) inside initramfs | initramfs (root) | Direct exec, no privhelper |
| `intel-syscfg /s` from running OS (drift check) | clientd (root, on node) | Direct exec — clientd is already root on node |
| `intel-syscfg /r` from running OS (post-boot apply, future) | clientd → privhelper | New verb `bios-apply`, allowlist + audit |
| Settings read from clustr-serverd UI/CLI | serverd (unprivileged) → clientd → exec | clientd is the privilege boundary on the node side |

Per the standing rule, all host-root ops route through one privhelper. The new verb `bios-apply` is additive to the existing verb set; no new sudoers entries, no new polkit rules.

---

## Risks and irreversibility

1. **D1 (initramfs apply phase) is the most consequential decision and the most reversible.** If real-hardware testing reveals BIOS apply takes too long inside initramfs (Intel SYSCFG can be slow on dense-config motherboards — tens of seconds), we can pivot to "apply post-boot via clientd reboot loop" without breaking the schema or interface. The deploy-time correctness argument loses but nothing in `bios_profiles` / `node_bios_profile` changes.
2. **Setting-name case sensitivity.** Intel docs use one casing in some places, another in others. Normalize at write time to whatever `intel-syscfg /d` emits. Document in commit 1.
3. **Vendor binary version drift.** Operator updates `intel-syscfg`, new version emits subtly different setting names. Mitigation: profile-create runs `SupportedSettings()` against the live binary and warns on unknown names; doesn't block. Failures still surface at apply.
4. **Profile JSON growth.** If operators paste in 200-setting dumps the JSON column gets large. SQLite handles it fine; v1 doesn't worry.
5. **Drift remediation policy.** v1 reports drift, doesn't auto-fix. If a customer asks for auto-fix it's a server_config flag; cheap to add later.

The Type-1 decisions are: D2 (interface shape — Diff/Apply/ReadCurrent surface), D4 (schema), D6 (operator-supplied binary, not bundled), and D1 (apply phase). All four pinned in commit 1's PR review before commit 2 ships.
