// Package server — keys.go embeds the clustr GPG release signing public key
// and the upstream Rocky Linux 9 and EPEL 9 release keys used for dep RPM
// verification (GAP-17 hardening).
//
// The keys live at keys/*.asc.pub (copies of build/slurm/keys/*.asc.pub
// committed here so that Go's //go:embed directive can reference them without
// a ".." path — embed paths must not escape the containing module directory).
// The canonical source of truth is build/slurm/keys/; when any key file is
// updated (on key rotation) this copy must be updated in the same PR.
//
// The embedded keys are used at compile time so:
//   - A freshly installed clustr-serverd can write RPM-GPG-KEY-{clustr,rocky-9,EPEL-9}
//     to the repo directory and chroot without a network round-trip.
//   - bundle.go::verifyRPMSignatures passes the appropriate key to each rpm --import
//     call in its two-pass isolated-db verification.
//   - finalize.go::installSlurmInChroot writes all three keys into the chroot at
//     /etc/pki/rpm-gpg/ so dnf can gpgcheck=1 against each respective stanza.
package server

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed keys/clustr-release.asc.pub
var clustrReleasePubkeyBytes []byte

//go:embed keys/RPM-GPG-KEY-rocky-9.asc.pub
var rockyKeyBytes []byte

//go:embed keys/RPM-GPG-KEY-EPEL-9.asc.pub
var epelKeyBytes []byte

// GPGKeyBytes returns the embedded clustr release GPG public key (ASCII-armored).
// Safe to call from multiple goroutines; the returned slice is a copy.
func GPGKeyBytes() []byte {
	out := make([]byte, len(clustrReleasePubkeyBytes))
	copy(out, clustrReleasePubkeyBytes)
	return out
}

// RockyKeyBytes returns the embedded Rocky Linux 9 release GPG public key
// (ASCII-armored).  Used to verify passthrough Rocky/EPEL dep RPMs in
// bundle.go::verifyRPMSignatures (pass 2) and written to node chroots by
// finalize.go::installSlurmInChroot.
func RockyKeyBytes() []byte {
	out := make([]byte, len(rockyKeyBytes))
	copy(out, rockyKeyBytes)
	return out
}

// EPELKeyBytes returns the embedded EPEL 9 release GPG public key (ASCII-armored).
// Used alongside RockyKeyBytes for dep RPM verification; munge and munge-libs
// are signed with this key.
func EPELKeyBytes() []byte {
	out := make([]byte, len(epelKeyBytes))
	copy(out, epelKeyBytes)
	return out
}

// WriteGPGKeyToRepo writes the embedded clustr release GPG public key to
// <repoDir>/RPM-GPG-KEY-clustr with mode 0644.  Idempotent: if the file
// already contains the same bytes it is left unchanged.  The parent directory
// must exist before calling.
func WriteGPGKeyToRepo(repoDir string) error {
	dest := filepath.Join(repoDir, "RPM-GPG-KEY-clustr")

	// Fast idempotency check: compare existing contents before overwriting.
	existing, err := os.ReadFile(dest)
	if err == nil && string(existing) == string(clustrReleasePubkeyBytes) {
		return nil
	}

	if err := os.WriteFile(dest, clustrReleasePubkeyBytes, 0o644); err != nil {
		return err
	}
	return nil
}

// WriteAllGPGKeysToRepo writes all three embedded GPG public keys to repoDir:
//   - RPM-GPG-KEY-clustr  (clustr release signing key)
//   - RPM-GPG-KEY-rocky-9 (Rocky Linux 9 release key — for dep RPM verification)
//   - RPM-GPG-KEY-EPEL-9  (EPEL 9 release key — for munge/munge-libs verification)
//
// Each file is written with mode 0644.  Idempotent: unchanged files are
// not rewritten.  The parent directory must exist before calling.
//
// Used by:
//   - bundle.go::extractAndInstall — writes to repoDir root for rpm --import
//   - finalize.go::installSlurmInChroot — writes to <chroot>/etc/pki/rpm-gpg/
func WriteAllGPGKeysToRepo(repoDir string) error {
	type keyFile struct {
		name  string
		bytes []byte
	}
	keys := []keyFile{
		{"RPM-GPG-KEY-clustr", clustrReleasePubkeyBytes},
		{"RPM-GPG-KEY-rocky-9", rockyKeyBytes},
		{"RPM-GPG-KEY-EPEL-9", epelKeyBytes},
	}
	for _, k := range keys {
		dest := filepath.Join(repoDir, k.name)
		existing, err := os.ReadFile(dest)
		if err == nil && string(existing) == string(k.bytes) {
			continue // already correct
		}
		if err := os.WriteFile(dest, k.bytes, 0o644); err != nil {
			return err
		}
	}
	return nil
}
