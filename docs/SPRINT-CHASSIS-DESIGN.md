# Sprint 31 — Multi-node enclosure support in the datacenter model

**Status:** Design — go/no-go decision pending founder review
**Author:** Richard
**Date:** 2026-05-03
**Source:** founder gap "datacenter does not respect other node rack types like blade servers"
**Task IDs:** continue from #244 (last assigned in SPRINT-30 follow-on) → start at #260 to leave runway
**Verdict (TL;DR):** Ship it as **Sprint 31, parallel to Sprint 30** (different files, different owners, near-zero merge surface). Use **Path A** (separate `enclosures` table, mutually-exclusive `rack_id` vs `enclosure_id` on `node_rack_position`). Ship 4 canned enclosure types, positional slots (1..N) with per-type slot-orientation metadata, no operator-defined types, no chassis-shared switch / chassis-BMC / chassis power. One sprint. Forward-looking — no lab hardware, unit tests + visual stubs only.

---

## TL;DR for the founder

The current rack model treats every node as a 1U–4U pizza box that occupies contiguous rack units directly in a rack. That assumption is built into one column on one table (`node_rack_position.slot_u`). Blade chassis, 2U twins, GPU shelves, and half-width 1U all share the same shape: **a parent enclosure occupies N rack units, exposes M slots, each slot may hold a node.** That parent–slot indirection is one new table and one nullable column on the existing one.

The clean call is Path A: a new `enclosures` table, a nullable `enclosure_id` + `slot_index` on `node_rack_position`, and a CHECK constraint that exactly one of `(rack_id, slot_u)` or `(enclosure_id, slot_index)` is populated. The polymorphic Path B is overengineered for v1. The NodeGroup-reuse Path C conflates physical topology with logical policy and is a trap. Path A keeps the existing rack-direct path bit-identical (no migration of existing rows beyond a column add) and adds a parallel placement path for enclosure-resident nodes.

Slots are positional integers (1..N) with a per-enclosure-type orientation hint (`horizontal`, `vertical`, `grid_2x2`, `grid_2x4`) so the renderer knows how to lay out the inner cells. We ship 4 canned enclosure types in v1 (1U-half-width-2-slot, 2U-twin-2-slot, 2U-blade-4-slot, 4U-quad-4-slot), hard-coded as a Go map keyed by type ID. Operator-defined types are out of scope — most operators run one or two chassis families per cluster, and a closed enum gets us to ship in one sprint.

Sprint 31 runs parallel to Sprint 30 because the file overlap is tiny (`internal/db/racks.go`, the racks handler, and `web/src/routes/datacenter.tsx` — none of which Sprint 30 touches). One engineer (Dinesh) on the data model + UI; one ~2-day arc for Gilfoyle on the migration test + RPM packaging notes; the rest is the React work in `datacenter.tsx`. Eight tasks. Ship as v0.11.0.

This is a forward-looking feature — we have no blade hardware in the lab. The ship gate is unit tests + visual storybook-style stubs in the running webapp + selector-grammar regression. End-to-end lab validation is an explicit known-unvalidated until hardware lands. That's an acceptable tradeoff because the data model is the load-bearing decision, and that we *can* test exhaustively without hardware.

---

## 1. Data model — Path A wins

### What exists today

```
racks(id, name, height_u, created_at, updated_at)

node_rack_position(node_id PK → node_configs.id,
                   rack_id  → racks.id,
                   slot_u   INTEGER NOT NULL,   -- bottom-most U occupied
                   height_u INTEGER NOT NULL DEFAULT 1)
```

One node → one rack position. `slot_u` is a 1-based integer in the rack's U inventory. Overlap detection is advisory (read-after-write, returns a warning string), not a constraint.

### The three paths I evaluated

#### Path A — separate `enclosures` table, mutually-exclusive parent on `node_rack_position`

```
enclosures(
    id                TEXT PRIMARY KEY,
    rack_id           TEXT NOT NULL REFERENCES racks(id) ON DELETE CASCADE,
    rack_slot_u       INTEGER NOT NULL,      -- bottom-most U the chassis occupies
    height_u          INTEGER NOT NULL,      -- how many U the chassis is
    type_id           TEXT NOT NULL,         -- 'blade-2u-4slot', 'twin-2u-2slot', etc.
    label             TEXT,                  -- operator-supplied chassis name (optional)
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

CREATE INDEX idx_enclosures_rack ON enclosures(rack_id);

-- node_rack_position gains two nullable columns; existing rows are untouched.
ALTER TABLE node_rack_position ADD COLUMN enclosure_id TEXT
    REFERENCES enclosures(id) ON DELETE CASCADE;
ALTER TABLE node_rack_position ADD COLUMN slot_index INTEGER;

-- enforce exactly-one-parent invariant via trigger (SQLite ALTER TABLE ADD CHECK
-- is restricted; trigger is the portable path):
CREATE TRIGGER node_rack_position_xor_parent_insert
BEFORE INSERT ON node_rack_position
BEGIN
    SELECT CASE
        WHEN (NEW.rack_id IS NOT NULL AND NEW.enclosure_id IS NOT NULL)
          OR (NEW.rack_id IS NULL     AND NEW.enclosure_id IS NULL)
        THEN RAISE(ABORT, 'node_rack_position: exactly one of rack_id/enclosure_id required')
    END;
END;
-- (mirror UPDATE trigger)
```

