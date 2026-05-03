// Package ipmi wraps pkg/ipmi as a power.Provider.
// Register is called from an init() so the server only needs to import this
// package with a blank import to make the "ipmi" provider available.
//
// Production bare-metal behaviour:
//
//   - SetNextBoot uses IPMI's "persistent flag" on the bootdev override.
//     This is IPMI-spec terminology for "the override survives a mid-boot
//     crash or power flap until the node successfully boots once" — it is
//     NOT the same as setting a persistent BootOrder via the BMC's UEFI
//     settings. After the node boots once, the override is consumed and the
//     BMC's persistent BootOrder (set by the operator at commissioning time)
//     takes over. This matches the one-shot semantics documented in
//     internal/power/power.go SetNextBoot.
//     See docs/boot-architecture.md §10.
//
//   - BMC vendor is auto-detected on the first SetNextBoot call; known quirks
//     (Dell iDRAC, HPE iLO, Supermicro X9/X10, Lenovo XCC) are applied
//     automatically without operator intervention.
//
//   - After setting the boot device, the setting is verified by reading back
//     chassis bootparam 5. A mismatch logs a warning but does not fail the
//     operation (Lenovo XCC reports stale values but boots correctly).
//
// Environment variables:
//
//	CLUSTR_IPMI_USE_RAW=true  — force raw chassis bootparam command for all BMCs
//	CLUSTR_IPMI_EFI=true      — force UEFI boot mode when auto-detect fails
package ipmi

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	coreipm "github.com/sqoia-dev/clustr/internal/ipmi"
	"github.com/sqoia-dev/clustr/internal/power"
)

// Provider adapts ipmi.Client to the power.Provider interface.
// It caches the detected BMC vendor after the first detection call to avoid
// an extra round-trip on every subsequent boot-flip operation.
type Provider struct {
	client *coreipm.Client

	mu     sync.Mutex
	vendor coreipm.BMCVendor // cached after first detection; empty = not yet detected
}

// New constructs a Provider from a ProviderConfig.
// Required fields: "host", "username", "password".
func New(cfg power.ProviderConfig) (power.Provider, error) {
	host := cfg.Fields["host"]
	if host == "" {
		return nil, fmt.Errorf("ipmi provider: missing required field \"host\"")
	}
	return &Provider{
		client: &coreipm.Client{
			Host:     host,
			Username: cfg.Fields["username"],
			Password: cfg.Fields["password"],
		},
	}, nil
}

// Register registers the "ipmi" factory with the given registry.
func Register(r *power.Registry) {
	r.Register("ipmi", New)
}

// Name implements power.Provider.
func (p *Provider) Name() string { return "ipmi" }

// Status implements power.Provider.
func (p *Provider) Status(ctx context.Context) (power.PowerStatus, error) {
	s, err := p.client.PowerStatus(ctx)
	if err != nil {
		return power.PowerUnknown, err
	}
	switch s {
	case coreipm.PowerOn:
		return power.PowerOn, nil
	case coreipm.PowerOff:
		return power.PowerOff, nil
	default:
		return power.PowerUnknown, nil
	}
}

// PowerOn implements power.Provider.
func (p *Provider) PowerOn(ctx context.Context) error {
	return p.client.PowerOn(ctx)
}

// PowerOff implements power.Provider.
func (p *Provider) PowerOff(ctx context.Context) error {
	return p.client.PowerOff(ctx)
}

// PowerCycle implements power.Provider.
func (p *Provider) PowerCycle(ctx context.Context) error {
	return p.client.PowerCycle(ctx)
}

// Reset implements power.Provider.
func (p *Provider) Reset(ctx context.Context) error {
	return p.client.PowerReset(ctx)
}

