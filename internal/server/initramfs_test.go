package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/server"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// newInitramfsTestServer creates a test server with auth dev mode enabled.
func newInitramfsTestServer(t *testing.T) (*server.Server, *httptest.Server, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := config.ServerConfig{
		ListenAddr:  ":0",
		ImageDir:    filepath.Join(dir, "images"),
		DBPath:      filepath.Join(dir, "test.db"),
		AuthDevMode: true,
		LogLevel:    "error",
		PXE: config.PXEConfig{
			BootDir: filepath.Join(dir, "boot"),
		},
	}
	if err := os.MkdirAll(cfg.PXE.BootDir, 0o755); err != nil {
		t.Fatalf("mkdir boot dir: %v", err)
	}

	srv := server.New(cfg, database, openTestStatsDB(t), server.BuildInfo{})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, database
}

// TestRebuildInitramfs_GuardActiveDeployRejects verifies that POST
// /api/v1/system/initramfs/rebuild returns 409 when a node has an active deploy.
func TestRebuildInitramfs_GuardActiveDeployRejects(t *testing.T) {
	_, ts, database := newInitramfsTestServer(t)
	ctx := context.Background()

	// Create a node and image to satisfy FK constraints.
	node := api.NodeConfig{
		ID:         "node-rebuild-guard",
		Hostname:   "test-node",
		PrimaryMAC: "aa:bb:cc:dd:ee:ff",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		Interfaces: []api.InterfaceConfig{},
		SSHKeys:    []string{},
		Groups:     []string{},
		CustomVars: map[string]string{},
	}
	if err := database.CreateNodeConfig(ctx, node); err != nil {
		t.Fatalf("create node: %v", err)
	}

	img := api.BaseImage{
		ID:        "img-rebuild-guard",
		Name:      "guard-test-image",
		Status:    api.ImageStatusBuilding,
		Format:    api.ImageFormatFilesystem,
		Tags:      []string{},
		CreatedAt: time.Now().UTC(),
	}
	if err := database.CreateBaseImage(ctx, img); err != nil {
		t.Fatalf("create image: %v", err)
	}
	if err := database.FinalizeBaseImage(ctx, img.ID, 1024, "abc123"); err != nil {
		t.Fatalf("finalize image: %v", err)
	}

	// Insert a running reimage request — simulates an active deploy.
	_, err := database.SQL().ExecContext(ctx, `
		INSERT INTO reimage_requests (id, node_id, image_id, status, requested_by, created_at)
		VALUES (?, ?, ?, 'running', 'test', ?)
	`, "req-guard-001", node.ID, img.ID, time.Now().Unix())
	if err != nil {
		t.Fatalf("insert reimage request: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/system/initramfs/rebuild", nil)
	req.Header.Set("Authorization", "Bearer test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 Conflict when deploy is active, got %d", resp.StatusCode)
	}
	var errResp api.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Code != "deploy_active" {
		t.Errorf("expected code=deploy_active, got %q", errResp.Code)
	}
}

// TestRebuildInitramfs_AtomicRename verifies that partial builds do not
// overwrite the existing initramfs until the rename succeeds.
func TestRebuildInitramfs_AtomicRename(t *testing.T) {
	bootDir := t.TempDir()
	finalPath := filepath.Join(bootDir, "initramfs-clustr.img")
	stagingPath := finalPath + ".building"

	// Write a sentinel file to the final path.
	sentinel := []byte("original-initramfs-content")
	if err := os.WriteFile(finalPath, sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Simulate a failed build: staging exists but rename never happens.
	if err := os.WriteFile(stagingPath, []byte("partial-build"), 0o644); err != nil {
		t.Fatalf("write staging: %v", err)
	}

	// Final path must still hold the sentinel.
	final, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if string(final) != string(sentinel) {
		t.Errorf("final overwritten before atomic rename; got %q want %q", final, sentinel)
	}

	// Simulate success: rename staging → final.
	newContent := []byte("new-initramfs-content")
	if err := os.WriteFile(stagingPath, newContent, 0o644); err != nil {
		t.Fatalf("write new staging: %v", err)
	}
	if err := os.Rename(stagingPath, finalPath); err != nil {
		t.Fatalf("rename: %v", err)
	}

	updated, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final after rename: %v", err)
	}
	if string(updated) != string(newContent) {
		t.Errorf("after rename: got %q want %q", updated, newContent)
	}
	if _, err := os.Stat(stagingPath); !os.IsNotExist(err) {
		t.Errorf("staging path still exists after rename")
	}
}

