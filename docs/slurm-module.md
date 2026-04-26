# Slurm Module Operator Guide

This document covers the clustr Slurm module from initial enablement through a
working `srun hostname` test. It is the authoritative operator reference for
everything Slurm-related in clustr.

**Contents**

1. [Overview](#1-overview)
2. [Image prerequisites](#2-image-prerequisites)
3. [Enabling the Slurm module](#3-enabling-the-slurm-module)
4. [Controller vs worker roles](#4-controller-vs-worker-roles)
5. [Munge key distribution](#5-munge-key-distribution)
6. [slurm.conf rendering](#6-slurmconf-rendering)
7. [First job submission — `srun hostname` smoke test](#7-first-job-submission--srun-hostname-smoke-test)
8. [Day-2 ops](#8-day-2-ops)
9. [API reference](#9-api-reference)
10. [Troubleshooting](#10-troubleshooting)

---

## 1. Overview

The clustr Slurm module automates the operational chores that make a Slurm
cluster painful to maintain at scale:

- Munge key generation and deploy-time injection into every node's rootfs so
  all nodes share the same key without the operator copying it manually.
- `slurm.conf` generation from the node inventory: node names, CPUs, and
  partition assignments are kept in clustr's DB and rendered into a consistent
  `slurm.conf` on every config push.
- Role assignment (controller vs. worker) stored per-node so a reimage
  automatically installs the right services (`slurmctld` vs. `slurmd`).

When the module is enabled with a `slurm_repo_url`, clustr **auto-installs
Slurm packages** at deploy finalize time on any node that has a Slurm role.
No pre-baking of Slurm into the base image is required — a standard Rocky
Linux 9/10 or Ubuntu image is sufficient. Operators who want faster deploys or
air-gapped operation can still pre-install Slurm in a gold image; clustr
detects the binaries and skips the install step automatically.

---

## 2. Image prerequisites

### Recommended path — any base image, auto-install at deploy time

As of the auto-install feature, **Slurm does not need to be in the base image**
for the default turnkey path. When the Slurm module is enabled and a node has a
Slurm role (`controller` or `worker`), clustr installs Slurm packages from
`slurm_repo_url` automatically during the deploy finalize phase, before
injecting the munge key and writing `slurm.conf`.

You can start with any standard base image (Rocky Linux 9/10, AlmaLinux 9/10,
or Ubuntu 22.04/24.04). The only requirement is that `dnf` (RPM-based) or
`apt` (Debian-based) is available in the rootfs.

**What auto-install does during finalize:**

1. Adds the repository at `slurm_repo_url` to the node's rootfs package manager.
2. Installs: `slurm`, `slurm-slurmd`, `slurm-slurmctld`, `munge`, `munge-libs`.
3. Injects the munge key into `/etc/munge/munge.key`.
4. Writes `/etc/slurm/slurm.conf` and companion config files.
5. Enables `munge`, `slurmd` (workers) or `slurmctld` (controller) in systemd.

If `slurm_repo_url` is unreachable at finalize time, the install step logs a
WARN and the deploy continues — the node boots without Slurm installed. Check
the deploy logs and re-image once the repo is reachable.

**Providing the repo URL:**

Pass `slurm_repo_url` when enabling the module (see §3). The OpenHPC
community repository for EL9 is the recommended starting point:

```
https://repos.openhpc.community/OpenHPC/3/EL_9
```

For EL10 or Ubuntu, substitute the appropriate repo URL from your distro's
Slurm package provider.

### Advanced path — pre-install Slurm in the image (gold image)

For production clusters where deploy speed matters, or where the provisioning
network cannot reach an external repo at finalize time, pre-installing Slurm
in a gold image removes the network dependency and makes every reimage faster.

**When to use this path:**

- Large clusters (100+ nodes) where finalize time per node matters.
- Air-gapped environments with no external repo access.
- Operators who want a fully reproducible, pre-validated binary set.

**How to build a gold image with Slurm pre-installed:**

```bash
# On the clustr-serverd host — drop into an interactive chroot of your base image
clustr shell <image-id>

# Inside the chroot — Rocky Linux 9 example using OpenHPC repo:
dnf install -y https://github.com/openhpc/ohpc/releases/download/v3.0.GA/ohpc-release-3-1.el9.x86_64.rpm
dnf install -y slurm-ohpc slurm-slurmd-ohpc slurm-slurmctld-ohpc munge munge-libs

# Verify the binaries are present
which slurmd slurmctld munge

# Exit the chroot — changes are committed to the image automatically
exit
```

After exiting, the image status returns to `ready`. When this pre-installed
image is deployed, clustr detects that Slurm binaries are already present and
skips the package install step — it proceeds directly to munge key injection
and config file writes.

**Note:** When using a gold image, `slurm_repo_url` is still required at
module enable time but is used only as a fallback for nodes whose image does
not include Slurm. Set it to the correct repo for your distro regardless.

---

## 3. Enabling the Slurm module

### API call to enable

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/modules/slurm/enable \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"cluster_name":"my-hpc","slurm_repo_url":"https://repos.openhpc.community/OpenHPC/3/EL_9"}' \
  | python3 -m json.tool
# Expected: { "status": "ready" }
```

The `cluster_name` is required and is used as the `ClusterName` directive in
`slurm.conf`. The `slurm_repo_url` points to the DNF/YUM repository from which
Slurm packages are installed automatically at node deploy time — see §2 for
the full auto-install flow.

### What happens automatically on enable

1. **Munge key generation.** If no munge key exists in `slurm_secrets`, a
   cryptographically random 32-byte key is generated, base64-encoded, and
   stored encrypted (AES-256-GCM via `CLUSTR_SECRET_KEY`). This key will be
   injected into every node's `/etc/munge/munge.key` at deploy time.
2. **Default config files.** Five default config files are created in the DB:
   `slurm.conf`, `cgroup.conf`, `gres.conf`, `plugstack.conf`, `topology.conf`.
   These are rendered from the current node inventory and can be edited via the
   web UI (Slurm > Configs) or the API.
3. **`cluster_name` and `slurm_repo_url` stored.** The values from the request
   body are persisted in the module config. `cluster_name` is used as the
   `ClusterName` directive in every rendered `slurm.conf`. `slurm_repo_url` is
   used by the auto-install step at deploy finalize time.
4. **Module status set to `enabled`.** The web UI Slurm section becomes active.
   From this point, every node deploy with a Slurm role will auto-install Slurm
   from `slurm_repo_url` (unless binaries are already present in the image).

### Verify the module is enabled

```bash
curl -s http://10.99.0.1:8080/api/v1/modules/slurm/status \
  -H "Authorization: Bearer <your-api-key>" | python3 -m json.tool
# Expected:
# {
#   "enabled": true,
#   "munge_key_present": true,
#   "cluster_name": "my-hpc",
#   "slurm_repo_url": "https://repos.openhpc.community/OpenHPC/3/EL_9"
# }
```

The `munge_key_present` field is `true` once the key has been generated (on
first enable). It becomes `false` only if the `slurm_secrets` table is
manually cleared — under normal operation it stays `true` permanently after
first enable.

---

## 4. Controller vs worker roles

### Role semantics

| Role | Service enabled | Description |
|---|---|---|
| `controller` | `slurmctld` | Runs the Slurm control daemon. Manages the job queue and resource allocation. Only one active controller per cluster (standard HA with a backup controller is a future feature). |
| `worker` | `slurmd` | Runs on every compute node. Executes jobs and reports status to the controller. |
| `none` (default) | neither | Node participates in clustr management but is not part of the Slurm cluster. Slurm config files are not injected during finalize. |

### Assigning a controller

A common topology is to designate the clustr-serverd host itself as the
controller (if it has spare compute capacity) so the provisioning server also
runs `slurmctld`. Alternatively, any compute node can be the controller.

```bash
# Assign a node as the Slurm controller
NODE_ID="<node-id>"

curl -s -X PUT http://10.99.0.1:8080/api/v1/slurm/roles/${NODE_ID} \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"role": "controller"}' | python3 -m json.tool
```

Or in the web UI: **Nodes > select node > Slurm tab > Role > Controller**.

### Assigning workers

```bash
# Assign multiple nodes as workers (repeat for each)
curl -s -X PUT http://10.99.0.1:8080/api/v1/slurm/roles/${NODE_ID} \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"role": "worker"}' | python3 -m json.tool
```

### Topology options

**Option 1 — clustr host as controller + N compute workers**

The provisioning server runs `slurmctld`. Compute nodes are workers.
This works well for small clusters (<50 nodes) where the provisioning host
has adequate memory (slurmctld uses ~200 MB per 1000 nodes).

```
clustr-serverd host: role=controller  (slurmctld runs here)
compute-001 .. compute-N: role=worker (slurmd runs here)
```

**Option 2 — Dedicated controller node + N workers**

One compute node is designated the controller. This is appropriate for large
clusters where the provisioning host should not run cluster services.

```
compute-001: role=controller
compute-002 .. compute-N: role=worker
```

After setting roles, regenerate the `slurm.conf` (see §6) before the next
reimage.

---

## 5. Munge key distribution

### How it works

Munge is the authentication layer that Slurm daemons use to verify each other.
All nodes in the cluster must share the same munge key. Munge keys are secret —
any node with the key can authenticate as any other node.

clustr handles this automatically:

1. The munge key is generated when the module is first enabled (§3) and stored
   encrypted in the `slurm_secrets` table.
2. At deploy finalize time, the key is decrypted and written to
   `/etc/munge/munge.key` in the node's rootfs with the correct permissions
   (`chmod 400`, owned by the `munge` user).
3. The `munge` service is enabled in the rootfs so it starts on first boot.

### What the operator needs to verify

After a reimage completes, SSH into the node and verify:

```bash
# Munge key is present and has correct permissions
ls -la /etc/munge/munge.key
# Expected: -r-------- 1 munge munge ... /etc/munge/munge.key

# Munge service is running
systemctl status munge
# Expected: active (running)

# Munge can authenticate successfully (round-trip encode + decode)
munge -n | unmunge
# Expected: STATUS: Success (0)
```

If munge fails to authenticate, check:
- All nodes have the same key (compare `sha256sum /etc/munge/munge.key` across nodes).
- The `munge` user exists in the image: `id munge`.
- The munge service is not masked: `systemctl is-enabled munge`.

### Rotating the munge key

To rotate the munge key across the cluster:

```bash
# Generate a new key (server side)
curl -s -X POST http://10.99.0.1:8080/api/v1/slurm/munge/rotate \
  -H "Authorization: Bearer <your-api-key>" | python3 -m json.tool

# Then reimage all nodes — the new key is injected at finalize time
# Use the bulk reimage feature (docs/install.md §9) for the full cluster
```

Nodes with the old key cannot authenticate to nodes with the new key until they
are reimaged. Schedule the rotation during a maintenance window when no jobs are
running.

---

## 6. slurm.conf rendering

### How clustr generates slurm.conf

When you push a config sync (or trigger a reimage), clustr renders `slurm.conf`
from:

1. The node inventory in the DB (hostnames, CPU counts from hardware profiles,
   role assignments from §4).
2. The partition definitions stored in the Slurm module config.
3. The controller assignment (sets `SlurmctldHost=<controller-hostname>`).

The rendered `slurm.conf` is stored in the DB and injected into
`/etc/slurm/slurm.conf` on every node during the finalize phase.

### Editing slurm.conf

In the web UI: **Slurm > Configs > slurm.conf**. The editor shows the current
rendered config. You can override any field directly.

Via API:

```bash
# Get current slurm.conf
curl -s http://10.99.0.1:8080/api/v1/slurm/configs/slurm.conf \
  -H "Authorization: Bearer <your-api-key>"

# Update slurm.conf (replace the entire content)
curl -s -X PUT http://10.99.0.1:8080/api/v1/slurm/configs/slurm.conf \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: text/plain" \
  --data-binary @/path/to/your/slurm.conf
```

### Pushing a config change without reimaging

If you need to push an updated `slurm.conf` to running nodes without a full
reimage, use the sync endpoint:

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/slurm/sync \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"node_ids": ["<node-id-1>", "<node-id-2>"]}' | python3 -m json.tool
```

This pushes the current `slurm.conf` to the specified nodes via `clustr-clientd`
(the per-node agent that maintains a persistent connection to the server). The
`slurmctld`/`slurmd` processes are reloaded (`scontrol reconfigure`) after the
file is written.

**Note:** `clustr-clientd` must be connected on the target nodes for live sync
to work. Nodes that are offline will receive the updated config on their next
reimage.

### Key slurm.conf parameters set by clustr

| Parameter | Source | Notes |
|---|---|---|
| `SlurmctldHost` | Controller node hostname from role assignment | Set automatically when a controller is assigned |
| `NodeName` lines | All worker nodes in the Slurm module, with CPU counts from hardware profiles | Updated on config re-render |
| `PartitionName` | Partition definitions in the module config | Defaults to a single `compute` partition |
| `StateSaveLocation` | `/var/spool/slurmctld` | Default; override in the config editor |
| `SlurmdSpoolDir` | `/var/spool/slurmd` | Default per node |
| `AuthType` | `auth/munge` | Fixed — munge is the only supported auth type |

---

## 7. First job submission — `srun hostname` smoke test

With auto-install enabled, this smoke test works on a fresh Rocky Linux 9 or
10 base image with no Slurm pre-baked. After a single reimage of your
controller and worker nodes, `srun hostname` should succeed end-to-end.

### Prerequisites checklist

Before attempting job submission, verify all of the following:

- [ ] Slurm module is enabled (`munge_key_present: true` in status response)
- [ ] `slurm_repo_url` is set and reachable from the provisioning host
- [ ] At least one node has `role=controller` and at least one has `role=worker`
- [ ] All nodes have been reimaged after the module was enabled (auto-install, munge key, and `slurm.conf` are injected at that reimage)
- [ ] All nodes: `systemctl status munge` shows `active (running)`
- [ ] Controller node: `systemctl status slurmctld` shows `active (running)`
- [ ] Worker nodes: `systemctl status slurmd` shows `active (running)`
- [ ] Munge authentication works: `munge -n | unmunge` returns `STATUS: Success (0)` on every node

### Run the smoke test

From the controller node (or any node with Slurm client binaries installed):

```bash
# List all nodes — should show worker nodes in "idle" state
sinfo

# Run a simple job across all available nodes
srun --nodes=1 --ntasks=1 hostname
# Expected: compute-001 (or whatever your worker hostname is)

# Run across multiple nodes
srun --nodes=2 --ntasks=2 hostname
# Expected: two lines, one per worker node

# Interactive job
srun --pty /bin/bash
```

### Expected `sinfo` output (healthy cluster)

```
PARTITION AVAIL  TIMELIMIT  NODES  STATE NODELIST
compute*     up   infinite      2   idle compute-[001-002]
```

If nodes show `down` or `drain`, check `scontrol show node <hostname>` for
the reason string.

### What to check if `srun` hangs

1. **Munge authentication failure.** Run `munge -n | unmunge` from the submit
   host. If this fails, the munge key is wrong or the munge service is not
   running. Fix and restart munge before retrying.

2. **slurmctld not reachable.** From a worker, verify the controller is
   reachable: `ping <controller-hostname>` and `telnet <controller-hostname>
   6817` (the default `SlurmctldPort`). Check firewall rules if the port is
   blocked.

3. **slurmd not registered.** On the controller: `scontrol show node
   <worker-hostname>`. If the node is not listed, check that `slurmd` is
   running on the worker and that `NodeName` in `slurm.conf` matches the node's
   actual hostname.

4. **Wrong hostname in slurm.conf.** The `NodeName` parameter must match the
   output of `hostname -s` on the worker node. If they differ (e.g., the node
   booted with a different hostname than configured in clustr), update the node
   config and trigger a reimage.

---

## 8. Day-2 ops

### Adding a node to the cluster

1. Register the new node in clustr (`POST /api/v1/nodes`) with correct MAC,
   hostname, and `ssh_keys`. See [docs/install.md §8](install.md#8-registering-nodes).
2. Assign the Slurm role: `PUT /api/v1/slurm/roles/<new-node-id>` with
   `{"role": "worker"}`.
3. Update the NodeGroup membership if you use group-based cluster management.
4. Re-render `slurm.conf` so the new `NodeName` line is included:
   `POST /api/v1/slurm/sync` or let the next reimage pick it up.
5. Trigger a reimage on the new node.
6. Once `verified_booted`, verify `slurmd` is running and the node appears in
   `sinfo` as `idle`.

### Removing a node from the cluster

1. Drain the node so no new jobs are scheduled: `scontrol update
   NodeName=<hostname> State=drain Reason="decommission"`.
2. Wait for running jobs to complete, or cancel them: `squeue -w <hostname>`.
3. In clustr, set the Slurm role to `none`: `PUT /api/v1/slurm/roles/<node-id>`
   with `{"role": "none"}`.
4. Re-render and sync `slurm.conf` to remove the node from `NodeName` lines.
5. Optionally delete the node record from clustr: `DELETE /api/v1/nodes/<node-id>`.

### Upgrading Slurm

**Auto-install path:** Update `slurm_repo_url` (via `POST
/api/v1/modules/slurm/enable` with the new repo URL, or through Settings >
Slurm > Module Config in the web UI) to point to the repository containing the
new Slurm version. Then reimage all nodes — the updated packages are installed
automatically at finalize time. No image rebuild required.

**Gold image path:** Slurm upgrades require replacing the binaries in the base
image. The steps are:

1. Create a new image version with the updated Slurm RPMs installed (use
   `clustr shell <image-id>` to chroot into a copy of the current image and
   upgrade, or pull a new image with the updated packages pre-installed).
2. Update the node configs to point to the new `base_image_id`.
3. Drain all nodes before reimaging: `scontrol update NodeName=ALL State=drain
   Reason="slurm-upgrade"`.
4. Stop jobs (or wait for them to finish).
5. Stop the controller: `systemctl stop slurmctld` on the controller node.
6. Bulk reimage all nodes using the new image (see [docs/install.md §9](install.md#9-reimaging-multiple-nodes)).
7. Verify `slurmd` and `slurmctld` restart cleanly on the new version.
8. Undrain nodes: `scontrol update NodeName=ALL State=idle`.

**Slurm upgrade compatibility:** Slurm requires that `slurmctld` and `slurmd`
versions match within one minor version. Do not mix major versions across
nodes. Upgrade the controller and all workers in the same maintenance window.

---

## 9. API reference

All Slurm API routes require an admin-scoped Bearer token.

### Module status

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/modules/slurm/status` | Returns module enabled state, munge key presence, `cluster_name`, `slurm_repo_url`, and config summary |
| `POST` | `/api/v1/modules/slurm/enable` | Enable the module. Body: `{"cluster_name":"…","slurm_repo_url":"…"}`. Generates munge key if absent. Returns `{"status":"ready"}`. |
| `POST` | `/api/v1/modules/slurm/disable` | Disable the module. Stops munge key injection, Slurm config writes, and auto-install at finalize. Does not delete existing keys or configs. |

### Config management

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/slurm/configs` | List all config files stored in the DB (name, last modified) |
| `GET` | `/api/v1/slurm/configs/{name}` | Get a specific config file content (`slurm.conf`, `cgroup.conf`, etc.) |
| `PUT` | `/api/v1/slurm/configs/{name}` | Replace a config file with new content. Takes `text/plain` body. |

### Node roles

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/slurm/nodes` | List all nodes with their Slurm role assignments |
| `GET` | `/api/v1/slurm/roles` | List all role assignments (node ID → role mapping) |
| `PUT` | `/api/v1/slurm/roles/{node_id}` | Set the Slurm role for a node. Body: `{"role": "controller"/"worker"/"none"}` |

### Config sync

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/slurm/sync` | Push current Slurm configs to running nodes via `clustr-clientd`. Body: `{"node_ids": [...]}`. If `node_ids` is empty, syncs all connected nodes. |

### Builds and upgrades

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/slurm/builds` | List available Slurm build definitions (for the build-from-source pipeline) |
| `GET` | `/api/v1/slurm/upgrades` | List pending or completed Slurm upgrade jobs |

### Secrets

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/slurm/munge/rotate` | Generate a new munge key and store it. The old key is discarded. Nodes must be reimaged to pick up the new key. |

---

## 10. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Slurm module enabled but `slurmd` not installed after reimage | `slurm_repo_url` was unreachable at finalize time (non-fatal WARN — deploy continued, Slurm skipped) | Check deploy logs for the WARN line. Verify the URL is accessible: `curl -I <slurm_repo_url>` from the clustr-serverd host. Fix network/firewall/DNS, then reimage. |
| `slurmd.service` degraded after reimage (advanced/gold-image path) | Slurm binaries missing from the pre-built image | Either switch to the recommended auto-install path (set `slurm_repo_url`, reimage), or re-bake the gold image with Slurm installed. See §2. |
| `munge -n \| unmunge` fails with `STATUS: Socket communication error` | munge service not running | `systemctl start munge`. If it fails to start, check `journalctl -u munge` for the reason. |
| `munge -n \| unmunge` fails with `STATUS: Invalid credential` | munge key mismatch — node has a different key than the server | Reimage the node. The correct key will be injected at finalize time. |
| `sinfo` shows all nodes as `down` | slurmctld not reachable, or node hostnames do not match `slurm.conf` | Check `slurmctld` is running on the controller. Verify `NodeName` lines in `slurm.conf` match actual node hostnames. |
| `srun` hangs indefinitely | Port 6817 (slurmctld) or 6818 (slurmd) blocked by firewall | Open the Slurm ports on the provisioning network: `firewall-cmd --add-port=6817-6818/tcp --permanent && firewall-cmd --reload`. |
| `POST /api/v1/modules/slurm/enable` returns 500 | `CLUSTR_SECRET_KEY` not set — munge key cannot be encrypted | Set `CLUSTR_SECRET_KEY` in `secrets.env` and restart the server. |
| Slurm configs not written to `/etc/slurm/` after reimage | Module was not enabled before the reimage was triggered | Enable the module (`POST /api/v1/modules/slurm/enable`), then reimage the nodes. |
| `GET /api/v1/slurm/nodes` returns 404 | Routes not yet implemented in this build | Check the server version. This route is planned for Sprint 4+ builds. In the interim, use `GET /api/v1/slurm/roles` to see role assignments. |
