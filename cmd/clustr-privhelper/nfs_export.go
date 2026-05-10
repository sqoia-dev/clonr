// nfs_export.go — clustr-privhelper nfs-export verb.
//
// # Verb: nfs-export
//
// Idempotently writes a read-only NFSv4 export entry for a clustr image
// rootfs directory into /etc/exports, then runs `exportfs -ra` to reload
// the NFS daemon's export table.
//
// # Usage
//
//	clustr-privhelper nfs-export --image-id <uuid> --subnet <cidr>
//
// # Security model
//
//   - imageID is validated against ^[a-f0-9-]{36}$ (standard UUID form) before
//     any file I/O. The validated value is used to construct a fixed path under
//     /var/lib/clustr/images/ — no user-controlled string is ever interpolated
//     into an exec argv or shell command.
//   - subnet is parsed by net.ParseCIDR; invalid CIDRs are rejected.
//   - The rootfs directory must exist; we stat() it before modifying /etc/exports.
//   - /etc/exports is written atomically: content is assembled in memory, written
//     to /etc/exports.tmp, then renamed over /etc/exports. A partial write never
//     leaves /etc/exports in a broken state.
//   - exportfs -ra is invoked with a fixed, caller-independent argv.
//
// # /etc/exports anchor format
//
// Clustr-managed entries are wrapped in sentinel comments that allow safe
// idempotent updates without touching unrelated operator-added lines:
//
//	# BEGIN clustr-managed NFS exports — do not edit between these lines
//	/var/lib/clustr/images/<uuid>/rootfs <cidr>(ro,no_subtree_check,fsid=N)
//	# END clustr-managed NFS exports
//
// # fsid derivation
//
// exportfs(8) requires a unique uint32 fsid per exported path when the path
// shares a filesystem with other exports. We derive the value deterministically
// from the first 8 hex characters of the UUID parsed as uint32 modulo (2³²−1):
//
//	"6b875781-..." → 0x6b875781 = 1804031873
//
// This ensures two clustr instances sharing a server always agree on the fsid
// for the same image UUID, and avoids the reserved exportfs value 0xFFFFFFFF.
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// nfsExportsPath is the system NFS configuration file.
const nfsExportsPath = "/etc/exports"

// nfsImagesBase is where clustr stores image root filesystems.
const nfsImagesBase = "/var/lib/clustr/images"

// nfsAnchorBegin and nfsAnchorEnd mark the clustr-managed block.
const nfsAnchorBegin = "# BEGIN clustr-managed NFS exports — do not edit between these lines"
const nfsAnchorEnd = "# END clustr-managed NFS exports"

// nfsUUIDRe validates the image-id argument (standard lowercase UUID form).
var nfsUUIDRe = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// verbNFSExport implements the nfs-export verb.
//
// Args accepted via flag-style parsing (no positional args):
//
//	--image-id <uuid>   (required)
//	--subnet <cidr>     (required)
//
// All other arguments are rejected.  The helper rebuilds argv for exportfs
// internally — no caller-supplied shell strings reach exec.
func verbNFSExport(callerUID int, args []string) int {
	imageID, subnet, err := parseNFSExportArgs(args)
	if err != nil {
		msg := fmt.Sprintf("nfs-export: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "nfs-export", args, 1, msg)
		return 1
	}

	// 1. Validate imageID — must match UUID regexp.
	if !nfsUUIDRe.MatchString(imageID) {
		msg := fmt.Sprintf("nfs-export: image-id %q does not match UUID pattern ^[a-f0-9-]{36}$", imageID)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "nfs-export", args, 1, msg)
		return 1
	}

	// 2. Validate subnet — must parse as a CIDR.
	if _, _, err := net.ParseCIDR(subnet); err != nil {
		msg := fmt.Sprintf("nfs-export: subnet %q is not a valid CIDR: %v", subnet, err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "nfs-export", args, 1, msg)
		return 1
	}

	// 3. Validate rootfs directory exists.
	rootfsPath := nfsImagesBase + "/" + imageID + "/rootfs"
	if _, statErr := os.Stat(rootfsPath); statErr != nil {
		msg := fmt.Sprintf("nfs-export: rootfs directory does not exist: %s (%v)", rootfsPath, statErr)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "nfs-export", args, 1, msg)
		return 1
	}

	// 4. Build /etc/exports content.
	// Only ignore a missing-file error — any other read failure (transient I/O,
	// SELinux denial, permission change) must abort rather than silently overwriting
	// /etc/exports with a fresh file that drops all operator-managed exports.
	existing := ""
	if data, readErr := os.ReadFile(nfsExportsPath); readErr == nil { //#nosec G304 -- reading system /etc/exports; path is a fixed constant
		existing = string(data)
	} else if !os.IsNotExist(readErr) {
		msg := fmt.Sprintf("nfs-export: read %s: %v", nfsExportsPath, readErr)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "nfs-export", args, 2, msg)
		return 2
	}
	// If the file doesn't exist yet, existing remains "".

	newContent, buildErr := buildNFSExportsContent(existing, imageID, subnet)
	if buildErr != nil {
		msg := fmt.Sprintf("nfs-export: build exports content: %v", buildErr)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "nfs-export", args, 2, msg)
		return 2
	}

	// 5. Write /etc/exports atomically.
	tmpPath := nfsExportsPath + ".tmp"
	if writeErr := os.WriteFile(tmpPath, []byte(newContent), 0644); writeErr != nil { //#nosec G306 -- /etc/exports must be world-readable for NFS
		msg := fmt.Sprintf("nfs-export: write tmp: %v", writeErr)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "nfs-export", args, 2, msg)
		return 2
	}
	if renameErr := os.Rename(tmpPath, nfsExportsPath); renameErr != nil {
		_ = os.Remove(tmpPath)
		msg := fmt.Sprintf("nfs-export: rename to %s: %v", nfsExportsPath, renameErr)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "nfs-export", args, 2, msg)
		return 2
	}

	// 6. Run exportfs -ra to reload the NFS daemon's export table.
	// -ra: unexport all, then re-export all entries from /etc/exports.
	// This is the canonical idempotent reload; the helper builds argv internally.
	cmd := exec.Command("exportfs", "-ra") //#nosec G204 -- fixed literal argv; no user input
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		exitCode := 2
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		msg := fmt.Sprintf("nfs-export: exportfs -ra: %v", runErr)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "nfs-export", args, exitCode, msg)
		return exitCode
	}

	fmt.Printf("nfs-export: exported %s to %s (fsid derived from imageID)\n", rootfsPath, subnet)
	writeAudit(callerUID, "nfs-export", args, 0, "")
	return 0
}

