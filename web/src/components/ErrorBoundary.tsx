// ErrorBoundary — route-level error boundary (POL-4).
// Catches render errors in children, shows a recovery card.
// Shows error details only when ?debug=1 is in the URL.
//
// SectionErrorBoundary — lightweight per-section boundary.
// Wraps individual page sections so one broken section doesn't kill the page.
// Always logs the error to console with the section name for DevTools diagnosis.
import * as React from "react"
import { Button } from "@/components/ui/button"
import { AlertTriangle } from "lucide-react"

interface State {
  error: Error | null
  // Last 5 user action labels (no PII, no payload data).
  actions: string[]
}

interface Props {
  children: React.ReactNode
}

export const actionLog: string[] = []

/** Record a user action label (max 5, FIFO). Call this before mutations/navigations. */
export function recordAction(label: string) {
  actionLog.unshift(label)
  if (actionLog.length > 5) actionLog.length = 5
}

// ─── SectionErrorBoundary ─────────────────────────────────────────────────────

interface SectionBoundaryProps {
  section: string
  children: React.ReactNode
}

interface SectionBoundaryState {
  error: Error | null
}

export class SectionErrorBoundary extends React.Component<SectionBoundaryProps, SectionBoundaryState> {
  constructor(props: SectionBoundaryProps) {
    super(props)
    this.state = { error: null }
  }

  static getDerivedStateFromError(error: Error): SectionBoundaryState {
    return { error }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // Always log with section name so DevTools shows exactly which section crashed.
    console.error(`[SectionErrorBoundary:${this.props.section}] render error:`, error, info.componentStack)
  }

  render() {
    if (!this.state.error) return this.props.children
    return (
      <div className="rounded border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm text-destructive flex items-center gap-2">
        <AlertTriangle className="h-4 w-4 shrink-0" />
        <span>
          <span className="font-medium">{this.props.section}</span> failed to render.
          Check DevTools console for details.
        </span>
      </div>
    )
  }
}

// ─── ErrorBoundary ────────────────────────────────────────────────────────────

export class ErrorBoundary extends React.Component<Props, State> {
  constructor(props: Props) {
    super(props)
    this.state = { error: null, actions: [] }
  }

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error, actions: [...actionLog] }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // Log to console but never to a remote service (no telemetry).
    console.error("[ErrorBoundary] caught:", error, info.componentStack)
  }

  handleReload = () => {
    window.location.reload()
  }

  handleDismiss = () => {
    this.setState({ error: null, actions: [] })
  }

  render() {
    const { error, actions } = this.state
    if (!error) return this.props.children

    const showDebug =
      typeof window !== "undefined" &&
      new URLSearchParams(window.location.search).get("debug") === "1"

    return (
      <div className="flex items-center justify-center min-h-[60vh] p-8">
        <div className="max-w-md w-full rounded-lg border border-destructive/40 bg-card p-6 space-y-4">
          <div className="flex items-center gap-3 text-destructive">
            <AlertTriangle className="h-5 w-5 shrink-0" />
            <h2 className="font-semibold text-sm">Something went wrong</h2>
          </div>

          <p className="text-sm text-muted-foreground">
            An unexpected error occurred rendering this page. Your data is safe — this is a display error.
          </p>

          {actions.length > 0 && (
            <div>
              <p className="text-xs font-medium text-muted-foreground mb-1">Recent actions:</p>
              <ul className="text-xs text-muted-foreground space-y-0.5 font-mono">
                {actions.map((a, i) => (
                  <li key={i} className="truncate">· {a}</li>
                ))}
              </ul>
            </div>
          )}

          {showDebug && (
            <pre className="text-xs text-destructive bg-destructive/10 rounded p-2 overflow-auto max-h-40">
              {error.message}
              {"\n\n"}
              {error.stack}
            </pre>
          )}

          <div className="flex gap-2">
            <Button size="sm" onClick={this.handleReload}>
              Reload page
            </Button>
            <Button size="sm" variant="ghost" onClick={this.handleDismiss}>
              Dismiss
            </Button>
          </div>
        </div>
      </div>
    )
  }
}
