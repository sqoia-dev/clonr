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
	"strconv"
	"strings"
	"sync"
	"syscall"
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

//go:embed scripts/initramfs-init.sh
var initramfsInitScript []byte // init template substituted by build-initramfs.sh via sed

// buildSessionRingCap is the maximum number of log lines retained per build.
// Oldest lines are silently dropped when the buffer is full.
const buildSessionRingCap = 10000

// BuildSession holds the in-memory state of a single background initramfs build.
// It survives the HTTP handler lifecycle — SSE clients can reconnect and replay
// buffered lines, and the build goroutine finalises the DB record regardless of
// whether any client is connected.
type BuildSession struct {
	buildID string

	mu       sync.Mutex
	lines    []string // ring buffer; capped at buildSessionRingCap
	done     bool     // true once the goroutine has exited (success or failure)
	outcome  string   // final outcome string (empty until done)
	newLine  chan struct{} // closed-and-replaced on each append; SSE readers select on it
	cancel   context.CancelFunc
}

// appendLine adds a log line to the ring buffer and signals any waiting readers.
func (s *BuildSession) appendLine(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lines) >= buildSessionRingCap {
		// Drop oldest: shift left by 1.
		copy(s.lines, s.lines[1:])
		s.lines[len(s.lines)-1] = line
	} else {
		s.lines = append(s.lines, line)
	}
	// Wake SSE readers.
	close(s.newLine)
	s.newLine = make(chan struct{})
}

// snapshot returns all currently buffered lines and the channel to wait on for the
// next line. Callers should: read snapshot, wait on notify, repeat.
func (s *BuildSession) snapshot() (lines []string, notify <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.lines))
	copy(cp, s.lines)
	return cp, s.newLine
}

// markDone sets the done flag and wakes any waiting SSE readers.
func (s *BuildSession) markDone(outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	s.outcome = outcome
	close(s.newLine)
	s.newLine = make(chan struct{}) // replace so subsequent selects don't panic
}

// isDone reports whether the build has finished.
func (s *BuildSession) isDone() (done bool, outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done, s.outcome
}

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
	// activeBuildID is the ID of the currently running BuildInitramfsFromImage build.
	// Protected by mu.
	activeBuildID string
	// sessions maps build ID → active BuildSession for in-flight builds.
	// Protected by mu.
	sessions map[string]*BuildSession
}

// IsRunning reports whether an initramfs build is currently in flight.
// Safe to call from any goroutine.
func (h *InitramfsHandler) IsRunning() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running
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
		// Log line count only — raw script output may contain credentials (e.g.
		// sshpass invocations) and must not be written to structured logs or
		// returned in error responses. Retrieve full output from the build log
		// endpoint (GET /api/v1/initramfs/builds/{id}/log) which requires auth.
		log.Error().Err(buildErr).
			Str("build_id", buildID).
			Int("script_output_lines", len(logLines)).
			Msg("initramfs rebuild: failed")
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":    fmt.Sprintf("initramfs rebuild failed: %v", buildErr),
			"code":     "rebuild_failed",
			"build_id": buildID,
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

	// Auto-place initramfs and kernel into the boot directory.
	bootDir := filepath.Dir(h.InitramfsPath)
	sizeMiB := float64(scriptSize) / (1024 * 1024)
	logLines = append(logLines, fmt.Sprintf("Built initramfs (%.1f MiB) — placed at %s", sizeMiB, h.InitramfsPath))

	kver, placed, kernelErr := ensureKernelPlaced(bootDir)
	if kernelErr != nil {
		log.Warn().Err(kernelErr).Msg("initramfs rebuild: kernel auto-place failed (non-fatal)")
		logLines = append(logLines, "Warning: kernel auto-place failed: "+kernelErr.Error())
	} else if placed {
		logLines = append(logLines, fmt.Sprintf("Kernel placed at %s/vmlinuz (%s)", bootDir, kver))
		logLines = append(logLines, "Ready to PXE boot.")
	} else {
		logLines = append(logLines, fmt.Sprintf("Kernel at %s/vmlinuz already current (%s)", bootDir, kver))
		logLines = append(logLines, "Ready to PXE boot.")
	}

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

