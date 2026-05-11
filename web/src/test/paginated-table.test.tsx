/**
 * paginated-table.test.tsx — unit tests for the PaginatedTable primitive.
 *
 * Covers:
 *   PAGE-1  page navigation (prev/next enable/disable)
 *   PAGE-2  page-size change resets to page 1
 *   PAGE-3  correct rows rendered for current page
 *   PAGE-4  "Page X of Y" label
 *   PAGE-5  parsePage / parsePageSize URL-sync helpers
 *   PAGE-6  footer hidden when total == 0
 */

import * as React from "react"
import { describe, it, expect } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { PaginatedTable, parsePage, parsePageSize, DEFAULT_PAGE_SIZE } from "../components/PaginatedTable"
import {
  TableRow,
  TableHead,
  TableCell,
} from "../components/ui/table"

// ─── helpers ──────────────────────────────────────────────────────────────────

type Row = { id: string; label: string }

function makeRows(n: number): Row[] {
  return Array.from({ length: n }, (_, i) => ({ id: `r${i}`, label: `Row ${i + 1}` }))
}

function renderTable(rows: Row[], page: number, pageSize: number, onPageChange = (_p: number) => {}, onPageSizeChange = (_ps: number) => {}) {
  return render(
    <PaginatedTable
      rows={rows}
      page={page}
      pageSize={pageSize}
      onPageChange={onPageChange}
      onPageSizeChange={onPageSizeChange}
      headers={
        <TableRow>
          <TableHead>Label</TableHead>
        </TableRow>
      }
      renderRow={(row) => (
        <TableRow key={row.id} data-testid={`row-${row.id}`}>
          <TableCell>{row.label}</TableCell>
        </TableRow>
      )}
      data-testid="paginated-table"
    />,
  )
}

// ─── PAGE-1: prev/next navigation ─────────────────────────────────────────────

describe("PaginatedTable — PAGE-1: prev/next navigation", () => {
  it("should disable Prev button on page 1", () => {
    renderTable(makeRows(30), 1, 10)
    expect(screen.getByTestId("prev-page")).toBeDisabled()
    expect(screen.getByTestId("next-page")).not.toBeDisabled()
  })

  it("should disable Next button on last page", () => {
    renderTable(makeRows(30), 3, 10)
    expect(screen.getByTestId("prev-page")).not.toBeDisabled()
    expect(screen.getByTestId("next-page")).toBeDisabled()
  })

  it("should call onPageChange with page+1 when clicking Next", () => {
    let called = 0
    let calledWith = 0
    renderTable(makeRows(30), 1, 10, (p) => { called++; calledWith = p })
    fireEvent.click(screen.getByTestId("next-page"))
    expect(called).toBe(1)
    expect(calledWith).toBe(2)
  })

  it("should call onPageChange with page-1 when clicking Prev", () => {
    let calledWith = 0
    renderTable(makeRows(30), 3, 10, (p) => { calledWith = p })
    fireEvent.click(screen.getByTestId("prev-page"))
    expect(calledWith).toBe(2)
  })
})

// ─── PAGE-2: page-size change ─────────────────────────────────────────────────

describe("PaginatedTable — PAGE-2: page-size change", () => {
  it("should call onPageSizeChange and onPageChange(1) when selecting a different size", () => {
    const sizes: number[] = []
    const pages: number[] = []
    renderTable(makeRows(100), 3, 25, (p) => pages.push(p), (ps) => sizes.push(ps))
    fireEvent.click(screen.getByTestId("page-size-50"))
    expect(sizes).toContain(50)
    expect(pages).toContain(1)
  })

  it("should highlight the active page-size button", () => {
    renderTable(makeRows(100), 1, 25)
    const btn25 = screen.getByTestId("page-size-25")
    expect(btn25.getAttribute("aria-pressed")).toBe("true")
    const btn10 = screen.getByTestId("page-size-10")
    expect(btn10.getAttribute("aria-pressed")).toBe("false")
  })
})

// ─── PAGE-3: correct rows rendered ───────────────────────────────────────────

