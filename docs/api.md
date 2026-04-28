# clustr API Reference

All API endpoints are prefixed with `/api/v1`.

## Authentication

Every request must supply credentials in one of two forms:

| Method | How | Scope |
|---|---|---|
| Session cookie | `POST /api/v1/auth/login` → `Set-Cookie: session=...` | Tied to user role |
| API key | `Authorization: Bearer <key>` | Tied to key scope (`admin`, `operator`, `node`) |

**Roles** (ascending privilege): `readonly` < `pi` < `operator` < `admin`

**Key scopes:**

| Scope | What it can do |
|---|---|
| `admin` | All routes |
| `operator` | All routes except user/key management |
| `node` | Node registration, deploy progress ingest, log ingest only |

Unauthenticated requests return `401`. Insufficient role/scope returns `403`.

**Common error shape:**

```json
{"error": "reason string"}
```

---

## Auth

### `POST /api/v1/auth/login`

**Auth:** none

**Request:**

```json
{"username": "admin", "password": "changeme"}
```

**Response `200 OK`:**

```json
{
  "user_id": "3a67c76c-...",
  "username": "admin",
  "role": "admin",
  "must_change_password": false
}
```

Sets `session` cookie. On first boot the default credentials are shown by `GET /api/v1/auth/bootstrap-status`.

**Errors:** `401` bad credentials, `423` account locked.

---

### `POST /api/v1/auth/logout`

**Auth:** session cookie (any role)

Clears the session cookie.

**Response `204 No Content`**

---

### `GET /api/v1/auth/me`

**Auth:** any

**Response `200 OK`:**

```json
{
  "user_id": "3a67c76c-...",
  "username": "admin",
  "role": "admin",
  "must_change_password": false
}
```

---

### `POST /api/v1/auth/set-password`

**Auth:** session cookie (any role, typically triggered by `must_change_password: true`)

**Request:**

```json
{"current_password": "changeme", "new_password": "Str0ngP@ssw0rd!"}
```

**Response `204 No Content`**

---

### `GET /api/v1/auth/bootstrap-status`

**Auth:** none (safe to expose publicly — returns only a boolean)

Returns whether the initial admin setup is complete, used by the login page to show first-boot credentials hint.

**Response `200 OK`:**

```json
{"bootstrap_complete": true}
```

`false` when no users exist yet.

---

## Nodes

### `GET /api/v1/nodes`

**Auth:** admin, operator, or readonly

Lists all registered nodes.

**Query params:**

| Param | Description |
|---|---|
| `group_id` | Filter by node group UUID |
| `deploy_state` | Filter by deploy state (see values below) |
| `tag` | Filter by tag |

**Response `200 OK`:**

```json
{
  "nodes": [
    {
      "id": "3a67c76c-...",
      "hostname": "compute-01",
      "mac": "bc:24:11:00:01:00",
      "ip": "10.99.0.101",
      "deploy_state": "deployed_verified",
      "image_id": "img-uuid",
      "group_id": "grp-uuid",
      "tags": ["compute"],
      "created_at": "2026-04-27T12:00:00Z",
      "updated_at": "2026-04-27T22:00:00Z",
      "deploy_verified_booted_at": "2026-04-27T22:35:00Z"
    }
  ],
  "count": 1
}
```

**Deploy state values:** `registered`, `configured`, `deployed_preboot`, `deployed_verified`, `deploy_verify_timeout`, `failed`, `reimage_pending`

---

### `POST /api/v1/nodes`

**Auth:** admin or operator

Creates a node record manually (pre-registration before PXE boot).

**Request:**

```json
{
  "hostname": "compute-01",
  "mac": "bc:24:11:00:01:00",
  "ip": "10.99.0.101",
  "image_id": "img-uuid",
  "tags": ["compute"]
}
```

**Response `201 Created`:** node object (same shape as list item)

---

### `GET /api/v1/nodes/{id}`

**Auth:** admin, operator, or readonly

Returns a single node by UUID.

**Response `200 OK`:** node object

**Errors:** `404` not found

---

### `PUT /api/v1/nodes/{id}`

**Auth:** admin or group-scoped operator

Updates node hostname, image, tags, or configuration. Requires group-scoped access — operators can only update nodes in their assigned group.

**Request:** partial node object (any fields to change)

**Response `200 OK`:** updated node object

---

### `DELETE /api/v1/nodes/{id}`

**Auth:** admin or group-scoped operator

Removes a node record. Does not affect the physical machine.

**Response `204 No Content`**

---

### `GET /api/v1/nodes/by-mac/{mac}`

**Auth:** admin, operator, or readonly

Looks up a node by MAC address (lowercase, colon-separated).

**Response `200 OK`:** node object. `404` if not found.

---

### `GET /api/v1/nodes/connected`

**Auth:** admin, operator, or readonly

Returns nodes with an active `clustr-clientd` WebSocket connection.

**Response `200 OK`:**

```json
{"nodes": ["node-uuid-1", "node-uuid-2"], "count": 2}
```

---

### `GET /api/v1/nodes/{id}/heartbeat`

**Auth:** admin or readonly

Returns the latest clientd heartbeat data for a node.

**Response `200 OK`:**

```json
{
  "node_id": "uuid",
  "received_at": "2026-04-27T22:35:00Z",
  "load_avg": [0.12, 0.08, 0.05],
  "mem_total_mb": 32768,
  "mem_free_mb": 28000,
  "uptime_seconds": 3600
}
```

`404` if no heartbeat received yet.

---

### `PUT /api/v1/nodes/{id}/config-push`

**Auth:** admin or operator

Pushes a whitelisted config file to the live node over the clientd channel.

**Request:**

```json
{"path": "/etc/slurm/slurm.conf", "content": "..."}
```

**Response `200 OK`:** `{"ok": true}`

---

### `POST /api/v1/nodes/{id}/exec`

**Auth:** admin or operator

Runs a whitelisted diagnostic command on a live node.

