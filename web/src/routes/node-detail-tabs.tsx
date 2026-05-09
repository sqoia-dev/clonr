// node-detail-tabs.tsx — Sensors, Event Log, Console, and Deploy Log tabs for the node Sheet (#152)
//
// Four independent components, each mounted only when their tab is active:
//   <SensorsTab nodeId> — recharts sparklines + sensor table from /api/v1/nodes/{id}/stats
//   <EventLogTab nodeId> — SEL list from /api/v1/nodes/{id}/sel with level/regex/head-tail toolbar
//   <ConsoleTab nodeId> — xterm.js terminal over WS /api/v1/console/{node_id}
//   <DeployLogTab nodeId primaryMac> — live SSE log from GET /api/v1/logs/stream?component=deploy&node_mac=<mac>

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
import { RefreshCw, Trash2, AlertTriangle, ChevronDown, ChevronRight, WifiOff } from "lucide-react"
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
  ts: number       // Unix ms (the server field is `ts` in ms per the wire spec)
  phase?: string   // STREAM-LOG-PHASE field — may be absent before that ships
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

    connect(0)

    return () => {
      cleanup()
      if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [primaryMac])

  function connect(attempt: number) {
    if (!mountedRef.current) return

    const path = `/api/v1/logs/stream?component=deploy&node_mac=${encodeURIComponent(primaryMac)}`
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
                    {formatLogTs(entry.ts)}
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
