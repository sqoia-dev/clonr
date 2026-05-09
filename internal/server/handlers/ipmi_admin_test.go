package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sqoia-dev/clustr/internal/privhelper"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// stubPrivhelperOps records calls and returns canned output. Used by every
// handler test below so the dispatch shape and response decoding are
// verified without exec'ing the real privhelper binary.
type stubPrivhelperOps struct {
	powerCalls   []stubCall
	selCalls     []stubCall
	sensorsCalls []stubCall
	powerOut     string
	selOut       string
	sensorsOut   string
	err          error
}

type stubCall struct {
	Action string
	Creds  privhelper.IPMICredentials
}

func (s *stubPrivhelperOps) IPMIPower(_ context.Context, creds privhelper.IPMICredentials, action string) (string, error) {
	s.powerCalls = append(s.powerCalls, stubCall{Action: action, Creds: creds})
	return s.powerOut, s.err
}

func (s *stubPrivhelperOps) IPMISEL(_ context.Context, creds privhelper.IPMICredentials, op string) (string, error) {
	s.selCalls = append(s.selCalls, stubCall{Action: op, Creds: creds})
	return s.selOut, s.err
}

func (s *stubPrivhelperOps) IPMISensors(_ context.Context, creds privhelper.IPMICredentials) (string, error) {
	s.sensorsCalls = append(s.sensorsCalls, stubCall{Creds: creds})
	return s.sensorsOut, s.err
}

func setupAdminHandler(t *testing.T) (*chi.Mux, *stubPrivhelperOps) {
	t.Helper()
	stub := &stubPrivhelperOps{}
	h := &IPMIAdminHandler{
		DB: &fakeConsoleDB{
			nodes: map[string]api.NodeConfig{
				"node-1": {BMC: &api.BMCNodeConfig{IPAddress: "10.0.0.5", Username: "admin", Password: "p"}},
				"node-2": {},
			},
		},
		PrivhelperOps: stub,
	}
	r := chi.NewRouter()
	r.Post("/api/v1/nodes/{id}/ipmi/power/{action}", h.PowerAction)
	r.Get("/api/v1/nodes/{id}/ipmi/sel", h.GetSEL)
	r.Delete("/api/v1/nodes/{id}/ipmi/sel", h.ClearSEL)
	r.Get("/api/v1/nodes/{id}/ipmi/sensors", h.GetSensors)
	return r, stub
}

func TestIPMIAdmin_PowerStatus(t *testing.T) {
	r, stub := setupAdminHandler(t)
	stub.powerOut = "10.0.0.5: on"

	req := httptest.NewRequest("POST", "/api/v1/nodes/node-1/ipmi/power/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp IPMIPowerActionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Action != "status" || resp.Output != "10.0.0.5: on" || !resp.OK {
		t.Errorf("response mismatch: %+v", resp)
	}
	if len(stub.powerCalls) != 1 {
		t.Fatalf("expected 1 power call, got %d", len(stub.powerCalls))
	}
	if stub.powerCalls[0].Action != "status" {
		t.Errorf("action = %q, want status", stub.powerCalls[0].Action)
	}
	if stub.powerCalls[0].Creds.Host != "10.0.0.5" {
		t.Errorf("creds.host = %q, want 10.0.0.5", stub.powerCalls[0].Creds.Host)
	}
}

func TestIPMIAdmin_PowerCycle(t *testing.T) {
	r, stub := setupAdminHandler(t)
	stub.powerOut = "ok"

	req := httptest.NewRequest("POST", "/api/v1/nodes/node-1/ipmi/power/cycle", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if len(stub.powerCalls) != 1 || stub.powerCalls[0].Action != "cycle" {
		t.Errorf("expected one cycle call, got %v", stub.powerCalls)
	}
}

func TestIPMIAdmin_NodeWithoutBMC(t *testing.T) {
	r, _ := setupAdminHandler(t)

	req := httptest.NewRequest("POST", "/api/v1/nodes/node-2/ipmi/power/on", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code == 200 {
		t.Errorf("expected non-200 for node without BMC, got %d", w.Code)
	}
}

func TestIPMIAdmin_GetSEL(t *testing.T) {
	r, stub := setupAdminHandler(t)
	stub.selOut = "1,2024-01-01,12:00:00,Temperature,Threshold,Critical,Upper Critical going high\n"

	req := httptest.NewRequest("GET", "/api/v1/nodes/node-1/ipmi/sel", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp IPMISELResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(resp.Entries))
	}
	if resp.Entries[0].Severity != "critical" {
		t.Errorf("severity = %q, want critical", resp.Entries[0].Severity)
	}
}

func TestIPMIAdmin_ClearSEL(t *testing.T) {
	r, stub := setupAdminHandler(t)

	req := httptest.NewRequest("DELETE", "/api/v1/nodes/node-1/ipmi/sel", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if len(stub.selCalls) != 1 || stub.selCalls[0].Action != "clear" {
		t.Errorf("expected one clear call, got %v", stub.selCalls)
	}
}

func TestIPMIAdmin_GetSensors(t *testing.T) {
	r, stub := setupAdminHandler(t)
	stub.sensorsOut = "1,CPU0_Temp,Temperature,Nominal,42,degrees C,'OK'\n"

	req := httptest.NewRequest("GET", "/api/v1/nodes/node-1/ipmi/sensors", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp IPMISensorsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Sensors) != 1 || resp.Sensors[0].Status != "ok" {
		t.Errorf("sensors mismatch: %+v", resp.Sensors)
	}
	if len(stub.sensorsCalls) != 1 {
		t.Errorf("expected one sensors call, got %d", len(stub.sensorsCalls))
	}
}

func TestIPMIAdmin_PrivhelperFailure_BadGateway(t *testing.T) {
	r, stub := setupAdminHandler(t)
	stub.err = &stubError{msg: "BMC unreachable"}

	req := httptest.NewRequest("POST", "/api/v1/nodes/node-1/ipmi/power/on", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
	if !strings.Contains(w.Body.String(), "BMC unreachable") {
		t.Errorf("error body should mention failure: %s", w.Body.String())
	}
}

type stubError struct{ msg string }

func (e *stubError) Error() string { return e.msg }