// buildScriptCmd creates the exec.Cmd for the initramfs build wrapper script.
// Shared by runScript and runScriptPgid.
func (h *InitramfsHandler) buildScriptCmd(workDir, outputPath string) (*exec.Cmd, string, []string, error) {
	// Write the embedded script to a temp file so the binary is self-contained.
	// The handler's ScriptPath field is ignored at runtime; the embedded bytes
	// are always used. This fixes "exit status 127" caused by relative ScriptPath
	// not existing in the service's WorkingDirectory (/var/lib/clustr).
	//
	// Both scripts are written into a private temp directory so that
	// $(dirname "$0")/initramfs-init.sh resolves correctly — build-initramfs.sh
	// references its sibling init template via that relative path.
	scriptDir, err := os.MkdirTemp("", "clustr-build-initramfs-*")
	if err != nil {
		return nil, "", nil, fmt.Errorf("create script dir: %w", err)
	}

	tmpScriptPath := filepath.Join(scriptDir, "build-initramfs.sh")
	if err := os.WriteFile(tmpScriptPath, buildInitramfsScript, 0o700); err != nil {
		os.RemoveAll(scriptDir)
		return nil, "", nil, fmt.Errorf("write temp script: %w", err)
	}

	// build-initramfs.sh uses $(dirname "$0")/initramfs-init.sh for the PID-1
	// init template. Place the embedded copy alongside the main script.
	initTemplatePath := filepath.Join(scriptDir, "initramfs-init.sh")
	if err := os.WriteFile(initTemplatePath, initramfsInitScript, 0o600); err != nil {
		os.RemoveAll(scriptDir)
		return nil, "", nil, fmt.Errorf("write initramfs-init.sh: %w", err)
	}

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

	// build-initramfs.sh uses `sshpass -p $PASS scp user@host:/path` to pull
	// binaries from localhost. When running on the server itself we create a
	// sshpass shim that drops the -p flag and relies on root's SSH key via
	// ssh-agent — same pattern as clustr-autodeploy.sh.
	shimDir, shimErr := os.MkdirTemp("", "clustr-sshpass-shim-*")
	if shimErr != nil {
		os.RemoveAll(scriptDir)
		return nil, "", nil, fmt.Errorf("create shim dir: %w", shimErr)
	}

	shimPath := filepath.Join(shimDir, "sshpass")
	shimContent := `#!/bin/bash
# sshpass shim: strip -p <password> and exec ssh/scp directly with key auth.
shift 2  # drop -p <password>
exec "$@"
`
	os.WriteFile(shimPath, []byte(shimContent), 0o755) //nolint:errcheck

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
		os.RemoveAll(scriptDir)
		os.RemoveAll(shimDir)
		return nil, "", nil, fmt.Errorf("create wrapper script: %w", wErr)
	}
	wrapperPath := wrapperFile.Name()
	wrapperFile.WriteString(wrapper)
	wrapperFile.Chmod(0o700)
	wrapperFile.Close()

	cmd := exec.Command("bash", wrapperPath) //nolint:gosec
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

	// Cleanup dirs to remove after the command completes.
	toClean := []string{scriptDir, shimDir, wrapperPath}
	return cmd, wrapperPath, toClean, nil
}

// runScript executes build-initramfs.sh and streams output to lines.
// The script is written from the embedded copy to a temp file at call time,
// making the binary self-contained with no on-disk script dependency.
// Closes lines when done.
//
// Used by RebuildInitramfs (synchronous path). For the async path, use
// runScriptPgid which adds process-group tracking for BUG-16 protection.
func (h *InitramfsHandler) runScript(workDir, outputPath string, lines chan<- string) error {
	defer close(lines)

	cmd, _, toClean, err := h.buildScriptCmd(workDir, outputPath)
	if err != nil {
		return err
	}
	defer func() {
		for _, p := range toClean {
			os.RemoveAll(p)
		}
	}()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start script: %w", err)
	}

	// Kill timer: if the process runs past the internal timeout, kill it so
	// cmd.Wait() does not block forever (the pipe-close path below handles
	// the orphan case, but a stuck main process needs this as a backup).
	timer := time.AfterFunc(10*time.Minute, func() {
		if cmd.Process != nil {
			cmd.Process.Kill() //nolint:errcheck
		}
	})
	defer timer.Stop()

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

	// Close the read end of the pipe explicitly before Wait(). This decouples
	// us from any orphaned subprocess that still holds the write end open,
	// preventing cmd.Wait() from blocking indefinitely (BUG-16 pipe-close defense).
	stdout.Close()

	waitErr := cmd.Wait()
	if waitErr != nil {
		return waitErr
	}
	if scanner.Err() != nil {
		return fmt.Errorf("runScript: output scanner: %w", scanner.Err())
	}
	return nil
}

