// SHELL-4..6: xterm.js full-screen drawer for image shell sessions.
// Opens a PTY-backed shell inside the image chroot via WebSocket.
import * as React from "react"
import { Terminal as TerminalIcon, X, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { apiFetch, wsUrl } from "@/lib/api"
import type { BaseImage } from "@/lib/types"

interface ImageShellProps {
  image: BaseImage
  onClose: () => void
}

// wsMsg mirrors the server's wsMsg struct.
interface WsMsg {
  type: "data" | "resize" | "ping"
  data?: string
  cols?: number
  rows?: number
}

export function ImageShell({ image, onClose }: ImageShellProps) {
  const containerRef = React.useRef<HTMLDivElement>(null)
  const termRef = React.useRef<import("@xterm/xterm").Terminal | null>(null)
  const fitRef = React.useRef<import("@xterm/addon-fit").FitAddon | null>(null)
  const wsRef = React.useRef<WebSocket | null>(null)
  const sessionIdRef = React.useRef<string | null>(null)
  const [status, setStatus] = React.useState<"connecting" | "connected" | "error" | "closed">("connecting")
  const [errorMsg, setErrorMsg] = React.useState("")

  React.useEffect(() => {
    let cancelled = false

    async function init() {
      // 1. Create shell session via REST.
      let sessionId: string
      try {
        const res = await apiFetch<{ session_id: string }>(`/api/v1/images/${image.id}/shell-session`, {
          method: "POST",
          body: JSON.stringify({}),
        })
        sessionId = res.session_id
        sessionIdRef.current = sessionId
      } catch (err) {
        if (!cancelled) {
          setStatus("error")
          setErrorMsg(`Failed to create shell session: ${err}`)
        }
        return
      }

      if (cancelled) return

      // 2. Initialize xterm.js dynamically to avoid SSR issues.
      const [{ Terminal }, { FitAddon }] = await Promise.all([
        import("@xterm/xterm"),
        import("@xterm/addon-fit"),
      ])
      await import("@xterm/xterm/css/xterm.css")

      if (cancelled) return
      if (!containerRef.current) return

      const term = new Terminal({
        theme: {
          background: "#0a0a0a",
          foreground: "#e4e4e4",
          cursor: "#22d3ee",
        },
        fontFamily: "'JetBrains Mono Variable', 'JetBrains Mono', monospace",
        fontSize: 13,
        scrollback: 2000,
        cursorBlink: true,
      })
      const fit = new FitAddon()
      term.loadAddon(fit)
      term.open(containerRef.current)
      fit.fit()
      termRef.current = term
      fitRef.current = fit

      // 3. Open WebSocket to the shell session.
      const wsEndpoint = wsUrl(`/api/v1/images/${image.id}/shell-session/${sessionId}/ws`)
      const ws = new WebSocket(wsEndpoint)
      ws.binaryType = "arraybuffer"
      wsRef.current = ws

      ws.onopen = () => {
        if (cancelled) { ws.close(); return }
        setStatus("connected")
        // Send initial resize.
        const dims = fit.proposeDimensions()
        if (dims) {
          ws.send(JSON.stringify({ type: "resize", cols: dims.cols, rows: dims.rows }))
        }
      }

      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data as string) as WsMsg
          if (msg.type === "data" && msg.data) {
            term.write(msg.data)
          }
        } catch { /* ignore parse errors */ }
      }

      ws.onerror = () => {
        if (!cancelled) {
          setStatus("error")
          setErrorMsg("WebSocket error — check server logs")
        }
      }

      ws.onclose = () => {
        if (!cancelled) setStatus("closed")
        term.writeln("\r\n\x1b[2m[session closed]\x1b[0m")
      }

      // 4. Forward keystrokes to WebSocket.
      term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: "data", data }))
        }
      })

      // 5. Handle resize.
      const resizeObserver = new ResizeObserver(() => {
        fit.fit()
        if (ws.readyState === WebSocket.OPEN) {
          const dims = fit.proposeDimensions()
          if (dims) {
            ws.send(JSON.stringify({ type: "resize", cols: dims.cols, rows: dims.rows }))
          }
        }
      })
      if (containerRef.current) {
        resizeObserver.observe(containerRef.current)
      }

      // Cleanup function stored for the effect teardown below.
      ;(init as unknown as { _cleanup?: () => void })._cleanup = () => {
        resizeObserver.disconnect()
        ws.close()
        term.dispose()
      }
    }

    const p = init()

    return () => {
      cancelled = true
      // Close session on unmount.
      if (sessionIdRef.current) {
        apiFetch(`/api/v1/images/${image.id}/shell-session/${sessionIdRef.current}`, {
          method: "DELETE",
        }).catch(() => {/* best effort */})
      }
      wsRef.current?.close()
      termRef.current?.dispose()
      void p
    }
  }, [image.id]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-[#0a0a0a]" role="dialog" aria-label={`Shell: ${image.name}`}>
      {/* Header bar */}
      <div className="flex items-center gap-3 px-4 py-2 border-b border-white/10 bg-[#111]">
        <TerminalIcon className="h-4 w-4 text-cyan-400" />
        <span className="text-sm font-mono text-white">
          {image.name} {image.version}
        </span>
        <span className={`ml-2 text-xs px-1.5 py-0.5 rounded font-mono ${
          status === "connected" ? "bg-green-500/20 text-green-400"
          : status === "error" ? "bg-red-500/20 text-red-400"
          : status === "closed" ? "bg-gray-500/20 text-gray-400"
          : "bg-yellow-500/20 text-yellow-400"
        }`}>
          {status}
        </span>
        <div className="ml-auto">
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 text-white/60 hover:text-white hover:bg-white/10"
            onClick={onClose}
            aria-label="Close shell"
          >
            <X className="h-4 w-4" />
          </Button>
        </div>
      </div>

      {/* Terminal area */}
      <div className="flex-1 relative overflow-hidden">
        {status === "connecting" && (
          <div className="absolute inset-0 flex items-center justify-center z-10">
            <div className="flex items-center gap-2 text-sm text-white/60">
              <Loader2 className="h-4 w-4 animate-spin" />
              Opening shell session…
            </div>
          </div>
        )}
        {status === "error" && (
          <div className="absolute inset-0 flex items-center justify-center z-10">
            <div className="text-center space-y-2">
              <p className="text-sm text-red-400">{errorMsg}</p>
              <Button variant="outline" size="sm" onClick={onClose}>Close</Button>
            </div>
          </div>
        )}
        <div ref={containerRef} className="h-full w-full p-2" style={{ display: status === "connecting" || status === "error" ? "none" : "block" }} />
      </div>
    </div>
  )
}
