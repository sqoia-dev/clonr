# clustr Show HN Launch Plan — 2026-07-27

**Owner:** Erlich (Angel Advisor / Launch Comms)
**Sprint:** I7
**Status:** DRAFT — week 1 of Sprint I
**Target launch date:** Sunday 2026-07-27
**Today:** 2026-04-28
**Weeks to launch:** ~13

This document is the WHEN/WHO/HOW around the Show HN post. It does NOT duplicate
Monica's I6 deliverables (post copy, FAQ pre-empts). It does NOT duplicate Jared's
I5 (first-run audit). It references D24 (powerhouse positioning thesis) and D29
(Sprint I selection rationale) throughout.

---

## 1. Posting Day and Time

### Primary slot

**Sunday 2026-07-27, 09:30–10:00 ET (06:30–07:00 PT)**

Rationale:

HN's front page is most accessible when the vote velocity needed to hold a top-10
position is lowest. The Sunday morning US window is the single best slot for an
open-source technical launch. The reasons are well-established in the HN community:

- Weekend traffic is lighter, so a smaller initial vote burst can propel a post into
  the visible zone.
- Sunday morning (ET) hits the US East Coast audience first, then rolls west — you
  get two waves of US morning readers before European mid-day traffic fades.
- The competitive field on Sundays is weaker. Show HN posts from solo engineers and
  small teams cluster on weekdays (Monday–Wednesday) because that is when founders
  are in "announce mode." The Sunday slot is underused.
- 09:30 ET gives the post roughly 30 minutes of early voting before the West Coast
  wakes up. By 11:00 ET you know whether it is gaining traction, with enough of the
  day left for the post to compound.

D16 already confirmed 2026-07-27 (Sunday) as the target. This slot is consistent
with D16 and should not be moved without a written counter-rationale.

### Backup slot

**Monday 2026-07-28, 08:00–08:30 ET (05:00–05:30 PT)**

If Sunday's posting is blocked (illness, a critical bug surfaces Saturday night, a
large competitor launches the same morning and dominates HN's top-10), move to
Monday morning before the US East Coast workday starts. Monday 08:00 ET is the
second-best window: serious readers are checking HN before standup, and the post
can build velocity through the work morning. The risk on Monday is higher competition
density; compensate by having three or four known community members ready to upvote
within the first 10 minutes.

### A note on algorithmic gaming and HN rules

Do NOT solicit upvotes in any coordinated way. HN's ranking algorithm penalizes
posts that receive an unusual cluster of votes in a short window from accounts
with low karma or no history of HN engagement. Ask only people who would genuinely
upvote after reading. A few authentic early upvotes from engaged HN regulars are
worth more — and safer — than a dozen from accounts who never comment.

The post title must follow Show HN format: `Show HN: <project name> — <one sentence>`.
Monica owns the exact title copy (I6). The title must not be changed between Monica's
draft and the actual post without both Monica and the founder reviewing.

---

## 2. Pre-Launch Checklist (T-13 weeks to T-1)

### Phase A: Foundation (T-13 to T-9, weeks 1–4 of Sprint I)

**Week 1 (2026-04-28 to 2026-05-04)**

- [ ] I7 launch plan (this document) authored and committed — Erlich
- [ ] I5 first-run audit dispatched — Jared begins `docs/getting-started-audit-2026-04.md`
- [ ] I6 README hero + Show HN draft dispatched — Monica begins `docs/SHOW-HN-DRAFT.md`
- [ ] I1 first-run install hardening dispatched — Dinesh
- [ ] Decide on I8 (hosted demo) — Monica + Erlich alignment call by end of week 1.
  Default is NO (I8 is correctly defaulted off per D29). Only reverse if Monica
  surfaces a concrete reason a 30-second GIF does not clear the bar.

**Week 2 (2026-05-05 to 2026-05-11)**

- [ ] Jared audit doc first draft ready; high-priority frictions identified
- [ ] README hero draft from Monica: above-the-fold pitch + "Why clustr" bullets
- [ ] Dinesh: `clustr-serverd doctor` subcommand shipped and passing in CI
- [ ] Erlich: identify 5–10 community targets for pre-launch warm outreach
  (see section 2 Community Targets below)

**Week 3 (2026-05-12 to 2026-05-18)**