// runScriptPgidAsync is the goroutine body for the async initramfs build path.
// It implements two-layer BUG-16 protection:
//
//  1. Process-group kill after main process exits: the wrapper is started with
//     Setpgid=true so all child processes (ssh-agent, backgrounded helpers) join
//     the same process group. After cmd.Wait() returns (the wrapper exited),
//     SIGKILL is sent to the entire process group to kill any orphaned processes.
//     This ensures they release the pipe's write end, allowing the line-scanner
//     goroutine to see EOF and exit cleanly.
//
//  2. Hard deadline from caller: killPgid is sent to killCh right after cmd.Start()
//     so the caller (runBuildAsync) can force-kill the process group if a hard
//     timeout fires before the build completes.
//
// The critical sequencing insight: the scanner goroutine blocks on the pipe's
// read end until ALL writers close their copy of the write end. A backgrounded
// subprocess (sleep N &) inherits the pipe's write fd. The only way to unblock
// the scanner is to kill the orphan (via SIGKILL to the process group) and then
// close the read end of the pipe.
//
// Protocol:
//   - killCh receives a killPgid func (or nil on start failure) right after cmd.Start().
//   - lines is closed when the function returns.
//   - scriptDone receives the final error when the function returns.
func (h *InitramfsHandler) runScriptPgidAsync(
	workDir, outputPath string,
	lines chan<- string,
	killCh chan<- func(),
	scriptDone chan<- error,
) {
	var finalErr error
	defer func() {
		close(lines)
		scriptDone <- finalErr
	}()

	cmd, _, toClean, err := h.buildScriptCmd(workDir, outputPath)
	if err != nil {
		killCh <- nil
		finalErr = err
		return
	}
	defer func() {
		for _, p := range toClean {
			os.RemoveAll(p)
		}
	}()

	// Setpgid=true: the wrapper and all its children share a new process group
	// whose ID equals the wrapper's PID.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		killCh <- nil
		finalErr = fmt.Errorf("stdout pipe: %w", err)
		return
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		killCh <- nil
		finalErr = fmt.Errorf("start script: %w", err)
		return
	}

	// Build the kill function now that we have a live process.
	pgid := cmd.Process.Pid
	var killOnce sync.Once
	killPgid := func() {
		killOnce.Do(func() {
			// Negative pid sends SIGKILL to the entire process group.
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		})
	}

	// Deliver killPgid to the caller ASAP so it can kill on hard timeout.
	killCh <- killPgid

	// Scan lines from stdout in a goroutine. The goroutine blocks until EOF,
	// which only arrives after all write-end holders close their fd. We unblock
	// it below by killing the process group and then closing stdout.
	scanDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1<<20), 16<<20) // 1MB initial, 16MB max
		for scanner.Scan() {
			line := scanner.Text()
			select {
			case lines <- line:
			default:
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			log.Warn().Err(scanErr).Msg("runScriptPgidAsync: scanner error")
			scanDone <- scanErr
			return
		}
		scanDone <- nil
	}()

	// Wait for the main bash wrapper to exit.
	waitErr := cmd.Wait()

	// BUG-16 core fix: once the main process exits, kill the entire process group
	// to terminate any orphaned subprocesses (ssh-agent, backgrounded sleep, etc.)
	// that still hold the pipe's write end open. Then close our read end.
	// This sequence is the only reliable way to unblock the scanner goroutine.
	killPgid()
	stdout.Close()

	// Wait for the scanner goroutine to finish (it will now see EOF or a closed-pipe
	// error and exit quickly).
	scanErr := <-scanDone

	if waitErr != nil {
		finalErr = waitErr
		return
	}
	// Ignore scanner errors when the script exited cleanly. After stdout.Close()
	// the scanner's next Read() returns a "file already closed" or "use of
	// closed network connection" error — this is expected and should not be
	// treated as a build failure. The script's exit code is authoritative.
	if scanErr != nil && !isPipeClosedError(scanErr) {
		log.Warn().Err(scanErr).Msg("runScriptPgidAsync: unexpected scanner error (non-fatal, script exited cleanly)")
	}
}

