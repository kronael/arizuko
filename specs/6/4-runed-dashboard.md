---
status: draft
depends: [1-cockpit-index]
---

# runed — dashboard

Architecture, routing, auth, theme, and the read-path rule live in
[`6/1`](1-cockpit-index.md). This spec adds only runed's page list,
show/control matrix, and the `/v1` work it requires.

## 1. Purpose

Operator view + control over the execution plane: live spawns, run
history, capacity/saturation, and the per-spawn brokered tokens —
with kill as the one dangerous control.

## 2. Pages

All under `/dash/runed/` on `:8080` (proxyd route `/dash/runed/` →
`runed:8080/dash/`; runed ships no service TOML, so the route entry
goes in the static core list in `compose/compose.go`, per `6/1`
Routing — the entry IS the registration).

| Page       | Path                    | Covers                                                |
| ---------- | ----------------------- | ----------------------------------------------------- |
| overview   | `/dash/runed/`          | capacity gauges + breaker + broker/GC config          |
| runs       | `/dash/runed/runs`      | active runs AND history — one table, `?state=` filter |
| run detail | `/dash/runed/runs/{id}` | spawn + session_log + token detail pane               |
| tokens     | `/dash/runed/tokens`    | brokered-token issuance history                       |

The brief's "capacity" merges into overview (capacity IS the overview:
`activeCount`/`maxRun`/waiting are one `Manager` struct,
[`runed/manager.go:37`](../../runed/manager.go)), and "active runs" /
"run history" are one renderer: both are `spawns` rows distinguished
only by `state IN ('queued','running')` — two pages would be two
renderers over one table.

## 3. Show

**overview** — live `Manager` state only the daemon can serve (`6/1`
read-path argument) + static config:

- capacity gauge: `activeCount` / `maxRun`
  (`MAX_CONCURRENT_CONTAINERS`, default 5,
  [`runed/manager.go:20`](../../runed/manager.go),
  [`core/config.go:157`](../../core/config.go))
- waiting FIFO: folders queued for admission (`Manager.waiting`,
  [`runed/manager.go:41`](../../runed/manager.go))
- breaker: per-folder consecutive-failure counts
  (`Manager.failures`, threshold 3,
  [`runed/manager.go:18`](../../runed/manager.go)); folders AT the
  threshold flagged. Inline note: a new inbound auto-resets
  ([`runed/manager.go:101`](../../runed/manager.go)) and a tripped run
  reports `breaker_open` on its outcome to routd
  ([`runed/manager.go:244`](../../runed/manager.go)) — the routd-side
  consequence renders on `6/3`.
- config strip: run ceiling `RUNED_RUN_TIMEOUT` (= `runTTL`, broker
  token TTL, kill deadline), broker mode (authd live token vs static
  fallback, [`runed/broker.go:25`](../../runed/broker.go)), GC cadence
  (hourly sweep, 7-day spawn retention,
  [`runed/cmd/runed/main.go:100`](../../runed/cmd/runed/main.go)).

**runs** — `spawns` rows ([`runed/db.go:170`](../../runed/db.go)):
run_id, folder, topic, state (`queued|running|exited|error|killed`),
outcome (`ok|silent|error`), exit_code, steered, container_name,
created/started/ended + computed age. Default filter
`state=queued,running` (the active view); `?state=all` is the history
view. Newest first.

**run detail** — the `RunStatus` shape
([`runed/api/v1/types.go:69`](../../runed/api/v1/types.go)) plus the
linked `session_log` row (result, error, message_count, session_id —
[`runed/db.go:131`](../../runed/db.go)) and the spawn's token jti.
Kill button here and on the active-runs rows.

**tokens** — `mcp_tokens` rows ([`runed/db.go:274`](../../runed/db.go)):
jti, run_id, folder, scope (the downscoped set, ceiling ∩ requested —
[`runed/manager.go:187`](../../runed/manager.go)), issued_at,
expires_at, parent (`service:runed`), expired flagged. Never a raw JWS
— runed persists only the ref ([`runed/db.go:274`](../../runed/db.go)).

## 4. Control

| Affordance             | Verb                            | Exists?                                                             | Danger        |
| ---------------------- | ------------------------------- | ------------------------------------------------------------------- | ------------- |
| kill run               | `DELETE /v1/runs/{run_id}`      | yes ([`runed/server.go:41`](../../runed/server.go), handler `:169`) | `.btn-danger` |
| stop folder's live run | `POST /v1/runs/stop` `{folder}` | yes ([`runed/server.go:39`](../../runed/server.go))                 | `.btn-danger` |

Kill path verified: `DELETE /v1/runs/{run_id}` → `Manager.Kill`
([`runed/manager.go:334`](../../runed/manager.go)) — stop→kill→`rm -f`
on the container, `state='killed'` (sticky, never clobbered back to
error, [`runed/db.go:228`](../../runed/db.go)), does NOT count toward
the breaker (operator intent, not a run failure), idempotent on an
already-exited run. The kill confirm dialog states the agent's turn is
lost mid-flight (routd's re-feed + `turn_results` dedup governs what
the chat sees).

