# ADR-0009: Content-Only Images

**Date:** 2026-04-13
**Status:** Accepted
**Amends:** ADR-0003 (image format and distribution model), ADR-0004 (persistence schema — images table extended)
**Breaks:** All images created under the patch-based model (rocky10-seabios-blkext, rocky101). No back-compat. Rebuild required.
**Amended:** 2026-04-13 — Gilfoyle's `admin-image-customization-requirements.md` (commit `034b7d9`) surfaced three gaps in the original draft: fstab ownership split (§5A), DKMS build pipeline (§5B), and secrets envelope encryption (§5C). Those sections replace the original hand-waving in §5.5 and the open note that previously appeared here.

---

## Context

Every bug fought in Sprint 2's final eight hours of debugging was downstream of one architectural confusion: **clonr images are disk-state captures, not content-only tarballs**.

The captured rootfs includes:

- `/etc/fstab` with UUIDs from the original Anaconda install
- `/boot/loader/entries/*.conf` (BLS entries) with paths tied to the original `machine-id` and root UUID
- `/boot/grub2/grub.cfg` with `save_env` references, submenu state, and UUIDs from the original install
- `/boot/efi/EFI/*/grubx64.efi` installed against the original ESP
- `grub core.img` writes that are topology-specific (md0, diskfilter, biosboot placement)

Deploy then patches all of this to match the target layout: sed fstab UUIDs, rewrite BLS entries with a new `machine-id` prefix, patch `grub.cfg` UUIDs, strip `save_env` lines, regenerate `grub2-mkconfig`, run `grub2-install` against md devices. Each patch is a potential bug. Sprint 2 hit all of them:

- BLS entries couldn't find `md0` during `grub2-probe`
- `grub.cfg save_env` broke on diskfilter write
- ESP content ended up on the root filesystem because `mountPartitions` skipped `/boot/efi`
- `grub2-install` failed with "embedding is not possible" because the biosboot partition was in the RAID instead of per-raw-disk
- `rocky101` tar-sha256 desynced because a UI shell session mutated the rootfs without triggering a re-hash

The root cause is not any individual bug. The root cause is that the image format carries state that belongs to a specific machine topology. Patching that state at deploy time is inherently fragile: every new topology variant (RAID, NVMe, UEFI vs. BIOS, multiple disks) adds a new patch branch that may fail in a new way.

**The fix is architectural, not mechanical.** The image must carry only content — binaries, libraries, kernel modules, configuration — and must carry zero topology state. All topology-specific artifacts are generated fresh at deploy time against the actual target disks.

---

## Decision

### 1. Image Format: Content-Only Rootfs Tarball

An image is a rootfs tarball (`rootfs.tar.zst`) plus a metadata sidecar (`image.json`).

#### What the tarball CONTAINS

| Path | Included | Notes |
|---|---|---|
| `/usr/**`, `/lib/**`, `/lib64/**`, `/bin/**`, `/sbin/**` | Yes | All system binaries and libraries |
| `/etc/**` (minus excluded paths below) | Yes | Site configs, service configs, everything except topology state |
| `/lib/modules/<version>/` | Yes | Kernel modules for the deployed kernel version |
| `/boot/vmlinuz-<version>` | Yes | Kernel image |
| `/boot/initramfs-<version>.img` | Yes | Initramfs (dracut-generated at image creation time) |
| `/var/lib/**` (state dirs) | Yes | Pre-populated state, e.g. RPM database |
| `/root/**`, `/home/**` | Yes | If present in the base image; users are managed via §5.3 |

#### What the tarball EXCLUDES (generated at deploy time)

| Path | Excluded | Reason |
|---|---|---|
| `/etc/fstab` | Yes — empty file | Generated at deploy from layout spec + `blkid` of target FSes |
| `/etc/machine-id` | Yes — empty file | `systemd-firstboot` regenerates on first boot |
| `/boot/loader/entries/*.conf` | Yes — directory present but empty | BLS entries generated at deploy against target root UUID + fresh machine-id |
| `/boot/grub2/grub.cfg` | Yes — empty file | Generated at deploy; `grub2-mkconfig` runs in target chroot |
| `/boot/efi/EFI/` | Yes — directory structure present but no binaries | `grub2-install` runs at deploy for BIOS and UEFI; no pre-installed EFI binaries |
| MBR / `core.img` / biosboot artifacts | Yes — not in rootfs tarball at all | `grub2-install --target=i386-pc` runs per raw member disk at deploy |

**The tarball is firmware-agnostic.** The same `rootfs.tar.zst` deploys correctly to a BIOS node and a UEFI node. Firmware selection is a deploy-time concern, branched on the image metadata `firmware_hint` and the node's actual boot mode.

#### Metadata sidecar (`image.json`)

