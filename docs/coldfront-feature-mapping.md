# ColdFront Feature Mapping — clustr Strategic Analysis

**Date:** 2026-04-27
**Reviewer:** Monica (Strategy / Investor Relations)
**Source:** ColdFront docs fetched from https://docs.coldfront.dev/en/stable/ and
GitHub repository https://github.com/ubccr/coldfront
**Context:** Founder directive to map ColdFront's feature surface to clustr and
evaluate whether absorbing the best ~30 features positions clustr as a "powerhouse"
platform combining node provisioning + resource allocation management.
**NOT in scope:** Implementation design (Richard owns integration into sprint plans),
code copying (ColdFront is AGPLv3; clustr must study, not copy).

---

## What ColdFront Is (One Paragraph)

ColdFront is an open-source HPC resource allocation management system built in
Python/Django by the Center for Computational Research at the University at Buffalo,
released under AGPLv3. It is the authoritative source of truth for who is allowed to
use which HPC resource, for how long, and why. It collects PI/project/grant/publication
data to justify compute spend to center directors and funding bodies. It does not
provision nodes, manage images, configure Slurm at the OS level, or handle PXE boot.
Its plugins bridge allocation decisions into backend systems (Slurm sacctmgr,
FreeIPA HBAC, OpenLDAP groups, OnDemand portal links). The typical deployment is:
ColdFront says "this user is in this project with this allocation" — then plugins
make that true in the actual systems. clustr today handles the "actual systems" side
(node provisioning, Slurm install, LDAP account management) without any allocation
governance layer sitting above it.

---

## Phase 1 — ColdFront Feature Inventory

### Core Platform Features

| # | Feature | Purpose | Primary Persona | Workflow |
|---|---|---|---|---|
| CF-01 | Project management | Container entity: title, description, field of science, PI assignment, status (new/active/archived) | PI / Admin | PI creates project; admin approves; annual review cycle |
| CF-02 | Allocation management | Grants specific users access to a specific resource for a bounded time period | PI / Admin | PI requests allocation; admin approves/denies; allocation expires with notification |
| CF-03 | Allocation expiration + renewal | Mandatory expiration dates on all allocations; email notifications at 60/30/7 days; renewal workflow | Admin / PI | Automated notifications; PI submits renewal request; admin approves |
| CF-04 | Allocation attributes (custom) | Custom key-value fields on allocations (e.g. slurm_account_name, core_hours, QOS) that propagate to backend systems | Admin | Admin defines attribute types; per-allocation values set on approval |
| CF-05 | Resource management | Define available resources (cluster, partition, storage, software license, cloud, server, instrument) with types and attributes | Admin | Admin creates resource; sets availability and public/private visibility; users request access |
| CF-06 | Resource attributes (custom + inherited) | Attributes on resources that propagate automatically to child allocations (e.g. slurm_cluster name) | Admin | Admin sets resource attributes; allocations inherit at creation |
| CF-07 | Linked resources (parent-child) | Cluster partition resources link to parent cluster; "Is Allocatable" flag on resource | Admin | Admin creates partition resource under parent cluster resource |
| CF-08 | User management | Add/remove users to projects and allocations; role assignment (PI, manager, user, staff views) | Admin / PI | Admin creates user accounts; PI adds members to project; PI-managed allocation membership |
| CF-09 | PI / Manager delegation | PI can designate managers to handle renewals and user management without admin intervention | PI | PI promotes project member to manager role; manager can add/remove users, submit renewals |
| CF-10 | Self-service user portal | PIs and researchers log in to view their projects, request allocations, manage team members | PI / Researcher | Researcher logs in, sees their projects, requests new allocations, checks status |
| CF-11 | Annual project review workflow | Center-wide review process where PIs affirm project status and research output | Admin / PI | Admin triggers review cycle; PIs complete review form; center director reviews results |
| CF-12 | Grant tracking | Collect grant data (title, funding agency, grant number, amount, start/end dates, status) linked to projects | PI / Center Director | PI enters grant data; center director uses for ROI reporting |
| CF-13 | Publication tracking + DOI search | Collect publications linked to projects; DOI lookup for auto-fill | PI / Center Director | PI enters DOI; system fetches metadata; publication linked to project for impact reporting |
| CF-14 | Research output / impact reporting | Center director view of grants, publications, utilization across all projects | Center Director | Quarterly/annual reporting for institutional justification of HPC spend |
| CF-15 | Email notifications | Automated notifications for allocation expiration, renewal, approval/denial, review deadlines | All personas | Event-driven via Django signals; configurable templates |
| CF-16 | Field of Science classification | NSF FOS list used to categorize research projects; admin-customizable | Admin / PI | PI selects FOS at project creation; used for reporting aggregation |
| CF-17 | Read-only center director view | Directors can view all projects, allocations, grants, publications without mutation capability | Center Director | Role-gated read-only dashboard for leadership reporting |
| CF-18 | System admin messaging to project users | Admins can broadcast messages to all users on a project | Admin | Admin selects project, writes message, system notifies all project users |
| CF-19 | User status tracking | Active/inactive user states; inactive members hidden from non-admin views | Admin | Admin deactivates user; existing allocation memberships frozen |
| CF-20 | Allocation change requests | PI/manager can request changes to allocation attributes (e.g., increase core hours); admin approves | PI / Admin | PI submits change request with justification; admin approves or denies; attribute updated |

