---
status: draft
---

# Adoption — publish the environment, not the wrap

arizuko is not a harness. It is orchestration: it decides how agents run and for
whom — routing, tenancy, identity, permissions — the layer _above_ any harness.
The adoption thesis (operator decision 2026-08-15, inverting this spec's
original "wrap what you already run"): **arizuko publishes an environment, and
the value is what the environment gives you for free.** Wrapping a foreign
harness stays available as an on-ramp; it is no longer the thesis.

## Problem

The crowded field (Claude Code, OpenClaw, NanoClaw, ElizaOS, Hermes, a new one
every week) is a field of _harnesses_ — each runs one agent for one user.
arizuko is a different layer: it orchestrates agents across tenants. Competing
with a harness head-to-head is a category error.

Two things make the layer worth adopting (`specs/5/A`, `USELESS.md` §4):

1. **The folder coordinate.** One path is tenant, ACL, route, egress, web host,
   and file tree — how agents run and for whom, on one box, the same code from
   `solo/inbox` to `corp/eng/sre/oncall`.
2. **Agents reshape the system; you stay in the loop.** The persona, skills,
   routing, even child groups are files the LLM edits — yet every change is a
   file you diff, a grant that gates it, and (if you `git init` the group) a
   `git revert` away. Self-modifying, human-owned.

## The thesis — the environment IS the product (2026-08-15)

The original framing ("run your harness inside arizuko") is **emulation**, and
it was rejected. Emulation inherits the wrapped thing's model, carries
compatibility debt forever, and caps arizuko at whatever it wraps.

The Lambda analogy is the argument. Lambda never emulated your server. It
published a contract — handler, event, context, and a role the SDK finds on its
own — and the value was what the environment gives you for free: isolation,
IAM, scaling. You give up generality and gain the platform. **You write to
their APIs; they do not reimplement yours.**

arizuko has two such environments today — the **agent environment** (event +
tools + home) and the **component environment** (what a daemon gets and owes).
Both existed only as prose in root `CLAUDE.md`; nothing answered "is this a
well-formed arizuko daemon" or "is this a well-formed arizuko agent". So the
adoption work is: write each as a contract, give each a conformance check a
stranger's part must pass. **[`6/7`](7-environment-contracts.md) is that
document** — it owns both contracts and their conformance (agent: `anteval`
per backend; component: a probe against a live process). `6/16`'s four-layer
model is the map of where the seams are.

## Tracks, ranked

**1. The contracts (primary).** `6/7`'s two contracts, their conformance
checks, and `pi` as the second agent-slot occupant — a contract proven against
a real second implementation, not a hypothetical one.

**2. The on-ramp (secondary).** Wrapping stays useful as an entry path, not
the product:

- **Runtime pluralism.** runed spawns "just another binary"; a foreign harness
  can occupy the agent slot to the degree it meets the `6/7` agent contract.
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
   `refs/`) and its released changes since last run.
2. **Extract** the config/protocol surface an arizuko folder would need to
   host or import it, measured against the `6/7` contracts.
3. **Reimplement / shim** the smallest interop, in an isolated worktree.
4. **Verify** end-to-end (spawn a folder on that runtime, drive a turn), gate
   on an adversarial check, open the change for operator review.
5. **Record** in the Hub (that system's page) + a compatibility matrix.

This reuses machinery that exists: the Hub's monthly code-analysis protocol,
worktree-isolated subagents, the migrate/broadcast delivery path. The loop is
the same shape arizuko already runs on itself (`specs/5/22` self-learning);
here it points outward.

## What the environment provides — services one level up

The environment is worth occupying only if arizuko supplies what a bare
harness lacks. Each row is a gap a bare harness has (Hermes is the worked
example) that the folder coordinate closes for _every_ occupant:

| Service          | arizuko mechanism                                                      | harness gap it closes                              |
| ---------------- | ---------------------------------------------------------------------- | -------------------------------------------------- |
| Keys / secrets   | folder-scoped encryption (`8/E`) + injection at the tool edge (`5/13`) | keys plaintext in the runtime; never host-injected |
| Egress           | per-spawn folder-scoped allowlist (`6/24`, opt-in)                     | one global blocklist; no per-tenant egress         |
| Identity / authz | folder-containment + grant DSL, injected Gate (`5/17`)                 | admin-vs-user binary; one shared bearer            |
| Tenant isolation | ephemeral per-turn container per folder                                | one shared long-lived container; users share FS    |
| Cost / budget    | per-tenant spend gate (`store/cost_log.go`)                            | per-session usage only; no caps                    |
| Audit / obs      | `audit_log` + `obs` (journald + OTLP)                                  | plaintext logs; no structured audit                |
| Routing / web    | route table + `/pub` `/priv` + WebDAV                                  | home-channel delivery only; no served web          |

These seven are the moat; never trade them for a harness's model/skill
breadth. An on-ramp adapter that dilutes any of them is not worth carrying.

## Demand classes, not just systems

The loop also answers a _demand class_ (knowledge graphs, RAG, "codebase
maps") — a category of want every vendor restates as a reason to buy heavy
machinery. The discipline: strip to the primitive need, try the folder
primitives first (tree + files + `jq`/`grep`/FTS5, no new always-on
subsystem), and escalate to a mounted capability (RO `/mnt` or an MCP tool)
only on proof the primitives can't. (marble is the reference output: a
1,590-topic prerequisite graph answered by `jq` over three bundled JSON files,
no graph DB.)

## Non-goals / honest risks

- **Not a shim graveyard.** Interop that dilutes the folder coordinate is a
  liability. Every adapter must keep tenancy + egress + ACL intact.
- **The moat must stay the reason.** If arizuko is only "compatible with X", X
  wins on maturity. The contract is the product; compatibility is the on-ramp.
- **Validation is still autobiographical** (`USELESS.md` §3). One external
  operator running one foreign part against the published contracts is the
  first real proof — optimize for that, not for feature count.
- **The reimplementation loop is unproven and expensive.** Scope it to one
  target, measure whether the interop it produces is actually adopted, before
  scaling the fleet.

## Ties

[`6/7`](7-environment-contracts.md) (the two contracts — owns the terms) ·
[`6/16`](16-daemon-standalone-matrix.md) (the four layers) · `specs/5/A`
(positioning) · `USELESS.md` (honest gaps) · the Agent Research Hub ·
`specs/5/22` self-learning (the loop, turned outward).
