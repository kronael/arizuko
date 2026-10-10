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
state the agent and component contracts (`6/1`), give each a
conformance check a stranger's part must pass (`anteval` per backend;
a component probe), and let the folder coordinate (tenancy + egress +
ACL + web) be what the environment gives you for free. Wrapping stays
an on-ramp; the engine is an agentic reimplementation loop —
arizuko's self-learning pointed outward. Since 2026-08-27 it also
carries the operator cockpit (`6/28`–`6/33`): a daemon nobody can watch
alone has not shipped alone.

**Phase 7** is the cloud track: what a part may be written in (`7/22`),
what it is a participant in (`7/23`), how the agent inside it finds its
skills (`7/24`), and the package contract that lets an outsider ship one
(`7/25`). It sits after 6 because putting arizuko in the cloud comes after
splitting it into parts, and you cannot deploy a part you have not
separated. The operator cockpit (`6/28`–`6/33`) and the store model
(`6/37`) both sit with the split for the same reason: a part that cannot
be watched, or has not settled what it stores, has not shipped alone.
Products are phase 17.

**Phase 8** layers enterprise hardening: encryption at rest, audit
stream, per-daemon secrets, SSO/SAML, tool-level secret broker,
MITM-isolated egress. The trust primitives that make arizuko
credible to regulated buyers.

**Phase 9** owns the data model: tier separation (cold / warm / hot), which
is the vocabulary `CLAUDE.md` leans on for "every cold-tier entity is a resreg
resource". That is its one remaining action. MCP+REST unification belongs to
phase 5 (`5/17` the mechanism, `5/16` the rollout), and both ship. Git as
truth is DECLINED: `5/8` makes the SQLite database authoritative and YAML a
transport dump, so committing an `export` to git is fine and a
continuously-synced working tree is not the model (`specs/9/index.md`).

Together: phases 5 + 8 give **enterprise-ready** (capabilities +
trust); phase 9 gives **operationally minimal** (one storage
discipline, git as the universal versioned-data primitive). The
combination is the platform thesis arizuko ships toward.

## Phase table

| Phase      | Description                                                                                                                                                                                                                                   | Status     |
| ---------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- |
| [1/](1/)   | core gateway — routing, channels, auth, scheduler                                                                                                                                                                                             | shipped    |
| [2/](2/)   | social channels — events, actions, twitter                                                                                                                                                                                                    | shipped    |
| [3/](3/)   | permissions, cleanup, gaps                                                                                                                                                                                                                    | shipped    |
| [4/](4/)   | dashboards, memory, scheduler, personas — core architecture (channel protocol → `5/34`, ACL → `5/32`·`5/33`)                                                                                                                                  | shipped    |
| [5/](5/)   | platform core — surfaces, identity, routing, tenancy, runtime                                                                                                                                                                                 | shipped    |
| [6/](6/)   | split arizuko into parts — the environment contracts (`6/1`), the ship-in-parts catalogue (`6/16`), crackbox, the operator cockpit (`6/28`–`6/33`), the store model (`6/37`) , the QEMU substrate (`6/41`), DynamoDB over PostgreSQL (`6/42`) | **active** |
| [7/](7/)   | the cloud track — the Rust track, the habitat reframing, skill search, the package contract, the lambda target, and the minimum arizuko (`7/27`: a folder as files, four verbs, a skill); the cockpit and the store model sit with the split  | queued     |
| [8/](8/)   | enterprise hardening — encryption, audit, SSO, secret broker                                                                                                                                                                                  | queued     |
| [9/](9/)   | the data model — cold / warm / hot tier separation, and operator-extensible `<autocalls>`; git-as-truth is declined (`5/8`) and MCP+REST unification ships in `5/16`·`5/17`                                                                   | drafting   |
| [10/](10/) | self-healing — Aeon mechanism incorporation                                                                                                                                                                                                   | queued     |
| [11/](11/) | operator tools — branding, usage limits                                                                                                                                                                                                       | queued     |
| [12/](12/) | _dissolved 2026-07-14 → consolidated into [6/](6/) (`6/41`, `6/12`, `6/16`); self-learning/skill-guard → `5/22`·`5/23`_                                                                                                                       | dissolved  |
| [13/](13/) | standalone + reusable — ant, workflows, self-eval                                                                                                                                                                                             | planned    |
| [14/](14/) | future features — pinned, CLI, dynamic channels                                                                                                                                                                                               | planned    |
| [15/](15/) | later — committed direction, not scheduled                                                                                                                                                                                                    | planned    |
| [16/](16/) | multiplayer — shared sessions, durable streams, presence                                                                                                                                                                                      | drafting   |
| [17/](17/) | products — persona templates, publishing surface                                                                                                                                                                                              | queued     |

**A retired spec number is burnt.** Numbering continues past the highest ID its
phase has ever held; reusing one turns every old pointer to it into a
confidently wrong pointer rather than a dangling one. `specs/6/1` has been five
different specs and a phase-7 number six — that is the cost of not following
the rule. `tests/specrefs` checks that every link under `specs/` resolves and
that every backtick spec ID names a file.

**Status vocabulary.** `active` is the phase being built, and exactly ONE phase
holds it — the table said `active` on six phases at once, which expressed no
order at all. `queued` is committed and waiting its turn; `drafting` is still
being designed; `planned` is a placeholder with no specs written. Phase 5
closed 2026-08-15 (its last two carve-outs, `5/5` Open 2 and `5/28`'s
migrations, shipped as decisions); phase 6 took `active` the same day.

**The re-cut of 6 and 7, applied 2026-08-27.** Because phase 6 took `active`
the day phase 5 closed, every later arrival landed there whatever its subject,
and phase 7 held two tracks that shared a number and nothing else. The
operator's re-cut, refined 2026-08-16 to the order the work actually runs in:
**6 = split arizuko into parts** (the tools, the components, the contracts that
let a part ship alone), **7 = put the parts in the cloud.** Splitting comes
first because you cannot deploy a part you have not separated.

The cockpit is `6/28`–`6/33` and the store model `6/37`: an engine
decision settles what a part stores, which is a condition of shipping it
alone rather than of deploying it. The contracts, the strategy and the
ship-in-parts map are in phase 6 for the same reason. Destination numbers
start at 28 because every number below it has been used in phase 6 before —
reusing one turns an old pointer into a confidently wrong one, which is the
failure `L43` measured.
