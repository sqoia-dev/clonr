package chroot

import (
	"errors"
	"syscall"
	"testing"
)

// TestDefaultMounts verifies the standard mount set contains the required
// virtual filesystems in the correct order.
func TestDefaultMounts(t *testing.T) {
	specs := DefaultMounts()

	required := []string{"proc", "sys", "dev", "dev/pts", "run"}
	if len(specs) < len(required) {
		t.Fatalf("expected at least %d mount specs, got %d", len(required), len(specs))
	}

	for i, target := range required {
		if specs[i].Target != target {
			t.Errorf("spec[%d].Target: expected %q, got %q", i, target, specs[i].Target)
		}
	}
}

func TestDefaultMounts_ProcFlags(t *testing.T) {
	specs := DefaultMounts()
	proc := specs[0]
	if proc.FSType != "proc" {
		t.Errorf("proc spec FSType: expected \"proc\", got %q", proc.FSType)
	}
	if proc.Flags&syscall.MS_NOSUID == 0 {
		t.Error("proc spec should have MS_NOSUID set")
	}
	if proc.Flags&syscall.MS_NOEXEC == 0 {
		t.Error("proc spec should have MS_NOEXEC set")
	}
}

func TestDefaultMounts_DevIsBind(t *testing.T) {
	specs := DefaultMounts()
	// Find the /dev bind mount
	var devSpec *MountSpec
	for i := range specs {
		if specs[i].Target == "dev" {
			devSpec = &specs[i]
			break
		}
	}
	if devSpec == nil {
		t.Fatal("no dev mount in DefaultMounts()")
	}
	if devSpec.Flags&syscall.MS_BIND == 0 {
		t.Error("dev mount should be a bind mount (MS_BIND)")
	}
}

// TestMountOrderPreserved ensures MountAll processes specs in the order given.
// We use a fake mounter to capture the sequence without touching the kernel.
func TestMountOrderPreserved(t *testing.T) {
	var order []string
	fakeMounter := func(source, target, fstype string, flags uintptr, data string) error {
		order = append(order, target)
		return nil
	}

	var unmountOrder []string
	fakeUnmounter := func(target string, flags int) error {
		unmountOrder = append(unmountOrder, target)
		return nil
	}

	specs := []MountSpec{
		{Source: "proc", Target: "proc", FSType: "proc"},
		{Source: "sysfs", Target: "sys", FSType: "sysfs"},
		{Source: "/dev", Target: "dev", FSType: ""},
	}

	cleanup, err := mountAllWith("/chroot", specs, fakeMounter, fakeUnmounter)
	if err != nil {
		t.Fatalf("mountAllWith: %v", err)
	}

	expectedMount := []string{"/chroot/proc", "/chroot/sys", "/chroot/dev"}
	for i, want := range expectedMount {
		if i >= len(order) {
			t.Fatalf("only %d mounts recorded, expected %d", len(order), len(expectedMount))
		}
		if order[i] != want {
			t.Errorf("mount[%d]: expected %q, got %q", i, want, order[i])
		}
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Cleanup must happen in reverse order.
	expectedUnmount := []string{"/chroot/dev", "/chroot/sys", "/chroot/proc"}
	for i, want := range expectedUnmount {
		if i >= len(unmountOrder) {
			t.Fatalf("only %d unmounts recorded, expected %d", len(unmountOrder), len(expectedUnmount))
		}
		if unmountOrder[i] != want {
			t.Errorf("unmount[%d]: expected %q, got %q", i, want, unmountOrder[i])
		}
	}
}

// TestMountAllFailureRollsBack verifies that if a mount fails mid-way, the
// already-mounted specs are cleaned up before the error is returned.
func TestMountAllFailureRollsBack(t *testing.T) {
	var mounted []string
	var unmounted []string

	fakeMounter := func(source, target, fstype string, flags uintptr, data string) error {
		// Fail on the third mount.
		if len(mounted) == 2 {
			return errors.New("simulated mount failure")
		}
		mounted = append(mounted, target)
		return nil
	}
	fakeUnmounter := func(target string, flags int) error {
		unmounted = append(unmounted, target)
		return nil
	}

	specs := []MountSpec{
		{Source: "proc", Target: "proc", FSType: "proc"},
		{Source: "sysfs", Target: "sys", FSType: "sysfs"},
		{Source: "/dev", Target: "dev", FSType: ""}, // this will fail
	}

	_, err := mountAllWith("/chroot", specs, fakeMounter, fakeUnmounter)
	if err == nil {
		t.Fatal("expected error from failed mount, got nil")
	}

	// The two successful mounts must have been unmounted.
	if len(unmounted) != 2 {
		t.Errorf("expected 2 rollback unmounts, got %d: %v", len(unmounted), unmounted)
	}
	// Rollback must be in reverse order.
	if len(unmounted) == 2 {
		if unmounted[0] != "/chroot/sys" || unmounted[1] != "/chroot/proc" {
			t.Errorf("rollback order wrong: %v", unmounted)
		}
	}
}

// TestNewSession_MissingRoot verifies that NewSession returns an error for a
// non-existent directory.
func TestNewSession_MissingRoot(t *testing.T) {
	_, err := NewSession("/this/does/not/exist/clustr-test")
	if err == nil {
		t.Fatal("expected error for missing root, got nil")
	}
}

// TestSession_CloseIdempotent verifies Close can be called safely with no
// Enter (no cleanups registered).
func TestSession_CloseIdempotent(t *testing.T) {
	s := &Session{Root: "/tmp", Mounts: DefaultMounts()}
	if err := s.Close(); err != nil {
		t.Errorf("Close on un-entered session: %v", err)
	}
	// Second close should also be safe.
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
