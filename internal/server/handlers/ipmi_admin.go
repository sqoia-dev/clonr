// Package handlers — ipmi_admin.go (Sprint 34 IPMI-MIN federation)
//
// Admin-scoped /api/v1/nodes/{id}/ipmi/* endpoints federated through
// clustr-serverd. These delegate to the ipmi-* privhelper verbs (which run
// freeipmi as root) so the unprivileged clustr user never holds the BMC
// password in /proc/<pid>/cmdline.
//
// Routes (registered in server.go):
//
//	POST   /api/v1/nodes/{id}/ipmi/power/{action}     {status,on,off,cycle,reset}
//	GET    /api/v1/nodes/{id}/ipmi/sel
//	DELETE /api/v1/nodes/{id}/ipmi/sel
//	GET    /api/v1/nodes/{id}/ipmi/sensors
//
// The existing /api/v1/nodes/{id}/{power,sel,sensors} routes are unchanged —
// those use the ipmitool wrapper directly. The new /ipmi/* family is the
// privhelper-federated path the spec calls for, and is the path the CLI's
// `clustr ipmi node <id> ...` subcommand hits.
package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/ipmi"
	"github.com/sqoia-dev/clustr/internal/privhelper"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// IPMIAdminHandler federates the admin /ipmi/* routes through
// clustr-privhelper.
//
// PrivhelperOps is the abstraction over the helper. Tests inject a fake;
// the production type is realPrivhelperOps which simply forwards to the
// privhelper package functions.
type IPMIAdminHandler struct {
	DB ConsoleDB

	// PrivhelperOps is the privhelper client surface — injected for tests.
	PrivhelperOps PrivhelperOps
}

// PrivhelperOps captures the privhelper functions IPMIAdminHandler uses.
// Defined as an interface so handler tests can swap in a stub without
// shelling out to the real binary.
type PrivhelperOps interface {
	IPMIPower(ctx context.Context, creds privhelper.IPMICredentials, action string) (string, error)
	IPMISEL(ctx context.Context, creds privhelper.IPMICredentials, op string) (string, error)
	IPMISensors(ctx context.Context, creds privhelper.IPMICredentials) (string, error)
}

// realPrivhelperOps is the production PrivhelperOps that delegates to the
// privhelper package's stateless functions.
type realPrivhelperOps struct{}

// NewRealPrivhelperOps returns a PrivhelperOps implementation backed by the
// privhelper package's function-style API.
func NewRealPrivhelperOps() PrivhelperOps { return realPrivhelperOps{} }

func (realPrivhelperOps) IPMIPower(ctx context.Context, creds privhelper.IPMICredentials, action string) (string, error) {
	return privhelper.IPMIPower(ctx, creds, action)
}
func (realPrivhelperOps) IPMISEL(ctx context.Context, creds privhelper.IPMICredentials, op string) (string, error) {
	return privhelper.IPMISEL(ctx, creds, op)
}
func (realPrivhelperOps) IPMISensors(ctx context.Context, creds privhelper.IPMICredentials) (string, error) {
	return privhelper.IPMISensors(ctx, creds)
}

// IPMIPowerActionResponse is the shape returned by POST
// /ipmi/power/{action}.
type IPMIPowerActionResponse struct {
	NodeID string `json:"node_id"`
	Action string `json:"action"`
	Output string `json:"output"`
	OK     bool   `json:"ok"`
}

// IPMISensorsResponse mirrors the existing SensorsResponse shape but is
// produced by parsing the freeipmi comma-separated output.
type IPMISensorsResponse struct {
	NodeID      string        `json:"node_id"`
	Sensors     []ipmi.Sensor `json:"sensors"`
	LastChecked time.Time     `json:"last_checked"`
}

// IPMISELResponse mirrors the existing SELResponse shape but produced from
// the freeipmi parser.
type IPMISELResponse struct {
	NodeID      string          `json:"node_id"`
	Entries     []ipmi.SELEntry `json:"entries"`
	LastChecked time.Time       `json:"last_checked"`
}

// PowerAction handles POST /api/v1/nodes/{id}/ipmi/power/{action}.
func (h *IPMIAdminHandler) PowerAction(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	action := chi.URLParam(r, "action")

	creds, err := h.resolveCreds(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}

	out, err := h.ops().IPMIPower(r.Context(), creds, action)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Str("action", action).Msg("ipmi-power failed")
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: fmt.Sprintf("ipmi power %s failed: %v", action, err),
			Code:  "ipmi_error",
		})
		return
	}
	writeJSON(w, http.StatusOK, IPMIPowerActionResponse{
		NodeID: nodeID,
		Action: action,
		Output: out,
		OK:     true,
	})
}

