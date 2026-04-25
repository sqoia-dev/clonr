package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// CreateZFSPools creates all ZFS zpools defined in the layout.
// Each pool is created, a ROOT dataset is created inside it, and the dataset's
// mountpoint is set to the configured mountpoint.
//
// Prerequisites on the deploy host:
//   - zpool(8) and zfs(8) must be installed and the zfs kernel module loaded.
//
// v1 constraints:
//   - Supported vdev types: mirror, raidz, stripe (no keyword).
//   - Single pool for root (rpool) with optional separate bpool (/boot).
//   - No nested vdevs, cache, log, or spare devices.
func CreateZFSPools(ctx context.Context, layout api.DiskLayout) error {
	if len(layout.ZFSPools) == 0 {
		return nil
	}

	if err := checkZFSTools(); err != nil {
		return err
	}

	for _, pool := range layout.ZFSPools {
		if err := createZFSPool(ctx, pool); err != nil {
			return fmt.Errorf("deploy/zfs: create pool %s: %w", pool.Name, err)
		}
	}
	return nil
}

// checkZFSTools verifies that zpool and zfs binaries are available.
func checkZFSTools() error {
	for _, bin := range []string{"zpool", "zfs"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("deploy/zfs: %s not found in PATH — install zfsutils-linux or zfs-release and ensure the zfs kernel module is loaded", bin)
		}
	}
	return nil
}

// createZFSPool creates a single zpool and its root dataset.
func createZFSPool(ctx context.Context, pool api.ZFSPool) error {
	log := logger()

	// Resolve member paths to absolute /dev/ paths.
	members := resolveZFSMembers(pool.Members)

	// Wipe any existing ZFS labels on members so zpool create -f succeeds cleanly.
	for _, m := range members {
		wipeCmd := exec.CommandContext(ctx, "wipefs", "-a", m)
		if out, err := wipeCmd.CombinedOutput(); err != nil {
			// Non-fatal: member may have no labels; log and continue.
			log.Debug().Str("device", m).Str("output", string(out)).
				Msg("zfs: wipefs (non-fatal)")
		}
	}

	// Build zpool create arguments.
	// Base flags:
	//   -f  force (overwrite existing pool label / partition table)
	//   -o ashift=12  4K sector alignment (safe default for all modern storage)
	//   -O mountpoint=none  datasets control their own mountpoints
	args := []string{"create", "-f",
		"-o", "ashift=12",
		"-O", "mountpoint=none",
	}

	// Apply user-supplied pool/dataset properties.
	for k, v := range pool.Properties {
		if strings.Contains(k, "/") {
			// Dataset property (key contains "/") — use -O.
			args = append(args, "-O", k+"="+v)
		} else {
			// Pool property — use -o.
			args = append(args, "-o", k+"="+v)
		}
	}

	args = append(args, pool.Name)

	// vdev topology keyword (none for stripe).
	switch pool.VdevType {
	case "mirror", "raidz":
		args = append(args, pool.VdevType)
	case "stripe", "":
		// no vdev keyword for stripe
	}

	args = append(args, members...)

	log.Info().
		Str("pool", pool.Name).
		Str("vdev_type", pool.VdevType).
		Strs("members", members).
		Msg("zfs: creating zpool")
	if err := runAndLog(ctx, "zpool", exec.CommandContext(ctx, "zpool", args...)); err != nil {
		return fmt.Errorf("zpool create: %w", err)
	}

	// Create the ROOT dataset with the configured mountpoint.
	rootDS := pool.Name + "/ROOT"
	dsArgs := []string{"create",
		"-o", "mountpoint=" + pool.Mountpoint,
		rootDS,
	}
	log.Info().Str("dataset", rootDS).Str("mountpoint", pool.Mountpoint).Msg("zfs: creating ROOT dataset")
	if err := runAndLog(ctx, "zfs", exec.CommandContext(ctx, "zfs", dsArgs...)); err != nil {
		return fmt.Errorf("zfs create dataset %s: %w", rootDS, err)
	}

	// Wait for udev to register the new pool devices.
	_ = runCmd(ctx, "udevadm", "settle")

	log.Info().Str("pool", pool.Name).Str("mountpoint", pool.Mountpoint).Msg("zfs: pool and ROOT dataset created")
	return nil
}

