package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
)

// fakeExternalStatsDB implements ExternalStatsDBIface against an
// in-memory slice. The "now" parameter is honoured so the handler's
// freshness logic is exercised end-to-end.
type fakeExternalStatsDB struct {
	rows []db.NodeExternalStatRow
	err  error
	last struct {
		nodeID string
		now    time.Time
	}
}

func (f *fakeExternalStatsDB) ListExternalStatsForNode(ctx context.Context, nodeID string, now time.Time) ([]db.NodeExternalStatRow, error) {
	f.last.nodeID = nodeID
	f.last.now = now
	if f.err != nil {
		return nil, f.err
	}
	// Mimic the DB layer: filter rows whose expires_at is at or before now.
	var out []db.NodeExternalStatRow
	for _, r := range f.rows {
		if r.NodeID != nodeID {
			continue
		}
		if !r.ExpiresAt.After(now) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// newRouterForExternalStats wires the handler under chi so chi.URLParam
// resolves correctly inside the handler.
func newRouterForExternalStats(h *ExternalStatsHandler) *chi.Mux {
	r := chi.NewMux()
	r.Get("/api/v1/nodes/{id}/external_stats", h.Get)
	return r
}

func TestExternalStatsHandler_AllSourcesPresent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	expires := now.Add(60 * time.Minute)
	older := now.Add(-30 * time.Second)

	rows := []db.NodeExternalStatRow{
		{
			NodeID:     "n1",
			Source:     db.ExternalSourceProbe,
			Payload:    json.RawMessage(`{"ping":true,"ssh":false,"ipmi_mc":true,"checked_at":"2026-05-09T11:59:50Z"}`),
			LastSeenAt: older,
			ExpiresAt:  expires,
		},
		{
			NodeID:     "n1",
			Source:     db.ExternalSourceBMC,
			Payload:    json.RawMessage(`{"sensors":{"CPU Temp":{"value":"42","unit":"C"}}}`),
			LastSeenAt: now.Add(-10 * time.Second),
			ExpiresAt:  expires,
		},
		{
			NodeID:     "n1",
			Source:     db.ExternalSourceSNMP,
			Payload:    json.RawMessage(`{"samples":{"1.3.6.1.2.1.1.3.0":{"value":"123456","type":"ticks"}}}`),
			LastSeenAt: now.Add(-5 * time.Second),
			ExpiresAt:  expires.Add(5 * time.Minute), // later — earliest should still be `expires`
		},
	}
	h := &ExternalStatsHandler{
		DB:  &fakeExternalStatsDB{rows: rows},
		Now: func() time.Time { return now },
	}
	router := newRouterForExternalStats(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/n1/external_stats", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if got["probes"] == nil {
		t.Fatalf("probes: nil; body=%s", w.Body.String())
	}
	probes := got["probes"].(map[string]any)
	if probes["ping"] != true {
		t.Fatalf("probes.ping: %v, want true", probes["ping"])
	}
	if probes["ipmi_mc"] != true {
		t.Fatalf("probes.ipmi_mc: %v, want true", probes["ipmi_mc"])
	}
	samples := got["samples"].(map[string]any)
	if samples["bmc"] == nil {
		t.Fatal("samples.bmc: nil")
	}
	if samples["snmp"] == nil {
		t.Fatal("samples.snmp: nil")
	}
	if samples["ipmi"] != nil {
		t.Fatalf("samples.ipmi: should be nil (no ipmi row), got %v", samples["ipmi"])
	}
	if got["last_seen"] == nil {
		t.Fatal("last_seen: nil")
	}
	if got["expires_at"] == nil {
		t.Fatal("expires_at: nil")
	}
	// last_seen must be the newest of the three (snmp at now-5s).
	wantLastSeen := now.Add(-5 * time.Second).UTC().Format(time.RFC3339Nano)
	if !strings.HasPrefix(got["last_seen"].(string), wantLastSeen[:19]) {
		t.Fatalf("last_seen: got %v, want prefix %v", got["last_seen"], wantLastSeen[:19])
	}
}

func TestExternalStatsHandler_EmptyState(t *testing.T) {
	t.Parallel()
	h := &ExternalStatsHandler{
		DB:  &fakeExternalStatsDB{},
		Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	router := newRouterForExternalStats(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/never-polled/external_stats", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, want 200", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["probes"] != nil {
		t.Fatalf("probes: %v, want nil for never-polled node", got["probes"])
	}
	if got["last_seen"] != nil {
		t.Fatalf("last_seen: %v, want nil", got["last_seen"])
	}
	if got["expires_at"] != nil {
		t.Fatalf("expires_at: %v, want nil", got["expires_at"])
	}
}

func TestExternalStatsHandler_DBError(t *testing.T) {
	t.Parallel()
	h := &ExternalStatsHandler{
		DB:  &fakeExternalStatsDB{err: errors.New("disk full")},
		Now: func() time.Time { return time.Now() },
	}
	router := newRouterForExternalStats(h)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/n1/external_stats", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}
}

func TestExternalStatsHandler_FiltersExpiredRows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	rows := []db.NodeExternalStatRow{
		// expired one second ago.
		{
			NodeID:     "n1",
			Source:     db.ExternalSourceProbe,
			Payload:    json.RawMessage(`{}`),
			LastSeenAt: now.Add(-time.Hour),
			ExpiresAt:  now.Add(-time.Second),
		},
		// fresh.
		{
			NodeID:     "n1",
			Source:     db.ExternalSourceBMC,
			Payload:    json.RawMessage(`{"sensors":{}}`),
			LastSeenAt: now.Add(-time.Minute),
			ExpiresAt:  now.Add(time.Hour),
		},
	}
	h := &ExternalStatsHandler{
		DB:  &fakeExternalStatsDB{rows: rows},
		Now: func() time.Time { return now },
	}
	router := newRouterForExternalStats(h)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/n1/external_stats", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["probes"] != nil {
		t.Fatalf("expired probe row should be filtered; got %v", got["probes"])
	}
	samples := got["samples"].(map[string]any)
	if samples["bmc"] == nil {
		t.Fatal("fresh bmc row was filtered")
	}
}

func TestBuildExternalStatsResponse_UnknownSourceDropped(t *testing.T) {
	t.Parallel()
	rows := []db.NodeExternalStatRow{
		{
			NodeID:     "n1",
			Source:     db.ExternalStatsSource("future_source_for_bundle_b"),
			Payload:    json.RawMessage(`{"a":1}`),
			LastSeenAt: time.Now(),
			ExpiresAt:  time.Now().Add(time.Hour),
		},
	}
	resp := buildExternalStatsResponse(rows)
	if resp.Probes != nil {
		t.Fatal("probes: should be nil when no probe row present")
	}
	if resp.Samples.BMC != nil || resp.Samples.SNMP != nil || resp.Samples.IPMI != nil {
		t.Fatal("samples: should all be nil for unknown source")
	}
	// Codex post-ship review issue #11: the envelope timestamps must
	// also be nil — an unknown-source row used to update last_seen /
	// expires_at before being silently dropped, so the UI's freshness
	// indicator reflected data the client couldn't actually surface.
	if resp.LastSeen != nil {
		t.Errorf("LastSeen contributed by unknown source: %v", resp.LastSeen)
	}
	if resp.ExpiresAt != nil {
		t.Errorf("ExpiresAt contributed by unknown source: %v", resp.ExpiresAt)
	}
}

// TestBuildExternalStatsResponse_UnknownSourceMixedWithKnown verifies
// the partial-recognition case: a known source contributes to the
// envelope, an unknown source must NOT.
func TestBuildExternalStatsResponse_UnknownSourceMixedWithKnown(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	rows := []db.NodeExternalStatRow{
		{
			NodeID:     "n1",
			Source:     db.ExternalSourceBMC,
			Payload:    json.RawMessage(`{"sensors":{}}`),
			LastSeenAt: now.Add(-time.Minute),
			ExpiresAt:  now.Add(time.Hour),
		},
		{
			NodeID: "n1",
			Source: db.ExternalStatsSource("future_source_for_bundle_b"),
			// LastSeenAt is more recent than the BMC row — without
			// the fix, the envelope's last_seen would have pinned to
			// this stale unknown timestamp.
			Payload:    json.RawMessage(`{"a":1}`),
			LastSeenAt: now.Add(-1 * time.Second),
			// ExpiresAt is earlier than the BMC row — without the
			// fix, the envelope's expires_at would have pinned here.
			ExpiresAt: now.Add(5 * time.Minute),
		},
	}
	resp := buildExternalStatsResponse(rows)

	if resp.Samples.BMC == nil {
		t.Fatal("BMC sample dropped")
	}
	if resp.LastSeen == nil || !resp.LastSeen.Equal(now.Add(-time.Minute)) {
		t.Errorf("LastSeen = %v, want BMC row's last_seen (%v)", resp.LastSeen, now.Add(-time.Minute))
	}
	if resp.ExpiresAt == nil || !resp.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("ExpiresAt = %v, want BMC row's expires_at (%v)", resp.ExpiresAt, now.Add(time.Hour))
	}
}
