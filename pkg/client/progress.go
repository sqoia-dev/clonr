package client

import (
	"context"
	"sync"
	"time"

	"github.com/sqoia-dev/clonr/pkg/api"
)

// phaseOrder maps phase names to their 1-based ordinal.
// Total phases = 6.
var phaseOrder = map[string]int{
	"preflight":    1,
	"partitioning": 2,
	"formatting":   3,
	"downloading":  4,
	"extracting":   5,
	"finalizing":   6,
}

const totalPhases = 6

// ProgressReporter sends DeployProgress updates to the clonr server.
// It is safe for concurrent use and rate-limits POSTs to at most once per second
// (or on phase changes) to avoid flooding the server during fast operations.
type ProgressReporter struct {
	client       *Client
	nodeMAC      string
	hostname     string
	currentPhase string
	phaseStart   time.Time
	bytesDone    int64
	bytesTotal   int64
	lastSend     time.Time
	mu           sync.Mutex
}

// NewProgressReporter creates a ProgressReporter attached to the given client and node.
func NewProgressReporter(c *Client, nodeMAC, hostname string) *ProgressReporter {
	return &ProgressReporter{
		client:   c,
		nodeMAC:  nodeMAC,
		hostname: hostname,
	}
}

// SetNode updates the node identity (MAC and hostname) on the reporter.
// Call this once the MAC is known from hardware discovery.
func (r *ProgressReporter) SetNode(nodeMAC, hostname string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodeMAC = nodeMAC
	r.hostname = hostname
}

// StartPhase signals the beginning of a new deployment phase.
// total is the expected number of bytes (or 0 if unknown) for the phase.
func (r *ProgressReporter) StartPhase(phase string, total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentPhase = phase
	r.phaseStart = time.Now()
	r.bytesDone = 0
	r.bytesTotal = total
	r.sendLocked("", true)
}

// Update reports the current byte count within the active phase.
// Called frequently (on every Read); internally rate-limited to once per second
// or per 10% change in completion.
func (r *ProgressReporter) Update(bytesDone int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bytesDone = bytesDone
	r.sendIfDueLocked()
}

// EndPhase signals the end of the current phase. errMsg is empty on success.
func (r *ProgressReporter) EndPhase(errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if errMsg != "" {
		r.sendLocked(errMsg, true)
		return
	}
	// Mark bytes as fully done for the phase.
	if r.bytesTotal > 0 {
		r.bytesDone = r.bytesTotal
	}
	r.sendLocked("", true)
}

// Complete sends a final "complete" progress event.
func (r *ProgressReporter) Complete() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentPhase = "complete"
	r.sendLocked("", true)
}

// Fail sends a final "error" progress event.
func (r *ProgressReporter) Fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentPhase = "error"
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	r.sendLocked(errMsg, true)
}

// sendIfDueLocked sends a progress update if enough time has passed or if progress
// has changed by >= 10%. Must be called with r.mu held.
func (r *ProgressReporter) sendIfDueLocked() {
	now := time.Now()
	if now.Sub(r.lastSend) < time.Second {
		return
	}
	r.sendLocked("", false)
}

// sendLocked assembles and posts a DeployProgress. Must be called with r.mu held.
// force skips the rate-limit check.
func (r *ProgressReporter) sendLocked(errMsg string, force bool) {
	if !force {
		if time.Since(r.lastSend) < time.Second {
			return
		}
	}

	now := time.Now()
	elapsed := now.Sub(r.phaseStart).Seconds()

	var speed, eta int64
	if elapsed > 0 && r.bytesDone > 0 {
		speed = int64(float64(r.bytesDone) / elapsed)
		if r.bytesTotal > r.bytesDone && speed > 0 {
			eta = (r.bytesTotal - r.bytesDone) / speed
		}
	}

	phaseIdx := phaseOrder[r.currentPhase]

	entry := api.DeployProgress{
		NodeMAC:    r.nodeMAC,
		Hostname:   r.hostname,
		Phase:      r.currentPhase,
		PhaseIndex: phaseIdx,
		PhaseTotal: totalPhases,
		BytesDone:  r.bytesDone,
		BytesTotal: r.bytesTotal,
		Speed:      speed,
		ETA:        int(eta),
		UpdatedAt:  now.UTC(),
		Error:      errMsg,
	}

	// Fire-and-forget with a short context — progress updates are best-effort.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	go func() {
		defer cancel()
		_ = r.client.SendDeployProgress(ctx, entry)
	}()

	r.lastSend = now
}
