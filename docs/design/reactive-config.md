# Sprint 36 — Reactive Config Model (Design Doc)

**Status:** DESIGN — pending Richard sign-off before any code lands.
**Sprint:** 36 (`docs/SPRINT-PLAN.md` lines 776–848).
**Owner:** Richard.
**Items covered:** `CONFIG-OBSERVE` (10d), `ANCHORS` (1d).

---

## 1. Background & motivation

Today clustr's config plane is **imperative**. An operator runs `clustr deploy <node>` (or `--all`) and clustr re-renders every plugin's output for every targeted node and re-pushes the entire bundle of `InstallInstruction`s through the `config_push` WebSocket message that `internal/clientd/configapply.go` consumes. The failure mode is well understood: operators do not trust partial deploys, so they run `deploy --all` after every edit "just in case." On a 200-node lab that is a multi-minute reapply for a one-line hostname change.

The behaviour we want is the one **clustervisor** has shipped for years:

- Plugins declare which config keys they care about with a decorator: `@settings(global_observe=["network.interfaces.*", "node.hostname"])`.
- A central instruction processor (`cv_reconfigure.py`) walks the dirty-set after every config write, finds the plugins whose watched keys intersect the dirty-set, re-renders only those plugins, diffs the output against the last-rendered hash, and pushes only what changed.
- The operator never types `deploy`. Edits converge.

clustr already has the leaf pieces:

- `internal/config/` holds the cluster config tree (read-modify-write through the server).
- `internal/clientd/configapply.go` already knows how to apply a list of `InstallInstruction`s from a `config_push` WS frame (`pkg/api/types.go:215`).

What is missing is the wiring in the middle: an **observer**, a **plugin interface**, a **diff engine**, and a **scheduler**. This doc specifies those pieces plus a small companion change (`ANCHORS`) that lets two plugins share one file safely, which is the single most common reason today's plugins are forced to overwrite-the-world.

---

## 2. Decision: exact-path subscriptions, not regex

Plugins subscribe to **fully-qualified config paths** (e.g. `nodes.<id>.network.interfaces[0].ip_address`), expanded against the live tree. They do **not** subscribe to regex or glob patterns.

**Rationale:**

- **O(1) dispatch.** Every config write produces a dirty-set of fully-qualified paths. Dispatch is a hash-set intersection: `dirty ∩ watched`. Constant time per plugin.
- **Regex would force linear re-evaluation.** A regex subscription means every config write must re-evaluate every plugin's pattern against every dirty path. On a 200-node cluster with ~50 plugins the inner loop becomes 50 × dirty-set-size regex evaluations per write. Hash-set intersection is two orders of magnitude faster.
- **Empirical:** clustervisor's actual deployed `@settings(global_observe=...)` calls are ~95% literal paths; the few wildcards are small enough (`network.interfaces.*`) that they can be expanded by the framework into a per-instance exact-path subscription at plugin-registration time. We adopt this same expansion.
- **Future option:** we keep the door open for prefix subscriptions (a path tree, not a hash) if a real plugin shows up that needs them. We do **not** add regex.

**Subscription expansion rule:**
A plugin returns `WatchedKeys() []string` where each entry is either:
1. A fully-qualified path (`nodes.<node-id>.hostname`), or
2. A path with a single trailing wildcard segment (`nodes.<node-id>.network.interfaces.*`), expanded by the observer at registration time and re-expanded when the parent set changes (e.g. an interface is added).

No mid-path wildcards. No regex. No globs.

---

## 3. Plugin interface design

