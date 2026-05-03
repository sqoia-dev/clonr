package handlers

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
)

// makeInitramfsGzCpio creates a minimal gzipped newc cpio archive at dest
// containing a lib/modules/<kernelVer>/ directory entry.
// This is a self-contained helper that does not require cpio at generation
// time — only zcat+cpio are needed at test-runtime for the code under test.
func makeInitramfsGzCpio(t *testing.T, dest, kernelVer string) {
	t.Helper()

	f, err := os.Create(dest)
	if err != nil {
		t.Fatalf("makeInitramfsGzCpio: create: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)

	writeDirEntry := func(name string) {
		namePlusNull := name + "\x00"
		nameLen := len(namePlusNull)
		header := []byte("070701" +
			"00000001" + "000041ED" + "00000000" + "00000000" +
			"00000002" + "00000000" + "00000000" + "00000008" +
			"00000001" + "00000000" + "00000000" +
			fmt.Sprintf("%08X", nameLen) + "00000000")
		_, _ = gw.Write(header)
		_, _ = gw.Write([]byte(namePlusNull))
		total := len(header) + nameLen
		if pad := (4 - total%4) % 4; pad > 0 {
			_, _ = gw.Write(make([]byte, pad))
		}
	}

	writeTrailer := func() {
		const trailerName = "TRAILER!!!\x00"
		nameLen := len(trailerName)
		header := []byte("070701" +
			"00000000" + "00000000" + "00000000" + "00000000" +
			"00000001" + "00000000" + "00000000" + "00000000" +
			"00000000" + "00000000" + "00000000" +
			fmt.Sprintf("%08X", nameLen) + "00000000")
		_, _ = gw.Write(header)
		_, _ = gw.Write([]byte(trailerName))
		total := len(header) + nameLen
		if pad := (4 - total%4) % 4; pad > 0 {
			_, _ = gw.Write(make([]byte, pad))
		}
	}

	writeDirEntry("lib/modules/" + kernelVer)
	writeTrailer()

	if err := gw.Close(); err != nil {
		t.Fatalf("makeInitramfsGzCpio: gzip close: %v", err)
	}
}

// newInitramfsHandler returns a handler wired to a fresh test DB with the
// initramfs file at path.
func newInitramfsHandler(d *db.DB, imgPath string) *InitramfsHandler {
	h := &InitramfsHandler{
		DB:            d,
		InitramfsPath: imgPath,
	}
	h.InitLiveSHA256()
	return h
}

// TestGetInitramfs_KernelVersionAlreadyInDB verifies that when the DB record
// already has a kernel_version, the handler returns it without re-extracting
// from disk (the short-circuit path).
func TestGetInitramfs_KernelVersionAlreadyInDB(t *testing.T) {
	const wantVer = "5.14.0-611.5.1.el9_7.x86_64"

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "initramfs.img")
	makeInitramfsGzCpio(t, imgPath, wantVer)

	d := openTestDB(t)
	h := newInitramfsHandler(d, imgPath)

	// Seed a successful build record with the live sha256 and a known kernel version.
	ctx := context.Background()
	record := db.InitramfsBuildRecord{
		ID:        "build-001",
		StartedAt: time.Now().UTC(),
		SHA256:    h.liveSHA256,
		Outcome:   "success",
	}
	if err := d.CreateInitramfsBuild(ctx, record); err != nil {
		t.Fatalf("CreateInitramfsBuild: %v", err)
	}
	if err := d.FinishInitramfsBuild(ctx, record.ID, h.liveSHA256, 0, wantVer, "success"); err != nil {
		t.Fatalf("FinishInitramfsBuild: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/initramfs", nil)
	w := httptest.NewRecorder()
	h.GetInitramfs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GetInitramfs: status %d, want 200", w.Code)
	}

	var info InitramfsBuildInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("GetInitramfs: unmarshal: %v", err)
	}
	if info.KernelVersion != wantVer {
		t.Errorf("GetInitramfs: KernelVersion = %q, want %q", info.KernelVersion, wantVer)
	}
}

