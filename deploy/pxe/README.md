# deploy/pxe — iPXE EFI Binary

## Binary provenance

| Field | Value |
|---|---|
| Filename | `ipxe.efi` |
| iPXE version | Unknown — see "Verification" below |
| Architecture | x86_64 EFI |
| SHA-256 | `868aa34057ff416ebf2fdfb5781de035e2c540477c04039198a9f8a9c6130034` |
| Build flags | Unknown — binary was committed without recorded provenance |

## Verification

Verify the binary has not been modified:

```bash
sha256sum -c deploy/pxe/ipxe.efi.sha256
```

Expected output: `ipxe.efi: OK`

## Rebuilding from source (recommended)

Shipping a prebuilt binary without recorded provenance is a supply-chain risk.
Rebuild from the official iPXE source to establish a trusted baseline:

```bash
git clone https://github.com/ipxe/ipxe.git
cd ipxe/src

# Standard EFI build (UEFI chainload target):
make bin-x86_64-efi/ipxe.efi

# With embedded clonr boot script (recommended for production):
# Create a script file, e.g. clonr.ipxe:
#   #!ipxe
#   dhcp
#   chain http://${next-server}/boot.ipxe
make bin-x86_64-efi/ipxe.efi EMBED=/path/to/clonr.ipxe

# Record the version and hash of the resulting binary:
git -C . describe --tags
sha256sum bin-x86_64-efi/ipxe.efi
```

Replace `deploy/pxe/ipxe.efi` with the rebuilt binary and update
`deploy/pxe/ipxe.efi.sha256` with the new hash.

## TODO

- Rebuild from source with a pinned iPXE commit and record the version + build flags here.
- Embed the clonr boot script at build time rather than relying on a generic chainload.
