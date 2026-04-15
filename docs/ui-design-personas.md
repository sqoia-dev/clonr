# clonr UI Design: Persona Spec for Turn-Key HPC

**Last Verified:** 2026-04-13 — applies to clonr main @ 9b56b4f

**Sprint 2 design input — UI experience, not API design.**
Written against the baseline in `pkg/server/ui/static/js/app.js` and `pkg/server/ui/static/index.html`.
Richard's ADRs and roadmap are out of scope here. This doc owns the UX layer only.

---

## 1. Persona Journeys

### Persona A — Maria (Junior sysadmin, 20-node GPU cluster)

**Goal:** Go from "I have 20 bare servers and a freshly installed clonr" to "my cluster is running and sbatch works."

**Step-by-step journey:**

**Step 1 — Land on Dashboard.**
Maria opens clonr in her browser. She sees the current dashboard: stat cards (images, nodes, active deployments, system health), an empty Active Deployments table, and a live log stream.
- Status: ✓ Page exists. The "System Health" card says "Online: PXE · API · Logs" which is good.
- Gap: ✗ No call-to-action for a first-time user. The dashboard has no entry point to a cluster wizard. Maria sees "Pull Image" and "View Logs" buttons, both of which mean nothing to her yet.
- Gap: ✗ No concept of a "cluster" at all. The stat cards track images and nodes as disconnected objects, not as a unified system.

**Step 2 — Discover nodes.**
Maria powers on the 20 servers. They PXE boot into clonr's discovery environment. She navigates to `#/nodes`. She sees nodes appearing in the list as their MACs register, each with status "Registered" (hardware profile present, no image assigned).
- Status: ✓ Node list at `#/nodes` exists (`Pages.nodes()`). Nodes appear as MACs register via PXE. The "Registered" badge state is implemented (`nodeBadge()`).
- Gap: ✗ No rack/role assignment at this stage. Every node looks identical in the list. Maria cannot distinguish GPU compute nodes from head nodes.
- Gap: ✗ No "I'm done discovering, build my cluster from these" affordance. The CTA buttons on the nodes page are "Add Node" (manual) and "Import CSV" — no wizard entry point.
- Gap: ◐ Hardware profile is stored (`hardware_profile` field in API) but not surfaced in the list view. GPU nodes, storage nodes, and head nodes look the same in the table.

**Step 3 — Enter "New Cluster" wizard (does not exist today).**
Maria clicks a prominent "New Cluster" button on either the Dashboard or the Nodes page. This opens a full-page wizard (not a modal) at `#/wizard/cluster`. The wizard walks her through 10 steps detailed in Section 3. Each step has a progress indicator at the top (Step 2 of 10), a "Next" / "Back" pair, and a live validation strip that surfaces errors before she can proceed.
- Status: ✗ This entire page does not exist. There is no `#/wizard/*` route, no cluster concept, and no multi-step form pattern in the current codebase.

**Step 4 — Topology: assign roles.**
In wizard step 2, Maria sees a grid of discovered nodes (MAC + hardware profile summary). She clicks each one and assigns a role: head, compute, storage. A "Bulk assign" button lets her select all GPU-profiled nodes and tag them "compute" at once.
- Status: ✗ No role assignment UI. Image roles exist (`/api/v1/image-roles`) and are rendered in the ISO build modal, but node-level role assignment is not connected to topology.

**Step 5 — Image selection.**
Wizard step 4: Maria picks an image per role. If no ready image exists, she is offered a guided "Build from ISO" inline — the same ISO build flow that exists today but surfaced contextually.
- Status: ◐ The Build from ISO modal exists and works (`Pages.showBuildFromISOModal()`). The gap is that it's disconnected from any cluster or role context. Wizard would embed it inline.

**Step 6 — Network + directory services configuration.**
Wizard steps 5-7: Maria fills in DNS server, domain, NTP server, NFS mounts, and optionally LDAP endpoint. These fields do not exist anywhere in the current UI — they are free-form config values clonr needs to inject into the deployed OS.
- Status: ✗ None of these fields exist in any page or modal today. The node config API (`api.NodeConfig`) has `kernel_args` and `custom_vars` but no first-class fields for DNS/NTP/NFS/LDAP.