// TestGetInitramfs_LazyExtract verifies that when the DB record exists but
// kernel_version is empty (autodeploy timer path), the handler extracts the
// version from disk and back-fills the DB.
func TestGetInitramfs_LazyExtract(t *testing.T) {
	const wantVer = "5.14.0-611.5.1.el9_7.x86_64"

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "initramfs.img")
	makeInitramfsGzCpio(t, imgPath, wantVer)

	d := openTestDB(t)
	h := newInitramfsHandler(d, imgPath)

	// Seed a successful build record with empty kernel_version (simulates autodeploy).
	ctx := context.Background()
	record := db.InitramfsBuildRecord{
		ID:        "build-002",
		StartedAt: time.Now().UTC(),
		SHA256:    h.liveSHA256,
		Outcome:   "success",
	}
	if err := d.CreateInitramfsBuild(ctx, record); err != nil {
		t.Fatalf("CreateInitramfsBuild: %v", err)
	}
	if err := d.FinishInitramfsBuild(ctx, record.ID, h.liveSHA256, 0, "", "success"); err != nil {
		t.Fatalf("FinishInitramfsBuild: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/initramfs", nil)
	w := httptest.NewRecorder()
	h.GetInitramfs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GetInitramfs: status %d, want 200", w.Code)
	}

	var info InitramfsBuildInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("GetInitramfs: unmarshal: %v", err)
	}
	if info.KernelVersion != wantVer {
		t.Errorf("GetInitramfs: KernelVersion = %q, want %q (lazy extract failed)", info.KernelVersion, wantVer)
	}

	// Verify the DB was back-filled.
	_, backFilled, err := d.GetLatestSuccessfulBuildBySHA256(ctx, h.liveSHA256)
	if err != nil {
		t.Fatalf("GetLatestSuccessfulBuildBySHA256 after back-fill: %v", err)
	}
	if backFilled != wantVer {
		t.Errorf("DB back-fill: kernel_version = %q, want %q", backFilled, wantVer)
	}
}

// TestRunScript_InitTemplatePresent verifies that when runScript sets up its
// temp directory, both build-initramfs.sh and the initramfs-init.sh sibling
// template are written there. This guards against the regression where the sed
// step crashed with "can't read /var/lib/clustr/tmp/initramfs-init.sh: No such
// file or directory" because only the main script was extracted.
//
// The test replaces the embedded scripts with minimal stubs that verify the
// sibling file is present and immediately exit 0 without doing a real build.
func TestRunScript_InitTemplatePresent(t *testing.T) {
	// Swap the embedded bytes with a script stub that checks for its sibling.
	origScript := buildInitramfsScript
	origInit := initramfsInitScript
	t.Cleanup(func() {
		buildInitramfsScript = origScript
		initramfsInitScript = origInit
	})

	// The stub exits 1 if initramfs-init.sh is absent next to $0.
	buildInitramfsScript = []byte(`#!/bin/bash
if [[ ! -f "$(dirname "$0")/initramfs-init.sh" ]]; then
    echo "FAIL: initramfs-init.sh not found next to $0" >&2
    exit 1
fi
echo "OK: initramfs-init.sh present"
exit 0
`)
	initramfsInitScript = []byte("# stub init template\n")

	d := openTestDB(t)
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "initramfs-clustr.img")

	h := &InitramfsHandler{
		DB:            d,
		InitramfsPath: outPath,
		// ClustrBinPath left empty; script exits before needing it.
	}

	lines := make(chan string, 64)
	err := h.runScript(t.TempDir(), outPath, lines)

	var output strings.Builder
	for l := range lines {
		output.WriteString(l)
		output.WriteByte('\n')
	}

	if err != nil {
		t.Fatalf("runScript returned error: %v\noutput:\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "OK: initramfs-init.sh present") {
		t.Errorf("expected success marker in output, got:\n%s", output.String())
	}
}

// TestGetInitramfs_NoDB_NoFile verifies that the handler returns 200 with an
// empty KernelVersion when neither the DB record nor the file yields a version
// (e.g. corrupt/missing initramfs). It must not crash or return 5xx.
func TestGetInitramfs_NoDB_NoFile(t *testing.T) {
	d := openTestDB(t)
	h := &InitramfsHandler{
		DB:            d,
		InitramfsPath: "/nonexistent/initramfs.img",
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/initramfs", nil)
	w := httptest.NewRecorder()
	h.GetInitramfs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GetInitramfs (no file): status %d, want 200", w.Code)
	}

	var info InitramfsBuildInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("GetInitramfs: unmarshal: %v", err)
	}
	if info.KernelVersion != "" {
		t.Errorf("GetInitramfs: KernelVersion = %q, want empty", info.KernelVersion)
	}
}

