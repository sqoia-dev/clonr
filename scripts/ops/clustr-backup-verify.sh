#!/usr/bin/env bash
# clustr-backup-verify.sh — weekly backup integrity verification (S4-8 / HA-3)
#
# What it does:
#   1. Finds the most recent DB backup in CLUSTR_BACKUP_DIR.
#   2. Copies it to a temp directory.
#   3. Starts clustr-serverd --db-path <temp> on an alternate port.
#   4. Hits GET /api/v1/healthz/ready (S1-10 readiness endpoint).
#   5. Shuts down the verification instance.
#   6. Logs PASS or FAIL to the system journal.
#   7. On FAIL: emits a WARNING-priority journal entry tagged clustr-backup-verify
#      so operators are alerted before data loss.
#
# Installation:
#   Copy this script to /usr/local/sbin/clustr-backup-verify.sh
#   Install deploy/systemd/clustr-backup-verify.{service,timer} and run:
#     systemctl enable --now clustr-backup-verify.timer
#
# Run as root. The verification instance does not modify production data.

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration — override via environment or edit here
# ---------------------------------------------------------------------------
BACKUP_DIR="${CLUSTR_BACKUP_DIR:-/var/lib/clustr/backups}"
SERVERD_BIN="${CLUSTR_SERVERD_BIN:-/usr/local/bin/clustr-serverd}"
# Port for the ephemeral verification instance. Must not conflict with production.
VERIFY_PORT="${CLUSTR_BACKUP_VERIFY_PORT:-18080}"
# How long to wait for the verification instance to become ready (seconds).
READY_WAIT="${CLUSTR_BACKUP_VERIFY_WAIT:-30}"

TAG="clustr-backup-verify"
TEMP_DIR=""
VERIFY_PID=""

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log() {
    # logger -p daemon.info sends to journal at INFO priority.
    logger -t "${TAG}" -p daemon.info -- "$*"
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] INFO  $*"
}

warn() {
    # logger -p daemon.warning sends to journal at WARNING priority.
    # systemd-cat equivalent that also surfaces in urgent journal queries.
    logger -t "${TAG}" -p daemon.warning -- "WARNING: $*"
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] WARN  $*" >&2
    # Also emit via systemd-cat for operators watching with journalctl -p warning
    systemd-cat -t "${TAG}" -p warning echo "WARNING: $*" 2>/dev/null || true
}

die() {
    warn "$*"
    cleanup
    exit 1
}

cleanup() {
    # Shut down verification instance if still running.
    if [[ -n "${VERIFY_PID}" ]] && kill -0 "${VERIFY_PID}" 2>/dev/null; then
        log "Stopping verification instance (PID ${VERIFY_PID})"
        kill "${VERIFY_PID}" 2>/dev/null || true
        # Give it up to 5s to exit cleanly.
        local deadline=$(( $(date +%s) + 5 ))
        while kill -0 "${VERIFY_PID}" 2>/dev/null && [[ $(date +%s) -lt ${deadline} ]]; do
            sleep 0.5
        done
        kill -9 "${VERIFY_PID}" 2>/dev/null || true
    fi
    # Remove temp dir.
    if [[ -n "${TEMP_DIR}" ]] && [[ -d "${TEMP_DIR}" ]]; then
        rm -rf "${TEMP_DIR}"
        log "Removed temp dir ${TEMP_DIR}"
    fi
}

# Clean up on any exit, including signals.
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
log "Starting backup verification run"

[[ -d "${BACKUP_DIR}" ]] || die "Backup dir not found: ${BACKUP_DIR}"
command -v sqlite3 >/dev/null || die "sqlite3 not in PATH"
command -v curl    >/dev/null || die "curl not in PATH"
[[ -x "${SERVERD_BIN}" ]] || die "clustr-serverd binary not found or not executable: ${SERVERD_BIN}"

# ---------------------------------------------------------------------------
# 1. Find the most recent DB backup
# ---------------------------------------------------------------------------
# Backups are named clustr-YYYYMMDD-HHMMSS.db — sort descending, take first.
LATEST_BACKUP=$(find "${BACKUP_DIR}" -maxdepth 1 -name 'clustr-*.db' -type f \
    | sort -r | head -1)

