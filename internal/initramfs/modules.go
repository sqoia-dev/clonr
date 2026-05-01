// Package initramfs provides helpers for inspecting and building clustr
// initramfs images.
package initramfs

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ModuleEntry represents a single kernel module found during enumeration.
type ModuleEntry struct {
	// Name is the canonical module name (underscores, no .ko suffix).
	Name string
	// RelPath is the path relative to the initramfs root (e.g. "lib/modules/5.14/kernel/drivers/net/foo.ko").
	RelPath string
	// SHA256 is the hex-encoded SHA-256 of the decompressed .ko file.
	SHA256 string
}

// ManifestLine formats the entry as a single manifest line.
// Format: "<name> <rel_path> <sha256>"
func (e ModuleEntry) ManifestLine() string {
	return fmt.Sprintf("%s %s %s", e.Name, e.RelPath, e.SHA256)
}

// ModuleAllowlist is the canonical list of kernel module names supported by
// clustr initramfs. Names use underscores (modprobe canonical form). The
// enumerator normalises hyphens to underscores when matching.
//
// To add a new driver: append the module name here. No file paths to maintain.
var ModuleAllowlist = []string{
	// VMs — keep existing virtio support
	"failover", "net_failover", "virtio_net",
	"virtio_scsi", "virtio_blk",

	// Mellanox/NVIDIA ConnectX NICs (CX-3 through CX-6+)
	"mlx5_core", "mlx5_ib",
	"mlx4_core", "mlx4_en", "mlx4_ib",

	// Intel NICs (XL710 i40e, E810 ice, 82599 ixgbe, 1G igb, e1000e)
	"i40e", "ice", "ixgbe", "igb", "e1000e",

	// Broadcom NICs
	"bnxt_en", "bnx2x", "tg3",

	// NVMe storage
	"nvme", "nvme_core",

	// Hardware RAID controllers
	"megaraid_sas", "mpt3sas", "aacraid",

	// SCSI mid-layer (required by hardware HBAs)
	"sd_mod", "scsi_mod",

	// Device Mapper (LVM + thin provisioning)
	"dm_mod", "dm_mirror", "dm_snapshot", "dm_thin_pool",

	// Filesystems
	"xfs", "btrfs", "ext4", "jbd2", "mbcache", "fat", "vfat",

	// Crypto / CRC
	"crc32c_generic", "libcrc32c", "crc32c_intel",

	// MD software RAID personalities
	"raid0", "raid1", "raid10", "raid456",
}

// EnumerateModules walks moduleRoot looking for .ko and .ko.xz files whose
// stem (filename without extension) is in allowlist. For each match the file
// is hashed, and a ModuleEntry is returned.
//
// moduleRoot is the /lib/modules/<kver>/kernel directory (or a fixture stand-in
// in tests). The enumerator descends recursively into all subdirectories so the
// caller does not need to enumerate search paths — any .ko living anywhere
// under moduleRoot is considered.
//
// Hyphen/underscore normalisation: "crc32c-intel.ko" matches allowlist entry
// "crc32c_intel" and vice-versa.
//
// .ko.xz files are read without decompressing for hash purposes (the hash
// covers the compressed bytes on disk; the build script decompresses them).
// The RelPath returned uses the path within moduleRoot without any initramfs
// prefix; callers prepend "lib/modules/<kver>/kernel/" as needed.
func EnumerateModules(moduleRoot string, allowlist []string) ([]ModuleEntry, error) {
	// Build a lookup set from allowlist, normalised to underscores.
	allowed := make(map[string]string, len(allowlist)) // normalised → canonical
	for _, name := range allowlist {
		norm := normModuleName(name)
		allowed[norm] = name
	}

	var entries []ModuleEntry
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		// Accept .ko and .ko.xz only.
		var stem string
		switch {
		case strings.HasSuffix(name, ".ko.xz"):
			stem = strings.TrimSuffix(name, ".ko.xz")
		case strings.HasSuffix(name, ".ko"):
			stem = strings.TrimSuffix(name, ".ko")
		default:
			return nil
		}

		canonical, ok := allowed[normModuleName(stem)]
		if !ok {
			return nil
		}

		rel, relErr := filepath.Rel(moduleRoot, path)
		if relErr != nil {
			rel = path
		}

		h, hashErr := hashFile(path)
		if hashErr != nil {
			// Non-fatal: include the entry with a placeholder hash so the
			// manifest is still useful for debugging.
			h = "unavailable"
		}

		entries = append(entries, ModuleEntry{
			Name:    canonical,
			RelPath: rel,
			SHA256:  h,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enumerate modules: walk %s: %w", moduleRoot, err)
	}
	return entries, nil
}

// WriteManifest writes entries to w, one line per entry.
// Format: "<name> <rel_path> <sha256>\n"
func WriteManifest(w io.Writer, entries []ModuleEntry) error {
	bw := bufio.NewWriter(w)
	for _, e := range entries {
		if _, err := fmt.Fprintln(bw, e.ManifestLine()); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// WriteManifestFile writes the manifest to path (creates or truncates).
func WriteManifestFile(path string, entries []ModuleEntry) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	defer f.Close()
	return WriteManifest(f, entries)
}

// normModuleName normalises a module name to lowercase underscores so that
// "crc32c-intel", "crc32c_intel", and "CRC32C_INTEL" all map to the same key.
func normModuleName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

// hashFile returns the hex-encoded SHA-256 of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