### Plugin Ecosystem

| # | Plugin | Purpose | Primary Persona | Integration Point |
|---|---|---|---|---|
| CF-21 | Slurm plugin (sacctmgr) | Maps ColdFront allocations to Slurm accounts and user associations via sacctmgr; slurm_check sync verification; slurm_import bulk migration | Admin | Allocation approval triggers sacctmgr account creation; removal triggers account deactivation |
| CF-22 | FreeIPA plugin | Bridges ColdFront allocation membership to FreeIPA HBAC rules and unix group membership; event-driven via Django signals; freeipa_check CLI for drift detection | Admin | Allocation add/remove triggers FreeIPA group membership change; HBAC controls host access |
| CF-23 | LDAP user search | Search LDAP directory when adding users to projects/allocations; attribute mapping (uid, sn, givenName, mail) | Admin | Replaces local-DB-only user lookup with LDAP directory search |
| CF-24 | OpenLDAP project plugin | Creates posixGroup in OpenLDAP for each project; syncs membership; project archival moves/deletes group; calculates GID from configured base | Admin | Project creation triggers OU + posixGroup creation; membership events sync group members |
| CF-25 | Mokey/OIDC plugin | OIDC authentication via Mokey (self-service FreeIPA account portal); links OIDC identities to ColdFront users; auto-syncs claims from ID token | Researcher / PI | Users authenticate via institutional SSO; account created/linked on first login |
| CF-26 | OnDemand plugin | Marks resources as OnDemand-enabled; shows portal link next to allocation; routes users to Open OnDemand for browser-based HPC access | Researcher / PI | Admin enables per-resource; users click logo to access OnDemand from ColdFront allocation view |
| CF-27 | XDMoD plugin | Pulls HPC job usage metrics and cloud core-time from XDMoD API; syncs into allocation attributes for utilization reporting | Admin / Center Director | CLI command syncs XDMoD data into ColdFront; visible in allocation attributes |
| CF-28 | iQuota plugin | Displays NFS storage quota information on ColdFront portal homepage; Kerberos-authenticated | Researcher / PI | Users see their storage quota on login; admin configures keytab + API endpoint |
| CF-29 | Auto-compute allocation plugin | Automatically creates compute allocation when a project is created; auto-assigns Slurm account name, core hour gauges, fairshare attributes | Admin | No-op for admin; project creation triggers allocation; eliminates manual allocation step for standard compute |
| CF-30 | OpenStack plugin | (Community, not in main repo) Maps allocations to OpenStack cloud resource quotas | Admin | Cloud resource allocation governance |
| CF-31 | Keycloak user search | (Community) LDAP-style user search against Keycloak IdP | Admin | Alternative to LDAP user search for Keycloak-based institutions |
| CF-32 | Starfish plugin | (Community) Converts Starfish storage data to ColdFront format | Admin | Storage system integration |

### Technical / Architectural Features

| # | Feature | Purpose | Notes |
|---|---|---|---|
| CF-33 | REST API | Programmatic access to projects, allocations, resources, users | Admin-keyed; enables external automation |
| CF-34 | Django admin interface | Full admin UI for direct DB management of all entities | Admin only; fallback for complex operations |
| CF-35 | Django signals / event hooks | Plugin integration point; signals fire on allocation add/remove, project create/archive, user add/remove | Enables extensible plugin architecture |
| CF-36 | Multiple auth backends | Local DB, LDAP bind, OIDC via Mokey; configurable | Institutional SSO compatibility |
| CF-37 | Custom attribute types | Admin-defined attribute types (Active/Inactive, Date, Int, Public/Private, Text, Yes/No) with public/private visibility | Flexible data model for any resource type |
| CF-38 | PostgreSQL data store | Relational DB with Django ORM; multi-user concurrent access by design | External dependency; not embedded |
| CF-39 | Allocation visibility controls | Private attributes (admin-only) vs public attributes (visible to allocation members) | Granular data disclosure per attribute |
| CF-40 | Resource access restriction by group | Restrict ability to request allocations to specific LDAP groups or individual users | Prevents unauthorized allocation requests for sensitive resources |

**Total ColdFront features inventoried: 40**
(20 core platform + 12 plugins + 8 technical/architectural)

---

## Phase 2 — clustr Coverage Matrix

### Bucket 1: Already Covered by clustr

