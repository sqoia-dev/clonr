# clustr

Node cloning and image management for HPC bare-metal clusters.

Register nodes, manage base images and bundles, and reimage machines from a single binary — no external dependencies.

## Live UI

`http://10.99.0.1:8080/` — served from the dev host (`cloner`, 192.168.1.151)

## Quick start

```bash
# Start the server
CLUSTR_SECRET_KEY=<secret> clustr-serverd

# Create an admin API key
CLUSTR_SECRET_KEY=<secret> clustr-serverd apikey create --scope admin --description "local"

# Open the UI, paste the API key at the login screen
open http://localhost:8080/
```

## Build

Requirements: Go 1.25+, Node 24+, pnpm 10+

```bash
# Build everything (web assets + Go binaries)
make all

# Web assets only
make web

# Go binaries only (requires internal/server/web/dist/ to exist)
make server
```

Binaries land in `bin/`:

| Binary | Description |
|--------|-------------|
| `bin/clustr` | Static CLI (CGO_ENABLED=0, linux/amd64) |
| `bin/clustr-serverd` | Server with embedded web UI |

## Docker

```bash
docker build -t clustr-server .
docker run -p 8080:8080 \
  -v /var/lib/clustr:/var/lib/clustr \
  -e CLUSTR_SECRET_KEY=<secret> \
  clustr-server
```
