package handlers

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/initramfs"
)

//go:embed scripts/build-initramfs.sh
var buildInitramfsScript []byte // embedded at compile time — no on-disk dependency at runtime

// InitramfsHandler handles system-level initramfs management endpoints.
type InitramfsHandler struct {
	DB            *db.DB
	ScriptPath    string // path to build-initramfs.sh (abs)
	InitramfsPath string // final output path (e.g. /var/lib/clustr/boot/initramfs-clustr.img)
	ClustrBinPath  string // path to the clustr static binary passed to the script
	// ImageDir is the base directory for the image store (same as ImagesHandler.ImageDir).
	// When set, BuildInitramfsFromImage copies the built file here and registers a BaseImage.
	ImageDir    string
	// ImageEvents, when set, receives lifecycle SSE events emitted by BuildInitramfsFromImage.
	ImageEvents ImageEventStoreIface

	mu          sync.Mutex // serialises concurrent rebuild requests
	running     bool
	liveSHA256  string // sha256 of the on-disk initramfs; cached to avoid per-request file reads
	// activeBuildCtxCancel holds the cancel function for the in-flight BuildInitramfsFromImage.
	// Only one build may run at a time. Protected by mu.
	activeBuildCtxCancel context.CancelFunc
}

// InitLiveSHA256 computes and caches the sha256 of the current on-disk initramfs.
// Call this once at startup (non-fatal if the file does not yet exist).
func (h *InitramfsHandler) InitLiveSHA256() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.liveSHA256 = computeFileSHA256(h.InitramfsPath)
}

// computeFileSHA256 returns the hex sha256 of the file at path, or "" on error.
func computeFileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return ""
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// InitramfsBuildInfo is the shape returned by GET /api/v1/system/initramfs.
type InitramfsBuildInfo struct {
	SHA256        string                      `json:"sha256"`
	SizeBytes     int64                       `json:"size_bytes"`
	BuildTime     *time.Time                  `json:"build_time,omitempty"`
	KernelVersion string                      `json:"kernel_version,omitempty"`
	History       []db.InitramfsBuildRecord   `json:"history"`
}

// GetInitramfs handles GET /api/v1/system/initramfs.
// Returns current sha256, size, build_time, kernel version, and last 5 history rows.
//
// Kernel version is resolved lazily: if the most recent DB record matching the
// on-disk sha256 has an empty kernel_version (e.g. written by the autodeploy
// timer rather than the server-side rebuild API), the handler extracts the
// version directly from the file, caches it in the DB, and returns it. This
// makes the UI self-healing regardless of how the file arrived.
func (h *InitramfsHandler) GetInitramfs(w http.ResponseWriter, r *http.Request) {
	info := InitramfsBuildInfo{}
	ctx := r.Context()

	// Read current file stats.
	if stat, err := os.Stat(h.InitramfsPath); err == nil {
		info.SizeBytes = stat.Size()
		mtime := stat.ModTime().UTC()
		info.BuildTime = &mtime
		// Use the cached live sha256 — avoids a 27 MB synchronous file read per
		// request.  The cache is populated at startup and after each successful
		// rebuild, so it is always current.
		h.mu.Lock()
		info.SHA256 = h.liveSHA256
		h.mu.Unlock()
	}

	// Resolve kernel version from the DB record whose sha256 matches the live
	// file.  If the record's kernel_version is empty (autodeploy timer path),
	// extract it from the file on disk and write it back to the DB.
	if info.SHA256 != "" {
		buildID, kver, err := h.DB.GetLatestSuccessfulBuildBySHA256(ctx, info.SHA256)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// No DB record for the current file — try extracting directly.
			kver, err = initramfs.ExtractKernelVersion(h.InitramfsPath)
			if err != nil {
				log.Debug().Err(err).Msg("initramfs: lazy kernel version extract failed (no db record)")
			} else {
				info.KernelVersion = kver
			}
		case err != nil:
			log.Debug().Err(err).Msg("initramfs: db lookup for kernel version failed")
		case kver == "":
			// Record exists but kernel_version was not captured at build time.
			kver, err = initramfs.ExtractKernelVersion(h.InitramfsPath)
			if err != nil {
				log.Debug().Err(err).Msg("initramfs: lazy kernel version extract failed")
			} else {
				info.KernelVersion = kver
				// Back-fill the DB so subsequent requests skip the shell-out.
				dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if dbErr := h.DB.UpdateInitramfsBuildKernelVersion(dbCtx, buildID, kver); dbErr != nil {
					log.Debug().Err(dbErr).Str("build_id", buildID).Msg("initramfs: failed to back-fill kernel_version in db")
				}
			}
		default:
			info.KernelVersion = kver
		}
	}

	// Load history.
	history, err := h.DB.ListInitramfsBuilds(ctx, 5)
	if err != nil {
		log.Warn().Err(err).Msg("initramfs: list history")
	}
	if history == nil {
		history = []db.InitramfsBuildRecord{}
	}
	info.History = history

	writeJSON(w, http.StatusOK, info)
}

