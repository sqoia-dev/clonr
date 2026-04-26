package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/db"
)

const defaultPerPage = 50
const maxPerPage = 500

// pageParams holds parsed pagination parameters.
type pageParams struct {
	page    int // 1-based
	perPage int
}

// paginate applies pagination to a slice length.
// Returns (startIdx, endIdx, params) where startIdx/endIdx are the slice bounds
// and params.page/params.perPage reflect the resolved values.
func paginate(total, rawPage, rawPerPage int) (start, end int, p pageParams) {
	perPage := rawPerPage
	if perPage <= 0 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	page := rawPage
	if page <= 0 {
		page = 1
	}
	start = (page - 1) * perPage
	if start > total {
		start = total
	}
	end = start + perPage
	if end > total {
		end = total
	}
	return start, end, pageParams{page: page, perPage: perPage}
}

// parsePaginationQuery parses ?page= and ?per_page= from the request.
// Returns (page, perPage, pagingRequested) — pagingRequested is true when
// either parameter is present in the URL.
func parsePaginationQuery(r *http.Request) (page, perPage int, paging bool) {
	pageStr := r.URL.Query().Get("page")
	perPageStr := r.URL.Query().Get("per_page")
	if pageStr == "" && perPageStr == "" {
		return 0, 0, false
	}
	if v, err := strconv.Atoi(pageStr); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(perPageStr); err == nil && v > 0 {
		perPage = v
	}
	return page, perPage, true
}

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a structured error response, mapping sentinel errors to
// appropriate HTTP status codes.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, api.ErrNotFound):
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{Error: err.Error(), Code: "not_found"})
	case errors.Is(err, api.ErrConflict):
		writeJSON(w, http.StatusConflict, api.ErrorResponse{Error: err.Error(), Code: "conflict"})
	case errors.Is(err, api.ErrBadRequest):
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: err.Error(), Code: "bad_request"})
	default:
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{Error: "internal server error", Code: "internal_error"})
	}
}

// writeValidationError writes a 400 with a custom message.
func writeValidationError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: msg, Code: "validation_error"})
}

// mergeGroupExtraMounts returns a copy of cfg with ExtraMounts replaced by
// the effective merged list (group base + node overrides). Used to pre-compute
// the deploy-time mount list before returning a NodeConfig to the client.
// If the node is not in a group, or the group fetch fails, the node's own
// ExtraMounts are returned unchanged.
func mergeGroupExtraMounts(ctx context.Context, store *db.DB, cfg api.NodeConfig) api.NodeConfig {
	if cfg.GroupID == "" {
		return cfg
	}
	group, err := store.GetNodeGroup(ctx, cfg.GroupID)
	if err != nil {
		// Non-fatal: group may have been deleted between assignment and query.
		log.Warn().Err(err).Str("group_id", cfg.GroupID).
			Msg("handlers: could not load group for extra-mounts merge — using node mounts only")
		return cfg
	}
	merged := cfg.EffectiveExtraMounts(&group)
	cfg.ExtraMounts = merged
	return cfg
}
