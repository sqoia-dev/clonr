#!/bin/bash
# install-dev-vm.sh — Bootstrap a fresh Rocky Linux 9 VM into a clustr dev host.
#
# Idempotent: safe to run multiple times on the same host. Each step checks
# whether work is already done before doing it.
#
# Prerequisites:
#   - Rocky Linux 9 (minimal install), or Ubuntu/Debian
#   - Root shell (run as root or via sudo)
#   - Internet access to dl.google.com and github.com
#   - Two block devices:
#       /dev/sdb (or first disk) — OS disk (32 GB+)
#       /dev/sda (or second disk) — data disk (100 GB+), must be XFS with
#         LABEL=clustr-data, or will be formatted by this script
#   - eth0: LAN/internet uplink (DHCP)
#   - eth1: provisioning network (static 10.99.0.1/24)
#
# This script configures the host as a NAT gateway so that nodes on the
# provisioning network (10.99.0.0/24) can reach the internet via eth0.
# IP forwarding, masquerade (firewalld external zone or iptables), and a
# DNS forwarder (dnsmasq on eth1) are all set up automatically.
#
# Usage:
#   bash scripts/setup/install-dev-vm.sh
#
# Environment overrides:
#   CLUSTR_REPO_URL   — Git remote to clone from (default: https://github.com/sqoia-dev/clustr.git)
#   CLUSTR_REPO_DIR   — Local clone destination (default: /opt/clustr)
#   CLUSTR_DATA_LABEL — XFS volume label for the data disk (default: clustr-data)
#   CLUSTR_DATA_MOUNT — Mount point for data disk (default: /var/lib/clustr)
#   GO_VERSION        — Go toolchain version to install (default: go1.24.2)
#   CLUSTR_MGMT_IP    — Stable IP alias for the management interface (default: derive
#                       from eth0 primary IP by replacing the last octet with .254).
#                       Caddy binds to this IP so operators can reach the web UI from
#                       the management LAN without touching CLUSTR_LISTEN_ADDR.
#                       Example: CLUSTR_MGMT_IP=192.168.1.254

set -euo pipefail

# ---------------------------------------------------------------------------
# OS detection (set early — used throughout the script)
# ---------------------------------------------------------------------------
detect_os() {
    if [[ -f /etc/os-release ]]; then
        # shellcheck source=/dev/null
        . /etc/os-release
        OS_ID="${ID}"
        OS_VERSION="${VERSION_ID:-0}"
    else
        OS_ID="unknown"
        OS_VERSION="0"
    fi
}

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
CLUSTR_REPO_URL="${CLUSTR_REPO_URL:-https://github.com/sqoia-dev/clustr.git}"
CLUSTR_REPO_DIR="${CLUSTR_REPO_DIR:-/opt/clustr}"
CLUSTR_DATA_LABEL="${CLUSTR_DATA_LABEL:-clustr-data}"
CLUSTR_DATA_MOUNT="${CLUSTR_DATA_MOUNT:-/var/lib/clustr}"
GO_VERSION="${GO_VERSION:-go1.24.2}"
GO_TARBALL="${GO_VERSION}.linux-amd64.tar.gz"
GO_URL="https://dl.google.com/go/${GO_TARBALL}"
GO_SHA256_URL="https://dl.google.com/go/${GO_TARBALL}.sha256"

# CLUSTR_MGMT_IP: stable IP alias on the management interface (eth0).
# If not set explicitly, derive it by taking eth0's first IPv4 address and
# replacing the last octet with 254 (the conventional .254 alias pattern).
# This IP is what Caddy binds to — operators reach the web UI at this address.
derive_mgmt_ip() {
    local eth0_ip
    eth0_ip="$(ip -4 addr show eth0 2>/dev/null | awk '/inet / {print $2; exit}' | cut -d/ -f1)"
    if [[ -z "${eth0_ip}" ]]; then
        echo ""
        return
    fi
    # Replace last octet with 254
    echo "${eth0_ip%.*}.254"
}

if [[ -z "${CLUSTR_MGMT_IP:-}" ]]; then
    CLUSTR_MGMT_IP="$(derive_mgmt_ip)"
    if [[ -n "${CLUSTR_MGMT_IP}" ]]; then
        log "CLUSTR_MGMT_IP not set — derived from eth0: ${CLUSTR_MGMT_IP}"
    else
        warn "Could not derive CLUSTR_MGMT_IP from eth0 — Caddy config step will be skipped"
    fi
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log()  { echo "[setup] $*"; }
info() { echo "[setup] >>> $*"; }
warn() { echo "[setup] WARNING: $*" >&2; }
die()  { echo "[setup] FATAL: $*" >&2; exit 1; }