| ColdFront Feature | clustr Surface | Gap |
|---|---|---|
| CF-05 (partial) Resource management | Node management, Images catalog, Hardware profiles | clustr manages compute nodes; no concept of software licenses, storage, cloud, or instruments |
| CF-08 (partial) User management | Settings > Users, RBAC model (admin/operator/readonly) | clustr has 3 roles; no PI/manager role, no project-scoped membership |
| CF-21 (partial) Slurm integration | Slurm module: sacctmgr-equivalent via slurm.conf rendering, munge key distribution, role assignment, GPG-verified install | ColdFront's Slurm plugin manages accounting/fairshare/QOS via sacctmgr; clustr manages Slurm installation and config files. Different abstraction layers — complementary, not overlapping |
| CF-23 (partial) LDAP user search | LDAP module: users and groups management | clustr's LDAP manages HPC account provisioning; ColdFront's LDAP search is for adding existing users to projects. Same data source, different operations |
| CF-33 REST API | Full REST API at `/api/v1/` | clustr's API is admin-scoped node/provisioning operations; not an allocation governance API |
| CF-34 Admin interface | Web UI (full admin surface) | Clustr's UI is admin-only today — structurally equivalent to ColdFront's Django admin for its domain |
| CF-36 (partial) Auth backends | Session + API key auth | No OIDC or LDAP-bind for clustr auth; LDAP is for HPC accounts, not clustr login |

**Covered: 7 features (partial in all cases — clustr covers the provisioning half, not the governance half)**

---

### Bucket 2: v1.2 Researcher Portal MVP

The minimal set that proves the researcher/PI wedge without committing to full ColdFront
feature parity. These are the features that directly answer the question institutional
buyers will ask in year one: "How do my researchers interact with this system?"

| ColdFront Feature | Proposed clustr v1.2 Equivalent | Rationale |
|---|---|---|
| CF-10 Self-service user portal | Researcher-facing read-only status view (new `viewer` role, scoped to partition health + their LDAP account) | Lowest-viable researcher surface; preempts Bright Computing's user portal objection |
| CF-02 (partial) Allocation management | Expose clustr NodeGroup assignments as "allocations" in a researcher-readable format: "You are in group X, which has N nodes in partition Y" | Reframes existing NodeGroup model in language researchers understand |
| CF-09 (partial) PI / Manager delegation | PI role added to clustr's RBAC model: can view their NodeGroup's health, add/remove members from their group (but not manage nodes) | Extend 3-tier model to 4 tiers: admin / operator / PI / viewer |
| CF-08 (partial) User management | PI self-service: add users to their NodeGroup (submits request; admin approves or auto-approves based on config) | Eliminates "email the sysadmin to add a student" workflow |
| CF-26 OnDemand plugin | Link from researcher view to Open OnDemand URL if configured (single env var, single UI link) | Low-effort; high institutional value; UB/XSEDE-style deployments expect this |
| CF-28 iQuota / storage display | LDAP-derived storage quota display on researcher dashboard (if LDAP module enabled and quota attribute mapped) | Answers "how much space do I have?" without calling the sysadmin |
| CF-15 (partial) Email notifications | Email to researcher when their LDAP account is created, when a node in their partition goes offline, when password reset is needed | Existing LDAP module has account creation event; just needs notification hooks |

**v1.2 MVP count: 7 features**

---

### Bucket 3: v1.3 / v1.4 Follow-ons

Natural second layer after the researcher portal is proven to be used.

| ColdFront Feature | Proposed clustr Equivalent | Notes |
|---|---|---|
| CF-12 Grant tracking | Grant record linked to a NodeGroup: PI enters grant number, funding agency, period; visible to IT Director | Enables ROI reporting; data is simple; schema addition only |
| CF-13 Publication tracking + DOI search | Publication record linked to NodeGroup; DOI lookup for auto-fill | Same simple pattern as grants; high institutional value for annual review justification |
| CF-11 Annual project review workflow | Annual "NodeGroup health report" that PI must acknowledge; admin-triggered; PI clicks "affirm active" or "archive" | Lightweight annual review; prevents zombie groups consuming resources |
| CF-20 Allocation change requests | PI requests additional nodes or partitions; admin approves or denies; audit-logged | Forms the governance layer that gives the IT director a paper trail |
| CF-16 Field of Science classification | NSF FOS tag on NodeGroup; optional; used for utilization reports | Low cost; high reporting value for grant-funded institutional customers |
| CF-27 XDMoD integration | Pull job-level utilization from XDMoD API into NodeGroup utilization view (if customer has XDMoD) | XDMoD is common at R1 universities; this integration unlocks the existing XDMoD investment |
| CF-14 Research output / impact reporting | IT Director dashboard: grants + publications + node utilization per group, exportable to CSV | Combines CF-12, CF-13, CF-27 into a reporting surface for leadership |
| CF-17 Read-only center director view | IT Director role (read-only across all groups, allocations, utilization) | Extend RBAC: admin / operator / PI / viewer / director |
| CF-18 Messaging to project users | Admin can broadcast message to all users in a NodeGroup via email | Maintenance notifications without manual email list management |
| CF-39 Attribute visibility controls | Per-attribute public/private flag (e.g., BMC credentials private; node count public) | Already partially in place for LDAP credentials; generalize |

