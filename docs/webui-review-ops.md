# clustr Web UI — Operator UX Review

**Date:** 2026-04-27
**Reviewer:** Jared (Chief of Staff / Ops)
**Scope:** Operator UX only — not a security review, not a bug review, not a persona analysis.
**Lens:** New admin opening the UI for the first time. Where do they get lost? Where is the product doing too much? Where is it doing too little?
**Sources read:** `index.html`, `app.js` (9,346 lines), `slurm.js`, `network.js`, `login.html`, `docs/install.md`, `docs/lab-validation-pr5.md`

---

## A. Onboarding and First-Run Experience

### A-1 — Login page gives no hint about default credentials

**Pain Severity:** High

**Where in UI:** `login.html`

**What is wrong:** The login page shows "clustr" as the username placeholder and an empty password field. A new admin who did not read the install guide will try incorrect passwords, or worse, leave the tab open and walk away assuming the server is broken. The bootstrap credentials (`clustr` / `clustr`) are only documented in `docs/install.md §6`. The login page itself carries zero help text about first-time access.

**Operator's expectation:** On a fresh install, some systems display "First time? Use username: admin / password: admin" or show a "default credentials" hint below the form. At minimum, the placeholder text for the password field should acknowledge that a first-run password exists.

**Suggested direction:** Add a subtle helper line below the login form: "First time? Default username is `clustr`. Your password was printed in the server startup log." This disappears once the bootstrap account has set a permanent password (server can expose a flag in an unauthenticated probe endpoint or omit it after `must_change_password` is cleared globally).

---

### A-2 — Getting-started wizard is conditional on BOTH images AND nodes being zero

**Pain Severity:** High

**Where in UI:** `app.js:629–665` (Dashboard, first-deploy wizard)

**What is wrong:** The three-step wizard card on the dashboard only appears when `images.length === 0 AND nodes.length === 0`. The moment a node auto-registers via PXE boot (before any image exists), the wizard disappears. Since nodes self-register when they PXE boot, a new admin who plugs in hardware before building an image will lose the wizard on their first node registration and see the raw stats grid with zeros.

**Operator's expectation:** The wizard should guide until the first successful full deploy cycle is complete, not disappear on the first PXE heartbeat.

**Suggested direction:** Show the wizard until `deploy_verified_booted_at` is non-null on at least one node. If that is too complex to gate on, show it while `images.length === 0` regardless of node count. A partial node inventory with no images is not "done onboarding."

---

### A-3 — Empty-state subtexts are helpful on Images and Nodes, silent on Dashboard

**Pain Severity:** Medium

**Where in UI:** Dashboard (no nodes or images), `app.js:1050–1052`

**What is wrong:** The nodes table on the dashboard shows "No nodes configured — Add a node from the Nodes page" which tells the operator what to click. The images table shows "No images yet — Pull an image from the Images page." Both are fine. However, the primary "Active Deployments" card — the center of gravity on the dashboard — shows `emptyState('No active deployments')` with no subtext and no CTA. This is meaningless to a new admin who doesn't know whether "no active deployments" means everything is fine or nothing has ever run.

**Operator's expectation:** An empty deployments panel should say something like: "No deployments in progress. Trigger a reimage from the Nodes page to start one." Context + action.

**Suggested direction:** Add a subtext and a "Go to Nodes" link to the `emptyState` call for active deployments when total node count > 0. When node count is 0, show "Add a node to get started."

---

### A-4 — Password-change requirement is clear, but the route back is lost

**Pain Severity:** Medium

**Where in UI:** `set-password.html`, `app.js:Auth.boot()`

**What is wrong:** The forced password change flow (`/set-password`) works correctly. However, if a new admin completes the password change and the browser lands on the root `/` route, they hit the dashboard wizard. If they navigate directly to a deep link (e.g., a node they bookmarked before the password change), the `?next=` param is not preserved through the `/set-password` redirect. Operators lose their intended destination.

