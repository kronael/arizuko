---
status: draft
depends: [1-cockpit-index]
---

# timed dashboard — scheduled tasks, next ticks, recent runs

Architecture, routing, auth, theme: [`6/1`](1-cockpit-index.md). This
spec adds only timed's pages + show/control matrix.

## Purpose

Observe and control the scheduler: every scheduled task, its computed
next tick, lag and stuck-fire detection, and recent run outcomes.

## Ownership split (load-bearing)

routd owns the at-rest rows — `scheduled_tasks` + `task_run_logs` live
in routd.db ([`routd/migrations/0009-tasks.sql`](../../routd/migrations/0009-tasks.sql))
and routd already serves the agent MCP verbs (`schedule_task`,
`pause_task`/`resume_task`, `cancel_task`, task reads —
[`routd/mcp.go:463-468`](../../routd/mcp.go), writers in
[`routd/sibling_db.go:183-189`](../../routd/sibling_db.go)). timed owns
the **fire runtime**: the 60s tick ([`timed/main.go:96`](../../timed/main.go),
split loop [`timed/split.go:66`](../../timed/split.go)), the atomic
claim, and all cron/interval computation (`cronParser`
[`timed/main.go:24`](../../timed/main.go), `computeNextRun`
[`timed/main.go:251`](../../timed/main.go)).

So: `/dash/timed/` renders the fire runtime from in-process state, and
reads/writes task rows through **routd's `/v1/tasks` REST face** (to
add, below) over the same authenticated transit the fire loop already
uses ([`timed/split.go:177`](../../timed/split.go) `call()`, service
token from [`timed/split.go:32`](../../timed/split.go)). No direct DB
(6/1 read-path); in split mode timed opens no DB at all.

## Pages

| Page                     | Content                                                  |
| ------------------------ | -------------------------------------------------------- |
| `/dash/timed/`           | overview: loop health, counts by status, lag/stuck flags |
| `/dash/timed/tasks`      | task table (filter by folder, status)                    |
| `/dash/timed/tasks/{id}` | one task: full row, next-ticks preview, its recent runs  |
| `/dash/timed/runs`       | recent `task_run_logs` across all tasks                  |

## Show

- **Loop health** (overview) — mode (split vs monolith path,
  [`timed/main.go:36`](../../timed/main.go)), timestamp + outcome of
  the last `fire`/`fireSplit` call, tasks fired on last tick. In-memory
  timed state; recorded by the fire loop, rendered in-process.
- **Task table** — `id`, `owner`, `chat_jid`, prompt (truncated),
  `cron`, `next_run`, `status` (`active|firing|completed` per schema
  default + transitions; `paused` via PATCH below), `context_mode`.
- **Next tick** — the stored `next_run` (timed computes it at fire time,
  [`timed/main.go:251`](../../timed/main.go)). The "next ticks" view is
  the task table sorted by `next_run` ascending.
- **Next-ticks preview** (detail page) — the upcoming N fire times for
  a cron task, computed in-process with `cronParser` + `TZ`
  ([`timed/main.go:266`](../../timed/main.go) `nextCron`). Only timed
  can compute this; no `/v1` endpoint needed — it's a pure function of
  the row the page already fetched.
- **Lag** — `now − next_run` for `active` rows whose `next_run` is in
  the past. ≤60s is normal (waiting on the next tick); >2 ticks renders
  the `warn` status dot.
- **Stuck fires** — rows in `firing` older than 2 ticks. A crash
  between claim and fire strands a task in `firing` (hazard documented
  at [`timed/main.go:176-186`](../../timed/main.go)); the overview
  counts these and the table flags them `err`.
- **Recent runs** — `task_run_logs` rows: `run_at`, task, `status`
  (`success|error`), `duration_ms`, `error`.

## Control

| Affordance     | Verb                                                    | Danger                                                                                |
| -------------- | ------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| pause / resume | `PATCH routd /v1/tasks/{id}` `{status}` (to add)        |                                                                                       |
| cancel         | `DELETE routd /v1/tasks/{id}` (to add)                  | `.btn-danger` — irreversible, cascades `task_run_logs` (FK `ON DELETE CASCADE`, 0009) |
| run now        | `PATCH routd /v1/tasks/{id}` `{next_run: now}` (to add) | confirm — fires the prompt into the chat on the next tick (≤60s)                      |

