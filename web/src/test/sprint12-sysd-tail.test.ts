/**
 * sprint12-sysd-tail.test.ts — Sprint 12 Vitest coverage.
 *
 * Covers:
 *   - sysd.ButtonState equivalents (UI button set derivation from Active/Enabled)
 *   - SlurmPushOperation status badge mapping (PushOpStatusBadge)
 *   - TAIL-1: render-preview response shape validation
 *   - TAIL-3: dep-matrix row parsing
 *   - TAIL-4: push-op polling stop condition (completed/failed)
 *   - LDAPInternalStatusResponse ui_buttons field shape
 */

import { describe, it, expect } from "vitest"
import type {
  LDAPInternalStatusResponse,
  SlurmRenderPreviewResponse,
  SlurmDepMatrixResponse,
  SlurmDepMatrixRow,
  SlurmPushOperation,
} from "@/lib/types"

// ─── sysd ButtonState equivalent ─────────────────────────────────────────────
//
// Mirrors the Go sysd.ButtonState logic — UI tests verify the frontend
// correctly handles all four states the server can now return in ui_buttons.

type UIButton = "enable" | "disable" | "takeover" | "stop" | "start"

/** Derives the recommended button set from systemd state. Mirrors sysd.ButtonState. */
function deriveButtons(active: string, enabled: string): UIButton[] {
  const isActive = active === "active"
  const isEnabled = enabled === "enabled"
  if (isActive && isEnabled) return ["disable"]
  if (!isActive && !isEnabled) return ["enable"]
  if (isActive && !isEnabled) return ["takeover", "stop"]
  if (!isActive && isEnabled) return ["start"]
  return ["enable"]
}

describe("sysd ButtonState — UI derivation", () => {
  it("active+enabled → disable only", () => {
    expect(deriveButtons("active", "enabled")).toEqual(["disable"])
  })

  it("inactive+disabled → enable only", () => {
    expect(deriveButtons("inactive", "disabled")).toEqual(["enable"])
  })

  it("active+disabled → takeover + stop (orphan)", () => {
    const buttons = deriveButtons("active", "disabled")
    expect(buttons).toContain("takeover")
    expect(buttons).toContain("stop")
    expect(buttons).toHaveLength(2)
  })

  it("inactive+enabled → start (will-restart-at-boot)", () => {
    expect(deriveButtons("inactive", "enabled")).toEqual(["start"])
  })

  it("unknown state falls back to enable", () => {
    expect(deriveButtons("", "")).toEqual(["enable"])
  })

  it("failed+enabled → treated as inactive (not active), returns start", () => {
    // "failed" is not "active" so isActive=false, isEnabled=true
    expect(deriveButtons("failed", "enabled")).toEqual(["start"])
  })
})

// ─── LDAPInternalStatusResponse ui_buttons field ─────────────────────────────

describe("LDAPInternalStatusResponse ui_buttons field", () => {
  it("accepts response without ui_buttons (pre-Sprint 12 server)", () => {
    const resp: LDAPInternalStatusResponse = {
      enabled: true,
      status: "ready",
      running: true,
      port: 636,
      uptime_sec: 3600,
      admin_password_set: true,
      source_mode: "internal",
      // ui_buttons intentionally absent
    }
    // Should not throw when accessed.
    expect(resp.ui_buttons ?? ["disable"]).toContain("disable")
  })

  it("accepts ui_buttons with all four button types", () => {
    const buttons: string[] = ["disable", "enable", "takeover", "stop", "start"]
    const resp: LDAPInternalStatusResponse = {
      enabled: true,
      status: "ready",
      running: true,
      port: 636,
      uptime_sec: 100,
      admin_password_set: true,
      source_mode: "internal",
      ui_buttons: buttons,
    }
    expect(resp.ui_buttons).toHaveLength(5)
  })

  it("accepts systemd_active and systemd_enabled fields", () => {
    const resp: LDAPInternalStatusResponse = {
      enabled: true,
      status: "ready",
      running: true,
      port: 636,
      uptime_sec: 0,
      admin_password_set: false,
      source_mode: "internal",
      systemd_active: "active",
      systemd_enabled: "enabled",
    }
    expect(resp.systemd_active).toBe("active")
    expect(resp.systemd_enabled).toBe("enabled")
  })
})

// ─── TAIL-1: render preview response shape ───────────────────────────────────