require_root() {
    [[ "${EUID}" -eq 0 ]] || die "must run as root"
}

step_done() {
    # Print a green "done" marker for terminal clarity
    echo "[setup] OK: $*"
}

# ---------------------------------------------------------------------------
# Step 1: System packages
# ---------------------------------------------------------------------------
install_packages() {
    info "Installing system packages"
    # EPEL for additional tooling
    if ! rpm -q epel-release &>/dev/null; then
        dnf install -y epel-release
    fi
    dnf install -y \
        git \
        gcc \
        make \
        wget \
        curl \
        vim \
        gdisk \
        firewalld \
        dnsmasq \
        xfsprogs \
        e2fsprogs \
        dosfstools \
        parted \
        mdadm \
        grub2-tools \
        grub2-efi-x64-modules \
        efibootmgr \
        pigz \
        zstd \
        rsync \
        util-linux \
        busybox \
        cpio \
        file \
        xz \
        binutils \
        sshpass \
        qemu-kvm \
        qemu-img \
        genisoimage \
        xorriso \
        p7zip \
        p7zip-plugins \
        2>&1 | tail -5
    step_done "system packages"
}

# ---------------------------------------------------------------------------
# Step 2: Data disk (LABEL=clustr-data)
# ---------------------------------------------------------------------------
setup_data_disk() {
    info "Checking data disk (LABEL=${CLUSTR_DATA_LABEL})"

    # Find the device that carries the label, if already formatted
    DATA_DEV="$(blkid -L "${CLUSTR_DATA_LABEL}" 2>/dev/null || true)"

    if [[ -z "${DATA_DEV}" ]]; then
        warn "No device with LABEL=${CLUSTR_DATA_LABEL} found — locating a candidate data disk"
        # Pick the largest unmounted disk that is NOT the OS disk (sdb = OS in our setup)
        # In the VM: sda = data (100G), sdb = OS (32G) — but this is VM-layout dependent.
        # We look for a disk that is not mounted at / or /boot.
        CANDIDATE=""
        for dev in /dev/sda /dev/sdb /dev/sdc /dev/vda /dev/vdb; do
            [[ -b "${dev}" ]] || continue
            # Skip if it has a partition mounted at / or /boot
            if lsblk -no MOUNTPOINT "${dev}" 2>/dev/null | grep -qE '^/$|^/boot$'; then
                continue
            fi
            CANDIDATE="${dev}"
            break
        done

        [[ -n "${CANDIDATE}" ]] || die "Could not locate a candidate data disk. Set CLUSTR_DATA_LABEL or format manually."
        warn "Will format ${CANDIDATE} as XFS with LABEL=${CLUSTR_DATA_LABEL}. Ctrl-C within 5s to abort."
        sleep 5

        wipefs -a "${CANDIDATE}"
        parted -s "${CANDIDATE}" mklabel gpt
        parted -s "${CANDIDATE}" mkpart primary xfs 1MiB 100%
        partprobe "${CANDIDATE}" 2>/dev/null || true
        sleep 1

        # Find the partition (e.g., /dev/sda1)
        DATA_PART="${CANDIDATE}1"
        [[ -b "${DATA_PART}" ]] || DATA_PART="${CANDIDATE}p1"
        [[ -b "${DATA_PART}" ]] || die "Could not find partition on ${CANDIDATE}"

        mkfs.xfs -f -L "${CLUSTR_DATA_LABEL}" "${DATA_PART}"
        DATA_DEV="${DATA_PART}"
        log "Formatted ${DATA_DEV} as XFS LABEL=${CLUSTR_DATA_LABEL}"
    fi

    # Mount point
    mkdir -p "${CLUSTR_DATA_MOUNT}"

    # Add to fstab if not already there
    if ! grep -q "LABEL=${CLUSTR_DATA_LABEL}" /etc/fstab; then
        echo "LABEL=${CLUSTR_DATA_LABEL}  ${CLUSTR_DATA_MOUNT}  xfs  defaults,noatime  0  2" >> /etc/fstab
        log "Added data disk to /etc/fstab"
    fi

    # Mount if not already mounted
    if ! mountpoint -q "${CLUSTR_DATA_MOUNT}"; then
        mount "${CLUSTR_DATA_MOUNT}"
        log "Mounted ${CLUSTR_DATA_MOUNT}"
    fi

    # Create clustr runtime subdirectories
    mkdir -p \
        "${CLUSTR_DATA_MOUNT}/images" \
        "${CLUSTR_DATA_MOUNT}/boot" \
        "${CLUSTR_DATA_MOUNT}/tftpboot" \
        "${CLUSTR_DATA_MOUNT}/db"

    step_done "data disk at ${CLUSTR_DATA_MOUNT}"
}

