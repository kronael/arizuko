---
status: shipped
---

# Adoption — publish the environment, not the wrap

arizuko is not a harness. It is orchestration: it decides how agents run and for
whom — routing, tenancy, identity, permissions — the layer _above_ any harness.
The adoption thesis (operator decision 2026-08-15): **arizuko publishes an
environment, and the value is what the environment gives you for free.**
Wrapping a foreign harness stays available as an on-ramp; it is not the thesis.

## Problem

The field is crowded, and it is not a field of single-user harnesses.
Three open-source projects orchestrate agents across people and channels today
(measured below), and each ships two or three of arizuko's four defining
properties. Competing with a harness head-to-head is a category error;
competing with an orchestrator on the properties it already has is a losing
one. The thesis survives on one property, and the adoption work is to make
that one property the contract a stranger can occupy.

Two things make the layer worth adopting (`specs/5/A`, `USELESS.md` §4):

1. **The folder coordinate.** One path is tenant, ACL, route, egress, web host,
   and file tree — how agents run and for whom, on one box, the same code from
   `solo/inbox` to `corp/eng/sre/oncall`.
2. **Agents reshape the system; you stay in the loop.** The persona, skills,
   routing, even child groups are files the LLM edits — yet every change is a
   file you diff, a grant that gates it, and (if you `git init` the group) a
   `git revert` away. Self-modifying, human-owned.

## The field, measured by property (2026-09-05)

Four properties, each yes/no against the project's docs or source (never a
README unless marked): **a** — multi-tenant with a hierarchical scope, not a
flat org→user model; **b** — any harness (Claude Code, codex, pi) as a
swappable executor; **c** — inbound from several chat platforms normalized to
one shape; **d** — outbound only through permissioned tools, the model's plain
reply not auto-posted.

| Project                                                                   | a    | b    | c       | d    | License                         | Decisive source                                                                                                                                                                                        |
| ------------------------------------------------------------------------- | ---- | ---- | ------- | ---- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `openclaw/openclaw`                                                       | no   | yes  | yes     | mode | MIT                             | `docs/gateway/operator-scopes.md`: scopes are "not hostile multi-tenant isolation"; `docs/concepts/agent-runtimes.md`: `pi`, `codex`, `claude-cli` runtimes; ~30 channels behind one `message` adapter |
| `yc-software/qm`                                                          | no   | yes  | part    | yes  | MIT                             | `src/core/orchestrator/surface-tools.ts` (`post` … `staySilent`); scope kinds are a closed flat set; Slack + own web only                                                                              |
| `paperclipai/paperclip`                                                   | part | yes  | no      | yes  | MIT                             | `doc/MCP-ACCESS-GOVERNANCE.md`: one policy engine for the agent tool call and the human approval; scope is a fixed five-level lattice; no chat ingress (issues #7, #49)                                |
| `letta-ai/letta-code`                                                     | no   | no   | yes     | yes  | Apache-2.0                      | `src/channels/README.md` (reply only via `MessageChannel`); server auth is one shared password (docs.letta.com/guides/selfhosting)                                                                     |
| `NousResearch/hermes-agent`                                               | no   | part | yes     | no   | MIT                             | replies delivered "without special handling from the agent"; `SECURITY.md` names a single trusted operator                                                                                             |
| Dust                                                                      | no   | no   | part    | no   | —                               | `connectors/src/connectors/slack/bot.ts` streams the answer; Workspace → Space → Role, flat (docs.dust.tt access-controls)                                                                             |
| Onyx                                                                      | no   | no   | part    | no   | —                               | `backend/onyx/onyxbot/slack/handlers/handle_regular_answer.py` posts the answer itself                                                                                                                 |
| Chatwoot                                                                  | no   | no   | yes     | part | MIT + proprietary `enterprise/` | Captain posts the reply (`enterprise/…/response_builder_job.rb`); AgentBot is webhook-in, REST-out; account → inbox → role, flat                                                                       |
| AnythingLLM                                                               | no   | no   | part    | no   | MIT                             | docs.anythingllm.com/channels/telegram: the connector "only works in single-user mode … multi-user mode will automatically disconnect the bot"                                                         |
| Coder Tasks, Ona, OpenHands Cloud, Devin, Claude in Slack, Managed Agents | no   | no   | no/part | no   | mixed                           | each is one harness on one ingress; Coder states it is "not a wrapper around third-party agent tools like Claude Code or Codex"                                                                        |

Two readings, both required:

- **Properties b, c and d are commodity.** openclaw has all three of them (d
  as an opt-in mode with fallbacks), qm has b and d, Paperclip has b
  and d. Claiming any of the three as arizuko's difference is claiming a
  commodity, which `5/A` forbids.
- **Property a is unshipped anywhere found.** The nearest are Paperclip's fixed
  five-level lattice and Keycloak's slash-separated group paths (`6/16`), and
  neither binds the path to a mount, an egress allowlist or a web prefix. In
  the field the two halves even exclude each other inside one product
  (AnythingLLM, above). That is the thesis, and it is narrow: **one path binds
  tenant, grant, route, egress, web host and file tree, and one evaluator
  applies it to the agent over MCP and to the human over REST.**

The strongest single "adopt this instead" candidate is openclaw. A deployer who
does not need tenancy with depth should be told so; a deployer who does has no
off-the-shelf answer, and that is the only audience the thesis addresses.

## The thesis — the environment IS the product

The Lambda analogy is the argument. Lambda never emulated your server. It
published a contract — handler, event, context, and a role the SDK finds on its
own — and the value is what the environment gives you for free: isolation,
IAM, scaling. You give up generality and gain the platform. **You write to
their APIs; they do not reimplement yours.**

arizuko has two such environments — the **agent environment** (event + tools +
home) and the **component environment** (what a daemon gets and owes).
Conformance is measured, not declared: `anteval` per backend for the agent,
a probe against a live process for the component.
`6/16`'s four-layer model is the map of where the seams are.

## Tracks, ranked

**1. The contracts (primary).** The two contracts above, their conformance
checks, and `pi` as the second agent-slot occupant — a contract proven against
a real second implementation, not a hypothetical one.

**2. The on-ramp (secondary).** Wrapping stays useful as an entry path, not
the product:

- **Runtime pluralism.** runed spawns "just another binary"; a foreign harness
  can occupy the agent slot to the degree it meets the agent contract.
  openclaw and qm already do this for their own slots; it is table stakes, not
  a differentiator.
- **MCP as the seam.** arizuko is MCP-native (agent side) + REST (human side).
  Every capability another harness exposes over MCP is reachable; every
  arizuko resource is reachable by their agent. Interop is protocol work.
- **Import, don't convert.** Read their config — a Claude Code
  `settings.json` + skills, an OpenClaw channel map, a character.json — and
  mount it into a folder as-is.

**3. Campaign (supporting).** Persuasion rides on proof: the Agent Research
Hub (comparative, source-cited), `USELESS.md` (the self-critique that makes
honest claims believable), the `web-native Linux` one-liner (`specs/5/A`).
Campaigning before the contracts exist asks people to take a leap.

## The engine — an agentic reimplementation loop

The interop surface is large (N harnesses × their features) and moving.
Hand-building it does not scale. Dogfood instead: arizuko's own agents build
arizuko's interop. A scheduled loop (`timed` → a folder-agent), one system at
a time:

1. **Read** the target system's source (the Hub already clones repos into
   `refs/`) and its released changes since last run. `refs/` lags upstream by
   months; a claim is verified against live `main`, never against the clone.