- **Pros:** existing rack-direct rows have NULL `enclosure_id`/`slot_index` and behave identically; new enclosure-resident rows have NULL `rack_id`/`slot_u` and join to `enclosures` for their physical position; `node_rack_position` stays the single source of truth for "where is this node," which means selectors and the existing handler logic don't fork; the cardinality is clear (one node → one position, ever); migration is one ADD COLUMN + one new table + a trigger.
- **Cons:** two nullable column pairs on the same table is mildly ugly; the trigger isn't as clean as a CHECK constraint (SQLite has limited ALTER ADD CHECK support and rebuilding the table is invasive); query joins for "list all nodes in rack X including those in enclosures in rack X" need a UNION or a LEFT JOIN through `enclosures`.
- **Migration cost:** ADD COLUMN x2 + new table + new trigger + new index. Zero existing rows touched. Safe to run live.

#### Path B — polymorphic `rack_items` table

Single table with a `kind` enum (`node`, `enclosure`, `reservation`). Enclosures are rows. Nodes-directly-in-rack are rows with `parent_id=NULL, rack_id=X`. Nodes-in-enclosure are rows with `parent_id=enclosure_row_id, rack_id=NULL`.

- **Pros:** one query renders the entire rack ("SELECT * FROM rack_items WHERE rack_id=X OR parent IN (SELECT id FROM rack_items WHERE rack_id=X)"); semantically pure; extensible to `kind=reservation` (a U-block reserved for future hardware) which is a real future feature.
- **Cons:** **invasive migration** — every existing `node_rack_position` row gets transformed into a `rack_items` row with `kind=node`. Touches every node placement in the system. The rack handler, the selector resolver, the unassigned-nodes query, and the React layer all change. One bug in the migration loses placement state. Higher blast radius for a feature that has zero customers asking for the polymorphism today.
- **Verdict:** REJECTED for v1. Reconsider only if (a) we add reservations *and* (b) the union-of-parents query in Path A becomes a real perf bottleneck (it won't — tens of racks × tens of items per rack is negligible).

#### Path C — `NodeGroup` with `group_kind=enclosure`

Reuse the existing `node_groups` + `node_group_memberships` tables. Add `group_kind` enum (`logical | enclosure`). Add `slot_index` to membership row. No new tables.

- **Pros:** zero new tables; the existing group APIs already enumerate members.
- **Cons:** **conflates two unrelated concepts.** A NodeGroup today is a *logical/policy* construct — disk layout overrides, LDAP restrictions, expiry timestamps, group-managers, etc. (see `pkg/api/types.go:70` and migrations 011, 021, 042, 043, 048, 068, 070). An enclosure is a *physical/topological* construct that has zero policy semantics and one hard constraint (a slot holds at most one node). Forcing both through one table means every NodeGroup query gets a `WHERE group_kind=...` clause forever, the UI has to filter group lists by kind everywhere, and the next feature ("an enclosure can have a chassis-level BMC") immediately wants columns that have no NodeGroup analog. Worse: when an operator drag-renames an enclosure, that's not a "rename a group" operation — there's no policy implication. The semantics drift apart immediately.
- **Verdict:** REJECTED. This is the kind of "save a table" move that costs us six months of code-review gymnastics. Physical topology and logical policy are different problems.

### Path A — interaction with the rest of the system

**Existing rack rendering** (`web/src/routes/datacenter.tsx`, 1143 LOC, `RackTile` at line 489): `RackTile` today iterates `rack.positions[]` and renders each `NodeBlock` at `slot_u`. After the change: it iterates `rack.positions[]` (rack-direct nodes, unchanged) AND `rack.enclosures[]` (a new field on the `Rack` shape) and renders each enclosure as an `EnclosureBlock` at `enclosure.rack_slot_u` with internal slot cells. Existing pizza-box rendering is untouched. New code path is additive.

**Selector grammar** (`internal/selector/selector.go:54`): `--chassis` is already declared with empty-fallback semantics ("Chassis names — resolved after #138 lands"). This sprint resolves it. Add `ListNodeIDsByEnclosureLabels(ctx, labels []string)` to `SelectorDB` and wire it through `Resolve`. `--chassis enc01,enc02` becomes a real selector. No new flag.

**Deploy/reimage path**: deploy is always per-node (the deploy/reimage paths take `node_id`, never enclosure_id). An enclosure has no deploy semantics — it doesn't run anything; it's a passive physical container. We will NOT add a "deploy to enclosure" surface. The existing `--chassis blade-chassis-01` selector expands to "all nodes in slots of enclosure blade-chassis-01" and the existing per-node deploy fan-out handles the rest. Same pattern as `--racks`. Zero changes to the deploy code path.

**Verdict on Path A:** ship it. Smallest blast radius of the three, fully forward-compatible (we can promote to Path B in a year if reservations show up and queries actually get awkward), and the trigger is the only mildly clever part — and the trigger is testable in isolation.

---

## 2. Slot taxonomy — positional integers + per-type orientation hint

### Positional vs named

**Positional (1..N) wins.** Reasons:
- Vendors disagree on naming conventions (Supermicro labels Twin nodes A/B, Dell PowerEdge FX2 labels 1a/1b/2a/2b, HPE BladeSystem c7000 uses 1..16). Picking one nomenclature alienates the other vendors.
- Positional integers map cleanly to a stable database column (`slot_index INTEGER`) and to a deterministic UI render order.
- The label ("top-left", "blade-A") is presentational — it can be derived from `(slot_index, orientation)` in the UI without storing it.

Slot indices are 1-based to match the rack `slot_u` convention. Slot 1 is the canonical "first" slot defined by the orientation rule (see below).

### Orientation hint as part of the enclosure type

The renderer needs to know how to lay out the inner slot grid. Encode this on the type, not per-instance:

| Orientation | Layout rule | Slot 1 position |
|---|---|---|
| `horizontal` | Slots laid out left→right in a single row | Leftmost |
| `vertical` | Slots laid out top→bottom in a single column | Topmost |
| `grid_2x2` | 2 columns × 2 rows | Top-left, then row-major |
| `grid_2x4` | 2 columns × 4 rows | Top-left, then row-major |

This covers our v1 canned types and the foreseeable next two (`grid_4x2`, `grid_4x4`). When we add a 7-blade horizontal chassis someone will want `grid_7x1` — that's literally one new enum value and one switch case in the renderer.

### Canned types vs operator-defined — canned wins for v1

Ship 4 hard-coded enclosure types in v1, defined as a Go map in `internal/enclosures/types.go`:

```go
type EnclosureType struct {
    ID            string  // 'blade-2u-4slot'
    DisplayName   string  // '2U Blade Chassis (4 slots)'
    HeightU       int     // 2
    SlotCount     int     // 4
    Orientation   string  // 'horizontal'
    Description   string  // free text for the picker
}

var Catalog = map[string]EnclosureType{
    "halfwidth-1u-2slot": {HeightU: 1, SlotCount: 2, Orientation: "horizontal"},
    "twin-2u-2slot":      {HeightU: 2, SlotCount: 2, Orientation: "horizontal"},
    "blade-2u-4slot":     {HeightU: 2, SlotCount: 4, Orientation: "horizontal"},
    "quad-4u-4slot":      {HeightU: 4, SlotCount: 4, Orientation: "grid_2x2"},
}
```

The catalog is exposed via `GET /api/v1/enclosure-types` so the UI can render the type picker. Validation on enclosure create/update: `type_id` must exist in `Catalog`. Slot count is read from `Catalog[type_id].SlotCount` — never stored on the enclosure row, never operator-supplied. Eliminates an entire class of "operator put 5 nodes in a 4-slot chassis" bugs.

**Why not operator-defined types in v1?** Three reasons:
1. **Scope.** A type editor is its own UI surface (form, validation, name-uniqueness, deletion-with-references-check). That's half a sprint by itself.
2. **Demand.** We have no customer asking for "let me define a custom 6-slot chassis." The four canned types cover Supermicro Twin/TwinPro, HPE Synergy half-blades, Dell FX2 quarter-width sleds, and the half-width 1U niche. ~95% of HPC enclosure hardware in production today.
3. **Cost of being wrong.** If a customer needs a custom type next quarter, we add it to the Go map and ship a point release. That's a 30-minute change. The cost of shipping a half-baked type editor and then needing to evolve it under a deprecation contract is much higher.

Ship canned. Add an operator-defined type system in v0.12.0 or later, only if a real customer asks.

---

## 3. Scope cut — confirming the founder's instinct

**IN scope for v0.11.0:**
- Schema: `enclosures` table + `node_rack_position.enclosure_id`/`slot_index` columns + XOR trigger
- Backend: enclosures CRUD (GET/POST/GET-one/PUT/DELETE), set/delete node-in-slot, list-by-rack-with-enclosures
- 4 canned enclosure types in `internal/enclosures/types.go` + `GET /api/v1/enclosure-types`
- Selector: `--chassis` resolves via enclosure label (replacing the empty-fallback stub)
- UI: render enclosure as a distinct bordered block in the rack tile, with internal slot cells; empty slot = drop target
- UI: drag matrix below (§4) — full coverage
- UI: "Add chassis" button in the rack header → type picker → places a new enclosure at the specified U
- Tests: unit tests for the trigger (every illegal combination of NULL/non-NULL across the two parent pairs), unit tests for the selector, integration test for the handler endpoints

**OUT of scope for v0.11.0** (the founder's list, confirmed):
- Operator-defined custom enclosure types (canned only)
- Chassis-shared network topology (the "blade chassis has one switch upstream" modeling)
- Enclosure-level power management (per-chassis PDU control, "power off the entire chassis")
- Chassis-level BMC aggregation (some Supermicro 2U Twin chassis expose a chassis-management BMC that owns all node BMCs — this is a real feature, but requires its own BMC discovery + auth path)
- Enclosure templates / cloning / bulk-create
- Drag-to-reorder slots within an enclosure (slot index is positional, not draggable)
- Reservations (Path B's `kind=reservation` future use case)
- Lab hardware validation (no blade chassis on hand)

**Why these are out:** each one is a self-contained sprint. Network-shared topology touches `internal/network/`, `internal/switch/`, and the discovery path. Chassis power touches `internal/power/` and adds a new API surface. Chassis BMC touches `internal/bios/` and `internal/ipmi/`. None of these block the data-model decision; all of them can layer onto Path A's schema cleanly when their respective sprints come.

The shipping criterion is **operators can model their physical inventory accurately** — not "operators can manage their physical inventory." Modeling first, management after.

---

## 4. UX coexistence with the existing rack model — drag matrix

### Rendering rules

- A rack tile renders both `positions[]` (rack-direct nodes) and `enclosures[]` (chassis blocks) at their respective `slot_u`.
- An enclosure block has a clearly distinct visual treatment: solid 2px border (versus 1px for direct nodes), corner badge with the type icon, internal slot grid sized to `slot_count` and laid out per `orientation`.
- An empty slot inside an enclosure renders as a dashed-border drop target with the slot index ("Slot 1") as a hint.
- A filled slot renders the same `NodeBlock` component used in rack-direct positions, but constrained to the slot's bounding box (it does NOT span U-positions; the slot owns its visual real estate).
- Enclosure occupies its full `height_u` in the rack — no rack-direct node can be placed inside the U range that the enclosure occupies (overlap detection extended to consider enclosure bounding boxes).

### Drag matrix (full coverage, founder's list confirmed)

| From | To | Behavior | Atomic? |
|---|---|---|---|
| Unassigned sidebar | Rack U position | Existing — POST `/api/v1/racks/{id}/positions/{node_id}` with `slot_u`, `height_u` | Yes, single endpoint |
| Unassigned sidebar | Enclosure slot | NEW — POST `/api/v1/enclosures/{id}/slots/{slot_index}` with `node_id` | Yes, single endpoint |
| Rack U position | Enclosure slot | NEW — single endpoint `POST /api/v1/nodes/{node_id}/placement` accepts `{kind: "enclosure_slot", enclosure_id, slot_index}` and atomically swaps the parent within one transaction | **Yes, single endpoint** — backend handles the unassign+assign in one tx; no UI orchestration |
| Enclosure slot | Enclosure slot (same chassis) | Same `/api/v1/nodes/{node_id}/placement` endpoint with new `slot_index` | Yes |
| Enclosure slot | Enclosure slot (different chassis) | Same endpoint with new `enclosure_id, slot_index` | Yes |
| Enclosure slot | Rack U position | Same `/api/v1/nodes/{node_id}/placement` endpoint with `{kind: "rack_u", rack_id, slot_u, height_u}` | Yes |
| Enclosure slot | Unassigned | DELETE `/api/v1/nodes/{node_id}/placement` (replaces the existing `DELETE /api/v1/racks/{id}/positions/{node_id}` semantically — but we keep the old endpoint for compat, see migration note) | Yes |

**Critical UX decision:** introduce a unified `/api/v1/nodes/{node_id}/placement` endpoint that subsumes "where this node lives." The body is a tagged union of `{kind: "rack_u", ...}` or `{kind: "enclosure_slot", ...}`. This collapses the drag-source × drag-target combinatorics from N×M endpoints into one, makes atomicity explicit (one transaction, no half-moves), and gives the UI one mutation hook to subscribe to.

The legacy `PUT /api/v1/racks/{id}/positions/{node_id}` and `DELETE /api/v1/racks/{id}/positions/{node_id}` endpoints stay for one release (v0.11.0), forwarding to the new unified endpoint internally, and are deleted in v0.12.0. CLI continues to work unchanged during the deprecation window.

### Optimistic UI

The existing `datacenter.tsx` does optimistic positions updates with rollback on failure. Same pattern applies to enclosure-slot moves. The unified placement endpoint returns the full new placement record, and the React Query cache is invalidated for both `racks` (positions changed) and `enclosures` (slot occupancy changed).

### Empty-state guidance

- A rack with zero nodes and zero enclosures: existing copy ("No nodes assigned yet — drag from the sidebar.") plus new line: "Or click 'Add chassis' to place a multi-node enclosure."
- An enclosure with zero filled slots: corner copy "Empty chassis — drag a node into a slot."
- The "Add chassis" affordance lives in the rack-tile header, not in a global menu, so it's contextual to where the chassis will go.

---

## 5. Sprint 31 plan (parallel to Sprint 30)

**Theme:** model multi-node enclosures (blade chassis, twins, GPU shelves, half-width) as first-class citizens in the rack inventory.
**Estimated duration:** 5–7 working days, one engineer (Dinesh) primary, Gilfoyle ~2 days for migration test + RPM, Richard arch reviews on PRs.
**Parallelism with Sprint 30:** safe. File overlap matrix:

| File / area | Sprint 30 (split) | Sprint 31 (chassis) |
|---|---|---|
| `cmd/clustr-serverd/main.go` | YES (mode flag) | NO |
| `internal/server/server.go` | YES (route wiring change for builder client) | YES (route wiring for new enclosure endpoints) |
| `internal/builder/` (new) | YES | NO |
| `internal/db/racks.go` | NO | YES (extended) |
| `internal/db/enclosures.go` (new) | NO | YES |
| `internal/server/handlers/racks.go` | NO | YES (extended for `enclosures` field) |
| `internal/server/handlers/enclosures.go` (new) | NO | YES |
| `internal/server/handlers/placement.go` (new) | NO | YES |
| `internal/selector/selector.go` | NO | YES (chassis resolution) |
| `web/src/routes/datacenter.tsx` | NO | YES |
| `internal/db/migrations/097_*.sql` | NO | YES |
| systemd units, autodeploy script | YES | NO |

`internal/server/server.go` is the only meaningful overlap and it's only the route registration (~5 lines each). Trivial merge. Sprint 30 lands first by a few days because it's older; Sprint 31 rebases.

### Tasks (continue numbering from #244 last assigned in Sprint 30 follow-on → start #260)

- [ ] **#260 — Migration 097: enclosures table + node_rack_position columns + XOR trigger (HIGH, S)**
  Owner: Dinesh.
  In: `internal/db/migrations/097_enclosures.sql`. New `enclosures` table per §1; ADD COLUMN `enclosure_id`, `slot_index` to `node_rack_position`; install BEFORE INSERT and BEFORE UPDATE triggers enforcing the XOR invariant; add `idx_enclosures_rack` and `idx_node_rack_position_enclosure` indices. NO data migration — existing rows have NULL enclosure_id/slot_index and stay valid under the trigger.
  Depends on: nothing.
  DoD: migration runs cleanly on a fresh DB and on a live cloner DB; unit test attempts every illegal NULL combination (both NULL, both non-NULL, INSERT and UPDATE) and confirms the trigger rejects.

- [ ] **#261 — `internal/enclosures/types.go` canned catalog + `pkg/api` types (HIGH, S)**
  Owner: Dinesh.
  In: new `internal/enclosures/types.go` exporting `Catalog map[string]EnclosureType` with the four canned types per §2. New types in `pkg/api/types.go`: `Enclosure`, `EnclosureSlot` (slot_index + node_id-or-null), `ListEnclosuresResponse`, `EnclosureType`, `ListEnclosureTypesResponse`. Extend `Rack` with `Enclosures []Enclosure` populated when `?include=enclosures` is passed.
  Depends on: nothing (parallel with #260).
  DoD: Go types compile, JSON marshal round-trip test passes for every enclosure type in the catalog.

- [ ] **#262 — `internal/db/enclosures.go` CRUD + slot occupancy (HIGH, M)**
  Owner: Dinesh.
  In: new `enclosures.go` with `CreateEnclosure`, `GetEnclosure`, `ListEnclosuresByRack`, `UpdateEnclosure`, `DeleteEnclosure` (cascades to slot occupancy via FK), `SetSlotOccupancy(enclosureID, slotIndex, nodeID)`, `ClearSlotOccupancy(enclosureID, slotIndex)`, `ListSlotsByEnclosure(enclosureID)`. Validation: slot_index must be in `[1, Catalog[type_id].SlotCount]`; enclosure must fit in rack (`rack_slot_u + height_u - 1 <= rack.height_u`); enclosure must not overlap other enclosures or rack-direct nodes in the same rack.
  Depends on: #260, #261.
  DoD: unit tests for every CRUD path, every validation rule, every overlap case; -race clean.

- [ ] **#263 — `internal/server/handlers/enclosures.go` HTTP surface (HIGH, M)**
  Owner: Dinesh.
  In: new handler with routes `GET /api/v1/enclosure-types`, `GET /api/v1/racks/{rack_id}/enclosures`, `POST /api/v1/racks/{rack_id}/enclosures`, `GET /api/v1/enclosures/{id}`, `PUT /api/v1/enclosures/{id}`, `DELETE /api/v1/enclosures/{id}`, `POST /api/v1/enclosures/{id}/slots/{slot_index}`, `DELETE /api/v1/enclosures/{id}/slots/{slot_index}`. Extend `RacksHandler.ListRacks`/`GetRack` to populate `Enclosures` field when `include=enclosures` is in the query. All write endpoints validate against `internal/enclosures.Catalog`.
  Depends on: #262.
  DoD: integration tests for every route; 4xx error cases covered (invalid type_id, slot_index out of range, slot already occupied, enclosure overlap with rack-direct node, enclosure overflows rack).

- [ ] **#264 — Unified `/api/v1/nodes/{node_id}/placement` endpoint (HIGH, M)**
  Owner: Dinesh.
  In: new `internal/server/handlers/placement.go` with `PUT /api/v1/nodes/{node_id}/placement` (body: tagged union of `rack_u` or `enclosure_slot`) and `DELETE /api/v1/nodes/{node_id}/placement`. Atomic transaction inside one DB call: clear current placement, insert new placement, all under one `BEGIN`/`COMMIT`. Old `PUT/DELETE /api/v1/racks/{id}/positions/{node_id}` endpoints stay and forward internally for one release; deprecation log line + `Sunset:` HTTP header per RFC 8594.
  Depends on: #263.
  DoD: integration test exercises every cell of the §4 drag matrix; concurrent-placement test confirms one tx wins, the other gets a clean 409 Conflict.

- [ ] **#265 — Selector: `--chassis` resolution (MEDIUM, S)**
  Owner: Dinesh.
  In: extend `SelectorDB` interface in `internal/selector/selector.go` with `ListNodeIDsByEnclosureLabels(ctx, labels []string) ([]NodeID, error)`. Implement in `internal/db/enclosures.go`. Wire into `Resolve` for the `--chassis` selector (replaces the current empty-fallback noop). Update the flag help text — drop the "resolved after #138 lands" stub.
  Depends on: #262.
  DoD: unit test with a mix of chassis labels, some valid some unknown; CLI smoke test `clustr exec --chassis blade-chassis-01 -- hostname` shows expected behavior against a fixture DB.

- [ ] **#266 — Datacenter UI: render enclosures + drag-into-slot (HIGH, L)**
  Owner: Dinesh.
  In: extend `web/src/routes/datacenter.tsx`. New `EnclosureBlock` component rendered alongside `NodeBlock` inside `RackTile`. Internal slot grid uses CSS grid keyed off `orientation`. New `SlotDropZone` variant for enclosure slots (similar to existing rack `SlotDropZone` but with `enclosure_id` + `slot_index` instead of `rack_id` + `slot_u`). Update the shared `DndContext` `handleDragEnd` to dispatch to either the rack-position endpoint (legacy path during deprecation window) or the unified placement endpoint (preferred — feature-flag this behind an `?api=v2` query param for the first 24 hours of cloner soak, then flip to default). New "Add chassis" button in rack tile header → opens type picker modal → POSTs to `/api/v1/racks/{rack_id}/enclosures`. New "Edit chassis" / "Delete chassis" affordances on `EnclosureBlock` corner.
  Depends on: #261, #263, #264.
  DoD: every cell of the §4 drag matrix works against a real two-rack two-enclosure fixture on cloner; visual stub for each of the 4 canned enclosure types renders correctly; empty slot is a clearly-affordant drop target; SSE updates from a concurrent placement change reflect within 2s.

- [ ] **#267 — End-to-end test + visual stub fixture (MEDIUM, M)**
  Owner: Dinesh.
  In: new `test/integration/enclosures/` with a Go test that fans through: create rack → create enclosure of each canned type → place a node in each slot → move a node from rack-U into a slot → move it back → delete the enclosure (verify cascade clears slot occupancy). Add a developer-only fixture script `scripts/seed-enclosure-demo.sh` that seeds one of every canned type into a rack so we have a visual stub for each layout in the running webapp without needing real hardware.
  Depends on: #266.
  DoD: green in CI on at least 5 consecutive runs; `seed-enclosure-demo.sh` produces a screenshot-ready datacenter view on a fresh cloner DB.

- [ ] **#268 — Docs + CHANGELOG + RPM packaging notes (LOW, S)**
  Owner: Gilfoyle + Dinesh.
  In: append to `CHANGELOG.md` a clear "v0.11.0 — multi-node enclosure support" section enumerating the four canned types, the new endpoints, and the deprecation of `/api/v1/racks/{id}/positions/{node_id}`. RPM packaging unchanged (no new files outside the binary). Confirm `clustr-serverd doctor` exits clean against the new schema (it shouldn't break — but verify).
  Depends on: #267.
  DoD: CHANGELOG section reviewed; doctor clean on cloner; tag candidate v0.11.0-rc1.

### Sprint 31 ship gate

- All 9 tasks landed. CI green on `main`.
- Migration applies cleanly on cloner's live DB; zero existing rows touched; XOR trigger blocks every illegal placement combination.
- `seed-enclosure-demo.sh` produces a visual rendering of every canned enclosure type in the cloner webapp.
- Drag matrix manually walked through end-to-end on cloner (10-minute checklist) — every cell works.
- `--chassis blade-chassis-01` resolves to expected node IDs in CLI smoke test.
- **NOT a ship gate:** lab validation against actual blade hardware. Explicitly deferred until hardware lands. v0.11.0 ships as "forward-looking enclosure support, model only, validated against fixture data."

### What v0.11.0 does NOT promise

- Power management of an entire chassis
- Chassis-level BMC discovery
- Network topology awareness for blade-shared switches
- Operator-defined custom enclosure types
- Reservations / planned-capacity rows in the rack
- Field-validated correctness against actual blade hardware

These each get their own future sprint when demand or hardware lands. The v0.11.0 release notes call this out explicitly so operators know what to expect.

---

## 6. Cross-product applicability

**Does the enclosure-as-parent-of-nodes pattern share code with NodeGroup?**

No, and this matters. NodeGroup is **logical/policy**: it answers "which nodes share this disk layout, this LDAP restriction, this expiry, this manager?" Membership is dynamic, often computed (group memberships have an LDAP-restriction policy filter), and a single node can belong to multiple groups. The cardinality is many-to-many.

Enclosure is **physical/topological**: it answers "which physical chassis does this node live in?" Membership is static (a node is in one slot at a time, the slot is in one chassis at a time, the chassis is in one rack at a time). A node belongs to exactly one enclosure at a time, or zero. The cardinality is many-to-one.

These have different invariants (NodeGroup has no "max members" constraint; enclosure slot has cardinality-1; enclosure has fixed slot count from its type), different lifecycles (groups are renamed/expired/restricted; enclosures are physical metal that gets racked/unracked), and different consumers (groups feed into the deploy policy engine and LDAP module; enclosures feed into the datacenter render and the `--chassis` selector). Sharing a table is the kind of premature consolidation that costs us velocity later.

**Where the pattern DOES generalize:** the parent-with-fixed-slots shape recurs in two future features I can already see coming:

1. **Switch port modeling.** A network switch is "one parent with N fixed slots, each slot may hold a node connection." Same shape as an enclosure. When the network module gets switch-port modeling (already on the roadmap per `internal/network/`), the same tagged-parent pattern in `node_rack_position` could extend to `node_switch_port_assignment` with the same XOR-trigger discipline. Don't build it now — but the architectural pattern transfers.

2. **PDU outlet modeling.** A PDU is "one parent with N outlets, each outlet may be assigned to a node." Same shape. Same future.

The right way to express this is "Path A is the template for any physical-parent-with-fixed-slots topology," not "merge enclosures with NodeGroup." The future templates inherit the trigger discipline and the tagged-parent column pattern, not the table.

**Net opinion:** keep enclosure separate from NodeGroup permanently. Document the Path A pattern as the canonical shape for future physical-topology features. Reject any future PR that proposes folding switch ports or PDU outlets into NodeGroup for the same reason we reject it here.

---

## 7. Where this fits relative to Sprint 30

**Recommendation: Sprint 31 runs in parallel to Sprint 30, with Sprint 30 having priority on shared review bandwidth.**

Reasoning:

- File overlap is genuinely tiny (§5 matrix). The only shared file is `internal/server/server.go` for route registration, and that's a 5-line change on each side.
- Sprint 30 is the bigger architectural bet (control/builder split, RPC contract, systemd units, autodeploy rewrite) and deserves Richard's primary review attention. Sprint 31 is mostly schema + handler + UI, which is well-trodden territory for this codebase.
- Sprint 30 has a 7-day soak gate; Sprint 31 has no soak gate (no hardware to validate against). They naturally serialize on the *release* timeline even if they parallelize on the *development* timeline — Sprint 30 ships as v0.y, Sprint 31 ships as v0.y+1 = v0.11.0 once Sprint 30 is in.
- If Sprint 30 slips (it might — ARCH-1 is a big sprint), Sprint 31 ships independently as v0.11.0 against the current monolithic clustr-serverd. The two are decoupled in the releasable-unit sense.

**Could we do Sprint 31 first?** Yes, technically. It's smaller and lower-risk. But Sprint 30 has been the official "next big thing" since the design landed, and the autodeploy pain it solves is currently active operator pain. Sprint 31 is forward-looking (no customer is currently blocked on enclosure support). Don't reshuffle the priority.

**If Dinesh is fully consumed by Sprint 30:** push Sprint 31 to the next sprint slot. A two-week delay on enclosure support has zero customer impact today.

**If Sprint 30 ships early:** Sprint 31 is the natural next sprint, drops in immediately, ships as v0.11.0 within ~7 days of Sprint 30's v0.y.

---

## 8. Risks

| Risk | Likelihood | Severity | Mitigation |
|---|---|---|---|
| XOR trigger logic wrong → silent invalid placements | LOW | HIGH | exhaustive unit tests (#260 DoD); CHECK alternative documented if SQLite version permits |
| Existing rack-direct rendering regresses | MEDIUM | HIGH | additive code path; existing `NodeBlock` in `RackTile` untouched; manual smoke against cloner before merge |
| Drag matrix UX feels confusing with new enclosure block visual | MEDIUM | MEDIUM | clear visual differentiation (border weight, corner badge); empty-slot copy; visual stub fixture (#267) lets us iterate before customer sees it |
| Lab-unvalidated assumption about real blade BMC discovery breaks v1.x BMC features | LOW | MEDIUM | v0.11.0 explicitly does NOT model chassis BMC; when real hardware lands and we add it, the schema is additive |
| Operator wants a 5th enclosure type next month | MEDIUM | LOW | 30-min Go map addition + point release; documented in §2 |
| `--chassis` selector behavior surprises CLI users (was empty-noop, now resolves) | LOW | LOW | explicit CHANGELOG callout; `--chassis foo` against an unknown label returns clear error not silent empty |
| Unified `/api/v1/nodes/{node_id}/placement` endpoint takes longer to design well than estimated | MEDIUM | MEDIUM | tagged union schema is well-trodden; if it slips by >2 days, ship v0.11.0 with the legacy rack endpoints + new enclosure-slot endpoints separately, defer the unification to v0.11.1 |
| Conflict with Sprint 30 on `internal/server/server.go` route table | LOW | LOW | rebase Sprint 31 on top of Sprint 30 at merge; conflicts are mechanical (route registration list) |
| Migration 097 breaks on a deployed cluster with weird existing data | LOW | MEDIUM | dry-run on cloner DB before tagging release; migration is ADD COLUMN + new table, both safe operations |

---

## 9. Recommendation

**Do it. Path A. Sprint 31, parallel to Sprint 30.** Estimated 5–7 working days for Dinesh, ~2 days for Gilfoyle, Richard reviews on PRs.

Path A captures the full modeling power needed for blade chassis, twins, GPU shelves, and half-width nodes with the smallest possible blast radius. Existing rack-direct placements are bit-identical post-migration. The XOR trigger is testable in isolation. The four canned enclosure types cover ~95% of HPC enclosure hardware in production today. Operator-defined types are explicitly deferred — when a real customer asks, that's a 30-minute Go map addition.

The unified `/api/v1/nodes/{node_id}/placement` endpoint is the most interesting architectural choice in this sprint. It collapses the drag matrix combinatorics, makes atomicity explicit, and gives us a clean deprecation path off the legacy rack-position endpoints. If it threatens to slip, ship the enclosure-slot endpoints separately and defer the unification — it's a v0.11.1 add, not a blocker.

The forward-looking aspect (no lab hardware to validate against) is acceptable because the data model is the load-bearing decision and we *can* test it exhaustively without hardware. The features that genuinely need hardware (chassis BMC, chassis power, blade-switch topology) are explicitly deferred to future sprints. v0.11.0 ships as "operators can model their multi-node enclosures accurately," not "operators can manage them" — and that's the correct line for a one-sprint, hardware-less release.

Reassess at Sprint 31 retro:
- If a customer asks for chassis BMC or chassis power → next sprint (v0.12.0 candidate)
- If operators ask for custom enclosure types → next sprint
- If the unified placement endpoint behavior is confusing in production → tighten docs, do not roll back

— Richard
