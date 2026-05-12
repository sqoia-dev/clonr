// TypedConfirmDialog — reusable typed-confirmation modal.
//
// Operator must type a specific token (e.g. "REIMAGE", hostname, or an image name)
// before the confirm button enables. Used for destructive operations throughout the UI.
//
// Usage:
//   <TypedConfirmDialog
//     open={open}
//     onClose={() => setOpen(false)}
//     onConfirm={() => doDestructiveAction()}
//     title="Reimage node"
//     description={<p>This will wipe and reinstall the OS.</p>}
//     confirmToken="REIMAGE"
//     confirmPrompt='Type "REIMAGE" to confirm:'
//     confirmButtonLabel="Confirm reimage"
//     confirmButtonVariant="destructive"
//     isPending={mutation.isPending}
//     error={error}
//   />

import * as React from "react"
import { AlertTriangle } from "lucide-react"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"

export interface TypedConfirmDialogProps {
  open: boolean
  onClose: () => void
  onConfirm: () => void
  title: string
  /** Optional descriptive content rendered below the title. */
  description?: React.ReactNode
  /** The exact string the operator must type to unlock the confirm button. */
  confirmToken: string
  /** Case-sensitive comparison? Default: false (case-insensitive). */
  caseSensitive?: boolean
  /** Label shown above the input. */
  confirmPrompt?: string
  /** Label for the confirm button. Default: "Confirm". */
  confirmButtonLabel?: string
  /** Visual variant for the confirm button. Default: "destructive". */
  confirmButtonVariant?: "destructive" | "default"
  /** Shows a spinner + disables input when true. */
  isPending?: boolean
  /** Optional error message displayed below the input. */
  error?: string | null
  /** Optional extra content rendered inside the form (e.g. image selector). */
  extraContent?: React.ReactNode
}

export function TypedConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  description,
  confirmToken,
  caseSensitive = false,
  confirmPrompt,
  confirmButtonLabel = "Confirm",
  confirmButtonVariant = "destructive",
  isPending = false,
  error,
  extraContent,
}: TypedConfirmDialogProps) {
  const [typed, setTyped] = React.useState("")

  const matches = caseSensitive
    ? typed === confirmToken
    : typed.toLowerCase() === confirmToken.toLowerCase()

  const canConfirm = matches && !isPending

  function handleConfirm() {
    if (!canConfirm) return
    onConfirm()
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Enter" && canConfirm) handleConfirm()
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v && !isPending) { setTyped(""); onClose() } }}>
      <DialogContent className="sm:max-w-md" data-testid="typed-confirm-dialog">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-destructive">
            <AlertTriangle className="h-4 w-4 shrink-0" />
            {title}
          </DialogTitle>
          {description && (
            <DialogDescription asChild>
              <div className="text-sm text-muted-foreground">{description}</div>
            </DialogDescription>
          )}
        </DialogHeader>

        <div className="space-y-3 pt-1">
          {extraContent}

          <div className="space-y-1.5">
            <p className="text-xs text-muted-foreground">
              {confirmPrompt ?? (
                <>
                  Type{" "}
                  <code className="font-mono font-semibold text-foreground">{confirmToken}</code>{" "}
                  to confirm:
                </>
              )}
            </p>
            <Input
              className={cn("font-mono text-xs", error && "border-destructive")}
              placeholder={confirmToken}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={handleKeyDown}
              disabled={isPending}
              data-testid="typed-confirm-input"
              autoFocus
            />
          </div>

          {error && (
            <p className="text-xs text-destructive" data-testid="typed-confirm-error">{error}</p>
          )}

          <div className="flex gap-2 pt-1">
            <Button
              variant={confirmButtonVariant}
              size="sm"
              className="flex-1"
              disabled={!canConfirm}
              onClick={handleConfirm}
              data-testid="typed-confirm-submit"
            >
              {isPending ? `${confirmButtonLabel}…` : confirmButtonLabel}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => { setTyped(""); onClose() }}
              disabled={isPending}
              data-testid="typed-confirm-cancel"
            >
              Cancel
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
