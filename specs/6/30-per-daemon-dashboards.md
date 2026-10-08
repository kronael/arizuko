---
status: shipped
shipped: 2026-08-30
depends: [28-cockpit-index]
---

# Per-daemon dashboards — routd, runed, authd, proxyd, onbod, timed, webd, davd, ttsd

Architecture, read-path (`/v1` only), routing, auth, theme and HTMX
grammar live in [`6/28`](28-cockpit-index.md) and are not restated here.
Channel adapters are [`6/31`](31-adapter-contract.md) +
[`6/32`](32-adapter-dashboards.md).

Each daemon's dashboard is its own `/dash/<daemon>/` namespace on
`:8080`, rendering the same handlers its `/v1` face already serves.
The table is the spec; the notes below hold only the reasoning that
does not generalise.

## The set

| Daemon | Dashboard                                | Pages                                                                          | Controls                                                                                                                                                                      |
| ------ | ---------------------------------------- | ------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| routd  | [`routd/dash.go`](../../routd/dash.go)   | overview                                                                       | retry errored chat (= reset breaker + re-enqueue), replay                                                                                                                     |
| runed  | [`runed/dash.go`](../../runed/dash.go)   | overview (capacity + run table; **no breaker** — `runed/dash.go` renders none) | kill run (`POST /dash/runed/kill`). **"stop folder's run" is not mounted**: `runed/server.go:65-66` has the GET and `kill`, nothing else                                      |
| authd  | [`authd/dash.go`](../../authd/dash.go)   | keys, sessions (refresh families)                                              | revoke (`POST /dash/authd/revoke`). **rotate key, revoke-all keys and issue token are not mounted**: `authd/http.go:119-120` has the GET and `revoke`, nothing else           |
| proxyd | [`proxyd/dash.go`](../../proxyd/dash.go) | routes                                                                         | route create/delete ([`proxyd/resource.go`](../../proxyd/resource.go))                                                                                                        |
| onbod  | [`onbod/dash.go`](../../onbod/dash.go)   | admission queue (`/dash/onbod/`)                                               | approve / deny / reprompt — dash shims over the four `/v1/onboarding` verbs ([`onbod/main.go:167-181`](../../onbod/main.go), [`onbod/admin.go:85-150`](../../onbod/admin.go)) |
| timed  | [`timed/dash.go`](../../timed/dash.go)   | overview (loop health + task table)                                            | —                                                                                                                                                                             |
| webd   | [`webd/dash.go`](../../webd/dash.go)     | SSE hub overview                                                               | **none** — token revocation and web-route edits are routd-owned                                                                                                               |
| davd   | none — hub tile only                     | —                                                                              | **none exist** — dufs has no session list and no disconnect verb; it has no `/health`, so `GET /` 200 is the tile's signal ([`6/29`](29-dashd-hub.md))                        |
| ttsd   | [`ttsd/dash.go`](../../ttsd/dash.go)     | backend + live probe                                                           | **none** — backend selection is `TTS_BACKEND_URL`, an env/restart concern                                                                                                     |

Route registration follows `6/28`: core daemons get their
`/dash/<daemon>/` entry in `coreProxydRoutes`
([`compose/compose.go:302-308`](../../compose/compose.go), ahead of the
`/dash/` catch-all to dashd); packaged daemons ship a `services/<name>.yaml`.

## Federation state (2026-08-30)

**The core set is COMPLETE — all nine accounted for.** Eight serve their own
dashboard (onbod, timed, routd, runed, authd, proxyd, webd, ttsd) and davd
deliberately serves none: dufs exposes no session list and no disconnect verb,
so a dashboard would render an empty page and offer nothing. Its finished state
is the hub tile probed at `GET /`, which ships. The abstention is the ninth
outcome, not a ninth gap.

**proxyd has its own `requireAuth` wrapper** — a path on proxyd's mux never
reaches `s.route`, where a routed request is authenticated, so a bare mount
would serve the route table and its write forms unauthenticated. A test drives
all three paths unauthenticated to pin it.

**ttsd gates differently, and has to.** It verifies no tokens and holds no
keyset, so the nil-`KeySet` form every other daemon uses would fall OPEN — and
proxyd's `user` route authenticates a person, not an operator. It gates on the
stamped operator scope directly, enforcing whenever `AUTHD_URL` is set.

## What dashd does not host