```json
{
  "schema_version": 1,
  "id": "<uuid>",
  "name": "rocky10-base",
  "version": "1.0.0",
  "distro": "rocky",
  "distro_version": "10.0",
  "kernel_version": "5.14.0-570.22.1.el10_0.x86_64",
  "kernel_pinned": true,
  "firmware_hint": "bios|uefi|both",
  "default_kernel_cmdline": "console=ttyS0,115200n8 crashkernel=1G-4G:192M,4G-64G:256M,64G-:512M",
  "required_kernel_modules": ["dm_mirror", "dm_raid", "md_mod"],
  "required_secrets": [
    { "name": "munge.key", "path": "/etc/munge/munge.key", "owner": "munge", "mode": "0400" },
    { "name": "root.authorized_keys", "path": "/root/.ssh/authorized_keys", "owner": "root", "mode": "0600" }
  ],
  "dkms_modules": [
    { "name": "nvidia", "expected_ko": "extra/nvidia.ko" }
  ],
  "content_sha256": "<hex>",
  "tarball_size_bytes": 1234567890,
  "package_manifest": [
    { "name": "rocky-release", "version": "10.0-1.el10.noarch", "arch": "noarch" },
    ...
  ],
  "created_at": "2026-04-13T00:00:00Z",
  "created_by": "iso-builder|host-capture",
  "clonr_version": "0.2.0"
}
```

The `content_sha256` is computed over the tarball bytes after the tarball is sealed. It is the ground truth for integrity verification. It is recomputed whenever the image content changes (package install, config overlay commit, shell session commit — see §5).

### 2. Image Creation Paths

Both creation paths produce the same output format. The image record is identical regardless of how it was created.

#### 2a. ISO Builder (Anaconda + QEMU)

1. QEMU runs Anaconda inside a VM as today. The disk image written by Anaconda is ephemeral — it is used only as a vehicle to get a clean, RPM-verified install.
2. After Anaconda exits, `clonr-iso-builder` mounts the QEMU disk image (loopback) and captures the rootfs via:

   ```bash
   tar --create --zstd \
     --exclude='./etc/fstab' \
     --exclude='./etc/machine-id' \
     --exclude='./boot/loader/entries/*' \
     --exclude='./boot/grub2/grub.cfg' \
     --exclude='./boot/efi/EFI/*/grub*.efi' \
     --exclude='./boot/efi/EFI/*/shimx64.efi' \
     --exclude='./boot/efi/EFI/BOOT/BOOTX64.EFI' \
     --one-file-system \
     -C /mnt/anaconda-root \
     -f rootfs.tar.zst \
     .
   ```

3. Empty placeholder files are written into the tarball for `/etc/fstab`, `/etc/machine-id`, and `/boot/grub2/grub.cfg` so the directory structure is correct and deploy-time writes succeed without `mkdir -p` guards.
4. The QEMU disk image is discarded. No disk-state artifact is retained.
5. `image.json` is generated: kernel version extracted from `/boot/vmlinuz-*`, package manifest from `rpm -qa --queryformat`, firmware_hint set based on the ISO's kickstart (bios/uefi/both).
6. `content_sha256` is computed and written into `image.json`.
7. Both files are registered in the DB and made available for distribution (ADR-0003 model unchanged).

#### 2b. Host Capture (rsync from running reference machine)

1. Operator designates a running Rocky/RHEL/AlmaLinux machine as the reference host.
2. `clonr capture --host <ref> --output <name>` rsync/tars the live rootfs with the same exclude list as §2a. An rsync-based capture uses `--exclude` flags; a tar-based capture uses the same `--exclude` arguments.
3. The capture tool warns if the reference host's `/boot` contains BLS entries or grub.cfg that would have been captured under the old model, so operators can verify the excludes fired correctly.
4. Same `image.json` generation as §2a.
5. The source machine is unaffected; no disk writes are made to it.

Both paths go through the same `pkg/images/builder.go` validation step that verifies the tarball does not contain any of the excluded paths before the image record is committed. This is a hard failure: a tarball containing `/etc/fstab` content is rejected.

### 3. Deploy-Time Finalize (Rewritten from Scratch)

The finalize phase in `pkg/deploy/finalize.go` is the only place topology-specific state is generated. It runs in the PXE initramfs on the target node.

#### 3a. Partition and Mount

1. Partition target disks per the node's layout spec (unchanged from current behavior).
2. Mount all partitions in depth order: `/`, `/boot`, `/boot/efi`, swap/data as applicable.
3. All mounts go under a single chroot root, e.g., `/mnt/target`.

#### 3b. Extract Content

```bash
tar --extract --zstd \
  -C /mnt/target \
  -f rootfs.tar.zst \
  --numeric-owner \
  --preserve-permissions
```

