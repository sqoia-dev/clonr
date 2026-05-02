// bios.go — CLI subcommands for BIOS settings management (#159, Sprint 26).
//
// Commands:
//
//	clustr bios profiles list
//	clustr bios profiles create --name <n> --vendor intel --settings '{"k":"v"}'
//	clustr bios profiles show <id>
//	clustr bios profiles delete <id>
//	clustr bios assign <node-id> <profile-id>
//	clustr bios detach <node-id>
//	clustr bios apply -n NODE [--profile PROFILE_ID]   -- post-boot apply (no reimage)
//	clustr bios provider verify <vendor>
//
// The "apply" subcommand pushes the assigned BIOS profile to a running node via
// clustr-clientd.  Settings are written to NVRAM; a reboot is required for changes
// to take effect.  The node is NOT rebooted automatically.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"text/tabwriter"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/sqoia-dev/clustr/internal/bios"
	"github.com/sqoia-dev/clustr/internal/bios/intel"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── Top-level bios command ────────────────────────────��──────────────────────

func newBiosCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bios",
		Short: "BIOS settings management",
	}

	profilesCmd := &cobra.Command{
		Use:   "profiles",
		Short: "Manage BIOS profiles",
	}
	profilesCmd.AddCommand(
		newBiosProfilesListCmd(),
		newBiosProfilesCreateCmd(),
		newBiosProfilesShowCmd(),
		newBiosProfilesDeleteCmd(),
	)
	cmd.AddCommand(profilesCmd)
	cmd.AddCommand(newBiosAssignCmd())
	cmd.AddCommand(newBiosDetachCmd())
	cmd.AddCommand(newBiosApplyCmd())
	cmd.AddCommand(newBiosProviderCmd())
	return cmd
}

// ─── bios profiles list ──────────────────────��────────────────────────────────

func newBiosProfilesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all BIOS profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			var resp api.ListBiosProfilesResponse
			if err := c.GetJSON(ctx, "/api/v1/bios-profiles", &resp); err != nil {
				return fmt.Errorf("list bios profiles: %w", err)
			}

			if len(resp.Profiles) == 0 {
				fmt.Println("No BIOS profiles found.")
				return nil
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tVENDOR\tSETTINGS\tCREATED")
			for _, p := range resp.Profiles {
				var m map[string]string
				_ = json.Unmarshal([]byte(p.SettingsJSON), &m)
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d settings\t%s\n",
					p.ID, p.Name, p.Vendor, len(m),
					p.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		},
	}
}

// ─── bios profiles create ─────────────────────────────────────────────────────

func newBiosProfilesCreateCmd() *cobra.Command {
	var (
		flagName        string
		flagVendor      string
		flagSettings    string
		flagDescription string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new BIOS profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			req := api.CreateBiosProfileRequest{
				Name:        flagName,
				Vendor:      flagVendor,
				SettingsJSON: flagSettings,
				Description: flagDescription,
			}
			var resp api.BiosProfileResponse
			if err := c.PostJSON(ctx, "/api/v1/bios-profiles", req, &resp); err != nil {
				return fmt.Errorf("create bios profile: %w", err)
			}
			fmt.Printf("Created BIOS profile %s (%s)\n", resp.Profile.ID, resp.Profile.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&flagName, "name", "", "Profile name (required)")
	cmd.Flags().StringVar(&flagVendor, "vendor", "", "BIOS vendor: intel, dell, supermicro (required)")
	cmd.Flags().StringVar(&flagSettings, "settings", "{}", `JSON object of setting name → value pairs`)
	cmd.Flags().StringVar(&flagDescription, "description", "", "Optional description")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("vendor")
	return cmd
}

// ─── bios profiles show ────────────────────────────��──────────────────────────

func newBiosProfilesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <profile-id>",
		Short: "Show a BIOS profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			var resp api.BiosProfileResponse
			if err := c.GetJSON(ctx, "/api/v1/bios-profiles/"+args[0], &resp); err != nil {
				return fmt.Errorf("get bios profile: %w", err)
			}

			p := resp.Profile
			fmt.Printf("ID:          %s\n", p.ID)
			fmt.Printf("Name:        %s\n", p.Name)
			fmt.Printf("Vendor:      %s\n", p.Vendor)
			fmt.Printf("Description: %s\n", p.Description)
			fmt.Printf("Created:     %s\n", p.CreatedAt.Format(time.RFC3339))
			fmt.Printf("Updated:     %s\n", p.UpdatedAt.Format(time.RFC3339))

			var settings map[string]string
			if err := json.Unmarshal([]byte(p.SettingsJSON), &settings); err != nil {
				fmt.Printf("Settings: %s\n", p.SettingsJSON)
			} else {
				fmt.Printf("Settings (%d):\n", len(settings))
				for k, v := range settings {
					fmt.Printf("  %s = %s\n", k, v)
				}
			}
			return nil
		},
	}
}

