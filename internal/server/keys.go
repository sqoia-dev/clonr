// Package server — keys.go embeds the clustr GPG release signing public key.
//
// The key is committed at build/slurm/keys/clustr-release.asc.pub and is the
// single source of truth.  It is embedded at compile time so:
//   - A freshly installed clustr-serverd can write RPM-GPG-KEY-clustr to the
//     repo directory without a network round-trip.
//   - The deploy code (PR4) can inject the key into node chroots at
//     /etc/pki/rpm-gpg/RPM-GPG-KEY-clustr before running dnf install.
package server

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed ../../build/slurm/keys/clustr-release.asc.pub
var clustrReleasePubkeyBytes []byte

// GPGKeyBytes returns the embedded clustr release GPG public key (ASCII-armored).
// Safe to call from multiple goroutines; the returned slice is a copy.
func GPGKeyBytes() []byte {
	out := make([]byte, len(clustrReleasePubkeyBytes))
	copy(out, clustrReleasePubkeyBytes)
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
