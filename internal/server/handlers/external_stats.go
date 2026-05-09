package handlers

// external_stats.go — Sprint 38 Bundle A.
//
// Serves GET /api/v1/nodes/{id}/external_stats: the agent-less view of
// a node's reachability + most-recent BMC/SNMP/IPMI samples. Read-only
// API; the writes happen in internal/server/stats/external (the
// goroutine pool).
//
// Wire shape (committed in PR description):
//
//	{
//	  "probes": {
//	    "ping":     bool,
//	    "ssh":      bool,
//	    "ipmi_mc":  bool,
//	    "checked_at": <RFC3339>
//	  } | null,
//	  "samples": {
//	    "bmc":  {...payload...} | null,
//	    "snmp": {...payload...} | null,
//	    "ipmi": {...payload...} | null
//	  },
//	  "last_seen": <RFC3339> | null,        // newest of any source
//	  "expires_at": <RFC3339> | null         // earliest of any source
//	}
//
// Behaviour:
//   - if no rows exist for the node, the handler returns 200 with all
//     fields nil — the UI distinguishes "not yet polled" from
//     "unreachable".
//   - rows whose expires_at has elapsed are silently dropped (the DB
//     layer handles this).

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// ExternalStatsDBIface defines the DB operations used by the
// ExternalStatsHandler. Mirrors the StatsDBIface pattern: small
// single-method interface so tests inject a fake without depending on
// the full DB type.
type ExternalStatsDBIface interface {
	ListExternalStatsForNode(ctx context.Context, nodeID string, now time.Time) ([]db.NodeExternalStatRow, error)
}

// ExternalStatsHandler serves the per-node agent-less view.
type ExternalStatsHandler struct {
	DB  ExternalStatsDBIface
	Now func() time.Time // injectable for tests; nil → time.Now
}

// externalStatsResponse is the wire shape returned by GET
// /external_stats. Field order: probes first (UI shows the three
// dots), then samples (UI tabs), then the freshness envelope.
type externalStatsResponse struct {
	Probes    json.RawMessage         `json:"probes"`
	Samples   externalStatsSamples    `json:"samples"`
	LastSeen  *time.Time              `json:"last_seen"`
	ExpiresAt *time.Time              `json:"expires_at"`
}

// externalStatsSamples is the per-source map. We keep it as a struct
// rather than a generic map[string]json.RawMessage so the JSON shape
// is fixed and the OpenAPI generator (if we add one) gets a stable
// schema. Unknown sources are dropped silently.
type externalStatsSamples struct {
	BMC  json.RawMessage `json:"bmc"`
	SNMP json.RawMessage `json:"snmp"`
	IPMI json.RawMessage `json:"ipmi"`
}

// ServeHTTP implements http.Handler so the route can be registered as
// either Get(handler.ServeHTTP) or with handler directly.
func (h *ExternalStatsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.Get(w, r)
}

// Get returns the latest external_stats envelope for one node.
//
// Route: GET /api/v1/nodes/{id}/external_stats
func (h *ExternalStatsHandler) Get(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if nodeID == "" {
		writeValidationError(w, "missing node id")
		return
	}

	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}

	rows, err := h.DB.ListExternalStatsForNode(r.Context(), nodeID, now)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("external_stats: ListExternalStatsForNode failed")
		writeError(w, err)
		return
	}

	resp := buildExternalStatsResponse(rows)
	writeJSON(w, http.StatusOK, resp)
}

// buildExternalStatsResponse stitches the per-source rows into the
// wire envelope. Exported only at package scope for the unit test in
// external_stats_test.go.
//
// Codex post-ship review issue #11: the envelope's last_seen /
// expires_at must reflect ONLY the sources we recognise.  The
// previous implementation updated both timestamps before checking the
// source, so a stale row from a forward-compat unknown source
// influenced the envelope while its payload was correctly dropped.
// We now switch on Source first; only known sources contribute to
// both the samples map and the envelope timestamps.
func buildExternalStatsResponse(rows []db.NodeExternalStatRow) externalStatsResponse {
	resp := externalStatsResponse{
		Probes: nil,
		Samples: externalStatsSamples{
			BMC:  nil,
			SNMP: nil,
			IPMI: nil,
		},
	}

	var (
		newestSeen     time.Time
		earliestExpire time.Time
	)
	for _, r := range rows {
		switch r.Source {
		case db.ExternalSourceProbe:
			resp.Probes = json.RawMessage(r.Payload)
		case db.ExternalSourceBMC:
			resp.Samples.BMC = json.RawMessage(r.Payload)
		case db.ExternalSourceSNMP:
			resp.Samples.SNMP = json.RawMessage(r.Payload)
		case db.ExternalSourceIPMI:
			resp.Samples.IPMI = json.RawMessage(r.Payload)
		default:
			// Unknown source — drop silently and skip the envelope
			// contribution.  Forward-compatible: when Bundle B adds a
			// new source, old binaries don't crash, they just don't
			// display the new data — and they don't poison the
			// freshness envelope with timestamps for data they can't
			// surface.
			continue
		}

		// Reached only on a recognised source — update the envelope.
		if newestSeen.IsZero() || r.LastSeenAt.After(newestSeen) {
			newestSeen = r.LastSeenAt
		}
		if earliestExpire.IsZero() || r.ExpiresAt.Before(earliestExpire) {
			earliestExpire = r.ExpiresAt
		}
	}

	if !newestSeen.IsZero() {
		t := newestSeen.UTC()
		resp.LastSeen = &t
	}
	if !earliestExpire.IsZero() {
		t := earliestExpire.UTC()
		resp.ExpiresAt = &t
	}
	return resp
}
