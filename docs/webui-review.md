# clustr Web UI Review — Operator Persona Analysis

**Review date:** 2026-04-25  
**Reviewer:** Gilfoyle (Infra/Platform/Security)  
**Code base:** `staging/clustr/internal/server/ui/static/` + `internal/server/handlers/`  
**Live instance:** `http://10.99.0.1:8080` (cloner dev host, 1 node / 1 image in DB)  
**App.js size:** 8,005 lines (no build step, no framework — vanilla JS SPA)

---

## Methodology

Full read of `index.html`, `app.js` (8 kLOC), `slurm.js` (2 kLOC), `ldap.js` (1.7 kLOC), `network.js` (900 LOC), `sysaccounts.js`, `logs.js`, all handler Go files (`nodes.go`, `images.go`, `reimage.go`, `ipmi.go`, `power.go`, `logs.go`). Live API probed with admin key across nodes, images, reimages, node groups, network, slurm, ldap, heartbeat, sensors, power, logs, and progress endpoints.

---

## Personas

### Persona A — HPC Sysadmin (200-node bare-metal cluster, research university)

Operates a production Slurm cluster. Works in deploy windows. Needs audit history, RBAC (students vs faculty), IPMI/BMC control, image lifecycle governance, Slurm/PBS integration, and failure alerting.

### Persona B — Lab Operator (20–50 nodes, AI/ML org, mixed VM + bare-metal)

Needs speed. Creates gold images, re-deploys nodes after failed jobs. GPU/CUDA-aware deploys, per-team node assignments, cost/usage visibility.

### Persona C — Embedded Test Bench Operator (5–15 nodes, hardware-in-the-loop testing)

Reflashes nodes per test run. Needs scriptable API (CI integration), headless operation, serial console output visibility, clean per-run state.

### Persona D — DevOps Hobbyist / Homelabber (3–10 Proxmox VMs + NUCs)

Needs documentation, low-friction onboarding, sensible defaults, auto-detection, intuitive UI. Single operator.

---

## Per-Persona Evaluations

### Persona A — HPC Sysadmin

**What works well today**

- Slurm module is the most complete feature in the UI. Config file editor with version history (`#/slurm/configs`), script management, upgrade orchestration, and sync status are all present in `slurm.js`. No other tool in this space ships Slurm lifecycle management in the provisioner.
- Reimage history per node in the node detail Overview tab shows status, phase, exit code, and error detail at `app.js:3340–3387`. A "Cancel" button exists on non-terminal reimage requests. This is usable audit history.
- Node group reimage is present: `app.js:6487–6568`. Groups can be bulk-reimaged with image selection and concurrency controls. This is the foundation of fleet operations.
- Power control (on/off/cycle/reset/PXE boot) on every node, backed by pluggable provider abstraction (`ipmi.go`, `power.go`). Proxmox and IPMI are both supported.
- Log stream on the dashboard and per-node Logs tab with SSE-based live follow. Component filter supports `deploy`, `hardware`, `chroot`, `ipmi`, `efiboot`, `network`, `rsync`, `raid` — covers the phases an HPC admin would care about during a deploy.

**What is missing entirely**

- No RBAC. There are two roles (`admin`, `node`) in the `users` table but no permission boundaries between them. There is no `read-only` role, no per-group permission scoping, no way to give a student account view-only access to their assigned node group. `app.js:7320` gates the Users settings tab on `admin` role, but the API has no finer-grained controls.
- No scheduled deploy windows. The `CreateReimageRequest` API accepts `scheduled_at` (`reimage.go:105–127`), and the DB stores it, but the UI reimage modal in `app.js` never surfaces the `scheduled_at` field. The feature exists at the API layer and is completely invisible in the UI.
- No alerting or notification system. There is no way to configure email, Slack, or webhook notifications for deploy failures, verify-boot timeouts, or node state changes. Operators must poll the dashboard or watch logs manually.
- No PBS/Torque integration path. Slurm is first-class; PBS is not mentioned anywhere.
- No audit log distinguishing who triggered what. The reimage history shows `requested_by: "api"` for all entries (`reimage.go:108`). There is no user identity attached to actions — no way to tell which admin triggered a reimage or changed a node config.
- No image promotion/retirement workflow. Images have statuses (`building`, `ready`, `archived`) but no concept of `testing`, `staging`, `production` lifecycle stages. There is no way to tag an image as "promoted to production" vs "under test."
- No node labeling beyond the comma-separated freeform `groups` field and the `NodeGroup` entity. No key-value labels with constraint-based image assignment.

**What is painful or broken**