if [[ -z "${LATEST_BACKUP}" ]]; then
    # No backups yet — log and exit cleanly (not a failure, may be day 1).
    log "No backup files found in ${BACKUP_DIR} — skipping verification (no backups exist yet)"
    exit 0
fi

log "Latest backup: ${LATEST_BACKUP}"

# Quick SQLite integrity check before proceeding.
TABLES=$(sqlite3 "${LATEST_BACKUP}" '.tables' 2>&1) || \
    die "Backup integrity pre-check failed — sqlite3 .tables returned error: ${TABLES}"
log "Backup pre-check OK — tables present"

# ---------------------------------------------------------------------------
# 2. Copy backup to a temp directory
# ---------------------------------------------------------------------------
TEMP_DIR=$(mktemp -d /tmp/clustr-verify-XXXXXX)
TEMP_DB="${TEMP_DIR}/clustr-verify.db"

log "Copying backup to temp path: ${TEMP_DB}"
cp -a "${LATEST_BACKUP}" "${TEMP_DB}"
chmod 600 "${TEMP_DB}"

# Also create the required directory structure the server checks at startup.
mkdir -p "${TEMP_DIR}/boot" "${TEMP_DIR}/images"

# ---------------------------------------------------------------------------
# 3. Start verification instance on alternate port
#    Uses CLUSTR_AUTH_DEV_MODE=1 to bypass auth (loopback only, ephemeral DB).
#    PXE is disabled. Image/boot dirs point to empty temp dirs.
# ---------------------------------------------------------------------------
log "Starting verification instance on 127.0.0.1:${VERIFY_PORT}"

CLUSTR_AUTH_DEV_MODE=1 \
CLUSTR_LISTEN_ADDR="127.0.0.1:${VERIFY_PORT}" \
CLUSTR_DB_PATH="${TEMP_DB}" \
CLUSTR_IMAGE_DIR="${TEMP_DIR}/images" \
CLUSTR_BOOT_DIR="${TEMP_DIR}/boot" \
CLUSTR_TFTP_DIR="${TEMP_DIR}/boot" \
CLUSTR_LOG_ARCHIVE_DIR="${TEMP_DIR}/log-archive" \
CLUSTR_PXE_ENABLED=false \
CLUSTR_LOG_LEVEL=error \
    "${SERVERD_BIN}" &

VERIFY_PID=$!
log "Verification instance PID: ${VERIFY_PID}"

# Give the process a moment to fail fast if startup panics.
sleep 1
if ! kill -0 "${VERIFY_PID}" 2>/dev/null; then
    die "Verification instance exited immediately — backup may be corrupt or migration failed. Check: journalctl -u clustr-backup-verify -n 50"
fi

# ---------------------------------------------------------------------------
# 4. Wait for /api/v1/healthz/ready to return 200
# ---------------------------------------------------------------------------
READY_URL="http://127.0.0.1:${VERIFY_PORT}/api/v1/healthz/ready"
log "Waiting up to ${READY_WAIT}s for ${READY_URL}"

ELAPSED=0
HTTP_STATUS=""
while [[ ${ELAPSED} -lt ${READY_WAIT} ]]; do
    if ! kill -0 "${VERIFY_PID}" 2>/dev/null; then
        die "Verification instance died during readiness wait. Backup restore verification FAILED."
    fi

    HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
        --connect-timeout 2 --max-time 5 \
        "${READY_URL}" 2>/dev/null || echo "000")

    if [[ "${HTTP_STATUS}" == "200" ]]; then
        break
    fi

    sleep 2
    ELAPSED=$(( ELAPSED + 2 ))
done

# ---------------------------------------------------------------------------
# 5. Evaluate result
# ---------------------------------------------------------------------------
if [[ "${HTTP_STATUS}" == "200" ]]; then
    log "PASS — backup verification succeeded. Restored backup responded 200 on /api/v1/healthz/ready within ${ELAPSED}s. Backup: ${LATEST_BACKUP}"
else
    # This is the critical failure path — emit at WARNING so operators see it.
    warn "FAIL — backup verification FAILED. /api/v1/healthz/ready returned ${HTTP_STATUS} (expected 200) after ${ELAPSED}s. Backup: ${LATEST_BACKUP}. Investigate: journalctl -t clustr-backup-verify -n 100"
    exit 1
fi
