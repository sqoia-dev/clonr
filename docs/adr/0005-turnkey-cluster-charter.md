# ADR-0005: Turn-Key Cluster Charter — Scope Boundary

**Date:** 2026-04-13
**Status:** Accepted

---

## Context

The product vision is "unbox 200 servers, get to `sbatch hello-world.sh` without writing 40 Ansible scripts." This is a powerful and differentiated goal. It is also a scope that, without a hard boundary, will expand indefinitely. Every HPC admin has a list of things they wish were automated.

Scope creep into day-2 operations and application-layer management would make clonr a platform tool (Foreman/Katello territory) rather than a provisioning tool. The strategic bet is that HPC admins want a sharp, well-understood tool that does one thing excellently — not another platform to learn and maintain.

This ADR draws the permanent line. Items inside the line are clonr's responsibility. Items outside the line are explicitly out of scope — not "maybe later," but "a different tool's job."

---

## Decision

### In Scope (clonr owns this end-to-end)

**Provisioning-time operations** — everything that happens from bare metal to a running, cluster-joined node:

- PXE boot, DHCP, TFTP, iPXE chainload
- Hardware discovery (CPU, memory, disk, network, IB, BMC)
- OS image management: pull, import, capture, chroot customization, checksum, versioning
- Disk partitioning and image deployment (filesystem and block modes)
- BMC/IPMI: power control, boot device selection, SOL, sensor reads, hardware health polling
- First-boot network configuration: static IP assignment, VLAN tagging, NIC bonding, /etc/hosts, DNS registration
- First-boot identity join: SSSD/LDAP client configuration (connecting to an existing LDAP server)
- First-boot storage mounts: NFS/autofs config pushed to deployed OS
- First-boot time configuration: NTP/chrony config pushed to deployed OS
- Scheduler join: drain a node from SLURM/PBS before reimage; rejoin after successful deployment (`scontrol update`, `pbsnodes`)
- Monitoring agent deployment: drop node_exporter (or a configured agent) as part of first-boot finalize
- Node roles and topology: head, compute, gpu-compute, storage, login — role-driven service enabling/disabling
- Bulk node import from CSV; NodeConfig templating for rack-scale uniformity
- Fleet operations: rolling reimage, group-targeted deploy jobs with concurrency control
- Cluster "recipe" (v1.1): a declarative spec that maps node groups to images and roles, driving a full cluster bring-up from a single command
- LDAP server deployment on the head node (standing up the server, populating initial DIT from clonr's user store, distributing client config) — this is provisioning-time infrastructure, not day-2 management
- SLURM cluster deployment: slurm.conf generation, munge key distribution, slurmctld/slurmd enable, initial accounting structure — the initial bring-up only

### Out of Scope (explicitly not clonr's job)

**Day-2 operations and application-layer management:**

- Slurm configuration changes after initial deployment (partition tuning, QOS modifications, job priority adjustments)
- User account lifecycle beyond provisioning-time setup (HR-driven onboarding/offboarding workflows, quota enforcement policy changes, password resets)
- Storage array provisioning (LUNs, filesystems, ZFS pools, Lustre MDT/OST — clonr configures clients, not servers)
- Firmware/BIOS updates and vendor-specific BMC management (HPE iLO policy, Dell iDRAC lifecycle controller, Supermicro update tools)
- Application software management post-deploy (module system content, software stack updates, environment modules)
- Patch management and CVE remediation on running nodes (clonr provides the image patching pipeline; rolling live patches via package manager are outside scope)
- Network switch configuration (VLANs, port profiles, InfiniBand fabric management)
- Cluster scheduler policy and accounting beyond initial structure (fairshare trees, reservation management, backfill tuning)
- Compliance auditing and configuration drift detection on running nodes (that is a configuration management tool's job — Ansible, Salt, Chef)
- Multi-site network topology and WAN replication (v2.x consideration, not a charter item)
- Cloud bursting orchestration (scheduling burst jobs to cloud, managing ephemeral cloud instance lifecycle)

### The Rule

If the operation requires the node to be **running** and **in production** for more than 24 hours, it is likely day-2 and out of scope. If the operation is part of taking a node from **bare metal to cluster-joined**, it is in scope.

When evaluating a feature request: ask "does this belong in clonr, or in Ansible/Salt/whatever the site uses for configuration management?" clonr is not a configuration management tool. It is a provisioning and imaging tool with cluster-aware first-boot automation.

---

## Consequences

- Feature requests for day-2 operations will be declined with a clear pointer to this ADR. This will happen repeatedly. The team must be comfortable saying no.
- The scope boundary means clonr integrates with but does not replace Ansible, Salt, or Puppet. Documentation should explicitly say: "use clonr to get nodes into the cluster; use your configuration management tool of choice for everything after that."
- The SLURM and LDAP deployment features are bounded: clonr stands them up, it does not manage them. This is a meaningful distinction — "clonr deployed SLURM" and "clonr manages SLURM" are different products with different complexity profiles.
- The charter is written today and locked. Relaxing it requires a new ADR with explicit justification.
