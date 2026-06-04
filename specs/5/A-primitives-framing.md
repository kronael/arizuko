---
status: draft
depends:
  [
    5-uniform-mcp-rest,
    D-docs-refs-redesign,
    Q-unified-routing,
    E-routd,
    P-runed,
    ../4/9-acl-unified,
  ]
---

# specs/5/A — primitive framing (how to explain arizuko)

A FRAMING spec: it changes no behavior. It distils arizuko to the
smallest honest set of named primitives so a later docs rewrite
(`5/D`) leads with the model, not the daemon zoo. The deliverable is
the framing itself — the primitive names, what each owns, the code
anchor that proves it, and the narrative order docs should follow.

Verified against the tree at write time; every claim carries a
`file:line` anchor. The spec's whole value is being in sync with the
code — a drifted anchor is a bug here.

## Open decisions for sign-off

Three calls the author wants signed off before the docs rewrite
commits to them. The body below assumes the lean in each; flip them
here and the rewrite follows.

1. **6 stages + identity-as-namespace, vs 7 flat primitives.** The
   lean (below): six pipeline stages — Event, Agent, Routing,
   Authorization, Turn, State — with **Identity** named as the
   cross-cutting coordinate system the six are addressed in (subs,
   JIDs, folders, tiers, scopes), not a pipeline stage. Codex's
   alternative: seven flat peers (Event, Agent, Routing, **Identity**,
   Authorization, Execution, State). Identity is genuinely first-class
   in code (`store/identities.go`, `auth/identity.go:10`,
   `types/identity.go`); the only question is whether it reads as a
   stage or a namespace. Lean: namespace — it is referenced by every
   stage, sequenced by none.
2. **Orthogonality wording.** Do NOT claim the primitives are
   independent (⊥). Reality is **layered coupling**: routing reads the
   event's shape and is overridden by engagement/direct-address/sticky
   (`routd/loop.go:382`); authorization is multi-gate (structural +
   ACL rows, `auth/policy.go:18` + `auth/authorize.go:36`); a turn
   needs grants + container config + egress resolved before spawn
   (`routd/dispatch.go:235`). The honest wedge is **"one job each,
   composed in a fixed pipeline — no special cases,"** never
   mathematical independence. Sign off on this phrasing.
3. **Reaction vs Turn.** "Reaction" is the _thesis verb_ for the whole
   event→reaction loop. The runtime stage that actually runs the agent
   is named **Turn** (a.k.a. Execution), not "Reaction". Keeps the
   loop's name and the stage's name from colliding.

## Thesis

arizuko is an **event→reaction engine**: something happens, an agent
reacts. The wedge is that the entire system reduces to a small named
set of primitives, each owning one concern, composed in a fixed
pipeline — **no special cases**. The apparent feature sprawl (channels,
topics, tasks, webhooks, secrets, egress, delegation, observe) is all
_recomposition_ of those primitives, never new machinery.

The primitives are invariant across topologies. The shipped monolith
(`gated`) and the planned split (`authd` + `routd` + `runed`, specs
`5/E`/`5/P`) reorganise the _daemons_; the primitives beneath don't
move — README "Direction" (`README.md:56`): "the per-folder runtime —
all unchanged." So the docs **name primitives, not daemons.** A reader
who learns Event/Agent/Routing/Authorization/Turn/State understands
arizuko whether it runs as one process or four.

## Use cases first

The docs lead with concrete, high-impact scenarios, THEN reveal each
was the same primitives recomposed. Three, chosen for lowest
hand-waving:

1. **Slack @mention → delegate to a child → reply in-thread.** A
   mention arrives as an Event; Routing maps it to a folder (Agent);
   the agent delegates to a child folder; a Turn runs; the reply goes
   out and engagement keeps the thread live without re-mention.
   Exercises Event → Routing → Agent → delegation → Turn → outbound →
   engagement.
2. **A scheduled task wakes an agent that posts to a channel.** A
   `timed` tick is just an Event; the same Routing + Turn run; "tasks"
   are not a new primitive — they are scheduled Events.
3. **A WebDAV drop / webhook POST feeds the SAME agent that chats in
   Slack.** A `davd` file write or a `/hook/<token>` POST
   (`webd/route_token.go:76`) is another Event source; same Agent,
   same State, same Turn. The event _source_ is not a primitive.

After the scenarios: the catalog, then "no special cases."

## The primitives

Each: one-sentence definition; the one concern it owns; its code
anchor; what people mistake for a separate feature but is this
primitive recomposed.

### 1. Event

Anything that happens becomes one row in `messages`. **Owns: the one
inbox.** A chat message, a webhook POST, an inbound email, a WebDAV
file drop, a scheduled tick, a reaction — all land as rows.