// isPipeClosedError reports whether err is a benign "pipe/file already closed"
// error that results from calling stdout.Close() to unblock a scanner goroutine.
// These errors are expected in runScriptPgidAsync's cleanup sequence and should
// not be propagated as build failures.
func isPipeClosedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	// The os package wraps the underlying syscall error in *os.PathError or
	// *net.OpError; the message contains "file already closed" or "use of
	// closed network connection" (the latter from net.Conn-based pipes).
	msg := err.Error()
	return strings.Contains(msg, "file already closed") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "closed pipe") ||
		strings.Contains(msg, "broken pipe")
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

// GetBuildLog returns all buffered log lines for an in-flight or recently
// finished initramfs build. Returns 204 when no session is active for the given
// build ID (e.g. after the server has been restarted and the session is gone).
func (h *InitramfsHandler) GetBuildLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	h.mu.Lock()
	sess, exists := h.sessions[id]
	h.mu.Unlock()

	if !exists {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	lines, _ := sess.snapshot()
	done, outcome := sess.isDone()
	writeJSON(w, http.StatusOK, map[string]any{
		"build_id": id,
		"lines":    lines,
		"done":     done,
		"outcome":  outcome,
	})
}

// BuildInitramfsFromImage handles POST /api/v1/initramfs/build (INITRD-1..5).
//
// Request body (all optional):
//
//	{"base_image_id":"<uuid>", "name":"optional", "kernel_args":"optional extra args"}
//
// Returns 200 immediately with SSE headers, streams log lines from the build script,
// and emits a terminal event when done:
//
//	data: {"type":"log","line":"<text>"}
//	data: {"type":"done","image_id":"<uuid>","sha256":"<hex>"}
//	data: {"type":"error","message":"<text>"}
//
// The build itself runs in a detached background goroutine that outlives the HTTP
// connection. If the client disconnects mid-build the goroutine continues; when a
// new SSE client connects (or the same client reconnects) all buffered lines are
// replayed from the in-memory ring buffer.
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
		activeID := h.activeBuildID
		h.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "an initramfs build is already in progress",
			"code":     "build_in_progress",
			"build_id": activeID,
		})
		return
	}

	buildID := uuid.New().String()
	buildCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	sess := &BuildSession{
		buildID: buildID,
		newLine: make(chan struct{}),
		cancel:  cancel,
	}
	h.running = true
	h.activeBuildID = buildID
	if h.sessions == nil {
		h.sessions = make(map[string]*BuildSession)
	}
	h.sessions[buildID] = sess
	h.mu.Unlock()

	imgName := req.Name
	if imgName == "" {
		imgName = "initramfs-" + buildID[:8]
	}

	// Create DB record for this build.
	record := db.InitramfsBuildRecord{
		ID:                buildID,
		StartedAt:         time.Now().UTC(),
		TriggeredByPrefix: extractKeyPrefix(r),
		Outcome:           "pending",
	}
	if err := h.DB.CreateInitramfsBuild(r.Context(), record); err != nil {
		log.Error().Err(err).Msg("initramfs build: create db record")
		// Clean up the session we just registered.
		h.mu.Lock()
		h.running = false
		h.activeBuildID = ""
		delete(h.sessions, buildID)
		h.mu.Unlock()
		cancel()
		writeError(w, err)
		return
	}

	// Pre-create image record with status=building so the UI shows it immediately.
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

	// Launch the detached background goroutine. It owns its own resources and
	// never reads from the HTTP request after this point.
	// Pass the build timeout so runBuildAsync can enforce it with process-group kill.
	go h.runBuildAsync(buildCtx, cancel, sess, buildID, img, buildInitramfsTimeout())

	// Switch to SSE streaming so the caller can tail the build log.
	h.streamBuildSession(w, r, sess)
}

