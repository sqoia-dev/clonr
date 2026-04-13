# clonr

clonr is a self-hosted node cloning and image management system for HPC clusters. It separates deployable OS images (base images) from per-node identity (hostname, network config, SSH keys), so a single image can be deployed to hundreds of nodes without modification.

The system includes: an image factory (pull cloud images, import from ISO, capture from running nodes), chroot customization sessions, a built-in PXE/DHCP/TFTP server, IPMI/BMC management, InfiniBand device discovery, centralized logging with live streaming, and an embedded web UI.

The system has two binaries: `clonr-serverd` (the management server) and `clonr` (the CLI, which runs both on operator workstations and on target nodes during deployment).

---

## Quick Start

### 1. Start the server

```bash
# Using Docker (recommended):
docker run -d \
  -p 8080:8080 \
  -v /var/lib/clonr:/var/lib/clonr \
  -e CLONR_AUTH_TOKEN=mytoken \
  ghcr.io/sqoia-dev/clonr-server

# Or build and run directly:
make server
CLONR_AUTH_TOKEN=mytoken ./bin/clonr-serverd

# With built-in PXE server enabled:
CLONR_AUTH_TOKEN=mytoken ./bin/clonr-serverd --pxe
```

### 2. Pull an image

```bash
clonr image pull \
  --url https://your-image-server.example.com/rocky9-base.tar.gz \
  --name rocky9-hpc-base \
  --version 1.0.0 \
  --os "Rocky Linux 9" \
  --format filesystem
```

### 3. Customize the image

```bash
# Drop into an interactive chroot shell for package installs, config changes, etc.
clonr shell <image-id>
# Inside chroot: dnf install -y slurm munge, configure sshd, etc.
```

### 4. Register node-specific config

```bash
curl -X POST http://localhost:8080/api/v1/nodes \
  -H "Authorization: Bearer mytoken" \
  -H "Content-Type: application/json" \
  -d '{
    "hostname": "compute-001",
    "fqdn": "compute-001.cluster.example.com",
    "primary_mac": "aa:bb:cc:dd:ee:01",
    "base_image_id": "<image-id>",
    "interfaces": [
      {
        "mac_address": "aa:bb:cc:dd:ee:01",
        "name": "eth0",
        "ip_address": "10.0.1.1/24",
        "gateway": "10.0.1.254",
        "dns": ["10.0.0.1"]
      }
    ],
    "ssh_keys": ["ssh-ed25519 AAAA... admin@bastion"],
    "groups": ["compute", "gpu"]
  }'
```

### 5. Deploy to a node

Boot the target node from a PXE initramfs containing `clonr`, then:

```bash
clonr deploy \
  --server http://clonr.cluster.internal:8080 \
  --token mytoken \
  --image <image-id> \
  --fix-efi
```

`deploy` auto-discovers the node's MAC address, fetches the matching node config from the server, runs preflight checks, verifies image integrity (sha256), downloads and writes the image, applies hostname/network/SSH config, streams logs back to the server in real-time, and optionally repairs EFI boot entries. On failure the disk partition table is automatically restored from a pre-deploy backup.

---

## Full Workflow Example

```bash
# 1. Start server with built-in PXE
clonr-serverd --pxe

# 2. Pull a Rocky 9 cloud image
clonr image pull \
  --url https://dl.rockylinux.org/.../Rocky-9-GenericCloud.latest.x86_64.qcow2 \
  --name rocky9-base \
  --version 1.0

# 3. Customize it via chroot
clonr shell <image-id>
# Inside: dnf install -y slurm munge, configure /etc/ssh/sshd_config, etc.

# 4. Register node configs
curl -X POST http://localhost:8080/api/v1/nodes \
  -H "Authorization: Bearer mytoken" \
  -d '{"hostname":"compute-001","primary_mac":"aa:bb:cc:dd:ee:01","base_image_id":"<id>",...}'

# 5. PXE boot nodes via IPMI (sets next boot to PXE + power cycles)
clonr ipmi pxe --host 10.0.0.101 --user admin --pass admin

# 6. Watch deployment logs in real time
clonr logs --follow --hostname compute-001
```

---

## Server Requirements

### Hardware

