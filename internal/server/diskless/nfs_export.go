// Package diskless provides helpers for the stateless/diskless boot path.
//
// The NFS export plumbing in this file manages read-only NFSv4 exports of
// rootfs directories so stateless_nfs nodes can mount their root over the
// network at PXE time.
//
// # Architecture
//
// clustr-serverd calls EnsureExport (as the unprivileged "clustr" user) which
// delegates the actual file-and-exportfs work to clustr-privhelper via the
// nfs-export verb.  The privileged binary owns /etc/exports and runs
// `exportfs -ra`; clustr-serverd never writes /etc/exports directly.
//
// # /etc/exports anchor format
//
// All clustr-managed entries are wrapped in a clearly marked block so unrelated
// operator-added entries are never touched:
//
//	# BEGIN clustr-managed NFS exports — do not edit between these lines
//	/var/lib/clustr/images/<id>/rootfs 10.0.0.0/8(ro,no_subtree_check,fsid=3735928559)
//	# END clustr-managed NFS exports
//
// The block is idempotent: applying the same (imageID, subnet) pair twice
// leaves only one entry in the block.
//
// # fsid allocation
//
// exportfs requires a unique uint32 fsid per exported path (when the path is
// not on a dedicated filesystem). We derive the fsid deterministically from
// the first 8 hex characters of the image UUID parsed as a uint32 mod (2³²−1):
//
//	imageID = "6b875781-aaaa-bbbb-cccc-ddddeeeeeeee"
//	first8  = "6b875781"  →  uint32(0x6b875781) = 1804092289
//
// Two installs of clustr sharing an NFS server will get the same fsid for the
// same image UUID, which is correct: they would be exporting the identical
// rootfs from the same path.  Collisions between distinct images are possible
// in theory but require the first 32 bits of two different UUIDs to be
// identical — astronomically unlikely for real UUIDs.
package diskless

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	// imagesBase is where clustr stores image root filesystems on the host.
	imagesBase = "/var/lib/clustr/images"

	// anchorBegin and anchorEnd bracket the clustr-managed block in /etc/exports.
	anchorBegin = "# BEGIN clustr-managed NFS exports — do not edit between these lines"
	anchorEnd   = "# END clustr-managed NFS exports"
)

// uuidRe validates an image ID (36-char lowercase hex UUID).
var uuidRe = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// NFSExporter dispatches the nfs-export privhelper verb.
// Tests can inject a stub here so they never need to shell out.
type NFSExporter interface {
	// NfsExport calls the privhelper nfs-export verb with the given imageID and subnet.
	NfsExport(ctx context.Context, imageID, subnet string) error
}

// RealExporter calls clustr-privhelper nfs-export via execPrivhelper.
// Used in production; set as the default in NewExportManager.
type RealExporter struct{}

// NfsExport calls the privhelper directly via os/exec.
// The actual implementation lives in internal/privhelper/privhelper.go;
// we use a thin wrapper here so diskless tests can inject stubs without
// pulling in the privhelper package (which expects /usr/sbin/clustr-privhelper
// on-disk).
func (r *RealExporter) NfsExport(ctx context.Context, imageID, subnet string) error {
	return nfsExportViaPrivhelper(ctx, imageID, subnet)
}

// ExportManager manages the NFS export lifecycle for diskless images.
type ExportManager struct {
	exporter NFSExporter
	// imagesBase is overridden in tests.
	imagesBase string
}

// NewExportManager returns a production ExportManager wired to the real privhelper.
func NewExportManager() *ExportManager {
	return &ExportManager{
		exporter:   &RealExporter{},
		imagesBase: imagesBase,
	}
}

// newExportManagerWithExporter returns an ExportManager with a stub exporter.
// Used by tests.
func newExportManagerWithExporter(e NFSExporter, base string) *ExportManager {
	return &ExportManager{exporter: e, imagesBase: base}
}

// EnsureExport idempotently exports the rootfs for imageID read-only to subnet.
//
// Validation (mirrors what the privhelper also checks so the caller gets a
// clear error before forking the helper):
//   - imageID must match the UUID regexp
//   - subnet must parse as a valid CIDR (e.g. "10.99.0.0/16")
//   - the rootfs directory must exist on disk
//
// The privhelper writes /etc/exports (anchored block) and runs exportfs -ra.
func (m *ExportManager) EnsureExport(ctx context.Context, imageID, subnet string) error {
	if err := validateExportInputs(imageID, subnet, m.imagesBase); err != nil {
		return fmt.Errorf("diskless: EnsureExport validate: %w", err)
	}
	if err := m.exporter.NfsExport(ctx, imageID, subnet); err != nil {
		return fmt.Errorf("diskless: EnsureExport privhelper: %w", err)
	}
	return nil
}

// validateExportInputs checks imageID, subnet, and rootfs directory existence.
func validateExportInputs(imageID, subnet, base string) error {
	if !uuidRe.MatchString(imageID) {
		return fmt.Errorf("imageID %q does not match UUID pattern", imageID)
	}
	if _, _, err := net.ParseCIDR(subnet); err != nil {
		return fmt.Errorf("subnet %q is not a valid CIDR: %w", subnet, err)
	}
	rootfsPath := base + "/" + imageID + "/rootfs"
	if _, err := os.Stat(rootfsPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("rootfs directory does not exist: %s", rootfsPath)
		}
		return fmt.Errorf("stat rootfs directory %s: %w", rootfsPath, err)
	}
	return nil
}

