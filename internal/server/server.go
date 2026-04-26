// Package server provides the clustr-serverd HTTP API built on chi.
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/image"
	ldapmodule "github.com/sqoia-dev/clustr/internal/ldap"
	networkmodule "github.com/sqoia-dev/clustr/internal/network"
	slurmmodule "github.com/sqoia-dev/clustr/internal/slurm"
	"github.com/sqoia-dev/clustr/internal/metrics"
	"github.com/sqoia-dev/clustr/internal/power"
	"github.com/sqoia-dev/clustr/internal/sysaccounts"
	ipmipower "github.com/sqoia-dev/clustr/internal/power/ipmi"
	proxmoxpower "github.com/sqoia-dev/clustr/internal/power/proxmox"
	"github.com/sqoia-dev/clustr/internal/reimage"
	"github.com/sqoia-dev/clustr/internal/server/handlers"
	"github.com/sqoia-dev/clustr/internal/server/ui"
	"github.com/sqoia-dev/clustr/internal/webhook"
)

// BuildInfo holds build-time metadata injected via -ldflags.
type BuildInfo struct {
	Version   string
	CommitSHA string
	BuildTime string
}

// Server wraps the HTTP server and all its dependencies.
type Server struct {
	cfg                 config.ServerConfig
	db                  *db.DB
	audit               *db.AuditService
	broker              *LogBroker
	progress            *ProgressStore
	buildProgress       *BuildProgressStore
	shells              *image.ShellManager
	powerCache          *PowerCache
	powerRegistry       *power.Registry
	reimageOrchestrator *reimage.Orchestrator
	ldapMgr             *ldapmodule.Manager
	sysAccountsMgr      *sysaccounts.Manager
	networkMgr          *networkmodule.Manager
	slurmMgr            *slurmmodule.Manager
	clientdHub          *ClientdHub
	webhookDispatcher   *webhook.Dispatcher
	sessionSecret       []byte // HMAC key for browser session tokens
	router              chi.Router
	http                *http.Server
	logsHandler         *handlers.LogsHandler
	imgFactory          *image.Factory
	buildInfo           BuildInfo

	// flipBackFailureCount tracks verify-boot flipNodeToDiskFirst failures for
	// the /health endpoint (S4-9). Incremented atomically; read without lock for
	// health response since occasional skew is acceptable.
	flipBackFailureCount int64

	// dhcpLeaseLookup is set after construction by SetDHCPServer when the PXE
	// server is available. The NodesHandler captures it via a closure so it picks
	// up the live function even after the router is built.
	dhcpLeaseLookup func(mac string) net.IP
}

// buildProgressAdapter adapts *BuildProgressStore to image.BuildProgressReporter.
// The image package defines an interface with Start returning a BuildHandle interface;
// this adapter bridges the concrete server types to that interface.
type buildProgressAdapter struct {
	store *BuildProgressStore
}

// buildHandleAdapter wraps *BuildHandle (server) to satisfy image.BuildHandle.
type buildHandleAdapter struct {
	h *BuildHandle
}

func (a buildHandleAdapter) SetPhase(phase string)      { a.h.SetPhase(phase) }
func (a buildHandleAdapter) SetProgress(d, t int64)     { a.h.SetProgress(d, t) }
func (a buildHandleAdapter) AddSerialLine(line string)   { a.h.AddSerialLine(line) }
func (a buildHandleAdapter) AddStderrLine(line string)   { a.h.AddStderrLine(line) }
func (a buildHandleAdapter) Fail(msg string)             { a.h.Fail(msg) }
func (a buildHandleAdapter) Complete()                   { a.h.Complete() }

func (a buildProgressAdapter) Start(imageID string) image.BuildHandle {
	h := a.store.Start(imageID)
	return buildHandleAdapter{h: h}
}

// New creates a Server wired with the given config and database.
func New(cfg config.ServerConfig, database *db.DB, info BuildInfo) *Server {
	// Build the power provider registry and register all supported backends.
	registry := power.NewRegistry()
	ipmipower.Register(registry)
	proxmoxpower.Register(registry)

	reimageOrch := reimage.New(database, registry, log.Logger)

	shells := image.NewShellManager(database, cfg.ImageDir, log.Logger)
	buildProg := NewBuildProgressStore(cfg.ImageDir)

	// Resolve or generate the session HMAC secret.
	var secret []byte
	if cfg.SessionSecret != "" {
		secret = []byte(cfg.SessionSecret)
	} else {
		var err error
		secret, err = generateSessionSecret()
		if err != nil {
			log.Fatal().Err(err).Msg("server: failed to generate session secret")
		}
		log.Warn().Msg("CLUSTR_SESSION_SECRET not set — generated ephemeral session secret (sessions will not survive restarts)")
	}

	ldapMgr := ldapmodule.New(cfg, database)
	sysAccountsMgr := sysaccounts.New(database)
	networkMgr := networkmodule.New(database)

	// clientdHub must be created before SlurmManager so the hub reference is valid.
	hub := NewClientdHub()

	slurmMgr := slurmmodule.New(database, hub)

	s := &Server{
		cfg:                 cfg,
		db:                  database,
		audit:               db.NewAuditService(database),
		broker:              NewLogBroker(),
		progress:            NewProgressStore(),
		buildProgress:       buildProg,
		shells:              shells,
		powerCache:          NewPowerCache(15 * time.Second),
		powerRegistry:       registry,
		reimageOrchestrator: reimageOrch,
		ldapMgr:             ldapMgr,
		sysAccountsMgr:      sysAccountsMgr,
		networkMgr:          networkMgr,
		slurmMgr:            slurmMgr,
		clientdHub:          hub,
		webhookDispatcher:   webhook.New(database, log.Logger),
		sessionSecret:       secret,
		buildInfo:           info,
	}
	s.router = s.buildRouter()
	s.http = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      300 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// NetworkManager returns the server's network module manager.
// Used by main to wire external callbacks (e.g. DHCP switch auto-discovery).
func (s *Server) NetworkManager() *networkmodule.Manager {
	return s.networkMgr
}

// SetDHCPLeaseLookup wires a DHCP lease lookup function so the registration
// handler can auto-populate node interfaces from DHCP-assigned IPs. Call this
// after server.New() when the PXE server is available. Safe to call with nil
// to disable the feature (e.g. when PXE is disabled).
func (s *Server) SetDHCPLeaseLookup(fn func(mac string) net.IP) {
	s.dhcpLeaseLookup = fn
}

// lookupDHCPLease is the closure passed to NodesHandler. It delegates to the
// dhcpLeaseLookup field, which may be set after router construction.
func (s *Server) lookupDHCPLease(mac string) net.IP {
	if s.dhcpLeaseLookup == nil {
		return nil
	}
	return s.dhcpLeaseLookup(mac)
}

// StartBackgroundWorkers starts long-running background goroutines.
// Call this after New() and before ListenAndServe().
func (s *Server) StartBackgroundWorkers(ctx context.Context) {
	// Wire the server-lifetime context into the logs ingest handler so that
	// client disconnects (r.Context() cancellations) do not abort in-flight
	// SQLite log-batch transactions and silently drop deploy logs.
	s.logsHandler.ServerCtx = ctx
	// Wire shutdown context into the image factory so async build goroutines
	// are cancelled on graceful shutdown and the semaphore respects context.
	if s.imgFactory != nil {
		s.imgFactory.SetContext(ctx)
	}
	go s.reimageOrchestrator.Scheduler(ctx)
	go s.runLogPurger(ctx)
	go s.runAuditPurger(ctx)
	go s.runDiskSpaceMonitor(ctx)
	// ADR-0008: Post-reboot verification timeout scanner.
	go s.runVerifyTimeoutScanner(ctx)
	// S4-1: Prometheus gauge collector.
	go s.runMetricsCollector(ctx)
	// S4-3: Reimage-pending reaper — clears orphaned reimage_pending flags.
	go s.runReimagePendingReaper(ctx)
	// S4-4: Resume any group reimage jobs that were running before this process started.
	s.resumeRunningGroupReimageJobs(ctx)
	// LDAP module health checker.
	s.ldapMgr.StartBackgroundWorkers(ctx)
	// Slurm module health checker.
	s.slurmMgr.StartBackgroundWorkers(ctx)
}

// defaultFlipSemCap is the default max concurrent flipNodeToDiskFirst goroutines
// in the verify-boot timeout scanner (S4-6). Override with CLUSTR_FLIP_CONCURRENCY.
const defaultFlipSemCap = 5

// scannerFlipSemaphore returns a buffered channel used as a semaphore to bound
// the number of concurrent flipNodeToDiskFirst calls in the verify-boot scanner.
// A new channel is created on each tick cycle — cheap (just scanner goroutines,
// not a hot path) and avoids shared state between tick cycles.
func scannerFlipSemaphore() chan struct{} {
	cap := defaultFlipSemCap
	if v := os.Getenv("CLUSTR_FLIP_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cap = n
		}
	}
	return make(chan struct{}, cap)
}

