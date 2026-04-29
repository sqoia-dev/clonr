import * as React from "react"
import { useNavigate } from "@tanstack/react-router"
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
import { Server, Image, Activity, Settings, RefreshCw, Key, Clock } from "lucide-react"

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
  { label: "Images", path: "/images", icon: Image },
  { label: "Activity", path: "/activity", icon: Activity },
  { label: "Settings", path: "/settings", icon: Settings },
]

// ─── CommandPalette ───────────────────────────────────────────────────────────

interface Props {
  open: boolean
  onClose: () => void
}

export function CommandPalette({ open, onClose }: Props) {
  const navigate = useNavigate()
  const [recent, setRecent] = React.useState<RecentEntity[]>([])

  // Load recent entities when palette opens.
  React.useEffect(() => {
    if (open) {
      setRecent(loadRecentEntities())
    }
  }, [open])

  function goTo(path: string) {
    onClose()
    if (path === "/nodes") navigate({ to: "/nodes", search: { q: undefined, status: undefined, sort: undefined, dir: undefined } })
    else if (path === "/images") navigate({ to: "/images", search: { q: undefined, tab: undefined, sort: undefined, dir: undefined } })
    else if (path === "/activity") navigate({ to: "/activity", search: { q: undefined, kind: undefined } })
    else navigate({ to: "/settings" })
  }

  function reimageNode() {
    onClose()
    navigate({ to: "/nodes", search: { q: undefined, status: undefined, sort: undefined, dir: undefined } })
    // The reimage flow is triggered inline from the Node detail Sheet.
  }

  function createAPIKey() {
    onClose()
    navigate({ to: "/settings" })
    // The create form is inline in Settings → API Keys.
  }

  function visitRecent(entity: RecentEntity) {
    onClose()
    if (entity.kind === "node") {
      navigate({ to: "/nodes", search: { q: undefined, status: undefined, sort: undefined, dir: undefined } })
    } else {
      navigate({ to: "/images", search: { q: undefined, tab: undefined, sort: undefined, dir: undefined } })
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="p-0 gap-0 max-w-md">
        <Command className="rounded-lg">
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

            {/* Actions (PAL-1..4) */}
            <CommandGroup heading="Actions">
              <CommandItem value="reimage node" onSelect={reimageNode}>
                <RefreshCw className="mr-2 h-4 w-4" />
                Reimage node…
                <span className="ml-auto text-xs text-muted-foreground">Select node → Reimage</span>
              </CommandItem>
              <CommandItem value="create api key" onSelect={createAPIKey}>
                <Key className="mr-2 h-4 w-4" />
                Create API key…
                <span className="ml-auto text-xs text-muted-foreground">Settings → API Keys</span>
              </CommandItem>
              <CommandItem
                value="upload image"
                onSelect={() => {
                  onClose()
                  // PAL-4: CLI-only, link to docs.
                  window.open("https://github.com/sqoia-dev/clustr", "_blank", "noopener")
                }}
              >
                <Image className="mr-2 h-4 w-4" />
                Upload image…
                <span className="ml-auto text-xs text-muted-foreground">CLI only</span>
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
        </Command>
      </DialogContent>
    </Dialog>
  )
}
