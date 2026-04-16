#!/bin/sh
# initramfs-init.sh — PID 1 init script for the clonr deploy initramfs.
#
# This file is a template: build-initramfs.sh substitutes placeholders at
# build time via sed before writing the final /init into the cpio archive.
#
# Placeholders substituted by build-initramfs.sh:
#   ${CLONR_SERVER}      — default server URL (e.g. http://10.99.0.1:8080)
#   ${CLONR_STATIC_BIN}  — path to the clonr static binary being embedded
#
# All other shell variables (${CLONR_TOKEN}, ${LOG}, etc.) are runtime
# variables resolved when the init script executes inside the initramfs.

# ── Step 0: create /tmp and start logging to /tmp/init.log ───────────────────
# VERY EARLY: mount proc/sys/dev before anything else so /dev/console exists.
# We mount devtmpfs here (before /tmp) so we can write to /dev/console
# in the log() function immediately.
mount -t proc  proc    /proc    2>/dev/null
mount -t sysfs sysfs   /sys     2>/dev/null
mount -t devtmpfs devtmpfs /dev 2>/dev/null || mount -t tmpfs tmpfs /dev
mkdir -p /dev/pts
mount -t devpts devpts /dev/pts 2>/dev/null
mkdir -p /tmp /mnt
chmod 1777 /tmp
LOG=/tmp/init.log
touch "$LOG"

# log() writes to the log file and to stdout.
# PID 1 stdout is attached to the serial console (ttyS0) via the kernel
# cmdline console= parameter. Do NOT also write to /dev/console — on VMs
# without a VGA adapter /dev/console resolves to ttyS0 as well, causing
# every log line to appear twice on the serial console.
log() {
    echo "$*" >> "$LOG"
    echo "$*"
}

# ── Step 1: virtual filesystems already mounted in Step 0 ────────────────────

log "============================================"
log " clonr initramfs init started"
log "============================================"

# ── Step 1b: register mdev as the kernel hotplug handler ─────────────────────
# This makes the kernel exec /bin/mdev for every uevent (new disk, new partition,
# etc.). Without this, partition nodes (/dev/sda1, /dev/sda2) are never created
# after sgdisk because there is no udevd in the initramfs to process the uevents.
# With this set, the moment sgdisk writes a new partition table and the kernel
# fires the partition-add uevents, mdev is invoked and creates /dev/sda1 etc.
echo /bin/mdev > /proc/sys/kernel/hotplug 2>/dev/null || true
log "hotplug handler: $(cat /proc/sys/kernel/hotplug 2>/dev/null || echo unavailable)"
# Run mdev -s once now to create device nodes for all currently visible hardware.
/bin/mdev -s 2>/dev/null || true
log "cmdline: $(cat /proc/cmdline)"
log "kernel : $(uname -r)"

# ── Step 3: parse kernel command line ─────────────────────────────────────────
CLONR_SERVER=""
CLONR_MAC=""
for arg in $(cat /proc/cmdline); do
    case $arg in
        clonr.server=*) CLONR_SERVER="${arg#clonr.server=}" ;;
        clonr.mac=*)    CLONR_MAC="${arg#clonr.mac=}" ;;
    esac
done
log "server : ${CLONR_SERVER:-not set}"
log "mac    : ${CLONR_MAC:-auto-detect}"

# ── Step 4: load virtio NIC modules ───────────────────────────────────────────
# Dependency order: failover → net_failover → virtio_net
# Modules are pre-decompressed (.ko, not .ko.xz) because busybox insmod
# uses init_module syscall which requires uncompressed ELF.
KVER=$(uname -r)
MODBASE="/lib/modules/$KVER"
log "modules dir: $(ls $MODBASE/kernel/net/core/ 2>&1)"

# Wait for PCI enumeration to complete
sleep 3

# Pre-flight: check PCI devices and virtio bus
log "virtio bus devs: $(ls /sys/bus/virtio/devices/ 2>/dev/null | tr '\n' ' ' || echo NONE)"
log "pci devs: $(ls /sys/bus/pci/devices/ 2>/dev/null | tr '\n' ' ' | head -c 200 || echo NONE)"
log "dmesg virtio: $(dmesg 2>/dev/null | grep -iE 'virtio|net' | tail -5 | tr '\n' '|')"