- Nodes list has a group-based filter (detected at `app.js:2336`) but there is no search by hostname, IP, or MAC. On 200 nodes the only way to find a specific node is to scroll or click through groups.
- The nodes list sorts by group/role, then by whatever the API returns. Column headers in the nodes table are not clickable sort controls. There is no way to sort by last deploy status or last seen time from the list view.
- The Slurm nav section uses a `<details><summary>Slurm</summary>` element at `index.html:103`. On Firefox and some Chromium builds, the `details` open/close state does not persist across hash navigation because the router replaces `innerHTML`. An HPC admin navigating deep into Slurm config editing loses the expanded sidebar state on every page transition.
- The node group reimage modal (`app.js:6487`) has concurrency and failure threshold inputs but no dry-run option. The individual node reimage API supports `dry_run` (`reimage.go:101`), but the group reimage path never passes it through.
- Image delete requires knowing the image name to confirm — the modal shows node hostnames/MACs that reference the image, but on a 200-node cluster that list could be enormous with no scrollable container.
- The "Verify Timeout" state renders a hardcoded "Attach serial console to investigate" message at `app.js:3183`. There is no link to the console, no IPMI SOL button, no guidance on how to actually attach that serial console.
- Power status on the nodes list is polled per-node on hover/dropdown open, not pre-fetched. Opening the power dropdown for the first time on each node triggers a separate `/api/v1/nodes/{id}/power` call. On 200 nodes this causes a thundering-herd against the BMC network if an admin opens multiple dropdowns quickly.

**What needs to be removed**

- The `<details><summary>Slurm</summary>` and `<details><summary>LDAP</summary>` nav collapsibles at `index.html:103,169`. They add no information hierarchy value and break navigation state. Replace with flat section headers.
- "Server info will appear here in a future update" at `app.js:7336`. Either implement it or remove it entirely. Placeholder text in a production tool erodes trust.

**What should be automated**

- Initramfs rebuild should be triggered automatically when a new image is finalized, not require a manual button press. Current flow: finalize image → remember to rebuild initramfs → manually click Rebuild. Any deploy attempted with a stale initramfs before the rebuild will use the old kernel.
- Node discovery rediscover should auto-trigger on first registration, not require a manual "Re-discover Hardware" button click after a node has been seen.

**What needs more customization**

- Kernel args are a single text field per node. HPC environments commonly need per-node kernel args that inherit from a group default and then override — there is no inheritance model.
- Disk layout override exists per-node but there is no way to define a group-level disk layout template that nodes inherit from.
- The fstab mount editor is good but the NFS options field is freeform with no validation of mount option correctness (e.g., `_netdev` requirement for network mounts). There is a `_netdev` auto-suggest for network FS types (`app.js:2826`) but no enforcement.

**What should be more preset/cookie-cutter**

- Image creation: four entry points exist (Pull, Build from ISO, Capture from Host, Import ISO) at `app.js:918–935`. For a new HPC admin this is confusing without documentation of which to use when. A decision tree or recommended path ("Start here: Build from ISO") would reduce cognitive load.
- SSH key management: each node has its own SSH keys field. There is no cluster-wide SSH key that propagates to all nodes unless LDAP is configured. This means adding a new admin's SSH key to 200 nodes requires 200 edits or LDAP.

---

### Persona B — Lab Operator (AI/ML, 20–50 nodes)

**What works well today**

- Image roles (`built_for_roles`) exist in the API response (`"built_for_roles": ["compute"]`) and the image pull modal lets you select roles during ISO build. The role-mismatch warning in the node assignment modal (`app.js:2583`, `app.js:2714`) warns when assigning a non-matching image to a node. This is the right concept for GPU/CUDA-aware deploys.
- Hardware profile on the node detail Hardware tab shows CPUs, NICs, and other detected hardware. The detection runs at PXE registration time and is stored as JSON.
- Node groups with group-level image reimage is a solid base for team/project assignments.
- Per-node Mounts tab with NFS/Lustre/BeeGFS/CIFS presets (`app.js:2843`) maps directly to shared storage patterns common in ML labs.

**What is missing entirely**

- No GPU detection or display. The hardware profile captures CPUs, NICs, memory, and disk from the hardware survey. GPU inventory (PCIe device list, GPU model, VRAM, driver version) is not collected and not displayed. A ML lab operator cannot see from the UI which nodes have GPUs or which CUDA version the deployed image provides.
- No image metadata display in the image detail page. The API has `GET /api/v1/images/{id}/metadata` (`images.go:926`) that returns an `ImageMetadata` sidecar with build details. The UI never calls this endpoint — the metadata is collected but completely invisible. This sidecar likely contains CUDA version, kernel version, and installed packages from the build — exactly what an ML operator needs.
- No per-team node assignment model beyond node groups. There is no concept of "Team A owns nodes in group gpu-team-a and can only deploy images tagged for Team A." Ownership is implied only by group membership.
- No cost or usage reporting. No deploy duration tracking, no uptime stats, no per-image or per-node usage counters.
- No image version comparison or diff. When a new image version is built from the same ISO, there is no way to see what changed between versions from the UI.

