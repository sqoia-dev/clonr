// privhelper_test.go — anti-regression tests for the privhelper binary.
//
// These tests build the binary from source and invoke it in a test fixture
// to verify that:
//   - dnf-install rejects packages not in deps_matrix.json
//   - dnf-install rejects package names with shell metacharacters
//   - cap-bit-test returns a parseable euid line
//   - unknown verbs are rejected
package privhelper

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildHelper builds the clustr-privhelper binary and returns its path.
// The binary is built into a temp dir and cleaned up by t.Cleanup.
func buildHelper(t *testing.T) string {
	t.Helper()

	// Locate the module root by walking up from this test file.
	// thisFile: .../internal/privhelper/privhelper_test.go
	// modRoot:  .../ (two parent dirs up from "internal/privhelper")
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	modRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "clustr-privhelper")

	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/clustr-privhelper")
	cmd.Dir = modRoot
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build clustr-privhelper: %v\n%s", err, out)
	}
	return binPath
}

// TestPrivhelperDnfInstallRejectsUnknownPackage verifies that dnf-install exits
// non-zero and prints a structured rejection for a package not in the allowlist.
func TestPrivhelperDnfInstallRejectsUnknownPackage(t *testing.T) {
	bin := buildHelper(t)

	cmd := exec.CommandContext(context.Background(), bin, "dnf-install", "totally-not-a-real-package-xyz")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected non-zero exit for unknown package, got exit 0")
	}

	output := string(out)
	if !strings.Contains(output, "not in the deps_matrix allowlist") {
		t.Errorf("expected allowlist rejection message, got: %q", output)
	}
}

// TestPrivhelperDnfInstallRejectsInjectionAttempt verifies that package names
// containing shell metacharacters or flag-like strings are rejected by the
// character-safety check before even reaching the allowlist lookup.
func TestPrivhelperDnfInstallRejectsInjectionAttempt(t *testing.T) {
	bin := buildHelper(t)

	badNames := []string{
		"pkg; rm -rf /",
		"pkg --assumeyes",
		"pkg$(id)",
		"../etc/passwd",
		"pkg|cat",
		"-y",
	}

	for _, name := range badNames {
		name := name
		t.Run("bad="+name, func(t *testing.T) {
			cmd := exec.CommandContext(context.Background(), bin, "dnf-install", name)
			_, err := cmd.CombinedOutput()
			if err == nil {
				t.Errorf("expected non-zero exit for malicious pkg name %q, got exit 0", name)
			}
		})
	}
}

// TestPrivhelperDnfInstallRejectsEmptyPackage verifies that an empty package
// name is rejected.
func TestPrivhelperDnfInstallRejectsEmptyPackage(t *testing.T) {
	bin := buildHelper(t)

	// Pass "" as the package argument — the helper should exit non-zero.
	cmd := exec.CommandContext(context.Background(), bin, "dnf-install", "")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit for empty package name, got exit 0\noutput: %s", out)
	}
}

// TestPrivhelperCapBitTest verifies that cap-bit-test exits 0 and outputs a
// line with the format "clustr-privhelper cap-bit-test: euid=<n>".
func TestPrivhelperCapBitTest(t *testing.T) {
	bin := buildHelper(t)

	cmd := exec.CommandContext(context.Background(), bin, "cap-bit-test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cap-bit-test failed: %v\noutput: %s", err, out)
	}

	line := strings.TrimSpace(string(out))
	if !strings.HasPrefix(line, "clustr-privhelper cap-bit-test: euid=") {
		t.Errorf("unexpected cap-bit-test output: %q", line)
	}

	// Verify the euid value is parseable.
	var euid int
	if _, scanErr := fmt.Sscanf(line, "clustr-privhelper cap-bit-test: euid=%d", &euid); scanErr != nil {
		t.Errorf("failed to parse euid from output %q: %v", line, scanErr)
	}
	t.Logf("cap-bit-test reported euid=%d", euid)
}

// TestPrivhelperUnknownVerb verifies that an unknown verb exits non-zero.
func TestPrivhelperUnknownVerb(t *testing.T) {
	bin := buildHelper(t)

	cmd := exec.CommandContext(context.Background(), bin, "not-a-real-verb")
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for unknown verb")
	}
}

