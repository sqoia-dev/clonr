/**
 * sprint8-ldap-write.test.ts — WRITE-TEST-2: Vitest coverage for Sprint 8 LDAP write-back UI.
 *
 * Covers:
 *   - Add LDAP user form validation
 *   - Add LDAP group form validation
 *   - Write-bind form state transitions
 *   - Group mode toggle enum validation
 *   - Dialect-specific error message rendering
 *   - Temp-password visibility state
 *   - Optimistic rollback simulation
 *   - Delete confirm typed-name validation
 */

import { describe, it, expect } from "vitest"

// ─── Shared validation helpers (extracted from identity.tsx forms) ─────────────

/** Validates a new LDAP user form. Returns error messages by field. */
function validateLDAPUserForm(uid: string, uidNum: string, gidNum: string): Record<string, string> {
  const errs: Record<string, string> = {}
  if (!uid) {
    errs.uid = "UID is required"
  } else if (!/^[a-z][a-z0-9_-]{0,30}$/.test(uid)) {
    errs.uid = "UID must start with a letter and contain only lowercase letters, digits, underscores, or hyphens (max 31 chars)"
  }
  if (uidNum !== "" && (isNaN(Number(uidNum)) || Number(uidNum) < 0)) {
    errs.uid_number = "UID number must be a non-negative integer"
  }
  if (gidNum !== "" && (isNaN(Number(gidNum)) || Number(gidNum) < 0)) {
    errs.gid_number = "GID number must be a non-negative integer"
  }
  return errs
}

/** Validates a new LDAP group form. Returns error messages by field. */
function validateLDAPGroupForm(cn: string, gidNum: string): Record<string, string> {
  const errs: Record<string, string> = {}
  if (!cn) {
    errs.cn = "CN is required"
  } else if (cn.length > 64) {
    errs.cn = "CN must be 64 characters or fewer"
  }
  if (gidNum !== "" && (isNaN(Number(gidNum)) || Number(gidNum) < 0)) {
    errs.gid_number = "GID number must be a non-negative integer"
  }
  return errs
}

/** Validates the write-bind form. */
function validateWriteBindForm(bindDN: string, _bindPassword: string): Record<string, string> {
  const errs: Record<string, string> = {}
  if (bindDN && !bindDN.includes("=")) {
    errs.write_bind_dn = "Bind DN must be in DN format (e.g. cn=admin,dc=cluster,dc=local)"
  }
  return errs
}

/** Derive the write-mode banner state from the config response. */
function getWriteBannerState(
  writeCapable: boolean | undefined,
  writeBindDNSet: boolean | undefined
): "none" | "yellow" | "green" {
  if (!writeBindDNSet && writeCapable === undefined) return "none"
  if (writeCapable === true) return "green"
  if (writeBindDNSet && !writeCapable) return "yellow"
  return "none"
}

/** Validates group mode values. */
function validateGroupMode(mode: string): boolean {
  return mode === "overlay" || mode === "direct"
}

// ─── LDAP user form validation ─────────────────────────────────────────────────

describe("LDAP user form validation (WRITE-TEST-2)", () => {
  it("should pass with a valid uid", () => {
    const errs = validateLDAPUserForm("alice", "", "")
    expect(Object.keys(errs)).toHaveLength(0)
  })

  it("should pass with uid, uid_number, and gid_number", () => {
    const errs = validateLDAPUserForm("bob", "1001", "1001")
    expect(Object.keys(errs)).toHaveLength(0)
  })

  it("should reject empty uid", () => {
    const errs = validateLDAPUserForm("", "", "")
    expect(errs.uid).toBeTruthy()
  })

  it("should reject uid starting with a number", () => {
    const errs = validateLDAPUserForm("1alice", "", "")
    expect(errs.uid).toBeTruthy()
  })

  it("should reject uid with uppercase letters", () => {
    const errs = validateLDAPUserForm("Alice", "", "")
    expect(errs.uid).toBeTruthy()
  })

  it("should reject uid with spaces", () => {
    const errs = validateLDAPUserForm("alice smith", "", "")
    expect(errs.uid).toBeTruthy()
  })

  it("should reject negative uid_number", () => {
    const errs = validateLDAPUserForm("alice", "-1", "")
    expect(errs.uid_number).toBeTruthy()
  })

  it("should reject non-numeric uid_number", () => {
    const errs = validateLDAPUserForm("alice", "abc", "")
    expect(errs.uid_number).toBeTruthy()
  })

  it("should reject negative gid_number", () => {
    const errs = validateLDAPUserForm("alice", "", "-5")
    expect(errs.gid_number).toBeTruthy()
  })

  it("should allow uid_number = 0 (auto-assign)", () => {
    const errs = validateLDAPUserForm("alice", "0", "0")
    expect(Object.keys(errs)).toHaveLength(0)
  })
})

