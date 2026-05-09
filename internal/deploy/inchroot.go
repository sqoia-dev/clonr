package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

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
func inChrootReconfigure(ctx context.Context, cfg api.NodeConfig, mountRoot string, instrs []api.InstallInstruction) error {
	log := deployLogger(nil)
	log.Info().Str("mountRoot", mountRoot).Msg("inChrootReconfigure: applying node identity to target filesystem")

	if err := applyNodeConfig(ctx, cfg, mountRoot); err != nil {
		return fmt.Errorf("inChrootReconfigure: %w", err)
	}

	if len(instrs) > 0 {
		log.Info().Str("mountRoot", mountRoot).Int("count", len(instrs)).Msg("inChrootReconfigure: applying install instructions")
		if err := applyInstallInstructions(ctx, mountRoot, instrs); err != nil {
			return fmt.Errorf("inChrootReconfigure: install instructions: %w", err)
		}
	}

	log.Info().Str("mountRoot", mountRoot).Msg("inChrootReconfigure: node identity written — node will boot with correct hostname, network, and config")
	return nil
}

// modifyPayload is the JSON structure expected in an InstallInstruction with
// opcode "modify". Find is a Go regular expression; Replace is the replacement
// string (regexp.ReplaceAll semantics — $1, ${name} etc. are supported).
type modifyPayload struct {
	Find    string `json:"find"`
	Replace string `json:"replace"`
}

// applyInstallInstructions runs each instruction in order against mountRoot.
// Instructions are applied after applyNodeConfig; the image author is
// responsible for idempotency on re-deploys.
func applyInstallInstructions(ctx context.Context, mountRoot string, instrs []api.InstallInstruction) error {
	log := deployLogger(nil)
	for i, instr := range instrs {
		log.Info().Int("step", i+1).Str("opcode", instr.Opcode).Str("target", instr.Target).Msg("install instruction")
		switch instr.Opcode {
		case "modify":
			if err := applyModify(mountRoot, instr); err != nil {
				return fmt.Errorf("step %d (modify %s): %w", i+1, instr.Target, err)
			}
		case "overwrite":
			if err := applyOverwrite(mountRoot, instr); err != nil {
				return fmt.Errorf("step %d (overwrite %s): %w", i+1, instr.Target, err)
			}
		case "script":
			if err := applyScript(ctx, mountRoot, instr, i+1); err != nil {
				return fmt.Errorf("step %d (script): %w", i+1, err)
			}
		default:
			return fmt.Errorf("step %d: unknown opcode %q (must be modify, overwrite, or script)", i+1, instr.Opcode)
		}
	}
	return nil
}

// applyModify performs a find-and-replace inside the file at instr.Target
// within mountRoot. The payload must be JSON-encoded {"find": "<regex>",
// "replace": "<string>"}. Fails if the target file does not exist.
func applyModify(mountRoot string, instr api.InstallInstruction) error {
	if instr.Target == "" {
		return fmt.Errorf("target is required")
	}
	var mp modifyPayload
	if err := json.Unmarshal([]byte(instr.Payload), &mp); err != nil {
		return fmt.Errorf("payload must be JSON {\"find\": \"<regex>\", \"replace\": \"<string>\"}: %w", err)
	}
	if mp.Find == "" {
		return fmt.Errorf("payload.find is required")
	}
	re, err := regexp.Compile(mp.Find)
	if err != nil {
		return fmt.Errorf("payload.find is not a valid regexp: %w", err)
	}

	hostPath := filepath.Join(mountRoot, filepath.FromSlash(instr.Target))
	data, err := os.ReadFile(hostPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("target file %s does not exist in deployed image", instr.Target)
		}
		return fmt.Errorf("read %s: %w", instr.Target, err)
	}

	modified := re.ReplaceAll(data, []byte(mp.Replace))
	if err := os.WriteFile(hostPath, modified, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", instr.Target, err)
	}
	return nil
}

// applyOverwrite writes instr.Payload as text to instr.Target within mountRoot.
// If the file already exists, its existing mode is preserved; otherwise 0644 is
// used. The parent directory must already exist.
func applyOverwrite(mountRoot string, instr api.InstallInstruction) error {
	if instr.Target == "" {
		return fmt.Errorf("target is required")
	}
	hostPath := filepath.Join(mountRoot, filepath.FromSlash(instr.Target))

	// Check parent directory exists.
	parent := filepath.Dir(hostPath)
	if _, err := os.Stat(parent); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("parent directory of %s does not exist in deployed image", instr.Target)
		}
		return fmt.Errorf("stat parent of %s: %w", instr.Target, err)
	}

	// Determine file mode: preserve existing mode, default to 0644.
	var mode fs.FileMode = 0o644
	if info, err := os.Stat(hostPath); err == nil {
		mode = info.Mode()
	}

	if err := os.WriteFile(hostPath, []byte(instr.Payload), mode); err != nil {
		return fmt.Errorf("write %s: %w", instr.Target, err)
	}
	return nil
}

