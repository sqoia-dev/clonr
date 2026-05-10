package server

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/metrics"
)

// techTrigTickInterval is how often the tech-trig evaluator runs.
// 10 minutes is a slow-path background check; these signals evolve over weeks.
const techTrigTickInterval = 10 * time.Minute

//lint:ignore U1000 used by the tech-trigger contention checker once SQLITE_BUSY instrumentation lands (TECHTRIG-CONTENTION)
// contentionWindow is the window over which SQLITE_BUSY events are counted for T1.
// 5 minutes of sustained contention (>=5/sec = 300 events in 5 min) fires the trigger.
const contentionWindow = 5 * time.Minute

// contentionThresholdPerSec is the T1 write-contention threshold.
// Pick: 5 SQLITE_BUSY events/second sustained for contentionWindow.
// Rationale: WAL mode should make contention rare at our scale; 5/sec sustained for
// 5 minutes (1500 events) indicates genuine write saturation, not transient blips.
// This is more conservative than the D27 spec ("N ops/sec") deliberately — we don't
// want false-positive noise from bursty admin operations.
const contentionThresholdPerSec float64 = 5.0

// t1NodeCountThreshold is the T1 node-count threshold.
const t1NodeCountThreshold = 500

// t4LogBytesThreshold is the T4 log-storage threshold: 50 GiB.
const t4LogBytesThreshold int64 = 50 * 1024 * 1024 * 1024

// sqliteBusyCount tracks the rolling count of SQLITE_BUSY/LOCKED errors seen by
// DB operations across the process lifetime. Incremented by db.DB when contention
// is detected. The evaluator reads a snapshot on each tick and computes the rate.
// Declared as a package-level atomic so it can be incremented from the db package
// without circular imports (the db package calls a registered callback instead).
var sqliteBusyCount int64

// IncrSQLiteBusyCount increments the process-lifetime SQLITE_BUSY counter.
// Called by the DB layer whenever it encounters a busy/locked error.
func IncrSQLiteBusyCount() {
	atomic.AddInt64(&sqliteBusyCount, 1)
}

// runTechTrigEvaluator ticks every 10 minutes, evaluates all four TECH-TRIG signals,
// updates tech_trig_state, refreshes Prometheus metrics, and sends an admin notification
// email on the first firing transition for each trigger.
func (s *Server) runTechTrigEvaluator(ctx context.Context) {
	log.Info().
		Str("tick", techTrigTickInterval.String()).
		Int("t1_node_threshold", t1NodeCountThreshold).
		Float64("t1_contention_threshold_per_sec", contentionThresholdPerSec).
		Int64("t4_log_bytes_threshold", t4LogBytesThreshold).
		Msg("tech-trig evaluator: started")

	// Run immediately at startup so values are populated before the first admin login.
	s.evaluateTechTrigs(ctx)

	ticker := time.NewTicker(techTrigTickInterval)
	defer ticker.Stop()

	// busySnapshotAt and busySnapshotCount are used to compute the rolling
	// contention rate between ticks.
	var busySnapshotAt time.Time
	var busySnapshotCount int64

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("tech-trig evaluator: stopping")
			return
		case <-ticker.C:
			// Compute T1 contention rate since last tick.
			now := time.Now()
			curBusy := atomic.LoadInt64(&sqliteBusyCount)
			var contentionRate float64
			if !busySnapshotAt.IsZero() {
				elapsed := now.Sub(busySnapshotAt).Seconds()
				if elapsed > 0 {
					contentionRate = float64(curBusy-busySnapshotCount) / elapsed
				}
			}
			busySnapshotAt = now
			busySnapshotCount = curBusy

			// Store the computed contention rate for use by evaluateTechTrigs.
			s.lastContentionRate.Store(contentionRate)

			s.evaluateTechTrigs(ctx)
		}
	}
}

// lastContentionRate caches the most recent contention rate computed between ticks.
// Stored on Server so evaluateTechTrigs can read it.
// We store it as a uint64 bit-pattern of a float64 using atomic.

// evaluateTechTrigs runs the evaluation logic for all four triggers.
// Called on startup and every tick.
func (s *Server) evaluateTechTrigs(ctx context.Context) {
	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// ─── T1: PostgreSQL migration trigger ────────────────────────────────────
	s.evalT1(ctx2)

	// ─── T2: Framework ceiling trigger ───────────────────────────────────────
	s.evalT2(ctx2)

	// ─── T3: Multi-tenant trigger ─────────────────────────────────────────────
	// T3 is purely manual; the evaluator only refreshes Prometheus to reflect the
	// current manual_signal value. No automatic firing logic.
	s.evalT3(ctx2)

	// ─── T4: Hot/cold log archive trigger ────────────────────────────────────
	s.evalT4(ctx2)
}

