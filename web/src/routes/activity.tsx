import * as React from "react"
import { useNavigate, useSearch } from "@tanstack/react-router"
import { useQuery } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import { Search, RefreshCw } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from "@/components/ui/table"
import { apiFetch } from "@/lib/api"
import type { AuditRecord, AuditQueryResponse } from "@/lib/types"
import { cn } from "@/lib/utils"

interface ActivitySearch {
  q?: string
  kind?: string
}

const LIMIT = 100

function kindLabel(action: string, resourceType: string): string {
  if (resourceType === "node") return "provisioning"
  if (action.includes("error") || action.includes("fail")) return "error"
  if (resourceType === "api_key" || resourceType === "session") return "api"
  return "event"
}

function kindColor(kind: string): string {
  switch (kind) {
    case "provisioning": return "text-status-healthy"
    case "error": return "text-destructive"
    case "api": return "text-blue-400"
    default: return "text-muted-foreground"
  }
}

export function ActivityPage() {
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as ActivitySearch
  const q = search.q ?? ""
  const kind = search.kind ?? ""

  const listRef = React.useRef<HTMLDivElement>(null)
  const [userScrolled, setUserScrolled] = React.useState(false)
  const [selectedRecord, setSelectedRecord] = React.useState<AuditRecord | null>(null)

  function updateSearch(patch: Partial<ActivitySearch>) {
    navigate({
      to: "/activity",
      search: {
        q: patch.q !== undefined ? patch.q : q || undefined,
        kind: patch.kind !== undefined ? patch.kind : kind || undefined,
      },
      replace: true,
    })
  }

  const { data, refetch, isFetching } = useQuery<AuditQueryResponse>({
    queryKey: ["activity", q, kind],
    queryFn: () => {
      const params = new URLSearchParams({ limit: String(LIMIT) })
      if (q) params.set("action", q)
      if (kind === "provisioning") params.set("resource_type", "node")
      if (kind === "api") params.set("resource_type", "api_key")
      return apiFetch<AuditQueryResponse>(`/api/v1/audit?${params}`)
    },
    refetchInterval: 5000,
    staleTime: 3000,
  })

  // Auto-scroll to bottom unless user scrolled up.
  React.useEffect(() => {
    if (!userScrolled && listRef.current) {
      listRef.current.scrollTop = listRef.current.scrollHeight
    }
  })

  function handleScroll() {
    const el = listRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50
    setUserScrolled(!atBottom)
  }

  const records = data?.records ?? []
  const filtered = records.filter((r) => {
    if (q && !r.action.includes(q) && !r.actor_label.includes(q) && !r.resource_id.includes(q)) return false
    if (kind && kindLabel(r.action, r.resource_type) !== kind) return false
    return true
  })

  return (
    <div className="flex flex-col h-full">
      {/* Toolbar */}
      <div className="flex items-center justify-between gap-3 border-b border-border px-6 py-3">
        <div className="relative w-72">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            className="pl-8"
            placeholder="Filter by action or subject..."
            value={q}
            onChange={(e) => updateSearch({ q: e.target.value || undefined })}
          />
        </div>
        <div className="flex items-center gap-2">
          <div className="flex gap-1">
            {["", "provisioning", "api", "error"].map((k) => (
              <Button
                key={k || "all"}
                variant={kind === k ? "secondary" : "ghost"}
                size="sm"
                className="text-xs"
                onClick={() => updateSearch({ kind: k || undefined })}
              >
                {k || "All"}
              </Button>
            ))}
          </div>
          <Button
            variant="ghost"
            size="icon"
            className={cn(isFetching && "animate-spin")}
            onClick={() => refetch()}
          >
            <RefreshCw className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>

      {/* Table */}
      <div
        ref={listRef}
        className="flex-1 overflow-auto"
        onScroll={handleScroll}
      >
        {filtered.length === 0 ? (
          <EmptyState />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Time</TableHead>
                <TableHead>Kind</TableHead>
                <TableHead>Actor</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Subject</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((rec) => {
                const k = kindLabel(rec.action, rec.resource_type)
                return (
                  <TableRow
                    key={rec.id}
                    className="cursor-pointer"
                    onClick={() => setSelectedRecord(rec)}
                  >
                    <TableCell className="text-xs text-muted-foreground whitespace-nowrap">
                      {relativeTime(rec.created_at)}
                    </TableCell>
                    <TableCell>
                      <span className={cn("text-xs font-medium", kindColor(k))}>{k}</span>
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {rec.actor_label || "—"}
                    </TableCell>
                    <TableCell className="text-xs">
                      {rec.action}
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {rec.resource_id ? rec.resource_id.slice(0, 12) : "—"}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}

        {/* Scroll lock indicator */}
        {userScrolled && (
          <div className="sticky bottom-4 flex justify-center pointer-events-none">
            <button
              className="pointer-events-auto bg-secondary text-xs text-muted-foreground rounded-full px-3 py-1 border border-border shadow"
              onClick={() => {
                setUserScrolled(false)
                if (listRef.current) {
                  listRef.current.scrollTop = listRef.current.scrollHeight
                }
              }}
            >
              Scroll to latest
            </button>
          </div>
        )}
      </div>

      {selectedRecord && (
        <ActivitySheet record={selectedRecord} onClose={() => setSelectedRecord(null)} />
      )}
    </div>
  )
}

function relativeTime(iso?: string) {
  if (!iso) return "—"
  try { return formatDistanceToNow(new Date(iso), { addSuffix: true }) } catch { return "—" }
}

function EmptyState() {
  return (
    <div className="flex flex-col items-center justify-center h-full min-h-64 gap-2 p-8 text-center">
      <h2 className="text-base font-semibold">No activity yet</h2>
      <p className="text-sm text-muted-foreground">
        Trigger a node provisioning or upload an image to see events here.
      </p>
    </div>
  )
}

function ActivitySheet({ record, onClose }: { record: AuditRecord; onClose: () => void }) {
  function tryPrettyJSON(val: unknown): string {
    if (val === null || val === undefined) return ""
    try { return JSON.stringify(val, null, 2) } catch { return String(val) }
  }

  return (
    <Sheet open onOpenChange={(v) => !v && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-xl overflow-y-auto">
        <SheetHeader>
          <SheetTitle className="font-mono text-sm">{record.action}</SheetTitle>
        </SheetHeader>

        <div className="mt-6 space-y-4">
          <Section title="Event">
            <Row label="ID" value={record.id} mono />
            <Row label="Time" value={relativeTime(record.created_at)} />
            <Row label="Actor" value={record.actor_label || record.actor_id} mono />
            <Row label="Resource type" value={record.resource_type} />
            <Row label="Resource ID" value={record.resource_id} mono />
            {record.ip_addr && <Row label="IP" value={record.ip_addr} mono />}
          </Section>

          {record.new_value !== undefined && record.new_value !== null && (
            <Section title="New value">
              <pre className="text-xs font-mono bg-secondary rounded p-3 overflow-auto max-h-48 whitespace-pre-wrap">
                {tryPrettyJSON(record.new_value)}
              </pre>
            </Section>
          )}

          {record.old_value !== undefined && record.old_value !== null && (
            <Section title="Old value">
              <pre className="text-xs font-mono bg-secondary rounded p-3 overflow-auto max-h-48 whitespace-pre-wrap">
                {tryPrettyJSON(record.old_value)}
              </pre>
            </Section>
          )}
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
      <span className={cn("text-right break-all", mono && "font-mono text-xs")}>{value ?? "—"}</span>
    </div>
  )
}
