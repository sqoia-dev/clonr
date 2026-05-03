package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// newDHCPHandler returns a DHCPHandler wired to the given DB.
func newDHCPHandler(d *db.DB) *DHCPHandler {
	return &DHCPHandler{DB: d}
}

// insertDHCPTestNode creates a node with optional interface (IP) and tags.
// ip may be empty to simulate a node with no DHCP allocation.
func insertDHCPTestNode(t *testing.T, d *db.DB, id, mac, hostname, ip string, tags []string) api.NodeConfig {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	ifaces := []api.InterfaceConfig{}
	if ip != "" {
		ifaces = []api.InterfaceConfig{{MACAddress: mac, IPAddress: ip}}
	}
	cfg := api.NodeConfig{
		ID:         id,
		Hostname:   hostname,
		PrimaryMAC: mac,
		Tags:       tags,
		Interfaces: ifaces,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(context.Background(), cfg); err != nil {
		t.Fatalf("insertDHCPTestNode CreateNodeConfig %s: %v", id, err)
	}
	return cfg
}

// dhcpGetRequest fires GET /api/v1/dhcp/leases with optional query params.
func dhcpGetRequest(t *testing.T, h *DHCPHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/v1/dhcp/leases"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	h.ListLeases(w, req)
	return w
}

// TestDHCPLeases_HappyPath verifies the handler returns leases sorted by IP
// and that the response shape matches DHCPLeasesResponse.
func TestDHCPLeases_HappyPath(t *testing.T) {
	d := openTestDB(t)
	h := newDHCPHandler(d)

	// Insert three nodes with IPs out of order; expect sorted output.
	insertDHCPTestNode(t, d, "n1", "aa:bb:cc:00:00:03", "node-c", "10.99.0.103/24", []string{"compute"})
	insertDHCPTestNode(t, d, "n2", "aa:bb:cc:00:00:01", "node-a", "10.99.0.101/24", []string{"controller"})
	insertDHCPTestNode(t, d, "n3", "aa:bb:cc:00:00:02", "node-b", "10.99.0.102/24", []string{"compute"})

	w := dhcpGetRequest(t, h, "")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp api.DHCPLeasesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Count != 3 {
		t.Errorf("count = %d; want 3", resp.Count)
	}
	if len(resp.Leases) != 3 {
		t.Fatalf("leases len = %d; want 3", len(resp.Leases))
	}

	// Verify sorted by IP ascending.
	wantIPs := []string{"10.99.0.101", "10.99.0.102", "10.99.0.103"}
	for i, want := range wantIPs {
		if resp.Leases[i].IP != want {
			t.Errorf("lease[%d].IP = %q; want %q", i, resp.Leases[i].IP, want)
		}
	}

	// Verify the /24 CIDR suffix was stripped.
	for _, l := range resp.Leases {
		if len(l.IP) > 0 && l.IP[len(l.IP)-3:] == "/24" {
			t.Errorf("lease %s IP still has CIDR suffix: %q", l.Hostname, l.IP)
		}
	}

	// Verify role field.
	if resp.Leases[0].Role != "controller" {
		t.Errorf("lease[0].Role = %q; want %q", resp.Leases[0].Role, "controller")
	}
}

// TestDHCPLeases_Empty verifies that an empty node table returns count=0 and
// an empty (non-null) array rather than null.
func TestDHCPLeases_Empty(t *testing.T) {
	d := openTestDB(t)
	h := newDHCPHandler(d)

	w := dhcpGetRequest(t, h, "")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp api.DHCPLeasesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Count != 0 {
		t.Errorf("count = %d; want 0", resp.Count)
	}
	if resp.Leases == nil {
		t.Error("leases should be non-nil empty slice, not null")
	}
	if len(resp.Leases) != 0 {
		t.Errorf("leases len = %d; want 0", len(resp.Leases))
	}
}

// TestDHCPLeases_RoleFilter verifies that ?role= filters the result.
func TestDHCPLeases_RoleFilter(t *testing.T) {
	d := openTestDB(t)
	h := newDHCPHandler(d)

	insertDHCPTestNode(t, d, "n1", "aa:bb:cc:00:01:01", "ctrl", "10.99.0.101/24", []string{"controller"})
	insertDHCPTestNode(t, d, "n2", "aa:bb:cc:00:01:02", "comp1", "10.99.0.102/24", []string{"compute"})
	insertDHCPTestNode(t, d, "n3", "aa:bb:cc:00:01:03", "comp2", "10.99.0.103/24", []string{"compute"})

	w := dhcpGetRequest(t, h, "role=compute")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp api.DHCPLeasesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Count != 2 {
		t.Errorf("count = %d; want 2 (only compute nodes)", resp.Count)
	}
	for _, l := range resp.Leases {
		if l.Role != "compute" {
			t.Errorf("expected only compute nodes; got role %q for %s", l.Role, l.Hostname)
		}
	}
}

// TestDHCPLeases_NoIPNodesIncluded verifies that nodes without any IP still
// appear in the list (operator needs to see them to debug "did node get an IP?").
func TestDHCPLeases_NoIPNodesIncluded(t *testing.T) {
	d := openTestDB(t)
	h := newDHCPHandler(d)

	insertDHCPTestNode(t, d, "n1", "aa:bb:cc:00:02:01", "with-ip", "10.99.0.5/24", []string{})
	insertDHCPTestNode(t, d, "n2", "aa:bb:cc:00:02:02", "no-ip", "", []string{})

	w := dhcpGetRequest(t, h, "")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp api.DHCPLeasesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Both nodes must appear.
	if resp.Count != 2 {
		t.Errorf("count = %d; want 2 (no-IP node must still appear)", resp.Count)
	}

	// The node with an IP should sort before the one without.
	if resp.Leases[0].Hostname != "with-ip" {
		t.Errorf("expected with-ip first, got %q", resp.Leases[0].Hostname)
	}
	if resp.Leases[1].IP != "" {
		t.Errorf("no-ip node has unexpected IP %q", resp.Leases[1].IP)
	}
}
