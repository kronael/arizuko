---
status: shipped
depends:
  [
    5-uniform-mcp-rest,
    D-docs-refs-redesign,
    Q-unified-routing,
    E-routd,
    P-runed,
    X-session-workflows,
    ../4/9-acl-unified,
    ../16/9-positioning,
  ]
---

# specs/5/A — primitive framing (how to explain arizuko)

A FRAMING spec: it changes no behavior. It distils arizuko to the
smallest honest set of named primitives, then shows how those
primitives stack into the things you deploy and ship — so the docs,
the README, and the one-pager lead with the model, not the daemon
zoo. The deliverable is the framing itself: the primitive names, what
each owns, the code anchor that proves it, the four-layer decomposition
they build into, and the grand-message wording each surface reuses.

Verified against the tree at write time; every claim carries a
`file:line` anchor or a package name. The spec's whole value is being
in sync with the code — a drifted anchor is a bug here.

## Decisions (signed off)

These three calls were debated and are now locked; the body assumes
them.

1. **Six pipeline primitives + Identity as a coordinate system.** The
   visible pipeline is six stages — Event, Routing, Agent,
   Authorization, Turn, State. **Identity** is the coordinate system
   the six are addressed in (subs, JIDs, folders, tiers, scopes), not a
   seventh stage: every stage references it, none sequences it. Identity
   is genuinely first-class in code (`store/identities.go:13`,
   `auth/identity.go:10`, `types/identity.go`) — it reads as a
   coordinate system, not a step.
2. **"One job each, fixed pipeline, layered overrides — no special
   cases."** Do NOT claim the primitives are independent (⊥). Reality is
   layered coupling: routing reads the event's shape and is overridden
   by engagement/direct-address/sticky (`routd/loop.go:382`);
   authorization is multi-gate (`auth/policy.go:18` + `auth/authorize.go:36`);
   a turn needs grants + container config + egress resolved before spawn
   (`routd/dispatch.go:235`). The honest wedge is "one job each, fixed
   pipeline, no special cases," never mathematical independence.
3. **Reaction is the loop; Turn is the stage.** "Reaction" is the thesis
   verb for the whole event→reaction loop. The runtime stage that
   actually runs the agent is named **Turn** — so the loop's name and
   the stage's name don't collide. **Workflow is not a seventh
   primitive**: it is an operating discipline at the session boundary,
   implemented by Turn + State + the session lifecycle (see "Workflows"
   below and `5/X`).

## Thesis

arizuko is an **event→reaction engine**: something happens, an agent
reacts. The wedge is that the entire system reduces to a small named
set of primitives, each owning one concern, composed in a fixed
pipeline — no special cases. The apparent feature sprawl (channels,
topics, tasks, webhooks, secrets, egress, delegation, observe) is all
_recomposition_ of those primitives, never new machinery.

The payoff is products. You don't assemble agent demos from low-level
wiring; you ship **ownable agents as real products** — a Slack team
agent, a personal reality agent, a company brain — each a folder of
files you can diff, review, fork, and `git revert` on your own host.
The same fixed pipeline serves all of them; only the folder contents
and the routing differ. Ownership is what makes deep customization
possible — the thing you need at this stage of AI, against opaque SaaS
agents you can only tune through a text box (`6/9`).

The deeper wedge is _how_ you shape it: through language, not config.
You mold the system by talking to LLMs that edit the files that ARE the
system — persona, memory, skills, routing, even an operational fix. The
agent is both what runs and how you author; there is no separate config
console, because the conversation is the console. What keeps that safe is
the daemon structure: each primitive lives in its own compartment (`routd`
routes, `runed` executes, `authd` authorizes) with hard boundaries —
folder scoping, the authz gate, ephemeral per-turn containers — so
LLM-driven shaping stays bounded to what the grants allow. You reshape a
system in natural language without it becoming a black box you can't
audit: every change lands as files you diff and `git revert`.
Compartmentalized daemons make the molding safe; ownership makes it
permanent. (This is the evolved lead — primitives and ownership are the
two halves it rests on, not competing angles.)

The primitives are invariant across topologies. The former monolith
(`gated`, now removed) and the split (`authd` + `routd` + `runed`, specs
`5/E`/`5/P`, the only topology) reorganised the _daemons_; the primitives
beneath don't move — README
"Direction" (`README.md:56`): "the per-folder runtime — all unchanged."
So the docs **name primitives, not daemons.** A reader who learns
Event/Routing/Agent/Authorization/Turn/State understands arizuko
whether it runs as one process or four.

## Use cases first

