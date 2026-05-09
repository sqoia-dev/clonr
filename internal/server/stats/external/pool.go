package external

// pool.go — the agent-less collector goroutine pool.
//
// Architecture:
//
//   ticker (cadence, default 60 s)
//     │
//     ▼
//   list nodes from store          (DB roundtrip, single query)
//     │
//     ▼
//   fan out one job per node ────▶ workers (default 20)
//                                    │
//                                    ▼
//                                  Prober.RunAllOnce(t) — 3 probes
//                                    │
//                                    ▼
//                                  Store.Upsert(probe payload)
//                                    │
//                                    ├─▶ if BMC configured:
//                                    │     BMCCollector.Collect(...)
//                                    │     Store.Upsert(bmc payload)
//                                    │
//                                    └─▶ if SNMP configured:
//                                          SNMPCollector.Collect(...)
//                                          Store.Upsert(snmp payload)
//
// Worker count and cadence are configurable via
// CLUSTR_EXTERNAL_POOL_WORKERS / CLUSTR_EXTERNAL_POOL_CADENCE_SECONDS
// at startup. Defaults match the spec (20 workers, 60-second cadence).
//
// Why goroutines and not a separate process: we are intentionally
// rejecting clustervisor's separate-daemon split — that was a Python
// GIL workaround we don't import. A goroutine pool inside
// clustr-serverd shares the DB connection, the secrets manager, the
// LDAP cache, and the structured logger.
//
// Fairness: the dispatcher uses a buffered channel (size = worker
// count), so a slow node holds at most one slot at a time. If more
// than `workers` nodes are slow simultaneously, the dispatcher backs
// off and the cycle becomes longer than the ticker — that's expected
// and surfaces as the "external_stats sample older than expected"
// signal in the UI.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Defaults. Override via env in NewPool's caller.
const (
	DefaultWorkerCount    = 20
	DefaultCadence        = 60 * time.Second
	DefaultProbeTTL       = 60 * time.Minute
	DefaultBMCTTL         = 60 * time.Minute
	DefaultSNMPTTL        = 60 * time.Minute
	pollerBackpressureCap = 4 // jobs per worker before we drop a cycle
)

// PoolConfig configures the agent-less collector pool. Zero values map
// to the package defaults so callers can construct a Pool with just
// PoolConfig{} for development. Production callers wire env values.
type PoolConfig struct {
	Workers     int
	Cadence     time.Duration
	ProbeTTL    time.Duration
	BMCTTL      time.Duration
	SNMPTTL     time.Duration
	SkipBMC     bool
	SkipSNMP    bool
	SkipProbes  bool
}

// SourceTargets is the per-node tuple the pool scheduler consumes.
// Hub callers fill this from db.GetNodeConfig + secrets.Decrypt. Empty
// fields skip that probe / collector cleanly (see RunAllOnce
// behaviour).
type SourceTargets struct {
	NodeID     string
	HostIP     string
	BMCAddr    string
	BMCUser    string
	BMCPass    string
	SNMP       *SNMPTarget // nil = no SNMP poll for this node
	HostPort   int
}

// Store is the persistence interface. The DB adapter in
// internal/server implements this; tests inject an in-memory fake.
type Store interface {
	UpsertExternalStat(ctx context.Context, nodeID string, source string, payload []byte, lastSeen, expiresAt time.Time) error
}

// NodeLister supplies the per-cycle list of nodes to poll. The DB
// adapter in internal/server implements this; tests inject a static
// slice.
type NodeLister interface {
	ListExternalStatTargets(ctx context.Context) ([]SourceTargets, error)
}

// Pool is the goroutine pool that runs one cadence-driven sweep over
// the node list. Construct via NewPool; Start launches the workers
// and the dispatcher; Stop stops them via the supplied context.
type Pool struct {
	cfg      PoolConfig
	store    Store
	lister   NodeLister
	prober   *Prober
	bmc      *BMCCollector
	snmp     *SNMPCollector

	jobs   chan SourceTargets
	wg     sync.WaitGroup
	closed bool
}

// NewPool wires the pool with concrete collectors. nil collectors
// disable the corresponding source: pass nil bmc to skip BMC sweeps
// even if a node has BMCAddr set.
func NewPool(cfg PoolConfig, store Store, lister NodeLister, prober *Prober, bmc *BMCCollector, snmp *SNMPCollector) *Pool {
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultWorkerCount
	}
	if cfg.Cadence <= 0 {
		cfg.Cadence = DefaultCadence
	}
	if cfg.ProbeTTL <= 0 {
		cfg.ProbeTTL = DefaultProbeTTL
	}
	if cfg.BMCTTL <= 0 {
		cfg.BMCTTL = DefaultBMCTTL
	}
	if cfg.SNMPTTL <= 0 {
		cfg.SNMPTTL = DefaultSNMPTTL
	}
	return &Pool{
		cfg:    cfg,
		store:  store,
		lister: lister,
		prober: prober,
		bmc:    bmc,
		snmp:   snmp,
		jobs:   make(chan SourceTargets, cfg.Workers),
	}
}

