package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
)

// ─── fake DB ──────────────────────────────────────────────────────────────────

type fakeChangesDB struct {
	mu      sync.Mutex
	changes map[string]db.PendingChange
	flags   map[string]bool
}

func newFakeChangesDB() *fakeChangesDB {
	return &fakeChangesDB{
		changes: make(map[string]db.PendingChange),
		flags:   make(map[string]bool),
	}
}

func (f *fakeChangesDB) PendingChangesInsert(_ context.Context, c db.PendingChange) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes[c.ID] = c
	return nil
}

func (f *fakeChangesDB) PendingChangesList(_ context.Context, kind string) ([]db.PendingChange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []db.PendingChange
	for _, c := range f.changes {
		if kind == "" || c.Kind == kind {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeChangesDB) PendingChangesGet(_ context.Context, id string) (db.PendingChange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.changes[id]
	if !ok {
		return db.PendingChange{}, fmt.Errorf("not found: %s", id)
	}
	return c, nil
}

func (f *fakeChangesDB) PendingChangesDelete(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		delete(f.changes, id)
	}
	return nil
}

func (f *fakeChangesDB) PendingChangesDeleteAll(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes = make(map[string]db.PendingChange)
	return nil
}

func (f *fakeChangesDB) PendingChangesCount(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.changes), nil
}

func (f *fakeChangesDB) StageModeGet(_ context.Context, surface string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flags[surface], nil
}

func (f *fakeChangesDB) StageModeSet(_ context.Context, surface string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flags[surface] = enabled
	return nil
}

func (f *fakeChangesDB) StageModeGetAll(_ context.Context) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]bool, len(f.flags))
	for k, v := range f.flags {
		out[k] = v
	}
	return out, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func newTestChangesHandler() (*ChangesHandler, *fakeChangesDB) {
	fdb := newFakeChangesDB()
	applied := make(map[string]bool)
	h := &ChangesHandler{
		DB: fdb,
		CommitFns: map[string]ChangesCommitFn{
			"ldap_user": func(_ context.Context, c db.PendingChange) error {
				applied[c.ID] = true
				return nil
			},
			"sudoers_rule": func(_ context.Context, c db.PendingChange) error {
				applied[c.ID] = true
				return nil
			},
			"node_network": func(_ context.Context, c db.PendingChange) error {
				applied[c.ID] = true
				return nil
			},
		},
	}
	return h, fdb
}

