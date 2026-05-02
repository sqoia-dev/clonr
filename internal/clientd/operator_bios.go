// operator_bios.go — node-side handlers for BIOS settings messages (#159).
//
// # Message flow
//
//   bios_read_request  (server → node):
//     Node reads current BIOS settings via the vendor binary (exec'd directly
//     since clientd runs as root on deployed nodes) and replies with
//     bios_read_result carrying the settings list.
//
//   bios_apply_request (server → node):
//     POST-BOOT apply path.  Per Sprint 25 D1, BIOS apply runs in initramfs
//     on every deploy.  This handler exists so the post-boot path can be
//     enabled without a protocol change if the design decision is revisited.
//     In v1 it is registered but the server does NOT send bios_apply_request
//     outside of initramfs; the handler returns an error if invoked post-boot
//     (future-proofing, not a footgun).
//
// # Privilege path
//
//   ReadCurrent (bios_read_request): clientd is root on the node; the vendor
//   binary is exec'd directly without privhelper.  This is the same pattern as
//   lsblk in operator_disk_capture.go.
//
//   Apply (bios_apply_request): routes through privhelper verb bios-apply
//   per the standing rule: all host-root ops via one setuid helper.  In v1
//   this path is never triggered server-side; the handler is wired for
//   completeness.
//
// # Drift detection
//
//   The 24h drift check goroutine (startBiosDriftChecker) reads current BIOS
//   settings via the Intel provider, computes a hash, and compares it to the
//   last known applied_settings_hash from the server config.  When they differ
//   it sends a bios_drift message to the server.
package clientd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/bios"
	_ "github.com/sqoia-dev/clustr/internal/bios/intel" // register Intel provider
)

// privhelperPath is the canonical path for the setuid helper binary.
// Matches the constant in internal/privhelper/privhelper.go.
const privhelperPath = "/usr/sbin/clustr-privhelper"

// privhelperBiosApplyCmd constructs the exec.Cmd for bios-apply.
// Extracted as a function (not inlined) so tests can intercept via
// biosApplyViaPrivhelper without needing to replace the whole exec path.
func privhelperBiosApplyCmd(ctx context.Context, vendor, profilePath string) *exec.Cmd {
	return exec.CommandContext(ctx, privhelperPath, "bios-apply", vendor, profilePath) //#nosec G204 -- vendor and profilePath validated by caller; helper enforces allowlist
}

const (
	// biosDriftCheckInterval is how often clientd compares current BIOS settings
	// against the last-applied hash.  Once per day is sufficient for drift
	// detection — settings don't change on their own.
	biosDriftCheckInterval = 24 * time.Hour

	// biosStagingDir is where clientd writes the profile JSON before calling
	// privhelper bios-apply.  Must match the privhelper's biosStagingDir constant.
	biosStagingDir = "/var/lib/clustr/bios-staging/"
)

// HandleBiosReadRequest processes a "bios_read_request" message from the server.
// It reads current BIOS settings via the vendor provider and sends back a
// "bios_read_result" on the send channel.
//
// Called from the node-side message dispatch loop (exec.go or clientd.go).
func HandleBiosReadRequest(ctx context.Context, payload BiosReadRequestPayload, send func(ClientMessage) error) {
	log.Info().
		Str("vendor", payload.Vendor).
		Str("ref_msg_id", payload.RefMsgID).
		Msg("bios: read request received")

	provider, err := bios.Lookup(payload.Vendor)
	if err != nil {
		sendBiosReadResult(send, payload.RefMsgID, payload.Vendor, nil,
			fmt.Sprintf("unknown vendor %q", payload.Vendor))
		return
	}

	settings, err := provider.ReadCurrent(ctx)
	if err != nil {
		sendBiosReadResult(send, payload.RefMsgID, payload.Vendor, nil, err.Error())
		return
	}

	// Convert internal bios.Setting to clientd BiosSetting for wire format.
	wireSettings := make([]BiosSetting, len(settings))
	for i, s := range settings {
		wireSettings[i] = BiosSetting{Name: s.Name, Value: s.Value}
	}
	sendBiosReadResult(send, payload.RefMsgID, payload.Vendor, wireSettings, "")
}

func sendBiosReadResult(send func(ClientMessage) error,
	refMsgID, vendor string, settings []BiosSetting, errMsg string) {

	if settings == nil {
		settings = []BiosSetting{}
	}
	result := BiosReadResultPayload{
		RefMsgID: refMsgID,
		Vendor:   vendor,
		Settings: settings,
		Error:    errMsg,
	}
	raw, _ := json.Marshal(result)
	msg := ClientMessage{
		Type:    "bios_read_result",
		MsgID:   refMsgID,
		Payload: json.RawMessage(raw),
	}
	if err := send(msg); err != nil {
		log.Error().Err(err).Str("ref_msg_id", refMsgID).
			Msg("bios: failed to send bios_read_result")
	}
}

