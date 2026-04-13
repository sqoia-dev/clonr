package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/pkg/config"
	"github.com/sqoia-dev/clonr/pkg/db"
	"github.com/sqoia-dev/clonr/pkg/pxe"
	"github.com/sqoia-dev/clonr/pkg/server"
)

var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "clonr-serverd",
	Short: "clonr provisioning server",
	RunE:  runServer,
}

var flagPXE bool

func init() {
	rootCmd.Flags().BoolVar(&flagPXE, "pxe", false,
		"Enable built-in DHCP/TFTP PXE server (also set via CLONR_PXE_ENABLED=true)")

	// apikey subcommand — for operator key rotation.
	apikeyCmd := &cobra.Command{
		Use:   "apikey",
		Short: "Manage clonr-serverd API keys",
	}

	var flagScope string
	var flagDesc string
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Generate a new API key for the given scope",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApikeyCreate(flagScope, flagDesc)
		},
	}
	createCmd.Flags().StringVar(&flagScope, "scope", "", "Key scope: admin or node (required)")
	createCmd.Flags().StringVar(&flagDesc, "description", "", "Human-readable description for this key")
	_ = createCmd.MarkFlagRequired("scope")

	apikeyCmd.AddCommand(createCmd)
	rootCmd.AddCommand(apikeyCmd)
}

func main() {
	// Bootstrap a console logger early so startup failures are readable.
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr}).
		With().
		Str("service", "clonr-serverd").
		Logger()

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runApikeyCreate(scopeStr, description string) error {
	scope := api.KeyScope(scopeStr)
	if scope != api.KeyScopeAdmin && scope != api.KeyScopeNode {
		return fmt.Errorf("invalid scope %q: must be 'admin' or 'node'", scopeStr)
	}

	cfg := config.LoadServerConfig()
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	ctx := context.Background()
	rawKey, id, err := server.CreateAPIKey(ctx, database, scope, description)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}

	fmt.Printf("\nNew %s API key created (ID: %s)\n", scopeStr, id)
	fmt.Printf("Raw key (save this — it will NOT be shown again):\n\n  clonr-%s-%s\n\n", scopeStr, rawKey)
	return nil
}

func runServer(cmd *cobra.Command, args []string) error {
	cfg := config.LoadServerConfig()

	// --pxe flag overrides env var.
	if flagPXE {
		cfg.PXE.Enabled = true
	}

	// Set log level from config.
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	log.Info().Str("version", version).Str("addr", cfg.ListenAddr).Msg("clonr-serverd starting")

	// Ensure image directory exists.
	if err := os.MkdirAll(cfg.ImageDir, 0o755); err != nil {
		return fmt.Errorf("failed to create image dir %s: %w", cfg.ImageDir, err)
	}

	// Open database (applies migrations on first run).
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database %s: %w", cfg.DBPath, err)
	}
	defer database.Close()

	log.Info().Str("db", cfg.DBPath).Msg("database ready")

	// Graceful shutdown on SIGINT / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Inject the HTTP port into PXEConfig so the DHCP server can build correct
	// iPXE chainload URLs without hardcoding port 8080.
	if _, port, err := net.SplitHostPort(cfg.ListenAddr); err == nil {
		cfg.PXE.HTTPPort = port
	} else {
		cfg.PXE.HTTPPort = "8080" // safe fallback
	}

	// Start PXE server if enabled.
	if cfg.PXE.Enabled {
		pxeSrv, err := pxe.New(cfg.PXE)
		if err != nil {
			return fmt.Errorf("failed to init PXE server: %w", err)
		}
		go func() {
			if err := pxeSrv.Start(ctx); err != nil {
				log.Error().Err(err).Msg("PXE server error")
			}
		}()
		log.Info().
			Str("interface", cfg.PXE.Interface).
			Str("range", cfg.PXE.IPRange).
			Msg("PXE server enabled")
	}

	// Bootstrap initial admin API key if none exists.
	// Prints the raw key to stdout ONCE — operator must capture it.
	if !cfg.AuthDevMode {
		if err := server.BootstrapAdminKey(ctx, database); err != nil {
			return fmt.Errorf("failed to bootstrap admin key: %w", err)
		}
	}

	// Wire up and start the HTTP server.
	srv := server.New(cfg, database)

	// Reconcile any images stuck in "building" state from before the restart.
	// These have no live goroutine behind them and will never progress on their own.
	if err := srv.ReconcileStuckBuilds(ctx); err != nil {
		log.Error().Err(err).Msg("reconcile stuck builds failed (non-fatal)")
	}

	// Start background workers (reimage scheduler, etc.) before accepting traffic.
	srv.StartBackgroundWorkers(ctx)

	if err := srv.ListenAndServe(ctx); err != nil {
		return fmt.Errorf("server error: %w", err)
	}

	log.Info().Msg("shutdown complete")
	return nil
}
