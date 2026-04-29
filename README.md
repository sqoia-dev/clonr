# clustr

Node cloning and image management for HPC bare-metal clusters.

Register nodes, manage base images and bundles, and reimage machines from a single binary — no external dependencies.

## Live UI

`http://10.99.0.1:8080/` — served from the dev host (`cloner`, 192.168.1.151)

## Install

clustr targets RHEL-family distros (Rocky Linux, RHEL, AlmaLinux). Each EL major
version has its own signed yum/dnf repository.

### Rocky / RHEL / AlmaLinux 9

```sh
sudo rpm --import https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr
sudo dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/el9/clustr.repo
sudo dnf install clustr-serverd
```

### Rocky / RHEL / AlmaLinux 8

```sh
sudo rpm --import https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr
sudo dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/el8/clustr.repo
sudo dnf install clustr-serverd
```

### Rocky / RHEL / AlmaLinux 10

```sh
sudo rpm --import https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr
sudo dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/el10/clustr.repo
sudo dnf install clustr-serverd
```

### CLI only

```sh
# Add the repo for your EL version first (see above), then:
sudo dnf install clustr
```

## Quick start

After installing the package:

```bash
# 1. Configure the server
#    Set CLUSTR_PXE_INTERFACE and CLUSTR_PXE_SERVER_IP for your provisioning network,
#    then set CLUSTR_PXE_ENABLED=true.
sudo vi /etc/clustr/clustr-serverd.conf

# 2. Create a persistent session secret
openssl rand -hex 64 | sed 's/^/CLUSTR_SESSION_SECRET=/' \
  | sudo tee /etc/clustr/secrets.env > /dev/null
sudo chmod 0400 /etc/clustr/secrets.env

# 3. Enable and start the service
sudo systemctl enable --now clustr-serverd

# 4. Create the admin user (run once, on the server host)
sudo clustr-serverd bootstrap-admin
#   Default credentials: clustr / clustr
#   You will be prompted to change the password on first login.
#
#   Override the defaults with flags:
#     sudo clustr-serverd bootstrap-admin --username ops --password "S3cr3t!"

# 5. Open the UI and sign in
open http://localhost:8080/
```

## Build from source

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
