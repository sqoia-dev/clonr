package pxe

import (
	"net"
	"strings"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

// TestGenerateDiskBootScript_BIOS verifies BIOS nodes get sanboot (INT 13h) in the
// disk boot path, a boot menu with a reimage option, and no bare `exit` line.
func TestGenerateDiskBootScript_BIOS(t *testing.T) {
	script, err := GenerateDiskBootScript("node207", "bios", "http://10.0.0.1:8080", "v0.1.0-test")
	if err != nil {
		t.Fatalf("GenerateDiskBootScript(bios) returned error: %v", err)
	}
	out := string(script)

	if !strings.Contains(out, "sanboot --no-describe --drive 0x80") {
		t.Errorf("BIOS disk boot script missing sanboot command; got:\n%s", out)
	}

	// Boot menu must be present.
	if !strings.Contains(out, "menu ") {
		t.Errorf("BIOS disk boot script missing boot menu; got:\n%s", out)
	}
	if !strings.Contains(out, "reimage") {
		t.Errorf("BIOS disk boot script missing reimage menu item; got:\n%s", out)
	}

	// Reimage URL must use the provided server URL.
	if !strings.Contains(out, "http://10.0.0.1:8080/api/v1/boot/ipxe") {
		t.Errorf("BIOS disk boot script missing reimage URL with server; got:\n%s", out)
	}

	// Bare "exit" must not appear as a standalone line — it causes SeaBIOS PXE loop.
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "exit" {
			t.Errorf("BIOS disk boot script must not contain bare 'exit' line (SeaBIOS loop); got line: %q", line)
		}
	}

	if !strings.Contains(out, "node207") {
		t.Errorf("BIOS disk boot script should include hostname 'node207'; got:\n%s", out)
	}
}

// TestGenerateDiskBootScript_UEFI verifies UEFI nodes get a bare `exit` in the
// :disk label body (ADR: post-deploy UEFI uses `exit`, not chain-boot — see
// docs/boot-architecture.md), a boot menu with reimage option, and NOT sanboot
// (INT 13h is a BIOS concept) and NOT a grub.efi chain URL (removed path).
func TestGenerateDiskBootScript_UEFI(t *testing.T) {
	script, err := GenerateDiskBootScript("node201", "uefi", "http://10.0.0.1:8080", "v0.1.0-test")
	if err != nil {
		t.Fatalf("GenerateDiskBootScript(uefi) returned error: %v", err)
	}
	out := string(script)

	// Must NOT reference grub.efi — server-side chain-boot path has been removed.
	if strings.Contains(out, "/api/v1/boot/grub.efi") {
		t.Errorf("UEFI disk boot script must NOT contain grub.efi chain URL (removed path); got:\n%s", out)
	}

	// The :disk label body must be a bare `exit` so firmware walks BootOrder
	// to the OS NVRAM entry written by FixEFIBoot.
	diskBodyFound := false
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "exit" {
			diskBodyFound = true
			break
		}
	}
	if !diskBodyFound {
		t.Errorf("UEFI disk boot script must contain bare 'exit' line (firmware BootOrder handoff); got:\n%s", out)
	}

	// Must NOT contain `exit 1` (breaks OVMF BootOrder routing).
	if strings.Contains(out, "exit 1") {
		t.Errorf("UEFI disk boot script must NOT contain 'exit 1' (breaks OVMF BootOrder routing); got:\n%s", out)
	}

	if strings.Contains(out, "sanboot") {
		t.Errorf("UEFI disk boot script must not contain sanboot (INT 13h fails on OVMF); got:\n%s", out)
	}

	// Boot menu must be present.
	if !strings.Contains(out, "menu ") {
		t.Errorf("UEFI disk boot script missing boot menu; got:\n%s", out)
	}
	if !strings.Contains(out, "reimage") {
		t.Errorf("UEFI disk boot script missing reimage menu item; got:\n%s", out)
	}

	// Reimage URL must still reference the server (chain into iPXE endpoint).
	if !strings.Contains(out, "http://10.0.0.1:8080/api/v1/boot/ipxe") {
		t.Errorf("UEFI disk boot script missing reimage URL with server; got:\n%s", out)
	}

	if !strings.Contains(out, "node201") {
		t.Errorf("UEFI disk boot script should include hostname 'node201'; got:\n%s", out)
	}
}

// TestGenerateDiskBootScript_DefaultsToUEFI verifies that an empty/unknown firmware
// string is treated as UEFI (safe default for new images).
func TestGenerateDiskBootScript_DefaultsToUEFI(t *testing.T) {
	script, err := GenerateDiskBootScript("node-unknown", "", "http://10.0.0.1:8080", "v0.1.0-test")
	if err != nil {
		t.Fatalf("GenerateDiskBootScript('') returned error: %v", err)
	}
	out := string(script)

	if strings.Contains(out, "sanboot") {
		t.Errorf("default (empty firmware) disk boot script must not use sanboot; got:\n%s", out)
	}

	// Default (UEFI) must NOT reference grub.efi — server-side chain-boot path removed.
	if strings.Contains(out, "/api/v1/boot/grub.efi") {
		t.Errorf("default (UEFI) disk boot script must NOT contain grub.efi chain URL (removed path); got:\n%s", out)
	}

	// Must contain bare exit for BootOrder handoff.
	exitFound := false
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "exit" {
			exitFound = true
			break
		}
	}
	if !exitFound {
		t.Errorf("default (UEFI) disk boot script must contain bare 'exit' line; got:\n%s", out)
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
