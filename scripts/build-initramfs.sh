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
for tool in cpio gzip sshpass; do
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
mkdir -p "$WORKDIR"/{bin,sbin,dev,proc,sys,etc,run,tmp,var/log,mnt}
mkdir -p "$WORKDIR"/usr/{bin,sbin,share/udhcpc,lib64}
mkdir -p "$WORKDIR"/lib64
mkdir -p "$WORKDIR"/usr/lib64
mkdir -p "$WORKDIR"/usr/lib64/systemd    # for libsystemd-shared (udevadm dep)
mkdir -p "$WORKDIR"/usr/lib/grub         # grub2 platform modules
mkdir -p "$WORKDIR"/usr/share/grub       # grub2 locale data

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
# The binary is ALWAYS installed as exactly /usr/bin/clonr regardless of the
# source name (e.g. clonr-static, clonr-linux-amd64).  The init script exec
# line is hardcoded to /usr/bin/clonr — any mismatch here would cause the
# deploy agent to not be found at runtime.
CLONR_INSTALLED_PATH="$WORKDIR/usr/bin/clonr"
cp "$CLONR_BIN" "$CLONR_INSTALLED_PATH"
chmod 755 "$CLONR_INSTALLED_PATH"

echo "  [+] Installed clonr binary as /usr/bin/clonr ($(du -h "$CLONR_BIN" | cut -f1), src=$(basename "$CLONR_BIN"))"

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

# ──────────────────────────────────────────────────────────────────────────────
# Install lsblk.
#
# lsblk is not a busybox applet — it comes from util-linux. Without it, clonr's
# hardware discovery returns an empty disk list and disk selection during deploy
# fails. We fetch the binary directly from the clonr-server, which already has
# util-linux installed.
#
# Strategy (in order):
#   1. Fetch static lsblk from clonr-server at /usr/bin/lsblk (preferred).
#   2. If the server binary is dynamically linked, copy it plus its required
#      shared libraries from the server.
#   3. If sshpass/server is unavailable, check the local system for a static
#      lsblk binary (e.g. util-linux-static package on Debian/Ubuntu).
# ──────────────────────────────────────────────────────────────────────────────
echo "  [+] Installing lsblk..."

LSBLK_INSTALLED=false
LSBLK_DEST="$WORKDIR/usr/bin/lsblk"

# Helper: try to fetch lsblk from the clonr-server.
fetch_lsblk_from_server() {
    if ! command -v sshpass &>/dev/null; then
        echo "      sshpass not found — cannot fetch lsblk from server" >&2
        return 1
    fi

    # Copy the binary.
    if ! sshpass -p "$CLONR_SERVER_PASS" scp -o StrictHostKeyChecking=no \
        "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}:/usr/bin/lsblk" \
        "$LSBLK_DEST" 2>/dev/null; then
        echo "      failed to scp lsblk from ${CLONR_SERVER_HOST}" >&2
        return 1
    fi
    chmod 755 "$LSBLK_DEST"

    # Determine if the binary is statically linked.
    LSBLK_FILE_INFO=$(file "$LSBLK_DEST" 2>/dev/null || echo "")
    if echo "$LSBLK_FILE_INFO" | grep -q "statically linked"; then
        echo "      fetched static lsblk from ${CLONR_SERVER_HOST}"
        return 0
    fi

    # Dynamically linked — copy required shared libraries from the server.
    echo "      lsblk is dynamically linked — fetching required libs..."
    NEEDED_LIBS=$(sshpass -p "$CLONR_SERVER_PASS" ssh -o StrictHostKeyChecking=no \
        "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}" "ldd /usr/bin/lsblk 2>/dev/null" | \
        grep -oP '/[^ ]+\.so[^ ]*' | sort -u 2>/dev/null || true)

    if [[ -z "$NEEDED_LIBS" ]]; then
        echo "      WARNING: could not determine lsblk dependencies" >&2
        return 0  # keep the binary anyway, it may work if libs are already present
    fi

    for lib in $NEEDED_LIBS; do
        lib_dir="$WORKDIR$(dirname "$lib")"
        mkdir -p "$lib_dir"
        sshpass -p "$CLONR_SERVER_PASS" scp -o StrictHostKeyChecking=no \
            "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}:${lib}" \
            "${lib_dir}/$(basename "$lib")" 2>/dev/null || \
            echo "      WARNING: could not fetch lib $lib" >&2
    done

    # Set up /lib64/ld-linux-x86-64.so.2 symlink if needed (glibc dynamic linker).
    if [[ ! -e "$WORKDIR/lib64/ld-linux-x86-64.so.2" ]]; then
        LINKER=$(sshpass -p "$CLONR_SERVER_PASS" ssh -o StrictHostKeyChecking=no \
            "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}" \
            "readlink -f /lib64/ld-linux-x86-64.so.2 2>/dev/null" 2>/dev/null || echo "")
        if [[ -n "$LINKER" ]]; then
            mkdir -p "$WORKDIR/lib64"
            sshpass -p "$CLONR_SERVER_PASS" scp -o StrictHostKeyChecking=no \
                "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}:${LINKER}" \
                "$WORKDIR/lib64/ld-linux-x86-64.so.2" 2>/dev/null || true
        fi
    fi

    echo "      fetched dynamic lsblk + libs from ${CLONR_SERVER_HOST}"
    return 0
}

