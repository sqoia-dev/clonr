# Getting-Started Audit — 2026-04

**Date:** 2026-04-27
**Scope:** v1.7.0 (`549aad0`), Sprint I pre-work — first-run experience audit
**Persona:** HN reader, sysadmin background (Linux, systemd, basic networking), no prior clustr exposure. Has read the HN post title. Is skeptical. Has 30 minutes before they give up.
**Auditor:** Jared (Chief of Staff / Ops)
**Method:** Cold read of all public-facing docs, live verification against the running cloner dev host (192.168.1.151, clustr-serverd v1.7.0), cross-reference README vs reality.

---

## 1. README Assessment

### What the README is today

The README is 1,355 lines in its current form. It opens with a two-sentence product description (good), then immediately drops into a 5-step Quick Start that mixes two distinct install paths (Docker Compose and bare-metal) without clearly labeling which is which. After the Quick Start it presents an 8-step "2-Node Slurm Cluster" walkthrough. Then a 5-item "Full Workflow Example." Then 13+ sections of reference documentation. The README is trying to be a product pitch, an install guide, a CLI reference, and a troubleshooting guide all at once. That is a four-way collision that serves no reader well.

### P0 Issues (bounce within the first 30 seconds)

**P0-1 — The README has no hero pitch above the fold (line 1-8)**
Lines 1-8 are two sentences of product description followed immediately by a horizontal rule and a section called "Quick Start." There is nothing that answers "why would I use this instead of Warewulf, xCAT, or just Ansible?" A stranger on HN has a short attention span and a high skepticism filter. They need a 3-line pitch — what it is, who it is for, and what the key differentiator is — before they are willing to invest in reading install steps. The current opening is accurate but not compelling. A reader who already knows what clustr is will survive it. The stranger who just clicked from HN will not.
Monica is addressing this in I6 (README hero rewrite). This is the hardest P0 to fix via text alone — it needs the GIF demo and "why clustr" bullets that I6 is producing.

**P0-2 — Duplicate numbered lists in Quick Start (lines 15-88 vs 90-147)**
The Quick Start section has two separate numbered lists. The first runs 1-4 and covers server setup. The second restarts at 2 and covers post-install usage (pull image, customize, register node, deploy). This is not a typo — it is two conceptually separate workflows mashed into one section without a dividing header. A stranger following step 4 ("Verify the server is healthy") will continue to the next numbered item, which is step 2 ("Pull an image") — and if they are being careful they will notice the number reset and stop. If they are skimming they will be confused when they try to pull an image before having a node registered or even a network configured. This is an immediate bounce risk for careful readers.
Fix: split into two sections with explicit headers: "Server Setup (5 min)" and "First Use: pull, customize, deploy (after server is running)."

**P0-3 — `deploy/docker-compose/.env.example` does not exist (line 35-38)**
The Quick Start step 2 instructs the user to `curl -fsSL` a file at `deploy/docker-compose/.env.example` from the raw GitHub URL. That file does not exist in the repo. `ls deploy/docker-compose/` shows only `docker-compose.yml`. A user who follows this step verbatim will get a 404 from the raw.githubusercontent.com URL, and the `curl -fsSL` command will silently write an empty or error file to `/etc/clustr/clustr.env`. The next step will then fail with a confusing config-not-found or parse error. This is the single highest-probability early failure mode in the entire install path, and it occurs at step 2 of the Quick Start.

### P1 Issues (bounce within the first 5 minutes)

**P1-1 — `docs/architecture.md` is linked but does not exist (line 1279)**
The Architecture Overview section ends with "See [docs/architecture.md](docs/architecture.md) for the full design doc." That file does not exist. The `docs/` directory contains no `architecture.md`. It does contain an `architecture/` subdirectory (a directory, not a file). A reader who clicks this link on GitHub gets a 404. This is a trust signal — a broken link in the architecture section of a self-hosted infrastructure tool tells the reader "this project is not polished." On HN, that is a comment waiting to happen.