// GetSEL handles GET /api/v1/nodes/{id}/ipmi/sel.
func (h *IPMIAdminHandler) GetSEL(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	creds, err := h.resolveCreds(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := h.ops().IPMISEL(r.Context(), creds, "list")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: fmt.Sprintf("ipmi-sel list failed: %v", err),
			Code:  "ipmi_error",
		})
		return
	}
	entries := parseFreeIPMISELForResponse(out)
	writeJSON(w, http.StatusOK, IPMISELResponse{
		NodeID:      nodeID,
		Entries:     entries,
		LastChecked: time.Now().UTC(),
	})
}

// ClearSEL handles DELETE /api/v1/nodes/{id}/ipmi/sel.
func (h *IPMIAdminHandler) ClearSEL(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	creds, err := h.resolveCreds(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := h.ops().IPMISEL(r.Context(), creds, "clear"); err != nil {
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: fmt.Sprintf("ipmi-sel clear failed: %v", err),
			Code:  "ipmi_error",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":    nodeID,
		"cleared":    true,
		"cleared_at": time.Now().UTC(),
	})
}

// GetSensors handles GET /api/v1/nodes/{id}/ipmi/sensors.
func (h *IPMIAdminHandler) GetSensors(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	creds, err := h.resolveCreds(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := h.ops().IPMISensors(r.Context(), creds)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: fmt.Sprintf("ipmi-sensors failed: %v", err),
			Code:  "ipmi_error",
		})
		return
	}
	sensors := parseFreeIPMISensorsForResponse(out)
	writeJSON(w, http.StatusOK, IPMISensorsResponse{
		NodeID:      nodeID,
		Sensors:     sensors,
		LastChecked: time.Now().UTC(),
	})
}

// resolveCreds extracts BMC credentials from the node config.
func (h *IPMIAdminHandler) resolveCreds(ctx context.Context, nodeID string) (privhelper.IPMICredentials, error) {
	cfg, err := h.DB.GetNodeConfig(ctx, nodeID)
	if err != nil {
		return privhelper.IPMICredentials{}, err
	}
	creds, ok := resolveSOLCreds(cfg)
	if !ok {
		return privhelper.IPMICredentials{}, fmt.Errorf("node %q has no BMC configured", nodeID)
	}
	return privhelper.IPMICredentials{
		Host:     creds.Host,
		Username: creds.Username,
		Password: creds.Password,
	}, nil
}

// ops returns h.PrivhelperOps or a fresh real-ops adapter.
func (h *IPMIAdminHandler) ops() PrivhelperOps {
	if h.PrivhelperOps != nil {
		return h.PrivhelperOps
	}
	return realPrivhelperOps{}
}

// parseFreeIPMISELForResponse adapts the package-private ipmi parser for
// the handler's response. We re-use the public FreeIPMIClient wrapper with
// a no-op runner so we invoke the internal parser without exec'ing
// anything.
func parseFreeIPMISELForResponse(out string) []ipmi.SELEntry {
	c := &ipmi.FreeIPMIClient{Runner: &fixedRunner{out: out}}
	entries, _ := c.SEL(context.Background(), ipmi.FreeIPMISELList)
	if entries == nil {
		entries = []ipmi.SELEntry{}
	}
	return entries
}

// parseFreeIPMISensorsForResponse mirrors parseFreeIPMISELForResponse but
// for the sensors verb.
func parseFreeIPMISensorsForResponse(out string) []ipmi.Sensor {
	c := &ipmi.FreeIPMIClient{Runner: &fixedRunner{out: out}}
	sensors, _ := c.Sensors(context.Background())
	if sensors == nil {
		sensors = []ipmi.Sensor{}
	}
	return sensors
}

// fixedRunner returns a fixed pre-captured stdout so the FreeIPMIClient
// parser can be reused without invoking the real binary. Used only for the
// admin handler's response-shaping code path.
type fixedRunner struct{ out string }

func (r *fixedRunner) Run(_ context.Context, _ ...string) (string, error) {
	return r.out, nil
}
