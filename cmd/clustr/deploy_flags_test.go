// deploy_flags_test.go — tests for deploy command flag visibility (#222).
package main

import (
	"strings"
	"testing"
)

// TestDeployCmd_FixEFI_HiddenFromHelp verifies that --fix-efi does not appear
// in the default --help output (it is a deprecated no-op recovery flag).
func TestDeployCmd_FixEFI_HiddenFromHelp(t *testing.T) {
	cmd := newDeployCmd()
	// Cobra writes help to cmd.OutOrStdout(); capture via SetOut.
	var sb strings.Builder
	cmd.SetOut(&sb)
	cmd.SetErr(&sb)
	_ = cmd.Help()
	helpText := sb.String()

	if strings.Contains(helpText, "--fix-efi") {
		t.Error("--fix-efi must not appear in default --help output (it is a hidden recovery flag, #222)")
	}
}

// TestDeployCmd_FixEFI_ParsesWithoutError verifies that --fix-efi is still
// accepted as a valid flag (hidden ≠ removed). The command will fail at the
// RunE stage due to no server being available, but flag parsing must succeed
// with no "unknown flag" error.
func TestDeployCmd_FixEFI_ParsesWithoutError(t *testing.T) {
	cmd := newDeployCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	// Parse only — do not run. ParseFlags validates the flag set without
	// invoking RunE, so we get a clean signal on whether the flag is
	// registered and hidden (not removed).
	err := cmd.ParseFlags([]string{"--fix-efi"})
	if err != nil {
		t.Errorf("--fix-efi must still parse without error after being hidden; got: %v", err)
	}
}