**Request:**

```json
{"command": "df", "args": ["-h"]}
```

**Response `200 OK`:**

```json
{"stdout": "Filesystem ...", "stderr": "", "exit_code": 0}
```

---

### `GET /api/v1/nodes/{id}/config-history`

**Auth:** admin only

Returns audit trail of configuration changes for a node.

**Response `200 OK`:**

```json
{
  "history": [
    {
      "id": "uuid",
      "node_id": "uuid",
      "changed_by": "admin",
      "changed_at": "2026-04-27T22:00:00Z",
      "field": "image_id",
      "old_value": "img-old",
      "new_value": "img-new"
    }
  ]
}
```

---

## Node Groups

### `GET /api/v1/node-groups`

**Auth:** admin, operator, or readonly

Lists all node groups with live member counts.

**Response `200 OK`:**

```json
{
  "node_groups": [
    {
      "id": "grp-uuid",
      "name": "compute-rack-A",
      "description": "Rack A compute nodes",
      "role": "compute",
      "member_count": 8,
      "expires_at": null,
      "created_at": "2026-04-01T00:00:00Z",
      "updated_at": "2026-04-27T00:00:00Z"
    }
  ],
  "count": 1
}
```

---

### `POST /api/v1/node-groups`

**Auth:** admin or operator

**Request:**

```json
{
  "name": "compute-rack-A",
  "description": "Rack A compute nodes",
  "role": "compute"
}
```

**Response `201 Created`:** node group object

---

### `GET /api/v1/node-groups/{id}`

**Auth:** admin, operator, or readonly

**Response `200 OK`:** node group object with `disk_layout_override` and `extra_mounts` populated if set.

---

### `PUT /api/v1/node-groups/{id}`

**Auth:** admin or operator

**Request:** partial node group object

**Response `200 OK`:** updated node group object

---

### `DELETE /api/v1/node-groups/{id}`

**Auth:** admin only

Deletes the group. Nodes in the group are not deleted — they become ungrouped.

**Response `204 No Content`**

---

### `POST /api/v1/node-groups/{id}/members`

**Auth:** admin or operator

Adds nodes to the group.

**Request:**

```json
{"node_ids": ["uuid-1", "uuid-2"]}
```

**Response `200 OK`:** `{"added": 2}`

---

### `DELETE /api/v1/node-groups/{id}/members/{node_id}`

**Auth:** admin or group-scoped operator

Removes a node from the group.

**Response `204 No Content`**

---

### `PUT /api/v1/node-groups/{id}/pi`

**Auth:** admin only

Assigns a PI (principal investigator) user to own this group.

**Request:**

```json
{"user_id": "pi-user-uuid"}
```

**Response `200 OK`:** updated node group

---

### `PUT /api/v1/node-groups/{id}/expiration`

**Auth:** admin or pi role

Sets an expiration timestamp on the group allocation.

**Request:**

```json
{"expires_at": "2026-12-31T23:59:59Z"}
```

**Response `200 OK`:** updated node group

---

### `DELETE /api/v1/node-groups/{id}/expiration`

**Auth:** admin or pi role

Clears the expiration timestamp (allocation becomes permanent).

**Response `204 No Content`**

---

### `GET /api/v1/node-groups/{id}/ldap-restrictions`

**Auth:** admin only

Returns the list of LDAP groups allowed to use this partition. Empty list = open access.

**Response `200 OK`:**

```json
{"ldap_groups": ["hpc-users", "project-alpha"]}
```

---

### `PUT /api/v1/node-groups/{id}/ldap-restrictions`

**Auth:** admin only

Replaces the LDAP group restriction list. Pass `[]` to clear (open access).

**Request:**

```json
{"ldap_groups": ["hpc-users"]}
```

**Response `200 OK`:** `{"ldap_groups": ["hpc-users"]}`

---

### `POST /api/v1/node-groups/{id}/reimage`

**Auth:** admin or group-scoped operator

Queues a rolling reimage of all nodes in the group.

**Request:**

```json
{"image_id": "img-uuid", "concurrency": 2}
```

**Response `202 Accepted`:**

```json
{"job_id": "job-uuid"}
```

---

### `GET /api/v1/reimages/jobs/{jobID}`

**Auth:** admin, operator, or readonly

Returns status of a group reimage job.

**Response `200 OK`:**

```json
{
  "job_id": "job-uuid",
  "group_id": "grp-uuid",
  "state": "running",
  "total": 8,
  "completed": 3,
  "failed": 0,
  "created_at": "2026-04-27T22:00:00Z"
}
```

---

### `POST /api/v1/reimages/jobs/{jobID}/resume`

**Auth:** admin or operator

Resumes a paused or interrupted group reimage job.

**Response `200 OK`:** `{"ok": true}`

---

### `GET /api/v1/node-groups/{id}/auto-policy-state`

**Auth:** admin or operator

Returns the current auto-compute allocation policy state for the group.

**Response `200 OK`:**

```json
{"enabled": true, "policy_version": 3, "last_applied_at": "2026-04-27T20:00:00Z"}
```

---

### `POST /api/v1/node-groups/{id}/undo-auto-policy`

**Auth:** admin only

Reverts the last auto-policy action on this group.

**Response `200 OK`:** `{"ok": true}`

---

## Disk Layout

### `GET /api/v1/nodes/{id}/layout-recommendation`

**Auth:** admin, operator, or readonly

Returns the hardware-aware disk layout recommendation for a node, based on its detected disks and NVMe topology.

**Response `200 OK`:** `DiskLayout` object (see `/api/v1/images/{id}/disklayout` for schema)

---

### `GET /api/v1/nodes/{id}/effective-layout`

**Auth:** admin, operator, or readonly

Returns the resolved disk layout that will be applied at deploy time (node override → group override → image default, in that priority order).

**Response `200 OK`:** `DiskLayout` object

---

### `PUT /api/v1/nodes/{id}/layout-override`

