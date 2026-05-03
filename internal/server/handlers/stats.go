package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// maxStatsRows is the hard cap on query results. If more rows match, we return
// a 414 status with a hint to narrow the time window.
const maxStatsRows = 10000

// StatsDBIface defines the DB operations used by StatsHandler.
type StatsDBIface interface {
	QueryNodeStats(ctx context.Context, p db.QueryNodeStatsParams) ([]db.NodeStatRow, bool, error)
}

// StatsHandler serves GET /api/v1/nodes/{id}/stats.
type StatsHandler struct {
	DB StatsDBIface
}

// GetNodeStats returns per-plugin stats samples for a node.
//
// Route: GET /api/v1/nodes/{id}/stats
// Query params:
//   - plugin  — filter by plugin name (optional)
//   - sensor  — filter by sensor name (optional)
//   - since   — Unix seconds lower bound (default: now-1h)
//   - until   — Unix seconds upper bound (default: now)
//
// Returns up to 10000 rows. If more rows match, responds 414 with a hint.
func (h *StatsHandler) GetNodeStats(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if nodeID == "" {
		writeValidationError(w, "missing node id")
		return
	}

	now := time.Now()
	since := now.Add(-time.Hour)
	until := now

	if s := r.URL.Query().Get("since"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			since = time.Unix(v, 0)
		} else {
			writeValidationError(w, "since must be a Unix timestamp (integer seconds)")
			return
		}
	}
	if u := r.URL.Query().Get("until"); u != "" {
		if v, err := strconv.ParseInt(u, 10, 64); err == nil {
			until = time.Unix(v, 0)
		} else {
			writeValidationError(w, "until must be a Unix timestamp (integer seconds)")
			return
		}
	}

	if since.After(until) {
		writeValidationError(w, "since must be before until")
		return
	}

	plugin := r.URL.Query().Get("plugin")
	sensor := r.URL.Query().Get("sensor")

	rows, truncated, err := h.DB.QueryNodeStats(r.Context(), db.QueryNodeStatsParams{
		NodeID: nodeID,
		Plugin: plugin,
		Sensor: sensor,
		Since:  since,
		Until:  until,
		Limit:  maxStatsRows,
	})
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("stats: QueryNodeStats failed")
		writeError(w, err)
		return
	}

	if truncated {
		writeJSON(w, http.StatusRequestURITooLong, api.ErrorResponse{
			Error: "query matched more than 10000 rows — narrow the time window using since/until",
			Code:  "too_many_rows",
		})
		return
	}

	// Convert DB rows to the API response shape.
	type sampleResponse struct {
		Plugin string            `json:"plugin"`
		Sensor string            `json:"sensor"`
		Value  float64           `json:"value"`
		Unit   string            `json:"unit,omitempty"`
		Labels map[string]string `json:"labels,omitempty"`
		TS     int64             `json:"ts"` // Unix seconds
	}

	out := make([]sampleResponse, len(rows))
	for i, r := range rows {
		out[i] = sampleResponse{
			Plugin: r.Plugin,
			Sensor: r.Sensor,
			Value:  r.Value,
			Unit:   r.Unit,
			Labels: r.Labels,
			TS:     r.TS.Unix(),
		}
	}

	writeJSON(w, http.StatusOK, out)
}
