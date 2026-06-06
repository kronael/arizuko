---
status: draft
depends: [F-topic-lineage, G-engagement, A-primitives-framing, ../4/P-personas]
---

# specs/5/X — session-governed workflows (conversation with an enforced opening ritual)

> Workflows are **operating discipline, not a seventh primitive** —
> [specs/5/A](A-primitives-framing.md) canonicalizes this. The opening
> ritual is Turn + State + the session lifecycle, not a workflow engine.
> This spec owns the mechanism; 5/A owns the framing. Still `draft`: the
> enforced-opening hardening (non-skippable directive, plan-of-record)
> is unshipped — do not describe it as live machinery in the docs.

A framing + minimal-mechanism spec. arizuko agents converse freely, but they
"fail hard" at anything multi-step — they improvise every turn from chat
scrollback, never produce a durable plan, never consult their own skills, and
restart cold each session. The fix is NOT more skills or a workflow engine: it
is making the **session/thread boundary** run a mandatory opening ritual —
load context, open a plan-of-record, apply the workflow's guidelines — _before_
the agent is allowed to free-associate. Conversation stays free; the _opening_
gets strict. The waiter remembers you and chats, but every time you sit down he
still runs the same script: greet → recall your table → take the order → read
it back → then act.

## Why — empirical (rhias + mayai)

Two live krons agents used as travel concierges (rhias/Telegram, mayai/WhatsApp)
were analysed across their full planning arcs. The failures are not capability
gaps — every needed skill and memory store exists — they are the absence of an
_enforced_ opening-and-closing ritual:

- **No plan-of-record.** The itinerary lived only in the transcript, so each
  turn re-derived it from scrollback. The same stop (Jardim do Torel) was
  added → questioned → removed → re-added across an hour; "✅ all done" was
  emitted, then edited for 4 more hours. Corrections thrash instead of
  converging because there is no single object to converge _on_.
- **No enforced opening.** The platform's triage skill `/resolve` is
  `user-invocable: false` and fires via a soft gateway _nudge the model skips
  at will_; `/hello` (the intro) effectively never triggers (5 matching
  messages across both agents' entire history). A bare "hello" to mayai got
  **no reply at all** for 5 hours.
- **Context not loaded at decision time.** mayai claimed "Nemám přístup k
  message history" / "nemám kontext" _mid-session_ despite active context;
  rhias had no `facts/` and no diary for the entire planning period, so every
  new session started blind. The persona _says_ "recover context before each
  session" — nothing makes it happen.
- **Own skills/rules not consulted.** mayai: _"itinerary skill neexistuje"_ →
  then _"Teď vidím! Měl jsem ho použít od začátku"_ (I should have used it from
  the start). The Google-Maps-vs-mapy.com rule — already written in the skill —
  was re-taught live 4×. Rules accrete in CLAUDE.md as scar tissue; nothing
  makes the agent _read and apply_ them at turn start.
- **Identity incoherence.** mayai carries three conflicting personas
  (slack-team-agent / Fiu / slack product) and a grab-bag `facts/`; both agents
  sign chat as "krons" (the instance), not their group. The "load your persona
  first" step has nothing coherent to load.

Root pattern, identical across two agents / two platforms / two languages:
**free-chat all the way down.** No machine-enforced gate runs a fixed opening
routine before the agent acts.

## Why — the field agrees (2026)

The reliable-agent literature converges on the same shape: a **deterministic
backbone** with intelligence deployed at specific steps, control always
returning to the backbone; **durable artifacts + structured state** over
fully free-form conversation; **enforced gates** (validation / step-enforcement
/ verification-aware planning) rather than prompt nudges. ("Most production
wins come from structured state + summaries + artifacts rather than
fully autonomous free-form conversation.") arizuko's primitives (sessions,
topics, skills, CLAUDE.md, the sysmsg-injection path) already supply the
backbone; what is missing is making the session boundary _use_ it.

## The model — the session/thread is the governance unit

arizuko already has the two units this needs (spec `5/F` topic-lineage,
`5/A` State):

- **Session** — a continuous run of turns under one Claude Code session id,
  keyed per `(folder, topic)`. A NEW session = the agent "sitting down fresh".
- **Topic / thread** — a forked conversation with inherited lineage.

Workflow strictness is a function of the boundary, not a separate engine:

| Boundary                                       | What runs                                                                                                                                                                                         |
| ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **New session** (cold / idle-expired / `/new`) | the FULL opening ritual: load persona+memory+facts, read the open plan-of-record for this topic, run the workflow's opening (greet, state guidelines, confirm the goal) — _before_ any free reply |
| **Continuation** (same session)                | free conversation, but every turn the agent edits the plan-of-record in place and re-reads its guidelines (cheap, from the injected envelope)                                                     |
| **New topic** (fork)                           | inherits parent lineage/context (`5/F`) + a _scoped_ opening (goal + guidelines for the new thread, parent tail as context)                                                                       |

Free conversation is the default _inside_ a session. The discipline is at the
_edges_ — exactly the shopkeeper/waiter pattern the operator described.

## Minimal mechanism (no workflow engine — reuse what exists)

Four changes, each small, each leveraging an existing primitive:

1. **Enforced opening, not a nudge.** Today `emitSystemEvents` injects a
   `new_session` system message (`5/A` Turn / gateway), and `/resolve` is a
   skippable prompt nudge. Change: on a new session/topic the gateway (routd
   post-cutover) injects the opening ritual as a **non-skippable system
   directive** at the head of the envelope — "before replying: load context,
   read/open `~/plans/<topic>.md`, apply the workflow below, confirm the
   goal." It is part of the prompt the model cannot not-see, not a tool it may
   forget to call. (The eval's single root cause is the skip; this removes it.)
2. **Plan-of-record artifact.** A convention: any multi-step task gets one file
   `~/plans/<topic>.md` (or `<group>/plans/`), written FIRST and edited IN
   PLACE. The opening ritual reads it; the closing ritual updates it; "done"
   means _the plan file is updated_, not a chat closer. This is the durable
   artifact the field calls for and the thing whose absence made both agents
   thrash. Lightweight — a markdown file the agent owns, no schema.
3. **Workflows encoded in CLAUDE.md, injected at the boundary.** The "standard
   workflow" for an agent (its opening script + plan discipline + the
   forced skill/rule scan) lives in the agent's `CLAUDE.md` (operating manual)
   — the natural home, already per-group. The platform's job is only to
   **surface the opening section into the session-start directive** so it is
   applied, not merely present. A `## Session opening` (or `## Workflow`)
   section in CLAUDE.md is the contract; the gateway lifts it into the
   `new_session` directive.
4. **Forced skill/context scan as a precondition.** The opening directive
   includes "scan `skills/` + read `MEMORY.md`/`facts/` relevant to the goal
   before acting" — making the dispatch/recall step a gate (the field's
   step-enforcement), not advice. "I don't have context" becomes impossible
   before this runs.

