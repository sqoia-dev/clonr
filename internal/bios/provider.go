// Package bios defines the vendor-agnostic BIOS settings Provider interface
// and the global provider registry.
//
// # Provider interface
//
// Each vendor ships one file implementing Provider and self-registers from
// its init() function.  clustr's deploy and drift-detection paths use the
// registry to look up the right provider by vendor identifier.  Adding a new
// vendor is purely additive — no changes to this file, no changes to deploy
// logic:
//
//	internal/bios/dell/dell.go    — Dell racadm provider
//	internal/bios/supermicro/sm.go — Supermicro SUM provider
//
// # Operator binary supply chain
//
// Intel SYSCFG (and future vendor tools) are NOT redistributed by clustr.
// The operator places the binary at /var/lib/clustr/vendor-bios/<vendor>/<binary>
// on the clustr server.  See docs/BIOS-INTEL-SETUP.md for the Intel workflow.
// When the binary is absent ReadCurrent and Apply return ErrBinaryMissing.
// Builds and deploments continue; the error is recorded in node_bios_profile.last_apply_error
// with the operator runbook URL.
//
// # Settings format
//
// settings_json is an opaque flat JSON object: { "setting-name": "value" }.
// clustr does not own the schema; keys and values are vendor-defined.
// The Intel/SYSCFG convention is documented in docs/BIOS-INTEL-SETUP.md.
package bios

import (
	"context"
	"errors"
)

// ErrBinaryMissing is returned by ReadCurrent and Apply when the vendor binary
// is not present at the expected operator-supplied path.  Callers should surface
// the operator runbook URL from the error rather than treating it as a fatal
// system error.
var ErrBinaryMissing = errors.New("bios: vendor binary not present at operator-configured path")

// Setting is one BIOS key/value pair as returned by the vendor binary.
// Both Name and Value are opaque strings — their meaning is vendor-defined.
type Setting struct {
	Name  string `json:"name"`  // e.g. "Intel(R) Hyper-Threading Technology"
	Value string `json:"value"` // e.g. "Enable"
}

// Change represents a single BIOS setting transition: a setting that must move
// from its current value (From) to a desired value (To).
type Change struct {
	Setting
	From string `json:"from"` // current value on this node
	To   string `json:"to"`   // desired value from the assigned profile
}

// Provider is the vendor-agnostic interface for BIOS settings management.
// Each vendor implementation wraps its CLI tool and translates between
// clustr's Change/Setting types and the vendor's wire format.
//
// All implementations must be safe for concurrent use from multiple goroutines.
type Provider interface {
	// Vendor returns the vendor identifier: "intel", "dell", "supermicro".
	// Must match the vendor column in bios_profiles rows.
	Vendor() string

	// ReadCurrent returns all BIOS settings readable by the vendor binary on
	// this node.  Returns ErrBinaryMissing when the operator has not placed the
	// vendor binary at its expected path.
	//
	// Called from inside initramfs (as root, direct exec) and from clientd (as
	// root on the node, direct exec — no privhelper needed for reads).
	ReadCurrent(ctx context.Context) ([]Setting, error)

	// Diff computes the minimal change set to bring current → desired.
	//
	// Settings present in desired but not in current are included (new setting).
	// Settings present in current but absent in desired are ignored (partial
	// override — clustr never resets settings the profile doesn't mention).
	// Settings in both with equal values produce no Change entry.
	//
	// Name comparison is case-insensitive per Intel SYSCFG convention.
	// All implementations must honour this.
	Diff(desired, current []Setting) ([]Change, error)

	// Apply writes the change set to the BIOS.  Caller has already run Diff to
	// produce changes.  Returns the subset of changes that were actually applied
	// (vendor binary may report some as "already set" or "deferred until POST").
	//
	// Inside initramfs Apply may exec the binary directly.  Post-boot, callers
	// MUST route through privhelper (bios-apply verb) so execution is audited.
	Apply(ctx context.Context, changes []Change) ([]Change, error)

	// SupportedSettings returns vendor-known setting names.  Used at profile
	// create-time to validate the settings_json keys.  May return an empty slice
	// when the binary is absent (validation is then skipped with a warning).
	SupportedSettings(ctx context.Context) ([]string, error)
}
