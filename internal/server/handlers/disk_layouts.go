// Package handlers — disk_layouts.go implements the disk layout catalog endpoints (#146).
//
// Routes:
//
//	POST   /api/v1/disk-layouts                    — create (or duplicate) a layout
//	POST   /api/v1/disk-layouts/capture/{node_id}  — capture from live node
//	GET    /api/v1/disk-layouts                    — list all
//	GET    /api/v1/disk-layouts/{id}               — fetch one
//	PUT    /api/v1/disk-layouts/{id}               — rename / edit JSON
//	DELETE /api/v1/disk-layouts/{id}               — delete (409 if referenced)
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// DiskLayoutsDBIface is the subset of *db.DB used by DiskLayoutsHandler.
// Declared as an interface to make the handler testable without the concrete DB.
type DiskLayoutsDBIface interface {
	CreateDiskLayout(ctx context.Context, dl api.StoredDiskLayout) error
	GetDiskLayout(ctx context.Context, id string) (api.StoredDiskLayout, error)
	ListDiskLayouts(ctx context.Context) ([]api.StoredDiskLayout, error)
	UpdateDiskLayoutFields(ctx context.Context, id, name string, layout api.DiskLayout) error
	DeleteDiskLayout(ctx context.Context, id string) error
	DiskLayoutRefCount(ctx context.Context, id string) (int, error)
	// GetNodeDiskLayoutID and GetGroupDiskLayoutID are used by the deploy
	// precedence resolver in layout.go; included here so the mock covers them.
	GetNodeDiskLayoutID(ctx context.Context, nodeID string) (string, error)
	GetGroupDiskLayoutID(ctx context.Context, groupID string) (string, error)
}

// DiskLayoutsCaptureHub is the subset of ClientdHubIface needed for capture.
type DiskLayoutsCaptureHub interface {
	IsConnected(nodeID string) bool
	Send(nodeID string, msg clientd.ServerMessage) error
	RegisterDiskCapture(msgID string) <-chan clientd.DiskCaptureResultPayload
	UnregisterDiskCapture(msgID string)
}

// DiskLayoutsHandler handles /api/v1/disk-layouts endpoints.
type DiskLayoutsHandler struct {
	DB  DiskLayoutsDBIface
	Hub DiskLayoutsCaptureHub // nil-safe — capture endpoint returns 503 when nil
}

// ─── POST /api/v1/disk-layouts ───────────────────────────────────────────────

