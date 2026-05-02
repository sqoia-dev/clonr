# clustr initramfs builder image
#
# PURPOSE: build-time only. This image is used by CI (and local devs) to produce
# a reproducible initramfs cpio archive. It is NOT a runtime dependency; it is
# never installed on cluster nodes.
#
# It contains:
#   - Rocky Linux 9 with the pinned kernel + kernel-modules RPMs
#   - All tools required by scripts/build-initramfs.sh in LOCAL_MODE
#   - A fixed /modules directory copied from /lib/modules/$KERNEL_VERSION
#
# Build once per kernel-version pin; cache in ghcr.io/sqoia-dev/initramfs-builder.
# Rebuild via the initramfs-builder.yml workflow_dispatch; do NOT run on every push.
#
# Kernel version pin:
#   KERNEL_VERSION must match what is installed on the target cluster nodes.
#   Change it here when you update the production kernel; rebuild + push the image.
#
# Usage (CI — inside container where /modules already exists):
#   make initramfs MODULES_PATH=/modules
#
# Usage (local — pull image, bind-mount repo):
#   docker run --rm -v $(pwd):/clustr \
#     ghcr.io/sqoia-dev/initramfs-builder:5.14.0-503.40.1.el9_5.x86_64 \
#     make -C /clustr initramfs MODULES_PATH=/modules

FROM rockylinux:9

# Kernel version to embed.  Must match the kernel running on cluster nodes.
# Override at build time: docker build --build-arg KERNEL_VERSION=5.14.0-X.el9
ARG KERNEL_VERSION=5.14.0-503.40.1.el9_5.x86_64

# Install the kernel RPM (provides /boot/vmlinuz-* and /lib/modules/$KVER).
# kernel-modules provides the .ko files we need in the initramfs.
# The remaining packages are required by build-initramfs.sh in LOCAL_MODE.
RUN dnf install -y --setopt=install_weak_deps=False \
        kernel-${KERNEL_VERSION} \
        kernel-modules-${KERNEL_VERSION} \
        kernel-modules-extra-${KERNEL_VERSION} \
        cpio \
        xz \
        gzip \
        file \
        binutils \
        findutils \
        kmod \
        # depmod is part of kmod
        busybox \
        curl \
        rsync \
        tar \
        gzip \
        pigz \
        zstd \
        mdadm \
        gdisk \
        xfsprogs \
        e2fsprogs \
        dosfstools \
        parted \
        util-linux \
        grub2-tools \
        efibootmgr \
        dropbear \
        screen \
        make \
        git \
        golang \
    && dnf clean all

# Copy kernel modules to a stable, well-known path (/modules).
# The kernel RPM installs into /lib/modules/$KERNEL_VERSION; we copy it
# so the path is independent of any version-specific directory naming.
# CI mounts the builder image and sets MODULES_PATH=/modules.
RUN cp -r /lib/modules/${KERNEL_VERSION} /modules && \
    chmod -R a+rX /modules

# Expose the pinned kernel version as a label for inspection.
LABEL org.opencontainers.image.title="clustr initramfs builder" \
      org.opencontainers.image.description="Build-time image for reproducible clustr initramfs. Not a runtime dep." \
      io.clustr.kernel-version="${KERNEL_VERSION}"
