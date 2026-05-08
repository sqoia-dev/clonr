#!/bin/sh
# initramfs-init.sh — PID 1 init script for the clustr deploy initramfs.
#
# This file is a template: build-initramfs.sh substitutes placeholders at
# build time via sed before writing the final /init into the cpio archive.
#
# Placeholders substituted by build-initramfs.sh:
#   ${CLUSTR_SERVER}      — default server URL (e.g. http://10.99.0.1:8080)
#   ${CLUSTR_STATIC_BIN}  — path to the clustr static binary being embedded
#
# All other shell variables (${CLUSTR_TOKEN}, ${LOG}, etc.) are runtime
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
log " clustr initramfs init started"
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
CLUSTR_SERVER=""
CLUSTR_MAC=""
CLUSTR_MULTICAST=""
for arg in $(cat /proc/cmdline); do
    case $arg in
        clustr.server=*)    CLUSTR_SERVER="${arg#clustr.server=}" ;;
        clustr.mac=*)       CLUSTR_MAC="${arg#clustr.mac=}" ;;
        clustr.multicast=*) CLUSTR_MULTICAST="${arg#clustr.multicast=}" ;;
    esac
done
log "server    : ${CLUSTR_SERVER:-not set}"
log "mac       : ${CLUSTR_MAC:-auto-detect}"
log "multicast : ${CLUSTR_MULTICAST:-off}"

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
# (handled by the clustr deploy code itself).
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
    # Assign static IP so clustr-server can reach us to pull /tmp/init.log
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

# ── Step 7: start log server so clustr-server can pull diagnostics ─────────────
# Serve /tmp/init.log via busybox httpd on port 9999.
# From clustr-server: curl http://10.99.0.100:9999/init.log
mkdir -p /tmp/www
ln -sf "$LOG" /tmp/www/init.log
httpd -p 9999 -h /tmp/www 2>/dev/null &
HTTPPID=$!
# Also try nc as fallback
(while true; do cat "$LOG" | nc -l -p 9998 2>/dev/null; done) &
log "log server: httpd :9999 (pid $HTTPPID), nc :9998"

# ── Step 8: run clustr deploy --auto ───────────────────────────────────────────
# Ensure PATH includes /usr/bin so exec.Command("lsblk",...) in Go can find it.
# busybox sh may not set a complete PATH by default, which would cause Go's
# os/exec.LookPath to fail silently, returning no disks.
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
export CLUSTR_SERVER="${CLUSTR_SERVER:-http://10.99.0.1:8080}"

# Parse clustr.token from /proc/cmdline — the PXE boot handler embeds a fresh
# node-scoped API key here at PXE-serve time. The deploy agent reads it via
# CLUSTR_TOKEN so that GetImage and DownloadBlob calls are authenticated.
# Bail out loudly if no token is present; silent unauthenticated fallback is
# exactly how we ended up with the v0.1.0 auth gap — do not allow it.
CLUSTR_TOKEN_RAW=$(cat /proc/cmdline | tr ' ' '\n' | grep '^clustr.token=' | cut -d= -f2- | tr -d '[:space:]')
if [ -z "$CLUSTR_TOKEN_RAW" ]; then
    log "FATAL: clustr.token not found in /proc/cmdline — refusing to deploy without auth"
    log "cmdline: $(cat /proc/cmdline)"
    log "This node needs a fresh PXE boot from a server running clustr v0.2.0+ which"
    log "embeds a node-scoped token at PXE-serve time."
    while true; do sleep 3600; done
fi
export CLUSTR_TOKEN="$CLUSTR_TOKEN_RAW"
log "clustr.token parsed from cmdline (length=${#CLUSTR_TOKEN_RAW})"

log "PATH: $PATH"
log "lsblk location: $(which lsblk 2>&1 || echo NOT_FOUND)"

# ── Step 8a: check for deferred deploy-complete flag ─────────────────────────
# If the previous boot's clustr wrote /tmp/clustr-deploy-success, the node was
# fully deployed on disk but the server state was not updated (transient network
# or server error). Re-send the deploy-complete report now, before running
# deploy --auto, so the server transitions to NodeStateDeployed and the PXE
# boot handler returns "exit" rather than triggering another deploy loop.
if [ -f /tmp/clustr-deploy-success ]; then
    NODE_ID=$(cat /tmp/clustr-deploy-success | tr -d '[:space:]')
    log "found /tmp/clustr-deploy-success (node_id=${NODE_ID}) — re-sending deploy-complete before entering deploy loop"
    RETRY=0
    REPORTED=0
    while [ $RETRY -lt 5 ]; do
        HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
            -H "Content-Type: application/json" \
            -H "Authorization: Bearer ${CLUSTR_TOKEN}" \
            "${CLUSTR_SERVER}/api/v1/nodes/${NODE_ID}/deploy-complete" 2>/dev/null)
        log "deploy-complete retry ${RETRY}: HTTP ${HTTP_STATUS}"
        if [ "${HTTP_STATUS}" = "200" ] || [ "${HTTP_STATUS}" = "204" ] || [ "${HTTP_STATUS}" = "201" ]; then
            log "deploy-complete re-send succeeded — removing flag file"
            rm -f /tmp/clustr-deploy-success
            REPORTED=1
            break
        fi
        RETRY=$((RETRY + 1))
        sleep 2
    done
    if [ $REPORTED -eq 0 ]; then
        log "WARNING: deploy-complete re-send failed after 5 attempts — proceeding with deploy --auto"
        log "(server may still transition the node; clustr register will handle it)"
    fi