for mod in \
    "$MODBASE/kernel/net/core/failover.ko" \
    "$MODBASE/kernel/drivers/net/net_failover.ko" \
    "$MODBASE/kernel/drivers/net/virtio_net.ko" \
    "$MODBASE/kernel/drivers/scsi/virtio_scsi.ko" \
    "$MODBASE/kernel/drivers/scsi/sd_mod.ko" \
    "$MODBASE/kernel/drivers/block/virtio_blk.ko" \
    "$MODBASE/kernel/arch/x86/crypto/crc32c-intel.ko" \
    "$MODBASE/kernel/lib/libcrc32c.ko" \
    "$MODBASE/kernel/fs/xfs/xfs.ko" \
    "$MODBASE/kernel/fs/mbcache.ko" \
    "$MODBASE/kernel/fs/jbd2/jbd2.ko" \
    "$MODBASE/kernel/fs/ext4/ext4.ko" \
    "$MODBASE/kernel/fs/fat/fat.ko" \
    "$MODBASE/kernel/fs/fat/vfat.ko" \
    "$MODBASE/kernel/drivers/md/raid1.ko" \
    "$MODBASE/kernel/drivers/md/raid0.ko" \
    "$MODBASE/kernel/drivers/md/raid10.ko" \
    "$MODBASE/kernel/drivers/md/raid456.ko"; do
    name=$(basename "$mod")
    if [ -f "$mod" ]; then
        err=$(insmod "$mod" 2>&1)
        rc=$?
        log "insmod $name: exit=$rc err='${err}'"
        # Capture kernel ring buffer messages for module load failures
        dmesg | tail -3 >> "$LOG"
    else
        log "insmod $name: FILE MISSING at $mod"
        ls -la "$(dirname $mod)" 2>&1 >> "$LOG"
    fi
done

# Wait for storage devices to enumerate after module load.
# virtio_scsi probes asynchronously — the SCSI device appears in dmesg almost
# immediately, but the block device node (/dev/sda) is created by the kernel
# slightly later. SeaBIOS + virtio-scsi can take 30-45 seconds to enumerate
# disks (BIOS firmware initialisation is slower than UEFI/OVMF). Poll
# /sys/class/block/ until at least one disk appears (or 45 seconds elapse).
# Exclude loop devices and ram disks. Run mdev -s every 5 seconds to ensure
# device nodes are created for any newly enumerated block devices.
log "waiting for block devices to appear in /sys/class/block/..."
for _wait in $(seq 1 45); do
    BLKDEVS=$(ls /sys/class/block/ 2>/dev/null | grep -vE '^(loop|ram)' | tr '\n' ' ')
    if [ -n "$BLKDEVS" ]; then
        log "block devices appeared after ${_wait}s: $BLKDEVS"
        break
    fi
    # Re-run mdev every 5 seconds to catch late-arriving block device uevents
    case "$_wait" in 5|10|15|20|25|30|35|40) /bin/mdev -s 2>/dev/null || true ;; esac
    sleep 1
done
if [ -z "$BLKDEVS" ]; then
    log "WARNING: no block devices appeared after 45s — disk discovery will return empty"
fi
log "block devices: $(ls /sys/class/block/ 2>/dev/null | tr '\n' ' ' || echo NONE)"
log "/dev contents: $(ls /dev/ 2>/dev/null | tr '\n' ' ' | head -c 200)"
log "lsblk test (simple): $(/usr/bin/lsblk --json --bytes --output NAME,SIZE,TYPE 2>&1 | head -c 500 || echo LSBLK_FAILED)"
log "lsblk test (full cols): $(/usr/bin/lsblk --json --bytes --output NAME,SIZE,TYPE,MODEL,SERIAL,FSTYPE,MOUNTPOINT,TRAN,ROTA,PHY-SEC,LOG-SEC,PTTYPE,PTUUID,PARTUUID,PARTTYPE,PARTLABEL 2>&1 | head -c 800 || echo LSBLK_FULL_FAILED)"