**What is painful or broken**

- Re-deploy after a failed ML job requires three steps: go to node detail → Overview → click "Request Reimage." For a lab operator who does this 10 times a day for different nodes, this is slow. There is no "re-deploy last image" action from the nodes list without entering the node detail page.
- The image card grid (`app.js:947`) shows name, status, OS, arch, format, size, and relative creation time. It does not show the image version, the roles it was built for, or a "nodes using this image" count. A lab operator managing 5 image variants cannot tell them apart at a glance.
- Image list is unordered beyond creation date. There is no sort-by-name, sort-by-status, or filter-by-OS on the Images page.
- The role-mismatch warning is non-blocking. An operator can assign a CPU-only image to a GPU node and proceed. The warning appears at `app.js:2583` but there is no confirmation gate for role mismatches.

**What needs to be removed**

- None specific to this persona.

**What should be automated**

- After a failed deploy (node status transitions to "Failed"), the UI should offer an immediate "Retry" action in the node list row, not require navigating into the detail page.
- When a new image version is finalized, nodes that are running an older version of the same image name could be highlighted automatically as "update available."

**What needs more customization**

- Image tags are stored (`"tags": []` in the API response) but there is no UI to add, remove, or filter by tags. The tags field exists in the DB and API but is dead surface area in the UI.
- Node custom variables (`custom_vars` in the node config) are editable in the Configuration tab but there is no templating for common ML patterns (e.g., `CUDA_VERSION`, `NCCL_VERSION`).

**What should be more preset/cookie-cutter**

- GPU node disk layout: a common pattern for ML nodes is a large `/scratch` for job data and a smaller root. The disk layout editor lacks a preset for "GPU compute node" that pre-populates a sensible partition scheme.

---

### Persona C — Embedded Test Bench Operator (5–15 nodes, HIL testing)

**What works well today**

- The API is fully bearer-token authenticated and machine-readable. The `POST /api/v1/nodes/{id}/reimage` endpoint is scriptable from CI (`reimage.go:40`). The `force=true` flag exists for unblocking stuck states (`reimage.go:67`).
- Deploy phase reporting via SSE (`GET /api/v1/progress/stream`) provides real-time granularity: `downloading`, `extracting`, `partitioning`, `formatting`, `finalizing`, `complete`, `error`. The progress stream includes `bytes_done`, `bytes_total`, `speed_bps`, `eta_seconds`, and a `message` field. This is CI-friendly.
- `POST /api/v1/nodes/{id}/deploy-complete` and `POST /api/v1/nodes/{id}/deploy-failed` are well-defined state transition endpoints with structured failure payloads (`phase`, `exit_code`, `exit_name`) at `nodes.go:581–691`.
- Verify-boot architecture (ADR-0008): the two-phase deploy confirmation (`deploy_completed_preboot_at` + `deploy_verified_booted_at`) gives CI a programmatic signal that the OS is actually running, not just that the image was written.
- The `dry_run` flag on reimages is present in the API and the single-node reimage modal in the node detail page exposes it.

**What is missing entirely**

- No webhook or event callback. After a deploy completes or fails, the only way for CI to know the outcome is to poll `GET /api/v1/nodes/{id}` or subscribe to the SSE progress stream. There is no `POST {callback_url}` on reimage completion.
- No API endpoint to check whether a specific node is currently in the middle of a deploy before submitting a new reimage. `GET /api/v1/progress` returns all active deploys but does not exist as a documented stable API (the UI makes a call to `/api/v1/progress` but the endpoint returned 404 in live testing — this may be a routing issue or the endpoint is served under a different path).
- No terminal/console output capture for non-interactive serial console access. The deploy logs exist in the logs table and stream via SSE. But the deploy agent's raw terminal output (what you would see on a physical console during PXE boot) is not captured in a form that a headless CI system can retrieve as a single artifact.
- No image integrity pre-check before deployment. The `X-Clustr-Blob-SHA256` header is set during blob serving (`images.go:531,576`) and the deploy agent verifies it. But the UI offers no way to verify an image's integrity before deploying it, and a failed integrity check produces a deploy failure rather than a pre-flight warning.

**What is painful or broken**