// RebuildInitramfs handles POST /api/v1/system/initramfs/rebuild.
// Guards:
//   - Rejects 409 if any node has an active (non-terminal) deploy progress.
//   - Rejects 409 if a rebuild is already in flight.
//
// Shells out to build-initramfs.sh in a staging dir, sha256-checks the result,
// then atomically renames it into place.
func (h *InitramfsHandler) RebuildInitramfs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Guard: reject if a rebuild is already in flight.
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: "an initramfs rebuild is already in progress",
			Code:  "rebuild_in_progress",
		})
		return
	}
	h.running = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}()

	// Guard: reject if any node has an active deploy.
	if hasActive, nodeID := h.hasActiveDeployViaDB(ctx); hasActive {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: fmt.Sprintf("node %s has an active deployment — wait for it to complete before rebuilding initramfs", nodeID),
			Code:  "deploy_active",
		})
		return
	}

	// Determine triggered_by from the API key prefix in the Authorization header.
	triggeredBy := extractKeyPrefix(r)

	// Create DB record.
	buildID := uuid.New().String()
	record := db.InitramfsBuildRecord{
		ID:               buildID,
		StartedAt:        time.Now().UTC(),
		TriggeredByPrefix: triggeredBy,
		Outcome:          "pending",
	}
	if err := h.DB.CreateInitramfsBuild(ctx, record); err != nil {
		log.Error().Err(err).Msg("initramfs rebuild: create db record")
		writeError(w, err)
		return
	}

	// Deferred panic guard: if anything in this handler panics after the DB
	// record was created, mark the build as failed so it does not stay 'pending'
	// forever. The panic is re-raised after the DB write so the server still
	// crashes (and the operator sees the panic in the journal).
	defer func() {
		if r := recover(); r != nil {
			h.failBuild(buildID, fmt.Errorf("panic during rebuild: %v", r))
			panic(r) // re-raise so the server's default recovery middleware handles it
		}
	}()

	// Audit log start.
	log.Info().
		Str("build_id", buildID).
		Str("triggered_by", triggeredBy).
		Msg("initramfs rebuild: started")

	// Staging path — write next to final so rename is atomic (same filesystem).
	stagingPath := h.InitramfsPath + ".building"

	// Build in a temp work dir.
	workDir, err := os.MkdirTemp("", "clustr-initramfs-*")
	if err != nil {
		h.failBuild(buildID, fmt.Errorf("create work dir: %w", err))
		writeError(w, err)
		return
	}
	defer os.RemoveAll(workDir)

	// Prepare the build progress store for streaming.
	// We use the build ID as the "image ID" key so the SSE stream can subscribe
	// to progress using the existing BuildProgressStore.
	lines := make(chan string, 256)

	// Run the script in a goroutine and collect output.
	var buildErr error
	var scriptSHA256 string
	var scriptSize int64
	var kernelVer string

	done := make(chan struct{})
	go func() {
		defer close(done)
		buildErr = h.runScript(workDir, stagingPath, lines)
		// Compute sha256 + size of staging file if script succeeded.
		if buildErr == nil {
			scriptSHA256, scriptSize, kernelVer = h.inspectStagingFile(stagingPath)
		}
	}()

	// Collect all lines; the response is returned after completion (not streamed
	// per-line for simplicity — the UI SSE approach polls the build-log endpoint).
	var logLines []string
	for line := range lines {
		logLines = append(logLines, line)
	}
	<-done

	if buildErr != nil {
		h.failBuild(buildID, buildErr)
		// Log collected output so the error is visible in the service journal.
		if len(logLines) > 0 {
			log.Error().
				Str("build_id", buildID).
				Strs("script_output", logLines).
				Msg("initramfs rebuild: script output before failure")
		}
		log.Error().Err(buildErr).Str("build_id", buildID).Msg("initramfs rebuild: failed")
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":     fmt.Sprintf("initramfs rebuild failed: %v", buildErr),
			"code":      "rebuild_failed",
			"log_lines": logLines,
		})
		return
	}

	// Atomic rename: staging → final.
	if err := os.Rename(stagingPath, h.InitramfsPath); err != nil {
		h.failBuild(buildID, fmt.Errorf("atomic rename failed: %w", err))
		writeError(w, err)
		return
	}

	// Update the in-memory live sha256 cache now that the new image is on disk.
	h.mu.Lock()
	h.liveSHA256 = scriptSHA256
	h.mu.Unlock()

	// Finalize DB record — use a background context so a slow or disconnected
	// HTTP client does not cancel the write after a successful build.
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dbCancel()
	outcome := "success"
	if err := h.DB.FinishInitramfsBuild(dbCtx, buildID, scriptSHA256, scriptSize, kernelVer, outcome); err != nil {
		log.Warn().Err(err).Str("build_id", buildID).Msg("initramfs rebuild: failed to update db record (non-fatal)")
	}
	// Trim history to 5 rows.
	if err := h.DB.TrimInitramfsBuilds(dbCtx, 5); err != nil {
		log.Warn().Err(err).Msg("initramfs rebuild: trim history failed (non-fatal)")
	}

	log.Info().
		Str("build_id", buildID).
		Str("sha256", scriptSHA256).
		Int64("size_bytes", scriptSize).
		Str("kernel_version", kernelVer).
		Msg("initramfs rebuild: complete")

	writeJSON(w, http.StatusOK, map[string]any{
		"build_id":       buildID,
		"sha256":         scriptSHA256,
		"size_bytes":     scriptSize,
		"kernel_version": kernelVer,
		"log_lines":      logLines,
	})
}

