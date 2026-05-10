package config

import (
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestRenderDiff_SameInputSameHash asserts that hashing the same instruction
// list twice produces identical digests (Render idempotency at the hash level).
func TestRenderDiff_SameInputSameHash(t *testing.T) {
	instrs := []api.InstallInstruction{
		{Opcode: "overwrite", Target: "/etc/hostname", Payload: "compute-01\n"},
		{Opcode: "overwrite", Target: "/etc/hosts", Payload: "127.0.0.1 localhost\n"},
	}

	h1, err := HashInstructions(instrs)
	if err != nil {
		t.Fatalf("HashInstructions (first): %v", err)
	}
	h2, err := HashInstructions(instrs)
	if err != nil {
		t.Fatalf("HashInstructions (second): %v", err)
	}
	if h1 != h2 {
		t.Errorf("same input produced different hashes: %q vs %q", h1, h2)
	}
}

// TestRenderDiff_OrderIndependent asserts that instruction order does not
// affect the hash — the canonical form sorts by (target, opcode) first.
func TestRenderDiff_OrderIndependent(t *testing.T) {
	a := []api.InstallInstruction{
		{Opcode: "overwrite", Target: "/etc/hostname", Payload: "compute-01\n"},
		{Opcode: "overwrite", Target: "/etc/hosts", Payload: "127.0.0.1 localhost\n"},
	}
	b := []api.InstallInstruction{
		{Opcode: "overwrite", Target: "/etc/hosts", Payload: "127.0.0.1 localhost\n"},
		{Opcode: "overwrite", Target: "/etc/hostname", Payload: "compute-01\n"},
	}

	ha, err := HashInstructions(a)
	if err != nil {
		t.Fatalf("HashInstructions(a): %v", err)
	}
	hb, err := HashInstructions(b)
	if err != nil {
		t.Fatalf("HashInstructions(b): %v", err)
	}
	if ha != hb {
		t.Errorf("re-ordered instructions produced different hashes: %q vs %q", ha, hb)
	}
}

// TestRenderDiff_DifferentStateDifferentHash asserts that a change in payload
// produces a different hash — the diff engine would detect a change.
func TestRenderDiff_DifferentStateDifferentHash(t *testing.T) {
	base := []api.InstallInstruction{
		{Opcode: "overwrite", Target: "/etc/hostname", Payload: "compute-01\n"},
	}
	changed := []api.InstallInstruction{
		{Opcode: "overwrite", Target: "/etc/hostname", Payload: "compute-99\n"},
	}

	h1, err := HashInstructions(base)
	if err != nil {
		t.Fatalf("HashInstructions(base): %v", err)
	}
	h2, err := HashInstructions(changed)
	if err != nil {
		t.Fatalf("HashInstructions(changed): %v", err)
	}
	if h1 == h2 {
		t.Error("different payloads produced identical hashes — diff engine would miss the change")
	}
}

// TestRenderDiff_EmptySliceHash asserts that nil and empty slices produce
// the same hash so "no instructions" is stable across calls.
func TestRenderDiff_EmptySliceHash(t *testing.T) {
	h1, err := HashInstructions(nil)
	if err != nil {
		t.Fatalf("HashInstructions(nil): %v", err)
	}
	h2, err := HashInstructions([]api.InstallInstruction{})
	if err != nil {
		t.Fatalf("HashInstructions(empty): %v", err)
	}
	if h1 != h2 {
		t.Errorf("nil vs empty slice produced different hashes: %q vs %q", h1, h2)
	}
}

// TestRenderDiff_AnchorsIncludedInHash asserts that two instructions with the
// same target/opcode/payload but different AnchorPairs produce different hashes.
func TestRenderDiff_AnchorsIncludedInHash(t *testing.T) {
	withAnchors := []api.InstallInstruction{
		{
			Opcode:  "overwrite",
			Target:  "/etc/security/limits.conf",
			Payload: "@slurm soft memlock unlimited",
			Anchors: &api.AnchorPair{Begin: "# BEGIN clustr/limits-slurm", End: "# END clustr/limits-slurm"},
		},
	}
	withoutAnchors := []api.InstallInstruction{
		{
			Opcode:  "overwrite",
			Target:  "/etc/security/limits.conf",
			Payload: "@slurm soft memlock unlimited",
		},
	}

	h1, err := HashInstructions(withAnchors)
	if err != nil {
		t.Fatalf("HashInstructions(withAnchors): %v", err)
	}
	h2, err := HashInstructions(withoutAnchors)
	if err != nil {
		t.Fatalf("HashInstructions(withoutAnchors): %v", err)
	}
	if h1 == h2 {
		t.Error("instructions with vs without Anchors produced same hash — AnchorPair must be included in canonical form")
	}
}
