# clustr

clustr is a self-hosted node cloning and image management system for HPC clusters. It separates deployable OS images (base images) from per-node identity (hostname, network config, SSH keys), so a single image can be deployed to hundreds of nodes without modification.

The system includes: an image factory (pull cloud images, import from ISO, capture from running nodes), chroot customization sessions, a built-in PXE/DHCP/TFTP server, IPMI/BMC management, InfiniBand device discovery, centralized logging with live streaming, and an embedded web UI.

The system has two binaries: `clustr-serverd` (the management server) and `clustr` (the CLI, which runs both on operator workstations and on target nodes during deployment).

---

## Quick Start

The fastest path to a running clustr instance is Docker Compose. For production HPC environments that need DHCP/TFTP on the host network namespace, or for operators who prefer bare-metal installs, see [docs/install.md](docs/install.md).

### 1. Create directories and secrets

```bash
# Data directories
mkdir -p /var/lib/clustr/{db,images,boot,tftpboot,iso-cache,backups,log-archive,tmp}
chmod 700 /var/lib/clustr

# Config directory
mkdir -p /etc/clustr && chmod 700 /etc/clustr

# Generate secrets (never commit these)
echo "CLUSTR_SECRET_KEY=$(openssl rand -hex 32)"     > /etc/clustr/secrets.env
echo "CLUSTR_SESSION_SECRET=$(openssl rand -hex 64)" >> /etc/clustr/secrets.env
chmod 400 /etc/clustr/secrets.env
```

### 2. Create clustr.env (Docker Compose only)

```bash
# Download the example and edit for your provisioning interface:
curl -fsSL https://raw.githubusercontent.com/sqoia-dev/clustr/main/deploy/docker-compose/.env.example \
  -o /etc/clustr/clustr.env
# Edit CLUSTR_LISTEN_ADDR, CLUSTR_PXE_INTERFACE, CLUSTR_PXE_SERVER_IP
# to match your provisioning network interface.
chmod 600 /etc/clustr/clustr.env
```

