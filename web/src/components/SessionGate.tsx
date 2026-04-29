import * as React from "react"
import { useNavigate } from "@tanstack/react-router"
import { useSession } from "@/contexts/auth"

/**
 * SessionGate — wraps protected routes.
 * Checks the session state on every render and redirects as needed:
 *   loading        → render nothing (prevents flash of content)
 *   setup_required → /setup
 *   unauthed       → /login
 *   authed, force_password_change cookie set → /set-password
 *   authed         → render children
 */
export function SessionGate({ children }: { children: React.ReactNode }) {
  const { session } = useSession()
  const navigate = useNavigate()

  React.useEffect(() => {
    if (session.status === "setup_required") {
      navigate({ to: "/setup" })
    } else if (session.status === "unauthed") {
      navigate({ to: "/login", search: { firstrun: undefined } })
    } else if (session.status === "authed") {
      // Check for force-password-change cookie (set by the server on login).
      if (document.cookie.includes("clustr_force_password_change=1")) {
        navigate({ to: "/set-password" })
      }
    }
  }, [session.status, navigate])

  if (session.status === "loading") {
    // Blank during auth check — prevents unauthenticated flash.
    return null
  }

  if (session.status !== "authed") {
    return null
  }

  return <>{children}</>
}