**P1-2 — Management network access is buried and contradicts the Quick Start (lines 66-88)**
The Quick Start step 4 says to verify the server via `curl -s http://10.99.0.1:8080/...`. Then a large block explains that `clustr-serverd` binds to the provisioning interface only and that to reach the web UI from an operator workstation, you need Caddy configured with a management IP. This is not a "note" — it is a prerequisite that must be completed before the web UI is usable. But it is presented as an afterthought after the verification step, and it references `scripts/setup/install-dev-vm.sh` without telling the reader what that script does or that they need to run it. A Docker Compose user who has never used clustr will set up the server, try to open `http://10.99.0.1:8080` in their browser, and get "refused to connect" because 10.99.0.1 is on the provisioning interface, not reachable from their laptop. Zero user-facing error; just silence.

**P1-3 — The Quick Start assumes a two-NIC server without stating it (lines 13-88)**
The entire Quick Start assumes the user has a dedicated provisioning network interface. The prerequisites section (`docs/install.md §1`) documents this but the README does not mention it until halfway through the management IP discussion on line 27 of the install.md reference. A reader with a single-NIC VM (very common for "try this out" scenarios) will get stuck at network setup. The README should state in the first paragraph of Quick Start: "You need two network interfaces: one for management/admin access, one for the provisioning network (nodes PXE boot on this)."

**P1-4 — The `.env.example` curl is the only place where PXE interface is configured in Quick Start**
Given that P0-3 makes that step fail, the user has no guidance on how to set `CLUSTR_PXE_INTERFACE` to their actual provisioning interface. The variable exists in the install.md env reference table, but the Quick Start section that was supposed to introduce it is broken.

**P1-5 — "Run `install-dev-vm.sh` interactively" appears unexpectedly (lines 70-88)**
The Quick Start suddenly references `scripts/setup/install-dev-vm.sh` mid-paragraph as the mechanism for setting up management IP and Caddy. The reader has not been told this script exists, what it does, or that they need it. The script is in the repo at `scripts/setup/install-dev-vm.sh`, but the README presents it as if you already know to use it. The inline call `bash scripts/setup/install-dev-vm.sh` appears with no surrounding context about what the script does. This is the bare-metal path leaking into the Docker Compose Quick Start.

### P2 Issues (friction that causes comment threads but not bounce)

**P2-1 — "Quick Start: 2-Node Slurm Cluster" time estimate is wrong for a stranger (line 152)**
The section header says "under 30 minutes on a provisioned server with decent internet." The ISO build alone takes 20-35 minutes (stated later on line 197). That is before node registration (steps 2-4), before deploying to two nodes (step 6, "5-15 min per node"), and before Slurm config (step 5). The actual elapsed time from "start the build" to `srun hostname` succeeding is realistically 90-120 minutes. The 30-minute claim will be the first HN comment from anyone who tried it.

**P2-2 — Step 4 register-node curl payload uses raw API, which requires knowing MACs upfront (lines 218-253)**
The walkthrough says "Register two nodes" using `curl -X POST /api/v1/nodes` with `<controller-mac>` and `<compute-mac>` as literal placeholders. There is no guidance on how to get the MAC addresses of VMs/nodes before they boot. A stranger setting up Proxmox VMs (the most likely test environment) would need to look this up in Proxmox. The web UI "Configure and Deploy" modal (B2-1 from Sprint B) presumably handles this better — but the README walkthrough goes straight to curl. This will cause confusion for lab users.

**P2-3 — The slurm.conf in Step 5 has a CPU/RAM placeholder that requires explanation (lines 338-342)**
The comment says `CPUs=2 RealMemory=3905`. The `RealMemory` value (3905 MB) is not obvious — it is not 4096 because Slurm reserves some for system use, but a new operator will not know this and will likely put `4096` and wonder why Slurm is unhappy. There should be a one-line comment: "RealMemory should be ~90% of actual RAM in MB, not exact — Slurm's system reservation is counted separately."

**P2-4 — Server Requirements section appears before the 2-Node walkthrough in the file, but after Quick Start (lines 487-617)**
The hardware requirements (CPU, RAM, Disk, KVM requirement) and the full package list appear late in the README, after both the Quick Start and the 2-Node walkthrough. A reader who discovers that their host needs `/dev/kvm` for ISO builds only after they have already started the install process will be annoyed. The requirements block should be the first section, before any install steps.