# ---------------------------------------------------------------------------
# Step 3: Go toolchain
# ---------------------------------------------------------------------------
install_go() {
    info "Checking Go toolchain (want ${GO_VERSION})"

    if /usr/local/go/bin/go version 2>/dev/null | grep -q "${GO_VERSION}"; then
        step_done "Go ${GO_VERSION} already installed"
        return
    fi

    log "Downloading ${GO_TARBALL}..."
    cd /tmp
    curl -fsSL -o "${GO_TARBALL}" "${GO_URL}"

    # Verify checksum
    EXPECTED_SHA256="$(curl -fsSL "${GO_SHA256_URL}")"
    ACTUAL_SHA256="$(sha256sum "${GO_TARBALL}" | awk '{print $1}')"
    if [[ "${ACTUAL_SHA256}" != "${EXPECTED_SHA256}" ]]; then
        rm -f "${GO_TARBALL}"
        die "SHA256 mismatch for ${GO_TARBALL}: got ${ACTUAL_SHA256}, expected ${EXPECTED_SHA256}"
    fi

    rm -rf /usr/local/go
    tar -C /usr/local -xzf "${GO_TARBALL}"
    rm -f "${GO_TARBALL}"

    # Add to PATH for all sessions
    cat > /etc/profile.d/go.sh << 'GOPATH_SETUP'
export PATH=/usr/local/go/bin:$PATH
GOPATH_SETUP
    chmod +x /etc/profile.d/go.sh

    step_done "Go $(/usr/local/go/bin/go version)"
}

# ---------------------------------------------------------------------------
# Step 4: Clone / update clustr repo
# ---------------------------------------------------------------------------
setup_repo() {
    info "Checking clustr repo at ${CLUSTR_REPO_DIR}"

    if [[ -d "${CLUSTR_REPO_DIR}/.git" ]]; then
        log "Repo exists — pulling latest from origin/main"
        cd "${CLUSTR_REPO_DIR}"
        git fetch origin
        git reset --hard origin/main
    else
        log "Cloning ${CLUSTR_REPO_URL} → ${CLUSTR_REPO_DIR}"
        rm -rf "${CLUSTR_REPO_DIR}"
        git clone "${CLUSTR_REPO_URL}" "${CLUSTR_REPO_DIR}"
    fi

    cd "${CLUSTR_REPO_DIR}"
    log "Repo at: $(git log --oneline -1)"
    step_done "clustr repo"
}

# ---------------------------------------------------------------------------
# Step 5: Build clustr binaries
# ---------------------------------------------------------------------------
build_binaries() {
    info "Building clustr binaries"
    export PATH=/usr/local/go/bin:$PATH
    export GOPATH=/root/go

    cd "${CLUSTR_REPO_DIR}"

    GOTOOLCHAIN=auto go build -o /usr/local/bin/clustr-serverd ./cmd/clustr-serverd
    GOTOOLCHAIN=auto go build -o /usr/local/bin/clustr          ./cmd/clustr

    log "clustr-serverd: $(ls -lh /usr/local/bin/clustr-serverd | awk '{print $5}')"
    log "clustr:         $(ls -lh /usr/local/bin/clustr          | awk '{print $5}')"
    step_done "clustr binaries"
}

# ---------------------------------------------------------------------------
# Step 6: Install systemd service unit for clustr-serverd
# ---------------------------------------------------------------------------
install_service() {
    info "Installing clustr-serverd systemd unit"

    UNIT_SRC="${CLUSTR_REPO_DIR}/deploy/systemd/clustr-serverd.service"
    UNIT_DST="/etc/systemd/system/clustr-serverd.service"

    if [[ -f "${UNIT_SRC}" ]]; then
        cp "${UNIT_SRC}" "${UNIT_DST}"
        log "Installed from repo: ${UNIT_SRC}"
    else
        # Fallback: write a minimal unit
        warn "No unit file found at ${UNIT_SRC} — writing minimal fallback unit"
        cat > "${UNIT_DST}" << 'UNIT'
[Unit]
Description=clustr Server Daemon
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/clustr-serverd
Restart=on-failure
RestartSec=5s
User=root
WorkingDirectory=/var/lib/clustr
StandardOutput=journal
StandardError=journal
SyslogIdentifier=clustr-serverd

[Install]
WantedBy=multi-user.target
UNIT
    fi

    systemctl daemon-reload
    systemctl enable clustr-serverd

    # Restart if already running (picks up new binary), start if not
    if systemctl is-active --quiet clustr-serverd; then
        systemctl restart clustr-serverd
        log "clustr-serverd restarted"
    else
        systemctl start clustr-serverd
        log "clustr-serverd started"
    fi

    sleep 2
    systemctl is-active clustr-serverd || die "clustr-serverd failed to start — check: journalctl -u clustr-serverd"
    step_done "clustr-serverd service"
}

