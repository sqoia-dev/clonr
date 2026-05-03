package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
)

// TechTrigDB is the subset of *db.DB used by TechTriggersHandler.
type TechTrigDB interface {
	ListTechTrigStates(ctx context.Context) ([]db.TechTrigState, error)
	GetTechTrigState(ctx context.Context, name db.TechTrigName) (db.TechTrigState, error)
	ResetTechTrig(ctx context.Context, name db.TechTrigName) error
	SetTechTrigManualSignal(ctx context.Context, name db.TechTrigName, signal bool) error
	ListTechTrigHistory(ctx context.Context, limit int) ([]db.TechTrigHistoryRecord, error)
}

// TechTriggersHandler serves the TECH-TRIG monitoring API (Sprint M, v1.11.0).
// All endpoints require admin scope (wired in buildRouter).
type TechTriggersHandler struct {
	DB           TechTrigDB
	Audit        TechTrigAuditRecorder
	GetActorInfo func(r *http.Request) (id, label string)
}

// TechTrigAuditRecorder is satisfied by *db.AuditService.
type TechTrigAuditRecorder interface {
	Record(ctx context.Context, actorID, actorLabel, action, resourceType, resourceID, ipAddr string, oldVal, newVal interface{})
}

// techTrigResponse is the JSON shape returned by GET /api/v1/admin/tech-triggers.
type techTrigResponse struct {
	TriggerName   string     `json:"trigger_name"`
	Description   string     `json:"description"`
	CurrentValue  any        `json:"current_value"`
	Threshold     any        `json:"threshold"`
	Fired         bool       `json:"fired"`
	FiredAt       *time.Time `json:"fired_at,omitempty"`
	LastEvaluated *time.Time `json:"last_evaluated_at,omitempty"`
	ManualSignal  bool       `json:"manual_signal"`
}

// triggerMeta holds the human-readable description for each trigger.
var triggerMeta = map[db.TechTrigName]string{
	db.TechTrigPostgreSQL:  "T1: PostgreSQL migration signal — fires when node count >= 500 OR write contention >= 5 events/sec sustained for 5 minutes",
	db.TechTrigFramework:   "T2: Framework ceiling signal — fires when frontend JS LOC >= 5000 OR operator marks framework friction",
	db.TechTrigMultiTenant: "T3: Multi-tenant isolation signal — fires when operator marks multi-tenant deployment planned",
	db.TechTrigLogArchive:  "T4: Hot/cold log archive signal — fires when estimated log storage >= 50 GiB",
}

func toResponse(s db.TechTrigState) techTrigResponse {
	var cv, th any
	// Best-effort unmarshal; keep raw string if parse fails.
	if err := json.Unmarshal([]byte(s.CurrentValueJSON), &cv); err != nil {
		cv = s.CurrentValueJSON
	}
	if err := json.Unmarshal([]byte(s.ThresholdJSON), &th); err != nil {
		th = s.ThresholdJSON
	}
	return techTrigResponse{
		TriggerName:   string(s.TriggerName),
		Description:   triggerMeta[s.TriggerName],
		CurrentValue:  cv,
		Threshold:     th,
		Fired:         s.Fired(),
		FiredAt:       s.FiredAt,
		LastEvaluated: s.LastEvaluatedAt,
		ManualSignal:  s.ManualSignal,
	}
}

// HandleList handles GET /api/v1/admin/tech-triggers.
// Returns current state for all four triggers.
func (h *TechTriggersHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	states, err := h.DB.ListTechTrigStates(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	resp := make([]techTrigResponse, 0, len(states))
	for _, s := range states {
		resp = append(resp, toResponse(s))
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleHistory handles GET /api/v1/admin/tech-triggers/history.
// Returns past firings, resets, and manual signals from the audit log.
func (h *TechTriggersHandler) HandleHistory(w http.ResponseWriter, r *http.Request) {
	records, err := h.DB.ListTechTrigHistory(r.Context(), 200)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, records)
}

// HandleReset handles POST /api/v1/admin/tech-triggers/{name}/reset.
// Clears fired_at and manual_signal. Audit logged.
func (h *TechTriggersHandler) HandleReset(w http.ResponseWriter, r *http.Request) {
	name := db.TechTrigName(chi.URLParam(r, "name"))
	if !validTriggerName(name) {
		writeValidationError(w, "unknown trigger name")
		return
	}

	// Capture old state for audit before resetting.
	old, _ := h.DB.GetTechTrigState(r.Context(), name)

	if err := h.DB.ResetTechTrig(r.Context(), name); err != nil {
		writeError(w, err)
		return
	}

	var actorID, actorLabel string
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}
	if h.Audit != nil {
		h.Audit.Record(r.Context(), actorID, actorLabel,
			"tech_trig.reset", "tech_trigger", string(name), r.RemoteAddr,
			map[string]interface{}{"fired_at": old.FiredAt, "manual_signal": old.ManualSignal},
			map[string]interface{}{"fired_at": nil, "manual_signal": false},
		)
	}

	// Return updated state.
	state, err := h.DB.GetTechTrigState(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
		return
	}
	writeJSON(w, http.StatusOK, toResponse(state))
}

// HandleSignal handles POST /api/v1/admin/tech-triggers/{name}/signal.
// Flips the manual_signal boolean. Only valid for T2 (framework) and T3 (multitenant).
// Body: {"signal": true|false}
func (h *TechTriggersHandler) HandleSignal(w http.ResponseWriter, r *http.Request) {
	name := db.TechTrigName(chi.URLParam(r, "name"))
	if name != db.TechTrigFramework && name != db.TechTrigMultiTenant {
		writeValidationError(w, "signal endpoint is only valid for t2_framework and t3_multitenant")
		return
	}

	var body struct {
		Signal bool `json:"signal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "invalid request body")
		return
	}

	old, _ := h.DB.GetTechTrigState(r.Context(), name)

	if err := h.DB.SetTechTrigManualSignal(r.Context(), name, body.Signal); err != nil {
		writeError(w, err)
		return
	}

	var actorID, actorLabel string
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}
	if h.Audit != nil {
		h.Audit.Record(r.Context(), actorID, actorLabel,
			"tech_trig.signal", "tech_trigger", string(name), r.RemoteAddr,
			map[string]interface{}{"manual_signal": old.ManualSignal},
			map[string]interface{}{"manual_signal": body.Signal},
		)
	}

	state, err := h.DB.GetTechTrigState(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	writeJSON(w, http.StatusOK, toResponse(state))
}

// validTriggerName confirms the provided name is one of the four canonical triggers.
func validTriggerName(name db.TechTrigName) bool {
	switch name {
	case db.TechTrigPostgreSQL, db.TechTrigFramework, db.TechTrigMultiTenant, db.TechTrigLogArchive:
		return true
	}
	return false
}

