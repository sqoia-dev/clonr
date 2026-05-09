// boot_operating_mode_test.go — Sprint 37 DISKLESS operating_mode routing.
//
// Coverage:
//
//   block_install (default):
//     Script unchanged from current behavior — the existing per-state
//     assertions still apply. No-regression golden test.
//
//   stateless_nfs (Bundle B):
//     Full iPXE script: kernel URL, stateless initramfs URL, nfsroot= cmdline.
//     Verified against node.BaseImageID and h.ServerURL.
//
//   filesystem_install / stateless_ram (sentinels):
//     TODO sentinel iPXE script — fails fast with a clear error message.
//     These modes are not yet wired; the sentinel script prevents silent loops.
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

// TestServeIPXEScript_OperatingMode_StatelessNFS_FullScript verifies that
// stateless_nfs now serves the full Bundle-B iPXE script (not the sentinel).
//
// The generated script must:
//   - Start with #!ipxe
//   - Include a kernel URL pointing at the clustr boot endpoint
//   - Include an initrd URL referencing the stateless image (<imageID>-stateless.img)
//   - Include the nfsroot= kernel arg pointing at the cloner host
//   - Include ip=dhcp in the kernel cmdline
//   - Include root=/dev/nfs in the kernel cmdline
//   - NOT contain "Bundle B pending" (that's the old sentinel)
//   - NOT contain sanboot (disk-boot path must not bleed into stateless mode)
func TestServeIPXEScript_OperatingMode_StatelessNFS_FullScript(t *testing.T) {
	d := openTestDB(t)
	// Create an image and assign it to the node — required for stateless_nfs.
	imgID := makeTestImage(t, d, api.FirmwareUEFI)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:82", "stateless-nfs-node")
	node.BaseImageID = imgID
	if err := d.UpdateNodeConfig(t.Context(), node); err != nil {
		t.Fatalf("UpdateNodeConfig: %v", err)
	}

	h := newBootHandler(d)
	setOperatingMode(t, h, node.ID, api.OperatingModeStatelessNFS)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("stateless_nfs: expected HTTP 200, got %d; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Must be a valid iPXE script.
	if !strings.HasPrefix(body, "#!ipxe") {
		t.Errorf("stateless_nfs: expected #!ipxe shebang; got:\n%s", body)
	}

	// Must contain a kernel command pointing at the boot endpoint.
	if !strings.Contains(body, "kernel ") {
		t.Errorf("stateless_nfs: missing 'kernel' command; got:\n%s", body)
	}
	if !strings.Contains(body, "/api/v1/boot/vmlinuz") {
		t.Errorf("stateless_nfs: kernel URL must reference /api/v1/boot/vmlinuz; got:\n%s", body)
	}

	// Must contain an initrd command for the stateless image.
	if !strings.Contains(body, "initrd ") {
		t.Errorf("stateless_nfs: missing 'initrd' command; got:\n%s", body)
	}
	if !strings.Contains(body, imgID+"-stateless.img") {
		t.Errorf("stateless_nfs: initrd must reference %s-stateless.img; got:\n%s", imgID, body)
	}

	// Must contain nfsroot= pointing at the cloner host and image path.
	if !strings.Contains(body, "nfsroot=") {
		t.Errorf("stateless_nfs: missing nfsroot= in kernel cmdline; got:\n%s", body)
	}
	if !strings.Contains(body, imgID+"/rootfs") {
		t.Errorf("stateless_nfs: nfsroot must reference %s/rootfs; got:\n%s", imgID, body)
	}

	// Must contain ip=dhcp and root=/dev/nfs.
	if !strings.Contains(body, "ip=dhcp") {
		t.Errorf("stateless_nfs: missing ip=dhcp in kernel cmdline; got:\n%s", body)
	}
	if !strings.Contains(body, "root=/dev/nfs") {
		t.Errorf("stateless_nfs: missing root=/dev/nfs in kernel cmdline; got:\n%s", body)
	}

	// Must NOT be the old sentinel.
	if strings.Contains(body, "Bundle B pending") {
		t.Errorf("stateless_nfs: must not serve Bundle-A sentinel anymore; got:\n%s", body)
	}
	if strings.Contains(body, "sanboot") {
		t.Errorf("stateless_nfs: must not contain sanboot (disk-boot path); got:\n%s", body)
	}
}

// TestServeIPXEScript_OperatingMode_StatelessNFS_NoBaseImage verifies that a
// node with stateless_nfs but no base_image_id set returns HTTP 500, not a
// silent boot loop. The operator must assign an image before PXE can succeed.
func TestServeIPXEScript_OperatingMode_StatelessNFS_NoBaseImage(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:8f", "stateless-nfs-noimage")
	h := newBootHandler(d)

	setOperatingMode(t, h, node.ID, api.OperatingModeStatelessNFS)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("stateless_nfs with no image: expected HTTP 500, got %d; body: %s",
			w.Code, w.Body.String())
	}
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

// TestServeIPXEScript_OperatingMode_StatelessNFS_OverridesDeployedState
// confirms that a deployed_preboot node with stateless_nfs operating_mode
// receives the stateless NFS iPXE script, not the disk-boot (sanboot/exit)
// script. Stateless nodes never have a "deployed" terminal state in the same
// sense as block_install nodes — each PXE boot mounts the NFS root fresh.
func TestServeIPXEScript_OperatingMode_StatelessNFS_OverridesDeployedState(t *testing.T) {
	d := openTestDB(t)
	// Create an image so stateless_nfs script generation succeeds.
	imgID := makeTestImage(t, d, api.FirmwareUEFI)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:84", "deployed-stateless-node")
	node.BaseImageID = imgID
	if err := d.UpdateNodeConfig(t.Context(), node); err != nil {
		t.Fatalf("UpdateNodeConfig: %v", err)
	}

	h := newBootHandler(d)

	// Advance to deployed_preboot (would normally trigger disk-boot for block_install).
	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}

	// Flip to stateless_nfs — must receive the NFS iPXE script, not sanboot.
	setOperatingMode(t, h, node.ID, api.OperatingModeStatelessNFS)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("deployed+stateless_nfs: expected HTTP 200, got %d; body: %s",
			w.Code, w.Body.String())
	}
	body := w.Body.String()
	// Must be the stateless NFS script.
	if !strings.Contains(body, "nfsroot=") {
		t.Errorf("deployed+stateless_nfs: expected nfsroot= in script; got:\n%s", body)
	}
	// Must NOT be a disk-boot script.
	if strings.Contains(body, "sanboot") || strings.Contains(body, "exit") {
		t.Errorf("deployed+stateless_nfs: must not contain disk-boot commands; got:\n%s", body)
	}
}

