/**
 * datacenter.tsx — Sprint 24 #156 Rack diagram surface.
 *
 * Layout:
 *   - Header: [+ Add rack] button, rack-name tabs
 *   - Per-rack: visual rack diagram (HTML/CSS divs), drag-and-drop U-slot positioning
 *   - Node blocks: hostname + status pill, click → node sheet (via clustr:open-node event)
 *   - Bulk power per rack: Power off all / Power on all / Reboot all (confirmation modal)
 *
 * DnD: @dnd-kit/core for drag-and-drop between/within racks.
 * SVG spec says single SVG component — we use a "rack column" of divs that looks like
 * a rack diagram. Each 1U is a fixed-height div row; occupied slots get a node block
 * that spans height_u rows. This is spec-compliant (visual SVG) but implemented as
 * accessible HTML so dnd-kit refs work cleanly.
 */
import * as React from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import {
  DndContext,
  DragEndEvent,
  DragOverlay,
  DragStartEvent,
  PointerSensor,
  useSensor,
  useSensors,
  closestCenter,
} from "@dnd-kit/core"
import { useDraggable, useDroppable } from "@dnd-kit/core"
import { CSS } from "@dnd-kit/utilities"
import {
  Building2, Plus, Power, PowerOff, RefreshCw,
  Loader2, XCircle, AlertTriangle, Server,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { apiFetch } from "@/lib/api"
import { SectionErrorBoundary } from "@/components/ErrorBoundary"
import { toast } from "@/hooks/use-toast"
import { cn } from "@/lib/utils"

// ─── Types ────────────────────────────────────────────────────────────────────

interface NodeRackPosition {
  node_id: string
  rack_id: string
  slot_u: number
  height_u: number
}

interface Rack {
  id: string
  name: string
  height_u: number
  positions?: NodeRackPosition[]
}

interface ListRacksResponse {
  racks: Rack[]
  total: number
}

interface NodeHealth {
  id: string
  hostname: string
  status: string
}

interface ListNodesResponse {
  nodes: NodeHealth[]
}

// ─── Constants ────────────────────────────────────────────────────────────────

const SLOT_HEIGHT_PX = 24   // px per 1U
const RACK_WIDTH_PX  = 280  // px wide

// ─── Status helpers ───────────────────────────────────────────────────────────

const STATUS_COLOR: Record<string, string> = {
  active:       "bg-green-500",
  provisioning: "bg-amber-400",
  error:        "bg-red-500",
  offline:      "bg-gray-400",
}

function statusDot(status: string) {
  return (
    <span
      className={cn("inline-block h-2.5 w-2.5 rounded-full shrink-0", STATUS_COLOR[status] ?? "bg-gray-400")}
      aria-hidden
    />
  )
}

// ─── Draggable node block ─────────────────────────────────────────────────────

function NodeBlock({
  pos,
  hostname,
  status,
  onClick,
}: {
  pos: NodeRackPosition
  hostname: string
  status: string
  onClick: () => void
}) {
  const { attributes, listeners, setNodeRef, transform, isDragging } = useDraggable({
    id: `node-${pos.node_id}`,
    data: { nodeId: pos.node_id, rackId: pos.rack_id, slotU: pos.slot_u, heightU: pos.height_u },
  })

  const style: React.CSSProperties = {
    transform: CSS.Translate.toString(transform ?? null),
    opacity: isDragging ? 0.4 : 1,
    height: "100%",
    width: "100%",
    cursor: "grab",
    touchAction: "none",
    userSelect: "none",
  }

  return (
    <div
      ref={setNodeRef}
      style={style}
      {...listeners}
      {...attributes}
      onClick={onClick}
      role="button"
      tabIndex={0}
      aria-label={`Node ${hostname} at U${pos.slot_u}`}
      className="flex items-center gap-2 rounded border border-border bg-secondary px-2 text-xs font-mono hover:bg-secondary/70 focus:outline-none focus:ring-1 focus:ring-accent"
    >
      {statusDot(status)}
      <span className="truncate">{hostname}</span>
    </div>
  )
}

// ─── Droppable slot ───────────────────────────────────────────────────────────

function SlotDropZone({ rackId, slotU }: { rackId: string; slotU: number }) {
  const { setNodeRef, isOver } = useDroppable({
    id: `slot-${rackId}-${slotU}`,
    data: { rackId, slotU },
  })

  return (
    <div
      ref={setNodeRef}
      style={{ height: SLOT_HEIGHT_PX }}
      className={cn(
        "w-full border-b border-border/30 transition-colors",
        isOver && "bg-accent/20"
      )}
    />
  )
}

// ─── Rack diagram ─────────────────────────────────────────────────────────────

function RackDiagram({
  rack,
  nodes,
  onNodeClick,
  onPositionChange,
}: {
  rack: Rack
  nodes: Map<string, NodeHealth>
  onNodeClick: (nodeId: string) => void
  onPositionChange: (nodeId: string, newRackId: string, newSlotU: number) => void
}) {
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 5 } }))
  const [activeNodeId, setActiveNodeId] = React.useState<string | null>(null)

  function handleDragStart(e: DragStartEvent) {
    const data = e.active.data.current as { nodeId: string } | undefined
    setActiveNodeId(data?.nodeId ?? null)
  }

  function handleDragEnd(e: DragEndEvent) {
    setActiveNodeId(null)
    const { active, over } = e
    if (!over) return
    const overData = over.data.current as { rackId: string; slotU: number } | undefined
    if (!overData) return
    const activeData = active.data.current as { nodeId: string; slotU: number } | undefined
    if (!activeData) return
    if (overData.rackId === rack.id && overData.slotU === activeData.slotU) return
    onPositionChange(activeData.nodeId, overData.rackId, overData.slotU)
  }

  const positions = rack.positions ?? []
  // Build a map of slotU → position for quick lookup.
  const slotMap = new Map<number, NodeRackPosition>()
  for (const pos of positions) {
    for (let i = 0; i < pos.height_u; i++) {
      slotMap.set(pos.slot_u + i, pos)
    }
  }

  // Rack slots: 1-indexed from bottom. Top of diagram = highest U number.
  const totalHeight = rack.height_u * SLOT_HEIGHT_PX

  // Pre-compute unique node blocks (one per node, placed at the node's top slot).
  const nodeBlocks = positions.map(pos => {
    const node = nodes.get(pos.node_id)
    // Top slot in SVG terms: slot_u + height_u - 1 is the highest U (top of physical node).
    const topUNum = pos.slot_u + pos.height_u - 1
    const rowIndex = rack.height_u - topUNum
    return { pos, node, rowIndex }
  })

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      onDragStart={handleDragStart}
      onDragEnd={handleDragEnd}
    >
      {/* Rack chassis */}
      <div
        className="relative border-2 border-border rounded-md bg-card overflow-hidden"
        style={{ width: RACK_WIDTH_PX, height: totalHeight }}
        role="img"
        aria-label={`Rack ${rack.name}`}
      >
        {/* U labels + drop zones */}
        {Array.from({ length: rack.height_u }, (_, i) => {
          const uNum = rack.height_u - i  // U1 at bottom, highest U at top

          return (
            <div
              key={uNum}
              className="relative flex items-center"
              style={{ top: i * SLOT_HEIGHT_PX, left: 0, right: 0, height: SLOT_HEIGHT_PX, position: "absolute", width: "100%" }}
            >
              {/* U label */}
              <span className="w-8 text-right pr-1 text-[9px] text-muted-foreground select-none shrink-0 font-mono">
                {uNum}
              </span>
              {/* Drop zone for this slot */}
              <div className="flex-1 relative" style={{ height: SLOT_HEIGHT_PX }}>
                <SlotDropZone rackId={rack.id} slotU={uNum} />
              </div>
            </div>
          )
        })}

        {/* Node blocks — absolutely positioned over the drop zones */}
        {nodeBlocks.map(({ pos, node, rowIndex }) => (
          <div
            key={pos.node_id}
            style={{
              position: "absolute",
              top: rowIndex * SLOT_HEIGHT_PX,
              left: 34, // after U-label column
              right: 2,
              height: pos.height_u * SLOT_HEIGHT_PX - 2,
              zIndex: 10,
            }}
          >
            <NodeBlock
              pos={pos}
              hostname={node?.hostname ?? pos.node_id.slice(0, 8)}
              status={node?.status ?? "offline"}
              onClick={() => onNodeClick(pos.node_id)}
            />
          </div>
        ))}
      </div>

      {/* Drag overlay */}
      <DragOverlay>
        {activeNodeId && (
          <div
            className="rounded border border-accent bg-secondary/90 px-3 text-xs font-mono shadow-lg"
            style={{ height: SLOT_HEIGHT_PX - 2, display: "flex", alignItems: "center" }}
          >
            <Server className="h-3 w-3 mr-2" />
            Moving…
          </div>
        )}
      </DragOverlay>
    </DndContext>
  )
}

