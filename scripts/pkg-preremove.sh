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
    # Stop + disable the rpm-update timer too so the package can be cleanly
    # removed.  is-active/is-enabled return non-zero when the unit doesn't
    # exist, hence the 2>/dev/null guards — ignore those errors on hosts
    # that never enabled the timer.
    if systemctl is-active --quiet clustr-rpm-update.timer 2>/dev/null; then
        systemctl stop clustr-rpm-update.timer || true
    fi
    if systemctl is-enabled --quiet clustr-rpm-update.timer 2>/dev/null; then
        systemctl disable clustr-rpm-update.timer || true
    fi
fi
