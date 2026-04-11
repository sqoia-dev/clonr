# clonr Product Roadmap

**Document status:** Living technical vision. Decisions marked [LOCKED] reflect architectural commitments already encoded in the codebase and API contract. All other decisions are subject to revision as we learn from real cluster deployments.

**Current state:** v0.x — all core machinery exists and runs. The system deploys nodes, manages images, serves PXE, and exposes a REST API with an embedded UI. What it is not yet: trusted in production, operable at fleet scale, or observable enough for on-call use.

---

## Where We Are: An Honest Assessment of v0.x

Reading the source carefully, here is what is real versus what is scaffolding.

**Solid foundations:**

- The `BaseImage` / `NodeConfig` split is architecturally correct and already enforced at the API boundary. This is the most important irreversible decision in the system, and it was made right. One image, N nodes.
- The `Deployer` interface (`Preflight → Deploy → Finalize`) is a clean three-phase contract that separates disk matching, data transfer, and identity application. This is extensible.
- Hardware discovery via `/proc` and `/sys` — no agents, no daemons, pure reads. The collector pattern with parallel execution and per-collector fault isolation is correct.
- The `ChrootSession` lifecycle with defer-chain unmounting is safe. Silent partial-mount failures are handled.
- IPMI management is real: power, SOL, BMC config, boot device selection, sensor reads.
- InfiniBand discovery and IPoIB NM keyfile generation is implemented and tested. Most tools skip this entirely.
- The log broker (in-process SSE pub/sub) is a clean pattern for the current scale.
- Pure-Go SQLite (`modernc.org/sqlite`, CGO_ENABLED=0) is the right call for an air-gapped appliance binary.

**What needs work before production:**

- `selectTargetDisk` picks the first disk that fits the layout. On multi-disk nodes this will write to the wrong device. This is a correctness bug.
- `applyKernelArgs` failure is silently swallowed: `_ = err`. If grub config is wrong, the node boots with the wrong kernel arguments and nobody knows.
- The `downloadAndExtract` path in `rsync.go` has no resume support. A dropped connection mid-download leaves a partially extracted filesystem on disk with no cleanup.
- `checksumDir` walks the rootfs in filesystem traversal order, not a stable canonical order. The checksum is not reproducible — the same rootfs on a different filesystem or after a repack can produce a different hash. This matters when we add image verification at deploy time.
- `internal/db/schema.go` has a stub schema (`images`, `image_tags`, `nodes`) that is distinct from the real schema in `pkg/db/` (`base_images`, `node_configs`, `build_jobs`, `deploy_events`). This is dead code but creates confusion and risks future migration bugs.
- There is no rollback. A failed deployment leaves the node with a partially written disk and no way to recover to a known state from the server.
- Single pre-shared token auth is fine for now, but there is no token rotation path and no way to scope access by operation type.
- The blob download in `BlockDeployer.Deploy` uses `http.DefaultClient` with no timeout. A stalled server during block writes will hang the deployment indefinitely.

---

## Competitive Positioning

Understanding why someone picks clonr over the incumbents is the prerequisite for knowing what to build.

### The Incumbent Landscape

**xCAT** is the standard in serious HPC centers. It does everything: provisioning, image management, hardware discovery, scheduler integration, firmware management, parallel command execution. The cost is complexity. A fresh xCAT install requires understanding diskful vs. diskless modes, statelite, genesis, osimage vs. netboot, litefile, and the xCAT database schema before you can provision a single node. The documentation is dense and assumes you already understand the domain. Configuration lives in a SQL database with ~60 tables. HPC centers that run xCAT have a person whose job is partially xCAT.

**Warewulf** (v4, the rewrite) learned from this. Containers as images, a cleaner CLI, Go-based. But it is RHEL/Rocky-centric, the overlay system has sharp edges with stateful data, and multi-site or cross-cluster support is nonexistent. Its strength is simplicity for the common case.

**Foreman/Katello** is a full lifecycle management platform — provisioning, configuration management, patch management, content views, subscription management. It is what you choose when you need Red Hat's level of enterprise support and compliance posture. It is also what you choose when you have a dedicated team to run it. Not appropriate for clusters where operators want to provision nodes, not manage a platform.

**MAAS (Metal as a Service)** is cloud-native bare metal: IPXE, preseed, curtin, cloud-init, region+rack controller topology, PostgreSQL backend, full API. It is well-suited for data centers running Ubuntu at scale. It brings heavy Canonical assumptions. InfiniBand support is absent. HPC-specific hardware (IB fabric, specialized storage interconnects) is not a focus.

### Where clonr is differentiated

**Today:**
- Single binary, no external database, no separate controller. Drop the binary on a provisioning server and it works. This matters enormously for HPC centers that have isolated networks and cannot reach package mirrors to install a 40-dependency stack.
- InfiniBand first-class. IPoIB NM keyfile generation, IB device discovery, PKey configuration. None of the major tools do this out of the box.
- The image model is explicitly designed around the reality of HPC: one golden image, hundreds of identical nodes. The `BaseImage` / `NodeConfig` split enforces this architecture rather than letting operators paint themselves into per-node image corners.
- Pure Go, CGO_ENABLED=0, statically linked. Deploy to any x86_64 or aarch64 Linux host without libc concerns.

**Where we are behind:**
- No fleet-wide parallel execution (xCAT does this with `xdsh`, MAAS does it with the API).
- No image versioning or rollback history.
- No scheduler integration. xCAT has native SLURM node state hooks; clonr has nothing.
- No hardware inventory beyond a single-node snapshot. No aggregate view across the cluster.
- No TLS on the server. This is acceptable for air-gapped clusters but becomes a blocker for any multi-tenant or partially-connected environment.
- No firmware management. HPE iLO, Dell iDRAC, and Supermicro IPMI all have vendor-specific extensions that matter for HPC operators.

**The strategic bet:** clonr wins by being the tool that HPC sysadmins can actually understand, modify, and run without reading a book first. It wins by making InfiniBand and bare-metal provisioning first-class, not afterthoughts. It does not try to be a full platform lifecycle tool. That is a deliberate constraint, not a gap.

---

## Phase 1: Production Hardening (v1.0)

**Goal:** A sysadmin can run clonr on a real cluster and trust it not to silently corrupt a node's filesystem or leave the system in an unknown state.

The test for "production-grade" is simple: does a random failure mid-deployment leave the cluster in a recoverable, known state? Currently the answer is "sometimes, by accident."

### Must-Have

