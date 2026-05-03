package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// buildMinimalChroot creates the directory skeleton needed for applyNodeConfig to
// run without errors against a tmpdir root. Only the subdirectories consulted by
// writeHostname and writeNetworkConfig are created — we don't need the full OS tree.
func buildMinimalChroot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{
		"etc",
		"etc/NetworkManager/system-connections",
		"etc/systemd",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("buildMinimalChroot: mkdir %s: %v", dir, err)
		}
	}
	return root
}

// TestInChrootReconfigure_WritesHostname verifies that inChrootReconfigure
// writes /etc/hostname to the target root, not to the live host filesystem.
func TestInChrootReconfigure_WritesHostname(t *testing.T) {
	root := buildMinimalChroot(t)

	cfg := api.NodeConfig{
		Hostname: "compute-01",
		FQDN:     "compute-01.cluster.local",
	}

	if err := inChrootReconfigure(context.Background(), cfg, root, nil); err != nil {
		// applyNodeConfig has non-fatal code paths; a missing NM dir etc. is warned
		// but the function returns nil. A hard error here is a real failure.
		t.Fatalf("inChrootReconfigure: %v", err)
	}

	hostnameFile := filepath.Join(root, "etc", "hostname")
	got, err := os.ReadFile(hostnameFile)
	if err != nil {
		t.Fatalf("read %s: %v", hostnameFile, err)
	}
	if !strings.Contains(string(got), "compute-01") {
		t.Errorf("etc/hostname = %q, want it to contain %q", string(got), "compute-01")
	}

	// Sanity: the live /etc/hostname must not have been changed to "compute-01".
	if liveHostname, err := os.ReadFile("/etc/hostname"); err == nil {
		if strings.TrimSpace(string(liveHostname)) == "compute-01" {
			t.Error("live /etc/hostname was unexpectedly set to compute-01 — inChrootReconfigure targeted the wrong root")
		}
	}
}

// TestInChrootReconfigure_WritesHosts verifies that /etc/hosts entries are
// written to the target root and include the injected cluster host roster.
func TestInChrootReconfigure_WritesHosts(t *testing.T) {
	root := buildMinimalChroot(t)

	cfg := api.NodeConfig{
		Hostname: "head-01",
		FQDN:     "head-01.cluster.local",
		ClusterHosts: []api.HostEntry{
			{IP: "10.99.0.1", Hostname: "clustr-server"},
			{IP: "10.99.0.10", Hostname: "head-01"},
			{IP: "10.99.0.11", Hostname: "compute-01"},
		},
	}

	if err := inChrootReconfigure(context.Background(), cfg, root, nil); err != nil {
		t.Fatalf("inChrootReconfigure: %v", err)
	}

	hostsFile := filepath.Join(root, "etc", "hosts")
	got, err := os.ReadFile(hostsFile)
	if err != nil {
		t.Fatalf("read %s: %v", hostsFile, err)
	}
	for _, wantIP := range []string{"10.99.0.1", "10.99.0.10", "10.99.0.11"} {
		if !strings.Contains(string(got), wantIP) {
			t.Errorf("etc/hosts missing entry for %s\ncontent:\n%s", wantIP, string(got))
		}
	}
}

// TestInChrootReconfigure_NetworkConfig verifies that a simple interface config
// results in a NetworkManager keyfile in the target root.
func TestInChrootReconfigure_NetworkConfig(t *testing.T) {
	root := buildMinimalChroot(t)

	cfg := api.NodeConfig{
		Hostname: "compute-02",
		FQDN:     "compute-02.cluster.local",
		Interfaces: []api.InterfaceConfig{
			{
				Name:      "eth0",
				IPAddress: "10.99.0.20/24",
				Gateway:   "10.99.0.1",
				MTU:       1500,
			},
		},
	}

	if err := inChrootReconfigure(context.Background(), cfg, root, nil); err != nil {
		t.Fatalf("inChrootReconfigure: %v", err)
	}

	nmConn := filepath.Join(root, "etc", "NetworkManager", "system-connections", "eth0.nmconnection")
	got, err := os.ReadFile(nmConn)
	if err != nil {
		t.Fatalf("read %s: %v", nmConn, err)
	}
	if !strings.Contains(string(got), "10.99.0.20") {
		t.Errorf("eth0.nmconnection missing IP address 10.99.0.20\ncontent:\n%s", string(got))
	}
}

