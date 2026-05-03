package portal

// Publication handlers for the PI portal — Sprint D (D3-2/D3-3, CF-13).
//
// PI can CRUD publications on their owned NodeGroups.
// DOI lookup is available when CLUSTR_DOI_LOOKUP_ENABLED=true (opt-in, not default).
// Admin can CRUD publications on all NodeGroups.
//
// Routes (registered under PI middleware group):
//   GET    /api/v1/portal/pi/groups/{id}/publications
//   POST   /api/v1/portal/pi/groups/{id}/publications
//   PUT    /api/v1/portal/pi/groups/{id}/publications/{pubID}
//   DELETE /api/v1/portal/pi/groups/{id}/publications/{pubID}
//   GET    /api/v1/portal/pi/publications/lookup?doi=<doi>

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// PublicationRequest is the request body for create/update publication.
type PublicationRequest struct {
	DOI     string `json:"doi"`
	Title   string `json:"title"`
	Authors string `json:"authors"`
	Journal string `json:"journal"`
	Year    int    `json:"year"`
}

// DOIMetadata is returned by the CrossRef lookup endpoint.
type DOIMetadata struct {
	DOI     string `json:"doi"`
	Title   string `json:"title"`
	Authors string `json:"authors"`
	Journal string `json:"journal"`
	Year    int    `json:"year"`
	Found   bool   `json:"found"`
}

// HandleListPublications handles GET /api/v1/portal/pi/groups/{id}/publications.
func (h *PIHandler) HandleListPublications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	if role != "admin" {
		owned, err := h.DB.IsNodeGroupOwnedByPI(ctx, groupID, userID)
		if err != nil {
			writeError(w, "failed to verify group ownership", http.StatusInternalServerError)
			return
		}
		if !owned {
			writeError(w, "you do not own this group", http.StatusForbidden)
			return
		}
	}

	pubs, err := h.DB.ListPublicationsByGroup(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("publications: list failed")
		writeError(w, "failed to list publications", http.StatusInternalServerError)
		return
	}
	if pubs == nil {
		pubs = []db.Publication{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"publications": pubs})
}

// HandleCreatePublication handles POST /api/v1/portal/pi/groups/{id}/publications.
func (h *PIHandler) HandleCreatePublication(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	if role != "admin" {
		owned, err := h.DB.IsNodeGroupOwnedByPI(ctx, groupID, userID)
		if err != nil {
			writeError(w, "failed to verify group ownership", http.StatusInternalServerError)
			return
		}
		if !owned {
			writeError(w, "you do not own this group", http.StatusForbidden)
			return
		}
	}

	var req PublicationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Title == "" {
		writeError(w, "title is required", http.StatusBadRequest)
		return
	}

	p := db.Publication{
		ID:              fmt.Sprintf("pub-%d", time.Now().UnixNano()),
		NodeGroupID:     groupID,
		DOI:             req.DOI,
		Title:           req.Title,
		Authors:         req.Authors,
		Journal:         req.Journal,
		Year:            req.Year,
		CreatedByUserID: userID,
	}

	if err := h.DB.CreatePublication(ctx, p); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("publications: create failed")
		writeError(w, "failed to create publication", http.StatusInternalServerError)
		return
	}

	if h.Audit != nil {
		h.Audit.Record(ctx, userID, userID, db.AuditActionPublicationCreate,
			"publication", p.ID, r.RemoteAddr, nil,
			map[string]interface{}{"doi": p.DOI, "title": p.Title, "group_id": groupID})
	}
	writeJSON(w, http.StatusCreated, p)
}

// HandleUpdatePublication handles PUT /api/v1/portal/pi/groups/{id}/publications/{pubID}.
func (h *PIHandler) HandleUpdatePublication(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	pubID := chi.URLParam(r, "pubID")
	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	if role != "admin" {
		owned, err := h.DB.IsNodeGroupOwnedByPI(ctx, groupID, userID)
		if err != nil {
			writeError(w, "failed to verify group ownership", http.StatusInternalServerError)
			return
		}
		if !owned {
			writeError(w, "you do not own this group", http.StatusForbidden)
			return
		}
	}

	existing, err := h.DB.GetPublication(ctx, pubID)
	if err != nil {
		writeError(w, "publication not found", http.StatusNotFound)
		return
	}
	if existing.NodeGroupID != groupID {
		writeError(w, "publication does not belong to this group", http.StatusForbidden)
		return
	}

	var req PublicationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Title == "" {
		writeError(w, "title is required", http.StatusBadRequest)
		return
	}

	existing.DOI = req.DOI
	existing.Title = req.Title
	existing.Authors = req.Authors
	existing.Journal = req.Journal
	existing.Year = req.Year

	if err := h.DB.UpdatePublication(ctx, existing); err != nil {
		log.Error().Err(err).Str("pub_id", pubID).Msg("publications: update failed")
		writeError(w, "failed to update publication", http.StatusInternalServerError)
		return
	}

	if h.Audit != nil {
		h.Audit.Record(ctx, userID, userID, db.AuditActionPublicationUpdate,
			"publication", pubID, r.RemoteAddr, nil,
			map[string]interface{}{"title": existing.Title})
	}
	writeJSON(w, http.StatusOK, existing)
}

