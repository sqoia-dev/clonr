#!/bin/bash
# Build a minimal initramfs containing the clonr static binary.
# The initramfs boots, brings up networking via DHCP, then runs
# 'clonr deploy --auto' to register with the server and deploy an image.
#
# Usage:
#   ./scripts/build-initramfs.sh <clonr-binary> [output-path]
#
# Prerequisites:
#   - clonr binary must be statically compiled (CGO_ENABLED=0)
#   - busybox-static package OR internet access to download busybox
#   - cpio, gzip
#   - sshpass + access to clonr-server (192.168.1.151) for kernel modules
#     (virtio_net, net_failover, failover required for virtio NIC in initramfs)
#
# Example:
#   CGO_ENABLED=0 go build -o bin/clonr ./cmd/clonr
#   ./scripts/build-initramfs.sh bin/clonr initramfs-clonr.img

set -euo pipefail

CLONR_BIN="${1:?Usage: build-initramfs.sh <clonr-binary> [output]}"
OUTPUT="${2:-initramfs-clonr.img}"

# clonr-server SSH credentials — used to pull kernel modules.
# The initramfs kernel version must match the modules being loaded.
CLONR_SERVER_HOST="${CLONR_SERVER_HOST:-192.168.1.151}"
CLONR_SERVER_USER="${CLONR_SERVER_USER:-clonr}"
CLONR_SERVER_PASS="${CLONR_SERVER_PASS:-clonr}"

# Verify the binary exists and is executable.
if [[ ! -f "$CLONR_BIN" ]]; then
    echo "ERROR: clonr binary not found: $CLONR_BIN" >&2
    exit 1
fi

# Check required tools.
for tool in cpio gzip; do
    if ! command -v "$tool" &>/dev/null; then
        echo "ERROR: required tool not found: $tool" >&2
        exit 1
    fi
done

# Create temp root and ensure cleanup on exit.
WORKDIR=$(mktemp -d /tmp/clonr-initramfs.XXXXXXXX)
trap "rm -rf '$WORKDIR'" EXIT

echo "Building initramfs in $WORKDIR..."

# Minimal Linux directory structure.
mkdir -p "$WORKDIR"/{bin,sbin,dev,proc,sys,etc,run,tmp,var/log}
mkdir -p "$WORKDIR"/usr/{bin,sbin,share/udhcpc}
mkdir -p "$WORKDIR"/lib64

# Pre-create essential device nodes so /dev is usable before devtmpfs mounts.
mknod -m 622 "$WORKDIR/dev/console" c 5 1 2>/dev/null || true
mknod -m 666 "$WORKDIR/dev/null"    c 1 3 2>/dev/null || true
mknod -m 666 "$WORKDIR/dev/zero"    c 1 5 2>/dev/null || true
mknod -m 666 "$WORKDIR/dev/random"  c 1 8 2>/dev/null || true
mknod -m 666 "$WORKDIR/dev/urandom" c 1 9 2>/dev/null || true
mknod -m 666 "$WORKDIR/dev/tty"     c 5 0 2>/dev/null || true
mknod -m 640 "$WORKDIR/dev/tty0"    c 4 0 2>/dev/null || true
mknod -m 640 "$WORKDIR/dev/tty1"    c 4 1 2>/dev/null || true
mkdir -p "$WORKDIR/dev/pts"

# Install clonr binary.
cp "$CLONR_BIN" "$WORKDIR/usr/bin/clonr"
chmod 755 "$WORKDIR/usr/bin/clonr"

echo "  [+] Installed clonr binary ($(du -h "$CLONR_BIN" | cut -f1))"

# Install busybox for shell and basic utilities.
# Prefer a musl static build from busybox.net (most complete applet set).
# Fall back to the system busybox if the download fails.
BUSYBOX_URL="https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox"
if curl -sL --max-time 30 -o "$WORKDIR/bin/busybox" "$BUSYBOX_URL"; then
    chmod 755 "$WORKDIR/bin/busybox"
    echo "  [+] Downloaded busybox 1.35.0 musl from busybox.net"
elif command -v busybox &>/dev/null && file "$(command -v busybox)" | grep -q "statically linked"; then
    cp "$(command -v busybox)" "$WORKDIR/bin/busybox"
    chmod 755 "$WORKDIR/bin/busybox"
    echo "  [+] Using system busybox (static): $(command -v busybox)"
