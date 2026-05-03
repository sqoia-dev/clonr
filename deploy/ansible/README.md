# clustr Ansible Role

Idempotent bare-metal installation of `clustr-serverd` on Rocky Linux 9 or Ubuntu 22.04.

This is the secondary install path (Docker Compose is primary per Decision D7). Use this role for:
- Production HPC environments where DHCP/TFTP must run in the host network namespace without Docker
- Environments where container runtimes are not available or not permitted
- Large fleets where configuration management is already driven by Ansible

## Role structure

```
deploy/ansible/
├── README.md                  # this file
├── site.yml                   # example top-level playbook
└── roles/
    └── clustr/
        ├── defaults/
        │   └── main.yml       # all variables with defaults
        ├── handlers/
        │   └── main.yml       # service restart handlers
        ├── tasks/
        │   └── main.yml       # installation tasks
        ├── templates/
        │   ├── clustr.env.j2  # clustr.env EnvironmentFile
        │   └── clustr-serverd.service.j2  # systemd unit
        └── vars/
            └── main.yml       # OS-family conditional vars
```

## Requirements

- Ansible >= 2.14
- Target: Rocky Linux 9 / RHEL 9 / AlmaLinux 9, or Ubuntu 22.04 / 24.04
- Target host must be reachable via SSH with become (sudo/root)
- The clustr-serverd binary must be available via GitHub Releases or a local mirror

## Quick usage

```bash
# Install Ansible if needed
pip install ansible

# Run the site playbook against your provisioning host
ansible-playbook -i inventory.ini deploy/ansible/site.yml

# Dry-run (check mode — no changes made)
ansible-playbook -i inventory.ini deploy/ansible/site.yml --check

# Run only firewall tasks
ansible-playbook -i inventory.ini deploy/ansible/site.yml --tags firewall

# Re-run safely — the role is fully idempotent
ansible-playbook -i inventory.ini deploy/ansible/site.yml
```

## Variables

All variables are in `roles/clustr/defaults/main.yml` with documented defaults. The most important ones to override:

| Variable | Default | Description |
|---|---|---|
| `clustr_version` | `latest` | Release tag to install (e.g. `v1.0.0`). `latest` queries the GitHub API. |
| `clustr_listen_addr` | `10.99.0.1:8080` | Bind address for the HTTP API. |
| `clustr_pxe_interface` | `eth1` | Provisioning interface for DHCP/TFTP. |
| `clustr_pxe_server_ip` | `10.99.0.1` | IP advertised as `next-server` in DHCP offers. |
| `clustr_pxe_range` | `10.99.0.100-10.99.0.200` | DHCP pool. |
| `clustr_image_dir` | `/var/lib/clustr/images` | Image blob storage. |
| `clustr_db_path` | `/var/lib/clustr/db/clustr.db` | SQLite database path. |
| `clustr_secret_key` | `""` | **Required.** AES-256-GCM key (hex). Generate: `openssl rand -hex 32`. |
| `clustr_session_secret` | `""` | **Required.** Session HMAC key (hex). Generate: `openssl rand -hex 64`. |
| `clustr_firewall_ports` | `[8080/tcp, 67/udp, 69/udp]` | Ports to open on the provisioning interface. |

Override in your inventory or group_vars:

```yaml
# group_vars/provisioning_hosts.yml
clustr_version: "v1.0.0"
clustr_pxe_interface: "ens6"
clustr_pxe_server_ip: "192.168.100.1"
clustr_listen_addr: "192.168.100.1:8080"
clustr_secret_key: "{{ vault_clustr_secret_key }}"
clustr_session_secret: "{{ vault_clustr_session_secret }}"
```

Store secrets in Ansible Vault — never in plaintext.

## Idempotency

Re-running the playbook against a live server is safe:
- Binary installation: checks the installed version against `clustr_version`; skips download if already current.
- Directory creation: `state: directory` is idempotent.
- secrets.env: written only if content has changed; service is NOT restarted unless the file actually changes.
- systemd unit: `daemon_reload` + `restart` triggered only by handlers, which only fire when tasks report `changed`.
- Firewall rules: `firewalld`/`ufw` modules are idempotent by design.

If a reimage is in progress when the playbook runs, the service is NOT restarted. The playbook checks the readiness endpoint before triggering any restart handler:

```yaml
# tasks/main.yml — service restart guard
- name: Check for active reimages before restart
  uri:
    url: "http://{{ clustr_listen_addr }}/api/v1/reimages?status=running"
    headers:
      Authorization: "Bearer {{ clustr_admin_key | default('') }}"
  register: active_reimages
  failed_when: (active_reimages.json.total | default(0)) > 0
  when: clustr_admin_key is defined
```

## Tags

| Tag | What it runs |
|---|---|
| `install` | Download binary, verify checksum |
| `config` | Write clustr.env and secrets.env |
| `systemd` | Install unit file, enable service |
| `firewall` | Open ports via firewalld/ufw |
| `all` | Everything (default) |