- The `GET /api/v1/progress` endpoint returned HTTP 404 in live testing against the running instance. If this is the correct path for checking active deploys, it is unreachable. The dashboard JS makes a call at `app.js:462` to `API.progress.list()` — the endpoint routing may be `GET /api/v1/deploy/progress` or similar. This needs to be verified and documented.
- Reimage cancel via the API (`DELETE /api/v1/reimage/{id}`) requires knowing the reimage request ID. There is no `DELETE /api/v1/nodes/{id}/reimage/active` endpoint to cancel whatever reimage is currently in flight for a node, which is the natural CI operation.
- Node registration and auto-provisioning require the node to PXE boot. For a HIL test bench where nodes may not PXE boot between tests (they reboot into an already-deployed OS), there is no "force re-provision on next boot" API call that does not also require a power cycle. The reimage flow handles this for nodes with a configured power provider, but nodes without one are stuck.
- The `dry_run` option is only available on the single-node reimage modal in the node detail page, not on the group reimage modal. CI automation that uses group reimages cannot do a dry run.

**What needs to be removed**

- Nothing specific to this persona, but the `confirm()` dialog used for destructive actions (`app.js:1130`, `app.js:3022`) blocks headless Playwright/Puppeteer-based UI testing. These should be replaced with modal-based confirmations that testing frameworks can interact with programmatically.

**What should be automated**

- Post-deploy boot verification timeout should trigger an automatic alert or callback. Currently, a `deploy_verify_timeout_at` timestamp is set in the DB but there is no automated notification. A CI pipeline waiting for boot confirmation will hang until it times out on its own.
- Image build completion (ISO build or pull) should have a webhook or SSE event that CI can subscribe to. Currently, CI must poll `GET /api/v1/images/{id}/status` to detect build completion.

**What needs more customization**

- Per-image, per-deploy custom variables should be injectable at reimage request time (e.g., `TEST_SUITE=suite-a`, `BUILD_ID=1234`). Currently, `custom_vars` are stored at the node config level and require a separate PUT to update before reimaging.

**What should be more preset/cookie-cutter**

- The API key creation flow (`app.js:_settingsCreateKeyModal`) should offer a "CI integration" preset that creates a node-scoped key with a 30-day TTL and displays the curl example for triggering a reimage.

---

### Persona D — DevOps Hobbyist / Homelabber

**What works well today**

- Proxmox power provider is fully supported in the node edit modal (`app.js:2615–2652`). Fields for API URL, node name, VM ID, username, password, and TLS verification are all present. This is the primary use case for homelabbers.
- The auto-hostname generation from MAC (`nodes.go:47`, `clustr-{6hex}`) means a Proxmox VM boots into PXE and immediately appears in the UI with a usable identifier — no pre-registration required.
- The DHCP lease auto-population of interface config (`nodes.go:425–428`) means that after PXE boot, the Network tab is already pre-filled with the node's IP address. This is good zero-friction onboarding.
- The ISO build workflow (Pull Image → enter ISO URL → clustr detects `.iso` extension → shows QEMU build parameters) is a reasonable path for building a base image from a Rocky or Ubuntu ISO without any existing infrastructure.
- The hardware profile display shows CPU model, core count, NIC names/MACs, RAM, and disk list. For a homelabber debugging why a node does not deploy correctly, this is useful.

**What is missing entirely**

- No getting-started documentation or in-app onboarding. The UI opens to a dashboard with empty stat cards. There is no "First time? Start here" flow, no step-by-step wizard (create an image → add a node → deploy), and no inline help text explaining what PXE boot means or how to configure Proxmox network for PXE.
- No health check or preflight validation of the server environment. There is no page that tells a new operator: "PXE is enabled/disabled, TFTP is reachable, DHCP range is configured, the initramfs has been built." A homelabber may run through the entire setup and not know why nodes are not booting until they read logs.
- No in-UI documentation of the initramfs rebuild requirement. The initramfs card on the Images page (`app.js:961`) shows SHA256 and build time, but there is no clear callout that a rebuild is required when a new image is added, and no indicator that the current initramfs is stale relative to any image.

**What is painful or broken**

- The `<meta name="clustr-token" content="">` tag in `index.html:6` is blank. Auth is entirely handled by session cookie after login. A new user who navigates directly to `http://10.99.0.1:8080/` sees a redirect to `/login` (the server handles this), but if the session expires mid-session, actions silently fail with JSON `{"error":"authentication required"}` responses — there is no in-UI session expiry detection beyond the session expiry banner that only appears within 10 minutes of expiry (`app.js:248–268`). If a user walks away for an hour, their next action fails with an unhandled error.
- The login page (`login.html`) and set-password page (`set-password.html`) are separate HTML files, not within the SPA routing. This means the browser back button after login goes to the login page, not the previous SPA route.
- The first-time setup requires creating an admin user through a mechanism that is not documented in the UI. The CLI `clustr-serverd apikey create` command works, but new operators do not know this unless they have read the README.
- The node edit modal (`app.js:2542`) and node detail inline editor are two separate editing surfaces for the same node config, with partial field overlap. A homelabber editing a node from the list modal will not see the Network tab, Mounts tab, or Disk Layout override — those are only in the detail page. This is confusing: the modal feels like the primary edit path but it is actually incomplete.
- Groups field is a freeform comma-separated text input (`app.js:2587`). The distinction between "groups" (freeform tags, `node.groups[]`) and "Node Group" (the structured `node.group_id` entity with group-level reimages and network profiles) is not explained anywhere in the UI. Both appear in the same Overview tab, labeled "Groups / Tags" and "Node Group" respectively. A homelabber will not understand the difference.

