package initramfs_test

import (
	"os"
	"strings"
	"testing"
)

// initramfsInitStatelessPath is the path to the stateless NFS init script template.
// Tests read it directly to verify it does not contain block-install operations.
const initramfsInitStatelessPath = "../../scripts/initramfs-init-stateless-nfs.sh"

// TestStatelessNFSInitScript_NoBlockInstallOps verifies that the stateless NFS
// init script template does NOT invoke any disk-partitioning or disk-write
// operations on executable lines. Comment lines are excluded from the check
// since the header explicitly documents what the script does not do.
//
// A stateless node's init script must ONLY:
//  1. Mount virtual filesystems
//  2. Load NIC modules and run DHCP
//  3. Mount the NFS root
//  4. Pivot root and exec the real init
//
// The following command tokens must not appear on executable (non-comment) lines.
func TestStatelessNFSInitScript_NoBlockInstallOps(t *testing.T) {
	data, err := os.ReadFile(initramfsInitStatelessPath)
	if err != nil {
		t.Fatalf("read stateless init script %s: %v", initramfsInitStatelessPath, err)
	}

	// Collect only non-comment, non-blank lines.
	var execLines []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		execLines = append(execLines, line)
	}
	execContent := strings.Join(execLines, "\n")

	// These command tokens must NOT appear on executable lines.
	forbidden := []struct {
		token string
		label string
	}{
		{"wipefs", "wipefs (destructive disk signature wipe)"},
		{"grub2-install", "grub2-install (bootloader install to disk)"},
		{"grub2-mkconfig", "grub2-mkconfig (bootloader config generation)"},
		{"mkfs.xfs", "mkfs.xfs (partition format)"},
		{"mkfs.ext4", "mkfs.ext4 (partition format)"},
		{"mkfs.vfat", "mkfs.vfat (partition format)"},
		{"sgdisk", "sgdisk (GPT partition tool)"},
		{"partprobe", "partprobe (partition probe tool)"},
		{"partx", "partx (partition detection tool)"},
		{"clustr deploy", "clustr deploy (block-install deploy agent)"},
		{"clustr.token", "clustr.token (block-install auth token)"},
		{"fsfreeze", "fsfreeze (filesystem freeze for snapshot)"},
		{"efibootmgr", "efibootmgr (NVRAM BootOrder management)"},
		{"mkswap", "mkswap (swap partition setup)"},
	}

	for _, f := range forbidden {
		if strings.Contains(execContent, f.token) {
			t.Errorf("stateless-nfs init script executable lines must NOT invoke %s", f.label)
		}
	}
}

// TestStatelessNFSInitScript_ContainsNFSMount verifies the stateless init
// script contains the NFS mount and pivot root commands that define its purpose.
func TestStatelessNFSInitScript_ContainsNFSMount(t *testing.T) {
	data, err := os.ReadFile(initramfsInitStatelessPath)
	if err != nil {
		t.Fatalf("read stateless init script: %v", err)
	}
	content := string(data)

	required := []string{
		"nfsroot",       // reads nfsroot= from kernel cmdline
		"mount -t nfs4", // mounts the NFS root filesystem
		"switch_root",   // pivots into the NFS root
		"DHCP",          // brings up networking via DHCP
	}

	for _, r := range required {
		if !strings.Contains(content, r) {
			t.Errorf("stateless-nfs init script must contain %q but it is absent", r)
		}
	}
}

// TestStatelessNFSInitScript_StartsWithShebang verifies the script is a valid
// POSIX sh script (starts with #!/bin/sh).
func TestStatelessNFSInitScript_StartsWithShebang(t *testing.T) {
	data, err := os.ReadFile(initramfsInitStatelessPath)
	if err != nil {
		t.Fatalf("read stateless init script: %v", err)
	}
	if !strings.HasPrefix(string(data), "#!/bin/sh") {
		t.Errorf("stateless init script must start with #!/bin/sh")
	}
}
