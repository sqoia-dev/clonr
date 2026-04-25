# clustr-clientd Architecture

**Status:** Design (pre-implementation)
**Audience:** Dinesh (implementation), Gilfoyle (deployment), Richard (review)

---

## Overview

`clustr-clientd` is a persistent, lightweight daemon installed on every deployed
node. It maintains a single multiplexed WebSocket connection back to
`clustr-serverd`, enabling config push, remote diagnostics, log streaming, and
health heartbeats without reimaging.

The existing codebase already has all the foundational primitives this needs:

- Node-scoped API keys with `node_id` binding (migration 017, `internal/db/apikeys.go`)
- A token already written to `/etc/clustr/node-token` during finalization (`internal/deploy/phonehome.go`)
- A `verify-boot-url` already written to `/etc/clustr/verify-boot-url`
- The `LogBroker` pub/sub pattern (`internal/server/logbroker.go`)
- An SSE streaming pattern (`handlers/logs.go` `StreamLogs`)
- A WebSocket pattern with gorilla/websocket (`handlers/shell_ws.go`)
- The `requireNodeOwnership` middleware for per-node auth enforcement

---

## 1. Protocol & Transport

### Decision: WebSocket over TLS

**Rationale:**

| Option | Verdict |
|---|---|
| WebSocket | Chosen. Bidirectional, multiplexed over a single TCP connection. gorilla/websocket is already in go.mod. Fits naturally alongside the existing shell WebSocket. |
| gRPC | Overkill. Requires proto compilation, a separate listener, and adds ~5 MB to the node binary. No existing investment in the codebase. |
| Long-poll | One-directional. Server-to-node pushes require holding open a response body; poor fit for config push acknowledgments and command responses. |
| SSE (server-push only) | Cannot carry node-to-server messages on the same connection; would need a separate HTTP POST channel. Creates two connections per node. |

### Connection Lifecycle

```
clientd boots
    |
    v
Read /etc/clustr/node-token
Read /etc/clustr/clustrd-url (new file, written during finalization)
    |
    v
GET ws://<server>/api/v1/nodes/{id}/clientd/ws
    Authorization: Bearer <node-token>
    |
    +---> 101 Switching Protocols
    |
    v
[connected]
    Send: hello message (node ID, hostname, kernel, clientd version)
    <-- Server: ack
    |
    v
[steady-state loop]
    Every 60s: send heartbeat message
    On server message: dispatch to handler (config_push, exec_request, log_pull)
    On connection drop: exponential backoff reconnect (5s, 10s, 20s, 40s, cap 5m)
    |
    v
[systemd stop signal received]
    Send: goodbye message
    Close WebSocket with code 1001 (going away)
```

### Message Format

Reuse the existing `wsMsg` pattern from `handlers/shell_ws.go` as a model.
All messages are JSON envelopes:

```go
// ClientMessage is sent from node to server.
type ClientMessage struct {
    Type    string          `json:"type"`    // "hello", "heartbeat", "log_batch", "ack", "exec_result"
    MsgID   string          `json:"msg_id"`  // UUID, for ack correlation
    Payload json.RawMessage `json:"payload,omitempty"`
}

// ServerMessage is sent from server to node.
type ServerMessage struct {
    Type    string          `json:"type"`    // "ack", "config_push", "exec_request", "log_pull_start", "log_pull_stop"
    MsgID   string          `json:"msg_id"`  // UUID echoed in ack
    Payload json.RawMessage `json:"payload,omitempty"`
}
```

**Message type catalog:**

| Direction | Type | Payload | Description |
|---|---|---|---|
| node→server | `hello` | `HelloPayload` | Sent on connect: node ID, hostname, kernel, uptime, clientd version |
| node→server | `heartbeat` | `HeartbeatPayload` | Sent every 60s: uptime, load, disk, services |
| node→server | `log_batch` | `[]api.LogEntry` | Batch of journald entries (reuse existing `api.LogEntry`) |
| node→server | `ack` | `{ref_msg_id, ok, error}` | Acknowledge a server-sent message |
| node→server | `exec_result` | `{ref_msg_id, exit_code, stdout, stderr}` | Result of an exec_request |
| server→node | `ack` | `{ref_msg_id}` | Acknowledge a node-sent message |
| server→node | `config_push` | `ConfigPushPayload` | Push a config update |
| server→node | `exec_request` | `ExecRequestPayload` | Run a diagnostic command |
| server→node | `log_pull_start` | `{cursor, filters}` | Start streaming journald entries |
| server→node | `log_pull_stop` | `{}` | Stop log stream |

### Multiplexing

All capabilities share the single WebSocket connection. The `msg_id` field
provides correlation for request/response pairs (config push + ack,
exec_request + exec_result). Log batches are unsolicited fire-and-forget from
the node side; the server acks them for flow control.

**Bandwidth budget at steady state (200-node cluster):**
- Heartbeat: ~300 bytes × 1/60s × 200 nodes = ~1 KB/s aggregate inbound
- Log stream (when active): ~2 KB/s per node on demand, only while UI is open
- Config push: rare, operator-triggered

---

## 2. Security Model

### Authentication

`clustr-clientd` uses the same node-scoped API key already written to
`/etc/clustr/node-token` during finalization. No new auth mechanism needed.

The WebSocket endpoint uses `extractBearerToken(r)` from the existing
middleware, which already supports the `?token=` query parameter for WebSocket
compatibility (browsers cannot set Authorization headers on WebSocket upgrades).
The node binary sets the header directly.

The server validates with `requireNodeOwnership("id")` — the key's bound
`node_id` must match the `{id}` URL parameter, same as `verify-boot`.

**Token rotation:** When a node is reimaged, `RevokeAndCreateNodeScopedKey` is
called (already exists), which atomically rotates the token. The new token is
written to the new rootfs during finalization. `clustr-clientd` reads the token
file at startup and on reconnect. If the server returns 401, the daemon reads
the token file again before the next reconnect attempt (handles rotation after
reimage of a running node).

### Authorization

What the server can push to a node via `clustr-clientd`:

| Operation | Allowed | Rationale |
|---|---|---|
| Write files in `/etc/clustr/` managed namespace | Yes | Scoped to clustr-managed configs only |
| Write Slurm config files in `/etc/slurm/` | Yes | Cluster-managed configs; path validated against per-file whitelist (see Section 8) |
| Restart specific whitelisted services (sssd, ntpd, slurmd) | Yes | Controlled whitelist, not arbitrary systemctl |
| Run `scontrol reconfigure` on local slurmd | Yes | Non-destructive in-process reload; Slurm-specific reconfigure action |
| Run whitelisted diagnostic commands | Yes | Read-only commands only (journalctl, systemctl status, df, etc.) |
| Arbitrary shell execution | No | Blast radius too large |
| Write files outside `/etc/clustr/`, `/etc/slurm/`, or other config targets | No | Filesystem scope enforced in client |

The client enforces its own command whitelist and path constraints. A
compromised server cannot escape the whitelist because the client refuses
out-of-scope requests and returns an error ack.

### Blast Radius Analysis

| Threat | Impact | Mitigation |
|---|---|---|
| Compromised server → nodes | Can push configs and run whitelisted diagnostics on all connected nodes | Client whitelist; no arbitrary exec; no root shell |
| Compromised node → server | Can send fabricated log entries, fake heartbeats | Node key is scoped to its own node_id only; cannot read/write other nodes |
| Token theft from `/etc/clustr/node-token` | Attacker can impersonate the node | 0600 permissions (already set by phonehome.go); token is node-scoped, cannot escalate to admin |
| MitM on the WebSocket connection | Can intercept logs, replay config pushes | TLS required (CLUSTR_TLS or Caddy termination); clientd refuses plaintext ws:// in production mode |

**Key architectural constraint:** `clustr-clientd` runs as a dedicated
`clustr-clientd` system user (not root) with a writable namespace limited to
`/etc/clustr/`, `/etc/sssd/`, `/etc/slurm/`, `/etc/hosts`, `/etc/ntp.conf`, and
`/etc/resolv.conf`. File writes outside these paths are rejected by the daemon
before attempting them, regardless of server instruction. The `clustr-clientd`
user requires group membership in the `slurm` group (or appropriate ACL entries)
to write to `/etc/slurm/`; this is configured during finalization alongside the
Slurm user account setup (see Section 8.9).

---

## 3. Capabilities

### 3.1 Config Push

**Supported config targets (v1):**

| Config | File path | Apply action | Rollback |
|---|---|---|---|
| `/etc/hosts` | `/etc/hosts` | Atomic write (tmp + rename) | Keep previous copy as `.bak` |
| SSSD config | `/etc/sssd/sssd.conf` + restart sssd | Atomic write + `systemctl restart sssd` | Restore `.bak` on restart failure |
| NTP config | `/etc/chrony.conf` or `/etc/ntp.conf` | Atomic write + restart ntpd/chronyd | Restore `.bak` on failure |
| DNS/resolv | `/etc/resolv.conf` | Atomic write | Restore `.bak` |
| Slurm configs | `/etc/slurm/` (multiple files) | Atomic write + reconfigure or restart (see Section 8) | Restore `.bak`; Section 8 covers Slurm-specific rollback |

Slurm config targets are cluster-managed and use an extended push protocol defined in Section 8. They are not pushed individually via the per-node `PUT /api/v1/nodes/{id}/config-push` endpoint; instead they go through the cluster-scoped Slurm push API described in Section 8.5.

**Apply protocol:**

```
Server sends config_push{target, content, checksum}
    |
    v
Client validates:
  - target in whitelist
  - sha256(content) == checksum
  - content size < 1 MB
    |
    v
Client writes atomically (tmp file + os.Rename)
    |
    v
Client runs apply action (service restart if needed)
    |
    +-- success: send ack{ok: true}
    +-- failure: restore .bak, send ack{ok: false, error: "..."}
```

Rollback is file-level only. If a service restart fails after the file is
written, the previous `.bak` is restored and the service is restarted again.
If the second restart also fails, the error is surfaced in the ack and
logged — no further automatic action.

### 3.2 Log Streaming

**Source:** journald via `journalctl -f -o json` subprocess.

When the server sends `log_pull_start`, the client:
1. Forks `journalctl -f -o json [--since=<cursor>] [--unit=<filter>]`
2. Parses JSON output into `api.LogEntry` batches (batch up to 50 entries or
   500ms, whichever comes first)
3. Sends `log_batch` messages to server
4. Server publishes to its `LogBroker`, which fans out to any subscribed SSE
   streams (existing browser UI already consumes from `LogBroker`)

On `log_pull_stop`, the client kills the `journalctl` subprocess.

**Filtering:** The `log_pull_start` payload carries optional `units` (systemd
unit names) and `priority` (journald priority level 0-7). These map directly to
`journalctl --unit` and `--priority` flags.

**Bandwidth management:**
- Server sends `log_pull_start` only when a browser has the Logs tab open for
  that node. When the browser closes the tab or navigates away, the server sends
  `log_pull_stop`.
- Batch size cap: 50 entries or 500ms. At 1000 log lines/second (pathological
  case), this is ~100 KB/s per node — acceptable on a management network.
- The existing `nodeRateLimiter` in `handlers/logs.go` still applies to the
  REST ingest path; log_batch over WebSocket is a separate code path with its
  own in-memory flow control (drop oldest if server-side buffer exceeds 1000
  pending entries per node).

**Component label:** entries get `component: "node-journal"` to distinguish
them from deploy-time logs in the existing `api.LogEntry` schema.

### 3.3 Health / Heartbeat

**HeartbeatPayload:**

```go
type HeartbeatPayload struct {
    Uptime         float64            `json:"uptime_seconds"`
    Load1          float64            `json:"load_1"`
    Load5          float64            `json:"load_5"`
    Load15         float64            `json:"load_15"`
    MemTotalKB     int64              `json:"mem_total_kb"`
    MemAvailKB     int64              `json:"mem_avail_kb"`
    DiskUsage      []DiskUsage        `json:"disk_usage"`
    Services       []ServiceStatus    `json:"services"`   // whitelisted services only
    KernelVersion  string             `json:"kernel_version"`
    ClientdVersion string             `json:"clientd_version"`
}

type DiskUsage struct {
    MountPoint  string `json:"mount_point"`
    TotalBytes  int64  `json:"total_bytes"`
    UsedBytes   int64  `json:"used_bytes"`
}

type ServiceStatus struct {
    Name   string `json:"name"`    // e.g. "sssd", "munge", "slurmd"
    Active bool   `json:"active"`
    State  string `json:"state"`   // systemd ActiveState
}
```

**Heartbeat interval:** 60 seconds. Configurable via `/etc/clustr/clientd.conf`.

**Server-side storage:** Heartbeat data is stored in a new `node_heartbeats`
table (see Section 5). Only the most recent heartbeat per node is kept in the
DB; the history is not retained. `last_seen_at` on `NodeConfig` is updated on
every heartbeat receipt.

**Whitelisted services for status reporting:** `sssd`, `munge`, `slurmd`,
`slurmctld`, `ntpd`, `chronyd`, `sshd`. The whitelist is configurable via
`/etc/clustr/clientd.conf`.

### 3.4 Remote Exec (Diagnostics Only)

**Whitelisted commands (v1 — read-only diagnostics):**

```
journalctl --unit=<name> --lines=<n> --no-pager
systemctl status <service>
systemctl is-active <service>
df -h
free -m
uptime
ip addr show
ip route show
cat /etc/os-release
cat /etc/hostname
ping -c 4 <host>    (restricted: host must match a pattern the server validates)
```

The whitelist is enforced in the **client binary**, not the server. The server
constructs an `exec_request` with `command` and `args`, the client validates
against its local whitelist, and refuses with an error ack if the command is
not permitted. This means a compromised server cannot trivially bypass the
restriction.

**No arbitrary shell execution.** No `bash -c`, no shell metacharacters, no
argument injection (args are passed as a `[]string` and never passed through a
shell).

**Exec timeout:** 30 seconds. Output is capped at 64 KB. Truncated output
includes a `[truncated]` suffix.

---

## 4. Client Architecture

### Binary

A new standalone binary: `clustr-clientd`. It does NOT share a binary with the
existing `clustr` CLI. Rationale:

- The `clustr` CLI is a deploy-time tool (initramfs); linking it with a
  persistent daemon's dependencies creates unnecessary coupling.
- `clustr-clientd` needs to be a small, static binary for easy image baking.
- Separate binary = separate systemd unit = separate resource limits = simpler
  code ownership.

**Module path:** `cmd/clustr-clientd/main.go`, package
`github.com/sqoia-dev/clustr/cmd/clustr-clientd`.

**Shared packages used by clientd:**
- `pkg/api` — `LogEntry`, `VerifyBootRequest` (shared wire types)
- `internal/config` — `ClientConfig` extended with clientd fields

### Installation

**During finalization** (`internal/deploy/finalize.go`), alongside the existing
`injectPhoneHome` call, a new `injectClientd` function:

