/**
 * fetch-wrapper.test.ts — TEST-3: apiFetch 401 handling.
 *
 * Tests run in jsdom via vitest.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { apiFetch, SESSION_EXPIRED_EVENT } from "../lib/api"

// ---- apiFetch 401 handling ---------------------------------------------------

describe("apiFetch -- 401 handling", () => {
  beforeEach(() => {
    vi.resetAllMocks()
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("should dispatch SESSION_EXPIRED_EVENT on 401 and throw", async () => {
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: false,
        status: 401,
        text: () => Promise.resolve('{"error":"unauthorized"}'),
        json: () => Promise.resolve({ error: "unauthorized" }),
      })
    ))

    const dispatched: Event[] = []
    window.addEventListener(SESSION_EXPIRED_EVENT, (e) => dispatched.push(e))

    await expect(apiFetch("/api/v1/nodes")).rejects.toThrow("401")
    expect(dispatched).toHaveLength(1)
    expect(dispatched[0].type).toBe(SESSION_EXPIRED_EVENT)
  })

  it("should NOT dispatch SESSION_EXPIRED_EVENT on non-401 errors", async () => {
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: false,
        status: 500,
        text: () => Promise.resolve("internal error"),
      })
    ))

    const dispatched: Event[] = []
    window.addEventListener(SESSION_EXPIRED_EVENT, (e) => dispatched.push(e))

    await expect(apiFetch("/api/v1/nodes")).rejects.toThrow("500")
    expect(dispatched).toHaveLength(0)
  })

  it("should return parsed JSON on success", async () => {
    const payload = { nodes: [], total: 0 }
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: () => null },
        text: () => Promise.resolve(JSON.stringify(payload)),
      })
    ))

    const result = await apiFetch("/api/v1/nodes")
    expect(result).toEqual(payload)
  })

  it("should always send credentials: include", async () => {
    const mockFetch = vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: () => null },
        text: () => Promise.resolve("{}"),
      })
    )
    vi.stubGlobal("fetch", mockFetch)

    await apiFetch("/api/v1/nodes")

    expect(mockFetch).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ credentials: "include" })
    )
  })

  it("should return undefined for 204 No Content", async () => {
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 204,
        headers: { get: () => null },
      })
    ))

    const result = await apiFetch("/api/v1/images/abc")
    expect(result).toBeUndefined()
  })

  it("should return undefined when Content-Length is 0", async () => {
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: (h: string) => (h === "Content-Length" ? "0" : null) },
      })
    ))

    const result = await apiFetch("/api/v1/images/abc")
    expect(result).toBeUndefined()
  })

  it("should return undefined for 200 with empty body", async () => {
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: () => null },
        text: () => Promise.resolve(""),
      })
    ))

    const result = await apiFetch("/api/v1/images/abc")
    expect(result).toBeUndefined()
  })

  it("should throw a descriptive error for non-JSON body", async () => {
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: () => null },
        text: () => Promise.resolve("<html>Bad Gateway</html>"),
      })
    ))

    await expect(apiFetch("/api/v1/nodes")).rejects.toThrow(
      "apiFetch: server returned non-JSON body for /api/v1/nodes"
    )
  })

  it("should throw with status text on 500", async () => {
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: false,
        status: 500,
        text: () => Promise.resolve("internal server error"),
      })
    ))

    await expect(apiFetch("/api/v1/nodes")).rejects.toThrow("500: internal server error")
  })
})

// ---- Content-Type header behaviour (BUG-10) ---------------------------------

describe("apiFetch -- Content-Type header", () => {
  beforeEach(() => {
    vi.resetAllMocks()
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("should NOT send Content-Type on GET (no body)", async () => {
    const mockFetch = vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: () => null },
        text: () => Promise.resolve("{}"),
      })
    )
    vi.stubGlobal("fetch", mockFetch)

    await apiFetch("/api/v1/nodes")

    expect(mockFetch).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({
        headers: expect.not.objectContaining({ "Content-Type": expect.anything() }),
      })
    )
  })

  it("should send Content-Type: application/json on POST with JSON body", async () => {
    const mockFetch = vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: () => null },
        text: () => Promise.resolve("{}"),
      })
    )
    vi.stubGlobal("fetch", mockFetch)

    await apiFetch("/api/v1/nodes", {
      method: "POST",
      body: JSON.stringify({ name: "node-1" }),
    })

    expect(mockFetch).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({
        headers: expect.objectContaining({ "Content-Type": "application/json" }),
      })
    )
  })

  it("should let caller-supplied Content-Type win over the default", async () => {
    const mockFetch = vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: () => null },
        text: () => Promise.resolve("{}"),
      })
    )
    vi.stubGlobal("fetch", mockFetch)

    await apiFetch("/api/v1/upload", {
      method: "POST",
      body: "raw-data",
      headers: { "Content-Type": "text/plain" },
    })

    expect(mockFetch).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({
        headers: expect.objectContaining({ "Content-Type": "text/plain" }),
      })
    )
  })
})

// ---- api module contracts ---------------------------------------------------

describe("api module -- stable exports", () => {
  it("SESSION_EXPIRED_EVENT is exported with a stable name", () => {
    // Other modules depend on this constant; ensure it doesn't silently change.
    expect(SESSION_EXPIRED_EVENT).toBe("clustr:session-expired")
  })
})
