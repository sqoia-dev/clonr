// NodeIdentityPopover — secondary identity fields behind an "i" icon.
//
// Triggered by clicking the info icon next to the hostname in NodeStatusStrip.
// Shows a compact 5-row table: ID (UUID) / MAC / FQDN / Firmware / Provider
// with a "Copy ID" button next to the UUID row.
// Uses a click-outside-to-close pattern consistent with SystemAlertsPopover.

import * as React from "react"
import { Info, Copy, Check } from "lucide-react"
import { cn } from "@/lib/utils"
import type { NodeConfig } from "@/lib/types"
import { NODE_PROVIDERS } from "@/lib/types"

export interface NodeIdentityPopoverProps {
  node: NodeConfig
}

export function NodeIdentityPopover({ node }: NodeIdentityPopoverProps) {
  const [open, setOpen] = React.useState(false)
  const [copied, setCopied] = React.useState(false)
  const ref = React.useRef<HTMLDivElement>(null)

  // Close on outside click
  React.useEffect(() => {
    if (!open) return
    function onDown(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener("mousedown", onDown)
    return () => document.removeEventListener("mousedown", onDown)
  }, [open])

  // Close on Escape
  React.useEffect(() => {
    if (!open) return
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") setOpen(false)
    }
    document.addEventListener("keydown", onKey)
    return () => document.removeEventListener("keydown", onKey)
  }, [open])

  function handleCopyId() {
    navigator.clipboard.writeText(node.id).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }

  const providerLabel = NODE_PROVIDERS.find((p) => p.value === (node.provider ?? ""))?.label ?? "—"

  return (
    <div className="relative" ref={ref} data-testid="node-identity-popover">
      <button
        className={cn(
          "inline-flex items-center justify-center h-6 w-6 rounded transition-colors",
          open
            ? "bg-secondary text-foreground"
            : "text-muted-foreground hover:bg-secondary/60 hover:text-foreground",
        )}
        onClick={() => setOpen((v) => !v)}
        aria-label="Node identity details"
        aria-expanded={open}
        data-testid="node-identity-icon"
      >
        <Info className="h-3.5 w-3.5" />
      </button>

      {open && (
        <div
          className={cn(
            "absolute left-0 top-full mt-1.5 z-50",
            "w-80 rounded-md border border-border bg-popover shadow-lg",
            "p-3",
          )}
          data-testid="node-identity-panel"
          role="dialog"
          aria-label="Node identity"
        >
          <p className="text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2">
            Identity
          </p>

          <table className="w-full text-xs">
            <tbody className="divide-y divide-border">
              {/* UUID row — with copy button */}
              <tr>
                <td className="py-1.5 pr-3 text-muted-foreground whitespace-nowrap align-top">ID</td>
                <td className="py-1.5 w-full">
                  <div className="flex items-center justify-between gap-2 min-w-0">
                    <span className="font-mono truncate text-foreground" title={node.id}>
                      {node.id}
                    </span>
                    <button
                      className="shrink-0 inline-flex items-center gap-0.5 rounded px-1 py-0.5 text-[10px] text-muted-foreground hover:bg-secondary transition-colors"
                      onClick={handleCopyId}
                      aria-label="Copy node UUID"
                      data-testid="copy-node-id"
                    >
                      {copied ? (
                        <><Check className="h-2.5 w-2.5 text-status-healthy" /> Copied</>
                      ) : (
                        <><Copy className="h-2.5 w-2.5" /> Copy</>
                      )}
                    </button>
                  </div>
                </td>
              </tr>

              <tr>
                <td className="py-1.5 pr-3 text-muted-foreground whitespace-nowrap">MAC</td>
                <td className="py-1.5 font-mono text-foreground">{node.primary_mac || "—"}</td>
              </tr>

              <tr>
                <td className="py-1.5 pr-3 text-muted-foreground whitespace-nowrap">FQDN</td>
                <td className="py-1.5 font-mono text-foreground">{node.fqdn || "—"}</td>
              </tr>

              <tr>
                <td className="py-1.5 pr-3 text-muted-foreground whitespace-nowrap">Firmware</td>
                <td className="py-1.5 text-foreground">{node.detected_firmware || "—"}</td>
              </tr>

              <tr>
                <td className="py-1.5 pr-3 text-muted-foreground whitespace-nowrap">Provider</td>
                <td className="py-1.5 text-foreground">{providerLabel}</td>
              </tr>
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