// HandleDeletePublication handles DELETE /api/v1/portal/pi/groups/{id}/publications/{pubID}.
func (h *PIHandler) HandleDeletePublication(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	pubID := chi.URLParam(r, "pubID")
	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	if role != "admin" {
		owned, err := h.DB.IsNodeGroupOwnedByPI(ctx, groupID, userID)
		if err != nil {
			writeError(w, "failed to verify group ownership", http.StatusInternalServerError)
			return
		}
		if !owned {
			writeError(w, "you do not own this group", http.StatusForbidden)
			return
		}
	}

	existing, err := h.DB.GetPublication(ctx, pubID)
	if err != nil {
		writeError(w, "publication not found", http.StatusNotFound)
		return
	}
	if existing.NodeGroupID != groupID {
		writeError(w, "publication does not belong to this group", http.StatusForbidden)
		return
	}

	if err := h.DB.DeletePublication(ctx, pubID); err != nil {
		log.Error().Err(err).Str("pub_id", pubID).Msg("publications: delete failed")
		writeError(w, "failed to delete publication", http.StatusInternalServerError)
		return
	}

	if h.Audit != nil {
		h.Audit.Record(ctx, userID, userID, db.AuditActionPublicationDelete,
			"publication", pubID, r.RemoteAddr, nil, nil)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// HandleDOILookup handles GET /api/v1/portal/pi/publications/lookup?doi=<doi>.
//
// This is the ONLY outbound network call clustr makes.
// It is opt-in via CLUSTR_DOI_LOOKUP_ENABLED=true.
// Air-gap deployments keep this disabled (the default) and use manual entry.
//
// When enabled, calls CrossRef: https://api.crossref.org/works/<doi>
// If CrossRef is unreachable or returns an error, returns {"found": false}
// and lets the PI enter metadata manually. Never blocks submission.
func (h *PIHandler) HandleDOILookup(w http.ResponseWriter, r *http.Request) {
	enabled := os.Getenv("CLUSTR_DOI_LOOKUP_ENABLED")
	if enabled != "true" && enabled != "1" {
		writeJSON(w, http.StatusOK, DOIMetadata{Found: false})
		return
	}

	doi := strings.TrimSpace(r.URL.Query().Get("doi"))
	if doi == "" {
		writeError(w, "doi query parameter required", http.StatusBadRequest)
		return
	}

	meta, err := lookupDOI(r.Context(), doi)
	if err != nil {
		log.Warn().Err(err).Str("doi", doi).Msg("doi lookup failed; returning not-found for manual entry")
		writeJSON(w, http.StatusOK, DOIMetadata{Found: false})
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

// lookupDOI fetches metadata for a DOI from the CrossRef API.
// Returns an error on network failure or non-200 response.
// Callers must handle errors gracefully (return manual-entry fallback).
func lookupDOI(ctx context.Context, doi string) (DOIMetadata, error) {
	escapedDOI := url.PathEscape(doi)
	apiURL := "https://api.crossref.org/works/" + escapedDOI

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return DOIMetadata{}, fmt.Errorf("crossref: build request: %w", err)
	}
	req.Header.Set("User-Agent", "clustr/1.3.0 (mailto:noreply@sqoia.dev)")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return DOIMetadata{}, fmt.Errorf("crossref: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return DOIMetadata{}, fmt.Errorf("crossref: status %d for doi %q", resp.StatusCode, doi)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return DOIMetadata{}, fmt.Errorf("crossref: read body: %w", err)
	}

	return parseCrossRefResponse(doi, body)
}

// parseCrossRefResponse parses the CrossRef API response into DOIMetadata.
// CrossRef returns a nested JSON structure; we extract only what we need.
func parseCrossRefResponse(doi string, body []byte) (DOIMetadata, error) {
	var raw struct {
		Status  string `json:"status"`
		Message struct {
			Title  []string `json:"title"`
			Author []struct {
				Given  string `json:"given"`
				Family string `json:"family"`
			} `json:"author"`
			ContainerTitle []string `json:"container-title"`
			Published      struct {
				DateParts [][]int `json:"date-parts"`
			} `json:"published"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return DOIMetadata{}, fmt.Errorf("crossref: parse: %w", err)
	}
	if raw.Status != "ok" {
		return DOIMetadata{}, fmt.Errorf("crossref: status=%q", raw.Status)
	}

	meta := DOIMetadata{DOI: doi, Found: true}

	if len(raw.Message.Title) > 0 {
		meta.Title = raw.Message.Title[0]
	}

	// Build author list as "Family, Given; Family, Given".
	var authorParts []string
	for _, a := range raw.Message.Author {
		if a.Family != "" {
			if a.Given != "" {
				authorParts = append(authorParts, a.Family+", "+a.Given)
			} else {
				authorParts = append(authorParts, a.Family)
			}
		}
	}
	meta.Authors = strings.Join(authorParts, "; ")

	if len(raw.Message.ContainerTitle) > 0 {
		meta.Journal = raw.Message.ContainerTitle[0]
	}

	if len(raw.Message.Published.DateParts) > 0 && len(raw.Message.Published.DateParts[0]) > 0 {
		meta.Year = raw.Message.Published.DateParts[0][0]
	}

	return meta, nil
}