# ---------------------------------------------------------------------------
# Step 7: Install autodeploy timer
# ---------------------------------------------------------------------------
install_autodeploy() {
    info "Installing autodeploy timer"
    bash "${CLUSTR_REPO_DIR}/scripts/autodeploy/install.sh"
    step_done "autodeploy timer"
}

# ---------------------------------------------------------------------------
# Step 7b: Management IP alias (CLUSTR_MGMT_IP on eth0)
# ---------------------------------------------------------------------------
# clustr-serverd binds CLUSTR_LISTEN_ADDR to the provisioning interface only
# (10.99.0.1:8080). To reach the web UI from the management LAN, Caddy must
# listen on a separate IP on eth0. We add a stable IP alias using the .254
# convention so the operator always has a predictable address to bookmark.
setup_mgmt_ip_alias() {
    if [[ -z "${CLUSTR_MGMT_IP}" ]]; then
        warn "CLUSTR_MGMT_IP is empty — skipping management IP alias setup"
        return
    fi

    info "Setting up management IP alias: ${CLUSTR_MGMT_IP}/24 on eth0"

    if ! ip link show eth0 &>/dev/null; then
        warn "eth0 not found — skipping management IP alias (run again after NIC is attached)"
        return
    fi

    # Determine the prefix length from eth0's existing address (default 24 if unknown).
    local prefix_len
    prefix_len="$(ip -4 addr show eth0 2>/dev/null | awk '/inet / {split($2,a,"/"); print a[2]; exit}')"
    prefix_len="${prefix_len:-24}"

    # Check if the alias is already present.
    if ip -4 addr show eth0 2>/dev/null | grep -q "${CLUSTR_MGMT_IP}"; then
        log "IP alias ${CLUSTR_MGMT_IP} already present on eth0 — skipping"
        step_done "management IP alias (already configured)"
        return
    fi

    # Add the alias via NetworkManager for persistence.
    if command -v nmcli &>/dev/null; then
        local mgmt_con
        mgmt_con="$(nmcli -t -f NAME,DEVICE con show 2>/dev/null | grep ':eth0$' | head -1 | cut -d: -f1)"
        if [[ -n "${mgmt_con}" ]]; then
            nmcli con mod "${mgmt_con}" "+ipv4.addresses" "${CLUSTR_MGMT_IP}/${prefix_len}"
            nmcli con up "${mgmt_con}"
            log "Added ${CLUSTR_MGMT_IP}/${prefix_len} to NM connection '${mgmt_con}' (eth0)"
        else
            warn "No NetworkManager connection found for eth0 — adding alias via ip command (not persistent)"
            ip addr add "${CLUSTR_MGMT_IP}/${prefix_len}" dev eth0 2>/dev/null || true
        fi
    else
        # Fall back to ip command (non-persistent across reboots).
        warn "nmcli not found — adding alias via ip command (not persistent across reboots)"
        ip addr add "${CLUSTR_MGMT_IP}/${prefix_len}" dev eth0 2>/dev/null || true
    fi

    # Verify.
    if ip -4 addr show eth0 2>/dev/null | grep -q "${CLUSTR_MGMT_IP}"; then
        log "Verified: ${CLUSTR_MGMT_IP} is now present on eth0"
    else
        warn "Could not verify ${CLUSTR_MGMT_IP} on eth0 — Caddy config may not work correctly"
    fi

    step_done "management IP alias (${CLUSTR_MGMT_IP}/24 on eth0)"
}

