import * as React from "react"
import { useNavigate, useSearch, useParams, Link } from "@tanstack/react-router"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import {
  Search, ChevronUp, ChevronDown, ChevronRight, ChevronsUpDown, Copy, Check, AlertTriangle, Plus, Pencil, X, Tag, Trash2,
  Power, PowerOff, RefreshCw, RotateCcw, Network, HardDrive, Cpu, Camera, Users, Loader2, Activity, BookOpen, Terminal, ScrollText, Settings2, Zap, Radio,
  Square, CheckSquare, WifiOff, ImagePlay, Play, GitBranch, ArrowLeft,
} from "lucide-react"
import { SensorsTab, EventLogTab, ConsoleTab, DeployLogTab, IpmiTab, ExternalStatsTab } from "@/routes/node-detail-tabs"
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
import type { NodeConfig, ListNodesResponse, ListImagesResponse, ReimageRequest, PowerStatusResponse, SensorsResponse, SlurmNodeRole, SlurmNodeSyncStatus, SlurmNodeOverride, ProbeResult, EffectiveLayoutResponse } from "@/lib/types"
import { nodeState, NODE_PROVIDERS, NODE_OPERATING_MODES, operatingModeLabel } from "@/lib/types"
import { cn } from "@/lib/utils"
import { GroupsPanel } from "@/routes/groups"
import { UserPicker } from "@/components/UserPicker"
import { BootSettingsModal } from "@/components/BootSettingsModal"
import { DiskLayoutPicker, FirmwareBadge } from "@/components/DiskLayoutPicker"
import { HostlistInput } from "@/components/HostlistInput"
import { InterfaceList, validateInterfaces } from "@/components/InterfaceList"
import type { InterfaceRow } from "@/components/InterfaceList"
import type { ListNodeSudoersResponse, UserSearchResult } from "@/lib/types"

// ─── Sprint 38: PROBE-3 — Reachability dots ──────────────────────────────────
//
// Each node row shows three compact dots: ping / ssh / bmc.
// Dots are green (reachable), red (unreachable), or grey (not yet probed).
// Tooltip shows the last-probed timestamp.
// Source: GET /api/v1/nodes/{id}/probes   (Richard Bundle A)
// The probe result is fetched per-node lazily when the row is first rendered
// and cached for 60s (the server collects every 60s).

const PROBE_LABELS = [
  { key: "ping", label: "Ping" },
  { key: "ssh",  label: "SSH"  },
  { key: "bmc",  label: "BMC"  },
] as const

type ProbeKey = typeof PROBE_LABELS[number]["key"]

/** Returns true when the thrown error is a 404 (probe endpoint returns 404
 *  when the node has no probe configuration, which is a valid state). */
function is404Error(err: unknown): boolean {
  return err instanceof Error && err.message.startsWith("404:")
}

function ReachabilityDots({ nodeId }: { nodeId: string }) {
  const { data, error } = useQuery<ProbeResult>({
    queryKey: ["node-probes", nodeId],
    queryFn: () => apiFetch<ProbeResult>(`/api/v1/nodes/${nodeId}/probes`),
    refetchInterval: 60_000,
    staleTime: 55_000,
    retry: false,
  })

  const checkedAt = data?.checked_at
    ? new Date(data.checked_at).toLocaleTimeString()
    : null

  // 404 → node has no probe config → grey "not probed" (normal state).
  // Other errors (5xx, network, auth) → distinct amber dot + tooltip.
  const hasFetchError = !!error && !is404Error(error)

  return (
    <TooltipProvider>
      <div className="flex items-center gap-1" data-testid={`reachability-${nodeId}`}>
        {hasFetchError && (
          <Tooltip>
            <TooltipTrigger asChild>
              <span
                className="h-2 w-2 rounded-full shrink-0 bg-status-warning"
                aria-label="probe fetch error"
                data-testid="probe-dot-error"
              />
            </TooltipTrigger>
            <TooltipContent side="top" className="text-xs">
              Probe fetch failed: {String(error)}
            </TooltipContent>
          </Tooltip>
        )}
        {!hasFetchError && PROBE_LABELS.map(({ key, label }) => {
          const val: boolean | undefined = data ? (data[key as ProbeKey] as boolean) : undefined
          const color =
            val === true  ? "bg-status-healthy" :
            val === false ? "bg-status-error" :
            "bg-muted-foreground/30"
          const tip = checkedAt
            ? `${label}: ${val === true ? "up" : val === false ? "down" : "unknown"} (${checkedAt})`
            : `${label}: not probed`
          return (
            <Tooltip key={key}>
              <TooltipTrigger asChild>
                <span
                  className={`h-2 w-2 rounded-full shrink-0 ${color}`}
                  aria-label={tip}
                  data-testid={`probe-dot-${key}`}
                />
              </TooltipTrigger>
              <TooltipContent side="top" className="text-xs">{tip}</TooltipContent>
            </Tooltip>
          )
        })}
      </div>
    </TooltipProvider>
  )
}

