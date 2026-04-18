# clonr Roadmap

clonr is a self-hosted node cloning and image management system for HPC clusters. This roadmap describes what we are building, in what order, and why each capability matters to the HPC sysadmins who run bare-metal clusters at scale.

This document is written for potential design partners and contributors. If you run an HPC cluster and any of this resonates — or if you have opinions about what is missing or wrong — we want to hear from you.

---

## Where We Are Today

The foundation is built and tested. clonr can:

- Pull cloud images (qcow2, raw, tar.gz) and import from Rocky/RHEL ISOs
- Customize images via interactive chroot sessions
- Register per-node identity (hostname, IP, SSH keys, groups) separately from image blobs
- Deploy images to bare-metal nodes via PXE with a fully integrated DHCP/TFTP/iPXE stack
- Manage IPMI/BMC power and boot-device control across Dell, HPE, Supermicro, and Lenovo hardware
- Stream structured deployment logs in real time to CLI and a built-in web UI
- Discover hardware including InfiniBand HCAs, software RAID arrays, and multi-NIC layouts
- Recover automatically from failed deployments via partition table rollback

The architecture decision that drives everything: base images are never node-specific. One image blob deploys to hundreds of nodes. Per-node identity is applied at deploy time and never baked in.

---

## What We Are Not

clonr is not:

- A configuration management system (that is Ansible, Salt, Puppet — clonr hands off cleanly to those tools post-deploy)
- A cloud provisioning platform (no EC2, no GKE — this is for bare metal)
- An orchestration layer for running jobs (no Slurm integration, no workload scheduling)
- A monitoring system (clonr does not replace Prometheus, Grafana, or Nagios)

The goal is to do one thing well: get a known-good OS image onto a bare-metal node, fast and reliably, at scale.

---

## Competitive Positioning

The existing tools HPC sysadmins use today are not bad — they are just old and narrow.

| Tool | What It Does Well | Where It Falls Short for HPC |
|---|---|---|
| **Warewulf** | Cluster-aware, stateless node images | No interactive image builds, limited web UI, steep config learning curve |
| **xCAT** | Comprehensive BMC/IPMI management, mature | XML-heavy config, complex dependencies, aging architecture |
| **Cobbler** | Kickstart/Anaconda automation, integrates with Ansible | Tied to the Kickstart workflow, poor image capture story |
| **Foreman** | Full lifecycle management, strong plugin ecosystem | Heavy: Rails app + Puppet/Ansible dependencies, complex to self-host |
| **MrProvision / in-house scripts** | Fits the exact cluster topology | No one else can maintain it when the original author leaves |

clonr's differentiation:

- **Image-first, not kickstart-first.** Build once, deploy identically to any node. No re-running Anaconda, no config drift between nodes in the same role.
- **Stateless CLI on nodes.** The `clonr` binary runs in the PXE initramfs with no external dependencies. No Python, no agent, no pre-installed packages needed on the target.
- **BMC-aware by default.** IPMI power and boot control is first-class, not bolted on.
- **InfiniBand and HPC hardware understood natively.** Not a generic datacenter tool that happens to work on clusters.
- **Simple enough to self-host without a dedicated ops engineer.** One binary, SQLite, no Kubernetes required.

---

## Phase Structure

The roadmap is organized into five phases. Each phase delivers value independently — you do not need to complete one phase to use the next, but earlier phases are the foundation later phases build on.

```
Phase 1: Foundation        (current — core deploy pipeline)
Phase 2: Commissioning     (cluster-scale provisioning operations)
Phase 3: Configuration     (post-deploy workload readiness)
Phase 4: Operations        (day-2: redeployment, fleet health, automation)
Phase 5: Scale             (multi-cluster, high-availability, integration ecosystem)
```

---

## Phase 1: Foundation

**Status: Largely complete. Active hardening.**

### What is here

The end-to-end deploy pipeline: image factory, node registry, PXE server, IPMI control, deployment engine, centralized logging, web UI.

### What still needs work

**iPXE binary supply chain**
The committed `ipxe.efi` binary has no recorded provenance — no pinned iPXE commit, no recorded build flags. For a tool used in production clusters this is a trust problem.

- Pin a specific iPXE release commit
- Add a reproducible build script (`scripts/build-ipxe.sh`) that produces a deterministic binary
- Record the version, commit SHA, and build flags in `deploy/pxe/README.md`
- Provide BIOS/legacy `undionly.kpxe` alongside the EFI binary
- Effort: 1-2 days

