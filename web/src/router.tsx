import { createRouter, createRoute, createRootRoute, redirect, Outlet } from "@tanstack/react-router"
import { AppShell } from "@/components/AppShell"
import { LoginPage } from "@/routes/login"
import { NodesPage } from "@/routes/nodes"
import { StubPage } from "@/routes/stub"

function getApiKey() {
  return localStorage.getItem("clustr.apiKey")
}

const rootRoute = createRootRoute({
  component: Outlet,
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: LoginPage,
  beforeLoad: () => {
    if (getApiKey()) {
      throw redirect({ to: "/nodes", search: { q: undefined, status: undefined, sort: undefined, dir: undefined } })
    }
  },
})

const protectedLayout = createRoute({
  getParentRoute: () => rootRoute,
  id: "protected",
  component: () => (
    <AppShell>
      <Outlet />
    </AppShell>
  ),
  beforeLoad: () => {
    if (!getApiKey()) {
      throw redirect({ to: "/login" })
    }
  },
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
  component: () => <StubPage title="Images" />,
})

const activityRoute = createRoute({
  getParentRoute: () => protectedLayout,
  path: "/activity",
  component: () => <StubPage title="Activity" />,
})

const settingsRoute = createRoute({
  getParentRoute: () => protectedLayout,
  path: "/settings",
  component: () => <StubPage title="Settings" />,
})

const routeTree = rootRoute.addChildren([
  loginRoute,
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
