# clustr Install Guide

This guide covers installing `clustr-serverd` on a dedicated provisioning host. Two paths are documented: Docker Compose (primary, fastest to running) and bare-metal / Ansible (secondary, preferred for production HPC environments where DHCP and TFTP must run on the host network namespace).

---

## Contents

1. [Prerequisites](#1-prerequisites)
2. [Network Setup](#2-network-setup)
3. [Path A — Docker Compose](#3-path-a--docker-compose)
4. [Path B — Bare-Metal (Ansible)](#4-path-b--bare-metal-ansible)
5. [Env Var Reference](#5-env-var-reference)
6. [Bootstrap Admin Account](#6-bootstrap-admin-account)
7. [First-Deploy Smoke Test](#7-first-deploy-smoke-test)

---

## 1. Prerequisites

### Host requirements

| Resource | Minimum | Notes |
|---|---|---|
| OS | Ubuntu 22.04 / Rocky Linux 9 | Other RHEL-compatible or Debian-compatible distros work. |
| CPU | 4 vCPU | ISO build pipeline spawns QEMU VMs (2 GB RAM each). More cores = parallel builds. |
| RAM | 8 GB | 2 GB per concurrent QEMU build + management plane overhead. |
| Disk — OS/data | 100 GB (SSD preferred) | Image blobs live at `CLUSTR_IMAGE_DIR`. A single Rocky Linux image is ~3–4 GB. |
| Network interfaces | 2 | One interface for management/admin access; one dedicated provisioning interface on the same L2 segment as the nodes to be imaged. |

Kernel modules required for ISO build pipeline (loaded automatically if installed):

```bash
# Verify KVM is available
ls /dev/kvm || echo "WARN: /dev/kvm not found — ISO builds will be slow (no hardware virtualisation)"

# Loop device module (for rootfs extraction)
modprobe loop
```

### Software dependencies

**Docker Compose path:**

```bash
# Ubuntu
apt install -y docker.io docker-compose-plugin sqlite3 rsync

# Rocky Linux 9
dnf install -y docker docker-compose-plugin sqlite rsync
systemctl enable --now docker
```

**Bare-metal path:**

```bash
# Ubuntu
apt install -y sqlite3 rsync curl qemu-kvm qemu-utils genisoimage dracut \
               isc-dhcp-server tftp-hpa

# Rocky Linux 9
dnf install -y sqlite rsync curl qemu-kvm qemu-img genisoimage dracut \
               dhcp-server tftp-server
```

---

## 2. Network Setup

clustr requires a **dedicated provisioning interface** on the same L2 segment as the nodes you intend to image. This interface runs the built-in DHCP and TFTP server that delivers the PXE boot environment.

### Management IP and operator access

`clustr-serverd` binds to the **provisioning interface only** (`CLUSTR_LISTEN_ADDR=10.99.0.1:8080` by default). This prevents the built-in DHCP server from answering on any other network segment — a security requirement (see [docs/tls-provisioning.md §3](tls-provisioning.md#3-management-interface-access-dual-nic-setup) for background).

To reach the web UI and API from your operator workstation on the management LAN, you need an IP on the management interface. The recommended pattern is to add a **stable IP alias** to the management interface (typically `eth0`) using the `.254` host address of your management subnet, or simply bind Caddy to the host's existing DHCP address if stability is not a concern.

**Why `.254` as a recommended alias?**
- The router is conventionally `.1`.
- DHCP pools typically occupy the middle range (`.100`–`.200` or similar).
- `.254` is the last usable address before the broadcast address (`.255`), so it is almost never in a DHCP pool and almost never conflicts with an existing host.
- It gives Caddy a stable bookmark address that does not change when the host's DHCP lease renews.

`CLUSTR_MGMT_IP` is the variable that controls what Caddy binds to, what your operator browser bookmarks, and what you pin in DNS.

**How `install-dev-vm.sh` handles this (important — read before running):**

The install script does **not** silently modify network state. Its behavior depends on how it is invoked:

| Situation | Script behavior |
|---|---|
| `CLUSTR_MGMT_IP` env var is set | Uses that IP directly — no prompt, no auto-derive. |
| Running interactively (TTY) | Detects the management network, suggests `<network>.254`, and prompts you to accept, override, or skip. The alias is only added after explicit confirmation. |
| Non-interactive and env var not set | Skips the alias and Caddy setup entirely. Prints a warning to set `CLUSTR_MGMT_IP` and re-run. |
| Host already has NM static addresses on eth0 | Leaves the NM connection alone — does not add or remove addresses. Tells you how to make changes manually. |

**Running interactively — what the prompt looks like:**

```
========================================
  Management IP Setup
========================================

clustr-serverd binds only to the provisioning interface (10.99.0.1:8080).
To reach the web UI from your workstation, Caddy needs an IP on the
management interface (eth0) to proxy from.

Detected management network: 192.168.1.0/24
Suggested management IP:     192.168.1.254

The .254 address is recommended — it is rarely in DHCP pools and easy
to remember. Using it adds a stable IP alias on eth0 alongside your
existing DHCP address. Your current DHCP address is unchanged.

Accept 192.168.1.254 as the management IP? [Y/n/custom]:
```

Enter `Y` (or press Enter) to accept the suggested `.254` address, `n` to skip, or type a custom IP.

**Non-interactive override — `CLUSTR_MGMT_IP`:**

Set this environment variable before running the script to bypass the prompt and apply the alias unconditionally:

```bash
# Use .254 of the management subnet:
export CLUSTR_MGMT_IP=192.168.1.254
bash scripts/setup/install-dev-vm.sh

# Or bind Caddy to the host's existing DHCP address (no alias needed):
export CLUSTR_MGMT_IP=192.168.1.151
bash scripts/setup/install-dev-vm.sh
```

**Manually adding the alias — Rocky Linux 9 (NetworkManager):**

If you want to add the `.254` alias yourself without re-running the install script:

```bash
nmcli con mod "$(nmcli -t -f NAME,DEVICE con show | grep ':eth0$' | cut -d: -f1)" \
    +ipv4.addresses 192.168.1.254/24
nmcli con up "$(nmcli -t -f NAME,DEVICE con show | grep ':eth0$' | cut -d: -f1)"

# Verify — both the original DHCP address and .254 should appear:
ip -4 addr show eth0
```

This creates a persistent secondary address. The alias survives reboots and NetworkManager restarts.

**Manually adding the alias — Ubuntu 22.04 (Netplan):**

```yaml
# /etc/netplan/99-clustr-mgmt.yaml
network:
  version: 2
  ethernets:
    eth0:
      dhcp4: true           # keep your existing DHCP address
      addresses:
        - 192.168.1.254/24  # stable alias for clustr management access
```

```bash
netplan apply
ip -4 addr show eth0   # verify both addresses appear
```

**Already-installed systems:**

The install script will not modify an already-configured host's network state. If you ran an earlier version of the script that silently added a `.254` alias and you want to remove it:

```bash
# Remove the NM static address (replace the connection name and IP to match your system):
nmcli con mod "Wired connection 1" -ipv4.addresses "192.168.1.254/24"
nmcli con up "Wired connection 1"
ip -4 addr show eth0   # confirm only your DHCP address remains
```

**DNS pin (recommended for production):**

Pin a short name to the management IP in your internal DNS so operators can reach the server by name:

```
clustr.lab.example.com  →  192.168.1.254
```

Then set your Caddyfile hostname to `clustr.lab.example.com` and let Caddy obtain a certificate automatically.

### Assign a static IP to the provisioning interface

The provisioning interface must have a static IP. The default subnet used by clustr is `10.99.0.0/24`.

**Ubuntu (netplan):**

```yaml
# /etc/netplan/99-clustr-provisioning.yaml
network:
  version: 2
  ethernets:
    eth1:                         # replace with your provisioning interface
      dhcp4: false
      addresses:
        - 10.99.0.1/24
```

```bash
netplan apply
```

**Rocky Linux 9 (NetworkManager):**

```bash
nmcli connection add type ethernet ifname eth1 con-name clustr-prov \
  ipv4.method manual ipv4.addresses 10.99.0.1/24 autoconnect yes
nmcli connection up clustr-prov
```

### Firewall rules

Open the ports the provisioning server requires on the provisioning interface:

```bash
# UDP 67  — DHCP server
# UDP 69  — TFTP server
# TCP 8080 — clustr HTTP API (adjust if CLUSTR_LISTEN_ADDR is different)

# UFW (Ubuntu)
ufw allow in on eth1 to any port 67 proto udp
ufw allow in on eth1 to any port 69 proto udp
ufw allow in on eth1 to any port 8080 proto tcp

# firewalld (Rocky)
firewall-cmd --permanent --add-service=dhcp
firewall-cmd --permanent --add-service=tftp
firewall-cmd --permanent --add-port=8080/tcp
firewall-cmd --reload
```

**Security note:** Do not expose port 8080 to the management LAN or the internet unless you are using a TLS-terminating reverse proxy (Caddy recommended). The provisioning API carries BMC credentials and image blobs. See [docs/tls-provisioning.md](tls-provisioning.md) for the Caddy setup.

**Dual-NIC operators:** if `clustr-serverd` is bound to the provisioning interface only (`CLUSTR_LISTEN_ADDR=10.99.0.1:8080`) and you need operator access from the management LAN, use Caddy on the same host to bridge the two networks — see [docs/tls-provisioning.md §3](tls-provisioning.md#3-management-interface-access-dual-nic-setup). Caddy binds to `CLUSTR_MGMT_IP` (the management interface alias, typically `.254`) on both `:80` and `:8080` so that operators with existing bookmarks or scripts on either port continue to work seamlessly. Do not rebind `CLUSTR_LISTEN_ADDR` to `0.0.0.0` — that re-exposes the DHCP server on the management interface.

---

## 3. Path A — Docker Compose

Docker Compose is the fastest way to get a running instance. It is well-suited for homelabs, test environments, and operators who want an isolated service. For production HPC environments that need DHCP/TFTP on the host network namespace, see [Path B](#4-path-b--bare-metal-ansible).

### 3.1 Create directories

```bash
mkdir -p /var/lib/clustr/{db,images,boot,tftpboot,iso-cache,backups,log-archive}
chmod 700 /var/lib/clustr
```

### 3.2 Create secrets

```bash
# Create the secrets env file. This file must never be committed to git.
mkdir -p /etc/clustr
chmod 700 /etc/clustr

# Session secret — HMAC key for browser session tokens. Rotate this to invalidate
# all active browser sessions (e.g. on suspected compromise).
# WARNING: if you skip this, a new random key is generated on every server start
# and every web UI session is invalidated on each restart. Always set this.
openssl rand -hex 64 | sed 's/^/CLUSTR_SESSION_SECRET=/' > /etc/clustr/secrets.env
chmod 400 /etc/clustr/secrets.env

# Encryption key for BMC and LDAP credentials at rest (AES-256-GCM).
# Server refuses to start without this in non-dev mode.
echo "CLUSTR_SECRET_KEY=$(openssl rand -hex 32)" >> /etc/clustr/secrets.env
```

### 3.3 Create .env

```bash
cat > /etc/clustr/clustr.env <<'EOF'
# clustr-serverd runtime configuration
# See docs/install.md §5 for full env var reference.

CLUSTR_LISTEN_ADDR=10.99.0.1:8080
CLUSTR_IMAGE_DIR=/var/lib/clustr/images
CLUSTR_DB_PATH=/var/lib/clustr/db/clustr.db
CLUSTR_BOOT_DIR=/var/lib/clustr/boot
CLUSTR_TFTP_DIR=/var/lib/clustr/tftpboot
CLUSTR_LOG_ARCHIVE_DIR=/var/lib/clustr/log-archive

# PXE server (enable if nodes PXE boot via this host)
CLUSTR_PXE_ENABLED=true
CLUSTR_PXE_INTERFACE=eth1
CLUSTR_PXE_RANGE=10.99.0.100-10.99.0.200
CLUSTR_PXE_SERVER_IP=10.99.0.1

# Logging
CLUSTR_LOG_LEVEL=info
CLUSTR_LOG_RETENTION=168h
CLUSTR_LOG_MAX_ROWS_PER_NODE=50000
EOF
chmod 600 /etc/clustr/clustr.env
```

### 3.4 docker-compose.yml

```bash
# Using the compose file from the repo:
curl -fsSL https://raw.githubusercontent.com/sqoia-dev/clustr/main/deploy/docker-compose/docker-compose.yml \
  -o /etc/clustr/docker-compose.yml

# Or write it manually:
cat > /etc/clustr/docker-compose.yml <<'EOF'
version: "3.9"
services:
  clustr:
    image: ghcr.io/sqoia-dev/clustr-server:latest
    restart: unless-stopped
    network_mode: host      # Required: DHCP/TFTP must bind on the host network
    env_file:
      - /etc/clustr/clustr.env
      - /etc/clustr/secrets.env
    volumes:
      - /var/lib/clustr:/var/lib/clustr
      - /dev/kvm:/dev/kvm   # Optional: hardware-accelerated ISO builds
    cap_add:
      - NET_BIND_SERVICE    # Bind ports 67 (DHCP) and 69 (TFTP)
      - SYS_ADMIN           # Required for systemd-nspawn chroot sessions
      - SYS_CHROOT
      - MKNOD
    devices:
      - /dev/kvm:/dev/kvm
      - /dev/net/tun:/dev/net/tun
EOF
```

### 3.5 Start

```bash
cd /etc/clustr
docker compose up -d

# Tail startup logs to capture bootstrap output (admin key printed here — see §6)
docker compose logs -f clustr
```

### 3.6 Verify the server is running

```bash
curl -s http://10.99.0.1:8080/api/v1/healthz/ready | python3 -m json.tool
# Expected: { "status": "ok", "checks": { "db": "ok", "boot_dir": "ok", "initramfs": ... } }
# A 503 response means one or more checks failed — the "checks" map identifies which.
# /api/v1/healthz/ready is unauthenticated (no token required).
```

---

## 4. Path B — Bare-Metal (Ansible)

Bare-metal installation is preferred for production HPC environments. Running directly on the host avoids Docker networking constraints and gives DHCP/TFTP full access to the host network stack. This is what Persona A (HPC sysadmin) will use.

The Ansible role is delivered in Sprint 6. Until then, use the manual steps below, which are what the Ansible role automates.

### 4.1 Download binaries

```bash
# Replace <version> with the desired release tag, e.g. v0.9.0
VERSION="$(curl -s https://api.github.com/repos/sqoia-dev/clustr/releases/latest | grep '"tag_name"' | cut -d'"' -f4)"
ARCH="$(uname -m)"  # x86_64 or aarch64

curl -fsSL "https://github.com/sqoia-dev/clustr/releases/download/${VERSION}/clustr-serverd-linux-${ARCH}" \
  -o /usr/local/bin/clustr-serverd
curl -fsSL "https://github.com/sqoia-dev/clustr/releases/download/${VERSION}/clustr-linux-${ARCH}" \
  -o /usr/local/bin/clustr

chmod +x /usr/local/bin/clustr-serverd /usr/local/bin/clustr

# Verify checksums (SHA-256 published in the GitHub Release notes)
curl -fsSL "https://github.com/sqoia-dev/clustr/releases/download/${VERSION}/checksums.txt" \
  | grep "clustr-serverd-linux-${ARCH}" | sha256sum -c -
```

### 4.2 Create data directories

```bash
mkdir -p /var/lib/clustr/{db,images,boot,tftpboot,iso-cache,backups,log-archive,tmp}
chmod 700 /var/lib/clustr
chown root:root /var/lib/clustr
```

### 4.3 Create config and secrets

```bash
mkdir -p /etc/clustr
chmod 700 /etc/clustr

# Encryption key for BMC and LDAP credentials at rest
echo "CLUSTR_SECRET_KEY=$(openssl rand -hex 32)" >> /etc/clustr/secrets.env
# Session HMAC key
echo "CLUSTR_SESSION_SECRET=$(openssl rand -hex 64)" >> /etc/clustr/secrets.env
chmod 400 /etc/clustr/secrets.env
```

### 4.4 Install systemd unit

```bash
# Download from repo
curl -fsSL https://raw.githubusercontent.com/sqoia-dev/clustr/main/deploy/systemd/clustr-serverd.service \
  -o /etc/systemd/system/clustr-serverd.service

# Edit CLUSTR_LISTEN_ADDR and CLUSTR_PXE_INTERFACE to match your provisioning interface
systemctl daemon-reload
systemctl enable --now clustr-serverd
```

**Bare-metal env var note:** For the bare-metal path, non-secret configuration variables (`CLUSTR_LISTEN_ADDR`, `CLUSTR_PXE_INTERFACE`, etc.) go in `Environment=` lines directly inside the systemd unit file — **not** in `/etc/clustr/clustr.env`. The unit's `EnvironmentFile=` stanza only reads `/etc/clustr/secrets.env` (for `CLUSTR_SECRET_KEY` and `CLUSTR_SESSION_SECRET`). The `clustr.env` file referenced in the Docker Compose Quick Start is a Docker Compose convention; it has no effect on a bare-metal systemd install. If you create `/etc/clustr/clustr.env` on a bare-metal install and set variables there, the server will ignore them unless you also add `EnvironmentFile=/etc/clustr/clustr.env` to the unit.

### 4.5 Capture bootstrap output

On first start, the server creates the default admin account and prints a one-time API key. See [§6 — Bootstrap Admin Account](#6-bootstrap-admin-account).

```bash
# Watch startup and capture bootstrap output
journalctl -u clustr-serverd -f --no-pager
```

### 4.6 Install backup and ops scripts

```bash
# Download ops scripts
mkdir -p /opt/clustr/scripts/ops
curl -fsSL https://raw.githubusercontent.com/sqoia-dev/clustr/main/scripts/ops/clustr-backup.sh \
  -o /opt/clustr/scripts/ops/clustr-backup.sh
curl -fsSL https://raw.githubusercontent.com/sqoia-dev/clustr/main/scripts/ops/clustr-backup-verify.sh \
  -o /usr/local/sbin/clustr-backup-verify.sh
chmod +x /opt/clustr/scripts/ops/clustr-backup.sh /usr/local/sbin/clustr-backup-verify.sh

# Install systemd timer units
for unit in clustr-backup.service clustr-backup.timer clustr-backup-verify.service clustr-backup-verify.timer; do
    curl -fsSL "https://raw.githubusercontent.com/sqoia-dev/clustr/main/deploy/systemd/${unit}" \
      -o "/etc/systemd/system/${unit}"
done

systemctl daemon-reload
systemctl enable --now clustr-backup.timer clustr-backup-verify.timer
```

---

## 5. Env Var Reference

All variables are read from the process environment. With Docker Compose, set them in `clustr.env` or `secrets.env`. With systemd, set them in `clustr-serverd.service` or an `EnvironmentFile`.

### Core

| Variable | Default | Required | Description |
|---|---|---|---|
| `CLUSTR_LISTEN_ADDR` | `:8080` | No | `host:port` to bind the HTTP API. Use the provisioning interface IP to avoid exposing the API on the management LAN. |
| `CLUSTR_MGMT_IP` | *(derived: mgmt-interface-ip with last octet replaced by `.254`)* | No | The IP alias on the management interface that Caddy binds to. Operators access the web UI and API at this address. Not read by `clustr-serverd` directly — used by the install script and Caddy config generation to know which IP to bind Caddy's `:80` and `:8080` listeners to. Set this explicitly to skip auto-derivation. Example: `192.168.1.254`. |
| `CLUSTR_DB_PATH` | `/var/lib/clustr/db/clustr.db` | No | Path to the SQLite database file. Parent directory is created automatically on first start. |
| `CLUSTR_IMAGE_DIR` | `/var/lib/clustr/images` | No | Directory for image blobs (OS rootfs tarballs). Created automatically if absent. |
| `CLUSTR_BOOT_DIR` | `/var/lib/clustr/boot` | No | Directory for the PXE kernel and initramfs. |
| `CLUSTR_TFTP_DIR` | `/var/lib/clustr/tftpboot` | No | Directory for TFTP-served files (iPXE binaries). |
| `CLUSTR_LOG_ARCHIVE_DIR` | `/var/lib/clustr/log-archive` | No | Directory for purge summary records. |

### Security (required in production)

| Variable | Default | Required | Description |
|---|---|---|---|
| `CLUSTR_SECRET_KEY` | — | **Yes** | 32-byte hex key used to encrypt BMC and LDAP credentials at rest (AES-256-GCM). Generate with `openssl rand -hex 32`. Server refuses to start if unset outside dev mode. |
| `CLUSTR_SECRET_MASTER_KEY_PATH` | *(unset)* | No | Path to a file containing the master key. When set, the server reads the key from this file instead of `CLUSTR_SECRET_KEY`. If the file does not exist the server falls back to `CLUSTR_SECRET_KEY`. The bare-metal systemd unit template references this variable; omitting the file is safe if `CLUSTR_SECRET_KEY` is set in `secrets.env`. |
| `CLUSTR_SESSION_SECRET` | *(ephemeral)* | **Strongly recommended** | HMAC key for browser session tokens. **If unset, a random key is generated at each startup — every web UI session is invalidated on every server restart.** Generate with `openssl rand -hex 64`. |
| `CLUSTR_SESSION_SECURE` | `0` | No | Set to `1` to mark session cookies as `Secure` (requires TLS). Should be `1` in any deployment with a TLS reverse proxy. |
| `CLUSTR_AUTH_DEV_MODE` | `0` | No | Set to `1` to disable authentication entirely. Only valid on loopback (`127.0.0.1`). Server refuses to start with this flag on a non-loopback address. **Never use in production.** |

### PXE / DHCP / TFTP

| Variable | Default | Required | Description |
|---|---|---|---|
| `CLUSTR_PXE_ENABLED` | `false` | No | Set to `true` to activate the built-in DHCP + TFTP server. Required if nodes PXE boot via this host. |
| `CLUSTR_PXE_INTERFACE` | *(auto-detect)* | No | Network interface the DHCP server binds to. Should be your provisioning interface (e.g. `eth1`). |
| `CLUSTR_PXE_RANGE` | `10.99.0.100-10.99.0.200` | No | DHCP pool for nodes that do not have a static IP configured in clustr. Format: `<start>-<end>`. |
| `CLUSTR_PXE_SERVER_IP` | *(auto-detect)* | No | IP advertised as `next-server` in DHCP offers. Nodes download the initramfs from this address via HTTP. Should match your provisioning interface IP. |
| `CLUSTR_PXE_SUBNET_CIDR` | `24` | No | Prefix length of the provisioning subnet (DHCP Option 1). |

### Logging and retention

| Variable | Default | Description |
|---|---|---|
| `CLUSTR_LOG_LEVEL` | `info` | Log verbosity: `debug`, `info`, `warn`, `error`. |
| `CLUSTR_LOG_RETENTION` | `168h` (7 days) | TTL for node log rows. Go duration string: `168h`, `336h`, `720h`. |
| `CLUSTR_LOG_MAX_ROWS_PER_NODE` | `50000` | Maximum deploy log rows retained per node. Older rows are evicted when this cap is exceeded. |
| `CLUSTR_LOG_ARCHIVE_DIR` | `/var/lib/clustr/log-archive` | Directory where purge summary records are written. |
| `CLUSTR_AUDIT_RETENTION` | `0` (server treats as 90 days) | TTL for audit log rows. Go duration string. |

### Image builds and deploys

| Variable | Default | Description |
|---|---|---|
| `CLUSTR_MAX_CONCURRENT_BUILDS` | *(default in source)* | Maximum number of ISO build VMs running simultaneously. |
| `CLUSTR_REIMAGE_MAX_CONCURRENT` | *(default in source)* | Maximum concurrent node reimages. |
| `CLUSTR_VERIFY_TIMEOUT` | `5m` | How long after a successful deploy the node has to phone home via `/verify-boot`. Minimum 2m, maximum 30m. |
| `CLUSTR_BUILD_AUTO_RESUME` | *(default in source)* | Set to `false` to disable automatic resumption of interrupted ISO builds at startup. |
| `CLUSTR_ALLOW_PRIVATE_URLS` | `false` | Allow image pull from RFC-1918 addresses (useful in air-gapped environments). |

### Paths

| Variable | Default | Description |
|---|---|---|
| `CLUSTR_BIN_PATH` | `/usr/local/bin/clustr` | Absolute path to the `clustr` CLI binary baked into the initramfs. |
| `CLUSTR_CLIENTD_BIN_PATH` | *(auto-detect)* | Path to `clustr-clientd` binary injected into deployed rootfs. Detected automatically when empty. |
| `CLUSTR_ISO_DIR` | *(default in source)* | Directory where downloaded ISO files are cached for the ISO build pipeline. |

### LDAP module directories

| Variable | Default | Description |
|---|---|---|
| `CLUSTR_LDAP_DATA_DIR` | `/var/lib/clustr/ldap` | Root for slapd MDB data and backups. |
| `CLUSTR_LDAP_CONFIG_DIR` | `/etc/clustr/ldap` | slapd `cn=config` tree and TLS certificates. |
| `CLUSTR_LDAP_PKI_DIR` | `/etc/clustr/pki` | CA key and certificate for LDAP TLS. |

### Backup scripts

These are used by `clustr-backup.sh` and `clustr-backup-verify.sh`, not by `clustr-serverd` itself.

| Variable | Default | Description |
|---|---|---|
| `CLUSTR_BACKUP_DIR` | `/var/lib/clustr/backups` | Destination directory for DB backups. |
| `CLUSTR_ISO_CACHE_DIR` | `/var/lib/clustr/iso-cache` | Source directory for ISO rsync backup. |
| `CLUSTR_ISO_BACKUP_DIR` | `/var/lib/clustr/iso-cache-backup` | Destination mirror for ISO cache. |
| `CLUSTR_BACKUP_REMOTE` | *(unset)* | `user@host:/path` for off-site rsync. When unset, backups stay local (same volume as data — risky). |
| `CLUSTR_BACKUP_VERIFY_PORT` | `18080` | Alternate port for the ephemeral verification instance started by `clustr-backup-verify.sh`. Must not conflict with the production port. |
| `CLUSTR_BACKUP_VERIFY_WAIT` | `30` | Seconds to wait for the verification instance to become ready. |

---

## 6. Bootstrap Admin Account

On the first start (when the users table is empty), `clustr-serverd` automatically creates two credentials:

**Default UI account:**

| Field | Value |
|---|---|
| Username | `clustr` |
| Password | `clustr` |
| Role | admin |
| First-login behavior | Password change is **required** — you will be redirected to a change screen immediately after login |

**Bootstrap API key:**
- An admin-scoped API key is generated and printed to stdout **once** at startup.
- This is the only time the raw key is visible. Copy it immediately.
- The key is stored as a SHA-256 hash in the DB. If lost, rotate it using `clustr-serverd apikey create --scope admin`.

> **IMPORTANT — read this before your first login:**
> The password `clustr` is a known default. The server sets `must_change_password = true` on this account.
> When you first log in, the UI will redirect you to a forced password-change screen before allowing access to any
> other page. Your new password must be at least 8 characters and contain one uppercase letter, one lowercase
> letter, and one digit (e.g. `Myclust3r!`). Until you complete this step, the rest of the UI is locked.

### Step 1: Log in to the web UI

Open `http://<your-clustr-mgmt-ip>` (or `http://<your-clustr-mgmt-ip>:8080` — both work if Caddy is
configured per [docs/tls-provisioning.md](tls-provisioning.md)). The canonical example management IP
used throughout this guide is `192.168.1.254`. If you followed the management IP setup in §2, both
ports proxy through Caddy to `clustr-serverd` on the provisioning interface.

Enter:
- **Username:** `clustr`
- **Password:** `clustr`

You will be immediately redirected to a password-change screen. Set a strong password and continue.

### Step 2: Create a personal admin account

Once logged in, do not use the `clustr` account for day-to-day work:

1. Navigate to **Settings > Users**.
2. Click **Create user**, set a strong password, and assign role **Admin**.
3. Log out and log back in with the new personal credentials.
4. Disable or demote the `clustr` bootstrap account.

### Capturing the bootstrap API key

**Docker Compose:**

```bash
# Watch startup, copy the key before the log scrolls
docker compose logs -f clustr 2>&1 | head -60
```

**systemd:**

```bash
journalctl -u clustr-serverd --no-pager | grep -A2 "Bootstrap admin"
```

### Recovery: if you missed the bootstrap API key

The bootstrap key is printed **once** at first startup and never shown again. If you missed it, create a replacement:

```bash
# bare-metal (run as root on the server host)
clustr-serverd apikey create --scope admin --description "replacement-admin"

# Docker Compose
docker exec -it clustr clustr-serverd apikey create --scope admin --description "replacement-admin"
```

The new key is printed to stdout. Copy it immediately — it is also shown only once.

### Recovery: if you forgot or lost the web UI password

The web UI password is stored as a bcrypt hash in the SQLite DB. There is no plaintext recovery. To reset it,
use the admin API key to call the reset-password endpoint:

**Step 1 — Find the user ID:**

```bash
sqlite3 /var/lib/clustr/db/clustr.db "SELECT id, username, role FROM users;"
```

**Step 2 — Reset the password via the API:**

```bash
# Replace USER_ID with the UUID from step 1.
# Replace YOUR_ADMIN_KEY with your admin API key.
# Password must be 8+ chars, with at least one uppercase letter, lowercase letter, and digit.
curl -s -X POST http://10.99.0.1:8080/api/v1/admin/users/USER_ID/reset-password \
  -H "Authorization: Bearer YOUR_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"password": "Newpassword1"}'
# Expected response: {"ok":true}
```

After the reset, `must_change_password` is set to true. Log in with the new password — you will be redirected to
the forced change screen again to set a final password of your choice.

**No admin API key either?** If you have lost both the webui password and the admin API key, generate a new API
key directly on the server host:

```bash
# bare-metal
clustr-serverd apikey create --scope admin --description "emergency-recovery"

# Docker Compose
docker exec -it clustr clustr-serverd apikey create --scope admin --description "emergency-recovery"
```

Then use that key with the password-reset curl command above.

---

## 7. First-Deploy Smoke Test

This procedure verifies a working end-to-end deployment: image created, node registered, node reimaged, node boots and confirms.

### Step 1: Verify the server is healthy

```bash
curl -s http://10.99.0.1:8080/api/v1/healthz/ready | python3 -m json.tool
# All checks must be "ok" or "warn" — a single "fail" means the server cannot serve PXE.
# /api/v1/healthz/ready is unauthenticated — no token required.
```

### Step 2: Build a test image

**Recommended first path — pull a pre-built cloud image (no extra dependencies):**

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/factory/pull \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "smoke-test",
    "version": "1.0",
    "os": "Rocky Linux 9",
    "url": "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2",
    "format": "qcow2"
  }'
```

The server pulls and converts the image in the background. Poll `GET /api/v1/images` until `status` is `ready` (usually 2–5 minutes on a fast link). This path works out of the box on both Docker Compose and bare-metal installs with no additional host packages.

**Alternative path — Build from ISO (requires additional host packages):**

ISO builds require KVM acceleration and host-side build tooling. Before using this path, verify the following are installed on the server host (or available inside the Docker container):

```bash
# Rocky Linux / RHEL
dnf install -y qemu-kvm qemu-img genisoimage xorriso

# Ubuntu / Debian
apt install -y qemu-kvm qemu-utils genisoimage xorriso

# Verify KVM is accessible
ls /dev/kvm || echo "WARN: no KVM — ISO builds will be very slow"
```

Then in the web UI navigate to **Images > Build from ISO**:

1. Provide the URL of a Rocky Linux 9 or Ubuntu 22.04 minimal ISO (or upload a local file).
2. Leave all defaults. Click **Build**.
3. The build takes 8–15 minutes depending on network speed and whether KVM is available.
4. When status is `ready`, note the image ID.

### Step 3: Register a test node

Replace `aa:bb:cc:dd:ee:ff` with the MAC address of your test node's provisioning NIC:

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/nodes \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "hostname": "smoke-test-001",
    "primary_mac": "aa:bb:cc:dd:ee:ff",
    "base_image_id": "<image-id>",
    "interfaces": [
      {
        "mac_address": "aa:bb:cc:dd:ee:ff",
        "name": "eth0",
        "ip_address": "10.99.0.50/24",
        "gateway": "10.99.0.1",
        "dns": ["10.99.0.1"]
      }
    ],
    "ssh_keys": ["ssh-ed25519 AAAA... operator@bastion"]
  }' | python3 -m json.tool
```

### Step 4: Trigger a reimage

```bash
NODE_ID="<node-id from step 3>"

curl -s -X POST http://10.99.0.1:8080/api/v1/nodes/${NODE_ID}/reimage \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"image_id": "<image-id>"}' | python3 -m json.tool
```

### Step 5: PXE boot the node

Power on the test node. Ensure the BIOS/UEFI boot order is set to **disk first, then network** (`scsi0;net0` in Proxmox terms, or equivalent on bare metal). Persistent default is disk-first; clustr's reimage trigger temporarily flips this to PXE-first via the Proxmox API (or IPMI `SetNextBoot`) for the deploy run, then flips back after the node posts its verify-boot. On a blank disk the firmware falls through to the network fallback automatically. The node will:

1. Get a DHCP offer from clustr.
2. Download the iPXE binary via TFTP.
3. Chainload the clustr iPXE menu.
4. Download the initramfs over HTTP.
5. Boot into the initramfs, which runs `clustr deploy`.
6. After rootfs is written and the node reboots, it POSTs to `/verify-boot`.

### Step 6: Verify success

In the web UI, navigate to **Nodes > smoke-test-001**. Status should transition:

```
pending → triggered → in_progress → verify_pending → verified_booted → idle
```

Or watch via the API:

```bash
watch -n 5 "curl -s http://10.99.0.1:8080/api/v1/nodes/${NODE_ID}/reimage/active \
  -H 'Authorization: Bearer <your-api-key>' | python3 -m json.tool"
```

**Pass criterion:** status reaches `verified_booted`. SSH into the node:

```bash
ssh -i ~/.ssh/your-key root@10.99.0.50
```

If the node is reachable and running the expected OS, the smoke test passes.

### Troubleshooting common failures

| Symptom | Likely cause | Fix |
|---|---|---|
| Node never appears in DHCP log | Provisioning NIC not on same L2 as `CLUSTR_PXE_INTERFACE` | Check switch VLAN assignment; verify `CLUSTR_PXE_INTERFACE` is correct |
| iPXE loads but says "No boot filename" | `CLUSTR_BOOT_DIR` empty — initramfs not built | Run an image build; or manually trigger initramfs rebuild via **Images > Rebuild Initramfs** |
| Reimage stuck at `in_progress` | Node can't reach the server HTTP port | Check firewall rules; verify `CLUSTR_SERVER` or `CLUSTR_PXE_SERVER_IP` is reachable from the node subnet |
| `verified_booted` never reached | `clustr-clientd` not injected into rootfs | Check `CLUSTR_CLIENTD_BIN_PATH`; ensure the image was built with the client binary available |
| `/api/v1/healthz/ready` returns `503` on `initramfs` | No initramfs built yet | Normal on a fresh install before the first image build completes |
| TFTP log shows `autoexec.ipxe not found` | Normal iPXE probe behaviour | UEFI iPXE tries `autoexec.ipxe` over TFTP before falling back to the HTTP chain. This file does not exist in clustr and is not required — iPXE proceeds to `/api/v1/boot/script` automatically. No action needed. |
| Deploy log shows `initramfs not found — BLS entry will reference it anyway` | Normal for images built from minimal ISOs | The base image tar does not include a pre-built initramfs. dracut rebuilds it automatically during the first deploy's finalize phase (adds ~30 seconds). This is expected and correct. It is only a problem if the warning persists after a complete deploy cycle. |
| `systemctl is-system-running` shows `degraded` after first boot | `slurmd.service` enabled but slurm not installed in image | The Slurm module attempts to enable `slurmd.service` during finalize. If slurm is not installed in the base image, systemd marks the unit failed and the system shows as degraded. To avoid this: either pre-install slurm in your image before deploying (see [docs/slurm-module.md](slurm-module.md)), or disable the Slurm module (`POST /api/v1/slurm/disable`) before reimaging nodes that do not have slurm installed. |

---

## 8. Registering Nodes

### Discovering the node's MAC address

Each node is identified by the MAC address of its provisioning NIC. To find it:

- **Proxmox:** the MAC is shown in the VM hardware config (Network Device field). Example: `BC:24:11:DA:58:6A`.
- **Bare metal:** boot the node and check the DHCP lease log on the clustr host (`journalctl -u clustr-serverd | grep "DHCP DISCOVER"`), or read it from the node's BIOS/UEFI network settings, or `ip link show` from the OS.
- **IPMI:** `ipmitool lan print 1` shows the BMC MAC, but you want the host NIC MAC — check the host OS or inspect DHCP leases.

### Registering a node via the API

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/nodes \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "hostname": "compute-001",
    "primary_mac": "aa:bb:cc:dd:ee:ff",
    "base_image_id": "<image-id>",
    "interfaces": [
      {
        "mac_address": "aa:bb:cc:dd:ee:ff",
        "name": "eth0",
        "ip_address": "10.99.0.50/24",
        "gateway": "10.99.0.1",
        "dns": ["10.99.0.1"]
      }
    ],
    "ssh_keys": ["ssh-ed25519 AAAA... operator@bastion"]
  }' | python3 -m json.tool
```

**Important:** always populate `ssh_keys`. If `ssh_keys` is empty, the deployed node will have no authorized keys in `/root/.ssh/authorized_keys` and SSH access will be completely unavailable after deployment.

### Configuring power providers

Power providers allow clustr to automatically power-cycle nodes when a reimage is triggered, without requiring the operator to manually press the power button.

**Proxmox (recommended for lab/dev):**

Add a `power_provider` block to the node config:

```json
"power_provider": {
  "type": "proxmox",
  "proxmox": {
    "host": "https://192.168.1.223:8006",
    "token_id": "root@pam!clustr",
    "token_secret": "<proxmox-api-token>",
    "node": "pve",
    "vmid": "201",
    "insecure": false,
    "tls_ca_cert_path": ""
  }
}
```

**IPMI (for bare-metal nodes with BMC):**

```json
"power_provider": {
  "type": "ipmi",
  "ipmi": {
    "host": "10.0.0.101",
    "username": "admin",
    "password": "changeme"
  }
}
```

### Manual power-cycle workflow (no BMC)

If your nodes do not have an IPMI BMC and are not managed by Proxmox, clustr cannot automatically power-cycle them. The workflow in this case is:

1. Register the node and trigger a reimage via `POST /api/v1/nodes/{id}/reimage`.
2. Manually power-cycle the node (physical power button or hypervisor console).
3. The node PXE boots, clustr serves the deploy script, and the reimage proceeds automatically.

The reimage trigger sets the node's `reimage_pending` flag in the DB. clustr's iPXE routing decision checks this flag when the node boots — no timing coordination is required between the trigger and the power cycle.

---

## 9. Reimaging Multiple Nodes

### Bulk reimage via the web UI

From the **Nodes** page, check the boxes next to the nodes you want to reimage. A floating action bar appears at the bottom of the page. Click **Reimage Selected**.

In the confirmation modal:

- Choose a target image (or leave blank to use each node's currently assigned image).
- Optionally enable **Dry run** to simulate the operation without sending reimage requests.
- Click **Reimage N nodes**.

Reimage requests are submitted individually. Progress for each node is visible in the **Nodes** list and the **Deployments** page (`#/deploys`).

### Bulk reimage via the group API

If your nodes are organized into a NodeGroup, you can reimage the entire group with one API call:

```bash
GROUP_ID="<node-group-id>"

curl -s -X POST http://10.99.0.1:8080/api/v1/node-groups/${GROUP_ID}/reimage \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "image_id": "<image-id>",
    "dry_run": false
  }' | python3 -m json.tool
```

This creates a `group_reimage_job` that fans out individual reimage requests with bounded concurrency (default: 5 concurrent reimages, configurable via `CLUSTR_REIMAGE_MAX_CONCURRENT`). The job status is visible in the web UI Deployments page.

**Use case: redeploying an entire cluster**

1. Build or update your base image.
2. Create a NodeGroup containing all compute nodes.
3. Post the group reimage request with the new image ID.
4. Monitor the Deployments page — nodes complete in batches as power cycles and deploys proceed.

---

## 10. Provisioning Human User Accounts

After your first nodes are deployed, your cluster has system daemon accounts (`slurm`, `munge`, `root`) but no human users. Before cluster users can submit Slurm jobs, they must exist on every node with consistent UIDs and GIDs.

clustr provides two built-in mechanisms:

- **sysaccounts module** — Injects local `/etc/passwd` entries at deploy time. Best for lab clusters with a small, stable user list. No external dependencies.
- **LDAP module** — Runs `slapd` on the clustr-serverd host; deployed nodes authenticate via `sssd`. Best for multi-user production clusters where users are added or removed regularly.

See **[docs/user-management.md](user-management.md)** for the full operator guide, including example API calls, a validation procedure, and a Slurm smoke test that runs as a real user.

---

## 8. Slurm bundle management

clustr ships Slurm as a versioned RPM bundle built from upstream source and
signed with the clustr release GPG key.  The bundle is fetched once at
server install time and served locally by `clustr-serverd` at
`/repo/el9-x86_64/`.  Deployed nodes pull Slurm RPMs from the clustr-server
directly — no external network access is required at deploy time.

### Initial bundle install

After placing the `clustr-serverd` binary, install the bundled Slurm RPM
repository.  The binary embeds the correct version and SHA256 — just run:

```bash
clustr-serverd bundle install
```

This fetches `clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz` from
the GitHub Release, verifies its SHA256 and RPM GPG signatures, and unpacks
it to `/var/lib/clustr/repo/el9-x86_64/`.

### Verify the install

```bash
# List installed bundles
clustr-serverd bundle list
# Expected:
# DISTRO-ARCH   SLURM VERSION  CLUSTR RELEASE  INSTALLED AT          SHA256 (short)
# el9-x86_64    24.11.4        1               2026-04-27T...        d5e397e19bb4...

# Confirm the repo is reachable (no auth required)
curl -I http://10.99.0.1:8080/repo/el9-x86_64/repodata/repomd.xml
# Expected: HTTP/1.1 200 OK
```

### Air-gapped install

For servers that cannot reach GitHub at install time, side-load the bundle
tarball from a workstation that has internet access:

```bash
# On a workstation with internet access — download the bundle:
curl -fLO https://github.com/sqoia-dev/clustr/releases/download/slurm-v24.11.4-clustr1/clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz

# Transfer to the air-gapped server (adjust to your transport):
scp clustr-slurm-bundle-*.tar.gz root@<clustr-server>:/tmp/

# On the clustr-server — install from the local file:
clustr-serverd bundle install --from-file /tmp/clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz
```

### Rollback to the previous bundle

Each install keeps the previous bundle in a `.previous-<timestamp>` directory
under `/var/lib/clustr/repo/`.  To roll back:

```bash
clustr-serverd bundle install --rollback
```

The previous bundle is swapped back into place atomically.  Only the most
recent previous bundle is retained.  After rollback, restart the server:

```bash
systemctl restart clustr-serverd
```

### Autodeploy and bundle upgrades

When the `clustr-autodeploy` timer detects a new server commit, it
rebuilds `clustr-serverd` with the updated `builtinSlurmBundleVersion`
embedded via ldflags.  Before restarting the service, it compares the
embedded bundle SHA256 against `/var/lib/clustr/repo/el9-x86_64/.installed-version`.
If they differ, it runs `clustr-serverd bundle install` automatically to
fetch and install the new bundle.

This means: **upgrading the clustr-server binary via autodeploy also upgrades
the Slurm bundle automatically**, with no manual intervention required.

If the bundle install fails (network issue, GitHub temporarily unreachable),
autodeploy logs a warning and continues the restart with the previously
installed bundle.  It retries on the next cycle.  After 3 consecutive failures,
the circuit breaker opens and autodeploy stops retrying until an operator
manually runs `clustr-serverd bundle install` and resets the counter:

```bash
# Manually fix the bundle, then reset the circuit breaker:
clustr-serverd bundle install
echo 0 > /var/lib/clustr/bundle-install-failures
```

See [docs/server-repo.md](server-repo.md) for full bundle management
documentation including the `/repo/*` HTTP surface and supply-chain details.

---

## See Also

- [docs/rbac.md](rbac.md) — Role model, group-scoped operators, user management
- [docs/upgrade.md](upgrade.md) — Upgrade procedure, migration notes, rollback
- [docs/tls-provisioning.md](tls-provisioning.md) — TLS setup with Caddy, initramfs HTTPS configuration
- [docs/slurm-module.md](slurm-module.md) — Slurm module operator guide: enable, configure, first job
- [docs/server-repo.md](server-repo.md) — Bundled Slurm repo: bundle install, rollback, /repo/* HTTP surface
- [docs/user-management.md](user-management.md) — Human user provisioning: sysaccounts, LDAP, smoke test
- [README.md](../README.md) — Quick Start and architecture overview
