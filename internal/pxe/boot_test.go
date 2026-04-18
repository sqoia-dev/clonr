package pxe

import (
	"net"
	"strings"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

// TestGenerateDiskBootScript_BIOS verifies BIOS nodes get sanboot (INT 13h).
// bare `exit` must NOT appear — it causes SeaBIOS to restart the PXE loop.
func TestGenerateDiskBootScript_BIOS(t *testing.T) {
	script, err := GenerateDiskBootScript("node207", "bios")
	if err != nil {
		t.Fatalf("GenerateDiskBootScript(bios) returned error: %v", err)
	}
	out := string(script)

	if !strings.Contains(out, "sanboot --no-describe --drive 0x80") {
		t.Errorf("BIOS disk boot script missing sanboot command; got:\n%s", out)
	}

	// Ensure bare "exit" line is not present — it causes SeaBIOS PXE loop.
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "exit" {
			t.Errorf("BIOS disk boot script must not contain bare 'exit' line (SeaBIOS loop); got line: %q", line)
		}
	}

	if !strings.Contains(out, "node207") {
		t.Errorf("BIOS disk boot script should include hostname 'node207'; got:\n%s", out)
	}
}

// TestGenerateDiskBootScript_UEFI verifies UEFI nodes get `exit 1` (signal PXE
// failure to UEFI firmware so it falls through to the next boot entry — the disk)
// and NOT sanboot (INT 13h is a BIOS concept, fails on OVMF).
//
// `exit 1` (non-zero) is required: `exit` or `exit 0` tells OVMF that PXE
// succeeded and it shows the boot picker. A non-zero exit signals failure,
// causing OVMF to try the next BootOrder entry (the OS disk).
func TestGenerateDiskBootScript_UEFI(t *testing.T) {
	script, err := GenerateDiskBootScript("node201", "uefi")
	if err != nil {
		t.Fatalf("GenerateDiskBootScript(uefi) returned error: %v", err)
	}
	out := string(script)

	// Must contain `exit 1` so UEFI firmware falls through to the next boot entry.
	if !strings.Contains(out, "exit 1") {
		t.Errorf("UEFI disk boot script must contain 'exit 1'; got:\n%s", out)
	}

	if strings.Contains(out, "sanboot") {
		t.Errorf("UEFI disk boot script must not contain sanboot (INT 13h fails on OVMF); got:\n%s", out)
	}

	if !strings.Contains(out, "node201") {
		t.Errorf("UEFI disk boot script should include hostname 'node201'; got:\n%s", out)
	}
}

// TestGenerateDiskBootScript_DefaultsToUEFI verifies that an empty/unknown firmware
// string is treated as UEFI (safe default for new images).
func TestGenerateDiskBootScript_DefaultsToUEFI(t *testing.T) {
	script, err := GenerateDiskBootScript("node-unknown", "")
	if err != nil {
		t.Fatalf("GenerateDiskBootScript('') returned error: %v", err)
	}
	out := string(script)

	if strings.Contains(out, "sanboot") {
		t.Errorf("default (empty firmware) disk boot script must not use sanboot; got:\n%s", out)
	}
}

// TestBootFilename_ArchDispatch verifies that DHCP option 93 values are mapped
// to the correct chainloader binary. In particular, arch type 0x0010 (UEFI
// HTTP boot x86-64, "HTTPClient:Arch:00016") must return ipxe.efi — not the
// BIOS-only undionly.kpxe — because UEFI firmware cannot execute a BIOS binary.
func TestBootFilename_ArchDispatch(t *testing.T) {
	serverIP := net.ParseIP("192.168.1.151")
	httpPort := "8080"

	makeReqWithArch := func(archType uint16) *dhcpv4.DHCPv4 {
		req, _ := dhcpv4.New()
		if archType != 0xFFFF { // 0xFFFF sentinel = no option 93
			req.UpdateOption(dhcpv4.OptGeneric(
				dhcpv4.OptionClientSystemArchitectureType,
				[]byte{byte(archType >> 8), byte(archType & 0xFF)},
			))
		}
		return req
	}

	tests := []struct {
		name     string
		archType uint16
		isIPXE   bool
		wantFile string
	}{
		{"BIOS x86 (0x0000)", 0x0000, false, "undionly.kpxe"},
		{"no option 93", 0xFFFF, false, "undionly.kpxe"},
		{"EFI IA32 (0x0006)", 0x0006, false, "ipxe.efi"},
		{"EFI x86-64 (0x0007)", 0x0007, false, "ipxe.efi"},
		{"EFI x86-64 alt (0x0009)", 0x0009, false, "ipxe.efi"},
		{"UEFI HTTP boot x86-64 (0x0010)", 0x0010, false, "ipxe.efi"},
		{"EFI ARM64 (0x000b)", 0x000b, false, "ipxe.efi"},
		{"iPXE already loaded", 0x0007, true, "http://"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req *dhcpv4.DHCPv4
			if tc.archType == 0xFFFF {
				req, _ = dhcpv4.New() // no option 93
			} else {
				req = makeReqWithArch(tc.archType)
			}
			got := bootFilename(req, tc.isIPXE, serverIP, httpPort)
			if !strings.Contains(got, tc.wantFile) {
				t.Errorf("arch 0x%04x: got %q, want contains %q", tc.archType, got, tc.wantFile)
			}
		})
	}
}
