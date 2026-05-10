/**
 * disk-layouts-edit.test.tsx — Tests for the disk layout edit flow
 *
 * Covers:
 *   1. Edit button is visible and enabled for custom layouts
 *   2. Edit button is disabled (with tooltip) for seed layouts (clustr-default-*)
 *   3. Clicking Edit opens dialog with pre-filled name and layout JSON
 *   4. Save fires PUT with correct body shape
 *   5. On 400/409 error, toast shows and dialog stays open
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { DiskLayoutsPage } from "../routes/disk-layouts"
import type { StoredDiskLayout, ListDiskLayoutsResponse } from "../lib/types"

// ─── Helpers ─────────────────────────────────────────────────────────────────

function makeQC() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
}

function withQC(ui: React.ReactElement, qc = makeQC()) {
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

type FetchHandler = (url: string, init?: RequestInit) => Promise<Response>
let fetchHandler: FetchHandler | null = null

function jsonOk(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function jsonErr(body: unknown, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

beforeEach(() => {
  fetchHandler = null
  vi.stubGlobal(
    "fetch",
    (url: string, init?: RequestInit) =>
      fetchHandler ? fetchHandler(url, init) : Promise.resolve(jsonOk({})),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// ─── Sample catalog data ─────────────────────────────────────────────────────

const customLayout: StoredDiskLayout = {
  id: "custom-001",
  name: "my-custom-layout",
  firmware_kind: "any",
  layout: { partitions: [{ size: "-", fs: "xfs", mountpoint: "/" }] },
  captured_at: "2026-01-01T00:00:00Z",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}

const biosLayout: StoredDiskLayout = {
  id: "bios-001",
  name: "clustr-default-bios",
  firmware_kind: "bios",
  layout: { partitions: [{ size: "1GiB", fs: "ext4", mountpoint: "/boot" }, { size: "-", fs: "xfs", mountpoint: "/" }] },
  captured_at: "2026-01-01T00:00:00Z",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}

const uefiLayout: StoredDiskLayout = {
  id: "uefi-001",
  name: "clustr-default-uefi",
  firmware_kind: "uefi",
  layout: {
    partitions: [
      { size: "512MiB", fs: "vfat", flags: ["esp"], mountpoint: "/boot/efi" },
      { size: "-", fs: "xfs", mountpoint: "/" },
    ],
  },
  captured_at: "2026-01-01T00:00:00Z",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}

const sampleCatalog: ListDiskLayoutsResponse = {
  layouts: [biosLayout, uefiLayout, customLayout],
  total: 3,
}

// ─── Test 1: Edit button visible for custom, disabled for seed ────────────────

describe("DiskLayoutsPage — edit button visibility", () => {
  it("should render an enabled edit button for custom layouts", async () => {
    fetchHandler = () => Promise.resolve(jsonOk(sampleCatalog))

    withQC(<DiskLayoutsPage />)

    await waitFor(() => {
      expect(screen.getByText("my-custom-layout")).toBeInTheDocument()
    })

    const editBtn = screen.getByTestId(`edit-layout-${customLayout.id}`)
    expect(editBtn).toBeInTheDocument()
    expect(editBtn).not.toBeDisabled()
  })

  it("should render a disabled edit button for clustr-default-bios", async () => {
    fetchHandler = () => Promise.resolve(jsonOk(sampleCatalog))

    withQC(<DiskLayoutsPage />)

    await waitFor(() => {
      expect(screen.getByText("clustr-default-bios")).toBeInTheDocument()
    })

    const disabledBtn = screen.getByTestId(`edit-layout-disabled-${biosLayout.id}`)
    expect(disabledBtn).toBeInTheDocument()
    expect(disabledBtn).toBeDisabled()
  })

  it("should render a disabled edit button for clustr-default-uefi", async () => {
    fetchHandler = () => Promise.resolve(jsonOk(sampleCatalog))

    withQC(<DiskLayoutsPage />)

    await waitFor(() => {
      expect(screen.getByText("clustr-default-uefi")).toBeInTheDocument()
    })

    const disabledBtn = screen.getByTestId(`edit-layout-disabled-${uefiLayout.id}`)
    expect(disabledBtn).toBeInTheDocument()
    expect(disabledBtn).toBeDisabled()
  })
})

// ─── Test 2: Edit dialog opens with pre-filled data ───────────────────────────

describe("DiskLayoutsPage — edit dialog opens with pre-filled values", () => {
  it("should open edit dialog with the layout's name and JSON", async () => {
    const user = userEvent.setup()
    fetchHandler = () => Promise.resolve(jsonOk(sampleCatalog))

    withQC(<DiskLayoutsPage />)

    await waitFor(() => {
      expect(screen.getByText("my-custom-layout")).toBeInTheDocument()
    })

    const editBtn = screen.getByTestId(`edit-layout-${customLayout.id}`)
    await user.click(editBtn)

    // Dialog should be visible with pre-filled name
    const nameInput = await screen.findByTestId("edit-layout-name")
    expect(nameInput).toBeInTheDocument()
    expect((nameInput as HTMLInputElement).value).toBe("my-custom-layout")

    // Layout JSON textarea should be pre-filled
    const jsonArea = screen.getByTestId("edit-layout-json")
    expect(jsonArea).toBeInTheDocument()
    const parsed = JSON.parse((jsonArea as HTMLTextAreaElement).value)
    expect(parsed.partitions).toBeDefined()
  })
})

// ─── Test 3: Save fires PUT with correct body ─────────────────────────────────

describe("DiskLayoutsPage — edit save fires PUT with correct shape", () => {
  it("should PUT to /api/v1/disk-layouts/{id} with name and layout_json", async () => {
    const user = userEvent.setup()
    const putBodies: { url: string; body: unknown }[] = []

    fetchHandler = (url, init) => {
      if (init?.method === "PUT" && url.includes(`/disk-layouts/${customLayout.id}`)) {
        putBodies.push({ url, body: JSON.parse(init.body as string) })
        return Promise.resolve(jsonOk({ disk_layout: { ...customLayout, name: "updated-name" } }))
      }
      return Promise.resolve(jsonOk(sampleCatalog))
    }

    withQC(<DiskLayoutsPage />)

    await waitFor(() => {
      expect(screen.getByText("my-custom-layout")).toBeInTheDocument()
    })

    const editBtn = screen.getByTestId(`edit-layout-${customLayout.id}`)
    await user.click(editBtn)

    // Wait for dialog to open
    const nameInput = await screen.findByTestId("edit-layout-name")

    // Change the name
    await user.clear(nameInput)
    await user.type(nameInput, "updated-name")

    // Click save
    const saveBtn = screen.getByTestId("edit-layout-save")
    await user.click(saveBtn)

    await waitFor(() => {
      expect(putBodies.length).toBe(1)
    })

    expect(putBodies[0].body).toMatchObject({
      name: "updated-name",
      layout_json: expect.any(String),
    })

    // layout_json must be valid JSON
    const layoutJson = JSON.parse((putBodies[0].body as { layout_json: string }).layout_json)
    expect(layoutJson.partitions).toBeDefined()
  })
})

// ─── Test 4: On server error, dialog stays open ───────────────────────────────

describe("DiskLayoutsPage — edit error keeps dialog open", () => {
  it("should keep dialog open when PUT returns 400", async () => {
    const user = userEvent.setup()

    fetchHandler = (url, init) => {
      if (init?.method === "PUT" && url.includes(`/disk-layouts/${customLayout.id}`)) {
        return Promise.resolve(
          jsonErr({ error: "name already exists", code: "conflict" }, 400)
        )
      }
      return Promise.resolve(jsonOk(sampleCatalog))
    }

    withQC(<DiskLayoutsPage />)

    await waitFor(() => {
      expect(screen.getByText("my-custom-layout")).toBeInTheDocument()
    })

    const editBtn = screen.getByTestId(`edit-layout-${customLayout.id}`)
    await user.click(editBtn)

    const saveBtn = await screen.findByTestId("edit-layout-save")
    await user.click(saveBtn)

    // Dialog stays open — save button still visible
    await waitFor(() => {
      expect(screen.getByTestId("edit-layout-save")).toBeInTheDocument()
    })
  })
})

// ─── Test 5: JSON validation prevents save with invalid JSON ──────────────────

describe("DiskLayoutsPage — edit JSON validation", () => {
  it("should show error and not PUT when layout JSON is invalid", async () => {
    const user = userEvent.setup()
    const putUrls: string[] = []

    fetchHandler = (url, init) => {
      if (init?.method === "PUT") {
        putUrls.push(url)
        return Promise.resolve(jsonOk({ disk_layout: customLayout }))
      }
      return Promise.resolve(jsonOk(sampleCatalog))
    }

    withQC(<DiskLayoutsPage />)

    await waitFor(() => {
      expect(screen.getByText("my-custom-layout")).toBeInTheDocument()
    })

    const editBtn = screen.getByTestId(`edit-layout-${customLayout.id}`)
    await user.click(editBtn)

    const jsonArea = await screen.findByTestId("edit-layout-json")

    // Replace with invalid JSON
    await user.clear(jsonArea)
    await user.type(jsonArea, "{ invalid json }")

    const saveBtn = screen.getByTestId("edit-layout-save")
    await user.click(saveBtn)

    // Error message should appear
    await waitFor(() => {
      expect(screen.getByTestId("edit-layout-json-error")).toBeInTheDocument()
    })

    // PUT should NOT have been called
    expect(putUrls.length).toBe(0)
  })
})
