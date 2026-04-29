import * as React from "react"
import { useNavigate, useSearch } from "@tanstack/react-router"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import { Search, ChevronUp, ChevronDown, ChevronsUpDown, Copy, Check, AlertTriangle, Plus, Pencil, X, Tag } from "lucide-react"
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
import { StatusDot } from "@/components/StatusDot"
import { useConnection } from "@/contexts/connection"
import { apiFetch, sseUrl } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type { NodeConfig, ListNodesResponse, ListImagesResponse, ReimageRequest } from "@/lib/types"
import { nodeState } from "@/lib/types"
import { cn } from "@/lib/utils"

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
  const [notes, setNotes] = React.useState("")
  const [errors, setErrors] = React.useState<Record<string, string>>({})

  function reset() {
    setHostname(""); setMac(""); setIp(""); setRoles([]); setNotes(""); setErrors({})
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

  // Fetch base images for base_image_id required by CreateNode
  const { data: imagesData } = useQuery<ListImagesResponse>({
    queryKey: ["images"],
    queryFn: () => apiFetch<ListImagesResponse>("/api/v1/images"),
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
  // TAG-4: one or more key:value tag filters (AND semantics, repeated ?tag= param)
  tag?: string[]
}

export function NodesPage() {
  const { setStatus, retryToken } = useConnection()
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as NodeSearch

  // URL-driven state
  const q = search.q ?? ""
  const sortCol = search.sort ?? ""
  const sortDir = search.dir ?? "asc"
  // TAG-4: active tag filters from URL (normalised to string[])
  const activeTags: string[] = search.tag ?? []
  const [advanced, setAdvanced] = React.useState(false)
  const [selectedNode, setSelectedNode] = React.useState<NodeConfig | null>(null)
  const [addNodeOpen, setAddNodeOpen] = React.useState(false)
  // NODE-CREATE-5: auto-open AddNode sheet from URL param (used by Cmd-K "Add node…").
  React.useEffect(() => {
    if (search.addNode === "1") {
      setAddNodeOpen(true)
      navigate({
        to: "/nodes",
        search: { q: q || undefined, status: search.status, sort: sortCol || undefined, dir: sortDir === "asc" ? undefined : "desc", tag: activeTags.length ? activeTags : undefined, openNode: undefined, reimage: undefined, addNode: undefined },
        replace: true,
      })
    }
  }, [search.addNode]) // eslint-disable-line react-hooks/exhaustive-deps
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
        tag: patch.tag !== undefined ? (patch.tag?.length ? patch.tag : undefined) : (activeTags.length ? activeTags : undefined),
        openNode: undefined,
        reimage: undefined,
        addNode: undefined,
      },
      replace: true,
    })
  }

  // TanStack Query for nodes
  const { data, refetch, isLoading } = useQuery<ListNodesResponse>({
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
        search: { q: q || undefined, status: search.status, sort: sortCol || undefined, dir: sortDir === "asc" ? undefined : "desc", tag: activeTags.length ? activeTags : undefined, openNode: undefined, reimage: undefined, addNode: undefined },
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
          <div className="relative w-72 shrink-0">
            <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
            <Input
              className="pl-8"
              placeholder="Search nodes..."
              value={q}
              onChange={(e) => updateSearch({ q: e.target.value || undefined })}
            />
          </div>
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
          {/* TAG-4: tag picker */}
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
        </div>
        <div className="flex items-center gap-2">
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
        </div>
      </div>

      {/* Table */}
      <div className="flex-1 overflow-auto">
        {isLoading ? (
          <NodesSkeleton />
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
}

// ─── NodeSheet ────────────────────────────────────────────────────────────────
// Sprint 4: EDIT-NODE-2/3 (inline edit mode) + TAG-3/5 (tag management)

function NodeSheet({ node, onClose, advanced, relativeTime, autoReimage }: NodeSheetProps) {
  const qc = useQueryClient()
  const state = nodeState(node)
  const [editing, setEditing] = React.useState(false)
  const [editHostname, setEditHostname] = React.useState(node.hostname)
  const [editFqdn, setEditFqdn] = React.useState(node.fqdn || "")
  const [editTags, setEditTags] = React.useState<string[]>(node.tags ?? [])
  const [editRoleConfirm, setEditRoleConfirm] = React.useState("")
  const [editError, setEditError] = React.useState("")
  // TAG-3/5: inline tag add
  const [tagInput, setTagInput] = React.useState("")

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
                <Button size="sm" variant="ghost" onClick={() => { setEditing(false); setEditError(""); setEditHostname(node.hostname); setEditFqdn(node.fqdn || ""); setEditTags(node.tags ?? []) }}>
                  Cancel
                </Button>
              </div>
            </div>
          ) : (
            <>
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

          <ReimageFlow node={node} autoExpand={autoReimage} />
            </>
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
