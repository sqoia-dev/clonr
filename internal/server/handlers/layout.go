package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/hardware"
	"github.com/sqoia-dev/clustr/internal/image/layout"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// LayoutHandler handles layout recommendation, validation, and override endpoints.
type LayoutHandler struct {
	DB *db.DB
}

// diskLayoutCatalogResolver is the minimal interface needed by
// resolveDiskLayoutFromCatalog.  Satisfied by *db.DB and by test fakes.
type diskLayoutCatalogResolver interface {
	GetNodeDiskLayoutID(ctx context.Context, nodeID string) (string, error)
	GetGroupDiskLayoutID(ctx context.Context, groupID string) (string, error)
	GetDiskLayout(ctx context.Context, id string) (api.StoredDiskLayout, error)
}

// resolveDiskLayoutFromCatalog implements the #146 disk_layout_id precedence:
//
//  1. node.disk_layout_id (per-node FK override) — highest
//  2. node_groups.disk_layout_id (group FK default)
//  3. Returns (zero, false) when neither level has a matching record — caller
//     must fall back to the existing inline-override / image-default path.
//
// A missing record for a non-empty FK is treated as a miss (warning logged);
// the caller falls back rather than returning an error so a stale FK doesn't
// take a node offline.
func resolveDiskLayoutFromCatalog(
	ctx context.Context,
	r diskLayoutCatalogResolver,
	nodeID, groupID string,
) (layout api.DiskLayout, source string, ok bool) {
	// Level 1: per-node FK.
	nodeDiskLayoutID, _ := r.GetNodeDiskLayoutID(ctx, nodeID)
	if nodeDiskLayoutID != "" {
		stored, err := r.GetDiskLayout(ctx, nodeDiskLayoutID)
		if err == nil {
			return stored.Layout, "layout_catalog:node", true
		}
		log.Warn().Err(err).Str("node_id", nodeID).Str("disk_layout_id", nodeDiskLayoutID).
			Msg("effective-layout: node disk_layout_id resolves to missing record — falling back")
	}

	// Level 2: group FK.
	if groupID != "" {
		groupDiskLayoutID, _ := r.GetGroupDiskLayoutID(ctx, groupID)
		if groupDiskLayoutID != "" {
			stored, err := r.GetDiskLayout(ctx, groupDiskLayoutID)
			if err == nil {
				return stored.Layout, "layout_catalog:group", true
			}
			log.Warn().Err(err).Str("node_id", nodeID).Str("group_id", groupID).
				Str("disk_layout_id", groupDiskLayoutID).
				Msg("effective-layout: group disk_layout_id resolves to missing record — falling back")
		}
	}

	return api.DiskLayout{}, "", false
}

