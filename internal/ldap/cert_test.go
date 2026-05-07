// cert_test.go — pin generateServerCert SAN coverage.
//
// Context (P1 from PR #2 review): NodeConfig.ldap_uri uses internalLDAPHost,
// which honors CLUSTR_PXE_SERVER_IP. If the cert SAN list omits that IP, nodes
// using ldap_tls_reqcert=demand reject the server cert because the dialed
// address isn't covered. The fix threads internalLDAPHost(cfg) into the SAN
// builder via the extraIPs argument; this test pins that contract so a future
// regression that re-narrows the SAN list back to detectPrimaryIP only is
// caught at test time.
package ldap

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// parseCertOrFail parses a PEM-encoded leaf certificate from b. Test helper.
func parseCertOrFail(t *testing.T, b []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(b)
	if block == nil {
		t.Fatalf("pem.Decode: no block found")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate: %v", err)
	}
	return c
}

func TestGenerateServerCert_IncludesPrimaryIPInSAN(t *testing.T) {
	_, caKey, caCert, err := generateCA("test CA")
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}
	bundle, err := generateServerCert("clustr-server", "192.168.1.151", nil, caKey, caCert)
	if err != nil {
		t.Fatalf("generateServerCert: %v", err)
	}
	cert := parseCertOrFail(t, bundle.CertPEM)

	wantIPs := []string{"127.0.0.1", "::1", "192.168.1.151"}
	for _, w := range wantIPs {
		if !sanContainsIP(cert, w) {
			t.Errorf("SAN missing IP %s; got %v", w, cert.IPAddresses)
		}
	}
}

// TestGenerateServerCert_IncludesExtraIPsInSAN is the primary regression test
// for the PR #2 fix: when detectPrimaryIP returns a different address from
// internalLDAPHost (the dual-stack-with-PXE-IP case), the PXE IP MUST appear
// in the SAN list so nodes connecting with ldap_tls_reqcert=demand pass cert
// validation.
func TestGenerateServerCert_IncludesExtraIPsInSAN(t *testing.T) {
	_, caKey, caCert, err := generateCA("test CA")
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}
	// Simulate: detectPrimaryIP() returned a public IPv6 (the v0.1.14 bug
	// scenario), but CLUSTR_PXE_SERVER_IP is the internal IPv4 the nodes
	// actually dial. Both must appear in the SAN list.
	primaryIP := "2001:db8::1"
	extraIPs := []string{"10.99.0.1"}

	bundle, err := generateServerCert("clustr-server", primaryIP, extraIPs, caKey, caCert)
	if err != nil {
		t.Fatalf("generateServerCert: %v", err)
	}
	cert := parseCertOrFail(t, bundle.CertPEM)

	if !sanContainsIP(cert, "10.99.0.1") {
		t.Fatalf("SAN missing CLUSTR_PXE_SERVER_IP 10.99.0.1; got %v — nodes will reject ldaps cert with ldap_tls_reqcert=demand", cert.IPAddresses)
	}
	if !sanContainsIP(cert, "2001:db8::1") {
		t.Errorf("SAN missing primary IP 2001:db8::1 (back-compat); got %v", cert.IPAddresses)
	}
	if !sanContainsIP(cert, "127.0.0.1") {
		t.Errorf("SAN missing loopback 127.0.0.1; got %v", cert.IPAddresses)
	}
}

// TestGenerateServerCert_DedupsExtraIPs guards against the case where
// internalLDAPHost(cfg) and detectPrimaryIP() return the same address (e.g.
// PXE.ServerIP unset, single-stack host) — the SAN list must not contain
// duplicate entries.
func TestGenerateServerCert_DedupsExtraIPs(t *testing.T) {
	_, caKey, caCert, err := generateCA("test CA")
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}
	bundle, err := generateServerCert("clustr-server", "10.99.0.1", []string{"10.99.0.1", "127.0.0.1"}, caKey, caCert)
	if err != nil {
		t.Fatalf("generateServerCert: %v", err)
	}
	cert := parseCertOrFail(t, bundle.CertPEM)

	count := 0
	for _, ip := range cert.IPAddresses {
		if ip.String() == "10.99.0.1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("10.99.0.1 appears %d times in SAN; want exactly 1; got %v", count, cert.IPAddresses)
	}
}

// TestGenerateServerCert_DropsNonIPExtras confirms that unparseable extraIPs
// entries (e.g. a hostname returned from internalLDAPHost when no IP is
// available) are silently dropped rather than mis-stored as IP literals.
// IPAddresses must contain only valid IPs.
func TestGenerateServerCert_DropsNonIPExtras(t *testing.T) {
	_, caKey, caCert, err := generateCA("test CA")
	if err != nil {
		t.Fatalf("generateCA: %v", err)
	}
	bundle, err := generateServerCert("clustr-server", "10.99.0.1", []string{"clustr-server", ""}, caKey, caCert)
	if err != nil {
		t.Fatalf("generateServerCert: %v", err)
	}
	cert := parseCertOrFail(t, bundle.CertPEM)
	for _, ip := range cert.IPAddresses {
		if ip == nil {
			t.Errorf("found nil IP in SAN list")
		}
	}
}

func sanContainsIP(cert *x509.Certificate, want string) bool {
	for _, ip := range cert.IPAddresses {
		if ip.String() == want {
			return true
		}
	}
	return false
}