1. Writes `/etc/clustr/clustrd-url` (the WebSocket endpoint URL)
2. Writes `/etc/clustr/clientd.conf` (heartbeat interval, service whitelist)
3. Writes `/etc/systemd/system/clustr-clientd.service` from embedded unit
4. Copies the `clustr-clientd` binary to `/usr/local/bin/clustr-clientd`
5. Creates the `WantedBy=multi-user.target` symlink (same pattern as `injectPhoneHome`)

The `clustr-clientd` binary is embedded in the server binary at build time (same
pattern as `internal/bootassets/assets.go` for `ipxe.efi`). The server's
`CLUSTR_CLIENTD_BIN_PATH` env var allows overriding with a local path for
development.

**Files written to deployed rootfs:**

```
/etc/clustr/node-token          (existing, reused)
/etc/clustr/clustrd-url          (new: ws://clustr-server:8080/api/v1/nodes/{id}/clientd/ws)
/etc/clustr/clientd.conf        (new: interval, service whitelist, log filters)
/usr/local/bin/clustr-clientd   (new: the daemon binary)
/etc/systemd/system/clustr-clientd.service  (new: systemd unit)
```

### Systemd Unit

```ini
[Unit]
Description=clustr node management daemon
After=network-online.target
Wants=network-online.target
ConditionPathExists=/etc/clustr/node-token
ConditionPathExists=/etc/clustr/clustrd-url

[Service]
Type=simple
User=clustr-clientd
Group=clustr-clientd
ExecStart=/usr/local/bin/clustr-clientd
Restart=on-failure
RestartSec=10
StartLimitBurst=5
StartLimitIntervalSec=300

# Resource limits — this is a lightweight daemon
MemoryMax=64M
CPUQuota=5%

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/etc/clustr /etc/sssd /etc/hosts /etc/chrony.conf /etc/ntp.conf /etc/resolv.conf
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

The `clustr-clientd` system user is created by the finalization step (using the
existing sysaccounts infrastructure — `internal/deploy/sysaccounts.go`).

### Token Management

1. At startup, `clustr-clientd` reads `/etc/clustr/node-token` into memory.
2. It sends the token as `Authorization: Bearer <token>` on WebSocket connect.
3. On 401 response, it re-reads the file from disk (handles post-reimage token
   rotation) and retries after the reconnect backoff.
4. On persistent 401 after 3 re-reads, it logs a critical error and backs off
   for 5 minutes before trying again (avoids token lockout amplification).
5. The token file is never written by `clustr-clientd` — only by the
   finalization layer.

---

## 5. Server Architecture

### New Endpoint

```
GET /api/v1/nodes/{id}/clientd/ws
```

- Auth: `requireNodeOwnership("id")` (node-scoped key, same as verify-boot)
- Protocol: WebSocket upgrade (gorilla/websocket, same upgrader as shell_ws.go)
- Registered in the **node-scope callbacks** section of `buildRouter()`, outside
  the admin-only group

### WebSocket Hub (Connection Registry)

A new `ClientdHub` struct in `internal/server/clientdhub.go`:

```go
type ClientdHub struct {
    mu    sync.RWMutex
    conns map[string]*clientdConn  // keyed by nodeID
}

type clientdConn struct {
    nodeID string
    conn   *websocket.Conn
    send   chan ServerMessage  // buffered, capacity 64
    cancel context.CancelFunc
}

// Register adds a connection, closing any existing connection for the same nodeID.
func (h *ClientdHub) Register(nodeID string, conn *clientdConn)

// Unregister removes and closes a connection.
func (h *ClientdHub) Unregister(nodeID string)

// Send queues a message to a specific node. Returns ErrNotConnected if the
// node has no active connection.
func (h *ClientdHub) Send(nodeID string, msg ServerMessage) error

// ConnectedNodes returns the list of node IDs with active connections.
func (h *ClientdHub) ConnectedNodes() []string

// IsConnected reports whether a node has an active clientd connection.
func (h *ClientdHub) IsConnected(nodeID string) bool
```

The hub is added to the `Server` struct alongside `broker *LogBroker`. A new
`handlers.ClientdHandler` wraps it.

**One connection per node:** When a new WebSocket connection arrives for a
nodeID that already has an entry in the hub, the old connection is closed
(sends websocket.CloseGoingAway) before the new one is registered. This handles
reimage + reconnect without stale connections accumulating.

### Log Storage for Node Journal Entries

Incoming `log_batch` messages from `clustr-clientd` are processed through the
existing `LogBroker.Publish()` path. They are also persisted via the existing
`DB.InsertLogBatch()` call. The `component` field is set to `"node-journal"` so
they are distinguishable from deploy-time logs.

**No new migration needed for phase 1.** The existing `node_logs` table
(migration 003) stores all log entries. The `component` column already exists
on `api.LogEntry` and in the schema.

**Retention:** Node journal logs are subject to the same `runLogPurger` that
purges deploy logs (default 14 days). No separate retention policy in phase 1.

**In-memory ring buffer per node (server-side):** The hub maintains a 500-entry
ring buffer per connected node for log entries received since connection. This
allows the Logs tab to display recent entries immediately on open, before the
SSE stream begins delivering new entries. The ring buffer is in-memory only; it
is discarded when the node disconnects.

### Heartbeat Storage

New migration `032_node_heartbeats.sql`:

```sql
-- node_heartbeats: most-recent heartbeat per node (upsert on node_id).
-- Only the current snapshot is kept; no history.
CREATE TABLE IF NOT EXISTS node_heartbeats (
    node_id      TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    received_at  INTEGER NOT NULL,
    uptime_sec   REAL,
    load_1       REAL,
    load_5       REAL,
    load_15      REAL,
    mem_total_kb INTEGER,
    mem_avail_kb INTEGER,
    disk_usage   TEXT,   -- JSON blob: []DiskUsage
    services     TEXT,   -- JSON blob: []ServiceStatus
    kernel       TEXT,
    clientd_ver  TEXT
);
```

Heartbeat receipt also updates `nodes.last_seen_at` (existing column) — same
field updated by `verify-boot` — so the existing node list UI can show "last
seen" for clientd-connected nodes without schema changes to `nodes`.

### UI Integration

**Logs tab in node detail page:**

The existing `LogStream` class in `logs.js` already handles SSE via
`/api/v1/logs/stream?mac=<node_mac>`. For node journal logs, the tab adds
`?component=node-journal` to the filter. No new SSE endpoint is needed.

**Flow when admin opens the Logs tab for a node:**

```
Browser opens /api/v1/logs/stream?mac=<node_mac>&component=node-journal
    |
    v
Server: LogBroker.Subscribe(filter)
        + Server sends log_pull_start to node via ClientdHub
    |
    v
Node: forks journalctl -f -o json
      sends log_batch messages
    |
    v
Server: receives log_batch → DB.InsertLogBatch → LogBroker.Publish
        LogBroker fans out to SSE subscribers
    |
    v
Browser: receives SSE events → LogStream.appendEntry
    |
    v
Browser navigates away / closes tab:
    SSE connection closes → Server detects subscriber gone → LogBroker.cancel
    Server sends log_pull_stop to node
    Node kills journalctl subprocess
```

**Implementation note:** The server needs to track which SSE subscriptions are
watching a specific node, so it can start/stop `journalctl` on the node
accordingly. This is managed in the `ClientdHandler` by observing when the
SSE subscriber count for a `(nodeID, component=node-journal)` filter goes from
0→1 (send `log_pull_start`) and from 1→0 (send `log_pull_stop`). A thin
`subscriptionCounter` map in `ClientdHub` handles this.

**Health display:** The node detail page shows a new "Live" badge and a compact
health summary (load, memory, disk) sourced from
`GET /api/v1/nodes/{id}/heartbeat` — a new endpoint returning the most recent
`node_heartbeats` row. Heartbeat age is shown so admins can see stale data.

---

## 6. Data Flow Diagrams

### 6.1 Node Boot: clientd Connects

```
Node boots
    |
    clustr-clientd.service starts (After=network-online.target)
    |
    Reads /etc/clustr/node-token, /etc/clustr/clustrd-url
    |
    GET ws://clustr-server:8080/api/v1/nodes/{id}/clientd/ws
         Authorization: Bearer <node-token>
    |
    Server: requireNodeOwnership validates token.node_id == {id}
    Server: ClientdHub.Register(nodeID, conn)
    Server: updates nodes.last_seen_at
    |
    Node sends: hello{hostname, kernel, uptime, clientd_version}
    Server: acks, logs connection event
    |
    [connection steady-state]
```

### 6.2 Admin Pushes Config Update

```
Admin: PUT /api/v1/nodes/{id}/config-push
       Body: {target: "hosts", content: "...", checksum: "sha256:..."}
    |
    Server validates: admin scope required
    Server: ClientdHub.Send(nodeID, ServerMessage{type: "config_push", ...})
    |
    Node receives config_push
    Node validates: target in whitelist, checksum matches
    Node: atomic write to /etc/hosts (tmp + os.Rename)
    Node: apply action (no restart needed for /etc/hosts)
    Node sends: ack{ref_msg_id, ok: true}
    |
    Server: 200 OK to admin with ack result
         OR timeout (30s) → 504 Gateway Timeout
```

### 6.3 Admin Opens Logs Tab

```
Browser: GET /api/v1/logs/stream?mac=<node_mac>&component=node-journal
    |
    Server: LogBroker.Subscribe(filter)
    Server: subscriptionCounter[nodeID]++ → was 0, now 1
    Server: ClientdHub.Send(nodeID, ServerMessage{type: "log_pull_start"})
    |
    Node: forks journalctl -f -o json --priority=7
    Node: sends log_batch{entries: [...api.LogEntry...]} to server
    |
    Server: DB.InsertLogBatch(entries)
    Server: LogBroker.Publish(entries)
    Server: SSE → Browser: "data: {log entry JSON}\n\n"
    |
    Browser navigates away: SSE connection closes
    |
    Server: LogBroker.cancel for this subscriber
    Server: subscriptionCounter[nodeID]-- → now 0
    Server: ClientdHub.Send(nodeID, ServerMessage{type: "log_pull_stop"})
    |
    Node: kills journalctl subprocess
```

### 6.4 Heartbeat Flow

```
[every 60 seconds]
    |
    Node: collect metrics (uptime, load, mem, disk, service status)
    Node sends: heartbeat{...HeartbeatPayload...}
    |
    Server: DB.UpsertHeartbeat(nodeID, payload)  // INSERT OR REPLACE
    Server: DB.UpdateLastSeen(nodeID, now)
    Server: ack → Node
    |
    [Node list UI, refreshed every 30s]
    Browser: GET /api/v1/nodes → nodes list includes last_seen_at
    Browser: shows "Live" badge if last_seen_at < 2 minutes ago
    Browser: GET /api/v1/nodes/{id}/heartbeat → compact health summary
```

---

## 7. Implementation Plan

### Phase 1: Connection + Heartbeat (MVP — ~1 week)

**Goal:** Nodes connect, stay connected, send heartbeats. Server tracks
connectivity. UI shows "Live" badge and last-seen.

1. `internal/clientd/` package: WebSocket client, reconnect loop, heartbeat
   ticker, message dispatch skeleton
2. `cmd/clustr-clientd/main.go`: systemd notify integration, signal handling,
   token file reading
3. `internal/server/clientdhub.go`: hub, register/unregister, send
4. `internal/server/handlers/clientd.go`: WebSocket upgrade handler, hello/heartbeat processors
5. `internal/db/migrations/032_node_heartbeats.sql` + DB methods
6. New route: `GET /api/v1/nodes/{id}/clientd/ws`
7. New route: `GET /api/v1/nodes/{id}/heartbeat` (returns most recent heartbeat)
8. New route: `GET /api/v1/nodes/connected` (returns list of node IDs with live connections)
9. `internal/deploy/phonehome.go`: extend `injectPhoneHome` → extract into `injectNodeAgents`,
   add `clustrd-url` and `clientd.conf` file writes
10. Build embedding: `internal/bootassets/clientd.go` (`//go:embed clustr-clientd`)
11. UI: "Live" badge on node list, last-seen display on node detail
12. systemd unit file for node

**CI:** The clientd binary is built in `ci.yml` alongside server + CLI. The
existing `Makefile` gets a `clustr-clientd` target.

### Phase 2: Log Streaming (~3 days after Phase 1)

**Goal:** Admin can open the Logs tab for a live node and see real-time journal entries.

1. `internal/clientd/journal.go`: `journalctl -f -o json` subprocess manager,
   batch collector, JSON→LogEntry parser
2. Server: `log_pull_start` / `log_pull_stop` handlers in `clientdhub.go`
3. Server: subscription counter tracking (SSE subscriber count per nodeID)
4. Server: wire LogBroker into clientd log_batch handler (already compatible)
5. UI: Logs tab for node detail page with `component=node-journal` SSE filter
   and toggle between deploy logs and live journal

### Phase 3: Config Push (~4 days after Phase 2)

**Goal:** Admin can push /etc/hosts, SSSD config, NTP config from the UI without reimaging.

1. `internal/clientd/configapply.go`: whitelist validation, atomic write, apply
   actions, rollback
2. Server: new admin route `PUT /api/v1/nodes/{id}/config-push`
3. Server: `ClientdHub.Send` + 30s timeout for ack
4. UI: Config Push panel on node detail page (target selector, textarea, apply button)
5. Tests: table-driven tests for each config target

### Phase 4: Remote Exec (~2 days after Phase 3)

**Goal:** Admin can run whitelisted diagnostic commands from the UI.

1. `internal/clientd/exec.go`: command whitelist, argument sanitization, exec +
   capture, timeout enforcement
2. Server: new admin route `POST /api/v1/nodes/{id}/exec`
3. Server: `ClientdHub.Send` + 30s timeout for exec_result
4. UI: Diagnostics panel on node detail page with command picker + output display

---

## Irreversibility Assessment

**Type 1 decisions (expensive to change later):**

- **Message format (`ClientMessage`/`ServerMessage` JSON envelope):** Once nodes
  are deployed with clientd, the wire format is an API contract. Use explicit
  versioning in the `hello` payload from day 1 (`clientd_version` field).
  The server must tolerate unknown message types gracefully (log and ignore).

- **Auth mechanism (reuse existing node-scoped token):** This is the right call.
  Changing to mTLS later would require generating client certs during
  finalization and a CA management story. The token model is simpler and already
  works.

- **Storage in existing `node_logs` table for journal entries:** This makes
  journal logs searchable via the existing query API with zero new schema work.
  The tradeoff is that high-volume journal streaming will grow the log table
  faster. Mitigation: the log purger already exists and runs hourly.

**Type 2 decisions (reversible, decide fast):**

- Heartbeat interval (60s default) — configurable via clientd.conf
- Ring buffer size (500 entries) — in-memory, trivially adjustable
- Service status whitelist — in clientd.conf, no binary change needed
- Which diagnostic commands are whitelisted — can expand in any clientd release

---

## Files To Create

### clustr-clientd Core

