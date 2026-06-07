---
status: draft
---

# 5/37 — agent-capability eval (`agenteval`)

## Problem

arizuko's value is that the in-container agent can operate the platform
_itself_ — modify its own skills, spawn child agents and grant them
privileges, publish web, stand up online chat apps, and wire those apps
to chats over REST and MCP. When the agent silently fails to understand
one of those surfaces (the "atlas can't tell trigger from observe"
class — see `self/chat-routing.md`, migration 157), the capability
regresses with no signal: health checks stay green, tests pass, and the
gap surfaces only when a user hits it.

Nothing measures agent _capability_. The `eval` skill checks daemon
**operational health** (cursors, containers, sockets). `create-eval`
generates a project's test-criteria **skill**. `make test-e2e` drives
**one** round through webd in-tree. None answer the real question:
_given a real task, does the live agent know how to do it?_

## Approach

`agenteval` is a **black-box capability prober**. For each case it
injects a real task through a public surface, lets the live agent
perform it with its own MCP tools, then asserts the **externally
observable effect** — never the agent's prose, never the instance's
internal state.

- **Public surfaces only.** Assertions use routd REST (inject, read
  chats, cost), proxyd HTTP (`/pub` `/priv` `/chat` `/hook`), the MCP
  face, and a harness-owned callback sink (below). If a capability
  cannot be proven through a public surface, that is a _surface gap to
  fix_, not a reason to inspect internals. Reading the instance data
  dir (fs/sqlite) is a `--debug` aid only and **never gates a case**.
  Zero `github.com/kronael/arizuko/*` imports (11/A) — `agenteval`
  could eval a different agent platform behind a thin surface adapter.
- **Callback sink, not prose.** The driver serves `POST /cb/{nonce}`.
  A case that must prove "the agent built something that works" tells
  the agent to wire its artifact — a created skill, an added MCP tool,
  a published app — to hit `{sink}/cb/{nonce}`. The checker waits for
  that callback. Grading an HTTP hit carrying a unique token is
  deterministic; grading wording is not.
- **One case, one capability.** Each case proves a single distinct
  capability via a single checker — no workflow decomposition, no
  duplicate transport paths. (A codex review 2026-06-07 collapsed an
  earlier matrix that had both.)
- **Run-nonce idempotence.** Every run mints a nonce embedded in every
  folder name, URL, message body, child id, and token the cases create;
  checkers match only that nonce, so concurrent and repeated runs never
  collide. Teardown is best-effort, not correctness-critical.
- **Cost as a budget, not a readout.** Each case declares `max_tokens`,
  `max_turns`, `max_wall_ms`; the driver aborts and fails a case that
  breaches them, and the report totals real spend (routd cost via REST).
  `--smoke` runs the tagged minimal basis; full runs all.

WHY a shipped component, not a `go test`: capability must be checkable
against a **live deployed** instance — release gate, post-migrate smoke,
or a regression watch with a dashboard — not only in the build. It ships
in arizuko's suite (11/A) and runs standalone.

## Cases

~19 cases over six dimensions; each proves one capability through a
public-surface checker. `★` marks the `--smoke` basis (the gate). All
identifiers carry the run nonce.

