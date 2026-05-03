package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/alerts"
)

// ─── Fakes ────────────────────────────────────────────────────────────────────

// fakeRulesEngine implements AlertRulesEngineIface for tests.
type fakeRulesEngine struct {
	rules       []*alerts.Rule
	reloadCount int
}

func (f *fakeRulesEngine) Rules() []*alerts.Rule { return f.rules }
func (f *fakeRulesEngine) Reload()               { f.reloadCount++ }

// fakeStatsDB implements AlertRulesStatsQuerier for tests.
type fakeStatsDB struct {
	// knownPairs maps "plugin/sensor" → true for known combinations.
	knownPairs map[string]bool
}

func (f *fakeStatsDB) KnownPluginSensor(_ context.Context, plugin, sensor string) (bool, error) {
	if f.knownPairs == nil {
		return false, nil
	}
	return f.knownPairs[plugin+"/"+sensor], nil
}

// ─── Tests ────────────────────────────────────────────────────────────────────

const validRuleYAML = `name: disk-percent
plugin: disks
sensor: used_pct
threshold:
  op: ">="
  value: 90
severity: warn
notify:
  webhook: false
`

// TestAlertRulesPUT_ValidYAML verifies that a valid YAML body returns 200 and
// triggers an engine reload. File write is stubbed via the handler's RuleWriter.
func TestAlertRulesPUT_ValidYAML(t *testing.T) {
	eng := &fakeRulesEngine{}
	statsDB := &fakeStatsDB{
		knownPairs: map[string]bool{"disks/used_pct": true},
	}
	h := &AlertRulesHandler{
		Engine:     eng,
		StatsDB:    statsDB,
		RuleWriter: func(_ context.Context, name string, content []byte) error { return nil },
	}

	body, _ := json.Marshal(putRuleRequest{YAML: validRuleYAML})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/alerts/rules/disk-percent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Inject chi URL params.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "disk-percent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("ok = %v, want true", resp["ok"])
	}
	if eng.reloadCount != 1 {
		t.Errorf("Reload called %d times, want 1", eng.reloadCount)
	}
}

// TestAlertRulesPUT_MalformedYAML verifies that malformed YAML returns 400.
func TestAlertRulesPUT_MalformedYAML(t *testing.T) {
	eng := &fakeRulesEngine{}
	h := &AlertRulesHandler{
		Engine:     eng,
		RuleWriter: func(_ context.Context, name string, content []byte) error { return nil },
	}

	body, _ := json.Marshal(putRuleRequest{YAML: "{ not: valid: yaml: ["})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/alerts/rules/disk-percent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "disk-percent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\nbody: %s", w.Code, w.Body.String())
	}
}

// TestAlertRulesPUT_UnknownPlugin verifies that a valid rule with an unknown
// plugin/sensor returns 422.
func TestAlertRulesPUT_UnknownPlugin(t *testing.T) {
	eng := &fakeRulesEngine{}
	statsDB := &fakeStatsDB{knownPairs: map[string]bool{}}
	h := &AlertRulesHandler{
		Engine:     eng,
		StatsDB:    statsDB,
		RuleWriter: func(_ context.Context, name string, content []byte) error { return nil },
	}

	body, _ := json.Marshal(putRuleRequest{YAML: validRuleYAML})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/alerts/rules/disk-percent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "disk-percent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\nbody: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := resp["identifier"]; !ok {
		t.Error("response should contain 'identifier' field for 422")
	}
}

// TestAlertRulesPUT_NameMismatch verifies that a rule whose name field differs
// from the URL path returns 400.
func TestAlertRulesPUT_NameMismatch(t *testing.T) {
	eng := &fakeRulesEngine{}
	h := &AlertRulesHandler{
		Engine:     eng,
		RuleWriter: func(_ context.Context, name string, content []byte) error { return nil },
	}

	// YAML says "disk-percent" but URL says "other-rule".
	body, _ := json.Marshal(putRuleRequest{YAML: validRuleYAML})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/alerts/rules/other-rule", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "other-rule")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\nbody: %s", w.Code, w.Body.String())
	}
}
