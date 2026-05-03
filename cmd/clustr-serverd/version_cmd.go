package main

// version_cmd.go implements "clustr-serverd version".
//
// Prints build metadata: version tag, commit SHA, build date,
// embedded Slurm bundle version, and DB schema version (if DB is reachable).

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
)

func init() {
	var flagShort bool
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print clustr-serverd version and build information",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVersion(flagShort)
		},
	}
	versionCmd.Flags().BoolVar(&flagShort, "short", false, "Print only the version string")
	rootCmd.AddCommand(versionCmd)
}

func runVersion(short bool) error {
	if short {
		fmt.Println(version)
		return nil
	}

	fmt.Printf("clustr-serverd %s\n", version)
	fmt.Printf("  commit:         %s\n", commitSHA)
	fmt.Printf("  built:          %s\n", buildTime)
	fmt.Printf("  slurm bundle:   %s\n", builtinSlurmBundleVersion)
	fmt.Printf("  slurm version:  %s\n", builtinSlurmVersion)

	// Try to read the DB schema version — non-fatal if DB is not reachable.
	cfg := config.LoadServerConfig()
	if _, err := os.Stat(cfg.DBPath); err == nil {
		database, err := db.Open(cfg.DBPath)
		if err == nil {
			defer database.Close()
			if ver, err := database.SchemaVersion(context.Background()); err == nil {
				fmt.Printf("  schema version: %d\n", ver)
			}
		}
	}

	return nil
}
