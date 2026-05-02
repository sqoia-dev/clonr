package multicast

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// TestBitRateFlag verifies the bits/sec → udp-sender flag conversion.
func TestBitRateFlag(t *testing.T) {
	cases := []struct {
		rateBPS int64
		want    string
	}{
		{100_000_000, "800m"}, // 100 MB/s = 800 Mbps
		{10_000_000, "80m"},
		{1_000_000, "8m"},
		{125_000, "1m"},
		{1_000, "8k"},
		{100, "800"},
	}
	for _, c := range cases {
		got := bitRateFlag(c.rateBPS)
		if got != c.want {
			t.Errorf("bitRateFlag(%d) = %q, want %q", c.rateBPS, got, c.want)
		}
	}
}

// TestSenderMissingBinary verifies that Run returns ErrSenderMissing when the
// udp-sender binary is not present at BinPath.
func TestSenderMissingBinary(t *testing.T) {
	s := &Sender{BinPath: "/nonexistent/path/udp-sender"}
	sess := Session{
		ID:             "test-session",
		MulticastGroup: "239.255.42.1",
		SenderPort:     9000,
		RateBPS:        100_000_000,
	}
	err := s.Run(context.Background(), sess, bytes.NewReader(nil))
	if err == nil {
		t.Fatal("expected error when binary missing, got nil")
	}
	// Should be ErrSenderMissing sentinel.
	if err != ErrSenderMissing {
		// ErrSenderMissing is the exact error (no wrapping in the binary-absent path).
		t.Errorf("expected ErrSenderMissing, got %v", err)
	}
}

// TestSenderExitZero creates a fake binary that exits 0 and verifies Run returns nil.
func TestSenderExitZero(t *testing.T) {
	fakeBin := writeFakeBinary(t, 0)
	s := &Sender{BinPath: fakeBin}
	sess := Session{
		ID:             "sess-ok",
		MulticastGroup: "239.255.42.1",
		SenderPort:     9001,
		RateBPS:        100_000_000,
	}
	payload := []byte("fake image bytes")
	err := s.Run(context.Background(), sess, bytes.NewReader(payload))
	if err != nil {
		t.Errorf("expected nil error from fake zero-exit binary, got %v", err)
	}
}

// TestSenderExitNonZero verifies that a non-zero exit code is propagated as an error.
func TestSenderExitNonZero(t *testing.T) {
	fakeBin := writeFakeBinary(t, 1)
	s := &Sender{BinPath: fakeBin}
	sess := Session{
		ID:             "sess-fail",
		MulticastGroup: "239.255.42.1",
		SenderPort:     9002,
		RateBPS:        100_000_000,
	}
	err := s.Run(context.Background(), sess, bytes.NewReader([]byte("data")))
	if err == nil {
		t.Error("expected error from non-zero exit, got nil")
	}
}

// TestSenderContextCancellation verifies that cancelling the context stops the sender.
func TestSenderContextCancellation(t *testing.T) {
	// Write a fake binary that sleeps 60 seconds.
	fakeBin := writeSleepBinary(t, 60)
	s := &Sender{BinPath: fakeBin}
	sess := Session{
		ID:             "sess-cancel",
		MulticastGroup: "239.255.42.1",
		SenderPort:     9003,
		RateBPS:        100_000_000,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	err := s.Run(ctx, sess, bytes.NewReader(nil))
	if err == nil {
		t.Error("expected error when context already cancelled, got nil")
	}
}

// TestLimitedWriter verifies the stderr capture cap.
func TestLimitedWriter(t *testing.T) {
	w := &limitedWriter{}
	chunk := make([]byte, maxStderrCapture+1000)
	for i := range chunk {
		chunk[i] = 'x'
	}
	n, err := w.Write(chunk)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(chunk) {
		t.Errorf("Write returned %d, want %d", n, len(chunk))
	}
	if len(w.buf) > maxStderrCapture {
		t.Errorf("buf len %d > maxStderrCapture %d", len(w.buf), maxStderrCapture)
	}
}

// writeFakeBinary writes a tiny shell script to a temp dir that exits with exitCode.
// It must be executable so os.Stat succeeds and exec.Command can run it.
func writeFakeBinary(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "udp-sender")
	script := "#!/bin/sh\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	// Verify the script is executable.
	cmd := exec.Command("/bin/sh", "-c", "test -x "+path)
	if err := cmd.Run(); err != nil {
		t.Fatalf("fake binary not executable: %v", err)
	}
	return path
}

// writeSleepBinary writes a shell script that sleeps for seconds.
func writeSleepBinary(t *testing.T, seconds int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "udp-sender")
	script := "#!/bin/sh\nsleep " + strconv.Itoa(seconds) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write sleep binary: %v", err)
	}
	return path
}