// GetLayoutRecommendation handles GET /api/v1/nodes/:id/layout-recommendation.
// Returns a hardware-aware DiskLayout recommendation for the node, based on its
// stored hardware profile. When the query parameter ?role=storage is present,
// the response is a StorageRecommendation (ZFS pool layout) instead of a general
// DiskLayout. The recommendation includes human-readable reasoning so the admin
// can evaluate it before applying.
func (h *LayoutHandler) GetLayoutRecommendation(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")
	if strings.EqualFold(role, "storage") {
		h.getStorageLayoutRecommendation(w, r)
		return
	}
	id := chi.URLParam(r, "id")

	node, err := h.DB.GetNodeConfig(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	if len(node.HardwareProfile) == 0 {
		writeValidationError(w, "node has no hardware profile — hardware is discovered on first PXE boot")
		return
	}

	// Parse the stored hardware profile JSON into a SystemInfo.
	var hw hardware.SystemInfo
	if err := json.Unmarshal(node.HardwareProfile, &hw); err != nil {
		log.Error().Err(err).Str("node_id", id).Msg("parse hardware profile for layout recommendation")
		writeError(w, fmt.Errorf("cannot parse hardware profile: %w", err))
		return
	}

	// Determine image format and firmware for the recommendation.
	// Both affect partition layout: format determines whether /boot is needed,
	// firmware determines whether an ESP or biosboot partition is used.
	// Node's detected firmware (from /sys/firmware/efi at PXE boot) takes
	// priority over the image's firmware field.
	imageFormat := string(api.ImageFormatFilesystem)
	imageFirmware := node.DetectedFirmware
	if imageFirmware == "" && node.BaseImageID != "" {
		img, imgErr := h.DB.GetBaseImage(r.Context(), node.BaseImageID)
		if imgErr == nil {
			imageFormat = string(img.Format)
			imageFirmware = string(img.Firmware)
		}
	} else if node.BaseImageID != "" {
		img, imgErr := h.DB.GetBaseImage(r.Context(), node.BaseImageID)
		if imgErr == nil {
			imageFormat = string(img.Format)
		}
	}

	rec, err := layout.Recommend(hw, imageFormat, imageFirmware)
	if err != nil {
		log.Error().Err(err).Str("node_id", id).Msg("layout recommendation failed")
		writeValidationError(w, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, api.LayoutRecommendation{
		Layout:    rec.Layout,
		Reasoning: rec.Reasoning,
		Warnings:  rec.Warnings,
	})
}

// getStorageLayoutRecommendation is the storage-role sub-handler called when
// ?role=storage is passed to GetLayoutRecommendation.
func (h *LayoutHandler) getStorageLayoutRecommendation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	node, err := h.DB.GetNodeConfig(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	if len(node.HardwareProfile) == 0 {
		writeValidationError(w, "node has no hardware profile — hardware is discovered on first PXE boot")
		return
	}

	var hw hardware.SystemInfo
	if err := json.Unmarshal(node.HardwareProfile, &hw); err != nil {
		log.Error().Err(err).Str("node_id", id).Msg("parse hardware profile for storage layout recommendation")
		writeError(w, fmt.Errorf("cannot parse hardware profile: %w", err))
		return
	}

	// Firmware preference: node's detected firmware takes priority.
	imageFirmware := node.DetectedFirmware
	if imageFirmware == "" && node.BaseImageID != "" {
		img, imgErr := h.DB.GetBaseImage(r.Context(), node.BaseImageID)
		if imgErr == nil {
			imageFirmware = string(img.Firmware)
		}
	}

	rec, err := layout.RecommendStorage(hw, imageFirmware)
	if err != nil {
		log.Error().Err(err).Str("node_id", id).Msg("storage layout recommendation failed")
		writeValidationError(w, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, rec)
}

// GetEffectiveLayout handles GET /api/v1/nodes/:id/effective-layout.
// Returns the resolved DiskLayout that will be used for the next deployment,
// including the source level (node / group / image / layout_catalog).
//
// Precedence (highest → lowest):
//  1. node.disk_layout_id         — named layout record (per-node override)
//  2. node_groups.disk_layout_id  — named layout record (group default)
//  3. node.DiskLayoutOverride     — inline JSON override on the node
//  4. node_group.DiskLayoutOverride — inline JSON override on the group
//  5. BaseImage.DiskLayout        — image default
func (h *LayoutHandler) GetEffectiveLayout(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	node, err := h.DB.GetNodeConfig(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	var img *api.BaseImage
	if node.BaseImageID != "" {
		fetched, imgErr := h.DB.GetBaseImage(r.Context(), node.BaseImageID)
		if imgErr == nil {
			img = &fetched
		}
	}

	var group *api.NodeGroup
	if node.GroupID != "" {
		fetched, gErr := h.DB.GetNodeGroup(r.Context(), node.GroupID)
		if gErr == nil {
			group = &fetched
		}
	}

	// ── Precedence levels 1 & 2: named disk layout from the catalog ──────────
	effective, source, catalogHit := resolveDiskLayoutFromCatalog(r.Context(), h.DB, id, node.GroupID)

	// ── Precedence levels 3-5: existing inline / image-default path ───────────
	if !catalogHit {
		// Neither catalog level matched — use existing inline/image logic.
		effective = node.EffectiveLayout(img, group)
		source = node.EffectiveLayoutSource(img, group)
	}

	// Auto-correct layout when:
	//   - The node reported its firmware type at registration (DetectedFirmware set).
	//   - The layout came from the image default (no operator override).
	//   - The image's firmware type doesn't match the node's actual firmware.
	// Operator overrides (node-level, group-level, or catalog) are always respected as-is.
	if node.DetectedFirmware != "" && source == "image" && img != nil {
		effective = layout.AutoCorrectForFirmware(effective, string(img.Firmware), node.DetectedFirmware, node.ID, node.Hostname)
	}

	resp := api.EffectiveLayoutResponse{
		Layout:  effective,
		Source:  source,
		GroupID: node.GroupID,
	}
	if img != nil {
		resp.ImageID = img.ID
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetEffectiveMounts handles GET /api/v1/nodes/:id/effective-mounts.
// Returns the merged fstab entries that will be applied on the next deployment,
// annotated with their source (node-level or group-level).
func (h *LayoutHandler) GetEffectiveMounts(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	node, err := h.DB.GetNodeConfig(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	var group *api.NodeGroup
	if node.GroupID != "" {
		fetched, gErr := h.DB.GetNodeGroup(r.Context(), node.GroupID)
		if gErr == nil {
			group = &fetched
		}
	}

	// Build annotated entries showing provenance.
	resp := api.EffectiveMountsResponse{
		NodeID:  node.ID,
		GroupID: node.GroupID,
	}

	// Start with group mounts.
	if group != nil {
		for _, m := range group.ExtraMounts {
			resp.Mounts = append(resp.Mounts, api.EffectiveMountEntry{
				FstabEntry: m,
				Source:     "group",
				GroupID:    group.ID,
			})
		}
	}
	// Apply node overrides / additions.
	seen := map[string]int{}
	for i, e := range resp.Mounts {
		seen[e.MountPoint] = i
	}
	for _, m := range node.ExtraMounts {
		if idx, exists := seen[m.MountPoint]; exists {
			resp.Mounts[idx] = api.EffectiveMountEntry{
				FstabEntry: m,
				Source:     "node",
			}
		} else {
			resp.Mounts = append(resp.Mounts, api.EffectiveMountEntry{
				FstabEntry: m,
				Source:     "node",
			})
		}
	}
	if resp.Mounts == nil {
		resp.Mounts = []api.EffectiveMountEntry{}
	}

	writeJSON(w, http.StatusOK, resp)
}

// SetNodeLayoutOverride handles PUT /api/v1/nodes/:id/layout-override.
// Stores a node-level DiskLayout override. Send an empty partitions array or
// set clear_layout_override=true to remove the override.
func (h *LayoutHandler) SetNodeLayoutOverride(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Layout              *api.DiskLayout `json:"layout"`
		ClearLayoutOverride bool            `json:"clear_layout_override"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	// Confirm node exists.
	node, err := h.DB.GetNodeConfig(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	var newOverride *api.DiskLayout
	if !req.ClearLayoutOverride && req.Layout != nil && len(req.Layout.Partitions) > 0 {
		// Validate before saving.
		result := layout.Validate(*req.Layout, hardware.Disk{})
		if !result.Valid {
			writeJSON(w, http.StatusUnprocessableEntity, api.LayoutValidationResponse{
				Valid:    false,
				Errors:   result.Errors,
				Warnings: result.Warnings,
			})
			return
		}
		newOverride = req.Layout
	}
	// else: clear override (newOverride stays nil)

	if err := h.DB.SetNodeLayoutOverride(r.Context(), id, newOverride); err != nil {
		log.Error().Err(err).Str("node_id", id).Msg("set node layout override")
		writeError(w, err)
		return
	}

	// Return the updated node.
	node.DiskLayoutOverride = newOverride
	writeJSON(w, http.StatusOK, sanitizeNodeConfig(node))
}

// ValidateLayout handles POST /api/v1/nodes/:id/layout/validate.
// Validates a DiskLayout against the node's discovered hardware.
func (h *LayoutHandler) ValidateLayout(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req api.LayoutValidationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	node, err := h.DB.GetNodeConfig(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	// Extract target disk from hardware profile for size checking.
	targetDisk := hardware.Disk{}
	var ramKB uint64
	if len(node.HardwareProfile) > 0 {
		var hw hardware.SystemInfo
		if parseErr := json.Unmarshal(node.HardwareProfile, &hw); parseErr == nil {
			ramKB = hw.Memory.TotalKB
			// Pick the first non-boot disk as the target for validation.
			for _, d := range hw.Disks {
				if !isBoot(d) {
					targetDisk = d
					break
				}
			}
		}
	}

	result := layout.ValidateWithRAM(req.Layout, targetDisk, ramKB)
	writeJSON(w, http.StatusOK, api.LayoutValidationResponse{
		Valid:    result.Valid,
		Errors:   result.Errors,
		Warnings: result.Warnings,
	})
}

// AssignNodeGroup handles PUT /api/v1/nodes/:id/group.
func (h *LayoutHandler) AssignNodeGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req api.AssignGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	// If a group ID is specified, confirm it exists.
	if req.GroupID != "" {
		if _, err := h.DB.GetNodeGroup(r.Context(), req.GroupID); err != nil {
			writeError(w, err)
			return
		}
	}

	if err := h.DB.AssignNodeToGroup(r.Context(), id, req.GroupID); err != nil {
		log.Error().Err(err).Str("node_id", id).Str("group_id", req.GroupID).Msg("assign node to group")
		writeError(w, err)
		return
	}

	node, err := h.DB.GetNodeConfig(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sanitizeNodeConfig(node))
}

// isBoot returns true if any partition on the disk is mounted at "/" or "/boot".
func isBoot(d hardware.Disk) bool {
	for _, p := range d.Partitions {
		mp := p.MountPoint
		if mp == "/" || mp == "/boot" || mp == "/boot/efi" {
			return true
		}
	}
	return false
}
