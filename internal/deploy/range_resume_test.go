package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeRangeBlobServer creates a fake HTTP server that:
//   - Serves a fixed-size blob.
//   - On the first request, drops the connection after sending dropAfter bytes
//     (simulating a mid-stream network failure).
//   - On subsequent Range requests, resumes from the requested offset.
//
// Returns the server URL and the full blob bytes (for checksum verification).
func fakeRangeBlobServer(t *testing.T, blobSize int, dropAfter int) (*httptest.Server, []byte) {
	t.Helper()

	// Build a deterministic blob: repeating byte pattern.
	blob := make([]byte, blobSize)
	for i := range blob {
		blob[i] = byte(i % 251)
	}

	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		start := int64(0)
		end := int64(len(blob))

		// Parse Range header if present.
		if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
			// Expect "bytes=N-" form.
			rangeHdr = strings.TrimPrefix(rangeHdr, "bytes=")
			parts := strings.SplitN(rangeHdr, "-", 2)
			if n, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
				start = n
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end-1, len(blob)))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start, 10))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
		}

		// First request: send only dropAfter bytes then close (simulate drop).
		if requestCount == 1 && dropAfter > 0 && start == 0 {
			remaining := int64(dropAfter) - start
			if remaining <= 0 {
				return
			}
			w.Write(blob[start : start+remaining])
			// Hijack and close is not easily testable; just return early so the
			// client gets a truncated body (ContentLength set, body short).
			return
		}

		// Subsequent requests or resumed requests: serve full slice from start.
		w.Write(blob[start:end])
	}))
	return srv, blob
}

// TestCountingReader verifies that countingReader accurately counts bytes.
func TestCountingReader_Count(t *testing.T) {
	data := []byte("hello world")
	cr := &countingReader{r: strings.NewReader(string(data))}
	buf := make([]byte, 4)
	for {
		n, err := cr.Read(buf)
		if n == 0 && err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			t.Fatalf("read: %v", err)
		}
	}
	if cr.n != int64(len(data)) {
		t.Errorf("countingReader: got %d bytes, want %d", cr.n, len(data))
	}
}

// TestDeployTimeout_DefaultValue verifies the default deploy timeout is 30m.
func TestDeployTimeout_DefaultValue(t *testing.T) {
	os.Unsetenv("CLUSTR_DEPLOY_TIMEOUT")
	got := deployTimeout()
	if got != 30*time.Minute {
		t.Errorf("deployTimeout: got %v, want 30m", got)
	}
}

// TestDeployTimeout_EnvVar verifies CLUSTR_DEPLOY_TIMEOUT is parsed.
func TestDeployTimeout_EnvVar(t *testing.T) {
	t.Setenv("CLUSTR_DEPLOY_TIMEOUT", "45m")
	got := deployTimeout()
	if got != 45*time.Minute {
		t.Errorf("deployTimeout: got %v, want 45m", got)
	}
}

// TestDeployTimeout_InvalidEnvFallback verifies that an invalid env var returns
// the default rather than 0 or panicking.
func TestDeployTimeout_InvalidEnvFallback(t *testing.T) {
	t.Setenv("CLUSTR_DEPLOY_TIMEOUT", "not-a-duration")
	got := deployTimeout()
	if got != defaultDeployTimeout {
		t.Errorf("deployTimeout: got %v, want default %v", got, defaultDeployTimeout)
	}
}

// TestBlockDeployer_RangeResume is an end-to-end test that:
//  1. Sets up a fake HTTP server that drops the connection after 256 bytes.
//  2. Creates a target file (simulates a block device).
//  3. Runs BlockDeployer.attemptBlockWrite twice: once gets truncated, the second
//     resumes with a Range header from the correct offset.
//  4. Verifies the full blob is present in the target file.
func TestBlockDeployer_RangeResume(t *testing.T) {
	const blobSize = 1024
	const dropAfter = 256 // server drops after 256 bytes on first request

	srv, blob := fakeRangeBlobServer(t, blobSize, dropAfter)
	defer srv.Close()

	// Create a target file to simulate a block device.
	targetFile := filepath.Join(t.TempDir(), "disk.img")
	f, err := os.Create(targetFile)
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	// Pre-size the file so Seek works correctly.
	if err := f.Truncate(blobSize); err != nil {
		t.Fatalf("truncate target: %v", err)
	}
	f.Close()

	d := &BlockDeployer{}
	opts := DeployOpts{
		ImageURL:    srv.URL + "/blob",
		SkipVerify:  true, // skip sha256 check for this test
	}

	// First attempt: server sends only 256 bytes then truncates.
	n1, err1 := d.attemptBlockWrite(context.Background(), targetFile, opts, 0, nil)
	// The first attempt should fail (truncated body) or succeed with partial data.
	// We don't assert error here — the behaviour depends on whether io.Copy
	// returns an error on a short body. What we verify is that n1 ≤ dropAfter.
	_ = err1
	if n1 > int64(dropAfter) {
		t.Errorf("first attempt: wrote %d bytes, expected ≤ %d", n1, dropAfter)
	}

	// Second attempt with Range resume from n1.
	n2, err2 := d.attemptBlockWrite(context.Background(), targetFile, opts, n1, nil)
	if err2 != nil {
		t.Fatalf("range-resume attempt failed: %v", err2)
	}

	totalWritten := n1 + n2
	if totalWritten != int64(blobSize) {
		t.Errorf("total written: got %d, want %d", totalWritten, blobSize)
	}

	// Read the file back and compare SHA256 to the original blob.
	written, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}

	wantSHA := sha256Hex(blob)
	gotSHA := sha256Hex(written)
	if gotSHA != wantSHA {
		t.Errorf("SHA256 mismatch: got %s, want %s", gotSHA, wantSHA)
	}
}

func sha256Hex(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
