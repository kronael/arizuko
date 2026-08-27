# specs

## The story (phases 5 → 6 → 7 → 8 → 9)

**Phase 5** builds the platform's core capabilities: the surfaces
(MCP, REST, web, voice, WebDAV), ingress (the channel adapter contract,
webhook routes), identity (auth, ACL, JID format, multi-account), routing (route table, topics, engagement, mentions,
webhooks), tenancy (org-chart, invites, user-spawned agents,
genericized daemons), and runtime (pipeline, middleware, modality).

**Phase 6** is adoption & interop: the end goal is that people run
arizuko instead of their own agent harness. The path is publishing the
environment, not wrapping the harness (`6/1`, inverted 2026-08-15):
state the agent and component contracts (`6/7`), give each a
conformance check a stranger's part must pass (`anteval` per backend;
a component probe), and let the folder coordinate (tenancy + egress +
ACL + web) be what the environment gives you for free. Wrapping stays
an on-ramp; the engine is an agentic reimplementation loop —
arizuko's self-learning pointed outward.

**Phase 7** is the operator cockpit: every daemon serves its own
dashboard (HTMX, its own `/dash/<daemon>/` namespace, source served
by the daemon itself) that observes AND controls all its aspects via
its `/v1` surface; a lean dashd hub probes and links them, AWS-console
style. Products move to phase 17. It also now carries **the cloud
track** (`7/21` storage seam), which sits here because putting arizuko
in the cloud comes after splitting it into parts — phase 6's job.

**Phase 8** layers enterprise hardening: encryption at rest, audit
stream, per-daemon secrets, SSO/SAML, tool-level secret broker,
MITM-isolated egress. The trust primitives that make arizuko
credible to regulated buyers.

**Phase 9** delivers the operationally-minimal pivot: data-model tier
separation (cold / warm / hot) and git-as-truth for the cold tier (audit,
history, fork, distribute — native git verbs replace bespoke machinery).
Secrets stay in SQLite; git carries only references. Its first action —
MCP+REST unification (one mutation path) — was pulled forward to phase 5
(`5/17` mechanism + `5/16` rollout) and both have shipped; the remaining
phase-9 actions are the data model and git-as-truth.

Together: phases 5 + 8 give **enterprise-ready** (capabilities +
trust); phase 9 gives **operationally minimal** (one storage
discipline, git as the universal versioned-data primitive). The
combination is the platform thesis arizuko ships toward.

## Phase table

| Phase      | Description                                                                                                           | Status     |
| ---------- | --------------------------------------------------------------------------------------------------------------------- | ---------- |
| [1/](1/)   | core gateway — routing, channels, auth, scheduler                                                                     | shipped    |
| [2/](2/)   | social channels — events, actions, twitter                                                                            | shipped    |
| [3/](3/)   | permissions, cleanup, gaps                                                                                            | shipped    |
| [4/](4/)   | dashboards, memory, scheduler, personas — core architecture (channel protocol → `5/34`, ACL → `5/32`·`5/33`)          | shipped    |
| [5/](5/)   | platform core — surfaces, identity, routing, tenancy, runtime                                                         | shipped    |
| [6/](6/)   | adoption & interop + components — the environment contracts (`6/7`), the ship-in-parts catalogue (`6/16`), crackbox   | **active** |
| [7/](7/)   | operator cockpit + the cloud track (`7/21` storage seam) — two tracks sharing a number until `L43` is repaired        | queued     |
| [8/](8/)   | enterprise hardening — encryption, audit, SSO, secret broker                                                          | queued     |
| [9/](9/)   | platform program — data model, git-as-truth (MCP+REST unification → 5/16·5/17)                                        | drafting   |
| [10/](10/) | self-healing — Aeon mechanism incorporation                                                                           | queued     |
| [11/](11/) | operator tools — branding, usage limits                                                                               | queued     |
| [12/](12/) | _dissolved 2026-07-14 → consolidated into [6/](6/) (`6/8`–`6/12`, `6/16`); self-learning/skill-guard → `5/22`·`5/23`_ | dissolved  |
| [13/](13/) | standalone + reusable — ant, workflows, self-eval                                                                     | planned    |
| [14/](14/) | future features — pinned, CLI, dynamic channels                                                                       | planned    |
| [15/](15/) | later — committed direction, not scheduled                                                                            | planned    |
| [16/](16/) | multiplayer — shared sessions, durable streams, presence                                                              | drafting   |
| [17/](17/) | products — persona templates, publishing surface                                                                      | queued     |

**Retired IDs live in [`RETIRED.md`](RETIRED.md).** A spec that is deleted or
renumbered leaves its pointers behind in `BUGS.md`, `CHANGELOG.md`, code
comments and the web docs; that table says what each retired ID was and where a
reader goes now. A number listed there is burnt — numbering continues past the
highest ID its phase has ever held, because reusing one turns an old pointer
into a confidently wrong one rather than a dangling one. `tests/specrefs`
enforces both halves.

**Status vocabulary.** `active` is the phase being built, and exactly ONE phase
holds it — the table said `active` on six phases at once, which expressed no
order at all. `queued` is committed and waiting its turn; `drafting` is still
being designed; `planned` is a placeholder with no specs written. Phase 5
closed 2026-08-15 (its last two carve-outs, `5/5` Open 2 and `5/28`'s
migrations, shipped as decisions); phase 6 took `active` the same day.

**Open — a re-cut of 6 and 7 (2026-08-16, recorded, not applied).** Because
phase 6 took `active` the day phase 5 closed, every later arrival landed there
whatever its subject: 7 of its 11 specs are platform work, not adoption, and
say so in their own frontmatter (`6/19`
depends on `5/13`+`5/14`). The operator's re-cut is **6 = finish and harden the
platform**, absorbing the phase-7 cockpit, and **7 = adoption** — contracts,
conformance, ship-in-parts, cloud. Refined 2026-08-16 to the order the work
actually runs in: **6 = split arizuko into parts** (the tools, the components,
the contracts that let a part ship alone), **7 = put the parts in the cloud**.
Splitting comes first because you cannot deploy a part you have not separated.
`7/21` moved under that reading. Only four of today's phase-6 specs (`1`,
`7`, `16`, `8`) belong to adoption. Not applied here because spec IDs are
stable identifiers: they appear 222 times across 64 files including `BUGS.md`,
`CHANGELOG.md` and diary history, and `6/1`/`7/1` collide, so moving them is a
deliberate pass with its own verification. Also drifting: `specs/7/index.md`
says `status: active` while the table above says `queued`. See
[`6/index.md`](6/index.md) §"Open".
