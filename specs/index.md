# specs

## Design directions are discussed here, and retired specs stay in git

Each design direction of arizuko is a spec in a phase directory, `specs/<N>/`,
and the phase table below names each phase. A spec leaves the tree when it is
retired: its content goes into the repository docs, or the decision is
declined. git keeps every deleted spec. To list the deleted specs and the
commit that deleted each one:

```bash
git log --diff-filter=D --name-only --format='%h %ad %s' --date=short -- specs/
```

To read one deleted spec, find the commit that deleted it, then show the file
as it was in the commit before:

```bash
git log --diff-filter=D -1 --format=%h -- specs/6/20-sandbox-lifecycle-decline.md
git show 3a655b33^:specs/6/20-sandbox-lifecycle-decline.md
```

## The story (phases 5 → 6 → 7 → 8 → 9)

**Phase 5** builds the platform's core capabilities: the surfaces
(MCP, REST, web, voice, WebDAV), ingress (the channel adapter contract,
webhook routes), identity (auth, ACL, JID format, multi-account), routing (route table, topics, engagement, mentions,
webhooks), tenancy (org-chart, invites, user-spawned agents,
genericized daemons), and runtime (pipeline, middleware, modality).

**Phase 6** is adoption & interop: the end goal is that people run
arizuko instead of their own agent harness. The path is publishing the
environment, not wrapping the harness (inverted 2026-08-15; `ARCHITECTURE.md` § Decided, and why):
state the agent and component contracts, give each a
conformance check a stranger's part must pass (`anteval` per backend;
a component probe), and let the folder coordinate (tenancy + egress +
ACL + web) be what the environment gives you for free. Wrapping stays
an on-ramp; the engine is an agentic reimplementation loop —
arizuko's self-learning pointed outward. Since 2026-08-27 it also
carries the operator cockpit (`6/28`–`6/33`): a daemon nobody can watch
alone has not shipped alone.

Phase 6 also defines the agent's customizable persistent machine and runtime,
and identity-held model credentials and tool accounts.
**Phase 7** composes the core modules into one process for a single world,
and defines the functional minimum.

**Phase 8** layers enterprise hardening: encryption at rest, audit
stream, per-daemon secrets, SSO/SAML, tool-level secret broker,
MITM-isolated egress. The trust primitives make arizuko
credible to regulated buyers. Phase 8 also contains packages,
programmable turns and groups without agents.

**Phase 9** owns the data model: tier separation (cold / warm / hot), which
is the vocabulary `CLAUDE.md` leans on for "every cold-tier entity is a resreg
resource". That is its one remaining action. MCP+REST unification belongs to
phase 5 (`5/17` the mechanism, `5/16` the rollout), and both ship. Git as
truth is DECLINED: `5/8` makes the store authoritative and YAML a
transport dump, so committing a `dump` to git is fine and a
continuously-synced working tree is not the model (`specs/9/index.md`).

Together: phases 5 + 8 give **enterprise-ready** (capabilities +
trust); phase 9 gives **operationally minimal** (one storage
discipline, git as the universal versioned-data primitive). The
combination is the platform thesis arizuko ships toward.

## Phase table

| Phase      | Description                                                                                                                                                                                                                           | Status     |
| ---------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- |
| [3/](3/)   | permissions, cleanup, gaps                                                                                                                                                                                                            | shipped    |
| [4/](4/)   | dashboards, memory, scheduler, personas — core architecture (channel protocol → `5/34`, ACL → `5/32`·`5/33`)                                                                                                                          | shipped    |
| [5/](5/)   | platform core — surfaces, identity, routing, tenancy, runtime                                                                                                                                                                         | partial    |
| [6/](6/)   | split arizuko into parts — the environment contracts, the ship-in-parts catalogue (`6/16`), crackbox, the operator cockpit (`6/28`–`6/33`), the store model (`6/37`) , the QEMU substrate (`6/41`), DynamoDB over PostgreSQL (`6/42`) | **active** |
| [7/](7/)   | single-world deployment — one core process and the functional minimum                                                                                                                                                                 | queued     |
| [8/](8/)   | enterprise hardening, packages, workflows and groups without agents                                                                                                                                                                   | queued     |
| [9/](9/)   | the data model — cold / warm / hot tier separation, and operator-extensible `<autocalls>`; git-as-truth is declined (`5/8`) and MCP+REST unification ships in `5/16`·`5/17`                                                           | drafting   |
| [10/](10/) | self-healing — Aeon mechanism incorporation                                                                                                                                                                                           | queued     |
| [11/](11/) | operator tools — branding, usage limits                                                                                                                                                                                               | queued     |
| [12/](12/) | _dissolved 2026-07-14 → consolidated into [6/](6/) (`6/41`, `6/12`, `6/16`); self-learning (`ARCHITECTURE.md` § Decided, and why) and skill-guard → `5/23`_                                                                           | dissolved  |
| [13/](13/) | standalone + reusable — ant, workflows, self-eval                                                                                                                                                                                     | planned    |
| [14/](14/) | future features — pinned, CLI, dynamic channels                                                                                                                                                                                       | planned    |
| [15/](15/) | later — committed direction, not scheduled                                                                                                                                                                                            | planned    |
| [16/](16/) | multiplayer — shared sessions, durable streams, presence                                                                                                                                                                              | drafting   |
| [17/](17/) | products — persona templates, publishing surface                                                                                                                                                                                      | queued     |

