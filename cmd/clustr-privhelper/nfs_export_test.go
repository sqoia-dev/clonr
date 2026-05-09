package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseNFSExportArgs covers argument parsing for the nfs-export verb.
func TestParseNFSExportArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantImageID string
		wantSubnet  string
		wantErr     bool
	}{
		{
			name:        "valid args",
			args:        []string{"--image-id", "6b875781-aaaa-bbbb-cccc-ddddeeeeffff", "--subnet", "10.99.0.0/16"},
			wantImageID: "6b875781-aaaa-bbbb-cccc-ddddeeeeffff",
			wantSubnet:  "10.99.0.0/16",
		},
		{
			name:    "missing image-id",
			args:    []string{"--subnet", "10.99.0.0/16"},
			wantErr: true,
		},
		{
			name:    "missing subnet",
			args:    []string{"--image-id", "6b875781-aaaa-bbbb-cccc-ddddeeeeffff"},
			wantErr: true,
		},
		{
			name:    "unknown flag",
			args:    []string{"--image-id", "6b875781-aaaa-bbbb-cccc-ddddeeeeffff", "--subnet", "10.0.0.0/8", "--extra", "bad"},
			wantErr: true,
		},
		{
			name:    "missing value for image-id",
			args:    []string{"--image-id"},
			wantErr: true,
		},
		{
			name:    "empty args",
			args:    []string{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageID, subnet, err := parseNFSExportArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil (imageID=%q subnet=%q)", imageID, subnet)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if imageID != tt.wantImageID {
				t.Errorf("imageID = %q, want %q", imageID, tt.wantImageID)
			}
			if subnet != tt.wantSubnet {
				t.Errorf("subnet = %q, want %q", subnet, tt.wantSubnet)
			}
		})
	}
}

// TestNFSUUIDRe verifies the UUID regexp used by the nfs-export verb.
func TestNFSUUIDRe(t *testing.T) {
	valid := []string{
		"6b875781-aaaa-bbbb-cccc-ddddeeeeffff",
		"00000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
	}
	invalid := []string{
		"",
		"not-a-uuid",
		"6b875781-AAAA-bbbb-cccc-ddddeeeeffff", // uppercase
		"6b875781-aaaa-bbbb-cccc-ddddeeeefffff", // too long
		"6b875781-aaaa-bbbb-cccc-ddddeeeeffe",   // too short
		"../../etc/passwd",
		"6b875781 aaaa bbbb cccc ddddeeeeffff", // spaces
	}
	for _, v := range valid {
		if !nfsUUIDRe.MatchString(v) {
			t.Errorf("expected valid UUID %q to match, but it did not", v)
		}
	}
	for _, v := range invalid {
		if nfsUUIDRe.MatchString(v) {
			t.Errorf("expected invalid UUID %q not to match, but it did", v)
		}
	}
}

// TestNFSFsidForImageID verifies the deterministic fsid derivation.
func TestNFSFsidForImageID(t *testing.T) {
	imageID := "6b875781-aaaa-bbbb-cccc-ddddeeeeffff"
	// 0x6b875781 = 1804031873; value < 2^32-1 so mod is identity.
	const want uint32 = 1804031873

	got, err := nfsFsidForImageID(imageID)
	if err != nil {
		t.Fatalf("nfsFsidForImageID: %v", err)
	}
	if got != want {
		t.Errorf("nfsFsidForImageID(%q) = %d, want %d", imageID, got, want)
	}

	// Deterministic: calling again returns same value.
	got2, _ := nfsFsidForImageID(imageID)
	if got2 != got {
		t.Errorf("non-deterministic: second call = %d, first = %d", got2, got)
	}
}

// TestBuildNFSExportsContent_Golden verifies rendered output for a fresh file.
func TestBuildNFSExportsContent_Golden(t *testing.T) {
	imageID := "6b875781-aaaa-bbbb-cccc-ddddeeeeffff"
	subnet := "10.99.0.0/16"

	got, err := buildNFSExportsContent("", imageID, subnet)
	if err != nil {
		t.Fatalf("buildNFSExportsContent: %v", err)
	}
	if !strings.Contains(got, nfsAnchorBegin) {
		t.Errorf("missing begin anchor; got:\n%s", got)
	}
	if !strings.Contains(got, nfsAnchorEnd) {
		t.Errorf("missing end anchor; got:\n%s", got)
	}
	wantPath := nfsImagesBase + "/" + imageID + "/rootfs"
	if !strings.Contains(got, wantPath) {
		t.Errorf("missing export path %q; got:\n%s", wantPath, got)
	}
	if !strings.Contains(got, "ro,no_subtree_check,fsid=") {
		t.Errorf("missing export options; got:\n%s", got)
	}
}

