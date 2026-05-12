// NodeDangerZone — collapsible red-bordered card housing all destructive node actions.
//
// Replaces the three stacked buttons (Reimage / Capture / Change Boot Settings)
// and the separate Delete button at the bottom of NodeDetailContent.
//
// Default state: collapsed with a "Show destructive actions" toggle.
// Expanded state: four action rows, each opening a typed-confirm modal or the
// existing BootSettingsModal (no typed-confirm needed for that one).

import * as React from "react"
import { AlertTriangle, ChevronDown, ChevronRight } from "lucide-react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { TypedConfirmDialog } from "@/components/TypedConfirmDialog"
import { BootSettingsModal } from "@/components/BootSettingsModal"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type { NodeConfig, ListImagesResponse, ReimageRequest } from "@/lib/types"
import { nodeState } from "@/lib/types"

// ─── Types ────────────────────────────────────────────────────────────────────

type DangerAction = "reimage" | "capture" | "delete" | "boot" | null

export interface NodeDangerZoneProps {
  node: NodeConfig
  /** Called after a successful delete so the parent can navigate away. */
  onDeleted: () => void
  /** If true, expand the zone immediately (e.g. from a query-param trigger). */
  autoExpand?: boolean
}

// ─── NodeDangerZone ───────────────────────────────────────────────────────────

