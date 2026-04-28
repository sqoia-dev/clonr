# clustr Web UI Review — Personas and Strategic Positioning

**Date:** 2026-04-27
**Reviewer:** Monica (Strategy / Investor Relations)
**Scope:** Persona discovery, strategic positioning, and persona-segmented opportunity mapping.
**NOT in scope:** Security findings (Gilfoyle's lane), implementation bugs (Dinesh's lane),
operator paper-cuts (Jared's lane).
**Sources read:** `index.html`, `app.js` (8 kLOC, read by section), `api.js`, `slurm.js`,
`ldap.js`, `network.js`, `sysaccounts.js`, `docs/decisions.md`, `docs/rbac.md`,
`docs/slurm-build-pipeline.md`, `docs/webui-review.md` (Gilfoyle's review),
`docs/90-day-sprint-plan.md`.

---

## The Central Strategic Fact

Before any persona discussion: clustr's webui today is designed for and by one person
— the HPC sysadmin who installs, configures, and operates the cluster. Every nav item,
every modal, every action surface assumes that person. That is not a bug; it is an
honest reflection of where the product is at v1.0. The strategic question is whether
that single-persona shape is a durable competitive position or a temporary state that
will cost market share as the product scales.

The answer from the evidence: it is temporary. The Slurm module, the LDAP module, and
the group-scoped RBAC model (D1) already create the data structures for a multi-persona
product. The webui has not caught up.

---

## Section A — Persona Discovery

### Persona 1: HPC Sysadmin (Primary Buyer, Primary Champion)

**Who they are:** The person who installs clustr, maintains the cluster, owns Slurm,
manages node lifecycle, and is accountable if the cluster goes down. Typically a staff
engineer at a research university, national lab, or government HPC center.

**Top 3 jobs:**
1. Deploy and re-deploy nodes reliably during maintenance windows without guessing.
2. Push Slurm config changes to the full fleet and know every node converged.
3. Diagnose a failed deploy within 5 minutes without SSHing into the problem node.

**What they see today:** The full product. Dashboard, Images, Nodes, Deployments,
Allocations, Switches, Profiles, System Accounts, Groups, Slurm (Config, Scripts, Sync,
Push, Builds, Upgrades), LDAP (Users, Groups), Settings. They see everything and can
do everything.

**What they need that is missing:** Scheduled deploy windows (the `scheduled_at` API
field exists; the UI never exposes it). A column-sortable, searchable node list that
survives 200 nodes. Audit trail with user attribution (currently all reimages record
`"api"` as the actor per the sprint plan's S1-11 finding). Initramfs staleness banner
visible without navigating to Images.

**What they should be shielded from:** Nothing. This is their product. The problem is
that everyone else also sees the sysadmin's full surface, which makes clustr feel
intimidating to every other persona.

**Missing persona-specific affordances:** A "cluster health at a glance" view that
summarizes: nodes unreachable, Slurm sync drift count, initramfs stale status, active
deploys. The current dashboard mixes operational status (Active Deployments, Live Log
Stream) with catalog content (Recent Images, Recent Nodes) in a way that serves no
single persona well.

---

### Persona 2: Junior Ops / On-Call Operator

**Who they are:** The person who keeps the cluster running during nights and weekends.
They did not design the cluster, do not modify Slurm configs, and should not be able to
accidentally nuke a node group. They handle one thing: "a node is down, get it back up."

**Top 3 jobs:**
1. Identify which node is failing and why without having to understand the full stack.
2. Trigger a re-deploy on a specific node within their assigned group.
3. Confirm the deploy succeeded and hand off to the next shift.

**What they see today:** The full product. Every destructive action (delete node, delete
image, group reimage, Slurm push, LDAP admin repair) is visible and accessible. The RBAC
model (D1) defines an `operator` role scoped to NodeGroups, but the UI does not actually
change shape for operators. A logged-in operator sees the Slurm and LDAP nav sections,
the System Accounts section, and the Settings page — all of which they cannot use and
should not need to see.

**What they need:** A filtered node view showing only their assigned group's nodes. A
simple "Is it deployed? Is it reachable?" status. One-click re-deploy with confirmation.
A log view scoped to recent activity on their nodes.

**What they should be shielded from:** Slurm Config Files, Slurm Scripts, Slurm Push,
LDAP, System Accounts, Images (creation/deletion), Node Groups (management), all Settings
except password change.

**Missing affordances:** The `operator` role exists in the data model and the API enforces
group-scoped mutations. The UI does not honor this. An operator logs in and sees the
identical interface as the admin, including tabs and sections that will return 403 when
they click them. The first experience of a non-admin user in clustr today is a product
that shows them everything and then silently refuses when they try to do half of it.

---

### Persona 3: HPC Researcher / Scientist

**Who they are:** A graduate student, postdoc, or research scientist who submits Slurm
jobs to the cluster. They do not install software, do not manage nodes, and do not want
to think about provisioning. They want to know: "Is my job running? Is the cluster up?
When will my queue position be reached?"

**Top 3 jobs:**
1. Check if the cluster (or their specific partition) is operational before submitting.
2. Know if a node failure is affecting their running job.
3. Understand what image/environment is deployed on the nodes they use.

**What they see today:** Nothing. Researchers have no access to clustr at all. There is
no public-facing status page, no read-only researcher view, no job queue visibility.
The `readonly` role defined in `docs/rbac.md` can view all nodes, images, groups, and
logs — but that is still the full sysadmin surface area, just without mutation buttons.
A researcher logging into clustr with a `readonly` account would see raw node MAC
addresses, disk layouts, LDAP configuration status, and Slurm config files — none of
which is meaningful to them.

**What they need that does not exist:** A status surface that answers "Is partition X
up? How many nodes are available? My job is running on node Y — is it having deploy
issues?" This is a fundamentally different question than what the sysadmin dashboard
answers.

**What they should be shielded from:** Everything the sysadmin sees. Node internals, image
management, Slurm config editing, LDAP, power control, network profiles. A researcher
should not be able to even navigate to those pages.

**Missing affordances:** A cluster status view scoped to partitions and availability.
A "my job's node" lookup. A self-service password change for their HPC account (the LDAP
module manages HPC accounts; a researcher should be able to reset their own password
without asking the sysadmin). None of this exists.

---

### Persona 4: PI / Research Group Lead

**Who they are:** The principal investigator who owns the research allocation on the
cluster. They got the grant, they are accountable for how compute is used, and they
occasionally field questions from the IT director about whether the cluster spend is
justified. They are not technical in the sysadmin sense.

**Top 3 jobs:**
1. Know if their group's allocation is being utilized or wasted.
2. Know if their students can actually access the cluster and are not being throttled.
3. Answer "is the cluster healthy?" for a department meeting without calling the sysadmin.

**What they see today:** Nothing. Same situation as the researcher — no PI-specific
surface exists. The audit log (admin-only, per `docs/rbac.md`) contains the data that
would answer utilization questions, but it is locked behind admin access and formatted
for forensics, not reporting.

**What they need:** A group-level utilization summary. "Nodes assigned to my group:
N. Nodes currently deployed with a ready image: M. Node reimages this month: K."
No raw technical data. No log streams.

**What they should be shielded from:** Everything. Node internals, network config, Slurm
config, LDAP administration, image management.

**Missing affordances:** A dashboard scoped to a NodeGroup with utilization metrics
and a node health summary expressed in non-technical language. This does not exist and
cannot be assembled from the current data model without adding a utilization event table.

---

### Persona 5: IT Director / Department Leadership

**Who they are:** The person who approved the budget for the cluster and needs to justify
the expense. They interact with clustr once a quarter, when the CFO asks about server
costs, or when the sysadmin requests funding for more nodes.

**Top 3 jobs:**
1. Show a utilization report that demonstrates the cluster is earning its keep.
2. Understand the risk profile — when was the last major outage, how long did it take to
   recover?
3. Know whether clustr itself (the management tool) needs an upgrade or represents a
   security liability.

**What they see today:** Nothing. No reporting surface exists.

**What they need:** An export-friendly summary. Uptime over the last 30/90 days.
Node count over time. Deploy success rate. These are answerable from the existing DB
tables (`reimages`, `node_logs`, `audit_log`) but require aggregation queries and a
read-only reporting endpoint that does not exist.

**What they should be shielded from:** Everything operational.

**Missing affordances:** A reporting API and a corresponding UI surface. Not in scope
for v1.0 or v1.1. This persona only becomes relevant when a paying institutional customer
asks for it as a procurement requirement.

---

### Persona 6: External / Federated User

**Who they are:** A collaborator from a partner institution — another university or national
lab — who has been given temporary access to run jobs on the cluster during a joint project.

**Top 3 jobs:**
1. Submit jobs and check their status without needing a full account on the home institution's
   LDAP.
2. Know what software environments (images) are available and which partitions they can use.
3. Self-service credential management for their temporary access.

**What they see today:** Nothing. Federated identity (OIDC/SAML) is explicitly deferred to
v1.1 per D1. The current auth model is session cookies + API keys, no external IdP.

**What they need:** This persona is not viable in v1.0 and arguably not viable until v1.2+.
OIDC support and a federated user portal are prerequisites. Flagging here because the
HPC market increasingly requires cross-institutional collaboration workflows; ignoring this
persona is a deliberate and bounded decision, not an oversight.

**Strategic note:** The decision in D1 to defer OIDC/SAML until a customer requires it is
correct stage-appropriate discipline. But the founder should be aware that larger
institutional deals (50+ nodes) will almost always have an SSO requirement. When the first
institutional LOI arrives, OIDC becomes a 90-day priority, not a v2.0 discussion.

---

## Section B — Current WebUI Audit Through the Persona Lens

### Navigation: Who Is It For?

The sidebar enumerates 16 nav destinations:

| Nav Item | Persona 1 | Persona 2 | Persona 3 | Persona 4 | Persona 5 |
|---|---|---|---|---|---|
| Dashboard | Serves (partially) | Should see (simplified) | Should not see | No | No |
| Images | Serves | No | No | No | No |
| Nodes | Serves | Serves (scoped) | No | No | No |
| Deployments | Serves | Serves (scoped) | No | No | No |
| Allocations | Serves | Serves | No | No | No |
| Switches | Serves | No | No | No | No |
| Profiles | Serves | No | No | No | No |
| System / Accounts | Serves | No | No | No | No |
| System / Groups | Serves | No | No | No | No |
| Slurm / Config Files | Serves | No | No | No | No |
| Slurm / Scripts | Serves | No | No | No | No |
| Slurm / Sync | Serves | Partial | No | No | No |
| Slurm / Push | Serves | No | No | No | No |
| Slurm / Builds | Serves | No | No | No | No |
| Slurm / Upgrades | Serves | No | No | No | No |
| LDAP / Users | Serves | No | No | No | No |
| LDAP / Groups | Serves | No | No | No | No |
| Settings | Serves | Password only | No | No | No |

The answer to "who is this for?" is almost universally "Persona 1." The RBAC model (D1)
defines three roles. The UI currently serves one role with content. An `operator` user who
logs in today sees the same 16 nav items as the `admin` and encounters 403 errors on most
of them, with no explanation.

### What Role-Gating Actually Exists in the UI Today

From reading `index.html` and `app.js`:

- `nav-network-section` is hidden by default and shown only when admin access is confirmed
  (`index.html:82`, JS post-load). This is the only nav section with role-gating.
- `nav-system-section` (Accounts + Groups) is similarly hidden and gated on admin.
- Slurm and LDAP sections are gated on their respective modules being _enabled_, not on
  the user's role. An `operator` sees the full Slurm and LDAP nav if those modules are on.
- The Settings page gates the "Users" and "API Keys" tabs on admin role (`app.js:7320`).
  That is the sum total of persona-aware UI behavior today.

The practical result: a `readonly` user or `operator` user who logs in today receives an
interface shaped for the admin. The sidebar shows them 16 nav items. They will click
things that fail. The product does not tell them why. This is not a UX paper-cut; it is
a strategic trust gap. If clustr's first non-admin user is a junior ops person or a
researcher given a `readonly` account, their first experience is a product that shows
promises it cannot keep.

### Dashboard Content Analysis

The dashboard renders: stat cards (Total Images, Nodes, Active Deployments, System Health),
Active Deployments table, Recent Images, Recent Nodes, Recent Activity timeline, Live Log
Stream.

For Persona 1 (sysadmin): the Active Deployments table and Live Log Stream are the two
operationally critical elements. Everything else is navigation to other pages.

For Persona 2 (operator): the Active Deployments table is useful if scoped to their group.
The Live Log Stream is noise at their level. Recent Images is irrelevant — they don't manage
images.

For Personas 3-5: the dashboard is entirely the wrong information architecture. A researcher
wants partition health. A PI wants group utilization. An IT director wants a 90-day summary.
None of these are on the dashboard.

The dashboard is currently an admin tool that has been given the word "Dashboard" on the
assumption that everyone uses the product the same way. That assumption holds at v1.0.
It will not hold when the first institutional operator tries to give a `readonly` login to
a researcher or a PI.

---

## Section C — Strategic Gaps and Opportunities

### Gap 1: The UI Shape Does Not Match the RBAC Model

**Strategic Severity: Foundational**

The data model supports three roles. The business rationale for those roles (D1 in
`decisions.md`) is explicitly HPC team structure: "HPC clusters often have dedicated teams
per sub-cluster: a GPU team, a storage team, a compute team. Group-scoped operators let
each team manage their own nodes." That is a multi-persona argument. The UI has not
delivered on it.

This matters for positioning. When a sysadmin evaluates clustr for a 50-node institutional
cluster, they are not the only user. They have a team. They need to hand off on-call duties.
They need to give researchers a way to check cluster status without calling them. If the
pitch is "turnkey HPC platform" but every user gets the sysadmin UI, the sysadmin will
ask: "How do I give my grad students a read-only view?" The current answer is "give them
an account and hope they don't click anything dangerous." That is not a sellable answer for
institutional procurement.

The fix is not a full multi-persona portal (that is v2.0 territory). It is role-aware nav
and a role-aware dashboard — show operators their scoped node list, hide the Slurm/LDAP
management sections from non-admins, give readonly users a clean status view. The data
model already supports this; the UI just needs to respond to the role returned by
`/api/v1/auth/me`.

### Gap 2: The Researcher Has No Surface

**Strategic Severity: High**

The HPC researcher persona has zero coverage. They cannot log in, check status, or manage
their HPC account. This persona represents the majority of human beings who interact with
an HPC cluster. Sysadmins are 1-5 people. Researchers are 20-500.

The strategic opportunity is not to build a Slurm job scheduler (that is XSEDE/Open
XDMoD/XDMOD territory). It is to build a thin status surface: "Is my partition up?
What image is deployed on the compute nodes? Can I reset my password?" This is
differentiated because Bright Computing and xCAT do not offer this surface — they are
purely sysadmin tools that assume researchers interact via SSH and Slurm CLI only.

A researcher-facing status page (even read-only, no jobs) is a genuine wedge. It makes
clustr the answer to "how do I give my researchers cluster visibility without giving them
SSH to the management server?"

### Gap 3: The Trust Story Is Invisible in the UI

**Strategic Severity: High**

clustr's positioning narrative — open source, self-hosted, signed bundles, zero-egress —
is documented in `docs/slurm-build-pipeline.md` and internal planning docs. None of it
appears in the product UI.

A sysadmin evaluating clustr for a production cluster wants to know: "Can I trust this
tool with my nodes?" The trust signals that exist (GPG-signed Slurm bundles, AES-256-GCM
credential encryption, per-node scoped API keys, HMAC session cookies) are buried in
docs or entirely invisible. The Settings About tab has placeholder text. The first-deploy
wizard on the dashboard ("Getting Started") says nothing about security posture.

This is a positioning loss. Bright Computing and xCAT have no meaningful open-source story.
clustr does. But if the UI never surfaces the trust signals — "your data never leaves your
cluster," "Slurm packages are signed by clustr's key and verified on install," "credentials
are encrypted at rest" — the positioning advantage is invisible to the evaluator who only
reads the UI.

Concrete opportunities: a Settings → About tab that lists the security properties of the
running instance. A "verified" badge on the Slurm module showing the installed bundle's
GPG signature status. A one-line note in the Initramfs card explaining that initramfs
content is built and controlled by the operator, not fetched from a CDN.

### Gap 4: Competing Tools and the Persona Gap

**Strategic Severity: Medium**

xCAT (IBM), Warewulf (OpenHPC), and Bright Computing are the primary competitors.

- **xCAT** is single-persona (sysadmin only), CLI-first, no web UI worth mentioning.
  clustr's web UI is already a differentiator.
- **Warewulf** is open source, community-maintained, no persistent web UI at all. clustr
  is already ahead here.
- **Bright Computing** has a web UI (BCM) with multi-persona awareness — admins, operators,
  and a "user portal" for researchers. It is commercial, expensive, and complex. clustr
  cannot match its feature depth, but clustr can match and exceed its persona segmentation
  clarity at a fraction of the complexity.

The competitor that poses the most strategic threat is Bright Computing's user portal
concept. If an institutional buyer is deciding between Bright and clustr, and Bright offers
a researcher portal while clustr offers "give them a readonly account and see everything,"
Bright wins that conversation. Building even a minimal researcher-facing surface in v1.2
preempts that objection.

### Gap 5: Multi-Tenancy and the Federated User Persona

**Strategic Severity: Defer**

The current data model is single-organization. NodeGroups are org-wide. LDAP is a single
directory. There is no tenant isolation layer.

The federated user persona (Persona 6) requires either: (a) multi-tenant data isolation,
or (b) OIDC federation with a trusted external IdP, or (c) both. Neither is on the v1.0
or v1.1 roadmap, and correctly so.

The founder should track this: if the first institutional customer is a national lab or a
university consortium, they will ask for cross-institutional user access. That question
arrives with a request for OIDC (D1 re-decision trigger: "First design partner explicitly
requires SSO"). At that point, the federated persona becomes a v1.2 priority. Until then,
it is a known gap with a documented exit path.

---

## Section D — v1.1+ Persona-Segmented Opportunities

### Horizon 1: v1.1 (4–8 weeks post v1.0)

**Role-aware nav (operator and readonly mode)**

The single highest-leverage persona change available. When a user logs in with `operator`
or `readonly` role, hide Slurm Config/Scripts/Push/Builds/Upgrades, LDAP, System Accounts,
Network Switches/Profiles from the sidebar. Show only: Nodes (scoped to their group for
operators), Deployments (scoped), Allocations. For readonly, add a cluster status card.

This requires no backend changes. The role is already returned by `/api/v1/auth/me`.
The nav visibility logic already exists for the network and system sections. Extending it
to cover the full nav is a single sprint task.

Impact: the `operator` account becomes usable for a junior ops handoff. The `readonly`
account becomes usable for a researcher or PI who wants status visibility.

**Operator-scoped dashboard**

When the logged-in role is `operator`, render a different dashboard: "Your Groups" card
showing the groups they are assigned to, node health within those groups, recent deploys.
Replace the admin's "Recent Images" and "Live Log Stream" (which the operator cannot manage)
with group-relevant status.

**Trust signal surface in Settings → About**

Add a structured "Security" section to Settings that lists: encryption status (LDAP/BMC
credentials encrypted: yes/no), session configuration (HMAC, 12h TTL), Slurm bundle
provenance (GPG verified: yes/no, bundle version, signing key fingerprint), API key scoping
model. This converts invisible security properties into visible trust signals for the
evaluating sysadmin.

### Horizon 2: v1.2–1.3 (2–4 months post v1.0)

**Researcher cluster status view**

A minimal read-only page accessible to users with a new `viewer` role (more restricted than
`readonly` — only sees partition-level cluster status, not node internals). Shows: partition
health (online/offline), node count per partition, recent deploy activity summary, LDAP
account self-service (change own password). No images, no node details, no config.

This is the single feature that most directly competes with Bright's user portal concept
and creates a wedge against xCAT/Warewulf for institutional deals.

**PI / group lead dashboard**

A NodeGroup-scoped reporting view: utilization by month, deploy success rate, node health
history. Requires adding a `utilization_events` table or aggregating from the existing
`reimages` and `audit_log` tables. Read-only, exportable to CSV.

**Scheduled deploy windows in the UI**

The `scheduled_at` field already exists in the API (`reimage.go:105`). Surfacing it in
the reimage modal — a datetime picker with timezone display — directly serves Persona 1's
need for maintenance-window deploys and signals to institutional buyers that clustr
understands production HPC operations.

### Horizon 3: v2.0+ (6+ months post v1.0)

**Separate flows per persona**

A `/portal` route for researchers and PIs, distinct from `/admin` for sysadmins and
operators. This is the architectural split that BCM and similar enterprise tools use. It
allows separate navigation architectures, separate auth flows (OIDC for researchers, local
sessions for sysadmins), and separate feature velocity.

This is not a routing trick — it requires design work, an OIDC auth path (D1 re-decision
trigger), and a clear product decision about whether clustr is one product or two. Defer
until at least one institutional customer is on a signed LOI and explicitly requires the
researcher portal.

**Multi-tenant data isolation**

Required for federated/external users. Requires schema changes (tenant_id on most tables),
per-tenant NodeGroup scoping, and OIDC federation. This is a major architectural undertaking
that should not be started until the business case is proven by a paying customer.

---

## Section E — Persona × Coverage Matrix

Rows = personas. Cols = key UI surfaces. Cells:
- **S** = Served (the surface exists and is useful for this persona)
- **P** = Partial (the surface exists but is incomplete or wrong-shaped for this persona)
- **U** = Unmet (the persona needs this but it does not exist)
- **N** = Not applicable (this persona should not see this surface)

| Surface | P1: Sysadmin | P2: Jr Ops | P3: Researcher | P4: PI/Lead | P5: IT Director |
|---|---|---|---|---|---|
| Dashboard (admin view) | S | P (sees too much) | N | N | N |
| Cluster status view | U | P | U | U | U |
| Images — catalog | S | N | N | N | N |
| Images — build/import | S | N | N | N | N |
| Nodes — full list | S | P (scoped) | N | N | N |
| Nodes — group-scoped | S | U | N | N | N |
| Deployments — all | S | P (scoped) | N | N | N |
| Deployments — group-scoped | S | U | N | N | N |
| Network Allocations | S | P | N | N | N |
| Network Switches/Profiles | S | N | N | N | N |
| Slurm — config/scripts/push | S | N | N | N | N |
| Slurm — sync status | S | P | N | N | N |
| Slurm — upgrade mgmt | S | N | N | N | N |
| LDAP — user/group mgmt | S | N | N | N | N |
| LDAP — self-service password | N | P | U | N | N |
| System accounts/groups | S | N | N | N | N |
| Audit log | S | N | N | N | N |
| Utilization reporting | U | N | N | U | U |
| Trust/security posture | U | N | N | N | P |
| Settings — all | S | N | N | N | N |
| Settings — password only | N | U | U | N | N |

**Matrix dimensions: 5 personas x 20 surfaces = 100 cells.**
**Served: 28. Partial: 12. Unmet: 12. Not applicable: 48.**

The 28 served cells are almost entirely Persona 1. The 12 unmet cells are concentrated in
Personas 2–4, with the most acute gaps in the researcher and operator rows.

---

## Section F — If clustr Were the Obvious Turnkey HPC Platform

If clustr were the obvious choice for any HPC team evaluating provisioning tools in 2026,
here is what each persona would expect on day 1:

**Sysadmin:** What clustr already delivers, plus a searchable node list that works at
200 nodes, a scheduled deploy window in the reimage modal, and visible trust signals (GPG
bundle verification, encryption status) in the Settings About tab.

**Junior Ops:** Login shows only their assigned group's nodes. Sidebar has three items:
Nodes, Deployments, and a password change under their user avatar. They cannot accidentally
delete an image or push a Slurm config. The on-call handoff is safe to do.

**Researcher:** A read-only status page they can bookmark. "Partition compute is up. 48
of 50 nodes are deployed and available. Your HPC account: active. Password last changed
30 days ago." A "Change Password" button that hits the LDAP self-service endpoint. No
technical infrastructure visible.

**PI:** A monthly email or exportable report: "Your group ran X deploys, Y% succeeded,
average node uptime Z%." Or, if they log in, a single-page view of their NodeGroup's
health. No config management, no log streams.

**IT Director:** A read-only reporting endpoint or a quarterly summary export. Nothing
more.

The gap between that vision and today is not one sprint. It is a phased product roadmap.
The v1.1 role-aware nav and operator-scoped dashboard are the first two moves. The
researcher status view is the v1.2 move that creates the competitive moat. Everything else
follows when a paying customer pulls it forward.

---

## Strategic Recommendation

The single most important persona-segmented move available to clustr before any external
customer installs v1.0 is this: make the UI visibly honor the role the user logged in
with.

Right now, the RBAC model is invisible from the user's perspective. An operator logs in
and sees a product that is not for them. A readonly user logs in and is confused. This
creates a trust gap with the first sysadmin who evaluates clustr for a team installation
— the moment they try to give a junior ops person an operator account, they discover that
the UI exposes everything and enforces nothing visibly.

The v1.1 role-aware nav task (hide Slurm/LDAP/System from non-admins; scope Nodes and
Deployments to group membership for operators) requires no backend changes, no schema
changes, and no framework migration. The role is in the `/api/v1/auth/me` response. The
JS nav show/hide logic is already written for the network and system sections. This is a
frontend-only change that converts the RBAC model from "technically correct" to "visibly
trustworthy."

That single change, shipped in v1.1, transforms clustr's institutional pitch from "we have
RBAC in the API" to "my junior ops person logs in and sees exactly what they need to do
their job." That is the difference between a feature checkbox on a procurement form and
a reason to choose clustr over xCAT.

---

## Items That Could Not Be Evaluated Without Product Owner Input

1. **Researcher portal priority relative to operator-scoped nav.** Both are v1.1 candidates.
   If the first design partner signs and turns out to be a university with 50+ researchers
   on the cluster, the researcher status page becomes the top ask. If the first customer is
   an AI/ML lab with a 3-person ops team, operator-scoped nav is the top ask. The priority
   ordering depends on which segment the first customer comes from.

2. **LDAP self-service scope.** The LDAP module manages HPC accounts. A researcher
   self-service password change is a natural v1.2 feature, but it requires defining whether
   clustr serves researchers directly or only through sysadmin delegation. This is a product
   boundary decision, not an implementation question.

3. **Reporting data model.** Utilization reporting for PIs and IT directors requires either
   a dedicated aggregation table or periodic rollup queries against `reimages` and
   `audit_log`. The right answer depends on what data the first institutional customer
   actually needs. Do not build a reporting schema speculatively — get a paying customer
   to define the first three metrics they want and build exactly those.