fi

# ── Step 8b: optional dropbear SSH + screen setup ────────────────────────────
# Gated by clustr.ssh=1 in the kernel cmdline. When enabled:
#   - dropbear SSH starts on port 22 with a per-boot password
#   - deploy agent runs inside a screen session named "clustr-deploy"
#   - operator can: ssh root@<node-ip>  then  screen -r clustr-deploy
ENABLE_SSH=$(cat /proc/cmdline | tr ' ' '\n' | grep '^clustr.ssh=' | cut -d= -f2 | tr -d '[:space:]' | head -1)
# Generate a random per-boot 8-char hex password. Do NOT log the password value.
# Any clustr.ssh.pass= on the cmdline is intentionally ignored to prevent passwords
# appearing in kernel cmdline logs (dmesg, serial console, PXE server logs).
#
# Use tr+head on /dev/urandom — both are busybox applets already symlinked in the
# initramfs. 'od' is NOT a busybox applet and is not present in the initramfs, so
# the previous dd|od pipeline produced "od: not found" and an empty SSH_PASS.
SSH_PASS=$(tr -dc 'a-f0-9' < /dev/urandom 2>/dev/null | head -c 8)
if [ -z "$SSH_PASS" ]; then
    # Fallback if /dev/urandom is unavailable: derive from /proc/uptime nanoseconds
    SSH_PASS=$(cat /proc/uptime 2>/dev/null | tr -d '.' | cut -c1-8)
fi

if [ "$ENABLE_SSH" = "1" ] && [ -x /usr/sbin/dropbear ]; then
    log "--- SSH debug access enabled ---"
    # Password intentionally not logged — retrieve from node console/serial only.

    # Generate an ephemeral ed25519 host key.
    mkdir -p /etc/dropbear
    if [ -x /usr/sbin/dropbearkey ]; then
        /usr/sbin/dropbearkey -t ed25519 -f /etc/dropbear/dropbear_ed25519_host_key 2>/dev/null && \
            log "  host key : generated"
    fi

    # Write root password to /etc/shadow so dropbear can authenticate.
    # busybox sh includes the 'passwd' applet on most builds; fall back to
    # direct /etc/shadow manipulation if it is not available.
    SHADOW_WRITTEN=0
    if command -v passwd >/dev/null 2>&1; then
        printf '%s\n%s\n' "$SSH_PASS" "$SSH_PASS" | passwd root 2>/dev/null && SHADOW_WRITTEN=1
    fi
    if [ "$SHADOW_WRITTEN" -eq 0 ] && command -v openssl >/dev/null 2>&1; then
        SALT=$(head -c 8 /dev/urandom 2>/dev/null | base64 2>/dev/null | tr -d '/+=\n' | head -c 8)
        HASH=$(printf '%s' "$SSH_PASS" | openssl passwd -1 -salt "$SALT" -stdin 2>/dev/null)
        if [ -n "$HASH" ] && [ -f /etc/shadow ]; then
            sed -i "s|^root:[^:]*:|root:${HASH}:|" /etc/shadow 2>/dev/null && SHADOW_WRITTEN=1
        fi
    fi
    if [ "$SHADOW_WRITTEN" -eq 0 ]; then
        log "  WARNING: could not set root password — password auth will fail"
    fi

    # Start dropbear: -E log to stderr, -B allow blank-password root if needed,
    # -p 22, run in background.
    /usr/sbin/dropbear -E -B -p 22 2>>"$LOG" &
    DROPBEAR_PID=$!
    log "  dropbear PID=$DROPBEAR_PID started on :22"
    NODE_IP=$(ip addr show 2>/dev/null | grep 'inet ' | grep -v '127\.' | head -1 | awk '{print $2}' | cut -d/ -f1)
    log "  SSH enabled on port 22 : ssh root@${NODE_IP:-<node-ip>}"
    log "  (password available via serial/IPMI console only — not logged)"
    log "  Then: screen -r clustr-deploy"
    log "--- end SSH info ---"