**P2-5 — Bootstrap API key capture instruction in Quick Start is insufficient (line 55)**
`docker compose logs -f clustr 2>&1 | grep -A2 "Bootstrap admin"` works, but it will return nothing if the container was already running before you ran the grep (logs have scrolled). The `-f` flag follows; without knowing to `Ctrl-C` after seeing the key, the user will sit waiting. The install.md §6 has a better version (`head -60`). The Quick Start should use the same pattern or note explicitly: "Run this on first start only — the key appears once and never again."

**P2-6 — The README does not mention the web UI login path at all in the Quick Start**
After step 3 (start the server) and step 4 (verify healthy), the next logical action for a new operator is "open a browser and log in." This step is entirely absent from the Quick Start. The credentials (`clustr` / `clustr`) are in install.md §6, but the README Quick Start never says "open your browser at `http://<mgmt-ip>` and sign in." This gap means a stranger who follows only the README will have a running server but no idea they have a web UI to use.

**P2-7 — `api/v1/nodes` endpoint in README Quick Start Step 4 registers nodes, but the actual table name in the DB is `node_configs` (cosmetic)**
The API surface is `/api/v1/nodes` (correct), but the internal table is `node_configs`. Not a user-facing issue, but it signals a potential naming inconsistency that could confuse readers of the architecture section if it ever ships.

---

## 2. Install Path Walkthrough

Walking `docs/install.md` cold, as if executing.

### Prerequisites (§1) — P1 friction

**IP-1 (P1) — "Other RHEL-compatible or Debian-compatible distros work" is unverified and unqualified**
The table says this with no caveats. The bare-metal install path uses NetworkManager-specific commands (`nmcli`) in the network setup section. On Ubuntu 22.04 with Netplan (the default), the `nmcli` commands do not apply. The doc does give both Ubuntu (Netplan) and Rocky (NetworkManager) variants for the static IP setup — but the prerequisites table does not warn that the rest of §2 has OS-specific splits. A reader skimming the table will assume the rest is generic.

**IP-2 (P1) — Docker on Rocky Linux 9 uses a different package**
The software dependencies table says `dnf install -y docker docker-compose-plugin`. On Rocky Linux 9, the official Docker CE package is not `docker` — it is `docker-ce`, from the Docker CE repo, which is not installed by default. `dnf install docker` on Rocky 9 will install `moby-engine` or fail depending on what repos are enabled. This is a very common trip-up for Rocky Linux users and will generate a bug report immediately.
Reference: https://docs.docker.com/engine/install/centos/

**IP-3 (P2) — `modprobe loop` at §1 will fail on most cloud VMs**
`modprobe loop` requires CAP_SYS_MODULE. On many cloud VMs and locked-down hypervisors, this command fails silently or with "Operation not permitted." The comment says "(loaded automatically if installed)" but the instruction to run it is still there. A stranger on a cloud VM will copy-paste it and get a confusing failure before they have even started the real install.

### Network Setup (§2) — P0 friction

**IP-4 (P0) — "You need two network interfaces" is stated as a requirement but never explained for someone who does not have them**
The entire section assumes a dual-NIC host. There is no guidance for what to do if you only have one NIC (e.g. a single-NIC home lab VM). The install guide should say explicitly in the first sentence: "If you only have one NIC, skip the provisioning interface sections and use loopback PXE mode (not yet documented; see issue #XXXX). Two NICs are required for production."
The absence of this warning means single-NIC users will follow all of §2, configure a non-existent `eth1`, and wonder why the provisioning interface assignment fails.

**IP-5 (P1) — The `.254` alias rationale is explained three times**
The "Why `.254` as a recommended alias" block appears in the README, in the Quick Start of install.md, and again in the main body of install.md §2. That is 800 words explaining a single network alias. The duplicate is not a quality issue per se — the documentation is correct — but for an HN reader who does not care about `.254` conventions and just wants the server running, this repetition signals "this is a complicated product" at the wrong moment.

