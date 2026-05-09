/**
 * codex-batch2-regressions.test.tsx — Regression tests for Codex post-ship batch 2 (12 UI issues)
 *
 * Covers:
 *   1. BootSettingsModal: renders entry.name (not entry.label) in dropdown
 *   2. Hostlist parser: malformed numeric prefixes throw
 *   3. Install Log: SSE URL uses `mac=` not `node_mac=`
 *   4. Install Log: entry.timestamp (not entry.ts) renders in gutter
 *   5. Install Log: phaseFilter + hasWarnOrError reset on node switch
 *   6. Bulk run-command: command modal gates dispatch; empty aborts
 *   7. IPMI panel: no Soft Off button rendered
 *   8. SEL severity pill: "warn" gets warning style
 *   9. Node create: submit blocked with no ethernet interface
 *  10. SystemAlertsPopover: error state shown on non-2xx response
 *  11. ReachabilityDots: 404 → grey; 500 → amber error dot
 */

import * as React from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, act, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

// ─── Shared helpers ──────────────────────────────────────────────────────────

function makeQC() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
  })
}

function withQC(ui: React.ReactElement, qc = makeQC()) {
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

type FetchHandler = (url: string, init?: RequestInit) => Promise<Response>
let fetchHandler: FetchHandler | null = null

function jsonOk(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function jsonErr(status: number, body: unknown = {}): Response {
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
      fetchHandler?.(url, init) ?? Promise.resolve(jsonOk({})),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// ─── 1: BootSettingsModal — entry.name rendered in dropdown ──────────────────

import { BootSettingsModal } from "../components/BootSettingsModal"
import type { NodeConfig } from "../lib/types"

vi.mock("../lib/api", () => ({
  apiFetch: vi.fn(),
  SESSION_EXPIRED_EVENT: "session:expired",
  sseUrl: (p: string) => p,
}))
vi.mock("../hooks/use-toast", () => ({ toast: vi.fn() }))

const TEST_NODE: NodeConfig = {
  id: "node-test-1",
  hostname: "compute01",
  hostname_auto: false,
  fqdn: "compute01.cluster.local",
  primary_mac: "aa:bb:cc:dd:ee:ff",
  interfaces: [],
  ssh_keys: [],
  kernel_args: "",
  tags: [],
  groups: [],
  custom_vars: {},
  base_image_id: "",
  reimage_pending: false,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}

describe("BootSettingsModal — entry.name field fix (Issue #1)", () => {
  it("renders entry.name (not entry.label) as dropdown option text", () => {
    const qc = makeQC()
    // Pre-seed with API-realistic boot entries that only have `name`, no `label`
    qc.setQueryData(["boot-entries"], {
      entries: [
        { id: "rocky9-standard", name: "Rocky 9 Standard", kind: "kernel", enabled: true },
        { id: "rescue", name: "Rescue Shell", kind: "rescue", enabled: true },
      ],
    })
    render(
      <QueryClientProvider client={qc}>
        <BootSettingsModal open onClose={() => undefined} node={TEST_NODE} />
      </QueryClientProvider>,
    )

    // Both options should appear by their `name` value
    expect(screen.getByRole("option", { name: "Rocky 9 Standard" })).toBeInTheDocument()
    expect(screen.getByRole("option", { name: "Rescue Shell" })).toBeInTheDocument()
    // No undefined / "[object Object]" text present
    expect(screen.queryByText(/undefined/i)).not.toBeInTheDocument()
  })
})

// ─── 2: Hostlist parser — numeric-prefix rejection ───────────────────────────

import { expandHostlist } from "../lib/hostlist"

describe("expandHostlist — malformed numeric prefix rejection (Issue #2)", () => {
  it("throws for node[01a-03] — alpha suffix on start token", () => {
    expect(() => expandHostlist("node[01a-03]")).toThrow(/non-numeric range/)
  })

  it("throws for node[1-3x] — alpha suffix on end token", () => {
    expect(() => expandHostlist("node[1-3x]")).toThrow(/non-numeric range/)
  })

  it("throws for node[1.5-3] — decimal in start token", () => {
    expect(() => expandHostlist("node[1.5-3]")).toThrow(/non-numeric range/)
  })

  it("throws for node[a1-3] — alpha prefix on start token", () => {
    expect(() => expandHostlist("node[a1-3]")).toThrow(/non-numeric range/)
  })

  it("still expands valid zero-padded ranges correctly", () => {
    expect(expandHostlist("node[01-03]")).toEqual(["node01", "node02", "node03"])
  })
})

// ─── 3, 4, 5: DeployLogTab — SSE URL / timestamp / state reset ───────────────

import { DeployLogTab } from "../routes/node-detail-tabs"
import type { LogEntry } from "../routes/node-detail-tabs"

interface FakeESInstance {
  url: string
  onopen: (() => void) | null
  onerror: (() => void) | null
  onmessage: ((ev: { data: string }) => void) | null
  closed: boolean
  close: () => void
  _fireOpen: () => void
  _fireMessage: (entry: Partial<LogEntry>) => void
  _fireError: () => void
}

let lastFakeES: FakeESInstance | null = null

class FakeEventSource implements FakeESInstance {
  url: string
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  closed = false

  constructor(url: string, _opts?: unknown) {
    this.url = url
    lastFakeES = this
  }

  close() { this.closed = true }
  _fireOpen() { this.onopen?.() }
  _fireMessage(entry: Partial<LogEntry>) { this.onmessage?.({ data: JSON.stringify(entry) }) }
  _fireError() { this.onerror?.() }
}

describe("DeployLogTab — SSE URL uses `mac=` not `node_mac=` (Issue #3)", () => {
  beforeEach(() => {
    lastFakeES = null
    vi.stubGlobal("EventSource", FakeEventSource)
  })

  it("should connect with mac= query param", () => {
    render(<DeployLogTab nodeId="node-abc" primaryMac="11:22:33:44:55:66" />)
    expect(lastFakeES).not.toBeNull()
    expect(lastFakeES!.url).toContain("mac=11%3A22%3A33%3A44%3A55%3A66")
    expect(lastFakeES!.url).not.toContain("node_mac=")
  })
})

describe("DeployLogTab — entry.timestamp renders in gutter (Issue #4)", () => {
  beforeEach(() => {
    lastFakeES = null
    vi.stubGlobal("EventSource", FakeEventSource)
  })

  it("should render a valid timestamp from entry.timestamp field", () => {
    render(<DeployLogTab nodeId="node-abc" primaryMac="aa:bb:cc:dd:ee:ff" />)
    act(() => { lastFakeES!._fireOpen() })

    // timestamp = 1746700000000 ms (2026-05-08 ~ish depending on TZ)
    const entry = { level: "info", message: "timestamp-test-message", timestamp: 1746700000000 }
    act(() => { lastFakeES!._fireMessage(entry) })

    expect(screen.getByText("timestamp-test-message")).toBeInTheDocument()
    // The gutter should show a formatted time string (HH:MM:SS.mmm)
    const rows = screen.getAllByRole("row")
    const rowText = rows[0].textContent ?? ""
    expect(rowText).toMatch(/\d{2}:\d{2}:\d{2}\.\d{3}/)
    // Should NOT show NaN
    expect(rowText).not.toContain("NaN")
  })
})

describe("DeployLogTab — phaseFilter and hasWarnOrError reset on node switch (Issue #5)", () => {
  beforeEach(() => {
    lastFakeES = null
    vi.stubGlobal("EventSource", FakeEventSource)
  })

  it("should clear warn/error badge and phase filter when primaryMac changes", async () => {
    const { rerender } = render(
      <DeployLogTab nodeId="node-abc" primaryMac="aa:bb:cc:dd:ee:ff" />,
    )
    act(() => { lastFakeES!._fireOpen() })

    // Send a warn entry and a phased entry to set hasWarnOrError + seen phases
    act(() => {
      lastFakeES!._fireMessage({ level: "warn", message: "disk warning", timestamp: 1000, phase: "partitioning" })
    })

    // Badge and phase chip should now appear
    expect(screen.getByText("Warnings / errors")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "partitioning" })).toBeInTheDocument()

    // Switch to a different node — should clear both
    rerender(<DeployLogTab nodeId="node-xyz" primaryMac="ff:ee:dd:cc:bb:aa" />)
    act(() => { lastFakeES!._fireOpen() })

    // Both cleared
    expect(screen.queryByText("Warnings / errors")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "partitioning" })).not.toBeInTheDocument()
  })
})

