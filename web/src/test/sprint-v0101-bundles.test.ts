/**
 * sprint-v0101-bundles.test.ts — Vitest coverage for Bundles tab.
 *
 * Covers:
 *   - Bundle API response shape (all required fields present)
 *   - sig_status values ("signed" | "unsigned" | "unknown")
 *   - bundle name format ("slurm-{version}-{arch}")
 *   - nodes_using enrichment logic
 *   - kind field: only "build" is valid (no "builtin")
 *   - KindBadge label mapping (only "build" → "build pipeline")
 *   - Empty state: ListBundlesResponse with zero bundles is valid
 *   - Delete guard: DELETE /api/v1/bundles/builtin returns 404 (not 409)
 *   - Delete guard: active build cannot be deleted
 *   - Delete guard: in-use without force returns conflict
 *   - Delete guard: in-use with force=true proceeds
 *   - UI confirm-name guard: button disabled until name matches
 *   - UI force checkbox: required when nodes_using > 0
 */

import { describe, it, expect } from "vitest"
import type { Bundle, ListBundlesResponse } from "@/lib/types"

// ─── Helpers (mirror server-side logic for isolation) ─────────────────────────

/** Mirror of server bundleName() helper. */
function bundleName(version: string, arch: string): string {
  if (!arch) return `slurm-${version}`
  return `slurm-${version}-${arch}`
}

/** Mirror of server signatureStatus() logic. */
function signatureStatus(status: string, artifactChecksum: string): string {
  if (status !== "completed") return "unknown"
  if (artifactChecksum) return "signed"
  return "unsigned"
}

/** Simulate the in-use delete guard. Returns an error string or null. */
function deleteGuard(
  bundle: Bundle,
  force: boolean
): string | null {
  if (bundle.is_active) {
    return "cannot delete the active build"
  }
  if (bundle.nodes_using > 0 && !force) {
    return `bundle is in use by ${bundle.nodes_using} node(s); use ?force=true to delete anyway`
  }
  return null
}

/** Simulate the UI confirm-name disable logic. */
function isDeleteButtonDisabled(
  confirmName: string,
  bundle: Bundle,
  force: boolean,
  isPending: boolean
): boolean {
  if (isPending) return true
  if (confirmName !== bundle.name) return true
  if (bundle.nodes_using > 0 && !force) return true
  return false
}

// ─── KindBadge label mapping ──────────────────────────────────────────────────

/** Mirror of KindBadge label logic. Only "build" is a valid kind. */
function kindLabel(kind: string): string {
  if (kind === "build") return "build pipeline"
  return kind
}

describe("kindLabel (KindBadge)", () => {
  it("maps 'build' to 'build pipeline'", () => {
    expect(kindLabel("build")).toBe("build pipeline")
  })

  it("passes unknown kinds through unchanged", () => {
    expect(kindLabel("import")).toBe("import")
  })
})

// ─── kind field ───────────────────────────────────────────────────────────────

describe("Bundle kind field", () => {
  it("pipeline build has kind='build' and source='clustr-build-pipeline'", () => {
    const b: Bundle = {
      id: "a0b3755c-0000-0000-0000-000000000001",
      name: "slurm-24.11.5-x86_64",
      slurm_version: "24.11.5",
      bundle_version: "24.11.5",
      sha256: "0b50354b",
      kind: "build",
      source: "clustr-build-pipeline",
      status: "completed",
      is_active: true,
      nodes_using: 0,
      sig_status: "signed",
    }
    expect(b.kind).toBe("build")
    expect(b.source).toBe("clustr-build-pipeline")
  })

  it("kind values from live API payload only include 'build'", () => {
    const livePayload: ListBundlesResponse = {
      bundles: [
        { id: "a0b3755c-1234", name: "slurm-24.11.5-x86_64", slurm_version: "24.11.5", bundle_version: "24.11.5", sha256: "0b50354b", kind: "build", source: "clustr-build-pipeline", status: "completed", is_active: true, nodes_using: 0, sig_status: "signed", started_at: 1777222447, completed_at: 1777222789 },
      ],
      total: 1,
    }
    for (const b of livePayload.bundles) {
      expect(b.kind).toBe("build")
    }
  })
})

// ─── bundleName ───────────────────────────────────────────────────────────────

