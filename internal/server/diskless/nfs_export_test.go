package diskless

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubExporter records calls without actually invoking the privhelper.
type stubExporter struct {
	calls []stubCall
}

type stubCall struct {
	imageID string
	subnet  string
}

func (s *stubExporter) NfsExport(_ context.Context, imageID, subnet string) error {
	s.calls = append(s.calls, stubCall{imageID, subnet})
	return nil
}

// makeRootfs creates a temporary directory structure that looks like a
// valid clustr image rootfs so validateExportInputs passes the stat check.
func makeRootfs(t *testing.T, base, imageID string) {
	t.Helper()
	rootfs := filepath.Join(base, imageID, "rootfs")
	if err := os.MkdirAll(rootfs, 0755); err != nil {
		t.Fatalf("makeRootfs: %v", err)
	}
}

const testImageID = "6b875781-aaaa-bbbb-cccc-ddddeeeeffff"
const testSubnet = "10.99.0.0/16"

// TestBuildExportsContent_Golden verifies the rendered /etc/exports content
// for a fresh file (no existing content).
func TestBuildExportsContent_Golden(t *testing.T) {
	got, err := BuildExportsContent("", testImageID, testSubnet)
	if err != nil {
		t.Fatalf("BuildExportsContent: %v", err)
	}

	// Must contain the anchor markers.
	if !strings.Contains(got, anchorBegin) {
		t.Errorf("missing begin anchor; got:\n%s", got)
	}
	if !strings.Contains(got, anchorEnd) {
		t.Errorf("missing end anchor; got:\n%s", got)
	}

	// Must contain the expected export line.
	wantPath := "/var/lib/clustr/images/" + testImageID + "/rootfs"
	if !strings.Contains(got, wantPath) {
		t.Errorf("missing export path %q; got:\n%s", wantPath, got)
	}
	if !strings.Contains(got, testSubnet) {
		t.Errorf("missing subnet %q; got:\n%s", testSubnet, got)
	}

	// Export options must be ro (read-only), no_subtree_check.
	if !strings.Contains(got, "ro,no_subtree_check") {
		t.Errorf("missing ro,no_subtree_check in options; got:\n%s", got)
	}

	// fsid must be present.
	if !strings.Contains(got, "fsid=") {
		t.Errorf("missing fsid= in options; got:\n%s", got)
	}
}

// TestBuildExportsContent_Idempotent verifies that applying the same
// (imageID, subnet) pair twice results in exactly one managed entry.
func TestBuildExportsContent_Idempotent(t *testing.T) {
	first, err := BuildExportsContent("", testImageID, testSubnet)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	second, err := BuildExportsContent(first, testImageID, testSubnet)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}

	// Count occurrences of the export path — must be exactly one.
	wantPath := "/var/lib/clustr/images/" + testImageID + "/rootfs"
	count := strings.Count(second, wantPath)
	if count != 1 {
		t.Errorf("expected exactly one managed entry for %s, got %d; content:\n%s",
			testImageID, count, second)
	}
}

// TestBuildExportsContent_DoesNotEatUnrelatedLines verifies that clustr's
// anchor block does not remove lines that exist outside the anchors.
func TestBuildExportsContent_DoesNotEatUnrelatedLines(t *testing.T) {
	existing := `/exports/nfs/shared 192.168.0.0/24(rw,sync)
/exports/nfs/backup 10.0.0.0/8(ro)
`
	got, err := BuildExportsContent(existing, testImageID, testSubnet)
	if err != nil {
		t.Fatalf("BuildExportsContent: %v", err)
	}

	// Unrelated lines must survive.
	if !strings.Contains(got, "/exports/nfs/shared") {
		t.Errorf("unrelated line /exports/nfs/shared was removed; got:\n%s", got)
	}
	if !strings.Contains(got, "/exports/nfs/backup") {
		t.Errorf("unrelated line /exports/nfs/backup was removed; got:\n%s", got)
	}

	// Clustr anchor block must be present.
	if !strings.Contains(got, anchorBegin) {
		t.Errorf("missing begin anchor; got:\n%s", got)
	}

	// The new clustr entry must also be present.
	wantPath := "/var/lib/clustr/images/" + testImageID + "/rootfs"
	if !strings.Contains(got, wantPath) {
		t.Errorf("missing clustr export line; got:\n%s", got)
	}
}

// TestBuildExportsContent_TwoImages verifies that two different images each
// get their own entry in the managed block, with distinct fsid values.
func TestBuildExportsContent_TwoImages(t *testing.T) {
	imageID2 := "aabbccdd-1111-2222-3333-444455556666"
	first, err := BuildExportsContent("", testImageID, testSubnet)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := BuildExportsContent(first, imageID2, testSubnet)
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if !strings.Contains(second, testImageID) {
		t.Errorf("first imageID missing after second apply; got:\n%s", second)
	}
	if !strings.Contains(second, imageID2) {
		t.Errorf("second imageID missing; got:\n%s", second)
	}
}

// TestBuildExportsContent_InvalidImageID verifies input validation.
func TestBuildExportsContent_InvalidImageID(t *testing.T) {
	_, err := BuildExportsContent("", "not-a-uuid", testSubnet)
	if err == nil {
		t.Errorf("expected error for invalid imageID, got nil")
	}
}

