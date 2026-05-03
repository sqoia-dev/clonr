/**
 * connection-provider.test.tsx — UX-4 ConnectionProvider tests.
 *
 * Verifies:
 *   1. useEventSubscription delivers events to the correct topic callback.
 *   2. useEventInvalidation calls invalidateQueries when an event arrives.
 *   3. useConnectionStatus returns status transitions.
 *   4. Subscribers auto-unsubscribe on unmount (no calls after unmount).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, act } from "@testing-library/react"
import * as React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ConnectionProvider, useEventSubscription, useEventInvalidation, useConnectionStatus } from "../contexts/connection"

// ─── Fake EventSource ──────────────────────────────────────────────────────────
//
// jsdom doesn't implement EventSource. We replace it with a controllable fake
// that lets tests fire named events and trigger onerror.

interface FakeEventSourceInstance {
  url: string
  withCredentials?: boolean
  onopen: (() => void) | null
  onerror: (() => void) | null
  listeners: Map<string, ((ev: { data: string }) => void)[]>
  addEventListener: (type: string, handler: (ev: { data: string }) => void) => void
  close: () => void
  // test helpers
  _fireOpen: () => void
  _fireEvent: (type: string, data: unknown) => void
  _fireError: () => void
}

let lastFakeES: FakeEventSourceInstance | null = null

class FakeEventSource {
  url: string
  withCredentials?: boolean
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  listeners: Map<string, ((ev: { data: string }) => void)[]> = new Map()
  closed = false

  constructor(url: string, init?: { withCredentials?: boolean }) {
    this.url = url
    this.withCredentials = init?.withCredentials
    lastFakeES = this as unknown as FakeEventSourceInstance
  }

  addEventListener(type: string, handler: (ev: { data: string }) => void) {
    if (!this.listeners.has(type)) this.listeners.set(type, [])
    this.listeners.get(type)!.push(handler)
  }

  close() {
    this.closed = true
  }

  _fireOpen() {
    this.onopen?.()
  }

  _fireEvent(type: string, data: unknown) {
    const handlers = this.listeners.get(type) ?? []
    for (const h of handlers) h({ data: JSON.stringify(data) })
  }

  _fireError() {
    this.onerror?.()
  }
}

beforeEach(() => {
  lastFakeES = null
  vi.stubGlobal("EventSource", FakeEventSource)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// ─── Helpers ───────────────────────────────────────────────────────────────────

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function Wrapper({ children, qc }: { children: React.ReactNode; qc: QueryClient }) {
  return (
    <QueryClientProvider client={qc}>
      <ConnectionProvider>{children}</ConnectionProvider>
    </QueryClientProvider>
  )
}

// ─── Test 1: useEventSubscription ─────────────────────────────────────────────

describe("useEventSubscription", () => {
  it("calls callback when a matching event fires", async () => {
    const received: unknown[] = []

    function Sub() {
      useEventSubscription("nodes", (data) => received.push(data))
      return null
    }

    const qc = makeQueryClient()
    render(<Wrapper qc={qc}><Sub /></Wrapper>)

    await act(async () => {
      lastFakeES?._fireOpen()
      lastFakeES?._fireEvent("nodes", { node_id: "n1" })
    })

    expect(received).toHaveLength(1)
    expect((received[0] as Record<string, string>).node_id).toBe("n1")
  })

  it("does not call callback for a different topic", async () => {
    const received: unknown[] = []

    function Sub() {
      useEventSubscription("images", (data) => received.push(data))
      return null
    }

    const qc = makeQueryClient()
    render(<Wrapper qc={qc}><Sub /></Wrapper>)

    await act(async () => {
      lastFakeES?._fireOpen()
      lastFakeES?._fireEvent("nodes", { node_id: "n2" }) // wrong topic
    })

    expect(received).toHaveLength(0)
  })

  it("stops receiving after unmount", async () => {
    const received: unknown[] = []

    function Sub() {
      useEventSubscription("groups", (data) => received.push(data))
      return null
    }

    const qc = makeQueryClient()
    const { unmount } = render(<Wrapper qc={qc}><Sub /></Wrapper>)

    await act(async () => {
      lastFakeES?._fireOpen()
    })

    unmount()

    await act(async () => {
      lastFakeES?._fireEvent("groups", { group_id: "g1" })
    })

    expect(received).toHaveLength(0)
  })
})

// ─── Test 2: useEventInvalidation ─────────────────────────────────────────────

describe("useEventInvalidation", () => {
  it("calls invalidateQueries when event fires", async () => {
    const qc = makeQueryClient()
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries")

    function Sub() {
      useEventInvalidation("images", ["images"])
      return null
    }

    render(<Wrapper qc={qc}><Sub /></Wrapper>)

    await act(async () => {
      lastFakeES?._fireOpen()
      lastFakeES?._fireEvent("images", { image_id: "img1" })
    })

    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["images"] })
  })
})

// ─── Test 3: useConnectionStatus ──────────────────────────────────────────────

describe("useConnectionStatus", () => {
  it("transitions from connecting to open on ES onopen", async () => {
    let statusCapture = ""

    function StatusWatcher() {
      const { status } = useConnectionStatus()
      statusCapture = status
      return <span data-testid="status">{status}</span>
    }

    const qc = makeQueryClient()
    const { getByTestId } = render(<Wrapper qc={qc}><StatusWatcher /></Wrapper>)

    // Initial state
    expect(["connecting", "reconnecting"]).toContain(getByTestId("status").textContent)

    await act(async () => {
      lastFakeES?._fireOpen()
    })

    expect(getByTestId("status").textContent).toBe("open")
    expect(statusCapture).toBe("open")
  })
})