**What needs to be removed**

- The `<details><summary>Slurm</summary>` and `<details><summary>LDAP</summary>` collapsibles in the sidebar at `index.html:103,169`. A homelabber who is not using Slurm or LDAP sees these sections in the nav every time and has to mentally filter them out. These should only be shown when the respective modules are enabled.
- "Server Info — will appear here in a future update" at `app.js:7336`. This is dead UI real estate.

**What should be automated**

- When the first node registers and the DB contains no images, the UI should automatically redirect to the Images page with a callout: "Node registered — add an image to deploy it."
- Initramfs should be built automatically on first-ever image finalization if no initramfs exists yet.

**What should be more preset/cookie-cutter**

- The node group concept should have a "Create a group for my Proxmox cluster" wizard that auto-assigns all registered Proxmox nodes to the group and prompts for a shared SSH key and base image.
- Image disk layout should have a "recommended defaults for your firmware type" button that pre-populates the partition table based on detected UEFI/BIOS firmware.

---

## Cross-Persona Synthesis

### Top 5 Universal Gaps (affect all personas)

**1. No server-side search or filtering on the nodes list.**  
The nodes list has a group-based client-side filter but no search input. On 10 nodes this is acceptable. On 200 nodes it is unusable. Filtering happens client-side after a full `/api/v1/nodes` fetch. The API supports `?base_image_id=` filtering (`nodes.go:124`) but no hostname/MAC/status/label search. This gap blocks Persona A (200-node fleet), impairs Persona B (50 nodes by team), and is acceptable only for Personas C and D.

**2. No alerting or event notification system.**  
Deploy failures, verify-boot timeouts, and node unreachability produce no outbound notifications. Every persona must poll the UI or watch logs. For Persona A this is an operational SLA risk. For Persona C this breaks CI automation. For Persona B it means failed job re-deploys are discovered manually. For Persona D it is less critical but still bad hygiene.

**3. No audit trail with user attribution.**  
Every reimage request records `requested_by: "api"` regardless of which user or API key triggered it (`reimage.go:108`). Node config changes have no event log — only `updated_at`. This is a gap for Persona A (institutional compliance), Persona B (shared lab accountability), and Persona C (test run reproducibility).

**4. Image management lacks lifecycle stages and version management.**  
Images have `building/ready/archived` statuses and a tags array that is never populated or displayed. There is no path from `ready` → `tested` → `promoted` → `deprecated`. There is no image version diff. Tags are collected by the API but invisible in the UI. All four personas need some form of "this image is safe to deploy" signal beyond "status: ready."

**5. The two editing surfaces for nodes (modal vs. detail page tabs) are inconsistent.**  
The Add/Edit Node modal exposed from the nodes list omits Network tab, Mounts tab, Disk Layout tab, and Power/IPMI tab. The node detail page has all of these as inline-editable tabs. An operator who edits a node from the list modal makes incomplete changes and does not know they are incomplete. This causes confusion and misconfigured nodes across all personas.

---

### Top 5 Persona-Specific Gaps

**1. No RBAC / multi-user permission model (Persona A only).**  
A 200-node university cluster has students, postdocs, faculty, and sysadmins. They cannot all have `admin` access. This gap is Persona A's most critical missing feature and is not relevant to the single-operator personas (C, D).

**2. GPU/CUDA inventory and constraint-based image assignment (Persona B only).**  
ML labs need to know which nodes have GPUs, which CUDA version each image provides, and receive a warning or block when assigning a CPU-only image to a GPU node. The role system (`built_for_roles`) is the embryo of this feature. Persona A does not need CUDA-awareness; Personas C and D don't either.

**3. Webhook/callback API for deploy state changes (Persona C only).**  
CI automation needs a way to be notified of deploy completion without polling. This is specifically a Persona C need — HPC admins watch dashboards, lab operators tolerate polling, homelabbers have no CI. Persona C needs push notification.

**4. Getting-started wizard and in-app onboarding (Persona D only).**  
Personas A, B, and C already know what PXE boot and IPMI are and will read documentation independently. Persona D needs hand-holding through the first deploy and benefits from in-app guidance that would be noise to experienced operators.