| File | Purpose |
|---|---|
| `cmd/clustr-clientd/main.go` | Daemon entry point |
| `internal/clientd/client.go` | WebSocket client, reconnect loop |
| `internal/clientd/heartbeat.go` | Heartbeat collection |
| `internal/clientd/journal.go` | journalctl subprocess + log_batch |
| `internal/clientd/configapply.go` | Config push handler + whitelist |
| `internal/clientd/exec.go` | Remote exec handler + whitelist |
| `internal/clientd/messages.go` | ClientMessage/ServerMessage types |
| `internal/server/clientdhub.go` | Hub, connection registry |
| `internal/server/handlers/clientd.go` | WebSocket handler, hello/heartbeat/log_batch processors |
| `internal/db/migrations/032_node_heartbeats.sql` | Heartbeat table |
| `internal/db/heartbeats.go` | DB methods for heartbeat upsert |
| `deploy/systemd/clustr-clientd.service` | Template for node systemd unit (embedded) |
| `deploy/systemd/clustr-clientd-node.service` | Embedded in finalization layer |

### Slurm Module

| File | Purpose |
|---|---|
| `internal/slurm/manager.go` | Slurm module Manager: Enable/Disable lifecycle, Status, config rendering coordination, health checks |
| `internal/slurm/routes.go` | `RegisterRoutes()` — registers all `/api/v1/slurm/*` endpoints, self-contained |
| `internal/slurm/render.go` | Template rendering engine: base template + per-node overrides → rendered per-node content |
| `internal/slurm/push.go` | Cluster-wide push orchestration: fans out via ClientdHub, tracks per-node ack/failure, updates push op state; role-aware file/script filtering |
| `internal/slurm/validate.go` | Server-side config validation: required keys, NodeName coverage, no Include directives, file size; script shebang/size/syntax checks |
| `internal/slurm/builder.go` | Build-from-source pipeline: download tarball, verify checksum, configure, make, package artifact, store in data store |
| `internal/slurm/deps.go` | Dependency management: resolve version matrix, build munge/PMIx/hwloc/UCX/libjwt, artifact reuse check, munge key generation and distribution |
| `internal/slurm/upgrade.go` | Rolling upgrade orchestration: pre-upgrade validation, phase ordering (DBD → controller → compute batches), drain/push/restart/resume, rollback |
| `internal/slurm/scripts.go` | Script versioning, push orchestration, role-filtered delivery; shebang validation; sync-status for scripts |
| `internal/slurm/roles.go` | Node role definitions, role-to-file mapping, role-to-script mapping, auto-detection heuristics |
| `internal/slurm/templates/slurm.conf.tmpl` | Default slurm.conf base template with {{range .Nodes}} block |
| `internal/slurm/templates/gres.conf.tmpl` | Default gres.conf header template |
| `internal/slurm/templates/cgroup.conf.tmpl` | Default cgroup.conf template |
| `internal/slurm/templates/topology.conf.tmpl` | Default topology.conf template |
| `internal/slurm/templates/plugstack.conf.tmpl` | Default plugstack.conf template |
| `internal/slurm/deps_matrix.json` | Bundled dependency compatibility matrix (Slurm version → PMIx/hwloc/UCX version ranges); seeded into DB at startup |
| `internal/db/migrations/033_slurm_module.sql` | Core Slurm module tables: slurm_module_config, slurm_config_files, slurm_node_overrides, slurm_node_config_state, slurm_push_operations |
| `internal/db/migrations/034_slurm_extended.sql` | Extended Slurm tables: slurm_builds, slurm_build_deps, slurm_dep_matrix, slurm_secrets, slurm_upgrade_operations, slurm_scripts, slurm_script_state, slurm_script_config, slurm_node_roles; ALTER slurm_node_config_state ADD COLUMN slurm_version |
| `internal/db/slurm.go` | All DB methods: config file CRUD, override CRUD, push op write/update, node config state upsert, drift query, build CRUD, dep matrix seed/resolve, secret encrypt/decrypt, upgrade op CRUD, script CRUD, node role CRUD |
| `internal/clientd/slurmapply.go` | Node-side: validate slurm_config_push / slurm_script_push / slurm_binary_push messages; atomic writes; chmod +x for scripts; run reconfigure or restart; fetch binary artifact via signed URL; send ack messages |

## Files To Modify

| File | Change |
|---|---|
| `internal/deploy/phonehome.go` | Extend with clientd file injection |
| `internal/deploy/finalize.go` | Add role-aware `writeSlurmConfig()` step; install Slurm binary artifact from `slurm_builds` when active build is set; write munge.key from `slurm_secrets`; write and enable role-specific services; add `/etc/sudoers.d/clustr-clientd-slurm`; add `/etc/slurm/` and `/etc/munge/` to `ReadWritePaths` |
| `internal/server/server.go` | Add `clientdHub` field, wire new routes; create SlurmManager, call `slurm.RegisterRoutes()`; expose build artifact download endpoint (signed URL); start Slurm background workers (health check + dep matrix seed) |
| `internal/server/ui/static/js/app.js` | Logs tab integration, Live badge; Slurm config page JS: version editor, push panel, node override form; Scripts tab JS: editor, version history, push; Builds page JS: trigger build, log SSE tail, delete; Upgrade wizard JS: validate, step progress, pause/resume/rollback; Role assignment checkboxes; cluster role summary |
| `internal/server/ui/static/index.html` | New UI elements; Slurm nav section: config editor, scripts tab, build management, upgrade wizard, push panel, sync status panel, role summary |
| `pkg/api/types.go` | Add `SlurmNodeConfig`, `SlurmModuleConfig`, `SlurmConfigFile`, `SlurmNodeOverride`, `SlurmPushOperation`, `SlurmBuild`, `SlurmScript`, `SlurmScriptFile`, `SlurmUpgradeOperation`, `SlurmNodeRoles` types; embed `SlurmConfig *SlurmNodeConfig` in `NodeConfig` |
| `internal/clientd/messages.go` | Add `SlurmConfigPushPayload`, `SlurmConfigAckPayload`, `SlurmScriptPushPayload`, `SlurmScriptAckPayload`, `SlurmBinaryPushPayload`, `SlurmBinaryAckPayload` types and associated result types |
| `internal/clientd/client.go` | Dispatch `slurm_config_push`, `slurm_script_push`, `slurm_binary_push` message types to `slurmapply.go` handler |
| `deploy/systemd/clustr-clientd.service` | Add `/etc/slurm/` and `/etc/munge/` to `ReadWritePaths` |
| `Makefile` | Add `clustr-clientd` build target |
| `.github/workflows/ci.yml` | Build clientd binary in CI |
| `.github/workflows/release.yml` | Include clientd in cross-platform release |

---

## 8. Slurm Module

### 8.0 Module Design Overview

Slurm configuration management is implemented as a **first-class clustr module**,
following the same architectural pattern as the LDAP module. The Slurm module
lives in `internal/slurm/` and has a singleton Manager, self-contained route
registration, its own DB layer, and a read-only projection embedded in
`NodeConfig` for use by the finalization layer.

**Key distinction from the LDAP module:** LDAP runs a local service (`slapd`)
on the clustr server and performs DIT operations (user/group CRUD). The Slurm
module does NOT run any local service. It exclusively manages config files that
are rendered server-side and pushed to nodes via the `ClientdHub`. The module
lifecycle (Enable/Disable) controls whether clustr treats itself as the
authoritative source for cluster Slurm configuration — it does not start or
stop any process on the server.

**Shared structural elements with LDAP:**
- Singleton Manager with `Enable()` / `Disable()` / `Status()` lifecycle
- `RegisterRoutes()` function registers all endpoints within the module package
- Singleton DB config table + per-node state tracking table
- Background health checker (ticks every 30s, verifies module config integrity)
- In-memory state restoration from DB on server restart
- `SlurmNodeConfig` projection embedded in `NodeConfig` (nil when module inactive)
- `writeSlurmConfig()` in `finalize.go` (non-fatal, same as `writeLDAPConfig()`)

---

### 8.1 Package Structure: `internal/slurm/`

```
internal/slurm/
    manager.go          -- Manager: Enable/Disable lifecycle, Status, config coordination
    routes.go           -- RegisterRoutes: all /api/v1/slurm/* endpoints
    render.go           -- Template rendering: base template + per-node overrides → rendered configs
    push.go             -- Cluster-wide push orchestration: fan-out, ack tracking, push op state
    validate.go         -- Server-side config validation: required keys, NodeName coverage, etc.
    templates/
        slurm.conf.tmpl     -- Default slurm.conf base template (embedded via //go:embed)
        gres.conf.tmpl      -- Default gres.conf header template
        cgroup.conf.tmpl    -- Default cgroup.conf template
        topology.conf.tmpl  -- Default topology.conf template
        plugstack.conf.tmpl -- Default plugstack.conf template
```

All route handlers are defined within the `slurm` package. There are no
cross-package handler dependencies. The Manager is the only type exported from
this package; route handlers are unexported methods or closures.

---

### 8.2 Manager (`internal/slurm/manager.go`)

The Manager is the singleton that owns the Slurm module lifecycle. One instance
is created by the server and passed to `RegisterRoutes()`.

```go
// Manager owns the Slurm module lifecycle.
type Manager struct {
    db  *db.DB
    hub *server.ClientdHub  // for push orchestration
    mu  sync.RWMutex
    cfg *SlurmModuleConfig  // in-memory cache, loaded from DB on New()
}

// New creates a Manager and restores in-memory state from the DB.
// If no config row exists (fresh install), the module starts in disabled state.
func New(db *db.DB, hub *server.ClientdHub) (*Manager, error)

// Enable activates the Slurm module with the given cluster configuration.
// Creates the default config file templates from embedded //go:embed defaults
// if no config files exist yet. Sets status to "ready".
func (m *Manager) Enable(ctx context.Context, req EnableRequest) error

// Disable marks the module disabled. Does NOT remove configs from deployed
// nodes — it only stops clustr from acting as the authoritative config source.
func (m *Manager) Disable(ctx context.Context) error

// Status returns the current module state plus a per-node sync summary.
func (m *Manager) Status(ctx context.Context) (*SlurmModuleStatus, error)

// NodeConfig returns the read-only SlurmNodeConfig projection for a given node.
// Returns nil if the module is not enabled — consumed by finalize.go to decide
// whether to call writeSlurmConfig().
func (m *Manager) NodeConfig(ctx context.Context, nodeID string) (*api.SlurmNodeConfig, error)

// Push initiates a cluster-wide config push. Delegates to push.go.
// This is the single entry point for all push operations; the route handler
// calls this, not ClientdHub directly.
func (m *Manager) Push(ctx context.Context, req PushRequest) (*api.SlurmPushOperation, error)

// RecordNodeSlurmConfigured is called by the server after receiving a
// slurm_config_ack from a node, updating slurm_node_config_state.
func (m *Manager) RecordNodeSlurmConfigured(ctx context.Context, nodeID string, results []NodeFileResult) error

// healthCheck is run every 30s in the background.
// Verifies that the module config row is consistent and that no DB-level
// corruption has occurred (e.g., missing config file rows after partial write).
func (m *Manager) healthCheck(ctx context.Context)

// StartBackgroundWorkers starts the health check ticker. Called by server.go
// after creating the Manager, same pattern as LDAP.
func (m *Manager) StartBackgroundWorkers(ctx context.Context)
```

**Enable/Disable lifecycle state machine:**

```
[not_configured]
    |
    Enable(req{cluster_name, ...})
    |-- creates default config file templates from embedded //go:embed defaults
    |-- writes slurm_module_config row (enabled=true, status="ready")
    |-- caches cfg in memory
    v
[ready]
    |
    Disable()
    |-- sets enabled=false, status="disabled"
    v
[disabled]
    |
    Enable() again → back to [ready]
```

Status values: `"not_configured"` (no DB row), `"ready"` (enabled, config
present), `"disabled"` (explicitly disabled), `"error"` (health check detected
inconsistency).

---

### 8.3 Routes (`internal/slurm/routes.go`)

```go
// RegisterRoutes registers all /api/v1/slurm/* endpoints on the given router.
// All routes require admin scope — enforced via the middleware passed in.
// No cross-package handler dependencies; all handlers are within this package.
func RegisterRoutes(router *gin.RouterGroup, m *Manager)
```

All endpoints are admin-only. The route group is `/api/v1/slurm/`. Handlers are
unexported functions in `routes.go` that call Manager methods and marshal
responses.

**Route table:**

| Method | Path | Handler | Description |
|---|---|---|---|
| `GET` | `/api/v1/slurm/status` | `handleStatus` | Module enable state + per-node sync summary |
| `POST` | `/api/v1/slurm/enable` | `handleEnable` | Enable module with cluster_name + initial config |
| `POST` | `/api/v1/slurm/disable` | `handleDisable` | Disable module |
| `GET` | `/api/v1/slurm/configs` | `handleListConfigs` | Current version of all managed config files |
| `GET` | `/api/v1/slurm/configs/{filename}` | `handleGetConfig` | Current version of one file |
| `PUT` | `/api/v1/slurm/configs/{filename}` | `handleSaveConfig` | Save new version (creates immutable version row) |
| `GET` | `/api/v1/slurm/configs/{filename}/history` | `handleConfigHistory` | Version history list |
| `GET` | `/api/v1/slurm/configs/{filename}/render/{node_id}` | `handleRenderPreview` | Dry-run render for a specific node (no DB write) |
| `GET` | `/api/v1/slurm/sync-status` | `handleSyncStatus` | Per-node drift status for all files |
| `POST` | `/api/v1/slurm/push` | `handlePush` | Initiate cluster-wide push |
| `GET` | `/api/v1/slurm/push-ops/{op_id}` | `handlePushOpStatus` | Push operation status + per-node results |
| `GET` | `/api/v1/nodes/{node_id}/slurm/overrides` | `handleGetOverrides` | Get per-node hardware override values |
| `PUT` | `/api/v1/nodes/{node_id}/slurm/overrides` | `handleSaveOverrides` | Set per-node hardware override values |
| `GET` | `/api/v1/nodes/{node_id}/slurm/sync-status` | `handleNodeSyncStatus` | This node's deployed version vs current |

The `/api/v1/nodes/{node_id}/slurm/*` routes are registered on the same
admin-scoped router group. They are self-contained within the `slurm` package
— `routes.go` receives the router group and registers them directly, without
delegating to any node handler in another package.

---

### 8.4 Database Layer (`internal/db/slurm.go`)

**Migration `033_slurm_module.sql`:**

