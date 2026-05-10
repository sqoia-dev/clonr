// disk-layouts.tsx — Disk Layout Catalog (Sprint 35 UEFI-WEBAPP + DISK-LAYOUT-DUPLICATE)
//
// Features:
//   - List all layouts with firmware_kind badge per row
//   - Filter dropdown: All / BIOS / UEFI / Any
//   - "Duplicate this layout" action → POST /api/v1/disk-layouts with name suffix (copy)

import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import { HardDrive, Copy, Trash2, ChevronDown, Plus, Search } from "lucide-react"
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from "@/components/ui/table"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { FirmwareBadge } from "@/components/DiskLayoutPicker"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type { StoredDiskLayout, ListDiskLayoutsResponse, FirmwareKind } from "@/lib/types"
import { cn } from "@/lib/utils"

// ── Filter values ─────────────────────────────────────────────────────────────

type FirmwareFilter = "all" | FirmwareKind

const FIRMWARE_FILTER_OPTIONS: { value: FirmwareFilter; label: string }[] = [
  { value: "all", label: "All firmware" },
  { value: "bios", label: "BIOS only" },
  { value: "uefi", label: "UEFI only" },
  { value: "any", label: "Any (agnostic)" },
]

// ── Helpers ───────────────────────────────────────────────────────────────────

function relativeTime(iso: string | undefined): string {
  if (!iso) return "—"
  try {
    return formatDistanceToNow(new Date(iso), { addSuffix: true })
  } catch {
    return iso
  }
}

function partitionSummary(layout: StoredDiskLayout["layout"]): string {
  const parts = layout?.partitions ?? []
  if (parts.length === 0) return "—"
  const mounts = parts.map((p) => p.mountpoint).filter(Boolean)
  if (mounts.length > 0) return mounts.join(", ")
  return `${parts.length} partition${parts.length !== 1 ? "s" : ""}`
}

// ── Duplicate confirmation state ───────────────────────────────────────────────

interface DuplicateState {
  sourceId: string
  sourceName: string
}

// ── buildCopyName ─────────────────────────────────────────────────────────────

/**
 * Generate a non-colliding copy name given the set of existing layout names.
 *
 * Algorithm:
 *  1. Strip any trailing " (copy N)" or " (copy)" suffix from the source name
 *     so duplicating a copy doesn't produce "Foo (copy) (copy)".
 *  2. Try "<base> (copy)".  If that already exists in the catalog, increment:
 *     "<base> (copy 2)", "<base> (copy 3)", …
 *
 * Exported for unit testing.
 */
export function buildCopyName(sourceName: string, existingNames: Set<string>): string {
  const base = sourceName.replace(/ \(copy(?: \d+)?\)$/, "")
  const candidate = `${base} (copy)`
  if (!existingNames.has(candidate)) return candidate
  for (let n = 2; ; n++) {
    const numbered = `${base} (copy ${n})`
    if (!existingNames.has(numbered)) return numbered
  }
}

// ── Main page ─────────────────────────────────────────────────────────────────

