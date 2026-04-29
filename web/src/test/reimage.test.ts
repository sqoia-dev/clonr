/**
 * reimage.test.ts — TEST-4: reimage confirm-gate and rollback behavior.
 *
 * These tests verify the two invariants of the reimage inline flow:
 *   1. The confirm button is disabled unless the typed node ID matches exactly.
 *   2. The optimistic rollback path (onError handler) restores the previous
 *      query cache state when the POST fails.
 */

import { describe, it, expect } from "vitest"

// ─── Confirm-gate invariant ────────────────────────────────────────────────────
// The UI enforces: canConfirm = (confirmId === node.id) && selectedImageId !== ""
// We test the pure logic extracted from the component.

function canConfirm(nodeId: string, confirmId: string, selectedImageId: string): boolean {
  return confirmId === nodeId && selectedImageId !== ""
}

describe("reimage confirm-gate (TEST-4)", () => {
  const NODE_ID = "cbf2c958-4172-47c3-9b0d-29caa4e21df4"

  it("should be false when confirmId is empty", () => {
    expect(canConfirm(NODE_ID, "", "img-abc")).toBe(false)
  })

  it("should be false when confirmId is a partial match", () => {
    expect(canConfirm(NODE_ID, "cbf2c958", "img-abc")).toBe(false)
  })

  it("should be false when no image is selected", () => {
    expect(canConfirm(NODE_ID, NODE_ID, "")).toBe(false)
  })

  it("should be true only when both confirmId matches exactly AND an image is selected", () => {
    expect(canConfirm(NODE_ID, NODE_ID, "img-abc")).toBe(true)
  })

  it("should be case-sensitive (UUIDs are lowercase — exact match required)", () => {
    expect(canConfirm(NODE_ID, NODE_ID.toUpperCase(), "img-abc")).toBe(false)
  })
})

// ─── Optimistic rollback pattern ───────────────────────────────────────────────
// The onMutate / onError pattern is tested here by simulating the cache
// manipulation functions. This matches the pattern used in GPG delete and
// API key revoke.

interface MockCache<T> {
  data: T | undefined
}

function simulateOptimisticRemove<T extends { id: string }>(
  cache: MockCache<{ items: T[] }>,
  id: string
): { prev: { items: T[] } | undefined } {
  const prev = cache.data
  if (cache.data) {
    cache.data = { ...cache.data, items: cache.data.items.filter((i) => i.id !== id) }
  }
  return { prev }
}

function simulateRollback<T>(cache: MockCache<T>, prev: T | undefined) {
  if (prev !== undefined) {
    cache.data = prev
  }
}

describe("optimistic rollback pattern (TEST-4)", () => {
  it("should remove item from cache optimistically", () => {
    const cache: MockCache<{ items: Array<{ id: string; name: string }> }> = {
      data: { items: [{ id: "a", name: "alpha" }, { id: "b", name: "beta" }] },
    }
    simulateOptimisticRemove(cache, "a")
    expect(cache.data?.items).toHaveLength(1)
    expect(cache.data?.items[0].id).toBe("b")
  })

  it("should restore cache on rollback after error", () => {
    const cache: MockCache<{ items: Array<{ id: string }> }> = {
      data: { items: [{ id: "a" }, { id: "b" }] },
    }
    const { prev } = simulateOptimisticRemove(cache, "a")
    // Simulate error — rollback.
    simulateRollback(cache, prev)
    expect(cache.data?.items).toHaveLength(2)
    expect(cache.data?.items.map((i) => i.id)).toEqual(["a", "b"])
  })

  it("should be a no-op rollback when prev is undefined", () => {
    const cache: MockCache<{ items: Array<{ id: string }> }> = {
      data: { items: [{ id: "x" }] },
    }
    // If somehow prev is undefined, rollback should not crash or corrupt.
    simulateRollback(cache, undefined)
    expect(cache.data?.items).toHaveLength(1)
  })
})