The docs lead with concrete, high-impact scenarios, THEN reveal each
was the same primitives recomposed. Three, chosen for lowest
hand-waving:

1. **Slack @mention → delegate to a child → reply in-thread.** A
   mention arrives as an Event; Routing maps it to a folder; the
   Agent delegates to a child folder; a Turn runs; the reply goes out
   and engagement keeps the thread live without re-mention. Exercises
   Event → Routing → Agent → delegation → Turn → outbound → engagement.
2. **A scheduled task wakes an agent that posts to a channel.** A
   `timed` tick is just an Event; the same Routing + Turn run; "tasks"
   are not a new primitive — they are scheduled Events.
3. **A WebDAV drop / webhook POST feeds the SAME agent that chats in
   Slack.** A `davd` file write or a `/hook/<token>` POST
   (`webd/route_token.go:76`) is another Event source; same Agent,
   same State, same Turn. The event _source_ is not a primitive.

After the scenarios: the catalog, then "no special cases," then the
four-layer decomposition.

## The primitives

Pipeline order — the runtime starts from "something happened, where
does it go?", so Routing precedes Agent. Each: one-sentence definition;
the one concern it owns; its code anchor; what people mistake for a
separate feature but is this primitive recomposed.

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

### 2. Routing

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

### 3. Agent (folder-as-data)

An agent IS a folder of values — `PERSONA.md`, `skills/`, `MEMORY.md`,
a `.diary/`, route rows, an ACL — plus a `groups` row. **Owns: who
reacts and with what knowledge.** README "Overview" (`README.md:28`):
"A folder is an agent." The org chart is the folder tree, arbitrary
depth (`corp/eng/sre`); `groupfolder.ParentOf`/`NameOf`
(`groupfolder/folder.go:13`) give hierarchy semantics; rows live via
`store.PutGroup` (`store/groups.go:22`).

Recompositions: personas, skills, memory, sub-teams, delegation
targets are all just folder contents / folder structure. **A product
is one such recomposition** — a folder with the right persona, skills,
and routes (see "The four layers").

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

Honest note: the split adds **per-plane DBs** — `routd.db` and
`runed.db` (specs `5/E`, `5/P`). State the monolith reality as
canonical; note the split exists.

### Identity (the coordinate system)

Identity is the **coordinate system everything is addressed in**:
canonical subs + identity claims (`store/identities.go:13`), the
runtime `Identity{Folder, Tier, World}` (`auth/identity.go:10`), the
boundary-crossing IDs `UserSub`/`Folder`/`Tier`/`Scope`
(`types/identity.go`), JIDs, and capability scopes. Every other
primitive references it — Routing resolves to a folder, Authorization
keys on a principal, a Turn runs as `sub: agent:<folder>`, State is
folder-partitioned — but none _sequences_ it. Hence: a coordinate
system, not a pipeline step.

### Workflows (operating discipline, not a primitive)

A multi-step job thrashes when each turn re-improvises from chat
scrollback. The fix is not a workflow engine: it is making the
**session/thread boundary** run a fixed opening before the agent
free-associates — load context, open a plan-of-record file, apply the
group's `CLAUDE.md` workflow, scan skills — then converse freely
(`5/X`). Conversation stays free; the _opening_ gets strict (the
shopkeeper greets, recalls your table, reads the order back, then
acts).

This is **Turn + State + the session lifecycle**, not new machinery:
the session boundary already injects a `new_session` system message
(`5/A` Turn / gateway); the discipline tightens that injection into a
non-skippable opening directive and adds a `~/plans/<topic>.md`
artifact. So workflows are recomposition too — an operating rule on the
boundary of a Turn, addressed in the same Identity coordinates. Naming
it a seventh primitive would imply a separate engine that does not
exist.

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
| Workflows                  | an enforced opening at the session boundary                | Turn + State         |
| Grants / scopes            | views over the one `Authorize` question                    | Authorization        |
| Secrets                    | folder/user-scoped State resolved into a Turn              | State + Turn         |
| Egress allowlist           | per-folder State resolved before spawn (`dispatch.go:235`) | State + Turn         |
| A product                  | a folder with persona + skills + routes                    | Agent (recomposed)   |
| Tasks dashboard / CLI      | a second surface over the same handlers (`5/5`)            | (surface, not prim.) |

Every row is recomposition. None introduces machinery absent from the
six primitives + identity coordinate system.

## The four layers

The "no special cases" claim has a structural twin: the same small set
of concepts stacks, cleanly, into the things you deploy and ship. This
is the spine of the grand message — primitives become packages become
processes become products.

