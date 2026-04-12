// Package handlers — ipmi.go implements the power management API endpoints.
// Power operations are delegated to the pluggable power.Provider abstraction.
// The provider is selected per-node from the node's PowerProvider config; when
// no provider is configured the handler falls back to legacy IPMI using the
// node's BMC config.
//
// Route prefix: /api/v1/nodes/{id}/power  and  /api/v1/nodes/{id}/sensors
package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/pkg/db"
	"github.com/sqoia-dev/clonr/pkg/ipmi"
	"github.com/sqoia-dev/clonr/pkg/power"
)

// bmcTimeout caps every power management operation at 10 seconds.
// ipmitool can hang for 30+ seconds when a BMC is unreachable; we prefer a
// fast failure so the UI can show an error quickly.
const bmcTimeout = 10 * time.Second

// PowerCache is the minimal interface the IPMIHandler needs — satisfied by
// *server.PowerCache without importing the server package (avoids import cycle).
// GetFlat returns primitive values so this interface carries no cross-package
// struct types.
type PowerCache interface {
	GetFlat(nodeID string) (status string, lastChecked time.Time, errMsg string, ok bool)
	Set(nodeID, status, errMsg string)
	Invalidate(nodeID string)
}

// IPMIHandler handles /api/v1/nodes/{id}/power* and /api/v1/nodes/{id}/sensors.
type IPMIHandler struct {
	DB       *db.DB
	Cache    PowerCache
	Registry *power.Registry
}

// ─── Response types ───────────────────────────────────────────────────────────

// PowerStatusResponse is returned by GET /api/v1/nodes/{id}/power.
type PowerStatusResponse struct {
	Status      string    `json:"status"`          // "on", "off", or "unknown"
	LastChecked time.Time `json:"last_checked"`
	Error       string    `json:"error,omitempty"` // set when the provider was unreachable
}

// PowerActionResponse is returned after a successful power action.
type PowerActionResponse struct {
	Action      string    `json:"action"`
	NodeID      string    `json:"node_id"`
	Status      string    `json:"status,omitempty"`
	LastChecked time.Time `json:"last_checked,omitempty"`
}

// SensorsResponse is returned by GET /api/v1/nodes/{id}/sensors.
type SensorsResponse struct {
	NodeID      string        `json:"node_id"`
	Sensors     []ipmi.Sensor `json:"sensors"`
	LastChecked time.Time     `json:"last_checked"`
}

// ─── Provider resolution ──────────────────────────────────────────────────────

// nodeProvider fetches the NodeConfig and resolves the power.Provider for the node.
//
// Resolution order:
//  1. cfg.PowerProvider (explicit provider config) — used when present.
//  2. cfg.BMC (legacy IPMI config) — builds an "ipmi" provider from BMC fields.
//
// Returns ok==false and writes an appropriate HTTP error when neither is set.
func (h *IPMIHandler) nodeProvider(w http.ResponseWriter, r *http.Request) (nodeID string, prov power.Provider, ok bool) {
	nodeID = chi.URLParam(r, "id")
	cfg, err := h.DB.GetNodeConfig(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return nodeID, nil, false
	}

	// 1. Explicit PowerProvider config.
	if cfg.PowerProvider != nil && cfg.PowerProvider.Type != "" {
		if h.Registry == nil {
			writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
				Error: "power registry not initialised",
				Code:  "internal_error",
			})
			return nodeID, nil, false
		}
		prov, err = h.Registry.Create(power.ProviderConfig{
			Type:   cfg.PowerProvider.Type,
			Fields: cfg.PowerProvider.Fields,
		})
		if err != nil {
			log.Error().Str("node_id", nodeID).Str("provider", cfg.PowerProvider.Type).
				Err(err).Msg("power: failed to create provider")
			writeJSON(w, http.StatusBadRequest, api.ErrorResponse{
				Error: fmt.Sprintf("power provider error: %v", err),
				Code:  "provider_error",
			})
			return nodeID, nil, false
		}
		return nodeID, prov, true
	}

	// 2. Legacy fallback: build an IPMI provider from the BMC config.
	if cfg.BMC == nil || cfg.BMC.IPAddress == "" {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{
			Error: "no power provider configured for this node — add a power_provider or BMC config",
			Code:  "power_not_configured",
		})
		return nodeID, nil, false
	}

	if h.Registry != nil {
		prov, err = h.Registry.Create(power.ProviderConfig{
			Type: "ipmi",
			Fields: map[string]string{
				"host":     cfg.BMC.IPAddress,
				"username": cfg.BMC.Username,
				"password": cfg.BMC.Password,
			},
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
				Error: fmt.Sprintf("failed to build IPMI provider: %v", err),
				Code:  "internal_error",
			})
			return nodeID, nil, false
		}
		return nodeID, prov, true
	}

	// Fallback for code paths without a registry (e.g. some unit tests).
	prov = &legacyIPMIAdapter{client: &ipmi.Client{
		Host:     cfg.BMC.IPAddress,
		Username: cfg.BMC.Username,
		Password: cfg.BMC.Password,
	}}
	return nodeID, prov, true
}

