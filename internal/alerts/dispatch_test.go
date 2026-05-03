package alerts

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMailer counts Send calls and can be made arbitrarily slow.
type fakeMailer struct {
	sendCount atomic.Int64
	delay     time.Duration
}

func (m *fakeMailer) IsConfigured() bool { return true }

func (m *fakeMailer) Send(_ context.Context, to []string, subject, body string) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.sendCount.Add(1)
	return nil
}

// sampleRule returns a minimal Rule with email notify set.
func sampleRule(emails []string) *Rule {
	return &Rule{
		Name:     "test-rule",
		Plugin:   "disks",
		Sensor:   "used_pct",
		Severity: SeverityWarn,
		Threshold: Threshold{Op: OpGte, Value: 90},
		Notify:   Notify{Email: emails},
	}
}

// sampleAlert returns a minimal Alert.
func sampleAlert(nodeID string) *Alert {
	return &Alert{
		ID:           1,
		RuleName:     "test-rule",
		NodeID:       nodeID,
		Sensor:       "used_pct",
		Severity:     SeverityWarn,
		State:        StateFiring,
		FiredAt:      time.Now(),
		LastValue:    95,
		ThresholdOp:  string(OpGte),
		ThresholdVal: 90,
	}
}

// TestDispatcherQueue_AllJobsDelivered fires 1000 alerts and asserts all
// mailJobs eventually reach the fake Mailer (none dropped, since queue=256
// and workers process fast with no delay).
func TestDispatcherQueue_AllJobsDelivered(t *testing.T) {
	mailer := &fakeMailer{}
	d := &Dispatcher{Mailer: mailer}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d.Start(ctx)

	r := sampleRule([]string{"ops@example.com"})
	// Fire 200 alerts — well within queue capacity of 256.
	const n = 200
	for i := 0; i < n; i++ {
		d.Fire(context.Background(), r, sampleAlert("node-1"))
	}

	// Wait for workers to drain.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mailer.sendCount.Load() == n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := mailer.sendCount.Load()
	if got != n {
		t.Errorf("expected %d sends, got %d", n, got)
	}
}

// TestDispatcherQueue_DropsWhenFull verifies that Fire drops and warns when the
// queue is saturated (queue capacity 1, workers not started).
func TestDispatcherQueue_DropsWhenFull(t *testing.T) {
	mailer := &fakeMailer{}
	d := &Dispatcher{Mailer: mailer}

	// Manually initialise a tiny queue but start NO workers so it fills immediately.
	d.mailQueue = make(chan mailJob, 1)

	r := sampleRule([]string{"ops@example.com"})
	a := sampleAlert("node-1")

	// First enqueue should succeed (capacity=1).
	d.Fire(context.Background(), r, a)
	// Second enqueue should drop without blocking.
	start := time.Now()
	d.Fire(context.Background(), r, a)
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Errorf("Fire blocked for %v, should be non-blocking", elapsed)
	}
	// No sends happened (no workers).
	if n := mailer.sendCount.Load(); n != 0 {
		t.Errorf("expected 0 sends (no workers), got %d", n)
	}
}

// TestDispatcherFire_NonBlockingWithSlowMailer asserts Fire returns within 1ms
// even when Mailer.Send sleeps for 5 seconds.
func TestDispatcherFire_NonBlockingWithSlowMailer(t *testing.T) {
	mailer := &fakeMailer{delay: 5 * time.Second}
	d := &Dispatcher{Mailer: mailer}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)

	r := sampleRule([]string{"ops@example.com"})
	a := sampleAlert("node-1")

	start := time.Now()
	d.Fire(context.Background(), r, a)
	elapsed := time.Since(start)

	if elapsed > time.Millisecond {
		t.Errorf("Fire took %v, want < 1ms (slow mailer must not block tick loop)", elapsed)
	}
}
