package handlers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/pkg/bootassets"
	"github.com/sqoia-dev/clonr/pkg/db"
)

// TestServeIPXEEFI_EmbeddedBinary verifies that GET /api/v1/boot/ipxe.efi
// returns 200, Content-Type: application/efi, and the exact embedded binary
// without requiring any on-disk file in TFTPDir.
func TestServeIPXEEFI_EmbeddedBinary(t *testing.T) {
	h := &BootHandler{
		TFTPDir:   "/nonexistent/tftp", // must NOT be read — binary is embedded
		ServerURL: "http://192.168.1.151:8080",
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe.efi", nil)
	w := httptest.NewRecorder()

	h.ServeIPXEEFI(w, req)

	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ServeIPXEEFI: got status %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/efi" {
		t.Errorf("ServeIPXEEFI: Content-Type = %q, want %q", ct, "application/efi")
	}

	if resp.ContentLength != int64(len(bootassets.IPXEEFI)) {
		t.Errorf("ServeIPXEEFI: Content-Length = %d, want %d", resp.ContentLength, len(bootassets.IPXEEFI))
	}

	body := w.Body.Bytes()
	if len(body) != len(bootassets.IPXEEFI) {
		t.Errorf("ServeIPXEEFI: body length = %d, want %d", len(body), len(bootassets.IPXEEFI))
	}
	for i := range body {
		if body[i] != bootassets.IPXEEFI[i] {
			t.Errorf("ServeIPXEEFI: body mismatch at byte %d (got 0x%02x, want 0x%02x)", i, body[i], bootassets.IPXEEFI[i])
			break
		}
	}
}

// openTestDB opens a fresh SQLite database in a temp directory.
// The database is closed automatically when the test finishes.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// makeTestNode creates a minimal NodeConfig with the given MAC and hostname,
// inserts it into d, and returns the created config.
func makeTestNode(t *testing.T, d *db.DB, mac, hostname string) api.NodeConfig {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	cfg := api.NodeConfig{
		ID:         "node-" + mac,
		Hostname:   hostname,
		PrimaryMAC: mac,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(t.Context(), cfg); err != nil {
		t.Fatalf("makeTestNode CreateNodeConfig: %v", err)
	}
	return cfg
}

// newBootHandler returns a BootHandler wired to the given DB with a fixed ServerURL.
func newBootHandler(d *db.DB) *BootHandler {
	return &BootHandler{
		ServerURL: "http://10.99.0.1:8080",
		DB:        d,
	}
}

// assertDiskBoot checks that the response body contains the sanboot command
// (disk boot iPXE script) and does NOT contain the kernel/initrd boot commands.
func assertDiskBoot(t *testing.T, w *httptest.ResponseRecorder, label string) {
	t.Helper()
	body := w.Body.String()
	if !strings.Contains(body, "sanboot") {
		t.Errorf("%s: expected disk-boot (sanboot) script; got:\n%s", label, body)
	}
	if strings.Contains(body, "kernel") && strings.Contains(body, "initrd") {
		t.Errorf("%s: response contains initramfs boot commands; expected disk-boot only; got:\n%s", label, body)
	}
	if w.Code != http.StatusOK {
		t.Errorf("%s: expected HTTP 200, got %d", label, w.Code)
	}
}

// assertInitramfsBoot checks that the response body contains initramfs boot
// commands and does NOT contain sanboot (which would indicate a disk-boot script).
func assertInitramfsBoot(t *testing.T, w *httptest.ResponseRecorder, label string) {
	t.Helper()
	body := w.Body.String()
	if !strings.Contains(body, "kernel") || !strings.Contains(body, "initrd") {
		t.Errorf("%s: expected initramfs boot script (kernel+initrd); got:\n%s", label, body)
	}
	if strings.Contains(body, "sanboot") {
		t.Errorf("%s: unexpected sanboot in initramfs boot script; got:\n%s", label, body)
	}
	if w.Code != http.StatusOK {
		t.Errorf("%s: expected HTTP 200, got %d", label, w.Code)
	}
}

// TestServeIPXEScript_DeployedPreboot_DiskBoots is the primary regression test for
// ADR-0008: a node in deployed_preboot state (deploy-complete received from initramfs,
// OS not yet verified) MUST receive a disk-boot (sanboot) script so that
// clonr-verify-boot.service can run and phone home. Falling through to re-deploy
// would cause an infinite loop.
func TestServeIPXEScript_DeployedPreboot_DiskBoots(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:01", "node01")

	// Advance node to deployed_preboot by recording a successful deploy.
	// RecordDeploySucceeded sets deploy_completed_preboot_at = now, which
	// causes State() to return NodeStateDeployedPreboot.
	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}

	// Confirm state is as expected before testing the handler.
	got, err := d.GetNodeConfigByMAC(t.Context(), node.PrimaryMAC)
	if err != nil {
		t.Fatalf("GetNodeConfigByMAC: %v", err)
	}
	if got.State() != api.NodeStateDeployedPreboot {
		t.Fatalf("precondition: expected state deployed_preboot, got %s", got.State())
	}

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertDiskBoot(t, w, "deployed_preboot -> disk-boot")
}

