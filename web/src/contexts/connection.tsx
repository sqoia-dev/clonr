// connection.tsx — tracks SSE connectivity state.
// POL-6: after 30s of non-"connected" state, `paused` becomes true.
// AppShell reads `paused` to show a banner; `retry` reloads the SSE.
import * as React from "react"

export type ConnectionStatus = "connected" | "reconnecting" | "disconnected"

interface ConnectionContextValue {
  status: ConnectionStatus
  /** True when SSE has been non-connected for >30s (POL-6). */
  paused: boolean
  /** Update status; clears `paused` on transition to "connected". */
  setStatus: (s: ConnectionStatus) => void
  /**
   * Incrementing token. Components that depend on SSE watch this and
   * restart their EventSource when it changes (triggered by user "retry").
   */
  retryToken: number
  /** Force SSE reconnect by bumping retryToken. Clears paused state. */
  retry: () => void
}

const ConnectionContext = React.createContext<ConnectionContextValue | null>(null)

const PAUSED_DELAY_MS = 30_000

export function ConnectionProvider({ children }: { children: React.ReactNode }) {
  const [status, setStatusRaw] = React.useState<ConnectionStatus>("disconnected")
  const [paused, setPaused] = React.useState(false)
  const [retryToken, setRetryToken] = React.useState(0)

  const pausedTimer = React.useRef<ReturnType<typeof setTimeout> | null>(null)

  function setStatus(s: ConnectionStatus) {
    setStatusRaw(s)
    if (s === "connected") {
      // Clear the paused state and cancel any pending timer.
      if (pausedTimer.current) {
        clearTimeout(pausedTimer.current)
        pausedTimer.current = null
      }
      setPaused(false)
    } else {
      // Start the 30s timer if not already running.
      if (!pausedTimer.current) {
        pausedTimer.current = setTimeout(() => {
          setPaused(true)
          pausedTimer.current = null
        }, PAUSED_DELAY_MS)
      }
    }
  }

  function retry() {
    setPaused(false)
    if (pausedTimer.current) {
      clearTimeout(pausedTimer.current)
      pausedTimer.current = null
    }
    setRetryToken((t) => t + 1)
  }

  // Clean up timer on unmount.
  React.useEffect(() => {
    return () => {
      if (pausedTimer.current) clearTimeout(pausedTimer.current)
    }
  }, [])

  return (
    <ConnectionContext.Provider value={{ status, paused, setStatus, retryToken, retry }}>
      {children}
    </ConnectionContext.Provider>
  )
}

export function useConnection(): ConnectionContextValue {
  const ctx = React.useContext(ConnectionContext)
  if (!ctx) throw new Error("useConnection must be used inside ConnectionProvider")
  return ctx
}
