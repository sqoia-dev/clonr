/**
 * sse-backoff.test.ts — unit tests for sseReconnectDelay (SSE-KA-1).
 *
 * Mocks Math.random to produce deterministic values at the extremes and
 * midpoint, then asserts the returned delays fall within the expected bands.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { sseReconnectDelay } from "../lib/sse-backoff"

describe("sseReconnectDelay", () => {
  beforeEach(() => {
    vi.spyOn(Math, "random")
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // Helper: run with Math.random returning a fixed value.
  function withRandom(r: number, fn: () => void) {
    vi.mocked(Math.random).mockReturnValue(r)
    fn()
  }

  it("attempt 1 — midpoint ~1000ms (random=0.5)", () => {
    withRandom(0.5, () => {
      // jitter = 0.75 + 0.5*0.5 = 1.0, base = 1000 → 1000ms
      expect(sseReconnectDelay(1)).toBe(1000)
    })
  })

  it("attempt 1 — lower bound ~750ms (random=0)", () => {
    withRandom(0, () => {
      // jitter = 0.75 + 0*0.5 = 0.75, base = 1000 → 750ms
      expect(sseReconnectDelay(1)).toBe(750)
    })
  })

  it("attempt 1 — upper bound ~1250ms (random=1)", () => {
    withRandom(1, () => {
      // jitter = 0.75 + 1*0.5 = 1.25, base = 1000 → 1250ms
      expect(sseReconnectDelay(1)).toBe(1250)
    })
  })

  it("attempt 2 — midpoint ~2000ms (random=0.5)", () => {
    withRandom(0.5, () => {
      expect(sseReconnectDelay(2)).toBe(2000)
    })
  })

  it("attempt 3 — midpoint ~4000ms (random=0.5)", () => {
    withRandom(0.5, () => {
      expect(sseReconnectDelay(3)).toBe(4000)
    })
  })

  it("attempt 4 — midpoint ~8000ms (random=0.5)", () => {
    withRandom(0.5, () => {
      expect(sseReconnectDelay(4)).toBe(8000)
    })
  })

  it("attempt 5 — midpoint ~30000ms (random=0.5)", () => {
    withRandom(0.5, () => {
      expect(sseReconnectDelay(5)).toBe(30000)
    })
  })

  it("attempt 10 — capped at 30s base (random=0.5)", () => {
    withRandom(0.5, () => {
      // Any attempt >= 5 uses the last base (30000ms).
      expect(sseReconnectDelay(10)).toBe(30000)
    })
  })

  it("attempt 5 — lower bound ~22500ms (random=0)", () => {
    withRandom(0, () => {
      // 30000 * 0.75 = 22500
      expect(sseReconnectDelay(5)).toBe(22500)
    })
  })

  it("attempt 5 — upper bound ~37500ms (random=1)", () => {
    withRandom(1, () => {
      // 30000 * 1.25 = 37500
      expect(sseReconnectDelay(5)).toBe(37500)
    })
  })

  it("delay increases monotonically at midpoint random", () => {
    vi.mocked(Math.random).mockReturnValue(0.5)
    const d1 = sseReconnectDelay(1)
    const d2 = sseReconnectDelay(2)
    const d3 = sseReconnectDelay(3)
    const d4 = sseReconnectDelay(4)
    const d5 = sseReconnectDelay(5)
    expect(d1).toBeLessThan(d2)
    expect(d2).toBeLessThan(d3)
    expect(d3).toBeLessThan(d4)
    expect(d4).toBeLessThan(d5)
  })
})
