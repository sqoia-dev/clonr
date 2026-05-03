// Package testutil provides test helpers for the deploy package.
//
// # Model
//
// Tests that exercise real partition/format/mount/extract logic use loopback
// block devices backed by sparse files. A sparse file costs no real disk space
// beyond the sectors actually written, so a "2 GB disk" allocates almost
// nothing on the host.
//
// # Root requirement
//
// The loopback ioctl (LOOP_CTL_GET_FREE) and partition scanning require
// CAP_SYS_ADMIN. Tests in this package skip automatically when not running as
// root. To run them:
//
//	sudo go test -tags=deploy_integration -v ./pkg/deploy/...
//
// # Build tag
//
// All loopback-using tests are guarded by the build tag deploy_integration:
//
//	//go:build deploy_integration
//
// A plain "go test ./..." does NOT build or run them. This keeps the normal
// dev loop fast and root-free.
//
// # CI
//
// The privileged integration job runs:
//
//	GOTOOLCHAIN=auto sudo go test -tags=deploy_integration -v -count=1 -timeout=300s ./pkg/deploy/...
//
// The runner must have "privileged: true" (GitHub Actions) or equivalent
// so that losetup and mount calls succeed inside the container.
//
// # Cleanup
//
// Every helper registers cleanup via t.Cleanup(). Leaked loopbacks (from a
// test that panicked or was killed) can be removed with:
//
//	sudo losetup -D
//
// The test-deploy.sh script does this automatically on exit.
package testutil
