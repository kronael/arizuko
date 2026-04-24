# timed

Scheduler daemon: polls `scheduled_tasks`, inserts messages at due times.

## Purpose

Separate process that fires cron/interval tasks. Keeps scheduler logic out
of `gated`. Writes prompts as messages through the shared SQLite DB;
gateway picks them up via normal polling.

## Responsibilities

- Poll `scheduled_tasks` every 60s; atomically claim due rows.
- Insert the task prompt as a message with sender `timed` or `timed-isolated:<id>`.
- Advance `next_run` (`robfig/cron` or interval-ms); mark one-shots completed.
- Log each run to `task_run_logs`.
- Daily `cleanupSpawns`: close idle child groups, archive closed groups as `.tar.gz`.

## Entry points

- Binary: `timed/main.go`
- Listen: `:8080` (health only)
- Config: `DATA_DIR`, `DATABASE`, `TZ`

## Dependencies

- `core` (config)
- Direct SQLite via `modernc.org/sqlite` (no schema ownership — read/write only)

## Configuration

- `DATA_DIR` — resolves `<DATA_DIR>/store/messages.db` if `DATABASE` unset
- `DATABASE` — explicit sqlite DSN
- `TZ` — cron timezone (default `UTC`)

## Health signal

`GET /health` returns 200 when `db.Ping()` succeeds. Red flag:
no `task_run_logs` rows appearing despite active tasks.

## Files

- `main.go` — poll loop, claim-fire-advance, daily cleanup, tar.gz archive.

## Related docs

- `specs/4/8-scheduler-service.md`
- `ARCHITECTURE.md` (Scheduler section)
