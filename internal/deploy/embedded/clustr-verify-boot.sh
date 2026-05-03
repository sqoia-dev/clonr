#!/bin/sh
# clustr-verify-boot: phone-home script injected by clustr into the deployed OS.
# Reads /etc/clustr/node-token and /etc/clustr/verify-boot-url, collects basic
# system info, and POSTs it to the clustr-serverd verify-boot endpoint.
# Sprint 15 #99: also probes sssd status and pam_sss.so presence.
# ADR-0008.
set -eu

TOKEN_FILE="/etc/clustr/node-token"
URL_FILE="/etc/clustr/verify-boot-url"

if [ ! -f "$TOKEN_FILE" ]; then
    echo "clustr-verify-boot: $TOKEN_FILE not found — skipping phone-home" >&2
    exit 1
fi
if [ ! -f "$URL_FILE" ]; then
    echo "clustr-verify-boot: $URL_FILE not found — skipping phone-home" >&2
    exit 1
fi

TOKEN=$(cat "$TOKEN_FILE")
URL=$(cat "$URL_FILE")

HOSTNAME=$(hostname)
KERNEL=$(uname -r)
UPTIME_SEC=$(awk '{print int($1)}' /proc/uptime)
SYSTEMCTL_STATE=$(systemctl is-system-running 2>/dev/null || true)
OS_RELEASE=""
if [ -f /etc/os-release ]; then
    OS_RELEASE=$(cat /etc/os-release)
fi

# Escape special chars in OS_RELEASE for JSON embedding.
# Order matters: backslash first, then double-quote, then newlines.
# Single awk pass handles all three without the tr+sed backslash round-trip
# that previously destroyed the quote escaping.
OS_RELEASE_ESCAPED=$(printf '%s' "$OS_RELEASE" \
    | awk '{gsub(/\\/, "\\\\"); gsub(/"/, "\\\""); printf "%s\\n", $0}')

# ── Sprint 15 #99: SSSD status probe ─────────────────────────────────────────
# Probe sssd to determine whether LDAP authentication is ready.
# sssctl domain-status reports the online/offline state per-domain.
# We capture the first non-empty line and send it to the server.
SSSD_STATUS=""
PAM_SSS_PRESENT="false"

if command -v sssctl >/dev/null 2>&1; then
    # Extract the first domain from sssd.conf to probe it.
    SSSD_DOMAIN=""
    if [ -f /etc/sssd/sssd.conf ]; then
        SSSD_DOMAIN=$(awk -F= '/^domains[[:space:]]*=/{gsub(/[[:space:]]/, "", $2); print $2; exit}' /etc/sssd/sssd.conf | cut -d, -f1)
    fi
    if [ -n "$SSSD_DOMAIN" ]; then
        SSSD_STATUS=$(sssctl domain-status "$SSSD_DOMAIN" 2>/dev/null | head -1 | tr -d '\n' || echo "probe_failed")
    else
        SSSD_STATUS=$(sssctl domain-status 2>/dev/null | head -1 | tr -d '\n' || echo "probe_failed")
    fi
    # Trim whitespace.
    SSSD_STATUS=$(printf '%s' "$SSSD_STATUS" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
    [ -z "$SSSD_STATUS" ] && SSSD_STATUS="empty_output"
else
    # sssd not installed in this image.
    SSSD_STATUS="not_installed"
fi

# Check whether pam_sss.so is wired into the PAM stack.
if grep -q 'pam_sss\.so' /etc/pam.d/system-auth 2>/dev/null; then
    PAM_SSS_PRESENT="true"
fi

# Escape SSSD_STATUS for safe JSON embedding (strip double-quotes, newlines).
SSSD_STATUS_ESCAPED=$(printf '%s' "$SSSD_STATUS" | tr -d '"' | tr '\n' ' ')

PAYLOAD=$(printf '{"hostname":"%s","kernel_version":"%s","uptime_seconds":%s,"systemctl_state":"%s","os_release":"%s","sssd_status":"%s","pam_sss_present":%s}' \
    "$HOSTNAME" "$KERNEL" "$UPTIME_SEC" "$SYSTEMCTL_STATE" "$OS_RELEASE_ESCAPED" "$SSSD_STATUS_ESCAPED" "$PAM_SSS_PRESENT")

HTTP_CODE=$(curl --silent --output /dev/null --write-out "%{http_code}" \
    --max-time 30 \
    --retry 0 \
    -X POST "$URL" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD")

if [ "$HTTP_CODE" = "204" ]; then
    echo "clustr-verify-boot: phone-home accepted (204) — boot verified" >&2
    exit 0
else
    echo "clustr-verify-boot: unexpected HTTP status $HTTP_CODE from $URL" >&2
    exit 1
fi
