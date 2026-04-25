package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/image"
	"github.com/sqoia-dev/clustr/internal/image/isoinstaller"
)

// FactoryHandler handles image ingest operations and chroot shell sessions.
type FactoryHandler struct {
	DB       *db.DB
	ImageDir string
	Factory  *image.Factory
	Shells   *image.ShellManager
}

// ListImageRoles handles GET /api/v1/image-roles.
// Returns the static list of built-in HPC node role presets with UI-friendly
// metadata: id, name, description, notes, and the unique package count across
// all supported distros (so the UI can show "45 packages" without knowing the
// target distro at browse-time).
func (h *FactoryHandler) ListImageRoles(w http.ResponseWriter, _ *http.Request) {
	roles := isoinstaller.HPCRoles
	out := make([]api.ImageRoleResponse, 0, len(roles))
	for _, role := range roles {
		// Count unique packages across all distros for the UI preview line.
		pkgSet := map[string]struct{}{}
		for _, pkgs := range role.Packages {
			for _, p := range pkgs {
				pkgSet[p] = struct{}{}
			}
		}
		out = append(out, api.ImageRoleResponse{
			ID:           role.ID,
			Name:         role.Name,
			Description:  role.Description,
			Notes:        role.Notes,
			PackageCount: len(pkgSet),
		})
	}
	writeJSON(w, http.StatusOK, api.ListImageRolesResponse{Roles: out, Total: len(out)})
}

// Pull handles POST /api/v1/factory/pull
// Delegates to image.Factory.PullImage, which returns immediately with a
// "building" record and downloads/extracts in the background.
func (h *FactoryHandler) Pull(w http.ResponseWriter, r *http.Request) {
	var req api.PullRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.URL == "" {
		writeValidationError(w, "url is required")
		return
	}
	if req.Name == "" {
		writeValidationError(w, "name is required")
		return
	}

	img, err := h.Factory.PullImage(r.Context(), req)
	if err != nil {
		log.Error().Err(err).Msg("factory pull")
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, img)
}

// Import handles POST /api/v1/factory/import
// Accepts a multipart upload: field "file" or "iso" = the image file, fields
// "name", "version" directly in the form, or field "meta" = JSON ImportISORequest.
// Streams the upload to CLUSTR_ISO_DIR (default /var/lib/clustr/iso/) and calls
// Factory.ImportISO. The temp file is cleaned up by the async import goroutine.
// Supports large files (2-4 GB ISOs) — the 32 MiB memory limit causes the rest
// to be spooled to disk by Go's multipart parser.
func (h *FactoryHandler) Import(w http.ResponseWriter, r *http.Request) {
	// 32 MiB buffered in memory; remainder spooled to disk by the multipart parser.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeValidationError(w, "failed to parse multipart form")
		return
	}

	// Accept metadata either as individual form fields or as a JSON "meta" blob.
	var meta api.ImportISORequest
	if metaStr := r.FormValue("meta"); metaStr != "" {
		if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
			writeValidationError(w, "invalid meta JSON")
			return
		}
	}
	// Individual fields override the meta blob when both are present.
	if n := r.FormValue("name"); n != "" {
		meta.Name = n
	}
	if v := r.FormValue("version"); v != "" {
		meta.Version = v
	}
	if meta.Name == "" {
		writeValidationError(w, "name is required")
		return
	}

	// Accept either "file" (browser upload widget) or legacy "iso" field name.
	file, _, err := r.FormFile("file")
	if err != nil {
		file, _, err = r.FormFile("iso")
	}
	if err != nil {
		writeValidationError(w, "file field is required (multipart field name: file or iso)")
		return
	}
	defer file.Close()

	// Write the upload to CLUSTR_ISO_DIR so it lives alongside other ISOs and is
	// on the same filesystem as the image store (avoids cross-device rename issues).
	isoDir := os.Getenv("CLUSTR_ISO_DIR")
	if isoDir == "" {
		isoDir = defaultISODir
	}
	if err := os.MkdirAll(isoDir, 0o755); err != nil {
		log.Error().Err(err).Str("iso_dir", isoDir).Msg("factory import: create iso dir")
		writeError(w, err)
		return
	}

	tmp, err := os.CreateTemp(isoDir, "clustr-upload-*.tmp")
	if err != nil {
		log.Error().Err(err).Msg("factory import: create temp file")
		writeError(w, err)
		return
	}
	tmpPath := tmp.Name()

	written, err := io.Copy(tmp, file)
	tmp.Close()
	if err != nil {
		os.Remove(tmpPath)
		log.Error().Err(err).Msg("factory import: stream upload to disk")
		writeError(w, err)
		return
	}
	log.Info().Str("tmp", tmpPath).Int64("bytes", written).Msg("factory import: upload received")

	// ImportISO launches an async goroutine; it is responsible for removing tmpPath
	// after it has finished mounting/extracting the file.
	img, err := h.Factory.ImportISO(r.Context(), tmpPath, meta.Name, meta.Version)
	if err != nil {
		os.Remove(tmpPath)
		log.Error().Err(err).Msg("factory import ISO")
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, img)
}

