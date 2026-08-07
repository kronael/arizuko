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
> World      → users onboard here; the tenancy + web-vhost boundary
>   Agent    → a "main group"; one agent identity, its skills/persona/memory
>     Session → a run: the turn container, its subagent spawns, prototyping
> ```

This replaces arbitrary depth **as a tenancy model**: depth used to compute
default power (`tier = min(count("/"), 3)`), and `5/33` deleted that axis, so
depth confers no authority anywhere now, tiered or not. Deep folders still
work as coordinates and are still used live (krons runs
`eval/eti0hsk-priv-grant-g`, `krons/public/marble`) — only the claim that
depth _structures tenancy_ is dropped.

> **Status (2026-08-07) — `partial`, and specifically NOT `draft`.** The tier
> FRAMEWORK is a design proposal awaiting sign-off; one tier-1 primitive under it
> is shipped and load-bearing. It stays `partial` until R2-R5 and §Open are
> answered — the answers are what unblock it, not effort. Nothing here is left
> for a reader to hunt for:
>
> **Unbuilt — the proposal.** Verified 2026-08-07: no `worlds` table, no
> `world_*`/`agent_*` tools (zero hits repo-wide), no topic `kind`. A world is
> still just a top-level group; tiers 2 and 3 are vocabulary only. Read them as
> the intended shape. Resolutions 2-5 and §Open below are framed as decisions —
> each carries options, their cost in code, what it forecloses, and a marked
> recommendation. **None is decided.**
>
> **What remains to build, once they are answered** (sizes are order-of-magnitude,
> measured against comparable shipped work):
>
> | Piece                                           | Size                                                                                                                                                                                                              | Gated on                           |
> | ----------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------- |
> | `worlds` table + backfill migration             | ~30 lines SQL, 1 routd migration                                                                                                                                                                                  | R2                                 |
> | `world_*`/`agent_*` resreg resources            | ~300 lines (catalog + handler + gate, by the `groups` precedent: `resreg/resources/groups.go` 132 + `routd/groups_resource.go` 161) ×2                                                                            | R2                                 |
> | World → World + implicit `root` Agent migration | **the risk item** — every existing top-level folder string is also its `web:` JID, IPC socket path, container home, `/pub/<world>/` root and `acl` scope glob; splitting `W` into `W` + `W/root` renames all five | R2                                 |
> | topic `kind`                                    | 1 column on routd `sessions` + verb dispatch; there is no `topics` table to hang it on                                                                                                                            | R2 (naming), independent otherwise |
> | delegated-use rule layer                        | 0 schema if `acl` is reused; the real work is plumbing `Caller.Claims` onto the agent socket (`routd/sibling_db.go:192` passes none)                                                                              | §Open 2                            |
>
> Choosing option A on R2 and §Open 1 collapses the table to its last two rows.
>
> **Built, which is why this is not a `draft`.**
>
> - **Invites** — the tier-1 admission primitive (mechanism in Tier 1 below).
>   `invites` is an onbod-owned resreg resource (`resreg/resources/invites.go`,
>   `DB: SubsystemOnbod`) serving `/v1/invites`, with the `invite_create` /
>   `invite_list` / `invite_revoke` agent tools (`ipc/ipc.go:1881,1937,1964`), the
>   `arizuko invite` CLI verb (`cmd/arizuko/main.go:750` `cmdInvite`), and the
>   `/dash/invites/` operator page (`dashd/invites.go`). All five re-verified
>   2026-08-07.
> - **Design resolution 1 is enforced, not merely proposed** — a top-level
>   `register_group` from the agent socket is refused with "worlds are CLI-only"
>   (`routd/groups_resource.go:150`).
> - **Secrets (Phase C)** — shipped as `5/14`'s credential model, which supersedes
>   this spec's original §Secrets shape outright.

## Vocabulary

A **group** is a folder identified by a path — a pure coordinate. A **topic**
is the transient work-unit overlaid on a group; many topics per group. The
org-chart mapping still frames the tiers below: an organization is a world, a
job description is a grant rule list, the mailroom is the routes table,
hiring is invite + grant, off-boarding is revoke.

## Tier 1 — World

The top-level boundary where **users are onboarded** (`5/18`). Its users can
invite others; invited users are **guests**.

"Owns" is the word to be careful with, because three of the four things this
spec used to list as world-owned are already owned elsewhere, derived, and
working:

- **Roster** — `acl` rows scoped `<world>/**` plus `acl_membership`
  (`5/32`/`5/33`). The scope glob IS the containment; there is no roster column
  to add.
- **Web vhost** — **derived, never stored.** `5/V` (shipped) retired
  `vhosts.json` on purpose: host `W.<HOSTING_DOMAIN>` 302s to `/pub/W/` with
  "no per-host config, no vhost file, no DB table", and the label≠name case is
  `WEB_VHOST_ALIASES` env (`groupfolder.ParseVhostAliases`). A `worlds.vhost`
  column would reintroduce exactly what `5/V` deleted.
- **World-scoped secrets** — `store.FolderSecretsResolved` walks folder
  ancestry deeper-wins (`store/secrets.go:532`); the top-level folder's rows
  already ARE the world default.
- **Grant root** — the one genuinely unowned thing. `auth.Authorize` has no
  per-world root; `role:operator` (`routd/migrations/0022`) is instance-wide.

A world is **not** a billing boundary today either: `cost_cap_cents_per_day` is
read per exact folder (`store/cost_log.go:66`) and per user
(`user_profiles`, line 87) with no roll-up to the top-level folder.

Decision R2 below is therefore narrower than it looks.

**Invites (shipped, 2026-05).** `invites` — opaque bearer tokens that produce a
grant on acceptance. onbod owns the table (`onbod/migrations/0001-onboarding.sql`,
moved to hash-at-rest by `0003-invites-hash-at-rest.sql`: the PK is
`ref = hex(sha256(token))` and the raw bearer is never persisted); surfaces are
listed in the status block above. Two modes on `target_glob`: a trailing slash (`atlas/`) is
**subgroup-create** — the recipient picks a username, `atlas/<username>` is
created and they are granted it; no trailing
slash (`atlas/support`) is **join** — the recipient is granted the existing
group directly. `**` is reserved and rejected as a `target_glob`. An issuer
holding grants on `target_glob` mints a token → the recipient opens
`/invite/<token>` → OAuth → the token is consumed and the grant issued or the
group created → `used_count` increments and the row stays for audit. A
failed grant write rolls the consume back (`RestoreInvite`) — an invite is
never burned without its grant. The created subgroup is seeded by
`container.SetupGroup(cfg, folder, "")` — the **default** group scaffold, empty
seed dir. There is no `groups/<world>/prototype/` template dir; the spec used to
claim one and no code has ever read it.

Intended tools: `world_invite`, `world_members`, `world_revoke`, layered onto
the invites primitive above. Onboarding a user = admitting them to the world;
world _creation_ stays operator/CLI-only (design resolution 1, below).

## Tier 2 — Agent (the main group)

A folder with one persona (`5/21`), its skills, memory, and route bindings.
Creating an agent seeds a group (`container.SetupGroup`, called from
`onbod/main.go:868` and four other sites). Agents are **peers within a world**,
not nested. Intended tools: `agent_create`, `agent_list`, `agent_grant` (who may
act in this agent, with what scope) — grants stay `acl` rows (`5/32`).

Nothing in production distinguishes a top-level folder from a nested one except
`routd/groups_resource.go:150`'s inline `!strings.Contains(gfld, "/")`.
`groupfolder.IsTopLevel` exists but has **zero** production callers (test-only).
That single site is the whole surface R2 would have to move.

## Tier 3 — Session

One agent run: the turn container (`5/P`), its **subagent spawns**,
auto-onboarding, and prototyping.

**The word `session` is taken, three times over.** Before any `session_*`
resource is named: `sessions` is already a globally-unique wire identity
(`resreg/resources/names.go` `SessionsName`) owned by **authd** — refresh-token
families at `/v1/sessions`, no MCP face. Separately, routd has a `sessions`
TABLE that is the topic→Claude-session-id map (`routd/migrations/0001:81`, PK
`(group_folder, topic)`). This tier's "session" is a third thing: a run, keyed
`spawns.run_id` (`runed/migrations/0001:19`). Per CLAUDE.md's "a resource's name
IS its wire identity, globally unique", this tier cannot register as `sessions`
and must pick another name — `runs` is the one the data already uses.

**Subagents have no identity today.** `grep -rn subagent --include=*.go` returns
nothing. A turn is credentialed by the MCP socket's `SO_PEERCRED`, not a token,
so a Claude Code subagent inside the container shares the parent's socket and
therefore holds the parent's grants exactly. The `mcp_tokens` per-spawn
capability-token table was dropped as unconsumed
(`runed/migrations/0003-drop-mcp-tokens.sql`) — there is no broker shell left to
build R4 on.

Prototyping has **one** mechanism, and it is not a prototype feature.
Automatic prototype spawn was deleted (`4a9a49c7`, with `4/26`): a
delegated message to an unrouted folder no longer creates anything —
`routd/steer.go`'s `delegateViaMessage` errors naming the folder and the
tool that creates it. Groups are never created by routing.

"Make me a copy of this group at that path" needs no mechanism of its own:
`5/8` already ships the two primitives. Precisely, as of 2026-08-07 — export is
**instance-wide** (`arizuko export <instance>`, `cmd/arizuko/apply.go:313`, one
manifest per subsystem), and the target-path half is `arizuko apply --as-folder
<folder>` (`cmd/arizuko/retarget.go`, over `resreg.Resource.Retarget`). Both are
**operator CLI only — no MCP tool exists for either**, so an agent cannot run
this recipe itself, and `Retarget` rewrites DB rows only, never files.
Spawning a prototype, seeding a `5/28` package group, and cross-instance folder
migration are each a **recipe** over those two verbs — product/use-case
guidance, not a spec, not a new tool. Anything that copies-and-registers behind
one verb would be a second mechanism beside `5/8`'s, which is why the old one
went.

**Intended**, not built: a subagent gets a strict subset of the parent's grants,
by `auth.Delegate`-style subset-of-held (`5/33`), never by a depth rung. R4
below is the decision; today it is a strict _equal_, not a subset.

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
  agents, consent, revocation, audited. The rule layer is §Open 2 below — and
  note that "add a predicate layer" is no longer an available option, because
  `acl.predicate` shipped (see there).

## Topic kinds (unbuilt)

Topics may carry a `kind` — `task`, `project`, `meeting`, `question`,
`discussion`, `incident`, or the default `thread`. Kind drives kind-specific
workflow verbs (`set_due` on a task, `set_attendees` on a meeting). It is
metadata on the topic, **not** a hierarchy level. Not built.

There is no "topic node" to hang it on: a topic is a **column**, on routd's
`sessions` (PK `(group_folder, topic)`) and on `messages`, with
`chats.sticky_topic` for sticky routing. So `kind` is one `ALTER TABLE
sessions ADD COLUMN kind` plus verb dispatch — not a new entity. It is the one
piece of this spec that does not depend on any decision below; it is unbuilt for
want of a caller, not for want of a sign-off.

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

Resolution 1 is already enforced in code. **R2-R5 are open decisions, framed
below as options with costs and a marked recommendation — the recommendation is
the author's, not a decision.** Accepting one is a one-word answer; rejecting one
should be done knowing what the table says it costs. Costs were measured against
the tree at `f1ed8e29` (2026-08-07).

### R1 — world creation is operator/CLI-only (SETTLED, enforced)

No `world_create` agent tool. `world_invite` admits a user INTO an existing
world. `onbod.createWorldTx` (`onbod/main.go:921`) is an operator-plane act,
consistent with the anteval case requiring an agent to refuse rather than claim
success (`5/9`); the refusal is live at `routd/groups_resource.go:150`, which
rejects a top-level `register_group` on the agent socket with "worlds are
CLI-only". Nothing to decide here — listed so the numbering matches history.

### R2 — does a world get its own row?

**Question.** Today a world is "a top-level group" and nothing more. Should it
become a `worlds` row with agents as its children — meaning every existing
top-level folder `W` splits into a World `W` plus an implicit Agent `W/root`?

|       | Option                                                                                                                             | Cost                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A** | **No `worlds` table.** World stays a folder-shape predicate.                                                                       | Zero. No migration, no resource, no rename. The per-world grant root stays unavailable — `role:operator` remains instance-wide. This spec's roster/vhost/secrets-root ownership claims get deleted as already-covered (they are, see Tier 1).                                                                                                                                                                                |
| **B** | **`worlds` table, thin — grant root only.** `worlds(folder PK, created_at, root_principal)`.                                       | 1 routd migration + backfill (one row per existing top-level group) + one resreg pair (~300 lines). `routd/groups_resource.go:150` MUST be rewritten to read the table, or two answers to "is this a world" drift. **No** `W`→`W/root` split — agents stay peers of the world folder, not children of it.                                                                                                                    |
| **C** | **`worlds` table owning roster + vhost + secrets root + grant root**, as originally proposed, with the `W`→`W`+`W/root` migration. | Adds a `vhost` column that `5/V` deliberately deleted, a roster beside the `acl` scope glob, and a secrets root beside `FolderSecretsResolved`'s walk — three parallel second paths, each a "no duplication" violation. The migration renames `W`'s `web:` JID, IPC socket path, container home, `/pub/W/` root and every `acl` scope glob at once; that is the 0042/0043 typed-JID cutover's blast radius, on live tenants. |

**Forecloses.** C forces the split migration, which forces §Open 1 (a `world/agent`
JID has to mean something once the two differ). A and B leave §Open 1 free to
answer "no change". B is the only option that delivers a per-world grant root —
but a per-world grant root is arguably a `5/33` change (a scoped `role:operator`
row) rather than a table, and choosing that instead makes B collapse into A.

**Recommendation (not a decision).** **A**, plus delete R2's roster/vhost/
secrets claims. Reasoning: three of the four things the row was to own already
work and are owned elsewhere; the fourth is expressible as an `acl` row. C's
cost is a live-tenant rename bought for vocabulary. Reject A only if a per-world
grant root is a firm requirement — and then ask whether it needs a table at all.

### R3 — does a new user's first contact run an agent?

**Question.** A user completes onboarding: do they get the canned
`ONBOARDING_GREETING` (`onbod/main.go:288,466-467`) or a real turn from the
world's agent?

|       | Option                                                     | Cost                                                                                                                                                                                                                                                                                                                         |
| ----- | ---------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A** | Keep the canned greeting.                                  | Zero. Instant page, no spend, no container. What ships today.                                                                                                                                                                                                                                                                |
| **B** | Onboarding enqueues a message to the world folder instead. | onbod already writes routd's DB under the FS-mount write-discipline, so this is a call-site swap, not new plumbing. Real cost is operational: one uncapped agent turn per admission (spend on an unauthenticated-ish path) and a container spawn's latency on the signup flow, where today the user sees a page immediately. |
| **C** | Canned greeting immediately, agent turn queued behind it.  | B's cost plus a second delivery; the user sees two messages.                                                                                                                                                                                                                                                                 |

**Forecloses.** Nothing else — R3 is sequencing, not shape. It is listed here
only so it is not mistaken for a blocker on R2.

**Recommendation (not a decision).** **A** until a spend cap exists on the
admission path. B is a good feature behind a cap; without one it is a
signup-driven cost channel.

### R4 — should a subagent hold fewer grants than its parent?

**Question.** A Claude Code subagent inside a turn container today holds the
parent agent's grants **exactly** — same `SO_PEERCRED` socket, same
`folder:<path>` principal. Should it be downscoped?

|       | Option                                         | Cost                                                                                                                                                                                                                                                                                                                                                                                                                              |
| ----- | ---------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A** | Accept it: subagent = parent.                  | Zero. Failure mode: a subagent spawned for a narrow job can call every tool the parent holds, including secrets and egress.                                                                                                                                                                                                                                                                                                       |
| **B** | Per-spawn downscoped principal.                | Mint `folder:<f>#<run_id>`, `auth.Delegate` the subset, bind a **second** socket, and garbage-collect the rows per run. Touches ipc/, container/, runed/ **and** the ant TS side (the subagent launcher must be pointed at the second socket). `auth.Delegate` is wired at exactly one site today — `routd/acl_resource.go:213` (add_acl) — **not at spawn**, despite `auth/delegate.go`'s header comment saying "used at spawn". |
| **C** | Token-based downscope via a capability broker. | Not available: `mcp_tokens` was dropped as unconsumed (`runed/migrations/0003`). C would first rebuild the broker, then do B anyway.                                                                                                                                                                                                                                                                                              |

**Forecloses.** B needs a name for the session principal, which is §Open 1's
question wearing a different hat: if §Open 1 answers "no JID segment", the
principal is `run_id`-derived and invisible to routing; if it answers "JID
segment", the JID is the principal. Answer §Open 1 first.

**Recommendation (not a decision).** **A**, and rewrite `auth/delegate.go`'s
"used at spawn" comment, which describes an unbuilt design as shipped. B is
worth building when a subagent runs untrusted input — not before, because the
second socket is the kind of parallel path this codebase spends effort removing.

### R5 — are `product` and `prototype` one authored asset?

**Question.** Both mean "persona + skills + seed files, copied into a new
folder". Should there be one asset, or is `prototype` dead vocabulary?

|       | Option                                                                                            | Cost                                                                                                                                                                                                                                                                                                                     |
| ----- | ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **A** | Product-only: drop "prototype" from the vocabulary.                                               | Doc-only. `product` already ships as a `groups` column (`routd/migrations/0001:10`, default `assistant`) plus TOML templates the CLI applies (`cmd/arizuko/products.go`, `arizuko create --product`, `arizuko register --product`). The `prototype/` dir mechanism was deleted with `4/26` (`4a9a49c7`) and has no code. |
| **B** | Unify onto the `5/8` manifest: a product becomes an exported manifest applied with `--as-folder`. | Blocked on a gap: products carry **files**, and `Retarget` rewrites DB rows only (`cmd/arizuko/retarget.go:76` — "rewrites ONLY the folder column"). Unifying needs a file half of `5/8` that does not exist.                                                                                                            |
| **C** | Leave open.                                                                                       | Status quo: two words for one shape, and `5/21` stays `draft`.                                                                                                                                                                                                                                                           |

**Forecloses.** Nothing. R5 is independent of R2-R4 and of §Open.

**Recommendation (not a decision).** **A**. One word, and it is the one with
shipped code behind it. B is a real idea but it is `5/8`'s file half, so file it
there rather than blocking this spec on it.

## Open

Same format as R2-R5: options, costs, what each forecloses, a marked
recommendation. Neither is decided.

### Open 1 — can a user address a single run?

**Question.** A run is reachable today only as `spawns.run_id` through runed's
API — nothing can route a message _to_ a run. Should a session earn a JID
segment (`web:<world>/<agent>/<session>`) so it becomes a routable address?

|       | Option                                             | Cost                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| ----- | -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **A** | `run_id` only — no JID change.                     | Zero. `5/S` is untouched. A run stays an API object; the routable sub-unit stays the **topic**, which already exists (`sessions.topic`, `#topic` sticky, `chats.sticky_topic`) and is already outside the JID.                                                                                                                                                                                                                                                                                                                                                                 |
| **B** | Session as a JID segment.                          | `groupfolder.JidFolder` returns the **whole** rest for `web:` (`groupfolder/folder.go:90`) because a folder may itself contain `/` — so adding a trailing session segment requires teaching every `web:` JID where the folder stops. That is exactly the token-vs-sub split `5/S` **deliberately deferred** ("`web:` stays folder-keyed… deferred until the web stack itself splits them"). Plus a migration of `messages.chat_jid`/`sender`, `chats.jid`, `user_jids.jid`, `acl` scope globs and every `chat_jid=web:*` route predicate — the 0042/0043 cutover shape, again. |
| **C** | Promote **topic** into the JID instead of session. | Same `web:` split cost as B, for a unit that works fine outside the JID today. Strictly worse than B: it pays B's price and does not make runs addressable.                                                                                                                                                                                                                                                                                                                                                                                                                    |

**Forecloses.** B unblocks R4's named session principal (the JID is the
principal) and is what R2 option C's `W`→`W/root` split would force anyway. A
keeps R4 on a `run_id`-derived, unroutable principal. Answering this settles the
naming half of R4.

**Recommendation (not a decision).** **A**, and record that B is gated on
`5/S`'s deferred `web:` split — i.e. it is blocked on `W-webhook-routes`
splitting route-token routing from JWT identity, not on this spec. Do not pay
the JID cutover for vocabulary.

### Open 2 — what expresses "the agent may act as this guest, for this"?

**Question.** When an agent acts at a third party with a guest's linked
credential (`5/15`), what row says which actions, for which guest, for how long?

**The premise this item shipped with is stale.** It offered "reuse `acl` rows,
or add a predicate layer". `acl` **already has** a `predicate` column
(`routd/migrations/0007-acl.sql`), evaluated on every call
(`auth/authorize.go:62` → `predicateMatches`, line 282): comma-separated
`key=glob` conjunctions matched against the caller's **JWT claims**. The choice
was never acl-vs-predicate. It is: what can a predicate see?

|       | Option                                                                                                                                                | Cost                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| ----- | ----------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A** | Reuse `acl` as-is — e.g. `(principal=folder:<agent>, action=mcp:<tool>, scope=<folder>, params=host=api.github.com, predicate=onbehalf=<guest-sub>)`. | Zero schema. **Blocker, and it is concrete:** the agent socket's gate is `routd/sibling_db.go:192`, which builds `auth.Caller{Principal: sub}` with **no Claims** — so on the agent socket `caller.Claims` is always nil and any row with a non-empty predicate can never match. A grant written through `/v1/acl` with a predicate is silently inert there (BUGS F59). Making A work is a `Caller.Claims` plumbing decision, not a schema one, and it needs its own answer: what does the socket even know about a guest? |
| **B** | A + time: add `expires_at` to `acl` for consent windows.                                                                                              | One column, one clause in `Authorize`'s row loop. Note this spec's §Out of scope already lists `expires_at` as "additive, ship later" — a consent window IS that, so choosing B lifts it out of §Out of scope rather than inventing a layer.                                                                                                                                                                                                                                                                               |
| **C** | A separate `delegations` table with its own evaluator.                                                                                                | A second authorization evaluator beside `auth.Authorize` — the exact thing `5/32`+`5/33` spent a whole cutover collapsing into one. Reject under "one renderer, many sinks" unless A and B are shown insufficient.                                                                                                                                                                                                                                                                                                         |

**Forecloses.** Independent of R2/R5 and of Open 1. It is coupled to `5/31`
pairing (the guest must be identified to the turn before any predicate can name
them) and to `5/15` (the credential must resolve per-guest, not per-folder).

**Recommendation (not a decision).** **A now, B when a consent window is
actually asked for; never C.** The prerequisite — populating `Caller.Claims` on
the agent socket — is a real, unmade judgment call and is named here rather than
hidden inside "reuse `acl` rows".

## Out of scope

Time-bounded grants (`expires_at`), an audit log of permission changes,
on-call rotation for routing — all additive, ship later. Cross-org
collaboration is disallowed by design: worlds are isolation boundaries.

## Ties

`5/18` onboarding · `5/31` pairing · `5/S` JID · `5/15` surrogate OAuth ·
`5/17` one handler, two faces · `5/32`+`5/33` grants · `5/P` runed
sessions/spawns · `5/21` products (`draft`) ·
`5/8` export/apply · `5/28` packages · `5/14` credentials · `5/9` capability
eval · `5/V` web vhosts.

`4/26` prototypes is **not** a tie: the spec file was deleted with the mechanism
in `4a9a49c7` (`specs/4/26-prototypes.md`, -77 lines). It is named in Tier 3 as
history only.
