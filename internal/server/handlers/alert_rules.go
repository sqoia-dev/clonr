// Package handlers — alert_rules.go implements:
//   - GET  /api/v1/alerts/rules          (#155) list loaded alert rules
//   - PUT  /api/v1/alerts/rules/{name}   (UX-9) write/update a rule YAML file
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/alerts"
	"github.com/sqoia-dev/clustr/internal/privhelper"
	"go.yaml.in/yaml/v2"
)

// AlertRulesEngineIface is the subset of *alerts.Engine used by AlertRulesHandler.
type AlertRulesEngineIface interface {
	Rules() []*alerts.Rule
	// Reload forces an immediate rule reload. Safe to call from any goroutine.
	Reload()
}

// AlertRulesStatsQuerier is used by the PUT handler to validate that the
// rule's (plugin, sensor) pair has been seen in node_stats. Satisfied by
// *db.DB via AlertRulesDBAdapter.
type AlertRulesStatsQuerier interface {
	// KnownPluginSensor returns true when at least one node_stats row with the
	// given plugin and sensor exists. May return (false, nil) for brand-new
	// deployments with no stats yet.
	KnownPluginSensor(ctx context.Context, plugin, sensor string) (bool, error)
}

// AlertRulesHandler handles GET and PUT /api/v1/alerts/rules.
type AlertRulesHandler struct {
	Engine AlertRulesEngineIface
	// StatsDB is used by the PUT handler for plugin/sensor validation.
	// May be nil; if nil, the unknown-plugin 422 check is skipped.
	StatsDB AlertRulesStatsQuerier
	// RuleWriter is the function used to persist the rule YAML file. Defaults to
	// privhelper.RuleWrite when nil. Overridable for tests.
	RuleWriter func(ctx context.Context, name string, content []byte) error
}

// ruleResponse is the JSON shape returned for each rule.
// Unexported fields (sourceFile, compiledLabels) are omitted.
type ruleResponse struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Plugin      string            `json:"plugin"`
	Sensor      string            `json:"sensor"`
	Labels      map[string]string `json:"labels,omitempty"`
	Threshold   struct {
		Op    string  `json:"op"`
		Value float64 `json:"value"`
	} `json:"threshold"`
	DurationSeconds float64 `json:"duration_seconds"`
	Severity        string  `json:"severity"`
	Notify          struct {
		Webhook bool     `json:"webhook"`
		Email   []string `json:"email,omitempty"`
	} `json:"notify"`
}

// HandleList handles GET /api/v1/alerts/rules.
func (h *AlertRulesHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	rules := h.Engine.Rules()
	out := make([]ruleResponse, 0, len(rules))
	for _, rule := range rules {
		rr := ruleResponse{
			Name:            rule.Name,
			Description:     rule.Description,
			Plugin:          rule.Plugin,
			Sensor:          rule.Sensor,
			Labels:          rule.Labels,
			DurationSeconds: rule.Duration.Seconds(),
			Severity:        rule.Severity,
		}
		rr.Threshold.Op = string(rule.Threshold.Op)
		rr.Threshold.Value = rule.Threshold.Value
		rr.Notify.Webhook = rule.Notify.Webhook
		rr.Notify.Email = rule.Notify.Email
		out = append(out, rr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out, "total": len(out)})
}

// putRuleRequest is the JSON body accepted by PUT /api/v1/alerts/rules/{name}.
type putRuleRequest struct {
	YAML string `json:"yaml"`
}