// applyScript writes instr.Payload as a POSIX shell script to a temp file,
// then runs it inside the target root via chroot(2). Fails the deploy if the
// script exits non-zero.
//
// Before chrooting, the executor sets up the standard chroot virtual
// filesystems (/proc, /sys, /dev) AND bind-mounts the host's
// /etc/resolv.conf onto the target's /etc/resolv.conf. This lets scripts
// run `dnf install`, `curl`, `ssh`, or anything else that resolves DNS
// without hanging on the image's baked-in (and unreachable from the deploy
// network) nameserver entries. See chroot_mounts.go for the rationale.
//
// Script output (stdout + stderr) is streamed line-by-line to the deploy
// log as it is produced so an operator can see in-progress activity (e.g.
// "still on dnf install package N of 47") rather than only seeing the
// final blob after the script exits — important when scripts take minutes.
func applyScript(ctx context.Context, mountRoot string, instr api.InstallInstruction, stepNum int) error {
	log := deployLogger(nil)

	// Set up /proc, /sys, /dev, and /etc/resolv.conf bind-mounts so the
	// chroot has a working environment. This is THE fix for the v0.1.15
	// "deploy hangs 35+min on dnf install" bug. Cleanup runs unconditionally
	// (deferred) to avoid leaking mounts on the deploy host.
	cleanup, err := setupChrootMounts(mountRoot)
	if err != nil {
		return fmt.Errorf("setup chroot mounts: %w", err)
	}
	defer cleanup()

	// Write the script to a temp file inside the target root so chroot can find it.
	scriptName := fmt.Sprintf(".clustr-install-step-%d.sh", stepNum)
	hostScriptPath := filepath.Join(mountRoot, scriptName)
	if err := os.WriteFile(hostScriptPath, []byte(instr.Payload), 0o700); err != nil {
		return fmt.Errorf("write script to target: %w", err)
	}
	defer func() { _ = os.Remove(hostScriptPath) }()

	// Run the script inside the chroot, streaming output line-by-line to
	// the deploy log so long-running steps (dnf install of dozens of pkgs)
	// are visible in real time.
	chrootScriptPath := "/" + scriptName
	cmd := exec.CommandContext(ctx, "chroot", mountRoot, "/bin/sh", chrootScriptPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start chroot: %w", err)
	}

	// Tail both pipes concurrently; collect tail of output to embed in any
	// failure error so the operator sees what the script printed before it
	// died, not just "exit status 1".
	var (
		mu       sync.Mutex
		tailBuf  []string
		maxLines = 50
	)
	addTail := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		if len(tailBuf) >= maxLines {
			tailBuf = tailBuf[1:]
		}
		tailBuf = append(tailBuf, line)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go streamPipeWithFallback(stdout, "stdout", stepNum, addTail, &wg)
	go streamPipeWithFallback(stderr, "stderr", stepNum, addTail, &wg)
	wg.Wait()

	if waitErr := cmd.Wait(); waitErr != nil {
		mu.Lock()
		tail := strings.Join(tailBuf, "\n")
		mu.Unlock()
		if tail != "" {
			return fmt.Errorf("script exited non-zero: %w\nlast %d lines of output:\n%s", waitErr, len(tailBuf), tail)
		}
		return fmt.Errorf("script exited non-zero: %w", waitErr)
	}
	return nil
}

// scannerInitBufBytes / scannerMaxTokenBytes are package-level so tests
// can swap in tiny values (a few KB) and trigger the ErrTooLong
// fallback path without allocating tens of megabytes of buffer.
//
// Production: 1MB initial, 64MB max.  Most workloads (dnf transaction
// summary, base64 chunks) stay well under the cap.
var (
	scannerInitBufBytes  = 1 << 20  // 1 MiB
	scannerMaxTokenBytes = 64 << 20 // 64 MiB
)

// streamPipeWithFallback tails r line-by-line via bufio.Scanner with a
// generous max-token buffer.  When the scanner errors (ErrTooLong
// from a single-line emission larger than scannerMaxTokenBytes, or any
// other read failure before EOF), the pipe is drained via a raw
// bufio.Reader in fixed-size chunks so the child process never blocks
// on a full pipe.  Without the fallback path a single oversized line
// caused cmd.Wait to hang until context cancellation — Codex post-ship
// review issue #13.
//
// addTail accepts each emitted log line so the calling chroot driver
// can stitch the tail into its non-zero-exit error message.
func streamPipeWithFallback(r io.Reader, stream string, stepNum int, addTail func(string), wg *sync.WaitGroup) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, scannerInitBufBytes), scannerMaxTokenBytes)
	for scanner.Scan() {
		line := scanner.Text()
		log.Info().Int("step", stepNum).Str("stream", stream).Msg(line)
		addTail(line)
	}
	if err := scanner.Err(); err != nil {
		log.Warn().Int("step", stepNum).Str("stream", stream).Err(err).
			Msg("streamPipe: scanner failed; falling back to raw chunked drain")
		br := bufio.NewReader(r)
		buf := make([]byte, 64*1024)
		for {
			n, readErr := br.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				log.Info().Int("step", stepNum).Str("stream", stream).
					Int("bytes", n).Bool("raw_chunk", true).
					Msg(chunk)
				addTail(chunk)
			}
			if readErr != nil {
				return
			}
		}
	}
}