**Operator's expectation:** Logging in and changing your password should bring you to exactly where you were going.

**Suggested direction:** The `/set-password` flow should read a `?next=` param from the forced-change cookie or URL and redirect to it on success. This is a standard login-flow convention.

---

## B. Where Automation is Absent and Should Be Present

### B-1 — Reseed-defaults is a hidden curl call, not a webui button

**Pain Severity:** High

**Where in UI:** Slurm module, no corresponding button exists

**What is wrong:** The `POST /api/v1/slurm/reseed-defaults` endpoint (D18) resets the Slurm module's config templates back to clustr defaults. This is a critical recovery operation when an operator has corrupted or over-customized configs and wants to start over. It is documented in code (commit `3a708d0`) but has no webui surface. The only way to invoke it is with a raw API call or curl command. A new admin who gets their Slurm config into an unusable state and can't figure out how to recover will assume the product is broken.

**Operator's expectation:** "Restore defaults" is a common enough admin action that every config editor should have it as a button.

**Suggested direction:** Add a "Restore Defaults" button to the Slurm Settings page, guarded by a confirmation modal explaining what it resets. This button should call `POST /api/v1/slurm/reseed-defaults`.

---

### B-2 — Initramfs rebuild is on the Images page, not where operators look for it

**Pain Severity:** High

**Where in UI:** `app.js:1166–1246` (Images page, `_initramfsCard`)

**What is wrong:** The initramfs rebuild button lives inside the Images page in a dedicated card. This is an infrastructural system action, not an image management action. When a new admin is told "your nodes are getting the wrong kernel," the natural first stop is the Nodes page or the Dashboard — not the Images page. The stale-initramfs warning on the Dashboard correctly links back to the Images page, but the relationship between "images" and "initramfs" is not obvious to someone unfamiliar with the architecture.

A secondary problem: the card is always visible at the top of the Images page, consuming significant vertical real estate on every page load even for operators who never think about the initramfs.

**Operator's expectation:** Infrastructure-level actions (rebuild PXE boot environment) belong in a System or Settings section, not on the image catalog page.

**Suggested direction:** Move the initramfs card to a "System" tab in Settings, alongside users and API keys. Keep the stale-initramfs warning banner on the Dashboard and the Images page, but make the rebuild action live somewhere that feels like infrastructure management.

---

### B-3 — Node role assignment after PXE registration is a multi-step manual process

**Pain Severity:** High

**Where in UI:** Nodes page → node detail → Overview tab

**What is wrong:** When a node PXE-boots for the first time, it auto-registers with status "Registered." The admin must then: (1) open the node detail, (2) click Edit in the Overview tab, (3) assign an image, (4) assign a group/role, (5) add SSH keys if not already on the node, (6) save, (7) then trigger a reimage separately. That is six distinct actions across two modals to get from "node appeared" to "deploy started." The lab-validation docs confirm this was the manual flow used in testing.

**Operator's expectation:** When a new node appears, the UI should prompt: "New node registered. Assign an image and deploy?" A toast notification or a highlighted row in the Nodes list with a "Configure and Deploy" CTA would eliminate multiple steps.

**Suggested direction:** When a node has no `base_image_id` (status "Registered" or "Unconfigured"), show an inline "Configure" button in the Nodes list row that opens a mini-wizard: pick image, confirm SSH keys, trigger reimage. Three clicks instead of six manual steps across two pages.

---

### B-4 — Slurm controller dual-role still requires awareness of undocumented behavior

**Pain Severity:** Medium

**Where in UI:** Slurm Settings page, node configuration

**What is wrong:** Per lab-validation-pr5 (Round 2 notes and D17), the controller node automatically gets a `compute` role in addition to `slurmctld` when the Slurm module generates `slurm.conf`. This is now the documented default behavior. However, the UI provides no indication that enabling the Slurm module on a cluster will modify the node topology. A new admin enabling Slurm for the first time will not know the controller will appear as a compute node in `sinfo` until they actually run jobs.