if fetch_lsblk_from_server; then
    LSBLK_INSTALLED=true
else
    # Fallback: check for a locally installed static lsblk.
    # util-linux-static (Debian/Ubuntu) or util-linux-ng-static (some distros)
    # installs a statically linked lsblk at /usr/bin/lsblk.static or similar.
    for candidate in /usr/bin/lsblk.static /usr/lib/util-linux/lsblk \
                     /usr/bin/lsblk /sbin/lsblk; do
        if [[ -f "$candidate" ]]; then
            FILE_INFO=$(file "$candidate" 2>/dev/null || echo "")
            if echo "$FILE_INFO" | grep -q "statically linked"; then
                cp "$candidate" "$LSBLK_DEST"
                chmod 755 "$LSBLK_DEST"
                echo "      using local static lsblk: $candidate"
                LSBLK_INSTALLED=true
                break
            fi
        fi
    done
fi

if [[ "$LSBLK_INSTALLED" == "true" ]]; then
    echo "  [+] lsblk installed at /usr/bin/lsblk ($(du -h "$LSBLK_DEST" | cut -f1))"
else
    echo "  [!] WARNING: lsblk could not be installed — disk discovery will return empty results" >&2
    echo "               Run: sshpass -p clonr scp clonr@192.168.1.151:/usr/bin/lsblk initramfs-lsblk && rebuild" >&2
fi

# ──────────────────────────────────────────────────────────────────────────────
# Install deployment tools from clonr-server.
#
# The initramfs must be able to partition disks and create filesystems during
# deployment. Without these binaries, sgdisk/mkfs calls fail silently and the
# deploy loop hangs after "starting image write" with zero disk writes.
#
# Tools required:
#   sgdisk        — GPT partitioning (from gdisk)
#   mkfs.xfs      — XFS filesystem creation (from xfsprogs)
#   mkfs.ext4     — ext4 filesystem creation (from e2fsprogs)
#   mkfs.vfat     — FAT32 filesystem creation (for EFI partitions, from dosfstools)
#   partprobe     — tell kernel about new partition table (from parted)
#   tar           — GNU tar for archive extraction (busybox tar can't handle .tar.gz reliably)
#   gzip          — full gzip for decompression (busybox gzip is limited)
#   rsync         — file syncing (optional but common in deploy scripts)
#   blockdev      — get disk size (from util-linux)
#   mkswap        — swap partition creation (from util-linux)
#
# Strategy: SSH to clonr-server, copy binaries + their shared libraries.
# We use ldd on the server to find all required .so files and scp them over.
# ──────────────────────────────────────────────────────────────────────────────
echo "  [+] Installing deployment tools from ${CLONR_SERVER_HOST}..."

if ! command -v sshpass &>/dev/null; then
    echo "  [!] WARNING: sshpass not found — cannot fetch deployment tools from server" >&2
