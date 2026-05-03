// connection.tsx — UX-4: owns ONE EventSource to /api/v1/events.
//
// ConnectionProvider opens a single multiplexed SSE connection per browser tab.
// All live-update consumers subscribe through the hooks below instead of opening
// their own EventSource connections.
//
// POL-6: after 30s of non-"open" state, `paused` becomes true.
// AppShell reads `paused` to show the "Live updates paused" banner.
// The `retry` function bumps retryToken, tearing down and reopening the EventSource.
import * as React from "react"
import { useQueryClient } from "@tanstack/react-query"
import { sseUrl } from "@/lib/api"
import { sseReconnectDelay } from "@/lib/sse-backoff"

// ─── Types ────────────────────────────────────────────────────────────────────

export type ConnectionStatus = "connecting" | "open" | "reconnecting" | "failed"

/** Topics emitted by GET /api/v1/events. Matches server-side eventbus.Topic. */
export type EventTopic =
  | "nodes"
  | "images"
  | "progress"
  | "alerts"
  | "stats"
  | "bundles"
  | "groups"
  | "ping"

/** Callback invoked with the raw parsed JSON payload for a matching event. */
export type EventCallback<T = unknown> = (data: T) => void

// ─── Internal event bus (in-process) ─────────────────────────────────────────

// One Map per topic, each map holds subscriber id → callback.
type SubscriberMap = Map<string, EventCallback>
type TopicMap = Map<EventTopic, SubscriberMap>

let nextSubId = 0
function freshSubId() {
  return String(++nextSubId)
}

// ─── Context ──────────────────────────────────────────────────────────────────

interface ConnectionContextValue {
  status: ConnectionStatus
  /** True when SSE has been non-open for >30s (POL-6). */
  paused: boolean
  /**
   * Subscribe to a specific event topic. Calls `callback(data)` for each
   * matching event. Returns an unsubscribe function — call it on unmount.
   *
   * @deprecated Prefer useEventInvalidation for query-invalidation patterns.
   */
  subscribe: <T>(topic: EventTopic, callback: EventCallback<T>) => () => void
  /** Force reconnect — clears paused state and bumps retryToken. */
  retry: () => void
}

const ConnectionContext = React.createContext<ConnectionContextValue | null>(null)

// ─── Provider ─────────────────────────────────────────────────────────────────

const PAUSED_DELAY_MS = 30_000
const SSE_PATH = "/api/v1/events"