No UUID patching. No BLS rewriting. No grub.cfg sed. The tarball contains only content; there is nothing to patch.

#### 3c. Generate fstab

```bash
blkid -s UUID -o value <root_dev>    # → ROOT_UUID
blkid -s UUID -o value <boot_dev>    # → BOOT_UUID (if /boot is separate)
blkid -s UUID -o value <esp_dev>     # → ESP_UUID (if UEFI)
```

Write `/mnt/target/etc/fstab.d/00-clonr-os.fstab` from the layout spec using these UUIDs. Then assemble `/mnt/target/etc/fstab` by concatenating `00-clonr-os.fstab` with any `/etc/fstab.d/[10-89]-*.fstab` files that were extracted from the image tarball, in lexicographic order. The deploy engine MUST NOT overwrite any file matching `/etc/fstab.d/9*-*.fstab` or any `/etc/systemd/system/*.mount` unit — those paths are owned by the image overlay (see §5A for the full ownership contract). Fstab generation is deterministic and testable without a real disk — pass a mock blkid map in tests.

#### 3d. Generate machine-id and BLS entries

1. Write a fresh `machine-id` to `/mnt/target/etc/machine-id` (128-bit random hex, no newline-trailing format).
2. Create `/mnt/target/boot/loader/entries/<machine-id>-<kernel-version>.conf`:

   ```
   title Rocky Linux 10 (5.14.0-570.22.1...)
   version 5.14.0-570.22.1.el10_0.x86_64
   linux /vmlinuz-5.14.0-570.22.1.el10_0.x86_64
   initrd /initramfs-5.14.0-570.22.1.el10_0.x86_64.img
   options root=UUID=<ROOT_UUID> ro <default_kernel_cmdline> <node_kernel_cmdline_overrides>
   ```

   The kernel cmdline is: `image.json:default_kernel_cmdline` + node-level overrides from the node's layout spec. No `save_env`. No submenus.

#### 3e. Regenerate initramfs

Run in chroot against `/mnt/target`:

```bash
chroot /mnt/target dracut \
  --regenerate-all \
  --no-hostonly \
  --force
```

This ensures the initramfs contains modules appropriate for the target hardware, not the QEMU VM used during capture. Modules required for the target layout (e.g., `dm_mirror`, `md_mod`, `dm_raid`) are added via `image.json:required_kernel_modules` passed as `--add-drivers` arguments.

#### 3f. Bootloader Installation (firmware-branched)

**BIOS path:**

For each raw member disk in the layout:

```bash
grub2-install \
  --target=i386-pc \
  --boot-directory=/mnt/target/boot \
  /dev/<raw_disk_N>
```

Each raw disk must have a dedicated biosboot partition (type `21686148-6449-6E6F-744E-656564454649`, size 1MiB, no filesystem). The biosboot partition must be on the raw disk itself, NOT inside the MD array. This is the fix for the Sprint 2 "embedding is not possible" failure.

After `grub2-install`, write a minimal `/mnt/target/boot/grub2/grub.cfg` by hand — **not via `grub2-mkconfig`** — to eliminate all `save_env` and submenu state that `grub2-mkconfig` injects:

```
set default=0
set timeout=5
insmod gzio
insmod part_gpt
insmod ext2
search --no-floppy --fs-uuid --set=root <ROOT_UUID>
menuentry "Rocky Linux 10" {
  linux /boot/vmlinuz-<version> root=UUID=<ROOT_UUID> ro <cmdline>
  initrd /boot/initramfs-<version>.img
}
```

No `save_env`. No `load_env`. No `submenu`. This is intentional: the complexity `grub2-mkconfig` adds exists to handle multi-boot scenarios, rescue entries, and saved defaults. Provisioned compute nodes have exactly one OS and do not need any of it.

**UEFI path:**

```bash
grub2-install \
  --target=x86_64-efi \
  --efi-directory=/mnt/target/boot/efi \
  --bootloader-id=clonr \
  --recheck

efibootmgr \
  --create \
  --disk /dev/<primary_disk> \
  --part <esp_partition_number> \
  --label "Rocky Linux 10" \
  --loader '\EFI\clonr\grubx64.efi'
```

BLS-native path: if the distro uses `grub2` with BLS support (Rocky 9+, RHEL 9+), the BLS entry written in §3d is picked up automatically. No separate `grub.cfg` authoring is needed for UEFI with BLS-capable grub.

**Same-image, firmware-branched:** The branch is on `node.firmware_mode` (`bios` or `uefi`), not on the image. The same `rootfs.tar.zst` is used for both paths.

#### 3g. Post-deploy injection (ADR-0008 compliance)

The finalize phase continues to write:

- `/mnt/target/etc/clonr/node-token` (mode 0600) — node-scoped API key for post-boot verification (ADR-0008 §3)
- `/mnt/target/etc/systemd/system/clonr-verify-boot.service` — oneshot phone-home unit (ADR-0008 §3)
- Per-node secret overlays for all `required_secrets` declared in `image.json` (see §5C for the full delivery model)

### 4. Shell Session: Overlay Model

The chroot shell feature (currently `systemd-nspawn` into the image rootfs) must continue to work. The content-only model makes the session-corruption bug explicit: a shell session that mutates the rootfs without recomputing `content_sha256` is undefined behavior.

**Decision: overlayfs model.**

When an operator opens a shell session on an image:

1. The image rootfs is mounted read-only as the overlayfs `lowerdir`.
2. An empty overlayfs `upperdir` (writable scratch dir) is created per session.
3. `systemd-nspawn` runs against the merged overlayfs view. The session sees a fully writable filesystem; all writes go to `upperdir`.
4. On session close, the operator is presented with two choices:
   - **Discard** — the `upperdir` is deleted. The image is unchanged. `content_sha256` is unchanged.
   - **Commit** — the `upperdir` diff is applied to the image rootfs, a new image version record is created (e.g., `rocky10-base:1.0.1`), and `content_sha256` is recomputed over the new tarball. The original image version is retained and not mutated.

**Why option (b) over option (a) (read-only + explicit commit button):** The overlayfs approach allows operators to do exploratory work (install a package, check dependencies, run tests) without committing. Option (a) forces a commit decision before the shell is useful. Option (b) aligns with how OCI image layers work and makes the commit/discard choice happen at the right moment — after the operator knows whether their changes are correct.

**Commit path recomputes content_sha256.** The rocky101 sidecar desync bug is impossible under this model: `content_sha256` in `image.json` is always computed over the sealed tarball, and the tarball is only sealed via an explicit commit action, not by leaving a shell session running.

**Implementation note for Dinesh:** The overlayfs `upperdir` must be on the same filesystem as the image rootfs storage, or the diff application will require a cross-device copy. The session handler must clean up the `upperdir` on crash/disconnect, not just on clean close.

### 5. Admin Customization Surface

> **TODO — reconcile with Gilfoyle's `admin-image-customization-requirements.md` once available.** The categories below represent the complete enumeration Richard believes is required based on the problem statement and HPC deployment experience. Any category in Gilfoyle's doc not covered here requires a model extension before Sprint 3 begins.

The customization model has three scopes:

- **Image-scoped**: applies to all nodes deployed from this image, regardless of which node.
- **Group-scoped**: applies to all nodes in a named group (e.g., `gpu-nodes`, `storage-nodes`).
- **Node-scoped**: applies to a single node. Highest precedence; overrides image and group.

Merge order: image customizations → group customizations → node customizations. Later layers win on conflicts.

#### 5.1 Package Management

**At image creation time (preferred):** The kickstart or the host capture already includes the desired packages. The package manifest in `image.json` is the ground truth.

**Post-creation (declarative overlay):** Operators can attach an RPM package list to an image. At deploy time, `clonr-static` runs `dnf install --installroot=/mnt/target -y <packages>` before the bootloader step. This allows package additions without rebuilding the base image.

**Removal:** `dnf remove` in the same pre-boot chroot phase.

**Shell session (escape hatch):** The overlayfs shell (§4) can be used for one-off installs. Committing the session creates a new image version with the package baked in.

#### 5.2 Config File Overlay

Operators can attach a set of site-specific files to an image or node record. Each entry is:

```json
{
  "path": "/etc/ntp.conf",
  "content": "...",
  "mode": "0644",
  "owner": "root:root",
  "scope": "image|group|node"
}
```

At deploy time, after tarball extraction, these files are written into the target rootfs in merge order (§5 precedence). This is the correct model for site-specific configs (NTP servers, DNS resolvers, `/etc/hosts` entries, Slurm client configs, module configs) that must not be baked into the shared base image.

#### 5.3 User and UID Injection

User records are declared in the image or node spec, not baked into the image's `/etc/passwd`. At deploy time, `clonr-static` runs `useradd`/`usermod` in a chroot to write the correct UID/GID entries. This avoids UID collision across heterogeneous images and allows per-site UID namespaces.

Required fields per user entry: `username`, `uid`, `gid`, `home`, `shell`, `groups` (list). Password is not set here — authentication for HPC users is via LDAP, Kerberos, or SSH keys, none of which require a local password hash.

#### 5.4 Kernel Cmdline Overrides

`image.json:default_kernel_cmdline` is the base. Operators can add or override arguments at group or node scope. The merge strategy is append-and-override: node-level arguments that match a key already in the base cmdline (`key=value` form) replace the base value; unkeyed arguments (flags) are appended. Example: a GPU node group adds `nvidia-modeset=1`; a specific node overrides `console=tty0` to suppress serial output.