Honest note: a row is a rich envelope, not a bare string —
`core.Message` carries `Verb`, `Topic`, `RoutedTo`, `Attachments`,
`Source`, `TurnID`, `Status`, `PlatformID`, `ReplyToID`, … (`core/types.go:16`),
persisted by `store.PutMessage` (`store/messages.go:12`).

Recompositions, not primitives: channel adapters (`teled`, `slakd`, …),
`emaid`, webhooks (`/hook/<token>`), `timed` ticks, `davd` file drops —
all are **event sources writing rows**.

### 2. Agent (folder-as-data)

An agent IS a folder of values — `PERSONA.md`, `skills/`, `MEMORY.md`,
a `.diary/`, route rows, an ACL — plus a `groups` row. **Owns: who
reacts and with what knowledge.** README "Overview" (`README.md:28`):
"A folder is an agent." The org chart is the folder tree, arbitrary
depth (`corp/eng/sre`); `groupfolder.ParentOf`/`NameOf`
(`groupfolder/folder.go:13`) give hierarchy semantics; rows live via
`store.PutGroup` (`store/groups.go:22`).

Recompositions: personas, skills, memory, sub-teams, delegation
targets are all just folder contents / folder structure.

### 3. Routing

Maps an Event to the folder that reacts. **Owns: event → folder.**
Route table `routes(seq, match, target)`, first-match-wins —
`router.ResolveRoute`/`ResolveRouteTarget` (`router/router.go:356`),
stored via `store/routes.go`.

Honest note: routing is _layered_, not a single table lookup.
`routd/loop.go:382` (`resolve`) checks, in order: (1) direct
`web:<folder>` / bare-folder address bypass; (2) the route table; with
engagement overriding the matched route, sticky `#topic` rewriting the
dispatch topic (`effectiveTopic`, `routd/loop.go:423`), and `observe`
mode producing silent ingest with no turn; (3) an engagement fallback
on a route miss. Direct-address, engagement, sticky, observe, delegate,
escalate are routing **layers**, not new primitives. Routing also reads
the event's shape (`Verb`, `Topic`, `ReplyToID`) — it is coupled to
Event by design.

### 4. Authorization

One question — `(principal, action, scope) → allow | deny` — resolved
by `auth.Authorize` (`auth/authorize.go:36`). **Owns: may this
principal do this here.** Tier defaults live in code
(`grants.DeriveRules`, the `mcp:*` fallback); operator overrides are
rows in the `acl` table (`store/migrations/0052-acl-unified.sql:3`),
with identity indirection via `acl_membership`. Canonical spec
`../4/9-acl-unified.md`; pointer `GRANTS.md`.

Honest note: authorization is **multi-gate**, not one switch. A
structural gate enforces tree-shape invariants (caller-folder prefix,
tier bounds, task-owner-must-match) — `auth.AuthorizeStructural`
(`auth/policy.go:18`); the ACL row gate is `auth.Authorize`
(`auth/authorize.go:36`); many tool call-sites need both
(`auth/README.md:10`). Capability scopes carried on tokens are a
fourth layer. Describe the _question_ as one shape; the _resolution_
is layered.

Recompositions: the grants UI, the scope vocabulary, delegation limits
are all views over / restrictions of this one question.

### 5. Turn (execution)

An ephemeral Docker container runs the agent loop for ONE turn, then
exits. **Owns: the bounded reaction.** Every side effect is one MCP
tool call (the same handler a REST endpoint reaches, as the uniform
surface rolls out — `5/5`), through the auth gate, written to one
`audit_log` row (`store/migrations/0066-audit-log.sql:30`). The
container runtime is `runed/docker.go` (`dockerRuntime.Run`,
`runed/docker.go:63`); the spawn request — model, container config,
grants, egress allowlist all resolved _before_ spawn — is built in
`routd/dispatch.go:220` (`dispatchRun`; egress resolved at
`routd/dispatch.go:235`).

Honest note: a Turn is not free-standing — it needs grants + container
config + session + egress resolved first; that resolution is the
coupling between Authorization, State, and Turn.

Recompositions: tasks, autocalls, interjections are all just Turns
triggered by different Events.

### 6. State

Two durable stores, **by design** — not a cache layer:

- **Shared relational DB** (`messages.db`): events, routes, grants,
  ACL, sessions — `store.Store` over SQLite WAL (`store/store.go:19`).
- **Per-agent folder on disk**: `PERSONA.md`, `skills/`, `MEMORY.md`,
  `.diary/` — the agent-as-data substrate (`README.md:28`,
  `README.md:52`). The folder is NOT a cache; containers are stateless
  and mount it per turn (`README.md:44`).

A tar of one instance dir is a complete backup (`README.md:88`).

Honest note: the planned split adds **per-plane DBs** — `routd.db` and
`runed.db` (specs `5/E`, `5/P`). State the monolith reality as
canonical; note the split exists.

### Identity (cross-cutting, not a stage)

