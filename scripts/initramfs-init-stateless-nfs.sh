#!/bin/sh
# initramfs-init-stateless-nfs.sh — PID 1 init script for the clustr
# STATELESS NFS boot mode.
#
# This script mounts an NFS root filesystem exported by clustr-serverd and
# pivots into it.  It does NOT perform any disk operations: no wipefs, no
# partitioning, no grub2-install, no mkfs.  The node is stateless — the root
# lives on NFS and changes are lost on reboot.
#
# Kernel cmdline expected (set by ServeIPXEScript for stateless_nfs nodes):
#   root=/dev/nfs
#   nfsroot=<cloner-ip>:/var/lib/clustr/images/<id>/rootfs,ro,vers=4
#   ip=dhcp
#   rw                 (nfs4 mount requires rw flag even for ro exports)
#
# This file is a template: build-initramfs.sh with --mode=stateless-nfs
# substitutes placeholders at build time via sed.
#
# Placeholders substituted at build time:
#   (none — this init script has no build-time placeholders)
#
# Differences from the block-install init script (initramfs-init.sh):
#   1. No clustr binary — stateless nodes do not run `clustr deploy --auto`.
#   2. No disk device enumeration or partitioning tools.
#   3. After DHCP succeeds, parse nfsroot= from /proc/cmdline and mount it.
#   4. Pivot root into the NFS mount point and exec the real /sbin/init.
#
# The nfs kernel module is loaded by the Linux kernel when ip=dhcp and
# root=/dev/nfs are present in the cmdline — the kernel handles the initial
# NFS mount.  This script is the fallback/pivot path for kernels that use an
# initramfs-based NFS root rather than in-kernel NFS mount.

# ── Step 0: Virtual filesystems ─────────────────────────────────────────────
mount -t proc  proc    /proc    2>/dev/null
mount -t sysfs sysfs   /sys     2>/dev/null
mount -t devtmpfs devtmpfs /dev 2>/dev/null || mount -t tmpfs tmpfs /dev
mkdir -p /dev/pts
mount -t devpts devpts /dev/pts 2>/dev/null
mkdir -p /tmp /mnt /newroot
chmod 1777 /tmp
LOG=/tmp/init.log
touch "$LOG"

log() {
    echo "$*" >> "$LOG"
    echo "$*"
}

log "============================================"
log " clustr stateless-nfs initramfs init started"
log "============================================"
log "cmdline: $(cat /proc/cmdline)"
log "kernel : $(uname -r)"

# ── Step 1: Register mdev as hotplug handler ─────────────────────────────────
echo /bin/mdev > /proc/sys/kernel/hotplug 2>/dev/null || true
/bin/mdev -s 2>/dev/null || true

# ── Step 2: Parse kernel cmdline ─────────────────────────────────────────────
NFSROOT=""
NFSIP=""
for arg in $(cat /proc/cmdline); do
    case $arg in
        nfsroot=*) NFSROOT="${arg#nfsroot=}" ;;
        ip=*)      NFSIP="${arg#ip=}" ;;
    esac
done
log "nfsroot : ${NFSROOT:-not set}"
log "ip      : ${NFSIP:-not set}"

# ── Step 3: Load NIC kernel modules ──────────────────────────────────────────
KVER=$(uname -r)
MODBASE="/lib/modules/$KVER"
for mod in \
    "$MODBASE/kernel/net/core/failover.ko" \
    "$MODBASE/kernel/drivers/net/net_failover.ko" \
    "$MODBASE/kernel/drivers/net/virtio_net.ko" \
    "$MODBASE/kernel/drivers/scsi/virtio_scsi.ko" \
    "$MODBASE/kernel/drivers/block/virtio_blk.ko"; do
    name=$(basename "$mod")
    if [ -f "$mod" ]; then
        err=$(insmod "$mod" 2>&1)
        log "insmod $name: exit=$? err='${err}'"
    fi
done

