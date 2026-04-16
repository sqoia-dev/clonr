package image

import (
	"archive/tar"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// elfMagic is the 4-byte ELF magic number at the start of any ELF binary.
var elfMagic = [4]byte{0x7f, 'E', 'L', 'F'}

// elfHeader is the minimal ELF header fields we need.
// e_machine is a uint16 at byte offset 18, little-endian on all targets clonr supports.
const elfEMachineOffset = 18

// Known e_machine values (ELF spec table).
const (
	elfMachineI386   = 0x0003 // EM_386
	elfMachineX86_64 = 0x003E // EM_X86_64
	elfMachineAArch64 = 0x00B7 // EM_AARCH64
)

// eMachineToArch maps ELF e_machine values to the clonr arch strings used in
// the base_images DB column and the UI.
func eMachineToArch(eMachine uint16) (string, error) {
	switch eMachine {
	case elfMachineX86_64:
		return "x86_64", nil
	case elfMachineAArch64:
		return "aarch64", nil
	case elfMachineI386:
		return "i386", nil
	default:
		return "", fmt.Errorf("image arch: unknown e_machine 0x%04X", eMachine)
	}
}

// wellKnownBinaries is the ordered list of paths inside the rootfs that we
// probe when detecting architecture. We try each in order and return on the
// first ELF match. Paths are relative to the rootfs root (no leading slash).
var wellKnownBinaries = []string{
	"bin/bash",
	"usr/bin/bash",
	"bin/sh",
	"usr/bin/sh",
	"usr/lib/systemd/systemd",
}

// parseELFMachine reads exactly 20 bytes from r and returns the e_machine
// field if the magic is valid. Returns an error if the magic is wrong or the
// e_machine is unknown.
func parseELFMachine(r io.Reader) (uint16, error) {
	var buf [20]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, fmt.Errorf("image arch: read ELF header: %w", err)
	}
	var magic [4]byte
	copy(magic[:], buf[:4])
	if magic != elfMagic {
		return 0, fmt.Errorf("image arch: not an ELF binary (magic %x)", buf[:4])
	}
	eMachine := binary.LittleEndian.Uint16(buf[elfEMachineOffset : elfEMachineOffset+2])
	return eMachine, nil
}

// DetectArchFromRootfsDir detects the CPU architecture of an extracted rootfs
// directory by reading the ELF header of the first found well-known binary.
//
// rootfsDir is the absolute path to the extracted rootfs directory
// (e.g. /var/lib/clonr/images/<id>/rootfs).
func DetectArchFromRootfsDir(rootfsDir string) (string, error) {
	if rootfsDir == "" {
		return "", fmt.Errorf("image arch: rootfs dir path is empty")
	}
	for _, rel := range wellKnownBinaries {
		path := filepath.Join(rootfsDir, rel)
		f, err := os.Open(path)
		if err != nil {
			continue // not present — try next
		}
		eMachine, err := parseELFMachine(f)
		f.Close()
		if err != nil {
			continue // not a valid ELF — try next
		}
		arch, err := eMachineToArch(eMachine)
		if err != nil {
			continue // unknown machine type — try next
		}
		return arch, nil
	}
	return "", fmt.Errorf("image arch: no well-known ELF binary found in rootfs dir %s", rootfsDir)
}

// DetectArchFromTarball detects the CPU architecture of a rootfs stored as a
// gzip-compressed tar archive by streaming the tarball and reading the ELF
// header of the first found well-known binary without extracting the full file.
//
// blobPath is the absolute path to the .tar.gz / .blob file.
func DetectArchFromTarball(blobPath string) (string, error) {
	if blobPath == "" {
		return "", fmt.Errorf("image arch: blob path is empty")
	}

	f, err := os.Open(blobPath)
	if err != nil {
		return "", fmt.Errorf("image arch: open blob %s: %w", blobPath, err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("image arch: open gzip stream %s: %w", blobPath, err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("image arch: read tar entry: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Normalize the path: strip leading "./" or "/" so we can compare
		// against wellKnownBinaries which have no leading slash.
		name := strings.TrimPrefix(hdr.Name, "./")
		name = strings.TrimPrefix(name, "/")

		for _, wk := range wellKnownBinaries {
			if name != wk {
				continue
			}
			eMachine, err := parseELFMachine(tr)
			if err != nil {
				break // not a valid ELF; continue scanning tar
			}
			arch, err := eMachineToArch(eMachine)
			if err != nil {
				break // unknown machine type; continue scanning tar
			}
			return arch, nil
		}
	}

	return "", fmt.Errorf("image arch: no well-known ELF binary found in tarball %s", blobPath)
}
