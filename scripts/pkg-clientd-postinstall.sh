#!/bin/sh
# pkg-clientd-postinstall.sh — post-install script for the clustr-clientd RPM.
#
# Run by the package manager after binary and unit files are placed.
# Must be idempotent: safe to run on both fresh install and upgrade.

set -e

# ---------------------------------------------------------------------------
# Reload systemd unit database so the new/updated unit is visible
# ---------------------------------------------------------------------------
if command -v systemctl > /dev/null 2>&1; then
    systemctl daemon-reload || true

    # Enable the unit so it starts on next boot even if the config files are
    # not yet present (ConditionPathExists gates the actual start).
    systemctl enable clustr-clientd.service 2>/dev/null || true

    # Start (or restart on upgrade) only when the required config files exist.
    # If /etc/clustr/node-token or /etc/clustr/clustrd-url are absent the node
    # hasn't been deployed yet — leave the service stopped; it will start once
    # clustr-serverd runs finalize and writes those files.
    if [ -f /etc/clustr/node-token ] && [ -f /etc/clustr/clustrd-url ]; then
        systemctl restart clustr-clientd.service 2>/dev/null || true
    fi
fi

echo ""
echo "clustr-clientd installed."
echo ""
echo "The service is enabled and will start automatically on next boot."
echo "If this node is already enrolled, the agent will (re)start now."
echo "To check status:  systemctl status clustr-clientd"
echo "To view logs:     journalctl -u clustr-clientd -f"
echo ""
