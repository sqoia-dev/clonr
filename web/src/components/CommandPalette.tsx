import * as React from "react"
import { useNavigate } from "@tanstack/react-router"
import { useQuery } from "@tanstack/react-query"
import { Dialog, DialogContent } from "@/components/ui/dialog"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command"
import { Server, Image as ImageIcon, Activity, Settings, ShieldCheck, Cpu, Building2, Bell, RefreshCw, Key, Clock, ChevronLeft, Plus, Pencil, Trash2, Layers } from "lucide-react"
import { apiFetch } from "@/lib/api"
import type { ListNodesResponse, ListImagesResponse } from "@/lib/types"

// ─── Types ────────────────────────────────────────────────────────────────────

export interface RecentEntity {
  kind: "node" | "image"
  id: string
  label: string
  visitedAt: number
}

const RECENT_STORAGE_KEY = "clustr.recentEntities"
const MAX_RECENT = 5

// ─── Recent entities helpers (PAL-5) ─────────────────────────────────────────

export function recordRecentEntity(entity: Omit<RecentEntity, "visitedAt">): void {
  try {
    const raw = localStorage.getItem(RECENT_STORAGE_KEY)
    const list: RecentEntity[] = raw ? JSON.parse(raw) : []
    const filtered = list.filter((e) => e.id !== entity.id)
    filtered.unshift({ ...entity, visitedAt: Date.now() })
    localStorage.setItem(RECENT_STORAGE_KEY, JSON.stringify(filtered.slice(0, MAX_RECENT)))
  } catch {
    // Ignore storage errors.
  }
}

function loadRecentEntities(): RecentEntity[] {
  try {
    const raw = localStorage.getItem(RECENT_STORAGE_KEY)
    return raw ? JSON.parse(raw) : []
  } catch {
    return []
  }
}

// ─── Routes ───────────────────────────────────────────────────────────────────

const navRoutes = [
  { label: "Nodes", path: "/nodes", icon: Server },
  { label: "Images", path: "/images", icon: ImageIcon },
  { label: "Slurm", path: "/slurm", icon: Cpu },
  { label: "Alerts", path: "/alerts", icon: Bell },
  { label: "Datacenter", path: "/datacenter", icon: Building2 },
  { label: "Activity", path: "/activity", icon: Activity },
  { label: "Identity", path: "/identity", icon: ShieldCheck },
  { label: "Settings", path: "/settings", icon: Settings },
]

// ─── CommandPalette ───────────────────────────────────────────────────────────

interface Props {
  open: boolean
  onClose: () => void
}

type PaletteMode = "root" | "node-picker" | "edit-node-picker" | "delete-node-picker" | "add-image" | "delete-image-picker"

