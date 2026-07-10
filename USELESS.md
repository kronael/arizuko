# USELESS.md — the case against arizuko

This file argues arizuko is useless, overengineered, and replicates
what already exists. It is kept in-repo on purpose. Read it before
believing README.md. The method is borrowed from our own Agent
Research Hub (krons.fiu.wtf/pub/krons/agents/): every system gets
exactly one orthogonal component; the rest is filler. Applied to
ourselves, adversarially. A counter-section follows, then the parts
of the critique that survive the counter.

## 1. Someone already does each piece, arguably better

**Container isolation.** NanoClaw shipped container-by-default first —
we credit it in README.md §Thanks. Muaddib is a rung stronger: QEMU
micro-VMs where API keys physically cannot enter the guest. Edera is
stronger still: per-agent dedicated kernel under Type-1 Xen. Our
containers share the host kernel and one docker daemon. The hub's own
patterns research says containers alone don't stop breakouts
(agents/patterns-that-matter.md §1). We sit mid-spectrum and call it
a security model.

**Secrets boundary.** NemoClaw's inference proxy injects the key
host-side; the secret cannot exist in the sandbox. Centaur's
iron-proxy means the agent never holds a key. IronClaw's WASM tools
never see credential values (credential_injector.rs). We inject
secrets INTO the container at spawn (README.md §Security model).
Prompt injection can read ours. It cannot read theirs. The fix —
egred HTTPS-MITM swap at the boundary — is a spec, not code
(specs/8/Z-egred-mitm.md, "planned, not shipped").

**Channels.** OpenClaw routes 24+ channels from one Node process, no
message bus. Hermes speaks 16 platforms. We run 10 adapters as 10
daemons (teled, discd, slakd, mastd, bskyd, reditd, emaid, whapd,
twitd, linkd), each with its own token exchange, healthcheck, and
systemd unit. Per-channel operator cost is the highest in the survey.

**Route table.** Kanipi already had command/pattern/keyword/sender
routing with parent→child delegation, in TypeScript, in 13k LOC. We
rewrote it in Go and added daemons around it. ROUTING.md is a better
document than the mechanism is novel.

**Grants DSL.** Claude Code ships an 8-level permission hierarchy
exercised by millions of sessions. Our `[!]action(param=glob)` DSL is
bespoke, and we already logged OPA/Rego as its replacement candidate
when rules outgrow it (2026-05-22). A policy language you plan to
replace is scaffolding, not an asset.

**Skills.** Hermes has a skills marketplace, trust tiers, and a
932-LOC threat scanner (skills_guard.py) gating installs. Our skills
are folders plus a version bump; the scanner port is a spec
(specs/12/8-skill-guard.md), not code.

**Scale.** ElizaOS is the only horizontally-scalable system in the
survey (PostgreSQL + pgvector). We are one host, SQLite WAL,
single-writer per DB. The ceiling is one box, and it is load-bearing.

**Durability.** Centaur checkpoints every step and replays; DBOS does
it as a library. Our queue replays failures. Once it replayed a
broken spawn forever — the krons 2026-04-29 outage (CLAUDE.md, first
section). Their replay is the feature. Ours was the incident.

**Onboarding.** Every hosted platform does invites better. Ours has
admitted roughly a handful of humans, all known to the author.

## 2. The overengineering charges

- **17+ daemons for a single-host system.** authd, routd, runed,
  timed, onbod, webd, proxyd, dashd, plus ten adapters. OpenClaw
  covers the same user-visible surface with one process. Every daemon
  is a token exchange, a DB, a migration path, a failure mode.
- **N+M hand-rolled handlers, by policy.** CLAUDE.md admits the cost
  in its own words ("Cost is N+M hand-rolled handlers"). resreg exists
  so REST and MCP cannot drift. Drift happened anyway: proxyd's live
  resource said `routes` while catalog and OpenAPI said
  `proxyd_routes` (fixed 2026-07-01, CLAUDE.md).
