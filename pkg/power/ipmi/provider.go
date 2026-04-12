// Package ipmi wraps pkg/ipmi as a power.Provider.
// Register is called from an init() so the server only needs to import this
// package with a blank import to make the "ipmi" provider available.
package ipmi

import (
	"context"
	"fmt"

	"github.com/sqoia-dev/clonr/pkg/ipmi"
	"github.com/sqoia-dev/clonr/pkg/power"
)

// Provider adapts ipmi.Client to the power.Provider interface.
type Provider struct {
	client *ipmi.Client
}

// New constructs a Provider from a ProviderConfig.
// Required fields: "host", "username", "password".
func New(cfg power.ProviderConfig) (power.Provider, error) {
	host := cfg.Fields["host"]
	if host == "" {
		return nil, fmt.Errorf("ipmi provider: missing required field \"host\"")
	}
	return &Provider{
		client: &ipmi.Client{
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
	case ipmi.PowerOn:
		return power.PowerOn, nil
	case ipmi.PowerOff:
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
// Maps BootDevice constants to the appropriate ipmitool chassis bootdev command.
func (p *Provider) SetNextBoot(ctx context.Context, dev power.BootDevice) error {
	switch dev {
	case power.BootPXE:
		return p.client.SetBootPXE(ctx)
	case power.BootDisk:
		return p.client.SetBootDisk(ctx)
	default:
		return fmt.Errorf("ipmi provider: unsupported boot device %q", dev)
	}
}

// SetPersistentBootOrder implements power.Provider.
// IPMI chassis bootdev commands are one-time only; persistent order is not
// supported via ipmitool in the standard way used here.
func (p *Provider) SetPersistentBootOrder(_ context.Context, _ []power.BootDevice) error {
	return power.ErrNotSupported
}
