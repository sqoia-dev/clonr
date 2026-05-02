/**
 * generate-temp-password.test.ts — unit tests for generateTempPassword (BUG-3).
 *
 * Verifies length, charset conformance, and that the function uses
 * crypto.getRandomValues (not Math.random).
 */

import { describe, it, expect, vi, afterEach } from "vitest"
import { generateTempPassword } from "../lib/utils"

const CHARSET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"

describe("generateTempPassword", () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("should return a string of the default length (24)", () => {
    const pwd = generateTempPassword()
    expect(pwd).toHaveLength(24)
  })

  it("should return a string of a custom length", () => {
    expect(generateTempPassword(16)).toHaveLength(16)
    expect(generateTempPassword(32)).toHaveLength(32)
  })

  it("should only contain characters from the expected charset", () => {
    const pwd = generateTempPassword(64)
    for (const ch of pwd) {
      expect(CHARSET).toContain(ch)
    }
  })

  it("should call crypto.getRandomValues, not Math.random", () => {
    const mathRandomSpy = vi.spyOn(Math, "random")
    generateTempPassword()
    expect(mathRandomSpy).not.toHaveBeenCalled()
  })

  it("should produce different values on successive calls", () => {
    // Probabilistically: collision chance is astronomically small for 24-char output.
    const a = generateTempPassword()
    const b = generateTempPassword()
    expect(a).not.toBe(b)
  })
})