// legacyIPMIAdapter wraps ipmi.Client when the registry is not available.
type legacyIPMIAdapter struct{ client *ipmi.Client }

func (p *legacyIPMIAdapter) Name() string { return "ipmi" }
func (p *legacyIPMIAdapter) Status(ctx context.Context) (power.PowerStatus, error) {
	s, err := p.client.PowerStatus(ctx)
	if err != nil {
		return power.PowerUnknown, err
	}
	if s == ipmi.PowerOn {
		return power.PowerOn, nil
	}
	return power.PowerOff, nil
}
func (p *legacyIPMIAdapter) PowerOn(ctx context.Context) error    { return p.client.PowerOn(ctx) }
func (p *legacyIPMIAdapter) PowerOff(ctx context.Context) error   { return p.client.PowerOff(ctx) }
func (p *legacyIPMIAdapter) PowerCycle(ctx context.Context) error { return p.client.PowerCycle(ctx) }
func (p *legacyIPMIAdapter) Reset(ctx context.Context) error      { return p.client.PowerReset(ctx) }
func (p *legacyIPMIAdapter) SetNextBoot(ctx context.Context, dev power.BootDevice) error {
	switch dev {
	case power.BootPXE:
		return p.client.SetBootPXE(ctx)
	case power.BootDisk:
		return p.client.SetBootDisk(ctx)
	default:
		return fmt.Errorf("ipmi: unsupported boot device %q", dev)
	}
}
func (p *legacyIPMIAdapter) SetPersistentBootOrder(_ context.Context, _ []power.BootDevice) error {
	return power.ErrNotSupported
}

// ─── GET /api/v1/nodes/{id}/power ────────────────────────────────────────────

// GetPowerStatus returns the current power state of the node.
// Results are cached for 15 seconds to avoid hammering the provider on every UI poll.
func (h *IPMIHandler) GetPowerStatus(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")

	// Return cached result if still fresh.
	if h.Cache != nil {
		if status, lastChecked, errMsg, hit := h.Cache.GetFlat(nodeID); hit {
			writeJSON(w, http.StatusOK, PowerStatusResponse{
				Status:      status,
				LastChecked: lastChecked,
				Error:       errMsg,
			})
			return
		}
	}

	_, prov, ok := h.nodeProvider(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), bmcTimeout)
	defer cancel()

	now := time.Now().UTC()
	ps, err := prov.Status(ctx)
	if err != nil {
		errMsg := fmt.Sprintf("power provider (%s) unreachable: %v", prov.Name(), err)
		log.Warn().Str("node_id", nodeID).Str("provider", prov.Name()).Err(err).Msg("power: status failed")
		if h.Cache != nil {
			h.Cache.Set(nodeID, "unknown", errMsg)
		}
		writeJSON(w, http.StatusOK, PowerStatusResponse{
			Status:      "unknown",
			LastChecked: now,
			Error:       errMsg,
		})
		return
	}

	status := string(ps)
	if h.Cache != nil {
		h.Cache.Set(nodeID, status, "")
	}
	writeJSON(w, http.StatusOK, PowerStatusResponse{
		Status:      status,
		LastChecked: now,
	})
}

// ─── Power action handlers ────────────────────────────────────────────────────

// PowerOn handles POST /api/v1/nodes/{id}/power/on
func (h *IPMIHandler) PowerOn(w http.ResponseWriter, r *http.Request) {
	h.doPowerAction(w, r, "on", func(ctx context.Context, p power.Provider) error {
		return p.PowerOn(ctx)
	})
}

// PowerOff handles POST /api/v1/nodes/{id}/power/off
func (h *IPMIHandler) PowerOff(w http.ResponseWriter, r *http.Request) {
	h.doPowerAction(w, r, "off", func(ctx context.Context, p power.Provider) error {
		return p.PowerOff(ctx)
	})
}

