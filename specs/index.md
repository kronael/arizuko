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
carries the operator cockpit (`dashd/README.md`, `chanlib/README.md`): a daemon nobody can watch
alone has not shipped alone.

Phase 9 holds the unsigned persistent-machine, identity-held credential and
local-daemon proposals. These proposals are not phase-6 implementation claims.
**Phase 7** composes the core modules into one process for a single world,
and defines the functional minimum.

**Phase 8** layers enterprise hardening: encryption at rest, audit
stream, per-daemon secrets, SSO/SAML, tool-level secret broker,
MITM-isolated egress. The trust primitives make arizuko
credible to regulated buyers. Phase 8 also contains packages,
programmable turns and groups without agents.

**Phase 9** owns later features and the data model: tier separation (cold / warm / hot), which
is the vocabulary `CLAUDE.md` leans on for "every cold-tier entity is a resreg
resource". MCP+REST unification shipped in
phase 5 (`resreg/README.md`). Git as truth is DECLINED: the store is
authoritative and YAML a transport dump (`cmd/arizuko/README.md`), so committing a `dump` to git is fine and a
continuously-synced working tree is not the model (`specs/9/index.md`).

Together: phases 5 + 8 give **enterprise-ready** (capabilities +
trust); phase 9 gives **operationally minimal** (one storage
discipline, git as the universal versioned-data primitive). The
combination is the platform thesis arizuko ships toward.

## Phase table

| Phase            | Description                                                                                                                                        | Status     |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- |
| [3/](3/)         | permissions, cleanup, gaps                                                                                                                         | shipped    |
| [4/](4/)         | dashboards, memory, scheduler, personas — core architecture (ACL → `5/33`)                                                                         | shipped    |
| [5/](5/)         | platform core — surfaces, identity, routing, tenancy, runtime                                                                                      | partial    |
| [6/](6/)         | split arizuko into parts — the environment contracts, crackbox                                                                                     | **active** |
| [7/](7/index.md) | single-world deployment — one core process and the functional minimum                                                                              | queued     |
| [8/](8/)         | enterprise hardening, the QEMU substrate (`8/41`), DynamoDB over PostgreSQL (`8/42`), packages, workflows and groups without agents                | queued     |
| [9/](9/index.md) | Later features: persistent machines, personal model/tool accounts, local daemon discovery, and the cold / warm / hot data model.                   | drafting   |
| [10/](10/)       | self-healing — Aeon mechanism incorporation                                                                                                        | queued     |
| [11/](11/)       | operator tools — branding, usage limits                                                                                                            | queued     |
| [12/](12/)       | _dissolved 2026-07-14 → consolidated into [8/](8/) (`8/41`); self-learning (`ARCHITECTURE.md` § Decided, and why) and skill-guard → `SECURITY.md`_ | dissolved  |
| [13/](13/)       | standalone + reusable — ant, workflows, self-eval                                                                                                  | planned    |
| [14/](14/)       | future features — pinned, CLI, dynamic channels                                                                                                    | planned    |
| [15/](15/)       | later — committed direction, not scheduled                                                                                                         | planned    |
| [16/](16/)       | multiplayer — shared sessions, durable streams, presence                                                                                           | drafting   |
| [17/](17/)       | products — persona templates, publishing surface                                                                                                   | queued     |

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

**Composition boundaries.** Phase 6 defines independently usable components.
The [phase 9 index](9/index.md) holds the unsigned machine, personal-account
and service-discovery contracts with estimated costs and approval boundaries.

The [phase 7 index](7/index.md) holds single-world deployment and the
functional minimum. The [phase 8 index](8/index.md) holds packages,
programmable turns and groups without agents.

## Implementation closeout

The linked specs name each remaining item and the files it needs.

| Spec                                                         | Status  | Summary                                                                                                                                                                                                                                                                                                                                                                        |
| ------------------------------------------------------------ | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| [6/53-per-group-harness.md](6/53-per-group-harness.md)       | shipped | One harness document per group over the instance defaults; no other setting selects backend, model or effort, and dashd writes the document to the store itself. The default, pi and codex cases of `anteval/backends` passed on one disposable instance; the claude case waits for a credential (BUGS `L951`).                                                                |
| [6/46-turn-progress.md](6/46-turn-progress.md)               | partial | Shared reducer, native snapshots, bounded progress, one reminder and browser replay/deletion are tested, the page in Chrome. Measured: one temporary `<status>` message works; no checklist reaches a chat. Remaining: an image with the Claude task-tool opt-in and a new sample, the Codex plan tool (L936), the GitHub repository-chat notice (L938), the Drive probe.      |
| [6/49-approval-ask.md](6/49-approval-ask.md)                 | shipped | Approval text and declared standing keys share one renderer. krons and sloth store no copy of a skip default.                                                                                                                                                                                                                                                                  |
| [8/42-dynamo-over-postgres.md](8/42-dynamo-over-postgres.md) | partial | Driver, protocols, message search and SQL deletion are built; the store server is external. PostgreSQL-backed ExtendDB policy enforcement and race conformance pass, on SQLite and DynamoDB Local too. Remaining, all live: the soak, `make test` wall time, transaction latency and `EXPLAIN`, the opposite-order body on PostgreSQL-backed ExtendDB, the daemon image build. |
| [8/41-qemu-substrate.md](8/41-qemu-substrate.md)             | partial | Native boot and turn tests cover MCP, proxy policy, two-VM isolation, the SIGKILL sweep and cleanup; eight smoke cases pass on VM guests. Remaining, all live: local name resolution, deployment/memory acceptance, the EROFS guest test (written, not run) and balloon reporting under load.                                                                                  |