**Operator's expectation:** When configuring Slurm, a preview or summary of what the generated `slurm.conf` will look like — specifically the node/partition layout — before committing.

**Suggested direction:** On the Slurm Settings page, add a "Preview generated config" button that shows the current Slurm module would produce, before any deploy. This is not the same as the Config Files editor (which shows files already written); it should show what would be generated on the next deploy given current settings.

---

### B-5 — No one-click "add to Slurm after deploy" path

**Pain Severity:** Medium

**Where in UI:** Nodes page, Slurm module

**What is wrong:** After reimaging a node, the Slurm config on the controller does not automatically include the new compute node. The operator must: go to Slurm → Config Files → edit `slurm.conf` → add the NodeName line → push the config → scontrol reconfigure. For a platform that automates the OS install, this gap in Slurm lifecycle automation is conspicuous.

**Operator's expectation:** "I reimaged a new compute node. Is it in Slurm automatically?" The answer should be yes, or at least there should be an obvious button.

**Suggested direction:** On the Slurm Sync Status page, surface "Nodes not yet in slurm.conf" — nodes that are deployed and verified but not represented in the Slurm config. Offer a "Add to cluster" button that updates slurm.conf and triggers a Slurm push in one operation.

---

### B-6 — Bundle install after server upgrade is CLI-only

**Pain Severity:** Low

**Where in UI:** No UI surface exists

**What is wrong:** `docs/install.md §8` documents that `clustr-serverd bundle install` must be run after certain upgrades, and that the autodeploy circuit breaker may require a manual `echo 0 > /var/lib/clustr/bundle-install-failures` reset after a failure. Both operations require SSH access to the server. There is no webui equivalent.

**Operator's expectation:** Bundle status and install/upgrade operations should be visible in the UI. An operator who upgraded via the autodeploy mechanism should be able to see whether the new bundle installed successfully without SSHing in.

**Suggested direction:** Add a "Bundle" section under Settings or a System page showing the current installed bundle version, SHA256, and install timestamp. Include a "Re-install bundle" button for recovery.

---

## C. Where Customization is Too Open

### C-1 — Slurm Config Files editor exposes raw `slurm.conf` with no guardrails

**Pain Severity:** Blocker

**Where in UI:** `#/slurm/configs` — the Config Files section in the SLURM nav

**What is wrong:** The Slurm Config Files editor allows free-text editing of `slurm.conf`, `slurmd.conf`, `cgroup.conf`, and other Slurm config files. There is no validation that runs before saving. A new admin who accidentally deletes `SlurmctldHost=` or sets an invalid `AuthType=` will not know until the next deploy fails — and the failure will appear as a mysterious Slurm startup error on the node, not a UI error. The lab-validation rounds (R3-C, R3-D) showed exactly this pattern: subtle config mistakes that only surface on boot.

**Operator's expectation:** Config editors that touch daemon configuration should validate structure before saving. At minimum: detect obviously invalid changes (missing required directives, invalid enum values) and warn before commit. This is what every managed config tool does.

**Suggested direction:** Add a server-side "validate config" step before writing Slurm config files. The API should return validation errors that the UI displays inline. Do not block saving entirely — operators may have deliberate non-standard configs — but warn clearly with a "Save anyway" option for high-confidence errors.

---

### C-2 — Custom Kickstart field in ISO build modal accepts arbitrary text with no syntax help

**Pain Severity:** High

**Where in UI:** Pull modal / Build from ISO modal, `app.js:1484–1490`

**What is wrong:** The "Custom Kickstart / Autoinstall" textarea in the Pull modal accepts raw kickstart or autoinstall YAML with a placeholder of "Paste a custom kickstart or autoinstall config here." There is no syntax guide, no link to kickstart documentation, no validation, and no indication of what the default kickstart template looks like. An operator who pastes a malformed kickstart will not discover the error until an 8–30 minute ISO build fails.

