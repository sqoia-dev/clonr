package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/image"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/rs/zerolog"
)

// ── stub helpers ──────────────────────────────────────────────────────────────

// stubImageBuildStore implements ImageBuildStoreIface for tests.
type stubImageBuildStore struct {
	ids []string
}

func (s *stubImageBuildStore) ActiveBuildIDs() []string { return s.ids }

// stubDeployProgressLister implements DeployProgressLister for tests.
type stubDeployProgressLister struct {
	entries []api.DeployProgress
}

func (s *stubDeployProgressLister) List() []api.DeployProgress { return s.entries }

// stubActiveReimagesDB implements ActiveReimagesDBIface for tests.
type stubActiveReimagesDB struct {
	ids []string
	err error
}

func (s *stubActiveReimagesDB) ListAllActiveReimageIDs(_ context.Context) ([]string, error) {
	return s.ids, s.err
}
func (s *stubActiveReimagesDB) WaitForActiveReimages(_ context.Context) {}

// stubDHCPLeases implements DHCPLeasesIface for tests.
type stubDHCPLeases struct {
	macs []string
}

func (s *stubDHCPLeases) RecentLeases(_ time.Duration) []string { return s.macs }

// newNullShellManager returns a ShellManager with no sessions (no DB needed).
func newNullShellManager() *image.ShellManager {
	return image.NewShellManager(nil, "/tmp", zerolog.Nop())
}

// ── idle case ─────────────────────────────────────────────────────────────────