```sql
-- slurm_module_config: singleton (id=1). Stores module enable state and
-- cluster-level settings. Mirrors the pattern of ldap_module_config.
CREATE TABLE IF NOT EXISTS slurm_module_config (
    id              INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled         INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'not_configured',  -- not_configured|ready|disabled|error
    cluster_name    TEXT,                    -- e.g. "hpc-prod"
    managed_files   TEXT NOT NULL DEFAULT '["slurm.conf","gres.conf","cgroup.conf","topology.conf","plugstack.conf"]',
                                             -- JSON array: which files this module manages
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- slurm_config_files: versioned config file storage.
-- One row per file per version. Versions are immutable once written.
-- The "current" version is MAX(version) for a given filename.
CREATE TABLE IF NOT EXISTS slurm_config_files (
    id           TEXT PRIMARY KEY,           -- UUID
    filename     TEXT NOT NULL,              -- e.g. "slurm.conf"
    version      INTEGER NOT NULL,           -- monotonically increasing per filename, starts at 1
    content      TEXT NOT NULL,              -- raw template content with {{.NodeName}} etc. markers
    is_template  INTEGER NOT NULL DEFAULT 0, -- 1 if content contains Go template markers
    checksum     TEXT NOT NULL,              -- sha256 of content
    authored_by  TEXT,                       -- admin key that created this version
    message      TEXT,                       -- optional commit message
    created_at   INTEGER NOT NULL,
    UNIQUE(filename, version)
);

-- slurm_node_overrides: per-node hardware parameters and GRES data.
-- One row per (node_id, override_key). Injected into template rendering.
CREATE TABLE IF NOT EXISTS slurm_node_overrides (
    id           TEXT PRIMARY KEY,
    node_id      TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    override_key TEXT NOT NULL,              -- e.g. "cpu_count", "sockets", "gres_conf_content"
    value        TEXT NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE(node_id, override_key)
);

-- slurm_node_config_state: per-node per-file sync tracking.
-- Records which config version is currently live on each node for each file.
-- Updated on successful push ack. Source of truth for drift detection.
-- Mirrors the pattern of ldap_node_state (configured + config hash).
CREATE TABLE IF NOT EXISTS slurm_node_config_state (
    node_id          TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    filename         TEXT NOT NULL,
    deployed_version INTEGER NOT NULL,       -- version from slurm_config_files
    content_hash     TEXT NOT NULL,          -- sha256 of the rendered content that was pushed
    deployed_at      INTEGER NOT NULL,
    push_op_id       TEXT REFERENCES slurm_push_operations(id),
    PRIMARY KEY (node_id, filename)
);

-- slurm_push_operations: push operation history.
-- Each push (even a targeted repush to a subset of nodes) creates one row.
CREATE TABLE IF NOT EXISTS slurm_push_operations (
    id               TEXT PRIMARY KEY,       -- UUID
    filenames        TEXT NOT NULL,          -- JSON array: which files were included in this push
    file_versions    TEXT NOT NULL,          -- JSON map: {filename: version_int}
    initiated_by     TEXT,                   -- admin key that triggered the push
    apply_action     TEXT NOT NULL,          -- "reconfigure" or "restart"
    status           TEXT NOT NULL,          -- pending|in_progress|completed|partial|failed
    node_count       INTEGER NOT NULL,
    success_count    INTEGER NOT NULL DEFAULT 0,
    failure_count    INTEGER NOT NULL DEFAULT 0,
    started_at       INTEGER NOT NULL,
    completed_at     INTEGER,
    node_results     TEXT                    -- JSON map: {node_id: {ok, error, file_results}}
);
```

**DB methods in `internal/db/slurm.go`:**

```go
// Module config (singleton, mirrors LDAPGetConfig / LDAPSaveConfig pattern)
func (db *DB) SlurmGetConfig(ctx context.Context) (*SlurmModuleConfig, error)
func (db *DB) SlurmSaveConfig(ctx context.Context, cfg SlurmModuleConfig) error
func (db *DB) SlurmSetStatus(ctx context.Context, status string) error

// Config file CRUD
func (db *DB) SlurmGetCurrentConfig(ctx context.Context, filename string) (*SlurmConfigFileRow, error)
func (db *DB) SlurmGetConfigVersion(ctx context.Context, filename string, version int) (*SlurmConfigFileRow, error)
func (db *DB) SlurmListConfigHistory(ctx context.Context, filename string) ([]SlurmConfigFileRow, error)
func (db *DB) SlurmSaveConfigVersion(ctx context.Context, filename, content, authoredBy, message string) (int, error)

// Per-node overrides
func (db *DB) SlurmGetNodeOverrides(ctx context.Context, nodeID string) (map[string]string, error)
func (db *DB) SlurmSaveNodeOverrides(ctx context.Context, nodeID string, overrides map[string]string) error

// Per-node config state (drift detection)
func (db *DB) SlurmGetNodeConfigState(ctx context.Context, nodeID string) ([]SlurmNodeConfigStateRow, error)
func (db *DB) SlurmUpsertNodeConfigState(ctx context.Context, nodeID, filename string, version int, contentHash, pushOpID string) error
func (db *DB) SlurmDriftQuery(ctx context.Context) ([]SlurmDriftRow, error)  // powers sync-status

// Push operations
func (db *DB) SlurmCreatePushOp(ctx context.Context, op SlurmPushOperationRow) error
func (db *DB) SlurmUpdatePushOp(ctx context.Context, opID string, updates SlurmPushOpUpdate) error
func (db *DB) SlurmGetPushOp(ctx context.Context, opID string) (*SlurmPushOperationRow, error)
```

The drift query is the same pattern as Section 8.7 in the previous design. It
computes `in_sync` / `out_of_sync` per `(node_id, filename)` by joining
`slurm_config_files` current versions against `slurm_node_config_state`.

**Additional tables (added by subsections 8.17–8.21):**

These tables are defined in a second migration `034_slurm_extended.sql` to keep
the initial module migration (033) minimal and reviewable. They are created
unconditionally alongside the Slurm module tables; rows appear only when the
corresponding features are used.

```sql
-- slurm_builds: one row per Slurm version build attempt.
CREATE TABLE IF NOT EXISTS slurm_builds (
    id               TEXT PRIMARY KEY,      -- UUID
    version          TEXT NOT NULL,         -- e.g. "24.05.3"
    arch             TEXT NOT NULL,         -- e.g. "x86_64"
    status           TEXT NOT NULL,         -- queued|building|completed|failed
    configure_flags  TEXT NOT NULL,         -- JSON array of extra ./configure flags
    artifact_path    TEXT,                  -- relative path within CLUSTR_DATA_DIR
    artifact_checksum TEXT,                 -- sha256 of the final tarball
    artifact_size_bytes INTEGER,
    initiated_by     TEXT,                  -- admin key
    log_key          TEXT,                  -- key into node_logs (component="slurm-build")
    started_at       INTEGER NOT NULL,
    completed_at     INTEGER,
    error_message    TEXT,
    UNIQUE(version, arch)
);

-- slurm_build_deps: which dependency artifacts were used in a given Slurm build.
CREATE TABLE IF NOT EXISTS slurm_build_deps (
    id               TEXT PRIMARY KEY,
    build_id         TEXT NOT NULL REFERENCES slurm_builds(id) ON DELETE CASCADE,
    dep_name         TEXT NOT NULL,         -- "munge", "pmix", "hwloc", "ucx", "libjwt"
    dep_version      TEXT NOT NULL,
    artifact_path    TEXT NOT NULL,
    artifact_checksum TEXT NOT NULL
);

-- slurm_dep_matrix: compatibility matrix between Slurm versions and dependency versions.
-- Seeded from internal/slurm/deps_matrix.json at server startup (INSERT OR IGNORE).
-- Admin-created rows (source="custom") take precedence over bundled rows.
CREATE TABLE IF NOT EXISTS slurm_dep_matrix (
    id               TEXT PRIMARY KEY,
    slurm_version_min TEXT NOT NULL,        -- e.g. "24.05.0" (inclusive)
    slurm_version_max TEXT NOT NULL,        -- e.g. "25.00.0" (exclusive)
    dep_name         TEXT NOT NULL,
    dep_version_min  TEXT NOT NULL,
    dep_version_max  TEXT NOT NULL,
    source           TEXT NOT NULL DEFAULT 'bundled',  -- "bundled" or "custom"
    created_at       INTEGER NOT NULL
);

-- slurm_secrets: encrypted cluster-level secrets (munge.key, etc.).
-- AES-256-GCM encrypted with CLUSTR_SECRET_KEY. Never returned in plaintext via API.
CREATE TABLE IF NOT EXISTS slurm_secrets (
    key_type         TEXT PRIMARY KEY,      -- "munge_key"
    encrypted_value  TEXT NOT NULL,         -- base64(AES-256-GCM(plaintext, CLUSTR_SECRET_KEY))
    rotated_at       INTEGER NOT NULL,
    rotated_by       TEXT
);

-- slurm_upgrade_operations: one row per rolling upgrade attempt.
CREATE TABLE IF NOT EXISTS slurm_upgrade_operations (
    id               TEXT PRIMARY KEY,
    from_build_id    TEXT NOT NULL REFERENCES slurm_builds(id),
    to_build_id      TEXT NOT NULL REFERENCES slurm_builds(id),
    status           TEXT NOT NULL,         -- queued|phase_dbd|phase_controller|phase_compute|completed|failed|rolled_back|paused
    batch_size       INTEGER NOT NULL,
    drain_timeout_min INTEGER NOT NULL,
    confirmed_db_backup INTEGER NOT NULL DEFAULT 0,
    initiated_by     TEXT,
    phase            TEXT,                  -- current active phase (dbd|controller|compute|done)
    current_batch    INTEGER,               -- 0-indexed batch number in compute phase
    total_batches    INTEGER,
    started_at       INTEGER NOT NULL,
    completed_at     INTEGER,
    node_results     TEXT                   -- JSON map: {node_id: {ok, version, error}}
);

-- slurm_scripts: versioned Slurm hook script storage.
CREATE TABLE IF NOT EXISTS slurm_scripts (
    id           TEXT PRIMARY KEY,
    script_type  TEXT NOT NULL,             -- "Prolog", "Epilog", "HealthCheckProgram", etc.
    version      INTEGER NOT NULL,
    content      TEXT NOT NULL,
    dest_path    TEXT NOT NULL,             -- e.g. "/etc/slurm/prolog.sh"
    checksum     TEXT NOT NULL,
    authored_by  TEXT,
    message      TEXT,
    created_at   INTEGER NOT NULL,
    UNIQUE(script_type, version)
);

-- slurm_script_state: per-node per-script deployment tracking.
CREATE TABLE IF NOT EXISTS slurm_script_state (
    node_id          TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    script_type      TEXT NOT NULL,
    deployed_version INTEGER NOT NULL,
    content_hash     TEXT NOT NULL,
    deployed_at      INTEGER NOT NULL,
    push_op_id       TEXT,
    PRIMARY KEY (node_id, script_type)
);

-- slurm_script_config: which scripts are enabled and their dest_path.
CREATE TABLE IF NOT EXISTS slurm_script_config (
    script_type  TEXT PRIMARY KEY,
    dest_path    TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    updated_at   INTEGER NOT NULL
);

-- slurm_node_roles: per-node Slurm role assignment.
CREATE TABLE IF NOT EXISTS slurm_node_roles (
    node_id     TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    roles       TEXT NOT NULL DEFAULT '[]',  -- JSON array
    auto_detect INTEGER NOT NULL DEFAULT 0,
    updated_at  INTEGER NOT NULL
);

-- slurm_node_config_state gains slurm_version column (separate ALTER in 034):
-- ALTER TABLE slurm_node_config_state ADD COLUMN slurm_version TEXT;
-- Tracks installed Slurm binary version per node, set on slurm_binary_ack receipt.
```

**Additional DB methods in `internal/db/slurm.go`:**

```go
// Builds
func (db *DB) SlurmCreateBuild(ctx context.Context, b SlurmBuildRow) error
func (db *DB) SlurmUpdateBuild(ctx context.Context, id string, u SlurmBuildUpdate) error
func (db *DB) SlurmGetBuild(ctx context.Context, id string) (*SlurmBuildRow, error)
func (db *DB) SlurmListBuilds(ctx context.Context) ([]SlurmBuildRow, error)
func (db *DB) SlurmDeleteBuild(ctx context.Context, id string) error  // validates no node refs
func (db *DB) SlurmInsertBuildDep(ctx context.Context, dep SlurmBuildDepRow) error
func (db *DB) SlurmGetBuildDeps(ctx context.Context, buildID string) ([]SlurmBuildDepRow, error)

// Dependency matrix
func (db *DB) SlurmSeedDepMatrix(ctx context.Context, entries []SlurmDepMatrixRow) error  // INSERT OR IGNORE
func (db *DB) SlurmResolveDepVersions(ctx context.Context, slurmVersion string) (map[string]SlurmDepRange, error)

// Secrets (munge key)
func (db *DB) SlurmGetSecret(ctx context.Context, keyType string) ([]byte, error)     // decrypts in memory
func (db *DB) SlurmUpsertSecret(ctx context.Context, keyType string, plaintext []byte) error  // encrypts before write

// Upgrade operations
func (db *DB) SlurmCreateUpgradeOp(ctx context.Context, op SlurmUpgradeOpRow) error
func (db *DB) SlurmUpdateUpgradeOp(ctx context.Context, id string, u SlurmUpgradeOpUpdate) error
func (db *DB) SlurmGetUpgradeOp(ctx context.Context, id string) (*SlurmUpgradeOpRow, error)
func (db *DB) SlurmListUpgradeOps(ctx context.Context) ([]SlurmUpgradeOpRow, error)

// Scripts
func (db *DB) SlurmGetCurrentScript(ctx context.Context, scriptType string) (*SlurmScriptRow, error)
func (db *DB) SlurmGetScriptVersion(ctx context.Context, scriptType string, version int) (*SlurmScriptRow, error)
func (db *DB) SlurmListScriptHistory(ctx context.Context, scriptType string) ([]SlurmScriptRow, error)
func (db *DB) SlurmSaveScriptVersion(ctx context.Context, scriptType, destPath, content, authoredBy, message string) (int, error)
func (db *DB) SlurmListScriptConfigs(ctx context.Context) ([]SlurmScriptConfigRow, error)
func (db *DB) SlurmUpsertScriptConfig(ctx context.Context, cfg SlurmScriptConfigRow) error
func (db *DB) SlurmGetScriptState(ctx context.Context, nodeID string) ([]SlurmScriptStateRow, error)
func (db *DB) SlurmUpsertScriptState(ctx context.Context, nodeID, scriptType string, version int, hash, pushOpID string) error

// Node roles
func (db *DB) SlurmGetNodeRoles(ctx context.Context, nodeID string) ([]string, error)
func (db *DB) SlurmSetNodeRoles(ctx context.Context, nodeID string, roles []string, autoDetect bool) error
func (db *DB) SlurmListNodesByRole(ctx context.Context, role string) ([]string, error)
func (db *DB) SlurmRoleSummary(ctx context.Context) (map[string]int, error)  // count per role
```

---

### 8.5 API Types (`pkg/api/types.go`)

Following the LDAP pattern, Slurm types are defined in `pkg/api/types.go`.
`SlurmNodeConfig` is the read-only client projection consumed by the
finalization layer. It is embedded in `NodeConfig` as a nullable pointer.