**Operator's expectation:** Either show the default kickstart template so operators can see what they are overriding, or provide a diff/preview mode. At minimum, link to the relevant upstream docs.

**Suggested direction:** Add a "View default template" link that opens a read-only modal showing the generated kickstart for the current distro/role selection. The operator edits a copy, not a blank textarea. Add a warning: "Custom kickstarts are advanced — mistakes will cause silent build failures. The build log shows the error."

---

### C-3 — Custom Variables on nodes are freeform key-value pairs with no documentation

**Pain Severity:** Medium

**Where in UI:** Node detail → Configuration tab → Custom Variables section

**What is wrong:** The Custom Variables section (app.js ~line 5105) allows operators to add arbitrary key-value pairs. There is no in-UI documentation of which variable names are actually consumed by the deploy pipeline, what format values should be in, or what happens to unknown variables. A new admin who sets `CLUSTR_MY_VAR=test` will not know if it does anything.

**Operator's expectation:** A list of supported variable names with descriptions, or at least a link to documentation that explains which variables the initramfs actually reads.

**Suggested direction:** Show a "Supported variables" info link next to the "Add Variable" button that lists the recognized variable names. Unknown variables should be displayed with a warning icon indicating they will have no effect.

---

### C-4 — Network interface configuration is fully freeform: IP CIDR, gateway, DNS

**Pain Severity:** Medium

**Where in UI:** Node detail → Network tab, `app.js:5030–5080`

**What is wrong:** Every network field is a free-text input: IP address (CIDR notation), gateway, DNS. There is no validation that the IP is in the provisioning subnet, no check that the gateway is reachable, no warning if the DNS servers are outside the provisioning network. An operator who types `10.99.0.50` instead of `10.99.0.50/24` for the IP will get a silent configuration error that only surfaces when the node fails to route after deploy.

**Operator's expectation:** CIDR format validation, at minimum. Ideally: auto-populate gateway from the provisioning network configuration, auto-populate DNS from server defaults.

**Suggested direction:** Add client-side CIDR format validation on the IP field. Pre-populate gateway and DNS from DHCP server settings where available. Show a warning if the entered IP is not in the expected provisioning subnet.

---

### C-5 — Disk layout editor on Node Groups is powerful but has no "what will this do" preview

**Pain Severity:** Medium

**Where in UI:** Node Groups → Create/Edit modal, disk layout section (`app.js:7216–7229`)

**What is wrong:** The Node Group disk layout editor allows operators to define custom partition tables (mountpoints, sizes, filesystems). This is a powerful and potentially destructive configuration: a wrong layout will cause deploy to fail or produce an unbootable node. The only guardrails present are: root partition required and only one "fill" partition. There is no preview of what the resulting partition table will look like on the actual disk, and no comparison with the default layout.

**Operator's expectation:** "Inherit from image" should be the visible default with a clear callout explaining what that means. Custom layout should require the operator to explicitly opt in.

**Suggested direction:** Default to "Inherit" with a collapsed "Customize layout" section. When expanded, show a visual disk layout preview rendering the partitions as proportional bars. The default layout (derived from image) should be shown as the baseline.

---

## D. Where Customization is Too Locked

### D-1 — DHCP pool range is not configurable from the UI

**Pain Severity:** High

**Where in UI:** No UI surface

**What is wrong:** The DHCP pool range (`CLUSTR_PXE_RANGE`, default `10.99.0.100–10.99.0.200`) is set only via environment variable at server startup. An operator who needs to adjust the pool for their network — common in any non-default lab or HPC environment — must restart the server with a new env var. There is no way to see or change the current range from the UI.

**Operator's expectation:** The provisioning network configuration (interface, IP range, server IP, subnet mask) should be visible in Settings. Ideally editable without a server restart.

