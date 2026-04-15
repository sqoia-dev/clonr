# clonr Validation and Testing Strategy

**Date:** 2026-04-13
**Last updated:** 2026-04-13 (Sprint 2 post-mortem: serial console gate, boot matrix, ADR-0008 alignment)
**Scope:** Defines what "works, testable, validated" means for clonr, and how we get there before v1.0 ships.

---

## Serial Console Verification — Mandatory Gate

### Why This Exists

Sprint 2 produced four nodes (VM206, VM207, VM201, VM202) that telemetry declared "success" while their actual state ranged from broken bootloader to empty `/boot` partition to missing NVRAM entries. The `deploy-complete` callback fires from inside the PXE initramfs — it proves `clonr-static` ran clean, not that the deployed OS boots. Hours of debugging were required before anyone attached a serial console.

**Standing rule, effective immediately:** For every deploy-path test — automated or manual, VM or physical — the pass criterion requires a verbatim login prompt captured from the serial console. Telemetry alone (deploy event `status=success`, `deploy_completed_preboot_at` set, SSH responding) is a necessary but not sufficient condition. It does not replace serial console confirmation.

This rule applies to:
- Every cell in the test matrix below
- Every E2E golden path run (Proxmox lab)
- Every pre-release gate run
- Every nightly failure-mode run that exercises the deploy path

### Serial Console Capture Standard

A valid serial console capture is a text artifact (log file or CI artifact) containing a contiguous block that includes:

1. BIOS/UEFI POST output (or at minimum the final POST line before bootloader)
2. GRUB/systemd-boot menu or direct boot line
3. Kernel boot messages (`Booting Linux...` or equivalent)
4. At least one systemd unit starting (`Starting ...`)
5. The login prompt: `<hostname> login:` or equivalent (getty output)

The artifact must be timestamped and associated with the specific deploy cycle (node ID + deploy timestamp). It is stored as a CI artifact on every E2E run. A run that produces no serial capture, or a capture that does not reach step 5, is a FAIL regardless of telemetry state.

For Proxmox lab VMs: serial console output is captured via `qm terminal <vmid>` piped to a log file, or via the Proxmox SPICE/VNC console log export. Gilfoyle owns the tooling for automated capture and artifact upload.

---

## Boot Matrix

### Matrix Definition

Every deploy-path must be validated across the full combination of firmware type, storage topology, and disk size. The matrix is:

| | Single disk | RAID1 (2-disk) | RAID5 (3-disk) | RAID10 (4-disk) |
|---|---|---|---|---|
| **BIOS** | Cell 1 | Cell 2 | Cell 3 | Cell 4 |
| **UEFI** | Cell 5 | Cell 6 | Cell 7 | Cell 8 |

Each cell is further subdivided by disk size tier:

- **Small:** < 100 GB (typical NVMe SSD in lab VMs, 32 GB virtual disk)
- **Medium:** 100 GB – 2 TB (typical enterprise SATA/SAS, 500 GB virtual disk)
- **Large:** > 2 TB (GPT required, 4 TB virtual disk; tests 2 TB boundary behavior in partition layout)

Full matrix: 8 topology cells × 3 size tiers = 24 cells.

For v1.0, the minimum bar is coverage of all 8 topology cells at the medium size tier (16 cells), plus the small and large tiers for the two most common paths (UEFI single-disk, BIOS single-disk) = 20 cells minimum. The remaining 4 large-disk RAID cells are Sprint 2+.

### Current Lab Coverage (VMs 201–207)

