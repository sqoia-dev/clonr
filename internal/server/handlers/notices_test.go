package handlers_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/server/handlers"
)

// ─── In-memory stub ──────────────────────────────────────────────────────────

type noticesStub struct {
	notices []db.Notice
	nextID  int64
}

func newNoticesStub() *noticesStub { return &noticesStub{nextID: 1} }

func (s *noticesStub) InsertNotice(_ context.Context, p db.CreateNoticeParams) (db.Notice, error) {
	n := db.Notice{
		ID:        s.nextID,
		Body:      p.Body,
		Severity:  p.Severity,
		CreatedBy: p.CreatedBy,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: p.ExpiresAt,
	}
	s.nextID++
	s.notices = append(s.notices, n)
	return n, nil
}

func (s *noticesStub) GetActiveNotice(_ context.Context) (*db.Notice, error) {
	now := time.Now()
	sevOrder := map[string]int{"critical": 0, "warning": 1, "info": 2}
	var best *db.Notice
	for i := range s.notices {
		n := &s.notices[i]
		if n.DismissedAt != nil {
			continue
		}
		if n.ExpiresAt != nil && !n.ExpiresAt.After(now) {
			continue
		}
		if best == nil ||
			sevOrder[n.Severity] < sevOrder[best.Severity] ||
			(sevOrder[n.Severity] == sevOrder[best.Severity] && n.CreatedAt.After(best.CreatedAt)) {
			best = n
		}
	}
	return best, nil
}

func (s *noticesStub) DismissNotice(_ context.Context, id int64) error {
	for i := range s.notices {
		if s.notices[i].ID == id {
			t := time.Now().UTC()
			s.notices[i].DismissedAt = &t
			return nil
		}
	}
	return sql.ErrNoRows
}

func makeNoticesHandler(stub *noticesStub) *handlers.NoticesHandler {
	return &handlers.NoticesHandler{
		DB:    stub,
		Audit: nil,
		GetActorInfo: func(_ *http.Request) (string, string) {
			return "actor-1", "user:actor-1"
		},
	}
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestNoticesCreate_AllSeverities(t *testing.T) {
	t.Parallel()
	for _, sev := range []string{"info", "warning", "critical"} {
		sev := sev
		t.Run(sev, func(t *testing.T) {
			t.Parallel()
			stub := newNoticesStub()
			h := makeNoticesHandler(stub)

			body, _ := json.Marshal(map[string]string{"body": "test notice", "severity": sev})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/notices", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			h.HandleCreate(rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
			}
			var resp map[string]interface{}
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if resp["severity"] != sev {
				t.Errorf("want severity=%q, got %v", sev, resp["severity"])
			}
		})
	}
}

func TestNoticesCreate_RejectsInvalidSeverity(t *testing.T) {
	t.Parallel()
	stub := newNoticesStub()
	h := makeNoticesHandler(stub)

	body, _ := json.Marshal(map[string]string{"body": "x", "severity": "extreme"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/notices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandleCreate(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
}

func TestNoticesGetActive_ReturnsMostSevere(t *testing.T) {
	t.Parallel()
	stub := newNoticesStub()
	h := makeNoticesHandler(stub)

	// Insert info then critical.
	_, _ = stub.InsertNotice(context.Background(), db.CreateNoticeParams{Body: "info notice", Severity: "info"})
	_, _ = stub.InsertNotice(context.Background(), db.CreateNoticeParams{Body: "critical notice", Severity: "critical"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notices/active", nil)
	rec := httptest.NewRecorder()
	h.HandleGetActive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp struct {
		Notice *struct {
			Severity string `json:"severity"`
		} `json:"notice"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Notice == nil {
		t.Fatal("expected notice, got nil")
	}
	if resp.Notice.Severity != "critical" {
		t.Errorf("want critical, got %s", resp.Notice.Severity)
	}
}

func TestNoticesGetActive_ExcludesDismissed(t *testing.T) {
	t.Parallel()
	stub := newNoticesStub()
	h := makeNoticesHandler(stub)

	n, _ := stub.InsertNotice(context.Background(), db.CreateNoticeParams{Body: "gone", Severity: "info"})
	_ = stub.DismissNotice(context.Background(), n.ID)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notices/active", nil)
	rec := httptest.NewRecorder()
	h.HandleGetActive(rec, req)

	var resp struct {
		Notice *map[string]interface{} `json:"notice"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Notice != nil {
		t.Errorf("want nil notice after dismiss, got %v", resp.Notice)
	}
}

func TestNoticesGetActive_ExcludesExpired(t *testing.T) {
	t.Parallel()
	stub := newNoticesStub()
	h := makeNoticesHandler(stub)

	past := time.Now().Add(-1 * time.Hour)
	_, _ = stub.InsertNotice(context.Background(), db.CreateNoticeParams{
		Body: "expired", Severity: "warning", ExpiresAt: &past,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notices/active", nil)
	rec := httptest.NewRecorder()
	h.HandleGetActive(rec, req)

	var resp struct {
		Notice *map[string]interface{} `json:"notice"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Notice != nil {
		t.Errorf("want nil notice for expired entry, got %v", resp.Notice)
	}
}

func TestNoticesDismiss(t *testing.T) {
	t.Parallel()
	stub := newNoticesStub()
	h := makeNoticesHandler(stub)

	n, _ := stub.InsertNotice(context.Background(), db.CreateNoticeParams{Body: "hi", Severity: "info"})

	rtr := chi.NewRouter()
	rtr.Delete("/api/v1/admin/notices/{id}", h.HandleDismiss)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/notices/1", nil)
	rec := httptest.NewRecorder()
	rtr.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	// Confirm the notice is dismissed.
	active, _ := stub.GetActiveNotice(context.Background())
	if active != nil {
		t.Errorf("notice %d should be dismissed, but GetActiveNotice returned it", n.ID)
	}
}

func TestNoticesDismiss_NotFound(t *testing.T) {
	t.Parallel()
	stub := newNoticesStub()
	h := makeNoticesHandler(stub)

	rtr := chi.NewRouter()
	rtr.Delete("/api/v1/admin/notices/{id}", h.HandleDismiss)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/notices/999", nil)
	rec := httptest.NewRecorder()
	rtr.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}