else
    # ── Shared library helper ─────────────────────────────────────────────────────
    # collect_libs_for_binary <remote_binary_path>
    # Emits a newline-separated, deduplicated list of all .so paths needed by
    # the binary, including transitive deps of any libs it pulls in.
    # We do two rounds: first-order ldd on the binary, then ldd on each unique
    # lib to catch transitive deps (e.g. libsystemd-shared → many more libs).
    collect_libs_for_binary() {
        local remote_path="$1"
        # First-order libs
        local first_order
        first_order=$(sshpass -p "$CLONR_SERVER_PASS" ssh -o StrictHostKeyChecking=no \
            "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}" \
            "ldd ${remote_path} 2>/dev/null" 2>/dev/null | \
            grep -oP '/[^ ]+\.so[^ ]*' | sort -u || true)

        # Collect unique libs across binary + transitive layer
        local all_libs="$first_order"
        for lib in $first_order; do
            local transitive
            transitive=$(sshpass -p "$CLONR_SERVER_PASS" ssh -o StrictHostKeyChecking=no \
                "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}" \
                "ldd ${lib} 2>/dev/null" 2>/dev/null | \
                grep -oP '/[^ ]+\.so[^ ]*' | sort -u || true)
            all_libs="${all_libs}
${transitive}"
        done

        echo "$all_libs" | grep -v '^$' | sort -u
    }

    # Helper: copy a binary + all its shared libs (including transitive deps)
    # from the server into the initramfs.
    install_server_binary() {
        local remote_path="$1"
        local dest_dir="${2:-$WORKDIR/usr/sbin}"
        local bin_name
        bin_name=$(basename "$remote_path")

        # Copy the binary.
        mkdir -p "$dest_dir"
        if ! sshpass -p "$CLONR_SERVER_PASS" scp -o StrictHostKeyChecking=no \
            "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}:${remote_path}" \
            "${dest_dir}/${bin_name}" 2>/dev/null; then
            echo "      WARNING: could not fetch ${remote_path}" >&2
            return 1
        fi
        chmod 755 "${dest_dir}/${bin_name}"

        # Check if static — if so, we're done.
        local file_info
        file_info=$(file "${dest_dir}/${bin_name}" 2>/dev/null || echo "")
        if echo "$file_info" | grep -q "statically linked"; then
            echo "      fetched static binary: ${bin_name}"
            return 0
        fi

        # Dynamically linked: fetch all required shared libraries including
        # transitive deps (e.g. udevadm pulls in libsystemd-shared which has
        # its own large dep set that a single-pass ldd would miss).
        local needed_libs
        needed_libs=$(collect_libs_for_binary "$remote_path")

        for lib in $needed_libs; do
            local lib_dir
            lib_dir="$WORKDIR$(dirname "$lib")"
            mkdir -p "$lib_dir"
            # scp the real file (resolving symlinks on the server side).
            # We need the soname symlink too so the dynamic linker finds it by
            # the name embedded in the binary's NEEDED entries.
            local real_lib
            real_lib=$(sshpass -p "$CLONR_SERVER_PASS" ssh -o StrictHostKeyChecking=no \
                "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}" \
                "readlink -f ${lib} 2>/dev/null || echo ${lib}" 2>/dev/null || echo "$lib")
            sshpass -p "$CLONR_SERVER_PASS" scp -o StrictHostKeyChecking=no \
                "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}:${real_lib}" \
                "${lib_dir}/$(basename "$lib")" 2>/dev/null || \
                echo "      WARNING: could not fetch lib ${lib}" >&2
        done

        # Ensure the dynamic linker itself is present under /lib64/
        if [[ ! -e "$WORKDIR/lib64/ld-linux-x86-64.so.2" ]]; then
            local linker
            linker=$(sshpass -p "$CLONR_SERVER_PASS" ssh -o StrictHostKeyChecking=no \
                "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}" \
                "readlink -f /lib64/ld-linux-x86-64.so.2 2>/dev/null" 2>/dev/null || echo "")
            if [[ -n "$linker" ]]; then
                mkdir -p "$WORKDIR/lib64"
                sshpass -p "$CLONR_SERVER_PASS" scp -o StrictHostKeyChecking=no \
                    "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}:${linker}" \
                    "$WORKDIR/lib64/ld-linux-x86-64.so.2" 2>/dev/null || true
            fi
        fi

        echo "      fetched dynamic binary + libs: ${bin_name}"
        return 0
    }

    # Find each binary on the server and install it.
    # Uses 'which' to resolve the canonical path (handles /sbin vs /usr/sbin etc.)
    find_and_install_bin() {
        local bin_name="$1"
        local dest_dir="${2:-$WORKDIR/usr/sbin}"
        local remote_path
        remote_path=$(sshpass -p "$CLONR_SERVER_PASS" ssh -o StrictHostKeyChecking=no \
            "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}" \
            "which ${bin_name} 2>/dev/null || command -v ${bin_name} 2>/dev/null" 2>/dev/null || echo "")
        if [[ -z "$remote_path" ]]; then
            echo "      WARNING: ${bin_name} not found on ${CLONR_SERVER_HOST}" >&2
            return 1
        fi
        install_server_binary "$remote_path" "$dest_dir"
    }

    # ── Disk tools → /usr/sbin ───────────────────────────────────────────────────
    # These binaries live in /usr/sbin on Rocky 9 and are called via the PATH
    # that the init script sets: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:...
    DEPLOY_TOOLS_SBIN=(
        mdadm           # Linux Software RAID management — required for RAID disk layouts
                        # (raid1/5/6 over multiple disks; seabios-vm206 uses md0 over sda+sdb)
        sgdisk          # GPT partition table creation (gdisk package)
        mkfs.xfs        # XFS filesystem creation (xfsprogs)
        mkfs.ext4       # ext4 filesystem creation (e2fsprogs)
        mkfs.vfat       # FAT32 for EFI System Partition (dosfstools)
        partprobe       # kernel partition re-read after sgdisk (parted package)
        partx           # force /dev partition node creation via BLKPG ioctl (util-linux)
        wipefs          # wipe existing filesystem signatures before re-partitioning
        blockdev        # sector count / disk size queries (util-linux)
        mkswap          # swap partition setup (util-linux)
        blkid           # UUID lookup for fstab generation (util-linux) — finalize.go getUUID()
        grub2-install   # bootloader install into deployed OS MBR/EFI (chroot use)
        grub2-mkconfig  # generate /boot/grub2/grub.cfg inside chroot
        fsfreeze        # force filesystem log commit before unmount (prevents EBUSY on umount)
        efibootmgr      # EFI NVRAM BootOrder management — SetPXEBootFirst after finalize
    )
    for tool in "${DEPLOY_TOOLS_SBIN[@]}"; do
        find_and_install_bin "$tool" "$WORKDIR/usr/sbin" || true
    done

    # ── util-linux mount/umount → /usr/sbin ──────────────────────────────────────
    # busybox umount does NOT support -R (recursive unmount). clonr's unmountAll()
    # calls `umount -R <mountRoot>` which requires util-linux's umount. Without it,
    # busybox silently ignores -R and returns exit 0, leaving XFS partitions mounted
    # and causing all subsequent finalize operations to fail with EBUSY.
    #
    # busybox mount also does not support --make-rprivate (shared peer propagation
    # severing). Fetch util-linux mount+umount and install them into /usr/sbin so
    # they appear earlier in PATH than the busybox symlinks in /bin.
    for mntool in mount umount; do
        find_and_install_bin "$mntool" "$WORKDIR/usr/sbin" || true
    done

    # ── GNU userland → /usr/bin ──────────────────────────────────────────────────
    # tar: busybox tar cannot reliably handle .tar.gz with large files or extended
    # headers. GNU tar at /usr/bin/tar overrides the busybox symlink at /bin/tar.
    # gzip: similarly, GNU gzip handles multi-stream and large files correctly.
    # pigz: parallel gzip — uses all CPU cores for decompression of .tar.gz images.
    #       clonr's streamExtract() prefers pigz over gzip when available.
    # zstd: zstandard — 3-5x faster decompression than gzip at similar ratio.
    #       clonr stores new captures as .tar.zst and detects the magic bytes at
    #       deploy time. zstd binary must be in PATH for .tar.zst extraction.
    # rsync: used for incremental deploys; not in busybox.
    # udevadm: 'udevadm settle' flushes kernel uevents after partprobe so that
    # /dev/sda1 etc. exist before we try to mkfs them. Lives in /usr/bin on Rocky 9.
    DEPLOY_TOOLS_BIN=(
        tar             # GNU tar (image extraction — .tar.gz / .tar.zst)
        gzip            # GNU gzip (decompression fallback)
        pigz            # parallel gzip — multi-core decompression of .tar.gz images
        zstd            # zstandard — fast decompression of .tar.zst images
        rsync           # incremental deploy sync
        udevadm         # device settle after partition table changes
        curl            # HTTP client for deploy-complete POST and connectivity tests
                        # (requires shared libs: libcurl, libssl, libcrypto, libz, etc.)
                        # busybox wget cannot do POST with Authorization headers or
                        # capture HTTP response codes via -w "%{http_code}"
    )
    for tool in "${DEPLOY_TOOLS_BIN[@]}"; do
        find_and_install_bin "$tool" "$WORKDIR/usr/bin" || true
    done

    # ── grub2 module data ────────────────────────────────────────────────────────
    # grub2-install reads platform modules from /usr/lib/grub/<platform>/ and
    # locale data from /usr/share/grub/. Without these, grub2-install fails with
    # "cannot find a GRUB drive for /dev/...".
    # Strategy: we use grub2-install in a chroot (chroot /mnt grub2-install /dev/sdX)
    # so ideally these come from the deployed image. However we also copy them into
    # the initramfs so grub2-install can fall back if the chroot path is missing.
    #
    # scp -r <host>:<dir> <local_parent>  — copies the dir INTO local_parent,
    # creating local_parent/<basename(dir)>/. We scp to the PARENT of the target
    # so the directory structure is preserved correctly.
    echo "      fetching grub2 module data from ${CLONR_SERVER_HOST}..."
    for grub_dir in /usr/lib/grub /usr/share/grub; do
        # Parent dir inside initramfs (e.g. $WORKDIR/usr/lib for /usr/lib/grub)
        local_parent="$WORKDIR$(dirname "$grub_dir")"
        mkdir -p "$local_parent"
        if sshpass -p "$CLONR_SERVER_PASS" scp -o StrictHostKeyChecking=no -r \
            "${CLONR_SERVER_USER}@${CLONR_SERVER_HOST}:${grub_dir}" \
            "${local_parent}/" 2>/dev/null; then
            echo "      fetched ${grub_dir} ($(du -sh "${local_parent}/$(basename "$grub_dir")" 2>/dev/null | cut -f1))"
        else
            echo "      WARNING: could not fetch ${grub_dir}" >&2
        fi
    done

    echo "  [+] Deployment tools installed"