**5. Scheduled deploy windows (Persona A primarily, Persona B secondarily).**  
The `scheduled_at` field on reimage requests is fully implemented in the API (`reimage.go:105–127`) and DB but is invisible in the UI. University clusters have maintenance windows. The UI does not surface this at all. Personas C and D deploy on-demand; this matters only for managed fleet operators.

---

### Things in the UI Today That NO Persona Needs

1. **Sidebar `<details><summary>Slurm</summary>` and `<details><summary>LDAP</summary>` collapsibles** when those modules are disabled. If `slurm.enabled == false` (the live state in this instance), the entire Slurm nav section should be collapsed and show only a "Settings" link, not a full section with sub-items. The same applies to LDAP. The current behavior shows all sub-items regardless of module state because `nav-slurm-managed` is shown/hidden by JS after load, creating a flash of full nav content.

2. **"Server info — will appear here in a future update"** at `app.js:7336`. It is placeholder text in a shipped product. Remove it or implement it.

3. **The `alert()` and `confirm()` browser dialog calls** scattered throughout (`app.js:1130`, `app.js:3022`, `app.js:1639`). These are non-stylable system dialogs that break the visual language, block automated testing, and are inaccessible. No persona benefits from OS-native dialogs in a web UI that otherwise has a coherent design system.

4. **Duplicate Delete button + Actions dropdown "Delete node" item** on the node detail page header (`app.js:3153–3156`). Both call `deleteNodeAndGoBack`. No persona needs two paths to the same destructive action.

---

### Things That Should Be Promoted to Top-Level

1. **Initramfs status should be a persistent indicator on the sidebar or dashboard stat card, not buried on the Images page.** Currently the initramfs card is the first card on the Images page (`app.js:939`). If the initramfs is stale relative to the newest image, this is a deployment-blocking condition that should be prominently visible without navigating to Images.

2. **Node power status column in the nodes list.** The nodes list table does not show whether a node is powered on or off. Power status requires opening a per-node dropdown. For fleet operators, a power state column with cached status (which already exists on the server at `ipmi.go:191–201`) would allow at-a-glance fleet health visibility.

3. **Active deployments table on the dashboard is the most operationally useful view.** It should have a direct nav link in the sidebar or a dedicated `/deploys` page rather than being embedded in the Dashboard scroll. Operators managing rolling deploys need to reach this table in one click.

4. **Node logs tab should be accessible directly from the nodes list row**, not require entering the detail page. A "Logs" action in the power/actions dropdown on each row would serve Personas A, B, and C.

5. **Image metadata (the `/api/v1/images/{id}/metadata` endpoint at `images.go:926`) should be displayed on the image detail page.** This endpoint returns build metadata — kernel version, installed packages, build timestamp, CUDA version if present. The image detail page renders a "Image Details" KV grid that omits all of this. The endpoint is called nowhere in the frontend.

---

### API/UI Mismatches

**Mismatch 1: scheduled_at field exists in API, absent from UI.**  
`POST /api/v1/nodes/{id}/reimage` accepts `scheduled_at` (RFC3339). The node detail reimage trigger modal does not expose this field. Any operator wanting to schedule a maintenance-window deploy must use the raw API.

**Mismatch 2: `GET /api/v1/progress` returned 404 in live testing.**  
`app.js:462` calls `API.progress.list()` which should fetch all active deploy progress. The endpoint returned HTTP 404 during live API probing. If the correct path is `/api/v1/deploy/progress` or requires a different route prefix, this is an undocumented divergence. The dashboard correctly subscribes to the SSE stream at `/api/v1/progress/stream`, but the REST list endpoint is broken or differently routed.

**Mismatch 3: Image tags are API-managed but UI-invisible.**  
Every `BaseImage` response includes a `"tags": []` field. Tags are stored in the DB, accepted in `CreateImageRequest`, and available in `GetImage`. No UI element in the image grid, image detail page, or any filter uses them. Operators cannot add, remove, or search by tags from the UI.

**Mismatch 4: `/api/v1/images/{id}/metadata` exists, UI never calls it.**  
The handler at `images.go:926` returns an `ImageMetadata` sidecar with build details. The image detail page at `app.js:1878` does not call this endpoint. The metadata is built and stored but never surfaced.

**Mismatch 5: Node `group_id` vs `groups[]` semantics not exposed.**  
The API has two distinct grouping concepts: `group_id` (FK to the `node_groups` table, enables group-level reimages and network profiles) and `groups` (freeform `[]string` labels). In the node Overview tab inline editor, these appear side by side labeled "Node Group" and "Groups / Tags" (`app.js:3219–3228`). No tooltip or help text explains the distinction. The node list modal at `app.js:2587` only shows "Groups (comma-separated)" and omits `group_id` entirely, so nodes created from the list modal cannot be assigned to a Node Group from the creation flow.