fi

# ── Step 8c: export multicast session parameters (if present) ────────────────
# When the PXE boot handler embedded multicast params in the kernel cmdline,
# export them so the clustr deploy agent (runAutoDeployMode) can:
#   1. Call POST /multicast/enqueue to join the session
#   2. Long-poll GET /multicast/sessions/{id}/wait for the session descriptor
#   3. Fork udp-receiver to receive the image stream
#   4. Fall back to unicast HTTP if udp-receiver fails or session falls back
#
# These are read by runAutoDeployMode in cmd/clustr/main.go.
if [ "$CLUSTR_MULTICAST" = "1" ]; then
    if [ -x /usr/bin/udp-receiver ]; then
        export CLUSTR_MULTICAST_ENABLED=1
        log "multicast: enabled — udp-receiver present"
    else
        log "multicast: WARNING: udp-receiver not found in initramfs — will fall back to unicast"
        export CLUSTR_MULTICAST_ENABLED=0
    fi
fi

log "running: /usr/bin/clustr deploy --auto --server ${CLUSTR_SERVER} --token <redacted>"

if [ "$ENABLE_SSH" = "1" ] && [ -x /usr/bin/screen ]; then
    # Run deploy agent inside a named screen session so the operator can attach.
    # The screen session runs in detached mode (-dm). We block by polling until
    # the session exits, then read the exit code from /tmp/clustr-exit-code.
    #
    # Output is redirected directly to $LOG (no pipe-to-tee) to avoid PTY
    # backpressure: when screen's internal scrollback buffer fills up in
    # detached mode it stops reading the PTY master, which stalls the PTY
    # slave write, which blocks the pipe, which blocks clustr's stderr write
    # before the first byte reaches the log.  Appending directly to $LOG
    # bypasses the PTY entirely — screen still provides an attachable pty for
    # live inspection via "screen -r clustr-deploy", but log I/O is independent.
    log "starting deploy agent in screen session 'clustr-deploy'..."
    # SCREENDIR: screen needs a writable directory for its socket files.
    # Without it screen falls back to $HOME/.screen; /root exists in the initramfs
    # but setting SCREENDIR=/tmp/screen is safer and avoids any HOME lookup.
    # TERM: screen requires a terminal type; vt100 is universally safe in a minimal
    # initramfs environment and is always available without terminfo databases.
    export SCREENDIR=/tmp/screen
    mkdir -p "$SCREENDIR"
    chmod 700 "$SCREENDIR"
    export TERM="${TERM:-vt100}"
    screen -dmS clustr-deploy sh -c \
        "/usr/bin/clustr deploy --auto --server \"${CLUSTR_SERVER}\" --token \"${CLUSTR_TOKEN}\" >> \"$LOG\" 2>&1; echo \$? > /tmp/clustr-exit-code; exec sh"
    # Wait for the deploy to write its exit code.
    WAITED=0
    while [ ! -f /tmp/clustr-exit-code ]; do
        sleep 5
        WAITED=$((WAITED + 5))
        # Log a heartbeat every 60 seconds so the log server shows progress.
        case "$WAITED" in
            60|120|180|300|600|900|1200|1800) log "  deploy in progress (${WAITED}s elapsed)..." ;;
        esac
    done
    CLUSTR_EXIT=$(cat /tmp/clustr-exit-code 2>/dev/null | tr -d '[:space:]')
    CLUSTR_EXIT="${CLUSTR_EXIT:-1}"
else
    /usr/bin/clustr deploy --auto --server "${CLUSTR_SERVER}" --token "${CLUSTR_TOKEN}" >> "$LOG" 2>&1
    CLUSTR_EXIT=$?
fi

log "clustr exit: $CLUSTR_EXIT"

if [ "$CLUSTR_EXIT" -eq 0 ]; then
    log "deployment succeeded — rebooting into deployed OS in 3s"
    sync
    sleep 3
    # reboot triggers PXE again (net0 is always first in boot order).
    # clustr serves an iPXE disk-boot script that chains grub.efi from the server.
    reboot -f
else
    log "deployment failed (exit $CLUSTR_EXIT) — sleeping to allow log collection"
    if [ "$ENABLE_SSH" = "1" ] && [ -x /usr/sbin/dropbear ]; then
        log "SSH still active — connect to inspect: ssh root@<node-ip>"
        log "(password visible on serial/IPMI console only)"
        log "Attach to deploy session: screen -r clustr-deploy"
    else
        log "(pull log: nc <node-ip> 9999)"
    fi
    # ── Step 9: loop on failure — PID 1 must not exit ─────────────────────────
    while true; do
        sleep 3600
    done
fi
