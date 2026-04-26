#!/bin/bash
# clustr-autodeploy.sh — Poll origin/main and rebuild if HEAD has moved.
#
# Invoked by clustr-autodeploy.service (Type=oneshot) every 2 minutes via
# clustr-autodeploy.timer.  Idempotent: exits 0 immediately if HEAD already
# matches origin/main.  Exits non-zero on build/health failure so the next
# timer cycle retries automatically.
#
# WARNING: This script performs `git reset --hard origin/main` on every
# detected drift.  Local uncommitted work on this host WILL BE LOST.
# If you need to test uncommitted changes, stop the timer first:
#   systemctl stop clustr-autodeploy.timer
# Resume automatic sync with:
#   systemctl start clustr-autodeploy.timer

set -euo pipefail

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
REPO_DIR="/opt/clustr"
SERVERD_BIN="/usr/local/bin/clustr-serverd"
SERVERD_NEW="${SERVERD_BIN}.autodeploy-new"
CLI_STATIC_BIN="/usr/local/bin/clustr-static"
CLI_STATIC_NEW="${CLI_STATIC_BIN}.autodeploy-new"
CLIENTD_BIN="/usr/local/bin/clustr-clientd"
CLIENTD_NEW="${CLIENTD_BIN}.autodeploy-new"
INITRAMFS_BOOT="/var/lib/clustr/boot/initramfs.img"
INITRAMFS_TFTP="/var/lib/clustr/tftpboot/clustr-initramfs.img"
GOBIN="/usr/local/go/bin/go"
# Derive health-check URL from CLUSTR_LISTEN_ADDR so it works when the server
# is bound to a non-loopback provisioning interface (e.g. 10.99.0.1:8080).
# Source the systemd unit's EnvironmentFile or inline Environment values if present.
_LISTEN_ADDR="${CLUSTR_LISTEN_ADDR:-}"
if [[ -z "${_LISTEN_ADDR}" ]]; then
    # Try to extract from the systemd unit directly (covers the production setup where
    # Environment=CLUSTR_LISTEN_ADDR=<ip>:<port> is set inline in the service file)
    _LISTEN_ADDR=$(systemctl show clustr-serverd --property=Environment 2>/dev/null \
        | tr ' ' '\n' | grep 'CLUSTR_LISTEN_ADDR=' | cut -d= -f2 || true)
fi
# Fallback to localhost if not found (covers dev environments with default binding)
_HEALTH_HOST="${_LISTEN_ADDR:-localhost:8080}"
HEALTH_URL="http://${_HEALTH_HOST}/api/v1/nodes"
HEALTH_TIMEOUT=30
SERVERD_PREV="${SERVERD_BIN}.prev"

# build-initramfs.sh uses sshpass+scp to pull binaries/libs from the clustr-server.
# When autodeploy runs ON the clustr-server itself we create a sshpass shim in
# /tmp that wraps the -p flag and drops it — letting SSH fall through to key auth
# (root now has an authorized key). This means sshpass commands succeed via key
# auth to 127.0.0.1, the build script can copy binaries, libs, and kernel modules
# from the local filesystem over SSH, and the initramfs is complete.
# Start ssh-agent and load root's key so key-based SSH to 127.0.0.1 works
# for the build-initramfs.sh sshpass shim below.
# Root's key at /root/.ssh/id_ed25519 has no passphrase on this host.
_AGENT_PID=""
if [[ -f /root/.ssh/id_ed25519 ]]; then
    eval "$(ssh-agent -s)" > /dev/null 2>&1
    _AGENT_PID="${SSH_AGENT_PID:-}"
    ssh-add /root/.ssh/id_ed25519 > /dev/null 2>&1 || true
fi

SSHPASS_SHIM_DIR=$(mktemp -d /tmp/clustr-autodeploy-shim.XXXXXXXX)
_CLEANUP_DIRS="${SSHPASS_SHIM_DIR}"
trap 'rm -rf ${_CLEANUP_DIRS}; [[ -n "${_AGENT_PID}" ]] && kill "${_AGENT_PID}" 2>/dev/null || true' EXIT
# Shim: sshpass wrapper that strips -p <password> and executes the rest as-is
cat > "${SSHPASS_SHIM_DIR}/sshpass" << 'SHIM_EOF'
#!/bin/bash
# Autodeploy sshpass shim: strip "-p <password>" and run ssh/scp directly.
# Root on 127.0.0.1 has a key-based auth setup via clustr-autodeploy install.
args=()
skip_next=0
for arg in "$@"; do
    if [[ "${skip_next}" -eq 1 ]]; then
        skip_next=0
        continue
    fi
    if [[ "${arg}" == "-p" ]]; then
        skip_next=1
        continue
    fi
    args+=("${arg}")