**Auth:** admin or operator

Sets a node-level disk layout override. Pass `null` to clear the override and fall back to group/image defaults.

**Request:**

```json
{
  "partitions": [...],
  "bootloader": {"type": "grub2", "install_device": "/dev/sda"}
}
```

**Response `200 OK`:** updated node object

---

### `POST /api/v1/nodes/{id}/layout/validate`

**Auth:** admin or operator

Validates a disk layout against the node's hardware profile without applying it.

**Request:** `DiskLayout` object

**Response `200 OK`:**

```json
{"valid": true, "warnings": []}
```

**Response `422 Unprocessable Entity`:**

```json
{"valid": false, "errors": ["partition /boot too small: need 512MiB, got 256MiB"]}
```

---

### `PUT /api/v1/nodes/{id}/group`

**Auth:** admin or operator

Assigns (or reassigns) a node to a node group. Pass `null` for `group_id` to remove from all groups.

**Request:**

```json
{"group_id": "grp-uuid"}
```

**Response `200 OK`:** updated node object

---

### `GET /api/v1/nodes/{id}/effective-mounts`

**Auth:** admin, operator, or readonly

Returns the resolved fstab mount list (node entries merged over group entries).

**Response `200 OK`:**

```json
{
  "mounts": [
    {
      "source": "nfs-server:/export/home",
      "mount_point": "/home/shared",
      "fs_type": "nfs4",
      "options": "defaults,_netdev",
      "dump": 0,
      "pass": 0,
      "auto_mkdir": true
    }
  ]
}
```

---

## Images

### `GET /api/v1/images`

**Auth:** admin, operator, or readonly

Lists all base images.

**Response `200 OK`:**

```json
{
  "images": [
    {
      "id": "img-uuid",
      "name": "rocky9-slurm",
      "status": "ready",
      "format": "filesystem",
      "firmware": "uefi",
      "size_bytes": 4294967296,
      "os_name": "Rocky Linux 9.3",
      "arch": "amd64",
      "tags": ["slurm", "production"],
      "created_at": "2026-04-01T00:00:00Z",
      "updated_at": "2026-04-27T00:00:00Z"
    }
  ],
  "count": 1
}
```

**Image status values:** `building`, `ready`, `error`, `archived`, `interrupted`

---

### `POST /api/v1/images`

**Auth:** admin only

Creates an image record (without blob). Use factory endpoints to build or import the actual blob.

**Request:**

```json
{
  "name": "rocky9-slurm",
  "format": "filesystem",
  "firmware": "uefi",
  "os_name": "Rocky Linux 9.3",
  "arch": "amd64",
  "tags": ["slurm"]
}
```

**Response `201 Created`:** image object

---

### `DELETE /api/v1/images/{id}`

**Auth:** admin only

Deletes the image record and its blob from disk. Fails if any nodes reference this image.

**Response `204 No Content`**

**Errors:** `409 Conflict` if nodes are using this image.

---

### `GET /api/v1/images/{id}/status`

**Auth:** admin, operator, or readonly

**Response `200 OK`:**

```json
{"id": "img-uuid", "status": "ready", "error_message": null}
```

---

### `GET /api/v1/images/{id}/metadata`

**Auth:** admin, operator, or readonly

Returns extended image metadata (OS release, kernel version, package manifest summary).

**Response `200 OK`:**

```json
{
  "id": "img-uuid",
  "os_name": "Rocky Linux 9.3",
  "kernel_version": "5.14.0-362.el9.x86_64",
  "slurm_version": "23.11.4",
  "build_source": "iso",
  "built_at": "2026-04-01T00:00:00Z"
}
```

---

### `PUT /api/v1/images/{id}/tags`

**Auth:** admin only

Replaces the tag list on an image.

**Request:**

```json
{"tags": ["slurm", "production", "v23"]}
```

**Response `200 OK`:** updated image object

---

### `GET /api/v1/images/{id}/disklayout`

**Auth:** admin, operator, or readonly

Returns the default disk layout embedded in the image.

**Response `200 OK`:** `DiskLayout` object

---

### `PUT /api/v1/images/{id}/disklayout`

**Auth:** admin only

Replaces the default disk layout on the image.

**Request:** `DiskLayout` object

**Response `200 OK`:** updated image object

---

### `POST /api/v1/images/{id}/blob`

**Auth:** admin only

Uploads a raw image blob (tar.gz or block image). Use `Content-Type: application/octet-stream`. Large uploads are streamed.

**Response `200 OK`:** `{"ok": true, "size_bytes": 4294967296}`

---

### `POST /api/v1/images/{id}/cancel`

**Auth:** admin only

Cancels an in-progress build.

**Response `200 OK`:** `{"ok": true}`

---

### `POST /api/v1/images/{id}/resume`

**Auth:** admin only

Resumes an interrupted build (status `interrupted`).

**Response `202 Accepted`:** `{"ok": true}`

---

### `GET /api/v1/images/{id}/build-progress`

**Auth:** admin, operator, or readonly

Returns the latest build progress snapshot.

**Response `200 OK`:**

```json
{
  "image_id": "img-uuid",
  "phase": "installing_packages",
  "percent": 42,
  "message": "Installing slurmd...",
  "started_at": "2026-04-27T20:00:00Z",
  "updated_at": "2026-04-27T20:15:00Z"
}
```

---

### `GET /api/v1/images/{id}/build-progress/stream`

**Auth:** admin, operator, or readonly

SSE stream of build progress events. Each event is a JSON build-progress object.

---

### `GET /api/v1/images/{id}/build-log`

**Auth:** admin only

Returns the full build log as plain text.

---

### `GET /api/v1/images/{id}/build-manifest`

**Auth:** admin only

Returns the build manifest (resolved kickstart/preseed/cloud-init fragments used).

---

### `GET /api/v1/images/{id}/active-deploys`

**Auth:** admin, operator, or readonly

