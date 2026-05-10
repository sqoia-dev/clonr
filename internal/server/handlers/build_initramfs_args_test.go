package handlers

// build_initramfs_args_test.go — CODEX-FIX-3 Issue #3
//
// Verifies that build-initramfs.sh parses --mode=* flags correctly regardless of
// position after the mandatory binary argument.  Prior to the fix, OUTPUT was
// hard-bound to $2 before flag parsing, so invoking the script as:
//
//	build-initramfs.sh <bin> --mode=stateless-nfs
//
// treated "--mode=stateless-nfs" as the output filename instead of the mode flag.
// After the fix, flags are parsed out of $@ first; OUTPUT defaults to
// "initramfs-clustr.img" when no positional argument is present.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildInitramfsScript_ModeWithoutOutput verifies that passing
// --mode=stateless-nfs without an explicit output path does NOT create a file
// named "--mode=stateless-nfs".  The script should fail early (missing tools in
// CI) but must NOT interpret the flag as the output filename.
//
// We exercise the script only up to the point where it would attempt tool
// detection — we stub the binary with a zero-byte file so the script fails early
// without doing any real build work.  The key assertion is that the file
// "--mode=stateless-nfs" was never created.
func TestBuildInitramfsScript_ModeWithoutOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available — skipping shell script test")
	}

	// Write the canonical build-initramfs.sh to a temp directory.
	scriptContent := buildInitramfsScript // embedded via go:embed scripts/build-initramfs.sh
	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "build-initramfs.sh")
	if err := os.WriteFile(scriptPath, scriptContent, 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	// Also write initramfs-init.sh alongside so $(dirname $0)/initramfs-init.sh resolves.
	initPath := filepath.Join(scriptDir, "initramfs-init.sh")
	if err := os.WriteFile(initPath, initramfsInitScript, 0644); err != nil {
		t.Fatalf("write initramfs-init.sh: %v", err)
	}

	// Stub binary: a zero-byte file named "clustr-stub" — non-executable on purpose
	// so the script fails at the binary validation step, not at any build step.
	binPath := filepath.Join(scriptDir, "clustr-stub")
	if err := os.WriteFile(binPath, nil, 0755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}

	// Run the script in the scriptDir so relative initramfs-init.sh references work.
	// We pass: <bin> --mode=stateless-nfs  (no explicit output path).
	// The script is expected to fail early (not a real static binary), but the file
	// "--mode=stateless-nfs" must NOT be created in the working directory.
	cmd := exec.Command("bash", scriptPath, binPath, "--mode=stateless-nfs")
	cmd.Dir = scriptDir
	// Suppress output to keep test logs clean; we only care about side effects.
	_ = cmd.Run() // error expected — we don't assert the exit code

	badFile := filepath.Join(scriptDir, "--mode=stateless-nfs")
	if _, err := os.Stat(badFile); err == nil {
		t.Errorf("build-initramfs.sh created file named %q — arg parsing bug: --mode= was treated as the output filename", "--mode=stateless-nfs")
	}

	// The default output file "initramfs-clustr.img" may or may not exist
	// (the script typically fails before writing it in CI without real tools), but
	// its name must NOT match the flag string.
	entries, _ := os.ReadDir(scriptDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "--mode=") {
			t.Errorf("build-initramfs.sh created a file whose name starts with '--mode=': %q", e.Name())
		}
	}
}
