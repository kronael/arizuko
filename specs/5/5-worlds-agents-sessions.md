---
status: partial
depends: [18-onboarding-model, S-jid-format, 15-surrogate-oauth]
---

# specs/5/5 — worlds, agents, sessions

> Tenancy collapses to three tiers, each a primitive that already exists,
> each managed through resreg resources (REST + agent-MCP from one handler,
> `5/17`):
>
> ```
> World      → users onboard here; the tenancy + billing + web-vhost boundary
>   Agent    → a "main group"; one agent identity, its skills/persona/memory
>     Session → a run: the turn container, its subagent spawns, prototyping
> ```

This replaces arbitrary depth **as a tenancy model**: depth used to compute
default power (`tier = min(count("/"), 3)`), and `5/33` deleted that axis, so
depth confers no authority anywhere now, tiered or not. Deep folders still
work as coordinates and are still used live (krons runs
`eval/eti0hsk-priv-grant-g`, `krons/public/marble`) — only the claim that
depth _structures tenancy_ is dropped.

**Only the vocabulary and invites below are built.** No `worlds` table, no
`world_*`/`agent_*` tools; a world is still just a top-level group. Read the
tiers as the intended shape.

## Vocabulary

A **group** is a folder identified by a path — a pure coordinate. A **topic**
is the transient work-unit overlaid on a group; many topics per group. The
org-chart mapping still frames the tiers below: an organization is a world, a
job description is a grant rule list, the mailroom is the routes table,
hiring is invite + grant, off-boarding is revoke.

## Tier 1 — World

The top-level boundary where **users are onboarded** (`5/18`). It owns the
user roster, invites, the web vhost (`<world>.<domain>` → `/pub/<world>/`,
`5/V`), world-scoped secrets, and the grant root. Its users can invite
others; invited users are **guests**.

**Invites (shipped, 2026-05).** `invites` — opaque tokens that produce a
grant on acceptance. Schema `store/migrations/0032-invites-rewrite.sql`;
resreg resource + `arizuko invite` CLI + `invite_create` MCP tool + dashd
`/dash/invites/`. Two modes on `target_glob`: a trailing slash (`atlas/`) is
**subgroup-create** — the recipient picks a username, `atlas/<username>` is
created from `groups/atlas/prototype/`, and they are granted it; no trailing
slash (`atlas/support`) is **join** — the recipient is granted the existing
group directly. `**` is reserved and rejected as a `target_glob`. An issuer
holding grants on `target_glob` mints a token → the recipient opens
`/invite/<token>` → OAuth → the token is consumed and the grant issued or the
group created → `used_count` increments and the row stays for audit. A
failed grant write rolls the consume back (`RestoreInvite`) — an invite is
never burned without its grant.

Intended tools: `world_invite`, `world_members`, `world_revoke`, layered onto
the invites primitive above. Onboarding a user = admitting them to the world;
world _creation_ stays operator/CLI-only (design resolution 1, below).

## Tier 2 — Agent (the main group)

A folder with one persona (`5/21`), its skills, memory, and route bindings.
Creating an agent seeds a group (`onbod/SetupGroup`). Agents are **peers
within a world**, not nested. Intended tools: `agent_create`, `agent_list`,
`agent_grant` (who may act in this agent, with what scope) — grants stay
`acl` rows (`5/32`).

## Tier 3 — Session

One agent run: the turn container (`5/P`), its **subagent spawns**,
auto-onboarding, and prototyping.

Two things share the word "prototype", and must not be conflated. The
**automatic** case is shipped: a delegated message targeting an unrouted
child folder makes `routd/steer.go:529` spawn one from the parent's
`prototype/` dir (`routd/spawn.go:44` `spawnFromPrototype` — copy, register,
room-route, with rollback). That's system routing, not an agent action, and
`4/26` fully specifies it (template dir, `room=<jid room>` route match,
`max_children` cap via `auth.CheckSpawnAllowed`) — not restated here.

The **agent-initiated** case — "make me a copy of this group at that
path" — has no mechanism of its own and needs none: `5/8` already ships the
two primitives (export a folder, apply it at a target path). Spawning a
prototype by hand, seeding a `5/28` package group, and cross-instance folder
migration are each a **recipe** over those two verbs — product/use-case
guidance, not a spec, not a new tool. `register_group`'s `fromPrototype` flag
stays unwired (`routd/groups_resource.go:100`, returns "not configured");
wiring it would be a second mechanism beside `5/8`'s.

