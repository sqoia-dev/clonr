package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/sqoia-dev/clustr/internal/chroot"
	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/deploy"
	"github.com/sqoia-dev/clustr/internal/hardware"
	"github.com/sqoia-dev/clustr/internal/image/layout"
	"github.com/sqoia-dev/clustr/internal/ipmi"
	"github.com/sqoia-dev/clustr/internal/power"
	poweripm "github.com/sqoia-dev/clustr/internal/power/ipmi"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/pkg/client"
)

// ANSI colour codes used by the log viewer.
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
)

var version = "dev"

// Persistent flag values applied to every subcommand.
var (
	flagServer string
	flagToken  string
	// flagMulticastMode is the package-level value of --multicast, set by
	// newDeployCmd and read by runAutoDeployMode so the initramfs auto-deploy
	// path can participate in multicast sessions (#157).
	flagMulticastMode string
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	// SilenceErrors prevents cobra from printing the error string again since
	// RunE handlers print their own messages.
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true

	if err := rootCmd.Execute(); err != nil {
		// execExitError carries the max per-node exit code so the caller can
		// distinguish between "all nodes failed with exit 2" and "unreachable".
		var exitErr *execExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:     "clustr",
	Short:   "Node cloning and image management for HPC clusters",
	Version: version,
}

func init() {
	// Persistent flags available on all subcommands.
	rootCmd.PersistentFlags().StringVar(&flagServer, "server", "", "clustr-serverd URL (env: CLUSTR_SERVER)")
	rootCmd.PersistentFlags().StringVar(&flagToken, "token", "", "API auth token (env: CLUSTR_TOKEN)")

	// image subcommand group.
	imageCmd := &cobra.Command{
		Use:   "image",
		Short: "Manage base images",
	}
	imageCmd.AddCommand(
		newImageListCmd(),
		newImageDetailsCmd(),
		newImagePullCmd(),
		newImageImportISOCmd(),
		newImageCaptureCmd(),
	)
	rootCmd.AddCommand(imageCmd)

	// node subcommand group.
	nodeCmd := &cobra.Command{
		Use:   "node",
		Short: "Manage node configurations",
	}
	nodeCmd.AddCommand(
		newNodeListCmd(),
		newNodeConfigCmd(),
	)
	rootCmd.AddCommand(nodeCmd)

	// admin subcommand group.
	adminCmd := &cobra.Command{
		Use:   "admin",
		Short: "Server administration commands",
	}
	keysCmd := &cobra.Command{
		Use:   "keys",
		Short: "Manage API keys",
	}
	keysCmd.AddCommand(
		newAdminKeysListCmd(),
		newAdminKeysCreateCmd(),
		newAdminKeysRotateCmd(),
		newAdminKeysRevokeCmd(),
	)
	adminCmd.AddCommand(keysCmd)
	rootCmd.AddCommand(adminCmd)

	// ipmi subcommand group.
	ipmiCmd := &cobra.Command{
		Use:   "ipmi",
		Short: "IPMI / BMC management",
	}
	ipmiCmd.AddCommand(
		newIPMIStatusCmd(),
		newIPMIPowerCmd(),
		newIPMIConfigureCmd(),
		newIPMIPXECmd(),
		newIPMISensorsCmd(),
		newIPMITestBootFlipDirectCmd(),
		newIPMISELCmd(), // #129
	)
	rootCmd.AddCommand(ipmiCmd)

	// Top-level commands.
	rootCmd.AddCommand(hardwareCmd)
	rootCmd.AddCommand(identifyCmd)
	rootCmd.AddCommand(newDeployCmd())
	rootCmd.AddCommand(newFixEFIBootCmd())
	rootCmd.AddCommand(newLogsCmd())
	rootCmd.AddCommand(newShellCmd())
	rootCmd.AddCommand(newHealthCmd())  // #130
	rootCmd.AddCommand(newExecCmd())    // #126
	rootCmd.AddCommand(newCpCmd())      // #127
	rootCmd.AddCommand(newConsoleCmd()) // #128
	rootCmd.AddCommand(newAlertsCmd())  // #134
	rootCmd.AddCommand(newStatsCmd())   // #134
}

// clientFromFlags builds an API client resolving server/token from flags then env.
func clientFromFlags() *client.Client {
	cfg := config.LoadClientConfig()
	if flagServer != "" {
		cfg.ServerURL = flagServer
	}
	if flagToken != "" {
		cfg.AuthToken = flagToken
	}
	return client.New(cfg.ServerURL, cfg.AuthToken)
}

// ─── image list ──────────────────────────────────────────────────────────────

func newImageListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all base images on the server",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			images, err := c.ListImages(ctx)
			if err != nil {
				return fmt.Errorf("list images: %w", err)
			}

			if len(images) == 0 {
				fmt.Println("No images found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tVERSION\tOS\tARCH\tFORMAT\tSTATUS\tSIZE\tCREATED")
			for _, img := range images {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					shortID(img.ID),
					img.Name,
					img.Version,
					img.OS,
					img.Arch,
					img.Format,
					img.Status,
					humanBytes(img.SizeBytes),
					img.CreatedAt.Format("2006-01-02"),
				)
			}
			return w.Flush()
		},
	}
}

// ─── image details ───────────────────────────────────────────────────────────

func newImageDetailsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "details <id>",
		Short: "Show detailed metadata for an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			img, err := c.GetImage(ctx, args[0])
			if err != nil {
				return fmt.Errorf("get image: %w", err)
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(img)
		},
	}
}

// ─── image pull ──────────────────────────────────────────────────────────────

func newImagePullCmd() *cobra.Command {
	var (
		flagURL     string
		flagName    string
		flagVersion string
		flagOS      string
		flagArch    string
		flagFormat  string
		flagNotes   string
	)

	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Pull an image from a URL into the server's image store",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagURL == "" {
				return fmt.Errorf("--url is required")
			}
			if flagName == "" {
				return fmt.Errorf("--name is required")
			}

			ctx := context.Background()
			c := clientFromFlags()

			req := api.PullRequest{
				URL:     flagURL,
				Name:    flagName,
				Version: flagVersion,
				OS:      flagOS,
				Arch:    flagArch,
				Format:  api.ImageFormat(flagFormat),
				Notes:   flagNotes,
			}

			fmt.Fprintf(os.Stderr, "Requesting pull of %s from %s...\n", flagName, flagURL)
			img, err := c.PullImage(ctx, req)
			if err != nil {
				return fmt.Errorf("pull image: %w", err)
			}

			fmt.Printf("Image pull initiated:\n")
			fmt.Printf("  ID:     %s\n", img.ID)
			fmt.Printf("  Name:   %s\n", img.Name)
			fmt.Printf("  Status: %s\n", img.Status)
			fmt.Printf("\nPoll status with: clustr image details %s\n", img.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&flagURL, "url", "", "Source URL for the image blob (required)")
	cmd.Flags().StringVar(&flagName, "name", "", "Image name (required)")
	cmd.Flags().StringVar(&flagVersion, "version", "1.0.0", "Image version")
	cmd.Flags().StringVar(&flagOS, "os", "", "OS name, e.g. 'Rocky Linux 9'")
	cmd.Flags().StringVar(&flagArch, "arch", "x86_64", "Target architecture")
	cmd.Flags().StringVar(&flagFormat, "format", "filesystem", "Image format: filesystem or block")
	cmd.Flags().StringVar(&flagNotes, "notes", "", "Free-text notes")

	return cmd
}

// ─── node list ───────────────────────────────────────────────────────────────

func newNodeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all node configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			nodes, err := c.ListNodes(ctx)
			if err != nil {
				return fmt.Errorf("list nodes: %w", err)
			}

			if len(nodes) == 0 {
				fmt.Println("No node configurations found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tHOSTNAME\tFQDN\tMAC\tIMAGE\tGROUPS")
			for _, node := range nodes {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					shortID(node.ID),
					node.Hostname,
					node.FQDN,
					node.PrimaryMAC,
					shortID(node.BaseImageID),
					strings.Join(node.Groups, ","),
				)
			}
			return w.Flush()
		},
	}
}

// ─── node config ─────────────────────────────────────────────────────────────

func newNodeConfigCmd() *cobra.Command {
	var flagMAC string

	cmd := &cobra.Command{
		Use:   "config [id]",
		Short: "Show node configuration by ID or MAC address",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			var (
				cfg *api.NodeConfig
				err error
			)

			switch {
			case len(args) == 1:
				cfg, err = c.GetNode(ctx, args[0])
			case flagMAC != "":
				cfg, err = c.GetNodeConfigByMAC(ctx, flagMAC)
			default:
				return fmt.Errorf("provide an ID as argument or --mac <address>")
			}

			if err != nil {
				return fmt.Errorf("get node config: %w", err)
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(cfg)
		},
	}

	cmd.Flags().StringVar(&flagMAC, "mac", "", "Lookup node by primary MAC address")
	return cmd
}

// ─── hardware ────────────────────────────────────────────────────────────────