elif [[ -f /usr/lib/busybox/busybox-static ]]; then
    cp /usr/lib/busybox/busybox-static "$WORKDIR/bin/busybox"
    chmod 755 "$WORKDIR/bin/busybox"
    echo "  [+] Using /usr/lib/busybox/busybox-static"
else
    echo "ERROR: cannot obtain a static busybox binary" >&2
    exit 1
fi

# Create symlinks for all busybox applets we need.
# Note: lsblk is NOT a busybox applet (it comes from util-linux).
# clonr hardware discovery tolerates lsblk absence — disk list will be empty,
# but node registration still succeeds.
for cmd in sh ash ls cat echo mount umount mkdir cp mv rm ip \
           ifconfig udhcpc modprobe insmod sleep printf \
           grep sed awk cut tr head tail wc df free uname dmesg \
           mdev switch_root pivot_root chroot; do
    ln -sf /bin/busybox "$WORKDIR/bin/$cmd"
done

echo "  [+] Installed busybox and symlinks"

# ──────────────────────────────────────────────────────────────────────────────
# Kernel modules for virtio NIC support.
#
# The Rocky 9 kernel served by clonr-server has virtio_pci built-in but
# virtio_net (+ its deps net_failover, failover) as loadable modules.
# Without these, the NIC won't appear in the initramfs and DHCP won't work.
#
# We pull the modules from the clonr-server (same kernel version as the PXE
# kernel) and embed them. The init script calls modprobe before udhcpc.
# ──────────────────────────────────────────────────────────────────────────────
echo "  [+] Fetching kernel modules from clonr-server ${CLONR_SERVER_HOST}..."

# Discover the kernel version from the server.
KVER=$(sshpass -p "$CLONR_SERVER_PASS" ssh -o StrictHostKeyChecking=no \
    "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}" "uname -r" 2>/dev/null)

if [[ -z "$KVER" ]]; then
    echo "WARNING: cannot reach clonr-server — skipping kernel modules." >&2
    echo "         virtio_net will not be loaded; DHCP may fail on virtio NICs." >&2
    KVER="unknown"
else
    echo "      kernel version: $KVER"

    # Modules needed for virtio NIC: failover → net_failover → virtio_net
    # failover lives in net/core/, the rest in drivers/net/.
    # We fetch the .ko.xz files and decompress to plain .ko because busybox
    # insmod uses the init_module syscall which needs an uncompressed ELF.
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/net/core"
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/drivers/net"

    # List of module paths relative to /lib/modules/$KVER/kernel/
    MODULES=(
        "net/core/failover.ko.xz"
        "drivers/net/net_failover.ko.xz"
        "drivers/net/virtio_net.ko.xz"
    )

    for mod_rel in "${MODULES[@]}"; do
        REMOTE_PATH="/lib/modules/$KVER/kernel/${mod_rel}"
        # Destination: strip .xz suffix for the local .ko file
        LOCAL_KO_XZ="$WORKDIR/lib/modules/$KVER/kernel/${mod_rel}"
        LOCAL_KO="${LOCAL_KO_XZ%.xz}"
        mkdir -p "$(dirname "$LOCAL_KO_XZ")"

        if sshpass -p "$CLONR_SERVER_PASS" scp -o StrictHostKeyChecking=no \
            "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}:${REMOTE_PATH}" \
            "$LOCAL_KO_XZ" 2>/dev/null; then
            # Decompress in place: failover.ko.xz → failover.ko
            if xz -d "$LOCAL_KO_XZ" 2>/dev/null; then
                echo "      fetched+decompressed: $(basename "$LOCAL_KO")"
            else
                echo "WARNING: failed to decompress ${LOCAL_KO_XZ}" >&2
                rm -f "$LOCAL_KO_XZ"
            fi
        else
            echo "WARNING: failed to fetch ${REMOTE_PATH}" >&2
        fi
    done

    # Generate a minimal modules.dep for plain .ko files.
    MODDEP_DIR="$WORKDIR/lib/modules/$KVER"
    cat > "$MODDEP_DIR/modules.dep" << MODDEP
kernel/net/core/failover.ko:
kernel/drivers/net/net_failover.ko: kernel/net/core/failover.ko
kernel/drivers/net/virtio_net.ko: kernel/drivers/net/net_failover.ko kernel/net/core/failover.ko
MODDEP

    cat > "$MODDEP_DIR/modules.alias" << MODALIAS
alias virtio:d00000001v* virtio_net
MODALIAS

    echo "      generated modules.dep for $KVER"
fi