Lists nodes currently being reimaged with this image.

**Response `200 OK`:** `{"node_ids": ["uuid-1"], "count": 1}`

---

### `GET /api/v1/image-roles`

**Auth:** admin, operator, or readonly

Returns the list of available image roles (HPC role presets for the factory).

**Response `200 OK`:**

```json
{"roles": ["compute", "login", "storage", "gpu", "admin"]}
```

---

## Factory (Image Building)

All factory endpoints are admin-only.

### `POST /api/v1/factory/pull`

Pulls an image from a remote registry (OCI or HTTP blob URL).

**Request:**

```json
{"source": "https://images.example.com/rocky9-hpc.tar.gz", "name": "rocky9-hpc", "firmware": "uefi"}
```

**Response `202 Accepted`:** `{"image_id": "img-uuid"}`

---

### `POST /api/v1/factory/import`

Imports a pre-built image from a local path accessible to the server.

**Request:**

```json
{"path": "/var/lib/clustr/uploads/rocky9.tar.gz", "name": "rocky9", "format": "filesystem"}
```

**Response `202 Accepted`:** `{"image_id": "img-uuid"}`

---

### `POST /api/v1/factory/import-path`

Alias for `import`. Used by the web UI.

---

### `POST /api/v1/factory/import-iso`

Alias for `import`. Used by the web UI for ISO imports.

---

### `POST /api/v1/factory/probe-iso`

Probes an ISO file and returns metadata (distro, version, firmware type).

**Request:**

```json
{"path": "/var/lib/clustr/uploads/Rocky-9.3-x86_64-dvd.iso"}
```

**Response `200 OK`:**

```json
{"distro": "rocky", "version": "9.3", "arch": "x86_64", "firmware": "uefi"}
```

---

### `POST /api/v1/factory/build-from-iso`

Builds a clustr image from an ISO using a QEMU build VM. Long-running — poll `GET /api/v1/images/{id}/build-progress` for status.

**Request:**

```json
{
  "iso_path": "/var/lib/clustr/uploads/Rocky-9.3-x86_64-dvd.iso",
  "name": "rocky9-slurm",
  "firmware": "uefi",
  "role": "compute",
  "slurm_version": "23.11.4"
}
```

**Response `202 Accepted`:** `{"image_id": "img-uuid"}`

---

### `POST /api/v1/factory/capture`

Captures a running system into a clustr image.

**Request:**

```json
{"node_id": "node-uuid", "name": "golden-image-v2"}
```

**Response `202 Accepted`:** `{"image_id": "img-uuid"}`

---

## Shell Sessions (Image Customization)

All shell session endpoints are admin-only.

### `POST /api/v1/images/{id}/shell-session`

Opens an interactive shell inside the image's build environment.

**Response `201 Created`:** `{"session_id": "sid-uuid"}`

---

### `DELETE /api/v1/images/{id}/shell-session/{sid}`

Closes the shell session.

**Response `204 No Content`**

---

### `POST /api/v1/images/{id}/shell-session/{sid}/exec`

Runs a command in the shell session.

**Request:**

```json
{"command": "dnf install -y htop"}
```

**Response `200 OK`:**

```json
{"stdout": "...", "stderr": "", "exit_code": 0}
```

---

### `GET /api/v1/images/{id}/shell-session/{sid}/ws`

WebSocket endpoint for interactive TTY access to the shell session.

---

## Reimaging

### `POST /api/v1/nodes/{id}/reimage`

**Auth:** admin or group-scoped operator

Queues a reimage of a single node.

**Request:**

```json
{"image_id": "img-uuid"}
```

**Response `202 Accepted`:** `{"reimage_id": "reimage-uuid"}`

---

### `DELETE /api/v1/nodes/{id}/reimage/active`

**Auth:** admin or group-scoped operator

Cancels the active reimage for this node.

**Response `204 No Content`**

---

### `GET /api/v1/nodes/{id}/reimage/active`

**Auth:** admin, operator, or readonly

Returns the currently active reimage for the node, or `{}` if none.

**Response `200 OK`:** reimage object or `{}`

---

### `GET /api/v1/nodes/{id}/reimage`

**Auth:** admin, operator, or readonly

Lists all reimage history for a node (newest first).

**Response `200 OK`:**

```json
{
  "reimages": [
    {
      "id": "reimage-uuid",
      "node_id": "node-uuid",
      "image_id": "img-uuid",
      "state": "completed",
      "started_at": "2026-04-27T20:00:00Z",
      "completed_at": "2026-04-27T20:35:00Z"
    }
  ]
}
```

---

### `GET /api/v1/reimages`

**Auth:** admin, operator, or readonly

Lists all reimages across all nodes. Supports `?state=` filter.

---

### `GET /api/v1/reimage/{id}`

**Auth:** admin, operator, or readonly

Returns a single reimage by UUID.

---

### `DELETE /api/v1/reimage/{id}`

**Auth:** admin or operator

Cancels a pending or in-progress reimage.

**Response `204 No Content`**

---

### `POST /api/v1/reimage/{id}/retry`

**Auth:** admin or operator

Retries a failed reimage.

**Response `202 Accepted`:** `{"ok": true}`

---

### `POST /api/v1/reimage/cancel-all-active`

**Auth:** admin only

Cancels all active reimages across all nodes. Emergency stop.

**Response `200 OK`:** `{"cancelled": 3}`

---

## Power Management (IPMI)

### `GET /api/v1/nodes/{id}/power`

**Auth:** admin, operator, or readonly

Returns current power state.

**Response `200 OK`:**

```json
{"node_id": "uuid", "power_state": "on"}
```

`power_state` values: `on`, `off`, `unknown`

---

### `POST /api/v1/nodes/{id}/power/on`

**Auth:** admin or group-scoped operator

Powers on the node via IPMI.

**Response `200 OK`:** `{"ok": true}`

---

### `POST /api/v1/nodes/{id}/power/off`