fi

# Create symlinks for all busybox applets we need.
# NOTE: which, ping, reboot, sync, touch, seq are busybox applets — they must
# be explicitly symlinked or the init script fails with "command not found".
# which: used in init for `which lsblk` diagnostics
# ping:  used in init for connectivity test
# reboot: used in init after successful deploy
# sync:  used in init before reboot
# touch: used in init for LOG file creation
# seq:   used in init for the block-device wait loop
for cmd in sh ash ls cat echo mount umount mkdir cp mv rm ip \
           ifconfig udhcpc modprobe insmod sleep printf \
           grep sed awk cut tr head tail tee wc df free uname dmesg \
           basename dirname readlink ln \
           httpd nc netcat \
           mdev switch_root pivot_root chroot \
           which ping reboot sync touch seq; do
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
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/drivers/scsi"
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/drivers/block"
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/drivers/md"
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/fs/xfs"
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/fs/ext4"
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/fs/jbd2"
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/fs/fat"
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/lib"
    mkdir -p "$WORKDIR/lib/modules/$KVER/kernel/arch/x86/crypto"

    # List of module paths relative to /lib/modules/$KVER/kernel/
    # Network: failover → net_failover → virtio_net
    # Storage (virtio-scsi-pci controller, e.g. Proxmox scsi0 with scsihw=virtio-scsi-pci):
    #   virtio_scsi  — the HBA driver; makes the SCSI bus visible to the kernel
    #   sd_mod       — the SCSI disk driver; turns a SCSI target into /dev/sdX
    #                  Without sd_mod, virtio_scsi sees the device but never creates
    #                  the block node, so /sys/class/block/ stays empty.
    # Storage (virtio block device, e.g. Proxmox virtio0):
    #   virtio_blk   — direct virtio block driver, creates /dev/vdX
    # Filesystems and their deps:
    #   crc32c-intel — hardware CRC32C acceleration (required by xfs, libcrc32c)
    #   libcrc32c    — software CRC32C library (required by xfs)
    #   xfs          — XFS filesystem (required for mount after mkfs.xfs)
    #   mbcache      — meta-data block cache (required by ext4)
    #   jbd2         — journaling block device (required by ext4)
    #   ext4         — ext4 filesystem
    #   fat          — FAT/vFAT base layer (required by vfat; no deps)
    #   vfat         — vFAT filesystem (required for ESP/EFI System Partition mount)
    # Software RAID (md):
    #   raid1        — RAID1 (mirror) personality; required for nodes with RAID1 disk layouts
    #   raid0        — RAID0 (stripe) personality; included for completeness
    #   raid10       — RAID10 personality
    #   raid456      — RAID4/5/6 personalities
    # NOTE: md_mod is typically built-in to Rocky 9 kernel. If RUN_ARRAY fails with
    # ENODEV, it means raid1.ko (the personality) is not loaded — insmod it here.
    MODULES=(
        "net/core/failover.ko.xz"
        "drivers/net/net_failover.ko.xz"
        "drivers/net/virtio_net.ko.xz"
        "drivers/scsi/virtio_scsi.ko.xz"
        "drivers/scsi/sd_mod.ko.xz"
        "drivers/block/virtio_blk.ko.xz"
        "arch/x86/crypto/crc32c-intel.ko.xz"
        "lib/libcrc32c.ko.xz"
        "fs/xfs/xfs.ko.xz"
        "fs/mbcache.ko.xz"
        "fs/jbd2/jbd2.ko.xz"
        "fs/ext4/ext4.ko.xz"
        "fs/fat/fat.ko.xz"
        "fs/fat/vfat.ko.xz"
        "drivers/md/raid1.ko.xz"
        "drivers/md/raid0.ko.xz"
        "drivers/md/raid10.ko.xz"
        "drivers/md/raid456.ko.xz"
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
kernel/drivers/scsi/virtio_scsi.ko:
kernel/drivers/scsi/sd_mod.ko:
kernel/drivers/block/virtio_blk.ko:
kernel/arch/x86/crypto/crc32c-intel.ko:
kernel/lib/libcrc32c.ko: kernel/arch/x86/crypto/crc32c-intel.ko
kernel/fs/xfs/xfs.ko: kernel/lib/libcrc32c.ko
kernel/fs/mbcache.ko:
kernel/fs/jbd2/jbd2.ko:
kernel/fs/ext4/ext4.ko: kernel/fs/mbcache.ko kernel/fs/jbd2/jbd2.ko
kernel/fs/fat/fat.ko:
kernel/fs/fat/vfat.ko: kernel/fs/fat/fat.ko
kernel/drivers/md/raid1.ko:
kernel/drivers/md/raid0.ko:
kernel/drivers/md/raid10.ko:
kernel/drivers/md/raid456.ko:
MODDEP

    cat > "$MODDEP_DIR/modules.alias" << MODALIAS
alias virtio:d00000001v* virtio_net
alias virtio:d00000008v* virtio_scsi
alias virtio:d00000002v* virtio_blk
alias scsi:t-0x00* sd_mod
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
        # Bring interface up first
        ip link set "$interface" up 2>/dev/null || true
        # Flush old addresses
        ip addr flush dev "$interface" 2>/dev/null || true
        # Assign IP: try full iproute2 first, fall back to ifconfig.
        # ifconfig is preferred because busybox's 'ip addr add' may not
        # automatically create the connected (subnet) route, causing
        # "Network is unreachable" for same-subnet hosts.
        if ifconfig "$interface" "$ip" netmask "${mask:-255.255.255.0}" 2>/dev/null; then
            echo "udhcpc: ifconfig bound ${ip} netmask ${mask} on ${interface}"
        else
            ip addr add "${ip}/${PREFIX}" dev "$interface" 2>/dev/null
            # Explicitly add the connected subnet route (busybox 'ip' may omit this)
            # Compute network address from ip and mask
            echo "udhcpc: ip addr add bound ${ip}/${PREFIX} on ${interface}"
        fi
        # Add default gateway
        [ -n "$router" ] && ip route add default via "$router" dev "$interface" 2>/dev/null || true
        # Update resolv.conf
        [ -n "$dns" ] && {
            > /etc/resolv.conf
            for d in $dns; do echo "nameserver $d" >> /etc/resolv.conf; done
        }
        echo "udhcpc: bound ${ip}/${PREFIX} gw=${router:-none} on ${interface}"
        # Show resulting network state for diagnostics
        ip addr show dev "$interface" 2>/dev/null || true
        ip route show 2>/dev/null || true
        ;;
    deconfig)
        ip addr flush dev "$interface" 2>/dev/null || true
        ifconfig "$interface" 0.0.0.0 2>/dev/null || true
        ;;
