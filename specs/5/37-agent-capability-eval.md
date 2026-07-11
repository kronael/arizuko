---
status: shipped
---

# 5/37 — agent-capability eval (`anteval`)

> **Status (2026-07-11): shipped.** Run LIVE against krons ($4.78 / 23 turns):
> **7/8 of the `--smoke` basis passed** — self-skill, rest-roundtrip,
> chat-entrypoint, pub-200, child-delegate, priv-grant, and priv-401 (green
> after the harness's redirect-following bug was fixed — see `## Live-run
preconditions`). The 8th, `webhook-in`, FAILED — and that is the eval doing
> its job: it caught a **real krons bug**, not an agent-capability gap. The
> `/hook/` proxyd route was never seeded on the instance (seeded-once drift) and
> live proxyd doesn't hot-reload the route table, so spec 5/W webhooks are dead
> there (BUGS.md; row staged for the next deploy). A green health check missed
> it; the capability prober did not. The
> operator token gap is closed by `arizuko token <inst> issue bearer <folder>
--scope messages:write,messages:read` (CLI signs with authd's active key from
> auth.db). Former gap (a) is closed in code: routd `GET /v1/cost?turn_id=`
> (scope `cost:read`) + `HTTPTarget.Cost()`; budgets bite once the target routd
> carries the endpoint. Gap (b) remains, honest not silent: `--mcp` unset →
> `rest-mcp-parity` fails loudly ("surface not configured"); the platform's
> chat-token MCP face lacks an inspect-read, proposal filed in `BUGS.md`.

> **Understanding cases (2026-07-11).** Added five cases that probe the
> agent's _model of arizuko_, not the plumbing — each targets a
> documented misunderstanding-class where the agent fails SILENTLY and
> health stays green: `route-observe-silent` (trigger vs `#observe`,
> chat-routing.md / migration 157), `egress-diagnose` +
> `self-grant-noop` (crackbox egress vs auth; settings.json edits are a
> no-op — ant/CLAUDE.md), `priv-pub-separation` (`/priv` tree does not
> leak into `/pub`), `state-persist` (only `~` survives an ephemeral
> container). They ride three new checker modifiers on the fixed kinds —
> negation, callback field-match, two-turn (`Prompt2`, `/new`) — so the
> checker vocabulary stays closed. `smoke = false` (capability
> regression watch, not the release gate). `route-observe-silent` and
> the egress pair carry run-time preconditions in their TOML comments
> (isolated run / tier ≥2 / non-`*` allowlist), like `MaxChildren`.

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

`anteval` is a **black-box capability prober**. For each case it
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
  Zero `github.com/kronael/arizuko/*` imports (11/A) — `anteval`
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
- **Cost as a budget, not a readout.** Each case declares `max_wall_ms`
  (a hard await deadline) and `max_tokens` (spend over it fails the case,
  read from routd cost via REST); the report totals real spend. Turn
  counts aren't exposed black-box, so there is no `max_turns`. `--smoke`
  runs the tagged minimal basis; full runs all.

WHY a shipped component, not a `go test`: capability must be checkable
against a **live deployed** instance — release gate, post-migrate smoke,
or a regression watch with a dashboard — not only in the build. It ships
in arizuko's suite (11/A) and runs standalone.

## Cases

24 cases over eight dimensions; each proves one capability through a
public-surface checker. `★` marks the `--smoke` basis (the gate). All
identifiers carry the run nonce. `NEW` flags the five silent-failure
cases (2026-07-11) and the checker modifier each exercises.

