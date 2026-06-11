---
status: draft
depends: [1-cockpit-index]
---

# routd — dashboard

Architecture, routing, auth, theme, and the read-path rule live in
[`6/1`](1-cockpit-index.md). This spec adds only routd's page list,
show/control matrix, and the `/v1` work it requires.

## 1. Purpose

Operator view + control over the conversation/routing plane: the
dispatch queue, the circuit breaker, the channel registry, errored
chats, the route table, and cost/engagement state routd owns.

## 2. Pages

All under `/dash/routd/` on `:8080` (proxyd route `/dash/routd/` →
`routd:8080/dash/`; routd ships no service TOML, so the route entry
goes in the static core list in `compose/compose.go` — the core-daemon
equivalent of the `[[proxyd_route]]` TOML, per `6/1` Routing).

| Page     | Path                   | Covers                                                                |
| -------- | ---------------------- | --------------------------------------------------------------------- |
| overview | `/dash/routd/`         | instance gauges (the `/status` chat command, promoted to a page)      |
| queue    | `/dash/routd/queue`    | dispatch queue + circuit breaker (one in-memory struct, one renderer) |
| channels | `/dash/routd/channels` | channel-registry health                                               |
| errored  | `/dash/routd/errored`  | errored chats + pending outbound                                      |
| routes   | `/dash/routd/routes`   | route table, web routes, route tokens                                 |
| cost     | `/dash/routd/cost`     | per-folder spend vs caps + live engagements                           |

The brief's "queue" and "circuit breaker" are one page: the breaker IS
per-chat queue state (`queue.groupState.consecutiveFailures`,
[`queue/queue.go:22`](../../queue/queue.go)); splitting them would mean
two renderers over one struct.

When this spec ships, the routes page subsumes dashd's retained
`routes`/`route-tokens` editor: they edit routd-owned tables, so per
`6/2`'s migration rule the migrating PR deletes the dashd renderer and
repoints the link.

## 3. Show

**overview** — the numbers `cmdStatus` already computes
([`routd/steer.go:200`](../../routd/steer.go)): registered channels,
groups, active runs (`q.ActiveCount()`,
[`queue/queue.go:361`](../../queue/queue.go)), errored-chat count
(`CountErroredChats`, [`routd/db.go:447`](../../routd/db.go)), active
scheduled tasks, plus in-flight turns (`RunningTurns`,
[`routd/db.go:748`](../../routd/db.go)) with turn_id / folder / age.

**queue** — live process state only routd can serve (the `6/1`
read-path argument):

- per-chat slot: active?, container name, owning folder, consecutive
  failures (0–3, threshold `circuitBreakerThreshold = 3`,
  [`queue/queue.go:20`](../../queue/queue.go))
- waiting list in FIFO order (`waitingGroups`), active-folder map,
  `activeCount` / `maxConcurrent`
- breaker pane: chats at the threshold, with the trip evidence — the
  chat's errored flag (`MarkChatErrored`,
  [`routd/db.go:411`](../../routd/db.go)) and its `errored=1` message
  rows. Two breakers exist in the split and the pane labels them:
  routd's queue breaker counts `processGroupMessages` failures per chat
  JID ([`queue/queue.go:298`](../../queue/queue.go)); runed's breaker
  counts spawn failures per folder and reports `breaker_open` on the
  run outcome, which routd consumes in `onCircuitBreakerOpen`
  ([`routd/dispatch.go:246`](../../routd/dispatch.go),
  [`routd/loop.go:643`](../../routd/loop.go)). runed's own counters
  render on `6/4`; here we show the routd-side consequence.
- note rendered inline: the breaker auto-resets on the next inbound
  ([`queue/queue.go:117`](../../queue/queue.go)) — manual reset is a
  convenience, not a rescue.