**v1.3/v1.4 count: 10 features**

---

### Bucket 4: v2.0+ Horizon

Strategic but architecturally heavier. Do not start until a paying institutional customer
explicitly requests.

| ColdFront Feature | clustr v2.0 Equivalent | Prerequisite |
|---|---|---|
| CF-25 OIDC / Mokey auth | OIDC login for researchers and PIs (separate from admin session auth) | First institutional LOI with SSO requirement (D1 re-decision trigger already defined) |
| CF-22 FreeIPA plugin | FreeIPA HBAC bridge: allocation membership drives FreeIPA HBAC rules automatically | Requires FreeIPA deployment at customer site; niche but important for R1 universities |
| CF-29 Auto-compute allocation | Automatic NodeGroup creation when a new PI is onboarded; auto-assigns partitions | Requires PI onboarding workflow to exist first (v1.2) |
| CF-36 Full multi-backend auth | LDAP-bind login for clustr authentication (distinct from HPC LDAP account management) | D1 deferred; requires decoupling clustr auth from HPC account management conceptually |
| CF-24 OpenLDAP project plugin | Automatic posixGroup creation in LDAP when NodeGroup is created | Useful for environments without FreeIPA; requires LDAP module to be bidirectional |
| CF-40 Resource access restriction by group | Restrict which LDAP groups can request allocation to a NodeGroup | Multi-tenant isolation prerequisite |
| CF-30 Cloud resource allocation | Cloud VM quotas managed alongside HPC node allocations | Out of scope unless clustr expands to hybrid HPC+cloud |
| CF-38 PostgreSQL migration | Multi-tenant data isolation may require PostgreSQL (D6 trigger already defined) | Do not pursue until sustained SQLite write contention or >50 concurrent deploys |

**v2.0+ count: 8 features**

---

### Bucket 5: Skip (with rationale)

| ColdFront Feature | Decision | Rationale |
|---|---|---|
| CF-31 Keycloak user search | Skip | Keycloak is not common in the clustr target market (air-gap HPC, bare-metal, research labs); OIDC covers the use case generically |
| CF-32 Starfish plugin | Skip | Starfish is CCR-specific storage tooling; niche to a single institution's workflow |
| CF-34 Django admin interface | Skip | Not applicable — clustr is not Django. The clustr web UI already serves this function |
| CF-35 Django signals | Skip | Not applicable — clustr is Go, not Django. Event hooks for modules are already implemented via the module plugin pattern |
| CF-38 PostgreSQL (as prerequisite) | Defer, not skip | D6 already has a defined trigger; PostgreSQL migration is conditional on scale, not a default adoption |
| CF-03 (strict) Mandatory expiration on all allocations | Skip | Mandatory expiration creates operational friction for bare-metal clusters that are not grant-funded; make expiration optional (PI can set it, but it is not enforced by default) |

**Skip count: 5 features (3 not applicable, 2 conditional/deferred)**

---

## Phase 3 — The Powerhouse Thesis

### What the Combined Product Looks Like

Take clustr's existing strengths — zero-egress PXE provisioning, signed Slurm bundles,
single-binary Go server, 2-VM turnkey lab, integrated Slurm pipeline, LDAP account
management, GPG-verified installs, AES-256-GCM credential encryption — and add the
best 30 features from ColdFront's governance surface. The resulting product is something
that does not currently exist in the open-source HPC ecosystem: a unified platform that
handles the full lifecycle from bare metal to running job, including the governance layer
that justifies compute spend to the people who approved the budget.

Today, a university HPC center that wants the ColdFront + xCAT combination must:
deploy ColdFront (Django + PostgreSQL + worker processes), deploy xCAT (complex, XML-
heavy, CLI-only), connect them manually via custom scripts, and maintain two separate
web UIs, two separate user databases, and two separate plugin ecosystems. The combined
product is maintained by different teams at different institutions with no shared data
model. The integration is fragile and the operational burden is significant.

clustr + ColdFront-inspired governance layer, in a single Go binary with SQLite, built
by one team with one data model, would eliminate that entire class of integration work.

### Competitive Position

The combined positioning competes in three directions simultaneously:

**Against xCAT / Warewulf (node provisioning tools):** clustr already leads on web UI,
ease of install, and Slurm integration. Adding allocation governance and researcher
self-service makes clustr the only provisioning tool with a governance layer — turning
an operational tool into a platform that department heads can justify to their CFO.

**Against ColdFront itself:** ColdFront has no node provisioning capability. It tells
Slurm what accounts exist; it does not provision the nodes those accounts run on.
A shop running ColdFront still needs xCAT or Warewulf for node management. clustr
eliminates the need for ColdFront by absorbing its governance surface into a stack
that already handles the provisioning layer. This is a genuine displacement play — not
a "we do this better" argument but a "why run two systems when one does both" argument.

