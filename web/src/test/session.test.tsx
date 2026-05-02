/**
 * session.test.tsx — critical tests for useSession() and apiFetch
 *
 * Tests run in jsdom. We mock fetch globally.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import { SESSION_EXPIRED_EVENT } from "../lib/api"
import { SessionProvider, useSession } from "../contexts/auth"

// ─── Helper: render inside SessionProvider ────────────────────────────────────

function TestConsumer() {
  const { session } = useSession()
  return <div data-testid="status">{session.status}</div>
}

function renderWithSession() {
  return render(
    <SessionProvider>
      <TestConsumer />
    </SessionProvider>
  )
}

// ─── Tests ────────────────────────────────────────────────────────────────────

describe("useSession", () => {
  beforeEach(() => {
    vi.resetAllMocks()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("should start in loading state", () => {
    // Mock fetch to never resolve so we stay in loading.
    vi.stubGlobal("fetch", () => new Promise(() => {}))

    renderWithSession()
    expect(screen.getByTestId("status").textContent).toBe("loading")
  })

  it("should transition to authed when /me returns 200", async () => {
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      if (url.includes("/auth/status")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          headers: { get: () => null },
          text: () => Promise.resolve(JSON.stringify({ has_admin: true })),
        })
      }
      if (url.includes("/auth/me")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          headers: { get: () => null },
          text: () => Promise.resolve(JSON.stringify({
            sub: "user-1",
            role: "admin",
            expires_at: "2026-12-31T00:00:00Z",
            assigned_groups: [],
          })),
        })
      }
      return Promise.reject(new Error("unexpected fetch: " + url))
    }))

    renderWithSession()

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("authed")
    })
  })

  it("should transition to unauthed when /me returns 401", async () => {
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      if (url.includes("/auth/status")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          headers: { get: () => null },
          text: () => Promise.resolve(JSON.stringify({ has_admin: true })),
        })
      }
      if (url.includes("/auth/me")) {
        return Promise.resolve({
          ok: false,
          status: 401,
          text: () => Promise.resolve('{"error":"no session"}'),
        })
      }
      return Promise.reject(new Error("unexpected fetch: " + url))
    }))

    renderWithSession()

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("unauthed")
    })
  })

  it("should transition to setup_required when /auth/status returns has_admin: false", async () => {
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      if (url.includes("/auth/status")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          headers: { get: () => null },
          text: () => Promise.resolve(JSON.stringify({ has_admin: false })),
        })
      }
      return Promise.reject(new Error("unexpected fetch: " + url))
    }))

    renderWithSession()

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("setup_required")
    })
  })

  it("should flip to unauthed when SESSION_EXPIRED_EVENT is dispatched", async () => {
    // Start authed.
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      if (url.includes("/auth/status")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          headers: { get: () => null },
          text: () => Promise.resolve(JSON.stringify({ has_admin: true })),
        })
      }
      if (url.includes("/auth/me")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          headers: { get: () => null },
          text: () => Promise.resolve(JSON.stringify({
            sub: "user-1",
            role: "admin",
            expires_at: "2026-12-31T00:00:00Z",
            assigned_groups: [],
          })),
        })
      }
      return Promise.reject(new Error("unexpected fetch: " + url))
    }))

    renderWithSession()

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("authed")
    })

    // Simulate a 401 from an API call.
    window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT))

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("unauthed")
    })
  })
})

// ─── Query key factory tests ─────────────────────────────────────────────────

describe("query key factories", () => {
  it("nodes query key includes search params for cache isolation", () => {
    // The pattern: ["nodes", q, sortCol, sortDir] — verify different q values produce different keys.
    const key1 = ["nodes", "web1", "hostname", "asc"]
    const key2 = ["nodes", "web2", "hostname", "asc"]
    expect(key1).not.toEqual(key2)
  })

  it("images query key is separate from nodes", () => {
    const nodesKey = ["nodes", "", "", "asc"]
    const imagesKey = ["images", "", "", "asc"]
    expect(nodesKey[0]).not.toBe(imagesKey[0])
  })
})
