import { cn } from "@/lib/utils"
import type { NodeState } from "@/lib/types"

const stateConfig: Record<NodeState, { color: string; label: string; shape: string }> = {
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

export function StatusDot({ state, className }: { state: NodeState; className?: string }) {
  const cfg = stateConfig[state]
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-xs", className)}>
      <span className={cn("inline-block h-2 w-2 shrink-0", cfg.color, cfg.shape)} />
      {cfg.label}
    </span>
  )
}
