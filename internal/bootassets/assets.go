// Package bootassets embeds static boot-time binaries shipped with clustr-serverd.
// These are served over HTTP by the BootHandler to PXE/UEFI HTTP boot clients
// that need to chainload into iPXE before executing the clustr boot script.
package bootassets

import _ "embed"

// IPXEEFI is the iPXE UEFI binary for x86-64 (ipxe.efi).
//
// Provenance: built from https://github.com/ipxe/ipxe at tag v1.21.1
// with EXTRA_CFLAGS="-DCOLOUR_CMD -DCONSOLE_CMD" NO_WERROR=1 (x86_64 EFI target).
// IMAGE_PNG is omitted from EXTRA_CFLAGS because config/general.h already defines
// it; passing -DIMAGE_PNG twice causes a redefinition error on GCC 11+.
//
// CI verification: .github/workflows/ipxe-build.yml builds ipxe.efi from source
// and compares its SHA-256 to deploy/pxe/ipxe.efi.sha256 on every push and tag.
// A mismatch fails the build — no release artifact ships without a verified hash.
//
// SHA-256: b09d02dd8903cac6ff7f85988c0b10b0069fb118c197a9d5f07e018806dfa2b4
//
//go:embed ipxe.efi
var IPXEEFI []byte
