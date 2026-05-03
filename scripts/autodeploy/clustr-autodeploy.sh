#!/bin/bash
# clustr-autodeploy.sh — Poll origin/main and rebuild if HEAD has moved.
#
# Invoked by clustr-autodeploy.service (Type=oneshot) every 5 minutes via
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
PRIVHELPER_BIN="/usr/sbin/clustr-privhelper"
PRIVHELPER_NEW="${PRIVHELPER_BIN}.autodeploy-new"

# Bundle-sync circuit-breaker state file.
# Stores the number of consecutive bundle-install failures.  After
# BUNDLE_FAIL_LIMIT failures the autodeploy stops retrying and logs a warning.
BUNDLE_FAIL_COUNTER="/var/lib/clustr/bundle-install-failures"
BUNDLE_FAIL_LIMIT=3
INITRAMFS_BOOT="/var/lib/clustr/boot/initramfs.img"
INITRAMFS_TFTP="/var/lib/clustr/tftpboot/clustr-initramfs.img"
GOBIN="/usr/local/go/bin/go"
# Derive health-check URL from CLUSTR_LISTEN_ADDR so it works when the server
# is bound to a non-loopback provisioning interface (e.g. 10.99.0.1:8080).
# Source the systemd unit's EnvironmentFile or inline Environment values if present.
_LISTEN_ADDR="${CLUSTR_LISTEN_ADDR:-}"
if [[ -z "${_LISTEN_ADDR}" ]]; then
    # Extract from systemd unit. The property output is:
    #   Environment=KEY=val KEY2=val2 ...
    # Strip the leading "Environment=" then split on spaces to find the
    # CLUSTR_LISTEN_ADDR token, then take everything after the first "=".
    _LISTEN_ADDR=$(systemctl show clustr-serverd --property=Environment 2>/dev/null \
        | sed 's/^Environment=//' \
        | tr ' ' '\n' \
        | grep '^CLUSTR_LISTEN_ADDR=' \
        | cut -d= -f2- || true)
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
# Collect changed paths between old and new HEAD (before reset)
# Used by the fast-path check and the autodeploy-script self-update below.
# ---------------------------------------------------------------------------
CHANGED_PATHS=$(git diff --name-only "${LOCAL_SHA}" "${REMOTE_SHA}" 2>/dev/null || true)
log "Changed paths (${LOCAL_SHA:0:7} → ${REMOTE_SHA:0:7}):"
echo "${CHANGED_PATHS}" | sed 's/^/  /'

# ---------------------------------------------------------------------------
# OPS-6: Self-update — if the autodeploy script or install.sh itself changed,
# run install.sh first to update /usr/local/sbin/clustr-autodeploy.sh, then
# exit so the NEXT cycle runs the freshly-installed script.
#
# We must reset --hard before running install.sh so the repo copy is current.
# ---------------------------------------------------------------------------
if echo "${CHANGED_PATHS}" | grep -qE "^scripts/autodeploy/(clustr-autodeploy|install)\.sh$"; then
    log "Autodeploy script itself changed — syncing repo then running install.sh"
    git reset --hard origin/main 2>&1 | (sed 's/^/  [git] /' || true)
    log "Tree reset to ${REMOTE_SHA}"
    log "Running install.sh to update /usr/local/sbin/clustr-autodeploy.sh..."
    bash "${REPO_DIR}/scripts/autodeploy/install.sh" 2>&1 | (sed 's/^/  [install] /' || true)
    log "install.sh complete — exiting cycle; next cycle runs the new script"
    exit 0
fi

# ---------------------------------------------------------------------------
# Sync to origin/main (discard any local changes)
# ---------------------------------------------------------------------------
git reset --hard origin/main 2>&1 | (sed 's/^/  [git] /' || true)
log "Tree reset to ${REMOTE_SHA}"

