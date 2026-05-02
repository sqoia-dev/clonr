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

`clustr-serverd` pulls in `memtest86+` automatically (ships in EL8 BaseOS and
EL9/EL10 AppStream — no EPEL required).

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

Without this binary, BIOS profile features report "intel-syscfg not configured"
and Intel BIOS push is unavailable. All other clustr features work without it.

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
