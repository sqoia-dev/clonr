package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sqoia-dev/clonr/pkg/api"
)

// TestWriteClonrDHCPProfile_WritesCorrectly verifies that writeClonrDHCPProfile
// writes the NM connection file with correct content and mandatory 0600 permissions.
// NetworkManager silently ignores connection files that are not 0600.
func TestWriteClonrDHCPProfile_WritesCorrectly(t *testing.T) {
	root := t.TempDir()

	if err := writeClonrDHCPProfile(root); err != nil {
		t.Fatalf("writeClonrDHCPProfile: %v", err)
	}

	profilePath := filepath.Join(root, "etc", "NetworkManager", "system-connections", "clonr-dhcp.nmconnection")

	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("profile not written: %v", err)
	}

	content := string(data)

	// Must contain the connection section with correct id and type.
	if !strings.Contains(content, "id=clonr-dhcp") {
		t.Errorf("profile missing id=clonr-dhcp; got:\n%s", content)
	}
	if !strings.Contains(content, "type=ethernet") {
		t.Errorf("profile missing type=ethernet; got:\n%s", content)
	}
	if !strings.Contains(content, "autoconnect=true") {
		t.Errorf("profile missing autoconnect=true; got:\n%s", content)
	}
	if !strings.Contains(content, "autoconnect-priority=100") {
		t.Errorf("profile missing autoconnect-priority=100; got:\n%s", content)
	}

	// IPv4 must be auto (DHCP).
	if !strings.Contains(content, "method=auto") {
		t.Errorf("profile missing method=auto (DHCP); got:\n%s", content)
	}

	// Permissions MUST be 0600 — NM ignores world-readable keyfiles.
	info, err := os.Stat(profilePath)
	if err != nil {
		t.Fatalf("stat profile: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("profile permissions = %04o, want 0600 (NM requires this)", info.Mode().Perm())
	}
}

// TestWriteClonrDHCPProfile_Idempotent verifies that calling writeClonrDHCPProfile
// twice does not fail (second call overwrites the first without error).
func TestWriteClonrDHCPProfile_Idempotent(t *testing.T) {
	root := t.TempDir()

	if err := writeClonrDHCPProfile(root); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := writeClonrDHCPProfile(root); err != nil {
		t.Fatalf("second call (idempotency): %v", err)
	}
}

// TestWriteHandcraftedGrubCfg_NoLoadEnv verifies that the grub.cfg written by
// writeHandcraftedGrubCfg does NOT contain load_env or saved_entry (Option A:
// sole authority is set default=0, no dependency on grubenv state).
func TestWriteHandcraftedGrubCfg_NoLoadEnv(t *testing.T) {
	root := t.TempDir()

	// Plant a fake kernel so writeHandcraftedGrubCfg finds something.
	bootDir := filepath.Join(root, "boot")
	if err := os.MkdirAll(bootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	kver := "5.14.0-427.13.1.el9_4.x86_64"
	if err := os.WriteFile(filepath.Join(bootDir, "vmlinuz-"+kver), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bootDir, "initramfs-"+kver+".img"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure grub2 dir exists.
	if err := os.MkdirAll(filepath.Join(root, "boot", "grub2"), 0o755); err != nil {
		t.Fatal(err)
	}

	rootUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	bootUUID := rootUUID // single-partition layout (bootUUID == rootUUID)

	// Non-RAID layout: single root partition, no RAID arrays.
	layout := api.DiskLayout{
		Partitions: []api.PartitionSpec{
			{MountPoint: "/", Filesystem: "xfs"},
		},
	}

	if err := writeHandcraftedGrubCfg(root, rootUUID, bootUUID, layout); err != nil {
		t.Fatalf("writeHandcraftedGrubCfg: %v", err)
	}

	cfgPath := filepath.Join(root, "boot", "grub2", "grub.cfg")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("grub.cfg not written: %v", err)
	}
	content := string(data)

	// Must NOT contain load_env — it reads grubenv which may not exist on RAID.
	if strings.Contains(content, "load_env") {
		t.Errorf("grub.cfg must not contain 'load_env'; got:\n%s", content)
	}

	// Must NOT use saved_entry — that depends on grubenv state.
	if strings.Contains(content, "saved_entry") {
		t.Errorf("grub.cfg must not contain 'saved_entry'; got:\n%s", content)
	}

	// Must contain set default=0 — production kernel is always first entry.
	if !strings.Contains(content, "set default=0") {
		t.Errorf("grub.cfg must contain 'set default=0'; got:\n%s", content)
	}

	// Must contain the production kernel menuentry.
	if !strings.Contains(content, "menuentry") {
		t.Errorf("grub.cfg must contain menuentry block; got:\n%s", content)
	}

	// Must reference the correct kernel version.
	if !strings.Contains(content, kver) {
		t.Errorf("grub.cfg must reference kernel version %q; got:\n%s", kver, content)
	}

	// Must NOT contain a rescue menuentry.
	if strings.Contains(content, "rescue") {
		t.Errorf("grub.cfg must not contain rescue menuentry (Option A: single entry only); got:\n%s", content)
	}
}
