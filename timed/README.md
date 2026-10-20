# timed

Scheduler daemon: polls routd for due tasks, enqueues messages at due times.

## Purpose

Separate process that fires cron/interval tasks. Keeps scheduler logic out
of `routd`. Drives routd over HTTP; opens NO local DB.

## Responsibilities

- Poll routd's `/v1/tasks/due` every 60s; routd atomically claims + returns due tasks.
- Enqueue the task prompt as a message (`POST /v1/messages`) with sender `timed` or `timed-isolated:<id>`.
- Compute `next_run` client-side through `core.NextRun`, the one reader of a
  task's cron value.
- Reschedule task (`POST /v1/tasks/{id}/reschedule`); mark one-shots completed.
- Log each run (`POST /v1/tasks/runlog`).

### One fire, one message id

The enqueued id is `sched-<taskID>@<next_run>` (`fireID`), keyed on the
SCHEDULED time the claim fires for — carried on the `/v1/tasks/due` row. Every
attempt at the same fire therefore mints the same id, and routd's id-PK dedup
collapses them. This matters on the error path: when the `POST /v1/messages`
response is lost but routd recorded the row, a one-shot is re-armed to the
next_run it was CLAIMED at (not a fresh `now+1m`), the next 60s tick re-claims
the same fire, and routd drops the duplicate. A wall-clock id fired the task
twice; a bare `sched-<taskID>` would instead silently swallow a legitimately
re-armed one-shot. BUGS `L33`.

## Entry points

- Binary: `timed/main.go`
- Listen: `:8080` — three routes, registered in `runSplit`:
  - `GET /health` — always 200 `ok`
  - `GET /openapi.json` — `resreg.OpenAPIHandler("timed", mux)`, empty by
    derivation: timed mounts no resource, it is a CLIENT of routd's
    `scheduled_tasks`
  - `GET /dash/timed/` — the fire-loop overview, operator-only. timed opens
    no store, so the task rows come from routd's `GET /v1/tasks` over the
    same `service:timed` bearer the fire loop uses; only loop health is
    timed's own memory. The page flags a row by its lag against
    `next_run`: an `active` row more than two ticks, 2 min, past due reads
    `lagging`, and a row `firing` for more than two ticks reads `stuck`
    (`dash.go`, `twoTicks`). One tick of lag is normal
- Config: `ROUTER_URL`, `AUTHD_URL`, `AUTHD_SERVICE_KEY`, `TZ`

## Configuration

Env vars:

- `ROUTER_URL` — required; routd address (e.g. `http://routd:8080`)
- `AUTHD_URL` / `AUTHD_SERVICE_KEY` — service-token boot-exchange; required for routd auth
- `TZ` — cron timezone (default `UTC`)
- `ARIZUKO_INSTANCE` — instance name (used by `obs.Setup` for OTLP)

timed exchanges `AUTHD_SERVICE_KEY` for a `service:timed` ES256 token at boot
(`auth.ServiceToken`) and presents it on every routd call. If
`AUTHD_URL`/`AUTHD_SERVICE_KEY` are unset, routd's bearer gate denies all calls.

## Dependencies

- `core` (config, `NextRun` — the cron-to-next-instant reader)
- `auth` (service-token bootstrap)
- `routd` (HTTP client)
- `obs` (OTLP setup)
- `resreg` (OpenAPI handler)

## Health signal

`GET /health` returns 200 `ok` always. Red flag: no `task_run_logs` rows
appearing despite active tasks.

## Files

- `main.go` — entry point; validates `ROUTER_URL`, `obs.Setup`, delegates to `runSplit`
- `split.go` — the mux and the federated fire loop: claim due tasks → enqueue
  message → log → reschedule
- `dash.go` — `dashServer`, the `/dash/timed/` renderer

## Related docs

- `specs/4/8-scheduler-service.md`
- `ARCHITECTURE.md` (Scheduler section)

## Coverage

`go test ./timed/ -short -cover` reports 61.5% (2026-09-13). The scheduler
logic, the federated router client and the `/dash/timed/`
renderer are covered. The number stops there because `main` and
`runSplit` are the rest of the package: they exchange a service token over the
network, `http.ListenAndServe`, and `os.Exit(1)` on every failure — a test that
reaches one takes the test binary with it. That path is `make test-e2e`'s.