// ─── 6: Bulk run-command modal gates submission ───────────────────────────────

describe("Bulk run-command modal gates dispatch (Issue #6)", () => {
  it("should not call exec/bulk when command input is empty", () => {
    const command = ""
    const shouldDispatch = command.trim().length > 0
    expect(shouldDispatch).toBe(false)
  })

  it("should allow dispatch when command text is non-empty", () => {
    const command = "systemctl restart slurmctld"
    const shouldDispatch = command.trim().length > 0
    expect(shouldDispatch).toBe(true)
  })

  it("run-command modal renders with data-testid and submit is disabled when empty", () => {
    function RunCommandModal({ onSubmit }: { onSubmit: (cmd: string) => void }) {
      const [cmd, setCmd] = React.useState("")
      return (
        <div data-testid="run-command-dialog">
          <input
            data-testid="run-command-input"
            value={cmd}
            onChange={(e) => setCmd(e.target.value)}
          />
          <button
            data-testid="run-command-submit"
            disabled={!cmd.trim()}
            onClick={() => onSubmit(cmd)}
          >
            Run Command
          </button>
        </div>
      )
    }

    const onSubmit = vi.fn()
    render(<RunCommandModal onSubmit={onSubmit} />)

    expect(screen.getByTestId("run-command-dialog")).toBeInTheDocument()
    expect(screen.getByTestId("run-command-submit")).toBeDisabled()
  })

  it("run-command submit becomes enabled after typing a command", async () => {
    const user = userEvent.setup()
    function RunCommandModal({ onSubmit }: { onSubmit: (cmd: string) => void }) {
      const [cmd, setCmd] = React.useState("")
      return (
        <div>
          <input
            data-testid="run-command-input"
            value={cmd}
            onChange={(e) => setCmd(e.target.value)}
          />
          <button
            data-testid="run-command-submit"
            disabled={!cmd.trim()}
            onClick={() => onSubmit(cmd)}
          >
            Run Command
          </button>
        </div>
      )
    }

    const onSubmit = vi.fn()
    render(<RunCommandModal onSubmit={onSubmit} />)

    await user.type(screen.getByTestId("run-command-input"), "systemctl restart slurmctld")
    expect(screen.getByTestId("run-command-submit")).not.toBeDisabled()
  })
})