**Suggested direction:** Add a "Provisioning Network" card to Settings > System showing the current DHCP configuration as read-only. Flag this as "requires server restart to change" with a note on where to set the env var. For v1.1, allow runtime reconfiguration via API.

---

### D-2 — Verify timeout window cannot be changed per node

**Pain Severity:** Medium

**Where in UI:** No per-node setting

**What is wrong:** `CLUSTR_VERIFY_TIMEOUT` (default 5 minutes) is a global setting. Large bare-metal nodes with slow disk controllers or complex RAID configurations may need more time to phone home after a deploy. There is no way to give an individual node or node group a longer timeout without changing the global setting for all nodes.

**Operator's expectation:** Verify timeout as a per-node or per-group override is a reasonable operator request, especially for heterogeneous fleets mixing fast VMs with slow bare-metal.

**Suggested direction:** Add an optional `verify_timeout_override` field to node configuration. When set, this overrides the global `CLUSTR_VERIFY_TIMEOUT` for that specific node. Display it on the node's Configuration tab.

---

### D-3 — Log retention parameters are not visible or configurable from UI

**Pain Severity:** Low

**Where in UI:** No UI surface

**What is wrong:** `CLUSTR_LOG_RETENTION` (7 days) and `CLUSTR_LOG_MAX_ROWS_PER_NODE` (50,000) are env-var-only settings. An operator running a heavily-deploying cluster who wants to extend retention to 30 days, or an operator on a small server who wants to reduce the row cap to save space, has no UI path. The Settings page has no "Log Management" section.

**Operator's expectation:** Log retention policy should be visible and ideally tunable in the UI. Even read-only visibility ("Your logs are retained for 7 days, 50,000 rows per node") would help operators understand what they are looking at.

**Suggested direction:** Add a read-only "Retention Policy" display in Settings. For the v1.1 cycle, consider making these runtime-configurable via a Settings > Advanced section.

---

### D-4 — Reimage concurrency cap is global, not per-group

**Pain Severity:** Low

**Where in UI:** Group reimage modal exposes concurrency, but the global cap (`CLUSTR_REIMAGE_MAX_CONCURRENT`) is invisible

**What is wrong:** The group reimage modal lets the operator set concurrency (default 5). But `CLUSTR_REIMAGE_MAX_CONCURRENT` is a global cap that silently clamps any concurrency value above it. An operator who sets concurrency to 20 in the UI does not know it may be clamped to 5 (or whatever the server default is). There is no feedback in the UI when the effective concurrency differs from the requested value.

**Operator's expectation:** If the requested concurrency is clamped by a server limit, the operator should be told: "Requested: 20, Effective: 5 (server limit)."

**Suggested direction:** Expose the effective server max in a tooltip on the concurrency input, fetched from a `/api/v1/system/config` endpoint. Display the actual effective value after submission.

---

## E. Critical Operator Flows — Graded

### E-1 — "I need to add a new compute node"

**Grade: C+**

The happy path works but requires too many steps. After PXE boot: open Nodes, find the new unregistered node (may require scrolling or search), click View, click Edit in Overview, assign image, assign group, set SSH keys if not defaulted, save, then separately trigger reimage. That is 7 steps with no workflow guidance between them. The node list does not highlight "new" nodes that appeared since last visit. There is no node-centric "wizard" flow.

**Key gap:** No "quick configure and deploy" shortcut from the node list. The Allocations tab (newly added) helps identify MAC/IP but does not link directly to a "register this node" action.

---

### E-2 — "A node is unhealthy — what's wrong?"

**Grade: B**

The node detail page has a Logs tab with component-filtered live stream, a reimage history table with phase and error detail, and a "Live" badge showing clientd heartbeat status. The overall diagnostic surface is reasonable. The main gap is that node health state is binary (verified/not verified) — there is no consolidated "health score" or "last known issues" summary at the top of the node detail. An admin must tab through Overview, Logs, and possibly the Deploy history to reconstruct what went wrong.