// buildInitramfsTimeout returns the configured build timeout from the
// CLUSTR_INITRAMFS_BUILD_TIMEOUT environment variable (in minutes), defaulting
// to 30 minutes. This is the hard deadline for the entire build goroutine;
// when it fires the process group is killed so orphaned subprocesses die too.
func buildInitramfsTimeout() time.Duration {
	if s := os.Getenv("CLUSTR_INITRAMFS_BUILD_TIMEOUT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return time.Duration(n) * time.Minute
		}
	}
	return 30 * time.Minute
}

// runBuildAsync is the detached build goroutine. It runs independently of the HTTP
// connection and always finalises the DB record regardless of outcome or panics.
//
// buildTimeout is the hard deadline for the entire build. When it fires, the
// process group is sent SIGKILL so no orphaned subprocess can keep the goroutine
// alive past the deadline.
func (h *InitramfsHandler) runBuildAsync(
	buildCtx context.Context,
	cancel context.CancelFunc,
	sess *BuildSession,
	buildID string,
	img api.BaseImage,
	buildTimeout time.Duration,
) {
	defer cancel()
	defer func() {
		// Clear the running flag and remove the session so the next build can proceed.
		// The session is kept briefly (markDone called first) so any late SSE readers
		// can collect the terminal outcome before it disappears.
		h.mu.Lock()
		h.running = false
		h.activeBuildID = ""
		delete(h.sessions, buildID)
		h.mu.Unlock()
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("build_id", buildID).
				Interface("panic", r).
				Msg("initramfs build: panic in background goroutine")
			sess.appendLine(fmt.Sprintf("PANIC: %v", r))
			sess.markDone("failed: panic")
			h.failBuild(buildID, fmt.Errorf("panic: %v", r))
			_ = h.DB.UpdateBaseImageStatus(context.Background(), img.ID, api.ImageStatusError, fmt.Sprintf("panic: %v", r))
		}
	}()

	sess.appendLine(fmt.Sprintf("build %s started", buildID[:8]))

	stagingPath := h.InitramfsPath + ".build-" + buildID[:8]

	workDir, err := os.MkdirTemp("", "clustr-initramfs-build-*")
	if err != nil {
		msg := "failed to create work dir: " + err.Error()
		sess.appendLine(msg)
		sess.markDone("failed: work dir")
		h.failBuild(buildID, err)
		_ = h.DB.UpdateBaseImageStatus(context.Background(), img.ID, api.ImageStatusError, err.Error())
		return
	}
	defer os.RemoveAll(workDir)

	// Hard deadline context: when this fires we kill the entire process group,
	// ensuring orphaned subprocesses (ssh-agent, backgrounded helpers) cannot
	// keep the goroutine alive past the timeout (BUG-16).
	hardCtx, hardCancel := context.WithTimeout(context.Background(), buildTimeout)
	defer hardCancel()

	// Run the build script; stream output lines into the ring buffer.
	// killPgid is delivered via killCh as soon as the process starts; we call
	// it if hardCtx fires before the script exits.
	lines := make(chan string, 256)
	scriptDone := make(chan error, 1)
	killCh := make(chan func(), 1) // buffered: goroutine sends kill fn right after cmd.Start
	go h.runScriptPgidAsync(workDir, stagingPath, lines, killCh, scriptDone)

	// Receive the kill function once the process has started (or nil if start failed).
	// This arrives almost immediately — just after cmd.Start().
	var killPgid func()
	select {
	case kfn := <-killCh:
		killPgid = kfn
	case <-time.After(30 * time.Second):
		// Extremely unlikely: script setup itself hung. Proceed without a kill fn.
		log.Error().Str("build_id", buildID).Msg("initramfs build: timed out waiting for process to start")
	}

	// Drain lines into the ring buffer while the script runs.
	drainLines := func() {
		for line := range lines {
			sess.appendLine(line)
		}
	}

	// Wait for the script to finish, forwarding output. Respect both buildCtx
	// (external cancel / 15-min outer timeout) and hardCtx (absolute deadline).
	// When hardCtx fires we kill the process group immediately.
	var buildErr error
	cancelled := false
	scriptFinished := false
	for !scriptFinished {
		select {
		case err := <-scriptDone:
			buildErr = err
			scriptFinished = true
			// Drain any remaining lines the goroutine emitted before close.
			drainLines()
		case <-buildCtx.Done():
			// External cancel (e.g. CancelInitramfsBuild). Kill the pg and wait.
			cancelled = true
			if killPgid != nil {
				killPgid()
			}
			select {
			case <-scriptDone:
			case <-time.After(10 * time.Second):
			}
			drainLines()
			scriptFinished = true
		case <-hardCtx.Done():
			// Hard timeout (BUG-16 safety net). Kill the process group immediately.
			msg := fmt.Sprintf("build exceeded %v timeout — killing process group", buildTimeout)
			sess.appendLine(msg)
			log.Error().Str("build_id", buildID).Dur("timeout", buildTimeout).
				Msg("initramfs build: hard timeout — killing process group")
			if killPgid != nil {
				killPgid()
			}
			select {
			case <-scriptDone:
			case <-time.After(10 * time.Second):
			}
			drainLines()
			buildErr = fmt.Errorf("build exceeded %v timeout", buildTimeout)
			scriptFinished = true
		}
	}

	// External cancel overrides a successful script exit.
	if cancelled {
		buildErr = context.Canceled
	}

	if buildErr != nil {
		msg := "build script failed: " + buildErr.Error()
		sess.appendLine(msg)
		sess.markDone("failed: script")
		h.failBuild(buildID, buildErr)
		_ = h.DB.UpdateBaseImageStatus(context.Background(), img.ID, api.ImageStatusError, buildErr.Error())
		os.Remove(stagingPath) //nolint:errcheck
		return
	}

	// Compute SHA256 of staging artifact.
	scriptSHA256, scriptSize, kernelVer := h.inspectStagingFile(stagingPath)
	if scriptSHA256 == "" {
		msg := "failed to hash staging artifact"
		sess.appendLine(msg)
		sess.markDone("failed: hash")
		h.failBuild(buildID, fmt.Errorf("hash staging artifact: empty sha256"))
		_ = h.DB.UpdateBaseImageStatus(context.Background(), img.ID, api.ImageStatusError, msg)
		os.Remove(stagingPath) //nolint:errcheck
		return
	}

	// Atomic-promote staging → live path.
	if h.InitramfsPath != "" {
		if renameErr := os.Rename(stagingPath, h.InitramfsPath); renameErr != nil {
			log.Warn().Err(renameErr).Msg("initramfs build: atomic rename to system path failed (non-fatal)")
			sess.appendLine("Warning: rename to system path failed: " + renameErr.Error())
		} else {
			h.mu.Lock()
			h.liveSHA256 = scriptSHA256
			h.mu.Unlock()
		}
	}

	// Copy into the image store (if ImageDir is set).
	imgSrc := stagingPath
	if h.InitramfsPath != "" {
		if _, statErr := os.Stat(h.InitramfsPath); statErr == nil {
			imgSrc = h.InitramfsPath
		}
	}
	if h.ImageDir != "" {
		imageDir := filepath.Join(h.ImageDir, img.ID)
		if mkErr := os.MkdirAll(imageDir, 0o755); mkErr == nil {
			destPath := filepath.Join(imageDir, "image.img")
			if copyErr := initramfsCopyFile(imgSrc, destPath); copyErr != nil {
				log.Warn().Err(copyErr).Str("image_id", img.ID).Msg("initramfs build: copy to image store failed")
			}
		}
	}
	os.Remove(stagingPath) //nolint:errcheck

	// Auto-place kernel into the boot directory.
	bootDir := filepath.Dir(h.InitramfsPath)
	sizeMiB := float64(scriptSize) / (1024 * 1024)
	sess.appendLine(fmt.Sprintf("Built initramfs (%.1f MiB) — placed at %s", sizeMiB, h.InitramfsPath))

	kverPlaced, placed, kernelErr := ensureKernelPlaced(bootDir)
	if kernelErr != nil {
		log.Warn().Err(kernelErr).Msg("initramfs build: kernel auto-place failed (non-fatal)")
		sess.appendLine("Warning: kernel auto-place failed: " + kernelErr.Error())
	} else if placed {
		sess.appendLine(fmt.Sprintf("Kernel placed at %s/vmlinuz (%s)", bootDir, kverPlaced))
		sess.appendLine("Ready to PXE boot.")
	} else {
		sess.appendLine(fmt.Sprintf("Kernel at %s/vmlinuz already current (%s)", bootDir, kverPlaced))
		sess.appendLine("Ready to PXE boot.")
	}

	// Finalise DB records.
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

	sess.appendLine(fmt.Sprintf("done image_id=%s sha256=%s", img.ID, scriptSHA256))
	sess.markDone("success")
}

