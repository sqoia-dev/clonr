// Package initramfs provides helpers for inspecting clustr initramfs images.
package initramfs

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"
)

// ExtractKernelVersion returns the kernel version embedded in a gzipped cpio
// initramfs image at path.
//
// The version is read from the directory name under lib/modules/<version>/ in
// the archive — the same location used by modprobe and the Linux kernel.
//
// Implementation: pure-Go gzip + newc (SVR4 ASCII) cpio parser using only the
// standard library. This removes the dependency on cpio and zcat binaries so
// the function works on any host regardless of which tools are installed.
//
// Returns a non-nil error if the file cannot be decompressed, if the archive
// format is unrecognised, or if no lib/modules entry is found. The caller
// should treat an error as "version unknown" and continue rather than fail hard.
func ExtractKernelVersion(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("initramfs: path is empty")
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("initramfs: open %s: %w", path, err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("initramfs: gzip open %s: %w", path, err)
	}
	defer gr.Close()

	return scanCpioForKernelVersion(gr)
}

// scanCpioForKernelVersion reads a cpio newc (SVR4 ASCII) stream from r and
// returns the first lib/modules/<version> directory name found.
//
// newc header layout — 13 fields, each exactly 8 ASCII hex digits, no spaces:
//
//	magic(6) ino mode uid gid nlink mtime filesize devmaj devmin
//	rdevmaj rdevmin namesize check
//
// Total header: 110 bytes. Followed by namesize bytes (NUL-terminated name),
// padded to 4-byte boundary, then filesize bytes of data padded to 4-byte
// boundary. magic is "070701" (newc) or "070702" (newc with CRC).
func scanCpioForKernelVersion(r io.Reader) (string, error) {
	const headerSize = 110

	buf := make([]byte, headerSize)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return "", fmt.Errorf("initramfs: cpio read header: %w", err)
		}

		magic := string(buf[0:6])
		if magic != "070701" && magic != "070702" {
			return "", fmt.Errorf("initramfs: unexpected cpio magic %q (want 070701 or 070702)", magic)
		}

		filesize := parseHex8(buf[54:62])
		namesize := parseHex8(buf[94:102])

		if namesize == 0 {
			return "", fmt.Errorf("initramfs: cpio namesize=0 (corrupt archive)")
		}

		// Read the entry name (NUL-terminated, padded to 4-byte boundary after header+name).
		nameBuf := make([]byte, namesize)
		if _, err := io.ReadFull(r, nameBuf); err != nil {
			return "", fmt.Errorf("initramfs: cpio read name: %w", err)
		}
		// Trim trailing NUL.
		name := strings.TrimRight(string(nameBuf), "\x00")

		// Discard padding bytes after header (110 bytes) + name.
		namePad := cpioAlign4(headerSize + int(namesize))
		if namePad > 0 {
			if _, err := io.ReadFull(r, make([]byte, namePad)); err != nil {
				return "", fmt.Errorf("initramfs: cpio skip name pad: %w", err)
			}
		}

		// TRAILER marks end of archive.
		if name == "TRAILER!!!" {
			break
		}

		// Check for lib/modules/<version> match before skipping file data.
		const prefix = "lib/modules/"
		if strings.HasPrefix(name, prefix) {
			rest := name[len(prefix):]
			if idx := strings.Index(rest, "/"); idx >= 0 {
				rest = rest[:idx]
			}
			if rest != "" {
				return rest, nil
			}
		}

		// Skip file data + padding.
		if filesize > 0 {
			totalData := int64(filesize) + int64(cpioAlign4(int(filesize)))
			if _, err := io.CopyN(io.Discard, r, totalData); err != nil {
				return "", fmt.Errorf("initramfs: cpio skip data: %w", err)
			}
		}
	}

	return "", fmt.Errorf("initramfs: no lib/modules entry found in archive")
}

// cpioAlign4 returns the number of padding bytes needed to bring (base+n) to a
// 4-byte boundary. base is the fixed offset before the variable-length field
// (110 for name, 0 for data since data starts on a 4-byte boundary already).
func cpioAlign4(n int) int {
	return (4 - n%4) % 4
}

// parseHex8 parses an 8-character ASCII hex field from a cpio newc header.
// Returns 0 if the field is malformed rather than propagating an error, since a
// single corrupt field should not abort the entire scan.
func parseHex8(b []byte) uint32 {
	if len(b) < 8 {
		return 0
	}
	var v uint64
	for _, c := range b[:8] {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint64(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= uint64(c-'A') + 10
		default:
			return 0
		}
	}
	return uint32(v)
}