- [ ] Monica: Show HN draft v1 committed to `docs/SHOW-HN-DRAFT.md`
- [ ] Monica: FAQ pre-empts drafted (auth model, why no SaaS, why Go, "this exists"
  response, cloud question)
- [ ] Dinesh: bootstrap admin flow (`clustr-serverd bootstrap-admin` or equivalent)
  working end-to-end on cloner dev host
- [ ] Erlich: first outreach messages drafted for sysadmin community contacts;
  DO NOT SEND until Phase C

**Week 4 (2026-05-19 to 2026-05-25) — Sprint I target ship date**

- [ ] Sprint I (v1.8.0) ships. Tag cut. CI green.
- [ ] README hero merged to main
- [ ] Show HN draft v2 reviewed by founder
- [ ] Smoke test (`make smoke`) green on 3 consecutive main pushes (I2 acceptance)
- [ ] WCAG AA and Lighthouse budgets enforced in CI (I3 acceptance)
- [ ] `docs/api.md` regenerated from current handler set (I9 acceptance)

### Phase B: Repo polish and community seeding (T-9 to T-5, weeks 5–8)

**Week 5 (2026-05-26 to 2026-06-01)**

- [ ] Repo README: CI status badge, license badge, Docker pull badge (GHCR pulls
  visible immediately after first pull)
- [ ] CONTRIBUTING.md: minimal — how to run tests, how to open a bug, DCO or CLA
  stance (none required for MIT, state clearly)
- [ ] GitHub topics set: `hpc`, `cluster-management`, `provisioning`, `slurm`,
  `bare-metal`, `pxe`, `golang`, `self-hosted`
- [ ] GitHub releases: confirm v1.0.0 through v1.7.0 release notes are clean and
  readable by a stranger (not just changelog-paste)
- [ ] Demo GIF: 15–30 seconds, shows the "stranger moment" — from portal login to
  a node appearing as provisioned. Record on the cloner dev host. Commit to
  `docs/assets/` or host on a CDN (not the prod Linode — keep the static site
  load separate from potential HN traffic).

**Week 6 (2026-06-02 to 2026-06-08)**