export function CommandPalette({ open, onClose }: Props) {
  const navigate = useNavigate()
  const [recent, setRecent] = React.useState<RecentEntity[]>([])
  const [mode, setMode] = React.useState<PaletteMode>("root")

  // Load recent entities when palette opens; reset mode on close.
  React.useEffect(() => {
    if (open) {
      setRecent(loadRecentEntities())
      setMode("root")
    }
  }, [open])

  // Fetch nodes when picker opens (PAL-2-2 / EDIT-NODE-4 / NODE-DEL-4).
  const { data: nodesData } = useQuery<ListNodesResponse>({
    queryKey: ["nodes-palette"],
    queryFn: () => apiFetch<ListNodesResponse>("/api/v1/nodes"),
    enabled: mode === "node-picker" || mode === "edit-node-picker" || mode === "delete-node-picker",
    staleTime: 10000,
  })
  const nodes = nodesData?.nodes ?? []

  // Fetch images when delete-image-picker opens (IMG-DEL-3).
  const { data: imagesData } = useQuery<ListImagesResponse>({
    queryKey: ["images-palette"],
    queryFn: () => apiFetch<ListImagesResponse>("/api/v1/images"),
    enabled: mode === "delete-image-picker",
    staleTime: 10000,
  })
  const images = imagesData?.images ?? []

  function goTo(path: string) {
    onClose()
    if (path === "/nodes") navigate({ to: "/nodes", search: { q: undefined, status: undefined, sort: undefined, dir: undefined, openNode: undefined, reimage: undefined, addNode: undefined, deleteNode: undefined, tag: undefined, view: undefined, createGroup: undefined, page: undefined, per_page: undefined } })
    else if (path === "/images") navigate({ to: "/images", search: { q: undefined, tab: undefined, sort: undefined, dir: undefined, addImage: undefined } })
    else if (path === "/activity") navigate({ to: "/activity", search: { q: undefined, kind: undefined, page: undefined, per_page: undefined } })
    else if (path === "/identity") navigate({ to: "/identity", search: { users_page: undefined, users_per_page: undefined, groups_page: undefined, groups_per_page: undefined } })
    else if (path === "/slurm") navigate({ to: "/slurm", search: { deps_page: undefined, deps_per_page: undefined } })
    else if (path === "/alerts") navigate({ to: "/alerts" })
    else if (path === "/datacenter") navigate({ to: "/datacenter" })
    else navigate({ to: "/settings" })
  }

  // PAL-2-2: inline node picker → navigate to /nodes?openNode=<id>&reimage=1
  function pickNodeForReimage(nodeId: string, nodeLabel: string) {
    onClose()
    recordRecentEntity({ kind: "node", id: nodeId, label: nodeLabel })
    navigate({
      to: "/nodes",
      search: {
        q: undefined,
        status: undefined,
        sort: undefined,
        dir: undefined,
        openNode: nodeId,
        reimage: "1",
        addNode: undefined,
        deleteNode: undefined,
        tag: undefined,
        view: undefined,
        createGroup: undefined,
        page: undefined,
        per_page: undefined,
      },
    })
  }

  function addNode() {
    onClose()
    navigate({
      to: "/nodes",
      search: { q: undefined, status: undefined, sort: undefined, dir: undefined, openNode: undefined, reimage: undefined, addNode: "1", deleteNode: undefined, tag: undefined, view: undefined, createGroup: undefined, page: undefined, per_page: undefined },
    })
  }

  function createAPIKey() {
    onClose()
    navigate({ to: "/settings" })
  }

  function visitRecent(entity: RecentEntity) {
    onClose()
    if (entity.kind === "node") {
      navigate({ to: "/nodes", search: { q: undefined, status: undefined, sort: undefined, dir: undefined, openNode: entity.id, reimage: undefined, addNode: undefined, deleteNode: undefined, tag: undefined, view: undefined, createGroup: undefined, page: undefined, per_page: undefined } })
    } else {
      navigate({ to: "/images", search: { q: undefined, tab: undefined, sort: undefined, dir: undefined, addImage: undefined } })
    }
  }

  // EDIT-NODE-4: navigate to /nodes?openNode=<id> (opens detail Sheet in view mode; user clicks Edit button).
  function pickNodeForEdit(nodeId: string, nodeLabel: string) {
    onClose()
    recordRecentEntity({ kind: "node", id: nodeId, label: nodeLabel })
    navigate({
      to: "/nodes",
      search: { q: undefined, status: undefined, sort: undefined, dir: undefined, openNode: nodeId, reimage: undefined, addNode: undefined, deleteNode: undefined, tag: undefined, view: undefined, createGroup: undefined, page: undefined, per_page: undefined },
    })
  }

  // NODE-DEL-4: navigate to /nodes?openNode=<id>&deleteNode=1 to open detail sheet with delete pre-expanded.
  function pickNodeForDelete(nodeId: string, nodeLabel: string) {
    onClose()
    recordRecentEntity({ kind: "node", id: nodeId, label: nodeLabel })
    navigate({
      to: "/nodes",
      search: {
        q: undefined,
        status: undefined,
        sort: undefined,
        dir: undefined,
        openNode: nodeId,
        reimage: undefined,
        addNode: undefined,
        deleteNode: "1",
        tag: undefined,
        view: undefined,
        createGroup: undefined,
        page: undefined,
        per_page: undefined,
      },
    })
  }

  // IMG-URL-6: navigate to /images?addImage=1 to auto-open the Add Image sheet.
  function addImageFromURL() {
    onClose()
    navigate({ to: "/images", search: { q: undefined, tab: undefined, sort: undefined, dir: undefined, addImage: "1" } })
  }

  // INITRD-7: navigate to /images?tab=initramfs to surface the Build Initramfs button.
  function buildInitramfs() {
    onClose()
    navigate({ to: "/images", search: { q: undefined, tab: "initramfs", sort: undefined, dir: undefined, addImage: undefined } })
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="p-0 gap-0 max-w-md">
        <Command className="rounded-lg">
          {mode === "root" && (
            <>
              <CommandInput placeholder="Search commands and routes..." />
              <CommandList>
                <CommandEmpty>No results.</CommandEmpty>

                {/* Navigation (PAL-1) */}
                <CommandGroup heading="Navigation">
                  {navRoutes.map((r) => (
                    <CommandItem key={r.path} value={r.label} onSelect={() => goTo(r.path)}>
                      <r.icon className="mr-2 h-4 w-4" />
                      {r.label}
                    </CommandItem>
                  ))}
                </CommandGroup>

                <CommandSeparator />

                {/* Actions (PAL-1..4, EDIT-NODE-4, IMG-URL-6, IMG-DEL-3) */}
                <CommandGroup heading="Actions">
                  {/* NODE-CREATE-5: Cmd-K add node */}
                  <CommandItem value="add node" onSelect={addNode}>
                    <Plus className="mr-2 h-4 w-4" />
                    Add node…
                  </CommandItem>
                  {/* EDIT-NODE-4: Cmd-K edit node */}
                  <CommandItem value="edit node" onSelect={() => setMode("edit-node-picker")}>
                    <Pencil className="mr-2 h-4 w-4" />
                    Edit node…
                    <span className="ml-auto text-xs text-muted-foreground">Select node</span>
                  </CommandItem>
                  {/* NODE-DEL-4: Cmd-K delete node */}
                  <CommandItem value="delete node" onSelect={() => setMode("delete-node-picker")}>
                    <Trash2 className="mr-2 h-4 w-4" />
                    Delete node…
                    <span className="ml-auto text-xs text-muted-foreground">Select node</span>
                  </CommandItem>
                  {/* PAL-2-2: inline node picker, no redirect */}
                  <CommandItem value="reimage node" onSelect={() => setMode("node-picker")}>
                    <RefreshCw className="mr-2 h-4 w-4" />
                    Reimage node…
                    <span className="ml-auto text-xs text-muted-foreground">Select node</span>
                  </CommandItem>
                  {/* IMG-URL-6: Cmd-K add image from URL */}
                  <CommandItem value="add image from url upload" onSelect={addImageFromURL}>
                    <ImageIcon className="mr-2 h-4 w-4" />
                    Add image from URL…
                  </CommandItem>
                  {/* IMG-DEL-3: Cmd-K delete image */}
                  <CommandItem value="delete image" onSelect={() => setMode("delete-image-picker")}>
                    <Trash2 className="mr-2 h-4 w-4" />
                    Delete image…
                    <span className="ml-auto text-xs text-muted-foreground">Select image</span>
                  </CommandItem>
                  {/* INITRD-7: Cmd-K build initramfs */}
                  <CommandItem value="build initramfs" onSelect={buildInitramfs}>
                    <Layers className="mr-2 h-4 w-4" />
                    Build initramfs…
                    <span className="ml-auto text-xs text-muted-foreground">Images → Initramfs</span>
                  </CommandItem>
                  <CommandItem value="create api key" onSelect={createAPIKey}>
                    <Key className="mr-2 h-4 w-4" />
                    Create API key…
                    <span className="ml-auto text-xs text-muted-foreground">Settings → API Keys</span>
                  </CommandItem>
                  <CommandItem value="slurm status configs roles scripts builds upgrades" onSelect={() => goTo("/slurm")}>
                    <Cpu className="mr-2 h-4 w-4" />
                    Slurm management…
                    <span className="ml-auto text-xs text-muted-foreground">Slurm</span>
                  </CommandItem>
                </CommandGroup>

                {/* Recent entities (PAL-5) */}
                {recent.length > 0 && (
                  <>
                    <CommandSeparator />
                    <CommandGroup heading="Recent">
                      {recent.map((entity) => (
                        <CommandItem
                          key={entity.id}
                          value={`${entity.kind} ${entity.label} ${entity.id}`}
                          onSelect={() => visitRecent(entity)}
                        >
                          <Clock className="mr-2 h-4 w-4 text-muted-foreground" />
                          <span>{entity.label}</span>
                          <span className="ml-2 text-xs text-muted-foreground font-mono">{entity.id.slice(0, 8)}</span>
                          <span className="ml-auto text-xs text-muted-foreground capitalize">{entity.kind}</span>
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  </>
                )}
              </CommandList>
            </>
          )}

          {mode === "node-picker" && (
            <>
              <CommandInput placeholder="Search nodes to reimage..." autoFocus />
              <CommandList>
                <CommandEmpty>
                  {nodes.length === 0 ? "Loading nodes…" : "No matching nodes."}
                </CommandEmpty>

                <CommandGroup>
                  {/* Back button */}
                  <CommandItem value="__back__" onSelect={() => setMode("root")} className="text-muted-foreground">
                    <ChevronLeft className="mr-2 h-4 w-4" />
                    Back
                  </CommandItem>
                </CommandGroup>

                {nodes.length > 0 && (
                  <CommandGroup heading="Select node to reimage">
                    {nodes.map((node) => (
                      <CommandItem
                        key={node.id}
                        value={`${node.hostname} ${node.id} ${node.primary_mac}`}
                        onSelect={() => pickNodeForReimage(node.id, node.hostname || node.id)}
                      >
                        <Server className="mr-2 h-4 w-4" />
                        <span className="font-medium">{node.hostname || node.id}</span>
                        <span className="ml-2 text-xs text-muted-foreground font-mono">{node.primary_mac}</span>
                      </CommandItem>
                    ))}
                  </CommandGroup>
                )}
              </CommandList>
            </>
          )}

          {/* EDIT-NODE-4: node picker for editing */}
          {mode === "edit-node-picker" && (
            <>
              <CommandInput placeholder="Search nodes to edit..." autoFocus />
              <CommandList>
                <CommandEmpty>
                  {nodes.length === 0 ? "Loading nodes…" : "No matching nodes."}
                </CommandEmpty>

                <CommandGroup>
                  <CommandItem value="__back__" onSelect={() => setMode("root")} className="text-muted-foreground">
                    <ChevronLeft className="mr-2 h-4 w-4" />
                    Back
                  </CommandItem>
                </CommandGroup>

                {nodes.length > 0 && (
                  <CommandGroup heading="Select node to edit">
                    {nodes.map((node) => (
                      <CommandItem
                        key={node.id}
                        value={`${node.hostname} ${node.id} ${node.primary_mac}`}
                        onSelect={() => pickNodeForEdit(node.id, node.hostname || node.id)}
                      >
                        <Server className="mr-2 h-4 w-4" />
                        <span className="font-medium">{node.hostname || node.id}</span>
                        <span className="ml-2 text-xs text-muted-foreground font-mono">{node.primary_mac}</span>
                      </CommandItem>
                    ))}
                  </CommandGroup>
                )}
              </CommandList>
            </>
          )}

          {/* NODE-DEL-4: node picker for deletion */}
          {mode === "delete-node-picker" && (
            <>
              <CommandInput placeholder="Search nodes to delete..." autoFocus />
              <CommandList>
                <CommandEmpty>
                  {nodes.length === 0 ? "Loading nodes…" : "No matching nodes."}
                </CommandEmpty>

                <CommandGroup>
                  <CommandItem value="__back__" onSelect={() => setMode("root")} className="text-muted-foreground">
                    <ChevronLeft className="mr-2 h-4 w-4" />
                    Back
                  </CommandItem>
                </CommandGroup>

                {nodes.length > 0 && (
                  <CommandGroup heading="Select node to delete">
                    {nodes.map((node) => (
                      <CommandItem
                        key={node.id}
                        value={`${node.hostname} ${node.id} ${node.primary_mac}`}
                        onSelect={() => pickNodeForDelete(node.id, node.hostname || node.id)}
                      >
                        <Server className="mr-2 h-4 w-4" />
                        <span className="font-medium">{node.hostname || node.id}</span>
                        <span className="ml-2 text-xs text-muted-foreground font-mono">{node.primary_mac}</span>
                      </CommandItem>
                    ))}
                  </CommandGroup>
                )}
              </CommandList>
            </>
          )}

          {/* IMG-DEL-3: image picker for deletion */}
          {mode === "delete-image-picker" && (
            <>
              <CommandInput placeholder="Search images to delete..." autoFocus />
              <CommandList>
                <CommandEmpty>
                  {images.length === 0 ? "Loading images…" : "No matching images."}
                </CommandEmpty>

                <CommandGroup>
                  <CommandItem value="__back__" onSelect={() => setMode("root")} className="text-muted-foreground">
                    <ChevronLeft className="mr-2 h-4 w-4" />
                    Back
                  </CommandItem>
                </CommandGroup>

                {images.length > 0 && (
                  <CommandGroup heading="Select image to delete">
                    {images.map((img) => (
                      <CommandItem
                        key={img.id}
                        value={`${img.name} ${img.version} ${img.id}`}
                        onSelect={() => {
                          onClose()
                          navigate({ to: "/images", search: { q: undefined, tab: undefined, sort: undefined, dir: undefined, addImage: undefined } })
                          // Navigate to images page; operator uses the ImageSheet delete UI.
                          // The image ID is preserved via recent entity for quick access.
                          recordRecentEntity({ kind: "image", id: img.id, label: `${img.name} ${img.version ?? ""}`.trim() })
                        }}
                      >
                        <ImageIcon className="mr-2 h-4 w-4" />
                        <span className="font-medium">{img.name}</span>
                        <span className="ml-2 text-xs text-muted-foreground">{img.version}</span>
                        <span className="ml-auto text-xs text-muted-foreground font-mono">{img.id.slice(0, 8)}</span>
                      </CommandItem>
                    ))}
                  </CommandGroup>
                )}
              </CommandList>
            </>
          )}
        </Command>
      </DialogContent>
    </Dialog>
  )
}