**Mismatch 6: `sensors` endpoint returns empty list for Proxmox-backed nodes.**  
The Sensors endpoint at `ipmi.go:330` only populates sensor data for nodes with IPMI/BMC credentials. For Proxmox-backed nodes (which have `power_provider.type = "proxmox"` rather than BMC config), `sensors` returns `[]`. The UI shows an empty "IPMI Sensors" section in the node detail BMC tab with no explanation that sensors are not available for this provider type.

**Mismatch 7: Group reimage modal missing `dry_run` option.**  
Individual node reimage modal exposes `dry_run`. The group reimage modal at `app.js:6487` does not pass `dry_run` to `API.nodeGroups.reimage()`. The API path for group reimage presumably also supports `dry_run` in the request body. These two code paths are inconsistent.

---

## Prioritized Backlog

### P0 — Blocks Real Use (fix before any external users)

| # | Finding | File:Line |
|---|---------|-----------|
| P0-1 | `GET /api/v1/progress` returns 404 in live instance — active deploy polling on dashboard broken | `app.js:462`, server routing |
| P0-2 | Node list modal omits `group_id` field — nodes created from the modal cannot be assigned to a Node Group | `app.js:2542–2708` |
| P0-3 | `<details><summary>` nav flash shows full Slurm/LDAP nav on every page transition even when modules disabled | `index.html:103,169` |
| P0-4 | `alert()` / `confirm()` browser dialogs on destructive actions — must be modal-based for accessibility and testability | `app.js:1130,1639,3022` |
| P0-5 | Session expiry: 401 responses from API produce unhandled JSON error blobs, not a redirect to login | `app.js:_watchHealth` |

### P1 — Significant Operator Pain (target for sprint 1)

| # | Finding | File:Line |
|---|---------|-----------|
| P1-1 | No hostname/MAC/status search on nodes list | `app.js:2336` (nodes page) |
| P1-2 | `scheduled_at` reimage field exists in API but not surfaced in UI | `app.js:_nodeActionsTriggerReimage`, `reimage.go:105` |
| P1-3 | Image tags: stored and returned by API, no UI to add/filter by tags | `app.js:images()`, `images.go:ListImages` |
| P1-4 | Image metadata endpoint never called — build metadata invisible | `app.js:imageDetail`, `images.go:926` |
| P1-5 | Power state missing from nodes list — requires per-node dropdown open to see | `app.js:_nodesRefresh`, `ipmi.go:191` |
| P1-6 | Initramfs staleness indicator absent — no dashboard-level warning when initramfs is older than newest image | `app.js:dashboard`, `images.go` |
| P1-7 | Group reimage missing `dry_run` option — inconsistent with single-node reimage | `app.js:6487`, single-node modal |
| P1-8 | Sensors tab shows empty list for Proxmox nodes with no explanation | `app.js:_onBMCTabOpen`, `ipmi.go:330` |
| P1-9 | Audit trail: reimage `requested_by` hardcoded to `"api"` — no user identity attached | `reimage.go:108` |
| P1-10 | `group_id` vs `groups[]` confusion: no UI help text explaining the distinction | `app.js:3219,3228` |

### P2 — Important but Not Blocking (sprint 2+)

| # | Finding | File:Line |
|---|---------|-----------|
| P2-1 | No RBAC / fine-grained role model | `app.js:7320` (admin check only) |
| P2-2 | No alerting/notification system (email, Slack, webhook) | — |
| P2-3 | No GPU hardware detection or display | `app.js:_hardwareProfile` |
| P2-4 | No image lifecycle stages (test → staging → production) | `app.js:badge()` |
| P2-5 | No column sorting on any table (nodes list, images list, reimages) | `app.js:_nodesTable`, `_imagesTable` |
| P2-6 | No webhook/callback on deploy completion for CI | `reimage.go:Create` |
| P2-7 | `<details>` nav collapsibles break sidebar state on hash navigation | `index.html:103,169` |
| P2-8 | Duplicate Delete button + dropdown Delete item on node detail page header | `app.js:3153,3156` |
| P2-9 | No cancel-active-reimage-by-node-id endpoint for CI use | `reimage.go:Cancel` |
| P2-10 | No getting-started onboarding flow | — |
| P2-11 | Cluster-wide SSH key management absent | `app.js:2595` |
| P2-12 | "Server info — will appear here in a future update" placeholder | `app.js:7336` |
| P2-13 | Image creation: 4 entry points with no recommended path guidance | `app.js:918–935` |
| P2-14 | Kernel args: no group-level inheritance model | `app.js:2591` |
| P2-15 | Post-deploy boot failure: "Attach serial console" with no link or IPMI SOL button | `app.js:3183` |
| P2-16 | Automatic initramfs rebuild on image finalization absent | `app.js:confirmRebuildInitramfs` |
| P2-17 | No pagination on nodes list or images list — full fetch on every load | `app.js:nodes(), images()` |

