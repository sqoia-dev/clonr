package deploy

import (
	"context"
	"fmt"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// inChrootReconfigure applies node-specific identity into the deployed filesystem
// at mountRoot BEFORE it is unmounted. This eliminates the "online but useless"
// first-boot window: previously, a freshly imaged node booted with the image's
// generic hostname/network/hosts settings and only became fully configured once
// clustr-clientd connected and the server sent config_push messages (30s–3m).
//
// Implementation: delegates to applyNodeConfig, which already performs pure
// file-write operations against an arbitrary root. No chroot(2) or binary
// execution inside the target is required for the current set of config kinds —
// all writes are host-side path-prefixed file operations.
//
// The name "inChrootReconfigure" is retained from the gap-sprint plan to
// preserve intent: if a future config kind requires running target binaries
// (e.g. authselect), that specific step must use chroot(2) or systemd-nspawn
// semantics; that work is tracked separately.
//
// Callers: FilesystemDeployer.Finalize and BlockDeployer.Finalize both call
// this function after the image is extracted and mounted but before unmount.
//
// First-boot clustr-clientd still calls applyConfig for live config_push
// messages. The in-chroot pass is idempotent and a safety net — the node
// is already identity-correct when clientd first connects, and clientd
// re-applies any configs that may have changed between deploy and first boot.
func inChrootReconfigure(ctx context.Context, cfg api.NodeConfig, mountRoot string) error {
	log := deployLogger(nil)
	log.Info().Str("mountRoot", mountRoot).Msg("inChrootReconfigure: applying node identity to target filesystem")

	if err := applyNodeConfig(ctx, cfg, mountRoot); err != nil {
		return fmt.Errorf("inChrootReconfigure: %w", err)
	}

	log.Info().Str("mountRoot", mountRoot).Msg("inChrootReconfigure: node identity written — node will boot with correct hostname, network, and config")
	return nil
}