A subagent gets a strict subset of the parent's grants, by
`auth.Delegate`-style subset-of-held (`5/33`), never by a depth rung.

## Guests & delegated OAuth

- **Guest** — a world user invited by another, not an operator. Admitted at
  Tier 1, granted into specific agents at Tier 2. A guest arriving on a
  channel is anonymous and holds no grants until **paired** (`5/31`) —
  pairing is what makes a channel identity a guest rather than a stranger.
- **Account linking** — outbound only, distinct from pairing: a guest links a
  platform account via surrogate OAuth (`5/15`) so the agent can act _as_
  them at a third party. Pairing proves who the guest is _to_ arizuko;
  surrogate lets arizuko act _for_ them elsewhere.
- **Delegated use** — within a session the agent may act **as** a guest with
  their linked credentials, gated by explicit rules: which actions, which
  agents, consent, revocation, audited. Reuse the `acl` row for the rule
  layer; a distinct predicate layer only if cross-row/time conditions demand
  it.

## Topic kinds (unbuilt)

Topics may carry a `kind` — `task`, `project`, `meeting`, `question`,
`discussion`, `incident`, or the default `thread`. Kind drives kind-specific
workflow verbs (`set_due` on a task, `set_attendees` on a meeting). It is
metadata on the topic node, **not** a hierarchy level. Not built.

## Secrets (Phase C)

Folder- and user-scoped secrets, independent of the hierarchy — **shipped**,
as `5/14`'s credential model, which fully supersedes this spec's original
§Secrets shape (`chats.kind` gate, scope model). `FolderSecretsResolvedForUser`
does a live folder walk (deeper wins) with a user overlay. **Not** built: the
call-time broker (`5/13` shape 3) — capability credentials still inject at
container spawn, interim, instead of a key that never lands in container env.
Adoption, for context: `secrets` holds **one row fleet-wide** across all
three live instances — shipped, and the BYOA unlock it enables is barely
used.

## Design resolutions — pending sign-off

Nothing here is built; each is a decision proposed for when the tier
framework is implemented.

1. **World creation stays operator/CLI-only; no `world_create` agent tool.**
   `world_invite` admits a user INTO an existing world. `onbod.createWorldTx`
   is an operator-plane act, consistent with the anteval case requiring an
   agent to refuse ("worlds are CLI-only") rather than claim success (`5/9`).
2. **World becomes a thin first-class entity; agents are its children.** A
   `worlds` row owns the roster, vhost, secrets root, and grant root; the
   top-level group stops doubling as an agent. Migration: each existing
   top-level group becomes a World + one implicit `root` Agent child.
3. **Auto-onboarding-as-session is a phase-2 build, not a rename.** v1 keeps
   the canned onbod greeting; a world-onboarding-prototype-driven session is
   new agent-run behavior.
4. **Subagent grants are a session-scoped downscope of the agent's grants**,
   minted by delegation at spawn, bounded by subset-of-held (`5/33`) — no
   depth rung to map them onto.
5. **`product` and `prototype` name the same shape, not yet the same
   mechanism.** A `5/21` product (group-create) and Tier 3's `prototype/` dir
   (auto-spawn, or the by-hand recipe above) both mean "persona + skills +
   seed files, copied into a new folder." Unifying the shape into one
   authored asset is still open.

## Open

- JID shape (`5/S`) for the three tiers — does `world/agent` suffice with
  sessions addressed by `run_id`, or does a session need a JID segment?
- Delegated-use rule layer: reuse `acl` rows, or add a predicate layer once
  cross-row/time conditions appear (guest consent windows, per-action
  scopes)?

## Out of scope

Time-bounded grants (`expires_at`), an audit log of permission changes,
on-call rotation for routing — all additive, ship later. Cross-org
collaboration is disallowed by design: worlds are isolation boundaries.

## Ties

`5/18` onboarding · `5/31` pairing · `5/S` JID · `5/15` surrogate OAuth ·
`5/17` one handler, two faces · `5/32`+`5/33` grants · `5/P` runed
sessions/spawns · `5/21` products · `4/26` prototype auto-spawn (shipped) ·
`5/8` export/apply · `5/28` packages · `5/14` credentials · `5/9` capability
eval.