**1.1 Correct target disk selection** [Complexity: Low]
The current `selectTargetDisk` picks the first disk that satisfies the layout's minimum size. On a node with two NVMe drives this is a coin flip. Fix: match disk type constraints from `DiskLayout` (NVMe vs SATA vs SAS) and expose a `TargetDiskHint` in `NodeConfig` so the operator can override. Additionally, require explicit acknowledgment when the selected disk contains an existing partition table — refuse to proceed silently.

Dependency: None. This is a correctness fix.

**1.2 Deployment rollback / atomic failure handling** [Complexity: Medium]
Today a failed `FilesystemDeployer.Deploy` leaves the disk partially partitioned. Implement a pre-deployment snapshot: before wiping the target disk, capture the existing partition table and a checksum of the first 1MB (MBR/GPT). On failure, emit a structured error event with enough state for the operator to understand what happened. The rollback for bare metal is "re-run the deployment" — we do not need to restore data, we need to ensure the node can be re-deployed cleanly. Fix: ensure that a failed deployment always sets the IPMI boot device back to PXE so the node reboots into a deployable state on next power cycle, and posts a failure event to the server with full context.

Dependency: IPMI integration already exists in `pkg/ipmi`. This is wiring work.

**1.3 Deployment integrity verification** [Complexity: Medium]
After `Finalize`, verify the deployed image against the stored checksum before reporting success. For filesystem deployments: compute a checksum of the deployed rootfs using the same algorithm as `checksumDir`, compare against `BaseImage.Checksum`. For block deployments: verify a spot-check of critical paths (`/etc/hostname`, `/etc/fstab`, key binaries). Canonicalize the `checksumDir` walk to sort paths alphabetically before hashing — fix the non-reproducibility issue.

Dependency: Requires 1.1 to be correct first. Checksum verification against a wrong disk is worse than no verification.

**1.4 Error propagation — eliminate silent failures** [Complexity: Low]
Audit all `_ = err` patterns in the deploy and finalize paths. The current `applyKernelArgs` failure suppression is dangerous: if `grub2-mkconfig` fails, the node may not boot but deployment reports success. Policy: any failure in `Finalize` that affects bootability is fatal. Non-fatal failures (BMC config, shell history cleanup) must emit a structured warning log entry and be surfaced in the deployment event record. Implement a `FinalizeResult` type that carries a list of warnings alongside success/failure.

Dependency: None.

**1.5 Download resumption and timeout** [Complexity: Low]
The block deployer uses `http.DefaultClient` with no timeout. The filesystem deployer has the same issue. Add: per-request timeouts (configurable, default 30 minutes for large images), HTTP `Range` header support for resuming interrupted downloads, and a maximum retry count with exponential backoff. The `progressReader` already tracks bytes written — use this to construct the `Range` header on retry.

Dependency: None.

**1.6 Remove the dead internal schema** [Complexity: Low]
`internal/db/schema.go` defines a stub schema (`images`, `image_tags`, `nodes`) that diverges from the actual `pkg/db/` schema. Delete or reconcile it. This is a maintenance hazard — a future migration will have to navigate two schema definitions.

Dependency: None.

**1.7 TLS for clonr-serverd** [Complexity: Low]
Add TLS support to `clonr-serverd`. Self-signed cert generation on first run is acceptable for the common case. Operators with a CA can bring their own cert/key pair. The PXE boot path must remain HTTP (iPXE before TLS negotiation is complex), but the API and UI should support HTTPS. The blob download during deployment should use the same TLS context.

Dependency: None. This is configuration + `http.Server` wiring.

**1.8 Integration test suite against Proxmox lab** [Complexity: High]
The test lab design document specifies exactly the right setup. Implement a CI pipeline that: (a) boots test-node-01 via PXE, (b) deploys a Rocky 9 image, (c) verifies the node boots and responds to SSH, (d) re-deploys a second image to the same node, (e) verifies the second deployment. This is the acceptance test for v1.0. Without it, every release is "works on my machine."

Dependency: Requires 1.1 and 1.2 first — otherwise the test will be flaky.

### Nice-to-Have

**1.9 API versioning enforcement** [Complexity: Low]
Add `Accept: application/vnd.clonr.v1+json` support and return `API-Version` headers. This is cheap to add now and expensive to retrofit later when clients exist in the wild.

**1.10 Config file validation on startup** [Complexity: Low]
`clonr-serverd` should fail fast with a clear error message if `ImageDir` is not writable, if `PXE.TFTPDir` is not accessible, or if the SQLite file cannot be opened. Currently some of these failures surface only when the first request arrives.

**1.11 Structured deploy event schema enrichment** [Complexity: Low]
Add `node_hardware_snapshot` (JSON) and `image_checksum_at_deploy` to `deploy_events`. This creates an audit trail that answers "what image was on this node when it was last deployed, and what was the hardware state at that time."

---

## Phase 2: Fleet Management (v1.x)

**Goal:** A single clonr-serverd instance can manage 100-500 nodes without per-node operator intervention for routine operations.

The core insight here is that HPC operations are batch operations. Sysadmins think in racks and partitions, not individual nodes. The API must reflect this.

### Must-Have

**2.1 Node groups and group-targeted operations** [Complexity: Medium]
`NodeConfig.Groups` already exists as a `[]string` field. Build on it: add group-level operations to the API (`POST /api/v1/groups/:name/deploy`, `POST /api/v1/groups/:name/reboot`, `POST /api/v1/groups/:name/assign-image`). Groups should map naturally to SLURM partitions — the same strings. Store groups in a `node_groups` table with name, description, and image assignment, with a `node_group_members` join table.

Schema addition to `pkg/db/`:
```sql
CREATE TABLE node_groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    base_image_id TEXT REFERENCES base_images(id),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE node_group_members (
    group_id TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    node_id  TEXT NOT NULL REFERENCES node_configs(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, node_id)
);
```

This is a schema change — it is an irreversible decision once API consumers exist. The design above is the right shape: groups are first-class objects, not just tags on nodes.

Dependency: None.

**2.2 Bulk node import from CSV/JSON** [Complexity: Low]
HPC centers manage their node inventory in spreadsheets or CMDB exports. Add `POST /api/v1/nodes/bulk` accepting a JSON array of `CreateNodeConfigRequest` objects, processing each atomically (all-or-nothing within a single SQLite transaction). Add a CSV import path for the common rack spreadsheet format: `hostname,mac,ip,bmc_ip,group`.

Dependency: 2.1 (groups must exist to assign during import).

**2.3 Rolling deployment with concurrency control** [Complexity: Medium]
Add a `DeployJob` model: a server-side job that deploys a list of nodes or a group to an image, with configurable concurrency (default: 10% of group size, minimum 1). The server does not execute deployments directly — it queues them and signals nodes via IPMI PXE boot. Nodes pull their assignment on registration. The `RegisterResponse.Action = "deploy"` path already handles this. The new piece is: the server maintains a `deploy_jobs` table with per-node status tracking, and the UI shows progress across all nodes in a job.

