/**
 * slurm.tsx — Sprint 10 Slurm surface.
 *
 * Sections (anchored within one page):
 *   #status   — module status + enable/disable + sync-now
 *   #configs  — config file list + editor sheet (textarea, validate, save, history)
 *   #roles    — node role assignments + role-summary cards
 *   #scripts  — prolog/epilog scripts + editor
 *   #builds   — async build pipeline + SSE live log
 *   #upgrades — rolling upgrade orchestration + phase stepper
 *
 * Editor choice: <textarea> with JetBrains Mono + line-number overlay.
 * Rationale: Slurm configs are ≤100 lines; codemirror would add ~300 kB to the
 * bundle. A styled textarea gives Validate/Save/History without the weight.
 */
import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import {
  Cpu, CheckCircle2, XCircle, AlertCircle, Play, StopCircle,
  RefreshCw, ChevronRight, ChevronDown, Loader2,
  FileText, Code2, History, TerminalSquare, Package, ArrowUpCircle,
  ScrollText, CircleDot, Check, X, Wrench, Eye, KeyRound, Table2, ShieldAlert,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet"
import { apiFetch, sseUrl } from "@/lib/api"
import { SectionErrorBoundary } from "@/components/ErrorBoundary"
import { toast } from "@/hooks/use-toast"
import { cn } from "@/lib/utils"
import type {
  SlurmStatus,
  ListSlurmConfigsResponse,
  SlurmConfigFile,
  SlurmValidateResponse,
  ListSlurmRoleSummaryResponse,
  ListSlurmNodesResponse,
  SlurmNodeEntry,
  ListSlurmScriptsResponse,
  SlurmScriptFile,
  ListSlurmBuildsResponse,
  SlurmBuild,
  SlurmBuildLogEvent,
  SlurmUpgradeValidation,
  SlurmUpgradeOperation,
  ListSlurmUpgradesResponse,
  // Sprint 12 — TAIL-1..4
  SlurmRenderPreviewResponse,
  SlurmDepMatrixResponse,
  SlurmPushOperation,
  SlurmMungeKeyResponse,
  // Sprint 24 #153
  SlurmJob,
  ListSlurmJobsResponse,
  SlurmPartitionInfo,
  ListSlurmPartitionsResponse,
} from "@/lib/types"

// ─── Helpers ──────────────────────────────────────────────────────────────────

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1048576) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1073741824) return `${(n / 1048576).toFixed(1)} MB`
  return `${(n / 1073741824).toFixed(1)} GB`
}

function fmtUnix(ts: number | undefined): string {
  if (!ts) return "—"
  return formatDistanceToNow(new Date(ts * 1000), { addSuffix: true })
}

// ─── Status badge ─────────────────────────────────────────────────────────────

function SlurmStatusBadge({ status }: { status: string }) {
  const map: Record<string, { label: string; className: string; icon: React.ReactNode }> = {
    ready:          { label: "Ready",         className: "bg-status-healthy/10 text-status-healthy border-status-healthy/30",   icon: <CheckCircle2 className="h-3.5 w-3.5" /> },
    disabled:       { label: "Disabled",      className: "bg-status-neutral/10 text-status-neutral border-status-neutral/30",   icon: <XCircle className="h-3.5 w-3.5" /> },
    not_configured: { label: "Not configured", className: "bg-status-warning/10 text-status-warning border-status-warning/30", icon: <AlertCircle className="h-3.5 w-3.5" /> },
    error:          { label: "Error",         className: "bg-status-error/10 text-status-error border-status-error/30",         icon: <XCircle className="h-3.5 w-3.5" /> },
  }
  const cfg = map[status] ?? { label: status, className: "bg-muted/30 text-muted-foreground border-border", icon: <CircleDot className="h-3.5 w-3.5" /> }
  return (
    <span className={cn("inline-flex items-center gap-1.5 rounded border px-2 py-0.5 text-xs font-medium", cfg.className)}>
      {cfg.icon}
      {cfg.label}
    </span>
  )
}

function BuildStatusBadge({ status }: { status: string }) {
  const map: Record<string, string> = {
    building:  "bg-status-warning/10 text-status-warning border-status-warning/30",
    completed: "bg-status-healthy/10 text-status-healthy border-status-healthy/30",
    failed:    "bg-status-error/10 text-status-error border-status-error/30",
    cancelled: "bg-status-neutral/10 text-status-neutral border-status-neutral/30",
  }
  return (
    <span className={cn("inline-flex items-center gap-1 rounded border px-2 py-0.5 text-xs font-medium capitalize",
      map[status] ?? "bg-muted/30 text-muted-foreground border-border")}>
      {status === "building" && <Loader2 className="h-3 w-3 animate-spin" />}
      {status}
    </span>
  )
}

function UpgradeStatusBadge({ status }: { status: string }) {
  const map: Record<string, string> = {
    queued:             "bg-status-neutral/10 text-status-neutral border-status-neutral/30",
    in_progress:        "bg-status-warning/10 text-status-warning border-status-warning/30",
    paused:             "bg-status-warning/10 text-status-warning border-status-warning/30",
    completed:          "bg-status-healthy/10 text-status-healthy border-status-healthy/30",
    failed:             "bg-status-error/10 text-status-error border-status-error/30",
    rollback_initiated: "bg-status-error/10 text-status-error border-status-error/30",
  }
  return (
    <span className={cn("inline-flex items-center gap-1 rounded border px-2 py-0.5 text-xs font-medium",
      map[status] ?? "bg-muted/30 text-muted-foreground border-border")}>
      {(status === "in_progress" || status === "queued") && <Loader2 className="h-3 w-3 animate-spin" />}
      {status.replace(/_/g, " ")}
    </span>
  )
}

// ─── Section wrapper ──────────────────────────────────────────────────────────

function Section({ id, icon, title, children }: { id: string; icon: React.ReactNode; title: string; children: React.ReactNode }) {
  return (
    <section id={id} className="rounded-lg border border-border bg-card p-6">
      <h2 className="mb-4 flex items-center gap-2 text-base font-semibold">
        {icon}
        {title}
      </h2>
      {children}
    </section>
  )
}

// ─── Status section ───────────────────────────────────────────────────────────