| #                          | Case                 | Task given to the agent                                                                                                                | Checker (public surface)                                                                 |
| -------------------------- | -------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| **Self-modification**      |                      |                                                                                                                                        |                                                                                          |
| ★                          | self-skill           | create a skill that, when invoked, calls `{sink}/cb/{nonce}`                                                                           | callback received                                                                        |
|                            | self-mcp             | add an MCP server and call a tool that emits `{nonce}` to the sink                                                                     | callback received                                                                        |
| `NEW`                      | state-persist        | turn 1 persists `{nonce}` under `~`; turn 2 (`/new` resets the session) reads it back from disk and calls the sink                     | two-turn callback (proves on-disk, not session, persistence)                             |
| **Subagents + privileges** |                      |                                                                                                                                        |                                                                                          |
| ★                          | child-delegate       | create a child group, delegate a `{nonce}` task                                                                                        | child-owned reply carrying `{nonce}` readable via REST                                   |
| ★                          | privilege-gate       | child attempts a privileged action (publish) → expect fail; grant; retry                                                               | URL 404 before grant, 200 after                                                          |
|                            | privilege-revoke     | revoke that grant; child retries the action                                                                                            | URL 200→404 (privilege withdrawn)                                                        |
|                            | child-identity       | child publishes a `{nonce}` page                                                                                                       | `GET /pub/<child-folder>/…` == 200 (subagent identity in the path)                       |
| **Web publishing**         |                      |                                                                                                                                        |                                                                                          |
| ★                          | pub-200              | publish a public page at a `{nonce}` URL                                                                                               | `GET /pub/…/{nonce}` == 200                                                              |
| ★                          | priv-401             | publish a gated page at a `{nonce}` URL                                                                                                | `GET /priv/…/{nonce}` == 303 → login unauth (gate engaged; proxyd never 401s a page GET) |
|                            | unpublish-404        | delete a previously published page                                                                                                     | URL 200→404 (publish lifecycle)                                                          |
|                            | web-route            | add a web route for a `{nonce}` path                                                                                                   | path resolves per the rule (200 / redirect / 401)                                        |
| `NEW`                      | priv-pub-separation  | publish a gated page under `~/private_html/` at a `{nonce}` path                                                                       | `GET /pub/<same path>` == 404 — the private tree does not leak into `/pub`               |
| **Online chat apps**       |                      |                                                                                                                                        |                                                                                          |
| ★                          | chat-entrypoint      | publish a page embedding a chat link (`issue_chat_link`)                                                                               | page 200 AND `/chat/{token}` 200 AND a message through it reaches the agent              |
|                            | app-to-chat          | publish an app that on submit posts `{nonce}` into a chat                                                                              | turn carrying `{nonce}` observed in the chat via REST                                    |
|                            | chatlink-revoke      | revoke a chat link                                                                                                                     | `/chat/{token}` 200→404/403                                                              |
| **Reach via REST + MCP**   |                      |                                                                                                                                        |                                                                                          |
| ★                          | webhook-in           | create a webhook (`issue_webhook`); send `{nonce}` payload                                                                             | `POST /hook/{token}` produces a turn carrying `{nonce}` (REST read)                      |
| ★                          | rest-roundtrip       | post `{nonce}` via the REST chat surface                                                                                               | agent reply readable via REST                                                            |
| ★                          | mcp-roundtrip        | external MCP client posts `{nonce}` into a chat and reads it back                                                                      | post + read both succeed over MCP                                                        |
|                            | rest-mcp-parity      | harness writes a sentinel turn                                                                                                         | REST and MCP return the same canonical subset (chat id, message id, body)                |
| **Composite**              |                      |                                                                                                                                        |                                                                                          |
|                            | spawn-publish-reach  | create + grant-web a child; child publishes an app with a chat entrypoint; reach the **child** from public web into a chat; it replies | full chain green, public-surface only                                                    |
|                            | product-rest-and-mcp | stand up one online app + chat reachable via a public link **and** REST **and** MCP                                                    | all three reach `{nonce}`                                                                |
| **Routing**                |                      |                                                                                                                                        |                                                                                          |
| `NEW`                      | route-observe-silent | make this chat observe-only (`#observe`, no turn), then a plain follow-up arrives                                                      | negated `rest_reply`: a bot reply means the channel still triggers → fail                |
| **Egress**                 |                      |                                                                                                                                        |                                                                                          |
| `NEW`                      | egress-diagnose      | probe a reachable host and a non-allowlisted one; diagnose WHY the failed one failed (`egress` vs `auth`)                              | `callback` field-match `deny_reason=egress`                                              |
| `NEW`                      | self-grant-noop      | tier-2 agent tries to self-grant egress to a denied host, then curl the sink from it (settings.json edits are a no-op)                 | negated `callback`: the host must never become reachable                                 |

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

