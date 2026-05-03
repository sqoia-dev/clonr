package handlers

// dashboard.go — GET /api/v1/dashboard/anomalies (C2-5)
//
// C2-5 HTMX content negotiation: when HX-Request: true the handler returns
// an HTML fragment (the Anomalies card body) suitable for HTMX swap. When
// HX-Request is absent the handler returns the anomaly counts as JSON.
//
// Anomaly categories (mirrors the JS _buildAnomalyCard from B2-4):
//   - failed: nodes whose State() == NodeStateDeployFailed or DeployVerifyTimeout
//   - never_deployed: configured nodes (BaseImageID set) with no preboot record
//   - stale: nodes with no successful deploy in >90 days

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// DashboardHandler handles dashboard aggregate endpoints.
type DashboardHandler struct {
	DB *db.DB
}

// anomalyCounts holds the three anomaly category counts.
type anomalyCounts struct {
	Failed       int `json:"failed"`
	NeverDeployed int `json:"never_deployed"`
	Stale        int `json:"stale"`
	Total        int `json:"total"`
}

// computeAnomalies queries all node configs and classifies them into the
// three anomaly buckets. Mirrors the JS _buildAnomalyCard logic exactly.
func (h *DashboardHandler) computeAnomalies(ctx context.Context) (anomalyCounts, error) {
	nodes, err := h.DB.ListNodeConfigs(ctx, "")
	if err != nil {
		return anomalyCounts{}, err
	}

	ninetyDaysAgo := time.Now().UTC().Add(-90 * 24 * time.Hour)
	var counts anomalyCounts
	for i := range nodes {
		n := &nodes[i]
		state := n.State()
		if state == api.NodeStateFailed || state == api.NodeStateDeployVerifyTimeout {
			counts.Failed++
			continue
		}
		if n.BaseImageID != "" && n.DeployCompletedPrebootAt == nil && n.DeployVerifiedBootedAt == nil {
			counts.NeverDeployed++
			continue
		}
		// Stale: had a successful deploy but not in the last 90 days.
		if n.DeployVerifiedBootedAt != nil && n.DeployVerifiedBootedAt.Before(ninetyDaysAgo) {
			counts.Stale++
		} else if n.DeployCompletedPrebootAt != nil && n.DeployVerifiedBootedAt == nil &&
			n.DeployVerifyTimeoutAt == nil && n.DeployCompletedPrebootAt.Before(ninetyDaysAgo) {
			// preboot-only deploy (no verify-boot configured) older than 90 days
			counts.Stale++
		}
	}
	counts.Total = counts.Failed + counts.NeverDeployed + counts.Stale
	return counts, nil
}

// HandleAnomalies handles GET /api/v1/dashboard/anomalies.
//
// Returns anomaly counts and, when HX-Request: true, an HTML fragment
// of the anomalies card body for HTMX swap.
func (h *DashboardHandler) HandleAnomalies(w http.ResponseWriter, r *http.Request) {
	counts, err := h.computeAnomalies(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("dashboard: anomaly query failed")
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<div class="empty-state text-muted py-3">Could not load anomalies.</div>`)
			return
		}
		writeError(w, err)
		return
	}

	// C2-5: HTMX content negotiation.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, renderAnomalyCard(counts))
		return
	}

	writeJSON(w, http.StatusOK, counts)
}

// renderAnomalyCard renders the full anomaly card HTML for HTMX outerHTML swap.
//
// When there are no anomalies an empty <div> is returned — HTMX replaces the
// placeholder with it, effectively removing the card from the layout.
// When anomalies exist the full card (header + body) is returned.
//
// The structure mirrors cardWrap() in app.js and the existing _buildAnomalyCard.
func renderAnomalyCard(c anomalyCounts) string {
	if c.Total == 0 {
		// Empty placeholder — no anomalies so no card is shown, but we keep the
		// element with hx-trigger so HTMX continues polling every 30s.
		return `<div id="dash-anomaly-htmx" hx-get="/api/v1/dashboard/anomalies" hx-trigger="every 30s" hx-swap="outerHTML" hx-headers="{&quot;HX-Request&quot;: &quot;true&quot;}"></div>`
	}

	var items strings.Builder
	if c.Failed > 0 {
		label := "nodes with failed deploy or verify timeout"
		if c.Failed == 1 {
			label = "node with failed deploy or verify timeout"
		}
		items.WriteString(fmt.Sprintf(
			`<a href="#/nodes?filter=failed" style="display:flex;align-items:center;gap:8px;padding:8px 12px;border-radius:6px;background:var(--error-bg,#fef2f2);border:1px solid #fca5a5;text-decoration:none;color:inherit;">
				<span style="font-size:20px;font-weight:700;color:#dc2626;">%d</span>
				<span style="font-size:13px;color:#991b1b;">%s</span>
			</a>`, c.Failed, label))
	}
	if c.NeverDeployed > 0 {
		label := "configured nodes never deployed"
		if c.NeverDeployed == 1 {
			label = "configured node never deployed"
		}
		items.WriteString(fmt.Sprintf(
			`<a href="#/nodes?filter=never_deployed" style="display:flex;align-items:center;gap:8px;padding:8px 12px;border-radius:6px;background:#fffbeb;border:1px solid #fde68a;text-decoration:none;color:inherit;">
				<span style="font-size:20px;font-weight:700;color:#d97706;">%d</span>
				<span style="font-size:13px;color:#92400e;">%s</span>
			</a>`, c.NeverDeployed, label))
	}
	if c.Stale > 0 {
		label := "nodes with no successful deploy in >90 days"
		if c.Stale == 1 {
			label = "node with no successful deploy in >90 days"
		}
		items.WriteString(fmt.Sprintf(
			`<a href="#/nodes?filter=stale" style="display:flex;align-items:center;gap:8px;padding:8px 12px;border-radius:6px;background:var(--bg-secondary);border:1px solid var(--border);text-decoration:none;color:inherit;">
				<span style="font-size:20px;font-weight:700;color:var(--text-secondary);">%d</span>
				<span style="font-size:13px;color:var(--text-secondary);">%s</span>
			</a>`, c.Stale, label))
	}

	return fmt.Sprintf(`<div id="dash-anomaly-htmx"
		hx-get="/api/v1/dashboard/anomalies"
		hx-trigger="every 30s"
		hx-swap="outerHTML"
		hx-headers="{&quot;HX-Request&quot;: &quot;true&quot;}">
	<div class="card" style="margin-bottom:20px">
		<div class="card-header">
			<h3 class="card-title">Anomalies</h3>
			<a href="#/nodes" class="btn btn-secondary btn-sm">View Nodes</a>
		</div>
		<div class="card-body">
			<div style="display:flex;flex-direction:column;gap:8px;">%s</div>
		</div>
	</div>
</div>`, items.String())
}
