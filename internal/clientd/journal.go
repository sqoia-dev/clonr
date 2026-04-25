package clientd

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
)

const (
	journalBatchSize    = 50
	journalBatchTimeout = 500 * time.Millisecond
)

// journalEntry is a minimal subset of the journalctl JSON output fields we care about.
type journalEntry struct {
	Message           string `json:"MESSAGE"`
	Hostname          string `json:"_HOSTNAME"`
	Priority          string `json:"PRIORITY"`
	SystemdUnit       string `json:"_SYSTEMD_UNIT"`
	RealtimeTimestamp string `json:"__REALTIME_TIMESTAMP"`
}

// JournalStreamer forks a journalctl process and batches its output as api.LogEntry slices.
// Batches are sent on the output channel: up to journalBatchSize entries OR journalBatchTimeout,
// whichever comes first.
type JournalStreamer struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	output chan []api.LogEntry

	nodeMAC string
}

// NewJournalStreamer creates a JournalStreamer that will tail journal entries for
// the given systemd units (empty = all units), at or above the given priority
// (0=emerg … 7=debug, 0 = show all), since the given timestamp string (empty = now).
// nodeMAC is stamped onto every LogEntry so the server can route entries correctly.
func NewJournalStreamer(units []string, priority int, since, nodeMAC string) *JournalStreamer {
	return &JournalStreamer{
		output:  make(chan []api.LogEntry, 8),
		nodeMAC: nodeMAC,
	}
}

// Start forks journalctl and begins reading. Returns an error if journalctl
// cannot be started. The goroutine exits when ctx is cancelled or journalctl exits.
func (j *JournalStreamer) Start(ctx context.Context, units []string, priority int, since string) error {
	cmdCtx, cancel := context.WithCancel(ctx)
	j.cancel = cancel

	args := []string{"-f", "-o", "json", "--no-pager"}
	for _, u := range units {
		if u != "" {
			args = append(args, "--unit="+u)
		}
	}
	if priority >= 0 && priority <= 7 {
		args = append(args, "--priority="+strconv.Itoa(priority))
	}
	if since != "" {
		args = append(args, "--since="+since)
	}

	cmd := exec.CommandContext(cmdCtx, "journalctl", args...)
	cmd.Stderr = nil // discard journalctl's own stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	j.cmd = cmd

	hostname, _ := os.Hostname()

	go func() {
		defer cancel()
		defer func() {
			_ = cmd.Wait()
			close(j.output)
		}()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 256*1024), 256*1024)

		batch := make([]api.LogEntry, 0, journalBatchSize)
		timer := time.NewTimer(journalBatchTimeout)
		defer timer.Stop()

		flush := func() {
			if len(batch) == 0 {
				return
			}
			toSend := make([]api.LogEntry, len(batch))
			copy(toSend, batch)
			batch = batch[:0]
			select {
			case j.output <- toSend:
			case <-cmdCtx.Done():
			}
		}

		// We need to drive both the scanner (blocking) and the timer (async)
		// concurrently. We put the scanner in a goroutine that sends parsed
		// entries on a local channel, then the outer select handles both.
		// lineCh is closed by the scanner goroutine when it exits.
		// The outer select uses lineCh closure (ok==false) as its exit signal.
		lineCh := make(chan api.LogEntry, 32)

		go func() {
			defer close(lineCh)
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					continue
				}
				entry := parseJournalLine(line, hostname, j.nodeMAC)
				if entry == nil {
					continue
				}
				select {
				case lineCh <- *entry:
				case <-cmdCtx.Done():
					return
				}
			}
			if err := scanner.Err(); err != nil && cmdCtx.Err() == nil {
				log.Warn().Err(err).Msg("clientd journal: scanner error")
			}
		}()

		for {
			select {
			case <-cmdCtx.Done():
				flush()
				// Drain lineCh so the scanner goroutine can exit cleanly.
				go func() {
					for range lineCh {
					}
				}()
				return

			case entry, ok := <-lineCh:
				if !ok {
					// Scanner goroutine finished and closed lineCh — flush and exit.
					flush()
					return
				}
				batch = append(batch, entry)
				if len(batch) >= journalBatchSize {
					flush()
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(journalBatchTimeout)
				}

			case <-timer.C:
				flush()
				timer.Reset(journalBatchTimeout)
			}
		}
	}()

	return nil
}

// Stop cancels the journalctl subprocess and closes the output channel.
func (j *JournalStreamer) Stop() {
	if j.cancel != nil {
		j.cancel()
	}
	if j.cmd != nil && j.cmd.Process != nil {
		_ = j.cmd.Process.Kill()
	}
}

// Batches returns the read-only channel of entry batches.
func (j *JournalStreamer) Batches() <-chan []api.LogEntry {
	return j.output
}

// parseJournalLine parses one JSON line from journalctl -o json into a LogEntry.
// Returns nil if the line is not valid journal JSON or has no message.
func parseJournalLine(line, fallbackHostname, nodeMAC string) *api.LogEntry {
	var je journalEntry
	if err := json.Unmarshal([]byte(line), &je); err != nil {
		return nil
	}

	msg := je.Message
	if msg == "" {
		return nil
	}

	// Prefix the unit name into the message for easy human reading.
	// The Component field is always "node-journal" so SSE filter by component works.
	if je.SystemdUnit != "" {
		msg = "[" + strings.TrimSuffix(je.SystemdUnit, ".service") + "] " + msg
	}

	hostname := je.Hostname
	if hostname == "" {
		hostname = fallbackHostname
	}

	// Map PRIORITY (syslog severity, 0=emerg, 7=debug) → clustr level string.
	level := mapPriority(je.Priority)

	// __REALTIME_TIMESTAMP is microseconds since the Unix epoch as a string.
	ts := parseRealtimeTimestamp(je.RealtimeTimestamp)

	return &api.LogEntry{
		ID:        uuid.New().String(),
		NodeMAC:   nodeMAC,
		Hostname:  hostname,
		Level:     level,
		Component: "node-journal",
		Message:   msg,
		Timestamp: ts,
	}
}

// mapPriority maps a syslog priority string (0-7) to a clustr log level string.
func mapPriority(p string) string {
	switch p {
	case "0", "1", "2": // emerg, alert, crit
		return "error"
	case "3": // err
		return "error"
	case "4": // warning
		return "warn"
	case "5": // notice
		return "info"
	case "6", "7": // info, debug
		return "info"
	default:
		return "info"
	}
}

// parseRealtimeTimestamp parses __REALTIME_TIMESTAMP (microseconds since epoch)
// into a time.Time. Falls back to time.Now() on any error.
func parseRealtimeTimestamp(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	us, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Now().UTC()
	}
	return time.Unix(us/1_000_000, (us%1_000_000)*1000).UTC()
}