# ---------------------------------------------------------------------------
# Step 7c: Install Caddy and write Caddyfile for management interface
# ---------------------------------------------------------------------------
# Caddy reverse-proxies CLUSTR_MGMT_IP:{80,8080} → 10.99.0.1:8080 (clustr-serverd).
# This is the operator's "front door". clustr-serverd stays bound to the
# provisioning interface only (SEC-P0-2 preserved).
install_caddy() {
    if [[ -z "${CLUSTR_MGMT_IP}" ]]; then
        warn "CLUSTR_MGMT_IP is empty — skipping Caddy install"
        return
    fi

    info "Installing Caddy reverse proxy for management interface (${CLUSTR_MGMT_IP})"

    # Install Caddy based on OS.
    if ! command -v caddy &>/dev/null; then
        if [[ "${OS_ID}" == "rocky" || "${OS_ID}" == "rhel" || "${OS_ID}" == "almalinux" || "${OS_ID}" == "centos" ]]; then
            dnf install -y 'dnf-command(copr)' 2>&1 | tail -3 || true
            dnf copr enable -y @caddy/caddy 2>&1 | tail -3
            dnf install -y caddy
        elif [[ "${OS_ID}" == "ubuntu" || "${OS_ID}" == "debian" ]]; then
            apt-get install -y debian-keyring debian-archive-keyring apt-transport-https 2>&1 | tail -3
            curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
                | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
            curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
                | tee /etc/apt/sources.list.d/caddy-stable.list
            apt-get update -qq && apt-get install -y caddy
        else
            warn "Unknown OS '${OS_ID}' — cannot install Caddy automatically. Install manually and re-run."
            return
        fi
        log "Caddy installed"
    else
        log "Caddy already installed"
    fi

    # Create log directory.
    mkdir -p /var/log/caddy
    chown caddy:caddy /var/log/caddy 2>/dev/null || true
    chmod 750 /var/log/caddy

    # Write Caddyfile.
    # Both :80 and :8080 on CLUSTR_MGMT_IP proxy to clustr-serverd on the
    # provisioning interface. :8080 ensures operators with existing bookmarks
    # or scripts on that port continue working.
    cat > /etc/caddy/Caddyfile << CADDYFILE
# clustr management interface bridge
# Generated by install-dev-vm.sh — do not edit by hand (re-run install to regenerate).
# CLUSTR_MGMT_IP = ${CLUSTR_MGMT_IP}
#
# Binds on the management interface alias (both :80 and :8080) and
# reverse-proxies to clustr-serverd which is bound to the provisioning
# interface (10.99.0.1:8080).
# SEC-P0-2 preserved: DHCP/TFTP/API remain on the provisioning interface only.

http://${CLUSTR_MGMT_IP}, http://${CLUSTR_MGMT_IP}:8080 {
    bind ${CLUSTR_MGMT_IP}

    reverse_proxy 10.99.0.1:8080 {
        transport http {
            read_timeout  3600s
            write_timeout 3600s
        }
    }

    header {
        X-Content-Type-Options "nosniff"
        X-Frame-Options "DENY"
        -Server
    }

    log {
        output file /var/log/caddy/clustr-access.log {
            roll_size 50mb
            roll_keep 5
        }
        format json
    }
}
CADDYFILE

    # Validate and start Caddy.
    caddy validate --config /etc/caddy/Caddyfile || die "Caddyfile validation failed — check /etc/caddy/Caddyfile"
    systemctl enable --now caddy
    systemctl reload caddy 2>/dev/null || systemctl restart caddy

    log "Caddy running — operator access at http://${CLUSTR_MGMT_IP}/ and http://${CLUSTR_MGMT_IP}:8080/"
    step_done "Caddy management interface bridge (${CLUSTR_MGMT_IP}:{80,8080} → 10.99.0.1:8080)"
}

# ---------------------------------------------------------------------------
# Step 7d: PXE provisioning network (eth1 static IP)
# ---------------------------------------------------------------------------
setup_pxe_network() {
    info "Configuring eth1 as PXE provisioning NIC (10.99.0.1/24)"

    if ! ip link show eth1 &>/dev/null; then
        warn "eth1 not found — skipping PXE network config (run again after NIC is attached)"
        return
    fi

    # Create the NetworkManager connection profile for eth1 if not already present.
    if ! nmcli con show pxe-net &>/dev/null; then
        nmcli con add type ethernet con-name pxe-net ifname eth1 \
            ipv4.method manual ipv4.addresses 10.99.0.1/24
        log "Created NetworkManager profile 'pxe-net' for eth1"
    else
        log "NetworkManager profile 'pxe-net' already exists"
    fi

    # Bring the connection up (idempotent).
    nmcli con up pxe-net || warn "nmcli con up pxe-net failed — interface may already be configured"

    step_done "PXE provisioning NIC (eth1 = 10.99.0.1/24)"
}