```go
package config

// Plugin is the unit of reactive config rendering. Implementations are
// stateless and registered once at server startup.
type Plugin interface {
    // Name is a stable identifier used in DB rows, logs, and alert labels.
    // Must be unique across all registered plugins. Convention: lowercase,
    // hyphenated, e.g. "hostname", "hosts", "sssd-conf".
    Name() string

    // WatchedKeys returns the set of config paths this plugin depends on.
    // Each entry is either a fully-qualified path or a path with a single
    // trailing "*" segment (see §2). The observer expands wildcards at
    // registration time.
    //
    // The returned slice MUST be deterministic for a given plugin version
    // (no time, no random). The observer caches the expansion.
    WatchedKeys() []string

    // Render produces the install-instructions this plugin contributes for
    // a single node, given a snapshot of the cluster state. Render MUST be:
    //
    //   - Idempotent: same input → same output, byte-for-byte.
    //   - Side-effect-free: no DB writes, no filesystem writes, no network
    //     calls, no logging at WARN/ERROR. The diff engine WILL call Render
    //     speculatively (e.g. to compute a "what would change" preview)
    //     and discard the output.
    //   - Pure-functional in the input: it must not read global state
    //     outside `state`. State is the only source of truth.
    //
    // Render returns the install-instructions this plugin would push to the
    // node. An empty slice means "this plugin contributes nothing for this
    // node" (valid — e.g. a slurm-controller plugin returns nil for compute
    // nodes).
    Render(state ClusterState, nodeID string) ([]api.InstallInstruction, error)
}
```

**Rationale for the purity contract:**

The diff engine **will** call `Render` more than once per logical "change":

1. Once to produce the candidate output for the dirty set.
2. Possibly again, speculatively, to answer "what would the next push look like?" for an operator preview UI (out of scope for v1, but the contract must allow it).
3. On startup, to rebuild the last-rendered hash for every (node, plugin) pair after a serverd restart.

If `Render` had side effects (DB writes, log mutations, anything observable), all three call-sites would need to be carefully audited every time we add one. Forbidding side effects in the contract closes that whole class of bug.

`ClusterState` is a read-only snapshot struct — defined in `internal/config/state.go` — that holds the parts of the config tree the observer has already loaded. The observer is responsible for materializing `state` once per scheduler tick, not once per plugin call.

---

## 4. DB schema

**Migration file:** `internal/db/migrations/112_config_render_state.sql`.

The number `108` quoted in `SPRINT-PLAN.md` line 808 is **stale** — `108_system_alerts.sql` was used by Sprint 38. Migration numbers 108–111 are taken. **112 is the next free integer** as of `732ede5`.

```sql
-- 112_config_render_state.sql
--
-- config_render_state tracks, per-(node, plugin), the hash of the last
-- successfully-rendered install-instruction set and when it was pushed.
-- The reactive-config diff engine uses this to short-circuit re-pushes
-- when a plugin's render output is byte-identical to the last push.
--
-- Granularity is per-(node, plugin) so two plugins that contribute to the
-- same target file (via ANCHORS) can be re-pushed independently.

CREATE TABLE IF NOT EXISTS config_render_state (
    node_id        TEXT NOT NULL,
    plugin_name    TEXT NOT NULL,
    rendered_hash  TEXT NOT NULL,           -- sha256 of canonical-JSON(instructions)
    rendered_at    TIMESTAMP NOT NULL,      -- when Render was last called
    pushed_at      TIMESTAMP,               -- NULL until the push is acked by clientd
    push_attempts  INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT,                    -- last Render or push error, NULL on success
    PRIMARY KEY (node_id, plugin_name),
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_config_render_state_pushed_at
    ON config_render_state (pushed_at);
```

**Why the hash:**
The diff engine's hot path is: render → hash → compare against `rendered_hash`. If equal, skip the push entirely. A hostname-on-node-A edit that touches plugin X but produces the same output for node B (because B doesn't depend on A) should result in **zero** outbound WS traffic to B.

**Why per-(node, plugin) granularity:**
Two plugins (`limits-slurm`, `limits-pam`) can each own a region of `/etc/security/limits.conf` via ANCHORS. We must be able to re-push one without re-pushing the other. If the row key were `(node, target_file)` we could not, and ANCHORS would be useless.

**Why store `last_error`:**
Render failures are surfaced as `system_alert`s (§7), but the per-row error column lets the operator see "this plugin has been broken for 6 hours" via a simple SQL query without joining alert history.

**Note on FK to `plugins`:** there is intentionally no `plugins` table. Plugins are code, not data; their identity is the string `Name()`. If a plugin is removed, its rows are pruned by a startup sweep that compares `SELECT DISTINCT plugin_name` against the registered set.

