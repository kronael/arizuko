---
status: draft
depends:
  [5-tenant-self-service, 18-onboarding-model, S-jid-format, 15-surrogate-oauth]
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

## Design resolutions (proposed — pending sign-off)

The alignment pass (2026-07-28) surfaced five points where the shipped system
contradicts this model. Proposed resolutions:

1. **World creation stays operator/CLI-only; there is no `world_create` agent
   tool.** `world_invite` admits a user INTO an existing world. Chat-initiated
   onboarding (`onbod.createWorldTx`) is an operator-plane provisioning act, not
   an agent capability — consistent with the `5/9` eval that an agent must
   refuse to provision a world. Resolves the two-admission-contract conflict.
2. **World becomes a thin first-class entity; agents are its children.** A
   `worlds` row owns the roster, vhost (`5/V`), secrets root, and grant root;
   Tier-2 agents are children. The top-level group stops doubling as an agent.
   Migration: each existing top-level group becomes a World + one implicit
   `root` Agent child.
3. **Auto-onboarding-as-session is a phase-2 BUILD, not a rename.** ⚠️ **Gates
   on sign-off.** v1 keeps the canned onbod greeting (`promptUnprompted`); the
   session driven by a world onboarding-prototype is a follow-on (it introduces
   a new agent-run path).
4. **The depth-≥3 grant rung maps onto the Session/subagent tier.** ⚠️ **Gates
   on sign-off — load-bearing.** Subagent grants become an explicit
   _session-scoped downscope_ of the agent's grants, NOT `grants.DeriveRules`
   depth derivation. World + Agent keep their tier defaults; the narrow
   depth-3+ rung (`reply`/`send_file`/`like`/`edit`) becomes the subagent
   default. Changes `grants.DeriveRules` semantics.
5. **`product` and `prototype` unify under one name: "prototype".** One resreg
   resource, three apply-sites — group-create (was `5/21` product), invite
   subgroup-create (`5/5` `prototype/`), subagent-spawn (Tier 3). `5/21`'s
   product folds in as the group-create apply-site.
6. **A prototype is filesystem-native but ships as an OCI image — docker-style.**
   The prototype's content (persona + skills + tool grants) lives as files in the
   folder: fast to read at spawn, editable in place, git-versionable (`9/3`
   cold-tier-in-git). It is NOT abstracted into a DB blob. It **ships** as a
   package (`5/28`): the folder is the build context, the shipped artifact is the
   distributable unit, `arizuko packages install` unpacks it back to the
   filesystem. The "docker image" framing is specifically the OCI variant
   (`5/28-oci-packages`, currently _superseded_ by the source-first
   `5/28-packages`) — a prototype's build-once/ship-anywhere need may be the case
   that reopens OCI. Only the resreg _binding_ (which prototype an agent uses,
   spawn params) is DB-backed for the uniform REST+MCP management surface.

## Still open

- JID shape (`5/S`) for the three tiers — does `world/agent` suffice with
  sessions addressed by `run_id`, or does the session need a JID segment?
- Delegated-use rule layer: reuse the grant DSL, or a predicate layer once
  cross-row/time conditions appear (guest consent windows, per-action scopes)?

## Ties

`5/5` tenancy (org-chart → this collapse) · `5/18` onboarding · `5/S` JID ·
`5/15` surrogate OAuth · `5/17` REST+MCP one-handler · `4/9` grants ·
`5/P` runed sessions/spawns · `5/21` products (the Tier-2 twin of the
Tier-3 prototype).