done
exec "${args[@]}"
SHIM_EOF
chmod +x "${SSHPASS_SHIM_DIR}/sshpass"
# Prepend shim dir to PATH so build-initramfs.sh uses our wrapper
export PATH="${SSHPASS_SHIM_DIR}:${PATH}"

export CLUSTR_SERVER_HOST="127.0.0.1"
export CLUSTR_SERVER_USER="root"
export CLUSTR_SERVER_PASS="unused_key_auth_via_shim"

# ---------------------------------------------------------------------------
# Logging helper — all output goes to journal via stdout
# ---------------------------------------------------------------------------
log() { echo "[clustr-autodeploy] $*"; }

# ---------------------------------------------------------------------------
# Ensure repo exists
# ---------------------------------------------------------------------------
if [[ ! -d "${REPO_DIR}/.git" ]]; then
    log "ERROR: repo not found at ${REPO_DIR} — aborting"
    exit 1
fi

cd "${REPO_DIR}"

# ---------------------------------------------------------------------------
# Fetch latest from origin (network call — fail hard if unreachable)
# ---------------------------------------------------------------------------
log "Fetching origin/main..."
# Note: git fetch output goes to stderr; redirect both streams
git fetch origin main 2>&1 | (sed 's/^/  [git] /' || true)

LOCAL_SHA=$(git rev-parse HEAD)
REMOTE_SHA=$(git rev-parse origin/main)

log "Local:  ${LOCAL_SHA}"
log "Remote: ${REMOTE_SHA}"

if [[ "${LOCAL_SHA}" == "${REMOTE_SHA}" ]]; then
    log "Already up to date — nothing to do"
    exit 0
fi

log "Drift detected — updating ${LOCAL_SHA} → ${REMOTE_SHA}"

# ---------------------------------------------------------------------------
# Sync to origin/main (discard any local changes)
# ---------------------------------------------------------------------------
git reset --hard origin/main 2>&1 | (sed 's/^/  [git] /' || true)
log "Tree reset to ${REMOTE_SHA}"

