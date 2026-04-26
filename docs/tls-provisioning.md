# clustr TLS Provisioning Guide

This document covers TLS termination for the clustr API and web UI.

---

## Contents

1. [When TLS is required](#1-when-tls-is-required)
2. [Recommended: Caddy as TLS terminator](#2-recommended-caddy-as-tls-terminator)
3. [Configure initramfs for HTTPS](#3-configure-initramfs-for-https)
4. [Air-gapped and physically-isolated environments](#4-air-gapped-and-physically-isolated-environments)
5. [Alternatives to Caddy](#5-alternatives-to-caddy)

---

## 1. When TLS is required

**TLS is strongly recommended for any clustr instance reachable beyond a single physically-isolated provisioning network.** The clustr API transmits BMC credentials, SSH private keys, and LDAP bind passwords. Without TLS, these are sent in cleartext to any observer on the path between the operator's browser and the server.

| Scenario | TLS required? |
|---|---|
| Operator accesses the web UI from a workstation on a different network | **Yes** |
| Provisioning server has a management interface reachable from the corporate LAN | **Yes** |
| clustr is installed on a cloud VM (Linode, AWS, etc.) | **Yes** |
| Provisioning server is accessible only from a dedicated HPC management VLAN with no external routing | Strongly recommended, but physically-isolated HTTP is acceptable (see §4) |
| Lab / homelab on a fully air-gapped provisioning-only network | Acceptable without TLS (see §4) |

**Never expose port 8080 to the internet without TLS.**

---

## 2. Recommended: Caddy as TLS terminator

[Caddy](https://caddyserver.com) is the recommended TLS front-end for clustr. It handles certificate provisioning, renewal, and HTTP→HTTPS redirect automatically using Let's Encrypt or ZeroSSL (no manual cert management required).

### 2.1 Install Caddy

**Rocky Linux 9:**
```bash
dnf install -y 'dnf-command(copr)'
dnf copr enable -y @caddy/caddy
dnf install -y caddy
systemctl enable --now caddy
```

**Ubuntu 22.04:**
```bash
apt install -y debian-keyring debian-archive-keyring apt-transport-https
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
  | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
  | tee /etc/apt/sources.list.d/caddy-stable.list
apt update && apt install -y caddy
systemctl enable --now caddy
```

### 2.2 Caddyfile

Create `/etc/caddy/Caddyfile`. Replace `clustr.hpc.example.com` with your actual hostname:

```caddyfile
# clustr provisioning server — TLS termination
# Caddy provisions and renews the TLS certificate automatically via ACME (Let's Encrypt).
# Requires: outbound HTTPS to Let's Encrypt on the management interface.

clustr.hpc.example.com {
    # Reverse-proxy all requests to clustr-serverd listening on the local provisioning IP.
    # The management API lives at :8080; Caddy terminates TLS and forwards plain HTTP.
    reverse_proxy 10.99.0.1:8080

    # Enable structured access logging (optional — useful for audit correlation).
    log {
        output file /var/log/caddy/clustr-access.log {
            roll_size 50mb
            roll_keep 5
        }
        format json
    }

    # Header hardening — applied to all responses from the clustr API and web UI.
    header {
        # Prevent browsers from sniffing content type.
        X-Content-Type-Options "nosniff"
        # Disallow clustr being embedded in iframes (clickjacking protection).
        X-Frame-Options "DENY"
        # Strict-Transport-Security: enforce HTTPS for 1 year.
        # Add includeSubDomains only if all subdomains of this host also use TLS.
        Strict-Transport-Security "max-age=31536000"
        # Remove the Caddy version header from responses.
        -Server
    }
}
```

### 2.3 Apply the configuration

```bash
# Validate syntax
caddy validate --config /etc/caddy/Caddyfile

# Reload
systemctl reload caddy

# Or restart for the first time
systemctl restart caddy

# Verify Caddy obtained a certificate
caddy list-certificates
```

### 2.4 Tell clustr to set Secure cookies

Once TLS is active, set `CLUSTR_SESSION_SECURE=1` so the browser only sends session cookies over HTTPS:

**Bare-metal (`/etc/clustr/clustr.env`):**
```bash
sed -i 's/^CLUSTR_SESSION_SECURE=.*/CLUSTR_SESSION_SECURE=1/' /etc/clustr/clustr.env
systemctl restart clustr-serverd
```

**Docker Compose (`/etc/clustr/clustr.env`):**
```bash
sed -i 's/^CLUSTR_SESSION_SECURE=.*/CLUSTR_SESSION_SECURE=1/' /etc/clustr/clustr.env
cd /etc/clustr && docker compose up -d
```

### 2.5 Firewall

Caddy binds to port 443 (HTTPS) and 80 (HTTP redirect). Open those on the management interface:

```bash
# Rocky Linux 9 / firewalld
firewall-cmd --permanent --add-service=https --add-service=http
firewall-cmd --reload

# Ubuntu / ufw
ufw allow 80/tcp
ufw allow 443/tcp
```

Port 8080 should remain blocked on the management interface — Caddy is the only entry point for external traffic:

```bash
# Rocky Linux / firewalld — ensure 8080 is NOT in the public zone
firewall-cmd --permanent --remove-port=8080/tcp --zone=public 2>/dev/null || true
firewall-cmd --reload

# Ubuntu / ufw — deny 8080 from external sources
ufw deny 8080/tcp
```

### 2.6 Internal PKI (no public DNS)

If the clustr server is not reachable from the internet, Caddy cannot use ACME HTTP-01 or TLS-ALPN-01 challenges. Options:

**Option A: DNS-01 challenge** — Caddy supports DNS-01 via provider plugins (Cloudflare, Route53, etc.). This allows automatic cert provisioning without inbound HTTP access. See [Caddy DNS challenge docs](https://caddyserver.com/docs/automatic-https#dns-challenge).

**Option B: Internal CA** — Use your organisation's CA to sign a certificate, then configure Caddy to serve it directly:

```caddyfile
clustr.hpc.example.com {
    tls /etc/caddy/certs/clustr.crt /etc/caddy/certs/clustr.key
    reverse_proxy 10.99.0.1:8080
}
```

**Option C: mkcert for development** — `mkcert` creates a local CA trusted by the dev machine's browser. Not suitable for production.

---

## 3. Configure initramfs for HTTPS

When TLS is enabled, nodes booting via PXE must communicate with the clustr server over HTTPS — not HTTP. The `clustr` binary baked into the initramfs reads `CLUSTR_SERVER` from the kernel command line or environment at boot time.

### 3.1 Set CLUSTR_SERVER in the iPXE boot script

The iPXE script served by clustr passes kernel cmdline parameters to the initramfs. Edit or ensure your boot script includes the HTTPS URL:

In the iPXE boot script (served from `CLUSTR_BOOT_DIR/boot.ipxe`):
```ipxe
#!ipxe
kernel http://10.99.0.1/vmlinuz initrd=initramfs-clustr.img \
    CLUSTR_SERVER=https://clustr.hpc.example.com \
    CLUSTR_INSECURE=false \
    quiet
initrd http://10.99.0.1/initramfs-clustr.img
boot
```

`CLUSTR_SERVER` tells the `clustr deploy` process where to register and upload logs. If using an internal CA (Option B above), also pass `CLUSTR_TLS_CA_CERT` pointing to the CA PEM or set `CLUSTR_INSECURE=true` (only acceptable if the provisioning network is fully isolated and no credentials flow over the wire).

### 3.2 Inject the CA certificate into the initramfs (internal CA only)

If the clustr server uses a certificate signed by a private CA, the initramfs must trust that CA. Add the CA PEM to `CLUSTR_BOOT_DIR/` and reference it:

```bash
# Copy your CA cert into the boot dir
cp /path/to/internal-ca.crt /var/lib/clustr/boot/internal-ca.crt

# In boot.ipxe, pass:
#   CLUSTR_TLS_CA_CERT=/run/clustr/internal-ca.crt
# The initramfs init script will download and trust this cert before running `clustr deploy`.
```

Alternatively, rebuild the initramfs with the CA certificate pre-baked using a custom `scripts/build-initramfs.sh` hook that installs the cert into the initramfs root CA store.

### 3.3 Verify

From a node that has booted into the initramfs, verify that the server is reachable over HTTPS:

```bash
wget -qO- https://clustr.hpc.example.com/api/v1/health
# Expected: {"status":"ok", ...}
```

---

## 4. Air-gapped and physically-isolated environments

Unencrypted HTTP on a **physically-isolated provisioning network** is acceptable under these conditions:

- The provisioning network is a dedicated L2 segment with no routing to the corporate LAN, management VLAN, or internet.
- Physical access to the provisioning switch is restricted (locked rack or equivalent).
- The clustr management interface (web UI access port) is NOT on the provisioning network — it is on a separate, access-controlled management network.
- No BMC/IPMI traffic flows over the same segment as the HTTP API.

In this configuration, data-at-rest encryption (AES-256-GCM via `CLUSTR_SECRET_KEY`) still protects credentials stored in SQLite. Network interception of the provisioning traffic (initramfs download, log uploads) exposes only OS images and deploy logs — not credentials — because credentials are fetched via the management API, not from the initramfs network path.

**Even in a physically-isolated environment, TLS is strongly recommended for the management interface (web UI / API) if that interface is on a shared network.**

---

## 5. Alternatives to Caddy

### nginx

```nginx
# /etc/nginx/conf.d/clustr.conf
server {
    listen 443 ssl;
    server_name clustr.hpc.example.com;

    ssl_certificate     /etc/ssl/clustr/fullchain.pem;
    ssl_certificate_key /etc/ssl/clustr/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_ciphers         HIGH:!aNULL:!MD5;

    location / {
        proxy_pass         http://10.99.0.1:8080;
        proxy_set_header   Host $host;
        proxy_set_header   X-Real-IP $remote_addr;
        proxy_set_header   X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;
        # WebSocket support — needed for SSE log streaming
        proxy_http_version 1.1;
        proxy_set_header   Upgrade $http_upgrade;
        proxy_set_header   Connection "upgrade";
        proxy_read_timeout 3600s;
    }
}

server {
    listen 80;
    server_name clustr.hpc.example.com;
    return 301 https://$host$request_uri;
}
```

nginx does not auto-provision certificates. Use Certbot:
```bash
certbot --nginx -d clustr.hpc.example.com
```

### Traefik

Traefik with Docker Compose — add a `traefik` service and labels to the `clustr` service. Not covered here; see the [Traefik docs](https://doc.traefik.io/traefik/).

### HAProxy

HAProxy can terminate TLS but requires manual certificate management. Caddy or nginx are simpler choices for most operators.

---

## See Also

- [docs/install.md](install.md) — Installation guide (includes firewall rules)
- [docs/upgrade.md](upgrade.md) — Upgrade procedure
- [deploy/docker-compose/docker-compose.yml](../deploy/docker-compose/docker-compose.yml) — Docker Compose package