# ---------------------------------------------------------------------------
# Fast path: skip full rebuild + restart for docs/config-only changes.
#
# If every changed file matches the safe-skip set (docs/, *.md at root,
# .github/, .gitignore, CLAUDE.md, README.md) the running clustr-serverd
# binary does not need to be replaced and the service should not be
# restarted. A restart is disruptive: it breaks active sessions (until
# CLUSTR_SESSION_SECRET is stable) and interrupts in-flight operations.
#
# Scope decision: docs-only (not docs+web). Web changes require a binary
# rebuild due to //go:embed all:dist; staging a new binary without restarting
# adds complexity for minimal gain. The operator can hit "Restart Server" in
# the webapp or wait for the next non-docs commit to carry web changes live.
# This fast path only fires when 100% of the diff is in the safe set.
# ---------------------------------------------------------------------------
_SAFE_SKIP_REGEX='^(docs/|\.github/|\.gitignore$|README\.md$|CLAUDE\.md$|[^/]+\.md$)'
_ALL_SAFE=1
while IFS= read -r _path; do
    [[ -z "${_path}" ]] && continue
    if ! echo "${_path}" | grep -qE "${_SAFE_SKIP_REGEX}"; then
        _ALL_SAFE=0
        break
    fi
done <<< "${CHANGED_PATHS}"

if [[ "${_ALL_SAFE}" -eq 1 ]] && [[ -n "${CHANGED_PATHS}" ]]; then
    log "All changes are docs/config-only — skipping rebuild and service restart"
    log "  (scope: docs/, *.md at root, .github/, .gitignore, CLAUDE.md)"
    log "  Web changes are NOT in the fast-path — push a non-docs commit to carry web changes live"
    log "Fast-path sync complete: ${LOCAL_SHA} → ${REMOTE_SHA}"
    exit 0
fi

# ---------------------------------------------------------------------------
# Ensure Node.js 24 + pnpm 10 are available
# internal/server/web/dist/ is a build artifact — NOT committed to git.
# We must build it here before `go build` so the embed.FS has current content.
# ---------------------------------------------------------------------------
NODE_BIN="/usr/local/node/bin/node"
NPM_BIN="/usr/local/node/bin/npm"
PNPM_BIN="/usr/local/node/bin/pnpm"
NODE_VERSION_TARGET="24"
NODE_INSTALL_DIR="/usr/local/node"

if [[ ! -x "${NODE_BIN}" ]] || ! "${NODE_BIN}" --version 2>/dev/null | grep -q "^v${NODE_VERSION_TARGET}"; then
    log "Node.js ${NODE_VERSION_TARGET} not found — installing via nvm tarball..."
    NODE_TARBALL_URL="https://nodejs.org/dist/latest-v${NODE_VERSION_TARGET}.x/node-v${NODE_VERSION_TARGET}."
    # Resolve exact version
    _NODE_EXACT=$(curl -s "https://nodejs.org/dist/latest-v${NODE_VERSION_TARGET}.x/" \
        | grep -oE "node-v[0-9]+\.[0-9]+\.[0-9]+" | sed 's/^node-v//' | head -1)
    if [[ -z "${_NODE_EXACT}" ]]; then
        log "ERROR: could not resolve Node.js ${NODE_VERSION_TARGET} version from nodejs.org"
        exit 1
    fi
    _NODE_TGZ="node-v${_NODE_EXACT}-linux-x64.tar.xz"
    _NODE_URL="https://nodejs.org/dist/v${_NODE_EXACT}/${_NODE_TGZ}"
    log "Downloading ${_NODE_URL}..."
    _NODE_TMPDIR=$(mktemp -d /tmp/node-install.XXXXXXXX)
    _CLEANUP_DIRS="${SSHPASS_SHIM_DIR} ${_NODE_TMPDIR}"
    curl -fsSL "${_NODE_URL}" -o "${_NODE_TMPDIR}/${_NODE_TGZ}"
    rm -rf "${NODE_INSTALL_DIR}"
    mkdir -p "${NODE_INSTALL_DIR}"
    tar -xJf "${_NODE_TMPDIR}/${_NODE_TGZ}" -C "${NODE_INSTALL_DIR}" --strip-components=1
    rm -rf "${_NODE_TMPDIR}"
    log "Node.js v${_NODE_EXACT} installed at ${NODE_INSTALL_DIR}"
