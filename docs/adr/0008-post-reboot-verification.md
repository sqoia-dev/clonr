# ADR-0008: Post-Reboot Verification (Two-Phase Deploy Success)

**Date:** 2026-04-13
**Status:** Accepted
**Amends:** ADR-0001 (node-scoped token reuse), ADR-0004 (persistence schema extended)
**Last Verified:** 2026-04-15 — applies to clonr main @ 6038544

---

## Context

Sprint 2 exposed a fundamental flaw in the deploy success signal: every "success" recorded in clonr is a pre-reboot callback. The `deploy-complete` event fires from inside the PXE initramfs, after `clonr-static` finishes writing the rootfs to disk and before the node reboots into the deployed OS. `last_deploy_succeeded_at` is set at this point.

This caused hours of false-green debugging during Sprint 2. VM206, VM207, VM201, and VM202 all reported:
- `status=success` in the deploy event
- `last_deploy_succeeded_at` set
- Server UI showing "Proxmox running"

In reality: broken bootloaders, empty `/boot` partitions, missing NVRAM entries. The truth was only visible via manual serial console attachment. The telemetry layer had no visibility past the initramfs handoff.

The `deploy-complete` signal proves that `clonr-static` ran without error. It does not prove:
- The bootloader was written correctly
- The kernel and initramfs are in place
- NVRAM/EFI entries are registered and point to the correct partition
- The OS can execute init, start userspace, and accept logins

We need a second, independent signal that originates from the deployed OS itself.

---

## Decision

### 1. Two-Phase Success Model

The deploy lifecycle is split into two phases with distinct timestamps and distinct semantic meaning. Both must be set before a node is considered `deployed`.

**`deploy_completed_preboot_at`** (renamed from `last_deploy_succeeded_at`)

Set when `clonr-static` finishes its work in the PXE initramfs and POSTs the `deploy-complete` callback. Proves: the rootfs tarball was extracted without error, finalize ran without fatal errors, and `clonr-static` exited cleanly. Does NOT prove the OS boots.

**`deploy_verified_booted_at`** (new field)

Set when the deployed OS makes its own phone-home callback from userspace. Proves: the bootloader executed, the kernel loaded, init started, and the systemd unit responsible for the callback reached the `active` state — which requires login/PAM/getty to be functional or at minimum that the network stack and basic userspace are alive.

**Back-compat:** `last_deploy_succeeded_at` remains as a column alias for `deploy_completed_preboot_at` for one release cycle (v0.x → v1.0). It is removed in v1.0.

### 2. Node State Taxonomy

```
deploying
  → deployed_preboot       (deploy_completed_preboot_at is set; deploy_verified_booted_at is null)
    → deployed_verified    (deploy_verified_booted_at is set within timeout window)
    → deploy_verify_timeout (deploy_verified_booted_at not set within CLONR_VERIFY_TIMEOUT)
  → deploy_failed          (clonr-static reported error; neither timestamp set)
```

A node is NOT in state `deployed_verified` until both timestamps are present. `deployed_preboot` is an intermediate state, not a terminal success state.

### 3. Phone-Home Mechanism — Option A: systemd Oneshot

Three options were considered:

**Option A: systemd oneshot in extracted rootfs** — A oneshot unit, written into the deployed rootfs during the finalize phase by `clonr-static`. On first boot, it POSTs to `/api/v1/nodes/<id>/verify-boot`. On success, it marks itself inactive but remains enabled to update `last_seen_at` on subsequent boots (richer than disabling entirely). **Selected.**

**Option B: persistent clonr-agent** — A lightweight daemon heartbeating every N seconds. Better for continuous health monitoring but substantially more complexity: process management, crash recovery, upgrade path. Out of scope for the current phase. This is the Sprint 2+ target once the basic two-phase model is stable.

**Option C: qemu-guest-agent probes from the server** — Server-initiated, works only on QEMU VMs, fragile on physical hardware. Rejected. clonr targets bare-metal first.

