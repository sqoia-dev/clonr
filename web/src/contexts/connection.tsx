import * as React from "react"

export type ConnectionStatus = "connected" | "reconnecting" | "disconnected"

interface ConnectionContextValue {
  status: ConnectionStatus
  setStatus: (s: ConnectionStatus) => void
}

const ConnectionContext = React.createContext<ConnectionContextValue | null>(null)

export function ConnectionProvider({ children }: { children: React.ReactNode }) {
  const [status, setStatus] = React.useState<ConnectionStatus>("disconnected")

  return (
    <ConnectionContext.Provider value={{ status, setStatus }}>
      {children}
    </ConnectionContext.Provider>
  )
}

export function useConnection(): ConnectionContextValue {
  const ctx = React.useContext(ConnectionContext)
  if (!ctx) throw new Error("useConnection must be used inside ConnectionProvider")
  return ctx
}