New schema:
```sql
CREATE TABLE deploy_jobs (
    id              TEXT PRIMARY KEY,
    group_id        TEXT REFERENCES node_groups(id),
    base_image_id   TEXT NOT NULL REFERENCES base_images(id),
    concurrency     INTEGER NOT NULL DEFAULT 10,
    status          TEXT NOT NULL DEFAULT 'pending',
    created_at      INTEGER NOT NULL,
    started_at      INTEGER,
    completed_at    INTEGER
);

CREATE TABLE deploy_job_nodes (
    job_id      TEXT NOT NULL REFERENCES deploy_jobs(id) ON DELETE CASCADE,
    node_id     TEXT NOT NULL REFERENCES node_configs(id),
    status      TEXT NOT NULL DEFAULT 'pending',
    started_at  INTEGER,
    completed_at INTEGER,
    error       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (job_id, node_id)
);
```

**2.4 SLURM node state integration** [Complexity: Medium]
When clonr begins deploying a node, it should drain that node from SLURM (`scontrol update NodeName=<host> State=DRAIN Reason="clonr deployment"`). After successful deployment and first boot, it can optionally resume (`scontrol update NodeName=<host> State=RESUME`). This should be optional and driven by a `ClusterIntegration` config block. Do not make SLURM a hard dependency — many sites run PBS or no scheduler.

Implementation: add an optional `ClusterIntegration` interface with `DrainNode(ctx, hostname) error` and `ResumeNode(ctx, hostname) error`. Implement a SLURM backend that shells out to `scontrol`. Wire it into the deploy job lifecycle.

Dependency: 2.3.

---

### Must-Have: Cluster Stack Automation

This is the founder's core vision for Phase 2: after PXE imaging is done, clonr finishes the job. Racking hardware and booting nodes should yield a fully functional HPC cluster — SLURM running, users configured, storage mounted. The features below make that a reality. They depend on 2.1 (groups) and 2.3 (deploy jobs) because they operate on a fleet topology, not individual nodes.

**2.10 Automated SLURM cluster deployment** [Complexity: High]

The goal: `clonr slurm deploy` provisions a complete, operational SLURM cluster from the node inventory — no manual slurm.conf editing, no hand-copying munge keys.

Scope:

- **Head node configuration:** Install and configure `slurmctld` on the designated head node. clonr connects over SSH post-imaging and runs the provisioning sequence: package install, slurm.conf generation, slurmctld enable/start.
- **Compute node configuration:** Install and configure `slurmd` on all compute nodes. clonr drives this in parallel (bounded by the same concurrency control as deploy jobs — see 2.3).
- **Automatic slurm.conf generation:** clonr generates `slurm.conf` from the node inventory and hardware profiles already in its database. No manual entry. Partition definitions come from node groups (see 2.1). Hardware-specific partitions are derived from the hardware profile: nodes with GRES (GPUs) get a `gpu` partition; nodes meeting a configurable memory threshold get a `bigmem` partition; all remaining nodes default to the `compute` partition. The GRES line per node is generated from the hardware discovery data already collected at registration.
- **Munge key distribution:** Generate a single munge key on the head node and push it to all compute nodes over SSH. Munge is enabled and started on all nodes before slurmctld/slurmd come up.
- **Accounting database (slurmdbd + MariaDB):** Install MariaDB on the head node (or a designated accounting node), create the SLURM accounting database and user, install and configure `slurmdbd`, and wire it into slurmctld via `AccountingStorageHost`. This is not optional — sites that start without accounting almost always regret it when they need to enforce QOS or produce usage reports.
- **Partition auto-generation:** The partition builder reads node group hardware profiles and emits partition stanzas. The mapping is configurable but ships with sensible defaults:

  | Condition | Partition name |
  |-----------|---------------|
  | Any GPU in GRES | `gpu` |
  | MemSpecLimit > configurable threshold (default 512GB) | `bigmem` |
  | InfiniBand present | `ib` (optional, off by default) |
  | Default | `compute` |

  Nodes can appear in multiple partitions.

- **SLURM user/account/QOS provisioning:** After the cluster is up, clonr creates the initial SLURM accounting structure via `sacctmgr`: a root cluster entry, a default account, and one account per node group. Users created in the identity layer (see 2.11) are automatically added as SLURM users under their account. QOS definitions are configurable via a YAML stanza in the clonr config:

  ```yaml
  slurm:
    qos:
      - name: normal
        priority: 10
        max_wall: "24:00:00"
      - name: high
        priority: 100
        max_wall: "72:00:00"
        max_tres_per_user: "cpu=256,gres/gpu=8"
  ```

Implementation notes: the provisioning engine shells out over SSH using the credentials already in `NodeConfig`. All generated configuration files are stored as artifacts in the clonr database alongside the deploy event — so the exact slurm.conf that was deployed to a cluster is queryable after the fact. Idempotency: re-running `clonr slurm deploy` detects existing configuration and updates in place rather than overwriting.

Dependency: 2.1 (node groups), 2.3 (parallel fleet operations), 2.11 (user provisioning — for SLURM account linkage), 2.13 (node roles — determines which nodes receive `slurmctld` vs `slurmd`, which partitions each node joins, and which nodes carry GPU GRES declarations).

**2.11 Identity layer: user and group management** [Complexity: High]

HPC clusters are multi-user systems. The identity layer — who can log in, what groups they belong to, what their UID/GID is — must be consistent across every node. clonr manages this centrally.

Scope:

- **Central user/group store:** A `users` and `groups` table in the clonr database. Users have: username, UID, primary GID, SSH public keys (one or more), home directory path, shell, and account status (active/suspended). Groups have: name, GID, member list.

  ```sql
  CREATE TABLE users (
      id          TEXT PRIMARY KEY,
      username    TEXT NOT NULL UNIQUE,
      uid         INTEGER NOT NULL UNIQUE,
      primary_gid INTEGER NOT NULL,
      gecos       TEXT NOT NULL DEFAULT '',
      home_dir    TEXT NOT NULL,
      shell       TEXT NOT NULL DEFAULT '/bin/bash',
      status      TEXT NOT NULL DEFAULT 'active',
      created_at  INTEGER NOT NULL,
      updated_at  INTEGER NOT NULL
  );

  CREATE TABLE user_ssh_keys (
      id          TEXT PRIMARY KEY,
      user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      label       TEXT NOT NULL,
      public_key  TEXT NOT NULL,
      added_at    INTEGER NOT NULL
  );

  CREATE TABLE groups (
      id          TEXT PRIMARY KEY,
      name        TEXT NOT NULL UNIQUE,
      gid         INTEGER NOT NULL UNIQUE,
      created_at  INTEGER NOT NULL
  );

  CREATE TABLE group_members (
      group_id    TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
      user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      PRIMARY KEY (group_id, user_id)
  );
  ```