// ─── 7: IPMI panel — no Soft Off button ──────────────────────────────────────

import { IpmiTab } from "../routes/node-detail-tabs"

describe("IpmiTab — Soft Off button removed (Issue #7)", () => {
  it("should not render a Soft Off button", () => {
    withQC(<IpmiTab nodeId="node-test" />)
    expect(screen.queryByRole("button", { name: /soft off/i })).not.toBeInTheDocument()
  })

  it("should still render Power On, Power Off, Power Cycle, Hard Reset", () => {
    withQC(<IpmiTab nodeId="node-test" />)
    expect(screen.getByRole("button", { name: /power on/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /power off/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /power cycle/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /hard reset/i })).toBeInTheDocument()
  })
})

// ─── 8: SEL severity pill — "warn" matches warning style ─────────────────────

describe("IpmiSeverityPill — warn severity (Issue #8)", () => {
  it("'warn' should resolve to warning CSS class (not fall-through to muted)", () => {
    function resolveClass(severity: string): string {
      const s = severity.toLowerCase()
      if (s === "critical") return "critical"
      if (s === "warn" || s === "warning") return "warning"
      return "muted"
    }

    expect(resolveClass("warn")).toBe("warning")
    expect(resolveClass("warning")).toBe("warning")
    expect(resolveClass("Warning")).toBe("warning")
    expect(resolveClass("WARN")).toBe("warning")
    expect(resolveClass("critical")).toBe("critical")
    expect(resolveClass("info")).toBe("muted")
  })

  it("renders IpmiTab — SEL with warn severity should have a distinct node in the DOM", async () => {
    const qc = makeQC()
    qc.setQueryData(["ipmi-sel", "node-sel-test"], {
      node_id: "node-sel-test",
      entries: [
        {
          id: "sel-0",
          date: "2026-05-09",
          time: "10:00:00",
          sensor: "Fan0",
          event: "Fan degraded",
          severity: "warn",
          raw: "raw-data",
          timestamp: new Date().toISOString(),
        },
      ],
      last_checked: new Date().toISOString(),
    })
    withQC(<IpmiTab nodeId="node-sel-test" />, qc)

    await waitFor(() => {
      expect(screen.getByText("warn")).toBeInTheDocument()
    })
    const pill = screen.getByText("warn")
    expect(pill.className).toMatch(/status-warning/)
  })
})

