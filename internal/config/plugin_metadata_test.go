package config_test

// plugin_metadata_test.go — Sprint 41 Day 1 P2 fix
//
// Regression tests for the EffectivePriority / ValidatePriority contract:
//
//   - Priority=0 is the unset sentinel; EffectivePriority maps it to DefaultPriority (100).
//   - Priority=1 is "run first" and is returned as-is (NOT promoted to 100).
//   - Negative priorities are always invalid; ValidatePriority returns an error.
//   - Priority=0 is a valid sentinel; ValidatePriority returns nil.

import (
	"testing"

	"github.com/sqoia-dev/clustr/internal/config"
)

// TestEffectivePriority_ZeroIsDefault verifies that a zero Priority is promoted
// to DefaultPriority (100) by EffectivePriority. Zero is the unset sentinel;
// a plugin that never sets Priority gets scheduled at the default band.
func TestEffectivePriority_ZeroIsDefault(t *testing.T) {
	m := config.PluginMetadata{Priority: 0}
	got := config.EffectivePriority(m)
	if got != config.DefaultPriority {
		t.Errorf("EffectivePriority(Priority=0) = %d; want %d (DefaultPriority)",
			got, config.DefaultPriority)
	}
}

// TestEffectivePriority_OneIsRunFirst verifies that Priority=1 is returned
// as-is (1) and is NOT promoted to DefaultPriority (100). Priority=1 is the
// canonical "run first" value now that 0 is reserved as the unset sentinel.
func TestEffectivePriority_OneIsRunFirst(t *testing.T) {
	m := config.PluginMetadata{Priority: 1}
	got := config.EffectivePriority(m)
	if got != 1 {
		t.Errorf("EffectivePriority(Priority=1) = %d; want 1 (run-first, not promoted to default)",
			got)
	}
}

// TestValidatePriority_Negative verifies that ValidatePriority returns a
// non-nil error for any negative input. Negative priorities are always a
// caller error — there is no valid use for a negative priority value.
func TestValidatePriority_Negative(t *testing.T) {
	for _, p := range []int{-1, -10, -1000} {
		if err := config.ValidatePriority(p); err == nil {
			t.Errorf("ValidatePriority(%d) = nil; want error — negative priority must be rejected", p)
		}
	}
}

// TestValidatePriority_Zero verifies that ValidatePriority returns nil for
// Priority=0. Zero is the unset sentinel; it is a valid input that callers
// may pass to indicate "use the default". EffectivePriority handles promotion.
func TestValidatePriority_Zero(t *testing.T) {
	if err := config.ValidatePriority(0); err != nil {
		t.Errorf("ValidatePriority(0) = %v; want nil — zero is the valid unset sentinel", err)
	}
}