**IP-6 (P2) — Firewall instructions use interface name `eth1` without qualifying**
The UFW commands use `ufw allow in on eth1`. On Rocky Linux 9, the interface might be `ens3`, `enp3s0`, or `bond0`. The doc uses `eth1` throughout with a parenthetical "(replace with your provisioning interface)" in some places but not all. An operator who does not notice the parenthetical will run the firewall command against the wrong interface.

### Path A — Docker Compose (§3) — P1 friction

**IP-7 (P1) — Section 3.3 creates a full `clustr.env` from a heredoc with hardcoded `eth1`**
The env file template on line 291 hardcodes `CLUSTR_PXE_INTERFACE=eth1`. This is the second most likely failure mode after P0-3 (.env.example missing). A user whose provisioning interface is `ens3` or `enp3s0` will not notice this placeholder in the template. The `#` comment above it says "(enable if nodes PXE boot via this host)" but does not say "CHANGE THIS TO YOUR ACTUAL INTERFACE NAME."

**IP-8 (P1) — Section 3.4 docker-compose.yml is manually inlined but also available via curl**
The operator is shown both a `curl` command to download the Compose file AND an alternative to write it manually via heredoc. These produce different results if the repo has been updated since the heredoc was written (the curl gets the current version; the heredoc is pinned to the version in the docs). There should be one path, clearly labeled as recommended.

**IP-9 (P2) — Docker Compose requires `network_mode: host` for DHCP/TFTP but this means TLS setup later is more complex**
The compose file uses `network_mode: host`, which means the container has direct access to all host interfaces. This is required for DHCP/TFTP to work but it breaks Docker's network isolation. A security-conscious reader who sees `network_mode: host` in a production infrastructure tool will ask questions. There is no note explaining why it is necessary or what the security implications are.

### Path B — Bare-Metal (§4) — P2 friction

**IP-10 (P2) — "The Ansible role is delivered in Sprint 6" (line 359)**
This is internal planning language leaked into the public docs. A stranger reading "delivered in Sprint 6" has no idea when Sprint 6 is or whether it already happened. This needs to be either a specific version reference ("available starting v2.0") or removed.

**IP-11 (P2) — Binary download uses `uname -m` for arch detection but the release filenames use `amd64`/`arm64`, not `x86_64`/`aarch64`**
The `upgrade.md` has the correct `sed` pattern (`uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/'`). The `install.md §4.1` bare-metal path uses `ARCH="$(uname -m)"` without the sed transform. This will produce a download URL like `clustr-serverd-linux-x86_64` when the actual release binary is `clustr-serverd-linux-amd64`. The download will 404.

**IP-12 (P1) — The bare-metal env var note (§4.4) is a wall of text explaining a confusing split**
The note at §4.4 explains that for bare-metal, config vars go in the systemd unit but secrets go in `/etc/clustr/secrets.env` via `EnvironmentFile=`. This split between two env sources is architecturally sound but confusing to document. The explanation is accurate but long. The key sentence is buried: "If you create `/etc/clustr/clustr.env` on a bare-metal install and set variables there, the server will ignore them unless you also add `EnvironmentFile=/etc/clustr/clustr.env` to the unit." This gotcha should be a warning box, not an inline paragraph.

### Bootstrap Admin (§6) — P1 friction

**IP-13 (P1) — The bootstrap API key capture UX is fragile**
`docker compose logs -f clustr 2>&1 | head -60` works only if the key appears in the first 60 lines of logs and if no prior log output has pushed it past line 60. On a system where the server had previous starts (e.g. the operator restarted after a config change), the bootstrap key will not appear again — it is a one-time event. The doc acknowledges this but the primary instruction does not make it clear enough that you have one chance and it should be the very first thing you do after `docker compose up -d`.
The proposed `clustr-serverd bootstrap-admin` subcommand from I1 would eliminate this entirely. Until then, the doc should show `docker compose logs clustr | grep -A5 "Bootstrap admin"` (without `-f`) and add an explicit "NOTE: this key appears ONCE and is never shown again. Do not proceed past this step without copying it."

