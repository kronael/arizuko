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
answers: _given a real task, does the live agent know how to do it?_

## Approach

`anteval` is a **black-box capability prober**: inject a real task through
a public surface, let the live agent perform it with its own MCP tools,
then assert the **externally observable effect** — never the agent's
prose, never the instance's internal state.

- **Public surfaces only.** routd REST, proxyd HTTP (`/pub` `/priv`
  `/chat` `/hook`), the MCP face, and a harness-owned callback sink. If a
  capability can't be proven through a public surface, that is a _surface
  gap to fix_, not a licence to read internals. Zero
  `github.com/kronael/arizuko/*` imports (11/A) — `anteval` could eval a
  different platform behind a thin adapter. Reading the data dir is a
  `--debug` aid and **never gates a case**.
- **Callback sink, not prose.** The driver serves `POST /cb/{nonce}`; a
  case tells the agent to wire its artifact to hit `{sink}/cb/{nonce}`.
  Grading an HTTP hit carrying a unique token is deterministic; grading
  wording is not.
- **One case, one capability, one checker** — no workflow decomposition,
  no duplicate transport paths (a codex review 2026-06-07 collapsed an
  earlier matrix that had both).
- **Run-nonce idempotence.** Every run mints a nonce embedded in every
  folder, URL, body, child id, and token the cases create; checkers match
  only that nonce, so concurrent and repeated runs never collide. Teardown
  is best-effort, not correctness-critical.
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
against a **live deployed** instance — release gate, post-migrate smoke,
regression watch — not only in the build.

## Cases

Data, not spec: the case registry is `anteval/cases/anteval.toml`
(22 cases as of 2026-08-21, reduced from 35 — a passing case costs ~50s
live but a failing one burns its full wall cap, so the catalog's cost is
failure-dominated and same-effect/same-path duplicates,
want=404 shapes a total failure also satisfies, and cases whose success
arm is unreachable on the documented web:eval chat were deleted, each
with an in-place note; `smoke = true` tags the 9-case release-gate
basis). Do not mirror the case list here — it drifts the moment a case
lands.

Ten dimensions: self-modification · subagents + privileges · web
publishing · online chat apps · reach via REST + MCP · composites ·
routing · egress · the backend contract floor · continuity
(added 2026-08-20: the group-knowledge
layer must carry a fact to a transcript-less isolated turn — the
portability claim; what stays live-unmeasured and why is in
`anteval/README.md` §"Continuity coverage"), plus a
**fabricate-vs-surface** family added 2026-07-12
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

### A product carries its own cases (2026-08-15)

The harness already takes `--cases <dir>` and `spec.Load` reads every
`*.toml` under it, so **a product ships `eval/*.toml` and the same harness
runs it** — no second runner, no per-product mode. The convention is the
whole mechanism:

- the payload falls to the blend's catch-all, so `products apply` copies
  it into the group home verbatim;
- `anteval validate --cases <data-dir>/groups/<folder>/eval` checks schema
  and templates for free;
- `anteval run <target> --cases <data-dir>/groups/<folder>/eval` runs it
  against the live agent, and costs model credits like any other run.

**What a product case asserts is different in kind from the 35 above.**
Those certify the PLATFORM's capability — can an agent modify itself,
publish, reach REST. A product's case certifies that ITS persona, rules
and facts produce the behaviour its page promises: `rest_reply` with the
string the answer must contain is the usual shape. An agent that replies
politely and offers the refund has regressed even though every daemon is
healthy and all 35 platform cases pass.

Reference product: `ant/examples/helpdesk/eval/helpdesk.toml` (4 cases).
One of them — the triage skill being loaded — doubles as the live check
for `L37`, which is the intended pattern: a platform bug becomes visible
as a product behaviour failure rather than a file-placement detail.

Authoring guide (canonical for operators):
`template/web/pub/arizuko/howto/build-a-product.html` §"Ship tests with
it". Do not restate the case schema anywhere else — it drifts.

## What the first live run proved (krons, 2026-07-11, $4.78 / 23 turns)

7/8 of the then-`--smoke` basis passed. The 8th, `webhook-in`, FAILED —
**and that is the eval doing its job**: it caught a real krons bug, not an
agent-capability gap. The `/hook/` proxyd route was never seeded on the
instance (seeded-once drift) and live proxyd doesn't hot-reload the route
table, so `5/W` webhooks were dead there. A green health check missed it;
the capability prober did not. That single result is the spec's
justification.

