#!/usr/bin/env bash
# test-deploy.sh — run pkg/deploy integration tests with loopback devices.
#
# Usage:
#   sudo ./scripts/test-deploy.sh [extra go test flags]
#   ./scripts/test-deploy.sh             # will re-exec with sudo if not root
#
# What it does:
#   1. Checks that required binaries are present (losetup, parted, mkfs.ext4,
#      mount, tar).
#   2. Re-execs with sudo if the caller is not root.
#   3. Runs:
#        GOTOOLCHAIN=auto go test -tags=deploy_integration -v -count=1 \
#          -timeout=300s ./pkg/deploy/...
#   4. On EXIT (success or failure), runs `losetup -D` to detach any loopbacks
#      that leaked from a previous crashed run. This is conservative — it
#      removes ALL loopbacks, not just ones created by this test run. Do not
#      run this script concurrently with other loopback-using processes.
#
# CI configuration:
#   Add this step to the privileged GitHub Actions job:
#
#     - name: Run deploy integration tests
#       run: sudo ./scripts/test-deploy.sh
#
#   The job container must have:
#     privileged: true    # or equivalent CAP_SYS_ADMIN grant
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# ── Cleanup on exit (catches Ctrl-C, SIGTERM, and normal exit) ────────────────
cleanup_loopbacks() {
    local exit_code=$?
    echo ""
    echo "[test-deploy] cleaning up leaked loopbacks (losetup -D)..."
    losetup -D 2>/dev/null || true
    exit "$exit_code"
}
trap cleanup_loopbacks EXIT

# ── Privilege check ───────────────────────────────────────────────────────────
if [[ "$EUID" -ne 0 ]]; then
    echo "[test-deploy] not root — re-execing with sudo..."
    exec sudo bash "$0" "$@"
fi

# ── Required binary checks ────────────────────────────────────────────────────
missing=()
for bin in losetup parted mkfs.ext4 mount umount tar; do
    if ! command -v "$bin" &>/dev/null; then
        missing+=("$bin")
    fi
done
if [[ ${#missing[@]} -gt 0 ]]; then
    echo "[test-deploy] ERROR: required binaries not found: ${missing[*]}"
    echo "             Install them (e.g. apt-get install util-linux e2fsprogs mount tar)"
    exit 1
fi

echo "[test-deploy] all required binaries present"
echo "[test-deploy] running: GOTOOLCHAIN=auto go test -tags=deploy_integration -v -count=1 -timeout=300s ./pkg/deploy/... $*"
echo ""

GOTOOLCHAIN=auto go test \
    -tags=deploy_integration \
    -v \
    -count=1 \
    -timeout=300s \
    ./pkg/deploy/... \
    "$@"
