import { cn } from "@/lib/utils"
import type { NodeState } from "@/lib/types"

const nodeStateConfig: Record<NodeState, { color: string; label: string; shape: string }> = {
  registered: { color: "bg-status-neutral", label: "Registered", shape: "rounded-full" },
  configured: { color: "bg-status-warning", label: "Configured", shape: "rounded-full" },
  deploying: { color: "bg-primary animate-pulse", label: "Deploying", shape: "rounded-full" },
  deployed: { color: "bg-status-healthy", label: "Deployed", shape: "rounded-full" },
  reimage_pending: { color: "bg-status-warning animate-pulse", label: "Reimage Pending", shape: "rounded-sm" },
  failed: { color: "bg-status-error", label: "Failed", shape: "rounded-sm" },
  deployed_preboot: { color: "bg-primary animate-pulse", label: "Pre-boot", shape: "rounded-full" },
  deployed_verified: { color: "bg-status-healthy", label: "Verified", shape: "rounded-full" },
  deploy_verify_timeout: { color: "bg-status-error", label: "Verify Timeout", shape: "rounded-sm" },
}

export type GenericState = "healthy" | "warning" | "error" | "neutral" | "pending"

const genericStateConfig: Record<GenericState, { color: string; shape: string }> = {
  healthy: { color: "bg-status-healthy", shape: "rounded-full" },
  warning: { color: "bg-status-warning", shape: "rounded-full" },
  error: { color: "bg-status-error", shape: "rounded-sm" },
  neutral: { color: "bg-status-neutral", shape: "rounded-full" },
  pending: { color: "bg-primary animate-pulse", shape: "rounded-full" },
}

type Props =
  | { state: NodeState; label?: string; className?: string }
  | { state: GenericState; label: string; className?: string }

export function StatusDot({ state, label, className }: Props) {
  // Check if it's a NodeState key.
  if (state in nodeStateConfig) {
    const cfg = nodeStateConfig[state as NodeState]
    return (
      <span className={cn("inline-flex items-center gap-1.5 text-xs", className)}>
        <span className={cn("inline-block h-2 w-2 shrink-0", cfg.color, cfg.shape)} />
        {label ?? cfg.label}
      </span>
    )
  }
  // Generic state.
  const cfg = genericStateConfig[state as GenericState]
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-xs", className)}>
      <span className={cn("inline-block h-2 w-2 shrink-0", cfg.color, cfg.shape)} />
      {label ?? state}
    </span>
  )
}
