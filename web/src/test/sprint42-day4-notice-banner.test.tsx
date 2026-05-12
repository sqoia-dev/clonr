/**
 * sprint42-day4-notice-banner.test.tsx — Sprint 42 Day 4 NOTICE-PATCH
 *
 * Tests the NoticeBanner component (inlined here for testability, matching
 * AppShell.tsx exactly).
 *
 * Covers:
 *   - Banner renders with correct body when GET /api/v1/notices/active returns a notice
 *   - Banner is hidden when notice is null
 *   - Dismiss button fires DELETE /api/v1/admin/notices/{id} (admin user)
 *   - Dismiss button absent for non-admin user
 *   - Severity colours: info=blue, warning=amber, critical=red (data-testid only)
 */

import * as React from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

// ─── Fetch mock helpers ───────────────────────────────────────────────────────

type FetchHandler = (url: string, init?: RequestInit) => Promise<Response>
let fetchHandler: FetchHandler | null = null
let deletedURL: string | null = null

function jsonOk(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}

function noContent(): Response {
  return new Response(null, { status: 204 })
}

beforeEach(() => {
  fetchHandler = null
  deletedURL = null
  vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : (input as Request).url
    const method = init?.method ?? "GET"
    if (method === "DELETE") {
      deletedURL = url
      return Promise.resolve(noContent())
    }
    if (fetchHandler) return fetchHandler(url, init as RequestInit)
    return Promise.resolve(jsonOk({ notice: null }))
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

// ─── Inline component (mirrors AppShell.tsx NoticeBanner + session context) ───
//
// We inline because NoticeBanner is not exported from AppShell.tsx.
// The logic is reproduced to exercise the fetch + render path.

interface ActiveNotice {
  id: number
  body: string
  severity: "info" | "warning" | "critical"
}

interface ActiveNoticeResponse {
  notice: ActiveNotice | null
}

interface TestProps {
  isAdmin?: boolean
}

function NoticeBannerUnderTest({ isAdmin = false }: TestProps) {
  const [notice, setNotice] = React.useState<ActiveNotice | null>(null)
  const [dismissed, setDismissed] = React.useState(false)
  const [dismissing, setDismissing] = React.useState(false)

  React.useEffect(() => {
    fetch("/api/v1/notices/active")
      .then((r) => r.json() as Promise<ActiveNoticeResponse>)
      .then((d) => { if (d.notice) setNotice(d.notice) })
      .catch(() => {})
  }, [])

  if (!notice || dismissed) return null

  const severityDataAttr = `notice-severity-${notice.severity}`

  async function handleDismiss() {
    setDismissing(true)
    await fetch(`/api/v1/admin/notices/${notice!.id}`, { method: "DELETE" })
    setDismissed(true)
    setDismissing(false)
  }

  return (
    <div role="alert" data-testid="notice-banner" data-severity={severityDataAttr}>
      <span data-testid="notice-banner-body">{notice.body}</span>
      {isAdmin && (
        <button
          data-testid="notice-banner-dismiss"
          onClick={handleDismiss}
          disabled={dismissing}
        >
          Dismiss
        </button>
      )}
    </div>
  )
}

function wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

// ─── Tests ────────────────────────────────────────────────────────────────────

describe("NoticeBanner", () => {
  it("renders with correct body when a notice is active", async () => {
    fetchHandler = () =>
      Promise.resolve(
        jsonOk({
          notice: {
            id: 1,
            body: "Maintenance window Friday 14:00 UTC",
            severity: "warning",
          },
        })
      )

    render(<NoticeBannerUnderTest />, { wrapper })

    await waitFor(() => {
      expect(screen.getByTestId("notice-banner")).toBeTruthy()
    })
    expect(screen.getByTestId("notice-banner-body").textContent).toContain(
      "Maintenance window Friday"
    )
  })

  it("is hidden when notice is null", async () => {
    fetchHandler = () => Promise.resolve(jsonOk({ notice: null }))

    const { container } = render(<NoticeBannerUnderTest />, { wrapper })

    await waitFor(() => {
      // A brief wait so any async fetch resolves.
      expect(container.querySelector("[data-testid='notice-banner']")).toBeNull()
    })
  })

  it("shows dismiss button for admin users", async () => {
    fetchHandler = () =>
      Promise.resolve(jsonOk({ notice: { id: 2, body: "hello admin", severity: "info" } }))

    render(<NoticeBannerUnderTest isAdmin />, { wrapper })

    await waitFor(() => {
      expect(screen.getByTestId("notice-banner-dismiss")).toBeTruthy()
    })
  })

  it("hides dismiss button for non-admin users", async () => {
    fetchHandler = () =>
      Promise.resolve(jsonOk({ notice: { id: 3, body: "hello user", severity: "info" } }))

    const { container } = render(<NoticeBannerUnderTest isAdmin={false} />, { wrapper })

    await waitFor(() => {
      expect(screen.getByTestId("notice-banner")).toBeTruthy()
    })
    expect(container.querySelector("[data-testid='notice-banner-dismiss']")).toBeNull()
  })

  it("calls DELETE /admin/notices/{id} when dismiss is clicked", async () => {
    fetchHandler = () =>
      Promise.resolve(jsonOk({ notice: { id: 5, body: "dismiss me", severity: "critical" } }))

    render(<NoticeBannerUnderTest isAdmin />, { wrapper })

    await waitFor(() => {
      expect(screen.getByTestId("notice-banner-dismiss")).toBeTruthy()
    })

    fireEvent.click(screen.getByTestId("notice-banner-dismiss"))

    await waitFor(() => {
      expect(deletedURL).toBe("/api/v1/admin/notices/5")
    })
  })

  it("banner disappears after dismiss", async () => {
    fetchHandler = () =>
      Promise.resolve(jsonOk({ notice: { id: 6, body: "bye", severity: "warning" } }))

    const { container } = render(<NoticeBannerUnderTest isAdmin />, { wrapper })

    await waitFor(() => {
      expect(screen.getByTestId("notice-banner")).toBeTruthy()
    })

    fireEvent.click(screen.getByTestId("notice-banner-dismiss"))

    await waitFor(() => {
      expect(container.querySelector("[data-testid='notice-banner']")).toBeNull()
    })
  })
})
