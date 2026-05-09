// DiskLayoutPicker — modal dialog for selecting a disk layout override on a node.
//
// Opened from the Disk Layout section of the node Overview tab.
// Filters catalog to UEFI-compatible layouts when node.detected_firmware === "uefi".

import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { HardDrive, X } from "lucide-react"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api"
import { cn } from "@/lib/utils"
import type { StoredDiskLayout, ListDiskLayoutsResponse, FirmwareKind } from "@/lib/types"

// ── Firmware badge ─────────────────────────────────────────────────────────────

export function FirmwareBadge({ kind }: { kind: FirmwareKind | string }) {
  const colorClass =
    kind === "uefi"
      ? "bg-blue-100 text-blue-800 border border-blue-300"
      : kind === "bios"
        ? "bg-amber-100 text-amber-800 border border-amber-300"
        : "bg-gray-100 text-gray-600 border border-gray-300"

  return (
    <span className={`inline-flex items-center px-1.5 py-0.5 rounded text-xs font-medium ${colorClass}`}>
      {kind === "uefi" ? "UEFI" : kind === "bios" ? "BIOS" : "Any"}
    </span>
  )
}

// ── Props ─────────────────────────────────────────────────────────────────────

export type DiskLayoutPickerProps = {
  nodeId: string
  nodeFirmware?: string   // "uefi" | "bios" | "" | undefined
  open: boolean
  onClose: () => void
}

// ── Component ────────────────────────────────────────────────────────────────

export function DiskLayoutPicker({ nodeId, nodeFirmware, open, onClose }: DiskLayoutPickerProps) {
  const qc = useQueryClient()
  const [selected, setSelected] = React.useState<string | null>(null)

  const { data, isLoading } = useQuery<ListDiskLayoutsResponse>({
    queryKey: ["disk-layouts"],
    queryFn: () => apiFetch<ListDiskLayoutsResponse>("/api/v1/disk-layouts"),
    enabled: open,
  })

  // When node reports UEFI firmware, filter to layouts tagged uefi or any.
  // For BIOS nodes, show all (bios or any). For unknown, show all.
  const layouts = React.useMemo(() => {
    const all = data?.layouts ?? []
    if (nodeFirmware === "uefi") {
      return all.filter((l) => l.firmware_kind === "uefi" || l.firmware_kind === "any")
    }
    return all
  }, [data, nodeFirmware])

  const setOverride = useMutation({
    mutationFn: async (layoutId: string) => {
      // Fetch the full layout body, then push it as the node-level override.
      const resp = await apiFetch<{ disk_layout: StoredDiskLayout }>(
        `/api/v1/disk-layouts/${layoutId}`
      )
      return apiFetch(`/api/v1/nodes/${nodeId}/layout-override`, {
        method: "PUT",
        body: JSON.stringify({ layout: resp.disk_layout.layout }),
      })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["effective-layout", nodeId] })
      qc.invalidateQueries({ queryKey: ["nodes"] })
      onClose()
    },
  })

  function handleConfirm() {
    if (!selected) return
    setOverride.mutate(selected)
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) onClose() }}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <HardDrive className="h-4 w-4" />
            Select Disk Layout Override
          </DialogTitle>
          <DialogDescription>
            {nodeFirmware === "uefi"
              ? "Showing UEFI-compatible layouts only (node reported UEFI firmware)."
              : "Pick a layout to override the effective layout for this node's next deploy."}
          </DialogDescription>
        </DialogHeader>

        <div className="mt-2 space-y-2 max-h-80 overflow-y-auto pr-1">
          {isLoading && (
            <p className="text-sm text-muted-foreground">Loading layouts…</p>
          )}
          {!isLoading && layouts.length === 0 && (
            <p className="text-sm text-muted-foreground">No compatible layouts found.</p>
          )}
          {layouts.map((layout) => (
            <button
              key={layout.id}
              onClick={() => setSelected(layout.id)}
              className={cn(
                "w-full text-left rounded-md border px-3 py-2 text-sm transition-colors",
                selected === layout.id
                  ? "border-primary bg-primary/10 font-medium"
                  : "border-border hover:bg-muted/50"
              )}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="truncate">{layout.name}</span>
                <FirmwareBadge kind={layout.firmware_kind} />
              </div>
              {layout.layout?.partitions && layout.layout.partitions.length > 0 && (
                <p className="text-xs text-muted-foreground mt-0.5">
                  {layout.layout.partitions.length} partition{layout.layout.partitions.length !== 1 ? "s" : ""}
                  {layout.layout.partitions.map((p) => p.mountpoint).filter(Boolean).join(", ") &&
                    ` — ${layout.layout.partitions.map((p) => p.mountpoint).filter(Boolean).join(", ")}`}
                </p>
              )}
            </button>
          ))}
        </div>

        <div className="flex justify-end gap-2 mt-4">
          <Button variant="ghost" size="sm" onClick={onClose}>
            <X className="h-3.5 w-3.5 mr-1" />
            Cancel
          </Button>
          <Button
            size="sm"
            disabled={!selected || setOverride.isPending}
            onClick={handleConfirm}
          >
            {setOverride.isPending ? "Applying…" : "Set Override"}
          </Button>
        </div>

        {setOverride.isError && (
          <p className="text-xs text-destructive mt-1">{String(setOverride.error)}</p>
        )}
      </DialogContent>
    </Dialog>
  )
}