// runVerifyTimeoutScanner ticks every 60 seconds and marks as timed-out any node
// that has deploy_completed_preboot_at set but no deploy_verified_booted_at within
// CLUSTR_VERIFY_TIMEOUT. ADR-0008.
func (s *Server) runVerifyTimeoutScanner(ctx context.Context) {
	timeout := s.cfg.VerifyTimeout
	if timeout == 0 {
		timeout = 5 * time.Minute // safe default if config somehow zero
	}
	log.Info().Str("timeout", timeout.String()).Msg("verify-boot scanner: started — post-reboot verification timeout set to " + timeout.String())

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("verify-boot scanner: stopping")
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-timeout)
			nodes, err := s.db.ListNodesAwaitingVerification(ctx, cutoff)
			if err != nil {
				log.Error().Err(err).Msg("verify-boot scanner: ListNodesAwaitingVerification failed")
				continue
			}
			// S4-6: Fan out flipNodeToDiskFirst calls via goroutines with a bounded
			// semaphore (default 5 concurrent) to prevent the scanner from blocking
			// sequentially on simultaneous timeouts at 200-node clusters.
			flipSem := scannerFlipSemaphore()
			var wg sync.WaitGroup
			for _, n := range nodes {
				n := n // capture
				wg.Add(1)
				flipSem <- struct{}{} // acquire slot
				go func() {
					defer func() { <-flipSem; wg.Done() }()

					if err := s.db.RecordVerifyTimeout(ctx, n.ID); err != nil {
						log.Error().Err(err).Str("node_id", n.ID).Str("hostname", n.Hostname).
							Msg("verify-boot scanner: RecordVerifyTimeout failed")
						return
					}
					// S4-2: fire verify_boot.timeout webhook.
					if s.webhookDispatcher != nil {
						s.webhookDispatcher.Dispatch(ctx, webhook.EventVerifyBootTimeout, webhook.Payload{
							NodeID:  n.ID,
							ImageID: n.BaseImageID,
						})
					}
					log.Warn().
						Str("node_id", n.ID).
						Str("hostname", n.Hostname).
						Str("timeout", timeout.String()).
						Msgf("verify-boot scanner: node %s (%s) did not phone home within %s of deploy-complete — possible bootloader failure, kernel panic, or /etc/clustr/node-token not written correctly",
							n.ID, n.Hostname, timeout)
					// Flip persistent boot order back to disk-first on deploy-timeout.
					// Prevents Proxmox VMs from being stuck PXE-first forever when the
					// deploy completes but the node never calls verify-boot.
					// Best-effort: errors are logged, not fatal. See docs/boot-architecture.md §10.
					if err := s.flipNodeToDiskFirst(ctx, n.ID); err != nil {
						log.Warn().Err(err).Str("node_id", n.ID).Str("hostname", n.Hostname).
							Msg("verify-boot scanner: FlipToDiskFirst failed on deploy-timeout (non-fatal)")
						// S4-9: track flip-back failures in Prometheus and health endpoint.
						metrics.FlipBackFailures.Inc()
						atomic.AddInt64(&s.flipBackFailureCount, 1)
					} else {
						log.Info().Str("node_id", n.ID).Str("hostname", n.Hostname).
							Msg("verify-boot scanner: persistent boot order flipped to disk-first after deploy-timeout")
					}
				}()
			}
			wg.Wait()
		}
	}
}

// flipNodeToDiskFirst resolves the power provider for nodeID and calls
// SetPersistentBootOrder([BootDisk, BootPXE]) to restore the disk-first
// persistent boot order after a successful deploy or deploy-timeout.
//
// On Proxmox this triggers an explicit stop+start (via the provider's
// SetPersistentBootOrder implementation) so the config change is committed.
// On IPMI this is a best-effort harmless reaffirmation of the one-shot
// override that was already consumed on the previous boot.
//
// Returns an error if the provider cannot be resolved or the call fails.
// Callers should treat errors as non-fatal warnings, not hard failures.
//
// See docs/boot-architecture.md §10.
func (s *Server) flipNodeToDiskFirst(ctx context.Context, nodeID string) error {
	node, err := s.db.GetNodeConfig(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("flipNodeToDiskFirst: load node %s: %w", nodeID, err)
	}

	var provCfg power.ProviderConfig
	switch {
	case node.PowerProvider != nil && node.PowerProvider.Type != "":
		provCfg = power.ProviderConfig{
			Type:   node.PowerProvider.Type,
			Fields: node.PowerProvider.Fields,
		}
	case node.BMC != nil && node.BMC.IPAddress != "":
		provCfg = power.ProviderConfig{
			Type: "ipmi",
			Fields: map[string]string{
				"host":     node.BMC.IPAddress,
				"username": node.BMC.Username,
				"password": node.BMC.Password,
			},
		}
	default:
		// No provider configured — nothing to flip. Bare-metal nodes with no
		// power provider use operator-managed BMC boot order; no clustr action needed.
		return nil
	}

	provider, err := s.powerRegistry.Create(provCfg)
	if err != nil {
		return fmt.Errorf("flipNodeToDiskFirst: resolve provider for node %s: %w", nodeID, err)
	}

	if err := provider.SetPersistentBootOrder(ctx, []power.BootDevice{power.BootDisk, power.BootPXE}); err != nil {
		if errors.Is(err, power.ErrNotSupported) {
			// Provider has no persistent-order concept — that's fine.
			return nil
		}
		return fmt.Errorf("flipNodeToDiskFirst: SetPersistentBootOrder for node %s: %w", nodeID, err)
	}
	return nil
}

