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
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── fake DB ─────────────────────────────────────────────────────────────────

type fakeBiosDB struct {
	profiles     map[string]api.BiosProfile
	nodeBindings map[string]api.NodeBiosProfile
}

func newFakeBiosDB() *fakeBiosDB {
	return &fakeBiosDB{
		profiles:     make(map[string]api.BiosProfile),
		nodeBindings: make(map[string]api.NodeBiosProfile),
	}
}

func (f *fakeBiosDB) CreateBiosProfile(_ context.Context, p api.BiosProfile) error {
	f.profiles[p.ID] = p
	return nil
}

func (f *fakeBiosDB) GetBiosProfile(_ context.Context, id string) (api.BiosProfile, error) {
	p, ok := f.profiles[id]
	if !ok {
		return api.BiosProfile{}, api.ErrNotFound
	}
	return p, nil
}

func (f *fakeBiosDB) ListBiosProfiles(_ context.Context) ([]api.BiosProfile, error) {
	out := make([]api.BiosProfile, 0, len(f.profiles))
	for _, p := range f.profiles {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeBiosDB) UpdateBiosProfile(_ context.Context, id, name, settingsJSON, description string) error {
	p, ok := f.profiles[id]
	if !ok {
		return api.ErrNotFound
	}
	if name != "" {
		p.Name = name
	}
	if settingsJSON != "" {
		p.SettingsJSON = settingsJSON
	}
	p.Description = description
	p.UpdatedAt = time.Now().UTC()
	f.profiles[id] = p
	return nil
}

func (f *fakeBiosDB) DeleteBiosProfile(_ context.Context, id string) error {
	if _, ok := f.profiles[id]; !ok {
		return api.ErrNotFound
	}
	delete(f.profiles, id)
	return nil
}

func (f *fakeBiosDB) BiosProfileRefCount(_ context.Context, id string) (int, error) {
	count := 0
	for _, b := range f.nodeBindings {
		if b.ProfileID == id {
			count++
		}
	}
	return count, nil
}

func (f *fakeBiosDB) AssignBiosProfile(_ context.Context, nodeID, profileID string) error {
	f.nodeBindings[nodeID] = api.NodeBiosProfile{
		NodeID:    nodeID,
		ProfileID: profileID,
	}
	return nil
}

func (f *fakeBiosDB) DetachBiosProfile(_ context.Context, nodeID string) error {
	delete(f.nodeBindings, nodeID)
	return nil
}

func (f *fakeBiosDB) GetNodeBiosProfile(_ context.Context, nodeID string) (api.NodeBiosProfile, error) {
	b, ok := f.nodeBindings[nodeID]
	if !ok {
		return api.NodeBiosProfile{}, api.ErrNotFound
	}
	return b, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func newBiosRouter(db BiosDBIface) http.Handler {
	h := &BiosHandler{DB: db}
	r := chi.NewRouter()
	r.Post("/bios-profiles", h.CreateProfile)
	r.Get("/bios-profiles", h.ListProfiles)
	r.Get("/bios-profiles/{id}", h.GetProfile)
	r.Put("/bios-profiles/{id}", h.UpdateProfile)
	r.Delete("/bios-profiles/{id}", h.DeleteProfile)
	r.Put("/nodes/{id}/bios-profile", h.AssignProfile)
	r.Delete("/nodes/{id}/bios-profile", h.DetachProfile)
	r.Get("/nodes/{id}/bios-profile", h.GetNodeProfile)
	r.Get("/bios/providers/{vendor}/verify", h.VerifyProvider)
	return r
}

func mustJSONBody(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	return bytes.NewBuffer(b)
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestBiosCreateProfile(t *testing.T) {
	db := newFakeBiosDB()
	router := newBiosRouter(db)

	body := mustJSONBody(t, api.CreateBiosProfileRequest{
		Name:         "hpc-default",
		Vendor:       "intel",
		SettingsJSON: `{"Intel(R) Hyper-Threading Technology":"Disable"}`,
		Description:  "disables HT for MPI workloads",
	})
	req := httptest.NewRequest(http.MethodPost, "/bios-profiles", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp api.BiosProfileResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Profile.Name != "hpc-default" {
		t.Errorf("name: got %q, want %q", resp.Profile.Name, "hpc-default")
	}
	if resp.Profile.Vendor != "intel" {
		t.Errorf("vendor: got %q, want %q", resp.Profile.Vendor, "intel")
	}
	if resp.Profile.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestBiosCreateProfileValidation(t *testing.T) {
	db := newFakeBiosDB()
	router := newBiosRouter(db)

	tests := []struct {
		name       string
		body       any
		wantStatus int
	}{
		{
			name:       "missing name",
			body:       api.CreateBiosProfileRequest{Vendor: "intel", SettingsJSON: `{}`},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing vendor",
			body:       api.CreateBiosProfileRequest{Name: "test", SettingsJSON: `{}`},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown vendor",
			body:       api.CreateBiosProfileRequest{Name: "test", Vendor: "amiga", SettingsJSON: `{}`},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid settings_json",
			body:       api.CreateBiosProfileRequest{Name: "test", Vendor: "intel", SettingsJSON: `not-json`},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/bios-profiles", mustJSONBody(t, tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d: %s", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestBiosListProfiles(t *testing.T) {
	db := newFakeBiosDB()
	now := time.Now().UTC()
	db.profiles["id1"] = api.BiosProfile{ID: "id1", Name: "a", Vendor: "intel", SettingsJSON: "{}", CreatedAt: now, UpdatedAt: now}
	db.profiles["id2"] = api.BiosProfile{ID: "id2", Name: "b", Vendor: "intel", SettingsJSON: "{}", CreatedAt: now, UpdatedAt: now}
	router := newBiosRouter(db)

	req := httptest.NewRequest(http.MethodGet, "/bios-profiles", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp api.ListBiosProfilesResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("total: got %d, want 2", resp.Total)
	}
}

func TestBiosGetProfileNotFound(t *testing.T) {
	db := newFakeBiosDB()
	router := newBiosRouter(db)

	req := httptest.NewRequest(http.MethodGet, "/bios-profiles/nonexistent", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestBiosDeleteProfileInUse(t *testing.T) {
	db := newFakeBiosDB()
	profileID := uuid.NewString()
	now := time.Now().UTC()
	db.profiles[profileID] = api.BiosProfile{ID: profileID, Name: "p", Vendor: "intel", SettingsJSON: "{}", CreatedAt: now, UpdatedAt: now}
	db.nodeBindings["node1"] = api.NodeBiosProfile{NodeID: "node1", ProfileID: profileID}
	router := newBiosRouter(db)

	req := httptest.NewRequest(http.MethodDelete, "/bios-profiles/"+profileID, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestBiosAssignAndDetachProfile(t *testing.T) {
	db := newFakeBiosDB()
	profileID := uuid.NewString()
	now := time.Now().UTC()
	db.profiles[profileID] = api.BiosProfile{ID: profileID, Name: "p", Vendor: "intel", SettingsJSON: "{}", CreatedAt: now, UpdatedAt: now}
	router := newBiosRouter(db)

	// Assign
	body := mustJSONBody(t, api.AssignBiosProfileRequest{ProfileID: profileID})
	req := httptest.NewRequest(http.MethodPut, "/nodes/node1/bios-profile", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("assign: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Get binding
	req2 := httptest.NewRequest(http.MethodGet, "/nodes/node1/bios-profile", nil)
	rr2 := httptest.NewRecorder()
	router.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("get binding: expected 200, got %d", rr2.Code)
	}

	// Detach
	req3 := httptest.NewRequest(http.MethodDelete, "/nodes/node1/bios-profile", nil)
	rr3 := httptest.NewRecorder()
	router.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusNoContent {
		t.Fatalf("detach: expected 204, got %d", rr3.Code)
	}

	// Get binding after detach → 404
	req4 := httptest.NewRequest(http.MethodGet, "/nodes/node1/bios-profile", nil)
	rr4 := httptest.NewRecorder()
	router.ServeHTTP(rr4, req4)
	if rr4.Code != http.StatusNotFound {
		t.Errorf("after detach: expected 404, got %d", rr4.Code)
	}
}

func TestBiosVerifyProvider(t *testing.T) {
	db := newFakeBiosDB()
	router := newBiosRouter(db)

	req := httptest.NewRequest(http.MethodGet, "/bios/providers/intel/verify", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.BiosProviderVerifyResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Vendor != "intel" {
		t.Errorf("vendor: got %q, want intel", resp.Vendor)
	}
	// Binary is absent in tests — available should be false.
	if resp.Available {
		t.Error("expected available=false when binary absent")
	}
}

func TestBiosVerifyProviderUnknown(t *testing.T) {
	db := newFakeBiosDB()
	router := newBiosRouter(db)

	req := httptest.NewRequest(http.MethodGet, "/bios/providers/amiga/verify", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown vendor, got %d", rr.Code)
	}
}
