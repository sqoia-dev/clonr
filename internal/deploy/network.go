package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sqoia-dev/clonr/pkg/api"
)

// injectNetworkConfig writes NetworkManager keyfiles for bond interfaces, bond
// slaves, VLAN sub-interfaces, and IPoIB into the deployed rootfs at mountRoot.
// When cfg.OpenSMConf is non-empty, it also writes /etc/opensm/opensm.conf and
// enables opensm.service via chroot systemctl.
//
// All file writes are direct (no chroot subprocess) — NM keyfiles are plain text
// and need no special tooling to create. The systemctl enable call is the only
// chroot command used here.
//
// The function is idempotent: os.WriteFile truncates and replaces existing files.
// Re-running finalize on the same rootfs overwrites with the current DB config.
func injectNetworkConfig(ctx context.Context, mountRoot string, cfg *api.NetworkNodeConfig) error {
	log := logger()

	nmDir := filepath.Join(mountRoot, "etc", "NetworkManager", "system-connections")
	if err := os.MkdirAll(nmDir, 0o700); err != nil {
		return fmt.Errorf("network inject: mkdir NM connections: %w", err)
	}

	// ── Bond interfaces ───────────────────────────────────────────────────────
	for _, bond := range cfg.Bonds {
		if err := writeBondMasterKeyfile(nmDir, bond); err != nil {
			return fmt.Errorf("network inject: bond master %s: %w", bond.BondName, err)
		}
		log.Info().Str("bond", bond.BondName).Msg("finalize: wrote bond master NM keyfile")

		for _, member := range bond.Members {
			if err := writeBondSlaveKeyfile(nmDir, bond.BondName, member); err != nil {
				memberID := member.MatchName
				if memberID == "" {
					memberID = member.MatchMAC
				}
				return fmt.Errorf("network inject: bond slave %s/%s: %w", bond.BondName, memberID, err)
			}
		}
		log.Info().Str("bond", bond.BondName).Int("members", len(bond.Members)).
			Msg("finalize: wrote bond slave NM keyfiles")

		if bond.VLANID > 0 {
			if err := writeVLANKeyfile(nmDir, bond); err != nil {
				return fmt.Errorf("network inject: vlan %s.%d: %w", bond.BondName, bond.VLANID, err)
			}
			log.Info().Str("bond", bond.BondName).Int("vlan", bond.VLANID).
				Msg("finalize: wrote VLAN NM keyfile")
		}
	}

	// ── IPoIB interface ───────────────────────────────────────────────────────
	if cfg.IB != nil {
		if err := writeIPoIBProfileFromIBProfile(nmDir, cfg.IB); err != nil {
			return fmt.Errorf("network inject: IPoIB: %w", err)
		}
		log.Info().Str("mode", cfg.IB.IPoIBMode).Msg("finalize: wrote IPoIB NM keyfile")
	}

	// ── OpenSM config ─────────────────────────────────────────────────────────
	if cfg.OpenSMConf != "" {
		osmDir := filepath.Join(mountRoot, "etc", "opensm")
		if err := os.MkdirAll(osmDir, 0o755); err != nil {
			return fmt.Errorf("network inject: mkdir /etc/opensm: %w", err)
		}

		osmConf := filepath.Join(osmDir, "opensm.conf")
		if err := os.WriteFile(osmConf, []byte(cfg.OpenSMConf), 0o644); err != nil {
			return fmt.Errorf("network inject: write opensm.conf: %w", err)
		}
		log.Info().Msg("finalize: wrote /etc/opensm/opensm.conf")

		// Enable opensm.service in the chroot. Non-fatal: opensm may not be
		// installed if the image was not built with the head-node role.
		cmd := exec.CommandContext(ctx, "chroot", mountRoot, "systemctl", "enable", "opensm")
		if err := runAndLog(ctx, "systemctl-enable-opensm", cmd); err != nil {
			log.Warn().Err(err).Msg("finalize: systemctl enable opensm failed (non-fatal — opensm may not be installed in image)")
		} else {
			log.Info().Msg("finalize: opensm.service enabled in chroot")
		}
	}

	return nil
}