func TestGetActiveJobs_Idle(t *testing.T) {
	h := &SystemHandler{
		Initramfs:   &InitramfsHandler{},
		ImageBuilds: &stubImageBuildStore{},
		Reimages:    &stubActiveReimagesDB{},
		Shells:      newNullShellManager(),
		DHCPLeases:  &stubDHCPLeases{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(body.InitramfsBuilds) != 0 {
		t.Errorf("expected empty initramfs_builds, got %v", body.InitramfsBuilds)
	}
	if len(body.ImageBuilds) != 0 {
		t.Errorf("expected empty image_builds, got %v", body.ImageBuilds)
	}
	if len(body.Reimages) != 0 {
		t.Errorf("expected empty reimages, got %v", body.Reimages)
	}
	if len(body.OperatorSessions) != 0 {
		t.Errorf("expected empty operator_sessions, got %v", body.OperatorSessions)
	}
	if len(body.PxeInFlight) != 0 {
		t.Errorf("expected empty pxe_in_flight, got %v", body.PxeInFlight)
	}
}

// ── initramfs build via BuildSession (BUG-14 path) ───────────────────────────

func TestGetActiveJobs_InitramfsBuildInFlight(t *testing.T) {
	ih := &InitramfsHandler{}

	// Simulate an in-flight BuildInitramfsFromImage build (BUG-14 path):
	// register an active (not-done) BuildSession.
	buildID := "test-build-1234"
	ih.mu.Lock()
	ih.running = true
	ih.activeBuildID = buildID
	ih.sessions = map[string]*BuildSession{
		buildID: {
			buildID: buildID,
			newLine: make(chan struct{}),
			// done == false by default
		},
	}
	ih.mu.Unlock()

	h := &SystemHandler{
		Initramfs:   ih,
		ImageBuilds: &stubImageBuildStore{},
		Reimages:    &stubActiveReimagesDB{},
		Shells:      newNullShellManager(),
		DHCPLeases:  &stubDHCPLeases{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(body.InitramfsBuilds) != 1 {
		t.Errorf("expected 1 initramfs_build entry, got %d: %v", len(body.InitramfsBuilds), body.InitramfsBuilds)
	}
	expected := "initramfs_" + buildID
	if body.InitramfsBuilds[0] != expected {
		t.Errorf("expected %q, got %q", expected, body.InitramfsBuilds[0])
	}
	if len(body.ImageBuilds) != 0 {
		t.Errorf("expected empty image_builds, got %v", body.ImageBuilds)
	}
	if len(body.Reimages) != 0 {
		t.Errorf("expected empty reimages, got %v", body.Reimages)
	}
}

// ── legacy RebuildInitramfs single-slot (no BuildSession) ────────────────────

func TestGetActiveJobs_InitramfsRebuildLegacy(t *testing.T) {
	ih := &InitramfsHandler{}
	// Simulate RebuildInitramfs (sets running=true but no session entry).
	ih.mu.Lock()
	ih.running = true
	ih.mu.Unlock()

	h := &SystemHandler{
		Initramfs:   ih,
		ImageBuilds: &stubImageBuildStore{},
		Reimages:    &stubActiveReimagesDB{},
		Shells:      newNullShellManager(),
		DHCPLeases:  &stubDHCPLeases{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.InitramfsBuilds) != 1 {
		t.Errorf("expected 1 initramfs_builds entry, got %d: %v", len(body.InitramfsBuilds), body.InitramfsBuilds)
	}
	if body.InitramfsBuilds[0] != "initramfs_rebuild" {
		t.Errorf("expected 'initramfs_rebuild', got %q", body.InitramfsBuilds[0])
	}
}

// ── nil InitramfsHandler ──────────────────────────────────────────────────────

func TestGetActiveJobs_NilInitramfsHandler(t *testing.T) {
	h := &SystemHandler{
		Initramfs:   nil,
		ImageBuilds: &stubImageBuildStore{},
		Reimages:    &stubActiveReimagesDB{},
		Shells:      newNullShellManager(),
		DHCPLeases:  &stubDHCPLeases{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(body.InitramfsBuilds) != 0 {
		t.Errorf("expected empty initramfs_builds with nil handler, got %v", body.InitramfsBuilds)
	}
}

// ── image build active ────────────────────────────────────────────────────────

func TestGetActiveJobs_ImageBuildInFlight(t *testing.T) {
	h := &SystemHandler{
		Initramfs:   &InitramfsHandler{},
		ImageBuilds: &stubImageBuildStore{ids: []string{"img-aabbccdd", "img-11223344"}},
		Reimages:    &stubActiveReimagesDB{},
		Shells:      newNullShellManager(),
		DHCPLeases:  &stubDHCPLeases{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.ImageBuilds) != 2 {
		t.Errorf("expected 2 image_builds, got %d: %v", len(body.ImageBuilds), body.ImageBuilds)
	}
	// Check prefix convention.
	for _, entry := range body.ImageBuilds {
		if len(entry) < 7 || entry[:6] != "image_" {
			t.Errorf("image_builds entry %q missing 'image_' prefix", entry)
		}
	}
	if len(body.InitramfsBuilds) != 0 {
		t.Errorf("expected empty initramfs_builds, got %v", body.InitramfsBuilds)
	}
}

// ── reimages active ───────────────────────────────────────────────────────────

func TestGetActiveJobs_ReimagesActive(t *testing.T) {
	h := &SystemHandler{
		Initramfs:   &InitramfsHandler{},
		ImageBuilds: &stubImageBuildStore{},
		Reimages:    &stubActiveReimagesDB{ids: []string{"req-xyz123", "req-abc456"}},
		Shells:      newNullShellManager(),
		DHCPLeases:  &stubDHCPLeases{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Reimages) != 2 {
		t.Errorf("expected 2 reimages, got %d: %v", len(body.Reimages), body.Reimages)
	}
	for _, entry := range body.Reimages {
		if len(entry) < 8 || entry[:8] != "reimage_" {
			t.Errorf("reimages entry %q missing 'reimage_' prefix", entry)
		}
	}
}

// ── PXE in-flight ─────────────────────────────────────────────────────────────

func TestGetActiveJobs_PxeInFlight(t *testing.T) {
	macs := []string{"bc:24:11:da:58:6a", "bc:24:11:bb:99:01"}
	h := &SystemHandler{
		Initramfs:   &InitramfsHandler{},
		ImageBuilds: &stubImageBuildStore{},
		Reimages:    &stubActiveReimagesDB{},
		Shells:      newNullShellManager(),
		DHCPLeases:  &stubDHCPLeases{macs: macs},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.PxeInFlight) != 2 {
		t.Errorf("expected 2 pxe_in_flight, got %d: %v", len(body.PxeInFlight), body.PxeInFlight)
	}
}

// ── nil optional sources ──────────────────────────────────────────────────────

func TestGetActiveJobs_NilSources(t *testing.T) {
	// All optional sources nil — should not panic and all fields empty.
	h := &SystemHandler{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.InitramfsBuilds) != 0 || len(body.ImageBuilds) != 0 ||
		len(body.Reimages) != 0 || len(body.OperatorSessions) != 0 ||
		len(body.PxeInFlight) != 0 {
		t.Errorf("expected all empty with nil sources, got: %+v", body)
	}
}

// ── DB error on reimages is non-fatal ─────────────────────────────────────────

func TestGetActiveJobs_ReimagesDBError_NonFatal(t *testing.T) {
	h := &SystemHandler{
		Initramfs:   &InitramfsHandler{},
		ImageBuilds: &stubImageBuildStore{},
		Reimages:    &stubActiveReimagesDB{err: context.DeadlineExceeded},
		Shells:      newNullShellManager(),
		DHCPLeases:  &stubDHCPLeases{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req) // must not panic

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 even on DB error, got %d", res.StatusCode)
	}
	var body api.ActiveJobsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Reimages must be empty (fail-open).
	if len(body.Reimages) != 0 {
		t.Errorf("expected empty reimages on DB error, got %v", body.Reimages)
	}
}

// ── all categories active simultaneously ──────────────────────────────────────

func TestGetActiveJobs_AllActive(t *testing.T) {
	ih := &InitramfsHandler{}
	buildID := "build-all-active"
	ih.mu.Lock()
	ih.running = true
	ih.activeBuildID = buildID
	ih.sessions = map[string]*BuildSession{
		buildID: {buildID: buildID, newLine: make(chan struct{})},
	}
	ih.mu.Unlock()

	h := &SystemHandler{
		Initramfs:   ih,
		ImageBuilds: &stubImageBuildStore{ids: []string{"img-1"}},
		Reimages:    &stubActiveReimagesDB{ids: []string{"req-1"}},
		Shells:      newNullShellManager(),
		DHCPLeases:  &stubDHCPLeases{macs: []string{"aa:bb:cc:dd:ee:ff"}},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.InitramfsBuilds) == 0 {
		t.Error("expected non-empty initramfs_builds")
	}
	if len(body.ImageBuilds) == 0 {
		t.Error("expected non-empty image_builds")
	}
	if len(body.Reimages) == 0 {
		t.Error("expected non-empty reimages")
	}
	if len(body.PxeInFlight) == 0 {
		t.Error("expected non-empty pxe_in_flight")
	}
}

// ── BUG-18: active deploy progress visible in active-jobs ─────────────────────

// TestGetActiveJobs_DeploysActiveInFlight verifies that POST /api/v1/deploy/progress
// entries with non-terminal phases appear in the "deploys" field of GET
// /api/v1/system/active-jobs. This is the BUG-18 regression test.
//
// Before the fix, deploys from the node-initiated path (no reimage_requests row)
// were invisible to active-jobs, allowing autodeploy to restart clustr-serverd
// mid-deploy and strand the node.
func TestGetActiveJobs_DeploysActiveInFlight(t *testing.T) {
	// Seed the stub deploy progress lister with one active and one terminal entry.
	progressLister := &stubDeployProgressLister{
		entries: []api.DeployProgress{
			{
				NodeMAC:   "bc:24:11:da:58:6a",
				Hostname:  "node01",
				Phase:     "downloading", // active — must appear
				UpdatedAt: time.Now().UTC(),
			},
			{
				NodeMAC:   "bc:24:11:bb:99:01",
				Hostname:  "node02",
				Phase:     "complete", // terminal — must NOT appear
				UpdatedAt: time.Now().UTC(),
			},
			{
				NodeMAC:   "bc:24:11:cc:00:02",
				Hostname:  "node03",
				Phase:     "error", // terminal — must NOT appear
				UpdatedAt: time.Now().UTC(),
			},
		},
	}

	h := &SystemHandler{
		Initramfs:      &InitramfsHandler{},
		ImageBuilds:    &stubImageBuildStore{},
		Reimages:       &stubActiveReimagesDB{},
		DeployProgress: progressLister,
		Shells:         newNullShellManager(),
		DHCPLeases:     &stubDHCPLeases{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Only the "downloading" phase should be in deploys — "complete" and "error" are terminal.
	if len(body.Deploys) != 1 {
		t.Errorf("expected 1 deploy entry, got %d: %v", len(body.Deploys), body.Deploys)
	}
	if len(body.Deploys) > 0 {
		expected := "deploy_bc:24:11:da:58:6a"
		if body.Deploys[0] != expected {
			t.Errorf("deploy entry = %q, want %q", body.Deploys[0], expected)
		}
	}
	// Terminal entries must NOT appear.
	for _, entry := range body.Deploys {
		if strings.Contains(entry, "bb:99:01") || strings.Contains(entry, "cc:00:02") {
			t.Errorf("terminal deploy phase leaked into deploys: %q", entry)
		}
	}

	// Other categories must remain empty.
	if len(body.InitramfsBuilds) != 0 {
		t.Errorf("unexpected initramfs_builds: %v", body.InitramfsBuilds)
	}
	if len(body.Reimages) != 0 {
		t.Errorf("unexpected reimages: %v", body.Reimages)
	}
}

// TestGetActiveJobs_DeploysAllTerminalShowsEmpty verifies that when all deploy
// progress entries are in terminal phases, the deploys field is empty.
func TestGetActiveJobs_DeploysAllTerminalShowsEmpty(t *testing.T) {
	progressLister := &stubDeployProgressLister{
		entries: []api.DeployProgress{
			{NodeMAC: "aa:bb:cc:dd:ee:01", Phase: "complete", UpdatedAt: time.Now()},
			{NodeMAC: "aa:bb:cc:dd:ee:02", Phase: "error", UpdatedAt: time.Now()},
			{NodeMAC: "aa:bb:cc:dd:ee:03", Phase: "", UpdatedAt: time.Now()}, // empty phase — treated as terminal
		},
	}

	h := &SystemHandler{
		Initramfs:      &InitramfsHandler{},
		ImageBuilds:    &stubImageBuildStore{},
		Reimages:       &stubActiveReimagesDB{},
		DeployProgress: progressLister,
		Shells:         newNullShellManager(),
		DHCPLeases:     &stubDHCPLeases{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req)

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Deploys) != 0 {
		t.Errorf("expected empty deploys when all phases terminal, got %v", body.Deploys)
	}
}

// TestGetActiveJobs_NilDeployProgress verifies that a nil DeployProgress source
// does not panic and returns an empty deploys field.
func TestGetActiveJobs_NilDeployProgress(t *testing.T) {
	h := &SystemHandler{
		Initramfs:      &InitramfsHandler{},
		ImageBuilds:    &stubImageBuildStore{},
		Reimages:       &stubActiveReimagesDB{},
		DeployProgress: nil, // explicitly nil
		Shells:         newNullShellManager(),
		DHCPLeases:     &stubDHCPLeases{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/active-jobs", nil)
	w := httptest.NewRecorder()
	h.GetActiveJobs(w, req) // must not panic

	var body api.ActiveJobsResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Deploys) != 0 {
		t.Errorf("expected empty deploys with nil DeployProgress, got %v", body.Deploys)
	}
}
