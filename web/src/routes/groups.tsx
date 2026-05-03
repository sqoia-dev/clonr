// GRP-2..5: Node groups tab — rendered inside the /nodes route as a tab panel.
// Groups as a tab on /nodes, not a top-level surface. IA stays at 4 surfaces.
import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { Plus, Users, Pencil, Trash2, AlertTriangle, Play } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import type {
  NodeGroupWithCount,
  ListNodeGroupsResponse,
  GroupMembersResponse,
  ListImagesResponse,
  GroupReimageJobStatus,
  GroupReimageEvent,
} from "@/lib/types"
import { cn } from "@/lib/utils"
import { useEventSubscription } from "@/contexts/connection"

// ─── GroupsPanel ──────────────────────────────────────────────────────────────

interface GroupsPanelProps {
  createOpen?: boolean
  onCreateClose?: () => void
}

export function GroupsPanel({ createOpen, onCreateClose }: GroupsPanelProps) {
  const qc = useQueryClient()
  const [selectedGroup, setSelectedGroup] = React.useState<NodeGroupWithCount | null>(null)
  const [createSheetOpen, setCreateSheetOpen] = React.useState(createOpen ?? false)

  // Sync external open state.
  React.useEffect(() => {
    if (createOpen) setCreateSheetOpen(true)
  }, [createOpen])

  const { data, isLoading } = useQuery<ListNodeGroupsResponse>({
    queryKey: ["node-groups"],
    queryFn: () => apiFetch<ListNodeGroupsResponse>("/api/v1/node-groups"),
    refetchInterval: 15000,
    staleTime: 10000,
  })

  const groups = data?.groups ?? []

  function handleCreateClose() {
    setCreateSheetOpen(false)
    onCreateClose?.()
  }

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center justify-between px-6 py-3 border-b border-border">
        <span className="text-sm text-muted-foreground">{groups.length} group{groups.length !== 1 ? "s" : ""}</span>
        <Button size="sm" onClick={() => setCreateSheetOpen(true)}>
          <Plus className="h-4 w-4 mr-1" />
          New Group
        </Button>
      </div>

      <div className="flex-1 overflow-auto">
        {isLoading ? (
          <div className="p-4 space-y-2">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-14 w-full rounded" />
            ))}
          </div>
        ) : groups.length === 0 ? (
          <GroupsEmptyState onCreate={() => setCreateSheetOpen(true)} />
        ) : (
          <div className="divide-y divide-border">
            {groups.map((g) => (
              <button
                key={g.id}
                className="w-full flex items-center gap-4 px-6 py-4 text-left hover:bg-secondary/40 transition-colors"
                onClick={() => setSelectedGroup(g)}
              >
                <Users className="h-5 w-5 text-muted-foreground shrink-0" />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-sm">{g.name}</span>
                    {g.role && (
                      <span className="text-xs bg-secondary px-1.5 py-0.5 rounded font-mono">{g.role}</span>
                    )}
                  </div>
                  {g.description && (
                    <p className="text-xs text-muted-foreground truncate">{g.description}</p>
                  )}
                </div>
                <span className="text-xs text-muted-foreground shrink-0">{g.member_count} node{g.member_count !== 1 ? "s" : ""}</span>
              </button>
            ))}
          </div>
        )}
      </div>

      {/* Group detail sheet */}
      {selectedGroup && (
        <GroupDetailSheet
          group={selectedGroup}
          onClose={() => setSelectedGroup(null)}
          onEdit={() => { /* editing handled inside sheet */ }}
          onDeleted={() => {
            setSelectedGroup(null)
            qc.invalidateQueries({ queryKey: ["node-groups"] })
          }}
          onUpdated={(g) => {
            setSelectedGroup(g as NodeGroupWithCount)
            qc.invalidateQueries({ queryKey: ["node-groups"] })
          }}
        />
      )}

      {/* Create group sheet */}
      <CreateGroupSheet
        open={createSheetOpen}
        onClose={handleCreateClose}
        onCreated={(g) => {
          qc.invalidateQueries({ queryKey: ["node-groups"] })
          setCreateSheetOpen(false)
          onCreateClose?.()
          setSelectedGroup(g)
        }}
      />
    </div>
  )
}

