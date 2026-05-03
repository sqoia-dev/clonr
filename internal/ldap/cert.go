// Package ldap implements the clustr LDAP module — slapd lifecycle management,
// user/group CRUD, and node-side sssd configuration.
package ldap

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// certBundle holds a PEM-encoded certificate and its corresponding private key.
type certBundle struct {
	CertPEM     []byte
	KeyPEM      []byte
	Fingerprint string // SHA-256 hex fingerprint of the DER cert
	NotAfter    time.Time
}

// generateCA creates a self-signed RSA-4096 CA certificate valid for 30 years.
// The CA is used only to sign the slapd server certificate; it is not added to
// any general system trust store except on node deployment.
func generateCA(commonName string) (*certBundle, *rsa.PrivateKey, *x509.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ldap cert: generate CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ldap cert: CA serial: %w", err)
	}

	notBefore := time.Now().Add(-1 * time.Minute) // 1-minute back-date to avoid clock-skew rejections
	notAfter := notBefore.Add(30 * 365 * 24 * time.Hour)

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"clustr LDAP CA"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ldap cert: CA sign: %w", err)
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ldap cert: CA parse: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	fp := certFingerprint(derBytes)

	return &certBundle{
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		Fingerprint: fp,
		NotAfter:    notAfter,
	}, key, cert, nil
}

// generateServerCert creates an RSA-4096 server certificate signed by the given
// CA, valid for 5 years. SANs include the hostname, primary IP, and clustr.local.
// Per the design spec, we only bind ldaps:// on 636 — no StartTLS.
func generateServerCert(hostname, primaryIP string, caKey *rsa.PrivateKey, caCert *x509.Certificate) (*certBundle, error) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, fmt.Errorf("ldap cert: generate server key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("ldap cert: server serial: %w", err)
	}

	notBefore := time.Now().Add(-1 * time.Minute)
	notAfter := notBefore.Add(5 * 365 * 24 * time.Hour)

	// Populate SANs: DNS names + IP addresses.
	// "clustr-server" is the canonical hostname alias used in sssd.conf and
	// /etc/hosts entries pushed to nodes. It must be in the SAN list so nodes
	// connecting with ldaps://clustr-server:636 pass TLS hostname verification.
	// "clustr" is the short alias; "clustr.local" is the mDNS fallback.
	dnsNames := []string{"clustr-server", "clustr", "clustr.local"}
	if hostname != "" && hostname != "clustr-server" && hostname != "clustr" {
		dnsNames = append(dnsNames, hostname)
	}
	// Always include loopback addresses so local probes (readiness check, health
	// checker) can dial ldaps://127.0.0.1:636 and pass TLS hostname verification.
	// Go's TLS verifier matches ServerName against IP SANs when the ServerName is
	// an IP literal, so 127.0.0.1 must appear explicitly in the SAN list.
	// Certs are regenerated on every Enable(), so no migration is required.
	ipAddresses := []net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::1"),
	}
	if primaryIP != "" {
		if ip := net.ParseIP(primaryIP); ip != nil {
			ipAddresses = append(ipAddresses, ip)
		}
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   hostname,
			Organization: []string{"clustr LDAP"},
		},
		DNSNames:    dnsNames,
		IPAddresses: ipAddresses,
		NotBefore:   notBefore,
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("ldap cert: server sign: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	fp := certFingerprint(derBytes)

	return &certBundle{
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		Fingerprint: fp,
		NotAfter:    notAfter,
	}, nil
}

// randomSerial returns a cryptographically random 128-bit serial number.
func randomSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, max)
}

// certFingerprint returns the SHA-256 hex fingerprint of a DER-encoded certificate.
func certFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// parsePEMPrivateKey decodes an RSA private key from PEM bytes.
// Used when re-loading the CA key from the database to sign a new server cert.
func parsePEMPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("ldap cert: no PEM block found in private key data")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ldap cert: parse PKCS1 private key: %w", err)
	}
	return key, nil
}

// parsePEMCertificate decodes an X.509 certificate from PEM bytes.
func parsePEMCertificate(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("ldap cert: no PEM block found in certificate data")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ldap cert: parse certificate: %w", err)
	}
	return cert, nil
}
