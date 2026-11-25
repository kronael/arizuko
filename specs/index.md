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
resource". That is its one remaining action. MCP+REST unification shipped in
phase 5 (`resreg/README.md`). Git as truth is DECLINED: the store is
authoritative and YAML a transport dump (`cmd/arizuko/README.md`), so committing a `dump` to git is fine and a
continuously-synced working tree is not the model (`specs/9/index.md`).

Together: phases 5 + 8 give **enterprise-ready** (capabilities +
trust); phase 9 gives **operationally minimal** (one storage
discipline, git as the universal versioned-data primitive). The
combination is the platform thesis arizuko ships toward.

## Draft for review

| Spec                                                                     | Status | Covers                                                                       |
| ------------------------------------------------------------------------ | ------ | ---------------------------------------------------------------------------- |
| [6/48-github-feature-improvement.md](6/48-github-feature-improvement.md) | draft  | GitHub feature workflow placement, adoption decisions, and package revisions |

## Phase table

| Phase            | Description                                                                                                                                                                                           | Status     |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- |
| [3/](3/)         | permissions, cleanup, gaps                                                                                                                                                                            | shipped    |
| [4/](4/)         | dashboards, memory, scheduler, personas — core architecture (ACL → `5/33`)                                                                                                                            | shipped    |
| [5/](5/)         | platform core — surfaces, identity, routing, tenancy, runtime                                                                                                                                         | partial    |
| [6/](6/)         | split arizuko into parts — the environment contracts, crackbox, the QEMU substrate (`6/41`), DynamoDB over PostgreSQL (`6/42`)                                                                        | **active** |
| [7/](7/index.md) | single-world deployment — one core process and the functional minimum                                                                                                                                 | queued     |
| [8/](8/)         | enterprise hardening, packages, workflows and groups without agents                                                                                                                                   | queued     |
| [9/](9/)         | the data model — cold / warm / hot tiers, [state transitions](9/21-state-transitions.md), and operator-extensible `<autocalls>`; git-as-truth is declined and MCP+REST unification ships in `resreg/` | drafting   |
| [10/](10/)       | self-healing — Aeon mechanism incorporation                                                                                                                                                           | queued     |
| [11/](11/)       | operator tools — branding, usage limits                                                                                                                                                               | queued     |
| [12/](12/)       | _dissolved 2026-07-14 → consolidated into [6/](6/) (`6/41`); self-learning (`ARCHITECTURE.md` § Decided, and why) and skill-guard → `SECURITY.md`_                                                    | dissolved  |
| [13/](13/)       | standalone + reusable — ant, workflows, self-eval                                                                                                                                                     | planned    |
| [14/](14/)       | future features — pinned, CLI, dynamic channels                                                                                                                                                       | planned    |
| [15/](15/)       | later — committed direction, not scheduled                                                                                                                                                            | planned    |
| [16/](16/)       | multiplayer — shared sessions, durable streams, presence                                                                                                                                              | drafting   |
| [17/](17/)       | products — persona templates, publishing surface                                                                                                                                                      | queued     |

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

## Implementation closeout

The linked specs name each remaining item and the files it needs.

| Spec                                                                     | Status  | Summary                                                                                                                                                                                                                                                                                                                                                                        |
| ------------------------------------------------------------------------ | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| [6/53-per-group-harness.md](6/53-per-group-harness.md)                   | partial | Harness carry, validation, the dashboard fields, the anteval `harness` case field and the bulk-import carry test are built. Remaining: the dashboard write path (owner decision, L903), the disposable mixed-backend smoke (live) and the operator override/ACL review.                                                                                                        |
| [6/46-turn-progress.md](6/46-turn-progress.md)                           | partial | Shared reducer, native snapshots, bounded progress, one reminder and browser replay/deletion are tested. Remaining: a browser test of the built-in page script, the adoption sampler and measurement, the image build, channel acceptance and the Drive reply-edit probe.                                                                                                      |
| [6/50-anteval-ephemeral-instance.md](6/50-anteval-ephemeral-instance.md) | partial | Real disposable cleanup covers case failure and interruption, including account and tables. Guest callbacks, passing smoke/product runs, success cleanup and partial-startup cleanup remain.                                                                                                                                                                                   |
| [6/47-voluntary-reply.md](6/47-voluntary-reply.md)                       | partial | Addressed delivery, one shared nudge, hooks and fresh follow-up identity are tested. Sloth silence/reply/correction acceptance, disposable Codex smoke, the weekly nudge outcome measurement, the `<think>` convention in packaged skills and two docs naming deleted code remain.                                                                                             |
| [6/49-approval-ask.md](6/49-approval-ask.md)                             | partial | Approval text and declared standing keys share one renderer. The audit of stored skip-default copies remains.                                                                                                                                                                                                                                                                  |
| [6/42-dynamo-over-postgres.md](6/42-dynamo-over-postgres.md)             | partial | Driver, protocols, message search and SQL deletion are built; the store server is external. PostgreSQL-backed ExtendDB policy enforcement and race conformance pass, on SQLite and DynamoDB Local too. Remaining, all live: the soak, `make test` wall time, transaction latency and `EXPLAIN`, the opposite-order body on PostgreSQL-backed ExtendDB, the daemon image build. |
| [6/34-resident-prompt.md](6/34-resident-prompt.md)                       | partial | Resident manual and pointer guards exist; L188 conformance backfill and disposable guest-observable acceptance remain.                                                                                                                                                                                                                                                         |
| [6/41-qemu-substrate.md](6/41-qemu-substrate.md)                         | partial | Native boot and turn tests cover MCP, proxy policy, two-VM isolation, the SIGKILL sweep and cleanup; eight smoke cases pass on VM guests. Remaining, all live: local name resolution, deployment/memory acceptance, the EROFS guest test (written, not run) and balloon reporting under load.                                                                                  |