| #                          | Case                 | Task given to the agent                                                                                                                | Checker (public surface)                                                    |
| -------------------------- | -------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| **Self-modification**      |                      |                                                                                                                                        |                                                                             |
| ★                          | self-skill           | create a skill that, when invoked, calls `{sink}/cb/{nonce}`                                                                           | callback received                                                           |
|                            | self-mcp             | add an MCP server and call a tool that emits `{nonce}` to the sink                                                                     | callback received                                                           |
| **Subagents + privileges** |                      |                                                                                                                                        |                                                                             |
| ★                          | child-delegate       | create a child group, delegate a `{nonce}` task                                                                                        | child-owned reply carrying `{nonce}` readable via REST                      |
| ★                          | privilege-gate       | child attempts a privileged action (publish) → expect fail; grant; retry                                                               | URL 404 before grant, 200 after                                             |
|                            | privilege-revoke     | revoke that grant; child retries the action                                                                                            | URL 200→404 (privilege withdrawn)                                           |
|                            | child-identity       | child publishes a `{nonce}` page                                                                                                       | `GET /pub/<child-folder>/…` == 200 (subagent identity in the path)          |
| **Web publishing**         |                      |                                                                                                                                        |                                                                             |
| ★                          | pub-200              | publish a public page at a `{nonce}` URL                                                                                               | `GET /pub/…/{nonce}` == 200                                                 |
| ★                          | priv-401             | publish a gated page at a `{nonce}` URL                                                                                                | `GET /priv/…/{nonce}` == 401 unauth (gate engaged)                          |
|                            | unpublish-404        | delete a previously published page                                                                                                     | URL 200→404 (publish lifecycle)                                             |
|                            | web-route            | add a web route for a `{nonce}` path                                                                                                   | path resolves per the rule (200 / redirect / 401)                           |
| **Online chat apps**       |                      |                                                                                                                                        |                                                                             |
| ★                          | chat-entrypoint      | publish a page embedding a chat link (`issue_chat_link`)                                                                               | page 200 AND `/chat/{token}` 200 AND a message through it reaches the agent |
|                            | app-to-chat          | publish an app that on submit posts `{nonce}` into a chat                                                                              | turn carrying `{nonce}` observed in the chat via REST                       |
|                            | chatlink-revoke      | revoke a chat link                                                                                                                     | `/chat/{token}` 200→404/403                                                 |
| **Reach via REST + MCP**   |                      |                                                                                                                                        |                                                                             |
| ★                          | webhook-in           | create a webhook (`issue_webhook`); send `{nonce}` payload                                                                             | `POST /hook/{token}` produces a turn carrying `{nonce}` (REST read)         |
| ★                          | rest-roundtrip       | post `{nonce}` via the REST chat surface                                                                                               | agent reply readable via REST                                               |
| ★                          | mcp-roundtrip        | external MCP client posts `{nonce}` into a chat and reads it back                                                                      | post + read both succeed over MCP                                           |
|                            | rest-mcp-parity      | harness writes a sentinel turn                                                                                                         | REST and MCP return the same canonical subset (chat id, message id, body)   |
| **Composite**              |                      |                                                                                                                                        |                                                                             |
|                            | spawn-publish-reach  | create + grant-web a child; child publishes an app with a chat entrypoint; reach the **child** from public web into a chat; it replies | full chain green, public-surface only                                       |
|                            | product-rest-and-mcp | stand up one online app + chat reachable via a public link **and** REST **and** MCP                                                    | all three reach `{nonce}`                                                   |

The composites are the headline the suite certifies: _the agent builds
an online app with chats, publishes it, and makes it reachable over REST
and MCP — for a child agent it created and privileged._ `privilege-gate`

- `privilege-revoke` are the actual privilege-mediation boundary
  (behavior, not a row mutation).

## Harness

Per case: mint nonce → start the callback sink → inject the prompt via
the case's surface → await `round_done`/reply or budget breach → run the
checker → record `{pass, latency_ms, tokens, reason}`. Emit a report
(JSON + markdown); non-zero exit on any fail. Selectors: `--smoke`,
`--dimension`, `--case`.

The sink must be reachable from the target's agent containers, so the
eval host has to sit on the target folder's crackbox egress allowlist
(or run on an already-allowed host) — a deploy precondition, noted so a
default-deny refusal isn't misread as a capability failure.

Scoring is **binary per case** (effect present or absent), aggregated to
pass-rate per dimension. A partial-credit layer (attempted vs achieved)
is additive and out of scope for the gate.

## Component shape

`agenteval/` is a sibling component (11/A), structured like `crackbox/`:

- `agenteval/cmd/agenteval/main.go` — `run <target>` | `dash`
- `agenteval/pkg/spec` — TOML case schema + loader
- `agenteval/pkg/check` — checker vocabulary: `http_status`,
  `callback`, `rest_reply`, `rest_observe`, `mcp_roundtrip`,
  `parity_sentinel`
- `agenteval/pkg/run` — driver; hosts the callback sink; nonce → inject
  → await → check → record
- `agenteval/pkg/report` — JSON + markdown/HTML render (the `dash`
  subcommand reads these artifacts; no daemon)
- `agenteval/cases/*.toml` — the ~19 cases
- `agenteval/{Dockerfile,Makefile,README.md}` — standalone
  build/test/ship; root `Makefile` `COMPONENTS += agenteval`

Cases are data; checkers are a small fixed vocabulary; driver and report
are thin. The dashboard is a subcommand rendering report artifacts —
split a daemon out only if a hosted always-on watch is needed.

## Orthogonality + neighbors

Public surfaces only — the same ones a human or external tool uses. The
parity case is a direct check of the uniform-MCP+REST invariant (`5/5`);
webhook/chat-link cases lean on `5/W`; privilege cases on `4/9`. No
arizuko Go imports; fs/sqlite reads are a non-gating `--debug` aid.

Distinct from: the `eval` skill (operational health), `create-eval`
(generates a project eval skill), `make test-e2e` (one in-tree round).
`agenteval` is the **agent-capability gate** — what the platform
promises the agent can do for itself, certified against a live instance.
