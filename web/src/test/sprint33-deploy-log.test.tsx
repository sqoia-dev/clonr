/**
 * sprint33-deploy-log.test.tsx — STREAM-LOG-UI tests.
 *
 * Verifies:
 *   1. Empty state renders when no log entries are present.
 *   2. An incoming SSE event renders as a log row with timestamp, level, and message.
 *   3. Phase badge renders when the entry carries a `phase` field.
 *   4. Phase filter chips appear once entries with phases arrive; filtering works.
 *   5. Warn/error indicator badge appears when an error-level entry arrives.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, act } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { DeployLogTab } from "../routes/node-detail-tabs"
import type { LogEntry } from "../routes/node-detail-tabs"

// ─── Fake EventSource ─────────────────────────────────────────────────────────
//
// jsdom does not implement EventSource. We replace it with a controllable fake
// that exposes test helpers for firing open / message / error events.

interface FakeESInstance {
  url: string
  onopen: (() => void) | null
  onerror: (() => void) | null
  onmessage: ((ev: { data: string }) => void) | null
  closed: boolean
  close: () => void
  _fireOpen: () => void
  _fireMessage: (entry: LogEntry) => void
  _fireError: () => void
}

let lastFakeES: FakeESInstance | null = null

class FakeEventSource implements FakeESInstance {
  url: string
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  closed = false

  constructor(url: string) {
    this.url = url
    lastFakeES = this
  }

  close() { this.closed = true }

  _fireOpen() { this.onopen?.() }

  _fireMessage(entry: LogEntry) {
    this.onmessage?.({ data: JSON.stringify(entry) })
  }

  _fireError() { this.onerror?.() }
}

beforeEach(() => {
  lastFakeES = null
  vi.stubGlobal("EventSource", FakeEventSource)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// ─── Helpers ──────────────────────────────────────────────────────────────────

function renderTab(primaryMac = "aa:bb:cc:dd:ee:ff") {
  return render(
    <DeployLogTab nodeId="test-node-id" primaryMac={primaryMac} />,
  )
}

// ─── Test 1: empty state ──────────────────────────────────────────────────────

describe("DeployLogTab — empty state (STREAM-LOG-UI)", () => {
  it("should show the empty state when no log entries have arrived", () => {
    renderTab()
    expect(screen.getByText("No active install log")).toBeInTheDocument()
    expect(
      screen.getByText("Waiting for a deploy to start on this node"),
    ).toBeInTheDocument()
  })

  it("should show the connecting status before the stream opens", () => {
    renderTab()
    expect(screen.getByText("Connecting…")).toBeInTheDocument()
  })

  it("should connect to the correct SSE URL for the given MAC", () => {
    renderTab("12:34:56:78:9a:bc")
    expect(lastFakeES).not.toBeNull()
    expect(lastFakeES!.url).toContain("component=deploy")
    expect(lastFakeES!.url).toContain("mac=12%3A34%3A56%3A78%3A9a%3Abc")
    expect(lastFakeES!.url).not.toContain("node_mac=")
  })

  it("should show Live status after the stream opens", async () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })
    expect(screen.getByText("Live")).toBeInTheDocument()
  })
})

// ─── Test 2: incoming event renders as a row ──────────────────────────────────

describe("DeployLogTab — incoming events (STREAM-LOG-UI)", () => {
  it("should render an incoming log entry as a table row", async () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })

    const entry: LogEntry = {
      level: "info",
      message: "sgdisk --zap-all /dev/sda",
      ts: 1746700000000,
    }

    act(() => { lastFakeES!._fireMessage(entry) })

    expect(screen.getByText("sgdisk --zap-all /dev/sda")).toBeInTheDocument()
    // level column
    expect(screen.getByText("info")).toBeInTheDocument()
    // empty state should no longer be visible
    expect(screen.queryByText("No active install log")).not.toBeInTheDocument()
  })

  it("should render the timestamp in HH:MM:SS.mmm format", async () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })

    // ts = 1746700000000 → UTC milliseconds; the formatted value depends on
    // local timezone so we just assert a time-looking string is present.
    const entry: LogEntry = {
      level: "info",
      message: "test timestamp",
      ts: 1746700000000,
    }
    act(() => { lastFakeES!._fireMessage(entry) })

    // Should contain something like "12:34:56.789" — regex test
    const rows = screen.getAllByRole("row")
    const rowText = rows[0].textContent ?? ""
    expect(rowText).toMatch(/\d{2}:\d{2}:\d{2}\.\d{3}/)
  })

  it("should render multiple entries as separate rows", () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })

    const entries: LogEntry[] = [
      { level: "info",  message: "first line",  ts: 1000 },
      { level: "warn",  message: "second line", ts: 2000 },
      { level: "error", message: "third line",  ts: 3000 },
    ]
    act(() => {
      for (const e of entries) lastFakeES!._fireMessage(e)
    })

    expect(screen.getByText("first line")).toBeInTheDocument()
    expect(screen.getByText("second line")).toBeInTheDocument()
    expect(screen.getByText("third line")).toBeInTheDocument()
  })
})

// ─── Test 3: phase badge ──────────────────────────────────────────────────────

describe("DeployLogTab — phase badge (STREAM-LOG-UI)", () => {
  it("should render the phase badge when an entry has a phase field", () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })

    const entry: LogEntry = {
      level: "info",
      message: "sgdisk --zap-all /dev/sda",
      ts: 1000,
      phase: "partitioning",
    }
    act(() => { lastFakeES!._fireMessage(entry) })

    // Phase text appears both in the filter chip AND in the row badge.
    const matches = screen.getAllByText("partitioning")
    expect(matches.length).toBeGreaterThanOrEqual(1)
  })

  it("should render a dash placeholder when an entry has no phase", () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })

    const entry: LogEntry = {
      level: "info",
      message: "some message without phase",
      ts: 1000,
    }
    act(() => { lastFakeES!._fireMessage(entry) })

    // The phase cell shows "—" for phase-less entries.
    expect(screen.getByText("—")).toBeInTheDocument()
  })
})

// ─── Test 4: phase filter chips ───────────────────────────────────────────────

describe("DeployLogTab — phase filter chips (STREAM-LOG-UI)", () => {
  it("should display phase chips once phased entries arrive", () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })

    act(() => {
      lastFakeES!._fireMessage({ level: "info", message: "partitioning log", ts: 1000, phase: "partitioning" })
      lastFakeES!._fireMessage({ level: "info", message: "downloading log", ts: 2000, phase: "downloading" })
    })

    // "All" + the two phase chips should appear
    expect(screen.getAllByRole("button", { name: "All" }).length).toBeGreaterThan(0)
    expect(screen.getByRole("button", { name: "partitioning" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "downloading" })).toBeInTheDocument()
  })

  it("should filter rows when a phase chip is clicked", async () => {
    const user = userEvent.setup()
    renderTab()
    act(() => { lastFakeES!._fireOpen() })

    act(() => {
      lastFakeES!._fireMessage({ level: "info", message: "partitioning log", ts: 1000, phase: "partitioning" })
      lastFakeES!._fireMessage({ level: "info", message: "downloading log", ts: 2000, phase: "downloading" })
    })

    // Click the "partitioning" chip.
    await user.click(screen.getByRole("button", { name: "partitioning" }))

    // partitioning entry should be visible, downloading entry should be hidden.
    expect(screen.getByText("partitioning log")).toBeInTheDocument()
    expect(screen.queryByText("downloading log")).not.toBeInTheDocument()
  })

  it("should restore all rows when 'All' chip is clicked after filtering", async () => {
    const user = userEvent.setup()
    renderTab()
    act(() => { lastFakeES!._fireOpen() })

    act(() => {
      lastFakeES!._fireMessage({ level: "info", message: "partitioning log", ts: 1000, phase: "partitioning" })
      lastFakeES!._fireMessage({ level: "info", message: "downloading log", ts: 2000, phase: "downloading" })
    })

    await user.click(screen.getByRole("button", { name: "partitioning" }))
    // Now click All — both should reappear.
    // There may be multiple "All" buttons; grab the phase-filter one.
    const allButtons = screen.getAllByRole("button", { name: "All" })
    await user.click(allButtons[0])

    expect(screen.getByText("partitioning log")).toBeInTheDocument()
    expect(screen.getByText("downloading log")).toBeInTheDocument()
  })
})

// ─── Test 5: warn/error indicator ─────────────────────────────────────────────

describe("DeployLogTab — warn/error indicator (STREAM-LOG-UI)", () => {
  it("should not show the warnings badge when only info entries arrive", () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })
    act(() => {
      lastFakeES!._fireMessage({ level: "info", message: "all good", ts: 1000 })
    })
    expect(screen.queryByText("Warnings / errors")).not.toBeInTheDocument()
  })

  it("should show the warnings badge when a warn-level entry arrives", () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })
    act(() => {
      lastFakeES!._fireMessage({ level: "warn", message: "disk almost full", ts: 1000 })
    })
    expect(screen.getByText("Warnings / errors")).toBeInTheDocument()
  })

  it("should show the warnings badge when an error-level entry arrives", () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })
    act(() => {
      lastFakeES!._fireMessage({ level: "error", message: "mount failed", ts: 1000 })
    })
    expect(screen.getByText("Warnings / errors")).toBeInTheDocument()
  })
})

// ─── Test 6: deduplication by id ──────────────────────────────────────────────

describe("DeployLogTab — deduplication (STREAM-LOG-UI)", () => {
  it("should not render duplicate entries when the same id is emitted twice", () => {
    renderTab()
    act(() => { lastFakeES!._fireOpen() })

    const entry: LogEntry = { id: "log-001", level: "info", message: "unique message", ts: 1000 }
    act(() => {
      lastFakeES!._fireMessage(entry)
      lastFakeES!._fireMessage(entry) // duplicate
    })

    const matches = screen.getAllByText("unique message")
    expect(matches).toHaveLength(1)
  })
})
