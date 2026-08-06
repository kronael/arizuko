---
status: partial
depends: [1-cockpit-index]
---

# Per-daemon dashboards — routd, runed, authd, proxyd, onbod, timed, crackbox, webd, davd, ttsd

Architecture, read-path (`/v1` only), routing, auth, theme, HTMX
grammar, and non-goals live in [`7/1`](1-cockpit-index.md) and are not
restated here. Channel adapters are [`7/11`](11-adapter-contract.md) +
[`7/12`](12-adapter-dashboards.md).

Each daemon's dashboard is its own `/dash/<daemon>/` namespace on
`:8080`, rendering the same handlers its `/v1` face already serves.
The table is the spec; the notes below hold only the reasoning that
does not generalise.

## The set

| Daemon   | Built?                                                     | Pages                                                                   | Key controls                                                                                                                           | Required `/v1` work                                                                                                                                                                                                        |
| -------- | ---------------------------------------------------------- | ----------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| routd    | no                                                         | overview, queue (+breaker), channels, errored, routes, cost             | retry errored chat (= reset breaker + re-enqueue), drop errored rows, route CRUD, revoke route token, disengage                        | `GET /v1/queue` (needs `GroupQueue.Snapshot()`), `POST /v1/queue/retry`, `DELETE /v1/routing/errored?jid=`, `GET /v1/outbound/pending`, `GET /v1/cost/summary`, `GET /v1/engagement` (list)                                |
| runed    | no                                                         | overview (capacity + breaker + GC config), runs (`?state=`), run detail | kill run, stop folder's run — both exist ([`runed/server.go:38-43`](../../runed/server.go))                                            | `GET /v1/runs?state=&folder=&limit=` (needs `DB.ListSpawns`), `GET /v1/capacity` (needs `Manager.Snapshot()`)                                                                                                              |
| authd    | no                                                         | overview, keys, tokens (refresh families), providers, identities        | rotate key, revoke-all keys, issue token (exists), revoke refresh family, unlink provider identity                                     | `GET /v1/keys/meta`, `POST /v1/keys/rotate`, `POST /v1/keys/revoke-all`, `GET` + `DELETE /v1/refresh/families[/{family}]`, `GET /v1/users[/{id}]`, `DELETE /v1/users/{id}/identities/{provider}`, `GET /v1/identities`     |
| proxyd   | no                                                         | overview, routes, denials, transit                                      | route create/edit/delete (all exist, [`proxyd/resource.go`](../../proxyd/resource.go)), clear denial counters                          | `GET` + `DELETE /v1/denials` (in-memory ring + counters), `GET /v1/upstreams` (per-backend 500ms probe), `GET /v1/transit`                                                                                                 |
| onbod    | **yes** — [`onbod/dash.go`](../../onbod/dash.go)           | admission queue (`/dash/onbod/`)                                        | approve / deny / reprompt — shipped as dash shims ([`onbod/main.go:178-181`](../../onbod/main.go))                                     | **none for the queue** — all four `/v1/onboarding` verbs ship ([`onbod/main.go:167-171`](../../onbod/main.go), [`onbod/admin.go:85-150`](../../onbod/admin.go)). Gates + invites pages unbuilt; their `/v1` already exists |
| timed    | **overview only** — [`timed/dash.go`](../../timed/dash.go) | overview (shipped); tasks, task detail, runs (unbuilt)                  | pause/resume, cancel, run-now — routd's `PATCH` + `DELETE /v1/tasks/{id}` ship ([`routd/tasks_http.go:16`](../../routd/tasks_http.go)) | **none on routd** — `/v1/tasks` list/get/patch/delete + `/v1/tasks/runs` + `/v1/tasks/{id}/runs` all ship ([`routd/server.go:258-267`](../../routd/server.go)). Remaining work is timed-local page handlers                |
| crackbox | no                                                         | overview, registry, denials                                             | unregister workload, overwrite live allowlist — both exist ([`crackbox/pkg/admin/api.go:76-85`](../../crackbox/pkg/admin/api.go))      | `GET /v1/denials?limit=` (ring over the four deny sites), proxyd `Route.InjectAuthorization`, a `template/services/crackbox-routes.json` gated by `EGRESS_CRACKBOX`                                                        |
| webd     | no                                                         | overview (registration + SSE hub usage)                                 | **none** — token revocation and web-route edits are routd-owned, rendered on routd's routes page                                       | `GET /v1/hub/status` (hub keys, per-key subscribers, caps — memory-only)                                                                                                                                                   |
| davd     | no, and none planned                                       | **none** — hub tile only                                                | **none exist** — dufs has no session list and no disconnect verb                                                                       | a per-tile probe-path override in the hub ([`7/2`](2-dashd-hub.md)): dufs has no `/health`, so `GET /` 200 is the signal                                                                                                   |
| ttsd     | no                                                         | overview, read-only                                                     | **none** — backend selection is `TTS_BACKEND_URL`, an env/restart concern                                                              | `GET /v1/status` (`{backend, ok, probe, latency_ms, last_error, last_error_at}`)                                                                                                                                           |

