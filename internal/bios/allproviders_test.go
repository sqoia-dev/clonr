// allproviders_test.go — registry integration test that imports all three
// vendor packages to verify they register without conflict and that Lookup
// resolves each one correctly.
package bios_test

import (
	"testing"

	_ "github.com/sqoia-dev/clustr/internal/bios/dell"       // register Dell provider
	_ "github.com/sqoia-dev/clustr/internal/bios/intel"      // register Intel provider
	_ "github.com/sqoia-dev/clustr/internal/bios/supermicro" // register Supermicro provider

	"github.com/sqoia-dev/clustr/internal/bios"
)

// TestAllProvidersRegister verifies that all three vendor packages register
// cleanly and that Lookup resolves each one.
func TestAllProvidersRegister(t *testing.T) {
	vendors := []string{"intel", "dell", "supermicro"}
	for _, vendor := range vendors {
		p, err := bios.Lookup(vendor)
		if err != nil {
			t.Errorf("Lookup(%q): unexpected error: %v", vendor, err)
			continue
		}
		if p.Vendor() != vendor {
			t.Errorf("Lookup(%q).Vendor() = %q, want %q", vendor, p.Vendor(), vendor)
		}
	}
}

// TestAllProvidersRegisteredVendors verifies that RegisteredVendors contains
// all three expected vendors.
func TestAllProvidersRegisteredVendors(t *testing.T) {
	vendors := bios.RegisteredVendors()
	want := map[string]bool{"intel": true, "dell": true, "supermicro": true}

	for _, v := range vendors {
		delete(want, v)
	}
	if len(want) > 0 {
		t.Errorf("RegisteredVendors missing: %v", want)
	}
}

// TestAllProvidersLookupUnknown verifies that Lookup returns an error for an
// unregistered vendor.
func TestAllProvidersLookupUnknown(t *testing.T) {
	_, err := bios.Lookup("hp")
	if err == nil {
		t.Error("Lookup(hp): expected error for unregistered vendor, got nil")
	}
}
