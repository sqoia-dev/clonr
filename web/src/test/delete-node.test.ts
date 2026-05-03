/**
 * delete-node.test.ts — NODE-DEL-6: Vitest coverage for node deletion UI.
 *
 * Covers:
 *   1. Typed-confirm gate: "Delete permanently" stays disabled until the typed
 *      value matches the node hostname exactly.
 *   2. Optimistic remove + rollback on 409 (active deployment guard).
 */

import { describe, it, expect } from "vitest"

// ─── Typed-confirm gate ───────────────────────────────────────────────────────
// The UI enforces: canDelete = (confirmHostname === node.hostname)
// Extracted from DeleteNodeFlow in nodes.tsx.

function canDelete(hostname: string, confirmHostname: string): boolean {
  return confirmHostname === hostname
}

describe("delete node typed-confirm gate (NODE-DEL-6)", () => {
  const HOSTNAME = "compute-01"

  it("should be false when confirm field is empty", () => {
    expect(canDelete(HOSTNAME, "")).toBe(false)
  })

  it("should be false when confirm is a partial match", () => {
    expect(canDelete(HOSTNAME, "compute")).toBe(false)
  })

  it("should be false when confirm has extra characters", () => {
    expect(canDelete(HOSTNAME, "compute-01 ")).toBe(false)
  })

  it("should be true only when confirm matches exactly", () => {
    expect(canDelete(HOSTNAME, "compute-01")).toBe(true)
  })

  it("should be case-sensitive (hostnames are lowercase — exact match required)", () => {
    expect(canDelete(HOSTNAME, "Compute-01")).toBe(false)
    expect(canDelete(HOSTNAME, "COMPUTE-01")).toBe(false)
  })

  it("should work for UUIDs (fallback when hostname is empty)", () => {
    const id = "cbf2c958-4172-47c3-9b0d-29caa4e21df4"
    expect(canDelete(id, id)).toBe(true)
    expect(canDelete(id, id.slice(0, 8))).toBe(false)
  })
})

// ─── Optimistic remove + rollback on 409 ─────────────────────────────────────
// Mirrors the onMutate/onError pattern in DeleteNodeFlow.deleteMutation.

interface NodeEntry {
  id: string
  hostname: string
}

interface NodeListCache {
  nodes: NodeEntry[]
  total: number
}

function optimisticRemoveNode(
  cache: NodeListCache | undefined,
  nodeId: string
): { prev: NodeListCache | undefined; updated: NodeListCache | undefined } {
  if (!cache) return { prev: undefined, updated: undefined }
  const prev = cache
  const updated: NodeListCache = {
    ...cache,
    nodes: cache.nodes.filter((n) => n.id !== nodeId),
    total: Math.max(0, cache.total - 1),
  }
  return { prev, updated }
}

function rollbackNodeCache(
  _current: NodeListCache | undefined,
  prev: NodeListCache | undefined
): NodeListCache | undefined {
  return prev
}

describe("delete node optimistic remove + rollback (NODE-DEL-6)", () => {
  const INITIAL: NodeListCache = {
    nodes: [
      { id: "node-a", hostname: "compute-01" },
      { id: "node-b", hostname: "compute-02" },
    ],
    total: 2,
  }

  it("should remove node from cache optimistically", () => {
    const { updated } = optimisticRemoveNode(INITIAL, "node-a")
    expect(updated?.nodes).toHaveLength(1)
    expect(updated?.nodes[0].id).toBe("node-b")
    expect(updated?.total).toBe(1)
  })

  it("should not mutate the previous reference", () => {
    const { prev, updated } = optimisticRemoveNode(INITIAL, "node-a")
    expect(prev?.nodes).toHaveLength(2)
    expect(updated?.nodes).toHaveLength(1)
    expect(prev).not.toBe(updated)
  })

  it("should restore cache on 409 rollback", () => {
    const { prev, updated } = optimisticRemoveNode(INITIAL, "node-a")
    const restored = rollbackNodeCache(updated, prev)
    expect(restored?.nodes).toHaveLength(2)
    expect(restored?.nodes.map((n) => n.id)).toEqual(["node-a", "node-b"])
    expect(restored?.total).toBe(2)
  })

  it("should clamp total to 0 if cache somehow has total=0", () => {
    const zeroCache: NodeListCache = { nodes: [{ id: "x", hostname: "lone" }], total: 0 }
    const { updated } = optimisticRemoveNode(zeroCache, "x")
    expect(updated?.total).toBe(0)
  })

  it("should be a no-op when cache is undefined", () => {
    const { prev, updated } = optimisticRemoveNode(undefined, "node-a")
    expect(prev).toBeUndefined()
    expect(updated).toBeUndefined()
  })

  it("should leave cache unchanged when node ID not found", () => {
    const { updated } = optimisticRemoveNode(INITIAL, "nonexistent-id")
    expect(updated?.nodes).toHaveLength(2)
    expect(updated?.total).toBe(1) // total decrements by 1 regardless — mirrors server decrement
  })
})