// streamBuildSession switches the response to SSE and tails sess until the build
// finishes or the client disconnects. If the build is already done when the client
// connects, all buffered lines plus the terminal event are flushed immediately.
func (h *InitramfsHandler) streamBuildSession(w http.ResponseWriter, r *http.Request, sess *BuildSession) {
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

	sendEvent := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	// Replay buffered lines and tail new ones. We track how many lines have been
	// sent so we only emit new ones on each iteration.
	sent := 0
	for {
		snapshot, notify := sess.snapshot()
		done, outcome := sess.isDone()

		// Emit any new lines since last iteration.
		for ; sent < len(snapshot); sent++ {
			sendEvent(map[string]any{"type": "log", "line": snapshot[sent]})
		}

		if done {
			// Emit the terminal event and close the stream.
			if strings.HasPrefix(outcome, "failed") || strings.HasPrefix(outcome, "panic") {
				sendEvent(map[string]any{"type": "error", "message": outcome})
			} else {
				sendEvent(map[string]any{"type": "done", "build_id": sess.buildID})
			}
			return
		}

		// Wait for a new line, client disconnect, or keepalive tick.
		ticker := time.NewTicker(15 * time.Second)
		select {
		case <-r.Context().Done():
			ticker.Stop()
			// Client disconnected — build continues in the background.
			return
		case <-ticker.C:
			// Keepalive comment so proxies don't close the connection.
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-notify:
			// New line(s) available — loop again to emit them.
		}
		ticker.Stop()
	}
}

