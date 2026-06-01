# specs

## The story (phases 5 → 6 → 7)

**Phase 5** builds the platform's core capabilities: the surfaces
(MCP, REST, web, voice, WebDAV), identity (auth, ACL, JID format,
multi-account), routing (route table, topics, engagement, mentions,
webhooks), tenancy (org-chart, invites, user-spawned agents,
genericized daemons), and runtime (pipeline, middleware, modality).

**Phase 6** layers enterprise hardening on top of those
capabilities: encryption at rest, audit stream, per-daemon secrets,
SSO/SAML, tool-level secret broker, MITM-isolated egress. The trust
primitives that make arizuko credible to regulated buyers.

**Phase 7** delivers the operationally-minimal pivot: MCP+REST
unification (one mutation path), data-model tier separation
(cold / warm / hot), git-as-truth for the cold tier (audit,
history, fork, distribute — native git verbs replace bespoke
machinery). Secrets stay in SQLite; git carries only references.

Together: phases 5 + 6 give **enterprise-ready** (capabilities +
trust); phase 7 gives **operationally minimal** (one surface, one
storage discipline, git as the universal versioned-data primitive).
The combination is the platform thesis arizuko ships toward.

## Phase table

| Phase      | Description                                                       | Status   |
| ---------- | ----------------------------------------------------------------- | -------- |
| [1/](1/)   | core gateway — routing, channels, auth, scheduler                 | shipped  |
| [2/](2/)   | social channels — events, actions, twitter                        | shipped  |
| [3/](3/)   | permissions, cleanup, gaps                                        | shipped  |
| [4/](4/)   | dashboards, memory, web layer — core architecture                 | shipped  |
| [5/](5/)   | platform core — surfaces, identity, routing, tenancy, runtime     | active   |
| [6/](6/)   | enterprise hardening — encryption, audit, SSO, secret broker      | active   |
| [7/](7/)   | platform program — MCP+REST unification, data model, git-as-truth | drafting |
| [8/](8/)   | products — persona templates, publishing surface                  | active   |
| [9/](9/)   | self-healing — Aeon mechanism incorporation                       | active   |
| [10/](10/) | operator tools — branding, usage limits                           | active   |
| [11/](11/) | security + standalone — hardening, crackbox, mcp-fw               | active   |
| [12/](12/) | standalone + reusable — ant, workflows, self-eval                 | planned  |
| [13/](13/) | future features — pinned, CLI, dynamic channels                   | planned  |
| [14/](14/) | later — committed direction, not scheduled                        | planned  |
| [15/](15/) | multiplayer — shared sessions, durable streams, presence          | drafting |