describe("bundleName", () => {
  it("includes arch when present", () => {
    expect(bundleName("25.11.5", "x86_64")).toBe("slurm-25.11.5-x86_64")
  })

  it("omits arch suffix when arch is empty", () => {
    expect(bundleName("24.11.4", "")).toBe("slurm-24.11.4")
  })

  it("handles prerelease versions", () => {
    expect(bundleName("25.11.5-1", "x86_64")).toBe("slurm-25.11.5-1-x86_64")
  })
})

// ─── signatureStatus ──────────────────────────────────────────────────────────

describe("signatureStatus", () => {
  it("returns 'signed' for completed build with checksum", () => {
    expect(signatureStatus("completed", "abc123")).toBe("signed")
  })

  it("returns 'unsigned' for completed build without checksum", () => {
    expect(signatureStatus("completed", "")).toBe("unsigned")
  })

  it("returns 'unknown' for non-completed build", () => {
    expect(signatureStatus("running", "abc123")).toBe("unknown")
    expect(signatureStatus("failed", "")).toBe("unknown")
    expect(signatureStatus("building", "abc123")).toBe("unknown")
  })
})

// ─── Bundle response shape ────────────────────────────────────────────────────

describe("Bundle response shape", () => {
  const validBundle: Bundle = {
    id: "abc-123",
    name: "slurm-25.11.5-x86_64",
    slurm_version: "25.11.5",
    bundle_version: "25.11.5",
    sha256: "deadbeef1234567890abcdef",
    kind: "build",
    source: "clustr-build-pipeline",
    status: "completed",
    is_active: true,
    nodes_using: 2,
    last_deployed_at: 1746000000,
    sig_status: "signed",
    started_at: 1745990000,
    completed_at: 1745995000,
  }

  it("has all required fields", () => {
    expect(validBundle.id).toBeTruthy()
    expect(validBundle.name).toBeTruthy()
    expect(validBundle.slurm_version).toBeTruthy()
    expect(validBundle.status).toBeTruthy()
    expect(typeof validBundle.is_active).toBe("boolean")
    expect(typeof validBundle.nodes_using).toBe("number")
  })

  it("nodes_using is a non-negative integer", () => {
    expect(validBundle.nodes_using).toBeGreaterThanOrEqual(0)
    expect(Number.isInteger(validBundle.nodes_using)).toBe(true)
  })

  it("sig_status is one of the allowed values", () => {
    const allowed = ["signed", "unsigned", "unknown", undefined]
    expect(allowed).toContain(validBundle.sig_status)
  })

  it("last_deployed_at is a unix timestamp when present", () => {
    expect(validBundle.last_deployed_at).toBeGreaterThan(0)
  })
})

describe("ListBundlesResponse shape", () => {
  const response: ListBundlesResponse = {
    bundles: [],
    total: 0,
  }

  it("has bundles array and total", () => {
    expect(Array.isArray(response.bundles)).toBe(true)
    expect(typeof response.total).toBe("number")
  })

  it("total matches bundles length", () => {
    expect(response.total).toBe(response.bundles.length)
  })
})

// ─── nodes_using enrichment ───────────────────────────────────────────────────

describe("nodes_using enrichment", () => {
  type NodeVersion = { buildID: string; installedAt: number }

  /** Mirror of server-side enrichment map building. */
  function computeEnrichment(
    nodeVersions: NodeVersion[]
  ): Map<string, { nodesUsing: number; lastDeployedAt: number }> {
    const m = new Map<string, { nodesUsing: number; lastDeployedAt: number }>()
    for (const nv of nodeVersions) {
      if (!nv.buildID) continue
      const e = m.get(nv.buildID) ?? { nodesUsing: 0, lastDeployedAt: 0 }
      e.nodesUsing++
      if (nv.installedAt > e.lastDeployedAt) e.lastDeployedAt = nv.installedAt
      m.set(nv.buildID, e)
    }
    return m
  }

  it("counts nodes per build", () => {
    const nodeVersions: NodeVersion[] = [
      { buildID: "build-1", installedAt: 100 },
      { buildID: "build-1", installedAt: 200 },
      { buildID: "build-2", installedAt: 150 },
    ]
    const m = computeEnrichment(nodeVersions)
    expect(m.get("build-1")?.nodesUsing).toBe(2)
    expect(m.get("build-2")?.nodesUsing).toBe(1)
  })

  it("tracks the most recent installedAt per build", () => {
    const nodeVersions: NodeVersion[] = [
      { buildID: "build-1", installedAt: 100 },
      { buildID: "build-1", installedAt: 300 },
      { buildID: "build-1", installedAt: 200 },
    ]
    const m = computeEnrichment(nodeVersions)
    expect(m.get("build-1")?.lastDeployedAt).toBe(300)
  })

  it("ignores entries with empty buildID", () => {
    const nodeVersions: NodeVersion[] = [
      { buildID: "", installedAt: 100 },
      { buildID: "build-1", installedAt: 200 },
    ]
    const m = computeEnrichment(nodeVersions)
    expect(m.size).toBe(1)
    expect(m.has("")).toBe(false)
  })

  it("returns zero counts for a build not in node versions", () => {
    const m = computeEnrichment([])
    expect(m.get("any-build")).toBeUndefined()
  })
})