**IP-14 (P2) — Password complexity rule is mentioned once and not at first login**
The forced-password-change flow in §6 says the password must be 8+ chars with uppercase, lowercase, and a digit. This rule is mentioned in the doc but the web UI password-change screen does not display the rule inline next to the input field. Users who try weak passwords will get an error but not know the specific requirement. (This is a UI bug, not a doc bug, but it is confirmed by live inspection of the login page HTML.)

---

## 3. "First Node" Flow

### What was Sprint B's contribution

Sprint B (B2-1) added a "Configure and Deploy" inline action on the Nodes list: when a node has no `base_image_id`, a primary CTA opens a 3-step modal. This was designed to replace the 7-step manual flow from the earlier ops review.

### What a stranger actually experiences

After installing, logging in, and surviving the password change, the stranger lands on the Dashboard. The Dashboard has:
- A cluster summary section (node count, image count, recent activity)
- A "Deployments" section
- An "Anomalies" card (Sprint B B2-4)

The empty-state Dashboard gives no guidance. The "Active Deployments" empty state has subtext and a CTA added in B2-7: "No deployments in progress. Trigger a reimage from the Nodes page." This is helpful, but the stranger has to find it.

The sidebar navigation shows: Dashboard, Images, Nodes, Deployments, DHCP Leases, and then a long list of module-specific sections (Slurm, LDAP, Network, System). For a new admin with zero nodes and zero images, the correct next step is ambiguous. Should they start with Images? Or Nodes? The system requires an image before a node can be deployed, but the sidebar gives them both as parallel options with no indication of order.

**FN-1 (P1) — No "what to do next" guidance on an empty dashboard**
A freshly-installed clustr with zero nodes and zero images shows a dashboard with all counters at zero. There is no "Getting Started" checklist, no ordered step-by-step ("Step 1: Build an image, Step 2: Register a node, Step 3: Deploy"), no first-run wizard. The B2-6 fix (first-deploy wizard gating) was shipped but it gates on `deploy_verified_booted_at` being null on all nodes — meaning the wizard only shows when no nodes have been deployed. The question is: does the wizard actually appear and does it guide the stranger through the right sequence? From live inspection of the HTML and JS, the first-deploy wizard path exists in the JS (visible in app.js via the B2-6 logic), but the wizard logic is driven by the images=0 AND nodes=0 check — not the simpler "has any node ever been deployed" check that was the intended gate. This is a UX gap: the wizard should show on a zero-images zero-nodes install, but the specific gate condition may not match.

**FN-2 (P1) — Nodes page "Configure and Deploy" shortcut requires the node to already be registered**
The B2-1 "Configure and Deploy" CTA appears on the Nodes list for nodes with no `base_image_id`. But to see a node on the Nodes list, the node must already have been registered (either via the API or by PXE-booting with `--auto`). A complete stranger does not know to do either of those things first. The shortcut reduces friction from step 4 onward, but steps 1-3 (build image, register node, configure PXE) still require API calls or a PXE boot.

**FN-3 (P0) — The first-node flow for a lab/VM user requires knowing the node's MAC address before it boots**
The README Quick Start walkthrough and the install.md smoke test both require the user to know the MAC address of their target node before registering it. For bare-metal servers this means looking it up in IPMI/BMC. For Proxmox VMs this means checking the Proxmox UI. There is no guidance in either document on how to discover this. The `clustr deploy --auto` PXE path eliminates this requirement (the node self-registers), but PXE boot requires the provisioning network to be fully configured — which is itself a prerequisite that requires knowing your NIC names and static IP.

**FN-4 (P2) — Proxmox-specific boot order is mentioned in the README (line 215) but not in install.md**
The README's 2-Node walkthrough mentions "Set the Proxmox/IPMI boot order to disk first, then network (scsi0;net0)." This is essential for PXE-based lab setups but it appears in the middle of a tutorial step without context, and it is not cross-referenced in the install guide. A new operator who does not read the README carefully will set up their Proxmox VMs with network-first boot (the natural default for "I want PXE boot"), hit the infinite-PXE-loop problem, and have no idea why.