#### 5.5 Secret Injection (Per-Node, Deploy-Time Only)

Secrets are NEVER baked into images. Secrets are delivered per-node at deploy time via the finalize phase. The model:

1. The operator registers a named secret in the clonr server (stored encrypted at rest, scoped to a node or group).
2. At finalize time, `clonr-static` pulls the secret via the authenticated node API and writes it to the target path (e.g., `/etc/clonr/node-token`, `/etc/krb5.keytab`, `/etc/ipa/ca.crt`).
3. The secret is written with operator-specified mode and owner.
4. The secret value is held in `clonr-static` memory during finalize and never written to the image tarball or the image record.

The `content_sha256` in `image.json` covers only the tarball content, not injected secrets. This is correct: secrets are per-node and ephemeral from the image's perspective.

#### 5.6 Service Enable/Disable

Each image record carries a `systemd_units` list:

```json
{
  "units": [
    { "name": "firewalld.service", "state": "disabled" },
    { "name": "slurmd.service", "state": "enabled" },
    { "name": "chronyd.service", "state": "enabled" }
  ]
}
```

At finalize time, `clonr-static` runs `chroot /mnt/target systemctl enable/disable <unit>` for each entry. This is cleaner than baking the symlinks into the image because service state often varies by node role (compute vs. storage vs. login nodes).

#### 5.7 Post-Install Scripts (Escape Hatch)

Each image or node record may carry a list of ordered shell scripts that run inside a chroot at finalize time, after all declarative customizations are applied. Scripts run as root. They receive environment variables: `CLONR_NODE_ID`, `CLONR_IMAGE_ID`, `CLONR_TARGET_ROOT`, `CLONR_FIRMWARE_MODE`.

Scripts are the escape hatch for anything the declarative model cannot express. They are audited (logged, content-hashed in the deploy event record). A failed script aborts the deploy and reports the script's stderr in the deploy failure event.

### 5A. Fstab Ownership Split

**Problem.** The original §3c wrote `/mnt/target/etc/fstab` from scratch using OS-disk UUIDs. Gilfoyle's §7 identified the gap: every HPC node image carries NFS, Lustre, GPFS, and BeeGFS mount entries that clonr's deploy logic does not know about. Overwriting the entire fstab silently drops those entries. The failure is invisible at boot if any network mounts carry the `nofail` option — the node boots, looks healthy, and users discover `/home` or `/scratch` is missing when they run their first job.

**Contract.**

| Owner | Paths | Rule |
|---|---|---|
| Deploy engine (clonr) | `/etc/fstab.d/00-clonr-os.fstab` | Written at deploy time; contains only OS-disk entries (root, boot, swap) keyed by deploy-time UUIDs. On distros that do not support `fstab.d` drop-ins natively, deploy writes `/etc/fstab` as a file containing ONLY OS-disk mounts and nothing else. |
| Image overlay (Gilfoyle/admin) | `/etc/fstab.d/[10-89]-*.fstab` and `/etc/systemd/system/*.mount` | Network filesystem mounts. Placed in the rootfs by the image overlay at build time. Deploy engine extracts these from the tarball and leaves them untouched. |
| RESERVED — deploy engine MUST NOT write | `/etc/fstab.d/9[0-9]-*.fstab` and `/etc/fstab.d/9*-*.fstab` | Namespace reserved for future cluster-level orchestration. Touching these at deploy time is a bug. |
| RESERVED — deploy engine MUST NOT write | `/etc/systemd/system/*.mount` | All `.mount` units come from the image overlay. Deploy engine never creates or modifies them. |

**Rationale.** Sites have tens of NFS/Lustre mounts per node. A single missing mount entry with `nofail` is invisible until users report wrong filesystem state, by which point hundreds of jobs may have written to the wrong path. The only safe contract is strict namespace partitioning: deploy engine owns exactly one file (`00-clonr-os.fstab`) and is constitutionally forbidden from touching anything else in the fstab namespace.

**Distro compatibility.** Rocky 8/9/10 and RHEL 8/9 do not natively assemble `/etc/fstab.d/` into `/etc/fstab` at runtime. For these distros, the deploy engine writes a single `/etc/fstab` consisting of: (1) the OS-disk block it generates and (2) the content of every `/etc/fstab.d/[10-89]-*.fstab` file found in the image overlay, concatenated in lexicographic order. The image's `/etc/fstab.d/` directory is a clonr convention, not a distro mechanism. The deploy engine is the assembler. This assembly must happen after tarball extraction and before bootloader installation.

---

### 5B. DKMS and Out-of-Tree Kernel Modules