// defaultISODir is the allowed base directory for server-local ISO imports.
// Override with CLUSTR_ISO_DIR environment variable.
const defaultISODir = "/var/lib/clustr/iso"

// ImportPath handles POST /api/v1/factory/import-path (and /factory/import-iso alias)
// For server-local ISO imports: accepts a JSON body with "path", "name", "version".
// Only useful when the CLI is running on the same host as the server.
// The path must be within CLUSTR_ISO_DIR (default /var/lib/clustr/iso).
func (h *FactoryHandler) ImportPath(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if body.Path == "" {
		writeValidationError(w, "path is required")
		return
	}
	if body.Name == "" {
		writeValidationError(w, "name is required")
		return
	}
	// Resolve to absolute so the async goroutine doesn't lose cwd context.
	absPath, err := filepath.Abs(body.Path)
	if err != nil {
		writeValidationError(w, "cannot resolve path")
		return
	}

	// Enforce that the path is under the configured ISO directory to prevent
	// arbitrary host path access.
	isoDir := os.Getenv("CLUSTR_ISO_DIR")
	if isoDir == "" {
		isoDir = defaultISODir
	}
	isoDir = filepath.Clean(isoDir)
	if !strings.HasPrefix(absPath, isoDir+string(filepath.Separator)) && absPath != isoDir {
		log.Warn().Str("path", absPath).Str("iso_dir", isoDir).Msg("factory import-path: path outside allowed directory")
		writeValidationError(w, "path must be within the configured ISO directory (CLUSTR_ISO_DIR)")
		return
	}

	img, err := h.Factory.ImportISO(r.Context(), absPath, body.Name, body.Version)
	if err != nil {
		log.Error().Err(err).Str("path", absPath).Msg("factory import-path")
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, img)
}

// ProbeISO handles POST /api/v1/factory/probe-iso.
//
// Downloads (or cache-hits) an ISO and returns available environment groups
// parsed from the ISO's comps XML. For non-RHEL ISOs or minimal ISOs without
// comps data, returns no_comps=true and an empty environments list.
//
// This is a synchronous, potentially long-running request. The client should
// use a long HTTP timeout (recommended: 600s). On a cache hit the response
// arrives in <5s; on a cold cache it waits for the full ISO download.
func (h *FactoryHandler) ProbeISO(w http.ResponseWriter, r *http.Request) {
	var req api.ProbeISORequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.URL == "" {
		writeValidationError(w, "url is required")
		return
	}
	if !strings.HasSuffix(strings.ToLower(strings.Split(req.URL, "?")[0]), ".iso") {
		writeValidationError(w, "url must point to an installer ISO (.iso extension)")
		return
	}

	environments, distro, volumeLabel, noComps, err := h.Factory.ProbeISO(r.Context(), req.URL)
	if err != nil {
		log.Error().Err(err).Str("url", req.URL).Msg("factory probe-iso")
		writeError(w, err)
		return
	}
	if environments == nil {
		environments = []api.ISOEnvironmentGroup{}
	}

	writeJSON(w, http.StatusOK, api.ProbeISOResponse{
		URL:          req.URL,
		Distro:       distro,
		VolumeLabel:  volumeLabel,
		Environments: environments,
		NoComps:      noComps,
	})
}