# Run mdev -s to scan sysfs and create device nodes for all discovered hardware.
# This is critical in initramfs environments — devtmpfs creates /dev/sda but
# partition nodes (/dev/sda1 etc.) may not appear until mdev scans /sys/class/block/.
# We run mdev twice: once now (for base disk nodes), and again after partprobe
# (handled by the clonr deploy code itself).
/bin/mdev -s 2>/dev/null || true
log "mdev -s ran — dev nodes after mdev: $(ls /dev/sd* /dev/vd* /dev/nvme* 2>/dev/null | tr '\n' ' ' || echo none)"
log "loaded: $(cat /proc/modules 2>/dev/null | grep -E 'virtio|failover|xfs|ext4' | cut -d' ' -f1 | tr '\n' ' ')"
log "ifaces: $(ls /sys/class/net/ 2>/dev/null | tr '\n' ' ')"
# Also dump all interfaces for diagnostics
ls -la /sys/class/net/ 2>/dev/null >> "$LOG"

# Give kernel time to enumerate the NIC after module load
sleep 2
log "ifaces after sleep: $(ls /sys/class/net/ 2>/dev/null | tr '\n' ' ')"

# ── Step 5: bring up loopback ─────────────────────────────────────────────────
ip link set lo up 2>/dev/null
ip addr add 127.0.0.1/8 dev lo 2>/dev/null || true

# ── Step 6: DHCP on each non-loopback interface ───────────────────────────────
# IMPORTANT: do NOT pipe udhcpc directly into tee inside an 'if' — the if
# tests the pipe's last command (tee), which exits 0 even when udhcpc fails.
# Instead capture exit code separately.
IFACE_UP=""
for iface_path in /sys/class/net/*/; do
    iface=$(basename "$iface_path")
    [ "$iface" = "lo" ] && continue
    log "dhcp: trying $iface ..."
    ip link set "$iface" up 2>/dev/null
    udhcpc -i "$iface" -n -q -t 10 -T 2 -V "PXEClient" -s /usr/share/udhcpc/default.script 2>&1 >> "$LOG"
    DHCP_RC=$?
    log "dhcp: $iface exit=$DHCP_RC"
    if [ $DHCP_RC -eq 0 ]; then
        IFACE_UP="$iface"
        log "dhcp: $iface OK"
        break
    else
        log "dhcp: $iface failed (rc=$DHCP_RC)"
    fi
done

if [ -z "$IFACE_UP" ]; then
    log "WARNING: DHCP failed on all interfaces — assigning static fallback IP"
    # Assign static IP so clonr-server can reach us to pull /tmp/init.log
    for iface_path in /sys/class/net/*/; do
        iface=$(basename "$iface_path")
        [ "$iface" = "lo" ] && continue
        ip link set "$iface" up 2>/dev/null
        ip addr add 10.99.0.100/24 dev "$iface" 2>/dev/null && {
            ip route add default via 10.99.0.1 dev "$iface" 2>/dev/null || true
            IFACE_UP="$iface"
            log "static: assigned 10.99.0.100/24 on $iface, gw 10.99.0.1"
            break
        }
    done
fi

log "net state:"
ip addr show 2>/dev/null >> "$LOG"
ip route show 2>/dev/null >> "$LOG"

# ── Step 6b: connectivity test (output goes to VGA via /dev/console) ──────────
log "=== NETWORK CONNECTIVITY TEST ==="
log "ping 10.99.0.1:"
ping -c3 -W2 10.99.0.1 2>&1 >> "$LOG"
log "curl connect test:"
curl -v --max-time 5 "http://10.99.0.1:8080/" 2>&1 | head -20 >> "$LOG"
log "=== END CONNECTIVITY TEST ==="

# ── Step 7: start log server so clonr-server can pull diagnostics ─────────────
# Serve /tmp/init.log via busybox httpd on port 9999.
# From clonr-server: curl http://10.99.0.100:9999/init.log
mkdir -p /tmp/www
ln -sf "$LOG" /tmp/www/init.log
httpd -p 9999 -h /tmp/www 2>/dev/null &
HTTPPID=$!
# Also try nc as fallback
(while true; do cat "$LOG" | nc -l -p 9998 2>/dev/null; done) &
log "log server: httpd :9999 (pid $HTTPPID), nc :9998"

