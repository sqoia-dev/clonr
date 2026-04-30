/**
 * sprint11-slurm.test.ts — Sprint 11 Vitest coverage for Slurm surface (#85).
 *
 * Covers:
 *   - Build form validation (version format, flags parsing)
 *   - SSE log consumer logic (event parsing, deduplication)
 *   - Config editor save flow validation (empty content, empty filename)
 *   - Role bulk-edit validation (valid roles, empty list)
 *   - fmtBytes helper
 *   - SlurmValidateResponse issue rendering logic
 *   - SlurmBuild status badge class mapping
 *   - SlurmUpgradeOperation phase ordering
 */

import { describe, it, expect } from "vitest"
import type {
  SlurmBuild,
  SlurmValidateResponse,
  SlurmValidationIssue,
  SlurmUpgradeOperation,
  SlurmBuildLogEvent,
} from "@/lib/types"

// ─── fmtBytes (extracted from slurm.tsx for isolation) ───────────────────────

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1048576) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1073741824) return `${(n / 1048576).toFixed(1)} MB`
  return `${(n / 1073741824).toFixed(1)} GB`
}

describe("fmtBytes", () => {
  it("formats bytes below 1 KB", () => {
    expect(fmtBytes(0)).toBe("0 B")
    expect(fmtBytes(512)).toBe("512 B")
    expect(fmtBytes(1023)).toBe("1023 B")
  })

  it("formats kilobytes", () => {
    expect(fmtBytes(1024)).toBe("1.0 KB")
    expect(fmtBytes(2048)).toBe("2.0 KB")
    expect(fmtBytes(1536)).toBe("1.5 KB")
  })

  it("formats megabytes", () => {
    expect(fmtBytes(1048576)).toBe("1.0 MB")
    expect(fmtBytes(5242880)).toBe("5.0 MB")
  })

  it("formats gigabytes", () => {
    expect(fmtBytes(1073741824)).toBe("1.0 GB")
    expect(fmtBytes(2147483648)).toBe("2.0 GB")
  })
})

// ─── Build form validation ────────────────────────────────────────────────────

/** Validates a Slurm build version string. */
function validateBuildVersion(version: string): { ok: boolean; error?: string } {
  if (!version.trim()) return { ok: false, error: "Version is required" }
  // Slurm versions: MAJOR.MINOR.PATCH (e.g. 24.11.4)
  if (!/^\d+\.\d+\.\d+$/.test(version.trim())) {
    return { ok: false, error: "Version must be in MAJOR.MINOR.PATCH format (e.g. 24.11.4)" }
  }
  return { ok: true }
}

/** Parses a configure-flags string into an array, filtering blank entries. */
function parseConfigureFlags(raw: string): string[] {
  return raw
    .split(/\s+/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0)
}

describe("build form validation", () => {
  it("rejects empty version", () => {
    const r = validateBuildVersion("")
    expect(r.ok).toBe(false)
    expect(r.error).toContain("required")
  })

  it("rejects version with missing patch", () => {
    expect(validateBuildVersion("24.11").ok).toBe(false)
  })

  it("rejects non-numeric version segments", () => {
    expect(validateBuildVersion("24.11.x").ok).toBe(false)
    expect(validateBuildVersion("latest").ok).toBe(false)
  })

  it("accepts valid MAJOR.MINOR.PATCH version", () => {
    expect(validateBuildVersion("24.11.4").ok).toBe(true)
    expect(validateBuildVersion("23.02.1").ok).toBe(true)
  })

  it("parses configure flags correctly", () => {
    expect(parseConfigureFlags("--with-pmix --enable-pam")).toEqual(["--with-pmix", "--enable-pam"])
  })

  it("filters blank configure flag entries", () => {
    expect(parseConfigureFlags("  --with-pmix   ")).toEqual(["--with-pmix"])
    expect(parseConfigureFlags("")).toEqual([])
  })

  it("handles multi-whitespace between flags", () => {
    expect(parseConfigureFlags("--a  --b\t--c")).toEqual(["--a", "--b", "--c"])
  })
})

// ─── SSE log consumer logic ───────────────────────────────────────────────────

/** Parses a raw SSE data line into a SlurmBuildLogEvent. Returns null on parse failure. */
function parseBuildLogEvent(data: string): SlurmBuildLogEvent | null {
  try {
    const parsed = JSON.parse(data)
    if (typeof parsed.build_id !== "string") return null
    return parsed as SlurmBuildLogEvent
  } catch {
    return null
  }
}

/** Deduplicates consecutive identical log lines. */
function deduplicateLogs(events: SlurmBuildLogEvent[]): SlurmBuildLogEvent[] {
  return events.filter((e, i) => i === 0 || e.line !== events[i - 1].line)
}

