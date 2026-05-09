package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// fakeInterfacesDB is a light in-memory store satisfying InterfacesDB.
type fakeInterfacesDB struct {
	mu    sync.Mutex
	nodes map[string]api.NodeConfig
}

func (f *fakeInterfacesDB) GetNodeConfig(_ context.Context, id string) (api.NodeConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, ok := f.nodes[id]
	if !ok {
		return api.NodeConfig{}, api.ErrNotFound
	}
	return cfg, nil
}

func (f *fakeInterfacesDB) UpdateNodeConfig(_ context.Context, cfg api.NodeConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.nodes[cfg.ID]; !ok {
		return api.ErrNotFound
	}
	f.nodes[cfg.ID] = cfg
	return nil
}

func newInterfacesRouter(db *fakeInterfacesDB) *chi.Mux {
	h := &InterfacesHandler{DB: db}
	r := chi.NewRouter()
	r.Get("/api/v1/nodes/{id}/interfaces", h.Get)
	r.Put("/api/v1/nodes/{id}/interfaces", h.Put)
	return r
}

func TestInterfaces_GetFlattensThreeShapes(t *testing.T) {
	db := &fakeInterfacesDB{nodes: map[string]api.NodeConfig{
		"n1": {
			ID:         "n1",
			PrimaryMAC: "aa:bb:cc:dd:ee:01",
			Interfaces: []api.InterfaceConfig{
				{Name: "eth0", MACAddress: "aa:bb:cc:dd:ee:01", IPAddress: "10.0.0.5/24"},
				{Name: "eth1", MACAddress: "aa:bb:cc:dd:ee:02"},
			},
			IBConfig: []api.IBInterfaceConfig{
				{DeviceName: "ib0", IPAddress: "10.99.0.5/24"},
			},
			BMC: &api.BMCNodeConfig{IPAddress: "192.168.10.50", Username: "ADMIN"},
		},
	}}
	r := newInterfacesRouter(db)

	req := httptest.NewRequest("GET", "/api/v1/nodes/n1/interfaces", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp InterfacesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.NodeID != "n1" {
		t.Errorf("node_id = %q, want n1", resp.NodeID)
	}
	if got, want := len(resp.Interfaces), 4; got != want {
		t.Fatalf("interfaces len = %d, want %d (2 eth + 1 ib + 1 bmc): %+v", got, want, resp.Interfaces)
	}
	if resp.Interfaces[0].Kind != "ethernet" || resp.Interfaces[0].Name != "eth0" {
		t.Errorf("[0] = %+v", resp.Interfaces[0])
	}
	if resp.Interfaces[2].Kind != "fabric" || resp.Interfaces[2].Name != "ib0" {
		t.Errorf("[2] = %+v", resp.Interfaces[2])
	}
	if resp.Interfaces[3].Kind != "ipmi" || resp.Interfaces[3].IP != "192.168.10.50" {
		t.Errorf("[3] = %+v", resp.Interfaces[3])
	}
	// pass field must never leak
	if resp.Interfaces[3].Pass != "" {
		t.Errorf("ipmi.pass leaked in GET: %q", resp.Interfaces[3].Pass)
	}
}

func TestInterfaces_PutWellFormedAccepted(t *testing.T) {
	db := &fakeInterfacesDB{nodes: map[string]api.NodeConfig{
		"n1": {ID: "n1"},
	}}
	r := newInterfacesRouter(db)

	body, _ := json.Marshal(InterfacesRequest{
		Mode: "replace",
		Interfaces: []TypedInterface{
			{Kind: "ethernet", Name: "eth0", MAC: "AA:BB:CC:DD:EE:01", IP: "10.0.0.5/24"},
			{Kind: "ethernet", Name: "eth1", MAC: "aa:bb:cc:dd:ee:02", VLAN: "100"},
			{Kind: "fabric", Name: "ib0", GUID: "0001:0002:0003:0004", IP: "10.99.0.5/24"},
			{Kind: "ipmi", Name: "ipmi0", IP: "192.168.10.50", Channel: "1", User: "admin", Pass: "secret"},
		},
	})
	req := httptest.NewRequest("PUT", "/api/v1/nodes/n1/interfaces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	cfg := db.nodes["n1"]
	if got, want := len(cfg.Interfaces), 2; got != want {
		t.Errorf("eth count = %d, want %d", got, want)
	}
	if cfg.Interfaces[0].MACAddress != "aa:bb:cc:dd:ee:01" {
		t.Errorf("MAC not lowercased: %q", cfg.Interfaces[0].MACAddress)
	}
	if cfg.PrimaryMAC != "aa:bb:cc:dd:ee:01" {
		t.Errorf("PrimaryMAC sync = %q", cfg.PrimaryMAC)
	}
	if got, want := len(cfg.IBConfig), 1; got != want {
		t.Errorf("ib count = %d, want %d", got, want)
	}
	if cfg.BMC == nil || cfg.BMC.IPAddress != "192.168.10.50" {
		t.Errorf("BMC = %+v", cfg.BMC)
	}
	if cfg.BMC.Password != "secret" {
		t.Errorf("BMC password not stored")
	}
}

func TestInterfaces_PutMalformedRejected(t *testing.T) {
	db := &fakeInterfacesDB{nodes: map[string]api.NodeConfig{
		"n1": {ID: "n1"},
	}}
	r := newInterfacesRouter(db)

	cases := []struct {
		name      string
		rows      []TypedInterface
		fieldKey  string
		wantSubstr string
	}{
		{
			name:       "ethernet missing mac",
			rows:       []TypedInterface{{Kind: "ethernet", Name: "eth0"}},
			fieldKey:   "0.mac",
			wantSubstr: "MAC",
		},
		{
			name:       "ethernet bad mac",
			rows:       []TypedInterface{{Kind: "ethernet", Name: "eth0", MAC: "not-a-mac"}},
			fieldKey:   "0.mac",
			wantSubstr: "Invalid MAC",
		},
		{
			name:       "ethernet bad vlan",
			rows:       []TypedInterface{{Kind: "ethernet", Name: "eth0", MAC: "aa:bb:cc:dd:ee:01", VLAN: "9999"}},
			fieldKey:   "0.vlan",
			wantSubstr: "VLAN",
		},
		{
			name:       "fabric missing guid",
			rows:       []TypedInterface{{Kind: "fabric", Name: "ib0"}},
			fieldKey:   "0.guid",
			wantSubstr: "GUID",
		},
		{
			name:       "ipmi bad channel",
			rows:       []TypedInterface{{Kind: "ipmi", Name: "ipmi0", IP: "10.0.0.1", Channel: "999", User: "admin"}},
			fieldKey:   "0.channel",
			wantSubstr: "Channel",
		},
		{
			name:       "unknown kind",
			rows:       []TypedInterface{{Kind: "wifi", Name: "wlan0"}},
			fieldKey:   "0.kind",
			wantSubstr: "unknown kind",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(InterfacesRequest{Interfaces: tc.rows})
			req := httptest.NewRequest("PUT", "/api/v1/nodes/n1/interfaces", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
			var resp struct {
				Error  string            `json:"error"`
				Code   string            `json:"code"`
				Fields map[string]string `json:"fields"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Code != "validation_error" {
				t.Errorf("code = %q, want validation_error", resp.Code)
			}
			msg, ok := resp.Fields[tc.fieldKey]
			if !ok {
				t.Fatalf("missing field error %q in %+v", tc.fieldKey, resp.Fields)
			}
			if !strings.Contains(msg, tc.wantSubstr) {
				t.Errorf("field %q message = %q, want contains %q", tc.fieldKey, msg, tc.wantSubstr)
			}
		})
	}
}

func TestInterfaces_PutMergeMode(t *testing.T) {
	db := &fakeInterfacesDB{nodes: map[string]api.NodeConfig{
		"n1": {
			ID: "n1",
			Interfaces: []api.InterfaceConfig{
				{Name: "eth0", MACAddress: "aa:bb:cc:dd:ee:01"},
			},
			BMC: &api.BMCNodeConfig{IPAddress: "192.168.10.50", Username: "admin"},
		},
	}}
	r := newInterfacesRouter(db)

	// Merge: only fabric posted — eth + bmc must be preserved.
	body, _ := json.Marshal(InterfacesRequest{
		Mode: "merge",
		Interfaces: []TypedInterface{
			{Kind: "fabric", Name: "ib0", GUID: "0001:0002:0003:0004"},
		},
	})
	req := httptest.NewRequest("PUT", "/api/v1/nodes/n1/interfaces", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	cfg := db.nodes["n1"]
	if len(cfg.Interfaces) != 1 || cfg.Interfaces[0].Name != "eth0" {
		t.Errorf("ethernet wiped in merge mode: %+v", cfg.Interfaces)
	}
	if cfg.BMC == nil {
		t.Errorf("BMC wiped in merge mode")
	}
	if len(cfg.IBConfig) != 1 {
		t.Errorf("fabric not added: %+v", cfg.IBConfig)
	}
}

func TestInterfaces_PutMultipleIPMIRejected(t *testing.T) {
	db := &fakeInterfacesDB{nodes: map[string]api.NodeConfig{
		"n1": {ID: "n1"},
	}}
	r := newInterfacesRouter(db)

	body, _ := json.Marshal(InterfacesRequest{
		Interfaces: []TypedInterface{
			{Kind: "ipmi", Name: "ipmi0", IP: "10.0.0.1", Channel: "1", User: "admin"},
			{Kind: "ipmi", Name: "ipmi1", IP: "10.0.0.2", Channel: "1", User: "admin"},
		},
	})
	req := httptest.NewRequest("PUT", "/api/v1/nodes/n1/interfaces", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}