**channels** — the in-memory registry (`GET /v1/channels` shape,
`handleChannelList`, [`routd/channels.go:147`](../../routd/channels.go)):
name, URL, jid_prefixes, capabilities, health_fails (0–3),
registered_at. Inline note: the health loop probes every 30s and
auto-deregisters after 3 fails
([`chanreg/health.go:55`](../../chanreg/health.go)); adapters
re-register themselves (chanlib retries), so an absent row after a
restart is expected for ~seconds.

**errored** — `GET /v1/routing/errored` (`handleErroredChats`,
[`routd/reads_http.go:171`](../../routd/reads_http.go)): chat_jid,
errored-row count, routed_to, last_at. Below it, pending outbound: bot
rows stuck `status=pending` (`PendingOutbound`,
[`routd/db.go:384`](../../routd/db.go)) with age — the retry sweep
redelivers every 30s and fails rows after 24h
([`routd/loop.go:331`](../../routd/loop.go)).

**routes** — `GET /v1/routes` rows (seq, match, target, observe
windows — `apiv1.Route`,
[`routd/api/v1/types.go:206`](../../routd/api/v1/types.go)),
`GET /v1/web_routes`, `GET /v1/route_tokens` (never the raw token).

**cost** — per-folder today-spend vs cap (`SpendTodayFolder` /
`FolderCap`, [`routd/db.go:925`](../../routd/db.go),
[`routd/db.go:913`](../../routd/db.go)), instance toggle
`COST_CAPS_ENABLED`, recent `cost_log` rows. Engagements pane: live
engagement windows (jid, topic, folder, expires) — routd owns the
engagement table ([`routd/reads_http.go:199`](../../routd/reads_http.go)).

## 4. Control

Every affordance maps to a `/v1` verb; dangerous ones get `.btn-danger`

- the `auth/dashauth.go` CSRF check (`6/1`).

| Affordance                                        | Verb                                        | Exists?                                                      | Danger                |
| ------------------------------------------------- | ------------------------------------------- | ------------------------------------------------------------ | --------------------- |
| retry errored chat (= reset breaker + re-enqueue) | `POST /v1/queue/retry`                      | to add                                                       | no                    |
| drop a chat's errored rows                        | `DELETE /v1/routing/errored?jid=`           | to add                                                       | `.btn-danger`         |
| add route                                         | `POST /v1/routes`                           | yes ([`routd/server.go:228`](../../routd/server.go))         | no                    |
| delete route                                      | `DELETE /v1/routes/{id}`                    | yes ([`routd/server.go:229`](../../routd/server.go))         | `.btn-danger`         |
| replace route table                               | `PUT /v1/routes`                            | yes ([`routd/server.go:227`](../../routd/server.go))         | `.btn-danger`         |
| put/delete web route                              | `PUT/DELETE /v1/web_routes`                 | yes                                                          | delete: `.btn-danger` |
| revoke route token                                | `DELETE /v1/route_tokens/{jid}`             | yes ([`routd/server.go:237`](../../routd/server.go))         | `.btn-danger`         |
| disengage an engagement                           | `POST /v1/engagement` with `ttl_seconds<=0` | yes ([`routd/reads_http.go:218`](../../routd/reads_http.go)) | no                    |

"Reset breaker" and "retry" are ONE verb: resetting failures without
re-enqueuing is a no-op state (the next inbound resets anyway), and
re-enqueuing resets by construction
([`queue/queue.go:117`](../../queue/queue.go)). One code path.

"Retry pending outbound now" is deliberately absent: the sweep
self-heals on a 30s cadence; a manual trigger would add a verb whose
only effect is impatience.

## 5. Required `/v1` work

1. **`GET /v1/queue`** — queue + breaker snapshot: active slots
   (jid, folder, container), waiting FIFO, active/max counts, per-jid
   consecutive failures, plus running `turn_context` rows. Needs a
   `Snapshot()` accessor on `queue.GroupQueue` (today only
   `ActiveCount()` is public,
   [`queue/queue.go:361`](../../queue/queue.go)) — in-memory state only
   routd can serve.