- **Push to all nodes:** `clonr users sync` (or triggered automatically on user change) pushes the current user/group state to all registered nodes. Mechanism: generate `/etc/passwd`, `/etc/shadow`, and `/etc/group` fragments for clonr-managed users, then either (a) append/merge into the node's existing files via SSH, or (b) if LDAP is deployed (see 2.12), push via LDAP instead and configure SSSD/nss-pam-ldapd on nodes as LDAP clients. The LDAP path is preferred when available; the SSH-push path is the fallback for sites that do not want LDAP.

- **SSH key management:** clonr generates and distributes `~/.ssh/authorized_keys` for each user on each node. When a key is added or revoked, the change is pushed to all active nodes immediately. Key rotation: clonr supports flagging a key as revoked-by-date, which removes it from all nodes before the deadline.

- **Home directory provisioning:** When a user is created, clonr creates their home directory on the shared storage server (via SSH to a designated storage node) with correct ownership, permissions, and skeleton copy (`/etc/skel`). On NFS-mounted home directories, this is a one-time operation that is immediately visible to all compute nodes.

- **Quota management integration:** clonr can issue `setquota` commands on the storage node when a user is created, with per-user soft/hard limits configurable in the user record. This is SSH-based automation, not a storage protocol integration.

API additions:
- `POST /api/v1/users` — create user
- `GET /api/v1/users` — list users with group membership
- `PUT /api/v1/users/:id/ssh-keys` — add SSH key
- `DELETE /api/v1/users/:id/ssh-keys/:key_id` — revoke key
- `POST /api/v1/users/sync` — push current state to all nodes
- `POST /api/v1/groups` — create group
- `PUT /api/v1/groups/:id/members` — set membership

Dependency: 2.1 (node groups drive which nodes receive user sync), post-2.3 (parallel SSH push to fleet).

**2.12 LDAP server deployment and client configuration** [Complexity: High]

Standing up LDAP is optional — sites that want to use the SSH-push identity model from 2.11 can do so without it. But LDAP is the right long-term answer for clusters with more than ~50 users, and clonr should be able to stand it up completely, not just configure clients against an existing server.

Scope:

- **Server installation:** `clonr ldap deploy --head-node <hostname>` installs and configures OpenLDAP (or 389 Directory Server, configurable) on the head node via SSH. The choice between OpenLDAP and 389DS is driven by the OS family detected in the node's hardware profile: Rocky/RHEL → 389DS (ships with `dirsrv` packages); Debian/Ubuntu → OpenLDAP (`slapd`).

- **Schema setup:** Configure POSIX account schema (`posixAccount`, `posixGroup`, `shadowAccount`) automatically. This covers the standard UID/GID/home/shell attributes that `nsswitch` expects.

- **Initial DIT population:** clonr populates the LDAP directory from its own `users` and `groups` tables (see 2.11). The two sources of truth are reconciled: the clonr database is canonical; the LDAP directory is a derived view. `clonr ldap sync` pushes incremental changes.

- **TLS certificate generation:** Self-signed cert generation using Go's `crypto/x509` — no `openssl` dependency. The certificate is generated server-side, stored in the clonr database alongside the LDAP deployment record, and distributed to compute nodes during client configuration. Sites can bring their own cert.

- **Client configuration pushed to compute nodes:** After LDAP is up, clonr configures all compute nodes as LDAP clients:
  - For SSSD: generates and deploys `/etc/sssd/sssd.conf` with the LDAP URI, search base, TLS CA cert, and access control rules.
  - For nss-pam-ldapd: generates and deploys `/etc/nslcd.conf`.
  - Enables and starts the appropriate service (`sssd` or `nslcd`) on each node via SSH.
  - Adds `ldap` to `/etc/nsswitch.conf` for `passwd`, `group`, and `shadow`.

- **SLURM accounting integration:** When both LDAP and SLURM are deployed, clonr links them: LDAP users are automatically created as SLURM accounts under their primary group. The SLURM `AuthType` can optionally be configured to use LDAP for user validation if the site's SLURM version supports it.

- **Idempotency:** Re-running `clonr ldap deploy` on a cluster where LDAP is already running detects the existing installation and performs a configuration sync only. It does not wipe the directory.

All LDAP deployment state — server hostname, DIT base, TLS cert, bound service credentials — is stored in the clonr database in a `ldap_deployments` table. This makes the LDAP configuration queryable and auditable from the clonr API.

Dependency: 2.11 (user store as source of truth for DIT population), 2.1 (node groups determine which nodes become LDAP clients), 2.13 (node roles — `head-node` role determines where the LDAP server is installed; `compute`, `login`, and `burst` roles determine which nodes receive LDAP client configuration).

**2.5 Node hardware inventory aggregation** [Complexity: Low]
The hardware profile is already captured at registration (`NodeConfig.HardwareProfile`). Add: (a) a queryable API for aggregate inventory (`GET /api/v1/inventory?group=compute&field=cpu.model`), (b) a mismatch detection system that alerts when a node registers with hardware that does not match what was previously recorded (e.g., a DIMM was removed), (c) a hardware profile diff endpoint (`GET /api/v1/nodes/:id/hardware-diff`) that shows what changed since last deployment.

Dependency: 2.1 for group-level aggregation.

**2.6 NodeConfig templating** [Complexity: Medium]
On large clusters, 200 node configs differ only in hostname, IP address, and MAC. Add a `NodeTemplate` model: a NodeConfig with fields that support simple variable substitution (`{{.Index}}`, `{{.Rack}}`, `{{.Slot}}`). A template expansion operation generates N NodeConfig records from a template and a range specification. This is not Jinja — keep it minimal and well-scoped.

Dependency: 2.2 (template expansion should honor groups).

**2.13 Node roles: topology-driven cluster configuration** [Complexity: High]

Nodes in an HPC cluster are not interchangeable. A head node runs `slurmctld`, exports NFS, and hosts the LDAP server. A compute node mounts NFS, runs `slurmd`, and should never be a login target. A GPU node is a compute node plus CUDA drivers, `nvidia-fabricmanager`, and DCGM. Today clonr has no concept of these distinctions — every node is a `NodeConfig` with manual field population. At fleet scale this breaks down: 100 compute nodes should not require 100 hand-crafted configs.