---

## Anti-Patterns Observed (Do Not Repeat)

1. **Parallel editing surfaces with different field coverage.** The node list modal and the node detail inline editors edit the same resource but expose different subsets of fields. Any future resource that is editable should have exactly one editing surface, or the secondary surface must explicitly acknowledge it is a subset.

2. **Freeform text for multi-value fields.** `groups` is a comma-separated text input. `ssh_keys` is a newline-separated textarea. Both are split by the frontend (`app.js:2892–2893`). These should be purpose-built tag inputs or key editors. Freeform split is brittle (what if a key contains a comma?) and makes validation impossible.

3. **Hard-wiring `confirm()` for destructive actions.** `browser.confirm()` is not stylable, not keyboard-accessible, and not testable. The codebase uses it at multiple points despite having a working modal system. Any new destructive action must use the modal pattern.

4. **Inline style strings for layout throughout app.js.** Approximately 40% of layout styling is in `style=` attributes in template literals inside `app.js`. Maintenance requires grep-based archaeology. New UI work should move layout to CSS classes in `style.css`.

5. **Event listener cleanup by side effect.** The router's `_navigate()` function removes specific named listeners (`Pages._closeActionsDropdownOnOutsideClick`, `Pages._closePowerDropdownsOnOutsideClick`) by name at `app.js:33–39`. This pattern requires the cleanup logic in the router to know about every page-level listener. New features must register their cleanup in the same place or their listeners will leak.

6. **Module nav visibility controlled by post-load JS hide/show.** The Slurm and LDAP nav sections are rendered in the HTML, then hidden via `style.display = 'none'` in `index.html:65,82`. This causes a flash of the full nav on every load and tightly couples the nav HTML to JS initialization order. Modules should gate their nav items entirely on server-returned capability/status data fetched at init time.

7. **`escHtml()` used inconsistently as event handler argument.** Throughout `app.js`, node IDs and display names are injected into `onclick=""` attribute strings via `escHtml()`. This works until a value contains a single quote or backslash. Correct pattern is to attach event listeners programmatically post-render, not via HTML attribute injection. No XSS is exploitable in this specific deployment (same-origin, authenticated), but the pattern will cause bugs when node names contain apostrophes.

---

## Open Questions for the Founder

These require strategic direction before the code review team can make recommendations:

**Q1: What is the intended RBAC model?**  
Currently: `admin` (full access) and `node` (deploy agent scoped key). Is the target model user-role (admin/operator/viewer) scoped per node group? Or is it LDAP-group-based delegation? The answer determines whether to build a custom RBAC table or lean into the existing LDAP groups integration. This decision has significant backend implications.

**Q2: Is there a commitment to the `groups[]` freeform tag field alongside the `NodeGroup` entity, or should `groups[]` be deprecated in favor of making NodeGroups the sole grouping primitive?**  
Currently both coexist. The freeform `groups[]` is used for Slurm node role assignment (`app.js:slurm` role matching). If Slurm roles and node grouping both need to exist, they should be clearly named differently and documented. If `groups[]` is vestigial, it should be removed before the surface area grows.

**Q3: What is the retention and archival model for the node log table?**  
The logs table in SQLite receives ingest at up to 100 req/s per node (`logs.go:141`). At 50 nodes doing a 10-minute deploy, this table can accumulate millions of rows. There is no log rotation, TTL policy, or archive path visible in the UI or API. For Persona A at 200 nodes, this is a disk pressure issue within months of production use. The answer determines whether to add a background pruning job or expose log retention settings in the Settings page.

**Q4: Is the Slurm module intended as a differentiator for the HPC market or a secondary feature?**  
It is the most complete module in the codebase and significantly ahead of the images/nodes core in terms of UI depth (config versioning, upgrade orchestration, per-node overrides). If this is the primary HPC differentiator, it deserves first-class nav treatment and should not be buried behind a collapsible `<details>` element.

**Q5: What is the path to external documentation?**  
There is no in-app help link, no `?` tooltip system, and no documentation URL in the Settings About tab (though the structure exists at `app.js:_settingsAboutTab`). Before public launch, an operator should be able to reach documentation from within the UI. Is the documentation target the existing `tunnl.sh-docs` repo, a dedicated site, or embedded in-app?