**Bare-metal installs (Path B):** do not use `clustr.env`. Configuration variables go in `Environment=` lines inside the systemd unit file. See [docs/install.md §4.4](docs/install.md#44-install-systemd-unit) for details.

### 3. Start the server

```bash
# Download the Compose file and start:
curl -fsSL https://raw.githubusercontent.com/sqoia-dev/clustr/main/deploy/docker-compose/docker-compose.yml \
  -o /etc/clustr/docker-compose.yml

cd /etc/clustr
docker compose up -d

# Capture the one-time bootstrap admin API key printed at first startup:
docker compose logs -f clustr 2>&1 | grep -A2 "Bootstrap admin"
```

### 4. Verify the server is healthy

```bash
# From the provisioning host directly:
curl -s http://10.99.0.1:8080/api/v1/healthz/ready | python3 -m json.tool
# All checks should be "ok"
```

**Accessing the web UI from your workstation:**

`clustr-serverd` binds to the provisioning interface only (`10.99.0.1`). To reach the web UI from your operator workstation on the management LAN, you need a Caddy reverse proxy listening on a management interface address.

When running `install-dev-vm.sh` interactively, the script will **prompt you** to choose a management IP. It detects your management network and suggests the `.254` address as a stable alias (e.g. `192.168.1.254` for a `192.168.1.0/24` network), but you can accept, skip, or enter a custom address. The alias is only added after your explicit confirmation — the script never silently changes network state.

For non-interactive or automated installs, set `CLUSTR_MGMT_IP` before running the script:

```bash
# Use the suggested .254 alias (adds a secondary address alongside your DHCP address):
export CLUSTR_MGMT_IP=192.168.1.254
bash scripts/setup/install-dev-vm.sh

# Or bind Caddy to the host's existing DHCP address (no alias needed):
export CLUSTR_MGMT_IP=192.168.1.151
bash scripts/setup/install-dev-vm.sh
```

Then open `http://<CLUSTR_MGMT_IP>/` in a browser and log in with `clustr` / `clustr`. You will be prompted to change the password immediately. For the full Caddy setup see [docs/tls-provisioning.md §3](docs/tls-provisioning.md#3-management-interface-access-dual-nic-setup).

**Note for existing installs:** if you ran an earlier version of the script that silently added a `.254` alias, remove it with `nmcli con mod "<connection>" -ipv4.addresses "192.168.1.254/24" && nmcli con up "<connection>"`. The install script will not modify an already-configured host's network state.

For a full walk-through — including image builds, node registration, and the first-deploy smoke test — see [docs/install.md](docs/install.md).

### 2. Pull an image

```bash
clustr image pull \
  --url https://your-image-server.example.com/rocky9-base.tar.gz \
  --name rocky9-hpc-base \
  --version 1.0.0 \
  --os "Rocky Linux 9" \
  --format filesystem
```

### 3. Customize the image

```bash
# Drop into an interactive chroot shell for package installs, config changes, etc.
clustr shell <image-id>
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

Boot the target node from a PXE initramfs containing `clustr`, then:

```bash
clustr deploy \
  --server http://clustr.cluster.internal:8080 \
  --token mytoken \
  --image <image-id> \
  --fix-efi
```

`deploy` auto-discovers the node's MAC address, fetches the matching node config from the server, runs preflight checks, verifies image integrity (sha256), downloads and writes the image, applies hostname/network/SSH config, streams logs back to the server in real-time, and optionally repairs EFI boot entries. On failure the disk partition table is automatically restored from a pre-deploy backup.

---

## Quick Start: 2-Node Slurm Cluster

This section walks a new operator from "clustr is installed" to `srun -N2 hostname` printing both node hostnames. Target time: under 30 minutes on a provisioned server with decent internet.

**Prerequisites:** clustr-serverd running with `--pxe`, two bare-metal or VM nodes on the provisioning network, your admin API token. See [docs/install.md](docs/install.md) for server setup.

**Variable conventions used below:**

```
CLUSTR_URL   = http://<your-clustr-server-ip>:8080
TOKEN        = <your-admin-api-token>
ROCKY9_IMAGE = <image-id returned in step 1>
CTRL_NODE_ID = <node-id of your controller node>
WORK_NODE_ID = <node-id of your worker node>
```

---

### Step 1 — Build a Rocky Linux 9 base image

OpenHPC (the recommended Slurm package source) publishes packages for EL9. **OpenHPC EL10 packages do not exist as of 2026-04-26** (`HTTP 404` at the EL10 repo URL). Use Rocky Linux 9 for all Slurm deployments until OpenHPC publishes EL10 support.

```bash
# Kick off a build from the Rocky 9 minimal ISO (BIOS/MBR layout for broad hardware compat).
# Returns 202 immediately; the build runs async.
# Total time: ~20-35 min (ISO is cached on second build — 0 download time).
curl -s -X POST $CLUSTR_URL/api/v1/factory/build-from-iso \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "url":         "https://download.rockylinux.org/pub/rocky/9/isos/x86_64/Rocky-9-latest-x86_64-minimal.iso",
    "name":        "rocky9",
    "version":     "9.5",
    "firmware":    "bios",
    "disk_size_gb": 20,
    "memory_mb":    4096,
    "cpus":         4
  }' | python3 -m json.tool
# Save the "id" field — that is ROCKY9_IMAGE.

# Poll until status is "ready" (check every 30 seconds):
watch -n 30 "curl -s $CLUSTR_URL/api/v1/images/\$ROCKY9_IMAGE \
  -H 'Authorization: Bearer $TOKEN' | python3 -m json.tool | grep status"
```

**Server requirements:** `/usr/libexec/qemu-kvm` (RHEL/Rocky) or `/usr/bin/qemu-system-x86_64` (Ubuntu). The factory uses KVM acceleration; builds without KVM fall back to TCG (software emulation, 3-5x slower). See [Server Requirements](#server-requirements) for the full package list.

**Build once, reuse forever:** The ISO is cached in `/var/lib/clustr/iso-cache/`. Subsequent builds with the same URL skip the download entirely. Once the image is `ready`, it persists across restarts and does not need to be rebuilt.

**Verify repo reachability before continuing** (prevents silent Slurm install failure later):

```bash
curl -I https://repos.openhpc.community/OpenHPC/3/EL_9/repodata/repomd.xml
# Expected: HTTP 200. If you get 404, the URL or EL version is wrong.
```

---

### Step 2 — Register two nodes

Both nodes must PXE-boot into the clustr initramfs for self-registration. Set the Proxmox/IPMI boot order to **disk first, then network** (`scsi0;net0` in Proxmox) and trigger a PXE reimage from the API — the iPXE menu routes PXE-booted nodes into the deploy flow, not into an infinite PXE loop.

```bash
# Register node 1 (controller)
curl -s -X POST $CLUSTR_URL/api/v1/nodes \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "hostname": "slurm-controller",
    "primary_mac": "<controller-mac>",
    "base_image_id": "'$ROCKY9_IMAGE'",
    "interfaces": [{
      "mac_address": "<controller-mac>",
      "name": "eth0",
      "ip_address": "10.99.0.100/24",
      "gateway": "10.99.0.1"
    }],
    "ssh_keys": ["<your-ssh-public-key>"]
  }'
# Save the "id" field — that is CTRL_NODE_ID.

# Register node 2 (worker)
curl -s -X POST $CLUSTR_URL/api/v1/nodes \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "hostname": "slurm-compute",
    "primary_mac": "<compute-mac>",
    "base_image_id": "'$ROCKY9_IMAGE'",
    "interfaces": [{
      "mac_address": "<compute-mac>",
      "name": "eth0",
      "ip_address": "10.99.0.101/24",
      "gateway": "10.99.0.1"
    }],
    "ssh_keys": ["<your-ssh-public-key>"]
  }'
