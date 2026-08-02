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
(34 cases as of 2026-08-02; `smoke = true` tags the release-gate basis).
Do not mirror the case list here — it drifts the moment a case lands.

Eight dimensions: self-modification · subagents + privileges · web
publishing · online chat apps · reach via REST + MCP · composites ·
routing · egress, plus a **fabricate-vs-surface** family added 2026-07-12
(evidence-ranked from ~50 candidates mined from cross-project memory, live
`journalctl`, and doc anti-patterns) where the agent gives a confident
wrong answer instead of surfacing a real failure.

The composites are the headline the suite certifies: _the agent builds an
online app with chats, publishes it, and makes it reachable over REST and
MCP — for a child agent it created and privileged._ The
privilege-gate/revoke pair is the actual privilege-mediation boundary —
behavior, not a row mutation.

Scoring is **binary per case** (effect present or absent), aggregated to
pass-rate per dimension. Partial credit is additive and out of scope for
the gate.

## What the first live run proved (krons, 2026-07-11, $4.78 / 23 turns)

7/8 of the then-`--smoke` basis passed. The 8th, `webhook-in`, FAILED —
**and that is the eval doing its job**: it caught a real krons bug, not an
agent-capability gap. The `/hook/` proxyd route was never seeded on the
instance (seeded-once drift) and live proxyd doesn't hot-reload the route
table, so `5/W` webhooks were dead there. A green health check missed it;
the capability prober did not. That single result is the spec's
justification.

Surface gaps the run exposed, kept honest rather than silent: with
`--mcp` unset, `rest-mcp-parity` fails loudly ("surface not configured")
because the chat-token MCP face lacks an inspect-read.

## Live-run preconditions (learned the hard way)

- **Token**: `arizuko token <inst> issue bearer <folder> --scope
messages:write,messages:read` (add `cost:read` for budgets).
- **Eval folder**: `arizuko group <inst> add web:<folder> <folder>` —
  never a manual `mkdir`; `--chat web:<folder>` then routes 1:1.
- **Subagent cases need child capacity**: `MaxChildren` defaults to 0, so
  child-delegate/priv-grant fail with "spawning disabled" until an
  operator raises it.
- **Sink reachability**: bind `--sink-addr :PORT` on the eval host and
  pass the target's docker-gateway IP as `--sink http://<gateway-ip>:PORT`
  — reachable from containers even on internal networks. crackbox
  name-matching skips IP entries, so a name-based allowlist rule cannot
  cover it (a folder `*` rule can). The eval host must sit on the target
  folder's egress allowlist, or a default-deny refusal reads as a
  capability failure.
- **Sequential cases share one agent session**: a case that blows its wall
  budget leaves the agent mid-task and the backlog bleeds into the next
  case's window. Per-case latencies are honest only when the prior turn
  finished; `--case` re-runs isolate cleanly.

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
