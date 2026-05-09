package handlers

import (
	"context"
	"io"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// fakePipe is a synchronous bidirectional in-memory pipe that satisfies
// io.ReadWriteCloser. Bytes written via Write are delivered to peer.Read;
// reading blocks until bytes are available. Used by the SOL bridge unit
// test to simulate a fake ipmitool subprocess.
type fakePipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

func newFakePipe() *fakePipe {
	p := &fakePipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *fakePipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	p.buf = append(p.buf, b...)
	p.cond.Broadcast()
	return len(b), nil
}

func (p *fakePipe) Read(out []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && !p.closed {
		p.cond.Wait()
	}
	if len(p.buf) == 0 && p.closed {
		return 0, io.EOF
	}
	n := copy(out, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *fakePipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.cond.Broadcast()
	return nil
}

// ─── Bridge: bidirectional forwarding ────────────────────────────────────────

func TestSOLBridge_BidirectionalForwarding(t *testing.T) {
	subprocess := newFakePipe()

	h := &SOLConsoleHandler{
		DB: &fakeConsoleDB{
			nodes: map[string]api.NodeConfig{
				"node-1": {BMC: &api.BMCNodeConfig{IPAddress: "10.0.0.5", Username: "admin", Password: "p"}},
			},
		},
		active: make(map[string]*SOLBridge),
		Spawn: func(_ context.Context, _ solCreds) (io.ReadWriteCloser, *exec.Cmd, error) {
			return subprocess, nil, nil
		},
	}

	r := chi.NewRouter()
	r.Get("/api/v1/nodes/{id}/console/sol", h.HandleSOL)
	srv := httptest.NewServer(r)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/nodes/node-1/console/sol"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close()

	// upstream (subprocess → WS): write some bytes to the fake subprocess
	// stdout and verify they arrive at the WS as a binary frame.
	want := []byte("BIOS POST\r\n")
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = subprocess.Write(want)
	}()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, got, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read upstream: %v", err)
	}
	if mt != websocket.BinaryMessage {
		t.Errorf("upstream frame type = %d, want BinaryMessage", mt)
	}
	if string(got) != string(want) {
		t.Errorf("upstream payload = %q, want %q", got, want)
	}

	// downstream (WS → subprocess): write a frame; in our fake the same
	// buffer is shared by both directions, so we expect the bridge to
	// forward our keystroke back as another binary frame.
	keystroke := []byte("ls -la\n")
	if err := conn.WriteMessage(websocket.BinaryMessage, keystroke); err != nil {
		t.Fatalf("write keystroke: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt2, echoed, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read echoed: %v", err)
	}
	if mt2 != websocket.BinaryMessage {
		t.Errorf("echo frame type = %d, want BinaryMessage", mt2)
	}
	if string(echoed) != string(keystroke) {
		t.Errorf("echoed payload = %q, want %q", echoed, keystroke)
	}

	// Close the subprocess to terminate the bridge.
	_ = subprocess.Close()

	// Read the exit frame.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage()
}

// ─── Single-active-session: second connect supersedes first ──────────────────

func TestSOLBridge_SecondConnectSupersedesFirst(t *testing.T) {
	subprocess1 := newFakePipe()
	subprocess2 := newFakePipe()
	spawnCalls := 0

	h := &SOLConsoleHandler{
		DB: &fakeConsoleDB{
			nodes: map[string]api.NodeConfig{
				"node-1": {BMC: &api.BMCNodeConfig{IPAddress: "10.0.0.5", Username: "admin", Password: "p"}},
			},
		},
		active: make(map[string]*SOLBridge),
		Spawn: func(_ context.Context, _ solCreds) (io.ReadWriteCloser, *exec.Cmd, error) {
			spawnCalls++
			if spawnCalls == 1 {
				return subprocess1, nil, nil
			}
			return subprocess2, nil, nil
		},
	}

	r := chi.NewRouter()
	r.Get("/api/v1/nodes/{id}/console/sol", h.HandleSOL)
	srv := httptest.NewServer(r)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/nodes/node-1/console/sol"

	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer conn1.Close()
	time.Sleep(100 * time.Millisecond)

	h.mu.Lock()
	if _, ok := h.active["node-1"]; !ok {
		h.mu.Unlock()
		t.Fatal("first session should have registered an active bridge")
	}
	h.mu.Unlock()

	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer conn2.Close()

	time.Sleep(150 * time.Millisecond)

	// First connection's subprocess should be closed (cancel signalled it).
	conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn1.ReadMessage()

	_ = subprocess1.Close()
	_ = subprocess2.Close()

	if spawnCalls != 2 {
		t.Errorf("expected 2 spawn calls, got %d", spawnCalls)
	}
}