# Save the "id" field — that is WORK_NODE_ID.
```

---

### Step 3 — Enable the Slurm module

```bash
curl -s -X POST $CLUSTR_URL/api/v1/slurm/enable \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "cluster_name":   "my-hpc",
    "slurm_repo_url": "https://repos.openhpc.community/OpenHPC/3/EL_9"
  }'
# Expected: {"status":"ready"}

# Verify the munge key was generated:
curl -s $CLUSTR_URL/api/v1/slurm/status \
  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool
# Look for: "munge_key_present": true
```

**Verify the repo URL is reachable from the server before enabling:**

```bash
curl -I https://repos.openhpc.community/OpenHPC/3/EL_9/repodata/repomd.xml
# Expected: HTTP 200. If 404, check the EL version in the URL.
```

---

### Step 4 — Assign Slurm roles

```bash
# Controller role
curl -s -X PUT $CLUSTR_URL/api/v1/nodes/$CTRL_NODE_ID/slurm/role \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"roles": ["controller"]}'
# Expected: {"status":"ok"}

# Worker role
curl -s -X PUT $CLUSTR_URL/api/v1/nodes/$WORK_NODE_ID/slurm/role \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"roles": ["worker"]}'
# Expected: {"status":"ok"}

# Verify:
curl -s $CLUSTR_URL/api/v1/slurm/nodes \
  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool
```

**Note:** The body field is `roles` (plural array), not `role` (singular string). Sending `{"role":"controller"}` silently sets an empty role list.

---

### Step 5 — Update slurm.conf

The default rendered `slurm.conf` uses the clustr server hostname as `SlurmctldHost`. You must update it to match the hostname registered in step 2, and set `AccountingStorageType=accounting_storage/none` unless you have slurmdbd set up.

```bash
# Write your corrected slurm.conf
cat > /tmp/my-slurm.conf << 'EOF'
ClusterName=my-hpc
SlurmctldHost=slurm-controller

MpiDefault=pmix
ProctrackType=proctrack/cgroup
TaskPlugin=task/cgroup,task/affinity

SlurmctldPidFile=/var/run/slurmctld.pid
SlurmdPidFile=/var/run/slurmd.pid
SlurmdSpoolDir=/var/spool/slurmd
StateSaveLocation=/var/spool/slurmctld

SlurmUser=slurm
AuthType=auth/munge

SchedulerType=sched/backfill
SelectType=select/cons_tres
SelectTypeParameters=CR_Core_Memory

# No slurmdbd for a basic cluster — change to slurmdbd if you add accounting
AccountingStorageType=accounting_storage/none
JobAcctGatherType=jobacct_gather/cgroup

ReturnToService=2
SlurmctldTimeout=120
SlurmdTimeout=300
InactiveLimit=0
MinJobAge=300
MaxJobCount=50000

# List each worker node. CPUs = vcpu count. RealMemory = MB of RAM.
NodeName=slurm-compute CPUs=2 RealMemory=3905 State=UNKNOWN
PartitionName=batch Nodes=slurm-compute Default=YES MaxTime=INFINITE State=UP
EOF

# Upload via the API (body is JSON with a "content" field)
python3 -c "
import json
with open('/tmp/my-slurm.conf') as f:
    content = f.read()
print(json.dumps({'content': content, 'message': 'initial slurm.conf for 2-node cluster'}))
" > /tmp/slurm-payload.json

curl -s -X PUT $CLUSTR_URL/api/v1/slurm/configs/slurm.conf \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @/tmp/slurm-payload.json
# Expected: {"filename":"slurm.conf","version":2}
```

---

### Step 6 — Reimage both nodes

Both nodes must be reimaged after the Slurm module is enabled. The reimage injects the munge key, writes `slurm.conf`, installs Slurm packages from the repo, and enables the appropriate systemd units.

```bash
# Trigger reimage on controller
curl -s -X POST $CLUSTR_URL/api/v1/nodes/$CTRL_NODE_ID/reimage \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"image_id": "'$ROCKY9_IMAGE'"}'

# Trigger reimage on worker
curl -s -X POST $CLUSTR_URL/api/v1/nodes/$WORK_NODE_ID/reimage \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"image_id": "'$ROCKY9_IMAGE'"}'

# Poll for verified_booted on both (takes 5-15 min per node):
watch -n 15 "curl -s $CLUSTR_URL/api/v1/nodes \
  -H 'Authorization: Bearer $TOKEN' | python3 -m json.tool | grep -E '(hostname|deploy_verified)'"
```

---

### Step 7 — Verify Slurm is running

SSH into the controller:

```bash
ssh root@10.99.0.100  # or your controller IP

# Munge must be running and able to authenticate:
systemctl status munge
munge -n | unmunge   # Expected: STATUS: Success (0)

# slurmctld must be active:
systemctl status slurmctld