// BuildFromISO handles POST /api/v1/factory/build-from-iso
//
// Downloads an installer ISO from a URL, runs it in a temporary QEMU VM with
// an auto-generated kickstart/autoinstall, extracts the installed filesystem,
// and creates a ready BaseImage. Returns 202 immediately; the build runs async.
// Requires qemu-system-x86_64, qemu-img, and genisoimage/xorriso on the server.
func (h *FactoryHandler) BuildFromISO(w http.ResponseWriter, r *http.Request) {
	var req api.BuildFromISORequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.URL == "" {
		writeValidationError(w, "url is required")
		return
	}
	if req.Name == "" {
		writeValidationError(w, "name is required")
		return
	}
	if !strings.HasSuffix(strings.ToLower(strings.Split(req.URL, "?")[0]), ".iso") {
		writeValidationError(w, "url must point to an installer ISO (.iso extension)")
		return
	}
	if req.DiskSizeGB < 0 || (req.DiskSizeGB > 0 && req.DiskSizeGB < 10) {
		writeValidationError(w, "disk_size_gb must be >= 10 (or 0 for default of 20)")
		return
	}
	if req.MemoryMB < 0 || (req.MemoryMB > 0 && req.MemoryMB < 512) {
		writeValidationError(w, "memory_mb must be >= 512 (or 0 for default of 2048)")
		return
	}

	img, err := h.Factory.BuildFromISO(r.Context(), req)
	if err != nil {
		log.Error().Err(err).Msg("factory build-from-iso")
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, img)
}

// Capture handles POST /api/v1/factory/capture
// Accepts a CaptureRequest, SSHes to the source host, and streams the filesystem
// into a new BaseImage via rsync. Returns 202 immediately; the capture runs async.
// Poll GET /api/v1/images/:id for status transitions: building → ready | error.
func (h *FactoryHandler) Capture(w http.ResponseWriter, r *http.Request) {
	var req api.CaptureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.SourceHost == "" {
		writeValidationError(w, "source_host is required")
		return
	}
	if req.Name == "" {
		writeValidationError(w, "name is required")
		return
	}
	if req.Version == "" {
		req.Version = "1.0.0"
	}

	captureReq := image.CaptureRequest{
		SourceHost:   req.SourceHost,
		SSHUser:      req.SSHUser,
		SSHPassword:  req.SSHPassword,
		SSHKeyPath:   req.SSHKeyPath,
		SSHPort:      req.SSHPort,
		Name:         req.Name,
		Version:      req.Version,
		OS:           req.OS,
		Arch:         req.Arch,
		Tags:         req.Tags,
		Notes:        req.Notes,
		ExcludePaths: req.ExcludePaths,
	}

	img, err := h.Factory.CaptureNode(r.Context(), captureReq)
	if err != nil {
		log.Error().Err(err).Msg("factory capture")
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, img)
}

// OpenShellSession handles POST /api/v1/images/:id/shell-session
// Creates and enters a chroot session for the specified image.
func (h *FactoryHandler) OpenShellSession(w http.ResponseWriter, r *http.Request) {
	imageID := chi.URLParam(r, "id")

	sess, err := h.Shells.OpenSession(r.Context(), imageID)
	if err != nil {
		log.Error().Err(err).Str("image_id", imageID).Msg("open shell session")
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, api.ShellSessionResponse{
		SessionID: sess.ID,
		ImageID:   sess.ImageID,
		RootDir:   sess.RootDir,
	})
}

// CloseShellSession handles DELETE /api/v1/images/:id/shell-session/:sid
func (h *FactoryHandler) CloseShellSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sid")

	if err := h.Shells.CloseSession(sessionID); err != nil {
		log.Error().Err(err).Str("session_id", sessionID).Msg("close shell session")
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ExecInSession handles POST /api/v1/images/:id/shell-session/:sid/exec
func (h *FactoryHandler) ExecInSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sid")

	var req api.ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Command == "" {
		writeValidationError(w, "command is required")
		return
	}

	out, err := h.Shells.ExecInSession(r.Context(), sessionID, req.Command, req.Args...)
	if err != nil {
		log.Error().Err(err).Str("session_id", sessionID).Str("cmd", req.Command).Msg("exec in session")
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, api.ExecResponse{Output: string(out)})
}