// TestSOLBridge_RaceyConnectsLandOneActive locks down Codex post-ship
// review issue #6: HandleSOL previously did
//
//   superseded(nodeID)   // remove old entry
//   ... Spawn(...)        // potentially long
//   active[nodeID] = bridge
//
// A concurrent connect arriving while Spawn was in progress saw an
// empty active map, took the no-supersede path, and ended up running
// alongside the first session — two simultaneous bridges to the same
// BMC.  The fix reserves the slot atomically before Spawn, so a racing
// connect cancels the previous one even mid-Spawn.
//
// We simulate the race by gating the Spawn function on a channel: the
// first connect blocks inside Spawn until released; while it's blocked
// we fire the second connect.  Because the slot is reserved BEFORE
// Spawn, the second connect sees the placeholder, cancels it, and the
// first connect's Spawn unblocks into a cancelled context.  Net: only
// the second bridge is ever active.
func TestSOLBridge_RaceyConnectsLandOneActive(t *testing.T) {
	subprocess1 := newFakePipe()
	subprocess2 := newFakePipe()

	// First-spawn release gate.  Spawn #1 blocks on this until we close
	// the channel — modelling the case where Spawn takes long enough
	// for a second connect to arrive.
	releaseSpawn1 := make(chan struct{})
	spawn1Started := make(chan struct{})
	spawnCalls := 0

	h := &SOLConsoleHandler{
		DB: &fakeConsoleDB{
			nodes: map[string]api.NodeConfig{
				"node-1": {BMC: &api.BMCNodeConfig{IPAddress: "10.0.0.5", Username: "admin", Password: "p"}},
			},
		},
		active: make(map[string]*SOLBridge),
		Spawn: func(ctx context.Context, _ solCreds) (io.ReadWriteCloser, *exec.Cmd, error) {
			spawnCalls++
			if spawnCalls == 1 {
				close(spawn1Started)
				select {
				case <-releaseSpawn1:
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
				return subprocess1, nil, nil
			}
			return subprocess2, nil, nil
		},
	}

	r := chi.NewRouter()
	r.Get("/api/v1/nodes/{id}/console/sol", h.HandleSOL)
	srv := httptest.NewServer(r)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/nodes/node-1/console/sol"

	// Fire the first connect.  Don't wait for it to "succeed" — it's
	// going to block in Spawn.
	conn1Done := make(chan *websocket.Conn, 1)
	go func() {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err == nil {
			conn1Done <- conn
		} else {
			conn1Done <- nil
		}
	}()

	// Wait for the first connect to enter Spawn so we know its slot is
	// reserved (per the fix).
	<-spawn1Started

	// Fire the second connect while #1 is wedged in Spawn.
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer conn2.Close()

	// Give the supersede path a moment to land, then release Spawn 1.
	time.Sleep(100 * time.Millisecond)
	close(releaseSpawn1)

	// At steady state we expect exactly one bridge active for node-1
	// and the FIRST connect's bridge to have been replaced.
	time.Sleep(200 * time.Millisecond)

	h.mu.Lock()
	got := len(h.active)
	h.mu.Unlock()
	if got != 1 {
		t.Errorf("expected exactly 1 active bridge after race, got %d", got)
	}

	// Drain the first connect cleanly so the test goroutine exits.
	if conn1 := <-conn1Done; conn1 != nil {
		conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, _ = conn1.ReadMessage()
		conn1.Close()
	}
	_ = subprocess1.Close()
	_ = subprocess2.Close()
}

// ─── No BMC = 400 ─────────────────────────────────────────────────────────────

func TestSOLBridge_NodeWithoutBMC_Rejected(t *testing.T) {
	h := &SOLConsoleHandler{
		DB: &fakeConsoleDB{
			nodes: map[string]api.NodeConfig{
				"node-2": {},
			},
		},
		active: make(map[string]*SOLBridge),
		Spawn: func(_ context.Context, _ solCreds) (io.ReadWriteCloser, *exec.Cmd, error) {
			t.Fatal("spawn should not be called when node has no BMC")
			return nil, nil, nil
		},
	}

	r := chi.NewRouter()
	r.Get("/api/v1/nodes/{id}/console/sol", h.HandleSOL)
	srv := httptest.NewServer(r)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/nodes/node-2/console/sol"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected dial to fail for node without BMC")
	}
	if resp == nil {
		t.Fatal("expected an HTTP response on rejection")
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ─── resolveSOLCreds priority ─────────────────────────────────────────────────

func TestResolveSOLCreds_BMCFallback(t *testing.T) {
	cfg := api.NodeConfig{
		BMC: &api.BMCNodeConfig{IPAddress: "10.0.0.5", Username: "admin", Password: "p"},
	}
	c, ok := resolveSOLCreds(cfg)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if c.Host != "10.0.0.5" {
		t.Errorf("host = %q, want 10.0.0.5", c.Host)
	}
}

func TestResolveSOLCreds_PowerProviderPreferred(t *testing.T) {
	cfg := api.NodeConfig{
		BMC: &api.BMCNodeConfig{IPAddress: "10.0.0.5"},
		PowerProvider: &api.PowerProviderConfig{
			Type:   "ipmi",
			Fields: map[string]string{"host": "10.0.0.99", "username": "u", "password": "p"},
		},
	}
	c, ok := resolveSOLCreds(cfg)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if c.Host != "10.0.0.99" {
		t.Errorf("expected PowerProvider host to win, got %q", c.Host)
	}
}

func TestResolveSOLCreds_None(t *testing.T) {
	if _, ok := resolveSOLCreds(api.NodeConfig{}); ok {
		t.Error("expected ok=false when no BMC configured")
	}
}