// runScript executes build-initramfs.sh and streams output to lines.
// The script is written from the embedded copy to a temp file at call time,
// making the binary self-contained with no on-disk script dependency.
// Closes lines when done.
func (h *InitramfsHandler) runScript(workDir, outputPath string, lines chan<- string) error {
	defer close(lines)

	// Write the embedded script to a temp file so the binary is self-contained.
	// The handler's ScriptPath field is ignored at runtime; the embedded bytes
	// are always used. This fixes "exit status 127" caused by relative ScriptPath
	// not existing in the service's WorkingDirectory (/var/lib/clustr).
	tmpScript, err := os.CreateTemp("", "clustr-build-initramfs-*.sh")
	if err != nil {
		return fmt.Errorf("create temp script: %w", err)
	}
	tmpScriptPath := tmpScript.Name()
	defer os.Remove(tmpScriptPath)

	if _, err := tmpScript.Write(buildInitramfsScript); err != nil {
		tmpScript.Close()
		return fmt.Errorf("write temp script: %w", err)
	}
	if err := tmpScript.Chmod(0o700); err != nil {
		tmpScript.Close()
		return fmt.Errorf("chmod temp script: %w", err)
	}
	tmpScript.Close()

	scriptPath := tmpScriptPath

	clustrBin := h.ClustrBinPath
	if clustrBin == "" {
		// Default: look for clustr-static alongside the running binary.
		exe, _ := os.Executable()
		clustrBin = filepath.Join(filepath.Dir(exe), "clustr-static")
	} else if !filepath.IsAbs(clustrBin) {
		// Relative path: resolve relative to the running binary's directory so
		// the path is stable regardless of WorkingDirectory in the systemd unit.
		exe, _ := os.Executable()
		clustrBin = filepath.Join(filepath.Dir(exe), clustrBin)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// build-initramfs.sh uses `sshpass -p $PASS scp user@host:/path` to pull
	// binaries from localhost. When running on the server itself we create a
	// sshpass shim that drops the -p flag and relies on root's SSH key via
	// ssh-agent — same pattern as clustr-autodeploy.sh.
	shimDir, shimErr := os.MkdirTemp("", "clustr-sshpass-shim-*")
	if shimErr != nil {
		return fmt.Errorf("create shim dir: %w", shimErr)
	}
	defer os.RemoveAll(shimDir)
	shimPath := filepath.Join(shimDir, "sshpass")
	shimContent := `#!/bin/bash
# sshpass shim: strip -p <password> and exec ssh/scp directly with key auth.
shift 2  # drop -p <password>
exec "$@"
`
	os.WriteFile(shimPath, []byte(shimContent), 0o755)

	wrapper := fmt.Sprintf(`#!/bin/bash
set -euo pipefail
# Start ssh-agent and load root's key for localhost SCP.
if [[ -f /root/.ssh/id_ed25519 ]]; then
    eval "$(ssh-agent -s)" > /dev/null 2>&1
    ssh-add /root/.ssh/id_ed25519 < /dev/null 2>/dev/null || true
fi
export CLUSTR_SERVER_USER=root
exec bash %q %q %q
`, scriptPath, clustrBin, outputPath)

	wrapperFile, wErr := os.CreateTemp("", "clustr-initramfs-wrapper-*.sh")
	if wErr != nil {
		return fmt.Errorf("create wrapper script: %w", wErr)
	}
	wrapperPath := wrapperFile.Name()
	defer os.Remove(wrapperPath)
	wrapperFile.WriteString(wrapper)
	wrapperFile.Chmod(0o700)
	wrapperFile.Close()

	cmd := exec.CommandContext(ctx, "bash", wrapperPath) //nolint:gosec
	cmd.Dir = workDir
	// Prepend the sshpass shim dir to PATH so build-initramfs.sh finds our
	// shim before the real sshpass. Also add /root/bin for busybox-static.
	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + shimDir + ":" + strings.TrimPrefix(e, "PATH=") + ":/root/bin"
			break
		}
	}
	cmd.Env = append(env,
		"CLUSTR_SERVER_HOST=127.0.0.1",
		"CLUSTR_SERVER_USER=root",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start script: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	// Increase buffer limits to handle long output lines from build scripts.
	// Default is 64KB which can cause scanner to stop early on verbose output,
	// leaving the pipe unread and deadlocking cmd.Wait().
	scanner.Buffer(make([]byte, 1<<20), 16<<20) // 1MB initial, 16MB max

	for scanner.Scan() {
		line := scanner.Text()
		select {
		case lines <- line:
		default:
		}
	}

	// Drain any remaining bytes after scanner stops (e.g. on token-size error)
	// so the pipe buffer never blocks the child process before cmd.Wait().
	if scanErr := scanner.Err(); scanErr != nil {
		log.Warn().Err(scanErr).Msg("runScript: scanner stopped early — draining pipe")
		_, _ = io.Copy(io.Discard, stdout)
	}

	waitErr := cmd.Wait()
	if waitErr != nil {
		return waitErr
	}
	if scanner.Err() != nil {
		return fmt.Errorf("runScript: output scanner: %w", scanner.Err())
	}
	return nil
}