---

## 4. "First Job" Flow

The path from "server installed" to "researcher submits a job" is not a single doc — it is scattered across README, install.md, user-management.md, and slurm-module.md. Walking it cold:

**Step A — Enable Slurm module (README Step 3 / slurm-module.md)**
The README and the 2-Node walkthrough both cover enabling the Slurm module via `POST /api/v1/slurm/enable`. This works. The bundled Slurm repo is a significant differentiator — no external repo needed — and it is documented. The `clustr-serverd bundle list` command confirms the repo is installed on the cloner host. Score: mostly clear.

**Step B — Register nodes with Slurm roles (README Step 4)**
Requires knowing node UUIDs from the previous registration step. The `roles` vs `role` gotcha is documented (README line 297: "The body field is `roles` (plural array), not `role` (singular string). Sending `{"role":"controller"}` silently sets an empty role list."). This is a genuine gotcha and the documentation is correct to call it out. Score: documented, but it is a footgun that could be eliminated by better API error handling.

**Step C — Update slurm.conf (README Step 5)**
Requires writing a full slurm.conf manually, wrapping it in JSON, and uploading via the API. The example is complete and accurate. However, the `slurm.conf` content is 32 lines of dense Slurm configuration that assumes familiarity with Slurm concepts (MpiDefault, ProctrackType, TaskPlugin, SelectType, etc.). A stranger who has never configured Slurm will not know what any of these mean. The raw editor approach (as discussed in Sprint Plan C3) is acceptable, but the "Quick Settings" structured form that was supposed to wrap it is not documented in the README or in slurm-module.md — which may mean it does not exist yet or is not discoverable.

**Step D — Reimage nodes (README Step 6)**
Requires PXE infrastructure to be working. If anything in the network setup was wrong, this step will silently hang or produce a timeout. The "Poll for verified_booted" watch command is adequate. Score: OK if PXE works.

**Step E — Submit a job (README Step 8)**
The smoke test runs as `root`. The README correctly notes: "This is sufficient to verify Slurm is working. Root exists on every node by definition." Then it defers to `docs/user-management.md` for real user provisioning.

**Step F — Provision real users (user-management.md)**
This is where "first job submitted" by a researcher breaks. The doc explains three approaches: local sysaccounts (lab), clustr LDAP module (production), external LDAP/NIS. The local sysaccounts path is the right choice for a new HN reader trying things out. But:

**FJ-1 (P1) — The sysaccounts API path requires multiple curl commands and a reimage**
Creating a user via sysaccounts requires: `POST /api/v1/sysaccounts/groups`, `POST /api/v1/sysaccounts/users`, then a full reimage of all nodes. There is no web UI path documented for this in user-management.md. The doc is correct but requires the operator to know and use the API. For an HN reader who wants to verify the system works before committing to it, this is a significant barrier.

**FJ-2 (P1) — No clear "first researcher job" end-to-end path exists in any single document**
A researcher wanting to submit their first Slurm job needs to: (1) have an account on every node with consistent UID/GID, (2) have SSH access to the controller, (3) know the partition name from slurm.conf, and (4) know the syntax for `srun` or `sbatch`. None of these are connected in a single "researcher's getting started" document. The Portal (Sprint C/C.5) handles LDAP-based account creation through the web UI, but the path from "PI creates a researcher account in the portal" to "researcher SSH's to the controller and runs srun" is not documented end-to-end anywhere.

**FJ-3 (P2) — "srun from where?" is unanswered**
A researcher cannot SSH into the controller unless they have credentials on that node and know the controller's IP. The install.md smoke test uses `ssh root@10.99.0.100` (hardcoded IP). A real researcher has neither root nor a node on `10.99.0.0/24`. The access path (SSH bastion? Jump host? VPN? OnDemand?) is entirely undocumented. This is partially because clustr is not an OnDemand replacement, but the gap between "nodes are provisioned" and "researchers can submit jobs" is not acknowledged anywhere.

---

