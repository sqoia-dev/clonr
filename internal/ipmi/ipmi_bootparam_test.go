package ipmi

import (
	"testing"
)

// ─── buildRawBootBytes ────────────────────────────────────────────────────────

// TestBuildRawBootBytes_FlagsEncoding verifies the bit-packing of the flags byte.
// These are the values documented in the task spec and IPMI spec table 28-14;
// any regression here would cause the node to boot from the wrong device or
// fail to apply a persistent override.
func TestBuildRawBootBytes_FlagsEncoding(t *testing.T) {
	tests := []struct {
		name          string
		dev           BootDevice
		opts          BootOpts
		wantFlags     byte
		wantDevByte   byte
	}{
		{
			name:        "disk persistent EFI (iDRAC/LKE prod default)",
			dev:         BootDevDisk,
			opts:        BootOpts{Persistent: true, EFI: true},
			wantFlags:   0xE0, // 0x80 valid | 0x40 persistent | 0x20 EFI
			wantDevByte: 0x08,
		},
		{
			name:        "PXE persistent EFI",
			dev:         BootDevPXE,
			opts:        BootOpts{Persistent: true, EFI: true},
			wantFlags:   0xE0,
			wantDevByte: 0x04,
		},
		{
			name:        "disk persistent BIOS/legacy",
			dev:         BootDevDisk,
			opts:        BootOpts{Persistent: true, EFI: false},
			wantFlags:   0xC0, // 0x80 valid | 0x40 persistent
			wantDevByte: 0x08,
		},
		{
			name:        "PXE persistent BIOS/legacy",
			dev:         BootDevPXE,
			opts:        BootOpts{Persistent: true, EFI: false},
			wantFlags:   0xC0,
			wantDevByte: 0x04,
		},
		{
			name:        "disk one-time EFI",
			dev:         BootDevDisk,
			opts:        BootOpts{Persistent: false, EFI: true},
			wantFlags:   0xA0, // 0x80 valid | 0x20 EFI
			wantDevByte: 0x08,
		},
		{
			name:        "PXE one-time BIOS (bare minimum — valid only)",
			dev:         BootDevPXE,
			opts:        BootOpts{Persistent: false, EFI: false},
			wantFlags:   0x80, // valid bit only
			wantDevByte: 0x04,
		},
		{
			name:        "BIOS setup persistent EFI",
			dev:         BootDevBIOS,
			opts:        BootOpts{Persistent: true, EFI: true},
			wantFlags:   0xE0,
			wantDevByte: 0x18,
		},
		{
			name:        "CD persistent BIOS",
			dev:         BootDevCD,
			opts:        BootOpts{Persistent: true, EFI: false},
			wantFlags:   0xC0,
			wantDevByte: 0x14,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			flags, devByte := buildRawBootBytes(tc.dev, tc.opts)
			if flags != tc.wantFlags {
				t.Errorf("flags byte: got 0x%02X, want 0x%02X", flags, tc.wantFlags)
			}
			if devByte != tc.wantDevByte {
				t.Errorf("device byte: got 0x%02X, want 0x%02X", devByte, tc.wantDevByte)
			}
		})
	}
}

// TestBuildRawBootBytes_ValidBitAlwaysSet ensures the valid bit (0x80) is set
// in every combination, because a BMC that receives flags=0x00 will ignore the
// entire chassis bootparam set command.
func TestBuildRawBootBytes_ValidBitAlwaysSet(t *testing.T) {
	devices := []BootDevice{BootDevDisk, BootDevPXE, BootDevBIOS, BootDevCD}
	optsCombinations := []BootOpts{
		{false, false, false},
		{true, false, false},
		{false, true, false},
		{true, true, false},
		{false, false, true},
		{true, true, true},
	}
	for _, dev := range devices {
		for _, opts := range optsCombinations {
			flags, _ := buildRawBootBytes(dev, opts)
			if flags&0x80 == 0 {
				t.Errorf("valid bit (0x80) not set for dev=0x%02X opts=%+v flags=0x%02X",
					byte(dev), opts, flags)
			}
		}
	}
}

// ─── parseBootParam ───────────────────────────────────────────────────────────