describe("SSE log consumer", () => {
  it("parses valid build log event", () => {
    const raw = JSON.stringify({ build_id: "abc123", line: "make install" })
    const evt = parseBuildLogEvent(raw)
    expect(evt).not.toBeNull()
    expect(evt?.build_id).toBe("abc123")
    expect(evt?.line).toBe("make install")
  })

  it("returns null for malformed JSON", () => {
    expect(parseBuildLogEvent("not json")).toBeNull()
  })

  it("returns null for missing build_id field", () => {
    expect(parseBuildLogEvent(JSON.stringify({ line: "foo" }))).toBeNull()
  })

  it("deduplicates consecutive identical lines", () => {
    const events: SlurmBuildLogEvent[] = [
      { build_id: "a", line: "step 1" },
      { build_id: "a", line: "step 1" },
      { build_id: "a", line: "step 2" },
    ]
    const deduped = deduplicateLogs(events)
    expect(deduped).toHaveLength(2)
    expect(deduped[0].line).toBe("step 1")
    expect(deduped[1].line).toBe("step 2")
  })

  it("preserves non-consecutive duplicate lines", () => {
    const events: SlurmBuildLogEvent[] = [
      { build_id: "a", line: "step 1" },
      { build_id: "a", line: "step 2" },
      { build_id: "a", line: "step 1" },
    ]
    expect(deduplicateLogs(events)).toHaveLength(3)
  })

  it("handles empty event list", () => {
    expect(deduplicateLogs([])).toEqual([])
  })
})

// ─── Config editor save flow ──────────────────────────────────────────────────

/** Validates a config save request before sending. */
function validateConfigSave(filename: string, content: string): { ok: boolean; error?: string } {
  if (!filename.trim()) return { ok: false, error: "Filename is required" }
  if (!content.trim()) return { ok: false, error: "Config content must not be empty" }
  return { ok: true }
}

describe("config editor save flow validation", () => {
  it("rejects empty filename", () => {
    const r = validateConfigSave("", "ClusterName=clustr")
    expect(r.ok).toBe(false)
    expect(r.error).toContain("Filename")
  })

  it("rejects empty content", () => {
    const r = validateConfigSave("slurm.conf", "   ")
    expect(r.ok).toBe(false)
    expect(r.error).toContain("content")
  })

  it("accepts valid filename and content", () => {
    expect(validateConfigSave("slurm.conf", "ClusterName=clustr").ok).toBe(true)
  })

  it("accepts cgroup.conf filename", () => {
    expect(validateConfigSave("cgroup.conf", "CgroupPlugin=cgroup/v2").ok).toBe(true)
  })
})

// ─── Role bulk-edit validation ────────────────────────────────────────────────

const VALID_ROLES = ["controller", "worker", "dbd", "login"] as const

/** Validates a role set for bulk assignment. */
function validateRoleBulkEdit(roles: string[]): { ok: boolean; invalid?: string[] } {
  const invalid = roles.filter((r) => !(VALID_ROLES as readonly string[]).includes(r))
  if (invalid.length > 0) return { ok: false, invalid }
  return { ok: true }
}

/** Returns the set union of roles across multiple nodes. */
function getRoleUnion(nodeRoles: string[][]): string[] {
  const seen = new Set<string>()
  for (const roles of nodeRoles) {
    for (const r of roles) seen.add(r)
  }
  return Array.from(seen).sort()
}

describe("role bulk-edit validation", () => {
  it("accepts valid role set", () => {
    expect(validateRoleBulkEdit(["controller", "worker"]).ok).toBe(true)
  })

  it("accepts single valid role", () => {
    expect(validateRoleBulkEdit(["login"]).ok).toBe(true)
  })

  it("rejects unknown role", () => {
    const r = validateRoleBulkEdit(["controller", "supercomputer"])
    expect(r.ok).toBe(false)
    expect(r.invalid).toContain("supercomputer")
  })

  it("accepts empty role list (unassign all)", () => {
    // Empty list is valid — means clear all roles from selected nodes.
    expect(validateRoleBulkEdit([]).ok).toBe(true)
  })

  it("computes role union correctly", () => {
    const union = getRoleUnion([["controller", "worker"], ["worker", "login"]])
    expect(union).toEqual(["controller", "login", "worker"])
  })

  it("deduplicates roles in union", () => {
    const union = getRoleUnion([["worker"], ["worker"]])
    expect(union).toEqual(["worker"])
  })
})

// ─── SlurmValidateResponse issue rendering ───────────────────────────────────

/** Returns a summary string for a validate response. */
function summarizeValidation(resp: SlurmValidateResponse): string {
  if (resp.valid) return "valid"
  const errors = resp.issues.filter((i) => i.severity === "error").length
  const warnings = resp.issues.filter((i) => i.severity === "warning").length
  const parts: string[] = []
  if (errors > 0) parts.push(`${errors} error${errors !== 1 ? "s" : ""}`)
  if (warnings > 0) parts.push(`${warnings} warning${warnings !== 1 ? "s" : ""}`)
  return parts.join(", ") || "invalid"
}

