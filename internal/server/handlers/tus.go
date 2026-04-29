package handlers

// tus.go — TUS 1.0 protocol server for resumable ISO uploads.
//
// Implements core TUS under /api/v1/uploads/:
//   OPTIONS /api/v1/uploads/       — capabilities
//   POST    /api/v1/uploads/       — create upload, returns Location
//   HEAD    /api/v1/uploads/{id}   — return current offset
//   PATCH   /api/v1/uploads/{id}   — append bytes at offset
//   DELETE  /api/v1/uploads/{id}   — abort upload, clean up
//
// After TUS upload completes, the client calls:
//   POST /api/v1/images/from-upload — register assembled file as an image
//
// Storage: uploads land in <ImageDir>/tus/<id>.part
// Stale uploads (no PATCH for >24h) are garbage-collected periodically.
// Spec: https://tus.io/protocols/resumable-upload

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

const (
	tusVersion    = "1.0.0"
	tusMaxAge     = 24 * time.Hour
	tusGCInterval = 4 * time.Hour
)

// tusUploadMeta holds in-memory state for an in-progress TUS upload.
type tusUploadMeta struct {
	ID          string
	Length      int64
	Offset      int64
	Filename    string
	Path        string
	CreatedAt   time.Time
	LastSeen    time.Time
	mu          sync.Mutex
}

// TUSHandler handles TUS 1.0 upload protocol endpoints.
type TUSHandler struct {
	ImageDir     string
	DB           *db.DB
	Audit        *db.AuditService
	ImageEvents  ImageEventStoreIface
	GetActorInfo func(r *http.Request) (id, label string)

	uploads sync.Map // id → *tusUploadMeta
	gcOnce  sync.Once
}

func (h *TUSHandler) tusDir() string {
	return filepath.Join(h.ImageDir, "tus")
}

// StartGC launches a periodic goroutine to remove stale uploads.
// Safe to call multiple times; only one GC goroutine ever runs.
func (h *TUSHandler) StartGC() {
	h.gcOnce.Do(func() {
		go func() {
			for {
				time.Sleep(tusGCInterval)
				h.gcStaleUploads()
			}
		}()
	})
}

func (h *TUSHandler) gcStaleUploads() {
	cutoff := time.Now().Add(-tusMaxAge)
	h.uploads.Range(func(k, v interface{}) bool {
		meta, ok := v.(*tusUploadMeta)
		if !ok {
			return true
		}
		meta.mu.Lock()
		stale := meta.LastSeen.Before(cutoff)
		meta.mu.Unlock()
		if stale {
			log.Info().Str("upload_id", meta.ID).Msg("tus: GC stale upload")
			_ = os.Remove(meta.Path)
			h.uploads.Delete(k)
		}
		return true
	})
}

func (h *TUSHandler) setTUSHeaders(w http.ResponseWriter) {
	w.Header().Set("Tus-Resumable", tusVersion)
	w.Header().Set("Tus-Version", tusVersion)
	w.Header().Set("Tus-Extension", "creation,termination,expiration")
	w.Header().Set("Tus-Max-Size", strconv.FormatInt(blobMaxBytes(), 10))
}

