package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/alerts"
)

// fakeAlertsStore implements AlertsStore for testing.
type fakeAlertsStore struct {
	active []alerts.Alert
	recent []alerts.Alert
	// filtered returns active + recent combined for QueryFiltered.
	all []alerts.Alert
}

func (f *fakeAlertsStore) QueryActive(ctx context.Context) ([]alerts.Alert, error) {
	return f.active, nil
}

func (f *fakeAlertsStore) QueryRecent(ctx context.Context) ([]alerts.Alert, error) {
	return f.recent, nil
}

func (f *fakeAlertsStore) QueryFiltered(ctx context.Context, severities []string, nodeID, ruleName, state string) ([]alerts.Alert, error) {
	var out []alerts.Alert
	for _, a := range f.all {
		if state != "" && a.State != state {
			continue
		}
		if nodeID != "" && a.NodeID != nodeID {
			continue
		}
		if ruleName != "" && a.RuleName != ruleName {
			continue
		}
		if len(severities) > 0 {
			found := false
			for _, s := range severities {
				if a.Severity == s {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, a)
	}
	return out, nil
}

func TestAlertsHandleList_Default(t *testing.T) {
	now := time.Now()
	store := &fakeAlertsStore{
		active: []alerts.Alert{
			{ID: 1, RuleName: "disk-percent", NodeID: "node-1", Sensor: "used_pct",
				Severity: "warn", State: "firing", FiredAt: now, LastValue: 92,
				ThresholdOp: ">=", ThresholdVal: 90},
		},
		recent: []alerts.Alert{
			{ID: 2, RuleName: "disk-percent", NodeID: "node-2", Sensor: "used_pct",
				Severity: "warn", State: "resolved", FiredAt: now.Add(-1 * time.Hour), LastValue: 91,
				ThresholdOp: ">=", ThresholdVal: 90},
		},
	}
	h := &AlertsHandler{Store: store}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
	w := httptest.NewRecorder()
	h.HandleList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Active []alerts.Alert `json:"active"`
		Recent []alerts.Alert `json:"recent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Active) != 1 {
		t.Errorf("active len = %d, want 1", len(resp.Active))
	}
	if len(resp.Recent) != 1 {
		t.Errorf("recent len = %d, want 1", len(resp.Recent))
	}
}

func TestAlertsHandleList_FilterBySeverity(t *testing.T) {
	now := time.Now()
	store := &fakeAlertsStore{
		all: []alerts.Alert{
			{ID: 1, RuleName: "r1", NodeID: "n1", Sensor: "s", Severity: "warn",
				State: "firing", FiredAt: now, LastValue: 1, ThresholdOp: ">=", ThresholdVal: 0},
			{ID: 2, RuleName: "r2", NodeID: "n2", Sensor: "s", Severity: "critical",
				State: "firing", FiredAt: now, LastValue: 1, ThresholdOp: ">=", ThresholdVal: 0},
		},
	}
	h := &AlertsHandler{Store: store}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts?severity=warn", nil)
	w := httptest.NewRecorder()
	h.HandleList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Active []alerts.Alert `json:"active"`
		Recent []alerts.Alert `json:"recent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Active) != 1 {
		t.Errorf("active len = %d, want 1", len(resp.Active))
	}
	if resp.Active[0].Severity != "warn" {
		t.Errorf("severity = %q, want %q", resp.Active[0].Severity, "warn")
	}
}

func TestAlertsHandleList_FilterByNode(t *testing.T) {
	now := time.Now()
	store := &fakeAlertsStore{
		all: []alerts.Alert{
			{ID: 1, RuleName: "r1", NodeID: "node-A", Sensor: "s", Severity: "warn",
				State: "firing", FiredAt: now, LastValue: 1, ThresholdOp: ">=", ThresholdVal: 0},
			{ID: 2, RuleName: "r1", NodeID: "node-B", Sensor: "s", Severity: "warn",
				State: "firing", FiredAt: now, LastValue: 1, ThresholdOp: ">=", ThresholdVal: 0},
		},
	}
	h := &AlertsHandler{Store: store}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts?node=node-A", nil)
	w := httptest.NewRecorder()
	h.HandleList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Active []alerts.Alert `json:"active"`
		Recent []alerts.Alert `json:"recent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	total := len(resp.Active) + len(resp.Recent)
	if total != 1 {
		t.Errorf("total results = %d, want 1", total)
	}
}

func TestAlertsHandleList_EmptyStoreReturnsArrays(t *testing.T) {
	store := &fakeAlertsStore{}
	h := &AlertsHandler{Store: store}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
	w := httptest.NewRecorder()
	h.HandleList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Active []alerts.Alert `json:"active"`
		Recent []alerts.Alert `json:"recent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Active == nil {
		t.Error("active should be [] not null")
	}
	if resp.Recent == nil {
		t.Error("recent should be [] not null")
	}
}