**Auth:** admin or group-scoped operator

Powers off the node (graceful shutdown attempted first).

**Response `200 OK`:** `{"ok": true}`

---

### `POST /api/v1/nodes/{id}/power/cycle`

**Auth:** admin or group-scoped operator

Power cycles the node.

**Response `200 OK`:** `{"ok": true}`

---

### `POST /api/v1/nodes/{id}/power/reset`

**Auth:** admin or group-scoped operator

Hard resets the node (equivalent to pressing the reset button).

**Response `200 OK`:** `{"ok": true}`

---

### `POST /api/v1/nodes/{id}/power/pxe`

**Auth:** admin or group-scoped operator

Sets next boot to PXE (one-time) and reboots.

**Response `200 OK`:** `{"ok": true}`

---

### `POST /api/v1/nodes/{id}/power/disk`

**Auth:** admin or group-scoped operator

Sets next boot to local disk (one-time) and reboots.

**Response `200 OK`:** `{"ok": true}`

---

### `GET /api/v1/nodes/{id}/sensors`

**Auth:** admin, operator, or readonly

Returns IPMI sensor readings (temperature, fan speed, voltage).

**Response `200 OK`:**

```json
{
  "sensors": [
    {"name": "CPU Temp", "value": 42.0, "unit": "C", "status": "ok"},
    {"name": "Fan 1", "value": 3600, "unit": "RPM", "status": "ok"}
  ]
}
```

---

## DHCP Allocations

### `GET /api/v1/dhcp/leases`

**Auth:** admin, operator, or readonly

Returns all DHCP-allocated nodes derived from the `node_configs` table. No dnsmasq lease files are read — the node table is the single source of truth.

**Query params:**

| Param | Description |
|---|---|
| `role` | (optional) Filter by HPC role tag (e.g. `compute`, `controller`). Matched against the node's first tag. |

**Response `200 OK`:**

```json
{
  "leases": [
    {
      "node_id": "3a67c76c-...",
      "hostname": "slurm-controller",
      "mac": "bc:24:11:00:01:00",
      "ip": "10.99.0.100",
      "role": "controller",
      "deploy_state": "deployed_verified",
      "last_seen_at": "2026-04-27T22:35:50Z",
      "first_seen_at": "2026-04-27T15:42:18Z"
    }
  ],
  "count": 1
}
```

**Sort:** ascending by IP (numeric per-octet). Nodes with no IP sort last.

**Empty cluster:** `{"leases": [], "count": 0}`

---

## Logs

### `POST /api/v1/logs`

**Auth:** node key scope

Ingests log lines from a node during deployment. Used internally by `clustr-clientd`.

**Request:**

```json
{"node_id": "uuid", "lines": [{"ts": "2026-04-27T22:00:00Z", "level": "info", "msg": "Starting deploy"}]}
```

**Response `204 No Content`**

---

### `GET /api/v1/logs`

**Auth:** admin, operator, or readonly

Queries stored deployment logs.

**Query params:**

| Param | Description |
|---|---|
| `node_id` | Filter by node UUID |
| `since` | RFC 3339 start time |
| `until` | RFC 3339 end time |
| `level` | Filter by log level (`info`, `warn`, `error`) |
| `limit` | Max results (default 200, max 1000) |

**Response `200 OK`:**

```json
{
  "logs": [
    {"id": "log-uuid", "node_id": "uuid", "ts": "2026-04-27T22:00:00Z", "level": "info", "msg": "Starting deploy"}
  ],
  "count": 1
}
```

---

### `GET /api/v1/logs/stream`

**Auth:** admin, operator, or readonly

SSE stream of live log lines. Query params same as `GET /api/v1/logs`.

---

## Deploy Progress

### `POST /api/v1/deploy/progress`

**Auth:** node key scope

Ingests deploy progress from a node during imaging. Used internally by the deploy initramfs.

---

### `GET /api/v1/deploy/progress`

**Auth:** admin, operator, or readonly

Lists current deploy progress for all active reimages.

**Response `200 OK`:**

```json
{
  "progress": [
    {
      "mac": "bc:24:11:00:01:00",
      "phase": "writing_image",
      "percent": 65,
      "message": "Writing block 6500/10000",
      "updated_at": "2026-04-27T22:15:00Z"
    }
  ]
}
```

---

### `GET /api/v1/deploy/progress/{mac}`

**Auth:** admin, operator, or readonly

Returns deploy progress for a specific node by MAC address.

---

### `GET /api/v1/deploy/progress/stream`

**Auth:** admin, operator, or readonly

SSE stream of deploy progress events.

---

## System — Initramfs

All initramfs endpoints are admin-only.

### `GET /api/v1/system/initramfs`

Returns the current initramfs metadata (size, build time, kernel version it was built against).

**Response `200 OK`:**

```json
{
  "built_at": "2026-04-27T10:00:00Z",
  "size_bytes": 52428800,
  "kernel_version": "5.14.0-362.el9.x86_64",
  "history": [
    {"id": "hist-uuid", "built_at": "2026-04-20T10:00:00Z", "size_bytes": 51000000}
  ]
}
```

---

### `POST /api/v1/system/initramfs/rebuild`

Triggers an initramfs rebuild. Long-running — the response returns when the build completes.

**Response `200 OK`:** `{"ok": true, "built_at": "2026-04-27T10:00:00Z"}`

---

### `DELETE /api/v1/system/initramfs/history/{id}`

Deletes an old initramfs from the history list (frees disk space).

**Response `204 No Content`**

---

## Boot Assets (PXE/iPXE)