// MountZFSPools mounts all ZFS ROOT datasets under mountRoot in mountpoint order
// (root first, then /boot, then everything else).
// Returns the list of datasets that were mounted, for logging and cleanup.
func MountZFSPools(ctx context.Context, layout api.DiskLayout, mountRoot string) ([]string, error) {
	if len(layout.ZFSPools) == 0 {
		return nil, nil
	}

	log := logger()

	// Sort: "/" first so sub-trees land under it, then by mountpoint length.
	sorted := sortedZFSPools(layout.ZFSPools)

	var mounted []string
	for _, pool := range sorted {
		target := filepath.Join(mountRoot, pool.Mountpoint)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return mounted, fmt.Errorf("deploy/zfs: mkdir %s: %w", target, err)
		}
		ds := pool.Name + "/ROOT"
		// zfs mount mounts to the dataset's configured mountpoint; we need it
		// under mountRoot. Use zfs set + mount with -o to override.
		mountCmd := exec.CommandContext(ctx, "mount", "-t", "zfs", ds, target)
		if out, err := mountCmd.CombinedOutput(); err != nil {
			return mounted, fmt.Errorf("deploy/zfs: mount %s → %s: %w\noutput: %s", ds, target, err, string(out))
		}
		log.Info().Str("dataset", ds).Str("target", target).Msg("zfs: dataset mounted")
		mounted = append(mounted, target)
	}
	return mounted, nil
}

// UnmountZFSPools unmounts all ZFS datasets that were mounted by MountZFSPools.
// Unmounts in reverse order (deepest first).
func UnmountZFSPools(ctx context.Context, layout api.DiskLayout, mountRoot string) {
	if len(layout.ZFSPools) == 0 {
		return
	}
	log := logger()
	sorted := sortedZFSPools(layout.ZFSPools)

	// Unmount deepest first.
	for i := len(sorted) - 1; i >= 0; i-- {
		pool := sorted[i]
		target := filepath.Join(mountRoot, pool.Mountpoint)
		if err := runCmd(ctx, "umount", target); err != nil {
			log.Warn().Str("target", target).Err(err).Msg("zfs: unmount failed (non-fatal)")
		}
	}
	// Export all pools so they can be imported on the deployed node.
	for _, pool := range layout.ZFSPools {
		if err := runCmd(ctx, "zpool", "export", pool.Name); err != nil {
			log.Warn().Str("pool", pool.Name).Err(err).Msg("zfs: zpool export failed (non-fatal)")
		}
	}
}

// WriteZFSFstab writes /etc/fstab entries for ZFS pools into the deployed
// filesystem. ZFS uses automount, so the entries use the zfs fstype and
// the dataset name as the source.
func WriteZFSFstab(layout api.DiskLayout, mountRoot string) error {
	if len(layout.ZFSPools) == 0 {
		return nil
	}

	fstabPath := filepath.Join(mountRoot, "etc", "fstab")
	if err := os.MkdirAll(filepath.Dir(fstabPath), 0o755); err != nil {
		return fmt.Errorf("deploy/zfs: mkdir etc: %w", err)
	}

	// Read existing fstab content if present.
	existing, _ := os.ReadFile(fstabPath)

	var lines []string
	for _, pool := range layout.ZFSPools {
		ds := pool.Name + "/ROOT"
		// fstab format: <source> <mountpoint> <type> <options> <dump> <pass>
		// ZFS automount: options=zfsutil for root, defaults for others.
		options := "defaults"
		if pool.Mountpoint == "/" {
			options = "x-systemd.requires=zfs-mount.service"
		}
		line := fmt.Sprintf("%s\t%s\tzfs\t%s\t0\t0\t# managed by clustr",
			ds, pool.Mountpoint, options)
		lines = append(lines, line)
	}

	content := string(existing)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += strings.Join(lines, "\n") + "\n"

	return os.WriteFile(fstabPath, []byte(content), 0o644)
}

// ZFSDracutArgs returns the dracut arguments needed to include ZFS support in
// the initramfs of the deployed OS. Add these to the dracut command line in
// applyBootConfig when ZFS pools are present.
func ZFSDracutArgs() []string {
	return []string{"--add", "zfs", "--force-add", "zfs"}
}

// resolveZFSMembers converts member device names to absolute /dev/ paths.
// Handles both partition names ("sda3") and whole disks ("sda").
func resolveZFSMembers(specs []string) []string {
	resolved := make([]string, 0, len(specs))
	for _, s := range specs {
		if strings.HasPrefix(s, "/dev/") {
			resolved = append(resolved, s)
		} else {
			resolved = append(resolved, "/dev/"+s)
		}
	}
	return resolved
}

// sortedZFSPools returns the pools sorted by mountpoint depth: "/" first,
// then by ascending string length so parent mounts precede children.
func sortedZFSPools(pools []api.ZFSPool) []api.ZFSPool {
	out := make([]api.ZFSPool, len(pools))
	copy(out, pools)
	// Simple insertion sort — pool count is tiny (1-2 in v1).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			ai := mountpointSortKey(out[j-1].Mountpoint)
			aj := mountpointSortKey(out[j].Mountpoint)
			if aj < ai {
				out[j-1], out[j] = out[j], out[j-1]
			}
		}
	}
	return out
}

// mountpointSortKey returns a sort key that places "/" first, then shorter
// paths before longer ones (so /boot sorts before /boot/efi).
func mountpointSortKey(mp string) int {
	if mp == "/" {
		return 0
	}
	return len(mp)
}