Surface gaps the run exposed, kept honest rather than silent: with
`--mcp` unset, every parity check fails loudly ("surface not
configured") rather than passing on an unread surface. Configuring it is
anteval-side work — the platform's half is served. `HTTPTarget.McpMessages`
GETs a REST-shaped `/v1/messages/inspect`, so it can only be pointed at a
REST URL, while webd's MCP face answers `get_history` over streamable HTTP
on both `POST /mcp` and `POST /chat/{token}/mcp`.

## Live-run preconditions (learned the hard way)

- **Token**: `arizuko token <inst> issue bearer <folder> --scope
messages:write,messages:read` (add `cost:read` for budgets). Pass
  `--ttl 6h`: the default is one hour, and a full suite outlives it, so
  every remaining case 401s and the run reads as a total capability
  failure.
- **API base**: `--api http://localhost:<routd-port>`. The public host
  serves no `/v1`, so pointing at it 404s every injected message.
- **Eval folder**: `arizuko group <inst> add web:<folder> <folder>` —
  never a manual `mkdir`; `--chat web:<folder>` then routes 1:1. Creating
  it seeds five `/compact-memories` tasks that fire at once (BUGS `L312`),
  and each of those turns persists a root session, so the first run after
  the folder is created aborts on the baseline unless it drains first.
- **Subagent cases need a child that exists**: nothing auto-creates a
  group. Create the child with `register_group` (the eval folder must hold
  `mcp:register_group`) before a child-delegate case can pass.
- **Sink reachability**: bind `--sink-addr :PORT` on the eval host and
  pass the target's docker-gateway IP as `--sink http://<gateway-ip>:PORT`
  — reachable from containers even on internal networks. That gateway is
  the agent's OWN per-folder network (`docker network inspect
<inst>_<folder>`, e.g. `10.99.15.1`), never `docker0`'s `172.17.0.1`:
  a container on a user-defined network cannot reach the default bridge,
  so every callback case times out with an empty artifact URL and reads
  as an agent failure. crackbox
  name-matching skips IP entries, so a name-based allowlist rule cannot
  cover it (a folder `*` rule can). The eval host must sit on the target
  folder's egress allowlist, or a default-deny refusal reads as a
  capability failure.
- **Sequential cases share one chat worker**: a case that blows its wall
  budget leaves the agent mid-task and the backlog bleeds into the next
  case's window (routd serializes turns per chat). Per-case latencies are
  honest only when the prior turn finished; `--case` re-runs isolate
  cleanly.

## Session isolation

Each case injects into its own fresh topic, so each case gets its own
Claude session — sharing the chat's default topic made case 1's refusal
the context of cases 2–8 on the 2026-08-08 live run.

Per-case topics alone stopped being isolation the day topic forking was
repaired (BUGS L54): the first turn of any non-main topic forks the
folder's MAIN session (`5/F`), so a root session — prior runs, operator
chat — sits in front of every case. The driver therefore establishes a
pristine baseline itself, before EVERY case (`--folder` names the root):

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
observed cursor (`5/F` unchanged); the case's ambient window holds only
`is_observed=1` rows, which injected case prompts never are — so cases
cannot leak into each other through the observed window either.

`5/F`'s fork rule itself does not change for anteval — the eval measures
the platform as shipped; the harness owns its baseline.

## Component shape

`anteval/` is a sibling component (11/A), structured like `crackbox/`:
`cmd/anteval` (`run` | `validate` | `dash`), `pkg/spec` (TOML schema),
`pkg/check` (the closed vocabulary), `pkg/run` (driver + sink),
`pkg/report` (JSON + markdown), `cases/`, and a standalone
Dockerfile/Makefile. Cases are data, checkers are a small fixed
vocabulary, driver and report are thin. The dashboard is a subcommand over
report artifacts — split a daemon out only if a hosted always-on watch is
needed.

## Neighbors

The parity case directly checks the uniform MCP+REST invariant (`5/17`);
webhook/chat-link cases lean on `5/W`; privilege cases on `5/32`.
Distinct from the `eval` skill (operational health), `create-eval`
(generates a project eval skill), and `make test-e2e` (one in-tree round).