func (s *Server) evalT1(ctx context.Context) {
	nodeCount, err := s.db.CountNodes(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("tech-trig T1: count nodes failed (skipping)")
		return
	}

	contentionRate := s.lastContentionRate.Load()

	valueJSON, err := db.T1ValueJSON(nodeCount, contentionRate)
	if err != nil {
		log.Warn().Err(err).Msg("tech-trig T1: marshal value json failed")
		return
	}

	fired := nodeCount >= t1NodeCountThreshold || contentionRate >= contentionThresholdPerSec
	wasAlreadyFired, _ := s.db.WasTechTrigAlreadyFired(ctx, db.TechTrigPostgreSQL)

	if err := s.db.UpdateTechTrigState(ctx, db.TechTrigPostgreSQL, valueJSON, fired); err != nil {
		log.Warn().Err(err).Msg("tech-trig T1: update state failed")
		return
	}

	// Prometheus.
	firedVal := float64(0)
	if fired {
		firedVal = 1
	}
	metrics.TechTrigFired.WithLabelValues(string(db.TechTrigPostgreSQL)).Set(firedVal)
	metrics.TechTrigValue.WithLabelValues(string(db.TechTrigPostgreSQL)).Set(float64(nodeCount))

	// First-fire notification.
	if fired && !wasAlreadyFired {
		s.sendTechTrigNotification(ctx, db.TechTrigPostgreSQL,
			"T1 PostgreSQL migration trigger fired",
			"The SQLite cluster has reached the PostgreSQL migration threshold.\n\n"+
				"Details:\n"+
				"  Node count: "+intToStr(nodeCount)+" (threshold: "+intToStr(t1NodeCountThreshold)+")\n"+
				"  Contention rate: "+floatToStr(contentionRate)+" events/sec (threshold: "+floatToStr(contentionThresholdPerSec)+")\n\n"+
				"Action: consult docs/tech-triggers.md for the PostgreSQL migration sprint spec.\n",
		)
		s.audit.Record(ctx, "system", "clustr", "tech_trig.fired",
			"tech_trigger", string(db.TechTrigPostgreSQL), "",
			nil, map[string]interface{}{
				"node_count":       nodeCount,
				"contention_rate":  contentionRate,
				"threshold_nodes":  t1NodeCountThreshold,
				"threshold_conten": contentionThresholdPerSec,
			})
		log.Warn().
			Int("node_count", nodeCount).
			Float64("contention_rate", contentionRate).
			Msg("tech-trig T1: FIRED — PostgreSQL migration threshold crossed")
	}
}

func (s *Server) evalT2(ctx context.Context) {
	// T2 fires only on manual_signal now that the legacy JS UI is gone.
	state, stateErr := s.db.GetTechTrigState(ctx, db.TechTrigFramework)
	manualSignal := stateErr == nil && state.ManualSignal

	valueJSON, err := db.T2ValueJSON(0)
	if err != nil {
		log.Warn().Err(err).Msg("tech-trig T2: marshal value json failed")
		return
	}

	fired := manualSignal
	wasAlreadyFired, _ := s.db.WasTechTrigAlreadyFired(ctx, db.TechTrigFramework)

	if err := s.db.UpdateTechTrigState(ctx, db.TechTrigFramework, valueJSON, fired); err != nil {
		log.Warn().Err(err).Msg("tech-trig T2: update state failed")
		return
	}

	firedVal := float64(0)
	if fired {
		firedVal = 1
	}
	metrics.TechTrigFired.WithLabelValues(string(db.TechTrigFramework)).Set(firedVal)
	metrics.TechTrigValue.WithLabelValues(string(db.TechTrigFramework)).Set(0)

	if fired && !wasAlreadyFired {
		s.sendTechTrigNotification(ctx, db.TechTrigFramework,
			"T2 Framework ceiling trigger fired",
			"The frontend framework ceiling trigger was manually signalled.\n\n"+
				"Action: consult SPRINT.md for the webapp rebuild sprint spec.\n",
		)
		s.audit.Record(ctx, "system", "clustr", "tech_trig.fired",
			"tech_trigger", string(db.TechTrigFramework), "",
			nil, map[string]interface{}{
				"manual_signal": manualSignal,
			})
		log.Warn().
			Bool("manual_signal", manualSignal).
			Msg("tech-trig T2: FIRED — operator-marked framework ceiling")
	}
}

func (s *Server) evalT3(ctx context.Context) {
	state, err := s.db.GetTechTrigState(ctx, db.TechTrigMultiTenant)
	if err != nil {
		return
	}
	firedVal := float64(0)
	if state.Fired() {
		firedVal = 1
	}
	metrics.TechTrigFired.WithLabelValues(string(db.TechTrigMultiTenant)).Set(firedVal)
	metrics.TechTrigValue.WithLabelValues(string(db.TechTrigMultiTenant)).Set(0)
}

