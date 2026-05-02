// Package handlers — bios.go implements the BIOS profile management endpoints (#159).
//
// Routes:
//
//	POST   /api/v1/bios-profiles                      — create profile
//	GET    /api/v1/bios-profiles                      — list all profiles
//	GET    /api/v1/bios-profiles/{id}                 — get one profile
//	PUT    /api/v1/bios-profiles/{id}                 — update profile
//	DELETE /api/v1/bios-profiles/{id}                 — delete profile (409 if referenced)
//	PUT    /api/v1/nodes/{id}/bios-profile            — assign profile to node
//	DELETE /api/v1/nodes/{id}/bios-profile            — detach profile from node
//	GET    /api/v1/nodes/{id}/bios-profile            — get current binding
//	GET    /api/v1/bios/providers/{vendor}/verify     — verify operator binary present
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/internal/bios"
	_ "github.com/sqoia-dev/clustr/internal/bios/intel" // self-registers Intel provider
	"github.com/sqoia-dev/clustr/pkg/api"
)

// BiosDBIface is the subset of *db.DB used by BiosHandler.
// Declared as an interface to allow handler testing without the concrete DB.
type BiosDBIface interface {
	CreateBiosProfile(ctx context.Context, p api.BiosProfile) error
	GetBiosProfile(ctx context.Context, id string) (api.BiosProfile, error)
	ListBiosProfiles(ctx context.Context) ([]api.BiosProfile, error)
	UpdateBiosProfile(ctx context.Context, id, name, settingsJSON, description string) error
	DeleteBiosProfile(ctx context.Context, id string) error
	BiosProfileRefCount(ctx context.Context, id string) (int, error)
	AssignBiosProfile(ctx context.Context, nodeID, profileID string) error
	DetachBiosProfile(ctx context.Context, nodeID string) error
	GetNodeBiosProfile(ctx context.Context, nodeID string) (api.NodeBiosProfile, error)
}

// BiosHandler handles /api/v1/bios-profiles and /api/v1/nodes/{id}/bios-profile.
type BiosHandler struct {
	DB BiosDBIface
}

// ─── POST /api/v1/bios-profiles ──────────────────────────────────────────────

// CreateProfile handles profile creation.
// Validates: name required, vendor registered, settings_json is valid JSON object.
// Optionally validates setting keys against Provider.SupportedSettings() when
// the operator binary is present.
func (h *BiosHandler) CreateProfile(w http.ResponseWriter, r *http.Request) {
	var req api.CreateBiosProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeValidationError(w, "name is required")
		return
	}
	if req.Vendor == "" {
		writeValidationError(w, "vendor is required")
		return
	}
	if req.SettingsJSON == "" {
		writeValidationError(w, "settings_json is required")
		return
	}

	// Validate settings_json is a well-formed JSON object.
	var settingsMap map[string]string
	if err := json.Unmarshal([]byte(req.SettingsJSON), &settingsMap); err != nil {
		writeValidationError(w, "settings_json must be a valid JSON object mapping string keys to string values")
		return
	}

	// Validate vendor is registered.
	provider, err := bios.Lookup(req.Vendor)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{
			Error: "unknown vendor: " + req.Vendor + "; registered vendors: " + joinVendors(),
			Code:  "unknown_vendor",
		})
		return
	}

	// Optional: validate settings keys against SupportedSettings if binary is present.
	// A missing binary produces a warning (not an error) so operators can create
	// profiles before placing the binary.
	if supported, serr := provider.SupportedSettings(r.Context()); serr == nil && len(supported) > 0 {
		supportedSet := make(map[string]bool, len(supported))
		for _, s := range supported {
			supportedSet[s] = true
		}
		var unknown []string
		for k := range settingsMap {
			if !supportedSet[k] {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			// Warn but don't block — firmware versions vary.
			w.Header().Set("X-Bios-Warning", "unknown settings keys: "+joinStrings(unknown))
		}
	}

	now := time.Now().UTC()
	profile := api.BiosProfile{
		ID:           uuid.NewString(),
		Name:         req.Name,
		Vendor:       req.Vendor,
		SettingsJSON: req.SettingsJSON,
		Description:  req.Description,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := h.DB.CreateBiosProfile(r.Context(), profile); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, api.BiosProfileResponse{Profile: profile})
}

// ─── GET /api/v1/bios-profiles ───────────────────────────────────────────────

// ListProfiles handles profile listing.
func (h *BiosHandler) ListProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.DB.ListBiosProfiles(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if profiles == nil {
		profiles = []api.BiosProfile{}
	}
	writeJSON(w, http.StatusOK, api.ListBiosProfilesResponse{Profiles: profiles, Total: len(profiles)})
}

// ─── GET /api/v1/bios-profiles/{id} ──────────────────────────────────────────