**Problem.** The original ADR treated the image as "install packages and you're done." GPU/Infiniband/Lustre clusters run DKMS modules (NVIDIA, MLNX_OFED, Lustre-client) that compile `.ko` files at package-install time against a specific kernel-devel tree. If DKMS does not run at image-build time inside the chroot, the `.ko` files are absent from `/lib/modules/<kver>/extra/` and the sealed image is non-functional on first deploy.

**Build flow.**

1. The image-build pipeline installs the pinned kernel and `kernel-devel` packages as the first package-install step. Kernel version is locked immediately via `dnf versionlock add kernel-<version>` (requires `dnf-plugin-versionlock`). The `exclude=kernel*` line is written to `/etc/dnf/dnf.conf` inside the chroot to prevent any future update from pulling a new kernel.

2. DKMS packages (e.g., `nvidia-dkms`, `kmod-mlnx-ofa_kernel`, `lustre-client-dkms`) are installed. They register with DKMS but do not automatically build inside a plain `chroot` because `/proc`, `/sys`, and `/dev` are not mounted.

3. DKMS build runs inside a `systemd-nspawn` container with full bind mounts:

   ```bash
   systemd-nspawn \
     --directory=/mnt/image-rootfs \
     --bind=/proc \
     --bind=/sys \
     --bind=/dev \
     dkms autoinstall -k <pinned-kernel-version>
   ```

   This step runs after all packages are installed and after any `post_install_scripts` that register DKMS modules. It runs before the image tarball is sealed.

4. **Post-build verification gate.** The image-build pipeline checks that every `.ko` file declared in `image.json:dkms_modules[].expected_ko` exists under `/lib/modules/<kver>/extra/` before marking the image `ready`. A missing `.ko` fails the build with a structured error — the image is set to `build_failed` state and is not made available for deployment. This gate is non-negotiable: a GPU image that passes build validation with no GPU modules is worse than a build failure because it deploys silently broken nodes.

5. The sealed tarball contains the built `.ko` files. Deploy-time does NOT re-run DKMS. The `dracut --regenerate-all` step in §3e picks up the modules from `/lib/modules/<kver>/` as they exist in the extracted tarball.

**Metadata.** `image.json` carries:

```json
"kernel_version": "5.14.0-570.22.1.el10_0.x86_64",
"kernel_pinned": true,
"dkms_modules": [
  { "name": "nvidia", "expected_ko": "extra/nvidia.ko" },
  { "name": "mlnx-ofa_kernel", "expected_ko": "extra/mlx5_core.ko" }
]
```

`kernel_pinned: true` is a signal to any future update tooling that the kernel on this image must not be changed without a full image rebuild. It is also displayed in the UI image detail page as a badge.

**Why sealed `.ko` files, not deploy-time rebuild.** Compute nodes in production HPC environments typically have no internet access and no compiler toolchain installed. Re-running DKMS at deploy time would require shipping `gcc`, `make`, and `kernel-devel` to every target node — hundreds of MB of build toolchain that does not belong on a production compute node. The image-build host is the right place for compilers. The deployed node is not.

---

### 5C. Secret Delivery

**Problem.** The original §5.5 said "per-node secret injection, delivered over the secure token channel" without specifying the storage architecture. Gilfoyle's §12 required the actual model: encryption at rest, per-cluster (not per-node) scoping for shared secrets like `munge.key`, and a declarative `required_secrets` field in `image.json` so the image can express what it needs without the admin having to configure injection separately per node.

**Storage architecture — envelope encryption (v1.0).**

The clonr server stores all secrets encrypted at rest using envelope encryption, following the pattern established in ADR-0002 for BMC credentials:

- A **master key** lives outside the database in `/etc/clonr/secret-master.key` (mode 0400, owned by the `clonr-serverd` OS user). This file is never committed to git and never included in backups that leave the server. It is the operator's responsibility to protect and back up this file independently.
- Each secret value is encrypted with a per-secret **data encryption key (DEK)** using AES-256-GCM. The DEK is wrapped (encrypted) by the master key and stored alongside the ciphertext in the database. This is the standard envelope encryption pattern: `DB stores {wrapped_dek, ciphertext}`; decryption requires the master key from the filesystem.
- The database schema: `secrets(id, name, cluster_id, wrapped_dek BLOB, ciphertext BLOB, created_at, updated_at)`. No plaintext secret value ever touches the DB.

**Scoping.** Secrets are scoped to a cluster, not to individual nodes. `munge.key`, root `authorized_keys`, Kerberos keytab, and SSSD bind password are cluster-wide — the same value delivered to every node in the cluster. Per-node secrets (TLS certificates with FQDN subjects) are deferred to v1.1.

**Image declaration.** The image declares which secrets it requires via `image.json:required_secrets`:

