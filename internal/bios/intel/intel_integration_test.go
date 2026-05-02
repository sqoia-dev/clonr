//go:build integration

// Package intel — integration tests that exercise the intel provider against
// the fake-syscfg binary built from test/bios/fake-syscfg/main.go.
//
// Build the fake binary first:
//
//	go build -o /tmp/fake-syscfg ./test/bios/fake-syscfg
//
// Then run:
//
//	go test -tags integration ./internal/bios/intel/...
//
// The test is tagged "integration" so it doesn't run in CI by default.
// The fake-syscfg binary is built on demand if the FAKE_SYSCFG_BIN env var
// is unset and go build is available.
package intel_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sqoia-dev/clustr/internal/bios"
	"github.com/sqoia-dev/clustr/internal/bios/intel"
)

// buildFakeSyscfg builds the fake-syscfg binary into a temp directory and
// returns its path. Skips the test if go build fails.
func buildFakeSyscfg(t *testing.T) string {
	t.Helper()
	binPath := os.Getenv("FAKE_SYSCFG_BIN")
	if binPath != "" {
		return binPath
	}

	// Find the module root by walking up to find go.mod.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Walk up from internal/bios/intel → repo root.
	root := wd
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		root = filepath.Dir(root)
	}
	fakeSrc := filepath.Join(root, "test", "bios", "fake-syscfg")
	if _, err := os.Stat(fakeSrc); err != nil {
		t.Skipf("fake-syscfg source not found at %s: %v", fakeSrc, err)
	}

	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "fake-syscfg")
	cmd := exec.Command("go", "build", "-o", bin, fakeSrc)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Skipf("go build fake-syscfg failed: %v", err)
	}
	return bin
}

// TestIntelProviderWithFakeSyscfg_ReadCurrent verifies that ReadCurrent parses
// the fake-syscfg /s - output correctly.
func TestIntelProviderWithFakeSyscfg_ReadCurrent(t *testing.T) {
	bin := buildFakeSyscfg(t)
	p := intel.NewWithBinaryPath(bin)

	// Use default canned settings (no FAKE_SYSCFG_SETTINGS set).
	t.Setenv("FAKE_SYSCFG_SETTINGS", "")
	t.Setenv("FAKE_SYSCFG_FAIL", "")

	settings, err := p.ReadCurrent(context.Background())
	if err != nil {
		t.Fatalf("ReadCurrent: %v", err)
	}
	if len(settings) == 0 {
		t.Fatal("ReadCurrent returned 0 settings, want > 0")
	}
	// Verify HyperThreading is present (it's in the fake's default set).
	found := false
	for _, s := range settings {
		if s.Name == "HyperThreading" {
			found = true
			if s.Value != "Enabled" {
				t.Errorf("HyperThreading = %q, want Enabled", s.Value)
			}
			break
		}
	}
	if !found {
		t.Error("ReadCurrent: HyperThreading not found in settings")
	}
}

// TestIntelProviderWithFakeSyscfg_DiffAndApply verifies that Diff + Apply work
// end-to-end when current and desired differ.
func TestIntelProviderWithFakeSyscfg_DiffAndApply(t *testing.T) {
	bin := buildFakeSyscfg(t)

	// Current: VTd=Disabled, TurboMode=Enabled
	t.Setenv("FAKE_SYSCFG_SETTINGS", `{"VTd":"Disabled","TurboMode":"Enabled","HyperThreading":"Enabled"}`)
	t.Setenv("FAKE_SYSCFG_FAIL", "")

	p := intel.NewWithBinaryPath(bin)
	current, err := p.ReadCurrent(context.Background())
	if err != nil {
		t.Fatalf("ReadCurrent: %v", err)
	}

	// Desired: enable VTd, disable TurboMode.
	desired := []bios.Setting{
		{Name: "VTd", Value: "Enabled"},
		{Name: "TurboMode", Value: "Disabled"},
	}
	changes, err := p.Diff(desired, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("Diff returned %d changes, want 2: %+v", len(changes), changes)
	}

	applied, err := p.Apply(context.Background(), changes)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(applied) != 2 {
		t.Errorf("Apply returned %d applied, want 2", len(applied))
	}
}

// TestIntelProviderWithFakeSyscfg_NoDrift verifies that Diff returns 0 changes
// when current matches desired.
func TestIntelProviderWithFakeSyscfg_NoDrift(t *testing.T) {
	bin := buildFakeSyscfg(t)
	t.Setenv("FAKE_SYSCFG_SETTINGS", `{"HyperThreading":"Enabled","VTd":"Enabled"}`)
	t.Setenv("FAKE_SYSCFG_FAIL", "")

	p := intel.NewWithBinaryPath(bin)
	current, err := p.ReadCurrent(context.Background())
	if err != nil {
		t.Fatalf("ReadCurrent: %v", err)
	}

	desired := []bios.Setting{
		{Name: "HyperThreading", Value: "Enabled"},
		{Name: "VTd", Value: "Enabled"},
	}
	changes, err := p.Diff(desired, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("Diff returned %d changes for identical settings, want 0: %+v", len(changes), changes)
	}

	// Apply with no changes: must be a no-op (nil, nil).
	applied, err := p.Apply(context.Background(), changes)
	if err != nil {
		t.Errorf("Apply(nil): unexpected error: %v", err)
	}
	if applied != nil {
		t.Errorf("Apply(nil): got %d applied changes, want nil", len(applied))
	}
}

// TestIntelProviderWithFakeSyscfg_SupportedSettings verifies /d parsing.
func TestIntelProviderWithFakeSyscfg_SupportedSettings(t *testing.T) {
	bin := buildFakeSyscfg(t)
	t.Setenv("FAKE_SYSCFG_SETTINGS", "")
	t.Setenv("FAKE_SYSCFG_FAIL", "")

	p := intel.NewWithBinaryPath(bin)
	names, err := p.SupportedSettings(context.Background())
	if err != nil {
		t.Fatalf("SupportedSettings: %v", err)
	}
	if len(names) == 0 {
		t.Error("SupportedSettings returned 0 names, want > 0")
	}
}

// TestIntelProviderWithFakeSyscfg_HardwareError verifies that a simulated
// hardware error propagates correctly.
func TestIntelProviderWithFakeSyscfg_HardwareError(t *testing.T) {
	bin := buildFakeSyscfg(t)
	t.Setenv("FAKE_SYSCFG_FAIL", "1")
	t.Setenv("FAKE_SYSCFG_SETTINGS", "")

	p := intel.NewWithBinaryPath(bin)
	_, err := p.ReadCurrent(context.Background())
	if err == nil {
		t.Fatal("ReadCurrent with FAKE_SYSCFG_FAIL=1: expected error, got nil")
	}
}