// runLogPurger ticks every hour and applies two-pass log eviction (D2):
//
//  Pass 1 — TTL: delete rows older than CLUSTR_LOG_RETENTION (default 7d).
//  Pass 2 — per-node cap: for each node exceeding CLUSTR_LOG_MAX_ROWS_PER_NODE
//            (default 50000), delete the oldest rows until it is at the cap.
//
// Each cycle appends one row to node_logs_summary for audit purposes.
// Uses the server-lifetime context so it shuts down cleanly on SIGTERM.
func (s *Server) runLogPurger(ctx context.Context) {
	retention := 7 * 24 * time.Hour // default 7 days (D2: changed from 14d)
	if v := s.cfg.LogRetention; v != 0 {
		retention = v
	}
	maxRowsPerNode := int64(50000) // default 50K rows per node (D2)
	if v := s.cfg.LogMaxRowsPerNode; v != 0 {
		maxRowsPerNode = v
	}
	log.Info().
		Str("retention", retention.String()).
		Int64("max_rows_per_node", maxRowsPerNode).
		Msg("log purger: started")

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("log purger: stopping")
			return
		case <-ticker.C:
			// Pass 1: TTL eviction.
			olderThan := time.Now().Add(-retention)
			ttlRows, err := s.db.PurgeLogs(ctx, olderThan)
			if err != nil {
				log.Error().Err(err).Str("older_than", olderThan.Format(time.RFC3339)).
					Msg("log purger: TTL pass failed")
				continue
			}

			// Pass 2: per-node cap eviction.
			capRows, nodesAffected, err := s.db.PurgeLogsPerNodeCap(ctx, maxRowsPerNode)
			if err != nil {
				log.Error().Err(err).Int64("max_rows_per_node", maxRowsPerNode).
					Msg("log purger: per-node cap pass failed")
				// Don't skip summary; record what we have so far.
			}

			total := ttlRows + capRows
			log.Info().
				Int64("ttl_rows", ttlRows).
				Int64("cap_rows", capRows).
				Int64("total_rows", total).
				Int64("nodes_capped", nodesAffected).
				Str("retention", retention.String()).
				Int64("max_rows_per_node", maxRowsPerNode).
				Msg("log purger: purge complete")

			// Record summary event for audit trail.
			summary := db.LogPurgeSummaryRow{
				ID:            generatePurgeID(),
				PurgedAt:      time.Now().UTC(),
				TTLRows:       ttlRows,
				CapRows:       capRows,
				TotalRows:     total,
				RetentionSecs: int64(retention.Seconds()),
				MaxRowsCap:    maxRowsPerNode,
				NodeCount:     nodesAffected,
			}
			if serr := s.db.RecordLogPurgeSummary(ctx, summary); serr != nil {
				log.Warn().Err(serr).Msg("log purger: failed to record summary (non-fatal)")
			}
		}
	}
}

// generatePurgeID returns a short unique ID for a purge summary row.
func generatePurgeID() string {
	return fmt.Sprintf("purge-%d", time.Now().UnixNano())
}

// runAuditPurger ticks every hour and deletes audit_log rows older than
// CLUSTR_AUDIT_RETENTION (default 90 days, D13).
func (s *Server) runAuditPurger(ctx context.Context) {
	retention := 90 * 24 * time.Hour // default 90 days (D13)
	if v := s.cfg.AuditRetention; v != 0 {
		retention = v
	}
	log.Info().Str("retention", retention.String()).Msg("audit purger: started")

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("audit purger: stopping")
			return
		case <-ticker.C:
			olderThan := time.Now().Add(-retention)
			n, err := s.db.PurgeAuditLog(ctx, olderThan)
			if err != nil {
				log.Error().Err(err).Msg("audit purger: failed")
				continue
			}
			if n > 0 {
				log.Info().Int64("rows", n).Str("older_than", olderThan.Format(time.RFC3339)).
					Msg("audit purger: purged rows")
			}
		}
	}
}

// diskSpaceThresholds are the disk usage fractions at which we warn / error / fatal.
const (
	diskWarnThreshold  = 0.80
	diskErrorThreshold = 0.90
	diskFatalThreshold = 0.95
)

// runDiskSpaceMonitor checks disk space on CLUSTR_IMAGE_DIR every 15 minutes
// and logs WARN at 80%, ERROR at 90%, FATAL+exit at 95% (S3-9).
func (s *Server) runDiskSpaceMonitor(ctx context.Context) {
	checkDisk := func() bool {
		pct, err := diskUsagePct(s.cfg.ImageDir)
		if err != nil {
			log.Warn().Err(err).Str("dir", s.cfg.ImageDir).Msg("disk monitor: could not check usage (non-fatal)")
			return true
		}
		switch {
		case pct >= diskFatalThreshold:
			log.Error().
				Str("dir", s.cfg.ImageDir).
				Str("usage", fmt.Sprintf("%.1f%%", pct*100)).
				Msg("disk space CRITICAL: image directory is ≥95% full — shutting down to prevent data corruption")
			return false // signal caller to exit
		case pct >= diskErrorThreshold:
			log.Error().
				Str("dir", s.cfg.ImageDir).
				Str("usage", fmt.Sprintf("%.1f%%", pct*100)).
				Msg("disk space ERROR: image directory is ≥90% full — free space immediately")
		case pct >= diskWarnThreshold:
			log.Warn().
				Str("dir", s.cfg.ImageDir).
				Str("usage", fmt.Sprintf("%.1f%%", pct*100)).
				Msg("disk space WARNING: image directory is ≥80% full")
		}
		return true
	}

	// Initial check at startup.
	if ok := checkDisk(); !ok {
		// Fatal — exit the process so systemd restarts it after space is freed.
		log.Fatal().Msg("disk space >95%: refusing to continue")
		return
	}

	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ok := checkDisk(); !ok {
				log.Fatal().Msg("disk space >95%: shutting down")
				return
			}
		}
	}
}

