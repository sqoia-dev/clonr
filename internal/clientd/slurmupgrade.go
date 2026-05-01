// slurmupgrade.go — Client-side handlers for slurm_dnf_upgrade and slurm_artifact_install.
//
// Primary path (slurm_dnf_upgrade):
//   Server sends target build ID + package specs.
//   Node runs: clustr-privhelper dnf-upgrade <pkg-specs...>
//   Restarts services in role-correct order.
//   Reports {build_id, ok, installed_version, fallback_used: false}.
//
// Fallback path (slurm_artifact_install):
//   Operator-triggered only. Server sends artifact URL + checksum.
//   Node downloads, verifies, extracts tarball to /.
//   Restarts services.
//   Reports {build_id, ok, installed_version, fallback_used: true}.
//
// The fallback path is explicitly NOT wired to the rolling upgrade orchestrator —
// it is surfaced as an emergency operation only.
package clientd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// applyDnfUpgrade handles a slurm_dnf_upgrade message.
// Returns a SlurmDnfUpgradeAckPayload to send back to the server.
func applyDnfUpgrade(ctx context.Context, payload SlurmDnfUpgradePayload) SlurmDnfUpgradeAckPayload {
	log.Info().
		Str("build_id", payload.BuildID).
		Str("version", payload.Version).
		Strs("pkg_specs", payload.PkgSpecs).
		Msg("slurmupgrade: running dnf upgrade via privhelper")

	if len(payload.PkgSpecs) == 0 {
		return dnfUpgradeAckError(payload.BuildID, fmt.Errorf("no package specs provided"))
	}

	// Shell out via clustr-privhelper dnf-upgrade.
	// The privhelper validates that all specs start with "slurm" and restricts
	// dnf to clustr-internal-repo. We never call dnf directly from clientd.
	privhelperArgs := append([]string{"dnf-upgrade"}, payload.PkgSpecs...)
	dnfCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(dnfCtx, "/usr/sbin/clustr-privhelper", privhelperArgs...) //#nosec G204 -- pkg specs validated by privhelper
	out, err := cmd.CombinedOutput()
	if err != nil {
		errMsg := fmt.Sprintf("dnf upgrade failed: %v\noutput: %s", err, strings.TrimRight(string(out), "\n"))
		log.Error().Str("build_id", payload.BuildID).Str("error", errMsg).Msg("slurmupgrade: dnf upgrade failed")
		return dnfUpgradeAckError(payload.BuildID, fmt.Errorf("%s", errMsg))
	}
	log.Info().Str("build_id", payload.BuildID).Msg("slurmupgrade: dnf upgrade completed")

	// Restart Slurm services in role-correct order.
	if err := restartSlurmServicesOrdered(ctx); err != nil {
		log.Warn().Err(err).Msg("slurmupgrade: service restart after dnf upgrade had errors (non-fatal if no services active)")
	}

	installedVer := detectInstalledSlurmVersion(ctx)
	log.Info().
		Str("build_id", payload.BuildID).
		Str("installed_version", installedVer).
		Msg("slurmupgrade: dnf upgrade complete")

	return SlurmDnfUpgradeAckPayload{
		BuildID:          payload.BuildID,
		OK:               true,
		InstalledVersion: installedVer,
		FallbackUsed:     false,
	}
}

// applyArtifactInstall handles a slurm_artifact_install message.
// This is the fallback/recovery path — operator-triggered only.
func applyArtifactInstall(ctx context.Context, baseURL string, payload SlurmArtifactInstallPayload) SlurmArtifactInstallAckPayload {
	log.Info().
		Str("build_id", payload.BuildID).
		Str("version", payload.Version).
		Msg("slurmupgrade: artifact-install fallback triggered (operator-initiated)")

	// Resolve artifact URL.
	downloadURL := payload.ArtifactURL
	if strings.HasPrefix(downloadURL, "/") {
		downloadURL = strings.TrimRight(baseURL, "/") + downloadURL
	}

	// Reuse the existing applySlurmBinary pipeline — it handles download + verify + extract.
	binaryPayload := SlurmBinaryPushPayload{
		BuildID:     payload.BuildID,
		Version:     payload.Version,
		ArtifactURL: payload.ArtifactURL,
		Checksum:    payload.Checksum,
	}
	binaryAck := applySlurmBinary(ctx, baseURL, binaryPayload)

	return SlurmArtifactInstallAckPayload{
		BuildID:          payload.BuildID,
		OK:               binaryAck.OK,
		Error:            binaryAck.Error,
		InstalledVersion: binaryAck.InstalledVersion,
		FallbackUsed:     true, // always true — this path is always the fallback
	}
}

// restartSlurmServicesOrdered restarts Slurm services in the role-correct order.
// slurmdbd first (if present), then slurmctld (if present), then slurmd (if present).
// Services not active on this node are skipped silently.
func restartSlurmServicesOrdered(ctx context.Context) error {
	restCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	order := []string{"slurmdbd", "slurmctld", "slurmd"}
	var lastErr error
	for _, svc := range order {
		chkCtx, chkCancel := context.WithTimeout(ctx, 5*time.Second)
		chk := exec.CommandContext(chkCtx, "systemctl", "is-active", "--quiet", svc)
		err := chk.Run()
		chkCancel()
		if err != nil {
			continue // not active on this node
		}
		cmd := exec.CommandContext(restCtx, "systemctl", "restart", svc)
		if out, err := cmd.CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("restart %s: %w (output: %s)", svc, err, strings.TrimRight(string(out), "\n"))
			log.Error().Err(lastErr).Str("service", svc).Msg("slurmupgrade: service restart failed")
		} else {
			log.Info().Str("service", svc).Msg("slurmupgrade: service restarted")
		}
	}
	return lastErr
}

// dnfUpgradeAckError constructs a failed SlurmDnfUpgradeAckPayload.
func dnfUpgradeAckError(buildID string, err error) SlurmDnfUpgradeAckPayload {
	return SlurmDnfUpgradeAckPayload{
		BuildID:      buildID,
		OK:           false,
		Error:        err.Error(),
		FallbackUsed: false,
	}
}
