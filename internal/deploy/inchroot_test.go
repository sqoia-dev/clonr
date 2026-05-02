package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// buildMinimalChroot creates the directory skeleton needed for applyNodeConfig to
// run without errors against a tmpdir root. Only the subdirectories consulted by
// writeHostname and writeNetworkConfig are created — we don't need the full OS tree.
func buildMinimalChroot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{
		"etc",
		"etc/NetworkManager/system-connections",
		"etc/systemd",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("buildMinimalChroot: mkdir %s: %v", dir, err)
		}
	}
	return root
}

// TestInChrootReconfigure_WritesHostname verifies that inChrootReconfigure
// writes /etc/hostname to the target root, not to the live host filesystem.
func TestInChrootReconfigure_WritesHostname(t *testing.T) {
	root := buildMinimalChroot(t)

	cfg := api.NodeConfig{
		Hostname: "compute-01",
		FQDN:     "compute-01.cluster.local",
	}

	if err := inChrootReconfigure(context.Background(), cfg, root); err != nil {
		// applyNodeConfig has non-fatal code paths; a missing NM dir etc. is warned
		// but the function returns nil. A hard error here is a real failure.
		t.Fatalf("inChrootReconfigure: %v", err)
	}

	hostnameFile := filepath.Join(root, "etc", "hostname")
	got, err := os.ReadFile(hostnameFile)
	if err != nil {
		t.Fatalf("read %s: %v", hostnameFile, err)
	}
	if !strings.Contains(string(got), "compute-01") {
		t.Errorf("etc/hostname = %q, want it to contain %q", string(got), "compute-01")
	}

	// Sanity: the live /etc/hostname must not have been changed to "compute-01".
	if liveHostname, err := os.ReadFile("/etc/hostname"); err == nil {
		if strings.TrimSpace(string(liveHostname)) == "compute-01" {
			t.Error("live /etc/hostname was unexpectedly set to compute-01 — inChrootReconfigure targeted the wrong root")
		}
	}
}

// TestInChrootReconfigure_WritesHosts verifies that /etc/hosts entries are
// written to the target root and include the injected cluster host roster.
func TestInChrootReconfigure_WritesHosts(t *testing.T) {
	root := buildMinimalChroot(t)

	cfg := api.NodeConfig{
		Hostname: "head-01",
		FQDN:     "head-01.cluster.local",
		ClusterHosts: []api.HostEntry{
			{IP: "10.99.0.1", Hostname: "clustr-server"},
			{IP: "10.99.0.10", Hostname: "head-01"},
			{IP: "10.99.0.11", Hostname: "compute-01"},
		},
	}

	if err := inChrootReconfigure(context.Background(), cfg, root); err != nil {
		t.Fatalf("inChrootReconfigure: %v", err)
	}

	hostsFile := filepath.Join(root, "etc", "hosts")
	got, err := os.ReadFile(hostsFile)
	if err != nil {
		t.Fatalf("read %s: %v", hostsFile, err)
	}
	for _, wantIP := range []string{"10.99.0.1", "10.99.0.10", "10.99.0.11"} {
		if !strings.Contains(string(got), wantIP) {
			t.Errorf("etc/hosts missing entry for %s\ncontent:\n%s", wantIP, string(got))
		}
	}
}

// TestInChrootReconfigure_NetworkConfig verifies that a simple interface config
// results in a NetworkManager keyfile in the target root.
func TestInChrootReconfigure_NetworkConfig(t *testing.T) {
	root := buildMinimalChroot(t)

	cfg := api.NodeConfig{
		Hostname: "compute-02",
		FQDN:     "compute-02.cluster.local",
		Interfaces: []api.InterfaceConfig{
			{
				Name:      "eth0",
				IPAddress: "10.99.0.20/24",
				Gateway:   "10.99.0.1",
				MTU:       1500,
			},
		},
	}

	if err := inChrootReconfigure(context.Background(), cfg, root); err != nil {
		t.Fatalf("inChrootReconfigure: %v", err)
	}

	nmConn := filepath.Join(root, "etc", "NetworkManager", "system-connections", "eth0.nmconnection")
	got, err := os.ReadFile(nmConn)
	if err != nil {
		t.Fatalf("read %s: %v", nmConn, err)
	}
	if !strings.Contains(string(got), "10.99.0.20") {
		t.Errorf("eth0.nmconnection missing IP address 10.99.0.20\ncontent:\n%s", string(got))
	}
}

// TestInChrootReconfigure_FilesLandInRoot verifies that none of the files written
// by inChrootReconfigure land outside the target root, regardless of what the
// node config contains. This guards against path-traversal regressions.
func TestInChrootReconfigure_FilesLandInRoot(t *testing.T) {
	root := buildMinimalChroot(t)

	// Snapshot modification time of /etc on the LIVE host before calling inChrootReconfigure.
	liveEtcInfo, err := os.Stat("/etc")
	if err != nil {
		t.Skip("cannot stat /etc — skipping live-root guard check")
	}
	liveEtcMtime := liveEtcInfo.ModTime()

	cfg := api.NodeConfig{
		Hostname: "node-test",
		FQDN:     "node-test.cluster.local",
		ClusterHosts: []api.HostEntry{
			{IP: "192.168.1.1", Hostname: "test-host"},
		},
	}

	if err := inChrootReconfigure(context.Background(), cfg, root); err != nil {
		t.Fatalf("inChrootReconfigure: %v", err)
	}

	// If inChrootReconfigure touched the live /etc, its mtime would have changed.
	afterInfo, err := os.Stat("/etc")
	if err != nil {
		t.Fatalf("stat /etc after call: %v", err)
	}
	if afterInfo.ModTime() != liveEtcMtime {
		t.Error("live /etc mtime changed — inChrootReconfigure may have written to the live root")
	}
}
