# timed

Scheduler daemon: polls routd for due tasks, enqueues messages at due times.

## Purpose

Separate process that fires cron/interval tasks. Keeps scheduler logic out
of `routd`. Drives routd over HTTP; opens NO local DB.

## Responsibilities

- Poll routd's `/v1/tasks/due` every 60s; routd atomically claims + returns due tasks.
- Enqueue the task prompt as a message (`POST /v1/messages`) with sender `timed` or `timed-isolated:<id>`.
- Compute `next_run` client-side (`robfig/cron` or interval-ms).
- Reschedule task (`POST /v1/tasks/{id}/reschedule`); mark one-shots completed.
- Log each run (`POST /v1/tasks/runlog`).

## Entry points

- Binary: `timed/main.go`
- Listen: `:8080` — `/health` (always 200 `ok`) + `GET /openapi.json`
  (`resreg.OpenAPIHandler("timed", ["scheduled_tasks"])`)
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

- `core` (config)
- `auth` (service-token bootstrap)
- `routd` (HTTP client)
- `robfig/cron/v3` (cron parsing)
- `obs` (OTLP setup)
- `resreg` (OpenAPI handler)

## Health signal

`GET /health` returns 200 `ok` always. Red flag: no `task_run_logs` rows
appearing despite active tasks.

## Files

- `main.go` — entry point; validates `ROUTER_URL`, `obs.Setup`, delegates to `runSplit`
- `split.go` — federated fire loop: claim due tasks → enqueue message → log → reschedule
- `split_test.go` / `timed_test.go` — unit tests

## Related docs

- `specs/4/8-scheduler-service.md`
- `ARCHITECTURE.md` (Scheduler section)