Identity is the **namespace everything is addressed in**: canonical
subs + identity claims (`store/identities.go:13`), the runtime
`Identity{Folder, Tier, World}` (`auth/identity.go:10`), the
boundary-crossing IDs `UserSub`/`Folder`/`Tier`/`Scope`
(`types/identity.go`), JIDs, and capability scopes. Every other
primitive references it — Routing resolves to a folder, Authorization
keys on a principal, a Turn runs as `sub: agent:<folder>`, State is
folder-partitioned — but none _sequences_ it. Hence: a coordinate
system, not a pipeline step. (See open decision 1 if it should instead
be a seventh peer.)

## No special cases

Each "feature" reduces to a primitive recomposed. This table is the
docs' proof that the surface is small.

| Apparent feature           | Is really…                                                 | Primitive            |
| -------------------------- | ---------------------------------------------------------- | -------------------- |
| Channel adapters           | event sources writing `messages` rows                      | Event                |
| Webhooks (`/hook`)         | an event source (`webd/route_token.go:76`)                 | Event                |
| Inbound email (`emaid`)    | an event source                                            | Event                |
| WebDAV drop (`davd`)       | an event source                                            | Event                |
| Scheduled tasks            | Events on a `timed` tick → a Turn                          | Event + Turn         |
| Autocalls / interjection   | Turns triggered by a non-chat Event                        | Turn                 |
| Topics                     | a scoping field on the Event + a Routing layer (sticky)    | Event + Routing      |
| Engagement                 | a Routing override keyed on `(jid, topic)`                 | Routing              |
| Observe                    | a Routing mode: silent ingest, no Turn                     | Routing              |
| Delegation / escalation    | Routing to a child/parent folder                           | Routing + Agent      |
| Personas / skills / memory | folder contents                                            | Agent (folder-data)  |
| Grants / scopes            | views over the one `Authorize` question                    | Authorization        |
| Secrets                    | folder/user-scoped State resolved into a Turn              | State + Turn         |
| Egress allowlist           | per-folder State resolved before spawn (`dispatch.go:235`) | State + Turn         |
| Tasks dashboard / CLI      | a second surface over the same handlers (`5/5`)            | (surface, not prim.) |

Every row is recomposition. None introduces machinery absent from the
six primitives + identity namespace.

## Honesty notes

The docs rewrite must NOT reintroduce these false claims (a codex
review + a CTO audit flagged them):

- **Not strict orthogonality.** The primitives are layered and coupled
  (open decision 2). Routing depends on Event shape and is overridden
  by engagement/direct-address/sticky (`routd/loop.go:382`).
  Authorization is multi-gate (`auth/policy.go:18` + `auth/authorize.go:36`).
  A Turn needs grants/config/egress resolved first (`routd/dispatch.go:235`).
  Frame the wedge as "one job each, fixed pipeline, no special cases" —
  not independence.
- **Not "one SQLite is the only truth."** Two durable stores by design:
  the shared DB _and_ the per-agent folder (`README.md:28`,
  `store/store.go:19`). The folder is not a cache. The split adds
  per-plane DBs (`5/E`, `5/P`).
- **"One rule gates agent MCP and operator REST" is the TARGET, not
  shipped fact.** It rolls out resource by resource — only some
  resources are on the uniform surface today: README "Direction"
  (`README.md:54`) notes the _first_ resource (`proxyd/resource.go`)
  already runs the pattern; the rest follow. Spec `5/5` is `status:
partial`. Mark it as direction, not done.
- **The grant primitive in code is the unified ACL, not just
  `grant_rules`.** `grant_rules`/`grants.DeriveRules` is the
  legacy/tier-default path; the unified model is the `acl` +
  `acl_membership` tables resolved by `auth.Authorize`
  (`store/migrations/0052-acl-unified.sql`, `../4/9-acl-unified.md`).
  Describe authorization as the one `(principal, action, scope)`
  question; note tier defaults live in code.

## Docs-rewrite implications

What changes when this framing lands (do NOT edit these here — listed
for the `5/D` rewrite to pick up):

- **`index.html` lead.** Open on event→reaction + "everything is a
  handful of primitives," then the three use cases — not a daemon list.
- **`concepts/index.html` curriculum order (`5/D` § Concepts
  walkthrough).** Re-sequence so the primitive arc reads
  Event → Agent → Routing → Authorization → Turn → State, with
  identity threaded through; the per-platform/feature pages become
  "this is primitive X recomposed."
- **Possible new `concepts/primitives.html`.** A single page holding
  the catalog + the "no special cases" table as the conceptual spine
  the tour hangs off. Insert into the curriculum order and fix
  neighbouring pagers per `5/D` § Maintenance.
- **Reference pages unchanged in shape** (`5/D` ownership rule):
  concepts owns the narrative primitive framing; reference still owns
  exhaustive grammar/fields.
