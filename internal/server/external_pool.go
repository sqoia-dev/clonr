package server

// external_pool.go — wires the agent-less collector pool (Sprint 38
// Bundle A) into clustr-serverd's startup. The pool itself lives in
// internal/server/stats/external; this file owns:
//
//   - Configuration (env-driven knobs).
//   - The DB adapters that satisfy external.NodeLister + external.Store.
//   - The retention sweeper for node_external_stats and node_stats
//     rows whose expires_at has elapsed.
//
// Lifecycle: StartExternalCollectorPool is called from
// StartBackgroundWorkers and runs until ctx is cancelled.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	statsdb "github.com/sqoia-dev/clustr/internal/db/stats"
	"github.com/sqoia-dev/clustr/internal/server/stats/external"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// External pool tunables. All read once at startup; restart the
// daemon to change.
const (
	externalEnvWorkers   = "CLUSTR_EXTERNAL_POOL_WORKERS"
	externalEnvCadence   = "CLUSTR_EXTERNAL_POOL_CADENCE_SECONDS"
	externalEnvProbeTTL  = "CLUSTR_EXTERNAL_PROBE_TTL_MINUTES"
	externalEnvBMCTTL    = "CLUSTR_EXTERNAL_BMC_TTL_MINUTES"
	externalEnvSNMPTTL   = "CLUSTR_EXTERNAL_SNMP_TTL_MINUTES"
	externalEnvDisable   = "CLUSTR_EXTERNAL_POOL_DISABLE" // "1" to skip startup
	externalEnvSkipBMC   = "CLUSTR_EXTERNAL_SKIP_BMC"
	externalEnvSkipSNMP  = "CLUSTR_EXTERNAL_SKIP_SNMP"
	externalEnvSkipPing  = "CLUSTR_EXTERNAL_SKIP_PING"

	// Sweeper cadence: daily. Stale rows still serve correct
	// "current" reads (the read-side filter strips them) so a faster
	// sweep is unnecessary.
	externalSweepInterval = 24 * time.Hour
)

// externalStoreAdapter forwards UpsertExternalStat into the stats DB layer
// via the typed UpsertExternalStat method. It exists so the pool can
// be tested with a fake store that doesn't carry a *statsdb.StatsDB.
type externalStoreAdapter struct{ sdb *statsdb.StatsDB }

func (a externalStoreAdapter) UpsertExternalStat(ctx context.Context, nodeID, source string, payload []byte, lastSeen, expiresAt time.Time) error {
	return a.sdb.UpsertExternalStat(ctx, statsdb.NodeExternalStatRow{
		NodeID:     nodeID,
		Source:     statsdb.ExternalStatsSource(source),
		Payload:    json.RawMessage(payload),
		LastSeenAt: lastSeen,
		ExpiresAt:  expiresAt,
	})
}

// externalListerAdapter is the NodeLister implementation. It pulls the
// full node_configs row set, then projects each into an
// external.SourceTargets — primary IP from the first interface, BMC
// fields from the (decrypted) bmc_config blob.
//
// SNMP targets are deferred to a follow-up sprint: there's no DB
// column for "this node also speaks SNMP" yet, so we leave SNMP=nil
// for now. The pool handles nil SNMP cleanly.
type externalListerAdapter struct{ db *db.DB }

func (a externalListerAdapter) ListExternalStatTargets(ctx context.Context) ([]external.SourceTargets, error) {
	if a.db == nil {
		return nil, errors.New("external lister: nil db")
	}
	nodes, err := a.db.ListNodeConfigs(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]external.SourceTargets, 0, len(nodes))
	for _, n := range nodes {
		t := external.SourceTargets{
			NodeID: n.ID,
			HostIP: primaryHostIP(n),
		}
		if n.BMC != nil {
			t.BMCAddr = n.BMC.IPAddress
			t.BMCUser = n.BMC.Username
			t.BMCPass = n.BMC.Password
		}
		out = append(out, t)
	}
	return out, nil
}

// primaryHostIP returns the first non-empty IPv4 address from n's
// interfaces. The CIDR suffix (/24, /16, etc.) is stripped because
// PROBE-3's ICMP/SSH probes want a bare host IP, not a network. If
// every interface is unset, returns "" — the probe layer treats that
// as "skip ping/ssh" and only runs the BMC probe.
func primaryHostIP(n api.NodeConfig) string {
	for _, iface := range n.Interfaces {
		if iface.IPAddress == "" {
			continue
		}
		// Strip /<prefix> if present.
		for i := 0; i < len(iface.IPAddress); i++ {
			if iface.IPAddress[i] == '/' {
				return iface.IPAddress[:i]
			}
		}
		return iface.IPAddress
	}
	return ""
}