## 5. The "Would I Bounce?" Map

Each row is a decision point. Bounce estimate = percentage of HN sysadmins with 30 minutes who would give up at this step.

| Step | What happens | Bounce estimate | Primary reason |
|---|---|---|---|
| Land on GitHub README | Read opening 2 sentences | 20% | No compelling pitch; no GIF; "HPC clusters" is niche |
| Read Quick Start step 1 | Directories and secrets | 5% | Straightforward; looks familiar |
| Read Quick Start step 2 | Download .env.example | 35% | File does not exist; curl 404; silent failure |
| Read step 3 | Start Docker Compose | 10% conditional | If .env.example issue is not noticed |
| Discover management IP section | Read Caddy/alias block | 15% | Unexpected complexity; "I thought this was Quick Start" |
| First login to web UI | Find the IP, open browser | 10% | Not documented in Quick Start at all |
| Dashboard — empty state | Zero guidance | 20% | "What do I do next?" has no answer |
| Build a base image | 20-35 min ISO build | 5% | Long but clearly documented |
| Register first node | MAC address required | 25% | "Where do I find the MAC?" has no answer in docs |
| Reimage first node | PXE required, boot order required | 20% | PXE config not validated; Proxmox boot order not clear |
| Verify Slurm works | `srun hostname` from root | 5% | Well-documented smoke test |
| Submit job as real user | sysaccounts + reimage required | 40% | Too many undocumented steps |

**Cumulative estimate:** If I model this as independent bounce probabilities, roughly 15-20% of motivated HN sysadmins reach a working single-node Slurm cluster on their first attempt. That is not good enough for Show HN. The target should be 50%+ reaching the `srun hostname` milestone within 30 minutes.

The biggest single lever: fix P0-3 (the missing .env.example). That step currently has a 35% bounce rate for a step that should be near-zero.

---

## 6. Top 10 Fixes Ordered by (Impact × Ease)

1. **Create `deploy/docker-compose/.env.example`** — P0-3. This is a one-file fix that unblocks the entire Quick Start for Docker Compose users. The content is already in install.md §3.3 as a heredoc. Copy it to the file with the two required changes (replace `eth1` with a comment, replace hardcoded IP with a comment). Effort: 15 minutes. Impact: eliminates the highest-probability early failure.

2. **Add `architecture.md` or redirect the broken link** — P1-1. Either create a stub `docs/architecture.md` that redirects to `docs/architecture/` (if that directory has content), or remove the broken link. Effort: 5 minutes. Impact: removes an obvious "this project is not polished" signal.

3. **Add "what to do next" guidance to the empty-state Dashboard** — FN-1. A three-step ordered checklist card ("1. Build an image, 2. Register a node, 3. Deploy") with links to the relevant pages. Effort: 1-2 hours (frontend). Impact: eliminates the "I'm in the UI, now what?" confusion that affects every new install.

4. **Fix `uname -m` → architecture mapping in install.md §4.1** — IP-11. The current code produces a 404 on binary download. One-line sed fix, same as upgrade.md already has. Effort: 2 minutes. Impact: bare-metal install path actually works.

5. **Split the README Quick Start into two clearly labeled sections** — P0-2. Rename the second numbered list from a continuation to a separate section: "After install: first operations." Effort: 5 minutes (pure doc edit). Impact: eliminates the confusing number-reset that trips up careful readers.

6. **Add the web UI login step to the README Quick Start** — P2-6. After step 3 (start server), add step 4: "Open `http://<your-clustr-mgmt-ip>` and sign in with username `clustr`, password `clustr`. You will be prompted to change the password immediately." Effort: 5 minutes. Impact: closes the gap where a new operator does not know they have a web UI.

7. **Fix Docker package name for Rocky Linux 9 in install.md §1** — IP-2. Change `dnf install -y docker` to `dnf install -y docker-ce` with a note that the Docker CE repo must be added first (link to official Docker docs). Effort: 10 minutes. Impact: prevents a very common Rocky Linux install failure.