// TestInChrootReconfigure_FilesLandInRoot verifies that none of the files written
// by inChrootReconfigure land outside the target root, regardless of what the
// node config contains. This guards against path-traversal regressions.
func TestInChrootReconfigure_FilesLandInRoot(t *testing.T) {
	root := buildMinimalChroot(t)

	// Snapshot modification time of /etc on the LIVE host before calling inChrootReconfigure.
	liveEtcInfo, err := os.Stat("/etc")
	if err != nil {
		t.Skip("cannot stat /etc — skipping live-root guard check")
	}
	liveEtcMtime := liveEtcInfo.ModTime()

	cfg := api.NodeConfig{
		Hostname: "node-test",
		FQDN:     "node-test.cluster.local",
		ClusterHosts: []api.HostEntry{
			{IP: "192.168.1.1", Hostname: "test-host"},
		},
	}

	if err := inChrootReconfigure(context.Background(), cfg, root, nil); err != nil {
		t.Fatalf("inChrootReconfigure: %v", err)
	}

	// If inChrootReconfigure touched the live /etc, its mtime would have changed.
	afterInfo, err := os.Stat("/etc")
	if err != nil {
		t.Fatalf("stat /etc after call: %v", err)
	}
	if afterInfo.ModTime() != liveEtcMtime {
		t.Error("live /etc mtime changed — inChrootReconfigure may have written to the live root")
	}
}

// ─── Install Instruction unit tests (#147) ───────────────────────────────────

// TestInstallInstruction_Overwrite_CreatesFile verifies that the "overwrite"
// opcode writes the payload to the target path with mode 0644.
func TestInstallInstruction_Overwrite_CreatesFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}

	instr := api.InstallInstruction{
		Opcode:  "overwrite",
		Target:  "/etc/motd",
		Payload: "Welcome to clustr node\n",
	}

	if err := applyOverwrite(root, instr); err != nil {
		t.Fatalf("applyOverwrite: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "etc", "motd"))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != instr.Payload {
		t.Errorf("content = %q, want %q", string(got), instr.Payload)
	}

	info, err := os.Stat(filepath.Join(root, "etc", "motd"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode() != 0o644 {
		t.Errorf("mode = %o, want 644", info.Mode())
	}
}

// TestInstallInstruction_Overwrite_PreservesMode verifies that if the target
// file already exists with a non-default mode, that mode is preserved.
func TestInstallInstruction_Overwrite_PreservesMode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}

	hostPath := filepath.Join(root, "etc", "custom")
	if err := os.WriteFile(hostPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	instr := api.InstallInstruction{
		Opcode:  "overwrite",
		Target:  "/etc/custom",
		Payload: "new content",
	}

	if err := applyOverwrite(root, instr); err != nil {
		t.Fatalf("applyOverwrite: %v", err)
	}

	info, err := os.Stat(hostPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != 0o755 {
		t.Errorf("mode = %o, want 755 (preserved)", info.Mode())
	}
	got, _ := os.ReadFile(hostPath)
	if string(got) != "new content" {
		t.Errorf("content = %q, want %q", string(got), "new content")
	}
}

// TestInstallInstruction_Overwrite_MissingParent verifies that "overwrite"
// returns an error when the target's parent directory does not exist.
func TestInstallInstruction_Overwrite_MissingParent(t *testing.T) {
	root := t.TempDir()

	instr := api.InstallInstruction{
		Opcode:  "overwrite",
		Target:  "/etc/no-such-dir/file",
		Payload: "content",
	}

	err := applyOverwrite(root, instr)
	if err == nil {
		t.Fatal("expected error for missing parent directory, got nil")
	}
	if !strings.Contains(err.Error(), "parent directory") && !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %q, want mention of parent directory", err.Error())
	}
}

// TestInstallInstruction_Modify_FindReplace verifies the "modify" opcode
// performs a regex find-and-replace in an existing file.
func TestInstallInstruction_Modify_FindReplace(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}

	hostPath := filepath.Join(root, "etc", "sysctl.conf")
	original := "# kernel settings\nkernel.panic = 0\nvm.swappiness = 60\n"
	if err := os.WriteFile(hostPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	instr := api.InstallInstruction{
		Opcode:  "modify",
		Target:  "/etc/sysctl.conf",
		Payload: `{"find": "kernel\\.panic = 0", "replace": "kernel.panic = 10"}`,
	}

	if err := applyModify(root, instr); err != nil {
		t.Fatalf("applyModify: %v", err)
	}

	got, _ := os.ReadFile(hostPath)
	if !strings.Contains(string(got), "kernel.panic = 10") {
		t.Errorf("expected replace to land; got:\n%s", string(got))
	}
	if strings.Contains(string(got), "kernel.panic = 0\n") {
		t.Error("original value should be replaced")
	}
}

// TestInstallInstruction_Modify_MissingTarget verifies that "modify" fails
// cleanly when the target file does not exist.
func TestInstallInstruction_Modify_MissingTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}

	instr := api.InstallInstruction{
		Opcode:  "modify",
		Target:  "/etc/no-such-file",
		Payload: `{"find": "x", "replace": "y"}`,
	}

	err := applyModify(root, instr)
	if err == nil {
		t.Fatal("expected error for missing target, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %q, want 'does not exist'", err.Error())
	}
}

