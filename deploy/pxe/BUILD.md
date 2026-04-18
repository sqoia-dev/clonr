# iPXE Build Instructions — Colour / cpair Support

## Problem

The stock `ipxe.efi` committed in this directory was built without colour
support. The `cpair` iPXE command (used in clonr boot scripts to set foreground
and background colour pairs) is gated behind the `COLOUR_CMD` compile-time
feature flag. Attempting to call `cpair` in a boot script against this binary
produces:

```
No such command: cpair
```

The binary must be compiled from source with the flags below to enable `cpair`
and related display features.

## Required Feature Flags

| Flag | Purpose |
|---|---|
| `COLOUR_CMD` | Enables the `cpair` command for setting colour pairs in boot scripts |
| `CONSOLE_CMD` | Enables the `console` command for controlling output console selection |
| `IMAGE_PNG` | Enables PNG image loading (required for logo/splash display) |

## Build Environment

Tested on: Ubuntu 22.04 / Debian 12 (x86_64)

Install build dependencies:

```bash
apt-get install -y \
    gcc binutils make perl \
    liblzma-dev mtools xorriso \
    gcc-x86-64-linux-gnu \
    isolinux
```

## Build Steps

```bash
git clone https://github.com/ipxe/ipxe.git
cd ipxe

# Pin to a known-good release tag (check https://github.com/ipxe/ipxe/releases):
git checkout v1.21.1

cd src

# Build ipxe.efi for x86_64 UEFI with colour, console, and PNG support.
# Feature flags are passed as preprocessor defines via EXTRA_CFLAGS.
make bin-x86_64-efi/ipxe.efi \
    EXTRA_CFLAGS="-DCOLOUR_CMD -DIMAGE_PNG -DCONSOLE_CMD"
```

Alternatively, add the flags permanently in `src/config/general.h` before
building (preferred for reproducible builds):

```c
// src/config/general.h — add these lines in the "Image types" or
// "Commands" section, or append at the bottom before the closing #endif:
#define COLOUR_CMD        // cpair command
#define CONSOLE_CMD       // console command
#define IMAGE_PNG         // PNG image support
```

Then build without `EXTRA_CFLAGS`:

```bash
make bin-x86_64-efi/ipxe.efi
```

## Embed the clonr Boot Script (Recommended for Production)

Embedding the boot script avoids an extra TFTP/HTTP round-trip at boot time:

```bash
# Create a minimal chainload script, e.g.:
cat > /tmp/clonr.ipxe << 'EOF'
#!ipxe
dhcp
chain http://${next-server}/boot.ipxe
EOF

make bin-x86_64-efi/ipxe.efi \
    EXTRA_CFLAGS="-DCOLOUR_CMD -DIMAGE_PNG -DCONSOLE_CMD" \
    EMBED=/tmp/clonr.ipxe
```

## Replace the Committed Binary

After a successful build:

```bash
# Copy the new binary into the repo:
cp ipxe/src/bin-x86_64-efi/ipxe.efi deploy/pxe/ipxe.efi

# Update the checksum file:
sha256sum deploy/pxe/ipxe.efi > deploy/pxe/ipxe.efi.sha256

# Commit with the iPXE version tag and build flags recorded:
git add deploy/pxe/ipxe.efi deploy/pxe/ipxe.efi.sha256
git commit -m "chore: rebuild ipxe.efi v1.21.1 with COLOUR_CMD CONSOLE_CMD IMAGE_PNG"
```

Update `deploy/pxe/README.md` to record the iPXE version tag, commit SHA, and
build flags used.

## Verification

```bash
# Confirm the binary is a valid EFI image:
file deploy/pxe/ipxe.efi
# Expected: PE32+ executable (EFI application) x86-64, for MS Windows

# Verify the checksum matches:
sha256sum -c deploy/pxe/ipxe.efi.sha256
# Expected: deploy/pxe/ipxe.efi: OK
```

## Current Binary Status

The `ipxe.efi` currently committed in this directory does NOT have `COLOUR_CMD`
compiled in. Boot scripts using `cpair` will fail with "No such command: cpair".
Rebuild using the steps above before deploying to a production PXE environment
that uses colour-coded boot menus.