- [ ] Post to r/HPC and r/sysadmin: NOT a launch announcement. A genuine
  "I built this, looking for feedback" post. Title should be technical and
  honest ("I built a self-hosted HPC provisioning + allocation manager in Go
  — anyone interested in early feedback?"). This seeds the earliest community
  members who can comment with context on HN launch day.
- [ ] Post to relevant Discord/Slack communities (see targets below). Same tone —
  "feedback wanted," not "check out my product."
- [ ] No press, no Product Hunt, no Hacker News yet. Reddit and Discord first.
- [ ] Erlich: brief 2–3 community contacts (see targets below) with a one-paragraph
  summary of clustr. Goal: they know it exists, they understand why it's different
  from xCAT/Warewulf/ColdFront. Plant the seed. No ask yet.

**Week 7 (2026-06-09 to 2026-06-15)**

- [ ] Collect feedback from Reddit + Discord posts. Identify the top 3 objections
  or questions. Monica folds these into the FAQ pre-empts if not already covered.
- [ ] If r/HPC or r/sysadmin post gets traction (>20 upvotes, substantive comments),
  that is confirmation that the framing resonates. Document what specific framing
  landed.
- [ ] Founder: begin engaging on HN in unrelated threads (technical topics,
  Go programming, HPC, systems programming). This is not gaming HN — it is being
  a genuine member of the community. Karma and comment history matter for post
  credibility. If the posting account has zero comment history, the post starts
  with a credibility deficit.

**Week 8 (2026-06-16 to 2026-06-22)**

- [ ] Any critical fixes from Reddit/Discord feedback committed and shipped
- [ ] Demo GIF finalized — final cut approved by founder
- [ ] README hero: final version reviewed against Show HN draft for consistency
- [ ] Identify 1–2 HN regulars who are in the HPC/sysadmin/Go space and could
  amplify. Do not ask them to upvote. Ask if they would be willing to look at
  the repo before launch and give honest feedback. If they do, they become
  informed readers on launch day.

### Phase C: Final hardening and pre-launch warm outreach (T-5 to T-1, weeks 9–13)

**Week 9 (2026-06-23 to 2026-06-29)**

- [ ] All I1–I9 acceptance criteria confirmed green. No outstanding P1 issues.
- [ ] `docs/install.md` can be followed by a stranger in under 30 minutes
  (I1 acceptance confirmed by Jared's I5 audit)
- [ ] Freeze the codebase to hardening-only changes for the last 4 weeks before launch.
  No new features, no schema changes. Only bug fixes and doc improvements.

**Week 10 (2026-06-30 to 2026-07-06)**

- [ ] Warm outreach begins. Erlich sends personal messages (not mass email) to
  the 5–10 community contacts identified in Phase A. Message format:
  "Hey — I've been working on something I think you'd have opinions about.
  It's a self-hosted Go binary that handles bare-metal HPC provisioning AND
  allocation governance in one place — the xCAT/ColdFront gap. Would you take
  5 minutes to look at the repo before I post it on HN in a few weeks?"
  This is not a request to upvote. It is a request for feedback. The side effect
  is they know it exists and are primed to engage on launch day.
- [ ] Do NOT send outreach to more than 10 people. Beyond that, it looks like a
  vote ring. 5–8 genuine contacts is ideal.

**Week 11 (2026-07-07 to 2026-07-13)**

- [ ] Follow up on warm outreach responses. If a contact has substantive feedback,
  engage genuinely. If they identify a bug or UX issue, fix it.
- [ ] Final Show HN post copy reviewed: founder reads it aloud. If any sentence
  sounds like marketing, cut it.
- [ ] Confirm Monday 2026-07-28 is cleared as backup slot (no competing major
  engineering commits planned).

**Week 12 (2026-07-14 to 2026-07-20)**

- [ ] Code freeze. Only P0 bug fixes permitted.
- [ ] Confirm launch-day duty roster (Section 3).
- [ ] Erlich notifies 2–3 highest-quality contacts that the post goes up Sunday
  2026-07-27 morning. Not an ask — a heads-up.
- [ ] Founder sets alarm for 09:15 ET Sunday.

**Week 13 / T-1 (2026-07-21 to 2026-07-26)**

- [ ] Final smoke test on cloner dev host: fresh Docker Compose install from
  published README. Founder runs it personally.
- [ ] Confirm GitHub repo is public and all release artifacts are live.
- [ ] Monica's Show HN draft is copy-pasted into a staging document, ready to
  post. Title, body, and opening comment are finalized. Nothing left to write
  on launch morning.
- [ ] Confirm monitoring is running: GitHub repo notifications on, Uptime Kuma
  green, any demo URL (if I8 approved) stress-tested.
- [ ] Founder gets adequate sleep Saturday night.

---

## Community Targets for Pre-Launch Warm Outreach

These are categories, not named individuals, because the founder must identify
the right specific contacts from their own network. Do not cold-outreach someone
you have never interacted with.

**Target profile 1 — University HPC sysadmins**
These are the people who actually run xCAT/Warewulf deployments today. They are
the primary persona (D24). Find them through:
- ohpc-users mailing list (OpenHPC): public archive, many operators post with
  institutional affiliations
- Open OnDemand community (discourse.openondemand.org or the OSC Slack)
- SC (Supercomputing conference) community Slack or LinkedIn
- XSEDE/ACCESS network HPC staff

**Target profile 2 — HN regulars with HPC or systems background**
Look for HN accounts with comment history touching Slurm, LDAP, DHCP, PXE, bare
metal, Go, or "self-hosted." You can search Algolia HN search for these terms and
find recurring commenters. These people are credible voices on launch day.

**Target profile 3 — Go open-source community**
clustr is a Go binary. The Go community on HN is engaged and respected. If a
well-known Go community member finds the codebase interesting (clean design,
idiomatic Go, single binary), a comment from them carries enormous weight. Do not
pitch them on HPC — pitch them on the engineering: "single Go binary, SQLite,
no external dependencies, clean internal package structure."

**Target profile 4 — r/HPC and r/sysadmin regulars**
The Reddit post in week 6 will surface 3–5 users who engage substantively. These
are warm leads. By launch day they have context and may naturally upvote and comment
on HN without being asked.

**Hard limit: 5–10 warm contacts total.** Quality over quantity. An unsolicited
message to a stranger asking them to look at your repo is a cold outreach, not a
warm intro — and it rarely works on HN-caliber people.

---

## 3. Launch Day (T-0, Sunday 2026-07-27)

### Duty roster

| Role | Person | Responsibility |
|---|---|---|
| Post author | Founder | Submits the Show HN post at 09:30 ET. One-time action. |
| Comment responder | Founder | Primary voice in the HN thread. No one else comments in the thread under their own name. |
| Engineering standby | Dinesh | On-call for P0 bugs surfaced in comments. Has `git push` access and can ship a hotfix. |
| Infra standby | Gilfoyle | Watches server metrics, Uptime Kuma, any demo URL load. Can take action if infra is degraded. |
| GitHub issues triage | Jared | Monitors the GitHub issues queue. First-response to new issues within 2 hours on launch day. |
| Community monitor | Jared | Watches r/HPC, r/sysadmin, and relevant Discord/Slack for cross-posts or discussion. Does NOT cross-post without founder approval. |
| Social signal monitor | Monica | Watches HN comment score, Twitter/X engineering community, LinkedIn. Reports back to the group on tone and traction. |

### First 30 minutes (09:30–10:00 ET)

1. Founder submits the Show HN post (copy pre-staged from Monica's I6 draft).
2. Founder immediately posts the top comment in the thread — a 2–3 paragraph
   "author here" comment that adds context not in the title, acknowledges what
   the project does NOT do, and invites technical questions. This comment is
   pre-written by Monica (I6) and reviewed by the founder. It should read like
   a genuine engineer, not a press release.
3. The 2–3 warm contacts who were notified on T-7 days naturally check HN that
   morning. No further message is needed — they know it is Sunday.
4. Founder does NOT refresh compulsively. Check once at 09:45, once at 10:00.
   If the post has not gained traction by 10:00, that is normal — it is still
   very early. Do not panic-submit a second post.

### First 4 hours (10:00–13:30 ET)

- Founder engages with every substantive technical comment. Short, honest replies.
  The HN audience rewards directness and penalizes marketing language.
- Do not argue. If someone says "this already exists, xCAT does this" — respond
  with the specific delta, not with defensiveness. Pre-empts are in Monica's FAQ.
- Dinesh monitors GitHub for new issues opened by readers who try the install.
  If a P0 bug (install fails completely, daemon crashes on start) surfaces, Dinesh
  ships a hotfix immediately. The founder mentions it in the HN thread: "Good catch,
  fix is in v1.8.1 — thanks."
- Jared triages GitHub issues: tag as `bug`, `question`, `enhancement`. Respond to
  every new issue with a acknowledgment comment within 2 hours.
- Monitor for the post breaking the front page (top 30 on news.ycombinator.com).
  If it does, it will compound on its own. Do not intervene in the algorithm.

### First 24 hours (day-of through 2026-07-28 09:30 ET)

- Founder continues engaging in the thread as long as new substantive comments
  arrive. Drop off once the thread goes quiet (usually after 8–12 hours for a
  strong post).
- If the post did NOT make the front page by hour 4, it is unlikely to. Do not
  repost. Close the tab. See Section 6 (Failure Modes) for what to do next.
- Erlich notes which comments surfaced objections not in Monica's FAQ. These
  become input for Sprint J framing + future FAQ additions.
- Monica tracks the GitHub star count at T+1h, T+4h, T+12h, T+24h. This is the
  primary adoption signal.

### Comment-response rules (standing discipline)

1. Be the author, not the marketer. Personal voice, technical specifics.
2. Answer "how does X work" before answering "why did you build this."
3. If you do not know the answer, say so. "Good question, I haven't tested that
   configuration — if you try it, please open an issue."
4. Thank early adopters who report real usage. "That's a real deployment — I'd
   love to hear what breaks."
5. Trolls and dismissive one-liners: do not reply. Let the thread move on.
6. Never delete a comment. If you made a mistake, reply to correct it.
7. The "AI-written code" comment: see Section 5.

### Infrastructure watch on launch day

The primary risk on launch day is not server load — clustr's "product" is a
downloadable binary and GitHub repo. The Linode Nanode serves only the static
sqoia.dev page. GitHub handles the repo. GHCR handles the Docker image pulls.
These infrastructure components are not bottlenecks.

If I8 was approved (hosted demo): Gilfoyle must have rate-limiting in place before
launch. Maximum 10 concurrent sessions. If the demo is getting hammered,
redirect to a static screenshot page rather than let it degrade. Do not bring
down the demo gracefully in public — take the static fallback silently.

If I8 was NOT approved (default): no infra action needed on launch day.

---

## 4. Post-Launch (T+1 day to T+1 month)

### Week 1 post-launch (2026-07-28 to 2026-08-03)

**Duty roster:**

| Task | Owner | SLA |
|---|---|---|
| GitHub issue first response | Jared | Within 24 hours |
| Bug triage (P0/P1 classification) | Dinesh + Jared | Same-day |
| P0 hotfix (install-blocking bug) | Dinesh | Within 48 hours |
| PR review (external contributor) | Dinesh | Within 72 hours |
| HN thread follow-up comments | Founder | As needed, no SLA |
| Community cross-post engagement | Jared | Daily check |

**Triage cadence:**
- Day 1–3: all new issues get a response, even if just "triaging, thank you."
- Day 4–7: close duplicates, tag enhancements as Sprint J candidates, fix P0s.
- End of week 1: produce a one-paragraph summary of what the community surfaced.
  This feeds directly into Sprint J scoping.

### Follow-ups and amplification (T+1 to T+4 weeks)

**Blog post recap (T+1 week):**
Write a technical post-mortem of the launch: what the HN thread taught you,
which assumptions were confirmed or wrong, what you shipped in the hotfix.
Publish to GitHub Discussions or a simple sqoia.dev/blog post. This post gets
submitted to HN as a standard link post (not Show HN) and often catches a second
wave of readers who missed the original.

**Podcast appearances:**
Target the 3–5 podcasts that serve the sysadmin/HPC/open-source audience.
The goal is not mass reach — it is credibility-by-association. One well-placed
appearance on a show like "Changelog" or "FLOSS Weekly" or a university HPC center's
podcast (some CCRs run these) is worth more than ten smaller appearances.
Jared owns the outreach. Lead time is 4–8 weeks, so start the outreach in week 2
post-launch. Use the HN post as the calling card: "We got X upvotes on HN, here is
the link, we think the audience would find the engineering interesting."

**Conference CFPs:**
- SC2026 (Supercomputing 2026): CFP typically opens in late spring. If the HN
  launch generates traction, submit a BoF (Birds of a Feather) or a technical
  paper abstract for the Tools & Environments track. Target: submit by SC CFP
  deadline (typically July–August for SC held in November). This may be tight
  given the 2026-07-27 launch date — check the SC2026 CFP schedule immediately
  after launch.
- ISC High Performance 2027: CFP typically opens around October–November 2026 for
  a May 2027 conference. More runway post-launch. This is the better target for
  a first SC/ISC submission.
- PEARC (Practice and Experience in Advanced Research Computing): strong US
  university HPC community, more accessible for independent / small-team submissions.

**Do NOT submit to:**
- General software conferences (PyCon, GopherCon, KubeCon) unless the talk is
  specifically about the engineering (Go binary design, SQLite for embedded
  metadata, PXE protocol internals). Pitching "HPC management" to those audiences
  wastes a CFP slot.

### Adoption metrics that matter

These are the signals that tell you whether the HN launch actually produced
adoption vs. just attention:

| Metric | Where | Target (T+1 week) | Target (T+1 month) |
|---|---|---|---|
| GitHub stars | repo | 100+ | 500+ |
| GitHub forks | repo | 10+ | 50+ |
| Issues opened (non-bug) | repo | 5+ | 20+ |
| GHCR image pulls | ghcr.io/sqoia-dev/clustr | 50+ | 500+ |
| `install.md` page traffic | GitHub insights | top doc | top doc |
| Discord/Slack new members | if channel exists | N/A | create if demand exists |
| External blog mentions | search | 1+ | 3+ |

Stars are a vanity metric but are visible to the next wave of readers. GHCR pulls
are the signal that people are actually running it. Issues opened for feature
requests (not bugs) mean the audience sees a future in the project. Track these
weekly in the first month.

### Sprint J scoping from community feedback

The HN thread and the first week of GitHub issues will surface a short list of
things the community actually wants. Before Sprint J is dispatched:
- Erlich produces a one-paragraph characterization of the feedback themes
- Richard classifies each theme into D27 buckets (BUILD-NOW, TECH-TRIG, CUST-SPEC,
  SKIP)
- Sprint J scope is drafted from the BUILD-NOW items that community feedback elevated

This is the feedback loop that makes the HN launch compounding rather than one-shot.

---

## 5. Pre-Empts and Risks

### "This already exists — xCAT/Warewulf does this"

**Response framework (pre-write this in Monica's FAQ):**

xCAT and Warewulf handle node provisioning. ColdFront handles allocation governance.
No single open-source tool does both. clustr closes that specific gap in one binary.
If you are running xCAT today and happy with it, clustr is not trying to replace
your provisioning layer — it is adding the allocation governance layer you probably
built manually or are running ColdFront alongside. The framing is additive, not
replacement.

Do not say "clustr is better than xCAT." Say "clustr does something xCAT does not
do." The distinction matters on HN.

### "Why no SaaS?"

The answer is a feature, not a limitation. Air-gap deployments, export compliance,
institutional security policy, and HPC cluster topologies all require on-premises
software. The target persona (university HPC center sysadmin) cannot use SaaS for
cluster management — the compute is on-prem by institutional requirement. clustr
is deliberately self-hosted and will remain so. See D24.

This is also a positioning advantage: "we are not building a cloud business, we are
building a tool that works where cloud does not reach." HN appreciates principled
product decisions.

### "The code is AI-written"

This will come up. Every commit is authored as NessieCanCode (per CLAUDE.md standing
rule). On HN, this may surface as "is this a vibe-coded toy?"

The correct response is honest and direct: "The commit attribution is accurate — this
project uses an AI-assisted development workflow. The architecture decisions, product
design, and positioning are all human-authored. The code is tested, CI-enforced,
and you are welcome to review it. Judge it by whether it works, not by how it was
written."

Do not be defensive. Do not over-explain. Lean into the transparency. A significant
and growing portion of HN's technical audience is using AI-assisted dev and will
respect the honesty more than a deflection.

What you must NOT say: "The code is entirely human-written." It is not, and HN
commenters will find the commit log and call it out. Getting caught in a misrepresentation
on HN is far more damaging than owning the AI-assisted workflow from the start.

### "Where's the cloud support?"

Explicit non-goal per D24 and D27 (SKIP bucket). The answer is one sentence:
"clustr is designed for bare-metal HPC environments. Cloud resource allocation is
explicitly out of scope for v1.x." Do not apologize for the scope. The scope is
correct for the persona.

### "How is this safe? You are running DHCP and TFTP on my network."

This is the right question and deserves a substantive answer, not a dismissal.

- The signed-bundle trust chain means nodes only execute images signed by the
  operator's key. An adversary cannot inject a malicious image into the PXE queue
  without the operator's private key.
- Zero-egress architecture: the server does not phone home, does not pull external
  content at runtime, and does not require internet access post-install.
- RBAC is scoped: node-scoped API keys are auto-minted per-node and cannot escalate
  to admin access.
- `clustr-serverd doctor` (I1) surfaces misconfigurations before they become
  vulnerabilities.

Have the security answer ready. This question from a thoughtful sysadmin on HN
is an opportunity, not a threat.

### License questions

clustr is MIT licensed. State this clearly in the first sentence of any license
discussion. The AGPLv3 of ColdFront is a common source of confusion for operators
who want to know if they can modify and deploy without open-sourcing their
customizations. MIT removes that concern entirely.

### Trolls and bad-faith comments

Do not engage. Do not reply. Move on. If a comment is factually false and being
upvoted, a single calm correction is acceptable. One reply, not a thread.

---

## 6. Failure Modes

### HN shadowban / post gains no traction

HN can shadowban new accounts or flag posts as spam without notification. After
posting, verify the post is visible by opening HN in an incognito window. If the
post does not appear in `/newest` within 5 minutes, it may be shadowbanned.

Mitigation:
- The posting account should have established HN karma before launch day (see
  week 7 of the pre-launch plan).
- If shadowbanned: email HN team (hn@ycombinator.com) with the post URL. They
  are responsive and will delist shadowbans that were triggered in error.
- Backup: if HN fails entirely, post to r/HPC with "I was going to post this to
  HN but something went wrong — here's the project" framing. Not ideal, but the
  Reddit community is real and engaged.

### A competitor amplifies negative comments

Unlikely but possible (e.g., a vendor with a competing commercial product).
Do not engage. Let the technical community respond. HN readers are sophisticated
enough to identify vendor astroturfing and will backlash against it, not against
the target.

If the negative comments are technically accurate (a real bug, a real limitation),
acknowledge them honestly and explain the roadmap or the design tradeoff. Do not
treat accurate criticism as an attack.

### A critical bug surfaces in the first hour

If a bug that makes clustr non-functional for a common install path surfaces in
the HN thread:

1. Founder acknowledges it in the thread immediately: "Good catch — investigating."
2. Dinesh has the fix in < 2 hours. Ship v1.8.1 (patch bump per D28).
3. Founder returns to the thread: "Fix is live in v1.8.1 — apologies for the issue,
   thank you to [commenter] for catching it."

This response pattern — acknowledge fast, fix fast, thank the finder — is respected
on HN. The bug is not the story. The response is.

If the bug is a security vulnerability: treat it as a P0. Silence the thread comment
("we are investigating and will post an update") while Gilfoyle and Dinesh assess.
Do NOT post security details in the HN thread before the fix is confirmed and shipped.
Publish a brief post-mortem after the fix.

### Zero engagement (mediocre launch)

If the post gets under 20 upvotes and falls off the front page by hour 2, that is
not a death blow. It means one of:
- Framing was wrong (the title did not communicate the value)
- Timing was unlucky (a major story dominated that day)
- The audience is not on HN (possible for a niche HPC tool)

Mitigation plan:
1. Do not repost immediately. HN penalizes same-story reposts.
2. Wait 6–8 weeks. Ship a meaningful new version (Sprint J). Reframe the title.
3. Consider whether the primary channel should be the HPC-specific venues first
   (ohpc-users mailing list, SC BoF, OnDemand community) rather than HN. HN is
   high-variance for niche infrastructure tools. The HPC mailing lists are lower
   variance and reach the exact persona.
4. Erlich produces a two-paragraph debrief within 48 hours of a failed launch.
   The debrief drives Sprint J framing.

A zero-traction HN launch is recoverable. A security incident is much harder to
recover from. Protect the security reputation above all else.

---

## 7. Success Criteria

### "Good" outcome

- Post reaches the front page (top 30 on news.ycombinator.com) within the first
  4 hours.
- 100+ upvotes by end of launch day.
- 50+ comments, majority substantive and technical.
- 100+ GitHub stars in the first 24 hours.
- No P0 bugs surfaced that are not fixed within 2 hours.
- 3+ external people signal genuine interest in deploying it (GitHub issues with
  deployment questions, DMs, emails).

### "Great" outcome

- Post stays on the front page for 6+ hours.
- 300+ upvotes.
- 500+ GitHub stars in the first week.
- A recognizable name in the HN/Go/HPC community comments positively.
- An unsolicited blog post or tweet about clustr from someone outside the team
  within 48 hours.
- One university HPC center reaches out about production deployment.

### "We under-shipped — retry in 6 months"

- Post gets under 20 upvotes and no front-page traction.
- Zero GitHub stars from people outside the team.
- No substantive comments (only noise or dismissal).

If this outcome occurs:
- Do NOT repost the same framing.
- Sprint J must ship a meaningful new capability (not just polish).
- Revisit the title and opening comment — the framing may be wrong, not the product.
- Consider leading the re-launch with a technical blog post or conference talk first,
  so the Show HN post has supporting context a stranger can find.

---

## Appendix: The One Thing About HN You Cannot Forget

**HN readers are the most technically demanding early audience you will ever face.
They will read your code, run your installer, and find the bug you thought was
hidden. They are not the audience for a vision pitch — they are the audience for
a working product with a defensible design.**

The moment you over-claim (exaggerate what clustr does, describe a feature that
does not work, or use sales language in a technical thread), the thread becomes
about the exaggeration, not the product. HN has a long memory. A credible, honest,
technically specific post that undersells slightly and over-delivers on what a
stranger can actually run today will outperform a polished marketing post every
time.

Show them something that works. Let them find what is interesting about it.
Answer their questions like an engineer talking to another engineer. That is
the only playbook that works on HN.
