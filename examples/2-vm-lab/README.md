# 2-VM Lab Topology

The smallest working Slurm cluster: one controller VM + one compute VM.
This is the topology used for local lab validation and the Show HN demo.

## Node layout

| Node | Hostname | Slurm roles |
|---|---|---|
| vm201 | slurm-controller | `["controller", "worker"]` |
| vm202 | slurm-compute | `["worker"]` |

The controller runs **both** `slurmctld` (job scheduler) and `slurmd` (compute
daemon). This dual-role assignment is what makes `srun -N2` work against a 2-VM
cluster — without the controller also running `slurmd`, only 1 node is available
to the scheduler.

Production operators with spare VMs can drop `"worker"` from the controller's
role list. For a 2-VM lab, keeping it is the right choice.

## Role assignment commands

```bash
CTRL_ID="<slurm-controller node ID from clustr>"
COMPUTE_ID="<slurm-compute node ID from clustr>"
API="http://10.99.0.1:8080/api/v1"
AUTH="Authorization: Bearer <your-admin-key>"

# Controller: dual-role (controller + compute)
curl -s -X PUT ${API}/nodes/${CTRL_ID}/slurm/role \
  -H "${AUTH}" -H "Content-Type: application/json" \
  -d '{"roles": ["controller", "worker"]}'

# Compute: worker only
curl -s -X PUT ${API}/nodes/${COMPUTE_ID}/slurm/role \
  -H "${AUTH}" -H "Content-Type: application/json" \
  -d '{"roles": ["worker"]}'
```

## Smoke test

After reimaging both nodes:

```bash
# From the controller node
srun -N1 hostname    # should return slurm-compute
srun -N2 hostname    # should return both hostnames
```

## See also

- [docs/slurm-module.md](../../docs/slurm-module.md) — full operator guide
- [docs/upgrade.md](../../docs/upgrade.md) — v1.0 one-time fixes for existing clusters
