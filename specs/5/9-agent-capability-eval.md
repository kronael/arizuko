---
status: shipped
---

# 5/9 — agent-capability eval (`anteval`)

## Problem

arizuko's value is that the in-container agent can operate the platform
_itself_ — modify its own skills, spawn and privilege child agents,
publish web, stand up chat apps, and wire those apps to chats over REST
and MCP. When the agent silently fails to understand one of those
surfaces, the capability regresses with **no signal**: health checks stay
green, tests pass, and the gap surfaces only when a user hits it.

Nothing else measures agent _capability_. The `eval` skill checks daemon
**operational health**; `create-eval` generates a project's test-criteria
**skill**; `make test-e2e` drives **one** round through webd in-tree. None
answers: _given a real task, does a running agent know how to do it?_

## Approach

`anteval` is a **black-box capability prober**: inject a real task through
a public surface, let a running agent perform it with its own MCP tools,
then assert the **externally observable effect** — never the agent's
prose, never the instance's internal state.

- **Public surfaces only.** routd REST, proxyd HTTP (`/pub` `/priv`
  `/chat` `/hook`), the MCP face, and a harness-owned callback sink. If a
  capability can't be proven through a public surface, that is a _surface
  gap to fix_, not a licence to read internals. The cases, checkers and
  driver import no daemon and no store package; `expand`, the template
  expander, is the one package they take from the tree.
- **Callback sink, not prose.** The driver serves `POST /cb/{nonce}`; a
  case tells the agent to wire its artifact to hit `{sink}/cb/{nonce}`.
  Grading an HTTP hit carrying a unique token is deterministic; grading
  wording is not.
- **One case, one capability, one checker** — no workflow decomposition,
  no duplicate transport paths (a codex review 2026-06-07 collapsed an
  earlier matrix that had both).
- **The run owns its instance.** `anteval run` creates a fresh instance
  with the `arizuko` CLI, runs the cases against it and removes it; an
  existing instance is refused, and a failed removal fails the run
  (`specs/6/50-anteval-ephemeral-instance.md`). It never runs against a
  live instance: its cases register groups, mint tokens and publish pages,
  and on a live instance nothing removed them (BUGS `L72`).
- **Run-nonce idempotence.** Every run mints a nonce embedded in every
  folder, URL, body, child id, and token the cases create; checkers match
  only that nonce, so the cases of one run never collide.
- **Cost is a budget, not a readout.** Each case declares `max_wall_ms`
  and `max_tokens` (overspend fails the case, read from routd cost via
  REST). Turn counts aren't exposed black-box, so there is no `max_turns`.
- **The checker vocabulary is CLOSED.** `http_status`, `callback`,
  `rest_reply`, `rest_observe`, `mcp_roundtrip`, `parity_sentinel` — no
  new kinds, no modifiers. Negation and diagnosis are pushed into the
  AGENT, which self-reports by curling the CORRECT marker only: right →
  `{sink}/cb/{nonce}` (the checker matches), wrong →
  `{sink}/cb/{nonce}-<x>` (a key the checker never sees → timeout →
  fail). The LLM reasons; the sink judges; the harness stays dumb.

**WHY a shipped component, not a `go test`:** capability must be checkable
on a running instance of a release, with real daemons, a real guest and a
real model, and a `go test` builds none of them. The owner starts a run by
hand; `anteval/README.md` § Run holds the command and what the env file
supplies.

## Cases

Data, not spec: the case registry is `anteval/cases/anteval.toml`. A
passing case costs about 50 s and a failing one burns its full wall cap, so
the catalog's cost is failure-dominated; same-effect and same-path
duplicates, want=404 shapes a total failure also satisfies, and cases whose
success arm is unreachable on the eval chat were deleted, each with a note
in place. `smoke = true` tags the shorter suite. Do not mirror the case
list or its count here — it drifts the moment a case lands.

Ten dimensions: self-modification · subagents + privileges · web
publishing · online chat apps · reach via REST + MCP · composites ·
routing · egress · the backend contract floor · continuity
(the group diary, read on demand, must carry a fact to a transcript-less
isolated turn — the portability claim; what stays unmeasured and why is in
`anteval/README.md` §"Continuity coverage"), plus a
**fabricate-vs-surface** family
(evidence-ranked from ~50 candidates mined from cross-project memory, live
`journalctl`, and doc anti-patterns) where the agent gives a confident
wrong answer instead of surfacing a real failure.

The composite is the headline the suite certifies: _the agent creates and
privileges a child, and the child publishes an online app with a live
chat entrypoint reachable from the public web._ The
privilege-gate pair (grant → 200, deny → 404) is the actual
privilege-mediation boundary — behavior, not a row mutation; the revoke
twin was deleted 2026-08-21 as the same gate answer (a revoked and a
never-granted folder both end at "no acl row").

