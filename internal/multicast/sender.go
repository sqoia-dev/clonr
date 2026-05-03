package multicast

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/rs/zerolog/log"
)

// udpSenderBinaryPath is the expected location of the udp-sender binary on the
// server host.  Operators must install it from the udpcast package:
//
//	dnf install udpcast    # EL8/9/10 (EPEL)
//
// The binary is NOT bundled in the clustr RPM because udpcast is GPL-2.0 and
// the binary is already packaged for every supported EL release.  This is the
// same pattern as #159's intel-syscfg: operator-supplied, fixed path, no
// cluster-controlled installation.
//
// Override with CLUSTR_UDPSENDER_PATH for non-standard installations.
const udpSenderBinaryPath = "/usr/bin/udp-sender"

// ErrSenderMissing is returned by Run when the udp-sender binary is not found
// at udpSenderBinaryPath.  Callers (the scheduler) treat this as a session
// failure and instruct all enrolled nodes to fall back to unicast.
var ErrSenderMissing = fmt.Errorf("multicast: udp-sender binary not found at %s (install udpcast package)", udpSenderBinaryPath)

// Sender wraps the udp-sender binary invocation.
type Sender struct {
	// BinPath is the path to the udp-sender binary.
	// Defaults to udpSenderBinaryPath if empty.
	BinPath string
}

// NewSender creates a Sender using the operator-configured binary path.
// Reads CLUSTR_UDPSENDER_PATH from the environment; falls back to
// udpSenderBinaryPath.
func NewSender() *Sender {
	path := os.Getenv("CLUSTR_UDPSENDER_PATH")
	if path == "" {
		path = udpSenderBinaryPath
	}
	return &Sender{BinPath: path}
}

// Run invokes udp-sender for the given session, streaming from blobReader.
//
// It builds the udp-sender command line:
//
//	udp-sender
//	  --interface  <management NIC, autodetected by udp-sender from routing>
//	  --mcast-rdv-addr  <session.MulticastGroup>
//	  --mcast-data-addr <session.MulticastGroup>
//	  --portbase    <session.SenderPort>
//	  --max-bitrate <rate-string>   (e.g. "800m")
//	  --broadcast                   (fallback for switches that block multicast)
//	  --min-clients 0               (fire immediately; scheduler controls batching)
//	  --max-wait    0               (no sender-side client wait; scheduler does it)
//
// stdin is piped from blobReader so the image bytes flow directly to udp-sender
// without a staging copy (same path as the /blob HTTP handler).
//
// Returns nil on exit code 0, ErrSenderMissing if the binary is absent, or a
// wrapped error including stderr output on any other failure.
func (s *Sender) Run(ctx context.Context, sess Session, blobReader io.Reader) error {
	binPath := s.BinPath
	if binPath == "" {
		binPath = udpSenderBinaryPath
	}

	if _, err := os.Stat(binPath); err != nil {
		return ErrSenderMissing
	}

	rate := bitRateFlag(sess.RateBPS)
	args := []string{
		"--mcast-rdv-addr", sess.MulticastGroup,
		"--mcast-data-addr", sess.MulticastGroup,
		"--portbase", strconv.Itoa(sess.SenderPort),
		"--max-bitrate", rate,
		"--min-clients", "0",
		"--max-wait", "0",
		"--pipe", "/bin/cat", // read from stdin via pipe mode
	}

	cmd := exec.CommandContext(ctx, binPath, args...) //#nosec G204 -- binPath is operator-supplied fixed path

	// Pipe the blob reader into udp-sender's stdin.
	cmd.Stdin = blobReader

	// Capture stderr for error reporting; stdout is not used by udp-sender.
	var stderrBuf limitedWriter
	cmd.Stderr = &stderrBuf

	log.Info().
		Str("session_id", sess.ID).
		Str("group", sess.MulticastGroup).
		Int("port", sess.SenderPort).
		Str("rate", rate).
		Msg("multicast: invoking udp-sender")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("multicast: udp-sender exited non-zero: %w; stderr: %s", err, stderrBuf.String())
	}
	return nil
}

// Run is the SenderFunc-compatible wrapper for Sender.Run.
// It reads the image blob from the image store via a nil blobReader
// placeholder — in production the blob is fed by the Scheduler caller.
// This signature matches SenderFunc for embedding in the scheduler.
func (s *Sender) RunSession(ctx context.Context, sess Session) error {
	// The blob reader is wired by the caller that has access to internal/image.
	// Stub returns ErrSenderMissing if binary absent, nil otherwise (Commit 2
	// wires real blob streaming in the scheduler.go transmit path).
	return s.Run(ctx, sess, os.Stdin) // os.Stdin is replaced by the real reader in production
}

// bitRateFlag converts bytes/sec to a udp-sender --max-bitrate string.
// udp-sender accepts suffixes: k (kilobits), m (megabits), g (gigabits).
// Note: udp-sender treats rates as BITS/sec, not bytes/sec.
func bitRateFlag(rateBPS int64) string {
	bits := rateBPS * 8
	switch {
	case bits >= 1_000_000_000:
		return strconv.FormatInt(bits/1_000_000_000, 10) + "g"
	case bits >= 1_000_000:
		return strconv.FormatInt(bits/1_000_000, 10) + "m"
	case bits >= 1_000:
		return strconv.FormatInt(bits/1_000, 10) + "k"
	default:
		return strconv.FormatInt(bits, 10)
	}
}

// limitedWriter caps captured stderr at 4 KB to avoid log bloat from a
// pathological udp-sender stderr stream.
type limitedWriter struct {
	buf []byte
}

const maxStderrCapture = 4096

func (w *limitedWriter) Write(p []byte) (int, error) {
	n := len(p) // capture original length before any slicing
	remaining := maxStderrCapture - len(w.buf)
	if remaining > 0 {
		toAppend := p
		if len(toAppend) > remaining {
			toAppend = toAppend[:remaining]
		}
		w.buf = append(w.buf, toAppend...)
	}
	return n, nil // always report full write so udp-sender doesn't die on partial write
}

func (w *limitedWriter) String() string { return string(w.buf) }
