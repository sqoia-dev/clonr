package network

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/sqoia-dev/clonr/pkg/api"
)

// CablingEntry is one row in the cabling plan — one NIC on one node, assigned
// to a specific switch and port.
type CablingEntry struct {
	NodeID      string `json:"node_id"`
	NodeName    string `json:"node_name"`
	NodeMAC     string `json:"node_mac"`
	SwitchID    string `json:"switch_id"`
	SwitchName  string `json:"switch_name"`
	PortNumber  int    `json:"port_number"`
	CableType   string `json:"cable_type"`   // "cat6" for ethernet, "dac" for data, "qsfp" for IB
	NetworkRole string `json:"network_role"` // "management", "data", "infiniband"
}

// CablingPlan holds the full cabling plan for the cluster.
type CablingPlan struct {
	Entries       []CablingEntry `json:"entries"`
	TotalNodes    int            `json:"total_nodes"`
	TotalPorts    int            `json:"total_ports"`
	UnassignedNodes []string     `json:"unassigned_nodes,omitempty"` // node names with no port available
	Warnings      []string       `json:"warnings,omitempty"`
}

// parseUplinkPorts parses a comma-separated list of port numbers into a set.
func parseUplinkPorts(s string) map[int]bool {
	out := make(map[int]bool)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if n, err := strconv.Atoi(part); err == nil {
			out[n] = true
		}
	}
	return out
}

// buildAvailablePorts returns the sorted list of port numbers on a switch
// that are not designated as uplinks.
func buildAvailablePorts(sw api.NetworkSwitch) []int {
	uplinks := parseUplinkPorts(sw.UplinkPorts)
	portCount := sw.PortCount
	if portCount == 0 {
		portCount = 48
	}
	var ports []int
	for i := 1; i <= portCount; i++ {
		if !uplinks[i] {
			ports = append(ports, i)
		}
	}
	return ports
}

// cableTypeForRole returns a reasonable cable type label for a network role.
func cableTypeForRole(role api.NetworkSwitchRole) string {
	switch role {
	case api.NetworkSwitchRoleData:
		return "dac" // direct-attach copper typical for HPC data
	case api.NetworkSwitchRoleInfiniBand:
		return "qsfp"
	default:
		return "cat6"
	}
}

