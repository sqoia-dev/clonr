#!/usr/bin/env bash
# clustr-rpm-update.sh — RPM-driven auto-update for clustr packages.
#
# Runs as a oneshot systemd service (clustr-rpm-update.service) on a 15-minute
# timer (clustr-rpm-update.timer).  Polls the configured DNF repo (typically
# pkg.sqoia.dev) for new clustr / clustr-serverd RPMs and upgrades + restarts
# the daemon when a newer version is available.
#
# Active-job guard (#225 RPM-UPDATE-1):
#   Restarting clustr-serverd while a deploy / image-build / initramfs-build /
#   reimage is in flight kills the in-progress operation — including UDPCast
#   blob streams to deploying nodes.  Before running `dnf upgrade`, we hit the
#   unauthenticated GET /api/v1/system/active-jobs endpoint.  If ANY field is
#   non-empty (initramfs_builds, image_builds, reimages, deploys,
#   operator_sessions, pxe_in_flight) we log the deferral reason and exit 0.
#   The next 15-minute cycle re-evaluates.
#
#   Cap: after DEFER_CAP consecutive deferrals (default 24 = ~6 hours) we
#   proceed with the update anyway, on the assumption that the running server
#   has stuck jobs.  The cap counter is cleared on every successful update OR
#   on the first cycle that finds the server idle.  Mirrors the defer logic in
#   scripts/autodeploy/clustr-autodeploy.sh.
#
# Logs visible via:
#   journalctl -u clustr-rpm-update.service [--since "1 hour ago"]

set -euo pipefail

PACKAGES="clustr clustr-serverd"
LISTEN_ADDR_DEFAULT="localhost:8080"
ACTIVE_JOBS_PATH="/api/v1/system/active-jobs"
DEFER_CAP="${CLUSTR_RPM_UPDATE_DEFER_CAP:-24}"
DEFER_FILE="/var/lib/clustr/rpm-update-defer-count"
LOG_PREFIX="[clustr-rpm-update]"

log() { echo "${LOG_PREFIX} $*"; }

# Resolve the host:port the running clustr-serverd is bound to.  Mirrors the
# discovery logic in scripts/autodeploy/clustr-autodeploy.sh so the guard
# survives operators who bind to a non-loopback provisioning interface.
_LISTEN_ADDR="${CLUSTR_LISTEN_ADDR:-}"
if [[ -z "${_LISTEN_ADDR}" ]]; then
    _LISTEN_ADDR=$(systemctl show clustr-serverd --property=Environment 2>/dev/null \
        | sed 's/^Environment=//' \
        | tr ' ' '\n' \
        | grep '^CLUSTR_LISTEN_ADDR=' \
        | cut -d= -f2- || true)
fi
HEALTH_HOST="${_LISTEN_ADDR:-${LISTEN_ADDR_DEFAULT}}"
ACTIVE_JOBS_URL="http://${HEALTH_HOST}${ACTIVE_JOBS_PATH}"

# ---------------------------------------------------------------------------
# Active-job guard — defer the update if anything is in flight
# ---------------------------------------------------------------------------
# Fail-open if the server isn't reachable: a server that's already down can't
# be made worse by a dnf upgrade, and refusing to upgrade a stuck server would
# block recovery.  Mirrors the autodeploy script's "Could not reach API"
# branch.
_ACTIVE_JOBS_JSON=$(curl -sf --max-time 5 "${ACTIVE_JOBS_URL}" 2>/dev/null || echo "")

if [[ -n "${_ACTIVE_JOBS_JSON}" ]]; then
    # Count active items across all known classes.  python3 is part of the EL
    # base install (and is already a transitive dep of dnf) so this is safe.
    _ACTIVE_SUMMARY=$(echo "${_ACTIVE_JOBS_JSON}" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("0 unparseable")
    sys.exit(0)
parts = []
total = 0
for k in ("initramfs_builds", "image_builds", "reimages", "deploys",
         "operator_sessions", "pxe_in_flight"):
    n = len(d.get(k, []) or [])
    if n > 0:
        parts.append(f"{k}={n}")
        total += n
print(total, " ".join(parts) if parts else "idle")
' 2>/dev/null || echo "0 idle")
    _TOTAL_ACTIVE="${_ACTIVE_SUMMARY%% *}"
    _DETAIL="${_ACTIVE_SUMMARY#* }"
else
    log "could not reach ${ACTIVE_JOBS_URL} — assuming server is down or starting; proceeding with update"
    _TOTAL_ACTIVE=0
    _DETAIL="server-unreachable"
fi

if [[ "${_TOTAL_ACTIVE}" =~ ^[0-9]+$ ]] && [[ "${_TOTAL_ACTIVE}" -gt 0 ]]; then
    # Defer — but with a cap so a permanently-stuck job doesn't block updates
    # forever.
    _RAW_DEFER=$(cat "${DEFER_FILE}" 2>/dev/null || echo "0")
    if ! [[ "${_RAW_DEFER}" =~ ^[0-9]+$ ]]; then
        _RAW_DEFER=0
    fi

    if [[ "${_RAW_DEFER}" -ge "${DEFER_CAP}" ]]; then
        log "WARNING: active jobs (${_DETAIL}) have blocked update for ${_RAW_DEFER} consecutive cycles (~$(( _RAW_DEFER * 15 / 60 ))h) — appears stuck; proceeding with update anyway"
        rm -f "${DEFER_FILE}"
        # Fall through to the upgrade below.
    else
        _NEXT_DEFER=$(( _RAW_DEFER + 1 ))
        log "deferring update — ${_TOTAL_ACTIVE} active job(s): ${_DETAIL} (cycle ${_NEXT_DEFER}/${DEFER_CAP})"
        # Best-effort write — directory may not exist on the very first run.
        mkdir -p "$(dirname "${DEFER_FILE}")" 2>/dev/null || true
        echo "${_NEXT_DEFER}" > "${DEFER_FILE}" 2>/dev/null || true
        exit 0
    fi
else
    # Idle — clear the defer counter so the cap resets cleanly for the next
    # busy window.
    rm -f "${DEFER_FILE}" 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Upgrade
# ---------------------------------------------------------------------------
# Record version before upgrade attempt
before_version=$(rpm -q clustr-serverd --queryformat "%{VERSION}-%{RELEASE}" 2>/dev/null || echo "not-installed")
log "Current clustr-serverd: ${before_version}"

log "Running: dnf upgrade -y ${PACKAGES}"
dnf upgrade -y ${PACKAGES} 2>&1

after_version=$(rpm -q clustr-serverd --queryformat "%{VERSION}-%{RELEASE}" 2>/dev/null || echo "not-installed")
log "After upgrade: clustr-serverd ${after_version}"

if [[ "${before_version}" != "${after_version}" ]]; then
    log "New version detected (${before_version} -> ${after_version}). Restarting clustr-serverd..."
    systemctl restart clustr-serverd
    log "clustr-serverd restarted."

    # Brief health check after restart
    sleep 3
    if systemctl is-active --quiet clustr-serverd; then
        log "clustr-serverd is active after restart. OK."
    else
        log "WARNING: clustr-serverd failed to start after upgrade. Check: journalctl -u clustr-serverd"
        exit 1
    fi
else
    log "No update available. clustr-serverd is up to date at ${after_version}."
fi
