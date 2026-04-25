#!/bin/bash
# install.sh — Install clustr-autodeploy systemd units on this host.
#
# Run as root from the repo root:
#   sudo bash scripts/autodeploy/install.sh
#
# After install, the timer fires 1 minute after boot and every 2 minutes.
# To trigger an immediate sync (without waiting for the timer):
#   systemctl start clustr-autodeploy.service
#
# To pause auto-sync (e.g., for testing uncommitted changes):
#   systemctl stop clustr-autodeploy.timer
# Resume:
#   systemctl start clustr-autodeploy.timer

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SYSTEMD_DIR="/etc/systemd/system"
SCRIPT_DEST="/opt/clustr/scripts/autodeploy/clustr-autodeploy.sh"

log() { echo "[install] $*"; }

if [[ "${EUID}" -ne 0 ]]; then
    echo "ERROR: must run as root" >&2
    exit 1
fi

# Verify we're running from a valid repo
if [[ ! -d "${REPO_DIR}/.git" ]]; then
    echo "ERROR: must run from inside the clustr git repo (expected .git at ${REPO_DIR})" >&2
    exit 1
fi

log "Installing from repo: ${REPO_DIR}"

# ---------------------------------------------------------------------------
# Ensure root can SSH to 127.0.0.1 via its own key (no passphrase needed).
# build-initramfs.sh uses sshpass+scp to localhost to fetch binaries/modules.
# The autodeploy script uses a sshpass shim that drops -p and lets key auth
# through. For this to work, root's own public key must be in authorized_keys.
# ---------------------------------------------------------------------------
if [[ -f /root/.ssh/id_ed25519.pub ]]; then
    ROOT_PUBKEY=$(cat /root/.ssh/id_ed25519.pub)
    if ! grep -qF "${ROOT_PUBKEY}" /root/.ssh/authorized_keys 2>/dev/null; then
        echo "${ROOT_PUBKEY}" >> /root/.ssh/authorized_keys
        chmod 600 /root/.ssh/authorized_keys
        log "Added root's own public key to authorized_keys (for localhost SSH)"
    else
        log "Root's own public key already in authorized_keys"
    fi
else
    log "WARNING: /root/.ssh/id_ed25519.pub not found — localhost SSH for build-initramfs may fail"
fi

# ---------------------------------------------------------------------------
# Install the autodeploy script to /usr/local/sbin/ — OUTSIDE the repo.
# This is critical: the systemd unit runs from /usr/local/sbin/clustr-autodeploy.sh,
# NOT from the repo path. If we ran from /opt/clustr/scripts/autodeploy/..., a
# git reset --hard to a commit before this script existed would delete the file
# and make every subsequent timer invocation fail with status=203/EXEC.
#
# The /usr/local/sbin copy is the stable entry point. The repo copy is the
# source of truth for development. To update the installed copy after script
# changes, re-run this install.sh.
# ---------------------------------------------------------------------------
SCRIPT_SRC="${REPO_DIR}/scripts/autodeploy/clustr-autodeploy.sh"
SCRIPT_DEST="/usr/local/sbin/clustr-autodeploy.sh"
cp "${SCRIPT_SRC}" "${SCRIPT_DEST}"
chmod +x "${SCRIPT_DEST}"
log "Installed clustr-autodeploy.sh → ${SCRIPT_DEST}"

# If the repo is not at /opt/clustr, warn — the script hardcodes /opt/clustr as REPO_DIR
if [[ "${REPO_DIR}" != "/opt/clustr" ]]; then
    log "WARNING: repo is at ${REPO_DIR} but clustr-autodeploy.sh expects /opt/clustr"
    log "         Edit REPO_DIR in ${SCRIPT_DEST} if you use a different path."
fi

# Install systemd units
log "Installing systemd units..."
cp "${REPO_DIR}/deploy/systemd/clustr-autodeploy.service" "${SYSTEMD_DIR}/"
cp "${REPO_DIR}/deploy/systemd/clustr-autodeploy.timer" "${SYSTEMD_DIR}/"

systemctl daemon-reload
log "systemd daemon reloaded"

# Enable and start the timer (not the service directly — let the timer manage it)
systemctl enable --now clustr-autodeploy.timer
log "clustr-autodeploy.timer enabled and started"

echo ""
echo "Installation complete."
echo ""
echo "Timer status:"
systemctl status clustr-autodeploy.timer --no-pager
echo ""
echo "Next steps:"
echo "  - Watch the first run:  journalctl -u clustr-autodeploy.service -f"
echo "  - Force an immediate run:  systemctl start clustr-autodeploy.service"
echo "  - Pause auto-sync:  systemctl stop clustr-autodeploy.timer"
echo "  - Resume auto-sync:  systemctl start clustr-autodeploy.timer"