```json
"required_secrets": [
  { "name": "munge.key",            "path": "/etc/munge/munge.key",          "owner": "munge", "mode": "0400" },
  { "name": "root.authorized_keys", "path": "/root/.ssh/authorized_keys",    "owner": "root",  "mode": "0600" },
  { "name": "krb5.keytab",          "path": "/etc/krb5.keytab",              "owner": "root",  "mode": "0600" }
]
```

The `name` field is the key into the `secrets` table scoped to the node's cluster. The admin registers the secret value once per cluster via the clonr UI or CLI (`clonr secret set munge.key --cluster <id> --file /etc/munge/munge.key`). Clonr stores it encrypted. Every node in the cluster that deploys an image declaring `munge.key` in `required_secrets` receives the same decrypted value at finalize time.

**Delivery at finalize time.** After tarball extraction and before unmount, `clonr-static` calls the server's authenticated secrets API using the node-scoped token established in §3g:

```
GET /api/v1/secrets/<name>?node=<node_id>
Authorization: Bearer <node-token>
```

The server verifies the node token, looks up the image's `required_secrets`, decrypts the DEK with the master key, decrypts the secret value, and returns it in the response body over TLS. `clonr-static` writes the value to the declared path inside the chroot with the declared mode and owner, then immediately frees the value from memory. The secret is never written to disk outside the target rootfs path.

If a required secret is declared in `image.json` but has no registered value for the node's cluster, the finalize phase fails with a structured error identifying the missing secret name. This is a hard failure — a node deployed without `munge.key` cannot run Slurm jobs, and a silent deploy that skips the secret is worse than a failed deploy.

**What the `content_sha256` covers.** The tarball hash covers only image content, not injected secrets. Secrets are per-cluster ephemera from the image's perspective; they are not part of the sealed image and do not affect the image's integrity hash.

**Master key rotation.** Out of scope for v1.0. The architecture does not preclude it: rotation requires re-wrapping all stored DEKs under a new master key, which is a standard envelope encryption operation. The `wrapped_dek` column exists precisely to make this possible without re-encrypting all secret values. A rotation procedure and CLI command are planned for v1.1.

---

### 6. Migration Plan

clonr is at v0.1.2 with two working BIOS nodes (VM206, VM207) deployed from the rocky10-seabios-blkext and rocky101 images. There are no production deployments and no external users.

**The existing images are invalidated. There is no back-compat for the patch-based legacy format.** The migration is a clean break.

| Step | Owner | Action |
|---|---|---|
| 1 | Dinesh | Add `schema_version` field to image DB record. Add `image.json` sidecar storage to image distribution layer (ADR-0003). |
| 2 | Dinesh | Rewrite `pkg/images/builder.go` to enforce exclude list validation on tarball ingest. Reject any tarball containing excluded paths. |
| 3 | Dinesh | Rewrite `pkg/deploy/finalize.go` to implement §3 (fstab generation, BLS generation, dracut, firmware-branched bootloader install). Remove all UUID-patching and grub.cfg-sed code. |
| 4 | Dinesh | Implement overlayfs shell session model (§4). The existing `systemd-nspawn` chroot infrastructure is reused; the overlayfs mount and commit/discard handlers are new. |
| 5 | Dinesh | Implement declarative customization merge (§5.1–5.7) in the finalize phase. |
| 6 | Richard/Gilfoyle | Rebuild the rocky10-base image using the ISO builder with the new exclude list. Validate on VM206 (BIOS) and VM207 (BIOS) and at least one UEFI node per ADR-0006's validation strategy. |
| 7 | Dinesh | Update UI: images list shows firmware_hint, kernel_version, content_sha256 (truncated). Image detail page has Packages, Config Files, Users, Services, Kernel Args, Post-Install Scripts, and Shell tabs. Node detail page surfaces the image+layout duality explicitly. |

**Old images are not migrated.** The DB migration adds `schema_version` and `image.json` columns with `NOT NULL` and no default, so any attempt to use an old image record fails fast at validation rather than silently misbehaving.

The version bump is `v0.1.2 → v0.2.0`. The minor version increment signals the breaking image format change per the project's pre-v1.0 versioning convention.

### 7. UI Changes (Summary)

The UI must reflect the content/layout separation explicitly:

- **Images list:** each row shows `name`, `distro`, `kernel_version`, `firmware_hint` (Firmware-agnostic badge, or BIOS-only/UEFI-only), `size`, `content_sha256` (first 12 hex chars with copy button), `created_at`.
- **Image detail:** tabbed — Packages (RPM manifest from `image.json`), Config Files (overlay entries), Users, Services, Kernel Args, Post-Install Scripts, Shell (with overlay model UI: "Open Shell" → session → "Commit Changes" or "Discard").
- **Node detail:** two distinct cards — "Image" (which rootfs content is assigned) and "Layout" (disk topology, firmware mode, partition scheme). These are parallel concerns and must not be visually merged.
- **Deploy event:** shows both timestamps per ADR-0008. Adds a "Bootloader mode" field (`bios` or `uefi`) to the deploy event record for audit purposes.

