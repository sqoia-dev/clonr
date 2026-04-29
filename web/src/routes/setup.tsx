import * as React from "react"
import { useNavigate } from "@tanstack/react-router"
import { Button } from "@/components/ui/button"
import { Copy, Check, RefreshCw } from "lucide-react"
import { apiFetch } from "@/lib/api"

const BOOTSTRAP_CMD = "clustr-serverd bootstrap-admin"

export function SetupPage() {
  const navigate = useNavigate()
  const [copied, setCopied] = React.useState(false)
  const [checking, setChecking] = React.useState(false)
  const [markedDone, setMarkedDone] = React.useState(false)

  function copy() {
    navigator.clipboard.writeText(BOOTSTRAP_CMD).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  // Poll /auth/status after the operator clicks "I've run bootstrap-admin".
  // On has_admin: true → redirect to /login?firstrun=1.
  async function handleDone() {
    setMarkedDone(true)
    setChecking(true)
    try {
      const status = await apiFetch<{ has_admin: boolean }>("/api/v1/auth/status")
      if (status.has_admin) {
        navigate({ to: "/login", search: { firstrun: "1" } })
        return
      }
      // Admin still not found — give the operator feedback.
      setChecking(false)
      setMarkedDone(false)
    } catch {
      setChecking(false)
      setMarkedDone(false)
    }
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
          Default credentials:{" "}
          <code className="font-mono">clustr</code>{" "}
          /{" "}
          <code className="font-mono">clustr</code>
          {" "}— change the password via Settings whenever you want.
        </p>

        <p className="text-xs text-muted-foreground text-center">
          Use <code className="font-mono">--force</code> to overwrite an existing admin account.
        </p>

        <Button
          className="w-full"
          onClick={handleDone}
          disabled={checking || markedDone}
        >
          {checking ? (
            <>
              <RefreshCw className="mr-2 h-4 w-4 animate-spin" />
              Checking…
            </>
          ) : (
            "I've run bootstrap-admin"
          )}
        </Button>
      </div>
    </div>
  )
}