// HandleBiosApplyRequest processes a "bios_apply_request" message from the server.
//
// Post-boot apply path (Sprint 26):
//  1. Validate vendor is in the allowlist (intel; dell/supermicro are future work).
//  2. Stage settings_json to /var/lib/clustr/bios-staging/<msg_id>.json.
//  3. Call clustr-privhelper bios-apply <vendor> <staging-path>.
//  4. Clean up the staging file.
//  5. Reply with bios_apply_result.
//
// Settings are written to BIOS NVRAM.  They take effect on the next POST cycle.
// This handler does NOT reboot the node — the operator must reboot manually.
func HandleBiosApplyRequest(ctx context.Context, payload BiosApplyRequestPayload, send func(ClientMessage) error) {
	log.Info().
		Str("vendor", payload.Vendor).
		Str("profile_id", payload.ProfileID).
		Str("ref_msg_id", payload.RefMsgID).
		Msg("bios: post-boot apply request received")

	// Validate vendor allowlist (intel only in this sprint; dell/supermicro are future).
	allowedVendors := map[string]bool{"intel": true, "dell": true, "supermicro": true}
	if !allowedVendors[payload.Vendor] {
		sendBiosApplyResult(send, payload.RefMsgID, payload.ProfileID, 0,
			"unknown vendor "+payload.Vendor+"; supported vendors: intel")
		return
	}

	if payload.SettingsJSON == "" {
		sendBiosApplyResult(send, payload.RefMsgID, payload.ProfileID, 0, "settings_json is empty")
		return
	}

	// Use msg_id as the staging filename so concurrent applies for different
	// msg_ids don't collide.  Privhelper validates the path prefix.
	stagingID := payload.RefMsgID
	if stagingID == "" {
		stagingID = payload.ProfileID
	}
	stagingPath, err := WriteBiosStagingFile(stagingID, payload.SettingsJSON)
	if err != nil {
		log.Error().Err(err).Str("ref_msg_id", payload.RefMsgID).Msg("bios: failed to write staging file")
		sendBiosApplyResult(send, payload.RefMsgID, payload.ProfileID, 0,
			"staging file write failed: "+err.Error())
		return
	}
	// Clean up staging file regardless of apply outcome.
	defer func() {
		if rerr := os.Remove(stagingPath); rerr != nil && !os.IsNotExist(rerr) {
			log.Warn().Err(rerr).Str("path", stagingPath).Msg("bios: failed to remove staging file")
		}
	}()

	log.Info().
		Str("vendor", payload.Vendor).
		Str("staging_path", stagingPath).
		Str("profile_id", payload.ProfileID).
		Msg("bios: calling privhelper bios-apply")

	// Route through clustr-privhelper per the standing privilege boundary rule.
	if err := biosApplyViaPrivhelper(ctx, payload.Vendor, stagingPath); err != nil {
		log.Error().Err(err).
			Str("vendor", payload.Vendor).
			Str("profile_id", payload.ProfileID).
			Msg("bios: privhelper bios-apply failed")
		sendBiosApplyResult(send, payload.RefMsgID, payload.ProfileID, 0, err.Error())
		return
	}

	// Count applied settings from the JSON blob so the caller gets a meaningful number.
	appliedCount := countSettingsJSON(payload.SettingsJSON)

	log.Info().
		Str("vendor", payload.Vendor).
		Str("profile_id", payload.ProfileID).
		Int("applied_count", appliedCount).
		Msg("bios: post-boot apply succeeded; reboot required for settings to take effect")

	sendBiosApplyResult(send, payload.RefMsgID, payload.ProfileID, appliedCount, "")
}