These endpoints are unauthenticated — they must be reachable by nodes during PXE boot.

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/boot/ipxe` | Dynamic iPXE script (node lookup by MAC) |
| `GET` | `/api/v1/boot/vmlinuz` | Kernel image |
| `GET` | `/api/v1/boot/initramfs.img` | Deploy initramfs |
| `GET` | `/api/v1/boot/ipxe.efi` | UEFI iPXE binary |
| `GET` | `/api/v1/boot/undionly.kpxe` | Legacy BIOS iPXE binary |

The `/api/v1/boot/ipxe` script inspects `${net0/mac}` and returns a per-node boot directive based on deploy state.

---

## Node Registration (Node-scoped)

### `POST /api/v1/nodes/register`

**Auth:** node key scope

Called by the deploy initramfs to register or update a node's hardware profile. Updates MAC, hostname, NIC list, and disk topology.

**Request:**

```json
{
  "mac": "bc:24:11:00:01:00",
  "hostname": "compute-01",
  "hardware_profile": {"NICs": [...], "Disks": [...]}
}
```

**Response `200 OK`:** `{"node_id": "uuid", "deploy_state": "registered"}`

---

## Users

All user management endpoints are admin-only and require admin scope.

### `GET /api/v1/admin/users`

Lists all users including their group memberships.

**Response `200 OK`:**

```json
{
  "users": [
    {
      "id": "user-uuid",
      "username": "alice",
      "role": "operator",
      "group_ids": ["grp-uuid-1"],
      "created_at": "2026-04-01T00:00:00Z",
      "last_login_at": "2026-04-27T22:00:00Z"
    }
  ],
  "count": 1
}
```

Also available at `GET /api/v1/users` (alias).

---

### `POST /api/v1/admin/users`

Creates a user.

**Request:**

```json
{"username": "alice", "password": "TempP@ss1!", "role": "operator"}
```

**Role values:** `readonly`, `pi`, `operator`, `admin`

**Response `201 Created`:** user object

---

### `PUT /api/v1/admin/users/{id}`

Updates user role or other attributes.

**Request:** partial user object

**Response `200 OK`:** updated user object

---

### `POST /api/v1/admin/users/{id}/reset-password`

Sets a new password for the user (admin bypass — no current password required).

**Request:**

```json
{"new_password": "TempP@ss1!", "must_change_password": true}
```

**Response `204 No Content`**

---

### `DELETE /api/v1/admin/users/{id}`

Deletes a user. Cannot delete your own account.

**Response `204 No Content`**

---

### `GET /api/v1/users/{id}/group-memberships`

Returns the node groups this user is a member of.

**Response `200 OK`:**

```json
{"group_ids": ["grp-uuid-1", "grp-uuid-2"]}
```

---

### `PUT /api/v1/users/{id}/group-memberships`

Replaces the user's group membership list.

**Request:**

```json
{"group_ids": ["grp-uuid-1"]}
```

**Response `200 OK`:** `{"group_ids": ["grp-uuid-1"]}`

---

## API Key Management

Admin-only. Requires admin scope (API keys cannot manage other API keys via operator scope).

### `GET /api/v1/admin/api-keys`

Lists all API keys (key values are never returned after creation).

**Response `200 OK`:**

```json
{
  "api_keys": [
    {
      "id": "key-uuid",
      "name": "ci-runner",
      "scope": "operator",
      "created_at": "2026-04-01T00:00:00Z",
      "last_used_at": "2026-04-27T22:00:00Z",
      "expires_at": null
    }
  ]
}
```

---

### `POST /api/v1/admin/api-keys`

Creates an API key. The raw key value is returned only in this response — store it securely.

**Request:**

```json
{"name": "ci-runner", "scope": "operator", "expires_at": null}
```

**Response `201 Created`:**

```json
{
  "id": "key-uuid",
  "name": "ci-runner",
  "scope": "operator",
  "key": "clustr_op_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
}
```

---

### `DELETE /api/v1/admin/api-keys/{id}`

Revokes an API key immediately.

**Response `204 No Content`**

---

### `POST /api/v1/admin/api-keys/{id}/rotate`

Rotates an API key — generates a new key value and revokes the old one.

**Response `200 OK`:**

```json
{"id": "key-uuid", "key": "clustr_op_yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"}
```

---

## Webhooks

Admin-only.

### `GET /api/v1/admin/webhooks`

Lists webhook subscriptions.

**Response `200 OK`:**

```json
{
  "webhooks": [
    {
      "id": "wh-uuid",
      "url": "https://hooks.example.com/clustr",
      "events": ["node.deployed", "reimage.failed"],
      "active": true,
      "created_at": "2026-04-01T00:00:00Z"
    }
  ]
}
```

---

### `POST /api/v1/admin/webhooks`

Creates a webhook subscription.

**Request:**

```json
{
  "url": "https://hooks.example.com/clustr",
  "events": ["node.deployed", "reimage.failed"],
  "secret": "webhook-hmac-secret"
}
```

**Event names:** `node.deployed`, `node.registered`, `node.failed`, `reimage.started`, `reimage.completed`, `reimage.failed`, `image.build_complete`, `image.build_failed`

**Response `201 Created`:** webhook object

---

### `GET /api/v1/admin/webhooks/{id}`

Returns a single webhook subscription.

---

### `PUT /api/v1/admin/webhooks/{id}`

Updates a webhook subscription (URL, events, active flag).

**Response `200 OK`:** updated webhook object

---

### `DELETE /api/v1/admin/webhooks/{id}`

Deletes a webhook subscription.

**Response `204 No Content`**

---

### `GET /api/v1/admin/webhooks/{id}/deliveries`

Lists recent delivery attempts for a webhook.

**Response `200 OK`:**

```json
{
  "deliveries": [
    {
      "id": "del-uuid",
      "event": "node.deployed",
      "response_code": 200,
      "delivered_at": "2026-04-27T22:00:00Z",
      "duration_ms": 45
    }
  ]
}
```

---

## Audit Log

Admin-only.

### `GET /api/v1/audit`

Queries the structured audit log.

**Query params:**

| Param | Description |
|---|---|
| `actor` | Filter by actor username |
| `action` | Filter by action string (e.g. `node.delete`, `user.create`) |
| `resource_type` | Filter by resource type (e.g. `node`, `image`, `user`) |
| `resource_id` | Filter by specific resource UUID |
| `since` | RFC 3339 start time |
| `until` | RFC 3339 end time |
| `limit` | Max results (default 200, max 1000) |

**Response `200 OK`:**

```json
{
  "events": [
    {
      "id": "audit-uuid",
      "ts": "2026-04-27T22:00:00Z",
      "actor": "admin",
      "action": "node.delete",
      "resource_type": "node",
      "resource_id": "node-uuid",
      "detail": {"hostname": "compute-01"},
      "ip": "192.168.1.50"
    }
  ],
  "count": 1
}
```

---

### `GET /api/v1/audit/export`

SIEM JSONL streaming export of the full audit log. Rate-limited to 1 request per minute.

**Response `200 OK`:** newline-delimited JSON, one audit event per line. `Content-Type: application/x-ndjson`

---

## Notifications

### `GET /api/v1/admin/smtp`

**Auth:** admin only

Returns current SMTP configuration (password is redacted).

**Response `200 OK`:**

```json
{
  "host": "smtp.example.com",
  "port": 587,
  "username": "clustr@example.com",
  "from_address": "clustr@example.com",
  "tls": true,
  "enabled": true
}
```

---

### `PUT /api/v1/admin/smtp`

**Auth:** admin only

Updates SMTP configuration.

**Request:** same shape as response, with `password` field.

**Response `200 OK`:** updated config (password redacted)

---

### `POST /api/v1/admin/smtp/test`

**Auth:** admin only

Sends a test email using the current SMTP configuration.

**Request:**

```json
{"to": "admin@example.com"}
```

**Response `200 OK`:** `{"ok": true}` or `{"ok": false, "error": "connection refused"}`

---

### `POST /api/v1/node-groups/{id}/broadcast`

**Auth:** admin only

Sends an email broadcast to all users with access to this node group.

**Request:**

```json
{"subject": "Maintenance window tonight", "body": "Nodes will be offline 22:00-02:00 UTC."}
```

**Response `200 OK`:** `{"sent": 12}`

---

## Notification Preferences

### `GET /api/v1/me/notification-prefs`

**Auth:** session (any role)

Returns the authenticated user's notification preferences.

**Response `200 OK`:**

```json
{
  "prefs": [
    {"event": "reimage.completed", "enabled": true, "channel": "email"}
  ]
}
```

---

### `PUT /api/v1/me/notification-prefs/{event}`

**Auth:** session (any role)

Sets preference for a specific event.

**Request:**

```json
{"enabled": false}
```

**Response `200 OK`:** updated pref

---

### `POST /api/v1/me/notification-prefs/reset`

**Auth:** session (any role)

Resets all notification preferences to defaults.

**Response `204 No Content`**

---

### `GET /api/v1/admin/users/{id}/notification-prefs`

**Auth:** admin only

Returns notification preferences for any user.

---

## Slurm

Admin-only. Slurm endpoints manage the bundled Slurm configuration and deployment.

### `GET /api/v1/slurm/builds/{build_id}/artifact`

Returns a built Slurm RPM bundle artifact (binary download).

**Auth:** admin only

**Response `200 OK`:** binary stream, `Content-Type: application/octet-stream`

Additional Slurm management routes are registered by the `slurmmodule` package and follow the same admin-only auth pattern. Routes include cluster config, partition management, and accounting sync.

---

## Health

### `GET /api/v1/healthz/ready`

**Auth:** none

Kubernetes-style readiness probe. Returns `200` when the server is ready to serve traffic (DB connected, migrations applied).

**Response `200 OK`:** `{"status": "ready"}`

**Response `503 Service Unavailable`:** `{"status": "not_ready", "reason": "db migration pending"}`

---

### `GET /api/v1/health`

**Auth:** admin scope (internal liveness check)

Returns detailed internal health metrics. Used by monitoring integrations.

---

### `GET /api/v1/repo/health`

**Auth:** none

Returns the health of the image repository (disk usage, image count).

**Response `200 OK`:**

```json
{
  "images": 5,
  "disk_used_bytes": 21474836480,
  "disk_free_bytes": 85899345920,
  "status": "healthy"
}
```

---

### `GET /metrics`

**Auth:** none (Prometheus scrape endpoint — restrict via network policy)

Prometheus metrics in text exposition format. Exposes Go runtime metrics, HTTP request counters, and clustr-specific gauges (node count, active reimages, build queue depth).

---

## Debug (pprof)

**Enabled only when `CLUSTR_PPROF_ENABLED=true`.**

**Auth:** admin scope + admin role required for all pprof endpoints.

| Method | Path | Description |
|---|---|---|
| `GET` | `/debug/pprof/` | Profile index |
| `GET` | `/debug/pprof/cmdline` | Process command line |
| `GET` | `/debug/pprof/profile?seconds=30` | 30-second CPU profile |
| `GET` | `/debug/pprof/symbol` | Symbol lookup |
| `GET` | `/debug/pprof/trace` | Execution trace |
| `GET` | `/debug/pprof/{name}` | Named profiles: `heap`, `goroutine`, `allocs`, `mutex`, `block`, `threadcreate` |

**Usage:**

```bash
CLUSTR_PPROF_ENABLED=true clustr-serverd

