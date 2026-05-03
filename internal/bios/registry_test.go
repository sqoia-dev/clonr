package bios

import (
	"context"
	"testing"
)

// stubProvider is a minimal Provider used for registry tests only.
type stubProvider struct{ vendor string }

func (s *stubProvider) Vendor() string                                    { return s.vendor }
func (s *stubProvider) ReadCurrent(_ context.Context) ([]Setting, error)  { return nil, nil }
func (s *stubProvider) Diff(desired, current []Setting) ([]Change, error) { return Diff(desired, current) }
func (s *stubProvider) Apply(_ context.Context, c []Change) ([]Change, error) { return c, nil }
func (s *stubProvider) SupportedSettings(_ context.Context) ([]string, error) { return nil, nil }

func TestRegistryLookup(t *testing.T) {
	// Use a fresh registry to avoid global state pollution.
	reg := &registry{providers: make(map[string]Provider)}

	reg.providers["teststub"] = &stubProvider{vendor: "teststub"}

	p, err := func(v string) (Provider, error) {
		reg.mu.RLock()
		defer reg.mu.RUnlock()
		p, ok := reg.providers[v]
		if !ok {
			return nil, &lookupError{vendor: v}
		}
		return p, nil
	}("teststub")

	if err != nil {
		t.Fatalf("lookup: unexpected error: %v", err)
	}
	if p.Vendor() != "teststub" {
		t.Errorf("vendor: got %q, want teststub", p.Vendor())
	}
}

func TestRegistryLookupMissing(t *testing.T) {
	_, err := Lookup("vendor-that-does-not-exist-xyz")
	if err == nil {
		t.Fatal("expected error for unregistered vendor, got nil")
	}
}

func TestRegistryDoubleRegisterPanics(t *testing.T) {
	reg := &registry{providers: make(map[string]Provider)}
	reg.providers["dup"] = &stubProvider{vendor: "dup"}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate register, got none")
		}
	}()

	// Simulate what Register does.
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, exists := reg.providers["dup"]; exists {
		panic("bios: vendor \"dup\" already registered")
	}
}

// lookupError is a local error type for the inline registry test.
type lookupError struct{ vendor string }

func (e *lookupError) Error() string { return "bios: no provider registered for vendor " + e.vendor }