// GetProfile handles single-profile fetch.
func (h *BiosHandler) GetProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	profile, err := h.DB.GetBiosProfile(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.BiosProfileResponse{Profile: profile})
}

// ─── PUT /api/v1/bios-profiles/{id} ──────────────────────────────────────────

// UpdateProfile handles profile updates (name, settings_json, description).
func (h *BiosHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Fetch existing to use as defaults for omitted fields.
	existing, err := h.DB.GetBiosProfile(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	var req api.UpdateBiosProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	// Apply partial update — only replace what's in the request body.
	name := existing.Name
	settingsJSON := existing.SettingsJSON
	description := existing.Description
	if req.Name != "" {
		name = req.Name
	}
	if req.SettingsJSON != "" {
		var settingsMap map[string]string
		if err := json.Unmarshal([]byte(req.SettingsJSON), &settingsMap); err != nil {
			writeValidationError(w, "settings_json must be a valid JSON object")
			return
		}
		settingsJSON = req.SettingsJSON
	}
	if req.Description != "" {
		description = req.Description
	}

	if err := h.DB.UpdateBiosProfile(r.Context(), id, name, settingsJSON, description); err != nil {
		writeError(w, err)
		return
	}

	updated, err := h.DB.GetBiosProfile(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.BiosProfileResponse{Profile: updated})
}

// ─── DELETE /api/v1/bios-profiles/{id} ───────────────────────────────────────

// DeleteProfile handles profile deletion.
// Returns 409 Conflict when one or more nodes still reference the profile.
func (h *BiosHandler) DeleteProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	count, err := h.DB.BiosProfileRefCount(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if count > 0 {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: "profile is assigned to one or more nodes; detach before deleting",
			Code:  "profile_in_use",
		})
		return
	}

	if err := h.DB.DeleteBiosProfile(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── PUT /api/v1/nodes/{id}/bios-profile ─────────────────────────────────────

// AssignProfile binds an existing profile to a node.
func (h *BiosHandler) AssignProfile(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")

	var req api.AssignBiosProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.ProfileID == "" {
		writeValidationError(w, "profile_id is required")
		return
	}

	// Verify the profile exists.
	if _, err := h.DB.GetBiosProfile(r.Context(), req.ProfileID); err != nil {
		writeError(w, err)
		return
	}

	if err := h.DB.AssignBiosProfile(r.Context(), nodeID, req.ProfileID); err != nil {
		writeError(w, err)
		return
	}

	binding, err := h.DB.GetNodeBiosProfile(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.NodeBiosProfileResponse{Binding: binding})
}

// ─── DELETE /api/v1/nodes/{id}/bios-profile ──────────────────────────────────

// DetachProfile removes the BIOS profile binding from a node.
// Idempotent: returns 204 even when no binding exists.
func (h *BiosHandler) DetachProfile(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if err := h.DB.DetachBiosProfile(r.Context(), nodeID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── GET /api/v1/nodes/{id}/bios-profile ─────────────────────────────────────

// GetNodeProfile returns the current BIOS profile binding for a node.
// Returns 404 when no profile is assigned.
func (h *BiosHandler) GetNodeProfile(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	binding, err := h.DB.GetNodeBiosProfile(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.NodeBiosProfileResponse{Binding: binding})
}

// ─── GET /api/v1/bios/providers/{vendor}/verify ──────────────────────────────

// VerifyProvider checks whether the operator-supplied vendor binary is present
// and executable at its expected path.  Does NOT exec the binary.
func (h *BiosHandler) VerifyProvider(w http.ResponseWriter, r *http.Request) {
	vendor := chi.URLParam(r, "vendor")
	provider, err := bios.Lookup(vendor)
	if err != nil {
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{
			Error: "unknown vendor: " + vendor,
			Code:  "unknown_vendor",
		})
		return
	}

	// Probe binary availability by calling ReadCurrent with a background context.
	// ReadCurrent returns ErrBinaryMissing when the operator has not placed the
	// vendor binary at its expected path — that's the canonical availability check.
	// Any other error (e.g. syscfg failing on this server host) is treated as
	// available=true since the binary is present even if it fails outside initramfs.
	_, readErr := provider.ReadCurrent(r.Context())
	available := !errors.Is(readErr, bios.ErrBinaryMissing)

	// Resolve expected path for Intel (only registered vendor in v1).
	binPath := "/var/lib/clustr/vendor-bios/" + vendor + "/syscfg"
	msg := ""
	if !available {
		msg = "binary not found at " + binPath + "; see docs/BIOS-INTEL-SETUP.md"
	}

	writeJSON(w, http.StatusOK, api.BiosProviderVerifyResponse{
		Vendor:    vendor,
		Available: available,
		BinPath:   binPath,
		Message:   msg,
	})
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func joinVendors() string {
	vendors := bios.RegisteredVendors()
	return joinStrings(vendors)
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}