# Profile from a host with admin API access:
go tool pprof "http://host:8080/debug/pprof/profile?seconds=30"
```

---

## Researcher Portal

The researcher portal (`/api/v1/portal/`) is accessible to users with a valid session. These endpoints power the self-service web UI at `/portal/`.

### `GET /api/v1/portal/status`

**Auth:** session (any role)

Returns the authenticated user's portal status (quota usage, partition access, active allocations).

---

### `POST /api/v1/portal/me/password`

**Auth:** session (any role)

Changes the authenticated user's password.

**Request:**

```json
{"current_password": "old", "new_password": "Str0ngP@ss!"}
```

**Response `204 No Content`**

---

### `GET /api/v1/portal/me/quota`

**Auth:** session (any role)

Returns the authenticated user's compute quota (from LDAP `quota` attributes).

---

### `GET /api/v1/portal/partitions/status`

**Auth:** session (any role)

Returns Slurm partition status visible to this user.

---

## PI Portal

Endpoints for Principal Investigator self-service. Requires `pi` role or higher.

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/portal/pi/groups` | List PI's groups |
| `GET` | `/api/v1/portal/pi/groups/{id}/utilization` | CPU/GPU utilization for group |
| `GET` | `/api/v1/portal/pi/groups/{id}/members` | List group members |
| `POST` | `/api/v1/portal/pi/groups/{id}/members` | Add member to group |
| `DELETE` | `/api/v1/portal/pi/groups/{id}/members/{username}` | Remove member |
| `POST` | `/api/v1/portal/pi/groups/{id}/expansion-requests` | Request allocation expansion |
| `GET` | `/api/v1/portal/pi/groups/{id}/grants` | List funding grants |
| `POST` | `/api/v1/portal/pi/groups/{id}/grants` | Add grant |
| `PUT` | `/api/v1/portal/pi/groups/{id}/grants/{grantID}` | Update grant |
| `DELETE` | `/api/v1/portal/pi/groups/{id}/grants/{grantID}` | Delete grant |
| `GET` | `/api/v1/portal/pi/groups/{id}/publications` | List publications |
| `POST` | `/api/v1/portal/pi/groups/{id}/publications` | Add publication |
| `PUT` | `/api/v1/portal/pi/groups/{id}/publications/{pubID}` | Update publication |
| `DELETE` | `/api/v1/portal/pi/groups/{id}/publications/{pubID}` | Delete publication |
| `GET` | `/api/v1/portal/pi/publications/lookup` | DOI metadata lookup |
| `GET` | `/api/v1/portal/pi/review-cycles` | List active review cycles |
| `POST` | `/api/v1/portal/pi/review-cycles/{cycleID}/groups/{groupID}/respond` | Submit review response |
| `GET` | `/api/v1/portal/pi/groups/{id}/managers` | List delegated managers |
| `POST` | `/api/v1/portal/pi/groups/{id}/managers` | Add manager |
| `DELETE` | `/api/v1/portal/pi/groups/{id}/managers/{userID}` | Remove manager |
| `GET` | `/api/v1/portal/pi/managed-groups` | List groups I can manage as delegate |
| `GET` | `/api/v1/portal/pi/onboarding-status` | Check onboarding completion |
| `POST` | `/api/v1/portal/pi/onboarding-complete` | Mark onboarding complete |
| `GET` | `/api/v1/portal/pi/groups/{id}/change-requests` | List allocation change requests |
| `POST` | `/api/v1/portal/pi/groups/{id}/change-requests` | Submit change request |
| `POST` | `/api/v1/portal/pi/change-requests/{reqID}/withdraw` | Withdraw a change request |
| `PATCH` | `/api/v1/portal/pi/groups/{id}/field-of-science` | Set group's field of science |
| `GET` | `/api/v1/portal/pi/groups/{id}/attribute-visibility` | List attribute visibility settings |
| `PATCH` | `/api/v1/portal/pi/groups/{id}/attribute-visibility` | Set attribute visibility |
| `DELETE` | `/api/v1/portal/pi/groups/{id}/attribute-visibility/{attr}` | Delete visibility setting |

