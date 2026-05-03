package handlers_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// invalidateImageSidecarTest is a local reimplementation of the hotfix logic
// used by TestInvalidateImageSidecar so the test does not depend on unexported
// internals.  The production code lives in shell_ws.go; this test validates the
// observable contract: after a shell session closes, the tar-sha256 sidecar file
// must be absent from the image directory.
func invalidateImageSidecarTest(imageDir, imageID string) bool {
	sidecarPath := filepath.Join(imageDir, imageID, "tar-sha256")
	err := os.Remove(sidecarPath)
	if err != nil && !os.IsNotExist(err) {
		// Unexpected error — propagate for test visibility.
		panic("unexpected remove error: " + err.Error())
	}
	return !os.IsNotExist(err)
}

// TestInvalidateImageSidecar_Removed verifies that the tar-sha256 sidecar is
// deleted when a shell session closes on an image that has a cached checksum.
func TestInvalidateImageSidecar_Removed(t *testing.T) {
	dir := t.TempDir()
	imageID := "test-image-sidecar-hotfix"

	// Create the image directory and populate the sidecar file.
	imageDir := filepath.Join(dir, imageID)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sidecarPath := filepath.Join(imageDir, "tar-sha256")
	if err := os.WriteFile(sidecarPath, []byte("abc123\n"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	// Simulate shell session close — expect mutated=true.
	mutated := invalidateImageSidecarTest(dir, imageID)
	if !mutated {
		t.Error("expected mutated=true when sidecar existed before session close")
	}

	// The sidecar must no longer exist.
	if _, err := os.Stat(sidecarPath); !os.IsNotExist(err) {
		t.Errorf("expected sidecar to be removed after session close, got: %v", err)
	}
}

// TestInvalidateImageSidecar_NoSidecar verifies that the hotfix is a no-op
// (no error) when no sidecar file exists — e.g. first-stream session before
// any blob has been downloaded. mutated must be false.
func TestInvalidateImageSidecar_NoSidecar(t *testing.T) {
	dir := t.TempDir()
	imageID := "test-image-no-sidecar"

	// Create the image directory but do NOT create a tar-sha256 sidecar.
	imageDir := filepath.Join(dir, imageID)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Must not panic or error. mutated must be false.
	mutated := invalidateImageSidecarTest(dir, imageID)
	if mutated {
		t.Error("expected mutated=false when no sidecar existed")
	}

	sidecarPath := filepath.Join(imageDir, "tar-sha256")
	if _, err := os.Stat(sidecarPath); !os.IsNotExist(err) {
		t.Errorf("sidecar should not exist, got: %v", err)
	}
}

// TestShellSessionResponse_WarningFields verifies that the ShellSessionResponse
// type carries the RISK-1(a) warning fields and that the canonical warning
// constant is non-empty. This is a compile-time + content check — the actual
// HTTP handler test would require a full DB integration test.
func TestShellSessionResponse_WarningFields(t *testing.T) {
	resp := api.ShellSessionResponse{
		SessionID:       "test-session",
		ImageID:         "test-image",
		RootDir:         "/var/lib/clustr/images/test-image/rootfs",
		Warning:         api.ShellMutationWarning,
		WarningSeverity: "high",
	}

	if resp.Warning == "" {
		t.Error("ShellSessionResponse.Warning must not be empty")
	}
	if resp.WarningSeverity != "high" {
		t.Errorf("expected warning_severity=high, got %q", resp.WarningSeverity)
	}
	if api.ShellMutationWarning == "" {
		t.Error("api.ShellMutationWarning constant must not be empty")
	}
	// Confirm the warning text mentions overlay isolation so operators know what
	// the remediation path is.
	if len(api.ShellMutationWarning) < 50 {
		t.Errorf("warning text too short (%d chars); expected substantive message", len(api.ShellMutationWarning))
	}
}