| Layer          | What it is                      | Examples                                                                           |
| -------------- | ------------------------------- | ---------------------------------------------------------------------------------- |
| **Primitives** | invariant concepts              | Event, Routing, Agent, Authorization, Turn, State (+ Identity)                     |
| **Components** | Go packages that implement them | `store`, `chanlib`, `router`, `groupfolder`, `auth`, `grants`, `ipc`, `runed`      |
| **Daemons**    | deployable processes            | `gated` (monolith) or `authd`+`routd`+`runed` (split); `webd`, `timed`, `slakd`, … |
| **Products**   | installable agents              | Slack team agent, reality agent, company brain                                     |

Per-primitive, the mapping is direct (each primitive has a home
package, bundled into the monolith today and into a split daemon as the
planes separate):

| Primitive     | Component (package)                                   | Daemon (monolith → split)    |
| ------------- | ----------------------------------------------------- | ---------------------------- |
| Event         | `store` (messages), `chanlib`/`chanreg`               | `gated` → `routd` + adapters |
| Routing       | `router`                                              | `gated` → `routd`            |
| Agent         | `groupfolder`, `store` (groups)                       | `gated` → `routd`            |
| Authorization | `auth`, `grants`                                      | `gated` → `authd`            |
| Turn          | `ipc` (MCP bridge), `runed` (container)               | `gated` → `runed`            |
| State         | `store` (DB) + the folder on disk                     | `gated` → `routd`/`runed`    |
| Identity      | `types/identity`, `auth/identity`, `store/identities` | `gated` → `authd`            |

Honest notes on the four layers (do not let this become a just-so
story):

- **The public docs collapse Components into Daemons.** Operators
  deploy daemons, not packages; the `components/` doc section lists
  daemons. The package layer is real and worth naming in the spec and
  README, but the operator-facing site keeps three layers
  (primitives → daemons → products), not four. Don't invent a docs
  section for packages.
- **The lived shape is the split.** `gated` is removed; the three planes
  (`authd`+`routd`+`runed`) are the only topology. Say "the split moves each
  plane into its own daemon," not "the monolith bundles every component."
- **A product is not a skin.** It earns the word "product" by carrying
  bounded execution (Turn), an ACL (Authorization), state separation
  (State + folder), and channel-native presence (Event sources) — not
  by swapping a prompt. Prove it with a trace, not a slogan.

## The grand message

Lead arizuko with the four-layer story; the surfaces (README,
`index.html`, the pitch) reuse this wording.

**Headline (evolved lead).** _Shape an agent system in plain language —
the LLM edits the files that are the system, compartmentalized daemons
keep it bounded, and you own every change._

**Headline (product-first alternative, prior lead).** _Ship ownable
agents as real products — each a folder that reacts to events through
one fixed pipeline._

**Elevator (3 sentences).** arizuko is an event→reaction engine you
shape through LLMs: something happens, it's routed to a folder-agent,
checked for permission, run for one bounded turn, and persisted in two
durable stores — the shared DB and the agent's folder. You author and
operate it by talking to an agent that edits those files (persona,
skills, routing), and the daemon compartments (`routd`/`runed`/`authd`,
each owning one primitive) keep that molding inside the grants — no
black box, no breakout. The wedge is that ownership and language meet:
deep customization is changing files you diff, fork, and `git revert` on
your own host, not tuning an opaque SaaS agent through a text box.