// GenerateCablingPlan builds a structured cabling plan by assigning available
// switch ports sequentially to nodes, grouped by network role.
//
// Assignment order:
//  1. Management switches → each node's primary management NIC
//  2. Data switches → each node's data NIC(s)
//  3. InfiniBand switches → each node's IB HCA port
//
// Only "confirmed" switches participate in cabling assignment.
func (m *Manager) GenerateCablingPlan(ctx context.Context) (*CablingPlan, error) {
	switches, err := m.db.NetworkListSwitches(ctx)
	if err != nil {
		return nil, fmt.Errorf("network: cabling plan: list switches: %w", err)
	}

	nodes, err := m.db.ListNodeConfigs(ctx, "") // empty string = all nodes, no image filter
	if err != nil {
		return nil, fmt.Errorf("network: cabling plan: list nodes: %w", err)
	}

	plan := &CablingPlan{}

	if len(nodes) == 0 {
		plan.Warnings = append(plan.Warnings, "no nodes registered")
		return plan, nil
	}

	// Separate confirmed switches by role.
	var mgmtSwitches, dataSwitches, ibSwitches []api.NetworkSwitch
	for _, sw := range switches {
		if sw.Status == "discovered" {
			continue // skip unconfirmed
		}
		switch sw.Role {
		case api.NetworkSwitchRoleManagement:
			mgmtSwitches = append(mgmtSwitches, sw)
		case api.NetworkSwitchRoleData:
			dataSwitches = append(dataSwitches, sw)
		case api.NetworkSwitchRoleInfiniBand:
			ibSwitches = append(ibSwitches, sw)
		}
	}

	if len(mgmtSwitches) == 0 {
		plan.Warnings = append(plan.Warnings, "no confirmed management switches — management port assignment skipped")
	}

	// Sort nodes deterministically by hostname.
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Hostname < nodes[j].Hostname
	})

	plan.TotalNodes = len(nodes)

	// portAssigner wraps sequential port allocation for a set of switches.
	type portAssigner struct {
		switches []api.NetworkSwitch
		swIdx    int
		ports    []int
		portIdx  int
	}

	newAssigner := func(sws []api.NetworkSwitch) *portAssigner {
		pa := &portAssigner{switches: sws}
		if len(sws) > 0 {
			pa.ports = buildAvailablePorts(sws[0])
		}
		return pa
	}

	next := func(pa *portAssigner) (api.NetworkSwitch, int, bool) {
		for {
			if pa.swIdx >= len(pa.switches) {
				return api.NetworkSwitch{}, 0, false
			}
			if pa.portIdx < len(pa.ports) {
				sw := pa.switches[pa.swIdx]
				port := pa.ports[pa.portIdx]
				pa.portIdx++
				return sw, port, true
			}
			// Current switch exhausted — advance to next.
			pa.swIdx++
			pa.portIdx = 0
			if pa.swIdx < len(pa.switches) {
				pa.ports = buildAvailablePorts(pa.switches[pa.swIdx])
			}
		}
	}

	mgmtPA := newAssigner(mgmtSwitches)
	dataPA := newAssigner(dataSwitches)
	ibPA := newAssigner(ibSwitches)

	unassigned := map[string]bool{}

	for _, node := range nodes {
		hostname := node.Hostname
		if hostname == "" {
			hostname = node.ID
		}
		mac := node.PrimaryMAC

		// Management port.
		if sw, port, ok := next(mgmtPA); ok {
			plan.Entries = append(plan.Entries, CablingEntry{
				NodeID:      node.ID,
				NodeName:    hostname,
				NodeMAC:     mac,
				SwitchID:    sw.ID,
				SwitchName:  sw.Name,
				PortNumber:  port,
				CableType:   cableTypeForRole(api.NetworkSwitchRoleManagement),
				NetworkRole: "management",
			})
			plan.TotalPorts++
		} else if len(mgmtSwitches) > 0 {
			unassigned[hostname] = true
		}

		// Data port — only assign if data switches exist.
		if len(dataSwitches) > 0 {
			if sw, port, ok := next(dataPA); ok {
				plan.Entries = append(plan.Entries, CablingEntry{
					NodeID:      node.ID,
					NodeName:    hostname,
					NodeMAC:     mac,
					SwitchID:    sw.ID,
					SwitchName:  sw.Name,
					PortNumber:  port,
					CableType:   cableTypeForRole(api.NetworkSwitchRoleData),
					NetworkRole: "data",
				})
				plan.TotalPorts++
			} else {
				unassigned[hostname] = true
			}
		}

		// IB port — only assign if IB switches exist.
		if len(ibSwitches) > 0 {
			if sw, port, ok := next(ibPA); ok {
				plan.Entries = append(plan.Entries, CablingEntry{
					NodeID:      node.ID,
					NodeName:    hostname,
					NodeMAC:     mac,
					SwitchID:    sw.ID,
					SwitchName:  sw.Name,
					PortNumber:  port,
					CableType:   cableTypeForRole(api.NetworkSwitchRoleInfiniBand),
					NetworkRole: "infiniband",
				})
				plan.TotalPorts++
			} else {
				unassigned[hostname] = true
			}
		}
	}

	for name := range unassigned {
		plan.UnassignedNodes = append(plan.UnassignedNodes, name)
	}
	sort.Strings(plan.UnassignedNodes)
	if len(plan.UnassignedNodes) > 0 {
		plan.Warnings = append(plan.Warnings,
			fmt.Sprintf("%d node(s) could not be fully assigned — insufficient switch port capacity", len(plan.UnassignedNodes)))
	}

	return plan, nil
}