// PowerCycle handles POST /api/v1/nodes/{id}/power/cycle
func (h *IPMIHandler) PowerCycle(w http.ResponseWriter, r *http.Request) {
	h.doPowerAction(w, r, "cycle", func(ctx context.Context, p power.Provider) error {
		return p.PowerCycle(ctx)
	})
}

// PowerReset handles POST /api/v1/nodes/{id}/power/reset
func (h *IPMIHandler) PowerReset(w http.ResponseWriter, r *http.Request) {
	h.doPowerAction(w, r, "reset", func(ctx context.Context, p power.Provider) error {
		return p.Reset(ctx)
	})
}

// SetBootPXE handles POST /api/v1/nodes/{id}/power/pxe
// Sets next boot device to PXE, then power-cycles the node.
func (h *IPMIHandler) SetBootPXE(w http.ResponseWriter, r *http.Request) {
	h.doPowerAction(w, r, "pxe", func(ctx context.Context, p power.Provider) error {
		if err := p.SetNextBoot(ctx, power.BootPXE); err != nil {
			return fmt.Errorf("set boot PXE: %w", err)
		}
		return p.PowerCycle(ctx)
	})
}

// SetBootDisk handles POST /api/v1/nodes/{id}/power/disk
func (h *IPMIHandler) SetBootDisk(w http.ResponseWriter, r *http.Request) {
	h.doPowerAction(w, r, "disk", func(ctx context.Context, p power.Provider) error {
		return p.SetNextBoot(ctx, power.BootDisk)
	})
}

// doPowerAction is the common implementation for all mutating power actions.
// It resolves the provider, runs fn, invalidates the cache, and logs the action.
func (h *IPMIHandler) doPowerAction(
	w http.ResponseWriter,
	r *http.Request,
	action string,
	fn func(ctx context.Context, p power.Provider) error,
) {
	nodeID, prov, ok := h.nodeProvider(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), bmcTimeout)
	defer cancel()

	if err := fn(ctx, prov); err != nil {
		log.Error().Str("node_id", nodeID).Str("provider", prov.Name()).
			Str("action", action).Err(err).Msg("power: action failed")
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: fmt.Sprintf("power %s failed (%s): %v", action, prov.Name(), err),
			Code:  "power_error",
		})
		return
	}

	// Invalidate cache so the next status poll reflects the new state.
	if h.Cache != nil {
		h.Cache.Invalidate(nodeID)
	}

	log.Info().Str("node_id", nodeID).Str("provider", prov.Name()).
		Str("action", action).Msg("power: action succeeded")

	writeJSON(w, http.StatusOK, PowerActionResponse{
		Action:      action,
		NodeID:      nodeID,
		LastChecked: time.Now().UTC(),
	})
}

// ─── GET /api/v1/nodes/{id}/sensors ──────────────────────────────────────────

// GetSensors handles GET /api/v1/nodes/{id}/sensors.
// Returns IPMI sensor readings. Only meaningful for IPMI-backed nodes;
// non-IPMI providers return an empty list so the UI degrades gracefully.
func (h *IPMIHandler) GetSensors(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")

	cfg, err := h.DB.GetNodeConfig(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}

	// Resolve IPMI credentials regardless of which config path they came from.
	var bmcHost, bmcUser, bmcPass string
	if cfg.PowerProvider != nil && cfg.PowerProvider.Type == "ipmi" {
		bmcHost = cfg.PowerProvider.Fields["host"]
		bmcUser = cfg.PowerProvider.Fields["username"]
		bmcPass = cfg.PowerProvider.Fields["password"]
	} else if cfg.BMC != nil && cfg.BMC.IPAddress != "" {
		bmcHost = cfg.BMC.IPAddress
		bmcUser = cfg.BMC.Username
		bmcPass = cfg.BMC.Password
	}

	var sensors []ipmi.Sensor
	if bmcHost != "" {
		client := &ipmi.Client{Host: bmcHost, Username: bmcUser, Password: bmcPass}
		ctx, cancel := context.WithTimeout(r.Context(), bmcTimeout)
		defer cancel()

		sensors, err = client.GetSensorData(ctx)
		if err != nil {
			log.Warn().Str("node_id", nodeID).Str("bmc_host", bmcHost).
				Err(err).Msg("power: sensor read failed")
			writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
				Error: fmt.Sprintf("sensor read failed: %v", err),
				Code:  "power_error",
			})
			return
		}
	}

	if sensors == nil {
		sensors = []ipmi.Sensor{}
	}
	writeJSON(w, http.StatusOK, SensorsResponse{
		NodeID:      nodeID,
		Sensors:     sensors,
		LastChecked: time.Now().UTC(),
	})
}
