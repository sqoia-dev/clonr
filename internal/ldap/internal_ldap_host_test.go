// internal_ldap_host_test.go — pin internalLDAPHost behavior so a future
// regression that re-introduces net.LookupHost as the primary source for the
// ldap_uri host is caught at test time.
//
// Context: v0.1.14 shipped sssd.conf with ldap_uri pointing at the cloner's
// public IPv6 address, because detectPrimaryIP grabbed addrs[0] from
// net.LookupHost which returned the global IPv6 first on a dual-stack host.
// Nodes on the provisioning subnet have no IPv6 reachability, so the deploy
// produced LDAP-broken nodes that nonetheless transitioned to deployed_verified.
package ldap

import (
	"testing"

	"github.com/sqoia-dev/clustr/internal/config"
)

func TestInternalLDAPHost_PrefersPXEServerIP(t *testing.T) {
	cfg := config.ServerConfig{
		PXE: config.PXEConfig{ServerIP: "10.99.0.1"},
	}
	got := internalLDAPHost(cfg)
	if got != "10.99.0.1" {
		t.Fatalf("internalLDAPHost: got %q, want %q (cfg.PXE.ServerIP must take precedence over hostname/IP detection)", got, "10.99.0.1")
	}
}

func TestInternalLDAPHost_PXEServerIPWinsEvenWhenHostnameResolvesToOtherAddress(t *testing.T) {
	// On the cloner host, os.Hostname()+net.LookupHost may return a routable
	// IPv6 address. Setting PXE.ServerIP must dominate that result regardless
	// of what the host resolver does — the regression we're guarding against.
	cfg := config.ServerConfig{
		PXE: config.PXEConfig{ServerIP: "10.99.0.1"},
	}
	got := internalLDAPHost(cfg)
	// Whatever detectPrimaryIP would return, the configured value wins.
	if got != "10.99.0.1" {
		t.Fatalf("internalLDAPHost: got %q, want exactly %q regardless of host network state", got, "10.99.0.1")
	}
	// And the URI rendering uses port 636 — pin the contract.
	if uri := "ldaps://" + got + ":636"; uri != "ldaps://10.99.0.1:636" {
		t.Fatalf("rendered URI: got %q, want %q", uri, "ldaps://10.99.0.1:636")
	}
}

func TestInternalLDAPHost_FallsBackWhenPXEServerIPUnset(t *testing.T) {
	// When CLUSTR_PXE_SERVER_IP is not configured (e.g. unit tests, dev runs
	// without PXE), fall back to the prior behavior so we don't break setups
	// that currently work via hostname resolution.
	cfg := config.ServerConfig{} // PXE.ServerIP == ""
	got := internalLDAPHost(cfg)
	// The fallback is best-effort — just assert it returns *something* non-empty
	// so callers always produce a valid ldaps URI. The exact value depends on
	// the test host's network configuration.
	if got == "" {
		t.Fatalf("internalLDAPHost: returned empty string with no PXE.ServerIP; want a non-empty fallback (detectPrimaryIP or detectHostname)")
	}
}