Option A is selected. It requires no persistent daemon, leverages the existing node-scoped API key already minted during PXE boot, and gives us the critical signal (did the OS boot?) with minimal moving parts.

**Phone-home unit behavior:**

The systemd unit (`clonr-verify-boot.service`) is written into the deployed rootfs at finalize time. It is a `oneshot` with `RemainAfterExit=yes`. On each boot it:

1. Reads the node token from `/etc/clonr/node-token` (mode 0600, root only).
2. POSTs to `https://<CLONR_SERVER_URL>/api/v1/nodes/<NODE_ID>/verify-boot` with:
   - `hostname` (from uname or /etc/hostname)
   - `kernel_version` (from uname -r)
   - `uptime_seconds` (from /proc/uptime)
   - `systemd_state` (output of `systemctl is-system-running`; accepted values: `running`, `degraded`)
3. On HTTP 200, exits 0. The server sets `deploy_verified_booted_at` on first call; on subsequent boots it updates `last_seen_at`.
4. On non-200 or network failure, exits non-zero. systemd logs the failure. The server's timeout mechanism handles the consequence (see §4).

The unit runs after `network-online.target` to ensure the management interface is up before the POST.

The `verify-boot` endpoint is authenticated via the node-scoped API key (Bearer token from `/etc/clonr/node-token`). It is the same key minted during PXE enrollment (see ADR-0001). Only the `deploy:node` scope is required — the endpoint rejects admin-scoped keys.

### 4. Timeout Handling

If `deploy_verified_booted_at` is not set within `CLONR_VERIFY_TIMEOUT` minutes after `deploy_completed_preboot_at`, the server:

1. Transitions the node to `deploy_verify_timeout`.
2. Emits a deploy event with `status=verify_timeout` and a human-readable message: "Node did not phone home within N minutes of deploy-complete. Possible causes: bootloader failure, kernel panic, network misconfiguration, or `/etc/clonr/node-token` not written correctly."
3. Does NOT automatically reset the IPMI boot device — operator must inspect via serial console and decide whether to re-deploy or investigate.

**Default:** `CLONR_VERIFY_TIMEOUT=5` (minutes). Configurable via environment variable. Minimum: 2 minutes (to allow slow boot sequences on physical hardware with BIOS POST delays). Maximum: 30 minutes (to catch pathological cases without holding state forever).

The timeout is enforced by a server-side goroutine that runs on a 30-second tick and scans for nodes in `deployed_preboot` state whose `deploy_completed_preboot_at` is older than the timeout window.

### 5. Token Scoping and Security Model

The node token written to `/etc/clonr/node-token` is the same node-scoped API key minted during PXE enrollment (ADR-0001, commit `219ff57`-ish). It is reused post-boot — no new key is minted.

**Write path:** During the finalize phase, `clonr-static` writes the token to the deployed rootfs at `<mountpoint>/etc/clonr/node-token` with permissions `0600`, owner `root:root`. The `/etc/clonr/` directory is created with `0700`.

**Threat model:**