echo "  [+] Kernel modules ready"

# /etc/resolv.conf placeholder (udhcpc will overwrite this).
cat > "$WORKDIR/etc/resolv.conf" << 'EOF'
nameserver 8.8.8.8
nameserver 8.8.4.4
EOF

# udhcpc default script — busybox udhcpc calls this to configure the interface.
# $mask is passed as dotted-decimal (e.g. 255.255.255.0); convert to CIDR prefix
# because `ip addr add` requires CIDR notation (e.g. 192.168.1.10/24).
cat > "$WORKDIR/usr/share/udhcpc/default.script" << 'UDHCPC_EOF'
#!/bin/sh

# Convert a dotted-decimal netmask to a CIDR prefix length.
mask2cidr() {
    local mask="$1"
    local cidr=0
    local IFS='.'
    for octet in $mask; do
        case "$octet" in
            255) cidr=$((cidr + 8)) ;;
            254) cidr=$((cidr + 7)) ;;
            252) cidr=$((cidr + 6)) ;;
            248) cidr=$((cidr + 5)) ;;
            240) cidr=$((cidr + 4)) ;;
            224) cidr=$((cidr + 3)) ;;
            192) cidr=$((cidr + 2)) ;;
            128) cidr=$((cidr + 1)) ;;
            0)   ;;
        esac
    done
    echo "$cidr"
}

case "$1" in
    bound|renew)
        PREFIX=$(mask2cidr "$mask")
        ip addr flush dev "$interface" 2>/dev/null || true
        ip addr add "${ip}/${PREFIX}" dev "$interface"
        [ -n "$router" ] && ip route add default via "$router" dev "$interface" 2>/dev/null || true
        [ -n "$dns" ] && {
            > /etc/resolv.conf
            for d in $dns; do echo "nameserver $d" >> /etc/resolv.conf; done
        }
        echo "udhcpc: bound ${ip}/${PREFIX} gw=${router} on ${interface}"
        ;;
    deconfig)
        ip addr flush dev "$interface" 2>/dev/null || true
        ;;
esac
exit 0
UDHCPC_EOF
chmod 755 "$WORKDIR/usr/share/udhcpc/default.script"

# init script — runs as PID 1 in the initramfs.
# Always drops to a busybox shell on exit so the node stays debuggable.
# NOTE: do NOT redirect to /dev/console at startup — the kernel already sets
# up PID 1's stdio to /dev/console based on the 'console=' kernel param.
# An explicit exec >/dev/console can hang if the device node isn't ready.
cat > "$WORKDIR/init" << INIT_EOF
#!/bin/sh

# Log everything to both console and /tmp/init.log for post-mortem debugging.
exec 2>&1
LOG=/tmp/init.log
log() { echo "\$*" | tee -a "\$LOG"; }

# Mount virtual filesystems.
mount -t proc  proc    /proc           2>/dev/null
mount -t sysfs sysfs   /sys            2>/dev/null
mount -t devtmpfs devtmpfs /dev        2>/dev/null || mount -t tmpfs tmpfs /dev
mkdir -p /dev/pts
mount -t devpts devpts /dev/pts        2>/dev/null
mkdir -p /tmp /tmp/mnt
chmod 1777 /tmp
touch "\$LOG"

log "============================================"
log " clonr initramfs booted"
log "============================================"
log "cmdline: \$(cat /proc/cmdline)"
log "kernel : \$(uname -r)"
log ""

# Parse kernel command line.
CLONR_SERVER=""
CLONR_MAC=""
for arg in \$(cat /proc/cmdline); do
    case \$arg in
        clonr.server=*) CLONR_SERVER="\${arg#clonr.server=}" ;;
        clonr.mac=*)    CLONR_MAC="\${arg#clonr.mac=}" ;;
    esac
done

log "Server : \${CLONR_SERVER:-not set}"
log "MAC    : \${CLONR_MAC:-auto-detect}"
log ""

# Load kernel modules for virtio NIC using insmod with explicit paths.
KVER=\$(uname -r)
MODBASE="/lib/modules/\$KVER"
log "Loading NIC modules for \$KVER..."
log "  module dir exists: \$(ls \$MODBASE/kernel/net/core/ 2>/dev/null || echo MISSING)"

insmod "\$MODBASE/kernel/net/core/failover.ko" 2>>/tmp/insmod.err
RC=\$?; log "  insmod failover: exit=\$RC \$(cat /tmp/insmod.err 2>/dev/null)"
: > /tmp/insmod.err