---

## IT Director Portal

Endpoints for IT Director reports and review workflows. Requires `admin` role or higher.

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/portal/director/summary` | Cluster-wide summary (nodes, utilization, active groups) |
| `GET` | `/api/v1/portal/director/groups` | All groups with utilization metrics |
| `GET` | `/api/v1/portal/director/groups/{id}` | Single group detail |
| `GET` | `/api/v1/portal/director/export.csv` | Export groups to CSV |
| `GET` | `/api/v1/portal/director/export-full.csv` | Export full detail to CSV |
| `GET` | `/api/v1/portal/director/review-cycles` | List allocation review cycles |
| `GET` | `/api/v1/portal/director/review-cycles/{id}` | Get review cycle detail |
| `GET` | `/api/v1/portal/director/fos-utilization` | Field-of-science utilization breakdown |

---

## Fields of Science

### `GET /api/v1/fields-of-science`

**Auth:** session (any role)

Returns the canonical NSF fields of science taxonomy used for group classification.

**Response `200 OK`:**

```json
{"fields": [{"id": "fos-uuid", "code": "1.1", "name": "Mathematics"}]}
```

---

## Projects

### `POST /api/v1/projects`

**Auth:** pi role or higher

Creates a new project (triggers auto-policy group creation if auto-compute allocation is enabled).

**Request:**

```json
{"name": "Protein Folding Study", "field_of_science_id": "fos-uuid"}
```

**Response `201 Created`:** `{"project_id": "proj-uuid", "group_id": "grp-uuid"}`

---

## Attribute Visibility Defaults

### `PUT /api/v1/admin/attribute-visibility-defaults/{attr}`

**Auth:** admin only

Sets the cluster-wide default visibility for a group attribute (e.g., whether grant amounts are shown to members by default).

---

## LDAP Module

Admin-only. Registered by the `ldapmodule` package. Manages LDAP server configuration and user sync.

Notable endpoints (all under `/api/v1/`):

- `GET /admin/ldap/config` — Returns current LDAP config
- `PUT /admin/ldap/config` — Updates LDAP config
- `POST /admin/ldap/sync` — Triggers manual LDAP user sync
- `POST /ldap/sudoers/push` — Broadcasts sudoers drop-in to all connected nodes

---

## System Accounts Module

Admin-only. Registered by the `sysaccounts` package. Manages shared system accounts (e.g., `slurm`, `munge`).

---

## Network Module

Admin-only. Registered by the `networkmodule` package. Manages network interface configuration and DHCP range settings.