| Cell | Description | Lab VM | Status | Notes |
|------|-------------|--------|--------|-------|
| BIOS / single-disk / small | BIOS boot, 1× 32 GB virt disk | VM203 | Partial | BIOS boot confirmed, serial capture not yet automated |
| BIOS / single-disk / medium | BIOS boot, 1× 500 GB virt disk | VM204 | Not started | VM exists, no image tested |
| BIOS / RAID1 / small | BIOS boot, 2× 32 GB | VM205 | Not started | Need to add second virt disk |
| BIOS / RAID1 / medium | BIOS boot, 2× 500 GB | — | Needs new VM | Not provisioned |
| BIOS / RAID5 / medium | BIOS boot, 3× 500 GB | — | Needs new VM | Not provisioned |
| BIOS / RAID10 / medium | BIOS boot, 4× 500 GB | — | Needs new VM | Not provisioned |
| UEFI / single-disk / small | UEFI boot, 1× 32 GB | VM201 | Broken (Sprint 2) | EFI partition / NVRAM issues; Dinesh fixing |
| UEFI / single-disk / medium | UEFI boot, 1× 500 GB | VM202 | Broken (Sprint 2) | Same root cause as VM201 |
| UEFI / RAID1 / small | UEFI boot, 2× 32 GB | VM206 | Broken (Sprint 2) | False-green in Sprint 2 |
| UEFI / RAID1 / medium | UEFI boot, 2× 500 GB | VM207 | Broken (Sprint 2) | False-green in Sprint 2 |
| UEFI / RAID5 / medium | UEFI boot, 3× 500 GB | — | Needs new VM | Not provisioned |
| UEFI / RAID10 / medium | UEFI boot, 4× 500 GB | — | Needs new VM | Not provisioned |
| BIOS / single-disk / large | BIOS boot, 1× 4 TB | — | Needs new VM | GPT boundary test |
| UEFI / single-disk / large | UEFI boot, 1× 4 TB | — | Needs new VM | GPT boundary test |
| BIOS / RAID1 / large | BIOS boot, 2× 4 TB | — | Sprint 2+ | Deferred |
| UEFI / RAID1 / large | UEFI boot, 2× 4 TB | — | Sprint 2+ | Deferred |