| Resource | Minimum | Recommended |
|---|---|---|
| CPU | 2 cores | 4+ cores (2 for the server, 2+ reserved for ISO build VMs) |
| RAM | 8 GB | 16 GB (ISO builds spin up temporary VMs — budget 2–4 GB per concurrent build) |
| Disk | 100 GB | 200 GB+ for the image store (each base image is 1–5 GB) |
| Virtualization | KVM support (`/dev/kvm` accessible) | Nested virt required if running clonr-serverd inside a VM |

**Network:** A dedicated provisioning network interface for PXE is strongly recommended. Separating the management network from the provisioning network avoids DHCP conflicts and makes firewall rules easier to reason about.

---

### Operating System

- Rocky Linux 9 / RHEL 9 / AlmaLinux 9 (primary, most tested)
- Ubuntu 22.04 / Ubuntu 24.04 (also supported)

Requires systemd.

---

### Required System Packages

**Rocky Linux / RHEL / AlmaLinux:**

```bash
sudo dnf install -y \
    qemu-kvm qemu-img \
    genisoimage xorriso \
    rsync tar gzip pigz zstd \
    e2fsprogs xfsprogs dosfstools \
    util-linux parted gdisk \
    kpartx multipath-tools \
    ipmitool \
    edk2-ovmf seabios \
    grub2-tools grub2-tools-extra \
    efibootmgr \
    dracut
```

**Ubuntu / Debian:**

```bash
sudo apt install -y \
    qemu-kvm qemu-utils \
    genisoimage xorriso \
    rsync tar gzip pigz zstd \
    e2fsprogs xfsprogs dosfstools \
    util-linux parted gdisk \
    kpartx multipath-tools \
    ipmitool \
    ovmf seabios \
    grub-efi-amd64 grub-pc \
    efibootmgr \
    dracut
```

---

### Optional Packages

| Package | Purpose |
|---|---|
| `cdrkit` | Alternative ISO tooling — only needed as a fallback if `genisoimage`/`xorriso` are unavailable |
| `libvirt-daemon-driver-qemu` | libvirt integration (planned future feature) |
| `swtpm` | TPM emulation in build VMs, required only if customer nodes need Secure Boot |

---

### KVM Access

clonr-serverd needs read/write access to `/dev/kvm` for ISO build VMs.

- **Run as root** (default in the systemd unit) — simplest, no additional setup.
- **Run as a service user** — add the user to the `kvm` group: `usermod -aG kvm clonr`

Verify access:

```bash
ls -la /dev/kvm
# Expected: crw-rw---- 1 root kvm ...
```

---

### Network Dependencies

- **Outbound HTTPS** to distro mirrors for `clonr image pull` and ISO-based builds.
- **Air-gapped environments:** use `clonr image import-iso` with a local file path. No outbound access required.
- **Firewall rules** on the provisioning interface:

| Port | Protocol | Purpose |
|---|---|---|
| 8080 | TCP | HTTP API and web UI |
| 67 | UDP | DHCP (PXE only, `--pxe` flag) |
| 69 | UDP | TFTP (PXE only, `--pxe` flag) |

---

### Required Directories

clonr-serverd creates these on first run. The parent path (`/var/lib/clonr/`) must exist and have adequate free space.

| Path | Config variable | Notes |
|---|---|---|
| `/var/lib/clonr/images` | `CLONR_IMAGE_DIR` | Image blob storage — needs 200 GB+ free |
| `/var/lib/clonr/clonr.db` | `CLONR_DB_PATH` | SQLite database |
| `/var/lib/clonr/boot` | `CLONR_BOOT_DIR` | Kernel and initramfs for PXE |
| `/var/lib/clonr/tftpboot` | `CLONR_TFTP_DIR` | TFTP root (iPXE binaries) |
| `/var/lib/clonr/iso` | `CLONR_ISO_DIR` | ISO import staging area |

**Filesystem requirements:** the image store must be on XFS or ext4 block storage. tmpfs and NFS are not supported — block storage only.

---

### Verifying Dependencies

Check all required binaries are present before starting the server:

```bash
for bin in qemu-kvm qemu-img genisoimage xorriso rsync tar zstd sgdisk mkfs.xfs mkfs.ext4; do
    command -v $bin >/dev/null && echo "  $bin" || echo "MISSING: $bin"
done
ls -la /dev/kvm
```

---

## Web UI

