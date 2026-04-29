import * as React from "react"
import { Link, useRouterState } from "@tanstack/react-router"
import { Server, Image, Activity, Settings, ChevronsLeft, ChevronsRight, Command as CmdIcon, Sun, Moon } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { CommandPalette } from "@/components/CommandPalette"
import { useTheme } from "@/contexts/theme"
import { useConnection } from "@/contexts/connection"
import { cn } from "@/lib/utils"

const navItems = [
  { label: "Nodes", path: "/nodes", icon: Server, active: true },
  { label: "Images", path: "/images", icon: Image, active: false },
  { label: "Activity", path: "/activity", icon: Activity, active: false },
  { label: "Settings", path: "/settings", icon: Settings, active: false },
]

const connectionConfig = {
  connected: { color: "bg-status-healthy", label: "Connected" },
  reconnecting: { color: "bg-status-warning animate-pulse", label: "Reconnecting" },
  disconnected: { color: "bg-status-neutral", label: "Disconnected" },
}

export function AppShell({ children }: { children: React.ReactNode }) {
  const [collapsed, setCollapsed] = React.useState(false)
  const [paletteOpen, setPaletteOpen] = React.useState(false)
  const { theme, toggle } = useTheme()
  const { status } = useConnection()
  const routerState = useRouterState()
  const currentPath = routerState.location.pathname

  React.useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault()
        setPaletteOpen(true)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])

  const conn = connectionConfig[status]

  return (
    <div className="flex h-full bg-background text-foreground">
      {/* Sidebar */}
      <aside
        className={cn(
          "flex flex-col border-r border-border bg-card transition-all duration-200",
          collapsed ? "w-14" : "w-52"
        )}
      >
        {/* Logo */}
        <div className={cn("flex h-14 items-center border-b border-border px-3", collapsed ? "justify-center" : "gap-2")}>
          <span className="inline-flex h-7 w-7 items-center justify-center rounded bg-primary text-primary-foreground text-xs font-bold shrink-0">
            C
          </span>
          {!collapsed && <span className="font-semibold text-sm">clustr</span>}
        </div>

        {/* Nav */}
        <nav className="flex flex-col gap-1 p-2 flex-1">
          {navItems.map((item) => {
            const isActive = currentPath.startsWith(item.path)
            const el = (
              <Link
                key={item.path}
                to={item.path}
                disabled={!item.active}
                className={cn(
                  "flex items-center gap-3 rounded-md px-2 py-2 text-sm transition-colors",
                  isActive
                    ? "bg-secondary text-foreground"
                    : "text-muted-foreground hover:bg-secondary/50 hover:text-foreground",
                  !item.active && "pointer-events-none opacity-40"
                )}
              >
                <item.icon className="h-4 w-4 shrink-0" />
                {!collapsed && <span>{item.label}</span>}
              </Link>
            )
            if (!item.active && collapsed) {
              return (
                <Tooltip key={item.path}>
                  <TooltipTrigger asChild>{el}</TooltipTrigger>
                  <TooltipContent side="right">Sprint 2</TooltipContent>
                </Tooltip>
              )
            }
            if (!item.active) {
              return (
                <Tooltip key={item.path}>
                  <TooltipTrigger asChild>{el}</TooltipTrigger>
                  <TooltipContent side="right">{item.label} — Sprint 2</TooltipContent>
                </Tooltip>
              )
            }
            return el
          })}
        </nav>

        {/* Collapse toggle */}
        <div className="border-t border-border p-2">
          <Button
            variant="ghost"
            size="icon"
            className="w-full"
            onClick={() => setCollapsed((c) => !c)}
          >
            {collapsed ? <ChevronsRight className="h-4 w-4" /> : <ChevronsLeft className="h-4 w-4" />}
          </Button>
        </div>
      </aside>

      {/* Main content */}
      <div className="flex flex-col flex-1 min-w-0">
        {/* Top bar */}
        <header className="flex h-14 items-center justify-between border-b border-border px-4 bg-card shrink-0">
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              className="gap-2 text-muted-foreground text-xs"
              onClick={() => setPaletteOpen(true)}
            >
              <CmdIcon className="h-3.5 w-3.5" />
              <span>Command</span>
              <kbd className="pointer-events-none ml-1 select-none rounded border border-border bg-muted px-1 text-[10px] font-mono">
                ⌘K
              </kbd>
            </Button>
          </div>

          <div className="flex items-center gap-3">
            {/* Connection indicator */}
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span className={cn("h-2 w-2 rounded-full shrink-0", conn.color)} />
              <span>{conn.label}</span>
            </div>

            {/* Theme toggle */}
            <Button variant="ghost" size="icon" onClick={toggle}>
              {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            </Button>
          </div>
        </header>

        {/* Page content */}
        <main className="flex-1 overflow-auto">
          {children}
        </main>
      </div>

      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />
    </div>
  )
}
