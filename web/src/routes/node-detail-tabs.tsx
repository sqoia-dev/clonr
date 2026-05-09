// node-detail-tabs.tsx — Sensors, Event Log, Console, Deploy Log, and IPMI tabs for the node Sheet (#152)
//
// Five independent components, each mounted only when their tab is active:
//   <SensorsTab nodeId> — recharts sparklines + sensor table from /api/v1/nodes/{id}/stats
//   <EventLogTab nodeId> — SEL list from /api/v1/nodes/{id}/sel with level/regex/head-tail toolbar
//   <ConsoleTab nodeId> — xterm.js terminal over WS /api/v1/console/{node_id}
//   <DeployLogTab nodeId primaryMac> — live SSE log from GET /api/v1/logs/stream?component=deploy&node_mac=<mac>
//   <IpmiTab nodeId> — IPMI panel: power controls, sensor pull, SEL viewer (Sprint 34 UI B)

import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import {
  ResponsiveContainer,
  LineChart,
  Line,
  XAxis,
  YAxis,
  Tooltip as RechartsTooltip,
} from "recharts"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"
import { RefreshCw, Trash2, AlertTriangle, ChevronDown, ChevronRight, WifiOff, Power, Zap } from "lucide-react"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { apiFetch, sseUrl, wsUrl } from "@/lib/api"
import { sseReconnectDelay } from "@/lib/sse-backoff"
import { cn } from "@/lib/utils"
import { toast } from "@/hooks/use-toast"

// ─── Types ────────────────────────────────────────────────────────────────────

interface StatSample {
  plugin: string
  sensor: string
  value: number
  unit?: string
  labels?: Record<string, string>
  ts: number // Unix seconds
}

interface SELEntry {
  id: string
  date: string
  time: string
  sensor: string
  event: string
  severity: string
  raw: string
  timestamp: string // ISO string from Go time.Time
}

interface SELResponse {
  node_id: string
  entries: SELEntry[]
  last_checked: string
}

// ─── Sensors Tab ──────────────────────────────────────────────────────────────

const OVERVIEW_PLUGINS = [
  { plugin: "cpu",    sensor: "load1",      label: "CPU Load",       unit: "" },
  { plugin: "memory", sensor: "used_pct",   label: "Mem Used",       unit: "%" },
  { plugin: "disks",  sensor: "read_bytes", label: "Disk Read",      unit: "B/s" },
  { plugin: "net",    sensor: "rx_bytes",   label: "Net RX",         unit: "B/s" },
]

// ALL_PLUGINS defines the order and display names for the sensor table.
const ALL_PLUGINS = [
  "cpu", "memory", "disks", "md", "net", "system",
  "nvme", "infiniband", "firmware", "nvidia", "megaraid", "zfs", "ntp",
]

function formatValue(value: number, unit?: string): string {
  if (!unit) return value.toFixed(2)
  const u = unit.toLowerCase()
  if (u === "b/s" || u === "bytes") {
    if (value >= 1e9) return `${(value / 1e9).toFixed(1)} GB/s`
    if (value >= 1e6) return `${(value / 1e6).toFixed(1)} MB/s`
    if (value >= 1e3) return `${(value / 1e3).toFixed(1)} KB/s`
    return `${value.toFixed(0)} B/s`
  }
  return `${value.toFixed(2)}${unit ? " " + unit : ""}`
}

function SparkCard({
  label,
  data,
  unit,
  isLoading,
}: {
  label: string
  data: StatSample[]
  unit: string
  isLoading: boolean
}) {
  const current = data.length > 0 ? data[data.length - 1].value : null
  const chartData = data.map((s) => ({ ts: s.ts, v: s.value }))

  return (
    <div className="rounded-md border border-border bg-card p-3 flex flex-col gap-1">
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">{label}</span>
        {current !== null && (
          <span className="text-sm font-mono font-medium tabular-nums">
            {formatValue(current, unit)}
          </span>
        )}
      </div>
      {isLoading ? (
        <div className="h-12 flex items-center justify-center text-xs text-muted-foreground">Loading…</div>
      ) : data.length === 0 ? (
        <div className="h-12 flex items-center justify-center text-xs text-muted-foreground">No data</div>
      ) : (
        <ResponsiveContainer width="100%" height={48}>
          <LineChart data={chartData} margin={{ top: 2, right: 2, bottom: 2, left: 2 }}>
            <XAxis dataKey="ts" hide />
            <YAxis hide />
            <RechartsTooltip
              content={({ active, payload }) => {
                if (!active || !payload?.length) return null
                const v = payload[0].value as number
                return (
                  <div className="rounded bg-popover border border-border px-2 py-1 text-xs shadow">
                    {formatValue(v, unit)}
                  </div>
                )
              }}
            />
            <Line
              type="monotone"
              dataKey="v"
              dot={false}
              strokeWidth={1.5}
              stroke="hsl(var(--primary))"
              isAnimationActive={false}
            />
          </LineChart>
        </ResponsiveContainer>
      )}
    </div>
  )
}

interface PluginGroupProps {
  plugin: string
  rows: StatSample[]
}