**"Revoke token" is kill-run.** runed mints nothing and has no
revocation primitive; the brokered token's TTL is the run ceiling
(`runTTL`, default 20m) and the only thing holding it is the container
— killing the run is the effective revoke. True jti revocation is an
authd concern (`6/5`); duplicating it here would be a second renderer
over authd's resource. The tokens page links a live token's run to its
kill affordance instead of growing a revoke verb.

Stale-run cleanup is automatic (hourly `SweepExpired`, 7-day
retention) — no manual sweep verb; see Non-goals.

## 5. Required `/v1` work

1. **`GET /v1/runs?state=&folder=&limit=`** — list spawns. No list
   endpoint exists (only `GET /v1/runs/{run_id}`,
   [`runed/server.go:40`](../../runed/server.go)) and no list query in
   the DB layer — add `DB.ListSpawns(state, folder, limit)` beside
   `GetSpawn` ([`runed/db.go:242`](../../runed/db.go)). Serves both
   the active view and history. Scope: `runs:read` (new; mirrors
   `sessions:read`). Folder-scoped tokens see their subtree only
   (`ownsFolder`, [`runed/server.go:73`](../../runed/server.go));
   the operator/service caller (folder="") sees all.
2. **`GET /v1/capacity`** — Manager snapshot: active_count, max,
   waiting folders (FIFO order), per-folder failure counts, runTTL.
   Needs a `Manager.Snapshot()` accessor (today only `ActiveCount()` /
   `ActiveRunID()` are public,
   [`runed/manager.go:376`](../../runed/manager.go)) — in-memory state
   no DB read can see. Scope: `runs:read`.
3. **`GET /v1/tokens?folder=&limit=`** — mcp_tokens read; add
   `DB.ListTokens` (only `RecordToken` exists,
   [`runed/db.go:274`](../../runed/db.go)). Returns refs only (jti,
   scope, expiry), never a JWS. Scope: `tokens:read` (new).

All three also serve dashd cross-pages and future MCP parity — not a
dashboard-only API (`6/1`).

## 6. Auth

Per `6/1` exactly: proxyd transit gate (`auth:"user"`) +
`RequireSigned` + `auth/dashauth.go` operator gate + same-origin CSRF
on the kill/stop writes. No per-page exceptions. The `/v1` additions
above keep runed's existing bearer+scope gate (`authz`,
[`runed/server.go:50`](../../runed/server.go)) — the dashboard is a
third face on the same handlers, not a bypass.

## 7. HTMX fragments

`/dash/runed/x/capacity`, `/x/runs`, `/x/run/{id}`, `/x/tokens` —
table/pane partials the pages poll. The runs fragment carries the
`?state=` filter through. Theme tables + detail panes by name (`6/1`).

## 8. Non-goals

`6/1` non-goals apply. runed-specific:

- **No token revocation verb** — kill-run is the revoke (above); jti
  revocation belongs to authd (`6/5`).
- **No manual GC/sweep verb** — the hourly `SweepExpired` self-heals
  ([`runed/cmd/runed/main.go:100`](../../runed/cmd/runed/main.go));
  a button would duplicate a timer.
- **No container log streaming** — the docker log driver is `none`
  (journald is the log plane, per CLAUDE.md); the `spawn_logs` table
  exists in schema but nothing writes it — don't render an empty pane.
- **No run launching** — `POST /v1/runs` stays routd's contract;
  spawning agents from a browser is not an operator affordance.

## 9. Acceptance

- `GET /dash/runed/` behind proxyd renders capacity for an operator;
  non-operator gets the themed 403 banner (`6/1` Auth).
- During a live agent turn, `/dash/runed/runs` shows the spawn
  `state=running` with its container name matching
  `sudo docker ps --filter name=arizuko-`.
- Kill from the run detail flips the row to `state=killed` within one
  fragment poll, the container is gone from `docker ps`, and the
  folder's breaker count is unchanged.
- `GET /v1/capacity` active_count matches the rendered gauge and
  `Manager.ActiveCount()` under concurrent runs; queued folders appear
  in the waiting list while at the cap.
- Three consecutive failed runs in one folder show that folder at the
  breaker threshold on the overview; the next inbound clears it.
- The tokens page lists one row per spawn with `expires_at` ≈
  started_at + runTTL, and no response anywhere contains a JWS.
- A folder-scoped token calling `GET /v1/runs` sees only its subtree
  (containment test mirrors `authz_containment_test.go`).
- All page data flows through `/v1` handlers/helpers — no SQL in any
  `/dash/` handler (code-review check, `6/1` read-path).
