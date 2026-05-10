package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/bootassets"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
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

// assertDiskBoot checks that the response body is a disk boot iPXE script
// (either sanboot for BIOS or exit for UEFI) and does NOT contain initramfs
// boot commands (kernel/initrd). This is the firmware-agnostic check used by
// tests that don't care which disk-boot variant is returned.
func assertDiskBoot(t *testing.T, w *httptest.ResponseRecorder, label string) {
	t.Helper()
	body := w.Body.String()
	// A disk-boot script must contain either sanboot (BIOS) or exit (UEFI).
	hasSanboot := strings.Contains(body, "sanboot")
	hasExit := strings.Contains(body, "exit")
	if !hasSanboot && !hasExit {
		t.Errorf("%s: expected disk-boot script (sanboot or exit); got:\n%s", label, body)
	}
	if strings.Contains(body, "kernel") && strings.Contains(body, "initrd") {
		t.Errorf("%s: response contains initramfs boot commands; expected disk-boot only; got:\n%s", label, body)
	}
	if w.Code != http.StatusOK {
		t.Errorf("%s: expected HTTP 200, got %d", label, w.Code)
	}
}

// assertBIOSDiskBoot checks that the response is a BIOS sanboot script.
func assertBIOSDiskBoot(t *testing.T, w *httptest.ResponseRecorder, label string) {
	t.Helper()
	body := w.Body.String()
	if !strings.Contains(body, "sanboot --no-describe --drive 0x80") {
		t.Errorf("%s: expected BIOS sanboot script; got:\n%s", label, body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "exit" {
			t.Errorf("%s: BIOS disk boot script must not contain bare 'exit' (SeaBIOS loop); got:\n%s", label, body)
			break
		}
	}
	if w.Code != http.StatusOK {
		t.Errorf("%s: expected HTTP 200, got %d", label, w.Code)
	}
}

// assertUEFIDiskBoot checks that the response is a UEFI exit script.
// The script must contain plain `exit` (not `exit 1`). Boot routing for UEFI
// nodes is handled via NVRAM BootOrder (OS entry first, set by FixEFIBoot).
// `exit 1` was previously used but caused OVMF to try HTTP IPv6 boot instead
// of the OS disk because `exit 1` triggers OVMF's firmware-enumerated fallback
// rather than walking BootOrder.
func assertUEFIDiskBoot(t *testing.T, w *httptest.ResponseRecorder, label string) {
	t.Helper()
	body := w.Body.String()
	if !strings.Contains(body, "exit") {
		t.Errorf("%s: expected UEFI exit script with 'exit'; got:\n%s", label, body)
	}
	if strings.Contains(body, "exit 1") {
		t.Errorf("%s: UEFI disk boot script must NOT contain 'exit 1' (breaks OVMF BootOrder routing); got:\n%s", label, body)
	}
	if strings.Contains(body, "sanboot") {
		t.Errorf("%s: UEFI disk boot script must not contain sanboot; got:\n%s", label, body)
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
// clustr-verify-boot.service can run and phone home. Falling through to re-deploy
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

// TestServeIPXEScript_DeployedLDAPFailed_DiskBoots is the primary regression
// test for the PR #2 review fix: a node in deployed_ldap_failed state (booted
// + phoned home but sssd not connected) MUST disk-boot on a subsequent PXE
// cycle. Pre-fix the state fell through the disk-boot switch into the
// initramfs/reimage path, so any node that PXE-booted again (manual reboot,
// persistent netboot config, IPMI bootdev pxe used during operator triage)
// would be silently reimaged — losing a potentially trivially-fixable LDAP
// integration failure. The state means "OS bootable, LDAP broken"; only
// operators decide whether to repair LDAP or reimage.
func TestServeIPXEScript_DeployedLDAPFailed_DiskBoots(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:05", "node05")

	// Drive the node to deployed_ldap_failed:
	//   1. RecordDeploySucceeded -> deploy_completed_preboot_at = now.
	//   2. RecordVerifyBooted    -> deploy_verified_booted_at = now.
	//   3. RecordNodeLDAPReady(false, ...) -> ldap_ready = false.
	// State() returns NodeStateDeployedLDAPFailed when DeployVerifiedBootedAt
	// is set AND LDAPReady is non-nil and false.
	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}
	if _, err := d.RecordVerifyBooted(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordVerifyBooted: %v", err)
	}
	if err := d.RecordNodeLDAPReady(t.Context(), node.ID, false, "sssd not connected"); err != nil {
		t.Fatalf("RecordNodeLDAPReady: %v", err)
	}

	got, err := d.GetNodeConfigByMAC(t.Context(), node.PrimaryMAC)
	if err != nil {
		t.Fatalf("GetNodeConfigByMAC: %v", err)
	}
	if got.State() != api.NodeStateDeployedLDAPFailed {
		t.Fatalf("precondition: expected deployed_ldap_failed, got %s", got.State())
	}

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertDiskBoot(t, w, "deployed_ldap_failed -> disk-boot (NOT auto-reimage)")
}

// TestServeIPXEScript_DeployedVerified_DiskBoots verifies that the happy-path
// fully-verified state also receives a disk-boot script (regression guard).
func TestServeIPXEScript_DeployedVerified_DiskBoots(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:04", "node04")

	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}
	if _, err := d.RecordVerifyBooted(t.Context(), node.ID); err != nil {
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
// reimage_pending=true AND a base_image_id assigned receives the initramfs boot
// script, triggering a fresh deployment.
func TestServeIPXEScript_ReimagePending_InitramfsBoot(t *testing.T) {
	d := openTestDB(t)
	imgID := makeTestImage(t, d, api.FirmwareUEFI)
	// Use makeDeployedNodeWithImage to ensure base_image_id is set — a real reimage
	// flow always has an image assigned before the operator triggers reimage.
	node := makeDeployedNodeWithImage(t, d, "aa:bb:cc:dd:ee:06", "node06", imgID)

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

	assertInitramfsBoot(t, w, "reimage_pending (with image) -> initramfs boot")
}

// TestServeIPXEScript_ReimagePending_NoImage_WaitRetry verifies that a node in
// reimage_pending state with no base_image_id receives a wait/retry iPXE script
// instead of a deploy script. Without an image, the deploy agent would attempt
// to fetch an image and get 403 from requireImageAccess. The wait-retry script
// keeps the node looping in iPXE until an operator assigns an image.
func TestServeIPXEScript_ReimagePending_NoImage_WaitRetry(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:07", "node07")

	// Set reimage_pending WITHOUT assigning a base_image_id — the bug scenario.
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
	if got.BaseImageID != "" {
		t.Fatalf("precondition: expected empty BaseImageID, got %q", got.BaseImageID)
	}

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac="+node.PrimaryMAC, nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("reimage_pending (no image) -> expected HTTP 200, got %d", w.Code)
	}
	// Must contain sleep+retry logic, NOT kernel+initrd (no deploy attempt).
	if !strings.Contains(body, "sleep") || !strings.Contains(body, "retry") {
		t.Errorf("reimage_pending (no image) -> expected wait-retry script; got:\n%s", body)
	}
	if strings.Contains(body, "kernel") && strings.Contains(body, "initrd") {
		t.Errorf("reimage_pending (no image) -> must not serve deploy script when no image assigned; got:\n%s", body)
	}
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

// makeTestImage creates a BaseImage with the given firmware type and returns its ID.
func makeTestImage(t *testing.T, d *db.DB, firmware api.ImageFirmware) string {
	t.Helper()
	imgID := "img-" + string(firmware) + "-test"
	img := api.BaseImage{
		ID:       imgID,
		Name:     "test-image-" + string(firmware),
		Version:  "1.0",
		OS:       "rocky",
		Arch:     "x86_64",
		Status:   api.ImageStatusReady,
		Format:   api.ImageFormatFilesystem,
		Firmware: firmware,
		CreatedAt: time.Now().UTC(),
	}
	if err := d.CreateBaseImage(t.Context(), img); err != nil {
		t.Fatalf("makeTestImage CreateBaseImage(%s): %v", firmware, err)
	}
	return imgID
}

// withChiParam injects a chi URL param into r's context so handlers that call
// chi.URLParam(r, name) work without a real chi router in unit tests.
func withChiParam(r *http.Request, name, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(name, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// makeTestImageUUID creates a BaseImage with a valid UUID as its ID (required for
// handlers that validate imageID against the UUID regexp, e.g. ServeStatelessInitramfs).
func makeTestImageUUID(t *testing.T, d *db.DB, firmware api.ImageFirmware) string {
	t.Helper()
	// Use a fixed, well-formed UUID so the handler's UUID regexp accepts it.
	imgID := "6b875781-aaaa-bbbb-cccc-ddddeeeeffff"
	img := api.BaseImage{
		ID:        imgID,
		Name:      "test-stateless-image-" + string(firmware),
		Version:   "1.0",
		OS:        "rocky",
		Arch:      "x86_64",
		Status:    api.ImageStatusReady,
		Format:    api.ImageFormatFilesystem,
		Firmware:  firmware,
		CreatedAt: time.Now().UTC(),
	}
	if err := d.CreateBaseImage(t.Context(), img); err != nil {
		t.Fatalf("makeTestImageUUID CreateBaseImage: %v", err)
	}
	return imgID
}

// TestStatelessInitrdRouteServes200ForKnownImage verifies that a GET for a known
// image's stateless initramfs returns 200 and a non-empty body.
func TestStatelessInitrdRouteServes200ForKnownImage(t *testing.T) {
	d := openTestDB(t)
	// Must use a UUID-format imageID because ServeStatelessInitramfs validates it
	// against the UUID regexp before DB lookup.
	imgID := makeTestImageUUID(t, d, api.FirmwareUEFI)

	// Write a stub stateless initramfs file into a temp BootDir.
	bootDir := t.TempDir()
	stubContent := []byte("stub-stateless-initramfs-payload")
	imgFile := filepath.Join(bootDir, imgID+"-stateless.img")
	if err := os.WriteFile(imgFile, stubContent, 0644); err != nil {
		t.Fatalf("write stub initramfs: %v", err)
	}

	h := &BootHandler{
		BootDir:   bootDir,
		ServerURL: "http://10.99.0.1:8080",
		DB:        d,
	}

	param := imgID + "-stateless.img"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/"+param, nil)
	req = withChiParam(req, "imageIDStateless", param)
	w := httptest.NewRecorder()
	h.ServeStatelessInitramfs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("ServeStatelessInitramfs: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if w.Body.Len() == 0 {
		t.Error("ServeStatelessInitramfs: expected non-empty body for known image")
	}
}

// TestStatelessInitrdRouteReturns404ForUnknownImage verifies that a bogus but
// well-formed UUID returns 404.
func TestStatelessInitrdRouteReturns404ForUnknownImage(t *testing.T) {
	d := openTestDB(t)
	h := &BootHandler{
		BootDir:   t.TempDir(),
		ServerURL: "http://10.99.0.1:8080",
		DB:        d,
	}

	bogusID := "deadbeef-0000-0000-0000-000000000000"
	param := bogusID + "-stateless.img"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/"+param, nil)
	req = withChiParam(req, "imageIDStateless", param)
	w := httptest.NewRecorder()
	h.ServeStatelessInitramfs(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("ServeStatelessInitramfs unknown image: got %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestStatelessInitrdRouteRejectsBadImageID verifies that a non-UUID imageID path
// returns 400, not a 404 or a file-open attempt.
func TestStatelessInitrdRouteRejectsBadImageID(t *testing.T) {
	d := openTestDB(t)
	h := &BootHandler{
		BootDir:   t.TempDir(),
		ServerURL: "http://10.99.0.1:8080",
		DB:        d,
	}

	for _, bad := range []string{
		"not-a-uuid-stateless.img",
		"../../etc/passwd-stateless.img",
		"-stateless.img",
		"DEADBEEF-0000-0000-0000-000000000000-stateless.img", // uppercase
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/"+bad, nil)
		req = withChiParam(req, "imageIDStateless", bad)
		w := httptest.NewRecorder()
		h.ServeStatelessInitramfs(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("ServeStatelessInitramfs bad ID %q: got %d, want 400; body: %s",
				bad, w.Code, w.Body.String())
		}
	}
}

// makeDeployedNodeWithImage creates a deployed+verified node linked to an image.
func makeDeployedNodeWithImage(t *testing.T, d *db.DB, mac, hostname, imageID string) api.NodeConfig {
	t.Helper()
	node := makeTestNode(t, d, mac, hostname)
	node.BaseImageID = imageID
	if err := d.UpdateNodeConfig(t.Context(), node); err != nil {
		t.Fatalf("makeDeployedNodeWithImage UpdateNodeConfig: %v", err)
	}
	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("makeDeployedNodeWithImage RecordDeploySucceeded: %v", err)
	}
	if _, err := d.RecordVerifyBooted(t.Context(), node.ID); err != nil {
		t.Fatalf("makeDeployedNodeWithImage RecordVerifyBooted: %v", err)
	}
	got, err := d.GetNodeConfigByMAC(t.Context(), mac)
	if err != nil {
		t.Fatalf("makeDeployedNodeWithImage GetNodeConfigByMAC: %v", err)
	}
	return got
}

// TestServeIPXEScript_BIOSNode_UsesSanboot verifies that a deployed BIOS node
// receives a sanboot disk-boot script (INT 13h), not an exit-based UEFI script.
// sanboot is required for SeaBIOS — exit causes an infinite PXE loop on SeaBIOS.
func TestServeIPXEScript_BIOSNode_UsesSanboot(t *testing.T) {
	d := openTestDB(t)
	imgID := makeTestImage(t, d, api.FirmwareBIOS)
	makeDeployedNodeWithImage(t, d, "aa:bb:cc:dd:ee:b1", "bios-node01", imgID)

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac=aa:bb:cc:dd:ee:b1", nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertBIOSDiskBoot(t, w, "BIOS deployed_verified -> sanboot")
}

// TestServeIPXEScript_UEFINode_UsesExit verifies that a deployed UEFI node
// receives an exit-based disk-boot script (returns to UEFI firmware boot order),
// NOT a sanboot script. sanboot uses INT 13h which is unavailable on OVMF/EDK2.
func TestServeIPXEScript_UEFINode_UsesExit(t *testing.T) {
	d := openTestDB(t)
	imgID := makeTestImage(t, d, api.FirmwareUEFI)
	makeDeployedNodeWithImage(t, d, "aa:bb:cc:dd:ee:e1", "uefi-node01", imgID)

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac=aa:bb:cc:dd:ee:e1", nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	assertUEFIDiskBoot(t, w, "UEFI deployed_verified -> exit")
}

// TestServeIPXEScript_NoImageID_DefaultsToUEFI verifies that a deployed node with
// no BaseImageID (e.g. manually deployed without image assignment) defaults to the
// UEFI exit script, which is the safe default for modern hardware.
func TestServeIPXEScript_NoImageID_DefaultsToUEFI(t *testing.T) {
	d := openTestDB(t)
	node := makeTestNode(t, d, "aa:bb:cc:dd:ee:f0", "noimage-node01")
	// Deploy without setting BaseImageID.
	if err := d.RecordDeploySucceeded(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}
	if _, err := d.RecordVerifyBooted(t.Context(), node.ID); err != nil {
		t.Fatalf("RecordVerifyBooted: %v", err)
	}

	h := newBootHandler(d)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/ipxe?mac=aa:bb:cc:dd:ee:f0", nil)
	w := httptest.NewRecorder()
	h.ServeIPXEScript(w, req)

	// No image = no firmware info = UEFI default (safe for new images).
	assertUEFIDiskBoot(t, w, "no image -> UEFI exit default")
}