**Key gap:** No "last failure summary" card at the top of the node detail. An admin opening a failing node should see the most recent error prominently, not buried in the Logs tab.

---

### E-3 — "I want to update Slurm"

**Grade: B-**

The Slurm Upgrades page (`#/slurm/upgrades`) exists in the nav and provides upgrade orchestration. However, navigating to it requires: knowing that "Upgrades" is a sub-item under SLURM in the nav, which is only visible when the Slurm module is enabled. The upgrade flow itself requires understanding what "bundle version" means and how it differs from the Slurm package version. A new admin who just wants to "upgrade Slurm to 24.11" must understand the bundle concept first.

**Key gap:** No "Check for Slurm updates" prominent entry point. The Upgrades page is discoverable only if you know to look for it.

---

### E-4 — "I need to reimage everything"

**Grade: A-**

Bulk reimage is well-implemented: checkboxes in the Nodes list, a floating action bar, image selection in the modal, with dry-run option. Group reimage is also available with concurrency control. This flow is better than most competitors. The minor deduction: there is no "reimage all nodes not yet verified" shortcut — bulk select requires manual checkbox selection, which gets tedious at 50+ nodes.

**Key gap:** A "Select all unverified" or "Select all failed" bulk-select shortcut would save time on large deployments.

---

### E-5 — "I forgot the admin password"

**Grade: D**

The recovery path requires SSH access to the server and raw `curl` commands to the API with an admin API key. The install doc covers this at §6 but the UI itself provides zero recovery path. If an operator forgot both the web password and lost their API key (a realistic scenario for an infrequently-accessed lab cluster), they must SSH in and run `clustr-serverd apikey create`. The login page has no "forgot password" link and no in-product recovery flow.

**Operator's expectation:** Recovery instructions accessible without SSH, or at minimum a "Forgot password?" link on the login page pointing to the docs.

**Suggested direction:** Add a "Forgot password?" link to the login page that links to the recovery documentation. The in-browser error message on failed login ("Invalid credentials") should include a hint: "Contact your clustr administrator or see the recovery guide at [link]." Full self-service password reset is a v1.1 scope item.

---

## F. Dashboard Quality

### F-1 — Dashboard is functional but does not surface anomalies proactively

**Pain Severity:** High

**Where in UI:** `app.js:560–795`

**What is wrong:** The dashboard has four stat cards (images, nodes, active deployments, system health), an active deployments table with live SSE updates, recent images/nodes tables, and a live log stream. This is a solid real-time operational view. However:

- "System Health: Online" is always green as long as the API responds. It does not reflect node health, stale deployments, or failed reimages that happened outside the current session.
- There is no count of "nodes in failed state" or "nodes not reimaged in X days."
- The "Recent Activity" timeline shows image and node creation events — it does not show deployment failures or verify timeouts.
- The stale-initramfs warning banner does appear (via the `staleInitramfsWarning` check), which is the one good proactive anomaly surface.

**Operator's expectation:** The first thing an admin opening the dashboard after a weekend should see is "3 nodes failed their last deploy" or "2 nodes have not been reimaged in 90 days." Instead they see counts and a live log stream that requires them to mentally reconstruct the fleet state.

**Suggested direction:** Add an "Anomalies" or "Alerts" card to the dashboard showing: failed nodes count (clickable, filters the Nodes list), verify timeout count, nodes with no successful deploy in >N days. This is a health summary, not a log dump.

---

### F-2 — System Health stat card shows "Online" with no nuance

**Pain Severity:** Medium

**Where in UI:** `app.js:703–712`

**What is wrong:** The System Health stat card always shows "Online" in green as long as `/api/v1/healthz/ready` returns 200. The `/healthz/ready` endpoint returns structured check results (db, boot_dir, initramfs), but the dashboard card only shows "Online" or (implicitly) nothing when offline. A degraded state — like "initramfs is stale" or "db writable but boot_dir has low space" — is not reflected here.

