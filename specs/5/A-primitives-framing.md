---
status: defected
depends:
  [
    17-openapi-mcp,
    D-docs-refs-redesign,
    Q-unified-routing,
    E-routd,
    P-runed,
    32-acl-unified,
    33-paths-roles,
    ../17/9-positioning,
  ]
defects: [F16]
---

# specs/5/A — primitive framing (how to explain arizuko)

A FRAMING spec: it changes no behavior. It fixes the vocabulary the
README, `index.html`, and the docs site all reuse, so every surface names
the same small model instead of the daemon zoo. Mechanism lives in the
spec each primitive links to — this file must not restate it.

## Decisions (signed off)

1. **Six pipeline primitives + Identity as a coordinate system.** The
   pipeline is Event → Routing → Agent → Authorization → Turn → State.
   **Identity** is the coordinate system those six are addressed in
   (subs, JIDs, paths, roles, scopes), not a seventh stage: every stage
   references it, none sequences it. [`5/33`](33-paths-roles.md) sharpens
   the split — the `path` is location (where you are), the `role` is
   capability (what you may do), and they never leak into each other.
   The old `tier` (capability derived from path depth) was exactly that
   leak and is **removed** (`auth.Identity` is `{Folder, IsRoot}`,
   `auth/identity.go`; `types/identity.go` carries `UserSub`/`Folder`/
   `Scope` and no tier).
2. **Route-first order.** The runtime starts from "something happened,
   where does it go?", so Routing precedes Agent. Docs follow the
   runtime, not the org chart.
3. **"One job each, fixed pipeline, layered overrides — no special
   cases"** — never mathematical independence (⊥). Routing reads the
   Event's shape and is overridden by engagement/direct-address/sticky
   (`routd/loop.go` `resolve`); a Turn needs grants + container config +
   egress resolved before spawn (`routd/dispatch.go` `dispatchRun`).
4. **Reaction is the loop; Turn is the stage.** "Reaction" names the
   whole event→reaction loop; the runtime stage that runs the agent is
   **Turn**, so loop and stage names don't collide.
5. **Workflow is not a seventh primitive.** It is an operating discipline
   at the session boundary — Turn + State + the session lifecycle, built
   on the `new_session` system message routd already injects
   (`routd/dispatch.go:163`). Naming it a primitive would imply a
   workflow engine that does not exist. <!-- UNVERIFIED: the former
   spec 5/X is gone (its modality half landed in 5/Y); the shipped
   surface is concepts/workflows.html and no spec owns the discipline. -->

## Thesis

arizuko is an **event→reaction engine**: something happens, an agent
reacts. The apparent feature sprawl (channels, topics, tasks, webhooks,
secrets, egress, delegation, observe) is _recomposition_ of six
primitives, never new machinery.

Two halves carry the pitch:

- **Ownership.** A product is a folder of files you diff, review, fork,
  and back up with one `tar` on your own host — not a SaaS agent you tune
  through a text box (`../17/9`). The platform does not commit for you
  (make-it-true tracked in `../9/3-git-as-truth.md`).
- **Language, not config.** You shape the system by talking to an LLM
  that edits the files that ARE the system. What keeps that safe is the
  daemon structure — `routd` routes and runs the authz gate, `runed`
  executes, `authd` issues identity — so LLM-driven shaping stays inside
  the ACL rows.

**The platform bet** (user-locked 2026-07-14): hosting **many
specialized agents on one host**. Specialization is what lets an agent
hold a job a general blob can't; the platform's work is making it cheap
and coexistence safe:

- **Context isolation that keeps memory.** Each agent is a folder with
  its own persona/skills/memory, run in per-turn ephemeral containers;
  with crackbox, egress is allowlist-only. Another folder's home is never
  mounted in (shared world dirs are an explicit operator choice).
- **Hand-offs through the org tree, not a mesh.** Three named mechanisms,
  each authorized by `auth.Authorize`: `delegate_group` parent→child
  (`ipc/ipc.go:1828`), `escalate_group` child→parent (`ipc/ipc.go:1779`),
  both depth-capped at 1, and read-only `observe_group` (`5/F`). Never
  "any agent can message any agent."
