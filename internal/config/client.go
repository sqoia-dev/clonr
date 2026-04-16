package config

import "os"

// ClientConfig holds all runtime configuration for the clonr CLI.
// Values are resolved in priority order: flag > environment > default.
type ClientConfig struct {
	// ServerURL is the base URL of the clonr-serverd instance.
	// Set via --server flag or CLONR_SERVER env var.
	// Default: http://localhost:8080
	ServerURL string

	// AuthToken is the Bearer token sent with every API request.
	// Set via --token flag or CLONR_TOKEN env var.
	// Leave empty when the server has auth disabled.
	AuthToken string
}

// LoadClientConfig populates ClientConfig from environment variables with
// sensible defaults. Flag values override this — callers should apply flags
// after calling LoadClientConfig.
func LoadClientConfig() ClientConfig {
	return ClientConfig{
		ServerURL: envOrDefault("CLONR_SERVER", "http://localhost:8080"),
		AuthToken: os.Getenv("CLONR_TOKEN"),
	}
}
