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

> **User accounts required before jobs will run.** The Slurm daemon users
> (`slurm`, `munge`) are created automatically by the package installer. Human
> users who actually submit jobs must be provisioned on every node with
> consistent UIDs and GIDs before `srun` will work. See
> [docs/user-management.md](user-management.md) for the three approaches (local
> sysaccounts, clustr LDAP, external LDAP) and a full smoke test.

The clustr Slurm module automates the operational chores that make a Slurm
cluster painful to maintain at scale:

- Munge key generation and deploy-time injection into every node's rootfs so
  all nodes share the same key without the operator copying it manually.
- `slurm.conf` generation from the node inventory: node names, CPUs, and
  partition assignments are kept in clustr's DB and rendered into a consistent
  `slurm.conf` on every config push.
- Role assignment (controller vs. worker) stored per-node so a reimage
  automatically installs the right services (`slurmctld` vs. `slurmd`).

clustr **ships Slurm built-in**. When a node is deployed with a Slurm role,
clustr installs Slurm packages from the clustr-server's own bundled package
repository at `http://<clustr-server>:<port>/repo/el9-x86_64/`. No external
repo URL is needed, no internet access from deployed nodes is required, and
every RPM is GPG-verified against the clustr release key before installation.

Operators who want faster deploys can still pre-install Slurm in a gold image;
clustr detects the binaries and skips the install step automatically.
Operators who need a custom Slurm build can override the repo URL — see §2.1.

---

## 2. Image prerequisites

### Recommended path — bundled repo, auto-install at deploy time

**Slurm does not need to be in the base image.** When the Slurm module is
enabled and a node has a Slurm role (`controller` or `worker`), clustr installs
Slurm packages from its own bundled repository during the deploy finalize phase,
before injecting the munge key and writing `slurm.conf`.

The bundled repo is served by the clustr-server itself at
`http://<clustr-server>:<port>/repo/el9-x86_64/`. Deployed nodes never reach
GitHub, OpenHPC, or schedmd.com — they only talk to the clustr-server, which is
already required for everything else. This means:

- **Air-gap friendly by default.** No external internet required at deploy time.
- **GPG-verified installs.** Every RPM is signed with the clustr release key.
  `gpgcheck=1` is enforced on both repo stanzas in the generated `.repo` file
  (see §2 repo format below); all three keys are injected into the chroot at
  `/etc/pki/rpm-gpg/` before `dnf` runs (GAP-17 hardening, `clustr5`+).
- **Zero operator configuration.** Leave `slurm_repo_url` empty (or set to
  `"clustr-builtin"`) to use the bundled repo. That is the default.

**What auto-install does during finalize (GAP-17 hardened path, clustr5+):**

1. Writes all three GPG keys into the chroot at `/etc/pki/rpm-gpg/`:
   `RPM-GPG-KEY-clustr`, `RPM-GPG-KEY-rocky-9`, `RPM-GPG-KEY-EPEL-9`.
2. Writes `/etc/yum.repos.d/clustr-slurm.repo` as a two-stanza file:
   - `[clustr-slurm]` pointing at `http://<clustr-server>:<port>/repo/el9-x86_64/`
     with `gpgcheck=1` against the clustr key.
   - `[clustr-slurm-deps]` pointing at `http://<clustr-server>:<port>/repo/el9-x86_64-deps/`
     with `gpgcheck=1` against the Rocky 9 and EPEL 9 keys.
3. Installs: `slurm`, `slurm-slurmd` or `slurm-slurmctld` (by role), `munge`.
4. Injects the munge key into `/etc/munge/munge.key`.
5. Writes `/etc/slurm/slurm.conf` and companion config files.
6. Enables `munge`, `slurmd` (workers) or `slurmctld` (controller) in systemd.

If the clustr-server's `/repo/` endpoint is unreachable at finalize time, the
install step logs a WARN and the deploy continues — the node boots without Slurm
installed. This should only happen if the management network is misconfigured.

**Enabling the module (bundled repo — the default):**

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/slurm/enable \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"cluster_name":"my-hpc"}'
# slurm_repo_url is omitted — defaults to "clustr-builtin"
```

Or explicitly:

```bash
  -d '{"cluster_name":"my-hpc","slurm_repo_url":"clustr-builtin"}'