esac
exit 0
UDHCPC_EOF
chmod 755 "$WORKDIR/usr/share/udhcpc/default.script"

# init script — runs as PID 1 in the initramfs.
# The template file (scripts/initramfs-init.sh) uses ${CLONR_SERVER} and
# ${CLONR_STATIC_BIN} as placeholders that are substituted here via sed.
# All other variables (${LOG}, ${CLONR_TOKEN}, etc.) are runtime variables
# resolved inside the initramfs — they are intentionally left as-is.
CLONR_STATIC_BIN="$CLONR_BIN"
# WARNING: Default uses plain HTTP on the provisioning network.
# For environments where provisioning network is not fully trusted,
# configure TLS on clonr-serverd and set:
#   CLONR_SERVER="https://10.99.0.1:8443"
# The initramfs curl will need the CA cert embedded — see docs/tls-provisioning.md
# Substitute runtime variables into init script
sed -e "s|\${CLONR_SERVER}|${CLONR_SERVER:-http://10.99.0.1:8080}|g" \
    -e "s|\${CLONR_STATIC_BIN}|${CLONR_STATIC_BIN}|g" \
    "$(dirname "$0")/initramfs-init.sh" > "$WORKDIR/init"
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

# ──────────────────────────────────────────────────────────────────────────────
# Validation: verify every command the init script invokes is present in the
# staging rootfs. Fail loudly if anything is missing — catches future drift.
# We check each command under its expected PATH locations.
# ──────────────────────────────────────────────────────────────────────────────
echo "Validating initramfs binary coverage..."
VALIDATION_FAILED=0

