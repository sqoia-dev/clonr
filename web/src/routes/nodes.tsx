import * as React from "react"
import { useNavigate, useSearch } from "@tanstack/react-router"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import {
  Search, ChevronUp, ChevronDown, ChevronRight, ChevronsUpDown, Copy, Check, AlertTriangle, Plus, Pencil, X, Tag, Trash2,
  Power, PowerOff, RefreshCw, RotateCcw, Network, HardDrive, Cpu, Camera, Users, Loader2, Activity, BookOpen, Terminal, ScrollText,
} from "lucide-react"
import { SensorsTab, EventLogTab, ConsoleTab, DeployLogTab } from "@/routes/node-detail-tabs"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
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
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { StatusDot } from "@/components/StatusDot"
import { useEventInvalidation } from "@/contexts/connection"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type { NodeConfig, ListNodesResponse, ListImagesResponse, ReimageRequest, PowerStatusResponse, SensorsResponse, SlurmNodeRole, SlurmNodeSyncStatus, SlurmNodeOverride } from "@/lib/types"
import { nodeState, NODE_PROVIDERS } from "@/lib/types"
import { cn } from "@/lib/utils"
import { GroupsPanel } from "@/routes/groups"
import { UserPicker } from "@/components/UserPicker"
import type { ListNodeSudoersResponse, UserSearchResult } from "@/lib/types"

// ─── Zod-like validation helpers (no extra dep) ──────────────────────────────
const hostnameRe = /^[a-z0-9-]{1,63}$/
const macRe = /^([0-9a-f]{2}:){5}[0-9a-f]{2}$/
const ipv4Re = /^(\d{1,3}\.){3}\d{1,3}(\/\d+)?$/

function normalizeMAC(raw: string): string {
  return raw.toLowerCase().replace(/[^0-9a-f]/g, "").replace(/(.{2})(?=.)/g, "$1:")
}

// ─── AddNodeSheet ──────────────────────────────────────────────────────────────

interface AddNodeSheetProps {
  open: boolean
  onClose: () => void
}

export function AddNodeSheet({ open, onClose }: AddNodeSheetProps) {
  const qc = useQueryClient()
  const [hostname, setHostname] = React.useState("")
  const [mac, setMac] = React.useState("")
  const [ip, setIp] = React.useState("")
  const [roles, setRoles] = React.useState<string[]>([])
  const [provider, setProvider] = React.useState("")
  const [notes, setNotes] = React.useState("")
  const [errors, setErrors] = React.useState<Record<string, string>>({})

  function reset() {
    setHostname(""); setMac(""); setIp(""); setRoles([]); setProvider(""); setNotes(""); setErrors({})
  }

  function handleClose() { reset(); onClose() }

  function validate(): boolean {
    const errs: Record<string, string> = {}
    if (!hostnameRe.test(hostname)) errs.hostname = "Lowercase letters, digits, hyphens, 1–63 chars"
    const normMac = normalizeMAC(mac)
    if (!macRe.test(normMac)) errs.mac = "Must be a valid MAC address (e.g. bc:24:11:36:e9:2f)"
    if (ip && !ipv4Re.test(ip)) errs.ip = "Must be a valid IPv4 address or CIDR (e.g. 10.0.0.5)"
    setErrors(errs)
    return Object.keys(errs).length === 0
  }

  // Fetch base images for base_image_id required by CreateNode.
  // ?kind=base excludes initramfs build artifacts from the picker.
  const { data: imagesData } = useQuery<ListImagesResponse>({
    queryKey: ["images", "base"],
    queryFn: () => apiFetch<ListImagesResponse>("/api/v1/images?kind=base"),
    staleTime: 30000,
    enabled: open,
  })
  const readyImages = imagesData?.images?.filter((img) => img.status === "ready") ?? []
  const [baseImageId, setBaseImageId] = React.useState("")

  const mutation = useMutation({
    mutationFn: async () => {
      const normMac = normalizeMAC(mac)
      const body: Record<string, unknown> = {
        hostname,
        primary_mac: normMac,
        base_image_id: baseImageId || (readyImages[0]?.id ?? ""),
        tags: roles.length ? roles : [],
      }
      if (provider) body.provider = provider
      if (ip) body.interfaces = [{ name: "eth0", mac_address: normMac, ip_address: ip }]
      if (notes) body.notes = notes
      return apiFetch<NodeConfig>("/api/v1/nodes", {
        method: "POST",
        body: JSON.stringify(body),
      })
    },
    onSuccess: (node) => {
      qc.invalidateQueries({ queryKey: ["nodes"] })
      toast({ title: "Node registered", description: `${node.hostname} added successfully.` })
      handleClose()
    },
    onError: (err) => {
      const msg = String(err)
      // Mirror server field errors verbatim
      if (msg.includes("hostname")) setErrors((e) => ({ ...e, hostname: msg }))
      else if (msg.includes("mac")) setErrors((e) => ({ ...e, mac: msg }))
      else toast({ variant: "destructive", title: "Failed to add node", description: msg })
    },
  })

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!validate()) return
    mutation.mutate()
  }

  function toggleRole(r: string) {
    setRoles((prev) => prev.includes(r) ? prev.filter((x) => x !== r) : [...prev, r])
  }

  const [sheetTab, setSheetTab] = React.useState<"single" | "bulk">("single")

  function handleCloseWithTabReset() {
    setSheetTab("single")
    handleClose()
  }

  return (
    <Sheet open={open} onOpenChange={(v) => !v && handleCloseWithTabReset()}>
      <SheetContent side="right" className="w-full sm:max-w-lg overflow-y-auto">
        <SheetHeader>
          <SheetTitle>Add Node</SheetTitle>
          <SheetDescription>Register a node manually (or PXE-boot to auto-register).</SheetDescription>
        </SheetHeader>
        <div className="mt-6">
          <Tabs value={sheetTab} onValueChange={(v) => setSheetTab(v as "single" | "bulk")}>
            <TabsList className="w-full mb-4">
              <TabsTrigger value="single" className="flex-1">Single</TabsTrigger>
              <TabsTrigger value="bulk" className="flex-1">Bulk (CSV/YAML)</TabsTrigger>
            </TabsList>
            <TabsContent value="single">
              <form onSubmit={handleSubmit} className="space-y-4">
                <Field label="Hostname *" error={errors.hostname}>
                  <Input
                    placeholder="compute-01"
                    value={hostname}
                    onChange={(e) => setHostname(e.target.value)}
                    className={cn(errors.hostname && "border-destructive")}
                  />
                </Field>
                <Field label="MAC Address *" error={errors.mac}>
                  <Input
                    placeholder="bc:24:11:36:e9:2f"
                    value={mac}
                    onChange={(e) => setMac(e.target.value)}
                    className={cn(errors.mac && "border-destructive")}
                  />
                </Field>
                <Field label="IP Address (optional — leave blank for DHCP)" error={errors.ip}>
                  <Input
                    placeholder="10.99.0.10 or 10.99.0.10/24"
                    value={ip}
                    onChange={(e) => setIp(e.target.value)}
                    className={cn(errors.ip && "border-destructive")}
                  />
                </Field>
                <Field label="Base Image">
                  <select
                    className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
                    value={baseImageId}
                    onChange={(e) => setBaseImageId(e.target.value)}
                  >
                    <option value="">None (assign later)</option>
                    {readyImages.map((img) => (
                      <option key={img.id} value={img.id}>{img.name} {img.version}</option>
                    ))}
                  </select>
                </Field>
                <Field label="Role (select all that apply)">
                  <div className="flex gap-3">
                    {(["controller", "worker"] as const).map((r) => (
                      <label key={r} className="flex items-center gap-1.5 text-sm cursor-pointer">
                        <input
                          type="checkbox"
                          checked={roles.includes(r)}
                          onChange={() => toggleRole(r)}
                          className="rounded"
                        />
                        {r}
                      </label>
                    ))}
                  </div>
                </Field>
                <Field label="Provider">
                  <select
                    className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
                    value={provider}
                    onChange={(e) => setProvider(e.target.value)}
                  >
                    {NODE_PROVIDERS.map((p) => (
                      <option key={p.value} value={p.value}>{p.label}</option>
                    ))}
                  </select>
                </Field>
                <Field label="Notes (optional)">
                  <textarea
                    className="w-full text-sm border border-border bg-background rounded-md px-3 py-2 resize-none"
                    rows={2}
                    placeholder="Optional notes about this node…"
                    value={notes}
                    onChange={(e) => setNotes(e.target.value)}
                  />
                </Field>
                <div className="flex gap-2 pt-2">
                  <Button type="submit" className="flex-1" disabled={mutation.isPending}>
                    {mutation.isPending ? "Registering…" : "Register Node"}
                  </Button>
                  <Button type="button" variant="ghost" onClick={handleCloseWithTabReset}>Cancel</Button>
                </div>
              </form>
            </TabsContent>
            <TabsContent value="bulk">
              <BulkAddNodes onSuccess={handleCloseWithTabReset} readyImages={readyImages} />
            </TabsContent>
          </Tabs>
        </div>
      </SheetContent>
    </Sheet>
  )
}

// ─── BulkAddNodes ─────────────────────────────────────────────────────────────
// BULK-2..5: CSV or YAML paste → preview table → batch submit with per-row results.

interface BulkRow {
  hostname: string
  mac: string
  ip?: string
  role?: string
  base_image_id?: string
  // parse error for this row (client-side)
  parseError?: string
}

interface BatchResult {
  index: number
  status: "created" | "failed" | "skipped"
  id?: string
  error?: string
}

function parseBulkInput(raw: string): BulkRow[] {
  const trimmed = raw.trim()
  if (!trimmed) return []

  // Auto-detect: YAML starts with '-' or 'hostname:' or has ':' on the first non-blank line.
  const firstLine = trimmed.split("\n").find((l) => l.trim() !== "") ?? ""
  const isYAML = firstLine.trim().startsWith("-") || firstLine.includes("hostname:")

  if (isYAML) {
    // Minimal YAML list parse — handles `- hostname: x\n  mac: y` blocks.
    const rows: BulkRow[] = []
    let current: BulkRow | null = null
    for (const line of trimmed.split("\n")) {
      const t = line.trim()
      if (t.startsWith("- ") || t === "-") {
        if (current) rows.push(current)
        current = { hostname: "", mac: "" }
        const rest = t.slice(2).trim()
        if (rest) {
          const [k, ...vParts] = rest.split(":")
          const v = vParts.join(":").trim()
          assignField(current, k.trim(), v)
        }
      } else if (current && t.includes(":")) {
        const [k, ...vParts] = t.split(":")
        const v = vParts.join(":").trim()
        assignField(current, k.trim(), v)
      }
    }
    if (current) rows.push(current)
    return rows.map((r) => validateBulkRow(r))
  }

  // CSV parse.
  const lines = trimmed.split("\n").filter((l) => l.trim() !== "")
  const header = lines[0].toLowerCase().split(",").map((h) => h.trim())
  const dataLines = header.includes("hostname") ? lines.slice(1) : lines

  return dataLines.map((line) => {
    const cells = line.split(",").map((c) => c.trim())
    const row: BulkRow = { hostname: "", mac: "" }
    if (header.includes("hostname")) {
      row.hostname = cells[header.indexOf("hostname")] ?? ""
      row.mac = cells[header.indexOf("mac")] ?? ""
      row.ip = cells[header.indexOf("ip")] ?? ""
      row.role = cells[header.indexOf("role")] ?? ""
    } else {
      // Positional: hostname, mac, ip, role
      row.hostname = cells[0] ?? ""
      row.mac = cells[1] ?? ""
      row.ip = cells[2] ?? ""
      row.role = cells[3] ?? ""
    }
    return validateBulkRow(row)
  })
}