**The chat portal is webd's.** `/me/chats` + `/me/chats/new`
([`webd/server.go:128-141`](../../webd/server.go)), routed through proxyd at
`/me/` ([`compose/compose.go`](../../compose/compose.go) `{Path: "/me/", …
Auth: "user"}`); dashd's `chat` tile and its folder links point there. No
arizuko table stores a route token in plaintext
([`routd/migrations/0036-drop-chat-sessions.sql`](../../routd/migrations/0036-drop-chat-sessions.sql)).

**There is no route-token mint form in the cockpit.** Minting is routd's
`POST /v1/route_tokens/{chat,hook}`, the agent's `issue_chat_link` /
`issue_webhook` / `issue_pairing_link`
([`resreg/resources/route_tokens.go:38-54`](../../resreg/resources/route_tokens.go)),
and `arizuko token`. `/dash/tokens/{folder}/` lists and revokes.

**One cron parser: `core.NextRun`.** A `cron` value becomes an instant in
exactly one place ([`core/cron.go`](../../core/cron.go)), and every writer of
`next_run` reads it: the seeder that gives a new group's tasks their first run
([`store/tasks.go`](../../store/tasks.go) `SeedDefaultTasks`), timed's re-arm
after each fire ([`timed/main.go`](../../timed/main.go) `computeNextRun`), and
the two surfaces that take a cron from a human — routd's `parseTaskSchedule`
([`routd/scheduled_tasks_resource.go`](../../routd/scheduled_tasks_resource.go))
and dashd's `taskNextRun` ([`dashd/tasks_admin.go`](../../dashd/tasks_admin.go)).

What still differs is what those two surfaces ACCEPT, not what they compute:
routd reads an RFC3339 timestamp as a one-shot, dashd rejects that same value
with 400. So the identical schedule is accepted by the agent and refused by the
dashboard.

## Daemon-specific notes

### routd — two breakers, not one

The split has **two** circuit breakers and a reader who sees only one
page will conflate them:

- **routd's queue breaker** counts consecutive `processGroupMessages`
  failures per **chat JID** (`circuitBreakerThreshold = 3`,
  [`queue/queue.go:20,303`](../../queue/queue.go)) and auto-resets on
  the next inbound ([`queue/queue.go:117`](../../queue/queue.go)).
- **runed's spawn breaker** counts spawn failures per **folder**
  ([`runed/manager.go:97,225`](../../runed/manager.go)) and reports
  `breaker_open` on the run outcome, which routd consumes in
  `onCircuitBreakerOpen` ([`routd/dispatch.go:347`](../../routd/dispatch.go),
  [`routd/loop.go:720`](../../routd/loop.go)).

runed's counters render on runed's overview. Because the breaker
auto-resets, "reset breaker" and "retry" are ONE verb — resetting without
re-enqueuing is a no-op state, and re-enqueuing resets by construction. One
code path.

### runed — kill-run IS the revoke

runed mints nothing and holds no revocation primitive. The only thing
scoping a live turn is the container itself, so **killing the run is
the effective revoke**; there is no revoke verb to add. True `jti`
revocation is authd's concern — duplicating it here would be a second
renderer over authd's resource.

Kill is `DELETE /v1/runs/{run_id}` → `Manager.Kill`
([`runed/manager.go:278`](../../runed/manager.go)): stop → kill →
`rm -f`, `state='killed'` (sticky), idempotent on an already-exited
run, and it does **not** count toward the folder breaker (operator
intent is not a run failure). The confirm dialog says the agent's turn
is lost mid-flight.

There is no tokens page: runed mints no token. A turn's authority is the
SO_PEERCRED-gated unix socket, which is visible as the run itself.

### authd — metadata only

**Page-wide rule: metadata only, never secret material.** No
`priv_pem`, no refresh-token hashes, no service bootstrap secrets, no
OAuth client secrets — excluded at the SQL level, not by
post-filtering. The single exception is a freshly issued access JWT on
the issue-token form: shown once in the mint response, never persisted,
never re-displayable.

Access JWTs are stateless (no issued-token table, `jti` unpersisted),
so the "sessions" page is **refresh families**: `refresh_tokens` grouped
by `family_id`, with live / spent / revoked state. A reuse-revoked
family is the forensic trace of a replay.

`GET /v1/keys` is the public JWKS and must stay public-cacheable —
hence the separate operator-gated `GET /v1/keys/meta`. Providers are
mounted from env at boot, so there is no runtime enable/disable verb
and this spec does not invent one.

### proxyd — no route cache, therefore no reload verb

