# clustr Release Process

## Overview

Releases are triggered by pushing a semver tag (`vMAJOR.MINOR.PATCH`) to `main`.
The release workflow (`release.yml`) gates publication on:

1. `test` — go vet + go test (always required)
2. `build-client` / `build-server` — cross-platform binary builds

`lab-validate` runs in parallel but is NOT in `needs:` — it is informational only
(GitHub-hosted runners cannot reach the private lab network 192.168.1.x).

The Docker image is published separately by `docker.yml` on the same tag trigger.

## Tagging a Release

```bash
git tag -a vX.Y.Z -m "feat: vX.Y.Z — <summary>"
git push origin vX.Y.Z
```

Watch the run:
```bash
gh run list --workflow=release.yml --limit 1
gh run watch <run-id>
```

All jobs in `release.yml` must be green (or informational/allowed-to-fail) before
the release is considered published.

## iPXE Binary

The `ipxe.efi` binary is committed at two paths:

| Path | Purpose |
|---|---|
| `deploy/pxe/ipxe.efi` | Reference binary for operators; SHA-256 checked by CI |
| `internal/bootassets/ipxe.efi` | Embedded in `clustr-serverd` via `go:embed` |

Both must be identical. The `ipxe-build.yml` workflow builds from source on every
tag push, computes the SHA-256, and verifies it against `deploy/pxe/ipxe.efi.sha256`.

### Rebuilding iPXE

When upgrading the iPXE tag or changing build flags:

1. Build on a machine with GCC 11+, following `deploy/pxe/BUILD.md`
2. Copy the new binary to both `deploy/pxe/ipxe.efi` and `internal/bootassets/ipxe.efi`
3. Update `deploy/pxe/ipxe.efi.sha256` with `sha256sum ipxe.efi | awk '{print $1}'`
4. Update the SHA-256 comment in `internal/bootassets/assets.go`
5. Update `IPXE_TAG` and `IPXE_EXTRA_CFLAGS` in `.github/workflows/ipxe-build.yml`
6. Commit and push — `ipxe-build.yml` will verify the hash on the next tag

**Current build flags** (v1.21.1): `EXTRA_CFLAGS="-DCOLOUR_CMD -DCONSOLE_CMD" NO_WERROR=1`
`IMAGE_PNG` is omitted from `EXTRA_CFLAGS` — it is already defined in iPXE's
`config/general.h`. Passing it again causes a redefinition error on GCC 11+.

## Lab Validation Gate

`lab-validate.yml` reimages four Proxmox lab VMs (vm201 BIOS, vm202 UEFI, vm206,
vm207) via the clustr API and waits for each to reach a serial console login prompt.

### Current Status: INFORMATIONAL

The lab network (`192.168.1.x`) is behind a private NAT and is not reachable from
GitHub-hosted runners. The `lab-validate` job runs in parallel with the build jobs
but is NOT in the `release` job's `needs:` list, so failures do not block release
publication.

**Gap**: The lab gate is not load-bearing. Regressions in PXE boot or image
provisioning will not be caught by CI until a self-hosted runner is registered.
Manual validation on the Proxmox lab is required before tagging releases that
touch the boot path, initramfs, or image provisioning code.

### Upgrading to a Load-Bearing Gate (Self-Hosted Runner)

When the lab gate needs to be load-bearing:

1. On the Proxmox lab host (192.168.1.223) or cloner (192.168.1.151):

```bash
mkdir -p /opt/actions-runner && cd /opt/actions-runner
# Get the runner download URL from:
# https://github.com/sqoia-dev/clustr/settings/actions/runners/new
# Download and extract the runner tarball, then:
./config.sh --url https://github.com/sqoia-dev/clustr \
            --token <REGISTRATION_TOKEN> \
            --labels proxmox-lab \
            --name lab-runner-01 \
            --unattended
./svc.sh install
./svc.sh start
```

   - Use a **repo-scoped** registration token (not org-wide). Tokens expire after
     1 hour — generate them immediately before running `config.sh`.
   - Never commit a registration token to git.
   - The runner's lifecycle (start/stop/replace) is operator responsibility.
     Document any runner replacement in the ops log.

2. In `lab-validate.yml`, replace:
   ```yaml
   runs-on: ubuntu-22.04
   continue-on-error: true
   ```
   with:
   ```yaml
   runs-on: [self-hosted, proxmox-lab]
   ```
   Remove `continue-on-error: true` from the job.

3. In `release.yml`, add `lab-validate` back to the `release` job's `needs:`:
   ```yaml
   needs: [build-client, build-server, lab-validate]
   ```

4. Remove the SSH key setup step from `lab-validate.yml` (the runner is inside the NAT).

5. Test by pushing a `v*` tag and verifying the lab-validate job passes.