var hardwareCmd = &cobra.Command{
	Use:   "hardware",
	Short: "Discover and print this node's hardware profile as JSON",
	Long: `hardware runs full hardware discovery (CPU, memory, disks, NICs, DMI)
and prints the result as formatted JSON to stdout. No server connection required.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := hardware.Discover()
		if err != nil {
			return fmt.Errorf("hardware discovery: %w", err)
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	},
}

// ─── identify ────────────────────────────────────────────────────────────────

// identifyCmd runs hardware discovery and prints the result as JSON.
// Kept for backward compatibility — functionally identical to hardware.
var identifyCmd = &cobra.Command{
	Use:   "identify",
	Short: "Discover and print this node's hardware profile as JSON (alias for hardware)",
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := hardware.Discover()
		if err != nil {
			return fmt.Errorf("hardware discovery: %w", err)
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	},
}

// ─── deploy ──────────────────────────────────────────────────────────────────

func newDeployCmd() *cobra.Command {
	var (
		flagImage      string
		flagDisk       string
		flagMountRoot  string
		flagFixEFI     bool
		flagAuto       bool
		flagNoRollback bool
		flagSkipVerify bool
		flagTimeout    string
	)

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy an image to this node",
		Long: `deploy performs a full deployment:
  1. Discover local hardware
  2. Fetch node config from server (matched by MAC address)
  3. Fetch image details from server
  4. Preflight: validate disk size and architecture
  5. Deploy: download and write the image
  6. Finalize: apply hostname, network, SSH keys
     UEFI: grub2-install --removable writes \EFI\BOOT\BOOTX64.EFI; no NVRAM
     entry is created — firmware uses removable-media auto-discovery (§3.5.1.1).
     See docs/boot-architecture.md §8.

With --auto: discovers hardware, registers with the server, and waits for an
admin to assign a base image before proceeding with deployment. Intended for
PXE-booted nodes running from initramfs.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateMulticastFlag(flagMulticastMode); err != nil {
				return err
			}
			// --auto mode: register with server, wait for image assignment, then deploy.
			if flagAuto {
				return runAutoDeployMode()
			}

			if flagImage == "" {
				return fmt.Errorf("--image is required")
			}

			// Resolve deployment timeout (env var overrides flag default).
			timeoutStr := flagTimeout
			if envTimeout := os.Getenv("CLUSTR_DEPLOY_TIMEOUT"); envTimeout != "" {
				timeoutStr = envTimeout
			}
			deployTimeout, err := time.ParseDuration(timeoutStr)
			if err != nil {
				return fmt.Errorf("invalid deployment timeout %q: %w", timeoutStr, err)
			}

			baseCtx := context.Background()
			ctx, cancelTimeout := context.WithTimeout(baseCtx, deployTimeout)
			defer cancelTimeout()

			c := clientFromFlags()

			// ── Remote logging setup ─────────────────────────────────────────
			// Discover a best-effort MAC for the log writer before hardware
			// discovery runs fully. We'll update nodeMAC after hardware is done.
			remoteWriter := client.NewRemoteLogWriter(c, "unknown", "", client.WithComponent("deploy"))
			defer remoteWriter.Close()

			// ── Structured progress reporter ─────────────────────────────────
			// Created early with a placeholder MAC; updated after hardware discovery.
			// Best-effort: failures to POST progress don't abort the deployment.
			progressReporter := client.NewProgressReporter(c, "unknown", "")
			defer func() { progressReporter.Complete() }()

			// Tee all zerolog output: local console + remote server.
			multi := zerolog.MultiLevelWriter(zerolog.ConsoleWriter{Out: os.Stderr}, remoteWriter)
			deployLog := zerolog.New(multi).With().Timestamp().Logger()
			// Wire the deploy package so subprocess output goes through the same logger.
			deploy.SetLogger(deployLog)
			// ─────────────────────────────────────────────────────────────────

			// Step 1: Discover hardware.
			progressReporter.SetMessage("Discovering hardware")
			printPhase(phaseInProgress, "Discovering hardware")
			deployLog.Info().Str("component", "hardware").Msg("starting hardware discovery")
			hw, err := hardware.Discover()
			if err != nil {
				printPhase(phaseFailed, "Hardware discovery")
				printDeployError("hardware_discovery", err.Error())
				return fmt.Errorf("hardware discovery: %w", err)
			}
			printPhase(phaseDone, "Hardware discovered")

			// Step 2: Get node config by primary MAC.
			progressReporter.SetMessage("Fetching node configuration from server")
			printPhase(phaseInProgress, "Fetching node config from server")
			primaryMAC := primaryMACFromHW(hw)
			if primaryMAC == "" {
				printDeployError("hardware_discovery", "no usable NIC found — cannot look up node config")
				return fmt.Errorf("no usable NIC found — cannot look up node config")
			}

			// Now that we have the MAC, update the remote writer identity.
			remoteWriter.SetNodeMAC(primaryMAC)
			progressReporter.SetNode(primaryMAC, "")
			deployLog.Info().Str("component", "deploy").Str("mac", primaryMAC).Msg("fetching node config")

			nodeCfg, err := c.GetNodeConfigByMAC(ctx, primaryMAC)
			if err != nil {
				printPhase(phaseFailed, "Node config fetch")
				printDeployError("node_config", err.Error())
				deployLog.Error().Str("component", "deploy").Err(err).Msg("failed to fetch node config")
				return fmt.Errorf("get node config (MAC %s): %w", primaryMAC, err)
			}
			remoteWriter.SetHostname(nodeCfg.Hostname)
			progressReporter.SetNode(primaryMAC, nodeCfg.Hostname)
			printPhase(phaseDone, fmt.Sprintf("Node config loaded  (%s)", nodeCfg.Hostname))
			deployLog.Info().Str("component", "deploy").Str("hostname", nodeCfg.Hostname).Msg("node config loaded")

			// Step 3: Get image details.
			progressReporter.SetMessage("Fetching image details")
			printPhase(phaseInProgress, "Fetching image details")
			deployLog.Info().Str("component", "deploy").Str("image_id", flagImage).Msg("fetching image details")
			img, err := c.GetImage(ctx, flagImage)
			if err != nil {
				printPhase(phaseFailed, "Image details fetch")
				printDeployError("image_fetch", err.Error())
				deployLog.Error().Str("component", "deploy").Err(err).Msg("failed to fetch image")
				return fmt.Errorf("get image %s: %w", flagImage, err)
			}
			if img.Status != api.ImageStatusReady {
				printPhase(phaseFailed, "Image details fetch")
				printDeployError("image_fetch", fmt.Sprintf("image %s is not ready (status: %s)", img.ID, img.Status))
				return fmt.Errorf("image %s is not ready (status: %s)", img.ID, img.Status)
			}
			printPhase(phaseDone, fmt.Sprintf("Image details fetched  (%s %s)", img.Name, img.Version))
			deployLog.Info().Str("component", "deploy").
				Str("image", img.Name).Str("version", img.Version).Str("format", string(img.Format)).
				Msg("image ready")

			// Resolve server URL for blob download.
			cfg := config.LoadClientConfig()
			if flagServer != "" {
				cfg.ServerURL = flagServer
			}
			blobURL := cfg.ServerURL + "/api/v1/images/" + img.ID + "/blob"

			// Print the deploy header now that we have node + image identity.
			printDeployHeader(nodeCfg.Hostname, fmt.Sprintf("%s %s", img.Name, img.Version), cfg.ServerURL)

			// Resolve mount root.
			mountRoot := flagMountRoot
			if mountRoot == "" {
				tmp, err := os.MkdirTemp("", "clustr-deploy-*")
				if err != nil {
					return fmt.Errorf("create temp mount root: %w", err)
				}
				defer os.RemoveAll(tmp)
				mountRoot = tmp
			}

			// Step 4: Preflight.
			progressReporter.SetMessage("Running preflight checks")
			printPhase(phaseInProgress, "Running preflight checks")
			deployLog.Info().Str("component", "deploy").Msg("running preflight checks")
			progressReporter.StartPhase("preflight", 0)
			var deployer deploy.Deployer
			switch img.Format {
			case api.ImageFormatBlock:
				deployer = &deploy.BlockDeployer{}
			default:
				deployer = &deploy.FilesystemDeployer{}
			}

			// Wire progress reporter and serial console callback early so they
			// are active during Deploy() as well as Finalize(). During download/
			// extract the progress bar owns the console line; the reportStep
			// calls in Deploy fire only at phase boundaries (not during streaming)
			// so they do not fight with the \r progress bar overwrites.
			if fd, ok := deployer.(*deploy.FilesystemDeployer); ok {
				fd.Progress = progressReporter
				fd.ConsoleCallback = func(msg string) {
					printPhaseUpdate("Deploying", msg)
				}
			}

			if err := deployer.Preflight(ctx, img.DiskLayout, *hw); err != nil {
				printPhase(phaseFailed, "Preflight checks")
				printDeployError("preflight", err.Error())
				deployLog.Error().Str("component", "deploy").Err(err).Msg("preflight failed")
				progressReporter.EndPhase(err.Error())
				return fmt.Errorf("preflight: %w", err)
			}
			printPhase(phaseDone, "Preflight checks passed")
			deployLog.Info().Str("component", "deploy").Msg("preflight passed")
			progressReporter.EndPhase("")

			// Step 5: Deploy.
			deployLog.Info().Str("component", "deploy").Msg("starting image write")
			opts := deploy.DeployOpts{
				ImageURL:         blobURL,
				AuthToken:        cfg.AuthToken,
				TargetDisk:       flagDisk,
				Format:           string(img.Format),
				MountRoot:        mountRoot,
				NoRollback:       flagNoRollback,
				SkipVerify:       flagSkipVerify,
				ExpectedChecksum: img.Checksum,
				Reporter:         progressReporter,
			}

			start := time.Now()
			var lastPhase string
			var lastLoggedPct int64
			progressFn := func(written, total int64, phase string) {
				if phase != lastPhase {
					if lastPhase != "" {
						consolePrintln("") // end the previous \r line
						printPhase(phaseDone, phaseLabel(lastPhase))
					}
					lastPhase = phase
					deployLog.Info().Str("component", "deploy").Str("phase", phase).Msg("deployment phase started")
				}
				printProgressBar(phaseLabel(phase), written, total)

				if total > 0 {
					pct := float64(written) / float64(total) * 100
					milestone := int64(pct/10) * 10
					if milestone > lastLoggedPct && milestone > 0 {
						lastLoggedPct = milestone
						deployLog.Info().Str("component", "deploy").
							Str("phase", phase).Int64("pct", milestone).
							Str("written", humanBytes(written)).Str("total", humanBytes(total)).
							Msg("image write progress")
					}
				}
			}

			if err := deployer.Deploy(ctx, opts, progressFn); err != nil {
				consolePrintln("") // end any in-progress \r line
				if lastPhase != "" {
					printPhase(phaseFailed, phaseLabel(lastPhase))
				}
				if ctx.Err() != nil {
					printDeployError("deploy", fmt.Sprintf("timed out after %s", deployTimeout))
					deployLog.Error().Str("component", "deploy").
						Dur("timeout", deployTimeout).
						Msg("deployment timed out — rollback attempted")
					return fmt.Errorf("deploy: timed out after %s (limit set by --timeout / CLUSTR_DEPLOY_TIMEOUT): %w",
						deployTimeout, err)
				}
				printDeployError("deploy", err.Error())
				deployLog.Error().Str("component", "deploy").Err(err).Msg("image write failed")
				return fmt.Errorf("deploy: %w", err)
			}
			// Close the last progress line and mark it done.
			consolePrintln("")
			if lastPhase != "" {
				printPhase(phaseDone, phaseLabel(lastPhase))
			}
			elapsed := time.Since(start).Round(time.Second)
			deployLog.Info().Str("component", "deploy").Str("duration", elapsed.String()).Msg("image write complete")

			// Step 6: Finalize.
			printPhase(phaseInProgress, "Finalizing node (hostname, network, SSH keys, bootloader)")
			deployLog.Info().Str("component", "chroot").Msg("applying node configuration")
			progressReporter.StartPhase("finalizing", 0)
			// Wire phone-home injection (ADR-0008): set node token and verify-boot URL
			// on the deployer before Finalize so it writes them into the deployed rootfs.
			if phi, ok := deployer.(deploy.PhoneHomeInjector); ok && nodeCfg != nil {
				verifyBootURL := cfg.ServerURL + "/api/v1/nodes/" + nodeCfg.ID + "/verify-boot"
				phi.SetPhoneHome(cfg.AuthToken, verifyBootURL)
			}
			// Wire clustr-clientd injection: set the WebSocket URL on the deployer.
			if ci, ok := deployer.(deploy.ClientdInjector); ok && nodeCfg != nil {
				clientdURL := httpToWS(cfg.ServerURL) + "/api/v1/nodes/" + nodeCfg.ID + "/clientd/ws"
				ci.SetClientdURL(clientdURL)
			}
			// Wire clustr-clientd binary path so injectClientd can copy it into rootfs.
			if bs, ok := deployer.(deploy.ClientdBinPathSetter); ok {
				bs.SetClientdBinPath(cfg.ClientdBinPath)
			}
			// Wire per-image install instructions into the deployer.
			if is, ok := deployer.(deploy.InstallInstructionsSetter); ok {
				is.SetInstallInstructions(img.InstallInstructions)
			}
			// Switch the console callback label from "Deploying" to "Finalizing"
			// now that we are entering the finalize phase. Progress is already set.
			if fd, ok := deployer.(*deploy.FilesystemDeployer); ok {
				fd.ConsoleCallback = func(msg string) {
					printPhaseUpdate("Finalizing", msg)
				}
			}
			if err := deployer.Finalize(ctx, *nodeCfg, mountRoot); err != nil {
				consolePrintln("") // close the in-progress \r line
				printPhase(phaseFailed, "Finalize")
				printDeployError("finalize", err.Error())
				deployLog.Error().Str("component", "chroot").Err(err).Msg("finalize failed")
				progressReporter.EndPhase(err.Error())
				return fmt.Errorf("finalize: %w", err)
			}
			consolePrintln("") // advance past the \r sub-step line
			printPhase(phaseDone, "Node configuration applied")
			deployLog.Info().Str("component", "chroot").Msg("node configuration applied")
			progressReporter.EndPhase("")

			// UEFI boot is handled entirely by grub2-install --removable writing
			// \EFI\BOOT\BOOTX64.EFI during Finalize. No NVRAM entry is created.
			// Firmware uses removable-media auto-discovery (UEFI §3.5.1.1).
			// --fix-efi is accepted for backwards compatibility but is a no-op.
			// See docs/boot-architecture.md §8.
			if flagFixEFI {
				deployLog.Warn().Str("component", "efiboot").
					Msg("--fix-efi flag is deprecated and has no effect; clustr no longer manages EFI NVRAM entries (see docs/boot-architecture.md §8)")
			}

			totalDuration := time.Since(start).Round(time.Second)
			consolePrintln("")
			consolePrint(ansiBold + ansiGreen)
			consolePrintln("  Deployment complete.")
			consolePrint(ansiReset)
			consolePrintln(fmt.Sprintf("  Node:     %s", nodeCfg.Hostname))
			consolePrintln(fmt.Sprintf("  Image:    %s %s", img.Name, img.Version))
			consolePrintln(fmt.Sprintf("  Duration: %s", totalDuration))
			consolePrintln("")
			return nil
		},
	}

	cmd.Flags().StringVar(&flagImage, "image", "", "Image ID to deploy (required without --auto)")
	cmd.Flags().StringVar(&flagDisk, "disk", "", "Target block device, e.g. /dev/nvme0n1 (auto-detected if omitted)")
	cmd.Flags().StringVar(&flagMountRoot, "mount-root", "", "Temporary mount point directory (auto-created if omitted)")
	cmd.Flags().BoolVar(&flagFixEFI, "fix-efi", false, "Repair EFI boot entries after deployment")
	cmd.Flags().BoolVar(&flagAuto, "auto", false,
		"Auto mode: register with server, wait for image assignment, then deploy (for PXE-booted nodes)")
	cmd.Flags().BoolVar(&flagNoRollback, "no-rollback", false,
		"Skip partition table backup/restore on failure (use when intentionally wiping a disk)")
	cmd.Flags().BoolVar(&flagSkipVerify, "skip-verify", false,
		"Skip image checksum verification (deploy even if the sha256 does not match)")
	cmd.Flags().StringVar(&flagTimeout, "timeout", "30m",
		"Maximum time allowed for the entire deployment (env: CLUSTR_DEPLOY_TIMEOUT, e.g. 30m, 1h)")
	cmd.Flags().StringVar(&flagMulticastMode, "multicast", "auto",
		"Multicast mode: auto (default), off (force unicast), or require (error if multicast unavailable)")

	return cmd
}

