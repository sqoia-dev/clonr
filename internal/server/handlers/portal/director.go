package portal

// Director portal handlers — Sprint D (D1-1 through D1-5, CF-17).
//
// The director role gets a read-only summary view at /portal/director/.
// Directors see all NodeGroups, utilization, grants, publications, and review
// cycle status. They cannot mutate anything. BMC config, Slurm internal config,
// and LDAP credentials are NOT exposed — only aggregated summaries.
//
// Routes (all require director or admin role):
//   GET /api/v1/portal/director/summary
//   GET /api/v1/portal/director/groups
//   GET /api/v1/portal/director/groups/{id}
//   GET /api/v1/portal/director/export.csv
//   GET /api/v1/portal/director/export-full.csv
//   GET /api/v1/portal/director/review-cycles
//   GET /api/v1/portal/director/review-cycles/{id}

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// DirectorHandler provides HTTP handlers for the director portal.
type DirectorHandler struct {
	DB    *db.DB
	Audit *db.AuditService
}

// DirectorGroupDetail extends db.DirectorGroupView with grants and publications.
type DirectorGroupDetail struct {
	db.DirectorGroupView
	Grants       []db.Grant        `json:"grants"`
	Publications []db.Publication  `json:"publications"`
}

// HandleSummary handles GET /api/v1/portal/director/summary.
func (h *DirectorHandler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	summary, err := h.DB.GetDirectorSummary(ctx)
	if err != nil {
		log.Error().Err(err).Msg("director: get summary failed")
		writeError(w, "failed to load summary", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// HandleListGroups handles GET /api/v1/portal/director/groups.
func (h *DirectorHandler) HandleListGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groups, err := h.DB.ListDirectorGroups(ctx)
	if err != nil {
		log.Error().Err(err).Msg("director: list groups failed")
		writeError(w, "failed to list groups", http.StatusInternalServerError)
		return
	}
	if groups == nil {
		groups = []db.DirectorGroupView{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"groups": groups})
}

// HandleGetGroup handles GET /api/v1/portal/director/groups/{id}.
func (h *DirectorHandler) HandleGetGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")

	groups, err := h.DB.ListDirectorGroups(ctx)
	if err != nil {
		log.Error().Err(err).Msg("director: get group failed")
		writeError(w, "failed to load group", http.StatusInternalServerError)
		return
	}

	var found *db.DirectorGroupView
	for i := range groups {
		if groups[i].ID == groupID {
			found = &groups[i]
			break
		}
	}
	if found == nil {
		writeError(w, "group not found", http.StatusNotFound)
		return
	}

	grants, err := h.DB.ListGrantsByGroup(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("director: list grants failed")
		grants = nil
	}

	pubs, err := h.DB.ListPublicationsByGroup(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("director: list pubs failed")
		pubs = nil
	}

	if grants == nil {
		grants = []db.Grant{}
	}
	if pubs == nil {
		pubs = []db.Publication{}
	}

	writeJSON(w, http.StatusOK, DirectorGroupDetail{
		DirectorGroupView: *found,
		Grants:            grants,
		Publications:      pubs,
	})
}

// HandleExportCSV handles GET /api/v1/portal/director/export.csv.
// Returns a CSV with per-NodeGroup utilization + grant + publication counts.
func (h *DirectorHandler) HandleExportCSV(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	groups, err := h.DB.ListDirectorGroups(ctx)
	if err != nil {
		log.Error().Err(err).Msg("director: export csv list groups failed")
		http.Error(w, "failed to load groups", http.StatusInternalServerError)
		return
	}

	filename := "clustr-director-export-" + time.Now().UTC().Format("2006-01-02") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"group_id", "group_name",
		"node_count", "deployed_count",
		"grant_count", "publication_count",
	})
	for _, g := range groups {
		_ = cw.Write([]string{
			g.ID, g.Name,
			strconv.Itoa(g.NodeCount), strconv.Itoa(g.DeployedCount),
			strconv.Itoa(g.GrantCount), strconv.Itoa(g.PubCount),
		})
	}
	cw.Flush()
}

// HandleExportCSVFull handles GET /api/v1/portal/director/export-full.csv.
// Returns grants and publications as flat rows.
func (h *DirectorHandler) HandleExportCSVFull(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	grants, err := h.DB.ListAllGrants(ctx)
	if err != nil {
		log.Error().Err(err).Msg("director: export grants failed")
		http.Error(w, "failed to load grants", http.StatusInternalServerError)
		return
	}

	pubs, err := h.DB.ListAllPublications(ctx)
	if err != nil {
		log.Error().Err(err).Msg("director: export pubs failed")
		http.Error(w, "failed to load publications", http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("clustr-grants-pubs-%s.csv", time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	cw := csv.NewWriter(w)

	_ = cw.Write([]string{"type", "group_name", "title", "agency_or_journal", "identifier", "year_or_start", "status"})
	for _, g := range grants {
		_ = cw.Write([]string{
			"grant", g.NodeGroupName, g.Title, g.FundingAgency, g.GrantNumber, g.StartDate, g.Status,
		})
	}
	for _, p := range pubs {
		_ = cw.Write([]string{
			"publication", p.NodeGroupName, p.Title, p.Journal, p.DOI,
			strconv.Itoa(p.Year), "",
		})
	}
	cw.Flush()
}

// HandleListReviewCycles handles GET /api/v1/portal/director/review-cycles.
func (h *DirectorHandler) HandleListReviewCycles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cycles, err := h.DB.ListReviewCycles(ctx)
	if err != nil {
		log.Error().Err(err).Msg("director: list review cycles failed")
		writeError(w, "failed to list review cycles", http.StatusInternalServerError)
		return
	}
	if cycles == nil {
		cycles = []db.ReviewCycle{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"cycles": cycles})
}

// HandleGetReviewCycle handles GET /api/v1/portal/director/review-cycles/{id}.
func (h *DirectorHandler) HandleGetReviewCycle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cycleID := chi.URLParam(r, "id")

	cycle, err := h.DB.GetReviewCycle(ctx, cycleID)
	if err != nil {
		log.Error().Err(err).Str("cycle_id", cycleID).Msg("director: get review cycle failed")
		writeError(w, "review cycle not found", http.StatusNotFound)
		return
	}

	responses, err := h.DB.ListReviewResponsesByCycle(ctx, cycleID)
	if err != nil {
		log.Error().Err(err).Str("cycle_id", cycleID).Msg("director: list review responses failed")
		writeError(w, "failed to list responses", http.StatusInternalServerError)
		return
	}
	if responses == nil {
		responses = []db.ReviewResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cycle":     cycle,
		"responses": responses,
	})
}

// DirectorMiddleware enriches the request context with the director's user ID.
func DirectorMiddleware(userIDFromCtx func(context.Context) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			userID := userIDFromCtx(ctx)
			if userID != "" {
				ctx = context.WithValue(ctx, ctxKeyPortalUID{}, userID)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