# Check cluster health:
scontrol ping        # Expected: Slurmctld(primary) at slurm-controller is UP
sinfo                # Expected: batch partition with slurm-compute in idle state
```

---

### Step 8 — Submit the smoke test job

The test below runs as `root`. This is sufficient to verify that Slurm, munge, and networking are working correctly. Root exists on every node by definition, so it bypasses the user provisioning requirement — which is exactly what you want for a first-pass verification before dealing with user accounts.

```bash
# From the controller node (SSH in first):
ssh root@10.99.0.100

# Verify the cluster sees all nodes:
sinfo
# Expected:
# PARTITION AVAIL  TIMELIMIT  NODES  STATE NODELIST
# batch*       up   infinite      1   idle slurm-compute

# Single-task job on the worker:
srun hostname
# Expected output: slurm-compute

# 2-node job (if you have 2 workers, adjust NodeName + PartitionName in slurm.conf):
srun -N2 hostname
# Expected output (one line per node):
# slurm-compute
# slurm-compute2
```

**Expected `sinfo` output (healthy 1-worker cluster):**

```
PARTITION AVAIL  TIMELIMIT  NODES  STATE NODELIST
batch*       up   infinite      1   idle slurm-compute
```

**Next step — provision real users:** The test above runs jobs as root, which is not appropriate for production use. For real workloads you need human user accounts (`alice`, `bob`, etc.) provisioned on every node with consistent UIDs and GIDs. See **[docs/user-management.md](docs/user-management.md)** for the three approaches and a smoke test that submits a job as a real user.

---

### Troubleshooting the smoke test

| Symptom | Check | Fix |
|---|---|---|
| `slurmctld` not found after reimage | Slurm packages not installed | Verify `slurm_repo_url` is correct and reachable. Check that image is Rocky 9 (not Rocky 10 — OpenHPC EL10 does not exist yet). Reimage. |
| `slurmctld` fails with "CLUSTER NAME MISMATCH" | Stale `clustername` file from prior image install | `rm -f /var/spool/slurmctld/clustername && systemctl restart slurmctld`. The clustr finalize phase will clean this automatically in a future release. |
| `munge -n \| unmunge` fails | Key mismatch or munge not running | Reimage both nodes so they get the same munge key from clustr. |
| `sinfo` shows `down` | `slurmd` not reaching controller | Check `SlurmctldHost` in `/etc/slurm/slurm.conf` matches the actual controller hostname. Open port 6817-6818/tcp on any firewall. |
| `srun` hangs | Controller unreachable from worker | `ping slurm-controller` from the worker. Verify both nodes are on the same provisioning network. |
| `srun` fails for non-root users | User not provisioned on all nodes | See [docs/user-management.md](docs/user-management.md). Verify `id <username>` returns the same UID on every node. |

For full Slurm operator docs, see [docs/slurm-module.md](docs/slurm-module.md).
For user provisioning, see [docs/user-management.md](docs/user-management.md).

---

## Full Workflow Example

```bash
# 1. Start server with built-in PXE
clustr-serverd --pxe

# 2. Pull a Rocky 9 cloud image
clustr image pull \
  --url https://dl.rockylinux.org/.../Rocky-9-GenericCloud.latest.x86_64.qcow2 \
  --name rocky9-base \
  --version 1.0

# 3. Customize it via chroot
clustr shell <image-id>
# Inside: dnf install -y slurm munge, configure /etc/ssh/sshd_config, etc.

# 4. Register node configs
curl -X POST http://localhost:8080/api/v1/nodes \
  -H "Authorization: Bearer mytoken" \
  -d '{"hostname":"compute-001","primary_mac":"aa:bb:cc:dd:ee:01","base_image_id":"<id>",...}'

# 5. PXE boot nodes via IPMI (sets next boot to PXE + power cycles)
clustr ipmi pxe --host 10.0.0.101 --user admin --pass admin