func TestParseBootParam_PersistentEFIDisk(t *testing.T) {
	// Captured from a Dell R730 with iDRAC8, boot set to disk/persistent/EFI.
	raw := `Boot parameter version: 1
Boot parameter 5 is valid/unlocked
Boot parameter data: E008000000
Boot Flags :
 - Boot Flag Valid
 - Options apply to all future boots
 - BIOS EFI boot
 - Boot Device Selector : Force Boot from default Hard-Drive
 - Console Redirection control : System Default
 - BIOS verbosity : System Default
 - Force Progress Event Traps : No`

	r := parseBootParam(raw)

	if !r.Valid {
		t.Error("Valid should be true")
	}
	if !r.Persistent {
		t.Error("Persistent should be true")
	}
	if !r.EFI {
		t.Error("EFI should be true")
	}
	if r.Device != BootDevDisk {
		t.Errorf("Device: got 0x%02X, want 0x%02X (BootDevDisk)", byte(r.Device), byte(BootDevDisk))
	}
	if r.DataBytes[0] != 0xE0 {
		t.Errorf("DataBytes[0]: got 0x%02X, want 0xE0", r.DataBytes[0])
	}
	if r.DataBytes[1] != 0x08 {
		t.Errorf("DataBytes[1]: got 0x%02X, want 0x08", r.DataBytes[1])
	}
}

func TestParseBootParam_PersistentBIOSPXE(t *testing.T) {
	// Supermicro X10 with legacy/BIOS PXE, persistent.
	raw := `Boot parameter version: 1
Boot parameter 5 is valid/unlocked
Boot parameter data: C004000000
Boot Flags :
 - Boot Flag Valid
 - Options apply to all future boots
 - Boot Device Selector : Force PXE`

	r := parseBootParam(raw)

	if !r.Valid {
		t.Error("Valid should be true")
	}
	if !r.Persistent {
		t.Error("Persistent should be true")
	}
	if r.EFI {
		t.Error("EFI should be false for BIOS-mode PXE")
	}
	if r.Device != BootDevPXE {
		t.Errorf("Device: got 0x%02X, want 0x%02X (BootDevPXE)", byte(r.Device), byte(BootDevPXE))
	}
}

func TestParseBootParam_OneTimeDisk(t *testing.T) {
	// One-time override — no persistent flag.
	raw := `Boot parameter version: 1
Boot parameter 5 is valid/unlocked
Boot parameter data: 8008000000
Boot Flags :
 - Boot Flag Valid
 - Options apply to next boot only
 - Boot Device Selector : Force Boot from default Hard-Drive`

	r := parseBootParam(raw)

	if !r.Valid {
		t.Error("Valid should be true")
	}
	if r.Persistent {
		t.Error("Persistent should be false for one-time override")
	}
	if r.Device != BootDevDisk {
		t.Errorf("Device: got 0x%02X, want 0x%02X", byte(r.Device), byte(BootDevDisk))
	}
}

func TestParseBootParam_EmptyOutput(t *testing.T) {
	// BMC returns nothing meaningful — should not panic.
	r := parseBootParam("")
	if r == nil {
		t.Fatal("parseBootParam returned nil")
	}
	if r.Valid {
		t.Error("Valid should be false for empty output")
	}
}

func TestParseBootParam_NoDataLine(t *testing.T) {
	// Output without the "Boot parameter data:" line (some older ipmitool versions).
	raw := `Boot parameter version: 1
Boot parameter 5 is valid/unlocked
Boot Flags :
 - Boot Flag Valid`

	r := parseBootParam(raw)
	if r.Valid {
		t.Error("Valid should be false — no data bytes parsed")
	}
}

// ─── parseVendor ─────────────────────────────────────────────────────────────