fi
export PATH="${NODE_INSTALL_DIR}/bin:${PATH}"

if [[ ! -x "${PNPM_BIN}" ]]; then
    log "pnpm not found — installing via npm..."
    "${NPM_BIN}" install -g pnpm@10 --prefix "${NODE_INSTALL_DIR}" > /dev/null 2>&1
    log "pnpm installed"
fi

# ---------------------------------------------------------------------------
# Build web assets and copy to Go embed target
# ---------------------------------------------------------------------------
log "Building web assets (pnpm install + pnpm build)..."
_WEB_LOG=$(mktemp /tmp/clustr-web-build.XXXXXXXX)
(
    cd "${REPO_DIR}/web"
    "${PNPM_BIN}" install --frozen-lockfile 2>&1
    "${PNPM_BIN}" build 2>&1
) > "${_WEB_LOG}" 2>&1 \
    || { sed 's/^/  [web] /' "${_WEB_LOG}"; rm -f "${_WEB_LOG}"; log "ERROR: web build failed"; exit 1; }
sed 's/^/  [web] /' "${_WEB_LOG}"; rm -f "${_WEB_LOG}"
log "web build OK"

log "Copying web/dist → internal/server/web/dist..."
rm -rf "${REPO_DIR}/internal/server/web/dist"
cp -r "${REPO_DIR}/web/dist" "${REPO_DIR}/internal/server/web/dist"
log "web dist copied ($(du -sh "${REPO_DIR}/internal/server/web/dist" | cut -f1))"

# ---------------------------------------------------------------------------
# Build clustr-serverd
# ---------------------------------------------------------------------------
log "Building clustr-serverd..."
# Build with explicit bundle ldflags read from build/slurm/versions.yml and the
# Makefile's pinned BUNDLE_SHA256.  This ensures the binary embeds the correct
# builtinSlurmBundleVersion + builtinSlurmBundleSHA256 so the bundle-sync step
# below can compare them against the currently installed bundle without a
# separate subcommand or strings extraction.
_BUILD_LOG=$(mktemp /tmp/clustr-build.XXXXXXXX)
_VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
_BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
_SLURM_VERSION=$(awk '/^slurm:/{in_slurm=1} in_slurm && /version:/{gsub(/[" ]/, "", $2); print $2; exit}' \
    "${REPO_DIR}/build/slurm/versions.yml")
_CLUSTR_RELEASE=$(awk '/^clustr_release:/{gsub(/[" ]/, "", $2); print $2; exit}' \
    "${REPO_DIR}/build/slurm/versions.yml")
_BUNDLE_VERSION="v${_SLURM_VERSION}-clustr${_CLUSTR_RELEASE}"
# Read pinned SHA256 from the Makefile (same source the Makefile uses at build time).
# Line format: BUNDLE_SHA256  ?= <hex>
_BUNDLE_SHA256=$(awk '/^BUNDLE_SHA256[[:space:]]*\?=/{gsub(/[[:space:]]/, "", $0); sub(/.*\?=/, ""); print; exit}' \
    "${REPO_DIR}/Makefile")
