// Command clonr-clientd is a persistent lightweight daemon installed on deployed
// nodes. It connects to clonr-serverd via WebSocket and maintains a heartbeat,
// enabling server-push of config updates and remote diagnostics in later sprints.
package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clonr/internal/clientd"
)

// Build-time variables injected via -ldflags.
var (
	version   = "dev"
	commitSHA = "unknown"
	buildTime = "unknown"
)

func main() {
	// zerolog — pretty output to stdout; systemd-journald captures it.
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05"}).
		With().Timestamp().Str("service", "clonr-clientd").Logger()

	log.Info().
		Str("version", version).
		Str("commit", commitSHA).
		Str("build_time", buildTime).
		Msg("clonr-clientd starting")

	// Read config files.
	tokenPath := "/etc/clonr/node-token"
	clonrdURLPath := "/etc/clonr/clonrd-url"

	serverURL, err := readFileTrimmed(clonrdURLPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", clonrdURLPath).
			Msg("clonr-clientd: cannot read clonrd-url — was this node finalized with clonr-clientd support?")
	}

	log.Info().Str("server_url", serverURL).Str("token_path", tokenPath).
		Msg("clonr-clientd: configuration loaded")

	// Build the client.
	c, err := clientd.New(serverURL, tokenPath, version)
	if err != nil {
		log.Fatal().Err(err).Msg("clonr-clientd: failed to initialize client")
	}

	// Catch SIGTERM and SIGINT for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sig
		log.Info().Str("signal", s.String()).Msg("clonr-clientd: received signal, shutting down gracefully")
		cancel()
	}()

	if err := c.Run(ctx); err != nil && err != context.Canceled {
		log.Error().Err(err).Msg("clonr-clientd: exited with error")
		os.Exit(1)
	}

	log.Info().Msg("clonr-clientd: shutdown complete")
}

// readFileTrimmed reads a file and trims surrounding whitespace.
func readFileTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