```go
// SlurmModuleConfig is the module state returned by GET /api/v1/slurm/status.
type SlurmModuleConfig struct {
    Enabled      bool     `json:"enabled"`
    Status       string   `json:"status"`        // not_configured|ready|disabled|error
    ClusterName  string   `json:"cluster_name"`
    ManagedFiles []string `json:"managed_files"`
}

// SlurmNodeConfig is the read-only projection embedded in NodeConfig.
// Nil means the Slurm module is not active; finalize.go skips writeSlurmConfig().
// Non-nil means the module is enabled and this node should receive Slurm configs.
type SlurmNodeConfig struct {
    ClusterName  string            `json:"cluster_name"`
    Configs      []SlurmConfigFile `json:"configs"`   // rendered content per file, ready to write
}

// SlurmConfigFile is a rendered config file, ready for delivery to a node.
type SlurmConfigFile struct {
    Filename string `json:"filename"`   // e.g. "slurm.conf"
    Path     string `json:"path"`       // e.g. "/etc/slurm/slurm.conf"
    Content  string `json:"content"`    // rendered, node-specific plain text
    Checksum string `json:"checksum"`   // sha256 of Content
    FileMode string `json:"file_mode"`  // e.g. "0644" or "0600" for slurmdbd.conf
    Owner    string `json:"owner"`      // e.g. "slurm:slurm"
    Version  int    `json:"version"`    // version number from slurm_config_files
}

// SlurmNodeOverride holds per-node hardware parameters and GRES data.
type SlurmNodeOverride struct {
    NodeID   string            `json:"node_id"`
    Params   map[string]string `json:"params"`  // keyed by override_key
    UpdatedAt int64            `json:"updated_at"`
}

// SlurmPushOperation is the push operation status returned by the push endpoints.
type SlurmPushOperation struct {
    ID           string                     `json:"id"`
    Filenames    []string                   `json:"filenames"`
    FileVersions map[string]int             `json:"file_versions"`
    ApplyAction  string                     `json:"apply_action"`
    Status       string                     `json:"status"`
    NodeCount    int                        `json:"node_count"`
    SuccessCount int                        `json:"success_count"`
    FailureCount int                        `json:"failure_count"`
    StartedAt    int64                      `json:"started_at"`
    CompletedAt  *int64                     `json:"completed_at,omitempty"`
    NodeResults  map[string]SlurmNodeResult `json:"node_results,omitempty"`
}

// SlurmNodeResult is the per-node push result included in SlurmPushOperation.
type SlurmNodeResult struct {
    OK          bool               `json:"ok"`
    Error       string             `json:"error,omitempty"`
    FileResults []SlurmFileResult  `json:"file_results"`
    ApplyResult SlurmApplyResult   `json:"apply_result"`
}

type SlurmFileResult struct {
    Filename string `json:"filename"`
    OK       bool   `json:"ok"`
    Error    string `json:"error,omitempty"`
}

type SlurmApplyResult struct {
    Action   string `json:"action"`
    OK       bool   `json:"ok"`
    ExitCode int    `json:"exit_code"`
    Output   string `json:"output,omitempty"`
}
```

**Embedding in `NodeConfig`:**

```go
// NodeConfig is the full node projection returned to clients.
type NodeConfig struct {
    // ... existing fields ...
    LDAPConfig  *LDAPNodeConfig  `json:"ldap_config,omitempty"`   // existing
    SlurmConfig *SlurmNodeConfig `json:"slurm_config,omitempty"`  // new: nil if module inactive
}
```

`Manager.NodeConfig(ctx, nodeID)` renders all managed config files for the node
and returns a `*api.SlurmNodeConfig`. This is called from the server's
`NodeConfig(ctx)` handler, the same pattern as `LDAPNodeConfig(ctx)`.

---

### 8.6 Template Rendering (`internal/slurm/render.go`)

The renderer is responsible for producing per-node rendered content from the
base template plus per-node override values. It is called by the Manager in
two contexts:

1. **Push orchestration** (`push.go`): Renders all managed files for every node
   in the cluster before fanning out via `ClientdHub`. Renders are cached in
   memory for the duration of a single push operation (avoid re-querying the
   same override rows N times for N nodes receiving the same slurm.conf).

2. **Preview** (route handler for `render/{node_id}`): Renders for one node
   without writing anything. Returns plain text to the browser.

**Template data model:**

```go
// ClusterRenderContext is passed to slurm.conf template execution.
type ClusterRenderContext struct {
    ClusterName string
    Timestamp   string
    Nodes       []NodeRenderContext
}

// NodeRenderContext provides the per-node variables available in templates.
// Values come from slurm_node_overrides rows for this node.
type NodeRenderContext struct {
    NodeName       string  // nodes.hostname
    CPUCount       int     // override key: cpu_count
    Sockets        int     // override key: sockets
    CoresPerSocket int     // override key: cores_per_socket
    ThreadsPerCore int     // override key: threads_per_core
    RealMemoryMB   int     // override key: real_memory_mb
    GRESParam      string  // override key: gres_param, e.g. "gpu:a100:2"
    HasGRES        bool    // derived: GRESParam != ""
    SlurmdRole     string  // override key: slurm_role ("worker" or "controller")
}
```

**For `gres.conf`:** Each node receives a different rendered file. The template
is the comment header only; the per-node body is appended from override key
`gres_conf_content`. A node with no `gres_conf_content` override receives the
header only (valid empty `gres.conf`).

**For `slurm.conf`:** Every node receives the same rendered file (the
`{{range .Nodes}}` block expands the complete cluster node list). The render
is computed once and reused for all target nodes during a push operation.

**Template source:** Templates are loaded from `internal/slurm/templates/`
using `//go:embed templates/*.tmpl`. If a config file version stored in
`slurm_config_files` overrides the default template (because the admin has
edited it), the DB version takes precedence over the embedded default.

---

### 8.7 Push Orchestration (`internal/slurm/push.go`)

`push.go` implements `Manager.Push()`. The push is a cluster-wide fan-out, not
a per-node individual operation. The Manager owns the push; route handlers do
not interact with `ClientdHub` directly.

**Wire protocol extension:** Two new message types are added to the existing
WebSocket protocol:

```go
// ServerMessage type: "slurm_config_push"
type SlurmConfigPushPayload struct {
    PushOpID    string            `json:"push_op_id"`
    Files       []SlurmConfigFile `json:"files"`       // api.SlurmConfigFile, rendered per-node
    ApplyAction string            `json:"apply_action"` // "reconfigure" or "restart"
}

// node→server type: "slurm_config_ack"
type SlurmConfigAckPayload struct {
    PushOpID    string           `json:"push_op_id"`
    FileResults []SlurmFileResult `json:"file_results"`
    ApplyResult SlurmApplyResult  `json:"apply_result"`
}
```

These are defined in `internal/clientd/messages.go` alongside the existing
`ClientMessage`/`ServerMessage` types. The `SlurmConfigPushPayload` uses
`api.SlurmConfigFile` from `pkg/api/types.go` (shared wire type).

**Push sequence:**

```
Manager.Push(ctx, req{filenames, apply_action, node_ids, message})
    |
    Load current config file versions from DB (slurm_config_files)
    Load all node overrides from DB (slurm_node_overrides, batch query)
    Render all files for all target nodes (render.go)
    Server-side validation (validate.go): reject if any file fails
    |
    Create slurm_push_operations row (status="in_progress")
    |
    For each target node (parallel goroutines):
        Identify offline nodes (ClientdHub.IsConnected → false):
            Record as "offline" failure immediately; do not wait
        For connected nodes:
            ClientdHub.Send(nodeID, ServerMessage{
                Type:    "slurm_config_push",
                Payload: SlurmConfigPushPayload{push_op_id, files, apply_action},
            })
            Start 60s ack timer
    |
    Collect acks as they arrive (channel, one per node):
        On slurm_config_ack received:
            SlurmUpsertNodeConfigState for each file where result.OK
            Update push op success/failure counts
        On timeout:
            Record as "timeout" failure
    |
    After all nodes resolved:
        Set push op status: completed / partial / failed
        Set completed_at
    |
    Return *api.SlurmPushOperation to caller
```

**Offline node handling:** Offline nodes are flagged immediately as failures
before the push begins. This is not a blocking condition; the push proceeds to
online nodes. The push operation status reflects offline nodes as failures.

**Targeted repush:** `PushRequest.NodeIDs` filters the target node list. An
empty `NodeIDs` field means "all nodes in cluster." The route handler passes
the admin-supplied node list (if any) through to `Manager.Push()`.

**Controller-last ordering for `apply_action: "restart"`:** When the push
includes a `slurmdbd.conf` file (controller-only) or when the apply action is
`"restart"`, the push loop processes the controller node last. Worker nodes
are sent the push first (in parallel), then the controller goroutine fires
after all worker goroutines have been dispatched. This minimizes scheduling
disruption from `slurmctld` restart.

---

### 8.8 Managed Config Files and Per-Node Variation

clustr manages the following Slurm config files. These are the files where
cluster-wide drift causes real operational failures.

| File | Path on nodes | Uniform or per-node | Handling |
|---|---|---|---|
| `slurm.conf` | `/etc/slurm/slurm.conf` | Both — uniform structure, per-node NodeName lines | `{{range .Nodes}}` block in template; all nodes receive same rendered file |
| `gres.conf` | `/etc/slurm/gres.conf` | Per-node | Comment header template + per-node body from override key `gres_conf_content` |
| `cgroup.conf` | `/etc/slurm/cgroup.conf` | Cluster-uniform | No template markers; all nodes receive identical content |
| `topology.conf` | `/etc/slurm/topology.conf` | Cluster-uniform | No template markers; all nodes receive identical content |
| `plugstack.conf` | `/etc/slurm/plugstack.conf` | Cluster-uniform (base case) | Template conditionals available for GPU-only plugin lines |
| `slurmdbd.conf` | `/etc/slurm/slurmdbd.conf` | Controller-only | Delivered only to nodes with `slurm_role=controller`; stored AES-256-GCM encrypted in DB |

**Files clustr does NOT manage:**

- `/etc/slurm/epilog`, `/etc/slurm/prolog`, `/etc/slurm/task_prolog` — scripts,
  not config files. They belong in the clustr image, not pushed at runtime.
- Any site-local config referenced via `Include` directives — clustr does not
  parse or follow includes. Validation rejects configs containing Include lines.

**Template variables per node:**

| Variable | Source |
|---|---|
| `{{.NodeName}}` | `nodes.hostname` |
| `{{.CPUCount}}` | override key `cpu_count` |
| `{{.Sockets}}` | override key `sockets` |
| `{{.CoresPerSocket}}` | override key `cores_per_socket` |
| `{{.ThreadsPerCore}}` | override key `threads_per_core` |
| `{{.RealMemoryMB}}` | override key `real_memory_mb` |
| `{{.GRESParam}}` | override key `gres_param` (e.g. `gpu:a100:2`) |
| `{{.HasGRES}}` | derived: `GRESParam != ""` |

The template model deliberately limits injectable fields to hardware-descriptor
values that legitimately differ per node. Structural content (auth, topology
plugin, partition definitions, scheduler settings) is uniform and lives in the
base template verbatim. This keeps the template engine from becoming an
arbitrary code-generation system.

---

### 8.9 Validation (`internal/slurm/validate.go`)

Validation runs in two stages: server-side before sending, node-side after
receiving. Server-side catches logical errors before any traffic reaches nodes.
Node-side is a safety net for transmission errors.

**Server-side (validate.go):**

1. **Template render check:** Render against all cluster nodes. Any rendering
   failure on any node rejects the version before it is saved to DB.
2. **Structural checks:**
   - File size < 2 MB
   - `slurm.conf` must contain `ClusterName=`, `SlurmctldHost=`, `AuthType=`
   - `NodeName=` lines must match the registered node set (warn on missing;
     error on unknown hostname)
   - No `Include` directives
3. **Optional `slurmctld -t` dry-run:** If a controller node is connected,
   the server can send an `exec_request` for `slurmctld -t -f <tempfile>` to
   validate syntax. Optional in v1; skipped if controller is offline or admin
   bypasses it.

**Node-side (`internal/clientd/slurmapply.go`):**

1. `sha256(content) == checksum` — mismatch → reject, ack `ok: false`
2. Target path in `/etc/slurm/` whitelist — reject anything else
3. Content size < 2 MB

---

### 8.10 Reconfigure vs. Restart

The push request carries an `apply_action` field: `"reconfigure"` or
`"restart"`. The node-side handler in `slurmapply.go` executes the chosen
action after writing all files atomically.

**`apply_action: "reconfigure"` (preferred — no job disruption):**

```
clientd receives slurm_config_push
    write all files atomically (tmp + os.Rename)
    run: scontrol reconfigure
    capture exit code + output (30s timeout)
    if exit code != 0: record in ack; do NOT restore .bak files
        (files are correct; reconfigure failure is a slurmd diagnostic issue)
    send slurm_config_ack
```

Safe for: NodeName hardware changes, partition changes, scheduler param changes,
adding nodes. NOT safe for: AuthType, CryptoType, TopologyPlugin, SelectType,
port changes.

**`apply_action: "restart"` (required for plugin/auth changes):**

```
clientd receives slurm_config_push
    write all files atomically (with .bak of each previous file)
    run: systemctl restart slurmd (via sudoers)
    wait up to 30s for active state
    if slurmd fails to start:
        restore all .bak files
        attempt recovery restart
        send slurm_config_ack{ok: false, output: journalctl excerpt}
    else:
        send slurm_config_ack{ok: true}
```

The UI warns and pre-selects `"restart"` when the config diff touches `AuthType`,
`CryptoType`, `TopologyPlugin`, `SelectType`, any port field, or any plugin path.
The server does not auto-detect this; the operator makes the final choice.

**Permission for `systemctl restart slurmd`:** The `clustr-clientd` system user
requires a narrow sudoers entry, written by `writeSlurmConfig()` in finalize.go:

```
# /etc/sudoers.d/clustr-clientd-slurm
clustr-clientd ALL=(ALL) NOPASSWD: /bin/systemctl restart slurmd
clustr-clientd ALL=(ALL) NOPASSWD: /bin/systemctl restart slurmctld
```

Both lines are written to all nodes; the server never sends a `slurmctld`
restart to non-controller nodes.

---

### 8.11 Server Wiring (`internal/server/server.go`)

Follows the same pattern as LDAP wiring:

```go
// In Server struct:
slurmManager *slurm.Manager

// In New() / buildRouter():
slurmManager, err := slurm.New(db, clientdHub)
if err != nil {
    return nil, fmt.Errorf("slurm manager: %w", err)
}
slurm.RegisterRoutes(adminGroup, slurmManager)
slurmManager.StartBackgroundWorkers(ctx)

// Exposed to node-config handler:
func (s *Server) SlurmNodeConfig(ctx context.Context, nodeID string) (*api.SlurmNodeConfig, error) {
    return s.slurmManager.NodeConfig(ctx, nodeID)
}

// Called by clientd WebSocket handler on slurm_config_ack receipt:
func (s *Server) RecordNodeSlurmConfigured(ctx context.Context, nodeID string, results []slurm.NodeFileResult) error {
    return s.slurmManager.RecordNodeSlurmConfigured(ctx, nodeID, results)
}
```

