#!/bin/bash
# install-dev-vm.sh — Bootstrap a fresh Rocky Linux 9 VM into a clonr dev host.
#
# Idempotent: safe to run multiple times on the same host. Each step checks
# whether work is already done before doing it.
#
# Prerequisites:
#   - Rocky Linux 9 (minimal install)
#   - Root shell (run as root or via sudo)
#   - Internet access to dl.google.com and github.com
#   - Two block devices:
#       /dev/sdb (or first disk) — OS disk (32 GB+)
#       /dev/sda (or second disk) — data disk (100 GB+), must be XFS with
#         LABEL=clonr-data, or will be formatted by this script
#
# Usage:
#   bash scripts/setup/install-dev-vm.sh
#
# Environment overrides:
#   CLONR_REPO_URL   — Git remote to clone from (default: https://github.com/sqoia-dev/clonr.git)
#   CLONR_REPO_DIR   — Local clone destination (default: /opt/clonr)
#   CLONR_DATA_LABEL — XFS volume label for the data disk (default: clonr-data)
#   CLONR_DATA_MOUNT — Mount point for data disk (default: /var/lib/clonr)
#   GO_VERSION       — Go toolchain version to install (default: go1.24.2)

set -euo pipefail

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
CLONR_REPO_URL="${CLONR_REPO_URL:-https://github.com/sqoia-dev/clonr.git}"
CLONR_REPO_DIR="${CLONR_REPO_DIR:-/opt/clonr}"
CLONR_DATA_LABEL="${CLONR_DATA_LABEL:-clonr-data}"
CLONR_DATA_MOUNT="${CLONR_DATA_MOUNT:-/var/lib/clonr}"
GO_VERSION="${GO_VERSION:-go1.24.2}"
GO_TARBALL="${GO_VERSION}.linux-amd64.tar.gz"
GO_URL="https://dl.google.com/go/${GO_TARBALL}"
GO_SHA256_URL="https://dl.google.com/go/${GO_TARBALL}.sha256"

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
# Step 2: Data disk (LABEL=clonr-data)
# ---------------------------------------------------------------------------
setup_data_disk() {
    info "Checking data disk (LABEL=${CLONR_DATA_LABEL})"

    # Find the device that carries the label, if already formatted
    DATA_DEV="$(blkid -L "${CLONR_DATA_LABEL}" 2>/dev/null || true)"

    if [[ -z "${DATA_DEV}" ]]; then
        warn "No device with LABEL=${CLONR_DATA_LABEL} found — locating a candidate data disk"
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

        [[ -n "${CANDIDATE}" ]] || die "Could not locate a candidate data disk. Set CLONR_DATA_LABEL or format manually."
        warn "Will format ${CANDIDATE} as XFS with LABEL=${CLONR_DATA_LABEL}. Ctrl-C within 5s to abort."
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

        mkfs.xfs -f -L "${CLONR_DATA_LABEL}" "${DATA_PART}"
        DATA_DEV="${DATA_PART}"
        log "Formatted ${DATA_DEV} as XFS LABEL=${CLONR_DATA_LABEL}"
    fi

    # Mount point
    mkdir -p "${CLONR_DATA_MOUNT}"

    # Add to fstab if not already there
    if ! grep -q "LABEL=${CLONR_DATA_LABEL}" /etc/fstab; then
        echo "LABEL=${CLONR_DATA_LABEL}  ${CLONR_DATA_MOUNT}  xfs  defaults,noatime  0  2" >> /etc/fstab
        log "Added data disk to /etc/fstab"
    fi

    # Mount if not already mounted
    if ! mountpoint -q "${CLONR_DATA_MOUNT}"; then
        mount "${CLONR_DATA_MOUNT}"
        log "Mounted ${CLONR_DATA_MOUNT}"
    fi

    # Create clonr runtime subdirectories
    mkdir -p \
        "${CLONR_DATA_MOUNT}/images" \
        "${CLONR_DATA_MOUNT}/boot" \
        "${CLONR_DATA_MOUNT}/tftpboot" \
        "${CLONR_DATA_MOUNT}/db"

    step_done "data disk at ${CLONR_DATA_MOUNT}"
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
# Step 4: Clone / update clonr repo
# ---------------------------------------------------------------------------
setup_repo() {
    info "Checking clonr repo at ${CLONR_REPO_DIR}"

    if [[ -d "${CLONR_REPO_DIR}/.git" ]]; then
        log "Repo exists — pulling latest from origin/main"
        cd "${CLONR_REPO_DIR}"
        git fetch origin
        git reset --hard origin/main
    else
        log "Cloning ${CLONR_REPO_URL} → ${CLONR_REPO_DIR}"
        rm -rf "${CLONR_REPO_DIR}"
        git clone "${CLONR_REPO_URL}" "${CLONR_REPO_DIR}"
    fi

    cd "${CLONR_REPO_DIR}"
    log "Repo at: $(git log --oneline -1)"
    step_done "clonr repo"
}

# ---------------------------------------------------------------------------
# Step 5: Build clonr binaries
# ---------------------------------------------------------------------------
build_binaries() {
    info "Building clonr binaries"
    export PATH=/usr/local/go/bin:$PATH
    export GOPATH=/root/go

    cd "${CLONR_REPO_DIR}"

    GOTOOLCHAIN=auto go build -o /usr/local/bin/clonr-serverd ./cmd/clonr-serverd
    GOTOOLCHAIN=auto go build -o /usr/local/bin/clonr          ./cmd/clonr

    log "clonr-serverd: $(ls -lh /usr/local/bin/clonr-serverd | awk '{print $5}')"
    log "clonr:         $(ls -lh /usr/local/bin/clonr          | awk '{print $5}')"
    step_done "clonr binaries"
}

# ---------------------------------------------------------------------------
# Step 6: Install systemd service unit for clonr-serverd
# ---------------------------------------------------------------------------
install_service() {
    info "Installing clonr-serverd systemd unit"

    UNIT_SRC="${CLONR_REPO_DIR}/deploy/systemd/clonr-serverd.service"
    UNIT_DST="/etc/systemd/system/clonr-serverd.service"

    if [[ -f "${UNIT_SRC}" ]]; then
        cp "${UNIT_SRC}" "${UNIT_DST}"
        log "Installed from repo: ${UNIT_SRC}"
    else
        # Fallback: write a minimal unit
        warn "No unit file found at ${UNIT_SRC} — writing minimal fallback unit"
        cat > "${UNIT_DST}" << 'UNIT'
[Unit]
Description=clonr Server Daemon
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/clonr-serverd
Restart=on-failure
RestartSec=5s
User=root
WorkingDirectory=/var/lib/clonr
StandardOutput=journal
StandardError=journal
SyslogIdentifier=clonr-serverd

[Install]
WantedBy=multi-user.target
UNIT
    fi

    systemctl daemon-reload
    systemctl enable clonr-serverd

    # Restart if already running (picks up new binary), start if not
    if systemctl is-active --quiet clonr-serverd; then
        systemctl restart clonr-serverd
        log "clonr-serverd restarted"
    else
        systemctl start clonr-serverd
        log "clonr-serverd started"
    fi

    sleep 2
    systemctl is-active clonr-serverd || die "clonr-serverd failed to start — check: journalctl -u clonr-serverd"
    step_done "clonr-serverd service"
}

# ---------------------------------------------------------------------------
# Step 7: Install autodeploy timer
# ---------------------------------------------------------------------------
install_autodeploy() {
    info "Installing autodeploy timer"
    bash "${CLONR_REPO_DIR}/scripts/autodeploy/install.sh"
    step_done "autodeploy timer"
}

# ---------------------------------------------------------------------------
# Step 8: Firewall
# ---------------------------------------------------------------------------
configure_firewall() {
    info "Configuring firewalld"

    systemctl enable --now firewalld

    # Default zone: drop (deny everything not explicitly allowed)
    firewall-cmd --set-default-zone=drop

    # Assign interfaces if not already in the zone
    for iface in eth0 eth1; do
        ip link show "${iface}" &>/dev/null || continue
        firewall-cmd --zone=drop --add-interface="${iface}" --permanent 2>/dev/null || true
    done

    # LAN rules (192.168.1.0/24): SSH + clonr API
    firewall-cmd --zone=drop \
        --add-rich-rule='rule family=ipv4 source address=192.168.1.0/24 service name=ssh accept' \
        --permanent
    firewall-cmd --zone=drop \
        --add-rich-rule='rule family=ipv4 source address=192.168.1.0/24 port port=8080 protocol=tcp accept' \
        --permanent

    # Provisioning network (10.99.0.0/24): TFTP + HTTP API + DHCP
    firewall-cmd --zone=drop \
        --add-rich-rule='rule family=ipv4 source address=10.99.0.0/24 port port=69 protocol=udp accept' \
        --permanent
    firewall-cmd --zone=drop \
        --add-rich-rule='rule family=ipv4 source address=10.99.0.0/24 port port=8080 protocol=tcp accept' \
        --permanent
    firewall-cmd --zone=drop \
        --add-rich-rule='rule family=ipv4 source address=10.99.0.0/24 port port=67 protocol=udp accept' \
        --permanent

    firewall-cmd --reload
    step_done "firewalld"
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
# clonr dev VM — persistent interface names
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
    echo "  clonr dev VM setup complete"
    echo "========================================"
    echo ""
    echo "Services:"
    systemctl is-active clonr-serverd        && echo "  clonr-serverd:          active" || echo "  clonr-serverd:          FAILED"
    systemctl is-active clonr-autodeploy.timer && echo "  clonr-autodeploy.timer: active" || echo "  clonr-autodeploy.timer: FAILED"
    systemctl is-active firewalld            && echo "  firewalld:              active" || echo "  firewalld:              FAILED"
    echo ""
    echo "Disk:"
    df -h "${CLONR_DATA_MOUNT}" /
    echo ""
    echo "Network:"
    ip -4 addr show | grep -E 'inet |^[0-9]'
    echo ""
    echo "Go: $(/usr/local/go/bin/go version)"
    echo "clonr-serverd: $(ls -lh /usr/local/bin/clonr-serverd | awk '{print $5, $NF}')"
    echo "clonr:         $(ls -lh /usr/local/bin/clonr          | awk '{print $5, $NF}')"
    echo ""
    echo "Useful commands:"
    echo "  journalctl -u clonr-serverd -f              # tail server logs"
    echo "  journalctl -u clonr-autodeploy.service -f   # tail deploy logs"
    echo "  systemctl start clonr-autodeploy.service    # force immediate sync"
    echo "  systemctl stop clonr-autodeploy.timer       # pause auto-sync"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
require_root

log "Starting clonr dev VM bootstrap"
log "Repo: ${CLONR_REPO_URL}"
log "Data mount: ${CLONR_DATA_MOUNT} (LABEL=${CLONR_DATA_LABEL})"
log "Go: ${GO_VERSION}"
echo ""

install_packages
setup_data_disk
install_go
setup_repo
build_binaries
install_service
install_autodeploy
configure_firewall
harden_ssh
install_udev_rules
print_status
