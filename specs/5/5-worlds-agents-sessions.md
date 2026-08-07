---
status: shipped
shipped: 2026-08-07
depends: [18-onboarding-model, S-jid-format, 15-surrogate-oauth]
---

# specs/5/5 — worlds, agents, sessions

> Tenancy is three words for folder shapes that already exist:
>
> ```
> World      → a top-level folder; where users onboard
>   Agent    → a folder with a persona, its skills, memory, route bindings
>     Session → one run: the turn container and its subagent spawns
> ```
>
> **What shipped on 2026-08-07 is the decision that none of the three becomes
> a table.** The words stay vocabulary. The primitives under them — folders,
> `acl` rows, `invites`, `spawns.run_id` — are what exist and what you build
> against.

## How to read this

This spec shipped as a **decision record**. Six questions had accumulated on
it; five were answered on 2026-08-07 and one is deliberately still open. Four
of the five answers are decisions **not** to build, so `shipped` here does not
mean a tier framework exists — it means the framework was costed and declined,
and the reasoning is written down so nobody re-derives it.

- **What is built** → [§Built](#built), and the tier sections that follow it.
- **What was decided, and why** → [§Decisions (2026-08-07)](#decisions-2026-08-07).
- **What is genuinely still open** → [§Still open](#still-open) — one item,
  which blocks nothing here.

Depth used to be an authority axis (`tier = min(count("/"), 3)`); `5/33`
deleted it, so depth confers no power anywhere. Deep folders still work as
coordinates and are still used live (krons runs `eval/eti0hsk-priv-grant-g`,
`krons/public/marble`) — only the claim that depth _structures tenancy_ is
dropped.

## Built

- **Invites** — the tier-1 admission primitive (mechanism in
  [Tier 1](#tier-1--world)). An onbod-owned resreg resource
  (`resreg/resources/invites.go`, `DB: SubsystemOnbod`) serving `/v1/invites`,
  with the `invite_create` / `invite_list` / `invite_revoke` agent tools
  (`ipc/ipc.go:1881,1937,1964`), the `arizuko invite` CLI verb
  (`cmd/arizuko/main.go:753` `cmdInvite`), and the `/dash/invites/` operator
  page (`dashd/invites.go`).
- **World creation is CLI-only, enforced** — a top-level `register_group` from
  the agent socket is refused with "worlds are CLI-only"
  (`routd/groups_resource.go:150`). That is R1, and it is code, not a proposal.
- **Products** — a `groups` column (`routd/migrations/0001:10`, default
  `assistant`) plus TOML templates the CLI applies (`cmd/arizuko/products.go`,
  `arizuko create --product`, `arizuko register --product`).
- **Secrets** — shipped as `5/14`'s credential model, which supersedes this
  spec's original §Secrets shape outright (see
  [§Secrets (Phase C)](#secrets-phase-c)).

Not built, and by [§Decisions](#decisions-2026-08-07) not being built: any
`worlds` table, any `world_*`/`agent_*` resource, any session downscope. Topic
`kind` is unbuilt for want of a caller, not for want of a decision
([§Topic kinds](#topic-kinds-unbuilt)).

## Vocabulary

A **group** is a folder identified by a path — a pure coordinate. A **topic**
is the transient work-unit overlaid on a group; many topics per group. The
org-chart mapping frames the tiers below: an organization is a world, a job
description is a grant rule list, the mailroom is the routes table, hiring is
invite + grant, off-boarding is revoke.

## Tier 1 — World

The top-level boundary where **users are onboarded** (`5/18`). Its users can
invite others; invited users are **guests**.

A world **owns nothing of its own**. This is the substance of decision R2, and
it is worth stating positively rather than as an absence, because each of the
four things a `worlds` row was once proposed to own is already owned, derived,
or expressible elsewhere:

- **Roster** — `acl` rows scoped `<world>/**` plus `acl_membership`
  (`routd/migrations/0007-acl.sql`, `5/32`/`5/33`). The scope glob IS the
  containment.
- **Web vhost** — **derived, never stored.** `5/V` (shipped) retired
  `vhosts.json` on purpose: host `W.<HOSTING_DOMAIN>` 302s to `/pub/W/` with
  no per-host config and no DB table, and the label≠name case is the
  `WEB_VHOST_ALIASES` env (`groupfolder.ParseVhostAliases`,
  `groupfolder/folder.go:108`).
- **World-scoped secrets** — `store.FolderSecretsResolved`
  (`store/secrets.go:473`) walks folder ancestry deepest-wins, so the
  top-level folder's rows already ARE the world default.
- **Grant root** — the one genuinely unowned thing, and it stays unowned.
  `auth.Authorize` has no per-world root; `role:operator`
  (`routd/migrations/0022`) is instance-wide. See R2 for why that is a `5/33`
  question rather than a table.

A world is **not** a billing boundary either. The cap primitive ships
(`11/19`: `store.FolderCap` / `store.UserCap`) but is read per **exact** folder
(`store/cost_log.go:66`) and per user (`user_profiles`, line 87), with no
roll-up to the top-level folder — and `0` means uncapped, which is the schema
default (`routd/migrations/0001:11`).

**Invites.** Opaque bearer tokens that produce a grant on acceptance. onbod
owns the table (`onbod/migrations/0001-onboarding.sql`, moved to hash-at-rest
by `0003-invites-hash-at-rest.sql`: the PK is `ref = hex(sha256(token))` and
the raw bearer is never persisted); surfaces are listed in [§Built](#built).
Two modes on `target_glob`: a trailing slash (`atlas/`) is **subgroup-create**
— the recipient picks a username, `atlas/<username>` is created and they are
granted it; no trailing slash (`atlas/support`) is **join** — the recipient is
granted the existing group directly. `**` is reserved and rejected as a
`target_glob`. An issuer holding grants on `target_glob` mints a token → the
recipient opens `/invite/<token>` → OAuth → the token is consumed and the
grant issued or the group created → `used_count` increments and the row stays
for audit. A failed grant write rolls the consume back (`RestoreInvite`) — an
invite is never burned without its grant. The created subgroup is seeded by
`container.SetupGroup(cfg, folder, "")` — the **default** group scaffold,
empty seed dir. There is no `groups/<world>/prototype/` template dir; this
spec used to claim one and no code has ever read it.

Onboarding a user = admitting them to the world; world _creation_ stays
operator/CLI-only (R1).

## Tier 2 — Agent (the main group)

A folder with one persona (`5/21`), its skills, memory, and route bindings.
Creating an agent seeds a group (`container.SetupGroup`, called from
`onbod/main.go:868` and four other sites). Agents are **peers within a
world**, not nested; grants are `acl` rows (`5/32`).

Nothing in production distinguishes a top-level folder from a nested one
except `routd/groups_resource.go:150`'s inline `!strings.Contains(gfld, "/")`.
`groupfolder.IsTopLevel` exists but has **zero** production callers
(test-only). Under R2 that stays the whole surface — one predicate, not a
lookup.

## Tier 3 — Session

One agent run: the turn container (`5/P`), its **subagent spawns**,
auto-onboarding, and prototyping.

**The word `session` is taken, three times over,** so nothing here may
register as `sessions`. `sessions` is already a globally-unique wire identity
(`resreg/resources/names.go` `SessionsName`) owned by **authd** —
refresh-token families at `/v1/sessions`, no MCP face. Separately, routd has a
`sessions` TABLE that is the topic→Claude-session-id map
(`routd/migrations/0001:81`, PK `(group_folder, topic)`). This tier's
"session" is a third thing: a run, keyed `spawns.run_id`
(`runed/migrations/0001:19`). Per CLAUDE.md's "a resource's name IS its wire
identity, globally unique", the name to use is `runs` — the one the data
already uses.

**Subagents have no identity, and by R4 they are not getting one yet.**
`grep -rn subagent --include=*.go` returns nothing. A turn is credentialed by
the MCP socket's `SO_PEERCRED`, not a token, so a Claude Code subagent inside
the container shares the parent's socket and therefore holds the parent's
grants **exactly** — a strict equal, not a subset. The `mcp_tokens` per-spawn
capability-token table was dropped as unconsumed
(`runed/migrations/0003-drop-mcp-tokens.sql`), so there is no broker shell left
to build a downscope on.

Prototyping has **one** mechanism, and it is not a prototype feature.
Automatic prototype spawn was deleted (`4a9a49c7`, with `4/26`): a delegated
message to an unrouted folder no longer creates anything —
`routd/steer.go`'s `delegateViaMessage` errors naming the folder and the tool
that creates it. Groups are never created by routing.

"Make me a copy of this group at that path" needs no mechanism of its own:
`5/8` already ships the two primitives. Export is **instance-wide**
(`arizuko export <instance>`, `cmd/arizuko/apply.go:313`, one manifest per
subsystem); the target-path half is `arizuko apply --as-folder <folder>`
(`cmd/arizuko/retarget.go`, over `resreg.Resource.Retarget`). Both are
**operator CLI only — no MCP tool exists for either**, so an agent cannot run
this recipe itself, and `Retarget` rewrites DB rows only
(`cmd/arizuko/retarget.go:76`), never files. Spawning a prototype, seeding a
`5/28` package group, and cross-instance folder migration are each a **recipe**
over those two verbs — product guidance, not a spec, not a new tool. Anything
that copies-and-registers behind one verb would be a second mechanism beside
`5/8`'s, which is why the old one went.

## Guests & delegated OAuth

- **Guest** — a world user invited by another, not an operator. Admitted at
  Tier 1, granted into specific agents at Tier 2. A guest arriving on a
  channel is anonymous and holds no grants until **paired** (`5/31`) —
  pairing is what makes a channel identity a guest rather than a stranger.
- **Account linking** — outbound only, distinct from pairing: a guest links a
  platform account via surrogate OAuth (`5/15`) so the agent can act _as_ them
  at a third party. Pairing proves who the guest is _to_ arizuko; surrogate
  lets arizuko act _for_ them elsewhere.
- **Delegated use** — within a session the agent may act **as** a guest with
  their linked credentials, gated by explicit rules: which actions, which
  agents, consent, revocation, audited. The rule layer is the one item in
  [§Still open](#still-open).

## Topic kinds (unbuilt)

Topics may carry a `kind` — `task`, `project`, `meeting`, `question`,
`discussion`, `incident`, or the default `thread`. Kind drives kind-specific
workflow verbs (`set_due` on a task, `set_attendees` on a meeting). It is
metadata on the topic, **not** a hierarchy level.

There is no "topic node" to hang it on: a topic is a **column**, on routd's
`sessions` (PK `(group_folder, topic)`) and on `messages`, with
`chats.sticky_topic` for sticky routing. So `kind` is one `ALTER TABLE
sessions ADD COLUMN kind` plus verb dispatch — not a new entity. It depends on
no decision here and never did; it is unbuilt for want of a caller.

## Secrets (Phase C)

Folder- and user-scoped secrets, independent of the hierarchy — **shipped**,
as `5/14`'s credential model, which fully supersedes this spec's original
§Secrets shape (`chats.kind` gate, scope model).
`FolderSecretsResolvedForUser` (`store/secrets.go:532`) does a live folder walk
(deepest wins) with a user overlay. **Not** built: the call-time broker
(`5/13` shape 3) — capability credentials still inject at container spawn,
interim, instead of a key that never lands in container env. Adoption, for
context: `secrets` holds **one row fleet-wide** across all three live
instances.

## Decisions (2026-08-07)

All six are recorded here as settled. R1 was already enforced in code; **R2,
R3, R4 and R5 and Open 1 were signed off by the operator on 2026-08-07**,
each accepting the option marked ✓ below. Costs were measured against the tree
at `f1ed8e29`; the code pointers were re-verified against `2fb39720` when the
sign-off was recorded.

Rejected options are kept with their cost, one line each — that is the WHY,
and it is the part that stops the question being reopened from scratch.

### R1 — world creation is operator/CLI-only

**Decided: yes, and enforced in code.** No `world_create` agent tool.
`world_invite`-shaped admission puts a user INTO an existing world.
`onbod.createWorldTx` (`onbod/main.go:921`) is an operator-plane act,
consistent with the anteval case requiring an agent to refuse rather than
claim success (`5/9`). The refusal is live at `routd/groups_resource.go:150`.

### R2 — does a world get its own row?

**Question.** Should a world become a `worlds` row with agents as its
children, meaning every existing top-level folder `W` splits into a World `W`
plus an implicit Agent `W/root`?

**✓ A — no `worlds` table. A world stays a folder-shape predicate.**

Cost: zero. No migration, no resource, no rename. Three consequences, recorded
so they are not rediscovered as surprises:

1. **The per-world grant root remains unavailable.** `role:operator` stays
   instance-wide (`routd/migrations/0022`). If a per-world root ever becomes a
   firm requirement it is a **`5/33` question — a scoped `acl` row — not a
   table.** Reopening R2 is the wrong move; go to `5/33`.
2. **This spec's roster / vhost / secrets-root ownership claims are deleted**
   as already-covered. Tier 1 above now states where each actually lives
   (`acl` scope glob, `WEB_VHOST_ALIASES`, `FolderSecretsResolved`'s ancestry
   walk). All three re-verified against code at sign-off.
3. **Open 1 answers "no change"** — with no `world`/`agent` split there is
   nothing new for a JID to name.

Rejected:

- **B — `worlds` table, thin (grant root only):** 1 routd migration + backfill
  - one resreg pair (~300 lines), and `routd/groups_resource.go:150` must be
    rewritten to read the table or two answers to "is this a world" drift. It is
    the only option that delivers a per-world grant root — but that root is an
    `acl` row under `5/33`, which collapses B into A.
- **C — `worlds` table owning roster + vhost + secrets root + grant root, with
  the `W`→`W`+`W/root` migration:** adds a `vhost` column `5/V` deliberately
  deleted, a roster beside the `acl` scope glob, and a secrets root beside
  `FolderSecretsResolved`'s walk — three parallel second paths, three "no
  duplication" violations. Its migration renames `W`'s `web:` JID, IPC socket
  path, container home, `/pub/W/` root and every `acl` scope glob at once: the
  0042/0043 typed-JID cutover's blast radius, on live tenants, bought for
  vocabulary.

### R3 — does a new user's first contact run an agent?

**Question.** A user completes onboarding: canned `ONBOARDING_GREETING`
(`onbod/main.go:288`, prepended to the pairing prompt at `466-467`) or a real
turn from the world's agent?

**✓ A — keep the canned greeting.** Cost zero: instant page, no spend, no
container. What ships today.

**The condition under which this flips.** B becomes viable **once a spend cap
is enforced on the admission path**. Note precisely what is missing: the cap
_primitive_ already ships (`11/19`, `store.FolderCap`), but
`cost_cap_cents_per_day` defaults to `0` = uncapped
(`routd/migrations/0001:11`) and nothing on the admission path sets or
requires one. So B is not blocked on inventing a cap — it is blocked on the
admission path refusing to run a turn into an uncapped folder. Until then it
is a signup-driven cost channel on a path anyone can reach.

Rejected:

- **B — onboarding enqueues a message to the world folder:** onbod already
  writes routd's DB under the FS-mount write-discipline, so it is a call-site
  swap, not new plumbing. The real cost is operational — one uncapped agent
  turn per admission, plus a container spawn's latency on a flow where the
  user sees a page immediately today.
- **C — canned greeting, agent turn queued behind it:** B's cost plus a second
  delivery; the user sees two messages.

R3 is sequencing, not shape — it foreclosed nothing and never blocked R2.

### R4 — should a subagent hold fewer grants than its parent?

**Question.** A Claude Code subagent inside a turn container holds the parent
agent's grants **exactly** — same `SO_PEERCRED` socket, same `folder:<path>`
principal. Downscope it?

**✓ A — accept it: subagent = parent.** Cost zero. The accepted failure mode,
stated plainly: a subagent spawned for a narrow job can call every tool the
parent holds, secrets and egress included. This is a containment property of
the socket, not an oversight, and it is the right trade until a subagent runs
untrusted input.

**Shipped with this decision:** `auth/delegate.go`'s header comment claimed
delegation was "used at spawn (a parent agent seeds a child's grants)". It is
not — `auth.Delegate` has exactly one production call site,
`routd/acl_resource.go:213` (`add_acl`). The comment described an unbuilt
design as shipped; it now describes the one site that exists and names R4 as
the reason there is no second.

Rejected:

- **B — per-spawn downscoped principal:** mint `folder:<f>#<run_id>`,
  `auth.Delegate` the subset, bind a **second** socket, garbage-collect rows
  per run. Touches `ipc/`, `container/`, `runed/` **and** the ant TS side (the
  subagent launcher must be pointed at the second socket). A second socket is
  exactly the parallel path this codebase spends effort removing. Revisit when
  a subagent runs untrusted input.
- **C — token-based downscope via a capability broker:** not available.
  `mcp_tokens` was dropped as unconsumed (`runed/migrations/0003`), so C would
  rebuild the broker and then do B anyway.

B would have needed a name for the session principal — Open 1's question in
another hat. Open 1 answering "no JID segment" leaves that principal
`run_id`-derived and invisible to routing, which is consistent with A.

### R5 — are `product` and `prototype` one authored asset?

**Question.** Both once meant "persona + skills + seed files, copied into a
new folder". One asset, or is `prototype` dead vocabulary?

**✓ A — product-only. "Prototype" leaves the vocabulary.** Doc-only change:
`product` already ships (see [§Built](#built)), while the `prototype/` dir
mechanism was deleted with `4/26` (`4a9a49c7`) and has no code — `grep -rn
prototype --include=*.go` returns only historical comments. One word, and it
is the one with shipped code behind it.

Rejected:

- **B — unify onto the `5/8` manifest (a product becomes an exported manifest
  applied with `--as-folder`):** a real idea, blocked on a real gap — products
  carry **files**, and `Retarget` rewrites DB rows only
  (`cmd/arizuko/retarget.go:76`). Unifying needs a **file half of `5/8` that
  does not exist**. Filed there, at `5/8` §"Path retargeting", rather than
  left blocking this spec.
- **C — leave open:** two words for one shape, indefinitely.

### Open 1 — can a user address a single run?

**Question.** A run is reachable today only as `spawns.run_id` through runed's
API — nothing can route a message _to_ a run. Should a session earn a JID
segment (`web:<world>/<agent>/<session>`) so it becomes a routable address?

**✓ A — `run_id` only, no JID change.** This was filed as an open question;
R2 answers it. With no `world`/`agent` split there is nothing new for a JID to
name, so the answer is **no change**, not a deferral. Cost zero, `5/S`
untouched. A run stays an API object; the routable sub-unit stays the
**topic**, which already exists (`sessions.topic`, `#topic` sticky,
`chats.sticky_topic`) and is already outside the JID.

Rejected:

- **B — session as a JID segment:** `groupfolder.JidFolder` returns the
  **whole** rest for `web:` (`groupfolder/folder.go:90`) because a folder may
  itself contain `/`, so a trailing session segment requires teaching every
  `web:` JID where the folder stops — exactly the token-vs-sub split `5/S`
  **deliberately deferred** ("`web:` stays folder-keyed… deferred until the
  web stack itself splits them"). Plus a migration of
  `messages.chat_jid`/`sender`, `chats.jid`, `user_jids.jid`, `acl` scope
  globs and every `chat_jid=web:*` route predicate — the 0042/0043 cutover
  shape again. **Recorded as gated on `5/S`**, not on this spec: B needs
  `W-webhook-routes` to split route-token routing from JWT identity first.
- **C — promote **topic** into the JID instead:** pays B's `web:` split cost
  for a unit that works fine outside the JID today, and still does not make
  runs addressable. Strictly worse than B.

## Still open

One item — Open 2. (Open 1 was answered on 2026-08-07 and moved to
[§Decisions](#decisions-2026-08-07); the numbering is kept so older references
still land.) It blocks nothing in this spec: delegated use is unbuilt, no
built surface here depends on it, and its prerequisites are owned by other
specs.

### Open 2 — what expresses "the agent may act as this guest, for this"?

**Question.** When an agent acts at a third party with a guest's linked
credential (`5/15`), what row says which actions, for which guest, for how
long?

**Why it is genuinely open, and why it is open _here_.** The premise it was
filed under is stale: it used to offer "reuse `acl` rows, or add a predicate
layer", but `acl` **already has** a `predicate` column
(`routd/migrations/0007-acl.sql`), evaluated on every call
(`auth/authorize.go:62` → `predicateMatches`, line 282) as comma-separated
`key=glob` conjunctions against the caller's **JWT claims**. So the choice was
never acl-vs-predicate. The live question is narrower and harder: **what can a
predicate see on the agent socket?**

The socket's gate is `routd/sibling_db.go:191`, which builds
`auth.Caller{Principal: sub}` with **no Claims**. `caller.Claims` is therefore
always nil there, `predicateMatches` returns false for any non-empty
predicate, and a grant written through `/v1/acl` with a predicate is silently
inert on the one surface it was written for. That is a bug, tracked as
`BUGS.md` **F59**, and it fails closed — but the fix requires deciding **what
a turn knows about a guest** (paired sub? run id? elevation?) and threading it
through `ipc.Server.Authorize`, whose signature (`ipc/ipc.go:244`) has no
claims parameter. That is an auth-model judgment, not a schema one, and it is
this spec's to make — F59 option (b) defers to here on purpose.

Direction, when it is taken up: **reuse `acl` as-is** — e.g.
`(principal=folder:<agent>, action=mcp:<tool>, scope=<folder>,
params=host=api.github.com, predicate=onbehalf=<guest-sub>)` — zero schema,
once Claims are populated. Add `expires_at` to `acl` only when a consent
window is actually asked for; §Out of scope already lists it as additive, and
a consent window IS that. **Never** a separate `delegations` table with its
own evaluator — a second authorization evaluator beside `auth.Authorize` is
the exact thing `5/32`+`5/33` spent a cutover collapsing into one.

**Prerequisites, both external to this spec:** `5/31` pairing must identify
the guest to the turn before any predicate can name them, and `5/15` must
resolve the credential per-guest rather than per-folder. Neither is a `5/5`
deliverable, which is why this item does not hold `5/5` open.

## Out of scope

Time-bounded grants (`expires_at`), an audit log of permission changes,
on-call rotation for routing — all additive, ship later. Cross-org
collaboration is disallowed by design: worlds are isolation boundaries.

## Ties

`5/18` onboarding · `5/31` pairing · `5/S` JID · `5/15` surrogate OAuth ·
`5/17` one handler, two faces · `5/32`+`5/33` grants · `5/P` runed
sessions/spawns · `5/21` products (`draft`) · `5/8` export/apply ·
`5/28` packages · `5/14` credentials · `11/19` cost caps · `5/9` capability
eval · `5/V` web vhosts.

`4/26` prototypes is **not** a tie: the spec file was deleted with the
mechanism in `4a9a49c7` (`specs/4/26-prototypes.md`, -77 lines). It is named
in Tier 3 as history only.