// validateMulticastFlag returns an error when flagMulticast is not a recognized value.
func validateMulticastFlag(val string) error {
	switch val {
	case "auto", "off", "require":
		return nil
	default:
		return fmt.Errorf("--multicast must be auto, off, or require; got %q", val)
	}
}

// runAutoDeployMode implements deploy --auto.
// It discovers hardware, registers the node with the server, then waits until
// an admin assigns a base image, at which point it proceeds with full deployment.
func runAutoDeployMode() error {
	ctx := context.Background()
	c := clientFromFlags()

	// Resolve server URL for the header (best-effort; may be empty before config load).
	cfg := config.LoadClientConfig()
	if flagServer != "" {
		cfg.ServerURL = flagServer
	}

	// Step 1: Discover hardware.
	printPhase(phaseInProgress, "Discovering hardware")
	hw, err := hardware.Discover()
	if err != nil {
		printPhase(phaseFailed, "Hardware discovery")
		printDeployError("hardware_discovery", err.Error())
		return fmt.Errorf("hardware discovery: %w", err)
	}
	printPhase(phaseDone, "Hardware discovered")

	primaryMAC := primaryMACFromHW(hw)
	if primaryMAC == "" {
		printDeployError("hardware_discovery", "no usable NIC found — cannot register node")
		return fmt.Errorf("no usable NIC found — cannot register node")
	}

	// Set up remote log writer once we have the MAC.
	remoteWriter := client.NewRemoteLogWriter(c, primaryMAC, hw.Hostname, client.WithComponent("deploy"))
	defer remoteWriter.Close()
	multi := zerolog.MultiLevelWriter(zerolog.ConsoleWriter{Out: os.Stderr}, remoteWriter)
	deployLog := zerolog.New(multi).With().Timestamp().Logger()
	// Wire the deploy package so subprocess output goes through the same logger.
	deploy.SetLogger(deployLog)

	deployLog.Info().Str("mac", primaryMAC).Str("hostname", hw.Hostname).
		Msg("hardware discovered, registering with server")

	// Step 2: Register with the server (upsert).
	hwJSON, err := json.Marshal(hw)
	if err != nil {
		return fmt.Errorf("marshal hardware profile: %w", err)
	}

	printPhase(phaseInProgress, "Registering with server")
	regResp, err := c.RegisterNode(ctx, api.RegisterRequest{
		HardwareProfile:  hwJSON,
		DetectedFirmware: hw.Firmware,
		MulticastMode:    flagMulticastMode,
	})
	if err != nil {
		printPhase(phaseFailed, "Registration")
		printDeployError("register", err.Error())
		return fmt.Errorf("register node: %w", err)
	}
	printPhase(phaseDone, "Registered with server")

	deployLog.Info().
		Str("action", regResp.Action).
		Str("node_id", regResp.NodeConfig.ID).
		Msg("registered with server")

	// Update log writer with the server-assigned hostname now that we have it.
	nodeName := hw.Hostname
	if regResp.NodeConfig != nil && regResp.NodeConfig.Hostname != "" {
		remoteWriter.SetHostname(regResp.NodeConfig.Hostname)
		nodeName = regResp.NodeConfig.Hostname
	}

	// Step 3: Act on server directive.
	//
	// Deploy phase ordering (per docs/PHASE-SEQUENCE-DEPLOY.md):
	//   1. Hardware discovery  (done above)
	//   2. Server registration (done above)
	//   3. BIOS apply         — TODO(#159): hook here when NodeConfig.BIOSOnly is set
	//   4. Image fetch        — unicast (below) or multicast (Sprint 25 #157 Commit 3)
	//   5. Partition + write
	//   6. Finalize + reboot
	switch regResp.Action {
	case "deploy":
		// Print header now that we have node identity; image will be fetched next.
		printDeployHeader(nodeName, "", cfg.ServerURL)
		printPhase(phaseDone, "Image assigned — proceeding with deployment")
		return runAutoDeployImage(ctx, c, *regResp.NodeConfig, deployLog, remoteWriter)

	case "wait":
		printDeployHeader(nodeName, "waiting for assignment", cfg.ServerURL)
		printPhase(phaseInProgress, "Waiting for admin to assign an image (polling every 30s)")
		deployLog.Info().Msg("entering wait loop — assign an image via the clustr UI or API")
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-sleepCtx(ctx, 30*time.Second):
			}

			nodeCfg, err := c.GetNodeConfigByMAC(ctx, primaryMAC)
			if err != nil {
				deployLog.Warn().Err(err).Msg("poll failed, retrying")
				continue
			}
			if nodeCfg.BaseImageID != "" {
				deployLog.Info().Str("image_id", nodeCfg.BaseImageID).Msg("image assigned, starting deployment")
				consolePrintln("") // close the waiting line
				printPhase(phaseDone, "Image assigned — proceeding with deployment")
				// Admin may have assigned a hostname since registration — update now.
				if nodeCfg.Hostname != "" {
					remoteWriter.SetHostname(nodeCfg.Hostname)
				}
				return runAutoDeployImage(ctx, c, *nodeCfg, deployLog, remoteWriter)
			}
			deployLog.Debug().Msg("no image assigned yet, still waiting")
		}

	case "capture":
		printPhase(phaseFailed, "Capture mode not yet implemented")
		deployLog.Info().Msg("capture action received — not yet implemented")
		return nil

	default:
		return fmt.Errorf("unknown action from server: %s", regResp.Action)
	}
}

// attemptMulticastReceive tries to join a multicast session and start udp-receiver.
// On success it returns a ReadCloser wrapping udp-receiver's stdout and the
// session ID. On any failure it returns nil, "" so the caller falls back to
// unicast HTTP. The caller must call Close() on the returned ReadCloser when
// done, even if Deploy() succeeds.
//
// This function is called by runAutoDeployImage when CLUSTR_MULTICAST_ENABLED=1.
// It runs only inside the initramfs where /usr/bin/udp-receiver is available.
func attemptMulticastReceive(ctx context.Context, c *client.Client, imageID string, nodeCfg api.NodeConfig, deployLog zerolog.Logger) (io.ReadCloser, string) {
	// Check that udp-receiver binary is present.
	udpRecvBin, err := exec.LookPath("udp-receiver")
	if err != nil {
		deployLog.Warn().Msg("multicast: udp-receiver not found in PATH — falling back to unicast")
		return nil, ""
	}

	deployLog.Info().Str("image_id", imageID).Str("node_id", nodeCfg.ID).
		Msg("multicast: enrolling node in multicast session")
	printPhase(phaseInProgress, "Enrolling in multicast session")

	// Enqueue this node in the multicast session.
	enqResp, err := c.MulticastEnqueue(ctx, client.MulticastEnqueueRequest{
		ImageID: imageID,
		NodeID:  nodeCfg.ID,
	})
	if err != nil {
		deployLog.Warn().Err(err).Msg("multicast: enqueue failed — falling back to unicast")
		return nil, ""
	}
	sessionID := enqResp.SessionID
	deployLog.Info().Str("session_id", sessionID).Msg("multicast: enrolled in session")
	printPhase(phaseDone, fmt.Sprintf("Enrolled in multicast session %s — waiting for transmission window", sessionID))

	// Long-poll until session is transmitting or server signals fallback.
	// Cap total wait at 3 minutes (configurable window is typically 60s + tolerance).
	deadline := time.Now().Add(3 * time.Minute)
	printPhase(phaseInProgress, "Waiting for multicast transmission window")
	var descriptor *client.MulticastSessionDescriptor
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			deployLog.Warn().Msg("multicast: context cancelled while waiting for session")
			return nil, sessionID
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
		result, waitErr := c.MulticastWait(waitCtx, sessionID, nodeCfg.ID)
		waitCancel()
		if waitErr != nil {
			deployLog.Warn().Err(waitErr).Msg("multicast: wait poll failed — falling back to unicast")
			return nil, sessionID
		}
		if result.Fallback {
			deployLog.Info().Msg("multicast: server signalled fallback to unicast")
			printPhase(phaseDone, "Multicast session below threshold — using unicast")
			return nil, sessionID
		}
		if result.Descriptor != nil {
			descriptor = result.Descriptor
			deployLog.Info().
				Str("group", descriptor.MulticastGroup).
				Int("port", descriptor.SenderPort).
				Msg("multicast: session descriptor received — starting udp-receiver")
			break
		}
		// Status is "staging" — sleep briefly and retry.
		time.Sleep(2 * time.Second)
	}
	if descriptor == nil {
		deployLog.Warn().Msg("multicast: timed out waiting for session descriptor — falling back to unicast")
		return nil, sessionID
	}

	printPhase(phaseInProgress, fmt.Sprintf("Receiving multicast stream from %s:%d",
		descriptor.MulticastGroup, descriptor.SenderPort))

	// Fork udp-receiver. Its stdout is the image byte stream.
	// #nosec G204 -- udpRecvBin is resolved by exec.LookPath; args are from server-provided descriptor
	cmd := exec.CommandContext(ctx, udpRecvBin, //#nosec G204
		"--mcast-rdv-addr", descriptor.MulticastGroup,
		"--mcast-data-addr", descriptor.MulticastGroup,
		"--portbase", strconv.Itoa(descriptor.SenderPort),
		"--nosync", // don't fsync; deployer handles durability
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		deployLog.Warn().Err(err).Msg("multicast: failed to create stdout pipe — falling back to unicast")
		return nil, sessionID
	}
	if err := cmd.Start(); err != nil {
		deployLog.Warn().Err(err).Msg("multicast: failed to start udp-receiver — falling back to unicast")
		return nil, sessionID
	}

	deployLog.Info().Int("pid", cmd.Process.Pid).Msg("multicast: udp-receiver started")

	// Return a ReadCloser that closes the pipe and waits for the process.
	return &udpRecvReader{
		ReadCloser: stdout,
		cmd:        cmd,
		log:        deployLog,
	}, sessionID
}

