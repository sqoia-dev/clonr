import * as React from "react"
import { useQuery } from "@tanstack/react-query"
import { Search, User } from "lucide-react"
import { apiFetch } from "@/lib/api"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"
import type { UserSearchResult, UserSearchResponse } from "@/lib/types"

interface UserPickerProps {
  /** Called when the operator selects a user. */
  onSelect: (user: UserSearchResult) => void
  /** Which sources to search: "all" | "local" | "ldap" */
  source?: "all" | "local" | "ldap"
  placeholder?: string
  className?: string
}

/**
 * UserPicker — a searchable dropdown that queries both local and LDAP users.
 * NODE-SUDO-5 / GRP-WEB-2: reusable component for sudoers + group member pickers.
 */
export function UserPicker({ onSelect, source = "all", placeholder = "Search users…", className }: UserPickerProps) {
  const [q, setQ] = React.useState("")
  const [open, setOpen] = React.useState(false)
  const containerRef = React.useRef<HTMLDivElement>(null)

  const { data, isFetching } = useQuery<UserSearchResponse>({
    queryKey: ["user-search", q, source],
    queryFn: () =>
      apiFetch<UserSearchResponse>(`/api/v1/users/search?q=${encodeURIComponent(q)}&source=${source}`),
    enabled: open,
    staleTime: 5000,
  })

  // Close on outside click.
  React.useEffect(() => {
    if (!open) return
    function onClick(e: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener("mousedown", onClick)
    return () => document.removeEventListener("mousedown", onClick)
  }, [open])

  const users = data?.users ?? []

  function handleSelect(user: UserSearchResult) {
    onSelect(user)
    setQ("")
    setOpen(false)
  }

  return (
    <div className={cn("relative", className)} ref={containerRef}>
      <div className="relative">
        <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground pointer-events-none" />
        <Input
          className="pl-7 text-xs h-8"
          placeholder={placeholder}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onFocus={() => setOpen(true)}
        />
      </div>

      {open && (
        <div className="absolute z-50 mt-1 w-full rounded-md border border-border bg-card shadow-lg">
          {isFetching && (
            <div className="px-3 py-2 text-xs text-muted-foreground">Searching…</div>
          )}
          {!isFetching && users.length === 0 && (
            <div className="px-3 py-2 text-xs text-muted-foreground">No users found</div>
          )}
          {!isFetching && users.map((u) => (
            <button
              key={`${u.source}:${u.identifier}`}
              className="flex w-full items-center gap-2 px-3 py-2 text-xs hover:bg-secondary/50 text-left"
              onMouseDown={(e) => {
                e.preventDefault() // Prevent blur before click.
                handleSelect(u)
              }}
            >
              <User className="h-3 w-3 shrink-0 text-muted-foreground" />
              <span className="flex-1 min-w-0 truncate font-mono">{u.identifier}</span>
              {u.display_name !== u.identifier && (
                <span className="text-muted-foreground truncate max-w-[120px]">{u.display_name}</span>
              )}
              <span className={cn(
                "shrink-0 rounded px-1 py-0.5 text-[10px] font-medium",
                u.source === "ldap"
                  ? "bg-blue-500/10 text-blue-400"
                  : "bg-green-500/10 text-green-400"
              )}>
                {u.source}
              </span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