# ---------------------------------------------------------------------------
# Step 8: Firewall
# ---------------------------------------------------------------------------
configure_firewall() {
    info "Configuring firewalld"

    systemctl enable --now firewalld

    # ── Zone assignment ──────────────────────────────────────────────────────
    # eth0 (LAN/internet) → external zone (masquerade enabled by default)
    # eth1 (provisioning)  → internal zone (trusted local services)

    # Set zones via NetworkManager for persistence across NM restart events.
    local lan_con pxe_con
    lan_con=$(nmcli -t -f NAME,DEVICE con show --active 2>/dev/null | grep ':eth0$' | head -1 | cut -d: -f1)
    pxe_con=$(nmcli -t -f NAME,DEVICE con show --active 2>/dev/null | grep ':eth1$' | head -1 | cut -d: -f1)

    if [[ -n "${lan_con}" ]]; then
        nmcli con modify "${lan_con}" connection.zone external
        log "Set ${lan_con} (eth0) → external zone"
    else
        firewall-cmd --zone=external --change-interface=eth0 --permanent 2>/dev/null || true
        log "Set eth0 → external zone (firewall-cmd fallback)"
    fi

    if [[ -n "${pxe_con}" ]]; then
        nmcli con modify "${pxe_con}" connection.zone internal
        log "Set ${pxe_con} (eth1) → internal zone"
    else
        firewall-cmd --zone=internal --change-interface=eth1 --permanent 2>/dev/null || true
        log "Set eth1 → internal zone (firewall-cmd fallback)"
    fi

    # ── External zone (eth0, LAN) ────────────────────────────────────────────
    # Masquerade is enabled by default in the external zone; ensure it's on.
    firewall-cmd --zone=external --add-masquerade --permanent 2>/dev/null || true

    # Allow SSH and clustr API from LAN
    firewall-cmd --zone=external --add-service=ssh --permanent
    firewall-cmd --zone=external --add-port=8080/tcp --permanent

    # ── Internal zone (eth1, provisioning) ───────────────────────────────────
    # Services needed by PXE clients and deployed nodes
    firewall-cmd --zone=internal --add-service=ssh --permanent
    firewall-cmd --zone=internal --add-service=dhcp --permanent
    firewall-cmd --zone=internal --add-service=dns --permanent
    firewall-cmd --zone=internal --add-service=tftp --permanent
    firewall-cmd --zone=internal --add-port=8080/tcp --permanent
    firewall-cmd --zone=internal --add-port=636/tcp --permanent

    # DHCP (UDP 67): PXE clients send DHCPDISCOVER from 0.0.0.0 (before they
    # have an IP), so source-address rules won't match. Add a direct iptables
    # rule scoped to eth1 as a belt-and-suspenders guarantee.
    firewall-cmd --permanent --direct --add-rule ipv4 filter INPUT 0 \
        -i eth1 -p udp --dport 67 -j ACCEPT

    # ── Inter-zone forwarding policy ─────────────────────────────────────────
    # Allow traffic from internal (provisioning) to external (internet) for NAT.
    # firewalld 1.x requires a policy object for cross-zone forwarding.
    if ! firewall-cmd --permanent --get-policies 2>/dev/null | grep -q 'int-to-ext'; then
        firewall-cmd --permanent --new-policy int-to-ext
        firewall-cmd --permanent --policy int-to-ext --set-target ACCEPT
        firewall-cmd --permanent --policy int-to-ext --add-ingress-zone internal
        firewall-cmd --permanent --policy int-to-ext --add-egress-zone external
        log "Created int-to-ext forwarding policy"
    else
        log "int-to-ext policy already exists"
    fi

    firewall-cmd --reload
    step_done "firewalld (zone-based with masquerade)"
}

