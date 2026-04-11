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
mkdir -p "$WORKDIR"/usr/{bin,sbin}
mkdir -p "$WORKDIR"/lib64

# Install clonr binary.
cp "$CLONR_BIN" "$WORKDIR/usr/bin/clonr"
chmod 755 "$WORKDIR/usr/bin/clonr"

echo "  [+] Installed clonr binary ($(du -h "$CLONR_BIN" | cut -f1))"

# Install busybox for shell and basic utilities.
# Prefer system busybox-static; fall back to downloading a known-good musl build.
BUSYBOX_BIN=""
if command -v busybox &>/dev/null; then
    BUSYBOX_BIN="$(command -v busybox)"
    echo "  [+] Using system busybox: $BUSYBOX_BIN"
elif [[ -f /usr/lib/busybox/busybox-static ]]; then
    BUSYBOX_BIN=/usr/lib/busybox/busybox-static
    echo "  [+] Using /usr/lib/busybox/busybox-static"
else
    BUSYBOX_URL="https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox"
    echo "  [+] Downloading busybox from $BUSYBOX_URL..."
    curl -sL -o "$WORKDIR/bin/busybox" "$BUSYBOX_URL"
    BUSYBOX_BIN="$WORKDIR/bin/busybox"
fi

if [[ "$BUSYBOX_BIN" != "$WORKDIR/bin/busybox" ]]; then
    cp "$BUSYBOX_BIN" "$WORKDIR/bin/busybox"
fi
chmod 755 "$WORKDIR/bin/busybox"

# Create symlinks for all busybox applets we need.
for cmd in sh ash ls cat echo mount umount mkdir cp mv rm ip \
           ifconfig udhcpc lsblk modprobe insmod sleep printf \
           grep sed awk cut tr head tail wc df free uname dmesg; do
    ln -sf /bin/busybox "$WORKDIR/bin/$cmd"
done

echo "  [+] Installed busybox and symlinks"

# /etc/resolv.conf placeholder (udhcpc will overwrite this).
cat > "$WORKDIR/etc/resolv.conf" << 'EOF'
nameserver 8.8.8.8
nameserver 8.8.4.4
EOF

# udhcpc default script — busybox udhcpc calls this to configure the interface.
mkdir -p "$WORKDIR/usr/share/udhcpc"
cat > "$WORKDIR/usr/share/udhcpc/default.script" << 'UDHCPC_EOF'
#!/bin/sh
case "$1" in
    bound|renew)
        ip addr flush dev "$interface" 2>/dev/null
        ip addr add "$ip/$mask" dev "$interface"
        [ -n "$router" ] && ip route add default via "$router" dev "$interface"
        [ -n "$dns" ] && {
            > /etc/resolv.conf
            for d in $dns; do echo "nameserver $d" >> /etc/resolv.conf; done
        }
        ;;
    deconfig)
        ip addr flush dev "$interface" 2>/dev/null
        ;;
esac
exit 0
UDHCPC_EOF
chmod 755 "$WORKDIR/usr/share/udhcpc/default.script"

# init script — runs as PID 1 in the initramfs.
cat > "$WORKDIR/init" << 'INIT_EOF'
#!/bin/sh
# Mount virtual filesystems.
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev 2>/dev/null || mount -t tmpfs tmpfs /dev
mkdir -p /dev/pts
mount -t devpts devpts /dev/pts 2>/dev/null
mkdir -p /tmp
chmod 1777 /tmp

echo "clonr initramfs booted"

# Parse kernel command line.
CLONR_SERVER=""
CLONR_MAC=""
for arg in $(cat /proc/cmdline); do
    case $arg in
        clonr.server=*) CLONR_SERVER="${arg#clonr.server=}" ;;
        clonr.mac=*)    CLONR_MAC="${arg#clonr.mac=}" ;;
        console=*)      true ;; # already handled by kernel
    esac
done

echo "Server : ${CLONR_SERVER:-not set}"
echo "MAC    : ${CLONR_MAC:-auto-detect}"

# Bring up networking — try DHCP on all non-loopback interfaces.
IFACE_UP=""
for iface_path in /sys/class/net/*/; do
    iface=$(basename "$iface_path")
    [ "$iface" = "lo" ] && continue
    ip link set "$iface" up 2>/dev/null
    echo "Trying DHCP on $iface..."
    if udhcpc -i "$iface" -n -q -t 10 -s /usr/share/udhcpc/default.script 2>/dev/null; then
        IFACE_UP="$iface"
        echo "DHCP on $iface: OK"
        break
    fi
done

if [ -z "$IFACE_UP" ]; then
    echo "WARNING: DHCP failed on all interfaces — proceeding without network config"
fi

# Bring up loopback.
ip link set lo up 2>/dev/null
ip addr add 127.0.0.1/8 dev lo 2>/dev/null

# Export environment for clonr.
export CLONR_SERVER="${CLONR_SERVER}"

echo ""
echo "Starting clonr deploy --auto..."
exec /usr/bin/clonr deploy --auto
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