The server embeds a web UI accessible at `http://server:8080/`. Dark theme. No build step required — static assets are compiled into the binary via Go embed.

Pages:
- **Dashboard** — cluster summary: node count, image count, recent activity
- **Images** — browse and inspect base images, monitor pull/build status
- **Nodes** — view and manage node configurations
- **Logs** — searchable log viewer with live SSE streaming; filter by node, level, or component

---

## CLI Reference

All subcommands accept `--server` and `--token` flags (or `CLONR_SERVER` / `CLONR_TOKEN` environment variables).

### Global Flags

| Flag | Env | Default | Description |
|---|---|---|---|
| `--server` | `CLONR_SERVER` | `http://localhost:8080` | clonr-serverd base URL |
| `--token` | `CLONR_TOKEN` | _(none)_ | API auth token |

---

### `clonr image list`

List all base images on the server.

```
clonr image list
```

Output columns: ID, NAME, VERSION, OS, ARCH, FORMAT, STATUS, SIZE, CREATED

---

### `clonr image details <id>`

Print full image metadata as JSON.

```
clonr image details a1b2c3d4-...
```

---

### `clonr image pull`

Instruct the server to pull an image blob from a URL. Supports qcow2, raw, and tar.gz formats. Returns immediately with the image in `building` status.

```
clonr image pull \
  --url https://example.com/rocky9.tar.gz \
  --name rocky9-hpc-base \
  --version 1.0.0 \
  --os "Rocky Linux 9" \
  --arch x86_64 \
  --format filesystem
```

| Flag | Required | Description |
|---|---|---|
| `--url` | yes | Source URL for the image blob (qcow2, raw, tar.gz) |
| `--name` | yes | Image name |
| `--version` | no | Version string (default: 1.0.0) |
| `--os` | no | OS name |
| `--arch` | no | Target architecture (default: x86_64) |
| `--format` | no | `filesystem` or `block` (default: filesystem) |
| `--notes` | no | Free-text notes |

---

### `clonr image import-iso <path>`

Import an OS image directly from a Rocky Linux or RHEL ISO. The server mounts the ISO, extracts the root filesystem, and registers it as a new base image.

```
clonr image import-iso /path/to/Rocky-9.3-x86_64-dvd.iso \
  --name rocky9-from-iso \
  --version 1.0.0
```

---

### `clonr shell <image-id>`

Open an interactive chroot shell into a base image for customization. Mounts `/proc`, `/sys`, and `/dev` inside the chroot, then drops you into a bash session. Changes are committed back to the image on exit.

```
clonr shell a1b2c3d4-...
```

Use this to install packages, configure services, or run any setup that needs to happen before deployment.

---

### `clonr node list`

List all node configurations.

```
clonr node list
```

Output columns: ID, HOSTNAME, FQDN, MAC, IMAGE, GROUPS

---

### `clonr node config [id]`

Print node configuration as JSON. Accepts ID or MAC address.

```
# By ID:
clonr node config fe09bbcd-...

# By MAC:
clonr node config --mac aa:bb:cc:dd:ee:01
```

---

### `clonr hardware`

Discover local hardware and print as JSON. No server connection required.

```
clonr hardware
```

Output includes: hostname, CPUs, memory, disks (lsblk), NICs, DMI/firmware info, and InfiniBand devices (HCAs, port state, GUIDs, link speed).

---

### `clonr deploy`

Full deployment flow: discover hardware, fetch node config, preflight, verify image integrity, write image, apply config, stream logs to server. On failure, the disk partition table is automatically restored from a pre-deploy backup.

```
clonr deploy --image <id> [--disk /dev/nvme0n1] [--fix-efi] [--timeout 30m]
```

| Flag | Default | Description |
|---|---|---|
| `--image` | _(none)_ | Image ID to deploy (required without `--auto`) |
| `--disk` | auto-detect | Target block device (auto-detected from disk layout if omitted) |
| `--mount-root` | auto-create | Temporary mount point directory |
| `--fix-efi` | false | Repair EFI NVRAM boot entries after deployment |
| `--no-rollback` | false | Skip partition table rollback on failure |
| `--skip-verify` | false | Skip sha256 integrity check before writing image |
| `--timeout` | `30m` | Maximum time allowed for the full deployment (also `CLONR_DEPLOY_TIMEOUT`) |
| `--auto` | false | Auto mode: register with server, wait for image assignment, then deploy (for PXE-booted nodes) |