Every request reads `proxyd_routes` fresh
([`proxyd/resource.go:60-63`](../../proxyd/resource.go) — the spec-5/8
no-cache rule). A route mutation is live for the next request with no
restart. **Do not propose a "reload routes" button** — there is nothing
to reload. Equally, there is no `enabled` column: the honest disable is
DELETE.

Everything else proxyd serves (`/auth/*`, `/pub/*`, `/dav/*`,
`/chat/`, `/hook/`, `TRUSTED_PROXIES`, vhost aliases) is boot config —
rendered read-only.

### onbod — one writer per resource, two faces

`/dash/onbod/` renders the admission queue and its approve /
deny / reprompt shims call the **same store writers** the `/v1`
handlers use ([`onbod/dash.go`](../../onbod/dash.go)) — one writer per
resource, two faces. Its operator gate is `**` in the proxyd-stamped
`X-User-Groups`, distinct from both onbod's public-flow guard and its
bearer-scoped `/v1` gate, and distinct again from the end-user
`/onboard` page, which is not part of the cockpit.

No `/v1` response or rendered page ever carries a live onboarding
`token`; it is a bearer credential for binding a JID.

### timed — routd owns the rows, timed owns the fire loop

routd owns `scheduled_tasks` + `task_run_logs` at rest and serves the
agent MCP verbs. timed owns the **fire runtime**: the 60s tick, the
atomic claim, and the re-arm after each fire (which reads the cron
through `core.NextRun`, above). That split is why
`/dash/timed/` reads task rows through **routd's `GET /v1/tasks`** over
timed's `service:timed` bearer rather than its own DB — in split mode
timed opens no DB at all ([`timed/dash.go`](../../timed/dash.go),
mounted at [`timed/split.go:73`](../../timed/split.go)).

Consequence, accepted: routd's audit rows for dashboard writes
attribute to `service:timed`, not the operator; the operator decision
is recorded at timed's own dash gate.

Lag is `now − next_run` on `active` rows (≤60s is normal, >2 ticks is
`warn`); a row stuck in `firing` past 2 ticks is `err` — a crash between
claim and fire strands it there.

### webd / davd / ttsd — davd is the minimality precedent

**davd ships no `/dash/` namespace at all.** It is upstream
`sigoden/dufs` in an alpine wrapper ([`davd/README.md`](../../davd/README.md))
— there is no arizuko code in the container to host a page or a `/v1`
surface, and wrapping dufs in a Go sidecar purely to host a dashboard
fails minimality. Its cockpit presence is the hub tile. WebDAV here is
stateless per request; dufs exposes no session list and no
disconnect/revoke verb, and davd has no notion of identity — auth,
per-group scoping, and the write guard all live upstream in proxyd. So
"active sessions" and "revoke" are **not available**, and `/dav/*` write
traffic is observable where it is gated: proxyd.

webd's dash hosts no token or web-route pages: those tables are
routd-owned. There is no token minting anywhere in the cockpit —
creation stays with `/me/chats/new` and the agent tools, one creation
flow.

ttsd's page is read-only **explicitly**, and says so inline so nobody
hunts for a button: switching backends is an `.env` edit plus restart,
an operator runbook step. Infra toggles stay in env.

## Acceptance — and the one criterion that does not hold

Checked against the code 2026-10-08.

- ✅ Every page's data flows through the owning daemon's `/v1` handlers or
  helpers — no SQL in any `/dash/` handler (code-review check,
  `6/28` read-path).
- ⚠ Each daemon's overview renders for an operator behind proxyd; a
  non-operator gets the themed 403 banner, **and the hub tile shows `warn` (up,
  scope-denied) rather than `err` (probe failed)**. The render and the 403 hold; the
  `warn` state does not exist — `dashd/services.go:78-80` declares `ok`, `err` and
  `unknown`, so a scope-denied daemon is indistinguishable from one that did not
  answer (`6/29`'s acceptance carries the same item).
- ✅ Live-state claims hold against the process: runed's `active_count`
  matches `Manager.ActiveCount()` under concurrency; a killed run leaves
  the folder's breaker count unchanged.
- ✅ Creating a route from proxyd's page is honoured by the next request
  through proxyd with no restart.

The table above also over-promised two daemons' controls, now corrected in place:
runed has `kill` and not "stop folder's run", authd has `revoke` and not three more. A
table that lists a control nobody mounted sends an operator looking for a button that
is not there.

- No response under `/dash/authd/` or its `/v1` reads contains
  `priv_pem` or any refresh-token material; no onbod surface contains a
  live onboarding `token`.
- No `/dash/davd/` route is registered, and its tile is green exactly
  when dufs answers `GET /` with 200.