function assignField(row: BulkRow, key: string, value: string) {
  switch (key) {
    case "hostname": row.hostname = value; break
    case "mac": case "primary_mac": row.mac = value; break
    case "ip": case "ip_address": row.ip = value; break
    case "role": case "roles": case "tags": row.role = value; break
    case "base_image_id": row.base_image_id = value; break
  }
}

function validateBulkRow(row: BulkRow): BulkRow {
  const errs: string[] = []
  if (!row.hostname) errs.push("hostname required")
  else if (!/^[a-z0-9-]{1,63}$/.test(row.hostname)) errs.push("invalid hostname")
  const normMac = row.mac.toLowerCase().replace(/[^0-9a-f]/g, "").replace(/(.{2})(?=.)/g, "$1:")
  if (!row.mac) errs.push("mac required")
  else if (!/^([0-9a-f]{2}:){5}[0-9a-f]{2}$/.test(normMac)) errs.push("invalid MAC")
  if (errs.length > 0) return { ...row, parseError: errs.join("; ") }
  return { ...row, mac: normMac }
}

function BulkAddNodes({ onSuccess, readyImages }: { onSuccess: () => void; readyImages: Array<{ id: string; name: string; version: string }> }) {
  const qc = useQueryClient()
  const [raw, setRaw] = React.useState("")
  const [rows, setRows] = React.useState<BulkRow[]>([])
  const [results, setResults] = React.useState<BatchResult[]>([])
  const [submitted, setSubmitted] = React.useState(false)
  const [loading, setLoading] = React.useState(false)

  function handlePreview() {
    setResults([])
    setSubmitted(false)
    setRows(parseBulkInput(raw))
  }

  async function handleSubmit() {
    const valid = rows.filter((r) => !r.parseError)
    if (valid.length === 0) return
    setLoading(true)
    try {
      const resp = await apiFetch<{ results: BatchResult[] }>("/api/v1/nodes/batch", {
        method: "POST",
        body: JSON.stringify({
          nodes: valid.map((r) => ({
            hostname: r.hostname,
            primary_mac: r.mac,
            tags: r.role ? r.role.split(/[,\s]+/).filter(Boolean) : [],
            base_image_id: r.base_image_id || readyImages[0]?.id || "",
            ...(r.ip ? { interfaces: [{ name: "eth0", mac_address: r.mac, ip_address: r.ip }] } : {}),
          })),
        }),
      })
      setResults(resp.results)
      setSubmitted(true)
      qc.invalidateQueries({ queryKey: ["nodes"] })
      const created = resp.results.filter((r) => r.status === "created").length
      toast({ title: `${created} of ${valid.length} nodes created` })
      if (created === valid.length) setTimeout(onSuccess, 1200)
    } catch (err) {
      toast({ variant: "destructive", title: "Batch failed", description: String(err) })
    } finally {
      setLoading(false)
    }
  }

  const hasErrors = rows.some((r) => r.parseError)
  const validRows = rows.filter((r) => !r.parseError)

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <label className="text-sm text-muted-foreground">Paste CSV or YAML</label>
        <textarea
          className="w-full font-mono text-xs border border-border bg-background rounded-md px-3 py-2 resize-none"
          rows={7}
          placeholder={`hostname,mac,ip,role\ncompute-01,bc:24:11:aa:bb:cc,,worker\ncompute-02,bc:24:11:aa:bb:dd,,worker`}
          value={raw}
          onChange={(e) => { setRaw(e.target.value); setRows([]); setResults([]) }}
        />
        <p className="text-xs text-muted-foreground">
          CSV header: <code className="font-mono">hostname,mac,ip,role</code> — or YAML list with the same keys.
        </p>
      </div>

      {rows.length === 0 && (
        <Button variant="outline" size="sm" className="w-full" onClick={handlePreview} disabled={!raw.trim()}>
          Preview
        </Button>
      )}

      {rows.length > 0 && (
        <div className="space-y-2">
          <p className="text-xs text-muted-foreground">{rows.length} rows parsed ({validRows.length} valid{hasErrors ? `, ${rows.length - validRows.length} errors` : ""})</p>
          <div className="rounded-md border border-border overflow-auto max-h-52">
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b border-border bg-secondary/40">
                  <th className="text-left p-2">Hostname</th>
                  <th className="text-left p-2">MAC</th>
                  <th className="text-left p-2">Role</th>
                  <th className="text-left p-2">Status</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row, i) => {
                  const result = results[validRows.indexOf(row)]
                  const hasParseErr = !!row.parseError
                  return (
                    <tr key={i} className={cn("border-b border-border", hasParseErr && "bg-destructive/5")}>
                      <td className="p-2 font-mono">{row.hostname || "—"}</td>
                      <td className="p-2 font-mono">{row.mac || "—"}</td>
                      <td className="p-2">{row.role || "—"}</td>
                      <td className="p-2">
                        {hasParseErr ? (
                          <span className="text-destructive">{row.parseError}</span>
                        ) : result ? (
                          <span className={result.status === "created" ? "text-status-healthy" : "text-destructive"}>
                            {result.status}{result.error ? `: ${result.error}` : ""}
                          </span>
                        ) : (
                          <span className="text-muted-foreground">ready</span>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>

          {!submitted && (
            <div className="flex gap-2">
              <Button
                className="flex-1"
                size="sm"
                onClick={handleSubmit}
                disabled={loading || validRows.length === 0}
              >
                {loading ? "Creating…" : `Create ${validRows.length} node${validRows.length !== 1 ? "s" : ""}`}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => { setRows([]); setResults([]) }}>
                Reset
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function Field({ label, error, children }: { label: string; error?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <label className="text-sm text-muted-foreground">{label}</label>
      {children}
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  )
}

interface NodeSearch {
  q?: string
  status?: string
  sort?: string
  dir?: "asc" | "desc"
  openNode?: string
  reimage?: string
  addNode?: string
  // NODE-DEL-4: open node sheet with delete confirm pre-expanded (Cmd-K "Delete node…")
  deleteNode?: string
  // TAG-4: one or more key:value tag filters (AND semantics, repeated ?tag= param)
  tag?: string[]
  // GRP-2: toggle between "nodes" and "groups" view (tab on /nodes)
  view?: "nodes" | "groups"
  // GRP-5: open create group sheet from Cmd-K
  createGroup?: string
}

export function NodesPage() {
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as NodeSearch

  // URL-driven state
  const q = search.q ?? ""
  const sortCol = search.sort ?? ""
  const sortDir = search.dir ?? "asc"
  // TAG-4: active tag filters from URL (normalised to string[])
  const activeTags: string[] = search.tag ?? []
  // GRP-2: current view tab (nodes | groups)
  const view = search.view ?? "nodes"
  const [advanced, setAdvanced] = React.useState(false)
  const [selectedNode, setSelectedNode] = React.useState<NodeConfig | null>(null)
  const [addNodeOpen, setAddNodeOpen] = React.useState(false)
  // GRP-5: open create group sheet from URL param (Cmd-K)
  const [createGroupOpen, setCreateGroupOpen] = React.useState(false)
  React.useEffect(() => {
    if (search.createGroup === "1") {
      setCreateGroupOpen(true)
      navigate({
        to: "/nodes",
        search: { q: q || undefined, status: search.status, sort: sortCol || undefined, dir: sortDir === "asc" ? undefined : "desc", view: view === "nodes" ? undefined : view, tag: activeTags.length ? activeTags : undefined, openNode: undefined, reimage: undefined, addNode: undefined, deleteNode: undefined, createGroup: undefined },
        replace: true,
      })
    }
  }, [search.createGroup]) // eslint-disable-line react-hooks/exhaustive-deps
  // NODE-CREATE-5: auto-open AddNode sheet from URL param (used by Cmd-K "Add node…").
  React.useEffect(() => {
    if (search.addNode === "1") {
      setAddNodeOpen(true)
      navigate({
        to: "/nodes",
        search: { q: q || undefined, status: search.status, sort: sortCol || undefined, dir: sortDir === "asc" ? undefined : "desc", tag: activeTags.length ? activeTags : undefined, openNode: undefined, reimage: undefined, addNode: undefined, deleteNode: undefined, view: undefined, createGroup: undefined },
        replace: true,
      })
    }
  }, [search.addNode]) // eslint-disable-line react-hooks/exhaustive-deps
  // PAL-2-2: auto-open node from URL param (used by Cmd-K "Reimage node…").
  const openNodeId = search.openNode
  const autoReimage = search.reimage === "1"
  // NODE-DEL-4: auto-open node sheet with delete confirm pre-expanded (Cmd-K "Delete node…").
  const autoDelete = search.deleteNode === "1"

  function updateSearch(patch: Partial<NodeSearch>) {
    navigate({
      to: "/nodes",
      search: {
        q: patch.q !== undefined ? patch.q : q || undefined,
        status: patch.status !== undefined ? patch.status : search.status,
        sort: patch.sort !== undefined ? patch.sort : sortCol || undefined,
        dir: patch.dir !== undefined ? patch.dir : sortDir === "asc" ? undefined : "desc",
        tag: patch.tag !== undefined ? (patch.tag?.length ? patch.tag : undefined) : (activeTags.length ? activeTags : undefined),
        view: patch.view !== undefined ? (patch.view === "nodes" ? undefined : patch.view) : (view === "nodes" ? undefined : view),
        openNode: undefined,
        reimage: undefined,
        addNode: undefined,
        deleteNode: undefined,
        createGroup: undefined,
      },
      replace: true,
    })
  }

  // TanStack Query for nodes
  const { data, isLoading, isError } = useQuery<ListNodesResponse>({
    queryKey: ["nodes", q, sortCol, sortDir, activeTags],
    queryFn: () => {
      const params = new URLSearchParams()
      if (q) params.set("search", q)
      if (sortCol) params.set("sort", sortCol)
      if (sortDir) params.set("dir", sortDir)
      // TAG-2: pass each active tag as a repeated ?tag= param (AND semantics on server)
      for (const t of activeTags) {
        params.append("tag", t)
      }
      return apiFetch<ListNodesResponse>(`/api/v1/nodes?${params}`)
    },
    refetchInterval: 30000,
    staleTime: 10000,
  })

  // IMG-COL-1: base images for name lookup in the Nodes table Image column.
  const { data: imagesListData } = useQuery<ListImagesResponse>({
    queryKey: ["images", "base"],
    queryFn: () => apiFetch<ListImagesResponse>("/api/v1/images?kind=base"),
    staleTime: 60000,
  })

  // UX-4: invalidate the nodes query whenever a "nodes" event arrives on the
  // multiplexed /api/v1/events stream. Replaces the per-page raw EventSource.
  useEventInvalidation("nodes", ["nodes"])

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
        search: { q: q || undefined, status: search.status, sort: sortCol || undefined, dir: sortDir === "asc" ? undefined : "desc", tag: activeTags.length ? activeTags : undefined, openNode: undefined, reimage: undefined, addNode: undefined, deleteNode: undefined, view: undefined, createGroup: undefined },
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

  // TAG-4: collect unique tags from all loaded nodes for autocomplete suggestions.
  const allObservedTags = React.useMemo(() => {
    const set = new Set<string>()
    for (const node of data?.nodes ?? []) {
      for (const t of node.tags ?? []) {
        if (!activeTags.includes(t)) set.add(t)
      }
    }
    return Array.from(set).sort()
  }, [data?.nodes, activeTags])

  const [tagPickerOpen, setTagPickerOpen] = React.useState(false)
  const [tagInput, setTagInput] = React.useState("")
  const tagPickerRef = React.useRef<HTMLDivElement>(null)

  // Close tag picker on outside click.
  React.useEffect(() => {
    if (!tagPickerOpen) return
    function handleClick(e: MouseEvent) {
      if (tagPickerRef.current && !tagPickerRef.current.contains(e.target as Node)) {
        setTagPickerOpen(false)
        setTagInput("")
      }
    }
    document.addEventListener("mousedown", handleClick)
    return () => document.removeEventListener("mousedown", handleClick)
  }, [tagPickerOpen])

  const filteredTagSuggestions = tagInput
    ? allObservedTags.filter((t) => t.toLowerCase().includes(tagInput.toLowerCase()))
    : allObservedTags

  function addTagFilter(tag: string) {
    const trimmed = tag.trim()
    if (!trimmed || activeTags.includes(trimmed)) return
    updateSearch({ tag: [...activeTags, trimmed] })
    setTagPickerOpen(false)
    setTagInput("")
  }

  function removeTagFilter(tag: string) {
    updateSearch({ tag: activeTags.filter((t) => t !== tag) })
  }

  return (
    <div className="flex flex-col h-full">
      {/* Toolbar */}
      <div className="flex items-center justify-between gap-3 border-b border-border px-6 py-3">
        <div className="flex items-center gap-2 flex-1 min-w-0">
          {/* GRP-2: view toggle — Nodes / Groups tabs */}
          <Tabs value={view} onValueChange={(v) => updateSearch({ view: v as "nodes" | "groups" })} className="shrink-0">
            <TabsList className="h-8">
              <TabsTrigger value="nodes" className="text-xs px-3 h-6">
                <Cpu className="h-3.5 w-3.5 mr-1" />
                Nodes
              </TabsTrigger>
              <TabsTrigger value="groups" className="text-xs px-3 h-6">
                <Users className="h-3.5 w-3.5 mr-1" />
                Groups
              </TabsTrigger>
            </TabsList>
          </Tabs>

          {view === "nodes" && (
          <div className="relative w-72 shrink-0">
            <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
            <Input
              className="pl-8"
              placeholder="Search nodes..."
              value={q}
              onChange={(e) => updateSearch({ q: e.target.value || undefined })}
            />
          </div>
          )}
          {/* TAG-4: active tag filter chips */}
          {activeTags.map((tag) => (
            <span
              key={tag}
              className="inline-flex items-center gap-1 rounded-full border border-border bg-secondary px-2.5 py-0.5 text-xs font-mono text-foreground whitespace-nowrap"
            >
              <Tag className="h-3 w-3 text-muted-foreground" />
              {tag}
              <button
                onClick={() => removeTagFilter(tag)}
                className="ml-0.5 rounded-full hover:bg-muted p-0.5"
                aria-label={`Remove tag filter ${tag}`}
              >
                <X className="h-2.5 w-2.5" />
              </button>
            </span>
          ))}
          {/* TAG-4: tag picker — only shown in nodes view */}
          {view === "nodes" && (
          <div className="relative" ref={tagPickerRef}>
            <Button
              variant="outline"
              size="sm"
              className="h-8 gap-1 text-xs"
              onClick={() => setTagPickerOpen((o) => !o)}
            >
              <Tag className="h-3.5 w-3.5" />
              Filter by tag
            </Button>
            {tagPickerOpen && (
              <div className="absolute left-0 top-full mt-1 z-50 w-56 rounded-md border border-border bg-popover shadow-md">
                <div className="p-2">
                  <Input
                    autoFocus
                    className="h-7 text-xs"
                    placeholder="key:value or free text"
                    value={tagInput}
                    onChange={(e) => setTagInput(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && tagInput.trim()) addTagFilter(tagInput)
                      if (e.key === "Escape") { setTagPickerOpen(false); setTagInput("") }
                    }}
                  />
                </div>
                {filteredTagSuggestions.length > 0 && (
                  <div className="max-h-48 overflow-y-auto border-t border-border">
                    {filteredTagSuggestions.map((tag) => (
                      <button
                        key={tag}
                        className="flex w-full items-center gap-2 px-3 py-1.5 text-xs font-mono hover:bg-accent text-left"
                        onClick={() => addTagFilter(tag)}
                      >
                        <Tag className="h-3 w-3 shrink-0 text-muted-foreground" />
                        {tag}
                      </button>
                    ))}
                  </div>
                )}
                {filteredTagSuggestions.length === 0 && tagInput && (
                  <div className="px-3 py-2 text-xs text-muted-foreground border-t border-border">
                    Press Enter to filter by "{tagInput}"
                  </div>
                )}
              </div>
            )}
          </div>
          )}
        </div>
        <div className="flex items-center gap-2">
          {view === "nodes" ? (
            <>
              <Button
                size="sm"
                onClick={() => setAddNodeOpen(true)}
              >
                <Plus className="h-4 w-4 mr-1" />
                Add Node
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => setAdvanced((a) => !a)}
                className={cn(advanced && "bg-secondary")}
              >
                {advanced ? "Basic view" : "Advanced"}
              </Button>
            </>
          ) : (
            <Button
              size="sm"
              onClick={() => setCreateGroupOpen(true)}
            >
              <Plus className="h-4 w-4 mr-1" />
              New Group
            </Button>
          )}
        </div>
      </div>

      {/* Main content — Nodes table or Groups panel */}
      <div className="flex-1 overflow-auto">
        {view === "groups" ? (
          <GroupsPanel
            createOpen={createGroupOpen}
            onCreateClose={() => setCreateGroupOpen(false)}
          />
        ) : isLoading ? (
          <NodesSkeleton />
        ) : isError ? (
          <div className="flex items-center justify-center h-40">
            <p className="text-sm text-destructive">Failed to load nodes. Reload to retry.</p>
          </div>
        ) : nodes.length === 0 ? (
          <EmptyState onAddNode={() => setAddNodeOpen(true)} />
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
                {/* PWR-LIST-1: power column */}
                <TableHead scope="col" aria-label="Power actions">Power</TableHead>
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
                  <TableCell className="text-xs">
                    {(() => {
                      if (!node.base_image_id) return <span className="text-muted-foreground">—</span>
                      const img = imagesListData?.images?.find((i) => i.id === node.base_image_id)
                      return img ? (
                        <TooltipProvider>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span className="cursor-default">{img.name} {img.version}</span>
                            </TooltipTrigger>
                            <TooltipContent>
                              <span className="font-mono text-xs">{node.base_image_id.slice(0, 8)}</span>
                            </TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      ) : (
                        <span className="font-mono text-muted-foreground">{node.base_image_id.slice(0, 8)}</span>
                      )
                    })()}
                  </TableCell>
                  {/* PWR-LIST-1..2: per-row power action icons */}
                  <TableCell onClick={(e) => e.stopPropagation()}>
                    <PowerButtons nodeId={node.id} />
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
          autoDelete={autoDelete}
        />
      )}

      {/* Add Node sheet (NODE-CREATE-2) */}
      <AddNodeSheet open={addNodeOpen} onClose={() => setAddNodeOpen(false)} />
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

// BULK-5: CSV sample shown alongside CLI snippet in empty state.
const CSV_SAMPLE = `hostname,mac,ip,role
compute-01,bc:24:11:aa:bb:cc,,worker
compute-02,bc:24:11:aa:bb:dd,,worker`

function EmptyState({ onAddNode }: { onAddNode: () => void }) {
  const [copiedCli, setCopiedCli] = React.useState(false)
  const [copiedCsv, setCopiedCsv] = React.useState(false)
  const snippet = `clustr --server http://<server>:8080 deploy --auto`

  function copyCli() {
    navigator.clipboard.writeText(snippet).then(() => {
      setCopiedCli(true)
      setTimeout(() => setCopiedCli(false), 2000)
    })
  }

  function copyCsv() {
    navigator.clipboard.writeText(CSV_SAMPLE).then(() => {
      setCopiedCsv(true)
      setTimeout(() => setCopiedCsv(false), 2000)
    })
  }

  return (
    <div className="flex flex-col items-center justify-center h-full min-h-64 gap-6 p-8 text-center">
      <div className="space-y-1">
        <h2 className="text-base font-semibold">No nodes registered yet</h2>
        <p className="text-sm text-muted-foreground">
          PXE-boot a node to register it automatically, add one here, or use one of the options below.
        </p>
      </div>
      {/* CLI snippet */}
      <div className="w-full max-w-lg space-y-1 text-left">
        <p className="text-xs text-muted-foreground font-medium uppercase tracking-wide">CLI — auto-register on boot</p>
        <div className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2">
          <code className="text-xs font-mono flex-1">{snippet}</code>
          <Button variant="ghost" size="icon" className="h-7 w-7 shrink-0" onClick={copyCli}>
            {copiedCli ? <Check className="h-3.5 w-3.5 text-status-healthy" /> : <Copy className="h-3.5 w-3.5" />}
          </Button>
        </div>
      </div>
      {/* CSV sample — BULK-5 */}
      <div className="w-full max-w-lg space-y-1 text-left">
        <p className="text-xs text-muted-foreground font-medium uppercase tracking-wide">Bulk import — paste CSV into Add Node › Bulk tab</p>
        <div className="flex items-start gap-2 rounded-md border border-border bg-card px-3 py-2">
          <pre className="text-xs font-mono flex-1 whitespace-pre text-left leading-relaxed">{CSV_SAMPLE}</pre>
          <Button variant="ghost" size="icon" className="h-7 w-7 shrink-0 mt-0.5" onClick={copyCsv}>
            {copiedCsv ? <Check className="h-3.5 w-3.5 text-status-healthy" /> : <Copy className="h-3.5 w-3.5" />}
          </Button>
        </div>
      </div>
      <Button onClick={onAddNode} size="sm">
        <Plus className="h-4 w-4 mr-1" />
        Add Node
      </Button>
    </div>
  )
}

interface NodeSheetProps {
  node: NodeConfig
  onClose: () => void
  advanced: boolean
  relativeTime: (iso?: string) => string
  autoReimage?: boolean
  autoDelete?: boolean
}

// ─── NodeSheet ────────────────────────────────────────────────────────────────
// Sprint 4: EDIT-NODE-2/3 (inline edit mode) + TAG-3/5 (tag management)

type NodeDetailTab = "overview" | "sensors" | "eventlog" | "console" | "deploylog"

function NodeSheet({ node, onClose, advanced, relativeTime, autoReimage, autoDelete }: NodeSheetProps) {
  const qc = useQueryClient()
  const state = nodeState(node)
  const [editing, setEditing] = React.useState(false)
  const [editHostname, setEditHostname] = React.useState(node.hostname)
  const [editFqdn, setEditFqdn] = React.useState(node.fqdn || "")
  const [editTags, setEditTags] = React.useState<string[]>(node.tags ?? [])
  const [editProvider, setEditProvider] = React.useState(node.provider ?? "")
  const [editRoleConfirm, setEditRoleConfirm] = React.useState("")
  const [editError, setEditError] = React.useState("")
  // TAG-3/5: inline tag add
  const [tagInput, setTagInput] = React.useState("")
  // #152: per-node detail tabs
  const [detailTab, setDetailTab] = React.useState<NodeDetailTab>("overview")

  const isController = node.tags?.includes("controller")
  const editRemovesController = isController && !editTags.includes("controller")

  const editMutation = useMutation({
    mutationFn: () =>
      apiFetch<NodeConfig>(`/api/v1/nodes/${node.id}`, {
        method: "PATCH",
        body: JSON.stringify({
          hostname: editHostname || undefined,
          fqdn: editFqdn || undefined,
          tags: editTags,
          provider: editProvider,
        }),
      }),
    onSuccess: (updated) => {
      qc.setQueryData<{ nodes: NodeConfig[] }>(["nodes"], (old) => {
        if (!old) return old
        return { ...old, nodes: old.nodes.map((n) => n.id === updated.id ? updated : n) }
      })
      qc.invalidateQueries({ queryKey: ["nodes"] })
      toast({ title: "Node updated", description: `${updated.hostname} saved.` })
      setEditing(false)
      setEditError("")
      setEditRoleConfirm("")
    },
    onError: (err) => {
      setEditError(String(err))
    },
  })

  function handleSave() {
    if (editRemovesController && editRoleConfirm !== node.hostname) {
      setEditError(`Type the node hostname "${node.hostname}" to confirm removing controller role`)
      return
    }
    setEditError("")
    editMutation.mutate()
  }

  function addTag(tag: string) {
    const trimmed = tag.trim()
    if (!trimmed || editTags.includes(trimmed)) return
    setEditTags((prev) => [...prev, trimmed])
    setTagInput("")
  }

  function removeTag(tag: string) {
    setEditTags((prev) => prev.filter((t) => t !== tag))
  }

  return (
    <Sheet open onOpenChange={(v) => !v && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-xl overflow-y-auto">
        <SheetHeader>
          <div className="flex items-center justify-between">
            <SheetTitle className="font-mono">{node.hostname || node.id}</SheetTitle>
            {!editing && (
              <Button variant="ghost" size="sm" onClick={() => { setEditing(true); setEditError("") }} className="h-7 px-2">
                <Pencil className="h-3.5 w-3.5 mr-1" />
                Edit
              </Button>
            )}
          </div>
          <SheetDescription>
            <StatusDot state={state} />
          </SheetDescription>
        </SheetHeader>

        <div className="mt-6 space-y-4">
          {editing ? (
            /* ── Inline edit form (EDIT-NODE-2) ── */
            <div className="space-y-4 rounded-md border border-border p-4">
              <h3 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Editing node</h3>
              <EditField label="Hostname">
                <Input value={editHostname} onChange={(e) => setEditHostname(e.target.value)} className="font-mono text-xs" />
              </EditField>
              <EditField label="FQDN (optional)">
                <Input value={editFqdn} onChange={(e) => setEditFqdn(e.target.value)} className="font-mono text-xs" />
              </EditField>
              {/* TAG-5: tag management in edit mode */}
              <EditField label="Tags">
                <div className="flex flex-wrap gap-1.5 mb-2">
                  {editTags.map((t) => (
                    <span key={t} className="flex items-center gap-0.5 rounded bg-secondary px-2 py-0.5 text-xs font-mono">
                      {t}
                      <button onClick={() => removeTag(t)} className="ml-0.5 hover:text-destructive" aria-label={`Remove tag ${t}`}>
                        <X className="h-3 w-3" />
                      </button>
                    </span>
                  ))}
                </div>
                <div className="flex gap-2">
                  <Input
                    placeholder="add tag…"
                    value={tagInput}
                    onChange={(e) => setTagInput(e.target.value)}
                    onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); addTag(tagInput) } }}
                    className="text-xs flex-1"
                  />
                  <Button type="button" variant="outline" size="sm" onClick={() => addTag(tagInput)}>
                    <Plus className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </EditField>
              <EditField label="Provider">
                <select
                  className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
                  value={editProvider}
                  onChange={(e) => setEditProvider(e.target.value)}
                >
                  {NODE_PROVIDERS.map((p) => (
                    <option key={p.value} value={p.value}>{p.label}</option>
                  ))}
                </select>
              </EditField>
              {/* EDIT-NODE-3: typed confirm for controller demotion */}
              {editRemovesController && (
                <EditField label={`Type "${node.hostname}" to confirm removing controller role:`}>
                  <Input
                    placeholder={node.hostname}
                    value={editRoleConfirm}
                    onChange={(e) => setEditRoleConfirm(e.target.value)}
                    className="font-mono text-xs border-status-warning"
                  />
                </EditField>
              )}
              {editError && <p className="text-xs text-destructive">{editError}</p>}
              <div className="flex gap-2 pt-1">
                <Button size="sm" className="flex-1" onClick={handleSave} disabled={editMutation.isPending}>
                  {editMutation.isPending ? "Saving…" : "Save"}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => { setEditing(false); setEditError(""); setEditHostname(node.hostname); setEditFqdn(node.fqdn || ""); setEditTags(node.tags ?? []); setEditProvider(node.provider ?? "") }}>
                  Cancel
                </Button>
              </div>
            </div>
          ) : (
            /* ── #152: tabbed detail view ── */
            <Tabs value={detailTab} onValueChange={(v) => setDetailTab(v as NodeDetailTab)}>
              <TabsList className="w-full">
                <TabsTrigger value="overview" className="flex-1 text-xs gap-1">
                  <Cpu className="h-3 w-3" />
                  Overview
                </TabsTrigger>
                <TabsTrigger value="sensors" className="flex-1 text-xs gap-1">
                  <Activity className="h-3 w-3" />
                  Sensors
                </TabsTrigger>
                <TabsTrigger value="eventlog" className="flex-1 text-xs gap-1">
                  <BookOpen className="h-3 w-3" />
                  Event Log
                </TabsTrigger>
                <TabsTrigger value="console" className="flex-1 text-xs gap-1">
                  <Terminal className="h-3 w-3" />
                  Console
                </TabsTrigger>
                <TabsTrigger value="deploylog" className="flex-1 text-xs gap-1">
                  <ScrollText className="h-3 w-3" />
                  Install Log
                </TabsTrigger>
              </TabsList>

              {/* ── Overview tab (existing content) ── */}
              <TabsContent value="overview" className="mt-4 space-y-4">
                <Section title="Identity">
                  <Row label="ID" value={node.id} mono />
                  <Row label="Hostname" value={node.hostname} />
                  <Row label="FQDN" value={node.fqdn || "—"} />
                  <Row label="MAC" value={node.primary_mac} mono />
                  <Row label="Firmware" value={node.detected_firmware || "—"} />
                  <Row label="Provider" value={NODE_PROVIDERS.find((p) => p.value === (node.provider ?? ""))?.label ?? "—"} />
                </Section>

                <Section title="Deployment">
                  <ImageAssignRow node={node} qc={qc} />
                  <Row label="State" value={state} />
                  <Row label="Last seen" value={relativeTime(node.last_seen_at ?? node.deploy_verified_booted_at)} />
                  <Row label="Deploy complete" value={relativeTime(node.deploy_completed_preboot_at)} />
                  <Row label="Verified boot" value={relativeTime(node.deploy_verified_booted_at)} />
                </Section>

                {node.ldap_ready !== undefined && (
                  <LdapStatusSection node={node} qc={qc} />
                )}

                {/* TAG-5: tag display + inline add in view mode */}
                <Section title="Tags">
                  <div className="flex flex-wrap gap-1.5">
                    {(node.tags ?? []).map((t) => (
                      <span key={t} className="rounded bg-secondary px-2 py-0.5 text-xs font-mono">
                        {t}
                      </span>
                    ))}
                    {(node.tags ?? []).length === 0 && (
                      <span className="text-xs text-muted-foreground">No tags</span>
                    )}
                  </div>
                  <div className="flex gap-2 mt-2">
                    <Input
                      placeholder="add tag…"
                      value={tagInput}
                      onChange={(e) => setTagInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter") {
                          e.preventDefault()
                          const trimmed = tagInput.trim()
                          if (!trimmed) return
                          const newTags = [...(node.tags ?? []).filter((t) => t !== trimmed), trimmed]
                          apiFetch<NodeConfig>(`/api/v1/nodes/${node.id}`, {
                            method: "PATCH",
                            body: JSON.stringify({ tags: newTags }),
                          }).then(() => {
                            qc.invalidateQueries({ queryKey: ["nodes"] })
                            setTagInput("")
                            toast({ title: "Tag added", description: trimmed })
                          }).catch((err) => toast({ variant: "destructive", title: "Failed to add tag", description: String(err) }))
                        }
                      }}
                      className="text-xs flex-1 h-7"
                    />
                    <Tag className="h-3.5 w-3.5 mt-1.5 text-muted-foreground" />
                  </div>
                </Section>

                {advanced && (
                  <Section title="Advanced">
                    <Row label="Group" value={node.group_id || "—"} mono />
                    <Row label="Created" value={relativeTime(node.created_at)} />
                    <Row label="Updated" value={relativeTime(node.updated_at)} />
                  </Section>
                )}

                <HardwareSection node={node} />
                <SudoersSection node={node} />
                <SlurmNodeSection node={node} />
                <ReimageFlow node={node} autoExpand={autoReimage} />
                <CaptureNodeFlow node={node} />
                <DeleteNodeFlow node={node} autoExpand={autoDelete} onDeleted={onClose} />
              </TabsContent>

              {/* ── Sensors tab ── */}
              <TabsContent value="sensors" className="mt-2">
                <SensorsTab nodeId={node.id} />
              </TabsContent>

              {/* ── Event Log tab ── */}
              <TabsContent value="eventlog" className="mt-2">
                <EventLogTab nodeId={node.id} />
              </TabsContent>

              {/* ── Console tab ── */}
              <TabsContent value="console" className="mt-2">
                <ConsoleTab nodeId={node.id} />
              </TabsContent>

              {/* ── Install Log tab (STREAM-LOG-UI) ── */}
              <TabsContent value="deploylog" className="mt-2">
                <DeployLogTab nodeId={node.id} primaryMac={node.primary_mac} />
              </TabsContent>
            </Tabs>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function EditField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <label className="text-xs text-muted-foreground">{label}</label>
      {children}
    </div>
  )
}

// ─── ImageAssignRow ───────────────────────────────────────────────────────────
// Shows the currently assigned base image (by name) and lets the operator
// assign or change it inline without triggering a reimage.
// Fires PATCH /api/v1/nodes/{id} { base_image_id } on save.

interface ImageAssignRowProps {
  node: NodeConfig
  qc: ReturnType<typeof useQueryClient>
}

function ImageAssignRow({ node, qc }: ImageAssignRowProps) {
  const [editing, setEditing] = React.useState(false)
  const [selectedId, setSelectedId] = React.useState(node.base_image_id || "")
  const [saving, setSaving] = React.useState(false)

  // ?kind=base excludes initramfs build artifacts from the assignment picker.
  const { data: imagesData } = useQuery<ListImagesResponse>({
    queryKey: ["images", "base"],
    queryFn: () => apiFetch<ListImagesResponse>("/api/v1/images?kind=base"),
    staleTime: 60000,
  })

  const readyImages = imagesData?.images?.filter((img) => img.status === "ready") ?? []

  // Resolve current image name from the cached list.
  const currentImage = imagesData?.images?.find((img) => img.id === node.base_image_id)
  const displayName = currentImage
    ? `${currentImage.name} ${currentImage.version}`
    : node.base_image_id
      ? node.base_image_id.slice(0, 12) + "…"
      : "—"

  function handleOpen() {
    setSelectedId(node.base_image_id || "")
    setEditing(true)
  }

  function handleCancel() {
    setEditing(false)
    setSelectedId(node.base_image_id || "")
  }

  async function handleSave() {
    setSaving(true)
    try {
      await apiFetch<NodeConfig>(`/api/v1/nodes/${node.id}`, {
        method: "PATCH",
        body: JSON.stringify({ base_image_id: selectedId || null }),
      })
      qc.invalidateQueries({ queryKey: ["nodes"] })
      toast({ title: "Image assigned", description: selectedId ? `Image set to ${selectedId.slice(0, 8)}…` : "Image cleared." })
      setEditing(false)
    } catch (err) {
      toast({ variant: "destructive", title: "Failed to assign image", description: String(err) })
    } finally {
      setSaving(false)
    }
  }

  if (!editing) {
    return (
      <div className="flex items-center justify-between py-0.5">
        <span className="text-xs text-muted-foreground">Image</span>
        <div className="flex items-center gap-1.5">
          <span className="text-xs font-mono truncate max-w-[160px]" title={node.base_image_id || undefined}>
            {displayName}
          </span>
          <button
            onClick={handleOpen}
            className="text-muted-foreground hover:text-foreground transition-colors"
            aria-label="Change assigned image"
          >
            <Pencil className="h-3 w-3" />
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-2 py-1">
      <label className="text-xs text-muted-foreground">Image</label>
      <select
        className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
        value={selectedId}
        onChange={(e) => setSelectedId(e.target.value)}
        disabled={saving}
        autoFocus
      >
        <option value="">No image assigned</option>
        {readyImages.map((img) => (
          <option key={img.id} value={img.id}>
            {img.name} {img.version} ({img.id.slice(0, 8)})
          </option>
        ))}
      </select>
      <div className="flex gap-1.5">
        <Button size="sm" variant="outline" className="flex-1 h-7 text-xs" onClick={handleSave} disabled={saving}>
          {saving ? <Loader2 className="h-3 w-3 animate-spin" /> : "Save"}
        </Button>
        <Button size="sm" variant="ghost" className="h-7 text-xs" onClick={handleCancel} disabled={saving}>
          Cancel
        </Button>
      </div>
    </div>
  )
}

// ─── Reimage inline flow (REIMG-1..6) ────────────────────────────────────────

function ReimageFlow({ node, autoExpand }: { node: NodeConfig; autoExpand?: boolean }) {
  const qc = useQueryClient()
  const [expanded, setExpanded] = React.useState(autoExpand ?? false)
  const [selectedImageId, setSelectedImageId] = React.useState(node.base_image_id || "")
  const [confirmId, setConfirmId] = React.useState("")

  // Fetch available base images for selector and button label resolution.
  // Always enabled (not just when expanded) so the button can show the image name.
  // ?kind=base excludes initramfs build artifacts from the reimage picker.
  const { data: imagesData, isLoading: imagesLoading } = useQuery<ListImagesResponse>({
    queryKey: ["images", "base"],
    queryFn: () => apiFetch<ListImagesResponse>("/api/v1/images?kind=base"),
    staleTime: 60000,
  })

  // When images load, default selection to the currently assigned image if
  // the operator hasn't changed it yet.
  React.useEffect(() => {
    if (imagesData && selectedImageId === "" && node.base_image_id) {
      setSelectedImageId(node.base_image_id)
    }
  }, [imagesData, node.base_image_id, selectedImageId])

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
  const allImages = imagesData?.images ?? []
  const canConfirm = confirmId === node.id && selectedImageId !== ""

  // Defensive UI gate: even if a reimage_request row is stuck non-terminal in the
  // DB, suppress the "Reimage in progress" badge once the node has finished its
  // current provisioning cycle. The server clears reimage_pending the moment
  // deploy-complete fires, so reimage_pending is the canonical "is a reimage
  // happening RIGHT NOW" signal — a non-terminal reimage row without a pending
  // node flag is an orphan from a prior cycle and must not render as in-flight.
  // (fix/v0.1.14-ui-stale-reimage)
  const isProvisioning =
    node.reimage_pending &&
    activeReimage &&
    ["pending", "triggered", "in_progress"].includes(activeReimage.status)

  // Resolve human-readable names for the current → target diff display.
  const currentImageName = allImages.find((img) => img.id === node.base_image_id)
    ? `${allImages.find((img) => img.id === node.base_image_id)!.name} ${allImages.find((img) => img.id === node.base_image_id)!.version}`
    : node.base_image_id
      ? node.base_image_id.slice(0, 12)
      : "no image"
  const targetImageName = allImages.find((img) => img.id === selectedImageId)
    ? `${allImages.find((img) => img.id === selectedImageId)!.name} ${allImages.find((img) => img.id === selectedImageId)!.version}`
    : selectedImageId
      ? selectedImageId.slice(0, 12)
      : "(select target)"

  // Label for the collapsed button: show image name when one is assigned.
  const reimageButtonLabel = node.base_image_id && allImages.length > 0
    ? `Reimage with ${currentImageName}`
    : "Reimage node"

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
          {reimageButtonLabel}
        </Button>
      ) : (
        <div className="rounded-md border border-status-warning/30 bg-status-warning/5 p-4 space-y-3">
          <div className="flex items-center gap-2 text-sm font-medium text-status-warning">
            <AlertTriangle className="h-4 w-4 shrink-0" />
            Reimage node — this will reinstall the OS
          </div>

          {/* Current → target diff */}
          <div className="text-xs text-muted-foreground flex items-center gap-2">
            <span className="font-mono truncate">{currentImageName}</span>
            <span className="shrink-0">→</span>
            <span className="font-mono truncate">{targetImageName}</span>
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
              onClick={() => { setExpanded(false); setConfirmId(""); setSelectedImageId(node.base_image_id || "") }}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

// ─── SlurmNodeSection (NODE-SLURM-1..3) ──────────────────────────────────────
// Per-node Slurm subsection in the node detail Sheet.
// Shows: current Slurm role(s), sync status, override count.
// "Set role" picker + "Edit overrides" expand inline.

function SlurmNodeSection({ node }: { node: NodeConfig }) {
  const qc = useQueryClient()
  const [expanded, setExpanded] = React.useState(false)
  const [editRoles, setEditRoles] = React.useState(false)
  const [selectedRoles, setSelectedRoles] = React.useState<string[]>([])
  const [editOverrides, setEditOverrides] = React.useState(false)
  const [overrideText, setOverrideText] = React.useState("")

  const allRoles = ["controller", "worker", "dbd", "login"]

  const { data: roleData } = useQuery<SlurmNodeRole>({
    queryKey: ["slurm-node-role", node.id],
    queryFn: () => apiFetch<SlurmNodeRole>(`/api/v1/nodes/${node.id}/slurm/role`),
    enabled: expanded,
    staleTime: 15000,
  })

  const { data: syncData } = useQuery<SlurmNodeSyncStatus>({
    queryKey: ["slurm-node-sync", node.id],
    queryFn: () => apiFetch<SlurmNodeSyncStatus>(`/api/v1/nodes/${node.id}/slurm/sync-status`),
    enabled: expanded,
    staleTime: 20000,
  })

  const { data: overridesData } = useQuery<SlurmNodeOverride>({
    queryKey: ["slurm-node-overrides", node.id],
    queryFn: () => apiFetch<SlurmNodeOverride>(`/api/v1/nodes/${node.id}/slurm/overrides`),
    enabled: expanded,
    staleTime: 20000,
  })

  const setRoleMut = useMutation({
    mutationFn: () => apiFetch(`/api/v1/nodes/${node.id}/slurm/role`, {
      method: "PUT",
      body: JSON.stringify({ roles: selectedRoles }),
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["slurm-node-role", node.id] })
      qc.invalidateQueries({ queryKey: ["slurm-role-summary"] })
      qc.invalidateQueries({ queryKey: ["slurm-nodes"] })
      setEditRoles(false)
      toast({ title: "Slurm role updated" })
    },
    onError: (e: Error) => toast({ title: "Failed", description: e.message, variant: "destructive" }),
  })

  const saveOverridesMut = useMutation({
    mutationFn: () => {
      let params: Record<string, string> = {}
      try {
        params = JSON.parse(overrideText)
      } catch {
        throw new Error("Invalid JSON — overrides must be a JSON object")
      }
      return apiFetch(`/api/v1/nodes/${node.id}/slurm/overrides`, {
        method: "PUT",
        body: JSON.stringify({ params }),
      })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["slurm-node-overrides", node.id] })
      setEditOverrides(false)
      toast({ title: "Overrides saved" })
    },
    onError: (e: Error) => toast({ title: "Failed", description: e.message, variant: "destructive" }),
  })

  const roles = roleData?.roles ?? []
  const syncState = syncData?.state ?? []
  const overrideCount = Object.keys(overridesData?.params ?? {}).length

  return (
    <div className="rounded-md border border-border">
      <button
        className="flex w-full items-center justify-between px-3 py-2.5 text-left hover:bg-secondary/40 transition-colors"
        onClick={() => setExpanded((v) => !v)}
      >
        <span className="text-sm font-medium">Slurm</span>
        <div className="flex items-center gap-2">
          {roles.length > 0 && <span className="text-xs text-muted-foreground">{roles.join(", ")}</span>}
          {expanded ? <ChevronDown className="h-4 w-4 text-muted-foreground" /> : <ChevronRight className="h-4 w-4 text-muted-foreground" />}
        </div>
      </button>

      {expanded && (
        <div className="border-t border-border px-3 py-3 space-y-3">
          {/* Roles */}
          <div>
            <div className="flex items-center justify-between mb-1">
              <span className="text-xs text-muted-foreground">Roles</span>
              <Button
                size="sm"
                variant="ghost"
                className="h-6 text-xs"
                onClick={() => { setEditRoles((v) => !v); setSelectedRoles(roles) }}
              >
                {editRoles ? "Cancel" : "Set role"}
              </Button>
            </div>
            {editRoles ? (
              <div className="space-y-2">
                <div className="flex flex-wrap gap-1">
                  {allRoles.map((r) => (
                    <button
                      key={r}
                      onClick={() => setSelectedRoles((prev) => prev.includes(r) ? prev.filter((x) => x !== r) : [...prev, r])}
                      className={cn("rounded border px-2 py-0.5 text-xs capitalize transition-colors",
                        selectedRoles.includes(r)
                          ? "border-primary bg-primary/10 text-primary"
                          : "border-border text-muted-foreground hover:border-primary/50")}
                    >
                      {r}
                    </button>
                  ))}
                </div>
                <Button size="sm" className="h-7 text-xs" onClick={() => setRoleMut.mutate()} disabled={setRoleMut.isPending}>
                  {setRoleMut.isPending ? <Loader2 className="h-3 w-3 animate-spin mr-1" /> : null}Save
                </Button>
              </div>
            ) : (
              <div className="flex flex-wrap gap-1">
                {roles.length === 0 ? (
                  <span className="text-xs text-muted-foreground">No roles assigned</span>
                ) : roles.map((r) => (
                  <span key={r} className="rounded border border-border px-2 py-0.5 text-xs capitalize">{r}</span>
                ))}
              </div>
            )}
          </div>

          {/* Sync status */}
          <div>
            <span className="text-xs text-muted-foreground">Config sync</span>
            <div className="mt-1">
              {syncState.length === 0 ? (
                <span className="text-xs text-muted-foreground">No sync state — push configs first</span>
              ) : (
                <div className="space-y-1">
                  {syncState.map((s) => (
                    <div key={s.filename} className="flex items-center gap-2 text-xs">
                      <span className={cn("h-1.5 w-1.5 rounded-full shrink-0", s.deployed_version > 0 ? "bg-status-healthy" : "bg-status-neutral")} />
                      <span className="font-mono flex-1">{s.filename}</span>
                      <span className="text-muted-foreground">v{s.deployed_version}</span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>

          {/* Overrides */}
          <div>
            <div className="flex items-center justify-between mb-1">
              <span className="text-xs text-muted-foreground">Overrides ({overrideCount})</span>
              <Button
                size="sm"
                variant="ghost"
                className="h-6 text-xs"
                onClick={() => {
                  setEditOverrides((v) => !v)
                  if (!editOverrides) {
                    setOverrideText(JSON.stringify(overridesData?.params ?? {}, null, 2))
                  }
                }}
              >
                {editOverrides ? "Cancel" : "Edit overrides"}
              </Button>
            </div>
            {editOverrides && (
              <div className="space-y-2">
                <textarea
                  className="w-full min-h-24 rounded border border-border bg-background font-mono text-xs p-2 resize-none focus:outline-none focus:ring-1 focus:ring-ring"
                  value={overrideText}
                  onChange={(e) => setOverrideText(e.target.value)}
                  placeholder='{"NodeName": "node01", "CPUs": "4"}'
                  style={{ fontFamily: "'JetBrains Mono', monospace" }}
                />
                <Button size="sm" className="h-7 text-xs" onClick={() => saveOverridesMut.mutate()} disabled={saveOverridesMut.isPending}>
                  {saveOverridesMut.isPending ? <Loader2 className="h-3 w-3 animate-spin mr-1" /> : null}Save overrides
                </Button>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

// ─── DeleteNodeFlow (NODE-DEL-1..5) ──────────────────────────────────────────
// Inline destructive confirmation per UI/UX principle 4.

function DeleteNodeFlow({ node, autoExpand, onDeleted }: { node: NodeConfig; autoExpand?: boolean; onDeleted: () => void }) {
  const qc = useQueryClient()
  const state = nodeState(node)
  const isDeploying = state === "deploying" || state === "reimage_pending"
  const [expanded, setExpanded] = React.useState(autoExpand ?? false)
  const [confirmHostname, setConfirmHostname] = React.useState("")
  const [deleteError, setDeleteError] = React.useState("")

  const canDelete = confirmHostname === node.hostname

  const deleteMutation = useMutation({
    mutationFn: () =>
      apiFetch<void>(`/api/v1/nodes/${node.id}`, { method: "DELETE" }),
    onMutate: async () => {
      // Optimistic remove from list cache.
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
      onDeleted()
    },
    onError: (err, _vars, context) => {
      // Rollback optimistic remove.
      if (context?.prev) {
        qc.setQueryData(["nodes"], context.prev)
      }
      const msg = String(err)
      if (msg.includes("409") || msg.toLowerCase().includes("deploy")) {
        setDeleteError("Cannot delete: node is currently deploying. Cancel deployment first.")
      } else {
        setDeleteError(msg)
      }
    },
  })

  return (
    <div className="pt-4 border-t border-border space-y-3">
      {!expanded ? (
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="inline-block w-full">
                <Button
                  variant="ghost"
                  className="w-full text-destructive hover:text-destructive hover:bg-destructive/10"
                  onClick={() => { setExpanded(true); setDeleteError("") }}
                  disabled={isDeploying}
                >
                  <Trash2 className="h-4 w-4 mr-2" />
                  Delete node
                </Button>
              </span>
            </TooltipTrigger>
            {isDeploying && (
              <TooltipContent>Cancel active deployment to delete.</TooltipContent>
            )}
          </Tooltip>
        </TooltipProvider>
      ) : (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 p-4 space-y-3">
          <div className="flex items-center gap-2 text-sm font-medium text-destructive">
            <Trash2 className="h-4 w-4 shrink-0" />
            Delete node — this is permanent
          </div>

          <p className="text-xs text-muted-foreground">
            Type <code className="font-mono font-semibold text-foreground">{node.hostname}</code> to confirm:
          </p>
          <Input
            className="font-mono text-xs"
            placeholder={node.hostname}
            value={confirmHostname}
            onChange={(e) => { setConfirmHostname(e.target.value); setDeleteError("") }}
          />

          {deleteError && <p className="text-xs text-destructive">{deleteError}</p>}

          <div className="flex gap-2">
            <Button
              variant="destructive"
              size="sm"
              className="flex-1"
              disabled={!canDelete || deleteMutation.isPending}
              onClick={() => deleteMutation.mutate()}
            >
              {deleteMutation.isPending ? "Deleting…" : "Delete permanently"}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => { setExpanded(false); setConfirmHostname(""); setDeleteError("") }}
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

// ─── PowerButtons — PWR-LIST-1..2 ────────────────────────────────────────────
// Compact icon-only cluster shown in each nodes table row.

function PowerButtons({ nodeId }: { nodeId: string }) {
  const qc = useQueryClient()

  const { data: powerStatus } = useQuery<PowerStatusResponse>({
    queryKey: ["power-status", nodeId],
    queryFn: () => apiFetch<PowerStatusResponse>(`/api/v1/nodes/${nodeId}/power`),
    refetchInterval: 30000,
    staleTime: 15000,
    retry: false,
  })

  async function doAction(action: string) {
    try {
      await apiFetch(`/api/v1/nodes/${nodeId}/power/${action}`, { method: "POST" })
      qc.invalidateQueries({ queryKey: ["power-status", nodeId] })
      toast({ title: `Power ${action} sent`, description: nodeId.slice(0, 8) })
    } catch (err) {
      toast({ variant: "destructive", title: `Power ${action} failed`, description: String(err) })
    }
  }

  const statusColor = powerStatus?.status === "on"
    ? "bg-status-healthy"
    : powerStatus?.status === "off"
    ? "bg-muted-foreground"
    : "bg-status-warning"

  return (
    <TooltipProvider>
      <div className="flex items-center gap-0.5" role="group" aria-label="Power actions">
        {/* Power state indicator */}
        <span className={cn("h-2 w-2 rounded-full mr-1 shrink-0", statusColor)} aria-label={`Power ${powerStatus?.status ?? "unknown"}`} />
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              className="h-6 w-6 rounded hover:bg-secondary flex items-center justify-center text-muted-foreground hover:text-foreground"
              onClick={() => doAction("on")}
              aria-label="Power on"
            >
              <Power className="h-3.5 w-3.5" />
            </button>
          </TooltipTrigger>
          <TooltipContent>Power on</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              className="h-6 w-6 rounded hover:bg-secondary flex items-center justify-center text-muted-foreground hover:text-foreground"
              onClick={() => doAction("off")}
              aria-label="Power off"
            >
              <PowerOff className="h-3.5 w-3.5" />
            </button>
          </TooltipTrigger>
          <TooltipContent>Power off</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              className="h-6 w-6 rounded hover:bg-secondary flex items-center justify-center text-muted-foreground hover:text-foreground"
              onClick={() => doAction("cycle")}
              aria-label="Power cycle"
            >
              <RefreshCw className="h-3.5 w-3.5" />
            </button>
          </TooltipTrigger>
          <TooltipContent>Power cycle</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              className="h-6 w-6 rounded hover:bg-secondary flex items-center justify-center text-muted-foreground hover:text-foreground"
              onClick={() => doAction("pxe")}
              aria-label="Boot PXE"
            >
              <Network className="h-3.5 w-3.5" />
            </button>
          </TooltipTrigger>
          <TooltipContent>Boot PXE</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              className="h-6 w-6 rounded hover:bg-secondary flex items-center justify-center text-muted-foreground hover:text-foreground"
              onClick={() => doAction("disk")}
              aria-label="Boot disk"
            >
              <HardDrive className="h-3.5 w-3.5" />
            </button>
          </TooltipTrigger>
          <TooltipContent>Boot disk</TooltipContent>
        </Tooltip>
      </div>
    </TooltipProvider>
  )
}

// ─── LdapStatusSection — v0.1.22 ─────────────────────────────────────────────
// Renders the LDAP status badge and a "Re-verify LDAP" button that fires
// POST /api/v1/nodes/{id}/verify-ldap, then refreshes the nodes cache.

function LdapStatusSection({ node, qc }: { node: NodeConfig; qc: ReturnType<typeof useQueryClient> }) {
  const [verifyError, setVerifyError] = React.useState<string | null>(null)

  const verifyMutation = useMutation({
    mutationFn: () =>
      apiFetch(`/api/v1/nodes/${node.id}/verify-ldap`, { method: "POST" }),
    onSuccess: () => {
      setVerifyError(null)
      qc.invalidateQueries({ queryKey: ["nodes"] })
      toast({ title: "LDAP re-verified", description: "Node LDAP status refreshed." })
    },
    onError: (err) => {
      setVerifyError(String(err))
    },
  })

  const statusLabel = node.ldap_ready === true
    ? "OK"
    : node.ldap_ready === false
      ? "Failed"
      : "Unknown"

  const statusClass = node.ldap_ready === true
    ? "text-green-400"
    : node.ldap_ready === false
      ? "text-destructive"
      : "text-muted-foreground"

  return (
    <Section title="LDAP">
      <div className="flex items-center justify-between gap-4">
        <div className="space-y-0.5">
          <span className={cn("text-xs font-medium", statusClass)}>{statusLabel}</span>
          {node.ldap_ready_detail && (
            <p className="text-xs text-muted-foreground">{node.ldap_ready_detail}</p>
          )}
        </div>
        <Button
          size="sm"
          variant="outline"
          className="h-7 text-xs shrink-0"
          onClick={() => { setVerifyError(null); verifyMutation.mutate() }}
          disabled={verifyMutation.isPending}
        >
          {verifyMutation.isPending ? (
            <Loader2 className="h-3 w-3 animate-spin mr-1" />
          ) : (
            <RefreshCw className="h-3 w-3 mr-1" />
          )}
          Re-verify LDAP
        </Button>
      </div>
      {verifyError && (
        <p className="text-xs text-destructive mt-1">{verifyError}</p>
      )}
    </Section>
  )
}

// ─── HardwareSection — CFG-1..4 ──────────────────────────────────────────────
// New "Hardware" section in node detail Sheet.

function HardwareSection({ node }: { node: NodeConfig }) {
  const [bmcEditOpen, setBmcEditOpen] = React.useState(false)
  const [bmcIp, setBmcIp] = React.useState((node as unknown as Record<string, unknown>).bmc_ip_address as string ?? "")
  const [bmcUser, setBmcUser] = React.useState((node as unknown as Record<string, unknown>).bmc_username as string ?? "")
  const [bmcPass, setBmcPass] = React.useState("")
  const [bmcConfirm, setBmcConfirm] = React.useState("")
  const [bmcError, setBmcError] = React.useState("")
  const [testResult, setTestResult] = React.useState<{ ok: boolean; message: string } | null>(null)
  const [testing, setTesting] = React.useState(false)
  const [sensorsOpen, setSensorsOpen] = React.useState(false)

  const { data: sensors, isLoading: sensorsLoading, refetch: refetchSensors } = useQuery<SensorsResponse>({
    queryKey: ["sensors", node.id],
    queryFn: () => apiFetch<SensorsResponse>(`/api/v1/nodes/${node.id}/sensors`),
    enabled: sensorsOpen,
    staleTime: 30000,
    retry: false,
  })

  const bmcMutation = useMutation({
    mutationFn: () =>
      apiFetch(`/api/v1/nodes/${node.id}/bmc`, {
        method: "PATCH",
        body: JSON.stringify({
          ip_address: bmcIp,
          username: bmcUser,
          password: bmcPass || undefined,
          confirm: node.id,
        }),
      }),
    onSuccess: () => {
      toast({ title: "BMC config updated" })
      setBmcEditOpen(false)
      setBmcError("")
      setBmcConfirm("")
      setBmcPass("")
    },
    onError: (err) => setBmcError(String(err)),
  })

  async function handleTestBMC() {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await apiFetch<{ ok: boolean; error?: string; power_status?: string }>(`/api/v1/nodes/${node.id}/bmc/test`, {
        method: "POST",
      })
      setTestResult({
        ok: res.ok,
        message: res.ok
          ? `Connected — power ${res.power_status ?? "unknown"}`
          : (res.error ?? "Connection failed"),
      })
    } catch (err) {
      setTestResult({ ok: false, message: String(err) })
    } finally {
      setTesting(false)
    }
  }

  const bmc = node as unknown as {
    bmc?: { ip_address?: string; username?: string }
    power_provider?: { type?: string; fields?: Record<string, string> }
  }
  const hasBMC = !!(bmc.bmc?.ip_address || bmc.power_provider?.type)

  return (
    <div className="space-y-3">
      <Section title="Hardware">
        {/* BMC / IPMI */}
        {hasBMC ? (
          <>
            <Row label="BMC IP" value={bmc.bmc?.ip_address || bmc.power_provider?.fields?.host || "—"} mono />
            <Row label="BMC User" value={bmc.bmc?.username || bmc.power_provider?.fields?.username || "—"} />
            {bmc.power_provider?.type && <Row label="Provider" value={bmc.power_provider.type} mono />}
          </>
        ) : (
          <p className="text-xs text-muted-foreground">No BMC / power provider configured</p>
        )}

        {/* Edit BMC — CFG-3 */}
        {!bmcEditOpen ? (
          <div className="flex gap-2 pt-1">
            <Button size="sm" variant="outline" className="flex-1 text-xs" onClick={() => setBmcEditOpen(true)}>
              <Pencil className="h-3 w-3 mr-1" /> Edit BMC config
            </Button>
            <Button size="sm" variant="outline" className="text-xs" onClick={handleTestBMC} disabled={testing || !hasBMC}>
              {testing ? "Testing…" : "Test"}
            </Button>
          </div>
        ) : (
          <div className="rounded border border-border p-3 space-y-2 bg-secondary/20 mt-2">
            <p className="text-xs font-medium text-muted-foreground">Edit BMC config — confirm with node ID to avoid lockout</p>
            <Input className="text-xs font-mono" placeholder="BMC IP" value={bmcIp} onChange={(e) => setBmcIp(e.target.value)} />
            <Input className="text-xs" placeholder="BMC username" value={bmcUser} onChange={(e) => setBmcUser(e.target.value)} />
            <Input className="text-xs" type="password" placeholder="BMC password (leave blank to keep)" value={bmcPass} onChange={(e) => setBmcPass(e.target.value)} />
            <p className="text-xs text-muted-foreground">Type node ID <code className="font-mono">{node.id.slice(0, 8)}…</code> to confirm:</p>
            <Input
              className="text-xs font-mono"
              placeholder={node.id}
              value={bmcConfirm}
              onChange={(e) => setBmcConfirm(e.target.value)}
            />
            {bmcError && <p className="text-xs text-destructive">{bmcError}</p>}
            <div className="flex gap-2">
              <Button
                size="sm"
                className="flex-1 text-xs"
                disabled={bmcConfirm !== node.id || bmcMutation.isPending}
                onClick={() => bmcMutation.mutate()}
              >
                {bmcMutation.isPending ? "Saving…" : "Save BMC config"}
              </Button>
              <Button size="sm" variant="ghost" className="text-xs" onClick={() => { setBmcEditOpen(false); setBmcError(""); setBmcConfirm(""); setBmcPass("") }}>
                Cancel
              </Button>
            </div>
          </div>
        )}

        {/* Test result */}
        {testResult && (
          <p className={cn("text-xs mt-1", testResult.ok ? "text-status-healthy" : "text-destructive")}>
            {testResult.ok ? "✓" : "✗"} {testResult.message}
          </p>
        )}

        {/* Sensors panel — CFG-2 */}
        <div className="pt-2">
          <Button
            size="sm"
            variant="ghost"
            className="w-full text-xs text-muted-foreground"
            onClick={() => { setSensorsOpen((o) => !o); if (!sensorsOpen) refetchSensors() }}
          >
            <RotateCcw className="h-3 w-3 mr-1" />
            {sensorsOpen ? "Hide sensors" : "Show IPMI sensors"}
          </Button>
          {sensorsOpen && (
            <div className="mt-2 rounded border border-border overflow-auto max-h-44">
              {sensorsLoading ? (
                <div className="p-3 text-xs text-muted-foreground">Loading sensors…</div>
              ) : sensors?.sensors && sensors.sensors.length > 0 ? (
                <table className="w-full text-xs">
                  <thead>
                    <tr className="border-b border-border bg-secondary/40">
                      <th className="text-left p-2 font-normal text-muted-foreground">Sensor</th>
                      <th className="text-right p-2 font-normal text-muted-foreground">Value</th>
                      <th className="text-right p-2 font-normal text-muted-foreground">State</th>
                    </tr>
                  </thead>
                  <tbody>
                    {sensors.sensors.map((s, i) => (
                      <tr key={i} className="border-b border-border last:border-0">
                        <td className="p-2 font-mono">{s.name}</td>
                        <td className="p-2 text-right font-mono">{s.value} {s.unit}</td>
                        <td className={cn("p-2 text-right", s.state === "ok" ? "text-status-healthy" : "text-status-warning")}>{s.state}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <div className="p-3 text-xs text-muted-foreground">No sensor data (BMC may not be configured)</div>
              )}
            </div>
          )}
        </div>
      </Section>
    </div>
  )
}

// ─── CaptureNodeFlow — CAP-4..7 ──────────────────────────────────────────────
// "Capture as base image" flow in node detail Sheet.

function CaptureNodeFlow({ node }: { node: NodeConfig }) {
  const qc = useQueryClient()
  const [expanded, setExpanded] = React.useState(false)
  const [imageName, setImageName] = React.useState("")
  const [version, setVersion] = React.useState("1.0.0")
  const [sshUser, setSshUser] = React.useState("root")
  const [excludePaths, setExcludePaths] = React.useState("/proc\n/sys\n/dev\n/tmp\n/run")
  const [confirmHostname, setConfirmHostname] = React.useState("")
  const [captureError, setCaptureError] = React.useState("")
  const [inProgress, setInProgress] = React.useState(false)
  const [progressImageId, setProgressImageId] = React.useState<string | null>(null)

  const captureMutation = useMutation({
    mutationFn: () =>
      apiFetch<{ id: string }>("/api/v1/factory/capture", {
        method: "POST",
        body: JSON.stringify({
          source_host: node.hostname || node.fqdn || node.id,
          ssh_user: sshUser,
          name: imageName || `${node.hostname}-capture`,
          version,
          exclude_paths: excludePaths.split("\n").map((p) => p.trim()).filter(Boolean),
        }),
      }),
    onSuccess: (res) => {
      setInProgress(true)
      setProgressImageId(res.id)
      qc.invalidateQueries({ queryKey: ["images"] })
      toast({ title: "Capture started", description: `Capturing ${node.hostname} in background.` })
    },
    onError: (err) => setCaptureError(String(err)),
  })

  return (
    <div className="pt-4 border-t border-border space-y-3">
      {!expanded ? (
        <Button
          variant="outline"
          className="w-full text-xs"
          size="sm"
          onClick={() => setExpanded(true)}
        >
          <Camera className="h-3.5 w-3.5 mr-1.5" />
          Capture as base image
        </Button>
      ) : (
        <div className="rounded-md border border-border bg-secondary/10 p-4 space-y-3">
          <p className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Capture node as base image</p>

          {inProgress ? (
            <div className="space-y-2">
              <div className="flex items-center gap-2 text-sm">
                <span className="h-2 w-2 rounded-full bg-status-warning animate-pulse shrink-0" />
                <span>Capturing {node.hostname}…</span>
              </div>
              <p className="text-xs text-muted-foreground font-mono">Image ID: {progressImageId?.slice(0, 12)}</p>
              <p className="text-xs text-muted-foreground">Check /images for progress. Capture runs async via rsync.</p>
              <Button size="sm" variant="ghost" className="w-full text-xs" onClick={() => { setExpanded(false); setInProgress(false); setProgressImageId(null) }}>
                Close
              </Button>
            </div>
          ) : (
            <>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Image name</label>
                <Input className="text-xs" placeholder={`${node.hostname}-capture`} value={imageName} onChange={(e) => setImageName(e.target.value)} />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Version</label>
                <Input className="text-xs" value={version} onChange={(e) => setVersion(e.target.value)} />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">SSH user</label>
                <Input className="text-xs" value={sshUser} onChange={(e) => setSshUser(e.target.value)} />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Exclude paths (one per line)</label>
                <textarea
                  className="w-full font-mono text-xs border border-border bg-background rounded-md px-2 py-1.5 resize-none"
                  rows={4}
                  value={excludePaths}
                  onChange={(e) => setExcludePaths(e.target.value)}
                />
              </div>
              <p className="text-xs text-muted-foreground">
                Type <code className="font-mono">{node.hostname}</code> to confirm:
              </p>
              <Input
                className="font-mono text-xs"
                placeholder={node.hostname}
                value={confirmHostname}
                onChange={(e) => { setConfirmHostname(e.target.value); setCaptureError("") }}
              />
              {captureError && <p className="text-xs text-destructive">{captureError}</p>}
              <div className="flex gap-2">
                <Button
                  size="sm"
                  className="flex-1 text-xs"
                  disabled={confirmHostname !== node.hostname || captureMutation.isPending}
                  onClick={() => captureMutation.mutate()}
                >
                  {captureMutation.isPending ? "Starting…" : "Start capture"}
                </Button>
                <Button size="sm" variant="ghost" className="text-xs" onClick={() => { setExpanded(false); setCaptureError(""); setConfirmHostname("") }}>
                  Cancel
                </Button>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}

// ─── SudoersSection — NODE-SUDO-4 ─────────────────────────────────────────────
// Per-node sudoers management in the node detail Sheet.

function SudoersSection({ node }: { node: NodeConfig }) {
  const qc = useQueryClient()
  const [addOpen, setAddOpen] = React.useState(false)
  const [commands, setCommands] = React.useState("ALL")
  const [pendingUser, setPendingUser] = React.useState<UserSearchResult | null>(null)
  const [removeConfirm, setRemoveConfirm] = React.useState<string | null>(null)
  const [removeInput, setRemoveInput] = React.useState("")
  const [error, setError] = React.useState("")

  const { data, isLoading } = useQuery<ListNodeSudoersResponse>({
    queryKey: ["node-sudoers", node.id],
    queryFn: () => apiFetch<ListNodeSudoersResponse>(`/api/v1/nodes/${node.id}/sudoers`),
    staleTime: 10000,
  })

  const addMutation = useMutation({
    mutationFn: () =>
      apiFetch(`/api/v1/nodes/${node.id}/sudoers`, {
        method: "POST",
        body: JSON.stringify({
          user_identifier: pendingUser!.identifier,
          source: pendingUser!.source,
          commands,
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["node-sudoers", node.id] })
      setPendingUser(null)
      setCommands("ALL")
      setAddOpen(false)
      setError("")
      toast({ title: "Sudoer added" })
    },
    onError: (err) => setError(String(err)),
  })

  const removeMutation = useMutation({
    mutationFn: (uid: string) =>
      apiFetch(`/api/v1/nodes/${node.id}/sudoers/${encodeURIComponent(uid)}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["node-sudoers", node.id] })
      setRemoveConfirm(null)
      setRemoveInput("")
      toast({ title: "Sudoer removed" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Failed to remove sudoer", description: String(err) }),
  })

  const syncMutation = useMutation({
    mutationFn: () =>
      apiFetch(`/api/v1/nodes/${node.id}/sudoers/sync`, { method: "POST" }),
    onSuccess: (res: unknown) => {
      const r = res as { message?: string }
      toast({ title: "Sync queued", description: r?.message })
    },
    onError: (err) => toast({ variant: "destructive", title: "Sync failed", description: String(err) }),
  })

  const sudoers = data?.sudoers ?? []

  return (
    <Section title="Sudoers">
      {isLoading ? (
        <div className="space-y-1.5">
          <div className="h-4 bg-muted rounded animate-pulse" />
          <div className="h-4 bg-muted rounded animate-pulse w-2/3" />
        </div>
      ) : sudoers.length === 0 ? (
        <p className="text-xs text-muted-foreground">No sudoers assigned to this node.</p>
      ) : (
        <div className="space-y-1">
          {sudoers.map((s) => (
            <div key={s.user_identifier} className="flex items-center gap-2 text-xs">
              <span className="font-mono flex-1 truncate">{s.user_identifier}</span>
              <span className={cn(
                "rounded px-1 py-0.5 text-[10px] font-medium shrink-0",
                s.source === "ldap" ? "bg-blue-500/10 text-blue-400" : "bg-green-500/10 text-green-400"
              )}>{s.source}</span>
              <span className="text-muted-foreground font-mono shrink-0">{s.commands}</span>
              {removeConfirm === s.user_identifier ? (
                <div className="flex items-center gap-1 shrink-0">
                  <Input
                    className="h-6 w-32 text-[11px] font-mono"
                    placeholder={s.user_identifier}
                    value={removeInput}
                    onChange={(e) => setRemoveInput(e.target.value)}
                    autoFocus
                  />
                  <Button
                    size="sm"
                    variant="destructive"
                    className="h-6 px-2 text-[11px]"
                    disabled={removeInput !== s.user_identifier || removeMutation.isPending}
                    onClick={() => removeMutation.mutate(s.user_identifier)}
                  >
                    Remove
                  </Button>
                  <Button size="sm" variant="ghost" className="h-6 px-1" onClick={() => { setRemoveConfirm(null); setRemoveInput("") }}>
                    <X className="h-3 w-3" />
                  </Button>
                </div>
              ) : (
                <Button size="sm" variant="ghost" className="h-6 w-6 p-0 shrink-0" onClick={() => setRemoveConfirm(s.user_identifier)}>
                  <X className="h-3 w-3" />
                </Button>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Add sudoer inline form */}
      {addOpen ? (
        <div className="rounded border border-border p-3 space-y-2 bg-secondary/20 mt-2">
          <p className="text-xs font-medium text-muted-foreground">Add sudoer</p>
          <UserPicker
            onSelect={(u) => setPendingUser(u)}
            placeholder="Search LDAP + local users…"
          />
          {pendingUser && (
            <div className="flex items-center gap-2 text-xs rounded bg-secondary px-2 py-1">
              <span className="font-mono flex-1">{pendingUser.identifier}</span>
              <span className={cn(
                "rounded px-1 py-0.5 text-[10px] font-medium",
                pendingUser.source === "ldap" ? "bg-blue-500/10 text-blue-400" : "bg-green-500/10 text-green-400"
              )}>{pendingUser.source}</span>
              <button className="text-muted-foreground hover:text-foreground" onClick={() => setPendingUser(null)}>
                <X className="h-3 w-3" />
              </button>
            </div>
          )}
          <div className="space-y-1">
            <label className="text-xs text-muted-foreground">Commands</label>
            <Input
              className="text-xs font-mono h-7"
              placeholder="ALL"
              value={commands}
              onChange={(e) => setCommands(e.target.value)}
            />
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
          <div className="flex gap-2">
            <Button
              size="sm"
              className="flex-1 text-xs"
              disabled={!pendingUser || addMutation.isPending}
              onClick={() => addMutation.mutate()}
            >
              {addMutation.isPending ? "Adding…" : "Add sudoer"}
            </Button>
            <Button size="sm" variant="ghost" className="text-xs" onClick={() => { setAddOpen(false); setPendingUser(null); setError("") }}>
              Cancel
            </Button>
          </div>
        </div>
      ) : (
        <div className="flex gap-2 pt-1">
          <Button size="sm" variant="outline" className="flex-1 text-xs" onClick={() => setAddOpen(true)}>
            <Plus className="h-3 w-3 mr-1" /> Add sudoer
          </Button>
          {sudoers.length > 0 && (
            <Button
              size="sm"
              variant="outline"
              className="text-xs"
              disabled={syncMutation.isPending}
              onClick={() => syncMutation.mutate()}
            >
              {syncMutation.isPending ? "Syncing…" : "Sync to node"}
            </Button>
          )}
        </div>
      )}
    </Section>
  )
}