---

## 5. Push protocol

### 5.1 Re-use existing `config_push` WS message: **yes**.

`internal/clientd/messages.go` already defines `ConfigPushMsg` carrying `[]InstallInstruction`. Inventing a parallel `reactive_config_push` would double the surface area for no benefit — clientd's job is "apply this list of instructions," and that job does not change.

### 5.2 Per-plugin tag in the message: **yes**.

Extend `ConfigPushMsg` with a `Plugin string` field. Without it, clientd cannot tell two plugins' contributions apart, which means a render failure in plugin X invalidates the whole push (we lose failure isolation) and the ack we get back from clientd cannot update the right `config_render_state` row.

```go
// Existing (today):
type ConfigPushMsg struct {
    Instructions []api.InstallInstruction `json:"instructions"`
}

// After Sprint 36:
type ConfigPushMsg struct {
    Plugin       string                   `json:"plugin,omitempty"`        // empty = legacy full-reapply
    Instructions []api.InstallInstruction `json:"instructions"`
    RenderedHash string                   `json:"rendered_hash,omitempty"` // server echoes this back in ack
}
```

`Plugin == ""` preserves wire-compat with older clientds during the rollout (§9).

### 5.3 clientd routing

`internal/clientd/configapply.go` today walks the instruction list and dispatches per-opcode. The change is small:

1. If `Plugin == ""`, behave exactly as today (legacy path — kept until §9 deprecation).
2. If `Plugin != ""`, apply the instructions exactly as today (the per-instruction logic does not change), then emit a `config_push_ack` upstream containing `{plugin, rendered_hash, ok|err, error_message}`.

The server uses the ack to set `pushed_at = now()` and clear `last_error` on the matching `config_render_state` row. There is no "partial apply" semantics within a single plugin — a plugin's instruction list applies atomically to the file system, just like today.

**ANCHORS interaction:** when two plugins write to the same `Target` with disjoint anchors, clientd applies them in the order pushes arrive. The anchor-replace algorithm (§8) is region-local, so order does not matter for correctness — only for the timestamp on the file.

---

## 6. Concurrency / race

### 6.1 Decision: **coalesce, 50 ms debounce**, single in-process channel.

Two operator edits arriving 10 ms apart must NOT trigger two complete render-and-push cycles. We coalesce.

### 6.2 Mechanism

In `cmd/server/`, the observer owns one Go channel of `dirtyEvent` structs:

```go
type dirtyEvent struct {
    paths []string
    at    time.Time
}
```

A single goroutine — the **render scheduler** — drains the channel with a 50 ms debounce window:

1. Block on `<-dirtyCh`.
2. Start a 50 ms timer.
3. Drain any further events from `dirtyCh` until the timer fires, accumulating dirty paths into a set.
4. Compute `affectedPlugins = {p : p.WatchedKeys() ∩ accumulatedDirty ≠ ∅}`.
5. For each affected plugin × each affected node, call `Render`, hash, compare to `config_render_state.rendered_hash`, push if changed.
6. Loop.

**Rationale for coalesce:**

- **Avoids the thundering herd.** A scripted bulk-config flow (think: `for i in $(seq 1 200); do clustr node set-ip $i 10.0.0.$i; done`) would, without coalesce, produce 200 independent renders of the `network.interfaces` plugin. With a 50 ms window all 200 dirty events fall into one render pass.
- **50 ms is below operator perception** for any UI flow. An operator clicking "save" in the web UI cannot tell whether the push happened at T+5 ms or T+55 ms.
- **Bounded latency.** Worst case from edit-to-push is `50 ms + render time + WS RTT`. On a quiet cluster that's well under a second.

**Rationale for in-process channel, not a DB table:**

- The render queue is operationally ephemeral. If serverd crashes mid-render, the next startup re-runs the diff engine over all `config_render_state` rows where `rendered_hash` does not match the freshly-computed hash — the queue is reconstructed from durable state, not replayed from a log.
- A DB-backed queue would add transactional contention against the very tables we're rendering from. Not worth it.

### 6.3 Single render goroutine, not a worker pool