The `clientd.go` WebSocket handler dispatches `slurm_config_ack` messages to
`s.RecordNodeSlurmConfigured()` — same callback pattern as
`RecordNodeLDAPConfigured()`.

---

### 8.12 Finalization Integration (`internal/deploy/finalize.go`)

When the Slurm module is enabled, `NodeConfig` includes a non-nil `SlurmConfig`.
The finalization layer checks this and calls `writeSlurmConfig()`.

```
writeSlurmConfig(nodeID, slurmConfig, rootfsMountPath):
    for each file in slurmConfig.Configs:
        write rendered content to rootfsMountPath + file.Path
        set permissions per file.FileMode (0644 default, 0600 for slurmdbd.conf)
        set ownership: slurm:slurm
    write /etc/sudoers.d/clustr-clientd-slurm (sudoers entry for slurmd restart)
    enable slurmd.service in chroot (systemctl enable --root=rootfsMountPath slurmd)
    record in slurm_node_config_state: this node is at the current version for each file
```

**Non-fatal:** Errors in `writeSlurmConfig()` are logged and reported in the
finalization log but do not abort the imaging operation. Same behavior as
`writeLDAPConfig()`. A node that boots without Slurm configs will have
`slurm_node_config_state` showing no deployed versions, and the sync-status
dashboard will flag it as out-of-sync. The admin can trigger a targeted push
after the node connects.

**Effect on drift detection:** A freshly imaged node whose finalization wrote
Slurm configs is recorded in `slurm_node_config_state` at the current version.
The drift query will show it as `in_sync` from the moment it boots. No push
is required unless a new config version is saved between finalization and first
boot.

---

### 8.13 Hardware Auto-Discovery on Boot

When a node completes the `verify-boot` handshake, it can include Slurm hardware
parameters in the payload under an optional `slurm_hardware` key. The data source
is `slurmd -C` output, which reports Slurm's view of the hardware (CPUs, memory,
GRES, sockets, threads). The server writes these to `slurm_node_overrides` using
`INSERT OR IGNORE` — manually set values are never overwritten.

This is an optional enhancement. If `slurmd` is not installed on the rootfs
(non-Slurm image) or if the field is absent, the server skips the override
insert. The verify-boot flow is unaffected.

**Auto-discovery fields written to `slurm_node_overrides`:**

| Override key | Source in `slurmd -C` output |
|---|---|
| `cpu_count` | `CPUTot` |
| `sockets` | `Sockets` |
| `cores_per_socket` | `CoresPerSocket` |
| `threads_per_core` | `ThreadsPerCore` |
| `real_memory_mb` | `RealMemory` |

GRES lines are not auto-discovered from `slurmd -C` (the output format is not
reliable enough for automatic GRES parsing). The admin sets `gres_param` and
`gres_conf_content` overrides manually via the node overrides UI.

---

### 8.14 UI Design

The Slurm module has a dedicated nav section in the clustr UI, appearing alongside
Nodes and the LDAP section. It follows the same visual pattern as the LDAP UI.

**Nav entry: "Slurm"** — shows a status badge: green "enabled" / grey
"disabled" / red "error". Clicking opens the Slurm settings page.

#### Settings Page

- Module enable/disable toggle (calls `POST /api/v1/slurm/enable` or
  `/api/v1/slurm/disable`)
- Cluster name field (set on Enable; displayed read-only when enabled)
- Managed files list (which files the module is currently tracking)
- Module status and last health check result

#### Config Editor

File tabs: `slurm.conf` | `gres.conf` | `cgroup.conf` | `topology.conf`
| `plugstack.conf` (tabs shown only for files in `managed_files`).