# ── Step 8: run clonr deploy --auto ───────────────────────────────────────────
# Ensure PATH includes /usr/bin so exec.Command("lsblk",...) in Go can find it.
# busybox sh may not set a complete PATH by default, which would cause Go's
# os/exec.LookPath to fail silently, returning no disks.
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
export CLONR_SERVER="${CLONR_SERVER:-http://10.99.0.1:8080}"

# Parse clonr.token from /proc/cmdline — the PXE boot handler embeds a fresh
# node-scoped API key here at PXE-serve time. The deploy agent reads it via
# CLONR_TOKEN so that GetImage and DownloadBlob calls are authenticated.
# Bail out loudly if no token is present; silent unauthenticated fallback is
# exactly how we ended up with the v0.1.0 auth gap — do not allow it.
CLONR_TOKEN_RAW=$(cat /proc/cmdline | tr ' ' '\n' | grep '^clonr.token=' | cut -d= -f2- | tr -d '[:space:]')
if [ -z "$CLONR_TOKEN_RAW" ]; then
    log "FATAL: clonr.token not found in /proc/cmdline — refusing to deploy without auth"
    log "cmdline: $(cat /proc/cmdline)"
    log "This node needs a fresh PXE boot from a server running clonr v0.2.0+ which"
    log "embeds a node-scoped token at PXE-serve time."
    while true; do sleep 3600; done
fi
export CLONR_TOKEN="$CLONR_TOKEN_RAW"
log "clonr.token parsed from cmdline (length=${#CLONR_TOKEN_RAW})"

log "PATH: $PATH"
log "lsblk location: $(which lsblk 2>&1 || echo NOT_FOUND)"

# ── Step 8a: check for deferred deploy-complete flag ─────────────────────────
# If the previous boot's clonr wrote /tmp/clonr-deploy-success, the node was
# fully deployed on disk but the server state was not updated (transient network
# or server error). Re-send the deploy-complete report now, before running
# deploy --auto, so the server transitions to NodeStateDeployed and the PXE
# boot handler returns "exit" rather than triggering another deploy loop.
if [ -f /tmp/clonr-deploy-success ]; then
    NODE_ID=$(cat /tmp/clonr-deploy-success | tr -d '[:space:]')
    log "found /tmp/clonr-deploy-success (node_id=${NODE_ID}) — re-sending deploy-complete before entering deploy loop"
    RETRY=0
    REPORTED=0
    while [ $RETRY -lt 5 ]; do
        HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
            -H "Content-Type: application/json" \
            -H "Authorization: Bearer ${CLONR_TOKEN}" \
            "${CLONR_SERVER}/api/v1/nodes/${NODE_ID}/deploy-complete" 2>/dev/null)
        log "deploy-complete retry ${RETRY}: HTTP ${HTTP_STATUS}"
        if [ "${HTTP_STATUS}" = "200" ] || [ "${HTTP_STATUS}" = "204" ] || [ "${HTTP_STATUS}" = "201" ]; then
            log "deploy-complete re-send succeeded — removing flag file"
            rm -f /tmp/clonr-deploy-success
            REPORTED=1
            break
        fi
        RETRY=$((RETRY + 1))
        sleep 2
    done
    if [ $REPORTED -eq 0 ]; then
        log "WARNING: deploy-complete re-send failed after 5 attempts — proceeding with deploy --auto"
        log "(server may still transition the node; clonr register will handle it)"
    fi
fi

log "running: /usr/bin/clonr deploy --auto --server ${CLONR_SERVER} --token <redacted>"

/usr/bin/clonr deploy --auto --server "${CLONR_SERVER}" --token "${CLONR_TOKEN}" >> "$LOG" 2>&1
CLONR_EXIT=$?

log "clonr exit: $CLONR_EXIT"

if [ "$CLONR_EXIT" -eq 0 ]; then
    log "deployment succeeded — rebooting into deployed OS in 3s"
    sync
    sleep 3
    # reboot triggers the kernel to restart the machine. On BIOS/GPT systems
    # with scsi0 first in boot order, the next boot loads GRUB from the disk.
    reboot -f
else
    log "deployment failed (exit $CLONR_EXIT) — sleeping to allow log collection"
    log "(pull log: nc <node-ip> 9999)"
    # ── Step 9: loop on failure — PID 1 must not exit ─────────────────────────
    while true; do
        sleep 3600
    done
fi
