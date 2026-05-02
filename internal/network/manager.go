// Package network manages switch inventory, Ethernet bond/VLAN profiles, and
// InfiniBand/OpenSM configuration for network-aware node deployment.
//
// Phase 1 implemented switch CRUD. Phase 2 adds profile CRUD, group assignment,
// OpenSM config, and IB status. Finalize injection is added in Phase 4.
package network

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
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

// validBondModes is the set of NetworkManager-accepted bond mode strings.
var validBondModes = map[string]struct{}{
	"802.3ad":      {},
	"active-backup": {},
	"balance-rr":   {},
	"balance-xor":  {},
	"broadcast":    {},
	"balance-alb":  {},
	"balance-tlb":  {},
}

// ErrConflict is returned when a create/update would violate a uniqueness constraint.
var ErrConflict = errors.New("conflict")

// ErrProfileInUse is returned when a profile cannot be deleted because groups reference it.
var ErrProfileInUse = errors.New("profile_in_use")

// Manager owns DB access for the network module. Safe for concurrent use.
type Manager struct {
	db *db.DB
	// StagingDB is optional. When set, mutation handlers honour ?stage=true
	// by writing to pending_changes instead of applying immediately (#154).
	StagingDB StagingIface
}

// New creates a new Manager.
func New(database *db.DB) *Manager {
	return &Manager{db: database}
}

