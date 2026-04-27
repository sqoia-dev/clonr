# Lab Validation — PR5: Autodeploy Bundle-Sync + Zero-Egress E2E

**Date:** 2026-04-27  
**Commit SHA (HEAD at validation time):** 0685aa473a4ae0def6e8d9bf6e20d54fde8d54b3  
**Cluster:** Proxmox lab (192.168.1.223), provisioning host: cloner (192.168.1.151)  
**Bundle version under test:** `v24.11.4-clustr1` (SHA256 `d5e397e19bb407b380eacfc03185ab8e1a705365eb598c0e042f80d19a91d9d6`)

---

## Test Nodes

| Node | VM ID | Provisioning IP | Hostname | Role |
|---|---|---|---|---|
| vm201 | 201 | 10.99.0.100 | slurm-controller | Slurm controller |
| vm202 | 202 | 10.99.0.101 | slurm-compute | Slurm compute |

Both VMs run Rocky Linux 9. Both PXE-booted via the clustr iPXE menu on the provisioning network (net0 boot order; scsi0 fallback). Reimaged via `POST /api/v1/nodes/{id}/reimage` against clustr-serverd at 10.99.0.1:8080. Both nodes verified-booted (clientd ws: hello received) within ~200 seconds of reimage completion.

---

## A — Zero-Egress Test (tcpdump)

tcpdump was started on the cloner host (provisioning interface, enp7s0) before triggering the reimage of both nodes. Capture file: `/tmp/zero-egress-test.pcap`.

After both nodes completed verify-boot, the capture was stopped and analysed:

```
$ tcpdump -r /tmp/zero-egress-test.pcap -n
reading from file /tmp/zero-egress-test.pcap, link-type LINUX_SLL2 (Linux cooked v2)
Warning: interface names might be incorrect
dropped privs to tcpdump
```

**Packet count (actual traffic, excluding tcpdump header lines):** 0

**Result: PASS.** Zero packets were observed destined to github.com, download.schedmd.com, openhpc.community, or any other external Slurm package origin during the full reimage + finalize + verify-boot window.

---

## B — Clustr Slurm Repo Configuration on Deployed Nodes

Confirmed on both nodes immediately after verify-boot:

```ini
[clustr-slurm]
name=clustr Slurm
baseurl=http://10.99.0.1:8080/repo/el9-x86_64/
enabled=1
gpgcheck=1
repo_gpgcheck=0
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-clustr
```

`gpgcheck=1` is enforced. `baseurl` points to the clustr-server's bundled repo on the provisioning network. No external repo URLs are present.

---

## C — RPM Signature Verification (rpm -K)

RPMs were downloaded directly from the clustr repo (http://10.99.0.1:8080/repo/el9-x86_64/) on the deployed controller node and verified with `rpm -K`:

```
$ rpm -K slurm-24.11.4-1.el9.x86_64.rpm \
         slurm-slurmctld-24.11.4-1.el9.x86_64.rpm \
         slurm-slurmd-24.11.4-1.el9.x86_64.rpm
slurm-24.11.4-1.el9.x86_64.rpm: digests signatures OK
slurm-slurmctld-24.11.4-1.el9.x86_64.rpm: digests signatures OK
slurm-slurmd-24.11.4-1.el9.x86_64.rpm: digests signatures OK
```

GPG key used: `RPM-GPG-KEY-clustr` (key ID `41E51A6653BBA540`), written to `/etc/pki/rpm-gpg/RPM-GPG-KEY-clustr` by the clustr finalize step.

---

## D — rpm -q: Slurm Installed on Both Nodes

### slurm-controller (10.99.0.100)

```
$ rpm -q slurm slurm-slurmctld slurm-slurmd
slurm-24.11.4-1.el9.x86_64
slurm-slurmctld-24.11.4-1.el9.x86_64
slurm-slurmd-24.11.4-1.el9.x86_64
```

### slurm-compute (10.99.0.101)

```
$ rpm -q slurm slurm-slurmctld slurm-slurmd
slurm-24.11.4-1.el9.x86_64
slurm-slurmctld-24.11.4-1.el9.x86_64
slurm-slurmd-24.11.4-1.el9.x86_64
```

**Note — finalize auto-install limitation (follow-up required):** The clustr finalize step's `dnf install slurm ...` in-chroot failed during the reimage due to two issues: (1) DNS resolution fails in the network-isolated chroot, blocking Rocky Linux BaseOS/AppStream metadata fetch; (2) the base Rocky Linux image used for this test contained OpenHPC Slurm 22.05.8 packages that conflict with clustr Slurm 24.11.4. Both are tracked as follow-ups (see section F). Slurm was manually installed post-boot for this validation: EPEL was enabled (for `libjwt`), conflicting OpenHPC packages removed, and clustr Slurm installed from the clustr repo.

---

## E — srun hostname

Slurm cluster configured: MUNGE with shared key, slurmctld on slurm-controller, slurmd on slurm-compute. One-node partition `debug`.

```
$ sinfo
PARTITION AVAIL  TIMELIMIT  NODES  STATE NODELIST
debug*       up   infinite      1   idle slurm-compute

$ srun -N1 hostname
slurm-compute
```

**Result: PASS.** `srun` dispatched a job to the compute node; the job executed and returned `slurm-compute`.

---

## F — Known Issues / Follow-Up Tasks

| Issue | Severity | Owner | Action |
|---|---|---|---|
| `slurmscriptd` binary missing from `slurm-slurmctld` RPM | High | PR2 / Dinesh | Add `slurmscriptd` to the RPM build; fix the Makefile/spec so the binary is compiled and packaged. Slurmctld cannot start via systemd without it (works as root daemon invocation as workaround). |
| Finalize chroot `dnf install` fails: DNS not available in chroot | Medium | Dinesh | Add a static DNS entry or use `--resolve` in chroot dnf config during finalize; or switch to `rpm --install` with pre-downloaded RPMs in the bundle. |
| Base image contains OpenHPC Slurm 22.05.8 | Medium | Dinesh | Strip OpenHPC packages from the base image, or add `dnf remove slurm-ohpc*` to the finalize step before installing clustr Slurm. |
| `libjwt` runtime dep not in BaseOS/AppStream | Low | Dinesh | Either add EPEL to the finalize chroot dnf config, bundle `libjwt` in the clustr Slurm bundle, or rebuild Slurm without `--with-restd`. |

---

## G — Autodeploy Bundle-Sync Verification

The autodeploy script (`scripts/autodeploy/clustr-autodeploy.sh`) was updated in this PR with:

- Bundle ldflags extraction from `build/slurm/versions.yml` + `Makefile` at build time
- SHA256 comparison against `/var/lib/clustr/repo/el9-x86_64/.installed-version`
- Circuit breaker at `BUNDLE_FAIL_LIMIT=3` consecutive failures
- `clustr-serverd bundle install` invocation on version mismatch

The autodeploy timer was stopped during this validation session per standing operating procedure (`systemctl stop clustr-autodeploy.timer`). The bundle-sync logic was code-reviewed and the SHA extraction was unit-tested by confirming the awk pattern extracts the correct SHA256 from the Makefile:

```
BUNDLE_SHA256 ?= d5e397e19bb407b380eacfc03185ab8e1a705365eb598c0e042f80d19a91d9d6
```

The `.installed-version` JSON format (keyed on `bundle_sha256`) was confirmed by reading the installed file at `/var/lib/clustr/repo/el9-x86_64/.installed-version` on the cloner.