// HandleUpdate handles PUT /api/v1/alerts/rules/{name}.
// Body: { "yaml": "<raw YAML>" }
// Responses:
//   - 200 { "ok": true, "rule": <parsed ruleResponse> }
//   - 400 { "error": "..." } — YAML parse failure or name mismatch
//   - 422 { "error": "...", "identifier": "..." } — unknown plugin/sensor
func (h *AlertRulesHandler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	urlName := chi.URLParam(r, "name")
	if urlName == "" {
		writeJSONError(w, http.StatusBadRequest, "rule name is required in URL path")
		return
	}

	var req putRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.YAML) == "" {
		writeJSONError(w, http.StatusBadRequest, "yaml field is required and must not be empty")
		return
	}

	// Parse the YAML against the Rule schema.
	var rule alerts.Rule
	if err := yaml.Unmarshal([]byte(req.YAML), &rule); err != nil {
		writeJSONError(w, http.StatusBadRequest, "YAML parse error: "+err.Error())
		return
	}

	// Validate rule structure (required fields, valid ops, severity, etc.).
	if err := rule.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "rule validation error: "+err.Error())
		return
	}

	// Name in body must match the URL path parameter.
	if rule.Name != urlName {
		writeJSONError(w, http.StatusBadRequest,
			"rule name in YAML (\""+rule.Name+"\") does not match URL path (\""+urlName+"\")")
		return
	}

	// 422 check: plugin/sensor must be known in node_stats (or _meta).
	// Skip for the built-in _meta plugin which has no stats rows.
	if rule.Plugin != "_meta" && h.StatsDB != nil {
		known, err := h.StatsDB.KnownPluginSensor(ctx, rule.Plugin, rule.Sensor)
		if err != nil {
			log.Error().Err(err).Str("rule", urlName).Msg("alert_rules: plugin/sensor check failed")
			writeError(w, err)
			return
		}
		if !known {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":      "unknown plugin/sensor combination — no node_stats data found",
				"identifier": rule.Plugin + "/" + rule.Sensor,
			})
			return
		}
	}

	// Write the rule file via clustr-privhelper rule-write verb (or stub in tests).
	writer := h.RuleWriter
	if writer == nil {
		writer = privhelper.RuleWrite
	}
	if err := writer(ctx, urlName, []byte(req.YAML)); err != nil {
		log.Error().Err(err).Str("rule", urlName).Msg("alert_rules: privhelper rule-write failed")
		writeJSONError(w, http.StatusInternalServerError, "failed to write rule file: "+err.Error())
		return
	}

	// Signal the engine to reload immediately so the next tick uses the new rule.
	h.Engine.Reload()

	log.Info().Str("rule", urlName).Msg("alert_rules: rule updated via PUT")

	// Build and return the parsed rule as confirmation.
	rr := ruleResponse{
		Name:            rule.Name,
		Description:     rule.Description,
		Plugin:          rule.Plugin,
		Sensor:          rule.Sensor,
		Labels:          rule.Labels,
		DurationSeconds: rule.Duration.Seconds(),
		Severity:        rule.Severity,
	}
	rr.Threshold.Op = string(rule.Threshold.Op)
	rr.Threshold.Value = rule.Threshold.Value
	rr.Notify.Webhook = rule.Notify.Webhook
	rr.Notify.Email = rule.Notify.Email

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": rr})
}

// writeJSONError writes a JSON { "error": msg } response with the given status code.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ─── DB adapter ───────────────────────────────────────────────────────────────

// AlertRulesDBAdapter adapts *sql.DB to AlertRulesStatsQuerier.
type AlertRulesDBAdapter struct {
	db *sql.DB
}

// NewAlertRulesDBAdapter wraps a *sql.DB for use as AlertRulesStatsQuerier.
func NewAlertRulesDBAdapter(db *sql.DB) *AlertRulesDBAdapter {
	return &AlertRulesDBAdapter{db: db}
}

// KnownPluginSensor returns true when the (plugin, sensor) pair exists in
// node_stats. A brand-new cluster with no stats returns (false, nil).
func (a *AlertRulesDBAdapter) KnownPluginSensor(ctx context.Context, plugin, sensor string) (bool, error) {
	var count int
	err := a.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_stats WHERE plugin = ? AND sensor = ? LIMIT 1`,
		plugin, sensor,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