// Options handles OPTIONS /api/v1/uploads/ — returns TUS capabilities.
func (h *TUSHandler) Options(w http.ResponseWriter, r *http.Request) {
	h.setTUSHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

// Create handles POST /api/v1/uploads/ — create a new upload resource.
func (h *TUSHandler) Create(w http.ResponseWriter, r *http.Request) {
	h.setTUSHeaders(w)

	uploadLength, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil || uploadLength <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if uploadLength > blobMaxBytes() {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}

	// Parse Upload-Metadata (comma-separated "key base64val" pairs).
	filename := ""
	if raw := r.Header.Get("Upload-Metadata"); raw != "" {
		for _, pair := range strings.Split(raw, ",") {
			parts := strings.SplitN(strings.TrimSpace(pair), " ", 2)
			if len(parts) == 2 && parts[0] == "filename" {
				if decoded, decErr := base64.StdEncoding.DecodeString(parts[1]); decErr == nil {
					filename = string(decoded)
				}
			}
		}
	}

	idBytes := make([]byte, 16)
	_, _ = rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	if err := os.MkdirAll(h.tusDir(), 0o755); err != nil {
		log.Error().Err(err).Msg("tus: mkdirall")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	partPath := filepath.Join(h.tusDir(), id+".part")
	if f, err := os.Create(partPath); err != nil {
		log.Error().Err(err).Msg("tus: create part file")
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else {
		_ = f.Close()
	}

	now := time.Now().UTC()
	h.uploads.Store(id, &tusUploadMeta{
		ID:        id,
		Length:    uploadLength,
		Offset:    0,
		Filename:  filename,
		Path:      partPath,
		CreatedAt: now,
		LastSeen:  now,
	})

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	w.Header().Set("Location", fmt.Sprintf("%s://%s/api/v1/uploads/%s", scheme, host, id))
	w.WriteHeader(http.StatusCreated)
}

// Head handles HEAD /api/v1/uploads/{id} — return current offset.
func (h *TUSHandler) Head(w http.ResponseWriter, r *http.Request) {
	h.setTUSHeaders(w)
	id := chi.URLParam(r, "id")
	v, ok := h.uploads.Load(id)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	meta := v.(*tusUploadMeta)
	meta.mu.Lock()
	offset, length := meta.Offset, meta.Length
	meta.mu.Unlock()
	w.Header().Set("Upload-Offset", strconv.FormatInt(offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// Patch handles PATCH /api/v1/uploads/{id} — append bytes at the current offset.
func (h *TUSHandler) Patch(w http.ResponseWriter, r *http.Request) {
	h.setTUSHeaders(w)
	if r.Header.Get("Content-Type") != "application/offset+octet-stream" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}

	id := chi.URLParam(r, "id")
	v, ok := h.uploads.Load(id)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	meta := v.(*tusUploadMeta)
	meta.mu.Lock()
	defer meta.mu.Unlock()

	clientOffset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if clientOffset != meta.Offset {
		w.Header().Set("Upload-Offset", strconv.FormatInt(meta.Offset, 10))
		w.WriteHeader(http.StatusConflict)
		return
	}

	f, err := os.OpenFile(meta.Path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Error().Err(err).Str("upload_id", id).Msg("tus: open part file")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer f.Close()

	remaining := meta.Length - meta.Offset
	n, copyErr := io.Copy(f, io.LimitReader(r.Body, remaining))
	if copyErr != nil {
		log.Error().Err(copyErr).Str("upload_id", id).Msg("tus: write patch")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	meta.Offset += n
	meta.LastSeen = time.Now().UTC()
	w.Header().Set("Upload-Offset", strconv.FormatInt(meta.Offset, 10))
	w.WriteHeader(http.StatusNoContent)
}

// TUSDelete handles DELETE /api/v1/uploads/{id} — abort and clean up.
func (h *TUSHandler) TUSDelete(w http.ResponseWriter, r *http.Request) {
	h.setTUSHeaders(w)
	id := chi.URLParam(r, "id")
	v, loaded := h.uploads.LoadAndDelete(id)
	if !loaded {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = os.Remove(v.(*tusUploadMeta).Path)
	w.WriteHeader(http.StatusNoContent)
}

// FromUpload handles POST /api/v1/images/from-upload — IMG-ISO-2.
// After TUS upload completes, the client calls this to register the file as an image.
func (h *TUSHandler) FromUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UploadID       string `json:"upload_id"`
		Name           string `json:"name"`
		ExpectedSHA256 string `json:"expected_sha256"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.UploadID == "" {
		writeValidationError(w, "upload_id is required")
		return
	}

	v, ok := h.uploads.Load(req.UploadID)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{Error: "upload not found", Code: "upload_not_found"})
		return
	}
	meta := v.(*tusUploadMeta)
	meta.mu.Lock()
	offset, length, path, filename := meta.Offset, meta.Length, meta.Path, meta.Filename
	meta.mu.Unlock()

	if offset < length {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: fmt.Sprintf("upload incomplete: %d/%d bytes received", offset, length),
			Code:  "upload_incomplete",
		})
		return
	}

	name := req.Name
	if name == "" {
		name = filename
	}
	if name == "" {
		name = "uploaded-image"
	}

	// Move .part file to permanent blob path.
	imgID := tusNewID()
	blobPath := filepath.Join(h.ImageDir, imgID+".blob")
	if err := os.Rename(path, blobPath); err != nil {
		if err2 := tusCopyFile(path, blobPath); err2 != nil {
			writeError(w, err2)
			return
		}
		_ = os.Remove(path)
	}
	h.uploads.Delete(req.UploadID)

	now := time.Now().UTC()
	img := api.BaseImage{
		ID:        imgID,
		Name:      name,
		Status:    api.ImageStatusBuilding,
		Format:    api.ImageFormatBlock,
		Tags:      []string{},
		CreatedAt: now,
	}
	if err := h.DB.CreateBaseImage(r.Context(), img); err != nil {
		writeError(w, err)
		return
	}

	go h.finalizeUpload(imgID, blobPath, req.ExpectedSHA256)

	if h.Audit != nil {
		aID, aLabel := "", ""
		if h.GetActorInfo != nil {
			aID, aLabel = h.GetActorInfo(r)
		}
		h.Audit.Record(r.Context(), aID, aLabel, db.AuditActionImageCreate, "image", imgID,
			r.RemoteAddr, nil, map[string]string{"name": img.Name, "upload_id": req.UploadID})
	}
	if h.ImageEvents != nil {
		imgCopy := img
		h.ImageEvents.Publish(api.ImageEvent{Kind: api.ImageEventCreated, Image: &imgCopy, ID: img.ID})
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{"id": imgID, "status": img.Status})
}

func (h *TUSHandler) finalizeUpload(imageID, blobPath, expectedSHA256 string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := h.DB.SetBlobPath(ctx, imageID, blobPath); err != nil {
		h.failUpload(imageID, err)
		return
	}

	sum, size, err := tusComputeSHA256(blobPath)
	if err != nil {
		h.failUpload(imageID, err)
		return
	}
	if expectedSHA256 != "" && !strings.EqualFold(expectedSHA256, sum) {
		_ = os.Remove(blobPath)
		h.failUpload(imageID, fmt.Errorf("sha256 mismatch: expected %s, got %s", expectedSHA256, sum))
		return
	}
	if err := h.DB.FinalizeBaseImage(ctx, imageID, size, sum); err != nil {
		h.failUpload(imageID, err)
		return
	}
	if updated, err := h.DB.GetBaseImage(ctx, imageID); err == nil && h.ImageEvents != nil {
		h.ImageEvents.Publish(api.ImageEvent{Kind: api.ImageEventFinalized, Image: &updated, ID: updated.ID})
	}
}

func (h *TUSHandler) failUpload(imageID string, reason error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.DB.UpdateBaseImageStatus(ctx, imageID, api.ImageStatusError, reason.Error())
	if h.ImageEvents != nil {
		h.ImageEvents.Publish(api.ImageEvent{Kind: api.ImageEventUpdated, ID: imageID})
	}
	log.Error().Err(reason).Str("image_id", imageID).Msg("from-upload: finalize failed")
}

// tusNewID generates a new random ID for an image.
func tusNewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// tusComputeSHA256 reads the file at path and returns (hex-sha256, sizeBytes, error).
func tusComputeSHA256(path string) (string, int64, error) {
	f, err := os.Open(path) //#nosec G304 — path is server-controlled
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// tusCopyFile copies src to dst as a fallback when os.Rename fails.
func tusCopyFile(src, dst string) error {
	in, err := os.Open(src) //#nosec G304 — server-controlled paths
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