**Against Bright Computing:** Bright Computing (BCM) is the commercial incumbent with
multi-persona awareness, a user portal, and an admin UI. It is expensive ($50K-$150K+
per cluster), proprietary, and requires vendor lock-in for support and updates. clustr's
differentiated position is: open source, truly self-hosted, zero egress, single binary,
signed-bundle trust chain, and — with the governance layer — a feature surface that
covers 80% of what Bright offers at 0% of the license cost. The remaining 20% (HA
failover, enterprise support contracts, GPU scheduling integration beyond basic detection)
is the upsell for a future paid tier or support subscription.

**Against OpenHPC stack:** OpenHPC is a collection of packages, not a product. It requires
significant integration work. clustr positions as the opinionated, pre-integrated layer
on top of OpenHPC's components — or as an alternative that ships its own Slurm bundles.

### clustr's Unique Advantage in the Combined Position

Three things that none of the above can replicate easily:

**1. Single binary, single data model.** No separate allocation database, no Django
process, no worker queue, no PostgreSQL. The governance data (NodeGroups as allocation
containers, LDAP users as allocation members, Slurm roles as resource definitions) all
live in the same SQLite database that drives provisioning. Cluster state and allocation
state are always consistent because they are the same state.

**2. Signed-bundle trust chain.** The provisioning-to-governance chain is cryptographically
verified end-to-end. A PI's allocation maps to a NodeGroup; that group drives which Slurm
partition is configured; that config is GPG-signed and pushed to nodes via clustr-clientd.
No external authority can inject an unsigned Slurm config. This is a trust story that
ColdFront + xCAT cannot tell because their data flows are not cryptographically bound.

**3. Air-gap native.** The combined platform works in classified environments, research
networks without internet routing, and institutional clusters behind strict egress
controls. ColdFront's Django package ecosystem, OIDC flows, and PyPI dependencies make
it difficult to deploy in true air-gap environments. clustr's embedded Slurm repo, static
binary distribution, and zero-outbound architecture make it the natural choice for any
cluster with strict network controls.

### The "Why clustr Now" Pitch

The HPC platform market has a gap that has existed for a decade: node provisioning
tools (xCAT, Warewulf) handle bare metal but ignore governance, while allocation
management tools (ColdFront) handle governance but ignore bare metal — leaving every
HPC center to maintain a fragile custom bridge between them. clustr closes that gap
in a single open-source Go binary that provisions nodes, installs and configures Slurm
with a GPG-signed bundle, manages LDAP accounts, gives researchers a self-service
status portal, gives PIs a utilization dashboard, and gives IT directors the impact
reporting they need to justify compute spend — all self-hosted, all air-gap compatible,
all with zero egress and a cryptographically verified trust chain from allocation
decision to running job. The window is open because the major incumbents (Bright
Computing) are proprietary and expensive, the open-source alternatives (xCAT, Warewulf,
ColdFront) are siloed, and no single team has shipped a unified platform. That changes
with clustr v1.2.

---

## Phase 4 — Persona Rework

The earlier 6-persona model (webui-review-personas.md) identified the researcher
persona as a v1.2 opportunity but did not make PI a distinct first-class persona
separate from "research group lead." ColdFront's explicit PI / Manager / Center
Director model, examined against clustr's data structures, warrants promotion of
several implicit sub-personas to first-class status.

### Changes from Prior Model

**PI is now Persona 4A (first-class, split from "Research Group Lead").**
In the prior model, PI was a note under Persona 4. ColdFront's architecture is built
around the PI as the primary governance actor — the PI requests allocations, manages
team membership, submits grants and publications, and is accountable to the center
director. In clustr's v1.2 world, the PI is the person who owns a NodeGroup, can add
researchers to it, and is the first call when the IT director asks "is this cluster
earning its keep?" That is a distinct identity from the junior researcher who submits
jobs. Elevating PI to first-class surfaces the governance wedge clearly.

**Resource Manager is not a new first-class persona for clustr.**
ColdFront has an implicit "Resource Manager" role — the admin who approves allocations,
manages resource availability, and reviews PI requests. In clustr's model, this is
the HPC Sysadmin (Persona 1). clustr does not need a separate "resource manager" persona
because the sysadmin approves NodeGroup memberships, manages node availability, and
reviews PI requests. Adding a distinct "resource manager" persona would require a
separate role with overlapping capability to the admin — unnecessary complexity at this
stage. The admin approves; that is sufficient for v1.2.

**Center Director maps to IT Director (Persona 5) with enhanced scope.**
ColdFront's "Center Director" is institutional leadership who measures scientific impact.
clustr's "IT Director" is budget-accountability leadership who justifies compute spend.
The function is identical; the framing differs by institution type. The IT Director
persona absorbs the Center Director function and gains the grant/publication reporting
surface in v1.3.