For each file:
- Read-only view of current version (base template content, not a specific
  node's rendered output), monospace font
- **Edit button** opens the template in a `<textarea>` (no heavy JS editor
  dependency in v1)
- **Save** → `PUT /api/v1/slurm/configs/{filename}` creates an immutable new
  version row; does not push automatically
- **Version history** sidebar: timestamp, author, message, "Restore" button
  (creates new version with old content, maintains immutable history)

#### Node Overrides

Accessible from node detail page → "Slurm Overrides" tab.

Form fields: CPU Count, Sockets, Cores/Socket, Threads/Core, Real Memory (MB),
GRES Parameter, GRES Config Content (textarea for this node's full `gres.conf`
body). Save → `PUT /api/v1/nodes/{id}/slurm/overrides`. Does not immediately
push.

"Preview Rendered Config" button → `GET /api/v1/slurm/configs/{filename}/render/{node_id}`
— renders and displays the file as this node would receive it. No DB write.

#### Push Panel

Triggered from the config editor view via **"Push to Cluster" button**.

- File checkboxes (default: all managed files)
- Apply action radio: "scontrol reconfigure" vs "Restart slurmd"
- Warning banner when diff touches auth/topology/plugin fields; restart
  pre-selected in that case
- Pre-push preview table: per-node current version vs version about to be pushed;
  offline nodes flagged in grey
- Optional commit message field
- **Push** button → `POST /api/v1/slurm/push`

**Push progress:** After submit, live progress table polled via
`GET /api/v1/slurm/push-ops/{op_id}` every 2 seconds:

```
Node          | slurm.conf    | gres.conf     | Apply          | Status
---------------------------------------------------------------------------
node01        | v3 -> v4 OK  | v2 -> v3 OK  | reconfigure OK | in_sync
node02        | v3 -> v4 OK  | v2 -> v3 OK  | reconfigure OK | in_sync
node03        | v3 -> v4 FAIL| v2 -> v3 OK  | --             | FAILED
node04 (off)  | offline       | offline       | --             | out_of_sync
```

Failures show the error string inline. "Retry Failed Nodes" re-triggers the push
to the failed/offline subset only.

#### Sync Status Dashboard

Compact panel on the cluster overview page:

```
Slurm Config                               [Push all out-of-sync]
  slurm.conf   v4    42/44 nodes in sync  [2 out of sync - Push]
  gres.conf    v3    44/44 nodes in sync
  cgroup.conf  v1    44/44 nodes in sync
```

Powered by `GET /api/v1/slurm/sync-status`. "Push" links pre-populate the push
panel with only the out-of-sync file and only the out-of-sync nodes targeted.

---

### 8.15 Security Considerations

Slurm configuration controls job submission policy, resource limits,
authentication plugin selection, and SPANK plugin loading. A malicious config
push can disable a partition, break cluster auth, or inject code at job start
via `PrologFlags`. This makes the Slurm config push higher-stakes than pushing
`/etc/hosts`.

**Mitigations:**

1. **Admin scope only.** All Slurm config write and push endpoints require
   admin-scoped API keys. Node-scoped keys (used by `clustr-clientd`) can only
   send `slurm_config_ack` messages — they cannot call any write or push endpoint.

2. **Version immutability + full audit trail.** Every config version is immutable
   and attributed to an admin key and timestamp. `slurm_push_operations` records
   every push with per-node results. There is no silent-edit path.

3. **Node-side path enforcement.** `clustr-clientd` only accepts writes to the
   explicit `/etc/slurm/` whitelist. A compromised server cannot use the
   `slurm_config_push` message type to write outside that whitelist.

4. **`slurmdbd.conf` encrypted at rest.** Stored as `AES-256-GCM(content,
   CLUSTR_SECRET_KEY)` in `slurm_config_files.content`. Decrypted in memory only
   at render/push time. In transit over TLS. Written to node as `0600 slurm:slurm`.
   Reuses the existing server secret management pattern — no new secret surface.

5. **Worst-case blast radius is bounded to the cluster.** A compromised server
   can push bad Slurm configs to all connected nodes. It cannot use `slurm_config_push`
   to escape to arbitrary file writes or root execution. The `clustr-clientd` user
   has only `systemctl restart slurmd` via sudoers — no root shell, no path to
   privilege escalation.

6. **Confirmation gate (v2).** For changes touching `AuthType`, `CryptoType`,
   `TopologyPlugin`, or `SelectType`, add a two-step stage/confirm API. In v1,
   the UI warning is the only gate.

---

### 8.16 Irreversibility Assessment

**Type 1 decisions (expensive to change later):**

- **Module-scoped singleton design (`slurm_module_config` id=1).** This is
  the correct model: clustr is either the authoritative Slurm config source for
  the cluster or it is not. A per-cluster multi-tenant model would be the
  correct next step if clustr ever manages multiple independent Slurm clusters,
  but that requires a schema migration. For v1 (one cluster per clustr instance),
  the singleton is right.

- **`slurm_node_config_state` with `(node_id, filename)` primary key and
  version integer.** This is the source of truth for drift detection and
  rollback targeting. Do not simplify to a boolean `in_sync` flag — the version
  integer is necessary for "which version is deployed here" queries and for
  version history display. Changing this later requires migrating state and
  recomputing from push op history.

- **Push operation as a first-class DB entity (`slurm_push_operations`), not
  ephemeral in-memory state.** Push operations must survive server restarts so
  an admin can see the result of a push that completed while the server was
  restarting. Making this ephemeral would make the audit trail unreliable.

- **Slurm push uses the existing `ClientdHub` transport (no separate channel).**
  The `slurm_config_push` message type rides the same WebSocket connection as
  `config_push`, `exec_request`, and log streaming. Adding a separate transport
  for Slurm would fragment the connection model and add operational complexity.
  Locked in by the Section 1 transport decision.

**Type 2 decisions (reversible, decide fast):**

- **`text/template` as the rendering engine.** If operators need more power
  (per-GPU loops, feature-flag conditionals), switching engines is a server-side
  change. Existing template content in `slurm_config_files` is just a text
  column — it can be migrated. Decide fast, change later if needed.

- **No version purging in v1.** At realistic cluster sizes (50-200 nodes, monthly
  config changes, ~5 KB per version), the table stays well under 100 MB for
  years. Add a `DELETE WHERE version < (MAX - N)` API later if operators ask.

- **60-second ack timeout.** `systemctl restart slurmd` typically finishes in
  2-5 seconds; 60s is very conservative. Configurable per push request. Tighten
  if operators are waiting unnecessarily.

- **Push progress polled every 2 seconds (not SSE).** Simple and stateless.
  Upgrade to SSE if polling lag is noticeable in practice.

---

### 8.17 Build from Source (`internal/slurm/builder.go`)

#### Rationale

Distro-packaged Slurm is categorically unsuitable for HPC environments. Rocky
Linux 9 ships Slurm 23.02 at time of writing; production clusters run 23.11 or
24.05. The version gap is not cosmetic — new OpenMPI releases require PMIx APIs
that older Slurm versions do not expose, and NVIDIA GPU support (MIG partitioning,
`nvml` cgroup enforcement) is gated behind newer slurmctld releases. More
importantly, the controller and all compute nodes must run **exactly the same
Slurm version**. Using distro packages makes that guarantee impossible across a
heterogeneous cluster. Building from source, storing the resulting artifacts in
the clustr data store, and distributing them during imaging is the correct model.

The build subsystem lives at `internal/slurm/builder.go`. It is a distinct
concern from config management and is invoked independently from the Slurm
module's config push path. Build artifacts are stored in the clustr data store
and referenced by version; config push and finalization pull them from there.

#### Build Pipeline

```
Admin triggers build via UI or API
    |
    POST /api/v1/slurm/builds
    Body: { version: "24.05.3", configure_flags: [...], dependencies: {...} }
    |
    Server: SlurmBuildManager.StartBuild(req)
    Creates slurm_builds row (status="queued")
    Launches background goroutine
    Returns: { build_id: "uuid", status: "queued" }
    |
    [background goroutine]
    |
    1. Resolve and build dependencies (see 8.19):
       - munge (if not already built for this arch)
       - PMIx (version from dependency matrix)
       - hwloc (if system version < minimum)
       - UCX (if --with-ucx requested and not built)
       - Each dependency: download tarball → verify checksum → ./configure → make → make install DESTDIR
       - Store each dep artifact in data store, record in slurm_build_deps
    |
    2. Download Slurm tarball:
       https://download.schedmd.com/slurm/slurm-{version}.tar.bz2
       Verify SHA256 against schedmd's published checksum file
    |
    3. Run configure:
       ./configure \
         --prefix=/usr \
         --sysconfdir=/etc/slurm \
         --with-munge \
         --with-pmix=/opt/clustr/deps/pmix/{version} \
         --with-hwloc=/opt/clustr/deps/hwloc/{version} \
         --with-ucx=/opt/clustr/deps/ucx/{version} \
         --enable-pam \
         --with-lz4 \
         --with-json \
         --with-http-parser \
         --with-yaml \
         --with-jwt \
         [additional flags from build request]
    |
    4. make -j$(nproc)
    |
    5. Package output:
       make install DESTDIR=/tmp/slurm-{version}-stage
       tar -czf slurm-{version}-{arch}.tar.gz -C /tmp/slurm-{version}-stage .
       sha256sum → stored in slurm_builds.artifact_checksum
    |
    6. Store artifact:
       Write tarball to CLUSTR_DATA_DIR/slurm-builds/{version}/{arch}/slurm-{version}.tar.gz
       Update slurm_builds: status="completed", artifact_path, artifact_checksum, completed_at
    |
    7. Stream build log lines to slurm_build_logs (same pattern as deploy logs)
       Admin can tail via SSE while build is in progress
```

Build log lines are stored per-phase (dependency, configure, make, package) so
failures are localized to the exact step.

#### Build Artifact Layout in Data Store

```
CLUSTR_DATA_DIR/
  slurm-builds/
    24.05.3/
      x86_64/
        slurm-24.05.3.tar.gz        -- Slurm installation tree (staged)
        slurm-24.05.3.tar.gz.sha256
        deps/
          munge-0.5.16.tar.gz
          pmix-5.0.3.tar.gz
          hwloc-2.10.0.tar.gz
    23.11.10/
      x86_64/
        slurm-23.11.10.tar.gz
        ...
```

Multiple versions coexist in the store. Each version directory is immutable once
`status="completed"`. Deleting a build artifact requires an explicit API call
that first verifies no node is referencing that version (checked via
`slurm_node_config_state.slurm_version` — see 8.21).

#### Multiple Version Support

The `slurm_module_config` table gains an `active_version` field pointing to the
`slurm_builds.id` that is currently deployed cluster-wide. A second field
`pending_version` holds the upgrade target during a rolling upgrade (see 8.18).
Nodes record their currently installed Slurm version in `slurm_node_config_state`
(new `slurm_version` column added alongside the existing `deployed_version`).

**Use case — staged upgrades:** A cluster running 23.11.10 builds 24.05.3 in
the background while jobs are running. The new version is available in the store.
The upgrade workflow (Section 8.18) references the build artifact by its
`slurm_builds.id`. The old artifact remains in the store for rollback until the
admin explicitly purges it.

#### Routes

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/slurm/builds` | List all builds (version, status, artifact size, timestamps) |
| `POST` | `/api/v1/slurm/builds` | Trigger a new build |
| `GET` | `/api/v1/slurm/builds/{build_id}` | Build detail: status, log stream URL, artifact info |
| `DELETE` | `/api/v1/slurm/builds/{build_id}` | Delete artifact (validated: no node referencing this version) |
| `GET` | `/api/v1/slurm/builds/{build_id}/log` | SSE stream of build log lines (same pattern as deploy logs) |
| `GET` | `/api/v1/slurm/builds/configure-flags` | Return the configure flags used in a given build (for reference when building next version) |

#### Finalization Integration

When `writeSlurmConfig()` runs during finalization and a `slurm_builds` artifact
is the active version, the finalization layer:

1. Copies the artifact tarball into the node's rootfs staging area
2. Extracts it to `/` within the rootfs chroot (installs binaries, libs, manpages)
3. Runs `ldconfig` in the chroot to update the shared library cache
4. Enables `slurmd.service` (already handled by the existing step in 8.12)

This replaces any distro-packaged Slurm that may be present in the base image.
The artifact install is idempotent: re-imaging a node that already has the same
Slurm version installed is safe (same files, same paths, overwritten in place).

If no `slurm_builds` artifact is configured (build subsystem not used), the
finalization step skips artifact installation. In this case the base image is
expected to contain Slurm already, or the admin has opted to manage Slurm
installation outside of clustr.

#### Security

- Tarball checksums from schedmd are verified against the published `.sha256`
  file fetched over HTTPS (not the same connection as the tarball download, to
  provide mild TOCTOU protection). SHA256 mismatch aborts the build immediately.
- All build operations run as a dedicated `clustr-builder` system user with write
  access only to `CLUSTR_DATA_DIR/slurm-builds/` and `/tmp/`. No root.
- The `make install DESTDIR` step does not touch the live system. Installation
  into the running clustr server host is never performed.

---

### 8.18 Slurm Upgrades (`internal/slurm/upgrade.go`)

#### Design Principles

Slurm has strict version compatibility rules that must be encoded in the upgrade
workflow, not left to the operator to remember:

1. `slurmctld` (controller) must be upgraded **before** `slurmd` (compute nodes).
   Running compute nodes with a newer slurmd than slurmctld causes registration
   failures and dropped jobs.
2. `slurmdbd` must be upgraded before or simultaneously with `slurmctld`.
3. The version delta must not exceed one major release (e.g., 23.11 → 24.05 is
   permitted; 22.05 → 24.05 requires an intermediate step).
4. Downgrading across a minor release boundary is not supported by Slurm and
   must be blocked by clustr (rollback within the same minor is safe).

clustr enforces rules 1, 2, and 4 automatically. Rule 3 is validated and surfaced
as a warning (not a hard block, since cluster-specific factors may apply).

#### Pre-Upgrade Validation

```
POST /api/v1/slurm/upgrade
Body: { target_build_id: "uuid", drain_timeout_minutes: 30, batch_size: 10 }
    |
    Server: SlurmUpgradeManager.Validate(req)
    |
    Checks:
    1. target_build_id references a completed build in slurm_builds
    2. Version delta: parse major.minor of current vs target; warn if > 1 minor delta
    3. Running job check: exec_request to controller → squeue -h | wc -l
       If > 0: surface count to admin in validation response (not a hard block)
    4. All controller nodes connected (ClientdHub.IsConnected): warn if not
    5. DB backup prompt:
       Response includes { db_backup_required: true, message: "..." }
       Admin must include { confirmed_db_backup: true } in the upgrade request
       to proceed. This is a UX gate, not a DB-level lock.
    6. Check slurmdbd node is connected (DBD must be upgraded before controller)
    |
    Return: {
        valid: true/false,
        warnings: [...],
        errors: [...],
        upgrade_plan: {
            phase_1: ["dbd-node01"],           -- slurmdbd node(s)
            phase_2: ["controller-node01"],    -- slurmctld node(s)
            phase_3_batches: [                 -- compute nodes, batched
                ["node01","node02",...,"node10"],
                ["node11","node12",...,"node20"],
                ...
            ],
        }
    }
```

#### Rolling Upgrade Sequence

```
[Phase 1: DBD upgrade]
    Drain: slurmdbd stops accepting new accounting writes (graceful shutdown)
    Upgrade: push Slurm artifact to DBD node via ClientdHub (new msg type: slurm_binary_push)
    Apply: systemctl restart slurmdbd
    Wait: up to 60s for slurmdbd to report active via heartbeat
    If failed: halt upgrade, surface error, DBD still at old version
    |
[Phase 2: Controller upgrade]
    Drain: scontrol drain <controller> (allows running jobs to migrate if HA)
    Upgrade: push artifact to controller node
    Apply: systemctl restart slurmctld
    Wait: up to 120s for slurmctld to report active (longer: state restore from StateSaveLocation)
    Resume: scontrol resume <controller>
    If failed: halt upgrade, surface error
    |
[Phase 3: Compute nodes — rolling batches]
    For each batch of N nodes (admin-configured batch_size, default 10):
        Drain batch: scontrol update NodeName=node01,node02,...,nodeN State=DRAIN Reason="clustr-upgrade"
        Wait for drain: poll squeue --nodes=<batch> until no running jobs (timeout: drain_timeout_minutes)
        Push artifact to batch nodes in parallel (slurm_binary_push)
        Apply: systemctl restart slurmd on each node
        Wait: 30s for slurmd to report active on all nodes in batch
        Resume batch: scontrol update NodeName=... State=RESUME
        Record: update slurm_node_config_state.slurm_version for each node in batch
        If any node failed: pause, surface error. Admin can skip or retry individual nodes.
    |
[Upgrade complete]
    Update slurm_module_config.active_version = target_build_id
    Clear pending_version
    Create slurm_upgrade_operations row: completed_at, per-node results
```

**Batch size advisory:** A batch of 10 is a safe default for 100-200 node
clusters. At a drain timeout of 30 minutes per batch, a 200-node cluster takes
roughly 20 batches × 5 minutes average drain = under 2 hours for the compute
upgrade phase, assuming typical HPC job durations. The operator configures this
based on their workload profile.

**New wire message type: `slurm_binary_push`:**

```go
// ServerMessage type: "slurm_binary_push"
type SlurmBinaryPushPayload struct {
    UpgradeOpID    string `json:"upgrade_op_id"`
    Version        string `json:"version"`        // e.g. "24.05.3"
    ArtifactURL    string `json:"artifact_url"`   // internal HTTP endpoint on clustr server
    ArtifactSHA256 string `json:"artifact_sha256"`
}
```

The node fetches the tarball via HTTP from the clustr server (not pushed inline
over WebSocket — binaries are too large for that). The server exposes a
time-limited signed URL (HMAC, 10-minute TTL) that the clientd uses for the
download. This keeps the WebSocket message small and avoids memory pressure from
streaming a 20 MB binary through the hub.

**New node-side ack: `slurm_binary_ack`:**

```go
// node→server type: "slurm_binary_ack"
type SlurmBinaryAckPayload struct {
    UpgradeOpID string `json:"upgrade_op_id"`
    OK          bool   `json:"ok"`
    Error       string `json:"error,omitempty"`
    NewVersion  string `json:"new_version"` // reported by slurmd -V after restart
}
```

#### Rollback

Rollback is available at any point during or after an upgrade:

```
POST /api/v1/slurm/upgrade/{op_id}/rollback
Body: { target_nodes: ["node01", ...] }   -- optional: subset rollback
```

Rollback re-runs the upgrade sequence in reverse: compute nodes first, then
controller, then DBD. The previous version's artifact must still be present in
the store. If it has been purged, rollback is unavailable and the error response
says so explicitly.

The rollback plan mirrors the upgrade plan but reverses the node ordering. It
follows the same drain → push artifact (previous version) → restart → resume
sequence. The `slurm_builds` delete endpoint enforces a guard: an artifact that
is either `active_version` or `prev_version` in `slurm_module_config` cannot be
deleted.

#### Routes

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/slurm/upgrade/validate` | Dry-run validation: returns upgrade plan, warnings, running job count |
| `POST` | `/api/v1/slurm/upgrade` | Initiate rolling upgrade (requires confirmed_db_backup: true if DBD is managed) |
| `GET` | `/api/v1/slurm/upgrade/{op_id}` | Upgrade operation status: phase, per-node progress, current batch |
| `POST` | `/api/v1/slurm/upgrade/{op_id}/pause` | Pause between batches (completes current batch cleanly) |
| `POST` | `/api/v1/slurm/upgrade/{op_id}/resume` | Resume a paused upgrade |
| `POST` | `/api/v1/slurm/upgrade/{op_id}/rollback` | Rollback to previous version |
| `GET` | `/api/v1/slurm/upgrade/history` | List past upgrade operations |

#### UI

The upgrade UI is a step-by-step wizard accessible from the Slurm settings page.

Step 1 — Select version: dropdown of completed builds; shows current active
version vs selection; highlights version delta warning if applicable.

Step 2 — Pre-upgrade checklist: running job count, connected nodes, DB backup
confirmation checkbox, batch size input, drain timeout input.

Step 3 — Upgrade plan review: phase layout table (DBD → controller → batches),
estimated duration based on batch size and drain timeout.

Step 4 — Live progress: per-phase status, per-batch node table with drain status,
artifact push status, service restart status. Pause/Resume/Rollback buttons
accessible at all times. Auto-refreshes every 5 seconds.

---

### 8.19 Dependency Management (`internal/slurm/deps.go`)

#### Overview

Slurm's build dependencies are non-trivial and version-coupled. The dependency
manager tracks which dependency versions have been built, stores their artifacts,
and enforces the compatibility matrix between dependency versions and Slurm
versions. This is a subsystem within the build pipeline — `builder.go` calls
into `deps.go` to resolve and build dependencies before building Slurm itself.

#### Managed Dependencies

| Dependency | Why build from source | Notes |
|---|---|---|
| **munge** | Distro package acceptable for basic use; build from source when non-standard auth is needed | munge.key must be identical on all nodes — key management is separate (see below) |
| **PMIx** | Critical: PMIx API version must match MPI library (OpenMPI, MPICH). Distro PMIx is always outdated. | Version matrix is strict: PMIx 5.x for OpenMPI 5.x, PMIx 4.x for OpenMPI 4.x |
| **hwloc** | Build if system version < 2.2 (required for CPU binding in Slurm 23.11+) | Detect system version first; only build if needed |
| **UCX** | Build if `--with-ucx` requested and system UCX is absent or wrong version | Only required for InfiniBand / RoCE clusters |
| **libibverbs / rdma-core** | Distro package is acceptable; UCX build links against it | Do not build from source in v1 |
| **numactl** | System package acceptable | Listed for completeness; not built from source |
| **lua** | System package acceptable (Slurm uses Lua for job submit plugin scripting) | Not built from source in v1 |
| **json-c** | System package acceptable | Not built from source in v1 |
| **http-parser** | System package acceptable | Not built from source in v1 |
| **libyaml** | System package acceptable | Not built from source in v1 |
| **libjwt** | Build from source if system package absent (common on Rocky 9) | Simple build; few transitive deps |
| **pam-devel** | System package; installed as a build dep | Not built from source |
| **readline-devel** | System package; installed as a build dep | Not built from source |
| **dbus** | System package acceptable | Not built from source |

**Build-from-source priority:** munge, PMIx, hwloc (conditional), UCX
(conditional), libjwt (conditional). All others use system packages installed
via the distro package manager in the build environment. The build environment
is an isolated container (Podman or Docker, based on what is available on the
clustr server host) to avoid contaminating the host system.

#### Dependency Version Matrix

The compatibility matrix is stored in `slurm_dep_matrix` (see DB schema below).
Each row declares: for a given Slurm version range, which PMIx version range,
hwloc version range, and UCX version range are compatible.

The matrix is seeded at server startup from a bundled JSON file
(`internal/slurm/deps_matrix.json`, committed to the repo). This file is
maintained by the clustr maintainers and updated with each Slurm release. The
admin can also override specific compatibility entries via the API for site-local
constraints.

Example matrix entries:
```
slurm >= 24.05, < 25.00 → PMIx >= 4.2, < 6.0 | hwloc >= 2.2 | UCX >= 1.14
slurm >= 23.11, < 24.05 → PMIx >= 4.1, < 5.0 | hwloc >= 2.2 | UCX >= 1.12
slurm >= 23.02, < 23.11 → PMIx >= 4.0, < 4.3 | hwloc >= 2.2 | UCX >= 1.10
```

When a build is requested, `deps.go` consults the matrix, resolves the exact
dependency versions to build, and surfaces any ambiguity to the admin before
starting the build.

#### Munge Key Management

Munge requires that `/etc/munge/munge.key` be **identical** on every node in
the cluster. This is a correctness requirement: a node with a different key
cannot authenticate with slurmctld.

clustr manages the munge key as a cluster-level secret:

```
munge.key lifecycle:
    [first Enable of Slurm module, or explicit key rotation]
    |
    Server: generate 1024-byte random key (crypto/rand)
    Store encrypted: slurm_secrets table, key_type="munge_key",
                     value=AES-256-GCM(key, CLUSTR_SECRET_KEY)
    |
    [during finalization]
    writeSlurmConfig() decrypts munge.key, writes to rootfs at
    /etc/munge/munge.key (mode 0400, owner munge:munge)
    enables munge.service in rootfs chroot
    |
    [after key rotation, for live nodes]
    Manager.RotateMungeKey(): generates new key, updates slurm_secrets,
    creates a targeted push to all connected nodes:
        Server sends config_push{target: "munge_key", content: <new_key>}
        Node writes /etc/munge/munge.key, restarts munge
        Ack received
    Note: all nodes must receive the new key before any slurmd restarts.
    The push is synchronous (waits for all acks) before returning.
```

**Routes for munge key management:**

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/slurm/munge-key/rotate` | Generate and distribute new munge key to all connected nodes |
| `GET` | `/api/v1/slurm/munge-key/status` | Last rotation timestamp, node distribution status |

The munge key is never returned in plaintext via any API endpoint. Only the
last-rotated timestamp and per-node distribution status are exposed.

#### Artifact Storage for Dependencies

Dependencies are stored in the same `CLUSTR_DATA_DIR/slurm-builds/` tree:

```
CLUSTR_DATA_DIR/
  slurm-builds/
    deps/
      pmix/
        5.0.3/x86_64/pmix-5.0.3.tar.gz
      hwloc/
        2.10.0/x86_64/hwloc-2.10.0.tar.gz
      ucx/
        1.16.0/x86_64/ucx-1.16.0.tar.gz
```

Dependency artifacts are referenced from `slurm_builds` rows via
`slurm_build_deps`. A dependency artifact built for one Slurm version can be
reused for another if the version matrix allows it. The build pipeline checks
whether a matching dep artifact already exists before rebuilding.

---

### 8.20 Custom Scripts Management (`internal/slurm/scripts.go`)

#### Overview

Slurm exposes a set of hook scripts that are invoked at specific points in the
job lifecycle. These scripts run on compute nodes (prolog/epilog) or on the
controller (PrologSlurmctld/EpilogSlurmctld) and are a critical operational
surface: they handle GPU reset, environment setup, cgroup cleanup, health checks,
and custom site policy enforcement.

clustr manages these scripts with the same versioning model as config files
(`slurm_config_files`), but in a separate table (`slurm_scripts`) because they
have distinct metadata (executable bit, target role, script type) and a
different delivery mechanism (pushed to nodes with `chmod +x` applied, not
subject to `scontrol reconfigure`).

#### Managed Script Types

| Script | `slurm.conf` directive | Runs on | Runs as | Trigger |
|---|---|---|---|---|
| `RebootProgram` | `RebootProgram=` | Compute node | root | slurmctld sends reboot request |
| `Prolog` | `Prolog=` | Compute node | root | Before each job step begins |
| `Epilog` | `Epilog=` | Compute node | root | After each job step ends |
| `PrologSlurmctld` | `PrologSlurmctld=` | Controller | SlurmUser | Before job is dispatched |
| `EpilogSlurmctld` | `EpilogSlurmctld=` | Controller | SlurmUser | After job completes/fails |
| `TaskProlog` | `TaskProlog=` | Compute node | job user | Before each task |
| `TaskEpilog` | `TaskEpilog=` | Compute node | job user | After each task |
| `SrunProlog` | `SrunProlog=` | Compute node | job user | Before srun step |
| `SrunEpilog` | `SrunEpilog=` | Compute node | job user | After srun step |
| `HealthCheckProgram` | `HealthCheckProgram=` | Compute node | SlurmdUser | Periodic (HealthCheckInterval) |

The destination path for each script is a site-local choice. clustr stores the
path alongside the script content and writes that path into `slurm.conf` when
the corresponding directive is enabled.

#### DB Schema for Scripts

```sql
-- slurm_scripts: versioned script storage. Same immutable-version model as slurm_config_files.
CREATE TABLE IF NOT EXISTS slurm_scripts (
    id           TEXT PRIMARY KEY,           -- UUID
    script_type  TEXT NOT NULL,              -- "Prolog", "Epilog", "HealthCheckProgram", etc.
    version      INTEGER NOT NULL,           -- monotonically increasing per script_type
    content      TEXT NOT NULL,              -- script content (text)
    dest_path    TEXT NOT NULL,              -- e.g. "/etc/slurm/prolog.sh"
    checksum     TEXT NOT NULL,              -- sha256 of content
    authored_by  TEXT,
    message      TEXT,
    created_at   INTEGER NOT NULL,
    UNIQUE(script_type, version)
);

-- slurm_script_state: per-node per-script deployment tracking (mirrors slurm_node_config_state).
CREATE TABLE IF NOT EXISTS slurm_script_state (
    node_id          TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    script_type      TEXT NOT NULL,
    deployed_version INTEGER NOT NULL,
    content_hash     TEXT NOT NULL,
    deployed_at      INTEGER NOT NULL,
    push_op_id       TEXT,
    PRIMARY KEY (node_id, script_type)
);

-- slurm_script_config: which scripts are enabled and their dest_path.
-- One row per script_type. Presence of a row means the script is enabled.
CREATE TABLE IF NOT EXISTS slurm_script_config (
    script_type  TEXT PRIMARY KEY,
    dest_path    TEXT NOT NULL,              -- absolute path on nodes
    enabled      INTEGER NOT NULL DEFAULT 1,
    updated_at   INTEGER NOT NULL
);
```

#### Version Model

Scripts follow the same immutable versioning model as config files. Each save
operation creates a new row in `slurm_scripts`. The current version for a given
`script_type` is `MAX(version)`. History is fully preserved. The "Restore"
operation creates a new version with the old content.

This is intentional: a misbehaving Prolog script that gets deployed to 200 nodes
needs an auditable rollback path. The version history shows exactly which version
was deployed to which node and when.

#### Delivery Mechanism

Scripts are pushed via a new message type `slurm_script_push`, delivered through
the same `ClientdHub` transport as `slurm_config_push`. Scripts are **not**
bundled with config file pushes — they are pushed separately because they have
different role targeting (see 8.21) and because a script update that does not
require a `slurm.conf` change should not force a full config push.

```go
// ServerMessage type: "slurm_script_push"
type SlurmScriptPushPayload struct {
    PushOpID    string              `json:"push_op_id"`
    Scripts     []SlurmScriptFile  `json:"scripts"`
}

type SlurmScriptFile struct {
    ScriptType string `json:"script_type"`  // e.g. "Prolog"
    DestPath   string `json:"dest_path"`    // e.g. "/etc/slurm/prolog.sh"
    Content    string `json:"content"`
    Checksum   string `json:"checksum"`
    Version    int    `json:"version"`
}

// node→server type: "slurm_script_ack"
type SlurmScriptAckPayload struct {
    PushOpID      string              `json:"push_op_id"`
    ScriptResults []SlurmScriptResult `json:"script_results"`
}

type SlurmScriptResult struct {
    ScriptType string `json:"script_type"`
    OK         bool   `json:"ok"`
    Error      string `json:"error,omitempty"`
}
```

**Node-side apply (`slurmapply.go` extension):**

```
clientd receives slurm_script_push
    for each script in payload:
        validate: checksum matches content
        validate: dest_path is in /etc/slurm/ namespace
        write atomically (tmp + os.Rename)
        chmod +x dest_path
        verify: file is executable (os.Stat Mode check)
    send slurm_script_ack with per-script results
```

Scripts do **not** trigger a `scontrol reconfigure`. The path in `slurm.conf`
does not change when the script content changes — only when the `dest_path`
changes. A content update to an existing script at the same path takes effect
immediately for the next job that runs. A `dest_path` change requires a
`slurm.conf` update and push, which the UI warns about.

#### Validation Before Push

Server-side validation in `validate.go` (extended):

1. **Shebang check:** Content must begin with `#!`. Any script lacking a shebang
   is rejected with a clear error. This catches the common mistake of accidentally
   saving an empty or partial file.
2. **Size limit:** < 512 KB per script.
3. **No binary content:** Content must be valid UTF-8.
4. **Syntax check (best-effort):** If the shebang references `bash` or `sh`, the
   server can run `bash -n` against the content in a temporary file. Optional in
   v1 (requires bash on the server host). Python and other interpreters: skip.
5. **Path safety:** `dest_path` must be absolute, within `/etc/slurm/`, and must
   not contain `..` components.

#### UI: Script Editor

The Slurm nav section gains a "Scripts" tab alongside the config file tabs.

**Script list view:** Table of all managed script types. For each type:
- Enabled/disabled toggle (whether the directive appears in `slurm.conf`)
- Current version number and last-modified timestamp
- Destination path (editable inline)
- "Edit" button → opens the script editor

**Script editor:**
- `<textarea>` with monospace font and line numbers (v1: basic textarea, no
  heavy editor dependency)
- Current version displayed read-only above editor; editor loads current version
  content
- `dest_path` field (read-only if script is currently deployed; editable when
  creating a new script or explicitly overriding)
- Version history sidebar: timestamp, author, message, "Restore" button
- "Save" → creates new immutable version row (`slurm_scripts`)
- "Push to nodes" → opens push panel pre-filtered to this script type

**Push panel for scripts:**
- Node role filter: only nodes with the relevant role are shown (e.g., Prolog
  is only pushed to compute nodes — see 8.21)
- Per-node current deployed version vs version about to be pushed
- Offline nodes flagged grey
- Progress table same pattern as config push (Section 8.14)

#### Routes

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/slurm/scripts` | List all script types with current version info |
| `GET` | `/api/v1/slurm/scripts/{script_type}` | Current script content and metadata |
| `PUT` | `/api/v1/slurm/scripts/{script_type}` | Save new version |
| `GET` | `/api/v1/slurm/scripts/{script_type}/history` | Version history |
| `POST` | `/api/v1/slurm/scripts/{script_type}/push` | Push script to role-appropriate nodes |
| `GET` | `/api/v1/slurm/scripts/{script_type}/sync-status` | Per-node deployed version vs current |

---

### 8.21 Node Role Designations (`internal/slurm/roles.go`)

#### Overview

Slurm nodes have distinct roles that determine which daemons run, which config
files are relevant, and which scripts are delivered. Without role awareness,
clustr would either push all configs and scripts to all nodes (incorrect and
potentially harmful — `slurmdbd.conf` on a compute node is a security leak) or
require the admin to manually scope every push.

Role information is stored as a `slurm_role` field on each node. It is set in
the UI, optionally auto-detected on node registration, and consumed by
finalization, config push, script push, and the upgrade workflow.

#### Role Definitions

| Role | Constant | Daemon(s) | Config files received | Scripts received |
|---|---|---|---|---|
| `controller` | `SlurmRoleController` | `slurmctld` | `slurm.conf`, `slurmdbd.conf` (if DBD co-located), `cgroup.conf`, `topology.conf`, `plugstack.conf` | `PrologSlurmctld`, `EpilogSlurmctld` |
| `compute` | `SlurmRoleCompute` | `slurmd` | `slurm.conf`, `gres.conf`, `cgroup.conf`, `plugstack.conf` | `Prolog`, `Epilog`, `TaskProlog`, `TaskEpilog`, `SrunProlog`, `SrunEpilog`, `HealthCheckProgram`, `RebootProgram` |
| `login` | `SlurmRoleLogin` | none | `slurm.conf` only | none |
| `dbd` | `SlurmRoleDBD` | `slurmdbd` | `slurmdbd.conf`, `slurm.conf` | none |
| `none` | `SlurmRoleNone` | none | none | none (node excluded from all Slurm operations) |

A node can hold **multiple roles** simultaneously. The most common case is a
combined controller+DBD node (`["controller", "dbd"]`) in smaller clusters.
Roles are stored as a JSON array in `slurm_node_roles.roles`.

**Role-to-file mapping is enforced at push time.** The push orchestrator in
`push.go` (and `scripts.go`) queries each target node's roles and filters the
file/script list accordingly. A `slurmdbd.conf` push that targets "all nodes"
is silently scoped to only `controller` and `dbd` nodes. The push operation's
`node_results` records the scoping decision (field `role_filtered: true` on
nodes that were excluded).

#### DB Schema for Roles

```sql
-- slurm_node_roles: per-node role assignment.
-- One row per node. roles is a JSON array.
CREATE TABLE IF NOT EXISTS slurm_node_roles (
    node_id     TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    roles       TEXT NOT NULL DEFAULT '[]',   -- JSON array: ["controller","compute","dbd","login","none"]
    auto_detect INTEGER NOT NULL DEFAULT 0,   -- 1 if role was auto-detected on registration
    updated_at  INTEGER NOT NULL
);
```

`slurm_node_config_state` gains a `slurm_version` column to track the installed
Slurm binary version alongside the config version:

```sql
ALTER TABLE slurm_node_config_state ADD COLUMN slurm_version TEXT;
-- e.g. "24.05.3" — set during binary push ack, cleared on node removal
```

#### Role Assignment

**Manual assignment (primary path):**

Roles are set in the UI per node. The node detail page gains a "Slurm Role"
section with checkboxes for each role. Saving calls
`PUT /api/v1/nodes/{node_id}/slurm/role`.

**Auto-detection on node registration:**

When a node completes `verify-boot` and a `slurm_hardware` payload is present,
the server checks whether `slurmd` or `slurmctld` is running on the node (via
the `services` field in `HeartbeatPayload`). If detected, the server writes a
tentative role assignment using `INSERT OR IGNORE` — it does not overwrite
manually set roles.

Detection heuristics (from heartbeat `services` field):
- `slurmctld` active → add `controller`
- `slurmd` active → add `compute`
- `slurmdbd` active → add `dbd`

These are tentative: the admin reviews and confirms. Auto-detected roles are
flagged with `auto_detect=1` in the DB and shown with an "auto-detected" badge
in the UI.

#### Role-Aware Finalization

`writeSlurmConfig()` in `finalize.go` uses the node's assigned roles to filter
which configs and services to write:

```
finalize node with role ["compute"]:
    write: slurm.conf, gres.conf, cgroup.conf, plugstack.conf
    skip: slurmdbd.conf (role does not include "controller" or "dbd")
    enable: slurmd.service
    skip: slurmctld.service, slurmdbd.service

finalize node with role ["controller", "dbd"]:
    write: slurm.conf, cgroup.conf, topology.conf, plugstack.conf, slurmdbd.conf
    skip: gres.conf (no GPU resources on controller in typical HPC layout)
    enable: slurmctld.service, slurmdbd.service
    skip: slurmd.service

finalize node with role ["login"]:
    write: slurm.conf only
    skip: all daemon-specific configs
    skip: all services (no Slurm daemons on login nodes)
    install: Slurm client tools (srun, sbatch, squeue, scancel, sinfo)
             from the active slurm_builds artifact

finalize node with role ["none"] or no role set:
    skip: all Slurm operations
```

Role-aware finalization means a single `writeSlurmConfig()` call handles all
node types correctly without conditional branching in the caller.

#### Role-Aware Push Orchestration

The push orchestrator in `push.go` extends its per-node fan-out loop with a role
filter gate:

```
For each target node in push:
    roles = SlurmGetNodeRoles(nodeID)
    files_for_node = FilterFilesByRoles(all_requested_files, roles)
    scripts_for_node = FilterScriptsByRoles(all_requested_scripts, roles)

    if files_for_node is empty AND scripts_for_node is empty:
        record node_result{role_filtered: true, skipped: true}
        skip — do not send any message to this node
    else:
        send slurm_config_push{files: files_for_node}
        [wait for ack]
```

`FilterFilesByRoles` maps the requested file list against the role-to-file table
above. Example: if the push includes `["slurm.conf", "slurmdbd.conf"]` and the
target node is `compute`, only `slurm.conf` is sent to that node.

This filtering is transparent in the push operation result: the `node_results`
map shows `role_filtered: ["slurmdbd.conf"]` for nodes that were scoped down.

#### Routes

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/nodes/{node_id}/slurm/role` | Get current role assignment |
| `PUT` | `/api/v1/nodes/{node_id}/slurm/role` | Set role assignment (replaces existing) |
| `GET` | `/api/v1/slurm/nodes/by-role/{role}` | List all nodes with a given role |
| `GET` | `/api/v1/slurm/roles/summary` | Role distribution across the cluster (count per role) |

#### UI

**Node detail page → Slurm Role section:**
- Checkbox group: Controller, Compute, Login, DBD, None (mutually exclusive with
  None; the rest are combinable)
- Auto-detected roles shown with "(auto)" suffix; editable
- Unsaved changes warning if the node is currently live (role change affects
  next push and next reimage)
- "Save Role" → `PUT /api/v1/nodes/{node_id}/slurm/role`

**Cluster overview → Slurm panel:**
- Role distribution summary: "42 compute / 1 controller+dbd / 4 login / 1 none"
- Nodes with no role assigned shown as "unassigned" in a warning row; link to
  assign roles

**Push panel enhancement:**
- When pushing config files or scripts, the node table shows each node's role
  and which files/scripts that node will actually receive (post-filtering)
- Nodes that would receive nothing due to role filtering are greyed out with
  "(role excluded)" annotation
