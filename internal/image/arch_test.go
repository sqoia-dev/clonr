package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeELFHeader returns a 20-byte minimal ELF header with the given e_machine.
// Bytes 0-3: magic 0x7f ELF; bytes 18-19: e_machine (little-endian).
func makeELFHeader(eMachine uint16) []byte {
	buf := make([]byte, 20)
	copy(buf[:4], elfMagic[:])
	buf[elfEMachineOffset] = byte(eMachine)
	buf[elfEMachineOffset+1] = byte(eMachine >> 8)
	return buf
}

// makeTarGz creates a gzip-compressed tar at dest containing a single regular
// file at entryName with the given body bytes.
func makeTarGz(t *testing.T, dest, entryName string, body []byte) {
	t.Helper()
	f, err := os.Create(dest)
	if err != nil {
		t.Fatalf("makeTarGz: create %s: %v", dest, err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name:     entryName,
		Typeflag: tar.TypeReg,
		Size:     int64(len(body)),
		Mode:     0o755,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("makeTarGz: write header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("makeTarGz: write body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("makeTarGz: close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("makeTarGz: close gzip: %v", err)
	}
}

// ─── DetectArchFromTarball ───────────────────────────────────────────────────

func TestDetectArchFromTarball_x86_64(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "rootfs.tar.gz")
	makeTarGz(t, blob, "bin/bash", makeELFHeader(elfMachineX86_64))

	got, err := DetectArchFromTarball(blob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "x86_64" {
		t.Errorf("got %q, want %q", got, "x86_64")
	}
}

func TestDetectArchFromTarball_aarch64(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "rootfs.tar.gz")
	makeTarGz(t, blob, "usr/bin/bash", makeELFHeader(elfMachineAArch64))

	got, err := DetectArchFromTarball(blob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "aarch64" {
		t.Errorf("got %q, want %q", got, "aarch64")
	}
}

func TestDetectArchFromTarball_i386(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "rootfs.tar.gz")
	makeTarGz(t, blob, "bin/sh", makeELFHeader(elfMachineI386))

	got, err := DetectArchFromTarball(blob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "i386" {
		t.Errorf("got %q, want %q", got, "i386")
	}
}

func TestDetectArchFromTarball_dotSlashPrefix(t *testing.T) {
	// Tarballs created with "tar -C rootfsdir" often prefix entries with "./"
	dir := t.TempDir()
	blob := filepath.Join(dir, "rootfs.tar.gz")
	makeTarGz(t, blob, "./bin/bash", makeELFHeader(elfMachineX86_64))

	got, err := DetectArchFromTarball(blob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "x86_64" {
		t.Errorf("got %q, want %q", got, "x86_64")
	}
}

func TestDetectArchFromTarball_unknownEMachine(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "rootfs.tar.gz")
	// e_machine 0xFFFF is not a known architecture.
	makeTarGz(t, blob, "bin/bash", makeELFHeader(0xFFFF))

	_, err := DetectArchFromTarball(blob)
	if err == nil {
		t.Fatal("expected error for unknown e_machine, got nil")
	}
}

func TestDetectArchFromTarball_nonELFFile(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "rootfs.tar.gz")
	// A shell script — not an ELF binary.
	makeTarGz(t, blob, "bin/bash", []byte("#!/bin/sh\necho hello\n"))

	_, err := DetectArchFromTarball(blob)
	if err == nil {
		t.Fatal("expected error for non-ELF file, got nil")
	}
}

func TestDetectArchFromTarball_noKnownBinaries(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "rootfs.tar.gz")
	// Tarball contains only /etc/os-release — none of our well-known binaries.
	makeTarGz(t, blob, "etc/os-release", []byte(`ID=rocky
VERSION_ID="10.1"
`))

	_, err := DetectArchFromTarball(blob)
	if err == nil {
		t.Fatal("expected error when no well-known binary found, got nil")
	}
}

func TestDetectArchFromTarball_emptyPath(t *testing.T) {
	_, err := DetectArchFromTarball("")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

func TestDetectArchFromTarball_notGzip(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "notgzip.blob")
	if err := os.WriteFile(blob, []byte("definitely not a gzip stream"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, err := DetectArchFromTarball(blob)
	if err == nil {
		t.Fatal("expected error for non-gzip file, got nil")
	}
}

// ─── DetectArchFromRootfsDir ─────────────────────────────────────────────────

func TestDetectArchFromRootfsDir_x86_64(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "bash"), makeELFHeader(elfMachineX86_64), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := DetectArchFromRootfsDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "x86_64" {
		t.Errorf("got %q, want %q", got, "x86_64")
	}
}

func TestDetectArchFromRootfsDir_aarch64_fallback(t *testing.T) {
	// Only usr/bin/bash present (bin/bash absent).
	dir := t.TempDir()
	usrBinDir := filepath.Join(dir, "usr", "bin")
	if err := os.MkdirAll(usrBinDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(usrBinDir, "bash"), makeELFHeader(elfMachineAArch64), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := DetectArchFromRootfsDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "aarch64" {
		t.Errorf("got %q, want %q", got, "aarch64")
	}
}

func TestDetectArchFromRootfsDir_noBinaries(t *testing.T) {
	dir := t.TempDir()
	// Empty directory — no binaries.
	_, err := DetectArchFromRootfsDir(dir)
	if err == nil {
		t.Fatal("expected error when no well-known binary found, got nil")
	}
}

func TestDetectArchFromRootfsDir_emptyPath(t *testing.T) {
	_, err := DetectArchFromRootfsDir("")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

// ─── parseELFMachine (internal) ──────────────────────────────────────────────

func TestParseELFMachine_shortRead(t *testing.T) {
	// Only 10 bytes — too short for a full ELF header.
	_, err := parseELFMachine(bytes.NewReader(make([]byte, 10)))
	if err == nil {
		t.Fatal("expected error for short read, got nil")
	}
}

func TestParseELFMachine_badMagic(t *testing.T) {
	buf := make([]byte, 20)
	buf[0] = 0xDE
	buf[1] = 0xAD
	buf[2] = 0xBE
	buf[3] = 0xEF
	_, err := parseELFMachine(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("expected error for bad ELF magic, got nil")
	}
}