func (s *Server) evalT4(ctx context.Context) {
	logBytes, err := s.db.MeasureLogBytes(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("tech-trig T4: measure log bytes failed")
		return
	}
	valueJSON, err := db.T4ValueJSON(logBytes)
	if err != nil {
		log.Warn().Err(err).Msg("tech-trig T4: marshal value json failed")
		return
	}

	fired := logBytes >= t4LogBytesThreshold
	wasAlreadyFired, _ := s.db.WasTechTrigAlreadyFired(ctx, db.TechTrigLogArchive)

	if err := s.db.UpdateTechTrigState(ctx, db.TechTrigLogArchive, valueJSON, fired); err != nil {
		log.Warn().Err(err).Msg("tech-trig T4: update state failed")
		return
	}

	firedVal := float64(0)
	if fired {
		firedVal = 1
	}
	metrics.TechTrigFired.WithLabelValues(string(db.TechTrigLogArchive)).Set(firedVal)
	metrics.TechTrigValue.WithLabelValues(string(db.TechTrigLogArchive)).Set(float64(logBytes))

	if fired && !wasAlreadyFired {
		s.sendTechTrigNotification(ctx, db.TechTrigLogArchive,
			"T4 Log archive trigger fired",
			"Estimated log storage has exceeded the hot/cold archive threshold.\n\n"+
				"Current log bytes: "+int64ToStr(logBytes)+
				" (threshold: "+int64ToStr(t4LogBytesThreshold)+" = 50 GiB)\n\n"+
				"Action: consult docs/tech-triggers.md for the log archive sprint spec.\n",
		)
		s.audit.Record(ctx, "system", "clustr", "tech_trig.fired",
			"tech_trigger", string(db.TechTrigLogArchive), "",
			nil, map[string]interface{}{
				"log_bytes":  logBytes,
				"threshold":  t4LogBytesThreshold,
			})
		log.Warn().
			Int64("log_bytes", logBytes).
			Int64("threshold", t4LogBytesThreshold).
			Msg("tech-trig T4: FIRED — log storage threshold crossed")
	}
}

// sendTechTrigNotification emails all admin users when a trigger first fires.
// Best-effort: failures are logged but never block the evaluator.
func (s *Server) sendTechTrigNotification(ctx context.Context, name db.TechTrigName, subject, body string) {
	if s.notifier == nil || !s.notifier.Mailer.IsConfigured() {
		log.Info().
			Str("trigger", string(name)).
			Str("subject", subject).
			Msg("tech-trig: notification skipped (SMTP not configured)")
		return
	}

	emails, err := s.db.GetAdminEmails(ctx)
	if err != nil || len(emails) == 0 {
		log.Warn().Err(err).Str("trigger", string(name)).
			Msg("tech-trig: no admin emails found; skipping notification")
		return
	}

	if err := s.notifier.Mailer.Send(ctx, emails, "[clustr] "+subject, body); err != nil {
		log.Error().Err(err).
			Str("trigger", string(name)).
			Msg("tech-trig: notification send failed")
		return
	}
	log.Info().
		Str("trigger", string(name)).
		Strs("recipients", emails).
		Msg("tech-trig: notification sent")
}

// ─── atomic float64 helper ───────────────────────────────────────────────────

// atomicFloat64 stores a float64 value using atomic int64 bit-casting.
// Avoids adding a sync.Mutex just for one scalar.
type atomicFloat64 struct{ v int64 }

func (a *atomicFloat64) Store(f float64) {
	// Encode f as a scaled integer (×1e9), then store as int64.
	// See f2bits/bits2f: this is a fixed-precision approximation sufficient
	// for a contention-rate counter in the 0–1000 events/sec range.
	atomic.StoreInt64(&a.v, int64(f2bits(f))) //nolint:gosec // G115: int64 max (9.2e18) >> max value (uint64(1000*1e9) = 1e12); overflow is impossible.
}

func (a *atomicFloat64) Load() float64 {
	return bits2f(uint64(atomic.LoadInt64(&a.v)))
}

// f2bits and bits2f are float64 ↔ uint64 bit-cast helpers that avoid importing
// math/bits or unsafe directly. We use simple arithmetic equivalence here.
func f2bits(f float64) uint64 {
	// Encode: use the standard encoding/binary approach but inline it.
	// We just need a stable round-trip; exact bit pattern is fine.
	// Multiply by 1e9 and store as int64 truncated — good enough for a rate counter
	// (0–1000 range, 9 decimal places of precision).
	// This avoids any unsafe/math.Float64bits dependency.
	if f < 0 {
		f = 0
	}
	return uint64(f * 1e9)
}

func bits2f(u uint64) float64 {
	return float64(u) / 1e9
}

// ─── string conversion helpers (avoid fmt import for perf) ───────────────────

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

func int64ToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

func floatToStr(f float64) string {
	// Simple fixed-precision for log messages: 2 decimal places.
	// Avoids strconv/fmt imports in this file.
	i := int64(f)
	frac := int64((f - float64(i)) * 100)
	if frac < 0 {
		frac = -frac
	}
	s := int64ToStr(i) + "."
	if frac < 10 {
		s += "0"
	}
	s += int64ToStr(frac)
	return s
}
