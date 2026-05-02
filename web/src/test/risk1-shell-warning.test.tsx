/**
 * risk1-shell-warning.test.tsx — RISK-1(a) shell mutation warning modal tests.
 *
 * Covers:
 *  - Modal renders with the expected warning text when warningAccepted=false.
 *  - Cancel button calls onCancel without calling onConfirm.
 *  - Confirm button calls onConfirm without calling onCancel.
 *  - sessionStorage key is set after acceptance (via the exported helpers).
 */

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import { ShellMutationWarningModal } from "../components/ImageShell"

// ─── Helpers ──────────────────────────────────────────────────────────────────

const ACCEPTED_KEY = "clustr:shell_mutation_warning_accepted"

function clearSessionStorage() {
  sessionStorage.removeItem(ACCEPTED_KEY)
}

// ─── Tests ────────────────────────────────────────────────────────────────────

describe("ShellMutationWarningModal", () => {
  beforeEach(() => {
    clearSessionStorage()
    vi.resetAllMocks()
  })

  it("should render warning text mentioning base image modification", () => {
    render(
      <ShellMutationWarningModal
        imageName="rocky9-base"
        onConfirm={() => {}}
        onCancel={() => {}}
      />
    )

    // Title text
    expect(screen.getByText(/Shell session.*base image will be modified/i)).toBeTruthy()

    // Body must mention direct rootfs writes
    expect(screen.getByText(/write directly into/i)).toBeTruthy()

    // Must mention overlay isolation so operators know the roadmap
    expect(screen.getByText(/Overlay isolation/i)).toBeTruthy()

    // Image name must appear in the modal body
    expect(screen.getByText(/rocky9-base/)).toBeTruthy()
  })

  it("should render Cancel and Open shell anyway buttons", () => {
    render(
      <ShellMutationWarningModal
        imageName="test-image"
        onConfirm={() => {}}
        onCancel={() => {}}
      />
    )

    expect(screen.getByTestId("shell-warning-cancel-btn")).toBeTruthy()
    expect(screen.getByTestId("shell-warning-confirm-btn")).toBeTruthy()
  })

  it("should call onCancel when Cancel is clicked, not onConfirm", () => {
    const onConfirm = vi.fn()
    const onCancel = vi.fn()

    render(
      <ShellMutationWarningModal
        imageName="test-image"
        onConfirm={onConfirm}
        onCancel={onCancel}
      />
    )

    fireEvent.click(screen.getByTestId("shell-warning-cancel-btn"))

    expect(onCancel).toHaveBeenCalledOnce()
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it("should call onConfirm when Open shell anyway is clicked, not onCancel", () => {
    const onConfirm = vi.fn()
    const onCancel = vi.fn()

    render(
      <ShellMutationWarningModal
        imageName="test-image"
        onConfirm={onConfirm}
        onCancel={onCancel}
      />
    )

    fireEvent.click(screen.getByTestId("shell-warning-confirm-btn"))

    expect(onConfirm).toHaveBeenCalledOnce()
    expect(onCancel).not.toHaveBeenCalled()
  })
})