// TestPrivhelperNoArgs verifies that running with no arguments exits non-zero.
func TestPrivhelperNoArgs(t *testing.T) {
	bin := buildHelper(t)

	cmd := exec.CommandContext(context.Background(), bin)
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit when no args supplied")
	}
}

// TestPrivhelperDnfInstallKnownPackageAccepted verifies that a known package
// name (from the allowlist) passes validation and reaches dnf. The test only
// checks the rejection path — actual dnf execution may fail in CI where dnf is
// not present, and that is expected and acceptable.
func TestPrivhelperDnfInstallKnownPackageAccepted(t *testing.T) {
	bin := buildHelper(t)

	// numactl-devel is in every EL variant of the allowlist.
	cmd := exec.CommandContext(context.Background(), bin, "dnf-install", "numactl-devel")
	out, _ := cmd.CombinedOutput()

	// If the output contains the allowlist rejection message, the test fails —
	// that means the allowlist lookup failed on a known-good package.
	if strings.Contains(string(out), "not in the deps_matrix allowlist") {
		t.Errorf("known package 'numactl-devel' was rejected by allowlist: %s", out)
	}
	// We do NOT assert exit code 0 here because dnf may not be installed in CI.
}

// ─── service-control tests ────────────────────────────────────────────────────

// TestServiceControlRejectsUnknownUnit verifies that a unit not in the
// allowlist is rejected with a structured "unit_not_allowed" error message
// and a non-zero exit code. No systemctl invocation occurs.
func TestServiceControlRejectsUnknownUnit(t *testing.T) {
	bin := buildHelper(t)

	cmd := exec.CommandContext(context.Background(), bin, "service-control", "clustr-foobar.service", "start")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected non-zero exit for unit not in allowlist, got exit 0")
	}

	output := string(out)
	if !strings.Contains(output, "unit_not_allowed") {
		t.Errorf("expected 'unit_not_allowed' in output for rejected unit, got: %q", output)
	}
}

// TestServiceControlRejectsUnknownAction verifies that a valid unit combined
// with a disallowed action (e.g. mask) is rejected with "action_not_allowed".
func TestServiceControlRejectsUnknownAction(t *testing.T) {
	bin := buildHelper(t)

	cmd := exec.CommandContext(context.Background(), bin, "service-control", "clustr-slapd.service", "mask")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected non-zero exit for disallowed action, got exit 0")
	}

	output := string(out)
	if !strings.Contains(output, "action_not_allowed") {
		t.Errorf("expected 'action_not_allowed' in output for rejected action, got: %q", output)
	}
}

// TestServiceControlAllowedUnitPassesValidation verifies that
// "clustr-slapd.service" with action "restart" passes both allowlist checks
// and reaches systemctl. The test only validates that no rejection message
// appears — systemctl may exit non-zero in CI where the unit does not exist,
// and that is acceptable.
func TestServiceControlAllowedUnitPassesValidation(t *testing.T) {
	bin := buildHelper(t)

	cmd := exec.CommandContext(context.Background(), bin, "service-control", "clustr-slapd.service", "restart")
	out, _ := cmd.CombinedOutput()

	output := string(out)
	// If either rejection string appears, the allowlist check misfired on a known-good pair.
	if strings.Contains(output, "unit_not_allowed") {
		t.Errorf("clustr-slapd.service was rejected by unit allowlist: %s", output)
	}
	if strings.Contains(output, "action_not_allowed") {
		t.Errorf("'restart' action was rejected by action allowlist: %s", output)
	}
	// We do NOT assert exit code 0 — systemctl may fail in CI where systemd is not running.
}

// TestServiceControlRequiresTwoArgs verifies that wrong argument count exits non-zero.
func TestServiceControlRequiresTwoArgs(t *testing.T) {
	bin := buildHelper(t)

	// Too few args.
	cmd := exec.CommandContext(context.Background(), bin, "service-control", "clustr-slapd.service")
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Error("expected non-zero exit for service-control with only one arg")
	}

	// No args.
	cmd = exec.CommandContext(context.Background(), bin, "service-control")
	_, err = cmd.CombinedOutput()
	if err == nil {
		t.Error("expected non-zero exit for service-control with no args")
	}
}
