// SHELL-4..6: xterm.js full-screen drawer for image shell sessions.
// Opens a PTY-backed shell inside the image chroot via WebSocket.
import * as React from "react"
import { Terminal as TerminalIcon, X, Loader2, AlertTriangle, Copy, Check } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { apiFetch, wsUrl } from "@/lib/api"
import type { BaseImage } from "@/lib/types"

// RISK-1(a): session-scoped acceptance key. Once an operator accepts the
// mutation warning within a browser session, we don't prompt again until
// the tab is closed or refreshed. sessionStorage is intentional — we want
// to re-prompt after a full reload so the warning is not silently skipped.
const SHELL_WARNING_ACCEPTED_KEY = "clustr:shell_mutation_warning_accepted"

function shellWarningAccepted(): boolean {
  try {
    return sessionStorage.getItem(SHELL_WARNING_ACCEPTED_KEY) === "1"
  } catch {
    return false
  }
}

function setShellWarningAccepted() {
  try {
    sessionStorage.setItem(SHELL_WARNING_ACCEPTED_KEY, "1")
  } catch {
    // sessionStorage unavailable (private browsing edge case) — proceed anyway
  }
}

// ShellMutationWarningModal renders the confirmation dialog shown before
// opening a shell session. The modal is skipped if the operator already
// accepted during the current browser session.
interface ShellMutationWarningModalProps {
  imageName: string
  onConfirm: () => void
  onCancel: () => void
}

export function ShellMutationWarningModal({ imageName, onConfirm, onCancel }: ShellMutationWarningModalProps) {
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onCancel() }}>
      <DialogContent className="sm:max-w-lg" data-testid="shell-mutation-warning-modal">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-amber-400">
            <AlertTriangle className="h-5 w-5 shrink-0" />
            Shell session — base image will be modified
          </DialogTitle>
        </DialogHeader>
        <div className="space-y-3 text-sm text-muted-foreground">
          <p>
            Interactive shell sessions on a base image currently write directly into
            the image&apos;s root filesystem. Any changes you make will be reflected in
            all future deployments using <strong className="text-foreground">{imageName}</strong> until
            the image is rebuilt or recaptured.
          </p>
          <p>
            Once you close the shell, the image&apos;s checksum is invalidated to flag
            that mutation occurred. Overlay isolation (read-only base + RW overlay)
            is on the roadmap but not yet implemented.
          </p>
        </div>
        <div className="flex gap-2 pt-2">
          <Button
            variant="outline"
            className="flex-1 border-amber-500/40 text-amber-400 hover:bg-amber-500/10 hover:text-amber-300"
            onClick={onConfirm}
            data-testid="shell-warning-confirm-btn"
          >
            Open shell anyway
          </Button>
          <Button
            variant="ghost"
            className="flex-1"
            onClick={onCancel}
            data-testid="shell-warning-cancel-btn"
          >
            Cancel
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

interface ImageShellProps {
  image: BaseImage
  onClose: () => void
}

// wsMsg mirrors the server's wsMsg struct.
// RISK-1(a): "warning" is the initial frame sent by the server before PTY data.
interface WsMsg {
  type: "data" | "resize" | "ping" | "error" | "warning"
  data?: string
  cols?: number
  rows?: number
}

// ShellDepError mirrors the server's shellDepError struct.
interface ShellDepError {
  code: "shell_dependency_missing"
  missing: string[]
  remediation: string
}

// ImageShell is the public entry point. It shows a mutation-warning modal
// before mounting the terminal. Once the operator accepts (persisted in
// sessionStorage for the lifetime of the tab), the terminal mounts directly.
export function ImageShell({ image, onClose }: ImageShellProps) {
  const [warningAccepted, setWarningAccepted] = React.useState(() => shellWarningAccepted())

  if (!warningAccepted) {
    return (
      <ShellMutationWarningModal
        imageName={image.name}
        onConfirm={() => {
          setShellWarningAccepted()
          setWarningAccepted(true)
        }}
        onCancel={onClose}
      />
    )
  }

  return <ImageShellTerminal image={image} onClose={onClose} />
}