export function ConnectionProvider({ children }: { children: React.ReactNode }) {
  const [status, setStatus] = React.useState<ConnectionStatus>("connecting")
  const [paused, setPaused] = React.useState(false)
  const [retryToken, setRetryToken] = React.useState(0)

  // In-process fan-out: the single EventSource writes here; hooks read from here.
  const topicsRef = React.useRef<TopicMap>(new Map())

  const pausedTimer = React.useRef<ReturnType<typeof setTimeout> | null>(null)

  // ── Status management ───────────────────────────────────────────────────────
  const markOpen = React.useCallback(() => {
    setStatus("open")
    if (pausedTimer.current) {
      clearTimeout(pausedTimer.current)
      pausedTimer.current = null
    }
    setPaused(false)
  }, [])

  const markNonOpen = React.useCallback((s: "connecting" | "reconnecting" | "failed") => {
    setStatus(s)
    if (!pausedTimer.current) {
      pausedTimer.current = setTimeout(() => {
        setPaused(true)
        pausedTimer.current = null
      }, PAUSED_DELAY_MS)
    }
  }, [])

  // ── Fan-out: dispatch an event to all subscribers of that topic ──────────────
  const dispatch = React.useCallback((topic: EventTopic, data: unknown) => {
    const subs = topicsRef.current.get(topic)
    if (!subs) return
    for (const cb of subs.values()) {
      try {
        cb(data)
      } catch {
        // subscriber threw — don't crash the whole fan-out
      }
    }
  }, [])

  // ── Subscribe helper exposed via context ────────────────────────────────────
  const subscribe = React.useCallback(
    <T,>(topic: EventTopic, callback: EventCallback<T>): (() => void) => {
      const id = freshSubId()
      if (!topicsRef.current.has(topic)) {
        topicsRef.current.set(topic, new Map())
      }
      topicsRef.current.get(topic)!.set(id, callback as EventCallback)
      return () => {
        topicsRef.current.get(topic)?.delete(id)
      }
    },
    []
  )

  // ── Retry ────────────────────────────────────────────────────────────────────
  const retry = React.useCallback(() => {
    setPaused(false)
    if (pausedTimer.current) {
      clearTimeout(pausedTimer.current)
      pausedTimer.current = null
    }
    setRetryToken((t) => t + 1)
  }, [])

  // ── Single EventSource lifecycle ────────────────────────────────────────────
  React.useEffect(() => {
    let es: EventSource | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null
    let destroyed = false
    let attempt = 0

    function connect() {
      if (destroyed) return
      markNonOpen("connecting")

      const url = sseUrl(SSE_PATH)
      es = new EventSource(url, { withCredentials: true })

      es.onopen = () => {
        attempt = 0
        markOpen()
      }

      // Typed events from the server arrive as named SSE events.
      // The server emits `event: <topic>\ndata: {...}\n\n`.
      const allTopics: EventTopic[] = [
        "nodes", "images", "progress", "alerts", "stats", "bundles", "groups", "ping",
      ]
      for (const topic of allTopics) {
        es.addEventListener(topic, (ev: MessageEvent) => {
          try {
            const data = JSON.parse((ev as MessageEvent).data)
            dispatch(topic, data)
          } catch {
            // malformed payload — ignore
          }
        })
      }

      es.onerror = () => {
        if (destroyed) return
        attempt++
        markNonOpen("reconnecting")
        es?.close()
        es = null
        reconnectTimer = setTimeout(connect, sseReconnectDelay(attempt))
      }
    }

    connect()

    return () => {
      destroyed = true
      if (reconnectTimer) clearTimeout(reconnectTimer)
      es?.close()
      // Do NOT flip status to "failed" here. Cleanup runs on retryToken change
      // (intentional reconnect) and on unmount. The onerror path already
      // signals "reconnecting" for genuine failures; cleanup is silent.
    }
    // retryToken in deps: change → teardown + immediate reconnect.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [retryToken, markOpen, markNonOpen, dispatch])

  // ── Cleanup timer on unmount ─────────────────────────────────────────────────
  React.useEffect(() => {
    return () => {
      if (pausedTimer.current) clearTimeout(pausedTimer.current)
    }
  }, [])

  const value = React.useMemo(
    () => ({ status, paused, subscribe, retry }),
    [status, paused, subscribe, retry]
  )

  return (
    <ConnectionContext.Provider value={value}>
      {children}
    </ConnectionContext.Provider>
  )
}

// ─── Hooks ────────────────────────────────────────────────────────────────────

/** Returns current SSE connection status and the paused flag. */
export function useConnectionStatus(): { status: ConnectionStatus; paused: boolean; retry: () => void } {
  const ctx = React.useContext(ConnectionContext)
  if (!ctx) throw new Error("useConnectionStatus must be used inside ConnectionProvider")
  return { status: ctx.status, paused: ctx.paused, retry: ctx.retry }
}

/**
 * Subscribe to a specific event topic. Calls `callback` with the parsed JSON
 * payload whenever a matching event arrives. Auto-unsubscribes on unmount.
 *
 * @example
 *   useEventSubscription("nodes", (data) => { console.log(data) })
 */
export function useEventSubscription<T>(
  topic: EventTopic,
  callback: EventCallback<T>
): void {
  const ctx = React.useContext(ConnectionContext)
  if (!ctx) throw new Error("useEventSubscription must be used inside ConnectionProvider")

  // Stable ref so the subscribe call doesn't re-run when callback identity changes.
  const cbRef = React.useRef(callback)
  cbRef.current = callback

  React.useEffect(() => {
    const unsub = ctx.subscribe<T>(topic, (data) => cbRef.current(data))
    return unsub
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [topic, ctx.subscribe])
}

/**
 * When an event of `topic` arrives, invalidate the React Query cache for
 * `queryKey`. This is the primary hook for keeping list/detail queries fresh.
 *
 * @example
 *   // Refetch nodes list whenever a nodes event fires.
 *   useEventInvalidation("nodes", ["nodes"])
 *
 *   // Refetch a specific image record.
 *   useEventInvalidation("images", ["images", imageId])
 */
export function useEventInvalidation(
  topic: EventTopic,
  queryKey: unknown[]
): void {
  const queryClient = useQueryClient()
  useEventSubscription(topic, () => {
    queryClient.invalidateQueries({ queryKey })
  })
}

// ─── Backward-compat: useConnection ──────────────────────────────────────────
// Kept for the AppShell banner and any callers reading `paused`/`retry`/`status`.
// The setStatus callback is removed — the provider owns status internally now.

/** @deprecated Use useConnectionStatus() instead. */
export interface ConnectionContextLegacy {
  status: ConnectionStatus
  paused: boolean
  retry: () => void
  /**
   * @deprecated No-op shim. Status is now managed internally by ConnectionProvider.
   * Accepts any string to ease migration from the old "connected"/"disconnected" vocabulary.
   */
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  setStatus: (s: any) => void
  /** @deprecated Use useEventSubscription / useEventInvalidation. */
  retryToken: number
}

/**
 * Returns connection status, paused flag, and retry function.
 * @deprecated Use useConnectionStatus(), useEventSubscription(), or useEventInvalidation().
 */
export function useConnection(): ConnectionContextLegacy {
  const ctx = React.useContext(ConnectionContext)
  if (!ctx) throw new Error("useConnection must be used inside ConnectionProvider")
  // retryToken is no longer meaningful outside the provider — return 0 as a shim.
  return {
    status: ctx.status,
    paused: ctx.paused,
    retry: ctx.retry,
    setStatus: () => { /* no-op: provider owns status */ },
    retryToken: 0,
  }
}