// parseNFSExportArgs parses --image-id and --subnet from args.
// Returns an error if any unrecognised flag is present or required flags are absent.
func parseNFSExportArgs(args []string) (imageID, subnet string, err error) {
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--image-id":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--image-id requires a value")
			}
			imageID = args[i+1]
			i += 2
		case "--subnet":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--subnet requires a value")
			}
			subnet = args[i+1]
			i += 2
		default:
			return "", "", fmt.Errorf("unknown argument %q", args[i])
		}
	}
	if imageID == "" {
		return "", "", fmt.Errorf("--image-id is required")
	}
	if subnet == "" {
		return "", "", fmt.Errorf("--subnet is required")
	}
	return imageID, subnet, nil
}

// buildNFSExportsContent merges a new clustr-managed export entry for
// (imageID, subnet) into the existing /etc/exports content.
//
// The clustr-managed block (between nfsAnchorBegin and nfsAnchorEnd) is
// rewritten; all other lines are preserved verbatim.
func buildNFSExportsContent(existing, imageID, subnet string) (string, error) {
	fsid, err := nfsFsidForImageID(imageID)
	if err != nil {
		return "", fmt.Errorf("compute fsid: %w", err)
	}

	rootfsPath := nfsImagesBase + "/" + imageID + "/rootfs"
	newLine := fmt.Sprintf("%s %s(ro,no_subtree_check,fsid=%d)", rootfsPath, subnet, fsid)

	var before, managed, after []string
	inBlock := false
	blockFound := false

	for _, line := range strings.Split(existing, "\n") {
		switch {
		case line == nfsAnchorBegin:
			inBlock = true
			blockFound = true
		case line == nfsAnchorEnd:
			inBlock = false
		case inBlock:
			// Existing managed line: keep unless it matches this imageID (replace).
			if extractNFSImageID(line) == imageID {
				continue
			}
			managed = append(managed, line)
		default:
			if !blockFound {
				before = append(before, line)
			} else if !inBlock {
				after = append(after, line)
			}
		}
	}

	managed = append(managed, newLine)

	// Trim trailing blanks from before block.
	for len(before) > 0 && strings.TrimSpace(before[len(before)-1]) == "" {
		before = before[:len(before)-1]
	}

	var sb strings.Builder
	if len(before) > 0 {
		sb.WriteString(strings.Join(before, "\n"))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(nfsAnchorBegin)
	sb.WriteString("\n")
	for _, l := range managed {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	sb.WriteString(nfsAnchorEnd)
	sb.WriteString("\n")
	for _, l := range after {
		if strings.TrimSpace(l) == "" {
			continue
		}
		sb.WriteString(l)
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// nfsFsidForImageID derives a deterministic uint32 fsid from the first 8 hex
// digits of the image UUID.  See package-level comment for rationale.
func nfsFsidForImageID(imageID string) (uint32, error) {
	clean := strings.ReplaceAll(imageID, "-", "")
	if len(clean) < 8 {
		return 0, fmt.Errorf("imageID too short after stripping hyphens: %q", imageID)
	}
	first8 := clean[:8]
	v, err := strconv.ParseUint(first8, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("parse first8 %q as hex: %w", first8, err)
	}
	const maxFSID = uint64(^uint32(0)) // 2³²−1
	return uint32(v % maxFSID), nil
}

// extractNFSImageID extracts the image UUID from a clustr-managed export line.
func extractNFSImageID(line string) string {
	prefix := nfsImagesBase + "/"
	suffix := "/rootfs"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	rest := line[len(prefix):]
	idx := strings.Index(rest, suffix)
	if idx < 0 {
		return ""
	}
	candidate := rest[:idx]
	// Clean path traversal attempt.
	if strings.Contains(candidate, "/") || strings.Contains(candidate, "..") {
		return ""
	}
	// Validate against UUID pattern.
	if !nfsUUIDRe.MatchString(candidate) {
		return ""
	}
	return candidate
}