2. **`POST /v1/queue/retry`** `{jid}` — zero the jid's failure count +
   `Loop.Enqueue(jid)` ([`routd/loop.go:246`](../../routd/loop.go)).
3. **`DELETE /v1/routing/errored?jid=`** — wrap
   `DeleteErroredMessages` ([`routd/db.go:439`](../../routd/db.go)) +
   clear the chat's errored flag. The DB writes exist; only the handler
   is new.
4. **`GET /v1/outbound/pending`** — read face over `PendingOutbound`
   ([`routd/db.go:384`](../../routd/db.go)).
5. **`GET /v1/cost/summary?folder=`** — today-spend + cap per folder
   (all folders for the operator/root caller). Wraps
   `SpendTodayFolder`/`FolderCap`; `POST /v1/cost` is write-only today
   ([`routd/reads_http.go:274`](../../routd/reads_http.go)).
6. **`GET /v1/engagements`** — list live engagement windows. New DB
   read (today only the point-read `Engaged(jid, topic)` exists).

Scopes: reads under `routes:read` (1, 4, 6) / `cost:read` (5, new
scope); writes under `routes:write` (2, 3). Folder containment per the
existing `authz` + `ownsFolder`/`ownsJID` pattern
([`routd/server.go:287`](../../routd/server.go)) — every endpoint also
serves dashd's cross-cutting pages and MCP parity later, not just this
dashboard (`6/1`: no dashboard-only API).

## 6. Auth

Per `6/1` exactly: proxyd transit gate (`auth:"user"`) +
`RequireSigned` + `auth/dashauth.go` operator gate + same-origin CSRF
on writes. No per-page exceptions — all pages operator-only.

## 7. HTMX fragments

`/dash/routd/x/overview`, `/x/queue`, `/x/breaker`, `/x/channels`,
`/x/errored`, `/x/routes`, `/x/cost`, `/x/engagements` — each returns
the table/pane partial its page polls. Theme tables + detail panes by
name (`6/1` Theme), nothing else.

## 8. Non-goals

`6/1` non-goals apply. routd-specific:

- **No route enable/disable toggle.** `routes` has no disabled column
  ([`routd/api/v1/types.go:206`](../../routd/api/v1/types.go)); the
  affordance is delete/re-add. Adding a column for a UI convenience is
  out of scope.
- **No channel force-reregister/deregister.** Registration is
  adapter-driven (chanlib retries on 401); health auto-deregisters.
  routd cannot make an adapter re-register.
- **No message browsing.** dashd's activity page owns that (`6/2`).
- **No cost time-series.** Today-vs-cap and recent rows only (`6/1`:
  not a metrics system).

## 9. Acceptance

- `GET /dash/routd/` behind proxyd renders the overview for an
  operator; a non-operator gets the themed 403 banner (`6/1` Auth).
- `GET /v1/queue` returns active/waiting/failure state that matches
  `docker ps` + the `/status` chat command during a live run.
- Tripping a breaker (3 consecutive failed turns on one chat) shows the
  chat at threshold on `/dash/routd/queue`; clicking retry
  (`POST /v1/queue/retry`) dispatches a fresh run.
- `DELETE /v1/routing/errored?jid=` removes the chat from
  `GET /v1/routing/errored` and the errored page.
- The channels page shows an adapter's `health_fails` climbing when its
  `/health` 503s, and the row vanishing at 3 (auto-deregister).
- Deleting a route from the routes page is reflected in
  `GET /v1/routes` and the next `pollOnce` resolution.
- Cost page shows a folder at-cap exactly when `budgetGate` refuses its
  turns ([`routd/budget.go:28`](../../routd/budget.go)).
- All page data flows through `/v1` handlers/helpers — no SQL in any
  `/dash/` handler (code-review check, `6/1` read-path).