### 8. Out of Scope

The following are explicitly deferred:

- **Immutable/OSTree-based deployment** — future, v2.x. OSTree is the correct long-term model for immutable HPC nodes but adds significant complexity. The content-only tarball model is compatible with an eventual OSTree migration: the tarball can be converted to an OSTree commit.
- **A/B partition upgrades** — future. Requires a second root partition and a partition-switch bootloader.
- **Full customization UI implementation** — Sprint 3+. This ADR defines the model; the UI implementation is Dinesh's Sprint 3 work.
- **Physical hardware validation** — mandatory for v1.0 per ADR-0006, but not required to merge this ADR. The BIOS path must be validated on at least one physical server before v1.0 tag.
- **Image signing / chain-of-trust** — future. `content_sha256` provides integrity but not authenticity. GPG signing of `image.json` is the likely path; deferred to v0.3.x.

---

## Consequences

**Positive:**

- The entire class of UUID-patching bugs is eliminated by construction. There is nothing to patch at deploy time — the topology-specific state is generated fresh from first principles against the actual target hardware.
- The image is firmware-agnostic. One build pipeline produces one artifact that deploys to BIOS and UEFI nodes. The firmware branch is three lines of Go in `finalize.go`, not a separate code path.
- `content_sha256` is trustworthy again. It covers the sealed tarball, not a tarball-plus-runtime-mutations. The rocky101 desync bug cannot recur.
- The overlayfs shell model gives operators the same exploratory freedom they had before, with an explicit commit/discard gate that prevents silent corruption.
- The declarative customization model (§5) replaces ad-hoc shell scripting in kickstarts with a first-class API that clonr can audit, version, and display.

**Negative:**

- The existing rocky10-seabios-blkext and rocky101 images must be rebuilt. The rebuild is an hour of work, not a migration script.
- The finalize phase gains significant complexity: dracut, firmware-branched grub2-install, fstab generation, BLS entry generation, overlayfs, declarative customization merge. This is justified complexity — it was implicit before, now it is explicit and testable.
- `dracut --regenerate-all` in the finalize phase adds 60–120 seconds to the deploy time on typical hardware. This is acceptable for a provisioning flow (not a hot path), but operators should be informed via the UI progress indicator.
- The overlayfs shell session requires kernel support for overlayfs on the clonr server host. This is available on any Linux kernel 3.18+ and all target distributions. Not a practical constraint.

---

## Open Questions for Dinesh

1. **dracut driver injection for the initramfs:** `image.json:required_kernel_modules` lists the modules the image declares it needs. But the finalize phase runs on the target node, where the actual hardware may require additional modules not declared in the image. The question is whether to run `dracut` with `--no-hostonly` (includes all modules, larger initramfs, always works) or with `--hostonly` (smaller initramfs, but `dracut` must correctly detect the target hardware from inside a chroot, which is unreliable). Recommendation: `--no-hostonly` for v0.2.0, revisit for size-sensitive deployments. Dinesh needs to validate that `dracut --regenerate-all --no-hostonly` inside the PXE initramfs environment works correctly — the initramfs is a minimal environment and `dracut` may not have all its dependencies available.

2. **Overlayfs session commit — tarball diff strategy:** When an operator commits a shell session, the `upperdir` contains the overlay diff (modified and new files only; deletions are represented as whiteout files). To produce the new sealed tarball, Dinesh must either (a) apply the diff to a copy of the original rootfs and re-tar the whole thing, or (b) produce a delta tarball and merge them. Option (a) is simpler and correct but doubles storage temporarily (original + copy during commit). Option (b) is storage-efficient but requires delta merge logic. At current image sizes (1–3 GB), option (a) is fine. The question is whether the server host has sufficient transient disk space — this should be validated against the deployment guide's minimum storage requirements.

3. **Declarative customization API schema and conflict model:** Section 5 defines the merge order (image → group → node) and says "later layers win on conflicts." The hard question for Dinesh is: what constitutes a conflict for config file overlays? If image scope sets `/etc/ntp.conf` and node scope also sets `/etc/ntp.conf`, node wins — straightforward. But what if image scope runs a post-install script that modifies `/etc/ntp.conf`, and then node scope also sets it via the config overlay? The execution order is: tarball extract → config file overlays (all scopes merged) → post-install scripts (image scope) → post-install scripts (group scope) → post-install scripts (node scope). If a script modifies a file that an overlay already wrote, the script wins because scripts run last. This ordering must be made explicit in the operator docs and the UI must display the merge order to avoid operator confusion. Dinesh needs to decide whether to enforce a simpler model (config overlays win over scripts, or vice versa) or accept the ordering-dependent behavior and document it clearly.