The role system is the answer. A role is a named template that pre-fills `NodeConfig` fields and drives post-deploy provisioning decisions. Roles are the bridge between "what image is on this disk" and "what services are running on this node."

**Built-in roles (ships with clonr):**

| Role | Services enabled | Services disabled | Notes |
|---|---|---|---|
| `head-node` | `slurmctld`, `slurmdbd`, `slapd`/`dirsrv`, NFS server, `clonr-serverd`, `sshd` | — | Single node per cluster; drives SLURM/LDAP deploy targets |
| `compute` | `slurmd`, SSSD/nss-pam-ldapd, NFS client, `sshd` | login shell for normal users | Bulk of the cluster |
| `gpu-compute` | Everything in `compute` + `nvidia-fabricmanager`, `dcgm-exporter`, CUDA driver service | — | Inherits from `compute`; role composition |
| `storage` | NFS server, Lustre/BeeGFS OST or MDT service | `slurmd` | Dedicated storage nodes with large disk arrays |
| `login` | `sshd`, quota enforcement daemon, module system (`Lmod`), `sssd` | `slurmd`, `slurmctld` | User-facing; no job scheduling |
| `management` | Prometheus, Grafana, DNS (`bind`/`unbound`), DHCP (`dhcpd`) | `slurmd` | Monitoring and network services |
| `burst` | `slurmd`, NFS client, SSSD | — | On-demand cloud nodes; ephemeral; drain on shutdown |

Roles compose. A small cluster may have a single node with both `head-node` and `storage`. When roles are combined, the union of their service and package sets applies; conflicts (e.g., two roles that each set a different NFS export path) surface as validation errors at role-assignment time, not at deploy time.

**What a role definition contains:**

```yaml
role: compute
description: "Standard compute node — slurmd, NFS mount, LDAP client, no login"
base_image: rocky9-hpc-base          # defaults to cluster-wide base image if omitted
packages:
  install:
    - slurm-slurmd
    - sssd
    - nfs-utils
  remove:
    - firewalld                      # replaced by explicit nftables rules below
services:
  enable:
    - slurmd
    - sssd
    - rpcbind
  disable:
    - slurmctld
    - slapd
firewall:
  rules:
    - allow tcp 22 from management_net   # SSH from management subnet
    - allow tcp 6818 from head_net       # slurmd port from head node
    - deny all                           # default deny
network:
  interfaces:
    - name: eth0
      role: management                   # pulls IP from NodeConfig.ManagementIP
    - name: ib0
      role: infiniband                   # IPoIB, pulls from NodeConfig.InfiniBandIP
storage_mounts:
  - source: "head-node:/home"
    target: /home
    fstype: nfs4
    options: "rw,hard,intr"
  - source: "head-node:/opt/apps"
    target: /opt/apps
    fstype: nfs4
    options: "ro,hard,intr"
slurm:
  role: compute                          # slurmd, not slurmctld
  partitions: [compute]                  # which SLURM partitions this node joins
  gres: auto                             # derived from hardware discovery
scripts:
  pre_deploy: ""                         # path to script run before imaging
  post_deploy: |                         # inline script run after first boot via SSH
    systemctl enable --now slurmd
    sssctl cache-expire -E
```

**Schema additions to `pkg/db/`:**

```sql
CREATE TABLE node_roles (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    spec        TEXT NOT NULL,           -- YAML blob of the role definition above
    builtin     INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE node_role_assignments (
    node_id TEXT NOT NULL REFERENCES node_configs(id) ON DELETE CASCADE,
    role_id TEXT NOT NULL REFERENCES node_roles(id) ON DELETE RESTRICT,
    priority INTEGER NOT NULL DEFAULT 0, -- merge order when multiple roles apply
    PRIMARY KEY (node_id, role_id)
);
```

`NodeConfig` gains a `Roles []string` field (role names, resolved at provisioning time). The role spec is the source of truth; `NodeConfig` fields set explicitly by the operator override role defaults.

**How roles drive 2.10, 2.11, and 2.12:**

This is where roles become a first-class topology primitive, not just a template system. When `clonr slurm deploy` runs (2.10), it queries "which node has role `head-node`?" to determine where to install `slurmctld`. When `clonr ldap deploy` runs (2.12), it installs the LDAP server on the `head-node` role holder and pushes client configuration to every node with role `compute`, `gpu-compute`, `login`, or `burst`. User sync (2.11) targets nodes by role — `login` nodes get full home directory mounts and quota enforcement; `compute` nodes get only the SSSD lookup config.

Without a role system, these features require the operator to explicitly specify target nodes for every operation. With roles, the operator declares intent once ("this is the head node") and all downstream automation resolves from that.

**API additions:**

- `POST /api/v1/roles` — create a custom role from a YAML spec
- `GET /api/v1/roles` — list all roles (builtin and custom)
- `GET /api/v1/roles/:name` — fetch role spec
- `PUT /api/v1/nodes/:id/roles` — assign roles to a node
- `GET /api/v1/roles/:name/nodes` — list all nodes with a given role
- `POST /api/v1/roles/:name/apply` — re-apply role configuration to all nodes holding it (idempotent SSH-based enforcement)

**CLI additions:**

- `clonr role list` — show all roles and the nodes assigned to each
- `clonr role assign <node> <role>` — assign a role to a node
- `clonr role apply <role>` — push role configuration to all matching nodes
- `clonr cluster topology` — print a human-readable map of the cluster: which node holds which roles, SLURM partition membership, storage topology

**Implementation note on role composition:** Merge order matters. When a node has roles `[compute, gpu-compute]`, the `gpu-compute` role's package and service additions are unioned with `compute`. The merge algorithm is explicit and deterministic: packages are unioned, services are unioned, firewall rules are concatenated in priority order, storage mounts are unioned with duplicate-source detection, SLURM partitions are unioned. Role conflicts (two roles specifying contradictory values for a scalar field like `slurm.role`) are surfaced as errors at assignment time.

This is a schema change and an API addition. The schema additions are append-only and safe to add alongside existing tables. The `Roles` field on `NodeConfig` is additive — existing nodes with no role assignments behave exactly as before.

Dependency: 2.1 (node groups — roles augment groups; a group may have a default role), 2.6 (templating — role assignments can be declared in a `NodeTemplate`), 2.10, 2.11, 2.12 (roles are the topology driver for all cluster stack automation).

### Nice-to-Have

**2.7 PBS/Torque integration** [Complexity: Medium]
Same as 2.4 but for PBS. `qmgr -c "set node <host> state=offline"` and `pbsnodes -c <host>`. Implement as a second `ClusterIntegration` backend.