func postJSON(t *testing.T, h http.Handler, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatalf("postJSON: encode: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func getReq(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// ─── Stage ────────────────────────────────────────────────────────────────────

// TestChanges_StageMissingKind verifies that staging without a kind returns 400.
func TestChanges_StageMissingKind(t *testing.T) {
	h, _ := newTestChangesHandler()
	w := postJSON(t, http.HandlerFunc(h.HandleStage), "/api/v1/changes", map[string]interface{}{
		"target":  "user1",
		"payload": map[string]string{"uid": "user1"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

// TestChanges_Stage verifies that a valid stage request creates a pending change.
func TestChanges_Stage(t *testing.T) {
	h, fdb := newTestChangesHandler()
	w := postJSON(t, http.HandlerFunc(h.HandleStage), "/api/v1/changes", map[string]interface{}{
		"kind":    "ldap_user",
		"target":  "testuser",
		"payload": map[string]string{"uid": "testuser", "cn": "Test User"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp db.PendingChange
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID == "" {
		t.Error("response missing id")
	}
	if resp.Kind != "ldap_user" {
		t.Errorf("want kind=ldap_user, got %q", resp.Kind)
	}

	// Confirm the row is in the fake DB.
	fdb.mu.Lock()
	defer fdb.mu.Unlock()
	if len(fdb.changes) != 1 {
		t.Errorf("want 1 row in DB, got %d", len(fdb.changes))
	}
}

// ─── List ─────────────────────────────────────────────────────────────────────

// TestChanges_ListEmpty verifies that listing returns an empty slice when no changes are pending.
func TestChanges_ListEmpty(t *testing.T) {
	h, _ := newTestChangesHandler()
	w := getReq(t, http.HandlerFunc(h.HandleList), "/api/v1/changes")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	total := resp["total"].(float64)
	if total != 0 {
		t.Errorf("want total=0, got %v", total)
	}
}

// ─── Commit ───────────────────────────────────────────────────────────────────

// TestChanges_StageListCommitClear exercises the full stage → list → commit → list-empty lifecycle.
func TestChanges_StageListCommitClear(t *testing.T) {
	h, fdb := newTestChangesHandler()

	// Stage two changes.
	for _, uid := range []string{"alice", "bob"} {
		w := postJSON(t, http.HandlerFunc(h.HandleStage), "/api/v1/changes", map[string]interface{}{
			"kind":    "ldap_user",
			"target":  uid,
			"payload": map[string]string{"uid": uid},
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("stage %s: want 201, got %d", uid, w.Code)
		}
	}

	// List — should return 2.
	{
		w := getReq(t, http.HandlerFunc(h.HandleList), "/api/v1/changes")
		var resp map[string]interface{}
		_ = json.NewDecoder(w.Body).Decode(&resp)
		if total := resp["total"].(float64); total != 2 {
			t.Errorf("want total=2 after stage, got %v", total)
		}
	}

	// Commit all (no body → commit all).
	{
		req := httptest.NewRequest(http.MethodPost, "/api/v1/changes/commit", bytes.NewBufferString("{}"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.HandleCommit(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("commit: want 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]interface{}
		_ = json.NewDecoder(w.Body).Decode(&resp)
		results := resp["results"].([]interface{})
		if len(results) != 2 {
			t.Errorf("want 2 results, got %d", len(results))
		}
		for _, r := range results {
			row := r.(map[string]interface{})
			if !row["success"].(bool) {
				t.Errorf("commit result not success: %v", row)
			}
		}
	}

	// After commit, pending changes should be empty.
	fdb.mu.Lock()
	remaining := len(fdb.changes)
	fdb.mu.Unlock()
	if remaining != 0 {
		t.Errorf("want 0 pending after commit, got %d", remaining)
	}
}

// TestChanges_CommitUnknownKind verifies that committing an unknown kind returns skipped result.
func TestChanges_CommitUnknownKind(t *testing.T) {
	h, _ := newTestChangesHandler()

	// Stage a change with an unknown kind.
	w := postJSON(t, http.HandlerFunc(h.HandleStage), "/api/v1/changes", map[string]interface{}{
		"kind":    "unknown_kind",
		"target":  "x",
		"payload": map[string]string{"x": "y"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("stage: want 201, got %d", w.Code)
	}

	// Commit — should return success=false with an explanatory error.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/changes/commit", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	wr := httptest.NewRecorder()
	h.HandleCommit(wr, req)
	if wr.Code != http.StatusOK {
		t.Fatalf("commit: want 200, got %d", wr.Code)
	}
	var resp map[string]interface{}
	_ = json.NewDecoder(wr.Body).Decode(&resp)
	results := resp["results"].([]interface{})
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	row := results[0].(map[string]interface{})
	if row["success"].(bool) {
		t.Error("want success=false for unknown kind")
	}
}

// ─── Clear ────────────────────────────────────────────────────────────────────

// TestChanges_Clear verifies that clearing removes all pending changes.
func TestChanges_Clear(t *testing.T) {
	h, fdb := newTestChangesHandler()

	// Stage one.
	postJSON(t, http.HandlerFunc(h.HandleStage), "/api/v1/changes", map[string]interface{}{
		"kind":    "node_network",
		"target":  "node-abc",
		"payload": map[string]string{"ip": "10.0.0.5"},
	})

	// Clear all.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/changes/clear", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleClear(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear: want 200, got %d", w.Code)
	}

	fdb.mu.Lock()
	remaining := len(fdb.changes)
	fdb.mu.Unlock()
	if remaining != 0 {
		t.Errorf("want 0 after clear, got %d", remaining)
	}
}

// ─── Mode endpoints ───────────────────────────────────────────────────────────

// TestChanges_GetMode verifies that all known surfaces are present in the mode response.
func TestChanges_GetMode(t *testing.T) {
	h, _ := newTestChangesHandler()
	w := getReq(t, http.HandlerFunc(h.HandleGetMode), "/api/v1/changes/mode")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	mode := resp["mode"].(map[string]interface{})
	for _, s := range []string{"ldap_user", "sudoers_rule", "node_network"} {
		if _, ok := mode[s]; !ok {
			t.Errorf("mode response missing surface %q", s)
		}
	}
}

// TestChanges_SetMode verifies that setting a surface mode persists.
func TestChanges_SetMode(t *testing.T) {
	h, fdb := newTestChangesHandler()

	// Build a chi router so URL params are resolved.
	router := chi.NewRouter()
	router.Put("/api/v1/changes/mode/{surface}", h.HandleSetMode)

	body := map[string]bool{"enabled": true}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/changes/mode/ldap_user", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	fdb.mu.Lock()
	enabled := fdb.flags["ldap_user"]
	fdb.mu.Unlock()
	if !enabled {
		t.Error("want ldap_user stage mode to be enabled after PUT")
	}
}

// TestChanges_SetModeUnknownSurface verifies that an unknown surface returns 400.
func TestChanges_SetModeUnknownSurface(t *testing.T) {
	h, _ := newTestChangesHandler()
	router := chi.NewRouter()
	router.Put("/api/v1/changes/mode/{surface}", h.HandleSetMode)

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]bool{"enabled": true})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/changes/mode/unknown_thing", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}
