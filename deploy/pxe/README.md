# deploy/pxe — iPXE EFI Binary

## Binary provenance

| Field | Value |
|---|---|
| Filename | `ipxe.efi` |
| iPXE version | `v1.21.1` |
| iPXE upstream commit | pinned via `git clone --branch v1.21.1` |
| Architecture | x86_64 UEFI (EFI application) |
| Build target | `bin-x86_64-efi/ipxe.efi` |
| Build flags | `EXTRA_CFLAGS="-DCOLOUR_CMD -DIMAGE_PNG -DCONSOLE_CMD"` |
| SHA-256 | `868aa34057ff416ebf2fdfb5781de035e2c540477c04039198a9f8a9c6130034` |
| CI verification | `.github/workflows/ipxe-build.yml` — builds from source in CI and compares SHA-256 |

### Feature flags

| Flag | Purpose |
|---|---|
| `COLOUR_CMD` | Enables the `cpair` command for boot menu colour coding |
| `CONSOLE_CMD` | Enables the `console` command for output console selection |
| `IMAGE_PNG` | Enables PNG image loading for logo/splash display |

## Verification

### Via CI

Every push to main and every `v*` tag triggers `.github/workflows/ipxe-build.yml`, which:
1. Clones iPXE at the pinned tag (`v1.21.1`)
2. Builds `ipxe.efi` with the feature flags above
3. Computes SHA-256 of the built binary
4. Compares it to the value in `deploy/pxe/ipxe.efi.sha256`
5. Fails the build if they differ

On tag push, the built binary and its SHA-256 are attached to the GitHub Release.

### Manual verification

```bash
# Verify the committed binary has not been modified:
sha256sum -c deploy/pxe/ipxe.efi.sha256
# Expected: ipxe.efi: OK

# Confirm it is a valid x86_64 EFI application:
file deploy/pxe/ipxe.efi
# Expected: PE32+ executable (EFI application) x86-64, for MS Windows
```

## Updating to a new iPXE version

1. Update `IPXE_TAG` in `.github/workflows/ipxe-build.yml`.
2. Rebuild locally per `deploy/pxe/BUILD.md`:
   ```bash
   git clone --depth 1 --branch <new-tag> https://github.com/ipxe/ipxe.git /tmp/ipxe
   make -C /tmp/ipxe/src bin-x86_64-efi/ipxe.efi \
     EXTRA_CFLAGS="-DCOLOUR_CMD -DIMAGE_PNG -DCONSOLE_CMD"
   ```
3. Copy the built binary:
   ```bash
   cp /tmp/ipxe/src/bin-x86_64-efi/ipxe.efi deploy/pxe/ipxe.efi
   cp /tmp/ipxe/src/bin-x86_64-efi/ipxe.efi internal/bootassets/ipxe.efi
   sha256sum deploy/pxe/ipxe.efi > deploy/pxe/ipxe.efi.sha256
   ```
4. Update the SHA-256 comment in `internal/bootassets/assets.go`.
5. Update the version fields in this README.
6. Commit: `chore: rebuild ipxe.efi <new-tag> with COLOUR_CMD CONSOLE_CMD IMAGE_PNG`
7. Push — CI will verify the new binary against the committed SHA-256.

## Build instructions

See `deploy/pxe/BUILD.md` for full build instructions including the colour/cpair
feature flags required by the clustr boot menu.