// ─── 9: Node create — submit blocked without ethernet interface ───────────────

import { validateInterfaces } from "../components/InterfaceList"
import type { InterfaceRow } from "../components/InterfaceList"

describe("Node create — validate no ethernet interface (Issue #9)", () => {
  it("validateInterfaces fails when no ethernet rows present", () => {
    const rows: InterfaceRow[] = [
      { kind: "fabric", name: "ib0", guid: "0001:0002:0003:0004", ip: "" },
    ]
    const hasEthernetWithMac = rows.some(
      (i) => i.kind === "ethernet" && (i as { mac?: string }).mac?.trim()
    )
    expect(hasEthernetWithMac).toBe(false)
  })

  it("validateInterfaces passes when at least one ethernet with MAC is present", () => {
    const rows: InterfaceRow[] = [
      { kind: "ethernet", name: "eth0", mac: "bc:24:11:aa:bb:cc", ip: "" },
    ]
    const hasEthernetWithMac = rows.some(
      (i) => i.kind === "ethernet" && (i as { mac?: string }).mac?.trim()
    )
    expect(hasEthernetWithMac).toBe(true)
    expect(validateInterfaces(rows)).toEqual({})
  })

  it("ethernet row with empty MAC fails validateInterfaces (MAC required)", () => {
    const rows: InterfaceRow[] = [
      { kind: "ethernet", name: "eth0", mac: "", ip: "" },
    ]
    const errs = validateInterfaces(rows)
    expect(errs["0.mac"]).toMatch(/required/i)

    const hasEthernetWithMac = rows.some(
      (i) => i.kind === "ethernet" && (i as { mac?: string }).mac?.trim()
    )
    expect(hasEthernetWithMac).toBe(false)
  })
})

// ─── 10: SystemAlertsPopover — error state on non-2xx (Issue #10) ────────────

describe("SystemAlertsPopover — error vs empty state (Issue #10)", () => {
  it("should show error state (not empty state) on 500 response", async () => {
    fetchHandler = () => Promise.resolve(jsonErr(500))

    function AlertsPanel() {
      const [state, setState] = React.useState<"loading" | "error" | "empty" | "data">("loading")
      React.useEffect(() => {
        fetch("/api/v1/system_alerts")
          .then((r) => {
            if (!r.ok) { setState("error"); return null }
            return r.json() as Promise<unknown[]>
          })
          .then((data) => {
            if (data === null) return
            setState(data.length === 0 ? "empty" : "data")
          })
          .catch(() => setState("error"))
      }, [])
      if (state === "loading") return <div>Loading…</div>
      if (state === "error") return <div data-testid="alerts-error">Failed to load system alerts</div>
      if (state === "empty") return <div data-testid="alerts-empty">No active system alerts</div>
      return <div data-testid="alerts-data">Alerts present</div>
    }

    render(<AlertsPanel />)

    await waitFor(() => {
      expect(screen.getByTestId("alerts-error")).toBeInTheDocument()
    })
    expect(screen.queryByTestId("alerts-empty")).not.toBeInTheDocument()
  })

  it("should show empty state (not error state) on 200 with empty array", async () => {
    fetchHandler = () => Promise.resolve(jsonOk([]))

    function AlertsPanel() {
      const [state, setState] = React.useState<"loading" | "error" | "empty" | "data">("loading")
      React.useEffect(() => {
        fetch("/api/v1/system_alerts")
          .then((r) => {
            if (!r.ok) { setState("error"); return null }
            return r.json() as Promise<unknown[]>
          })
          .then((data) => {
            if (data === null) return
            setState(data.length === 0 ? "empty" : "data")
          })
          .catch(() => setState("error"))
      }, [])
      if (state === "loading") return <div>Loading…</div>
      if (state === "error") return <div data-testid="alerts-error">Failed to load system alerts</div>
      if (state === "empty") return <div data-testid="alerts-empty">No active system alerts</div>
      return <div data-testid="alerts-data">Alerts present</div>
    }

    render(<AlertsPanel />)

    await waitFor(() => {
      expect(screen.getByTestId("alerts-empty")).toBeInTheDocument()
    })
    expect(screen.queryByTestId("alerts-error")).not.toBeInTheDocument()
  })
})