- Run-now is offered only on `active` tasks (a paused task must be
  resumed first — the claim predicate is `status='active'`,
  [`timed/main.go:124`](../../timed/main.go)). No second fire path:
  setting `next_run` to now reuses the one claim-fire loop.
- **Schedule editing is excluded.** The brief allows it "only if timed
  owns it" — timed does not own the rows, and cron validation has no
  single shared site yet (only timed parses cron). If edit ships later
  it's `PATCH {cron}` with timed validating via `cronParser` BEFORE
  submitting (validate-before-persist); until then editing stays with
  the agent tools / re-create.

## Required `/v1` work

**routd** (row owner; these REST faces mirror its existing MCP task
verbs, so they're owed by `specs/5/5` MCP↔REST symmetry anyway):

- `GET /v1/tasks?folder=&status=` — list (`tasks:read`; backs onto
  `ListTasks`, [`routd/sibling_db.go:25`](../../routd/sibling_db.go))
- `GET /v1/tasks/{id}` — one row (`tasks:read`; `GetTask`,
  [`routd/sibling_db.go:179`](../../routd/sibling_db.go))
- `GET /v1/tasks/{id}/runs?limit=` — run history (`tasks:read`;
  `TaskRunLogs`, [`routd/sibling_db.go:208`](../../routd/sibling_db.go))
- `GET /v1/tasks/runs?limit=` — cross-task recent runs (`tasks:read`;
  small new read, backs the `/dash/timed/runs` page)
- `PATCH /v1/tasks/{id}` `{status|next_run}` — pause/resume/run-now
  (`tasks:write`; `SetTaskStatus` [`routd/sibling_db.go:186`](../../routd/sibling_db.go)
  - the existing reschedule writer)
- `DELETE /v1/tasks/{id}` — cancel (`tasks:write`; `DeleteTask`,
  [`routd/sibling_db.go:189`](../../routd/sibling_db.go))

Existing and unchanged (the timed-facing fire-loop slice,
[`routd/server.go:255-257`](../../routd/server.go)):
`GET /v1/tasks/due`, `POST /v1/tasks/runlog`,
`POST /v1/tasks/{id}/reschedule`.

**timed-local**: none beyond the `/dash/timed/` handlers themselves.
Next-tick preview and loop health are in-process; the fire loop records
last-tick timestamp/outcome into a small mutex-guarded struct the
overview reads.

## Auth

Per `6/1`: proxyd `auth: "user"` transit on `/dash/timed/`, daemon-side
`auth/middleware.go` `RequireSigned` + `auth/dashauth.go` operator gate,
CSRF on writes. Onward calls to routd reuse timed's `service:timed`
bearer; routd's `tasks:read`/`tasks:write` scope gate applies
([`routd/server.go:752`](../../routd/server.go) `authed`). Caveat,
accepted: routd audit rows for dashboard writes attribute to
`service:timed`, not the operator — the operator decision is recorded
at timed's dash gate. Forwarding operator identity end-to-end is future
`specs/5/1` capability-token work, not blocked on here.

## HTMX fragments

- `GET /dash/timed/x/overview` — loop health + counts (poll 10s)
- `GET /dash/timed/x/tasks?folder=&status=` — task table body
- `GET /dash/timed/x/tasks/{id}/runs` — run history rows
- `GET /dash/timed/x/runs` — cross-task run feed (poll 30s)
- `POST /dash/timed/x/tasks/{id}/{pause|resume|fire}` and
  `DELETE /dash/timed/x/tasks/{id}` — control forms; each handler is a
  thin forward to the routd `/v1` verb above, returns the refreshed row

## Non-goals

Per `6/1`. Additionally: no task creation from the dashboard (creation
stays with `schedule_task` / chat); no schedule (cron) editing (above);
no run-log retention management; no second fire path.

## Acceptance

- `/dash/timed/` shows last-tick time/outcome and per-status counts
  matching `SELECT status, COUNT(*)` on routd.db; timed itself opened
  no DB (split mode).
- An `active` task with past `next_run` older than 2 ticks renders the
  lag warning; a row stuck in `firing` renders the stuck flag.
- Pause from the dashboard → row `status='paused'`, task skipped by the
  next claim; resume → fires when due.
- Run-now on an active task → prompt message lands in the chat within
  one tick; a `task_run_logs` row appears.
- Cancel asks for confirmation, then the row and its run logs are gone.
- All reads/writes traverse `routd /v1/tasks*`; non-operator hits 403
  with the theme error banner.