## Live-run preconditions (learned on krons, 2026-07-11)

- **Token**: `arizuko token <inst> issue bearer <folder> --scope
messages:write,messages:read` mints the folder-scoped ES256 bearer
  (add `cost:read` once the target routd serves `GET /v1/cost`).
- **Eval folder**: create via `arizuko group <inst> add web:<folder> <folder>`
  (never a manual mkdir); `--chat web:<folder>` then routes 1:1.
- **Subagent cases need child capacity**: the folder's `MaxChildren`
  defaults to 0 — child-delegate/priv-grant fail with "spawning disabled"
  until the operator raises it (dashd group settings, container_config).
- **Sink**: bind `--sink-addr :PORT` on the eval host and pass the target
  instance's docker-gateway IP as `--sink http://<gateway-ip>:PORT` — a
  host-local IP is reachable from agent containers even on internal
  networks; crackbox name-matching skips IP entries, so a name-based
  allowlist rule cannot cover it (a folder `*` rule can, for https
  self-verification of published pages).
- **Sequential cases share one agent session**: a case that blows its
  wall budget leaves the agent mid-task and the backlog bleeds into the
  next case's window. Per-case latencies are honest only when the prior
  turn finished; `--case` re-runs isolate cleanly.

Scoring is **binary per case** (effect present or absent), aggregated to
pass-rate per dimension. A partial-credit layer (attempted vs achieved)
is additive and out of scope for the gate.

## Component shape

`anteval/` is a sibling component (11/A), structured like `crackbox/`:

- `anteval/cmd/anteval/main.go` — `run <target>` | `validate` | `dash`
- `anteval/pkg/spec` — TOML case schema + loader
- `anteval/pkg/check` — checker vocabulary: `http_status`,
  `callback`, `rest_reply`, `rest_observe`, `mcp_roundtrip`,
  `parity_sentinel`. Three modifiers (2026-07-11) ride the fixed
  kinds — no new kinds:
  - **negation** (`Check.Negate`, any kind) — a case passes only if the
    positive condition NEVER occurs within the budget window; one hit
    fails it immediately (`unexpected: …`), surviving the window passes
    it (`absent as required: …`). Reuses every positive checker.
  - **callback field-match** (`Check.Field`+`Equals`, `callback` only) —
    require a hit whose `Query[Field] == Expand(Equals)`, not just any
    hit; the reason names the field, the wanted value, and what was seen.
  - **two-turn** (`Case.Prompt2`) — inject Prompt1, let turn 1 settle
    (`min(30s, budget/3)`, no checker), inject Prompt2, grade turn 2 as
    today. Lead Prompt2 with `/new ` to reset the session so a pass
    proves on-disk (not session) persistence. Cost tracks turn 2.
- `anteval/pkg/run` — driver; hosts the callback sink; nonce → inject
  → await → check → record
- `anteval/pkg/report` — JSON + markdown/HTML render (the `dash`
  subcommand reads these artifacts; no daemon)
- `anteval/cases/*.toml` — the 24 cases
- `anteval/{Dockerfile,Makefile,README.md}` — standalone
  build/test/ship; root `Makefile` `COMPONENTS += anteval`

Cases are data; checkers are a small fixed vocabulary; driver and report
are thin. The dashboard is a subcommand rendering report artifacts —
split a daemon out only if a hosted always-on watch is needed.

## Orthogonality + neighbors

Public surfaces only — the same ones a human or external tool uses. The
parity case is a direct check of the uniform-MCP+REST invariant (`5/45`);
webhook/chat-link cases lean on `5/W`; privilege cases on `4/9`. No
arizuko Go imports; fs/sqlite reads are a non-gating `--debug` aid.

Distinct from: the `eval` skill (operational health), `create-eval`
(generates a project eval skill), `make test-e2e` (one in-tree round).
`anteval` is the **agent-capability gate** — what the platform
promises the agent can do for itself, certified against a live instance.
