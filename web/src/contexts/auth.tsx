import * as React from "react"
import { apiFetch, SESSION_EXPIRED_EVENT } from "@/lib/api"

// ─── Types ───────────────────────────────────────────────────────────────────

export interface SessionUser {
  sub: string
  role: string
  username?: string
  expires_at: string
  assigned_groups: string[]
}

export type SessionState =
  | { status: "loading" }
  | { status: "authed"; user: SessionUser }
  | { status: "unauthed" }
  | { status: "setup_required" }

interface SessionContextValue {
  session: SessionState
  /** Call after a successful POST /auth/login to sync session without refetching. */
  setAuthed: (user: SessionUser) => void
  /** Call after POST /auth/logout completes. */
  setUnauthed: () => void
  /** Manually trigger a /me refetch (used after set-password). */
  refresh: () => void
}

// ─── Context ─────────────────────────────────────────────────────────────────

const SessionContext = React.createContext<SessionContextValue | null>(null)

export function SessionProvider({ children }: { children: React.ReactNode }) {
  const [session, setSession] = React.useState<SessionState>({ status: "loading" })
  const [refreshCounter, setRefreshCounter] = React.useState(0)

  // On mount (and refresh): call /auth/status first to detect first-run, then /auth/me.
  React.useEffect(() => {
    let cancelled = false

    async function boot() {
      // Step 1: first-run detection.
      let hasAdmin = true
      try {
        const status = await apiFetch<{ has_admin: boolean }>("/api/v1/auth/status")
        hasAdmin = status.has_admin
      } catch {
        // If /auth/status itself fails, proceed as normal (assume admin exists).
      }

      if (!hasAdmin) {
        if (!cancelled) setSession({ status: "setup_required" })
        return
      }

      // Step 2: check active session.
      try {
        const me = await apiFetch<SessionUser>("/api/v1/auth/me")
        if (!cancelled) setSession({ status: "authed", user: me })
      } catch {
        // 401 is expected when not logged in — setUnauthed via SESSION_EXPIRED_EVENT
        // or directly here.
        if (!cancelled) setSession({ status: "unauthed" })
      }
    }

    boot()
    return () => { cancelled = true }
  }, [refreshCounter])

  // Listen for 401s from any apiFetch call after boot.
  React.useEffect(() => {
    function onExpired() {
      setSession({ status: "unauthed" })
    }
    window.addEventListener(SESSION_EXPIRED_EVENT, onExpired)
    return () => window.removeEventListener(SESSION_EXPIRED_EVENT, onExpired)
  }, [])

  const setAuthed = React.useCallback((user: SessionUser) => {
    setSession({ status: "authed", user })
  }, [])

  const setUnauthed = React.useCallback(() => {
    setSession({ status: "unauthed" })
  }, [])

  const refresh = React.useCallback(() => {
    setRefreshCounter((n) => n + 1)
  }, [])

  return (
    <SessionContext.Provider value={{ session, setAuthed, setUnauthed, refresh }}>
      {children}
    </SessionContext.Provider>
  )
}

export function useSession(): SessionContextValue {
  const ctx = React.useContext(SessionContext)
  if (!ctx) throw new Error("useSession must be used inside SessionProvider")
  return ctx
}

// ─── Compatibility shim ───────────────────────────────────────────────────────
// Legacy useAuth() — kept so nodes.tsx compiles without changes.
// Returns a thin wrapper over the session context.
// Once nodes.tsx is updated to use useSession(), remove this.

interface LegacyAuthContextValue {
  login: (key: string) => void
  logout: () => void
}

const LegacyAuthContext = React.createContext<LegacyAuthContextValue | null>(null)

/** @deprecated Use useSession() instead. */
export function AuthProvider({ children }: { children: React.ReactNode }) {
  // This is now a no-op wrapper — SessionProvider handles everything.
  // We keep it so App.tsx doesn't need immediate changes.
  const loginNoop = React.useCallback((_key: string) => {}, [])
  const logoutNoop = React.useCallback(() => {}, [])
  return (
    <LegacyAuthContext.Provider value={{ login: loginNoop, logout: logoutNoop }}>
      {children}
    </LegacyAuthContext.Provider>
  )
}

/** @deprecated Use useSession() instead. */
export function useAuth(): LegacyAuthContextValue {
  const ctx = React.useContext(LegacyAuthContext)
  if (!ctx) throw new Error("useAuth must be used inside AuthProvider")
  return ctx
}
