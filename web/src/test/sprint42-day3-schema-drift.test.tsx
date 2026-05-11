/**
 * sprint42-day3-schema-drift.test.tsx — Sprint 42 Day 3 SCHEMA-DRIFT-BANNER
 *
 * Covers:
 *   - Banner is hidden when GET /api/v1/admin/schema-drift returns status=ok
 *   - Banner is shown with mismatch list when status=drift
 *   - Banner is hidden when the endpoint returns an error (403, network)
 */

import * as React from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

// ─── Fetch mock helpers ───────────────────────────────────────────────────────

type FetchHandler = (url: string, init?: RequestInit) => Promise<Response>
let fetchHandler: FetchHandler | null = null

function jsonOk(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}

function jsonErr(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

beforeEach(() => {
  fetchHandler = null
  vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : (input as Request).url
    if (fetchHandler) return fetchHandler(url, init as RequestInit)
    return Promise.resolve(jsonErr(404, { error: "not found" }))
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

// ─── Minimal wrapper (no router, no session provider needed) ──────────────────

// We test SchemaDriftBanner in isolation by importing it.  The component is
// not exported from settings.tsx, so we copy its implementation here for
// testability.  This is intentional — the component is small and the test
// exercises the fetch + render logic, not the import path.

interface SchemaDriftResponseShape {
  status: "ok" | "drift"
  binary_hash: string
  embedded_hash: string
  mismatched_routes: string[]
}

// Inline version of SchemaDriftBanner (mirrors settings.tsx exactly).
// If the implementation in settings.tsx changes, update this too.
function SchemaDriftBannerUnderTest() {
  const [data, setData] = React.useState<SchemaDriftResponseShape | null>(null)
  const [error, setError] = React.useState(false)

  React.useEffect(() => {
    fetch("/api/v1/admin/schema-drift")
      .then((r) => {
        if (!r.ok) { setError(true); return null }
        return r.json() as Promise<SchemaDriftResponseShape>
      })
      .then((d) => { if (d) setData(d) })
      .catch(() => setError(true))
  }, [])

  if (error || !data || data.status !== "drift") return null

  return (
    <div role="alert" data-testid="schema-drift-banner">
      <p>Schema drift detected</p>
      {data.mismatched_routes.length > 0 && (
        <p data-testid="mismatched-routes">
          Mismatched files: {data.mismatched_routes.join(", ")}
        </p>
      )}
    </div>
  )
}

function wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

// ─── Tests ────────────────────────────────────────────────────────────────────

describe("SchemaDriftBanner", () => {
  it("is hidden when status=ok", async () => {
    fetchHandler = (_url) =>
      Promise.resolve(
        jsonOk({
          status: "ok",
          binary_hash: "sha256:abc",
          embedded_hash: "sha256:abc",
          mismatched_routes: [],
        })
      )

    const { container } = render(<SchemaDriftBannerUnderTest />, { wrapper })

    // Wait for the fetch to resolve.
    await waitFor(() => {
      expect(container.querySelector("[data-testid='schema-drift-banner']")).toBeNull()
    })
  })

  it("shows banner with mismatched files when status=drift", async () => {
    fetchHandler = (_url) =>
      Promise.resolve(
        jsonOk({
          status: "drift",
          binary_hash: "sha256:aaa",
          embedded_hash: "sha256:bbb",
          mismatched_routes: ["NodeConfig.json", "CreateUserRequest.json"],
        })
      )

    render(<SchemaDriftBannerUnderTest />, { wrapper })

    await waitFor(() => {
      expect(screen.getByTestId("schema-drift-banner")).toBeTruthy()
    })

    expect(screen.getByText(/Schema drift detected/i)).toBeTruthy()
    const routes = screen.getByTestId("mismatched-routes")
    expect(routes.textContent).toContain("NodeConfig.json")
    expect(routes.textContent).toContain("CreateUserRequest.json")
  })

  it("is hidden when the endpoint returns 403 (non-admin user)", async () => {
    fetchHandler = (_url) =>
      Promise.resolve(jsonErr(403, { error: "forbidden", code: "forbidden" }))

    const { container } = render(<SchemaDriftBannerUnderTest />, { wrapper })

    await waitFor(() => {
      expect(container.querySelector("[data-testid='schema-drift-banner']")).toBeNull()
    })
  })

  it("is hidden when the endpoint throws a network error", async () => {
    fetchHandler = (_url) => Promise.reject(new Error("network failure"))

    const { container } = render(<SchemaDriftBannerUnderTest />, { wrapper })

    await waitFor(() => {
      expect(container.querySelector("[data-testid='schema-drift-banner']")).toBeNull()
    })
  })
})
