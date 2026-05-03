# clustr

Node cloning and image management for HPC bare-metal clusters.

Register nodes, manage base images and bundles, and reimage machines from a single binary — no external dependencies.

Latest release: **[v0.10.0](https://github.com/sqoia-dev/clustr/releases/latest)**

## Install

RPMs are published to `pkg.sqoia.dev` for EL8, EL9, and EL10. Raw binaries and
per-release SHA256 checksums are attached to every
[GitHub Release](https://github.com/sqoia-dev/clustr/releases).

clustr targets RHEL-family distros (Rocky Linux, RHEL, AlmaLinux). Each EL major
version has its own signed yum/dnf repository.

clustr requires EPEL for two of its dependencies (`dropbear` and `screen`).
Enable EPEL before installing.

### Rocky / RHEL / AlmaLinux 9

```sh
sudo dnf install -y epel-release
sudo rpm --import https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr
sudo dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/el9/clustr.repo
sudo dnf install clustr-serverd
```

### Rocky / RHEL / AlmaLinux 8

```sh
sudo dnf install -y epel-release
sudo rpm --import https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr
sudo dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/el8/clustr.repo
sudo dnf install clustr-serverd
```

### Rocky / RHEL / AlmaLinux 10

```sh
sudo dnf install -y epel-release
sudo rpm --import https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr
sudo dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/el10/clustr.repo
sudo dnf install clustr-serverd
```

`clustr-serverd` pulls in `memtest86+` automatically (ships in EL8 BaseOS and
EL9/EL10 AppStream — no EPEL required for memtest86+).

`dropbear` (initramfs SSH debug server) and `screen` (initramfs console fallback)
are pulled in automatically from EPEL.

udpcast (used internally for fleet-reimage multicast) is bundled in
`clustr-serverd`. No additional install steps required.

### CLI only

```sh
# Add the repo for your EL version first (see above), then:
sudo dnf install clustr
```

### Optional: Intel BIOS settings push

If you intend to use clustr's BIOS settings push feature on Intel-based nodes,
you must install Intel's `syscfg` utility manually. Intel's EULA prohibits
redistribution, so clustr cannot bundle it.

1. Download `syscfg` from Intel's website. Search for
   **"Intel Server Configuration Utility (SYSCFG)"** or
   **"Save and Restore System Configuration Utility"** at:
   https://www.intel.com/content/www/us/en/download/center/

2. Extract the archive and locate the `syscfg` binary for Linux.

3. Copy it to the clustr vendor directory:

   ```sh
   sudo mkdir -p /var/lib/clustr/vendor-bios/intel
   sudo cp syscfg /var/lib/clustr/vendor-bios/intel/syscfg
   sudo chmod 0755 /var/lib/clustr/vendor-bios/intel/syscfg
   ```

4. Restart the service:

   ```sh
   sudo systemctl restart clustr-serverd
   ```

Without this binary, BIOS profile features report "vendor binary not present"
and Intel BIOS push is unavailable. All other clustr features work without it.

### Optional: Dell BIOS settings push

If you intend to use clustr's BIOS settings push feature on Dell PowerEdge
nodes, you must install Dell's `racadm` utility manually. Dell's EULA prohibits
redistribution, so clustr cannot bundle it.

1. Download iDRAC Tools for Linux from Dell's support site.
   Search for **"iDRAC Tools for Linux"** at:
   https://www.dell.com/support/home/

2. Extract the archive and locate the `racadm` binary.

3. Copy it to the clustr vendor directory:

   ```sh
   sudo mkdir -p /var/lib/clustr/vendor-bios/dell
   sudo cp racadm /var/lib/clustr/vendor-bios/dell/racadm
   sudo chmod 0755 /var/lib/clustr/vendor-bios/dell/racadm
   ```

4. Restart the service:

   ```sh
   sudo systemctl restart clustr-serverd
   ```

Without this binary, BIOS profile features for Dell nodes report
"vendor binary not present" and Dell BIOS push is unavailable.
All other clustr features work without it.

### Optional: Supermicro BIOS settings push

If you intend to use clustr's BIOS settings push feature on Supermicro nodes,
you must install Supermicro's `sum` utility manually. Supermicro's EULA
prohibits redistribution, so clustr cannot bundle it. sum 2.x and 3.x are
supported (INI-like and XML config formats, respectively). sum 1.x is not
supported.

1. Download Supermicro Update Manager (sum) from Supermicro's support site.
   Navigate to **Solutions > Management Software > BMC / IPMI** at:
   https://www.supermicro.com/

2. Extract the archive and locate the `sum` binary for Linux.

3. Copy it to the clustr vendor directory:

   ```sh
   sudo mkdir -p /var/lib/clustr/vendor-bios/supermicro
   sudo cp sum /var/lib/clustr/vendor-bios/supermicro/sum
   sudo chmod 0755 /var/lib/clustr/vendor-bios/supermicro/sum
   ```

4. Restart the service:

   ```sh
   sudo systemctl restart clustr-serverd
   ```

Without this binary, BIOS profile features for Supermicro nodes report
"vendor binary not present" and Supermicro BIOS push is unavailable.
All other clustr features work without it.

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

# 6. Add your first node
#    Click "Add Node" in the top-bar → fill in hostname + MAC address → Register.
#    Or PXE-boot a node — it registers automatically.

# 7. Upload an OS image
#    Click "Add Image" → "From URL" → paste an image URL → Download.
#    Or drag-and-drop an ISO onto the "Upload ISO" tab for resumable TUS upload.

# 8. Build the PXE initramfs
#    Click Images → Bundles tab → "Build Initramfs" → watch live build log.
#    The resulting image is registered and ready to reimage nodes.

# 9. Configure LDAP and manage system accounts from the Identity tab
#    By default, clustr provisions its own LDAP server on first config. Switch
#    the LDAP mode to External to connect to an existing directory.
#    Use the Users and Groups sections to browse local and LDAP identities,
#    manage specialty groups, and add per-node sudo entries from the node detail Sheet.
```

See [docs/AUTH.md](docs/AUTH.md) for authentication details across web UI, CLI, and WebSocket surfaces.

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

### Build tags

The web bundle is embedded into `clustr-serverd` at compile time via `//go:embed`.
For production builds, pass `-tags webdist` (or run `make all` / `make server`):

```bash
make all   # builds everything with the live web bundle
```

For backend-only development and testing, the default build uses a stub web FS
that serves a placeholder `index.html` — no `dist/` directory required:

```bash
go test ./...           # default, stub embed
go vet ./...            # default, stub embed
make test               # same — stub embed, fast
```

To verify the production build path (web + Go tests together):

```bash
make web && make test-web
```

## Further reading

- [docs/AUTH.md](docs/AUTH.md) — authentication model across web UI, CLI, and WebSocket surfaces
- [CHANGELOG.md](CHANGELOG.md) — full release history
- [SPRINT.md](SPRINT.md) — architecture decisions and sprint history
