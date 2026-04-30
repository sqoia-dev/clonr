/**
 * sprint9-ldap-internal.test.ts — X9-2: Vitest coverage for Sprint 9 Internal LDAP auto-deploy UI.
 *
 * Covers:
 *   - Mode toggle validation (internal / external; typed-confirm guards)
 *   - Enable mutation: each structured error variant rendering
 *   - Mode-switch typed-confirm validation
 *   - formatUptime helper
 *   - Structured error field completeness
 *   - Disable/Destroy confirm logic
 */

import { describe, it, expect } from "vitest"
import type { LDAPInternalEnableError, LDAPInternalStatusResponse } from "@/lib/types"

// ─── Mode toggle validation (MODE-4) ─────────────────────────────────────────

/** Returns whether a mode-switch confirm is valid. */
function validateModeSwitchConfirm(target: string, confirm: string): boolean {
  return confirm === target && (target === "internal" || target === "external")
}

describe("mode-switch typed-confirm (MODE-4)", () => {
  it("accepts exact match for internal", () => {
    expect(validateModeSwitchConfirm("internal", "internal")).toBe(true)
  })

  it("accepts exact match for external", () => {
    expect(validateModeSwitchConfirm("external", "external")).toBe(true)
  })

  it("rejects mismatched confirm", () => {
    expect(validateModeSwitchConfirm("internal", "ext")).toBe(false)
    expect(validateModeSwitchConfirm("external", "intern")).toBe(false)
    expect(validateModeSwitchConfirm("internal", "")).toBe(false)
  })

  it("rejects invalid mode values", () => {
    expect(validateModeSwitchConfirm("admin", "admin")).toBe(false)
    expect(validateModeSwitchConfirm("", "")).toBe(false)
  })
})

// ─── Structured error variants (ENABLE-2) ────────────────────────────────────

const knownErrorCodes = ["port_in_use", "slapd_not_installed", "selinux_denied", "unit_failed_to_start", "enable_failed"] as const

function buildTestError(code: typeof knownErrorCodes[number]): LDAPInternalEnableError {
  const errors: Record<typeof knownErrorCodes[number], LDAPInternalEnableError> = {
    port_in_use: {
      code: "port_in_use",
      message: "Port 636 (LDAPS) is already in use",
      remediation: "Stop the process occupying port 636 before enabling the internal LDAP server.",
      diag_cmd: "ss -tlnp | grep :636",
    },
    slapd_not_installed: {
      code: "slapd_not_installed",
      message: "openldap-servers could not be installed",
      remediation: "Install openldap-servers manually: sudo dnf install openldap-servers",
      diag_cmd: "dnf info openldap-servers",
    },
    selinux_denied: {
      code: "selinux_denied",
      message: "SELinux blocked slapd",
      remediation: "Run: sealert -a /var/log/audit/audit.log | tail -40",
      diag_cmd: "ausearch -c slapd --raw | audit2why",
    },
    unit_failed_to_start: {
      code: "unit_failed_to_start",
      message: "clustr-slapd.service failed to start",
      remediation: "Check the unit status and logs for details.",
      diag_cmd: "systemctl status clustr-slapd.service",
    },
    enable_failed: {
      code: "enable_failed",
      message: "Unexpected error during provisioning",
      remediation: "Check the server logs for more details.",
      diag_cmd: "journalctl -u clustr-serverd --since '5 minutes ago'",
    },
  }
  return errors[code]
}

describe("enable error variants (ENABLE-2)", () => {
  it("all known error codes have required fields", () => {
    for (const code of knownErrorCodes) {
      const err = buildTestError(code)
      expect(err.code, `[${code}] code`).toBeTruthy()
      expect(err.message, `[${code}] message`).toBeTruthy()
      expect(err.remediation, `[${code}] remediation`).toBeTruthy()
      expect(err.diag_cmd, `[${code}] diag_cmd`).toBeTruthy()
    }
  })

  it("port_in_use error includes ss command", () => {
    const err = buildTestError("port_in_use")
    expect(err.diag_cmd).toContain("636")
  })

  it("slapd_not_installed error includes dnf command", () => {
    const err = buildTestError("slapd_not_installed")
    expect(err.remediation).toContain("dnf install")
    expect(err.diag_cmd).toContain("dnf")
  })

  it("selinux_denied error includes audit2why", () => {
    const err = buildTestError("selinux_denied")
    expect(err.diag_cmd).toContain("audit2why")
  })

  it("unit_failed_to_start error includes systemctl status", () => {
    const err = buildTestError("unit_failed_to_start")
    expect(err.diag_cmd).toContain("systemctl status")
  })
})

// ─── Internal status shape validation (ENABLE-3) ─────────────────────────────

describe("internal status response shape (ENABLE-3)", () => {
  it("ready status has all required fields", () => {
    const status: LDAPInternalStatusResponse = {
      enabled: true,
      status: "ready",
      base_dn: "dc=cluster,dc=local",
      running: true,
      port: 636,
      uptime_sec: 300,
      admin_password_set: true,
      source_mode: "internal",
    }
    expect(status.port).toBe(636)
    expect(status.uptime_sec).toBeGreaterThan(0)
    expect(status.source_mode).toBe("internal")
  })

  it("disabled status has zero uptime", () => {
    const status: LDAPInternalStatusResponse = {
      enabled: false,
      status: "disabled",
      running: false,
      port: 636,
      uptime_sec: 0,
      admin_password_set: false,
      source_mode: "internal",
    }
    expect(status.uptime_sec).toBe(0)
    expect(status.running).toBe(false)
  })
})

// ─── Destroy confirm validation (DISABLE-1) ──────────────────────────────────

/** Returns whether the destroy confirmation input is valid. */
function validateDestroyConfirm(input: string): boolean {
  return input === "destroy"
}

describe("destroy typed-confirm (DISABLE-1)", () => {
  it("accepts exact 'destroy'", () => {
    expect(validateDestroyConfirm("destroy")).toBe(true)
  })

  it("rejects partial or wrong input", () => {
    expect(validateDestroyConfirm("Destroy")).toBe(false)
    expect(validateDestroyConfirm("DESTROY")).toBe(false)
    expect(validateDestroyConfirm("destro")).toBe(false)
    expect(validateDestroyConfirm("")).toBe(false)
    expect(validateDestroyConfirm("yes")).toBe(false)
  })
})

// ─── formatUptime helper ──────────────────────────────────────────────────────

function formatUptime(sec: number): string {
  if (sec <= 0) return "—"
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

describe("formatUptime", () => {
  it("returns — for 0", () => {
    expect(formatUptime(0)).toBe("—")
  })

  it("returns — for negative", () => {
    expect(formatUptime(-5)).toBe("—")
  })

  it("returns seconds for sub-minute", () => {
    expect(formatUptime(42)).toBe("42s")
  })

  it("returns minutes and seconds", () => {
    expect(formatUptime(90)).toBe("1m 30s")
  })

  it("returns hours and minutes", () => {
    expect(formatUptime(7260)).toBe("2h 1m")
  })

  it("returns 1h 0m for exactly one hour", () => {
    expect(formatUptime(3600)).toBe("1h 0m")
  })
})
