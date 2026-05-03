# Sprint 25 #157 — UDPCast multicast for fleet reimage

**Author:** Richard
**Date:** 2026-05-01
**Status:** Design — implementation owner Dinesh
**Source plan:** `docs/CLUSTERVISOR-GAP-SPRINT.md` Sprint 25 (renumbered #157 from #145)

---

## Goal

Cut bandwidth and elapsed time on multi-node reimages from O(N) HTTP streams to O(1) multicast transmission. Today every reimage opens its own `/blob` stream from clustr-serverd; on a 1 GbE management uplink we cap out around 20 simultaneous nodes. The fix is `udpcast` (`udp-sender` on the server, `udp-receiver` in the initramfs) with a server-side scheduler that batches reimage requests in a 60s window and fires one multicast session per `(image_id, layout_id)` tuple.

**Out of scope (explicit):** per-image bandwidth shaping (single global rate in v1), preserving session state across reset, IPv6 multicast (v1 is IPv4 only), inter-VLAN multicast routing (single L2 segment assumed — same as PXE).

---

## Architectural decisions (irreversible — get right first time)

### D1. Sender topology: in-process goroutine that fork/execs `udp-sender` per session

The sender lives inside `clustr-serverd`. Each multicast session is a goroutine that fork/execs `/usr/bin/udp-sender` with a pipe stdin fed from the same `internal/image` blob reader the unicast `/blob` handler uses. Reasons:

- One process boundary, not two. Operators already debug serverd; adding a sidecar daemon doubles the surface.
- The sender does not need root. `udp-sender` opens a UDP socket and binds to a high port — no `CAP_NET_ADMIN`, no privhelper hop.
- The blob reader already exists; multicast is "tee the bytes to a different transport," not a new ingestion path.

**Network requirements:**
- IPv4 multicast group from the administratively-scoped block, default `239.255.42.0/24`. Per-session group allocation: hash `(session_id) → 239.255.42.X` mod 254 + 1. Configurable via `CLUSTR_MULTICAST_GROUP_BASE`.
- UDP port range `9000–9999`. One sender + one receiver port per session (udpcast convention). Allocator in `internal/multicast/portalloc.go` checks the `multicast_sessions` active set to avoid collisions.
- MTU: udpcast default 1456; survives standard 1500-byte L2 frames with IP+UDP overhead. Don't tune for jumbo in v1.

### D2. Receiver discovery: kernel cmdline + iPXE menu split

Two iPXE menu items at the disk-boot screen (extending `internal/pxe/boot.go` `diskBootBIOS/UEFITemplate`):

```
item reimage-now      Reimage this node            (unicast, immediate)
item reimage-fleet    Reimage as part of fleet     (multicast, wait up to 60s)
```

`reimage-fleet` chains to a new endpoint `/api/v1/boot/ipxe?multicast=1&mac=${mac}&force_reimage=1`. The server marks the node `reimage_pending=true, multicast_join=true` and returns an iPXE script that boots the deploy initramfs **with extra cmdline params**:

```
clustr.multicast=1 clustr.session_poll_url=http://10.99.0.1:8080/api/v1/multicast/sessions/wait
```

The initramfs `clustr deploy --auto` flow (cmd/clustr/main.go `runAutoDeployMode`) checks `clustr.multicast` and, when set, **polls** `clustr.session_poll_url` instead of starting unicast download. Polling, not push: nodes drop into the boot loader with no inbound socket. The server returns 202 + `Retry-After: 5` until the scheduler fires; then 200 with the session descriptor `{group, port, rate, image_url, layout_id}`. The deploy then forks `udp-receiver --pipe "tar -xz -C /mnt/target"` (or whatever the layout demands).

**Why polling, not server push:** simpler, no socket reservation per pending node, recovers cleanly from initramfs reboots.

### D3. Scheduler state machine

States: `staging → transmitting → complete | failed | partial`.

- **staging** — first reimage request enters; session created with `started_at = now`; window timer set for 60s. Subsequent compatible requests (same `image_id` AND same effective `disk_layout_id`) attach to the same session. Incompatible requests open a new session.
- **transmitting** — window expired or `--multicast=require` count hit (configurable; default fire on first node if `require`); fork `udp-sender`; broadcast session descriptor to attached nodes via the polled wait endpoint.
- **complete** — `udp-sender` exited 0; receivers post-deploy report success.
- **failed** — `udp-sender` exited non-zero; all attached nodes told to fall back to unicast.
- **partial** — sender exited 0 but ≥1 receiver reported transmission failure (CRC mismatch, timeout, etc.). Failed receivers fall back to unicast individually.

**State home:** `multicast_sessions` table in clustr's SQLite, plus an in-memory `internal/multicast.Scheduler` that owns the goroutines. SQLite is the source of truth for cross-restart; the in-memory scheduler reconstructs from non-terminal rows on serverd startup (terminating any without a live goroutine as `failed`).

### D4. Schema — `multicast_sessions` table (migration NNN — see "migration number" note below)

```sql
CREATE TABLE multicast_sessions (
    id              TEXT    PRIMARY KEY,            -- UUIDv4
    image_id        TEXT    NOT NULL,
    layout_id       TEXT,                           -- nullable; matches disk_layouts.id
    state           TEXT    NOT NULL,               -- staging|transmitting|complete|failed|partial
    multicast_group TEXT    NOT NULL,               -- e.g. 239.255.42.7
    sender_port     INTEGER NOT NULL,
    rate_bps        INTEGER NOT NULL,               -- snapshot of server_config.multicast_rate_bps at start
    started_at      INTEGER NOT NULL,
    fire_at         INTEGER NOT NULL,               -- started_at + 60s, unless --multicast=require with no batch
    transmit_started_at INTEGER,
    completed_at    INTEGER,
    error           TEXT,                           -- non-empty when state in (failed, partial)
    member_count    INTEGER NOT NULL DEFAULT 0,
    success_count   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_multicast_sessions_state ON multicast_sessions(state);
CREATE INDEX idx_multicast_sessions_match ON multicast_sessions(image_id, layout_id, state);

CREATE TABLE multicast_session_members (
    session_id      TEXT    NOT NULL REFERENCES multicast_sessions(id),
    node_id         TEXT    NOT NULL,
    joined_at       INTEGER NOT NULL,
    notified_at     INTEGER,                        -- when descriptor was returned to the node
    finished_at     INTEGER,
    outcome         TEXT,                           -- success|failed|fellback_unicast
    PRIMARY KEY (session_id, node_id)
);
```

Member rows write the outcome via a new endpoint `POST /api/v1/multicast/sessions/{id}/members/{node_id}/outcome` called by the deploy agent after `udp-receiver` returns.

### D5. Rate setting: single `server_config.multicast_rate_bps`

Stored in the existing `server_config` key/value table (already used for other globals). Default **80% of 1 GbE = 100_000_000 bytes/sec ≈ 800 Mbps**. Min 10 Mbps, max 10 Gbps. Validated on write. `udp-sender` flag: `--max-bitrate 800m`. v2 may add per-image overrides; v1 is intentionally not configurable per session.

### D6. Failure modes and operator mental model

The operator's mental model is:

> "Multicast is a fast path. When it works, it's much faster than unicast. When it doesn't, the worst case is each node falls back to its own unicast download — no slower than today."

Implementation:
- **Sender-side failure** (sender exits non-zero, port collision, ENOBUFS) → state `failed` → all members notified `fallback=unicast` → each member proceeds via the existing `/blob` unicast path. Total elapsed time = 60s window + retry + unicast download. Acceptable.
- **Receiver-side packet loss above udpcast's redundancy threshold** → `udp-receiver` exits non-zero on that node → that one node reports `outcome=failed` and retries unicast. Other nodes that received the stream cleanly proceed normally. Session marked `partial`.
- **Operator visibility**: `clustr deploy --multicast=auto` prints `multicast session abc-123 → 8 nodes joined, fired at T+47s, 7/8 success, 1 fallback` so the partial outcome is obvious. UI surface deferred to a follow-up task (the UDPCast UI lives in the post-Sprint-25 backlog).

### D7. CLI flag semantics — `clustr deploy --multicast=auto|off|require`

- `auto` (default): server's normal scheduler logic — batch with peers if peers exist within 60s, otherwise fire alone or fall back to unicast based on `multicast_threshold` (server_config; default `2` — fire only if ≥2 nodes attached, else unicast).
- `off`: bypass scheduler entirely, force unicast for this request.
- `require`: refuse unicast fallback. If session can't fire (e.g. no peers within window AND threshold=2 means no fire), error out to the operator. Useful for capacity planning experiments.

The flag lives on the `clustr deploy` command (single-node initiator path) and is also accepted by `clustr group reimage` and the `POST /api/v1/node-groups/{id}/reimage` body.

---

## Privilege boundary

No new privhelper verbs. `udp-sender` runs as the unprivileged `clustr` user (the same user serverd runs as). `udp-receiver` runs in initramfs where everything is root, so no privhelper hop there either. The privhelper boundary is unchanged.

---

## Initramfs binary footprint

`udpcast` ships `udp-sender` and `udp-receiver` as standalone binaries. Static-built x86_64 size ≈ 200 KB per binary, ≈ 400 KB total. The udp-sender is **not** needed in initramfs (only the server needs it), so we ship only `udp-receiver` in initramfs — ~200 KB. Sprint 20 #120 already accommodates much larger additions (mlx5/megaraid kernel modules), so this is comfortably below the budget.

`scripts/build-initramfs.sh` `TOOL_PACKAGES` map gets one new entry: `[udp-receiver]="udpcast:udpcast"`. The builder's existing dependency-fetch path (rpm extract on the build host) handles the bundling.

---

## Phasing for Dinesh

Three commits, each independently testable. Each commit's CI must be green before the next ships.

### Commit 1 — Scheduler skeleton + DB schema, no actual transmission

- Migration `NNN_multicast_sessions.sql` (D4 above). **Migration number:** the highest committed migration on `main` at this design's HEAD is `091_pending_changes.sql`, but the parallel #160 stream is allocating `092_boot_entries.sql`. Pick the next free integer at commit time (likely `093` if #160 lands first, `092` if not). Coordinate with the #159/#160 streams via `git pull --rebase` immediately before pushing commit 1.
- `internal/multicast/scheduler.go` with `Scheduler.Enqueue(ctx, req) → SessionID`, `Wait(ctx, sessionID, nodeID) → SessionDescriptor | FallbackUnicast`. Implementation is in-memory only; tickers, state machine, and DB persistence wired in. **The "transmit" step is a 5-second `time.Sleep` stub** that flips state to `complete` without spawning udpcast.
- Endpoints `POST /api/v1/multicast/enqueue` and `GET /api/v1/multicast/sessions/{id}/wait` (long-poll, 5s window per request).
- Unit tests on the scheduler: batching logic, window expiry, member attach, state transitions. Use `testing/synctest` (Go 1.25 already in tree) so timer behaviour is deterministic.
- No CLI changes yet. No initramfs changes.

This commit lands the schema and the irreversible Type-1 decisions (D3, D4) in front of a code review where they're actually exercised in tests, before any networking complexity.

### Commit 2 — `udp-sender` integration on the server, unicast fallback wiring

- Ship `udp-sender` invocation: `internal/multicast/sender.go` `Run(ctx, session, blobReader) error`. fork/execs udp-sender with stdin = blob, captures stderr to the session's `error` column on non-zero exit.
- Wire commit 1's stub: when a session moves to `transmitting`, the scheduler now calls `sender.Run`. Session descriptor returned to attached nodes carries the real `{group, port, rate}`.
- `multicast_threshold` and `multicast_rate_bps` server_config keys with defaults.
- `clustr deploy --multicast=auto|off|require` CLI flag (cmd/clustr/main.go newDeployCmd — adds 6 lines plumbing).
- Integration test: a loopback-only test that runs udp-sender + udp-receiver on the same host through the loopback multicast path (`239.255.42.1` over `lo`). Compare md5 of input blob vs output blob. This skips on hosts without IP_MULTICAST loopback enabled — gate behind a build tag `multicast_loopback`.
- Unit tests on `sender.Run` mocking `exec.Cmd` via the existing `testutil` pattern in `internal/deploy/testutil`.

### Commit 3 — Initramfs receiver + iPXE menu split + outcome reporting

- `scripts/build-initramfs.sh` adds `udp-receiver` to TOOL_PACKAGES and the bundle.
- `scripts/initramfs-init.sh` parses `clustr.multicast=1` and `clustr.session_poll_url` from `/proc/cmdline`; passes through to the deploy binary as env vars.
- `cmd/clustr/main.go runAutoDeployMode`: when multicast env vars present, polls the wait endpoint, then on descriptor receipt fork/execs `udp-receiver --pipe "tar..."` instead of the unicast HTTP fetch. On udp-receiver non-zero exit, falls back to unicast (one retry) and reports `outcome=fellback_unicast`.
- `internal/pxe/boot.go` adds the second iPXE menu item `reimage-fleet`. New template var `MulticastEnabled` gated on `server_config.multicast_enabled` (default `true`; operators can disable for networks where multicast is broken). When disabled, only `reimage-now` is emitted.
- `POST /api/v1/multicast/sessions/{id}/members/{node_id}/outcome` endpoint.
- End-to-end qemu test: two qemu nodes share a tap bridge, both PXE-boot, both pick `reimage-fleet`, server fires session, both receivers complete. Lives under `test/multicast/qemu_test.go`. Slow (≈3 min); behind `-tags multicast_e2e`.

---

## Tests — strategy summary

| Layer | Test type | Where |
|---|---|---|
| Scheduler state machine | Unit, `synctest`-deterministic | `internal/multicast/scheduler_test.go` |
| `udp-sender` fork/exec | Unit with mocked `exec.Cmd` | `internal/multicast/sender_test.go` |
| Loopback multicast send→recv | Integration (`-tags multicast_loopback`) | `internal/multicast/loopback_test.go` |
| Two-node qemu reimage via multicast | E2E (`-tags multicast_e2e`) | `test/multicast/qemu_test.go` |
| HTTP endpoints (enqueue, wait, outcome) | `httptest` | `internal/server/handlers/multicast_test.go` |

The qemu E2E test is the only one that runs udpcast for real. It is **not** part of the default CI run — it's invoked by Gilfoyle on the cloner host (`192.168.1.151`) where `qemu-system-x86_64` plus a tap bridge is already configured for sprint validation. Documented in `SPRINT.md` as the gating check before #157 closes.

---

## Risks and irreversibility

1. **Multicast group allocation collision across restarts.** Mitigation: hash `session_id` rather than counter, and recover the in-flight set from `multicast_sessions` on serverd startup before opening any new session.
2. **The 60s window is wrong for some sites.** Probably true. Make it `server_config.multicast_window_seconds` (default 60). Reversible.
3. **iPXE menu split UX.** Whether operators choose `reimage-fleet` vs `reimage-now` correctly is a UX bet. If they don't, two follow-ups: (a) make `reimage-fleet` the default and demote `reimage-now` to an `Advanced` submenu; (b) auto-promote a single `reimage` item to multicast when the server already has ≥1 session in `staging`. Both reversible.
4. **udpcast project health.** The upstream is small but stable (last release 2024). Bundling a vendored copy is a follow-up if upstream goes dormant — not a v1 concern.
5. **`server_config.multicast_enabled` default.** Default `true`. If a customer reports L2 multicast pruning issues we flip the default; operators on broken networks disable per-site. Reversible.

The Type-1 decisions are: scheduler batching key (`image_id, layout_id`), session table schema, group allocation strategy, and the polled-wait protocol between server and initramfs. All four are pinned in commit 1 and reviewed before further code lands.
