#!/bin/bash
# Build a minimal initramfs containing the clustr static binary.
# The initramfs boots, brings up networking via DHCP, then runs
# 'clustr deploy --auto' to register with the server and deploy an image.
#
# Usage:
#   ./scripts/build-initramfs.sh <clustr-binary> [output-path]
#
# Prerequisites:
#   - clustr binary must be statically compiled (CGO_ENABLED=0)
#   - busybox-static package OR internet access to download busybox
#   - cpio, gzip
#   - sshpass + access to clustr-server (192.168.1.151) for kernel modules
#     (virtio_net, net_failover, failover required for virtio NIC in initramfs)
#
# Example:
#   CGO_ENABLED=0 go build -o bin/clustr ./cmd/clustr
#   ./scripts/build-initramfs.sh bin/clustr initramfs-clustr.img

set -euo pipefail

CLUSTR_BIN="${1:?Usage: build-initramfs.sh <clustr-binary> [output]}"
OUTPUT="${2:-initramfs-clustr.img}"

# clustr-server SSH credentials — used to pull kernel modules.
# The initramfs kernel version must match the modules being loaded.
# SECURITY: SSH key-based auth is strongly preferred over password auth.
#   Set up key auth: ssh-copy-id -i ~/.ssh/id_ed25519.pub <user>@<host>
#   Then invoke this script without CLUSTR_SERVER_PASS set.
CLUSTR_SERVER_HOST="${CLUSTR_SERVER_HOST:-192.168.1.151}"

# Detect local mode — skip SSH when building on the server itself, or in CI
# (CLUSTR_CI_MODE=1 disables remote SSH; kernel modules are sourced locally).
if [[ "$CLUSTR_SERVER_HOST" == "127.0.0.1" || "$CLUSTR_SERVER_HOST" == "localhost" || "$CLUSTR_SERVER_HOST" == "::1" || "${CLUSTR_CI_MODE:-0}" == "1" ]]; then
    LOCAL_MODE=1
else
    LOCAL_MODE=0
fi

# SSH credentials are only required in remote mode.
if [[ "$LOCAL_MODE" -eq 0 ]]; then
    CLUSTR_SERVER_USER="${CLUSTR_SERVER_USER:?ERROR: CLUSTR_SERVER_USER must be set (or set CLUSTR_CI_MODE=1 to skip SSH)}"
    CLUSTR_SERVER_PASS="${CLUSTR_SERVER_PASS:?ERROR: CLUSTR_SERVER_PASS must be set (or set CLUSTR_CI_MODE=1 to skip SSH)}"
else
    CLUSTR_SERVER_USER="${CLUSTR_SERVER_USER:-}"
    CLUSTR_SERVER_PASS="${CLUSTR_SERVER_PASS:-}"
fi

# Binary → package mapping for auto-install of missing tools.
# Format: TOOL_PACKAGES[binary_name]="rpm_pkg:deb_pkg"
declare -A TOOL_PACKAGES=(
    [mdadm]="mdadm:mdadm"
    [sgdisk]="gdisk:gdisk"
    [mkfs.xfs]="xfsprogs:xfsprogs"
    [mkfs.ext4]="e2fsprogs:e2fsprogs"
    [mkfs.vfat]="dosfstools:dosfstools"
    [partprobe]="parted:parted"
    [partx]="util-linux:util-linux"
    [wipefs]="util-linux:util-linux"
    [blockdev]="util-linux:util-linux"
    [mkswap]="util-linux:util-linux"
    [blkid]="util-linux:util-linux"
    [grub2-install]="grub2-tools:grub2-common"
    [grub2-mkconfig]="grub2-tools:grub2-common"
    [fsfreeze]="util-linux:util-linux"
    [efibootmgr]="efibootmgr:efibootmgr"
    [mount]="util-linux:util-linux"
    [umount]="util-linux:util-linux"
    [tar]="tar:tar"
    [gzip]="gzip:gzip"
    [pigz]="pigz:pigz"
    [zstd]="zstd:zstd"
    [rsync]="rsync:rsync"
    [udevadm]="systemd:udev"
    [curl]="curl:curl"
    [lsblk]="util-linux:util-linux"
    [cpio]="cpio:cpio"
    [file]="file:file"
    [sshpass]="sshpass:sshpass"
    [busybox]="busybox:busybox-static"
    [dropbear]="dropbear:dropbear-bin"
    [screen]="screen:screen"
)

# Remote execution / copy helpers — use local commands when on the same host.
remote_exec() {
    if [[ "$LOCAL_MODE" -eq 1 ]]; then
        bash -c "$1" 2>/dev/null
    else
        sshpass -p "$CLUSTR_SERVER_PASS" ssh -o StrictHostKeyChecking=accept-new \
            "${CLUSTR_SERVER_USER}@${CLUSTR_SERVER_HOST}" "$1" 2>/dev/null
    fi
}

remote_copy() {
    local src="$1" dst="$2"
    if [[ "$LOCAL_MODE" -eq 1 ]]; then
        cp -f "$src" "$dst" 2>/dev/null
    else
        sshpass -p "$CLUSTR_SERVER_PASS" scp -o StrictHostKeyChecking=accept-new \
            "${CLUSTR_SERVER_USER}@${CLUSTR_SERVER_HOST}:${src}" "$dst" 2>/dev/null
    fi
}