// ImageShellTerminal is the xterm.js PTY component. It is only mounted after
// the operator has accepted the mutation warning.
function ImageShellTerminal({ image, onClose }: ImageShellProps) {
  const containerRef = React.useRef<HTMLDivElement>(null)
  const termRef = React.useRef<import("@xterm/xterm").Terminal | null>(null)
  const fitRef = React.useRef<import("@xterm/addon-fit").FitAddon | null>(null)
  const wsRef = React.useRef<WebSocket | null>(null)
  const sessionIdRef = React.useRef<string | null>(null)
  const [status, setStatus] = React.useState<"connecting" | "connected" | "error" | "closed" | "dep_missing">("connecting")
  const [errorMsg, setErrorMsg] = React.useState("")
  const [depError, setDepError] = React.useState<ShellDepError | null>(null)
  const [copied, setCopied] = React.useState(false)
  // Track dep_missing via a ref so ws.onclose can read the current value
  // without a stale closure capturing the initial "connecting" state.
  const depMissingRef = React.useRef(false)

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
          if (msg.type === "warning") {
            // RISK-1(a): server sends a warning frame before PTY data.
            // The operator already accepted the modal — silently drop this
            // frame; it exists for non-browser API consumers (CLI, scripts).
            return
          }
          if (msg.type === "data" && msg.data) {
            term.write(msg.data)
          } else if (msg.type === "error" && msg.data) {
            // Structured server-side error. Attempt to decode as a known payload.
            try {
              const parsed = JSON.parse(msg.data) as ShellDepError
              if (parsed.code === "shell_dependency_missing") {
                if (!cancelled) {
                  depMissingRef.current = true
                  setDepError(parsed)
                  setStatus("dep_missing")
                }
                return
              }
            } catch { /* fall through to generic display */ }
            // Unknown error payload — show message text in terminal.
            term.writeln(`\r\n\x1b[31m[clustr] ${msg.data}\x1b[0m`)
          }
        } catch { /* ignore parse errors */ }
      }

      ws.onerror = () => {
        if (!cancelled) {
          setStatus("error")
          setErrorMsg("WebSocket connection error — check server logs")
        }
      }

      ws.onclose = (ev) => {
        if (cancelled) return
        // If we already rendered a dep_missing panel, leave it as-is.
        if (depMissingRef.current) return
        // The close reason "shell_dependency_missing" is the follow-up frame
        // after the structured "error" message — already handled above.
        if (ev.reason === "shell_dependency_missing") return
        setStatus("closed")
        term.writeln("\r\n\x1b[2m[session ended]\x1b[0m")
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

  function handleCopy(text: string) {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

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
          : status === "error" || status === "dep_missing" ? "bg-red-500/20 text-red-400"
          : status === "closed" ? "bg-gray-500/20 text-gray-400"
          : "bg-yellow-500/20 text-yellow-400"
        }`}>
          {status === "dep_missing" ? "unavailable" : status}
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
              Opening shell session...
            </div>
          </div>
        )}

        {status === "dep_missing" && depError && (
          <div className="absolute inset-0 flex items-center justify-center z-10 p-6">
            <div className="max-w-lg w-full rounded-lg border border-red-500/30 bg-red-950/30 p-6 space-y-4">
              <div className="flex items-start gap-3">
                <AlertTriangle className="h-5 w-5 text-red-400 mt-0.5 shrink-0" />
                <div>
                  <h2 className="text-base font-semibold text-white">Image shell unavailable</h2>
                  <p className="mt-1 text-sm text-white/70">
                    Your <code className="text-white/90 font-mono">clustr-serverd</code> installation is missing
                    required dependencies:{" "}
                    {depError.missing.map((dep, i) => (
                      <span key={dep}>
                        <code className="text-red-300 font-mono">{dep}</code>
                        {i < depError.missing.length - 1 ? ", " : ""}
                      </span>
                    ))}.
                  </p>
                </div>
              </div>

              <div className="space-y-1.5">
                <p className="text-xs text-white/50 uppercase tracking-wide">Remediation</p>
                <div className="flex items-center gap-2 rounded bg-black/40 border border-white/10 px-3 py-2">
                  <code className="flex-1 text-sm font-mono text-green-300 select-all">
                    sudo dnf install systemd-container
                  </code>
                  <button
                    onClick={() => handleCopy("sudo dnf install systemd-container")}
                    className="shrink-0 text-white/40 hover:text-white/80 transition-colors"
                    aria-label="Copy command"
                  >
                    {copied ? <Check className="h-4 w-4 text-green-400" /> : <Copy className="h-4 w-4" />}
                  </button>
                </div>
                <p className="text-xs text-white/40">RHEL / Rocky Linux / AlmaLinux</p>
              </div>

              <Button
                variant="outline"
                size="sm"
                className="w-full border-white/20 text-white/70 hover:text-white hover:bg-white/10"
                onClick={onClose}
              >
                Close
              </Button>
            </div>
          </div>
        )}

        {status === "error" && (
          <div className="absolute inset-0 flex items-center justify-center z-10">
            <div className="text-center space-y-3 px-6">
              <p className="text-sm text-red-400">{errorMsg}</p>
              <Button variant="outline" size="sm" onClick={onClose}>Close</Button>
            </div>
          </div>
        )}

        {status === "closed" && (
          <div className="absolute bottom-4 left-0 right-0 flex justify-center z-10 pointer-events-none">
            <span className="text-xs text-white/30 font-mono">Shell session ended unexpectedly</span>
          </div>
        )}

        <div
          ref={containerRef}
          className="h-full w-full p-2"
          style={{ display: status === "connecting" || status === "error" || status === "dep_missing" ? "none" : "block" }}
        />
      </div>
    </div>
  )
}