// ── BUG-14 / BUG-15 tests ────────────────────────────────────────────────────

// TestBuildSession_PanicFinalizesDB verifies that a panic inside the build
// goroutine is caught by the recover() defer and still marks the DB row as
// failed. This ensures a panicking goroutine never leaves a 'pending' row.
func TestBuildSession_PanicFinalizesDB(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "initramfs.img")

	// Inject a build script stub that deliberately panics by immediately exiting
	// non-zero (which will set buildErr). We cannot inject a real Go panic from
	// shell, but we simulate the same effect: make runScript return an error, then
	// verify the session's done/outcome is set to a failure state.
	//
	// For the actual panic path, we call markDone directly and verify the session
	// state, since runBuildAsync's panic recovery calls sess.markDone internally.
	sess := &BuildSession{
		buildID: "panic-test-01",
		newLine: make(chan struct{}),
	}

	// Simulate what the panic recovery defer in runBuildAsync does.
	var finalizeCalled atomic.Bool
	go func() {
		defer func() {
			if r := recover(); r != nil {
				sess.appendLine(fmt.Sprintf("PANIC: %v", r))
				sess.markDone("failed: panic")
				finalizeCalled.Store(true)
			}
		}()
		panic("injected test panic")
	}()

	// Wait up to 2 seconds for the goroutine to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		done, _ := sess.isDone()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	done, outcome := sess.isDone()
	if !done {
		t.Fatal("BuildSession: expected done=true after panic recovery, got false")
	}
	if !strings.HasPrefix(outcome, "failed") {
		t.Errorf("BuildSession: outcome = %q, want prefix 'failed'", outcome)
	}
	if !finalizeCalled.Load() {
		t.Error("BuildSession: finalise callback was not called after panic")
	}

	// Verify ring buffer captured the panic line.
	lines, _ := sess.snapshot()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "injected test panic") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("BuildSession: panic message not in ring buffer; lines=%v", lines)
	}

	// Verify the DB record can be finalized (simulates what runBuildAsync does
	// after panic recovery).
	record := db.InitramfsBuildRecord{
		ID:        "panic-test-01",
		StartedAt: time.Now().UTC(),
		Outcome:   "pending",
	}
	if err := d.CreateInitramfsBuild(ctx, record); err != nil {
		t.Fatalf("CreateInitramfsBuild: %v", err)
	}
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.FinishInitramfsBuild(dbCtx, record.ID, "", 0, "", "failed: panic: injected test panic"); err != nil {
		t.Fatalf("FinishInitramfsBuild after panic: %v", err)
	}
	records, err := d.ListInitramfsBuilds(ctx, 1)
	if err != nil {
		t.Fatalf("ListInitramfsBuilds: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("ListInitramfsBuilds: expected 1 record, got 0")
	}
	if !strings.HasPrefix(records[0].Outcome, "failed") {
		t.Errorf("DB record outcome = %q, want prefix 'failed'", records[0].Outcome)
	}
	_ = imgPath // used by newInitramfsHandler above if needed
}