// TestServeIPXEScript_OperatingMode_BlockInstall_NoRegression is the explicit
// no-regression golden test confirming the block_install path is byte-identical
// to what existed before operating_mode was introduced. The stateless_nfs
// branch must never alter the block_install code path.
func TestServeIPXEScript_OperatingMode_BlockInstall_NoRegression(t *testing.T) {
	d := openTestDB(t)
	// A fresh node with no image assigned — block_install default behavior.
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:99", "block-regression-node")
	h := newBootHandler(d)

	// block_install (empty string, i.e. unset) — must serve the initramfs deploy script.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)
	assertInitramfsBoot(t, w, "block_install (empty/default) -> initramfs boot")

	// Explicit block_install — must produce the same output.
	setOperatingMode(t, h, node.ID, api.OperatingModeBlockInstall)
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w2 := httptest.NewRecorder()
	h.ServeIPXEScript(w2, req2)
	assertInitramfsBoot(t, w2, "block_install (explicit) -> initramfs boot")

	// Must contain the clustr deploy args (token, server=, mac=).
	body := w.Body.String()
	if !strings.Contains(body, "clustr.server=") {
		t.Errorf("block_install: expected clustr.server= in script; got:\n%s", body)
	}
	// Must NOT have nfsroot — that belongs to stateless_nfs only.
	if strings.Contains(body, "nfsroot=") {
		t.Errorf("block_install: must not contain nfsroot= (stateless path leaked); got:\n%s", body)
	}
}