// ─── GroupsEmptyState ─────────────────────────────────────────────────────────

function GroupsEmptyState({ onCreate }: { onCreate: () => void }) {
  return (
    <div className="flex flex-col items-center justify-center h-full min-h-64 gap-4 p-8 text-center">
      <Users className="h-10 w-10 text-muted-foreground" />
      <div className="space-y-1">
        <h2 className="text-base font-semibold">No groups yet</h2>
        <p className="text-sm text-muted-foreground">
          Groups let you manage sets of nodes together — bulk reimage, shared config.
        </p>
      </div>
      <Button size="sm" onClick={onCreate}>
        <Plus className="h-4 w-4 mr-1" />
        New Group
      </Button>
    </div>
  )
}

// ─── CreateGroupSheet ─────────────────────────────────────────────────────────

interface CreateGroupSheetProps {
  open: boolean
  onClose: () => void
  onCreated: (g: NodeGroupWithCount) => void
}

function CreateGroupSheet({ open, onClose, onCreated }: CreateGroupSheetProps) {
  const [name, setName] = React.useState("")
  const [description, setDescription] = React.useState("")
  const [role, setRole] = React.useState("")
  const [error, setError] = React.useState("")

  function reset() { setName(""); setDescription(""); setRole(""); setError("") }
  function handleClose() { reset(); onClose() }

  const mutation = useMutation({
    mutationFn: () =>
      apiFetch<NodeGroupWithCount>("/api/v1/node-groups", {
        method: "POST",
        body: JSON.stringify({ name, description: description || undefined, role: role || undefined }),
      }),
    onSuccess: (g) => {
      toast({ title: "Group created", description: g.name })
      reset()
      onCreated({ ...g, member_count: 0 })
    },
    onError: (err) => setError(String(err)),
  })

  return (
    <Sheet open={open} onOpenChange={(v) => !v && handleClose()}>
      <SheetContent side="right" className="w-full sm:max-w-md overflow-y-auto">
        <SheetHeader>
          <SheetTitle>New Group</SheetTitle>
          <SheetDescription>Create a named set of nodes for batch operations.</SheetDescription>
        </SheetHeader>
        <form className="mt-6 space-y-4" onSubmit={(e) => { e.preventDefault(); if (!name.trim()) { setError("Name is required"); return } mutation.mutate() }}>
          <div className="space-y-1">
            <label className="text-sm text-muted-foreground">Name *</label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="gpu-compute" />
          </div>
          <div className="space-y-1">
            <label className="text-sm text-muted-foreground">Description (optional)</label>
            <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="GPU compute partition" />
          </div>
          <div className="space-y-1">
            <label className="text-sm text-muted-foreground">Role (optional)</label>
            <select
              className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
              value={role}
              onChange={(e) => setRole(e.target.value)}
            >
              <option value="">None</option>
              <option value="compute">compute</option>
              <option value="login">login</option>
              <option value="storage">storage</option>
              <option value="gpu">gpu</option>
              <option value="admin">admin</option>
            </select>
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
          <div className="flex gap-2 pt-2">
            <Button type="submit" className="flex-1" disabled={mutation.isPending}>
              {mutation.isPending ? "Creating…" : "Create Group"}
            </Button>
            <Button type="button" variant="ghost" onClick={handleClose}>Cancel</Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  )
}

// ─── GroupDetailSheet ─────────────────────────────────────────────────────────

interface GroupDetailSheetProps {
  group: NodeGroupWithCount
  onClose: () => void
  onEdit: () => void
  onDeleted: () => void
  onUpdated: (g: NodeGroupWithCount) => void
}