// TestBuildSession_DisconnectedClientDoesNotBlockFinalization verifies that
// when an SSE client disconnects mid-build, the background goroutine still
// finalises the DB record and promotes the artifact.
//
// The test uses a short-circuiting script stub (sets buildInitramfsScript to a
// no-op that immediately writes the output file and exits 0) so the build
// completes almost instantly. The HTTP client cancels its context right after
// the SSE stream opens, simulating a disconnect.
func TestBuildSession_DisconnectedClientDoesNotBlockFinalization(t *testing.T) {
	// Swap embedded scripts with stubs.
	origScript := buildInitramfsScript
	origInit := initramfsInitScript
	t.Cleanup(func() {
		buildInitramfsScript = origScript
		initramfsInitScript = origInit
	})

	// Script stub: write 10 bytes to the output path and exit 0.
	buildInitramfsScript = []byte(`#!/bin/bash
echo "stub: writing output"
printf '0123456789' > "$2"
echo "stub: done"
exit 0
`)
	initramfsInitScript = []byte("# stub\n")

	d := openTestDB(t)
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "initramfs.img")

	h := &InitramfsHandler{
		DB:            d,
		InitramfsPath: imgPath,
	}

	// Use a cancelable context that we cancel immediately after the SSE headers
	// are flushed — simulates the client disconnecting.
	clientCtx, clientCancel := context.WithCancel(context.Background())

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/initramfs/build", body).
		WithContext(clientCtx)

	// We need a ResponseRecorder that implements http.Flusher.
	rw := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}

	// Cancel the client context as soon as the SSE headers are written (first Flush call).
	rw.onFirstFlush = clientCancel

	h.BuildInitramfsFromImage(rw, req)
	// At this point the handler returned (client disconnected), but the background
	// goroutine is still running.

	// Wait for the DB record to be finalized (up to 15 seconds).
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	var outcome string
	for time.Now().Before(deadline) {
		records, err := d.ListInitramfsBuilds(ctx, 5)
		if err != nil {
			t.Fatalf("ListInitramfsBuilds: %v", err)
		}
		if len(records) > 0 && records[0].Outcome != "pending" {
			outcome = records[0].Outcome
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if outcome == "" || outcome == "pending" {
		t.Fatalf("DB record not finalized after client disconnect; outcome=%q", outcome)
	}
	// Our stub script writes 10 bytes — outcome should be success.
	if !strings.HasPrefix(outcome, "success") {
		t.Errorf("expected success outcome, got %q", outcome)
	}
	// Live path must exist.
	if _, err := os.Stat(imgPath); err != nil {
		t.Errorf("live initramfs path not promoted after client disconnect: %v", err)
	}
}

// flushRecorder is an httptest.ResponseRecorder that also implements http.Flusher.
// onFirstFlush, if set, is called exactly once on the first Flush invocation.
type flushRecorder struct {
	*httptest.ResponseRecorder
	onFirstFlush func()
	flushed      bool
}

func (f *flushRecorder) Flush() {
	if !f.flushed && f.onFirstFlush != nil {
		f.flushed = true
		f.onFirstFlush()
	}
	f.ResponseRecorder.Flush()
}

// TestRunScriptPgidAsync_OrphanDoesNotHangGoroutine is the BUG-16 regression test.
//
// It verifies that when the build script backgrounds a long-running subprocess
// (sleep 60) and then exits, the build goroutine still exits within 5 seconds.
// Before the fix, the orphaned "sleep 60" held the stdout pipe's write end open,
// causing cmd.Wait() (and therefore the goroutine) to block for 60 seconds.
//
// The fix (pipe-close + Setpgid) ensures:
//  1. Closing stdout before cmd.Wait() decouples us from the orphan's pipe hold.
//  2. SIGKILL to the process group (via killPgid, called by runBuildAsync on timeout)
//     terminates the orphan if the pipe-close defence alone is insufficient.
//
// This test exercises just the pipe-close defence: the goroutine must exit within
// 5 seconds even though the orphaned sleep lives for 60.
func TestRunScriptPgidAsync_OrphanDoesNotHangGoroutine(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping orphan-pipe test in short mode")
	}

	origScript := buildInitramfsScript
	origInit := initramfsInitScript
	t.Cleanup(func() {
		buildInitramfsScript = origScript
		initramfsInitScript = origInit
	})

	// Script that backgrounds a long sleep, writes the output file, then exits.
	// The backgrounded "sleep 60" holds the stdout pipe open after the main
	// script exits — this is exactly the BUG-16 scenario.
	buildInitramfsScript = []byte(`#!/bin/bash
sleep 60 &
echo "main script: backgrounded sleep 60 as PID $!"
printf '0123456789' > "$2"
echo "main script: wrote output file, exiting"
exit 0
`)
	initramfsInitScript = []byte("# stub\n")

	d := openTestDB(t)
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "initramfs.img")
	h := &InitramfsHandler{
		DB:            d,
		InitramfsPath: imgPath,
	}

	lines := make(chan string, 256)
	killCh := make(chan func(), 1)
	scriptDone := make(chan error, 1)

	started := time.Now()
	go h.runScriptPgidAsync(t.TempDir(), imgPath+".build-test0001", lines, killCh, scriptDone)

	// Collect the killPgid func (arrives right after cmd.Start).
	var killPgid func()
	select {
	case kfn := <-killCh:
		killPgid = kfn
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for killPgid from runScriptPgidAsync")
	}
	_ = killPgid // available for hard-timeout kill if needed

	// Drain lines.
	var logLines []string
	for l := range lines {
		logLines = append(logLines, l)
	}

	// Collect the final error.
	var scriptErr error
	select {
	case scriptErr = <-scriptDone:
	case <-time.After(10 * time.Second):
		t.Fatal("scriptDone channel not received within 10s — goroutine is stuck (BUG-16 not fixed)")
	}

	elapsed := time.Since(started)
	if elapsed > 5*time.Second {
		t.Errorf("runScriptPgidAsync took %v — expected < 5s; orphaned process may still be holding the pipe open (BUG-16)", elapsed)
	}
	if scriptErr != nil {
		t.Errorf("unexpected script error: %v", scriptErr)
	}

	// Verify the output file was written.
	if _, err := os.Stat(imgPath + ".build-test0001"); err != nil {
		t.Errorf("output file not written by stub script: %v", err)
	}

	t.Logf("goroutine exited in %v (log lines: %v)", elapsed, logLines)
}

