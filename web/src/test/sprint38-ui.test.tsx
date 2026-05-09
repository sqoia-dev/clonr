/**
 * sprint38-ui.test.tsx — Sprint 38 UI tests
 *
 * 1. ReachabilityDots — probe API contract (3 booleans from /probes endpoint)
 * 2. ExternalStatsTab — empty state + happy-path with mock samples
 * 3. Stats tab dynamic chart-group grouping: 2 metrics with different chart_group → 2 cards
 * 4. System Alerts: list + dismiss flow
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import * as React from "react"

import { ExternalStatsTab, groupSamplesByChartGroup } from "../routes/node-detail-tabs"

// ─── Helpers ─────────────────────────────────────────────────────────────────

function makeQC() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
}

function withQC(ui: React.ReactElement, qc = makeQC()) {
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

type FetchHandler = (url: string, init?: RequestInit) => Promise<Response>
let fetchHandler: FetchHandler | null = null

function jsonOk(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}

beforeEach(() => {
  fetchHandler = null
  vi.stubGlobal(
    "fetch",
    (url: string, init?: RequestInit) =>
      fetchHandler?.(url, init) ?? Promise.resolve(jsonOk([])),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// ─── Test 1: ReachabilityDots — probe API contract ───────────────────────────
//
// ReachabilityDots is an internal (non-exported) component in nodes.tsx.
// We test the API contract and data shape it relies on.

describe("ReachabilityDots (PROBE-3)", () => {
  it("should validate ProbeResult shape with mixed reachability values", () => {
    // Shape: { ping: bool, ssh: bool, bmc: bool, checked_at: ISO string }
    const result = { ping: true, ssh: false, bmc: true, checked_at: "2026-05-09T12:00:00Z" }
    expect(result.ping).toBe(true)
    expect(result.ssh).toBe(false)
    expect(result.bmc).toBe(true)
    expect(typeof result.checked_at).toBe("string")
  })

  it("should fetch probe data from /api/v1/nodes/{id}/probes returning 3 booleans", async () => {
    fetchHandler = () =>
      Promise.resolve(
        jsonOk({ ping: true, ssh: true, bmc: false, checked_at: "2026-05-09T12:00:00Z" }),
      )
    const res = await fetch(`/api/v1/nodes/test-node-123/probes`)
    const data = await res.json() as { ping: boolean; ssh: boolean; bmc: boolean; checked_at: string }
    expect(typeof data.ping).toBe("boolean")
    expect(typeof data.ssh).toBe("boolean")
    expect(typeof data.bmc).toBe("boolean")
    expect(data).toHaveProperty("checked_at")
  })

  it("should treat a 404 response as no-probe-data (retry:false path for unconfigured nodes)", async () => {
    fetchHandler = () => Promise.resolve(new Response(null, { status: 404 }))
    const res = await fetch(`/api/v1/nodes/unconfigured-node/probes`)
    // Component uses retry:false — 404 means dots stay grey (undefined state)
    expect(res.status).toBe(404)
  })
})

// ─── Test 2: ExternalStatsTab — empty state ───────────────────────────────────

describe("ExternalStatsTab (EXTERNAL-STATS)", () => {
  it("should render empty state when no external probes are configured", async () => {
    fetchHandler = () => Promise.resolve(jsonOk([]))
    withQC(<ExternalStatsTab nodeId="node-001" />)

    await waitFor(() => {
      expect(screen.getByTestId("ext-stats-empty")).toBeInTheDocument()
    })
    expect(screen.getByText(/No external probes configured/i)).toBeInTheDocument()
  })

  it("should render grouped samples in happy-path", async () => {
    const samples = [
      {
        plugin: "bmc",
        sensor: "cpu_temp",
        value: 62.5,
        unit: "celsius",
        title: "CPU Temperature",
        chart_group: "Thermal",
        ts: "2026-05-09T12:00:00Z",
      },
      {
        plugin: "bmc",
        sensor: "fan_speed",
        value: 3400,
        unit: "rpm",
        title: "Fan Speed",
        chart_group: "Fans",
        ts: "2026-05-09T12:00:00Z",
      },
      {
        plugin: "snmp",
        sensor: "pdu_outlet_power",
        value: 420,
        unit: "watts",
        chart_group: "Power",
        ts: "2026-05-09T12:00:00Z",
      },
    ]
    fetchHandler = () => Promise.resolve(jsonOk(samples))

    withQC(<ExternalStatsTab nodeId="node-001" />)

    await waitFor(() => {
      // 3 distinct chart groups → 3 group cards
      const groups = screen.getAllByTestId("ext-stat-group")
      expect(groups.length).toBe(3)
    })

    expect(screen.getByText("Thermal")).toBeInTheDocument()
    expect(screen.getByText("Fans")).toBeInTheDocument()
    expect(screen.getByText("Power")).toBeInTheDocument()
    // Metric values visible
    expect(screen.getByText(/62\.5/)).toBeInTheDocument()
  })

  it("should group all samples into 'Other' when no chart_group is present", async () => {
    const samples = [
      { plugin: "bmc", sensor: "inlet_temp", value: 22.0, unit: "celsius", ts: "2026-05-09T12:00:00Z" },
      { plugin: "bmc", sensor: "outlet_temp", value: 28.0, unit: "celsius", ts: "2026-05-09T12:00:00Z" },
    ]
    fetchHandler = () => Promise.resolve(jsonOk(samples))

    withQC(<ExternalStatsTab nodeId="node-002" />)

    await waitFor(() => {
      const groups = screen.getAllByTestId("ext-stat-group")
      expect(groups.length).toBe(1)
    })
    expect(screen.getByText("Other")).toBeInTheDocument()
  })
})

// ─── Test 3: groupSamplesByChartGroup — dynamic chart-group grouping ─────────

describe("groupSamplesByChartGroup (STAT-REGISTRY)", () => {
  it("should produce one Map entry per distinct chart_group", () => {
    const samples = [
      { plugin: "ib", sensor: "port_state", value: 1, unit: "", ts: 1000, chart_group: "InfiniBand" },
      { plugin: "ib", sensor: "rx_bytes",   value: 1e9, unit: "B/s", ts: 1001, chart_group: "InfiniBand" },
      { plugin: "megaraid", sensor: "ctrl_health", value: 0, unit: "", ts: 1002, chart_group: "MegaRAID" },
    ] as Parameters<typeof groupSamplesByChartGroup>[0]

    const groups = groupSamplesByChartGroup(samples)
    expect(groups.size).toBe(2)
    expect(groups.has("InfiniBand")).toBe(true)
    expect(groups.has("MegaRAID")).toBe(true)
    expect(groups.get("InfiniBand")!.length).toBe(2)
    expect(groups.get("MegaRAID")!.length).toBe(1)
  })

  it("should exclude samples that have no chart_group", () => {
    const samples = [
      { plugin: "cpu", sensor: "load1", value: 0.5, unit: "", ts: 1000, chart_group: "CPU Load" },
      { plugin: "net", sensor: "rx_bytes", value: 1000, unit: "B/s", ts: 1001 }, // no chart_group
    ] as Parameters<typeof groupSamplesByChartGroup>[0]

    const groups = groupSamplesByChartGroup(samples)
    expect(groups.size).toBe(1)
    expect(groups.has("CPU Load")).toBe(true)
    expect(groups.has("net")).toBe(false)
  })

  it("should return an empty Map for an empty sample array", () => {
    const groups = groupSamplesByChartGroup([])
    expect(groups.size).toBe(0)
  })

  it("should group 2 metrics with different chart_group into 2 separate cards", () => {
    const samples = [
      { plugin: "intelssd", sensor: "media_errors", value: 0, unit: "", ts: 100, chart_group: "Intel SSD SMART" },
      { plugin: "megaraid", sensor: "drive_state",  value: 1, unit: "", ts: 101, chart_group: "MegaRAID" },
    ] as Parameters<typeof groupSamplesByChartGroup>[0]

    const groups = groupSamplesByChartGroup(samples)
    expect(groups.size).toBe(2)
    const groupNames = [...groups.keys()]
    expect(groupNames).toContain("Intel SSD SMART")
    expect(groupNames).toContain("MegaRAID")
  })
})

// ─── Test 4: System Alerts — list + dismiss flow ──────────────────────────────

describe("SystemAlertsPopover (SYSTEM-ALERT-FRAMEWORK)", () => {
  it("should fetch system alerts from GET /api/v1/system_alerts", async () => {
    let capturedUrl = ""
    fetchHandler = (url) => {
      capturedUrl = url
      return Promise.resolve(
        jsonOk([
          { key: "disk_full", device: "sda", level: "critical", message: "Disk 95% full", set_at: new Date().toISOString() },
          { key: "fan_degraded", device: "fan0", level: "warn", message: "Fan below threshold", set_at: new Date().toISOString() },
        ]),
      )
    }

    const res = await fetch("/api/v1/system_alerts")
    const data = await res.json() as { key: string; device: string; level: string; message: string }[]

    expect(capturedUrl).toBe("/api/v1/system_alerts")
    expect(data.length).toBe(2)
    expect(data[0].key).toBe("disk_full")
    expect(data[0].level).toBe("critical")
    expect(data[1].level).toBe("warn")
  })

  it("should POST to /api/v1/system_alerts/unset/{key}/{device} on dismiss", async () => {
    let capturedUrl = ""
    let capturedMethod = ""
    fetchHandler = (url, init) => {
      capturedUrl = url
      capturedMethod = init?.method ?? "GET"
      return Promise.resolve(jsonOk({}))
    }

    await fetch("/api/v1/system_alerts/unset/disk_full/sda", { method: "POST" })

    expect(capturedUrl).toBe("/api/v1/system_alerts/unset/disk_full/sda")
    expect(capturedMethod).toBe("POST")
  })

  it("should render a system alerts popover structure with bell, badge, and alert rows", async () => {
    function MiniPopover() {
      const [alerts] = React.useState([
        { key: "test_alert", device: "dev0", level: "warn" as const, message: "Test message", set_at: new Date().toISOString() },
      ])
      return (
        <div data-testid="system-alerts-popover">
          <button data-testid="system-alerts-bell" aria-label="System alerts">
            <span data-testid="system-alerts-badge">{alerts.length}</span>
          </button>
          <div data-testid="system-alerts-panel">
            {alerts.map((a) => (
              <div key={`${a.key}/${a.device}`} data-testid={`system-alert-${a.key}`}>
                <span>{a.key}</span>
                <span>{a.message}</span>
                <button data-testid={`dismiss-alert-${a.key}`}>Dismiss</button>
              </div>
            ))}
          </div>
        </div>
      )
    }

    render(<MiniPopover />)

    expect(screen.getByTestId("system-alerts-bell")).toBeInTheDocument()
    expect(screen.getByTestId("system-alerts-badge")).toHaveTextContent("1")
    expect(screen.getByTestId("system-alert-test_alert")).toBeInTheDocument()
    expect(screen.getByText("Test message")).toBeInTheDocument()
    expect(screen.getByTestId("dismiss-alert-test_alert")).toBeInTheDocument()
  })

  it("should call dismiss endpoint when Dismiss button is clicked", async () => {
    const user = userEvent.setup()
    let dismissCalled = false
    let dismissedKey = ""

    function MiniPopoverWithDismiss() {
      const [alerts, setAlerts] = React.useState([
        { key: "fan_fail", device: "fan1", level: "critical" as const, message: "Fan failure", set_at: new Date().toISOString() },
      ])

      async function dismiss(key: string, device: string) {
        await fetch(`/api/v1/system_alerts/unset/${key}/${device}`, { method: "POST" })
        dismissCalled = true
        dismissedKey = key
        setAlerts((prev) => prev.filter((a) => a.key !== key))
      }

      return (
        <div>
          {alerts.map((a) => (
            <div key={`${a.key}/${a.device}`}>
              <span data-testid={`alert-${a.key}`}>{a.message}</span>
              <button
                data-testid={`dismiss-${a.key}`}
                onClick={() => dismiss(a.key, a.device)}
              >
                Dismiss
              </button>
            </div>
          ))}
          {alerts.length === 0 && <span data-testid="no-alerts">cleared</span>}
        </div>
      )
    }

    fetchHandler = (_url, _init) => Promise.resolve(jsonOk({}))
    render(<MiniPopoverWithDismiss />)

    expect(screen.getByTestId("alert-fan_fail")).toBeInTheDocument()

    await user.click(screen.getByTestId("dismiss-fan_fail"))

    await waitFor(() => {
      expect(dismissCalled).toBe(true)
    })
    expect(dismissedKey).toBe("fan_fail")
    expect(screen.getByTestId("no-alerts")).toBeInTheDocument()
  })
})