**Image integrity at rest**
sha256 is verified at deploy time against a server-stored checksum. There is no verification that the stored checksum itself has not drifted (e.g., partial write, filesystem corruption on the image store).

- Add periodic background checksum verification for all `ready` images
- Expose image health status in the web UI and API
- Effort: 1-2 days

**ARM64 support**
All current builds and testing are x86_64 only. As Arm-based HPC nodes (Grace Hopper, Ampere) become more common this becomes a gap.

- Extend hardware discovery for aarch64 topology
- Build and test `clonr` CLI as a static aarch64 binary for initramfs embedding
- EFI boot entry repair for aarch64
- Effort: 3-5 days (hardware-dependent for testing)

**Effort summary:** 1-2 engineer-weeks to close out Phase 1 fully.

---

## Phase 2: Commissioning

**Status: Not started. Next phase.**

Commissioning is the experience of taking a new or freshly racked cluster from zero to fully deployed. Today clonr can deploy individual nodes. Phase 2 makes deploying 200 nodes feel like deploying one.

### Bulk node registration

Today, registering nodes requires one API call per node. At cluster scale (50-500 nodes) this is impractical.

**What to build:**
- CSV/YAML import for bulk node registration (`clonr node import nodes.csv`)
- Validation: detect duplicate MACs, missing required fields, IP conflicts
- Dry-run mode that shows what would be created without committing
- Why it matters: a sysadmin racking a new 128-node partition should not spend a day on data entry

**Effort:** 3-4 days

### Parallel deployment orchestration

Today, each deployment is a single node operation. Deploying a rack requires scripting loops around the CLI.

**What to build:**
- `clonr deploy --group compute` to deploy all nodes in a named group concurrently
- Configurable concurrency limit (default: 10 parallel deployments) to avoid saturating the provisioning network
- Aggregate progress view in CLI and web UI: N/total complete, N in progress, N failed
- Abort-on-threshold: stop the batch if more than X% of nodes fail (prevent cascading badness)
- Why it matters: deploying 200 nodes one at a time is not viable in production

**Effort:** 1 week

### Deployment queuing and scheduling

Large deployments during business hours risk network saturation and disruption to running jobs.

**What to build:**
- Job queue for batch deployments with start-time scheduling
- Priority queuing: maintenance deployments vs emergency re-imaging
- Deployment windows configurable per group or cluster
- Why it matters: sysadmins need to say "deploy this rack at 2am Saturday"

**Effort:** 1 week

### BMC credential management

Today, IPMI credentials are passed as flags or per-node config fields in plaintext. At scale, rotating BMC passwords across a cluster is a manual nightmare.

**What to build:**
- Credential profiles: named sets of BMC credentials associated with node groups
- Bulk credential update: rotate the password on a credential profile and have it apply to all associated nodes
- Encrypted storage of BMC credentials at rest (not plaintext in SQLite)
- Why it matters: security hygiene and operational burden both demand this

**Effort:** 1 week

### Node discovery from network scan

**What to build:**
- `clonr discover --subnet 10.0.1.0/24` — scan the provisioning network for live BMCs, identify vendor via IPMI MC info, and pre-populate a candidate node list
- Sysadmin reviews and confirms before any node config is created
- Why it matters: building the node registry from scratch for a large cluster is the most tedious part of onboarding clonr

**Effort:** 1 week

**Phase 2 total effort estimate:** 4-6 engineer-weeks

---

## Phase 3: Configuration

**Status: Not started.**

Configuration is about what happens after the image lands. A freshly deployed compute node is not useful until it is configured for its role in the cluster. Phase 3 bridges the gap between "OS is on disk" and "node is ready to accept jobs."

### Post-deploy hook system

**What to build:**
- Per-image and per-group hook scripts that run on the node after deployment completes, before it reboots into the new OS
- Hook types: `pre-deploy` (preflight checks), `post-deploy` (first-boot configuration), `on-failure` (diagnostic capture)
- Hooks run inside the chroot before finalization; output streamed back to the log broker
- Why it matters: almost every cluster has site-specific configuration that cannot be baked into the base image (Slurm node name, site-specific resolv.conf, Munge key injection)

**Effort:** 1 week

### Secrets injection

Compute nodes need secrets at deploy time: Munge keys for Slurm auth, site CA certificates, IPA/LDAP enrollment tokens, Kerberos keytabs. These cannot be baked into image blobs.

