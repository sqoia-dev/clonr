import { RouterProvider } from "@tanstack/react-router"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { TooltipProvider } from "@/components/ui/tooltip"
import { Toaster } from "@/components/ui/toaster"
import { AuthProvider } from "@/contexts/auth"
import { ThemeProvider } from "@/contexts/theme"
import { ConnectionProvider } from "@/contexts/connection"
import { router } from "@/router"

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      staleTime: 10000,
    },
  },
})

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <ThemeProvider>
          <ConnectionProvider>
            <TooltipProvider>
              <RouterProvider router={router} />
              <Toaster />
            </TooltipProvider>
          </ConnectionProvider>
        </ThemeProvider>
      </AuthProvider>
    </QueryClientProvider>
  )
}
