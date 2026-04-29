import * as React from "react"
import { useNavigate } from "@tanstack/react-router"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useAuth } from "@/contexts/auth"
import { apiFetch } from "@/lib/api"
import { toast } from "@/hooks/use-toast"

export function LoginPage() {
  const { login } = useAuth()
  const navigate = useNavigate()
  const [key, setKey] = React.useState("")
  const [loading, setLoading] = React.useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const trimmed = key.trim()
    if (!trimmed) return
    setLoading(true)
    try {
      await apiFetch("/api/v1/nodes", trimmed, { method: "GET" })
      login(trimmed)
      navigate({ to: "/nodes", search: { q: undefined, status: undefined, sort: undefined, dir: undefined } })
    } catch {
      toast({ variant: "destructive", title: "Invalid API key", description: "Could not authenticate. Check the key and try again." })
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex h-full items-center justify-center bg-background">
      <div className="w-full max-w-sm space-y-6 p-8">
        <div className="space-y-2 text-center">
          <h1 className="text-2xl font-semibold">clustr</h1>
          <p className="text-sm text-muted-foreground">Paste your API key to continue</p>
        </div>
        <form onSubmit={handleSubmit} className="space-y-4">
          <Input
            type="password"
            placeholder="clustr-admin-..."
            value={key}
            onChange={(e) => setKey(e.target.value)}
            autoFocus
            className="font-mono text-xs"
          />
          <Button type="submit" className="w-full" disabled={loading || !key.trim()}>
            {loading ? "Checking..." : "Sign in"}
          </Button>
        </form>
      </div>
    </div>
  )
}