#### `--auto` mode

When booted from a PXE initramfs, pass `--auto` to have the node self-register and wait for an admin to assign a base image before proceeding:

```bash
clonr deploy --auto
```

The node discovers its hardware, registers with the server, and polls until an image is assigned. Intended for fully unattended PXE deployments.

#### Rollback

Before writing to disk, `deploy` snapshots the current partition table with `sgdisk --backup`. If the deployment fails at any point, it calls `sgdisk --load-backup` to restore the original layout. Pass `--no-rollback` to disable this behaviour (useful when deploying to a blank disk with no prior partition table).

#### Image integrity verification

Before writing, `deploy` downloads the image's recorded sha256 checksum from the server and verifies it against the local blob. Use `--skip-verify` to bypass this check if the server does not have a checksum on record for the image.

#### Retry on download failure

Blob downloads are retried up to 3 times with exponential backoff on transient network errors.

---

### `clonr logs`

Query historical deployment logs from the server or tail the live stream.

```
clonr logs [flags]
```

| Flag | Description |
|---|---|
| `--mac` | Filter by node MAC address |
| `--hostname` | Filter by hostname |
| `--level` | Filter by log level (`debug`, `info`, `warn`, `error`) |
| `--component` | Filter by component (`hardware`, `deploy`, `chroot`, `ipmi`, `efiboot`) |
| `--since` | Show logs since a duration ago (`1h`, `30m`) or RFC3339 timestamp |
| `--limit` | Max number of log entries to return (default: 100) |
| `--follow` | Tail the live log stream via SSE |

Examples:

```bash
clonr logs --mac aa:bb:cc:dd:ee:ff          # history for a specific node
clonr logs --follow                          # live tail all nodes
clonr logs --follow --mac aa:bb:cc:dd:ee:ff --level error
clonr logs --component deploy --since 1h    # last hour of deploy logs
```

All logs are also visible in the web UI log viewer.

---

### `clonr fix-efiboot`

Standalone EFI boot entry repair.

```
clonr fix-efiboot --disk /dev/nvme0n1 --esp 1 --label "Rocky Linux"
```

| Flag | Default | Description |
|---|---|---|
| `--disk` | _(required)_ | Target disk device |
| `--esp` | `1` | ESP partition number |
| `--label` | `Linux` | Boot menu label |
| `--loader` | `\EFI\rocky\grubx64.efi` | EFI loader path relative to ESP |

---

## IPMI / BMC Management

clonr includes built-in IPMI management via `ipmitool`. All `clonr ipmi` subcommands can target the local BMC (no flags needed) or a remote BMC via `--host`, `--user`, `--pass`.

### `clonr ipmi status`

Show local BMC network configuration and user list.

```
clonr ipmi status
```

Prints channel, IP address, netmask, gateway, IP source, and BMC users with access levels.

---

### `clonr ipmi power`

Control node power via IPMI.

```
clonr ipmi power [on|off|cycle|reset|status] --host <bmc-ip> --user <user> --pass <pass>
```

| Action | Description |
|---|---|
| `on` | Power the node on |
| `off` | Power the node off |
| `cycle` | Power cycle (off then on) |
| `reset` | Hard reset |
| `status` | Print current power state |

| Flag | Description |
|---|---|
| `--host` | BMC IP address (required for remote nodes) |
| `--user` | BMC username |
| `--pass` | BMC password |

---

### `clonr ipmi configure`

Configure the local BMC with a static IP address.

```
clonr ipmi configure --ip 10.0.0.200 --netmask 255.255.255.0 --gateway 10.0.0.1
```

| Flag | Required | Description |
|---|---|---|
| `--ip` | yes | Static IP address for the BMC |
| `--netmask` | yes | Subnet mask |
| `--gateway` | yes | Default gateway |

---

### `clonr ipmi pxe`

Set next boot to PXE and power cycle the target node. Use this to remotely kick off a deployment without physically touching the node.

```
clonr ipmi pxe --host 10.0.0.101 --user admin --pass admin
```

| Flag | Required | Description |
|---|---|---|
| `--host` | yes | BMC IP address |
| `--user` | no | BMC username |
| `--pass` | no | BMC password |