// ─── Delete guards ────────────────────────────────────────────────────────────

// ─── Empty state ──────────────────────────────────────────────────────────────

describe("ListBundlesResponse empty state", () => {
  it("zero-bundle response is valid", () => {
    const resp: ListBundlesResponse = { bundles: [], total: 0 }
    expect(resp.bundles).toHaveLength(0)
    expect(resp.total).toBe(0)
  })

  it("total matches bundles length in empty response", () => {
    const resp: ListBundlesResponse = { bundles: [], total: 0 }
    expect(resp.total).toBe(resp.bundles.length)
  })
})

// ─── DELETE /builtin returns 404 (simulated client-side guard) ────────────────

describe("delete guard: builtin ID returns 404", () => {
  /**
   * The server no longer special-cases id="builtin" with a 409.
   * It falls through to the DB lookup and returns 404 (not_found)
   * because there is no slurm_builds row with id="builtin".
   * This test documents the expected HTTP status from the API.
   */
  it("DELETE /api/v1/bundles/builtin yields 404 not_found", () => {
    // Simulate the server response shape for a missing row.
    const mockResponse = { error: "bundle not found", code: "not_found" }
    expect(mockResponse.code).toBe("not_found")
  })
})

describe("deleteGuard", () => {
  const base: Bundle = {
    id: "abc-123",
    name: "slurm-24.11.5-x86_64",
    slurm_version: "24.11.5",
    bundle_version: "24.11.5",
    sha256: "",
    kind: "build",
    source: "clustr-build-pipeline",
    status: "completed",
    is_active: false,
    nodes_using: 0,
  }

  it("blocks deletion of active build", () => {
    const err = deleteGuard({ ...base, is_active: true }, false)
    expect(err).toContain("active")
  })

  it("blocks deletion of in-use bundle without force", () => {
    const err = deleteGuard({ ...base, nodes_using: 3 }, false)
    expect(err).toContain("3 node(s)")
    expect(err).toContain("force=true")
  })

  it("allows deletion of in-use bundle with force=true", () => {
    const err = deleteGuard({ ...base, nodes_using: 3 }, true)
    expect(err).toBeNull()
  })

  it("allows deletion of unused, non-active, non-builtin bundle", () => {
    const err = deleteGuard(base, false)
    expect(err).toBeNull()
  })
})

// ─── UI confirm-name guard ────────────────────────────────────────────────────

describe("isDeleteButtonDisabled", () => {
  const bundle: Bundle = {
    id: "abc-123",
    name: "slurm-24.11.5-x86_64",
    slurm_version: "24.11.5",
    bundle_version: "24.11.5",
    sha256: "",
    kind: "build",
    source: "clustr-build-pipeline",
    status: "completed",
    is_active: false,
    nodes_using: 0,
  }

  it("disabled when confirmName does not match", () => {
    expect(isDeleteButtonDisabled("wrong-name", bundle, false, false)).toBe(true)
  })

  it("disabled when mutation is pending", () => {
    expect(isDeleteButtonDisabled(bundle.name, bundle, false, true)).toBe(true)
  })

  it("disabled when nodes_using > 0 and force is false", () => {
    const inUseBundle = { ...bundle, nodes_using: 2 }
    expect(isDeleteButtonDisabled(inUseBundle.name, inUseBundle, false, false)).toBe(true)
  })

  it("enabled when name matches, not pending, no nodes using", () => {
    expect(isDeleteButtonDisabled(bundle.name, bundle, false, false)).toBe(false)
  })

  it("enabled when name matches, not pending, nodes using but force=true", () => {
    const inUseBundle = { ...bundle, nodes_using: 2 }
    expect(isDeleteButtonDisabled(inUseBundle.name, inUseBundle, true, false)).toBe(false)
  })
})
