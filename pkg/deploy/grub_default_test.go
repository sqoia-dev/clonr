package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPinGrubDefaultBLSEntry_ProductionSelected verifies that a directory
// containing both a rescue and a production BLS entry causes the production
// entry to be written as saved_entry in grubenv.
func TestPinGrubDefaultBLSEntry_ProductionSelected(t *testing.T) {
	root := t.TempDir()
	entriesDir := filepath.Join(root, "boot", "loader", "entries")
	if err := os.MkdirAll(entriesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	machineID := "aabbccddeeff00112233445566778899"

	// Write a rescue entry (dracut naming: <machineID>-0-rescue-<token>.conf).
	rescueName := machineID + "-0-rescue-aabbccdd.conf"
	if err := os.WriteFile(filepath.Join(entriesDir, rescueName), []byte("title Rocky Linux (rescue)\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a production entry.
	prodName := machineID + "-5.14.0-427.13.1.el9_4.x86_64.conf"
	if err := os.WriteFile(filepath.Join(entriesDir, prodName), []byte("title Rocky Linux (5.14.0-427.13.1.el9_4.x86_64)\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	chosen, err := pinGrubDefaultBLSEntry(root)
	if err != nil {
		t.Fatalf("pinGrubDefaultBLSEntry returned unexpected error: %v", err)
	}

	wantStem := strings.TrimSuffix(prodName, ".conf")
	if chosen != wantStem {
		t.Errorf("chosen = %q, want %q", chosen, wantStem)
	}

	// Verify grubenv was written with correct content.
	grubenvPath := filepath.Join(root, "boot", "grub2", "grubenv")
	data, err := os.ReadFile(grubenvPath)
	if err != nil {
		t.Fatalf("grubenv not written: %v", err)
	}

	// Must be exactly 1024 bytes.
	if len(data) != grubEnvSize {
		t.Errorf("grubenv size = %d, want %d", len(data), grubEnvSize)
	}

	// Must start with the header.
	if !strings.HasPrefix(string(data), grubEnvHeader) {
		t.Errorf("grubenv missing header %q; got prefix %q", grubEnvHeader, string(data[:len(grubEnvHeader)]))
	}

	// Must contain the saved_entry line.
	wantLine := "saved_entry=" + wantStem
	if !strings.Contains(string(data), wantLine) {
		t.Errorf("grubenv does not contain %q", wantLine)
	}

	// Must be padded with '#' after the content.
	afterContent := string(data[len(grubEnvHeader)+len(wantLine)+1:]) // +1 for newline
	for i, ch := range afterContent {
		if ch != '#' {
			t.Errorf("grubenv padding byte %d = %q, want '#'", i, string(ch))
			break
		}
	}
}

// TestPinGrubDefaultBLSEntry_OnlyRescue verifies that an error is returned
// when only a rescue entry exists (no production kernel to pin to).
func TestPinGrubDefaultBLSEntry_OnlyRescue(t *testing.T) {
	root := t.TempDir()
	entriesDir := filepath.Join(root, "boot", "loader", "entries")
	if err := os.MkdirAll(entriesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	machineID := "aabbccddeeff00112233445566778899"
	rescueName := machineID + "-0-rescue-deadbeef.conf"
	if err := os.WriteFile(filepath.Join(entriesDir, rescueName), []byte("title Rocky Linux (rescue)\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := pinGrubDefaultBLSEntry(root)
	if err == nil {
		t.Error("expected error when only rescue entry exists, got nil")
	}
}

// TestPinGrubDefaultBLSEntry_MultipleProduction verifies that when multiple
// non-rescue entries exist the one with the highest kernel version is selected.
//
// Selection policy: highest numeric kernel version wins. This ensures that
// after a kernel upgrade the newest kernel is the boot default, consistent with
// the behaviour users expect from grub2-set-default on a running system.
func TestPinGrubDefaultBLSEntry_MultipleProduction(t *testing.T) {
	root := t.TempDir()
	entriesDir := filepath.Join(root, "boot", "loader", "entries")
	if err := os.MkdirAll(entriesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	machineID := "aabbccddeeff00112233445566778899"

	// Write two production entries with different kernel versions.
	// 427 > 362 numerically, even though '4' > '3' lexicographically here.
	// The test also covers the case where the higher-numbered entry is NOT first
	// alphabetically (362 < 427 alphabetically as strings, so 427 would sort
	// last if we used pure lexicographic order — we want it first).
	entries := []string{
		machineID + "-5.14.0-362.8.1.el9_3.x86_64.conf",
		machineID + "-5.14.0-427.13.1.el9_4.x86_64.conf",
	}
	for _, name := range entries {
		if err := os.WriteFile(filepath.Join(entriesDir, name), []byte("title stub\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	chosen, err := pinGrubDefaultBLSEntry(root)
	if err != nil {
		t.Fatalf("pinGrubDefaultBLSEntry returned unexpected error: %v", err)
	}

	wantStem := strings.TrimSuffix(machineID+"-5.14.0-427.13.1.el9_4.x86_64.conf", ".conf")
	if chosen != wantStem {
		t.Errorf("chosen = %q, want highest-version entry %q", chosen, wantStem)
	}
}

// TestPinGrubDefaultBLSEntry_EmptyDir verifies that an error is returned when
// the entries directory contains no .conf files at all.
func TestPinGrubDefaultBLSEntry_EmptyDir(t *testing.T) {
	root := t.TempDir()
	entriesDir := filepath.Join(root, "boot", "loader", "entries")
	if err := os.MkdirAll(entriesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := pinGrubDefaultBLSEntry(root)
	if err == nil {
		t.Error("expected error for empty entries dir, got nil")
	}
}

// TestKernelVersionGreater verifies the numeric-aware kernel version comparator
// used to select the highest installed kernel.
func TestKernelVersionGreater(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		// Major build number comparison: 427 > 362.
		{"5.14.0-427.13.1.el9_4.x86_64", "5.14.0-362.8.1.el9_3.x86_64", true},
		// Inverse.
		{"5.14.0-362.8.1.el9_3.x86_64", "5.14.0-427.13.1.el9_4.x86_64", false},
		// Equal.
		{"5.14.0-427.13.1.el9_4.x86_64", "5.14.0-427.13.1.el9_4.x86_64", false},
		// Major kernel version wins.
		{"6.1.0-100.el9.x86_64", "5.14.0-427.13.1.el9_4.x86_64", true},
		// Minor version comparison.
		{"5.15.0-100.el9.x86_64", "5.14.0-427.13.1.el9_4.x86_64", true},
	}

	for _, tc := range tests {
		got := kernelVersionGreater(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("kernelVersionGreater(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestWriteGrubenv verifies the raw grubenv writer produces a correctly
// sized and formatted block.
func TestWriteGrubenv(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "grubenv")

	entry := "aabbccddeeff00112233445566778899-5.14.0-427.13.1.el9_4.x86_64"
	if err := writeGrubenv(path, entry); err != nil {
		t.Fatalf("writeGrubenv: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) != grubEnvSize {
		t.Errorf("size = %d, want %d", len(data), grubEnvSize)
	}

	expectedPrefix := grubEnvHeader + "saved_entry=" + entry + "\n"
	if !strings.HasPrefix(string(data), expectedPrefix) {
		t.Errorf("unexpected prefix: got %q, want prefix %q", string(data[:len(expectedPrefix)]), expectedPrefix)
	}
}