function PluginGroup({ plugin, rows }: PluginGroupProps) {
  const [open, setOpen] = React.useState(true)

  // Deduplicate to latest value per sensor+labels key.
  const latest = React.useMemo(() => {
    const map = new Map<string, StatSample>()
    for (const r of rows) {
      const key = r.sensor + JSON.stringify(r.labels ?? {})
      const existing = map.get(key)
      if (!existing || r.ts > existing.ts) map.set(key, r)
    }
    return [...map.values()].sort((a, b) => a.sensor.localeCompare(b.sensor))
  }, [rows])

  if (latest.length === 0) return null

  return (
    <div className="border border-border rounded-md overflow-hidden">
      <button
        className="w-full flex items-center gap-2 px-3 py-2 text-xs font-medium bg-secondary/30 hover:bg-secondary/60 transition-colors"
        onClick={() => setOpen((o) => !o)}
      >
        {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        <span className="font-mono">{plugin}</span>
        <span className="text-muted-foreground ml-auto">{latest.length} sensors</span>
      </button>
      {open && (
        <table className="w-full text-xs">
          <tbody>
            {latest.map((s, i) => (
              <tr
                key={i}
                className={cn(
                  "border-t border-border",
                  i === 0 && "border-t-0",
                )}
              >
                <td className="px-3 py-1.5 font-mono text-muted-foreground">{s.sensor}</td>
                {s.labels && Object.keys(s.labels).length > 0 && (
                  <td className="px-2 py-1.5 text-muted-foreground/60 font-mono text-[10px]">
                    {Object.entries(s.labels).map(([k, v]) => `${k}=${v}`).join(" ")}
                  </td>
                )}
                <td className="px-3 py-1.5 text-right font-mono tabular-nums">
                  {formatValue(s.value, s.unit)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

export function SensorsTab({ nodeId }: { nodeId: string }) {
  const nowSec = () => Math.floor(Date.now() / 1000)

  // Fetch all plugins for the sensor table (no plugin filter).
  const { data: allData, isLoading: allLoading, refetch, dataUpdatedAt } = useQuery<StatSample[]>({
    queryKey: ["node-stats-all", nodeId],
    queryFn: () =>
      apiFetch<StatSample[]>(`/api/v1/nodes/${nodeId}/stats?since=${nowSec() - 3600}`),
    refetchInterval: 5000,
    staleTime: 4000,
  })

  // Fetch per-plugin sparkline data.
  const sparkQueries = OVERVIEW_PLUGINS.map(({ plugin, sensor }) => ({
    plugin,
    sensor,
    data: (allData ?? []).filter((s) => s.plugin === plugin && s.sensor === sensor),
  }))

  // Group all data by plugin for the table.
  const byPlugin = React.useMemo(() => {
    const map = new Map<string, StatSample[]>()
    for (const s of allData ?? []) {
      if (!map.has(s.plugin)) map.set(s.plugin, [])
      map.get(s.plugin)!.push(s)
    }
    return map
  }, [allData])

  // STAT-REGISTRY: group samples that carry chart_group metadata.
  const byChartGroup = React.useMemo(
    () => groupSamplesByChartGroup((allData ?? []) as StatSampleWithMeta[]),
    [allData],
  )
  const hasChartGroups = byChartGroup.size > 0

  const lastUpdated = dataUpdatedAt ? new Date(dataUpdatedAt).toLocaleTimeString() : null

  return (
    <div className="space-y-4 py-2">
      {/* Top row: 4 sparkline cards */}
      <div className="grid grid-cols-2 gap-2">
        {sparkQueries.map(({ plugin, sensor, data }) => {
          const meta = OVERVIEW_PLUGINS.find((p) => p.plugin === plugin && p.sensor === sensor)!
          return (
            <SparkCard
              key={`${plugin}-${sensor}`}
              label={meta.label}
              data={data}
              unit={meta.unit}
              isLoading={allLoading}
            />
          )
        })}
      </div>

      {/* Toolbar */}
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">
          All sensors — last 1h
          {lastUpdated && <span className="ml-2 opacity-60">updated {lastUpdated}</span>}
        </span>
        <Button
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-xs"
          onClick={() => refetch()}
        >
          <RefreshCw className="h-3 w-3 mr-1" />
          Refresh
        </Button>
      </div>

      {/* Sensor table — chart-group view when STAT-REGISTRY metadata present, else plugin view */}
      {allLoading ? (
        <div className="text-xs text-muted-foreground py-4 text-center">Loading sensors…</div>
      ) : (allData ?? []).length === 0 ? (
        <div className="text-xs text-muted-foreground py-4 text-center">
          No stats data. The stats agent must be running on this node and reporting to the server.
        </div>
      ) : hasChartGroups ? (
        <div className="space-y-2">
          {[...byChartGroup.entries()].map(([group, samples]) => (
            <ChartGroupCard key={group} group={group} samples={samples} />
          ))}
          {/* Also render any plugin groups whose samples lack chart_group */}
          {[...byPlugin.keys()].map((plugin) => {
            const rows = byPlugin.get(plugin)!.filter((s) => !(s as StatSampleWithMeta).chart_group)
            if (rows.length === 0) return null
            return <PluginGroup key={plugin} plugin={plugin} rows={rows} />
          })}
        </div>
      ) : (
        <div className="space-y-2">
          {ALL_PLUGINS.map((plugin) => {
            const rows = byPlugin.get(plugin)
            if (!rows || rows.length === 0) return null
            return <PluginGroup key={plugin} plugin={plugin} rows={rows} />
          })}
          {/* Any plugin not in ALL_PLUGINS list */}
          {[...byPlugin.keys()]
            .filter((p) => !ALL_PLUGINS.includes(p))
            .map((plugin) => (
              <PluginGroup key={plugin} plugin={plugin} rows={byPlugin.get(plugin)!} />
            ))}
        </div>
      )}
    </div>
  )
}

// ─── Event Log Tab ────────────────────────────────────────────────────────────

type SELLevel = "all" | "info" | "warn" | "critical"
type SELSlice = "all" | "head10" | "tail10"

const SEV_COLORS: Record<string, string> = {
  critical: "bg-destructive/20 text-destructive border-destructive/30",
  warn:     "bg-status-warning/20 text-status-warning border-status-warning/30",
  info:     "bg-secondary text-muted-foreground border-border",
}

function SeverityPill({ severity }: { severity: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide",
        SEV_COLORS[severity] ?? SEV_COLORS.info,
      )}
    >
      {severity}
    </span>
  )
}

export function EventLogTab({ nodeId }: { nodeId: string }) {
  const [level, setLevel] = React.useState<SELLevel>("all")
  const [regex, setRegex] = React.useState("")
  const [slice, setSlice] = React.useState<SELSlice>("all")
  const [clearOpen, setClearOpen] = React.useState(false)
  const [clearConfirm, setClearConfirm] = React.useState("")

  // Build query params.
  const params = new URLSearchParams()
  if (level !== "all") params.set("level", level)
  if (slice === "head10") params.set("head", "10")
  if (slice === "tail10") params.set("tail", "10")
  const paramStr = params.toString()

  const { data, isLoading, isFetching, refetch, error } = useQuery<SELResponse>({
    queryKey: ["node-sel", nodeId, level, slice],
    queryFn: () =>
      apiFetch<SELResponse>(`/api/v1/nodes/${nodeId}/sel${paramStr ? "?" + paramStr : ""}`),
    staleTime: 10000,
  })

  const clearMutation = useMutation({
    mutationFn: () =>
      apiFetch(`/api/v1/nodes/${nodeId}/sel/clear`, { method: "POST" }),
    onSuccess: () => {
      toast({ title: "SEL cleared", description: "All entries erased from BMC." })
      setClearOpen(false)
      setClearConfirm("")
      refetch()
    },
    onError: (err) => {
      toast({ variant: "destructive", title: "Clear failed", description: String(err) })
    },
  })

  // Client-side regex filter (server already did level + head/tail).
  const entries = React.useMemo(() => {
    const raw = data?.entries ?? []
    if (!regex.trim()) return raw
    try {
      const re = new RegExp(regex, "i")
      return raw.filter((e) => re.test(e.sensor) || re.test(e.event) || re.test(e.raw))
    } catch {
      return raw
    }
  }, [data, regex])

  return (
    <div className="space-y-3 py-2">
      {/* Toolbar */}
      <div className="flex flex-wrap gap-2 items-center">
        <select
          className="text-xs border border-border rounded px-2 py-1 bg-background"
          value={level}
          onChange={(e) => setLevel(e.target.value as SELLevel)}
        >
          <option value="all">All levels</option>
          <option value="info">Info+</option>
          <option value="warn">Warn+</option>
          <option value="critical">Critical only</option>
        </select>

        <Input
          className="h-7 text-xs w-36 font-mono"
          placeholder="regex filter…"
          value={regex}
          onChange={(e) => setRegex(e.target.value)}
        />

        <select
          className="text-xs border border-border rounded px-2 py-1 bg-background"
          value={slice}
          onChange={(e) => setSlice(e.target.value as SELSlice)}
        >
          <option value="all">All</option>
          <option value="head10">Head 10</option>
          <option value="tail10">Tail 10</option>
        </select>

        <Button
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-xs"
          onClick={() => refetch()}
          disabled={isFetching}
        >
          <RefreshCw className={cn("h-3 w-3 mr-1", isFetching && "animate-spin")} />
          Refresh
        </Button>

        <Button
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-xs text-destructive hover:text-destructive ml-auto"
          onClick={() => setClearOpen(true)}
        >
          <Trash2 className="h-3 w-3 mr-1" />
          Clear SEL…
        </Button>
      </div>

      {/* Clear SEL confirmation modal */}
      {clearOpen && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 space-y-2">
          <div className="flex items-start gap-2">
            <AlertTriangle className="h-4 w-4 text-destructive mt-0.5 shrink-0" />
            <p className="text-xs text-destructive font-medium">
              Clear all SEL entries? This cannot be undone.
            </p>
          </div>
          <p className="text-xs text-muted-foreground">
            Type <code className="font-mono bg-secondary px-1 rounded">clear</code> to confirm:
          </p>
          <Input
            className="h-7 text-xs font-mono"
            placeholder="clear"
            value={clearConfirm}
            onChange={(e) => setClearConfirm(e.target.value)}
          />
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="destructive"
              className="text-xs"
              disabled={clearConfirm !== "clear" || clearMutation.isPending}
              onClick={() => clearMutation.mutate()}
            >
              {clearMutation.isPending ? "Clearing…" : "Clear SEL"}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="text-xs"
              onClick={() => { setClearOpen(false); setClearConfirm("") }}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}

      {/* Entry count */}
      {!isLoading && !error && (
        <p className="text-xs text-muted-foreground">
          {entries.length} {entries.length === 1 ? "entry" : "entries"}
          {regex && " (filtered)"}
        </p>
      )}

      {/* SEL list */}
      {isLoading ? (
        <div className="text-xs text-muted-foreground py-4 text-center">Loading SEL…</div>
      ) : error ? (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-xs text-destructive">
          {String(error)}
        </div>
      ) : entries.length === 0 ? (
        <div className="text-xs text-muted-foreground py-4 text-center">No entries</div>
      ) : (
        <div
          className="rounded-md border border-border overflow-auto"
          style={{ maxHeight: "400px", contentVisibility: "auto" }}
        >
          <table className="w-full text-xs">
            <thead className="sticky top-0 bg-secondary/80 backdrop-blur-sm">
              <tr>
                <th className="text-left px-3 py-1.5 font-medium text-muted-foreground w-10">#</th>
                <th className="text-left px-2 py-1.5 font-medium text-muted-foreground">Timestamp</th>
                <th className="text-left px-2 py-1.5 font-medium text-muted-foreground">Sensor</th>
                <th className="text-left px-2 py-1.5 font-medium text-muted-foreground">Event</th>
                <th className="text-left px-2 py-1.5 font-medium text-muted-foreground w-16">Level</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e, i) => (
                <tr
                  key={e.id + "-" + i}
                  className={cn(
                    "border-t border-border",
                    e.severity === "critical" && "bg-destructive/5",
                    e.severity === "warn" && "bg-status-warning/5",
                  )}
                >
                  <td className="px-3 py-1.5 font-mono text-muted-foreground">{e.id}</td>
                  <td className="px-2 py-1.5 font-mono text-muted-foreground whitespace-nowrap">
                    {e.date} {e.time}
                  </td>
                  <td className="px-2 py-1.5 font-mono">{e.sensor}</td>
                  <td className="px-2 py-1.5">{e.event}</td>
                  <td className="px-2 py-1.5">
                    <SeverityPill severity={e.severity} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// ─── Console Tab ──────────────────────────────────────────────────────────────

type ConsoleMode = "ipmi-sol" | "ssh"

// CONSOLE_RECONNECT_MAX_ATTEMPTS caps auto-reconnect attempts before giving up.
const CONSOLE_RECONNECT_MAX_ATTEMPTS = 8

export function ConsoleTab({ nodeId }: { nodeId: string }) {
  const [mode, setMode] = React.useState<ConsoleMode>("ipmi-sol")
  const [connected, setConnected] = React.useState(false)
  const [connecting, setConnecting] = React.useState(false)
  const [disconnected, setDisconnected] = React.useState(false)
  const [errorMsg, setErrorMsg] = React.useState("")
  const [attemptCount, setAttemptCount] = React.useState(0)

  const termContainerRef = React.useRef<HTMLDivElement>(null)
  const termRef = React.useRef<Terminal | null>(null)
  const fitAddonRef = React.useRef<FitAddon | null>(null)
  const wsRef = React.useRef<WebSocket | null>(null)
  const reconnectTimerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)
  const mountedRef = React.useRef(true)
  const attemptRef = React.useRef(0)

  // Track mode changes — reconnect if already connected.
  const prevModeRef = React.useRef<ConsoleMode>(mode)

  React.useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      cleanupWS()
      if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current)
    }
  }, [])

  // Initialize xterm.js once on mount.
  React.useEffect(() => {
    if (!termContainerRef.current) return
    if (termRef.current) return // already initialised

    const term = new Terminal({
      cursorBlink: true,
      fontFamily: "'JetBrains Mono Variable', 'Cascadia Code', monospace",
      fontSize: 13,
      theme: {
        background: "#09090b", // zinc-950 to match dark theme
        foreground: "#e4e4e7",
        cursor: "#a1a1aa",
      },
      scrollback: 2000,
      convertEol: true,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(termContainerRef.current)
    fit.fit()
    termRef.current = term
    fitAddonRef.current = fit

    // Fit on container resize.
    const ro = new ResizeObserver(() => { fit.fit() })
    if (termContainerRef.current) ro.observe(termContainerRef.current)
    return () => ro.disconnect()
  }, [])

  function cleanupWS() {
    if (wsRef.current) {
      wsRef.current.onopen = null
      wsRef.current.onmessage = null
      wsRef.current.onclose = null
      wsRef.current.onerror = null
      wsRef.current.close()
      wsRef.current = null
    }
  }

  function connect(selectedMode: ConsoleMode, attempt: number) {
    if (!mountedRef.current) return
    setConnecting(true)
    setErrorMsg("")
    setDisconnected(false)

    const url = wsUrl(`/api/v1/console/${nodeId}?mode=${selectedMode}`)
    const ws = new WebSocket(url)
    wsRef.current = ws

    ws.onopen = () => {
      if (!mountedRef.current) { ws.close(); return }
      attemptRef.current = 0
      setAttemptCount(0)
      setConnected(true)
      setConnecting(false)
      setErrorMsg("")
      termRef.current?.writeln("\r\n\x1b[32m[connected]\x1b[0m\r\n")
      fitAddonRef.current?.fit()
    }

    ws.onmessage = (ev) => {
      if (!mountedRef.current) return
      const data: string | ArrayBuffer = ev.data
      if (typeof data === "string") {
        // Check for exit control frame.
        if (data.startsWith("{") && data.includes('"type":"exit"')) {
          try {
            const msg = JSON.parse(data) as { type: string; code: number; error?: string }
            if (msg.type === "exit") {
              termRef.current?.writeln(`\r\n\x1b[33m[session ended: exit ${msg.code}${msg.error ? " — " + msg.error : ""}]\x1b[0m`)
              return
            }
          } catch {
            // Not a control frame — fall through to write as-is.
          }
        }
        termRef.current?.write(data)
      } else {
        // Binary frame — write as Uint8Array.
        termRef.current?.write(new Uint8Array(data))
      }
    }

    ws.onclose = (ev) => {
      if (!mountedRef.current) return
      setConnected(false)
      setConnecting(false)
      const nextAttempt = attempt + 1
      attemptRef.current = nextAttempt
      setAttemptCount(nextAttempt)

      if (ev.wasClean) {
        setDisconnected(true)
        termRef.current?.writeln("\r\n\x1b[33m[disconnected]\x1b[0m")
        return
      }

      if (nextAttempt > CONSOLE_RECONNECT_MAX_ATTEMPTS) {
        setDisconnected(true)
        setErrorMsg(`Lost connection after ${CONSOLE_RECONNECT_MAX_ATTEMPTS} reconnect attempts.`)
        termRef.current?.writeln(`\r\n\x1b[31m[reconnect limit reached]\x1b[0m`)
        return
      }

      const delay = sseReconnectDelay(nextAttempt)
      termRef.current?.writeln(`\r\n\x1b[33m[reconnecting in ${(delay / 1000).toFixed(1)}s (attempt ${nextAttempt})]\x1b[0m`)
      reconnectTimerRef.current = setTimeout(() => {
        if (mountedRef.current) connect(selectedMode, nextAttempt)
      }, delay)
    }

    ws.onerror = () => {
      termRef.current?.writeln("\r\n\x1b[31m[WebSocket error]\x1b[0m")
    }

    // xterm → WebSocket: send keystrokes.
    termRef.current?.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(data)
      }
    })
  }

  function handleConnect() {
    cleanupWS()
    if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current)
    attemptRef.current = 0
    setAttemptCount(0)
    connect(mode, 0)
  }

  function handleDisconnect() {
    if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current)
    cleanupWS()
    setConnected(false)
    setConnecting(false)
    setDisconnected(true)
    termRef.current?.writeln("\r\n\x1b[33m[disconnected by user]\x1b[0m")
  }

  // When mode changes while connected, reconnect.
  React.useEffect(() => {
    if (prevModeRef.current === mode) return
    prevModeRef.current = mode
    if (connected || connecting) {
      handleDisconnect()
    }
  })

  return (
    <div className="flex flex-col gap-3 py-2" style={{ height: "100%" }}>
      {/* Mode selector + controls */}
      <div className="flex items-center gap-3 flex-wrap">
        <div className="flex items-center gap-1 rounded-md border border-border overflow-hidden">
          <button
            className={cn(
              "px-3 py-1 text-xs transition-colors",
              mode === "ipmi-sol"
                ? "bg-primary text-primary-foreground"
                : "bg-background text-muted-foreground hover:bg-secondary",
            )}
            onClick={() => setMode("ipmi-sol")}
          >
            IPMI SOL
          </button>
          <button
            className={cn(
              "px-3 py-1 text-xs transition-colors border-l border-border",
              mode === "ssh"
                ? "bg-primary text-primary-foreground"
                : "bg-background text-muted-foreground hover:bg-secondary",
            )}
            onClick={() => setMode("ssh")}
          >
            SSH
          </button>
        </div>

        {!connected && !connecting && (
          <Button size="sm" className="h-7 text-xs" onClick={handleConnect}>
            Connect
          </Button>
        )}
        {(connected || connecting) && (
          <Button
            size="sm"
            variant="outline"
            className="h-7 text-xs text-destructive border-destructive/30 hover:bg-destructive/10"
            onClick={handleDisconnect}
          >
            Disconnect
          </Button>
        )}

        <div className="flex items-center gap-1.5 ml-auto">
          {connecting && (
            <span className="text-xs text-muted-foreground animate-pulse">Connecting…</span>
          )}
          {connected && (
            <span className="flex items-center gap-1 text-xs text-status-healthy">
              <span className="h-1.5 w-1.5 rounded-full bg-status-healthy" />
              Connected
            </span>
          )}
          {disconnected && !connecting && !connected && (
            <span className="text-xs text-muted-foreground">Disconnected</span>
          )}
          {attemptCount > 0 && !connected && !disconnected && (
            <span className="text-xs text-status-warning">
              Attempt {attemptCount}…
            </span>
          )}
        </div>
      </div>

      {errorMsg && (
        <div className="rounded border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
          {errorMsg}
        </div>
      )}

      {/* xterm.js container */}
      <div
        ref={termContainerRef}
        className="rounded-md border border-border overflow-hidden bg-[#09090b]"
        style={{ minHeight: "320px", flex: 1 }}
      />

      <p className="text-[10px] text-muted-foreground/60">
        Copy: Ctrl+Shift+C (Linux/Win) / Cmd+C (Mac) &nbsp;·&nbsp; Paste: Ctrl+Shift+V / Cmd+V
      </p>
    </div>
  )
}

// ─── Deploy Log Tab (STREAM-LOG-UI) ──────────────────────────────────────────
//
// Streams install-log entries from GET /api/v1/logs/stream?component=deploy&node_mac=<mac>.
// The server emits newline-delimited JSON; each line is a LogEntry.
// Auto-reconnects on disconnect using sseReconnectDelay jitter.
// Auto-scrolls to newest unless the operator has scrolled up (pause-on-scroll-up).
// Phase filter chips hide noise from completed phases.
// Max 5000 rows kept in memory; oldest fall off automatically.

// LogEntry mirrors pkg/api/types.go LogEntry (+ STREAM-LOG-PHASE adds `phase`).
export interface LogEntry {
  id?: string
  node_mac?: string
  component?: string
  level: string
  message: string
  timestamp?: number  // Unix ms — matches pkg/api/types.go `json:"timestamp"`
  ts?: number         // legacy alias — do not use in new code
  phase?: string     // STREAM-LOG-PHASE field — may be absent before that ships
}

const MAX_LOG_ROWS = 5000

// DEPLOY_LOG_RECONNECT_MAX caps auto-reconnect before giving up.
const DEPLOY_LOG_RECONNECT_MAX = 12

// Phase colour palette — stable mapping so colours don't shift between renders.
const PHASE_COLORS: Record<string, string> = {
  preflight:    "bg-sky-500/20    text-sky-400    border-sky-500/30",
  partitioning: "bg-violet-500/20 text-violet-400 border-violet-500/30",
  formatting:   "bg-purple-500/20 text-purple-400 border-purple-500/30",
  mount:        "bg-indigo-500/20 text-indigo-400 border-indigo-500/30",
  downloading:  "bg-blue-500/20   text-blue-400   border-blue-500/30",
  extracting:   "bg-cyan-500/20   text-cyan-400   border-cyan-500/30",
  chroot:       "bg-teal-500/20   text-teal-400   border-teal-500/30",
  bootloader:   "bg-amber-500/20  text-amber-400  border-amber-500/30",
  dracut:       "bg-orange-500/20 text-orange-400 border-orange-500/30",
  finalizing:   "bg-lime-500/20   text-lime-400   border-lime-500/30",
  phonehome:    "bg-green-500/20  text-green-400  border-green-500/30",
  deploy:       "bg-emerald-500/20 text-emerald-400 border-emerald-500/30",
}

function phaseBadgeClass(phase: string | undefined): string {
  if (!phase) return "bg-secondary text-muted-foreground border-border"
  return PHASE_COLORS[phase.toLowerCase()] ?? "bg-secondary text-muted-foreground border-border"
}

const LEVEL_COLORS: Record<string, string> = {
  error: "text-destructive",
  warn:  "text-status-warning",
  fatal: "text-destructive font-semibold",
  debug: "text-muted-foreground/60",
}

function levelClass(level: string): string {
  return LEVEL_COLORS[level.toLowerCase()] ?? "text-foreground"
}

function formatLogTs(tsMs: number): string {
  const d = new Date(tsMs)
  const hh = String(d.getHours()).padStart(2, "0")
  const mm = String(d.getMinutes()).padStart(2, "0")
  const ss = String(d.getSeconds()).padStart(2, "0")
  const ms = String(d.getMilliseconds()).padStart(3, "0")
  return `${hh}:${mm}:${ss}.${ms}`
}

interface DeployLogTabProps {
  nodeId: string
  primaryMac: string
}

export function DeployLogTab({ nodeId: _nodeId, primaryMac }: DeployLogTabProps) {
  const [entries, setEntries] = React.useState<LogEntry[]>([])
  const [connected, setConnected] = React.useState(false)
  const [attempts, setAttempts] = React.useState(0)
  const [failed, setFailed] = React.useState(false)
  const [phaseFilter, setPhaseFilter] = React.useState<string | null>(null)
  // hasWarnOrError: tab-level indicator dot — turns true when any warn/error arrives.
  const [hasWarnOrError, setHasWarnOrError] = React.useState(false)

  // Scroll control: userScrolledUp suppresses auto-scroll.
  const scrollRef = React.useRef<HTMLDivElement>(null)
  const userScrolledUpRef = React.useRef(false)
  const mountedRef = React.useRef(true)
  const esRef = React.useRef<EventSource | null>(null)
  const reconnectTimerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)
  // attemptRef keeps the latest attempt count stable inside the closure.
  const attemptRef = React.useRef(0)

  // Deduplicate by id when the server provides one, otherwise accept all.
  const seenIdsRef = React.useRef<Set<string>>(new Set())

  React.useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      cleanup()
      if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function cleanup() {
    if (esRef.current) {
      esRef.current.onopen = null
      esRef.current.onerror = null
      esRef.current.onmessage = null
      esRef.current.close()
      esRef.current = null
    }
  }

  // Connect / reconnect whenever primaryMac changes (node switched).
  React.useEffect(() => {
    cleanup()
    if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current)
    setEntries([])
    seenIdsRef.current.clear()
    setConnected(false)
    setFailed(false)
    setAttempts(0)
    attemptRef.current = 0
    userScrolledUpRef.current = false
    // Reset per-node UI state — stale phase filter + warn badge from a previous
    // node must not persist when the user navigates to a different node.
    setPhaseFilter(null)
    setHasWarnOrError(false)

    connect(0)

    return () => {
      cleanup()
      if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [primaryMac])

  function connect(_attempt: number) {
    if (!mountedRef.current) return

    // Server reads query param `mac` (not `node_mac`) — see internal/server/handlers/logs.go StreamLogs.
    const path = `/api/v1/logs/stream?component=deploy&mac=${encodeURIComponent(primaryMac)}`
    const es = new EventSource(sseUrl(path), { withCredentials: true })
    esRef.current = es

    es.onopen = () => {
      if (!mountedRef.current) { es.close(); return }
      attemptRef.current = 0
      setAttempts(0)
      setConnected(true)
      setFailed(false)
    }

    es.onmessage = (ev) => {
      if (!mountedRef.current) return
      try {
        const entry = JSON.parse(ev.data) as LogEntry
        // Deduplicate if the server provides an id.
        if (entry.id) {
          if (seenIdsRef.current.has(entry.id)) return
          seenIdsRef.current.add(entry.id)
        }
        if (entry.level === "warn" || entry.level === "error" || entry.level === "fatal") {
          setHasWarnOrError(true)
        }
        setEntries((prev) => {
          const next = [...prev, entry]
          // Evict oldest when over the cap.
          return next.length > MAX_LOG_ROWS ? next.slice(next.length - MAX_LOG_ROWS) : next
        })
      } catch {
        // malformed line — ignore
      }
    }

    es.onerror = () => {
      if (!mountedRef.current) return
      setConnected(false)
      es.close()
      esRef.current = null

      const nextAttempt = attemptRef.current + 1
      attemptRef.current = nextAttempt
      setAttempts(nextAttempt)

      if (nextAttempt > DEPLOY_LOG_RECONNECT_MAX) {
        setFailed(true)
        return
      }

      const delay = sseReconnectDelay(nextAttempt)
      reconnectTimerRef.current = setTimeout(() => {
        if (mountedRef.current) connect(nextAttempt)
      }, delay)
    }
  }

  // Auto-scroll to bottom — only when user hasn't scrolled up.
  React.useEffect(() => {
    if (userScrolledUpRef.current) return
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [entries])

  function handleScroll() {
    const el = scrollRef.current
    if (!el) return
    // If user is within 40px of the bottom, re-enable auto-scroll.
    const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
    userScrolledUpRef.current = distFromBottom > 40
  }

  // Collect distinct phases present in the current entry set.
  const presentPhases = React.useMemo(() => {
    const seen = new Set<string>()
    for (const e of entries) {
      if (e.phase) seen.add(e.phase)
    }
    return [...seen]
  }, [entries])

  const filteredEntries = React.useMemo(() => {
    if (!phaseFilter) return entries
    return entries.filter((e) => e.phase === phaseFilter)
  }, [entries, phaseFilter])

  return (
    <div className="flex flex-col gap-3 py-2" style={{ height: "100%" }}>
      {/* ── Status bar ── */}
      <div className="flex items-center gap-2 flex-wrap">
        <div className="flex items-center gap-1.5">
          {connected ? (
            <>
              <span className="h-1.5 w-1.5 rounded-full bg-status-healthy animate-pulse" />
              <span className="text-xs text-status-healthy">Live</span>
            </>
          ) : failed ? (
            <>
              <WifiOff className="h-3.5 w-3.5 text-destructive" />
              <span className="text-xs text-destructive">Disconnected</span>
            </>
          ) : (
            <>
              <span className="h-1.5 w-1.5 rounded-full bg-status-warning animate-pulse" />
              <span className="text-xs text-muted-foreground">
                {attempts > 0 ? `Reconnecting (attempt ${attempts})…` : "Connecting…"}
              </span>
            </>
          )}
        </div>

        {hasWarnOrError && (
          <span className="inline-flex items-center gap-1 rounded border border-status-warning/30 bg-status-warning/10 px-1.5 py-0.5 text-[10px] text-status-warning">
            <AlertTriangle className="h-3 w-3" />
            Warnings / errors
          </span>
        )}

        <span className="text-xs text-muted-foreground ml-auto">
          {filteredEntries.length.toLocaleString()} line{filteredEntries.length !== 1 ? "s" : ""}
          {entries.length > filteredEntries.length && (
            <span className="ml-1 opacity-60">(of {entries.length.toLocaleString()})</span>
          )}
        </span>
      </div>

      {/* ── Phase filter chips ── */}
      {presentPhases.length > 0 && (
        <div className="flex flex-wrap gap-1.5 items-center">
          <span className="text-[10px] text-muted-foreground">Phase:</span>
          <button
            className={cn(
              "rounded border px-2 py-0.5 text-[10px] transition-colors",
              !phaseFilter
                ? "bg-primary text-primary-foreground border-primary"
                : "bg-secondary text-muted-foreground border-border hover:bg-secondary/80",
            )}
            onClick={() => setPhaseFilter(null)}
          >
            All
          </button>
          {presentPhases.map((phase) => (
            <button
              key={phase}
              className={cn(
                "rounded border px-2 py-0.5 text-[10px] font-mono transition-colors",
                phaseFilter === phase
                  ? phaseBadgeClass(phase) + " opacity-100"
                  : "bg-secondary text-muted-foreground border-border hover:bg-secondary/80",
              )}
              onClick={() => setPhaseFilter((prev) => prev === phase ? null : phase)}
            >
              {phase}
            </button>
          ))}
        </div>
      )}

      {/* ── Empty state ── */}
      {entries.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-2 py-8 text-center">
          <p className="text-sm text-muted-foreground">No active install log</p>
          <p className="text-xs text-muted-foreground/60">
            Waiting for a deploy to start on this node
          </p>
        </div>
      )}

      {/* ── Log rows ── */}
      {filteredEntries.length > 0 && (
        <div
          ref={scrollRef}
          onScroll={handleScroll}
          className="flex-1 overflow-auto rounded-md border border-border bg-[#09090b] font-mono text-xs"
          style={{ minHeight: "300px" }}
        >
          <table className="w-full border-collapse">
            <tbody>
              {filteredEntries.map((entry, i) => (
                <tr
                  key={entry.id ? entry.id : i}
                  className={cn(
                    "border-t border-white/5 hover:bg-white/5 transition-colors",
                    i === 0 && "border-t-0",
                    (entry.level === "error" || entry.level === "fatal") && "bg-destructive/10",
                    entry.level === "warn" && "bg-status-warning/5",
                  )}
                >
                  {/* Timestamp gutter */}
                  <td className="px-3 py-0.5 whitespace-nowrap text-[10px] text-white/30 select-none w-[100px]">
                    {formatLogTs(entry.timestamp ?? entry.ts ?? 0)}
                  </td>

                  {/* Phase badge */}
                  <td className="px-1 py-0.5 w-[110px]">
                    {entry.phase ? (
                      <span
                        className={cn(
                          "inline-flex items-center rounded border px-1.5 py-0 text-[9px] uppercase tracking-wide font-medium",
                          phaseBadgeClass(entry.phase),
                        )}
                      >
                        {entry.phase}
                      </span>
                    ) : (
                      <span className="text-white/20 text-[9px]">—</span>
                    )}
                  </td>

                  {/* Level */}
                  <td className={cn("px-1 py-0.5 w-[40px] text-[9px] uppercase font-medium", levelClass(entry.level))}>
                    {entry.level}
                  </td>

                  {/* Message */}
                  <td className="px-2 py-0.5 text-white/80 break-all">
                    {entry.message}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* ── Jump to latest button (shown when user has scrolled up) ── */}
      {filteredEntries.length > 0 && (
        <button
          className="self-end text-[10px] text-muted-foreground underline underline-offset-2 hover:text-foreground transition-colors"
          onClick={() => {
            userScrolledUpRef.current = false
            const el = scrollRef.current
            if (el) el.scrollTop = el.scrollHeight
          }}
        >
          Jump to latest
        </button>
      )}

      {failed && (
        <div className="rounded border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive flex items-center gap-2">
          <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
          <span>
            Lost connection after {DEPLOY_LOG_RECONNECT_MAX} reconnect attempts.
            No active deploy session on this node, or the server is unreachable.
          </span>
        </div>
      )}
    </div>
  )
}

// ─── IPMI Tab (Sprint 34 UI B) ────────────────────────────────────────────────
//
// Three sections:
//   1. Power controls  — POST /api/v1/nodes/{id}/power/{action}
//   2. Sensor pull     — GET  /api/v1/nodes/{id}/sensors
//   3. SEL viewer      — GET  /api/v1/nodes/{id}/sel  +  POST /api/v1/nodes/{id}/sel/clear

// ─── IPMI types ───────────────────────────────────────────────────────────────

interface IpmiSensor {
  name: string
  value: string
  unit: string
  state: string // ipmitool state: ok | cr | nc | nr | ns | na
}

interface IpmiSensorsResponse {
  node_id: string
  sensors: IpmiSensor[]
  last_checked: string
}

interface IpmiSELEntry {
  id: string
  date: string
  time: string
  sensor: string
  event: string
  severity: string
  raw: string
  timestamp: string
}

interface IpmiSELResponse {
  node_id: string
  entries: IpmiSELEntry[]
  last_checked: string
}

// ─── IPMI helpers ─────────────────────────────────────────────────────────────

const SENSOR_STATE_COLORS: Record<string, string> = {
  ok: "text-status-healthy",
  cr: "text-destructive",
  nc: "text-status-warning",
  nr: "text-status-warning",
  ns: "text-muted-foreground",
  na: "text-muted-foreground",
}

function sensorStateClass(state: string): string {
  return SENSOR_STATE_COLORS[state.toLowerCase()] ?? "text-muted-foreground"
}

const DESTRUCTIVE_POWER_ACTIONS = new Set(["off", "cycle", "reset"])

const POWER_ACTIONS = [
  { action: "on",    label: "Power On",  confirmWord: null },
  { action: "off",   label: "Power Off", confirmWord: "off" },
  { action: "cycle", label: "Power Cycle", confirmWord: "cycle" },
  { action: "reset", label: "Hard Reset",  confirmWord: "reset" },
  // Soft Off removed: the server has no /power/soft endpoint.
] as const

const IPMI_SEL_PAGE_SIZE = 20

// ─── sub-components ───────────────────────────────────────────────────────────

function IpmiSectionHeading({ children }: { children: React.ReactNode }) {
  return (
    <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-2 flex items-center gap-1.5">
      <Zap className="h-3 w-3" />
      {children}
    </h3>
  )
}

function IpmiSeverityPill({ severity }: { severity: string }) {
  const s = severity.toLowerCase()
  const cls =
    s === "critical" ? "bg-destructive/10 text-destructive border-destructive/30" :
    (s === "warn" || s === "warning") ? "bg-status-warning/10 text-status-warning border-status-warning/30" :
    "bg-muted/30 text-muted-foreground border-border"
  return (
    <span className={cn("inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] font-medium", cls)}>
      {severity}
    </span>
  )
}

// ─── PowerSection ─────────────────────────────────────────────────────────────

function PowerSection({ nodeId }: { nodeId: string }) {
  const [pendingAction, setPendingAction] = React.useState<string | null>(null)
  const [confirmInput, setConfirmInput] = React.useState("")

  const mutation = useMutation({
    mutationFn: (action: string) =>
      apiFetch(`/api/v1/nodes/${nodeId}/power/${action}`, { method: "POST" }),
    onSuccess: (_data, action) => {
      toast({ title: "Power command sent", description: `Action: ${action}` })
      setPendingAction(null)
      setConfirmInput("")
    },
    onError: (err) => {
      toast({ variant: "destructive", title: "Power command failed", description: String(err) })
    },
  })

  function handleActionClick(action: string) {
    if (DESTRUCTIVE_POWER_ACTIONS.has(action)) {
      setPendingAction(action)
      setConfirmInput("")
    } else {
      mutation.mutate(action)
    }
  }

  function handleConfirmSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!pendingAction) return
    if (confirmInput.trim().toLowerCase() === pendingAction.toLowerCase()) {
      mutation.mutate(pendingAction)
    }
  }

  const confirmMatch =
    pendingAction !== null &&
    confirmInput.trim().toLowerCase() === pendingAction.toLowerCase()

  return (
    <>
      <div className="flex flex-wrap gap-2">
        {POWER_ACTIONS.map(({ action, label }) => (
          <Button
            key={action}
            size="sm"
            variant={DESTRUCTIVE_POWER_ACTIONS.has(action) ? "destructive" : "outline"}
            className="text-xs"
            disabled={mutation.isPending}
            onClick={() => handleActionClick(action)}
          >
            <Power className="h-3 w-3 mr-1" />
            {label}
          </Button>
        ))}
      </div>

      {/* Typed-confirm dialog for destructive power actions */}
      <Dialog
        open={pendingAction !== null}
        onOpenChange={(open) => { if (!open) { setPendingAction(null); setConfirmInput("") } }}
      >
        <DialogContent className="sm:max-w-sm" data-testid="power-confirm-dialog">
          <DialogHeader>
            <DialogTitle>Confirm power action</DialogTitle>
            <DialogDescription>
              Type <strong>{pendingAction}</strong> to confirm this destructive power action.
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={handleConfirmSubmit} className="space-y-3 mt-1">
            <Input
              autoFocus
              placeholder={pendingAction ?? ""}
              value={confirmInput}
              onChange={(e) => setConfirmInput(e.target.value)}
              data-testid="power-confirm-input"
            />
            <div className="flex gap-2">
              <Button
                type="submit"
                variant="destructive"
                className="flex-1"
                disabled={!confirmMatch || mutation.isPending}
                data-testid="power-confirm-submit"
              >
                {mutation.isPending ? "Sending…" : "Confirm"}
              </Button>
              <Button
                type="button"
                variant="ghost"
                onClick={() => { setPendingAction(null); setConfirmInput("") }}
                disabled={mutation.isPending}
              >
                Cancel
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}

// ─── IpmiSensorsSection ───────────────────────────────────────────────────────

function IpmiSensorsSection({ nodeId }: { nodeId: string }) {
  const { data, isFetching, refetch } = useQuery<IpmiSensorsResponse>({
    queryKey: ["ipmi-sensors", nodeId],
    queryFn: () => apiFetch<IpmiSensorsResponse>(`/api/v1/nodes/${nodeId}/sensors`),
    staleTime: Infinity,
    refetchInterval: false,
    enabled: false,
  })

  const sensors = data?.sensors ?? []

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <Button
          size="sm"
          variant="outline"
          className="text-xs"
          disabled={isFetching}
          onClick={() => refetch()}
          data-testid="sensors-refresh-btn"
        >
          <RefreshCw className={cn("h-3 w-3 mr-1", isFetching && "animate-spin")} />
          {isFetching ? "Fetching…" : "Pull Sensors"}
        </Button>
        {data?.last_checked && (
          <span className="text-[10px] text-muted-foreground">
            Last: {new Date(data.last_checked).toLocaleTimeString()}
          </span>
        )}
      </div>

      {sensors.length === 0 && !isFetching && (
        <p className="text-xs text-muted-foreground py-2">
          No sensor data. Press &ldquo;Pull Sensors&rdquo; to fetch via IPMI.
        </p>
      )}

      {sensors.length > 0 && (
        <div className="overflow-x-auto rounded border border-border">
          <table className="w-full text-xs" data-testid="sensors-table">
            <thead>
              <tr className="border-b border-border bg-muted/30">
                <th className="text-left px-3 py-1.5 font-medium text-muted-foreground">Sensor</th>
                <th className="text-right px-3 py-1.5 font-medium text-muted-foreground">Value</th>
                <th className="text-left px-3 py-1.5 font-medium text-muted-foreground">State</th>
              </tr>
            </thead>
            <tbody>
              {sensors.map((s, i) => (
                <tr key={i} className="border-b border-border last:border-0 hover:bg-muted/20">
                  <td className="px-3 py-1.5 font-mono">{s.name}</td>
                  <td className="px-3 py-1.5 text-right font-mono">
                    {s.value}{s.unit ? ` ${s.unit}` : ""}
                  </td>
                  <td className={cn("px-3 py-1.5 font-medium", sensorStateClass(s.state))}>
                    {s.state.toUpperCase()}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// ─── IpmiSELSection ───────────────────────────────────────────────────────────

function IpmiSELSection({ nodeId }: { nodeId: string }) {
  const qc = useQueryClient()
  const [page, setPage] = React.useState(0)
  const [expandedRows, setExpandedRows] = React.useState<Set<number>>(new Set())
  const [clearDialogOpen, setClearDialogOpen] = React.useState(false)
  const [clearInput, setClearInput] = React.useState("")

  const { data, isFetching, refetch } = useQuery<IpmiSELResponse>({
    queryKey: ["ipmi-sel", nodeId],
    queryFn: () => apiFetch<IpmiSELResponse>(`/api/v1/nodes/${nodeId}/sel`),
    staleTime: Infinity,
    refetchInterval: false,
    enabled: false,
  })

  const clearMutation = useMutation({
    mutationFn: () =>
      apiFetch(`/api/v1/nodes/${nodeId}/sel/clear`, { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ipmi-sel", nodeId] })
      toast({ title: "SEL cleared", description: `Event log cleared for node ${nodeId}.` })
      setClearDialogOpen(false)
      setClearInput("")
      setPage(0)
    },
    onError: (err) => {
      toast({ variant: "destructive", title: "Failed to clear SEL", description: String(err) })
    },
  })

  const allEntries = data?.entries ?? []
  const totalPages = Math.max(1, Math.ceil(allEntries.length / IPMI_SEL_PAGE_SIZE))
  const pageEntries = allEntries.slice(page * IPMI_SEL_PAGE_SIZE, (page + 1) * IPMI_SEL_PAGE_SIZE)

  function toggleRow(i: number) {
    setExpandedRows((prev) => {
      const next = new Set(prev)
      if (next.has(i)) next.delete(i)
      else next.add(i)
      return next
    })
  }

  const clearMatch = clearInput.trim() === nodeId

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 flex-wrap">
        <Button
          size="sm"
          variant="outline"
          className="text-xs"
          disabled={isFetching}
          onClick={() => { setPage(0); refetch() }}
        >
          <RefreshCw className={cn("h-3 w-3 mr-1", isFetching && "animate-spin")} />
          {isFetching ? "Fetching…" : "Pull SEL"}
        </Button>
        <Button
          size="sm"
          variant="destructive"
          className="text-xs"
          onClick={() => { setClearDialogOpen(true); setClearInput("") }}
          data-testid="sel-clear-btn"
        >
          <Trash2 className="h-3 w-3 mr-1" />
          Clear SEL
        </Button>
        {data?.last_checked && (
          <span className="text-[10px] text-muted-foreground">
            Last: {new Date(data.last_checked).toLocaleTimeString()}
          </span>
        )}
      </div>

      {allEntries.length === 0 && !isFetching && (
        <p className="text-xs text-muted-foreground py-2">
          No SEL entries. Press &ldquo;Pull SEL&rdquo; to fetch via IPMI.
        </p>
      )}

      {allEntries.length > 0 && (
        <>
          <div className="overflow-x-auto rounded border border-border">
            <table className="w-full text-xs" data-testid="sel-table">
              <thead>
                <tr className="border-b border-border bg-muted/30">
                  <th className="w-4 px-2 py-1.5" />
                  <th className="text-left px-3 py-1.5 font-medium text-muted-foreground">Time</th>
                  <th className="text-left px-3 py-1.5 font-medium text-muted-foreground">Sensor</th>
                  <th className="text-left px-3 py-1.5 font-medium text-muted-foreground">Event</th>
                  <th className="text-left px-3 py-1.5 font-medium text-muted-foreground">Severity</th>
                </tr>
              </thead>
              <tbody>
                {pageEntries.map((entry, i) => {
                  const absIdx = page * IPMI_SEL_PAGE_SIZE + i
                  const expanded = expandedRows.has(absIdx)
                  return (
                    <React.Fragment key={entry.id ?? absIdx}>
                      <tr
                        className="border-b border-border last:border-0 hover:bg-muted/20 cursor-pointer"
                        onClick={() => toggleRow(absIdx)}
                        data-testid={`sel-row-${i}`}
                      >
                        <td className="px-2 py-1.5 text-muted-foreground">
                          {expanded
                            ? <ChevronDown className="h-3 w-3" />
                            : <ChevronRight className="h-3 w-3" />}
                        </td>
                        <td className="px-3 py-1.5 font-mono whitespace-nowrap">
                          {entry.date} {entry.time}
                        </td>
                        <td className="px-3 py-1.5">{entry.sensor}</td>
                        <td className="px-3 py-1.5">{entry.event}</td>
                        <td className="px-3 py-1.5">
                          <IpmiSeverityPill severity={entry.severity} />
                        </td>
                      </tr>
                      {expanded && (
                        <tr className="border-b border-border bg-muted/10">
                          <td colSpan={5} className="px-4 py-2">
                            <span className="text-[10px] text-muted-foreground font-mono break-all">
                              Raw: {entry.raw}
                            </span>
                          </td>
                        </tr>
                      )}
                    </React.Fragment>
                  )
                })}
              </tbody>
            </table>
          </div>

          {/* Pagination */}
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Button
              size="sm"
              variant="ghost"
              className="text-xs h-6 px-2"
              disabled={page === 0}
              onClick={() => setPage((p) => Math.max(0, p - 1))}
              data-testid="sel-prev-btn"
            >
              Prev
            </Button>
            <span>
              Page {page + 1} / {totalPages} ({allEntries.length} entries)
            </span>
            <Button
              size="sm"
              variant="ghost"
              className="text-xs h-6 px-2"
              disabled={page >= totalPages - 1}
              onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))}
              data-testid="sel-next-btn"
            >
              Next
            </Button>
          </div>
        </>
      )}

      {/* Typed-confirm dialog for SEL clear (type the node ID) */}
      <Dialog
        open={clearDialogOpen}
        onOpenChange={(open) => { if (!open) { setClearDialogOpen(false); setClearInput("") } }}
      >
        <DialogContent className="sm:max-w-sm" data-testid="sel-confirm-dialog">
          <DialogHeader>
            <DialogTitle>Clear System Event Log</DialogTitle>
            <DialogDescription>
              This will permanently erase all SEL entries on the BMC. Type the node ID{" "}
              <strong className="font-mono">{nodeId}</strong> to confirm.
            </DialogDescription>
          </DialogHeader>
          <form
            onSubmit={(e) => { e.preventDefault(); if (clearMatch) clearMutation.mutate() }}
            className="space-y-3 mt-1"
          >
            <Input
              autoFocus
              placeholder={nodeId}
              value={clearInput}
              onChange={(e) => setClearInput(e.target.value)}
              data-testid="sel-confirm-input"
            />
            <div className="flex gap-2">
              <Button
                type="submit"
                variant="destructive"
                className="flex-1"
                disabled={!clearMatch || clearMutation.isPending}
                data-testid="sel-confirm-submit"
              >
                {clearMutation.isPending ? "Clearing…" : "Clear SEL"}
              </Button>
              <Button
                type="button"
                variant="ghost"
                onClick={() => { setClearDialogOpen(false); setClearInput("") }}
                disabled={clearMutation.isPending}
              >
                Cancel
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// ─── IpmiTab (root export) ────────────────────────────────────────────────────

export function IpmiTab({ nodeId }: { nodeId: string }) {
  return (
    <div className="space-y-6 py-2">
      <section>
        <IpmiSectionHeading>Power</IpmiSectionHeading>
        <PowerSection nodeId={nodeId} />
      </section>
      <section>
        <IpmiSectionHeading>Sensor Pull</IpmiSectionHeading>
        <IpmiSensorsSection nodeId={nodeId} />
      </section>
      <section>
        <IpmiSectionHeading>System Event Log (SEL)</IpmiSectionHeading>
        <IpmiSELSection nodeId={nodeId} />
      </section>
    </div>
  )
}

// ─── Sprint 38: ExternalStatsTab ─────────────────────────────────────────────
//
// Renders BMC/IPMI/SNMP samples for nodes without clustr-clientd.
// Source: GET /api/v1/nodes/{id}/external_stats
// Refetches every 60s.  Empty state when no external probes are configured.
//
// Samples are dynamically grouped by `chart_group` (STAT-REGISTRY metadata).
// If `chart_group` is absent the sample falls into an "Other" group.

import type { ExternalStatSample } from "@/lib/types"

function formatExtValue(value: number, unit?: string): string {
  if (!unit) return value.toFixed(2)
  const u = unit.toLowerCase()
  if (u === "celsius" || u === "°c" || u === "c") return `${value.toFixed(1)} °C`
  if (u === "fahrenheit" || u === "°f" || u === "f") return `${value.toFixed(1)} °F`
  if (u === "volts" || u === "v") return `${value.toFixed(3)} V`
  if (u === "amps" || u === "a") return `${value.toFixed(3)} A`
  if (u === "watts" || u === "w") return `${value.toFixed(1)} W`
  if (u === "rpm") return `${Math.round(value)} RPM`
  if (u === "percent" || u === "%") return `${value.toFixed(1)} %`
  return `${value.toFixed(2)} ${unit}`
}

function ExternalStatGroup({
  chartGroup,
  samples,
}: {
  chartGroup: string
  samples: ExternalStatSample[]
}) {
  const [open, setOpen] = React.useState(true)
  return (
    <div className="border border-border rounded-md overflow-hidden" data-testid="ext-stat-group">
      <button
        className="w-full flex items-center gap-2 px-3 py-2 text-xs font-medium bg-secondary/30 hover:bg-secondary/60 transition-colors"
        onClick={() => setOpen((o) => !o)}
      >
        {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        <span>{chartGroup}</span>
        <span className="text-muted-foreground ml-auto">{samples.length} metric{samples.length !== 1 ? "s" : ""}</span>
      </button>
      {open && (
        <table className="w-full text-xs">
          <tbody>
            {samples.map((s, i) => (
              <tr key={i} className={cn("border-t border-border", i === 0 && "border-t-0")}>
                <td className="px-3 py-1.5 text-muted-foreground">{s.title ?? s.sensor}</td>
                <td className="px-2 py-1.5 text-[10px] text-muted-foreground/60 font-mono">{s.plugin}</td>
                <td className="px-3 py-1.5 text-right font-mono tabular-nums">
                  {formatExtValue(s.value, s.unit)}
                </td>
                <td className="px-2 py-1.5 text-[10px] text-muted-foreground/50 whitespace-nowrap">
                  {s.ts ? new Date(s.ts).toLocaleTimeString() : ""}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

export function ExternalStatsTab({ nodeId }: { nodeId: string }) {
  const { data, isLoading, isError, refetch, dataUpdatedAt } = useQuery<ExternalStatSample[]>({
    queryKey: ["external-stats", nodeId],
    queryFn: () => apiFetch<ExternalStatSample[]>(`/api/v1/nodes/${nodeId}/external_stats`),
    refetchInterval: 60_000,
    staleTime: 55_000,
  })

  // Group samples by chart_group (falling back to "Other").
  const groups = React.useMemo(() => {
    const map = new Map<string, ExternalStatSample[]>()
    for (const s of data ?? []) {
      const key = s.chart_group ?? "Other"
      if (!map.has(key)) map.set(key, [])
      map.get(key)!.push(s)
    }
    return map
  }, [data])

  const lastUpdated = dataUpdatedAt ? new Date(dataUpdatedAt).toLocaleTimeString() : null

  return (
    <div className="space-y-3 py-2">
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">
          Agent-less BMC / IPMI / SNMP probes
          {lastUpdated && <span className="ml-2 opacity-60">updated {lastUpdated}</span>}
        </span>
        <Button
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-xs"
          onClick={() => refetch()}
        >
          <RefreshCw className="h-3 w-3 mr-1" />
          Refresh
        </Button>
      </div>

      {isLoading && (
        <div className="text-xs text-muted-foreground py-4 text-center">Loading external stats…</div>
      )}

      {isError && (
        <div className="text-xs text-destructive py-4 text-center">
          Failed to load external stats.
        </div>
      )}

      {!isLoading && !isError && groups.size === 0 && (
        <div
          className="flex flex-col items-center justify-center py-12 text-center gap-2 text-muted-foreground"
          data-testid="ext-stats-empty"
        >
          <p className="text-sm">No external probes configured</p>
          <p className="text-xs opacity-60">
            External stats are collected when BMC credentials are set on the node and the external probe pool is running.
          </p>
        </div>
      )}

      {groups.size > 0 && (
        <div className="space-y-2">
          {[...groups.entries()].map(([group, samples]) => (
            <ExternalStatGroup key={group} chartGroup={group} samples={samples} />
          ))}
        </div>
      )}
    </div>
  )
}

// ─── Sprint 38: Dynamic chart-group grouping for SensorsTab ──────────────────
//
// StatSample may carry `title` and `chart_group` from the STAT-REGISTRY
// metadata. This function groups any sample set by chart_group and renders
// one ChartGroupCard per group.  Used by SensorsTab when chart_group data
// is present; falls back gracefully to the existing plugin-grouping path.

interface StatSampleWithMeta extends StatSample {
  title?: string
  chart_group?: string
}

function ChartGroupCard({
  group,
  samples,
}: {
  group: string
  samples: StatSampleWithMeta[]
}) {
  const [open, setOpen] = React.useState(true)
  const latest = React.useMemo(() => {
    const map = new Map<string, StatSampleWithMeta>()
    for (const s of samples) {
      const key = s.sensor + s.plugin + JSON.stringify(s.labels ?? {})
      const prev = map.get(key)
      if (!prev || s.ts > prev.ts) map.set(key, s)
    }
    return [...map.values()].sort((a, b) => a.sensor.localeCompare(b.sensor))
  }, [samples])

  if (latest.length === 0) return null

  return (
    <div
      className="border border-border rounded-md overflow-hidden"
      data-testid={`chart-group-card-${group}`}
    >
      <button
        className="w-full flex items-center gap-2 px-3 py-2 text-xs font-medium bg-secondary/30 hover:bg-secondary/60 transition-colors"
        onClick={() => setOpen((o) => !o)}
      >
        {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        <span>{group}</span>
        <span className="text-muted-foreground ml-auto">{latest.length} sensor{latest.length !== 1 ? "s" : ""}</span>
      </button>
      {open && (
        <table className="w-full text-xs">
          <tbody>
            {latest.map((s, i) => (
              <tr key={i} className={cn("border-t border-border", i === 0 && "border-t-0")}>
                <td className="px-3 py-1.5 text-muted-foreground">{s.title ?? s.sensor}</td>
                <td className="px-2 py-1.5 text-[10px] text-muted-foreground/60 font-mono">{s.plugin}</td>
                <td className="px-3 py-1.5 text-right font-mono tabular-nums">
                  {formatValue(s.value, s.unit)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

/**
 * groupSamplesByChartGroup — exported for tests.
 * Groups samples by `chart_group`; samples without chart_group are excluded.
 * Returns a Map<groupName, samples[]> with stable insertion order.
 */
export function groupSamplesByChartGroup(
  samples: StatSampleWithMeta[],
): Map<string, StatSampleWithMeta[]> {
  const map = new Map<string, StatSampleWithMeta[]>()
  for (const s of samples) {
    if (!s.chart_group) continue
    if (!map.has(s.chart_group)) map.set(s.chart_group, [])
    map.get(s.chart_group)!.push(s)
  }
  return map
}