// StartExternalCollectorPool starts the agent-less pool unless
// disabled via env. Returns nil pool when disabled so the caller
// can skip the Stop call cleanly.
func (s *Server) StartExternalCollectorPool(ctx context.Context) *external.Pool {
	if os.Getenv(externalEnvDisable) == "1" {
		log.Info().Msg("external pool: disabled via " + externalEnvDisable)
		return nil
	}
	cfg := external.PoolConfig{
		Workers:    envInt(externalEnvWorkers, external.DefaultWorkerCount),
		Cadence:    envDuration(externalEnvCadence, external.DefaultCadence, time.Second),
		ProbeTTL:   envDuration(externalEnvProbeTTL, external.DefaultProbeTTL, time.Minute),
		BMCTTL:     envDuration(externalEnvBMCTTL, external.DefaultBMCTTL, time.Minute),
		SNMPTTL:    envDuration(externalEnvSNMPTTL, external.DefaultSNMPTTL, time.Minute),
		SkipBMC:    os.Getenv(externalEnvSkipBMC) == "1",
		SkipSNMP:   os.Getenv(externalEnvSkipSNMP) == "1",
		SkipProbes: false,
	}
	prober := external.NewProber(nil)
	if os.Getenv(externalEnvSkipPing) == "1" {
		prober.SkipPing = true
	}
	bmc := &external.BMCCollector{}
	snmp := &external.SNMPCollector{}

	pool := external.NewPool(
		cfg,
		externalStoreAdapter{sdb: s.statsDB},
		externalListerAdapter{db: s.db},
		prober,
		bmc,
		snmp,
	)
	log.Info().
		Int("workers", cfg.Workers).
		Dur("cadence", cfg.Cadence).
		Dur("probe_ttl", cfg.ProbeTTL).
		Bool("skip_bmc", cfg.SkipBMC).
		Bool("skip_snmp", cfg.SkipSNMP).
		Msg("external pool: starting")
	pool.Start(ctx)
	return pool
}

// runExternalStatsSweeper deletes both expired node_external_stats
// rows and TTL-bounded node_stats rows whose expires_at has elapsed.
// Runs every 24 hours.
func (s *Server) runExternalStatsSweeper(ctx context.Context) {
	log.Info().Dur("interval", externalSweepInterval).Msg("external stats sweeper: started")

	sweep := func() {
		now := time.Now()
		nExt, err := s.statsDB.SweepExpiredExternalStats(ctx, now)
		if err != nil {
			log.Warn().Err(err).Msg("external stats sweeper: SweepExpiredExternalStats failed")
		}
		nNS, err := s.statsDB.SweepExpiredNodeStats(ctx, now)
		if err != nil {
			log.Warn().Err(err).Msg("external stats sweeper: SweepExpiredNodeStats failed")
		}
		if nExt > 0 || nNS > 0 {
			log.Info().
				Int64("external", nExt).
				Int64("node_stats", nNS).
				Msg("external stats sweeper: deleted expired rows")
		}
	}
	sweep() // immediate first sweep so a fresh deploy doesn't carry old data 24h.

	tick := time.NewTicker(externalSweepInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			sweep()
		}
	}
}

// envInt parses an int from env, falling back to def. <=0 → def.
func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// envDuration parses an integer-multiple-of-unit from env (e.g.
// "60" with unit=time.Second → 60s) falling back to def.
func envDuration(name string, def time.Duration, unit time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return time.Duration(n) * unit
}

// ExternalStatsDBAdapter implements handlers.ExternalStatsDBIface.
// Delegates to the stats DB and bridges types back to db.NodeExternalStatRow
// for the handler layer (which uses db.* types).
type ExternalStatsDBAdapter struct{ sdb *statsdb.StatsDB }

// NewExternalStatsDBAdapter constructs an adapter around the stats DB.
func NewExternalStatsDBAdapter(sdb *statsdb.StatsDB) *ExternalStatsDBAdapter {
	return &ExternalStatsDBAdapter{sdb: sdb}
}

// ListExternalStatsForNode delegates to the stats DB and bridges types.
func (a *ExternalStatsDBAdapter) ListExternalStatsForNode(ctx context.Context, nodeID string, now time.Time) ([]db.NodeExternalStatRow, error) {
	srows, err := a.sdb.ListExternalStatsForNode(ctx, nodeID, now)
	if err != nil {
		return nil, err
	}
	rows := make([]db.NodeExternalStatRow, len(srows))
	for i, r := range srows {
		rows[i] = db.NodeExternalStatRow{
			NodeID:     r.NodeID,
			Source:     db.ExternalStatsSource(r.Source),
			Payload:    r.Payload,
			LastSeenAt: r.LastSeenAt,
			ExpiresAt:  r.ExpiresAt,
		}
	}
	return rows, nil
}