log "bundle ldflags: builtinSlurmBundleVersion=${_BUNDLE_VERSION} sha256=${_BUNDLE_SHA256:0:12}..."
GOTOOLCHAIN=auto "${GOBIN}" build \
    -tags webdist \
    -ldflags="-X main.version=${_VERSION} \
              -X main.commitSHA=${_COMMIT} \
              -X main.buildTime=${_BUILD_TIME} \
              -X main.builtinSlurmVersion=${_SLURM_VERSION} \
              -X main.builtinSlurmBundleVersion=${_BUNDLE_VERSION} \
              -X main.builtinSlurmBundleSHA256=${_BUNDLE_SHA256} \
              -s -w" \
    -o "${SERVERD_NEW}" ./cmd/clustr-serverd > "${_BUILD_LOG}" 2>&1 \
    || { sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"; log "ERROR: clustr-serverd build failed"; exit 1; }
sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"
log "clustr-serverd build OK ($(du -h "${SERVERD_NEW}" | cut -f1)) bundle=${_BUNDLE_VERSION}"

# ---------------------------------------------------------------------------
# Pre-deploy web-embed guard — fail loud if binary contains the stub string.
#
# The stub string "Web bundle not built" is only present when the binary was
# compiled WITHOUT -tags webdist.  This fires before the binary is installed,
# so a bad build is caught here rather than after a service restart that takes
# the live server down.
#
# Cause: either the go build line above lost -tags webdist (edit error), or
# internal/server/web/dist/ was absent/empty when the build ran.
# ---------------------------------------------------------------------------
_STUB_COUNT=$(strings "${SERVERD_NEW}" 2>/dev/null | grep -c "Web bundle not built" || true)
if [[ "${_STUB_COUNT}" -gt 0 ]]; then
    log "ERROR: web-embed guard — new binary contains stub string 'Web bundle not built'"
    log "       This means the binary was built without -tags webdist or web/dist was empty."
    log "       The bad binary has NOT been deployed.  Aborting cycle."
    echo "clustr-autodeploy: web-embed guard triggered — binary contains stub; deploy aborted" \
        | systemd-cat -t clustr-autodeploy -p err 2>/dev/null || true
    rm -f "${SERVERD_NEW}"
    exit 1
fi
log "web-embed guard: OK — stub string absent from new binary"

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
# Build clustr-privhelper (setuid root privilege boundary — installed to /usr/sbin)
# ---------------------------------------------------------------------------
log "Building clustr-privhelper..."
GOTOOLCHAIN=auto CGO_ENABLED=0 "${GOBIN}" build \
    -ldflags="-X main.version=${_VERSION} -X main.commitSHA=${_COMMIT} -X main.buildTime=${_BUILD_TIME} -s -w" \
    -o "${PRIVHELPER_NEW}" ./cmd/clustr-privhelper > "${_BUILD_LOG}" 2>&1 \
    || { sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"; log "ERROR: clustr-privhelper build failed"; exit 1; }
sed 's/^/  [go] /' "${_BUILD_LOG}"; rm -f "${_BUILD_LOG}"
log "clustr-privhelper build OK ($(du -h "${PRIVHELPER_NEW}" | cut -f1))"

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
BUILD_STATUS=$(curl -s --max-time 5 "http://${_HEALTH_HOST}/api/v1/images" 2>/dev/null \
    | python3 -c 'import json,sys; imgs=json.load(sys.stdin).get("images",[]); print("building" if any(i.get("status")=="building" for i in imgs) else "idle")' \
    2>/dev/null || echo "unknown")

if [ "${BUILD_STATUS}" = "building" ]; then
    log "Build in progress — deferring restart to next cycle (binary staged, will deploy when idle)"
    # Clean up staged binaries so next cycle recompiles from the already-reset tree.
    rm -f "${SERVERD_NEW}" "${CLI_STATIC_NEW}" "${CLIENTD_NEW}" "${PRIVHELPER_NEW}"
    exit 0
fi

if [ "${BUILD_STATUS}" = "unknown" ]; then
    log "Could not reach clustr-serverd API to check build status — proceeding with restart"
fi

# ---------------------------------------------------------------------------
# Reimage-in-progress guard — defer restart if any node reimage is active
# ---------------------------------------------------------------------------
# Restarting clustr-serverd while a reimage is running interrupts the
# orchestrator goroutine and may leave a node mid-deploy with no server to
# report back to. Query /api/v1/reimages for non-terminal records.
# If the endpoint is unreachable or returns 401 (auth required), we treat
# the result as "unknown" and proceed — same fail-open pattern as the build
# guard above.
REIMAGE_STATUS=$(curl -s --max-time 5 "http://${_HEALTH_HOST}/api/v1/reimages" 2>/dev/null \
    | python3 -c '
import json, sys
try:
    data = json.load(sys.stdin)
    reqs = data.get("requests", [])
    non_terminal = {"pending", "triggered", "in_progress", "running"}
    if any(r.get("status") in non_terminal for r in reqs):
        print("active")
    else:
        print("idle")
except Exception:
    print("unknown")
' 2>/dev/null || echo "unknown")

if [ "${REIMAGE_STATUS}" = "active" ]; then
    log "Reimage in progress — deferring restart to next cycle (binary staged, will deploy when idle)"
    rm -f "${SERVERD_NEW}" "${CLI_STATIC_NEW}" "${CLIENTD_NEW}" "${PRIVHELPER_NEW}"
    exit 0
fi

if [ "${REIMAGE_STATUS}" = "unknown" ]; then
    log "Could not reach clustr-serverd API to check reimage status — proceeding with restart"
fi

# ---------------------------------------------------------------------------
# Initramfs-build guard — defer restart if an initramfs rebuild is active
# ---------------------------------------------------------------------------
# Initramfs builds via POST /system/initramfs/rebuild take 3-8 minutes.
# Restarting mid-build kills the in-progress script and leaves the old
# initramfs on disk (BUG-1 previously reported this as "interrupted").
#
# We poll the unauthenticated GET /api/v1/system/active-jobs endpoint, which
# returns {"initramfs_builds":[...],"image_builds":[...],"deploys":[...]}.
# A non-empty initramfs_builds array means a build is in flight.
#
# Defer cap: after INITRAMFS_DEFER_CAP consecutive deferred cycles (default
# 60 × 5 min = 5 hours) we assume the build is stuck/abandoned and proceed
# with the restart anyway.  The cap counter is reset on every successful
# restart.  If the server is unreachable (curl fails) we treat the result
# as "safe to restart" so an already-dead server is never blocked.
#
# Bootstrap note: this script is itself delivered via autodeploy.  The first
# cycle after a push that includes this new guard will apply the new script
# atomically via git reset --hard, then run the guard.  If an initramfs build
# was already in flight at that moment, the guard will correctly defer.
# There is no risk of a defer-loop on the first cycle: the cap counter starts
# at 0 and the previous cycle's binaries are already cleaned up at exit 0.
INITRAMFS_DEFER_CAP=60
INITRAMFS_DEFER_FILE="/var/lib/clustr/autodeploy-initramfs-defer-count"

_ACTIVE_JOBS=$(curl -sf --max-time 5 "http://${_HEALTH_HOST}/api/v1/system/active-jobs" 2>/dev/null || echo "")

if [ -n "${_ACTIVE_JOBS}" ]; then
    _INITRAMFS_ACTIVE=$(echo "${_ACTIVE_JOBS}" \
        | python3 -c 'import json,sys; d=json.load(sys.stdin); print(len(d.get("initramfs_builds",[])))' \
        2>/dev/null || echo "0")
    # BUG-18: also gate on in-progress node-initiated deploys (deploys field).
    # These are tracked in ProgressStore and may not have a reimage_requests row.
    _DEPLOYS_ACTIVE=$(echo "${_ACTIVE_JOBS}" \
        | python3 -c 'import json,sys; d=json.load(sys.stdin); print(len(d.get("deploys",[])))' \
        2>/dev/null || echo "0")
else
    # Server unreachable — fail open (safe to restart a server that isn't running).
    _INITRAMFS_ACTIVE="0"
    _DEPLOYS_ACTIVE="0"
    log "Could not reach clustr-serverd active-jobs endpoint — assuming safe to restart"
fi

# Defer restart if any initramfs build OR active node deploy is in progress.
_TOTAL_BLOCKING=$(( ${_INITRAMFS_ACTIVE:-0} + ${_DEPLOYS_ACTIVE:-0} ))

if [ "${_TOTAL_BLOCKING}" -gt 0 ] 2>/dev/null; then
    # Read defer counter (treat missing/corrupt file as 0).
    _RAW_DEFER=$(cat "${INITRAMFS_DEFER_FILE}" 2>/dev/null || echo "0")
    if ! echo "${_RAW_DEFER}" | grep -qE '^[0-9]+$'; then
        _RAW_DEFER=0
    fi

    if [ "${_RAW_DEFER}" -ge "${INITRAMFS_DEFER_CAP}" ]; then
        log "WARNING: active jobs have blocked restart for ${_RAW_DEFER} consecutive cycles \
(${_RAW_DEFER} × 5 min ≈ $(( _RAW_DEFER * 5 )) min) — appears stuck; proceeding with restart"
        echo "clustr-autodeploy: defer cap reached after ${_RAW_DEFER} cycles — forcing restart" \
            | systemd-cat -t clustr-autodeploy -p warning 2>/dev/null || true
        rm -f "${INITRAMFS_DEFER_FILE}"
        # Fall through to restart below.
    else
        _NEXT_DEFER=$(( _RAW_DEFER + 1 ))
        _DEFER_REASON=""
        [ "${_INITRAMFS_ACTIVE:-0}" -gt 0 ] && _DEFER_REASON="${_DEFER_REASON}initramfs_build "
        [ "${_DEPLOYS_ACTIVE:-0}" -gt 0 ] && _DEFER_REASON="${_DEFER_REASON}active_deploy "
        log "Active jobs blocking restart (${_DEFER_REASON%% }) — deferring (cycle ${_NEXT_DEFER}/${INITRAMFS_DEFER_CAP}, will retry next tick)"
        echo "${_NEXT_DEFER}" > "${INITRAMFS_DEFER_FILE}" 2>/dev/null || true
        rm -f "${SERVERD_NEW}" "${CLI_STATIC_NEW}" "${CLIENTD_NEW}" "${PRIVHELPER_NEW}"
        exit 0
    fi
else
    # No active blocking jobs — clear the defer counter so the cap resets cleanly.
    rm -f "${INITRAMFS_DEFER_FILE}"
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
log "Replacing clustr-serverd, clustr-static, clustr-clientd, and clustr-privhelper binaries..."
mv "${SERVERD_NEW}" "${SERVERD_BIN}"
mv "${CLI_STATIC_NEW}" "${CLI_STATIC_BIN}"
mv "${CLIENTD_NEW}" "${CLIENTD_BIN}"
# privhelper: mv into /usr/sbin then set 4755 (setuid root) in-place.
# The setuid bit must be applied AFTER mv — some kernels strip it during mv
# across mount boundaries (e.g. /tmp → /usr/sbin on separate tmpfs).
# owner:group must be root:root; chmod 4755 = setuid + rwxr-xr-x.
mv "${PRIVHELPER_NEW}" "${PRIVHELPER_BIN}"
chown root:root "${PRIVHELPER_BIN}"
chmod 4755 "${PRIVHELPER_BIN}"
log "clustr-privhelper installed: $(ls -la "${PRIVHELPER_BIN}")"
# Also update the non-static clustr CLI in-place (dynamic version for operator use)
_BUILD_LOG2=$(mktemp /tmp/clustr-build.XXXXXXXX)
GOTOOLCHAIN=auto "${GOBIN}" build -o /usr/local/bin/clustr ./cmd/clustr > "${_BUILD_LOG2}" 2>&1 \
    || { sed 's/^/  [go-clustr] /' "${_BUILD_LOG2}"; rm -f "${_BUILD_LOG2}"; log "WARNING: dynamic clustr CLI build failed (non-fatal)"; }
sed 's/^/  [go-clustr] /' "${_BUILD_LOG2}"; rm -f "${_BUILD_LOG2}"

# ---------------------------------------------------------------------------
# Bundle-sync: ensure the installed Slurm bundle matches the version embedded
# in the new clustr-serverd binary before restarting the service.
#
# Why here (after binary replacement, before restart):
#   The new binary serves /repo/ from /var/lib/clustr/repo/.  If the bundle
#   version it expects differs from what is installed, deployed nodes will get
#   a repo whose metadata does not match the binary's expectations.  Install
#   the correct bundle first so the server comes up in a consistent state.
#
# Version detection strategy:
#   We already computed _BUNDLE_VERSION and _BUNDLE_SHA256 from versions.yml
#   during the build step above.  The installed version is in the JSON at
#   /var/lib/clustr/repo/el9-x86_64/.installed-version (keyed on bundle_sha256).
#   Compare SHA256 values — if they match, skip.  If not, install.
#
# Circuit breaker:
#   A counter at ${BUNDLE_FAIL_COUNTER} tracks consecutive failures.
#   After ${BUNDLE_FAIL_LIMIT} failures in a row we stop retrying for this
#   cycle and log a warning, but still restart clustr-serverd so the existing
#   repo (if any) continues to serve.
# ---------------------------------------------------------------------------
_BUNDLE_INSTALL_NEEDED=0
_INSTALLED_BUNDLE_SHA=""
_REPO_VERSION_FILE="/var/lib/clustr/repo/el9-x86_64/.installed-version"

if [[ -f "${_REPO_VERSION_FILE}" ]]; then
    _INSTALLED_BUNDLE_SHA=$(python3 -c "
import json, sys
try:
    d = json.load(open('${_REPO_VERSION_FILE}'))
    print(d.get('bundle_sha256', ''))
except Exception:
    print('')
" 2>/dev/null || true)
fi

if [[ -z "${_INSTALLED_BUNDLE_SHA}" ]]; then
    log "bundle-sync: no installed bundle found — install required"
    _BUNDLE_INSTALL_NEEDED=1
elif [[ "${_INSTALLED_BUNDLE_SHA}" != "${_BUNDLE_SHA256}" ]]; then
    log "bundle-sync: installed SHA ${_INSTALLED_BUNDLE_SHA:0:12}... != builtin SHA ${_BUNDLE_SHA256:0:12}... — upgrade required"
    _BUNDLE_INSTALL_NEEDED=1
else
    log "bundle-sync: bundle ${_BUNDLE_VERSION} already current (SHA ${_BUNDLE_SHA256:0:12}...) — skipping"
    # Reset failure counter on confirmed match
    echo "0" > "${BUNDLE_FAIL_COUNTER}" 2>/dev/null || true
fi

if [[ "${_BUNDLE_INSTALL_NEEDED}" -eq 1 ]]; then
    # Check circuit breaker
    _FAIL_COUNT=0
    if [[ -f "${BUNDLE_FAIL_COUNTER}" ]]; then
        _RAW=$(cat "${BUNDLE_FAIL_COUNTER}" 2>/dev/null || true)
        # Accept only a bare integer; treat anything else as 0 (corrupt/empty file)
        if [[ "${_RAW}" =~ ^[0-9]+$ ]]; then
            _FAIL_COUNT="${_RAW}"
        fi
    fi

    if [[ "${_FAIL_COUNT}" -ge "${BUNDLE_FAIL_LIMIT}" ]]; then
        log "WARNING: bundle-sync: circuit breaker open after ${_FAIL_COUNT} consecutive failures"
        log "         Target bundle: slurm-${_BUNDLE_VERSION} (SHA ${_BUNDLE_SHA256:0:12}...)"
        log "         Manual fix: run 'clustr-serverd bundle install' on this host, then reset"
        log "         counter with: echo 0 > ${BUNDLE_FAIL_COUNTER}"
        echo "clustr-autodeploy: bundle-sync circuit breaker open — ${_FAIL_COUNT} consecutive failures; manual intervention required" \
            | systemd-cat -t clustr-autodeploy -p warning 2>/dev/null || true
    else
        log "bundle-sync: installing bundle slurm-${_BUNDLE_VERSION}..."
        _BUNDLE_LOG=$(mktemp /tmp/clustr-bundle.XXXXXXXX)
        if "${SERVERD_BIN}" bundle install > "${_BUNDLE_LOG}" 2>&1; then
            sed 's/^/  [bundle] /' "${_BUNDLE_LOG}"; rm -f "${_BUNDLE_LOG}"
            log "bundle-sync: install complete — bundle ${_BUNDLE_VERSION} is live"
            echo "0" > "${BUNDLE_FAIL_COUNTER}"
        else
            sed 's/^/  [bundle] /' "${_BUNDLE_LOG}"; rm -f "${_BUNDLE_LOG}"
            _FAIL_COUNT=$(( _FAIL_COUNT + 1 ))
            echo "${_FAIL_COUNT}" > "${BUNDLE_FAIL_COUNTER}"
            log "WARNING: bundle-sync: install failed (attempt ${_FAIL_COUNT}/${BUNDLE_FAIL_LIMIT})"
            log "         The existing bundle (if any) remains in place."
            log "         clustr-serverd will restart with whatever bundle is currently installed."
            echo "clustr-autodeploy: bundle install failed (attempt ${_FAIL_COUNT}/${BUNDLE_FAIL_LIMIT}) for ${_BUNDLE_VERSION}" \
                | systemd-cat -t clustr-autodeploy -p warning 2>/dev/null || true
            # Do NOT exit — restart clustr-serverd regardless so other changes take effect.
        fi
    fi
fi

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

    # Always ensure the TFTP directory exists and copy the initramfs there.
    # clustr-serverd creates /var/lib/clustr/tftpboot at startup (via os.MkdirAll),
    # but the autodeploy timer may run before or after the first serverd start.
    # Using mkdir -p here makes the copy unconditional and idempotent regardless
    # of service startup ordering.
    mkdir -p "$(dirname "${INITRAMFS_TFTP}")"
    cp "${INITRAMFS_NEW}" "${INITRAMFS_TFTP}.autodeploy-new"
    mv "${INITRAMFS_TFTP}.autodeploy-new" "${INITRAMFS_TFTP}"
    log "Initramfs deployed to boot + tftp directories"
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
# Post-restart stub-body check — confirm the running server is not serving
# the "Web bundle not built" stub page.
#
# The health-check URL (/api/v1/nodes) returns 401 — it does not exercise the
# SPA handler.  We separately fetch / and check the body.  If the stub string
# is present the binary was deployed without -tags webdist; this should now
# be caught by the pre-deploy guard above, but we check again here as a
# belt-and-suspenders defence against silent regressions (e.g. someone
# manually replaces the binary between build and restart).
#
# On failure: log LOUDLY to journal and exit non-zero so the next timer cycle
# retries the full build.  We do NOT rollback here because the previous binary
# may itself serve the stub (as happened in REGRESSION-2); a re-build with the
# correct flags is the only reliable fix.
# ---------------------------------------------------------------------------
_ROOT_BODY=$(curl -s --max-time 5 "http://${_HEALTH_HOST}/" 2>/dev/null || true)
if echo "${_ROOT_BODY}" | grep -qF "Web bundle not built"; then
    log "ERROR: stub-body check FAILED — running server is serving 'Web bundle not built'"
    log "       Binary at ${SERVERD_BIN} was deployed without -tags webdist."
    log "       The pre-deploy guard should have caught this; check for manual binary replacement."
    log "       Next autodeploy cycle will rebuild from source with the correct flags."
    echo "clustr-autodeploy: stub-body check failed — server serving stub page; next cycle will rebuild" \
        | systemd-cat -t clustr-autodeploy -p err 2>/dev/null || true
    exit 1
fi
log "stub-body check: OK — server is not serving stub page"

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
log "Auto-deploy complete: ${LOCAL_SHA} → ${REMOTE_SHA}"
