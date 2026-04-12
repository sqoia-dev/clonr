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
#
# Example:
#   CGO_ENABLED=0 go build -o bin/clonr ./cmd/clonr
#   ./scripts/build-initramfs.sh bin/clonr initramfs-clonr.img

set -euo pipefail

CLONR_BIN="${1:?Usage: build-initramfs.sh <clonr-binary> [output]}"
OUTPUT="${2:-initramfs-clonr.img}"

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
cat > "$WORKDIR/init" << 'INIT_EOF'
#!/bin/sh
# Redirect stdout/stderr to /dev/console so we get output on the serial/VGA console.
exec >/dev/console 2>/dev/console </dev/console

# Mount virtual filesystems.
mount -t proc  proc    /proc           2>/dev/null
mount -t sysfs sysfs   /sys            2>/dev/null
mount -t devtmpfs devtmpfs /dev        2>/dev/null || mount -t tmpfs tmpfs /dev
mkdir -p /dev/pts
mount -t devpts devpts /dev/pts        2>/dev/null
mkdir -p /tmp
chmod 1777 /tmp

echo "============================================"
echo " clonr initramfs booted"
echo "============================================"

# Parse kernel command line.
CLONR_SERVER=""
CLONR_MAC=""
for arg in $(cat /proc/cmdline); do
    case $arg in
        clonr.server=*) CLONR_SERVER="${arg#clonr.server=}" ;;
        clonr.mac=*)    CLONR_MAC="${arg#clonr.mac=}" ;;
    esac
done

echo "Server : ${CLONR_SERVER:-not set}"
echo "MAC    : ${CLONR_MAC:-auto-detect}"
echo ""

# Bring up loopback first.
ip link set lo up 2>/dev/null
ip addr add 127.0.0.1/8 dev lo 2>/dev/null || true

# Bring up networking — try DHCP on all non-loopback interfaces.
IFACE_UP=""
for iface_path in /sys/class/net/*/; do
    iface=$(basename "$iface_path")
    [ "$iface" = "lo" ] && continue
    echo "Bringing up $iface..."
    ip link set "$iface" up 2>/dev/null
    if udhcpc -i "$iface" -n -q -t 15 -T 3 -s /usr/share/udhcpc/default.script; then
        IFACE_UP="$iface"
        echo "DHCP on $iface: OK"
        break
    else
        echo "DHCP on $iface: failed"
    fi
done

if [ -z "$IFACE_UP" ]; then
    echo "WARNING: DHCP failed on all interfaces"
fi

echo ""
echo "Network state:"
ip addr show 2>/dev/null
ip route show 2>/dev/null
echo ""

# Build the clonr arguments.
# CLONR_SERVER env var is read by clonr's LoadClientConfig(), but also pass
# --server explicitly as belt-and-suspenders.
export CLONR_SERVER="${CLONR_SERVER:-http://10.99.0.1:8080}"
SERVER_ARG="--server ${CLONR_SERVER}"

echo "Running: /usr/bin/clonr deploy --auto ${SERVER_ARG}"
echo ""

/usr/bin/clonr deploy --auto ${SERVER_ARG}
CLONR_EXIT=$?

echo ""
if [ $CLONR_EXIT -eq 0 ]; then
    echo "clonr deploy --auto completed successfully (exit 0)"
else
    echo "clonr deploy --auto exited with code $CLONR_EXIT"
fi

echo ""
echo "Dropping to debug shell. Type 'poweroff' or 'reboot' when done."
exec /bin/sh
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