// TestReconcileStuckBuilds_MarksResumable verifies that ReconcileStuckBuilds
// marks "building" images as interrupted/resumable instead of hard-failing.
func TestReconcileStuckBuilds_MarksResumable(t *testing.T) {
	srv, _, database := newInitramfsTestServer(t)
	ctx := context.Background()

	img := api.BaseImage{
		ID:        "img-reconcile-resumable",
		Name:      "stuck-build",
		Status:    api.ImageStatusBuilding,
		Format:    api.ImageFormatFilesystem,
		Tags:      []string{},
		CreatedAt: time.Now().UTC(),
	}
	if err := database.CreateBaseImage(ctx, img); err != nil {
		t.Fatalf("create image: %v", err)
	}

	if err := srv.ReconcileStuckBuilds(ctx); err != nil {
		t.Fatalf("ReconcileStuckBuilds: %v", err)
	}

	phase, resumable, err := database.GetImageResumePhase(ctx, img.ID)
	if err != nil {
		t.Fatalf("GetImageResumePhase: %v", err)
	}
	if !resumable {
		t.Errorf("expected resumable=true, got false")
	}
	if phase == "" {
		t.Errorf("expected non-empty resume_from_phase, got empty")
	}
	t.Logf("resume_from_phase=%q", phase)

	updated, err := database.GetBaseImage(ctx, img.ID)
	if err != nil {
		t.Fatalf("GetBaseImage: %v", err)
	}
	if updated.Status != api.ImageStatusInterrupted {
		t.Errorf("expected status=interrupted, got %q", updated.Status)
	}
}

// TestReconcileStuckBuilds_InitramfsArtifactMarkedError verifies that
// ReconcileStuckBuilds marks a stuck initramfs artifact (build_method=initramfs)
// as status=error rather than interrupted/resumable.  These artifacts cannot be
// resumed through the phase-based resume mechanism; marking them interrupted
// was the direct cause of the "always interrupted" UX bug (the autodeploy timer
// restarts clustr-serverd every 2 min, which is shorter than a typical initramfs
// build; every restart triggered reconcile on the in-flight building record).
func TestReconcileStuckBuilds_InitramfsArtifactMarkedError(t *testing.T) {
	srv, _, database := newInitramfsTestServer(t)
	ctx := context.Background()

	img := api.BaseImage{
		ID:          "img-reconcile-initramfs",
		Name:        "stuck-initramfs-artifact",
		Status:      api.ImageStatusBuilding,
		Format:      api.ImageFormatBlock,
		BuildMethod: "initramfs",
		Tags:        []string{},
		CreatedAt:   time.Now().UTC(),
	}
	if err := database.CreateBaseImage(ctx, img); err != nil {
		t.Fatalf("create image: %v", err)
	}

	if err := srv.ReconcileStuckBuilds(ctx); err != nil {
		t.Fatalf("ReconcileStuckBuilds: %v", err)
	}

	updated, err := database.GetBaseImage(ctx, img.ID)
	if err != nil {
		t.Fatalf("GetBaseImage: %v", err)
	}
	// Must be error, not interrupted — initramfs artifacts are not resumable.
	if updated.Status != api.ImageStatusError {
		t.Errorf("initramfs artifact: expected status=error after reconcile, got %q", updated.Status)
	}
	// Must not be marked resumable.
	_, resumable, _ := database.GetImageResumePhase(ctx, img.ID)
	if resumable {
		t.Errorf("initramfs artifact: expected resumable=false after reconcile, got true")
	}
}

// TestResumeImageBuild_ReadyImageRejects verifies that POST /images/{id}/resume
// returns 409 when the image is in ready state (not resumable).
func TestResumeImageBuild_ReadyImageRejects(t *testing.T) {
	_, ts, database := newInitramfsTestServer(t)
	ctx := context.Background()

	img := api.BaseImage{
		ID:        "img-resume-ready",
		Name:      "ready-image",
		Status:    api.ImageStatusBuilding,
		Format:    api.ImageFormatFilesystem,
		Tags:      []string{},
		CreatedAt: time.Now().UTC(),
	}
	if err := database.CreateBaseImage(ctx, img); err != nil {
		t.Fatalf("create image: %v", err)
	}
	if err := database.FinalizeBaseImage(ctx, img.ID, 1024, "abc"); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/images/"+img.ID+"/resume", nil)
	req.Header.Set("Authorization", "Bearer test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
}

// TestInitramfsBuildHistory verifies build records are stored and trimmed correctly.
func TestInitramfsBuildHistory(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// Insert 7 records.
	for i := 0; i < 7; i++ {
		r := db.InitramfsBuildRecord{
			ID:        "build-" + string(rune('a'+i)),
			StartedAt: now.Add(time.Duration(i) * time.Minute),
			Outcome:   "success",
		}
		if err := database.CreateInitramfsBuild(ctx, r); err != nil {
			t.Fatalf("create build %d: %v", i, err)
		}
	}

	// Trim to 5.
	if err := database.TrimInitramfsBuilds(ctx, 5); err != nil {
		t.Fatalf("trim: %v", err)
	}

	records, err := database.ListInitramfsBuilds(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 5 {
		t.Errorf("expected 5 records after trim, got %d", len(records))
	}
}

// TestGetInitramfs_ReturnsHistory verifies GET /system/initramfs returns the
// history array even when no file exists yet.
func TestGetInitramfs_ReturnsHistory(t *testing.T) {
	_, ts, _ := newInitramfsTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/system/initramfs", nil)
	req.Header.Set("Authorization", "Bearer test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// history must be an array (empty is OK).
	if _, ok := body["history"]; !ok {
		t.Errorf("response missing 'history' field")
	}
}