**Covered by current lab (VMs 201–207):** 10 of the 24 cells are reachable with existing VMs (though several are currently in "Broken" state pending Dinesh's UEFI fixes). 6 cells need new VMs provisioned on the Proxmox host. 4 large-disk RAID cells are deferred.

**Physical hardware requirement:** At least 2 cells (BIOS/single-disk/medium and UEFI/single-disk/medium) must be validated on physical hardware before v1.0 — not just on QEMU VMs. QEMU's virtio block device does not faithfully reproduce real NVMe/SATA timing, BIOS POST sequences, or EFI NVRAM behavior. Gilfoyle to provision one physical node (lab server or repurposed workstation) for this purpose.

### Cell Pass Criterion

A matrix cell is GREEN when:
1. A full deploy completes with `deploy_verified_booted_at` set (ADR-0008 two-phase model).
2. A serial console capture artifact exists showing a login prompt (per the serial console standard above).
3. The test has been run 3 consecutive times without failure (pre-release) or once (nightly/push-to-main).

A cell is RED if any of the three conditions above are not met. A cell that passes on telemetry alone, without serial console capture, is YELLOW — it is not counted toward the pre-release gate.

---

## Deploy Integration Test Scaffold

The loopback-backed integration test harness for `pkg/deploy` lives at:

| Path | Purpose |
|------|---------|
| `pkg/deploy/testutil/` | Test helpers: `NewFakeDisk`, `FakeRootfs`, `FakeRootfsTar`, `VerifyRootfs` |
| `pkg/deploy/rsync_test.go` | `TestExtractSmoke` — proof-of-concept full-pipeline test |
| `scripts/test-deploy.sh` | CI/dev helper that sets up environment and runs the tag |

**Build tag:** `deploy_integration` — all loopback tests are excluded from `go test ./...` and only compile/run when the tag is set.

**Root requirement:** loopback ioctls require `CAP_SYS_ADMIN`. Tests skip automatically when not root. To run:

```bash
# Developer machine
sudo go test -tags=deploy_integration -run TestExtractSmoke -v ./pkg/deploy/...

# Or via the helper script (handles sudo automatically)
./scripts/test-deploy.sh -run TestExtractSmoke

# Full integration suite
./scripts/test-deploy.sh
```

**CI:** The privileged job must set `privileged: true` (GitHub Actions) or grant `CAP_SYS_ADMIN`. See `scripts/test-deploy.sh` for the exact invocation and loopback cleanup on exit.

---

## What "Works, Testable, Validated" Means

A contract, not a guideline.

**Works** means: a sequence of user actions that the product claims to support — "import a Rocky 9 image, enroll 50 nodes, trigger a rolling deploy, verify all nodes boot and join the scheduler" — succeeds end-to-end on real hardware without manual intervention, without operator-visible errors, and without leaving any node in an unrecoverable state. Works does not mean "the code compiles and the unit tests pass." It means the system does what the README says it does on the hardware it claims to support.

**Testable** means: every claim in the v1.0 MVP checklist (see ROADMAP.md) has a corresponding automated test or a documented manual procedure with an unambiguous pass/fail criterion. "It feels right" is not testable. "node_exporter is reachable on port 9100 of the deployed node" is testable.

**Validated** means: the release gate has been run, every gate item is green, and the results are recorded in the release artifact (a commit-tagged test run output). The release tag is not cut until the gate is passed. There are no exceptions.

---

## Test Pyramid

### Layer 1: Unit Tests

**Which packages need coverage:**

| Package | Coverage target | What to test |
|---------|----------------|--------------|
| `pkg/hardware` | 90% | Collector parsing against fixture files (real lsblk JSON, cpuinfo, meminfo). Each collector independently. Graceful degradation when dmidecode unavailable. |
| `pkg/image` (types, store) | 80% | BaseImage/NodeConfig CRUD through the ImageStore interface. Checksum canonicalization (alphabetical walk order). DiskLayout parsing and validation. |
| `pkg/deploy` (preflight, finalize) | 85% | Preflight disk matching logic: correct selection by type and size, rejection of ambiguous matches, TargetDiskHint override. FinalizeResult warning accumulation. |
| `pkg/db` | 75% | Migration runner correctness (apply, idempotency). Query wrappers round-trip. Encryption/decryption wrapper (AES-256-GCM correctness, nonce uniqueness over 1000 iterations). |
| `pkg/server/middleware` | 85% | API key scope enforcement: admin key reaches all endpoints, node key gets 403 on admin endpoints, expired key gets 401, malformed token gets 401. |
| `pkg/config` | 80% | Server config validation: fail fast on unwritable ImageDir, inaccessible TFTP dir, missing master key. |

**Who writes them:** Dinesh writes unit tests alongside each feature. Tests ship in the same PR as the feature. No deferred "we'll add tests later."

**Test fixtures:** All hardware fixtures live in `test/fixtures/`. They are real outputs from real hardware, not synthetic. When a new hardware variant is encountered in the lab, its fixture is added.

**Mocking policy:** The `ImageStore` interface is the primary mock surface. Use `testify/mock` for the store in handler tests. Do not mock the filesystem or the OS — use tempdir and loopback devices instead. Test against reality.

---

### Layer 2: Integration Tests

Integration tests require either root privileges (for mount/chroot operations) or access to the Proxmox lab. They are not run on every commit — they run on every push to main and on every PR that touches deploy or db packages.

**Chroot lifecycle (`test/integration/chroot_test.go`):**

Already exists in the architecture. Validate: NewChrootSession mounts in correct order, InjectFile writes to the correct path inside the chroot, Close unmounts in reverse order without leaving stale mounts, a panic inside RunInChroot triggers defer cleanup. Run against a loopback-mounted ext4 rootfs created from a small tarball fixture.

**Blob streaming (`test/integration/blob_test.go`):**

Spin up an in-process `clonr-serverd` against a tempdir database and image store. Upload a 100 MB synthetic blob. Download it with 10 concurrent goroutines using Range requests, with deliberate mid-transfer connection drops on clients 3 and 7. Verify: all clients receive the complete blob (SHA-256 matches), no server panic, download resumes correctly from the interrupted offset.

**Deploy pipeline — loopback mode (`test/integration/deploy_test.go`):**

Create two loopback block devices (simulating NVMe and SATA disks). Run `FilesystemDeployer.Preflight` against a DiskLayout that specifies NVMe-only — verify it selects the correct device and rejects the SATA device. Deploy a minimal Rocky 9 rootfs tarball fixture. Run `Finalize` with a synthetic NodeConfig. Verify: /etc/hostname is correct, NetworkManager keyfile is written, chrony.conf is present, checksum verifies.

**API key scope (`test/integration/auth_test.go`):**

Spin up an in-process server. Create an admin key and a node key. Verify: admin key reaches `POST /api/v1/images`, node key gets 403 on the same endpoint. Node key reaches `GET /api/v1/nodes/by-mac/:mac` successfully. Expired key (set `expires_at` to the past) gets 401 on all endpoints.

**Encryption roundtrip (`test/integration/secrets_test.go`):**

Initialize a database with a master key in a tempdir. Write a NodeConfig with a BMC password via the store. Read the raw SQLite row directly (bypass the store) and assert the `bmc_password` column is NOT the plaintext value. Read the NodeConfig back via the store and assert the BMC password decrypts to the original value. Rotate the master key. Verify the decrypted value is still correct post-rotation.

---

### Layer 3: End-to-End Tests (Proxmox Lab)

The Proxmox lab (documented in `docs/test-lab-design.md`) is the ground truth. E2E tests run against real VMs with real PXE boot, real block devices, and real OS images.

**Golden path E2E (CI — runs on every push to main, nightly, and pre-release):**

1. Start test-node-01 (VMID 201, UEFI, NVMe-style disk) from powered-off state.
2. clonr-serverd is running on VMID 200 with a pre-built Rocky 9 image.
3. test-node-01 PXE boots, registers via MAC lookup, receives deploy action.
4. clonr CLI (initramfs) deploys the Rocky 9 image.
5. Assertions (phase 1 — pre-boot): deploy event posted to server with `status=success`; `deploy_completed_preboot_at` is set on the node record.
6. Assertions (phase 2 — boot verification, per ADR-0008): node reboots into deployed OS; `clonr-verify-boot.service` fires and server sets `deploy_verified_booted_at` within `CLONR_VERIFY_TIMEOUT`; node state transitions to `deployed_verified`. Serial console capture artifact uploaded showing login prompt. SSH responds on management IP; /etc/hostname matches NodeConfig; chrony is synchronized; node_exporter is reachable on :9100.
7. Trigger a second deploy (different image version) via the rolling deploy API.
8. Assertions: SLURM drain event fired (if slurmctld is running in the lab); second deploy completes both phases; node reboots into second image with `deployed_verified` state; SLURM resume fired.

This test is the release gate for v1.0. It must pass 3 consecutive runs without failure before the release tag is cut.

**Failure-mode E2E (nightly only):**

1. Mid-transfer kill: terminate clonr-serverd during blob download to test-node-02. Verify: test-node-02 retries and completes successfully on restart.
2. Wrong disk: boot test-node-03 (two SATA disks). Deploy with a DiskLayout specifying NVMe. Verify: Preflight rejects the deploy cleanly with an error event; neither disk is touched.
3. Failed finalize: inject a deliberate error into the Finalize path (write a bad grub config). Verify: FinalizeResult carries a fatal error, deploy event has `status=error`, IPMI boot device is reset to PXE.

**Concurrent load test (pre-release only):**

Using all three test-node VMs plus QEMU VMs spawned on the Proxmox host, simulate 20 concurrent blob download clients. Measure aggregate throughput. Gate: aggregate throughput >= 200 MB/s on the provisioning bridge. This validates the blob concurrency cap (ADR-0003) does not deadlock and that the server does not OOM.

---

## Golden Path CI

**Every commit / PR:**
- `go vet ./...`
- `golangci-lint run` (staticcheck + errcheck + gosec — the `_ = err` pattern is flagged by errcheck)
- `go test ./...` excluding `test/integration/` and `test/e2e/`
- Build both binaries: `GOTOOLCHAIN=auto CGO_ENABLED=0 go build ./cmd/clonr-serverd ./cmd/clonr`

**Every push to main:**
- All of the above
- Integration tests: `go test ./test/integration/... -tags integration` (requires root, runs in a privileged CI runner)
- E2E golden path against Proxmox lab (Gilfoyle owns the runner and lab state)
- Serial console capture for each E2E deploy uploaded as CI artifact; run marked FAIL if capture is absent or does not reach login prompt

**Nightly:**
- All integration tests
- E2E golden path (with serial console capture artifacts)
- E2E failure-mode tests
- Boot matrix sweep: run the golden path deploy against every GREEN or YELLOW cell in the boot matrix table above; update cell status in a nightly report artifact
- 24-hour server stability soak: run clonr-serverd with simulated load (repeated MAC lookups, blob downloads, deploy events) for 24 hours; assert zero panics, memory usage stable within 10% over the run

**Pre-release (before cutting a tag):**
- All nightly tests, 3 consecutive passing runs of E2E golden path
- Boot matrix: all covered cells (per the table above) must be GREEN — i.e., `deployed_verified` state confirmed AND serial console capture showing login prompt. YELLOW cells (telemetry-only) are not accepted as green at pre-release.
- Concurrent load test: aggregate throughput gate
- Manual checklist walk-through of every MVP checklist item in ROADMAP.md, signed off by Gilfoyle
- ADR-0008 gate: every deploy in the pre-release run must set `deploy_verified_booted_at` within `CLONR_VERIFY_TIMEOUT`; any node left in `deployed_preboot` or `deploy_verify_timeout` at run end is a blocking failure

---

## Release Gates for v1.0

The following must all be green before `git tag v1.0.0` is pushed. No exceptions.

| Gate | Pass criterion |
|------|---------------|
| Unit test coverage | pkg/hardware >= 90%, pkg/deploy >= 85%, pkg/server/middleware >= 85% |
| Integration: auth scope | node-scoped key gets 403 on admin endpoints in automated test |
| Integration: encryption | No plaintext BMC credentials in SQLite after migration (grep-verifiable) |
| Integration: disk selection | Preflight selects correct disk on multi-disk test node, rejects wrong-type disk |
| E2E: golden path | 3 consecutive passing runs on Proxmox lab; each run produces serial console capture with login prompt as CI artifact |
| E2E: two-phase verification (ADR-0008) | Every deploy in the 3 golden path runs sets both `deploy_completed_preboot_at` and `deploy_verified_booted_at`; no node ends in `deployed_preboot` or `deploy_verify_timeout` |
| E2E: serial console gate | Verbatim login prompt present in serial console capture for every deploy-path test in the pre-release run. Telemetry alone does not satisfy this gate. |
| Boot matrix coverage | All covered cells (see boot matrix table) are GREEN: `deployed_verified` + serial console capture with login prompt. YELLOW cells are a blocking failure. |
| E2E: deploy success rate | >= 99% across a 50-node simulated deploy run (50 VMs or 50 sequential runs on available test nodes) |
| E2E: failure recovery | Failed deploy resets IPMI boot device to PXE in all 3 failure-mode scenarios |
| Load test: throughput | Aggregate blob throughput >= 200 MB/s with 20 concurrent clients |
| Stability soak | Zero panics over 24 hours of simulated load |
| Scheduler hook | SLURM drain/resume fires correctly (lab slurmctld, not a mock) |
| First-boot config | chrony.conf, autofs maps, sssd.conf, NetworkManager keyfiles all present and correct on deployed nodes (verified via SSH after `deployed_verified` state is set) |
| node_exporter | Running on :9100 on all deployed test nodes |
| Checklist sign-off | Every item in the ROADMAP.md MVP checklist marked YES by Gilfoyle |

These numbers are not aspirational — they are the bar. If the load test shows 150 MB/s, we fix the bottleneck before shipping. If the deploy success rate is 97% across 50 runs, we find the 1.5 failing cases and fix them. The gate is a gate, not a guideline.