# 6. Watch deployment logs in real time
clustr logs --follow --hostname compute-001
```

---

## Server Requirements

### Hardware

| Resource | Minimum | Recommended |
|---|---|---|
| CPU | 2 cores | 4+ cores (2 for the server, 2+ reserved for ISO build VMs) |
| RAM | 8 GB | 16 GB (ISO builds spin up temporary VMs — budget 2–4 GB per concurrent build) |
| Disk | 100 GB | 200 GB+ for the image store (each base image is 1–5 GB) |
| Virtualization | KVM support (`/dev/kvm` accessible) | Nested virt required if running clustr-serverd inside a VM |

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

clustr-serverd needs read/write access to `/dev/kvm` for ISO build VMs.

- **Run as root** (default in the systemd unit) — simplest, no additional setup.
- **Run as a service user** — add the user to the `kvm` group: `usermod -aG kvm clustr`

Verify access:

```bash
ls -la /dev/kvm
# Expected: crw-rw---- 1 root kvm ...
```

---

### Network Dependencies

- **Outbound HTTPS** to distro mirrors for `clustr image pull` and ISO-based builds.
- **Air-gapped environments:** use `clustr image import-iso` with a local file path. No outbound access required.
- **Firewall rules** on the provisioning interface:

| Port | Protocol | Purpose |
|---|---|---|
| 8080 | TCP | HTTP API and web UI |
| 67 | UDP | DHCP (PXE only, `--pxe` flag) |
| 69 | UDP | TFTP (PXE only, `--pxe` flag) |

---

### Required Directories

clustr-serverd creates these on first run. The parent path (`/var/lib/clustr/`) must exist and have adequate free space.

| Path | Config variable | Notes |
|---|---|---|
| `/var/lib/clustr/images` | `CLUSTR_IMAGE_DIR` | Image blob storage — needs 200 GB+ free |
| `/var/lib/clustr/db/clustr.db` | `CLUSTR_DB_PATH` | SQLite database |
| `/var/lib/clustr/boot` | `CLUSTR_BOOT_DIR` | Kernel and initramfs for PXE |
| `/var/lib/clustr/tftpboot` | `CLUSTR_TFTP_DIR` | TFTP root (iPXE binaries) |
| `/var/lib/clustr/iso` | `CLUSTR_ISO_DIR` | ISO import staging area |

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

All subcommands accept `--server` and `--token` flags (or `CLUSTR_SERVER` / `CLUSTR_TOKEN` environment variables).

### Global Flags

| Flag | Env | Default | Description |
|---|---|---|---|
| `--server` | `CLUSTR_SERVER` | `http://localhost:8080` | clustr-serverd base URL |
| `--token` | `CLUSTR_TOKEN` | _(none)_ | API auth token |

---

### `clustr image list`

List all base images on the server.

```
clustr image list
```

Output columns: ID, NAME, VERSION, OS, ARCH, FORMAT, STATUS, SIZE, CREATED

---

### `clustr image details <id>`

Print full image metadata as JSON.

```
clustr image details a1b2c3d4-...
```

---

### `clustr image pull`

Instruct the server to pull an image blob from a URL. Supports qcow2, raw, and tar.gz formats. Returns immediately with the image in `building` status.

```
clustr image pull \
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

### `clustr image import-iso <path>`

Import an OS image directly from a Rocky Linux or RHEL ISO. The server mounts the ISO, extracts the root filesystem, and registers it as a new base image.

```
clustr image import-iso /path/to/Rocky-9.3-x86_64-dvd.iso \
  --name rocky9-from-iso \
  --version 1.0.0
```

---

### `clustr shell <image-id>`

Open an interactive chroot shell into a base image for customization. Mounts `/proc`, `/sys`, and `/dev` inside the chroot, then drops you into a bash session. Changes are committed back to the image on exit.

```
clustr shell a1b2c3d4-...
```

Use this to install packages, configure services, or run any setup that needs to happen before deployment.

---

### `clustr node list`

List all node configurations.

```
clustr node list
```

Output columns: ID, HOSTNAME, FQDN, MAC, IMAGE, GROUPS

---

### `clustr node config [id]`

Print node configuration as JSON. Accepts ID or MAC address.

```
# By ID:
clustr node config fe09bbcd-...

# By MAC:
clustr node config --mac aa:bb:cc:dd:ee:01
```

---

### `clustr hardware`

Discover local hardware and print as JSON. No server connection required.

```
clustr hardware
```

Output includes: hostname, CPUs, memory, disks (lsblk), NICs, DMI/firmware info, and InfiniBand devices (HCAs, port state, GUIDs, link speed).

---

### `clustr deploy`

Full deployment flow: discover hardware, fetch node config, preflight, verify image integrity, write image, apply config, stream logs to server. On failure, the disk partition table is automatically restored from a pre-deploy backup.

```
clustr deploy --image <id> [--disk /dev/nvme0n1] [--fix-efi] [--timeout 30m]
```

| Flag | Default | Description |
|---|---|---|
| `--image` | _(none)_ | Image ID to deploy (required without `--auto`) |
| `--disk` | auto-detect | Target block device (auto-detected from disk layout if omitted) |
| `--mount-root` | auto-create | Temporary mount point directory |
| `--fix-efi` | false | Repair EFI NVRAM boot entries after deployment |
| `--no-rollback` | false | Skip partition table rollback on failure |
| `--skip-verify` | false | Skip sha256 integrity check before writing image |
| `--timeout` | `30m` | Maximum time allowed for the full deployment (also `CLUSTR_DEPLOY_TIMEOUT`) |
| `--auto` | false | Auto mode: register with server, wait for image assignment, then deploy (for PXE-booted nodes) |

#### `--auto` mode

When booted from a PXE initramfs, pass `--auto` to have the node self-register and wait for an admin to assign a base image before proceeding:

```bash
clustr deploy --auto
```

The node discovers its hardware, registers with the server, and polls until an image is assigned. Intended for fully unattended PXE deployments.

#### Rollback

Before writing to disk, `deploy` snapshots the current partition table with `sgdisk --backup`. If the deployment fails at any point, it calls `sgdisk --load-backup` to restore the original layout. Pass `--no-rollback` to disable this behaviour (useful when deploying to a blank disk with no prior partition table).

#### Image integrity verification

Before writing, `deploy` downloads the image's recorded sha256 checksum from the server and verifies it against the local blob. Use `--skip-verify` to bypass this check if the server does not have a checksum on record for the image.

#### Retry on download failure

Blob downloads are retried up to 3 times with exponential backoff on transient network errors.

---

### `clustr logs`

Query historical deployment logs from the server or tail the live stream.

```
clustr logs [flags]
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
clustr logs --mac aa:bb:cc:dd:ee:ff          # history for a specific node
clustr logs --follow                          # live tail all nodes
clustr logs --follow --mac aa:bb:cc:dd:ee:ff --level error
clustr logs --component deploy --since 1h    # last hour of deploy logs
```

All logs are also visible in the web UI log viewer.

---

### `clustr fix-efiboot`

Standalone EFI boot entry repair.

```
clustr fix-efiboot --disk /dev/nvme0n1 --esp 1 --label "Rocky Linux"
```

| Flag | Default | Description |
|---|---|---|
| `--disk` | _(required)_ | Target disk device |
| `--esp` | `1` | ESP partition number |
| `--label` | `Linux` | Boot menu label |
| `--loader` | `\EFI\rocky\grubx64.efi` | EFI loader path relative to ESP |

---

## IPMI / BMC Management

clustr includes built-in IPMI management via `ipmitool`. All `clustr ipmi` subcommands can target the local BMC (no flags needed) or a remote BMC via `--host`, `--user`, `--pass`.

### `clustr ipmi status`

Show local BMC network configuration and user list.

```
clustr ipmi status
```

Prints channel, IP address, netmask, gateway, IP source, and BMC users with access levels.

---

### `clustr ipmi power`

Control node power via IPMI.

```
clustr ipmi power [on|off|cycle|reset|status] --host <bmc-ip> --user <user> --pass <pass>
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