Nothing here is a state machine or a DAG runtime: the _backbone_ is the
session/topic lifecycle arizuko already runs; the _intelligence_ is the agent;
the new bit is a strict, enforced opening + a durable plan file.

## Advising users

Docs (a `concepts/workflows.html` + a `howto/`): teach operators to (a) write a
`## Session opening` workflow in their group's `CLAUDE.md` (greet → load context
→ confirm goal → name the deliverable), (b) keep the persona coherent (one
identity, a focused `facts/`), and (c) treat `~/plans/<topic>.md` as the source
of truth for any multi-step job. The platform enforces the _opening_; the
operator authors _what_ the opening says.

## Open decisions for sign-off

1. **Enforcement site.** Inject the opening directive in the gateway/routd
   prompt-build (`5/A` Turn) — agreed — but: hard-block (the agent literally
   cannot emit a non-plan reply on turn 1 of a session) vs strong-directive
   (injected, but the model still _could_ ignore it). Lean: strong-directive
   first (no new failure mode), measure skip-rate, harden only if it persists.
2. **Plan-of-record location + scope.** `~/plans/<topic>.md` per topic vs one
   `PLAN.md` per group vs a DB-backed plan row. Lean: a file per topic (cheap,
   diffable, agent-owned, no schema) — matches agent-as-data (`5/A`).
3. **Where the workflow text lives.** `CLAUDE.md ## Session opening` (per
   group, operator-authored) vs a platform default skill. Lean: CLAUDE.md as
   the contract + a sane platform default the operator overrides.
4. **Strictness knob.** Is "strict opening" always-on, or per-topic-kind
   (`5/F` task vs default thread → task topics get the full ritual, casual
   threads get a light one)? Lean: per-topic-kind, defaulting strict for
   `task` topics.

## Acceptance

- A new session/topic for a `task`-kind topic injects the opening directive +
  reads the plan-of-record before the first reply (test: the first turn's
  envelope contains the directive + plan contents).
- A multi-step task produces/edits exactly one `~/plans/<topic>.md`; "done"
  asserts the plan is updated (no more transcript-only plans).
- The opening directive includes the skill/memory scan; a turn that needed a
  known rule applies it without being re-taught (the rhias/mayai regression).
- Casual continuation turns stay free-form (no ritual overhead mid-session).
- Operator can author the opening in `CLAUDE.md ## Session opening` and see it
  reflected in the session-start envelope.