// BuildExportsContent renders the full /etc/exports content by merging a
// clustr-managed entry for (imageID, subnet) into existing content.
//
// Rules:
//  1. Lines outside the anchor block are preserved verbatim.
//  2. The managed block is replaced wholesale with the union of all existing
//     managed entries plus the new (imageID, subnet) entry — duplicates (same
//     imageID+subnet pair) are collapsed to one line.
//  3. If no anchor block exists yet, it is appended after a blank line.
//
// BuildExportsContent is package-level (not a method) so tests can call it
// directly without constructing a full ExportManager.
func BuildExportsContent(existing, imageID, subnet string) (string, error) {
	if !uuidRe.MatchString(imageID) {
		return "", fmt.Errorf("imageID %q does not match UUID pattern", imageID)
	}
	if _, _, err := net.ParseCIDR(subnet); err != nil {
		return "", fmt.Errorf("subnet %q is not a valid CIDR: %w", subnet, err)
	}

	fsid, err := fsidForImageID(imageID)
	if err != nil {
		return "", fmt.Errorf("compute fsid: %w", err)
	}

	newLine := fmt.Sprintf("/var/lib/clustr/images/%s/rootfs %s(ro,no_subtree_check,fsid=%d)",
		imageID, subnet, fsid)

	// Parse existing content into three regions:
	//   before — lines before the begin anchor (or all lines if no block)
	//   managed — lines currently inside the block
	//   after  — lines after the end anchor
	var before, managed, after []string
	inBlock := false
	blockFound := false
	for _, line := range strings.Split(existing, "\n") {
		switch {
		case line == anchorBegin:
			inBlock = true
			blockFound = true
		case line == anchorEnd:
			inBlock = false
		case inBlock:
			// Existing managed entry: keep unless it is the exact same path
			// (imageID match), in which case we will replace it with the new line.
			managedImageID := extractImageIDFromExportLine(line)
			if managedImageID == imageID {
				// Same image, different or same subnet: drop old entry; new one wins.
				continue
			}
			managed = append(managed, line)
		default:
			if !blockFound && !inBlock {
				before = append(before, line)
			} else if blockFound && !inBlock {
				after = append(after, line)
			} else {
				before = append(before, line)
			}
		}
	}

	// Add the new entry.
	managed = append(managed, newLine)

	// Reconstruct.
	// Trim trailing blank lines from before to avoid double-blanks.
	for len(before) > 0 && strings.TrimSpace(before[len(before)-1]) == "" {
		before = before[:len(before)-1]
	}

	var sb strings.Builder
	if len(before) > 0 {
		sb.WriteString(strings.Join(before, "\n"))
		sb.WriteString("\n")
	}
	// Blank separator before the block.
	sb.WriteString("\n")
	sb.WriteString(anchorBegin)
	sb.WriteString("\n")
	for _, l := range managed {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	sb.WriteString(anchorEnd)
	sb.WriteString("\n")
	if len(after) > 0 {
		for _, l := range after {
			if strings.TrimSpace(l) == "" {
				continue // collapse extra blank lines after block
			}
			sb.WriteString(l)
			sb.WriteString("\n")
		}
	}

	return sb.String(), nil
}

// fsidForImageID derives a deterministic uint32 fsid from the first 8 hex
// characters of the UUID.
//
// Derivation: imageID[0:8] parsed as hex uint64, then modulo (2³²−1).
// Using mod (2³²−1) instead of (2³²) avoids the reserved value 0xFFFFFFFF
// while keeping the full 32-bit range usable.
//
// Example:
//
//	imageID = "6b875781-…"   →   first8 = "6b875781"   →   0x6b875781 = 1804031873
//
// exportfs(8) treats fsid=0 as the root export and fsid values must be unique
// per exported path when that path is not on a dedicated mount point.  This
// function never returns 0 (would require imageID starting with "00000000")
// but callers should not rely on that guarantee.
func fsidForImageID(imageID string) (uint32, error) {
	if len(imageID) < 8 {
		return 0, fmt.Errorf("imageID too short: %q", imageID)
	}
	first8 := imageID[:8]
	// Remove hyphens if the UUID starts with 8 chars before the first hyphen.
	// Standard UUID: "6b875781-aaaa-..." — first 8 chars are hex digits.
	first8 = strings.ReplaceAll(first8, "-", "")
	if len(first8) < 8 {
		return 0, fmt.Errorf("imageID first 8 chars after strip are not pure hex: %q", imageID[:8])
	}
	v, err := strconv.ParseUint(first8[:8], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("parse first8 %q as hex: %w", first8[:8], err)
	}
	const maxFSID = uint64(^uint32(0)) // 2³²−1 = 4294967295
	return uint32(v % maxFSID), nil
}

// extractImageIDFromExportLine extracts the image UUID from a clustr-managed
// /etc/exports line. Returns "" if the line is not a clustr-managed line.
//
// Expected format:
//
//	/var/lib/clustr/images/<uuid>/rootfs <subnet>(...)
func extractImageIDFromExportLine(line string) string {
	const prefix = "/var/lib/clustr/images/"
	const suffix = "/rootfs"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	rest := line[len(prefix):]
	idx := strings.Index(rest, suffix)
	if idx < 0 {
		return ""
	}
	candidate := rest[:idx]
	if uuidRe.MatchString(candidate) {
		return candidate
	}
	return ""
}
