import * as React from "react"

type Theme = "dark" | "light"

interface ThemeContextValue {
  theme: Theme
  toggle: () => void
}

const ThemeContext = React.createContext<ThemeContextValue | null>(null)

function applyTheme(t: Theme) {
  const root = document.documentElement
  root.classList.toggle("light", t === "light")
}

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [theme, setTheme] = React.useState<Theme>(() => {
    const stored = localStorage.getItem("clustr.theme")
    return stored === "light" ? "light" : "dark"
  })

  React.useEffect(() => {
    applyTheme(theme)
  }, [theme])

  const toggle = React.useCallback(() => {
    setTheme((t) => {
      const next = t === "dark" ? "light" : "dark"
      localStorage.setItem("clustr.theme", next)
      return next
    })
  }, [])

  return (
    <ThemeContext.Provider value={{ theme, toggle }}>
      {children}
    </ThemeContext.Provider>
  )
}

export function useTheme(): ThemeContextValue {
  const ctx = React.useContext(ThemeContext)
  if (!ctx) throw new Error("useTheme must be used inside ThemeProvider")
  return ctx
}
