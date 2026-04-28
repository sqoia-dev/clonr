# First Job — Researcher Getting Started Guide

This document walks a researcher through submitting their first Slurm job on a clustr-managed cluster. It assumes the cluster operator has already:

1. Installed clustr and deployed at least one controller node and one compute node.
2. Enabled the Slurm module and built a Slurm-enabled image.
3. Created your user account (see [Approach A](#approach-a-local-sysaccounts) or [Approach B](#approach-b-ldap)).

**Contents**

1. [Get your account](#1-get-your-account)
2. [Log in to the cluster](#2-log-in-to-the-cluster)
3. [Verify your access](#3-verify-your-access)
4. [Submit your first job](#4-submit-your-first-job)
5. [Check job status and output](#5-check-job-status-and-output)
6. [Common failures and fixes](#6-common-failures-and-fixes)

---

## 1. Get your account

Before you can submit jobs, your account must exist on every node in the cluster with a consistent UID and GID. Ask your cluster administrator which provisioning method is in use.

### Approach A: Local sysaccounts

Your administrator creates your account via the clustr web UI (**System > Accounts**) or API, then reimages the cluster nodes to inject it. After reimage completes, your account (`alice`, UID `2001`, etc.) exists in `/etc/passwd` on every node.

**What you receive from your admin:**
- Your username and UID (e.g. `alice`, UID `2001`)
- A temporary password or SSH public key to set up

**Set your password** (ask your admin for SSH access to the controller as root):

```bash
# Admin runs on the controller node:
echo "alice:InitialPass1!" | chpasswd
# Repeat on each additional node if not using NFS home
```

Your account lands with a locked password after the sysaccounts injection. The admin must set it explicitly.

### Approach B: LDAP

Your administrator creates your account in the clustr LDAP module. No reimage is required — your account is available on all nodes immediately via sssd.

**What you receive from your admin:**
- Your LDAP username and initial password
- The controller hostname or IP

---

## 2. Log in to the cluster

Researchers access cluster resources via SSH into the controller node (the node with the `controller` Slurm role). Your administrator provides the hostname or IP.

```bash
ssh alice@10.99.0.100
# or, if a DNS name is configured:
ssh alice@hpc-login.example.edu
```

**If you use SSH keys**, add your public key to the node. For sysaccounts clusters, ask your admin to add it to `~alice/.ssh/authorized_keys` on the controller. For LDAP clusters, consult your admin — some sites configure sssd with LDAP-managed SSH keys.

**OnDemand portal:** If your cluster runs Open OnDemand and is using the resolvr SPA proxy, your admin will give you a URL of the form `https://ondemand.example.edu/`. Log in with your cluster credentials.

---

## 3. Verify your access

Once logged in to the controller, confirm Slurm is reachable and your account is recognized:

```bash
# Confirm you are who you think you are
id
# Expected: uid=2001(alice) gid=2000(users) groups=2000(users)
# UID must match on all nodes — inconsistency causes silent job failures

# Check partition and node status
sinfo
# Expected: one or more partitions with nodes in "idle" state
# If sinfo returns nothing or hangs, see Common failures §6

# Confirm munge authentication works
munge -n | unmunge
# Expected: STATUS: Success (0)
# If this fails, Slurm job submission will fail with authentication errors
```

---

## 4. Submit your first job

### Interactive job (recommended first test)

```bash
# Request one task on one node — runs hostname immediately
srun --nodes=1 --ntasks=1 hostname
# Expected: the worker node's hostname (e.g. "hpc-compute-001")
```

### Batch job

Create a simple job script:

```bash
cat > ~/first-job.sh <<'EOF'
#!/bin/bash
#SBATCH --job-name=first-job
#SBATCH --output=first-job-%j.out
#SBATCH --ntasks=1
#SBATCH --nodes=1

echo "Job started on $(hostname) at $(date)"
echo "Running as: $(id)"
srun hostname
echo "Job complete"
EOF

sbatch ~/first-job.sh
# Expected: Submitted batch job 1
```

### Check your job ran

```bash
# View output file (replace 1 with your job ID)
cat ~/first-job-1.out
# Expected: lines showing hostname, your user identity, and "Job complete"
```

---

## 5. Check job status and output

```bash
# Active jobs
squeue --me

# Completed jobs (requires sacct / slurmdbd — check with your admin)
sacct -j 1 --format=JobID,Partition,State,ExitCode

# Node assignment and resource usage
scontrol show job 1
```

**Output files:** With `#SBATCH --output=first-job-%j.out`, the output file lands in the directory where you ran `sbatch`. If your home directory is not NFS-mounted on the worker nodes, the output file may be on the worker, not the controller. NFS home directories are the standard solution — ask your admin.

---

## 6. Common failures and fixes

### `srun: error: Unable to connect to Slurm daemon on node`

**Cause:** `slurmd` is not running on the worker node, or Slurm cannot authenticate.

**Fix:**
```bash
# Check slurmd on the worker (ask admin or use SSH)
ssh root@hpc-compute-001 'systemctl status slurmd'

# Check munge on both nodes
munge -n | unmunge                          # on controller
ssh root@hpc-compute-001 'munge -n | unmunge'  # on worker
```

If munge fails: the munge key (`/etc/munge/munge.key`) may differ between nodes. All nodes must share the same munge key. This is injected automatically by clustr's sysaccounts module, but only if the `munge` system account and key were present in the image. Ask your admin to verify.

### `srun: error: Invalid partition name specified`

**Cause:** The partition name in `slurm.conf` does not match what you typed, or the partition is not configured.

**Fix:**
```bash
sinfo                        # shows available partitions and their states
scontrol show partition      # shows full partition configuration
```

Use the partition name exactly as shown in `sinfo` output. The default partition is typically `batch`, `debug`, or the cluster name — ask your admin.

### `sbatch: error: User alice not permitted to use account`

**Cause:** Slurm accounting is enabled (`slurmdbd`) and your user is not associated with a Slurm account.

**Fix:** Ask your admin to run:
```bash
sacctmgr add user alice Account=hpc DefaultAccount=hpc
```

See [docs/user-management.md §7.3](user-management.md#73-slurm-accounting-slurmdbd) for background.

### Job is queued but never starts (PD state in `squeue`)

**Cause:** No nodes are available, nodes are in `draining` or `down` state, or resource request exceeds partition limits.

**Fix:**
```bash
squeue --me                     # check your job reason (column NODELIST/REASON)
sinfo                           # check node states
scontrol show node hpc-compute-001  # detailed node state
```

Common reasons: `Resources` (nodes busy), `Priority` (fair-share queue), `PartitionNodeLimit` (requesting more nodes than the partition allows), `ReqNodeNotAvail` (requested node is down).

### `ssh: connect to host 10.99.0.100 port 22: Connection refused`

**Cause:** The controller node is not up, or SSH is not enabled in the base image.

**Fix:** Check with your admin. The cluster may still be deploying, or the node may need a reimage.

### My home directory doesn't exist on worker nodes

**Cause:** Home directories on local storage are per-node. Jobs that try to `chdir` to `$HOME` on a worker will fail if the directory was only created on the controller.

**Fix (sysaccounts clusters):** Ask your admin to either:
- Configure NFS-mounted home directories (standard for multi-user clusters), or
- Enable `create_home: true` in the sysaccounts module (creates the directory at first PAM login on each node).

---

## Next steps

- **More Slurm options:** `man srun`, `man sbatch`, `man scontrol`
- **Array jobs:** `sbatch --array=1-10 myjob.sh`
- **Resource requests:** `--cpus-per-task`, `--mem`, `--gres=gpu:1`
- **Job dependencies:** `sbatch --dependency=afterok:1 job2.sh`
- **Cluster user management:** [docs/user-management.md](user-management.md)