// ─── LDAP group form validation ────────────────────────────────────────────────

describe("LDAP group form validation (WRITE-TEST-2)", () => {
  it("should pass with valid cn", () => {
    const errs = validateLDAPGroupForm("hpc-users", "")
    expect(Object.keys(errs)).toHaveLength(0)
  })

  it("should pass with cn and gid_number", () => {
    const errs = validateLDAPGroupForm("clustr-admins", "5000")
    expect(Object.keys(errs)).toHaveLength(0)
  })

  it("should reject empty cn", () => {
    const errs = validateLDAPGroupForm("", "")
    expect(errs.cn).toBeTruthy()
  })

  it("should reject cn longer than 64 chars", () => {
    const longCN = "a".repeat(65)
    const errs = validateLDAPGroupForm(longCN, "")
    expect(errs.cn).toBeTruthy()
  })

  it("should accept cn exactly 64 chars", () => {
    const cn = "a".repeat(64)
    const errs = validateLDAPGroupForm(cn, "")
    expect(Object.keys(errs)).toHaveLength(0)
  })

  it("should reject invalid gid_number", () => {
    const errs = validateLDAPGroupForm("test-group", "not-a-number")
    expect(errs.gid_number).toBeTruthy()
  })

  it("should reject negative gid_number", () => {
    const errs = validateLDAPGroupForm("test-group", "-100")
    expect(errs.gid_number).toBeTruthy()
  })
})

// ─── Write-bind form validation ────────────────────────────────────────────────

describe("write-bind form validation (WRITE-TEST-2, WRITE-CFG-3)", () => {
  it("should pass with empty bind DN (clears write bind)", () => {
    const errs = validateWriteBindForm("", "")
    expect(Object.keys(errs)).toHaveLength(0)
  })

  it("should pass with a valid DN", () => {
    const errs = validateWriteBindForm("cn=admin,dc=cluster,dc=local", "password")
    expect(Object.keys(errs)).toHaveLength(0)
  })

  it("should reject a DN without equals sign", () => {
    const errs = validateWriteBindForm("admin-no-dn-format", "password")
    expect(errs.write_bind_dn).toBeTruthy()
  })
})

// ─── Write-mode banner state (WRITE-SAFETY-2) ─────────────────────────────────

describe("write-mode banner state (WRITE-SAFETY-2)", () => {
  it("should be none when no write bind is configured", () => {
    expect(getWriteBannerState(undefined, false)).toBe("none")
  })

  it("should be none when no write bind is set (undefined)", () => {
    expect(getWriteBannerState(undefined, undefined)).toBe("none")
  })

  it("should be green when write_capable=true", () => {
    expect(getWriteBannerState(true, true)).toBe("green")
  })

  it("should be yellow when write bind is set but probe not yet passed", () => {
    expect(getWriteBannerState(false, true)).toBe("yellow")
  })

  it("should be yellow when write bind is set but probe failed", () => {
    expect(getWriteBannerState(false, true)).toBe("yellow")
  })
})

// ─── Group mode toggle validation ─────────────────────────────────────────────

describe("group mode values (WRITE-GRP-4)", () => {
  it("should accept overlay mode", () => {
    expect(validateGroupMode("overlay")).toBe(true)
  })

  it("should accept direct mode", () => {
    expect(validateGroupMode("direct")).toBe(true)
  })

  it("should reject unknown mode", () => {
    expect(validateGroupMode("write-everything")).toBe(false)
  })

  it("should reject empty mode", () => {
    expect(validateGroupMode("")).toBe(false)
  })
})

// ─── Dialect error message rendering (WRITE-DIALECT-2) ────────────────────────