**What to build:**
- Secrets store in clonr-serverd (encrypted at rest, never returned via API after write)
- Secret bindings: associate a named secret with a node group, injected at deploy time into a specified path
- Audit log of which secrets were injected to which nodes
- Why it matters: the current workaround is baking secrets into images, which is a security anti-pattern at cluster scale

**Effort:** 1-2 weeks (crypto and audit requirements add complexity)

### Role-based image assignment

**What to build:**
- Node roles: `compute`, `gpu`, `login`, `storage`, `lustre-oss`, etc. — defined at the cluster level
- Each role maps to a specific base image version
- `clonr node assign-role --node compute-001 --role gpu` assigns the role and its image atomically
- Why it matters: large clusters have multiple node types with distinct OS images; managing the mapping manually at scale breaks down

**Effort:** 3-4 days

### Image versioning and promotion

Today there is no formal promotion workflow for images. A sysadmin builds a new image and deploys it — there is no staging, no canary, no rollback path at the fleet level.

**What to build:**
- Image channels: `dev`, `staging`, `production`
- Promote an image version through channels: `clonr image promote <id> --to production`
- Node groups track a channel, not a specific image ID — they automatically pick up the latest promoted image on next deploy
- Why it matters: deploying an untested image to 200 nodes simultaneously is catastrophic if the image is bad

**Effort:** 1 week

**Phase 3 total effort estimate:** 4-6 engineer-weeks

---

## Phase 4: Operations

**Status: Not started.**

Phase 4 is day-2 operations: keeping a running cluster healthy, handling hardware failures, re-imaging nodes that drift, and making the system observable.

### Fleet health dashboard

**What to build:**
- Per-node deployment history: last deployed image, deployment timestamp, outcome
- Node health state: `deployed`, `drifted`, `failed`, `unknown`, `decomissioned`
- Drift detection: compare current node state (via periodic check-in) against expected image version
- Cluster-wide summary view: how many nodes are on the current production image vs stale
- Why it matters: sysadmins at 500-node clusters have no visibility into how many nodes are running what image today

**Effort:** 1-2 weeks

### Scheduled maintenance deployments

**What to build:**
- Recurring deployment schedules per node group: "deploy the latest production image to the `compute` group every Sunday at 1am"
- Integration with Slurm drain/resume: drain nodes from the scheduler before imaging, resume after
- Why it matters: keeping compute nodes current requires a process, not manual intervention after each image update

**Effort:** 1-2 weeks (Slurm integration adds scope)

### Node re-imaging triggers

**What to build:**
- Watchdog re-image: if a node fails a health check N times in a row, automatically trigger a redeploy
- Manual "nuke and pave" from web UI with one click: select node, select image, confirm, done
- Why it matters: at cluster scale, nodes that drift or fail need fast remediation without manual SSH sessions

**Effort:** 1 week

### Metrics and observability

**What to build:**
- Prometheus metrics endpoint (`/metrics`) on clonr-serverd: deployment counts, success/failure rates, deployment duration histograms, image store size, node state distribution
- Pre-built Grafana dashboard (JSON export)
- Why it matters: clonr should integrate into the monitoring stack the cluster already has, not require a separate one

**Effort:** 3-5 days

### Audit trail

**What to build:**
- Immutable audit log for all write operations: who (which API token), what action, which resource, timestamp
- Audit log queryable via CLI and web UI
- Export to syslog or a log aggregator
- Why it matters: clusters in regulated environments (national labs, DoD contractors, universities with compliance requirements) need to know who re-imaged what and when

**Effort:** 1 week

**Phase 4 total effort estimate:** 5-8 engineer-weeks

---

## Phase 5: Scale

**Status: Not started. Contingent on Phase 2-4 feedback from real deployments.**

Phase 5 addresses the operational needs of the largest deployments and multi-cluster environments. We will not design this phase in detail until we have real-world data from Phase 2-4 deployments. The items below are directional, not committed.

### Multi-cluster management

Single clonr-serverd instances managing clusters of 200-1000 nodes should work fine with the current architecture. Clusters in the thousands, or organizations managing multiple separate clusters, will need a different topology.

- Federated server mode: a central management plane aggregates multiple site-level clonr instances
- Site-local servers handle PXE and image serving; central server handles policy, credentials, and audit
- Cross-site image replication: push a new image to all sites in one operation

### High-availability server

SQLite is the right choice for single-server deployments. At the scale where HA becomes a requirement, the database layer needs to change.