// TestServeIPXEScript_DeployedPreboot_AfterTimeout_StillDiskBoots tests that a
// deployed_preboot node whose verify deadline has passed still receives a disk-boot
// script (not a re-deploy). The operator must manually intervene if the OS is broken;
// automatic re-deploy in this state is unsafe per ADR-0008.
func TestServeIPXEScript_DeployedPreboot_AfterTimeout_StillDiskBoots(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:02", "node02")

	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}
	// RecordVerifyTimeout stamps deploy_verify_timeout_at; State() remains
	// deployed_preboot (not deploy_verify_timeout) because DeployVerifyTimeoutAt
	// is set but DeployCompletedPrebootAt is also set — State() returns
	// NodeStateDeployVerifyTimeout in that case.
	if err := d.RecordVerifyTimeout(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordVerifyTimeout: %v", err)
	}

	got, err := d.GetNodeConfigByMAC(t.Context(), node.PrimaryMAC)
	if err != nil {
		t.Fatalf("GetNodeConfigByMAC: %v", err)
	}
	// After RecordVerifyTimeout, State() returns NodeStateDeployVerifyTimeout.
	// Both NodeStateDeployedPreboot and NodeStateDeployVerifyTimeout must disk-boot.
	state := got.State()
	if state != api.NodeStateDeployVerifyTimeout && state != api.NodeStateDeployedPreboot {
		t.Fatalf("precondition: expected deployed_preboot or deploy_verify_timeout, got %s", state)
	}

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertDiskBoot(t, w, "deployed_preboot+timeout -> disk-boot")
}

// TestServeIPXEScript_DeployVerifyTimeout_DiskBoots explicitly exercises the
// NodeStateDeployVerifyTimeout routing case: OS never phoned home in time,
// background scanner stamped deploy_verify_timeout_at. Must still disk-boot.
func TestServeIPXEScript_DeployVerifyTimeout_DiskBoots(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:03", "node03")

	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}
	if err := d.RecordVerifyTimeout(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordVerifyTimeout: %v", err)
	}

	got, err := d.GetNodeConfigByMAC(t.Context(), node.PrimaryMAC)
	if err != nil {
		t.Fatalf("GetNodeConfigByMAC: %v", err)
	}
	if got.State() != api.NodeStateDeployVerifyTimeout {
		t.Fatalf("precondition: expected deploy_verify_timeout, got %s", got.State())
	}

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertDiskBoot(t, w, "deploy_verify_timeout -> disk-boot")
}

// TestServeIPXEScript_DeployedVerified_DiskBoots verifies that the happy-path
// fully-verified state also receives a disk-boot script (regression guard).
func TestServeIPXEScript_DeployedVerified_DiskBoots(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:04", "node04")

	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}
	if err := d.RecordVerifyBooted(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordVerifyBooted: %v", err)
	}

	got, err := d.GetNodeConfigByMAC(t.Context(), node.PrimaryMAC)
	if err != nil {
		t.Fatalf("GetNodeConfigByMAC: %v", err)
	}
	if got.State() != api.NodeStateDeployedVerified {
		t.Fatalf("precondition: expected deployed_verified, got %s", got.State())
	}

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertDiskBoot(t, w, "deployed_verified -> disk-boot")
}

// TestServeIPXEScript_Registered_InitramfsBoot verifies that a freshly-registered
// node (no image assigned) receives the initramfs boot script for deployment.
func TestServeIPXEScript_Registered_InitramfsBoot(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:05", "node05")

	got, err := d.GetNodeConfigByMAC(t.Context(), node.PrimaryMAC)
	if err != nil {
		t.Fatalf("GetNodeConfigByMAC: %v", err)
	}
	if got.State() != api.NodeStateRegistered {
		t.Fatalf("precondition: expected registered, got %s", got.State())
	}

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertInitramfsBoot(t, w, "registered -> initramfs boot")
}

// TestServeIPXEScript_ReimagePending_InitramfsBoot verifies that a node with
// reimage_pending=true receives the initramfs boot script even if it was previously
// deployed, triggering a fresh deployment.
func TestServeIPXEScript_ReimagePending_InitramfsBoot(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:06", "node06")

	// Deploy and verify the node first.
	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}
	if err := d.RecordVerifyBooted(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordVerifyBooted: %v", err)
	}
	// Now flip reimage_pending.
	if err := d.SetReimagePending(t.Context(), node.ID, true); err != nil {
		t.Fatalf("SetReimagePending: %v", err)
	}

	got, err := d.GetNodeConfigByMAC(t.Context(), node.PrimaryMAC)
	if err != nil {
		t.Fatalf("GetNodeConfigByMAC: %v", err)
	}
	if got.State() != api.NodeStateReimagePending {
		t.Fatalf("precondition: expected reimage_pending, got %s", got.State())
	}

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertInitramfsBoot(t, w, "reimage_pending -> initramfs boot")
}

// TestServeIPXEScript_UnknownMAC_InitramfsBoot verifies that an unrecognised MAC
// receives the default initramfs boot script so the node can self-register.
func TestServeIPXEScript_UnknownMAC_InitramfsBoot(t *testing.T) {
	d := openTestDB(t)

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac=ff:ff:ff:ff:ff:ff", nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertInitramfsBoot(t, w, "unknown MAC -> initramfs boot")
}