---

### `clonr ipmi sensors`

Display IPMI sensor readings (temperatures, voltages, fan speeds).

```
clonr ipmi sensors [--host <bmc-ip> --user <user> --pass <pass>]
```

Reads from local BMC when no `--host` is provided.

---

### `clonr ipmi test-boot-flip-direct`

Validates the boot-device override configuration directly against a real BMC **without power cycling the node**. Run this when setting up a new BMC or debugging IPMI compatibility issues before registering the node on the server.

For nodes already registered on the server, use `clonr ipmi test-boot-flip --node <id>` instead (it uses the server-stored credentials and provider config).

```
clonr ipmi test-boot-flip-direct \
  --host <bmc-ip> --user <user> --pass <pass> \
  --device disk --persistent --efi
```

Steps performed:
1. Detect BMC vendor (`ipmitool mc info`) and print applicable quirks
2. Send the boot override (`SetBootDevWithOpts`)
3. Read back `chassis bootparam get 5` and compare to expected values
4. Print the raw 5-byte parameter data

The node is **not** power cycled. Any mismatch between set and read-back values is printed as a warning, not an error.

| Flag | Default | Description |
|---|---|---|
| `--host` | required | BMC IP address |
| `--user` | | BMC username |
| `--pass` | | BMC password |
| `--device` | `disk` | Boot device: `disk`, `pxe`, `bios`, `cd` |
| `--persistent` | `true` | Persist override across all future power cycles |
| `--efi` | `false` | Request UEFI boot mode |

---

## IPMI Bootdev Compatibility

clonr uses a two-path strategy for setting the chassis boot device override on real bare-metal hardware:

1. **Friendly path** — `ipmitool chassis bootdev <dev> options=persistent[,efiboot]`
2. **Raw fallback** — `ipmitool raw 0x00 0x08 0x05 <flags> <device> 0x00 0x00 0x00`

The raw path is used automatically when the friendly command fails (non-zero exit). For BMC vendors where the friendly command is known to be silently broken (Supermicro X9/X10), the raw path is used immediately without attempting the friendly command first.

### Tested vendors

| Vendor | BMC | Notes |
|---|---|---|
| Dell | iDRAC7+ | Standard IPMI works; persistent mode forced (one-time override unreliable on pre-iDRAC7) |
| Dell | iDRAC5/6 (R6xx) | May silently ignore friendly command; raw fallback applied automatically |
| HPE | iLO4, iLO5 | Friendly path works but requires a 3-second pause before power cycle (applied automatically) |
| Supermicro | X10, X11, X12 | Standard IPMI works |
| Supermicro | X9 | One-time override broken in firmware; raw command + persistent forced automatically |
| Lenovo | XCC (ThinkSystem) | Standard IPMI works; `bootparam get 5` read-back is stale after write (verify skipped) |
| Lenovo | IMM2 (System x) | Same as XCC |
| Generic | Any IPMI 2.0 | Standard friendly path with persistent option |

### Known issues and workarounds

**Symptom:** Node ignores boot override and boots from previous default.
**Cause:** BMC consumed the one-time override bit during a previous reboot, or silently ignored the command.
**Fix:** Use `CLONR_IPMI_USE_RAW=true` to force the raw command path, which bypasses the BMC's high-level command parser.

**Symptom:** `clonr ipmi test-boot-flip-direct` shows device mismatch in the read-back, but the node actually boots correctly.
**Cause:** Some BMCs (especially Lenovo XCC/IMM2) return stale bootparam data in the same IPMI session as the write. The boot behaviour at POST time is correct.
**Fix:** This is expected; test-boot-flip-direct will note that verify is skipped for Lenovo. If the node boots correctly, ignore the read-back discrepancy.

**Symptom:** HPE node ignores boot override intermittently.
**Cause:** Power cycle was issued within 3 seconds of the boot-flip command. The iLO firmware races the flush to non-volatile storage.
**Fix:** When using clonr's `PowerCycleAfterBoot`, the 3-second delay is applied automatically. If scripting ipmitool directly, add `sleep 3` between the bootdev set and power cycle.

### Environment variable overrides

These environment variables override auto-detection when the heuristics fail:

