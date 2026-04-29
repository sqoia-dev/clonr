import * as React from "react"
import { useNavigate, useSearch } from "@tanstack/react-router"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useSession, type SessionUser } from "@/contexts/auth"
import { apiFetch } from "@/lib/api"

interface LoginResponse {
  ok: boolean
  role: string
  force_password_change?: boolean
}

export function LoginPage() {
  const { setAuthed, refresh } = useSession()
  const navigate = useNavigate()
  // DEF-5: read ?firstrun=1 from URL — shows default-creds hint on first-run path only.
  const search = useSearch({ from: "/login" })
  const isFirstRun = search.firstrun === "1"

  const [username, setUsername] = React.useState("")
  const [password, setPassword] = React.useState("")
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!username.trim() || !password) return
    setError(null)
    setLoading(true)
    try {
      const res = await apiFetch<LoginResponse>("/api/v1/auth/login", {
        method: "POST",
        body: JSON.stringify({ username: username.trim(), password }),
      })

      if (res.force_password_change) {
        // /auth/me will reflect the force-change cookie; refresh triggers that.
        refresh()
        return
      }

      // Fetch /me to populate full session user (role, sub, username, groups).
      try {
        const me = await apiFetch<SessionUser>("/api/v1/auth/me")
        setAuthed(me)
      } catch {
        // me failed — refresh will re-run boot sequence.
        refresh()
        return
      }

      // Drop the ?firstrun param on successful login — default creds no longer needed.
      navigate({
        to: "/nodes",
        search: { q: undefined, status: undefined, sort: undefined, dir: undefined, openNode: undefined, reimage: undefined, addNode: undefined, tag: undefined },
      })
    } catch (err) {
      // Try to extract the server's error message verbatim.
      let msg = "Login failed. Check your credentials and try again."
      if (err instanceof Error) {
        // apiFetch throws "401: <json body>"
        const raw = err.message.replace(/^\d+:\s*/, "")
        try {
          const parsed = JSON.parse(raw) as { error?: string }
          if (parsed.error) msg = parsed.error
        } catch {
          // body wasn't JSON — use the raw text if it's non-trivial
          if (raw.length > 0 && raw.length < 200) msg = raw
        }
      }
      setError(msg)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex h-full items-center justify-center bg-background">
      <div className="w-full max-w-sm space-y-6 p-8">
        <div className="space-y-2 text-center">
          <h1 className="text-2xl font-semibold">clustr</h1>
          <p className="text-sm text-muted-foreground">Sign in to continue</p>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Input
              id="username"
              type="text"
              placeholder="Username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoFocus
              autoComplete="username"
              disabled={loading}
            />
          </div>

          <div className="space-y-2">
            <Input
              id="password"
              type="password"
              placeholder="Password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              disabled={loading}
            />
          </div>

          {error && (
            <p className="text-sm text-destructive" role="alert">
              {error}
            </p>
          )}

          <Button
            type="submit"
            className="w-full"
            disabled={loading || !username.trim() || !password}
          >
            {loading ? "Signing in..." : "Sign in"}
          </Button>
        </form>

        {/* DEF-5: Show default-creds hint only on the ?firstrun=1 path.
            This disappears after a successful login (navigate drops the param). */}
        {isFirstRun && (
          <p className="text-xs text-muted-foreground text-center">
            Default credentials:{" "}
            <code className="font-mono">clustr</code>{" "}
            /{" "}
            <code className="font-mono">clustr</code>
            {" "}— you'll be prompted to set a real password.
          </p>
        )}
      </div>
    </div>
  )
}