Scoring is **binary per case** (effect present or absent), aggregated to
pass-rate per dimension. Partial credit is additive and out of scope for
the gate.

### A product carries its own cases

The harness takes `--cases <dir>` and `spec.Load` reads every `*.toml`
under it, so **a product ships `eval/*.toml` and the same harness runs
it** — no second runner, no per-product mode:

- `anteval validate --cases <checkout>/packages/<name>/eval` checks schema
  and templates for free;
- `anteval run --product <checkout>/packages/<name> --cases
<checkout>/packages/<name>/eval` installs the product for the eval group
  of the instance the run creates and runs the suite; it costs model
  credits like any other run.

**What a product case asserts is different in kind from the platform
catalog.** The catalog certifies the PLATFORM's capability — can an agent
modify itself, publish, reach REST. A product's case certifies that ITS
persona, rules and facts produce the behaviour its page promises:
`rest_reply` with the string the answer must contain is the usual shape.
An agent that replies politely and offers the refund has regressed even
though every daemon is healthy and every platform case passes.

Reference product: `packages/helpdesk/eval/helpdesk.toml` (4 cases).
One of them — the triage skill being loaded — doubles as the check that a
product's skill reaches the agent, which is the intended pattern: a platform
bug becomes visible as a product behaviour failure rather than a
file-placement detail.

Authoring guide for operators:
`template/web/pub/arizuko/howto/build-a-product.html` §"Ship tests with the workspace".
The case schema is `anteval/README.md` §"How a case proves itself"; do not
restate it anywhere else — it drifts.

## What the first run proved (krons, 2026-07-11, $4.78 / 23 turns)

7/8 of the then-`--smoke` basis passed. The 8th, `webhook-in`, FAILED —
**and that is the eval doing its job**: it caught a real krons bug, not an
agent-capability gap. The `/hook/` proxyd route was never seeded on the
instance (seeded-once drift) and live proxyd doesn't hot-reload the route
table, so `ROUTING.md` webhooks were dead there. A green health check missed it;
the capability prober did not. That single result is the spec's
justification. The run was on a live instance, which the driver then
allowed. The runs on an instance the driver creates are in
`specs/6/50-anteval-ephemeral-instance.md` §"Measured on a disposable instance".

## One chat worker, in sequence

routd serializes turns per chat and every case shares the eval chat. A
case that blows its wall budget leaves the agent mid-task and the backlog
bleeds into the next case's window, so the driver waits for the model
gateway to meter a turn before it injects the next case, and
per-case latencies are honest only when the prior turn finished.
`--case` reruns one case alone.

## Session isolation

Each case injects into its own fresh topic, so each case gets its own
session — sharing the chat's default topic made case 1's refusal
the context of cases 2–8 on the 2026-08-08 run.

Per-case topics alone stopped being isolation the day topic forking was
repaired: the first turn of any non-main topic forks the
folder's MAIN session (`ROUTING.md`), so a root session sits in front of
every case. The driver therefore establishes a
pristine baseline itself, before EVERY case:

1. Read `GET /v1/topic_sessions?folder=<folder>&topic=`. Empty → inject
   the case.
2. Otherwise send a bare `/new` to the chat (steer-consumed: deletes the
   root sessions row, spawns no turn) and poll until the root session is
   observably gone. Only then inject — steer consumes `/new` only as the
   newest pending message, so injecting a case early can turn the `/new`
   into literal trigger text on a root turn, re-creating the session.
3. A root session that will not clear aborts the run loudly; a run
   against a contaminated baseline scores the history, not the agent.

Per-case (not per-run) because a group-context scheduled follow-up fires
topic-less on the ROOT topic and re-persists a session there mid-run.
With main sessionless, a case topic's fork copies nothing and clears its
observed cursor (`ROUTING.md` unchanged); the case's ambient window holds only
`is_observed=1` rows, which injected case prompts never are — so cases
cannot leak into each other through the observed window either.

`ROUTING.md`'s fork rule itself does not change for anteval — the eval measures
the platform as shipped; the harness owns its baseline.

## Component shape

`anteval/` is a sibling component:
`cmd/anteval` (`run` | `validate` | `dash`), `pkg/spec` (TOML schema),
`pkg/check` (the closed vocabulary), `pkg/run` (driver + sink),
`pkg/report` (JSON + markdown), `cases/`, `backends/` (the per-backend
suite), and a standalone Dockerfile/Makefile. Cases are data, checkers are
a small fixed vocabulary, driver and report are thin. The dashboard is a
subcommand over report artifacts — split a daemon out only if a hosted
always-on watch is needed.

## Neighbors

The parity case directly checks the uniform MCP+REST invariant (`resreg/README.md`);
webhook/chat-link cases lean on `ROUTING.md`; privilege cases on `auth/README.md`.
Distinct from the `eval` skill (operational health), `create-eval`
(generates a project eval skill), and `make test-e2e` (one in-tree round).
