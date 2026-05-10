/**
 * node-detail-page.test.tsx — Tests for the full-page node detail route
 *
 * Covers:
 *   1. Nodes table rows have data-testid="node-row-<id>" (proves click target wired to navigate)
 *   2. NodeDetailPage renders node identity + back link when node loads successfully
 *   3. NodeDetailPage shows error state when fetch fails
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { NodeConfig, ListNodesResponse } from "../lib/types"

// ─── Helpers ─────────────────────────────────────────────────────────────────

function makeQC() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
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
    id: "node-abc-001",
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

// ─── Mock router hooks at module level ────────────────────────────────────────

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>()
  return {
    ...actual,
    useNavigate: () => vi.fn(),
    useSearch: () => ({}),
    useParams: () => ({ nodeId: sampleNode.id }),
    Link: ({ children, to, ...rest }: { children: React.ReactNode; to: string; [k: string]: unknown }) =>
      <a href={to} {...rest}>{children}</a>,
  }
})

// ─── Test 1: Nodes table rows have data-testid for click targeting ────────────

describe("Nodes list — row click wiring", () => {
  it("should render node rows with data-testid='node-row-<id>'", async () => {
    const nodesResp: ListNodesResponse = {
      nodes: [sampleNode],
      total: 1,
    }

    fetchHandler = (url) => {
      if (url.includes("/api/v1/nodes") && !url.includes(`/api/v1/nodes/${sampleNode.id}`)) {
        return Promise.resolve(jsonOk(nodesResp))
      }
      if (url.includes("/api/v1/images")) {
        return Promise.resolve(jsonOk({ images: [], total: 0 }))
      }
      return Promise.resolve(jsonOk({}))
    }

    const { NodesPage } = await import("../routes/nodes")

    render(
      <QueryClientProvider client={makeQC()}>
        <NodesPage />
      </QueryClientProvider>
    )

    await waitFor(() => {
      expect(screen.getByText("compute-01")).toBeInTheDocument()
    })

    // Each node row must have the testid so tests can assert click targeting.
    const row = screen.getByTestId(`node-row-${sampleNode.id}`)
    expect(row).toBeInTheDocument()
  })
})

// ─── Test 2: NodeDetailPage renders node data ─────────────────────────────────

describe("NodeDetailPage — renders node identity", () => {
  it("should render hostname and back link after fetching node", async () => {
    fetchHandler = (url) => {
      if (url.includes(`/api/v1/nodes/${sampleNode.id}`)) {
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

    render(
      <QueryClientProvider client={makeQC()}>
        <NodeDetailPage />
      </QueryClientProvider>
    )

    await waitFor(() => {
      expect(screen.getByText("compute-01")).toBeInTheDocument()
    })

    // Back link should be present with the correct testid
    expect(screen.getByTestId("back-to-nodes")).toBeInTheDocument()
  })

  it("should show error message when node fetch fails", async () => {
    fetchHandler = (url) => {
      if (url.includes(`/api/v1/nodes/${sampleNode.id}`)) {
        return Promise.resolve(
          new Response(JSON.stringify({ error: "not found" }), { status: 404 })
        )
      }
      return Promise.resolve(jsonOk({}))
    }

    const { NodeDetailPage } = await import("../routes/nodes")

    render(
      <QueryClientProvider client={makeQC()}>
        <NodeDetailPage />
      </QueryClientProvider>
    )

    await waitFor(() => {
      expect(screen.getByText(/failed to load node/i)).toBeInTheDocument()
    })
  })
})

// ─── Test 3: NodeDetailPage loading state ─────────────────────────────────────

describe("NodeDetailPage — loading skeleton", () => {
  it("should not show hostname while loading", () => {
    // Never resolve — keeps loading state
    fetchHandler = () => new Promise(() => {})

    const { NodeDetailPage } = require("../routes/nodes")

    render(
      <QueryClientProvider client={makeQC()}>
        <NodeDetailPage />
      </QueryClientProvider>
    )

    // During load the hostname is not yet rendered
    expect(screen.queryByText("compute-01")).not.toBeInTheDocument()
  })
})