// ─── bios profiles delete ────────────────────────────────���────────────────────

func newBiosProfilesDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <profile-id>",
		Short: "Delete a BIOS profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			if err := c.DeleteJSON(ctx, "/api/v1/bios-profiles/"+args[0]); err != nil {
				return fmt.Errorf("delete bios profile: %w", err)
			}
			fmt.Printf("Deleted BIOS profile %s\n", args[0])
			return nil
		},
	}
}

// ─── bios assign ───────────────────────────────��──────────────────────────────

func newBiosAssignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "assign <node-id> <profile-id>",
		Short: "Assign a BIOS profile to a node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			nodeID, profileID := args[0], args[1]
			req := api.AssignBiosProfileRequest{ProfileID: profileID}
			var resp api.NodeBiosProfileResponse
			if err := c.PutJSON(ctx, "/api/v1/nodes/"+nodeID+"/bios-profile", req, &resp); err != nil {
				return fmt.Errorf("assign bios profile: %w", err)
			}
			fmt.Printf("Assigned profile %s to node %s\n", profileID, nodeID)
			return nil
		},
	}
}

// ─── bios detach ─────────────────────────────────��────────────────────────────

func newBiosDetachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detach <node-id>",
		Short: "Remove BIOS profile assignment from a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			if err := c.DeleteJSON(ctx, "/api/v1/nodes/"+args[0]+"/bios-profile"); err != nil {
				return fmt.Errorf("detach bios profile: %w", err)
			}
			fmt.Printf("Detached BIOS profile from node %s\n", args[0])
			return nil
		},
	}
}

// ─── bios apply ───────────────────────────────────────────────────────────────

