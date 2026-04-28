# Show HN Draft — clustr v1.7.0

**Status:** Draft for Sprint I review. Target post date: 2026-07-27 (Sunday).
Post to r/hpc, r/homelab, and relevant mailing lists the same day.
Erlich owns the posting cadence and day-of roster per I7.

---

## Title Options

**Option A (recommended):**
> Show HN: clustr – open-source HPC cluster management with provisioning and allocation governance in one binary

Rationale: Names the two things HN readers will immediately recognize as "previously separate." "One binary" is the credibility signal that this is not another Python stack. Slightly long but every word does work.

**Option B (shorter, punchier):**
> Show HN: clustr – self-hosted HPC cluster manager that replaces xCAT + ColdFront

Rationale: More confrontational. HN likes direct comparisons. Risk: sounds like marketing. Use if Option A underperforms in the first hour.

**Option C (for a more technical audience):**
> Show HN: clustr – Go binary that PXE-provisions nodes, installs Slurm with a GPG-signed bundle, and runs a governance portal

Rationale: The most technically precise. Appeals to the commenter who wants to know "but what does it actually do." Safe but not headline-grabbing.

**Recommendation:** Post with Option A. Have Option B ready as the opening comment framing if the title gets challenged as vague.

---

## Post Body (target: under 300 words)

> Every HPC center I've seen runs two siloed systems: a node provisioner (xCAT, Warewulf) that handles bare metal, and an allocation manager (ColdFront) that handles governance — who gets which nodes, for how long, and why. These two systems share no data model, no trust chain, and no user database. Keeping them in sync is a permanent ops burden that every sysadmin handles with custom scripts and hope.
>
> clustr closes that gap in a single open-source Go binary.
>
> It provisions nodes via PXE boot, builds and manages OS images (pull from URL, import ISO, or capture from running node), distributes Slurm with a GPG-signed RPM bundle, manages LDAP accounts, and runs a governance layer — PI self-service portal, IT Director dashboard, allocation change requests, grant/publication tracking, annual review cycles — all in the same SQLite-backed binary that drives the provisioning.
>
> Why this matters: cluster state and governance state are always consistent because they are the same state. There is no "sync job" between your provisioner and your allocation manager. There is no second database to manage. The path from allocation decision to running Slurm job is GPG-signed end to end.
>
> What it does not do (be honest): no cloud or hybrid allocation, no OIDC/SSO yet (local auth only), no FreeIPA HBAC bridge, no multi-tenant data isolation. It is a single-organization self-hosted tool. If you need Kubernetes or cloud burst scheduling, look elsewhere.
>
> 15 minutes from git clone to a running server. Under 30 minutes to a provisioned node.
>
> Repo: https://github.com/sqoia-dev/clustr
> Docs: https://github.com/sqoia-dev/clustr/tree/main/docs
>
> Happy to answer questions about the architecture, the Slurm bundle signing chain, or why we picked Go + SQLite over Python + PostgreSQL.

---

## Opening Comment (post immediately after the thread opens)

Post this as the first reply to seed the technical discussion:

> A few things that might come up:
>
> **Why Go instead of Python?** ColdFront is Python/Django + PostgreSQL + worker processes. That is five things to deploy and operate. clustr is two static binaries and a SQLite file. For an air-gapped HPC cluster that does not have a Kubernetes cluster to run on, the single-binary model is not a preference — it is a requirement. Go compiles to a static binary with zero runtime dependencies. The `clustr` deploy agent runs inside a 50MB PXE initramfs with no package manager.
>
> **Why not just run ColdFront and xCAT side by side?** You can. Most people do. The operational cost is: two web UIs, two user databases, two plugin ecosystems, a custom sync script between them, and a permanent debugging question of "did this fail in xCAT or in ColdFront?" clustr makes that cost zero by unifying the data model. It is not a "we do this better" argument; it is a "why run two systems when one does both" argument.
>
> **What does "GPG-signed Slurm bundle" mean?** clustr ships its own RPM repository containing pre-built Slurm packages (currently v24.11.4 for EL9 x86_64). The repo metadata is GPG-signed. When a node PXE-boots and installs Slurm, it verifies the signature before installing. No external repo URL is needed; the clustr server itself serves the repo. This means the trust chain from "admin enables Slurm on this cluster" to "slurmd is running on this node with verified packages" is entirely under the operator's control.
>
> Source is at https://github.com/sqoia-dev/clustr. MIT/Apache licensed.