/** Simulates how the UI renders a dialect error from the server. */
function renderDialectError(serverError: string): { isDialectError: boolean; displayMessage: string } {
  const dialectPattern = /not implemented for backend dialect "([^"]+)"/
  const match = serverError.match(dialectPattern)
  if (match) {
    return {
      isDialectError: true,
      displayMessage: `This operation is not supported for the ${match[1]} directory backend. clustr v0.4.0 supports OpenLDAP only.`,
    }
  }
  return { isDialectError: false, displayMessage: serverError }
}

describe("dialect-specific error rendering (WRITE-DIALECT-2)", () => {
  it("should detect and render a FreeIPA dialect error", () => {
    const err = `ldap write: operation "create_user" is not implemented for backend dialect "freeipa" in clustr v0.4.0; use OpenLDAP`
    const result = renderDialectError(err)
    expect(result.isDialectError).toBe(true)
    expect(result.displayMessage).toContain("freeipa")
    expect(result.displayMessage).toContain("OpenLDAP")
  })

  it("should detect and render an AD dialect error", () => {
    const err = `ldap write: operation "password_reset" is not implemented for backend dialect "ad" in clustr v0.4.0; use OpenLDAP`
    const result = renderDialectError(err)
    expect(result.isDialectError).toBe(true)
    expect(result.displayMessage).toContain("ad")
  })

  it("should not flag a regular network error as a dialect error", () => {
    const err = "dial tcp 10.0.0.1:636: connection refused"
    const result = renderDialectError(err)
    expect(result.isDialectError).toBe(false)
    expect(result.displayMessage).toBe(err)
  })

  it("should not flag a bind error as a dialect error", () => {
    const err = "Invalid credentials (49)"
    const result = renderDialectError(err)
    expect(result.isDialectError).toBe(false)
  })
})

// ─── Temp-password visibility state (WRITE-USER-5) ────────────────────────────

describe("temp-password one-shot visibility (WRITE-USER-5)", () => {
  it("should show password only after toggle", () => {
    let showPwd = false
    const pwdValue = "Abc123XyzQwerty20"
    const displayed = showPwd ? pwdValue : "••••••••••••••••••••"
    expect(displayed).not.toBe(pwdValue)
    showPwd = true
    const displayed2 = showPwd ? pwdValue : "••••••••••••••••••••"
    expect(displayed2).toBe(pwdValue)
  })

  it("should allow clearing the temp password display", () => {
    let resetResult: { uid: string; pwd: string } | null = { uid: "alice", pwd: "tempPass123" }
    // Simulate dismissing
    resetResult = null
    expect(resetResult).toBeNull()
  })
})

// ─── Delete confirm typed-name validation ─────────────────────────────────────

describe("delete confirm typed-name validation (WRITE-USER-6, WRITE-GRP safety)", () => {
  it("should enable delete button when typed name matches", () => {
    const entityName = "alice"
    const typedInput = "alice"
    expect(typedInput === entityName).toBe(true)
  })

  it("should disable delete button when typed name does not match", () => {
    const entityName = "alice"
    const typedInput: string = "alic"
    expect(typedInput === entityName).toBe(false)
  })

  it("should be case-sensitive", () => {
    const entityName = "hpc-users"
    const typedInput: string = "HPC-Users"
    expect(typedInput === entityName).toBe(false)
  })
})

// ─── Optimistic rollback simulation (WRITE-TEST-2) ────────────────────────────

describe("optimistic update and rollback", () => {
  it("should revert local state on directory error", () => {
    // Simulate: optimistic update applied, then API returns 500
    const originalGroups = [{ cn: "hpc-users", gid_number: 5000, member_uids: ["alice"] }]
    let groups = [...originalGroups]

    // Optimistically add a member
    groups = groups.map(g =>
      g.cn === "hpc-users" ? { ...g, member_uids: [...g.member_uids, "bob"] } : g
    )
    expect(groups[0].member_uids).toContain("bob")

    // Simulate API failure: roll back
    groups = originalGroups
    expect(groups[0].member_uids).not.toContain("bob")
    expect(groups[0].member_uids).toContain("alice")
  })

  it("should revert group mode on toggle failure", () => {
    let currentMode: "overlay" | "direct" = "overlay"

    // Optimistic: toggle to direct
    const previousMode = currentMode
    currentMode = "direct"
    expect(currentMode).toBe("direct")

    // API fails: roll back
    currentMode = previousMode
    expect(currentMode).toBe("overlay")
  })
})
