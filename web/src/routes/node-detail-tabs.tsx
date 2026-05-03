// node-detail-tabs.tsx — Sensors, Event Log, and Console tabs for the node Sheet (#152)
//
// Three independent components, each mounted only when their tab is active:
//   <SensorsTab nodeId> — recharts sparklines + sensor table from /api/v1/nodes/{id}/stats
//   <EventLogTab nodeId> — SEL list from /api/v1/nodes/{id}/sel with level/regex/head-tail toolbar
//   <ConsoleTab nodeId> — xterm.js terminal over WS /api/v1/console/{node_id}

import * as React from "react"
import { useQuery, useMutation } from "@tanstack/react-query"
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
import { RefreshCw, Trash2, AlertTriangle, ChevronDown, ChevronRight } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { apiFetch, wsUrl } from "@/lib/api"
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

      {/* Sensor table grouped by plugin */}
      {allLoading ? (
        <div className="text-xs text-muted-foreground py-4 text-center">Loading sensors…</div>
      ) : (allData ?? []).length === 0 ? (
        <div className="text-xs text-muted-foreground py-4 text-center">
          No stats data. The stats agent must be running on this node and reporting to the server.
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