// buildRouter constructs the chi router and registers all routes.
func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()

	// Global middleware stack.
	r.Use(panicRecovery)
	r.Use(corsMiddleware) // CORS before logging so preflight OPTIONS are handled cleanly
	r.Use(requestLogger)
	r.Use(chimiddleware.StripSlashes)
	r.Use(apiVersionHeader) // sets API-Version: v1 on all /api/v1/* responses

	if s.cfg.AuthDevMode {
		log.Warn().Msg("CLUSTR_AUTH_DEV_MODE=1 — authentication is DISABLED (dev mode only, never use in production)")
	}
	// apiKeyAuth is applied only to the /api/v1 subrouter below,
	// so that the embedded web UI at / and /ui/* is always accessible.

	// Build the auth handler — wire DB lookup and session sign/validate functions
	// so the handler doesn't import the server package (avoids circular import).
	authH := s.buildAuthHandler()

	// Derive public server URL from listen addr for boot script generation.
	// Use net.SplitHostPort to extract only the port from ListenAddr (which may
	// be "0.0.0.0:8080"), then combine it with the PXE ServerIP.
	_, port, splitErr := net.SplitHostPort(s.cfg.ListenAddr)
	if splitErr != nil {
		// ListenAddr had no port component — fall back to the raw value.
		port = s.cfg.ListenAddr
	}
	var serverURL string
	if s.cfg.PXE.ServerIP != "" {
		serverURL = "http://" + s.cfg.PXE.ServerIP + ":" + port
	} else {
		// Fallback: use localhost when PXE is not configured.
		serverURL = "http://localhost:" + port
	}

	// Handler instances.
	apiKeysH := s.buildAPIKeysHandler()
	usersH := s.buildUsersHandler()
	auditH := &handlers.AuditHandler{DB: s.db}
	// Audit service and actor-info closure are wired after getActorInfo is defined below.

	health := &handlers.HealthHandler{
		Version:          s.buildInfo.Version,
		CommitSHA:        s.buildInfo.CommitSHA,
		BuildTime:        s.buildInfo.BuildTime,
		DB:               s.db,
		BootDir:          s.cfg.PXE.BootDir,
		InitramfsPath:    s.cfg.PXE.BootDir + "/initramfs-clustr.img",
		FlipBackFailures: &s.flipBackFailureCount,
	}
	// getActorInfo extracts (actorID, actorLabel) from a request context.
	// actorID is users.id for session auth, api_keys.id for Bearer auth, or "".
	// actorLabel is "user:<id>" or "key:<label>" for display in audit log.
	getActorInfo := func(r *http.Request) (string, string) {
		if uid := userIDFromContext(r.Context()); uid != "" {
			return uid, "user:" + uid
		}
		if kid := keyIDFromContext(r.Context()); kid != "" {
			label := keyLabelFromContext(r.Context())
			if label == "" {
				label = kid
			}
			return kid, "key:" + label
		}
		return "", actorLabel(r.Context())
	}
	// Wire audit + actor info into handlers that need it.
	usersH.Audit = s.audit
	usersH.GetActorInfo = getActorInfo

	images := &handlers.ImagesHandler{
		DB:                s.db,
		ImageDir:          s.cfg.ImageDir,
		Progress:          s.progress,
		Audit:             s.audit,
		GetActorInfo:      getActorInfo,
		WebhookDispatcher: s.webhookDispatcher,
	}
	nodes := &handlers.NodesHandler{
		DB:                s.db,
		Audit:             s.audit,
		GetActorInfo:      getActorInfo,
		FlipToDiskFirst:   s.flipNodeToDiskFirst,
		WebhookDispatcher: s.webhookDispatcher,
		LDAPNodeConfig: func(ctx context.Context) (*api.LDAPNodeConfig, error) {
			return s.ldapMgr.NodeConfig(ctx)
		},
		RecordNodeLDAPConfigured: func(ctx context.Context, nodeID, configHash string) error {
			return s.ldapMgr.RecordNodeConfigured(ctx, nodeID, configHash)
		},
		SystemAccountsConfig: func(ctx context.Context) (*api.SystemAccountsNodeConfig, error) {
			return s.sysAccountsMgr.NodeConfig(ctx)
		},
		NetworkConfig: func(ctx context.Context, groupID string) (*api.NetworkNodeConfig, error) {
			return s.networkMgr.NodeNetworkConfig(ctx, groupID)
		},
		SlurmNodeConfig: func(ctx context.Context, nodeID string) (*api.SlurmNodeConfig, error) {
			return s.slurmMgr.NodeConfig(ctx, nodeID)
		},
		SudoersNodeConfig: func(ctx context.Context) (*api.SudoersNodeConfig, error) {
			return s.ldapMgr.SudoersNodeConfig(ctx)
		},
		LookupDHCPLease: s.lookupDHCPLease,
		DHCPSubnetCIDR:  s.cfg.PXE.SubnetCIDR,
		ServerIP:        s.cfg.PXE.ServerIP,
	}
	nodeGroups := &handlers.NodeGroupsHandler{
		DB:           s.db,
		Orchestrator: s.reimageOrchestrator,
		Audit:        s.audit,
		GetActorInfo: getActorInfo,
	}
	layoutH := &handlers.LayoutHandler{DB: s.db}
	// Use NewFactory so the build semaphore is initialised (capacity from
	// CLUSTR_MAX_CONCURRENT_BUILDS, default 4). Context is wired later via
	// SetContext in StartBackgroundWorkers once the server-lifetime ctx exists.
	if s.imgFactory == nil {
		s.imgFactory = image.NewFactory(
			s.db,
			s.cfg.ImageDir,
			log.Logger,
			buildProgressAdapter{store: s.buildProgress},
			"",
		)
	}
	imgFactory := s.imgFactory
	factory := &handlers.FactoryHandler{
		DB:       s.db,
		ImageDir: s.cfg.ImageDir,
		Factory:  imgFactory,
		Shells:   s.shells,
	}
	buildProgressH := &handlers.BuildProgressHandler{
		Store:    s.buildProgress,
		ImageDir: s.cfg.ImageDir,
	}
	resumeH := &handlers.ResumeHandler{
		DB:       s.db,
		ImageDir: s.cfg.ImageDir,
		Factory:  imgFactory,
	}
	initramfsH := &handlers.InitramfsHandler{
		DB:            s.db,
		ScriptPath:    "scripts/build-initramfs.sh", // ignored at runtime — script is embedded
		InitramfsPath: s.cfg.PXE.BootDir + "/initramfs-clustr.img",
		ClustrBinPath:  s.cfg.ClustrBinPath, // abs path to clustr CLI binary; defaults to /usr/local/bin/clustr
	}
	// Prime the in-memory sha256 cache from the on-disk initramfs (if present).
	// Non-fatal: if the file does not yet exist the cache stays empty and the
	// live-entry guard in DeleteInitramfsHistory simply skips the check until
	// the first successful rebuild.
	initramfsH.InitLiveSHA256()
	logs := &handlers.LogsHandler{DB: s.db, Broker: s.broker, Hub: s.clientdHub}
	s.logsHandler = logs
	progress := &handlers.ProgressHandler{Store: s.progress}
	ipmiH := &handlers.IPMIHandler{DB: s.db, Cache: s.powerCache, Registry: s.powerRegistry}
	powerH := &handlers.PowerHandler{DB: s.db, Registry: s.powerRegistry}
	reimageH := &handlers.ReimageHandler{
		DB:           s.db,
		Orchestrator: s.reimageOrchestrator,
		Audit:        s.audit,
		GetActorLabel: func(r *http.Request) string {
			return actorLabel(r.Context())
		},
		GetActorInfo: getActorInfo,
	}
	boot := &handlers.BootHandler{
		BootDir:   s.cfg.PXE.BootDir,
		TFTPDir:   s.cfg.PXE.TFTPDir,
		ServerURL: serverURL,
		Version:   s.buildInfo.Version,
		DB:        s.db,
		MintNodeToken: func(nodeID string) (string, error) {
			return CreateNodeScopedKey(context.Background(), s.db, nodeID)
		},
	}

	// S4-1: Prometheus metrics endpoint — unauthenticated so scrapers can reach it
	// without managing API keys. Restrict at the network/reverse-proxy level if needed.
	r.Get("/metrics", (&handlers.MetricsHandler{}).ServeHTTP)

	// Embedded web UI — served without bearer auth.
	// The UI JavaScript talks to /api/v1 which enforces auth when a token is set.
	staticFS, _ := fs.Sub(ui.StaticFiles, "static")
	fileServer := http.FileServer(http.FS(staticFS))
	r.Handle("/ui/*", http.StripPrefix("/ui", fileServer))
	r.Get("/", serveIndex(staticFS))
	// /login — dedicated login page (served from same static FS as the main UI).
	r.Get("/login", serveLoginPage(staticFS))
	// /set-password — forced first-login password change page.
	r.Get("/set-password", serveSetPasswordPage(staticFS))

	r.Route("/api/v1", func(r chi.Router) {
		// All /api/v1 routes: resolve the API key scope from the Bearer token
		// or the session cookie (ADR-0006). Public endpoints (boot files, node
		// register, logs) accept node-scope keys OR unauthenticated requests.
		r.Use(apiKeyAuth(s.db, s.cfg.AuthDevMode, s.sessionSecret, s.cfg.SessionSecure))

		// Auth endpoints — no scope required (login is pre-auth by definition).
		r.Post("/auth/login", authH.HandleLogin)
		r.Post("/auth/logout", authH.HandleLogout)
		r.Get("/auth/me", authH.HandleMe)
		// set-password requires a valid session (even during forced-change flow).
		r.Post("/auth/set-password", authH.HandleSetPassword)

		// Readiness probe — unauthenticated so Docker Compose healthchecks, smoke tests,
		// and the README Quick Start can all call it without credentials. Returns 200
		// with JSON if healthy, 503 with reason map if not. (GAP-2)
		r.Get("/healthz/ready", health.ServeReady)

		// Fully public — no key required (PXE-booted nodes before any key is issued).
		r.Get("/boot/ipxe", boot.ServeIPXEScript)
		r.Get("/boot/vmlinuz", boot.ServeVMLinuz)
		r.Get("/boot/initramfs.img", boot.ServeInitramfs)
		r.Get("/boot/ipxe.efi", boot.ServeIPXEEFI)
		r.Get("/boot/undionly.kpxe", boot.ServeUndionlyKPXE)

		// Node-scope callbacks — accept both node and admin keys, or no key (legacy PXE nodes).
		r.Post("/nodes/register", nodes.RegisterNode)
		r.Post("/logs", logs.IngestLogs)
		// POST /deploy/progress is intentionally outside the admin-only group.
		// The deploy agent running in initramfs calls this endpoint using its
		// node-scoped API key (minted at PXE-serve time). Placing it inside
		// the admin group would require admin-scoped keys in the initramfs,
		// which violates the least-privilege design (node keys can only interact
		// with their own node's resources). GET paths for progress are inside the
		// admin group below — only operators read the aggregated progress stream.
		r.Post("/deploy/progress", progress.IngestProgress)

		// Deploy lifecycle callbacks — require node-scope auth where the key's bound
		// node_id must match the URL {id}. Admin keys also pass (for manual overrides).
		// These are intentionally outside the admin-only group so the deploy agent
		// running in initramfs can call them using its node-scoped key.
		r.With(requireNodeOwnership("id")).Post("/nodes/{id}/deploy-complete", nodes.DeployComplete)
		r.With(requireNodeOwnership("id")).Post("/nodes/{id}/deploy-failed", nodes.DeployFailed)

		// ADR-0008: Post-reboot verification phone-home endpoint.
		// Called by the deployed OS (via clustr-verify-boot.service systemd oneshot)
		// on first boot. Node-scoped token required; admin keys are NOT accepted here.
		// The node-scoped key written to /etc/clustr/node-token at finalize time is
		// the same one minted during PXE enrollment and is reused post-boot.
		r.With(requireNodeOwnership("id")).Post("/nodes/{id}/verify-boot", nodes.VerifyBoot)

		// flip-to-disk — called by the deploy agent (node-scoped key) after writing
		// the rootfs to signal the server to set next boot to disk and power-cycle.
		// Must be outside the admin-only group: UEFI nodes use a node-scoped key and
		// would get 403 if this were admin-only. Admin keys also pass (manual override).
		r.With(requireNodeOwnership("id")).Post("/nodes/{id}/power/flip-to-disk", powerH.FlipToDisk)

		// Self-read: allow a node-scoped key to read its own node record.
		// Used by the deploy agent's state verification loop after deploy-complete.
		// The chi router matches the most specific (longest) path first, so the
		// admin-only GET /nodes/{id} below still applies for admin keys; this route
		// is only reached by node-scoped keys (requireNodeOwnership allows both).
		r.With(requireNodeOwnership("id")).Get("/nodes/{id}/self", nodes.GetNode)

		// clustr-clientd WebSocket endpoint — node-scoped key required; the key's
		// bound node_id must match the {id} URL parameter (same as verify-boot).
		clientdH := &handlers.ClientdHandler{
			DB:     s.db,
			Hub:    s.clientdHub,
			Broker: s.broker,
			SudoersNodeConfig: func(ctx context.Context) (*api.SudoersNodeConfig, error) {
				return s.ldapMgr.SudoersNodeConfig(ctx)
			},
		}
		r.With(requireNodeOwnership("id")).Get("/nodes/{id}/clientd/ws", clientdH.HandleClientdWS)

		// Image fetch routes accessible by node-scoped keys (deploy agent reads its assigned image).
		// requireImageAccess handles both admin and node scopes; node keys may only access the
		// image currently assigned to their bound node. Must be outside the admin-only group.
		r.With(requireImageAccess("id", s.db)).Get("/images/{id}", images.GetImage)
		r.With(requireImageAccess("id", s.db)).Get("/images/{id}/blob", images.DownloadBlob)

		// Admin-only routes — require admin scope.
		r.Group(func(r chi.Router) {
			r.Use(requireScope(true)) // admin scope required

			// API key management — admin role only (operators cannot manage API keys).
			r.With(requireRole("admin")).Get("/admin/api-keys", apiKeysH.HandleList)
			r.With(requireRole("admin")).Post("/admin/api-keys", apiKeysH.HandleCreate)
			r.With(requireRole("admin")).Delete("/admin/api-keys/{id}", apiKeysH.HandleRevoke)
			r.With(requireRole("admin")).Post("/admin/api-keys/{id}/rotate", apiKeysH.HandleRotate)

			// S4-2: Webhook subscription management — admin role only.
			webhooksH := &handlers.WebhooksHandler{DB: s.db}
			r.With(requireRole("admin")).Get("/admin/webhooks", webhooksH.HandleList)
			r.With(requireRole("admin")).Post("/admin/webhooks", webhooksH.HandleCreate)
			r.With(requireRole("admin")).Get("/admin/webhooks/{id}", webhooksH.HandleGet)
			r.With(requireRole("admin")).Put("/admin/webhooks/{id}", webhooksH.HandleUpdate)
			r.With(requireRole("admin")).Delete("/admin/webhooks/{id}", webhooksH.HandleDelete)
			r.With(requireRole("admin")).Get("/admin/webhooks/{id}/deliveries", webhooksH.HandleListDeliveries)

			// User management (ADR-0007) — admin role only (operator cannot manage users).
			// GET /admin/users includes group_ids for each user (S3-3).
			r.With(requireRole("admin")).Get("/admin/users", usersH.HandleListWithMemberships)
			r.With(requireRole("admin")).Post("/admin/users", usersH.HandleCreate)
			r.With(requireRole("admin")).Put("/admin/users/{id}", usersH.HandleUpdate)
			r.With(requireRole("admin")).Post("/admin/users/{id}/reset-password", usersH.HandleResetPassword)
			r.With(requireRole("admin")).Delete("/admin/users/{id}", usersH.HandleDelete)
			// GAP-21: /api/v1/users CRUD aliases — Sprint 3 docs and the walkthrough
			// expect these paths; /admin/users is the canonical path but /users also works.
			r.With(requireRole("admin")).Get("/users", usersH.HandleListWithMemberships)
			r.With(requireRole("admin")).Post("/users", usersH.HandleCreate)
			r.With(requireRole("admin")).Get("/users/{id}", usersH.HandleGetUser)
			r.With(requireRole("admin")).Put("/users/{id}", usersH.HandleUpdate)
			r.With(requireRole("admin")).Delete("/users/{id}", usersH.HandleDelete)
			// Group membership assignment (S3-3).
			r.With(requireRole("admin")).Get("/users/{id}/group-memberships", usersH.HandleGetGroupMemberships)
			r.With(requireRole("admin")).Put("/users/{id}/group-memberships", usersH.HandleSetGroupMemberships)

			// Audit log (S3-4) — admin only (operators and readonly cannot read audit log).
			r.With(requireRole("admin")).Get("/audit", auditH.HandleQuery)

			// Health — liveness probe (existing).
			r.Get("/health", health.ServeHTTP)

			// Images — mutating operations are admin-only.
			// GET /images/{id} and GET /images/{id}/blob are registered above with
			// requireImageAccess so node keys can also reach them.
			r.Get("/images", images.ListImages)
			r.Post("/images", images.CreateImage)
			r.Delete("/images/{id}", images.DeleteImage)
			r.Get("/images/{id}/status", images.GetImageStatus)
			r.Get("/images/{id}/disklayout", images.GetDiskLayout)
			r.Put("/images/{id}/disklayout", images.PutDiskLayout)
			r.Post("/images/{id}/blob", images.UploadBlob)
			r.Get("/images/{id}/metadata", images.GetImageMetadata)
			r.Put("/images/{id}/tags", images.UpdateImageTags)

			// Factory
			r.Get("/image-roles", factory.ListImageRoles)
			r.Post("/factory/pull", factory.Pull)
			r.Post("/factory/import", factory.Import)
			r.Post("/factory/import-path", factory.ImportPath)
			r.Post("/factory/import-iso", factory.ImportPath) // alias used by the web UI
			r.Post("/factory/capture", factory.Capture)
			r.Post("/factory/probe-iso", factory.ProbeISO)
			r.Post("/factory/build-from-iso", factory.BuildFromISO)

			// ISO build observability — stream must come before plain snapshot route.
			r.Get("/images/{id}/build-progress/stream", buildProgressH.StreamBuildProgress)
			r.Get("/images/{id}/build-progress", buildProgressH.GetBuildProgress)
			r.Get("/images/{id}/build-log", buildProgressH.GetBuildLog)
			r.Get("/images/{id}/build-manifest", buildProgressH.GetBuildManifest)

			// Build resume (F2) — resume an interrupted build from last phase.
			r.Post("/images/{id}/resume", resumeH.ResumeImageBuild)

			// System initramfs management (F1).
			r.Get("/system/initramfs", initramfsH.GetInitramfs)
			r.Post("/system/initramfs/rebuild", initramfsH.RebuildInitramfs)
			r.Delete("/system/initramfs/history/{id}", initramfsH.DeleteInitramfsHistory)

			// Shell sessions
			r.Post("/images/{id}/shell-session", factory.OpenShellSession)
			r.Delete("/images/{id}/shell-session/{sid}", factory.CloseShellSession)
			r.Post("/images/{id}/shell-session/{sid}/exec", factory.ExecInSession)
			r.Get("/images/{id}/shell-session/{sid}/ws", factory.ShellWS)

			// Active deploy detection (for shell modal warning)
			r.Get("/images/{id}/active-deploys", factory.ActiveDeploys)

			// Nodes — by-mac must be before /{id} to avoid chi match ambiguity.
			// nodes/connected must be before nodes/{id} to avoid chi match ambiguity.
			r.Get("/nodes/by-mac/{mac}", nodes.GetNodeByMAC)
			r.Get("/nodes/connected", clientdH.GetConnectedNodes)
			r.Get("/nodes", nodes.ListNodes)
			r.Post("/nodes", nodes.CreateNode)
			r.Get("/nodes/{id}", nodes.GetNode)
			// PUT and DELETE require admin or group-scoped operator access.
			r.With(requireGroupAccess("id", s.db)).Put("/nodes/{id}", nodes.UpdateNode)
			r.With(requireGroupAccess("id", s.db)).Delete("/nodes/{id}", nodes.DeleteNode)

			// S5-12: Node config change history (admin-only audit trail).
			configHistoryH := &handlers.NodeConfigHistoryHandler{DB: s.db}
			r.With(requireRole("admin")).Get("/nodes/{id}/config-history", configHistoryH.HandleList)

			// clientd heartbeat — admin read of latest heartbeat data.
			r.Get("/nodes/{id}/heartbeat", clientdH.GetHeartbeat)

			// Config push — push a whitelisted config file to a live node.
			r.Put("/nodes/{id}/config-push", clientdH.ConfigPush)

			// Remote exec — run a whitelisted diagnostic command on a live node.
			r.Post("/nodes/{id}/exec", clientdH.ExecOnNode)

			// Disk layout hierarchy — node-level overrides, group assignment,
			// hardware-aware recommendations, and validation.
			r.Get("/nodes/{id}/layout-recommendation", layoutH.GetLayoutRecommendation)
			r.Get("/nodes/{id}/effective-layout", layoutH.GetEffectiveLayout)
			r.Put("/nodes/{id}/layout-override", layoutH.SetNodeLayoutOverride)
			r.Post("/nodes/{id}/layout/validate", layoutH.ValidateLayout)
			r.Put("/nodes/{id}/group", layoutH.AssignNodeGroup)
			r.Get("/nodes/{id}/effective-mounts", layoutH.GetEffectiveMounts)

			// Node groups — named sets of nodes sharing a disk layout override.
			r.Get("/node-groups", nodeGroups.ListNodeGroups)
			r.Post("/node-groups", nodeGroups.CreateNodeGroup)
			r.Get("/node-groups/{id}", nodeGroups.GetNodeGroup)
			r.Put("/node-groups/{id}", nodeGroups.UpdateNodeGroup)
			r.Delete("/node-groups/{id}", nodeGroups.DeleteNodeGroup)
			// Group membership management.
			r.Post("/node-groups/{id}/members", nodeGroups.AddGroupMembers)
			r.Delete("/node-groups/{id}/members/{node_id}", nodeGroups.RemoveGroupMember)
			// Rolling group reimage — requires admin or group-scoped operator access.
			r.With(requireGroupAccessByGroupID("id", s.db)).Post("/node-groups/{id}/reimage", nodeGroups.ReimageGroup)
			// Group reimage job status polling.
			r.Get("/reimages/jobs/{jobID}", nodeGroups.GetGroupReimageJob)
			r.Post("/reimages/jobs/{jobID}/resume", nodeGroups.ResumeGroupReimageJob)

			// IPMI / power management — subpaths of /nodes/{id} must be
			// registered in the same chi group so the auth middleware applies.
			// Read-only power status and sensors are visible to all authenticated users.
			// State-changing power ops require admin or group-scoped operator access.
			r.Get("/nodes/{id}/power", ipmiH.GetPowerStatus)
			r.With(requireGroupAccess("id", s.db)).Post("/nodes/{id}/power/on", ipmiH.PowerOn)
			r.With(requireGroupAccess("id", s.db)).Post("/nodes/{id}/power/off", ipmiH.PowerOff)
			r.With(requireGroupAccess("id", s.db)).Post("/nodes/{id}/power/cycle", ipmiH.PowerCycle)
			r.With(requireGroupAccess("id", s.db)).Post("/nodes/{id}/power/reset", ipmiH.PowerReset)
			r.With(requireGroupAccess("id", s.db)).Post("/nodes/{id}/power/pxe", ipmiH.SetBootPXE)
			r.With(requireGroupAccess("id", s.db)).Post("/nodes/{id}/power/disk", ipmiH.SetBootDisk)
			r.Get("/nodes/{id}/sensors", ipmiH.GetSensors)

			// Reimage — queue, track and retry node reimages via the power provider.
			// Create requires group-scoped operator access.
			r.With(requireGroupAccess("id", s.db)).Post("/nodes/{id}/reimage", reimageH.Create)
			// S4-10: Cancel in-flight reimage by node ID (not reimage UUID).
			r.With(requireGroupAccess("id", s.db)).Delete("/nodes/{id}/reimage/active", reimageH.CancelActiveForNode)
			r.Get("/nodes/{id}/reimage", reimageH.ListForNode)
			r.Get("/reimage/{id}", reimageH.Get)
			r.Delete("/reimage/{id}", reimageH.Cancel)
			// cancel-all-active must be registered before /{id}/retry so chi's
			// radix tree matches the literal segment before the wildcard.
			r.Post("/reimage/cancel-all-active", reimageH.CancelAllActive)
			r.Post("/reimage/{id}/retry", reimageH.Retry)
			r.Get("/reimages", reimageH.List)

			// Logs — stream must be registered before plain /logs.
			r.Get("/logs/stream", logs.StreamLogs)
			r.Get("/logs", logs.QueryLogs)

			// Deployment progress — stream must be registered before plain routes.
			r.Get("/deploy/progress/stream", progress.StreamProgress)
			r.Get("/deploy/progress/{mac}", progress.GetProgress)
			r.Get("/deploy/progress", progress.ListProgress)

			// LDAP module — admin-only management routes.
			r.With(requireRole("admin")).Group(func(r chi.Router) {
				ldapmodule.RegisterRoutes(r, s.ldapMgr)
				// Sudoers push — broadcasts the sudoers drop-in to all connected nodes.
				r.Post("/ldap/sudoers/push", clientdH.HandleSudoersPush)
			})

			// System Accounts module — admin-only management routes.
			r.With(requireRole("admin")).Group(func(r chi.Router) {
				sysaccounts.RegisterRoutes(r, s.sysAccountsMgr)
			})

			// Network module — admin-only management routes.
			r.With(requireRole("admin")).Group(func(r chi.Router) {
				networkmodule.RegisterRoutes(r, s.networkMgr)
			})

			// Slurm module — admin-only management routes.
			r.With(requireRole("admin")).Group(func(r chi.Router) {
				slurmmodule.RegisterRoutes(r, s.slurmMgr)
			})
		})
	})

	return r
}

