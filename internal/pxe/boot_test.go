package pxe

import (
	"net"
	"strings"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestGenerateDiskBootScript_BIOS verifies BIOS nodes get sanboot (INT 13h) in the
// disk boot path, a boot menu with a reimage option, and no bare `exit` line.
func TestGenerateDiskBootScript_BIOS(t *testing.T) {
	script, err := GenerateDiskBootScript("node207", "bios", "http://10.0.0.1:8080", "v0.1.0-test", nil, false)
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
	script, err := GenerateDiskBootScript("node201", "uefi", "http://10.0.0.1:8080", "v0.1.0-test", nil, false)
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
	script, err := GenerateDiskBootScript("node-unknown", "", "http://10.0.0.1:8080", "v0.1.0-test", nil, false)
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

// TestGenerateDiskBootScript_ExtraEntries verifies that enabled boot_entries are
// appended to the iPXE menu and rendered as label blocks with kernel/initrd/boot.
func TestGenerateDiskBootScript_ExtraEntries(t *testing.T) {
	entries := []api.BootEntry{
		{
			ID:        "test-memtest-id",
			Name:      "Memtest86+",
			Kind:      "memtest",
			KernelURL: "/api/v1/boot/extra/memtest",
			Enabled:   true,
		},
		{
			ID:        "test-rescue-id",
			Name:      "Rescue Shell",
			Kind:      "rescue",
			KernelURL: "/api/v1/boot/vmlinuz",
			InitrdURL: "/api/v1/boot/rescue.cpio.gz",
			Cmdline:   "console=ttyS0,115200n8",
			Enabled:   true,
		},
	}
	script, err := GenerateDiskBootScript("node-test", "uefi", "http://10.0.0.1:8080", "v0.1.0-test", entries, false)
	if err != nil {
		t.Fatalf("GenerateDiskBootScript with extra entries returned error: %v", err)
	}
	out := string(script)

	// Both entries should appear in the menu.
	if !strings.Contains(out, "entry_test-memtest-id") {
		t.Errorf("missing memtest menu item; got:\n%s", out)
	}
	if !strings.Contains(out, "Memtest86+") {
		t.Errorf("missing memtest label text; got:\n%s", out)
	}
	if !strings.Contains(out, "entry_test-rescue-id") {
		t.Errorf("missing rescue menu item; got:\n%s", out)
	}

	// Both should have kernel lines.
	if !strings.Contains(out, "/api/v1/boot/extra/memtest") {
		t.Errorf("missing memtest kernel_url; got:\n%s", out)
	}
	if !strings.Contains(out, "/api/v1/boot/vmlinuz") {
		t.Errorf("missing rescue kernel_url; got:\n%s", out)
	}

	// Rescue should have initrd line.
	if !strings.Contains(out, "/api/v1/boot/rescue.cpio.gz") {
		t.Errorf("missing rescue initrd_url; got:\n%s", out)
	}

	// Rescue cmdline should appear.
	if !strings.Contains(out, "console=ttyS0,115200n8") {
		t.Errorf("missing rescue cmdline; got:\n%s", out)
	}
}

// TestGenerateDiskBootScript_PersistedEntrySelectsByDefault locks down the
// fix for Codex post-ship review issue #3: GenerateDiskBootScriptWithSettings
// previously accepted persistedEntry/persistedKernelCmdline parameters but
// neither template referenced them, so a node with NetbootMenuEntry set
// still booted the standard disk default.
//
// Verified end-to-end:
//   - the chosen ExtraEntries row's --default flag fires (selected after
//     the 5s timeout instead of "disk")
//   - the choose fallback target matches that entry id
//   - the persistedKernelCmdline string is appended verbatim to that
//     entry's kernel line
func TestGenerateDiskBootScript_PersistedEntrySelectsByDefault(t *testing.T) {
	rescue := api.BootEntry{
		ID:        "rescue-id-7",
		Name:      "Rescue Shell",
		Kind:      "rescue",
		KernelURL: "/api/v1/boot/vmlinuz",
		InitrdURL: "/api/v1/boot/rescue.cpio.gz",
		Cmdline:   "rd.shell=1",
		Enabled:   true,
	}
	persistedCmdline := "loglevel=7 clustr.debug=1"

	for _, firmware := range []string{"bios", "uefi"} {
		t.Run(firmware, func(t *testing.T) {
			script, err := GenerateDiskBootScriptWithSettings(
				"node-x", firmware, "http://10.0.0.1:8080", "v0.1.0-test",
				[]api.BootEntry{rescue}, false,
				&rescue, persistedCmdline,
			)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			out := string(script)

			// The default disk item must NOT carry --default any more —
			// the persisted entry takes its place.
			if strings.Contains(out, "item --default disk") {
				t.Errorf("disk item kept --default despite persisted entry; got:\n%s", out)
			}
			// The persisted entry item line must carry --default.
			if !strings.Contains(out, "item --default entry_rescue-id-7") {
				t.Errorf("persisted entry item missing --default; got:\n%s", out)
			}
			// `choose --default entry_rescue-id-7` and the fallback goto
			// must both target the persisted entry.
			if !strings.Contains(out, "choose --default entry_rescue-id-7") {
				t.Errorf("choose missing --default entry_rescue-id-7; got:\n%s", out)
			}
			if !strings.Contains(out, "goto entry_rescue-id-7") {
				t.Errorf("fallback goto missing entry_rescue-id-7; got:\n%s", out)
			}
			// The persisted kernel cmdline must be appended to the
			// entry's kernel line in addition to the entry's own cmdline.
			if !strings.Contains(out, "rd.shell=1 loglevel=7 clustr.debug=1") {
				t.Errorf("persisted kernel cmdline not appended; got:\n%s", out)
			}
		})
	}
}

// TestGenerateDiskBootScript_NoPersistedEntryStillDefaultsDisk verifies the
// no-op path — when nothing is persisted, render output matches the
// pre-fix shape (disk auto-selected).
func TestGenerateDiskBootScript_NoPersistedEntryStillDefaultsDisk(t *testing.T) {
	script, err := GenerateDiskBootScriptWithSettings(
		"node-y", "uefi", "http://10.0.0.1:8080", "v0.1.0-test",
		nil, false, nil, "",
	)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := string(script)
	if !strings.Contains(out, "item --default disk --timeout 5000 disk") {
		t.Errorf("disk item lost --default in no-persist case; got:\n%s", out)
	}
	if !strings.Contains(out, "choose --default disk --timeout 5000 target") {
		t.Errorf("choose lost --default disk; got:\n%s", out)
	}
}

// TestGenerateDiskBootScript_MulticastMenuItem verifies that the reimage-fleet
// menu item appears when multicastEnabled=true and is absent when false.
func TestGenerateDiskBootScript_MulticastMenuItem(t *testing.T) {
	scriptOn, err := GenerateDiskBootScript("node-mc", "uefi", "http://10.0.0.1:8080", "v0.1.0-test", nil, true)
	if err != nil {
		t.Fatalf("GenerateDiskBootScript(multicastEnabled=true) error: %v", err)
	}
	outOn := string(scriptOn)
	if !strings.Contains(outOn, "reimage-fleet") {
		t.Errorf("multicastEnabled=true: expected 'reimage-fleet' in menu; got:\n%s", outOn)
	}
	if !strings.Contains(outOn, "multicast=1") {
		t.Errorf("multicastEnabled=true: expected 'multicast=1' in reimage-fleet chain URL; got:\n%s", outOn)
	}

	scriptOff, err := GenerateDiskBootScript("node-mc", "uefi", "http://10.0.0.1:8080", "v0.1.0-test", nil, false)
	if err != nil {
		t.Fatalf("GenerateDiskBootScript(multicastEnabled=false) error: %v", err)
	}
	outOff := string(scriptOff)
	if strings.Contains(outOff, "reimage-fleet") {
		t.Errorf("multicastEnabled=false: unexpected 'reimage-fleet' in menu; got:\n%s", outOff)
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
