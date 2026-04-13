# clonr Validation and Testing Strategy

**Date:** 2026-04-13
**Scope:** Defines what "works, testable, validated" means for clonr, and how we get there before v1.0 ships.

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
5. Assertions: deploy event posted to server with `status=success`; node reboots into deployed OS; SSH responds on management IP; /etc/hostname matches NodeConfig; chrony is synchronized; node_exporter is reachable on :9100.
6. Trigger a second deploy (different image version) via the rolling deploy API.
7. Assertions: SLURM drain event fired (if slurmctld is running in the lab); second deploy completes; node reboots into second image; SLURM resume fired.

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

**Nightly:**
- All integration tests
- E2E golden path
- E2E failure-mode tests
- 24-hour server stability soak: run clonr-serverd with simulated load (repeated MAC lookups, blob downloads, deploy events) for 24 hours; assert zero panics, memory usage stable within 10% over the run

**Pre-release (before cutting a tag):**
- All nightly tests, 3 consecutive passing runs of E2E golden path
- Concurrent load test: aggregate throughput gate
- Manual checklist walk-through of every MVP checklist item in ROADMAP.md, signed off by Gilfoyle

---

## Release Gates for v1.0

The following must all be green before `git tag v1.0.0` is pushed. No exceptions.

| Gate | Pass criterion |
|------|---------------|
| Unit test coverage | pkg/hardware >= 90%, pkg/deploy >= 85%, pkg/server/middleware >= 85% |
| Integration: auth scope | node-scoped key gets 403 on admin endpoints in automated test |
| Integration: encryption | No plaintext BMC credentials in SQLite after migration (grep-verifiable) |
| Integration: disk selection | Preflight selects correct disk on multi-disk test node, rejects wrong-type disk |
| E2E: golden path | 3 consecutive passing runs on Proxmox lab |
| E2E: deploy success rate | >= 99% across a 50-node simulated deploy run (50 VMs or 50 sequential runs on available test nodes) |
| E2E: failure recovery | Failed deploy resets IPMI boot device to PXE in all 3 failure-mode scenarios |
| Load test: throughput | Aggregate blob throughput >= 200 MB/s with 20 concurrent clients |
| Stability soak | Zero panics over 24 hours of simulated load |
| Scheduler hook | SLURM drain/resume fires correctly (lab slurmctld, not a mock) |
| First-boot config | chrony.conf, autofs maps, sssd.conf, NetworkManager keyfiles all present and correct on deployed nodes (verified via SSH) |
| node_exporter | Running on :9100 on all deployed test nodes |
| Checklist sign-off | Every item in the ROADMAP.md MVP checklist marked YES by Gilfoyle |

These numbers are not aspirational — they are the bar. If the load test shows 150 MB/s, we fix the bottleneck before shipping. If the deploy success rate is 97% across 50 runs, we find the 1.5 failing cases and fix them. The gate is a gate, not a guideline.