**Operator's expectation:** "System Health" should show Degraded, Warn, or OK states, and clicking it should show which checks are in which state.

**Suggested direction:** Fetch `/api/v1/healthz/ready` on dashboard load and render the health card as "OK / Degraded / Error" with a breakdown of which checks failed on hover or click.

---

### F-3 — No bundle version or server version displayed anywhere in the UI

**Pain Severity:** Medium

**Where in UI:** No UI surface

**What is wrong:** There is no visible display of the clustr server version, the installed Slurm bundle version, or any other version information in the web UI. An admin who upgrades the server and wants to confirm the new version is running must SSH in or check the startup logs. There is a "Settings" page and an "About" tab reference in the code (`Pages._settingsAboutTab`), but the code shows it returns a placeholder ("Server info will appear here in a future update," commit `app.js:7336` area).

**Operator's expectation:** Every admin tool shows version information prominently. At minimum: server version, bundle version, build date.

**Suggested direction:** Populate the Settings > About tab with: server version (from a `GET /api/v1/system/version` endpoint or similar), installed bundle version, build date, uptime, and a link to the changelog.

---

## G. Information Architecture

### G-1 — "Allocations" nav label is not operator-natural

**Pain Severity:** Medium

**Where in UI:** Sidebar nav, `index.html:72–79`

**What is wrong:** The nav item is labeled "Allocations" with a WiFi/signal icon. This is not what most HPC operators call DHCP leases. In the HPC world, "allocations" most commonly refers to Slurm job allocations or compute resource allocations. The actual content of the page is a DHCP lease/MAC-to-IP table, which operators would call "DHCP Leases," "Network Map," or "Host Table."

**Operator's expectation:** The name should match what operators call the thing. "DHCP Leases" or "Network Map" would be clearer. The WiFi icon is reasonable for network-related content.

**Suggested direction:** Rename to "DHCP Leases" or "Network Map." If the team prefers a shorter label, "Hosts" or "Leases" is clearer than "Allocations."

---

### G-2 — The SLURM section has two types of "Settings" — one at the bottom, one implied by the whole section

**Pain Severity:** Medium

**Where in UI:** Sidebar nav, SLURM section (`index.html:120–184`)

**What is wrong:** The SLURM nav section has: Config Files, Scripts, Sync Status, Push, Builds, Upgrades, and a final item labeled "Settings" with a disabled badge. When the Slurm module is disabled, only "Settings" is visible. When enabled, "Settings" is the last item in a list of six other items. The label "Settings" for the Slurm enable/configure screen is confusing because: (1) there is also a global "Settings" link in the sidebar footer, and (2) the Slurm section itself IS essentially "settings" for the Slurm module.

**Operator's expectation:** The module enable/configure screen should be labeled something that distinguishes it from the sub-feature nav items: "Slurm Setup," "Module Config," or "Enable Slurm."

**Suggested direction:** Rename the bottom Slurm nav item to "Module Setup" or "Configure." Apply the same renaming to the LDAP section's bottom "Settings" item.

---

### G-3 — The "SYSTEM" nav section is hidden by default and only appears when System Accounts are enabled

**Pain Severity:** Medium

**Where in UI:** `index.html:98–115`, hidden via `display:none` on `nav-system-section`

**What is wrong:** The System Accounts and Groups nav items live under a "SYSTEM" section header that is hidden by default and only shown when the System Accounts module is enabled (`SysAccountsPages.bootstrapNav()`). An admin who wants to set up system accounts must first find the feature — which is not discoverable from the nav because the nav entry does not exist yet when they open the UI. There is no "System" entry point that an admin would click to discover the System Accounts module.

**Operator's expectation:** Module discovery should work in the opposite direction: the nav should always show a "System" section with an "Enable System Accounts" link. Enabling it expands the sub-items.