// Start launches the worker goroutines and the dispatcher tick loop.
// Returns immediately; the goroutines run until ctx is cancelled.
//
// Concurrency invariant: jobs has capacity Workers; the dispatcher
// sends one job per node per cycle, blocking if all workers are busy.
// This caps the total in-flight RPCs at Workers regardless of node
// count.
func (p *Pool) Start(ctx context.Context) {
	if p.closed {
		log.Warn().Msg("external pool: Start called after Stop; ignored")
		return
	}
	for i := 0; i < p.cfg.Workers; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}
	p.wg.Add(1)
	go p.dispatcher(ctx)
}

// Wait blocks until all workers and the dispatcher have exited. Test
// helper; production callers use ctx-cancel + graceful shutdown.
func (p *Pool) Wait() { p.wg.Wait() }

// dispatcher fires every Cadence and feeds the jobs channel.
func (p *Pool) dispatcher(ctx context.Context) {
	defer p.wg.Done()

	cycle := func() {
		ctx2, cancel := context.WithTimeout(ctx, p.cfg.Cadence)
		defer cancel()
		nodes, err := p.lister.ListExternalStatTargets(ctx2)
		if err != nil {
			log.Warn().Err(err).Msg("external pool: ListExternalStatTargets failed; skipping cycle")
			return
		}
		// Backpressure: if more than Workers*pollerBackpressureCap
		// nodes are queued from the previous cycle, drop this one
		// rather than letting the queue grow unbounded.
		if len(p.jobs) >= p.cfg.Workers*pollerBackpressureCap {
			log.Warn().
				Int("queued", len(p.jobs)).
				Int("nodes", len(nodes)).
				Msg("external pool: backpressure — dropping cycle")
			return
		}
		for _, t := range nodes {
			select {
			case <-ctx.Done():
				return
			case p.jobs <- t:
			}
		}
	}

	// Run the first cycle immediately so the UI doesn't wait one
	// cadence after server startup before showing any data.
	cycle()

	tick := time.NewTicker(p.cfg.Cadence)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			cycle()
		}
	}
}

// worker pulls jobs and runs the three probes + the optional BMC /
// SNMP collectors per node. Each worker is a single goroutine; map
// state inside a worker is goroutine-local, so no mutex needed.
func (p *Pool) worker(ctx context.Context, id int) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-p.jobs:
			if !ok {
				return
			}
			p.runOne(ctx, id, t)
		}
	}
}

// runOne is the per-node hot path. Probes always run (cheap & no
// auth). BMC and SNMP run only if configured and the corresponding
// collector is non-nil.
func (p *Pool) runOne(ctx context.Context, _ int, t SourceTargets) {
	now := time.Now().UTC()

	// PROBE-3 — always run. The Prober honours empty fields
	// internally (so a node with HostIP="" still gets a {ping:false,
	// ssh:false, ipmi_mc:bool} row).
	if !p.cfg.SkipProbes && p.prober != nil {
		probe := p.prober.RunAllOnce(ctx, ProbeTargets{
			HostIP:   t.HostIP,
			BMCAddr:  t.BMCAddr,
			BMCUser:  t.BMCUser,
			BMCPass:  t.BMCPass,
			HostPort: t.HostPort,
		})
		if err := writePayload(ctx, p.store, t.NodeID, "probe", probe, now, now.Add(p.cfg.ProbeTTL)); err != nil {
			log.Warn().Err(err).Str("node_id", t.NodeID).Msg("external pool: probe upsert failed")
		}
	}

	// BMC sweep — only if configured.
	if !p.cfg.SkipBMC && p.bmc != nil && t.BMCAddr != "" {
		bmc := p.bmc.Collect(ctx, t.BMCAddr, t.BMCUser, t.BMCPass)
		if err := writePayload(ctx, p.store, t.NodeID, "bmc", bmc, now, now.Add(p.cfg.BMCTTL)); err != nil {
			log.Warn().Err(err).Str("node_id", t.NodeID).Msg("external pool: bmc upsert failed")
		}
	}

	// SNMP sweep — only if configured.
	if !p.cfg.SkipSNMP && p.snmp != nil && t.SNMP != nil && t.SNMP.Host != "" {
		snmp := p.snmp.Collect(ctx, *t.SNMP)
		if err := writePayload(ctx, p.store, t.NodeID, "snmp", snmp, now, now.Add(p.cfg.SNMPTTL)); err != nil {
			log.Warn().Err(err).Str("node_id", t.NodeID).Msg("external pool: snmp upsert failed")
		}
	}
}

// writePayload marshals payload and forwards to the store. Single
// helper so the three call sites stay aligned.
func writePayload(ctx context.Context, s Store, nodeID, source string, payload any, now, expires time.Time) error {
	if s == nil {
		return errors.New("external pool: nil store")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.UpsertExternalStat(ctx, nodeID, source, b, now, expires)
}

// Stop signals the pool to shut down and blocks until workers exit.
// Idempotent.
func (p *Pool) Stop() {
	if p.closed {
		return
	}
	p.closed = true
	close(p.jobs)
	p.wg.Wait()
}