| Variable | Effect |
|---|---|
| `CLONR_IPMI_USE_RAW=true` | Force raw `ipmitool raw 0x00 0x08 ...` command for all BMCs, skipping the friendly path entirely |
| `CLONR_IPMI_EFI=true` | Force UEFI boot mode even when not detected or not requested via flags |

### Raw IPMI command reference

The raw command maps to IPMI spec section 28.12 (Set System Boot Options, parameter 5):

```
# Disk, persistent, UEFI (default for production deploy)
ipmitool raw 0x00 0x08 0x05 0xE0 0x08 0x00 0x00 0x00

# PXE, persistent, UEFI
ipmitool raw 0x00 0x08 0x05 0xE0 0x04 0x00 0x00 0x00

# Disk, persistent, BIOS/legacy
ipmitool raw 0x00 0x08 0x05 0xC0 0x08 0x00 0x00 0x00

# PXE, persistent, BIOS/legacy
ipmitool raw 0x00 0x08 0x05 0xC0 0x04 0x00 0x00 0x00
```

Flag byte bit layout (3rd parameter byte):

| Bit | Mask | Meaning |
|---|---|---|
| 7 | `0x80` | Valid — must be 1 for BMC to honour the setting |
| 6 | `0x40` | Persistent — survive all future power cycles |
| 5 | `0x20` | EFI — request UEFI firmware path |
| 4-0 | — | Reserved, must be 0 |

Device byte values (4th parameter byte):

| Value | Device |
|---|---|
| `0x04` | PXE / Network boot |
| `0x08` | Hard disk (default) |
| `0x14` | CD/DVD |
| `0x18` | BIOS setup utility |

---

## Image Factory

The image factory handles the full image lifecycle: pulling from URLs, importing from ISOs, interactive chroot customization, and capturing images from running nodes.

| Command | Description |
|---|---|
| `clonr image pull --url ...` | Pull cloud images (qcow2, raw, tar.gz) from any URL |
| `clonr image import-iso <path>` | Import from a Rocky Linux or RHEL ISO |
| `clonr shell <image-id>` | Interactive chroot shell for customization |
| Image capture | Capture a configured running node back into a base image |

Images are stored in `CLONR_IMAGE_DIR` and tracked in the SQLite database. The factory runs image scrubbing on captured images to remove node-specific artifacts (machine IDs, SSH host keys, etc.) before registration.

---

## PXE Boot

clonr includes a built-in PXE server (DHCP + TFTP + iPXE chainloading). Enable it with the `--pxe` flag or `CLONR_PXE_ENABLED=true`.

```bash
./bin/clonr-serverd --pxe
```

How it works:
1. The built-in DHCP server responds only to PXE clients (no conflict with your existing DHCP server).
2. TFTP serves `ipxe.efi` / `undionly.kpxe` and the iPXE chainload script.
3. PXE-booted nodes load a minimal initramfs containing `clonr`.
4. Nodes run `clonr deploy --auto`, self-register with the server, and wait for image assignment.

Build the initramfs for PXE nodes:

```bash
./scripts/build-initramfs.sh
```

### PXE Configuration

| Variable | Default | Description |
|---|---|---|
| `CLONR_PXE_ENABLED` | `false` | Enable built-in PXE server |
| `CLONR_PXE_INTERFACE` | auto-detect | Network interface for the DHCP/TFTP server |
| `CLONR_PXE_RANGE` | `10.99.0.100-10.99.0.200` | DHCP IP pool for PXE clients |
| `CLONR_PXE_SERVER_IP` | auto-detect | Server IP advertised to PXE clients |
| `CLONR_BOOT_DIR` | `/var/lib/clonr/boot` | Kernel and initramfs location |
| `CLONR_TFTP_DIR` | `/var/lib/clonr/tftpboot` | TFTP root directory (iPXE binaries) |

### E2E Tested Boot Chain

The full PXE boot chain has been end-to-end tested on Proxmox VMs running Rocky Linux 9 across the following configurations:

| Configuration | Status |
|---|---|
| UEFI boot | Tested |
| BIOS / legacy boot | Tested |
| Single-disk deployment | Tested |
| Multi-disk deployment | Tested |
| Multi-NIC nodes | Tested |

Tests covered: DHCP lease, TFTP/iPXE chainload, initramfs boot, `clonr deploy --auto` self-registration, image write, finalization, and reboot into the deployed OS.

