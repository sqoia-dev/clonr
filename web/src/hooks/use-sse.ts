import * as React from "react"
import { sseUrl } from "@/lib/api"

export type SSEStatus = "connected" | "reconnecting" | "disconnected"

interface UseSSEOptions<T> {
  path: string
  enabled?: boolean
  onMessage: (data: T) => void
  onStatusChange?: (status: SSEStatus) => void
}

/**
 * Shared SSE hook. Opens an EventSource to `path` with credentials (cookie auth),
 * calls `onMessage` on each parsed JSON event, and auto-reconnects with a 5-second
 * delay on error. Cleans up on unmount or when `enabled` becomes false.
 */
export function useSSE<T>({
  path,
  enabled = true,
  onMessage,
  onStatusChange,
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

    function connect() {
      if (destroyed) return
      const url = sseUrl(path)
      es = new EventSource(url, { withCredentials: true })

      es.onopen = () => {
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
        onStatusRef.current?.("reconnecting")
        es?.close()
        es = null
        reconnectTimer = setTimeout(connect, 5000)
      }
    }

    connect()

    return () => {
      destroyed = true
      clearTimeout(reconnectTimer)
      es?.close()
      onStatusRef.current?.("disconnected")
    }
  }, [path, enabled])
}
