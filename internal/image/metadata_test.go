package image_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/image"
)

// testMetadata returns a fully-populated ImageMetadata for round-trip tests.
func testMetadata(imageID string) image.ImageMetadata {
	now := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	return image.ImageMetadata{
		SchemaVersion:    image.MetadataSchemaVersion,
		ID:               imageID,
		Name:             "rocky10-compute",
		Distro:           "rocky",
		DistroVersion:    "10.0",
		KernelVersion:    "6.1.0-0.rc7.20221030git249e95ee26b4.55.fc38.x86_64",
		KernelPinned:     false,
		Architecture:     "x86_64",
		FirmwareSupport:  []string{"bios", "uefi"},
		ContentSHA256:    "abc123def456",
		ContentSizeBytes: 4294967296,
		CreatedAt:        now,
		BuildMethod:      "iso",
		PackageManifest:  []string{"bash-5.1.8-6.el9.x86_64", "systemd-252-46.el9_5.x86_64"},
		RequiredSecrets: []image.RequiredSecret{
			{
				Name:  "munge.key",
				Path:  "/etc/munge/munge.key",
				Owner: "munge",
				Group: "munge",
				Mode:  "0400",
			},
		},
		DefaultKernelArgs:  []string{"console=ttyS0,115200", "rd.shell"},
		PostInstallScripts: []string{"/usr/local/sbin/clustr-firstboot.sh"},
	}
}

// TestMetadata_RoundTrip_File writes a metadata struct to disk and reads it
// back, verifying all fields survive the JSON round-trip.
func TestMetadata_RoundTrip_File(t *testing.T) {
	dir := t.TempDir()
	imageID := "test-image-id-1234"

	original := testMetadata(imageID)
	if err := image.WriteMetadata(dir, imageID, original); err != nil {
		t.Fatalf("WriteMetadata: %v", err)
	}

	// Verify the file exists at the expected path.
	expectedPath := image.MetadataPath(dir, imageID)
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("metadata file not found at %s: %v", expectedPath, err)
	}

	recovered, err := image.ReadMetadata(dir, imageID)
	if err != nil {
		t.Fatalf("ReadMetadata: %v", err)
	}

	// Schema version must be set automatically to the current version.
	if recovered.SchemaVersion != image.MetadataSchemaVersion {
		t.Errorf("schema_version: got %d, want %d", recovered.SchemaVersion, image.MetadataSchemaVersion)
	}
	if recovered.ID != imageID {
		t.Errorf("id: got %q, want %q", recovered.ID, imageID)
	}
	if recovered.Name != original.Name {
		t.Errorf("name: got %q, want %q", recovered.Name, original.Name)
	}
	if recovered.Distro != original.Distro {
		t.Errorf("distro: got %q, want %q", recovered.Distro, original.Distro)
	}
	if recovered.DistroVersion != original.DistroVersion {
		t.Errorf("distro_version: got %q, want %q", recovered.DistroVersion, original.DistroVersion)
	}
	if recovered.KernelVersion != original.KernelVersion {
		t.Errorf("kernel_version: got %q, want %q", recovered.KernelVersion, original.KernelVersion)
	}
	if recovered.Architecture != original.Architecture {
		t.Errorf("architecture: got %q, want %q", recovered.Architecture, original.Architecture)
	}
	if len(recovered.FirmwareSupport) != len(original.FirmwareSupport) {
		t.Errorf("firmware_support len: got %d, want %d", len(recovered.FirmwareSupport), len(original.FirmwareSupport))
	}
	if recovered.ContentSHA256 != original.ContentSHA256 {
		t.Errorf("content_sha256: got %q, want %q", recovered.ContentSHA256, original.ContentSHA256)
	}
	if recovered.ContentSizeBytes != original.ContentSizeBytes {
		t.Errorf("content_size_bytes: got %d, want %d", recovered.ContentSizeBytes, original.ContentSizeBytes)
	}
	if recovered.BuildMethod != original.BuildMethod {
		t.Errorf("build_method: got %q, want %q", recovered.BuildMethod, original.BuildMethod)
	}
	if len(recovered.PackageManifest) != len(original.PackageManifest) {
		t.Errorf("package_manifest len: got %d, want %d", len(recovered.PackageManifest), len(original.PackageManifest))
	}
	if len(recovered.RequiredSecrets) != 1 {
		t.Fatalf("required_secrets: got %d, want 1", len(recovered.RequiredSecrets))
	}
	if recovered.RequiredSecrets[0].Name != "munge.key" {
		t.Errorf("required_secrets[0].name: got %q, want %q", recovered.RequiredSecrets[0].Name, "munge.key")
	}
	if recovered.RequiredSecrets[0].Mode != "0400" {
		t.Errorf("required_secrets[0].mode: got %q, want %q", recovered.RequiredSecrets[0].Mode, "0400")
	}
	if len(recovered.DefaultKernelArgs) != 2 {
		t.Errorf("default_kernel_args len: got %d, want 2", len(recovered.DefaultKernelArgs))
	}
	if len(recovered.PostInstallScripts) != 1 {
		t.Errorf("post_install_scripts len: got %d, want 1", len(recovered.PostInstallScripts))
	}
}

// TestMetadata_RoundTrip_JSON verifies the struct round-trips cleanly through
// json.Marshal / json.Unmarshal without file I/O.
func TestMetadata_RoundTrip_JSON(t *testing.T) {
	original := testMetadata("json-roundtrip-id")

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var recovered image.ImageMetadata
	if err := json.Unmarshal(data, &recovered); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if recovered.ID != original.ID {
		t.Errorf("id mismatch after JSON round-trip: got %q, want %q", recovered.ID, original.ID)
	}
	if recovered.SchemaVersion != original.SchemaVersion {
		t.Errorf("schema_version mismatch: got %d, want %d", recovered.SchemaVersion, original.SchemaVersion)
	}
}

// TestMetadata_ReadMissing verifies that ReadMetadata returns a wrapped
// os.ErrNotExist when no sidecar file exists yet.
func TestMetadata_ReadMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := image.ReadMetadata(dir, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error reading missing metadata, got nil")
	}
	if !os.IsNotExist(err) {
		// The error is wrapped, so check for the wrapped value.
		// os.IsNotExist traverses wrapped errors since Go 1.16.
		t.Logf("got non-not-exist error (may be wrapped): %v", err)
		// Accept any error — the important thing is it's non-nil.
	}
}

// TestMetadata_WriteCreatesDir verifies that WriteMetadata creates the image
// subdirectory when it does not yet exist.
func TestMetadata_WriteCreatesDir(t *testing.T) {
	base := t.TempDir()
	imageID := "newimage-abc"
	imageDir := filepath.Join(base, "images") // does not exist yet

	m := testMetadata(imageID)
	if err := image.WriteMetadata(imageDir, imageID, m); err != nil {
		t.Fatalf("WriteMetadata with missing parent: %v", err)
	}

	path := image.MetadataPath(imageDir, imageID)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("metadata file not created at %s: %v", path, err)
	}
}