func newBiosApplyCmd() *cobra.Command {
	var flagProfile string

	cmd := &cobra.Command{
		Use:   "apply -n NODE [--profile PROFILE_ID]",
		Short: "Apply a BIOS profile to a running node (post-boot, no reimage)",
		Long: `Pushes the assigned BIOS profile to a running node via clustr-clientd.

Settings are written to BIOS NVRAM immediately.  They take effect after the next
operator-initiated reboot — the node is NOT rebooted automatically.

If --profile is omitted, the node's currently assigned profile is used.
To assign a profile first, run: clustr bios assign <node-id> <profile-id>

The node must be online (clustr-clientd connected) for this command to work.
To apply BIOS settings as part of a full reimage cycle, use:
  clustr reimage --bios-only <node-id>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			nodeID, _ := cmd.Flags().GetString("node")
			if nodeID == "" {
				return fmt.Errorf("--node / -n is required")
			}

			// If a profile override is provided, assign it first.
			if flagProfile != "" {
				req := api.AssignBiosProfileRequest{ProfileID: flagProfile}
				var resp api.NodeBiosProfileResponse
				if err := c.PutJSON(ctx, "/api/v1/nodes/"+nodeID+"/bios-profile", req, &resp); err != nil {
					return fmt.Errorf("bios apply: assign profile: %w", err)
				}
			}

			var resp api.BiosApplyResponse
			if err := c.PostJSON(ctx, "/api/v1/nodes/"+nodeID+"/bios/apply", nil, &resp); err != nil {
				return fmt.Errorf("bios apply: %w", err)
			}

			if resp.Applied == 0 {
				fmt.Printf("Node %s: %s\n", nodeID, resp.Message)
			} else {
				fmt.Printf("Node %s: %d setting(s) applied.\n", nodeID, resp.Applied)
				fmt.Printf("  %s\n", resp.Message)
			}
			return nil
		},
	}
	cmd.Flags().StringP("node", "n", "", "Node ID to apply BIOS settings to (required)")
	cmd.Flags().StringVar(&flagProfile, "profile", "", "Profile ID to assign before applying (optional; uses node's current profile if omitted)")
	_ = cmd.MarkFlagRequired("node")
	return cmd
}

// ─── bios provider ───────────────────────────────────��───────────────────────��

func newBiosProviderCmd() *cobra.Command {
	providerCmd := &cobra.Command{
		Use:   "provider",
		Short: "BIOS provider operations",
	}
	providerCmd.AddCommand(newBiosProviderVerifyCmd())
	return providerCmd
}

func newBiosProviderVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <vendor>",
		Short: "Verify a BIOS provider binary is available on the server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			vendor := args[0]
			var resp api.BiosProviderVerifyResponse
			if err := c.GetJSON(ctx, "/api/v1/bios/providers/"+vendor+"/verify", &resp); err != nil {
				return fmt.Errorf("verify provider: %w", err)
			}

			if resp.Available {
				fmt.Printf("Provider %q: AVAILABLE\n", vendor)
				if resp.BinPath != "" {
					fmt.Printf("Binary path: %s\n", resp.BinPath)
				}
			} else {
				fmt.Printf("Provider %q: NOT AVAILABLE\n", vendor)
				if resp.Message != "" {
					fmt.Printf("Detail: %s\n", resp.Message)
				}
				fmt.Printf("\nTo enable: place the vendor binary at %s on the server.\n", resp.BinPath)
				return fmt.Errorf("provider %q not available", vendor)
			}
			return nil
		},
	}
}

// ─── Deploy-time helpers (called from runAutoDeployMode) ─────────────────────

// initramfsBiosVendorPaths maps vendor names to where build-initramfs.sh
// installs the operator-supplied binary inside the initramfs.
var initramfsBiosVendorPaths = map[string]string{
	"intel":      "/usr/local/bin/intel-syscfg",
	"dell":       "/usr/local/bin/dell-racadm",
	"supermicro": "/usr/local/bin/supermicro-sum",
}

// applyBiosProfileInInitramfs applies the BIOS profile assigned to this node.
// It is called from runAutoDeployMode immediately after server registration and
// before image fetch (design decision D1: apply in initramfs, not post-boot).
//
// The vendor binary is exec'd directly — no privhelper needed inside initramfs
// since we are already root.
//
// Returns nil when no changes are needed or changes were applied successfully.
// Returns an error when the binary is missing or apply fails.
func applyBiosProfileInInitramfs(ctx context.Context, profile *api.BiosProfile, log zerolog.Logger) error {
	if profile == nil {
		return nil
	}

	log.Info().
		Str("profile_id", profile.ID).
		Str("vendor", profile.Vendor).
		Str("profile_name", profile.Name).
		Msg("bios: applying profile in initramfs")

	// Resolve the vendor binary path inside initramfs.
	binPath, ok := initramfsBiosVendorPaths[profile.Vendor]
	if !ok {
		return fmt.Errorf("bios: unsupported vendor %q", profile.Vendor)
	}

	// Check if binary is present. If absent, log a warning and skip.
	// This is intentional: operators who haven't installed the binary don't
	// get a failed deploy — just a warning. See docs/BIOS-INTEL-SETUP.md.
	if _, err := os.Stat(binPath); err != nil {
		log.Warn().
			Str("bin_path", binPath).
			Str("vendor", profile.Vendor).
			Msg("bios: vendor binary not present in initramfs — skipping BIOS apply (install binary to enable)")
		return bios.ErrBinaryMissing
	}

	// Look up the provider. Use the custom binary path so the provider exec's
	// the initramfs copy, not the operator server path.
	provider, err := biosProviderWithPath(profile.Vendor, binPath)
	if err != nil {
		return fmt.Errorf("bios: lookup provider %q: %w", profile.Vendor, err)
	}

	// Parse desired settings from the profile JSON.
	var settingsMap map[string]string
	if err := json.Unmarshal([]byte(profile.SettingsJSON), &settingsMap); err != nil {
		return fmt.Errorf("bios: parse profile settings_json: %w", err)
	}
	desired := make([]bios.Setting, 0, len(settingsMap))
	for name, value := range settingsMap {
		desired = append(desired, bios.Setting{Name: name, Value: value})
	}

	// Read current settings.
	printPhase(phaseInProgress, "Reading current BIOS settings")
	current, err := provider.ReadCurrent(ctx)
	if err != nil {
		if errors.Is(err, bios.ErrBinaryMissing) {
			log.Warn().Msg("bios: vendor binary absent — skipping BIOS apply")
			return err
		}
		return fmt.Errorf("bios: ReadCurrent: %w", err)
	}

	// Diff desired vs. current.
	changes, err := provider.Diff(desired, current)
	if err != nil {
		return fmt.Errorf("bios: Diff: %w", err)
	}

	if len(changes) == 0 {
		printPhase(phaseDone, "BIOS settings already match profile — no changes needed")
		log.Info().Str("profile_id", profile.ID).Msg("bios: no drift, profile already applied")
		return nil
	}

	log.Info().
		Int("change_count", len(changes)).
		Str("profile_id", profile.ID).
		Msg("bios: applying settings changes")
	printPhase(phaseInProgress, fmt.Sprintf("Applying %d BIOS setting(s)", len(changes)))

	// Apply the diff.
	applied, err := provider.Apply(ctx, changes)
	if err != nil {
		printPhase(phaseFailed, "BIOS apply failed")
		return fmt.Errorf("bios: Apply: %w", err)
	}

	// Compute and log the settings hash for the record.
	hash := biosSettingsHash(profile.SettingsJSON)
	printPhase(phaseDone, fmt.Sprintf("BIOS settings applied (%d changed, hash %s…)", len(applied), hash[:8]))
	log.Info().
		Int("applied_count", len(applied)).
		Str("profile_id", profile.ID).
		Str("settings_hash", hash).
		Msg("bios: apply complete")

	return nil
}

// biosProviderWithPath returns a provider with the given binary path.
// For Intel, uses intel.NewWithBinaryPath so the initramfs copy of the vendor
// binary is exec'd instead of the server-side path.
// Other vendors fall back to the registry (their binary paths are fixed).
func biosProviderWithPath(vendor, binPath string) (bios.Provider, error) {
	switch vendor {
	case "intel":
		return intel.NewWithBinaryPath(binPath), nil
	default:
		return bios.Lookup(vendor)
	}
}

// biosSettingsHash returns the hex SHA-256 of the settings JSON.
// Used for drift detection correlation.
func biosSettingsHash(settingsJSON string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(settingsJSON)))
}

// rebootSystem triggers an immediate system reboot.
// Called after a bios_only apply cycle completes.
func rebootSystem(log zerolog.Logger) {
	log.Info().Msg("rebooting system after bios_only apply")
	// Try reboot(8) from busybox (standard initramfs path).
	cmd := exec.Command("reboot", "-f")
	if err := cmd.Run(); err != nil {
		// Fallback: busybox may be at /bin/busybox.
		log.Warn().Err(err).Msg("reboot -f failed, trying /bin/reboot")
		_ = exec.Command("/bin/reboot", "-f").Run()
	}
	// If reboot returns (e.g. in a test environment), just exit.
	log.Error().Msg("reboot returned unexpectedly — exiting")
	os.Exit(0)
}