describe("SlurmValidateResponse issue rendering", () => {
  it("returns 'valid' for passing response", () => {
    const resp: SlurmValidateResponse = { filename: "slurm.conf", valid: true, issues: [] }
    expect(summarizeValidation(resp)).toBe("valid")
  })

  it("counts errors correctly", () => {
    const resp: SlurmValidateResponse = {
      filename: "slurm.conf",
      valid: false,
      issues: [
        { severity: "error", message: "Unknown param" },
        { severity: "error", message: "Missing value" },
      ],
    }
    expect(summarizeValidation(resp)).toBe("2 errors")
  })

  it("counts warnings correctly", () => {
    const resp: SlurmValidateResponse = {
      filename: "slurm.conf",
      valid: false,
      issues: [{ severity: "warning", message: "Deprecated param" }],
    }
    expect(summarizeValidation(resp)).toBe("1 warning")
  })

  it("combines errors and warnings", () => {
    const issues: SlurmValidationIssue[] = [
      { severity: "error", message: "e1" },
      { severity: "warning", message: "w1" },
      { severity: "warning", message: "w2" },
    ]
    const resp: SlurmValidateResponse = { filename: "slurm.conf", valid: false, issues }
    expect(summarizeValidation(resp)).toBe("1 error, 2 warnings")
  })
})

// ─── SlurmBuild status badge class mapping ───────────────────────────────────

function buildBadgeClass(status: SlurmBuild["status"]): string {
  const map: Record<string, string> = {
    building:  "bg-status-warning/10 text-status-warning",
    completed: "bg-status-healthy/10 text-status-healthy",
    failed:    "bg-status-error/10 text-status-error",
    cancelled: "bg-status-neutral/10 text-status-neutral",
  }
  return map[status] ?? "bg-muted/30 text-muted-foreground"
}

describe("SlurmBuild status badge class mapping", () => {
  it("maps building to warning class", () => {
    expect(buildBadgeClass("building")).toContain("warning")
  })

  it("maps completed to healthy class", () => {
    expect(buildBadgeClass("completed")).toContain("healthy")
  })

  it("maps failed to error class", () => {
    expect(buildBadgeClass("failed")).toContain("error")
  })

  it("maps cancelled to neutral class", () => {
    expect(buildBadgeClass("cancelled")).toContain("neutral")
  })

  it("returns muted for unknown status", () => {
    expect(buildBadgeClass("unknown_status" as SlurmBuild["status"])).toContain("muted")
  })
})

// ─── SlurmUpgradeOperation phase ordering ────────────────────────────────────

const UPGRADE_PHASE_ORDER = ["dbd", "controller", "compute", "login"] as const

function phaseIndex(phase: string): number {
  const idx = UPGRADE_PHASE_ORDER.indexOf(phase as typeof UPGRADE_PHASE_ORDER[number])
  return idx === -1 ? UPGRADE_PHASE_ORDER.length : idx
}

function isPhaseComplete(op: SlurmUpgradeOperation, phase: string): boolean {
  const currentIdx = phaseIndex(op.phase ?? "")
  const targetIdx = phaseIndex(phase)
  if (op.status === "completed") return true
  return currentIdx > targetIdx
}

describe("SlurmUpgradeOperation phase ordering", () => {
  it("dbd is first phase (index 0)", () => {
    expect(phaseIndex("dbd")).toBe(0)
  })

  it("login is last phase (index 3)", () => {
    expect(phaseIndex("login")).toBe(3)
  })

  it("unknown phase gets index beyond last", () => {
    expect(phaseIndex("storage")).toBe(UPGRADE_PHASE_ORDER.length)
  })

  it("marks dbd complete when current phase is controller", () => {
    const op: Partial<SlurmUpgradeOperation> = { phase: "controller", status: "in_progress" }
    expect(isPhaseComplete(op as SlurmUpgradeOperation, "dbd")).toBe(true)
  })

  it("does not mark current phase as complete while in_progress", () => {
    const op: Partial<SlurmUpgradeOperation> = { phase: "controller", status: "in_progress" }
    expect(isPhaseComplete(op as SlurmUpgradeOperation, "controller")).toBe(false)
  })

  it("marks all phases complete when status is completed", () => {
    const op: Partial<SlurmUpgradeOperation> = { phase: "login", status: "completed" }
    for (const phase of UPGRADE_PHASE_ORDER) {
      expect(isPhaseComplete(op as SlurmUpgradeOperation, phase)).toBe(true)
    }
  })
})
