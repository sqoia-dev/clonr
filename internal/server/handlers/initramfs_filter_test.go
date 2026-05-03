package handlers

// initramfs_filter_test.go — tests for the initramfs image exclusion invariant.
//
// Covers:
//   - GET /api/v1/images?kind=base        — excludes initramfs build artifacts
//   - GET /api/v1/images?kind=initramfs   — returns only initramfs images
//   - GET /api/v1/images (no kind param)  — returns both (backward compat)
//   - PATCH /api/v1/nodes/:id with an initramfs base_image_id — 400
//   - POST /api/v1/nodes/:id/reimage with an initramfs image_id — 400
//   - POST /api/v1/nodes/:id/reimage with initramfs image_id + force=true — still 400

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// seedTwoImages inserts one deployable image and one initramfs image into the
// database, returning both.
func seedTwoImages(t *testing.T, d *db.DB) (deployable, initramfs api.BaseImage) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	deployable = api.BaseImage{
		ID:          "img-deployable",
		Name:        "rocky9-clean",
		Version:     "9.7",
		Status:      api.ImageStatusReady,
		Format:      api.ImageFormatBlock,
		BuildMethod: "disk-capture",
		Tags:        []string{},
		CreatedAt:   now,
	}
	if err := d.CreateBaseImage(ctx, deployable); err != nil {
		t.Fatalf("seedTwoImages: create deployable: %v", err)
	}

	initramfs = api.BaseImage{
		ID:          "img-initramfs",
		Name:        "clustr-initramfs",
		Version:     "1.0",
		Status:      api.ImageStatusReady,
		Format:      api.ImageFormatBlock,
		BuildMethod: "initramfs",
		Tags:        []string{},
		CreatedAt:   now,
	}
	if err := d.CreateBaseImage(ctx, initramfs); err != nil {
		t.Fatalf("seedTwoImages: create initramfs: %v", err)
	}
	return deployable, initramfs
}

// listImages fires GET /api/v1/images with the given query string (e.g. "?kind=base").
func listImages(h *ImagesHandler, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/images"+query, nil)
	w := httptest.NewRecorder()
	h.ListImages(w, req)
	return w
}

// decodeImagesList decodes a ListImagesResponse from the recorder body.
func decodeImagesList(t *testing.T, w *httptest.ResponseRecorder) api.ListImagesResponse {
	t.Helper()
	var resp api.ListImagesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decodeImagesList: %v (body: %s)", err, w.Body.String())
	}
	return resp
}

// ─── ListImages kind filter tests ────────────────────────────────────────────

// TestListImages_KindBase_ExcludesInitramfs verifies that ?kind=base returns
// only non-initramfs images.
func TestListImages_KindBase_ExcludesInitramfs(t *testing.T) {
	h, d := newImagesHandler(t)
	_, _ = seedTwoImages(t, d)

	w := listImages(h, "?kind=base")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	resp := decodeImagesList(t, w)

	for _, img := range resp.Images {
		if img.BuildMethod == "initramfs" {
			t.Errorf("?kind=base: initramfs image %q should not appear in results", img.ID)
		}
	}
	found := false
	for _, img := range resp.Images {
		if img.ID == "img-deployable" {
			found = true
		}
	}
	if !found {
		t.Error("?kind=base: deployable image should appear in results")
	}
}

// TestListImages_KindInitramfs_OnlyInitramfs verifies that ?kind=initramfs returns
// only initramfs-method images.
func TestListImages_KindInitramfs_OnlyInitramfs(t *testing.T) {
	h, d := newImagesHandler(t)
	_, _ = seedTwoImages(t, d)

	w := listImages(h, "?kind=initramfs")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	resp := decodeImagesList(t, w)

	for _, img := range resp.Images {
		if img.BuildMethod != "initramfs" {
			t.Errorf("?kind=initramfs: non-initramfs image %q should not appear in results", img.ID)
		}
	}
	found := false
	for _, img := range resp.Images {
		if img.ID == "img-initramfs" {
			found = true
		}
	}
	if !found {
		t.Error("?kind=initramfs: initramfs image should appear in results")
	}
}