### `clustr ipmi configure`

Configure the local BMC with a static IP address.

```
clustr ipmi configure --ip 10.0.0.200 --netmask 255.255.255.0 --gateway 10.0.0.1
```

| Flag | Required | Description |
|---|---|---|
| `--ip` | yes | Static IP address for the BMC |
| `--netmask` | yes | Subnet mask |
| `--gateway` | yes | Default gateway |

---

### `clustr ipmi pxe`

Set next boot to PXE and power cycle the target node. Use this to remotely kick off a deployment without physically touching the node.

```
clustr ipmi pxe --host 10.0.0.101 --user admin --pass admin
```

| Flag | Required | Description |
|---|---|---|
| `--host` | yes | BMC IP address |
| `--user` | no | BMC username |
| `--pass` | no | BMC password |

---

### `clustr ipmi sensors`

Display IPMI sensor readings (temperatures, voltages, fan speeds).

```
clustr ipmi sensors [--host <bmc-ip> --user <user> --pass <pass>]
```

Reads from local BMC when no `--host` is provided.

---

### `clustr ipmi test-boot-flip-direct`

Validates the boot-device override configuration directly against a real BMC **without power cycling the node**. Run this when setting up a new BMC or debugging IPMI compatibility issues before registering the node on the server.

For nodes already registered on the server, use `clustr ipmi test-boot-flip --node <id>` instead (it uses the server-stored credentials and provider config).

