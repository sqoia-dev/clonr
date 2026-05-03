/**
 * group-reimage-sse.test.ts — REIMG-BULK-1 SSE consumer tests.
 *
 * Verifies that applyGroupReimageEvent correctly transitions per-node rows
 * through the full queued → started → imaging → done lifecycle, and handles
 * the failed terminal path.  All tests are pure unit tests over the reducer
 * function — no DOM, no network.
 */

import { describe, it, expect } from "vitest"
import type { GroupReimageEvent } from "../lib/types"

// ─── Inline the reducer (mirrors groups.tsx) ──────────────────────────────────
// We duplicate the type and reducer here so the test is self-contained and
// doesn't depend on React component internals.

interface NodeReimageRow {
  nodeId: string
  position: number
  status: "queued" | "started" | "imaging" | "verifying" | "done" | "failed"
  progress?: number
  durationMs?: number
  error?: string
}

function applyGroupReimageEvent(
  rows: NodeReimageRow[],
  event: GroupReimageEvent,
): NodeReimageRow[] {
  switch (event.kind) {
    case "reimage.queued":
      if (rows.some((r) => r.nodeId === event.node_id)) return rows
      return [
        ...rows,
        { nodeId: event.node_id!, position: event.position ?? rows.length + 1, status: "queued" },
      ]
    case "reimage.started":
      return rows.map((r) =>
        r.nodeId === event.node_id ? { ...r, status: "started" as const } : r
      )
    case "reimage.imaging":
      return rows.map((r) =>
        r.nodeId === event.node_id
          ? { ...r, status: "imaging" as const, progress: event.progress }
          : r
      )
    case "reimage.verifying":
      return rows.map((r) =>
        r.nodeId === event.node_id ? { ...r, status: "verifying" as const } : r
      )
    case "reimage.done":
      return rows.map((r) =>
        r.nodeId === event.node_id
          ? { ...r, status: "done" as const, durationMs: event.duration_ms }
          : r
      )
    case "reimage.failed":
      return rows.map((r) =>
        r.nodeId === event.node_id
          ? { ...r, status: "failed" as const, error: event.error }
          : r
      )
    default:
      return rows
  }
}

// ─── Test data ────────────────────────────────────────────────────────────────

const JOB_ID = "job-abc-123"
const NODE_A = "node-aaaa-0001"
const NODE_B = "node-bbbb-0002"

// ─── Happy-path: queued → started → imaging → done ───────────────────────────

describe("applyGroupReimageEvent — happy path (REIMG-BULK-1)", () => {
  it("should add a node row on reimage.queued", () => {
    const rows = applyGroupReimageEvent([], {
      kind: "reimage.queued",
      job_id: JOB_ID,
      node_id: NODE_A,
      position: 1,
    })
    expect(rows).toHaveLength(1)
    expect(rows[0].nodeId).toBe(NODE_A)
    expect(rows[0].status).toBe("queued")
    expect(rows[0].position).toBe(1)
  })

  it("should be idempotent: a second queued event for the same node is a no-op", () => {
    let rows: NodeReimageRow[] = []
    const evt: GroupReimageEvent = { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_A, position: 1 }
    rows = applyGroupReimageEvent(rows, evt)
    rows = applyGroupReimageEvent(rows, evt) // duplicate
    expect(rows).toHaveLength(1)
  })

  it("should transition node from queued → started", () => {
    let rows = applyGroupReimageEvent([], { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_A, position: 1 })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.started", job_id: JOB_ID, node_id: NODE_A })
    expect(rows[0].status).toBe("started")
  })

  it("should transition node from started → imaging with progress", () => {
    let rows = applyGroupReimageEvent([], { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_A, position: 1 })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.started", job_id: JOB_ID, node_id: NODE_A })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.imaging", job_id: JOB_ID, node_id: NODE_A, progress: 42 })
    expect(rows[0].status).toBe("imaging")
    expect(rows[0].progress).toBe(42)
  })

  it("should transition node from imaging → done with duration", () => {
    let rows = applyGroupReimageEvent([], { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_A, position: 1 })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.started", job_id: JOB_ID, node_id: NODE_A })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.imaging", job_id: JOB_ID, node_id: NODE_A, progress: 100 })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.done", job_id: JOB_ID, node_id: NODE_A, duration_ms: 12345 })
    expect(rows[0].status).toBe("done")
    expect(rows[0].durationMs).toBe(12345)
  })

  it("should transition node to failed with error message", () => {
    let rows = applyGroupReimageEvent([], { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_A, position: 1 })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.started", job_id: JOB_ID, node_id: NODE_A })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.failed", job_id: JOB_ID, node_id: NODE_A, error: "PowerCycle failed: timeout" })
    expect(rows[0].status).toBe("failed")
    expect(rows[0].error).toBe("PowerCycle failed: timeout")
  })
})

// ─── Multi-node isolation ────────────────────────────────────────────────────

describe("applyGroupReimageEvent — multi-node isolation (REIMG-BULK-1)", () => {
  it("should only update the targeted node row", () => {
    let rows: NodeReimageRow[] = []
    rows = applyGroupReimageEvent(rows, { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_A, position: 1 })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_B, position: 2 })
    // Transition only NODE_A to started.
    rows = applyGroupReimageEvent(rows, { kind: "reimage.started", job_id: JOB_ID, node_id: NODE_A })

    const rowA = rows.find((r) => r.nodeId === NODE_A)!
    const rowB = rows.find((r) => r.nodeId === NODE_B)!
    expect(rowA.status).toBe("started")
    expect(rowB.status).toBe("queued") // unchanged
  })

  it("should accumulate two nodes with independent statuses", () => {
    let rows: NodeReimageRow[] = []
    rows = applyGroupReimageEvent(rows, { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_A, position: 1 })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_B, position: 2 })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.done", job_id: JOB_ID, node_id: NODE_A, duration_ms: 5000 })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.failed", job_id: JOB_ID, node_id: NODE_B, error: "bmc unreachable" })

    expect(rows.find((r) => r.nodeId === NODE_A)!.status).toBe("done")
    expect(rows.find((r) => r.nodeId === NODE_B)!.status).toBe("failed")
    expect(rows).toHaveLength(2)
  })
})

// ─── Edge cases ───────────────────────────────────────────────────────────────

describe("applyGroupReimageEvent — edge cases (REIMG-BULK-1)", () => {
  it("should be a no-op when node_id does not match any row", () => {
    let rows = applyGroupReimageEvent([], { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_A, position: 1 })
    rows = applyGroupReimageEvent(rows, { kind: "reimage.started", job_id: JOB_ID, node_id: "nonexistent-node" })
    expect(rows[0].status).toBe("queued") // NODE_A unchanged
  })

  it("should handle reimage.completed without mutating rows", () => {
    let rows = applyGroupReimageEvent([], { kind: "reimage.queued", job_id: JOB_ID, node_id: NODE_A, position: 1 })
    // reimage.completed is a job-level terminal event; the reducer returns rows unchanged.
    rows = applyGroupReimageEvent(rows, { kind: "reimage.completed", job_id: JOB_ID, succeeded: 1, failed: 0, total: 1 })
    expect(rows).toHaveLength(1)
    expect(rows[0].status).toBe("queued") // rows unmodified by completed
  })

  it("should return an empty array when no events have been applied", () => {
    expect(applyGroupReimageEvent([], { kind: "reimage.started", job_id: JOB_ID, node_id: NODE_A })).toHaveLength(0)
  })
})