**Step 7 — Review and execute.**
Wizard step 9: Maria sees a human-readable summary of what clonr is about to do: 18 compute nodes will be imaged with `rocky-9-cuda124`, 1 head node with `rocky-9-head`, 1 storage node with `rocky-9-storage`. Network config shows her chosen DNS/NTP. She clicks "Deploy Cluster."
- Status: ✗ No review step. No cluster-scoped deploy action exists.

**Step 8 — Watch progress.**
A new `#/clusters/<id>` page appears showing a fleet-wide progress gauge (e.g. "14 of 20 nodes complete") and a per-node status grid (like the dashboard's Active Deployments table but scoped to this cluster). The existing `ProgressStream` SSE mechanism feeds the per-node rows.
- Status: ◐ The `_deployProgressTable` on the dashboard and the ProgressStream SSE are solid foundations. What doesn't exist: scoped to a cluster, a fleet gauge, or a "Cluster" entity in the data model.

**Step 9 — Success.**
All nodes reach "Deployed" status. The cluster page shows a green cluster health banner. Maria sees "Your cluster is ready. Head node: `head01.mylab.local`." A link takes her to first-boot validation results (ping, SLURM join, NFS mount check).
- Status: ✗ First-boot validation results page does not exist. No cluster health summary.

---

### Persona B — Sanjay (Senior HPC admin/SRE, 500-node cluster)

**Goal:** At 2am, reimage all 50 nodes in rack 7 during a maintenance window with rolling policy, pause on failure, and a clean audit trail.

**Step-by-step journey:**

**Step 1 — Open Dashboard.**
Sanjay navigates to Dashboard. He wants a snapshot: are there any active deployments, any nodes in error state, any hung jobs? The current dashboard gives him images count, nodes count, active deployments. It's functional but sparse.
- Status: ◐ The dashboard exists and shows active deployments via SSE. Missing: nodes-in-error count, last deploy success rate, a fleet health signal.

**Step 2 — Navigate to fleet view and filter by rack.**
Sanjay goes to `#/nodes`. He needs to see only rack 7. Today he can filter by group tag (node groups exist). He selects group "rack-07" or applies a tag filter "rack=7".
- Status: ◐ Node groups exist (`Pages.nodeGroups()`). The gap: there is no rack-aware topology view. Nodes in a group are shown as a flat list with no positional context. Tag filtering on the nodes list is not implemented — the filter field in the nodes list UI does client-side hostname substring search only (`nodesRefresh()` includes a filter input, but it matches hostname text, not tags/groups).

**Step 3 — Select 50 nodes.**
Sanjay needs to select all 50 nodes. He checks "Select all in this group" at the top of the filtered list. A bulk action bar appears at the bottom of the screen showing "50 nodes selected" and available actions.
- Status: ✗ No checkbox multi-select on the nodes list. No bulk action bar. Each node is individually clickable to its detail page only. This is the single highest-priority gap for Sanjay's workflow.

**Step 4 — Configure rolling reimage policy.**
Sanjay clicks "Reimage" from the bulk action bar. A modal appears: image picker (dropdown of ready images), rolling policy fields (max concurrent: 5, pause threshold: >10% failure rate, stagger delay: 60s), and a dry-run checkbox.
- Status: ✗ No bulk reimage action exists. No rolling policy concept. The existing reimage flow is per-node only (visible in node detail page actions dropdown). No dry-run mode.

**Step 5 — Watch execution.**
A "Deployment" record is created at `#/deployments/<id>`. Sanjay sees a progress panel: fleet gauge (completed / total), per-node status table sortable by phase, a real-time failure counter, and a prominent "Pause" button and "Abort Fleet" button.
- Status: ✗ No `/deployments` route or page exists. The current progress tracking is on the dashboard only, via `_deployProgressTable`. No Pause or fleet Abort action.

**Step 6 — Pause on failure.**
At node 23, the failure rate hits 12%. The UI automatically pauses the rolling deploy and shows an alert banner: "Deploy paused: 6/50 nodes failed (12%). Review failures before continuing." Sanjay inspects the failed nodes' logs inline, determines it's a DHCP timeout issue, resolves it, and clicks "Resume."
- Status: ✗ Auto-pause on failure threshold does not exist. No Resume action.

**Step 7 — Audit log.**
After the window, Sanjay opens `#/audit` to review what happened: every reimage action, who triggered it (API key), which image, timestamps. He exports the log as CSV for the incident report.
- Status: ✗ No audit log page. The current `#/logs` page (`Pages.logs()`) shows raw server-side event logs (node boot, deploy phases) but not structured audit events (who did what, when, to which nodes).

---

### Persona C — Priya (Research/ML engineer, custom CUDA image)

**Goal:** Build a CUDA 12.4 image with a custom PyTorch environment, verify it works, test on one node, then ask Sanjay to push it to the fleet.

**Step-by-step journey:**

**Step 1 — Navigate to Images.**
Priya opens `#/images`. She sees the image grid with existing images. She clicks "Build from ISO."
- Status: ✓ Images page exists. Build from ISO modal exists and works.

**Step 2 — Configure the build.**
She pastes a Rocky 9 ISO URL, names the image `cuda-124-pytorch`, selects the "GPU Compute" role preset (which injects CUDA packages and kernel args), enables "Install OS updates," and pastes her custom kickstart snippet that adds her lab's Conda environment.
- Status: ◐ Role presets exist and render in the modal. Custom kickstart textarea exists. The gap: there is no recipe editor with named sections (packages, post-install scripts, kernel args, user accounts). The kickstart textarea is raw and unvalidated — Priya has to know kickstart syntax.

**Step 3 — Monitor the build.**
The modal closes and redirects her to `#/images/<id>`. The ISO build progress panel shows elapsed time, current phase (partitioning, installing, capturing), and a live log tail.
- Status: ✓ ISO build progress panel exists (`Pages._isoBuildSSE`, `_renderIsoBuildProgress`). The image detail page at `#/images/<id>` exists with tabs.

**Step 4 — Shell into the image to verify.**
Once the image is `ready`, Priya clicks "Open Shell" on the image detail page. An xterm.js terminal opens in a panel, dropping her into a systemd-nspawn chroot of the image. She runs `nvcc --version`, `python -c "import torch"`, confirms her environment is correct.
- Status: ✓ Browser shell exists (`Pages.shellTerminal()`). The xterm.js integration is implemented. The gap: there is no read-only indicator on the shell (Priya may not know if her changes persist). No session timeout warning. No "copy file from image to local download" affordance.

**Step 5 — Test on a single node.**
Priya navigates to `#/nodes`, finds her test node `gpu-dev-01`, opens its detail page, assigns the new image in the Overview tab, and clicks "Deploy Now" from the Actions dropdown.
- Status: ✓ Node detail page exists with Overview tab for image assignment. Actions dropdown exists with "Deploy Now." The gap: there is no way for Priya to confirm which user account triggered the deploy (API key is not surfaced in the UI). She also cannot set a "deploy to single node, notify Sanjay on completion" workflow.

**Step 6 — View node deploy logs.**
She stays on the node detail page. The deploy progress appears in the Active Deployments panel on the dashboard (she has to navigate away to see it). On the node detail page itself, there is no live progress panel.
- Status: ✗ Node detail page does not have an inline deploy progress panel. Priya has to go to the dashboard to watch per-node progress. This is a usability gap for a researcher who wants to watch her single-node deploy.

**Step 7 — Promote to production.**
Once satisfied, Priya wants to flag the image as "tested / promote to fleet." There is no such status or workflow. She has to open a ticket or Slack Sanjay manually.
- Status: ✗ No image promotion workflow or "ready for fleet" flag. No notification/handoff mechanism between Priya and Sanjay in the UI.

---

## 2. Information Architecture

### Current nav
`Dashboard / Images / Nodes / Logs`

Node Groups live under `#/nodes/groups` — a sub-route of Nodes, not a top-level item.

### Proposed nav for turn-key HPC

```
Dashboard
Clusters          [NEW — turn-key unit]
Images
Nodes
  └─ Nodes (list)
  └─ Groups
  └─ Topology (rack view)  [NEW]
Deployments       [NEW — active + history, replaces dashboard embed]
Audit             [NEW — structured event log]
Logs              [keep — raw log stream, useful for debugging]
Settings          [NEW — DNS/NTP/LDAP/NFS sources, API keys]
```

**Defense of each change:**

- **Clusters** must be a first-class nav item. The turn-key value prop is "manage my cluster as a unit." Without a Clusters page, clonr is just a node imager. This is where Maria's wizard lives, where Sanjay's fleet health lives.
- **Topology** under Nodes gives Sanjay a rack/zone view without moving the flat node list that works today.
- **Deployments** as a top-level item removes the asymmetry where deploy progress only lives on the dashboard. A deployment is a first-class object (triggered, in-progress, paused, complete, failed). It deserves its own list and detail page.
- **Audit** is separate from Logs. Logs = raw event stream for debugging. Audit = structured who-did-what trail for compliance and incident review. They are different audiences (Sanjay's incident report vs. Dinesh's debugging session).
- **Settings** consolidates global config that today has no home: default DNS server, NTP server, LDAP endpoint, NFS mount templates, API key management. These are referenced by the cluster wizard but need to be editable standalone.
- **Node Groups** stays under Nodes — it is a property of the fleet, not a top-level concept.

---

## 3. The "New Cluster Wizard" (Maria's Magic Moment)

Route: `#/wizard/cluster`. Full-page layout, not a modal. Top progress bar shows current step. Each step validates before allowing Next.

### Step 1 — Name your cluster

What Maria sees:
```
+------------------------------------------+
| Step 1 of 10: Name Your Cluster          |
|                                          |
| Cluster name:  [ml-gpu-cluster         ] |
| Description:   [GPU cluster for ML lab ] |
| Admin contact: [maria@lab.edu          ] |
|                                          |
| [Next →]                                 |
+------------------------------------------+
```

API fields needed: `POST /api/v1/clusters` with `{name, description, contact_email}`.

---

### Step 2 — Discover nodes

What Maria sees: a live table that auto-refreshes as nodes PXE into the discovery environment. Each row shows MAC, discovered hardware profile (CPU cores, RAM, GPU count if detected), and a "Role" dropdown (head / compute / storage / login / unassigned).

```
+--------------------------------------------------+
| Step 2 of 10: Discover Nodes                     |
|  Waiting for nodes to PXE boot…  [3 / 20 found] |
|                                                  |
| MAC               CPU       RAM   GPU  Role       |
| aa:bb:cc:11:22:33 64c/3.2G  256G  4x   [Compute] |
| aa:bb:cc:44:55:66 32c/2.4G  128G  —    [Head   ] |
| ...                                              |
|                                                  |
| [Bulk assign: select + set role]                 |
| [← Back]  [Next → (confirm 20 nodes)]            |
+--------------------------------------------------+
```

Validation: must have exactly 1 head node to proceed. Warning if no storage node detected.

API fields needed: `GET /api/v1/nodes?cluster_id=<id>` returning discovered nodes with hardware profiles. `PATCH /api/v1/nodes/<id>` to set role.

---

### Step 3 — Topology assignment

What Maria sees: a rack grid. She can drag-and-drop nodes into rack slots, or import a CSV with columns `mac,rack,slot,role`.

```
+-----------------------------+
| Rack A         Rack B       |
| [head01 ]  [ ]             |
| [cmp01  ]  [cmp11  ]       |
| [cmp02  ]  [cmp12  ]       |
| ...                         |
+-----------------------------+
| [Import CSV] [Skip for now] |
```

API fields needed: `PATCH /api/v1/nodes/<id>` with `{rack, slot}`. Or `POST /api/v1/clusters/<id>/topology/import`.

---

### Step 4 — Image selection

Per-role image assignment. If no image is ready for a role, inline "Build from ISO" collapses open.

```
Head node role:    [rocky-9-head v1.2  ▼]
Compute role:      [rocky-9-cuda124 v1 ▼]  or [+ Build new image]
Storage role:      [rocky-9-storage v1 ▼]
```

API fields needed: `GET /api/v1/images?status=ready`. `PATCH /api/v1/clusters/<id>` with `{role_image_map: {head: <id>, compute: <id>, storage: <id>}}`.

---

### Step 5 — Network configuration

```
DNS servers:      [10.0.0.1, 10.0.0.2        ]
Domain:           [mylab.hpc.edu              ]
NTP servers:      [time.mylab.edu             ]
Provisioning VLAN:[100   ]  Prod VLAN: [200  ]
DHCP range:       [10.1.0.100 – 10.1.0.250   ]
```

Validation: DNS must be reachable (ping check via API). Domain must be non-empty.

API fields needed: `PATCH /api/v1/clusters/<id>` with `{dns_servers, domain, ntp_servers, provisioning_vlan, prod_vlan, dhcp_range_start, dhcp_range_end}`.

---

### Step 6 — Directory services (optional)

```
[ ] Enable LDAP/SSSD
    LDAP URI:    [ldap://auth.mylab.edu      ]
    Bind DN:     [cn=clonr,dc=mylab,dc=edu  ]
    Bind PW:     [**********                ]
    Base DN:     [dc=mylab,dc=edu           ]
    [ ] Test connection before proceeding
```

If unchecked, skip. Maria can come back via Settings.

API fields needed: `PATCH /api/v1/clusters/<id>` with `{ldap_uri, ldap_bind_dn, ldap_bind_password_ref, ldap_base_dn}`. Password stored as secret reference, not in DB plaintext.

---

### Step 7 — Storage / NFS mounts

```
+ Add NFS mount
  Server:     [nfs.mylab.edu          ]
  Export:     [/data                  ]
  Mount point:[/data                  ]
  Options:    [rw,hard,intr           ]
  Roles:      [x Compute  x Login     ]
```

API fields needed: existing `extra_mounts` on NodeConfig / Group. Cluster-level NFS templates that propagate to compute nodes in the group.

---

### Step 8 — Scheduler integration

```
Scheduler:      [SLURM ▼]  (SLURM / Torque / PBS / None)
Head node:      [head01.mylab.edu  (auto-detected)]
Partition name: [gpu                ]
Compute nodes:  [all compute-role nodes  (20)]
[ ] Register nodes with slurmctld after deploy
```

API fields needed: `PATCH /api/v1/clusters/<id>` with `{scheduler_type, head_node_id, partition_name, slurm_config_ref}`.

---

### Step 9 — Review

Full human-readable preview:

```
CLUSTER: ml-gpu-cluster
  Head node:     head01 → rocky-9-head v1.2
  Compute nodes: 18 nodes → rocky-9-cuda124 v1
  Storage node:  stor01   → rocky-9-storage v1

NETWORK:
  DNS: 10.0.0.1, 10.0.0.2 | Domain: mylab.hpc.edu
  NTP: time.mylab.edu | Provisioning VLAN: 100

DIRECTORY: LDAP at ldap://auth.mylab.edu (tested OK)
NFS: /data from nfs.mylab.edu → /data (compute, login)
SCHEDULER: SLURM, partition: gpu

[✓ Estimated time: ~45 minutes for 20 nodes]
[← Back] [Deploy Cluster →]
```

---

### Step 10 — Execute

Full-page progress view. This becomes the cluster detail page at `#/clusters/<id>`.

```
Cluster: ml-gpu-cluster          [14 / 20 complete]  ████████████░░░░ 70%

Node         Role     Phase        Progress
head01       head     complete     ✓
cmp01        compute  finalizing   ████████░ 85%
cmp02        compute  formatting   ████░░░░░ 45%
...

[Pause]  [Abort]

Live log: [scrolling deploy events…]
```

---

## 4. Bulk Operations UX (Sanjay's World)

### Selection model

Add checkbox column to the nodes list. Behavior:
- Header checkbox: select all nodes in the current filtered view (not all nodes globally).
- `Shift+click`: range select.
- Tag filter + group filter narrow what "select all" covers — so Sanjay can filter to `group=rack-07` then "select all" to get exactly the 50 nodes he wants.
- Selection count shown in a sticky bar at the bottom: `50 nodes selected · [Reimage] [Power] [Assign Group] [Tag] [Export] [Clear selection]`.

### Available bulk actions

| Action | Description |
|--------|-------------|
| Reimage | Opens rolling policy modal: image picker, max concurrent, failure threshold, stagger delay, dry-run toggle |
| Power cycle | Reboot / power-off via BMC (IPMI/Proxmox provider) |
| Assign image | Change base_image_id without triggering deploy |
| Assign group | Move selected nodes to a node group |
| Clear override | Remove node-level overrides, revert to group defaults |
| Tag add/remove | Add or remove tags (k=v pairs) on selected nodes |
| Export inventory | Download CSV: hostname, MAC, IP, image, group, rack, last deploy |

### Rolling policy modal

```
+-- Reimage 50 nodes ----------------------+
| Image:          [rocky-9-cuda124 v1  ▼]  |
|                                          |
| Rolling policy:                          |
|  Max concurrent:  [5   ] nodes           |
|  Pause if failure rate exceeds: [10  ]%  |
|  Stagger delay:   [60  ] seconds         |
|                                          |
| [x] Dry run (preview only, no changes)  |
|                                          |
| [Cancel]  [Start Deploy →]               |
+------------------------------------------+
```

### Pause on failure rate

When the running failure counter crosses the threshold during execution:
- The deployment status changes to `paused`.
- A red banner appears at the top of the deployment page: "Deploy paused: 6 of 50 nodes failed (12%). Investigate before continuing."
- Failed nodes are highlighted in the per-node table with a "View logs" inline link.
- Two action buttons: "Resume" (continues from where it left off, skipping already-failed nodes) and "Abort" (stops all pending nodes, marks deployment failed).

### Fleet abort

The "Abort" button is behind a one-step confirm dialog (not a double-confirm, not hidden in a menu — just a single modal):

```
+-- Abort Fleet Deploy? -------------------+
| This will cancel all pending nodes.      |
| Nodes currently imaging will complete    |
| their current phase before stopping.     |
| Nodes already deployed are unaffected.   |
|                                          |
| [Cancel]  [Abort Deploy]                 |
+------------------------------------------+
```

The button is visually distinct (red outline, not filled) to avoid accidental clicks, but it is not hidden.

---

## 5. Shell / Image Inspection UX (Priya's World)

### Current shell gaps

The browser shell (`Pages.shellTerminal()`) opens an xterm.js terminal connected to a WebSocket that proxies systemd-nspawn into the image. This works. What's missing:

1. **Read-only indicator.** The shell header should show "Read-only session — changes do not persist to the image." This is false today (changes inside nspawn may or may not persist depending on how the session is mounted), but the intent needs to be communicated. If the session is mounted read-write (for inspection), add a warning: "Changes in this shell are written to the image."
2. **Session timeout.** No timeout warning or auto-close. A 30-minute idle timeout with a 2-minute warning banner prevents runaway sessions holding the image locked.
3. **File download.** An "Export file" button in the shell panel that accepts a path and downloads it: useful for `nvidia-smi` output, `/etc/modules-load.d/`, custom config files.
4. **Diff against base.** A "Compare to base image" link that shows a file-level diff (files added/removed/modified vs. the source image it was built from). This helps Priya verify her kickstart changes landed correctly.

### Image recipe editor (speculative — pending Richard's ADR)

If clonr grows a recipe-based build system, the UI entry point would be a new tab on the Images page: "New from Recipe." The form would have:

```
Base image:      [rocky-9-base v1.2  ▼]
Name:            [cuda-124-pytorch        ]

Packages:        [+ Add package]
  cuda-toolkit-12-4
  python3-pip
  openmpi

Post-install scripts:  [+ Add script]
  [pip install torch==2.3.0 … (textarea)]

Kernel args:     [rd.driver.pre=nouveau modprobe.blacklist=nouveau]

User accounts:   [+ Add user]
  priya  UID:1234  Groups: wheel,users  SSH key: [paste]

Validate against base:  [✓ Run pre-flight]
```

Each package is validated against the distro's repo metadata (API call). Post-install scripts are linted for common errors (unclosed heredocs, missing shebangs). The "Run pre-flight" button triggers a dry-run build in a throwaway VM and returns a pass/fail.

---

## 6. Dashboard Redesign

### What the current dashboard answers

Active deployments (live), recent images, recent nodes, recent activity timeline, live log stream. This is useful for someone debugging a single deploy, not for Sanjay's Monday morning "is my cluster healthy?" question.

### Proposed widget set

| Widget | Maria | Sanjay | Source |
|--------|-------|--------|--------|
| Cluster health summary (green/yellow/red per cluster) | primary | secondary | new — cluster entity |
| Active deployments table (SSE, as today) | secondary | primary | exists |
| Deploy success rate (7-day sparkline) | — | primary | new — deployment history |
| Nodes in error state (count, links) | — | primary | new — aggregate node status |
| Images: ready / building / error | secondary | secondary | exists |
| SLURM health (partition up/down) | — | primary | new — optional integration |
| Pending reimages (nodes flagged, not yet deployed) | — | primary | new — reimage request queue |
| Recent audit events (last 5 structured events) | — | primary | new — audit log |
| Storage utilization on clonr-serverd | — | secondary | new — server-side metric |

### Persona toggle vs. single dense view

Recommendation: **one dashboard, but with a "Compact" / "Full" toggle in the top-right.** In Compact mode, the top half shows only: cluster health summary cards (one card per cluster, green/yellow/red), active deployments count, and a "View details" link. In Full mode (default for anyone who's clicked it once), all widgets are visible.

Do not implement a persona switcher — it's an engineering distraction and creates a support surface ("why does my view look different from yours?"). Maria will learn to read the dense dashboard in a week. The cluster health cards are visually prominent enough that she sees her green card and stops reading.

---

## 7. Gap List — Prioritized UI Work Items

| Priority | Item | Effort | Persona(s) | Notes |
|----------|------|--------|------------|-------|
| P0 | New Cluster wizard (`#/wizard/cluster`, 10-step flow) | L | Maria | Spine of the turn-key value prop. Nothing else matters without this. |
| P0 | Cluster entity + `#/clusters` list + `#/clusters/<id>` detail | M | Maria, Sanjay | Data model prerequisite for wizard and fleet operations. |
| P0 | Checkbox multi-select + bulk action bar on Nodes list | M | Sanjay | Without this, Sanjay cannot do any fleet operation from the UI. |
| P0 | Bulk Reimage with rolling policy modal | M | Sanjay | Core fleet op. Requires bulk select (above) and Deployment entity (below). |
| P0 | Deployments entity + `#/deployments` list + detail page | M | Sanjay, Maria | Active deployments off the dashboard. Pause/Resume/Abort actions live here. |
| P1 | Inline deploy progress panel on Node detail page | S | Priya | Priya watches her single-node deploy; today she has to switch to dashboard. |
| P1 | Settings page (`#/settings`): DNS/NTP/LDAP/NFS defaults + API keys | M | Maria, Sanjay | Wizard references these; need a home for editing them independently. |
| P1 | Audit log page (`#/audit`): structured events, filterable, CSV export | M | Sanjay | Incident review and compliance. Distinct from the raw Logs page. |
| P1 | Topology / rack view under Nodes | M | Sanjay | Rack filter prerequisite. Without it, bulk-by-rack requires perfect group naming. |
| P1 | Cluster health widget on Dashboard (per-cluster green/yellow/red) | S | Maria, Sanjay | Monday morning status check. |
| P1 | Shell session: read-only indicator + session timeout + file download | S | Priya | Polish around an already-working feature. |
| P2 | Image promotion workflow ("ready for fleet" flag + handoff) | S | Priya, Sanjay | Nice-to-have for the Priya→Sanjay handoff; can be a simple status tag initially. |
| P2 | Deploy success rate 7-day sparkline on Dashboard | S | Sanjay | Requires deployment history store. Blocked on P0 Deployments entity. |
| P2 | Image recipe editor (new-from-recipe tab) | L | Priya | Pending Richard's ADR on recipe build system. Do not start until ADR is resolved. |
| P2 | Diff-against-base-image in shell panel | M | Priya | Useful but not blocking. Requires image lineage tracking in data model. |
| P2 | Tag filter on Nodes list (not just hostname text search) | S | Sanjay | Currently only hostname substring filter exists in `nodesRefresh()`. |
| P2 | SLURM health widget on Dashboard | M | Sanjay | Requires SLURM API integration. Parking until scheduler ADR. |

### Coverage check

A new HPC admin (Maria) using only the clonr UI, starting from an empty system, can reach "sbatch hello-world runs" only after the P0 items above are shipped: the Cluster wizard, the Cluster entity, and the Deployments entity are all load-bearing. Without them, she can register nodes and build images but has no guided path to a running cluster. The current UI baseline is strong for image management and single-node operations; it is not yet a turn-key HPC tool. The gap is navigable in one sprint if the wizard and cluster entity are tackled first.
