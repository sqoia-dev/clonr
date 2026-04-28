# User Management

This document covers how human user accounts are provisioned across a clustr-managed cluster, why this matters for Slurm specifically, and the three approaches available from simplest to most robust.

**Contents**

1. [Why this matters](#1-why-this-matters)
2. [System users vs. human users](#2-system-users-vs-human-users)
3. [Approach A — Local users via sysaccounts (lab clusters)](#3-approach-a--local-users-via-sysaccounts-lab-clusters)
4. [Approach B — Centralized LDAP via the clustr LDAP module (production)](#4-approach-b--centralized-ldap-via-the-clustr-ldap-module-production)
5. [Approach C — External LDAP or NIS (bring your own)](#5-approach-c--external-ldap-or-nis-bring-your-own)
6. [Smoke test — submit a job as alice](#6-smoke-test--submit-a-job-as-alice)
7. [Slurm-specific considerations](#7-slurm-specific-considerations)
8. [Coordination with Dinesh — system user verification](#8-coordination-with-dinesh--system-user-verification)

---

## 1. Why this matters

Slurm is a multi-node distributed system. When `slurmctld` on the controller schedules a job to run on `slurmd` on a worker, it does so as a specific Linux user (e.g. `alice`, UID 2001). The job process runs as that UID on the worker. Files written during the job are owned by that UID.

This creates a hard requirement: **every user who submits Slurm jobs must exist on every node in the cluster with the same UID and GID.** If `alice` is UID 2001 on the controller but UID 2005 on a worker, or does not exist at all on the worker, one of the following happens:

- The job fails at spawn time with a permission error.
- The job runs but file ownership is mismatched, causing silent data corruption or access denial.
- `slurmd` refuses to start the task (uid not found in `/etc/passwd`).

Slurm does not provision users — it consumes them. Getting users onto every node with consistent UID/GID mapping is an operator responsibility. clustr provides two built-in mechanisms to automate this.

---

## 2. System users vs. human users

There are two distinct categories of users relevant to a Slurm cluster:

**System daemon users** (`slurm`, `munge`) are created automatically when Slurm packages are installed. The `slurm` user owns spool directories and state files; the `munge` user owns the munge socket and key. These are handled by the package manager via RPM/deb `%pre` scripts and do not require operator action. Clustr's auto-install step installs the packages, which triggers this creation. See [§8](#8-coordination-with-dinesh--system-user-verification) for verification.

**Human users** (`alice`, `bob`, your actual cluster users who submit jobs) do not exist until the operator provisions them. This document covers how to do that.

The two categories are independent. System daemon users work out of the box. Human users require explicit setup via one of the three approaches below.

---

## 3. Approach A — Local users via sysaccounts (lab clusters)

The `sysaccounts` module injects local POSIX accounts and groups directly into every deployed node's `/etc/passwd`, `/etc/group`, and `/etc/shadow` during the finalize step. This happens at reimage time, before the node boots. No external dependencies, no network services, no additional packages required on the nodes.

**When to use:** Lab clusters, single-operator setups, clusters with a small fixed user list that rarely changes.

**Limitations:** The user list is pinned in the clustr database. Adding or removing a user requires a reimage of all affected nodes to propagate the change. Not appropriate for clusters where users are added frequently or managed by HR/directory systems.

### 3.1 Create the POSIX group

```bash
# Create the users group (GID 2000)
curl -s -X POST http://10.99.0.1:8080/api/v1/sysaccounts/groups \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name":        "users",
    "gid":         2000,
    "description": "HPC cluster users"
  }' | python3 -m json.tool
```

### 3.2 Create user accounts

```bash
# Create alice (UID 2001, primary group: users GID 2000)
curl -s -X POST http://10.99.0.1:8080/api/v1/sysaccounts/accounts \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "username":     "alice",
    "uid":          2001,
    "primary_gid":  2000,
    "shell":        "/bin/bash",
    "home_dir":     "/home/alice",
    "create_home":  true,
    "comment":      "Alice Example"
  }' | python3 -m json.tool

# Create bob (UID 2002)
curl -s -X POST http://10.99.0.1:8080/api/v1/sysaccounts/accounts \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "username":     "bob",
    "uid":          2002,
    "primary_gid":  2000,
    "shell":        "/bin/bash",
    "home_dir":     "/home/bob",
    "create_home":  true,
    "comment":      "Bob Example"
  }' | python3 -m json.tool
```

**UID/GID assignment guidance:**

- Use the range 2000–59999 for human users. Avoid 0–999 (system accounts) and 1000–1999 (OS-created users that may conflict on some distributions).
- Assign UIDs consistently across your organization. If you already have a UID scheme from a previous NIS or LDAP deployment, carry it over exactly.
- GIDs below 1000 are reserved on most Linux distributions. Use 2000+ for cluster-specific groups.

### 3.3 Verify the configuration

```bash
# List groups
curl -s http://10.99.0.1:8080/api/v1/sysaccounts/groups \
  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool

# List accounts
curl -s http://10.99.0.1:8080/api/v1/sysaccounts/accounts \
  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool
```

### 3.4 Reimage to apply

Accounts and groups defined in the sysaccounts module are injected at the next reimage. The current node state is not modified in place. After adding users, reimage all nodes so every node receives the same `/etc/passwd` and `/etc/group`:

```bash
# Reimage the controller
curl -s -X POST http://10.99.0.1:8080/api/v1/nodes/$CTRL_NODE_ID/reimage \
  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool

# Reimage the worker(s)
curl -s -X POST http://10.99.0.1:8080/api/v1/nodes/$WORK_NODE_ID/reimage \
  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool
```

### 3.5 Set a password post-deploy

The sysaccounts module does not inject password hashes — accounts land with a locked password (`!` in `/etc/shadow`). Set passwords after first boot:

```bash
# SSH into the node as root, then:
echo "alice:changeme" | chpasswd

# For a one-liner from the provisioning host:
ssh root@10.99.0.100 'echo "alice:changeme" | chpasswd'
ssh root@10.99.0.101 'echo "alice:changeme" | chpasswd'
# Repeat for every node in the cluster
```

For lab use, you may also inject an authorized_keys file for the user as part of a post-deploy script. The sysaccounts module handles account creation; SSH key deployment is outside its scope.

**SSH key provisioning via the API (operator's own key):** The node `ssh_keys` field in clustr injects keys into root's `authorized_keys` only. To deploy SSH keys for human users, use a post-deploy script or Ansible run after the node reaches `verified_booted`.

---

## 4. Approach B — Centralized LDAP via the clustr LDAP module (production)

The clustr LDAP module runs `slapd` (OpenLDAP) directly on the clustr-serverd host and configures each deployed node to authenticate against it via `sssd`. This gives you a single source of truth for all cluster users. Adding a user once makes them available on every node immediately, without reimaging.

**When to use:** Multi-user production clusters, environments with regular user turnover, teams that already have LDAP operational knowledge.

**What clustr provisions automatically:**

- Installs `openldap-servers` on the clustr-serverd host.
- Generates a self-signed CA and server TLS certificate (LDAPS on port 636).
- Seeds the DIT with base DN, `ou=people`, `ou=groups`, `ou=services`, and a read-only `node-reader` service account.
- At node deploy time, writes `sssd.conf` with the node-reader credentials and the CA cert bundle into the deployed rootfs so the node authenticates against LDAP on first boot.

### 4.1 Enable the LDAP module

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/ldap/enable \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "base_dn":        "dc=cluster,dc=local",
    "admin_password": "<strong-password-for-directory-manager>"
  }' | python3 -m json.tool
# Returns 202 immediately — provisioning runs async (30–60 seconds)
```

Poll until `status` is `ready`:

```bash
watch -n 5 "curl -s http://10.99.0.1:8080/api/v1/ldap/status \
  -H 'Authorization: Bearer $TOKEN' | python3 -m json.tool"
# Wait for: "status": "ready"
```

**Base DN selection:** `dc=cluster,dc=local` is appropriate for an isolated HPC cluster. If this cluster is part of a larger LDAP infrastructure, choose a base DN that fits your organization's namespace, such as `dc=hpc,dc=example,dc=com`. The base DN is locked after the first node is configured — choose carefully.

### 4.2 Create the POSIX group in LDAP

```bash
# Create a "users" group (GID 2000) in LDAP
curl -s -X POST http://10.99.0.1:8080/api/v1/ldap/groups \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "cn":          "users",
    "gid_number":  2000,
    "description": "HPC cluster users"
  }' | python3 -m json.tool
```

### 4.3 Create user accounts in LDAP

```bash
# Create alice (UID 2001)
curl -s -X POST http://10.99.0.1:8080/api/v1/ldap/users \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "uid":            "alice",
    "uid_number":     2001,
    "gid_number":     2000,
    "cn":             "Alice Example",
    "sn":             "Example",
    "home_directory": "/home/alice",
    "login_shell":    "/bin/bash",
    "password":       "<initial-password>"
  }' | python3 -m json.tool

# Create bob (UID 2002)
curl -s -X POST http://10.99.0.1:8080/api/v1/ldap/users \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "uid":            "bob",
    "uid_number":     2002,
    "gid_number":     2000,
    "cn":             "Bob Example",
    "sn":             "Example",
    "home_directory": "/home/bob",
    "login_shell":    "/bin/bash",
    "password":       "<initial-password>"
  }' | python3 -m json.tool
```

The `ppolicy` overlay enforces a minimum password length of 8 characters and locks accounts after 5 failed authentications. Users are required to change their password on first login (`pwdMustChange: TRUE`).

### 4.4 Reimage nodes to inject sssd configuration

LDAP configuration (`sssd.conf`, `ldap.conf`, CA certificate) is injected into each node's rootfs at reimage time. Nodes that were deployed before the LDAP module was enabled will not have `sssd` configured — reimage them to pick up the LDAP configuration:

```bash
curl -s -X POST http://10.99.0.1:8080/api/v1/nodes/$CTRL_NODE_ID/reimage \
  -H "Authorization: Bearer $TOKEN"
curl -s -X POST http://10.99.0.1:8080/api/v1/nodes/$WORK_NODE_ID/reimage \
  -H "Authorization: Bearer $TOKEN"
```

After the reimage completes and the nodes reach `verified_booted`, `sssd` starts automatically on first boot and connects to slapd on the clustr-serverd host.

### 4.5 Verify LDAP authentication on a node

```bash
ssh root@10.99.0.100   # controller node

# sssd must be running
systemctl status sssd

# NSS lookup — should resolve alice's UID
id alice
# Expected: uid=2001(alice) gid=2000(users) groups=2000(users)

# Resolve all LDAP users via NSS (enumerate must be enabled in sssd.conf for this to work;
# in v1 sssd.conf it is disabled for performance — use "id <username>" instead)
getent passwd alice

# PAM authentication test
su - alice
# Enter alice's password — should succeed and land in /home/alice
```

If `id alice` returns `no such user`, check:

```bash
# sssd logs
journalctl -u sssd -n 50

# Direct LDAP bind test from the node (verify connectivity and credentials)
ldapsearch -H ldaps://10.99.0.1:636 \
  -D "cn=node-reader,ou=services,dc=cluster,dc=local" \
  -w "<node-reader-password>" \
  -b "ou=people,dc=cluster,dc=local" \
  "(uid=alice)"
# node-reader password is in /etc/sssd/sssd.conf on the node
```

### 4.6 Adding users after initial deploy

Once the LDAP module is running and nodes have `sssd` configured (from a prior reimage), new users added via the LDAP API are available on all nodes immediately — no reimage required. `sssd` caches entries and refreshes from slapd in the background.

```bash
# Add a new user (charlie) — available on all nodes within sssd's cache TTL (5 minutes by default)
curl -s -X POST http://10.99.0.1:8080/api/v1/ldap/users \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "uid": "charlie", "uid_number": 2003, "gid_number": 2000,
    "cn": "Charlie Example", "sn": "Example",
    "home_directory": "/home/charlie", "login_shell": "/bin/bash",
    "password": "<initial-password>"
  }'

# Force sssd cache invalidation on a node if you need immediate availability:
ssh root@10.99.0.100 'sss_cache -U'
```

---

## 5. Approach C — External LDAP or NIS (bring your own)

If your organization already runs an LDAP server (Active Directory, FreeIPA, 389-DS, OpenLDAP) or NIS, you can configure nodes to authenticate against it by providing a custom `sssd.conf` that clustr injects at deploy time.

**This approach is not yet exposed via the clustr API.** In v1, `sssd.conf` is rendered from the built-in template (see `internal/ldap/templates/sssd.conf.tmpl`) and is only written when the clustr LDAP module is enabled.

**Workaround for v1:** Use a post-deploy script (delivered via the `custom_vars` node config field or a shared NFS mount) to overwrite `/etc/sssd/sssd.conf` and restart `sssd` after the node boots. This is operational overhead but functional.

The recommended path is to use Approach B (clustr LDAP) with a sync from your corporate directory via LDAP replication or a periodic sync script. A future clustr release will expose the `sssd.conf` template as a configurable field so operators can point nodes at an external LDAP server without running `slapd` on the clustr host.

---

## 5.5 Researcher access path

Once user accounts are provisioned, researchers need a way to reach the cluster to submit jobs. clustr manages the nodes and accounts — it does not provide a login portal itself. The standard access paths are:

**SSH to the controller node:** The controller node (the one with the `controller` Slurm role) accepts SSH connections from users whose accounts exist on it. Provide researchers with the controller's IP or DNS name. Researchers SSH in as their own user, not root.

```bash
ssh alice@10.99.0.100     # direct IP (lab clusters)
ssh alice@hpc.example.edu # DNS name (production)
```

**Open OnDemand portal (optional):** If your site runs OOD in a subpath proxy configuration (via [resolvr](https://github.com/sqoia-dev/resolvr)), researchers access the cluster via a web browser. The OOD portal handles job submission, file transfer, and interactive sessions. clustr configures the underlying nodes and accounts; OOD sits on top.

**Jump host / bastion:** For clusters where compute nodes are not directly reachable, researchers SSH to a login node (a designated controller or separate bastion) and submit jobs from there. Compute nodes are typically not accessible directly from the researcher's workstation — they run jobs via `slurmctld` dispatch.

**What researchers need before submitting their first job:**
1. A valid account on every cluster node (consistent UID/GID — see Approach A or B above)
2. SSH access to the controller node (either password or key-based)
3. A known Slurm partition name (get from `sinfo`)
4. Munge authentication working on all nodes

For a complete end-to-end walkthrough from account creation to `srun hostname`, see **[docs/first-job.md](first-job.md)**.

---

## 6. Smoke test — submit a job as alice

This test validates the full stack: user exists on all nodes with consistent UID/GID, Slurm accepts the job, the worker executes it as alice, and the output comes back correctly.

**Prerequisites:**

- Slurm module enabled and both controller and worker reimaged after Slurm was enabled (see [docs/slurm-module.md](slurm-module.md))
- User `alice` (UID 2001) provisioned via Approach A or B above
- `munge -n | unmunge` returns `STATUS: Success (0)` on both nodes

### 6.1 Verify alice exists on every node

```bash
# Controller
ssh root@10.99.0.100 'id alice'
# Expected: uid=2001(alice) gid=2000(users) groups=2000(users)

# Worker
ssh root@10.99.0.101 'id alice'
# Expected: uid=2001(alice) gid=2000(users) groups=2000(users)
```

UID must match on both nodes. If there is a mismatch, the job will run but file ownership will be wrong. Stop here and fix the UID conflict before continuing.

### 6.2 Create alice's home directory on the controller

For Approach A (local users), `create_home: true` in the sysaccounts config causes `pam_mkhomedir` to create the home directory on first login. To test without a PAM login, create it manually:

```bash
ssh root@10.99.0.100 'mkdir -p /home/alice && chown 2001:2000 /home/alice && chmod 700 /home/alice'
```

For Approach B (LDAP with sssd), `pam_mkhomedir` creates the home directory automatically on first PAM login. If the home directory does not exist when Slurm tries to `chdir` into it before job execution, the job may fail. Pre-create it:

```bash
ssh root@10.99.0.100 'su - alice -s /bin/bash -c "echo home created"'
# pam_mkhomedir creates /home/alice on this call
```

### 6.3 Submit the job as alice from the controller

```bash
ssh root@10.99.0.100

# Switch to alice
su - alice

# Verify Slurm is accessible
sinfo
# Expected: batch partition with worker(s) in idle state

# Single-node job
srun --nodes=1 --ntasks=1 --uid=$(id -u) hostname
# Expected: slurm-compute (or your worker hostname)

# 2-node job (requires at least 2 worker nodes)
srun -N2 hostname
# Expected: two lines, one per worker
```

**Important:** `srun` must be run as the job user, not as root, for the full Slurm UID mapping to be exercised. The `--uid` flag is not required when already running as alice; it is shown above for clarity.

### 6.4 Verify job ran as alice on the worker

After the job completes, confirm it ran as the correct UID:

```bash
# From the controller (still as alice):
srun --nodes=1 --ntasks=1 id
# Expected: uid=2001(alice) gid=2000(users) ...
```

If the output shows `uid=0` (root), Slurm is not enforcing user mapping correctly. Check `slurm.conf` for the `PrivateData` and `SlurmdUser` directives.

---

## 7. Slurm-specific considerations

### 7.1 Consistent UID/GID is non-negotiable

Repeat this for emphasis: every Slurm node must have every job user with the same UID and GID. A user on one node with a different UID causes file ownership mismatches and may cause jobs to fail in non-obvious ways. Use one of the two clustr mechanisms to enforce consistency:

- Approach A: clustr injects the same `/etc/passwd` snapshot to every node at reimage time.
- Approach B: sssd resolves every user against the same slapd, so UIDs and GIDs are always consistent.

### 7.2 cgroup.conf and job user isolation

The default `cgroup.conf` generated by the Slurm module uses `ConstrainCores=yes` and `ConstrainMemorySpace=yes`. These require that cgroup v2 be mounted and that the running user has a cgroup delegation path. On Rocky Linux 9 with systemd, this works out of the box. If jobs fail with cgroup errors:

```bash
# Check cgroup version on the worker
mount | grep cgroup
# Rocky 9 default: cgroup2 at /sys/fs/cgroup

# Check that slurmd's cgroup hierarchy is present
ls /sys/fs/cgroup/system.slice/slurmd.service/
```

If your base image uses cgroup v1 (Rocky 8 or older kernels), add `SystemdCgroupEnable=no` to `cgroup.conf` via the Slurm module config editor.

### 7.3 Slurm accounting (slurmdbd)

If you plan to use `slurmdbd` for job accounting (recommended for multi-user production clusters), every user who submits jobs must have a Slurm account association. Slurm accounts are separate from Linux accounts — they are records in the `slurmdbd` database that control resource allocation and fair-share scheduling.

After enabling accounting:

```bash
# On the controller node, as root:
# Create the default account
sacctmgr add account hpc Description="HPC cluster account" Organization="Example"

# Add alice to the account
sacctmgr add user alice Account=hpc DefaultAccount=hpc

# Verify
sacctmgr show user alice
```

Without a Slurm account association, users receive a submission error: `Unable to allocate resources: User's group not permitted to use this partition`.

`slurmdbd` requires a database backend (MariaDB or PostgreSQL). The clustr Slurm module does not currently provision a database for `slurmdbd`. This is a manual operator step. See the SchedMD `slurmdbd` documentation for setup instructions.

### 7.4 QoS and partition access

By default, all users in the cluster have access to all partitions. To restrict job submission by Slurm account or user, configure the `AllowAccounts` or `AllowQos` directives in `PartitionName` lines in `slurm.conf`. These controls operate at the Slurm layer and are independent of Linux user accounts.

### 7.5 Home directories across nodes

Slurm jobs run on worker nodes. If `alice` submits a job that writes output to `~/results/`, that path must exist and be writable on the worker. Options:

- **NFS home directories:** Mount `/home` via NFS on all nodes. Use the `extra_mounts` field on the NodeGroup to inject the NFS mount into every node's `/etc/fstab` at deploy time (see the Node Groups section in the web UI). This is the standard HPC approach.
- **Local home directories per node:** Each node has alice's home independently. Output files written during a job are local to the worker and must be copied back. Works for Approach A (sysaccounts with `create_home: true`).
- **Parallel filesystem:** Lustre, GPFS, BeeGFS, etc. mounted on all nodes. Out of scope for clustr v1 but compatible — configure via `extra_mounts` on the NodeGroup.

The NFS approach is strongly recommended for any cluster with more than one node. Without shared home directories, job output collection becomes an operator burden.

---

## 8. Coordination with Dinesh — system user verification

The auto-install step installs `slurm`, `slurm-slurmd`, `slurm-slurmctld`, `munge`, and `munge-libs` from the OpenHPC repo. These packages create the `slurm` and `munge` system users via RPM `%pre` scripts. Verify this on a deployed node:

```bash
ssh root@10.99.0.100
id slurm
# Expected: uid=<N>(slurm) gid=<N>(slurm) groups=<N>(slurm)

id munge
# Expected: uid=<N>(munge) gid=<N>(munge) groups=<N>(munge)
```

If either user is missing after a successful reimage with `slurm_repo_url` set, the package install step failed silently (check deploy logs for the WARN line). The fix is to verify the repo URL is correct and reachable, then reimage.

**If `slurm` or `munge` users are absent and you need to unblock testing immediately**, add them manually via the sysaccounts module using the canonical UIDs from the OpenHPC packages:

```bash
# These UIDs match the OpenHPC RPM %pre scripts on EL9.
# Only use this workaround if the package install step failed.

# Group: munge (GID 981 on EL9 OpenHPC)
curl -s -X POST http://10.99.0.1:8080/api/v1/sysaccounts/groups \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"munge","gid":981,"description":"munge daemon group (OpenHPC EL9)"}'

# Group: slurm (GID 982 on EL9 OpenHPC)
curl -s -X POST http://10.99.0.1:8080/api/v1/sysaccounts/groups \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"slurm","gid":982,"description":"slurm daemon group (OpenHPC EL9)"}'

# Account: munge
curl -s -X POST http://10.99.0.1:8080/api/v1/sysaccounts/accounts \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"munge","uid":981,"primary_gid":981,"shell":"/sbin/nologin","home_dir":"/var/lib/munge","system_account":true,"comment":"MUNGE authentication service"}'

# Account: slurm
curl -s -X POST http://10.99.0.1:8080/api/v1/sysaccounts/accounts \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"slurm","uid":982,"primary_gid":982,"shell":"/sbin/nologin","home_dir":"/var/spool/slurm","system_account":true,"comment":"Slurm workload manager"}'
```

Then reimage. The sysaccounts injection runs before the Slurm package install step in finalize, so the UIDs will be reserved in `/etc/passwd` before RPM attempts to create them. RPM `%pre` scripts check for the existing UID and skip creation if the user is already present.

**Note:** The canonical UIDs for `slurm` and `munge` differ by distribution. On RHEL/Rocky with OpenHPC packages they are dynamically assigned (not fixed) unless the RPM `%pre` script finds existing entries. The values above (981/982) are illustrative. Confirm the actual UIDs on a deployed node with `id slurm` and `id munge` before adding them to sysaccounts — use whatever UIDs the packages chose, not the values above. Consistency matters; the exact number does not.
