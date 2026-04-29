#!/bin/sh
# pkg-preremove.sh — pre-remove script for clustr-serverd RPM/DEB package.
#
# Stops the service before the package manager removes files.
# Does NOT remove /var/lib/clustr data or /etc/clustr config — those are
# the operator's responsibility to keep or purge.

set -e

if command -v systemctl > /dev/null 2>&1; then
    if systemctl is-active --quiet clustr-serverd 2>/dev/null; then
        systemctl stop clustr-serverd || true
    fi
    if systemctl is-enabled --quiet clustr-serverd 2>/dev/null; then
        systemctl disable clustr-serverd || true
    fi
fi
