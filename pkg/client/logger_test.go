package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// fakeLogServer accepts POST /api/v1/logs and records the decoded batch.
// It is the minimum surface needed to exercise RemoteLogWriter.Flush() end-to-end.
type fakeLogServer struct {
	mu      sync.Mutex
	batches [][]api.LogEntry
}

func (f *fakeLogServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/logs" {
			http.NotFound(w, r)
			return
		}
		var entries []api.LogEntry
		if err := json.NewDecoder(r.Body).Decode(&entries); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.batches = append(f.batches, entries)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}
}

func (f *fakeLogServer) entries() []api.LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []api.LogEntry
	for _, b := range f.batches {
		all = append(all, b...)
	}
	return all
}

// newTestWriter builds a RemoteLogWriter pointed at the given server. The
// flusher goroutine is left running; tests that don't want background ticks
// should call Flush() directly. The default 2s flush interval keeps the
// background ticker out of the way for tests that drive Flush() manually.
func newTestWriter(t *testing.T, srvURL string) *RemoteLogWriter {
	t.Helper()
	c := New(srvURL, "test-key")
	w := NewRemoteLogWriter(c, "aa:bb:cc:dd:ee:ff", "test-host", WithComponent("deploy"))
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// TestRemoteLogWriter_StampsPhase is the Sprint 33 STREAM-LOG-PHASE acceptance
// test from the hardened sprint plan: SetPhase("partitioning") followed by a
// zerolog JSON Write must produce a LogEntry with Phase=="partitioning".
func TestRemoteLogWriter_StampsPhase(t *testing.T) {
	fake := &fakeLogServer{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	w := newTestWriter(t, srv.URL)
	w.SetPhase("partitioning")
	if got := w.Phase(); got != "partitioning" {
		t.Fatalf("Phase() = %q after SetPhase; want %q", got, "partitioning")
	}

	if _, err := w.Write([]byte(`{"level":"info","message":"sgdisk --zap-all"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	entries := fake.entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries; want 1", len(entries))
	}
	if got := entries[0].Phase; got != "partitioning" {
		t.Fatalf("Phase = %q; want %q", got, "partitioning")
	}
	if got := entries[0].Message; got != "sgdisk --zap-all" {
		t.Fatalf("Message = %q; want %q", got, "sgdisk --zap-all")
	}
	if got := entries[0].Component; got != "deploy" {
		t.Fatalf("Component = %q; want %q (writer default)", got, "deploy")
	}
}

// TestRemoteLogWriter_PhaseTransitions confirms that SetPhase mid-stream
// retags subsequent lines but does not retroactively rewrite already-buffered
// entries. This is the contract the UI relies on to colour-group correctly.
func TestRemoteLogWriter_PhaseTransitions(t *testing.T) {
	fake := &fakeLogServer{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	w := newTestWriter(t, srv.URL)

	w.SetPhase("preflight")
	mustWrite(t, w, `{"level":"info","message":"preflight start"}`)

	w.SetPhase("partitioning")
	mustWrite(t, w, `{"level":"info","message":"sgdisk"}`)

	w.SetPhase("downloading")
	mustWrite(t, w, `{"level":"info","message":"GET /blob"}`)
	mustWrite(t, w, `{"level":"info","message":"recv 50%"}`)

	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	entries := fake.entries()
	if len(entries) != 4 {
		t.Fatalf("got %d entries; want 4", len(entries))
	}
	want := []struct{ msg, phase string }{
		{"preflight start", "preflight"},
		{"sgdisk", "partitioning"},
		{"GET /blob", "downloading"},
		{"recv 50%", "downloading"},
	}
	for i, e := range entries {
		if e.Message != want[i].msg || e.Phase != want[i].phase {
			t.Errorf("entry[%d] = (%q, %q); want (%q, %q)",
				i, e.Message, e.Phase, want[i].msg, want[i].phase)
		}
	}
}

// TestRemoteLogWriter_InlinePhaseWins verifies a "phase" field on the zerolog
// line overrides the writer-level phase. Mirrors the existing "component"
// precedence rule so callers can override per-line without touching SetPhase().
// The runtime importance of this rule: the existing deploy progressFn already
// emits Str("phase", phase) on its progress lines; without inline-wins, those
// lines would be misattributed during the moment between the deployer's phase
// transition callback and the next SetPhase() call.
func TestRemoteLogWriter_InlinePhaseWins(t *testing.T) {
	fake := &fakeLogServer{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	w := newTestWriter(t, srv.URL)
	w.SetPhase("downloading")
	mustWrite(t, w, `{"level":"info","message":"checksum verified","phase":"verifying"}`)

	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	entries := fake.entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries; want 1", len(entries))
	}
	if got := entries[0].Phase; got != "verifying" {
		t.Fatalf("Phase = %q; want %q (inline override)", got, "verifying")
	}
}

// TestRemoteLogWriter_PhaseEmptyByDefault confirms entries written before any
// SetPhase() call ship with Phase=="" (no breakage for non-deploy contexts
// like the CLI's image command, which uses the same writer with component="cli").
func TestRemoteLogWriter_PhaseEmptyByDefault(t *testing.T) {
	fake := &fakeLogServer{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	w := newTestWriter(t, srv.URL)
	mustWrite(t, w, `{"level":"info","message":"unphased"}`)

	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	entries := fake.entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries; want 1", len(entries))
	}
	if got := entries[0].Phase; got != "" {
		t.Fatalf("Phase = %q; want \"\" (default)", got)
	}
}

// TestRemoteLogWriter_PhaseConcurrency exercises the lock contract: SetPhase
// from one goroutine while Write runs from another must not race. The test
// passes when run under `go test -race`. Without the mutex this would either
// trip the race detector or, on Go 1.25+, fault via the panic detector.
func TestRemoteLogWriter_PhaseConcurrency(t *testing.T) {
	fake := &fakeLogServer{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	w := newTestWriter(t, srv.URL)

	const n = 200
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		phases := []string{"preflight", "partitioning", "downloading", "extracting", "finalizing"}
		for i := 0; i < n; i++ {
			w.SetPhase(phases[i%len(phases)])
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_, _ = w.Write([]byte(`{"level":"info","message":"concurrent"}` + "\n"))
		}
	}()

	wg.Wait()

	// Drain whatever made it into the buffer. We don't assert exact counts
	// (n entries may exceed the buffer cap, which is fine — drops are
	// expected and documented). We only need to confirm we got some entries
	// and none crashed the writer.
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(fake.entries()) == 0 {
		t.Fatal("expected at least some buffered entries to be shipped")
	}
}

func mustWrite(t *testing.T, w *RemoteLogWriter, line string) {
	t.Helper()
	if _, err := w.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("Write(%q): %v", line, err)
	}
}

// TestRemoteLogWriter_PhaseSurvivesUrgentFlush validates that an ERROR-level
// line emitted just after a SetPhase() call is shipped with the right phase,
// even though Write() short-circuits the 2s ticker via the urgent-flush path.
// This is the failure mode operators care about most: the *first* line of a
// fatal phase must reach the server with the phase tag intact.
func TestRemoteLogWriter_PhaseSurvivesUrgentFlush(t *testing.T) {
	fake := &fakeLogServer{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	w := newTestWriter(t, srv.URL)
	w.SetPhase("partitioning")
	mustWrite(t, w, `{"level":"error","message":"sgdisk failed: device busy"}`)

	// Wait briefly for the urgent-flush path to drain via the flusher goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.entries()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries := fake.entries()
	if len(entries) == 0 {
		t.Fatal("urgent flush did not deliver entry within 2s")
	}
	if got := entries[0].Phase; got != "partitioning" {
		t.Fatalf("Phase = %q; want %q", got, "partitioning")
	}
	if got := entries[0].Level; got != "error" {
		t.Fatalf("Level = %q; want %q", got, "error")
	}
}