Route registration follows `7/1`: core daemons get their
`/dash/<daemon>/` entry in `coreProxydRoutes`
([`compose/compose.go:302-308`](../../compose/compose.go) — onbod and
timed are already there, ahead of the `/dash/` catch-all to dashd);
packaged daemons ship a `services/<name>-routes.json`.

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

The queue page labels both: routd's own counters, and the
runed-originated consequence. runed's counters render on runed's
overview. Because the breaker auto-resets, "reset breaker" and "retry"
are ONE verb — resetting without re-enqueuing is a no-op state, and
re-enqueuing resets by construction. One code path.

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

There is no tokens page: `mcp_tokens` and the per-spawn brokered token
were dropped ([`runed/migrations/0003-drop-mcp-tokens.sql`](../../runed/migrations/0003-drop-mcp-tokens.sql))
— nothing consumed the token and authd forced every one to
`sub=service:runed`. A turn's authority is the SO_PEERCRED-gated unix
socket, which is visible as the run itself.

### authd — metadata only, and two identity families

**Page-wide rule: metadata only, never secret material.** No
`priv_pem`, no refresh-token hashes, no service bootstrap secrets, no
OAuth client secrets — excluded at the SQL level, not by
post-filtering. The single exception is a freshly issued access JWT on
the issue-token form: shown once in the mint response, never persisted,
never re-displayable.

Access JWTs are stateless (no issued-token table, `jti` unpersisted),
so the "tokens" page is **refresh families**: `refresh_tokens` grouped
by `family_id`, with live / spent / revoked state. A reuse-revoked
family is the forensic trace of a replay.

The identities page renders one table family: `auth_users` +
`oauth_identities`
([`authd/migrations/0001-authd-schema.sql:22,32`](../../authd/migrations/0001-authd-schema.sql))
— the canonical user and its provider logins, what
`GET /v1/identities/{sub}` resolves for routd's `inspect_identity`.

There used to be a second family here — `identities` + `identity_claims`, an
advisory cross-channel-claim table with no live writer — dropped 2026-08-04
(`fcd845cb`, `authd/migrations/0006-drop-identities.sql`). Binding a channel
identity to a person is now [`5/31`](../5/31-identity-pairing.md) pairing,
which writes `acl_membership`, not this page's table — the dashboard row
above ("unlink provider identity") only ever unlinks an `oauth_identities`
row.

`GET /v1/keys` is the public JWKS and must stay public-cacheable —
hence the separate operator-gated `GET /v1/keys/meta`. Providers are
mounted from env at boot, so there is no runtime enable/disable verb
and this spec does not invent one.

### proxyd — no route cache, therefore no reload verb

Every request reads `proxyd_routes` fresh
([`proxyd/resource.go:60-63`](../../proxyd/resource.go) — the spec-5/8
no-cache rule; the old snapshot cache was deleted). A route mutation is
live for the next request with no restart. **Do not propose a "reload
routes" button** — there is nothing to reload. Equally, there is no
`enabled` column: the honest disable is DELETE.

Everything else proxyd serves (`/auth/*`, `/pub/*`, `/dav/*`,
`/chat/`, `/hook/`, `TRUSTED_PROXIES`, vhost aliases) is boot config —
rendered read-only. Denial classes tagged at their emit sites:
`auth_denied`, `dav_forbidden`, `dav_blocked`, `rate_limited`. The ring
is RAM-only by design; audit web events remain the durable record.

### onbod — the most built of the set

`/dash/onbod/` renders the admission queue today and its approve /
deny / reprompt shims call the **same store writers** the `/v1`
handlers use ([`onbod/dash.go`](../../onbod/dash.go)) — one writer per
resource, two faces. Its operator gate is `**` in the proxyd-stamped
`X-User-Groups`, distinct from both onbod's public-flow guard and its
bearer-scoped `/v1` gate, and distinct again from the end-user
`/onboard` page, which is not part of the cockpit.

Two facts the gates page must surface when it is built: gate matching
is first-match-wins with `*` as catch-all, and **zero enabled gates
means every linked JID auto-approves** — render that as a visible
`warn`, and put it in the confirm dialog for disabling the last gate.
No `/v1` response or rendered page ever carries a live onboarding
`token`; it is a bearer credential for binding a JID.

### timed — routd owns the rows, timed owns the fire loop

routd owns `scheduled_tasks` + `task_run_logs` at rest and serves the
agent MCP verbs. timed owns the **fire runtime**: the 60s tick, the
atomic claim, and all cron/interval computation. That split is why
`/dash/timed/` reads task rows through **routd's `GET /v1/tasks`** over
timed's `service:timed` bearer rather than its own DB — in split mode
timed opens no DB at all ([`timed/dash.go`](../../timed/dash.go),
mounted at [`timed/split.go:73`](../../timed/split.go)).

