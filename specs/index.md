# specs

## The story (phases 5 → 6 → 7 → 8 → 9)

**Phase 5** builds the platform's core capabilities: the surfaces
(MCP, REST, web, voice, WebDAV), identity (auth, ACL, JID format,
multi-account), routing (route table, topics, engagement, mentions,
webhooks), tenancy (org-chart, invites, user-spawned agents,
genericized daemons), and runtime (pipeline, middleware, modality).

**Phase 6** is adoption & interop: the end goal is that people run
arizuko instead of their own agent harness. Not "rip out your stack
and switch" — wrap what you already run. Make runed's runtime
swappable so another harness runs inside an arizuko folder, import its
config/skills as-is, and let the folder coordinate (tenancy + egress +
ACL + web) be the layer above the harness. The engine is an agentic
reimplementation loop — arizuko's self-learning pointed outward.

**Phase 7** is the operator cockpit: every daemon serves its own
dashboard (HTMX, its own `/dash/<daemon>/` namespace, source served
by the daemon itself) that observes AND controls all its aspects via
its `/v1` surface; a lean dashd hub probes and links them, AWS-console
style. Products move to phase 17.

**Phase 8** layers enterprise hardening: encryption at rest, audit
stream, per-daemon secrets, SSO/SAML, tool-level secret broker,
MITM-isolated egress. The trust primitives that make arizuko
credible to regulated buyers.

**Phase 9** delivers the operationally-minimal pivot: data-model tier
separation (cold / warm / hot) and git-as-truth for the cold tier (audit,
history, fork, distribute — native git verbs replace bespoke machinery).
Secrets stay in SQLite; git carries only references. Its first action —
MCP+REST unification (one mutation path) — was pulled forward to phase 5 as
active work (`5/45` mechanism + `5/44` rollout); the remaining phase-9
actions are the data model and git-as-truth.

Together: phases 5 + 8 give **enterprise-ready** (capabilities +
trust); phase 9 gives **operationally minimal** (one storage
discipline, git as the universal versioned-data primitive). The
combination is the platform thesis arizuko ships toward.

## Phase table

| Phase      | Description                                                                    | Status   |
| ---------- | ------------------------------------------------------------------------------ | -------- |
| [1/](1/)   | core gateway — routing, channels, auth, scheduler                              | shipped  |
| [2/](2/)   | social channels — events, actions, twitter                                     | shipped  |
| [3/](3/)   | permissions, cleanup, gaps                                                     | shipped  |
| [4/](4/)   | dashboards, memory, web layer — core architecture                              | shipped  |
| [5/](5/)   | platform core — surfaces, identity, routing, tenancy, runtime                  | active   |
| [6/](6/)   | adoption & interop — runtime pluralism, import, agentic reimplementation loop  | drafting |
| [7/](7/)   | operator cockpit — per-daemon dashboards + global hub                          | active   |
| [8/](8/)   | enterprise hardening — encryption, audit, SSO, secret broker                   | active   |
| [9/](9/)   | platform program — data model, git-as-truth (MCP+REST unification → 5/44·5/45) | drafting |
| [10/](10/) | self-healing — Aeon mechanism incorporation                                    | active   |
| [11/](11/) | operator tools — branding, usage limits                                        | active   |
| [12/](12/) | security + standalone — hardening, crackbox, mcp-fw                            | active   |
| [13/](13/) | standalone + reusable — ant, workflows, self-eval                              | planned  |
| [14/](14/) | future features — pinned, CLI, dynamic channels                                | planned  |
| [15/](15/) | later — committed direction, not scheduled                                     | planned  |
| [16/](16/) | multiplayer — shared sessions, durable streams, presence                       | drafting |
| [17/](17/) | products — persona templates, publishing surface                               | active   |