remote_copy_r() {
    local src="$1" dst="$2"
    if [[ "$LOCAL_MODE" -eq 1 ]]; then
        cp -rf "$src" "$dst" 2>/dev/null
    else
        sshpass -p "$CLUSTR_SERVER_PASS" scp -r -o StrictHostKeyChecking=accept-new \
            "${CLUSTR_SERVER_USER}@${CLUSTR_SERVER_HOST}:${src}" "$dst" 2>/dev/null
    fi
}

# ensure_tool_installed <binary_name>
# If the binary isn't found on the host (local mode) or remote server,
# attempt to install the package that provides it.
ensure_tool_installed() {
    local bin_name="$1"

    # Check if already available
    if remote_exec "command -v ${bin_name} >/dev/null 2>&1"; then
        return 0
    fi

    local pkg_spec="${TOOL_PACKAGES[$bin_name]:-}"
    if [[ -z "$pkg_spec" ]]; then
        echo "      WARNING: ${bin_name} not found and no package mapping exists" >&2
        return 1
    fi

    local rpm_pkg="${pkg_spec%%:*}"
    local deb_pkg="${pkg_spec##*:}"

    echo "      [*] ${bin_name} not found — installing..."
    if remote_exec "command -v dnf >/dev/null 2>&1"; then
        remote_exec "dnf install -y ${rpm_pkg} 2>&1 | tail -3" || true
    elif remote_exec "command -v yum >/dev/null 2>&1"; then
        remote_exec "yum install -y ${rpm_pkg} 2>&1 | tail -3" || true
    elif remote_exec "command -v apt-get >/dev/null 2>&1"; then
        remote_exec "apt-get update -qq && apt-get install -y ${deb_pkg} 2>&1 | tail -3" || true
    fi

    # Verify it's now available
    if remote_exec "command -v ${bin_name} >/dev/null 2>&1"; then
        echo "      [+] Installed ${bin_name} via package manager"
        return 0
    else
        echo "      WARNING: ${bin_name} still not found after install attempt" >&2
        return 1
    fi
}

# Verify the binary exists and is executable.
if [[ ! -f "$CLUSTR_BIN" ]]; then
    echo "ERROR: clustr binary not found: $CLUSTR_BIN" >&2
    exit 1
fi

# Check required tools (sshpass only needed in remote mode).
# Auto-install any missing tools using the system package manager.
REQUIRED_TOOLS=(cpio gzip)
if [[ "$LOCAL_MODE" -eq 0 ]]; then
    REQUIRED_TOOLS+=(sshpass)
fi
for tool in "${REQUIRED_TOOLS[@]}"; do
    if ! command -v "$tool" &>/dev/null; then
        echo "  [*] Required tool not found: ${tool} — attempting auto-install..."
        pkg_spec="${TOOL_PACKAGES[$tool]:-}"
        if [[ -n "$pkg_spec" ]]; then
            rpm_pkg="${pkg_spec%%:*}"
            deb_pkg="${pkg_spec##*:}"
            if command -v dnf &>/dev/null; then
                dnf install -y "$rpm_pkg" 2>&1 | tail -3
            elif command -v yum &>/dev/null; then
                yum install -y "$rpm_pkg" 2>&1 | tail -3
            elif command -v apt-get &>/dev/null; then
                apt-get update -qq && apt-get install -y "$deb_pkg" 2>&1 | tail -3
            fi
        fi
        if ! command -v "$tool" &>/dev/null; then
            echo "ERROR: required tool not found and could not be installed: $tool" >&2
            exit 1
        fi
        echo "  [+] Installed required tool: ${tool}"
    fi
done

# Create temp root and ensure cleanup on exit.
WORKDIR=$(mktemp -d /tmp/clustr-initramfs.XXXXXXXX)
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

# Install clustr binary.
# The binary is ALWAYS installed as exactly /usr/bin/clustr regardless of the
# source name (e.g. clustr-static, clustr-linux-amd64).  The init script exec
# line is hardcoded to /usr/bin/clustr — any mismatch here would cause the
# deploy agent to not be found at runtime.
CLUSTR_INSTALLED_PATH="$WORKDIR/usr/bin/clustr"
cp "$CLUSTR_BIN" "$CLUSTR_INSTALLED_PATH"
chmod 755 "$CLUSTR_INSTALLED_PATH"

echo "  [+] Installed clustr binary as /usr/bin/clustr ($(du -h "$CLUSTR_BIN" | cut -f1), src=$(basename "$CLUSTR_BIN"))"

# Install clustr-clientd binary (node agent, copied into deployed rootfs during finalize).
CLUSTR_CLIENTD_SRC="$(dirname "$CLUSTR_BIN")/clustr-clientd"
if [ -f "$CLUSTR_CLIENTD_SRC" ]; then
    cp "$CLUSTR_CLIENTD_SRC" "$WORKDIR/usr/bin/clustr-clientd"
    chmod 755 "$WORKDIR/usr/bin/clustr-clientd"
    echo "  [+] Installed clustr-clientd as /usr/bin/clustr-clientd ($(du -h "$CLUSTR_CLIENTD_SRC" | cut -f1))"