// CreateLayout creates a new named StoredDiskLayout record.
// Used by the UI to create from scratch or to duplicate an existing layout
// (the caller supplies the full body with a new name, e.g. "original (copy)").
//
// Body: { "name": "my-layout", "firmware_kind": "any"|"bios"|"uefi", "layout_json": "{...}" }
// Response (201): { "disk_layout": { id, name, ... } }
func (h *DiskLayoutsHandler) CreateLayout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		FirmwareKind string `json:"firmware_kind"`
		LayoutJSON   string `json:"layout_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeValidationError(w, "name is required")
		return
	}

	fk := req.FirmwareKind
	if fk == "" {
		fk = api.FirmwareKindAny
	}
	if fk != api.FirmwareKindAny && fk != api.FirmwareKindBIOS && fk != api.FirmwareKindUEFI {
		writeValidationError(w, "firmware_kind must be 'any', 'bios', or 'uefi'")
		return
	}

	var diskLayout api.DiskLayout
	if req.LayoutJSON != "" {
		if err := json.Unmarshal([]byte(req.LayoutJSON), &diskLayout); err != nil {
			writeValidationError(w, "layout_json is not valid JSON: "+err.Error())
			return
		}
	}

	now := time.Now().UTC()
	dl := api.StoredDiskLayout{
		ID:           uuid.New().String(),
		Name:         req.Name,
		FirmwareKind: fk,
		Layout:       diskLayout,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.DB.CreateDiskLayout(r.Context(), dl); err != nil {
		log.Error().Err(err).Str("name", req.Name).Msg("disk-layouts: create")
		writeError(w, err)
		return
	}
	log.Info().Str("layout_id", dl.ID).Str("name", dl.Name).Msg("disk-layouts: created")
	writeJSON(w, http.StatusCreated, map[string]any{"disk_layout": dl})
}

// ─── POST /api/v1/disk-layouts/capture/{node_id} ─────────────────────────────

// CaptureLayout captures the disk layout from a live node via the clientd hub
// and stores it as a named StoredDiskLayout record.
//
// Body: { "name": "compute-h100-v3" }
// Response (201): { "disk_layout": { id, name, ... } }
func (h *DiskLayoutsHandler) CaptureLayout(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	if nodeID == "" {
		writeValidationError(w, "missing node_id")
		return
	}

	if h.Hub == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.ErrorResponse{
			Error: "clientd hub not available",
			Code:  "hub_unavailable",
		})
		return
	}
	if !h.Hub.IsConnected(nodeID) {
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: "node is not connected (clustr-clientd offline)",
			Code:  "node_offline",
		})
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeValidationError(w, "name is required")
		return
	}

	msgID := uuid.New().String()
	payload, err := json.Marshal(clientd.DiskCaptureRequestPayload{RefMsgID: msgID})
	if err != nil {
		writeError(w, err)
		return
	}

	captureCh := h.Hub.RegisterDiskCapture(msgID)
	defer h.Hub.UnregisterDiskCapture(msgID)

	serverMsg := clientd.ServerMessage{
		Type:    "disk_capture_request",
		MsgID:   msgID,
		Payload: json.RawMessage(payload),
	}
	if err := h.Hub.Send(nodeID, serverMsg); err != nil {
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: "failed to send disk_capture_request to node: " + err.Error(),
			Code:  "send_failed",
		})
		return
	}

	log.Info().Str("node_id", nodeID).Str("msg_id", msgID).
		Msg("disk-layouts: capture request sent to node, waiting for result")

	select {
	case result := <-captureCh:
		if result.Error != "" {
			writeJSON(w, http.StatusUnprocessableEntity, api.ErrorResponse{
				Error: "node returned capture error: " + result.Error,
				Code:  "capture_failed",
			})
			return
		}
		if result.LayoutJSON == "" {
			writeJSON(w, http.StatusUnprocessableEntity, api.ErrorResponse{
				Error: "node returned empty layout",
				Code:  "capture_empty",
			})
			return
		}
		var diskLayout api.DiskLayout
		if err := json.Unmarshal([]byte(result.LayoutJSON), &diskLayout); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, api.ErrorResponse{
				Error: "node returned invalid layout JSON: " + err.Error(),
				Code:  "capture_invalid_json",
			})
			return
		}
		now := time.Now().UTC()
		dl := api.StoredDiskLayout{
			ID:           uuid.New().String(),
			Name:         req.Name,
			SourceNodeID: nodeID,
			CapturedAt:   now,
			Layout:       diskLayout,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := h.DB.CreateDiskLayout(r.Context(), dl); err != nil {
			log.Error().Err(err).Str("node_id", nodeID).Str("name", req.Name).
				Msg("disk-layouts: failed to store captured layout")
			writeError(w, err)
			return
		}
		log.Info().Str("node_id", nodeID).Str("layout_id", dl.ID).Str("name", dl.Name).
			Msg("disk-layouts: layout captured and stored")
		writeJSON(w, http.StatusCreated, map[string]any{"disk_layout": dl})

	case <-time.After(60 * time.Second):
		writeJSON(w, http.StatusGatewayTimeout, api.ErrorResponse{
			Error: "timed out waiting for disk_capture_result from node (60s)",
			Code:  "capture_timeout",
		})
	case <-r.Context().Done():
		// Client disconnected.
		return
	}
}

// ─── GET /api/v1/disk-layouts ────────────────────────────────────────────────

// ListLayouts returns all stored disk layouts.
//
// Supports optional pagination: ?page=N&per_page=M
func (h *DiskLayoutsHandler) ListLayouts(w http.ResponseWriter, r *http.Request) {
	layouts, err := h.DB.ListDiskLayouts(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("disk-layouts: list")
		writeError(w, err)
		return
	}
	if layouts == nil {
		layouts = []api.StoredDiskLayout{}
	}

	total := len(layouts)
	rawPage, rawPerPage, paging := parsePaginationQuery(r)

	resp := api.ListDiskLayoutsResponse{Total: total}
	if paging {
		start, end, p := paginate(total, rawPage, rawPerPage)
		resp.Layouts = layouts[start:end]
		// Unused fields would be Page/PerPage but ListDiskLayoutsResponse doesn't
		// carry them yet — keep the response minimal and consistent with the spec.
		_ = p
	} else {
		resp.Layouts = layouts
	}
	writeJSON(w, http.StatusOK, resp)
}

// ─── GET /api/v1/disk-layouts/{id} ───────────────────────────────────────────

// GetLayout returns a single stored disk layout by ID.
func (h *DiskLayoutsHandler) GetLayout(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dl, err := h.DB.GetDiskLayout(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disk_layout": dl})
}

// ─── PUT /api/v1/disk-layouts/{id} ───────────────────────────────────────────

// UpdateLayout edits the name and/or layout JSON of an existing record.
//
// Body: { "name"?: "new-name", "layout_json"?: "{...}" }
// Either field is optional; both must be valid when supplied.
func (h *DiskLayoutsHandler) UpdateLayout(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Fetch the current record first so we can apply partial updates.
	current, err := h.DB.GetDiskLayout(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	var req struct {
		Name       string `json:"name"`
		LayoutJSON string `json:"layout_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	newName := current.Name
	if req.Name != "" {
		newName = req.Name
	}

	newLayout := current.Layout
	if req.LayoutJSON != "" {
		if err := json.Unmarshal([]byte(req.LayoutJSON), &newLayout); err != nil {
			writeValidationError(w, "layout_json is not valid JSON: "+err.Error())
			return
		}
	}

	if err := h.DB.UpdateDiskLayoutFields(r.Context(), id, newName, newLayout); err != nil {
		log.Error().Err(err).Str("id", id).Msg("disk-layouts: update")
		writeError(w, err)
		return
	}

	updated, err := h.DB.GetDiskLayout(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disk_layout": updated})
}

// ─── DELETE /api/v1/disk-layouts/{id} ────────────────────────────────────────

// DeleteLayout removes a disk layout record.
//
// Returns 409 Conflict if any node group or node still references the layout.
// No cascade — operators must reassign or clear the references first.
func (h *DiskLayoutsHandler) DeleteLayout(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Confirm it exists.
	if _, err := h.DB.GetDiskLayout(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}

	refs, err := h.DB.DiskLayoutRefCount(r.Context(), id)
	if err != nil {
		log.Error().Err(err).Str("id", id).Msg("disk-layouts: ref count check")
		writeError(w, err)
		return
	}
	if refs > 0 {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: "disk layout is still referenced by one or more nodes or node groups; reassign them before deleting",
			Code:  "layout_in_use",
		})
		return
	}

	if err := h.DB.DeleteDiskLayout(r.Context(), id); err != nil {
		log.Error().Err(err).Str("id", id).Msg("disk-layouts: delete")
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