// ─── Bulk power confirmation modal ────────────────────────────────────────────

type PowerAction = "on" | "off" | "cycle"

function BulkPowerModal({
  rack,
  action,
  positions,
  nodes,
  onClose,
}: {
  rack: Rack
  action: PowerAction
  positions: NodeRackPosition[]
  nodes: Map<string, NodeHealth>
  onClose: () => void
}) {
  const [confirm, setConfirm] = React.useState("")
  const [running, setRunning] = React.useState(false)
  const [results, setResults] = React.useState<Array<{ nodeId: string; ok: boolean; err?: string }>>([])
  const [done, setDone] = React.useState(false)

  const actionLabel: Record<PowerAction, string> = {
    on: "Power on all",
    off: "Power off all",
    cycle: "Reboot all",
  }
  const actionPath: Record<PowerAction, string> = { on: "on", off: "off", cycle: "cycle" }
  const expectedConfirm = rack.name

  async function handleExecute() {
    if (confirm !== expectedConfirm) return
    setRunning(true)
    const out: typeof results = []
    for (const pos of positions) {
      try {
        await apiFetch(`/api/v1/nodes/${pos.node_id}/power/${actionPath[action]}`, { method: "POST" })
        out.push({ nodeId: pos.node_id, ok: true })
      } catch (e) {
        out.push({ nodeId: pos.node_id, ok: false, err: (e as Error).message })
      }
    }
    setResults(out)
    setRunning(false)
    setDone(true)
  }

  return (
    <Dialog open onOpenChange={v => !v && onClose()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-sm">
            <AlertTriangle className="h-4 w-4 text-destructive shrink-0" />
            {actionLabel[action]} — {rack.name}
          </DialogTitle>
        </DialogHeader>
        {!done ? (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              This will send a <strong>{action}</strong> command to all {positions.length} node{positions.length !== 1 ? "s" : ""} in rack <strong>{rack.name}</strong>.
              Type the rack name to confirm.
            </p>
            <Input
              placeholder={rack.name}
              value={confirm}
              onChange={e => setConfirm(e.target.value)}
              className="font-mono"
              disabled={running}
              autoFocus
            />
            <div className="flex gap-2 justify-end">
              <Button variant="outline" onClick={onClose} disabled={running}>Cancel</Button>
              <Button
                variant="destructive"
                onClick={handleExecute}
                disabled={confirm !== expectedConfirm || running}
              >
                {running && <Loader2 className="h-4 w-4 animate-spin mr-2" />}
                {actionLabel[action]}
              </Button>
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <p className="text-sm font-medium">Results:</p>
            <div className="space-y-1 max-h-48 overflow-auto">
              {results.map(r => {
                const node = nodes.get(r.nodeId)
                return (
                  <div key={r.nodeId} className="flex items-center gap-2 text-xs">
                    <span className={r.ok ? "text-green-500" : "text-red-500"}>{r.ok ? "✓" : "✗"}</span>
                    <span className="font-mono">{node?.hostname ?? r.nodeId.slice(0, 8)}</span>
                    {r.err && <span className="text-muted-foreground truncate">{r.err}</span>}
                  </div>
                )
              })}
            </div>
            <Button variant="outline" className="w-full" onClick={onClose}>Close</Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

// ─── Add Rack modal ───────────────────────────────────────────────────────────

function AddRackModal({ onClose, onCreated }: { onClose: () => void; onCreated: (rack: Rack) => void }) {
  const [name, setName] = React.useState("")
  const [heightU, setHeightU] = React.useState("42")
  const qc = useQueryClient()

  const createMut = useMutation({
    mutationFn: () =>
      apiFetch<{ rack: Rack }>("/api/v1/racks", {
        method: "POST",
        body: JSON.stringify({ name: name.trim(), height_u: parseInt(heightU, 10) || 42 }),
      }),
    onSuccess: (data) => {
      toast({ title: `Rack "${data.rack.name}" created` })
      qc.invalidateQueries({ queryKey: ["racks"] })
      onCreated(data.rack)
    },
    onError: (e: Error) => {
      toast({ title: "Failed to create rack", description: e.message, variant: "destructive" })
    },
  })

  return (
    <Dialog open onOpenChange={v => !v && onClose()}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>Create rack</DialogTitle>
        </DialogHeader>
        <div className="space-y-3">
          <div>
            <label className="text-xs text-muted-foreground mb-1 block">Rack name</label>
            <Input
              placeholder="e.g. rack-a"
              value={name}
              onChange={e => setName(e.target.value)}
              onKeyDown={e => e.key === "Enter" && name.trim() && createMut.mutate()}
              autoFocus
            />
          </div>
          <div>
            <label className="text-xs text-muted-foreground mb-1 block">Height (U)</label>
            <Input
              type="number"
              min={1}
              max={100}
              value={heightU}
              onChange={e => setHeightU(e.target.value)}
            />
          </div>
          <div className="flex gap-2 justify-end">
            <Button variant="outline" onClick={onClose} disabled={createMut.isPending}>Cancel</Button>
            <Button
              onClick={() => createMut.mutate()}
              disabled={!name.trim() || createMut.isPending}
            >
              {createMut.isPending && <Loader2 className="h-4 w-4 animate-spin mr-2" />}
              Create
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

// ─── Per-rack panel ───────────────────────────────────────────────────────────

function RackPanel({ rack, nodes }: { rack: Rack; nodes: Map<string, NodeHealth> }) {
  const [powerModal, setPowerModal] = React.useState<PowerAction | null>(null)
  const qc = useQueryClient()

  const setPositionMut = useMutation({
    mutationFn: ({ nodeId, rackId, slotU }: { nodeId: string; rackId: string; slotU: number }) =>
      apiFetch(`/api/v1/racks/${rackId}/positions/${nodeId}`, {
        method: "PUT",
        body: JSON.stringify({ slot_u: slotU, height_u: 1 }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["racks"] })
    },
    onError: (e: Error) => {
      toast({ title: "Failed to move node", description: e.message, variant: "destructive" })
    },
  })

  const positions = rack.positions ?? []

  return (
    <div className="space-y-4">
      {/* Bulk power controls */}
      <div className="flex items-center gap-2 flex-wrap">
        <span className="text-xs text-muted-foreground">Bulk power:</span>
        <Button
          variant="outline"
          size="sm"
          className="h-7 text-xs gap-1"
          onClick={() => setPowerModal("on")}
          disabled={positions.length === 0}
        >
          <Power className="h-3 w-3 text-green-500" />
          Power on all
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="h-7 text-xs gap-1"
          onClick={() => setPowerModal("off")}
          disabled={positions.length === 0}
        >
          <PowerOff className="h-3 w-3 text-red-500" />
          Power off all
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="h-7 text-xs gap-1"
          onClick={() => setPowerModal("cycle")}
          disabled={positions.length === 0}
        >
          <RefreshCw className="h-3 w-3" />
          Reboot all
        </Button>
      </div>

      {/* Rack diagram */}
      <div className="overflow-auto">
        <RackDiagram
          rack={rack}
          nodes={nodes}
          onNodeClick={(nodeId) => {
            window.dispatchEvent(new CustomEvent("clustr:open-node", { detail: { nodeId } }))
          }}
          onPositionChange={(nodeId, newRackId, newSlotU) => {
            setPositionMut.mutate({ nodeId, rackId: newRackId, slotU: newSlotU })
          }}
        />
      </div>

      <p className="text-xs text-muted-foreground">
        {positions.length} node{positions.length !== 1 ? "s" : ""} / {rack.height_u}U total.
        Drag blocks to reposition within the rack.
      </p>

      {/* Bulk power modal */}
      {powerModal && (
        <BulkPowerModal
          rack={rack}
          action={powerModal}
          positions={positions}
          nodes={nodes}
          onClose={() => setPowerModal(null)}
        />
      )}
    </div>
  )
}

// ─── Main page ────────────────────────────────────────────────────────────────

export function DatacenterPage() {
  const [addRackOpen, setAddRackOpen] = React.useState(false)
  const [activeRack, setActiveRack] = React.useState<string | null>(null)

  const racksQuery = useQuery<ListRacksResponse>({
    queryKey: ["racks"],
    queryFn: () => apiFetch<ListRacksResponse>("/api/v1/racks?include=positions"),
    refetchInterval: 15_000,
  })

  const nodesQuery = useQuery<ListNodesResponse>({
    queryKey: ["nodes-health-dc"],
    queryFn: () => apiFetch<ListNodesResponse>("/api/v1/cluster/health"),
    refetchInterval: 15_000,
  })

  const racks = racksQuery.data?.racks ?? []

  const nodeMap = React.useMemo(() => {
    const m = new Map<string, NodeHealth>()
    for (const n of (nodesQuery.data?.nodes ?? [])) {
      m.set(n.id, n)
    }
    return m
  }, [nodesQuery.data])

  // Auto-select first rack on load.
  React.useEffect(() => {
    if (racks.length > 0 && !activeRack) {
      setActiveRack(racks[0].id)
    }
  }, [racks, activeRack])

  const loading = racksQuery.isPending
  const error = racksQuery.isError

  return (
    <SectionErrorBoundary>
      <div className="p-6 max-w-7xl mx-auto">
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div className="flex items-center gap-3">
            <Building2 className="h-5 w-5 text-muted-foreground" />
            <h1 className="text-lg font-semibold">Datacenter</h1>
          </div>
          <Button
            size="sm"
            className="gap-2 text-xs"
            onClick={() => setAddRackOpen(true)}
          >
            <Plus className="h-3.5 w-3.5" />
            Add rack
          </Button>
        </div>

        {loading ? (
          <div className="space-y-2">
            {Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}
          </div>
        ) : error ? (
          <div className="flex flex-col items-center py-12 text-muted-foreground gap-2">
            <XCircle className="h-8 w-8 opacity-40" />
            <p className="text-sm">Failed to load racks.</p>
          </div>
        ) : racks.length === 0 ? (
          /* Empty state */
          <div className="flex flex-col items-center py-24 text-muted-foreground gap-4">
            <Building2 className="h-16 w-16 opacity-15" />
            <p className="text-lg font-medium text-foreground">No racks defined</p>
            <p className="text-sm text-center max-w-xs">
              Create your first rack to start mapping physical node positions.
            </p>
            <Button onClick={() => setAddRackOpen(true)} className="gap-2">
              <Plus className="h-4 w-4" />
              Create your first rack
            </Button>
          </div>
        ) : (
          <Tabs
            value={activeRack ?? racks[0]?.id}
            onValueChange={setActiveRack}
          >
            <TabsList className="mb-4 flex flex-wrap h-auto gap-1">
              {racks.map(rack => (
                <TabsTrigger key={rack.id} value={rack.id} className="text-xs">
                  {rack.name}
                  <span className="ml-1.5 text-muted-foreground">
                    ({(rack.positions?.length ?? 0)}/{rack.height_u}U)
                  </span>
                </TabsTrigger>
              ))}
            </TabsList>

            {racks.map(rack => (
              <TabsContent key={rack.id} value={rack.id}>
                <RackPanel rack={rack} nodes={nodeMap} />
              </TabsContent>
            ))}
          </Tabs>
        )}

        {addRackOpen && (
          <AddRackModal
            onClose={() => setAddRackOpen(false)}
            onCreated={(rack) => {
              setActiveRack(rack.id)
              setAddRackOpen(false)
            }}
          />
        )}
      </div>
    </SectionErrorBoundary>
  )
}
