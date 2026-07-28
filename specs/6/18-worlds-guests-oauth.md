---
status: draft
depends:
  [
    ../5/5-tenant-self-service,
    ../5/18-onboarding-model,
    ../5/S-jid-format,
    ../5/15-surrogate-oauth,
  ]
---

# worlds, agents, sessions — the collapsed hierarchy

The tenancy model collapses to **three tiers**, each a primitive that already
exists, each managed entirely through **MCP tools** (resreg resources per
`5/17` — REST + agent-MCP from one handler):

```
World      → users onboard here; the tenancy + billing + web-vhost boundary
  Agent    → a "main group"; one agent identity, its skills/persona/memory
    Session → a run: the turn container, its subagent spawns, prototyping
```

This **replaces `5/5`'s arbitrary-depth org-chart** (world → dept@2 → team@3+).
Depth is no longer load-bearing: a world contains agents, an agent runs
sessions. Sub-structure that used to be "deeper folders" is now either another
agent (a peer main group) or a subagent spawned inside a session.

## Tier 1 — World

A world is the top-level boundary where **users are onboarded** (`5/18`). It
owns: the user roster, invites, the web vhost (`<world>.<domain>` → `/pub/<world>/`,
`5/V`), world-scoped secrets, and the grant root. One world's users can invite
others; invited users are **guests** (below).

MCP tools: `world_invite`, `world_members`, `world_revoke` (map onto onbod
invites, `5/5` Phase B). Onboarding a user = admitting them to the world.

## Tier 2 — Agent (the main group)

An agent is a main group: a folder with one persona/`SOUL.md`, its skills,
memory (diary/facts/users), and route bindings. Creating an agent = seeding a
group (`onbod/SetupGroup`). Agents are **peers within a world**, not nested.

MCP tools: `agent_create`, `agent_list`, `agent_grant` (who — which world
users/guests — may act in this agent, and with what scope). Grants stay the
`[!]action(param=glob)` DSL (`4/9`); the world is the grant root.

## Tier 3 — Session

A session is one agent run: the turn container (`5/P` runed), its **subagent
spawns**, **auto-onboarding**, and **prototyping**. The agent holds a
**prototype** — a reusable spawn template (persona + skills + tool grants) — and
spawns subagents from it within the session. Subagents inherit a downscoped
slice of the agent's grants; nothing a subagent gets exceeds the parent.

The prototype is distinct from two shipped things already carrying the name:
`5/5`'s `groups/<world>/prototype/` seed folder (copied once on invite
subgroup-create) and `5/21`'s product (a curated template seeded at group
creation). Same shape, three instantiation points.

MCP tools (hot-tier, agent-facing): `spawn` (from a prototype), `prototype_set`
/ `prototype_get`, plus the existing session/subagent surface. Auto-onboarding
is a session that runs when a new user/chat first appears (`5/18`), scripted by
the world's onboarding prototype.

## Guests & delegated OAuth (folds in the original scope)

- **Guest** — a world user invited by another; not an operator. Admitted at
  Tier 1, granted into specific agents at Tier 2.
- **Account linking** — a guest links a platform account via that platform's
  OAuth (surrogate-OAuth, `5/15`). Credentials are per-guest, world/agent-scoped
  secrets; never shared across guests.
- **Delegated use** — within a session the agent may act **as** a guest with
  their linked creds, gated by explicit rules: which actions, which agents,
  consent + revocation, audited. Reuse the grant DSL for the rule layer; a
  distinct predicate layer only if cross-row/time conditions demand it.

## Open questions

- World as a first-class table vs the current "top-level group" convention
  (`5/5` uses the latter; collapse may warrant promoting it). Today a
  top-level group is BOTH the tenancy boundary and a main group with a
  persona — Tier 1 and Tier 2 in one folder.
- Tier 1 admission has two shapes that don't compose: `world_invite` admits a
  user INTO a world, while chat-initiated onboarding CREATES a world per
  admitted stranger (`5/18` path 2, `onbod.createWorldTx` writes group +
  admin `acl` + routes). Which is the tier's contract?
- JID shape (`5/S`) for the three tiers — does `world/agent` suffice with
  sessions addressed by `run_id`, or does the session need a JID segment?
- Prototype storage: a resreg resource per agent vs a file in the agent folder.

## Ties

`5/5` tenancy (org-chart → this collapse) · `5/18` onboarding · `5/S` JID ·
`5/15` surrogate OAuth · `5/17` REST+MCP one-handler · `4/9` grants ·
`5/P` runed sessions/spawns · `5/21` products (the Tier-2 twin of the
Tier-3 prototype).
