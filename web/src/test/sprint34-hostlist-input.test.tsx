/**
 * sprint34-hostlist-input.test.tsx — HostlistInput live-preview component tests (Sprint 34 UI-A)
 */

import * as React from "react"
import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { HostlistInput } from "../components/HostlistInput"

function ControlledHostlistInput(props: Partial<React.ComponentProps<typeof HostlistInput>> & { initialValue?: string }) {
  const [value, setValue] = React.useState(props.initialValue ?? "")
  return (
    <HostlistInput
      {...props}
      value={value}
      onChange={(v) => { setValue(v); props.onChange?.(v) }}
    />
  )
}

describe("HostlistInput — empty state", () => {
  it("should render without errors when value is empty", () => {
    render(<ControlledHostlistInput />)
    expect(screen.queryByTestId("hostlist-count-badge")).toBeNull()
    expect(screen.queryByRole("alert")).toBeNull()
  })
})

describe("HostlistInput — valid bracket range", () => {
  it("should show count badge of 3 for node[01-03]", () => {
    render(<ControlledHostlistInput initialValue="node[01-03]" />)
    const badge = screen.getByTestId("hostlist-count-badge")
    expect(badge).toHaveTextContent("3")
  })

  it("should expand node[01-03] to 3 names when list is shown", () => {
    render(<ControlledHostlistInput initialValue="node[01-03]" />)
    const toggle = screen.getByRole("button", { name: /show list|collapse/i })
    fireEvent.click(toggle)
    const list = screen.getByTestId("hostlist-preview-list")
    expect(list).toBeInTheDocument()
    expect(list).toHaveTextContent("node01")
    expect(list).toHaveTextContent("node02")
    expect(list).toHaveTextContent("node03")
  })

  it("should render exactly 3 listitem elements for node[01-03]", () => {
    render(<ControlledHostlistInput initialValue="node[01-03]" />)
    fireEvent.click(screen.getByRole("button", { name: /show list|collapse/i }))
    const items = screen.getAllByRole("listitem")
    expect(items).toHaveLength(3)
    expect(items[0]).toHaveTextContent("node01")
    expect(items[1]).toHaveTextContent("node02")
    expect(items[2]).toHaveTextContent("node03")
  })
})

describe("HostlistInput — count badge", () => {
  it("should show badge count matching the expanded list length", () => {
    render(<ControlledHostlistInput initialValue="gpu[1-8]" />)
    const badge = screen.getByTestId("hostlist-count-badge")
    expect(badge).toHaveTextContent("8")
  })

  it("should update badge when input changes", () => {
    const { rerender } = render(
      <HostlistInput value="node[01-03]" onChange={() => undefined} />,
    )
    expect(screen.getByTestId("hostlist-count-badge")).toHaveTextContent("3")

    rerender(<HostlistInput value="node[01-06]" onChange={() => undefined} />)
    expect(screen.getByTestId("hostlist-count-badge")).toHaveTextContent("6")
  })
})

describe("HostlistInput — invalid input", () => {
  it("should show error message for empty brackets", () => {
    render(<ControlledHostlistInput initialValue="node[]" />)
    const alert = screen.getByRole("alert")
    expect(alert).toBeInTheDocument()
    expect(alert.textContent).toMatch(/empty bracket/i)
  })

  it("should show error message for unmatched bracket", () => {
    render(<ControlledHostlistInput initialValue="node[01" />)
    const alert = screen.getByRole("alert")
    expect(alert).toBeInTheDocument()
  })

  it("should NOT show preview list when there is an error", () => {
    render(<ControlledHostlistInput initialValue="node[]" />)
    expect(screen.queryByTestId("hostlist-preview-list")).toBeNull()
  })
})

describe("HostlistInput — plain hostname", () => {
  it("should show count badge of 1 for a plain hostname (no brackets)", () => {
    render(<ControlledHostlistInput initialValue="compute01" />)
    const badge = screen.getByTestId("hostlist-count-badge")
    expect(badge).toHaveTextContent("1")
  })

  it("should NOT show the preview accordion for plain hostnames", () => {
    render(<ControlledHostlistInput initialValue="compute01" />)
    expect(screen.queryByTestId("hostlist-preview-list")).toBeNull()
    expect(screen.queryByRole("button", { name: /show list/i })).toBeNull()
  })
})

describe("HostlistInput — onExpanded callback", () => {
  it("should call onExpanded with the expanded array for valid input", () => {
    const onExpanded = vi.fn()
    render(<HostlistInput value="node[01-03]" onChange={() => undefined} onExpanded={onExpanded} />)
    expect(onExpanded).toHaveBeenCalledWith(["node01", "node02", "node03"])
  })

  it("should call onExpanded with empty array on error", () => {
    const onExpanded = vi.fn()
    render(<HostlistInput value="node[]" onChange={() => undefined} onExpanded={onExpanded} />)
    expect(onExpanded).toHaveBeenCalledWith([])
  })

  it("should call onExpanded with empty array when value is empty", () => {
    const onExpanded = vi.fn()
    render(<HostlistInput value="" onChange={() => undefined} onExpanded={onExpanded} />)
    expect(onExpanded).toHaveBeenCalledWith([])
  })
})
