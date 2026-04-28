# IT Director Portal

The IT Director portal provides a read-only institutional view of the cluster.
Directors can monitor overall utilization, see per-PI group summaries, view
grant and publication counts, and track annual review cycles — all without any
mutation capability.

---

## Role

The `director` role is the sixth RBAC role in clustr:

```
admin > operator > pi > readonly > viewer > director (read-only institutional)
```

Directors can only access:
- `/portal/director/` — the Director Portal SPA
- `/api/v1/portal/director/*` — read-only API endpoints
- `/api/v1/portal/director/export.csv` and `export-full.csv` — CSV exports

Directors **cannot** access the admin UI, make configuration changes, manage
nodes, or view any sensitive data (no IPMI credentials, no LDAP passwords, no
API keys).

---

## Creating a director account

Admin only. Via the admin UI (Settings → Users → Create User) or API:

```bash
curl -X POST https://clustr.example.com/api/v1/admin/users \
  -H "Authorization: Bearer clustr-admin-TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"jdirector","password":"TempPass123","role":"director"}'
```

The director will be prompted to change their password on first login
(if `must_change_password=true`).

---

## Portal walkthrough

### Login

Navigate to `/login`. Enter director credentials. The portal redirects
automatically to `/portal/director/`.

### Summary tab

Top-level KPI cards:
- Total nodes provisioned
- Nodes deployed (image installed)
- NodeGroups
- PIs
- Researchers (viewer role)
- Grants on record
- Publications on record

### Groups tab

Searchable table of all NodeGroups with:
- PI username
- Node and member counts
- Grant and publication counts
- Last deploy timestamp

Click a row to open a detail panel with the full grant and publication lists
for that group.

**CSV Export:** Click "Export CSV" for a summary CSV or "Export Full CSV" for
a flat file of all grants and publications.

### Annual Review tab

Shows active review cycles and per-group response status (pending / affirmed /
archive_requested / no_response). Read-only — only admins can create cycles and
only PIs can submit responses.

---

## API reference

All endpoints require `director` or `admin` role.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/portal/director/summary` | Cluster-wide aggregate stats |
| GET | `/api/v1/portal/director/groups` | All NodeGroups with summary columns |
| GET | `/api/v1/portal/director/groups/{id}` | Single group with grants + publications |
| GET | `/api/v1/portal/director/export.csv` | CSV: groups summary |
| GET | `/api/v1/portal/director/export-full.csv` | CSV: all grants + publications |
| GET | `/api/v1/portal/director/review-cycles` | All review cycles |
| GET | `/api/v1/portal/director/review-cycles/{id}` | Cycle + per-group responses |

### Example: get summary

```bash
curl https://clustr.example.com/api/v1/portal/director/summary \
  -H "Authorization: Bearer clustr-admin-TOKEN"
```

```json
{
  "total_nodes": 64,
  "total_deployed": 60,
  "total_groups": 8,
  "total_pis": 5,
  "total_researchers": 42,
  "total_grants": 12,
  "total_publications": 37,
  "deploy_success_rate_30d": 98.5
}
```

---

## Design principles

- **Read-only by design.** Director routes only register GET handlers. There is
  no path for a director to mutate state even with a crafted request.

- **No sensitive data.** Director API responses include no IPMI credentials,
  LDAP passwords, API key values, or BMC addresses. Node hostnames and MAC
  addresses are not exposed.

- **Aggregated view.** The director sees counts and summaries, not raw
  provisioning data. Slurm job history and LDAP attributes are not included.

- **CSV export for reporting.** Two CSV formats: group summary (suitable for
  leadership presentations) and full grants/publications (suitable for grant
  reporting to funding agencies).
