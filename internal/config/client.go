package config

import "os"

// ClientConfig holds all runtime configuration for the clustr CLI.
// Values are resolved in priority order: flag > environment > default.
type ClientConfig struct {
	// ServerURL is the base URL of the clustr-serverd instance.
	// Set via --server flag or CLUSTR_SERVER env var.
	// Default: http://localhost:8080
	ServerURL string

	// AuthToken is the Bearer token sent with every API request.
	// Set via --token flag or CLUSTR_TOKEN env var.
	// Leave empty when the server has auth disabled.
	AuthToken string

	// ClientdBinPath is the absolute path to the clustr-clientd binary that is
	// copied into the deployed rootfs during finalization. When empty, the deploy
	// agent auto-detects the binary by searching alongside os.Args[0],
	// /opt/clustr/bin/, and /usr/local/bin/. Set via CLUSTR_CLIENTD_BIN_PATH.
	ClientdBinPath string
}

// LoadClientConfig populates ClientConfig from environment variables with
// sensible defaults. Flag values override this — callers should apply flags
// after calling LoadClientConfig.
func LoadClientConfig() ClientConfig {
	return ClientConfig{
		ServerURL:      envOrDefault("CLUSTR_SERVER", "http://localhost:8080"),
		AuthToken:      os.Getenv("CLUSTR_TOKEN"),
		ClientdBinPath: os.Getenv("CLUSTR_CLIENTD_BIN_PATH"),
	}
}