2. **Extract** the config/protocol surface an arizuko folder would need to
   host or import it, measured against those two contracts.
3. **Reimplement / shim** the smallest interop, in an isolated worktree.
4. **Verify** end-to-end (spawn a folder on that runtime, drive a turn), gate
   on an adversarial check, open the change for operator review.
5. **Record** in the Hub (that system's page) + a compatibility matrix.

This reuses machinery that exists: the Hub's monthly code-analysis protocol,
worktree-isolated subagents, the migrate/broadcast delivery path. The loop is
the same shape arizuko already runs on itself (`specs/5/22` self-learning);
here it points outward. It is scoped to one target and measured on whether
the interop it produces is adopted before it scales.

## What the environment provides — services one level up

The environment is worth occupying only if arizuko supplies what a bare
harness lacks. Each row is a gap a bare harness has (Hermes is the worked
example) that the folder coordinate closes for _every_ occupant:

| Service          | arizuko mechanism                                                                                      | harness gap it closes                              |
| ---------------- | ------------------------------------------------------------------------------------------------------ | -------------------------------------------------- |
| Keys / secrets   | folder-scoped encryption (`8/E`) + injection at the tool edge (`5/13`)                                 | keys plaintext in the runtime; never host-injected |
| Egress           | per-turn folder-scoped allowlist (`6/24`); not opt-in — runed refuses to start without the proxy       | one global blocklist; no per-tenant egress         |
| Identity / authz | folder-containment + grant DSL, injected Gate (`5/17`)                                                 | admin-vs-user binary; one shared bearer            |
| Tenant isolation | ephemeral per-turn KVM guest per folder (`6/41`)                                                       | one shared long-lived container; users share FS    |
| Cost / budget    | per-tenant spend gate (`store/groups.go` cap + `routd/budget.go`; the gateway owns the ledger, `6/39`) | per-session usage only; no caps                    |
| Audit / obs      | `audit_log` + `obs` (journald + OTLP)                                                                  | plaintext logs; no structured audit                |
| Routing / web    | route table + `/pub` `/priv` + WebDAV                                                                  | home-channel delivery only; no served web          |

These seven are the moat; never trade them for a harness's model/skill
breadth. An on-ramp adapter that dilutes any of them is not worth carrying.
Against an orchestrator rather than a harness, the table narrows: openclaw
has per-agent workspaces (its docs: "not a hard sandbox"), Paperclip has a
governance gate and budgets, qm has an Envoy-checked egress. What none has is
the folder as the one key to all seven rows.

## Demand classes, not just systems

The loop also answers a _demand class_ (knowledge graphs, RAG, "codebase
maps") — a category of want every vendor restates as a reason to buy heavy
machinery. The discipline: strip to the primitive need, try the folder
primitives first (tree + files + `jq`/`grep`/FTS5, no new always-on
subsystem), and escalate to a mounted capability (RO `/mnt` or an MCP tool)
only on proof the primitives can't. (marble is the reference output: a
1,590-topic prerequisite graph answered by `jq` over three bundled JSON files,
no graph DB.)

## What is settled

Settled: the thesis, narrowed to property **a** and measured against the
field; the two contracts as the primary track; the on-ramp as secondary; the
seven services. The analysis is complete as positioning — nothing in it is
code to write. Vendor rows date — re-measure a row before citing it.

## Ties

[`6/16`](16-daemon-standalone-matrix.md) (the four layers; the counterpart per
commodity part) · [`7/23`](../7/23-agent-habitat.md) (the shared ingress and outbound action model) ·
`specs/5/A` (positioning) · `USELESS.md` (honest gaps) · the Agent Research
Hub · `specs/5/22` self-learning (the loop, turned outward).