8. **Make `CLUSTR_PXE_INTERFACE=eth1` a comment-only placeholder in 3.3 template** — IP-7. Change the line to `# CLUSTR_PXE_INTERFACE=eth1  # REQUIRED: replace with your actual provisioning interface name (e.g. ens3, enp3s0)`. Effort: 2 minutes. Impact: eliminates the silent wrong-interface failure.

9. **Remove the "delivered in Sprint 6" internal planning language from install.md** — IP-10. Replace with "manual steps below (Ansible role coming in a future release)." Effort: 2 minutes. Impact: removes a credibility-undermining internal reference from public docs.

10. **Add a one-line "What you need before starting" block at the top of the README Quick Start** — P1-3. Two sentences: "Prerequisites: a Linux server with two network interfaces (one for management, one for provisioning), Docker Compose installed, and `openssl` available." Effort: 5 minutes. Impact: filters out single-NIC users before they invest 20 minutes in a failing install.

---

## 7. The One Thing That Surprised Me

**The `clustr-serverd doctor` subcommand does not exist.**

The I1 spec calls for a `clustr-serverd doctor` subcommand that prints a green/red checklist of dnsmasq reachability, TFTP file presence, DB connectivity, image dir writable, secrets file present and locked-down. I tested this on the live cloner host and got:

```
Error: unknown command "doctor" for "clustr-serverd"
```

This subcommand is specified as a deliverable in the sprint we just started (I1). It does not exist yet. That is expected — it is on Dinesh's list. But what surprised me is that nothing approximating it exists either. The `clustr-serverd` binary currently has exactly three subcommands: `apikey`, `bundle`, and `completion`. There is no `version` command. There is no `status` command. There is no way to verify the server configuration from the CLI without starting the server and hitting the health endpoint.

The health endpoint (`/api/v1/healthz/ready`) is actually very good — it returns structured JSON with specific check names. But it requires the server to already be running, which means it cannot help a user who is failing to start the server. A pre-flight checker that runs before the server starts — checking that the required files exist, the listen address is accessible, the secrets are present and correctly formatted — would catch the most common failure modes (missing secrets.env, wrong interface name, missing boot dir) before they produce cryptic startup errors. This is the highest-value engineering gap I found that is not a documentation issue.

The good news: the I1 spec already identifies this. The bad news: it is not trivial to implement well, and it is the single thing that would most dramatically improve the "stranger tries clustr" experience. It should be the first item Dinesh ships in I1, not the last.

---

## Paper-Cut Count by Severity

| Severity | Count | Items |
|---|---|---|
| P0 (immediate bounce / broken path) | 3 | P0-1, P0-2, P0-3 |
| P1 (blocks progress within 5 minutes) | 12 | P1-1 through P1-5, IP-2, IP-4, IP-7, IP-11, IP-12, IP-13, FN-1, FN-2, FN-3, FJ-1, FJ-2 |
| P2 (friction, comment thread risk) | 13 | P2-1 through P2-7, IP-1, IP-3, IP-5, IP-6, IP-8, IP-9, IP-10, IP-14, FN-4, FJ-3 |
| **Total** | **28** | |

---

## Handoff Note to Dinesh (I1)

High-priority items from this audit that feed directly into I1 deliverables:

- FIX FIRST: create `deploy/docker-compose/.env.example` (P0-3). This is a doc change but it blocks the primary install path.
- FIX FIRST: fix `uname -m` arch mapping in install.md §4.1 (IP-11). Bare-metal binary download 404s today.
- Build `clustr-serverd doctor` — the most impactful new engineering deliverable. Pre-flight check before server start.
- Empty-state Dashboard guidance (FN-1) — 3-step onboarding card. Frontend change.
- Password complexity displayed inline on the set-password form (IP-14) — small frontend fix.
- Fix Docker package name for Rocky 9 (IP-2) — doc fix.

Items for Monica (I6 README rewrite):
- P0-1: hero pitch above the fold is the biggest single gap
- P0-2: split the Quick Start numbered lists
- P2-6: add web UI login step to Quick Start
- P1-3: add prerequisites block at top of Quick Start
- P2-4: move Server Requirements before install steps
