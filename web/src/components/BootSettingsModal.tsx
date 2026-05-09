/**
 * BootSettingsModal.tsx — BOOT-SETTINGS-MODAL (Sprint 34 UI-A)
 *
 * Renders a dialog that lets an operator configure per-node persistent boot settings:
 *   - Boot Order Policy: radio Network | OS
 *   - Netboot Menu Entry: dropdown of available iPXE entries
 *   - Kernel cmdline: free-text input
 *
 * On Save: PATCH /api/v1/nodes/{id}/boot-settings
 *   Body: { boot_order_policy?, netboot_menu_entry?, kernel_cmdline? }
 *
 * Wire shape matches Richard's Bundle A backend:
 *   PATCH /api/v1/nodes/{id}/boot-settings → 200 NodeConfig
 */

import * as React from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Settings2 } from "lucide-react"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"
import { cn } from "@/lib/utils"
import type { NodeConfig } from "@/lib/types"

// ─── Types ─────────────────────────────────────────────────────────────────────

export type BootOrderPolicy = "network" | "os" | ""

export interface BootSettingsBody {
  boot_order_policy?: BootOrderPolicy
  netboot_menu_entry?: string | null
  kernel_cmdline?: string | null
}

interface BootEntriesResponse {
  entries: BootMenuEntry[]
}

export interface BootMenuEntry {
  id: string
  label: string
  description?: string
}

// ─── BootSettingsModal ─────────────────────────────────────────────────────────

export interface BootSettingsModalProps {
  open: boolean
  onClose: () => void
  node: NodeConfig
}

export function BootSettingsModal({ open, onClose, node }: BootSettingsModalProps) {
  const qc = useQueryClient()

  const [bootOrderPolicy, setBootOrderPolicy] = React.useState<BootOrderPolicy>("")
  const [netbootEntry, setNetbootEntry] = React.useState("")
  const [kernelCmdline, setKernelCmdline] = React.useState("")

  React.useEffect(() => {
    if (!open) return
    const ext = node as NodeConfig & {
      boot_order_policy?: BootOrderPolicy
      persistent_ipxe_entry?: string
      persistent_kernel_cmdline?: string
    }
    setBootOrderPolicy(ext.boot_order_policy ?? "")
    setNetbootEntry(ext.persistent_ipxe_entry ?? "")
    setKernelCmdline(ext.persistent_kernel_cmdline ?? "")
  }, [open, node])

  const { data: entriesData } = useQuery<BootEntriesResponse>({
    queryKey: ["boot-entries"],
    queryFn: () => apiFetch<BootEntriesResponse>("/api/v1/boot-entries"),
    staleTime: 60_000,
    enabled: open,
  })
  const bootEntries = entriesData?.entries ?? []

  const mutation = useMutation({
    mutationFn: () => {
      const body: BootSettingsBody = {}
      if (bootOrderPolicy) body.boot_order_policy = bootOrderPolicy
      if (netbootEntry !== "") body.netboot_menu_entry = netbootEntry || null
      if (kernelCmdline !== "") body.kernel_cmdline = kernelCmdline || null
      return apiFetch<NodeConfig>(`/api/v1/nodes/${node.id}/boot-settings`, {
        method: "PATCH",
        body: JSON.stringify(body),
      })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] })
      toast({ title: "Boot settings saved", description: `${node.hostname} updated.` })
      onClose()
    },
    onError: (err) => {
      toast({ variant: "destructive", title: "Failed to save boot settings", description: String(err) })
    },
  })

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    mutation.mutate()
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Settings2 className="h-4 w-4" />
            Boot Settings — {node.hostname}
          </DialogTitle>
          <p className="text-sm text-muted-foreground">
            Persistent boot configuration applied on next PXE boot. Leave fields blank to keep the
            current value.
          </p>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-5 mt-2">
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">Boot Order Policy</legend>
            <p className="text-xs text-muted-foreground">
              Controls the EFI boot order written by <code className="font-mono">efibootmgr</code> after
              deploy.
            </p>
            <div className="flex gap-6">
              {(["", "network", "os"] as const).map((policy) => (
                <label
                  key={policy}
                  className={cn(
                    "flex items-center gap-2 text-sm cursor-pointer",
                    bootOrderPolicy === policy && "text-foreground font-medium",
                  )}
                >
                  <input
                    type="radio"
                    name="boot_order_policy"
                    value={policy}
                    checked={bootOrderPolicy === policy}
                    onChange={() => setBootOrderPolicy(policy)}
                    className="accent-primary"
                    aria-label={policy === "" ? "Inherit (default)" : policy === "network" ? "Network" : "OS disk"}
                  />
                  {policy === "" ? "Inherit" : policy === "network" ? "Network" : "OS disk"}
                </label>
              ))}
            </div>
          </fieldset>

          <div className="space-y-1.5">
            <label htmlFor="boot-entry-select" className="text-sm font-medium">
              Netboot Menu Entry
            </label>
            <p className="text-xs text-muted-foreground">
              iPXE menu entry to use for next PXE boot. Overrides the default per-image selection.
            </p>
            <select
              id="boot-entry-select"
              className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
              value={netbootEntry}
              onChange={(e) => setNetbootEntry(e.target.value)}
            >
              <option value="">Default (no override)</option>
              {bootEntries.map((entry) => (
                <option key={entry.id} value={entry.id}>
                  {entry.label}
                  {entry.description ? ` — ${entry.description}` : ""}
                </option>
              ))}
            </select>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="kernel-cmdline-input" className="text-sm font-medium">
              Kernel cmdline
            </label>
            <p className="text-xs text-muted-foreground">
              Appended verbatim to the kernel command line. Example:{" "}
              <code className="font-mono">console=ttyS0,115200n8</code>
            </p>
            <Input
              id="kernel-cmdline-input"
              className="font-mono text-xs"
              placeholder="(no override)"
              value={kernelCmdline}
              onChange={(e) => setKernelCmdline(e.target.value)}
            />
          </div>

          <div className="flex gap-2 pt-1">
            <Button type="submit" className="flex-1" disabled={mutation.isPending}>
              {mutation.isPending ? "Saving…" : "Save Boot Settings"}
            </Button>
            <Button type="button" variant="ghost" onClick={onClose} disabled={mutation.isPending}>
              Cancel
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