**A retired spec number is burnt.** Numbering continues past the highest ID its
phase has ever held; reusing one turns every old pointer to it into a
confidently wrong pointer rather than a dangling one. One phase-6 number has been five
different specs and a phase-7 number six — that is the cost of not following
the rule. `tests/specrefs` checks that every link under `specs/` resolves and
that every backtick spec ID names a file.

**Status vocabulary.** `active` is the phase being built, and exactly ONE phase
holds it — the table said `active` on six phases at once, which expressed no
order at all. `queued` is committed and waiting its turn; `drafting` is still
being designed; `planned` is a placeholder with no specs written. `partial`
marks a phase with unfinished implementation or acceptance. Phase 6 is active.

**Composition boundaries.** Phase 6 defines independently usable components and the agent's persistent machine.

## Phase 6 defines the agent's machine and identity-held credentials

| Spec                                                                   | Status | Summary                                                                                                                           |
| ---------------------------------------------------------------------- | ------ | --------------------------------------------------------------------------------------------------------------------------------- |
| [6/54-agent-machine.md](6/54-agent-machine.md)                         | draft  | A Unix user reaches a customizable persistent machine through SSH; private disk, installed software and explicit reset.           |
| [6/55-identity-held-credentials.md](6/55-identity-held-credentials.md) | draft  | A user's model subscription pays for the turns that user starts; delivery, enrollment and internal funding await owner decisions. |
| [6/56-identity-held-tools.md](6/56-identity-held-tools.md)             | queued | Tools act as a user's OAuth identity only under an explicit grant; the agent's own login stays the default.                       |

The [phase 7 index](7/index.md) holds single-world deployment and the
functional minimum. The [phase 8 index](8/index.md) holds packages,
programmable turns and groups without agents.

## ACL layers use one evaluator

| Spec                                     | Status  | Summary                                                                                                                                |
| ---------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| [6/52-acl-layers.md](6/52-acl-layers.md) | shipped | Local ACL rows override release defaults through one evaluator. Delegated rule mutations check authority and prove the policy version. |

## Implementation closeout

The linked specs name each remaining item and the files it needs.

| Spec                                                                     | Status  | Summary                                                                                                                                                                                                                |
| ------------------------------------------------------------------------ | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [6/53-per-group-harness.md](6/53-per-group-harness.md)                   | partial | Harness carry, validation, dashboard PATCH and manuals are complete. Operator override/ACL audit, image and disposable mixed-backend smoke remain.                                                                     |
| [6/46-turn-progress.md](6/46-turn-progress.md)                           | partial | Shared reducer, native snapshots, bounded progress, one reminder and browser replay/deletion are tested. Operator docs, image build, channel acceptance and adoption measurement remain.                               |
| [6/50-anteval-ephemeral-instance.md](6/50-anteval-ephemeral-instance.md) | partial | Disposable CLI lifecycle and cleanup have fake-based tests. Startup capacity and resource-allocation checks, guest callback reachability and real smoke/product/cleanup acceptance remain.                             |
| [5/34-channel-protocol.md](5/34-channel-protocol.md)                     | shipped | Adapter protocol, URI route targets, shared expansion, validation and migrations are tested. The observation eval and operator examples use query mode flags and topic templates.                                      |
| [6/27-git-channel.md](6/27-git-channel.md)                               | partial | Repository-qualified topics and sessions are tested. App replies, installation-token tools, revocation, source dashboard and live acceptance remain; repository workspaces/lifecycle need approval and implementation. |
| [6/47-voluntary-reply.md](6/47-voluntary-reply.md)                       | partial | Addressed delivery, one shared nudge, hooks and fresh follow-up identity are tested. Sloth silence/reply/correction acceptance, disposable Codex smoke and the weekly nudge outcome measurement remain.                |
| [6/49-approval-ask.md](6/49-approval-ask.md)                             | partial | Approval text and declared standing keys share one renderer. Stored row audit and two owner decisions remain.                                                                                                          |
| [6/42-dynamo-over-postgres.md](6/42-dynamo-over-postgres.md)             | partial | Driver, object protocols, BM25 and SQL deletion are built. Enforcing-server policy tests (L318), dual-server conformance, disposable smoke and search/soak acceptance remain.                                          |
| [6/43-shared-store-server.md](6/43-shared-store-server.md)               | partial | External enrollment, strict configuration refusal and setup/backup docs are complete. Isolated move acceptance and L318 enforcement remain.                                                                            |
| [6/34-resident-prompt.md](6/34-resident-prompt.md)                       | partial | Resident manual and pointer guards exist; L188 conformance backfill and disposable guest-observable acceptance remain.                                                                                                 |
| [6/41-qemu-substrate.md](6/41-qemu-substrate.md)                         | partial | Native boot and turn tests cover MCP, proxy policy, steering and cleanup. Guest smoke (L379), two-VM isolation, abrupt-crash sweep, two-release retention and deployment/memory acceptance remain.                     |