else
    echo "  [!] clustr-clientd not found at $CLUSTR_CLIENTD_SRC — node agent will not be available in initramfs"
fi

# Install busybox for shell and basic utilities.
# Prefer a musl static build from busybox.net (most complete applet set).
# Fall back to the system busybox if the download fails.
BUSYBOX_URL="https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox"
# SHA-256 of busybox 1.35.0-x86_64-linux-musl from busybox.net.
# Verify with: sha256sum busybox
# Re-pin by running: curl -sL "$BUSYBOX_URL" | sha256sum
BUSYBOX_SHA256="6e123e7f3202a8c1e9b1f94d8941580a25135382b99e8d3e34fb858bba311348"
if curl -sL --max-time 30 -o "$WORKDIR/bin/busybox" "$BUSYBOX_URL"; then
    echo "$BUSYBOX_SHA256  $WORKDIR/bin/busybox" | sha256sum -c - || {
        echo "ERROR: busybox checksum mismatch — binary may be tampered or URL changed" >&2
        exit 1
    }
    chmod 755 "$WORKDIR/bin/busybox"
    echo "  [+] Downloaded busybox 1.35.0 musl from busybox.net (checksum verified)"
elif command -v busybox &>/dev/null && file "$(command -v busybox)" | grep -q "statically linked"; then
    cp "$(command -v busybox)" "$WORKDIR/bin/busybox"
    chmod 755 "$WORKDIR/bin/busybox"
    echo "  [+] Using system busybox (static): $(command -v busybox)"
elif [[ -f /usr/lib/busybox/busybox-static ]]; then
    cp /usr/lib/busybox/busybox-static "$WORKDIR/bin/busybox"
    chmod 755 "$WORKDIR/bin/busybox"
    echo "  [+] Using /usr/lib/busybox/busybox-static"
else
    echo "  [*] Busybox not found — attempting package manager install..."
    ensure_tool_installed busybox || true

    # Re-check after install attempt
    if command -v busybox &>/dev/null; then
        cp "$(command -v busybox)" "$WORKDIR/bin/busybox"
        chmod 755 "$WORKDIR/bin/busybox"
        echo "  [+] Installed and using system busybox: $(command -v busybox)"
    elif [[ -f /usr/sbin/busybox ]]; then
        cp /usr/sbin/busybox "$WORKDIR/bin/busybox"
        chmod 755 "$WORKDIR/bin/busybox"
        echo "  [+] Installed and using system busybox: /usr/sbin/busybox"
    else
        echo "ERROR: cannot obtain a static busybox binary — install busybox manually" >&2
        exit 1
    fi
fi

# ──────────────────────────────────────────────────────────────────────────────
# Install lsblk.
#
# lsblk is not a busybox applet — it comes from util-linux. Without it, clustr's
# hardware discovery returns an empty disk list and disk selection during deploy
# fails. We fetch the binary directly from the clustr-server, which already has
# util-linux installed.
#
# Strategy (in order):
#   1. Fetch static lsblk from clustr-server at /usr/bin/lsblk (preferred).
#   2. If the server binary is dynamically linked, copy it plus its required
#      shared libraries from the server.
#   3. If sshpass/server is unavailable, check the local system for a static
#      lsblk binary (e.g. util-linux-static package on Debian/Ubuntu).
# ──────────────────────────────────────────────────────────────────────────────
echo "  [+] Installing lsblk..."

LSBLK_INSTALLED=false
LSBLK_DEST="$WORKDIR/usr/bin/lsblk"