**Persona 6 (External / Federated User) remains deferred.**
No change from prior assessment. OIDC prerequisite remains v2.0.

### Updated 7-Persona Model

**Persona 1: HPC Sysadmin (unchanged)**
Full platform admin. Provisions nodes, manages images, configures Slurm, manages LDAP.
Approves NodeGroup membership requests from PIs. Owns the trust chain. The only persona
that should see the full admin surface.

**Persona 2: Junior Ops / On-Call Operator (unchanged)**
Group-scoped. Sees only their assigned NodeGroups. Deploys and monitors nodes. Cannot
create images, manage users, or configure Slurm. The first user who needs role-aware nav.

**Persona 3: HPC Researcher / Scientist (elevated from "opportunity" to "v1.2 target")**
Read-only access to their partition's health status, their LDAP account (password change),
and their NodeGroup membership. Cannot see node internals, Slurm config, or image
management. The viewer role. In ColdFront's model, this is a user with an allocation.
In clustr's model, this is a user in a NodeGroup.

**Persona 4A: PI / Research Group Lead (NEW — first-class)**
Owns one or more NodeGroups. Can add/remove researchers from their group (with admin
approval or auto-approval). Views NodeGroup utilization summary. Enters grant and
publication data (v1.3). Can submit requests to expand their group's allocation.
Cannot manage nodes, images, or Slurm config. The governance actor that makes clustr
more than a sysadmin tool.

**Persona 4B: Allocation Approver / Center Staff (NEW — v1.3 target)**
An intermediate admin role that approves PI allocation requests but does not manage
nodes. This is ColdFront's typical "staff" role — a department administrator or HPC
center coordinator who handles paperwork without touching infrastructure. In clustr's
terms, this is a user who can approve NodeGroup membership requests and allocation
change requests without having full admin rights. Not required for v1.2 but needed
before the annual review workflow makes sense.

**Persona 5: IT Director / Center Director (enhanced)**
Read-only leadership view. Gains grant/publication data (v1.3), utilization reporting
per NodeGroup, exportable impact summary. Does not interact with nodes, Slurm, or
LDAP. The persona that justifies clustr at the institutional procurement conversation.

**Persona 6: External / Federated User (deferred — no change)**
Requires OIDC and multi-tenant isolation. v2.0+. Defer until first institutional LOI
with cross-institutional collaboration requirement.

### Updated Persona x Surface Matrix

Cells: S = Served, P = Partial, U = Unmet, N = Not applicable

| Surface | P1: Sysadmin | P2: Operator | P3: Researcher | P4A: PI | P4B: Approver | P5: IT Director |
|---|---|---|---|---|---|---|
| Admin dashboard (current) | S | P | N | N | N | N |
| Cluster status view | U | P | U | U | U | U |
| Images — catalog | S | N | N | N | N | N |
| Images — build/import | S | N | N | N | N | N |
| Nodes — full list | S | P (scoped) | N | N | N | N |
| Nodes — group-scoped view | S | U | N | N | N | N |
| Deployments — all | S | P (scoped) | N | N | N | N |
| Network Allocations | S | P | N | N | N | N |
| Network Switches/Profiles | S | N | N | N | N | N |
| Slurm — config/scripts/push | S | N | N | N | N | N |
| Slurm — sync status | S | P | N | N | N | N |
| Slurm — upgrade management | S | N | N | N | N | N |
| LDAP — user/group admin | S | N | N | N | N | N |
| LDAP — self-service password | N | P | U | U | N | N |
| NodeGroup — manage | S | N | N | N | N | N |
| NodeGroup — utilization view | U | N | N | U | N | U |
| NodeGroup membership — PI self-service | N | N | N | U | N | N |
| Allocation requests — submit | N | N | N | U | N | N |
| Allocation requests — approve | S | N | N | N | U | N |
| Grant / publication tracking | N | N | N | U | N | U |
| Research impact reporting | N | N | N | N | N | U |
| OnDemand portal link | N | N | U | U | N | N |
| Storage quota display | N | N | U | U | N | N |
| Email notifications — allocation | N | N | N | U | U | N |
| Email notifications — operational | S | P | N | N | N | N |
| Trust / security posture visible | U | N | N | N | N | P |
| IT Director read-only view | N | N | N | N | N | U |
| System accounts/groups | S | N | N | N | N | N |
| Audit log | S | N | N | N | N | N |
| Settings — full | S | N | N | N | N | N |
| Settings — password only | N | U | U | U | N | N |

**Matrix: 6 personas x 31 surfaces = 186 cells**
**Served: 30 (all Persona 1). Partial: 8. Unmet: 33. Not applicable: 115.**

The concentration of "Unmet" cells in Personas 3, 4A, and 5 is the strategic gap
and the v1.2/v1.3 opportunity space.

---