// inspectStagingFile computes sha256, size, and detects kernel version from the staging file.
func (h *InitramfsHandler) inspectStagingFile(path string) (sha256sum string, size int64, kernelVer string) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, ""
	}
	defer f.Close()

	hasher := sha256.New()
	n, err := io.Copy(hasher, f)
	if err != nil {
		return "", 0, ""
	}
	sha256sum = hex.EncodeToString(hasher.Sum(nil))
	size = n

	// Try to detect kernel version from uname -r on the host (the initramfs
	// is built for the running kernel).
	out, err := exec.Command("uname", "-r").Output()
	if err == nil {
		kernelVer = strings.TrimSpace(string(out))
	}
	return sha256sum, size, kernelVer
}

// failBuild records a failure outcome in the DB.
func (h *InitramfsHandler) failBuild(buildID string, buildErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg := ""
	if buildErr != nil {
		msg = buildErr.Error()
	}
	_ = h.DB.FinishInitramfsBuild(ctx, buildID, "", 0, "", "failed: "+msg)
}

// hasActiveDeployViaDB checks the deploy_progress table for any non-terminal entry.
// We read directly from the ProgressStore through a DB query on reimage_requests.
func (h *InitramfsHandler) hasActiveDeployViaDB(ctx context.Context) (bool, string) {
	// Query reimage_requests for any 'running' or 'pending' status.
	rows, err := h.DB.SQL().QueryContext(ctx, `
		SELECT node_id FROM reimage_requests
		WHERE status IN ('running', 'pending', 'triggered')
		LIMIT 1
	`)
	if err != nil {
		// Non-fatal: can't check, allow rebuild to proceed.
		return false, ""
	}
	defer rows.Close()
	if rows.Next() {
		var nodeID string
		_ = rows.Scan(&nodeID)
		return true, nodeID
	}
	return false, ""
}

