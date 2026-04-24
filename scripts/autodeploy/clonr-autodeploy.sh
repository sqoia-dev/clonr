#!/bin/bash
# clonr-autodeploy.sh — Poll origin/main and rebuild if HEAD has moved.
#
# Invoked by clonr-autodeploy.service (Type=oneshot) every 2 minutes via
# clonr-autodeploy.timer.  Idempotent: exits 0 immediately if HEAD already
# matches origin/main.  Exits non-zero on build/health failure so the next
# timer cycle retries automatically.
#
# WARNING: This script performs `git reset --hard origin/main` on every
# detected drift.  Local uncommitted work on this host WILL BE LOST.
# If you need to test uncommitted changes, stop the timer first:
#   systemctl stop clonr-autodeploy.timer
# Resume automatic sync with:
#   systemctl start clonr-autodeploy.timer

set -euo pipefail

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
REPO_DIR="/opt/clonr"
SERVERD_BIN="/usr/local/bin/clonr-serverd"
SERVERD_NEW="${SERVERD_BIN}.autodeploy-new"
CLI_STATIC_BIN="/usr/local/bin/clonr-static"
CLI_STATIC_NEW="${CLI_STATIC_BIN}.autodeploy-new"
CLIENTD_BIN="/usr/local/bin/clonr-clientd"
CLIENTD_NEW="${CLIENTD_BIN}.autodeploy-new"
INITRAMFS_BOOT="/var/lib/clonr/boot/initramfs.img"
INITRAMFS_TFTP="/var/lib/clonr/tftpboot/clonr-initramfs.img"
GOBIN="/usr/local/go/bin/go"
HEALTH_URL="http://localhost:8080/api/v1/nodes"
HEALTH_TIMEOUT=30

# build-initramfs.sh uses sshpass+scp to pull binaries/libs from the clonr-server.
# When autodeploy runs ON the clonr-server itself we create a sshpass shim in
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

SSHPASS_SHIM_DIR=$(mktemp -d /tmp/clonr-autodeploy-shim.XXXXXXXX)
_CLEANUP_DIRS="${SSHPASS_SHIM_DIR}"
trap 'rm -rf ${_CLEANUP_DIRS}; [[ -n "${_AGENT_PID}" ]] && kill "${_AGENT_PID}" 2>/dev/null || true' EXIT
# Shim: sshpass wrapper that strips -p <password> and executes the rest as-is
cat > "${SSHPASS_SHIM_DIR}/sshpass" << 'SHIM_EOF'
#!/bin/bash
# Autodeploy sshpass shim: strip "-p <password>" and run ssh/scp directly.
# Root on 127.0.0.1 has a key-based auth setup via clonr-autodeploy install.
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

export CLONR_SERVER_HOST="127.0.0.1"
export CLONR_SERVER_USER="root"
export CLONR_SERVER_PASS="unused_key_auth_via_shim"

# ---------------------------------------------------------------------------
# Logging helper — all output goes to journal via stdout
# ---------------------------------------------------------------------------
log() { echo "[clonr-autodeploy] $*"; }

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
# Build clonr-serverd
# ---------------------------------------------------------------------------
log "Building clonr-serverd..."
# Write to a staging path so the running binary is never replaced mid-build.
# Capture build output to a temp file to avoid pipefail masking errors from sed.
_BUILD_LOG=$(mktemp /tmp/clonr-build.XXXXXXXX)
_VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
_BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
log "ldflags: -X main.version=${_VERSION} -X main.commitSHA=${_COMMIT} -X main.buildTime=${_BUILD_TIME}"
GOTOOLCHAIN=auto "${GOBIN}" build \
    -ldflags="-X main.version=${_VERSION} -X main.commitSHA=${_COMMIT} -X main.buildTime=${_BUILD_TIME} -s -w" \
    -o "${SERVERD_NEW}" ./cmd/clonr-serverd > "${_BUILD_LOG}" 2>&1 \
    || { sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"; log "ERROR: clonr-serverd build failed"; exit 1; }
sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"
log "clonr-serverd build OK ($(du -h "${SERVERD_NEW}" | cut -f1))"

# ---------------------------------------------------------------------------
# Build static CLI binary (embeds into initramfs)
# ---------------------------------------------------------------------------
log "Building clonr static CLI..."
GOTOOLCHAIN=auto CGO_ENABLED=0 "${GOBIN}" build -o "${CLI_STATIC_NEW}" ./cmd/clonr > "${_BUILD_LOG}" 2>&1 \
    || { sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"; log "ERROR: clonr static CLI build failed"; exit 1; }
sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"
log "clonr static CLI build OK ($(du -h "${CLI_STATIC_NEW}" | cut -f1))"

# ---------------------------------------------------------------------------
# Build clonr-clientd (node agent copied into deployed rootfs during finalize)
# ---------------------------------------------------------------------------
log "Building clonr-clientd..."
GOTOOLCHAIN=auto CGO_ENABLED=0 "${GOBIN}" build \
    -ldflags="-X main.version=${_VERSION} -X main.commitSHA=${_COMMIT} -X main.buildTime=${_BUILD_TIME} -s -w" \
    -o "${CLIENTD_NEW}" ./cmd/clonr-clientd > "${_BUILD_LOG}" 2>&1 \
    || { sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"; log "ERROR: clonr-clientd build failed"; exit 1; }
sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"
log "clonr-clientd build OK ($(du -h "${CLIENTD_NEW}" | cut -f1))"

# ---------------------------------------------------------------------------
# Rebuild initramfs — stage new CLI binary before building
# ---------------------------------------------------------------------------
log "Building initramfs (embedding new clonr CLI)..."
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
# ISO builds via QEMU take 10-15 minutes. Restarting clonr-serverd mid-build
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
    log "Could not reach clonr-serverd API to check build status — proceeding with restart"
fi

# ---------------------------------------------------------------------------
# Atomic replace: server binary + restart service
# ---------------------------------------------------------------------------
log "Replacing clonr-serverd, clonr-static, and clonr-clientd binaries..."
mv "${SERVERD_NEW}" "${SERVERD_BIN}"
mv "${CLI_STATIC_NEW}" "${CLI_STATIC_BIN}"
mv "${CLIENTD_NEW}" "${CLIENTD_BIN}"
# Also update the non-static clonr CLI in-place (dynamic version for operator use)
_BUILD_LOG2=$(mktemp /tmp/clonr-build.XXXXXXXX)
GOTOOLCHAIN=auto "${GOBIN}" build -o /usr/local/bin/clonr ./cmd/clonr > "${_BUILD_LOG2}" 2>&1 \
    || { sed 's/^/  [go-clonr] /' "${_BUILD_LOG2}"; rm -f "${_BUILD_LOG2}"; log "WARNING: dynamic clonr CLI build failed (non-fatal)"; }
sed 's/^/  [go-clonr] /' "${_BUILD_LOG2}"; rm -f "${_BUILD_LOG2}"

systemctl restart clonr-serverd
log "clonr-serverd restarted"

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
log "Waiting for clonr-serverd to become healthy (${HEALTH_TIMEOUT}s timeout)..."
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
    log "ERROR: clonr-serverd did not become healthy within ${HEALTH_TIMEOUT}s (last HTTP code: ${HTTP_CODE:-none})"
    log "       The new binary is in place and the service was restarted — check 'journalctl -u clonr-serverd'"
    log "       The NEXT timer cycle will attempt another sync."
    exit 1
fi

log "Health check passed (HTTP ${HTTP_CODE})"

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
log "Auto-deploy complete: ${LOCAL_SHA} → ${REMOTE_SHA}"