function StatusSection() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery<SlurmStatus>({
    queryKey: ["slurm-status"],
    queryFn: () => apiFetch<SlurmStatus>("/api/v1/slurm/status"),
    refetchInterval: 15000,
  })
  const { data: roleSummary } = useQuery<ListSlurmRoleSummaryResponse>({
    queryKey: ["slurm-role-summary"],
    queryFn: () => apiFetch<ListSlurmRoleSummaryResponse>("/api/v1/slurm/roles/summary"),
    refetchInterval: 30000,
  })

  const [enableOpen, setEnableOpen] = React.useState(false)
  const [disableConfirm, setDisableConfirm] = React.useState("")
  const [clusterName, setClusterName] = React.useState("")
  // TAIL-4: push-op status polling drawer
  const [pushOpID, setPushOpID] = React.useState<string | null>(null)
  const [pushOpOpen, setPushOpOpen] = React.useState(false)
  // TAIL-2: munge key panel
  const [mungeOpen, setMungeOpen] = React.useState(false)

  const enableMut = useMutation({
    mutationFn: () => apiFetch("/api/v1/slurm/enable", {
      method: "POST",
      body: JSON.stringify({ cluster_name: clusterName || "clustr" }),
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["slurm-status"] })
      qc.invalidateQueries({ queryKey: ["slurm-role-summary"] })
      setEnableOpen(false)
      setClusterName("")
      toast({ title: "Slurm module enabled" })
    },
    onError: (e: Error) => toast({ title: "Enable failed", description: e.message, variant: "destructive" }),
  })

  const disableMut = useMutation({
    mutationFn: () => apiFetch("/api/v1/slurm/disable", { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["slurm-status"] })
      setDisableConfirm("")
      toast({ title: "Slurm module disabled" })
    },
    onError: (e: Error) => toast({ title: "Disable failed", description: e.message, variant: "destructive" }),
  })

  // TAIL-4: sync returns a push-op; open polling drawer on success.
  const syncMut = useMutation({
    mutationFn: () => apiFetch<SlurmPushOperation>("/api/v1/slurm/sync", { method: "POST" }),
    onSuccess: (op) => {
      setPushOpID(op.id)
      setPushOpOpen(true)
      toast({ title: "Sync started", description: `Push op ${op.id.slice(0, 8)}…` })
    },
    onError: (e: Error) => toast({ title: "Sync failed", description: e.message, variant: "destructive" }),
  })

  // TAIL-2: munge key mutations.
  const generateMungeMut = useMutation({
    mutationFn: () => apiFetch<SlurmMungeKeyResponse>("/api/v1/slurm/munge-key/generate", { method: "POST" }),
    onSuccess: (r) => { toast({ title: "Munge key generated", description: r.message }) },
    onError: (e: Error) => toast({ title: "Generate failed", description: e.message, variant: "destructive" }),
  })

  const rotateMungeMut = useMutation({
    mutationFn: () => apiFetch<SlurmMungeKeyResponse>("/api/v1/slurm/munge-key/rotate", { method: "POST" }),
    onSuccess: (r) => { toast({ title: "Munge key rotated", description: r.message }) },
    onError: (e: Error) => toast({ title: "Rotate failed", description: e.message, variant: "destructive" }),
  })

  if (isLoading) return <div className="space-y-2"><Skeleton className="h-8 w-48" /><Skeleton className="h-20 w-full" /></div>

  const status = data?.status ?? "not_configured"
  const enabled = data?.enabled ?? false

  return (
    <Section id="status" icon={<CircleDot className="h-4 w-4 text-muted-foreground" />} title="Status">
      <div className="flex flex-wrap items-start gap-6">
        {/* Status + meta */}
        <div className="flex-1 min-w-48 space-y-3">
          <div className="flex items-center gap-2">
            <SlurmStatusBadge status={status} />
            {data?.cluster_name && (
              <span className="text-xs text-muted-foreground font-mono">cluster: {data.cluster_name}</span>
            )}
          </div>
          {/* Role counts */}
          {(roleSummary?.summary ?? []).length > 0 && (
            <div className="flex flex-wrap gap-2">
              {(roleSummary?.summary ?? []).map((r) => (
                <div key={r.role} className="flex items-center gap-1.5 rounded border border-border px-2 py-1 text-xs">
                  <span className="font-medium capitalize">{r.role}</span>
                  <span className="text-muted-foreground">{r.count}</span>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* Actions */}
        <div className="flex flex-wrap gap-2">
          {!enabled && (
            <>
              {!enableOpen ? (
                <Button size="sm" onClick={() => setEnableOpen(true)}>
                  <Play className="mr-1.5 h-3.5 w-3.5" />
                  Enable
                </Button>
              ) : (
                <div className="flex items-center gap-2">
                  <Input
                    className="h-8 w-40 text-sm"
                    placeholder="cluster name"
                    value={clusterName}
                    onChange={(e) => setClusterName(e.target.value)}
                  />
                  <Button size="sm" onClick={() => enableMut.mutate()} disabled={enableMut.isPending}>
                    {enableMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : "Confirm"}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setEnableOpen(false)}>Cancel</Button>
                </div>
              )}
            </>
          )}
          {enabled && (
            <>
              <Button size="sm" variant="outline" onClick={() => syncMut.mutate()} disabled={syncMut.isPending}>
                {syncMut.isPending ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="mr-1.5 h-3.5 w-3.5" />}
                Sync now
              </Button>
              {/* TAIL-2: munge key panel toggle */}
              <Button size="sm" variant="outline" onClick={() => setMungeOpen((v) => !v)}>
                <KeyRound className="mr-1.5 h-3.5 w-3.5" />
                Munge key
              </Button>
              <div className="flex items-center gap-2">
                <Input
                  className="h-8 w-32 text-sm"
                  placeholder={`type "disable"`}
                  value={disableConfirm}
                  onChange={(e) => setDisableConfirm(e.target.value)}
                />
                <Button
                  size="sm"
                  variant="destructive"
                  onClick={() => disableMut.mutate()}
                  disabled={disableConfirm !== "disable" || disableMut.isPending}
                >
                  {disableMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <StopCircle className="mr-1.5 h-3.5 w-3.5" />}
                  Disable
                </Button>
              </div>
            </>
          )}
        </div>
      </div>

      {/* TAIL-2: munge key panel */}
      {enabled && mungeOpen && (
        <div className="mt-4 rounded border border-border bg-secondary/5 px-4 py-3 space-y-2">
          <p className="text-xs font-medium flex items-center gap-1.5">
            <KeyRound className="h-3.5 w-3.5" />
            Munge key management
          </p>
          <p className="text-[11px] text-muted-foreground">
            Generate creates a new random munge key and stores it in clustr's secrets store.
            Rotate replaces the existing key and schedules a push on next sync.
            Both operations require a subsequent "Sync now" to deploy to nodes.
          </p>
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="outline"
              className="text-xs"
              onClick={() => generateMungeMut.mutate()}
              disabled={generateMungeMut.isPending || rotateMungeMut.isPending}
            >
              {generateMungeMut.isPending ? <Loader2 className="mr-1.5 h-3 w-3 animate-spin" /> : null}
              Generate new key
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="text-xs"
              onClick={() => rotateMungeMut.mutate()}
              disabled={generateMungeMut.isPending || rotateMungeMut.isPending}
            >
              {rotateMungeMut.isPending ? <Loader2 className="mr-1.5 h-3 w-3 animate-spin" /> : null}
              Rotate existing key
            </Button>
            <Button size="sm" variant="ghost" className="text-xs ml-auto" onClick={() => setMungeOpen(false)}>
              Close
            </Button>
          </div>
        </div>
      )}

      {/* TAIL-4: push-op status drawer */}
      {pushOpID && (
        <PushOpDrawer
          opID={pushOpID}
          open={pushOpOpen}
          onClose={() => { setPushOpOpen(false); setPushOpID(null) }}
        />
      )}
    </Section>
  )
}

// ─── TAIL-4: Push-op status polling drawer ────────────────────────────────────

function PushOpDrawer({ opID, open, onClose }: { opID: string; open: boolean; onClose: () => void }) {
  const { data: op, isLoading } = useQuery<SlurmPushOperation>({
    queryKey: ["slurm-push-op", opID],
    queryFn: () => apiFetch<SlurmPushOperation>(`/api/v1/slurm/push-ops/${opID}`),
    refetchInterval: (query) => {
      const d = query.state.data
      if (!d) return 2000
      // Stop polling when completed or failed.
      return (d.status === "completed" || d.status === "failed") ? false : 2000
    },
    enabled: open && !!opID,
    staleTime: 0,
  })

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-xl flex flex-col gap-0 p-0">
        <SheetHeader className="border-b border-border px-6 py-4">
          <SheetTitle className="text-sm">Sync push status</SheetTitle>
          <SheetDescription className="text-xs font-mono">{opID}</SheetDescription>
        </SheetHeader>
        <div className="flex-1 overflow-auto px-6 py-4 space-y-4">
          {isLoading && <div className="flex items-center gap-2 text-xs text-muted-foreground"><Loader2 className="h-3.5 w-3.5 animate-spin" />Loading…</div>}
          {op && (
            <>
              <div className="flex items-center gap-3">
                <PushOpStatusBadge status={op.status} />
                <span className="text-xs text-muted-foreground">
                  {op.node_count} node{op.node_count !== 1 ? "s" : ""}
                  {" · "}{op.success_count} ok
                  {op.failure_count > 0 && <span className="text-status-error"> · {op.failure_count} failed</span>}
                </span>
              </div>
              <div className="text-xs text-muted-foreground space-y-1">
                <div>Apply: <span className="font-mono">{op.apply_action}</span></div>
                <div>Files: {(op.filenames ?? []).join(", ") || "all"}</div>
                <div>Started: {fmtUnix(op.started_at)}</div>
                {op.completed_at && <div>Completed: {fmtUnix(op.completed_at)}</div>}
              </div>
              {/* Per-node results */}
              {op.node_results && Object.keys(op.node_results).length > 0 && (
                <div className="space-y-2">
                  <p className="text-xs font-medium">Node results</p>
                  {Object.entries(op.node_results).map(([nodeID, result]) => (
                    <div key={nodeID} className={cn(
                      "rounded border p-3 text-xs space-y-1",
                      result.ok ? "border-status-healthy/30 bg-status-healthy/5" : "border-status-error/30 bg-status-error/5"
                    )}>
                      <div className="flex items-center gap-2">
                        {result.ok
                          ? <CheckCircle2 className="h-3.5 w-3.5 text-status-healthy" />
                          : <XCircle className="h-3.5 w-3.5 text-status-error" />
                        }
                        <code className="font-mono">{nodeID.slice(0, 8)}…</code>
                        {result.error && <span className="text-status-error">{result.error}</span>}
                      </div>
                      {(result.file_results ?? []).map((fr) => (
                        <div key={fr.filename} className="flex items-center gap-2 pl-5 text-muted-foreground">
                          {fr.ok
                            ? <Check className="h-3 w-3 text-status-healthy" />
                            : <X className="h-3 w-3 text-status-error" />
                          }
                          <span className="font-mono">{fr.filename}</span>
                          {fr.error && <span className="text-status-error">{fr.error}</span>}
                        </div>
                      ))}
                    </div>
                  ))}
                </div>
              )}
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function PushOpStatusBadge({ status }: { status: string }) {
  const map: Record<string, { label: string; className: string; icon?: React.ReactNode }> = {
    pending:   { label: "Pending",   className: "bg-status-neutral/10 text-status-neutral border-status-neutral/30", icon: <Loader2 className="h-3 w-3 animate-spin" /> },
    running:   { label: "Running",   className: "bg-status-warning/10 text-status-warning border-status-warning/30", icon: <Loader2 className="h-3 w-3 animate-spin" /> },
    completed: { label: "Completed", className: "bg-status-healthy/10 text-status-healthy border-status-healthy/30", icon: <CheckCircle2 className="h-3 w-3" /> },
    failed:    { label: "Failed",    className: "bg-status-error/10 text-status-error border-status-error/30",       icon: <XCircle className="h-3 w-3" /> },
  }
  const cfg = map[status] ?? { label: status, className: "bg-muted/30 text-muted-foreground border-border" }
  return (
    <Badge variant="outline" className={cn("text-xs flex items-center gap-1", cfg.className)}>
      {cfg.icon}{cfg.label}
    </Badge>
  )
}

// ─── Configs section ──────────────────────────────────────────────────────────

interface ConfigEditorSheetProps {
  file: SlurmConfigFile | null
  open: boolean
  onClose: () => void
}

function ConfigEditorSheet({ file, open, onClose }: ConfigEditorSheetProps) {
  const qc = useQueryClient()
  const [content, setContent] = React.useState("")
  const [message, setMessage] = React.useState("")
  const [tab, setTab] = React.useState<"edit" | "history" | "preview">("edit")
  const [validation, setValidation] = React.useState<SlurmValidateResponse | null>(null)
  const [validating, setValidating] = React.useState(false)
  const [reseedConfirm, setReseedConfirm] = React.useState("")
  // TAIL-1: per-node render preview
  const [previewNodeID, setPreviewNodeID] = React.useState("")

  React.useEffect(() => {
    if (file) {
      setContent(file.content ?? "")
      setMessage("")
      setValidation(null)
      setTab("edit")
      setPreviewNodeID("")
    }
  }, [file])

  const { data: historyData } = useQuery<{ history: SlurmConfigFile[] }>({
    queryKey: ["slurm-config-history", file?.filename],
    queryFn: () => apiFetch(`/api/v1/slurm/configs/${file!.filename}/history`),
    enabled: open && !!file && tab === "history",
  })

  // TAIL-1: per-node render preview query.
  const { data: previewData, isFetching: previewFetching, error: previewError } = useQuery<SlurmRenderPreviewResponse>({
    queryKey: ["slurm-render-preview", file?.filename, previewNodeID],
    queryFn: () => apiFetch(`/api/v1/slurm/configs/${file!.filename}/render/${previewNodeID}`),
    enabled: open && !!file && tab === "preview" && previewNodeID.trim().length > 0,
    staleTime: 0,
    retry: false,
  })

  const saveMut = useMutation({
    mutationFn: () => apiFetch(`/api/v1/slurm/configs/${file!.filename}`, {
      method: "PUT",
      body: JSON.stringify({ content, message }),
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["slurm-configs"] })
      qc.invalidateQueries({ queryKey: ["slurm-config-history", file?.filename] })
      setMessage("")
      toast({ title: "Config saved", description: `${file?.filename} v${(file?.version ?? 0) + 1}` })
    },
    onError: (e: Error) => toast({ title: "Save failed", description: e.message, variant: "destructive" }),
  })

  const reseedMut = useMutation({
    mutationFn: () => apiFetch<{ reseeded: string[]; skipped: { filename: string; reason: string }[] }>("/api/v1/slurm/configs/reseed-defaults", { method: "POST" }),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ["slurm-configs"] })
      setReseedConfirm("")
      toast({ title: "Reseed complete", description: `${data.reseeded?.length ?? 0} files reseeded` })
    },
    onError: (e: Error) => toast({ title: "Reseed failed", description: e.message, variant: "destructive" }),
  })

  async function handleValidate() {
    if (!file) return
    setValidating(true)
    try {
      const res = await apiFetch<SlurmValidateResponse>(`/api/v1/slurm/configs/${file.filename}/validate`, {
        method: "POST",
        body: JSON.stringify({ content }),
      })
      setValidation(res)
    } catch (e) {
      toast({ title: "Validation error", description: String(e), variant: "destructive" })
    } finally {
      setValidating(false)
    }
  }

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-3xl flex flex-col gap-0 p-0">
        <SheetHeader className="border-b border-border px-6 py-4">
          <SheetTitle className="font-mono text-sm">{file?.filename ?? "Config"}</SheetTitle>
          <SheetDescription className="text-xs">
            Current version: {file?.version}  ·  {file?.path}
          </SheetDescription>
        </SheetHeader>

        <Tabs value={tab} onValueChange={(v) => setTab(v as "edit" | "history" | "preview")} className="flex-1 flex flex-col overflow-hidden">
          <TabsList className="mx-6 mt-4 w-fit">
            <TabsTrigger value="edit"><Code2 className="mr-1.5 h-3.5 w-3.5" />Edit</TabsTrigger>
            <TabsTrigger value="history"><History className="mr-1.5 h-3.5 w-3.5" />History</TabsTrigger>
            <TabsTrigger value="preview"><Eye className="mr-1.5 h-3.5 w-3.5" />Preview</TabsTrigger>
          </TabsList>

          <TabsContent value="edit" className="flex-1 flex flex-col gap-3 overflow-auto px-6 pb-6 mt-4">
            {/* Validation results */}
            {validation && (
              <div className={cn("rounded border p-3 text-xs",
                validation.valid ? "border-status-healthy/30 bg-status-healthy/5 text-status-healthy" : "border-status-error/30 bg-status-error/5")}>
                {validation.valid ? (
                  <span className="flex items-center gap-1.5"><CheckCircle2 className="h-3.5 w-3.5" />Valid — no issues found</span>
                ) : (
                  <div className="space-y-1">
                    <span className="flex items-center gap-1.5 text-status-error font-medium">
                      <XCircle className="h-3.5 w-3.5" />{validation.issues.length} issue{validation.issues.length !== 1 ? "s" : ""}
                    </span>
                    {validation.issues.map((iss, i) => (
                      <div key={i} className="font-mono text-xs text-foreground">
                        {iss.line ? `Line ${iss.line}: ` : ""}{iss.message}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            {/* Textarea editor */}
            <textarea
              className="flex-1 min-h-[400px] w-full rounded border border-border bg-background font-mono text-xs leading-5 p-3 resize-none focus:outline-none focus:ring-1 focus:ring-ring"
              value={content}
              onChange={(e) => { setContent(e.target.value); setValidation(null) }}
              spellCheck={false}
              style={{ fontFamily: "'JetBrains Mono', monospace" }}
            />

            {/* Commit message */}
            <Input
              placeholder="Change description (optional)"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              className="text-sm"
            />

            <div className="flex items-center gap-2 flex-wrap">
              <Button size="sm" variant="outline" onClick={handleValidate} disabled={validating}>
                {validating ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : <CheckCircle2 className="mr-1.5 h-3.5 w-3.5" />}
                Validate
              </Button>
              <Button size="sm" onClick={() => saveMut.mutate()} disabled={saveMut.isPending}>
                {saveMut.isPending ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : null}
                Save
              </Button>

              {/* Reseed defaults */}
              <div className="ml-auto flex items-center gap-2">
                <Input
                  className="h-8 w-36 text-xs"
                  placeholder={`type "reseed"`}
                  value={reseedConfirm}
                  onChange={(e) => setReseedConfirm(e.target.value)}
                />
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-xs"
                  onClick={() => reseedMut.mutate()}
                  disabled={reseedConfirm !== "reseed" || reseedMut.isPending}
                >
                  {reseedMut.isPending ? <Loader2 className="mr-1.5 h-3 w-3 animate-spin" /> : <RefreshCw className="mr-1 h-3 w-3" />}
                  Reseed defaults
                </Button>
              </div>
            </div>
          </TabsContent>

          <TabsContent value="history" className="flex-1 overflow-auto px-6 pb-6 mt-4">
            {historyData?.history?.length === 0 && (
              <p className="text-sm text-muted-foreground">No history yet.</p>
            )}
            <div className="space-y-2">
              {historyData?.history?.map((h) => (
                <div key={h.version} className="rounded border border-border p-3 text-xs space-y-1">
                  <div className="flex items-center justify-between">
                    <span className="font-medium">v{h.version}</span>
                    <span className="text-muted-foreground font-mono">{h.checksum?.slice(0, 12)}</span>
                  </div>
                  <Button
                    size="sm"
                    variant="outline"
                    className="text-xs h-6"
                    onClick={() => { setContent(h.content); setTab("edit"); setValidation(null) }}
                  >
                    Restore this version
                  </Button>
                </div>
              ))}
            </div>
          </TabsContent>

          {/* TAIL-1: per-node render preview tab */}
          <TabsContent value="preview" className="flex-1 flex flex-col gap-3 overflow-auto px-6 pb-6 mt-4">
            <p className="text-xs text-muted-foreground">
              Renders this config file with all template variables resolved for a specific node.
              Enter a node ID below to preview the exact content that would be deployed.
            </p>
            <div className="flex items-center gap-2">
              <Input
                className="h-8 flex-1 text-xs font-mono"
                placeholder="Node ID (UUID)"
                value={previewNodeID}
                onChange={(e) => setPreviewNodeID(e.target.value)}
              />
            </div>
            {previewFetching && (
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                Rendering…
              </div>
            )}
            {previewError && !previewFetching && (
              <div className="rounded border border-status-error/30 bg-status-error/5 px-3 py-2 text-xs text-status-error">
                {String(previewError)}
              </div>
            )}
            {previewData && !previewFetching && (
              <div className="space-y-2">
                <div className="flex items-center justify-between text-xs text-muted-foreground">
                  <span>Node: <code className="font-mono">{previewData.node_id}</code></span>
                  <span className="font-mono">sha256:{previewData.checksum?.slice(0, 12)}</span>
                </div>
                <textarea
                  readOnly
                  className="w-full min-h-[400px] rounded border border-border bg-secondary/10 font-mono text-xs leading-5 p-3 resize-none focus:outline-none"
                  value={previewData.rendered_content}
                  style={{ fontFamily: "'JetBrains Mono', monospace" }}
                />
              </div>
            )}
            {!previewNodeID.trim() && !previewData && !previewFetching && (
              <p className="text-xs text-muted-foreground italic">Enter a node ID above to render the preview.</p>
            )}
          </TabsContent>
        </Tabs>
      </SheetContent>
    </Sheet>
  )
}

function ConfigsSection() {
  const { data, isLoading } = useQuery<ListSlurmConfigsResponse>({
    queryKey: ["slurm-configs"],
    queryFn: () => apiFetch<ListSlurmConfigsResponse>("/api/v1/slurm/configs"),
    staleTime: 30000,
  })
  const [selected, setSelected] = React.useState<SlurmConfigFile | null>(null)

  return (
    <Section id="configs" icon={<FileText className="h-4 w-4 text-muted-foreground" />} title="Configs">
      {isLoading ? (
        <div className="space-y-2">{[...Array(4)].map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}</div>
      ) : (data?.configs ?? []).length === 0 ? (
        <p className="text-sm text-muted-foreground">No configs found. Enable the Slurm module first.</p>
      ) : (
        <div className="divide-y divide-border rounded border border-border">
          {(data?.configs ?? []).map((cfg) => (
            <button
              key={cfg.filename}
              className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-secondary/40 transition-colors"
              onClick={() => setSelected(cfg)}
            >
              <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <span className="flex-1 font-mono text-sm">{cfg.filename}</span>
              <span className="text-xs text-muted-foreground">v{cfg.version}</span>
              <span className="text-xs text-muted-foreground font-mono">{cfg.checksum?.slice(0, 10)}</span>
              <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
            </button>
          ))}
        </div>
      )}
      <ConfigEditorSheet file={selected} open={!!selected} onClose={() => setSelected(null)} />
    </Section>
  )
}

// ─── Roles section ────────────────────────────────────────────────────────────

function RolesSection() {
  const qc = useQueryClient()
  const { data: nodesData, isLoading: nodesLoading } = useQuery<ListSlurmNodesResponse>({
    queryKey: ["slurm-nodes"],
    queryFn: () => apiFetch<ListSlurmNodesResponse>("/api/v1/slurm/nodes"),
    staleTime: 20000,
  })
  const { data: roleSummary } = useQuery<ListSlurmRoleSummaryResponse>({
    queryKey: ["slurm-role-summary"],
    queryFn: () => apiFetch<ListSlurmRoleSummaryResponse>("/api/v1/slurm/roles/summary"),
    staleTime: 20000,
  })

  const [editingNode, setEditingNode] = React.useState<string | null>(null)
  const [selectedRoles, setSelectedRoles] = React.useState<string[]>([])
  const allRoles = ["controller", "worker", "dbd", "login"]

  const setRoleMut = useMutation({
    mutationFn: ({ nodeId, roles }: { nodeId: string; roles: string[] }) =>
      apiFetch(`/api/v1/nodes/${nodeId}/slurm/role`, {
        method: "PUT",
        body: JSON.stringify({ roles }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["slurm-nodes"] })
      qc.invalidateQueries({ queryKey: ["slurm-role-summary"] })
      setEditingNode(null)
      toast({ title: "Role updated" })
    },
    onError: (e: Error) => toast({ title: "Update failed", description: e.message, variant: "destructive" }),
  })

  return (
    <Section id="roles" icon={<Wrench className="h-4 w-4 text-muted-foreground" />} title="Roles">
      {/* Role summary cards */}
      {(roleSummary?.summary ?? []).length > 0 && (
        <div className="mb-4 flex flex-wrap gap-3">
          {(roleSummary?.summary ?? []).map((r) => (
            <div key={r.role} className="rounded border border-border bg-background px-3 py-2 text-center min-w-20">
              <div className="text-xl font-bold tabular-nums">{r.count}</div>
              <div className="text-xs text-muted-foreground capitalize">{r.role}</div>
            </div>
          ))}
        </div>
      )}

      {nodesLoading ? (
        <div className="space-y-2">{[...Array(3)].map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}</div>
      ) : (nodesData?.nodes ?? []).length === 0 ? (
        <p className="text-sm text-muted-foreground">No nodes with Slurm roles assigned yet.</p>
      ) : (
        <div className="divide-y divide-border rounded border border-border">
          {(nodesData?.nodes ?? []).map((n) => (
            <div key={n.node_id} className="flex items-center gap-3 px-3 py-2.5">
              <span className="flex-1 font-mono text-xs text-muted-foreground truncate">{n.node_id.slice(0, 8)}</span>
              <div className="flex flex-wrap gap-1">
                {n.roles?.map((r) => (
                  <Badge key={r} variant="secondary" className="text-xs capitalize">{r}</Badge>
                ))}
                {(!n.roles || n.roles.length === 0) && <span className="text-xs text-muted-foreground">—</span>}
              </div>
              <span className={cn("h-2 w-2 rounded-full shrink-0", n.connected ? "bg-status-healthy" : "bg-status-neutral")} title={n.connected ? "Connected" : "Disconnected"} />

              {editingNode === n.node_id ? (
                <div className="flex items-center gap-2 ml-2">
                  <div className="flex gap-1">
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
                  <Button
                    size="sm"
                    className="h-7 text-xs"
                    onClick={() => setRoleMut.mutate({ nodeId: n.node_id, roles: selectedRoles })}
                    disabled={setRoleMut.isPending}
                  >
                    {setRoleMut.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Check className="h-3 w-3" />}
                  </Button>
                  <Button size="sm" variant="ghost" className="h-7 text-xs" onClick={() => setEditingNode(null)}>
                    <X className="h-3 w-3" />
                  </Button>
                </div>
              ) : (
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 text-xs"
                  onClick={() => { setEditingNode(n.node_id); setSelectedRoles(n.roles ?? []) }}
                >
                  Edit
                </Button>
              )}
            </div>
          ))}
        </div>
      )}
    </Section>
  )
}

// ─── Scripts section ──────────────────────────────────────────────────────────

interface ScriptEditorSheetProps {
  scriptType: string | null
  open: boolean
  onClose: () => void
}

function ScriptEditorSheet({ scriptType, open, onClose }: ScriptEditorSheetProps) {
  const qc = useQueryClient()
  const [content, setContent] = React.useState("")
  const [destPath, setDestPath] = React.useState("")
  const [message, setMessage] = React.useState("")
  const [tab, setTab] = React.useState<"edit" | "history" | "config">("edit")

  const { data: scriptData } = useQuery<SlurmScriptFile>({
    queryKey: ["slurm-script", scriptType],
    queryFn: () => apiFetch<SlurmScriptFile>(`/api/v1/slurm/scripts/${scriptType}`),
    enabled: open && !!scriptType,
  })

  React.useEffect(() => {
    if (scriptData) {
      setContent(scriptData.content ?? "")
      setDestPath(scriptData.dest_path ?? "")
    } else if (open && scriptType) {
      setContent("#!/bin/bash\n# Slurm " + scriptType + " script\n")
      setDestPath(`/etc/slurm/${scriptType.toLowerCase()}.sh`)
    }
  }, [scriptData, open, scriptType])

  const { data: historyData } = useQuery<{ history: SlurmScriptFile[] }>({
    queryKey: ["slurm-script-history", scriptType],
    queryFn: () => apiFetch(`/api/v1/slurm/scripts/${scriptType}/history`),
    enabled: open && !!scriptType && tab === "history",
  })

  const saveMut = useMutation({
    mutationFn: () => apiFetch(`/api/v1/slurm/scripts/${scriptType}`, {
      method: "PUT",
      body: JSON.stringify({ content, dest_path: destPath, message }),
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["slurm-scripts"] })
      qc.invalidateQueries({ queryKey: ["slurm-script", scriptType] })
      toast({ title: "Script saved" })
    },
    onError: (e: Error) => toast({ title: "Save failed", description: e.message, variant: "destructive" }),
  })

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-2xl flex flex-col gap-0 p-0">
        <SheetHeader className="border-b border-border px-6 py-4">
          <SheetTitle className="font-mono text-sm">{scriptType}</SheetTitle>
          <SheetDescription className="text-xs">Slurm script editor</SheetDescription>
        </SheetHeader>

        <Tabs value={tab} onValueChange={(v) => setTab(v as "edit" | "history" | "config")} className="flex-1 flex flex-col overflow-hidden">
          <TabsList className="mx-6 mt-4 w-fit">
            <TabsTrigger value="edit"><Code2 className="mr-1.5 h-3.5 w-3.5" />Edit</TabsTrigger>
            <TabsTrigger value="history"><History className="mr-1.5 h-3.5 w-3.5" />History</TabsTrigger>
          </TabsList>

          <TabsContent value="edit" className="flex-1 flex flex-col gap-3 overflow-auto px-6 pb-6 mt-4">
            <div className="flex items-center gap-2">
              <span className="text-xs text-muted-foreground w-20">Dest path:</span>
              <Input
                className="font-mono text-xs flex-1"
                value={destPath}
                onChange={(e) => setDestPath(e.target.value)}
                placeholder="/etc/slurm/prolog.sh"
              />
            </div>
            <textarea
              className="flex-1 min-h-[360px] w-full rounded border border-border bg-background font-mono text-xs leading-5 p-3 resize-none focus:outline-none focus:ring-1 focus:ring-ring"
              value={content}
              onChange={(e) => setContent(e.target.value)}
              spellCheck={false}
              style={{ fontFamily: "'JetBrains Mono', monospace" }}
            />
            <Input
              placeholder="Change description (optional)"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              className="text-sm"
            />
            <div className="flex gap-2">
              <Button size="sm" onClick={() => saveMut.mutate()} disabled={saveMut.isPending || !destPath}>
                {saveMut.isPending ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : null}
                Save
              </Button>
            </div>
          </TabsContent>

          <TabsContent value="history" className="flex-1 overflow-auto px-6 pb-6 mt-4">
            <div className="space-y-2">
              {historyData?.history?.map((h) => (
                <div key={h.version} className="rounded border border-border p-3 text-xs space-y-1">
                  <div className="flex items-center justify-between">
                    <span className="font-medium">v{h.version}</span>
                    <span className="text-muted-foreground font-mono">{h.dest_path}</span>
                  </div>
                  <Button
                    size="sm"
                    variant="outline"
                    className="text-xs h-6"
                    onClick={() => { setContent(h.content); setTab("edit") }}
                  >
                    Restore this version
                  </Button>
                </div>
              ))}
            </div>
          </TabsContent>
        </Tabs>
      </SheetContent>
    </Sheet>
  )
}

function ScriptsSection() {
  const { data, isLoading } = useQuery<ListSlurmScriptsResponse>({
    queryKey: ["slurm-scripts"],
    queryFn: () => apiFetch<ListSlurmScriptsResponse>("/api/v1/slurm/scripts"),
    staleTime: 30000,
  })
  const [selected, setSelected] = React.useState<string | null>(null)

  return (
    <Section id="scripts" icon={<ScrollText className="h-4 w-4 text-muted-foreground" />} title="Scripts">
      {isLoading ? (
        <div className="space-y-2">{[...Array(5)].map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}</div>
      ) : (data?.scripts ?? []).length === 0 ? (
        <p className="text-sm text-muted-foreground">No scripts found. Enable the Slurm module first.</p>
      ) : (
        <div className="divide-y divide-border rounded border border-border">
          {(data?.scripts ?? []).map((s) => (
            <button
              key={s.script_type}
              className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-secondary/40 transition-colors"
              onClick={() => setSelected(s.script_type)}
            >
              <TerminalSquare className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <span className="flex-1 font-mono text-sm">{s.script_type}</span>
              {s.has_content ? (
                <Badge variant="secondary" className="text-xs">v{s.version}</Badge>
              ) : (
                <span className="text-xs text-muted-foreground">no content</span>
              )}
              <span className={cn("h-2 w-2 rounded-full shrink-0", s.enabled ? "bg-status-healthy" : "bg-status-neutral")} title={s.enabled ? "Enabled" : "Disabled"} />
              <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
            </button>
          ))}
        </div>
      )}
      <ScriptEditorSheet scriptType={selected} open={!!selected} onClose={() => setSelected(null)} />
    </Section>
  )
}

// ─── Builds section ───────────────────────────────────────────────────────────

interface BuildLogPanelProps {
  buildId: string
  open: boolean
}

function BuildLogPanel({ buildId, open }: BuildLogPanelProps) {
  const [lines, setLines] = React.useState<string[]>([])
  const [done, setDone] = React.useState(false)
  const bottomRef = React.useRef<HTMLDivElement>(null)

  React.useEffect(() => {
    if (!open || !buildId) return
    setLines([])
    setDone(false)

    const url = sseUrl(`/api/v1/slurm/builds/${buildId}/log-stream`)
    const es = new EventSource(url, { withCredentials: true })

    es.onmessage = (ev) => {
      try {
        const data = JSON.parse(ev.data) as SlurmBuildLogEvent
        if (data.line) setLines((prev) => [...prev, data.line!])
      } catch { /* ignore */ }
    }

    es.addEventListener("done", () => {
      setDone(true)
      es.close()
    })

    es.onerror = () => {
      es.close()
    }

    return () => es.close()
  }, [buildId, open])

  // Auto-scroll to bottom when new lines arrive.
  React.useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [lines])

  return (
    <div className="mt-3 rounded border border-border bg-[#0d0d0d] p-3">
      <div className="max-h-64 overflow-y-auto text-xs font-mono leading-5 text-green-400">
        {lines.length === 0 && !done && (
          <span className="text-muted-foreground flex items-center gap-1.5">
            <Loader2 className="h-3 w-3 animate-spin" />Connecting to log stream…
          </span>
        )}
        {lines.map((l, i) => <div key={i}>{l}</div>)}
        {done && <div className="text-muted-foreground mt-1">— build finished —</div>}
        <div ref={bottomRef} />
      </div>
    </div>
  )
}

interface BuildDetailSheetProps {
  build: SlurmBuild | null
  open: boolean
  onClose: () => void
}

function BuildDetailSheet({ build, open, onClose }: BuildDetailSheetProps) {
  const qc = useQueryClient()
  const [cancelConfirm, setCancelConfirm] = React.useState("")

  // Sprint 17 #107 — recovery install state.
  const [advancedOpen, setAdvancedOpen] = React.useState(false)
  const [recoveryNodeId, setRecoveryNodeId] = React.useState("")
  const [recoveryConfirm, setRecoveryConfirm] = React.useState("")

  const { data: nodesData } = useQuery<ListSlurmNodesResponse>({
    queryKey: ["slurm-nodes"],
    queryFn: () => apiFetch<ListSlurmNodesResponse>("/api/v1/slurm/nodes"),
    enabled: advancedOpen,
  })
  const slurmNodes: SlurmNodeEntry[] = nodesData?.nodes ?? []

  const deleteMut = useMutation({
    mutationFn: () => apiFetch(`/api/v1/slurm/builds/${build!.id}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["slurm-builds"] })
      toast({ title: "Build deleted" })
      onClose()
    },
    onError: (e: Error) => toast({ title: "Delete failed", description: e.message, variant: "destructive" }),
  })

  const setActiveMut = useMutation({
    mutationFn: () => apiFetch(`/api/v1/slurm/builds/${build!.id}/set-active`, { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["slurm-builds"] })
      toast({ title: "Active build updated" })
    },
    onError: (e: Error) => toast({ title: "Failed", description: e.message, variant: "destructive" }),
  })

  const recoveryMut = useMutation({
    mutationFn: () => apiFetch(`/api/v1/nodes/${recoveryNodeId}/slurm/recovery-install`, {
      method: "POST",
      body: JSON.stringify({ build_id: build!.id }),
    }),
    onSuccess: () => {
      toast({ title: "Recovery install complete", description: `Node ${recoveryNodeId.slice(0, 8)} updated via artifact fallback` })
      setAdvancedOpen(false)
      setRecoveryNodeId("")
      setRecoveryConfirm("")
    },
    onError: (e: Error) => toast({ title: "Recovery install failed", description: e.message, variant: "destructive" }),
  })

  if (!build) return null

  const canRecover = build.status === "completed" && recoveryNodeId !== "" && recoveryConfirm === "recover"

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-2xl flex flex-col gap-0 p-0">
        <SheetHeader className="border-b border-border px-6 py-4">
          <SheetTitle>Build {build.id.slice(0, 8)}</SheetTitle>
          <SheetDescription>Slurm {build.version} · {build.arch}</SheetDescription>
        </SheetHeader>

        <div className="flex-1 overflow-auto px-6 py-4 space-y-4">
          <div className="grid grid-cols-2 gap-3 text-sm">
            <div><span className="text-muted-foreground text-xs">Status</span><div><BuildStatusBadge status={build.status} /></div></div>
            <div><span className="text-muted-foreground text-xs">Version</span><div className="font-medium">{build.version}</div></div>
            <div><span className="text-muted-foreground text-xs">Architecture</span><div className="font-medium">{build.arch}</div></div>
            <div><span className="text-muted-foreground text-xs">Started</span><div className="text-xs">{fmtUnix(build.started_at)}</div></div>
            {build.completed_at && (
              <div><span className="text-muted-foreground text-xs">Completed</span><div className="text-xs">{fmtUnix(build.completed_at)}</div></div>
            )}
            {build.artifact_size && (
              <div><span className="text-muted-foreground text-xs">Artifact size</span><div className="text-xs">{fmtBytes(build.artifact_size)}</div></div>
            )}
            {build.artifact_checksum && (
              <div className="col-span-2">
                <span className="text-muted-foreground text-xs">SHA256</span>
                <div className="font-mono text-xs break-all">{build.artifact_checksum}</div>
              </div>
            )}
            {build.configure_flags && build.configure_flags.length > 0 && (
              <div className="col-span-2">
                <span className="text-muted-foreground text-xs">Configure flags</span>
                <div className="font-mono text-xs">{build.configure_flags.join(" ")}</div>
              </div>
            )}
            {build.error_message && (
              <div className="col-span-2">
                <span className="text-muted-foreground text-xs">Error</span>
                <div className="text-xs text-status-error">{build.error_message}</div>
              </div>
            )}
          </div>

          {/* Live log for in-progress builds */}
          {build.status === "building" && (
            <BuildLogPanel buildId={build.id} open={open} />
          )}

          {/* Actions */}
          <div className="flex flex-wrap gap-2 pt-2 border-t border-border">
            {build.status === "completed" && !build.is_active && (
              <Button size="sm" onClick={() => setActiveMut.mutate()} disabled={setActiveMut.isPending}>
                {setActiveMut.isPending ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : <CheckCircle2 className="mr-1.5 h-3.5 w-3.5" />}
                Set active
              </Button>
            )}
            {build.is_active && (
              <Badge variant="secondary" className="text-xs">Active build</Badge>
            )}
            <div className="ml-auto flex items-center gap-2">
              <Input
                className="h-8 w-32 text-xs"
                placeholder={`type "delete"`}
                value={cancelConfirm}
                onChange={(e) => setCancelConfirm(e.target.value)}
              />
              <Button
                size="sm"
                variant="destructive"
                onClick={() => deleteMut.mutate()}
                disabled={cancelConfirm !== "delete" || deleteMut.isPending || build.is_active}
                title={build.is_active ? "Cannot delete the active build" : ""}
              >
                {deleteMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <X className="mr-1.5 h-3.5 w-3.5" />}
                Delete
              </Button>
            </div>
          </div>

          {/* Advanced — recovery install (Sprint 17 #107 part 5) */}
          {build.status === "completed" && (
            <div className="border border-amber-500/30 rounded-md">
              <button
                type="button"
                className="w-full flex items-center gap-2 px-4 py-2.5 text-xs font-medium text-amber-400 hover:text-amber-300 transition-colors"
                onClick={() => setAdvancedOpen((v) => !v)}
                aria-expanded={advancedOpen}
              >
                <ShieldAlert className="h-3.5 w-3.5 shrink-0" />
                Advanced
                <ChevronDown className={cn("h-3.5 w-3.5 ml-auto transition-transform", advancedOpen && "rotate-180")} />
              </button>
              {advancedOpen && (
                <div className="border-t border-amber-500/30 px-4 py-3 space-y-3">
                  <p className="text-xs text-muted-foreground">
                    <strong className="text-amber-400">Recovery: install from raw artifact.</strong>{" "}
                    Bypasses dnf and installs directly from the build tarball.
                    Use only when the yum repo or dnf itself is broken on the target node.
                    This operation is audit-logged.
                  </p>
                  <div className="space-y-2">
                    <label className="text-xs text-muted-foreground block">Target node</label>
                    <select
                      className="w-full h-8 rounded-md border border-border bg-background px-2 text-xs"
                      value={recoveryNodeId}
                      onChange={(e) => setRecoveryNodeId(e.target.value)}
                    >
                      <option value="">Select a node…</option>
                      {slurmNodes.map((n) => (
                        <option key={n.node_id} value={n.node_id}>
                          {n.node_id.slice(0, 8)} {n.connected ? "" : "(offline)"}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div className="space-y-1">
                    <label className="text-xs text-muted-foreground block">Type <code className="font-mono bg-secondary px-1 rounded">recover</code> to confirm</label>
                    <Input
                      className="h-8 text-xs"
                      placeholder='type "recover"'
                      value={recoveryConfirm}
                      onChange={(e) => setRecoveryConfirm(e.target.value)}
                    />
                  </div>
                  <Button
                    size="sm"
                    variant="destructive"
                    className="w-full"
                    disabled={!canRecover || recoveryMut.isPending}
                    onClick={() => recoveryMut.mutate()}
                  >
                    {recoveryMut.isPending
                      ? <><Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />Installing (may take up to 30 min)…</>
                      : <><ShieldAlert className="mr-1.5 h-3.5 w-3.5" />Recovery install on node</>
                    }
                  </Button>
                </div>
              )}
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}

interface NewBuildSheetProps {
  open: boolean
  onClose: () => void
}

function NewBuildSheet({ open, onClose }: NewBuildSheetProps) {
  const qc = useQueryClient()
  const [version, setVersion] = React.useState("")
  const [arch, setArch] = React.useState("x86_64")
  const [flags, setFlags] = React.useState("")
  const [activeBuildId, setActiveBuildId] = React.useState<string | null>(null)

  const startMut = useMutation({
    mutationFn: () => apiFetch<{ build_id: string }>("/api/v1/slurm/builds", {
      method: "POST",
      body: JSON.stringify({
        slurm_version: version,
        arch,
        configure_flags: flags ? flags.split(/\s+/).filter(Boolean) : [],
      }),
    }),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ["slurm-builds"] })
      setActiveBuildId(data.build_id)
      toast({ title: "Build started", description: `Slurm ${version}` })
    },
    onError: (e: Error) => toast({ title: "Build failed to start", description: e.message, variant: "destructive" }),
  })

  function handleClose() {
    setVersion(""); setArch("x86_64"); setFlags(""); setActiveBuildId(null)
    onClose()
  }

  return (
    <Sheet open={open} onOpenChange={(v) => !v && handleClose()}>
      <SheetContent side="right" className="w-full sm:max-w-xl flex flex-col gap-0 p-0">
        <SheetHeader className="border-b border-border px-6 py-4">
          <SheetTitle>New Slurm Build</SheetTitle>
          <SheetDescription>Compile Slurm from source and store the artifact.</SheetDescription>
        </SheetHeader>

        <div className="flex-1 overflow-auto px-6 py-4 space-y-4">
          <div className="space-y-3">
            <div>
              <label className="text-xs text-muted-foreground mb-1 block">Slurm version *</label>
              <Input
                placeholder="e.g. 24.05.3 or 23.11.10"
                value={version}
                onChange={(e) => setVersion(e.target.value)}
                className="font-mono text-sm"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground mb-1 block">Architecture</label>
              <Input
                value={arch}
                onChange={(e) => setArch(e.target.value)}
                className="font-mono text-sm"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground mb-1 block">Extra configure flags (space-separated, optional)</label>
              <Input
                placeholder="--with-pmix --with-ucx=/usr/local"
                value={flags}
                onChange={(e) => setFlags(e.target.value)}
                className="font-mono text-sm"
              />
            </div>
          </div>

          <Button
            className="w-full"
            onClick={() => startMut.mutate()}
            disabled={!version.trim() || startMut.isPending}
          >
            {startMut.isPending ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Play className="mr-2 h-4 w-4" />}
            Start build
          </Button>

          {/* Live log once build starts */}
          {activeBuildId && (
            <div>
              <p className="text-xs text-muted-foreground mb-1">Live log — build ID: <span className="font-mono">{activeBuildId.slice(0, 8)}</span></p>
              <BuildLogPanel buildId={activeBuildId} open={true} />
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function BuildsSection() {
  const { data, isLoading } = useQuery<ListSlurmBuildsResponse>({
    queryKey: ["slurm-builds"],
    queryFn: () => apiFetch<ListSlurmBuildsResponse>("/api/v1/slurm/builds"),
    refetchInterval: (q) => {
      // Poll faster when a build is in-progress.
      const builds = (q.state.data as ListSlurmBuildsResponse | undefined)?.builds ?? []
      return builds.some((b) => b.status === "building") ? 5000 : 30000
    },
  })
  const [selected, setSelected] = React.useState<SlurmBuild | null>(null)
  const [newBuildOpen, setNewBuildOpen] = React.useState(false)

  return (
    <Section id="builds" icon={<Package className="h-4 w-4 text-muted-foreground" />} title="Builds">
      <div className="mb-3 flex items-center justify-between">
        <p className="text-xs text-muted-foreground">{data?.total ?? 0} build{data?.total !== 1 ? "s" : ""}</p>
        <Button size="sm" onClick={() => setNewBuildOpen(true)}>
          <Play className="mr-1.5 h-3.5 w-3.5" />
          Build new
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-2">{[...Array(3)].map((_, i) => <Skeleton key={i} className="h-12 w-full" />)}</div>
      ) : (data?.builds ?? []).length === 0 ? (
        <p className="text-sm text-muted-foreground">No builds yet. Start one above.</p>
      ) : (
        <div className="divide-y divide-border rounded border border-border">
          {(data?.builds ?? []).map((b) => (
            <button
              key={b.id}
              className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-secondary/40 transition-colors"
              onClick={() => setSelected(b)}
            >
              <Package className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-sm">{b.version}</span>
                  <span className="text-xs text-muted-foreground">{b.arch}</span>
                  {b.is_active && <Badge variant="secondary" className="text-xs">active</Badge>}
                </div>
                <span className="text-xs text-muted-foreground">{fmtUnix(b.started_at)}</span>
              </div>
              <BuildStatusBadge status={b.status} />
              <ChevronRight className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
            </button>
          ))}
        </div>
      )}

      <BuildDetailSheet build={selected} open={!!selected} onClose={() => setSelected(null)} />
      <NewBuildSheet open={newBuildOpen} onClose={() => setNewBuildOpen(false)} />
    </Section>
  )
}

// ─── Upgrades section ─────────────────────────────────────────────────────────

const UPGRADE_PHASES = ["dbd", "controller", "compute", "login"] as const

function PhaseStepper({ currentPhase, status }: { currentPhase?: string; status: string }) {
  return (
    <div className="flex items-center gap-1">
      {UPGRADE_PHASES.map((phase, i) => {
        const idx = UPGRADE_PHASES.indexOf(currentPhase as (typeof UPGRADE_PHASES)[number])
        const done = idx > i || status === "completed"
        const active = idx === i && status === "in_progress"
        return (
          <React.Fragment key={phase}>
            <div className={cn(
              "flex items-center gap-1 rounded px-2 py-0.5 text-xs font-medium",
              done ? "bg-status-healthy/10 text-status-healthy" :
              active ? "bg-status-warning/10 text-status-warning" :
              "bg-muted/30 text-muted-foreground"
            )}>
              {done ? <Check className="h-3 w-3" /> : active ? <Loader2 className="h-3 w-3 animate-spin" /> : <span className="h-3 w-3 inline-flex items-center justify-center text-[10px]">{i + 1}</span>}
              <span className="capitalize">{phase}</span>
            </div>
            {i < UPGRADE_PHASES.length - 1 && <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />}
          </React.Fragment>
        )
      })}
    </div>
  )
}

interface StartUpgradeSheetProps {
  open: boolean
  onClose: () => void
  builds: SlurmBuild[]
}

function StartUpgradeSheet({ open, onClose, builds }: StartUpgradeSheetProps) {
  const qc = useQueryClient()
  const completedBuilds = builds.filter((b) => b.status === "completed")
  const [toBuildId, setToBuildId] = React.useState("")
  const [batchSize, setBatchSize] = React.useState(10)
  const [drainTimeout, setDrainTimeout] = React.useState(30)
  const [confirmedDbBackup, setConfirmedDbBackup] = React.useState(false)
  const [validation, setValidation] = React.useState<SlurmUpgradeValidation | null>(null)
  const [validating, setValidating] = React.useState(false)
  const [confirmText, setConfirmText] = React.useState("")

  React.useEffect(() => {
    if (open && completedBuilds.length > 0 && !toBuildId) {
      setToBuildId(completedBuilds[0].id)
    }
  }, [open, completedBuilds, toBuildId])

  async function handleValidate() {
    setValidating(true)
    try {
      const res = await apiFetch<SlurmUpgradeValidation>("/api/v1/slurm/upgrades/validate", {
        method: "POST",
        body: JSON.stringify({ to_build_id: toBuildId, batch_size: batchSize, drain_timeout_min: drainTimeout }),
      })
      setValidation(res)
    } catch (e) {
      toast({ title: "Validation failed", description: String(e), variant: "destructive" })
    } finally {
      setValidating(false)
    }
  }

  const startMut = useMutation({
    mutationFn: () => apiFetch<{ op_id: string }>("/api/v1/slurm/upgrades", {
      method: "POST",
      body: JSON.stringify({
        to_build_id: toBuildId,
        batch_size: batchSize,
        drain_timeout_min: drainTimeout,
        confirmed_db_backup: confirmedDbBackup,
      }),
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["slurm-upgrades"] })
      toast({ title: "Upgrade started", description: "DBD → controller → compute → login" })
      onClose()
    },
    onError: (e: Error) => toast({ title: "Start failed", description: e.message, variant: "destructive" }),
  })

  function handleClose() {
    setToBuildId(""); setValidation(null); setConfirmText(""); setConfirmedDbBackup(false); onClose()
  }

  return (
    <Sheet open={open} onOpenChange={(v) => !v && handleClose()}>
      <SheetContent side="right" className="w-full sm:max-w-xl flex flex-col gap-0 p-0">
        <SheetHeader className="border-b border-border px-6 py-4">
          <SheetTitle>Start Rolling Upgrade</SheetTitle>
          <SheetDescription>Phase order: DBD → controller → compute → login</SheetDescription>
        </SheetHeader>

        <div className="flex-1 overflow-auto px-6 py-4 space-y-4">
          <div className="space-y-3">
            <div>
              <label className="text-xs text-muted-foreground mb-1 block">Target build *</label>
              <select
                className="w-full rounded border border-border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
                value={toBuildId}
                onChange={(e) => { setToBuildId(e.target.value); setValidation(null) }}
              >
                {completedBuilds.map((b) => (
                  <option key={b.id} value={b.id}>
                    Slurm {b.version} ({b.arch}) — {b.id.slice(0, 8)}
                    {b.is_active ? " [active]" : ""}
                  </option>
                ))}
                {completedBuilds.length === 0 && (
                  <option value="" disabled>No completed builds — build one first</option>
                )}
              </select>
            </div>
            <div className="flex gap-3">
              <div className="flex-1">
                <label className="text-xs text-muted-foreground mb-1 block">Compute batch size</label>
                <Input
                  type="number"
                  min={1}
                  max={100}
                  value={batchSize}
                  onChange={(e) => setBatchSize(Number(e.target.value))}
                  className="text-sm"
                />
              </div>
              <div className="flex-1">
                <label className="text-xs text-muted-foreground mb-1 block">Drain timeout (min)</label>
                <Input
                  type="number"
                  min={1}
                  max={240}
                  value={drainTimeout}
                  onChange={(e) => setDrainTimeout(Number(e.target.value))}
                  className="text-sm"
                />
              </div>
            </div>
            <label className="flex items-center gap-2 text-sm cursor-pointer">
              <input
                type="checkbox"
                checked={confirmedDbBackup}
                onChange={(e) => setConfirmedDbBackup(e.target.checked)}
                className="rounded"
              />
              I have taken a database backup before upgrading
            </label>
          </div>

          {/* Validation panel */}
          <div className="space-y-2">
            <Button
              size="sm"
              variant="outline"
              onClick={handleValidate}
              disabled={!toBuildId || validating}
            >
              {validating ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : <CheckCircle2 className="mr-1.5 h-3.5 w-3.5" />}
              Validate upgrade plan
            </Button>

            {validation && (
              <div className={cn("rounded border p-3 text-xs space-y-2",
                validation.valid ? "border-status-healthy/30 bg-status-healthy/5" : "border-status-error/30 bg-status-error/5")}>
                <div className={cn("flex items-center gap-1.5 font-medium", validation.valid ? "text-status-healthy" : "text-status-error")}>
                  {validation.valid ? <CheckCircle2 className="h-3.5 w-3.5" /> : <XCircle className="h-3.5 w-3.5" />}
                  {validation.valid ? "Plan valid" : "Validation errors"}
                </div>
                {validation.from_version && (
                  <p className="text-muted-foreground">{validation.from_version} → {validation.to_version} · {validation.job_count} running jobs</p>
                )}
                {validation.warnings?.map((w, i) => (
                  <p key={i} className="text-status-warning flex items-start gap-1.5"><AlertCircle className="h-3.5 w-3.5 shrink-0 mt-0.5" />{w}</p>
                ))}
                {validation.errors?.map((e, i) => (
                  <p key={i} className="text-status-error flex items-start gap-1.5"><XCircle className="h-3.5 w-3.5 shrink-0 mt-0.5" />{e}</p>
                ))}
                {validation.upgrade_plan && (
                  <div className="pt-1 space-y-1">
                    <p className="font-medium text-foreground">Plan:</p>
                    {validation.upgrade_plan.dbd_nodes?.length > 0 && <p>DBD: {validation.upgrade_plan.dbd_nodes.join(", ")}</p>}
                    {validation.upgrade_plan.controller_nodes?.length > 0 && <p>Controller: {validation.upgrade_plan.controller_nodes.join(", ")}</p>}
                    {validation.upgrade_plan.compute_batches?.length > 0 && <p>Compute batches: {validation.upgrade_plan.compute_batches.length} × {batchSize}</p>}
                    {validation.upgrade_plan.login_nodes?.length > 0 && <p>Login: {validation.upgrade_plan.login_nodes.join(", ")}</p>}
                  </div>
                )}
              </div>
            )}
          </div>

          {/* Confirm + start */}
          <div className="border-t border-border pt-4 space-y-2">
            <Input
              placeholder={`type "upgrade" to confirm`}
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
              className="text-sm"
            />
            <Button
              className="w-full"
              onClick={() => startMut.mutate()}
              disabled={confirmText !== "upgrade" || !toBuildId || !validation?.valid || startMut.isPending}
            >
              {startMut.isPending ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <ArrowUpCircle className="mr-2 h-4 w-4" />}
              Start rolling upgrade
            </Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}

interface UpgradeDetailSheetProps {
  op: SlurmUpgradeOperation | null
  open: boolean
  onClose: () => void
}

function UpgradeDetailSheet({ op, open, onClose }: UpgradeDetailSheetProps) {
  const qc = useQueryClient()

  const pauseMut = useMutation({
    mutationFn: () => apiFetch(`/api/v1/slurm/upgrades/${op!.id}/pause`, { method: "POST" }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["slurm-upgrades"] }); toast({ title: "Upgrade paused" }) },
    onError: (e: Error) => toast({ title: "Failed", description: e.message, variant: "destructive" }),
  })
  const resumeMut = useMutation({
    mutationFn: () => apiFetch(`/api/v1/slurm/upgrades/${op!.id}/resume`, { method: "POST" }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["slurm-upgrades"] }); toast({ title: "Upgrade resumed" }) },
    onError: (e: Error) => toast({ title: "Failed", description: e.message, variant: "destructive" }),
  })
  const rollbackMut = useMutation({
    mutationFn: () => apiFetch(`/api/v1/slurm/upgrades/${op!.id}/rollback`, { method: "POST" }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["slurm-upgrades"] }); toast({ title: "Rollback initiated" }) },
    onError: (e: Error) => toast({ title: "Failed", description: e.message, variant: "destructive" }),
  })

  if (!op) return null

  const nodeResults = Object.entries(op.node_results ?? {})

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-2xl flex flex-col gap-0 p-0">
        <SheetHeader className="border-b border-border px-6 py-4">
          <SheetTitle>Upgrade {op.id.slice(0, 8)}</SheetTitle>
          <SheetDescription>→ build {op.to_build_id.slice(0, 8)} · initiated by {op.initiated_by}</SheetDescription>
        </SheetHeader>

        <div className="flex-1 overflow-auto px-6 py-4 space-y-4">
          <div className="flex items-center justify-between flex-wrap gap-2">
            <UpgradeStatusBadge status={op.status} />
            <PhaseStepper currentPhase={op.phase} status={op.status} />
          </div>

          {op.total_batches > 0 && (
            <div className="text-xs text-muted-foreground">
              Batch {op.current_batch} / {op.total_batches}
            </div>
          )}

          {/* Per-node results */}
          {nodeResults.length > 0 && (
            <div>
              <p className="text-xs font-medium mb-2">Node results</p>
              <div className="divide-y divide-border rounded border border-border">
                {nodeResults.map(([nodeId, res]) => (
                  <div key={nodeId} className="flex items-center gap-3 px-3 py-2 text-xs">
                    <span className={cn("h-2 w-2 rounded-full shrink-0", res.ok ? "bg-status-healthy" : "bg-status-error")} />
                    <span className="font-mono flex-1 truncate">{nodeId.slice(0, 8)}</span>
                    <Badge variant="secondary" className="capitalize">{res.phase}</Badge>
                    {!res.ok && res.error && <span className="text-status-error truncate max-w-40">{res.error}</span>}
                    {res.installed_version && <span className="text-muted-foreground">v{res.installed_version}</span>}
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Controls */}
          <div className="flex flex-wrap gap-2 border-t border-border pt-4">
            {op.status === "in_progress" && (
              <Button size="sm" variant="outline" onClick={() => pauseMut.mutate()} disabled={pauseMut.isPending}>
                {pauseMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
                Pause
              </Button>
            )}
            {op.status === "paused" && (
              <Button size="sm" onClick={() => resumeMut.mutate()} disabled={resumeMut.isPending}>
                {resumeMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
                Resume
              </Button>
            )}
            {(op.status === "in_progress" || op.status === "paused") && (
              <Button size="sm" variant="destructive" onClick={() => rollbackMut.mutate()} disabled={rollbackMut.isPending}>
                {rollbackMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
                Rollback
              </Button>
            )}
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}

function UpgradesSection() {
  const { data: buildsData } = useQuery<ListSlurmBuildsResponse>({
    queryKey: ["slurm-builds"],
    queryFn: () => apiFetch<ListSlurmBuildsResponse>("/api/v1/slurm/builds"),
    staleTime: 30000,
  })
  const { data, isLoading } = useQuery<ListSlurmUpgradesResponse>({
    queryKey: ["slurm-upgrades"],
    queryFn: () => apiFetch<ListSlurmUpgradesResponse>("/api/v1/slurm/upgrades"),
    refetchInterval: (q) => {
      const ops = (q.state.data as ListSlurmUpgradesResponse | undefined)?.operations ?? []
      return ops.some((o) => o.status === "in_progress" || o.status === "queued") ? 5000 : 30000
    },
  })
  const [selected, setSelected] = React.useState<SlurmUpgradeOperation | null>(null)
  const [newOpen, setNewOpen] = React.useState(false)

  return (
    <Section id="upgrades" icon={<ArrowUpCircle className="h-4 w-4 text-muted-foreground" />} title="Upgrades">
      <div className="mb-3 flex items-center justify-between">
        <p className="text-xs text-muted-foreground">{data?.total ?? 0} operation{data?.total !== 1 ? "s" : ""}</p>
        <Button size="sm" onClick={() => setNewOpen(true)}>
          <ArrowUpCircle className="mr-1.5 h-3.5 w-3.5" />
          Start upgrade
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-2">{[...Array(2)].map((_, i) => <Skeleton key={i} className="h-12 w-full" />)}</div>
      ) : (data?.operations ?? []).length === 0 ? (
        <p className="text-sm text-muted-foreground">No upgrades yet.</p>
      ) : (
        <div className="divide-y divide-border rounded border border-border">
          {(data?.operations ?? []).map((op) => (
            <button
              key={op.id}
              className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-secondary/40 transition-colors"
              onClick={() => setSelected(op)}
            >
              <ArrowUpCircle className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-xs text-muted-foreground font-mono">{op.id.slice(0, 8)}</span>
                  <span className="text-xs">→ {op.to_build_id.slice(0, 8)}</span>
                </div>
                <span className="text-xs text-muted-foreground">{fmtUnix(op.started_at)}</span>
              </div>
              <UpgradeStatusBadge status={op.status} />
              {op.phase && <Badge variant="secondary" className="text-xs capitalize">{op.phase}</Badge>}
              <ChevronRight className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
            </button>
          ))}
        </div>
      )}

      <UpgradeDetailSheet op={selected} open={!!selected} onClose={() => setSelected(null)} />
      <StartUpgradeSheet open={newOpen} onClose={() => setNewOpen(false)} builds={buildsData?.builds ?? []} />
    </Section>
  )
}

// ─── TAIL-3: Dep matrix viewer section ───────────────────────────────────────

function DepMatrixSection() {
  const { data, isLoading } = useQuery<SlurmDepMatrixResponse>({
    queryKey: ["slurm-dep-matrix"],
    queryFn: () => apiFetch<SlurmDepMatrixResponse>("/api/v1/slurm/deps/matrix"),
    staleTime: 60000,
  })

  return (
    <Section id="deps" icon={<Table2 className="h-4 w-4 text-muted-foreground" />} title="Dependency matrix">
      <p className="text-xs text-muted-foreground mb-3">
        Version constraints for Slurm dependencies used during the build pipeline.
      </p>
      {isLoading ? (
        <div className="space-y-2">{[...Array(4)].map((_, i) => <Skeleton key={i} className="h-8 w-full" />)}</div>
      ) : (data?.matrix ?? []).length === 0 ? (
        <p className="text-sm text-muted-foreground">No dependency matrix entries.</p>
      ) : (
        <div className="overflow-auto rounded border border-border">
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b border-border bg-secondary/30">
                <th className="px-3 py-2 text-left font-medium text-muted-foreground">Slurm range</th>
                <th className="px-3 py-2 text-left font-medium text-muted-foreground">Dependency</th>
                <th className="px-3 py-2 text-left font-medium text-muted-foreground">Dep version range</th>
                <th className="px-3 py-2 text-left font-medium text-muted-foreground">Source</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {(data?.matrix ?? []).map((row) => (
                <tr key={row.id} className="hover:bg-secondary/20">
                  <td className="px-3 py-2 font-mono">
                    {row.slurm_version_min}–{row.slurm_version_max}
                  </td>
                  <td className="px-3 py-2 font-medium">{row.dep_name}</td>
                  <td className="px-3 py-2 font-mono text-muted-foreground">
                    {row.dep_version_min}–{row.dep_version_max}
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">{row.source}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  )
}

// ─── Sprint 24 #153: Job state badge ─────────────────────────────────────────

function JobStateBadge({ state }: { state: string }) {
  const map: Record<string, string> = {
    RUNNING:    "bg-status-healthy/10 text-status-healthy border-status-healthy/30",
    PENDING:    "bg-status-warning/10 text-status-warning border-status-warning/30",
    COMPLETED:  "bg-status-neutral/10 text-status-neutral border-status-neutral/30",
    FAILED:     "bg-status-error/10 text-status-error border-status-error/30",
    CANCELLED:  "bg-status-neutral/10 text-status-neutral border-status-neutral/30",
    TIMEOUT:    "bg-status-error/10 text-status-error border-status-error/30",
  }
  return (
    <span className={cn(
      "inline-flex items-center rounded border px-1.5 py-0.5 text-xs font-medium",
      map[state.toUpperCase()] ?? "bg-muted/30 text-muted-foreground border-border",
    )}>
      {state}
    </span>
  )
}

// ─── Sprint 24 #153: Jobs tab ─────────────────────────────────────────────────

type SortDir = "asc" | "desc"

function JobsTab() {
  const qc = useQueryClient()
  const [stateFilter, setStateFilter] = React.useState("all")
  const [userFilter, setUserFilter] = React.useState("")
  const [partitionFilter, setPartitionFilter] = React.useState("all")
  const [page, setPage] = React.useState(0)
  const [sortCol, setSortCol] = React.useState<keyof SlurmJob>("job_id")
  const [sortDir, setSortDir] = React.useState<SortDir>("asc")
  const [expandedId, setExpandedId] = React.useState<string | null>(null)

  const PER_PAGE = 50

  const { data, isLoading, isFetching } = useQuery<ListSlurmJobsResponse>({
    queryKey: ["slurm-jobs"],
    queryFn: () => apiFetch<ListSlurmJobsResponse>("/api/v1/slurm/jobs"),
    refetchInterval: 5000,
  })

  const jobs = data?.jobs ?? []

  // Derive partition list for filter dropdown.
  const partitions = React.useMemo(() => {
    const s = new Set(jobs.map((j) => j.partition).filter(Boolean))
    return Array.from(s).sort()
  }, [jobs])

  // Filter.
  const filtered = React.useMemo(() => {
    return jobs.filter((j) => {
      if (stateFilter !== "all" && j.state.toUpperCase() !== stateFilter.toUpperCase()) return false
      if (userFilter && !j.user.toLowerCase().includes(userFilter.toLowerCase())) return false
      if (partitionFilter !== "all" && j.partition !== partitionFilter) return false
      return true
    })
  }, [jobs, stateFilter, userFilter, partitionFilter])

  // Sort.
  const sorted = React.useMemo(() => {
    return [...filtered].sort((a, b) => {
      const av = a[sortCol] ?? ""
      const bv = b[sortCol] ?? ""
      const cmp = String(av).localeCompare(String(bv), undefined, { numeric: true })
      return sortDir === "asc" ? cmp : -cmp
    })
  }, [filtered, sortCol, sortDir])

  // Paginate.
  const totalPages = Math.max(1, Math.ceil(sorted.length / PER_PAGE))
  const pageJobs = sorted.slice(page * PER_PAGE, (page + 1) * PER_PAGE)

  function toggleSort(col: keyof SlurmJob) {
    if (sortCol === col) {
      setSortDir((d) => (d === "asc" ? "desc" : "asc"))
    } else {
      setSortCol(col)
      setSortDir("asc")
    }
    setPage(0)
  }

  function SortIcon({ col }: { col: keyof SlurmJob }) {
    if (sortCol !== col) return <span className="ml-1 opacity-30">↕</span>
    return <span className="ml-1">{sortDir === "asc" ? "↑" : "↓"}</span>
  }

  const selectCls = "h-8 rounded border border-border bg-background px-2 text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-ring"

  if (isLoading) {
    return (
      <div className="space-y-2 p-1">
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-8 w-full" />
        ))}
      </div>
    )
  }

  return (
    <div className="space-y-3">
      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        <select
          value={stateFilter}
          onChange={(e) => { setStateFilter(e.target.value); setPage(0) }}
          className={selectCls}
        >
          <option value="all">All states</option>
          <option value="RUNNING">Running</option>
          <option value="PENDING">Pending</option>
          <option value="COMPLETED">Completed</option>
          <option value="FAILED">Failed</option>
          <option value="CANCELLED">Cancelled</option>
        </select>

        <Input
          placeholder="Filter by user…"
          value={userFilter}
          onChange={(e) => { setUserFilter(e.target.value); setPage(0) }}
          className="h-8 w-40 text-xs"
        />

        <select
          value={partitionFilter}
          onChange={(e) => { setPartitionFilter(e.target.value); setPage(0) }}
          className={selectCls}
        >
          <option value="all">All partitions</option>
          {partitions.map((p) => (
            <option key={p} value={p}>{p}</option>
          ))}
        </select>

        <Button
          variant="outline"
          size="sm"
          className="h-8 gap-1.5"
          onClick={() => qc.invalidateQueries({ queryKey: ["slurm-jobs"] })}
          disabled={isFetching}
        >
          <RefreshCw className={cn("h-3.5 w-3.5", isFetching && "animate-spin")} />
          Refresh
        </Button>

        <span className="ml-auto text-xs text-muted-foreground">
          {filtered.length} job{filtered.length !== 1 ? "s" : ""}
        </span>
      </div>

      {/* Table */}
      {pageJobs.length === 0 ? (
        <div className="rounded-lg border border-border bg-card px-6 py-12 text-center">
          <p className="text-sm text-muted-foreground">
            {jobs.length === 0
              ? "No jobs in the queue. Slurm module may not be enabled or slurmctld is unreachable."
              : "No jobs match the current filters."}
          </p>
        </div>
      ) : (
        <div className="rounded-lg border border-border overflow-hidden">
          <table className="w-full text-xs">
            <thead className="bg-muted/30">
              <tr>
                {(
                  [
                    { col: "job_id" as keyof SlurmJob,    label: "JobID" },
                    { col: "name" as keyof SlurmJob,      label: "Name" },
                    { col: "state" as keyof SlurmJob,     label: "State" },
                    { col: "user" as keyof SlurmJob,      label: "User" },
                    { col: "partition" as keyof SlurmJob, label: "Partition" },
                    { col: "num_nodes" as keyof SlurmJob, label: "Nodes" },
                    { col: "time_used" as keyof SlurmJob, label: "Time" },
                  ] as { col: keyof SlurmJob; label: string }[]
                ).map(({ col, label }) => (
                  <th
                    key={col}
                    onClick={() => toggleSort(col)}
                    className="cursor-pointer select-none px-3 py-2 text-left font-medium text-muted-foreground hover:text-foreground"
                  >
                    {label}<SortIcon col={col} />
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {pageJobs.map((job) => (
                <React.Fragment key={job.job_id}>
                  <tr
                    onClick={() => setExpandedId(expandedId === job.job_id ? null : job.job_id)}
                    className="cursor-pointer hover:bg-muted/20 transition-colors"
                  >
                    <td className="px-3 py-2 font-mono text-foreground">{job.job_id}</td>
                    <td className="px-3 py-2 max-w-[180px] truncate text-foreground" title={job.name}>{job.name || "—"}</td>
                    <td className="px-3 py-2"><JobStateBadge state={job.state} /></td>
                    <td className="px-3 py-2 text-muted-foreground">{job.user || "—"}</td>
                    <td className="px-3 py-2 text-muted-foreground">{job.partition || "—"}</td>
                    <td className="px-3 py-2 text-muted-foreground">{job.num_nodes || "—"}</td>
                    <td className="px-3 py-2 font-mono text-muted-foreground">
                      {job.time_used || "—"}
                      {job.time_limit && job.time_limit !== "N/A" && (
                        <span className="text-muted-foreground/50"> / {job.time_limit}</span>
                      )}
                    </td>
                  </tr>
                  {expandedId === job.job_id && (
                    <tr className="bg-muted/10">
                      <td colSpan={7} className="px-3 py-3">
                        <JobExpandedRow job={job} />
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Pagination */}
      {totalPages > 1 && (
        <div className="flex items-center gap-2 text-xs">
          <Button
            variant="outline" size="sm"
            className="h-7"
            disabled={page === 0}
            onClick={() => setPage((p) => p - 1)}
          >
            Previous
          </Button>
          <span className="text-muted-foreground">
            Page {page + 1} of {totalPages}
          </span>
          <Button
            variant="outline" size="sm"
            className="h-7"
            disabled={page >= totalPages - 1}
            onClick={() => setPage((p) => p + 1)}
          >
            Next
          </Button>
        </div>
      )}
    </div>
  )
}

// ─── Expanded job row ─────────────────────────────────────────────────────────

function JobExpandedRow({ job }: { job: SlurmJob }) {
  const nodeNames = job.node_list
    ? job.node_list.split(",").map((n) => n.trim()).filter(Boolean)
    : []

  return (
    <div className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
      <div className="space-y-1.5">
        <div className="font-semibold text-foreground mb-1">Resources</div>
        <Row label="CPUs" value={job.req_cpus || "—"} />
        <Row label="Memory" value={job.req_memory || "—"} />
        <Row label="Time limit" value={job.time_limit || "—"} />
        {job.reason && job.state.toUpperCase() === "PENDING" && (
          <Row label="Pending reason" value={job.reason} />
        )}
      </div>
      <div className="space-y-1.5">
        <div className="font-semibold text-foreground mb-1">Execution</div>
        {job.command && (
          <div>
            <span className="text-muted-foreground">Command</span>
            <pre className="mt-0.5 rounded bg-muted/30 px-2 py-1 font-mono text-[11px] break-all whitespace-pre-wrap">{job.command}</pre>
          </div>
        )}
        {nodeNames.length > 0 && (
          <div>
            <span className="text-muted-foreground">Allocated nodes</span>
            <div className="mt-1 flex flex-wrap gap-1">
              {nodeNames.map((n) => (
                <span
                  key={n}
                  className="rounded border border-border px-1.5 py-0.5 font-mono text-[11px] text-foreground"
                >
                  {n}
                </span>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <span className="w-28 shrink-0 text-muted-foreground">{label}</span>
      <span className="font-mono text-foreground">{value}</span>
    </div>
  )
}

// ─── Sprint 24 #153: Partitions tab ───────────────────────────────────────────

function PartitionsTab() {
  const qc = useQueryClient()
  const [sortCol, setSortCol] = React.useState<keyof SlurmPartitionInfo>("name")
  const [sortDir, setSortDir] = React.useState<SortDir>("asc")

  const { data, isLoading, isFetching } = useQuery<ListSlurmPartitionsResponse>({
    queryKey: ["slurm-partitions"],
    queryFn: () => apiFetch<ListSlurmPartitionsResponse>("/api/v1/slurm/partitions"),
    refetchInterval: 30000,
  })

  const partitions = data?.partitions ?? []

  const sorted = React.useMemo(() => {
    return [...partitions].sort((a, b) => {
      const av = a[sortCol]
      const bv = b[sortCol]
      const cmp = String(av ?? "").localeCompare(String(bv ?? ""), undefined, { numeric: true })
      return sortDir === "asc" ? cmp : -cmp
    })
  }, [partitions, sortCol, sortDir])

  function toggleSort(col: keyof SlurmPartitionInfo) {
    if (sortCol === col) {
      setSortDir((d) => (d === "asc" ? "desc" : "asc"))
    } else {
      setSortCol(col)
      setSortDir("asc")
    }
  }

  function SortIcon({ col }: { col: keyof SlurmPartitionInfo }) {
    if (sortCol !== col) return <span className="ml-1 opacity-30">↕</span>
    return <span className="ml-1">{sortDir === "asc" ? "↑" : "↓"}</span>
  }

  const cols: { col: keyof SlurmPartitionInfo; label: string }[] = [
    { col: "name",            label: "Name" },
    { col: "state",           label: "State" },
    { col: "total_nodes",     label: "Total" },
    { col: "allocated_nodes", label: "Allocated" },
    { col: "idle_nodes",      label: "Idle" },
    { col: "is_default",      label: "Default" },
    { col: "max_time",        label: "MaxTime" },
  ]

  if (isLoading) {
    return (
      <div className="space-y-2 p-1">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-8 w-full" />
        ))}
      </div>
    )
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          className="h-8 gap-1.5"
          onClick={() => qc.invalidateQueries({ queryKey: ["slurm-partitions"] })}
          disabled={isFetching}
        >
          <RefreshCw className={cn("h-3.5 w-3.5", isFetching && "animate-spin")} />
          Refresh
        </Button>
        <span className="ml-auto text-xs text-muted-foreground">
          {partitions.length} partition{partitions.length !== 1 ? "s" : ""}
        </span>
      </div>

      {sorted.length === 0 ? (
        <div className="rounded-lg border border-border bg-card px-6 py-12 text-center">
          <p className="text-sm text-muted-foreground">
            No partitions found. Slurm module may not be enabled or sinfo is unreachable.
          </p>
        </div>
      ) : (
        <div className="rounded-lg border border-border overflow-hidden">
          <table className="w-full text-xs">
            <thead className="bg-muted/30">
              <tr>
                {cols.map(({ col, label }) => (
                  <th
                    key={col}
                    onClick={() => toggleSort(col)}
                    className="cursor-pointer select-none px-3 py-2 text-left font-medium text-muted-foreground hover:text-foreground"
                  >
                    {label}<SortIcon col={col} />
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {sorted.map((p) => (
                <tr key={p.name} className="hover:bg-muted/20 transition-colors">
                  <td className="px-3 py-2 font-mono font-medium text-foreground">
                    {p.name}
                  </td>
                  <td className="px-3 py-2">
                    <PartitionStateBadge state={p.state} />
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">{p.total_nodes}</td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {p.allocated_nodes > 0 ? p.allocated_nodes : <span className="opacity-40">—</span>}
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {p.idle_nodes > 0 ? p.idle_nodes : <span className="opacity-40">—</span>}
                  </td>
                  <td className="px-3 py-2">
                    {p.is_default ? (
                      <span className="inline-flex items-center rounded border border-status-healthy/30 bg-status-healthy/10 px-1.5 py-0.5 text-[10px] font-medium text-status-healthy">
                        default
                      </span>
                    ) : (
                      <span className="text-muted-foreground/40">—</span>
                    )}
                  </td>
                  <td className="px-3 py-2 font-mono text-muted-foreground">{p.max_time || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function PartitionStateBadge({ state }: { state: string }) {
  const map: Record<string, string> = {
    up:     "bg-status-healthy/10 text-status-healthy border-status-healthy/30",
    down:   "bg-status-error/10 text-status-error border-status-error/30",
    drain:  "bg-status-warning/10 text-status-warning border-status-warning/30",
    drained:"bg-status-warning/10 text-status-warning border-status-warning/30",
    inact:  "bg-status-neutral/10 text-status-neutral border-status-neutral/30",
  }
  const lower = state.toLowerCase()
  return (
    <span className={cn(
      "inline-flex items-center rounded border px-1.5 py-0.5 text-xs font-medium",
      map[lower] ?? "bg-muted/30 text-muted-foreground border-border",
    )}>
      {state}
    </span>
  )
}

// ─── Main Slurm page ──────────────────────────────────────────────────────────

export function SlurmPage() {
  const [tab, setTab] = React.useState("overview")

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div>
        <h1 className="text-xl font-semibold flex items-center gap-2">
          <Cpu className="h-5 w-5" />
          Slurm
        </h1>
        <p className="text-sm text-muted-foreground mt-1">
          Manage Slurm configuration, builds, and rolling upgrades across your cluster.
        </p>
      </div>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList className="mb-2">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="jobs">Jobs</TabsTrigger>
          <TabsTrigger value="partitions">Partitions</TabsTrigger>
        </TabsList>

        {/* Overview: existing sections */}
        <TabsContent value="overview">
          <div className="space-y-6">
            {/* Jump links */}
            <nav className="flex flex-wrap gap-2 text-xs">
              {[
                { href: "#status",   label: "Status" },
                { href: "#configs",  label: "Configs" },
                { href: "#roles",    label: "Roles" },
                { href: "#scripts",  label: "Scripts" },
                { href: "#builds",   label: "Builds" },
                { href: "#upgrades", label: "Upgrades" },
                { href: "#deps",     label: "Dep matrix" },
              ].map((l) => (
                <a
                  key={l.href}
                  href={l.href}
                  className="rounded border border-border px-2 py-0.5 text-muted-foreground hover:text-foreground hover:border-foreground/30 transition-colors"
                >
                  {l.label}
                </a>
              ))}
            </nav>

            <SectionErrorBoundary section="Status">
              <StatusSection />
            </SectionErrorBoundary>
            <SectionErrorBoundary section="Configs">
              <ConfigsSection />
            </SectionErrorBoundary>
            <SectionErrorBoundary section="Roles">
              <RolesSection />
            </SectionErrorBoundary>
            <SectionErrorBoundary section="Scripts">
              <ScriptsSection />
            </SectionErrorBoundary>
            <SectionErrorBoundary section="Builds">
              <BuildsSection />
            </SectionErrorBoundary>
            <SectionErrorBoundary section="Upgrades">
              <UpgradesSection />
            </SectionErrorBoundary>
            <SectionErrorBoundary section="Dep matrix">
              <DepMatrixSection />
            </SectionErrorBoundary>
          </div>
        </TabsContent>

        {/* Jobs tab */}
        <TabsContent value="jobs">
          <SectionErrorBoundary section="Jobs">
            <JobsTab />
          </SectionErrorBoundary>
        </TabsContent>

        {/* Partitions tab */}
        <TabsContent value="partitions">
          <SectionErrorBoundary section="Partitions">
            <PartitionsTab />
          </SectionErrorBoundary>
        </TabsContent>
      </Tabs>
    </div>
  )
}
