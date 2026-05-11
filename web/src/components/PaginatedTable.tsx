/**
 * PaginatedTable.tsx — Generic paginated table primitive.
 *
 * Usage (client-side data, URL-synced):
 *
 *   <PaginatedTable
 *     rows={allRows}
 *     columns={columns}
 *     page={page}           // 1-based, from URL search param
 *     pageSize={pageSize}   // from URL search param
 *     total={allRows.length}
 *     onPageChange={(p) => navigate({ search: { ...s, page: p } })}
 *     onPageSizeChange={(ps) => navigate({ search: { ...s, page: 1, per_page: ps } })}
 *     renderRow={(row) => <TableRow key={row.id}>…</TableRow>}
 *     headers={<TableRow><TableHead>…</TableHead></TableRow>}
 *   />
 *
 * Design notes:
 * - Columns/rows are typed with generics so callers keep type-safety.
 * - The component only owns the pagination UI; row rendering is left to
 *   the caller via renderRow so it can use arbitrary table cells, links, etc.
 * - Page size selector: 10 / 25 / 50 / 100.  Default: 25.
 * - "Page X of Y" label.  When total == 0 the footer is hidden.
 */

import * as React from "react"
import { Button } from "@/components/ui/button"
import {
  Table,
  TableHeader,
  TableBody,
} from "@/components/ui/table"
import { cn } from "@/lib/utils"

export const PAGE_SIZE_OPTIONS = [10, 25, 50, 100] as const
export type PageSizeOption = typeof PAGE_SIZE_OPTIONS[number]
export const DEFAULT_PAGE_SIZE: PageSizeOption = 25

// ─── Props ────────────────────────────────────────────────────────────────────

export interface PaginatedTableProps<T> {
  /** Full (unsliced) rows — the component slices them for you. */
  rows: T[]
  /** 1-based current page number. */
  page: number
  /** Number of rows per page. */
  pageSize: number
  /**
   * Total number of rows (may differ from rows.length when the parent
   * provides server-paginated data — pass the server's `total` field).
   * When omitted falls back to rows.length.
   */
  total?: number
  /** Called when the user changes the page. */
  onPageChange: (page: number) => void
  /** Called when the user changes the page size. */
  onPageSizeChange: (pageSize: number) => void
  /** Render the <TableRow> for a single row. */
  renderRow: (row: T, index: number) => React.ReactNode
  /** The <TableRow> inside <TableHeader>. */
  headers: React.ReactNode
  /** Optional class for the wrapping div. */
  className?: string
  /** Optional test id for the table element. */
  "data-testid"?: string
}

// ─── Component ────────────────────────────────────────────────────────────────

export function PaginatedTable<T>({
  rows,
  page,
  pageSize,
  total: totalProp,
  onPageChange,
  onPageSizeChange,
  renderRow,
  headers,
  className,
  "data-testid": testId,
}: PaginatedTableProps<T>) {
  const total = totalProp ?? rows.length

  // Clamp page to valid range.
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  const safePage = Math.min(Math.max(1, page), totalPages)

  // Slice the rows for the current page (no-op for server-paginated data where
  // the caller already provides the correct slice and total).
  const start = (safePage - 1) * pageSize
  const end = Math.min(start + pageSize, rows.length)
  const pageRows = rows.slice(start, end)

  return (
    <div className={cn("space-y-2", className)}>
      <div className="overflow-auto rounded border border-border">
        <Table data-testid={testId}>
          <TableHeader>{headers}</TableHeader>
          <TableBody>
            {pageRows.map((row, i) => renderRow(row, i))}
          </TableBody>
        </Table>
      </div>

      {total > 0 && (
        <div className="flex items-center justify-between gap-4 px-1 text-xs text-muted-foreground">
          {/* Page size selector */}
          <div className="flex items-center gap-1.5">
            <span>Rows per page:</span>
            <div className="flex items-center gap-0.5">
              {PAGE_SIZE_OPTIONS.map((opt) => (
                <button
                  key={opt}
                  onClick={() => { onPageSizeChange(opt); onPageChange(1) }}
                  className={cn(
                    "px-1.5 py-0.5 rounded text-xs transition-colors",
                    pageSize === opt
                      ? "bg-primary/10 text-primary font-medium"
                      : "hover:bg-secondary/60",
                  )}
                  data-testid={`page-size-${opt}`}
                  aria-label={`Show ${opt} rows per page`}
                  aria-pressed={pageSize === opt}
                >
                  {opt}
                </button>
              ))}
            </div>
          </div>

          {/* Page navigation */}
          <div className="flex items-center gap-2">
            <span data-testid="page-label">
              Page {safePage} of {totalPages}
              {" "}({total} total)
            </span>
            <Button
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={() => onPageChange(safePage - 1)}
              disabled={safePage <= 1}
              data-testid="prev-page"
              aria-label="Previous page"
            >
              Prev
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={() => onPageChange(safePage + 1)}
              disabled={safePage >= totalPages}
              data-testid="next-page"
              aria-label="Next page"
            >
              Next
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

// ─── URL-sync helpers ─────────────────────────────────────────────────────────

/** Parse ?page= from a search object, defaulting to 1. */
export function parsePage(search: Record<string, unknown>): number {
  const v = search["page"]
  if (typeof v === "number" && v > 0) return v
  if (typeof v === "string") {
    const n = parseInt(v, 10)
    if (!isNaN(n) && n > 0) return n
  }
  return 1
}

/** Parse ?per_page= from a search object, defaulting to DEFAULT_PAGE_SIZE.
 *  Accepts any positive integer; does not restrict to PAGE_SIZE_OPTIONS. */
export function parsePageSize(search: Record<string, unknown>): number {
  const v = search["per_page"]
  if (typeof v === "number" && v > 0) return v
  if (typeof v === "string") {
    const n = parseInt(v, 10)
    if (!isNaN(n) && n > 0) return n
  }
  return DEFAULT_PAGE_SIZE
}
