// NodeStatusStrip — sticky header for the node detail page.
//
// Shows: hostname (bold), four semantic pills (State / Connectivity / Operating Mode / LDAP),
// Edit button, and an info icon that triggers the NodeIdentityPopover.
// Sticks to the top of the main content area (position: sticky, top-0) with backdrop blur.

import * as React from "react"
import { Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import type { NodeConfig } from "@/lib/types"
import { nodeState, operatingModeLabel, NODE_OPERATING_MODES } from "@/lib/types"
import { NodeIdentityPopover } from "@/components/nodes/NodeIdentityPopover"

// ─── Connectivity threshold ───────────────────────────────────────────────────
// A node is considered "disconnected" if last_seen_at is more than 5 minutes ago.
// This mirrors the heartbeat threshold the server uses (5 min idle → stale).
const CONNECTIVITY_STALE_MS = 5 * 60 * 1000

// ─── Pill helpers ─────────────────────────────────────────────────────────────

type PillVariant = "healthy" | "warning" | "error" | "neutral"

function Pill({ label, variant }: { label: string; variant: PillVariant }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-xs font-medium",
        variant === "healthy" && "border-status-healthy/40 bg-status-healthy/10 text-status-healthy",
        variant === "warning" && "border-status-warning/40 bg-status-warning/10 text-status-warning",
        variant === "error"   && "border-status-error/40 bg-status-error/10 text-status-error",
        variant === "neutral" && "border-border bg-secondary text-muted-foreground",
      )}
    >
      <span
        className={cn(
          "h-1.5 w-1.5 rounded-full shrink-0",
          variant === "healthy" && "bg-status-healthy",
          variant === "warning" && "bg-status-warning",
          variant === "error"   && "bg-status-error",
          variant === "neutral" && "bg-muted-foreground/50",
        )}
      />
      {label}
    </span>
  )
}

// ─── State pill ───────────────────────────────────────────────────────────────

function statePillProps(state: ReturnType<typeof nodeState>): { label: string; variant: PillVariant } {
  switch (state) {
    case "deployed_verified":
      return { label: "Verified",         variant: "healthy" }
    case "deployed_preboot":
      return { label: "Pre-boot",         variant: "warning" }
    case "deploying":
      return { label: "Deploying",        variant: "warning" }
    case "reimage_pending":
      return { label: "Reimage pending",  variant: "warning" }
    case "deploy_verify_timeout":
      return { label: "Verify timeout",   variant: "error" }
    case "deployed_ldap_failed":
      return { label: "LDAP failed",      variant: "error" }
    case "failed":
      return { label: "Failed",           variant: "error" }
    case "configured":
      return { label: "Configured",       variant: "neutral" }
    case "registered":
      return { label: "Registered",       variant: "neutral" }
    default:
      return { label: state,              variant: "neutral" }
  }
}

// ─── Connectivity pill ────────────────────────────────────────────────────────

function connectivityPillProps(lastSeenAt: string | undefined): { label: string; variant: PillVariant } {
  if (!lastSeenAt) return { label: "Unknown", variant: "neutral" }
  const age = Date.now() - new Date(lastSeenAt).getTime()
  return age < CONNECTIVITY_STALE_MS
    ? { label: "Connected",    variant: "healthy" }
    : { label: "Disconnected", variant: "error" }
}

// ─── Operating mode pill ──────────────────────────────────────────────────────

function operatingModePillLabel(mode: NodeConfig["operating_mode"]): string {
  if (!mode) return "Block install"
  return NODE_OPERATING_MODES.find((m) => m.value === mode)?.label ?? mode
}

// ─── LDAP pill ────────────────────────────────────────────────────────────────

function ldapPillProps(node: NodeConfig): { label: string; variant: PillVariant } | null {
  if (node.ldap_ready === undefined) return null
  if (node.ldap_ready === true)  return { label: "LDAP OK",      variant: "healthy" }
  if (node.ldap_ready === false) return { label: "LDAP failed",  variant: "error"   }
  return null
}

// ─── NodeStatusStrip ──────────────────────────────────────────────────────────

export interface NodeStatusStripProps {
  node: NodeConfig
  onEdit: () => void
}

export function NodeStatusStrip({ node, onEdit }: NodeStatusStripProps) {
  const state = nodeState(node)
  const statePill = statePillProps(state)
  const connPill  = connectivityPillProps(node.last_seen_at ?? node.deploy_verified_booted_at)
  const modePill  = operatingModePillLabel(node.operating_mode)
  const ldapPill  = ldapPillProps(node)

  return (
    <div
      className={cn(
        "sticky top-0 z-20",
        "flex items-center gap-3 flex-wrap",
        "px-6 py-3",
        "border-b border-border",
        "bg-background/90 backdrop-blur-sm",
      )}
      data-testid="node-status-strip"
    >
      {/* Left: hostname + identity info icon */}
      <div className="flex items-center gap-2 min-w-0 shrink-0">
        <h1 className="text-lg font-semibold font-mono truncate" data-testid="node-strip-hostname">
          {node.hostname || node.id}
        </h1>
        <NodeIdentityPopover node={node} />
      </div>

      {/* Pills */}
      <div className="flex items-center gap-1.5 flex-wrap flex-1">
        <Pill label={statePill.label} variant={statePill.variant} />
        <Pill label={connPill.label}  variant={connPill.variant} />
        <Pill label={modePill}        variant="neutral" />
        {ldapPill && <Pill label={ldapPill.label} variant={ldapPill.variant} />}
      </div>

      {/* Right: Edit button */}
      <Button
        variant="ghost"
        size="sm"
        className="h-7 px-2 shrink-0"
        onClick={onEdit}
        data-testid="node-strip-edit"
      >
        <Pencil className="h-3.5 w-3.5 mr-1" />
        Edit
      </Button>
    </div>
  )
}
