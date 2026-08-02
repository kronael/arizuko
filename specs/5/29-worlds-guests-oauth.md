---
status: draft
depends:
  [5-tenant-self-service, 18-onboarding-model, S-jid-format, 15-surrogate-oauth]
---

# worlds, agents, sessions — the collapsed hierarchy

The tenancy model collapses to three tiers, each a primitive that already
exists, each managed through resreg resources (REST + agent-MCP from one
handler, `5/17`):

```
World      → users onboard here; the tenancy + billing + web-vhost boundary
  Agent    → a "main group"; one agent identity, its skills/persona/memory
    Session → a run: the turn container, its subagent spawns, prototyping
```

This **replaces `5/5`'s arbitrary-depth org-chart**. Depth is not load-bearing:
a world contains agents, an agent runs sessions. Sub-structure that used to be
"deeper folders" is now either another agent (a peer) or a subagent spawned
inside a session. Note this is a _structural_ collapse only — depth already
confers no authority at all under [`5/33`](33-paths-roles.md).

**Nothing here is built.** There is no `worlds` table and none of the
`world_*`/`agent_*`/`prototype_*` tools exist; a world is still just a top-level
group. Read the tiers as the intended shape.

## Tier 1 — World

The top-level boundary where **users are onboarded** (`5/18`). It owns the user
roster, invites, the web vhost (`<world>.<domain>` → `/pub/<world>/`, `5/V`),
world-scoped secrets, and the grant root. Its users can invite others; invited
users are **guests**.

Intended tools: `world_invite`, `world_members`, `world_revoke`, mapping onto
onbod invites (`5/5`). Onboarding a user = admitting them to the world.

## Tier 2 — Agent (the main group)

A folder with one persona (`5/21`), its skills, memory, and route bindings.
Creating an agent seeds a group (`onbod/SetupGroup`). Agents are **peers within
a world**, not nested. Intended tools: `agent_create`, `agent_list`,
`agent_grant` (who may act in this agent, with what scope) — grants stay `acl`
rows (`5/32`).

## Tier 3 — Session

One agent run: the turn container (`5/P`), its **subagent spawns**,
auto-onboarding, and prototyping. The agent holds a **prototype** — a reusable
spawn template (persona + skills + tool grants) — and spawns subagents from it.
A subagent gets a strict subset of the parent's grants, by
`auth.Delegate`-style subset-of-held (`5/33`), never by a depth rung.

The prototype is the same shape as two shipped things already carrying the name:
`5/5`'s `groups/<world>/prototype/` seed folder and `5/21`'s product. Same
shape, three instantiation points.

## Guests & delegated OAuth

- **Guest** — a world user invited by another; not an operator. Admitted at Tier
  1, granted into specific agents at Tier 2. A guest arriving on a channel is
  anonymous and holds no grants until **paired**
  ([`5/31`](31-identity-pairing.md)) — pairing is what makes a channel identity
  a guest rather than a stranger.
- **Account linking** — outbound only, and distinct from pairing: a guest links
  a platform account via surrogate OAuth (`5/15`) so the agent can act _as_ them
  at a third party. Pairing proves who the guest is _to_ arizuko; surrogate lets
  arizuko act _for_ them elsewhere.
- **Delegated use** — within a session the agent may act **as** a guest with
  their linked credentials, gated by explicit rules: which actions, which
  agents, consent, revocation, audited. Reuse the `acl` row for the rule layer;
  a distinct predicate layer only if cross-row/time conditions demand it.

## Design resolutions (proposed — pending sign-off)

1. **World creation stays operator/CLI-only; there is no `world_create` agent
   tool.** `world_invite` admits a user INTO an existing world.
   `onbod.createWorldTx` is an operator-plane provisioning act, not an agent
   capability — consistent with the `5/9` eval that an agent must refuse to
   provision a world. Resolves the two-admission-contract conflict.
2. **World becomes a thin first-class entity; agents are its children.** A
   `worlds` row owns the roster, vhost, secrets root, and grant root; the
   top-level group stops doubling as an agent. Migration: each existing
   top-level group becomes a World + one implicit `root` Agent child.
3. **Auto-onboarding-as-session is a phase-2 BUILD, not a rename.** ⚠️ Gates on
   sign-off. v1 keeps the canned onbod greeting; the session driven by a world
   onboarding-prototype introduces a new agent-run path.
4. **Subagent grants are a session-scoped downscope of the agent's grants.**
   This was drafted as "the depth-≥3 grant rung maps onto the Session tier" —
   that framing is dead: `5/33` deleted every depth rung, so there is no rung to
   map. What remains is the substantive half: a subagent's grants are minted by
   delegation from the parent at spawn, bounded by subset-of-held.
5. **`product` and `prototype` unify under one name: "prototype".** One resreg
   resource, three apply-sites — group-create (was `5/21` product), invite
   subgroup-create (`5/5`), subagent-spawn (Tier 3).
6. **A prototype is filesystem-native but ships as a package.** Its content
   lives as files in the folder — fast to read at spawn, editable in place,
   git-versionable (`9/3`) — NOT a DB blob. It **ships** via `5/28`: the folder
   is the build context, the artifact is the distributable unit,
   `arizuko packages install` unpacks it back to the filesystem. Only the
   resreg _binding_ (which prototype an agent uses, spawn params) is DB-backed,
   for the uniform REST+MCP surface. A prototype's build-once/ship-anywhere need
   may be the case that reopens the OCI variant (`5/30`, superseded by `5/28`).

## Still open

- JID shape (`5/S`) for the three tiers — does `world/agent` suffice with
  sessions addressed by `run_id`, or does a session need a JID segment?
- Delegated-use rule layer: reuse `acl` rows, or add a predicate layer once
  cross-row/time conditions appear (guest consent windows, per-action scopes)?

## Ties

`5/5` tenancy · `5/18` onboarding · `5/31` pairing · `5/S` JID · `5/15`
surrogate OAuth · `5/17` one handler, two faces · `5/32`+`5/33` grants ·
`5/P` runed sessions/spawns · `5/21` products.