describe("PaginatedTable — PAGE-3: correct rows rendered for current page", () => {
  it("should render rows 11-20 on page 2 with pageSize 10", () => {
    const rows = makeRows(30)
    renderTable(rows, 2, 10)
    expect(screen.getByTestId("row-r10")).toBeInTheDocument()  // index 10 = Row 11
    expect(screen.getByTestId("row-r19")).toBeInTheDocument()  // index 19 = Row 20
    expect(screen.queryByTestId("row-r0")).not.toBeInTheDocument()  // page 1 row not shown
    expect(screen.queryByTestId("row-r20")).not.toBeInTheDocument() // page 3 row not shown
  })

  it("should render the last partial page correctly", () => {
    // 3 rows on page 3 when pageSize=10 and total=23
    const rows = makeRows(23)
    renderTable(rows, 3, 10)
    expect(screen.getByTestId("row-r20")).toBeInTheDocument()
    expect(screen.getByTestId("row-r21")).toBeInTheDocument()
    expect(screen.getByTestId("row-r22")).toBeInTheDocument()
    expect(screen.queryByTestId("row-r23")).not.toBeInTheDocument()
  })

  it("should render all identical input rows as distinct rows (no deduplication)", () => {
    // This test also validates the dep-matrix scenario: if the API returns
    // 6 identical-looking rows, the PaginatedTable renders all 6 and does NOT
    // collapse them — deduplication is the backend's responsibility.
    const rows: Row[] = [
      { id: "r0", label: "hwloc" },
      { id: "r1", label: "hwloc" },
      { id: "r2", label: "hwloc" },
    ]
    renderTable(rows, 1, 25)
    expect(screen.getAllByText("hwloc")).toHaveLength(3)
  })
})

// ─── PAGE-4: page label ───────────────────────────────────────────────────────

describe("PaginatedTable — PAGE-4: page label", () => {
  it("should show 'Page 1 of 3' for 30 rows with pageSize 10", () => {
    renderTable(makeRows(30), 1, 10)
    expect(screen.getByTestId("page-label")).toHaveTextContent("Page 1 of 3")
  })

  it("should show correct total count in the label", () => {
    renderTable(makeRows(47), 1, 25)
    expect(screen.getByTestId("page-label")).toHaveTextContent("47 total")
  })
})

// ─── PAGE-5: URL-sync helpers ─────────────────────────────────────────────────

describe("PaginatedTable — PAGE-5: parsePage / parsePageSize helpers", () => {
  it("parsePage should default to 1 when param missing", () => {
    expect(parsePage({})).toBe(1)
  })

  it("parsePage should parse numeric value", () => {
    expect(parsePage({ page: 3 })).toBe(3)
  })

  it("parsePage should parse string value", () => {
    expect(parsePage({ page: "5" })).toBe(5)
  })

  it("parsePage should return 1 for invalid value", () => {
    expect(parsePage({ page: "abc" })).toBe(1)
    expect(parsePage({ page: -1 })).toBe(1)
  })

  it("parsePageSize should default to DEFAULT_PAGE_SIZE when missing", () => {
    expect(parsePageSize({})).toBe(DEFAULT_PAGE_SIZE)
  })

  it("parsePageSize should parse valid page-size option", () => {
    expect(parsePageSize({ per_page: 50 })).toBe(50)
  })

  it("parsePageSize should return DEFAULT_PAGE_SIZE for non-positive values", () => {
    expect(parsePageSize({ per_page: 0 })).toBe(DEFAULT_PAGE_SIZE)
    expect(parsePageSize({ per_page: -5 })).toBe(DEFAULT_PAGE_SIZE)
    expect(parsePageSize({ per_page: "abc" })).toBe(DEFAULT_PAGE_SIZE)
  })
})

// ─── PAGE-6: empty state ─────────────────────────────────────────────────────

describe("PaginatedTable — PAGE-6: footer hidden when total == 0", () => {
  it("should not render pagination footer when rows array is empty", () => {
    renderTable([], 1, 25)
    expect(screen.queryByTestId("prev-page")).not.toBeInTheDocument()
    expect(screen.queryByTestId("next-page")).not.toBeInTheDocument()
  })
})
