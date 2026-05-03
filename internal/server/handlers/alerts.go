package handlers

// AlertsHandler implements GET /api/v1/alerts (#133).
//
// Query params (all optional):
//
//	severity=warn,critical   — comma-separated severity filter
//	node=<node_id>           — filter by node
//	rule=<rule_name>         — filter by rule name
//	state=firing|resolved    — filter by state; default returns both
//
// Response shape:
//
//	{
//	  "active":  [ ...Alert ],
//	  "recent":  [ ...Alert ]   // last 24h of resolved alerts
//	}
//
// When the state param is set, only the matching bucket is populated; the
// other is an empty array.

import (
	"context"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/alerts"
)

// AlertsStore is the subset of alerts.StateStore used by the handler.
type AlertsStore interface {
	QueryActive(ctx context.Context) ([]alerts.Alert, error)
	QueryRecent(ctx context.Context) ([]alerts.Alert, error)
	QueryFiltered(ctx context.Context, severities []string, nodeID, ruleName, state string) ([]alerts.Alert, error)
}

// AlertsHandler handles the GET /api/v1/alerts endpoint.
type AlertsHandler struct {
	Store AlertsStore
}

// alertsResponse is the JSON envelope returned by the endpoint.
type alertsResponse struct {
	Active []alerts.Alert `json:"active"`
	Recent []alerts.Alert `json:"recent"`
}

// HandleList handles GET /api/v1/alerts.
func (h *AlertsHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	severityParam := strings.TrimSpace(q.Get("severity"))
	nodeParam := strings.TrimSpace(q.Get("node"))
	ruleParam := strings.TrimSpace(q.Get("rule"))
	stateParam := strings.TrimSpace(q.Get("state"))

	// Parse comma-separated severity list.
	var severities []string
	if severityParam != "" {
		for _, s := range strings.Split(severityParam, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				severities = append(severities, s)
			}
		}
	}

	// If any filter is set, use the filtered query; otherwise use the default
	// two-bucket view (active + recent 24h resolved).
	if severityParam != "" || nodeParam != "" || ruleParam != "" || stateParam != "" {
		rows, err := h.Store.QueryFiltered(ctx, severities, nodeParam, ruleParam, stateParam)
		if err != nil {
			log.Error().Err(err).Msg("alerts: QueryFiltered failed")
			writeError(w, err)
			return
		}
		if rows == nil {
			rows = []alerts.Alert{}
		}

		// Split into active / recent buckets for a consistent envelope.
		var active, recent []alerts.Alert
		for _, a := range rows {
			if a.State == alerts.StateFiring {
				active = append(active, a)
			} else {
				recent = append(recent, a)
			}
		}
		if active == nil {
			active = []alerts.Alert{}
		}
		if recent == nil {
			recent = []alerts.Alert{}
		}
		writeJSON(w, http.StatusOK, alertsResponse{Active: active, Recent: recent})
		return
	}

	// Default two-bucket view.
	active, err := h.Store.QueryActive(ctx)
	if err != nil {
		log.Error().Err(err).Msg("alerts: QueryActive failed")
		writeError(w, err)
		return
	}
	recent, err := h.Store.QueryRecent(ctx)
	if err != nil {
		log.Error().Err(err).Msg("alerts: QueryRecent failed")
		writeError(w, err)
		return
	}
	if active == nil {
		active = []alerts.Alert{}
	}
	if recent == nil {
		recent = []alerts.Alert{}
	}
	writeJSON(w, http.StatusOK, alertsResponse{Active: active, Recent: recent})
}