// udpRecvReader wraps udp-receiver's stdout pipe and waits for the process on Close.
type udpRecvReader struct {
	io.ReadCloser
	cmd *exec.Cmd
	log zerolog.Logger
}

func (r *udpRecvReader) Close() error {
	err := r.ReadCloser.Close()
	if waitErr := r.cmd.Wait(); waitErr != nil {
		r.log.Warn().Err(waitErr).Msg("multicast: udp-receiver exited with error")
		if err == nil {
			err = waitErr
		}
	}
	return err
}

// sleepCtx returns a channel that closes after d, or immediately if ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
		case <-time.After(d):
		}
	}()
	return ch
}

// runAutoDeployImage performs the full deployment given a NodeConfig with an assigned image.
// The node config must have BaseImageID set.
func runAutoDeployImage(ctx context.Context, c *client.Client, nodeCfg api.NodeConfig, deployLog zerolog.Logger, remoteWriter *client.RemoteLogWriter) (retErr error) {
	// Panic recovery: if any deployment sub-call panics (e.g. nil pointer in a
	// hardware probe or partition library), catch it here, flush buffered logs so
	// the last messages before the panic reach the server, then return as an error
	// rather than crashing PID 1 in the initramfs.
	defer func() {
		if r := recover(); r != nil {
			deployLog.Error().
				Interface("panic", r).
				Stack().
				Msg("deploy panicked — caught by recovery wrapper")
			retErr = Wrap(ExitPanic, "panic", fmt.Errorf("deploy panicked: %v", r))
			// Flush immediately so crash logs reach the server before we exit.
			remoteWriter.FlushSync()
		}
	}()

	// Deferred failure reporter: if retErr is non-nil when this function returns
	// (including from early pre-deploy error paths like image-not-ready, preflight
	// fail, or temp-dir creation failure), unconditionally POST deploy-failed so
	// the server transitions the node to NodeStateFailed and the admin can see it.
	//
	// This is defensive against the VM202-style hang where a pre-deploy error caused
	// the node to remain in reimage_pending indefinitely, blocking all future deploys
	// until an admin manually reset the state.
	//
	// NOTE: We use a fresh context.Background() so the report is not cancelled if
	// the parent ctx is already done (e.g. signal received mid-deploy). The
	// deploy-complete path uses ReportDeployCompleteWithRetry with its own contexts,
	// so by the time we reach a successful return retErr is nil and this defer is a no-op.
	deployCompleted := false
	defer func() {
		if retErr == nil {
			return // successful deploy — do not send deploy-failed
		}
		if deployCompleted {
			return // server already received deploy-complete — do not double-transition
		}
		// Build classified payload. Fall back to ExitUnknown if retErr is not a DeployError.
		payload := api.DeployFailedPayload{
			ExitCode: int(ExitUnknown),
			ExitName: ExitUnknown.Name(),
			Phase:    "unknown",
			Message:  retErr.Error(),
		}
		var de *DeployError
		var be *deploy.BootloaderError
		if errors.As(retErr, &de) {
			payload.ExitCode = int(de.Code)
			payload.ExitName = de.Code.Name()
			payload.Phase = de.Phase
			payload.Message = de.Error()
		} else if errors.As(retErr, &be) {
			// BootloaderError from pkg/deploy: grub2-install failed on all target
			// disks. Map to ExitBootloader so the operator sees the correct exit
			// code without having to dig through logs.
			payload.ExitCode = int(ExitBootloader)
			payload.ExitName = ExitBootloader.Name()
			payload.Phase = "finalize/bootloader"
			payload.Message = be.Error()
		}

		deployLog.Error().
			Err(retErr).
			Int("exit_code", payload.ExitCode).
			Str("exit_name", payload.ExitName).
			Str("phase", payload.Phase).
			Msg("deploy failed — reporting to server (deferred)")

		reportCtx, reportCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer reportCancel()
		if reportErr := c.ReportDeployFailed(reportCtx, nodeCfg.ID, payload); reportErr != nil {
			deployLog.Warn().Err(reportErr).Msg("deferred deploy-failed report to server failed (non-fatal)")
		}
		remoteWriter.FlushSync()
	}()

	cfg := config.LoadClientConfig()
	if flagServer != "" {
		cfg.ServerURL = flagServer
	}

	// Ensure the log writer reflects the final resolved hostname (covers the
	// case where group inheritance or a late admin assignment changed it).
	if nodeCfg.Hostname != "" {
		remoteWriter.SetHostname(nodeCfg.Hostname)
	}

	// Create progress reporter — best-effort, failures don't abort deployment.
	reporter := client.NewProgressReporter(c, nodeCfg.PrimaryMAC, nodeCfg.Hostname)
	defer reporter.Complete()

	// Fetch image details.
	reporter.SetMessage("Fetching image details")
	printPhase(phaseInProgress, "Fetching image details")
	img, err := c.GetImage(ctx, nodeCfg.BaseImageID)
	if err != nil {
		printPhase(phaseFailed, "Image details fetch")
		printDeployError("image_fetch", err.Error())
		return Wrap(ExitImageFetch, "image_fetch", fmt.Errorf("fetch image %s: %w", nodeCfg.BaseImageID, err))
	}
	if img.Status != api.ImageStatusReady {
		printPhase(phaseFailed, "Image details fetch")
		printDeployError("image_fetch", fmt.Sprintf("image %s is not ready (status: %s)", img.ID, img.Status))
		return Wrap(ExitImageFetch, "image_fetch", fmt.Errorf("image %s is not ready (status: %s)", img.ID, img.Status))
	}
	printPhase(phaseDone, fmt.Sprintf("Image details fetched  (%s %s)", img.Name, img.Version))

	// Print the deploy header now that we have both node and image identity.
	printDeployHeader(
		nodeCfg.Hostname,
		fmt.Sprintf("%s %s", img.Name, img.Version),
		cfg.ServerURL,
	)

	deployLog.Info().Str("image", img.Name).Str("version", img.Version).
		Str("format", string(img.Format)).Msg("image details fetched")

	// Resolve hardware for preflight.
	reporter.SetMessage("Discovering hardware")
	printPhase(phaseInProgress, "Discovering hardware")
	hw, err := hardware.Discover()
	if err != nil {
		printPhase(phaseFailed, "Hardware discovery")
		printDeployError("hardware_discovery", err.Error())
		return Wrap(ExitHardware, "hardware_discovery", fmt.Errorf("hardware discovery for preflight: %w", err))
	}

	// Resolve the effective disk layout using the three-level hierarchy:
	//   1. Node-level override (highest)
	//   2. Group-level override
	//   3. Image default (lowest)
	var group *api.NodeGroup
	if nodeCfg.GroupID != "" {
		g, gErr := c.GetNodeGroup(ctx, nodeCfg.GroupID)
		if gErr != nil {
			deployLog.Warn().Err(gErr).Str("group_id", nodeCfg.GroupID).
				Msg("could not fetch node group — falling back to image layout")
		} else {
			group = g
		}
	}
	effectiveLayout := nodeCfg.EffectiveLayout(img, group)
	layoutSource := nodeCfg.EffectiveLayoutSource(img, group)

	// Auto-correct layout when the node reported its firmware at registration and
	// the image's firmware type doesn't match. Only applies when no operator override
	// is present (layoutSource == "image") so admin overrides are always respected.
	if nodeCfg.DetectedFirmware != "" && layoutSource == "image" {
		effectiveLayout = layout.AutoCorrectForFirmware(effectiveLayout, string(img.Firmware), nodeCfg.DetectedFirmware, nodeCfg.ID, nodeCfg.Hostname)
	}

	partCount := len(effectiveLayout.Partitions)
	firmware := string(img.Firmware)
	if nodeCfg.DetectedFirmware != "" {
		firmware = nodeCfg.DetectedFirmware
	}
	printPhase(phaseDone, fmt.Sprintf("Disk layout resolved  (%s, %d partition(s), source: %s)", strings.ToUpper(firmware), partCount, layoutSource))

	deployLog.Info().Str("layout_source", layoutSource).
		Msg("disk layout resolved")

	mountRoot, err := os.MkdirTemp("", "clustr-auto-deploy-*")
	if err != nil {
		return Wrap(ExitGeneric, "setup", fmt.Errorf("create temp mount root: %w", err))
	}
	defer os.RemoveAll(mountRoot)

	var deployer deploy.Deployer
	switch img.Format {
	case api.ImageFormatBlock:
		deployer = &deploy.BlockDeployer{}
	default:
		deployer = &deploy.FilesystemDeployer{}
	}

	// Wire progress reporter and serial console callback early so they are
	// active during Deploy() as well as Finalize(). The console callback
	// label is updated to "Finalizing" before Finalize() is called below.
	if fd, ok := deployer.(*deploy.FilesystemDeployer); ok {
		fd.Progress = reporter
		fd.ConsoleCallback = func(msg string) {
			printPhaseUpdate("Deploying", msg)
		}
	}

	reporter.SetMessage("Running preflight checks")
	printPhase(phaseInProgress, "Running preflight checks")
	deployLog.Info().Msg("running preflight checks")
	reporter.StartPhase("preflight", 0)
	if err := deployer.Preflight(ctx, effectiveLayout, *hw); err != nil {
		printPhase(phaseFailed, "Preflight checks")
		printDeployError("preflight", err.Error())
		reporter.EndPhase(err.Error())
		return Wrap(ExitHardware, "preflight", fmt.Errorf("preflight: %w", err))
	}
	printPhase(phaseDone, "Preflight checks passed")
	reporter.EndPhase("")

	blobURL := cfg.ServerURL + "/api/v1/images/" + img.ID + "/blob"
	deployLog.Info().Str("url", blobURL).Msg("starting image write")

	// ── Multicast delivery (Phase 4 — Sprint 25 #157 Commit 3) ───────────────
	// When CLUSTR_MULTICAST_ENABLED=1 is set by the initramfs init script (from
	// clustr.multicast=1 in the kernel cmdline), attempt to receive the image
	// via UDPCast multicast. On any failure, fall back to unicast HTTP.
	//
	// Multicast path:
	//   1. POST /multicast/enqueue to join the session for this (image, layout)
	//   2. Long-poll GET /multicast/sessions/{id}/wait until descriptor arrives
	//      or server signals fallback_unicast
	//   3. Fork udp-receiver piped into deployer.Deploy via ImageStream
	//   4. POST outcome (success | failed | fellback_unicast) to server
	//   5. On any error, fall back to unicast HTTP silently
	var imageStream io.ReadCloser
	var multicastSessionID string
	multicastEnabled := os.Getenv("CLUSTR_MULTICAST_ENABLED") == "1"
	if multicastEnabled {
		imageStream, multicastSessionID = attemptMulticastReceive(ctx, c, img.ID, nodeCfg, deployLog)
	}
	defer func() {
		if imageStream != nil {
			imageStream.Close()
		}
	}()

	opts := deploy.DeployOpts{
		ImageURL:         blobURL,
		AuthToken:        cfg.AuthToken,
		TargetDisk:       "", // auto-detect
		Format:           string(img.Format),
		MountRoot:        mountRoot,
		ExpectedChecksum: img.Checksum,
		Reporter:         reporter,
		ImageStream:      imageStream,
	}

	var lastLoggedPct int64
	var lastPhase string
	progressFn := func(written, total int64, phase string) {
		// When phase transitions, close the previous progress line and start new one.
		if phase != lastPhase {
			if lastPhase != "" {
				consolePrintln("") // end the previous \r line
				printPhase(phaseDone, phaseLabel(lastPhase))
			}
			lastPhase = phase
			deployLog.Info().Str("phase", phase).Msg("deployment phase started")
		}
		printProgressBar(phaseLabel(phase), written, total)

		// Log via zerolog at every 10% milestone so the remote log stream
		// shows download progress even in silent initramfs environments.
		if total > 0 {
			pct := float64(written) / float64(total) * 100
			milestone := int64(pct/10) * 10
			if milestone > lastLoggedPct && milestone > 0 {
				lastLoggedPct = milestone
				deployLog.Info().
					Str("phase", phase).
					Int64("pct", milestone).
					Str("written", humanBytes(written)).
					Str("total", humanBytes(total)).
					Msg("image write progress")
			}
		}
	}

	deployLog.Info().Str("url", blobURL).Msg("downloading image blob from server")
	start := time.Now()
	deployErr := deployer.Deploy(ctx, opts, progressFn)
	if deployErr != nil && imageStream != nil {
		// Multicast delivery failed — record fallback outcome and retry via unicast.
		deployLog.Warn().Err(deployErr).Msg("multicast delivery failed — falling back to unicast HTTP")
		imageStream.Close()
		imageStream = nil
		if multicastSessionID != "" {
			recordOutcomeCtx, rcCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = c.MulticastRecordOutcome(recordOutcomeCtx, multicastSessionID, nodeCfg.ID, "fellback_unicast")
			rcCancel()
		}
		// Retry with unicast: rebuild opts without ImageStream.
		opts.ImageStream = nil
		// Reset progress tracking for the retry.
		lastLoggedPct = 0
		lastPhase = ""
		deployErr = deployer.Deploy(ctx, opts, progressFn)
	}
	if deployErr != nil {
		consolePrintln("") // end any in-progress \r line
		if lastPhase != "" {
			printPhase(phaseFailed, phaseLabel(lastPhase))
		}
		deployLog.Error().Err(deployErr).Msg("image deploy failed")
		var failPhase string
		if lastPhase != "" {
			failPhase = lastPhase
		} else {
			failPhase = "deploy"
		}
		printDeployError(failPhase, deployErr.Error())
		// Record failed outcome if we were in a multicast session.
		if multicastSessionID != "" {
			recordOutcomeCtx, rcCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = c.MulticastRecordOutcome(recordOutcomeCtx, multicastSessionID, nodeCfg.ID, "failed")
			rcCancel()
		}
		// Classify the failure. The deployer runs partition, format, download, and
		// extract phases internally. We surface ExitDownload here since a blob stream
		// or checksum failure is the most common path; partition/format errors from
		// the underlying deployer will still surface via the error message.
		return Wrap(ExitDownload, "deploy", fmt.Errorf("deploy: %w", deployErr))
	}
	// Record success outcome if we received via multicast.
	if multicastSessionID != "" {
		outcome := "success"
		if imageStream == nil {
			// imageStream was closed above after multicast failure — we fell back.
			outcome = "fellback_unicast"
		}
		recordOutcomeCtx, rcCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.MulticastRecordOutcome(recordOutcomeCtx, multicastSessionID, nodeCfg.ID, outcome)
		rcCancel()
	}
	// Close the last progress line and mark it done.
	consolePrintln("")
	if lastPhase != "" {
		printPhase(phaseDone, phaseLabel(lastPhase))
	}
	elapsed := time.Since(start).Round(time.Second)
	deployLog.Info().Str("duration", elapsed.String()).Msg("image write complete")

	printPhase(phaseInProgress, "Finalizing node (hostname, network, SSH keys, bootloader)")
	deployLog.Info().Str("hostname", nodeCfg.Hostname).Msg("applying node configuration (hostname, network, SSH keys)")
	reporter.StartPhase("finalizing", 0)
	// Wire phone-home injection (ADR-0008): set node token and verify-boot URL
	// on the deployer before Finalize so it writes them into the deployed rootfs.
	if phi, ok := deployer.(deploy.PhoneHomeInjector); ok {
		verifyBootURL := cfg.ServerURL + "/api/v1/nodes/" + nodeCfg.ID + "/verify-boot"
		phi.SetPhoneHome(cfg.AuthToken, verifyBootURL)
	}
	// Wire clustr-clientd injection: set the WebSocket URL on the deployer.
	if ci, ok := deployer.(deploy.ClientdInjector); ok {
		clientdURL := httpToWS(cfg.ServerURL) + "/api/v1/nodes/" + nodeCfg.ID + "/clientd/ws"
		ci.SetClientdURL(clientdURL)
	}
	// Wire clustr-clientd binary path so injectClientd can copy it into rootfs.
	if bs, ok := deployer.(deploy.ClientdBinPathSetter); ok {
		bs.SetClientdBinPath(cfg.ClientdBinPath)
	}
	// Wire per-image install instructions into the deployer.
	if is, ok := deployer.(deploy.InstallInstructionsSetter); ok {
		is.SetInstallInstructions(img.InstallInstructions)
	}
	// Switch the console callback label from "Deploying" to "Finalizing"
	// now that we are entering the finalize phase. Progress is already set.
	if fd, ok := deployer.(*deploy.FilesystemDeployer); ok {
		fd.ConsoleCallback = func(msg string) {
			printPhaseUpdate("Finalizing", msg)
		}
	}
	if err := deployer.Finalize(ctx, nodeCfg, mountRoot); err != nil {
		consolePrintln("") // close the in-progress \r line
		printPhase(phaseFailed, "Finalize")
		printDeployError("finalize", err.Error())
		deployLog.Error().Err(err).Msg("finalize failed")
		reporter.EndPhase(err.Error())
		return Wrap(ExitFinalize, "finalize", fmt.Errorf("finalize: %w", err))
	}
	consolePrintln("") // advance past the \r sub-step line
	printPhase(phaseDone, "Node configuration applied")
	deployLog.Info().Str("hostname", nodeCfg.Hostname).Msg("node configuration applied")
	reporter.EndPhase("")

	// ── EFI boot setup (UEFI layouts only) ──────────────────────────────────
	// Skip efibootmgr entirely if the effective disk layout has no ESP partition.
	// This handles two cases:
	//   1. BIOS images (img.Firmware == "bios"): no ESP in the image's default layout.
	//   2. UEFI-capable image deployed with a BIOS disk layout override (e.g. a
	//      node that has a DiskLayoutOverride with a biosboot partition instead of
	//      an ESP). In this case img.Firmware may be "uefi" but the node is being
	//      deployed as BIOS — the UEFI firmware check alone is insufficient.
	//
	// The authoritative signal is whether the effective layout has an ESP-flagged
	// partition. If none, the deploy target has no EFI partition and efibootmgr
	// cannot write NVRAM entries (SeaBIOS VMs have no EFI vars at all; calling
	// efibootmgr produces "EFI variables are not supported on this system").
	layoutHasESP := false
	effectiveESPPartNum := 1
	for i, p := range effectiveLayout.Partitions {
		for _, flag := range p.Flags {
			if flag == "esp" || flag == "boot" {
				layoutHasESP = true
				effectiveESPPartNum = i + 1
				break
			}
		}
		if layoutHasESP {
			break
		}
	}
	if !layoutHasESP {
		deployLog.Info().
			Str("firmware", string(img.Firmware)).
			Str("layout_source", layoutSource).
			Msg("EFI boot setup: skipping — effective layout has no ESP partition (BIOS deploy or BIOS layout override)")
	} else {
		deployLog.Info().
			Str("disk", deployer.ResolvedDisk()).
			Int("esp_part", effectiveESPPartNum).
			Msg("EFI boot setup: skipping NVRAM entry — relying on UEFI removable-media discovery (see docs/boot-architecture.md §8)")
	}
	// ──────────────────────────────────────────────────────────────────────

	// ── Deploy complete callback ────────────────────────────────────────────
	// Tell the server the deploy succeeded. This sets deploy_completed_preboot_at
	// and clears reimage_pending, transitioning the node to NodeStateDeployedPreboot.
	// On the next PXE boot (after the node reboots) the boot handler will see
	// NodeStateDeployed and return "#!ipxe\nexit" so the BIOS boots from disk.
	//
	// This replaces the old FlipToDisk/SetNextBoot(disk) approach: the PXE
	// server handles boot routing, no BMC interaction required.
	printPhase(phaseInProgress, "Reporting deploy-complete to server")
	reporter.StartPhase("deploy-complete", 0)
	deployLog.Info().Str("hostname", nodeCfg.Hostname).
		Msg("reporting deploy-complete to server")

	// Use a background context so the HTTP call is not cancelled if the parent
	// deploy ctx is near its deadline, but still bound by per-attempt timeouts
	// inside ReportDeployCompleteWithRetry.
	completeBaseCtx := context.Background()
	completeErr := c.ReportDeployCompleteWithRetry(completeBaseCtx, nodeCfg.ID, 3)

	stateVerified := false
	if completeErr != nil {
		deployLog.Error().Err(completeErr).
			Str("hostname", nodeCfg.Hostname).
			Str("node_id", nodeCfg.ID).
			Msg("deploy-complete HTTP call failed after 3 attempts — starting state verification loop")
	} else {
		deployLog.Info().Str("hostname", nodeCfg.Hostname).
			Msg("deploy-complete reported to server — verifying node state")
	}

	// Verify the server actually recorded the state transition, regardless of
	// whether the HTTP call returned an error. Up to 5 attempts with 2s backoff.
	const maxVerifyAttempts = 5
	for attempt := 1; attempt <= maxVerifyAttempts; attempt++ {
		verifyCtx, verifyCancel := context.WithTimeout(completeBaseCtx, 10*time.Second)
		// Use GetSelfNode (GET /nodes/{id}/self) which is accessible to
		// node-scoped deploy tokens. GetNode (GET /nodes/{id}) requires admin scope.
		updated, err := c.GetSelfNode(verifyCtx, nodeCfg.ID)
		verifyCancel()

		if err != nil {
			deployLog.Warn().Err(err).
				Int("attempt", attempt).Int("max", maxVerifyAttempts).
				Msg("state verification: GetNode failed")
		} else if s := updated.State(); s == api.NodeStateDeployedPreboot ||
			s == api.NodeStateDeployedVerified ||
			s == api.NodeStateDeployed {
			// ADR-0008: after deploy-complete the server sets deployed_preboot, not
			// the legacy "deployed" state. Accept all three as a successful outcome.
			deployLog.Info().
				Str("hostname", nodeCfg.Hostname).
				Str("state", string(s)).
				Msg("state verified: deploy recorded by server, next PXE boot will exit to disk")
			stateVerified = true
			break
		} else {
			deployLog.Warn().
				Str("state", string(updated.State())).
				Int("attempt", attempt).Int("max", maxVerifyAttempts).
				Msg("state verification: node not yet in deployed state, retrying")

			// If the HTTP POST succeeded but the state isn't updated yet, retry
			// the POST as well — the server may have had a transient DB error.
			if completeErr == nil && attempt < maxVerifyAttempts {
				retryCtx, retryCancel := context.WithTimeout(completeBaseCtx, 15*time.Second)
				if retryErr := c.ReportDeployCompleteWithRetry(retryCtx, nodeCfg.ID, 1); retryErr != nil {
					deployLog.Warn().Err(retryErr).
						Int("attempt", attempt).Msg("state verification: re-POST deploy-complete failed")
				}
				retryCancel()
			}
		}

		if attempt < maxVerifyAttempts {
			time.Sleep(2 * time.Second)
		}
	}

	if !stateVerified {
		deployLog.Error().
			Str("hostname", nodeCfg.Hostname).
			Str("node_id", nodeCfg.ID).
			Str("mac", nodeCfg.PrimaryMAC).
			Msg("CRITICAL: deploy-complete state verification failed after all retries — " +
				"node is deployed on disk but server state was NOT updated. " +
				"Writing /tmp/clustr-deploy-success flag so init can re-send on next boot.")

		// Write a flag file so the init script can detect this on next boot,
		// re-send the deploy-complete report before entering the wait loop,
		// and avoid triggering another full re-deploy.
		flagErr := os.WriteFile("/tmp/clustr-deploy-success", []byte(nodeCfg.ID+"\n"), 0o644)
		if flagErr != nil {
			deployLog.Error().Err(flagErr).Msg("failed to write /tmp/clustr-deploy-success flag file")
		} else {
			deployLog.Warn().Msg("wrote /tmp/clustr-deploy-success — init script will retry deploy-complete on next boot")
		}

		printPhase(phaseFailed, "Server state verification (flag written — will retry on next boot)")
		reporter.EndPhase("state verification failed — rebooting anyway, flag written")
	} else {
		printPhase(phaseDone, "Deploy-complete reported and verified")
		reporter.EndPhase("")
	}
	// ───────────────────────────────────────────────────────────────────────

	// ── Belt-and-suspenders: flip boot device to disk via power provider ──
	// Even though the PXE handler returns "#!ipxe\nexit" when state=deployed
	// (which should cause BIOS to fall through to local disk), some hypervisors
	// and BIOS implementations don't honour iPXE exit cleanly. Explicitly set
	// the next-boot device to disk so the node never re-enters a PXE loop.
	// We pass cycle=false — the init script handles the reboot below.
	// If the node has no power provider configured, or the call fails for any
	// reason, we log a warning and continue: the iPXE fallthrough path still
	// works on most hardware.
	flipCtx, flipCancel := context.WithTimeout(completeBaseCtx, 15*time.Second)
	if flipErr := c.FlipToDisk(flipCtx, nodeCfg.ID, false); flipErr != nil {
		deployLog.Warn().Err(flipErr).
			Str("node_id", nodeCfg.ID).
			Msg("FlipToDisk call failed (no power provider, or provider error) — " +
				"relying on iPXE exit fallthrough for boot routing")
	} else {
		deployLog.Info().
			Str("node_id", nodeCfg.ID).
			Msg("boot device flipped to disk via power provider")
	}
	flipCancel()
	// ──────────────────────────────────────────────────────────────────────

	totalDuration := time.Since(start).Round(time.Second)
	deployLog.Info().Str("hostname", nodeCfg.Hostname).Str("duration",
		totalDuration.String()).Msg("auto-deployment complete — rebooting")

	// Mark deploy as complete before returning nil so the deferred failure
	// reporter knows NOT to post deploy-failed (node already transitioned).
	deployCompleted = true

	// Flush remote logs before the init script calls reboot. This ensures the
	// "deployment complete" log lines reach the server before the kernel kills
	// the network stack.
	remoteWriter.FlushSync()

	consolePrintln("")
	consolePrint(ansiBold + ansiGreen)
	consolePrintln("  Deployment complete — node will reboot to disk.")
	consolePrint(ansiReset)
	consolePrintln(fmt.Sprintf("  Node:     %s", nodeCfg.Hostname))
	consolePrintln(fmt.Sprintf("  Image:    %s %s", img.Name, img.Version))
	consolePrintln(fmt.Sprintf("  Duration: %s", totalDuration))
	consolePrintln("")
	return nil
}

