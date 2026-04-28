# clustr demo tape
#
# Generates docs/assets/clustr-demo.gif
#
# Requirements:
#   brew install vhs    (macOS)
#   go install github.com/charmbracelet/vhs@latest  (Go)
#
# Run (from repo root):
#   vhs docs/assets/demo.tape
#
# What this shows: server version → pre-flight doctor → health check →
# registered nodes → base image. Runs against a live clustr-serverd instance.
#
# Prerequisites:
#   A running clustr-serverd (see docs/install.md).
#   Set CLUSTR_URL and CLUSTR_API_KEY before running:
#     export CLUSTR_URL=http://<your-server-ip>:8080
#     export CLUSTR_API_KEY=<your-admin-api-key>

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
Sleep 3s

# ── Verify server is healthy ─────────────────────────────────────────────
Type `curl -s $CLUSTR_URL/api/v1/healthz/ready | python3 -m json.tool`
Enter
Sleep 2s

# ── List registered nodes ────────────────────────────────────────────────
Type `curl -s -H "Authorization: Bearer $CLUSTR_API_KEY" \`
Enter
Type `  $CLUSTR_URL/api/v1/nodes | python3 -m json.tool | grep -E '"hostname"|"status"|"total"'`
Enter
Sleep 2.5s

# ── Show base image ready for deployment ─────────────────────────────────
Type `curl -s -H "Authorization: Bearer $CLUSTR_API_KEY" \`
Enter
Type `  $CLUSTR_URL/api/v1/images | python3 -m json.tool | grep -E '"name"|"status"|"os"|"version"'`
Enter
Sleep 3s