**One-pager reveal order** (use-cases first; primitives explain why the
surface stays small, they don't open):

1. Hero — "build real agents you own."
2. Three product tiles — Slack team agent, reality agent, company brain.
3. One traced example, top to bottom (below).
4. The six primitives — the reason the surface stays small.
5. The four-layer decomposition (the table above).
6. Ownership / customization proof — folder tree, files, `git diff`,
   self-hosted, one-tar backup.
7. Honest constraints — Linux + Docker + SQLite, the split
   (`authd`+`routd`+`runed`) is the shipped topology, the uniform
   MCP+REST surface still converging.

**The trace** — the one diagram that makes the model legible. One
concrete event, the six primitives in a fixed column, the reply out:

```
Slack @mention in #eng
   │
   ▼  Event        one inbox row (messages)
   ▼  Routing      → corp/eng/oncall
   ▼  Agent        folder loaded: persona, skills, memory
   ▼  Authorization can it read / send / delegate here?
   ▼  Turn         one ephemeral container run
   ▼  State        DB rows written + folder edits
   │
   ▼
Reply in the Slack thread
```

The key reveal: it looks like feature sprawl until you trace one
example top to bottom — then it's six steps every time.

**Build-real-products angle** (emotional + rational, never
framework-brochure):

- This is not a chatbot you rent — it is a product you own.
- Each product is a starter folder plus channel wiring on top of the
  same fixed reaction pipeline.
- The same engine ships a support agent, a team copilot, or a personal
  life agent because each is the same reaction loop with different
  folder contents and routing.
- Customization is not prompt-tweaking in a SaaS UI; it is changing
  files you own and can review.

Avoid: "extensible platform," "flexible architecture," "powerful
abstractions," "scalable," "seamless." True and weak. Name the concrete
thing instead (a table, a row, a file, a folder).

## Honesty notes

The docs rewrite must NOT reintroduce these false claims (a codex
review + a CTO audit flagged them):

- **Not strict orthogonality.** The primitives are layered and coupled.
  Routing depends on Event shape and is overridden by
  engagement/direct-address/sticky (`routd/loop.go:382`). Authorization
  is multi-gate (`auth/policy.go:18` + `auth/authorize.go:36`). A Turn
  needs grants/config/egress resolved first (`routd/dispatch.go:235`).
  Frame the wedge as "one job each, fixed pipeline, no special cases" —
  not independence.
- **Not "one SQLite is the only truth."** Two durable stores by design:
  the shared DB _and_ the per-agent folder (`README.md:28`,
  `store/store.go:19`). The folder is not a cache. The split adds
  per-plane DBs (`5/E`, `5/P`).
- **"One rule gates agent MCP and operator REST" is the TARGET, not
  shipped fact.** It rolls out resource by resource — say "converging
  on one MCP+REST surface." README "Direction" (`README.md:54`) notes
  the _first_ resource (`proxyd/resource.go`) already runs the pattern;
  the rest follow. Spec `5/5` is `status: partial`.
- **The grant primitive in code is the unified ACL, not just
  `grant_rules`.** `grant_rules`/`grants.DeriveRules` is the
  legacy/tier-default path; the unified model is the `acl` +
  `acl_membership` tables resolved by `auth.Authorize`
  (`store/migrations/0052-acl-unified.sql`, `../4/9-acl-unified.md`).
- **Workflow is operating discipline, not a shipped engine.** Describe
  the enforced opening (`5/X`) as a session-boundary rule on Turn +
  State; do not imply a separate workflow substrate.
- **The four-layer story is a description, not a claim of clean code.**
  The split shipped (`gated` removed), but the daemons don't cleanly
  own four planes: State spans `routd`+`runed`, Agent lives in `routd`.
  Don't imply a one-daemon-per-layer mapping.

## Docs-rewrite implications

**Landed 2026-06-13.** The evolved "shape through LLMs" lead is now the
primary lead on `index.html` and `README.md` (product/ownership become the
two halves it rests on, not competing leads); `concepts/workflows.html` is
created and slotted after `tasks` in the curriculum; the product pages name
their recomposition and link `concepts/primitives.html`. `concepts/primitives.html`

- the four-layer/no-special-cases tables already shipped earlier (v0.49.0).
  The site-wide `gated`→split daemon-name purge across `components/`/`reference/`/
  `howto/` is tracked separately (`bugs.md`).

What changed when this framing landed (the original to-do, kept for record):

- **`index.html` lead.** Open use-cases-first per the grand-message
  reveal order: hero → three product tiles → one traced example → the
  six primitives → the four-layer table → ownership proof → honest
  constraints.
- **`concepts/index.html` curriculum order (`5/D` § Concepts
  walkthrough).** Re-sequence so the primitive arc reads
  Event → Routing → Agent → Authorization → Turn → State, with
  identity threaded through; the per-platform/feature pages become
  "this is primitive X recomposed."
- **New `concepts/primitives.html`.** A single page holding the catalog
  - the "no special cases" table + the four-layer table + the trace
    diagram, as the conceptual spine the tour hangs off. Insert into the
    curriculum order and fix neighbouring pagers per `5/D` § Maintenance.
- **New `concepts/workflows.html`.** The session-governed-workflow
  discipline (`5/X`): free conversation + enforced opening +
  plan-of-record. Slot after Turn/State in the arc.
- **Products pages.** Each product opens by naming the recomposition —
  which folder contents + routing make it that product — and links to
  `concepts/primitives.html`. Keeps "a product is the same pipeline,
  different folder" visible (`16/9`, `16/R-products`).
- **README lead.** Product-first + the six-step trace + the four-layer
  table; shorten the current sprawl.
- **Reference pages unchanged in shape** (`5/D` ownership rule):
  concepts owns the narrative primitive framing; reference still owns
  exhaustive grammar/fields.
