import * as React from "react"

const STORAGE_KEY = "clustr.apiKey"

interface AuthContextValue {
  apiKey: string | null
  login: (key: string) => void
  logout: () => void
}

const AuthContext = React.createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [apiKey, setApiKey] = React.useState<string | null>(() =>
    localStorage.getItem(STORAGE_KEY)
  )

  const login = React.useCallback((key: string) => {
    localStorage.setItem(STORAGE_KEY, key)
    setApiKey(key)
  }, [])

  const logout = React.useCallback(() => {
    localStorage.removeItem(STORAGE_KEY)
    setApiKey(null)
  }, [])

  return (
    <AuthContext.Provider value={{ apiKey, login, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthContextValue {
  const ctx = React.useContext(AuthContext)
  if (!ctx) throw new Error("useAuth must be used inside AuthProvider")
  return ctx
}