# Commands the init script calls, mapped to where they should live in the rootfs.
# Format: "command:path1,path2,..." — any one match is sufficient.
REQUIRED_CMDS=(
    "sh:/bin/sh,/usr/bin/sh"
    "mount:/bin/mount,/usr/sbin/mount"
    "umount:/bin/umount,/usr/sbin/umount"
    "mkdir:/bin/mkdir"
    "cat:/bin/cat"
    "echo:/bin/echo"
    "grep:/bin/grep"
    "head:/bin/head"
    "tail:/bin/tail"
    "tr:/bin/tr"
    "cut:/bin/cut"
    "seq:/bin/seq"
    "touch:/bin/touch"
    "ln:/bin/ln"
    "ls:/bin/ls"
    "rm:/bin/rm"
    "sleep:/bin/sleep"
    "uname:/bin/uname"
    "dmesg:/bin/dmesg"
    "ip:/bin/ip,/usr/sbin/ip"
    "ifconfig:/bin/ifconfig"
    "udhcpc:/bin/udhcpc"
    "insmod:/bin/insmod"
    "ping:/bin/ping"
    "which:/bin/which"
    "sync:/bin/sync"
    "reboot:/bin/reboot"
    "httpd:/bin/httpd"
    "nc:/bin/nc"
    "mdev:/bin/mdev"
    "basename:/bin/basename"
    "lsblk:/usr/bin/lsblk"
    "curl:/usr/bin/curl"
    "mdadm:/usr/sbin/mdadm,/sbin/mdadm"
    "clonr:/usr/bin/clonr"
)