export function DiskLayoutsPage() {
  const qc = useQueryClient()
  const [filter, setFilter] = React.useState<FirmwareFilter>("all")
  const [filterOpen, setFilterOpen] = React.useState(false)
  const [q, setQ] = React.useState("")
  const [duplicating, setDuplicating] = React.useState<DuplicateState | null>(null)
  const [deletingId, setDeletingId] = React.useState<string | null>(null)

  const { data, isLoading, isError } = useQuery<ListDiskLayoutsResponse>({
    queryKey: ["disk-layouts"],
    queryFn: () => apiFetch<ListDiskLayoutsResponse>("/api/v1/disk-layouts"),
    staleTime: 15_000,
  })

  const layouts = React.useMemo(() => {
    let list = data?.layouts ?? []
    if (filter !== "all") {
      list = list.filter((l) => l.firmware_kind === filter)
    }
    if (q.trim()) {
      const lower = q.toLowerCase()
      list = list.filter(
        (l) =>
          l.name.toLowerCase().includes(lower) ||
          l.id.toLowerCase().includes(lower)
      )
    }
    return list
  }, [data, filter, q])

  const duplicateMutation = useMutation({
    mutationFn: async ({ sourceId, sourceName }: DuplicateState) => {
      // Fetch the source layout's full body first.
      const resp = await apiFetch<{ disk_layout: StoredDiskLayout }>(
        `/api/v1/disk-layouts/${sourceId}`
      )
      const src = resp.disk_layout
      // Build a non-colliding name using the catalog snapshot already loaded.
      const existingNames = new Set((data?.layouts ?? []).map((l) => l.name))
      const copyName = buildCopyName(sourceName, existingNames)
      return apiFetch<{ disk_layout: StoredDiskLayout }>("/api/v1/disk-layouts", {
        method: "POST",
        body: JSON.stringify({
          name: copyName,
          firmware_kind: src.firmware_kind,
          layout_json: JSON.stringify(src.layout),
        }),
      })
    },
    onSuccess: (resp) => {
      qc.invalidateQueries({ queryKey: ["disk-layouts"] })
      toast({ title: "Layout duplicated", description: resp.disk_layout.name })
      setDuplicating(null)
    },
    onError: (err) => {
      toast({ variant: "destructive", title: "Duplicate failed", description: String(err) })
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/api/v1/disk-layouts/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["disk-layouts"] })
      toast({ title: "Layout deleted" })
      setDeletingId(null)
    },
    onError: (err) => {
      toast({ variant: "destructive", title: "Delete failed", description: String(err) })
      setDeletingId(null)
    },
  })

  const activeFilterLabel =
    FIRMWARE_FILTER_OPTIONS.find((o) => o.value === filter)?.label ?? "All firmware"

  return (
    <div className="p-6 space-y-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <HardDrive className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-lg font-semibold">Disk Layouts</h1>
          {data && (
            <span className="text-sm text-muted-foreground ml-1">
              ({data.total} total)
            </span>
          )}
        </div>
      </div>

      {/* Toolbar */}
      <div className="flex items-center gap-2">
        <div className="relative flex-1 max-w-xs">
          <Search className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
          <Input
            placeholder="Search layouts…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            className="pl-8 h-8 text-xs"
          />
        </div>

        {/* Firmware filter dropdown */}
        <div className="relative">
          <Button
            variant="outline"
            size="sm"
            className="h-8 text-xs gap-1"
            onClick={() => setFilterOpen((v) => !v)}
          >
            {activeFilterLabel}
            <ChevronDown className="h-3 w-3" />
          </Button>
          {filterOpen && (
            <div
              className="absolute right-0 top-full mt-1 z-50 bg-popover border border-border rounded-md shadow-md min-w-[160px] py-1"
              onMouseLeave={() => setFilterOpen(false)}
            >
              {FIRMWARE_FILTER_OPTIONS.map((opt) => (
                <button
                  key={opt.value}
                  className={cn(
                    "w-full text-left px-3 py-1.5 text-xs hover:bg-muted/60 transition-colors",
                    filter === opt.value && "font-medium text-primary"
                  )}
                  onClick={() => {
                    setFilter(opt.value)
                    setFilterOpen(false)
                  }}
                >
                  {opt.label}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Table */}
      {isLoading && (
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full rounded" />
          ))}
        </div>
      )}

      {isError && (
        <p className="text-sm text-destructive">Failed to load disk layouts.</p>
      )}

      {!isLoading && !isError && (
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="text-xs w-[280px]">Name</TableHead>
                <TableHead className="text-xs w-[100px]">Firmware</TableHead>
                <TableHead className="text-xs">Partitions</TableHead>
                <TableHead className="text-xs w-[120px]">Created</TableHead>
                <TableHead className="text-xs w-[100px] text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {layouts.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-xs text-muted-foreground py-8">
                    {filter !== "all" || q
                      ? "No layouts match your filter."
                      : "No disk layouts defined yet."}
                  </TableCell>
                </TableRow>
              )}
              {layouts.map((layout) => (
                <LayoutRow
                  key={layout.id}
                  layout={layout}
                  onDuplicate={() =>
                    setDuplicating({ sourceId: layout.id, sourceName: layout.name })
                  }
                  onDelete={() => setDeletingId(layout.id)}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {/* Duplicate confirmation */}
      {duplicating && (
        <DuplicateConfirmDialog
          sourceName={duplicating.sourceName}
          isPending={duplicateMutation.isPending}
          onConfirm={() => duplicateMutation.mutate(duplicating)}
          onCancel={() => setDuplicating(null)}
        />
      )}

      {/* Delete confirmation */}
      {deletingId && (
        <DeleteConfirmDialog
          layoutName={
            data?.layouts.find((l) => l.id === deletingId)?.name ?? deletingId
          }
          isPending={deleteMutation.isPending}
          onConfirm={() => deleteMutation.mutate(deletingId)}
          onCancel={() => setDeletingId(null)}
        />
      )}
    </div>
  )
}

// ── LayoutRow ─────────────────────────────────────────────────────────────────

function LayoutRow({
  layout,
  onDuplicate,
  onDelete,
}: {
  layout: StoredDiskLayout
  onDuplicate: () => void
  onDelete: () => void
}) {
  return (
    <TableRow>
      <TableCell className="text-sm font-medium">{layout.name}</TableCell>
      <TableCell>
        <FirmwareBadge kind={layout.firmware_kind} />
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {partitionSummary(layout.layout)}
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {relativeTime(layout.created_at)}
      </TableCell>
      <TableCell className="text-right">
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            className="h-7 w-7 p-0"
            title="Duplicate layout"
            onClick={onDuplicate}
          >
            <Copy className="h-3.5 w-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 w-7 p-0 text-destructive hover:text-destructive"
            title="Delete layout"
            onClick={onDelete}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  )
}

// ── DuplicateConfirmDialog ────────────────────────────────────────────────────

function DuplicateConfirmDialog({
  sourceName,
  isPending,
  onConfirm,
  onCancel,
}: {
  sourceName: string
  isPending: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="bg-background rounded-lg border shadow-lg p-5 w-full max-w-sm space-y-3">
        <div className="flex items-center gap-2">
          <Plus className="h-4 w-4 text-primary" />
          <h3 className="font-semibold text-sm">Duplicate layout?</h3>
        </div>
        <p className="text-sm text-muted-foreground">
          This will create a copy named{" "}
          <span className="font-medium text-foreground">{sourceName} (copy)</span>.
          You can rename or edit it after creation.
        </p>
        <div className="flex justify-end gap-2 pt-1">
          <Button variant="ghost" size="sm" onClick={onCancel} disabled={isPending}>
            Cancel
          </Button>
          <Button size="sm" onClick={onConfirm} disabled={isPending}>
            {isPending ? "Duplicating…" : "Duplicate"}
          </Button>
        </div>
      </div>
    </div>
  )
}

// ── DeleteConfirmDialog ───────────────────────────────────────────────────────

function DeleteConfirmDialog({
  layoutName,
  isPending,
  onConfirm,
  onCancel,
}: {
  layoutName: string
  isPending: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="bg-background rounded-lg border shadow-lg p-5 w-full max-w-sm space-y-3">
        <div className="flex items-center gap-2">
          <Trash2 className="h-4 w-4 text-destructive" />
          <h3 className="font-semibold text-sm">Delete layout?</h3>
        </div>
        <p className="text-sm text-muted-foreground">
          Delete{" "}
          <span className="font-medium text-foreground">{layoutName}</span>?
          This cannot be undone. The delete will be rejected if any node or group still references this layout.
        </p>
        <div className="flex justify-end gap-2 pt-1">
          <Button variant="ghost" size="sm" onClick={onCancel} disabled={isPending}>
            Cancel
          </Button>
          <Button variant="destructive" size="sm" onClick={onConfirm} disabled={isPending}>
            {isPending ? "Deleting…" : "Delete"}
          </Button>
        </div>
      </div>
    </div>
  )
}
