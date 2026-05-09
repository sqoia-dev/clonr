/**
 * HostlistInput.tsx — live-preview hostlist range input (Sprint 34 UI-A)
 *
 * Renders an <Input> that accepts pyhostlist-style range syntax (e.g. node[01-12,20-25]).
 * As the user types, a preview accordion below the input shows the expanded host count
 * and the full expanded list (truncated at PREVIEW_LIMIT with "… and N more" indicator).
 */

import * as React from "react"
import { Input } from "@/components/ui/input"
import { expandHostlist } from "@/lib/hostlist"
import { cn } from "@/lib/utils"

const PREVIEW_LIMIT = 20

export interface HostlistInputProps {
  value: string
  onChange: (value: string) => void
  onExpanded?: (names: string[]) => void
  placeholder?: string
  className?: string
  disabled?: boolean
  id?: string
}

export function HostlistInput({
  value,
  onChange,
  onExpanded,
  placeholder = "node[01-12] or compute01",
  className,
  disabled,
  id,
}: HostlistInputProps) {
  const [expanded, setExpanded] = React.useState<string[]>([])
  const [error, setError] = React.useState<string | null>(null)
  const [showList, setShowList] = React.useState(false)

  React.useEffect(() => {
    const trimmed = value.trim()
    if (!trimmed) {
      setExpanded([])
      setError(null)
      onExpanded?.([])
      return
    }
    try {
      const result = expandHostlist(trimmed)
      setExpanded(result)
      setError(null)
      onExpanded?.(result)
    } catch (err) {
      setExpanded([])
      setError(err instanceof Error ? err.message : String(err))
      onExpanded?.([])
    }
  }, [value]) // eslint-disable-line react-hooks/exhaustive-deps

  const hasInput = value.trim().length > 0
  const isBracketPattern = value.includes("[")

  return (
    <div className={cn("space-y-1", className)}>
      <Input
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        disabled={disabled}
        className={cn(
          error && "border-destructive",
          "font-mono text-xs",
        )}
        aria-invalid={!!error}
        aria-describedby={error ? `${id ?? "hostlist"}-error` : undefined}
      />

      {/* Error message */}
      {error && hasInput && (
        <p
          id={`${id ?? "hostlist"}-error`}
          className="text-xs text-destructive"
          role="alert"
        >
          {error}
        </p>
      )}

      {/* Live preview — only shown when there's a valid bracket expansion */}
      {!error && hasInput && isBracketPattern && expanded.length > 0 && (
        <div className="rounded-md border border-border bg-secondary/20 px-3 py-2 space-y-1.5">
          <button
            type="button"
            className="flex items-center justify-between w-full text-left"
            onClick={() => setShowList((s) => !s)}
            aria-expanded={showList}
          >
            <span className="text-xs text-muted-foreground">
              <span
                className="inline-flex items-center gap-1 rounded-full bg-primary/10 text-primary px-2 py-0.5 text-xs font-medium mr-1"
                aria-label={`${expanded.length} hosts`}
                data-testid="hostlist-count-badge"
              >
                {expanded.length}
              </span>
              host{expanded.length !== 1 ? "s" : ""} expanded
            </span>
            <span className="text-xs text-muted-foreground opacity-60">
              {showList ? "collapse" : "show list"}
            </span>
          </button>

          {showList && (
            <div
              className="font-mono text-xs text-muted-foreground leading-relaxed max-h-32 overflow-y-auto"
              data-testid="hostlist-preview-list"
              role="list"
              aria-label="Expanded hostnames"
            >
              {expanded.slice(0, PREVIEW_LIMIT).map((h) => (
                <div key={h} role="listitem">{h}</div>
              ))}
              {expanded.length > PREVIEW_LIMIT && (
                <div className="text-muted-foreground/60 italic">
                  … and {expanded.length - PREVIEW_LIMIT} more
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {/* Plain hostname with no error — just show count=1 badge if non-empty */}
      {!error && hasInput && !isBracketPattern && (
        <p className="text-xs text-muted-foreground">
          <span
            className="inline-flex items-center gap-1 rounded-full bg-primary/10 text-primary px-2 py-0.5 text-xs font-medium mr-1"
            data-testid="hostlist-count-badge"
          >
            1
          </span>
          host
        </p>
      )}
    </div>
  )
}