`Render` is pure. It is also fast (in clustervisor, p99 is <5 ms). One goroutine is enough at the scales we care about (≤ 1k nodes, ≤ 100 plugins). When that ceases to be true, we shard by `nodeID` — but **not in v1**.

---

## 7. Failure semantics

### 7.1 Per-plugin failure isolation: **yes**.

If plugin X's `Render` returns an error for node N, the scheduler:

1. Does NOT push X's output for N.
2. Sets `config_render_state.last_error` for `(N, X)`.
3. Continues rendering plugins Y, Z, … for N. **Their pushes proceed.**
4. Continues rendering X for other nodes. **Other nodes' X pushes proceed** if they succeed.

**Rationale:** a bug in one plugin must not freeze the entire cluster's config plane. The blast radius of a Render error is exactly `(plugin, node)`. This is the inverse of today's imperative model where one plugin failing aborts the whole `clustr deploy` for that node.

### 7.2 Operator surfacing

Render and push failures are emitted as `system_alert` rows (the framework Sprint 38 shipped, migration 108):

- `key = "config_render_failed"` or `"config_push_failed"`
- `device = node_id`
- `severity = "error"`
- `labels = {"plugin": plugin_name}`
- `summary = first line of Render error`
- `details = full error + last 3 attempts`

This means the existing alert UI (no new code) lights up when a plugin breaks. The alert auto-resolves when a subsequent render+push succeeds.

### 7.3 Transient vs permanent

We do **not** distinguish in v1. Every failed render is retried on the next dirty-set that touches the plugin's watched keys, plus a periodic 5-minute "retry stuck plugins" sweep. If the same `(node, plugin)` has failed >10 times, the alert severity escalates to `critical`. Anything more clever (exponential backoff, circuit-breaker) is a v2 problem.

---

## 8. ANCHORS — partial-file edits via begin/end markers

### 8.1 Spec

Per `SPRINT-PLAN.md` lines 822–848, extend `pkg/api/types.go`:

```go
type InstallInstruction struct {
    Opcode  string      `json:"opcode"`
    Target  string      `json:"target"`
    Payload string      `json:"payload"`
    Anchors *AnchorPair `json:"anchors,omitempty"` // NEW
}

type AnchorPair struct {
    Begin string `json:"begin"` // e.g. "# BEGIN clustr/limits-slurm"
    End   string `json:"end"`   // e.g. "# END clustr/limits-slurm"
}
```

`Anchors` is only honoured for `Opcode == "overwrite"`. Setting it on `modify` or `script` is a deploy-time error.

### 8.2 `applyOverwrite` algorithm

In `internal/deploy/inchroot.go`:

```
applyOverwrite(target, payload, anchors):
    if anchors == nil:
        # legacy behavior: write payload to target, replacing if present
        write_file(target, payload, mode=preserved_or_0644)
        return

    existing := read_file(target) if exists(target) else ""
    begin   := anchors.Begin
    end     := anchors.End

    block := begin + "\n" + payload + "\n" + end + "\n"

    if begin in existing and end in existing:
        # replace the region between markers, INCLUSIVE of the marker lines
        b := index_of_line(existing, begin)
        e := index_of_line(existing, end) + len(end_line)
        if b > e: error "anchors out of order in existing file"
        new := existing[:b] + block + existing[e:]
    else if begin not in existing and end not in existing:
        # neither marker present — append the block at end-of-file
        new := existing + ("" if existing.endswith("\n") else "\n") + block
    else:
        error "anchor pair half-present (one marker missing) — refusing to mutate"

    write_file(target, new, mode=preserved_or_0644)
```

**Invariants:**

- Markers are matched on whole-line equality (no substring matches), to prevent collision with payload content.
- Half-present marker pairs fail loudly. This is a sign of operator hand-editing or a previous failed apply; we refuse rather than silently corrupt.
- Region replacement is INCLUSIVE of the marker lines, so the markers always reflect the most recent payload's metadata if we ever extend `AnchorPair` with a version comment.

### 8.3 Test case shape