insmod "\$MODBASE/kernel/drivers/net/net_failover.ko" 2>>/tmp/insmod.err
RC=\$?; log "  insmod net_failover: exit=\$RC \$(cat /tmp/insmod.err 2>/dev/null)"
: > /tmp/insmod.err

insmod "\$MODBASE/kernel/drivers/net/virtio_net.ko" 2>>/tmp/insmod.err
RC=\$?; log "  insmod virtio_net: exit=\$RC \$(cat /tmp/insmod.err 2>/dev/null)"
: > /tmp/insmod.err

log ""
log "Loaded modules: \$(cat /proc/modules 2>/dev/null | grep -E 'virtio|failover' | cut -d' ' -f1 | tr '\n' ' ')"
log "Net interfaces: \$(ls /sys/class/net/ 2>/dev/null | tr '\n' ' ')"
log ""

# Give the kernel a moment to enumerate the new NIC.
sleep 2

# Bring up loopback first.
ip link set lo up 2>/dev/null
ip addr add 127.0.0.1/8 dev lo 2>/dev/null || true

# Bring up networking — try DHCP on all non-loopback interfaces.
IFACE_UP=""
for iface_path in /sys/class/net/*/; do
    iface=\$(basename "\$iface_path")
    [ "\$iface" = "lo" ] && continue
    log "Bringing up \$iface..."
    ip link set "\$iface" up 2>/dev/null
    if udhcpc -i "\$iface" -n -q -t 15 -T 3 -s /usr/share/udhcpc/default.script 2>&1 | tee -a "\$LOG"; then
        IFACE_UP="\$iface"
        log "DHCP on \$iface: OK"
        break
    else
        log "DHCP on \$iface: failed"
    fi
done

if [ -z "\$IFACE_UP" ]; then
    log "WARNING: DHCP failed on all interfaces"
fi

log ""
log "Network state:"
ip addr show 2>/dev/null | tee -a "\$LOG"
ip route show 2>/dev/null | tee -a "\$LOG"
log ""

# Build the clonr arguments.
export CLONR_SERVER="\${CLONR_SERVER:-http://10.99.0.1:8080}"
SERVER_ARG="--server \${CLONR_SERVER}"

log "Running: /usr/bin/clonr deploy --auto \${SERVER_ARG}"
log ""

/usr/bin/clonr deploy --auto \${SERVER_ARG}
CLONR_EXIT=\$?

log ""
if [ \$CLONR_EXIT -eq 0 ]; then
    log "clonr deploy --auto completed successfully (exit 0)"
else
    log "clonr deploy --auto exited with code \$CLONR_EXIT"
fi

log ""
log "Init complete. PID 1 sleeping to keep kernel alive."
log "Connect to serial console for debug access."
# Loop forever to prevent kernel panic from PID 1 exit.
# In production, clonr deploy would reboot after success.
while true; do
    sleep 3600
done
INIT_EOF
chmod 755 "$WORKDIR/init"

echo "  [+] Generated init script"

# Verify clonr binary is statically linked (best effort check on Linux).
if command -v file &>/dev/null; then
    FILE_OUT="$(file "$CLONR_BIN")"
    if echo "$FILE_OUT" | grep -q "dynamically linked"; then
        echo ""
        echo "WARNING: clonr binary appears to be dynamically linked." >&2
        echo "         Build with CGO_ENABLED=0 for a self-contained initramfs binary." >&2
        echo "         Command: CGO_ENABLED=0 go build -o $CLONR_BIN ./cmd/clonr" >&2
        echo ""
    fi
fi

# Build the cpio archive and compress with gzip.
echo "Packing cpio archive..."
(
    cd "$WORKDIR"
    find . | sort | cpio --quiet -H newc -o 2>/dev/null
) | gzip -9 > "$OUTPUT"

SIZE="$(du -h "$OUTPUT" | cut -f1)"
echo ""
echo "Built initramfs: $OUTPUT ($SIZE)"
echo ""
echo "Deploy to boot server:"
echo "  cp $OUTPUT /var/lib/clonr/boot/initramfs.img"
echo ""
echo "Download kernel:"
echo "  # Rocky Linux 9 kernel (example):"
echo "  dnf download --resolve kernel-core"
echo "  rpm2cpio kernel-core-*.rpm | cpio -id ./boot/vmlinuz-*"
echo "  cp boot/vmlinuz-* /var/lib/clonr/boot/vmlinuz"