// writeBondMasterKeyfile writes the NM keyfile for the bond master connection.
// File: <nmDir>/<bondName>.nmconnection, mode 0600.
func writeBondMasterKeyfile(nmDir string, bond api.BondConfig) error {
	bondName := bond.BondName
	if !ifaceNameRe.MatchString(bondName) {
		return fmt.Errorf("bond name %q contains invalid characters", bondName)
	}

	var sb strings.Builder

	sb.WriteString("[connection]\n")
	fmt.Fprintf(&sb, "id=%s\n", bondName)
	sb.WriteString("type=bond\n")
	fmt.Fprintf(&sb, "interface-name=%s\n", bondName)
	sb.WriteString("autoconnect=yes\n")
	sb.WriteString("\n")

	sb.WriteString("[bond]\n")
	if bond.Mode != "" {
		fmt.Fprintf(&sb, "mode=%s\n", bond.Mode)
	}
	// LACP-specific options only apply to 802.3ad mode.
	if bond.Mode == "802.3ad" {
		if bond.LACPRate != "" {
			fmt.Fprintf(&sb, "lacp-rate=%s\n", bond.LACPRate)
		}
		if bond.XmitHashPolicy != "" {
			fmt.Fprintf(&sb, "xmit-hash-policy=%s\n", bond.XmitHashPolicy)
		}
	}
	if bond.MTU > 0 {
		fmt.Fprintf(&sb, "mtu=%d\n", bond.MTU)
	}
	sb.WriteString("\n")

	sb.WriteString("[ipv4]\n")
	sb.WriteString(nmIPv4Method(bond.IPMethod))
	sb.WriteString("\n")

	sb.WriteString("[ipv6]\n")
	sb.WriteString("method=ignore\n")

	filename := filepath.Join(nmDir, bondName+".nmconnection")
	return os.WriteFile(filename, []byte(sb.String()), 0o600)
}

// writeBondSlaveKeyfile writes the NM keyfile for a NIC enslaved to a bond.
// File: <nmDir>/<bondName>-slave-<name>.nmconnection, mode 0600.
func writeBondSlaveKeyfile(nmDir, bondName string, member api.BondMember) error {
	// Derive a stable filename from the member's identifying field.
	// Prefer match_name for the filename; fall back to a MAC-derived label.
	memberLabel := member.MatchName
	if memberLabel == "" {
		// Sanitize MAC address for use as a filename: replace colons with hyphens.
		memberLabel = strings.ReplaceAll(member.MatchMAC, ":", "-")
	}
	if memberLabel == "" {
		memberLabel = member.ID
	}

	// Validate the computed label before using it in a filename.
	if !ifaceNameRe.MatchString(memberLabel) {
		return fmt.Errorf("bond slave label %q contains invalid characters for member %s", memberLabel, member.ID)
	}

	connID := fmt.Sprintf("%s-slave-%s", bondName, memberLabel)

	var sb strings.Builder

	sb.WriteString("[connection]\n")
	fmt.Fprintf(&sb, "id=%s\n", connID)
	sb.WriteString("type=ethernet\n")
	if member.MatchName != "" {
		fmt.Fprintf(&sb, "interface-name=%s\n", member.MatchName)
	}
	fmt.Fprintf(&sb, "master=%s\n", bondName)
	sb.WriteString("slave-type=bond\n")
	sb.WriteString("autoconnect=yes\n")
	sb.WriteString("\n")

	sb.WriteString("[ethernet]\n")
	// When matching by MAC, write the mac-address stanza so NM matches the
	// physical NIC by hardware address regardless of kernel interface naming.
	if member.MatchMAC != "" {
		fmt.Fprintf(&sb, "mac-address=%s\n", member.MatchMAC)
	}
	sb.WriteString("\n")

	filename := filepath.Join(nmDir, connID+".nmconnection")
	return os.WriteFile(filename, []byte(sb.String()), 0o600)
}