// ─── fix-efiboot ─────────────────────────────────────────────────────────────

// newFixEFIBootCmd returns the deprecated fix-efiboot diagnostic command.
//
// DEPRECATED: clustr no longer manages EFI NVRAM entries in normal operation.
// Post-deploy UEFI boot relies on UEFI removable-media auto-discovery of
// \EFI\BOOT\BOOTX64.EFI written by grub2-install --removable --no-nvram.
// Use this command only as a manual diagnostic on a node you do not intend
// to reimage — efibootmgr --create bakes the current ESP PARTUUID into the
// device path, which becomes stale after any subsequent reimage.
// See docs/boot-architecture.md §8.
func newFixEFIBootCmd() *cobra.Command {
	var (
		flagDisk    string
		flagESPPart int
		flagLabel   string
		flagLoader  string
	)

	cmd := &cobra.Command{
		Use:   "fix-efiboot",
		Short: "[DEPRECATED] Manual diagnostic: create an EFI NVRAM boot entry",
		Long: `DEPRECATED: clustr no longer manages EFI NVRAM entries during deployment.
Post-deploy UEFI boot relies on UEFI removable-media auto-discovery of
\EFI\BOOT\BOOTX64.EFI. Use this command only as a manual diagnostic tool.

WARNING: efibootmgr --create bakes the current ESP PARTUUID into the NVRAM
device path. This entry becomes stale after any subsequent reimage (pflash
survives disk wipe; the new ESP gets a new PARTUUID). Do not use on nodes
you plan to reimage. See docs/boot-architecture.md §8.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagDisk == "" {
				return fmt.Errorf("--disk is required")
			}

			ctx := context.Background()
			fmt.Fprintf(os.Stderr, "[DEPRECATED] Creating EFI NVRAM entry on %s partition %d (diagnostic only — see docs/boot-architecture.md §8)...\n", flagDisk, flagESPPart)

			if err := deploy.ManualCreateEFIEntry(ctx, flagDisk, flagESPPart, flagLabel, flagLoader); err != nil {
				return fmt.Errorf("fix-efiboot: %w", err)
			}

			fmt.Println("EFI boot entry created (diagnostic only — clustr does not manage NVRAM in normal operation).")
			return nil
		},
	}

	cmd.Flags().StringVar(&flagDisk, "disk", "", "Target disk device, e.g. /dev/nvme0n1 (required)")
	cmd.Flags().IntVar(&flagESPPart, "esp", 1, "ESP partition number (default: 1)")
	cmd.Flags().StringVar(&flagLabel, "label", "Linux", "Boot menu label")
	cmd.Flags().StringVar(&flagLoader, "loader", `\EFI\BOOT\BOOTX64.EFI`, "EFI loader path relative to ESP")

	return cmd
}

// ─── ipmi ────────────────────────────────────────────────────────────────────

// ipmiClientFromFlags builds an ipmi.Client from the standard remote flags.
// If host is empty, the client targets the local BMC.
func ipmiClientFromFlags(host, user, pass string) *ipmi.Client {
	return &ipmi.Client{
		Host:     host,
		Username: user,
		Password: pass,
	}
}

// newIPMIStatusCmd shows the local BMC network config and power state.
func newIPMIStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local BMC network config and power status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := ipmiClientFromFlags("", "", "")

			cfg, err := c.GetBMCConfig(ctx)
			if err != nil {
				return fmt.Errorf("get bmc config: %w", err)
			}

			fmt.Printf("BMC Network (channel %d):\n", cfg.Channel)
			fmt.Printf("  IP Address : %s\n", cfg.IPAddress)
			fmt.Printf("  Netmask    : %s\n", cfg.Netmask)
			fmt.Printf("  Gateway    : %s\n", cfg.Gateway)
			fmt.Printf("  IP Source  : %s\n", cfg.IPSource)

			users, err := c.GetBMCUsers(ctx)
			if err == nil && len(users) > 0 {
				fmt.Printf("\nBMC Users:\n")
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "  ID\tUSERNAME\tACCESS")
				for _, u := range users {
					fmt.Fprintf(w, "  %d\t%s\t%s\n", u.ID, u.Username, u.Access)
				}
				_ = w.Flush()
			}
			return nil
		},
	}
}

// newIPMIPowerCmd controls power on a remote node via its BMC.
func newIPMIPowerCmd() *cobra.Command {
	var flagHost, flagUser, flagPass string

	cmd := &cobra.Command{
		Use:   "power [on|off|cycle|reset]",
		Short: "Control power on a node via IPMI",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			action := strings.ToLower(args[0])
			ctx := context.Background()
			c := ipmiClientFromFlags(flagHost, flagUser, flagPass)

			switch action {
			case "on":
				if err := c.PowerOn(ctx); err != nil {
					return err
				}
				fmt.Println("Power on command sent.")
			case "off":
				if err := c.PowerOff(ctx); err != nil {
					return err
				}
				fmt.Println("Power off command sent.")
			case "cycle":
				if err := c.PowerCycle(ctx); err != nil {
					return err
				}
				fmt.Println("Power cycle command sent.")
			case "reset":
				if err := c.PowerReset(ctx); err != nil {
					return err
				}
				fmt.Println("Power reset command sent.")
			case "status":
				status, err := c.PowerStatus(ctx)
				if err != nil {
					return err
				}
				fmt.Printf("Power: %s\n", status)
			default:
				return fmt.Errorf("unknown power action %q — use on, off, cycle, reset, or status", action)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&flagHost, "host", "", "BMC IP address (required for remote)")
	cmd.Flags().StringVar(&flagUser, "user", "", "BMC username")
	cmd.Flags().StringVar(&flagPass, "pass", "", "BMC password")
	return cmd
}

// newIPMIConfigureCmd configures the local BMC network interface.
func newIPMIConfigureCmd() *cobra.Command {
	var flagIP, flagNetmask, flagGateway string

	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Configure local BMC network (static IP)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagIP == "" {
				return fmt.Errorf("--ip is required")
			}
			if flagNetmask == "" {
				return fmt.Errorf("--netmask is required")
			}
			if flagGateway == "" {
				return fmt.Errorf("--gateway is required")
			}

			ctx := context.Background()
			c := ipmiClientFromFlags("", "", "")

			cfg := ipmi.BMCConfig{
				Channel:   1,
				IPAddress: flagIP,
				Netmask:   flagNetmask,
				Gateway:   flagGateway,
				IPSource:  "static",
			}
			if err := c.SetBMCNetwork(ctx, cfg); err != nil {
				return fmt.Errorf("configure bmc: %w", err)
			}
			fmt.Printf("BMC network configured:\n")
			fmt.Printf("  IP      : %s\n", flagIP)
			fmt.Printf("  Netmask : %s\n", flagNetmask)
			fmt.Printf("  Gateway : %s\n", flagGateway)
			return nil
		},
	}

	cmd.Flags().StringVar(&flagIP, "ip", "", "Static IP address for the BMC (required)")
	cmd.Flags().StringVar(&flagNetmask, "netmask", "", "Subnet mask (required)")
	cmd.Flags().StringVar(&flagGateway, "gateway", "", "Default gateway (required)")
	return cmd
}

// newIPMIPXECmd sets next boot to PXE and power cycles the target node.
func newIPMIPXECmd() *cobra.Command {
	var flagHost, flagUser, flagPass string

	cmd := &cobra.Command{
		Use:   "pxe",
		Short: "Set next boot to PXE and power cycle the node",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagHost == "" {
				return fmt.Errorf("--host is required")
			}

			ctx := context.Background()
			c := ipmiClientFromFlags(flagHost, flagUser, flagPass)

			fmt.Fprintf(os.Stderr, "Setting next boot to PXE on %s...\n", flagHost)
			if err := c.SetBootDevWithOpts(ctx, ipmi.BootDevPXE, ipmi.BootOpts{Persistent: true}); err != nil {
				return fmt.Errorf("set boot pxe: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Power cycling...\n")
			if err := c.PowerCycle(ctx); err != nil {
				return fmt.Errorf("power cycle: %w", err)
			}

			fmt.Printf("Node %s will boot via PXE.\n", flagHost)
			return nil
		},
	}

	cmd.Flags().StringVar(&flagHost, "host", "", "BMC IP address (required)")
	cmd.Flags().StringVar(&flagUser, "user", "", "BMC username")
	cmd.Flags().StringVar(&flagPass, "pass", "", "BMC password")
	return cmd
}

// newIPMISensorsCmd displays sensor readings from a remote BMC.
func newIPMISensorsCmd() *cobra.Command {
	var flagHost, flagUser, flagPass string

	cmd := &cobra.Command{
		Use:   "sensors",
		Short: "Show IPMI sensor readings",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := ipmiClientFromFlags(flagHost, flagUser, flagPass)

			sensors, err := c.GetSensorData(ctx)
			if err != nil {
				return fmt.Errorf("get sensors: %w", err)
			}

			if len(sensors) == 0 {
				fmt.Println("No sensor data available.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SENSOR\tVALUE\tUNITS\tSTATUS")
			for _, s := range sensors {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Value, s.Units, s.Status)
			}
			return w.Flush()
		},
	}

	cmd.Flags().StringVar(&flagHost, "host", "", "BMC IP address (local BMC if omitted)")
	cmd.Flags().StringVar(&flagUser, "user", "", "BMC username")
	cmd.Flags().StringVar(&flagPass, "pass", "", "BMC password")
	return cmd
}

// newIPMITestBootFlipDirectCmd tests the boot-flip code path directly against a
// real BMC using raw credentials (bypasses the server API). Use this to verify
// ipmitool connectivity and boot-order control before configuring a node in the UI.
func newIPMITestBootFlipDirectCmd() *cobra.Command {
	var flagHost, flagUser, flagPass string
	var flagDevice string
	var flagCycle bool

	cmd := &cobra.Command{
		Use:   "test-boot-flip",
		Short: "Test IPMI boot-device flip directly against a BMC",
		Long: `test-boot-flip creates an IPMI provider from raw credentials and calls
SetNextBoot to the given device (pxe or disk), then optionally power-cycles.
Use this to verify BMC connectivity before registering a node.

Example:
  clustr ipmi test-boot-flip --host 10.0.0.5 --user admin --pass secret --device pxe --cycle`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagHost == "" {
				return fmt.Errorf("--host is required")
			}

			reg := power.NewRegistry()
			poweripm.Register(reg)

			prov, err := reg.Create(power.ProviderConfig{
				Type: "ipmi",
				Fields: map[string]string{
					"host":     flagHost,
					"username": flagUser,
					"password": flagPass,
				},
			})
			if err != nil {
				return fmt.Errorf("create ipmi provider: %w", err)
			}

			var dev power.BootDevice
			switch flagDevice {
			case "pxe":
				dev = power.BootPXE
			case "disk":
				dev = power.BootDisk
			default:
				return fmt.Errorf("--device must be 'pxe' or 'disk', got %q", flagDevice)
			}

			ctx := context.Background()

			fmt.Fprintf(os.Stderr, "Setting next boot to %s on %s via %s...\n", dev, flagHost, prov.Name())
			if err := prov.SetNextBoot(ctx, dev); err != nil {
				return fmt.Errorf("SetNextBoot: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Next boot set to %s.\n", dev)

			if flagCycle {
				fmt.Fprintf(os.Stderr, "Power cycling %s...\n", flagHost)
				if err := prov.PowerCycle(ctx); err != nil {
					return fmt.Errorf("PowerCycle: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Power cycle sent.\n")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&flagHost, "host", "", "BMC IP address (required)")
	cmd.Flags().StringVar(&flagUser, "user", "", "BMC username")
	cmd.Flags().StringVar(&flagPass, "pass", "", "BMC password")
	cmd.Flags().StringVar(&flagDevice, "device", "pxe", "Boot device: pxe or disk")
	cmd.Flags().BoolVar(&flagCycle, "cycle", false, "Power cycle after setting next boot")
	return cmd
}

// ─── logs ────────────────────────────────────────────────────────────────────

// newLogsCmd creates the "clustr logs" command and its subcommands.
func newLogsCmd() *cobra.Command {
	var (
		flagMAC       string
		flagHostname  string
		flagLevel     string
		flagComponent string
		flagSince     string
		flagLimit     int
		flagFollow    bool
	)

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "View deployment logs from the server",
		Long: `logs queries or tails the centralized deployment log stream.

Examples:
  clustr logs --mac aa:bb:cc:dd:ee:ff        # history for a specific node
  clustr logs --follow                        # live tail all nodes
  clustr logs --follow --mac aa:bb:cc:dd:ee:ff --level error
  clustr logs --component deploy --since 1h  # last hour of deploy phase logs`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()

			filter := api.LogFilter{
				NodeMAC:   flagMAC,
				Hostname:  flagHostname,
				Level:     flagLevel,
				Component: flagComponent,
				Limit:     flagLimit,
			}

			// Parse --since as a duration ("1h", "30m") or RFC3339 timestamp.
			if flagSince != "" {
				if d, err := time.ParseDuration(flagSince); err == nil {
					t := time.Now().UTC().Add(-d)
					filter.Since = &t
				} else if t, err := time.Parse(time.RFC3339, flagSince); err == nil {
					filter.Since = &t
				} else {
					return fmt.Errorf("--since: expected a duration (e.g. 1h, 30m) or RFC3339 timestamp")
				}
			}

			if flagFollow {
				return tailLogs(ctx, c, filter)
			}
			return queryLogs(ctx, c, filter)
		},
	}

	cmd.Flags().StringVar(&flagMAC, "mac", "", "Filter by node MAC address")
	cmd.Flags().StringVar(&flagHostname, "hostname", "", "Filter by hostname")
	cmd.Flags().StringVar(&flagLevel, "level", "", "Filter by log level (debug, info, warn, error)")
	cmd.Flags().StringVar(&flagComponent, "component", "", "Filter by component (hardware, deploy, chroot, ipmi, efiboot)")
	cmd.Flags().StringVar(&flagSince, "since", "", "Show logs since a duration ago (e.g. 1h, 30m) or RFC3339 timestamp")
	cmd.Flags().IntVar(&flagLimit, "limit", 100, "Max number of log entries to return")
	cmd.Flags().BoolVar(&flagFollow, "follow", false, "Tail the live log stream (SSE)")

	return cmd
}

