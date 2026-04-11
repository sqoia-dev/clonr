# clonr

clonr is a self-hosted node cloning and image management system for HPC clusters. It separates deployable OS images (base images) from per-node identity (hostname, network config, SSH keys), so a single image can be deployed to hundreds of nodes without modification.

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
```

### 2. Register a node configuration

```bash
curl -X POST http://localhost:8080/api/v1/images \
  -H "Authorization: Bearer mytoken" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "rocky9-hpc-base",
    "version": "1.0.0",
    "os": "Rocky Linux 9",
    "arch": "x86_64",
    "format": "filesystem",
    "disk_layout": {
      "partitions": [
        {"label": "esp",  "size_bytes": 536870912,  "filesystem": "vfat", "mountpoint": "/boot/efi", "flags": ["esp"]},
        {"label": "boot", "size_bytes": 1073741824,  "filesystem": "xfs",  "mountpoint": "/boot"},
        {"label": "root", "size_bytes": 0,           "filesystem": "xfs",  "mountpoint": "/"}
      ],
      "bootloader": {"type": "grub2", "target": "x86_64-efi"}
    }
  }'
```

### 3. Pull an image from a URL

```bash
clonr --server http://localhost:8080 --token mytoken \
  image pull \
  --url https://your-image-server.example.com/rocky9-base.tar.gz \
  --name rocky9-hpc-base \
  --version 1.0.0 \
  --os "Rocky Linux 9" \
  --format filesystem
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
    "base_image_id": "<image-id-from-step-3>",
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

`deploy` auto-discovers the node's MAC address, fetches the matching node config from the server, runs preflight checks, downloads and writes the image, applies hostname/network/SSH config, and optionally repairs EFI boot entries.

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

Instruct the server to pull an image blob from a URL. Returns immediately with the image in `building` status.

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
| `--url` | yes | Source URL for the image blob |
| `--name` | yes | Image name |
| `--version` | no | Version string (default: 1.0.0) |
| `--os` | no | OS name |
| `--arch` | no | Target architecture (default: x86_64) |
| `--format` | no | `filesystem` or `block` (default: filesystem) |
| `--notes` | no | Free-text notes |

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

Output includes: hostname, CPUs, memory, disks (lsblk), NICs, DMI/firmware info.

---

### `clonr deploy`

Full deployment flow: discover hardware, fetch node config, preflight, write image, apply config.

```
clonr deploy --image <id> [--disk /dev/nvme0n1] [--fix-efi]
```

| Flag | Description |
|---|---|
| `--image` | Image ID to deploy (required) |
| `--disk` | Target block device (auto-detected from disk layout if omitted) |
| `--mount-root` | Temporary mount point directory (auto-created if omitted) |
| `--fix-efi` | Repair EFI NVRAM boot entries after deployment |

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

## Server Configuration

`clonr-serverd` is configured via environment variables:

| Variable | Default | Description |
|---|---|---|
| `CLONR_LISTEN_ADDR` | `:8080` | Listen address |
| `CLONR_IMAGE_DIR` | `/var/lib/clonr/images` | Image blob storage directory |
| `CLONR_DB_PATH` | `/var/lib/clonr/clonr.db` | SQLite database path |
| `CLONR_AUTH_TOKEN` | _(empty = auth disabled)_ | Bearer token for API auth |
| `CLONR_LOG_LEVEL` | `info` | Log level: debug, info, warn, error |

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

### Package Layout

```
pkg/
  api/        Shared request/response types (REST contract)
  client/     HTTP client for CLI → server
  config/     ServerConfig and ClientConfig (env + flag resolution)
  deploy/     Deployment engines: rsync, block, efiboot, finalize
  hardware/   Hardware discovery: CPU, memory, disks, NICs, DMI
  server/     HTTP server + handlers + middleware
  db/         SQLite database layer + migrations
  chroot/     Chroot session lifecycle (mount/unmount proc/sys/dev)
  image/      Image store interface
```