---

## Centralized Logging

During deployment, the `clonr` CLI streams structured logs to the server in real-time over HTTP. All phases — hardware discovery, image write, chroot finalization, EFI repair — emit logs with component and level metadata.

Logs are stored in the SQLite database and queryable via CLI or web UI.

```bash
# Query historical logs
clonr logs

# Live tail (SSE stream)
clonr logs --follow

# Filter
clonr logs --mac aa:bb:cc:dd:ee:ff
clonr logs --hostname compute-001
clonr logs --level error
clonr logs --component deploy --since 1h
clonr logs --follow --hostname compute-001 --level warn
```

The web UI log viewer supports the same filters with live SSE streaming.

---

## InfiniBand Discovery

`clonr hardware` discovers InfiniBand HCAs, Intel OPA adapters, and RoCE interfaces via `/sys/class/infiniband/`. Output includes: device name, firmware version, node GUID, sys image GUID, ports with state, physical state, link layer, and link speed.

Supported devices: Mellanox ConnectX series (mlx5), Intel OPA (hfi1), RoCE interfaces.

NodeConfig supports IPoIB interface configuration, which is applied automatically during deployment finalization.

---

## Software RAID

clonr supports hardware discovery and provisioning of Linux software RAID (md) arrays.

**Discovery:** `clonr hardware` parses `/proc/mdstat` and sysfs to report all active md arrays alongside physical disks — including RAID level, component devices, and array state.

**Provisioning:** A `RAIDSpec` field in `DiskLayout` lets you declare arrays as part of a node's disk config. During deployment, `deploy` runs `mdadm --create` to assemble the specified arrays before the filesystem is written. After deployment, `finalize` generates `/etc/mdadm.conf` so the array is persistent across reboots.

Example `RAIDSpec` in a node config:

```json
"raid_arrays": [
  {
    "device": "/dev/md0",
    "level": 1,
    "members": ["/dev/sda", "/dev/sdb"]
  }
]
```

---

## Security

### SSRF protection

The server validates image pull URLs before fetching. Requests to private RFC 1918 addresses, loopback, link-local, and other non-routable ranges are rejected. Set `CLONR_ALLOW_PRIVATE_URLS=true` to allow pulling from internal registries or storage hosts on private networks.

### Request body size limits

Unauthenticated endpoints have explicit body size limits to prevent abuse: 1 MB for node registration, 5 MB for log submissions.

### ISO import path restriction

The server only allows ISO imports from paths under `CLONR_ISO_DIR` (default: `/var/lib/clonr/iso`). Paths outside this directory are rejected. Symlinks inside the ISO are extracted with `--copy-unsafe-links` to prevent traversal.

---

## Server Configuration

`clonr-serverd` is configured via environment variables:

| Variable | Default | Description |
|---|---|---|
| `CLONR_LISTEN_ADDR` | `:8080` | Listen address |
| `CLONR_IMAGE_DIR` | `/var/lib/clonr/images` | Image blob storage directory |
| `CLONR_DB_PATH` | `/var/lib/clonr/clonr.db` | SQLite database path |
| `CLONR_AUTH_TOKEN` | _(empty = auth disabled)_ | Bearer token for API auth |
| `CLONR_LOG_LEVEL` | `info` | Log level: debug, info, warn, error |
| `CLONR_ISO_DIR` | `/var/lib/clonr/iso` | Allowed directory for ISO imports |
| `CLONR_ALLOW_PRIVATE_URLS` | `false` | Allow image pulls from private/loopback IPs |
| `CLONR_DEPLOY_TIMEOUT` | `30m` | Default deployment timeout (overridable per-deploy with `--timeout`) |
| `CLONR_PXE_ENABLED` | `false` | Enable built-in PXE server |
| `CLONR_PXE_INTERFACE` | auto-detect | Network interface for PXE/DHCP/TFTP |
| `CLONR_PXE_RANGE` | `10.99.0.100-10.99.0.200` | DHCP IP pool for PXE clients |
| `CLONR_PXE_SERVER_IP` | auto-detect | Server IP advertised to PXE clients |
| `CLONR_BOOT_DIR` | `/var/lib/clonr/boot` | Kernel + initramfs location |
| `CLONR_TFTP_DIR` | `/var/lib/clonr/tftpboot` | TFTP root directory |

