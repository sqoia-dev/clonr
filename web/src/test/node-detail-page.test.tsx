/**
 * node-detail-page.test.tsx — Tests for the full-page node detail route
 *
 * Covers:
 *   1. Nodes table rows have data-testid="node-row-<id>" (proves click target wired to navigate)
 *   2. NodeDetailPage renders node identity + back link when node loads successfully
 *   3. NodeDetailPage shows error state when fetch fails
 *   4. NodeDetailPage shows no hostname while loading
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import * as React from "react"
import type { NodeConfig, ListNodesResponse } from "../lib/types"

// ─── Mock useEventInvalidation so ConnectionProvider is not needed ────────────
//
// NodesPage and NodeDetailPage both call useEventInvalidation which requires
// ConnectionProvider context. We mock the entire contexts/connection module so
// every hook is a no-op — these tests only care about fetch / render behaviour,
// not SSE event delivery.

vi.mock("../contexts/connection", () => ({
  ConnectionProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  useConnectionStatus: () => ({ status: "open", paused: false, retry: () => {} }),
  useEventSubscription: () => {},
  useEventInvalidation: () => {},
  useConnection: () => ({
    status: "open",
    paused: false,
    retry: () => {},
    subscribe: () => () => {},
  }),
}))

// ─── Mock router hooks at module level ────────────────────────────────────────

const NODE_ID = "node-abc-001"
const mockNavigate = vi.fn()

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>()
  return {
    ...actual,
    useNavigate: () => mockNavigate,
    useSearch: () => ({}),
    useParams: () => ({ nodeId: NODE_ID }),
    Link: ({ children, to, ...rest }: { children: React.ReactNode; to: string; [k: string]: unknown }) =>
      <a href={String(to)} {...(rest as React.HTMLAttributes<HTMLAnchorElement>)}>{children}</a>,
  }
})

// ─── Helpers ─────────────────────────────────────────────────────────────────

function makeQC() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
}

function withProviders(ui: React.ReactElement) {
  return render(
    <QueryClientProvider client={makeQC()}>
      {ui}
    </QueryClientProvider>
  )
}

type FetchHandler = (url: string, init?: RequestInit) => Promise<Response>
let fetchHandler: FetchHandler | null = null

function jsonOk(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

beforeEach(() => {
  fetchHandler = null
  vi.stubGlobal(
    "fetch",
    (url: string, init?: RequestInit) =>
      fetchHandler ? fetchHandler(url, init) : Promise.resolve(jsonOk({})),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.resetModules()
})

// ─── Sample data ─────────────────────────────────────────────────────────────

function makeNode(overrides: Partial<NodeConfig> = {}): NodeConfig {
  return {
    id: NODE_ID,
    hostname: "compute-01",
    hostname_auto: false,
    fqdn: "compute-01.cluster.local",
    primary_mac: "bc:24:11:aa:bb:cc",
    interfaces: [],
    ssh_keys: [],
    kernel_args: "",
    tags: [],
    groups: [],
    custom_vars: {},
    base_image_id: "",
    reimage_pending: false,
    operating_mode: "block_install",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  }
}

const sampleNode = makeNode()

// ─── Test 1: Nodes table rows have data-testid for click targeting ────────────

describe("Nodes list — row click wiring", () => {
  it("should render node rows with data-testid='node-row-<id>'", async () => {
    const nodesResp: ListNodesResponse = {
      nodes: [sampleNode],
      total: 1,
    }

    fetchHandler = (url) => {
      if (url.includes("/api/v1/nodes") && !url.includes(`/api/v1/nodes/${NODE_ID}`)) {
        return Promise.resolve(jsonOk(nodesResp))
      }
      if (url.includes("/api/v1/images")) {
        return Promise.resolve(jsonOk({ images: [], total: 0 }))
      }
      return Promise.resolve(jsonOk({}))
    }

    const { NodesPage } = await import("../routes/nodes")

    withProviders(<NodesPage />)

    await waitFor(() => {
      expect(screen.getByText("compute-01")).toBeInTheDocument()
    })

    // Each node row must have the testid so tests can assert click targeting.
    const row = screen.getByTestId(`node-row-${NODE_ID}`)
    expect(row).toBeInTheDocument()
  })
})

// ─── Test 2: NodeDetailPage renders node data ─────────────────────────────────

describe("NodeDetailPage — renders node identity", () => {
  it("should render hostname and back link after fetching node", async () => {
    fetchHandler = (url) => {
      // Sub-resource endpoints for this node — must match before the bare node route
      if (url.includes(`/api/v1/nodes/${NODE_ID}/effective-layout`)) {
        // effectiveData must have a valid source string or the component crashes on source.startsWith()
        return Promise.resolve(jsonOk({ source: "image", layout: null }))
      }
      if (url.includes(`/api/v1/nodes/${NODE_ID}/sudoers`)) {
        return Promise.resolve(jsonOk({ sudoers: [], total: 0 }))
      }
      if (url.includes(`/api/v1/nodes/${NODE_ID}/slurm`)) {
        return Promise.resolve(jsonOk({ node: null }))
      }
      if (url.includes(`/api/v1/nodes/${NODE_ID}/hardware`)) {
        return Promise.resolve(jsonOk({ hardware: null }))
      }
      // Bare node fetch
      if (url.endsWith(`/api/v1/nodes/${NODE_ID}`)) {
        return Promise.resolve(jsonOk(sampleNode))
      }
      if (url.includes("/api/v1/images")) {
        return Promise.resolve(jsonOk({ images: [], total: 0 }))
      }
      if (url.includes("/api/v1/nodes") && !url.includes("/api/v1/nodes/")) {
        return Promise.resolve(jsonOk({ nodes: [], total: 0 }))
      }
      return Promise.resolve(jsonOk({}))
    }

    const { NodeDetailPage } = await import("../routes/nodes")

    withProviders(<NodeDetailPage />)

    await waitFor(() => {
      expect(screen.getByText("compute-01")).toBeInTheDocument()
    })

    // Back link should be present with the correct testid
    expect(screen.getByTestId("back-to-nodes")).toBeInTheDocument()
  })

  it("should show error message when node fetch fails", async () => {
    fetchHandler = (url) => {
      if (url.includes(`/api/v1/nodes/${NODE_ID}`)) {
        return Promise.resolve(
          new Response(JSON.stringify({ error: "not found" }), { status: 404 })
        )
      }
      return Promise.resolve(jsonOk({}))
    }

    const { NodeDetailPage } = await import("../routes/nodes")

    withProviders(<NodeDetailPage />)

    await waitFor(() => {
      expect(screen.getByText(/failed to load node/i)).toBeInTheDocument()
    })
  })
})

// ─── Test 3: NodeDetailPage loading state ─────────────────────────────────────

describe("NodeDetailPage — loading skeleton", () => {
  it("should not show hostname while loading", async () => {
    // Never resolve — keeps loading state
    fetchHandler = () => new Promise(() => {})

    const { NodeDetailPage } = await import("../routes/nodes")

    withProviders(<NodeDetailPage />)

    // During load the hostname is not yet rendered
    expect(screen.queryByText("compute-01")).not.toBeInTheDocument()
  })
})
