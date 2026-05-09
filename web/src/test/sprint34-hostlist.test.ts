/**
 * sprint34-hostlist.test.ts — HOSTLIST parser unit tests (Sprint 34 UI-A)
 *
 * Mirrors the round-trip table from docs/SPRINT-PLAN.md §HOSTLIST acceptance criteria.
 * Compatible with internal/selector/hostlist.go semantics.
 */

import { describe, it, expect } from "vitest"
import { expandHostlist, compressHostnames } from "../lib/hostlist"

// ─── expandHostlist ───────────────────────────────────────────────────────────

describe("expandHostlist — basic", () => {
  it("plain hostname (no brackets) → single item", () => {
    expect(expandHostlist("compute01")).toEqual(["compute01"])
  })

  it("empty string → empty array", () => {
    expect(expandHostlist("")).toEqual([])
    expect(expandHostlist("  ")).toEqual([])
  })

  it("single-item bracket → single result", () => {
    expect(expandHostlist("compute[01]")).toEqual(["compute01"])
  })

  it("contiguous range", () => {
    expect(expandHostlist("node[01-12]")).toEqual([
      "node01","node02","node03","node04","node05","node06",
      "node07","node08","node09","node10","node11","node12",
    ])
  })

  it("preserves zero-padding from start token", () => {
    expect(expandHostlist("node[001-003]")).toEqual(["node001","node002","node003"])
  })

  it("single-digit range (no padding)", () => {
    expect(expandHostlist("gpu[1-4]")).toEqual(["gpu1","gpu2","gpu3","gpu4"])
  })

  it("comma-separated list (no ranges)", () => {
    expect(expandHostlist("node[01,03,05]")).toEqual(["node01","node03","node05"])
  })

  it("mixed range and single items", () => {
    expect(expandHostlist("compute[01-04,10]")).toEqual([
      "compute01","compute02","compute03","compute04","compute10",
    ])
  })

  it("multi-segment mixed: node[01-04,10,20-21]", () => {
    expect(expandHostlist("node[01-04,10,20-21]")).toEqual([
      "node01","node02","node03","node04","node10","node20","node21",
    ])
  })

  it("out-of-order items (comma list, not range)", () => {
    expect(expandHostlist("node[03,01,02]")).toEqual(["node03","node01","node02"])
  })

  it("large range does not OOM — node[001-128]", () => {
    const result = expandHostlist("node[001-128]")
    expect(result).toHaveLength(128)
    expect(result[0]).toBe("node001")
    expect(result[127]).toBe("node128")
  })

  it("suffix after brackets is preserved", () => {
    expect(expandHostlist("rack[1-2]-pdu")).toEqual(["rack1-pdu","rack2-pdu"])
  })
})

// ─── expandHostlist — error cases ─────────────────────────────────────────────

describe("expandHostlist — error cases", () => {
  it("throws on empty brackets []", () => {
    expect(() => expandHostlist("node[]")).toThrow(/empty bracket/)
  })

  it("throws on unmatched open bracket", () => {
    expect(() => expandHostlist("node[01")).toThrow(/unmatched bracket/)
  })

  it("throws on unmatched close bracket", () => {
    expect(() => expandHostlist("node01]")).toThrow(/unmatched bracket/)
  })

  it("throws on invalid range [a-]", () => {
    expect(() => expandHostlist("node[a-]")).toThrow()
  })

  it("throws on reversed range [12-01]", () => {
    expect(() => expandHostlist("node[12-01]")).toThrow(/range end < start/)
  })

  it("throws on non-numeric range token", () => {
    expect(() => expandHostlist("node[abc-def]")).toThrow()
  })
})

// ─── compressHostnames ────────────────────────────────────────────────────────

describe("compressHostnames — basic", () => {
  it("empty list → empty string", () => {
    expect(compressHostnames([])).toBe("")
  })

  it("single hostname", () => {
    expect(compressHostnames(["compute01"])).toBe("compute01")
  })

  it("contiguous run → range", () => {
    expect(compressHostnames(["node01","node02","node03"])).toBe("node[01-03]")
  })

  it("non-contiguous items → comma list in brackets", () => {
    expect(compressHostnames(["node01","node03","node05"])).toBe("node[01,03,05]")
  })

  it("mixed run + singles", () => {
    const result = compressHostnames(["compute01","compute02","compute03","compute10"])
    expect(result).toBe("compute[01-03,10]")
  })

  it("preserves zero-padding", () => {
    expect(compressHostnames(["node001","node002","node003"])).toBe("node[001-003]")
  })

  it("plain hostnames without numeric suffix — returned as-is", () => {
    expect(compressHostnames(["controller"])).toBe("controller")
  })
})

// ─── round-trip ───────────────────────────────────────────────────────────────

describe("expandHostlist / compressHostnames — round-trip", () => {
  const cases = [
    "node[01-12]",
    "gpu[001-128]",
    "compute[01-04,10]",
    "node[03,01,02]",
    "rack[1-4]",
  ] as const

  for (const pattern of cases) {
    it(`round-trips "${pattern}"`, () => {
      const expanded = expandHostlist(pattern)
      const compressed = compressHostnames(expanded)
      const reExpanded = expandHostlist(compressed)
      expect(new Set(reExpanded)).toEqual(new Set(expanded))
    })
  }
})
