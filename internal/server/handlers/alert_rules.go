// Package handlers — alert_rules.go implements GET /api/v1/alerts/rules.
// Returns the currently loaded alert rules from the engine (#155).
package handlers

import (
	"net/http"

	"github.com/sqoia-dev/clustr/internal/alerts"
)

// AlertRulesEngineIface is the subset of *alerts.Engine used by AlertRulesHandler.
type AlertRulesEngineIface interface {
	Rules() []*alerts.Rule
}

// AlertRulesHandler handles GET /api/v1/alerts/rules.
type AlertRulesHandler struct {
	Engine AlertRulesEngineIface
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
