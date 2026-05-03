package handlers

import (
	"net/http"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// DHCPHandler handles /api/v1/dhcp/leases — read-only view of DHCP allocations
// derived from the node_configs table. No dnsmasq lease files are read.
type DHCPHandler struct {
	DB *db.DB
}

// ListLeases handles GET /api/v1/dhcp/leases.
// Returns all nodes that have a known MAC address, sorted by IP ascending.
// An empty node list returns {"leases":[],"count":0} (never null).
//
// Optional query param ?role= filters by the node's first tag (HPC role).
// The IP field carries only the plain dotted-decimal address — any /prefix
// notation stored in the interface config is stripped.
func (h *DHCPHandler) ListLeases(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.DB.ListNodeConfigs(r.Context(), "")
	if err != nil {
		log.Error().Err(err).Msg("dhcp leases: list node configs")
		writeError(w, err)
		return
	}

	// Optional role filter: match against the node's first tag (the HPC role
	// label stored there, e.g. "compute", "login", "controller").
	roleFilter := strings.TrimSpace(r.URL.Query().Get("role"))

	leases := make([]api.DHCPLease, 0, len(nodes))
	for _, n := range nodes {
		// Only surface nodes that have a primary MAC. Unconfigured stubs without
		// a MAC are not relevant to DHCP allocation tracking.
		if n.PrimaryMAC == "" {
			continue
		}

		// Resolve the primary IP: use the first interface whose IP is non-empty.
		// Interfaces[0] is always the primary NIC (the one matching PrimaryMAC).
		ip := ""
		for _, iface := range n.Interfaces {
			if iface.IPAddress != "" {
				ip = extractIP(iface.IPAddress) // strip /prefix if present
				break
			}
		}

		// Resolve role from Tags (first tag is used as the HPC role label).
		role := ""
		if len(n.Tags) > 0 {
			role = n.Tags[0]
		}

		// Apply role filter when requested.
		if roleFilter != "" && role != roleFilter {
			continue
		}

		state := string(n.State())

		leases = append(leases, api.DHCPLease{
			NodeID:      n.ID,
			Hostname:    n.Hostname,
			MAC:         n.PrimaryMAC,
			IP:          ip,
			Role:        role,
			DeployState: state,
			LastSeenAt:  n.LastSeenAt,
			FirstSeenAt: n.CreatedAt,
		})
	}

	// Sort by IP ascending so operators can scan the management network layout
	// in subnet order. Nodes with no IP sort to the end.
	sort.SliceStable(leases, func(i, j int) bool {
		a, b := leases[i].IP, leases[j].IP
		if a == "" && b == "" {
			return leases[i].Hostname < leases[j].Hostname
		}
		if a == "" {
			return false // no-IP nodes go to the end
		}
		if b == "" {
			return true
		}
		return compareIPs(a, b) < 0
	})

	writeJSON(w, http.StatusOK, api.DHCPLeasesResponse{
		Leases: leases,
		Count:  len(leases),
	})
}

// compareIPs compares two dotted-decimal IP strings lexicographically by octet
// so that "10.99.0.2" < "10.99.0.10" (numeric comparison per octet).
// Returns negative if a < b, zero if equal, positive if a > b.
// Falls back to plain string comparison on any parse error.
func compareIPs(a, b string) int {
	ao := splitOctets(a)
	bo := splitOctets(b)
	for i := 0; i < 4 && i < len(ao) && i < len(bo); i++ {
		if ao[i] != bo[i] {
			return ao[i] - bo[i]
		}
	}
	return strings.Compare(a, b)
}

// splitOctets splits a dotted-decimal IP into four integer octets.
// Returns a four-element slice even on partial parse; unparseable octets default to 0.
func splitOctets(ip string) [4]int {
	var out [4]int
	parts := strings.SplitN(ip, ".", 4)
	for i, p := range parts {
		if i >= 4 {
			break
		}
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out
}