// queryLogs fetches and prints historical logs.
func queryLogs(ctx context.Context, c *client.Client, filter api.LogFilter) error {
	entries, err := c.QueryLogs(ctx, filter)
	if err != nil {
		return fmt.Errorf("query logs: %w", err)
	}
	if len(entries) == 0 {
		fmt.Println("No log entries found.")
		return nil
	}
	// Entries come back newest-first; reverse for chronological output.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	for _, e := range entries {
		printLogEntry(e)
	}
	return nil
}

// tailLogs opens an SSE stream and prints entries as they arrive.
func tailLogs(ctx context.Context, c *client.Client, filter api.LogFilter) error {
	ch, cancel, err := c.StreamLogs(ctx, filter)
	if err != nil {
		return fmt.Errorf("stream logs: %w", err)
	}
	defer cancel()

	fmt.Fprintln(os.Stderr, "Streaming live logs (Ctrl-C to stop)...")
	for entry := range ch {
		printLogEntry(entry)
	}
	return nil
}

// printLogEntry writes a formatted log line to stdout.
func printLogEntry(e api.LogEntry) {
	ts := e.Timestamp.Local().Format("2006-01-02 15:04:05")

	levelStr := levelColored(e.Level)
	node := e.Hostname
	if node == "" {
		node = e.NodeMAC
	}

	fmt.Printf("%s  %s  [%s] %s%s%s  %s\n",
		colorGray+ts+colorReset,
		levelStr,
		e.Component,
		colorGray+node+colorReset,
		sep(node),
		colorReset,
		e.Message,
	)
}

