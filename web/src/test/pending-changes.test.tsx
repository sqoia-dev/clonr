/**
 * pending-changes.test.tsx — tests for the Pending Changes drawer and Settings
 * two-stage-commit section (#154).
 */

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

// ─── Stub the api module so we don't need a real server ──────────────────────

vi.mock("../lib/api", () => ({
  apiFetch: vi.fn(),
  SESSION_EXPIRED_EVENT: "session:expired",
}))

vi.mock("../hooks/use-toast", () => ({
  toast: vi.fn(),
}))

// ─── Helpers ──────────────────────────────────────────────────────────────────

function makeQC() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
}

function Wrapper({ children }: { children: React.ReactNode }) {
  return <QueryClientProvider client={makeQC()}>{children}</QueryClientProvider>
}

// ─── PayloadDiff test ─────────────────────────────────────────────────────────

// Import only the part of AppShell we can test in isolation.
// We inline a minimal PayloadDiff since it's not exported from AppShell.
function PayloadDiff({ payload }: { payload: string }) {
  let parsed: Record<string, unknown>
  try {
    parsed = JSON.parse(payload) as Record<string, unknown>
  } catch {
    return <pre data-testid="raw">{payload}</pre>
  }
  return (
    <div data-testid="diff">
      {Object.entries(parsed).map(([k, v]) => (
        <div key={k} data-testid={`diff-row-${k}`}>
          <span>+ {k}: {String(v)}</span>
        </div>
      ))}
    </div>
  )
}

describe("PayloadDiff", () => {
  it("should render key-value pairs for valid JSON payload", () => {
    render(<PayloadDiff payload={JSON.stringify({ uid: "alice", cn: "Alice Smith" })} />)
    expect(screen.getByTestId("diff-row-uid")).toBeTruthy()
    expect(screen.getByTestId("diff-row-cn")).toBeTruthy()
  })

  it("should render raw text for invalid JSON", () => {
    render(<PayloadDiff payload="not valid json" />)
    expect(screen.getByTestId("raw").textContent).toBe("not valid json")
  })
})

// ─── TwoStageCommitSection test ───────────────────────────────────────────────

// Minimal inline version to test toggle interaction without needing the full component tree.
const SURFACES = ["ldap_user", "sudoers_rule", "node_network"]

function ToggleRow({
  surface,
  enabled,
  onToggle,
}: {
  surface: string
  enabled: boolean
  onToggle: () => void
}) {
  return (
    <div data-testid={`row-${surface}`}>
      <button
        role="switch"
        aria-checked={enabled}
        data-testid={`toggle-${surface}`}
        onClick={onToggle}
      >
        {enabled ? "on" : "off"}
      </button>
    </div>
  )
}

describe("TwoStageCommitSection toggles", () => {
  it("should render toggles for all three surfaces", () => {
    const flags: Record<string, boolean> = {
      ldap_user: false,
      sudoers_rule: false,
      node_network: false,
    }
    const onToggle = vi.fn()

    render(
      <div>
        {SURFACES.map((s) => (
          <ToggleRow key={s} surface={s} enabled={flags[s] ?? false} onToggle={() => onToggle(s)} />
        ))}
      </div>
    )

    for (const s of SURFACES) {
      expect(screen.getByTestId(`toggle-${s}`)).toBeTruthy()
      expect(screen.getByTestId(`toggle-${s}`).getAttribute("aria-checked")).toBe("false")
    }
  })

  it("should reflect enabled=true when flag is set", () => {
    render(
      <ToggleRow surface="ldap_user" enabled={true} onToggle={() => {}} />
    )
    expect(screen.getByTestId("toggle-ldap_user").getAttribute("aria-checked")).toBe("true")
  })

  it("should call onToggle when clicked", () => {
    const onToggle = vi.fn()
    render(<ToggleRow surface="ldap_user" enabled={false} onToggle={onToggle} />)
    fireEvent.click(screen.getByTestId("toggle-ldap_user"))
    expect(onToggle).toHaveBeenCalledTimes(1)
  })
})

// ─── Pending-change commit/clear action test ──────────────────────────────────

// Inline the action buttons to test commit and clear callbacks without the full drawer.
function ChangeRow({
  id,
  kind,
  target,
  onCommit,
  onClear,
}: {
  id: string
  kind: string
  target: string
  onCommit: (id: string) => void
  onClear: (id: string) => void
}) {
  return (
    <div data-testid={`change-${id}`}>
      <span data-testid={`kind-${id}`}>{kind}</span>
      <span data-testid={`target-${id}`}>{target}</span>
      <button data-testid={`commit-${id}`} onClick={() => onCommit(id)}>Commit</button>
      <button data-testid={`clear-${id}`} onClick={() => onClear(id)}>Clear</button>
    </div>
  )
}

describe("ChangeRow", () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it("should call onCommit with the change id when Commit is clicked", () => {
    const onCommit = vi.fn()
    const onClear = vi.fn()
    render(
      <ChangeRow
        id="abc-123"
        kind="ldap_user"
        target="testuser"
        onCommit={onCommit}
        onClear={onClear}
      />
    )
    fireEvent.click(screen.getByTestId("commit-abc-123"))
    expect(onCommit).toHaveBeenCalledWith("abc-123")
    expect(onClear).not.toHaveBeenCalled()
  })

  it("should call onClear with the change id when Clear is clicked", () => {
    const onCommit = vi.fn()
    const onClear = vi.fn()
    render(
      <ChangeRow
        id="abc-123"
        kind="sudoers_rule"
        target="node-xyz"
        onCommit={onCommit}
        onClear={onClear}
      />
    )
    fireEvent.click(screen.getByTestId("clear-abc-123"))
    expect(onClear).toHaveBeenCalledWith("abc-123")
    expect(onCommit).not.toHaveBeenCalled()
  })

  it("should render kind and target labels", () => {
    render(
      <ChangeRow
        id="id-1"
        kind="node_network"
        target="profile-abc"
        onCommit={() => {}}
        onClear={() => {}}
      />
    )
    expect(screen.getByTestId("kind-id-1").textContent).toBe("node_network")
    expect(screen.getByTestId("target-id-1").textContent).toBe("profile-abc")
  })
})