- **Users move between agents.** A standalone exact `@<folder>` pins the
  whole chat to that agent; bare `@` restores default routing
  (`routd/steer.go`).

The primitives are invariant across topologies: the split
(`authd`+`routd`+`runed`) reorganised the _daemons_, not the model. So
the docs **name primitives, not daemons**.

## The primitives

| #   | Primitive         | Owns                        | Anchor                                                    | Recomposed as (not new primitives)                                          |
| --- | ----------------- | --------------------------- | --------------------------------------------------------- | --------------------------------------------------------------------------- |
| 1   | **Event**         | the one inbox               | `core.Message` (`core/types.go:16`), `store.PutMessage`   | channel adapters, `emaid`, `/hook/<token>`, `timed` ticks, `davd`           |
| 2   | **Routing**       | event → folder              | `router.ResolveRoute` (`router/router.go:356`), `5/Q`     | direct-address, engagement, sticky, observe, delegate, escalate             |
| 3   | **Agent**         | who reacts, with what       | folder-as-data (`README.md:28`), `groupfolder`, `store`   | personas, skills, memory, sub-teams, **products**                           |
| 4   | **Authorization** | may this principal, here    | `auth.Authorize` (`auth/authorize.go:25`), `5/32`, `5/33` | grants UI, scope vocabulary, delegation limits                              |
| 5   | **Turn**          | the bounded reaction        | `runed` container (`runed/docker.go`), `5/P`              | tasks, autocalls, interjections                                             |
| 6   | **State**         | what survives the container | `store.Store` + the folder on disk, `5/E`/`5/P`           | secrets, egress allowlists, sessions                                        |
|     | _Identity_        | the coordinate system       | `auth/identity.go`, `types/identity.go`, `authd/store.go` | subs, JIDs, paths, roles, scopes — referenced by all six, sequenced by none |

Two honest notes the docs must carry:

- **Authorization is ONE gate, not a stack.** `auth.Authorize` is the
  sole runtime evaluator: row-based ACL over the caller's expanded
  principal set, deny-wins, magnitude AND containment in one call. There
  is no structural pre-gate and no tier fallback — `AuthorizeStructural`,
  `grants.DeriveRules`, and the `grants/` package are deleted
  (`auth/authorize_test.go` `TestAuthorizeWith_NoTierFallback` pins it).
  Rows live in `routd.db` (`routd/migrations/0007-acl.sql`), with
  identity indirection via `acl_membership`.
- **State is two durable stores by design**, not a cache layer: the
  per-plane DBs (`routd.db` events/routes/ACL/sessions, `auth.db`,
  `runed.db`, `onbod.db`) AND the per-agent folder on disk. The folder is
  not a cache; containers are stateless and mount it per turn.

## No special cases

The proof the surface is small — every "feature" is a row here, and none
introduces machinery absent from the six primitives.

| Apparent feature                             | Is really…                                              | Primitive            |
| -------------------------------------------- | ------------------------------------------------------- | -------------------- |
| Channel adapters / webhooks / email / WebDAV | event sources writing `messages` rows                   | Event                |
| Scheduled tasks                              | Events on a `timed` tick → a Turn                       | Event + Turn         |
| Autocalls / interjection                     | Turns triggered by a non-chat Event                     | Turn                 |
| Topics                                       | a scoping field on the Event + a Routing layer (sticky) | Event + Routing      |
| Engagement                                   | a Routing override keyed on `(jid, topic)`              | Routing              |
| Observe                                      | a Routing mode: silent ingest, no Turn                  | Routing              |
| Delegation / escalation                      | Routing to a child/parent folder                        | Routing + Agent      |
| Personas / skills / memory                   | folder contents                                         | Agent (folder-data)  |
| Workflows                                    | an enforced opening at the session boundary             | Turn + State         |
| Grants / roles / scopes                      | views over the one `Authorize` question                 | Authorization        |
| Secrets / egress allowlist                   | per-folder State resolved before spawn                  | State + Turn         |
| A product                                    | a folder with persona + skills + routes                 | Agent (recomposed)   |
| Dashboard / CLI                              | a second face over the same handlers (`5/17`)           | (surface, not prim.) |

