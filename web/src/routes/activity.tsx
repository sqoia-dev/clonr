import * as React from "react"
import { useNavigate, useSearch } from "@tanstack/react-router"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import { Search, RefreshCw, Trash2, AlertTriangle } from "lucide-react"
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
import { toast } from "@/hooks/use-toast"

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
  const qc = useQueryClient()
  const search = useSearch({ strict: false }) as ActivitySearch
  const q = search.q ?? ""
  const kind = search.kind ?? ""

  const listRef = React.useRef<HTMLDivElement>(null)
  const [userScrolled, setUserScrolled] = React.useState(false)
  const [selectedRecord, setSelectedRecord] = React.useState<AuditRecord | null>(null)

  // ACT-DEL-3..5: selection + deletion state.
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(new Set())
  const [deleteConfirm, setDeleteConfirm] = React.useState("")
  const [deleteExpanded, setDeleteExpanded] = React.useState(false)
  const [clearFilterExpanded, setClearFilterExpanded] = React.useState(false)
  const [clearConfirm, setClearConfirm] = React.useState("")
  const [optimisticRemovals, setOptimisticRemovals] = React.useState<Set<string>>(new Set())

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

  const { data, refetch, isFetching, isLoading: actLoading, isError: actError } = useQuery<AuditQueryResponse>({
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
    if (optimisticRemovals.has(r.id)) return false
    if (q && !r.action.includes(q) && !r.actor_label.includes(q) && !r.resource_id.includes(q)) return false
    if (kind && kindLabel(r.action, r.resource_type) !== kind) return false
    return true
  })

  const filterActive = !!(q || kind)
  const selectedCount = selectedIds.size

  async function handleDeleteSelected() {
    const ids = Array.from(selectedIds)
    if (ids.length === 0) return
    // Optimistic remove.
    setOptimisticRemovals((prev) => new Set([...prev, ...ids]))
    setSelectedIds(new Set())
    setDeleteExpanded(false)
    setDeleteConfirm("")
    try {
      await Promise.all(ids.map((id) => apiFetch(`/api/v1/audit/${id}`, { method: "DELETE" })))
      qc.invalidateQueries({ queryKey: ["activity"] })
      toast({ title: `Deleted ${ids.length} record${ids.length !== 1 ? "s" : ""}` })
    } catch (err) {
      // Rollback on error.
      setOptimisticRemovals((prev) => {
        const next = new Set(prev)
        ids.forEach((id) => next.delete(id))
        return next
      })
      toast({ variant: "destructive", title: "Delete failed", description: String(err) })
    }
  }

  async function handleClearFiltered() {
    const params = new URLSearchParams()
    params.set("before", new Date().toISOString())
    if (q) params.set("action", q)
    if (kind === "provisioning") params.set("resource_type", "node")
    if (kind === "api") params.set("resource_type", "api_key")

    // Optimistic remove all visible filtered records.
    const ids = filtered.map((r) => r.id)
    setOptimisticRemovals((prev) => new Set([...prev, ...ids]))
    setClearFilterExpanded(false)
    setClearConfirm("")
    try {
      const resp = await apiFetch<{ count: number }>(`/api/v1/audit?${params}`, { method: "DELETE" })
      qc.invalidateQueries({ queryKey: ["activity"] })
      toast({ title: `Cleared ${resp.count} record${resp.count !== 1 ? "s" : ""}` })
    } catch (err) {
      setOptimisticRemovals((prev) => {
        const next = new Set(prev)
        ids.forEach((id) => next.delete(id))
        return next
      })
      toast({ variant: "destructive", title: "Clear failed", description: String(err) })
    }
  }

  return (
    <div className="flex flex-col h-full">
      {/* Toolbar */}
      <div className="flex flex-col gap-2 border-b border-border px-6 py-3">
        <div className="flex items-center justify-between gap-3">
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
            {/* ACT-DEL-4: "Clear filtered…" when a filter is active */}
            {filterActive && !clearFilterExpanded && (
              <Button variant="ghost" size="sm" className="text-xs text-destructive" onClick={() => setClearFilterExpanded(true)}>
                <Trash2 className="h-3 w-3 mr-1" />
                Clear filtered…
              </Button>
            )}
            {/* ACT-DEL-3: "Delete selected" when rows are selected */}
            {selectedCount > 0 && !deleteExpanded && (
              <Button variant="ghost" size="sm" className="text-xs text-destructive" onClick={() => setDeleteExpanded(true)}>
                <Trash2 className="h-3 w-3 mr-1" />
                Delete {selectedCount} selected
              </Button>
            )}
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

        {/* ACT-DEL-3: Delete selected inline confirm */}
        {deleteExpanded && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 space-y-2">
            <div className="flex items-center gap-2 text-xs text-destructive">
              <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
              Type <code className="font-mono">delete {selectedCount} entries</code> to confirm:
            </div>
            <div className="flex gap-2">
              <Input
                className="text-xs h-7 flex-1"
                placeholder={`delete ${selectedCount} entries`}
                value={deleteConfirm}
                onChange={(e) => setDeleteConfirm(e.target.value)}
              />
              <Button
                variant="destructive"
                size="sm"
                className="h-7"
                disabled={deleteConfirm !== `delete ${selectedCount} entries`}
                onClick={handleDeleteSelected}
              >
                Confirm
              </Button>
              <Button variant="ghost" size="sm" className="h-7" onClick={() => { setDeleteExpanded(false); setDeleteConfirm("") }}>
                Cancel
              </Button>
            </div>
          </div>
        )}

        {/* ACT-DEL-4: Clear filtered inline confirm */}
        {clearFilterExpanded && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 space-y-2">
            <div className="flex items-center gap-2 text-xs text-destructive">
              <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
              Clear all {filtered.length} records matching current filter. Type <code className="font-mono">clear</code> to confirm:
            </div>
            <div className="flex gap-2">
              <Input
                className="text-xs h-7 flex-1"
                placeholder="clear"
                value={clearConfirm}
                onChange={(e) => setClearConfirm(e.target.value)}
              />
              <Button
                variant="destructive"
                size="sm"
                className="h-7"
                disabled={clearConfirm !== "clear"}
                onClick={handleClearFiltered}
              >
                Confirm
              </Button>
              <Button variant="ghost" size="sm" className="h-7" onClick={() => { setClearFilterExpanded(false); setClearConfirm("") }}>
                Cancel
              </Button>
            </div>
          </div>
        )}
      </div>

      {/* Table */}
      <div
        ref={listRef}
        className="flex-1 overflow-auto"
        onScroll={handleScroll}
      >
        {actLoading ? (
          <div className="p-4 space-y-2">
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className="h-8 w-full rounded bg-secondary/40 animate-pulse" />
            ))}
          </div>
        ) : actError ? (
          <div className="flex items-center justify-center h-40">
            <p className="text-sm text-destructive">Failed to load activity. Reload to retry.</p>
          </div>
        ) : filtered.length === 0 ? (
          <EmptyState />
        ) : (
          <Table>
            <caption className="sr-only">Cluster activity log</caption>
            <TableHeader>
              <TableRow>
                {/* ACT-DEL-3: checkbox column */}
                <TableHead scope="col" className="w-8">
                  <input
                    type="checkbox"
                    className="rounded"
                    checked={selectedCount === filtered.length && filtered.length > 0}
                    onChange={(e) => {
                      if (e.target.checked) setSelectedIds(new Set(filtered.map((r) => r.id)))
                      else setSelectedIds(new Set())
                    }}
                    aria-label="Select all"
                  />
                </TableHead>
                <TableHead scope="col">Time</TableHead>
                <TableHead scope="col">Kind</TableHead>
                <TableHead scope="col">Actor</TableHead>
                <TableHead scope="col">Action</TableHead>
                <TableHead scope="col">Subject</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((rec) => {
                const k = kindLabel(rec.action, rec.resource_type)
                const checked = selectedIds.has(rec.id)
                return (
                  <TableRow
                    key={rec.id}
                    className={cn("cursor-pointer", checked && "bg-secondary/40")}
                    onClick={() => setSelectedRecord(rec)}
                  >
                    <TableCell onClick={(e) => e.stopPropagation()}>
                      <input
                        type="checkbox"
                        className="rounded"
                        checked={checked}
                        onChange={(e) => {
                          setSelectedIds((prev) => {
                            const next = new Set(prev)
                            if (e.target.checked) next.add(rec.id)
                            else next.delete(rec.id)
                            return next
                          })
                        }}
                        aria-label={`Select record ${rec.id}`}
                      />
                    </TableCell>
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
