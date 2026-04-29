#!/bin/sh
# pkg-postinstall.sh — post-install script for the clustr-serverd RPM package.
#
# Run by the package manager after binary and config files are placed.
# Must be idempotent: safe to run on both fresh install and upgrade.

set -e

# ---------------------------------------------------------------------------
# Create clustr system user and group
# ---------------------------------------------------------------------------
# The service runs as root (required by nspawn/loop/DHCP capabilities) but
# data directories are owned by the clustr user so that files written by
# non-root subprocesses land under a predictable identity. This also makes
# the ownership story clean for backups and audits.

if ! getent group clustr > /dev/null 2>&1; then
    groupadd --system clustr
fi

if ! getent passwd clustr > /dev/null 2>&1; then
    useradd \
        --system \
        --gid clustr \
        --no-create-home \
        --home-dir /var/lib/clustr \
        --shell /sbin/nologin \
        --comment "clustr server" \
        clustr
fi

# ---------------------------------------------------------------------------
# Fix ownership on data and log directories
# ---------------------------------------------------------------------------
chown -R root:clustr /var/lib/clustr
chown -R root:clustr /var/log/clustr
chown -R root:clustr /etc/clustr

# ---------------------------------------------------------------------------
# Reload systemd unit database
# ---------------------------------------------------------------------------
if command -v systemctl > /dev/null 2>&1; then
    systemctl daemon-reload || true
fi

# ---------------------------------------------------------------------------
# Post-install notice
# ---------------------------------------------------------------------------
echo ""
echo "clustr-serverd installed."
echo ""
echo "Before starting the service:"
echo "  1. Edit /etc/clustr/clustr-serverd.conf"
echo "     Set CLUSTR_PXE_INTERFACE and CLUSTR_PXE_SERVER_IP for your"
echo "     provisioning network, then set CLUSTR_PXE_ENABLED=true."
echo ""
echo "  2. Create /etc/clustr/secrets.env with a persistent session secret:"
echo "       openssl rand -hex 64 | sed 's/^/CLUSTR_SESSION_SECRET=/' \\"
echo "         > /etc/clustr/secrets.env"
echo "       chmod 0400 /etc/clustr/secrets.env"
echo ""
echo "  3. Enable and start the service:"
echo "       systemctl enable --now clustr-serverd"
echo ""
echo "  4. Create the admin account (run once, on this host):"
echo "       clustr-serverd bootstrap-admin"
echo "     Default credentials: clustr / clustr"
echo "     You will be prompted to change the password on first login."
echo ""