function GroupDetailSheet({ group, onClose, onDeleted, onUpdated }: GroupDetailSheetProps) {
  const qc = useQueryClient()
  const [editing, setEditing] = React.useState(false)
  const [editName, setEditName] = React.useState(group.name)
  const [editDesc, setEditDesc] = React.useState(group.description ?? "")
  const [editRole, setEditRole] = React.useState(group.role ?? "")
  const [editError, setEditError] = React.useState("")
  const [deleteExpanded, setDeleteExpanded] = React.useState(false)
  const [deleteConfirm, setDeleteConfirm] = React.useState("")
  const [reimageExpanded, setReimageExpanded] = React.useState(false)

  // Fetch members.
  const { data: membersData, refetch: refetchMembers } = useQuery<GroupMembersResponse>({
    queryKey: ["node-group-members", group.id],
    queryFn: () => apiFetch<GroupMembersResponse>(`/api/v1/node-groups/${group.id}`),
    staleTime: 10000,
  })
  const members = membersData?.members ?? []

  // Fetch all nodes for member picker.
  const { data: nodesData } = useQuery<{ nodes: Array<{ id: string; hostname: string }> }>({
    queryKey: ["nodes"],
    staleTime: 30000,
  })
  const allNodes = nodesData?.nodes ?? []
  const memberIds = new Set(members.map((m) => m.id))

  const updateMutation = useMutation({
    mutationFn: () =>
      apiFetch<NodeGroupWithCount>(`/api/v1/node-groups/${group.id}`, {
        method: "PUT",
        body: JSON.stringify({ name: editName, description: editDesc, role: editRole }),
      }),
    onSuccess: (g) => {
      toast({ title: "Group updated" })
      setEditing(false)
      setEditError("")
      onUpdated({ ...g, member_count: members.length })
    },
    onError: (err) => setEditError(String(err)),
  })

  const deleteMutation = useMutation({
    mutationFn: () => apiFetch<void>(`/api/v1/node-groups/${group.id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast({ title: `Group deleted: ${group.name}` })
      qc.invalidateQueries({ queryKey: ["node-groups"] })
      onDeleted()
    },
    onError: (err) => toast({ variant: "destructive", title: "Delete failed", description: String(err) }),
  })

  const addMemberMutation = useMutation({
    mutationFn: (nodeId: string) =>
      apiFetch<GroupMembersResponse>(`/api/v1/node-groups/${group.id}/members`, {
        method: "POST",
        body: JSON.stringify({ node_ids: [nodeId] }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["node-groups"] })
      refetchMembers()
    },
    onError: (err) => toast({ variant: "destructive", title: "Failed to add member", description: String(err) }),
  })

  const removeMemberMutation = useMutation({
    mutationFn: (nodeId: string) =>
      apiFetch<void>(`/api/v1/node-groups/${group.id}/members/${nodeId}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["node-groups"] })
      refetchMembers()
    },
    onError: (err) => toast({ variant: "destructive", title: "Failed to remove member", description: String(err) }),
  })

  return (
    <Sheet open onOpenChange={(v) => !v && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-xl overflow-y-auto">
        <SheetHeader>
          <div className="flex items-center justify-between">
            <SheetTitle>{group.name}</SheetTitle>
            {!editing && (
              <Button variant="ghost" size="sm" onClick={() => setEditing(true)} className="h-7 px-2">
                <Pencil className="h-3.5 w-3.5 mr-1" />
                Edit
              </Button>
            )}
          </div>
          <SheetDescription>
            {group.role && <span className="font-mono text-xs bg-secondary px-1.5 py-0.5 rounded mr-2">{group.role}</span>}
            {members.length} node{members.length !== 1 ? "s" : ""}
          </SheetDescription>
        </SheetHeader>

        <div className="mt-6 space-y-5">
          {editing ? (
            <div className="rounded-md border border-border p-4 space-y-3">
              <h3 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Editing group</h3>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Name</label>
                <Input value={editName} onChange={(e) => setEditName(e.target.value)} className="text-sm" />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Description</label>
                <Input value={editDesc} onChange={(e) => setEditDesc(e.target.value)} className="text-sm" />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Role</label>
                <select
                  className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
                  value={editRole}
                  onChange={(e) => setEditRole(e.target.value)}
                >
                  <option value="">None</option>
                  <option value="compute">compute</option>
                  <option value="login">login</option>
                  <option value="storage">storage</option>
                  <option value="gpu">gpu</option>
                  <option value="admin">admin</option>
                </select>
              </div>
              {editError && <p className="text-xs text-destructive">{editError}</p>}
              <div className="flex gap-2">
                <Button size="sm" className="flex-1" onClick={() => updateMutation.mutate()} disabled={updateMutation.isPending}>
                  {updateMutation.isPending ? "Saving…" : "Save"}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => { setEditing(false); setEditError(""); setEditName(group.name); setEditDesc(group.description ?? ""); setEditRole(group.role ?? "") }}>
                  Cancel
                </Button>
              </div>
            </div>
          ) : (
            <>
              <GroupSection title="Details">
                <GroupRow label="ID" value={group.id} mono />
                {group.description && <GroupRow label="Description" value={group.description} />}
                {group.role && <GroupRow label="Role" value={group.role} mono />}
                <GroupRow label="Created" value={new Date(group.created_at).toLocaleDateString()} />
              </GroupSection>
            </>
          )}

          {/* Members section */}
          <GroupSection title={`Members (${members.length})`}>
            {members.length === 0 ? (
              <p className="text-xs text-muted-foreground">No members yet. Add nodes below.</p>
            ) : (
              <div className="space-y-1">
                {members.map((m) => (
                  <div key={m.id} className="flex items-center justify-between gap-2 py-1">
                    <span className="font-mono text-xs">{m.hostname || m.id}</span>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-6 px-2 text-xs text-muted-foreground hover:text-destructive"
                      onClick={() => removeMemberMutation.mutate(m.id)}
                      disabled={removeMemberMutation.isPending}
                      aria-label={`Remove ${m.hostname} from group`}
                    >
                      Remove
                    </Button>
                  </div>
                ))}
              </div>
            )}
            {/* Add member picker */}
            {allNodes.filter((n) => !memberIds.has(n.id)).length > 0 && (
              <div className="mt-2">
                <select
                  className="w-full text-xs border border-border bg-background rounded-md px-2 py-1.5"
                  defaultValue=""
                  onChange={(e) => {
                    if (e.target.value) {
                      addMemberMutation.mutate(e.target.value)
                      e.target.value = ""
                    }
                  }}
                >
                  <option value="">+ Add node…</option>
                  {allNodes.filter((n) => !memberIds.has(n.id)).map((n) => (
                    <option key={n.id} value={n.id}>{n.hostname || n.id}</option>
                  ))}
                </select>
              </div>
            )}
          </GroupSection>

          {/* Reimage group */}
          <GroupReimageFlow
            group={group}
            memberCount={members.length}
            expanded={reimageExpanded}
            onToggle={() => setReimageExpanded((e) => !e)}
          />

          {/* Delete group */}
          <div className="pt-4 border-t border-border space-y-3">
            {!deleteExpanded ? (
              <Button
                variant="ghost"
                className="w-full text-destructive hover:text-destructive hover:bg-destructive/10"
                onClick={() => setDeleteExpanded(true)}
              >
                <Trash2 className="h-4 w-4 mr-2" />
                Delete group
              </Button>
            ) : (
              <div className="rounded-md border border-destructive/30 bg-destructive/5 p-4 space-y-3">
                <div className="flex items-center gap-2 text-sm font-medium text-destructive">
                  <Trash2 className="h-4 w-4 shrink-0" />
                  Delete group — nodes are not affected
                </div>
                <p className="text-xs text-muted-foreground">
                  Type <code className="font-mono font-semibold text-foreground">{group.name}</code> to confirm:
                </p>
                <Input
                  className="font-mono text-xs"
                  placeholder={group.name}
                  value={deleteConfirm}
                  onChange={(e) => setDeleteConfirm(e.target.value)}
                />
                <div className="flex gap-2">
                  <Button
                    variant="destructive"
                    size="sm"
                    className="flex-1"
                    disabled={deleteConfirm !== group.name || deleteMutation.isPending}
                    onClick={() => deleteMutation.mutate()}
                  >
                    {deleteMutation.isPending ? "Deleting…" : "Delete permanently"}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => { setDeleteExpanded(false); setDeleteConfirm("") }}>
                    Cancel
                  </Button>
                </div>
              </div>
            )}
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}