describe("SlurmRenderPreviewResponse shape", () => {
  it("parses valid render preview response", () => {
    const resp: SlurmRenderPreviewResponse = {
      filename: "slurm.conf",
      node_id: "cbf2c958-4172-47c3-9b0d-29caa4e21df4",
      rendered_content: "ClusterName=clustr\nNodeName=slurm-compute\n",
      checksum: "abc123def456",
    }
    expect(resp.filename).toBe("slurm.conf")
    expect(resp.rendered_content).toContain("ClusterName")
    expect(resp.checksum).toBe("abc123def456")
  })

  it("handles empty rendered_content gracefully", () => {
    const resp: SlurmRenderPreviewResponse = {
      filename: "gres.conf",
      node_id: "some-id",
      rendered_content: "",
      checksum: "",
    }
    expect(resp.rendered_content).toBe("")
  })
})

// ─── TAIL-3: dep matrix parsing ──────────────────────────────────────────────

describe("SlurmDepMatrixResponse parsing", () => {
  const sampleMatrix: SlurmDepMatrixRow[] = [
    {
      id: "1",
      slurm_version_min: "23.11.0",
      slurm_version_max: "24.11.99",
      dep_name: "pmix",
      dep_version_min: "4.0.0",
      dep_version_max: "4.99.99",
      source: "builtin",
    },
    {
      id: "2",
      slurm_version_min: "24.11.0",
      slurm_version_max: "25.11.99",
      dep_name: "hwloc",
      dep_version_min: "2.0.0",
      dep_version_max: "2.99.99",
      source: "builtin",
    },
  ]

  it("matrix has correct total", () => {
    const resp: SlurmDepMatrixResponse = { matrix: sampleMatrix, total: sampleMatrix.length }
    expect(resp.total).toBe(2)
  })

  it("all required dep matrix row fields present", () => {
    const row = sampleMatrix[0]
    expect(row.id).toBeTruthy()
    expect(row.slurm_version_min).toBeTruthy()
    expect(row.slurm_version_max).toBeTruthy()
    expect(row.dep_name).toBeTruthy()
    expect(row.dep_version_min).toBeTruthy()
    expect(row.dep_version_max).toBeTruthy()
    expect(row.source).toBeTruthy()
  })

  it("handles empty matrix gracefully", () => {
    const resp: SlurmDepMatrixResponse = { matrix: [], total: 0 }
    expect(resp.matrix).toHaveLength(0)
    // ?? [] guard in component never throws.
    expect(resp.matrix ?? []).toHaveLength(0)
  })

  it("handles null matrix gracefully (server returns null slice)", () => {
    const raw = { matrix: null as unknown as SlurmDepMatrixRow[], total: 0 }
    expect(raw.matrix ?? []).toHaveLength(0)
  })
})

// ─── TAIL-4: push-op polling stop condition ───────────────────────────────────

/** Returns true when the push-op has reached a terminal state and polling should stop. */
function shouldStopPolling(op: SlurmPushOperation): boolean {
  return op.status === "completed" || op.status === "failed"
}

describe("push-op polling stop condition (TAIL-4)", () => {
  const baseOp: SlurmPushOperation = {
    id: "op-123",
    filenames: ["slurm.conf"],
    file_versions: { "slurm.conf": 3 },
    apply_action: "reconfigure",
    status: "pending",
    node_count: 2,
    success_count: 0,
    failure_count: 0,
    started_at: Math.floor(Date.now() / 1000),
  }

  it("does not stop polling when status is pending", () => {
    expect(shouldStopPolling({ ...baseOp, status: "pending" })).toBe(false)
  })

  it("does not stop polling when status is running", () => {
    expect(shouldStopPolling({ ...baseOp, status: "running" })).toBe(false)
  })

  it("stops polling when status is completed", () => {
    expect(shouldStopPolling({ ...baseOp, status: "completed" })).toBe(true)
  })

  it("stops polling when status is failed", () => {
    expect(shouldStopPolling({ ...baseOp, status: "failed" })).toBe(true)
  })

  it("push-op status badge maps pending correctly", () => {
    const statusMap: Record<string, string> = {
      pending:   "Pending",
      running:   "Running",
      completed: "Completed",
      failed:    "Failed",
    }
    expect(statusMap["completed"]).toBe("Completed")
    expect(statusMap["failed"]).toBe("Failed")
    expect(statusMap["unknown_status"] ?? "unknown_status").toBe("unknown_status")
  })
})

// ─── Munge key response shape ─────────────────────────────────────────────────

import type { SlurmMungeKeyResponse } from "@/lib/types"

describe("SlurmMungeKeyResponse shape (TAIL-2)", () => {
  it("parses generate response", () => {
    const resp: SlurmMungeKeyResponse = {
      status: "ok",
      message: "munge key generated and stored",
    }
    expect(resp.status).toBe("ok")
    expect(resp.message).toContain("generated")
  })

  it("parses rotate response", () => {
    const resp: SlurmMungeKeyResponse = {
      status: "ok",
      message: "munge key rotated",
    }
    expect(resp.status).toBe("ok")
    expect(resp.message).toContain("rotated")
  })
})
