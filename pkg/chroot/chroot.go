package chroot

import (
	"fmt"
	"os"
	"os/exec"
)

// Session manages a chroot environment including mount lifecycle.
// Always call Close() to release mounts — use defer after checking error.
type Session struct {
	Root     string
	Mounts   []MountSpec
	cleanups []func() error
}

// NewSession creates a Session for the given root directory using the default
// virtual filesystem mount set. The root must exist and contain a valid
// Linux root filesystem.
func NewSession(root string) (*Session, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("chroot: root %q: %w", root, err)
	}
	return &Session{
		Root:   root,
		Mounts: DefaultMounts(),
	}, nil
}

// Enter mounts all virtual filesystems into the chroot root. It must be called
// before Exec or Shell. Always pair with Close().
func (s *Session) Enter() error {
	cleanup, err := MountAll(s.Root, s.Mounts)
	if err != nil {
		return fmt.Errorf("chroot: enter %s: %w", s.Root, err)
	}
	s.cleanups = append(s.cleanups, cleanup)
	return nil
}

// Exec runs a command inside the chroot using the system chroot(8) utility.
// Enter must have been called first. The command's combined stdout+stderr is
// returned on success; a non-zero exit is wrapped in the returned error.
func (s *Session) Exec(command string, args ...string) ([]byte, error) {
	chrootArgs := append([]string{s.Root, command}, args...)
	cmd := exec.Command("chroot", chrootArgs...)
	cmd.Env = chrootEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("chroot: exec %q in %s: %w", command, s.Root, err)
	}
	return out, nil
}

// Shell drops into an interactive bash shell inside the chroot.
// stdin, stdout, and stderr are connected to the calling process's terminal.
func (s *Session) Shell() error {
	cmd := exec.Command("chroot", s.Root, "/bin/bash", "--login")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = chrootEnv()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("chroot: shell in %s: %w", s.Root, err)
	}
	return nil
}

// Close unmounts all filesystems mounted by Enter, in reverse order.
// It is safe to call multiple times. Always call this, even on error paths.
func (s *Session) Close() error {
	var firstErr error
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		if err := s.cleanups[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.cleanups = nil
	return firstErr
}

// chrootEnv returns a minimal environment suitable for chroot execution.
// It avoids leaking host paths that might not exist inside the chroot.
func chrootEnv() []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
}
