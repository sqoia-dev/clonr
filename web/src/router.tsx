import { createRouter, createRoute, createRootRoute, redirect, Outlet } from "@tanstack/react-router"
import { AppShell } from "@/components/AppShell"
import { LoginPage } from "@/routes/login"
import { SetupPage } from "@/routes/setup"
import { SetPasswordPage } from "@/routes/set-password"
import { NodesPage } from "@/routes/nodes"
import { ImagesPage } from "@/routes/images"
import { ActivityPage } from "@/routes/activity"
import { SettingsPage } from "@/routes/settings"
import { SessionGate } from "@/components/SessionGate"

const rootRoute = createRootRoute({
  component: Outlet,
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: LoginPage,
})

const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/setup",
  component: SetupPage,
})

const setPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/set-password",
  component: SetPasswordPage,
})

// The protected layout wraps all authenticated routes in SessionGate, which
// redirects to /login when unauthed, /setup when setup_required, and
// /set-password when force_password_change cookie is present.
const protectedLayout = createRoute({
  getParentRoute: () => rootRoute,
  id: "protected",
  component: () => (
    <SessionGate>
      <AppShell>
        <Outlet />
      </AppShell>
    </SessionGate>
  ),
})

const indexRoute = createRoute({
  getParentRoute: () => protectedLayout,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/nodes", search: { q: undefined, status: undefined, sort: undefined, dir: undefined } })
  },
})

const nodesRoute = createRoute({
  getParentRoute: () => protectedLayout,
  path: "/nodes",
  component: NodesPage,
  validateSearch: (search: Record<string, unknown>) => ({
    q: typeof search.q === "string" ? search.q : undefined,
    status: typeof search.status === "string" ? search.status : undefined,
    sort: typeof search.sort === "string" ? search.sort : undefined,
    dir:
      search.dir === "asc" || search.dir === "desc"
        ? (search.dir as "asc" | "desc")
        : undefined,
  }),
})

const imagesRoute = createRoute({
  getParentRoute: () => protectedLayout,
  path: "/images",
  component: ImagesPage,
  validateSearch: (search: Record<string, unknown>) => ({
    q: typeof search.q === "string" ? search.q : undefined,
    tab: typeof search.tab === "string" ? search.tab : undefined,
    sort: typeof search.sort === "string" ? search.sort : undefined,
    dir: search.dir === "asc" || search.dir === "desc" ? (search.dir as "asc" | "desc") : undefined,
  }),
})

const activityRoute = createRoute({
  getParentRoute: () => protectedLayout,
  path: "/activity",
  component: ActivityPage,
  validateSearch: (search: Record<string, unknown>) => ({
    q: typeof search.q === "string" ? search.q : undefined,
    kind: typeof search.kind === "string" ? search.kind : undefined,
  }),
})

const settingsRoute = createRoute({
  getParentRoute: () => protectedLayout,
  path: "/settings",
  component: SettingsPage,
})

const routeTree = rootRoute.addChildren([
  loginRoute,
  setupRoute,
  setPasswordRoute,
  protectedLayout.addChildren([
    indexRoute,
    nodesRoute,
    imagesRoute,
    activityRoute,
    settingsRoute,
  ]),
])

export const router = createRouter({
  routeTree,
  defaultPreload: "intent",
})

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
}