```

### 2.1 — `slurm_repo_url` — bundled repo vs. custom override

| Value | Behaviour |
|---|---|
| `""` (empty) or `"clustr-builtin"` | **Default.** Use the clustr-server's own `/repo/el9-x86_64/`. gpgcheck=1 with the embedded clustr key. Requires the bundled repo to be installed (`clustr-serverd bundle install`). |
| Any other URL | **Operator override.** Use the provided URL verbatim. gpgcheck=0. The operator owns GPG trust for custom repos. |

The `"clustr-builtin"` sentinel value is stored in the database. It is
**irreversible** — once a DB row contains this value, renaming it requires a
migration. Do not rename the sentinel.

**When to use a custom `slurm_repo_url`:**

- You have a private internal yum mirror and want nodes to pull from it.
- You are testing a specific Slurm build that is not yet in the clustr bundle.
- You are running EL10 nodes before the clustr EL10 bundle is available.

For a custom URL, set `gpgcheck` behaviour in your own repo definition, not via
clustr (clustr uses `gpgcheck=0` for all non-builtin URLs).

**Example — custom override:**

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/slurm/enable \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"cluster_name":"my-hpc","slurm_repo_url":"http://mirror.example.com/slurm/el9/"}'
```

**Switching an existing installation to the bundled repo:**