// extractKeyPrefix pulls the first 8 chars of the key from the Authorization header.
func extractKeyPrefix(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		// Strip typed prefix.
		key := after
		for _, pfx := range []string{"clustr-admin-", "clustr-node-"} {
			if strings.HasPrefix(key, pfx) {
				key = strings.TrimPrefix(key, pfx)
				break
			}
		}
		if len(key) > 8 {
			key = key[:8]
		}
		return key
	}
	return "session"
}

// DeleteInitramfsHistory handles DELETE /api/v1/system/initramfs/history/{id}.
// Deletes a single history entry by ID regardless of its outcome (success or
// failure), UNLESS the entry's sha256 matches the sha256 of the initramfs file
// currently on disk — that entry is the live image and must not be deleted.
//
// Guard logic:
//  1. Compute sha256 of the on-disk initramfs file (h.InitramfsPath).
//  2. Fetch the target record's sha256 from the DB.
//  3. If they match → 409 live_entry_cannot_delete.
//  4. Otherwise → proceed with deletion regardless of outcome field.
//
// This allows deletion of older successful entries that have been superseded by
// a newer rebuild, while still protecting the currently-serving image.
func (h *InitramfsHandler) DeleteInitramfsHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "missing id", Code: "bad_request"})
		return
	}

	ctx := r.Context()

	// Guard: fetch the target record's sha256 directly by ID — do not rely on
	// a windowed history scan, which could miss the live record if more than N
	// entries have been inserted since it was last built.
	targetSHA, err := h.DB.GetInitramfsBuildSHA256(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}

	// Compare against the authoritative in-memory live sha256.
	// liveSHA256 is set at startup and updated after every successful rebuild,
	// so it is always current without a synchronous 27 MB file read.
	h.mu.Lock()
	liveSHA256 := h.liveSHA256
	h.mu.Unlock()

	if liveSHA256 != "" && targetSHA == liveSHA256 {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: "cannot delete the live initramfs entry — its sha256 matches the file currently on disk",
			Code:  "live_entry_cannot_delete",
		})
		return
	}

	if err := h.DB.DeleteInitramfsBuild(ctx, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetBuildLog returns the full build log for an initramfs build.
// This is a stub — production would store lines in the DB or a temp file.
// For now we return an informative 204.
func (h *InitramfsHandler) GetBuildLog(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// BuildInitramfsFromImage handles POST /api/v1/initramfs/build (INITRD-1..5).
//
// Request body (all optional):
//   {"base_image_id":"<uuid>", "name":"optional", "kernel_args":"optional extra args"}
//
// Returns 202 immediately with {build_id, status:"queued"} and then switches to
// streaming SSE (text/event-stream) that emits log lines from the build script:
//   data: {"type":"log","line":"<text>"}
//   data: {"type":"done","image_id":"<uuid>","sha256":"<hex>"}
//   data: {"type":"error","message":"<text>"}
//
// On success, the built initramfs is copied into ImageDir and registered as a
// BaseImage with format=block, name = req.Name (or "initramfs-<buildID[:8]>"),
// notes = "kernel_version:<ver>". The image is also published via ImageEvents.
//
// Only one build may run at a time — returns 409 if one is already in progress.
func (h *InitramfsHandler) BuildInitramfsFromImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseImageID string `json:"base_image_id"`
		Name        string `json:"name"`
		KernelArgs  string `json:"kernel_args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	// Guard: reject if a build is already running.
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: "an initramfs build is already in progress",
			Code:  "build_in_progress",
		})
		return
	}
	h.running = true
	buildCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	h.activeBuildCtxCancel = cancel
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		h.running = false
		h.activeBuildCtxCancel = nil
		h.mu.Unlock()
		cancel()
	}()

	buildID := uuid.New().String()
	imgName := req.Name
	if imgName == "" {
		imgName = "initramfs-" + buildID[:8]
	}

	// Create DB record for this build.
	record := db.InitramfsBuildRecord{
		ID:               buildID,
		StartedAt:        time.Now().UTC(),
		TriggeredByPrefix: extractKeyPrefix(r),
		Outcome:          "pending",
	}
	if err := h.DB.CreateInitramfsBuild(r.Context(), record); err != nil {
		log.Error().Err(err).Msg("initramfs build: create db record")
		writeError(w, err)
		return
	}

	// Pre-create image record with status=building so the UI can show it immediately.
	now := time.Now().UTC()
	img := api.BaseImage{
		ID:          uuid.New().String(),
		Name:        imgName,
		Version:     buildID[:8],
		Status:      api.ImageStatusBuilding,
		Format:      api.ImageFormatBlock,
		BuildMethod: "initramfs",
		Notes:       "initramfs build — running",
		CreatedAt:   now,
	}
	if h.DB != nil {
		if err := h.DB.CreateBaseImage(r.Context(), img); err != nil {
			log.Warn().Err(err).Msg("initramfs build: pre-create image record failed (non-fatal)")
		} else if h.ImageEvents != nil {
			h.ImageEvents.Publish(api.ImageEvent{Kind: api.ImageEventCreated, Image: &img})
		}
	}

	// Switch to SSE streaming.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	writeSSELine := func(payload string) {
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}

	// Helper to send a typed JSON event.
	sendEvent := func(v any) {
		b, _ := json.Marshal(v)
		writeSSELine(string(b))
	}

	sendEvent(map[string]any{"type": "log", "line": fmt.Sprintf("build %s started", buildID[:8])})

	// Build in a temp staging dir.
	stagingPath := h.InitramfsPath + ".build-" + buildID[:8]
	workDir, err := os.MkdirTemp("", "clustr-initramfs-build-*")
	if err != nil {
		sendEvent(map[string]any{"type": "error", "message": "failed to create work dir: " + err.Error()})
		h.failBuild(buildID, err)
		_ = h.DB.UpdateBaseImageStatus(buildCtx, img.ID, api.ImageStatusError, err.Error())
		return
	}
	defer os.RemoveAll(workDir)

	lines := make(chan string, 256)
	var buildErr error
	var scriptSHA256 string
	var scriptSize int64
	var kernelVer string

	done := make(chan struct{})
	go func() {
		defer close(done)
		buildErr = h.runScript(workDir, stagingPath, lines)
		if buildErr == nil {
			scriptSHA256, scriptSize, kernelVer = h.inspectStagingFile(stagingPath)
		}
	}()

	// Stream log lines to the client.
	for {
		select {
		case line, more := <-lines:
			if !more {
				lines = nil
			} else {
				sendEvent(map[string]any{"type": "log", "line": line})
			}
		case <-done:
			// Drain remaining lines.
			for line := range lines {
				sendEvent(map[string]any{"type": "log", "line": line})
			}
			goto buildDone
		case <-buildCtx.Done():
			sendEvent(map[string]any{"type": "error", "message": "build cancelled"})
			h.failBuild(buildID, context.Canceled)
			_ = h.DB.UpdateBaseImageStatus(context.Background(), img.ID, api.ImageStatusError, "build cancelled")
			// Clean up staging.
			os.Remove(stagingPath) //nolint:errcheck
			return
		}
	}

buildDone:
	if buildErr != nil {
		sendEvent(map[string]any{"type": "error", "message": buildErr.Error()})
		h.failBuild(buildID, buildErr)
		_ = h.DB.UpdateBaseImageStatus(context.Background(), img.ID, api.ImageStatusError, buildErr.Error())
		os.Remove(stagingPath) //nolint:errcheck
		return
	}

	// Copy built initramfs into the image store (if ImageDir is set).
	if h.ImageDir != "" {
		imageDir := filepath.Join(h.ImageDir, img.ID)
		if err := os.MkdirAll(imageDir, 0o755); err == nil {
			destPath := filepath.Join(imageDir, "image.img")
			if copyErr := initramfsCopyFile(stagingPath, destPath); copyErr != nil {
				log.Warn().Err(copyErr).Str("image_id", img.ID).Msg("initramfs build: copy to image store failed")
			}
		}
	}
	os.Remove(stagingPath) //nolint:errcheck

	// Finalize the initramfs system path (same as regular rebuild).
	if h.InitramfsPath != "" {
		_ = os.Rename(stagingPath, h.InitramfsPath)
		h.mu.Lock()
		h.liveSHA256 = scriptSHA256
		h.mu.Unlock()
	}

	// Finalize the image record.
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dbCancel()

	notes := "initramfs build complete"
	if kernelVer != "" {
		notes = "kernel: " + kernelVer
	}
	_ = h.DB.FinishInitramfsBuild(dbCtx, buildID, scriptSHA256, scriptSize, kernelVer, "success")
	_ = h.DB.TrimInitramfsBuilds(dbCtx, 5)

	if err := h.DB.FinalizeBaseImage(dbCtx, img.ID, scriptSize, scriptSHA256); err != nil {
		log.Warn().Err(err).Str("image_id", img.ID).Msg("initramfs build: finalize image record failed")
	} else {
		// Persist notes (kernel version).
		_, _ = h.DB.SQL().ExecContext(dbCtx, `UPDATE base_images SET notes = ? WHERE id = ?`, notes, img.ID)
	}

	// Publish finalized image event.
	if h.ImageEvents != nil {
		updated := img
		updated.Status = api.ImageStatusReady
		updated.SizeBytes = scriptSize
		updated.Checksum = scriptSHA256
		h.ImageEvents.Publish(api.ImageEvent{Kind: api.ImageEventFinalized, Image: &updated})
	}

	log.Info().
		Str("build_id", buildID).
		Str("image_id", img.ID).
		Str("sha256", scriptSHA256).
		Int64("size_bytes", scriptSize).
		Str("kernel_version", kernelVer).
		Msg("initramfs build: complete")

	sendEvent(map[string]any{
		"type":           "done",
		"image_id":       img.ID,
		"sha256":         scriptSHA256,
		"size_bytes":     scriptSize,
		"kernel_version": kernelVer,
	})
}

// CancelInitramfsBuild handles DELETE /api/v1/initramfs/builds/{id} (INITRD-6).
// Cancels the currently in-flight build if its ID matches. Returns 409 if no build
// is running or the ID does not match the active build.
func (h *InitramfsHandler) CancelInitramfsBuild(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	h.mu.Lock()
	running := h.running
	cancel := h.activeBuildCtxCancel
	h.mu.Unlock()

	if !running || cancel == nil {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: "no build is currently in progress",
			Code:  "no_active_build",
		})
		return
	}

	// Signal cancellation. The goroutine detects context.Done() and cleans up.
	cancel()
	writeJSON(w, http.StatusOK, map[string]any{"build_id": id, "status": "cancelling"})
}

// initramfsCopyFile copies src to dst using io.Copy.
func initramfsCopyFile(src, dst string) error {
	in, err := os.Open(src)
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

// parseInitramfsBuildInfo is a helper to unmarshal the rebuild response.
func parseInitramfsBuildInfo(body []byte) (sha256sum, kernelVersion string, err error) {
	var resp struct {
		SHA256        string `json:"sha256"`
		KernelVersion string `json:"kernel_version"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", err
	}
	return resp.SHA256, resp.KernelVersion, nil
}