```
clustr ipmi test-boot-flip-direct \
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

clustr uses a two-path strategy for setting the chassis boot device override on real bare-metal hardware:

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
**Fix:** Use `CLUSTR_IPMI_USE_RAW=true` to force the raw command path, which bypasses the BMC's high-level command parser.

**Symptom:** `clustr ipmi test-boot-flip-direct` shows device mismatch in the read-back, but the node actually boots correctly.
**Cause:** Some BMCs (especially Lenovo XCC/IMM2) return stale bootparam data in the same IPMI session as the write. The boot behaviour at POST time is correct.
**Fix:** This is expected; test-boot-flip-direct will note that verify is skipped for Lenovo. If the node boots correctly, ignore the read-back discrepancy.

**Symptom:** HPE node ignores boot override intermittently.
**Cause:** Power cycle was issued within 3 seconds of the boot-flip command. The iLO firmware races the flush to non-volatile storage.
**Fix:** When using clustr's `PowerCycleAfterBoot`, the 3-second delay is applied automatically. If scripting ipmitool directly, add `sleep 3` between the bootdev set and power cycle.

### Environment variable overrides

These environment variables override auto-detection when the heuristics fail:

| Variable | Effect |
|---|---|
| `CLUSTR_IPMI_USE_RAW=true` | Force raw `ipmitool raw 0x00 0x08 ...` command for all BMCs, skipping the friendly path entirely |
| `CLUSTR_IPMI_EFI=true` | Force UEFI boot mode even when not detected or not requested via flags |

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
| `clustr image pull --url ...` | Pull cloud images (qcow2, raw, tar.gz) from any URL |
| `clustr image import-iso <path>` | Import from a Rocky Linux or RHEL ISO |
| `clustr shell <image-id>` | Interactive chroot shell for customization |
| Image capture | Capture a configured running node back into a base image |

Images are stored in `CLUSTR_IMAGE_DIR` and tracked in the SQLite database. The factory runs image scrubbing on captured images to remove node-specific artifacts (machine IDs, SSH host keys, etc.) before registration.

---

## PXE Boot

clustr includes a built-in PXE server (DHCP + TFTP + iPXE chainloading). Enable it with the `--pxe` flag or `CLUSTR_PXE_ENABLED=true`.

```bash
./bin/clustr-serverd --pxe
```

How it works:
1. The built-in DHCP server responds only to PXE clients (no conflict with your existing DHCP server).
2. TFTP serves `ipxe.efi` / `undionly.kpxe` and the iPXE chainload script.
3. PXE-booted nodes load a minimal initramfs containing `clustr`.
4. Nodes run `clustr deploy --auto`, self-register with the server, and wait for image assignment.

Build the initramfs for PXE nodes:

```bash
./scripts/build-initramfs.sh
```

### PXE Configuration

| Variable | Default | Description |
|---|---|---|
| `CLUSTR_PXE_ENABLED` | `false` | Enable built-in PXE server |
| `CLUSTR_PXE_INTERFACE` | auto-detect | Network interface for the DHCP/TFTP server |
| `CLUSTR_PXE_RANGE` | `10.99.0.100-10.99.0.200` | DHCP IP pool for PXE clients |
| `CLUSTR_PXE_SERVER_IP` | auto-detect | Server IP advertised to PXE clients |
| `CLUSTR_BOOT_DIR` | `/var/lib/clustr/boot` | Kernel and initramfs location |
| `CLUSTR_TFTP_DIR` | `/var/lib/clustr/tftpboot` | TFTP root directory (iPXE binaries) |

### E2E Tested Boot Chain

The full PXE boot chain has been end-to-end tested on Proxmox VMs running Rocky Linux 9 across the following configurations:

| Configuration | Status |
|---|---|
| UEFI boot | Tested |
| BIOS / legacy boot | Tested |
| Single-disk deployment | Tested |
| Multi-disk deployment | Tested |
| Multi-NIC nodes | Tested |

Tests covered: DHCP lease, TFTP/iPXE chainload, initramfs boot, `clustr deploy --auto` self-registration, image write, finalization, and reboot into the deployed OS.

---

## Centralized Logging

During deployment, the `clustr` CLI streams structured logs to the server in real-time over HTTP. All phases — hardware discovery, image write, chroot finalization, EFI repair — emit logs with component and level metadata.

Logs are stored in the SQLite database and queryable via CLI or web UI.

```bash
# Query historical logs
clustr logs

# Live tail (SSE stream)
clustr logs --follow

