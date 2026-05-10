// disk-layouts.tsx — Disk Layout Catalog (Sprint 35 UEFI-WEBAPP + DISK-LAYOUT-DUPLICATE + edit flow)
//
// Features:
//   - List all layouts with firmware_kind badge per row
//   - Filter dropdown: All / BIOS / UEFI / Any
//   - "Duplicate this layout" action → POST /api/v1/disk-layouts with name suffix (copy)
//   - "Edit layout" action → PUT /api/v1/disk-layouts/{id} (name + layout_json)
//     Built-in seed rows (clustr-default-uefi, clustr-default-bios) show a disabled
//     edit button with a "Built-in layout, cannot be edited" tooltip.

import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import { HardDrive, Copy, Trash2, ChevronDown, Plus, Search, Pencil } from "lucide-react"
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
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { FirmwareBadge } from "@/components/DiskLayoutPicker"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type { StoredDiskLayout, ListDiskLayoutsResponse, FirmwareKind } from "@/lib/types"
import { cn } from "@/lib/utils"

// Seed layout names that cannot be edited (seeded by migration 110).
const SEED_LAYOUT_NAMES = new Set(["clustr-default-uefi", "clustr-default-bios"])

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

// ── Edit dialog state ─────────────────────────────────────────────────────────

interface EditState {
  layout: StoredDiskLayout
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
  const [editingLayout, setEditingLayout] = React.useState<EditState | null>(null)

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

  const editMutation = useMutation({
    mutationFn: ({ id, name, layoutJson }: { id: string; name: string; layoutJson: string }) =>
      apiFetch<{ disk_layout: StoredDiskLayout }>(`/api/v1/disk-layouts/${id}`, {
        method: "PUT",
        body: JSON.stringify({ name, layout_json: layoutJson }),
      }),
    onSuccess: (resp) => {
      qc.invalidateQueries({ queryKey: ["disk-layouts"] })
      toast({ title: "Layout saved", description: resp.disk_layout.name })
      setEditingLayout(null)
    },
    onError: (err) => {
      // Keep dialog open on error so operator can correct — toast shows the reason.
      toast({ variant: "destructive", title: "Save failed", description: String(err) })
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
                  onEdit={() => setEditingLayout({ layout })}
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

      {/* Edit dialog */}
      {editingLayout && (
        <EditLayoutDialog
          layout={editingLayout.layout}
          isPending={editMutation.isPending}
          onSave={(name, layoutJson) =>
            editMutation.mutate({ id: editingLayout.layout.id, name, layoutJson })
          }
          onCancel={() => setEditingLayout(null)}
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
  onEdit,
}: {
  layout: StoredDiskLayout
  onDuplicate: () => void
  onDelete: () => void
  onEdit: () => void
}) {
  const isSeed = SEED_LAYOUT_NAMES.has(layout.name)

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
        <TooltipProvider>
          <div className="flex items-center justify-end gap-1">
            {isSeed ? (
              <Tooltip>
                <TooltipTrigger asChild>
                  <span className="inline-flex">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 w-7 p-0 opacity-40 cursor-not-allowed"
                      disabled
                      aria-label="Edit layout (disabled)"
                      data-testid={`edit-layout-disabled-${layout.id}`}
                    >
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>
                  </span>
                </TooltipTrigger>
                <TooltipContent side="top" className="text-xs">
                  Built-in layout, cannot be edited
                </TooltipContent>
              </Tooltip>
            ) : (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 w-7 p-0"
                title="Edit layout"
                onClick={onEdit}
                data-testid={`edit-layout-${layout.id}`}
              >
                <Pencil className="h-3.5 w-3.5" />
              </Button>
            )}
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
        </TooltipProvider>
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

// ── EditLayoutDialog ──────────────────────────────────────────────────────────
// Lets the operator rename a layout and edit its JSON body.
// firmware_kind is displayed read-only — the PUT endpoint does not accept it.

function EditLayoutDialog({
  layout,
  isPending,
  onSave,
  onCancel,
}: {
  layout: StoredDiskLayout
  isPending: boolean
  onSave: (name: string, layoutJson: string) => void
  onCancel: () => void
}) {
  const [name, setName] = React.useState(layout.name)
  const [layoutJson, setLayoutJson] = React.useState(
    JSON.stringify(layout.layout, null, 2)
  )
  const [jsonError, setJsonError] = React.useState("")

  function handleSave() {
    // Validate JSON before submitting.
    try {
      JSON.parse(layoutJson)
      setJsonError("")
    } catch (e) {
      setJsonError(`Invalid JSON: ${(e as Error).message}`)
      return
    }
    if (!name.trim()) {
      setJsonError("Name is required")
      return
    }
    onSave(name.trim(), layoutJson)
  }

  const firmwareLabel = layout.firmware_kind === "bios"
    ? "BIOS"
    : layout.firmware_kind === "uefi"
      ? "UEFI"
      : "Any"

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="bg-background rounded-lg border shadow-lg p-5 w-full max-w-lg space-y-4">
        <div className="flex items-center gap-2">
          <Pencil className="h-4 w-4 text-primary" />
          <h3 className="font-semibold text-sm">Edit disk layout</h3>
        </div>

        <div className="space-y-1">
          <label className="text-xs text-muted-foreground">Name</label>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="text-sm"
            placeholder="my-layout"
            data-testid="edit-layout-name"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs text-muted-foreground">Firmware (read-only — contact support to change)</label>
          <div className="flex items-center gap-2 px-3 py-1.5 rounded-md border border-border bg-secondary/30 text-sm text-muted-foreground">
            {firmwareLabel}
          </div>
        </div>

        <div className="space-y-1">
          <label className="text-xs text-muted-foreground">Layout JSON</label>
          <textarea
            className="w-full font-mono text-xs border border-border bg-background rounded-md px-3 py-2 resize-none focus:outline-none focus:ring-1 focus:ring-ring"
            rows={10}
            value={layoutJson}
            onChange={(e) => { setLayoutJson(e.target.value); setJsonError("") }}
            data-testid="edit-layout-json"
          />
          {jsonError && (
            <p className="text-xs text-destructive" data-testid="edit-layout-json-error">{jsonError}</p>
          )}
        </div>

        <div className="flex justify-end gap-2 pt-1">
          <Button variant="ghost" size="sm" onClick={onCancel} disabled={isPending}>
            Cancel
          </Button>
          <Button size="sm" onClick={handleSave} disabled={isPending} data-testid="edit-layout-save">
            {isPending ? "Saving…" : "Save changes"}
          </Button>
        </div>
      </div>
    </div>
  )
}