# Helper: try to fetch lsblk from the clustr-server.
fetch_lsblk_from_server() {
    if [[ "$LOCAL_MODE" -eq 0 ]] && ! command -v sshpass &>/dev/null; then
        echo "      sshpass not found — cannot fetch lsblk from server" >&2
        return 1
    fi

    # Copy the binary.
    if ! remote_copy "/usr/bin/lsblk" "$LSBLK_DEST"; then
        echo "      failed to copy lsblk from ${CLUSTR_SERVER_HOST}" >&2
        return 1
    fi
    chmod 755 "$LSBLK_DEST"

    # Determine if the binary is statically linked.
    LSBLK_FILE_INFO=$(file "$LSBLK_DEST" 2>/dev/null || echo "")
    if echo "$LSBLK_FILE_INFO" | grep -q "statically linked"; then
        echo "      fetched static lsblk from ${CLUSTR_SERVER_HOST}"
        return 0
    fi

    # Dynamically linked — copy required shared libraries from the server.
    echo "      lsblk is dynamically linked — fetching required libs..."
    NEEDED_LIBS=$(remote_exec "ldd /usr/bin/lsblk 2>/dev/null" | \
        grep -oP '/[^ ]+\.so[^ ]*' | sort -u 2>/dev/null || true)

    if [[ -z "$NEEDED_LIBS" ]]; then
        echo "      WARNING: could not determine lsblk dependencies" >&2
        return 0  # keep the binary anyway, it may work if libs are already present
    fi

    for lib in $NEEDED_LIBS; do
        lib_dir="$WORKDIR$(dirname "$lib")"
        mkdir -p "$lib_dir"
        remote_copy "${lib}" "${lib_dir}/$(basename "$lib")" || \
            echo "      WARNING: could not fetch lib $lib" >&2
    done

    # Set up /lib64/ld-linux-x86-64.so.2 symlink if needed (glibc dynamic linker).
    if [[ ! -e "$WORKDIR/lib64/ld-linux-x86-64.so.2" ]]; then
        LINKER=$(remote_exec "readlink -f /lib64/ld-linux-x86-64.so.2 2>/dev/null" || echo "")
        if [[ -n "$LINKER" ]]; then
            mkdir -p "$WORKDIR/lib64"
            remote_copy "${LINKER}" "$WORKDIR/lib64/ld-linux-x86-64.so.2" || true
        fi
    fi

    echo "      fetched dynamic lsblk + libs from ${CLUSTR_SERVER_HOST}"
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

    # Last resort: attempt to install lsblk via package manager on the target host.
    if [[ "$LSBLK_INSTALLED" == "false" ]]; then
        echo "      lsblk not available locally — attempting auto-install on ${CLUSTR_SERVER_HOST}..."
        if ensure_tool_installed lsblk; then
            if remote_copy "/usr/bin/lsblk" "$LSBLK_DEST"; then
                chmod 755 "$LSBLK_DEST"
                echo "      fetched lsblk after auto-install"
                LSBLK_INSTALLED=true
            fi
        fi
    fi
fi

if [[ "$LSBLK_INSTALLED" == "true" ]]; then
    echo "  [+] lsblk installed at /usr/bin/lsblk ($(du -h "$LSBLK_DEST" | cut -f1))"
else
    echo "  [!] WARNING: lsblk could not be installed — disk discovery will return empty results" >&2
    echo "               Run: sshpass -p clustr scp clustr@192.168.1.151:/usr/bin/lsblk initramfs-lsblk && rebuild" >&2
fi

# ──────────────────────────────────────────────────────────────────────────────
# Install deployment tools from clustr-server.
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
# Strategy: SSH to clustr-server, copy binaries + their shared libraries.
# We use ldd on the server to find all required .so files and scp them over.
# ──────────────────────────────────────────────────────────────────────────────
echo "  [+] Installing deployment tools from ${CLUSTR_SERVER_HOST}..."

if [[ "$LOCAL_MODE" -eq 0 ]] && ! command -v sshpass &>/dev/null; then
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
        first_order=$(remote_exec "ldd ${remote_path} 2>/dev/null" | \
            grep -oP '/[^ ]+\.so[^ ]*' | sort -u || true)

        # Collect unique libs across binary + transitive layer
        local all_libs="$first_order"
        for lib in $first_order; do
            local transitive
            transitive=$(remote_exec "ldd ${lib} 2>/dev/null" | \
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
        if ! remote_copy "${remote_path}" "${dest_dir}/${bin_name}"; then
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
            # Copy the real file (resolving symlinks on the server side).
            # We need the soname symlink too so the dynamic linker finds it by
            # the name embedded in the binary's NEEDED entries.
            local real_lib
            real_lib=$(remote_exec "readlink -f ${lib} 2>/dev/null || echo ${lib}" || echo "$lib")
            remote_copy "${real_lib}" "${lib_dir}/$(basename "$lib")" || \
                echo "      WARNING: could not fetch lib ${lib}" >&2
        done

        # Ensure the dynamic linker itself is present under /lib64/
        if [[ ! -e "$WORKDIR/lib64/ld-linux-x86-64.so.2" ]]; then
            local linker
            linker=$(remote_exec "readlink -f /lib64/ld-linux-x86-64.so.2 2>/dev/null" || echo "")
            if [[ -n "$linker" ]]; then
                mkdir -p "$WORKDIR/lib64"
                remote_copy "${linker}" "$WORKDIR/lib64/ld-linux-x86-64.so.2" || true
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
        remote_path=$(remote_exec "which ${bin_name} 2>/dev/null || command -v ${bin_name} 2>/dev/null" || echo "")
        if [[ -z "$remote_path" ]]; then
            ensure_tool_installed "$bin_name" || return 1
            remote_path=$(remote_exec "which ${bin_name} 2>/dev/null || command -v ${bin_name} 2>/dev/null" || echo "")
            if [[ -z "$remote_path" ]]; then
                echo "      WARNING: ${bin_name} not found on ${CLUSTR_SERVER_HOST}" >&2
                return 1
            fi
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
    # busybox umount does NOT support -R (recursive unmount). clustr's unmountAll()
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
    #       clustr's streamExtract() prefers pigz over gzip when available.
    # zstd: zstandard — 3-5x faster decompression than gzip at similar ratio.
    #       clustr stores new captures as .tar.zst and detects the magic bytes at
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
    echo "      fetching grub2 module data from ${CLUSTR_SERVER_HOST}..."
    for grub_dir in /usr/lib/grub /usr/share/grub; do
        # Parent dir inside initramfs (e.g. $WORKDIR/usr/lib for /usr/lib/grub)
        local_parent="$WORKDIR$(dirname "$grub_dir")"
        mkdir -p "$local_parent"
        if remote_copy_r "${grub_dir}" "${local_parent}/"; then
            echo "      fetched ${grub_dir} ($(du -sh "${local_parent}/$(basename "$grub_dir")" 2>/dev/null | cut -f1))"
        else
            echo "      WARNING: could not fetch ${grub_dir}" >&2
        fi
    done

    echo "  [+] Deployment tools installed"

    # ── dropbear SSH server ────────────────────────────────────────────────────
    # dropbear is a small SSH server suitable for initramfs. When present,
    # the init script starts it (gated by clustr.ssh=1 in the kernel cmdline)
    # so the operator can SSH into a node during deploy to inspect failures.
    #
    # We install from the server rather than the build host to guarantee the
    # binary matches the target kernel's libc. If dropbear is not available on
    # the server, the feature is silently disabled — the build still succeeds.
    echo "[+] Installing dropbear SSH server..."
    DROPBEAR_PATH=$(remote_exec "command -v dropbear 2>/dev/null || command -v /usr/sbin/dropbear 2>/dev/null" || echo "")
    if [[ -n "$DROPBEAR_PATH" ]]; then
        mkdir -p "$WORKDIR/usr/sbin" "$WORKDIR/etc/dropbear"
        if install_server_binary "$DROPBEAR_PATH" "$WORKDIR/usr/sbin"; then
            # Also install dropbearkey for host-key generation at boot time.
            DROPBEARKEY_PATH=$(remote_exec "command -v dropbearkey 2>/dev/null" || echo "")
            if [[ -n "$DROPBEARKEY_PATH" ]]; then
                install_server_binary "$DROPBEARKEY_PATH" "$WORKDIR/usr/sbin" || true
            fi
            echo "  [+] dropbear SSH server installed at /usr/sbin/dropbear"
        else
            echo "  [!] dropbear found but could not be installed — SSH-into-initramfs disabled" >&2
        fi
    else
        echo "  [!] dropbear not found on ${CLUSTR_SERVER_HOST} — SSH-into-initramfs disabled" >&2
        echo "       Install with: dnf install -y dropbear   # Rocky/RHEL" >&2
        echo "       Install with: apt-get install -y dropbear-bin  # Debian/Ubuntu" >&2
    fi

    # ── screen ────────────────────────────────────────────────────────────────
    # screen lets the operator attach to the deploy agent's session at runtime.
    # When present, the deploy agent runs inside 'screen -S clustr-deploy' so
    # the operator can 'screen -r clustr-deploy' after SSHing in.
    echo "[+] Installing screen..."
    SCREEN_PATH=$(remote_exec "command -v screen 2>/dev/null" || echo "")
    if [[ -n "$SCREEN_PATH" ]]; then
        mkdir -p "$WORKDIR/usr/bin"
        if install_server_binary "$SCREEN_PATH" "$WORKDIR/usr/bin"; then
            echo "  [+] screen installed at /usr/bin/screen"
        else
            echo "  [!] screen found but could not be installed — using direct exec fallback" >&2
        fi
    else
        echo "  [!] screen not found on ${CLUSTR_SERVER_HOST} — using direct exec fallback" >&2
        echo "       Install with: dnf install -y screen   # Rocky/RHEL" >&2
        echo "       Install with: apt-get install -y screen  # Debian/Ubuntu" >&2
    fi
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
# Kernel modules — real-hardware + VM support.
#
# Module allowlist covers:
#   NICs: mlx5 (Mellanox CX-4/5/6), mlx4 (CX-3), i40e/ice (Intel XL710/E810),
#         ixgbe/igb/e1000e (Intel 10G/1G), bnxt_en (Broadcom BCM5741x),
#         bnx2x (Broadcom 10G legacy), tg3 (Broadcom NetXtreme),
#         virtio_net + failover (VMs)
#   Storage: nvme/nvme_core (NVMe SSDs), megaraid_sas (LSI MegaRAID),
#            mpt3sas (LSI SAS3/SAS2), aacraid (Adaptec RAID),
#            virtio_scsi/virtio_blk/sd_mod (VMs)
#   Block/DM: dm_mod, dm_mirror, dm_snapshot, dm_thin_pool (Device Mapper)
#   FS: xfs, ext4, fat/vfat, btrfs, jbd2, mbcache
#   Crypto/Lib: crc32c_generic, crc32c-intel (x86 hw), libcrc32c
#   MD RAID: raid0/1/10/456
#
# Strategy: build-time enumeration. We walk the server's module directories for
# each allowlisted name, pulling every .ko/.ko.xz that matches. This captures
# sibling modules (e.g. mlx5_vdpa alongside mlx5_core) and survives kernel
# version bumps that move files between subdirectories.
#
# Out of scope: lspci-alias auto-detection. If the explicit list proves fragile
# after real-hardware lab validation, Sprint 21 can add lspci resolution.
# ──────────────────────────────────────────────────────────────────────────────
echo "  [+] Fetching kernel modules from clustr-server ${CLUSTR_SERVER_HOST}..."

# Discover the kernel version from the server.
KVER=$(remote_exec "uname -r" 2>/dev/null)

if [[ -z "$KVER" ]]; then
    echo "WARNING: cannot reach clustr-server — skipping kernel modules." >&2
    echo "         NIC drivers will not be loaded; DHCP may fail." >&2
    KVER="unknown"
else
    echo "      kernel version: $KVER"

    # ── Module allowlist ──────────────────────────────────────────────────────
    # Canonical module names (underscores, no .ko suffix).
    # The enumerator below searches for each name under the kernel module tree,
    # handling both foo.ko and foo.ko.xz, and any subdirectory layout.
    # Adding a name here is sufficient — no file paths to maintain.
    MODULE_ALLOWLIST=(
        # VMs (keep existing virtio support)
        failover net_failover virtio_net
        virtio_scsi virtio_blk

        # Mellanox/NVIDIA ConnectX NICs (CX-3 through CX-6+)
        mlx5_core mlx5_ib
        mlx4_core mlx4_en mlx4_ib

        # Intel NICs (XL710 i40e, E810 ice, 82599 ixgbe, 1G igb, e1000e)
        i40e ice ixgbe igb e1000e

        # Broadcom NICs
        bnxt_en bnx2x tg3

        # NVMe storage
        nvme nvme_core

        # Hardware RAID controllers
        megaraid_sas mpt3sas aacraid

        # SCSI mid-layer (required by hardware HBAs above)
        sd_mod scsi_mod

        # Device Mapper (LVM + thin provisioning)
        dm_mod dm_mirror dm_snapshot dm_thin_pool

        # Filesystems
        xfs btrfs ext4 jbd2 mbcache fat vfat

        # Crypto / CRC (required by xfs, btrfs, dm layers)
        crc32c_generic libcrc32c
        crc32c-intel    # x86 hardware acceleration (hyphen form in tree)

        # MD software RAID personalities
        raid0 raid1 raid10 raid456
    )

    # ── Directories to search on the server ──────────────────────────────────
    # We walk these recursively; the find command below handles subdirectory
    # depth automatically so we don't need to enumerate leaf directories.
    SEARCH_DIRS=(
        "net"
        "drivers/net"
        "drivers/scsi"
        "drivers/block"
        "drivers/nvme"
        "drivers/md"
        "drivers/infiniband"
        "fs"
        "arch/x86/crypto"
        "lib"
        "crypto"
    )

    KMOD_BASE="/lib/modules/$KVER/kernel"
    KMOD_LOCAL="$WORKDIR/lib/modules/$KVER/kernel"
    MANIFEST_FILE="${OUTPUT%.img}.modules.manifest"
    # Initialize manifest (overwrite any prior run)
    : > "$MANIFEST_FILE"

    # ── enumerate_and_fetch <module_name> ────────────────────────────────────
    # Walks each SEARCH_DIR on the server looking for <module_name>.ko or
    # <module_name>.ko.xz (recursively). Copies every match into the initramfs,
    # decompresses .xz files, and appends a manifest line.
    #
    # We normalize hyphens↔underscores when comparing names because the kernel
    # tree uses hyphens in some file names (crc32c-intel.ko) while modprobe
    # canonicalizes to underscores. We match both forms.
    enumerate_and_fetch() {
        local mod_name="$1"
        # Build a grep-friendly alternation: foo_bar matches foo_bar and foo-bar.
        local mod_hyp="${mod_name//_/-}"
        local mod_und="${mod_name//-/_}"
        local found=0

        for search_dir in "${SEARCH_DIRS[@]}"; do
            local remote_dir="${KMOD_BASE}/${search_dir}"
            # List .ko and .ko.xz files in this subtree matching either form.
            local hits
            hits=$(remote_exec "
                find '${remote_dir}' -type f \( -name '${mod_hyp}.ko' -o -name '${mod_hyp}.ko.xz' \
                    -o -name '${mod_und}.ko' -o -name '${mod_und}.ko.xz' \) 2>/dev/null
            " 2>/dev/null || true)

            if [[ -z "$hits" ]]; then
                continue
            fi

            while IFS= read -r remote_path; do
                [[ -z "$remote_path" ]] && continue
                # Compute relative path from kernel base
                local rel_path="${remote_path#${KMOD_BASE}/}"
                local local_dest="${KMOD_LOCAL}/${rel_path}"
                mkdir -p "$(dirname "$local_dest")"

                if remote_copy "${remote_path}" "${local_dest}"; then
                    local ko_path="$local_dest"
                    # Decompress .xz in place (busybox insmod needs uncompressed ELF)
                    if [[ "$local_dest" == *.xz ]]; then
                        if xz -d "$local_dest" 2>/dev/null; then
                            ko_path="${local_dest%.xz}"
                        else
                            echo "      WARNING: failed to decompress ${local_dest}" >&2
                            rm -f "$local_dest"
                            continue
                        fi
                    fi
                    # Append manifest line: <module_name> <relative_path> <sha256>
                    local ko_rel="${ko_path#${WORKDIR}/}"
                    local ko_sha256
                    ko_sha256=$(sha256sum "$ko_path" 2>/dev/null | awk '{print $1}' || echo "unavailable")
                    echo "${mod_und} ${ko_rel} ${ko_sha256}" >> "$MANIFEST_FILE"
                    echo "        + $(basename "$ko_path")"
                    found=1
                else
                    echo "      WARNING: could not fetch ${remote_path}" >&2
                fi
            done <<< "$hits"
        done

        if [[ "$found" -eq 0 ]]; then
            echo "      (not found on server — may be built-in or not installed): ${mod_name}"
        fi
    }

    echo "      enumerating modules from allowlist..."
    for mod in "${MODULE_ALLOWLIST[@]}"; do
        printf "    [*] %-25s " "${mod}:"
        enumerate_and_fetch "$mod"
    done

    # ── Generate modules.dep (modprobe dependency file) ───────────────────────
    # Run depmod on the server to generate a dep file for our exact module set,
    # then copy it. If depmod is unavailable, fall back to a hand-written dep
    # file that covers the known dependency chains.
    MODDEP_DIR="$WORKDIR/lib/modules/$KVER"

    # Try server-side depmod for an accurate dep file.
    if remote_exec "command -v depmod >/dev/null 2>&1"; then
        TMP_KMOD_DIR=$(remote_exec "mktemp -d /tmp/clustr-depmod.XXXXXXXX" 2>/dev/null || echo "")
        if [[ -n "$TMP_KMOD_DIR" ]]; then
            # Copy our fetched .ko files to the server temp dir so depmod can scan them.
            # This is best-effort: if it fails we fall back to the static dep map.
            # We list the .ko files we actually embedded.
            EMBEDDED_KOS=$(find "$KMOD_LOCAL" -name "*.ko" 2>/dev/null | \
                sed "s|${WORKDIR}/||" || true)
            server_depmod_ok=0
            if [[ -n "$EMBEDDED_KOS" ]]; then
                # Build a temporary module layout on the server mirroring ours.
                while IFS= read -r rel_ko; do
                    [[ -z "$rel_ko" ]] && continue
                    remote_dir_path="${TMP_KMOD_DIR}/$(dirname "$rel_ko")"
                    remote_exec "mkdir -p '${remote_dir_path}'" 2>/dev/null || true
                    # Push the .ko to the server for depmod scanning.
                    # (sshpass scp in reverse: push local→remote)
                    if [[ "$LOCAL_MODE" -eq 1 ]]; then
                        cp -f "$WORKDIR/$rel_ko" "${TMP_KMOD_DIR}/${rel_ko}" 2>/dev/null || true
                    else
                        sshpass -p "$CLUSTR_SERVER_PASS" scp -o StrictHostKeyChecking=accept-new \
                            "$WORKDIR/$rel_ko" \
                            "${CLUSTR_SERVER_USER}@${CLUSTR_SERVER_HOST}:${TMP_KMOD_DIR}/${rel_ko}" \
                            2>/dev/null || true
                    fi
                done <<< "$EMBEDDED_KOS"
                FAKE_KVER="depmod-fake"
                DEP_OUTPUT=$(remote_exec "
                    depmod -b '${TMP_KMOD_DIR}' '${FAKE_KVER}' 2>/dev/null &&
                    cat '${TMP_KMOD_DIR}/lib/modules/${FAKE_KVER}/modules.dep' 2>/dev/null || true
                " 2>/dev/null || true)
                if [[ -n "$DEP_OUTPUT" ]]; then
                    echo "$DEP_OUTPUT" > "$MODDEP_DIR/modules.dep"
                    server_depmod_ok=1
                    echo "      generated modules.dep via server depmod"
                fi
                remote_exec "rm -rf '${TMP_KMOD_DIR}'" 2>/dev/null || true
            fi
            if [[ "$server_depmod_ok" -eq 0 ]]; then
                echo "      depmod push failed — using static dep map" >&2
            fi
        fi
    fi

    # Fall back to a static modules.dep covering known dependency chains.
    # This covers the common case; depmod-generated is preferred when available.
    if [[ ! -s "$MODDEP_DIR/modules.dep" ]]; then
        cat > "$MODDEP_DIR/modules.dep" << 'MODDEP'
kernel/net/core/failover.ko:
kernel/drivers/net/net_failover.ko: kernel/net/core/failover.ko
kernel/drivers/net/virtio_net.ko: kernel/drivers/net/net_failover.ko kernel/net/core/failover.ko
kernel/drivers/scsi/virtio_scsi.ko:
kernel/drivers/scsi/sd_mod.ko: kernel/drivers/scsi/scsi_mod.ko
kernel/drivers/scsi/scsi_mod.ko:
kernel/drivers/block/virtio_blk.ko:
kernel/arch/x86/crypto/crc32c-intel.ko:
kernel/lib/libcrc32c.ko: kernel/arch/x86/crypto/crc32c-intel.ko
kernel/fs/xfs/xfs.ko: kernel/lib/libcrc32c.ko
kernel/fs/mbcache.ko:
kernel/fs/jbd2/jbd2.ko:
kernel/fs/ext4/ext4.ko: kernel/fs/mbcache.ko kernel/fs/jbd2/jbd2.ko
kernel/fs/fat/fat.ko:
kernel/fs/fat/vfat.ko: kernel/fs/fat/fat.ko
kernel/drivers/md/dm_mod.ko:
kernel/drivers/md/dm_mirror.ko: kernel/drivers/md/dm_mod.ko
kernel/drivers/md/dm_snapshot.ko: kernel/drivers/md/dm_mod.ko
kernel/drivers/md/dm_thin_pool.ko: kernel/drivers/md/dm_mod.ko
kernel/drivers/md/raid0.ko:
kernel/drivers/md/raid1.ko:
kernel/drivers/md/raid10.ko:
kernel/drivers/md/raid456.ko:
kernel/drivers/nvme/host/nvme_core.ko:
kernel/drivers/nvme/host/nvme.ko: kernel/drivers/nvme/host/nvme_core.ko
kernel/drivers/scsi/megaraid/megaraid_sas.ko: kernel/drivers/scsi/scsi_mod.ko
kernel/drivers/scsi/mpt3sas/mpt3sas.ko: kernel/drivers/scsi/scsi_mod.ko
kernel/drivers/scsi/aacraid/aacraid.ko: kernel/drivers/scsi/scsi_mod.ko
MODDEP
        echo "      generated static fallback modules.dep"
    fi

    # modules.alias: PCI device ID to module name mappings for common devices.
    # The init script uses modprobe which reads this file.
    cat > "$MODDEP_DIR/modules.alias" << 'MODALIAS'
alias virtio:d00000001v* virtio_net
alias virtio:d00000008v* virtio_scsi
alias virtio:d00000002v* virtio_blk
alias scsi:t-0x00* sd_mod
alias pci:v000015B3d* mlx5_core
alias pci:v000015B3d00001002* mlx4_core
alias pci:v00008086d00001572* i40e
alias pci:v00008086d00008800* ice
alias pci:v00008086d000010FB* ixgbe
alias pci:v00008086d0000150E* igb
alias pci:v00008086d000010D3* e1000e
alias pci:v000014E4d* bnxt_en
alias pci:v000014E4d00001639* bnx2x
alias pci:v000014E4d00001684* tg3
alias pci:v00001000d* megaraid_sas
alias pci:v00001000d00000097* mpt3sas
alias pci:v00009005d* aacraid
MODALIAS

    # ── Log manifest location ─────────────────────────────────────────────────
    MOD_COUNT=$(wc -l < "$MANIFEST_FILE" 2>/dev/null || echo 0)
    echo "      manifest: $MANIFEST_FILE ($MOD_COUNT modules)"
    echo "      generated modules.alias"
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
# The template file (scripts/initramfs-init.sh) uses ${CLUSTR_SERVER} and
# ${CLUSTR_STATIC_BIN} as placeholders that are substituted here via sed.
# All other variables (${LOG}, ${CLUSTR_TOKEN}, etc.) are runtime variables
# resolved inside the initramfs — they are intentionally left as-is.
CLUSTR_STATIC_BIN="$CLUSTR_BIN"
# WARNING: Default uses plain HTTP on the provisioning network.
# For environments where provisioning network is not fully trusted,
# configure TLS on clustr-serverd and set:
#   CLUSTR_SERVER="https://10.99.0.1:8443"
# The initramfs curl will need the CA cert embedded — see docs/tls-provisioning.md
# Substitute runtime variables into init script
sed -e "s|\${CLUSTR_SERVER}|${CLUSTR_SERVER:-http://10.99.0.1:8080}|g" \
    -e "s|\${CLUSTR_STATIC_BIN}|${CLUSTR_STATIC_BIN}|g" \
    "$(dirname "$0")/initramfs-init.sh" > "$WORKDIR/init"
chmod 755 "$WORKDIR/init"

echo "  [+] Generated init script"

# Verify clustr binary is statically linked (best effort check on Linux).
if command -v file &>/dev/null; then
    FILE_OUT="$(file "$CLUSTR_BIN")"
    if echo "$FILE_OUT" | grep -q "dynamically linked"; then
        echo ""
        echo "WARNING: clustr binary appears to be dynamically linked." >&2
        echo "         Build with CGO_ENABLED=0 for a self-contained initramfs binary." >&2
        echo "         Command: CGO_ENABLED=0 go build -o $CLUSTR_BIN ./cmd/clustr" >&2
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
    "clustr:/usr/bin/clustr"
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
echo "  cp $OUTPUT /var/lib/clustr/boot/initramfs.img"
echo ""
echo "Download kernel:"
echo "  # Rocky Linux 9 kernel (example):"
echo "  dnf download --resolve kernel-core"
echo "  rpm2cpio kernel-core-*.rpm | cpio -id ./boot/vmlinuz-*"
echo "  cp boot/vmlinuz-* /var/lib/clustr/boot/vmlinuz"
