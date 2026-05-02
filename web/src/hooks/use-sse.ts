import * as React from "react"
import { sseUrl } from "@/lib/api"
import { sseReconnectDelay } from "@/lib/sse-backoff"

export type SSEStatus = "connected" | "reconnecting" | "disconnected"

interface UseSSEOptions<T> {
  path: string
  enabled?: boolean
  onMessage: (data: T) => void
  onStatusChange?: (status: SSEStatus) => void
  /** Increment this value to force a reconnect (POL-6). */
  retryToken?: number
}

/**
 * Shared SSE hook. Opens an EventSource to `path` with credentials (cookie auth),
 * calls `onMessage` on each parsed JSON event, and auto-reconnects with a 5-second
 * delay on error. Cleans up on unmount or when `enabled` becomes false.
 * When `retryToken` changes, the existing connection is torn down and a new one
 * is opened immediately (bypassing the 5-second backoff).
 */
export function useSSE<T>({
  path,
  enabled = true,
  onMessage,
  onStatusChange,
  retryToken = 0,
}: UseSSEOptions<T>) {
  const onMessageRef = React.useRef(onMessage)
  onMessageRef.current = onMessage
  const onStatusRef = React.useRef(onStatusChange)
  onStatusRef.current = onStatusChange

  React.useEffect(() => {
    if (!enabled) return

    let es: EventSource | null = null
    let reconnectTimer: ReturnType<typeof setTimeout>
    let destroyed = false
    let attempt = 0

    function connect() {
      if (destroyed) return
      const url = sseUrl(path)
      es = new EventSource(url, { withCredentials: true })

      es.onopen = () => {
        attempt = 0
        onStatusRef.current?.("connected")
      }

      es.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data) as T
          onMessageRef.current(data)
        } catch {
          // malformed event — ignore
        }
      }

      es.onerror = () => {
        if (destroyed) return
        attempt++
        onStatusRef.current?.("reconnecting")
        es?.close()
        es = null
        reconnectTimer = setTimeout(connect, sseReconnectDelay(attempt))
      }
    }

    connect()

    return () => {
      destroyed = true
      clearTimeout(reconnectTimer)
      es?.close()
      // Do NOT fire "disconnected" on cleanup: this runs on every effect re-run
      // (retryToken change) and on unmount (navigation). Propagating "disconnected"
      // here starts the paused-banner timer on page navigation, causing false
      // "Live updates paused" warnings. Genuine failures are signalled via onerror.
    }
    // retryToken in deps: any change forces teardown + immediate reconnect.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, enabled, retryToken])
}
