package handlers

// sse_keepalive_test.go — SSE-KA-1: integration test for 15s keepalive pings.
//
// Connects to StreamProgress via httptest.NewServer, waits for a ": ping"
// comment line to arrive without any application event being fired, then
// disconnects. Verifies the keepalive ticker is wired correctly in the handler
// event loop.
//
// Uses a 20-second deadline (15s tick + 5s margin). The test is skipped under
// -short because it requires real wall-clock time to elapse.

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// fakeProgressStore satisfies ProgressStoreIface with a never-firing channel
// so no application events are sent during the test — only the keepalive tick
// can produce output.
type fakeProgressStore struct {
	ch chan api.DeployProgress
}

func newFakeProgressStore() *fakeProgressStore {
	return &fakeProgressStore{ch: make(chan api.DeployProgress)}
}

func (s *fakeProgressStore) Update(_ api.DeployProgress)              {}
func (s *fakeProgressStore) Get(_ string) (*api.DeployProgress, bool) { return nil, false }
func (s *fakeProgressStore) List() []api.DeployProgress               { return nil }
func (s *fakeProgressStore) Subscribe() (<-chan api.DeployProgress, func()) {
	return s.ch, func() {}
}

// TestStreamProgress_KeepalivePing verifies that a ": ping" comment is sent
// within one keepalive interval when no application events arrive.
func TestStreamProgress_KeepalivePing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping keepalive test under -short (requires ~15s wall-clock time)")
	}

	store := newFakeProgressStore()
	h := &ProgressHandler{Store: store}

	srv := httptest.NewServer(http.HandlerFunc(h.StreamProgress))
	defer srv.Close()

	// 20s deadline: 15s tick + 5s margin.
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}

	// Read lines until we find ": ping" or the deadline fires.
	found := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, ":") {
				found <- line
				return
			}
		}
		close(found)
	}()

	select {
	case line, ok := <-found:
		if !ok {
			t.Fatal("SSE stream ended before a ping comment arrived")
		}
		if line != ": ping" {
			t.Fatalf("expected ': ping', got %q", line)
		}
		// Pass — keepalive arrived.
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for keepalive ping (expected within 15s)")
	}
}
