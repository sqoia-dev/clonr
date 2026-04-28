# clustr demo tape
#
# Generates docs/assets/clustr-demo.gif
#
# Requirements:
#   brew install vhs    (macOS)
#   go install github.com/charmbracelet/vhs@latest  (Go)
#
# Run:
#   vhs docs/assets/demo.tape
#
# This shows the core trust chain: server start → bootstrap key → node
# registration → API verification. No PXE boot required — runs against
# a local Docker container.
#
# Prerequisites for recording:
#   docker run -d --name clustr-demo \
#     -e CLUSTR_SECRET_KEY=$(openssl rand -hex 32) \
#     -e CLUSTR_SESSION_SECRET=$(openssl rand -hex 64) \
#     -e CLUSTR_AUTH_DEV_MODE=0 \
#     -p 8080:8080 \
#     ghcr.io/sqoia-dev/clustr-server:latest
#   export CLUSTR_URL=http://localhost:8080
#   export CLUSTR_API_KEY=<key from docker logs>

Output docs/assets/clustr-demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1200
Set Height 600
Set Theme "Dracula"
Set Framerate 15
Set PlaybackSpeed 1.0
Set TypingSpeed 40ms

# ── Server version ────────────────────────────────────────────────────────
Type "clustr-serverd version"
Enter
Sleep 1.5s

# ── Pre-flight check ─────────────────────────────────────────────────────
Type "clustr-serverd doctor"
Enter
Sleep 2s

# ── Verify server is healthy ─────────────────────────────────────────────
Type `curl -s http://localhost:8080/api/v1/healthz/ready | python3 -m json.tool`
Enter
Sleep 2s

# ── List nodes (empty cluster) ───────────────────────────────────────────
Type `curl -s -H "Authorization: Bearer $CLUSTR_API_KEY" \`
Enter
Type `  http://localhost:8080/api/v1/nodes | python3 -m json.tool`
Enter
Sleep 2s

# ── Simulate PXE node self-registration ──────────────────────────────────
Type `curl -s -X POST \`
Enter
Type `  -H "Content-Type: application/json" \`
Enter
Type `  -d '{"mac":"52:54:00:aa:bb:01","hostname":"node001","arch":"x86_64"}' \`
Enter
Type `  http://localhost:8080/api/v1/nodes/register | python3 -m json.tool`
Enter
Sleep 2.5s

# ── Node appears in the list ─────────────────────────────────────────────
Type `curl -s -H "Authorization: Bearer $CLUSTR_API_KEY" \`
Enter
Type `  http://localhost:8080/api/v1/nodes | python3 -m json.tool`
Enter
Sleep 3s