# ---------------------------------------------------------------------------
# Step 8b: NAT gateway — IP forwarding + dnsmasq + masquerade
# ---------------------------------------------------------------------------
setup_nat_gateway() {
    info "Configuring NAT gateway for provisioning network"

    # ── a) Enable IP forwarding (persistent) ─────────────────────────────────
    cat > /etc/sysctl.d/99-clustr-ipforward.conf << 'SYSCTL'
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
SYSCTL
    sysctl -p /etc/sysctl.d/99-clustr-ipforward.conf
    log "IP forwarding enabled"

    # ── b) Install dnsmasq ────────────────────────────────────────────────────
    if ! command -v dnsmasq &>/dev/null; then
        if command -v dnf &>/dev/null; then
            dnf install -y dnsmasq
        elif command -v apt-get &>/dev/null; then
            apt-get install -y dnsmasq
        elif command -v yum &>/dev/null; then
            yum install -y dnsmasq
        else
            warn "No supported package manager found — dnsmasq must be installed manually"
        fi
    fi

    # Write dnsmasq config — listen only on the provisioning interface so we
    # don't conflict with systemd-resolved or any other resolver on eth0.
    mkdir -p /etc/dnsmasq.d
    cat > /etc/dnsmasq.d/clustr-provisioning.conf << 'DNSMASQ'
# clustr provisioning network DNS forwarder
# Only listen on the provisioning interface — do not conflict with systemd-resolved
# or other DNS on eth0.
interface=eth1
bind-interfaces
domain-needed
bogus-priv
# Forward to well-known public resolvers. The first server that responds wins.
server=8.8.8.8
server=1.1.1.1
DNSMASQ

    systemctl enable --now dnsmasq
    log "dnsmasq configured and started"

    # ── c) Masquerade fallback for non-firewalld systems ─────────────────────
    # On systems without firewalld (Ubuntu/Debian with ufw, or bare iptables),
    # set up masquerade via iptables and persist it.
    if ! command -v firewall-cmd &>/dev/null; then
        log "firewalld not found — applying iptables masquerade"
        iptables -t nat -C POSTROUTING -s 10.99.0.0/24 -o eth0 -j MASQUERADE 2>/dev/null \
            || iptables -t nat -A POSTROUTING -s 10.99.0.0/24 -o eth0 -j MASQUERADE

        if command -v ufw &>/dev/null; then
            # Persist via ufw/before.rules if not already present
            if ! grep -q 'clustr-masquerade' /etc/ufw/before.rules 2>/dev/null; then
                sed -i '/^# END COMMIT/i # clustr-masquerade\n-A POSTROUTING -s 10.99.0.0/24 -o eth0 -j MASQUERADE' \
                    /etc/ufw/before.rules 2>/dev/null || true
                grep -q '^DEFAULT_FORWARD_POLICY' /etc/default/ufw \
                    && sed -i 's/DEFAULT_FORWARD_POLICY=.*/DEFAULT_FORWARD_POLICY="ACCEPT"/' /etc/default/ufw \
                    || echo 'DEFAULT_FORWARD_POLICY="ACCEPT"' >> /etc/default/ufw
                ufw allow in on eth1 to any port 53
                ufw reload
            fi
        else
            # No ufw — write an iptables-restore service to persist across reboots
            if command -v iptables-save &>/dev/null; then
                iptables-save > /etc/iptables.rules
                cat > /etc/systemd/system/iptables-restore.service << 'UNIT'
[Unit]
Description=Restore iptables rules
Before=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
ExecStart=/sbin/iptables-restore /etc/iptables.rules
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT
                systemctl daemon-reload
                systemctl enable iptables-restore
            fi
        fi
    fi
    # When firewalld IS present the masquerade is already handled by
    # configure_firewall() (external zone + int-to-ext policy).

    step_done "NAT gateway (IP forwarding + dnsmasq + masquerade)"
}

# ---------------------------------------------------------------------------
# Step 9: SSH hardening
# ---------------------------------------------------------------------------
harden_ssh() {
    info "Hardening SSH"

    SSHD_CONF="/etc/ssh/sshd_config"

    # PermitRootLogin: allow key-based root login (required for dev VM)
    if grep -q '^#*PermitRootLogin' "${SSHD_CONF}"; then
        sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' "${SSHD_CONF}"
    else
        echo 'PermitRootLogin prohibit-password' >> "${SSHD_CONF}"
    fi

    # Disable password auth — keys only
    if grep -q '^#*PasswordAuthentication' "${SSHD_CONF}"; then
        sed -i 's/^#*PasswordAuthentication.*/PasswordAuthentication no/' "${SSHD_CONF}"
    else
        echo 'PasswordAuthentication no' >> "${SSHD_CONF}"
    fi

    systemctl reload sshd || systemctl reload sshd.service || true
    step_done "SSH hardening"
}