func sep(s string) string {
	if s == "" {
		return ""
	}
	return "  "
}

func levelColored(level string) string {
	switch strings.ToLower(level) {
	case "error":
		return colorRed + "ERR" + colorReset
	case "warn":
		return colorYellow + "WRN" + colorReset
	case "debug":
		return colorGray + "DBG" + colorReset
	default:
		return colorCyan + "INF" + colorReset
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// shortID returns the first 8 characters of a UUID for compact display.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// httpToWS converts an HTTP(S) server URL to the equivalent WebSocket URL
// by replacing the scheme: http:// → ws://, https:// → wss://.
// Returns the input unchanged when the scheme is already ws/wss or unrecognised.
func httpToWS(serverURL string) string {
	switch {
	case len(serverURL) >= 8 && serverURL[:8] == "https://":
		return "wss://" + serverURL[8:]
	case len(serverURL) >= 7 && serverURL[:7] == "http://":
		return "ws://" + serverURL[7:]
	default:
		return serverURL
	}
}

// humanBytes formats a byte count as a human-readable string.
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// primaryMACFromHW returns the MAC address of the first non-loopback physical NIC.
func primaryMACFromHW(hw *hardware.SystemInfo) string {
	for _, nic := range hw.NICs {
		if nic.Name == "lo" || nic.MAC == "" || nic.MAC == "00:00:00:00:00:00" {
			continue
		}
		return nic.MAC
	}
	return ""
}

// ─── image import-iso ────────────────────────────────────────────────────────

// newImageImportISOCmd creates "clustr image import-iso <path>".
// It passes the absolute ISO path to the server via POST /api/v1/factory/import-path.
// This requires the CLI and server share a filesystem (same host or NFS mount).
func newImageImportISOCmd() *cobra.Command {
	var (
		flagName    string
		flagVersion string
	)

	cmd := &cobra.Command{
		Use:   "import-iso <path>",
		Short: "Import an ISO image into the server's image store",
		Long: `import-iso passes a server-local ISO path to clustr-serverd, which mounts
the ISO, extracts the root filesystem, and creates a new BaseImage.

The ISO file must be accessible from the server process (same host or shared
mount). The command returns immediately; poll with "clustr image details <id>".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			isoPath := args[0]
			if flagName == "" {
				base := filepath.Base(isoPath)
				flagName = strings.TrimSuffix(base, filepath.Ext(base))
			}

			absPath, err := filepath.Abs(isoPath)
			if err != nil {
				return fmt.Errorf("resolve path: %w", err)
			}

			ctx := context.Background()
			c := clientFromFlags()

			fmt.Fprintf(os.Stderr, "Importing ISO %s as %q...\n", absPath, flagName)
			img, err := c.ImportISOPath(ctx, absPath, flagName, flagVersion)
			if err != nil {
				return fmt.Errorf("import iso: %w", err)
			}

			fmt.Printf("ISO import initiated:\n")
			fmt.Printf("  ID:     %s\n", img.ID)
			fmt.Printf("  Name:   %s\n", img.Name)
			fmt.Printf("  Status: %s\n", img.Status)
			fmt.Printf("\nPoll status with: clustr image details %s\n", img.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&flagName, "name", "", "Image name (default: ISO filename without extension)")
	cmd.Flags().StringVar(&flagVersion, "version", "1.0.0", "Image version")
	return cmd
}

// ─── shell ───────────────────────────────────────────────────────────────────

// newShellCmd creates "clustr shell <image-id>".
//
// Flow (local path — CLI on same host as server):
//  1. Verify image is ready/building.
//  2. Open a server-side session (triggers vfs mounts on the server).
//  3. Create a local chroot.Session against the returned rootfs path.
//  4. Drop into an interactive shell (stdin/stdout/stderr attached).
//  5. Close the server-side session on exit (unmounts vfs).
func newShellCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell <image-id>",
		Short: "Open an interactive chroot shell inside an image",
		Long: `shell drops you into an interactive bash shell inside the specified image's
root filesystem. The image must have status "ready" or "building".

The chroot mounts /proc, /sys, /dev, /dev/pts, and /run before dropping you
into the shell. All mounts are cleaned up on exit.

NOTE: Requires root privileges and that the CLI runs on the same host as
clustr-serverd (rootfs is accessed directly via local filesystem path).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			imageID := args[0]
			ctx := context.Background()
			c := clientFromFlags()

			img, err := c.GetImage(ctx, imageID)
			if err != nil {
				return fmt.Errorf("get image: %w", err)
			}
			if img.Status != api.ImageStatusReady && img.Status != api.ImageStatusBuilding {
				return fmt.Errorf("image %s has status %q — must be ready or building", img.ID, img.Status)
			}
			fmt.Fprintf(os.Stderr, "Opening shell in image: %s %s (%s)\n", img.Name, img.Version, img.ID)

			// Open a server-side session to trigger vfs mounts.
			sess, err := c.OpenShellSession(ctx, imageID)
			if err != nil {
				return fmt.Errorf("open shell session: %w", err)
			}
			defer func() {
				if closeErr := c.CloseShellSession(context.Background(), imageID, sess.SessionID); closeErr != nil {
					fmt.Fprintf(os.Stderr, "warning: close session: %v\n", closeErr)
				}
			}()

			// Create a local chroot.Session using the server's rootfs path.
			// Skip Enter() — the server-side session owns the mounts.
			localSess, err := chroot.NewSession(sess.RootDir)
			if err != nil {
				return fmt.Errorf("create local chroot: %w", err)
			}
			defer func() { _ = localSess.Close() }()

			fmt.Fprintf(os.Stderr, "Entering chroot at %s\n", sess.RootDir)
			fmt.Fprintf(os.Stderr, "Type 'exit' to leave the chroot.\n")

			if err := localSess.Shell(); err != nil {
				fmt.Fprintf(os.Stderr, "shell exited: %v\n", err)
			}
			return nil
		},
	}
	return cmd
}

// ─── image capture ───────────────────────────────────────────────────────────

// newImageCaptureCmd creates "clustr image capture".
// It instructs clustr-serverd to SSH into the target host and rsync its
// filesystem into a new BaseImage. The server must have network access
// to the source host, and either a private key or password must be provided.
// SSH host key verification is disabled (StrictHostKeyChecking=no) — only
// use this on trusted golden nodes on a management network.
func newImageCaptureCmd() *cobra.Command {
	var (
		flagFrom        string
		flagSSHKey      string
		flagSSHPassword string
		flagSSHPort     int
		flagName        string
		flagVersion     string
		flagOS          string
		flagArch        string
		flagExclude     []string
		flagNotes       string
	)

	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Capture a live server as a base image via SSH rsync",
		Long: `capture instructs clustr-serverd to SSH into the source host and stream
its filesystem into a new BaseImage via rsync. The server must be able to reach
the source host over the network.

SSH host key verification is disabled by default (StrictHostKeyChecking=no).
Only use this against trusted golden nodes on a management network.

If --ssh-key is omitted, the server's default SSH key (~/.ssh/id_rsa or the
key configured by the server's user environment) is used. Provide --ssh-password
only when key-based auth is unavailable; sshpass must be installed on the server.

The capture runs asynchronously. Poll with: clustr image details <id>

Examples:
  clustr image capture --from root@192.168.1.10 --name rocky9-golden --version 1.0.0
  clustr image capture --from 10.0.0.5 --ssh-key /etc/clustr/keys/golden --name hpc-compute`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagFrom == "" {
				return fmt.Errorf("--from is required (e.g. root@192.168.1.10)")
			}
			if flagName == "" {
				return fmt.Errorf("--name is required")
			}

			ctx := context.Background()
			c := clientFromFlags()

			req := api.CaptureRequest{
				SourceHost:   flagFrom,
				SSHKeyPath:   flagSSHKey,
				SSHPassword:  flagSSHPassword,
				SSHPort:      flagSSHPort,
				Name:         flagName,
				Version:      flagVersion,
				OS:           flagOS,
				Arch:         flagArch,
				ExcludePaths: flagExclude,
				Notes:        flagNotes,
				Tags:         []string{},
			}

			fmt.Fprintf(os.Stderr, "Requesting capture of %s from %s...\n", flagName, flagFrom)
			img, err := c.CaptureImage(ctx, req)
			if err != nil {
				return fmt.Errorf("capture: %w", err)
			}

			fmt.Printf("Capture initiated:\n")
			fmt.Printf("  ID:     %s\n", img.ID)
			fmt.Printf("  Name:   %s\n", img.Name)
			fmt.Printf("  Status: %s\n", img.Status)
			fmt.Printf("\nThe server is now rsyncing from %s — this may take several minutes.\n", flagFrom)
			fmt.Printf("Poll status with: clustr image details %s\n", img.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&flagFrom, "from", "", "Source host in user@host or host form (required)")
	cmd.Flags().StringVar(&flagSSHKey, "ssh-key", "", "Server-local path to SSH private key (uses server default key if omitted)")
	cmd.Flags().StringVar(&flagSSHPassword, "ssh-password", "", "SSH password (requires sshpass on server host; prefer key auth)")
	cmd.Flags().IntVar(&flagSSHPort, "ssh-port", 22, "SSH port on the source host")
	cmd.Flags().StringVar(&flagName, "name", "", "Image name (required)")
	cmd.Flags().StringVar(&flagVersion, "version", "1.0.0", "Image version")
	cmd.Flags().StringVar(&flagOS, "os", "", "OS name, e.g. 'Rocky Linux 9'")
	cmd.Flags().StringVar(&flagArch, "arch", "x86_64", "Target architecture")
	cmd.Flags().StringSliceVar(&flagExclude, "exclude", nil, "Additional rsync --exclude paths (repeatable)")
	cmd.Flags().StringVar(&flagNotes, "notes", "", "Free-text notes")

	return cmd
}

// ─── admin keys ──────────────────────────────────────────────────────────────

func newAdminKeysListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all non-revoked API keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()
			keys, err := c.ListAPIKeys(ctx)
			if err != nil {
				return fmt.Errorf("list api keys: %w", err)
			}
			if len(keys) == 0 {
				fmt.Println("No API keys found.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSCOPE\tLABEL\tCREATED_BY\tHASH_PREFIX\tCREATED_AT\tLAST_USED\tEXPIRES")
			for _, k := range keys {
				lastUsed := "—"
				if k.LastUsedAt != nil {
					lastUsed = *k.LastUsedAt
				}
				expires := "never"
				if k.ExpiresAt != nil {
					expires = *k.ExpiresAt
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s…\t%s\t%s\t%s\n",
					k.ID,
					k.Scope,
					k.Label,
					k.CreatedBy,
					k.HashPrefix,
					k.CreatedAt,
					lastUsed,
					expires,
				)
			}
			return w.Flush()
		},
	}
}