// serveSetPasswordPage serves set-password.html from the embedded static FS.
func serveSetPasswordPage(staticFS fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := staticFS.Open("set-password.html")
		if err != nil {
			// Fall back to login page if not yet present.
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			http.Error(w, "set-password page not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		serveHTMLFile(w, r, f, "set-password.html", stat.ModTime())
	}
}

// serveLoginPage serves login.html from the embedded static FS.
func serveLoginPage(staticFS fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := staticFS.Open("login.html")
		if err != nil {
			http.Error(w, "login page not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			http.Error(w, "login page not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		serveHTMLFile(w, r, f, "login.html", stat.ModTime())
	}
}

// buildAuthHandler constructs the AuthHandler with closures that call into
// the server's DB and session-signing functions. This avoids the handlers
// package importing the server package (which would be circular).
func (s *Server) buildAuthHandler() *handlers.AuthHandler {
	const cookieName = "clustr_session"

	// Legacy API-key login (deprecated — removed in v1.1).
	loginWithKeyFn := func(rawKey string) (keyPrefix string, scope string, ok bool) {
		hashInput := rawKey
		for _, pfx := range []string{"clustr-admin-", "clustr-node-"} {
			if strings.HasPrefix(rawKey, pfx) {
				hashInput = strings.TrimPrefix(rawKey, pfx)
				break
			}
		}
		h := sha256.Sum256([]byte(hashInput))
		hashHex := fmt.Sprintf("%x", h)
		lookupResult, err := s.db.LookupAPIKey(context.Background(), hashHex)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", "", false
			}
			log.Error().Err(err).Msg("auth handler: db lookup failed")
			return "", "", false
		}
		if lookupResult.Scope != api.KeyScopeAdmin {
			return "", "", false
		}
		kid := hashInput
		if len(kid) > 8 {
			kid = kid[:8]
		}
		return kid, string(lookupResult.Scope), true
	}

	// Primary username+password login (ADR-0007).
	loginWithPasswordFn := func(username, password string) (userID, role string, mustChange bool, err error) {
		user, err := s.db.GetUserByUsername(context.Background(), username)
		if err != nil {
			// ErrUserNotFound → "invalid" (generic, prevents user enumeration).
			// Any other error is a real DB failure — surface it so the handler
			// can return 500 rather than masking infrastructure failures as 401.
			if errors.Is(err, db.ErrUserNotFound) {
				return "", "", false, fmt.Errorf("invalid")
			}
			return "", "", false, fmt.Errorf("db: %w", err)
		}
		if user.IsDisabled() {
			return "", "", false, fmt.Errorf("disabled")
		}
		if berr := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); berr != nil {
			return "", "", false, fmt.Errorf("invalid")
		}
		// Update last_login_at asynchronously — never block the login response.
		go func() { _ = s.db.SetLastLogin(context.Background(), user.ID) }()
		return user.ID, string(user.Role), user.MustChangePassword, nil
	}

	signForUserFn := func(userID, role string) (string, time.Time, error) {
		p := newSessionPayload(userID, role)
		token, err := signSessionToken(s.sessionSecret, p)
		if err != nil {
			return "", time.Time{}, err
		}
		return token, time.Unix(p.EXP, 0), nil
	}

	signForKeyFn := func(keyPrefix string) (string, time.Time, error) {
		p := newSessionPayloadForKey(keyPrefix)
		token, err := signSessionToken(s.sessionSecret, p)
		if err != nil {
			return "", time.Time{}, err
		}
		return token, time.Unix(p.EXP, 0), nil
	}

	validateFn := func(token string) (sub, role string, exp time.Time, needsReissue bool, newToken string, ok bool) {
		result, err := validateSessionToken(s.sessionSecret, token)
		if err != nil {
			return "", "", time.Time{}, false, "", false
		}
		reissued := ""
		actuallyReissued := false
		if result.needsReissue {
			slid := slideSessionPayload(result.payload)
			if t, serr := signSessionToken(s.sessionSecret, slid); serr == nil {
				reissued = t
				result.payload = slid
				actuallyReissued = true
			}
			// If sign failed, skip re-issue silently — the existing valid token
			// continues to work. Do NOT return needsReissue=true with an empty
			// newToken, which would cause HandleMe to overwrite the cookie with "".
		}
		return result.payload.Sub, result.payload.Role, time.Unix(result.payload.EXP, 0), actuallyReissued, reissued, true
	}

	setPasswordFn := func(userID, currentPassword, newPassword string) (string, time.Time, error) {
		user, err := s.db.GetUser(context.Background(), userID)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("user_not_found")
		}
		if berr := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)); berr != nil {
			return "", time.Time{}, fmt.Errorf("wrong_password")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("hash: %w", err)
		}
		if err := s.db.SetUserPassword(context.Background(), userID, string(hash), true); err != nil {
			return "", time.Time{}, err
		}
		// Issue a fresh session token with the same role.
		p := newSessionPayload(user.ID, string(user.Role))
		token, err := signSessionToken(s.sessionSecret, p)
		if err != nil {
			return "", time.Time{}, err
		}
		return token, time.Unix(p.EXP, 0), nil
	}

	return &handlers.AuthHandler{
		LoginWithKey:      loginWithKeyFn,
		LoginWithPassword: loginWithPasswordFn,
		SignForUser:       signForUserFn,
		SignForKey:        signForKeyFn,
		Validate:          validateFn,
		SetPassword:       setPasswordFn,
		CookieName:        cookieName,
		Secure:            s.cfg.SessionSecure,
	}
}