---

## Preempted FAQ

These are the comments that will appear within the first two hours. Have these replies ready to paste (do not pre-post them — let the comment appear first, then respond).

---

### 1. "Why not use Bright Computing / BCM?"

**Expected comment form:** "Bright Computing already does all of this. Why reinvent the wheel?"

**Prepared reply:**
> Bright Computing (now NVIDIA Base Command Manager) is a commercial product. The last pricing I saw was in the $50K–$150K+ range per cluster depending on node count and support tier. That is not an option for a 32-node academic cluster at a regional university, a national lab team that needs air-gap deployment, or a research group that cannot commit to a vendor support contract. clustr is MIT/Apache licensed, self-hosted, and costs zero dollars. The feature set is not identical — Bright has HA failover and GPU scheduling integrations we do not have yet — but for the 80% of HPC centers that need "provision nodes, run Slurm, manage allocations," clustr covers the ground at 0% of the license cost.

---

### 2. "How is this different from xCAT?"

**Expected comment form:** "xCAT has been doing bare-metal provisioning for 20 years. What does clustr add?"

**Prepared reply:**
> xCAT is a capable provisioner but it has two problems for this use case. First, it does not have a governance layer — it provisions nodes but has no concept of who is allowed to use them, for how long, or why. You still need ColdFront or a custom portal for that. Second, the operational model is XML-heavy, CLI-centric, and requires significant tribal knowledge to operate. clustr has a web UI that a non-sysadmin can log into, a REST API that a PI can query, and a governance layer that gives the IT director a CSV export for their budget review. xCAT and clustr are not the same tool at different price points; they serve overlapping provisioning use cases with different operational philosophies.

---

### 3. "How does this differ from Warewulf?"

**Expected comment form:** "Warewulf 4 already does stateless PXE provisioning. What does clustr add?"

**Prepared reply:**
> Warewulf is a stateless node provisioner — it is good at what it does, and the stateless model is genuinely elegant. clustr's model is different: nodes are not stateless, they have persistent per-node identity (hostname, network config, SSH keys, Slurm role) that is stored in the server and applied at deploy time from a shared base image. This is the right model for HPC clusters where nodes have stable identities and Slurm needs a fixed nodelist. More importantly, Warewulf has no governance layer — no PI portal, no allocation management, no IT director reporting. That is the gap clustr fills.

---

### 4. "Why not just use ColdFront for the governance piece and a provisioner for the bare metal?"

**Expected comment form:** "ColdFront is great for governance. Just use it with xCAT."

**Prepared reply:**
> That is what most people do today. The operational cost of that combination: two web UIs with separate auth, two databases to back up, two plugin ecosystems (ColdFront's Django plugins and xCAT's Perl/Python scripts), and a custom sync layer between them that breaks whenever either system upgrades. Every shop I have talked to runs this combo with a fragile glue layer they are embarrassed about. clustr's answer is: the provisioning data and the governance data are the same data, in the same SQLite file, served by the same binary. When you disable a PI's NodeGroup, the nodes are not reimaged onto a different partition — that governance decision and the physical cluster state are always consistent. You cannot get that with two separate systems.

---

### 5. "How do I trust the bundles? What is the signing chain?"

**Expected comment form:** "GPG-signed bundles sounds good but how does the trust chain actually work?"