## Phase 5 — Risks and Tradeoffs

### Risk 1: Scope Absorption (Severity: High — address now)

ColdFront took a team at the University at Buffalo years to build and is still
actively developed. Its allocation/project/grant/publication/review surface represents
roughly 12-18 months of engineering for a 2-person team if built from scratch to
ColdFront's depth. The founder's framing — "powerhouse of an application" — is
strategically correct as a destination but dangerous as a near-term sprint directive.

The discipline required: ship the v1.2 researcher portal MVP (7 features, 1 new role,
1 new UI view) and let customer behavior dictate which v1.3 features get pulled forward.
Do not start CF-12 (grant tracking), CF-13 (publications), or CF-11 (annual review) until
at least one institutional customer is running v1.2 and asking for reporting. The
temptation to pre-build the full governance stack before validating that the researcher
portal is actually used will absorb 6 months and produce features nobody has asked for.

The constraint is simple: every ColdFront-inspired feature in the backlog must have a
named customer persona waiting for it before development starts. No speculative governance
features.

### Risk 2: Single-Binary Positioning vs. Governance Data Model (Severity: Medium)

clustr's "single binary, no external dependencies" positioning is a genuine differentiator.
ColdFront's data model is more complex than clustr's current schema — projects contain
allocations which contain users which have attributes which are typed and can be private
or public. Absorbing this model faithfully would require significant schema additions
and could begin to stress SQLite at the sizes where governance data grows (thousands
of users, hundreds of projects, multi-year allocation history).

The resolution is to implement the governance model at clustr's current abstraction
level, not ColdFront's. The equivalence is: NodeGroup = Project + Allocation (combined).
A NodeGroup already has an owner, members, a purpose, and a resource (the nodes in it).
The v1.2 PI portal does not require inventing a separate Project and Allocation entity
hierarchy — it requires surfacing the existing NodeGroup model in PI-readable terms.
This keeps the schema simple, preserves SQLite compatibility, and avoids the over-
engineering trap of building ColdFront's exact data model in Go.

If a customer ever needs the full project/allocation hierarchy — multiple allocations
per project, per-allocation expiration, allocation change requests with approval history
— that is the trigger for the v2.0 schema evolution, not a v1.2 prerequisite.

### Risk 3: AGPLv3 License Contamination (Severity: Low, but must be stated)

ColdFront is AGPLv3. clustr's license is not yet visible in the repo (no LICENSE file
found in the root at time of this review — this needs to be addressed before Show HN,
not after). If clustr intends MIT or Apache-2.0, no ColdFront code can be copied under
any circumstance. This review is explicitly a feature study, not a code study. The
risk of a developer referencing ColdFront's source and accidentally copying a Django
view or data model definition is real and should be explicitly documented in the
contributor guidelines.

Action item for Richard: add a LICENSE file to the clustr repo. Action item for Jared:
add a contributor guideline note that ColdFront's source code is AGPLv3 and cannot be
referenced in pull requests.

### Risk 4: ColdFront's Allocation Model Assumptions vs. clustr's Physical Model (Severity: Medium)

ColdFront's allocation model assumes that "access to a resource" is the thing being
managed — and that resource access is granted/revoked by policy, not by physical
provisioning. A user has an allocation on a Slurm cluster; the Slurm plugin creates
the sacctmgr account; the user can now submit jobs. The physical nodes don't change
when an allocation is granted or revoked.

clustr's model is the reverse: physical node state (provisioned or not, in which group,
with which Slurm role) is the source of truth. An allocation in clustr's terms is a
node group with a Slurm partition — physical, not policy.

This conceptual mismatch matters for the v1.2 UI: if clustr presents "allocations"
in PI-readable terms, it must be clear that an "allocation" in clustr means "your
group has these nodes in this partition" — not "you have been granted X core-hours
regardless of whether the nodes are provisioned." The governance model clustr can
build honestly is physical-resource governance, not abstract-quota governance. That
is actually a stronger story for bare-metal HPC: "you can see exactly which nodes
you have and whether they are deployed" rather than "you have an abstract core-hour
budget." Do not imitate ColdFront's quota model — lean into clustr's physical model.

### Risk 5: Feature Velocity vs. Core Provisioning Depth (Severity: High)

The founder's "ship sprints continuously" mandate and the "powerhouse" directive are
in genuine tension. The governance layer (v1.2-v1.4) is additive work that runs
in parallel with any remaining provisioning depth work (HA slurmctld, multi-controller
topology, GPU partition management, storage node types). Every sprint spent on the
governance layer is a sprint not spent on provisioning depth.

The strategic sequence that resolves this tension:

1. **v1.1:** Role-aware nav + operator-scoped dashboard (provisioning depth for existing
   personas — no new features, just make existing ones usable for non-admins).
2. **v1.2:** Researcher portal MVP (7 ColdFront-inspired features — the wedge). Ship
   this to the first institutional customer or Show HN community.