// CancelInitramfsBuild handles DELETE /api/v1/initramfs/builds/{id} (INITRD-6).
// Cancels the currently in-flight build if its ID matches. Returns 409 if no build
// is running or the ID does not match the active build.
func (h *InitramfsHandler) CancelInitramfsBuild(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	h.mu.Lock()
	sess, exists := h.sessions[id]
	running := h.running && h.activeBuildID == id
	h.mu.Unlock()

	if !running || !exists {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: "no build is currently in progress with that ID",
			Code:  "no_active_build",
		})
		return
	}

	// Signal cancellation. The goroutine detects context.Done() and cleans up.
	sess.cancel()
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

// ensureKernelPlaced copies /boot/vmlinuz-$(uname -r) to bootDir/vmlinuz when
// the destination is absent or older than the running kernel. Returns the
// kernel version string on success (or on a non-fatal skip) so callers can
// surface it in the build log. /boot/vmlinuz-<kver> is world-readable on
// Rocky 9 (rw-r--r-- root:root), so no privilege escalation is required.
func ensureKernelPlaced(bootDir string) (kver string, placed bool, err error) {
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return "", false, fmt.Errorf("uname -r: %w", err)
	}
	kver = strings.TrimSpace(string(out))

	src := "/boot/vmlinuz-" + kver
	dst := filepath.Join(bootDir, "vmlinuz")

	srcStat, err := os.Stat(src)
	if err != nil {
		return kver, false, fmt.Errorf("kernel source not found at %s: %w", src, err)
	}

	// Skip copy if destination already exists and is at least as new as the source.
	if dstStat, statErr := os.Stat(dst); statErr == nil {
		if !dstStat.ModTime().Before(srcStat.ModTime()) {
			return kver, false, nil // already current
		}
	}

	if mkErr := os.MkdirAll(bootDir, 0o755); mkErr != nil {
		return kver, false, fmt.Errorf("mkdir %s: %w", bootDir, mkErr)
	}

	if copyErr := initramfsCopyFile(src, dst); copyErr != nil {
		return kver, false, fmt.Errorf("copy %s → %s: %w", src, dst, copyErr)
	}
	return kver, true, nil
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