// TestListImages_NoKind_ReturnsBoth verifies that omitting ?kind returns all
// images (backward compatibility).
func TestListImages_NoKind_ReturnsBoth(t *testing.T) {
	h, d := newImagesHandler(t)
	_, _ = seedTwoImages(t, d)

	w := listImages(h, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	resp := decodeImagesList(t, w)

	ids := make(map[string]bool)
	for _, img := range resp.Images {
		ids[img.ID] = true
	}
	if !ids["img-deployable"] {
		t.Error("no-kind: deployable image missing from full list")
	}
	if !ids["img-initramfs"] {
		t.Error("no-kind: initramfs image missing from full list")
	}
}

// ─── PatchNode base_image_id guard ───────────────────────────────────────────

// patchNodeRequest fires PATCH /api/v1/nodes/:id with the given JSON body.
func patchNodeRequest(t *testing.T, h *NodesHandler, nodeID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("patchNodeRequest json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/nodes/"+nodeID, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", nodeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.PatchNode(w, req)
	return w
}

// TestPatchNode_InitramfsBaseImageID_Rejected verifies that PATCH with an
// initramfs image as base_image_id returns 400.
func TestPatchNode_InitramfsBaseImageID_Rejected(t *testing.T) {
	d := openTestDB(t)
	h := newNodesHandler(d)

	// Seed images and a node.
	_, initramfsImg := seedTwoImages(t, d)
	node := makeTestNode(t, d, "aa:bb:cc:00:11:22", "compute-01")

	w := patchNodeRequest(t, h, node.ID, map[string]string{
		"base_image_id": initramfsImg.ID,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when assigning initramfs as base_image_id, got %d; body: %s",
			w.Code, w.Body.String())
	}

	var resp api.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err == nil {
		if resp.Code != "initramfs_not_deployable" {
			t.Errorf("expected code=initramfs_not_deployable, got %q", resp.Code)
		}
	}

	// Verify the node's base_image_id was NOT updated.
	updated, err := d.GetNodeConfig(context.Background(), node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig after rejected patch: %v", err)
	}
	if updated.BaseImageID == initramfsImg.ID {
		t.Error("base_image_id was updated despite 400 — invariant violated")
	}
}

// TestPatchNode_DeployableBaseImageID_Accepted verifies that PATCH with a
// normal deployable image is accepted (regression guard).
func TestPatchNode_DeployableBaseImageID_Accepted(t *testing.T) {
	d := openTestDB(t)
	h := newNodesHandler(d)

	deployableImg, _ := seedTwoImages(t, d)
	node := makeTestNode(t, d, "aa:bb:cc:00:11:33", "compute-02")

	w := patchNodeRequest(t, h, node.ID, map[string]string{
		"base_image_id": deployableImg.ID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 when assigning deployable image, got %d; body: %s",
			w.Code, w.Body.String())
	}
}

// ─── Reimage Create initramfs guard ──────────────────────────────────────────

// postReimageRequest fires POST /api/v1/nodes/:id/reimage with the given body.
func postReimageRequest(t *testing.T, h *ReimageHandler, nodeID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("postReimageRequest json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/reimage", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", nodeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.Create(w, req)
	return w
}

// TestReimage_InitramfsImageID_Rejected verifies that POST /reimage with an
// initramfs image_id returns 400 even when force=true.
func TestReimage_InitramfsImageID_Rejected(t *testing.T) {
	d := openTestDB(t)
	// Orchestrator is nil — the initramfs check fires before any orchestrator
	// interaction, so nil is safe for these tests.
	h := &ReimageHandler{DB: d}

	_, initramfsImg := seedTwoImages(t, d)
	node := makeTestNode(t, d, "aa:bb:cc:00:22:11", "compute-03")

	for _, force := range []bool{false, true} {
		w := postReimageRequest(t, h, node.ID, map[string]interface{}{
			"image_id": initramfsImg.ID,
			"force":    force,
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("force=%v: expected 400 when reimaging with initramfs image_id, got %d; body: %s",
				force, w.Code, w.Body.String())
		}
		var resp api.ErrorResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err == nil {
			if resp.Code != "initramfs_not_deployable" {
				t.Errorf("force=%v: expected code=initramfs_not_deployable, got %q", force, resp.Code)
			}
		}
	}
}

// TestReimage_DeployableImageID_PassesInitramfsCheck verifies that a deployable
// image passes the initramfs guard (it may still fail for other reasons — SSH
// keys not set — but it must not fail with initramfs_not_deployable).
func TestReimage_DeployableImageID_PassesInitramfsCheck(t *testing.T) {
	d := openTestDB(t)
	h := &ReimageHandler{DB: d}

	deployableImg, _ := seedTwoImages(t, d)
	node := makeTestNode(t, d, "aa:bb:cc:00:22:22", "compute-04")

	w := postReimageRequest(t, h, node.ID, map[string]interface{}{
		"image_id": deployableImg.ID,
	})

	// We expect anything except 400 initramfs_not_deployable.
	// The request will fail with 400 "no_ssh_keys" (GAP-15) since the test node
	// has no SSH keys configured, but the initramfs guard must not be the reason.
	var resp api.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err == nil {
		if resp.Code == "initramfs_not_deployable" {
			t.Errorf("deployable image incorrectly rejected as initramfs: %s", w.Body.String())
		}
	}
}
