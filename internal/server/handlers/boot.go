package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/bootassets"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/pxe"
	"github.com/sqoia-dev/clustr/pkg/api"
)


// BootHandler serves boot assets and dynamic iPXE scripts over HTTP.
// Boot files (vmlinuz, initramfs.img) are served from BootDir.
// iPXE chainload files (ipxe.efi, undionly.kpxe) are served from TFTPDir.
//
// Post-deploy UEFI boot uses `exit` — firmware advances to scsi0 and UEFI
// removable-media auto-discovery loads \EFI\BOOT\BOOTX64.EFI from the ESP
// (written by grub2-install --removable --no-nvram during finalize). No custom
// NVRAM OS entry is created or managed by clustr.
// See docs/boot-architecture.md §8 for the full architectural decision record.
type BootHandler struct {
	// BootDir is the directory containing vmlinuz and initramfs.img.
	BootDir string
	// TFTPDir is the directory containing ipxe.efi and undionly.kpxe.
	TFTPDir string
	// ServerURL is the public URL of clustr-serverd (e.g. http://10.99.0.1:8080).
	// Used to generate the iPXE boot script.
	ServerURL string
	// DB is used to look up node state by MAC for PXE boot routing.
	// When nil the handler always returns the full boot script (safe default).
	DB *db.DB
	// Version is the clustr server version string displayed in boot menus.
	Version string
	// MintNodeToken is called to generate a fresh node-scoped API key at PXE-serve
	// time. The returned raw key is embedded in the kernel cmdline as clustr.token.
	// When nil (e.g. in tests that don't need auth), an empty token is used.
	MintNodeToken func(nodeID string) (rawKey string, err error)
}

// ServeRescueInitramfs handles GET /api/v1/boot/rescue.cpio.gz.
// Serves the rescue initramfs from BootDir when present.
func (h *BootHandler) ServeRescueInitramfs(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, filepath.Join(h.BootDir, "rescue.cpio.gz"), "application/octet-stream")
}

// ServeMemtest handles GET /api/v1/boot/extra/memtest.
// Serves the memtest86+ binary from BootDir/extra/memtest when present.
func (h *BootHandler) ServeMemtest(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, filepath.Join(h.BootDir, "extra", "memtest"), "application/octet-stream")
}