# Filter
clustr logs --mac aa:bb:cc:dd:ee:ff
clustr logs --hostname compute-001
clustr logs --level error
clustr logs --component deploy --since 1h
clustr logs --follow --hostname compute-001 --level warn
```

The web UI log viewer supports the same filters with live SSE streaming.

---

## InfiniBand Discovery

`clustr hardware` discovers InfiniBand HCAs, Intel OPA adapters, and RoCE interfaces via `/sys/class/infiniband/`. Output includes: device name, firmware version, node GUID, sys image GUID, ports with state, physical state, link layer, and link speed.

Supported devices: Mellanox ConnectX series (mlx5), Intel OPA (hfi1), RoCE interfaces.

NodeConfig supports IPoIB interface configuration, which is applied automatically during deployment finalization.

---

## Software RAID

clustr supports hardware discovery and provisioning of Linux software RAID (md) arrays.

**Discovery:** `clustr hardware` parses `/proc/mdstat` and sysfs to report all active md arrays alongside physical disks — including RAID level, component devices, and array state.

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

## Access Control

clustr uses a 3-tier role model: **admin** (full access), **operator** (group-scoped mutations), and **readonly** (view only). Operators are scoped to specific NodeGroups — they can only reimage and power nodes within the groups they are assigned to.

See [docs/rbac.md](docs/rbac.md) for the full permission matrix, group-scoped operator semantics, bootstrap flow, and migration guide.

---

## Security

### SSRF protection

The server validates image pull URLs before fetching. Requests to private RFC 1918 addresses, loopback, link-local, and other non-routable ranges are rejected. Set `CLUSTR_ALLOW_PRIVATE_URLS=true` to allow pulling from internal registries or storage hosts on private networks.

### Request body size limits

Unauthenticated endpoints have explicit body size limits to prevent abuse: 1 MB for node registration, 5 MB for log submissions.

### ISO import path restriction

The server only allows ISO imports from paths under `CLUSTR_ISO_DIR` (default: `/var/lib/clustr/iso`). Paths outside this directory are rejected. Symlinks inside the ISO are extracted with `--copy-unsafe-links` to prevent traversal.

---

## Server Configuration

`clustr-serverd` is configured via environment variables:

| Variable | Default | Description |
|---|---|---|
| `CLUSTR_LISTEN_ADDR` | `:8080` | Listen address |
| `CLUSTR_IMAGE_DIR` | `/var/lib/clustr/images` | Image blob storage directory |
| `CLUSTR_DB_PATH` | `/var/lib/clustr/db/clustr.db` | SQLite database path |
| `CLUSTR_AUTH_TOKEN` | _(empty = auth disabled)_ | Bearer token for API auth |
| `CLUSTR_LOG_LEVEL` | `info` | Log level: debug, info, warn, error |
| `CLUSTR_ISO_DIR` | `/var/lib/clustr/iso` | Allowed directory for ISO imports |
| `CLUSTR_ALLOW_PRIVATE_URLS` | `false` | Allow image pulls from private/loopback IPs |
| `CLUSTR_DEPLOY_TIMEOUT` | `30m` | Default deployment timeout (overridable per-deploy with `--timeout`) |
| `CLUSTR_PXE_ENABLED` | `false` | Enable built-in PXE server |
| `CLUSTR_PXE_INTERFACE` | auto-detect | Network interface for PXE/DHCP/TFTP |
| `CLUSTR_PXE_RANGE` | `10.99.0.100-10.99.0.200` | DHCP IP pool for PXE clients |
| `CLUSTR_PXE_SERVER_IP` | auto-detect | Server IP advertised to PXE clients |
| `CLUSTR_BOOT_DIR` | `/var/lib/clustr/boot` | Kernel + initramfs location |
| `CLUSTR_TFTP_DIR` | `/var/lib/clustr/tftpboot` | TFTP root directory |
| `CLUSTR_FLIP_CONCURRENCY` | `5` | Max concurrent Proxmox boot-order flip operations |

---

## Prometheus Metrics

clustr-serverd exposes a Prometheus-compatible metrics endpoint at `GET /metrics` (no authentication required).

```yaml
# prometheus.yml scrape config
scrape_configs:
  - job_name: clustr
    static_configs:
      - targets: ['<clustr-server-ip>:8080']
```

**Available metrics:**

| Metric | Type | Description |
|---|---|---|
| `clustr_active_deploys` | Gauge | Reimage requests in a non-terminal state |
| `clustr_deploy_total{status}` | Counter | Completed deploys by status (`complete`, `failed`, `canceled`) |
| `clustr_api_requests_total{endpoint,status,method}` | Counter | API requests served, by coarsened endpoint path |
| `clustr_db_size_bytes` | Gauge | SQLite database file size in bytes |
| `clustr_image_disk_bytes` | Gauge | Total image blob storage consumed in bytes |
| `clustr_nodes{state}` | Gauge | Node count by state (`new`, `deployed`, `failed`, `verified_booted`, etc.) |
| `clustr_flip_back_failures` | Counter | Proxmox boot-order reset failures since process start |
| `clustr_webhook_deliveries_total{event,status}` | Counter | Outbound webhook delivery attempts by event and status |

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
- `bin/clustr` — CLI binary (Linux amd64, CGO disabled)
- `bin/clustr-serverd` — Management server

---

## Installation

For a production install — covering Docker Compose (primary), bare-metal / Ansible (secondary), env var reference, bootstrap admin step, and first-deploy smoke test — see **[docs/install.md](docs/install.md)**.

- **Upgrade procedure:** [docs/upgrade.md](docs/upgrade.md) — how migrations work, which env vars invalidate sessions on rotation, rollback procedure
- **TLS setup:** [docs/tls-provisioning.md](docs/tls-provisioning.md) — Caddy TLS termination, initramfs HTTPS configuration, air-gapped guidance
- **RBAC:** [docs/rbac.md](docs/rbac.md) — role model, group-scoped operators, bootstrap admin flow

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

**Symptom:** clustr-serverd fails to start ISO builds with a permission error or "no such file or directory" on `/dev/kvm`.

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

**Symptom:** `clustr image import-iso` or an ISO-based build fails with a message about a missing binary.

**Cause:** `genisoimage` or `xorriso` is not installed, or is not on the `PATH` of the user running clustr-serverd.

**Fix:** Install the missing package (see [Required System Packages](#required-system-packages)) and verify:
```bash
command -v genisoimage && command -v xorriso
```

---

### Deploy fails with "Unexpected EOF in archive"

**Symptom:** `clustr deploy` fails during image extraction with an EOF or truncation error.

**Cause:** The image blob on disk is corrupted or was not fully written (e.g., interrupted pull or disk full condition).

**Fix:**
1. Check available disk space on the server: `df -h /var/lib/clustr/`.
2. Check the image status via `clustr image list` — a failed pull will show a non-`ready` status.
3. Delete and re-pull the image: `clustr image pull --url ...`.
4. If the blob was manually copied, re-verify its checksum against the source.