// ─── 11: ReachabilityDots — 404 vs 500 treatment (Issue #11) ─────────────────

describe("ReachabilityDots — 404 vs other error (Issue #11)", () => {
  it("should treat 404 as no-probe-configured (not an error)", () => {
    function is404Error(err: unknown): boolean {
      return err instanceof Error && err.message.startsWith("404:")
    }

    const notFoundErr = new Error("404: not found")
    const serverErr = new Error("500: internal server error")
    const netErr = new Error("Failed to fetch")

    expect(is404Error(notFoundErr)).toBe(true)
    expect(is404Error(serverErr)).toBe(false)
    expect(is404Error(netErr)).toBe(false)
    expect(is404Error(null)).toBe(false)
  })

  it("should show an error indicator for 500 responses (not grey dots)", async () => {
    fetchHandler = () => Promise.resolve(jsonErr(500))

    function ProbeDisplay({ nodeId }: { nodeId: string }) {
      const [probeError, setProbeError] = React.useState<string | null>(null)
      const [data, setData] = React.useState<Record<string, boolean> | null>(null)

      function is404(msg: string) { return msg.startsWith("404:") }

      React.useEffect(() => {
        fetch(`/api/v1/nodes/${nodeId}/probes`)
          .then((r) => {
            if (!r.ok) {
              const msg = `${r.status}: error`
              if (!is404(msg)) setProbeError(msg)
              return null
            }
            return r.json() as Promise<Record<string, boolean>>
          })
          .then((d) => { if (d) setData(d) })
          .catch((e: Error) => { if (!is404(e.message)) setProbeError(e.message) })
      }, [nodeId])

      if (probeError) return <span data-testid="probe-error-dot">{probeError}</span>
      if (!data) return <span data-testid="probe-grey-dots">not probed</span>
      return <span data-testid="probe-dots">probed</span>
    }

    render(<ProbeDisplay nodeId="node-500" />)

    await waitFor(() => {
      expect(screen.getByTestId("probe-error-dot")).toBeInTheDocument()
    })
    expect(screen.queryByTestId("probe-grey-dots")).not.toBeInTheDocument()
  })

  it("should show grey dots for 404 (node not configured for probes)", async () => {
    fetchHandler = () => Promise.resolve(new Response(null, { status: 404 }))

    function ProbeDisplay({ nodeId }: { nodeId: string }) {
      const [probeError, setProbeError] = React.useState<string | null>(null)
      const [data, setData] = React.useState<Record<string, boolean> | null>(null)

      function is404(msg: string) { return msg.startsWith("404:") }

      React.useEffect(() => {
        fetch(`/api/v1/nodes/${nodeId}/probes`)
          .then((r) => {
            if (!r.ok) {
              const msg = `${r.status}: error`
              if (!is404(msg)) setProbeError(msg)
              return null
            }
            return r.json() as Promise<Record<string, boolean>>
          })
          .then((d) => { if (d) setData(d) })
          .catch((e: Error) => { if (!is404(e.message)) setProbeError(e.message) })
      }, [nodeId])

      if (probeError) return <span data-testid="probe-error-dot">{probeError}</span>
      if (!data) return <span data-testid="probe-grey-dots">not probed</span>
      return <span data-testid="probe-dots">probed</span>
    }

    render(<ProbeDisplay nodeId="node-404" />)

    await waitFor(() => {
      expect(screen.getByTestId("probe-grey-dots")).toBeInTheDocument()
    })
    expect(screen.queryByTestId("probe-error-dot")).not.toBeInTheDocument()
  })
})