// biosApplyViaPrivhelper is the interface between the handler and the
// privhelper binary.  Extracted so tests can stub it.
var biosApplyViaPrivhelper = func(ctx context.Context, vendor, profilePath string) error {
	cmd := privhelperBiosApplyCmd(ctx, vendor, profilePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("privhelper bios-apply %s: %w; output: %s",
			vendor, err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// countSettingsJSON returns the number of keys in a flat JSON object string.
// Returns 0 on any parse error (non-fatal; only affects the AppliedCount field).
func countSettingsJSON(settingsJSON string) int {
	var m map[string]string
	if err := json.Unmarshal([]byte(settingsJSON), &m); err != nil {
		return 0
	}
	return len(m)
}

func sendBiosApplyResult(send func(ClientMessage) error,
	refMsgID, profileID string, appliedCount int, errMsg string) {

	result := BiosApplyResultPayload{
		RefMsgID:     refMsgID,
		ProfileID:    profileID,
		OK:           errMsg == "",
		AppliedCount: appliedCount,
		Error:        errMsg,
	}
	raw, _ := json.Marshal(result)
	msg := ClientMessage{
		Type:    "bios_apply_result",
		MsgID:   refMsgID,
		Payload: json.RawMessage(raw),
	}
	if err := send(msg); err != nil {
		log.Error().Err(err).Str("ref_msg_id", refMsgID).
			Msg("bios: failed to send bios_apply_result")
	}
}

// StartBiosDriftChecker starts a background goroutine that periodically reads
// current BIOS settings and compares them against the expected hash.
// When drift is detected it sends a "bios_drift" message via sendFn.
//
// Parameters:
//   - ctx: cancelled when the clientd connection is closed.
//   - nodeID: the node's UUID, included in the drift payload for correlation.
//   - vendor: BIOS vendor identifier ("intel").
//   - profileID: the bios_profiles.id of the currently assigned profile.
//   - expectedHash: sha256(profile.settings_json) from the last successful apply.
//   - profileSettings: the desired settings to diff against current.
//   - sendFn: the clientd send function; must be safe for concurrent use.
//
// When profileID is empty the checker is a no-op (no profile assigned).
func StartBiosDriftChecker(
	ctx context.Context,
	nodeID, vendor, profileID, expectedHash string,
	profileSettings []bios.Setting,
	sendFn func(ClientMessage) error,
) {
	if profileID == "" {
		return // no profile assigned — nothing to check
	}
	go runBiosDriftChecker(ctx, nodeID, vendor, profileID, expectedHash, profileSettings, sendFn)
}

func runBiosDriftChecker(
	ctx context.Context,
	nodeID, vendor, profileID, expectedHash string,
	profileSettings []bios.Setting,
	sendFn func(ClientMessage) error,
) {
	ticker := time.NewTicker(biosDriftCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkBiosDrift(ctx, nodeID, vendor, profileID, expectedHash, profileSettings, sendFn)
		}
	}
}

func checkBiosDrift(
	ctx context.Context,
	nodeID, vendor, profileID, expectedHash string,
	profileSettings []bios.Setting,
	sendFn func(ClientMessage) error,
) {
	provider, err := bios.Lookup(vendor)
	if err != nil {
		log.Warn().Str("vendor", vendor).Err(err).Msg("bios drift: unknown vendor, skipping")
		return
	}

	current, err := provider.ReadCurrent(ctx)
	if err != nil {
		log.Warn().Err(err).Str("vendor", vendor).Msg("bios drift: ReadCurrent failed (binary absent?), skipping")
		return
	}

	// Build a JSON snapshot of current settings for hashing.
	currentMap := make(map[string]string, len(current))
	for _, s := range current {
		currentMap[s.Name] = s.Value
	}
	currentJSON, err := json.Marshal(currentMap)
	if err != nil {
		log.Error().Err(err).Msg("bios drift: marshal current settings")
		return
	}
	actualHash := fmt.Sprintf("%x", sha256.Sum256(currentJSON))

	if actualHash == expectedHash {
		log.Debug().Str("node_id", nodeID).Str("vendor", vendor).
			Msg("bios drift: no drift detected")
		return
	}

	// Drift detected — compute which settings differ.
	changes, _ := provider.Diff(profileSettings, current)
	var driftedSettings []BiosSetting
	for _, c := range changes {
		driftedSettings = append(driftedSettings, BiosSetting{Name: c.Name, Value: c.From})
	}

	log.Warn().
		Str("node_id", nodeID).
		Str("vendor", vendor).
		Str("profile_id", profileID).
		Str("expected_hash", expectedHash).
		Str("actual_hash", actualHash).
		Int("drifted_count", len(driftedSettings)).
		Msg("bios drift: drift detected, reporting to server")

	payload := BiosDriftPayload{
		NodeID:          nodeID,
		Vendor:          vendor,
		ProfileID:       profileID,
		ExpectedHash:    expectedHash,
		ActualHash:      actualHash,
		DetectedAt:      time.Now().UTC(),
		DriftedSettings: driftedSettings,
	}
	raw, _ := json.Marshal(payload)
	msg := ClientMessage{
		Type:    "bios_drift",
		Payload: json.RawMessage(raw),
	}
	if err := sendFn(msg); err != nil {
		log.Error().Err(err).Msg("bios drift: failed to send drift message")
	}
}

// WriteBiosStagingFile writes the profile settings JSON to a file under the
// bios-staging directory.  Returns the path of the written file.
// The caller is responsible for removing the file after privhelper apply.
func WriteBiosStagingFile(profileID, settingsJSON string) (string, error) {
	if err := os.MkdirAll(biosStagingDir, 0o700); err != nil {
		return "", fmt.Errorf("bios staging: mkdir: %w", err)
	}
	path := biosStagingDir + profileID + ".json"
	if err := os.WriteFile(path, []byte(settingsJSON), 0o600); err != nil {
		return "", fmt.Errorf("bios staging: write: %w", err)
	}
	return path, nil
}
