package bios

import (
	"fmt"
	"sync"
)

// registry holds all registered Provider implementations, keyed by vendor
// identifier.  It is populated by each vendor package's init() function via
// Register.  The zero value is not usable; use the package-level functions
// Register and Lookup which operate on the global defaultRegistry.
var defaultRegistry = &registry{
	providers: make(map[string]Provider),
}

type registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// Register adds p to the global registry.  Typically called from a vendor
// package's init() function.  Panics if a provider for p.Vendor() is already
// registered — duplicate registrations are a programming error.
func Register(p Provider) {
	defaultRegistry.mu.Lock()
	defer defaultRegistry.mu.Unlock()
	v := p.Vendor()
	if _, exists := defaultRegistry.providers[v]; exists {
		panic(fmt.Sprintf("bios: vendor %q already registered", v))
	}
	defaultRegistry.providers[v] = p
}

// Lookup returns the Provider for the given vendor identifier, or an error if
// no provider is registered for that vendor.
//
// Vendor identifiers are the lowercase strings stored in bios_profiles.vendor
// ("intel", "dell", "supermicro").  Lookup is safe for concurrent use.
func Lookup(vendor string) (Provider, error) {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	p, ok := defaultRegistry.providers[vendor]
	if !ok {
		return nil, fmt.Errorf("bios: no provider registered for vendor %q", vendor)
	}
	return p, nil
}

// RegisteredVendors returns all currently registered vendor identifiers.
// Order is non-deterministic.
func RegisteredVendors() []string {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	vendors := make([]string, 0, len(defaultRegistry.providers))
	for v := range defaultRegistry.providers {
		vendors = append(vendors, v)
	}
	return vendors
}