`internal/deploy/inchroot_test.go` adds `TestInstallInstruction_Overwrite_Anchors_TwoPluginsCoexist`:

```
1. Apply  InstallInstruction{Target: "/etc/security/limits.conf", Payload: "@slurm soft memlock unlimited",
                              Anchors: &AnchorPair{"# BEGIN clustr/limits-slurm", "# END clustr/limits-slurm"}}
2. Apply  InstallInstruction{Target: "/etc/security/limits.conf", Payload: "* hard nofile 65536",
                              Anchors: &AnchorPair{"# BEGIN clustr/limits-pam", "# END clustr/limits-pam"}}
3. Assert file contains both regions, in append order.
4. Re-apply step 1 with a NEW payload "@slurm soft memlock 1048576" (same anchors).
5. Assert: limits-slurm region updated, limits-pam region untouched.
6. Re-apply step 2 with empty payload (same anchors).
7. Assert: limits-pam markers still present with empty body, limits-slurm region untouched.
```

Edge-case tests:

- `TestApplyOverwrite_AnchorPair_HalfPresent_Fails` — file has begin marker but no end marker, expect error.
- `TestApplyOverwrite_AnchorPair_OutOfOrder_Fails` — begin appears AFTER end in existing file.
- `TestApplyOverwrite_AnchorPair_NoTrailingNewline` — existing file does not end in `\n`; verify we add one before the block.

---

## 9. Migration / rollout plan

The reactive model lands behind a feature flag (`clustr-serverd.conf: reactive_config = false` default). The flag is off until day 4.

| Day | Scope |
|---|---|
| 1 | Land observer + plugin interface + DB migration 112 + `ConfigPushMsg.Plugin` field + feature flag, default OFF. **No real plugins yet.** A no-op test plugin proves the wiring. CI green. clientd handles `Plugin != ""` messages but server never sends them. |
| 2 | Convert ONE existing plugin — recommend `hostname` — to the new interface. Keep the imperative path also rendering it (dual-write). Flip flag ON in lab; verify edits to a node's hostname produce a single targeted push, not a full reapply. Verify legacy `clustr deploy` still works (flag-independent). |
| 3 | Convert remaining plugins (hosts, sssd, slurm.conf, limits, …). Add ANCHORS to the limits-slurm + limits-pam pair (the original collision case). Keep dual-write on. |
| 4 | **Deprecate, do NOT remove,** the imperative `clustr deploy` full-reapply path. Move it behind `--force-full` and emit a deprecation warning when invoked. Flip the flag ON in production once a 7-day soak in lab passes. |

**Why dual-write through day 3:** the reactive path is being validated against a known-good imperative path. If they diverge for any (node, plugin), the diff is loud and obvious; we can fix without rolling back.

**Why we keep `clustr deploy --force-full`:** it is the operator's escape hatch for "I don't trust the state, rebuild everything." Removing it costs nothing to keep and buys trust during the transition.

---

## 10. Test plan

| File | Asserts |
|---|---|
| `internal/config/observer_test.go` | (a) Observer fires the dirty channel exactly once when a watched key is written. (b) Observer does NOT fire for unwatched keys. (c) Wildcard expansion picks up newly-added child paths. (d) Coalesce window: 200 writes in 30 ms produce one render pass. |
| `internal/config/render_diff_test.go` | (a) Identical state + identical plugin → identical hash → no push generated. (b) State change that doesn't affect a plugin's `WatchedKeys` → no re-render even if scheduler is woken. (c) Render error sets `last_error` and emits a `system_alert`. |
| `internal/config/plugin_isolation_test.go` | One of three plugins returns an error from `Render`; the other two's pushes still go out. The failing plugin's `config_render_state.last_error` is set; the others' are clear. |
| `internal/clientd/configapply_test.go` (extend) | `ConfigPushMsg{Plugin: "hostname", ...}` apply produces the expected ack with the rendered_hash echoed. Legacy `Plugin == ""` path still works byte-for-byte. |
| `internal/deploy/inchroot_test.go` (extend) | The four `Anchors` test cases enumerated in §8.3. |
| Lab E2E (manual, scripted in `scripts/e2e-reactive-config.sh`) | Edit one node's hostname via the API. Tail clientd logs on all nodes. Assert: only `hostname`, `hosts`, `sssd` plugins log a `config_push` apply on the target node. Assert: zero `config_push` traffic to unrelated nodes. |