func TestParseVendor_ByNumericID(t *testing.T) {
	tests := []struct {
		name       string
		mcInfo     string
		wantVendor BMCVendor
	}{
		{
			name: "Dell iDRAC8 by numeric ID 674",
			mcInfo: `Device ID                 : 32
Device Revision           : 1
Firmware Revision         : 2.63
IPMI Version              : 2.0
Manufacturer ID           : 674
Manufacturer Name         : Dell Inc
Product ID                : 256 (0x0100)`,
			wantVendor: VendorDell,
		},
		{
			name: "HPE iLO5 by numeric ID 11",
			mcInfo: `Device ID                 : 1
Device Revision           : 0
Firmware Revision         : 2.72
IPMI Version              : 2.0
Manufacturer ID           : 11
Manufacturer Name         : Hewlett Packard Enterprise
Product ID                : 8192 (0x2000)`,
			wantVendor: VendorHPE,
		},
		{
			name: "Supermicro X10 by numeric ID 10876",
			mcInfo: `Device ID                 : 32
Device Revision           : 1
Firmware Revision         : 3.88
IPMI Version              : 2.0
Manufacturer ID           : 10876
Manufacturer Name         : Super Micro Computer
Product ID                : 2160 (0x0870)`,
			wantVendor: VendorSupermicro,
		},
		{
			name: "Lenovo XCC by numeric ID 19046",
			mcInfo: `Device ID                 : 32
Device Revision           : 1
Firmware Revision         : 4.20
IPMI Version              : 2.0
Manufacturer ID           : 19046
Manufacturer Name         : Lenovo
Product ID                : 1680 (0x0690)`,
			wantVendor: VendorLenovo,
		},
		{
			name: "Older IBM/Lenovo by numeric ID 2",
			mcInfo: `Device ID                 : 32
Manufacturer ID           : 2
Manufacturer Name         : IBM`,
			wantVendor: VendorLenovo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := parseVendor(tc.mcInfo)
			if got != tc.wantVendor {
				t.Errorf("parseVendor: got %q, want %q", got, tc.wantVendor)
			}
		})
	}
}

func TestParseVendor_ByNameFallback(t *testing.T) {
	// BMC reports ID 0 (or omits it) — must fall back to name matching.
	raw := `Device ID                 : 1
Manufacturer ID           : 0
Manufacturer Name         : Super Micro Computer`
	got := parseVendor(raw)
	if got != VendorSupermicro {
		t.Errorf("got %q, want %q", got, VendorSupermicro)
	}
}

func TestParseVendor_UnknownIsGeneric(t *testing.T) {
	raw := `Device ID                 : 1
Manufacturer ID           : 9999
Manufacturer Name         : AcmeBMC Corp`
	got := parseVendor(raw)
	if got != VendorGeneric {
		t.Errorf("got %q, want %q", got, VendorGeneric)
	}
}

func TestParseVendor_EmptyOutputIsGeneric(t *testing.T) {
	got := parseVendor("")
	if got != VendorGeneric {
		t.Errorf("got %q, want %q", got, VendorGeneric)
	}
}

// ─── QuirksFor ────────────────────────────────────────────────────────────────

func TestQuirksFor_DellForcePersistent(t *testing.T) {
	q := QuirksFor(VendorDell)
	if !q.ForcePersistent {
		t.Error("Dell quirk should ForcePersistent")
	}
}

func TestQuirksFor_HPEPowerCycleDelay(t *testing.T) {
	q := QuirksFor(VendorHPE)
	if q.PowerCycleDelay == 0 {
		t.Error("HPE quirk should have a non-zero PowerCycleDelay")
	}
}

func TestQuirksFor_SupermicroRawAndPersistent(t *testing.T) {
	q := QuirksFor(VendorSupermicro)
	if !q.UseRaw {
		t.Error("Supermicro quirk should UseRaw")
	}
	if !q.ForcePersistent {
		t.Error("Supermicro quirk should ForcePersistent")
	}
}

func TestQuirksFor_LenovoSkipVerify(t *testing.T) {
	q := QuirksFor(VendorLenovo)
	if !q.SkipVerify {
		t.Error("Lenovo quirk should SkipVerify")
	}
}

func TestQuirksFor_GenericNoQuirks(t *testing.T) {
	q := QuirksFor(VendorGeneric)
	if q.UseRaw || q.ForcePersistent || q.SkipVerify || q.PowerCycleDelay != 0 {
		t.Errorf("Generic should have no quirks: %+v", q)
	}
}
