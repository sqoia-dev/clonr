// boot_operating_mode_test.go — Sprint 37 DISKLESS Bundle A: ServeIPXEScript
// branches on NodeConfig.OperatingMode.
//
// Coverage:
//   * block_install (default) — script unchanged from current behavior; the
//     existing per-state assertions still apply.
//   * filesystem_install / stateless_nfs / stateless_ram — TODO sentinel
//     iPXE script that fails fast with a recognisable error message.
//
// The TODO-sentinel branch is intentionally observable in lab without
// shipping a half-broken boot path: the script returns 200 (so iPXE renders
// it), prints the mode + node identity, and exits 1 so the node bootloader
// drops to a recognisable failure rather than looping in clustr-deploy.
package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// setOperatingMode helper writes a node's operating_mode via UpdateNodeConfig.
// This bypasses the API-layer PATCH so the test exercises only ServeIPXEScript.
func setOperatingMode(t *testing.T, h *BootHandler, nodeID, mode string) {
	t.Helper()
	cfg, err := h.DB.GetNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatalf("setOperatingMode GetNodeConfig: %v", err)
	}
	cfg.OperatingMode = mode
	if err := h.DB.UpdateNodeConfig(t.Context(), cfg); err != nil {
		t.Fatalf("setOperatingMode UpdateNodeConfig: %v", err)
	}
}

// assertTODOSentinelScript checks that the response is the Bundle-A TODO
// sentinel iPXE script and that it carries the expected diagnostic strings.
func assertTODOSentinelScript(t *testing.T, w *httptest.ResponseRecorder, mode, nodeID, label string) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Errorf("%s: expected HTTP 200, got %d", label, w.Code)
	}
	body := w.Body.String()
	// Must be an iPXE script.
	if !strings.HasPrefix(body, "#!ipxe") {
		t.Errorf("%s: expected iPXE shebang, got body:\n%s", label, body)
	}
	// Must NOT contain the deploy initramfs commands (kernel/initrd) — that
	// would mean the sentinel branch leaked a half-wired deploy path.
	if strings.Contains(body, "kernel ") || strings.Contains(body, "initrd ") {
		t.Errorf("%s: TODO sentinel must not contain initramfs boot commands; body:\n%s", label, body)
	}
	// Must NOT contain sanboot — that would mean it leaked into the
	// disk-boot path.
	if strings.Contains(body, "sanboot") {
		t.Errorf("%s: TODO sentinel must not contain sanboot; body:\n%s", label, body)
	}
	// Must mention the operating_mode in the script body so an operator
	// looking at the iPXE console knows exactly which mode is unwired.
	if !strings.Contains(body, mode) {
		t.Errorf("%s: TODO sentinel must mention mode %q; body:\n%s", label, mode, body)
	}
	// Must mention the node ID for triage.
	if !strings.Contains(body, nodeID) {
		t.Errorf("%s: TODO sentinel must mention node id %q; body:\n%s", label, nodeID, body)
	}
	// Must mention "Bundle B" so the message is self-documenting about why
	// it failed and what unblocks it.
	if !strings.Contains(body, "Bundle B") {
		t.Errorf("%s: TODO sentinel must mention Bundle B; body:\n%s", label, body)
	}
	// Must exit so the node doesn't get stuck waiting.
	if !strings.Contains(body, "exit") {
		t.Errorf("%s: TODO sentinel must contain exit; body:\n%s", label, body)
	}
}

// TestServeIPXEScript_OperatingMode_BlockInstall_Unchanged confirms that the
// default operating_mode (block_install) preserves the existing initramfs-boot
// behavior bit-for-bit. This is the no-regression guarantee for every node
// that exists at upgrade time.
func TestServeIPXEScript_OperatingMode_BlockInstall_Unchanged(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:80", "block-install-node")

	h := newBootHandler(d)

	// New node, no image assigned — current behavior is to serve the
	// initramfs deploy script.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	// block_install is the default — the unchanged path serves the initramfs
	// boot script for an unconfigured node.
	assertInitramfsBoot(t, w, "block_install (default) -> initramfs boot")

	// Explicitly setting block_install must produce identical output.
	setOperatingMode(t, h, node.ID, api.OperatingModeBlockInstall)
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w2 := httptest.NewRecorder()
	h.ServeIPXEScript(w2, req2)
	assertInitramfsBoot(t, w2, "block_install (explicit) -> initramfs boot")
}

// TestServeIPXEScript_OperatingMode_FilesystemInstall_Sentinel confirms the
// filesystem_install mode serves the Bundle-A TODO sentinel iPXE script.
func TestServeIPXEScript_OperatingMode_FilesystemInstall_Sentinel(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:81", "fs-install-node")
	h := newBootHandler(d)

	setOperatingMode(t, h, node.ID, api.OperatingModeFilesystemInstall)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertTODOSentinelScript(t, w, api.OperatingModeFilesystemInstall, node.ID, "filesystem_install")
}

// TestServeIPXEScript_OperatingMode_StatelessNFS_Sentinel confirms the
// stateless_nfs mode serves the Bundle-A TODO sentinel.
func TestServeIPXEScript_OperatingMode_StatelessNFS_Sentinel(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:82", "stateless-nfs-node")
	h := newBootHandler(d)

	setOperatingMode(t, h, node.ID, api.OperatingModeStatelessNFS)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertTODOSentinelScript(t, w, api.OperatingModeStatelessNFS, node.ID, "stateless_nfs")
}

// TestServeIPXEScript_OperatingMode_StatelessRAM_Sentinel confirms the
// stateless_ram mode serves the Bundle-A TODO sentinel.
func TestServeIPXEScript_OperatingMode_StatelessRAM_Sentinel(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:83", "stateless-ram-node")
	h := newBootHandler(d)

	setOperatingMode(t, h, node.ID, api.OperatingModeStatelessRAM)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertTODOSentinelScript(t, w, api.OperatingModeStatelessRAM, node.ID, "stateless_ram")
}

// TestServeIPXEScript_OperatingMode_SentinelOverridesDeployedState confirms
// that the operating_mode branch fires regardless of node state — even a
// deployed_verified node with a non-default mode gets the sentinel rather
// than a disk-boot script. Bundle B will refine this (the stateless modes
// don't have a "deployed" terminal state in the same sense), but for
// Bundle A the contract is "non-default mode == sentinel, full stop".
func TestServeIPXEScript_OperatingMode_SentinelOverridesDeployedState(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:84", "deployed-stateless-node")
	h := newBootHandler(d)

	// Advance to deployed_preboot (would normally disk-boot).
	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}

	// Then flip operating_mode to a non-default value.
	setOperatingMode(t, h, node.ID, api.OperatingModeStatelessNFS)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	// Sentinel wins — the partial Bundle-B path can't smear a disk-boot script
	// into a stateless-config'd node before the rootfs export exists.
	assertTODOSentinelScript(t, w, api.OperatingModeStatelessNFS, node.ID, "deployed+stateless_nfs")
}