// ServeIPXEScript handles GET /api/v1/boot/ipxe.
//
// This is the PXE server's boot routing decision point. The DHCP handler sets
// the iPXE boot filename URL to:
//
//	http://<server>/api/v1/boot/ipxe?mac=${mac}
//
// iPXE expands ${mac} before fetching, so this handler receives the actual
// MAC address. It resolves the node state and returns one of:
//
//   - NodeStateDeployed, NodeStateDeployedVerified: sanboot from local disk --
//     the node is confirmed healthy; boot from disk unconditionally.
//
//   - NodeStateDeployedLDAPFailed: sanboot from local disk -- the OS booted
//     and phoned home, so the disk image is bootable, but the LDAP client
//     (sssd) is not ready. Auto-reimaging would discard a potentially
//     trivially-fixable state (e.g. transient slapd outage, sssd cache flush
//     needed). The operator must triage and decide whether to repair LDAP or
//     trigger a reimage explicitly. v0.1.15.
//
//   - NodeStateDeployedPreboot: sanboot from local disk -- deploy-complete was
//     received from initramfs but the OS has not yet phoned home via
//     POST /verify-boot. We MUST disk-boot here so clustr-verify-boot.service
//     can run and advance state to deployed_verified. Re-deploying in this
//     state would cause an infinite deploy loop. If DeployVerifyTimeoutAt is
//     set (scanner stamped a deadline miss) we still disk-boot and log a
//     warning; the operator must manually reimage if the OS is broken.
//
//   - NodeStateDeployVerifyTimeout: sanboot from local disk with a warning --
//     the background scanner determined the OS never phoned home in time.
//     Operator intervention (manual reimage) may be required.
//
//   - All other states (Registered, Configured, ReimagePending, Failed, or
//     unknown MAC): the full clustr initramfs boot script, which causes the
//     node to run `clustr deploy --auto` and deploy or wait for assignment.
//
// For non-deployed nodes a fresh node-scoped API key is minted and embedded in
// the kernel cmdline as clustr.token=<key> so the deploy agent can authenticate
// against /images/{id} and /images/{id}/blob without an admin key.
//
// This is the canonical pattern used by xCAT, Warewulf, and Cobbler: the PXE
// server is the source of truth for what each node boots. No BMC SetNextBoot
// calls are needed for normal boot routing. PXE must be first in the BIOS boot
// order, set once during rack/stack and never changed.
func (h *BootHandler) ServeIPXEScript(w http.ResponseWriter, r *http.Request) {
	mac := strings.ToLower(r.URL.Query().Get("mac"))
	forceReimage := r.URL.Query().Get("force_reimage") == "1"
	// multicast=1 is set by the "reimage-fleet" iPXE menu item. When present,
	// we embed clustr.multicast=1 + clustr.session_poll_url into the kernel cmdline
	// so the deploy agent can enqueue itself in a multicast session.
	wantMulticast := r.URL.Query().Get("multicast") == "1"

	// If we have a MAC and a DB, look up the node state and route the boot.
	if mac != "" && h.DB != nil {
		// Handle force_reimage=1: mark the node reimage_pending so the next routing
		// decision below (after the state lookup) will serve the deploy initramfs.
		// This is triggered from the boot menu "Request reimage" option.
		if forceReimage {
			existing, lookupErr := h.DB.GetNodeConfigByMAC(r.Context(), mac)
			if lookupErr == nil && existing.BaseImageID != "" {
				if setErr := h.DB.SetReimagePending(r.Context(), existing.ID, true); setErr != nil {
					log.Error().Err(setErr).Str("mac", mac).Msg("boot: force_reimage: SetReimagePending failed")
				} else {
					log.Info().Str("mac", mac).Str("hostname", existing.Hostname).
						Msg("boot: force_reimage=1 received from boot menu — node marked reimage_pending")
				}
			} else if lookupErr != nil {
				log.Warn().Err(lookupErr).Str("mac", mac).Msg("boot: force_reimage: node lookup failed (ignored)")
			}
		}

		nodeCfg, err := h.DB.GetNodeConfigByMAC(r.Context(), mac)
		if err != nil && !errors.Is(err, api.ErrNotFound) {
			// DB error: log and fall through to the safe default (full boot script).
			// A transient DB error must never cause a node to boot from disk when
			// it should be reimaged -- fail open toward clustr deploy, not disk boot.
			log.Error().Err(err).Str("mac", mac).Msg("boot: lookup node by MAC")
		} else if err == nil {
			state := nodeCfg.State()
			log.Info().
				Str("mac", mac).
				Str("hostname", nodeCfg.Hostname).
				Str("state", string(state)).
				Msg("boot: PXE routing decision")

			switch state {
			case api.NodeStateDeployed, api.NodeStateDeployedVerified:
				// Terminal success states -- node is confirmed healthy. Boot from disk.
				log.Info().Str("mac", mac).Str("hostname", nodeCfg.Hostname).Str("state", string(state)).Msg("boot: disk-boot (verified deployed)")
				script, genErr := h.generateDiskBootScript(r, &nodeCfg)
				if genErr != nil {
					log.Error().Err(genErr).Str("mac", mac).Msg("boot: generate disk boot script")
					http.Error(w, "failed to generate boot script", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(script)
				return

			case api.NodeStateDeployedLDAPFailed:
				// v0.1.15: the OS phoned home post-boot (so the disk image is
				// bootable) but sssd is not connected to slapd. The node is in
				// a degraded-but-recoverable state — the OS itself is fine,
				// LDAP integration is broken. If this node ever PXE-boots
				// again (manual reboot, persistent netboot config, IPMI
				// bootdev pxe set during triage) we MUST disk-boot it and let
				// the operator decide whether to retry LDAP repair or trigger
				// a reimage explicitly. Auto-reimaging here would discard a
				// potentially trivially-fixable state (transient slapd outage,
				// sssd cache flush, missing nss_sss config push) on every PXE
				// cycle, which is exactly the silent-data-loss class of bug
				// the deployed_ldap_failed state was introduced to surface.
				log.Warn().
					Str("mac", mac).
					Str("hostname", nodeCfg.Hostname).
					Str("state", string(state)).
					Msg("boot: disk-boot (deployed_ldap_failed -- LDAP broken but OS bootable; operator must triage)")
				script, genErr := h.generateDiskBootScript(r, &nodeCfg)
				if genErr != nil {
					log.Error().Err(genErr).Str("mac", mac).Msg("boot: generate disk boot script")
					http.Error(w, "failed to generate boot script", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(script)
				return

			case api.NodeStateDeployedPreboot:
				// ADR-0008: deploy-complete callback received from initramfs, but the OS
				// has not yet phoned home via POST /verify-boot. Boot from disk so the
				// deployed OS gets a chance to run clustr-verify-boot.service and phone
				// home. Do NOT fall through to re-deploy -- that would cause an infinite
				// loop: re-deploy -> deployed_preboot -> PXE boot -> re-deploy...
				//
				// If DeployVerifyTimeoutAt is set the background scanner already
				// determined that the OS never phoned home within the deadline. We still
				// disk-boot (giving the OS one more try) and log a warning. The operator
				// must intervene (mark the node failed / trigger a reimage) if the OS
				// genuinely cannot boot. Auto-re-deploy in this state is never safe.
				if nodeCfg.DeployVerifyTimeoutAt != nil {
					log.Warn().
						Str("mac", mac).
						Str("hostname", nodeCfg.Hostname).
						Time("deploy_verify_timeout_at", *nodeCfg.DeployVerifyTimeoutAt).
						Msg("boot: PXE from deployed_preboot node past verify-deadline -- still disk-booting; manual intervention may be required")
				} else {
					log.Info().Str("mac", mac).Str("hostname", nodeCfg.Hostname).Msg("boot: disk-boot (deployed_preboot, awaiting verify-boot phone-home)")
				}
				script, genErr := h.generateDiskBootScript(r, &nodeCfg)
				if genErr != nil {
					log.Error().Err(genErr).Str("mac", mac).Msg("boot: generate disk boot script")
					http.Error(w, "failed to generate boot script", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(script)
				return

			case api.NodeStateDeployVerifyTimeout:
				// Background scanner stamped a timeout: OS never phoned home within the
				// deadline after deploy_completed_preboot_at. Boot from disk and log a
				// prominent warning. Operator must manually re-image if the OS is broken.
				log.Warn().
					Str("mac", mac).
					Str("hostname", nodeCfg.Hostname).
					Time("deploy_verify_timeout_at", *nodeCfg.DeployVerifyTimeoutAt).
					Msg("boot: PXE from deploy_verify_timeout node -- disk-booting; OS failed to phone home; manual reimage may be required")
				script, genErr := h.generateDiskBootScript(r, &nodeCfg)
				if genErr != nil {
					log.Error().Err(genErr).Str("mac", mac).Msg("boot: generate disk boot script")
					http.Error(w, "failed to generate boot script", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(script)
				return
			}

			// Non-deployed node: guard against reimage_pending with no image assigned.
			// A node in reimage_pending without a base_image_id will PXE boot,
			// attempt to fetch an image in the deploy agent, and get 403 from
			// requireImageAccess. Serve a wait/retry script instead so the node
			// loops in iPXE until the operator assigns an image, preventing a
			// flood of failing deploy attempts.
			// Nodes in registered/configured state without an image (no ReimagePending)
			// are intentionally allowed through — the initramfs deploy agent handles
			// the no-image case gracefully for freshly-registered nodes.
			if state == api.NodeStateReimagePending && nodeCfg.BaseImageID == "" {
				log.Warn().
					Str("mac", mac).
					Str("hostname", nodeCfg.Hostname).
					Str("node_id", nodeCfg.ID).
					Msg("boot: node has reimage_pending but no image assigned — serving wait script")
				script, genErr := pxe.GenerateWaitRetryScript(nodeCfg.Hostname)
				if genErr != nil {
					log.Error().Err(genErr).Str("mac", mac).Msg("boot: generate wait-retry script")
					http.Error(w, "failed to generate boot script", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(script)
				return
			}

			// Mint a fresh node-scoped token for this deploy run.
			token := h.mintToken(r, nodeCfg.ID)
			mcastParams := h.multicastBootParams(r, mac, nodeCfg.ID, wantMulticast)
			script, sshPass, genErr := pxe.GenerateBootScript(h.ServerURL, "clustr-node-"+token, mcastParams)
			if genErr != nil {
				log.Error().Err(genErr).Str("mac", mac).Msg("boot: generate boot script")
				http.Error(w, "failed to generate boot script", http.StatusInternalServerError)
				return
			}
			log.Info().Str("mac", mac).Str("node_id", nodeCfg.ID).Str("ssh_pass", sshPass).
				Bool("multicast", wantMulticast).
				Msg("boot: deploy boot script served — SSH debug password for this boot")
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(script)
			return
		}
		// Unknown MAC (ErrNotFound): auto-register the node so it receives a token
		// immediately rather than booting without one and stalling in the initramfs.
		if errors.Is(err, api.ErrNotFound) {
			// Derive a deterministic-looking hostname from the last 6 hex digits of
			// the MAC. e.g. "bc:24:11:da:58:6a" → "node-da586a".
			macClean := strings.ReplaceAll(mac, ":", "")
			shortMAC := macClean
			if len(macClean) >= 6 {
				shortMAC = macClean[len(macClean)-6:]
			}
			hostname := fmt.Sprintf("node-%s", shortMAC)
			nodeID := uuid.New().String()

			newNode := api.NodeConfig{
				ID:           nodeID,
				Hostname:     hostname,
				HostnameAuto: true,
				PrimaryMAC:   mac,
			}
			created, upsertErr := h.DB.UpsertNodeByMAC(r.Context(), newNode)
			if upsertErr != nil {
				log.Error().Err(upsertErr).Str("mac", mac).Msg("boot: auto-register: UpsertNodeByMAC failed")
				// Fall through to tokenless script on error.
			} else {
				log.Info().
					Str("mac", mac).
					Str("hostname", created.Hostname).
					Str("node_id", created.ID).
					Msg("boot: auto-registered unknown MAC — serving boot script with fresh token")
				token := h.mintToken(r, created.ID)
				autoScript, sshPass, genErr := pxe.GenerateBootScript(h.ServerURL, "clustr-node-"+token, "")
				if genErr != nil {
					log.Error().Err(genErr).Str("mac", mac).Msg("boot: generate boot script for auto-registered node")
					http.Error(w, "failed to generate boot script", http.StatusInternalServerError)
					return
				}
				log.Info().Str("mac", mac).Str("node_id", created.ID).Str("ssh_pass", sshPass).
					Msg("boot: auto-register deploy boot script served — SSH debug password for this boot")
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(autoScript)
				return
			}
		}
	} else if mac == "" {
		log.Warn().Msg("boot: iPXE script requested without ?mac= -- returning full boot script")
	}

	// Default: return the full clustr initramfs boot script with no token.
	// Covers: requests without a MAC parameter, or auto-register failures.
	script, sshPass, err := pxe.GenerateBootScript(h.ServerURL, "", "")
	if err != nil {
		log.Error().Err(err).Msg("boot: generate iPXE script")
		http.Error(w, "failed to generate boot script", http.StatusInternalServerError)
		return
	}
	log.Info().Str("mac", mac).Str("ssh_pass", sshPass).
		Msg("boot: tokenless boot script served — SSH debug password for this boot")
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(script)
}

// generateDiskBootScript returns an iPXE disk boot script for the node, branching
// on the node's detected firmware type:
//
//   - BIOS: sanboot --no-describe --drive 0x80  (INT 13h, works on SeaBIOS/real BIOS)
//   - UEFI: chain grub.efi from clustr server (reliable across OVMF and real firmware)
//
// Firmware source priority: node.DetectedFirmware (set at PXE registration from
// /sys/firmware/efi check) > image.Firmware > default "uefi".
//
// Enabled boot_entries rows are fetched from the DB and appended to the menu.
// A failure to fetch entries is logged but does not prevent serving the script.
func (h *BootHandler) generateDiskBootScript(r *http.Request, node *api.NodeConfig) ([]byte, error) {
	firmware := node.DetectedFirmware
	if firmware == "" && h.DB != nil && node.BaseImageID != "" {
		if img, err := h.DB.GetBaseImage(r.Context(), node.BaseImageID); err == nil {
			if img.Firmware != "" {
				firmware = string(img.Firmware)
			}
		} else {
			log.Warn().Err(err).Str("image_id", node.BaseImageID).Str("node", node.Hostname).
				Msg("boot: could not fetch image firmware type")
		}
	}
	if firmware == "" {
		firmware = "uefi"
	}

	// Load enabled boot_entries to append to the menu.
	var extraEntries []api.BootEntry
	if h.DB != nil {
		var err error
		extraEntries, err = h.DB.ListBootEntries(r.Context(), true)
		if err != nil {
			log.Warn().Err(err).Str("node", node.Hostname).
				Msg("boot: could not load boot_entries — serving standard menu")
			extraEntries = nil
		}
	}

	// Check multicast config to determine whether to show the fleet reimage option.
	// A failure to read the config is non-fatal: omit the menu item rather than error.
	var multicastEnabled bool
	if h.DB != nil {
		mCfg, mErr := h.DB.MulticastGetConfig(r.Context())
		if mErr != nil {
			log.Warn().Err(mErr).Str("node", node.Hostname).
				Msg("boot: could not read multicast_config — omitting fleet-reimage menu item")
		} else {
			multicastEnabled = mCfg.Enabled
		}
	}

	// Sprint 34 BOOT-SETTINGS-MODAL: if the operator pinned a netboot menu
	// entry on this node, resolve the boot_entries row and pass it through to
	// the script renderer.  A dangling reference (entry deleted/disabled
	// between modal save and PXE serve) is degraded to "fall back to default
	// disk-boot menu" with a warning — never an error that strands the node.
	var persistedEntry *api.BootEntry
	if node.NetbootMenuEntry != "" && h.DB != nil {
		entry, lookupErr := h.DB.GetBootEntry(r.Context(), node.NetbootMenuEntry)
		switch {
		case lookupErr == nil && entry.Enabled:
			persistedEntry = &entry
		case lookupErr == nil && !entry.Enabled:
			log.Warn().Str("node", node.Hostname).Str("entry_id", node.NetbootMenuEntry).
				Msg("boot: persisted netboot_menu_entry is disabled — falling back to default disk boot")
		default:
			log.Warn().Err(lookupErr).Str("node", node.Hostname).Str("entry_id", node.NetbootMenuEntry).
				Msg("boot: persisted netboot_menu_entry not found — falling back to default disk boot")
		}
	}

	log.Info().Str("hostname", node.Hostname).Str("firmware", firmware).
		Int("extra_entries", len(extraEntries)).
		Bool("multicast_enabled", multicastEnabled).
		Bool("has_persisted_entry", persistedEntry != nil).
		Bool("has_persisted_cmdline", node.KernelCmdline != "").
		Msg("boot: generating disk boot script")
	return pxe.GenerateDiskBootScriptWithSettings(
		node.Hostname, firmware, h.ServerURL, h.Version,
		extraEntries, multicastEnabled,
		persistedEntry, node.KernelCmdline,
	)
}

// multicastBootParams returns the kernel cmdline fragment for multicast delivery
// when wantMulticast is true and multicast is enabled in the DB config.
// Returns "" when multicast is disabled or wantMulticast is false.
// The session poll URL points to the node-specific wait endpoint so the deploy
// agent knows where to poll after enrolling.
func (h *BootHandler) multicastBootParams(r *http.Request, mac, nodeID string, wantMulticast bool) string {
	if !wantMulticast || h.DB == nil {
		return ""
	}
	mCfg, err := h.DB.MulticastGetConfig(r.Context())
	if err != nil {
		log.Warn().Err(err).Str("mac", mac).Msg("boot: could not read multicast_config — no multicast params in cmdline")
		return ""
	}
	if !mCfg.Enabled {
		return ""
	}
	// clustr.multicast=1 signals the deploy agent to attempt multicast delivery.
	// The deploy agent enrolls itself via the standard multicast API endpoints
	// using the same CLUSTR_SERVER base URL already in its cmdline.
	return "clustr.multicast=1"
}

// mintToken calls MintNodeToken if configured and logs failures. Returns the raw
// key (without the clustr-node- prefix) on success, or "" on failure/unconfigured.
// The caller prepends "clustr-node-" before embedding in the cmdline.
func (h *BootHandler) mintToken(r *http.Request, nodeID string) string {
	if h.MintNodeToken == nil || nodeID == "" {
		return ""
	}
	raw, err := h.MintNodeToken(nodeID)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("boot: failed to mint node-scoped token")
		return ""
	}
	return raw
}

// ServeVMLinuz handles GET /api/v1/boot/vmlinuz.
func (h *BootHandler) ServeVMLinuz(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, filepath.Join(h.BootDir, "vmlinuz"), "application/octet-stream")
}

// ServeInitramfs handles GET /api/v1/boot/initramfs.img.
//
// The on-disk filename of the live (rebuildable) initramfs is
// "initramfs-clustr.img" — this is the file the build pipeline (re)writes via
// rename(2) on every successful rebuild and the file the auto-reconcile loop
// watches for staleness (internal/server/reconcile.go and the InitramfsPath
// constant in server.go). The legacy filename "initramfs.img" was a static
// pre-build seed shipped in tftpboot/ for first-boot bootstrap; once a build
// has occurred, the canonical live file is initramfs-clustr.img and the legacy
// path is frozen and SHOULD NOT be served.
//
// Prefer initramfs-clustr.img if it exists (the rebuildable live file). Fall
// back to initramfs.img only if the live file is missing — this preserves the
// pre-first-build bootstrap path on a brand-new install. Without this fallback
// order, every PXE-booting node continues serving the May-3 frozen image while
// the v0.1.12+ rebuilds sit unused on disk, masking deploy-logic fixes (the
// v0.1.13 root-cause investigation surfaced this — the embedded clustr in the
// served initramfs reported v0.1.11 even though the build pipeline had landed
// v0.1.12 multiple times).
func (h *BootHandler) ServeInitramfs(w http.ResponseWriter, r *http.Request) {
	livePath := filepath.Join(h.BootDir, "initramfs-clustr.img")
	if _, err := os.Stat(livePath); err == nil {
		h.serveFile(w, r, livePath, "application/octet-stream")
		return
	}
	// Fallback: pre-build bootstrap seed. Logged so a repeat of the v0.1.13
	// "stale-served-initramfs" class is loud, not silent.
	legacyPath := filepath.Join(h.BootDir, "initramfs.img")
	log.Warn().Str("served", legacyPath).Str("expected_live", livePath).
		Msg("boot: serving legacy initramfs.img — initramfs-clustr.img missing; rebuild via /api/v1/initramfs/rebuild")
	h.serveFile(w, r, legacyPath, "application/octet-stream")
}

// ServeIPXEEFI handles GET /api/v1/boot/ipxe.efi.
//
// Serves the embedded iPXE UEFI binary (x86-64) to OVMF/UEFI HTTP boot clients.
// This is the chainloader that UEFI HTTP boot downloads before executing the
// clustr boot script. It is intentionally served from an embedded binary so that
// the route works out-of-the-box without any on-disk file placement — the UEFI
// HTTP boot client hits 404 and loops forever if this route returns an error.
//
// The embedded binary takes precedence. A future operator override could be
// added by checking for an on-disk file in TFTPDir first, but for now the
// embedded binary is canonical and sufficient for x86-64 UEFI HTTP boot.
func (h *BootHandler) ServeIPXEEFI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/efi")
	http.ServeContent(w, r, "ipxe.efi", time.Time{}, bytes.NewReader(bootassets.IPXEEFI))
}

// ServeUndionlyKPXE handles GET /api/v1/boot/undionly.kpxe.
func (h *BootHandler) ServeUndionlyKPXE(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, filepath.Join(h.TFTPDir, "undionly.kpxe"), "application/octet-stream")
}

func (h *BootHandler) serveFile(w http.ResponseWriter, r *http.Request, path, contentType string) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Warn().Str("path", path).Msg("boot: file not found")
			writeError(w, api.ErrNotFound)
			return
		}
		log.Error().Err(err).Str("path", path).Msg("boot: open file")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
}
