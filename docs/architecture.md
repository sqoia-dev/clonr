# clustr Architecture

This document describes the high-level architecture of clustr — a single Go binary that provisions bare-metal HPC nodes, manages Slurm, handles governance, and serves a web UI.

For detailed design decisions, see [docs/decisions.md](decisions.md).
For the Slurm build pipeline, see [docs/slurm-build-pipeline.md](slurm-build-pipeline.md).
For the clientd agent, see [docs/architecture/clustr-clientd.md](architecture/clustr-clientd.md).

---

## Component Overview

```
┌─────────────────────────────────────────────────────┐
│                   clustr-serverd                    │
│                                                     │
│  ┌──────────┐  ┌──────────┐  ┌────────────────┐   │
│  │  Image   │  │   PXE    │  │  Governance    │   │
│  │ Factory  │  │ DHCP/TFTP│  │  (NodeGroups,  │   │
│  │          │  │  iPXE    │  │  PI, Director) │   │
│  └────┬─────┘  └────┬─────┘  └───────┬────────┘   │
│       │             │                │             │
│  ┌────┴─────────────┴────────────────┴────────┐   │
│  │           SQLite  (single file DB)          │   │
│  └─────────────────────────────────────────────┘   │
│                                                     │
│  ┌──────────┐  ┌──────────┐  ┌────────────────┐   │
│  │  Slurm   │  │   LDAP   │  │  Web UI        │   │
│  │  Module  │  │  Module  │  │  (embedded,    │   │
│  │ (bundled │  │          │  │   no build)    │   │
│  │  RPMs)   │  │          │  │                │   │
│  └──────────┘  └──────────┘  └────────────────┘   │
└─────────────────────────────────────────────────────┘
         ▲                              ▲
         │ API / WebSocket              │ HTTP
    ┌────┴────┐                   ┌─────┴──────┐
    │  clustr │                   │  Browser   │
    │  (CLI)  │                   │  (any)     │
    │  PXE    │                   │            │
    │  nodes  │                   │            │
    └─────────┘                   └────────────┘
```

---

## Two binaries

| Binary | Role |
|---|---|
| `clustr-serverd` | Management server: HTTP API, web UI, PXE/DHCP/TFTP, image factory, Slurm module, LDAP module, governance layer |
| `clustr` | CLI + deploy agent: runs on operator workstations, and embedded in the PXE initramfs on target nodes during deployment |

---

## Key design decisions

| Decision | Rationale |
|---|---|
| **Pure-Go SQLite** (`modernc.org/sqlite`) | Keeps both binaries buildable with `CGO_ENABLED=0`. Required for static initramfs embedding. |
| **Single binary, no external DB** | Operators deploy one file. No Postgres to manage in v1.x. Scale trigger for PostgreSQL migration: documented in [docs/decisions.md](decisions.md) D27. |
| **Embedded web UI** | Static assets compiled into the server binary via Go `embed`. No separate build step or asset server needed. |
| **BaseImage vs NodeConfig split** | One image blob serves N nodes. Per-node identity (hostname, IPs, SSH keys) is never baked into blobs — applied at deploy time only. |
| **chi router** | Composes cleanly with standard `net/http` middleware. |
| **No external auth system** | Single pre-shared API token for machines; session cookies for humans. HPC clusters are typically air-gapped and operator-administered. OIDC is v2.0+, gated on a named customer requiring it. |
| **Deployment engines** | Two backends: `FilesystemDeployer` (tar archive extraction with sgdisk + mkfs) and `BlockDeployer` (raw block image streamed directly to disk, no temp file). |
| **Centralized log broker** | In-process log broker fans out SSE streams to connected CLI and web UI clients. Logs persisted to SQLite for historical queries. |

---

## Package layout

```
pkg/
  api/        Shared request/response types (REST contract)
  client/     HTTP client for CLI → server

internal/
  config/     ServerConfig and ClientConfig (env + flag resolution)
  db/         SQLite database layer + migrations
  server/     HTTP server + handlers + middleware
  server/ui/  Embedded web UI (Go embed, dark theme, Alpine.js + HTMX)
  image/      Image factory (pull, import ISO, capture, shell sessions, scrubbing)
  pxe/        Built-in DHCP/TFTP/PXE server with iPXE chainloading
  deploy/     Deployment engines: rsync, block, efiboot, finalize
  hardware/   Hardware discovery: CPU, memory, disks, NICs, DMI, InfiniBand
  chroot/     Chroot session lifecycle (mount/unmount proc/sys/dev)
  ipmi/       IPMI/BMC management via ipmitool
  slurm/      Slurm module: config, munge key, GPG-verified bundle install
  ldap/       LDAP module: account lifecycle, group management, self-service
  reimage/    Reimage orchestrator: deploy scheduling, concurrency limits
  secrets/    AES-256-GCM credential encryption at rest
  metrics/    Prometheus metrics collector
  webhook/    Outbound webhook dispatcher
  notifications/ SMTP email notifications
  allocation/ Auto-allocation policy engine (PI onboarding)
  sysaccounts/ Local sysaccount management for non-LDAP installs

cmd/
  clustr-serverd/  Management server entrypoint + subcommands (doctor, bundle, apikey, ...)
  clustr/          CLI entrypoint + subcommands (deploy, image, node, ipmi, logs, ...)
  clustr-clientd/  Resident agent on deployed nodes (heartbeat, verify-boot)
```

---

## Data flow: node deployment

```
Operator         clustr-serverd             clustr (in initramfs)
   |                    |                          |
   |--POST /nodes-----→ |  (register node)         |
   |                    |                          |
   |--POST /reimage---→ |  (schedule deploy)       |
   |                    |                          |
   |                    |←--PXE boot DHCP req------| (node power-cycles)
   |                    |--TFTP: iPXE chainload---→|
   |                    |--DHCP: IP + next-server-→|
   |                    |                          |
   |                    |←--GET /boot (initramfs)--| (clustr deploy --auto)
   |                    |←--POST /register---------| (self-register)
   |                    |←--GET /node-config-------| (fetch image assignment)
   |                    |←--image blob stream------| (pull + verify SHA256)
   |                    |                          | (write to disk)
   |                    |←--POST /logs (streaming)-| (progress logs)
   |                    |←--POST /verify-boot------| (clustr-clientd, after reboot)
   |                    |                          |
   |←--SSE log stream---| (web UI / CLI tail)      |
```

---

## Web portals

| Route | Persona | Auth |
|---|---|---|
| `/` → `/admin/` | Sysadmin, Operator, Readonly | Session cookie (username + password) |
| `/portal/` | Researcher (viewer role) | Session cookie |
| `/portal/pi/` | PI (pi role) | Session cookie |
| `/portal/director/` | IT Director (director role) | Session cookie |
| `/api/v1/` | CLI, automation | Bearer token (API key) |