// TestBuildNFSExportsContent_Idempotent verifies applying same args twice is idempotent.
func TestBuildNFSExportsContent_Idempotent(t *testing.T) {
	imageID := "6b875781-aaaa-bbbb-cccc-ddddeeeeffff"
	subnet := "10.99.0.0/16"

	first, err := buildNFSExportsContent("", imageID, subnet)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := buildNFSExportsContent(first, imageID, subnet)
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	wantPath := nfsImagesBase + "/" + imageID + "/rootfs"
	count := strings.Count(second, wantPath)
	if count != 1 {
		t.Errorf("expected exactly 1 entry for %s, got %d; content:\n%s", imageID, count, second)
	}
}

// TestBuildNFSExportsContent_PreservesUnrelatedLines ensures operator lines survive.
func TestBuildNFSExportsContent_PreservesUnrelatedLines(t *testing.T) {
	existing := "/mnt/nas 192.168.0.0/24(rw,sync)\n/backup 10.0.0.0/8(ro)\n"
	imageID := "6b875781-aaaa-bbbb-cccc-ddddeeeeffff"
	subnet := "10.99.0.0/16"

	got, err := buildNFSExportsContent(existing, imageID, subnet)
	if err != nil {
		t.Fatalf("buildNFSExportsContent: %v", err)
	}
	if !strings.Contains(got, "/mnt/nas") {
		t.Errorf("unrelated line /mnt/nas removed; got:\n%s", got)
	}
	if !strings.Contains(got, "/backup") {
		t.Errorf("unrelated line /backup removed; got:\n%s", got)
	}
}

// TestExtractNFSImageID covers the line-parser helper.
func TestExtractNFSImageID(t *testing.T) {
	imageID := "6b875781-aaaa-bbbb-cccc-ddddeeeeffff"
	line := nfsImagesBase + "/" + imageID + "/rootfs 10.99.0.0/16(ro,no_subtree_check,fsid=1804031873)"

	got := extractNFSImageID(line)
	if got != imageID {
		t.Errorf("extractNFSImageID(%q) = %q, want %q", line, got, imageID)
	}

	// Non-clustr line returns "".
	if got2 := extractNFSImageID("/mnt/nas 10.0.0.0/8(rw)"); got2 != "" {
		t.Errorf("expected empty for non-clustr line, got %q", got2)
	}

	// Path traversal attempt returns "".
	traversal := nfsImagesBase + "/../../etc/passwd/rootfs 10.0.0.0/8(ro)"
	if got3 := extractNFSImageID(traversal); got3 != "" {
		t.Errorf("expected empty for traversal line, got %q", got3)
	}
}

// TestVerbNFSExport_MissingRootfs verifies the verb rejects a missing rootfs
// directory with exit code 1 and does not attempt to modify /etc/exports.
// We override nfsImagesBase by pointing it to a temp dir.
func TestVerbNFSExport_MissingRootfs(t *testing.T) {
	// Save and restore nfsImagesBase constant cannot be overridden at runtime;
	// we call verbNFSExport with an image ID whose rootfs doesn't exist under
	// the real nfsImagesBase — which is always true in CI since no real images
	// are present.  Exit code must be 1.
	imageID := "6b875781-aaaa-bbbb-cccc-ddddeeeeffff"
	subnet := "10.99.0.0/16"
	// Make sure the rootfs really doesn't exist.
	if _, err := os.Stat(nfsImagesBase + "/" + imageID + "/rootfs"); err == nil {
		t.Skip("rootfs directory unexpectedly exists — skipping to avoid side effects")
	}
	code := verbNFSExport(os.Getuid(), []string{"--image-id", imageID, "--subnet", subnet})
	if code != 1 {
		t.Errorf("expected exit code 1 for missing rootfs, got %d", code)
	}
}

// TestVerbNFSExport_InvalidUUID verifies the verb rejects a non-UUID image-id.
func TestVerbNFSExport_InvalidUUID(t *testing.T) {
	code := verbNFSExport(os.Getuid(), []string{"--image-id", "not-a-uuid", "--subnet", "10.99.0.0/16"})
	if code != 1 {
		t.Errorf("expected exit code 1 for invalid UUID, got %d", code)
	}
}

// TestVerbNFSExport_InvalidSubnet verifies the verb rejects an invalid CIDR.
func TestVerbNFSExport_InvalidSubnet(t *testing.T) {
	code := verbNFSExport(os.Getuid(), []string{"--image-id", "6b875781-aaaa-bbbb-cccc-ddddeeeeffff", "--subnet", "999.999.999.999/99"})
	if code != 1 {
		t.Errorf("expected exit code 1 for invalid subnet, got %d", code)
	}
}

// TestVerbNFSExport_MissingArgs verifies the verb rejects empty arg list.
func TestVerbNFSExport_MissingArgs(t *testing.T) {
	code := verbNFSExport(os.Getuid(), []string{})
	if code != 1 {
		t.Errorf("expected exit code 1 for empty args, got %d", code)
	}
}

// makeNFSTestRootfs creates a temporary directory tree and returns the temp
// directory path to use as nfsImagesBase for isolation.
func makeNFSTestRootfs(t *testing.T, imageID string) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, imageID, "rootfs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("makeNFSTestRootfs: %v", err)
	}
	return base
}