func newAdminKeysCreateCmd() *cobra.Command {
	var (
		flagScope   string
		flagLabel   string
		flagExpires string
		flagNodeID  string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API key",
		Long:  "Create a new API key. The raw key is shown once and cannot be retrieved later.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagScope != "admin" && flagScope != "node" {
				return fmt.Errorf("--scope must be 'admin' or 'node'")
			}
			if flagScope == "node" && flagNodeID == "" {
				return fmt.Errorf("--node-id is required when scope is 'node'")
			}

			var expiresAt string
			if flagExpires != "" {
				d, err := time.ParseDuration(flagExpires)
				if err != nil {
					return fmt.Errorf("--expires must be a Go duration (e.g. 24h, 720h): %w", err)
				}
				expiresAt = time.Now().Add(d).UTC().Format(time.RFC3339)
			}

			ctx := context.Background()
			c := clientFromFlags()
			resp, err := c.CreateAPIKeyRemote(ctx, client.CreateKeyRequest{
				Scope:     flagScope,
				Label:     flagLabel,
				NodeID:    flagNodeID,
				ExpiresAt: expiresAt,
			})
			if err != nil {
				return fmt.Errorf("create api key: %w", err)
			}

			fmt.Printf("\n"+
				"╔═══════════════════════════════════════════════════════════════════╗\n"+
				"║  NEW API KEY — Save this. It will NOT be shown again.            ║\n"+
				"╠═══════════════════════════════════════════════════════════════════╣\n"+
				"║  %s\n"+
				"╚═══════════════════════════════════════════════════════════════════╝\n\n",
				resp.Key,
			)
			fmt.Printf("  ID:    %s\n", resp.APIKey.ID)
			fmt.Printf("  Scope: %s\n", resp.APIKey.Scope)
			if resp.APIKey.Label != "" {
				fmt.Printf("  Label: %s\n", resp.APIKey.Label)
			}
			if resp.APIKey.ExpiresAt != nil {
				fmt.Printf("  Expires: %s\n", *resp.APIKey.ExpiresAt)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&flagScope, "scope", "admin", "Key scope: admin or node")
	cmd.Flags().StringVar(&flagLabel, "label", "", "Human-readable label (e.g. 'ci-runner', 'robert-laptop')")
	cmd.Flags().StringVar(&flagExpires, "expires", "", "Optional TTL duration (e.g. 24h, 720h)")
	cmd.Flags().StringVar(&flagNodeID, "node-id", "", "Node ID to bind (required when scope=node)")
	return cmd
}

func newAdminKeysRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate <id>",
		Short: "Rotate an API key (revoke old, mint new with same label/scope)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()
			resp, err := c.RotateAPIKey(ctx, args[0])
			if err != nil {
				return fmt.Errorf("rotate api key: %w", err)
			}

			fmt.Printf("\n"+
				"╔═══════════════════════════════════════════════════════════════════╗\n"+
				"║  ROTATED API KEY — Save this. It will NOT be shown again.        ║\n"+
				"╠═══════════════════════════════════════════════════════════════════╣\n"+
				"║  %s\n"+
				"╚═══════════════════════════════════════════════════════════════════╝\n\n",
				resp.Key,
			)
			fmt.Printf("  New ID: %s\n", resp.APIKey.ID)
			fmt.Printf("  Scope:  %s\n", resp.APIKey.Scope)
			fmt.Printf("  Label:  %s\n", resp.APIKey.Label)
			fmt.Println("\nThe old key has been revoked and will no longer authenticate.")
			return nil
		},
	}
}

func newAdminKeysRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke an API key (soft delete)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			c := clientFromFlags()
			if err := c.RevokeAPIKey(ctx, args[0]); err != nil {
				return fmt.Errorf("revoke api key: %w", err)
			}
			fmt.Printf("API key %s has been revoked.\n", args[0])
			return nil
		},
	}
}