3. **v1.3:** First provisioning depth sprint that customers have asked for (determined
   by v1.2 feedback), OR first governance follow-on (grants/publications) if institutional
   customers are asking. Let the signal drive the sequence.
4. **v1.4 and beyond:** Alternate provisioning depth and governance depth based on
   validated customer demand. Do not commit to a governance roadmap longer than two
   sprints without customer validation at each step.

Richard owns the sequencing call. This is flagged here because the governance roadmap
is long enough that it could absorb all available velocity if not constrained by explicit
customer-pull triggers.

---

## Closing: Top 10 ColdFront-Inspired Features for v1.2 — Priority Order

This is my strategy judgment, not an implementation spec. Richard should review
against sprint capacity and technical sequencing constraints before committing.

**1. Viewer role + researcher status view (CF-10)**
A new `viewer` role (more restricted than `readonly` — partition health, LDAP account
status, no node internals) with a dedicated `/portal` or `/status` route. The single
highest-leverage move: turns "clustr is a sysadmin tool" into "clustr has a researcher
portal." Preempts the Bright Computing objection. Zero backend schema changes — builds
on existing LDAP module data.

**2. Role-aware nav for all non-admin roles (CF-10 prerequisite)**
Hide Slurm/LDAP/System/Network from operator and viewer roles. Show only what the
persona can act on. This is v1.1 scope per the existing sprint plan but is a prerequisite
for any researcher portal — must ship before v1.2 researcher features are visible.

**3. PI role added to RBAC (CF-09)**
A fourth role: PI. Can view their NodeGroup's health, see member list, request new
members. Cannot manage nodes, images, or Slurm. The governance actor that turns
NodeGroups from technical constructs into owned resources. Schema addition: PI field
on NodeGroups, or a new `pi_assignments` table.

**4. PI self-service member management (CF-08)**
PI can add/remove researchers from their NodeGroup. Removal triggers LDAP account
deactivation (if LDAP module enabled). Optionally admin-approval-gated. Eliminates
the "email the sysadmin to add a student" workflow that every HPC center reports as
their top operational pain point.

**5. LDAP self-service password change for researchers (CF-10 / CF-08)**
Researcher logs in (viewer role), changes their LDAP password without calling the
sysadmin. The LDAP module already manages accounts — this is one new API endpoint
and one new UI form. Highest researcher satisfaction per engineering effort.

**6. NodeGroup utilization summary for PIs (CF-02 / CF-14)**
PI-facing view of their NodeGroup: node count, deployed/undeployed, last deploy
activity, partition availability. Read-only, non-technical language. Answers "is
my cluster healthy?" without giving the PI access to node internals. Requires
aggregating from existing DB tables — no schema changes.

**7. OnDemand portal link (CF-26)**
If `CLUSTR_ONDEMAND_URL` is set, show a link in the researcher view and PI view
to the OnDemand portal. Single env var, single UI element, zero backend work. High
institutional value for university deployments that already run OnDemand.

**8. Email notifications for LDAP account events (CF-15)**
When a researcher's LDAP account is created (or their NodeGroup membership changes),
send them an email with cluster access instructions. Requires an SMTP config and
a notification hook in the LDAP module. Eliminates the "go tell your student they
have access now" manual step.

**9. Storage quota display for researchers (CF-28)**
If LDAP quota attributes are mapped (many institutions store quotas in LDAP), surface
the researcher's storage quota on their portal view. Requires a configurable LDAP
attribute mapping — no new storage system integration. Low effort, high researcher
satisfaction.

**10. IT Director read-only summary view (CF-17)**
A new `director` role with a read-only view: node count over time, deploy success
rate, active NodeGroups, active researchers. Exportable to CSV. Builds on existing
`reimages` and `audit_log` table data. Answers "is this cluster earning its keep?"
at the level of leadership without exposing infrastructure details.

---

## Summary Scoreboard

| Bucket | Count | Notes |
|---|---|---|
| Already covered (partial) | 7 | All provisioning-side; no governance coverage |
| v1.2 MVP | 7 | Researcher portal, PI role, OnDemand link, notifications |
| v1.3/v1.4 follow-ons | 10 | Grant/publication tracking, annual review, XDMoD, reporting |
| v2.0+ horizon | 8 | OIDC, FreeIPA, multi-tenant, PostgreSQL migration |
| Skip | 5 | 3 not applicable (Django-specific), 2 conditional/deferred |
| **Total inventoried** | **40** | 32 actionable, 5 not applicable, 3 conditional |

The strategic answer to the founder's directive: yes, absorbing ColdFront's governance
surface makes clustr a powerhouse. No, it does not mean building all 40 features. It
means shipping the 7-feature v1.2 researcher portal, validating that institutional
customers use it, and pulling v1.3 features forward only when a named customer asks.
The risk is not ambition — the positioning is correct. The risk is building the full
governance stack before the first institutional customer confirms they need it.