// TestBuildExportsContent_InvalidSubnet verifies input validation.
func TestBuildExportsContent_InvalidSubnet(t *testing.T) {
	_, err := BuildExportsContent("", testImageID, "not-a-cidr")
	if err == nil {
		t.Errorf("expected error for invalid subnet, got nil")
	}
}

// TestEnsureExport_ValidInputs verifies that EnsureExport dispatches to the
// exporter when inputs are valid and the rootfs dir exists.
func TestEnsureExport_ValidInputs(t *testing.T) {
	base := t.TempDir()
	makeRootfs(t, base, testImageID)

	stub := &stubExporter{}
	m := newExportManagerWithExporter(stub, base)

	if err := m.EnsureExport(t.Context(), testImageID, testSubnet); err != nil {
		t.Fatalf("EnsureExport: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 privhelper call, got %d", len(stub.calls))
	}
	if stub.calls[0].imageID != testImageID {
		t.Errorf("wrong imageID: got %q, want %q", stub.calls[0].imageID, testImageID)
	}
	if stub.calls[0].subnet != testSubnet {
		t.Errorf("wrong subnet: got %q, want %q", stub.calls[0].subnet, testSubnet)
	}
}

// TestEnsureExport_InvalidImageID verifies pre-validation before the privhelper call.
func TestEnsureExport_InvalidImageID(t *testing.T) {
	base := t.TempDir()
	stub := &stubExporter{}
	m := newExportManagerWithExporter(stub, base)

	err := m.EnsureExport(t.Context(), "bad-id", testSubnet)
	if err == nil {
		t.Errorf("expected error for invalid imageID, got nil")
	}
	if len(stub.calls) != 0 {
		t.Errorf("privhelper should not be called on validation failure; got %d calls", len(stub.calls))
	}
}

// TestEnsureExport_InvalidSubnet verifies CIDR pre-validation.
func TestEnsureExport_InvalidSubnet(t *testing.T) {
	base := t.TempDir()
	makeRootfs(t, base, testImageID)
	stub := &stubExporter{}
	m := newExportManagerWithExporter(stub, base)

	err := m.EnsureExport(t.Context(), testImageID, "999.999.999.999/33")
	if err == nil {
		t.Errorf("expected error for invalid subnet, got nil")
	}
	if len(stub.calls) != 0 {
		t.Errorf("privhelper should not be called on validation failure; got %d calls", len(stub.calls))
	}
}

// TestEnsureExport_MissingRootfs verifies that a missing rootfs directory
// causes EnsureExport to return an error before calling the privhelper.
func TestEnsureExport_MissingRootfs(t *testing.T) {
	base := t.TempDir()
	// Deliberately NOT creating the rootfs directory.
	stub := &stubExporter{}
	m := newExportManagerWithExporter(stub, base)

	err := m.EnsureExport(t.Context(), testImageID, testSubnet)
	if err == nil {
		t.Errorf("expected error for missing rootfs dir, got nil")
	}
	if len(stub.calls) != 0 {
		t.Errorf("privhelper should not be called when rootfs is missing; got %d calls", len(stub.calls))
	}
}

// TestFsidForImageID_Deterministic verifies the same input always yields the
// same fsid.
func TestFsidForImageID_Deterministic(t *testing.T) {
	got1, err := fsidForImageID(testImageID)
	if err != nil {
		t.Fatalf("fsidForImageID: %v", err)
	}
	got2, err := fsidForImageID(testImageID)
	if err != nil {
		t.Fatalf("fsidForImageID second call: %v", err)
	}
	if got1 != got2 {
		t.Errorf("non-deterministic: %d != %d", got1, got2)
	}
}

// TestFsidForImageID_KnownValue verifies a known expected fsid to catch
// accidental algorithm changes.
//
// testImageID = "6b875781-aaaa-bbbb-cccc-ddddeeeeffff"
// first8      = "6b875781"
// 0x6b875781  = 1804031873
// 1804031873 % 4294967295 = 1804031873  (value is < 2^32-1, so no reduction)
func TestFsidForImageID_KnownValue(t *testing.T) {
	got, err := fsidForImageID(testImageID)
	if err != nil {
		t.Fatalf("fsidForImageID: %v", err)
	}
	const want uint32 = 1804031873 // 0x6b875781
	if got != want {
		t.Errorf("fsidForImageID(%q) = %d, want %d", testImageID, got, want)
	}
}

// TestExtractImageIDFromExportLine covers the line parser.
func TestExtractImageIDFromExportLine(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{
			line: "/var/lib/clustr/images/" + testImageID + "/rootfs 10.99.0.0/16(ro,no_subtree_check,fsid=1804031873)",
			want: testImageID,
		},
		{
			line: "/exports/nfs/shared 192.168.0.0/24(rw,sync)",
			want: "",
		},
		{
			line: anchorBegin,
			want: "",
		},
		{
			line: "",
			want: "",
		},
	}
	for _, tt := range tests {
		got := extractImageIDFromExportLine(tt.line)
		if got != tt.want {
			t.Errorf("extractImageIDFromExportLine(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}