// ─── Zod-like validation helpers (no extra dep) ──────────────────────────────
const hostnameRe = /^[a-z0-9-]{1,63}$/

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
  const [roles, setRoles] = React.useState<string[]>([])
  const [provider, setProvider] = React.useState("")
  const [notes, setNotes] = React.useState("")
  const [errors, setErrors] = React.useState<Record<string, string>>({})
  // MULTI-NIC-EDITOR: typed per-interface list replaces single MAC/IP fields
  const [interfaces, setInterfaces] = React.useState<InterfaceRow[]>([
    { kind: "ethernet", name: "eth0", mac: "", ip: "", vlan: "", is_default_gateway: true },
  ])
  const [ifaceErrors, setIfaceErrors] = React.useState<Record<string, string>>({})

  function reset() {
    setHostname(""); setRoles([]); setProvider(""); setNotes(""); setErrors({})
    setInterfaces([{ kind: "ethernet", name: "eth0", mac: "", ip: "", vlan: "", is_default_gateway: true }])
    setIfaceErrors({})
  }

  function handleClose() { reset(); onClose() }

  function validate(): boolean {
    const errs: Record<string, string> = {}
    if (!hostnameRe.test(hostname)) errs.hostname = "Lowercase letters, digits, hyphens, 1–63 chars"
    setErrors(errs)
    let iErrs = validateInterfaces(interfaces)
    // Require at least one ethernet interface with a non-empty MAC — this is
    // what drives PXE boot and becomes the node's primary_mac on the server.
    const hasEthWithMac = interfaces.some(
      (i) => i.kind === "ethernet" && (i as { mac?: string }).mac?.trim()
    )
    if (!hasEthWithMac) {
      iErrs = { ...iErrs, _global: "At least one Ethernet interface with a MAC address is required" }
    }
    setIfaceErrors(iErrs)
    return Object.keys(errs).length === 0 && Object.keys(iErrs).length === 0
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
      // Derive primary_mac from first ethernet interface with a MAC
      const ethIface = interfaces.find((i) => i.kind === "ethernet") as (typeof interfaces[0] & { mac?: string }) | undefined
      const primaryMac = ethIface ? normalizeMAC((ethIface as { mac: string }).mac) : ""

      // Build wire-format interfaces array
      const wireInterfaces = interfaces.map((iface) => {
        if (iface.kind === "ethernet") {
          return {
            kind: "ethernet",
            name: iface.name,
            mac_address: normalizeMAC(iface.mac),
            ip_address: iface.ip || undefined,
            vlan: iface.vlan ? parseInt(iface.vlan, 10) : undefined,
            is_default_gateway: iface.is_default_gateway,
          }
        }
        if (iface.kind === "fabric") {
          return {
            kind: "fabric",
            name: iface.name,
            guid: iface.guid,
            ip_address: iface.ip || undefined,
            port: iface.port ? parseInt(iface.port, 10) : undefined,
          }
        }
        // ipmi
        return {
          kind: "ipmi",
          name: iface.name,
          ip_address: iface.ip,
          channel: parseInt(iface.channel, 10),
          user: iface.user,
          password: iface.pass,
        }
      })

      const body: Record<string, unknown> = {
        hostname,
        primary_mac: primaryMac,
        base_image_id: baseImageId || (readyImages[0]?.id ?? ""),
        tags: roles.length ? roles : [],
        interfaces: wireInterfaces,
      }
      if (provider) body.provider = provider
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
      if (msg.includes("hostname")) setErrors((e) => ({ ...e, hostname: msg }))
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
              <TabsTrigger value="bulk" className="flex-1">Bulk</TabsTrigger>
            </TabsList>
            <TabsContent value="single">
              <form onSubmit={handleSubmit} className="space-y-4">
                <Field label="Hostname *" error={errors.hostname}>
                  <Input
                    placeholder="compute-01"
                    value={hostname}
                    onChange={(e) => setHostname(e.target.value)}
                    className={cn(errors.hostname && "border-destructive")}
                    data-testid="add-node-hostname"
                  />
                </Field>

                {/* MULTI-NIC-EDITOR — typed per-interface cards */}
                <div className="space-y-1">
                  <label className="text-sm text-muted-foreground">Interfaces</label>
                  <InterfaceList
                    value={interfaces}
                    onChange={setInterfaces}
                    errors={ifaceErrors}
                  />
                  {ifaceErrors._global && (
                    <p className="text-xs text-destructive mt-1">{ifaceErrors._global}</p>
                  )}
                </div>

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
                  <Button type="submit" className="flex-1" disabled={mutation.isPending} data-testid="add-node-submit">
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

// HOSTLIST-BULK-ADD: Hostlist is the PRIMARY tab; CSV/YAML is secondary.
function BulkAddNodes({ onSuccess, readyImages }: { onSuccess: () => void; readyImages: Array<{ id: string; name: string; version: string }> }) {
  const qc = useQueryClient()
  // Hostlist tab state
  const [hostlistValue, setHostlistValue] = React.useState("")
  const [hostlistExpanded, setHostlistExpanded] = React.useState<string[]>([])
  const [hostlistLoading, setHostlistLoading] = React.useState(false)
  const [hostlistResults, setHostlistResults] = React.useState<BatchResult[]>([])
  const [hostlistSubmitted, setHostlistSubmitted] = React.useState(false)
  const [defaultImageId, setDefaultImageId] = React.useState("")
  // CSV/YAML tab state
  const [raw, setRaw] = React.useState("")
  const [rows, setRows] = React.useState<BulkRow[]>([])
  const [results, setResults] = React.useState<BatchResult[]>([])
  const [submitted, setSubmitted] = React.useState(false)
  const [loading, setLoading] = React.useState(false)
  const [bulkTab, setBulkTab] = React.useState<"hostlist" | "csv">("hostlist")

  // HOSTLIST-BULK-ADD: commit expanded hostnames as nodes without MAC (assign later).
  async function handleHostlistSubmit() {
    if (hostlistExpanded.length === 0) return
    setHostlistLoading(true)
    try {
      const resp = await apiFetch<{ results: BatchResult[] }>("/api/v1/nodes/batch", {
        method: "POST",
        body: JSON.stringify({
          nodes: hostlistExpanded.map((hostname) => ({
            hostname,
            primary_mac: "",
            tags: [],
            base_image_id: defaultImageId || readyImages[0]?.id || "",
          })),
        }),
      })
      setHostlistResults(resp.results)
      setHostlistSubmitted(true)
      qc.invalidateQueries({ queryKey: ["nodes"] })
      const created = resp.results.filter((r) => r.status === "created").length
      toast({ title: `${created} of ${hostlistExpanded.length} nodes created` })
      if (created === hostlistExpanded.length) setTimeout(onSuccess, 1200)
    } catch (err) {
      toast({ variant: "destructive", title: "Batch failed", description: String(err) })
    } finally {
      setHostlistLoading(false)
    }
  }

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
      <Tabs value={bulkTab} onValueChange={(v) => setBulkTab(v as "hostlist" | "csv")}>
        <TabsList className="w-full mb-3">
          <TabsTrigger value="hostlist" className="flex-1 text-xs">Hostlist</TabsTrigger>
          <TabsTrigger value="csv" className="flex-1 text-xs">CSV / YAML</TabsTrigger>
        </TabsList>

        {/* ── HOSTLIST-BULK-ADD: primary tab ── */}
        <TabsContent value="hostlist" className="space-y-3">
          <p className="text-xs text-muted-foreground">
            Enter a range pattern to register nodes in bulk. MAC addresses can be assigned later.
          </p>
          <HostlistInput
            id="bulk-hostlist-primary"
            value={hostlistValue}
            onChange={(v) => { setHostlistValue(v); setHostlistResults([]); setHostlistSubmitted(false) }}
            onExpanded={setHostlistExpanded}
            placeholder="compute[001-128] or gpu[001-008]"
            data-testid="hostlist-primary-input"
          />
          {readyImages.length > 0 && (
            <div className="space-y-1">
              <label className="text-xs text-muted-foreground">Default base image (optional)</label>
              <select
                className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
                value={defaultImageId}
                onChange={(e) => setDefaultImageId(e.target.value)}
              >
                <option value="">None (assign later)</option>
                {readyImages.map((img) => (
                  <option key={img.id} value={img.id}>{img.name} {img.version}</option>
                ))}
              </select>
            </div>
          )}

          {/* Post-submit results preview */}
          {hostlistSubmitted && hostlistResults.length > 0 && (
            <div className="rounded-md border border-border overflow-auto max-h-40">
              <table className="w-full text-xs">
                <thead>
                  <tr className="border-b border-border bg-secondary/40">
                    <th className="text-left p-2">Hostname</th>
                    <th className="text-left p-2">Status</th>
                  </tr>
                </thead>
                <tbody>
                  {hostlistExpanded.map((hostname, i) => {
                    const r = hostlistResults[i]
                    return (
                      <tr key={hostname} className="border-b border-border last:border-0">
                        <td className="p-2 font-mono">{hostname}</td>
                        <td className="p-2">
                          {r ? (
                            <span className={r.status === "created" ? "text-status-healthy" : "text-destructive"}>
                              {r.status}{r.error ? `: ${r.error}` : ""}
                            </span>
                          ) : <span className="text-muted-foreground">—</span>}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}

          {!hostlistSubmitted && (
            <Button
              className="w-full"
              size="sm"
              onClick={handleHostlistSubmit}
              disabled={hostlistLoading || hostlistExpanded.length === 0}
              data-testid="hostlist-bulk-submit"
            >
              {hostlistLoading
                ? "Creating…"
                : hostlistExpanded.length > 0
                  ? `Add ${hostlistExpanded.length} node${hostlistExpanded.length !== 1 ? "s" : ""}`
                  : "Add nodes"}
            </Button>
          )}
        </TabsContent>

        {/* ── CSV / YAML tab (legacy) ── */}
        <TabsContent value="csv" className="space-y-4">
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
        </TabsContent>
      </Tabs>
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
  const [addNodeOpen, setAddNodeOpen] = React.useState(false)
  // BULK-MULTISELECT: row selection state
  const [selectedNodeIds, setSelectedNodeIds] = React.useState<Set<string>>(new Set())
  // BULK-POWER / BULK-ACTIONS: confirm dialog
  const [bulkActionPending, setBulkActionPending] = React.useState<string | null>(null)
  const [bulkConfirmInput, setBulkConfirmInput] = React.useState("")
  const [bulkLoading, setBulkLoading] = React.useState(false)
  // run-command: prompt for command text before dispatching bulk exec
  const [runCommandOpen, setRunCommandOpen] = React.useState(false)
  const [runCommandText, setRunCommandText] = React.useState("")
  const qc = useQueryClient()
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

  // BULK-MULTISELECT helpers
  function toggleSelectAll() {
    if (selectedNodeIds.size === nodes.length && nodes.length > 0) {
      setSelectedNodeIds(new Set())
    } else {
      setSelectedNodeIds(new Set(nodes.map((n) => n.id)))
    }
  }

  function toggleSelectNode(id: string) {
    setSelectedNodeIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  // DESTRUCTIVE_ACTIONS require typed count confirm
  const DESTRUCTIVE_BULK_ACTIONS = new Set(["off", "cycle", "reset", "soft-off", "reimage", "drain"])

  async function executeBulkAction(action: string) {
    const nodeIds = Array.from(selectedNodeIds)
    setBulkLoading(true)
    try {
      let url: string
      let body: Record<string, unknown> = { node_ids: nodeIds }
      if (["on", "off", "cycle", "reset", "soft-off"].includes(action)) {
        url = `/api/v1/nodes/bulk/power/${action}`
      } else if (action === "reimage") {
        url = "/api/v1/nodes/bulk/reimage"
      } else if (action === "drain") {
        url = "/api/v1/nodes/bulk/drain"
      } else if (action === "netboot") {
        url = "/api/v1/nodes/bulk/power/pxe"
      } else if (action === "run-command") {
        url = "/api/v1/exec/bulk"
        body = { node_ids: nodeIds, command: runCommandText }
      } else {
        url = `/api/v1/nodes/bulk/${action}`
      }
      const resp = await apiFetch<{ results?: Array<{ node_id: string; ok: boolean; error?: string }> }>(url, {
        method: "POST",
        body: JSON.stringify(body),
      })
      const results = resp.results ?? []
      const succeeded = results.filter((r) => r.ok).length
      const failed = results.length - succeeded
      if (results.length > 0) {
        toast({
          title: failed === 0
            ? `${succeeded}/${results.length} succeeded`
            : `${succeeded}/${results.length} succeeded — ${failed} failed`,
          variant: failed > 0 ? "destructive" : "default",
        })
      } else {
        toast({ title: `Bulk ${action} dispatched for ${nodeIds.length} node${nodeIds.length !== 1 ? "s" : ""}` })
      }
      qc.invalidateQueries({ queryKey: ["nodes"] })
      setSelectedNodeIds(new Set())
    } catch (err) {
      toast({ variant: "destructive", title: `Bulk ${action} failed`, description: String(err) })
    } finally {
      setBulkLoading(false)
      setBulkActionPending(null)
      setBulkConfirmInput("")
    }
  }

  function handleBulkAction(action: string) {
    if (action === "run-command") {
      // Open command-text modal before dispatching — prevents sending empty command.
      setRunCommandText("")
      setRunCommandOpen(true)
    } else if (DESTRUCTIVE_BULK_ACTIONS.has(action)) {
      setBulkActionPending(action)
      setBulkConfirmInput("")
    } else {
      executeBulkAction(action)
    }
  }

  // PAL-2-2: auto-navigate to full-page node detail when ?openNode=<id> is in the URL.
  // Used by Cmd-K "Reimage node…" / "Delete node…" — navigates then lets the detail
  // page handle autoReimage / autoDelete from its own search params.
  React.useEffect(() => {
    if (!openNodeId) return
    navigate({
      to: "/nodes/$nodeId",
      params: { nodeId: openNodeId },
      search: {
        reimage: autoReimage ? "1" : undefined,
        deleteNode: autoDelete ? "1" : undefined,
      },
      replace: true,
    })
  }, [openNodeId]) // eslint-disable-line react-hooks/exhaustive-deps

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
          <>
          {/* BULK-MULTISELECT: sticky action bar when nodes are selected */}
          {selectedNodeIds.size > 0 && (
            <div className="sticky top-0 z-20 border-b border-border bg-background/95 backdrop-blur px-4 py-2 flex items-center gap-2 flex-wrap" data-testid="bulk-action-bar">
              <span className="text-xs text-muted-foreground font-medium shrink-0">
                {selectedNodeIds.size} selected
              </span>
              <div className="h-4 w-px bg-border shrink-0" />
              {/* Power actions */}
              <span className="text-xs text-muted-foreground shrink-0">Power:</span>
              {(["on", "off", "cycle", "reset", "soft-off"] as const).map((action) => (
                <Button
                  key={action}
                  size="sm"
                  variant={DESTRUCTIVE_BULK_ACTIONS.has(action) ? "destructive" : "outline"}
                  className="h-7 text-xs px-2"
                  onClick={() => handleBulkAction(action)}
                  disabled={bulkLoading}
                  data-testid={`bulk-power-${action}`}
                >
                  {action === "on" && <Power className="h-3 w-3 mr-1" />}
                  {action === "off" && <PowerOff className="h-3 w-3 mr-1" />}
                  {action === "cycle" && <RefreshCw className="h-3 w-3 mr-1" />}
                  {action === "reset" && <RotateCcw className="h-3 w-3 mr-1" />}
                  {action === "soft-off" && <PowerOff className="h-3 w-3 mr-1" />}
                  {action}
                </Button>
              ))}
              <div className="h-4 w-px bg-border shrink-0" />
              {/* Bulk action group */}
              {([
                { action: "reimage", label: "Reimage", icon: <ImagePlay className="h-3 w-3 mr-1" /> },
                { action: "drain", label: "Drain", icon: <WifiOff className="h-3 w-3 mr-1" /> },
                { action: "netboot", label: "Netboot", icon: <Network className="h-3 w-3 mr-1" /> },
                { action: "run-command", label: "Run Command", icon: <Play className="h-3 w-3 mr-1" /> },
              ] as const).map(({ action, label, icon }) => (
                <Button
                  key={action}
                  size="sm"
                  variant={DESTRUCTIVE_BULK_ACTIONS.has(action) ? "destructive" : "outline"}
                  className="h-7 text-xs px-2"
                  onClick={() => handleBulkAction(action)}
                  disabled={bulkLoading}
                  data-testid={`bulk-action-${action}`}
                >
                  {icon}
                  {label}
                </Button>
              ))}
              <Button
                size="sm"
                variant="ghost"
                className="h-7 text-xs ml-auto"
                onClick={() => setSelectedNodeIds(new Set())}
              >
                Clear
              </Button>
            </div>
          )}

          {/* Run-command modal — prompts for command text before dispatching bulk exec */}
          {runCommandOpen && (
            <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 backdrop-blur-sm" data-testid="run-command-dialog">
              <div className="w-full max-w-sm rounded-lg border border-border bg-background p-6 shadow-lg space-y-4">
                <h3 className="text-sm font-semibold">Run command on {selectedNodeIds.size} node{selectedNodeIds.size !== 1 ? "s" : ""}</h3>
                <Input
                  autoFocus
                  className="font-mono text-xs"
                  placeholder="e.g. systemctl restart slurmctld"
                  value={runCommandText}
                  onChange={(e) => setRunCommandText(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && runCommandText.trim()) {
                      setRunCommandOpen(false)
                      executeBulkAction("run-command")
                    }
                  }}
                  data-testid="run-command-input"
                />
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    className="flex-1"
                    disabled={!runCommandText.trim() || bulkLoading}
                    onClick={() => { setRunCommandOpen(false); executeBulkAction("run-command") }}
                    data-testid="run-command-submit"
                  >
                    {bulkLoading ? <Loader2 className="h-3 w-3 animate-spin mr-1" /> : null}
                    Run
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => { setRunCommandOpen(false); setRunCommandText("") }}
                  >
                    Cancel
                  </Button>
                </div>
              </div>
            </div>
          )}

          {/* BULK-MULTISELECT typed-confirm dialog */}
          {bulkActionPending && (
            <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 backdrop-blur-sm" data-testid="bulk-confirm-dialog">
              <div className="w-full max-w-sm rounded-lg border border-border bg-background p-6 shadow-lg space-y-4">
                <h3 className="text-sm font-semibold">Confirm bulk {bulkActionPending}</h3>
                <p className="text-sm text-muted-foreground">
                  Type <code className="font-mono font-semibold text-foreground">{selectedNodeIds.size}</code> to confirm
                  this action on {selectedNodeIds.size} node{selectedNodeIds.size !== 1 ? "s" : ""}.
                </p>
                <Input
                  autoFocus
                  className="font-mono text-xs"
                  placeholder={String(selectedNodeIds.size)}
                  value={bulkConfirmInput}
                  onChange={(e) => setBulkConfirmInput(e.target.value)}
                  data-testid="bulk-confirm-input"
                />
                <div className="flex gap-2">
                  <Button
                    variant="destructive"
                    size="sm"
                    className="flex-1"
                    disabled={bulkConfirmInput !== String(selectedNodeIds.size) || bulkLoading}
                    onClick={() => executeBulkAction(bulkActionPending)}
                    data-testid="bulk-confirm-submit"
                  >
                    {bulkLoading ? <Loader2 className="h-3 w-3 animate-spin mr-1" /> : null}
                    Confirm
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => { setBulkActionPending(null); setBulkConfirmInput("") }}
                  >
                    Cancel
                  </Button>
                </div>
              </div>
            </div>
          )}

          <Table>
            <caption className="sr-only">Registered cluster nodes</caption>
            <TableHeader>
              <TableRow>
                {/* BULK-MULTISELECT: header checkbox */}
                <TableHead scope="col" className="w-8" onClick={(e) => e.stopPropagation()}>
                  <button
                    onClick={toggleSelectAll}
                    className="text-muted-foreground hover:text-foreground"
                    aria-label="Select all nodes"
                    data-testid="select-all-checkbox"
                  >
                    {selectedNodeIds.size === nodes.length && nodes.length > 0
                      ? <CheckSquare className="h-4 w-4" />
                      : <Square className="h-4 w-4" />}
                  </button>
                </TableHead>
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
                {/* PROBE-3: reachability column */}
                <TableHead scope="col" aria-label="Reachability probes (ping/ssh/bmc)">Reachability</TableHead>
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
                  className={cn("cursor-pointer", selectedNodeIds.has(node.id) && "bg-secondary/40")}
                  onClick={() => navigate({ to: "/nodes/$nodeId", params: { nodeId: node.id }, search: { reimage: undefined, deleteNode: undefined } })}
                  data-testid={`node-row-${node.id}`}
                >
                  {/* BULK-MULTISELECT: row checkbox */}
                  <TableCell onClick={(e) => { e.stopPropagation(); toggleSelectNode(node.id) }} className="w-8">
                    <button
                      className="text-muted-foreground hover:text-foreground"
                      aria-label={`Select ${node.hostname}`}
                      data-testid={`row-checkbox-${node.id}`}
                    >
                      {selectedNodeIds.has(node.id)
                        ? <CheckSquare className="h-4 w-4 text-primary" />
                        : <Square className="h-4 w-4" />}
                    </button>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1.5">
                      <span className="font-mono text-xs">{node.hostname || node.id}</span>
                      {operatingModeLabel(node.operating_mode) && (
                        <span
                          className="inline-flex items-center rounded border border-blue-300 bg-blue-100 px-1.5 py-0.5 text-xs font-medium text-blue-800 dark:border-blue-700 dark:bg-blue-950 dark:text-blue-300"
                          data-testid={`operating-mode-badge-${node.id}`}
                          title={`Operating mode: ${operatingModeLabel(node.operating_mode)}`}
                        >
                          {operatingModeLabel(node.operating_mode)}
                        </span>
                      )}
                    </div>
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
                  {/* PROBE-3: per-row reachability dots */}
                  <TableCell onClick={(e) => e.stopPropagation()}>
                    <ReachabilityDots nodeId={node.id} />
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
          </>
        )}
      </div>

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

type NodeDetailTab = "overview" | "sensors" | "extstats" | "eventlog" | "console" | "deploylog" | "ipmi"

function EditField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <label className="text-xs text-muted-foreground">{label}</label>
      {children}
    </div>
  )
}

// ─── OperatingModePicker ──────────────────────────────────────────────────────
// Sprint 37 UI: dropdown that lets the operator change a node's operating mode.
// Fires PATCH /api/v1/nodes/{id} { operating_mode } on save.
// Disabled options (filesystem_install, stateless_ram) show a tooltip explaining
// they are not yet implemented.

const NOT_YET_IMPLEMENTED_TOOLTIP = "Not yet implemented — planned for future release"

interface OperatingModePickerProps {
  node: NodeConfig
  qc: ReturnType<typeof useQueryClient>
  // onSaved is called with the newly confirmed mode after a successful PATCH so
  // the parent can update its selectedNode snapshot before pendingMode is cleared.
  // Without this, invalidateQueries races against setPendingMode(null): the picker
  // falls back to the stale node.operating_mode from the snapshot while the sheet
  // is still open, making it look like the save failed even though it succeeded.
  onSaved?: (mode: string) => void
}

function OperatingModePicker({ node, qc, onSaved }: OperatingModePickerProps) {
  // pendingMode is null when the operator has not changed anything (no dirty state).
  // When null, we display node.operating_mode which always reflects the latest server value.
  const [pendingMode, setPendingMode] = React.useState<string | null>(null)
  const [saving, setSaving] = React.useState(false)

  const serverMode = node.operating_mode ?? "block_install"
  const displayMode = pendingMode ?? serverMode
  const isDirty = pendingMode !== null && pendingMode !== serverMode

  async function handleSave() {
    if (!pendingMode) return
    setSaving(true)
    try {
      await apiFetch<NodeConfig>(`/api/v1/nodes/${node.id}`, {
        method: "PATCH",
        body: JSON.stringify({ operating_mode: pendingMode }),
      })
      qc.invalidateQueries({ queryKey: ["nodes"] })
      toast({ title: "Operating mode saved", description: `Set to ${NODE_OPERATING_MODES.find((m) => m.value === pendingMode)?.label ?? pendingMode}.` })
      // Update the parent's selectedNode snapshot BEFORE clearing pendingMode so
      // the picker stays on the saved value while the sheet is open. If we clear
      // pendingMode first, displayMode falls back to node.operating_mode from the
      // stale snapshot and the picker appears to revert to the old mode.
      onSaved?.(pendingMode)
      setPendingMode(null)
    } catch (err) {
      toast({ variant: "destructive", title: "Failed to save operating mode", description: String(err) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <Section title="Operating Mode">
      <div className="space-y-2">
        <p className="text-xs text-muted-foreground">
          Controls how this node boots and runs. Changes take effect on next PXE boot.
        </p>
        <TooltipProvider>
          <div className="space-y-1" data-testid="operating-mode-picker">
            {NODE_OPERATING_MODES.map((mode) => (
              <Tooltip key={mode.value}>
                <TooltipTrigger asChild>
                  <label
                    className={cn(
                      "flex items-center gap-2 rounded px-2 py-1.5 text-sm cursor-pointer transition-colors",
                      mode.disabled
                        ? "opacity-50 cursor-not-allowed"
                        : "hover:bg-secondary/50",
                      displayMode === mode.value && !mode.disabled && "bg-secondary/60 font-medium",
                    )}
                  >
                    <input
                      type="radio"
                      name={`operating-mode-${node.id}`}
                      value={mode.value}
                      checked={displayMode === mode.value}
                      disabled={mode.disabled}
                      onChange={() => !mode.disabled && setPendingMode(mode.value)}
                      className="accent-primary"
                      data-testid={`operating-mode-option-${mode.value}`}
                    />
                    {mode.label}
                    {mode.disabled && (
                      <span className="ml-auto text-xs text-muted-foreground italic">Not yet implemented</span>
                    )}
                  </label>
                </TooltipTrigger>
                {mode.disabled && (
                  <TooltipContent side="right" className="text-xs">
                    {NOT_YET_IMPLEMENTED_TOOLTIP}
                  </TooltipContent>
                )}
              </Tooltip>
            ))}
          </div>
        </TooltipProvider>
        {isDirty && (
          <div className="flex gap-1.5 pt-1">
            <Button
              size="sm"
              className="flex-1 h-7 text-xs"
              onClick={handleSave}
              disabled={saving}
              data-testid="operating-mode-save"
            >
              {saving ? <Loader2 className="h-3 w-3 animate-spin mr-1" /> : null}
              {saving ? "Saving…" : "Save"}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="h-7 text-xs"
              onClick={() => setPendingMode(null)}
              disabled={saving}
            >
              Cancel
            </Button>
          </div>
        )}
      </div>
    </Section>
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

// ─── VariantsEditor — Sprint 44 VARIANTS-SYSTEM ──────────────────────────────
// Shows applied overlays (variants) for this node and lets operators add/remove them.
// Wires to: GET /api/v1/nodes/{id}/effective-config, GET /api/v1/variants?node_id={id},
//           POST /api/v1/variants, DELETE /api/v1/variants/{id}

interface Variant {
  id: string
  attribute_path: string
  scope: "group" | "role" | "node"
  scope_ref?: string
  value: string
  created_at: string
}

interface EffectiveConfigEntry {
  attribute_path: string
  effective_value: string
  sources: Array<{ scope: string; scope_ref?: string; value: string }>
}

function VariantsEditor({ nodeId }: { nodeId: string }) {
  const qc = useQueryClient()
  const [expanded, setExpanded] = React.useState(false)
  const [addOpen, setAddOpen] = React.useState(false)
  const [newPath, setNewPath] = React.useState("")
  const [newScope, setNewScope] = React.useState<"group" | "role" | "node">("node")
  const [newScopeRef, setNewScopeRef] = React.useState("")
  const [newValue, setNewValue] = React.useState("")
  const [addError, setAddError] = React.useState("")
  const [removeConfirm, setRemoveConfirm] = React.useState<string | null>(null)

  const { data: variantsData, isLoading: variantsLoading } = useQuery<{ variants: Variant[] }>({
    queryKey: ["node-variants", nodeId],
    queryFn: () => apiFetch<{ variants: Variant[] }>(`/api/v1/variants?node_id=${nodeId}`),
    enabled: expanded,
    staleTime: 10000,
  })

  const { data: effectiveData } = useQuery<{ entries: EffectiveConfigEntry[] }>({
    queryKey: ["node-effective-config", nodeId],
    queryFn: () => apiFetch<{ entries: EffectiveConfigEntry[] }>(`/api/v1/nodes/${nodeId}/effective-config`),
    enabled: expanded,
    staleTime: 15000,
  })

  const addMutation = useMutation({
    mutationFn: () =>
      apiFetch<Variant>("/api/v1/variants", {
        method: "POST",
        body: JSON.stringify({
          node_id: nodeId,
          attribute_path: newPath,
          scope: newScope,
          scope_ref: newScopeRef || undefined,
          value: newValue,
        }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["node-variants", nodeId] })
      qc.invalidateQueries({ queryKey: ["node-effective-config", nodeId] })
      setAddOpen(false)
      setNewPath(""); setNewScope("node"); setNewScopeRef(""); setNewValue(""); setAddError("")
      toast({ title: "Variant added" })
    },
    onError: (err) => setAddError(String(err)),
  })

  const removeMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/api/v1/variants/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["node-variants", nodeId] })
      qc.invalidateQueries({ queryKey: ["node-effective-config", nodeId] })
      setRemoveConfirm(null)
      toast({ title: "Variant removed" })
    },
    onError: (err) => toast({ variant: "destructive", title: "Failed to remove variant", description: String(err) }),
  })

  const variants = variantsData?.variants ?? []
  const effectiveEntries = effectiveData?.entries ?? []

  const SCOPE_LABELS: Record<string, string> = { group: "Group", role: "Role", node: "Node-direct" }
  const SCOPE_BADGE: Record<string, string> = {
    group: "bg-blue-500/10 text-blue-400",
    role: "bg-purple-500/10 text-purple-400",
    node: "bg-green-500/10 text-green-400",
  }

  return (
    <div className="rounded-md border border-border" data-testid="variants-editor">
      <button
        className="flex w-full items-center justify-between px-3 py-2.5 text-left hover:bg-secondary/40 transition-colors"
        onClick={() => setExpanded((v) => !v)}
        data-testid="variants-expand"
      >
        <span className="text-sm font-medium flex items-center gap-2">
          <GitBranch className="h-3.5 w-3.5 text-muted-foreground" />
          Variants
        </span>
        <div className="flex items-center gap-2">
          {variants.length > 0 && <span className="text-xs text-muted-foreground">{variants.length} applied</span>}
          {expanded ? <ChevronDown className="h-4 w-4 text-muted-foreground" /> : <ChevronRight className="h-4 w-4 text-muted-foreground" />}
        </div>
      </button>

      {expanded && (
        <div className="border-t border-border px-3 py-3 space-y-3">
          {/* Applied variants list */}
          {variantsLoading ? (
            <div className="space-y-1.5">
              <div className="h-4 bg-muted rounded animate-pulse" />
              <div className="h-4 bg-muted rounded animate-pulse w-2/3" />
            </div>
          ) : variants.length === 0 ? (
            <p className="text-xs text-muted-foreground">No variants applied to this node.</p>
          ) : (
            <div className="space-y-1.5" data-testid="variants-list">
              {variants.map((v) => (
                <div key={v.id} className="flex items-start gap-2 text-xs rounded border border-border p-2 bg-secondary/10" data-testid={`variant-row-${v.id}`}>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-1.5 flex-wrap">
                      <span className="font-mono font-medium truncate">{v.attribute_path}</span>
                      <span className={cn("rounded px-1 py-0.5 text-[10px] font-medium shrink-0", SCOPE_BADGE[v.scope] ?? "")}>
                        {SCOPE_LABELS[v.scope] ?? v.scope}
                        {v.scope_ref ? `: ${v.scope_ref}` : ""}
                      </span>
                    </div>
                    <span className="font-mono text-muted-foreground">{v.value}</span>
                  </div>
                  {removeConfirm === v.id ? (
                    <div className="flex gap-1 shrink-0">
                      <Button
                        size="sm"
                        variant="destructive"
                        className="h-6 px-2 text-[11px]"
                        disabled={removeMutation.isPending}
                        onClick={() => removeMutation.mutate(v.id)}
                      >
                        Remove
                      </Button>
                      <Button size="sm" variant="ghost" className="h-6 px-1" onClick={() => setRemoveConfirm(null)}>
                        <X className="h-3 w-3" />
                      </Button>
                    </div>
                  ) : (
                    <button
                      className="text-muted-foreground hover:text-destructive shrink-0"
                      onClick={() => setRemoveConfirm(v.id)}
                      aria-label="Remove variant"
                      data-testid={`variant-remove-${v.id}`}
                    >
                      <X className="h-3.5 w-3.5" />
                    </button>
                  )}
                </div>
              ))}
            </div>
          )}

          {/* Effective config preview — only show if entries exist */}
          {effectiveEntries.length > 0 && (
            <details className="text-xs">
              <summary className="cursor-pointer text-muted-foreground hover:text-foreground select-none">
                Effective config ({effectiveEntries.length} attributes)
              </summary>
              <div className="mt-2 space-y-1 max-h-36 overflow-y-auto">
                {effectiveEntries.map((e) => (
                  <div key={e.attribute_path} className="flex items-start gap-2">
                    <span className="font-mono flex-1 truncate text-foreground">{e.attribute_path}</span>
                    <span className="font-mono text-muted-foreground shrink-0 text-right max-w-[120px] truncate">{e.effective_value}</span>
                  </div>
                ))}
              </div>
            </details>
          )}

          {/* Add variant form */}
          {addOpen ? (
            <div className="rounded border border-border p-3 space-y-2 bg-secondary/10 mt-2" data-testid="add-variant-form">
              <p className="text-xs font-medium text-muted-foreground">Add variant</p>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Attribute path</label>
                <Input
                  className="text-xs h-7 font-mono"
                  placeholder="kernel.cmdline"
                  value={newPath}
                  onChange={(e) => setNewPath(e.target.value)}
                  data-testid="variant-attr-path"
                />
              </div>
              <div className="grid grid-cols-2 gap-2">
                <div className="space-y-1">
                  <label className="text-xs text-muted-foreground">Scope</label>
                  <select
                    className="w-full text-xs border border-border bg-background rounded-md px-2 py-1.5"
                    value={newScope}
                    onChange={(e) => setNewScope(e.target.value as "group" | "role" | "node")}
                    data-testid="variant-scope"
                  >
                    <option value="node">Node-direct</option>
                    <option value="group">Group</option>
                    <option value="role">Role</option>
                  </select>
                </div>
                {newScope !== "node" && (
                  <div className="space-y-1">
                    <label className="text-xs text-muted-foreground">{newScope === "group" ? "Group name" : "Role name"}</label>
                    <Input
                      className="text-xs h-7 font-mono"
                      placeholder={newScope === "group" ? "compute" : "gpu"}
                      value={newScopeRef}
                      onChange={(e) => setNewScopeRef(e.target.value)}
                      data-testid="variant-scope-ref"
                    />
                  </div>
                )}
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Value</label>
                <Input
                  className="text-xs h-7 font-mono"
                  placeholder="rd.driver.pre=mlx5_core"
                  value={newValue}
                  onChange={(e) => setNewValue(e.target.value)}
                  data-testid="variant-value"
                />
              </div>
              {addError && <p className="text-xs text-destructive">{addError}</p>}
              <div className="flex gap-2">
                <Button
                  size="sm"
                  className="flex-1 text-xs"
                  disabled={!newPath || !newValue || addMutation.isPending}
                  onClick={() => addMutation.mutate()}
                  data-testid="variant-add-submit"
                >
                  {addMutation.isPending ? "Adding…" : "Add variant"}
                </Button>
                <Button size="sm" variant="ghost" className="text-xs" onClick={() => { setAddOpen(false); setAddError("") }}>
                  Cancel
                </Button>
              </div>
            </div>
          ) : (
            <Button
              size="sm"
              variant="outline"
              className="w-full text-xs"
              onClick={() => setAddOpen(true)}
              data-testid="variants-add-btn"
            >
              <Plus className="h-3 w-3 mr-1" />
              Add variant
            </Button>
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

// ─── DiskLayoutSection — Sprint 35 DISK-LAYOUT-PICKER ───────────────────────
// Shown in node Overview below HardwareSection.
// Displays the resolved effective layout source, any node-level override, and
// "Set override…" / "Clear override" actions.

function sourceLabel(source: string | undefined): string {
  if (!source) return "—"
  if (source === "node") return "node override"
  if (source === "group") return "group default"
  if (source === "image") return "image default"
  if (source.startsWith("layout_catalog:firmware_match")) return "catalog (firmware match)"
  if (source.startsWith("layout_catalog:firmware_predicate")) return "catalog (firmware predicate)"
  if (source.startsWith("layout_catalog:firmware_tag")) return "catalog (firmware tag)"
  if (source.startsWith("layout_catalog:firmware_agnostic")) return "catalog (agnostic)"
  if (source.startsWith("layout_catalog:firmware_mismatch")) return "catalog (firmware mismatch — autocorrected)"
  if (source.startsWith("layout_catalog:firmware_unknown")) return "catalog (firmware unknown)"
  if (source.startsWith("layout_catalog:node")) return "catalog (node pin)"
  if (source.startsWith("layout_catalog:group")) return "catalog (group pin)"
  if (source.startsWith("layout_catalog")) return "catalog"
  return source
}

// ─── NodeDetailPage — full-width node detail route (/nodes/$nodeId) ──────────
// Replaces the NodeSheet drawer with a full-viewport page.
// Fetches the node by ID and renders the same tabbed content.

export function NodeDetailPage() {
  const { nodeId } = useParams({ strict: false }) as { nodeId: string }
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as { reimage?: string; deleteNode?: string }
  const qc = useQueryClient()

  const { data: nodeData, isLoading, isError } = useQuery<NodeConfig>({
    queryKey: ["node", nodeId],
    queryFn: () => apiFetch<NodeConfig>(`/api/v1/nodes/${nodeId}`),
    staleTime: 10_000,
    refetchInterval: 30_000,
  })

  // Keep the nodes list cache warm so navigating back shows fresh data.
  useEventInvalidation("nodes", ["nodes"])

  const autoReimage = search.reimage === "1"
  const autoDelete = search.deleteNode === "1"

  function relativeTime(iso?: string) {
    if (!iso) return "—"
    try {
      return formatDistanceToNow(new Date(iso), { addSuffix: true })
    } catch {
      return "—"
    }
  }

  if (isLoading) {
    return (
      <div className="p-6 space-y-4">
        <div className="flex items-center gap-2">
          <Skeleton className="h-4 w-32" />
        </div>
        <div className="space-y-2">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-8 w-full" />
          ))}
        </div>
      </div>
    )
  }

  if (isError || !nodeData) {
    return (
      <div className="p-6 space-y-4">
        <Link
          to="/nodes"
          search={{ q: undefined, status: undefined, sort: undefined, dir: undefined, openNode: undefined, reimage: undefined, addNode: undefined, deleteNode: undefined, tag: undefined, view: undefined, createGroup: undefined }}
          className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors"
        >
          <ArrowLeft className="h-4 w-4" />
          Back to Nodes
        </Link>
        <p className="text-sm text-destructive">Failed to load node. It may have been deleted.</p>
      </div>
    )
  }

  const node = nodeData

  return (
    <div className="p-6 space-y-4 max-w-4xl">
      {/* Breadcrumb */}
      <Link
        to="/nodes"
        search={{ q: undefined, status: undefined, sort: undefined, dir: undefined, openNode: undefined, reimage: undefined, addNode: undefined, deleteNode: undefined, tag: undefined, view: undefined, createGroup: undefined }}
        className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors"
        data-testid="back-to-nodes"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to Nodes
      </Link>

      {/* Header */}
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold font-mono">{node.hostname || node.id}</h1>
        <StatusDot state={nodeState(node)} />
        {operatingModeLabel(node.operating_mode) && (
          <span className="inline-flex items-center rounded border border-blue-300 bg-blue-100 px-1.5 py-0.5 text-xs font-medium text-blue-800 dark:border-blue-700 dark:bg-blue-950 dark:text-blue-300">
            {operatingModeLabel(node.operating_mode)}
          </span>
        )}
      </div>

      {/* Full-width node detail — same content as NodeSheet, without the drawer chrome */}
      <NodeDetailContent
        node={node}
        qc={qc}
        advanced={false}
        relativeTime={relativeTime}
        autoReimage={autoReimage}
        autoDelete={autoDelete}
        onOperatingModeSaved={() => {
          // Invalidate the single-node query so the header badge refreshes.
          qc.invalidateQueries({ queryKey: ["node", nodeId] })
        }}
        onDeleted={() => navigate({ to: "/nodes", search: { q: undefined, status: undefined, sort: undefined, dir: undefined, openNode: undefined, reimage: undefined, addNode: undefined, deleteNode: undefined, tag: undefined, view: undefined, createGroup: undefined } })}
      />
    </div>
  )
}

// ─── NodeDetailContent ────────────────────────────────────────────────────────
// Extracted tabbed body shared by NodeDetailPage (full-page) and NodeSheet (drawer).
// Keeps both surfaces in sync without duplication.

interface NodeDetailContentProps {
  node: NodeConfig
  qc: ReturnType<typeof useQueryClient>
  advanced: boolean
  relativeTime: (iso?: string) => string
  autoReimage?: boolean
  autoDelete?: boolean
  onOperatingModeSaved?: (mode: string) => void
  onDeleted: () => void
}

function NodeDetailContent({
  node,
  qc,
  advanced,
  relativeTime,
  autoReimage,
  autoDelete,
  onOperatingModeSaved,
  onDeleted,
}: NodeDetailContentProps) {
  const [editing, setEditing] = React.useState(false)
  const [editHostname, setEditHostname] = React.useState(node.hostname)
  const [editFqdn, setEditFqdn] = React.useState(node.fqdn || "")
  const [editTags, setEditTags] = React.useState<string[]>(node.tags ?? [])
  const [editProvider, setEditProvider] = React.useState(node.provider ?? "")
  const [editRoleConfirm, setEditRoleConfirm] = React.useState("")
  const [editError, setEditError] = React.useState("")
  const [tagInput, setTagInput] = React.useState("")
  const [detailTab, setDetailTab] = React.useState<NodeDetailTab>("overview")
  const [bootSettingsOpen, setBootSettingsOpen] = React.useState(false)

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
      qc.invalidateQueries({ queryKey: ["node", node.id] })
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
    <>
      <div className="space-y-4">
        {editing ? (
          <div className="space-y-4 rounded-md border border-border p-4">
            <div className="flex items-center justify-between">
              <h3 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Editing node</h3>
              <Button size="sm" variant="ghost" className="h-7 px-2" onClick={() => { setEditing(false); setEditError(""); setEditHostname(node.hostname); setEditFqdn(node.fqdn || ""); setEditTags(node.tags ?? []); setEditProvider(node.provider ?? "") }}>
                Cancel
              </Button>
            </div>
            <EditField label="Hostname">
              <Input value={editHostname} onChange={(e) => setEditHostname(e.target.value)} className="font-mono text-xs" />
            </EditField>
            <EditField label="FQDN (optional)">
              <Input value={editFqdn} onChange={(e) => setEditFqdn(e.target.value)} className="font-mono text-xs" />
            </EditField>
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
            </div>
          </div>
        ) : (
          <Tabs value={detailTab} onValueChange={(v) => setDetailTab(v as NodeDetailTab)}>
            <div className="flex items-center justify-between gap-2 mb-4">
              <TabsList>
                <TabsTrigger value="overview" className="text-xs gap-1">
                  <Cpu className="h-3 w-3" />
                  Overview
                </TabsTrigger>
                <TabsTrigger value="sensors" className="text-xs gap-1">
                  <Activity className="h-3 w-3" />
                  Sensors
                </TabsTrigger>
                <TabsTrigger value="eventlog" className="text-xs gap-1">
                  <BookOpen className="h-3 w-3" />
                  Event Log
                </TabsTrigger>
                <TabsTrigger value="console" className="text-xs gap-1">
                  <Terminal className="h-3 w-3" />
                  Console
                </TabsTrigger>
                <TabsTrigger value="deploylog" className="text-xs gap-1">
                  <ScrollText className="h-3 w-3" />
                  Install Log
                </TabsTrigger>
                <TabsTrigger value="extstats" className="text-xs gap-1">
                  <Radio className="h-3 w-3" />
                  Ext Stats
                </TabsTrigger>
                <TabsTrigger value="ipmi" className="text-xs gap-1">
                  <Zap className="h-3 w-3" />
                  IPMI
                </TabsTrigger>
              </TabsList>
              <Button variant="ghost" size="sm" onClick={() => { setEditing(true); setEditError("") }} className="h-7 px-2 shrink-0">
                <Pencil className="h-3.5 w-3.5 mr-1" />
                Edit
              </Button>
            </div>

            <TabsContent value="overview" className="mt-0 space-y-4">
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
                <Row label="State" value={nodeState(node)} />
                <Row label="Last seen" value={relativeTime(node.last_seen_at ?? node.deploy_verified_booted_at)} />
                <Row label="Deploy complete" value={relativeTime(node.deploy_completed_preboot_at)} />
                <Row label="Verified boot" value={relativeTime(node.deploy_verified_booted_at)} />
              </Section>

              <OperatingModePicker node={node} qc={qc} onSaved={onOperatingModeSaved} />

              {node.ldap_ready !== undefined && (
                <LdapStatusSection node={node} qc={qc} />
              )}

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
                          qc.invalidateQueries({ queryKey: ["node", node.id] })
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

              <VariantsEditor nodeId={node.id} />
              <HardwareSection node={node} />
              <DiskLayoutSection node={node} />
              <SudoersSection node={node} />
              <SlurmNodeSection node={node} />
              <ReimageFlow node={node} autoExpand={autoReimage} />
              <CaptureNodeFlow node={node} />

              <div className="pt-2">
                <Button
                  variant="outline"
                  size="sm"
                  className="w-full gap-1.5 text-xs"
                  onClick={() => setBootSettingsOpen(true)}
                >
                  <Settings2 className="h-3.5 w-3.5" />
                  Change Boot Settings…
                </Button>
              </div>

              <DeleteNodeFlow node={node} autoExpand={autoDelete} onDeleted={onDeleted} />
            </TabsContent>

            <TabsContent value="sensors" className="mt-2">
              <SensorsTab nodeId={node.id} />
            </TabsContent>
            <TabsContent value="eventlog" className="mt-2">
              <EventLogTab nodeId={node.id} />
            </TabsContent>
            <TabsContent value="console" className="mt-2">
              <ConsoleTab nodeId={node.id} />
            </TabsContent>
            <TabsContent value="deploylog" className="mt-2">
              <DeployLogTab nodeId={node.id} primaryMac={node.primary_mac} />
            </TabsContent>
            <TabsContent value="extstats" className="mt-2">
              <ExternalStatsTab nodeId={node.id} />
            </TabsContent>
            <TabsContent value="ipmi" className="mt-2">
              <IpmiTab nodeId={node.id} />
            </TabsContent>
          </Tabs>
        )}
      </div>

      <BootSettingsModal
        open={bootSettingsOpen}
        onClose={() => setBootSettingsOpen(false)}
        node={node}
      />
    </>
  )
}

function DiskLayoutSection({ node }: { node: NodeConfig }) {
  const qc = useQueryClient()
  const [pickerOpen, setPickerOpen] = React.useState(false)

  const { data: effectiveData, isLoading: effectiveLoading } = useQuery<EffectiveLayoutResponse>({
    queryKey: ["effective-layout", node.id],
    queryFn: () => apiFetch<EffectiveLayoutResponse>(`/api/v1/nodes/${node.id}/effective-layout`),
    staleTime: 30_000,
    retry: false,
  })

  const hasOverride = effectiveData?.source === "node"

  const clearOverride = useMutation({
    mutationFn: () =>
      apiFetch(`/api/v1/nodes/${node.id}/layout-override`, {
        method: "PUT",
        body: JSON.stringify({ clear_layout_override: true }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["effective-layout", node.id] })
      qc.invalidateQueries({ queryKey: ["nodes"] })
    },
    onError: (err) => toast({ variant: "destructive", title: "Failed to clear override", description: String(err) }),
  })

  const partitions = effectiveData?.layout?.partitions ?? []

  return (
    <>
      <Section title="Disk Layout">
        {effectiveLoading && (
          <div className="space-y-1">
            <div className="h-4 w-2/3 rounded bg-muted animate-pulse" />
            <div className="h-4 w-1/2 rounded bg-muted animate-pulse" />
          </div>
        )}

        {!effectiveLoading && effectiveData && (
          <>
            <Row
              label="Effective source"
              value={sourceLabel(effectiveData.source)}
            />
            {partitions.length > 0 && (
              <Row
                label="Partitions"
                value={
                  partitions.map((p) => (p as { mountpoint?: string; fs?: string }).mountpoint ?? (p as { mountpoint?: string; fs?: string }).fs)
                    .filter(Boolean)
                    .join(", ") || `${partitions.length} partition(s)`
                }
              />
            )}
            {node.detected_firmware && (
              <div className="flex items-start justify-between gap-4 text-sm">
                <span className="text-muted-foreground shrink-0">Firmware</span>
                <FirmwareBadge kind={node.detected_firmware as "bios" | "uefi" | "any"} />
              </div>
            )}
            {hasOverride && (
              <div className="flex items-center justify-between pt-1">
                <span className="text-xs text-amber-600 font-medium">Node-level override active</span>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 text-xs text-destructive hover:text-destructive"
                  onClick={() => clearOverride.mutate()}
                  disabled={clearOverride.isPending}
                >
                  {clearOverride.isPending ? "Clearing…" : "Clear override"}
                </Button>
              </div>
            )}
          </>
        )}

        {!effectiveLoading && !effectiveData && (
          <p className="text-xs text-muted-foreground">No layout resolved (node has no image assigned).</p>
        )}

        <div className="pt-2">
          <Button
            variant="outline"
            size="sm"
            className="w-full gap-1.5 text-xs"
            onClick={() => setPickerOpen(true)}
          >
            <HardDrive className="h-3.5 w-3.5" />
            Set override…
          </Button>
        </div>
      </Section>

      <DiskLayoutPicker
        nodeId={node.id}
        nodeFirmware={node.detected_firmware}
        open={pickerOpen}
        onClose={() => setPickerOpen(false)}
      />
    </>
  )
}