// ─── GroupReimageFlow — REIMG-BULK-2..4 (SSE-driven per REIMG-BULK-1) ─────────

interface GroupReimageFlowProps {
  group: NodeGroupWithCount
  memberCount: number
  expanded: boolean
  onToggle: () => void
}

// NodeReimageRow tracks per-node progress state derived from SSE events.
interface NodeReimageRow {
  nodeId: string
  position: number
  status: "queued" | "started" | "imaging" | "verifying" | "done" | "failed"
  progress?: number
  durationMs?: number
  error?: string
}

function applyGroupReimageEvent(
  rows: NodeReimageRow[],
  event: GroupReimageEvent,
): NodeReimageRow[] {
  switch (event.kind) {
    case "reimage.queued":
      // Add the node row if not already present.
      if (rows.some((r) => r.nodeId === event.node_id)) return rows
      return [
        ...rows,
        { nodeId: event.node_id!, position: event.position ?? rows.length + 1, status: "queued" },
      ]
    case "reimage.started":
      return rows.map((r) =>
        r.nodeId === event.node_id ? { ...r, status: "started" as const } : r
      )
    case "reimage.imaging":
      return rows.map((r) =>
        r.nodeId === event.node_id
          ? { ...r, status: "imaging" as const, progress: event.progress }
          : r
      )
    case "reimage.verifying":
      return rows.map((r) =>
        r.nodeId === event.node_id ? { ...r, status: "verifying" as const } : r
      )
    case "reimage.done":
      return rows.map((r) =>
        r.nodeId === event.node_id
          ? { ...r, status: "done" as const, durationMs: event.duration_ms }
          : r
      )
    case "reimage.failed":
      return rows.map((r) =>
        r.nodeId === event.node_id
          ? { ...r, status: "failed" as const, error: event.error }
          : r
      )
    default:
      return rows
  }
}