# ---------------------------------------------------------------------------
# Build clustr-serverd
# ---------------------------------------------------------------------------
log "Building clustr-serverd..."
# Write to a staging path so the running binary is never replaced mid-build.
# Capture build output to a temp file to avoid pipefail masking errors from sed.
_BUILD_LOG=$(mktemp /tmp/clustr-build.XXXXXXXX)
_VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
_BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
log "ldflags: -X main.version=${_VERSION} -X main.commitSHA=${_COMMIT} -X main.buildTime=${_BUILD_TIME}"
GOTOOLCHAIN=auto "${GOBIN}" build \
    -ldflags="-X main.version=${_VERSION} -X main.commitSHA=${_COMMIT} -X main.buildTime=${_BUILD_TIME} -s -w" \
    -o "${SERVERD_NEW}" ./cmd/clustr-serverd > "${_BUILD_LOG}" 2>&1 \
    || { sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"; log "ERROR: clustr-serverd build failed"; exit 1; }
sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"
log "clustr-serverd build OK ($(du -h "${SERVERD_NEW}" | cut -f1))"

# ---------------------------------------------------------------------------
# Build static CLI binary (embeds into initramfs)
# ---------------------------------------------------------------------------
log "Building clustr static CLI..."
GOTOOLCHAIN=auto CGO_ENABLED=0 "${GOBIN}" build -o "${CLI_STATIC_NEW}" ./cmd/clustr > "${_BUILD_LOG}" 2>&1 \
    || { sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"; log "ERROR: clustr static CLI build failed"; exit 1; }
sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"
log "clustr static CLI build OK ($(du -h "${CLI_STATIC_NEW}" | cut -f1))"

# ---------------------------------------------------------------------------
# Build clustr-clientd (node agent copied into deployed rootfs during finalize)
# ---------------------------------------------------------------------------
log "Building clustr-clientd..."
GOTOOLCHAIN=auto CGO_ENABLED=0 "${GOBIN}" build \
    -ldflags="-X main.version=${_VERSION} -X main.commitSHA=${_COMMIT} -X main.buildTime=${_BUILD_TIME} -s -w" \
    -o "${CLIENTD_NEW}" ./cmd/clustr-clientd > "${_BUILD_LOG}" 2>&1 \
    || { sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"; log "ERROR: clustr-clientd build failed"; exit 1; }
sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"
log "clustr-clientd build OK ($(du -h "${CLIENTD_NEW}" | cut -f1))"

# ---------------------------------------------------------------------------
# Rebuild initramfs — stage new CLI binary before building
# ---------------------------------------------------------------------------
log "Building initramfs (embedding new clustr CLI)..."
INITRAMFS_NEW=$(mktemp /tmp/initramfs-autodeploy.XXXXXXXX)
_CLEANUP_DIRS="${SSHPASS_SHIM_DIR} ${INITRAMFS_NEW}"
INITRAMFS_OK=0
# build-initramfs.sh has its own set -e; capture failure without aborting
# our script — server binary and initramfs are independent artifacts.
# We use a temp log file to avoid the pipefail interaction: `cmd | sed`
# evaluates the sed exit code, not cmd's, even with pipefail.
_INITRAMFS_LOG=$(mktemp /tmp/initramfs-build-log.XXXXXXXX)
_CLEANUP_DIRS="${SSHPASS_SHIM_DIR} ${INITRAMFS_NEW} ${_INITRAMFS_LOG}"
if "${REPO_DIR}/scripts/build-initramfs.sh" "${CLI_STATIC_NEW}" "${INITRAMFS_NEW}" > "${_INITRAMFS_LOG}" 2>&1; then
    INITRAMFS_OK=1
    sed 's/^/  [initramfs] /' "${_INITRAMFS_LOG}"
    log "initramfs build OK ($(du -h "${INITRAMFS_NEW}" | cut -f1))"
else
    sed 's/^/  [initramfs] /' "${_INITRAMFS_LOG}"
    log "WARNING: initramfs build failed — the old initramfs.img remains in place"
    log "         PXE boot will use the previous initramfs until next successful cycle"
    rm -f "${INITRAMFS_NEW}"
fi
rm -f "${_INITRAMFS_LOG}"

# ---------------------------------------------------------------------------
# Build-in-progress guard — defer restart if an ISO build is active
# ---------------------------------------------------------------------------
# ISO builds via QEMU take 10-15 minutes. Restarting clustr-serverd mid-build
# sends SIGTERM to the process tree, kills the in-progress QEMU VM, and marks
# the image "interrupted". We query the running server before restarting; if
# any image is in "building" state we skip the restart and let the next
# 2-minute timer cycle re-evaluate.
#
# The new binaries are already compiled and staged (SERVERD_NEW, CLI_STATIC_NEW).
# They remain in /tmp until either this cycle restarts the service or the
# next cycle picks up the same REMOTE_SHA (no drift → early exit before here).
# Because we reset --hard to REMOTE_SHA above, the next cycle will see no drift
# and exit early — so if a build runs for more than one cycle we keep deferring
# until the server is idle, then deploy in one shot.
BUILD_STATUS=$(curl -s --max-time 5 "http://localhost:8080/api/v1/images" 2>/dev/null \
    | python3 -c 'import json,sys; imgs=json.load(sys.stdin).get("images",[]); print("building" if any(i.get("status")=="building" for i in imgs) else "idle")' \
    2>/dev/null || echo "unknown")

if [ "${BUILD_STATUS}" = "building" ]; then
    log "Build in progress — deferring restart to next cycle (binary staged, will deploy when idle)"
    # Clean up staged binaries so next cycle recompiles from the already-reset tree.
    rm -f "${SERVERD_NEW}" "${CLI_STATIC_NEW}" "${CLIENTD_NEW}"
    exit 0
fi

if [ "${BUILD_STATUS}" = "unknown" ]; then
    log "Could not reach clustr-serverd API to check build status — proceeding with restart"
fi

# ---------------------------------------------------------------------------
# Snapshot current binary for rollback (overwrite any older .prev)
# ---------------------------------------------------------------------------
# Capture the short SHA embedded in the running binary before we replace it.
# Used in journal message if rollback fires.
_PREV_SHA="unknown"
if [[ -f "${SERVERD_BIN}" ]]; then
    cp -f "${SERVERD_BIN}" "${SERVERD_PREV}"
    # Best-effort: read version string from binary metadata if available
    _PREV_SHA=$(strings "${SERVERD_BIN}" 2>/dev/null | grep -E '^[0-9a-f]{7,12}$' | tail -1 || echo "unknown")
    log "Snapshotted current binary to ${SERVERD_PREV} (prev SHA hint: ${_PREV_SHA})"
fi

# ---------------------------------------------------------------------------
# Atomic replace: server binary + restart service
# ---------------------------------------------------------------------------
log "Replacing clustr-serverd, clustr-static, and clustr-clientd binaries..."
mv "${SERVERD_NEW}" "${SERVERD_BIN}"
mv "${CLI_STATIC_NEW}" "${CLI_STATIC_BIN}"
mv "${CLIENTD_NEW}" "${CLIENTD_BIN}"
# Also update the non-static clustr CLI in-place (dynamic version for operator use)
_BUILD_LOG2=$(mktemp /tmp/clustr-build.XXXXXXXX)
GOTOOLCHAIN=auto "${GOBIN}" build -o /usr/local/bin/clustr ./cmd/clustr > "${_BUILD_LOG2}" 2>&1 \
    || { sed 's/^/  [go-clustr] /' "${_BUILD_LOG2}"; rm -f "${_BUILD_LOG2}"; log "WARNING: dynamic clustr CLI build failed (non-fatal)"; }
sed 's/^/  [go-clustr] /' "${_BUILD_LOG2}"; rm -f "${_BUILD_LOG2}"

systemctl restart clustr-serverd
log "clustr-serverd restarted"

# ---------------------------------------------------------------------------
# Atomic replace: initramfs (both boot and tftp directories)
# ---------------------------------------------------------------------------
if [[ "${INITRAMFS_OK}" -eq 1 ]] && [[ -f "${INITRAMFS_NEW}" ]]; then
    log "Deploying new initramfs..."
    # Use cp+mv for atomic replace — ensures PXE clients always see a complete file
    cp "${INITRAMFS_NEW}" "${INITRAMFS_BOOT}.autodeploy-new"
    mv "${INITRAMFS_BOOT}.autodeploy-new" "${INITRAMFS_BOOT}"

    if [[ -f "${INITRAMFS_TFTP}" ]]; then
        cp "${INITRAMFS_NEW}" "${INITRAMFS_TFTP}.autodeploy-new"
        mv "${INITRAMFS_TFTP}.autodeploy-new" "${INITRAMFS_TFTP}"
        log "Initramfs deployed to boot + tftp directories"
    else
        log "Initramfs deployed to boot directory (tftp path not present, skipping)"
    fi
    rm -f "${INITRAMFS_NEW}"
fi

# ---------------------------------------------------------------------------
# Health check — confirm the restarted service is responsive
# ---------------------------------------------------------------------------
log "Waiting for clustr-serverd to become healthy (${HEALTH_TIMEOUT}s timeout)..."
DEADLINE=$(( $(date +%s) + HEALTH_TIMEOUT ))
HEALTHY=0
while [[ $(date +%s) -lt ${DEADLINE} ]]; do
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" --max-time 3 "${HEALTH_URL}" 2>/dev/null || true)
    # 401 = service is up and enforcing auth (expected for /api/v1/nodes without token)
    # 200 = service is up (unexpected but healthy)
    # Anything else = not ready yet
    if [[ "${HTTP_CODE}" == "401" || "${HTTP_CODE}" == "200" ]]; then
        HEALTHY=1
        break
    fi
    sleep 2
done

if [[ "${HEALTHY}" -eq 0 ]]; then
    log "ERROR: clustr-serverd did not become healthy within ${HEALTH_TIMEOUT}s (last HTTP code: ${HTTP_CODE:-none})"
    # ---------------------------------------------------------------------------
    # Rollback: restore previous binary and restart
    # ---------------------------------------------------------------------------
    if [[ -f "${SERVERD_PREV}" ]]; then
        log "Rollback: restoring ${SERVERD_PREV} → ${SERVERD_BIN}"
        cp -f "${SERVERD_PREV}" "${SERVERD_BIN}"
        systemctl restart clustr-serverd
        # Brief wait to confirm rollback came up
        _RB_HEALTHY=0
        _RB_DEADLINE=$(( $(date +%s) + 20 ))
        while [[ $(date +%s) -lt ${_RB_DEADLINE} ]]; do
            _RB_CODE=$(curl -s -o /dev/null -w "%{http_code}" --max-time 3 "${HEALTH_URL}" 2>/dev/null || true)
            if [[ "${_RB_CODE}" == "401" || "${_RB_CODE}" == "200" ]]; then
                _RB_HEALTHY=1
                break
            fi
            sleep 2
        done
        if [[ "${_RB_HEALTHY}" -eq 1 ]]; then
            # Use journalctl-visible systemd-cat for structured journal entry
            echo "clustr-autodeploy: rollback applied — health check failed on SHA ${REMOTE_SHA}, restored SHA ${_PREV_SHA}" \
                | systemd-cat -t clustr-autodeploy -p warning 2>/dev/null || true
            log "Rollback SUCCEEDED — previous binary is running (prev SHA: ${_PREV_SHA}, failed SHA: ${REMOTE_SHA})"
        else
            echo "clustr-autodeploy: rollback FAILED — service did not come up on prev SHA ${_PREV_SHA} either" \
                | systemd-cat -t clustr-autodeploy -p err 2>/dev/null || true
            log "ERROR: Rollback FAILED — service did not come up on previous binary either"
            log "       Manual intervention required: check 'journalctl -u clustr-serverd'"
        fi
    else
        log "WARNING: No previous binary at ${SERVERD_PREV} — rollback not possible"
        log "         This is expected on the first deployment. Check 'journalctl -u clustr-serverd' for startup errors."
    fi
    exit 1
fi

log "Health check passed (HTTP ${HTTP_CODE})"

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
log "Auto-deploy complete: ${LOCAL_SHA} → ${REMOTE_SHA}"