**2.8 Parallel SSH execution** [Complexity: Low]
Add `clonr exec --group <name> -- <command>` that runs a command across all nodes in a group in parallel, collecting output. This is `xdsh` for clonr. Implement using the existing `SSHKeys` in `NodeConfig` — clonr already knows the authorized keys for every node. Cap parallelism at 64 concurrent SSH connections by default.

**2.9 Node tagging and search** [Complexity: Low]
Add freeform tags to `NodeConfig` (beyond Groups). Add a search API: `GET /api/v1/nodes?tag=infiniband&tag=gpu&group=compute`. Useful for mixed-hardware clusters where nodes within a SLURM partition have heterogeneous capabilities.

---

## Phase 3: Advanced Image Management (v2.0)

**Goal:** Image lifecycle has first-class versioning, automated patching, and inheritance — making golden image management tractable at scale.

This is the phase where clonr's image model becomes a genuine technical differentiator. The current `BaseImage.Version` field is a string label. Phase 3 makes it structural.

### Must-Have

**3.1 Image versioning with immutable history** [Complexity: Medium]
Today `BaseImage.Version` is an unstructured string. Formalize it: add semantic versioning with a `parent_image_id` field that creates an explicit lineage graph. Schema addition:

```sql
ALTER TABLE base_images ADD COLUMN parent_image_id TEXT REFERENCES base_images(id);
ALTER TABLE base_images ADD COLUMN semver TEXT NOT NULL DEFAULT '';
```

Add `POST /api/v1/images/:id/bump-version` that creates a new image record as a child of the current one. The child inherits the parent's `DiskLayout` unless explicitly overridden. The blob for the child can be either a full copy (safe) or a delta (see 3.3). This API shape enables the audit trail: "which version of the rocky9-hpc-base image is deployed on compute-001 right now?"

**3.2 Automated patching pipeline** [Complexity: High]
Add `POST /api/v1/images/:id/patch` that: (a) creates a chroot session on the image, (b) runs `dnf upgrade -y` (or `apt-get upgrade -y`, distro-detected), (c) runs a configurable validation script inside the chroot, (d) bumps the version and finalizes the new image. This is a server-side async operation, same pattern as `Factory.pullAsync`. The build log is streamed via SSE.

This is the critical feature for HPC centers running CVE patching cycles. Currently they manually pull a new image, chroot in, patch, and re-capture. This automates 80% of that workflow.

Dependency: 3.1.

**3.3 Image layering: overlay deltas** [Complexity: High]
Full-image copies are expensive (10-30GB per image version). Implement an overlay layer system using content-addressed storage. Each image blob is stored as a tar archive of changed files relative to its parent. Deployment assembles the full image by applying layers in lineage order. This mirrors Docker's layer model but for bare-metal tar archives rather than container union filesystems.

The key design constraint: assembly happens on the server, not the node. The node always receives a complete, consistent rootfs — the layering is a storage optimization on the server side. This keeps the deploy path simple and the node-side binary unchanged.

Implementation requires a content-addressable blob store (hash-addressed chunks) and a layer composition engine. The existing `ImageStore` interface already has the right shape to back this with a new implementation.

This is a significant investment. Mark it as a v2.1 target if 3.1 and 3.2 are already delivering value.

**3.4 Image promotion workflow** [Complexity: Medium]
Add lifecycle stages beyond `building / ready / archived`: `draft → testing → staging → production`. Image promotion requires an explicit `POST /api/v1/images/:id/promote` call and can be gated on a passing validation run (configurable post-deploy SSH check). Nodes in a group can be pinned to a stage: `GroupConfig.ImageStage = "production"` means the group always tracks the latest image in the production stage, not a specific version.

Dependency: 3.1.

**3.5 Image build from Dockerfile-like spec** [Complexity: Medium]
Add a `Clonrfile` format — a minimal declarative spec for building an image from a base URL plus a series of chroot commands:

```yaml
base: https://dl.rockylinux.org/pub/rocky/9/images/Rocky-9-GenericCloud.latest.x86_64.qcow2
name: rocky9-hpc-mpi
version: 1.0.0
steps:
  - run: dnf install -y openmpi openmpi-devel ucx
  - run: dnf install -y cuda-toolkit-12-4
  - copy: ./configs/modulefiles /etc/modulefiles/
  - run: echo "source /etc/profile.d/modules.sh" >> /etc/skel/.bashrc
disk_layout:
  partitions:
    - label: esp
      size_bytes: 536870912
      filesystem: vfat
      mountpoint: /boot/efi
      flags: [boot, esp]
    - label: root
      size_bytes: 0
      filesystem: xfs
      mountpoint: /
```

`clonr image build -f Clonrfile` submits this to the server, which executes the steps in a chroot session and finalizes the image. This makes image construction reproducible and version-controllable (the Clonrfile goes in git).

Dependency: 3.2 for the step execution engine.

### Nice-to-Have

**3.6 Image content search** [Complexity: Medium]
`GET /api/v1/images/:id/search?path=/etc/yum.repos.d` — returns files matching a path glob within the image rootfs. Useful for auditing: "which images have the old CUDA repo configured?" Implement as a server-side walk of the rootfs blob directory.

**3.7 Image export to OCI tar** [Complexity: Low]
`GET /api/v1/images/:id/export?format=oci` — exports the rootfs as an OCI image tarball compatible with `docker load` or `podman load`. This makes clonr images usable in container environments without re-building, useful for sites that want parity between their bare-metal and containerized workloads.

---

## Phase 4: Observability and Intelligence (v2.x)

**Goal:** clonr knows the health state of the cluster, not just its provisioning state.

This is where clonr transitions from a provisioning tool to a cluster operations platform. The data is already being collected (IPMI sensors, hardware profiles, deploy events) — this phase builds the aggregation and alerting layer.

### Must-Have

**4.1 Hardware health monitoring via IPMI polling** [Complexity: Medium]
The `ipmi.Client.GetSensorData` method already exists and returns structured `[]Sensor`. Add a server-side polling loop that queries the BMC of every registered node on a configurable interval (default: 5 minutes). Store readings in a time-series table:

```sql
CREATE TABLE sensor_readings (
    node_id     TEXT NOT NULL REFERENCES node_configs(id),
    sensor_name TEXT NOT NULL,
    value       REAL NOT NULL,
    units       TEXT NOT NULL,
    status      TEXT NOT NULL,
    sampled_at  INTEGER NOT NULL
);
CREATE INDEX idx_sensor_readings_node_time ON sensor_readings(node_id, sampled_at);
```

Retention: keep 30 days of 5-minute samples. This is approximately 8,640 readings per sensor per node per month — manageable in SQLite for clusters up to a few hundred nodes.