Consequence, accepted: routd's audit rows for dashboard writes
attribute to `service:timed`, not the operator; the operator decision
is recorded at timed's own dash gate.

Only timed can compute the next-ticks preview (it holds the cron
parser), so that pane needs no endpoint — it is a pure function of the
row the page already fetched. Lag is `now − next_run` on `active` rows
(≤60s is normal, >2 ticks is `warn`); a row stuck in `firing` past 2
ticks is `err` — a crash between claim and fire strands it there.
Run-now is `PATCH {next_run: now}`, reusing the one claim-fire loop
rather than adding a second fire path, and is offered only on `active`
tasks because the claim predicate requires it.

### crackbox — orthogonality forces a different gate, and a different port

crackbox imports no arizuko-internal subpackage
([`crackbox/README.md:152-155`](../../crackbox/README.md)), and `auth`
is in that grep list — so **crackbox cannot verify proxyd's transit
proof**. Its daemon-side gate is instead its existing constant-time
admin bearer check, with proxyd injecting
`Authorization: Bearer $CRACKBOX_ADMIN_SECRET` on forward (the new
`Route.InjectAuthorization` field). proxyd's own gate carries
`auth: "operator"`. Rejected: hand-rolling an HMAC verifier inside
crackbox (duplicates `auth/middleware.go`), and trusting the docker
network (agent containers share the egress network with crackbox —
that is exactly why the admin secret exists). CSRF is a hand-rolled
same-origin check; `auth/dashauth.go` is likewise unimportable.

`theme` is stdlib-only and absent from the orthogonality grep, so
crackbox imports it directly — no CSS copy, no rendering from dashd.

The dashboard is served from the **admin listener `:3129`**, a
documented deviation from the universal `:8080`: crackbox's ports
predate the convention and are part of its external contract. The
proxyd route backend targets `:3129` explicitly.

Live enforcement state is crackbox's; **persistent per-folder egress
policy is routd's `network_rules`**, edited on routd's dashboard. One
renderer each. A live-allowlist edit therefore lasts only until the
next spawn re-registers from routd-resolved policy — the page says so.

### webd / davd / ttsd — davd is the minimality precedent

**davd ships no `/dash/` namespace at all.** It is upstream
`sigoden/dufs` in an alpine wrapper ([`davd/README.md`](../../davd/README.md))
— there is no arizuko code in the container to host a page or a `/v1`
surface, and wrapping dufs in a Go sidecar purely to host a dashboard
fails minimality. Its cockpit presence is the hub tile. WebDAV here is
stateless per request; dufs exposes no session list and no
disconnect/revoke verb, and davd has no notion of identity — auth,
per-group scoping, and the write guard all live upstream in proxyd. So
"active sessions" and "revoke" are **not available**, not deferred, and
`/dav/*` write traffic is observable where it is gated: proxyd. Revisit
only if davd ever gains arizuko code for other reasons.

webd's dash hosts no token or web-route pages: those tables are
routd-owned and render on routd's routes page. There is no token
minting anywhere in the cockpit — creation stays with `/me/chats/new`
and the agent tools, one creation flow.

ttsd's page is read-only **explicitly**, and says so inline so nobody
hunts for a button: switching backends is an `.env` edit plus restart,
an operator runbook step. Infra toggles stay in env.

## Acceptance

- Every page's data flows through the owning daemon's `/v1` handlers or
  helpers — no SQL in any `/dash/` handler (code-review check,
  `7/1` read-path).
- Each daemon's overview renders for an operator behind proxyd; a
  non-operator gets the themed 403 banner, and the hub tile shows
  `warn` (up, scope-denied) rather than `err` (probe failed).
- Live-state claims hold against the process: routd's queue snapshot
  matches `docker ps` during a run; runed's `active_count` matches
  `Manager.ActiveCount()` under concurrency; a killed run leaves the
  folder's breaker count unchanged.
- Tripping a chat breaker (3 consecutive failed turns) shows it at
  threshold on routd's queue page, and retry dispatches a fresh run.
- Creating a route from proxyd's page is honoured by the next request
  through proxyd with no restart.
- No response under `/dash/authd/` or its new `/v1` reads contains
  `priv_pem` or any refresh-token material; no onbod surface contains a
  live onboarding `token`.
- A folder-scoped token calling `GET /v1/runs` sees only its subtree
  (containment mirrors `runed/authz_containment_test.go`).
- On an `EGRESS_CRACKBOX` instance a crackbox tile appears and a
  blocked egress attempt lands on its denials page within one poll; on
  a non-crackbox instance there is no route and no tile.
- No `/dash/davd/` route is registered, and its tile is green exactly
  when dufs answers `GET /` with 200.
