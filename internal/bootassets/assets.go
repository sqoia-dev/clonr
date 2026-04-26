// Package bootassets embeds static boot-time binaries shipped with clustr-serverd.
// These are served over HTTP by the BootHandler to PXE/UEFI HTTP boot clients
// that need to chainload into iPXE before executing the clustr boot script.
package bootassets

import _ "embed"

// IPXEEFI is the iPXE UEFI binary for x86-64 (ipxe.efi).
//
// Provenance: built from https://github.com/ipxe/ipxe at tag v1.21.1
// with EXTRA_CFLAGS="-DCOLOUR_CMD -DIMAGE_PNG -DCONSOLE_CMD" (x86_64 EFI target).
//
// CI verification: .github/workflows/ipxe-build.yml builds ipxe.efi from source
// and compares its SHA-256 to deploy/pxe/ipxe.efi.sha256 on every push and tag.
// A mismatch fails the build — no release artifact ships without a verified hash.
//
// SHA-256: 868aa34057ff416ebf2fdfb5781de035e2c540477c04039198a9f8a9c6130034
//
//go:embed ipxe.efi
var IPXEEFI []byte
