// Package handlers — node_config_history.go: S5-12 config change history endpoint.
//
// GET /api/v1/nodes/{id}/config-history — paginated field-level change log.
// Requires admin scope.
package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
)

// NodeConfigHistoryHandler serves GET /api/v1/nodes/{id}/config-history.
type NodeConfigHistoryHandler struct {
	DB *db.DB
}

// HandleList returns paginated config change rows for a node.
// Query params: ?page=1&per_page=50
func (h *NodeConfigHistoryHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if _, err := h.DB.GetNodeConfig(r.Context(), nodeID); err != nil {
		writeError(w, err)
		return
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))

	rows, total, err := h.DB.ListNodeConfigHistory(r.Context(), nodeID, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	if rows == nil {
		rows = []db.NodeConfigHistoryRow{}
	}

	effectivePage := page
	if effectivePage < 1 {
		effectivePage = 1
	}
	effectivePerPage := perPage
	if effectivePerPage <= 0 {
		effectivePerPage = 50
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"history":  rows,
		"total":    total,
		"page":     effectivePage,
		"per_page": effectivePerPage,
	})
}