**Suggested direction:** Always show the SYSTEM section in the nav, even when the module is disabled, with a single "System Accounts" item that leads to a module-enable/getting-started page. Same pattern should apply to LDAP and Network.

---

### G-4 — No breadcrumb or context header when viewing nested pages

**Pain Severity:** Low

**Where in UI:** Image detail, Node detail, Node Group detail, Slurm Config editor

**What is wrong:** When an operator navigates from "Nodes" → a specific node → "Edit Node" → a nested tab (Network, Configuration), the page header shows the node name but there is no breadcrumb showing the path back. Pressing the browser back button works, but clicking the node name (if it were a link) does not exist — you have to navigate the sidebar.

**Operator's expectation:** Breadcrumbs like "Nodes > slurm-compute > Network" help operators stay oriented in large fleet UIs.

**Suggested direction:** Add a breadcrumb component to detail pages. This is a low-priority polish item but contributes significantly to first-time navigation confidence.

---

### G-5 — The Settings page is reachable only from the gear icon in the sidebar footer

**Pain Severity:** Low

**Where in UI:** Sidebar footer, gear icon

**What is wrong:** Settings (Users, API Keys, Logs, About) is accessed via a small gear icon at the bottom of the sidebar. There is no "Settings" label visible by default — only an icon. New admins may miss this entirely if they are scanning for text labels. There is also no keyboard shortcut or command palette for navigation.

**Operator's expectation:** Settings should either have a text label in the sidebar nav or have the gear icon accompanied by a "Settings" tooltip/label on hover that is more visually prominent.

**Suggested direction:** Add a visible "Settings" nav item in the sidebar (with icon) alongside the other sections, or at minimum add a tooltip that says "Settings" and appears on hover without requiring the user to hover to discover it.

---

## Closing: If We Only Fix 5 Things in v1.1 — Pick These

These are the five changes that would have the largest impact on new admin experience, ranked by operator cost of hitting them:

**1. Add a "Configure and Deploy" shortcut from the Nodes list when a new node appears (B-3)**

The current 7-step flow to go from "node appeared" to "deploy started" is the single biggest friction point a new admin hits after the first PXE boot. One inline CTA in the node list would cut this to 3 steps.

**2. Login page: show default credentials hint on first-run installs (A-1)**

Every minute a new admin spends trying to log in with wrong credentials is goodwill burned. This is a two-line change with a massive return in first-impressions.

**3. Reseed-defaults: add a "Restore Defaults" button to the Slurm Settings page (B-1)**

The recovery path from a misconfigured Slurm setup requires knowing that a hidden API endpoint exists. A visible button changes this from a support ticket to a self-service recovery.

**4. Dashboard anomaly card: show failed nodes count and verify timeout count (F-1)**

The dashboard gives real-time deploy progress but no persistent fleet health state. An admin returning after time away should immediately know "3 nodes are in failed state." This is the difference between a dashboard and a wall clock.

**5. Rename "Allocations" to "DHCP Leases" and rename SLURM "Settings" to "Module Setup" (G-1, G-2)**

Two nav label changes that reduce the cognitive overhead of discovering what each section does. Low cost, high clarity gain.

---

## What Could Not Be Evaluated

- **Live behavior of the Slurm Config Files editor validation feedback** — reviewed from code only; could not observe the actual error experience on save.
- **The "About" tab content** — the code reference (`Pages._settingsAboutTab`) exists but the implementation was not legible in the code snapshot; the "Server info will appear here" placeholder was noted but the current state of the tab could not be fully confirmed from code alone.
- **Network module (Switches, Profiles) UX** — the nav items are hidden (`display:none`) and require the network module to be enabled; the `network.js` file was not read in full. Richard should evaluate whether the network profile UX follows the same patterns flagged in C-4.
- **Password recovery flow in `set-password.html`** — the HTML was not read; recovery flow evaluated from `auth.boot()` logic only.
