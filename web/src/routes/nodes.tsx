import * as React from "react"
import { useNavigate, useSearch } from "@tanstack/react-router"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import { Search, ChevronUp, ChevronDown, ChevronsUpDown, Copy, Check, AlertTriangle } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from "@/components/ui/table"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusDot } from "@/components/StatusDot"
import { useConnection } from "@/contexts/connection"
import { apiFetch, sseUrl } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type { NodeConfig, ListNodesResponse, ListImagesResponse, ReimageRequest } from "@/lib/types"
import { nodeState } from "@/lib/types"
import { cn } from "@/lib/utils"

interface NodeSearch {
  q?: string
  status?: string
  sort?: string
  dir?: "asc" | "desc"
  openNode?: string
  reimage?: string
}

export function NodesPage() {
  const { setStatus, retryToken } = useConnection()
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as NodeSearch

  // URL-driven state
  const q = search.q ?? ""
  const sortCol = search.sort ?? ""
  const sortDir = search.dir ?? "asc"
  const [advanced, setAdvanced] = React.useState(false)
  const [selectedNode, setSelectedNode] = React.useState<NodeConfig | null>(null)
  // PAL-2-2: auto-open node from URL param (used by Cmd-K "Reimage node…").
  const openNodeId = search.openNode
  const autoReimage = search.reimage === "1"

  function updateSearch(patch: Partial<NodeSearch>) {
    navigate({
      to: "/nodes",
      search: {
        q: patch.q !== undefined ? patch.q : q || undefined,
        status: patch.status !== undefined ? patch.status : search.status,
        sort: patch.sort !== undefined ? patch.sort : sortCol || undefined,
        dir: patch.dir !== undefined ? patch.dir : sortDir === "asc" ? undefined : "desc",
        openNode: undefined,
        reimage: undefined,
      },
      replace: true,
    })
  }

  // TanStack Query for nodes
  const { data, refetch, isLoading } = useQuery<ListNodesResponse>({
    queryKey: ["nodes", q, sortCol, sortDir],
    queryFn: () => {
      const params = new URLSearchParams()
      if (q) params.set("search", q)
      if (sortCol) params.set("sort", sortCol)
      if (sortDir) params.set("dir", sortDir)
      return apiFetch<ListNodesResponse>(`/api/v1/nodes?${params}`)
    },
    refetchInterval: 30000,
    staleTime: 10000,
  })

  // SSE subscription for live updates
  React.useEffect(() => {
    let es: EventSource | null = null
    let reconnectTimer: ReturnType<typeof setTimeout>

    function connect() {
      const url = sseUrl(`/api/v1/logs/stream?component=node-heartbeat`)
      es = new EventSource(url, { withCredentials: true })

      es.onopen = () => {
        setStatus("connected")
      }

      es.onmessage = () => {
        refetch()
      }

      es.onerror = () => {
        setStatus("reconnecting")
        es?.close()
        reconnectTimer = setTimeout(connect, 5000)
      }
    }

    connect()
    return () => {
      clearTimeout(reconnectTimer)
      es?.close()
      setStatus("disconnected")
    }
    // retryToken forces reconnect when the banner "Retry" is clicked (POL-6).
  }, [refetch, setStatus, retryToken])

  // Update connection status when query has data
  React.useEffect(() => {
    if (data) setStatus("connected")
  }, [data, setStatus])

  const nodes = data?.nodes ?? []

  // PAL-2-2: auto-open node sheet when ?openNode=<id> is in the URL.
  React.useEffect(() => {
    if (!openNodeId || nodes.length === 0) return
    const target = nodes.find((n) => n.id === openNodeId)
    if (target && !selectedNode) {
      setSelectedNode(target)
      // Clear the param so back-navigation doesn't re-open.
      navigate({
        to: "/nodes",
        search: { q: q || undefined, status: search.status, sort: sortCol || undefined, dir: sortDir === "asc" ? undefined : "desc", openNode: undefined, reimage: undefined },
        replace: true,
      })
    }
  }, [openNodeId, nodes, selectedNode, navigate, q, search.status, sortCol, sortDir])

  function handleSort(col: string) {
    if (sortCol === col) {
      updateSearch({ dir: sortDir === "asc" ? "desc" : "asc" })
    } else {
      updateSearch({ sort: col, dir: "asc" })
    }
  }

  function SortIcon({ col }: { col: string }) {
    if (sortCol !== col) return <ChevronsUpDown className="h-3 w-3 opacity-40" />
    return sortDir === "asc" ? (
      <ChevronUp className="h-3 w-3" />
    ) : (
      <ChevronDown className="h-3 w-3" />
    )
  }

  function relativeTime(iso?: string) {
    if (!iso) return "—"
    try {
      return formatDistanceToNow(new Date(iso), { addSuffix: true })
    } catch {
      return "—"
    }
  }

  return (
    <div className="flex flex-col h-full">
      {/* Toolbar */}
      <div className="flex items-center justify-between gap-3 border-b border-border px-6 py-3">
        <div className="relative w-72">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            className="pl-8"
            placeholder="Search nodes..."
            value={q}
            onChange={(e) => updateSearch({ q: e.target.value || undefined })}
          />
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => setAdvanced((a) => !a)}
          className={cn(advanced && "bg-secondary")}
        >
          {advanced ? "Basic view" : "Advanced"}
        </Button>
      </div>

      {/* Table */}
      <div className="flex-1 overflow-auto">
        {isLoading ? (
          <NodesSkeleton />
        ) : nodes.length === 0 ? (
          <EmptyState />
        ) : (
          <Table>
            <caption className="sr-only">Registered cluster nodes</caption>
            <TableHeader>
              <TableRow>
                <TableHead scope="col">
                  <button
                    className="flex items-center gap-1 hover:text-foreground"
                    onClick={() => handleSort("hostname")}
                  >
                    Hostname <SortIcon col="hostname" />
                  </button>
                </TableHead>
                <TableHead scope="col">
                  <button
                    className="flex items-center gap-1 hover:text-foreground"
                    onClick={() => handleSort("status")}
                  >
                    Status <SortIcon col="status" />
                  </button>
                </TableHead>
                <TableHead scope="col">Role / Tags</TableHead>
                <TableHead scope="col">
                  <button
                    className="flex items-center gap-1 hover:text-foreground"
                    onClick={() => handleSort("last_deploy")}
                  >
                    Last heartbeat <SortIcon col="last_deploy" />
                  </button>
                </TableHead>
                <TableHead scope="col">Image</TableHead>
                {advanced && (
                  <>
                    <TableHead scope="col">MAC</TableHead>
                    <TableHead scope="col">Firmware</TableHead>
                  </>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {nodes.map((node) => (
                <TableRow
                  key={node.id}
                  className="cursor-pointer"
                  onClick={() => setSelectedNode(node)}
                >
                  <TableCell>
                    <span className="font-mono text-xs">{node.hostname || node.id}</span>
                  </TableCell>
                  <TableCell>
                    <StatusDot state={nodeState(node)} />
                  </TableCell>
                  <TableCell>
                    <span className="text-xs text-muted-foreground">
                      {node.tags?.join(", ") || "—"}
                    </span>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {relativeTime(node.last_seen_at ?? node.deploy_verified_booted_at)}
                  </TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">
                    {node.base_image_id ? node.base_image_id.slice(0, 8) : "—"}
                  </TableCell>
                  {advanced && (
                    <>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {node.primary_mac}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {node.detected_firmware || "—"}
                      </TableCell>
                    </>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>

      {/* Node detail sheet */}
      {selectedNode && (
        <NodeSheet
          node={selectedNode}
          onClose={() => setSelectedNode(null)}
          advanced={advanced}
          relativeTime={relativeTime}
          autoReimage={autoReimage}
        />
      )}
    </div>
  )
}

function NodesSkeleton() {
  return (
    <div className="p-4 space-y-2">
      {Array.from({ length: 5 }).map((_, i) => (
        <Skeleton key={i} className="h-10 w-full rounded" />
      ))}
    </div>
  )
}

function EmptyState() {
  const [copied, setCopied] = React.useState(false)
  const snippet = `clustr --server http://<server>:8080 deploy --auto`

  function copy() {
    navigator.clipboard.writeText(snippet).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  return (
    <div className="flex flex-col items-center justify-center h-full min-h-64 gap-4 p-8 text-center">
      <div className="space-y-1">
        <h2 className="text-base font-semibold">No nodes registered yet</h2>
        <p className="text-sm text-muted-foreground">
          PXE-boot a node to register it automatically, or run:
        </p>
      </div>
      <div className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2 max-w-lg">
        <code className="text-xs font-mono flex-1 text-left">{snippet}</code>
        <Button variant="ghost" size="icon" className="h-7 w-7 shrink-0" onClick={copy}>
          {copied ? <Check className="h-3.5 w-3.5 text-status-healthy" /> : <Copy className="h-3.5 w-3.5" />}
        </Button>
      </div>
    </div>
  )
}

interface NodeSheetProps {
  node: NodeConfig
  onClose: () => void
  advanced: boolean
  relativeTime: (iso?: string) => string
  autoReimage?: boolean
}

function NodeSheet({ node, onClose, advanced, relativeTime, autoReimage }: NodeSheetProps) {
  const state = nodeState(node)

  return (
    <Sheet open onOpenChange={(v) => !v && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-xl overflow-y-auto">
        <SheetHeader>
          <SheetTitle className="font-mono">{node.hostname || node.id}</SheetTitle>
          <SheetDescription>
            <StatusDot state={state} />
          </SheetDescription>
        </SheetHeader>

        <div className="mt-6 space-y-4">
          <Section title="Identity">
            <Row label="ID" value={node.id} mono />
            <Row label="Hostname" value={node.hostname} />
            <Row label="FQDN" value={node.fqdn || "—"} />
            <Row label="MAC" value={node.primary_mac} mono />
            <Row label="Firmware" value={node.detected_firmware || "—"} />
          </Section>

          <Section title="Deployment">
            <Row label="Image" value={node.base_image_id || "—"} mono />
            <Row label="State" value={state} />
            <Row label="Last seen" value={relativeTime(node.last_seen_at ?? node.deploy_verified_booted_at)} />
            <Row label="Deploy complete" value={relativeTime(node.deploy_completed_preboot_at)} />
            <Row label="Verified boot" value={relativeTime(node.deploy_verified_booted_at)} />
          </Section>

          {node.tags?.length > 0 && (
            <Section title="Tags">
              <div className="flex flex-wrap gap-1.5">
                {node.tags.map((t) => (
                  <span key={t} className="rounded bg-secondary px-2 py-0.5 text-xs font-mono">
                    {t}
                  </span>
                ))}
              </div>
            </Section>
          )}

          {advanced && (
            <Section title="Advanced">
              <Row label="Group" value={node.group_id || "—"} mono />
              <Row label="Created" value={relativeTime(node.created_at)} />
              <Row label="Updated" value={relativeTime(node.updated_at)} />
            </Section>
          )}

          <ReimageFlow node={node} autoExpand={autoReimage} />
        </div>
      </SheetContent>
    </Sheet>
  )
}

// ─── Reimage inline flow (REIMG-1..6) ────────────────────────────────────────

function ReimageFlow({ node, autoExpand }: { node: NodeConfig; autoExpand?: boolean }) {
  const qc = useQueryClient()
  const [expanded, setExpanded] = React.useState(autoExpand ?? false)
  const [selectedImageId, setSelectedImageId] = React.useState("")
  const [confirmId, setConfirmId] = React.useState("")

  // Fetch available base images for selector.
  const { data: imagesData, isLoading: imagesLoading } = useQuery<ListImagesResponse>({
    queryKey: ["images"],
    queryFn: () => apiFetch<ListImagesResponse>("/api/v1/images"),
    staleTime: 30000,
    enabled: expanded,
  })

  // Poll active reimage for this node.
  const { data: activeReimage } = useQuery<ReimageRequest | null>({
    queryKey: ["reimage-active", node.id],
    queryFn: async () => {
      try {
        return await apiFetch<ReimageRequest>(`/api/v1/nodes/${node.id}/reimage/active`)
      } catch {
        return null
      }
    },
    refetchInterval: 3000,
    staleTime: 2000,
  })

  const reimageMutation = useMutation({
    mutationFn: () =>
      apiFetch<ReimageRequest>(`/api/v1/nodes/${node.id}/reimage`, {
        method: "POST",
        body: JSON.stringify({ image_id: selectedImageId }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] })
      qc.invalidateQueries({ queryKey: ["reimage-active", node.id] })
      setExpanded(false)
      setConfirmId("")
      setSelectedImageId("")
      toast({
        title: "Reimage triggered",
        description: `Node ${node.hostname || node.id} is now provisioning.`,
      })
    },
    onError: (err) => {
      toast({
        variant: "destructive",
        title: "Reimage failed",
        description: String(err),
      })
    },
  })

  const readyImages = imagesData?.images?.filter((img) => img.status === "ready") ?? []
  const canConfirm = confirmId === node.id && selectedImageId !== ""

  const isProvisioning = activeReimage && ["pending", "triggered", "in_progress"].includes(activeReimage.status)

  return (
    <div className="pt-4 border-t border-border space-y-3">
      {/* Active reimage progress (REIMG-5) */}
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
              style={{ width: activeReimage.status === "in_progress" ? "60%" : activeReimage.status === "triggered" ? "20%" : "10%" }}
            />
          </div>
          {activeReimage.error_message && (
            <p className="text-xs text-destructive">{activeReimage.error_message}</p>
          )}
        </div>
      )}

      {/* Expand / collapse reimage form (REIMG-2) */}
      {!expanded ? (
        <Button
          variant="outline"
          className="w-full text-status-warning border-status-warning/40 hover:bg-status-warning/10"
          onClick={() => setExpanded(true)}
          disabled={!!isProvisioning}
        >
          Reimage node
        </Button>
      ) : (
        <div className="rounded-md border border-status-warning/30 bg-status-warning/5 p-4 space-y-3">
          <div className="flex items-center gap-2 text-sm font-medium text-status-warning">
            <AlertTriangle className="h-4 w-4 shrink-0" />
            Reimage node — this will reinstall the OS
          </div>

          {/* Current → target diff */}
          <div className="text-xs text-muted-foreground flex items-center gap-2">
            <span className="font-mono">{node.base_image_id ? node.base_image_id.slice(0, 12) : "no image"}</span>
            <span>→</span>
            <span className="font-mono">{selectedImageId ? selectedImageId.slice(0, 12) : "(select target)"}</span>
          </div>

          {/* Target image selector (REIMG-3) */}
          {imagesLoading ? (
            <Skeleton className="h-8 w-full" />
          ) : (
            <select
              className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
              value={selectedImageId}
              onChange={(e) => setSelectedImageId(e.target.value)}
            >
              <option value="">Select target image…</option>
              {readyImages.map((img) => (
                <option key={img.id} value={img.id}>
                  {img.name} {img.version} ({img.id.slice(0, 8)})
                </option>
              ))}
            </select>
          )}

          {/* Typed node ID confirmation */}
          <div className="space-y-1">
            <p className="text-xs text-muted-foreground">
              Type <code className="font-mono">{node.id}</code> to confirm:
            </p>
            <Input
              className="font-mono text-xs"
              placeholder={node.id}
              value={confirmId}
              onChange={(e) => setConfirmId(e.target.value)}
            />
          </div>

          <div className="flex gap-2">
            <Button
              variant="destructive"
              size="sm"
              className="flex-1"
              disabled={!canConfirm || reimageMutation.isPending}
              onClick={() => reimageMutation.mutate()}
            >
              {reimageMutation.isPending ? "Triggering…" : "Confirm reimage"}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => { setExpanded(false); setConfirmId(""); setSelectedImageId("") }}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="space-y-2">
      <h3 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{title}</h3>
      <div className="space-y-1.5">{children}</div>
    </div>
  )
}

function Row({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4 text-sm">
      <span className="text-muted-foreground shrink-0">{label}</span>
      <span className={cn("text-right break-all", mono && "font-mono text-xs")}>
        {value ?? "—"}
      </span>
    </div>
  )
}