// writeVLANKeyfile writes the NM keyfile for a VLAN sub-interface on top of a bond.
// File: <nmDir>/<bondName>.<vlanID>.nmconnection, mode 0600.
func writeVLANKeyfile(nmDir string, bond api.BondConfig) error {
	vlanIfaceName := fmt.Sprintf("%s.%d", bond.BondName, bond.VLANID)
	if !ifaceNameRe.MatchString(vlanIfaceName) {
		return fmt.Errorf("vlan interface name %q contains invalid characters", vlanIfaceName)
	}

	var sb strings.Builder

	sb.WriteString("[connection]\n")
	fmt.Fprintf(&sb, "id=%s\n", vlanIfaceName)
	sb.WriteString("type=vlan\n")
	fmt.Fprintf(&sb, "interface-name=%s\n", vlanIfaceName)
	sb.WriteString("autoconnect=yes\n")
	sb.WriteString("\n")

	sb.WriteString("[vlan]\n")
	fmt.Fprintf(&sb, "id=%d\n", bond.VLANID)
	fmt.Fprintf(&sb, "parent=%s\n", bond.BondName)
	sb.WriteString("\n")

	sb.WriteString("[ipv4]\n")
	sb.WriteString(nmIPv4Method(bond.IPMethod))
	sb.WriteString("\n")

	sb.WriteString("[ipv6]\n")
	sb.WriteString("method=ignore\n")

	filename := filepath.Join(nmDir, vlanIfaceName+".nmconnection")
	return os.WriteFile(filename, []byte(sb.String()), 0o600)
}

// writeIPoIBProfileFromIBProfile writes the NM keyfile for an IPoIB interface
// derived from a NetworkProfile's IBProfile. The interface is always named "ib0"
// (the first IB device); multi-device setups are handled via separate profiles
// in a future iteration.
func writeIPoIBProfileFromIBProfile(nmDir string, ib *api.IBProfile) error {
	ifaceName := "ib0"

	mtu := ib.IPoIBMTU
	if mtu == 0 {
		if strings.EqualFold(ib.IPoIBMode, "connected") {
			mtu = 65520
		} else {
			mtu = 2044
		}
	}

	mode := strings.ToLower(ib.IPoIBMode)
	if mode == "" {
		mode = "datagram"
	}

	var sb strings.Builder

	sb.WriteString("[connection]\n")
	fmt.Fprintf(&sb, "id=%s\n", ifaceName)
	sb.WriteString("type=infiniband\n")
	fmt.Fprintf(&sb, "interface-name=%s\n", ifaceName)
	sb.WriteString("autoconnect=yes\n")
	sb.WriteString("\n")

	sb.WriteString("[infiniband]\n")
	fmt.Fprintf(&sb, "transport-mode=%s\n", mode)
	fmt.Fprintf(&sb, "mtu=%d\n", mtu)
	sb.WriteString("\n")

	// Map ip_method to NM's [ipv4] method string.
	sb.WriteString("[ipv4]\n")
	sb.WriteString(nmIPv4Method(ib.IPMethod))
	sb.WriteString("\n")

	sb.WriteString("[ipv6]\n")
	sb.WriteString("method=ignore\n")

	filename := filepath.Join(nmDir, ifaceName+".nmconnection")
	return os.WriteFile(filename, []byte(sb.String()), 0o600)
}

// nmIPv4Method returns the NM [ipv4] method= line for the given ip_method string.
// "dhcp" → "method=auto", "static" → "method=manual", anything else → "method=disabled".
func nmIPv4Method(ipMethod string) string {
	switch strings.ToLower(ipMethod) {
	case "dhcp":
		return "method=auto\n"
	case "static":
		// In v1, static address assignment comes from NodeConfig.Interfaces.
		// The bond profile controls structure; per-node addresses are not yet
		// embedded in the bond keyfile. Use method=manual as the declaration
		// intent; the operator must add address1= lines manually or via reimage
		// once per-node static addressing is wired into the deploy pipeline.
		return "method=manual\n"
	default:
		return "method=disabled\n"
	}
}