CI gates: all unit tests run on every PR. The lab E2E runs on the cloner host post-merge, results posted to the sprint thread.

---

## 11. Open questions / explicit non-goals

### 11.1 Out of scope for v1

- **Cross-node config dependencies.** Node A's hostname change cascades into Node B's `/etc/hosts`. v1 handles this only if plugin B's `WatchedKeys()` literally lists `nodes.A.hostname` — which is fine for fixed cluster topology but does not generalize. The proper fix is a "global render context" extension where a plugin can declare a render scope wider than one node. **Follow-up sprint.**
- **Operator preview UI ("what would change if I push?").** The `Render` purity contract makes this trivially implementable later; we just don't ship UI for it now.
- **Rolling vs parallel push to nodes.** v1 pushes to all affected nodes in parallel (the existing WS hub already supports fan-out). Sequential / canary rollouts are a v2 concern; the schema and protocol do not block them.
- **Cross-cluster config diffing.** Out of scope.
- **Hot-reload of plugin code.** Plugins are compiled in. Adding a plugin requires a `clustr-serverd` restart. We are not building dynamic loading.

### 11.2 Open questions Richard must close before code starts

1. **Plugin registration:** explicit `Register(p Plugin)` calls in `cmd/server/main.go`, or `init()`-side registration via a package import? — Recommend explicit, so plugin order in tests is deterministic.
2. **Hash canonicalization:** `encoding/json` is not canonical. Do we sort `InstallInstruction` field order at hash-time, or rely on Go's struct-field order being stable? — Recommend: a small `canonicalJSON` helper that sorts instructions by `(target, opcode)` then hashes. Spec'd in observer.go's helpers.
3. **Wildcard re-expansion trigger:** when a new node is added, every `nodes.*.hostname` subscription must re-expand. Where does that live — in the observer's `OnNodeAdded` hook, or in a periodic full re-expansion sweep? — Recommend the hook (cheaper, no polling).
4. **Render timeout:** `Render` is supposed to be fast (<5 ms p99 in clustervisor). Do we wrap it in a per-call deadline? — Recommend 250 ms hard timeout, treat timeout as a Render error.
5. **Backfill on first deploy:** when a never-deployed node's clientd connects for the first time, do we render every plugin and push, or do we wait for an explicit `clustr deploy <node>` to seed? — Recommend: render-and-push every plugin on first connect; `config_render_state` is empty, so every hash differs, so everything pushes. Natural fall-out of the design.

These are **scoped questions for a follow-up review**, not blockers — recommendations are given.

---

## 12. Summary of decisions

| # | Decision | Recommendation | Confidence |
|---|---|---|---|
| 1 | Subscription model | Exact-path + single-trailing-wildcard. **No regex.** | High |
| 2 | Plugin interface purity | Side-effect-free, idempotent `Render`. | High |
| 3 | DB migration number | **112** (108 is taken by system_alerts). | High |
| 4 | DB granularity | Per-(node, plugin), not per-(node, file). | High |
| 5 | WS message | Re-use `config_push`, add `Plugin` + `RenderedHash` fields. | High |
| 6 | Concurrency | In-process channel, 50 ms debounce, single render goroutine. | High |
| 7 | Failure isolation | Per-plugin; surface as `system_alert` with `severity=error`. | High |
| 8 | ANCHORS | Whole-line marker matching, half-present pair = error, append-on-absent. | High |
| 9 | Rollout | 4-day staged with feature flag, dual-write through day 3. | Medium (lab soak length to be confirmed) |
| 10 | Imperative path | Deprecate, do NOT remove; behind `--force-full`. | High |

**Headline architectural call requiring founder sign-off before code:** **exact-path subscriptions with no regex** (decision #1). It is the load-bearing assumption that makes the diff engine O(1) per write and is the one decision that, if wrong, requires re-architecting the observer rather than swapping a knob.