**4.2 Threshold-based alerting** [Complexity: Medium]
Add configurable alert rules: `{sensor: "CPU Temp", threshold: 85, unit: "degrees C", action: "warn"}`. When a threshold is crossed, emit a structured alert event (visible in the UI and optionally forwarded via webhook). Alert on: temperature thresholds, fan failure, power supply redundancy loss, memory correctable errors (via IPMI SEL, not just sensors).

Dependency: 4.1.

**4.3 Prometheus metrics endpoint** [Complexity: Low]
Expose `GET /metrics` in Prometheus text format. Metrics to emit:
- `clonr_nodes_total{status="deployed|pending|error"}` — node count by status
- `clonr_images_total{status="ready|building|archived"}` — image count by status
- `clonr_deploy_duration_seconds{image,result}` — deployment duration histogram
- `clonr_sensor_value{node,sensor,unit}` — IPMI sensor gauge
- `clonr_deploy_jobs_active` — currently running deployment jobs

This is a low-complexity, high-value addition. Every site that runs Prometheus can wire this up in minutes.

Dependency: None for deployment metrics. Requires 4.1 for sensor metrics.

**4.4 Deployment analytics** [Complexity: Low]
`GET /api/v1/analytics/deployments` — aggregate statistics over the `deploy_events` table: mean/p50/p99 deployment duration by image, failure rate by image, deployment frequency by node group. These answer the questions sysadmins actually ask: "why does compute-rack-2 take 30% longer to deploy?" and "which image version has the highest failure rate?"

Dependency: The `deploy_events` table already exists. This is a query layer.

**4.5 IPMI SEL monitoring** [Complexity: Medium]
Poll the System Event Log from each node's BMC (`ipmitool sel elist`). Parse and store critical events: memory ECC errors, PCIe errors, fan failures, thermal events. Surface recent SEL events in the node detail view alongside the hardware profile. SEL events are leading indicators of hardware failure — a node that has had 10 correctable ECC errors in the last week is likely to fail soon.

Dependency: 4.1 for the polling infrastructure.

### Nice-to-Have

**4.6 Predictive failure scoring** [Complexity: High]
Combine SEL event frequency, temperature trend over 7 days, deployment failure history, and uptime statistics into a per-node health score (0-100). Nodes below a threshold are flagged for preemptive maintenance. This is not ML — it is a weighted heuristic that sysadmins can understand and tune. The value is in surfacing leading indicators before nodes fail mid-job.

Dependency: 4.1, 4.5, and enough historical data (30+ days of readings).

**4.7 Grafana dashboard templates** [Complexity: Low]
Publish Grafana dashboard JSON files that work with the Prometheus metrics from 4.3. One for cluster overview, one for per-node hardware health, one for deployment pipeline metrics. Ship these in the repository so sites can import them immediately.

Dependency: 4.3.

---

## Phase 5: Multi-Cluster and Federation (v3.0)

**Goal:** One control plane manages multiple clusters, possibly across sites, with image replication and centralized policy.

This phase is necessary for sites with more than one cluster (common: a production cluster and a dev/test cluster, or clusters at multiple campus locations). It is also necessary for any commercial service built on top of clonr.

**Irreversibility warning:** The API changes in this phase are breaking. Existing clients will need to be updated. Plan accordingly.

### Must-Have

**5.1 Multi-cluster topology model** [Complexity: High]
Add a `Cluster` entity to the data model: a named, independently managed group of nodes with its own clonr-serverd instance. Introduce a federation layer — a lightweight control-plane-of-control-planes — that: (a) knows about all clusters and their health, (b) replicates image metadata and blobs across clusters, (c) provides a unified API for cross-cluster operations.

The federation layer is a separate binary: `clonr-federationd`. It connects to multiple `clonr-serverd` instances via their REST APIs. It does not replace the per-cluster server — each cluster remains independently operable without the federation layer. Federation is an opt-in overlay.

```
clonr-federationd
├── cluster-a: clonr-serverd (site 1)
├── cluster-b: clonr-serverd (site 2)
└── cluster-c: clonr-serverd (site 3)
```

The API contract between `clonr-federationd` and each `clonr-serverd` instance is the existing v1 API, with one addition: a `POST /api/v1/images/:id/replicate` endpoint that accepts a push of an image blob from the federation layer.

**5.2 Image replication across sites** [Complexity: High]
When an image is promoted to `production` stage in one cluster, the federation layer can automatically replicate it to other clusters. Replication uses chunked HTTP transfers with content-addressed deduplication — if a cluster already has a layer from 3.3 that is shared with the new image, only the delta is transferred.

Dependency: 5.1, 3.3.

**5.3 Cross-cluster node migration** [Complexity: High]
Capture a node's running state as an image in cluster A, replicate the image to cluster B, and provision an equivalent node in cluster B. This is not live migration — it is snapshot-and-restore across clusters. Useful for: moving workloads off a cluster undergoing maintenance, provisioning a spare node at a different site when a node fails.

Dependency: 5.2.

**5.4 Multi-tenant access control** [Complexity: Medium]
Replace the single pre-shared token with a proper auth model: namespaced API tokens, per-namespace image and node isolation, and role-based permissions (reader, deployer, admin). At the federation layer: cluster-wide admin roles and cross-cluster image promotion rights.

The existing `bearerAuth` middleware is the extension point. The `NodeConfig.Groups` field maps naturally to tenant namespaces. Schema additions: `api_tokens` table with `id, name, secret_hash, permissions, created_at, expires_at`.

This is a schema and API change that must be done before v3.0 ships publicly. Once external users have tokens issued by the v1 auth model, migration is painful.

Dependency: 5.1.

### Nice-to-Have

**5.5 Centralized audit log** [Complexity: Low]
Aggregate deploy events, image promotions, and user actions across all clusters into a centralized audit log at the federation layer. Required for compliance in academic HPC environments (NSF/DOE computing facilities have audit requirements).

**5.6 Cross-cluster resource planning** [Complexity: Medium]
"Cluster A has 20 nodes failing hardware checks. Cluster B has 40 spare nodes. Migrate partition X's workloads." The federation layer has the data to answer this — node counts, hardware profiles, cluster utilization. Surfacing it in the UI is the work.

---

## Phase 6: Ecosystem and Integrations (v3.x)

**Goal:** clonr is not an island. Sites have existing tooling and workflows. Meeting them where they are increases adoption.

### Must-Have

**6.1 Terraform provider** [Complexity: Medium]
A Terraform provider for clonr that exposes:
- `clonr_node_config` resource (maps to `NodeConfig`)
- `clonr_node_group` resource (maps to `NodeGroup`)
- `clonr_image` data source (read-only reference to a `BaseImage` by name/version)
- `clonr_deploy_job` resource (triggers a rolling deployment)

