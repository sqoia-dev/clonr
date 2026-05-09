/**
 * sprint34-boot-settings.test.tsx — BOOT-SETTINGS-MODAL tests (Sprint 34 UI-A)
 *
 * Covers:
 *   1. Modal mounts and renders all three fields
 *   2. Submit with all fields empty → PATCH body is {} (no fields)
 *   3. Submit with only boot_order_policy set → body contains only that field
 *   4. Submit with all fields → body contains all three
 *   5. Cancel calls onClose without API call
 */

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { BootSettingsModal } from "../components/BootSettingsModal"
import type { NodeConfig } from "../lib/types"

const mockApiFetch = vi.fn()

vi.mock("../lib/api", () => ({
  apiFetch: (...args: unknown[]) => mockApiFetch(...args),
  SESSION_EXPIRED_EVENT: "session:expired",
}))

vi.mock("../hooks/use-toast", () => ({
  toast: vi.fn(),
}))

function makeQC() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
}

const TEST_NODE: NodeConfig = {
  id: "node-abc-123",
  hostname: "compute01",
  hostname_auto: false,
  fqdn: "compute01.cluster.local",
  primary_mac: "aa:bb:cc:dd:ee:ff",
  interfaces: [],
  ssh_keys: [],
  kernel_args: "",
  tags: [],
  groups: [],
  custom_vars: {},
  base_image_id: "",
  reimage_pending: false,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}

function renderModal(props: Partial<React.ComponentProps<typeof BootSettingsModal>> = {}) {
  const qc = makeQC()
  qc.setQueryData(["boot-entries"], {
    entries: [
      { id: "rescue", label: "Rescue Shell", description: "Minimal rescue environment" },
      { id: "memtest", label: "Memtest86+", description: "Memory diagnostics" },
    ],
  })
  const onClose = vi.fn()
  const utils = render(
    <QueryClientProvider client={qc}>
      <BootSettingsModal
        open={props.open ?? true}
        onClose={props.onClose ?? onClose}
        node={props.node ?? TEST_NODE}
      />
    </QueryClientProvider>,
  )
  return { ...utils, onClose }
}

// ─── Test 1: modal renders all fields ────────────────────────────────────────

describe("BootSettingsModal — renders", () => {
  beforeEach(() => { mockApiFetch.mockReset() })

  it("should render the modal title with node hostname", () => {
    renderModal()
    const title = screen.getByRole("heading")
    expect(title).toHaveTextContent(/Boot Settings/)
    expect(title).toHaveTextContent(/compute01/)
  })

  it("should render boot order policy radio buttons", () => {
    renderModal()
    expect(screen.getByLabelText("Inherit (default)")).toBeInTheDocument()
    expect(screen.getByLabelText("Network")).toBeInTheDocument()
    expect(screen.getByLabelText("OS disk")).toBeInTheDocument()
  })

  it("should render netboot menu entry dropdown", () => {
    renderModal()
    expect(screen.getByLabelText(/Netboot Menu Entry/i)).toBeInTheDocument()
  })

  it("should render kernel cmdline input", () => {
    renderModal()
    expect(screen.getByLabelText(/Kernel cmdline/i)).toBeInTheDocument()
  })

  it("should show pre-populated boot entries in the dropdown", () => {
    renderModal()
    expect(screen.getByRole("combobox")).toBeInTheDocument()
    expect(screen.getByText(/Rescue Shell/)).toBeInTheDocument()
    expect(screen.getByText(/Memtest86\+/)).toBeInTheDocument()
  })
})

// ─── Test 2: submit with all empty → body is {} ───────────────────────────────

describe("BootSettingsModal — submit all-empty body", () => {
  beforeEach(() => {
    mockApiFetch.mockReset()
    mockApiFetch.mockResolvedValue({ ...TEST_NODE })
  })

  it("should call PATCH with empty body when no fields are filled", async () => {
    renderModal()
    fireEvent.click(screen.getByRole("button", { name: /Save Boot Settings/i }))
    await waitFor(() => expect(mockApiFetch).toHaveBeenCalled())

    const [url, opts] = mockApiFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`/api/v1/nodes/${TEST_NODE.id}/boot-settings`)
    expect(opts.method).toBe("PATCH")
    const body = JSON.parse(opts.body as string)
    expect(body).toEqual({})
  })
})

// ─── Test 3: submit with only boot_order_policy ───────────────────────────────

describe("BootSettingsModal — submit with one field", () => {
  beforeEach(() => {
    mockApiFetch.mockReset()
    mockApiFetch.mockResolvedValue({ ...TEST_NODE })
  })

  it("should include only boot_order_policy when only that radio is changed", async () => {
    renderModal()
    fireEvent.click(screen.getByLabelText("Network"))
    fireEvent.click(screen.getByRole("button", { name: /Save Boot Settings/i }))
    await waitFor(() => expect(mockApiFetch).toHaveBeenCalled())

    const [, opts] = mockApiFetch.mock.calls[0] as [string, RequestInit]
    const body = JSON.parse(opts.body as string)
    expect(body.boot_order_policy).toBe("network")
    expect(body.netboot_menu_entry).toBeUndefined()
    expect(body.kernel_cmdline).toBeUndefined()
  })
})

// ─── Test 4: submit with all fields ───────────────────────────────────────────

describe("BootSettingsModal — submit all fields", () => {
  beforeEach(() => {
    mockApiFetch.mockReset()
    mockApiFetch.mockResolvedValue({ ...TEST_NODE })
  })

  it("should include boot_order_policy, netboot_menu_entry, and kernel_cmdline", async () => {
    renderModal()

    fireEvent.click(screen.getByLabelText("OS disk"))
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "rescue" } })
    const cmdlineInput = screen.getByPlaceholderText("(no override)")
    fireEvent.change(cmdlineInput, { target: { value: "console=ttyS0,115200n8" } })

    fireEvent.click(screen.getByRole("button", { name: /Save Boot Settings/i }))
    await waitFor(() => expect(mockApiFetch).toHaveBeenCalled())

    const [url, opts] = mockApiFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`/api/v1/nodes/${TEST_NODE.id}/boot-settings`)
    const body = JSON.parse(opts.body as string)
    expect(body.boot_order_policy).toBe("os")
    expect(body.netboot_menu_entry).toBe("rescue")
    expect(body.kernel_cmdline).toBe("console=ttyS0,115200n8")
  })
})

// ─── Test 5: cancel calls onClose without API call ────────────────────────────

describe("BootSettingsModal — cancel", () => {
  beforeEach(() => { mockApiFetch.mockReset() })

  it("should call onClose and NOT call the API when Cancel is clicked", async () => {
    const { onClose } = renderModal()
    fireEvent.click(screen.getByRole("button", { name: /Cancel/i }))
    expect(onClose).toHaveBeenCalled()
    expect(mockApiFetch).not.toHaveBeenCalled()
  })
})
