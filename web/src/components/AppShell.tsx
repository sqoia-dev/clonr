import * as React from "react"
import { Link, useRouterState, useNavigate } from "@tanstack/react-router"
import { Server, Image, Activity, Settings, ChevronsLeft, ChevronsRight, Command as CmdIcon, Sun, Moon, LogOut, User, WifiOff } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { CommandPalette } from "@/components/CommandPalette"
import { ErrorBoundary } from "@/components/ErrorBoundary"
import { useTheme } from "@/contexts/theme"
import { useConnection } from "@/contexts/connection"
import { useSession } from "@/contexts/auth"
import { apiFetch } from "@/lib/api"
import { cn } from "@/lib/utils"

const navItems = [
  { label: "Nodes", path: "/nodes", icon: Server, active: true },
  { label: "Images", path: "/images", icon: Image, active: true },
  { label: "Activity", path: "/activity", icon: Activity, active: true },
  { label: "Settings", path: "/settings", icon: Settings, active: true },
]

const connectionConfig = {
  connected: { color: "bg-status-healthy", label: "Connected" },
  reconnecting: { color: "bg-status-warning animate-pulse", label: "Reconnecting" },
  disconnected: { color: "bg-status-neutral", label: "Disconnected" },
  paused: { color: "bg-status-error", label: "Live updates paused" },
}

export function AppShell({ children }: { children: React.ReactNode }) {
  const [collapsed, setCollapsed] = React.useState(false)
  const [paletteOpen, setPaletteOpen] = React.useState(false)
  const [userMenuOpen, setUserMenuOpen] = React.useState(false)
  const { theme, toggle } = useTheme()
  const { status, paused, retry } = useConnection()
  const { session, setUnauthed } = useSession()
  const routerState = useRouterState()
  const navigate = useNavigate()
  const currentPath = routerState.location.pathname

  const username =
    session.status === "authed" ? (session.user.username ?? session.user.sub) : ""

  // Cmd-K + vim-style leader keys (g n/i/a/s)
  const gKeyPending = React.useRef(false)
  const gTimer = React.useRef<ReturnType<typeof setTimeout> | null>(null)

  React.useEffect(() => {
    function onKey(e: KeyboardEvent) {
      // Skip if focused on an input/textarea.
      const tag = (e.target as HTMLElement)?.tagName?.toLowerCase()
      const editable = (e.target as HTMLElement)?.isContentEditable
      if (tag === "input" || tag === "textarea" || tag === "select" || editable) return

      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault()
        setPaletteOpen(true)
        return
      }

      // Vim-style leader: g then n/i/a/s
      if (gKeyPending.current) {
        gKeyPending.current = false
        if (gTimer.current) clearTimeout(gTimer.current)
        switch (e.key) {
          case "n": navigate({ to: "/nodes", search: { q: undefined, status: undefined, sort: undefined, dir: undefined, openNode: undefined, reimage: undefined, addNode: undefined } }); break
          case "i": navigate({ to: "/images", search: { q: undefined, tab: undefined, sort: undefined, dir: undefined } }); break
          case "a": navigate({ to: "/activity", search: { q: undefined, kind: undefined } }); break
          case "s": navigate({ to: "/settings" }); break
        }
        return
      }

      if (e.key === "g" && !e.metaKey && !e.ctrlKey && !e.altKey) {
        gKeyPending.current = true
        gTimer.current = setTimeout(() => { gKeyPending.current = false }, 1000)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [navigate])

  // Close user menu on outside click.
  const userMenuRef = React.useRef<HTMLDivElement>(null)
  React.useEffect(() => {
    if (!userMenuOpen) return
    function onClick(e: MouseEvent) {
      if (userMenuRef.current && !userMenuRef.current.contains(e.target as Node)) {
        setUserMenuOpen(false)
      }
    }
    document.addEventListener("mousedown", onClick)
    return () => document.removeEventListener("mousedown", onClick)
  }, [userMenuOpen])

  async function handleLogout() {
    try {
      await apiFetch("/api/v1/auth/logout", { method: "POST" })
    } catch {
      // Ignore errors — clear local state regardless.
    }
    setUnauthed()
    setUserMenuOpen(false)
  }

  // POL-6: use "paused" config when SSE has been down for >30s.
  const conn = paused ? connectionConfig.paused : connectionConfig[status]

  return (
    <div className="flex h-full bg-background text-foreground">
      {/* A11Y-1: skip-to-main link */}
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:top-2 focus:left-2 focus:px-3 focus:py-1.5 focus:rounded focus:bg-primary focus:text-primary-foreground focus:text-sm"
      >
        Skip to main content
      </a>

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
                className={cn(
                  "flex items-center gap-3 rounded-md px-2 py-2 text-sm transition-colors",
                  isActive
                    ? "bg-secondary text-foreground"
                    : "text-muted-foreground hover:bg-secondary/50 hover:text-foreground"
                )}
              >
                <item.icon className="h-4 w-4 shrink-0" />
                {!collapsed && <span>{item.label}</span>}
              </Link>
            )
            if (collapsed) {
              return (
                <Tooltip key={item.path}>
                  <TooltipTrigger asChild>{el}</TooltipTrigger>
                  <TooltipContent side="right">{item.label}</TooltipContent>
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

            {/* User menu */}
            <div className="relative" ref={userMenuRef}>
              <Button
                variant="ghost"
                size="sm"
                className="gap-1.5 text-xs text-muted-foreground"
                onClick={() => setUserMenuOpen((v) => !v)}
              >
                <User className="h-3.5 w-3.5" />
                {username && <span className="max-w-24 truncate">{username}</span>}
              </Button>

              {userMenuOpen && (
                <div className="absolute right-0 top-full mt-1 w-40 rounded-md border border-border bg-card shadow-md z-50">
                  <button
                    className="flex w-full items-center gap-2 px-3 py-2 text-sm text-muted-foreground hover:bg-secondary/50 hover:text-foreground rounded-md"
                    onClick={handleLogout}
                  >
                    <LogOut className="h-3.5 w-3.5" />
                    Sign out
                  </button>
                </div>
              )}
            </div>
          </div>
        </header>

        {/* POL-6: SSE disconnect banner — shown after 30s of no connection */}
        {paused && (
          <div
            role="alert"
            className="flex items-center justify-between gap-3 px-4 py-2 bg-destructive/10 border-b border-destructive/30 text-sm text-destructive shrink-0"
          >
            <span className="flex items-center gap-2">
              <WifiOff className="h-4 w-4 shrink-0" aria-hidden="true" />
              Live updates paused. Check your network connection.
            </span>
            <button
              onClick={retry}
              className="underline underline-offset-2 hover:no-underline shrink-0 text-sm"
            >
              Retry
            </button>
          </div>
        )}

        {/* Page content */}
        <main id="main-content" className="flex-1 overflow-auto" tabIndex={-1}>
          <ErrorBoundary>
            {children}
          </ErrorBoundary>
        </main>
      </div>

      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />
    </div>
  )
}