This is how infrastructure-as-code shops manage their clusters. Terraform lets them define their entire cluster topology in HCL and apply it idempotently. The clonr API already has the right CRUD shape to back a Terraform provider.

**6.2 Ansible modules** [Complexity: Low]
Python modules for the most common operations:
- `clonr_image_pull` — pull an image from a URL
- `clonr_node` — create or update a node config
- `clonr_deploy` — deploy an image to a node or group
- `clonr_facts` — gather hardware facts from a node and return as Ansible facts

Many HPC sites run Ansible for post-provision configuration. Ansible modules make clonr fit into existing playbooks.

**6.3 cloud-init compatibility** [Complexity: Medium]
During `Finalize`, generate a `cloud-init` NoCloud data source (user-data + meta-data) in the deployed filesystem. This makes clonr-provisioned nodes compatible with software that assumes cloud-init ran at boot: Ansible facts gathering, cloud-init-aware application installers, and hybrid environments where some nodes come from a cloud provider.

The `NodeConfig.CustomVars` map is the natural source for cloud-init user-data variables. A template system (from 2.6) feeds the user-data generation.

**6.4 Ignition support** [Complexity: Medium]
Fedora CoreOS and RHEL CoreOS use Ignition for first-boot provisioning. Add support for generating an Ignition config from `NodeConfig` and placing it where Ignition expects it. This extends clonr's reach to immutable OS deployments — a growing pattern in HPC environments that want to minimize state drift.

**6.5 Plugin system** [Complexity: High]
Add a plugin interface that lets operators extend clonr without forking it. Plugin hooks:
- `pre-deploy` / `post-deploy` — run a script before/after deployment
- `image-validate` — custom validation step in the patching pipeline
- `hardware-classify` — custom logic for assigning nodes to groups based on hardware profile

Implement as a directory of executable scripts invoked via stdin/stdout JSON. This is the `git hooks` model — simple, language-agnostic, inspectable. Avoid a dynamic library plugin model; it complicates CGO_ENABLED=0 builds.

### Nice-to-Have

**6.6 Kubernetes bare-metal provisioner (Cluster API provider)** [Complexity: High]
A Cluster API infrastructure provider that uses clonr to provision bare-metal nodes as Kubernetes worker nodes. This bridges the gap between HPC provisioning (clonr's domain) and Kubernetes workloads (a growing pattern in research computing). The provider would: allocate a node from a group, deploy a Kubernetes worker image, join the node to the cluster, and decommission it on scale-down.

**6.7 OpenHPC integration** [Complexity: Medium]
OpenHPC is the de facto package repository for HPC software stacks. Document and automate the workflow for building an OpenHPC-based image with clonr: pull a base Rocky 9 image, add OpenHPC repos in a chroot session, install the OpenHPC base stack, finalize and capture. Ship this as a reference `Clonrfile` (from 3.5) that sites can fork.

---

## What We Are Not Building

These are deliberate exclusions, not gaps.

**General-purpose configuration management.** Puppet, Chef, Ansible, and SaltStack are the right tools for ongoing application configuration drift management across a fleet. clonr does not replace them. The distinction is scope: clonr owns cluster infrastructure layer — the scheduler, the identity system, the image — because these are prerequisites for a functional HPC cluster, not application concerns. Anything that runs on top of a working cluster is out of scope.

**Network switch configuration.** Provisioning a node is not the same as configuring the leaf switch it connects to. Switch configuration is topology-specific and vendor-specific. We emit hardware profiles that contain the information a network automation tool needs; we do not automate network configuration.

**SLURM workload scheduling.** clonr deploys and configures SLURM (see 2.10) but does not schedule jobs, manage job queues, or make allocation decisions. That is SLURM's job. The boundary is: clonr stands up a correct, running SLURM installation; everything after that is the scheduler's domain.

**Paid SaaS.** The product is self-hosted. The code is MIT licensed. The business model (if any) is support, training, and enterprise extensions. We will not add telemetry, licensing enforcement, or feature flags behind a payment wall.

---

## Execution Priorities

The phases above are ordered by dependency and value, but here is the honest sequencing for a small team:

**Now (v1.0, 2-3 months):**
Do 1.1, 1.2, 1.4, and 1.5 in that order. These are correctness and reliability fixes. Without them, demos look good and production is a liability. Then do 1.8 (integration tests) — this is the only way to know if 1.1 and 1.2 actually work. Ship 1.3, 1.7, 1.9, 1.10, and 1.11 in parallel — they are independent and low-complexity.

**Next (v1.x, 3-6 months after v1.0):**
2.1, 2.3, and 2.5 unlock the core fleet management value proposition and are prerequisites for everything else. Do 2.4 (SLURM drain/resume) alongside 2.1 — it is cheap to wire in once groups exist. Then implement 2.13 (node roles) before any cluster stack automation. Roles are the topology primitive that 2.10, 2.11, and 2.12 all depend on — without them, the SLURM deployment feature requires operators to manually specify which node is the head node, which nodes are compute, and so on for every cluster operation. With roles, that intent is declared once and all automation resolves from it. After 2.13 is in, tackle the cluster stack automation features in this order: 2.11 (user/group management) first, because it has no external dependencies and delivers immediate value; 2.12 (LDAP) second, because it builds on 2.11's user store; 2.10 (SLURM deployment) last, because it depends on both 2.11 for account linkage and 2.12 for full user resolution. Before implementing 2.10, talk to two or three real HPC sysadmins about their exact slurm.conf conventions and partition expectations — the details are site-specific and it is easy to generate a config that is structurally valid but wrong for real workloads.

**Later (v2.0+):**
3.1 and 3.2 are the capabilities that make HPC centers choose clonr over Warewulf for long-term image lifecycle management. Phase 3 is where clonr establishes a durable moat. Phases 4, 5, and 6 are valuable but should be driven by actual user pull, not roadmap ambition.

**The test:** At the end of Phase 2, a sysadmin should be able to: (a) rack servers, (b) PXE boot them through clonr, (c) assign roles — one node as `head-node`, the rest as `compute` or `gpu-compute` — and have clonr derive the full cluster topology from those declarations, (d) run `clonr slurm deploy` and get a functional SLURM cluster with partitions derived from hardware and role assignments, (e) create users via the clonr API and have them appear on every compute and login node with SSH access, and (f) roll a new image to the compute partition without draining it manually. If that end-to-end workflow takes less than an hour of operator time on a 200-node cluster, clonr has earned its place in real HPC environments.