export function NodeDangerZone({ node, onDeleted, autoExpand }: NodeDangerZoneProps) {
  const qc = useQueryClient()
  const [zoneExpanded, setZoneExpanded] = React.useState(autoExpand ?? false)
  const [activeAction, setActiveAction] = React.useState<DangerAction>(null)
  const [bootOpen, setBootOpen] = React.useState(false)

  // ── Reimage state ────────────────────────────────────────────────────────────
  const [selectedImageId, setSelectedImageId] = React.useState(node.base_image_id || "")
  const [reimageError, setReimageError] = React.useState<string | null>(null)

  // ── Capture state ────────────────────────────────────────────────────────────
  const [captureImageName, setCaptureImageName] = React.useState("")
  const [captureError, setCaptureError] = React.useState<string | null>(null)

  // ── Delete state ─────────────────────────────────────────────────────────────
  const [deleteError, setDeleteError] = React.useState<string | null>(null)

  // ── Image list (for reimage selector) ────────────────────────────────────────
  const { data: imagesData, isLoading: imagesLoading } = useQuery<ListImagesResponse>({
    queryKey: ["images", "base"],
    queryFn: () => apiFetch<ListImagesResponse>("/api/v1/images?kind=base"),
    staleTime: 60_000,
    enabled: zoneExpanded,
  })

  // Sync selectedImageId when images load and node has an assigned image.
  React.useEffect(() => {
    if (imagesData && selectedImageId === "" && node.base_image_id) {
      setSelectedImageId(node.base_image_id)
    }
  }, [imagesData, node.base_image_id, selectedImageId])

  const readyImages = imagesData?.images?.filter((img) => img.status === "ready") ?? []
  const allImages   = imagesData?.images ?? []

  // Resolve current image name for the button label.
  const currentImageName = allImages.find((img) => img.id === node.base_image_id)
    ? `${allImages.find((img) => img.id === node.base_image_id)!.name} ${allImages.find((img) => img.id === node.base_image_id)!.version}`
    : node.base_image_id ? node.base_image_id.slice(0, 12) : null

  const reimageButtonLabel = currentImageName
    ? `Reimage with ${currentImageName}`
    : "Reimage node"

  // ── Active reimage poll ───────────────────────────────────────────────────────
  const { data: activeReimage } = useQuery<ReimageRequest | null>({
    queryKey: ["reimage-active", node.id],
    queryFn: async () => {
      try {
        return await apiFetch<ReimageRequest>(`/api/v1/nodes/${node.id}/reimage/active`)
      } catch {
        return null
      }
    },
    refetchInterval: 3_000,
    staleTime: 2_000,
    enabled: zoneExpanded,
  })

  const state = nodeState(node)
  const isProvisioning =
    node.reimage_pending &&
    activeReimage &&
    ["pending", "triggered", "in_progress"].includes(activeReimage.status)

  const isDeploying = state === "deploying" || state === "reimage_pending"

  // ── Mutations ─────────────────────────────────────────────────────────────────

  const reimageMutation = useMutation({
    mutationFn: () =>
      apiFetch<ReimageRequest>(`/api/v1/nodes/${node.id}/reimage`, {
        method: "POST",
        body: JSON.stringify({ image_id: selectedImageId }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] })
      qc.invalidateQueries({ queryKey: ["reimage-active", node.id] })
      setActiveAction(null)
      setReimageError(null)
      toast({
        title: "Reimage triggered",
        description: `Node ${node.hostname || node.id} is now provisioning.`,
      })
    },
    onError: (err) => {
      setReimageError(String(err))
    },
  })

  const captureMutation = useMutation({
    mutationFn: () =>
      apiFetch<{ id: string }>("/api/v1/factory/capture", {
        method: "POST",
        body: JSON.stringify({
          source_host: node.hostname || node.fqdn || node.id,
          ssh_user: "root",
          name: captureImageName || `${node.hostname}-capture`,
          version: "1.0.0",
          exclude_paths: ["/proc", "/sys", "/dev", "/tmp", "/run"],
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["images"] })
      setActiveAction(null)
      setCaptureError(null)
      toast({
        title: "Capture started",
        description: `Capturing ${node.hostname} in background. Check Images for progress.`,
      })
    },
    onError: (err) => {
      setCaptureError(String(err))
    },
  })

  const deleteMutation = useMutation({
    mutationFn: () =>
      apiFetch<void>(`/api/v1/nodes/${node.id}`, { method: "DELETE" }),
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: ["nodes"] })
      const prev = qc.getQueryData<{ nodes: NodeConfig[]; total: number }>(["nodes"])
      if (prev) {
        qc.setQueryData<{ nodes: NodeConfig[]; total: number }>(["nodes"], {
          ...prev,
          nodes: prev.nodes.filter((n) => n.id !== node.id),
          total: Math.max(0, prev.total - 1),
        })
      }
      return { prev }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] })
      toast({ title: `Node deleted: ${node.hostname}` })
      setActiveAction(null)
      onDeleted()
    },
    onError: (err, _vars, context) => {
      if (context?.prev) qc.setQueryData(["nodes"], context.prev)
      const msg = String(err)
      if (msg.includes("409") || msg.toLowerCase().includes("deploy")) {
        setDeleteError("Cannot delete: node is currently deploying. Cancel deployment first.")
      } else {
        setDeleteError(msg)
      }
    },
  })

  function openAction(action: DangerAction) {
    // Reset error state when opening a fresh modal.
    setReimageError(null)
    setCaptureError(null)
    setDeleteError(null)
    setActiveAction(action)
  }

  function closeAction() {
    if (reimageMutation.isPending || captureMutation.isPending || deleteMutation.isPending) return
    setActiveAction(null)
  }

  return (
    <>
      {/* Reimage in-progress banner (visible regardless of zone collapsed state) */}
      {isProvisioning && activeReimage && (
        <div className="rounded-md border border-border bg-card p-3 space-y-1.5">
          <div className="flex items-center gap-2 text-sm">
            <span className="h-2 w-2 rounded-full bg-status-warning animate-pulse shrink-0" />
            <span className="font-medium">Reimage in progress</span>
            <span className="text-xs text-muted-foreground ml-auto">{activeReimage.status}</span>
          </div>
          <div className="h-1.5 rounded-full bg-secondary overflow-hidden">
            <div
              className="h-full bg-status-warning transition-all duration-500 animate-pulse"
              style={{
                width:
                  activeReimage.status === "in_progress" ? "60%"
                  : activeReimage.status === "triggered" ? "20%"
                  : "10%",
              }}
            />
          </div>
          {activeReimage.error_message && (
            <p className="text-xs text-destructive">{activeReimage.error_message}</p>
          )}
        </div>
      )}

      {/* Danger Zone card */}
      <div
        className={cn(
          "rounded-md border",
          zoneExpanded
            ? "border-destructive/40 bg-destructive/5"
            : "border-destructive/25",
        )}
        data-testid="node-danger-zone"
      >
        {/* Header / toggle */}
        <button
          className="w-full flex items-center gap-2 px-4 py-3 text-sm font-medium text-left transition-colors hover:bg-destructive/10 rounded-md"
          onClick={() => setZoneExpanded((v) => !v)}
          aria-expanded={zoneExpanded}
          data-testid="danger-zone-toggle"
        >
          <AlertTriangle className="h-4 w-4 text-destructive shrink-0" />
          <span className="text-destructive">Danger Zone</span>
          <span className="flex-1" />
          {zoneExpanded ? (
            <ChevronDown className="h-4 w-4 text-muted-foreground" />
          ) : (
            <>
              <span className="text-xs text-muted-foreground mr-1">Show destructive actions</span>
              <ChevronRight className="h-4 w-4 text-muted-foreground" />
            </>
          )}
        </button>

        {zoneExpanded && (
          <div className="px-4 pb-4 space-y-2 border-t border-destructive/20 pt-3">
            {/* Reimage */}
            <DangerRow
              label={imagesLoading ? "Reimage node" : reimageButtonLabel}
              buttonLabel="Reimage…"
              disabled={!!isProvisioning}
              disabledReason="Reimage in progress"
              onClick={() => openAction("reimage")}
              testId="danger-reimage-btn"
            />

            {/* Capture */}
            <DangerRow
              label="Capture as base image"
              buttonLabel="Capture…"
              onClick={() => openAction("capture")}
              testId="danger-capture-btn"
            />

            {/* Boot settings */}
            <DangerRow
              label="Change Boot Settings"
              buttonLabel="Configure…"
              onClick={() => setBootOpen(true)}
              testId="danger-boot-btn"
            />

            {/* Delete */}
            <DangerRow
              label="Delete this node"
              buttonLabel="Delete…"
              disabled={isDeploying}
              disabledReason="Cannot delete while deploying"
              destructive
              onClick={() => openAction("delete")}
              testId="danger-delete-btn"
            />
          </div>
        )}
      </div>

      {/* ── Reimage modal ──────────────────────────────────────────────────────── */}
      <TypedConfirmDialog
        open={activeAction === "reimage"}
        onClose={closeAction}
        onConfirm={() => reimageMutation.mutate()}
        title="Reimage node — this will reinstall the OS"
        description={
          <p>Select a target image and type <code className="font-mono font-semibold text-foreground">REIMAGE</code> to proceed.</p>
        }
        confirmToken="REIMAGE"
        confirmPrompt='Type "REIMAGE" to confirm:'
        confirmButtonLabel="Confirm reimage"
        confirmButtonVariant="destructive"
        isPending={reimageMutation.isPending}
        error={reimageError}
        extraContent={
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground">Target image</label>
            {imagesLoading ? (
              <Skeleton className="h-8 w-full" />
            ) : (
              <select
                className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
                value={selectedImageId}
                onChange={(e) => setSelectedImageId(e.target.value)}
                disabled={reimageMutation.isPending}
              >
                <option value="">Select target image…</option>
                {readyImages.map((img) => (
                  <option key={img.id} value={img.id}>
                    {img.name} {img.version} ({img.id.slice(0, 8)})
                  </option>
                ))}
              </select>
            )}
          </div>
        }
      />

      {/* ── Capture modal ──────────────────────────────────────────────────────── */}
      <TypedConfirmDialog
        open={activeAction === "capture"}
        onClose={closeAction}
        onConfirm={() => captureMutation.mutate()}
        title="Capture node as base image"
        description={
          <p>
            An SSH rsync capture will run in the background. Enter an image name and type the hostname to confirm.
          </p>
        }
        confirmToken={node.hostname}
        caseSensitive
        confirmPrompt={`Type "${node.hostname}" to confirm:`}
        confirmButtonLabel="Start capture"
        confirmButtonVariant="destructive"
        isPending={captureMutation.isPending}
        error={captureError}
        extraContent={
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground">Image name</label>
            <input
              className="w-full text-xs font-mono border border-border bg-background rounded-md px-3 py-1.5 focus:outline-none focus:ring-1 focus:ring-ring"
              placeholder={`${node.hostname}-capture`}
              value={captureImageName}
              onChange={(e) => setCaptureImageName(e.target.value)}
              disabled={captureMutation.isPending}
            />
          </div>
        }
      />

      {/* ── Delete modal ───────────────────────────────────────────────────────── */}
      <TypedConfirmDialog
        open={activeAction === "delete"}
        onClose={closeAction}
        onConfirm={() => deleteMutation.mutate()}
        title="Delete node — this is permanent"
        description={
          <p>All node data, reimage history, and configuration will be removed.</p>
        }
        confirmToken={node.hostname}
        caseSensitive
        confirmPrompt={`Type "${node.hostname}" to confirm:`}
        confirmButtonLabel="Delete permanently"
        confirmButtonVariant="destructive"
        isPending={deleteMutation.isPending}
        error={deleteError}
      />

      {/* ── Boot settings modal ────────────────────────────────────────────────── */}
      <BootSettingsModal
        open={bootOpen}
        onClose={() => setBootOpen(false)}
        node={node}
      />
    </>
  )
}

// ─── DangerRow ────────────────────────────────────────────────────────────────

interface DangerRowProps {
  label: string
  buttonLabel: string
  disabled?: boolean
  disabledReason?: string
  destructive?: boolean
  onClick: () => void
  testId?: string
}

function DangerRow({ label, buttonLabel, disabled, disabledReason, destructive, onClick, testId }: DangerRowProps) {
  return (
    <div className="flex items-center justify-between gap-3 py-1">
      <div className="min-w-0">
        <span className={cn("text-sm", destructive ? "text-destructive" : "text-foreground")}>
          {label}
        </span>
        {disabled && disabledReason && (
          <p className="text-xs text-muted-foreground">{disabledReason}</p>
        )}
      </div>
      <Button
        variant={destructive ? "destructive" : "outline"}
        size="sm"
        className="shrink-0 h-7 text-xs"
        disabled={disabled}
        onClick={onClick}
        data-testid={testId}
      >
        {buttonLabel}
      </Button>
    </div>
  )
}