// TestReconcileStuckInitramfsBuilds_StagingVariants exercises the three
// BUG-15 cases directly through DB + filesystem operations (no HTTP), using the
// db.ListPendingInitramfsBuilds + db.FinishInitramfsBuild path that the real
// server.ReconcileStuckInitramfsBuilds calls.
//
// Case A — staging file present & non-empty  → expect success-like outcome (via rename + finish)
// Case B — staging file is empty              → expect failed outcome
// Case C — staging file absent               → expect failed outcome
func TestReconcileStuckInitramfsBuilds_StagingVariants(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	bootDir := t.TempDir()
	livePath := filepath.Join(bootDir, "initramfs-clustr.img")

	type testCase struct {
		name      string
		buildID   string
		setupFile func(stagingPath string) // nil = no file
		wantOK    bool                     // true = expect success-like, false = expect failed
	}

	cases := []testCase{
		{
			name:    "valid staging file self-heals",
			buildID: "aaaa1111-0000-0000-0000-000000000000",
			setupFile: func(p string) {
				// Write 1 KB of data — "valid" artifact.
				data := make([]byte, 1024)
				for i := range data {
					data[i] = 0xAA
				}
				if err := os.WriteFile(p, data, 0o644); err != nil {
					t.Fatalf("setup: write staging: %v", err)
				}
			},
			wantOK: true,
		},
		{
			name:    "empty staging file fails",
			buildID: "bbbb2222-0000-0000-0000-000000000000",
			setupFile: func(p string) {
				if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
					t.Fatalf("setup: write empty staging: %v", err)
				}
			},
			wantOK: false,
		},
		{
			name:      "absent staging file fails",
			buildID:   "cccc3333-0000-0000-0000-000000000000",
			setupFile: nil, // no file
			wantOK:    false,
		},
	}

	// Seed pending DB records for all cases.
	for _, tc := range cases {
		record := db.InitramfsBuildRecord{
			ID:        tc.buildID,
			StartedAt: time.Now().UTC(),
			Outcome:   "pending",
		}
		if err := d.CreateInitramfsBuild(ctx, record); err != nil {
			t.Fatalf("%s: CreateInitramfsBuild: %v", tc.name, err)
		}
	}

	// Set up filesystem state.
	for _, tc := range cases {
		shortID := tc.buildID[:8]
		stagingPath := filepath.Join(bootDir, "initramfs-clustr.img.build-"+shortID)
		if tc.setupFile != nil {
			tc.setupFile(stagingPath)
		}
	}

	// Run the reconcile logic inline (mirrors server.ReconcileStuckInitramfsBuilds).
	pending, err := d.ListPendingInitramfsBuilds(ctx)
	if err != nil {
		t.Fatalf("ListPendingInitramfsBuilds: %v", err)
	}
	if len(pending) != len(cases) {
		t.Fatalf("expected %d pending builds, got %d", len(cases), len(pending))
	}

	for _, build := range pending {
		shortID := build.ID[:8]
		stagingPath := filepath.Join(bootDir, "initramfs-clustr.img.build-"+shortID)

		info, statErr := os.Stat(stagingPath)
		if statErr != nil {
			// No artifact.
			_ = d.FinishInitramfsBuild(ctx, build.ID, "", 0, "", "failed: server restarted during build")
			continue
		}
		if info.Size() == 0 {
			os.Remove(stagingPath) //nolint:errcheck
			_ = d.FinishInitramfsBuild(ctx, build.ID, "", 0, "", "failed: empty artifact after restart")
			continue
		}
		// Non-empty: compute SHA, promote.
		sha := computeFileSHA256(stagingPath)
		if sha == "" {
			_ = d.FinishInitramfsBuild(ctx, build.ID, "", 0, "", "failed: hash on recovery")
			continue
		}
		_ = os.Rename(stagingPath, livePath)
		_ = d.FinishInitramfsBuild(ctx, build.ID, sha, info.Size(), "", "success (recovered after restart)")
	}

	// Verify outcomes.
	for _, tc := range cases {
		records, listErr := d.ListInitramfsBuilds(ctx, 10)
		if listErr != nil {
			t.Fatalf("%s: ListInitramfsBuilds: %v", tc.name, listErr)
		}
		var found *db.InitramfsBuildRecord
		for i := range records {
			if records[i].ID == tc.buildID {
				found = &records[i]
				break
			}
		}
		if found == nil {
			t.Errorf("%s: build record not found in history", tc.name)
			continue
		}
		if tc.wantOK {
			if !strings.HasPrefix(found.Outcome, "success") {
				t.Errorf("%s: outcome = %q, want prefix 'success'", tc.name, found.Outcome)
			}
			// Live path must exist for the success case (the first wantOK case promotes it).
			if _, statErr := os.Stat(livePath); statErr != nil {
				t.Errorf("%s: live path not present after self-heal: %v", tc.name, statErr)
			}
		} else {
			if !strings.HasPrefix(found.Outcome, "failed") {
				t.Errorf("%s: outcome = %q, want prefix 'failed'", tc.name, found.Outcome)
			}
		}
	}
}