## The four layers

Primitives become packages become processes become products.

| Layer          | What it is                      | Examples                                                                          |
| -------------- | ------------------------------- | --------------------------------------------------------------------------------- |
| **Primitives** | invariant concepts              | Event, Routing, Agent, Authorization, Turn, State (+ Identity)                    |
| **Components** | Go packages that implement them | `store`, `chanlib`, `router`, `groupfolder`, `auth`, `resreg`, `ipc`, `container` |
| **Daemons**    | deployable processes            | `authd`+`routd`+`runed` (the only topology); `webd`, `timed`, `slakd`, …          |
| **Products**   | installable agents              | Slack team agent, reality agent, company brain                                    |

Guardrails on the layer story:

- **The public docs collapse Components into Daemons** — operators deploy
  daemons, not packages. The site keeps three layers; don't invent a
  `components/` section for Go packages.
- **It is a description, not a claim of clean code.** The daemons don't
  own one plane each: State spans `routd`+`runed`, Agent lives in
  `routd`. Don't imply a one-daemon-per-layer mapping.
- **A product is not a skin.** It earns the word by carrying bounded
  execution (Turn), an ACL (Authorization), state separation, and
  channel-native presence — not by swapping a prompt.

## The grand message

**Headline.** _Shape an agent system in plain language — the LLM edits
the files that are the system, compartmentalized daemons keep it
bounded, and you own every change._

**Elevator.** arizuko is an event→reaction engine you shape through LLMs:
something happens, it's routed to a folder-agent, checked for permission,
run for one bounded turn, and persisted in two durable stores — the DB
and the agent's folder. You author and operate it by talking to an agent
that edits those files, and the daemon compartments
(`routd`/`runed`/`authd`) keep that molding inside the ACL. The wedge is
that ownership and language meet: deep customization is changing files
you diff, fork, and `git revert` on your own host.

**The trace** — the one diagram that makes the model legible:

```
Slack @mention in #eng
   ▼  Event         one inbox row (messages)
   ▼  Routing       → corp/eng/oncall
   ▼  Agent         folder loaded: persona, skills, memory
   ▼  Authorization may this principal read / send / delegate here?
   ▼  Turn          one ephemeral container run
   ▼  State         DB rows written + folder edits
   ▼
Reply in the Slack thread
```

It looks like feature sprawl until you trace one example top to bottom —
then it's six steps every time.

**Voice.** Avoid "extensible platform," "flexible architecture," "powerful
abstractions," "scalable," "seamless." True and weak. Name the concrete
thing — a table, a row, a file, a folder.

**The layer above the model.** Models and harnesses both get better;
neither is the race arizuko runs. The gap is the layer above them —
managing users, context, and organization across many agents over time —
and the bet is that layer wants composable primitives you own rather than
one system you rent. A web-native Linux: identity (folder/JID), storage
(the folder tree), routing (the route table), publishing
(`~/public_html` → `/pub`, no deploy step). Swap the agent tomorrow; the
folder, its history, and its grants stay.

## Honesty guardrails

Claims the docs must NOT reintroduce:

- **Not strict orthogonality** — the primitives are layered and coupled
  (decision 3).
- **Not "one SQLite is the only truth"** — two durable stores, plus
  per-plane DBs after the split.
- **Not "tiers"** — capability comes from ACL rows, never from path
  depth (`5/33`). Any doc saying "tier 0/1/2/3" is stale.
- **"One rule gates agent MCP and operator REST" is the target**, rolling
  out resource by resource — say "converging on one MCP+REST surface"
  (`5/17`, rollout `5/16`).
- **Workflow is operating discipline, not a shipped engine** (decision 5).
- **Owner is a convention, not an enforced boundary** — every core daemon
  mounts the whole data dir rw as UID 1000 (`compose/compose.go`), so
  per-plane DB ownership is discipline, not isolation.

Docs consequences landed 2026-06-13 (`index.html` lead,
`concepts/primitives.html`, `concepts/workflows.html`, product pages
naming their recomposition); the standing trigger → page map lives in
`template/web/CLAUDE.md` § Maintenance, not here.