If you previously enabled the module with an OpenHPC URL, you can switch to the
bundled repo by re-enabling with the sentinel:

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/slurm/enable \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"cluster_name":"my-hpc","slurm_repo_url":"clustr-builtin"}'
```

This updates the stored value. The next node deploy will use the bundled repo.
Previously-deployed nodes are unaffected until they are re-imaged.

### The bundled Slurm repo — how it works

The clustr-server binary includes a reference to the bundled Slurm version
(e.g. `v24.11.4-clustr1`). At server install time, the installer fetches the
signed bundle from GitHub Releases and unpacks it to `/var/lib/clustr/repo/`.
The server then serves the directory at `/repo/*` — a standard `http.FileServer`
mount. Deployed nodes see a yum repo at `http://<clustr-server>:<port>/repo/el9-x86_64/`
that is entirely local to your management network.

**To manually install or update the bundle (air-gap or first-time):**

```bash
# Fetch from GitHub automatically (requires outbound HTTPS on the clustr-server host):
clustr-serverd bundle install --from-release

# Or side-load from a local file (air-gap path):
clustr-serverd bundle install --from-file /path/to/clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz
```

**To verify the bundle is installed:**

```bash
clustr-serverd bundle list
# Expected output (clustr5+):
# installed: v24.11.4-clustr5  path: /var/lib/clustr/repo/
#   primary:  el9-x86_64/       (clustr-built RPMs)
#   deps:     el9-x86_64-deps/  (Rocky/EPEL passthrough RPMs)

curl http://10.99.0.1:8080/repo/el9-x86_64/repodata/repomd.xml
# Expected: 200 OK with XML content

curl http://10.99.0.1:8080/repo/el9-x86_64-deps/repodata/repomd.xml
# Expected: 200 OK with XML content (clustr5+ two-subdir layout)

curl http://10.99.0.1:8080/repo/health
# Expected: JSON with both subdirs listed
```

### Advanced path — pre-install Slurm in the image (gold image)

For production clusters where deploy speed matters, pre-installing Slurm in a
gold image skips the dnf install step at deploy time.

**When to use this path:**

- Large clusters (100+ nodes) where finalize time per node matters.
- You want a fully reproducible, pre-validated binary set.

**How to build a gold image with Slurm pre-installed (using the bundled repo):**

```bash
# On the clustr-serverd host — ensure the bundled repo is installed first
clustr-serverd bundle list  # should show v24.11.4-clustr1

# Drop into an interactive chroot of your base image
clustr shell <image-id>

# Inside the chroot — configure the clustr repos and install Slurm.
# Two stanzas are required (GAP-17 hardening, clustr5+): one for clustr-built
# RPMs (slurm-*, libjwt) and one for Rocky/EPEL dep RPMs (munge, pkgconf, etc).
cat > /etc/yum.repos.d/clustr-slurm.repo <<EOF
[clustr-slurm]
name=clustr Slurm (built and signed by clustr)
baseurl=http://10.99.0.1:8080/repo/el9-x86_64/
enabled=1
gpgcheck=1
repo_gpgcheck=0
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-clustr

[clustr-slurm-deps]
name=clustr Slurm runtime deps (mirrored from Rocky/EPEL, signed upstream)
baseurl=http://10.99.0.1:8080/repo/el9-x86_64-deps/
enabled=1
gpgcheck=1
repo_gpgcheck=0
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-rocky-9 file:///etc/pki/rpm-gpg/RPM-GPG-KEY-EPEL-9
EOF
# All three key files are injected automatically by clustr at deploy time.
# For a manual gold-image build, copy all three from the clustr-server:
# curl http://10.99.0.1:8080/repo/RPM-GPG-KEY-clustr  -o /etc/pki/rpm-gpg/RPM-GPG-KEY-clustr
# curl http://10.99.0.1:8080/repo/RPM-GPG-KEY-rocky-9 -o /etc/pki/rpm-gpg/RPM-GPG-KEY-rocky-9
# curl http://10.99.0.1:8080/repo/RPM-GPG-KEY-EPEL-9  -o /etc/pki/rpm-gpg/RPM-GPG-KEY-EPEL-9

dnf install -y slurm slurm-slurmd slurm-slurmctld munge

# Verify
which slurmd slurmctld munge

# Exit the chroot
exit
```

When this pre-installed image is deployed, clustr detects that Slurm binaries
are already present and skips the package install step.

---

## 3. Enabling the Slurm module

### API call to enable

**Default (bundled repo):**

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/slurm/enable \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"cluster_name":"my-hpc"}' \
  | python3 -m json.tool
# Expected: { "status": "ready" }
```

Omitting `slurm_repo_url` (or setting it to `"clustr-builtin"`) uses the
clustr-server's own bundled Slurm repo — no external URL needed. See §2 for
what this means for the deploy flow.

**Custom repo override (advanced):**

```bash
  -d '{"cluster_name":"my-hpc","slurm_repo_url":"http://mirror.example.com/slurm/el9/"}'
```

See §2.1 for the full override documentation.

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
   `ClusterName` directive in every rendered `slurm.conf`. `slurm_repo_url`
   (empty or `"clustr-builtin"`) is resolved at deploy time to the server's
   `/repo/el9-x86_64/` URL.
4. **Module status set to `enabled`.** The web UI Slurm section becomes active.
   From this point, every node deploy with a Slurm role will auto-install Slurm
   from the bundled repo (unless binaries are already present in the image).

### Verify the module is enabled

```bash
curl -s http://10.99.0.1:8080/api/v1/slurm/status \
  -H "Authorization: Bearer <your-api-key>" | python3 -m json.tool
# Expected:
# {
#   "enabled": true,
#   "status": "ready",
#   "munge_key_present": true,
#   "cluster_name": "my-hpc",
#   "slurm_repo_url": "",
#   "managed_files": ["slurm.conf", "gres.conf", "cgroup.conf", "topology.conf", "plugstack.conf", "slurmdbd.conf"],
#   "connected_nodes": []
# }
# Note: slurm_repo_url is "" or "clustr-builtin" for the default bundled-repo path.
# The actual resolved URL (http://<server>/repo/el9-x86_64/) is computed at deploy time.
```

The `munge_key_present` field is `true` once the key has been generated (on
first enable). It becomes `false` only if the `slurm_secrets` table is
manually cleared — under normal operation it stays `true` permanently after
first enable.

**Note on paths:** All Slurm API routes use the `/api/v1/slurm/` prefix. The older `/api/v1/modules/slurm/` prefix documented in earlier builds does not exist and returns 404. Use `POST /api/v1/slurm/disable` to disable the module.

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

curl -s -X PUT http://10.99.0.1:8080/api/v1/nodes/${NODE_ID}/slurm/role \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"roles": ["controller"]}' | python3 -m json.tool
# Expected: { "status": "ok" }
```

Or in the web UI: **Nodes > select node > Slurm tab > Role > Controller**.

**Important:** The body field is `roles` (plural, array), not `role` (singular string).
The API accepts multiple roles per node (e.g., `["controller", "dbd"]`), though a single
`controller` or `worker` role is the standard topology.

### Assigning workers

```bash
# Assign a node as a Slurm worker (repeat for each compute node)
curl -s -X PUT http://10.99.0.1:8080/api/v1/nodes/${NODE_ID}/slurm/role \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"roles": ["worker"]}' | python3 -m json.tool
# Expected: { "status": "ok" }
```

### Verify role assignments

```bash
# Get role for a specific node
curl -s http://10.99.0.1:8080/api/v1/nodes/${NODE_ID}/slurm/role \
  -H "Authorization: Bearer <your-api-key>"
# Expected: { "node_id": "...", "roles": ["controller"] }

# List all nodes with Slurm roles
curl -s http://10.99.0.1:8080/api/v1/slurm/nodes \
  -H "Authorization: Bearer <your-api-key>"
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

### Role assignment — default and opt-out (D17)

**Small-cluster default:** When you assign the `controller` role to a node, the
recommended starting point is to assign **both** `controller` and `worker`
(compute). This lets the controller node also run `slurmd`, so a 1+1 lab
topology (1 controller + 1 compute) can satisfy `srun -N2` without a third VM.

```bash
# Dual-role controller (small cluster / lab — the recommended starting point)
curl -s -X PUT http://10.99.0.1:8080/api/v1/nodes/${NODE_ID}/slurm/role \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"roles": ["controller", "worker"]}' | python3 -m json.tool
```

**Production opt-out:** For clusters where controller resource contention is a
concern, remove `worker` from the controller's role list:

```bash
# Controller-only (production / large clusters)
curl -s -X PUT http://10.99.0.1:8080/api/v1/nodes/${NODE_ID}/slurm/role \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"roles": ["controller"]}' | python3 -m json.tool
```

The server never overrides operator intent — the role list you PUT is the role
list that takes effect. The dual-role recommendation is a starting-point default
documented here, not a server-side enforcement.

> **Note on role string aliases:** The API accepts both `"worker"` (canonical
> value stored in the DB) and `"compute"` (accepted as a backward-compatible
> alias) anywhere a compute role is expected. New deployments should use
> `"worker"`. The `"compute"` string may be removed in v1.1.

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
curl -s -X POST http://10.99.0.1:8080/api/v1/slurm/munge-key/rotate \
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
- [ ] Human users are provisioned on all nodes with consistent UIDs/GIDs — see [docs/user-management.md](user-management.md). For a quick first test, run the smoke test below as `root` (root always exists on all nodes); switch to a real user for production validation.

**Note on the smoke test below:** Running `srun hostname` as `root` from the controller is sufficient to verify that Slurm, munge, and the network are functioning. It does not validate user provisioning. After confirming the cluster is functional as root, follow [docs/user-management.md §6](user-management.md#6-smoke-test--submit-a-job-as-alice) to run the same job as a real user (`alice`) and validate the full UID mapping pipeline.

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
| `GET` | `/api/v1/slurm/status` | Returns module enabled state, munge key presence, `cluster_name`, `slurm_repo_url`, managed files, and connected nodes |
| `POST` | `/api/v1/slurm/enable` | Enable the module. Body: `{"cluster_name":"…","slurm_repo_url":"…"}`. Generates munge key if absent. Returns `{"status":"ready"}`. |
| `POST` | `/api/v1/slurm/disable` | Disable the module. Stops munge key injection, Slurm config writes, and auto-install at finalize. Does not delete existing keys or configs. |

### Config management

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/slurm/configs` | List all config files stored in the DB (name, last modified) |
| `GET` | `/api/v1/slurm/configs/{name}` | Get a specific config file content (`slurm.conf`, `cgroup.conf`, etc.). Returns JSON with `content` field. |
| `PUT` | `/api/v1/slurm/configs/{name}` | Save a new version of a config file. Body: `{"content": "<full file content>", "message": "<optional commit message>"}`. Returns `{"filename":"…","version":<n>}`. |
| `GET` | `/api/v1/slurm/configs/{name}/history` | List all saved versions of a config file. |
| `GET` | `/api/v1/slurm/configs/{name}/render/{node_id}` | Preview rendered config for a specific node. |

### Node roles

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/slurm/nodes` | List all nodes with their Slurm role assignments and connection state |
| `GET` | `/api/v1/slurm/roles` | List the available Slurm role strings (`controller`, `worker`, `dbd`, `login`) |
| `GET` | `/api/v1/nodes/{node_id}/slurm/role` | Get the Slurm role for a specific node. Returns `{"node_id":"…","roles":[…]}` |
| `PUT` | `/api/v1/nodes/{node_id}/slurm/role` | Set the Slurm role for a node. Body: `{"roles": ["controller"]}` or `{"roles": ["worker"]}`. Note: `roles` is a plural array, not a singular string. |

### Config sync

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/slurm/push` | Push current Slurm configs to running nodes via `clustr-clientd`. Body: `{"node_ids": [...]}`. If `node_ids` is empty, pushes to all connected nodes. |
| `GET` | `/api/v1/slurm/sync-status` | Get the current sync state across all nodes. |
| `GET` | `/api/v1/nodes/{node_id}/slurm/sync-status` | Get the Slurm sync state for a specific node. |

### Config overrides

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/nodes/{node_id}/slurm/overrides` | Get per-node Slurm config overrides |
| `PUT` | `/api/v1/nodes/{node_id}/slurm/overrides` | Set per-node Slurm config overrides |

### Builds and upgrades

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/slurm/builds` | List available Slurm build definitions (for the build-from-source pipeline) |
| `POST` | `/api/v1/slurm/builds` | Start a new Slurm build |
| `GET` | `/api/v1/slurm/upgrades` | List pending or completed Slurm upgrade jobs |

### Secrets

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/slurm/munge-key/generate` | Generate a new munge key and store it (idempotent if key already exists). |
| `POST` | `/api/v1/slurm/munge-key/rotate` | Rotate the munge key — generates a new key, discarding the old one. Nodes must be reimaged to pick up the new key. |

---

## 10. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Slurm not installed on deployed node (binaries absent, `slurmd`/`slurmctld` missing) | EL version mismatch between `slurm_repo_url` and the base image — `dnf` silently found no matching packages | Verify `slurm_repo_url` matches the image's EL major version (see §2.1). Use `curl -I <slurm_repo_url>` from the server to confirm reachability. Correct the URL, re-enable the module with the right URL, and reimage. After server-side EL validation lands (see §2.1 coordination note), the module-enable call will reject mismatched URLs at configuration time. |
| Slurm module enabled but `slurmd` not installed after reimage | `slurm_repo_url` was unreachable at finalize time (non-fatal WARN — deploy continued, Slurm skipped) | Check deploy logs for the WARN line. Verify the URL is accessible: `curl -I <slurm_repo_url>` from the clustr-serverd host. Fix network/firewall/DNS, then reimage. |
| `slurmd.service` degraded after reimage (advanced/gold-image path) | Slurm binaries missing from the pre-built image | Either switch to the recommended auto-install path (set `slurm_repo_url`, reimage), or re-bake the gold image with Slurm installed. See §2. |
| `munge -n \| unmunge` fails with `STATUS: Socket communication error` | munge service not running | `systemctl start munge`. If it fails to start, check `journalctl -u munge` for the reason. |
| `munge -n \| unmunge` fails with `STATUS: Invalid credential` | munge key mismatch — node has a different key than the server | Reimage the node. The correct key will be injected at finalize time. |
| `sinfo` shows all nodes as `down` | slurmctld not reachable, or node hostnames do not match `slurm.conf` | Check `slurmctld` is running on the controller. Verify `NodeName` lines in `slurm.conf` match actual node hostnames. |
| `srun` hangs indefinitely | Port 6817 (slurmctld) or 6818 (slurmd) blocked by firewall | Open the Slurm ports on the provisioning network: `firewall-cmd --add-port=6817-6818/tcp --permanent && firewall-cmd --reload`. |
| `POST /api/v1/slurm/enable` returns 500 | `CLUSTR_SECRET_KEY` not set — munge key cannot be encrypted | Set `CLUSTR_SECRET_KEY` in `secrets.env` and restart the server. |
| Slurm configs not written to `/etc/slurm/` after reimage | Module was not enabled before the reimage was triggered | Enable the module (`POST /api/v1/slurm/enable`), then reimage the nodes. |
| Role assignment with `{"role":"controller"}` returns `{"status":"ok"}` but `GET /api/v1/nodes/{id}/slurm/role` shows empty `roles` | Wrong body format — singular `role` key is silently ignored | Use `{"roles": ["controller"]}` (plural array). The `PUT /api/v1/nodes/{id}/slurm/role` endpoint requires an array, not a string. |
| `dnf install` fails in chroot with `file conflicts` — e.g. `file /usr/sbin/slurmctld from install of slurm-slurmctld-24.11.4 conflicts with file from package slurm-slurmctld-ohpc-22.05.8` | Base image was built before PR4 and contains OpenHPC Slurm 22.05.8 packages. Both old and new packages own the same files. | **Canonical fix:** rebuild the base image using the current kickstart (which enforces the Slurm-free policy). See [docs/imagebuilder.md](imagebuilder.md). **Safety net (if you cannot rebuild immediately):** the finalize step attempts `dnf remove slurm-ohpc* ohpc-slurm-*` before installing. If that strip succeeds, the subsequent install will work. Check deploy logs for `stripped pre-existing OpenHPC slurm packages`. |
| Slurm install fails with `Curl error (6): Couldn't resolve host name` during finalize | DNS not available in the chroot at finalize time. The deploy initramfs must have `/etc/resolv.conf` populated (udhcpc writes it when the DHCP server advertises option 6 / OptDNS). | Check that the DHCP server on the provisioning network (pxe/dhcp.go) is advertising the clustr-server IP as the DNS resolver. The finalize step copies `/etc/resolv.conf` from the initramfs into the chroot — if the source file is absent or empty, dnf DNS will fail. Verify: `cat /etc/resolv.conf` in the PXE initramfs shows the clustr-server IP. |
| Post-install version check in deploy logs shows `CONFLICT — installed Slurm is the old OpenHPC 22.05.8 build` | The OpenHPC strip in the finalize step did not fully remove the conflicting packages, or the dnf install resolved to a cached stale RPM. | Rebuild the base image (see [docs/imagebuilder.md](imagebuilder.md)). As a temporary workaround, manually remove OpenHPC packages on the booted node and install from the clustr repo. |
| A template fix was committed (e.g. `MpiDefault=none`) but the deployed `slurm.conf` still has the old value | Template files are only read during initial `seedDefaultTemplates` — existing DB rows are never auto-overwritten. | Use the reseed endpoint (§11) to bump the config version from the corrected template, then push to nodes. |

---

## 11. Re-seeding default templates (D18)

### Background

clustr stores all Slurm config file content in the `slurm_config_files` table.
The embedded template files (`internal/slurm/templates/*.tmpl`) are only
consulted once, when the Slurm module is first enabled (`seedDefaultTemplates`).
After that, the DB row is the authoritative source — template commits have no
automatic effect on running clusters.

To propagate a template fix to an already-seeded cluster, use the reseed
endpoint.

### The `is_clustr_default` flag

Each row in `slurm_config_files` carries an `is_clustr_default` boolean:

| Value | Meaning | Reseed endpoint |
|---|---|---|
| `1` | Row was seeded from an embedded clustr template, never operator-edited | **Will** overwrite with a new version from the current template |
| `0` | Row was written by an operator (via API or UI) | **Skipped** — never touched |

New installations (post-v1.0) seed with `is_clustr_default=1`. Existing
clusters upgraded from pre-v1.0 have all rows set to `0` by the migration
(safe default — treat as operator-owned). See the v1.0 one-time fix in
[docs/upgrade.md](upgrade.md) for how to opt these rows back in.

### POST /api/v1/slurm/configs/reseed-defaults

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/slurm/configs/reseed-defaults \
  -H "Authorization: Bearer <your-admin-key>"
```

Response:

```json
{
  "reseeded": ["cgroup.conf"],
  "skipped": [
    {"filename": "slurm.conf", "reason": "operator-customized"}
  ],
  "missing": ["gres.conf"]
}
```

- `reseeded` — files that had `is_clustr_default=1` and now have a new version
  bumped from the embedded template. The new version also has
  `is_clustr_default=1`.
- `skipped` — files with `is_clustr_default=0`; content left unchanged.
- `missing` — files in the managed list that have no embedded template (e.g.
  `gres.conf`, which is node-specific). These are skipped without an error.

### Two-step deploy

The reseed endpoint creates new DB versions but does **not** push to nodes.
After reseeding, push the new versions:

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/slurm/sync \
  -H "Authorization: Bearer <your-admin-key>"
```

The two-step design is intentional: the operator reviews what changed before
it hits live nodes.