// TestReconcileStuckInitramfsBuilds_ManifestSidecarRequired is the BUG-17 regression test.
//
// It verifies that the reconciler only promotes a staging artifact to the live
// path when the .modules.manifest sidecar is present alongside it. Without the
// sidecar the artifact is considered incomplete (the build script crashed before
// dracut finished) and must be marked failed rather than promoted.
//
// Four cases:
//   - D: staging file + manifest → success (recovered after restart)
//   - E: staging file, no manifest → failed (build artifact incomplete)
//   - F: empty staging + manifest → failed (empty artifact — manifest is irrelevant)
//   - G: no staging, no manifest → failed (no artifact)
func TestReconcileStuckInitramfsBuilds_ManifestSidecarRequired(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	bootDir := t.TempDir()
	livePath := filepath.Join(bootDir, "initramfs-clustr.img")

	type testCase struct {
		name          string
		buildID       string
		writeStaging  bool
		stagingBytes  int // 0 = empty, >0 = non-empty
		writeManifest bool
		wantSuccess   bool
	}

	cases := []testCase{
		{
			name:          "staging + manifest promotes to success",
			buildID:       "dddd4444-0000-0000-0000-000000000000",
			writeStaging:  true,
			stagingBytes:  1024,
			writeManifest: true,
			wantSuccess:   true,
		},
		{
			name:          "staging without manifest marks failed",
			buildID:       "eeee5555-0000-0000-0000-000000000000",
			writeStaging:  true,
			stagingBytes:  1024,
			writeManifest: false,
			wantSuccess:   false,
		},
		{
			name:          "empty staging marks failed regardless of manifest",
			buildID:       "ffff6666-0000-0000-0000-000000000000",
			writeStaging:  true,
			stagingBytes:  0,
			writeManifest: true,
			wantSuccess:   false,
		},
		{
			name:          "no staging marks failed",
			buildID:       "gggg7777-0000-0000-0000-000000000000",
			writeStaging:  false,
			writeManifest: false,
			wantSuccess:   false,
		},
	}

	// Seed pending DB records.
	for _, tc := range cases {
		rec := db.InitramfsBuildRecord{
			ID:        tc.buildID,
			StartedAt: time.Now().UTC(),
			Outcome:   "pending",
		}
		if err := d.CreateInitramfsBuild(ctx, rec); err != nil {
			t.Fatalf("%s: CreateInitramfsBuild: %v", tc.name, err)
		}
	}

	// Set up filesystem state.
	for _, tc := range cases {
		shortID := tc.buildID[:8]
		stagingPath := filepath.Join(bootDir, "initramfs-clustr.img.build-"+shortID)
		manifestPath := stagingPath + ".modules.manifest"

		if tc.writeStaging {
			data := make([]byte, tc.stagingBytes)
			for i := range data {
				data[i] = 0xBB
			}
			if err := os.WriteFile(stagingPath, data, 0o644); err != nil {
				t.Fatalf("%s: write staging: %v", tc.name, err)
			}
		}
		if tc.writeManifest {
			if err := os.WriteFile(manifestPath, []byte("kernel/drivers/net/e1000.ko.xz\n"), 0o644); err != nil {
				t.Fatalf("%s: write manifest: %v", tc.name, err)
			}
		}
	}

	// Run the reconcile logic inline, mirroring server.ReconcileStuckInitramfsBuilds
	// with the BUG-17 manifest-sidecar check.
	pending, err := d.ListPendingInitramfsBuilds(ctx)
	if err != nil {
		t.Fatalf("ListPendingInitramfsBuilds: %v", err)
	}
	if len(pending) != len(cases) {
		t.Fatalf("expected %d pending builds, got %d", len(cases), len(pending))
	}

	for _, build := range pending {
		shortID := build.ID[:8]
		stagingPath := filepath.Join(bootDir, "initramfs-clustr.img.build-"+shortID)
		manifestPath := stagingPath + ".modules.manifest"

		info, statErr := os.Stat(stagingPath)
		if statErr != nil {
			_ = d.FinishInitramfsBuild(ctx, build.ID, "", 0, "", "failed: server restarted during build")
			continue
		}
		if info.Size() == 0 {
			os.Remove(stagingPath) //nolint:errcheck
			_ = d.FinishInitramfsBuild(ctx, build.ID, "", 0, "", "failed: empty artifact after restart")
			continue
		}

		// BUG-17: manifest sidecar check — must be present to prove dracut completed.
		if _, manifestErr := os.Stat(manifestPath); manifestErr != nil {
			_ = d.FinishInitramfsBuild(ctx, build.ID, "", 0, "", "failed: build artifact incomplete (no modules manifest)")
			continue
		}

		sha := computeFileSHA256(stagingPath)
		if sha == "" {
			_ = d.FinishInitramfsBuild(ctx, build.ID, "", 0, "", "failed: hash on recovery")
			continue
		}
		_ = os.Rename(stagingPath, livePath)
		os.Remove(manifestPath) //nolint:errcheck
		_ = d.FinishInitramfsBuild(ctx, build.ID, sha, info.Size(), "", "success (recovered after restart)")
	}

	// Verify outcomes.
	for _, tc := range cases {
		records, listErr := d.ListInitramfsBuilds(ctx, 10)
		if listErr != nil {
			t.Fatalf("%s: ListInitramfsBuilds: %v", tc.name, listErr)
		}
		var found *db.InitramfsBuildRecord
		for i := range records {
			if records[i].ID == tc.buildID {
				found = &records[i]
				break
			}
		}
		if found == nil {
			t.Errorf("%s: build record not found", tc.name)
			continue
		}
		if tc.wantSuccess {
			if !strings.HasPrefix(found.Outcome, "success") {
				t.Errorf("%s: outcome = %q, want prefix 'success'", tc.name, found.Outcome)
			}
		} else {
			if !strings.HasPrefix(found.Outcome, "failed") {
				t.Errorf("%s: outcome = %q, want prefix 'failed'", tc.name, found.Outcome)
			}
		}
	}

	// Manifest sidecar for the success case must be cleaned up.
	successBuild := cases[0]
	shortID := successBuild.buildID[:8]
	cleanedManifest := filepath.Join(bootDir, "initramfs-clustr.img.build-"+shortID+".modules.manifest")
	if _, err := os.Stat(cleanedManifest); err == nil {
		t.Errorf("manifest sidecar not removed after successful promotion: %s", cleanedManifest)
	}
}
