// Package network manages switch inventory, Ethernet bond/VLAN profiles, and
// InfiniBand/OpenSM configuration for network-aware node deployment.
//
// Phase 1 implements switch CRUD only. Profile, group assignment, OpenSM, and
// finalize injection are added in subsequent phases.
package network

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clonr/internal/db"
	"github.com/sqoia-dev/clonr/pkg/api"
)

// switchNameRe validates switch and profile names: alphanumeric plus dot, underscore, hyphen.
// Max 64 chars enforced separately.
var switchNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// validSwitchRoles is the set of accepted role values for network_switches.
var validSwitchRoles = map[api.NetworkSwitchRole]struct{}{
	api.NetworkSwitchRoleManagement: {},
	api.NetworkSwitchRoleData:       {},
	api.NetworkSwitchRoleInfiniBand: {},
}

// ErrConflict is returned when a create/update would violate a uniqueness constraint.
var ErrConflict = errors.New("conflict")

// Manager owns DB access for the network module. Safe for concurrent use.
type Manager struct {
	db *db.DB
}

// New creates a new Manager.
func New(database *db.DB) *Manager {
	return &Manager{db: database}
}

// ─── Switch CRUD ──────────────────────────────────────────────────────────────

// ListSwitches returns all registered switches ordered by name.
func (m *Manager) ListSwitches(ctx context.Context) ([]api.NetworkSwitch, error) {
	return m.db.NetworkListSwitches(ctx)
}

// CreateSwitch validates and inserts a new switch record.
// Returns ErrConflict when the name is already in use.
func (m *Manager) CreateSwitch(ctx context.Context, s api.NetworkSwitch) (api.NetworkSwitch, error) {
	if err := validateSwitch(s); err != nil {
		return api.NetworkSwitch{}, err
	}

	existing, err := m.db.NetworkListSwitches(ctx)
	if err != nil {
		return api.NetworkSwitch{}, fmt.Errorf("network: list switches for conflict check: %w", err)
	}
	for _, sw := range existing {
		if sw.Name == s.Name {
			return api.NetworkSwitch{}, fmt.Errorf("%w: switch name %q is already in use", ErrConflict, s.Name)
		}
	}

	now := time.Now().UTC()
	s.ID = uuid.New().String()
	s.CreatedAt = now
	s.UpdatedAt = now

	if err := m.db.NetworkCreateSwitch(ctx, s); err != nil {
		return api.NetworkSwitch{}, fmt.Errorf("network: create switch: %w", err)
	}
	log.Info().Str("name", s.Name).Str("role", string(s.Role)).Msg("network: switch created")
	return s, nil
}

// UpdateSwitch replaces a switch definition. Name conflicts with other records
// are validated before writing.
func (m *Manager) UpdateSwitch(ctx context.Context, id string, s api.NetworkSwitch) (api.NetworkSwitch, error) {
	if err := validateSwitch(s); err != nil {
		return api.NetworkSwitch{}, err
	}

	existing, err := m.db.NetworkListSwitches(ctx)
	if err != nil {
		return api.NetworkSwitch{}, fmt.Errorf("network: list switches for conflict check: %w", err)
	}

	var current *api.NetworkSwitch
	for i, sw := range existing {
		if sw.ID == id {
			current = &existing[i]
			break
		}
	}
	if current == nil {
		return api.NetworkSwitch{}, fmt.Errorf("switch %q not found", id)
	}

	for _, sw := range existing {
		if sw.ID == id {
			continue
		}
		if sw.Name == s.Name {
			return api.NetworkSwitch{}, fmt.Errorf("%w: switch name %q is already in use by a different switch", ErrConflict, s.Name)
		}
	}

	s.ID = id
	s.CreatedAt = current.CreatedAt
	s.UpdatedAt = time.Now().UTC()

	if err := m.db.NetworkUpdateSwitch(ctx, s); err != nil {
		return api.NetworkSwitch{}, fmt.Errorf("network: update switch: %w", err)
	}
	log.Info().Str("id", id).Str("name", s.Name).Str("role", string(s.Role)).Msg("network: switch updated")
	return s, nil
}

// DeleteSwitch removes a switch by ID. Always succeeds if the switch exists.
func (m *Manager) DeleteSwitch(ctx context.Context, id string) error {
	existing, err := m.db.NetworkListSwitches(ctx)
	if err != nil {
		return fmt.Errorf("network: list switches: %w", err)
	}
	var target *api.NetworkSwitch
	for i, sw := range existing {
		if sw.ID == id {
			target = &existing[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("switch %q not found", id)
	}

	if err := m.db.NetworkDeleteSwitch(ctx, id); err != nil {
		return fmt.Errorf("network: delete switch: %w", err)
	}
	log.Info().Str("id", id).Str("name", target.Name).Msg("network: switch deleted")
	return nil
}

// ─── Validation ───────────────────────────────────────────────────────────────

func validateSwitch(s api.NetworkSwitch) error {
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(s.Name) > 64 {
		return fmt.Errorf("name must be 64 characters or fewer")
	}
	if !switchNameRe.MatchString(s.Name) {
		return fmt.Errorf("name must match ^[a-zA-Z0-9._-]+$")
	}
	if _, ok := validSwitchRoles[s.Role]; !ok {
		return fmt.Errorf("role must be one of: management, data, infiniband")
	}
	return nil
}