**Prepared reply:**
> The Slurm RPM bundle is built and signed by us (sqoia-dev). The public key is embedded in the clustr binary and pinned in the install docs. When a node PXE-boots and the clustr deploy agent runs, it installs Slurm packages from the repo that clustr-serverd serves locally — it does not reach out to an external package mirror. The repo metadata (`repomd.xml`) is signed with the sqoia-dev GPG key. The node's `dnf` or `yum` is configured to verify that signature before installing any package. If the signature does not match, the install fails. The chain is: operator enables Slurm → clustr-serverd installs the signed bundle locally → node PXE-boots → deploy agent configures `dnf` with the server's repo URL → `dnf` verifies signature → Slurm installs only if verified. No package leaves the operator's network without being verified.

---

### 6. "Why no SaaS option?"

**Expected comment form:** "Would be great to have a hosted version."

**Prepared reply:**
> A hosted clustr would require us to expose PXE/DHCP to the internet, which is a non-starter — we would own your cluster's boot sequence from our servers. More practically: HPC clusters at universities and national labs are almost always air-gapped or on restricted networks. A SaaS model would exclude the majority of our target market. The value proposition is self-hosted and zero-egress. That is a feature, not a limitation. If you need a hosted demo to evaluate clustr, the 15-minute Docker Compose quickstart is the right path — it takes less time than signing up for a SaaS trial.

---

### 7. "Why SQLite instead of PostgreSQL?"

**Expected comment form:** "SQLite won't scale. Why not use a real database?"

**Prepared reply:**
> SQLite with WAL mode handles the write load of an HPC cluster management system comfortably — we are not doing OLAP, we are doing structured event storage for a single-organization deployment. The tradeoff is deliberate: PostgreSQL is an external dependency that requires its own HA, backup, and upgrade procedures. For a self-hosted tool on an air-gapped HPC cluster, that dependency is not free. SQLite gives us a single file that you can `cp` for a backup. Our scale target for a single SQLite instance is sub-200-node clusters with sub-50 concurrent active deploys — which covers the vast majority of academic HPC centers. If you hit contention (sustained write latency >100ms over 1 hour), that is the documented trigger to switch to PostgreSQL. We have not hit that trigger on any real deployment yet.

---

### 8. "What about multi-cluster / multi-tenant?"

**Expected comment form:** "We have three clusters at our institution. Can clustr manage all of them?"

**Prepared reply:**
> Not yet in v1.x. A single clustr instance manages one cluster. Running one instance per cluster and managing them from a shared bastion is the current pattern. Multi-tenant data isolation (tenant_id in the schema, row-level access controls) is explicitly a v2.0 item, gated on PostgreSQL migration and a named customer who needs it. It is the right design for v2.0 but it would add architectural complexity to v1.x that is not justified until we have a customer asking for it. If you are evaluating clustr for a multi-cluster environment, the current answer is: one instance per cluster, manage them from your standard tooling (Ansible, scripts) with the clustr REST API.

---

## Launch Day Checklist

This covers the post itself. Erlich owns the broader launch comms plan (I7).

- [ ] Repo is public and has a LICENSE file (MIT/Apache — confirm before posting)
- [ ] README hero is merged and the GIF is in place at the top
- [ ] `docs/install.md` Docker Compose path works end-to-end on a clean Rocky 9 VM (I5 audit complete)
- [ ] `docs/install.md` bare-metal path has been walked through at least once this sprint
- [ ] All links in README resolve (docs link-checker in CI is green — I9)
- [ ] CHANGELOG.md is current through v1.7.0
- [ ] `make smoke` is green on three consecutive CI runs (I2)
- [ ] The comparison table in README has been reviewed for factual accuracy (no overclaiming)
- [ ] Bootstrap admin flow works in under 2 steps from server start (I1)
- [ ] Opening comment text is saved and ready to paste the moment the thread opens
- [ ] FAQ replies are in a doc ready to paste (this file)
- [ ] Someone is on comment duty for the first 4 hours (fastest response window matters most on HN)
- [ ] Someone is on standby to push a hotfix if a critical bug surfaces in the first 72 hours
- [ ] GitHub Discussions is enabled (or Issues configured) to catch non-HN questions