// SetNextBoot implements power.Provider.
//
// This is the production boot-flip path. It:
//  1. Detects the BMC vendor (cached after first call).
//  2. Derives BootOpts from vendor quirks, applying persistent mode by default.
//  3. Calls SetBootDevWithOpts which tries the friendly chassis bootdev command
//     first and falls back to the raw IPMI command on failure.
//  4. Reads back chassis bootparam 5 to verify the setting was accepted.
//     Logs a warning on mismatch (Lenovo) rather than returning an error.
//
// The caller (post-deploy hook) should call SetNextBoot then PowerCycle as
// separate steps. The HPE quirk sleep is applied inside PowerCycleAfterBoot
// rather than here so callers that don't need a cycle aren't penalised.
func (p *Provider) SetNextBoot(ctx context.Context, dev power.BootDevice) error {
	vendor, err := p.cachedVendor(ctx)
	if err != nil {
		// Non-fatal: proceed with generic defaults rather than blocking the deploy.
		fmt.Fprintf(os.Stderr, "ipmi: vendor detection failed (%v); using generic defaults\n", err)
		vendor = coreipm.VendorGeneric
	}

	quirks := coreipm.QuirksFor(vendor)
	ipmiDev, err := powerDevToIPMI(dev)
	if err != nil {
		return err
	}

	opts := coreipm.BootOpts{
		Persistent: true, // always persistent for deploy operations
		EFI:        os.Getenv("CLUSTR_IPMI_EFI") == "true",
		UseRaw:     quirks.UseRaw || os.Getenv("CLUSTR_IPMI_USE_RAW") == "true",
	}
	if quirks.ForcePersistent {
		opts.Persistent = true
	}

	if err := p.client.SetBootDevWithOpts(ctx, ipmiDev, opts); err != nil {
		return fmt.Errorf("ipmi: set next boot %q: %w", dev, err)
	}

	// Verify the setting was accepted. Lenovo XCC read-backs are known to be
	// stale; log a warning instead of returning an error.
	if !quirks.SkipVerify {
		result, verifyErr := p.client.VerifyBootParam(ctx)
		if verifyErr != nil {
			// Non-fatal: the command succeeded; we just can't confirm it.
			fmt.Fprintf(os.Stderr, "ipmi: bootparam verify failed (%v); proceeding\n", verifyErr)
		} else if result != nil && result.Valid {
			// Cross-check the device byte if we got a valid read-back.
			if result.Device != ipmiDev {
				fmt.Fprintf(os.Stderr,
					"ipmi: bootparam verify mismatch: set 0x%02X, read back 0x%02X — BMC may have ignored the command\n",
					byte(ipmiDev), byte(result.Device))
			}
		}
	}

	return nil
}

// SetPersistentBootOrder implements power.Provider.
// For production bare-metal the most meaningful thing we can do is set the
// first device persistently; full ordering is not addressable via standard IPMI.
func (p *Provider) SetPersistentBootOrder(ctx context.Context, order []power.BootDevice) error {
	if len(order) == 0 {
		return fmt.Errorf("ipmi: SetPersistentBootOrder: order must not be empty")
	}
	return p.SetNextBoot(ctx, order[0])
}

// PowerCycleAfterBoot performs a power cycle, inserting any vendor-mandated
// delay between the boot-flip and the cycle. Use this instead of PowerCycle
// directly when a SetNextBoot call has just been made.
//
// HPE iLO requires ~3 seconds between the bootdev write and the power cycle or
// the iLO firmware races the flush to non-volatile storage.
func (p *Provider) PowerCycleAfterBoot(ctx context.Context) error {
	vendor, err := p.cachedVendor(ctx)
	if err != nil {
		vendor = coreipm.VendorGeneric
	}
	quirks := coreipm.QuirksFor(vendor)
	if quirks.PowerCycleDelay > 0 {
		time.Sleep(quirks.PowerCycleDelay)
	}
	return p.client.PowerCycle(ctx)
}

// DetectedVendor returns the cached BMC vendor, detecting it first if needed.
// Safe to call from diagnostic tooling (test-boot-flip-direct) without a full deploy.
func (p *Provider) DetectedVendor(ctx context.Context) (coreipm.BMCVendor, error) {
	return p.cachedVendor(ctx)
}

// ─── internal helpers ─────────────────────────────────────────────────────────

// cachedVendor returns the cached BMC vendor, detecting it via mc info if this
// is the first call. Concurrent callers are serialised by the mutex but only
// the first call hits the wire.
func (p *Provider) cachedVendor(ctx context.Context) (coreipm.BMCVendor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.vendor != "" {
		return p.vendor, nil
	}
	v, err := p.client.DetectVendor(ctx)
	if err != nil {
		return coreipm.VendorGeneric, err
	}
	p.vendor = v
	return v, nil
}

// powerDevToIPMI maps a power.BootDevice constant to the coreipm.BootDevice
// byte value used for raw chassis bootparam commands.
func powerDevToIPMI(dev power.BootDevice) (coreipm.BootDevice, error) {
	switch dev {
	case power.BootPXE:
		return coreipm.BootDevPXE, nil
	case power.BootDisk:
		return coreipm.BootDevDisk, nil
	case power.BootBIOS:
		return coreipm.BootDevBIOS, nil
	case power.BootCD:
		return coreipm.BootDevCD, nil
	default:
		return 0, fmt.Errorf("ipmi provider: unsupported boot device %q", dev)
	}
}