- **228 spec files for ~69k non-test Go LOC.** Three specs per
  thousand lines. Specs are cheap to write and expensive to keep true;
  several above are the promissory notes the security story rests on.
- **172 skill migrations** to keep agent folders current with the
  platform — machinery no single-user rival needs at all.
- **Architecture churn.** The gated monolith (~8000 LOC) was deleted
  three months after it worked. The split that replaced it silently
  dropped every agent reply for ~2 days (fixed 91937baf) — routd
  recorded the turn and discarded the prose. Healthchecks stayed
  green. That is what N daemons buys when the contract between two of
  them slips.
- **Identity machinery.** We needed a CLAUDE.md rule, an env-var
  convention, and an outage (2026-04-29) to stop deriving a name from
  a path. One-process systems do not have this bug class.

The pattern: complexity is paid daily; several benefits are
promissory (git-as-truth: specs/9/3, spec; egred: spec; tenant
self-service: specs/5/32, spec).

## 3. Validation, honestly

Three instances: krons, sloth, marinade. All operated by the author.
Zero known external installs. The hub's bar for validated usefulness
is Stripe Minions at 1,300 PRs/week. By our own lens, arizuko's
validated-usefulness cell reads: autobiographical.

## 4. Where the critique fails

**The folder coordinate is real and nobody else has it.** One
primitive — the folder path — is simultaneously tenant, ACL scope,
route target, egress scope, web host, and file tree (ant/CLAUDE.md
§Tenancy; crackbox per-source-IP default-deny; auth.Authorize).
Run the hub's four lenses across the survey: OpenClaw has no
isolation; NanoClaw is group-safe but single-user; Muaddib's channels
share a VM; ElizaOS scales but runs plugins in-process; Hermes
profiles are not a boundary; IronClaw is explicitly single-owner
(FEATURE_PARITY.md). Multi-tenancy is the empty column of the whole
survey. arizuko is the only system here that serves sibling tenants
with per-tenant container + per-tenant egress allowlist + per-tenant
ACL on one box, same code from `solo/inbox` to `corp/eng/sre/oncall`.
That is the orthogonal component. It is real, and it is narrow.

**Same-plane coordination.** Agents delegate, schedule, and route by
posting messages to routd — the coordination bus is the conversation
plane users already occupy (README.md §Overview). No surveyed system
collapses these; they all grow a second bus or reject subagents.

**Agent-as-data, exercised.** An agent is a folder you can git-diff
and revert. 171 migration versions have shipped over live agents.
Hermes and ElizaOS configure agents; they don't make the agent BE a
file tree with a working upgrade broadcast.

**Ephemeral turn containers.** runed spawns per turn and tears down.
Nothing survives a turn except DB rows and folder files. Smaller
stateful surface than NanoClaw's per-session containers; the
container-exit race class disappears instead of being handled.

**And the daemons are the tenancy.** The overengineering charge in §2
assumes the single-process alternative serves the same need. It
doesn't: one process means one blast radius, which is exactly why
OpenClaw's multi-tenancy cell is empty. Some of the daemon count is
the price of the orthogonal component, not decoration. Not all of it.

## 5. What still stands after the counter

1. Secrets enter container memory. NemoClaw, Centaur, and IronClaw
   prove the stronger boundary is buildable. Ship egred or stop
   claiming the secrets story is a strength.
2. One host, one kernel, SQLite ceiling. Real tenants will ask.
3. One runtime (Claude Code). brainpro routes per task; Hermes routes
   on cost. We cannot.
4. Validation is autobiographical. One stranger running one instance
   is worth more than the next five daemons.
5. Spec debt: the three specs the security and tenancy pitch lean on
   (egred, git-as-truth, tenant self-service) are unshipped. Until
   then, parts of README.md §Direction are marketing by other means.

Verdict: not useless — misweighted. Over-built for the proven
audience (one operator), under-proven for the designed audience
(multi-tenant fleets). Everything that doesn't serve the folder
coordinate is a candidate for deletion; everything that does deserves
the next line of code.