---

## Build Instructions

Requires Go 1.25+. Use `GOTOOLCHAIN=auto` if your local toolchain is older.

```bash
# Build both binaries:
make all

# CLI only (static, CGO_ENABLED=0 — suitable for PXE initramfs):
make client

# Server only:
make server

# Fully static CLI for embedding in initramfs (forces rebuild of all deps):
make static

# Run tests:
make test

# Or with verbose output:
GOTOOLCHAIN=auto go test ./... -v
```

Binaries land in `bin/`:
- `bin/clonr` — CLI binary (Linux amd64, CGO disabled)
- `bin/clonr-serverd` — Management server

---

## Architecture Overview

See [docs/architecture.md](docs/architecture.md) for the full design doc.

Key decisions:

- **BaseImage vs NodeConfig split** — One image blob serves N nodes. Per-node identity (hostname, IPs, SSH keys) is never baked into blobs. Applied at deploy time only.
- **Pure-Go SQLite** (`modernc.org/sqlite`) — Keeps both binaries buildable with `CGO_ENABLED=0`. Required for static initramfs embedding.
- **chi router** — Composes cleanly with standard `net/http` middleware.
- **No auth system at v1** — Single pre-shared API token. HPC clusters are typically air-gapped and operator-administered.
- **Deployment engines** — Two backends: `FilesystemDeployer` (tar archive extraction with sgdisk + mkfs) and `BlockDeployer` (raw block image streamed directly to disk via dd, no temp file required).
- **Embedded web UI** — Static assets compiled into the server binary via Go embed. No separate build step or asset server needed.
- **Centralized log broker** — In-process log broker fans out SSE streams to connected CLI and web UI clients. Logs persisted to SQLite for historical queries.

### Package Layout

```
pkg/
  api/        Shared request/response types (REST contract)
  client/     HTTP client for CLI → server
  config/     ServerConfig and ClientConfig (env + flag resolution)
  deploy/     Deployment engines: rsync, block, efiboot, finalize
  hardware/   Hardware discovery: CPU, memory, disks, NICs, DMI, InfiniBand
  server/     HTTP server + handlers + middleware
  server/ui/  Embedded web UI (Go embed, dark theme, no build step)
  db/         SQLite database layer + migrations
  chroot/     Chroot session lifecycle (mount/unmount proc/sys/dev)
  image/      Image factory (pull, import ISO, capture, shell sessions, scrubbing)
  ipmi/       IPMI/BMC management via ipmitool
  pxe/        Built-in DHCP/TFTP/PXE server with iPXE chainloading
```

---

## Troubleshooting

### KVM not available

**Symptom:** clonr-serverd fails to start ISO builds with a permission error or "no such file or directory" on `/dev/kvm`.

**Check:**
```bash
ls -la /dev/kvm
```

If the device does not exist, KVM is not available on the host. Verify:
- CPU virtualization extensions are enabled in BIOS/UEFI (`vmx` for Intel, `svm` for AMD).
- The `kvm` and `kvm_intel`/`kvm_amd` kernel modules are loaded: `lsmod | grep kvm`.
- If running inside a VM, nested virtualization must be enabled on the hypervisor.

If the device exists but the process lacks permission, add the service user to the `kvm` group or run as root.

---

### ISO build fails with "no such file or directory"

**Symptom:** `clonr image import-iso` or an ISO-based build fails with a message about a missing binary.

**Cause:** `genisoimage` or `xorriso` is not installed, or is not on the `PATH` of the user running clonr-serverd.

**Fix:** Install the missing package (see [Required System Packages](#required-system-packages)) and verify:
```bash
command -v genisoimage && command -v xorriso
```

---

### Deploy fails with "Unexpected EOF in archive"

**Symptom:** `clonr deploy` fails during image extraction with an EOF or truncation error.

**Cause:** The image blob on disk is corrupted or was not fully written (e.g., interrupted pull or disk full condition).

**Fix:**
1. Check available disk space on the server: `df -h /var/lib/clonr/`.
2. Check the image status via `clonr image list` — a failed pull will show a non-`ready` status.
3. Delete and re-pull the image: `clonr image pull --url ...`.
4. If the blob was manually copied, re-verify its checksum against the source.
