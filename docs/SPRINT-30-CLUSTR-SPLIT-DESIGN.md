# Sprint 30 — Splitting clustr-serverd: control plane vs. builder

**Status:** Design — go/no-go decision pending founder review
**Author:** Richard
**Date:** 2026-05-01
**Source:** ARCH-1 (#217), follow-on from autodeploy review 2026-05-02
**Task IDs:** continue from #222 → start at #223
**Verdict (TL;DR):** Worth doing. Recommended path: **C** (compile-time mode flag) in Sprint 30, then **B** (true split) in Sprints 31–32 only if Sprint 30 doesn't fully recover the win. Total estimated cost: 2.5–4 sprint-weeks of one engineer.

---

## TL;DR

clustr-serverd today is a single Go process that owns:

1. The HTTP API (operator + node-facing)
2. The DHCP and TFTP servers (when `--pxe`)
3. The clientd WebSocket hub
4. SQLite state (the source-of-truth DB at `/var/lib/clustr/db/clustr.db`)
5. The embedded React webapp (`//go:embed all:dist`)
6. Long-running ISO + initramfs build pipelines (10–15 min QEMU runs)
7. Long-running multicast `udp-sender` sessions (UDPCast)
8. Long-running reimage orchestration goroutines
9. The image-shell `systemd-nspawn` lifecycle
10. ~15 background workers (verify-boot scanner, alert engine, stats sweepers, digest queue, etc.)

Every restart of this process is a blast across all ten surfaces. We have spent the last ~2 weeks of engineering writing **smart-restart guards** in the autodeploy script (BUG-1, OPS-1, REGRESSION-1, BUG-14, BUG-15, AUTODEPLOY-1, AUTODEPLOY-2) trying to keep the process alive long enough for in-flight builds to finish before the next push triggers a restart. That cascade is the symptom; the disease is the monolith.

The cleanest durable fix is to split build orchestration out of the control plane — either as a separate daemon (`clustr-builderd`) talking over a Unix socket, or as the same binary running in two modes (`--mode=control` vs `--mode=builder`) with an internal RPC boundary between them. Either way, restarting the control plane no longer kills in-flight builds.

The ROI is real but **not where the founder might first look**. It is overwhelmingly a **dev-iteration speed + reliability** win. Direct end-user **performance** improvement is marginal-to-zero in v1; the real wins compound over months as restart-blast-radius bugs stop being introduced.

I recommend doing it, scoped to the cheaper of the two split strategies (Path C, mode flag), in Sprint 30. Reassess Path B (true daemon split) at the end of Sprint 30.

---

## 1. Problem statement

### The cascade

Look at the bug tracker. Filter to the last six weeks. Count how many bugs trace back to "the control plane restarted while something long-running was in flight":

| Task | What broke | Symptom |
|---|---|---|
| **BUG-1** (#167) | Initramfs build cancelled by HTTP handler context | Build button always reported "interrupted" |
| **OPS-1** (#178) | Initramfs build killed by autodeploy timer mid-build | Lost work; orphan temp dirs in `/tmp` |
| **BUG-11** (#204) | Embedded `build-initramfs.sh` drifted from canonical | Build crashed on cloner after autodeploy reset |
| **BUG-12** (#205) | Initramfs build crashed on missing `initramfs-init.sh` template | Same root cause class |
| **BUG-13** (#206) | `udp-receiver` not vendored on cloner; multicast silently disabled | Surface area too large to spot in CI |
| **REGRESSION-1** (#207) | DEV-4 flipped embed build tag → cloner serves "bundle not built" stub | Webapp embedded in same binary as everything else |
| **BUG-14** (#212) | Initramfs build attached to HTTP handler goroutine; client disconnect cancelled it | Same class as BUG-1 |
| **BUG-15** (#213) | `ReconcileStuckInitramfsBuilds` couldn't self-heal valid orphaned artifacts | Reconciliation only exists because crashes leave state inconsistent |
| **OPS-7** (#214) | Orphaned initramfs build temp dirs accumulate | Cleanup is now a scheduled task |
| **AUTODEPLOY-1** (#215) | `GetActiveJobs` doesn't cover ALL long-running operations | Restart guard is incomplete |
| **AUTODEPLOY-2** (#216) | Tighten autodeploy with longer cadence, defer cap, docs fast-path, session secret | Workaround upon workaround |

Eleven tasks. One root cause: **the process you would naturally restart for a config push is the same process running 15-minute QEMU children, in-flight `udp-sender` sessions, reimage orchestrators, and live `systemd-nspawn` shells.**

The autodeploy script in its current form (`scripts/autodeploy/clustr-autodeploy.sh`, 674 lines) reads like a monument to this problem:

- Lines 347–404: defer restart if any image is in `building` state
- Lines 376–408: defer restart if any reimage is in non-terminal state
- Lines 410–472: defer restart up to 60 cycles (5 hours!) if any initramfs build is in flight
- Lines 615–666: 30s health check + automatic rollback after restart
- The whole `_SAFE_SKIP_REGEX` docs-only fast-path exists because **we don't trust restarts**

These guards are correct workarounds. They are also fragile (every new long-running operation needs a new flag in `GetActiveJobs`), they delay shipping (a 5-hour stuck build defers every commit behind it), and they explicitly allow data loss after the cap fires ("forcing restart" at line 457).

The deployed `clustr-serverd.service` unit hard-codes the smoking gun. From `deploy/systemd/clustr-serverd.service:64–75`:

> "8G ceiling: clustr-serverd is a combined management plane + ISO builder that spawns QEMU children. Each QEMU builder VM requests `-m 2048` (2G), and multiple concurrent builds plus Go runtime overhead easily exceed 2G. The old 2G limit was appropriate for a pure control plane; with builders co-located it caused SIGKILL at mid-build... Follow-on: introduce `clustr-builders.slice` so builder QEMU children are accounted separately from the management plane cgroup."

That comment was written by an engineer staring directly at the architectural smell and applying a 4× memory-cap bandage instead. We've been doing that for months.

### What it costs us today

- **Engineering time, ~2 weeks already spent** on the restart-guard cascade above. More guards inevitable as more long-running operations land (e.g., post-boot BIOS apply, future `clustr-builderd installer build` for offline installer).
- **Operator credibility loss** — every shipped fix is described in commit messages as "smart-restart guard for X" instead of feature work.
- **Deploy throughput cap on the dev host (cloner)** — autodeploy timer fires every 5 min but defers up to 5 hours, so commits queue silently behind a single stuck build.
- **Implicit ceiling on concurrent builds** — current process holds the build semaphore; running two ISO builds + a multicast session + 50 SSE subscribers in one process is fine until OOM, and OOM kills everything not just the build.
- **Restart-survives-state hidden assumptions** — every component now has its own reconciler (`ReconcileStuckBuilds`, `ReconcileStuckInitramfsBuilds`, `runReimagePendingReaper`, `resumeRunningGroupReimageJobs`, `CleanupOrphanedInitramfsTmpDirs`). Each one is correct in isolation; collectively they are an admission that the process is too important to stay up and too fragile to restart cleanly.

The next class of bugs we will see if we don't fix this:

- A long-running multicast session restarted mid-stream → all 50 receivers fail, fall back to unicast, saturate the `/blob` endpoint
- A `clustr exec` over WebSocket in a 10-minute remote `dnf upgrade` killed by autodeploy → silently truncated output, partial upgrade
- A `systemd-nspawn` image-shell session terminated mid-edit → operator loses uncommitted overlay changes
- Any future post-boot BIOS apply (Sprint 26 backlog) interrupted → node left in an undefined firmware state

Every one of those is the same bug. We can keep patching, or we can move the boundary.

---

## 2. Target architecture

### Recommended shape (Path C, see §4)

```
                          ┌─────────────────────────────────┐
                          │  /var/lib/clustr/db/clustr.db   │
                          │  (SQLite WAL, single writer)    │
                          └──────────────┬──────────────────┘
                                         │
                  ┌──────────────────────┼──────────────────────┐
                  │                      │                      │
        ┌─────────▼─────────┐  ┌─────────▼─────────┐  ┌─────────▼─────────┐
        │ clustr-serverd    │  │ clustr-serverd    │  │ clustr-privhelper │
        │  --mode=control   │  │  --mode=builder   │  │ (existing setuid) │
        │                   │  │                   │  │                   │
        │ HTTP API          │◄─┤ Build orchestrator│  │ host-root ops     │
        │ Webapp (embed)    │  │  - ISO builds     │  │ (no change)       │
        │ DHCP/TFTP         │  │  - initramfs      │  │                   │
        │ ClientdHub (WS)   │  │  - rootfs extract │  │                   │
        │ Reimage scheduler │  │  - multicast send │  │                   │
        │ Alert engine      │  │                   │  │                   │
        │ ~15 background    │  │ Workers (cgroup:  │  │                   │
        │  workers          │  │  clustr-builders. │  │                   │
        │                   │  │  slice, 8G+)      │  │                   │
        │ cgroup: control.  │  │                   │  │                   │
        │  slice, 2G        │  │                   │  │                   │
        └───────────────────┘  └───────────────────┘  └───────────────────┘
                ▲                       ▲
                │                       │
                │  Unix socket /run/clustr/builder.sock (gRPC)
                └───────────────────────┘
```

Two **processes**, both built from the same Go binary, selected by `--mode={control,builder}`. They share the SQLite DB on disk and communicate over a local-only Unix domain socket using gRPC.

### Daemon responsibilities

#### `clustr-serverd --mode=control` (the control plane)

Owns:
- HTTP listen socket (operator API + node API)
- Embedded webapp (`//go:embed all:dist`)
- DHCP + TFTP servers (no change — they are tiny; keep with control plane in v1, see §2.D2)
- Clientd WebSocket hub (~25–500 long-lived WS connections to nodes)
- SQLite reads/writes for everything except build progress streams
- Reimage orchestrator (the **scheduler** that decides which nodes to reimage; the **work** is delegated to builder)
- Alert engine, stats sweepers, digest queue, audit purger, verify-boot scanner — all the small periodic workers
- Two-stage commit, LDAP module, slurm module, BIOS profile management, all CRUD
- Bootstrap, key rotation, session management

Restart blast radius after split:
- Open browser sessions: dropped (already mitigated by `CLUSTR_SESSION_SECRET`)
- In-flight HTTP requests: ~5–15 second drain
- SSE subscribers: reconnect with jittered backoff (UX-3 already handles this)
- WebSocket clientd connections: nodes reconnect (~10s) — transient
- **In-flight ISO/initramfs builds: NOT AFFECTED** ← the win
- **In-flight multicast sessions: NOT AFFECTED** ← the win
- **In-flight reimages mid-stream: NOT AFFECTED for the streaming/build work; the scheduler resumes from DB** ← the win

Target restart time: <10 seconds. Today's measured restart: 30+ seconds when an initramfs build is in flight (defers restart entirely; 5+ hours observed).

#### `clustr-serverd --mode=builder` (the builder)

Owns:
- All build pipelines: ISO factory, initramfs builder, rootfs extraction, qcow2 conversion, image scrubber
- Multicast `udp-sender` lifecycle (long-running per session)
- The image-shell `systemd-nspawn` session lifecycle (today via `image.ShellManager`)
- The build progress streams (the per-build ring buffers; not persisted)
- Cgroup slice: `clustr-builders.slice`, generous `MemoryMax` (16G+), `TasksMax=infinity`

Does NOT own:
- The DB schema or migrations (control plane runs migrations on startup; builder reads only)
- Any external network listeners (Unix socket only)
- Any session/auth state (control plane forwards the auth context as part of each RPC)

Restart blast radius after split:
- All in-flight builds aborted (same as today, but only when the **builder** restarts, not when the control plane restarts)
- Builder restarts only happen when (a) build code changed, or (b) operator explicitly issues `systemctl restart clustr-builderd` (or `clustr-serverd@builder`, see §5 for the systemd template-unit choice)
- Control-plane HTTP traffic and node WS connections: **unaffected**

This is the asymmetry that matters. The control plane changes 10× more often than the build pipeline (UI tweaks, API endpoints, schema migrations). Decoupling restart cadence is most of the ROI.

### Most important architectural decisions

#### D1. Builder is **stateful** (long-running process), not stateless

I considered the stateless variant: every build is a fresh `clustr-serverd build run --build-id=X --image-id=Y` process, fork/exec'd by the control plane. Rejected, for two reasons:

1. **Build progress ring buffers** (`internal/server/buildprogress.go`, 420 lines) live in memory and are streamed to operator over SSE. Pushing them through the DB or a separate file-tail layer adds latency and complexity for zero gain.
2. **Multicast `udp-sender` sessions** are inherently long-running (single 60s-batched session can drive 50 receivers for 10 min). A fork/exec model for these would re-derive the long-lived process anyway.

So: long-running daemon. Build IDs are persisted to DB by the control plane on enqueue; builder picks them up via RPC; in-memory ring buffer for progress is rebuilt on builder restart only for the currently-running build (not historical).

#### D2. DHCP/TFTP **stays with control plane** in v1 (do not split into clustr-pxed)

Reasoning:
- DHCP+TFTP+iPXE chainload is ~600 LOC total. Tiny. Splitting it doesn't recover blast radius — if the control plane is down, nothing PXE-boots anyway because the iPXE chainload URL is on `:8080`.
- DHCP server holds in-memory leases (`pxeSrv.DHCPServer.GetLeaseIP`). Splitting introduces a cross-process lease table.
- The `OnSwitchDiscovered`, `ResolveReservedIP`, `IsIPReservedByOtherMAC` callbacks all need a live DB connection; in v1 they're cheap closures (`cmd/clustr-serverd/main.go:368–407`).

Revisit if a future bug demonstrates that DHCP-restart pain is meaningful. Today it's not.

#### D3. Shared **SQLite database file**, not message-passed state

SQLite WAL mode allows multiple readers + one writer. The control plane is the writer for almost everything. The builder writes only:
- Build progress checkpoints (rare, every 5–15s)
- Build outcome (final terminal state + artifact path/SHA)

Concurrency model:
- Control plane and builder open the same `clustr.db` file
- Both use `_journal_mode=WAL`, `_busy_timeout=5000`
- Builder **never** runs schema migrations (control plane is authoritative)
- Builder writes are scoped to ~3 tables (`image_builds`, `initramfs_builds`, `multicast_sessions`)

Alternative considered: builder is read-only, all state mutations go through control plane via RPC. Rejected — adds a round-trip per progress checkpoint and creates a single point of failure for build progress (control plane down ⇒ can't checkpoint a 15-min build that's 90% done).

#### D4. IPC over **gRPC on Unix domain socket**

Choices considered:
- **HTTP/JSON over loopback** — simpler, but no streaming RPC, no codegen, no contract evolution story
- **gRPC on Unix socket** — protobuf-generated, streaming RPC for progress, well-trodden Go ecosystem
- **Custom protocol over Unix socket** — we already have one for clientd, but it's a chat protocol, not a request/response RPC

gRPC on Unix socket wins. Schema lives at `internal/builder/proto/builder.proto`. Both daemons import the generated code. Socket path: `/run/clustr/builder.sock`, mode `0600`, owned by `root:root` (both daemons run as root in v1). No transport-layer auth needed because the socket file permissions are the auth boundary — the same threat model as `/var/run/docker.sock`.

#### D5. **Restart sequencing**: control plane is independent; builder waits for control plane

systemd unit ordering:
- `clustr-serverd-builder.service` has `After=clustr-serverd.service` (so DB schema is current)
- Neither has `Wants=` on the other (each can survive without the other)
- If builder is down: control plane returns 503 from build-related endpoints with `Retry-After: 30`; queues survive in DB
- If control plane is down: builder finishes any in-flight build and idles waiting for new RPCs; no work lost

#### D6. Web bundle: **embedded in control plane only**

The `//go:embed all:dist` lives in the control-plane code path. The builder mode skips the embed via build tag (`-tags webdist` only on control). REGRESSION-1 is permanently fixed — builder has no opinion about whether webapp dist is built.

#### D7. clustr-clientd contract: **unchanged**

Nodes still talk to control plane on `:8080` over HTTPS+WebSocket. They do not know the builder exists. This is critical: 100+ deployed nodes in the field can't be expected to learn a new endpoint.

#### D8. Operator CLI/Web UI contract: **unchanged**

`clustr` CLI hits `:8080` HTTP API. Webapp loads from `:8080`. The builder is invisible to operators (except as a separate systemd unit they may need to look at). All build endpoints stay on the control plane HTTP surface; the control plane forwards the actual build work to the builder via internal RPC.

---

## 3. ROI quantification

This is the heart of the plan. The founder asked specifically about three dimensions: **performance, reliability, speed.** Honest answers below.

### 3.1 Reliability — STRONG WIN

**Bugs eliminated by class** (not just the specific bugs already in the tracker, but the class):

| Class | Eliminated? | Notes |
|---|---|---|
| Restart kills in-flight ISO build | YES | Builder doesn't restart on control-plane push |
| Restart kills in-flight initramfs build | YES | Same |
| Restart kills in-flight reimage stream | YES (mostly) | Streaming body work is in builder; scheduler in control plane is restartable |
| Restart kills multicast `udp-sender` mid-session | YES | Builder owns sender |
| Restart kills clientd WS hub | NO | Hub is in control plane; nodes will still reconnect on control-plane restart |
| Restart drops operator HTTP requests | NO | Same as today; standard graceful drain still applies |
| Restart kills in-flight `clustr exec` (long remote command) | NO | Exec rides the WS hub which lives in control plane |
| Restart kills `systemd-nspawn` image-shell session | YES | ShellManager moves to builder |
| OOM in build child process kills control plane too | YES | Separate cgroup slices isolate memory accounting |
| Build pipeline panic → control plane crash | YES | Process boundary contains panics |

**Specific historical bugs that wouldn't have happened post-split:**

- BUG-1 (#167) — initramfs build context tied to HTTP handler. Doesn't happen if build runs in a separate daemon over RPC.
- OPS-1 (#178) — autodeploy SIGKILLs initramfs build. Doesn't happen if autodeploy only restarts control plane.
- BUG-14 (#212) — same class as BUG-1.
- REGRESSION-1 (#207) — webapp embed tag flip broke cloner. Doesn't happen if webapp lives in control plane only and builder's deploy is decoupled.
- The next 5–10 bugs of this class that we haven't written yet.

**New failure modes introduced (honest accounting):**

| New failure | Mitigation |
|---|---|
| Builder daemon down → all build endpoints return 503 | Health check + `systemctl status clustr-serverd-builder`; control-plane health endpoint exposes builder status |
| Unix socket missing/permissions wrong | systemd `RuntimeDirectory=clustr` + unit ordering; integration test on first boot |
| Schema version skew between daemons (control plane upgraded, builder old) | Version handshake on first RPC; builder refuses to start if migration version exceeds its compiled max |
| Both daemons writing to same DB row → SQLite contention | Write paths are non-overlapping by design (D3); WAL mode handles incidental contention |
| RPC schema breaking change ships before builder upgraded | Strict contract: gRPC service version embedded; both daemons reject mismatched majors |
| Two restart points to monitor, not one | Slightly more `journalctl -u` typing; net win because control-plane restarts go from "rare, scary" to "frequent, boring" |

Net: I judge the **new failure surface to be substantially smaller** than the eliminated surface. The new failures are well-bounded (RPC unavailability is a single state with a single mitigation); the old failures were diffuse (every long-running operation needed its own restart guard).

### 3.2 Performance — NEUTRAL TO WEAK WIN

I'm being honest: splitting daemons usually **adds** latency, not subtracts.

**Where the split costs:**
- Operator request to start a build now incurs one extra Unix-socket RPC round-trip. ~100µs–1ms on loopback. Negligible vs. multi-second build setup.
- Build progress polling now has one extra hop control-plane → builder → control-plane → operator SSE. <2ms additional latency. Negligible.

**Where the split wins:**
- **Build child processes (QEMU, dracut, rsync) no longer compete with HTTP handlers, alert engine ticks, and stats sweepers for goroutine scheduling.** This is the only direct perf claim and it's modest — Go's scheduler is good and the work is mostly I/O-bound, but for CPU-bound phases (initramfs cpio compression, image scrub) you'll see ~5–10% wall-clock improvement when the host is also serving operator requests.
- **Memory accounting**: builder's cgroup gets the 8G+ ceiling; control plane runs in a tighter 2G slice. Net memory footprint unchanged; OOM blast radius shrinks dramatically.
- **Future**: builder could be deployed on a separate host (build farm). Not v1 scope, but the architecture allows it without rework. This is the long-tail perf win — when a customer reimages 200 nodes simultaneously they want 4 builder hosts pumping multicast, not one.

**Honest bottom line on perf:** if the founder is hoping for "the API got 30% faster", it didn't. Operator-facing latency is unchanged or fractionally worse. The perf story is **headroom**: the architecture stops being the limiter for any future scale work.

### 3.3 Speed (dev iteration + deploy throughput) — STRONG WIN

This is where the ROI compounds.

**Dev iteration speed:**
- Today: every commit on `main` triggers `git fetch` → web build → `go build` → `systemctl restart clustr-serverd`. The restart guards add up to **5 hours** of artificial defer if a build is in flight (from `INITRAMFS_DEFER_CAP=60` × 5 min cycles). Operators stop pushing during long builds.
- Post-split: control plane restarts unconditionally in <10s. No defer logic for control-plane changes. Builder restart only required when build code itself changes (~10–20% of commits today, less as build pipeline stabilizes).
- **Net cycle time: from "5 minutes optimistic, 5 hours worst-case" to "10 seconds, every push, deterministic"** for the 80% of commits that don't touch build code.

**Deploy throughput (dev host cloner):**
- Today: serial. One commit → one autodeploy cycle → one restart. Buffer between commits is 5 min minimum.
- Post-split: control-plane commits don't queue behind in-flight builds. Builder commits still serial (correct — only one build pipeline image at a time).

**Time-to-recover from a control-plane crash:**
- Today: a panic in the alert engine kills the process. systemd restarts it (`Restart=on-failure RestartSec=5s`). All in-flight builds are dead, all WS connections drop, all SSE subscribers reconnect, autodeploy's reconcile-stuck-builds runs, half the orphaned temp dirs need OPS-7 cleanup later.
- Post-split: panic in the alert engine kills only control plane. Builder keeps running. WS hub is gone but the underlying agents (clustr-clientd) reconnect in ~10s. No build state lost.

**Time-to-ship a hotfix:**
- Today: `git push` → wait up to 5 hours for autodeploy to dare restart → 30s health-check → maybe rollback. Operator anxiety is high; we measure deploys in hours.
- Post-split: control-plane hotfix `git push` → ~5 min autodeploy cycle (because cadence stays at 5 min for safety) → 10s restart → 5s health check. Operator anxiety is low.

**Quantification:** the autodeploy cascade has consumed roughly 11 task-units (BUG-1, OPS-1, BUG-11, BUG-12, BUG-13, REGRESSION-1, BUG-14, BUG-15, OPS-7, AUTODEPLOY-1, AUTODEPLOY-2). At ~1 sprint-day average, that's ~2 sprint-weeks of pure firefighting. The split costs ~2.5–4 sprint-weeks. **Break-even is the next ~10 bugs of this class we don't have to write** — and we will write them, because we are still adding long-running operations (post-boot BIOS apply, future installer builder, future image scrubber, multicast v2).

---

## 4. Migration path

Three options. I evaluated each.

### Path A — Big-bang split

Ship `clustr-builderd` as a separate binary at v0.x.0. Operators do a one-time migration: install the new RPM, run `clustr-builderd-migrate`, restart everything.

- **Pros:** clean cutover; new architecture from day one; no lingering compatibility code
- **Cons:** high blast radius on the cutover release; every customer's deploy script breaks once; rollback story is "downgrade to old single binary" which is non-trivial; can't ship incrementally
- **Verdict:** REJECTED. We have ~3 deployed clusters (cloner + a few prospect labs). Not worth the cutover risk.

### Path B — Strangler-fig incremental (extract over multiple sprints)

Sprint 30: extract initramfs builder into RPC-callable module, still in same process (foundation work).
Sprint 31: split that module into separate process (`clustr-builderd`), wire RPC.
Sprint 32: extract ISO builder + multicast sender similarly.
Sprint 33: extract image-shell ShellManager.

- **Pros:** each sprint independently shippable; lowest risk per deploy; we always have a working product; can pause after Sprint 30 if value is already captured
- **Cons:** longest total elapsed time (4 sprints); some intermediate states have awkward interfaces (a module-level RPC inside one process is mostly overhead); the "split" claim isn't true until Sprint 31
- **Verdict:** ACCEPTABLE FALLBACK. Use if Path C proves insufficient.

### Path C — Compile-time mode flag (recommended)

One binary, `clustr-serverd`. Two run modes selected by `--mode={control,builder}` (default `control`). The same code, partitioned at startup: `--mode=control` skips builder workers and exposes the build endpoints as RPC clients to the builder socket; `--mode=builder` skips HTTP listener and exposes a gRPC server on Unix socket. Same DB. Two systemd units (`clustr-serverd.service`, `clustr-serverd-builder.service`).

- **Pros:**
  - Single binary, single build, single RPM — packaging is unchanged
  - No new repo, no new CI workflow
  - Operator runs two systemd units instead of one — incremental complexity
  - Most of the reliability win is captured (separate restart cadence, separate cgroup slice)
  - Trivially reversible: drop one of the two units, the binary still functions in single-mode legacy form (we keep `--mode=monolithic` as a default-off escape hatch for v0.x compatibility)
- **Cons:**
  - Both processes carry the full binary (each ~80MB on disk; negligible)
  - You can't deploy builder on a separate host without bundling the binary (also fine; `scp` works)
  - Less architecturally "pure" than two distinct binaries
- **Verdict:** RECOMMENDED. Captures 90% of the value at 50% of the cost.

**Recommendation:** Sprint 30 = Path C. If after Sprint 30 we still see meaningful blast-radius bugs, Sprint 31 promotes the builder to a separate binary (Path B-style follow-on). Most likely we won't need to.

### What's the same across all three paths

- gRPC contract at `internal/builder/proto/builder.proto`
- Shared SQLite, WAL mode, separate write paths
- `clustr-builders.slice` cgroup with 16G memory cap
- Unix socket at `/run/clustr/builder.sock`, `0600`
- systemd unit ordering: builder `After=` control plane

---

## 5. Sprint breakdown

Path C scoped. Numbering continues from #222 → starts at #223.

### Sprint 30 — Control-plane / builder split (Path C, mode flag)

**Theme:** ship two processes from one binary, sharing DB, talking gRPC over Unix socket. Restart of control plane no longer kills in-flight builds.
**Estimated duration:** 2–3 weeks
**Owner mix:** Dinesh (code), Gilfoyle (systemd + autodeploy + cgroup), Richard (RPC contract review + arch decisions)

#### Tasks

- [ ] **#223 — Builder RPC contract + protobuf definitions (HIGH, M)**
  Owner: Richard arch + Dinesh implements.
  In: `internal/builder/proto/builder.proto` defining `BuilderService` with RPCs: `EnqueueImageBuild`, `EnqueueInitramfsBuild`, `EnqueueRootfsExtract`, `StartMulticastSession`, `WatchBuildProgress` (server-streaming), `CancelBuild`, `Status` (health + build queue depth + version). Codegen via `protoc-gen-go` + `protoc-gen-go-grpc`. Pin protobuf versions in `go.mod`. Acceptance: `make proto` regenerates cleanly; CI diff-check fails build on stale generated files.
  Depends on: nothing.
  DoD: contract reviewed by Richard; codegen lands behind a `make proto` target with CI diff-check; service version constant defined.

- [ ] **#224 — `--mode` flag plumbing in clustr-serverd (HIGH, S)**
  Owner: Dinesh.
  In: cobra flag `--mode={control,builder,monolithic}` (default `control`). `--mode=monolithic` is the v0.x compat shim: same as today, single process running everything (do NOT remove until v0.y when we deprecate). Wire mode into `runServer`: in `control` mode skip the StartBuilderWorkers branch; in `builder` mode skip ListenAndServe and start gRPC server on Unix socket; in `monolithic` mode do both (current behavior).
  Depends on: nothing (independent of #223).
  DoD: `clustr-serverd --mode=builder` starts and exits cleanly without a control-plane HTTP listener; `--mode=control` skips the builder subsystem; `--mode=monolithic` is bit-identical to today's behavior. Smoke test added.

- [ ] **#225 — Extract build orchestration into `internal/builder/` package (HIGH, L)**
  Owner: Dinesh.
  In: move `internal/server/buildprogress.go` (420 LOC), the build-pipeline goroutines that today live in `image.Factory`, the multicast `Scheduler` lifecycle, and the `image.ShellManager` into a new `internal/builder/` package. Keep the same Go API; the goal is a clean module boundary, not a refactor of internal logic. The control plane now imports `internal/builder` only for type definitions; the actual orchestration is invoked via the gRPC client.
  Depends on: #223 (proto types).
  DoD: `internal/server/server.go` no longer directly invokes build pipelines; all build entry points go through a `BuilderClient` interface. Existing tests pass. -race clean.

- [ ] **#226 — Builder gRPC server (HIGH, L)**
  Owner: Dinesh.
  In: `cmd/clustr-serverd/builder.go` registers the gRPC service on `/run/clustr/builder.sock`. Implements all `BuilderService` RPCs by dispatching to `internal/builder/`. `WatchBuildProgress` streams ring-buffer events to subscribers. Authentication: socket file permissions only (`0600`, `root:root`). Add a `Status` RPC returning version + queue depth + active build count.
  Depends on: #223, #225.
  DoD: integration test: spin up `--mode=builder`, connect a gRPC client, enqueue a no-op build, observe progress stream, query Status. CI green.

- [ ] **#227 — Control-plane gRPC client + endpoint forwarding (HIGH, L)**
  Owner: Dinesh.
  In: `internal/server/builderclient.go` wraps the gRPC client with retry, dial-on-demand, circuit breaker (3 consecutive RPC failures → 30s backoff). All HTTP build endpoints (`POST /api/v1/images/{id}/build`, `POST /api/v1/system/initramfs/rebuild`, `POST /api/v1/multicast/sessions`, `WS /api/v1/images/{id}/shell`) route through this client. If client is in backoff or builder unreachable, return HTTP 503 with `Retry-After: 30`.
  Depends on: #226.
  DoD: web UI build button works end-to-end against a two-process deployment. CLI `clustr image build` works. SSE progress works. Builder-down case returns 503 cleanly with a structured error body operators can parse.

- [ ] **#228 — Build progress streaming via gRPC server-streaming (MEDIUM, M)**
  Owner: Dinesh.
  In: `WatchBuildProgress` server-streams progress events from the builder's in-memory ring buffer. Control plane SSE handler subscribes via gRPC stream, fans out to all browser SSE subscribers via `eventbus`. Reconnect handling on both ends (gRPC stream dies → SSE handler reconnects up to 3× with backoff before reporting "build progress unavailable").
  Depends on: #226, #227.
  DoD: kill builder mid-build, restart it; SSE consumers see a `progress.unavailable` event then resume on reconnect (or surface failure cleanly if build was lost). CI green.

- [ ] **#229 — DB write-path partitioning + WAL mode review (MEDIUM, M)**
  Owner: Dinesh + Richard arch.
  In: audit every DB write to confirm builder writes are scoped to `image_builds`, `initramfs_builds`, `multicast_sessions`, `build_progress_checkpoints` (new table, see below). Confirm WAL mode is enabled by default in `internal/db/Open`. Add `_busy_timeout=5000` to the DSN if not already there. New table `build_progress_checkpoints(build_id, phase, percent, last_message, ts)` for crash-resume of in-flight builds (replaces the in-memory-only ring buffer for the **last known** state, not the full log).
  Depends on: nothing.
  DoD: new migration NNN added; integration test exercises concurrent builder write + control-plane write to non-overlapping rows; -race clean.

- [ ] **#230 — systemd units for two-mode deployment (HIGH, M)**
  Owner: Gilfoyle.
  In: rewrite `deploy/systemd/clustr-serverd.service` to default to `--mode=control` with `MemoryMax=2G`. Add new `deploy/systemd/clustr-serverd-builder.service` running `--mode=builder` under `clustr-builders.slice` with `MemoryMax=16G`, `TasksMax=infinity`, the existing capability + device allowlist (CAP_SYS_ADMIN, /dev/kvm, etc.). Add `RuntimeDirectory=clustr` to both for socket dir. Builder has `After=clustr-serverd.service`; not `Wants`. RPM postinstall enables both units.
  Depends on: #224.
  DoD: fresh RPM install on a Rocky 9 VM brings up both units; `systemctl status` shows both green; socket exists with correct permissions; control-plane restart leaves builder running.

- [ ] **#231 — Autodeploy: split-aware restart logic (HIGH, M)**
  Owner: Gilfoyle.
  In: rewrite `scripts/autodeploy/clustr-autodeploy.sh` so that:
  - Default action is `systemctl restart clustr-serverd` only (control plane). Builder stays up.
  - When changed paths include `internal/builder/`, `internal/image/`, `cmd/clustr-serverd/builder.go`, `internal/multicast/`, restart **builder** as well. Use the existing build-in-progress guards ONLY for builder restart, not control-plane restart.
  - Remove the `INITRAMFS_DEFER_CAP=60` defer-up-to-5-hours logic for control-plane restart; keep it for builder restart with a much smaller cap (e.g., 12 cycles = 1 hour).
  - Add a fast-path: if changed paths are 100% control-plane (no `internal/builder/`, `internal/image/`, etc.), bypass all build-in-progress guards.
  Depends on: #230.
  DoD: dry-run on a test commit that changes only `internal/server/handlers/`: control plane restarts in <10s, builder untouched. Dry-run on a commit changing `internal/builder/`: builder restart deferred until in-flight build completes.

- [ ] **#232 — Crash-recovery + reconcile across both daemons (MEDIUM, M)**
  Owner: Dinesh.
  In: control-plane `ReconcileStuckBuilds` and `ReconcileStuckInitramfsBuilds` need to query builder via gRPC `Status` RPC for "what's actually running", instead of marking everything stuck on startup. Builder on startup reads `build_progress_checkpoints` and offers each in-flight build to the client as "resumable" — control plane decides to fail it (today's behavior, safe default) or resume (future enhancement, not in v1).
  Depends on: #226, #229.
  DoD: scenario test: builder crash mid-build → restart control plane → control plane sees crashed build in DB, gRPC status confirms not running, marks failed cleanly. No orphan temp dir cleanup needed (separate task OPS-7 still applies for cgroup-leaked tmpdirs).

- [ ] **#233 — Builder health + observability surface (MEDIUM, S)**
  Owner: Dinesh.
  In: control plane `/healthz` and `/api/v1/system/active-jobs` query builder via `Status` RPC, surface builder version + reachability + queue depth. New `/api/v1/system/builder` endpoint returns full builder status. Web UI Settings → Server tab shows builder version + reachability + last RPC latency.
  Depends on: #226.
  DoD: pull builder unit, see UI reflect "builder unreachable"; restart builder, see status flip to green.

- [ ] **#234 — `clustr doctor` builder checks (MEDIUM, S)**
  Owner: Dinesh.
  In: extend `cmd/clustr-serverd/doctor.go` to verify (a) builder socket exists and is `0600`, (b) gRPC ping succeeds, (c) builder version matches control plane (or warn if skewed), (d) builder cgroup slice is loaded.
  Depends on: #226, #230.
  DoD: `clustr-serverd doctor` reports all five in green/yellow/red. Integration test for the broken-socket case.

- [ ] **#235 — Migration path docs + RPM postinstall behavior (MEDIUM, M)**
  Owner: Gilfoyle + Richard.
  In: `docs/SPLIT-MIGRATION.md` covering: how to upgrade from monolithic v0.x to split v0.y; how to roll back (downgrade RPM, set `--mode=monolithic`); how to debug a builder unreachable error; how to read the two unit logs. RPM postinstall script: on upgrade from v0.x, install builder unit but leave it disabled by default for one release cycle; emit a `clustr post-upgrade` notice telling the operator to enable it.
  Depends on: #230.
  DoD: doc reviewed by Jared (operator perspective); RPM upgrade tested on a v0.x → split target.

- [ ] **#236 — End-to-end integration test (HIGH, M)**
  Owner: Dinesh.
  In: under `test/integration/builder_split/`, a Go test that spins up a control plane + builder pair against a tmp DB, exercises: image build, initramfs build, multicast session, image-shell, builder-restart-during-build, control-plane-restart-during-build. Must run on every CI commit; tagged so it can be skipped locally.
  Depends on: #226, #227, #230.
  DoD: green in CI on at least 5 consecutive runs (flake-resistance check).

#### Sprint 30 ship gate

- All 14 tasks landed. CI green on `main`.
- Cloner running split mode for ≥7 consecutive days with at least 50 control-plane commits and at least 5 build-pipeline commits during the window.
- Zero new bugs in the BUG-1 / OPS-1 / BUG-14 class during the 7-day soak.
- Operator runbook for "builder unreachable" tested by Jared (he can recover without Richard's help).

### Sprint 31 — Optional follow-ons (only if Sprint 30 ROI confirmed)

**Theme:** harden split, build the foundation for multi-host builders.

- [ ] **#237 — Builder schema-version handshake (MEDIUM, S)** — refuse to start if compiled max migration < DB current
- [ ] **#238 — gRPC contract evolution discipline (MEDIUM, S)** — adopt buf lint/breaking-change check in CI
- [ ] **#239 — Per-build-type concurrency caps in builder (MEDIUM, M)** — semaphores for `image_build`, `initramfs_build`, `rootfs_extract`, `multicast_session`; configurable via env vars
- [ ] **#240 — `clustr build cancel` CLI (LOW, S)** — already works at API level; add CLI surface
- [ ] **#241 — Foundation for remote builder (LOW, L)** — replace Unix socket with TCP+mTLS option behind a config flag (don't enable; just prove the architecture allows it). DEFER unless a customer asks.

### Sprint 32 — Path B follow-on (only if needed)

**Theme:** if Sprint 30's mode-flag split has unresolved warts (binary size, packaging awkwardness, operator confusion), promote the builder to a separate binary `clustr-builderd`.

- [ ] **#242 — Extract `cmd/clustr-builderd/main.go`** — separate entry point
- [ ] **#243 — Separate RPM `clustr-builderd`** — independent versioning
- [ ] **#244 — Deprecate `--mode=monolithic` in v0.y+1**

Defer this entire sprint by default. Decide at Sprint 30 retro.

---

## 6. Risks and what could go wrong

| Risk | Likelihood | Severity | Mitigation |
|---|---|---|---|
| **gRPC contract breakage between daemons during rolling deploy** | MEDIUM | HIGH | Service version handshake (#237); buf breaking-change check in CI (#238); never remove RPC fields, only add |
| **SQLite write contention spikes under multi-writer** | LOW | MEDIUM | WAL mode + 5s busy_timeout + non-overlapping write paths (D3); load test in #236 |
| **Builder crash leaves stale Unix socket file** | MEDIUM | LOW | systemd `RuntimeDirectory=clustr` recreates dir on restart; builder removes stale socket on startup; integration test |
| **Operator confusion: "which logs do I tail?"** | HIGH | LOW | doc (#235); `clustr doctor` (#234) auto-shows both unit statuses; web UI surfaces builder reachability |
| **Restart sequencing bug in systemd: control plane restarts before builder is ready** | MEDIUM | LOW | builder `After=` control plane; control plane retries socket connect with backoff; 503 clean degradation |
| **gRPC streaming disconnect during long build progress watch** | MEDIUM | MEDIUM | Reconnect with backoff in #228; SSE consumers see structured `progress.unavailable` events; UI shows "reconnecting" instead of silent stall |
| **Memory regression: builder + control plane combined uses more RAM than monolithic** | MEDIUM | LOW | both processes share most static segments via `mmap`; expect <50MB additional RSS per process; monitored in soak |
| **Debugging cross-process behavior is harder than in-process** | HIGH | MEDIUM | structured logging with shared `request_id` propagated via gRPC metadata; OpenTelemetry trace IDs across the boundary (Sprint 31 nice-to-have) |
| **Build cancellation races (operator hits cancel, RPC arrives after build completes)** | MEDIUM | LOW | CancelBuild RPC is idempotent and returns "not_found" cleanly; operator UI tolerates terminal-state races already |
| **Two daemons drift in version skew during partial autodeploy failure** | LOW | MEDIUM | autodeploy script (#231) reports both unit statuses; doctor check (#234) flags skew; operator notified |
| **Operator runs only one of the two units (forgets to start builder)** | MEDIUM | LOW | RPM postinstall enables both; doctor check flags missing builder; control plane returns clear 503 with hint |
| **Path C "monolithic" escape hatch becomes permanent (we never delete it)** | HIGH | LOW | Schedule deprecation in v0.y+1 (#244); add startup deprecation warning when `--mode=monolithic` is used after v0.y |
| **The split work itself takes longer than estimated** | MEDIUM | MEDIUM | Path C is explicitly the cheap option; if Sprint 30 slips by >50% we should land what we have (mode flag + RPC contract) and ship even partial value rather than carry a half-done refactor on `main` |

---

## 7. Recommendation

**Do it. Path C, Sprint 30.** Estimated 2–3 weeks of one engineer's time (Dinesh primary, Gilfoyle on systemd + autodeploy, Richard on RPC contract).

The reliability and dev-iteration speed wins are real and large. The performance dimension is honestly neutral — operator latency is unchanged and slightly worse on the build-start RPC hop. If the founder is buying this on a "10× faster builds" expectation, that is not what they will get; they will get "autodeploy stops eating builds, control plane restarts in 10s instead of deferring 5 hours, and the next 10 BUG-X / OPS-X tickets in the autodeploy class never get filed."

The cost — 2.5–4 weeks — is comparable to what we have already burned firefighting the same class. We will pay this debt eventually one way or another. Paying it now, with the split design clearly in mind and the autodeploy fires fresh, is cheaper than paying it twice as much later when a customer's 200-node multicast reimage dies because a docs commit triggered a control-plane restart.

The recommended scoping (Path C, mode flag) preserves total reversibility: if the split turns out to introduce more pain than it removes, `--mode=monolithic` is a one-flag rollback. We do not bet the company on this; we bet two sprint-weeks of one engineer.

Reassess at Sprint 30 retro:
- If reliability win is captured and operators are happy → ship as v0.y, deprecate monolithic in v0.y+1, do not invest in Path B
- If meaningful gaps remain (e.g., binary-size pain, packaging awkwardness, operator confusion that doesn't yield to docs) → Sprint 32 promotes builder to separate binary
- If reliability win did NOT materialize → revert to monolithic, root-cause why, and don't repeat

— Richard
