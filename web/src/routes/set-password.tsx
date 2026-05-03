import * as React from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useSession } from "@/contexts/auth"
import { apiFetch } from "@/lib/api"

interface SetPasswordResponse {
  ok: boolean
  sub: string
  role: string
}

export function SetPasswordPage() {
  const { refresh } = useSession()
  const [newPassword, setNewPassword] = React.useState("")
  const [confirmPassword, setConfirmPassword] = React.useState("")
  const [currentPassword, setCurrentPassword] = React.useState("")
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!currentPassword || !newPassword || !confirmPassword) return
    if (newPassword !== confirmPassword) {
      setError("Passwords do not match")
      return
    }
    setError(null)
    setLoading(true)
    try {
      await apiFetch<SetPasswordResponse>("/api/v1/auth/set-password", {
        method: "POST",
        body: JSON.stringify({
          current_password: currentPassword,
          new_password: newPassword,
        }),
      })
      // Success: refresh the session — /me will now return without force_password_change.
      refresh()
    } catch (err) {
      let msg = "Password change failed."
      if (err instanceof Error) {
        const raw = err.message.replace(/^\d+:\s*/, "")
        try {
          const parsed = JSON.parse(raw) as { error?: string }
          if (parsed.error) msg = parsed.error
        } catch {
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
          <h1 className="text-2xl font-semibold">Change your password</h1>
          <p className="text-sm text-muted-foreground">
            Enter your current password and choose a new one.
          </p>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <Input
            type="password"
            placeholder="Current password"
            value={currentPassword}
            onChange={(e) => setCurrentPassword(e.target.value)}
            autoFocus
            autoComplete="current-password"
            disabled={loading}
          />

          <Input
            type="password"
            placeholder="New password"
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            autoComplete="new-password"
            disabled={loading}
          />

          <Input
            type="password"
            placeholder="Confirm new password"
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            autoComplete="new-password"
            disabled={loading}
          />

          {error && (
            <p className="text-sm text-destructive" role="alert">
              {error}
            </p>
          )}

          <Button
            type="submit"
            className="w-full"
            disabled={loading || !currentPassword || !newPassword || !confirmPassword}
          >
            {loading ? "Saving..." : "Set password"}
          </Button>
        </form>
      </div>
    </div>
  )
}