for entry in "${REQUIRED_CMDS[@]}"; do
    cmd="${entry%%:*}"
    paths="${entry#*:}"
    found=false
    IFS=',' read -ra path_list <<< "$paths"
    for p in "${path_list[@]}"; do
        local_path="$WORKDIR$p"
        if [[ -L "$local_path" ]]; then
            # Symlink — resolve target relative to WORKDIR (absolute symlinks like
            # /bin/busybox must be resolved within the staging tree, not the host).
            link_target=$(readlink "$local_path")
            if [[ "$link_target" = /* ]]; then
                # Absolute symlink — target is relative to WORKDIR
                resolved="$WORKDIR$link_target"
            else
                # Relative symlink — target is relative to the symlink's directory
                resolved="$(dirname "$local_path")/$link_target"
            fi
            if [[ -f "$resolved" && -x "$resolved" ]]; then
                found=true
                break
            fi
        elif [[ -f "$local_path" && -x "$local_path" ]]; then
            found=true
            break
        fi
    done
    if [[ "$found" == "false" ]]; then
        echo "  MISSING: $cmd (expected at: $paths)" >&2
        VALIDATION_FAILED=1
    else
        echo "  OK: $cmd"
    fi
done

if [[ "$VALIDATION_FAILED" -eq 1 ]]; then
    echo "" >&2
    echo "ERROR: initramfs validation failed — missing binaries listed above." >&2
    echo "       The initramfs will NOT boot correctly. Fix the missing tools and rebuild." >&2
    echo "" >&2
    exit 1
fi

echo "  [+] All required binaries present in initramfs rootfs"

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