// buildUsersHandler constructs the UsersHandler with a bcrypt helper closure.
func (s *Server) buildUsersHandler() *handlers.UsersHandler {
	return &handlers.UsersHandler{
		DB: s.db,
		HashPassword: func(plaintext string) (string, error) {
			h, err := bcrypt.GenerateFromPassword([]byte(plaintext), 12)
			if err != nil {
				return "", err
			}
			return string(h), nil
		},
	}
}

// buildAPIKeysHandler constructs the APIKeysHandler with closures that call into
// the server's DB and key-generation functions without causing circular imports.
func (s *Server) buildAPIKeysHandler() *handlers.APIKeysHandler {
	mintFn := func(r *http.Request, scope api.KeyScope, nodeID, label, createdBy string, expiresAt *time.Time) (string, string, string, error) {
		raw, err := generateRawKey()
		if err != nil {
			return "", "", "", err
		}
		keyHash := sha256Hex(raw)
		rec := db.APIKeyRecord{
			ID:        uuid.New().String(),
			Scope:     scope,
			NodeID:    nodeID,
			KeyHash:   keyHash,
			Label:     label,
			CreatedBy: createdBy,
			CreatedAt: time.Now(),
			ExpiresAt: expiresAt,
		}
		if err := s.db.CreateAPIKey(r.Context(), rec); err != nil {
			return "", "", "", fmt.Errorf("create api key: %w", err)
		}
		return raw, rec.ID, keyHash, nil
	}

	actorLabelFn := func(r *http.Request) string {
		return keyLabelFromContext(r.Context())
	}

	return &handlers.APIKeysHandler{
		DB:            s.db,
		MintKey:       mintFn,
		GetActorLabel: actorLabelFn,
	}
}