// TestInstallInstruction_Modify_InvalidRegex verifies that a bad regex in
// payload.find returns an error before touching the file.
func TestInstallInstruction_Modify_InvalidRegex(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	hostPath := filepath.Join(root, "etc", "f")
	if err := os.WriteFile(hostPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	instr := api.InstallInstruction{
		Opcode:  "modify",
		Target:  "/etc/f",
		Payload: `{"find": "[invalid(", "replace": "x"}`,
	}

	err := applyModify(root, instr)
	if err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
}

// TestInstallInstruction_Script_Runs verifies that the "script" opcode executes
// inside the target root and its output lands in a file we can check.
// This test is skipped when /usr/sbin/chroot or /bin/sh is unavailable.
func TestInstallInstruction_Script_Runs(t *testing.T) {
	if _, err := os.Stat("/usr/sbin/chroot"); err != nil {
		if _, err2 := os.Stat("/usr/bin/chroot"); err2 != nil {
			t.Skip("chroot not available — skipping script opcode test")
		}
	}
	// We don't have a real OS tree to chroot into during unit tests. Instead,
	// test applyScript indirectly by verifying the helper writes the script to
	// the correct temp path and cleans it up, without actually exec-ing chroot.
	// The exec path is covered by integration tests on the dev host.
	// Here we just verify the cleanup behaviour by checking the script file
	// is removed even when chroot fails (non-OS tree → exit non-zero).
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	instr := api.InstallInstruction{
		Opcode:  "script",
		Target:  "",
		Payload: "#!/bin/sh\necho hello",
	}

	// applyScript will fail because root is not a real OS tree, but the temp
	// script file must not linger.
	_ = applyScript(context.Background(), root, instr, 1)

	scriptPath := filepath.Join(root, fmt.Sprintf(".clustr-install-step-%d.sh", 1))
	if _, err := os.Stat(scriptPath); err == nil {
		t.Error("temp script file was not cleaned up after applyScript")
	}
}

// TestApplyInstallInstructions_OrderAndOpcode verifies that applyInstallInstructions
// runs all instructions in order and stops on first error.
func TestApplyInstallInstructions_OrderAndOpcode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}

	instrs := []api.InstallInstruction{
		{Opcode: "overwrite", Target: "/etc/step1", Payload: "one"},
		{Opcode: "overwrite", Target: "/etc/step2", Payload: "two"},
	}

	if err := applyInstallInstructions(context.Background(), root, instrs); err != nil {
		t.Fatalf("applyInstallInstructions: %v", err)
	}

	for _, tc := range []struct{ path, want string }{
		{filepath.Join(root, "etc", "step1"), "one"},
		{filepath.Join(root, "etc", "step2"), "two"},
	} {
		got, err := os.ReadFile(tc.path)
		if err != nil {
			t.Errorf("read %s: %v", tc.path, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s = %q, want %q", tc.path, string(got), tc.want)
		}
	}
}

// TestApplyInstallInstructions_StopsOnError verifies that instruction execution
// halts at the first failure and does not continue to subsequent steps.
func TestApplyInstallInstructions_StopsOnError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}

	instrs := []api.InstallInstruction{
		// This will fail: parent /etc/missing-dir does not exist.
		{Opcode: "overwrite", Target: "/etc/missing-dir/file", Payload: "x"},
		// This should not run.
		{Opcode: "overwrite", Target: "/etc/should-not-exist", Payload: "y"},
	}

	err := applyInstallInstructions(context.Background(), root, instrs)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if _, statErr := os.Stat(filepath.Join(root, "etc", "should-not-exist")); statErr == nil {
		t.Error("second instruction ran after first failed")
	}
}

// TestApplyInstallInstructions_UnknownOpcode verifies that an unknown opcode
// returns an error immediately.
func TestApplyInstallInstructions_UnknownOpcode(t *testing.T) {
	root := t.TempDir()
	instrs := []api.InstallInstruction{
		{Opcode: "unknown_op", Target: "/etc/f", Payload: "x"},
	}
	err := applyInstallInstructions(context.Background(), root, instrs)
	if err == nil {
		t.Fatal("expected error for unknown opcode, got nil")
	}
	if !strings.Contains(err.Error(), "unknown opcode") {
		t.Errorf("error = %q, want mention of 'unknown opcode'", err.Error())
	}
}

// TestInChrootReconfigure_WithInstructions verifies that inChrootReconfigure
// runs install instructions after applying node identity.
func TestInChrootReconfigure_WithInstructions(t *testing.T) {
	root := buildMinimalChroot(t)

	cfg := api.NodeConfig{
		Hostname: "test-node",
		FQDN:     "test-node.cluster.local",
	}

	instrs := []api.InstallInstruction{
		{Opcode: "overwrite", Target: "/etc/clustr-test-marker", Payload: "instructions-ran"},
	}

	if err := inChrootReconfigure(context.Background(), cfg, root, instrs); err != nil {
		t.Fatalf("inChrootReconfigure: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "etc", "clustr-test-marker"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != "instructions-ran" {
		t.Errorf("marker = %q, want %q", string(got), "instructions-ran")
	}
}
