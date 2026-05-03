package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── fakes ────────────────────────────────────────────────────────────────────

// fakeDiskLayoutsDB implements DiskLayoutsDBIface for handler tests.
type fakeDiskLayoutsDB struct {
	layouts map[string]api.StoredDiskLayout
	// groupLayoutID and nodeLayoutID are used by GetGroup/NodeDiskLayoutID.
	groupLayoutID map[string]string
	nodeLayoutID  map[string]string
}

func newFakeDiskLayoutsDB() *fakeDiskLayoutsDB {
	return &fakeDiskLayoutsDB{
		layouts:       make(map[string]api.StoredDiskLayout),
		groupLayoutID: make(map[string]string),
		nodeLayoutID:  make(map[string]string),
	}
}

func (f *fakeDiskLayoutsDB) CreateDiskLayout(_ context.Context, dl api.StoredDiskLayout) error {
	f.layouts[dl.ID] = dl
	return nil
}

func (f *fakeDiskLayoutsDB) GetDiskLayout(_ context.Context, id string) (api.StoredDiskLayout, error) {
	dl, ok := f.layouts[id]
	if !ok {
		return api.StoredDiskLayout{}, api.ErrNotFound
	}
	return dl, nil
}

func (f *fakeDiskLayoutsDB) ListDiskLayouts(_ context.Context) ([]api.StoredDiskLayout, error) {
	out := make([]api.StoredDiskLayout, 0, len(f.layouts))
	for _, dl := range f.layouts {
		out = append(out, dl)
	}
	return out, nil
}

func (f *fakeDiskLayoutsDB) UpdateDiskLayoutFields(_ context.Context, id, name string, layout api.DiskLayout) error {
	dl, ok := f.layouts[id]
	if !ok {
		return api.ErrNotFound
	}
	dl.Name = name
	dl.Layout = layout
	dl.UpdatedAt = time.Now().UTC()
	f.layouts[id] = dl
	return nil
}

func (f *fakeDiskLayoutsDB) DeleteDiskLayout(_ context.Context, id string) error {
	if _, ok := f.layouts[id]; !ok {
		return api.ErrNotFound
	}
	delete(f.layouts, id)
	return nil
}

func (f *fakeDiskLayoutsDB) DiskLayoutRefCount(_ context.Context, id string) (int, error) {
	count := 0
	for _, v := range f.groupLayoutID {
		if v == id {
			count++
		}
	}
	for _, v := range f.nodeLayoutID {
		if v == id {
			count++
		}
	}
	return count, nil
}

func (f *fakeDiskLayoutsDB) GetNodeDiskLayoutID(_ context.Context, nodeID string) (string, error) {
	return f.nodeLayoutID[nodeID], nil
}

func (f *fakeDiskLayoutsDB) GetGroupDiskLayoutID(_ context.Context, groupID string) (string, error) {
	return f.groupLayoutID[groupID], nil
}

// fakeCaptureHub implements DiskLayoutsCaptureHub.
type fakeCaptureHub struct {
	connected bool
	// queuedResult will be delivered to the next RegisterDiskCapture channel.
	queuedResult *clientd.DiskCaptureResultPayload
	// sentMessages accumulates messages sent via Send.
	sentMessages []clientd.ServerMessage
}

func (f *fakeCaptureHub) IsConnected(_ string) bool { return f.connected }

func (f *fakeCaptureHub) Send(_ string, msg clientd.ServerMessage) error {
	f.sentMessages = append(f.sentMessages, msg)
	return nil
}

func (f *fakeCaptureHub) RegisterDiskCapture(msgID string) <-chan clientd.DiskCaptureResultPayload {
	ch := make(chan clientd.DiskCaptureResultPayload, 1)
	if f.queuedResult != nil {
		r := *f.queuedResult
		r.RefMsgID = msgID
		ch <- r
	}
	return ch
}

func (f *fakeCaptureHub) UnregisterDiskCapture(_ string) {}

// ─── helpers ─────────────────────────────────────────────────────────────────

