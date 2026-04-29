import * as React from "react"
import { Button } from "@/components/ui/button"
import { Copy, Check } from "lucide-react"

const BOOTSTRAP_CMD = "clustr-serverd bootstrap-admin"

export function SetupPage() {
  const [copied, setCopied] = React.useState(false)

  function copy() {
    navigator.clipboard.writeText(BOOTSTRAP_CMD).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  return (
    <div className="flex h-full items-center justify-center bg-background">
      <div className="w-full max-w-lg space-y-6 p-8">
        <div className="space-y-2 text-center">
          <h1 className="text-2xl font-semibold">Setup required</h1>
          <p className="text-sm text-muted-foreground">
            No admin user found. Run this command on the server host to create one:
          </p>
        </div>

        <div className="flex items-center gap-2 rounded-md border border-border bg-card px-4 py-3">
          <code className="text-sm font-mono flex-1 text-left select-all">
            {BOOTSTRAP_CMD}
          </code>
          <Button variant="ghost" size="icon" className="h-7 w-7 shrink-0" onClick={copy}>
            {copied ? (
              <Check className="h-3.5 w-3.5 text-green-500" />
            ) : (
              <Copy className="h-3.5 w-3.5" />
            )}
          </Button>
        </div>

        <p className="text-xs text-muted-foreground text-center">
          Use <code className="font-mono">--bypass-complexity</code> for a temporary password and{" "}
          <code className="font-mono">--force</code> to overwrite an existing admin account.
          Refresh this page after running the command.
        </p>
      </div>
    </div>
  )
}
