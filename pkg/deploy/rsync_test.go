//go:build deploy_integration

package deploy_test

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clonr/pkg/deploy/testutil"
)

// TestExtractSmoke is the proof-of-concept integration test for pkg/deploy.
//
// It exercises the full deploy scaffold without requiring an HTTP server or a
// real block device:
//
//  1. Creates a 2 GB sparse file attached as a loopback device
//  2. Partitions it with a single GPT ext4 partition (parted)
//  3. Formats that partition with mkfs.ext4
//  4. Mounts at a temp directory
//  5. Extracts a fake rootfs tar into the mount
//  6. Verifies expected files are present
//  7. Unmounts and cleans up the loopback
//
// This test requires root and the deploy_integration build tag:
//
//	sudo go test -tags=deploy_integration -run TestExtractSmoke -v ./pkg/deploy/...
func TestExtractSmoke(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("TestExtractSmoke requires root (CAP_SYS_ADMIN); re-run with sudo")
	}
	for _, bin := range []string{"parted", "mkfs.ext4", "mount", "umount", "tar"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("TestExtractSmoke requires %q on PATH: %v", bin, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// ── Step 1: allocate a 2 GB sparse loopback disk ──────────────────────────
	t.Log("creating 2 GB fake disk...")
	disk := testutil.NewFakeDisk(t, 2)
	t.Logf("fake disk: file=%s loopdev=%s", disk.Path, disk.LoopDev)

	// ── Step 2: partition with a single GPT partition filling the disk ─────────
	t.Logf("partitioning %s...", disk.LoopDev)
	if out, err := exec.CommandContext(ctx, "parted", "--script", disk.LoopDev,
		"mklabel", "gpt",
		"mkpart", "primary", "ext4", "1MiB", "100%",
	).CombinedOutput(); err != nil {
		t.Fatalf("parted: %v\n%s", err, out)
	}

	// Wait for the kernel to expose the partition device node.
	t.Log("waiting for partition device nodes...")
	if err := disk.WaitForPartitions(1, 15*time.Second); err != nil {
		t.Fatalf("partition node did not appear: %v", err)
	}
	part := disk.Partition(1)
	t.Logf("partition device: %s", part)

	// ── Step 3: format as ext4 ─────────────────────────────────────────────────
	t.Logf("mkfs.ext4 on %s...", part)
	if out, err := exec.CommandContext(ctx, "mkfs.ext4", "-F", part).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v\n%s", err, out)
	}

	// ── Step 4: mount at a temp directory ─────────────────────────────────────
	mnt := t.TempDir()
	t.Logf("mounting %s → %s...", part, mnt)
	if out, err := exec.CommandContext(ctx, "mount", part, mnt).CombinedOutput(); err != nil {
		t.Fatalf("mount: %v\n%s", err, out)
	}
	// Always unmount before cleanup — losetup detach will fail if the device is busy.
	t.Cleanup(func() {
		t.Log("unmounting...")
		if out, err := exec.Command("umount", mnt).CombinedOutput(); err != nil {
			t.Logf("umount %s: %v\n%s", mnt, err, out)
		}
	})

	// ── Step 5: build a fake rootfs tar and extract it ─────────────────────────
	t.Log("building fake rootfs...")
	rootfsPath := testutil.FakeRootfs(t, 64)
	tarReader := testutil.FakeRootfsTar(t, rootfsPath)

	t.Logf("extracting tar into %s...", mnt)
	if err := extractTar(ctx, tarReader, mnt); err != nil {
		t.Fatalf("extract tar: %v", err)
	}

	// ── Step 6: verify expected files are present ──────────────────────────────
	t.Log("verifying extracted rootfs...")
	testutil.VerifyRootfs(t, mnt)

	t.Log("TestExtractSmoke: PASS")
}

// extractTar extracts a tar stream into destDir using Go's archive/tar.
// This bypasses the deploy package's tar subprocess so the test exercises
// the harness itself without requiring a network download.
func extractTar(ctx context.Context, r io.Reader, destDir string) error {
	tr := tar.NewReader(r)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, filepath.Clean(hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)|0o111); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}