- PostgreSQL backend as an alternative to SQLite
- Active-passive HA for clonr-serverd with shared image store (NFS or object storage)
- Health checks and automatic failover

### Image registry integration

Large clusters often already have internal artifact registries (Harbor, Nexus, Artifactory).

- Push/pull clonr images to OCI-compatible registries
- Use registry access control for image promotion gates
- Integrate with existing security scanning workflows (Trivy, Grype)

### External secrets integration

For clusters that already run Vault or a secrets manager:

- Vault integration for BMC credentials and injection secrets
- Avoid duplicating secrets management infrastructure

### Scheduler integration

Native integrations with Slurm and PBS/Torque:

- Drain a node group before deployment via Slurm API, not shell scripts
- Resume nodes automatically after successful deployment
- Report failed deployments back to the scheduler as unavailable

**Phase 5 total effort estimate:** 3-6 months, team-dependent. Scope defined by Phase 3-4 learnings.

---

## What to Do Right Now

Before writing more code, we need to talk to more HPC sysadmins.

### Find design partners

The goal is one to three sysadmins who manage real clusters (50+ nodes) and are willing to try clonr on a test partition or dev cluster. What we are looking for:

- What does their current provisioning workflow look like, start to finish?
- Where does it break down? What takes the most time or causes the most pain?
- What would they need to see before they would trust clonr on production nodes?
- What is in their cluster that clonr does not know about yet? (Storage hardware, specialized NICs, unusual BMC configurations)

Where to find them:

- HPC mailing lists: hpc-announce, beowulf-list
- r/HPC and HPC community Slack workspaces
- SC24/SC25 BoF sessions and birds-of-a-feather
- Campus HPC center contacts at R1 universities
- National lab sysadmin networks (XSEDE/ACCESS community)

### Specific questions to answer with design partners

1. Do sysadmins think of images as versioned artifacts (like Docker images) or as snapshots of a specific node? This determines whether the image versioning and channel model in Phase 3 is right or wrong.
2. Is Slurm drain/resume integration table stakes for Phase 4, or is a simple "wait for idle" pattern sufficient?
3. How do they handle Munge keys and IPA enrollment today? What does secrets injection need to look like to fit into their workflow without replacing their existing secret management?
4. What is the biggest cluster anyone has run clonr against? What broke?

### Open source community groundwork

- Publish the architecture doc (`docs/architecture.md`, referenced in the README but not yet written)
- Write a "Getting Started on a 3-node test cluster" guide aimed at someone who has never used clonr
- Engage the Warewulf and xCAT communities — these are the people who would consider switching, and their feedback is the most valuable

### Fix the iPXE supply chain issue

This is Phase 1 cleanup but it blocks trustworthy contributor onboarding. Nobody should be committing to a project that ships unprovenanced binaries. This is a one-day fix and it should happen before we solicit serious contributors.

---

## Effort Summary

| Phase | Scope | Estimated Effort |
|---|---|---|
| Phase 1 | Foundation hardening (iPXE provenance, ARM64, image integrity) | 1-2 engineer-weeks |
| Phase 2 | Commissioning (bulk registration, parallel deploy, BMC cred management, discovery) | 4-6 engineer-weeks |
| Phase 3 | Configuration (hooks, secrets injection, role-based images, versioning) | 4-6 engineer-weeks |
| Phase 4 | Operations (fleet health, scheduling, metrics, audit) | 5-8 engineer-weeks |
| Phase 5 | Scale (multi-cluster, HA, registry, scheduler integration) | 3-6 months |

These are single-engineer estimates assuming familiarity with the codebase. Phase 2 and 3 can be parallelized with two engineers.

---

## Guiding Principles

These shape every technical decision in the roadmap:

**Bare metal first.** Every feature is designed for the constraints of physical hardware — unreliable power sequences, inconsistent IPMI firmware, network boot quirks, non-homogeneous disk layouts. Cloud assumptions do not apply here.

**One binary on the node.** The `clonr` CLI that runs in the PXE initramfs must remain a single static binary with no runtime dependencies. Complexity lives in the server, not on the node being provisioned.

**The image is the unit of truth.** A node's state is defined by which image version was last successfully deployed to it, not by accumulated configuration changes. Remediation means redeployment, not patching.

**Air-gap first.** Every feature must work in a fully air-gapped environment. Network-dependent workflows get an offline path before they ship.

**Sysadmin ergonomics matter.** HPC sysadmins are power users. The CLI should be scriptable, composable, and assume competence. The web UI should give visibility, not hide controls behind wizards.
