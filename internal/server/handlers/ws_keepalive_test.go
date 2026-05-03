package handlers

// ws_keepalive_test.go — unit tests for WebSocket ping/pong keepalive (#166)
//
// These tests verify that:
//   1. The server sends periodic ping frames and a client that responds stays
//      connected well beyond a single pongDeadline (simulates a long-running
//      operator_exec_request with no stdout traffic).
//   2. A client that never responds to pings is torn down within wsPongDeadline
//      of the last activity (dead-connection fast teardown).
//   3. The wsPingInterval / wsPongDeadline constants meet the NAT-keepalive
//      requirements documented in #166.
//
// All tests use short test-scoped intervals so the suite finishes in < 1s.
// A minimal httptest.Server re-implements only the keepalive portion of
// HandleClientdWS — no DB, no hub, no protocol dispatch needed.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialWS dials a test server WebSocket endpoint and returns the client conn.
func dialWS(t *testing.T, s *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + path
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dialWS %s: %v", path, err)
	}
	return conn
}

// keepaliveServer starts a minimal httptest.Server that mimics the ping/pong
// keepalive loop from HandleClientdWS, using configurable intervals.
// The returned channel closes when the server handler exits (connection torn down).
func keepaliveServer(t *testing.T, pingInterval, pongDeadline time.Duration) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})

	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		defer close(done)

		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Mirror HandleClientdWS: pong handler bumps the read deadline.
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(pongDeadline))
			return nil
		})
		conn.SetReadDeadline(time.Now().Add(pongDeadline))

		// Ping goroutine — mirrors the pingTicker case in HandleClientdWS.
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		pingStop := make(chan struct{})
		defer close(pingStop)
		go func() {
			for {
				select {
				case <-pingStop:
					return
				case <-ticker.C:
					conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
						return
					}
				}
			}
		}()

		// Read loop — exits on deadline expiry or close frame.
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	})

	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, done
}

// TestWSKeepalive_PongsExtendConnection verifies that a well-behaved client
// that answers pings stays connected for longer than a single pongDeadline.
//
// Setup: ping=50ms, pong deadline=120ms.
// Without ping/pong the connection would be torn down at ~120ms.
// With ping/pong it survives the full 280ms test window and is only closed
// when the client sends a clean close frame.
func TestWSKeepalive_PongsExtendConnection(t *testing.T) {
	const (
		pingInterval = 50 * time.Millisecond
		pongDeadline = 120 * time.Millisecond
		testDuration = 280 * time.Millisecond
	)

	srv, serverDone := keepaliveServer(t, pingInterval, pongDeadline)

	client := dialWS(t, srv, "/ws")
	defer client.Close()

	// Count pings; respond to each with a pong (simulates healthy client).
	var pingCount int64
	client.SetPingHandler(func(data string) error {
		atomic.AddInt64(&pingCount, 1)
		return client.WriteControl(websocket.PongMessage, []byte(data),
			time.Now().Add(5*time.Second))
	})

	// Run the client read loop for testDuration.
	readErr := make(chan error, 1)
	go func() {
		client.SetReadDeadline(time.Now().Add(testDuration + 100*time.Millisecond))
		for {
			_, _, err := client.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	time.Sleep(testDuration)

	// Cleanly close the client — server should exit normally, not on deadline.
	_ = client.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "test done"),
		time.Now().Add(5*time.Second),
	)

	select {
	case <-serverDone:
		// Expected — server exited after the client's clean close.
	case <-time.After(2 * time.Second):
		t.Fatal("server did not exit after clean client close within 2s")
	}

	pings := atomic.LoadInt64(&pingCount)
	// At ping=50ms over 280ms we expect ≥4 pings.
	if pings < 4 {
		t.Errorf("expected ≥4 pings in %v with %v interval, got %d", testDuration, pingInterval, pings)
	}
}

// TestWSKeepalive_NoPongTeardown verifies that a client which ignores all pings
// (never sends a pong) causes the server to tear down the connection within the
// pongDeadline — no silent hang.
//
// Setup: ping=30ms, pong deadline=80ms.
// The connection should be torn down at ~80ms; we allow up to 4× deadline.
func TestWSKeepalive_NoPongTeardown(t *testing.T) {
	const (
		pingInterval = 30 * time.Millisecond
		pongDeadline = 80 * time.Millisecond
	)

	srv, serverDone := keepaliveServer(t, pingInterval, pongDeadline)

	client := dialWS(t, srv, "/ws")

	// Override the default pong-on-ping behaviour: do nothing.
	// This simulates a frozen/dead client that is still TCP-connected but
	// never responds to application-level keepalives.
	client.SetPingHandler(func(string) error { return nil })

	start := time.Now()

	// Keep the client read loop running so the TCP connection stays up (we want
	// the server to time out, not detect a half-closed TCP connection).
	go func() {
		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-serverDone:
		elapsed := time.Since(start)
		bound := pongDeadline * 4
		if elapsed > bound {
			t.Errorf("server took %v to tear down dead connection; expected ≤%v",
				elapsed, bound)
		}
		t.Logf("dead connection torn down after %v (pong deadline=%v)", elapsed, pongDeadline)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not tear down dead connection within 2s")
	}

	client.Close()
}

// TestWSPingInterval_Constants verifies that the package-level timing constants
// satisfy the requirements from #166:
//   - wsPingInterval < 60s  — beats typical stateful-firewall NAT idle timeout
//   - wsPongDeadline > 2×wsPingInterval — single dropped ping does not kill session
func TestWSPingInterval_Constants(t *testing.T) {
	if wsPingInterval >= 60*time.Second {
		t.Errorf("wsPingInterval=%v must be < 60s to survive typical NAT idle timeout", wsPingInterval)
	}
	if wsPongDeadline <= 2*wsPingInterval {
		t.Errorf("wsPongDeadline=%v must be > 2×wsPingInterval (%v)", wsPongDeadline, 2*wsPingInterval)
	}
}