func makeTestDiskLayout(name string) api.StoredDiskLayout {
	now := time.Now().UTC().Truncate(time.Second)
	return api.StoredDiskLayout{
		ID:         uuid.New().String(),
		Name:       name,
		CapturedAt: now,
		Layout: api.DiskLayout{
			Partitions: []api.PartitionSpec{
				{Label: "root", SizeBytes: 0, Filesystem: "xfs", MountPoint: "/"},
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// newChiContext creates an http.Request with a chi URL param set, so handlers
// that call chi.URLParam(r, "id") work in unit tests without a real router.
func newChiContext(r *http.Request, paramName, paramValue string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(paramName, paramValue)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// ─── CaptureLayout tests ──────────────────────────────────────────────────────

func TestDiskLayoutCapture_NodeOffline(t *testing.T) {
	db := newFakeDiskLayoutsDB()
	hub := &fakeCaptureHub{connected: false}
	h := &DiskLayoutsHandler{DB: db, Hub: hub}

	body := `{"name":"test-layout"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/disk-layouts/capture/node-1",
		bytes.NewBufferString(body))
	req = newChiContext(req, "node_id", "node-1")
	w := httptest.NewRecorder()
	h.CaptureLayout(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (node offline)", w.Code)
	}
}

func TestDiskLayoutCapture_Success(t *testing.T) {
	db := newFakeDiskLayoutsDB()
	hub := &fakeCaptureHub{
		connected: true,
		queuedResult: &clientd.DiskCaptureResultPayload{
			LayoutJSON: `{"partitions":[{"label":"root","size_bytes":0,"filesystem":"xfs","mountpoint":"/","flags":null}],"bootloader":{"type":"grub2","target":"x86_64-efi"}}`,
		},
	}
	h := &DiskLayoutsHandler{DB: db, Hub: hub}

	body := `{"name":"captured-layout"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/disk-layouts/capture/node-1",
		bytes.NewBufferString(body))
	req = newChiContext(req, "node_id", "node-1")
	w := httptest.NewRecorder()
	h.CaptureLayout(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]api.StoredDiskLayout
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	dl, ok := resp["disk_layout"]
	if !ok {
		t.Fatal("response missing disk_layout key")
	}
	if dl.Name != "captured-layout" {
		t.Errorf("name = %q, want captured-layout", dl.Name)
	}
	if dl.SourceNodeID != "node-1" {
		t.Errorf("source_node_id = %q, want node-1", dl.SourceNodeID)
	}
}

func TestDiskLayoutCapture_NodeReturnsError(t *testing.T) {
	db := newFakeDiskLayoutsDB()
	hub := &fakeCaptureHub{
		connected: true,
		queuedResult: &clientd.DiskCaptureResultPayload{
			Error: "lsblk not available",
		},
	}
	h := &DiskLayoutsHandler{DB: db, Hub: hub}

	body := `{"name":"will-fail"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/disk-layouts/capture/node-1",
		bytes.NewBufferString(body))
	req = newChiContext(req, "node_id", "node-1")
	w := httptest.NewRecorder()
	h.CaptureLayout(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", w.Code)
	}
}

// ─── ListLayouts test ─────────────────────────────────────────────────────────

func TestDiskLayoutList(t *testing.T) {
	db := newFakeDiskLayoutsDB()
	db.layouts["id1"] = makeTestDiskLayout("layout-a")
	db.layouts["id2"] = makeTestDiskLayout("layout-b")
	h := &DiskLayoutsHandler{DB: db}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/disk-layouts", nil)
	w := httptest.NewRecorder()
	h.ListLayouts(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp api.ListDiskLayoutsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
	if len(resp.Layouts) != 2 {
		t.Errorf("layouts len = %d, want 2", len(resp.Layouts))
	}
}

// ─── GetLayout test ───────────────────────────────────────────────────────────

func TestDiskLayoutGet_NotFound(t *testing.T) {
	h := &DiskLayoutsHandler{DB: newFakeDiskLayoutsDB()}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/disk-layouts/missing", nil)
	req = newChiContext(req, "id", "missing")
	w := httptest.NewRecorder()
	h.GetLayout(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDiskLayoutGet_Found(t *testing.T) {
	db := newFakeDiskLayoutsDB()
	dl := makeTestDiskLayout("my-layout")
	db.layouts[dl.ID] = dl
	h := &DiskLayoutsHandler{DB: db}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/disk-layouts/"+dl.ID, nil)
	req = newChiContext(req, "id", dl.ID)
	w := httptest.NewRecorder()
	h.GetLayout(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]api.StoredDiskLayout
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["disk_layout"].Name != "my-layout" {
		t.Errorf("name = %q, want my-layout", resp["disk_layout"].Name)
	}
}

// ─── UpdateLayout tests ───────────────────────────────────────────────────────

func TestDiskLayoutUpdate_RenameOnly(t *testing.T) {
	db := newFakeDiskLayoutsDB()
	dl := makeTestDiskLayout("old-name")
	db.layouts[dl.ID] = dl
	h := &DiskLayoutsHandler{DB: db}

	body := `{"name":"new-name"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/disk-layouts/"+dl.ID,
		bytes.NewBufferString(body))
	req = newChiContext(req, "id", dl.ID)
	w := httptest.NewRecorder()
	h.UpdateLayout(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]api.StoredDiskLayout
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["disk_layout"].Name != "new-name" {
		t.Errorf("name = %q, want new-name", resp["disk_layout"].Name)
	}
}

func TestDiskLayoutUpdate_InvalidLayoutJSON(t *testing.T) {
	db := newFakeDiskLayoutsDB()
	dl := makeTestDiskLayout("layout")
	db.layouts[dl.ID] = dl
	h := &DiskLayoutsHandler{DB: db}

	body := `{"layout_json":"not valid json at all !!!"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/disk-layouts/"+dl.ID,
		bytes.NewBufferString(body))
	req = newChiContext(req, "id", dl.ID)
	w := httptest.NewRecorder()
	h.UpdateLayout(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ─── DeleteLayout tests ───────────────────────────────────────────────────────

func TestDiskLayoutDelete_InUse(t *testing.T) {
	db := newFakeDiskLayoutsDB()
	dl := makeTestDiskLayout("in-use")
	db.layouts[dl.ID] = dl
	// Simulate a group referencing this layout.
	db.groupLayoutID["group-1"] = dl.ID
	h := &DiskLayoutsHandler{DB: db}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/disk-layouts/"+dl.ID, nil)
	req = newChiContext(req, "id", dl.ID)
	w := httptest.NewRecorder()
	h.DeleteLayout(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestDiskLayoutDelete_Success(t *testing.T) {
	db := newFakeDiskLayoutsDB()
	dl := makeTestDiskLayout("deletable")
	db.layouts[dl.ID] = dl
	h := &DiskLayoutsHandler{DB: db}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/disk-layouts/"+dl.ID, nil)
	req = newChiContext(req, "id", dl.ID)
	w := httptest.NewRecorder()
	h.DeleteLayout(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
	if _, ok := db.layouts[dl.ID]; ok {
		t.Error("layout still present in DB after delete")
	}
}

func TestDiskLayoutDelete_NotFound(t *testing.T) {
	h := &DiskLayoutsHandler{DB: newFakeDiskLayoutsDB()}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/disk-layouts/ghost", nil)
	req = newChiContext(req, "id", "ghost")
	w := httptest.NewRecorder()
	h.DeleteLayout(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