// serveIndex serves index.html from the embedded static FS.
func serveIndex(staticFS fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := staticFS.Open("index.html")
		if err != nil {
			http.Error(w, "UI not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			http.Error(w, "UI not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		serveHTMLFile(w, r, f, "index.html", stat.ModTime())
	}
}

// serveHTMLFile serves an fs.File via http.ServeContent, safely handling
// non-seekable files (e.g. os.DirFS in tests). embed.FS files satisfy
// io.ReadSeeker directly; for other FS implementations the file is read
// into a bytes.Reader so that http.ServeContent can seek for Range requests.
func serveHTMLFile(w http.ResponseWriter, r *http.Request, f fs.File, name string, modTime time.Time) {
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, modTime, rs)
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "failed to read "+name, http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, modTime, bytes.NewReader(data))
}

// Handler returns the underlying http.Handler for use in tests.
func (s *Server) Handler() http.Handler {
	return s.router
}

// ListenAndServe starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	go func() {
		<-ctx.Done()

		// Give in-flight builds up to 120 seconds to finish naturally before
		// we force-cancel them. HTTP shutdown gets its own 5-second window on
		// top of that. Total wall-clock budget: 125 seconds.
		//
		// The systemd unit sets TimeoutStopSec=300 which comfortably covers
		// this window. Previously the 25s budget caused QEMU builds to be
		// interrupted mid-install when the autodeploy timer restarted the
		// service; 120s gives most OS package-download phases time to complete.
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer drainCancel()

		log.Info().Msg("shutdown: waiting for in-flight builds to complete (up to 120s)")
		s.buildProgress.WaitForActive(drainCtx)

		// Any builds still active after the drain window are stuck — cancel them
		// so the DB record gets updated and the UI doesn't spin forever.
		s.buildProgress.CancelAllActive("server shutting down")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("graceful shutdown error")
		}
		if err := s.shells.CloseAll(); err != nil {
			log.Error().Err(err).Msg("shell session cleanup error")
		}
	}()

	log.Info().Str("addr", s.cfg.ListenAddr).Msg("server listening")
	if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}
