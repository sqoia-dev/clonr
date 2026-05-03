/**
 * sprint13-posixid.test.ts — Sprint 13 #93+#94+#95 frontend validation coverage.
 *
 * Covers:
 *   - UID/GID override toggle: sends 0 (auto) when not overridden
 *   - Structured posixid error code parsing from API response
 *   - Add form: email field presence in request body
 *   - SSH keys: multi-line split logic
 *   - Edit Sheet: name parsing (first/last split)
 *   - Edit Sheet: group diff (add/remove)
 */

import { describe, it, expect } from "vitest"

// ─── UID/GID override logic ───────────────────────────────────────────────────

/** Simulates the createMutation body builder with override toggles. */
function buildCreateBody(opts: {
  uid: string
  uidOverride: boolean
  uidNum: string
  gidOverride: boolean
  gidNum: string
  email: string
  sshKeys: string
}) {
  const sshKeys = opts.sshKeys.split("\n").map(k => k.trim()).filter(Boolean)
  return {
    uid: opts.uid,
    uid_number: opts.uidOverride ? (Number(opts.uidNum) || 0) : 0,
    gid_number: opts.gidOverride ? (Number(opts.gidNum) || 0) : 0,
    mail: opts.email || undefined,
    ssh_public_keys: sshKeys.length > 0 ? sshKeys : undefined,
  }
}

describe("LDAP create body", () => {
  it("sends uid_number=0 when override is off (auto-allocate)", () => {
    const body = buildCreateBody({ uid: "jdoe", uidOverride: false, uidNum: "12345", gidOverride: false, gidNum: "", email: "", sshKeys: "" })
    expect(body.uid_number).toBe(0)
    expect(body.gid_number).toBe(0)
  })

  it("sends explicit uid_number when override is on", () => {
    const body = buildCreateBody({ uid: "jdoe", uidOverride: true, uidNum: "15000", gidOverride: false, gidNum: "", email: "", sshKeys: "" })
    expect(body.uid_number).toBe(15000)
    expect(body.gid_number).toBe(0)
  })

  it("includes email in mail field when provided", () => {
    const body = buildCreateBody({ uid: "jdoe", uidOverride: false, uidNum: "", gidOverride: false, gidNum: "", email: "jdoe@example.com", sshKeys: "" })
    expect(body.mail).toBe("jdoe@example.com")
  })

  it("omits mail when email is empty", () => {
    const body = buildCreateBody({ uid: "jdoe", uidOverride: false, uidNum: "", gidOverride: false, gidNum: "", email: "", sshKeys: "" })
    expect(body.mail).toBeUndefined()
  })

  it("splits ssh_keys by newline and trims whitespace", () => {
    const body = buildCreateBody({
      uid: "jdoe", uidOverride: false, uidNum: "", gidOverride: false, gidNum: "", email: "",
      sshKeys: "ssh-ed25519 AAAA1\n  ssh-rsa AAAA2  \n\n",
    })
    expect(body.ssh_public_keys).toEqual(["ssh-ed25519 AAAA1", "ssh-rsa AAAA2"])
  })

  it("omits ssh_public_keys when keys string is empty", () => {
    const body = buildCreateBody({ uid: "jdoe", uidOverride: false, uidNum: "", gidOverride: false, gidNum: "", email: "", sshKeys: "" })
    expect(body.ssh_public_keys).toBeUndefined()
  })
})

// ─── PosixID error code parsing ───────────────────────────────────────────────

type PosixIDCode = "range_exhausted" | "reserved_id" | "out_of_range" | "id_collision" | "posixid_error"

function parsePosixIDError(body: { error: string; code?: string; field?: string }): { message: string; code: PosixIDCode; field: string } {
  const validCodes: PosixIDCode[] = ["range_exhausted", "reserved_id", "out_of_range", "id_collision", "posixid_error"]
  const code = (validCodes.includes(body.code as PosixIDCode) ? body.code : "posixid_error") as PosixIDCode
  return { message: body.error, code, field: body.field ?? "" }
}

describe("PosixID error parsing", () => {
  it("parses id_collision code", () => {
    const parsed = parsePosixIDError({ error: "UID 12345 already in use", code: "id_collision", field: "uid_number" })
    expect(parsed.code).toBe("id_collision")
    expect(parsed.field).toBe("uid_number")
  })

  it("parses range_exhausted code", () => {
    const parsed = parsePosixIDError({ error: "range exhausted", code: "range_exhausted", field: "gid_number" })
    expect(parsed.code).toBe("range_exhausted")
  })

  it("falls back to posixid_error for unknown code", () => {
    const parsed = parsePosixIDError({ error: "something went wrong", code: "unknown_thing" })
    expect(parsed.code).toBe("posixid_error")
  })
})

// ─── Name parsing (Edit Sheet) ────────────────────────────────────────────────

function parseName(full: string): { given_name: string; sn: string; cn: string } {
  const parts = full.trim().split(/\s+/)
  if (parts.length === 1) return { given_name: parts[0], sn: parts[0], cn: parts[0] }
  const sn = parts[parts.length - 1]
  const given_name = parts.slice(0, -1).join(" ")
  return { given_name, sn, cn: full.trim() }
}

describe("Name parsing", () => {
  it("splits two-part name correctly", () => {
    const r = parseName("Jane Doe")
    expect(r.given_name).toBe("Jane")
    expect(r.sn).toBe("Doe")
    expect(r.cn).toBe("Jane Doe")
  })

  it("handles three-part name (middle name goes to givenName)", () => {
    const r = parseName("Mary Jane Watson")
    expect(r.given_name).toBe("Mary Jane")
    expect(r.sn).toBe("Watson")
  })

  it("single word name uses same value for both", () => {
    const r = parseName("Slurm")
    expect(r.given_name).toBe("Slurm")
    expect(r.sn).toBe("Slurm")
  })
})

// ─── Supplementary group diff (Edit Sheet) ────────────────────────────────────

function computeGroupDiff(currentGroups: string[], newGroups: string[]) {
  return {
    add_groups: newGroups.filter(g => !currentGroups.includes(g)),
    remove_groups: currentGroups.filter(g => !newGroups.includes(g)),
  }
}

describe("Group diff", () => {
  it("detects added groups", () => {
    const diff = computeGroupDiff(["users"], ["users", "admins"])
    expect(diff.add_groups).toEqual(["admins"])
    expect(diff.remove_groups).toEqual([])
  })

  it("detects removed groups", () => {
    const diff = computeGroupDiff(["users", "admins"], ["users"])
    expect(diff.add_groups).toEqual([])
    expect(diff.remove_groups).toEqual(["admins"])
  })

  it("handles no change", () => {
    const diff = computeGroupDiff(["users"], ["users"])
    expect(diff.add_groups).toEqual([])
    expect(diff.remove_groups).toEqual([])
  })

  it("handles empty initial groups", () => {
    const diff = computeGroupDiff([], ["wheel"])
    expect(diff.add_groups).toEqual(["wheel"])
    expect(diff.remove_groups).toEqual([])
  })
})
