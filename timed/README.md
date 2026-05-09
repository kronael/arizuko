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

## Tables owned

`scheduled_tasks`, `task_run_logs`. gated runs the migrations today, but
timed is the only daemon that mutates these rows. Other daemons must go
through timed's API.

## Entry points

- Binary: `timed/main.go`
- Listen: `:8080` (health only today; `/v1/tasks` planned)
- Config: `DATA_DIR`, `DATABASE`, `TZ`

## Dependencies

- `core` (config)
- `auth` (token verification on `/v1/*`, planned)
- Direct SQLite via `modernc.org/sqlite` (no schema ownership — read/write only)

## Planned `/v1/tasks` surface

Per `specs/6/7-platform-api.md`, timed will serve `/v1/tasks` as the
canonical control surface for `scheduled_tasks`:

- `GET /v1/tasks` — list (paginated, filtered by folder, status, kind)
- `GET /v1/tasks/{id}` — get one
- `POST /v1/tasks` — create
- `PATCH /v1/tasks/{id}` — partial update (pause/resume → `status`)
- `DELETE /v1/tasks/{id}` — delete

`task_run_logs` is exposed as a sub-resource: `GET /v1/tasks/{id}/runs`
or via `GET /v1/tasks/runs?task_id=…` filter. Not separately writable.

## Token contract (planned)

timed validates JWTs minted by `auth.Mint` — no local issuance. The
same token format is used regardless of issuer:

- proxyd-issued user-session tokens (operator hitting dashd → timed)
- MCP-host-issued agent capability tokens (agent calling
  `pause_task` over MCP, HTTP-forwarded into timed)

Per request, timed runs:

```go
ident, err := auth.VerifyHTTP(r)
if !auth.HasScope(ident, "tasks", verb) { return 403 }   // read|write
if !auth.MatchesFolder(ident, taskFolder) { return 403 }
```

Scopes: `tasks:read` for GET, `tasks:write` for POST/PATCH/DELETE.
`folder` claim must cover the target task's folder; root tokens
(`folder: "*"`) bypass.

## Cross-daemon flow

Agent-side scheduler tools (`schedule_task`, `pause_task`, `list_tasks`)
become HTTP forwards from the MCP host into timed:

```
agent → ipc.tools/call(pause_task, id=42)
       → ipc verifies agent token has tasks:write + folder match
       → ipc HTTP-PATCH timed/v1/tasks/42 {status:"paused"}
              Authorization: Bearer <agent-cap-token>
       → timed verifies the same token, executes, returns
```

Local in-process calls go away once `/v1/tasks` ships; the MCP host
becomes a thin forwarder for task ops.

## Configuration

- `DATA_DIR` — resolves `<DATA_DIR>/store/messages.db` if `DATABASE` unset
- `DATABASE` — explicit sqlite DSN
- `TZ` — cron timezone (default `UTC`)

## Health signal

`GET /health` returns 200 when `db.Ping()` succeeds. Red flag:
no `task_run_logs` rows appearing despite active tasks.

## Files

- `main.go` — poll loop, claim-fire-advance.

## Related docs

- `specs/4/8-scheduler-service.md`
- `specs/6/7-platform-api.md` (full `/v1/*` contract, token model)
- `ARCHITECTURE.md` (Scheduler section)
