# clustr API Reference

All endpoints are under `/api/v1`. Authentication via session cookie or `Authorization: Bearer <key>`.

---

## DHCP Allocations

### `GET /api/v1/dhcp/leases`

Returns all DHCP-allocated nodes derived from the `node_configs` table. No dnsmasq
lease files are read — the node table is the single source of truth.

**Auth:** admin, operator, or readonly scope.

**Query params:**

| Param  | Description |
|--------|-------------|
| `role` | (optional) Filter by HPC role tag (e.g. `compute`, `controller`). Matched against the node's first tag. |

**Response `200 OK`:**

```json
{
  "leases": [
    {
      "node_id":      "3a67c76c-...",
      "hostname":     "slurm-controller",
      "mac":          "bc:24:11:00:01:00",
      "ip":           "10.99.0.100",
      "role":         "controller",
      "deploy_state": "deployed_verified",
      "last_seen_at": "2026-04-27T22:35:50Z",
      "first_seen_at": "2026-04-27T15:42:18Z"
    }
  ],
  "count": 1
}
```

**Fields:**

| Field          | Type            | Notes |
|----------------|-----------------|-------|
| `node_id`      | string (UUID)   | Matches `id` in `GET /api/v1/nodes/{id}`. |
| `hostname`     | string          | Auto-generated (`clustr-XXXXXX`) if not set by hardware. |
| `mac`          | string          | Primary MAC address (lowercase). |
| `ip`           | string          | Plain dotted-decimal IP; empty string if no DHCP lease has been assigned yet. |
| `role`         | string          | First tag on the node, used as the HPC role label. Empty when no tags are set. |
| `deploy_state` | string          | One of: `registered`, `configured`, `deployed_preboot`, `deployed_verified`, `deploy_verify_timeout`, `failed`, `reimage_pending`. |
| `last_seen_at` | RFC 3339 or null | Time of the most recent verify-boot / clientd heartbeat. |
| `first_seen_at`| RFC 3339        | Node creation time (first PXE registration). |

**Sort:** results are sorted by IP ascending (numeric per-octet). Nodes with no IP sort last.

**Empty cluster:**

```json
{"leases": [], "count": 0}
```