# ---------------------------------------------------------------------------
# Step 10: udev rules for provisioning NICs
# ---------------------------------------------------------------------------
install_udev_rules() {
    info "Installing udev persistent network rules"

    # Write rules that pin eth0 and eth1 to the correct MACs.
    # MAC addresses here match the VM's virtio NIC assignments.
    # Adjust MACs if deploying to a different host.
    UDEV_RULE="/etc/udev/rules.d/70-persistent-net.rules"

    if [[ ! -f "${UDEV_RULE}" ]]; then
        # Detect MACs from the current interfaces
        ETH0_MAC="$(cat /sys/class/net/eth0/address 2>/dev/null || echo '')"
        ETH1_MAC="$(cat /sys/class/net/eth1/address 2>/dev/null || echo '')"

        if [[ -n "${ETH0_MAC}" && -n "${ETH1_MAC}" ]]; then
            cat > "${UDEV_RULE}" << RULES
# clustr dev VM — persistent interface names
# eth0: LAN (DHCP from router)
SUBSYSTEM=="net", ACTION=="add", ATTR{address}=="${ETH0_MAC}", NAME="eth0"
# eth1: Provisioning network (static 10.99.0.1/24)
SUBSYSTEM=="net", ACTION=="add", ATTR{address}=="${ETH1_MAC}", NAME="eth1"
RULES
            log "Wrote udev rules (eth0=${ETH0_MAC}, eth1=${ETH1_MAC})"
        else
            warn "Could not detect all MAC addresses — skipping udev rules"
        fi
    else
        log "udev rules already exist at ${UDEV_RULE}"
    fi

    step_done "udev rules"
}

# ---------------------------------------------------------------------------
# Final status report
# ---------------------------------------------------------------------------
print_status() {
    echo ""
    echo "========================================"
    echo "  clustr dev VM setup complete"
    echo "========================================"
    echo ""
    echo "Services:"
    systemctl is-active clustr-serverd          && echo "  clustr-serverd:          active" || echo "  clustr-serverd:          FAILED"
    systemctl is-active clustr-autodeploy.timer  && echo "  clustr-autodeploy.timer: active" || echo "  clustr-autodeploy.timer: FAILED"
    systemctl is-active caddy                  && echo "  caddy:                  active" || echo "  caddy:                  not running (management bridge unavailable)"
    systemctl is-active firewalld              && echo "  firewalld:              active" || echo "  firewalld:              FAILED"
    systemctl is-active dnsmasq               && echo "  dnsmasq:                active" || echo "  dnsmasq:                FAILED"
    echo ""
    echo "Management IP (CLUSTR_MGMT_IP): ${CLUSTR_MGMT_IP:-not set}"
    if [[ -n "${CLUSTR_MGMT_IP}" ]]; then
        echo "  Operator access: http://${CLUSTR_MGMT_IP}/"
        echo "  Alt port:        http://${CLUSTR_MGMT_IP}:8080/"
        echo "  (Both ports are proxied by Caddy → 10.99.0.1:8080)"
    fi
    echo ""
    echo "NAT Gateway:"
    sysctl net.ipv4.ip_forward | tr -d ' '
    if command -v firewall-cmd &>/dev/null; then
        firewall-cmd --zone=external --query-masquerade --permanent 2>/dev/null \
            && echo "  masquerade:             enabled" || echo "  masquerade:             DISABLED"
    else
        iptables -t nat -C POSTROUTING -s 10.99.0.0/24 -o eth0 -j MASQUERADE 2>/dev/null \
            && echo "  masquerade (iptables):  enabled" || echo "  masquerade (iptables):  DISABLED"
    fi
    echo ""
    echo "Disk:"
    df -h "${CLUSTR_DATA_MOUNT}" /
    echo ""
    echo "Network:"
    ip -4 addr show | grep -E 'inet |^[0-9]'
    echo ""
    echo "Go: $(/usr/local/go/bin/go version)"
    echo "clustr-serverd: $(ls -lh /usr/local/bin/clustr-serverd | awk '{print $5, $NF}')"
    echo "clustr:         $(ls -lh /usr/local/bin/clustr          | awk '{print $5, $NF}')"
    echo ""
    echo "Useful commands:"
    echo "  journalctl -u clustr-serverd -f              # tail server logs"
    echo "  journalctl -u clustr-autodeploy.service -f   # tail deploy logs"
    echo "  systemctl start clustr-autodeploy.service    # force immediate sync"
    echo "  systemctl stop clustr-autodeploy.timer       # pause auto-sync"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
require_root
detect_os

log "Starting clustr dev VM bootstrap"
log "OS: ${OS_ID} ${OS_VERSION}"
log "Repo: ${CLUSTR_REPO_URL}"
log "Data mount: ${CLUSTR_DATA_MOUNT} (LABEL=${CLUSTR_DATA_LABEL})"
log "Go: ${GO_VERSION}"
echo ""

install_packages
setup_data_disk
install_go
setup_repo
build_binaries
install_service
install_autodeploy
setup_mgmt_ip_alias
install_caddy
setup_pxe_network
configure_firewall
setup_nat_gateway
harden_ssh
install_udev_rules
print_status