- The token grants `deploy:node` scope only. It cannot reach admin endpoints (image management, other nodes, server config). Compromise of one node's token does not compromise the cluster.
- The token is written to disk in plaintext. This is acceptable: the management network is trusted (provisioning bridge), and the same token is already held in memory by `clonr-static` in the initramfs. An attacker with local root access to the deployed OS can read the token — but at that point they already own the node.
- The token does not expire by default (consistent with ADR-0001's model for node keys). Operators who want rotation can revoke and re-enroll the node.
- The `verify-boot` endpoint is rate-limited: max 1 successful `deploy_verified_booted_at` write per deploy cycle. Subsequent calls only update `last_seen_at`, preventing a compromised node from resetting other nodes' verification state.

### 6. UI Changes

**Nodes list:**
- `deployed_verified` — green badge, "Verified"
- `deployed_preboot` — amber badge, "Deploy unverified" with a tooltip: "clonr-static completed successfully. Waiting for OS boot confirmation."
- `deploy_verify_timeout` — red badge, "Verification timeout" with a link to the node detail page and a "Re-deploy" action.
- `deploy_failed` — red badge, "Deploy failed" (unchanged from current behavior).

**Node detail page:**
- Both timestamps displayed: "Deploy completed (pre-boot)" and "Boot verified". If `deploy_verified_booted_at` is null, show "Pending" with an elapsed time counter.
- Deployment history section shows all deploy cycles with both timestamps per cycle.
- When in `deployed_preboot` state, show a banner: "This node has not confirmed boot. Attach serial console to verify if the deploy is stalled."

### 7. Out of Scope

The following are explicitly deferred to Sprint 2+:

- **Continuous health probes** — periodic heartbeat from a persistent `clonr-agent`. The oneshot approach only fires on (re)boot.
- **Drift detection** — comparing live node state against the deployed image manifest.
- **Node self-patching** — agent-driven package updates or config convergence.
- **IPMI/BMC power cycle on timeout** — automatic remediation. Too risky without operator confirmation for now.

---

## Schema Changes

```sql
-- Rename existing column (migration)
ALTER TABLE nodes RENAME COLUMN last_deploy_succeeded_at TO deploy_completed_preboot_at;

-- New column
ALTER TABLE nodes ADD COLUMN deploy_verified_booted_at TIMESTAMP WITH TIME ZONE;

-- Back-compat view alias (dropped in v1.0 migration)
ALTER TABLE nodes ADD COLUMN last_deploy_succeeded_at TIMESTAMP WITH TIME ZONE
  GENERATED ALWAYS AS (deploy_completed_preboot_at) STORED;
```

Node state is derived from (`deploy_completed_preboot_at`, `deploy_verified_booted_at`, `deploy_failed_at`, `deploy_verify_timeout_at`) rather than stored as a string enum, consistent with ADR-0004's preference for derived state.

---

## Consequences

**Positive:**
- False-green deploys are eliminated as a class. A node cannot be `deployed_verified` unless userspace is running and the network stack is functional.
- The phone-home data (kernel version, uptime, systemd state) gives operators a lightweight health snapshot without a full monitoring stack.
- The amber "unverified" state makes in-progress transitions visible in the UI rather than silently conflating initramfs-success with OS-success.

**Negative:**
- Finalize phase gains a new responsibility: writing the systemd unit and the token into the rootfs. This is a change to `pkg/deploy/finalize.go` — Dinesh owns this implementation.
- Any deploy to an OS that does not use systemd (e.g., an Alpine initrd, a custom embedded image) will never reach `deployed_verified` via this mechanism. Those cases must use Option B (persistent agent) or a custom phone-home script. This is documented in the operator guide, not treated as a bug.
- The `CLONR_VERIFY_TIMEOUT` window must be tuned for the slowest plausible physical hardware POST sequence. The default of 5 minutes covers most commodity server POST times, but operators with older BIOS-based hardware may need to increase it.

---

## Open Questions for Dinesh

1. The systemd unit template — specifically the `ExecStart` script — should use `curl` or a minimal Go binary bundled into the rootfs. `curl` is present on Rocky 9 and most enterprise Linux base images; a Go binary is more portable but adds image size. Recommend `curl` for v0.x, revisit if we need to support images without it.
2. The finalize phase currently writes into the chroot via `InjectFile`. The token value is available in `clonr-static` memory at finalize time (it was used to POST `deploy-complete`). Confirm that passing the token through `FinalizeParams` (or equivalent) is clean and does not require a new IPC path.
3. The `verify-boot` endpoint does not exist yet. It needs to be added to the server API alongside the new DB migration. This is a new handler in `internal/server/` — not in `pkg/pxe/` or `pkg/deploy/`.
