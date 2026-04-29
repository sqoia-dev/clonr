import * as React from "react"
import { useNavigate, useSearch } from "@tanstack/react-router"
import { useQuery } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import { Search, ChevronUp, ChevronDown, ChevronsUpDown, Copy, Check } from "lucide-react"
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
import { StatusDot } from "@/components/StatusDot"
import { useAuth } from "@/contexts/auth"
import { useConnection } from "@/contexts/connection"
import { apiFetch, sseUrl } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type { NodeConfig, ListNodesResponse } from "@/lib/types"
import { nodeState } from "@/lib/types"
import { cn } from "@/lib/utils"

interface NodeSearch {
  q?: string
  status?: string
  sort?: string
  dir?: "asc" | "desc"
}

export function NodesPage() {
  const { apiKey } = useAuth()
  const { setStatus } = useConnection()
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as NodeSearch

  // URL-driven state
  const q = search.q ?? ""
  const sortCol = search.sort ?? ""
  const sortDir = search.dir ?? "asc"
  const [advanced, setAdvanced] = React.useState(false)
  const [selectedNode, setSelectedNode] = React.useState<NodeConfig | null>(null)

  function updateSearch(patch: Partial<NodeSearch>) {
    navigate({
      to: "/nodes",
      search: {
        q: patch.q !== undefined ? patch.q : q || undefined,
        status: patch.status !== undefined ? patch.status : search.status,
        sort: patch.sort !== undefined ? patch.sort : sortCol || undefined,
        dir: patch.dir !== undefined ? patch.dir : sortDir === "asc" ? undefined : "desc",
      },
      replace: true,
    })
  }

  // TanStack Query for nodes
  const { data, refetch } = useQuery<ListNodesResponse>({
    queryKey: ["nodes", q, sortCol, sortDir],
    queryFn: () => {
      const params = new URLSearchParams()
      if (q) params.set("search", q)
      if (sortCol) params.set("sort", sortCol)
      if (sortDir) params.set("dir", sortDir)
      return apiFetch<ListNodesResponse>(`/api/v1/nodes?${params}`, apiKey)
    },
    refetchInterval: 30000,
    staleTime: 10000,
  })

  // SSE subscription for live updates
  React.useEffect(() => {
    if (!apiKey) return
    let es: EventSource | null = null
    let reconnectTimer: ReturnType<typeof setTimeout>

    function connect() {
      const url = sseUrl(`/api/v1/logs/stream?component=node-heartbeat`)
      es = new EventSource(url)

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
  }, [apiKey, refetch, setStatus])

  // Update connection status when query has data
  React.useEffect(() => {
    if (data) setStatus("connected")
  }, [data, setStatus])

  const nodes = data?.nodes ?? []

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
        {nodes.length === 0 ? (
          <EmptyState />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>
                  <button
                    className="flex items-center gap-1 hover:text-foreground"
                    onClick={() => handleSort("hostname")}
                  >
                    Hostname <SortIcon col="hostname" />
                  </button>
                </TableHead>
                <TableHead>
                  <button
                    className="flex items-center gap-1 hover:text-foreground"
                    onClick={() => handleSort("status")}
                  >
                    Status <SortIcon col="status" />
                  </button>
                </TableHead>
                <TableHead>Role / Tags</TableHead>
                <TableHead>
                  <button
                    className="flex items-center gap-1 hover:text-foreground"
                    onClick={() => handleSort("last_deploy")}
                  >
                    Last heartbeat <SortIcon col="last_deploy" />
                  </button>
                </TableHead>
                <TableHead>Image</TableHead>
                {advanced && (
                  <>
                    <TableHead>MAC</TableHead>
                    <TableHead>Firmware</TableHead>
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
        />
      )}
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
}

function NodeSheet({ node, onClose, advanced, relativeTime }: NodeSheetProps) {
  const state = nodeState(node)

  function handleReimage() {
    console.log({ intent: "reimage", nodeId: node.id })
    toast({ title: "Reimage flow lands Sprint 2." })
  }

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

          <div className="pt-4 border-t border-border">
            <Button variant="outline" onClick={handleReimage} className="w-full text-status-warning border-status-warning/40 hover:bg-status-warning/10">
              Reimage node
            </Button>
            <p className="text-xs text-muted-foreground mt-2 text-center">
              Reimage flow lands Sprint 2.
            </p>
          </div>
        </div>
      </SheetContent>
    </Sheet>
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
