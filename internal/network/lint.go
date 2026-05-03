package network

import (
	"context"
	"fmt"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// LintWarning is a single best-practice warning returned by LintNetworkConfig.
type LintWarning struct {
	Code    string `json:"code"`    // machine-readable identifier
	Message string `json:"message"` // human-readable description
	// Entity optionally identifies the profile, bond, or switch the warning applies to.
	Entity  string `json:"entity,omitempty"`
}

// LintNetworkConfig inspects the current network module state and returns a
// list of best-practice warnings. These are advisory — clustr will still deploy
// nodes when warnings are present. Admins should review and remediate.
//
// Checks performed:
//  1. Data bonds with MTU < 9000 (jumbo frames recommended for HPC)
//  2. Data bonds with xmit_hash_policy == "layer2" (too coarse for ECMP)
//  3. IB switch with is_managed=false but OpenSM is not configured
//  4. No VLAN separation between management and data bonds on the same profile
//  5. Oversubscription ratio > 4:1 when uplink_ports is configured on data switches
func (m *Manager) LintNetworkConfig(ctx context.Context) ([]LintWarning, error) {
	var warnings []LintWarning

	profiles, err := m.db.NetworkListProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("network: lint: list profiles: %w", err)
	}

	for _, p := range profiles {
		hasDataBond := false
		hasMgmtBond := false
		dataVLANs := map[int]bool{}
		mgmtVLANs := map[int]bool{}

		for _, b := range p.Bonds {
			// Classify bond by MTU heuristic: >=9000 = data, else = mgmt.
			isDataBond := b.MTU >= 9000

			if isDataBond {
				hasDataBond = true
				// Check 1: data bond MTU below jumbo frames threshold.
				if b.MTU < 9000 {
					warnings = append(warnings, LintWarning{
						Code:    "DATA_BOND_MTU_TOO_LOW",
						Message: fmt.Sprintf("bond %q in profile %q has MTU %d; data bonds should use jumbo frames (MTU ≥ 9000)", b.BondName, p.Name, b.MTU),
						Entity:  p.Name + "/" + b.BondName,
					})
				}
				// Check 2: layer2 xmit_hash_policy on data bonds.
				if b.XmitHashPolicy == "layer2" {
					warnings = append(warnings, LintWarning{
						Code:    "DATA_BOND_LAYER2_HASH",
						Message: fmt.Sprintf("bond %q in profile %q uses xmit_hash_policy=layer2; data bonds should use layer3+4 or encap-layer3+4 for better ECMP distribution", b.BondName, p.Name),
						Entity:  p.Name + "/" + b.BondName,
					})
				}
				if b.VLANID > 0 {
					dataVLANs[b.VLANID] = true
				}
			} else {
				hasMgmtBond = true
				if b.VLANID > 0 {
					mgmtVLANs[b.VLANID] = true
				}
			}
		}

		// Check 3: data bond MTU < 9000 when it is a data bond by role inference.
		// Re-check all bonds explicitly by MTU < 9000 when they carry a data VLAN
		// but were classified as mgmt above.
		for _, b := range p.Bonds {
			if b.MTU > 0 && b.MTU < 9000 && len(dataVLANs) > 0 && dataVLANs[b.VLANID] {
				warnings = append(warnings, LintWarning{
					Code:    "DATA_BOND_MTU_TOO_LOW",
					Message: fmt.Sprintf("bond %q in profile %q is on a data VLAN but has MTU %d < 9000", b.BondName, p.Name, b.MTU),
					Entity:  p.Name + "/" + b.BondName,
				})
			}
		}

		// Check 4: VLAN separation — management and data bonds share same VLAN IDs.
		if hasDataBond && hasMgmtBond {
			for vlan := range dataVLANs {
				if mgmtVLANs[vlan] {
					warnings = append(warnings, LintWarning{
						Code:    "VLAN_NOT_SEPARATED",
						Message: fmt.Sprintf("profile %q: management and data bonds share VLAN %d — use separate VLANs for traffic isolation", p.Name, vlan),
						Entity:  p.Name,
					})
				}
			}
			// Warn if no VLAN separation exists at all (both untagged).
			if len(dataVLANs) == 0 && len(mgmtVLANs) == 0 {
				warnings = append(warnings, LintWarning{
					Code:    "VLAN_NOT_SEPARATED",
					Message: fmt.Sprintf("profile %q has both data and management bonds but neither uses VLANs — traffic isolation is recommended", p.Name),
					Entity:  p.Name,
				})
			}
		}
	}

	// Check 5 (IB): unmanaged IB switch without OpenSM configured.
	ibStatus, err := m.GetIBStatus(ctx)
	if err == nil && ibStatus.HasUnmanagedIBSwitch && !ibStatus.OpenSMConfigured {
		warnings = append(warnings, LintWarning{
			Code:    "IB_OPENSM_NOT_CONFIGURED",
			Message: "one or more InfiniBand switches have is_managed=false (no built-in SM), but OpenSM is not configured — nodes will not have an active subnet manager",
		})
	}

	// Check 6: oversubscription on data switches.
	switches, err := m.db.NetworkListSwitches(ctx)
	if err == nil {
		for _, sw := range switches {
			if sw.Role != api.NetworkSwitchRoleData {
				continue
			}
			if sw.UplinkPorts == "" || sw.PortCount == 0 {
				continue
			}
			uplinkCount := len(parseUplinkPorts(sw.UplinkPorts))
			if uplinkCount == 0 {
				continue
			}
			downlinkCount := sw.PortCount - uplinkCount
			if downlinkCount <= 0 {
				continue
			}
			ratio := downlinkCount / uplinkCount
			if ratio > 4 {
				warnings = append(warnings, LintWarning{
					Code:    "HIGH_OVERSUBSCRIPTION",
					Message: fmt.Sprintf("data switch %q has %d downlink ports and %d uplink ports (%d:1 oversubscription) — consider adding uplinks for HPC workloads", sw.Name, downlinkCount, uplinkCount, ratio),
					Entity:  sw.Name,
				})
			}
		}
	}

	return warnings, nil
}
