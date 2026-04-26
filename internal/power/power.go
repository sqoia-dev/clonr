// Package power defines the provider abstraction for remote power and boot
// management. Each backend (IPMI, Proxmox, vSphere, …) implements Provider.
// The Registry maps provider type names to factory functions so the server can
// instantiate the right backend from a node's stored PowerProviderConfig.
//
// See docs/boot-architecture.md §10 for the contract semantics across IPMI and
// Proxmox, and for why SetNextBoot and SetPersistentBootOrder behave differently
// on each backend while preserving the same observable one-shot guarantee.
package power

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotSupported is returned when a provider does not implement an optional
// operation (e.g. SetPersistentBootOrder on IPMI which has no persistent order).
var ErrNotSupported = errors.New("operation not supported by this provider")

// PowerStatus represents the current power state of a node.
type PowerStatus string

const (
	PowerOn      PowerStatus = "on"
	PowerOff     PowerStatus = "off"
	PowerUnknown PowerStatus = "unknown"
)

// BootDevice names a boot source understood by all providers.
type BootDevice string

const (
	BootDisk BootDevice = "disk"
	BootPXE  BootDevice = "pxe"
	BootCD   BootDevice = "cd"
	BootBIOS BootDevice = "bios"
)

// Provider is implemented by each power management backend.
// Implementations should be safe for concurrent use.
type Provider interface {
	// Name returns a human-readable provider identifier ("ipmi", "proxmox", …).
	Name() string

	// Status returns the current power state.
	Status(ctx context.Context) (PowerStatus, error)

	// PowerOn brings the node out of soft-off.
	PowerOn(ctx context.Context) error

	// PowerOff forcefully powers off the node.
	PowerOff(ctx context.Context) error

	// PowerCycle hard-cycles the node (off then on).
	PowerCycle(ctx context.Context) error

	// Reset warm-resets the node without cycling power.
	Reset(ctx context.Context) error

	// SetNextBoot sets the boot target for the NEXT boot of this node, after
	// which the node returns to its persistent default boot order.
	//
	// Semantics MUST be observable as one-shot from the orchestrator's
	// perspective. Implementations MAY achieve this differently:
	//
	//   - IPMI: issues a non-persistent chassis bootdev override; consumed on
	//     next boot per IPMI spec §28. Pair with PowerCycle to actually boot.
	//
	//   - Proxmox: writes the persistent VM boot order to put dev first,
	//     because Proxmox has no one-shot concept. If the VM is running it
	//     performs an explicit stop → config-write → start so the new order
	//     takes effect immediately. The caller is responsible for restoring
	//     disk-first order after deploy via SetPersistentBootOrder.
	//
	// Callers MUST NOT assume SetNextBoot implies a power cycle — issue
	// PowerCycle separately after this call if needed. Callers MUST issue
	// SetPersistentBootOrder([BootDisk, BootPXE]) after a successful deploy
	// when SetNextBoot(BootPXE) was used (the Proxmox provider requires this
	// to restore disk-first order; the IPMI provider ignores it harmlessly).
	//
	// See docs/boot-architecture.md §10.
	SetNextBoot(ctx context.Context, dev BootDevice) error

	// SetPersistentBootOrder sets the persistent boot order for this node.
	//
	// On Proxmox: writes the VM config boot order. If the VM is currently
	// running it performs a stop → config-write → start so the new order is
	// committed and takes effect on the next boot. This is CRITICAL for the
	// post-deploy flip-back to disk-first: Proxmox only commits pending VM
	// config on a full stop+start, not on /status/reset (warm reset).
	// Returns once the new order is live in the running config.
	//
	// On IPMI: best-effort; the standard chassis bootdev call is one-shot by
	// spec so this is effectively a reaffirmation. Returns ErrNotSupported
	// when the backend has no meaningful persistent-order concept beyond the
	// operator-owned BMC boot sequence set at commissioning time.
	//
	// See docs/boot-architecture.md §10.
	SetPersistentBootOrder(ctx context.Context, order []BootDevice) error
}

// ProviderConfig carries the backend-agnostic configuration envelope that the
// server loads from the database and passes to a ProviderFactory.
type ProviderConfig struct {
	// Type identifies the backend: "ipmi", "proxmox", etc.
	Type string
	// Fields holds all backend-specific key/value pairs (host, credentials, …).
	Fields map[string]string
}

// ProviderFactory constructs a Provider from a ProviderConfig.
type ProviderFactory func(cfg ProviderConfig) (Provider, error)

// Registry maps provider type names to their factory functions.
// Use NewRegistry to create an instance; then call Register for each backend.
type Registry struct {
	factories map[string]ProviderFactory
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]ProviderFactory)}
}

// Register adds a ProviderFactory under the given name.
// Panics if name is empty or already registered — registration is meant to
// happen at startup via init() functions, not at request time.
func (r *Registry) Register(name string, factory ProviderFactory) {
	if name == "" {
		panic("power: Register called with empty name")
	}
	if _, exists := r.factories[name]; exists {
		panic(fmt.Sprintf("power: provider %q already registered", name))
	}
	r.factories[name] = factory
}

// Create instantiates the provider named by cfg.Type.
// Returns an error when the type is unknown or the factory returns an error.
func (r *Registry) Create(cfg ProviderConfig) (Provider, error) {
	factory, ok := r.factories[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("power: unknown provider type %q (registered: %v)", cfg.Type, r.registeredNames())
	}
	return factory(cfg)
}

func (r *Registry) registeredNames() []string {
	names := make([]string, 0, len(r.factories))
	for n := range r.factories {
		names = append(names, n)
	}
	return names
}