sleep 2
/bin/mdev -s 2>/dev/null || true
log "ifaces: $(ls /sys/class/net/ 2>/dev/null | tr '\n' ' ')"

# ── Step 4: DHCP ─────────────────────────────────────────────────────────────
ip link set lo up 2>/dev/null
ip addr add 127.0.0.1/8 dev lo 2>/dev/null || true

IFACE_UP=""
for iface_path in /sys/class/net/*/; do
    iface=$(basename "$iface_path")
    [ "$iface" = "lo" ] && continue
    log "dhcp: trying $iface ..."
    ip link set "$iface" up 2>/dev/null
    udhcpc -i "$iface" -n -q -t 10 -T 2 -V "PXEClient" -s /usr/share/udhcpc/default.script 2>&1 >> "$LOG"
    if [ $? -eq 0 ]; then
        IFACE_UP="$iface"
        log "dhcp: $iface succeeded"
        break
    fi
done

if [ -z "$IFACE_UP" ]; then
    log "ERROR: DHCP failed on all interfaces — cannot mount NFS root"
    log "Dropping to emergency shell"
    exec /bin/sh
fi

log "network up on $IFACE_UP"

# ── Step 5: Mount NFS root ────────────────────────────────────────────────────
# Parse nfsroot into server:path and options.
# Format: <server-ip>:<path>,option1,option2
# Example: 10.99.0.1:/var/lib/clustr/images/<id>/rootfs,ro,vers=4
if [ -z "$NFSROOT" ]; then
    log "ERROR: nfsroot= not set in kernel cmdline — cannot mount NFS root"
    log "Dropping to emergency shell"
    exec /bin/sh
fi

# Split nfsroot into server:path and mount options.
# The options come after the first comma that follows the path component.
NFS_SERVPATH="${NFSROOT%%,*}"
NFS_OPTS="${NFSROOT#*,}"
if [ "$NFS_OPTS" = "$NFS_SERVPATH" ]; then
    # No comma found — no extra options beyond the defaults.
    NFS_OPTS="ro,vers=4"
fi

log "mounting NFS: $NFS_SERVPATH opts=$NFS_OPTS"
mkdir -p /newroot
mount -t nfs4 -o "$NFS_OPTS" "$NFS_SERVPATH" /newroot 2>&1
if [ $? -ne 0 ]; then
    log "ERROR: NFS mount failed — server=$NFS_SERVPATH opts=$NFS_OPTS"
    log "Dropping to emergency shell"
    exec /bin/sh
fi
log "NFS root mounted at /newroot"

# ── Step 6: Verify the NFS root has a usable init ───────────────────────────
for candidate in /newroot/sbin/init /newroot/lib/systemd/systemd /newroot/usr/lib/systemd/systemd /newroot/init; do
    if [ -x "$candidate" ]; then
        REAL_INIT="$candidate"
        break
    fi
done
if [ -z "${REAL_INIT:-}" ]; then
    log "ERROR: no init found in NFS root (tried /sbin/init, /lib/systemd/systemd, /init)"
    log "NFS root contents:"
    ls /newroot/ 2>&1 >> "$LOG" || true
    log "Dropping to emergency shell"
    exec /bin/sh
fi
log "found init at $REAL_INIT — pivoting root"

# ── Step 7: Pivot root ───────────────────────────────────────────────────────
# Move virtual filesystems into the new root before pivot.
mount --bind /proc    /newroot/proc    2>/dev/null || true
mount --bind /sys     /newroot/sys     2>/dev/null || true
mount --bind /dev     /newroot/dev     2>/dev/null || true
mount --bind /dev/pts /newroot/dev/pts 2>/dev/null || true

# Switch root into the NFS filesystem and exec the real init.
# switch_root unmounts the old initramfs and frees its memory.
exec switch_root /newroot "${REAL_INIT#/newroot}" 2>&1
log "ERROR: switch_root failed"
exec /bin/sh