// SetStagingDB wires the staging DB into the network manager.
func (m *Manager) SetStagingDB(s StagingIface) {
	m.StagingDB = s
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

// ConfirmSwitch transitions a "discovered" switch to "confirmed" status and
// applies admin-supplied name and role. Validates the incoming fields.
func (m *Manager) ConfirmSwitch(ctx context.Context, id string, s api.NetworkSwitch) (api.NetworkSwitch, error) {
	if err := validateSwitch(s); err != nil {
		return api.NetworkSwitch{}, err
	}

	existing, err := m.db.NetworkListSwitches(ctx)
	if err != nil {
		return api.NetworkSwitch{}, fmt.Errorf("network: confirm switch: list: %w", err)
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

	// Check name uniqueness against other switches.
	for _, sw := range existing {
		if sw.ID == id {
			continue
		}
		if sw.Name == s.Name {
			return api.NetworkSwitch{}, fmt.Errorf("%w: switch name %q is already in use", ErrConflict, s.Name)
		}
	}

	s.ID = id
	s.CreatedAt = current.CreatedAt
	s.UpdatedAt = time.Now().UTC()
	s.Status = "confirmed"
	s.MACAddress = current.MACAddress
	s.DiscoveredAt = current.DiscoveredAt
	if s.PortCount == 0 {
		s.PortCount = current.PortCount
	}

	if err := m.db.NetworkUpdateSwitch(ctx, s); err != nil {
		return api.NetworkSwitch{}, fmt.Errorf("network: confirm switch: %w", err)
	}
	log.Info().Str("id", id).Str("name", s.Name).Str("role", string(s.Role)).
		Msg("network: discovered switch confirmed")
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

// ─── Profile CRUD ─────────────────────────────────────────────────────────────

// ListProfiles returns all profiles with their bonds, bond members, and IB profiles.
func (m *Manager) ListProfiles(ctx context.Context) ([]api.NetworkProfile, error) {
	return m.db.NetworkListProfiles(ctx)
}

// GetProfile returns a single profile by ID with full nested data.
func (m *Manager) GetProfile(ctx context.Context, id string) (api.NetworkProfile, error) {
	return m.db.NetworkGetProfile(ctx, id)
}

// CreateProfile validates and inserts a new profile with its bonds, members, and IB profile.
// Returns ErrConflict when the name is already in use.
func (m *Manager) CreateProfile(ctx context.Context, p api.NetworkProfile) (api.NetworkProfile, error) {
	if err := validateProfile(p); err != nil {
		return api.NetworkProfile{}, err
	}

	// Check name uniqueness.
	existing, err := m.db.NetworkListProfiles(ctx)
	if err != nil {
		return api.NetworkProfile{}, fmt.Errorf("network: list profiles for conflict check: %w", err)
	}
	for _, ep := range existing {
		if ep.Name == p.Name {
			return api.NetworkProfile{}, fmt.Errorf("%w: profile name %q is already in use", ErrConflict, p.Name)
		}
	}

	now := time.Now().UTC()
	p.ID = uuid.New().String()
	p.CreatedAt = now
	p.UpdatedAt = now

	// Assign IDs to bonds, members, and IB profile.
	for i := range p.Bonds {
		p.Bonds[i].ID = uuid.New().String()
		p.Bonds[i].ProfileID = p.ID
		p.Bonds[i].CreatedAt = now
		p.Bonds[i].UpdatedAt = now
		for j := range p.Bonds[i].Members {
			p.Bonds[i].Members[j].ID = uuid.New().String()
			p.Bonds[i].Members[j].BondID = p.Bonds[i].ID
		}
	}
	if p.IB != nil {
		ib := *p.IB
		ib.ID = uuid.New().String()
		ib.ProfileID = p.ID
		ib.CreatedAt = now
		ib.UpdatedAt = now
		if ib.PKeys == nil {
			ib.PKeys = []string{}
		}
		p.IB = &ib
	}

	if err := m.db.NetworkCreateProfile(ctx, p); err != nil {
		return api.NetworkProfile{}, fmt.Errorf("network: create profile: %w", err)
	}
	log.Info().Str("id", p.ID).Str("name", p.Name).Msg("network: profile created")
	return p, nil
}

// UpdateProfile replaces a profile's bonds, members, and IB profile transactionally.
// Returns ErrConflict when the new name conflicts with another profile.
func (m *Manager) UpdateProfile(ctx context.Context, id string, p api.NetworkProfile) (api.NetworkProfile, error) {
	if err := validateProfile(p); err != nil {
		return api.NetworkProfile{}, err
	}

	// Confirm the profile exists and get its created_at.
	current, err := m.db.NetworkGetProfile(ctx, id)
	if err != nil {
		return api.NetworkProfile{}, err
	}

	// Check name uniqueness against other profiles.
	existing, err := m.db.NetworkListProfiles(ctx)
	if err != nil {
		return api.NetworkProfile{}, fmt.Errorf("network: list profiles for conflict check: %w", err)
	}
	for _, ep := range existing {
		if ep.ID == id {
			continue
		}
		if ep.Name == p.Name {
			return api.NetworkProfile{}, fmt.Errorf("%w: profile name %q is already in use", ErrConflict, p.Name)
		}
	}

	now := time.Now().UTC()
	p.ID = id
	p.CreatedAt = current.CreatedAt
	p.UpdatedAt = now

	// Assign fresh IDs to new bonds/members (old ones are deleted in the transaction).
	for i := range p.Bonds {
		p.Bonds[i].ID = uuid.New().String()
		p.Bonds[i].ProfileID = id
		p.Bonds[i].CreatedAt = now
		p.Bonds[i].UpdatedAt = now
		for j := range p.Bonds[i].Members {
			p.Bonds[i].Members[j].ID = uuid.New().String()
			p.Bonds[i].Members[j].BondID = p.Bonds[i].ID
		}
	}
	if p.IB != nil {
		ib := *p.IB
		ib.ID = uuid.New().String()
		ib.ProfileID = id
		ib.CreatedAt = now
		ib.UpdatedAt = now
		if ib.PKeys == nil {
			ib.PKeys = []string{}
		}
		p.IB = &ib
	}

	if err := m.db.NetworkUpdateProfile(ctx, p); err != nil {
		return api.NetworkProfile{}, fmt.Errorf("network: update profile: %w", err)
	}
	log.Info().Str("id", id).Str("name", p.Name).Msg("network: profile updated")
	return p, nil
}

// DeleteProfile removes a profile by ID.
// Returns ErrProfileInUse (with group names in the message) when groups reference it.
func (m *Manager) DeleteProfile(ctx context.Context, id string) ([]string, error) {
	// Confirm exists.
	if _, err := m.db.NetworkGetProfile(ctx, id); err != nil {
		return nil, err
	}
	groups, err := m.db.NetworkDeleteProfile(ctx, id)
	if err != nil {
		if err.Error() == "profile_in_use" {
			return groups, ErrProfileInUse
		}
		return nil, fmt.Errorf("network: delete profile: %w", err)
	}
	log.Info().Str("id", id).Msg("network: profile deleted")
	return nil, nil
}

// ─── Group assignment ─────────────────────────────────────────────────────────

// AssignProfileToGroup upserts the group → profile mapping.
func (m *Manager) AssignProfileToGroup(ctx context.Context, groupID, profileID string) error {
	// Verify the profile exists.
	if _, err := m.db.NetworkGetProfile(ctx, profileID); err != nil {
		return err
	}
	if err := m.db.NetworkAssignProfileToGroup(ctx, groupID, profileID); err != nil {
		return fmt.Errorf("network: assign profile to group: %w", err)
	}
	log.Info().Str("group_id", groupID).Str("profile_id", profileID).Msg("network: profile assigned to group")
	return nil
}

// UnassignProfileFromGroup removes the group → profile mapping.
func (m *Manager) UnassignProfileFromGroup(ctx context.Context, groupID string) error {
	if err := m.db.NetworkUnassignProfileFromGroup(ctx, groupID); err != nil {
		return fmt.Errorf("network: unassign profile from group: %w", err)
	}
	log.Info().Str("group_id", groupID).Msg("network: network profile unassigned from group")
	return nil
}

// GetGroupProfile returns the profile assigned to a group, or nil if none.
func (m *Manager) GetGroupProfile(ctx context.Context, groupID string) (*api.NetworkProfile, error) {
	return m.db.NetworkGetGroupProfile(ctx, groupID)
}

// ─── OpenSM config ────────────────────────────────────────────────────────────

// GetOpenSMConfig returns the current OpenSM config, or nil if not yet configured.
func (m *Manager) GetOpenSMConfig(ctx context.Context) (*api.OpenSMConfig, error) {
	return m.db.NetworkGetOpenSMConfig(ctx)
}

// SetOpenSMConfig upserts the OpenSM config. Creates a new row if none exists;
// updates the existing row otherwise.
func (m *Manager) SetOpenSMConfig(ctx context.Context, cfg api.OpenSMConfig) (api.OpenSMConfig, error) {
	if err := validateOpenSMConfig(cfg); err != nil {
		return api.OpenSMConfig{}, err
	}

	now := time.Now().UTC()
	existing, err := m.db.NetworkGetOpenSMConfig(ctx)
	if err != nil {
		return api.OpenSMConfig{}, fmt.Errorf("network: get opensm config: %w", err)
	}
	if existing != nil {
		cfg.ID = existing.ID
		cfg.CreatedAt = existing.CreatedAt
	} else {
		cfg.ID = uuid.New().String()
		cfg.CreatedAt = now
	}
	cfg.UpdatedAt = now

	if cfg.LogPrefix == "" {
		cfg.LogPrefix = "/var/log/opensm"
	}

	if err := m.db.NetworkSetOpenSMConfig(ctx, cfg); err != nil {
		return api.OpenSMConfig{}, fmt.Errorf("network: set opensm config: %w", err)
	}
	log.Info().Bool("enabled", cfg.Enabled).Msg("network: opensm config updated")
	return cfg, nil
}

// ─── IB status ────────────────────────────────────────────────────────────────

// HasUnmanagedIBSwitch returns true if any registered IB switch has is_managed=false.
func (m *Manager) HasUnmanagedIBSwitch(ctx context.Context) (bool, error) {
	return m.db.NetworkHasUnmanagedIBSwitch(ctx)
}

// IBStatus holds the computed IB status for the cluster.
type IBStatus struct {
	HasUnmanagedIBSwitch bool `json:"has_unmanaged_ib_switch"`
	OpenSMRequired       bool `json:"opensm_required"`
	OpenSMConfigured     bool `json:"opensm_configured"`
}

// GetIBStatus returns a summary of the cluster's IB state.
func (m *Manager) GetIBStatus(ctx context.Context) (IBStatus, error) {
	hasUnmanaged, err := m.db.NetworkHasUnmanagedIBSwitch(ctx)
	if err != nil {
		return IBStatus{}, fmt.Errorf("network: ib status check: %w", err)
	}

	opensmCfg, err := m.db.NetworkGetOpenSMConfig(ctx)
	if err != nil {
		return IBStatus{}, fmt.Errorf("network: get opensm config for ib status: %w", err)
	}
	configured := opensmCfg != nil && opensmCfg.Enabled

	return IBStatus{
		HasUnmanagedIBSwitch: hasUnmanaged,
		OpenSMRequired:       hasUnmanaged,
		OpenSMConfigured:     configured,
	}, nil
}

// ─── Switch auto-discovery ────────────────────────────────────────────────────

// HandleDiscoveredSwitch is called by the DHCP server when it detects a switch
// fingerprint in Option 60. It creates a draft switch record with status
// "discovered" if no switch with the same MAC or mgmt_ip already exists.
// If a switch with a matching mgmt_ip is found and its vendor is empty, the
// vendor is updated. This is non-fatal — errors are logged, not propagated.
func (m *Manager) HandleDiscoveredSwitch(ctx context.Context, mac, vendor, ip string) error {
	// Normalize the last 6 hex chars of the MAC for a readable default name.
	cleanMAC := strings.ReplaceAll(mac, ":", "")
	suffix := cleanMAC
	if len(cleanMAC) >= 6 {
		suffix = cleanMAC[len(cleanMAC)-6:]
	}

	existing, err := m.db.NetworkListSwitches(ctx)
	if err != nil {
		return fmt.Errorf("network: discover switch: list: %w", err)
	}

	// Check if we already know this switch by MAC or by mgmt_ip.
	for i := range existing {
		sw := &existing[i]
		if sw.MACAddress == mac {
			log.Debug().Str("mac", mac).Str("name", sw.Name).
				Msg("network: discovered switch already in DB by MAC — skipping")
			return nil
		}
		if ip != "" && sw.MgmtIP == ip {
			// Update vendor if it was blank.
			if sw.Vendor == "" && vendor != "" {
				sw.Vendor = vendor
				sw.UpdatedAt = time.Now().UTC()
				if err := m.db.NetworkUpdateSwitch(ctx, *sw); err != nil {
					log.Warn().Err(err).Str("id", sw.ID).Msg("network: could not update vendor on discovered switch")
				}
			}
			log.Debug().Str("ip", ip).Str("name", sw.Name).
				Msg("network: discovered switch already in DB by mgmt_ip — skipping")
			return nil
		}
	}

	// Create a new draft switch.
	now := time.Now().UTC()
	s := api.NetworkSwitch{
		ID:          uuid.New().String(),
		Name:        "discovered-" + suffix,
		Role:        "unknown", // placeholder until admin confirms
		Vendor:      vendor,
		MgmtIP:      ip,
		MACAddress:  mac,
		Status:      "discovered",
		DiscoveredAt: &now,
		IsManaged:   true,
		PortCount:   48,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := m.db.NetworkCreateSwitch(ctx, s); err != nil {
		return fmt.Errorf("network: discover switch: create: %w", err)
	}
	log.Info().Str("mac", mac).Str("vendor", vendor).Str("ip", ip).
		Str("name", s.Name).Msg("network: auto-discovered switch created")
	return nil
}

// ─── Deploy pipeline ──────────────────────────────────────────────────────────

// NodeNetworkConfig resolves the effective network config for a node given its
// GroupID. Returns nil if no profile is assigned to the group or groupID is empty.
// When the group's profile matches the opensm head_node_profile_id and opensm is
// enabled, OpenSMConf is populated.
func (m *Manager) NodeNetworkConfig(ctx context.Context, groupID string) (*api.NetworkNodeConfig, error) {
	if groupID == "" {
		return nil, nil
	}

	profile, err := m.db.NetworkGetGroupProfile(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("network: get group profile for node config: %w", err)
	}
	if profile == nil {
		return nil, nil
	}

	result := &api.NetworkNodeConfig{
		Bonds: profile.Bonds,
		IB:    profile.IB,
	}

	// Check whether OpenSM config should be injected for this node's group.
	opensmCfg, err := m.db.NetworkGetOpenSMConfig(ctx)
	if err != nil {
		// Non-fatal: log and continue without opensm config.
		log.Warn().Err(err).Msg("network: could not fetch opensm config for node config (non-fatal)")
	} else if opensmCfg != nil && opensmCfg.Enabled &&
		opensmCfg.HeadNodeProfileID == profile.ID {
		result.OpenSMConf = opensmCfg.ConfContent
	}

	return result, nil
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

func validateProfile(p api.NetworkProfile) error {
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(p.Name) > 64 {
		return fmt.Errorf("name must be 64 characters or fewer")
	}
	if !switchNameRe.MatchString(p.Name) {
		return fmt.Errorf("name must match ^[a-zA-Z0-9._-]+$")
	}

	for i, b := range p.Bonds {
		if err := validateBond(b, i); err != nil {
			return err
		}
	}

	if p.IB != nil {
		if err := validateIBProfile(*p.IB); err != nil {
			return err
		}
	}
	return nil
}

func validateBond(b api.BondConfig, idx int) error {
	prefix := fmt.Sprintf("bond[%d]", idx)
	if b.BondName == "" {
		return fmt.Errorf("%s: bond_name is required", prefix)
	}
	if _, ok := validBondModes[b.Mode]; !ok {
		return fmt.Errorf("%s: mode must be one of: 802.3ad, active-backup, balance-rr, balance-xor, broadcast, balance-alb, balance-tlb", prefix)
	}
	if b.MTU != 0 && (b.MTU < 576 || b.MTU > 65535) {
		return fmt.Errorf("%s: mtu must be 576–65535", prefix)
	}
	if b.VLANID < 0 || b.VLANID > 4094 {
		return fmt.Errorf("%s: vlan_id must be 0 (none) or 1–4094", prefix)
	}
	if b.IPMethod != "" && b.IPMethod != "static" && b.IPMethod != "dhcp" && b.IPMethod != "none" {
		return fmt.Errorf("%s: ip_method must be static, dhcp, or none", prefix)
	}
	if len(b.Members) == 0 {
		return fmt.Errorf("%s: at least one member is required", prefix)
	}
	for j, m := range b.Members {
		if strings.TrimSpace(m.MatchMAC) == "" && strings.TrimSpace(m.MatchName) == "" {
			return fmt.Errorf("%s: member[%d]: match_mac or match_name must be non-empty", prefix, j)
		}
	}
	return nil
}

func validateIBProfile(ib api.IBProfile) error {
	if ib.IPoIBMode != "" && ib.IPoIBMode != "connected" && ib.IPoIBMode != "datagram" {
		return fmt.Errorf("ib: ipoib_mode must be connected or datagram")
	}
	if ib.IPMethod != "" && ib.IPMethod != "static" && ib.IPMethod != "dhcp" && ib.IPMethod != "none" {
		return fmt.Errorf("ib: ip_method must be static, dhcp, or none")
	}
	return nil
}

func validateOpenSMConfig(cfg api.OpenSMConfig) error {
	const maxConfSize = 64 * 1024 // 64 KiB
	if len(cfg.ConfContent) > maxConfSize {
		return fmt.Errorf("conf_content must be 64 KiB or smaller")
	}
	if cfg.SMPriority < 0 || cfg.SMPriority > 15 {
		return fmt.Errorf("sm_priority must be 0–15")
	}
	return nil
}