function GroupReimageFlow({ group, memberCount, expanded, onToggle }: GroupReimageFlowProps) {
  const [selectedImageId, setSelectedImageId] = React.useState("")
  const [parallelism, setParallelism] = React.useState(1)
  const [confirmName, setConfirmName] = React.useState("")
  const [jobStatus, setJobStatus] = React.useState<GroupReimageJobStatus | null>(null)
  const [jobError, setJobError] = React.useState("")
  // Per-node rows driven by SSE events.
  const [nodeRows, setNodeRows] = React.useState<NodeReimageRow[]>([])
  const [sseJobDone, setSseJobDone] = React.useState(false)

  // ?kind=base excludes initramfs build artifacts from the group-reimage picker.
  const { data: imagesData } = useQuery<ListImagesResponse>({
    queryKey: ["images", "base"],
    queryFn: () => apiFetch<ListImagesResponse>("/api/v1/images?kind=base"),
    staleTime: 30000,
    enabled: expanded,
  })
  const readyImages = imagesData?.images?.filter((img) => img.status === "ready") ?? []

  // UX-4: Subscribe to group reimage events via the multiplexed /api/v1/events stream.
  // Replaces the per-page useSSE("/api/v1/node-groups/.../reimage/events?job_id=...").
  // The bus carries all group events; we filter by job_id and enabled state here.
  useEventSubscription<GroupReimageEvent>("groups", (event) => {
    // Ignore events when no active job, already done, or wrong job_id.
    if (!jobStatus || sseJobDone) return
    if (event.job_id !== jobStatus.job_id) return
    if (event.kind === "reimage.completed") {
      // Terminal event — update the job status summary and stop the stream.
      setSseJobDone(true)
      setJobStatus((prev) => {
        if (!prev) return prev
        const succeeded = event.succeeded ?? 0
        const failed = event.failed ?? 0
        const total = event.total ?? prev.total_nodes
        const termStatus = failed > 0 ? "failed" : "completed"
        toast({
          title: termStatus === "completed"
            ? `Reimaged ${succeeded}/${total} nodes in ${group.name}`
            : `Group reimage finished with failures`,
          description: failed > 0 ? `${failed} node(s) failed` : undefined,
          variant: failed > 0 ? "destructive" : "default",
        })
        return {
          ...prev,
          status: termStatus,
          succeeded_nodes: succeeded,
          failed_nodes: failed,
          total_nodes: total,
        }
      })
    } else {
      // Per-node transition — patch the row list.
      setNodeRows((prev) => applyGroupReimageEvent(prev, event))
    }
  })

  const reimageMutation = useMutation({
    mutationFn: () =>
      apiFetch<GroupReimageJobStatus>(`/api/v1/node-groups/${group.id}/reimage`, {
        method: "POST",
        body: JSON.stringify({
          image_id: selectedImageId,
          concurrency: parallelism,
          pause_on_failure_pct: 20,
        }),
      }),
    onSuccess: (job) => {
      setJobStatus(job)
      setNodeRows([])
      setSseJobDone(false)
      setConfirmName("")
      setJobError("")
    },
    onError: (err) => setJobError(String(err)),
  })

  const canConfirm = confirmName === group.name && selectedImageId !== ""

  return (
    <div className="border-t border-border pt-4 space-y-3">
      {!expanded ? (
        <Button
          variant="outline"
          className={cn("w-full", memberCount === 0 && "opacity-50")}
          onClick={onToggle}
          disabled={memberCount === 0}
        >
          <Play className="h-4 w-4 mr-2" />
          Reimage group ({memberCount} node{memberCount !== 1 ? "s" : ""})
        </Button>
      ) : (
        <div className="rounded-md border border-status-warning/30 bg-status-warning/5 p-4 space-y-3">
          <div className="flex items-center gap-2 text-sm font-medium text-status-warning">
            <AlertTriangle className="h-4 w-4 shrink-0" />
            Reimage {memberCount} nodes — rolling, {parallelism} at a time
          </div>

          {/* Target image */}
          <div className="space-y-1">
            <label className="text-xs text-muted-foreground">Target image</label>
            <select
              className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
              value={selectedImageId}
              onChange={(e) => setSelectedImageId(e.target.value)}
            >
              <option value="">Select target image…</option>
              {readyImages.map((img) => (
                <option key={img.id} value={img.id}>{img.name} {img.version}</option>
              ))}
            </select>
          </div>

          {/* Parallelism */}
          <div className="space-y-1">
            <label className="text-xs text-muted-foreground">Parallelism: {parallelism} node{parallelism !== 1 ? "s" : ""} at a time</label>
            <input
              type="range"
              min={1}
              max={Math.max(memberCount, 1)}
              value={parallelism}
              onChange={(e) => setParallelism(Number(e.target.value))}
              className="w-full"
            />
          </div>

          {/* Typed group name confirm */}
          <div className="space-y-1">
            <p className="text-xs text-muted-foreground">Type <code className="font-mono">{group.name}</code> to confirm:</p>
            <Input
              className="font-mono text-xs"
              placeholder={group.name}
              value={confirmName}
              onChange={(e) => setConfirmName(e.target.value)}
            />
          </div>

          {jobError && <p className="text-xs text-destructive">{jobError}</p>}

          {/* Progress panel */}
          {jobStatus && (
            <GroupReimageProgress job={jobStatus} nodeRows={nodeRows} />
          )}

          {!jobStatus && (
            <div className="flex gap-2">
              <Button
                variant="destructive"
                size="sm"
                className="flex-1"
                disabled={!canConfirm || reimageMutation.isPending}
                onClick={() => reimageMutation.mutate()}
              >
                {reimageMutation.isPending ? "Triggering…" : "Start rolling reimage"}
              </Button>
              <Button variant="ghost" size="sm" onClick={onToggle}>Cancel</Button>
            </div>
          )}
          {jobStatus && (jobStatus.status === "completed" || jobStatus.status === "failed") && (
            <Button variant="outline" size="sm" className="w-full" onClick={() => { setJobStatus(null); onToggle() }}>
              Close
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

// ─── GroupReimageProgress ─────────────────────────────────────────────────────

const NODE_STATUS_LABEL: Record<NodeReimageRow["status"], string> = {
  queued: "Queued",
  started: "Starting…",
  imaging: "Imaging…",
  verifying: "Verifying…",
  done: "Done",
  failed: "Failed",
}

function GroupReimageProgress({
  job,
  nodeRows,
}: {
  job: GroupReimageJobStatus
  nodeRows: NodeReimageRow[]
}) {
  const pct = job.total_nodes > 0 ? Math.round(((job.succeeded_nodes + job.failed_nodes) / job.total_nodes) * 100) : 0
  const statusColor = job.status === "completed" ? "bg-status-healthy"
    : job.status === "failed" ? "bg-destructive"
    : "bg-status-warning"

  return (
    <div className="rounded border border-border bg-card p-3 space-y-2">
      {/* Summary row */}
      <div className="flex items-center justify-between text-xs">
        <span className="font-medium">
          {job.status === "running" ? "Reimaging…" : job.status === "completed" ? "Complete" : job.status === "paused" ? "Paused" : job.status === "failed" ? "Failed" : "Queued"}
        </span>
        <span className="text-muted-foreground">
          {job.succeeded_nodes}/{job.total_nodes} done
          {job.failed_nodes > 0 && <span className="text-destructive ml-1">({job.failed_nodes} failed)</span>}
        </span>
      </div>
      {/* Overall progress bar */}
      <div className="h-2 rounded-full bg-secondary overflow-hidden">
        <div
          className={`h-full transition-all duration-500 ${statusColor}`}
          style={{ width: `${pct}%` }}
        />
      </div>
      {/* Per-node rows (SSE-driven) */}
      {nodeRows.length > 0 && (
        <div className="mt-2 space-y-1 max-h-40 overflow-y-auto">
          {nodeRows
            .slice()
            .sort((a, b) => a.position - b.position)
            .map((row) => (
              <div key={row.nodeId} className="flex items-center justify-between text-xs gap-2">
                <span className="font-mono text-muted-foreground truncate max-w-[120px]" title={row.nodeId}>
                  {row.nodeId.slice(0, 8)}…
                </span>
                <span
                  className={cn(
                    "shrink-0",
                    row.status === "done" && "text-status-healthy",
                    row.status === "failed" && "text-destructive",
                    (row.status === "imaging" || row.status === "started" || row.status === "verifying") && "text-status-warning",
                  )}
                >
                  {NODE_STATUS_LABEL[row.status]}
                  {row.status === "imaging" && row.progress != null && ` ${row.progress}%`}
                  {row.status === "done" && row.durationMs != null && ` (${(row.durationMs / 1000).toFixed(1)}s)`}
                </span>
              </div>
            ))}
        </div>
      )}
    </div>
  )
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function GroupSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="space-y-2">
      <h3 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{title}</h3>
      <div className="space-y-1.5">{children}</div>
    </div>
  )
}

function GroupRow({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4 text-sm">
      <span className="text-muted-foreground shrink-0">{label}</span>
      <span className={cn("text-right break-all", mono && "font-mono text-xs")}>{value ?? "—"}</span>
    </div>
  )
}
